package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

type fakeAnthropicMessageStream struct {
	events []anthropic.MessageStreamEventUnion
	err    error
	index  int
	closed bool
}

func (s *fakeAnthropicMessageStream) Next() bool {
	if s.index >= len(s.events) {
		return false
	}
	s.index++
	return true
}

func (s *fakeAnthropicMessageStream) Current() anthropic.MessageStreamEventUnion {
	return s.events[s.index-1]
}

func (s *fakeAnthropicMessageStream) Err() error { return s.err }

func (s *fakeAnthropicMessageStream) Close() error {
	s.closed = true
	return nil
}

func TestAnthropicStreamCancellationClosesWithoutConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream := &fakeAnthropicMessageStream{events: anthropicTextEvents("text")}
		client := &anthropicClient{}
		output := client.streamFrom(ctx, func() anthropicMessageStream { return stream })
		synctest.Wait()
		cancel()
		synctest.Wait()
		assertCanceledStreamClosed(t, output)
		if !stream.closed {
			t.Error("canceled stream did not release its HTTP response")
		}
	})
}

func TestAnthropicStreamRetriesRateLimitBeforeEmission(t *testing.T) {
	attempts := 0
	client := &anthropicClient{retryWait: func(context.Context, time.Duration) error { return nil }}
	newStream := func() anthropicMessageStream {
		attempts++
		if attempts == 1 {
			return &fakeAnthropicMessageStream{err: anthropicRateLimitError()}
		}
		return &fakeAnthropicMessageStream{events: anthropicTextEvents("complete")}
	}

	events := collectProviderEvents(client.streamFrom(context.Background(), newStream))
	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
	if len(events) != 4 || events[0].Type != EventContentStart || events[1].Content != "complete" || events[2].Type != EventContentStop || events[3].Type != EventComplete {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestAnthropicStreamDoesNotRetryAfterPartialOutput(t *testing.T) {
	attempts := 0
	client := &anthropicClient{retryWait: func(context.Context, time.Duration) error { return nil }}
	newStream := func() anthropicMessageStream {
		attempts++
		return &fakeAnthropicMessageStream{
			events: anthropicTextEvents("partial")[:3],
			err:    anthropicRateLimitError(),
		}
	}

	events := collectProviderEvents(client.streamFrom(context.Background(), newStream))
	if attempts != 1 || len(events) != 3 || events[0].Type != EventContentStart || events[1].Content != "partial" || events[2].Type != EventError {
		t.Fatalf("partial output must fail rather than be duplicated by retry: attempts=%d events=%+v", attempts, events)
	}
}

func TestAnthropicStreamToolInputDeltasAreDecodedFragments(t *testing.T) {
	fragments := []string{`{"path":`, `"main.go"}`}
	rawEvents := []string{
		`{"type":"message_start","message":{}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"inspect","input":{}}}`,
	}
	for _, fragment := range fragments {
		rawEvents = append(rawEvents, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+mustJSONQuote(fragment)+`}}`)
	}
	rawEvents = append(rawEvents,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`{"type":"message_stop"}`,
	)
	stream := &fakeAnthropicMessageStream{}
	for _, raw := range rawEvents {
		var event anthropic.MessageStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		stream.events = append(stream.events, event)
	}
	client := &anthropicClient{}
	events := collectProviderEvents(client.streamFrom(context.Background(), func() anthropicMessageStream { return stream }))
	input := ""
	for _, event := range events {
		if event.Type == EventToolUseDelta {
			input += event.ToolCall.Input
		}
	}
	if input != `{"path":"main.go"}` || !json.Valid([]byte(input)) {
		t.Fatalf("streamed input was JSON-encoded twice: %q", input)
	}
	last := events[len(events)-1]
	if last.Type != EventComplete || last.Response == nil || len(last.Response.ToolCalls) != 1 || last.Response.ToolCalls[0].Input != input {
		t.Fatalf("streamed and completed tool input differ: %+v", last)
	}
}

func anthropicTextEvents(text string) []anthropic.MessageStreamEventUnion {
	rawEvents := []string{
		`{"type":"message_start","message":{}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + mustJSONQuote(text) + `}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_stop"}`,
	}
	events := make([]anthropic.MessageStreamEventUnion, len(rawEvents))
	for i, raw := range rawEvents {
		if err := json.Unmarshal([]byte(raw), &events[i]); err != nil {
			panic(err)
		}
	}
	return events
}

func mustJSONQuote(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func anthropicRateLimitError() error {
	return &anthropic.Error{
		StatusCode: http.StatusTooManyRequests,
		Response:   &http.Response{Header: http.Header{"Retry-After": []string{"0"}}},
	}
}
