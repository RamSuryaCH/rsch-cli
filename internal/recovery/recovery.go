// Package recovery turns persisted lifecycle events into safe recovery plans.
// Inspection is always passive; explicit re-execution accepts caller-supplied
// requests and delegates every action to the normal execution boundary.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
)

type Action struct {
	RequestID string         `json:"requestId"`
	Tool      string         `json:"tool,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	GoalID    string         `json:"goalId,omitempty"`
	AgentID   string         `json:"agentId,omitempty"`
	Risk      execution.Risk `json:"risk,omitempty"`
	Status    string         `json:"status"`
	Error     string         `json:"error,omitempty"`
}

type Plan struct {
	TaskID      string    `json:"taskId,omitempty"`
	EventCount  int       `json:"eventCount"`
	Actions     []Action  `json:"actions,omitempty"`
	Incomplete  []string  `json:"incomplete,omitempty"`
	Failures    int       `json:"failures"`
	LastEventAt time.Time `json:"lastEventAt,omitempty"`
	SafeInspect bool      `json:"safeInspection"`
}

// Analyze never executes an action. It identifies tool actions that do not
// have a terminal event and failed actions that require an explicit retry.
func Analyze(items []events.Event, taskID string) Plan {
	plan := Plan{TaskID: taskID, Actions: []Action{}, Incomplete: []string{}, SafeInspect: true}
	ordered := make([]events.Event, 0, len(items))
	sequenced := true
	for _, event := range items {
		if taskID != "" && firstNonEmpty(event.TaskID, dataString(event, "taskId")) != taskID {
			continue
		}
		plan.EventCount++
		if event.At.After(plan.LastEventAt) {
			plan.LastEventAt = event.At
		}
		switch event.Type {
		case events.ToolBefore, events.ToolAfter, events.ToolFailed:
		default:
			continue
		}
		if dataString(event, "requestId") == "" {
			continue
		}
		ordered = append(ordered, event)
		sequenced = sequenced && event.Sequence > 0
	}
	// Durable sequence is authoritative, including when clocks move backward.
	// Legacy unsequenced events use capture time, preserving ties and input.
	sort.SliceStable(ordered, func(i, j int) bool {
		if sequenced {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		return ordered[i].At.Before(ordered[j].At)
	})
	actions := map[string]Action{}
	for _, event := range ordered {
		requestID := dataString(event, "requestId")
		action := actions[requestID]
		if action.RequestID == "" {
			action = Action{RequestID: requestID, Status: "incomplete"}
		}
		action.TaskID = firstNonEmpty(action.TaskID, event.TaskID, dataString(event, "taskId"))
		action.GoalID = firstNonEmpty(action.GoalID, event.GoalID, dataString(event, "goalId"))
		action.AgentID = firstNonEmpty(action.AgentID, event.Actor, dataString(event, "agentId"))
		action.Tool = firstNonEmpty(action.Tool, dataString(event, "tool"))
		if risk := dataString(event, "risk"); risk != "" {
			action.Risk = execution.Risk(risk)
		} else if risk, ok := event.Data["risk"].(execution.Risk); ok {
			// Live events retain the typed risk value; decoded persisted events
			// carry its string representation.
			action.Risk = risk
		}
		switch event.Type {
		case events.ToolBefore:
			action.Status = "incomplete"
			action.Error = ""
		case events.ToolAfter:
			action.Status = "completed"
			action.Error = ""
		case events.ToolFailed:
			action.Status = "failed"
			action.Error = firstNonEmpty(dataString(event, "error"), dataString(event, "reason"))
		}
		actions[requestID] = action
	}
	for _, action := range actions {
		plan.Actions = append(plan.Actions, action)
		if action.Status != "completed" {
			plan.Incomplete = append(plan.Incomplete, action.RequestID)
		}
		if action.Status == "failed" {
			plan.Failures++
		}
	}
	sort.Slice(plan.Actions, func(i, j int) bool { return plan.Actions[i].RequestID < plan.Actions[j].RequestID })
	sort.Strings(plan.Incomplete)
	return plan
}

type RequestResolver func(Action) (execution.Request, bool)

// Reexecute is an explicit opt-in operation. The resolver must reconstruct a
// request from trusted application state; the persisted plan intentionally
// does not contain raw command arguments or tool input. Every reconstructed
// request still passes policy, approval, timeout, sandbox, and evidence
// handling in Executor.Execute.
func Reexecute(ctx context.Context, executor *execution.Executor, plan Plan, resolve RequestResolver) ([]execution.Result, error) {
	if executor == nil {
		return nil, errors.New("recovery executor is required")
	}
	if resolve == nil {
		return nil, errors.New("recovery request resolver is required")
	}
	// Validate the entire plan before any request can execute. In particular,
	// malformed later entries must not cause a partial replay of earlier ones.
	actions := make(map[string]Action, len(plan.Actions))
	for _, action := range plan.Actions {
		if strings.TrimSpace(action.RequestID) == "" {
			return nil, errors.New("recovery action request id is required")
		}
		if _, exists := actions[action.RequestID]; exists {
			return nil, fmt.Errorf("duplicate recovery action %q", action.RequestID)
		}
		actions[action.RequestID] = action
	}
	seen := make(map[string]bool, len(plan.Incomplete))
	for _, requestID := range plan.Incomplete {
		action, ok := actions[requestID]
		if !ok {
			return nil, fmt.Errorf("recovery action %q is missing", requestID)
		}
		if seen[requestID] {
			return nil, fmt.Errorf("duplicate recovery request %q", requestID)
		}
		seen[requestID] = true
		if action.Status != "incomplete" && action.Status != "failed" {
			return nil, fmt.Errorf("recovery action %q is not incomplete or failed", requestID)
		}
		if plan.TaskID != "" && action.TaskID != plan.TaskID {
			return nil, fmt.Errorf("recovery action %q belongs to a different task", requestID)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]execution.Result, 0, len(plan.Incomplete))
	for _, requestID := range plan.Incomplete {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		action := actions[requestID]
		request, ok := resolve(action)
		if !ok {
			return results, fmt.Errorf("recovery request %q is not available", requestID)
		}
		if request.ID == "" {
			request.ID = action.RequestID
		}
		request.TaskID = firstNonEmpty(request.TaskID, action.TaskID)
		request.GoalID = firstNonEmpty(request.GoalID, action.GoalID)
		request.AgentID = firstNonEmpty(request.AgentID, action.AgentID)
		if request.Tool.Name == "" {
			request.Tool.Name = action.Tool
		}
		result, err := executor.Execute(ctx, request)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func dataString(event events.Event, key string) string {
	if event.Data == nil {
		return ""
	}
	value, ok := event.Data[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
