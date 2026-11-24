package permission

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

var ErrorPermissionDenied = errors.New("permission denied")

type CreatePermissionRequest struct {
	SessionID   string    `json:"session_id"`
	ToolName    string    `json:"tool_name"`
	Description string    `json:"description"`
	Action      string    `json:"action"`
	Params      any       `json:"params"`
	Path        string    `json:"path"`
	ScopeKind   ScopeKind `json:"scope_kind,omitempty"`
}

type ScopeKind string

const (
	// ScopeFile preserves the legacy behavior: Path names a file and the
	// permission is scoped to its containing directory.
	ScopeFile ScopeKind = "file"
	// ScopeDirectory means Path is already the exact directory scope.
	ScopeDirectory ScopeKind = "directory"
	// ScopeURL keeps a network resource identifier opaque to filepath rules.
	ScopeURL ScopeKind = "url"
)

type PermissionRequest struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	ToolName    string    `json:"tool_name"`
	Description string    `json:"description"`
	Action      string    `json:"action"`
	Params      any       `json:"params"`
	Path        string    `json:"path"`
	ScopeKind   ScopeKind `json:"scope_kind,omitempty"`
}

type Service interface {
	pubsub.Suscriber[PermissionRequest]
	GrantPersistant(permission PermissionRequest)
	Grant(permission PermissionRequest)
	Deny(permission PermissionRequest)
	Request(opts CreatePermissionRequest) bool
	AutoApproveSession(sessionID string)
	RequestContext(ctx context.Context, opts CreatePermissionRequest) bool
}

type permissionService struct {
	*pubsub.Broker[PermissionRequest]

	sessionPermissions  []PermissionRequest
	pendingRequests     sync.Map
	autoApproveSessions []string
	eventBus            *events.Bus
	mu                  sync.RWMutex
}

type pendingPermission struct {
	request  PermissionRequest
	response chan bool
	ctx      context.Context
}

const requestTimeout = 10 * time.Minute

func (s *permissionService) GrantPersistant(permission PermissionRequest) {
	s.respond(permission, true, true)
}

func (s *permissionService) Grant(permission PermissionRequest) {
	s.respond(permission, true, false)
}

func (s *permissionService) Deny(permission PermissionRequest) {
	s.respond(permission, false, false)
}

func (s *permissionService) Request(opts CreatePermissionRequest) bool {
	return s.RequestContext(context.Background(), opts)
}

func (s *permissionService) RequestContext(ctx context.Context, opts CreatePermissionRequest) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	opts, ok := normalizePermissionIdentity(opts)
	if !ok {
		return false
	}

	scope, scopeKind, ok := normalizePermissionScope(opts.Path, opts.ScopeKind)
	if !ok {
		return false
	}
	permission := PermissionRequest{
		ID:          uuid.New().String(),
		Path:        scope,
		ScopeKind:   scopeKind,
		SessionID:   opts.SessionID,
		ToolName:    opts.ToolName,
		Description: opts.Description,
		Action:      opts.Action,
		Params:      opts.Params,
	}

	s.mu.RLock()
	autoApproved := slices.Contains(s.autoApproveSessions, opts.SessionID)
	permissions := append([]PermissionRequest(nil), s.sessionPermissions...)
	s.mu.RUnlock()
	if ctx.Err() != nil {
		return false
	}
	if autoApproved {
		s.publish(events.PermissionAutoGranted, permission, false)
		return ctx.Err() == nil
	}

	for _, p := range permissions {
		if p.ToolName == permission.ToolName && p.Action == permission.Action && p.SessionID == permission.SessionID && p.Path == permission.Path && p.ScopeKind == permission.ScopeKind {
			s.publish(events.PermissionAutoGranted, permission, true)
			return ctx.Err() == nil
		}
	}

	// The same bounded context governs waiting and incoming decisions, so a
	// late UI click cannot grant a request whose timeout has already expired.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	respCh := make(chan bool, 1)

	s.pendingRequests.Store(permission.ID, pendingPermission{request: permission, response: respCh, ctx: ctx})
	defer s.pendingRequests.Delete(permission.ID)

	s.publish(events.PermissionRequested, permission, false)
	s.Publish(pubsub.CreatedEvent, permission)

	// A prompt must never strand an agent forever. Callers that carry a
	// cancellable execution context get the earlier cancellation; legacy
	// callers using Request receive the bounded safety timeout.
	select {
	case resp := <-respCh:
		return resp && ctx.Err() == nil
	case <-ctx.Done():
		s.respond(permission, false, false)
		return false
	}
}

func normalizePermissionIdentity(opts CreatePermissionRequest) (CreatePermissionRequest, bool) {
	opts.SessionID = strings.TrimSpace(opts.SessionID)
	opts.ToolName = strings.TrimSpace(opts.ToolName)
	opts.Action = strings.TrimSpace(opts.Action)
	return opts, opts.SessionID != "" && opts.ToolName != "" && opts.Action != ""
}

func normalizePermissionScope(path string, kind ScopeKind) (string, ScopeKind, bool) {
	path = strings.TrimSpace(path)
	if kind == "" {
		kind = ScopeFile
	}
	switch kind {
	case ScopeFile:
		if path == "" {
			return "", kind, false
		}
		scope := filepath.Dir(path)
		if scope == "." {
			scope = config.WorkingDirectory()
		}
		return filepath.Clean(scope), kind, true
	case ScopeDirectory:
		if path == "" || path == "." {
			path = config.WorkingDirectory()
		}
		return filepath.Clean(path), kind, true
	case ScopeURL:
		if path == "" {
			return "", kind, false
		}
		return path, kind, true
	default:
		return "", kind, false
	}
}

func (s *permissionService) AutoApproveSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoApproveSessions = append(s.autoApproveSessions, sessionID)
}

func NewPermissionService(buses ...*events.Bus) Service {
	var eventBus *events.Bus
	if len(buses) > 0 {
		eventBus = buses[0]
	}
	return &permissionService{
		Broker:             pubsub.NewBroker[PermissionRequest](),
		sessionPermissions: make([]PermissionRequest, 0),
		eventBus:           eventBus,
	}
}

func (s *permissionService) publish(kind events.Type, request PermissionRequest, persistent bool) {
	if s.eventBus == nil {
		return
	}
	data := map[string]any{"permissionId": request.ID, "sessionId": request.SessionID, "tool": request.ToolName, "action": request.Action, "path": request.Path, "scopeKind": request.ScopeKind, "persistent": persistent}
	event := events.New(kind, data)
	event.SessionID = request.SessionID
	s.eventBus.Publish(event)
}

// respond claims the terminal decision before recording it and releasing the
// caller. Duplicate UI decisions and cancellation cannot publish conflicting
// terminal events or alter an already claimed permission.
func (s *permissionService) respond(decision PermissionRequest, approved, persistent bool) {
	if decision.ID == "" {
		return
	}
	value, ok := s.pendingRequests.LoadAndDelete(decision.ID)
	if !ok {
		return
	}
	pending, ok := value.(pendingPermission)
	if !ok {
		return
	}
	if pending.ctx.Err() != nil {
		approved = false
		persistent = false
	}
	if approved && persistent {
		s.mu.Lock()
		s.sessionPermissions = append(s.sessionPermissions, pending.request)
		s.mu.Unlock()
	}
	kind := events.PermissionDenied
	if approved {
		kind = events.PermissionGranted
	}
	s.publish(kind, pending.request, persistent)
	select {
	case pending.response <- approved:
	default:
	}
}

type governedService struct {
	delegate Service
	executor *execution.Executor
}

func NewGovernedService(delegate Service, executor *execution.Executor) Service {
	if delegate == nil {
		return delegate
	}
	return governedService{delegate: delegate, executor: executor}
}

func (g governedService) Subscribe(ctx context.Context) <-chan pubsub.Event[PermissionRequest] {
	return g.delegate.Subscribe(ctx)
}
func (g governedService) GrantPersistant(request PermissionRequest) {
	g.delegate.GrantPersistant(request)
}
func (g governedService) Grant(request PermissionRequest) { g.delegate.Grant(request) }
func (g governedService) Deny(request PermissionRequest)  { g.delegate.Deny(request) }
func (g governedService) AutoApproveSession(sessionID string) {
	g.delegate.AutoApproveSession(sessionID)
}
func (g governedService) Request(opts CreatePermissionRequest) bool {
	return g.RequestContext(context.Background(), opts)
}
func (g governedService) RequestContext(ctx context.Context, opts CreatePermissionRequest) bool {
	if g.executor == nil || g.delegate == nil {
		return false
	}
	opts, ok := normalizePermissionIdentity(opts)
	if !ok {
		return false
	}
	scope, scopeKind, ok := normalizePermissionScope(opts.Path, opts.ScopeKind)
	if !ok {
		return false
	}
	risk := execution.RiskMedium
	if opts.Action == "write" || opts.Action == "execute" || opts.Action == "delete" {
		risk = execution.RiskHigh
	}
	request := execution.Request{ID: uuid.NewString(), SessionID: opts.SessionID, Path: scope, Tool: execution.Spec{Name: opts.ToolName, Description: opts.Description, Risk: risk}, Input: map[string]any{"action": opts.Action, "scopeKind": scopeKind}}
	permissionApproved := false
	approval := func(approvalCtx context.Context, _ execution.Request) (bool, error) {
		approved := g.delegate.RequestContext(approvalCtx, opts)
		permissionApproved = approved
		return approved, nil
	}
	_, err := g.executor.ExecuteCallbackWithApproval(ctx, request, func(callbackCtx context.Context, _ execution.Request) (string, error) {
		// High-risk requests are approved before the callback by the shared
		// executor. Medium-risk requests reach the legacy permission service
		// here, preserving the existing prompt semantics without double prompts.
		if permissionApproved {
			return "approved", nil
		}
		if g.delegate.RequestContext(callbackCtx, opts) {
			return "approved", nil
		}
		return "denied", ErrorPermissionDenied
	}, approval)
	return err == nil
}
