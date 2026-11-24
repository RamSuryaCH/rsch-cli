package pubsub

import (
	"context"
	"sync"
)

const bufferSize = 64

// Stats describes delivery observed by a broker. Delivered and Dropped count
// subscriber deliveries, so one published event can contribute more than one
// delivery when multiple subscribers are active.
type Stats struct {
	Subscribers int
	Published   uint64
	Delivered   uint64
	Dropped     uint64
}

type Broker[T any] struct {
	subs              map[chan Event[T]]struct{}
	mu                sync.RWMutex
	done              chan struct{}
	subCount          int
	channelBufferSize int
	stats             Stats
}

func NewBroker[T any]() *Broker[T] {
	return NewBrokerWithOptions[T](bufferSize, 1000)
}

func NewBrokerWithOptions[T any](channelBufferSize, maxEvents int) *Broker[T] {
	if channelBufferSize <= 0 {
		channelBufferSize = bufferSize
	}
	// Keep the existing option as an upper bound for a subscriber queue. This
	// preserves the broker's bounded-delivery contract even when callers choose
	// a larger channel buffer.
	if maxEvents > 0 && channelBufferSize > maxEvents {
		channelBufferSize = maxEvents
	}
	b := &Broker[T]{
		subs:              make(map[chan Event[T]]struct{}),
		done:              make(chan struct{}),
		channelBufferSize: channelBufferSize,
	}
	return b
}

func (b *Broker[T]) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.done: // Already closed
		return
	default:
		close(b.done)
	}

	for ch := range b.subs {
		delete(b.subs, ch)
		close(ch)
	}

	b.subCount = 0
}

func (b *Broker[T]) Subscribe(ctx context.Context) <-chan Event[T] {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done:
		return closedChannel[T]()
	default:
	}
	if ctx == nil || ctx.Err() != nil {
		return closedChannel[T]()
	}

	sub := make(chan Event[T], b.channelBufferSize)
	b.subs[sub] = struct{}{}
	b.subCount++

	go func() {
		select {
		case <-ctx.Done():
			b.remove(sub)
		case <-b.done:
			return
		}
	}()

	return sub
}

func (b *Broker[T]) remove(sub chan Event[T]) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[sub]; !ok {
		return
	}
	delete(b.subs, sub)
	close(sub)
	b.subCount--
}

func closedChannel[T any]() <-chan Event[T] {
	ch := make(chan Event[T])
	close(ch)
	return ch
}

func (b *Broker[T]) GetSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.subCount
}

// Stats returns a point-in-time delivery snapshot. A dropped delivery means
// the subscriber queue was full; Publish remains non-blocking by design.
func (b *Broker[T]) Stats() Stats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	stats := b.stats
	stats.Subscribers = b.subCount
	return stats
}

func (b *Broker[T]) Publish(t EventType, payload T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.done:
		return
	default:
	}
	b.stats.Published++

	event := Event[T]{Type: t, Payload: payload}

	for sub := range b.subs {
		select {
		case sub <- event:
			b.stats.Delivered++
		default:
			b.stats.Dropped++
		}
	}
}
