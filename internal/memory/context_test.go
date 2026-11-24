package memory

import (
	"strings"
	"testing"
	"time"
)

func TestRetrieveContextUsesOwnedScopesAndPreservesProvenance(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{ID: "project", Scope: "repository", Layer: LayerProject, Content: "OAuth uses PKCE", Provenance: Provenance{SourceType: "file", SourceID: "docs/auth.md", Location: "line 12", ObservedAt: now.Add(-time.Hour)}},
		{ID: "session-a", Scope: "session", ScopeID: "session-a", Layer: LayerSession, Content: "OAuth callback is under review", Provenance: Provenance{SourceType: "session", SourceID: "session-a", ObservedAt: now.Add(-time.Minute)}},
		{ID: "session-b", Scope: "session", ScopeID: "session-b", Layer: LayerSession, Content: "OAuth rollout finished", Provenance: Provenance{SourceType: "session", SourceID: "session-b", ObservedAt: now}},
	}
	for _, entry := range entries {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.RetrieveContext(ContextOptions{
		Query: "OAuth",
		Scopes: []ContextScope{
			{Layer: LayerProject},
			{Layer: LayerSession, ID: "session-a"},
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items=%+v", result.Items)
	}
	ids := map[string]bool{}
	for _, item := range result.Items {
		ids[item.Entry.ID] = true
	}
	if !ids["project"] || !ids["session-a"] || ids["session-b"] {
		t.Fatalf("scope isolation failed: %+v", ids)
	}
	if !strings.Contains(result.Text, `"sourceId":"docs/auth.md"`) || !strings.Contains(result.Text, `"scopeId":"session-a"`) {
		t.Fatalf("rendered context lost provenance: %s", result.Text)
	}
	if result.Used > result.Budget {
		t.Fatalf("used=%d budget=%d", result.Used, result.Budget)
	}
}

func TestRetrieveContextDefaultsExcludeSessionAndTaskMemory(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{
		{ID: "project", Scope: "repository", Content: "Use SQLite"},
		{ID: "session", Scope: "session", ScopeID: "s-1", Layer: LayerSession, Content: "Use SQLite in this session"},
		{ID: "task", Scope: "task", ScopeID: "t-1", Layer: LayerTask, Content: "Use SQLite in this task"},
	} {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.RetrieveContext(ContextOptions{Query: "SQLite"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Entry.ID != "project" {
		t.Fatalf("unexpected default scope: %+v", result.Items)
	}
}

func TestRetrieveContextExclusionsRespectOwnedScopes(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Hour)
	expires := now.Add(-time.Minute)
	for _, entry := range []Entry{
		{ID: "foreign-expired", Scope: "session", ScopeID: "other", Content: "expired OAuth note", ExpiresAt: &expires},
		{ID: "foreign-deleted", Scope: "session", ScopeID: "other", Content: "deleted OAuth note", Status: "deleted"},
		{ID: "foreign-superseded", Scope: "task", ScopeID: "other", Content: "superseded OAuth note", Status: "superseded"},
		{ID: "owned-deleted", Scope: "session", ScopeID: "current", Content: "owned OAuth note", Status: "deleted"},
	} {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.RetrieveContext(ContextOptions{
		Query: "OAuth", Now: now,
		Scopes: []ContextScope{{Layer: LayerSession, ID: "current"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || len(result.Excluded) != 1 || result.Excluded[0].EntryID != "owned-deleted" {
		t.Fatalf("context disclosed another owner's records: %+v", result)
	}
	result, err = store.RetrieveContext(ContextOptions{Query: "OAuth", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || len(result.Excluded) != 0 {
		t.Fatalf("default context disclosed scoped records: %+v", result)
	}
}

func TestRetrieveContextFreshnessAndConflictPolicies(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	old := Entry{ID: "old", Scope: "repository", Content: "Deploy with blue strategy", Confidence: 0.9, Provenance: Provenance{ObservedAt: now.Add(-48 * time.Hour)}}
	fresh := Entry{ID: "fresh", Scope: "repository", Content: "Deploy with green strategy", Confidence: 0.8, Provenance: Provenance{ObservedAt: now.Add(-time.Hour)}, Relations: []Relation{{Type: "contradicts", EntryID: "old"}}}
	stale := Entry{ID: "stale", Scope: "repository", Content: "Deploy on Friday", Provenance: Provenance{ObservedAt: now.Add(-90 * 24 * time.Hour)}}
	for _, entry := range []Entry{old, fresh, stale} {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	preferred, err := store.RetrieveContext(ContextOptions{Query: "Deploy", Now: now, MaxAge: 60 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(preferred.Items) != 1 || preferred.Items[0].Entry.ID != "fresh" {
		t.Fatalf("fresh conflict winner not selected: %+v", preferred)
	}
	assertExcluded(t, preferred.Excluded, "old", "older conflicting memory")
	assertExcluded(t, preferred.Excluded, "stale", "stale")

	included, err := store.RetrieveContext(ContextOptions{Query: "Deploy", Now: now, ConflictPolicy: ConflictInclude})
	if err != nil {
		t.Fatal(err)
	}
	if len(included.Items) != 3 {
		t.Fatalf("include policy items=%+v", included.Items)
	}
	for _, item := range included.Items {
		if item.Entry.ID == "fresh" && len(item.ConflictsWith) != 1 {
			t.Fatalf("conflict provenance missing: %+v", item)
		}
	}

	excluded, err := store.RetrieveContext(ContextOptions{Query: "strategy", Now: now, ConflictPolicy: ConflictExclude})
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded.Items) != 0 {
		t.Fatalf("exclude policy retained conflicts: %+v", excluded.Items)
	}
}

func TestRetrieveContextHonorsNonMatchingSupersedingMemory(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	old := Entry{
		ID:         "old-deploy-command",
		Scope:      "repository",
		Content:    "Deploy with the legacy script",
		Provenance: Provenance{ObservedAt: now.Add(-24 * time.Hour)},
	}
	replacement := Entry{
		ID:         "new-release-command",
		Scope:      "repository",
		Content:    "Releases now run through the signed pipeline",
		Provenance: Provenance{ObservedAt: now},
		Relations:  []Relation{{Type: "supersedes", EntryID: old.ID}},
	}
	for _, entry := range []Entry{old, replacement} {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.RetrieveContext(ContextOptions{Query: "legacy script", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("superseded query match was retained: %+v", result.Items)
	}
	assertExcluded(t, result.Excluded, old.ID, "superseded")
}

func TestRetrieveContextIgnoresConflictsWithSupersededMemory(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, entry := range []Entry{
		{ID: "current", Content: "Deploy signed artifacts", Provenance: Provenance{ObservedAt: now.Add(-2 * time.Hour)}},
		{ID: "withdrawn", Content: "Deploy unsigned artifacts", Provenance: Provenance{ObservedAt: now.Add(-time.Hour)}, Relations: []Relation{{Type: "contradicts", EntryID: "current"}}},
		{ID: "correction", Content: "Unsigned artifacts were withdrawn", Provenance: Provenance{ObservedAt: now}, Relations: []Relation{{Type: "supersedes", EntryID: "withdrawn"}}},
	} {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}
	for _, policy := range []ConflictPolicy{ConflictPreferFresh, ConflictExclude, ConflictInclude} {
		t.Run(string(policy), func(t *testing.T) {
			result, err := store.RetrieveContext(ContextOptions{Query: "Deploy", Now: now, ConflictPolicy: policy})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 1 || result.Items[0].Entry.ID != "current" {
				t.Fatalf("superseded conflict suppressed active context: %+v", result)
			}
			if len(result.Items[0].ConflictsWith) != 0 {
				t.Fatalf("superseded entry remained an active conflict: %+v", result.Items[0])
			}
			assertExcluded(t, result.Excluded, "withdrawn", "superseded")
		})
	}
}

func TestRetrieveContextKeepsIdenticalContentInDifferentSessionsDistinct(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{
		{ID: "session-a", Scope: "session", ScopeID: "session-a", Layer: LayerSession, Content: "Use the local fixture"},
		{ID: "session-b", Scope: "session", ScopeID: "session-b", Layer: LayerSession, Content: "Use the local fixture"},
	} {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.RetrieveContext(ContextOptions{
		Query: "local fixture",
		Scopes: []ContextScope{
			{Layer: LayerSession, ID: "session-a"},
			{Layer: LayerSession, ID: "session-b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("same-content session memories were deduplicated: %+v", result.Items)
	}
	want := map[string]bool{"session-a": true, "session-b": true}
	for _, item := range result.Items {
		delete(want, item.Entry.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing session memories: %+v", want)
	}
}

func TestRetrieveContextHonorsExactCallerTokenBudget(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{
		{ID: "important", Scope: "repository", Content: "SQLite is the primary durable store", Importance: 0.9, Confidence: 0.9},
		{ID: "secondary", Scope: "repository", Content: "SQLite also supports local inspection workflows", Importance: 0.1, Confidence: 0.1},
	} {
		if _, err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}
	byteCounter := func(value string) int { return len([]byte(value)) }
	full, err := store.RetrieveContext(ContextOptions{Query: "SQLite", TokenBudget: 4096, TokenCounter: byteCounter})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Items) != 2 || full.Items[0].Entry.ID != "important" {
		t.Fatalf("ranking=%+v", full.Items)
	}
	limited, err := store.RetrieveContext(ContextOptions{Query: "SQLite", TokenBudget: full.Items[0].Tokens, TokenCounter: byteCounter})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Items) != 1 || limited.Items[0].Entry.ID != "important" || limited.Used > limited.Budget {
		t.Fatalf("bounded result=%+v", limited)
	}
	assertExcluded(t, limited.Excluded, "secondary", "token budget")
}

func TestSearchRankedMatchesMetadata(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Entry{Scope: "repository", Content: "Durable local store", Tags: []string{"database"}}); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchRanked("database", "repository", 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("metadata search=%+v err=%v", results, err)
	}
}

func assertExcluded(t *testing.T, exclusions []ContextExclusion, id, reason string) {
	t.Helper()
	for _, exclusion := range exclusions {
		if exclusion.EntryID == id && exclusion.Reason == reason {
			return
		}
	}
	t.Fatalf("missing exclusion %s/%s in %+v", id, reason, exclusions)
}
