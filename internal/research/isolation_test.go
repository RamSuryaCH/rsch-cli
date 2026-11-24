package research

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreRejectsSymlinkedStatePaths(t *testing.T) {
	for _, name := range []string{".rsch", ".rsch/research", ".rsch/research/records.json", ".rsch/research/records.json.bak", ".rsch/research/records.json.lock"} {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			target := outside
			if strings.Contains(filepath.Base(name), "records.json") {
				target = filepath.Join(outside, "records.json")
				if err := os.WriteFile(target, []byte(`{"version":1,"sources":[],"evidence":[],"claims":[]}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := New(root); err == nil {
				t.Fatalf("accepted symlinked research path %s", name)
			}
		})
	}
}

func TestStoreRejectsStateDirectoryReplacement(t *testing.T) {
	for _, name := range []string{".rsch", ".rsch/research"} {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.Rename(path, path+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := store.AddSource(Source{URI: "https://example.com/escape"}); err == nil {
				t.Fatal("write followed replaced state directory")
			}
			if _, err := store.Sources(); err == nil {
				t.Fatal("read followed replaced state directory")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("outside directory was changed: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestStoreRejectsFileReplacement(t *testing.T) {
	for _, suffix := range []string{"", ".bak", ".lock"} {
		t.Run("records.json"+suffix, func(t *testing.T) {
			store, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AddSource(Source{URI: "https://example.com/original"}); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "records.json")
			before := readResearchFile(t, store.path)
			if err := os.WriteFile(outside, before, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(store.path + suffix); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, store.path+suffix); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := store.AddSource(Source{URI: "https://example.com/replaced"}); err == nil {
				t.Fatal("write accepted symlinked store file")
			}
			if suffix == "" {
				if _, err := store.Sources(); err == nil {
					t.Fatal("read followed symlinked primary")
				}
			}
			if !bytes.Equal(before, readResearchFile(t, outside)) {
				t.Fatal("external file changed")
			}
		})
	}
}

func TestStoreInstancesPreserveConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	stores := make([]*Store, 2)
	for i := range stores {
		var err error
		stores[i], err = New(root)
		if err != nil {
			t.Fatal(err)
		}
	}
	const count = 20
	var wait sync.WaitGroup
	start := make(chan struct{})
	for i := range count {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			if _, err := stores[i%len(stores)].AddSource(Source{URI: fmt.Sprintf("https://example.com/%d", i)}); err != nil {
				t.Errorf("concurrent write: %v", err)
			}
		}(i)
	}
	close(start)
	wait.Wait()
	if status := stores[0].Health(); !status.Healthy || status.Sources != count {
		t.Fatalf("concurrent updates were lost: %+v", status)
	}
}

func TestStoreProcessesPreserveConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const processes = 2
	var commands []*exec.Cmd
	var inputs []*os.File
	var output [processes]bytes.Buffer
	for i := range processes {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestStoreConcurrentProcessWriter$")
		cmd.Env = append(os.Environ(), "RSCH_TEST_RESEARCH_ROOT="+root, fmt.Sprintf("RSCH_TEST_RESEARCH_WRITER=%d", i))
		cmd.Stdin = reader
		cmd.Stderr = &output[i]
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = reader.Close()
		t.Cleanup(func() { _ = writer.Close(); _ = stdout.Close(); _ = cmd.Process.Kill() })
		ready, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil || ready != "ready\n" {
			t.Fatalf("writer did not become ready: output=%q err=%v", ready, err)
		}
		commands = append(commands, cmd)
		inputs = append(inputs, writer)
	}
	for _, writer := range inputs {
		if _, err := writer.Write([]byte("start\n")); err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("writer failed: %v: %s", err, output[i].String())
		}
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if status := store.Health(); !status.Healthy || status.Sources != processes*12 {
		t.Fatalf("cross-process updates were lost: %+v", status)
	}
}

func TestStoreConcurrentProcessWriter(t *testing.T) {
	root := os.Getenv("RSCH_TEST_RESEARCH_ROOT")
	if root == "" {
		return
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		uri := fmt.Sprintf("https://example.com/writer-%s/%d", os.Getenv("RSCH_TEST_RESEARCH_WRITER"), i)
		if _, err := store.AddSource(Source{URI: uri}); err != nil {
			t.Fatal(err)
		}
	}
}
