package confidence

import (
	"encoding/json"
	"testing"

	"github.com/opencode-ai/opencode/internal/evidence"
)

func TestBuildDoesNotTreatUnavailableDimensionsAsPassing(t *testing.T) {
	root := t.TempDir()
	store, err := evidence.New(root)
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := json.Marshal(map[string]any{"totalSections": 2, "untrackedSections": 0})
	if _, err := store.Record(evidence.Record{Kind: "requirements", Source: "test", Data: audit}); err != nil {
		t.Fatal(err)
	}
	report, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Score == nil || *report.Score != 100 {
		t.Fatalf("score=%v", report.Score)
	}
	if report.Complete {
		t.Fatal("unobserved dimensions must prevent complete confidence")
	}
	for _, dimension := range report.Dimensions {
		if !dimension.Observed && dimension.Passed {
			t.Fatalf("unobserved dimension passed: %+v", dimension)
		}
	}
}

func TestBuildValidatesEvidenceBeforePassing(t *testing.T) {
	tests := []struct {
		name, kind, data string
		status           evidence.Status
		observed, passed bool
	}{
		{name: "empty requirements", kind: "requirements", data: `{}`, observed: true},
		{name: "null requirements", kind: "requirements", data: `null`, observed: true},
		{name: "missing counts", kind: "requirements", data: `{"totalSections":2}`, observed: true},
		{name: "negative counts", kind: "requirements", data: `{"totalSections":-1,"untrackedSections":0}`, observed: true},
		{name: "impossible counts", kind: "requirements", data: `{"totalSections":1,"untrackedSections":2}`, observed: true},
		{name: "zero sections", kind: "requirements", data: `{"totalSections":0,"untrackedSections":0}`, observed: true},
		{name: "failed requirements", kind: "requirements", data: `{"totalSections":2,"untrackedSections":0}`, status: evidence.Failed, observed: true},
		{name: "unavailable requirements", kind: "requirements", data: `{"totalSections":2,"untrackedSections":0}`, status: evidence.Unavailable},
		{name: "empty evaluation", kind: "evaluation", data: `{}`, observed: true},
		{name: "current evaluation", kind: "evaluation", data: `{"version":2,"evaluationType":"repository-quality","repositoryQualityPassed":true}`, observed: true, passed: true},
		{name: "current failure overrides legacy", kind: "evaluation", data: `{"repositoryQualityPassed":false,"taskSuccess":true}`, observed: true},
		{name: "legacy evaluation", kind: "evaluation", data: `{"taskSuccess":true}`, observed: true, passed: true},
		{name: "failed evaluation", kind: "evaluation", data: `{"repositoryQualityPassed":true}`, status: evidence.Failed, observed: true},
		{name: "unavailable evaluation", kind: "evaluation", data: `{"taskSuccess":true}`, status: evidence.Unavailable},
		{name: "unavailable security", kind: "security", data: `{}`, status: evidence.Unavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := evidence.New(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Record(evidence.Record{Kind: test.kind, Source: "test", Status: test.status, Data: json.RawMessage(test.data)}); err != nil {
				t.Fatal(err)
			}
			report, err := Build(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, dimension := range report.Dimensions {
				if dimension.Name == test.kind && (dimension.Observed != test.observed || dimension.Passed != test.passed) {
					t.Fatalf("dimension=%+v, want observed=%t passed=%t", dimension, test.observed, test.passed)
				}
			}
			if !test.observed && (report.Score != nil || report.ObservedWeight != 0) {
				t.Fatalf("unavailable evidence contributed to confidence: %+v", report)
			}
			if report.Complete {
				t.Fatal("a single dimension must not establish completion")
			}
		})
	}
}
