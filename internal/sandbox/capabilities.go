// Package sandbox contains platform capability negotiation. Callers must
// inspect the returned capability before claiming isolation is available.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/opencode-ai/opencode/internal/process"
)

type Mode string

type State string

const (
	Available          State = "available"
	Unavailable        State = "unavailable"
	Degraded           State = "degraded"
	Unsupported        State = "unsupported"
	RequiredButMissing State = "required-but-missing"
)

const (
	ReadOnly       Mode = "read-only"
	WorkspaceWrite Mode = "workspace-write"
	FullAccess     Mode = "full-access"
)

type Capability struct {
	Platform            string `json:"platform"`
	State               State  `json:"state"`
	Isolated            bool   `json:"isolated"`
	Backend             string `json:"backend"`
	Reason              string `json:"reason,omitempty"`
	Supported           bool   `json:"supported"`
	FilesystemIsolation bool   `json:"filesystemIsolation"`
	NetworkIsolation    bool   `json:"networkIsolation"`
	ProcessIsolation    bool   `json:"processIsolation"`
}

func Detect() Capability {
	capability := Capability{Platform: runtime.GOOS + "/" + runtime.GOARCH, State: Unsupported}
	switch runtime.GOOS {
	case "darwin":
		if commandExists("sandbox-exec") {
			capability.State = Available
			capability.Isolated, capability.Supported = true, true
			capability.FilesystemIsolation, capability.NetworkIsolation, capability.ProcessIsolation = true, true, true
			capability.Backend = "seatbelt"
		} else {
			capability.State = RequiredButMissing
			capability.Reason = "sandbox-exec is unavailable"
		}
	case "linux":
		if commandExists("bwrap") {
			capability.State = Available
			capability.Isolated, capability.Supported = true, true
			capability.FilesystemIsolation, capability.NetworkIsolation, capability.ProcessIsolation = true, true, true
			capability.Backend = "bubblewrap"
		} else if commandExists("firejail") {
			capability.State = Degraded
			capability.Backend = "firejail"
			capability.Reason = "bubblewrap is required: the firejail adapter cannot enforce workspace filesystem isolation"
		} else {
			capability.State = RequiredButMissing
			capability.Reason = "bubblewrap and firejail are unavailable"
		}
	case "windows":
		if wrapper := strings.TrimSpace(os.Getenv("RSCH_WINDOWS_SANDBOX_COMMAND")); wrapper != "" && commandExists(wrapper) {
			capability.State = Available
			capability.Isolated, capability.Supported = true, true
			capability.FilesystemIsolation, capability.NetworkIsolation, capability.ProcessIsolation = true, true, true
			capability.Backend = "windows-configured-wrapper"
		} else {
			// The process boundary always attaches a native Job Object on
			// Windows. It contains descendants, but it does not by itself
			// provide filesystem or network isolation, so resource execution
			// remains fail-closed until a full wrapper is configured.
			capability.State = Degraded
			capability.Supported = true
			capability.ProcessIsolation = true
			capability.Backend = "windows-job-object"
			capability.Reason = "native Job Object provides process containment; filesystem and network isolation require RSCH_WINDOWS_SANDBOX_COMMAND"
		}
	default:
		capability.Reason = "no supported sandbox backend for this platform"
	}
	return capability
}

// Probe validates that the detected backend can execute a harmless command in
// the requested workspace. This separates executable discovery from runtime
// availability, which is important on locked-down hosts and CI runners.
func Probe(ctx context.Context, workingDirectory string) Capability {
	capability := Detect()
	if !capability.Isolated {
		return capability
	}
	runner := PlatformRunner{Capability: capability, WorkingDirectory: workingDirectory, Mode: ReadOnly, AllowNetwork: false}
	if _, err := runner.Run(ctx, []string{"true"}); err != nil {
		capability.State = Degraded
		capability.Isolated = false
		capability.Reason = "sandbox probe failed: " + err.Error()
	}
	return capability
}
func commandExists(name string) bool { return process.Exists(name) }

type Runner interface {
	Run(context.Context, []string) ([]byte, error)
}

// EnvironmentRunner is implemented by runners that can apply a resource's
// explicit environment without inheriting the caller's environment.
type EnvironmentRunner interface {
	RunWithEnvironment(context.Context, []string, map[string]string) ([]byte, error)
}

var ErrUnavailable = errors.New("platform sandbox is unavailable")
var ErrInsufficientCapability = errors.New("platform sandbox capability is insufficient")

type RejectingRunner struct{ Capability Capability }

func (r RejectingRunner) Run(context.Context, []string) ([]byte, error) {
	if !r.Capability.Isolated {
		return nil, ErrUnavailable
	}
	return nil, errors.New("configured backend requires a platform adapter")
}

// PlatformRunner executes an argv command through the detected OS isolation
// backend. It never invokes a shell. A backend that cannot be configured is
// rejected before a child process is started.
type PlatformRunner struct {
	Capability       Capability
	WorkingDirectory string
	Mode             Mode
	AllowNetwork     bool
	MaxOutputBytes   int64
}

func NewPlatformRunner(workingDirectory string) PlatformRunner {
	return PlatformRunner{Capability: Detect(), WorkingDirectory: workingDirectory, Mode: WorkspaceWrite}
}

func (r PlatformRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	return r.RunWithEnvironment(ctx, argv, nil)
}

func (r PlatformRunner) RunWithEnvironment(ctx context.Context, argv []string, environment map[string]string) ([]byte, error) {
	command, err := r.Wrap(argv)
	if err != nil {
		return nil, err
	}
	workingDirectory := r.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = "."
	}
	workingDirectory, err = filepath.Abs(workingDirectory)
	if err != nil {
		return nil, err
	}
	filtered := make(map[string]string, len(environment)+1)
	keys := make([]string, 0, len(environment))
	for key := range environment {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		filtered[key] = environment[key]
	}
	// Keep executable discovery available without inheriting arbitrary
	// caller secrets, including when no explicit environment was supplied.
	if _, ok := filtered["PATH"]; !ok {
		if path := os.Getenv("PATH"); path != "" {
			filtered["PATH"] = path
		}
	}
	result, err := process.RunOutput(ctx, process.Spec{Argv: command, Directory: workingDirectory, Environment: filtered, ClearEnvironment: true, MaxOutputBytes: r.MaxOutputBytes})
	return result.Output, err
}

// Wrap returns the argv for a streaming or otherwise caller-managed process
// under the same isolation policy used by RunWithEnvironment.
func (r PlatformRunner) Wrap(argv []string) ([]string, error) {
	if err := validateCapability(r.Capability, r.Mode); err != nil {
		return nil, err
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("sandbox command is required")
	}
	workingDirectory := r.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = "."
	}
	workingDirectory, err := filepath.Abs(workingDirectory)
	if err != nil {
		return nil, err
	}
	var command []string
	switch r.Capability.Backend {
	case "seatbelt":
		return seatbeltCommand(workingDirectory, r.Mode, r.AllowNetwork, argv)
	case "bubblewrap":
		command = bubblewrapCommand(workingDirectory, r.Mode, r.AllowNetwork, argv)
	case "windows-configured-wrapper":
		wrapper := strings.TrimSpace(os.Getenv("RSCH_WINDOWS_SANDBOX_COMMAND"))
		command = append([]string{wrapper}, argv...)
	default:
		return nil, fmt.Errorf("sandbox backend %q is unsupported", r.Capability.Backend)
	}
	return command, nil
}

func seatbeltCommand(workingDirectory string, mode Mode, allowNetwork bool, argv []string) ([]string, error) {
	// Seatbelt matches the path the kernel resolves, so a lexical alias (such
	// as /var on macOS) cannot define the workspace grant.
	workspace, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox workspace: %w", err)
	}
	profile := seatbeltProfile(workspace, mode, allowNetwork)
	for _, name := range []string{".git", ".rsch"} {
		path := filepath.Join(workspace, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect sandbox protected path: %w", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			// A broken or cyclic link cannot safely identify the control-plane
			// target, which might otherwise be created by the child later.
			return nil, fmt.Errorf("resolve sandbox protected path: %w", err)
		}
		profile += fmt.Sprintf("(deny file-write* (subpath %s))", strconv.Quote(target))
	}
	return append([]string{"sandbox-exec", "-p", profile}, argv...), nil
}

func seatbeltProfile(workingDirectory string, mode Mode, allowNetwork bool) string {
	// macOS 27's libignition opens the filesystem root as an openat(2) anchor
	// while starting native executables. Granting read-data on that literal path
	// is sufficient for dyld bootstrap without making any child of / readable.
	path := strconv.Quote(workingDirectory)
	profile := `(version 1)(deny default)(allow process-exec)(allow process-fork)(allow file-read-data (literal "/"))`
	profile += fmt.Sprintf("(allow file-read* (subpath %s))", path)
	if mode != ReadOnly {
		profile += fmt.Sprintf("(allow file-write* (subpath %s))", path)
	}
	profile += fmt.Sprintf("(deny file-write* (subpath %s))", strconv.Quote(filepath.Join(workingDirectory, ".git")))
	profile += fmt.Sprintf("(deny file-write* (subpath %s))", strconv.Quote(filepath.Join(workingDirectory, ".rsch")))
	if allowNetwork {
		profile += "(allow network*)"
	} else {
		profile += "(deny network*)"
	}
	profile += "(allow mach-lookup)"
	return profile
}

var bubblewrapRuntimePaths = []string{
	"/bin",
	"/sbin",
	"/usr",
	"/lib",
	"/lib32",
	"/lib64",
	"/libx32",
	"/nix/store",
	"/etc/alternatives",
	"/etc/ca-certificates",
	"/etc/ssl",
	"/etc/ld.so.cache",
	"/etc/ld.so.conf",
	"/etc/ld.so.conf.d",
	"/etc/localtime",
	"/etc/nsswitch.conf",
	"/etc/passwd",
	"/etc/group",
	"/etc/hosts",
	"/etc/resolv.conf",
}

func bubblewrapCommand(workingDirectory string, mode Mode, allowNetwork bool, argv []string) []string {
	command := []string{"bwrap", "--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts"}
	if !allowNetwork {
		command = append(command, "--unshare-net")
	}
	command = append(command, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	for _, path := range bubblewrapRuntimePaths {
		command = appendBubblewrapReadOnlyPath(command, path)
	}

	// A directly configured executable outside the system runtime roots is
	// exposed as a single read-only file, never by mounting its parent (which
	// could be a user home containing credentials).
	wrappedArgv := append([]string(nil), argv...)
	if executable, err := process.LookPath(argv[0]); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		if absolute, absoluteErr := filepath.Abs(executable); absoluteErr == nil {
			executable = absolute
		}
		if !pathWithin(executable, workingDirectory) && !pathWithinAny(executable, bubblewrapRuntimePaths) {
			command = appendBubblewrapReadOnlyPath(command, executable)
			wrappedArgv[0] = executable
		}
	}

	workspaceMount := "--ro-bind"
	if mode != ReadOnly {
		workspaceMount = "--bind"
	}
	command = append(command, workspaceMount, workingDirectory, workingDirectory)
	if mode != ReadOnly {
		// The repository control plane and RSCH state remain read-only even
		// when the source workspace is writable.
		for _, protected := range []string{filepath.Join(workingDirectory, ".git"), filepath.Join(workingDirectory, ".rsch")} {
			if _, err := os.Stat(protected); err == nil {
				command = append(command, "--ro-bind", protected, protected)
			}
		}
	}
	command = append(command, "--chdir", workingDirectory)
	return append(command, wrappedArgv...)
}

func appendBubblewrapReadOnlyPath(command []string, path string) []string {
	info, err := os.Lstat(path)
	if err != nil {
		return command
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return command
		}
		return append(command, "--symlink", target, path)
	}
	return append(command, "--ro-bind", path, path)
}

func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func pathWithinAny(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithin(path, root) {
			return true
		}
	}
	return false
}

func validateCapability(capability Capability, mode Mode) error {
	if !capability.Isolated {
		return ErrUnavailable
	}
	if !capability.Supported || capability.Backend == "" {
		return ErrInsufficientCapability
	}
	if !capability.FilesystemIsolation || !capability.NetworkIsolation || !capability.ProcessIsolation {
		return ErrInsufficientCapability
	}
	switch mode {
	case ReadOnly, WorkspaceWrite, FullAccess:
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInsufficientCapability, mode)
	}
	switch capability.Backend {
	case "seatbelt", "bubblewrap", "windows-configured-wrapper":
		return nil
	default:
		return fmt.Errorf("%w: unsupported backend %q", ErrInsufficientCapability, capability.Backend)
	}
}
