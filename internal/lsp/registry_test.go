package lsp

import (
	"sync"
	"testing"
)

func TestClientRegistrySnapshotsConcurrentMutation(t *testing.T) {
	registry := NewClientRegistry()
	clients := []*Client{{}, {}}
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 200; index++ {
				name := string(rune('a' + (worker+index)%8))
				registry.Set(name, clients[index%len(clients)])
				_ = registry.Snapshot()
				if index%3 == 0 {
					registry.Delete(name)
				}
			}
		}()
	}
	wait.Wait()

	snapshot := registry.Snapshot()
	registry.Set("later", &Client{})
	if _, exists := snapshot["later"]; exists {
		t.Fatal("registry mutated a previously returned snapshot")
	}
}

func TestStaticClientProviderOwnsInputSnapshot(t *testing.T) {
	input := map[string]*Client{"original": {}}
	provider := NewStaticClientProvider(input)
	delete(input, "original")
	if _, exists := provider.Snapshot()["original"]; !exists {
		t.Fatal("static provider changed after caller mutated its input map")
	}
}
