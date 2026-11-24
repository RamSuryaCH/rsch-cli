package cmd

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

func TestTUIEventQueuePrioritizesReliableAndCoalescesUpdates(t *testing.T) {
	queue := newTUIEventQueue(4)
	first := pubsub.Event[message.Message]{
		Type:    pubsub.UpdatedEvent,
		Payload: message.Message{ID: "message-1", State: message.StatePending, Sequence: 1},
	}
	latest := first
	latest.Payload.Sequence = 2
	permissionEvent := pubsub.Event[permission.PermissionRequest]{
		Type:    pubsub.CreatedEvent,
		Payload: permission.PermissionRequest{ID: "permission-1"},
	}

	enqueueMessageEvent(queue, first)
	enqueueMessageEvent(queue, latest)
	queue.enqueueReliable(permissionEvent)

	got, ok := queue.dequeue(true)
	if !ok {
		t.Fatal("reliable event was not queued")
	}
	permissionMsg, ok := got.(pubsub.Event[permission.PermissionRequest])
	if !ok || permissionMsg.Payload.ID != "permission-1" {
		t.Fatalf("first event = %#v; want permission event", got)
	}

	got, ok = queue.dequeue(true)
	if !ok {
		t.Fatal("coalesced message update was not queued")
	}
	messageMsg, ok := got.(pubsub.Event[message.Message])
	if !ok || messageMsg.Payload.Sequence != 2 {
		t.Fatalf("coalesced event = %#v; want latest sequence 2", got)
	}
	if _, ok := queue.dequeue(true); ok {
		t.Fatal("older message update was retained instead of coalesced")
	}
}

func TestTUIEventQueueKeepsTerminalMessageAndDiscardsStaleProgress(t *testing.T) {
	queue := newTUIEventQueue(4)
	pending := pubsub.Event[message.Message]{
		Type:    pubsub.UpdatedEvent,
		Payload: message.Message{ID: "message-1", State: message.StatePending},
	}
	terminal := pending
	terminal.Payload.State = message.StateComplete

	enqueueMessageEvent(queue, pending)
	enqueueMessageEvent(queue, terminal)

	got, ok := queue.dequeue(true)
	if !ok {
		t.Fatal("terminal message event was not queued")
	}
	messageMsg, ok := got.(pubsub.Event[message.Message])
	if !ok || messageMsg.Payload.State != message.StateComplete {
		t.Fatalf("event = %#v; want terminal message", got)
	}
	if _, ok := queue.dequeue(true); ok {
		t.Fatal("stale pending update remained after terminal message")
	}
}

func TestTUIEventQueueBoundsDistinctStreamingUpdates(t *testing.T) {
	queue := newTUIEventQueue(2)
	queue.enqueueCoalesced("a", "a-old")
	queue.enqueueCoalesced("b", "b")
	queue.enqueueCoalesced("a", "a-latest")
	queue.enqueueCoalesced("c", "c")

	got, ok := queue.dequeue(true)
	if !ok || got != "a-latest" {
		t.Fatalf("first retained update = %#v, %t; want a-latest", got, ok)
	}
	got, ok = queue.dequeue(true)
	if !ok || got != "c" {
		t.Fatalf("second retained update = %#v, %t; want c", got, ok)
	}
	if _, ok := queue.dequeue(true); ok {
		t.Fatal("coalesced queue exceeded its configured bound")
	}
}

func TestTUIEventQueueReservesOutputCapacityForReliableEvents(t *testing.T) {
	queue := newTUIEventQueue(8)
	output := make(chan tea.Msg, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		queue.run(ctx, output)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("event dispatcher did not stop after cancellation")
		}
	}()

	for _, key := range []string{"a", "b", "c", "d"} {
		queue.enqueueCoalesced(key, key)
	}
	waitForChannelLength(t, output, 3)

	terminal := pubsub.Event[agent.AgentEvent]{
		Type:    pubsub.CreatedEvent,
		Payload: agent.AgentEvent{Type: agent.AgentEventTypeResponse, Done: true},
	}
	enqueueAgentEvent(queue, terminal)
	waitForChannelLength(t, output, 4)

	for i := 0; i < 3; i++ {
		<-output
	}
	got := <-output
	agentMsg, ok := got.(pubsub.Event[agent.AgentEvent])
	if !ok || !agentMsg.Payload.Done {
		t.Fatalf("reserved slot event = %#v; want terminal agent event", got)
	}
}

func waitForChannelLength(t *testing.T, ch <-chan tea.Msg, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if len(ch) == want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("channel length = %d; want %d", len(ch), want)
		}
	}
}
