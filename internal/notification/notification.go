// Package notification provides extensible, redacted workflow notifications.
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/network"
	"github.com/opencode-ai/opencode/internal/sandbox"
	"github.com/opencode-ai/opencode/internal/security"
)

type Event struct {
	ID       string            `json:"id"`
	At       time.Time         `json:"at"`
	Kind     string            `json:"kind"`
	Resource string            `json:"resource,omitempty"`
	Message  string            `json:"message,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}
type Provider interface {
	Notify(context.Context, Event) error
}

type Config struct {
	WebhookURL      string
	Command         string
	Args            []string
	Timeout         time.Duration
	AllowedHosts    []string
	AllowPrivate    bool
	AllowedCommands []string
	Sandbox         sandbox.Runner
}

// NewConfigured always includes the durable local provider and conditionally
// adds explicitly configured external providers.
func NewConfigured(root string, config Config) (Provider, error) {
	file, err := NewFile(root)
	if err != nil {
		return nil, err
	}
	providers := []Provider{file}
	if strings.TrimSpace(config.WebhookURL) != "" {
		policy := network.Policy{
			AllowedHosts: append([]string(nil), config.AllowedHosts...),
			AllowPrivate: config.AllowPrivate,
			Timeout:      boundedTimeout(config.Timeout),
			MaxBody:      64 << 10,
		}
		if err := policy.ValidateURL(config.WebhookURL); err != nil {
			return nil, fmt.Errorf("notification webhook denied by network policy: %w", err)
		}
		providers = append(providers, webhookProvider{url: config.WebhookURL, policy: policy})
	}
	if strings.TrimSpace(config.Command) != "" {
		if !commandAllowed(config.Command, config.AllowedCommands) {
			return nil, fmt.Errorf("notification command %q is not explicitly allowed", config.Command)
		}
		runner := config.Sandbox
		if runner == nil {
			capability := sandbox.Probe(context.Background(), root)
			if !capability.Isolated {
				return nil, fmt.Errorf("notification command requires an OS sandbox: %s", capability.Reason)
			}
			runner = sandbox.PlatformRunner{Capability: capability, WorkingDirectory: root, Mode: sandbox.ReadOnly, AllowNetwork: false}
		}
		environmentRunner, ok := runner.(sandbox.EnvironmentRunner)
		if !ok {
			return nil, errors.New("notification command sandbox must support an isolated environment")
		}
		providers = append(providers, commandProvider{path: config.Command, args: append([]string(nil), config.Args...), timeout: boundedTimeout(config.Timeout), runner: environmentRunner})
	}
	return MultiProvider(providers), nil
}

func boundedTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 || timeout > 30*time.Second {
		return 5 * time.Second
	}
	return timeout
}

func commandAllowed(command string, allowed []string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == command {
			return true
		}
	}
	return false
}

type FileProvider struct {
	root string
	mu   sync.Mutex
}

const notificationDirectory = ".rsch/notifications"
const notificationLog = "events.jsonl"

func NewFile(root string) (*FileProvider, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("notification root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	workspace, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	if err := validateDirectories(workspace, true); err != nil {
		return nil, err
	}
	if err := workspace.MkdirAll(notificationDirectory, 0o700); err != nil {
		return nil, err
	}
	for _, path := range []string{".rsch", notificationDirectory} {
		directory, err := workspace.Open(path)
		if err != nil {
			return nil, err
		}
		err = directory.Chmod(0o700)
		_ = directory.Close()
		if err != nil {
			return nil, err
		}
	}
	return &FileProvider{root: root}, nil
}

func validateDirectories(workspace *os.Root, allowMissing bool) error {
	for _, path := range []string{".rsch", notificationDirectory} {
		info, err := workspace.Lstat(path)
		if allowMissing && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("notification state directories must be directories, not symbolic links")
		}
	}
	return nil
}

// Open beneath the configured workspace on every operation so replacement of
// either state directory cannot redirect notification reads or writes.
func (p *FileProvider) openDirectory() (*os.Root, error) {
	workspace, err := os.OpenRoot(p.root)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	if err := validateDirectories(workspace, false); err != nil {
		return nil, err
	}
	return workspace.OpenRoot(notificationDirectory)
}

func validateLog(directory *os.Root) error {
	info, err := directory.Lstat(notificationLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("notification log must be a regular file")
	}
	return nil
}

func (p *FileProvider) Notify(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	event.Message = security.RedactText(event.Message)
	event.Resource = security.RedactText(event.Resource)
	safe := map[string]string{}
	for key, value := range event.Metadata {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "private") || strings.Contains(lower, "key") {
			safe[key] = "[REDACTED]"
		} else {
			safe[key] = security.RedactText(value)
		}
	}
	event.Metadata = safe
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	directory, err := p.openDirectory()
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := validateLog(directory); err != nil {
		return err
	}
	file, err := directory.OpenFile(notificationLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func (p *FileProvider) List(limit int) ([]Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	directory, err := p.openDirectory()
	if os.IsNotExist(err) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	if err := validateLog(directory); err != nil {
		return nil, err
	}
	file, err := directory.Open(notificationLog)
	if os.IsNotExist(err) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	events := []Event{}
	decoder := json.NewDecoder(file)
	for {
		var event Event
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

type MultiProvider []Provider

func (m MultiProvider) Notify(ctx context.Context, event Event) error {
	for _, provider := range m {
		if provider != nil {
			if err := provider.Notify(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

// webhookProvider sends redacted JSON through the shared allowlisted network
// boundary. It accepts no caller-controlled headers or redirect policy.
type webhookProvider struct {
	url    string
	policy network.Policy
}

func (p webhookProvider) Notify(ctx context.Context, event Event) error {
	if strings.TrimSpace(p.url) == "" {
		return errors.New("notification webhook URL is required")
	}
	data, err := json.Marshal(redact(event))
	if err != nil {
		return err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	response, err := p.policy.Request(ctx, http.MethodPost, p.url, data, headers)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("notification webhook returned non-success status")
	}
	return nil
}

// commandProvider invokes an exactly allowlisted executable without a shell.
// The redacted event is provided through a clean environment inside an OS
// sandbox; repository configuration cannot inherit host credentials.
type commandProvider struct {
	path    string
	args    []string
	timeout time.Duration
	runner  sandbox.EnvironmentRunner
}

func (p commandProvider) Notify(ctx context.Context, event Event) error {
	if strings.TrimSpace(p.path) == "" {
		return errors.New("notification command is required")
	}
	if p.runner == nil {
		return errors.New("notification command sandbox is required")
	}
	callCtx, cancel := context.WithTimeout(ctx, boundedTimeout(p.timeout))
	defer cancel()
	data, err := json.Marshal(redact(event))
	if err != nil {
		return err
	}
	_, err = p.runner.RunWithEnvironment(callCtx, append([]string{p.path}, p.args...), map[string]string{"RSCH_NOTIFICATION_JSON": string(data)})
	if err != nil {
		return errors.New("notification command failed")
	}
	return nil
}

func redact(event Event) Event {
	copy := event
	copy.Message = security.RedactText(copy.Message)
	copy.Resource = security.RedactText(copy.Resource)
	safe := map[string]string{}
	for key, value := range event.Metadata {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "private") || strings.Contains(lower, "key") {
			safe[key] = "[REDACTED]"
		} else {
			safe[key] = security.RedactText(value)
		}
	}
	copy.Metadata = safe
	return copy
}
