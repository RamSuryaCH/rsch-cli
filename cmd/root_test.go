package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/spf13/cobra"
)

func TestReadPromptInputReadsRedirectedStdin(t *testing.T) {
	prompt, redirected, err := readPromptInput(strings.NewReader("  summarize this repository\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !redirected {
		t.Fatal("expected a non-file reader to be treated as redirected stdin")
	}
	if prompt != "summarize this repository" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
}

func TestReadPromptInputPreservesInteractiveTerminal(t *testing.T) {
	prompt, redirected, err := readPromptInput(terminalReader{})
	if err != nil {
		t.Fatal(err)
	}
	if redirected {
		t.Fatal("expected a character device to preserve interactive mode")
	}
	if prompt != "" {
		t.Fatalf("unexpected terminal prompt %q", prompt)
	}
}

func TestReadPromptInputTreatsDevNullAsRedirected(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	prompt, redirected, err := readPromptInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if !redirected {
		t.Fatal("expected the null device to be treated as redirected stdin")
	}
	if prompt != "" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
}

func TestReadPromptInputRejectsOversizedInput(t *testing.T) {
	_, redirected, err := readPromptInput(strings.NewReader(strings.Repeat("x", maxStdinPromptBytes+1)))
	if !redirected {
		t.Fatal("expected oversized input to remain a non-interactive invocation")
	}
	var usageErr *cliUsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("expected a usage error, got %v", err)
	}
}

func TestReadPromptInputRejectsInvalidUTF8AndNUL(t *testing.T) {
	for name, input := range map[string][]byte{
		"invalid-utf8": {0xff},
		"nul":          []byte("hello\x00world"),
	} {
		t.Run(name, func(t *testing.T) {
			_, redirected, err := readPromptInput(bytes.NewReader(input))
			if !redirected {
				t.Fatal("invalid piped input was treated as a terminal")
			}
			var usageErr *cliUsageError
			if !errors.As(err, &usageErr) {
				t.Fatalf("error = %v, want usage error", err)
			}
		})
	}
}

func TestExecuteCommandWritesStructuredUsageError(t *testing.T) {
	command := &cobra.Command{Use: "rsch", RunE: func(*cobra.Command, []string) error { return nil }}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := executeCommand(context.Background(), command, []string{"--definitely-unknown"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("expected usage exit code 2, got %d", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected clean stdout, got %q", stdout.String())
	}

	envelope := decodeCLIError(t, stderr.Bytes())
	if !envelope.Error || envelope.Code != "UNKNOWN_FLAG" {
		t.Fatalf("unexpected error envelope: %+v", envelope)
	}
	if strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr should contain one machine-readable envelope, got %q", stderr.String())
	}
}

func TestExecuteCommandMapsCancellationTo130(t *testing.T) {
	command := &cobra.Command{Use: "rsch", RunE: func(*cobra.Command, []string) error {
		return context.Canceled
	}}
	var stderr bytes.Buffer

	exitCode := executeCommand(context.Background(), command, nil, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if exitCode != 130 {
		t.Fatalf("expected interrupt exit code 130, got %d", exitCode)
	}
	if envelope := decodeCLIError(t, stderr.Bytes()); envelope.Code != "INTERRUPTED" {
		t.Fatalf("unexpected error envelope: %+v", envelope)
	}
}

func TestExecuteCommandConvertsPanicsToFailure(t *testing.T) {
	command := &cobra.Command{Use: "rsch", Run: func(*cobra.Command, []string) {
		panic("boom")
	}}
	var stderr bytes.Buffer

	exitCode := executeCommand(context.Background(), command, nil, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if exitCode != 1 {
		t.Fatalf("expected panic exit code 1, got %d", exitCode)
	}
	if envelope := decodeCLIError(t, stderr.Bytes()); envelope.Code != "INTERNAL_PANIC" {
		t.Fatalf("unexpected error envelope: %+v", envelope)
	}
}

func TestClassifyCLIErrorUsesStableOperationalCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		exit int
		code string
	}{
		{name: "timeout", err: context.DeadlineExceeded, exit: 124, code: "TIMED_OUT"},
		{name: "sigterm", err: &terminationSignalError{signal: syscall.SIGTERM}, exit: 143, code: "TERMINATED"},
		{name: "agent cancellation", err: agent.ErrRequestCancelled, exit: 130, code: "INTERRUPTED"},
		{name: "permission", err: permission.ErrorPermissionDenied, exit: 11, code: "PERMISSION_DENIED"},
		{name: "policy", err: execution.ErrDenied, exit: 11, code: "PERMISSION_DENIED"},
		{name: "schema", err: format.ErrOutputSchemaMismatch, exit: 1, code: "OUTPUT_SCHEMA_MISMATCH"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exit, envelope := classifyCLIError(test.err)
			if exit != test.exit || envelope.Code != test.code {
				t.Fatalf("got exit=%d envelope=%+v, want exit=%d code=%s", exit, envelope, test.exit, test.code)
			}
		})
	}
}

func TestExecuteCommandPreservesCancellationCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	command := &cobra.Command{Use: "rsch", RunE: func(cmd *cobra.Command, _ []string) error {
		cancel(&terminationSignalError{signal: syscall.SIGTERM})
		<-cmd.Context().Done()
		return cmd.Context().Err()
	}}
	var stderr bytes.Buffer
	exit := executeCommand(ctx, command, nil, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if exit != 143 {
		t.Fatalf("exit = %d, want 143; stderr=%s", exit, stderr.String())
	}
	if envelope := decodeCLIError(t, stderr.Bytes()); envelope.Code != "TERMINATED" {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
}

func decodeCLIError(t *testing.T, data []byte) cliErrorEnvelope {
	t.Helper()
	var envelope cliErrorEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode structured error %q: %v", data, err)
	}
	return envelope
}

type terminalReader struct{}

func (terminalReader) Read([]byte) (int, error) {
	return 0, errors.New("terminal input must not be read during mode detection")
}

func (terminalReader) Stat() (os.FileInfo, error) { return terminalFileInfo{}, nil }

type terminalFileInfo struct{}

func (terminalFileInfo) Name() string       { return "tty" }
func (terminalFileInfo) Size() int64        { return 0 }
func (terminalFileInfo) Mode() os.FileMode  { return os.ModeCharDevice }
func (terminalFileInfo) ModTime() time.Time { return time.Time{} }
func (terminalFileInfo) IsDir() bool        { return false }
func (terminalFileInfo) Sys() any           { return nil }

var _ io.Reader = terminalReader{}
var _ os.FileInfo = terminalFileInfo{}
