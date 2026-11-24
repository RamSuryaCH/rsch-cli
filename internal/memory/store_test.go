package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreScopesSearchAndRemoval(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Add(Entry{Category: "decision", Scope: "repository", Content: "Use SQLite for durable workflow state", Tags: []string{"database"}, Source: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := store.Search("sqlite", "repository"); err != nil || len(entries) != 1 {
		t.Fatalf("unexpected search: %+v %v", entries, err)
	}
	entries, err := store.Search("sqlite", "session")
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected scoped search: %+v %v", entries, err)
	}
	if explained, err := store.Explain(entry.ID); err != nil || explained.Source != "human" {
		t.Fatalf("unexpected explanation: %+v %v", explained, err)
	}
	if err := store.Remove(entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Explain(entry.ID); err == nil {
		t.Fatal("expected removed entry to be absent")
	}
}

func TestStoreRanksMetadataAndRemovesExpiredEntries(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(-time.Minute)
	_, err = store.Add(Entry{Scope: "session", Layer: LayerSession, Kind: KindEpisodic, Content: "SQLite decision", Importance: 0.9, Confidence: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Add(Entry{Scope: "project", Layer: LayerProject, Kind: KindProcedural, Content: "SQLite decision", Importance: 0.2, Confidence: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Add(Entry{Scope: "project", Content: "expired SQLite note", ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchRanked("SQLite", "", 10)
	if err != nil || len(results) != 2 || results[0].Entry.Importance < results[1].Entry.Importance {
		t.Fatalf("unexpected ranked results: %+v %v", results, err)
	}
	removed, err := store.ForgetExpired()
	if err != nil || removed != 1 {
		t.Fatalf("expired memory was not removed: %d %v", removed, err)
	}
}

func TestStoreRecoversFromBackup(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Entry{Content: "recoverable"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".rsch", "memory", "entries.json")
	backup := path + ".bak"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.List(""); err != nil || len(entries) != 1 {
		t.Fatalf("backup was not used: %+v %v", entries, err)
	}
	corrupt, err := filepath.Glob(path + ".corrupt.*")
	if err != nil || len(corrupt) != 1 {
		t.Fatalf("corrupt primary was not quarantined: %v %v", corrupt, err)
	}
	if _, _, err := decodeStore(mustReadFile(t, path)); err != nil {
		t.Fatalf("primary was not repaired from backup: %v", err)
	}
}

func TestStoreWritesVersionedPrivateFilesAndPreservesProvenance(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, time.August, 30, 12, 30, 0, 0, time.UTC)
	want := Provenance{SourceType: "review", SourceID: "finding-1", Location: "main.go:12", Actor: "security", Method: "manual", ObservedAt: observed}
	created, err := store.Add(Entry{Content: "Use bounded reads", Provenance: want})
	if err != nil {
		t.Fatal(err)
	}
	if created.Provenance != want {
		t.Fatalf("provenance changed during add: got %+v want %+v", created.Provenance, want)
	}

	path := filepath.Join(root, ".rsch", "memory", "entries.json")
	for _, candidate := range []string{path, path + ".bak"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("%s permissions=%#o want 0600", candidate, permissions)
		}
	}

	var persisted persistedStore
	if err := json.Unmarshal(mustReadFile(t, path), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Version != storeSchemaVersion || persisted.Checksum == "" || len(persisted.Entries) != 1 {
		t.Fatalf("invalid versioned store: %+v", persisted)
	}
	if persisted.Entries[0].Provenance != want {
		t.Fatalf("persisted provenance changed: got %+v want %+v", persisted.Entries[0].Provenance, want)
	}
}

func TestStoreMigratesLegacyArrayOnNextMutation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, time.August, 30, 9, 0, 0, 0, time.UTC)
	legacy := []Entry{{ID: "legacy", Category: "fact", Scope: "repository", Content: "legacy entry", Provenance: Provenance{SourceID: "source-1", ObservedAt: observed}}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "entries.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Entry{Content: "new entry"}); err != nil {
		t.Fatal(err)
	}
	entries, legacyFormat, err := decodeStore(mustReadFile(t, path))
	if err != nil || legacyFormat || len(entries) != 2 {
		t.Fatalf("legacy migration failed: entries=%+v legacy=%t err=%v", entries, legacyFormat, err)
	}
	if entries[0].Provenance.SourceID != "source-1" || !entries[0].Provenance.ObservedAt.Equal(observed) {
		t.Fatalf("legacy provenance was not preserved: %+v", entries[0].Provenance)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("legacy file was not secured: info=%v err=%v", info, err)
	}
}

func TestStoreRejectsUnknownVersion(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Entry{Content: "valid entry"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".rsch", "memory", "entries.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"checksum":"invalid","entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(""); err == nil || !strings.Contains(err.Error(), "unsupported memory store version 99") {
		t.Fatalf("expected unsupported-version error, got %v", err)
	}
	var persisted persistedStore
	if err := json.Unmarshal(mustReadFile(t, path), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Version != 99 {
		t.Fatalf("unknown-version primary was replaced from backup: version=%d", persisted.Version)
	}
}

func TestStoreInstancesSerializeConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	first, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	const count = 40
	var wg sync.WaitGroup
	errorsFound := make(chan error, count)
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			store := first
			if index%2 == 1 {
				store = second
			}
			_, addErr := store.Add(Entry{Content: fmt.Sprintf("entry-%d", index)})
			if addErr != nil {
				errorsFound <- addErr
			}
		}(index)
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent add: %v", err)
	}
	entries, err := first.List("")
	if err != nil || len(entries) != count {
		t.Fatalf("lost concurrent writes: entries=%d err=%v", len(entries), err)
	}
}

func TestAddOpportunisticallyCleansPreviouslyExpiredEntries(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(-time.Minute)
	if _, err := store.Add(Entry{Content: "expired", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Entry{Content: "active"}); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetExpired(); err != nil || removed != 0 {
		t.Fatalf("expired entries were not cleaned during mutation: removed=%d err=%v", removed, err)
	}
	entries, err := store.List("")
	if err != nil || len(entries) != 1 || entries[0].Content != "active" {
		t.Fatalf("unexpected active entries: %+v err=%v", entries, err)
	}
}

func TestStoreRejectsSymlinkedDataDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".rsch")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := New(root); err == nil {
		t.Fatal("expected symlinked memory data directory to be rejected")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
