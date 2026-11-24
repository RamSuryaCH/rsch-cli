package format

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// OutputFormat represents the output format type for non-interactive mode
type OutputFormat string

const (
	// Text format outputs the AI response as plain text.
	Text OutputFormat = "text"

	// JSON format outputs the AI response wrapped in a JSON object.
	JSON OutputFormat = "json"

	// JSONL emits a versioned lifecycle event on each line. It is intended for
	// long-running automation that needs an unambiguous terminal record.
	JSONL OutputFormat = "jsonl"
)

// String returns the string representation of the OutputFormat
func (f OutputFormat) String() string {
	return string(f)
}

// SupportedFormats is a list of all supported output formats as strings
var SupportedFormats = []string{
	string(Text),
	string(JSON),
	string(JSONL),
}

// Parse converts a string to an OutputFormat
func Parse(s string) (OutputFormat, error) {
	s = strings.ToLower(strings.TrimSpace(s))

	switch s {
	case string(Text):
		return Text, nil
	case string(JSON):
		return JSON, nil
	case string(JSONL):
		return JSONL, nil
	default:
		return "", fmt.Errorf("invalid format: %s", s)
	}
}

// IsValid checks if the provided format string is supported
func IsValid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// GetHelpText returns a formatted string describing all supported formats
func GetHelpText() string {
	return fmt.Sprintf(`Supported output formats:
- %s: Plain text output (default)
- %s: One versioned JSON result object
- %s: Versioned newline-delimited lifecycle events`,
		Text, JSON, JSONL)
}

// FormatOutput formats the AI response according to the specified format
func FormatOutput(content string, formatStr string) string {
	format, err := Parse(formatStr)
	if err != nil {
		// Default to text format on error
		return content
	}

	switch format {
	case JSON:
		return formatAsJSON(content)
	case JSONL:
		return formatAsJSON(content)
	case Text:
		fallthrough
	default:
		return content
	}
}

const ExecutionSchemaVersion = "rsch.exec.v1"

// ExecutionUsage is the aggregate usage associated with a completed prompt.
// All fields use explicit units so consumers never need provider-specific
// knowledge to interpret the record.
type ExecutionUsage struct {
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// ExecutionEvent is the stable machine contract for prompt execution. JSON
// mode writes one terminal event; JSONL mode writes a started event followed
// by exactly one terminal event.
type ExecutionEvent struct {
	SchemaVersion string         `json:"schemaVersion"`
	Type          string         `json:"type"`
	Sequence      int            `json:"sequence"`
	Timestamp     time.Time      `json:"timestamp"`
	Status        string         `json:"status"`
	SessionID     string         `json:"sessionId"`
	GoalID        string         `json:"goalId,omitempty"`
	TaskID        string         `json:"taskId,omitempty"`
	MessageID     string         `json:"messageId,omitempty"`
	Model         string         `json:"model,omitempty"`
	Provider      string         `json:"provider,omitempty"`
	FinishReason  string         `json:"finishReason,omitempty"`
	Response      string         `json:"response"`
	Structured    any            `json:"structuredOutput,omitempty"`
	Usage         ExecutionUsage `json:"usage"`
	DurationMS    int64          `json:"durationMs"`
	Error         string         `json:"error,omitempty"`
}

// WriteExecutionEvent writes exactly one compact JSON record and a trailing
// newline. Using an explicit writer keeps stdout ownership with the caller and
// makes broken pipes observable as command failures.
func WriteExecutionEvent(w io.Writer, event ExecutionEvent) error {
	if w == nil {
		return errors.New("execution output writer is required")
	}
	if event.SchemaVersion == "" {
		event.SchemaVersion = ExecutionSchemaVersion
	}
	return json.NewEncoder(w).Encode(event)
}

// formatAsJSON wraps the content in a simple JSON object
func formatAsJSON(content string) string {
	// Use the JSON package to properly escape the content
	response := struct {
		Response string `json:"response"`
	}{
		Response: content,
	}

	jsonBytes, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		// In case of an error, return a manually formatted JSON
		jsonEscaped := strings.Replace(content, "\\", "\\\\", -1)
		jsonEscaped = strings.Replace(jsonEscaped, "\"", "\\\"", -1)
		jsonEscaped = strings.Replace(jsonEscaped, "\n", "\\n", -1)
		jsonEscaped = strings.Replace(jsonEscaped, "\r", "\\r", -1)
		jsonEscaped = strings.Replace(jsonEscaped, "\t", "\\t", -1)

		return fmt.Sprintf("{\n  \"response\": \"%s\"\n}", jsonEscaped)
	}

	return string(jsonBytes)
}
