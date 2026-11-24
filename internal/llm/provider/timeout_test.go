package provider

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type blockingProvider struct{}

func (blockingProvider) Model() models.Model { return models.Model{ID: "blocking"} }
func (blockingProvider) SendMessages(ctx context.Context, _ []message.Message, _ []tools.BaseTool) (*ProviderResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (blockingProvider) StreamResponse(ctx context.Context, _ []message.Message, _ []tools.BaseTool) <-chan ProviderEvent {
	output := make(chan ProviderEvent)
	go func() { <-ctx.Done(); close(output) }()
	return output
}

func TestWithTimeoutBoundsRequest(t *testing.T) {
	started := time.Now()
	_, err := WithTimeout(blockingProvider{}, 30*time.Millisecond).SendMessages(context.Background(), nil, nil)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("expected bounded timeout, err=%v duration=%s", err, time.Since(started))
	}
}

func TestWithTimeoutReportsDeadlineWhenSourceCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := callbackStreamProvider{stream: func(ctx context.Context) <-chan ProviderEvent {
			<-ctx.Done()
			output := make(chan ProviderEvent)
			close(output)
			return output
		}}
		for range 20 {
			events := collectProviderEvents(WithTimeout(inner, time.Second).StreamResponse(context.Background(), nil, nil))
			if len(events) != 1 || events[0].Type != EventError || !errors.Is(events[0].Error, context.DeadlineExceeded) {
				t.Fatalf("source closure hid request deadline: %+v", events)
			}
		}
	})
}

func TestWithTimeoutExpiresWhileConsumerIsNotReading(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := callbackStreamProvider{stream: func(ctx context.Context) <-chan ProviderEvent {
			output := make(chan ProviderEvent, 1)
			output <- ProviderEvent{Type: EventContentDelta, Content: "pending"}
			go func() {
				<-ctx.Done()
				close(output)
			}()
			return output
		}}
		output := WithTimeout(inner, time.Second).StreamResponse(context.Background(), nil, nil)
		time.Sleep(2 * time.Second)
		events := collectProviderEvents(output)
		if len(events) != 1 || events[0].Type != EventError || !errors.Is(events[0].Error, context.DeadlineExceeded) {
			t.Fatalf("request kept pending output beyond its deadline: %+v", events)
		}
	})
}

func TestWithTimeoutPreservesCompletedStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := fallbackTestProvider{events: []ProviderEvent{{Type: EventComplete, Response: &ProviderResponse{Content: "complete"}}}}
		output := WithTimeout(inner, time.Second).StreamResponse(context.Background(), nil, nil)
		time.Sleep(2 * time.Second)
		events := collectProviderEvents(output)
		if len(events) != 1 || events[0].Type != EventComplete || events[0].Response == nil || events[0].Response.Content != "complete" {
			t.Fatalf("completed response was replaced by a timeout: %+v", events)
		}
	})
}

func TestWithTimeoutParentCancellationClosesWithoutConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := callbackStreamProvider{stream: func(ctx context.Context) <-chan ProviderEvent {
			output := make(chan ProviderEvent, 1)
			output <- ProviderEvent{Type: EventContentDelta, Content: "pending"}
			go func() {
				<-ctx.Done()
				close(output)
			}()
			return output
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		output := WithTimeout(inner, time.Hour).StreamResponse(ctx, nil, nil)
		synctest.Wait()
		cancel()
		synctest.Wait()
		assertCanceledStreamClosed(t, output)
	})
}
