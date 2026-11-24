package intelligence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestArchitectureAndImpactUseImportEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service.go"), []byte("package main\nfunc Service() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "handler.go"), []byte("package api\nimport \"service\"\nfunc Handle() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := index.Impact(context.Background(), []string{"service.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.DirectFiles) != 1 || len(impact.IndirectFiles) != 1 || len(impact.APIs) != 1 || impact.Risk != "high" {
		t.Fatalf("unexpected impact: %+v", impact)
	}
}

func TestArchitectureResolvesGoModulePackageImports(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"service", "api"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixtures := map[string]string{
		"go.mod":             "module example.com/project\n\ngo 1.24\n",
		"service/service.go": "package service\nfunc Run() {}\n",
		"api/handler.go":     "package api\nimport \"example.com/project/service\"\nfunc Handle() { service.Run() }\n",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := index.Impact(context.Background(), []string{"service/service.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.IndirectFiles) != 1 || impact.IndirectFiles[0] != "api/handler.go" {
		t.Fatalf("module import impact = %+v, want api/handler.go", impact)
	}
}

func TestArchitectureResolvesRelativeTypeScriptImports(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"src/service.ts":   "export const run = () => true\n",
		"src/api/route.ts": "import { run } from '../service'\nexport const handle = () => run()\n",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := index.Impact(context.Background(), []string{"src/service.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.IndirectFiles) != 1 || impact.IndirectFiles[0] != "src/api/route.ts" {
		t.Fatalf("relative TypeScript impact = %+v, want src/api/route.ts", impact)
	}
}

func TestArchitectureRevalidatesIndexedPaths(t *testing.T) {
	for _, target := range []string{"outside", "secret", "ignored"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			alias := filepath.Join(root, "alias.ts")
			if err := os.WriteFile(alias, []byte("export const initial = true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			index, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := index.Build(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(alias); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "credentials.json")
			if target == "outside" {
				path = filepath.Join(t.TempDir(), "outside.ts")
			} else if target == "ignored" {
				path = filepath.Join(root, "ignored.ts")
				if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.ts\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("export const hidden = true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path, alias); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			graph, err := index.Architecture(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
				t.Fatalf("excluded file contributed architecture facts: %+v", graph)
			}
		})
	}
}
