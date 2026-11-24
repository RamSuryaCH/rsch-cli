package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type ToolInfo struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Parameters    map[string]any `json:"parameters,omitempty"`
	Required      []string       `json:"required,omitempty"`
	OutputSchema  map[string]any `json:"outputSchema,omitempty"`
	Permissions   []string       `json:"permissions,omitempty"`
	Risk          string         `json:"risk,omitempty"`
	Timeout       time.Duration  `json:"timeout,omitempty"`
	ExecutionMode string         `json:"executionMode,omitempty"`
	Capabilities  []string       `json:"capabilities,omitempty"`
}

type toolResponseType string

type (
	sessionIDContextKey string
	messageIDContextKey string
	taskIDContextKey    string
)

const (
	ToolResponseTypeText  toolResponseType = "text"
	ToolResponseTypeImage toolResponseType = "image"

	// MaxToolMediaBytes bounds the total native media retained for one tool
	// result. Tool output is persisted in the conversation history, so keeping
	// this limit at the tool boundary prevents screenshots from growing the
	// database and every subsequent provider request without bound.
	MaxToolMediaBytes = 4 << 20

	SessionIDContextKey sessionIDContextKey = "session_id"
	MessageIDContextKey messageIDContextKey = "message_id"
	TaskIDContextKey    taskIDContextKey    = "task_id"
)

// ToolMedia carries native media returned by a tool. Content remains the
// required text fallback for models that cannot accept attachments.
type ToolMedia struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

type ToolResponse struct {
	Type     toolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	Media    []ToolMedia      `json:"media,omitempty"`
	IsError  bool             `json:"is_error"`
}

func NewTextResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
	}
}

func WithResponseMetadata(response ToolResponse, metadata any) ToolResponse {
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return response
		}
		response.Metadata = string(metadataBytes)
	}
	return response
}

// WithResponseMedia attaches defensive copies of media up to the per-result
// byte limit. Oversized or empty entries are omitted; callers must always put
// a useful textual fallback in Content.
func WithResponseMedia(response ToolResponse, media ...ToolMedia) ToolResponse {
	remaining := MaxToolMediaBytes
	for _, item := range media {
		if item.MIMEType == "" || len(item.Data) == 0 || len(item.Data) > remaining {
			continue
		}
		data := append([]byte(nil), item.Data...)
		response.Media = append(response.Media, ToolMedia{MIMEType: item.MIMEType, Data: data})
		remaining -= len(data)
	}
	return response
}

func NewTextErrorResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
		IsError: true,
	}
}

// NewTextErrorResponseWithError preserves a tool's transport error while also
// returning a model-visible error response. Tool dispatchers must not have to
// guess that an empty response paired with an error is a failed tool call.
func NewTextErrorResponseWithError(err error) (ToolResponse, error) {
	if err == nil {
		err = fmt.Errorf("tool execution failed")
	}
	return NewTextErrorResponse(err.Error()), err
}

// DecodeToolInput performs strict JSON decoding and verifies that required
// fields were actually present. Presence is intentionally distinct from a
// zero value: an explicit empty string is valid for tools such as file write.
func DecodeToolInput(input string, target any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &fields); err != nil {
		return fmt.Errorf("invalid parameters: %w", err)
	}
	if fields == nil {
		return fmt.Errorf("invalid parameters: expected an object")
	}
	for _, name := range required {
		value, ok := fields[name]
		if !ok || string(value) == "null" {
			return fmt.Errorf("%s is required", name)
		}
	}

	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid parameters: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid parameters: multiple JSON values")
		}
		return fmt.Errorf("invalid parameters: %w", err)
	}
	return nil
}

func contentSHA256(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

// verifyFileSnapshot closes the approval-time race between presenting a diff
// and applying it. A modification is detected by content, even when filesystem
// timestamp resolution would make a modtime-only check miss it.
func verifyFileSnapshot(path string, expected []byte) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("file changed after approval was requested: %w", err)
	}
	if contentSHA256(current) != contentSHA256(expected) {
		return fmt.Errorf("file changed after approval was requested; read it again before modifying it")
	}
	return nil
}

func verifyFileStillAbsent(path string) error {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to verify new file path after approval: %w", err)
	}
	return fmt.Errorf("file was created after approval was requested; read it before modifying it")
}

type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type BaseTool interface {
	Info() ToolInfo
	Run(ctx context.Context, params ToolCall) (ToolResponse, error)
}

func GetContextValues(ctx context.Context) (string, string) {
	sessionID := ctx.Value(SessionIDContextKey)
	messageID := ctx.Value(MessageIDContextKey)
	if sessionID == nil {
		return "", ""
	}
	if messageID == nil {
		return sessionID.(string), ""
	}
	return sessionID.(string), messageID.(string)
}

func GetTaskID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(TaskIDContextKey).(string)
	return value
}
