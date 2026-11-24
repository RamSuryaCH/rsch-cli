package tools

import (
	"context"
	"testing"
)

func fileSessionContext(sessionID string) context.Context {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, sessionID)
	return context.WithValue(ctx, MessageIDContextKey, "message-1")
}

func TestFileReadAuthorizationIsScopedToSession(t *testing.T) {
	path := t.TempDir() + "/shared.txt"
	first := fileSessionContext("session-first")
	second := fileSessionContext("session-second")

	recordFileRead(first, path)
	if getLastReadTime(first, path).IsZero() {
		t.Fatal("reading session did not retain its authorization")
	}
	if !getLastReadTime(second, path).IsZero() {
		t.Fatal("file read authorization leaked across sessions")
	}
	if !getLastReadTime(context.Background(), path).IsZero() {
		t.Fatal("file read authorization leaked to an unscoped context")
	}
}
