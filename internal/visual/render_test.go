package visual

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/intelligence"
)

func TestArchitectureGroupsPreservesRealOtherModules(t *testing.T) {
	for _, otherNodes := range []int{1, 4} {
		t.Run(fmt.Sprintf("other-nodes-%d", otherNodes), func(t *testing.T) {
			graph := intelligence.Graph{}
			for i := range renderMaxGroups + 1 {
				for j := range 2 {
					path := fmt.Sprintf("module-%02d/file-%d.go", i, j)
					graph.Nodes = append(graph.Nodes, intelligence.GraphNode{ID: path, Kind: "file", Path: path})
				}
			}
			for _, name := range []string{"other", "other-2"} {
				for i := range otherNodes {
					path := fmt.Sprintf("%s/file-%d.go", name, i)
					graph.Nodes = append(graph.Nodes, intelligence.GraphNode{ID: path, Kind: "file", Path: path})
				}
			}
			graph.Edges = []intelligence.GraphEdge{{From: "other/file-0.go", To: "module-00/file-0.go", Kind: "imports"}}
			groups, nodeGroups, edges := architectureGroups(graph)
			if len(groups) != renderMaxGroups {
				t.Fatalf("got %d groups, want %d", len(groups), renderMaxGroups)
			}
			actualCounts := make(map[string]int)
			for _, group := range nodeGroups {
				actualCounts[group]++
			}
			seen := make(map[string]bool)
			total := 0
			for _, group := range groups {
				if seen[group.Name] {
					t.Fatalf("duplicate group %q", group.Name)
				}
				seen[group.Name] = true
				total += group.Nodes
				if group.Nodes != actualCounts[group.Name] {
					t.Errorf("group %q reports %d nodes, but contains %d", group.Name, group.Nodes, actualCounts[group.Name])
				}
			}
			if total != len(graph.Nodes) {
				t.Errorf("group total = %d, want all %d nodes", total, len(graph.Nodes))
			}
			if len(edges) != 1 || edges[0] != [2]string{nodeGroups["other/file-0.go"], nodeGroups["module-00/file-0.go"]} {
				t.Fatalf("real other module import lost or misdirected: %v", edges)
			}
		})
	}
}

func TestRenderArchitectureCannotWriteThroughEscapingDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".rsch")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	graph := intelligence.Graph{Nodes: []intelligence.GraphNode{{ID: "main", Kind: "file", Path: "main.go"}}}
	if _, err := RenderArchitecture(root, graph); err == nil {
		t.Fatal("RenderArchitecture succeeded through an escaping directory symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("RenderArchitecture wrote outside the repository: %v", entries)
	}
}

func TestRenderArchitectureReplacesOutputWithoutFollowingLinks(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			victim := filepath.Join(t.TempDir(), "keep.txt")
			if err := os.WriteFile(victim, []byte("preserve me"), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, ".rsch", "visual")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(victim, filepath.Join(dir, "architecture.png")); err != nil {
				t.Skipf("%s unavailable: %v", kind, err)
			}
			graph := intelligence.Graph{Nodes: []intelligence.GraphNode{{ID: "main", Kind: "file", Path: "main.go"}}}
			if _, err := RenderArchitecture(root, graph); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "preserve me" {
				t.Fatalf("linked file changed: length %d, error %v", len(data), err)
			}
		})
	}
}

func TestRenderArchitectureWritesInspectablePNG(t *testing.T) {
	root := t.TempDir()
	graph := intelligence.Graph{
		Nodes: []intelligence.GraphNode{
			{ID: "file:cmd/main.go", Kind: "file", Path: "cmd/main.go"},
			{ID: "file:internal/server/server.go", Kind: "file", Path: "internal/server/server.go"},
			{ID: "file:internal/review/review.go", Kind: "file", Path: "internal/review/review.go"},
		},
		Edges: []intelligence.GraphEdge{
			{From: "file:cmd/main.go", To: "file:internal/server/server.go", Kind: "imports"},
			{From: "file:internal/server/server.go", To: "file:internal/review/review.go", Kind: "imports"},
		},
	}
	report, err := RenderArchitecture(root, graph)
	if err != nil {
		t.Fatal(err)
	}
	if report.Format != "png" || report.Width <= 0 || report.Height <= 0 || len(report.Dominant) == 0 {
		t.Fatalf("unexpected rendered report: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(root, ".rsch", "visual", "architecture.png")); err != nil {
		t.Fatal(err)
	}
}
