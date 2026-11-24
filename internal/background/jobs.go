// Package background persists detached RSCH job state and output logs.
package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
)

type Status string

const (
	Running   Status = "running"
	Completed Status = "completed"
	Failed    Status = "failed"
	Stopped   Status = "stopped"
)

type Job struct {
	ID              string        `json:"id"`
	Prompt          string        `json:"prompt"`
	PID             int           `json:"pid"`
	ProcessIdentity string        `json:"processIdentity,omitempty"`
	Status          Status        `json:"status"`
	LogPath         string        `json:"logPath"`
	Error           string        `json:"error,omitempty"`
	StartedAt       time.Time     `json:"startedAt"`
	FinishedAt      time.Time     `json:"finishedAt,omitempty"`
	Timeout         time.Duration `json:"timeout,omitempty"`
}
type Manager struct {
	Root          string
	Notifier      notification.Provider
	MaxConcurrent int
	mu            sync.Mutex
}

var (
	ErrResourceLimit          = errors.New("background job resource limit reached")
	ErrProcessIdentityMissing = errors.New("background job process identity is missing")
	ErrProcessIdentityChanged = errors.New("background job process identity changed")
)

func NewManager(workspace string) (*Manager, error) {
	return NewManagerWithNotifications(workspace, notification.Config{})
}

func NewManagerWithNotifications(workspace string, config notification.Config) (*Manager, error) {
	return NewManagerWithNotificationsAndLimit(workspace, config, 2)
}

func NewManagerWithNotificationsAndLimit(workspace string, config notification.Config, maxConcurrent int) (*Manager, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("background workspace is required")
	}
	if maxConcurrent <= 0 {
		return nil, errors.New("max concurrent background jobs must be greater than zero")
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(workspace, ".rsch", "jobs")
	if err := validateStateDirectories(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	provider, err := notification.NewConfigured(workspace, config)
	if err != nil {
		return nil, err
	}
	return &Manager{Root: root, Notifier: provider, MaxConcurrent: maxConcurrent}, nil
}

func validateStateDirectories(root string) error {
	for _, path := range []string{filepath.Dir(root), root} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("background state directories must be directories, not symbolic links")
		}
	}
	return nil
}

func (m *Manager) openRoot() (*os.Root, error) {
	if err := validateStateDirectories(m.Root); err != nil {
		return nil, err
	}
	return os.OpenRoot(m.Root)
}

func (m *Manager) SetNotifier(provider notification.Provider) {
	if m != nil {
		m.Notifier = provider
	}
}
func (m *Manager) notify(event notification.Event) {
	if m != nil && m.Notifier != nil {
		_ = m.Notifier.Notify(context.Background(), event)
	}
}
func (m *Manager) Start(ctx context.Context, executable, prompt string) (Job, error) {
	return m.StartWithTimeout(ctx, executable, prompt, 0)
}

func (m *Manager) StartWithTimeout(ctx context.Context, executable, prompt string, timeout time.Duration) (Job, error) {
	if m == nil {
		return Job{}, errors.New("background manager is required")
	}
	if executable == "" || strings.TrimSpace(prompt) == "" {
		return Job{}, errors.New("executable and prompt are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	releaseAdmission, err := m.acquireAdmission(ctx)
	if err != nil {
		return Job{}, err
	}
	defer releaseAdmission()
	jobs, listErr := m.listLocked()
	if listErr != nil {
		return Job{}, listErr
	}
	jobs, listErr = m.reconcileLocked(jobs)
	if listErr != nil {
		return Job{}, listErr
	}
	running := 0
	for _, existing := range jobs {
		if existing.Status == Running && process.Alive(existing.PID) {
			running++
		}
	}
	if running >= m.MaxConcurrent {
		return Job{}, fmt.Errorf("%w: running=%d limit=%d", ErrResourceLimit, running, m.MaxConcurrent)
	}
	job := Job{ID: uuid.NewString(), Prompt: security.RedactText(prompt), Status: Running, StartedAt: time.Now().UTC(), LogPath: filepath.Join(m.Root, uuid.NewString()+".log"), Timeout: timeout}
	root, err := m.openRoot()
	if err != nil {
		return Job{}, err
	}
	defer root.Close()
	log, err := root.OpenFile(filepath.Base(job.LogPath), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Job{}, err
	}
	var processContext context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		processContext, cancel = context.WithTimeout(ctx, timeout)
	} else {
		processContext, cancel = context.WithCancel(ctx)
	}
	handle, err := process.Start(processContext, process.Spec{
		Argv:      []string{executable, "--prompt", prompt, "--quiet", "--job-id", job.ID},
		Directory: filepath.Dir(filepath.Dir(m.Root)),
		Stdout:    log,
		Stderr:    log,
		Detached:  true,
	})
	if err != nil {
		cancel()
		log.Close()
		return Job{}, err
	}
	job.PID = handle.PID()
	job.ProcessIdentity, err = backgroundProcessIdentity(job.PID)
	if err != nil {
		cancel()
		_ = handle.Kill()
		_, _ = handle.Wait()
		_ = log.Close()
		return Job{}, fmt.Errorf("capture background process identity: %w", err)
	}
	if err := m.save(job); err != nil {
		cancel()
		_ = handle.Kill()
		_, _ = handle.Wait()
		_ = log.Close()
		return Job{}, err
	}
	releaseAdmission()
	_ = log.Close()
	go m.watch(job.ID, handle, processContext, cancel)
	m.notify(notification.Event{Kind: "job.started", Resource: job.ID, Message: "background job started", Metadata: map[string]string{"status": string(job.Status)}})
	return job, nil
}

func (m *Manager) watch(id string, handle *process.Handle, processContext context.Context, cancel context.CancelFunc) {
	defer cancel()
	_, waitErr := handle.Wait()
	if processErr := processContext.Err(); processErr != nil {
		waitErr = processErr
	}
	status := Completed
	if waitErr != nil {
		status = Failed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.load(id)
	if err != nil || job.Status != Running {
		return
	}
	_ = m.finishLocked(&job, status, waitErr)
}

func (m *Manager) Mark(id string, status Status, runErr error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.load(id)
	if err != nil {
		return err
	}
	if status != Running && status != Completed && status != Failed && status != Stopped {
		return fmt.Errorf("invalid job status %q", status)
	}
	if job.Status != Running && job.Status != status {
		return fmt.Errorf("invalid job transition %s -> %s", job.Status, status)
	}
	return m.finishLocked(&job, status, runErr)
}
func (m *Manager) List() ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs, err := m.listLocked()
	if err != nil {
		return nil, err
	}
	return m.reconcileLocked(jobs)
}
func (m *Manager) listLocked() ([]Job, error) {
	root, err := m.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		job, err := m.load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.Before(jobs[j].StartedAt) })
	return jobs, nil
}
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.load(id)
	if err != nil {
		return err
	}
	if job.Status != Running {
		return fmt.Errorf("job %q is not running", id)
	}
	if err := validateProcessIdentity(job); err != nil {
		_ = m.finishLocked(&job, Failed, err)
		return err
	}
	if err := process.Interrupt(job.PID); err != nil && process.Alive(job.PID) {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for processIdentityMatches(job) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if processIdentityMatches(job) {
		if err := validateProcessIdentity(job); err != nil {
			_ = m.finishLocked(&job, Failed, err)
			return err
		}
		if err := process.Terminate(job.PID); err != nil && process.Alive(job.PID) {
			return err
		}
		deadline = time.Now().Add(time.Second)
		for processIdentityMatches(job) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if processIdentityMatches(job) {
			return fmt.Errorf("background process %d did not terminate", job.PID)
		}
	}
	return m.finishLocked(&job, Stopped, nil)
}
func (m *Manager) Recover(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.load(id)
	if err != nil {
		return Job{}, err
	}
	if job.Status == Running {
		if identityErr := validateProcessIdentity(job); identityErr != nil {
			if finishErr := m.finishLocked(&job, Failed, identityErr); finishErr != nil {
				return Job{}, finishErr
			}
		}
	}
	return job, nil
}

func (m *Manager) reconcileLocked(jobs []Job) ([]Job, error) {
	for index := range jobs {
		if jobs[index].Status != Running {
			continue
		}
		if err := validateProcessIdentity(jobs[index]); err != nil {
			if finishErr := m.finishLocked(&jobs[index], Failed, err); finishErr != nil {
				return nil, finishErr
			}
		}
	}
	return jobs, nil
}

func validateProcessIdentity(job Job) error {
	if !process.Alive(job.PID) {
		return fmt.Errorf("background process %d is no longer available", job.PID)
	}
	if strings.TrimSpace(job.ProcessIdentity) == "" {
		return fmt.Errorf("%w for pid %d; refusing to signal an unverified process", ErrProcessIdentityMissing, job.PID)
	}
	actual, err := backgroundProcessIdentity(job.PID)
	if err != nil {
		return fmt.Errorf("inspect background process %d: %w", job.PID, err)
	}
	if actual != job.ProcessIdentity {
		return fmt.Errorf("%w for pid %d (expected %q, found %q); refusing to signal a reused process id", ErrProcessIdentityChanged, job.PID, job.ProcessIdentity, actual)
	}
	return nil
}

func processIdentityMatches(job Job) bool {
	if strings.TrimSpace(job.ProcessIdentity) == "" || !process.Alive(job.PID) || processIsZombie(job.PID) {
		return false
	}
	actual, err := backgroundProcessIdentity(job.PID)
	return err == nil && actual == job.ProcessIdentity
}

func (m *Manager) finishLocked(job *Job, status Status, runErr error) error {
	job.Status = status
	if status == Running {
		job.FinishedAt = time.Time{}
	} else {
		job.FinishedAt = time.Now().UTC()
		if err := m.redactLog(job.LogPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if runErr != nil {
		job.Error = security.RedactText(runErr.Error())
	}
	if err := m.save(*job); err != nil {
		return err
	}
	m.notify(notification.Event{Kind: "job." + string(status), Resource: job.ID, Message: "background job state changed", Metadata: map[string]string{"status": string(status)}})
	return nil
}

// ReadLog returns a bounded tail of a persisted job log for safe attachment.
// It never re-executes the job or resumes tools.
func (m *Manager) ReadLog(id string, maxBytes int) (Job, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.load(id)
	if err != nil {
		return Job{}, "", err
	}
	root, err := m.openRoot()
	if err != nil {
		return Job{}, "", err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(job.LogPath))
	if err != nil {
		return Job{}, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Job{}, "", err
	}
	if !info.Mode().IsRegular() {
		return Job{}, "", errors.New("background log must be a regular file")
	}
	if maxBytes <= 0 || maxBytes > 1<<20 {
		maxBytes = 64 << 10
	}
	truncated := info.Size() > int64(maxBytes)
	if truncated {
		if _, err := file.Seek(info.Size()-int64(maxBytes), io.SeekStart); err != nil {
			return Job{}, "", err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)))
	if err != nil {
		return Job{}, "", err
	}
	if truncated {
		data = append([]byte("[log tail truncated]\n"), data...)
	}
	return job, security.RedactText(string(data)), nil
}

func (m *Manager) redactLog(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	root, err := m.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	data, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	redacted := []byte(security.RedactText(string(data)))
	temporaryPath := ".redact-" + uuid.NewString() + ".tmp"
	temporary, err := root.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(redacted); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return root.Rename(temporaryPath, name)
}
func (m *Manager) load(id string) (Job, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return Job{}, errors.New("invalid job id")
	}
	root, err := m.openRoot()
	if err != nil {
		return Job{}, err
	}
	defer root.Close()
	info, err := root.Lstat(id + ".json")
	if err != nil {
		return Job{}, err
	}
	if !info.Mode().IsRegular() {
		return Job{}, errors.New("background job record must be a regular file")
	}
	data, err := root.ReadFile(id + ".json")
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return Job{}, err
	}
	if err := m.validateJob(root, job, id); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (m *Manager) validateJob(root *os.Root, job Job, id string) error {
	if job.ID == "" || strings.ContainsAny(job.ID, `/\`) || job.ID != id {
		return errors.New("background job id does not match its record")
	}
	if job.LogPath == "" {
		return nil
	}
	path, err := filepath.Abs(job.LogPath)
	if err != nil {
		return err
	}
	if filepath.Dir(path) != filepath.Clean(m.Root) || filepath.Ext(path) != ".log" {
		return errors.New("background job log must be inside the job directory")
	}
	info, err := root.Lstat(filepath.Base(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("background job log must be a regular file")
	}
	return nil
}

func (m *Manager) save(job Job) error {
	root, err := m.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := m.validateJob(root, job, job.ID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	temporaryPath := "." + job.ID + "-" + uuid.NewString() + ".tmp"
	temporary, err := root.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporaryPath, job.ID+".json"); err != nil {
		return err
	}
	return nil
}
