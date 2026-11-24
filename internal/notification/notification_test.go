package notification

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type recordingNotificationSandbox struct {
	called      bool
	argv        []string
	environment map[string]string
}

func (r *recordingNotificationSandbox) Run(ctx context.Context, argv []string) ([]byte, error) {
	return r.RunWithEnvironment(ctx, argv, nil)
}

func (r *recordingNotificationSandbox) RunWithEnvironment(_ context.Context, argv []string, environment map[string]string) ([]byte, error) {
	r.called = true
	r.argv = append([]string(nil), argv...)
	r.environment = environment
	return nil, nil
}

func TestConfiguredProviderDeliversRedactedWebhook(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewConfigured(t.TempDir(), Config{WebhookURL: server.URL, AllowedHosts: []string{parsed.Hostname()}, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Notify(context.Background(), Event{Kind: "test", Metadata: map[string]string{"token": "secret", "safe": "value"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "secret") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("webhook body was not redacted: %s", body)
	}
}

func TestConfiguredWebhookFailsClosedWithoutExplicitNetworkPolicy(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if _, err := NewConfigured(t.TempDir(), Config{WebhookURL: server.URL}); err == nil {
		t.Fatal("expected webhook policy denial")
	}
	if called {
		t.Fatal("denied webhook was contacted")
	}
}

func TestConfiguredCommandRequiresExactPolicyAndSandbox(t *testing.T) {
	runner := &recordingNotificationSandbox{}
	if _, err := NewConfigured(t.TempDir(), Config{Command: "notify", AllowedCommands: []string{"other"}, Sandbox: runner}); err == nil {
		t.Fatal("expected command allowlist denial")
	}
	if runner.called {
		t.Fatal("denied notification command ran")
	}

	provider, err := NewConfigured(t.TempDir(), Config{Command: "notify", Args: []string{"--event"}, AllowedCommands: []string{"notify"}, Sandbox: runner})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Notify(context.Background(), Event{Kind: "test", Metadata: map[string]string{"token": "secret", "safe": "value"}}); err != nil {
		t.Fatal(err)
	}
	if !runner.called || len(runner.argv) != 2 || runner.argv[0] != "notify" {
		t.Fatalf("sandbox invocation = %#v", runner.argv)
	}
	payload := runner.environment["RSCH_NOTIFICATION_JSON"]
	if strings.Contains(payload, "secret") || !strings.Contains(payload, "[REDACTED]") {
		t.Fatalf("sandbox environment payload was not redacted: %s", payload)
	}
}

func TestFileProviderRedactsAndListsNotifications(t *testing.T) {
	root := t.TempDir()
	provider, err := NewFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Notify(context.Background(), Event{Kind: "job.failed", Metadata: map[string]string{"apiToken": "secret", "reason": "test"}}); err != nil {
		t.Fatal(err)
	}
	items, err := provider.List(0)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].Metadata["apiToken"] != "[REDACTED]" {
		t.Fatal("notification secret was not redacted")
	}
	if runtime.GOOS != "windows" {
		directoryInfo, err := os.Stat(filepath.Join(root, ".rsch", "notifications"))
		if err != nil {
			t.Fatal(err)
		}
		fileInfo, err := os.Stat(filepath.Join(root, ".rsch", "notifications", "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("notification permissions = directory %o file %o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
		}
	}
}

func TestFileProviderRejectsSymlinkedControlPlane(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link behavior is platform-specific")
	}
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".rsch")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile(root); err == nil {
		t.Fatal("expected symlinked .rsch directory rejection")
	}
}

func TestFileProviderListsLargeNotifications(t *testing.T) {
	provider, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	message := strings.Repeat("large notification message ", 8000)
	for index, event := range []Event{
		{Kind: "large", Message: message, At: time.Unix(1, 0).UTC()},
		{Kind: "later", Message: "later event", At: time.Unix(2, 0).UTC()},
	} {
		if err := provider.Notify(context.Background(), event); err != nil {
			t.Fatalf("notify %d: %v", index, err)
		}
	}
	items, err := provider.List(0)
	if err != nil || len(items) != 2 {
		t.Fatalf("list large event: count=%d err=%v", len(items), err)
	}
	if items[0].Message != message {
		t.Fatal("large notification message was changed")
	}
	items, err = provider.List(1)
	if err != nil || len(items) != 1 || items[0].Kind != "later" {
		t.Fatalf("limited history after large event: items=%v err=%v", items, err)
	}
}

func TestFileProviderRejectsSymlinkReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link behavior is platform-specific")
	}
	for _, component := range []string{".rsch", ".rsch/notifications", ".rsch/notifications/events.jsonl"} {
		t.Run(component, func(t *testing.T) {
			root := t.TempDir()
			provider, err := NewFile(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.Notify(context.Background(), Event{Kind: "original"}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, filepath.FromSlash(component))
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			target := outside
			externalLog := filepath.Join(outside, "events.jsonl")
			if component == ".rsch" {
				externalLog = filepath.Join(outside, "notifications", "events.jsonl")
			} else if component == ".rsch/notifications/events.jsonl" {
				target = externalLog
			}
			if err := os.MkdirAll(filepath.Dir(externalLog), 0o700); err != nil {
				t.Fatal(err)
			}
			original := []byte("{\"kind\":\"outside\",\"message\":\"private outside data\"}\n")
			if err := os.WriteFile(externalLog, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.List(0); err == nil {
				t.Error("history followed a replaced symbolic link")
			}
			if err := provider.Notify(context.Background(), Event{Kind: "new"}); err == nil {
				t.Error("notification write followed a replaced symbolic link")
			}
			data, err := os.ReadFile(externalLog)
			if err != nil || string(data) != string(original) {
				t.Errorf("outside notification log was modified: data=%q err=%v", data, err)
			}
		})
	}
}
