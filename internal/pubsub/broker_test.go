package pubsub

import (
	"context"
	"testing"
)

func TestBrokerUsesConfiguredBoundedBufferAndReportsDrops(t *testing.T) {
	broker := NewBrokerWithOptions[int](2, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := broker.Subscribe(ctx)

	broker.Publish(UpdatedEvent, 1)
	broker.Publish(UpdatedEvent, 2)
	broker.Publish(UpdatedEvent, 3)

	stats := broker.Stats()
	if stats.Subscribers != 1 || stats.Published != 3 || stats.Delivered != 2 || stats.Dropped != 1 {
		t.Fatalf("unexpected broker stats: %+v", stats)
	}
	if first := <-stream; first.Payload != 1 {
		t.Fatalf("first buffered event = %v, want 1", first.Payload)
	}
}

func TestBrokerHonorsMaxEventsAsQueueBound(t *testing.T) {
	broker := NewBrokerWithOptions[int](8, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := broker.Subscribe(ctx)

	broker.Publish(CreatedEvent, 1)
	broker.Publish(CreatedEvent, 2)

	stats := broker.Stats()
	if stats.Delivered != 1 || stats.Dropped != 1 {
		t.Fatalf("maxEvents did not bound queue: %+v", stats)
	}
	if value := (<-stream).Payload; value != 1 {
		t.Fatalf("buffered event = %v, want 1", value)
	}
}

func TestBrokerDoesNotRegisterCanceledSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	broker := NewBroker[int]()
	stream := broker.Subscribe(ctx)
	if broker.Stats().Subscribers != 0 {
		t.Fatal("canceled subscriber was registered")
	}
	if _, ok := <-stream; ok {
		t.Fatal("canceled subscriber channel was not closed")
	}
}

func TestBrokerShutdownIsIdempotentAndClosesSubscribers(t *testing.T) {
	broker := NewBroker[int]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := broker.Subscribe(ctx)

	broker.Shutdown()
	broker.Shutdown()

	if _, ok := <-stream; ok {
		t.Fatal("subscriber channel remained open after shutdown")
	}
	if stats := broker.Stats(); stats.Subscribers != 0 {
		t.Fatalf("subscriber count after shutdown = %d, want 0", stats.Subscribers)
	}
}
