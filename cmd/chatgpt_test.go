package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/chatgpt"
)

func useChatGPTStore(t *testing.T) chatgpt.Store {
	t.Helper()
	store := chatgpt.Store{Dir: t.TempDir()}
	previous := newChatGPTClient
	newChatGPTClient = func() (*chatgpt.Client, error) { return chatgpt.NewClient(store), nil }
	t.Cleanup(func() { newChatGPTClient = previous })
	return store
}

func runChatGPTStatus(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	chatgptStatusCmd.SetOut(&out)
	t.Cleanup(func() { chatgptStatusCmd.SetOut(nil) })
	if err := chatgptStatusCmd.RunE(chatgptStatusCmd, nil); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestChatGPTStatusShowsPlanUsageAndManageLink(t *testing.T) {
	store := useChatGPTStore(t)
	if got := runChatGPTStatus(t); !strings.Contains(got, "Not signed in") {
		t.Fatalf("status = %q", got)
	}
	record := chatgpt.Record{Email: "surya@example.com", ClientID: "oaiapp_x", AccessToken: "a", ExpiresIn: 3600, SavedAt: time.Now(),
		Scopes: []string{"openid", "email", "offline_access", "resource.invoke", chatgpt.ScopePlanUsage}}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	got := runChatGPTStatus(t)
	if !strings.Contains(got, "Using ChatGPT plan") || !strings.Contains(got, chatgpt.ManageUsageURL) || !strings.Contains(got, "surya@example.com") {
		t.Fatalf("status = %q", got)
	}
	record.Scopes = []string{"openid", "email"}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	if got := runChatGPTStatus(t); strings.Contains(got, "Using ChatGPT plan") || !strings.Contains(got, "not granted") {
		t.Fatalf("status without the plan-usage scope = %q", got)
	}
}
