package audit

import (
	"strings"
	"testing"
)

func TestAuditRedactsSensitiveMetadata(t *testing.T) {
	log, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Entry{Action: "tool.execute", Metadata: map[string]string{"apiToken": "secret", "path": "x"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := log.List(0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected entries: %+v %v", entries, err)
	}
	if entries[0].Metadata["apiToken"] != "[REDACTED]" {
		t.Fatal("secret metadata was not redacted")
	}
}

func TestAuditListsLargeAcceptedEntries(t *testing.T) {
	log, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	detail := strings.Repeat("diagnostic detail ", 8192)
	if err := log.Append(Entry{Action: "tool.execute", Metadata: map[string]string{"detail": detail}}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Entry{Action: "tool.completed"}); err != nil {
		t.Fatal(err)
	}
	entries, err := log.List(0)
	if err != nil {
		t.Fatalf("cannot read accepted audit entry: %v", err)
	}
	if len(entries) != 2 || entries[0].Metadata["detail"] != detail || entries[1].Action != "tool.completed" {
		t.Fatal("large entry or following entry was not preserved")
	}
	latest, err := log.List(1)
	if err != nil || len(latest) != 1 || latest[0].Action != "tool.completed" {
		t.Fatalf("unexpected latest audit entry: %+v, %v", latest, err)
	}
}
