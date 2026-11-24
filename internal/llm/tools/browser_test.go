package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/permission"
)

type blockedBrowserPermission struct {
	permission.Service
	started chan struct{}
	release chan struct{}
}

func (p blockedBrowserPermission) RequestContext(ctx context.Context, _ permission.CreatePermissionRequest) bool {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return false
}

func TestBrowserQueuedOperationHonorsCancellation(t *testing.T) {
	permissions := blockedBrowserPermission{started: make(chan struct{}), release: make(chan struct{})}
	tool := NewBrowserTool(permissions)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = tool.Run(bashToolTestContext(), ToolCall{Input: `{"operation":"start"}`})
	}()
	defer func() { close(permissions.release); <-firstDone }()
	select {
	case <-permissions.started:
	case <-time.After(time.Second):
		t.Fatal("first browser operation did not reach permission request")
	}
	ctx, cancel := context.WithTimeout(bashToolTestContext(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := tool.Run(ctx, ToolCall{Input: `{"operation":"close"}`})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued browser operation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled browser operation remained blocked behind another operation")
	}
}

func TestBrowserToolPersistsSessionAcrossOperations(t *testing.T) {
	var screenshotPNG bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(&screenshotPNG, img); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/session":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]string{"sessionId": "s1"}})
		case "/session/s1/source":
			_ = json.NewEncoder(w).Encode(map[string]string{"value": "<html><body>ok</body></html>"})
		case "/session/s1/screenshot":
			_ = json.NewEncoder(w).Encode(map[string]string{"value": base64.StdEncoding.EncodeToString(screenshotPNG.Bytes())})
		case "/session/s1":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("RSCH_WEBDRIVER_URL", server.URL)

	permissions := permission.NewPermissionService()
	permissions.AutoApproveSession("session-1")
	tool := NewBrowserTool(permissions)
	ctx := context.WithValue(context.WithValue(context.Background(), SessionIDContextKey, "session-1"), MessageIDContextKey, "message-1")

	started, err := tool.Run(ctx, ToolCall{Input: `{"operation":"start"}`})
	if err != nil || started.IsError {
		t.Fatalf("browser start failed: %+v %v", started, err)
	}
	dom, err := tool.Run(ctx, ToolCall{Input: `{"operation":"dom"}`})
	if err != nil || dom.Content != "<html><body>ok</body></html>" {
		t.Fatalf("browser DOM failed: %+v %v", dom, err)
	}
	screenshot, err := tool.Run(ctx, ToolCall{Input: `{"operation":"screenshot"}`})
	if err != nil || screenshot.Metadata == "" || !strings.Contains(screenshot.Metadata, "visual") {
		t.Fatalf("browser screenshot visual evidence failed: %+v %v", screenshot, err)
	}
	if strings.Contains(screenshot.Metadata, "base64") {
		t.Fatalf("browser screenshot metadata retained base64 payload: %s", screenshot.Metadata)
	}
	if !strings.Contains(screenshot.Content, "Deterministic visual evidence:") || !strings.Contains(screenshot.Content, `"width":1`) {
		t.Fatalf("browser screenshot lacks textual visual fallback: %q", screenshot.Content)
	}
	if len(screenshot.Media) != 1 || screenshot.Media[0].MIMEType != "image/png" || !bytes.Equal(screenshot.Media[0].Data, screenshotPNG.Bytes()) {
		t.Fatalf("browser screenshot lacks native image media: %+v", screenshot.Media)
	}
	closed, err := tool.Run(ctx, ToolCall{Input: `{"operation":"close"}`})
	if err != nil || closed.IsError {
		t.Fatalf("browser close failed: %+v %v", closed, err)
	}
}

func TestBrowserFailedCloseRetainsSessionForRetry(t *testing.T) {
	for _, operation := range []string{"close", "start"} {
		t.Run(operation, func(t *testing.T) {
			var starts, deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/session":
					starts.Add(1)
					_, _ = w.Write([]byte(`{"value":{"sessionId":"s1"}}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/session/s1":
					if deletes.Add(1) == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(`{"value":{"error":"unknown error","message":"temporary failure"}}`))
						return
					}
					_, _ = w.Write([]byte(`{"value":null}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			t.Setenv("RSCH_WEBDRIVER_URL", server.URL)
			tool := NewBrowserTool(bashPermissionStub{allow: true}).(*browserTool)
			ctx := bashToolTestContext()
			if _, err := tool.Run(ctx, ToolCall{Input: `{"operation":"start"}`}); err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(BrowserParams{Operation: operation})
			if err != nil {
				t.Fatal(err)
			}
			response, err := tool.Run(ctx, ToolCall{Input: string(input)})
			if err == nil || !response.IsError {
				t.Fatalf("failed cleanup was reported as success: response=%+v err=%v", response, err)
			}
			if tool.client == nil || tool.client.SessionID != "s1" || starts.Load() != 1 {
				t.Fatalf("failed cleanup lost or replaced the session: client=%+v starts=%d", tool.client, starts.Load())
			}
			response, err = tool.Run(ctx, ToolCall{Input: `{"operation":"close"}`})
			if err != nil || response.IsError || tool.client != nil || deletes.Load() != 2 {
				t.Fatalf("cleanup retry failed: response=%+v err=%v deletes=%d", response, err, deletes.Load())
			}
		})
	}
}

func TestWithResponseMediaBoundsAndCopiesPayload(t *testing.T) {
	source := []byte("png")
	response := WithResponseMedia(NewTextResponse("fallback"), ToolMedia{MIMEType: "image/png", Data: source})
	source[0] = 'X'
	if len(response.Media) != 1 || string(response.Media[0].Data) != "png" {
		t.Fatalf("media was not copied: %+v", response.Media)
	}

	oversized := make([]byte, MaxToolMediaBytes+1)
	response = WithResponseMedia(NewTextResponse("fallback"), ToolMedia{MIMEType: "image/png", Data: oversized})
	if len(response.Media) != 0 {
		t.Fatalf("oversized media was retained: %d bytes", len(response.Media[0].Data))
	}
}
