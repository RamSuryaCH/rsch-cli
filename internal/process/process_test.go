package process

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("RSCH_PROCESS_HELPER") == "1" {
		value := os.Getenv("RSCH_PROCESS_VALUE")
		if repetitions, err := strconv.Atoi(os.Getenv("RSCH_PROCESS_REPETITIONS")); err == nil && repetitions > 0 {
			value = strings.Repeat(value, repetitions)
		}
		_, _ = os.Stdout.WriteString(value)
		os.Exit(0)
	}
}

func TestProcessSleepHelper(t *testing.T) {
	if os.Getenv("RSCH_PROCESS_SLEEP") == "1" {
		if os.Getenv("RSCH_PROCESS_OUTPUT_BEFORE_SLEEP") == "1" {
			_, _ = os.Stdout.WriteString(strings.Repeat("x", 2048))
		}
		time.Sleep(5 * time.Second)
	}
}

func TestRunOutputLimitTerminatesIdleProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	result, err := RunOutput(ctx, Spec{
		Argv: []string{os.Args[0], "-test.run=^TestProcessSleepHelper$"},
		Environment: map[string]string{
			"RSCH_PROCESS_SLEEP":               "1",
			"RSCH_PROCESS_OUTPUT_BEFORE_SLEEP": "1",
		},
		ClearEnvironment: true,
		MaxOutputBytes:   1024,
	})
	if !errors.Is(err, ErrOutputLimitExceeded) || !result.OutputTruncated || len(result.Output) != 1024 {
		t.Fatalf("unexpected output-limit result: result=%+v err=%v", result, err)
	}
	if ctx.Err() != nil || time.Since(started) >= time.Second {
		t.Fatalf("output limit did not terminate the idle process promptly: elapsed=%s context=%v", time.Since(started), ctx.Err())
	}
}

func TestProcessPipeHelper(t *testing.T) {
	if os.Getenv("RSCH_PROCESS_PIPE") == "1" {
		_, _ = io.Copy(os.Stdout, os.Stdin)
	}
}

func TestRunOutputUsesExplicitEnvironment(t *testing.T) {
	result, err := RunOutput(context.Background(), Spec{
		Argv: []string{os.Args[0], "-test.run=TestProcessHelper"},
		Environment: map[string]string{
			"RSCH_PROCESS_HELPER": "1",
			"RSCH_PROCESS_VALUE":  "observed",
		},
		ClearEnvironment: true,
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(string(result.Output), "observed") || result.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunOutputCancellationTerminatesProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := RunOutput(ctx, Spec{
		Argv:             []string{os.Args[0], "-test.run=TestProcessSleepHelper"},
		Environment:      map[string]string{"RSCH_PROCESS_SLEEP": "1"},
		ClearEnvironment: true,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("deadline error lost the process exit status: %v", err)
	}
	if time.Since(started) > time.Second || result.ExitCode == 0 {
		t.Fatalf("process was not terminated promptly: result=%+v err=%v", result, err)
	}
}

func TestStartPipesCancellationPreservesContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipes, err := StartPipes(ctx, Spec{
		Argv:             []string{os.Args[0], "-test.run=^TestProcessSleepHelper$"},
		Environment:      map[string]string{"RSCH_PROCESS_SLEEP": "1"},
		ClearEnvironment: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	result, err := pipes.Handle.Wait()
	if !errors.Is(err, context.Canceled) || result.ExitCode == 0 {
		t.Fatalf("cancellation lost context error: result=%+v err=%v", result, err)
	}
}

func TestRunOutputCapsCapturedBytes(t *testing.T) {
	const limit = 1024
	direct := &lockedBuffer{maxBytes: limit}
	_, directErr := direct.Write([]byte(strings.Repeat("x", limit+1)))
	if !errors.Is(directErr, ErrOutputLimitExceeded) || direct.Len() != limit || !direct.Truncated() {
		t.Fatalf("direct bounded buffer failed: len=%d truncated=%t err=%v", direct.Len(), direct.Truncated(), directErr)
	}
	result, err := RunOutput(context.Background(), Spec{
		Argv: []string{os.Args[0], "-test.run=TestProcessHelper"},
		Environment: map[string]string{
			"RSCH_PROCESS_HELPER":      "1",
			"RSCH_PROCESS_VALUE":       "0123456789abcdef",
			"RSCH_PROCESS_REPETITIONS": "65536",
		},
		ClearEnvironment: true,
		MaxOutputBytes:   limit,
	})
	if !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("error = %v, want %v (bytes=%d truncated=%t)", err, ErrOutputLimitExceeded, len(result.Output), result.OutputTruncated)
	}
	if !result.OutputTruncated || len(result.Output) != limit {
		t.Fatalf("unexpected bounded result: bytes=%d truncated=%t", len(result.Output), result.OutputTruncated)
	}
}

func TestRunOutputDoesNotMarkExactLimitTruncated(t *testing.T) {
	const value = "exactly-sixteen!"
	result, err := RunOutput(context.Background(), Spec{
		Argv:             []string{os.Args[0], "-test.run=TestProcessHelper"},
		Environment:      map[string]string{"RSCH_PROCESS_HELPER": "1", "RSCH_PROCESS_VALUE": value},
		ClearEnvironment: true,
		MaxOutputBytes:   int64(len(value)),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.OutputTruncated || string(result.Output) != value {
		t.Fatalf("unexpected exact-limit result: output=%q truncated=%t", result.Output, result.OutputTruncated)
	}
}

func BenchmarkLockedBufferBoundedOverflow(b *testing.B) {
	payload := []byte(strings.Repeat("x", 32*1024))
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for i := 0; i < b.N; i++ {
		buffer := &lockedBuffer{maxBytes: 4096}
		_, _ = buffer.Write(payload)
	}
}

func TestStartPipesOwnsProtocolLifecycle(t *testing.T) {
	pipes, err := StartPipes(context.Background(), Spec{
		Argv:             []string{os.Args[0], "-test.run=TestProcessPipeHelper"},
		Environment:      map[string]string{"RSCH_PROCESS_PIPE": "1"},
		ClearEnvironment: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipes.Stdin.Write([]byte("pipe-message")); err != nil {
		t.Fatal(err)
	}
	if err := pipes.Stdin.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(pipes.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	_, waitErr := pipes.Handle.Wait()
	if waitErr != nil || !strings.HasPrefix(string(output), "pipe-message") {
		t.Fatalf("unexpected pipe result output=%q err=%v", output, waitErr)
	}
	_ = pipes.Stderr.Close()
}
