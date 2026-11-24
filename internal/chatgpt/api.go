package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// APIError is a failed API call. It keeps the HTTP status, the documented
// error code when present, and the request ID for diagnosis.
type APIError struct {
	Status    int
	Code      string
	Param     string
	Message   string
	RequestID string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ChatGPT API error (HTTP %d", e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, ", %s", e.Code)
	}
	b.WriteString(")")
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.Param != "" {
		b.WriteString(" [param " + e.Param + "]")
	}
	if e.RequestID != "" {
		b.WriteString(" [request " + e.RequestID + "]")
	}
	if hint := e.Hint(); hint != "" {
		b.WriteString(". " + hint)
	}
	return b.String()
}

// Hint is the documented recovery for the error, if any.
func (e *APIError) Hint() string {
	switch e.Code {
	case "subscription_sharing_usage_limit_exceeded":
		return "ChatGPT plan usage limit reached. Manage usage: " + ManageUsageURL
	case "subscription_sharing_user_not_eligible":
		return "ChatGPT plan usage is not available for this account or workspace"
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return "Temporarily unavailable; try again later"
	case "subscription_sharing_invalid_user":
		return "Sign in again: rsch login chatgpt"
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return "Check the signed-in account and granted scopes (rsch chatgpt status)"
	case http.StatusForbidden:
		return "A policy or permission check prevented the request"
	}
	return ""
}

func apiError(status int, body []byte) error {
	e := &APIError{Status: status}
	var structured struct {
		Error *struct {
			Code    string `json:"code"`
			Param   string `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &structured) == nil {
		if structured.Error != nil {
			e.Code, e.Param, e.Message = structured.Error.Code, structured.Error.Param, structured.Error.Message
		} else if structured.Detail != "" {
			e.Message = structured.Detail // diagnostic text, not a stable code
		}
	} else if text := strings.TrimSpace(string(body)); text != "" && len(text) < 500 {
		e.Message = text
	}
	return e
}

// Model is one entry of the account's model catalog.
type Model struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
}

// ListModels returns the models intended for display (visibility "list"),
// in the server's order, for the signed-in account.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	token, _, err := c.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Models []Model `json:"models"`
	}
	if err := c.getJSON(ctx, strings.TrimRight(c.Endpoints.APIBase, "/")+"/models", token, &catalog); err != nil {
		return nil, err
	}
	var listed []Model
	for _, m := range catalog.Models {
		if m.Visibility == "list" {
			listed = append(listed, m)
		}
	}
	return listed, nil
}

// ResponseRequest is a text request to the Responses API.
type ResponseRequest struct {
	Model        string
	Input        string
	Instructions string
}

// ResponseResult is a completed response.
type ResponseResult struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
}

// StreamResponse sends a request to POST /v1/responses with store=false and
// stream=true, as the docs require, calls onDelta for each text delta, and
// succeeds only after response.completed.
func (c *Client) StreamResponse(ctx context.Context, request ResponseRequest, onDelta func(string)) (ResponseResult, error) {
	var result ResponseResult
	token, _, err := c.AccessToken(ctx)
	if err != nil {
		return result, err
	}
	body := map[string]any{
		"model":  request.Model,
		"input":  []map[string]string{{"role": "user", "content": request.Input}},
		"store":  false,
		"stream": true,
	}
	if request.Instructions != "" {
		body["instructions"] = request.Instructions
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoints.APIBase, "/")+"/responses", bytes.NewReader(payload))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		apiErr := apiError(resp.StatusCode, data).(*APIError)
		apiErr.RequestID = resp.Header.Get("x-request-id")
		return result, apiErr
	}

	var text strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Usage struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		switch event.Type {
		case "response.output_text.delta":
			text.WriteString(event.Delta)
			if onDelta != nil {
				onDelta(event.Delta)
			}
		case "response.completed":
			result.Text = text.String()
			result.InputTokens = event.Response.Usage.InputTokens
			result.OutputTokens = event.Response.Usage.OutputTokens
			return result, nil
		case "response.failed":
			apiErr := &APIError{Status: http.StatusOK, Code: "unknown_error", RequestID: resp.Header.Get("x-request-id")}
			if event.Response.Error != nil {
				apiErr.Code, apiErr.Message = event.Response.Error.Code, event.Response.Error.Message
			}
			return result, apiErr
		case "response.incomplete":
			reason := "unknown"
			if event.Response.IncompleteDetails != nil {
				reason = event.Response.IncompleteDetails.Reason
			}
			return result, fmt.Errorf("response incomplete: %s", reason)
		case "error":
			return result, &APIError{Status: http.StatusOK, Code: event.Code, Message: event.Message, RequestID: resp.Header.Get("x-request-id")}
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("response stream interrupted: %w", err)
	}
	return result, errors.New("response stream ended without response.completed")
}
