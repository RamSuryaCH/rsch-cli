package events

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type recordingSink struct {
	items []Event
	err   error
}

func (s *recordingSink) Append(_ context.Context, event Event) error {
	s.items = append(s.items, event)
	return s.err
}

func TestBusPersistsBeforeBroadcast(t *testing.T) {
	bus := NewBus()
	sink := &recordingSink{}
	bus.AddSink(sink)
	stream, cancel := bus.Subscribe()
	defer cancel()
	event := New(ToolBefore, map[string]any{"requestId": "r1"})
	if !bus.PublishContext(context.Background(), event) {
		t.Fatal("expected durable publish")
	}
	if len(sink.items) != 1 || sink.items[0].ID != event.ID {
		t.Fatalf("sink did not receive event: %+v", sink.items)
	}
	if received := <-stream; received.ID != event.ID {
		t.Fatalf("subscriber received %q, want %q", received.ID, event.ID)
	}
}

func TestBusReportsSinkFailureWithoutDroppingSubscribers(t *testing.T) {
	bus := NewBus()
	bus.AddSink(&recordingSink{err: errors.New("storage unavailable")})
	stream, cancel := bus.Subscribe()
	defer cancel()
	event := New(ToolAfter, nil)
	if bus.Publish(event) {
		t.Fatal("expected sink failure to be observable")
	}
	if received := <-stream; received.ID != event.ID {
		t.Fatalf("subscriber did not receive event after sink failure")
	}
}

func TestNewRedactsCredentialMaterial(t *testing.T) {
	event := New(ToolFailed, map[string]any{
		"error":  "Authorization: Bearer super-secret-token",
		"nested": map[string]any{"apiKey": "do-not-persist-this"},
	})
	data, err := event.JSON()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "super-secret-token") || strings.Contains(text, "do-not-persist-this") {
		t.Fatalf("event contains credential material: %s", text)
	}
}

type blockingSequenceSink struct {
	sequence atomic.Int64
	first    chan struct{}
	release  chan struct{}
}

func (s *blockingSequenceSink) Append(ctx context.Context, event Event) error {
	return s.AppendEvent(ctx, &event)
}

func (s *blockingSequenceSink) AppendEvent(_ context.Context, event *Event) error {
	event.Sequence = s.sequence.Add(1)
	if event.Sequence == 1 {
		close(s.first)
		<-s.release
	}
	return nil
}

func TestConcurrentPublishesPreserveDurableSequenceOrder(t *testing.T) {
	bus := NewBus()
	t.Cleanup(bus.Close)
	sink := &blockingSequenceSink{first: make(chan struct{}), release: make(chan struct{})}
	bus.AddSink(sink)
	stream, cancel := bus.Subscribe()
	defer cancel()
	firstDone := make(chan bool, 1)
	go func() { firstDone <- bus.Publish(New(TaskStarted, nil)) }()
	<-sink.first
	secondDone := make(chan bool, 1)
	go func() { secondDone <- bus.Publish(New(TaskCompleted, nil)) }()

	// The second event must not overtake a durable write that has not yet
	// reached subscribers, even if that write's sink takes longer to return.
	select {
	case <-secondDone:
		close(sink.release)
		<-firstDone
		t.Fatal("second publish overtook the first durable event")
	case <-time.After(100 * time.Millisecond):
		close(sink.release)
	}
	if !<-firstDone || !<-secondDone {
		t.Fatal("publish failed")
	}
	for want := int64(1); want <= 2; want++ {
		if event := <-stream; event.Sequence != want {
			t.Fatalf("received sequence %d, want %d", event.Sequence, want)
		}
	}
}

func TestNewRedactsTypedPayloadsAndSeparatedCredentialKeys(t *testing.T) {
	type scopeKind string
	const scopeFile scopeKind = "file"
	type credentials struct {
		AccessToken string `json:"access_token"`
		Label       string `json:"label"`
	}
	payload := map[string]any{
		"api_key":       "plain-api-value",
		"private-key":   "plain-private-value",
		"refresh.token": "plain-refresh-value",
		"headers":       map[string]string{"Authorization": "plain-header-value", "Accept": "application/json"},
		"messages":      []string{"Authorization: Bearer plain-bearer-value", "visible-message"},
		"accounts":      []credentials{{AccessToken: "plain-access-value", Label: "visible-label"}},
		"nested":        []map[string]any{{"password": "plain-password-value"}},
		"scopeKind":     scopeFile,
		"counts":        map[string]int64{"count": 9007199254740993},
	}
	event := New(ToolAfter, payload)
	data, err := event.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "plain-") {
		t.Fatalf("event leaked credential material: %s", data)
	}
	for _, visible := range []string{"application/json", "visible-message", "visible-label", `"count":9007199254740993`} {
		if !strings.Contains(string(data), visible) {
			t.Fatalf("event lost nonsensitive value %q: %s", visible, data)
		}
	}
	if event.Data["scopeKind"] != scopeFile {
		t.Fatal("redaction changed a nonsensitive scalar's type")
	}
	if payload["headers"].(map[string]string)["Authorization"] != "plain-header-value" {
		t.Fatal("redaction mutated the caller's payload")
	}
}

func TestPublishRedactsPayloadAddedAfterConstruction(t *testing.T) {
	bus := NewBus()
	defer bus.Close()
	sink := &recordingSink{}
	bus.AddSink(sink)
	stream, cancel := bus.Subscribe()
	defer cancel()
	event := New(ToolAfter, nil)
	event.Data = map[string]any{"access_token": "plain-token", "result": "visible"}
	if !bus.Publish(event) {
		t.Fatal("publish failed")
	}
	live := <-stream
	if live.Data["access_token"] != "[REDACTED]" || sink.items[0].Data["access_token"] != "[REDACTED]" {
		t.Fatal("published event exposed a credential added after construction")
	}
	if event.Data["access_token"] != "plain-token" || live.Data["result"] != "visible" {
		t.Fatal("redaction mutated caller data or removed nonsensitive data")
	}
}

func TestPublishTransactionDoesNotDeliverFailedMutation(t *testing.T) {
	bus := NewBus()
	defer bus.Close()
	sink := &recordingSink{}
	bus.AddSink(sink)
	stream, cancel := bus.Subscribe()
	defer cancel()
	failure := errors.New("transaction failed")
	event, delivered, err := bus.PublishTransaction(context.Background(), nil, func(context.Context) (Event, error) {
		return New(TaskCreated, nil), failure
	})
	if !errors.Is(err, failure) || delivered || event.ID != "" || len(sink.items) != 0 {
		t.Fatalf("failed mutation escaped: event=%+v delivered=%t err=%v sinks=%d", event, delivered, err, len(sink.items))
	}
	select {
	case <-stream:
		t.Fatal("failed mutation was broadcast")
	default:
	}
}

func TestPublishTransactionSeparatesCommitFromDeliveryFailures(t *testing.T) {
	bus := NewBus()
	defer bus.Close()
	bus.AddSink(&recordingSink{err: errors.New("observer unavailable")})
	stream, cancelSubscription := bus.Subscribe()
	defer cancelSubscription()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	event, delivered, err := bus.PublishTransaction(ctx, nil, func(context.Context) (Event, error) {
		// Cancellation after commit must not make the committed operation look
		// like a failed mutation or suppress its live notification.
		cancel()
		return New(TaskCreated, nil), nil
	})
	if err != nil || delivered || event.ID == "" {
		t.Fatalf("postcommit failure reported as mutation failure: event=%+v delivered=%t err=%v", event, delivered, err)
	}
	if live := <-stream; live.ID != event.ID {
		t.Fatalf("committed event was not delivered: %+v", live)
	}
}

func TestPublishTransactionRejectsCanceledOrClosedBeforeMutation(t *testing.T) {
	for _, closed := range []bool{false, true} {
		bus := NewBus()
		ctx, cancel := context.WithCancel(context.Background())
		if closed {
			bus.Close()
		} else {
			cancel()
		}
		called := false
		_, delivered, err := bus.PublishTransaction(ctx, nil, func(context.Context) (Event, error) {
			called = true
			return New(TaskCreated, nil), nil
		})
		cancel()
		bus.Close()
		if called || delivered || err == nil {
			t.Fatalf("mutation ran after close/cancellation: closed=%t called=%t delivered=%t err=%v", closed, called, delivered, err)
		}
	}
}
