package background

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/opencode-ai/opencode/internal/process"
)

func writeJobScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script background lifecycle test is Unix-only")
	}
	path := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func startSleepingProcess(t *testing.T) (*process.Handle, string) {
	t.Helper()
	script := writeJobScript(t, "while :; do sleep 1; done")
	handle, err := process.Start(context.Background(), process.Spec{Argv: []string{script}, Detached: true})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := backgroundProcessIdentity(handle.PID())
	if err != nil {
		_ = handle.Kill()
		_, _ = handle.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = handle.Kill()
		_, _ = handle.Wait()
	})
	return handle, identity
}

func waitForJobStatus(t *testing.T, manager *Manager, id string, status Status) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.load(id)
		if err == nil && job.Status == status {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := manager.load(id)
	t.Fatalf("job did not reach %s: job=%+v err=%v", status, job, err)
	return Job{}
}

func TestManagerCreatesPrivateJobDirectory(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if manager.Root == "" {
		t.Fatal("missing job root")
	}
}

func TestBackgroundJobPersistsRedactedPrompt(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := writeJobScript(t, "sleep 0.2")
	secret := "super-" + "secret-value"
	job, err := manager.Start(context.Background(), executable, `deploy token: "`+secret+`"`)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := manager.load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted.Prompt, secret) {
		t.Fatalf("prompt was not redacted: %q", persisted.Prompt)
	}
	waitForJobStatus(t, manager, job.ID, Completed)
}

func TestManagerEnforcesConcurrentJobLimit(t *testing.T) {
	manager, err := NewManagerWithNotificationsAndLimit(t.TempDir(), notification.Config{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	handle, identity := startSleepingProcess(t)
	job := Job{ID: "running", PID: handle.PID(), ProcessIdentity: identity, Status: Running, StartedAt: time.Now().UTC(), LogPath: manager.Root + "/job.log"}
	if err := manager.save(job); err != nil {
		t.Fatal(err)
	}
	_, err = manager.Start(context.Background(), "sleep", "blocked")
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("expected resource limit, got %v", err)
	}
}

func TestJobTransitionsRejectTerminalOverwrite(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job-1", Status: Running, StartedAt: time.Now().UTC(), LogPath: manager.Root + "/job.log"}
	if err := manager.save(job); err != nil {
		t.Fatal(err)
	}
	if err := manager.Mark(job.ID, Completed, nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.Mark(job.ID, Failed, nil); err == nil {
		t.Fatal("expected terminal transition rejection")
	}
}

func TestTerminalJobRedactsPersistedLog(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "redact-log", Status: Running, StartedAt: time.Now().UTC(), LogPath: manager.Root + "/redact.log"}
	secret := "super-" + "secret-value"
	if err := os.WriteFile(job.LogPath, []byte(`token: "`+secret+`"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.save(job); err != nil {
		t.Fatal(err)
	}
	if err := manager.Mark(job.ID, Completed, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(job.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("terminal log still contains secret: %s", data)
	}
}

func TestStopUsesSharedProcessLifecycleBoundary(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, identity := startSleepingProcess(t)
	job := Job{ID: "stoppable", PID: handle.PID(), ProcessIdentity: identity, Status: Running, StartedAt: time.Now().UTC(), LogPath: manager.Root + "/stop.log"}
	if err := manager.save(job); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(job.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = handle.Wait()
	if process.Alive(job.PID) {
		t.Fatalf("process %d remained alive after stop", job.PID)
	}
	stopped, err := manager.load(job.ID)
	if err != nil || stopped.Status != Stopped {
		t.Fatalf("unexpected stopped job=%+v err=%v", stopped, err)
	}
}

func TestStopRefusesReusedPID(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := startSleepingProcess(t)
	job := Job{ID: "stale", PID: handle.PID(), ProcessIdentity: "different-process-incarnation", Status: Running, StartedAt: time.Now().UTC(), LogPath: manager.Root + "/stale.log"}
	if err := manager.save(job); err != nil {
		t.Fatal(err)
	}
	err = manager.Stop(job.ID)
	if !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("error = %v, want process identity change", err)
	}
	if !process.Alive(handle.PID()) {
		t.Fatal("stale PID protection signaled an unrelated process")
	}
	persisted, loadErr := manager.load(job.ID)
	if loadErr != nil || persisted.Status != Failed {
		t.Fatalf("unexpected reconciled job=%+v err=%v", persisted, loadErr)
	}
}

func TestWatcherMarksCompletedProcess(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := writeJobScript(t, "sleep 0.1\nexit 0")
	job, err := manager.Start(context.Background(), executable, "complete")
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForJobStatus(t, manager, job.ID, Completed)
	if completed.FinishedAt.IsZero() || process.Alive(job.PID) {
		t.Fatalf("completed process was not finalized: %+v", completed)
	}
}

func TestTimeoutMarksProcessFailed(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := writeJobScript(t, "trap '' INT\nwhile :; do sleep 1; done")
	job, err := manager.StartWithTimeout(context.Background(), executable, "timeout", 75*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForJobStatus(t, manager, job.ID, Failed)
	if failed.Error == "" || process.Alive(job.PID) {
		t.Fatalf("timed-out process was not finalized: %+v", failed)
	}
}

func TestListReconcilesUnavailableProcess(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "unavailable", PID: 1 << 30, ProcessIdentity: "gone", Status: Running, StartedAt: time.Now().UTC(), LogPath: manager.Root + "/missing.log"}
	if err := manager.save(job); err != nil {
		t.Fatal(err)
	}
	jobs, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != Failed || jobs[0].FinishedAt.IsZero() {
		t.Fatalf("unavailable process was not reconciled: %+v", jobs)
	}
}

func TestConcurrentManagersUseDistinctTemporaryFiles(t *testing.T) {
	workspace := t.TempDir()
	first, err := NewManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "concurrent-save", Status: Completed, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			manager := first
			if index%2 == 1 {
				manager = second
			}
			errorsFound <- manager.save(job)
		}(index)
	}
	wait.Wait()
	close(errorsFound)
	for saveErr := range errorsFound {
		if saveErr != nil {
			t.Fatalf("concurrent save failed: %v", saveErr)
		}
	}
	if _, err := first.load(job.ID); err != nil {
		t.Fatalf("final persisted job is invalid: %v", err)
	}
}

func TestManagerRejectsSymlinkedJobDirectoryBeforeCreatingState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link behavior is platform-specific")
	}
	for _, component := range []string{".rsch", filepath.Join(".rsch", "jobs")} {
		t.Run(component, func(t *testing.T) {
			workspace := t.TempDir()
			outside := t.TempDir()
			link := filepath.Join(workspace, component)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			if _, err := NewManager(workspace); err == nil {
				t.Fatal("manager accepted a symbolic-link state directory")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("manager modified the symlink target: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestPersistedJobRejectsUntrustedPaths(t *testing.T) {
	for _, scenario := range []string{"outside-log", "symlink-log", "mismatched-id", "traversal-id", "symlink-record"} {
		t.Run(scenario, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.HasPrefix(scenario, "symlink") {
				t.Skip("symbolic-link behavior is platform-specific")
			}
			manager, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside.log")
			original := []byte("token: secret-value\n")
			if err := os.WriteFile(outside, original, 0o600); err != nil {
				t.Fatal(err)
			}
			job := Job{ID: "untrusted", Status: Running, LogPath: filepath.Join(manager.Root, "job.log")}
			if scenario != "symlink-log" {
				if err := os.WriteFile(job.LogPath, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "outside-log":
				job.LogPath = outside
			case "symlink-log":
				if err := os.Symlink(outside, job.LogPath); err != nil {
					t.Fatal(err)
				}
			case "mismatched-id":
				job.ID = "another-job"
			case "traversal-id":
				job.ID = "../another-job"
			}
			data, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			recordPath := filepath.Join(manager.Root, "untrusted.json")
			if scenario == "symlink-record" {
				externalRecord := filepath.Join(t.TempDir(), "record.json")
				if err := os.WriteFile(externalRecord, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(externalRecord, recordPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(recordPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := manager.ReadLog("untrusted", 0); err == nil {
				t.Fatal("read accepted untrusted persisted paths")
			}
			if err := manager.Mark("untrusted", Completed, nil); err == nil {
				t.Fatal("state transition accepted untrusted persisted paths")
			}
			if _, err := manager.List(); err == nil {
				t.Fatal("list accepted untrusted persisted paths")
			}
			data, err = os.ReadFile(outside)
			if err != nil || string(data) != string(original) {
				t.Fatalf("outside file changed: data=%q err=%v", data, err)
			}
		})
	}
}

func TestReadLogReturnsBoundedTail(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "large-log", Status: Running, LogPath: filepath.Join(manager.Root, "large.log")}
	file, err := os.Create(job.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(8 << 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("tail-content"), (8<<20)-12); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.save(job); err != nil {
		t.Fatal(err)
	}
	_, tail, err := manager.ReadLog(job.ID, 12)
	if err != nil || tail != "[log tail truncated]\ntail-content" {
		t.Fatalf("unexpected log tail: %q err=%v", tail, err)
	}
}

func TestBackgroundJobRunsInConfiguredWorkspace(t *testing.T) {
	workspace := t.TempDir()
	manager, err := NewManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Start(context.Background(), writeJobScript(t, "pwd\nsleep 0.1"), "inspect workspace")
	if err != nil {
		t.Fatal(err)
	}
	waitForJobStatus(t, manager, job.ID, Completed)
	_, output, err := manager.ReadLog(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSpace(output))
	if err != nil || actual != want {
		t.Fatalf("background working directory = %q, want %q (err=%v)", output, want, err)
	}
}

func TestConcurrentManagersShareAdmissionLimit(t *testing.T) {
	workspace := t.TempDir()
	executable := writeJobScript(t, "sleep 10")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const attempts = 6
	managers := make([]*Manager, attempts)
	for index := range managers {
		manager, err := NewManagerWithNotificationsAndLimit(workspace, notification.Config{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		managers[index] = manager
	}
	type outcome struct {
		job Job
		err error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, attempts)
	for _, manager := range managers {
		go func(manager *Manager) {
			<-start
			job, err := manager.Start(ctx, executable, "concurrent admission")
			outcomes <- outcome{job: job, err: err}
		}(manager)
	}
	close(start)
	started := []Job{}
	for range attempts {
		result := <-outcomes
		if result.err == nil {
			started = append(started, result.job)
		} else if !errors.Is(result.err, ErrResourceLimit) {
			t.Errorf("start failed with unexpected error: %v", result.err)
		}
	}
	cancel()
	for _, job := range started {
		waitForJobStatus(t, managers[0], job.ID, Failed)
	}
	if len(started) != 1 {
		t.Fatalf("admitted %d jobs with a shared limit of one", len(started))
	}
}

func TestAdmissionWaitHonorsCancellation(t *testing.T) {
	workspace := t.TempDir()
	first, err := NewManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	release, err := first.acquireAdmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := second.Start(ctx, "unused-executable", "waiting for admission"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission error = %v, want deadline exceeded", err)
	}
	release()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	releaseSecond, err := second.acquireAdmission(ctx)
	if err != nil {
		t.Fatalf("canceled waiter did not release its resources: %v", err)
	}
	releaseSecond()
}
