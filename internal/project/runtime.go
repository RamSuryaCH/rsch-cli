package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/sandbox"
)

var (
	ErrCapabilityDenied  = errors.New("resource capability denied")
	ErrResourceDenied    = errors.New("resource is not explicitly approved")
	ErrEntrypointInvalid = errors.New("resource entrypoint is invalid")
	ErrSandboxRequired   = errors.New("resource execution requires an unavailable OS sandbox")
)

type RuntimePolicy struct {
	AllowedCapabilities map[string]bool
	ApprovedResources   map[string]bool
	Timeout             time.Duration
	MaxOutput           int
	Environment         map[string]string
	Sandbox             sandbox.Runner
	RequireSandbox      bool
	SandboxMode         sandbox.Mode
	AllowNetwork        bool
}

type Invocation struct {
	ResourceID string   `json:"resourceId"`
	Kind       string   `json:"kind"`
	Capability string   `json:"capability,omitempty"`
	Arguments  []string `json:"arguments,omitempty"`
	EventJSON  string   `json:"eventJson,omitempty"`
}

type Result struct {
	ResourceID string    `json:"resourceId"`
	Kind       string    `json:"kind"`
	Output     string    `json:"output,omitempty"`
	ExitCode   int       `json:"exitCode"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// Runtime executes project resources under an explicit capability policy.
// Resources never inherit the caller's environment and entrypoints cannot
// escape their resource directory.
type Runtime struct {
	Root   string
	Policy RuntimePolicy
}

func NewRuntime(root string, policy RuntimePolicy) (Runtime, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Runtime{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Runtime{}, err
	}
	if !info.IsDir() {
		return Runtime{}, errors.New("resource runtime root is not a directory")
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return Runtime{}, err
	}
	if policy.Timeout <= 0 || policy.Timeout > 10*time.Minute {
		policy.Timeout = 2 * time.Minute
	}
	if policy.MaxOutput <= 0 || policy.MaxOutput > 4<<20 {
		policy.MaxOutput = 1 << 20
	}
	if policy.SandboxMode == "" {
		policy.SandboxMode = sandbox.WorkspaceWrite
	}
	if policy.RequireSandbox && policy.Sandbox == nil {
		capability := sandbox.Probe(context.Background(), absolute)
		if !capability.Isolated {
			return Runtime{}, fmt.Errorf("%w: %s", ErrSandboxRequired, capability.Reason)
		}
		policy.Sandbox = sandbox.PlatformRunner{Capability: capability, WorkingDirectory: absolute, Mode: policy.SandboxMode, AllowNetwork: policy.AllowNetwork}
	}
	return Runtime{Root: absolute, Policy: policy}, nil
}

func (r Runtime) Run(ctx context.Context, resource Resource, invocation Invocation) (Result, error) {
	if resource.Manifest == nil {
		return Result{}, errors.New("resource manifest is required")
	}
	if invocation.ResourceID != "" && invocation.ResourceID != resource.Manifest.ID {
		return Result{}, errors.New("invocation resource id does not match manifest")
	}
	// Resource execution is an independent authority from individual declared
	// capabilities. Requiring a positive, per-resource decision prevents an
	// empty capabilities list from becoming an allow-all bypass.
	if !r.Policy.ApprovedResources[ApprovalKey(resource.Kind, resource.Manifest.ID)] {
		return Result{}, fmt.Errorf("%w: %s", ErrResourceDenied, resource.Manifest.ID)
	}
	// Project-controlled entrypoints never run directly on the host. A caller
	// may inject a tested sandbox runner, or NewRuntime can negotiate the
	// platform runner when RequireSandbox is set.
	if r.Policy.Sandbox == nil {
		return Result{}, ErrSandboxRequired
	}
	if resource.Kind != "hook" && resource.Kind != "plugin" && resource.Kind != "agent" {
		return Result{}, fmt.Errorf("resource kind %q is not executable", resource.Kind)
	}
	for _, capability := range resource.Manifest.Capabilities {
		if !r.Policy.AllowedCapabilities[capability] {
			return Result{}, fmt.Errorf("%w: %s", ErrCapabilityDenied, capability)
		}
	}
	if invocation.Capability != "" && !manifestHasCapability(resource.Manifest, invocation.Capability) {
		return Result{}, fmt.Errorf("%w: plugin does not declare %s", ErrCapabilityDenied, invocation.Capability)
	}
	entrypoint, err := r.entrypoint(resource)
	if err != nil {
		return Result{}, err
	}
	timeout := r.Policy.Timeout
	if resource.Manifest.Timeout > 0 && resource.Manifest.Timeout < timeout {
		timeout = resource.Manifest.Timeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now().UTC()
	env := make([]string, 0, len(r.Policy.Environment)+2)
	keys := make([]string, 0, len(r.Policy.Environment))
	for key := range r.Policy.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) != "" {
			env = append(env, key+"="+r.Policy.Environment[key])
		}
	}
	env = append(env, "RSCH_RESOURCE_ID="+resource.Manifest.ID, "RSCH_RESOURCE_KIND="+resource.Kind, "RSCH_PLATFORM="+runtime.GOOS+"/"+runtime.GOARCH)
	if invocation.EventJSON != "" {
		env = append(env, "RSCH_EVENT_JSON="+invocation.EventJSON)
	}
	envMap := make(map[string]string, len(env))
	for _, value := range env {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	argv := append([]string{entrypoint}, invocation.Arguments...)
	var output []byte
	var runErr error
	if environmentRunner, ok := r.Policy.Sandbox.(sandbox.EnvironmentRunner); ok {
		output, runErr = environmentRunner.RunWithEnvironment(timeoutCtx, argv, envMap)
	} else {
		output, runErr = r.Policy.Sandbox.Run(timeoutCtx, argv)
	}
	resultExitCode := 0
	if runErr != nil {
		resultExitCode = -1
		var exitErr interface{ ExitCode() int }
		if errors.As(runErr, &exitErr) {
			resultExitCode = exitErr.ExitCode()
		}
	}
	finished := time.Now().UTC()
	if len(output) > r.Policy.MaxOutput {
		output = append(output[:r.Policy.MaxOutput], []byte("\n[output truncated]")...)
	}
	result := Result{ResourceID: resource.Manifest.ID, Kind: resource.Kind, Output: string(output), ExitCode: resultExitCode, StartedAt: started, FinishedAt: finished}
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

// ApprovalKey scopes governance decisions by both resource kind and ID. This
// prevents a plugin from inheriting approval by reusing an approved hook ID.
func ApprovalKey(kind, id string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + ":" + strings.TrimSpace(id)
}

func manifestHasCapability(manifest *Manifest, capability string) bool {
	if manifest == nil {
		return false
	}
	for _, declared := range manifest.Capabilities {
		if declared == capability {
			return true
		}
	}
	return false
}

func (r Runtime) entrypoint(resource Resource) (string, error) {
	entry := strings.TrimSpace(resource.Manifest.Entrypoint)
	if entry == "" {
		return "", ErrEntrypointInvalid
	}
	if filepath.IsAbs(entry) || strings.ContainsAny(entry, "\x00\r\n") {
		return "", ErrEntrypointInvalid
	}
	manifestPath, err := filepath.Abs(resource.Path)
	if err != nil {
		return "", fmt.Errorf("%w: invalid resource path", ErrEntrypointInvalid)
	}
	base, err := filepath.EvalSymlinks(filepath.Dir(manifestPath))
	if err != nil || !withinResourceDirectory(r.Root, base) {
		return "", fmt.Errorf("%w: resource directory is outside the workspace", ErrEntrypointInvalid)
	}
	path := filepath.Clean(filepath.Join(base, entry))
	if !withinResourceDirectory(base, path) {
		return "", ErrEntrypointInvalid
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil || !withinResourceDirectory(base, path) {
		return "", fmt.Errorf("%w: entrypoint is outside the resource directory", ErrEntrypointInvalid)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", ErrEntrypointInvalid, entry)
	}
	if info.Mode()&0o111 == 0 && runtime.GOOS != "windows" {
		return "", fmt.Errorf("%w: entrypoint is not executable", ErrEntrypointInvalid)
	}
	return path, nil
}

func withinResourceDirectory(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
