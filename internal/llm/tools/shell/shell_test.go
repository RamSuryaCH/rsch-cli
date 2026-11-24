package shell

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuildCommandScriptUsesWindowsCmdSyntax(t *testing.T) {
	script := buildCommandScript("windows", "cmd.exe", "echo hello", `C:\\tmp\\stdout`, `C:\\tmp\\stderr`, `C:\\tmp\\cwd`, `C:\\tmp\\status`)
	if strings.Contains(script, "eval") || strings.Contains(script, "$?") || !strings.Contains(script, "%ERRORLEVEL%") || !strings.Contains(script, "< nul") {
		t.Fatalf("unexpected cmd script: %q", script)
	}
}

func TestBuildCommandScriptUsesPowerShellSyntax(t *testing.T) {
	script := buildCommandScript("windows", "pwsh.exe", "Write-Output hello", `C:\\tmp\\stdout`, `C:\\tmp\\stderr`, `C:\\tmp\\cwd`, `C:\\tmp\\status`)
	if !strings.Contains(script, "$LASTEXITCODE") || !strings.Contains(script, "Set-Content") || strings.Contains(script, "%ERRORLEVEL%") {
		t.Fatalf("unexpected PowerShell script: %q", script)
	}
}

func TestExecHonorsContextWhileQueueing(t *testing.T) {
	shell := &PersistentShell{
		isAlive:      true,
		commandQueue: make(chan *commandExecution),
		done:         make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, exitCode, interrupted, err := shell.Exec(ctx, "echo never-enqueued", 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if !interrupted || exitCode != 130 {
		t.Fatalf("unexpected cancellation result: exit=%d interrupted=%t", exitCode, interrupted)
	}
	if time.Since(started) > time.Second {
		t.Fatal("queueing did not stop promptly after cancellation")
	}
}

func TestExecHonorsContextWhileWaitingForResult(t *testing.T) {
	shell := &PersistentShell{
		isAlive:      true,
		commandQueue: make(chan *commandExecution, 1),
		done:         make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, _, interrupted, err := shell.Exec(ctx, "echo queued", 0)
	if !errors.Is(err, context.DeadlineExceeded) || !interrupted {
		t.Fatalf("unexpected cancellation result: interrupted=%t err=%v", interrupted, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("result wait did not stop promptly after cancellation")
	}
}

func TestTimeoutEscalatesAndRetiresUncooperativeShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell syntax")
	}
	shell := newPersistentShell(t.TempDir())
	if !shell.alive() {
		t.Fatalf("failed to start persistent shell: %v", shell.terminalError())
	}
	defer shell.Close()
	started := time.Now()
	_, _, exitCode, interrupted, err := shell.Exec(context.Background(), "trap '' INT; while :; do :; done", 50)
	if err != nil {
		t.Fatalf("timeout returned unexpected transport error: %v", err)
	}
	if !interrupted || exitCode != 124 {
		t.Fatalf("unexpected timeout result: exit=%d interrupted=%t", exitCode, interrupted)
	}
	deadline := time.Now().Add(time.Second)
	for shell.alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if shell.alive() {
		t.Fatal("uncooperative command left the persistent shell alive")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("timeout escalation exceeded its bounded grace period")
	}
}
