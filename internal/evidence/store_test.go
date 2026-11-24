package evidence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreRedactsAndListsEvidenceAtomically(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"token": "secret-value", "count": 2})
	if _, err := store.Record(Record{Kind: "dependencies", Source: "cli", Data: data}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != Observed {
		t.Fatalf("items=%+v", items)
	}
	if string(items[0].Data) == string(data) {
		t.Fatal("credential value was not redacted")
	}
	if _, err := os.Stat(filepath.Join(root, ".rsch", "evidence")); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAndFilterWorkflowEvidenceLinks(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Record(Record{Kind: "test", Source: "unit", Summary: "passed"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := store.CreateLink(Link{EvidenceID: record.ID, GoalID: "goal-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if link.EvidenceID != record.ID {
		t.Fatalf("link=%+v", link)
	}
	items, err := store.ListLinks(LinkQuery{TaskID: "task-1"}, 0)
	if err != nil || len(items) != 1 || items[0].GoalID != "goal-1" {
		t.Fatalf("links=%+v err=%v", items, err)
	}
	if _, err := store.CreateLink(Link{EvidenceID: record.ID}); err == nil {
		t.Fatal("expected workflow reference validation")
	}
	if _, err := store.CreateLink(Link{EvidenceID: "missing", GoalID: "goal-1"}); err == nil {
		t.Fatal("expected missing evidence validation")
	}
}

func TestStoreRejectsUnsafeKind(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(Record{Kind: "../escape", Source: "cli"}); err == nil {
		t.Fatal("expected unsafe kind to be rejected")
	}
}

func TestStoreRejectsUnsafeIDs(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "prefix/../../escape", `prefix\escape`, ".", "..", "with space"} {
		t.Run(id, func(t *testing.T) {
			if _, err := store.Record(Record{ID: id, Kind: "test", Source: "unit"}); err == nil {
				t.Fatal("expected unsafe evidence ID to be rejected")
			}
		})
	}
	items, err := store.List(0)
	if err != nil || len(items) != 0 {
		t.Fatalf("invalid IDs created records: items=%+v err=%v", items, err)
	}
}

func TestStorePreservesExactJSONNumbersWhileRedacting(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"count":9007199254740993,"ratio":0.12345678901234567890123456789,"large":1e400,"nested":[{"sequence":18446744073709551615,"token":"secret-value"}]}`)
	record, err := store.Record(Record{Kind: "test", Source: "unit", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Count  json.Number `json:"count"`
		Ratio  json.Number `json:"ratio"`
		Large  json.Number `json:"large"`
		Nested []struct {
			Sequence json.Number `json:"sequence"`
			Token    string      `json:"token"`
		} `json:"nested"`
	}
	if err := json.Unmarshal(stored.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != "9007199254740993" || decoded.Ratio != "0.12345678901234567890123456789" || decoded.Large != "1e400" || len(decoded.Nested) != 1 || decoded.Nested[0].Sequence != "18446744073709551615" || decoded.Nested[0].Token != "[REDACTED]" {
		t.Fatalf("evidence values were changed: %s", stored.Data)
	}
	for _, invalid := range []string{`{} {}`, `{} trailing`, `{"count":`} {
		if _, err := store.Record(Record{Kind: "test", Source: "unit", Data: json.RawMessage(invalid)}); err == nil {
			t.Fatalf("accepted invalid JSON %q", invalid)
		}
	}
}

func TestStoreRecordsAndLinksAreImmutableAcrossInstances(t *testing.T) {
	for _, legacyLayout := range []bool{false, true} {
		name := "current"
		if legacyLayout {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Record(Record{ID: "record-1", Kind: "test", Source: "unit", Summary: "original"})
			if err != nil {
				t.Fatal(err)
			}
			link, err := store.CreateLink(Link{ID: "link-1", EvidenceID: record.ID, TaskID: "task-1"})
			if err != nil {
				t.Fatal(err)
			}
			if legacyLayout {
				for _, directory := range []string{"evidence", "evidence-links"} {
					path := filepath.Join(root, ".rsch", directory)
					entries, err := os.ReadDir(path)
					if err != nil {
						t.Fatal(err)
					}
					if len(entries) != 1 {
						t.Fatalf("unexpected files in %s: %v", path, entries)
					}
					if err := os.Rename(filepath.Join(path, entries[0].Name()), filepath.Join(path, "20260101T000000.000000000Z-legacy-"+entries[0].Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			other, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, advanceTime := range []bool{false, true} {
				update := record
				update.Summary = "replacement"
				linkUpdate := link
				linkUpdate.TaskID = "task-2"
				if advanceTime {
					update.CapturedAt = update.CapturedAt.Add(time.Second)
					update.Kind = "changed"
					linkUpdate.CreatedAt = linkUpdate.CreatedAt.Add(time.Second)
				}
				if _, err := other.Record(update); !errors.Is(err, os.ErrExist) {
					t.Fatalf("duplicate evidence should fail with ErrExist: %v", err)
				}
				if _, err := other.CreateLink(linkUpdate); !errors.Is(err, os.ErrExist) {
					t.Fatalf("duplicate link should fail with ErrExist: %v", err)
				}
			}
			items, err := other.List(0)
			if err != nil || len(items) != 1 || items[0].Summary != "original" {
				t.Fatalf("original evidence changed: items=%+v err=%v", items, err)
			}
			links, err := other.ListLinks(LinkQuery{}, 0)
			if err != nil || len(links) != 1 || links[0].TaskID != "task-1" {
				t.Fatalf("original link changed: links=%+v err=%v", links, err)
			}
		})
	}
}

func TestStoreConcurrentDuplicateWritesPublishExactlyOneRecord(t *testing.T) {
	root := t.TempDir()
	const writers = 8
	stores := make([]*Store, writers)
	for i := range stores {
		var err error
		stores[i], err = New(root)
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, writers)
	start := make(chan struct{})
	for _, store := range stores {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			_, err := store.Record(Record{ID: "shared", Kind: "test", Source: "unit"})
			results <- err
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	for err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatalf("unexpected record error: %v", err)
		}
	}
	items, err := stores[0].List(0)
	if err != nil || created != 1 || len(items) != 1 {
		t.Fatalf("created=%d records=%+v err=%v", created, items, err)
	}
}
