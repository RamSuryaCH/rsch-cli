package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/openai/openai-go"
)

type fakeCopilotCompletionStream struct {
	chunks []openai.ChatCompletionChunk
	err    error
	next   int
}

func (s *fakeCopilotCompletionStream) Next() bool {
	if s.next >= len(s.chunks) {
		return false
	}
	s.next++
	return true
}

func (s *fakeCopilotCompletionStream) Current() openai.ChatCompletionChunk {
	return s.chunks[s.next-1]
}

func (s *fakeCopilotCompletionStream) Err() error {
	return s.err
}

func (s *fakeCopilotCompletionStream) Close() error { return nil }

func collectCopilotProviderEvents(stream <-chan ProviderEvent) []ProviderEvent {
	var events []ProviderEvent
	for event := range stream {
		events = append(events, event)
	}
	return events
}

func TestCopilotStreamHandlesChunkWithoutChoices(t *testing.T) {
	client := &copilotClient{}
	events := collectCopilotProviderEvents(client.streamFrom(context.Background(), func() copilotCompletionStream {
		return &fakeCopilotCompletionStream{chunks: []openai.ChatCompletionChunk{{ID: "usage-only"}}}
	}, false, "", 0))

	if len(events) != 1 || events[0].Type != EventError || events[0].Error == nil {
		t.Fatalf("expected one terminal error, got %+v", events)
	}
}

func TestCopilotStreamRetriesBeforeOutput(t *testing.T) {
	attempts := 0
	retryErr := copilotAPIError(http.StatusInternalServerError)
	retryErr.Response.Header.Set("Retry-After", "0")
	client := &copilotClient{}
	events := collectCopilotProviderEvents(client.streamFrom(context.Background(), func() copilotCompletionStream {
		attempts++
		if attempts == 1 {
			return &fakeCopilotCompletionStream{err: retryErr}
		}
		return &fakeCopilotCompletionStream{chunks: []openai.ChatCompletionChunk{{
			ID: "complete",
			Choices: []openai.ChatCompletionChunkChoice{{
				Delta:        openai.ChatCompletionChunkChoiceDelta{Content: "complete"},
				FinishReason: "stop",
			}},
		}}}
	}, false, "", 0))

	if attempts != 2 {
		t.Fatalf("stream attempts = %d, want 2", attempts)
	}
	if len(events) != 2 || events[0].Type != EventContentDelta || events[0].Content != "complete" || events[1].Type != EventComplete {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestCopilotStreamDoesNotRetryAfterPartialOutput(t *testing.T) {
	attempts := 0
	streamErr := copilotAPIError(http.StatusInternalServerError)
	streamErr.Response.Header.Set("Retry-After", "0")
	client := &copilotClient{}
	events := collectCopilotProviderEvents(client.streamFrom(context.Background(), func() copilotCompletionStream {
		attempts++
		return &fakeCopilotCompletionStream{
			chunks: []openai.ChatCompletionChunk{{
				ID: "partial",
				Choices: []openai.ChatCompletionChunkChoice{{
					Delta: openai.ChatCompletionChunkChoiceDelta{Content: "partial"},
				}},
			}},
			err: streamErr,
		}
	}, false, "", 0))

	if attempts != 1 {
		t.Fatalf("stream attempts = %d, want 1", attempts)
	}
	if len(events) != 2 || events[0].Type != EventContentDelta || events[0].Content != "partial" || events[1].Type != EventError {
		t.Fatalf("unexpected events: %+v", events)
	}
	if !errors.Is(events[1].Error, streamErr) {
		t.Fatalf("stream error = %v, want %v", events[1].Error, streamErr)
	}
}
