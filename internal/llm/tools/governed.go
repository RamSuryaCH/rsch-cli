package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/notification"
)

type governedTool struct {
	inner    BaseTool
	executor *execution.Executor
	risk     execution.Risk
}

func Govern(inner BaseTool, executor *execution.Executor, risk execution.Risk) BaseTool {
	if inner == nil {
		return inner
	}
	return governedTool{inner: inner, executor: executor, risk: risk}
}

func (t governedTool) Info() ToolInfo {
	info := t.inner.Info()
	info.Risk = string(t.risk)
	if info.OutputSchema == nil {
		info.OutputSchema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type":     map[string]any{"type": "string"},
				"content":  map[string]any{"type": "string"},
				"metadata": map[string]any{"type": "string"},
				"is_error": map[string]any{"type": "boolean"},
			},
		}
	}
	if len(info.Permissions) == 0 {
		if t.risk == execution.RiskLow {
			info.Permissions = []string{"read-only"}
		} else {
			info.Permissions = []string{"permission-service"}
		}
	}
	if info.Timeout <= 0 {
		info.Timeout = 10 * time.Minute
	}
	if info.ExecutionMode == "" {
		info.ExecutionMode = "callback"
	}
	if len(info.Capabilities) == 0 {
		info.Capabilities = []string{"structured-output"}
		if t.risk == execution.RiskLow {
			info.Capabilities = append(info.Capabilities, "filesystem-read")
		} else {
			info.Capabilities = append(info.Capabilities, "policy-gated")
		}
	}
	return info
}
func (t governedTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if t.executor == nil {
		err := errors.New("governed tool execution policy is unavailable")
		return NewTextErrorResponse(err.Error()), err
	}
	request := requestForCall(ctx, t.inner, call, t.risk)
	var response ToolResponse
	var responseErr error
	_, err := t.executor.ExecuteCallback(ctx, request, func(callbackCtx context.Context, _ execution.Request) (string, error) {
		var runErr error
		response, runErr = t.inner.Run(callbackCtx, call)
		if runErr != nil {
			return response.Content, runErr
		}
		if response.IsError {
			// Model-visible errors still represent failed executions for
			// evidence, hooks, and notifications. Keep their public response
			// contract separate from transport errors returned by the tool.
			responseErr = errors.New("tool returned an error response")
			return response.Content, responseErr
		}
		return response.Content, nil
	})
	if responseErr != nil && errors.Is(err, responseErr) {
		return response, nil
	}
	if err != nil && response.Content == "" {
		return NewTextErrorResponse(err.Error()), err
	}
	if err != nil {
		return response, err
	}
	return response, nil
}

// ReadOnlyExecutor returns the configured callback executor used by built-in
// tools. An explicitly supplied execution.Config is useful to callers that
// already own the application's policy, sandbox, approval, and evidence
// dependencies. Without one, the current loaded configuration is translated
// into the same policy inputs; there is no empty-policy autonomous fallback.
func ReadOnlyExecutor(bus *events.Bus, configured ...execution.Config) (*execution.Executor, error) {
	var cfg execution.Config
	if len(configured) > 0 {
		cfg = configured[0]
	} else {
		var err error
		cfg, err = configuredExecutionConfig(bus)
		if err != nil {
			return nil, err
		}
	}
	if bus == nil {
		bus = events.NewBus()
	}
	if cfg.Bus == nil {
		cfg.Bus = bus
	}
	return execution.New(cfg)
}

func configuredExecutionConfig(bus *events.Bus) (execution.Config, error) {
	loaded := config.Get()
	if loaded == nil {
		return execution.Config{}, errors.New("configured execution policy is unavailable")
	}

	policy := execution.DefaultPolicy{
		DeniedTools:       map[string]bool{},
		PromptTools:       map[string]bool{},
		AllowedPaths:      append([]string(nil), loaded.Policy.PathAllow...),
		AllowedCommands:   configuredCommandAllowlist(loaded.Policy.CommandAllow),
		Production:        loaded.Policy.ProductionDenied,
		DelegatedApproval: true,
	}
	for toolName, decision := range loaded.Policy.Tool {
		switch strings.ToLower(strings.TrimSpace(decision)) {
		case string(execution.Deny):
			policy.DeniedTools[toolName] = true
		case string(execution.Prompt):
			policy.PromptTools[toolName] = true
		}
	}
	var notifier notification.Provider
	settings := notification.Config{
		WebhookURL:      loaded.Notifications.WebhookURL,
		Command:         loaded.Notifications.Command,
		Args:            loaded.Notifications.Args,
		Timeout:         time.Duration(loaded.Notifications.TimeoutMS) * time.Millisecond,
		AllowedHosts:    loaded.Network.AllowedHosts,
		AllowPrivate:    loaded.Network.AllowPrivate,
		AllowedCommands: loaded.Policy.CommandAllow,
	}
	provider, err := notification.NewConfigured(config.WorkingDirectory(), settings)
	if err != nil {
		return execution.Config{}, fmt.Errorf("initialize notifications: %w", err)
	}
	notifier = provider

	return execution.Config{
		Policy:   policy,
		Approval: nil,
		Sandbox:  execution.LocalSandbox{AllowedCommands: cloneBoolMap(policy.AllowedCommands), WorkingDirectory: config.WorkingDirectory()},
		Autonomy: execution.Autonomy(loaded.Autonomy),
		Bus:      bus,
		Notifier: notifier,
	}, nil
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func configuredCommandAllowlist(commands []string) map[string]bool {
	allowed := make(map[string]bool, len(commands))
	for _, command := range commands {
		if command = strings.TrimSpace(command); command != "" {
			allowed[command] = true
		}
	}
	return allowed
}

func requestForCall(ctx context.Context, inner BaseTool, call ToolCall, risk execution.Risk) execution.Request {
	sessionID, _ := GetContextValues(ctx)
	info := inner.Info()
	request := execution.Request{
		ID:        call.ID,
		SessionID: sessionID,
		TaskID:    GetTaskID(ctx),
		Tool:      execution.Spec{Name: info.Name, Description: info.Description, Risk: risk, Timeout: info.Timeout, Capabilities: append([]string(nil), info.Capabilities...)},
		Input:     map[string]any{"raw": call.Input},
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(call.Input), &fields); err != nil {
		return request
	}
	if fields == nil {
		return request
	}
	request.Input = fields
	request.Input["_raw"] = call.Input
	paths := inputPaths(fields)
	if len(paths) > 0 {
		request.Path = paths[0]
		request.Input["_governance_paths"] = paths
	}
	request.Command = inputCommand(fields)
	return request
}

func inputPaths(fields map[string]any) []string {
	paths := make([]string, 0, 2)
	for _, key := range []string{"path", "file_path"} {
		if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
			paths = append(paths, value)
		}
	}
	if values, ok := fields["paths"].([]any); ok {
		for _, value := range values {
			if path, ok := value.(string); ok && strings.TrimSpace(path) != "" {
				paths = append(paths, path)
			}
		}
	}
	if patch, ok := fields["patch_text"].(string); ok {
		prefixes := []string{"*** Update File: ", "*** Delete File: ", "*** Add File: ", "*** Move to: "}
		for _, line := range strings.Split(patch, "\n") {
			for _, prefix := range prefixes {
				if !strings.HasPrefix(line, prefix) {
					continue
				}
				path := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if path != "" {
					paths = append(paths, path)
				}
				break
			}
		}
	}
	return paths
}

func inputCommand(fields map[string]any) []string {
	value, ok := fields["command"]
	if !ok {
		value = fields["argv"]
	}
	switch command := value.(type) {
	case string:
		command = strings.Trim(command, " \t\n")
		if command == "" {
			return nil
		}
		words, err := parseShellWords(command)
		if err != nil || len(words) == 0 {
			// Keep a command present so policy rejects ambiguous syntax instead
			// of accidentally bypassing command allowlist validation.
			return []string{""}
		}
		return []string{words[0]}
	case []any:
		if len(command) == 0 {
			return nil
		}
		if first, ok := command[0].(string); ok {
			// Structured argv is already decoded: quotes and spaces are literal
			// filename bytes, not shell syntax to strip before policy checks.
			return []string{first}
		}
	}
	return nil
}

var _ BaseTool = governedTool{}
