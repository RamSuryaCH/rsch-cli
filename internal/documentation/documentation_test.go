package docindex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScanFindsHeadingsAndBrokenLocalLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Project\n\nSee [missing](missing.md).\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Documents) != 1 || report.Documents[0].Title != "Project" || len(report.BrokenLinks) != 1 {
		t.Fatalf("unexpected documentation report: %+v", report)
	}
}

func TestScanUsesMarkdownLinkAndHeadingSyntax(t *testing.T) {
	root := t.TempDir()
	source := "Project title\n=============\n\n" +
		"See [multiword link label](missing.md), [reference][guide] and ![image label](diagram.png).\n\n" +
		"[guide]: <missing guide.md> \"Guide title\"\n\n" +
		"## Real heading ##\n\n" +
		"`[inline example](not-a-link.md)`\n\n" +
		"```markdown\n# Example heading\n[example](not-a-link.md)\n```\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	doc := report.Documents[0]
	if !reflect.DeepEqual(doc.Headings, []string{"Project title", "Real heading"}) {
		t.Fatalf("unexpected headings: %#v", doc.Headings)
	}
	if !reflect.DeepEqual(doc.Links, []string{"missing.md", "missing guide.md", "diagram.png"}) {
		t.Fatalf("unexpected links: %#v", doc.Links)
	}
	if len(report.BrokenLinks) != 3 || report.BrokenLinks[0].From != "README.md" {
		t.Fatalf("unexpected broken links: %+v", report.BrokenLinks)
	}
}

func TestScanResolvesWorkspaceLinksAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Project"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := "[root](/README.md) [parent](../README.md) [remote](//example.com/docs)\n" +
		"[escape](../../outside.md) [encoded escape](%2E%2E/%2E%2E/outside.md)"
	if err := os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.BrokenLinks) != 2 {
		t.Fatalf("unexpected broken links: %+v", report.BrokenLinks)
	}
	for _, link := range report.BrokenLinks {
		if link.Reason != "target is outside the workspace" || link.From != "docs/guide.md" {
			t.Errorf("unexpected escape diagnostic: %+v", link)
		}
	}
}

func TestScanDoesNotReadSymlinkedDocuments(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	external := filepath.Join(outside, "external.md")
	if err := os.WriteFile(external, []byte("# External private title"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "linked.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Project\n[link](linked.md)"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Documents) != 1 || len(report.BrokenLinks) != 1 {
		t.Fatalf("scan followed external symlink: %+v", report)
	}
}

func TestScanResolvesEscapedMarkdownDestinations(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"guide(1).md", "a&b.md", "100%.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("# Guide"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := `[escaped](guide\(1\).md) [entity](a&amp;b.md) [percent](100%.md)`
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.BrokenLinks) != 0 {
		t.Fatalf("valid Markdown destinations were rejected: %+v", report.BrokenLinks)
	}
}

func TestScanAcceptsLongMarkdownLinesAndBoundsDocumentSize(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("# Project\n"+strings.Repeat("a", 128<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), root); err != nil {
		t.Fatalf("long Markdown paragraph failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("a", maxDocumentBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), root); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized document error = %v", err)
	}
}

func TestAssessReportsDocumentationGaps(t *testing.T) {
	impact := Assess(Report{Documents: []Document{{Path: "docs/configuration.md", Title: "Configuration"}}}, []string{"internal/api/routes.go", "rsch.json"})
	if len(impact.Required) != 3 || len(impact.Missing) != 2 {
		t.Fatalf("unexpected impact: %+v", impact)
	}
	if len(impact.Covered["configuration documentation"]) != 1 {
		t.Fatalf("configuration document was not detected: %+v", impact)
	}
}
