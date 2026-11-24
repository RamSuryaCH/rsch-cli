package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/browser"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/visual"
)

const BrowserToolName = "browser"

type BrowserParams struct {
	Operation string `json:"operation"`
	URL       string `json:"url,omitempty"`
	Selector  string `json:"selector,omitempty"`
	Text      string `json:"text,omitempty"`
	Script    string `json:"script,omitempty"`
	Args      []any  `json:"args,omitempty"`
	Browser   string `json:"browser,omitempty"`
}

type browserTool struct {
	permissions permission.Service
	gateOnce    sync.Once
	gate        chan struct{}
	client      *browser.Client
}

func NewBrowserTool(permissions permission.Service) BaseTool {
	return &browserTool{permissions: permissions}
}

func (b *browserTool) Info() ToolInfo {
	return ToolInfo{
		Name:        BrowserToolName,
		Description: "Control a policy-validated WebDriver session for UI verification and browser automation.",
		Parameters: map[string]any{
			"operation": map[string]any{"type": "string", "enum": []string{"start", "navigate", "dom", "console", "screenshot", "find", "click", "type", "evaluate", "close"}},
			"url":       map[string]any{"type": "string"},
			"selector":  map[string]any{"type": "string"},
			"text":      map[string]any{"type": "string"},
			"script":    map[string]any{"type": "string"},
			"args":      map[string]any{"type": "array"},
			"browser":   map[string]any{"type": "string"},
		},
		Required: []string{"operation"},
	}
}

func (b *browserTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return NewTextErrorResponseWithError(err)
	}
	var params BrowserParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid browser parameters: " + err.Error()), nil
	}
	if params.Operation == "" {
		return NewTextErrorResponse("operation is required"), nil
	}

	// Serialize the shared WebDriver session without trapping canceled calls
	// behind another operation's network request or interactive approval.
	b.gateOnce.Do(func() { b.gate = make(chan struct{}, 1) })
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-ctx.Done():
		return NewTextErrorResponseWithError(ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return NewTextErrorResponseWithError(err)
	}
	if params.Operation != "close" {
		if err := b.authorize(ctx, params); err != nil {
			return ToolResponse{}, err
		}
	}

	switch params.Operation {
	case "start":
		return b.start(ctx, params)
	case "navigate":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		if err := client.Navigate(ctx, params.URL); err != nil {
			return ToolResponse{}, err
		}
		return NewTextResponse("browser navigated to " + params.URL), nil
	case "dom":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		dom, err := client.DOM(ctx)
		if err != nil {
			return ToolResponse{}, err
		}
		return NewTextResponse(dom), nil
	case "console":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		logs, err := client.Console(ctx)
		if err != nil {
			return ToolResponse{}, err
		}
		data, _ := json.Marshal(logs)
		return NewTextResponse(string(data)), nil
	case "screenshot":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		screenshot, err := client.Screenshot(ctx)
		if err != nil {
			return ToolResponse{}, err
		}
		visualReport, err := visual.AnalyzeBytes(screenshot.PNG)
		if err != nil {
			return ToolResponse{}, err
		}
		reportJSON, err := json.Marshal(visualReport)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("encode screenshot visual analysis: %w", err)
		}
		response := WithResponseMetadata(NewTextResponse("browser screenshot captured\nDeterministic visual evidence: "+string(reportJSON)), map[string]any{
			"mimeType": "image/png",
			"bytes":    len(screenshot.PNG),
			"visual":   visualReport,
		})
		if len(screenshot.PNG) <= MaxToolMediaBytes {
			response = WithResponseMedia(response, ToolMedia{MIMEType: "image/png", Data: screenshot.PNG})
		} else {
			response.Content += fmt.Sprintf("\nNative image omitted: %d bytes exceeds the %d-byte tool-media limit.", len(screenshot.PNG), MaxToolMediaBytes)
		}
		return response, nil
	case "find":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		element, err := client.FindElement(ctx, params.Selector)
		if err != nil {
			return ToolResponse{}, err
		}
		return WithResponseMetadata(NewTextResponse("element found: "+element.ID), map[string]any{"elementId": element.ID}), nil
	case "click":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		element, err := client.FindElement(ctx, params.Selector)
		if err != nil {
			return ToolResponse{}, err
		}
		if err := client.Click(ctx, element); err != nil {
			return ToolResponse{}, err
		}
		return NewTextResponse("element clicked: " + params.Selector), nil
	case "type":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		element, err := client.FindElement(ctx, params.Selector)
		if err != nil {
			return ToolResponse{}, err
		}
		if err := client.Type(ctx, element, params.Text); err != nil {
			return ToolResponse{}, err
		}
		return NewTextResponse("text entered: " + params.Selector), nil
	case "evaluate":
		client, err := b.requireClient()
		if err != nil {
			return ToolResponse{}, err
		}
		value, err := client.Evaluate(ctx, params.Script, params.Args)
		if err != nil {
			return ToolResponse{}, err
		}
		data, _ := json.Marshal(value)
		return NewTextResponse(string(data)), nil
	case "close":
		if b.client == nil {
			return NewTextResponse("browser session already closed"), nil
		}
		if err := b.client.Close(ctx); err != nil {
			return NewTextErrorResponseWithError(fmt.Errorf("close browser session: %w", err))
		}
		b.client = nil
		return NewTextResponse("browser session closed"), nil
	default:
		return NewTextErrorResponse(fmt.Sprintf("unsupported browser operation %q", params.Operation)), nil
	}
}

func (b *browserTool) start(ctx context.Context, params BrowserParams) (ToolResponse, error) {
	endpoint := os.Getenv("RSCH_WEBDRIVER_URL")
	if endpoint == "" {
		return ToolResponse{}, browser.ErrBackendUnavailable
	}
	if b.client != nil {
		if err := b.client.Close(ctx); err != nil {
			return NewTextErrorResponseWithError(fmt.Errorf("close previous browser session: %w", err))
		}
		b.client = nil
	}
	policy := browser.Policy{}
	if loaded := config.Get(); loaded != nil {
		policy.AllowedHosts = append([]string(nil), loaded.Network.AllowedHosts...)
		policy.AllowPrivate = loaded.Network.AllowPrivate
	}
	client, err := browser.New(endpoint, policy)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := client.Start(ctx, params.Browser); err != nil {
		return ToolResponse{}, err
	}
	b.client = client
	return NewTextResponse("browser session started"), nil
}

func (b *browserTool) requireClient() (*browser.Client, error) {
	if b.client == nil {
		return nil, errors.New("browser session is not started; use operation=start first")
	}
	return b.client, nil
}

func (b *browserTool) authorize(ctx context.Context, params BrowserParams) error {
	if b.permissions == nil {
		return errors.New("browser permission service is unavailable")
	}
	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return errors.New("session_id and message_id are required for browser actions")
	}
	permissionScope := strings.TrimSpace(os.Getenv("RSCH_WEBDRIVER_URL"))
	if permissionScope == "" {
		permissionScope = "browser://local"
	}
	if !b.permissions.RequestContext(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		ToolName:    BrowserToolName,
		Action:      params.Operation,
		Path:        permissionScope,
		ScopeKind:   permission.ScopeURL,
		Description: "Perform browser operation " + params.Operation,
		Params:      params,
	}) {
		return permission.ErrorPermissionDenied
	}
	return nil
}

var _ BaseTool = (*browserTool)(nil)
