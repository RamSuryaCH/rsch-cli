package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/sandbox"
)

type bashPermissionStub struct {
	permission.Service
	allow bool
}

func (p bashPermissionStub) RequestContext(context.Context, permission.CreatePermissionRequest) bool {
	return p.allow
}

func bashToolTestContext() context.Context {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "session-1")
	return context.WithValue(ctx, MessageIDContextKey, "message-1")
}

func completeSandboxCapability() sandbox.Capability {
	return sandbox.Capability{
		State:               sandbox.Available,
		Isolated:            true,
		Supported:           true,
		Backend:             "seatbelt",
		FilesystemIsolation: true,
		NetworkIsolation:    true,
		ProcessIsolation:    true,
	}
}

func newSandboxedBashTestTool(run bashSandboxRun) *bashTool {
	return &bashTool{
		permissions:      bashPermissionStub{allow: true},
		workingDirectory: func() string { return "/workspace" },
		probeSandbox: func(context.Context, string) sandbox.Capability {
			return completeSandboxCapability()
		},
		runSandbox: run,
		shellCommand: func(command string) ([]string, error) {
			return []string{"/bin/sh", "-c", command}, nil
		},
		environment: bashEnvironment,
	}
}

func TestValidateShellCommandRejectsChainingAndSubstitution(t *testing.T) {
	for _, command := range []string{"echo ok; rm -rf /", "echo ok && curl https://example.com", "sleep 10 &", "echo $(whoami)", "cat < secret"} {
		if err := validateShellCommand(command); err == nil {
			t.Fatalf("unsafe shell command was accepted: %q", command)
		}
	}
	if err := validateShellCommand("go test ./internal/llm/tools -run TestSafe"); err != nil {
		t.Fatalf("safe command was rejected: %v", err)
	}
}

func TestValidateShellCommandRejectsBannedAndWrapperBypasses(t *testing.T) {
	for _, command := range []string{
		"curl https://example.com",
		"/usr/bin/curl https://example.com",
		"'/usr/bin/curl' https://example.com",
		"HTTP_PROXY=none /usr/bin/wget https://example.com",
		"PATH+=:/tmp /usr/bin/curl https://example.com",
		"env curl https://example.com",
		"/usr/bin/env curl https://example.com",
		"command wget https://example.com",
		"bash -c 'curl https://example.com'",
		"nohup curl https://example.com",
	} {
		if err := validateShellCommand(command); err == nil {
			t.Errorf("banned or wrapped command was accepted: %q", command)
		}
	}
}

func TestSafeReadOnlyCommandUsesExactParsedSubcommands(t *testing.T) {
	for _, command := range []string{
		"ls -la",
		"date",
	} {
		if !isSafeReadOnlyCommand(command) {
			t.Errorf("read-only command was not classified safe: %q", command)
		}
	}

	for _, command := range []string{
		"./ls -la",
		"/bin/ls -la",
		"/workspace/custom/ls -la",
		"LS -la",
		"ls.exe -la",
		"git status --short",
		"git diff --no-ext-diff",
		"git config --get user.name",
		"go version",
		"go help",
		"git status-malicious",
		"git branch new-branch",
		"git tag v1.0.0",
		"git remote add origin example",
		"git config user.name value",
		"git diff --output=changes.patch",
		"git diff --out=changes.patch",
		"git grep --open-files-in-pager=vim needle",
		"git grep --open-files=vim needle",
		"git grep -Ovim needle",
		"git grep -nOvim needle",
		"git grep -e -- -Ovim",
		"go test ./...",
		"go run ./cmd/tool",
		"kill 123",
		"killall worker",
		"unset PATH",
		"timeout 1 ls",
		"hostname changed",
		"MODE=test ls",
	} {
		if isSafeReadOnlyCommand(command) {
			t.Errorf("mutating or ambiguous command was classified safe: %q", command)
		}
	}
}

func TestParseShellWordsRejectsIncompleteQuoting(t *testing.T) {
	for _, command := range []string{"   ", "echo 'unfinished", `echo unfinished\`} {
		if err := validateShellCommand(command); err == nil {
			t.Errorf("incomplete command was accepted: %q", command)
		}
	}
}

func TestBashGitUnsafeOptionAliasesRequireApproval(t *testing.T) {
	for _, command := range []string{"git grep -Ovim needle", "git grep --open-files=vim needle", "git diff --out=changes.patch"} {
		t.Run(command, func(t *testing.T) {
			runCalled := false
			tool := newSandboxedBashTestTool(func(context.Context, sandbox.PlatformRunner, []string, map[string]string) ([]byte, error) {
				runCalled = true
				return nil, nil
			})
			tool.permissions = bashPermissionStub{allow: false}
			input, err := json.Marshal(BashParams{Command: command})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tool.Run(bashToolTestContext(), ToolCall{Input: string(input)})
			if !errors.Is(err, permission.ErrorPermissionDenied) || runCalled {
				t.Fatalf("unsafe git alias bypassed approval: run=%v err=%v", runCalled, err)
			}
		})
	}
}

func TestBashAmbiguousReadCommandsRequireApproval(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		shell   []string
		path    string
	}{
		{name: "relative executable", command: "./ls"},
		{name: "arbitrary executable path", command: "/workspace/ls"},
		{name: "case alias", command: "LS"},
		{name: "repository Git helpers", command: "git status"},
		{name: "workspace Go toolchain", command: "go version"},
		{name: "workspace PATH", command: "ls", path: "/workspace/bin:/bin:/usr/bin"},
		{name: "relative PATH", command: "ls", path: ".:/bin:/usr/bin"},
		{name: "custom shell", command: "echo safe", shell: []string{"/workspace/sh", "-c"}},
		{name: "login shell", command: "echo safe", shell: []string{"/bin/sh", "-l", "-c"}},
		{name: "zsh startup file", command: "echo safe", shell: []string{"/bin/zsh", "-c"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runCalled := false
			tool := newSandboxedBashTestTool(func(context.Context, sandbox.PlatformRunner, []string, map[string]string) ([]byte, error) {
				runCalled = true
				return nil, nil
			})
			tool.permissions = bashPermissionStub{allow: false}
			tool.environment = func() map[string]string {
				path := test.path
				if path == "" {
					path = "/bin:/usr/bin"
				}
				return map[string]string{"PATH": path}
			}
			if test.shell != nil {
				tool.shellCommand = func(command string) ([]string, error) {
					return append(append([]string(nil), test.shell...), command), nil
				}
			}
			input, err := json.Marshal(BashParams{Command: test.command})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tool.Run(bashToolTestContext(), ToolCall{Input: string(input)})
			if !errors.Is(err, permission.ErrorPermissionDenied) || runCalled {
				t.Fatalf("ambiguous execution inherited a read exemption: run=%v err=%v", runCalled, err)
			}
		})
	}
}

func TestBashSystemReadCommandRetainsExemption(t *testing.T) {
	tool := newSandboxedBashTestTool(func(context.Context, sandbox.PlatformRunner, []string, map[string]string) ([]byte, error) {
		return []byte("safe"), nil
	})
	tool.permissions = bashPermissionStub{allow: false}
	tool.environment = func() map[string]string { return map[string]string{"PATH": "/bin:/usr/bin"} }
	response, err := tool.Run(bashToolTestContext(), ToolCall{Input: `{"command":"ls -la"}`})
	if err != nil || response.IsError || response.Content != "safe" {
		t.Fatalf("trusted read command lost its exemption: response=%+v err=%v", response, err)
	}
}

func TestBashFailureResponsesAreErrors(t *testing.T) {
	metadata := BashResponseMetadata{StartTime: 1, EndTime: 2}
	for _, test := range []struct {
		name        string
		exitCode    int
		interrupted bool
		wantText    string
	}{
		{name: "nonzero exit", exitCode: 2, wantText: "Exit code 2"},
		{name: "interrupted", exitCode: 143, interrupted: true, wantText: "Command was aborted before completion"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := formatBashResponse("", "", test.exitCode, test.interrupted, metadata)
			if !response.IsError {
				t.Fatal("failed command returned a successful tool response")
			}
			if !strings.Contains(response.Content, test.wantText) {
				t.Fatalf("response %q does not contain %q", response.Content, test.wantText)
			}
			if strings.HasPrefix(response.Content, "\n") {
				t.Fatalf("response has an unexpected leading newline: %q", response.Content)
			}
		})
	}

	response := formatBashResponse("ok", "", 0, false, metadata)
	if response.IsError || response.Content != "ok" {
		t.Fatalf("successful command returned %+v", response)
	}
}

func TestBashSandboxUnavailableFailsClosedBeforeExecution(t *testing.T) {
	runCalled := false
	tool := newSandboxedBashTestTool(func(context.Context, sandbox.PlatformRunner, []string, map[string]string) ([]byte, error) {
		runCalled = true
		return []byte("must not run"), nil
	})
	tool.probeSandbox = func(context.Context, string) sandbox.Capability {
		return sandbox.Capability{State: sandbox.RequiredButMissing, Reason: "sandbox backend missing"}
	}

	response, err := tool.Run(bashToolTestContext(), ToolCall{Input: `{"command":"echo safe"}`})
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("expected sandbox unavailable error, got %v", err)
	}
	if !response.IsError || !strings.Contains(response.Content, "sandbox backend missing") {
		t.Fatalf("unexpected fail-closed response: %+v", response)
	}
	if runCalled {
		t.Fatal("command runner was called without an available sandbox")
	}
}

func TestBashUsesWorkspaceWriteNetworkIsolationAndCleanEnvironment(t *testing.T) {
	t.Setenv("PATH", "/safe/bin")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	var observedRunner sandbox.PlatformRunner
	var observedArgv []string
	var observedEnvironment map[string]string
	tool := newSandboxedBashTestTool(func(_ context.Context, runner sandbox.PlatformRunner, argv []string, environment map[string]string) ([]byte, error) {
		observedRunner = runner
		observedArgv = append([]string(nil), argv...)
		observedEnvironment = environment
		return []byte("ok"), nil
	})

	response, err := tool.Run(bashToolTestContext(), ToolCall{Input: `{"command":"echo safe"}`})
	if err != nil || response.IsError || response.Content != "ok" {
		t.Fatalf("unexpected execution result: response=%+v err=%v", response, err)
	}
	if observedRunner.Mode != sandbox.WorkspaceWrite || observedRunner.AllowNetwork {
		t.Fatalf("sandbox policy = mode %q network %v", observedRunner.Mode, observedRunner.AllowNetwork)
	}
	if observedRunner.MaxOutputBytes != MaxOutputLength {
		t.Fatalf("sandbox output bound = %d, want %d", observedRunner.MaxOutputBytes, MaxOutputLength)
	}
	if observedRunner.WorkingDirectory != "/workspace" {
		t.Fatalf("sandbox workspace = %q", observedRunner.WorkingDirectory)
	}
	if want := []string{"/bin/sh", "-c", "echo safe"}; !reflect.DeepEqual(observedArgv, want) {
		t.Fatalf("sandbox argv = %#v, want %#v", observedArgv, want)
	}
	if observedEnvironment["PATH"] != "/safe/bin" || observedEnvironment["GIT_EDITOR"] != "true" {
		t.Fatalf("allowlisted environment was not passed: %#v", observedEnvironment)
	}
	if observedEnvironment["GIT_CONFIG_GLOBAL"] != os.DevNull {
		t.Fatalf("git global config was not isolated: %#v", observedEnvironment)
	}
	if _, ok := observedEnvironment["OPENAI_API_KEY"]; ok {
		t.Fatal("secret caller environment leaked into sandbox")
	}
}

func TestBashSandboxPropagatesParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(bashToolTestContext())
	tool := newSandboxedBashTestTool(func(runCtx context.Context, _ sandbox.PlatformRunner, _ []string, _ map[string]string) ([]byte, error) {
		cancel()
		<-runCtx.Done()
		return nil, runCtx.Err()
	})

	response, err := tool.Run(ctx, ToolCall{Input: `{"command":"echo safe"}`})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected parent cancellation, got %v", err)
	}
	if !response.IsError {
		t.Fatalf("canceled command returned success: %+v", response)
	}
}

func TestBashSandboxTimeoutIsInterruptedResult(t *testing.T) {
	tool := newSandboxedBashTestTool(func(runCtx context.Context, _ sandbox.PlatformRunner, _ []string, _ map[string]string) ([]byte, error) {
		<-runCtx.Done()
		return nil, runCtx.Err()
	})

	started := time.Now()
	response, err := tool.Run(bashToolTestContext(), ToolCall{Input: `{"command":"echo safe","timeout":1}`})
	if err != nil {
		t.Fatalf("tool timeout should be a model-visible result: %v", err)
	}
	if !response.IsError || !strings.Contains(response.Content, "aborted before completion") {
		t.Fatalf("timeout response = %+v", response)
	}
	if time.Since(started) > time.Second {
		t.Fatal("timeout did not propagate promptly to the sandbox runner")
	}
}

func TestBashSandboxOutputLimitIsFailedBoundedResult(t *testing.T) {
	tool := newSandboxedBashTestTool(func(context.Context, sandbox.PlatformRunner, []string, map[string]string) ([]byte, error) {
		return []byte(strings.Repeat("x", MaxOutputLength)), process.ErrOutputLimitExceeded
	})

	response, err := tool.Run(bashToolTestContext(), ToolCall{Input: `{"command":"echo safe"}`})
	if err != nil {
		t.Fatalf("bounded output should be a model-visible result: %v", err)
	}
	if !response.IsError || !strings.Contains(response.Content, "output exceeded") {
		t.Fatalf("bounded output response = %+v", response)
	}
	if len(response.Content) > MaxOutputLength+100 {
		t.Fatalf("bounded output response is unexpectedly large: %d", len(response.Content))
	}
}

func TestBashToolSchemaDescribesSandboxContract(t *testing.T) {
	info := NewBashTool(nil).Info()
	if info.ExecutionMode != "sandboxed-one-shot" || info.Risk != "high" {
		t.Fatalf("bash tool contract = %+v", info)
	}
	if !strings.Contains(info.Description, "do not persist") || !strings.Contains(info.Description, "fails closed") {
		t.Fatal("bash description does not disclose isolation and state semantics")
	}
	timeout := info.Parameters["timeout"].(map[string]any)
	if timeout["type"] != "integer" || timeout["maximum"] != MaxTimeout {
		t.Fatalf("timeout schema = %#v", timeout)
	}
}
