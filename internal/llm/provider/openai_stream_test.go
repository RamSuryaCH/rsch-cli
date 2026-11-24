package provider

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/openai/openai-go"
)

type fakeOpenAIChatCompletionStream struct {
	chunks  []openai.ChatCompletionChunk
	current int
	err     error
}

func (s *fakeOpenAIChatCompletionStream) Next() bool {
	if s.current >= len(s.chunks) {
		return false
	}
	s.current++
	return true
}

func (s *fakeOpenAIChatCompletionStream) Current() openai.ChatCompletionChunk {
	return s.chunks[s.current-1]
}

func (s *fakeOpenAIChatCompletionStream) Err() error { return s.err }

func (s *fakeOpenAIChatCompletionStream) Close() error { return nil }

func TestOpenAIStreamHandlesChunkWithEmptyChoices(t *testing.T) {
	client := &openaiClient{}
	events := collectProviderEvents(client.streamFrom(context.Background(), func() openAIChatCompletionStream {
		return &fakeOpenAIChatCompletionStream{chunks: []openai.ChatCompletionChunk{{ID: "usage-only"}}}
	}))

	if len(events) != 1 || events[0].Type != EventError || events[0].Error == nil {
		t.Fatalf("expected one terminal error, got %+v", events)
	}
}

func TestOpenAIStreamEndsWithErrorWithoutChunk(t *testing.T) {
	client := &openaiClient{}
	events := collectProviderEvents(client.streamFrom(context.Background(), func() openAIChatCompletionStream {
		return &fakeOpenAIChatCompletionStream{}
	}))

	if len(events) != 1 || events[0].Type != EventError || events[0].Error == nil {
		t.Fatalf("expected one terminal error, got %+v", events)
	}
}

func TestOpenAIStreamRetriesRateLimitBeforeEmission(t *testing.T) {
	attempts := 0
	client := &openaiClient{retryWait: func(context.Context, time.Duration) error { return nil }}
	events := collectProviderEvents(client.streamFrom(context.Background(), func() openAIChatCompletionStream {
		attempts++
		if attempts == 1 {
			return &fakeOpenAIChatCompletionStream{err: openAIRateLimitError(t)}
		}
		return &fakeOpenAIChatCompletionStream{chunks: []openai.ChatCompletionChunk{openAITextChunk("complete")}}
	}))

	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
	if len(events) != 2 || events[0].Type != EventContentDelta || events[0].Content != "complete" || events[1].Type != EventComplete {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestOpenAIStreamDoesNotRetryAfterPartialOutput(t *testing.T) {
	attempts := 0
	client := &openaiClient{retryWait: func(context.Context, time.Duration) error { return nil }}
	rateLimitErr := openAIRateLimitError(t)
	events := collectProviderEvents(client.streamFrom(context.Background(), func() openAIChatCompletionStream {
		attempts++
		return &fakeOpenAIChatCompletionStream{
			chunks: []openai.ChatCompletionChunk{openAITextChunk("partial")},
			err:    rateLimitErr,
		}
	}))

	if attempts != 1 || len(events) != 2 || events[0].Type != EventContentDelta || events[0].Content != "partial" || events[1].Type != EventError {
		t.Fatalf("partial output must fail rather than be duplicated by retry: attempts=%d events=%+v", attempts, events)
	}
	if events[1].Error != rateLimitErr {
		t.Fatalf("terminal error = %v, want original stream error %v", events[1].Error, rateLimitErr)
	}
}

func openAITextChunk(content string) openai.ChatCompletionChunk {
	return openai.ChatCompletionChunk{
		ID: "chunk-1",
		Choices: []openai.ChatCompletionChunkChoice{{
			Delta:        openai.ChatCompletionChunkChoiceDelta{Content: content},
			FinishReason: "stop",
			Index:        0,
		}},
	}
}

func openAIRateLimitError(t *testing.T) error {
	t.Helper()
	requestURL, err := url.Parse("https://api.openai.com/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	return &openai.Error{
		StatusCode: http.StatusTooManyRequests,
		Request:    &http.Request{Method: http.MethodPost, URL: requestURL},
		Response: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
		},
	}
}
