package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/permission"
)

func TestFetchFailsClosedWithoutPermissionService(t *testing.T) {
	response, err := NewFetchTool(nil).Run(bashToolTestContext(), ToolCall{Input: `{"url":"https://example.com","format":"text"}`})
	if err == nil || !response.IsError || !strings.Contains(response.Content, "permission service is unavailable") {
		t.Fatalf("fetch did not fail closed: response=%+v err=%v", response, err)
	}
}

func TestFetchDenialPreventsNetworkAccess(t *testing.T) {
	_, err := NewFetchTool(bashPermissionStub{allow: false}).Run(bashToolTestContext(), ToolCall{Input: `{"url":"https://example.com","format":"text"}`})
	if !errors.Is(err, permission.ErrorPermissionDenied) {
		t.Fatalf("fetch returned %v, want permission denial", err)
	}
}

func TestFetchCanceledContextDoesNotRequestPermission(t *testing.T) {
	ctx, cancel := context.WithCancel(bashToolTestContext())
	cancel()
	response, err := NewFetchTool(nil).Run(ctx, ToolCall{Input: `{"url":"https://example.com","format":"text"}`})
	if !errors.Is(err, context.Canceled) || !response.IsError {
		t.Fatalf("canceled fetch returned response=%+v err=%v", response, err)
	}
}
