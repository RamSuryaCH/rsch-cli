package events

import "testing"

func TestBusPublishesVersionedEvents(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	if !bus.Publish(New(GoalCreated, map[string]any{"id": "g1"})) {
		t.Fatal("publish failed")
	}
	event := <-ch
	if event.Version != Version || event.Type != GoalCreated || event.ID == "" {
		t.Fatalf("invalid event: %+v", event)
	}
}
