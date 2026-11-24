package message

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

func TestImageURLContentSurvivesSerialization(t *testing.T) {
	want := ImageURLContent{
		URL:    "https://example.com/image.png",
		Detail: "high",
	}

	data, err := marshallParts([]ContentPart{want})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := unmarshallParts(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("deserialized parts = %d, want 1", len(parts))
	}
	got, ok := parts[0].(ImageURLContent)
	if !ok {
		t.Fatalf("deserialized part type = %T, want ImageURLContent", parts[0])
	}
	if got != want {
		t.Fatalf("deserialized image part = %+v, want %+v", got, want)
	}
}

func TestToolResultMediaSurvivesSerialization(t *testing.T) {
	want := ToolResult{
		ToolCallID: "call-1",
		Name:       "browser",
		Content:    "textual visual analysis",
		Media:      []BinaryContent{{MIMEType: "image/png", Data: []byte("png")}},
	}

	data, err := marshallParts([]ContentPart{want})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := unmarshallParts(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("deserialized parts = %d, want 1", len(parts))
	}
	got, ok := parts[0].(ToolResult)
	if !ok || got.ToolCallID != want.ToolCallID || got.Name != want.Name || got.Content != want.Content || len(got.Media) != 1 || got.Media[0].MIMEType != "image/png" || !bytes.Equal(got.Media[0].Data, want.Media[0].Data) {
		t.Fatalf("deserialized tool result = %#v, want %#v", parts[0], want)
	}
}

func TestToolResultsReturnsEachResultExactlyOnceInOrder(t *testing.T) {
	msg := Message{Parts: []ContentPart{
		TextContent{Text: "ignored"},
		ToolResult{ToolCallID: "call-1", Content: "first"},
		Finish{Reason: FinishReasonEndTurn},
		ToolResult{ToolCallID: "call-2", Content: "second"},
	}}

	results := msg.ToolResults()
	if len(results) != 2 {
		t.Fatalf("ToolResults returned %d results, want exactly 2: %+v", len(results), results)
	}
	if results[0].ToolCallID != "call-1" || results[1].ToolCallID != "call-2" {
		t.Fatalf("ToolResults changed result order: %+v", results)
	}
}

func TestBinaryContentDataURL(t *testing.T) {
	got := (BinaryContent{MIMEType: "image/png", Data: []byte("png")}).DataURL()
	if !strings.HasPrefix(got, "data:image/png;base64,") || got != "data:image/png;base64,cG5n" {
		t.Fatalf("unexpected data URL: %q", got)
	}
}

func TestUpdatePublishesCanonicalRedactedMessage(t *testing.T) {
	connection, err := db.ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queries := db.New(connection)
	if _, err := queries.CreateSession(ctx, db.CreateSessionParams{ID: "session-1", Title: "redaction"}); err != nil {
		t.Fatal(err)
	}
	service := NewService(queries)
	created, err := service.Create(ctx, "session-1", CreateMessageParams{Role: Assistant})
	if err != nil {
		t.Fatal(err)
	}
	events := service.Subscribe(ctx)

	const secret = "AKIA1234567890ABCDEF"
	created.Parts = []ContentPart{ToolCall{
		ID:       "call-1",
		Name:     "bash",
		Input:    `{"command":"echo ` + secret + `"}`,
		Finished: true,
	}}
	if err := service.Update(ctx, created); err != nil {
		t.Fatal(err)
	}

	var updated Message
	select {
	case event := <-events:
		if event.Type != pubsub.UpdatedEvent {
			t.Fatalf("event type = %q, want %q", event.Type, pubsub.UpdatedEvent)
		}
		updated = event.Payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for updated message event")
	}

	stored, err := service.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	publishedCalls := updated.ToolCalls()
	storedCalls := stored.ToolCalls()
	if len(publishedCalls) != 1 || len(storedCalls) != 1 {
		t.Fatalf("tool calls published=%#v stored=%#v", publishedCalls, storedCalls)
	}
	if strings.Contains(publishedCalls[0].Input, secret) {
		t.Fatalf("published tool input contains secret: %q", publishedCalls[0].Input)
	}
	if publishedCalls[0] != storedCalls[0] {
		t.Fatalf("published tool call = %#v, stored = %#v", publishedCalls[0], storedCalls[0])
	}
	if updated.ID != created.ID || updated.SessionID != created.SessionID || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("update changed stable message fields: created=%+v published=%+v", created, updated)
	}
}

func TestDeleteSessionMessagesIsAtomicAndPublishesAfterSuccess(t *testing.T) {
	connection, err := db.ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queries := db.New(connection)
	for _, id := range []string{"target", "unrelated"} {
		if _, err := queries.CreateSession(ctx, db.CreateSessionParams{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(queries)
	for _, id := range []string{"target", "target", "unrelated"} {
		if _, err := service.Create(ctx, id, CreateMessageParams{Role: User, Parts: []ContentPart{TextContent{Text: "keep until committed"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.Exec(`CREATE TRIGGER block_message_delete BEFORE DELETE ON messages
		WHEN OLD.session_id = 'target' AND OLD.sequence = 2
		BEGIN SELECT RAISE(ABORT, 'simulated delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	events := service.Subscribe(ctx)
	if err := service.DeleteSessionMessages(ctx, "target"); err == nil {
		t.Fatal("expected delete failure")
	}
	items, err := service.List(ctx, "target")
	if err != nil || len(items) != 2 {
		t.Fatalf("failed delete left %d messages, want 2: %v", len(items), err)
	}
	parent, err := queries.GetSessionByID(ctx, "target")
	if err != nil || parent.MessageCount != 2 {
		t.Fatalf("failed delete changed message count: %+v, err = %v", parent, err)
	}
	select {
	case event := <-events:
		t.Fatalf("failed delete published event: %+v", event)
	default:
	}
	if _, err := connection.Exec(`DROP TRIGGER block_message_delete`); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSessionMessages(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	if items, err := service.List(ctx, "target"); err != nil || len(items) != 0 {
		t.Fatalf("deleted messages = %+v, err = %v", items, err)
	}
	if items, err := service.List(ctx, "unrelated"); err != nil || len(items) != 1 {
		t.Fatalf("unrelated messages = %+v, err = %v", items, err)
	}
	parent, err = queries.GetSessionByID(ctx, "target")
	if err != nil || parent.MessageCount != 0 {
		t.Fatalf("successful delete left message count: %+v, err = %v", parent, err)
	}
	for range 2 {
		select {
		case event := <-events:
			if event.Type != pubsub.DeletedEvent || event.Payload.SessionID != "target" {
				t.Fatalf("unexpected deletion event: %+v", event)
			}
		default:
			t.Fatal("missing deletion event")
		}
	}
}

func TestCreateAndUpdateRedactSecretsInsideSerializedContent(t *testing.T) {
	connection, err := db.ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queries := db.New(connection)
	if _, err := queries.CreateSession(ctx, db.CreateSessionParams{ID: "session", Title: "redaction"}); err != nil {
		t.Fatal(err)
	}
	service := NewService(queries)
	events := service.Subscribe(ctx)
	const secret = "synthetic-private-value"
	parts := []ContentPart{
		TextContent{Text: `api_key="` + secret + `"`},
		ToolCall{ID: "call", Input: `{"api_key":"` + secret + `","keep":"quoted \"value\""}`},
		ToolResult{ToolCallID: "call", Metadata: `{"token":"` + secret + `"}`, Media: []BinaryContent{{
			Path: `token="` + secret + `"`, MIMEType: "image/png", Data: []byte("opaque-binary-data"),
		}}},
	}
	created, err := service.Create(ctx, "session", CreateMessageParams{Role: Assistant, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	assertRedacted := func(item Message) {
		t.Helper()
		encoded, err := marshallParts(item.Parts)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Error("message contains a credential inside serialized content")
		}
		calls := item.ToolCalls()
		if len(calls) != 1 {
			t.Fatalf("tool calls = %+v, want one", calls)
		}
		var input map[string]string
		if err := json.Unmarshal([]byte(calls[0].Input), &input); err != nil {
			t.Fatalf("redaction corrupted tool JSON: %v", err)
		}
		if input["api_key"] != "[REDACTED]" || input["keep"] != `quoted "value"` {
			t.Errorf("redacted tool input = %+v", input)
		}
		results := item.ToolResults()
		if len(results) != 1 || len(results[0].Media) != 1 || !bytes.Equal(results[0].Media[0].Data, []byte("opaque-binary-data")) {
			t.Errorf("redaction changed binary media: %+v", results)
		}
	}
	assertRedacted(created)
	if !strings.Contains(parts[1].(ToolCall).Input, secret) || !strings.Contains(parts[2].(ToolResult).Media[0].Path, secret) {
		t.Fatal("Create mutated caller-owned parts")
	}
	for _, operation := range []string{"create", "update"} {
		if operation == "update" {
			created.Parts = parts
			if err := service.Update(ctx, created); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(parts[1].(ToolCall).Input, secret) || !strings.Contains(parts[2].(ToolResult).Media[0].Path, secret) {
				t.Fatal("Update mutated caller-owned parts")
			}
		}
		stored, err := service.Get(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertRedacted(stored)
		select {
		case event := <-events:
			assertRedacted(event.Payload)
		default:
			t.Fatalf("missing %s event", operation)
		}
	}
}
