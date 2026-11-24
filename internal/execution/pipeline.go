// Package execution is the single policy-aware boundary for agent-issued
// actions. Callers provide typed tool implementations; no caller may bypass
// policy, approval, sandbox, timeout, or event emission through this API.
package execution

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/risk"
)

type Risk string

const (
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

type Decision string

const (
	Allow  Decision = "allow"
	Prompt Decision = "prompt"
	Deny   Decision = "deny"
)

type Autonomy string

const (
	Manual     Autonomy = "manual"
	Assisted   Autonomy = "assisted"
	Autonomous Autonomy = "autonomous"
)

var ErrDenied = errors.New("execution denied by policy")
var ErrApprovalRequired = errors.New("execution requires approval")

type Spec struct {
	Name         string
	Description  string
	Risk         Risk
	Timeout      time.Duration
	Capabilities []string
}
type Request struct {
	ID        string
	SessionID string
	GoalID    string
	TaskID    string
	AgentID   string
	Tool      Spec
	Input     map[string]any
	Path      string
	Command   []string
}
type Result struct {
	ID         string
	RequestID  string
	Output     string
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	Evidence   events.Event
}

type Policy interface{ Evaluate(Request) Decision }
type Approval func(context.Context, Request) (bool, error)
type Sandbox interface {
	Run(context.Context, Request) (output string, exitCode int, err error)
}

type Callback func(context.Context, Request) (string, error)

type callbackSandbox struct{ callback Callback }

func (s callbackSandbox) Run(ctx context.Context, request Request) (string, int, error) {
	if s.callback == nil {
		return "", -1, errors.New("execution callback is required")
	}
	output, err := s.callback(ctx, request)
	if err != nil {
		return output, -1, err
	}
	return output, 0, nil
}

type Config struct {
	Policy     Policy
	Approval   Approval
	Sandbox    Sandbox
	Autonomy   Autonomy
	Bus        *events.Bus
	MaxTimeout time.Duration
	Notifier   notification.Provider
}
type Executor struct{ cfg Config }

func New(cfg Config) (*Executor, error) {
	if cfg.Policy == nil {
		return nil, errors.New("execution policy is required")
	}
	if cfg.Sandbox == nil {
		return nil, errors.New("sandbox runner is required")
	}
	if cfg.Bus == nil {
		cfg.Bus = events.NewBus()
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = 10 * time.Minute
	}
	if cfg.Autonomy == "" {
		cfg.Autonomy = Manual
	}
	switch cfg.Autonomy {
	case Manual, Assisted, Autonomous:
	default:
		return nil, fmt.Errorf("unknown execution autonomy %q", cfg.Autonomy)
	}
	return &Executor{cfg: cfg}, nil
}

func (e *Executor) Execute(parent context.Context, request Request) (Result, error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return Result{}, err
	}
	if request.ID == "" {
		request.ID = uuid.NewString()
	}
	if request.Tool.Name == "" {
		return Result{}, errors.New("tool name is required")
	}
	request.Tool.Risk = effectiveRisk(request)
	if !validRisk(request.Tool.Risk) {
		return Result{}, fmt.Errorf("%w: unknown tool risk %q", ErrDenied, request.Tool.Risk)
	}
	started := time.Now().UTC()
	request.Tool.Timeout = minTimeout(request.Tool.Timeout, e.cfg.MaxTimeout)
	if request.Tool.Timeout <= 0 {
		request.Tool.Timeout = e.cfg.MaxTimeout
	}
	e.publish(events.ToolBefore, request, map[string]any{"risk": request.Tool.Risk})
	e.publish(events.SandboxRequested, request, nil)
	decision := e.cfg.Policy.Evaluate(request)
	// Policy implementations may return an empty or unrecognized decision.
	// Only the two affirmative decisions may advance toward execution.
	if decision != Allow && decision != Prompt {
		e.notify("tool.denied", request, "execution denied by policy")
		e.publish(events.SandboxDenied, request, map[string]any{"reason": "policy_denied"})
		e.publish(events.ToolFailed, request, map[string]any{"reason": "policy_denied"})
		return Result{}, ErrDenied
	}
	if decision == Prompt || (e.cfg.Autonomy == Manual && request.Tool.Risk != RiskLow) {
		e.notify("approval.required", request, "approval required before execution")
		if e.cfg.Approval == nil {
			e.publish(events.SandboxDenied, request, map[string]any{"reason": "approval_unavailable"})
			e.publish(events.ToolFailed, request, map[string]any{"reason": "approval_unavailable"})
			return Result{}, ErrApprovalRequired
		}
		approved, err := e.cfg.Approval(parent, request)
		if err != nil {
			e.notify("approval.failed", request, err.Error())
			e.publish(events.SandboxDenied, request, map[string]any{"reason": "approval_failed"})
			e.publish(events.ToolFailed, request, map[string]any{"error": err.Error(), "reason": "approval_failed"})
			return Result{}, err
		}
		if !approved {
			e.publish(events.SandboxDenied, request, map[string]any{"reason": "approval_denied"})
			e.notify("approval.denied", request, "approval denied")
			e.publish(events.ToolFailed, request, map[string]any{"reason": "approval_denied"})
			return Result{}, ErrDenied
		}
	}
	ctx, cancel := context.WithTimeout(parent, request.Tool.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		e.publish(events.SandboxDenied, request, map[string]any{"reason": "context_canceled"})
		e.publish(events.ToolFailed, request, map[string]any{"error": err.Error(), "reason": "context_canceled"})
		return Result{}, err
	}
	e.publish(events.SandboxAllowed, request, nil)
	output, exitCode, err := e.cfg.Sandbox.Run(ctx, request)
	if err == nil {
		// In-process callbacks must cooperate with cancellation, but a callback
		// returning nil after its deadline must not produce success evidence.
		err = ctx.Err()
	}
	finished := time.Now().UTC()
	result := Result{ID: uuid.NewString(), RequestID: request.ID, Output: output, ExitCode: exitCode, StartedAt: started, FinishedAt: finished}
	if err != nil {
		e.notify("tool.failed", request, err.Error())
		e.publish(events.ToolFailed, request, map[string]any{"error": err.Error(), "exitCode": exitCode})
		return result, err
	}
	result.Evidence = events.New(events.ToolAfter, map[string]any{"requestId": request.ID, "exitCode": exitCode, "durationMs": finished.Sub(started).Milliseconds()})
	result.Evidence.SessionID, result.Evidence.GoalID, result.Evidence.TaskID, result.Evidence.Actor = request.SessionID, request.GoalID, request.TaskID, request.AgentID
	e.cfg.Bus.Publish(result.Evidence)
	e.notify("tool.completed", request, "tool execution completed")
	return result, nil
}

// ExecuteCallback applies the same policy, approval, timeout, cancellation,
// and evidence lifecycle to an in-process tool callback as Execute applies to
// a sandbox command. It is the bridge for tools that are not OS processes.
func (e *Executor) ExecuteCallback(parent context.Context, request Request, callback Callback) (Result, error) {
	return e.ExecuteCallbackWithApproval(parent, request, callback, nil)
}

// ExecuteCallbackWithApproval is the callback boundary for tools whose own
// permission service is the user-approval mechanism. The executor still owns
// validation, policy, timeout, sandbox, lifecycle events, and evidence; the
// supplied approval callback is invoked only when the executor's policy
// requires approval.
func (e *Executor) ExecuteCallbackWithApproval(parent context.Context, request Request, callback Callback, approval Approval) (Result, error) {
	if callback == nil {
		return Result{}, errors.New("execution callback is required")
	}
	clone := *e
	clone.cfg.Sandbox = callbackSandbox{callback: callback}
	if approval != nil {
		clone.cfg.Approval = approval
	}
	return clone.Execute(parent, request)
}

func (e *Executor) publish(kind events.Type, request Request, data map[string]any) {
	if data == nil {
		data = make(map[string]any)
	}
	data["requestId"] = request.ID
	data["tool"] = request.Tool.Name
	data["risk"] = request.Tool.Risk
	if request.TaskID != "" {
		data["taskId"] = request.TaskID
	}
	if request.GoalID != "" {
		data["goalId"] = request.GoalID
	}
	if request.AgentID != "" {
		data["agentId"] = request.AgentID
	}
	event := events.New(kind, data)
	event.SessionID, event.GoalID, event.TaskID, event.Actor = request.SessionID, request.GoalID, request.TaskID, request.AgentID
	e.cfg.Bus.Publish(event)
}

func (e *Executor) notify(kind string, request Request, message string) {
	if e == nil || e.cfg.Notifier == nil {
		return
	}
	_ = e.cfg.Notifier.Notify(context.Background(), notification.Event{Kind: kind, Resource: request.Tool.Name, Message: message, Metadata: map[string]string{"sessionId": request.SessionID, "goalId": request.GoalID, "taskId": request.TaskID, "agentId": request.AgentID, "path": request.Path}})
}
func minTimeout(value, max time.Duration) time.Duration {
	if value <= 0 || value > max {
		return max
	}
	return value
}

// LocalSandbox is an explicit capability implementation for local commands.
// It never invokes a shell; callers must provide argv, which prevents shell
// metacharacters from becoming an accidental second command language.
type LocalSandbox struct {
	// AllowedCommands contains exact executable names or paths. A bare name
	// does not authorize another executable with the same basename.
	AllowedCommands  map[string]bool
	WorkingDirectory string
}

func (s LocalSandbox) Run(ctx context.Context, request Request) (string, int, error) {
	if len(request.Command) == 0 {
		return "", -1, errors.New("sandbox command is required")
	}
	name := request.Command[0]
	if strings.TrimSpace(name) == "" || !s.AllowedCommands[name] {
		return "", -1, fmt.Errorf("command %q is not allowed", name)
	}
	result, err := process.RunOutput(ctx, process.Spec{Argv: request.Command, Directory: s.WorkingDirectory})
	return string(result.Output), result.ExitCode, err
}

type DefaultPolicy struct {
	DeniedTools     map[string]bool
	PromptTools     map[string]bool
	AllowedPaths    []string
	AllowedCommands map[string]bool
	Production      bool
	// DelegatedApproval marks a boundary whose wrapped operation performs its
	// own explicit permission request. High-risk classification and events are
	// retained, but this policy does not create a second approval prompt.
	DelegatedApproval bool
}

func (p DefaultPolicy) Evaluate(request Request) Decision {
	level := effectiveRisk(request)
	if p.Production || p.DeniedTools[request.Tool.Name] || !validRisk(level) {
		return Deny
	}
	if len(p.AllowedPaths) > 0 {
		paths := []string{request.Path}
		if additional, ok := request.Input["_governance_paths"].([]string); ok {
			paths = append(paths, additional...)
		} else if additional, ok := request.Input["_governance_paths"].([]any); ok {
			for _, value := range additional {
				if path, ok := value.(string); ok {
					paths = append(paths, path)
				}
			}
		}
		for _, path := range paths {
			if path != "" && !matchesPath(path, p.AllowedPaths) {
				return Deny
			}
		}
	}
	if len(request.Command) > 0 && (strings.TrimSpace(request.Command[0]) == "" || !p.AllowedCommands[request.Command[0]]) {
		return Deny
	}
	if p.PromptTools[request.Tool.Name] {
		return Prompt
	}
	if !p.DelegatedApproval && (level == RiskHigh || level == RiskCritical) {
		return Prompt
	}
	return Allow
}

func validRisk(value Risk) bool {
	switch value {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

func effectiveRisk(request Request) Risk {
	if request.Tool.Risk != "" {
		return request.Tool.Risk
	}
	assessment := risk.Assess(risk.Input{Tool: request.Tool.Name, Path: request.Path, Command: request.Command, Capabilities: request.Tool.Capabilities, Network: hasNetworkCapability(request.Tool.Capabilities), Mutating: false})
	return Risk(assessment.Level)
}

func hasNetworkCapability(capabilities []string) bool {
	for _, capability := range capabilities {
		if strings.Contains(strings.ToLower(capability), "network") || strings.Contains(strings.ToLower(capability), "browser") {
			return true
		}
	}
	return false
}
func matchesPath(candidate string, patterns []string) bool {
	candidate = filepath.Clean(candidate)
	for _, pattern := range patterns {
		if ok, _ := filepath.Match(pattern, candidate); ok {
			return true
		}
		if ok, _ := filepath.Match(pattern, filepath.ToSlash(candidate)); ok {
			return true
		}
		if strings.HasPrefix(candidate, filepath.Clean(pattern)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }
