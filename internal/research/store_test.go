package research

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRejectsDuplicateIDsWithoutChangingFiles(t *testing.T) {
	for _, kind := range []string{"source", "evidence", "claim"} {
		t.Run(kind, func(t *testing.T) {
			store, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source, err := store.AddSource(Source{ID: "source", URI: "https://example.com/original"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AddEvidence(Evidence{ID: "evidence", SourceID: source.ID, Quote: "original quote"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AddClaim(Claim{ID: "claim", Text: "original claim"}); err != nil {
				t.Fatal(err)
			}
			before := readResearchFile(t, store.path)
			backup := readResearchFile(t, store.path+".bak")
			switch kind {
			case "source":
				_, err = store.AddSource(Source{ID: "source", URI: "https://example.com/different"})
			case "evidence":
				_, err = store.AddEvidence(Evidence{ID: "evidence", SourceID: source.ID, Quote: "different quote"})
			case "claim":
				_, err = store.AddClaim(Claim{ID: "claim", Text: "different claim"})
			}
			if err == nil || !strings.Contains(err.Error(), "duplicate "+kind) {
				t.Fatalf("expected duplicate %s error, got %v", kind, err)
			}
			if !bytes.Equal(before, readResearchFile(t, store.path)) || !bytes.Equal(backup, readResearchFile(t, store.path+".bak")) {
				t.Fatal("invalid mutation changed the primary or backup")
			}
			if status := store.Health(); !status.Healthy || status.Sources != 1 || status.Evidence != 1 || status.Claims != 1 {
				t.Fatalf("invalid mutation damaged research provenance: %+v", status)
			}
		})
	}
}

func TestStoreInitialWriteHasRecoveryBackup(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSource(Source{URI: "https://example.com/first"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readResearchFile(t, store.path), readResearchFile(t, store.path+".bak")) {
		t.Fatal("initial backup differs from the primary")
	}
}

func TestStoreMutationPreservesValidRecoveryBackup(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if _, err := store.AddSource(Source{URI: "https://example.com/" + name}); err != nil {
			t.Fatal(err)
		}
	}
	backup := readResearchFile(t, store.path+".bak")
	if err := os.WriteFile(store.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSource(Source{URI: "https://example.com/after-recovery"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, readResearchFile(t, store.path+".bak")) {
		t.Fatal("recovery mutation replaced the valid backup with corrupt data")
	}
	if status := store.Health(); !status.Healthy || status.Sources != 2 {
		t.Fatalf("unexpected recovered store: %+v", status)
	}
}

func TestStoreBackupFailureLeavesPrimaryUnchanged(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSource(Source{URI: "https://example.com/first"}); err != nil {
		t.Fatal(err)
	}
	before := readResearchFile(t, store.path)
	if err := os.Remove(store.path + ".bak"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.path+".bak", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSource(Source{URI: "https://example.com/second"}); err == nil {
		t.Fatal("expected backup write error")
	}
	if !bytes.Equal(before, readResearchFile(t, store.path)) {
		t.Fatal("primary changed after backup write failed")
	}
}

func TestStoreDoesNotDowngradeFutureVersionFromBackup(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if _, err := store.AddSource(Source{URI: "https://example.com/" + name}); err != nil {
			t.Fatal(err)
		}
	}
	future := []byte(`{"version":99,"sources":[],"evidence":[],"claims":[]}`)
	if err := os.WriteFile(store.path, future, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSource(Source{URI: "https://example.com/new"}); err == nil || !strings.Contains(err.Error(), "unsupported research store version 99") {
		t.Fatalf("expected unsupported version error, got %v", err)
	}
	if !bytes.Equal(future, readResearchFile(t, store.path)) {
		t.Fatal("future-version primary was overwritten")
	}
}

func readResearchFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStorePreservesClaimProvenanceAndDeduplicatesSources(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.AddSource(Source{URI: "https://example.com/report", Title: "Report", Confidence: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.AddSource(Source{URI: source.URI, Title: "duplicate"})
	if err != nil || duplicate.ID != source.ID {
		t.Fatalf("source was not deduplicated: %+v %v", duplicate, err)
	}
	evidence, err := store.AddEvidence(Evidence{SourceID: source.ID, Quote: "SQLite is durable", Location: "p. 4", Supports: true, Confidence: 0.8})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.AddClaim(Claim{Text: "SQLite is durable", EvidenceIDs: []string{evidence.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Status != "supported" || len(claim.EvidenceIDs) != 1 {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	results, err := store.Search("durable", 10)
	if err != nil || len(results) != 1 || results[0].Claim.ID != claim.ID {
		t.Fatalf("unexpected search: %+v %v", results, err)
	}
}

func TestStoreRejectsEvidenceWithoutSource(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddEvidence(Evidence{SourceID: "missing", Quote: "quote"}); err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestStoreEvidenceDeduplicationPreservesPolarity(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.AddSource(Source{URI: "https://example.com/report"})
	if err != nil {
		t.Fatal(err)
	}
	positive, err := store.AddEvidence(Evidence{SourceID: source.ID, Quote: "reported observation", Supports: true})
	if err != nil {
		t.Fatal(err)
	}
	negative, err := store.AddEvidence(Evidence{SourceID: source.ID, Quote: positive.Quote, Supports: false})
	if err != nil {
		t.Fatal(err)
	}
	if negative.ID == positive.ID || negative.Supports {
		t.Fatalf("contradicting evidence was replaced with supporting evidence: %+v", negative)
	}
	claim, err := store.AddClaim(Claim{Text: "A claim contradicted by the observation", EvidenceIDs: []string{negative.ID}})
	if err != nil || claim.Status != "contradicted" {
		t.Fatalf("evidence polarity changed the claim outcome: claim=%+v err=%v", claim, err)
	}
	duplicate, err := store.AddEvidence(Evidence{SourceID: source.ID, Quote: positive.Quote, Supports: false})
	if err != nil || duplicate.ID != negative.ID {
		t.Fatalf("identical evidence was not deduplicated: evidence=%+v err=%v", duplicate, err)
	}
}

func TestStoreEvidenceHashCollisionPreservesProvenance(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstSource, err := store.AddSource(Source{URI: "https://example.com/first"})
	if err != nil {
		t.Fatal(err)
	}
	secondSource, err := store.AddSource(Source{URI: "https://example.com/second"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.AddEvidence(Evidence{SourceID: firstSource.ID, Quote: "first observation", Location: "page 1"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.AddEvidence(Evidence{SourceID: secondSource.ID, Quote: "second observation", Location: "page 2", Hash: original.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == original.ID || next.SourceID != secondSource.ID || next.Quote != "second observation" || next.Location != "page 2" {
		t.Fatalf("caller-supplied hash replaced evidence provenance: %+v", next)
	}
	if status := store.Health(); !status.Healthy || status.Evidence != 2 {
		t.Fatalf("unexpected durable evidence: %+v", status)
	}
}

func TestStoreHealthRejectsDanglingProvenance(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot{Version: 1, Sources: []Source{{ID: "source", URI: "https://example.com"}}, Evidence: []Evidence{{ID: "evidence", SourceID: "missing", Quote: "quote"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".rsch", "research", "records.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status := store.Health()
	if status.Available || status.Healthy || !strings.Contains(status.Reason, "missing source") {
		t.Fatalf("unexpected unhealthy research status: %+v", status)
	}
	if _, err := store.Sources(); err == nil {
		t.Fatal("expected reads to reject dangling provenance")
	}
}

func TestStoreHealthReportsDurableProvider(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	status := store.Health()
	if !status.Available || !status.Healthy || status.Sources != 0 || status.Evidence != 0 || status.Claims != 0 {
		t.Fatalf("unexpected empty store health: %+v", status)
	}
}
