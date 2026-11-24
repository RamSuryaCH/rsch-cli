package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
)

func TestRequestBoundaryRejectsCrossOriginMutation(t *testing.T) {
	store := engineering.NewStore()
	service := New(events.NewBus(), store)
	request := httptest.NewRequest(http.MethodPost, "/v1/goals", bytes.NewBufferString(`{"id":"cross-origin","title":"must not exist"}`))
	request.Host = "127.0.0.1:4096"
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.Goals()) != 0 {
		t.Fatalf("cross-origin request mutated goals: %+v", store.Goals())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("cross-origin response unexpectedly opted into CORS: %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestRequestBoundaryAllowsSameOriginAndNonBrowserClients(t *testing.T) {
	store := engineering.NewStore()
	service := New(events.NewBus(), store)

	sameOrigin := httptest.NewRequest(http.MethodPost, "/v1/goals", bytes.NewBufferString(`{"id":"same-origin","title":"browser"}`))
	sameOrigin.Host = "127.0.0.1:4096"
	sameOrigin.Header.Set("Origin", "http://127.0.0.1:4096")
	sameOrigin.Header.Set("Content-Type", "application/json; charset=utf-8")
	sameOriginResponse := httptest.NewRecorder()
	service.ServeHTTP(sameOriginResponse, sameOrigin)
	if sameOriginResponse.Code != http.StatusCreated {
		t.Fatalf("same-origin mutation status=%d body=%s", sameOriginResponse.Code, sameOriginResponse.Body.String())
	}

	cli := httptest.NewRequest(http.MethodPost, "/v1/goals", bytes.NewBufferString(`{"id":"cli","title":"non-browser"}`))
	cli.Host = "localhost:4096"
	cli.Header.Set("Content-Type", "application/vnd.rsch+json")
	cliResponse := httptest.NewRecorder()
	service.ServeHTTP(cliResponse, cli)
	if cliResponse.Code != http.StatusCreated {
		t.Fatalf("non-browser mutation status=%d body=%s", cliResponse.Code, cliResponse.Body.String())
	}
	if len(store.Goals()) != 2 {
		t.Fatalf("allowed requests did not create goals: %+v", store.Goals())
	}
}

func TestRequestBoundaryRejectsRebindingHost(t *testing.T) {
	store := engineering.NewStore()
	service := New(events.NewBus(), store)
	request := httptest.NewRequest(http.MethodPost, "/v1/goals", bytes.NewBufferString(`{"id":"rebound","title":"must not exist"}`))
	request.Host = "attacker.example:4096"
	request.Header.Set("Origin", "http://attacker.example:4096")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.ServeHTTP(response, request)

	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding host status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.Goals()) != 0 {
		t.Fatalf("rebinding request mutated goals: %+v", store.Goals())
	}
}

func TestRequestBoundaryRequiresJSONForRequestBodies(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
	}{
		{name: "missing"},
		{name: "browser simple text", contentType: "text/plain"},
		{name: "form", contentType: "application/x-www-form-urlencoded"},
		{name: "malformed", contentType: "application/json; charset"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := engineering.NewStore()
			service := New(events.NewBus(), store)
			request := httptest.NewRequest(http.MethodPost, "/v1/goals", bytes.NewBufferString(`{"id":"invalid-content","title":"must not exist"}`))
			request.Host = "127.0.0.1:4096"
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()

			service.ServeHTTP(response, request)

			if response.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if len(store.Goals()) != 0 {
				t.Fatalf("invalid content type mutated goals: %+v", store.Goals())
			}
		})
	}
}

func TestBearerAuthStillAllowsNonLoopbackCLIClients(t *testing.T) {
	store := engineering.NewStore()
	service := New(events.NewBus(), store).SetAuthToken("secret")
	request := httptest.NewRequest(http.MethodPost, "/v1/goals", bytes.NewBufferString(`{"id":"remote","title":"authenticated"}`))
	request.Host = "api.internal:4096"
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	service.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("authenticated non-loopback request status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSSERequestBoundaryRejectsCrossOriginAndAllowsSameOrigin(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	log := events.NewSQLLog(connection)
	event := events.New(events.TaskCompleted, map[string]any{"taskId": "task-1"})
	if err := log.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	service := NewWithLog(events.NewBus(), engineering.NewStore(), log)

	crossOrigin := httptest.NewRequest(http.MethodGet, "/v1/events?after=0", nil)
	crossOrigin.Host = "127.0.0.1:4096"
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOriginResponse := httptest.NewRecorder()
	service.ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin SSE status=%d body=%s", crossOriginResponse.Code, crossOriginResponse.Body.String())
	}

	sameOrigin := httptest.NewRequest(http.MethodGet, "/v1/events?after=0", nil)
	sameOrigin.Host = "127.0.0.1:4096"
	sameOrigin.Header.Set("Origin", "http://127.0.0.1:4096")
	requestContext, cancelRequest := context.WithCancel(context.Background())
	sameOrigin = sameOrigin.WithContext(requestContext)
	sameOriginResponse := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		service.ServeHTTP(sameOriginResponse, sameOrigin)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancelRequest()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("same-origin SSE did not stop after cancellation")
	}
	if got := sameOriginResponse.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("same-origin SSE content type=%q", got)
	}
	if !strings.Contains(sameOriginResponse.Body.String(), "event: task.completed") {
		t.Fatalf("same-origin SSE missing replay: %s", sameOriginResponse.Body.String())
	}
}
