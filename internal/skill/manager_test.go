package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillLifecycleAndNonOverwritingInstall(t *testing.T) {
	root := t.TempDir()
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create("security")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatal(err)
	}
	entry, err := manager.Info(" security ")
	if err != nil || entry.Manifest != nil {
		t.Fatalf("expected normalized skill lookup without implicit manifest: %+v %v", entry, err)
	}
	if _, err := manager.Create("security"); err == nil {
		t.Fatal("expected duplicate skill rejection")
	}
	if _, err := manager.Install("security", created); err == nil {
		t.Fatal("expected non-overwriting install rejection")
	}
	entries, err := manager.List()
	if err != nil || len(entries) != 1 || !entries[0].HasSkillMD {
		t.Fatalf("unexpected skill list: %+v %v", entries, err)
	}
	if err := manager.Remove("security"); err != nil {
		t.Fatal(err)
	}
}

func TestSkillInstallRejectsSymlinkAndMissingSkillFile(t *testing.T) {
	root := t.TempDir()
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("no skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install("bad", source); err == nil {
		t.Fatal("expected SKILL.md requirement")
	}
	if _, err := os.Stat(filepath.Join(root, ".rsch", "skills", "bad")); !os.IsNotExist(err) {
		t.Fatalf("failed install left destination behind: %v", err)
	}
	if err := os.Symlink(source, filepath.Join(root, "link")); err == nil {
		if _, err := manager.Install("linked", filepath.Join(root, "link")); err == nil {
			t.Fatal("expected symlink rejection")
		}
	}
}

func TestSkillResolveOrdersDependenciesAndChecksVersions(t *testing.T) {
	root := t.TempDir()
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"base", "web", "conflict"} {
		if _, err := manager.Create(name); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest := func(name string, manifest Manifest) {
		t.Helper()
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".rsch", "skills", name, manifestFile), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest("base", Manifest{ID: "base", Name: "Base", Version: "1.2.0"})
	writeManifest("web", Manifest{ID: "web", Name: "Web", Version: "2.0.0", Dependencies: []string{"base@^1.0.0"}})
	writeManifest("conflict", Manifest{ID: "conflict", Name: "Conflict", Version: "1.0.0", Conflicts: []string{"web"}})
	resolved, err := manager.Resolve("web")
	if err != nil || len(resolved) != 2 || resolved[0].Name != "base" || resolved[1].Name != "web" {
		t.Fatalf("unexpected dependency resolution: %+v %v", resolved, err)
	}
	if _, err := manager.Resolve("web", "conflict"); err == nil {
		t.Fatal("expected declared conflict")
	}
	if _, err := manager.Compose("web"); err != nil {
		t.Fatal(err)
	}
}

func TestSkillResolveDetectsCyclesAndSymlinkedCatalog(t *testing.T) {
	root := t.TempDir()
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if _, err := manager.Create(name); err != nil {
			t.Fatal(err)
		}
	}
	for name, dependency := range map[string]string{"one": "two", "two": "one"} {
		manifest := Manifest{ID: name, Name: name, Version: "1", Dependencies: []string{dependency}}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".rsch", "skills", name, manifestFile), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.Resolve("one"); err == nil {
		t.Fatal("expected dependency cycle")
	}
	if err := os.RemoveAll(filepath.Join(root, ".rsch")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".rsch")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := manager.List(); err == nil {
		t.Fatal("expected symlinked skill catalog rejection")
	}
}

func TestSkillSearchMetadataAndActivation(t *testing.T) {
	root := t.TempDir()
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := manager.Create("web-research")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: "web-research", Name: "Web Research", Description: "Retrieve and synthesize browser evidence", Tags: []string{"browser", "research"}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), manifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := manager.Search("browser", 10)
	if err != nil || len(results) != 1 || results[0].Entry.Name != "web-research" {
		t.Fatalf("unexpected search results: %+v %v", results, err)
	}
	if err := manager.SetEnabled("web-research", false); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled("web-research", false); err != nil {
		t.Fatalf("disabling an already-disabled skill must be idempotent: %v", err)
	}
	entry, err := manager.Info("web-research")
	if err != nil || entry.Enabled {
		t.Fatalf("expected disabled skill: %+v %v", entry, err)
	}
	if err := manager.SetEnabled("web-research", true); err != nil {
		t.Fatal(err)
	}
	entry, err = manager.Info("web-research")
	if err != nil || !entry.Enabled {
		t.Fatalf("expected enabled skill: %+v %v", entry, err)
	}
	issues, err := manager.Audit()
	if err != nil || len(issues) != 0 {
		t.Fatalf("unexpected audit issues: %+v %v", issues, err)
	}
}

func TestSkillAuditReportsMissingInstructionsAndMalformedMetadata(t *testing.T) {
	root := t.TempDir()
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, ".rsch", "skills", "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, manifestFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	issues, err := manager.Audit()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Severity != "error" {
		t.Fatalf("expected one malformed-skill error, got %+v", issues)
	}
}

func TestSetEnabledRejectsSymlinkMarker(t *testing.T) {
	for _, targetKind := range []string{"outside", "instructions"} {
		t.Run(targetKind, func(t *testing.T) {
			manager, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path, err := manager.Create("demo")
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "untouched.txt")
			linkTarget := target
			if targetKind == "instructions" {
				target = path
				linkTarget = "SKILL.md"
			}
			if err := os.WriteFile(target, []byte("original contents"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(linkTarget, filepath.Join(filepath.Dir(path), disabledFile)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := manager.SetEnabled("demo", false); err == nil {
				t.Fatal("disable must reject a symlink marker")
			}
			contents, err := os.ReadFile(target)
			if err != nil || string(contents) != "original contents" {
				t.Fatalf("disable modified the symlink target: %q, %v", contents, err)
			}
		})
	}
}

func TestInstallRejectsInvalidSkillBeforePublishing(t *testing.T) {
	for _, invalid := range []string{"instructions-directory", "malformed-metadata", "metadata-directory"} {
		t.Run(invalid, func(t *testing.T) {
			manager, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := t.TempDir()
			if invalid == "instructions-directory" {
				if err := os.Mkdir(filepath.Join(source, "SKILL.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Instructions"), 0o600); err != nil {
				t.Fatal(err)
			}
			if invalid == "malformed-metadata" {
				if err := os.WriteFile(filepath.Join(source, manifestFile), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if invalid == "metadata-directory" {
				if err := os.Mkdir(filepath.Join(source, manifestFile), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.Install("invalid", source); err == nil {
				t.Fatal("invalid skill was installed")
			}
			entries, err := manager.List()
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed install polluted the catalog: %+v, %v", entries, err)
			}
		})
	}
}

func TestInstallPreservesValidSkillMetadata(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	for name, contents := range map[string]string{
		"SKILL.md":   "# Reusable instructions",
		manifestFile: `{"id":"demo","name":"Demo","version":"1.2.0"}`,
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.Install("demo", source); err != nil {
		t.Fatal(err)
	}
	entry, err := manager.Info("demo")
	if err != nil || entry.Manifest == nil || entry.Manifest.Version != "1.2.0" {
		t.Fatalf("installed metadata was not preserved: %+v, %v", entry, err)
	}
	body, err := manager.Compose("demo@^1.0.0")
	if err != nil || body != "# Reusable instructions" {
		t.Fatalf("installed skill could not be composed: %q, %v", body, err)
	}
}

func TestSearchIgnoresMetadataAndInstructionCase(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := manager.Create("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# KUBERNETES instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), manifestFile), []byte(`{"id":"demo","description":"Browser Research"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"browser", "BROWSER", "kubernetes", "KUBERNETES"} {
		results, err := manager.Search(query, 1)
		if err != nil || len(results) != 1 || results[0].Entry.Name != "demo" {
			t.Errorf("search %q: %+v, %v", query, results, err)
		}
	}
}
