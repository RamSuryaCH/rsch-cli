package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/process"
)

type PersistentShell struct {
	process      *process.Handle
	stdin        io.WriteCloser
	cwd          string
	shellPath    string
	commandQueue chan *commandExecution
	done         chan struct{}
	stateMu      sync.RWMutex
	stdinMu      sync.Mutex
	isAlive      bool
	startErr     error
	closeOnce    sync.Once
	doneOnce     sync.Once
}

type commandExecution struct {
	command    string
	timeout    time.Duration
	resultChan chan commandResult
	ctx        context.Context
	started    chan struct{}
}

type commandResult struct {
	stdout      string
	stderr      string
	exitCode    int
	interrupted bool
	err         error
}

var (
	shellInstance   *PersistentShell
	shellInstanceMu sync.Mutex
)

func GetPersistentShell(workingDir string) *PersistentShell {
	shellInstanceMu.Lock()
	defer shellInstanceMu.Unlock()
	if shellInstance == nil || !shellInstance.alive() {
		shellInstance = newPersistentShell(workingDir)
	}
	return shellInstance
}

func newPersistentShell(cwd string) *PersistentShell {
	// Get shell configuration from config
	cfg := config.Get()

	// Default to environment variable if config is not set or nil
	var shellPath string
	var shellArgs []string

	if cfg != nil {
		shellPath = cfg.Shell.Path
		shellArgs = cfg.Shell.Args
	}

	if shellPath == "" {
		shellPath = os.Getenv("SHELL")
		if shellPath == "" {
			if runtime.GOOS == "windows" {
				shellPath = os.Getenv("COMSPEC")
				if shellPath == "" {
					shellPath = "cmd.exe"
				}
			} else {
				shellPath = "/bin/sh"
			}
		}
	}

	// Default shell args
	if len(shellArgs) == 0 {
		if runtime.GOOS == "windows" {
			shellArgs = []string{"/Q"}
		} else {
			shellArgs = []string{"-l"}
		}
	}

	allowedEnvironment := map[string]string{"GIT_EDITOR": "true"}
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TERM", "TMPDIR", "TEMP", "TMP", "SystemRoot", "ComSpec"} {
		if value := os.Getenv(name); value != "" {
			allowedEnvironment[name] = value
		}
	}
	shell := &PersistentShell{
		cwd:          cwd,
		shellPath:    shellPath,
		commandQueue: make(chan *commandExecution, 10),
		done:         make(chan struct{}),
	}
	pipes, err := process.StartPipes(context.Background(), process.Spec{
		Argv:             append([]string{shellPath}, shellArgs...),
		Directory:        cwd,
		Environment:      allowedEnvironment,
		ClearEnvironment: true,
	})
	if err != nil {
		shell.markDead(err)
		return shell
	}

	shell.process = pipes.Handle
	shell.stdin = pipes.Stdin
	shell.isAlive = true
	// The shell protocol redirects command output to temporary files. Drain
	// the transport streams so startup banners or diagnostics cannot fill a
	// pipe and deadlock the persistent session.
	go func() { _, _ = io.Copy(io.Discard, pipes.Stdout) }()
	go func() { _, _ = io.Copy(io.Discard, pipes.Stderr) }()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Panic in shell command processor: %v\n", r)
				if shell.process != nil {
					_ = shell.process.Kill()
				}
			}
		}()
		shell.processCommands()
	}()

	go func() {
		_, waitErr := shell.process.Wait()
		shell.markDead(waitErr)
	}()

	return shell
}

func (s *PersistentShell) processCommands() {
	for {
		select {
		case <-s.done:
			s.failQueuedCommands()
			return
		case cmd := <-s.commandQueue:
			if cmd == nil {
				continue
			}
			close(cmd.started)
			result := s.execCommand(cmd.command, cmd.timeout, cmd.ctx)
			// resultChan is buffered so a caller that has already returned on
			// cancellation never strands the command processor.
			cmd.resultChan <- result
		}
	}
}

func (s *PersistentShell) failQueuedCommands() {
	for {
		select {
		case cmd := <-s.commandQueue:
			if cmd != nil {
				cmd.resultChan <- commandResult{stderr: "Shell is not alive", exitCode: 1, err: s.terminalError()}
			}
		default:
			return
		}
	}
}

func (s *PersistentShell) execCommand(command string, timeout time.Duration, ctx context.Context) commandResult {
	if err := ctx.Err(); err != nil {
		return commandResult{stderr: err.Error(), exitCode: 130, interrupted: true, err: err}
	}
	if !s.alive() {
		return commandResult{
			stderr:   "Shell is not alive",
			exitCode: 1,
			err:      s.terminalError(),
		}
	}

	tempDir, err := os.MkdirTemp("", "opencode-shell-*")
	if err != nil {
		return commandResult{stderr: err.Error(), exitCode: 1, err: err}
	}
	defer os.RemoveAll(tempDir)
	stdoutFile := filepath.Join(tempDir, "stdout")
	stderrFile := filepath.Join(tempDir, "stderr")
	statusFile := filepath.Join(tempDir, "status")
	cwdFile := filepath.Join(tempDir, "cwd")

	fullCommand := buildCommandScript(runtime.GOOS, s.shellPath, command, stdoutFile, stderrFile, cwdFile, statusFile)

	s.stdinMu.Lock()
	_, err = s.stdin.Write([]byte(fullCommand + "\n"))
	s.stdinMu.Unlock()
	if err != nil {
		return commandResult{
			stderr:   fmt.Sprintf("Failed to write command to shell: %v", err),
			exitCode: 1,
			err:      err,
		}
	}

	interrupted := false
	var terminalErr error
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var timeoutTimer *time.Timer
	var timeoutC <-chan time.Time
	if timeout > 0 {
		timeoutTimer = time.NewTimer(timeout)
		timeoutC = timeoutTimer.C
		defer timeoutTimer.Stop()
	}
	waiting := true
	for waiting {
		select {
		case <-ctx.Done():
			interrupted = true
			terminalErr = ctx.Err()
			s.interruptCurrentCommand(statusFile)
			waiting = false
		case <-timeoutC:
			interrupted = true
			s.interruptCurrentCommand(statusFile)
			waiting = false
		case <-s.done:
			terminalErr = s.terminalError()
			waiting = false
		case <-ticker.C:
			waiting = !(fileExists(statusFile) && fileSize(statusFile) > 0)
		}
	}

	stdout := readFileOrEmpty(stdoutFile)
	stderr := readFileOrEmpty(stderrFile)
	exitCodeStr := readFileOrEmpty(statusFile)
	newCwd := readFileOrEmpty(cwdFile)

	exitCode := 0
	if exitCodeStr != "" {
		fmt.Sscanf(exitCodeStr, "%d", &exitCode)
	} else if interrupted {
		exitCode = 124
		if errors.Is(terminalErr, context.Canceled) {
			exitCode = 130
		}
		stderr += "\nCommand execution timed out or was interrupted"
	} else if terminalErr != nil {
		exitCode = 1
	}

	if newCwd != "" {
		s.stateMu.Lock()
		s.cwd = strings.TrimSpace(newCwd)
		s.stateMu.Unlock()
	}

	return commandResult{
		stdout:      stdout,
		stderr:      stderr,
		exitCode:    exitCode,
		interrupted: interrupted,
		err:         terminalErr,
	}
}

func (s *PersistentShell) interruptCurrentCommand(statusFile string) {
	if s.process == nil {
		return
	}
	_ = process.InterruptChildren(s.process.PID())
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fileExists(statusFile) && fileSize(statusFile) > 0 {
			return
		}
		select {
		case <-s.done:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	// The shared process boundary kills the entire process group. Sacrificing
	// this shell is preferable to allowing an uncooperative descendant to
	// survive cancellation; GetPersistentShell recreates it on the next call.
	_ = s.process.Kill()
	// Kill is only a signal request. Do not report cancellation as complete
	// until the shell process has actually been reaped, within a final bounded
	// grace period. This keeps descendants from lingering after Exec returns.
	select {
	case <-s.done:
	case <-time.After(500 * time.Millisecond):
	}
}

func (s *PersistentShell) Exec(ctx context.Context, command string, timeoutMs int) (string, string, int, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err.Error(), 130, true, err
	}
	if !s.alive() {
		return "", "Shell is not alive", 1, false, s.terminalError()
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond

	resultChan := make(chan commandResult, 1)
	execution := &commandExecution{
		command:    command,
		timeout:    timeout,
		resultChan: resultChan,
		ctx:        ctx,
		started:    make(chan struct{}),
	}
	select {
	case s.commandQueue <- execution:
	case <-ctx.Done():
		return "", ctx.Err().Error(), 130, true, ctx.Err()
	case <-s.done:
		return "", "Shell is not alive", 1, false, s.terminalError()
	}
	select {
	case result := <-resultChan:
		return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
	case <-ctx.Done():
		// If this execution is already active, wait for its bounded interrupt and
		// reap path. A queued execution has not launched work and can return
		// immediately; the processor will later observe its canceled context.
		select {
		case <-execution.started:
			select {
			case result := <-resultChan:
				return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
			case <-s.done:
				select {
				case result := <-resultChan:
					return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
				case <-time.After(250 * time.Millisecond):
				}
			case <-time.After(time.Second):
			}
		default:
		}
		return "", ctx.Err().Error(), 130, true, ctx.Err()
	case <-s.done:
		select {
		case result := <-resultChan:
			return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
		case <-time.After(100 * time.Millisecond):
			return "", "Shell is not alive", 1, false, s.terminalError()
		}
	}
}

func (s *PersistentShell) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.stateMu.Lock()
		s.isAlive = false
		s.stateMu.Unlock()
		if s.stdin != nil {
			s.stdinMu.Lock()
			_, _ = s.stdin.Write([]byte("exit\n"))
			_ = s.stdin.Close()
			s.stdinMu.Unlock()
		}
		select {
		case <-s.done:
			return
		case <-time.After(500 * time.Millisecond):
		}
		if s.process != nil {
			_ = s.process.Kill()
		}
		select {
		case <-s.done:
		case <-time.After(500 * time.Millisecond):
		}
	})
}

func (s *PersistentShell) alive() bool {
	if s == nil {
		return false
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.isAlive
}

func (s *PersistentShell) terminalError() error {
	if s == nil {
		return errors.New("shell is not available")
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.startErr != nil {
		return s.startErr
	}
	return errors.New("shell is not alive")
}

func (s *PersistentShell) markDead(err error) {
	s.stateMu.Lock()
	s.isAlive = false
	if err != nil && s.startErr == nil {
		s.startErr = err
	}
	s.stateMu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func buildCommandScript(goos, shellPath, command, stdoutFile, stderrFile, cwdFile, statusFile string) string {
	if goos == "windows" {
		base := strings.ToLower(filepath.Base(shellPath))
		if strings.Contains(base, "powershell") || strings.Contains(base, "pwsh") {
			return fmt.Sprintf("& { %s 1> %s 2> %s; $EXEC_EXIT_CODE = $LASTEXITCODE; (Get-Location).Path | Set-Content -NoNewline %s; $EXEC_EXIT_CODE | Set-Content -NoNewline %s }\r\n", command, powershellQuote(stdoutFile), powershellQuote(stderrFile), powershellQuote(cwdFile), powershellQuote(statusFile))
		}
		return fmt.Sprintf("%s < nul > %s 2> %s\r\nset EXEC_EXIT_CODE=%%ERRORLEVEL%%\r\ncd > %s\necho %%EXEC_EXIT_CODE%% > %s\r\n", command, windowsQuote(stdoutFile), windowsQuote(stderrFile), windowsQuote(cwdFile), windowsQuote(statusFile))
	}
	return fmt.Sprintf("\neval %s < /dev/null > %s 2> %s\nEXEC_EXIT_CODE=$?\npwd > %s\necho $EXEC_EXIT_CODE > %s\n", shellQuote(command), shellQuote(stdoutFile), shellQuote(stderrFile), shellQuote(cwdFile), shellQuote(statusFile))
}

func windowsQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func readFileOrEmpty(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
