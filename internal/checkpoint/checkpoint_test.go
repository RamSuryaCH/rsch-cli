package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCheckpointManifestFilenameRoundTrip(t *testing.T) {
	root := t.TempDir()
	paths := []string{"manifest.json", "files/manifest.json"}
	for _, path := range paths {
		target := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("original "+path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(root)
	if _, err := manager.Create("manifest-file", paths); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if err := os.WriteFile(filepath.Join(root, path), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.RestoreWithApproval("manifest-file", true); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(data) != "original "+path {
			t.Fatalf("restored %q = %q, error = %v", path, data, err)
		}
	}
}

func TestRestoreLegacyCheckpointLayouts(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			root := t.TempDir()
			manager := NewManager(root)
			dir := filepath.Join(manager.Root, "legacy")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			data := []byte("legacy contents")
			sum := sha256.Sum256(data)
			manifest := Manifest{Version: version, ID: "legacy", Files: []File{{Path: "saved.txt", Hash: hex.EncodeToString(sum[:]), Exists: version >= 2, Mode: 0o751}}}
			if version >= 2 {
				manifest.Files = append(manifest.Files, File{Path: "absent.txt"})
				if err := os.WriteFile(filepath.Join(root, "absent.txt"), []byte("later"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "manifest.json"), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "saved.txt"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := manager.RestoreWithApproval("legacy", true); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, "saved.txt"))
			if err != nil || string(got) != string(data) {
				t.Fatalf("restored contents = %q, error = %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(root, "absent.txt")); !os.IsNotExist(err) {
				t.Fatalf("restored absent file: %v", err)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Join(root, "saved.txt"))
				if err != nil {
					t.Fatal(err)
				}
				want := os.FileMode(0o600)
				if version >= 2 {
					want = 0o751
				}
				if info.Mode().Perm() != want {
					t.Fatalf("restored mode = %o, want %o", info.Mode().Perm(), want)
				}
			}
		})
	}
}

func TestRestoreRefusesToOverwriteChangedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	if _, err := manager.Create("c1", []string{path}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore("c1"); err == nil {
		t.Fatal("expected conflict")
	}
}

func TestRestorePlanRequiresApproval(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	if _, err := manager.Create("c2", []string{path}); err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Plan("c2")
	if err != nil || len(plan.Affected) != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if err := manager.RestoreWithApproval("c2", false); err == nil {
		t.Fatal("expected approval requirement")
	}
}

func TestCheckpointPreservesWorkflowMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	manifest, err := manager.CreateWithMetadata("metadata", []string{path}, Metadata{
		SessionID:    "session-1",
		GoalID:       "goal-1",
		TaskID:       "task-1",
		AgentID:      "agent-1",
		Verification: []string{"go test ./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Metadata.TaskID != "task-1" || len(manifest.Metadata.Verification) != 1 {
		t.Fatalf("metadata not preserved: %+v", manifest.Metadata)
	}
	items, err := manager.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("listed checkpoints=%d err=%v", len(items), err)
	}
	if items[0].Metadata.SessionID != "session-1" {
		t.Fatalf("listed metadata not preserved: %+v", items[0].Metadata)
	}
}

func TestApprovedRestorePreservesAbsenceContentAndMode(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "script.sh")
	absent := filepath.Join(root, "generated.txt")
	if err := os.WriteFile(existing, []byte("original\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	manifest, err := manager.Create("restore-state", []string{absent, existing})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != manifestVersion || len(manifest.Files) != 2 || manifest.Files[0].Path != "generated.txt" || manifest.Files[0].Exists {
		t.Fatalf("manifest did not preserve deterministic absence: %+v", manifest)
	}
	if err := os.WriteFile(existing, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absent, []byte("created later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Plan("restore-state")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 2 || plan.Risk != "high" {
		t.Fatalf("plan did not expose both changes: %+v", plan)
	}
	if err := manager.RestoreWithApproval("restore-state", true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "original\n" {
		t.Fatalf("restored content = %q, err=%v", data, err)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("originally absent file still exists: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(existing)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o751 {
			t.Fatalf("restored mode = %o, want 751", got)
		}
	}
}

func TestRestoreTransactionRollsBackAfterInjectedPartialFailure(t *testing.T) {
	states := map[string]fileState{
		"a.txt": {Path: "a.txt", Data: []byte("old-a"), Exists: true, Mode: 0o600},
		"b.txt": {Path: "b.txt", Data: []byte("old-b"), Exists: true, Mode: 0o640},
	}
	changes := []restoreChange{
		{Before: states["b.txt"], After: fileState{Path: "b.txt", Data: []byte("new-b"), Exists: true, Mode: 0o600}},
		{Before: states["a.txt"], After: fileState{Path: "a.txt", Data: []byte("new-a"), Exists: true, Mode: 0o600}},
	}
	forwardCalls := 0
	failureInjected := false
	apply := func(state fileState) error {
		state.Data = append([]byte(nil), state.Data...)
		states[state.Path] = state
		if !failureInjected {
			forwardCalls++
			if forwardCalls == 2 {
				failureInjected = true
				return errors.New("injected restore failure")
			}
		}
		return nil
	}
	err := applyRestoreTransaction(changes, apply)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("restore error = %v, want rollback evidence", err)
	}
	want := map[string]fileState{
		"a.txt": {Path: "a.txt", Data: []byte("old-a"), Exists: true, Mode: 0o600},
		"b.txt": {Path: "b.txt", Data: []byte("old-b"), Exists: true, Mode: 0o640},
	}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("states after rollback = %#v, want %#v", states, want)
	}
}

func TestCheckpointCreationIsAllOrNothing(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid.txt")
	if err := os.WriteFile(valid, []byte("valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	_, err := manager.Create("partial", []string{valid, root})
	if err == nil {
		t.Fatal("expected directory snapshot to fail")
	}
	if _, statErr := os.Stat(filepath.Join(manager.Root, "partial")); !os.IsNotExist(statErr) {
		t.Fatalf("partial checkpoint was published: %v", statErr)
	}
}
