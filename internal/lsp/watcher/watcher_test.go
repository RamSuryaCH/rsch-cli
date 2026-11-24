package watcher

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

func TestWatchWorkspaceReturnsWhenWatcherCreationFails(t *testing.T) {
	watcher := NewWorkspaceWatcher(nil)
	called := false
	watcher.newWatcher = func() (*fsnotify.Watcher, error) {
		called = true
		return nil, errors.New("file watcher limit reached")
	}
	defer func() {
		if value := recover(); value != nil {
			t.Errorf("watcher creation failure panicked: %v", value)
		}
	}()
	watcher.WatchWorkspace(context.Background(), t.TempDir())
	if !called {
		t.Fatal("watcher was not constructed")
	}
}

func TestPathWatchedCombinesMatchingRegistrationKinds(t *testing.T) {
	create := protocol.WatchCreate
	change := protocol.WatchChange
	remove := protocol.WatchDelete
	registration := func(pattern string, kind *protocol.WatchKind) protocol.FileSystemWatcher {
		return protocol.FileSystemWatcher{GlobPattern: protocol.GlobPattern{Value: pattern}, Kind: kind}
	}
	for _, test := range []struct {
		name          string
		registrations []protocol.FileSystemWatcher
		wantWatched   bool
		wantKind      protocol.WatchKind
	}{
		{"combine", []protocol.FileSystemWatcher{registration("**/*.go", &create), registration("**/*.go", &change)}, true, create | change},
		{"reverse order", []protocol.FileSystemWatcher{registration("**/*.go", &change), registration("**/*.go", &create)}, true, create | change},
		{"ignore nonmatching", []protocol.FileSystemWatcher{registration("**/*.go", &create), registration("**/*.ts", &remove)}, true, create},
		{"default kind", []protocol.FileSystemWatcher{registration("**/*.go", &create), registration("**/*.go", nil)}, true, create | change | remove},
		{"no match", []protocol.FileSystemWatcher{registration("**/*.ts", &create)}, false, 0},
		{"no registrations", nil, true, create | change | remove},
	} {
		t.Run(test.name, func(t *testing.T) {
			watcher := NewWorkspaceWatcher(nil)
			watcher.registrations = test.registrations
			watched, kind := watcher.isPathWatched(filepath.Join(t.TempDir(), "main.go"))
			if watched != test.wantWatched || kind != test.wantKind {
				t.Fatalf("watched=%v, kind=%d; want watched=%v, kind=%d", watched, kind, test.wantWatched, test.wantKind)
			}
		})
	}
}
