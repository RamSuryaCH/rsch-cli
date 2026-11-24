package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

type observedOpenAIStream struct {
	fakeOpenAIChatCompletionStream
	read   chan struct{}
	closed chan struct{}
	closes int
}

func (s *observedOpenAIStream) Current() openai.ChatCompletionChunk {
	if s.read != nil {
		close(s.read)
		s.read = nil
	}
	return s.fakeOpenAIChatCompletionStream.Current()
}

func (s *observedOpenAIStream) Close() error {
	s.closes++
	if s.closed != nil {
		close(s.closed)
		s.closed = nil
	}
	return nil
}

func compatibleStreamStarters() map[string]func(context.Context, func() *observedOpenAIStream) <-chan ProviderEvent {
	return map[string]func(context.Context, func() *observedOpenAIStream) <-chan ProviderEvent{
		"OpenAI": func(ctx context.Context, next func() *observedOpenAIStream) <-chan ProviderEvent {
			return (&openaiClient{}).streamFrom(ctx, func() openAIChatCompletionStream { return next() })
		},
		"Copilot": func(ctx context.Context, next func() *observedOpenAIStream) <-chan ProviderEvent {
			return (&copilotClient{}).streamFrom(ctx, func() copilotCompletionStream { return next() }, false, "", 0)
		},
	}
}

func TestOpenAICompatibleStreamCancellationReleasesBlockedProducer(t *testing.T) {
	for name, start := range compatibleStreamStarters() {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			read, closed := make(chan struct{}), make(chan struct{})
			stream := &observedOpenAIStream{
				fakeOpenAIChatCompletionStream: fakeOpenAIChatCompletionStream{chunks: []openai.ChatCompletionChunk{openAITextChunk("hello")}},
				read:                           read, closed: closed,
			}
			events := start(ctx, func() *observedOpenAIStream { return stream })
			select {
			case <-read:
			case <-time.After(time.Second):
				t.Fatal("producer did not read its first chunk")
			}
			// No consumer is reading events. Cancellation must release the
			// producer and its response body without needing to drain events.
			cancel()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("canceled producer did not close its response body")
			}
			select {
			case _, ok := <-events:
				if ok {
					t.Fatal("canceled producer emitted an event")
				}
			case <-time.After(time.Second):
				t.Fatal("canceled producer did not close its event channel")
			}
		})
	}
}

func TestOpenAICompatibleStreamClosesEveryAttempt(t *testing.T) {
	for name, start := range compatibleStreamStarters() {
		for _, failFirst := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/success", true: "/retry"}[failFirst], func(t *testing.T) {
				var streams []*observedOpenAIStream
				events := collectProviderEvents(start(context.Background(), func() *observedOpenAIStream {
					if len(streams) > 0 && streams[len(streams)-1].closes != 1 {
						t.Error("previous response body was not closed before retry")
					}
					stream := &observedOpenAIStream{fakeOpenAIChatCompletionStream: fakeOpenAIChatCompletionStream{
						chunks: []openai.ChatCompletionChunk{openAITextChunk("complete")},
					}}
					if failFirst && len(streams) == 0 {
						err := copilotAPIError(http.StatusTooManyRequests)
						err.Response.Header.Set("Retry-After", "0")
						stream.chunks = nil
						stream.err = err
					}
					streams = append(streams, stream)
					return stream
				}))
				if len(events) != 2 || events[1].Type != EventComplete {
					t.Fatalf("unexpected events: %+v", events)
				}
				for i, stream := range streams {
					if stream.closes != 1 {
						t.Errorf("attempt %d closed %d times, want 1", i+1, stream.closes)
					}
				}
			})
		}
	}
}

func TestOpenAICompatibleStreamClosesOnError(t *testing.T) {
	for name, start := range compatibleStreamStarters() {
		t.Run(name, func(t *testing.T) {
			stream := &observedOpenAIStream{fakeOpenAIChatCompletionStream: fakeOpenAIChatCompletionStream{err: errors.New("stream failed")}}
			events := collectProviderEvents(start(context.Background(), func() *observedOpenAIStream { return stream }))
			if len(events) != 1 || events[0].Type != EventError || stream.closes != 1 {
				t.Fatalf("events=%+v close count=%d", events, stream.closes)
			}
		})
	}
}

func TestOpenAICompatibleSendRejectsEmptyCompletionChoices(t *testing.T) {
	for _, body := range []string{`{"choices":[]}`, `{}`, `{"choices":null}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			sdkClient := openai.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0))
			opts := providerClientOptions{model: models.Model{APIModel: "test-model"}, maxTokens: 32}
			for name, client := range map[string]ProviderClient{
				"OpenAI":  &openaiClient{client: sdkClient, providerOptions: opts},
				"Copilot": &copilotClient{client: sdkClient, providerOptions: opts},
			} {
				t.Run(name, func(t *testing.T) {
					response, err := client.send(context.Background(), nil, nil)
					if response != nil || err == nil || !strings.Contains(err.Error(), "without a completion choice") {
						t.Fatalf("response=%+v error=%v, want missing-choice error", response, err)
					}
				})
			}
		})
	}
}

func TestOpenAICompatibleStreamRejectsTruncatedResponses(t *testing.T) {
	textChunk := openAITextChunk("partial text")
	textChunk.Choices[0].FinishReason = ""
	toolChunk := openai.ChatCompletionChunk{
		ID: "partial-tool",
		Choices: []openai.ChatCompletionChunkChoice{{
			Delta: openai.ChatCompletionChunkChoiceDelta{
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{
					ID: "call-1", Index: 0, Type: "function",
					Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{
						Name: "inspect", Arguments: `{"path":`,
					},
				}},
			},
		}},
	}
	for name, start := range compatibleStreamStarters() {
		for _, test := range []struct {
			name  string
			chunk openai.ChatCompletionChunk
		}{
			{name: "text", chunk: textChunk},
			{name: "tool", chunk: toolChunk},
		} {
			for _, streamErr := range []error{nil, io.EOF} {
				t.Run(fmt.Sprintf("%s/%s/%v", name, test.name, streamErr), func(t *testing.T) {
					attempts := 0
					events := collectProviderEvents(start(context.Background(), func() *observedOpenAIStream {
						attempts++
						return &observedOpenAIStream{fakeOpenAIChatCompletionStream: fakeOpenAIChatCompletionStream{
							chunks: []openai.ChatCompletionChunk{test.chunk}, err: streamErr,
						}}
					}))
					if attempts != 1 || len(events) == 0 || events[len(events)-1].Type != EventError {
						t.Fatalf("truncated response should end in error without retry: attempts=%d events=%+v", attempts, events)
					}
					if !strings.Contains(events[len(events)-1].Error.Error(), "before a finish reason") {
						t.Fatalf("unexpected terminal error: %v", events[len(events)-1].Error)
					}
					for _, event := range events {
						if event.Type == EventComplete || event.ToolCall != nil {
							t.Fatalf("truncated response emitted completed work: %+v", event)
						}
					}
				})
			}
		}
	}
}
