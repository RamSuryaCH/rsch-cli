// Package process is the platform-aware process boundary used by workflow
// runtimes. Policy and sandbox decisions remain owned by their callers; this
// package provides consistent argv, environment, timeout, exit-code, and
// cancellation semantics once a process has been authorized.
package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type Spec struct {
	Argv             []string
	Directory        string
	Environment      map[string]string
	ClearEnvironment bool
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	// MaxOutputBytes bounds the combined stdout/stderr captured by RunOutput.
	// Zero leaves capture unlimited for backwards compatibility. The process is
	// terminated as soon as the limit is exceeded, even if it stops writing.
	MaxOutputBytes int64
	// Detached keeps a successfully started process alive after the launcher
	// exits. Platform containment still applies, but the containment handle
	// must not use a parent-close kill policy.
	Detached bool
}

type Result struct {
	Output          []byte
	OutputTruncated bool
	ExitCode        int
	StartedAt       time.Time
	FinishedAt      time.Time
}

var (
	ErrCommandRequired     = errors.New("process command is required")
	ErrOutputLimitExceeded = errors.New("process output limit exceeded")
)

type Handle struct {
	command  *exec.Cmd
	context  context.Context
	started  time.Time
	done     chan struct{}
	killOnce sync.Once
	killErr  error
	waitOnce sync.Once
	result   Result
	err      error
	output   *lockedBuffer
	cleanup  func()
}

// Pipes contains standard streams for a long-lived protocol process while
// Handle retains ownership of lifecycle, cancellation, and exit state.
type Pipes struct {
	Handle *Handle
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
}

func Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// LookPath resolves an executable for capability checks without exposing the
// os/exec package to callers that only need platform process services.
func LookPath(name string) (string, error) { return exec.LookPath(name) }

// Run executes an already-authorized argv without invoking a shell. The
// platform adapter gives cancellation a chance to terminate the complete
// process group on Unix while remaining buildable on Windows.
func Run(ctx context.Context, spec Spec) (Result, error) {
	return RunOutput(ctx, spec)
}

// RunOutput is Run with deterministic combined stdout/stderr capture. It is
// separate from Run so callers that need custom stdio can grow without
// changing the process contract.
func RunOutput(ctx context.Context, spec Spec) (Result, error) {
	handle, err := Start(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	return handle.Wait()
}

// Start launches an authorized process and returns a lifecycle handle for
// persistent jobs. If stdout and stderr are not supplied, output is captured
// safely for Wait; callers managing live logs can provide their own writers.
func Start(ctx context.Context, spec Spec) (*Handle, error) {
	command, err := newCommand(ctx, spec)
	if err != nil {
		return nil, err
	}
	output := (*lockedBuffer)(nil)
	if spec.Stdout == nil && spec.Stderr == nil {
		output = &lockedBuffer{maxBytes: spec.MaxOutputBytes, limitExceeded: make(chan struct{})}
		command.Stdout = output
		command.Stderr = output
	} else {
		command.Stdout = spec.Stdout
		command.Stderr = spec.Stderr
	}
	started := time.Now().UTC()
	if err := command.Start(); err != nil {
		return nil, err
	}
	cleanup, err := attach(command, spec.Detached)
	if err != nil {
		_ = command.Process.Kill()
		return nil, err
	}
	handle := &Handle{command: command, context: ctx, started: started, done: make(chan struct{}), output: output, cleanup: cleanup}
	watchCancellation(ctx, handle)
	return handle, nil
}

// StartPipes starts an authorized process for a long-lived streaming
// protocol such as LSP. One-shot work should use Start or RunOutput.
func StartPipes(ctx context.Context, spec Spec) (Pipes, error) {
	command, err := newCommand(ctx, spec)
	if err != nil {
		return Pipes{}, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return Pipes{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return Pipes{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return Pipes{}, err
	}
	started := time.Now().UTC()
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return Pipes{}, err
	}
	cleanup, err := attach(command, spec.Detached)
	if err != nil {
		_ = command.Process.Kill()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return Pipes{}, err
	}
	handle := &Handle{command: command, context: ctx, started: started, done: make(chan struct{}), cleanup: cleanup}
	watchCancellation(ctx, handle)
	return Pipes{Handle: handle, Stdin: stdin, Stdout: stdout, Stderr: stderr}, nil
}

func newCommand(ctx context.Context, spec Spec) (*exec.Cmd, error) {
	if len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		return nil, ErrCommandRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	command.Dir = spec.Directory
	command.Stdin = spec.Stdin
	command.Env = environment(spec)
	configure(command)
	return command, nil
}

func watchCancellation(ctx context.Context, handle *Handle) {
	var outputLimit <-chan struct{}
	if handle.output != nil {
		outputLimit = handle.output.limitExceeded
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = handle.Kill()
		case <-outputLimit:
			_ = handle.Kill()
		case <-handle.done:
		}
	}()
}

func (h *Handle) Wait() (Result, error) {
	if h == nil || h.command == nil {
		return Result{}, errors.New("process handle is required")
	}
	h.waitOnce.Do(func() {
		waitErr := h.command.Wait()
		h.err = waitErr
		if contextErr := h.context.Err(); contextErr != nil {
			// Preserve both the cancellation reason and the process exit status
			// so callers can distinguish a deadline from a tool failure.
			h.err = errors.Join(waitErr, contextErr)
		}
		h.result = Result{ExitCode: exitCode(waitErr), StartedAt: h.started, FinishedAt: time.Now().UTC()}
		if h.output != nil {
			h.result.Output = h.output.Bytes()
			h.result.OutputTruncated = h.output.Truncated()
			if h.result.OutputTruncated {
				h.err = ErrOutputLimitExceeded
			}
		}
		if h.cleanup != nil {
			h.cleanup()
		}
		close(h.done)
	})
	return h.result, h.err
}

func (h *Handle) Kill() error {
	if h == nil || h.command == nil {
		return errors.New("process handle is required")
	}
	h.killOnce.Do(func() {
		// The command may have exited between Start and Kill. The platform
		// adapter treats that case as a successful no-op.
		if h.command.Process != nil {
			h.killErr = terminate(h.command)
		}
	})
	return h.killErr
}

func (h *Handle) PID() int {
	if h == nil || h.command == nil || h.command.Process == nil {
		return 0
	}
	return h.command.Process.Pid
}

// Path returns the resolved executable path without exposing exec.Cmd.
func (h *Handle) Path() string {
	if h == nil || h.command == nil {
		return ""
	}
	return h.command.Path
}

// Alive reports whether a process identified by pid still exists. It is
// intentionally implemented by the platform adapter because process
// inspection and permission semantics differ between Unix and Windows.
func Alive(pid int) bool { return alive(pid) }

// Interrupt requests graceful cancellation of a persisted process. Callers
// should follow it with Terminate after a bounded grace period.
func Interrupt(pid int) error { return interrupt(pid) }

// InterruptChildren requests cancellation of children while leaving a
// persistent parent protocol process available when the platform supports
// that distinction.
func InterruptChildren(pid int) error { return interruptChildren(pid) }

// Terminate forcefully stops a persisted process and its process group where
// the platform supports process groups.
func Terminate(pid int) error { return terminatePID(pid) }

// lockedBuffer is shared by stdout and stderr copy goroutines. os/exec does
// not serialize those streams when both are directed to one writer.
type lockedBuffer struct {
	mu            sync.Mutex
	maxBytes      int64
	truncated     bool
	buffer        bytes.Buffer
	limitExceeded chan struct{}
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxBytes <= 0 {
		return b.buffer.Write(value)
	}
	remaining := b.maxBytes - int64(b.buffer.Len())
	if remaining <= 0 {
		b.markTruncated()
		return 0, ErrOutputLimitExceeded
	}
	if int64(len(value)) <= remaining {
		return b.buffer.Write(value)
	}
	written, _ := b.buffer.Write(value[:int(remaining)])
	b.markTruncated()
	return written, ErrOutputLimitExceeded
}

// markTruncated runs under mu so stdout and stderr can signal the limit only
// once. The buffered output remains available after the process is stopped.
func (b *lockedBuffer) markTruncated() {
	if !b.truncated {
		b.truncated = true
		if b.limitExceeded != nil {
			close(b.limitExceeded)
		}
	}
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Len()
}

func (b *lockedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func environment(spec Spec) []string {
	base := []string{}
	if !spec.ClearEnvironment {
		base = append(base, os.Environ()...)
	}
	if len(spec.Environment) == 0 {
		return base
	}
	values := make(map[string]string, len(base)+len(spec.Environment))
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	for key, value := range spec.Environment {
		if strings.TrimSpace(key) != "" {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
