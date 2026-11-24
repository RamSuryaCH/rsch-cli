package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
)

func TestMalformedJSONDoesNotMutateTasks(t *testing.T) {
	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/v1/tasks?goalId=goal-1"},
		{http.MethodPost, "/v1/goals/goal-1/tasks"},
	} {
		for _, test := range []struct {
			name string
			body string
		}{
			{"null", "null"},
			{"extra document", "[] []"},
			{"trailing garbage", "[] invalid"},
			{"oversized suffix", "[]" + strings.Repeat(" ", 2<<20)},
		} {
			t.Run(endpoint.method+endpoint.path+"/"+test.name, func(t *testing.T) {
				store := engineering.NewStore()
				if err := store.ReplaceTasks(context.Background(), "goal-1", []engineering.Task{{ID: "task-1", Title: "preserve this task"}}); err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				New(nil, store).ServeHTTP(response, newServerRequest(endpoint.method, endpoint.path, strings.NewReader(test.body)))
				if response.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				tasks := store.Tasks()
				if len(tasks) != 1 || tasks[0].ID != "task-1" {
					t.Fatalf("invalid document changed tasks: %+v", tasks)
				}
			})
		}
	}
}

func TestJSONBodyAllowsTrailingWhitespaceAndEmptyTaskArray(t *testing.T) {
	store := engineering.NewStore()
	if err := store.ReplaceTasks(context.Background(), "goal-1", []engineering.Task{{ID: "task-1", Title: "replace this task"}}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	New(nil, store).ServeHTTP(response, newServerRequest(http.MethodPut, "/v1/tasks?goalId=goal-1", strings.NewReader("[] \n\t")))
	if response.Code != http.StatusOK || len(store.Tasks()) != 0 {
		t.Fatalf("valid empty collection rejected: status=%d body=%s tasks=%+v", response.Code, response.Body.String(), store.Tasks())
	}
}

func TestTaskEditCannotBypassTransitionPolicy(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := engineering.NewSQLStore(connection)
	if err := store.SaveGoal(context.Background(), engineering.Goal{ID: "goal-1", Title: "goal"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceTasks(context.Background(), "goal-1", []engineering.Task{{ID: "task-1", Title: "original", Status: engineering.StatusPending}}); err != nil {
		t.Fatal(err)
	}
	service := New(nil, store).SetMutationPolicy(func(_ *http.Request, action string) error {
		if action == "task.transition" {
			return fmt.Errorf("transitions are denied")
		}
		return nil
	})
	response := httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodPut, "/v1/tasks/task-1", bytes.NewBufferString(`{"title":"changed","status":"completed"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("task edit bypassed transition checks: status=%d body=%s", response.Code, response.Body.String())
	}
	tasks, err := store.LoadTasks(context.Background(), "goal-1")
	if err != nil || len(tasks) != 1 || tasks[0].Status != engineering.StatusPending || tasks[0].Title != "original" {
		t.Fatalf("rejected edit changed task: tasks=%+v err=%v", tasks, err)
	}
}

func TestSSEIdleStreamFlushesHeaders(t *testing.T) {
	server := httptest.NewServer(New(nil, nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("idle stream did not establish promptly: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d content type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
}

func TestSSEReplaysEntireBacklogAndResumesFromLastEventID(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	log := events.NewSQLLog(connection)
	const eventCount = 1003
	for i := 0; i < eventCount; i++ {
		if err := log.Append(context.Background(), events.New(events.TaskCompleted, nil)); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(NewWithLog(nil, nil, log))
	defer server.Close()
	for _, test := range []struct {
		name   string
		header string
		first  int
	}{
		{"multiple pages", "", 1},
		{"header overrides original query", "1000", 1001},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events?after=0", nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.header != "" {
				request.Header.Set("Last-Event-ID", test.header)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			scanner := bufio.NewScanner(response.Body)
			want := test.first
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), "id: ") {
					continue
				}
				if got := strings.TrimPrefix(scanner.Text(), "id: "); got != strconv.Itoa(want) {
					t.Fatalf("event sequence=%s, want %d", got, want)
				}
				want++
				if want > eventCount {
					return
				}
			}
			t.Fatalf("backlog stopped at sequence %d, want %d: %v", want-1, eventCount, scanner.Err())
		})
	}
}

func TestSSERejectsInvalidReconnectSequence(t *testing.T) {
	for _, cursor := range []string{"-1", "invalid", "9223372036854775808"} {
		t.Run(cursor, func(t *testing.T) {
			service := NewWithLog(nil, nil, events.NewSQLLog(nil))
			request := newServerRequest(http.MethodGet, "/v1/events", nil)
			request.Header.Set("Last-Event-ID", cursor)
			response := httptest.NewRecorder()
			service.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("invalid reconnect status=%d content type=%q", response.Code, response.Header().Get("Content-Type"))
			}
		})
	}
}

type flushCallbackRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (w *flushCallbackRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.onFlush()
}

func TestSSEReplayDeduplicatesBufferedEventsAndKeepsNewLiveEvents(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	log := events.NewSQLLog(connection)
	first := events.New(events.TaskCompleted, nil)
	if err := log.AppendEvent(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	second := events.New(events.TaskCompleted, nil)
	flushes := 0
	response := &flushCallbackRecorder{ResponseRecorder: httptest.NewRecorder()}
	response.onFlush = func() {
		flushes++
		if flushes == 1 {
			// Publish while the handler has subscribed and read its first page,
			// but has not yet written that page or consumed the live buffer.
			if err := log.AppendEvent(ctx, &second); err != nil {
				t.Fatal(err)
			}
			bus.Publish(first)
			bus.Publish(second)
		}
		if flushes == 3 {
			cancel()
		}
	}
	request := newServerRequest(http.MethodGet, "/v1/events?after=0", nil).WithContext(ctx)
	NewWithLog(bus, nil, log).ServeHTTP(response, request)
	for _, event := range []events.Event{first, second} {
		if count := strings.Count(response.Body.String(), "id: "+strconv.FormatInt(event.Sequence, 10)+"\n"); count != 1 {
			t.Fatalf("sequence %d appeared %d times: %s", event.Sequence, count, response.Body.String())
		}
	}
}
