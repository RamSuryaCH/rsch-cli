package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/lsp/protocol"
	"github.com/opencode-ai/opencode/internal/process"
)

type bufferWriteCloser struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	closed  bool
	written chan struct{}
	once    sync.Once
}

func (w *bufferWriteCloser) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	w.once.Do(func() {
		if w.written != nil {
			close(w.written)
		}
	})
	return w.buffer.Write(value)
}

func (w *bufferWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *bufferWriteCloser) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

type interleavingWriteCloser struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	closed bool
}

type blockingWriteCloser struct {
	started   chan struct{}
	release   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (w *blockingWriteCloser) Write(value []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	select {
	case <-w.release:
		return len(value), nil
	case <-w.closed:
		return 0, os.ErrClosed
	}
}

func (w *blockingWriteCloser) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

func (w *interleavingWriteCloser) Write(value []byte) (int, error) {
	for index := range value {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return index, os.ErrClosed
		}
		_ = w.buffer.WriteByte(value[index])
		w.mu.Unlock()
		runtime.Gosched()
	}
	return len(value), nil
}

func (w *interleavingWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *interleavingWriteCloser) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

func testClient(stdin io.WriteCloser) *Client {
	client := &Client{
		stdin:                 stdin,
		handlers:              make(map[int32]chan callResult),
		notificationHandlers:  make(map[string]NotificationHandler),
		serverRequestHandlers: make(map[string]ServerRequestHandler),
		diagnostics:           make(map[protocol.DocumentUri][]protocol.Diagnostic),
		openFiles:             make(map[string]*OpenFileInfo),
	}
	client.serverState.Store(StateStarting)
	return client
}

func TestCallReturnsWhenContextIsCancelled(t *testing.T) {
	writer := &bufferWriteCloser{}
	client := testClient(writer)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := client.Call(ctx, "workspace/symbol", map[string]string{"query": "x"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline, got %v", err)
	}
	client.handlersMu.RLock()
	pending := len(client.handlers)
	client.handlersMu.RUnlock()
	if pending != 0 {
		t.Fatalf("cancelled call left %d pending handlers", pending)
	}
}

func TestTransportEOFFailsPendingCall(t *testing.T) {
	serverOutput, clientOutput := io.Pipe()
	requestWriter := &bufferWriteCloser{written: make(chan struct{})}
	client := testClient(requestWriter)
	client.stdout = bufio.NewReader(serverOutput)
	client.stdoutCloser = serverOutput
	go client.handleMessages()

	result := make(chan error, 1)
	go func() {
		result <- client.Call(context.Background(), "workspace/symbol", struct{}{}, nil)
	}()
	select {
	case <-requestWriter.written:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	if err := clientOutput.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if !errors.Is(err, ErrTransportClosed) {
			t.Fatalf("expected transport error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call did not fail after EOF")
	}
}

func TestClientSerializesConcurrentFrames(t *testing.T) {
	writer := &interleavingWriteCloser{}
	client := testClient(writer)
	const total = 32
	var wait sync.WaitGroup
	for index := 0; index < total; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := client.Notify(context.Background(), "test/"+strconv.Itoa(index), map[string]int{"index": index}); err != nil {
				t.Errorf("notify %d: %v", index, err)
			}
		}()
	}
	wait.Wait()

	reader := bufio.NewReader(bytes.NewReader(writer.Bytes()))
	seen := make(map[string]bool, total)
	for index := 0; index < total; index++ {
		message, err := ReadMessage(reader)
		if err != nil {
			t.Fatalf("read frame %d: %v", index, err)
		}
		seen[message.Method] = true
	}
	for index := 0; index < total; index++ {
		method := "test/" + strconv.Itoa(index)
		if !seen[method] {
			t.Errorf("missing frame %q", method)
		}
	}
}

func TestWriteQueueHonorsContextCancellation(t *testing.T) {
	writer := newBlockingWriteCloser()
	client := testClient(writer)
	first := make(chan error, 1)
	go func() {
		first <- client.Notify(context.Background(), "test/blocked", struct{}{})
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("first write did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.Notify(ctx, "test/queued", struct{}{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued write error = %v, want context deadline", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("queued write did not stop promptly")
	}

	close(writer.release)
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first write failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first write did not finish")
	}
}

func TestActiveWriteCancellationClosesBrokenTransport(t *testing.T) {
	writer := newBlockingWriteCloser()
	client := testClient(writer)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := client.Notify(ctx, "test/blocked", struct{}{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active write error = %v, want context deadline", err)
	}
	select {
	case <-writer.closed:
	default:
		t.Fatal("canceled write did not close the broken transport")
	}
	if err := client.Notify(context.Background(), "test/after-close", struct{}{}); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("write after canceled transport error = %v, want transport closed", err)
	}
}

func TestCloseFailsPendingCall(t *testing.T) {
	writer := &bufferWriteCloser{written: make(chan struct{})}
	client := testClient(writer)
	result := make(chan error, 1)
	go func() {
		result <- client.Call(context.Background(), "workspace/symbol", struct{}{}, nil)
	}()
	select {
	case <-writer.written:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrClientClosed) {
			t.Fatalf("expected client closed error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call did not fail on Close")
	}
}

func TestDiagnosticsAccessorsReturnSnapshots(t *testing.T) {
	client := testClient(&bufferWriteCloser{})
	uri := protocol.DocumentUri("file:///workspace/main.go")
	client.diagnostics[uri] = []protocol.Diagnostic{{Message: "original"}}

	all := client.GetDiagnostics()
	all[uri][0].Message = "changed"
	all[protocol.DocumentUri("file:///workspace/other.go")] = nil
	file := client.GetFileDiagnostics(uri)
	file[0].Message = "also changed"

	current := client.GetFileDiagnostics(uri)
	if len(current) != 1 || current[0].Message != "original" {
		t.Fatalf("diagnostics cache was mutated through a snapshot: %+v", current)
	}
	if len(client.GetDiagnostics()) != 1 {
		t.Fatalf("diagnostics map was mutated through a snapshot: %+v", client.GetDiagnostics())
	}
}

func TestLSPProcessHelper(t *testing.T) {
	if os.Getenv("RSCH_LSP_PROCESS_HELPER") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestCloseReapsLSPProcess(t *testing.T) {
	t.Setenv("RSCH_LSP_PROCESS_HELPER", "1")
	client, err := NewClientWithOptions(context.Background(), ClientStartOptions{
		Argv:        []string{os.Args[0], "-test.run=^TestLSPProcessHelper$"},
		Environment: map[string]string{"RSCH_LSP_PROCESS_HELPER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pid := client.Process.PID()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if process.Alive(pid) {
		t.Fatalf("LSP process %d is still alive after Close", pid)
	}
}

func TestClientContextCancellationReapsLSPProcess(t *testing.T) {
	t.Setenv("RSCH_LSP_PROCESS_HELPER", "1")
	ctx, cancel := context.WithCancel(context.Background())
	client, err := NewClientWithOptions(ctx, ClientStartOptions{
		Argv:        []string{os.Args[0], "-test.run=^TestLSPProcessHelper$"},
		Environment: map[string]string{"RSCH_LSP_PROCESS_HELPER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pid := client.Process.PID()
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for process.Alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if process.Alive(pid) {
		t.Fatalf("canceled LSP process %d was not reaped", pid)
	}
	_ = client.Close()
}

func TestInitializeSendsInitializedExactlyOnce(t *testing.T) {
	writer := &bufferWriteCloser{written: make(chan struct{})}
	client := testClient(writer)
	done := make(chan error, 1)
	go func() {
		_, err := client.InitializeLSPClient(context.Background(), t.TempDir())
		done <- err
	}()
	select {
	case <-writer.written:
	case <-time.After(time.Second):
		t.Fatal("initialize request was not written")
	}
	if !client.deliverResponse(&Message{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: json.RawMessage(`{"capabilities":{}}`)}) {
		t.Fatal("initialize response was not delivered")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("initialize did not complete")
	}

	reader := bufio.NewReader(bytes.NewReader(writer.Bytes()))
	initialized := 0
	for {
		message, err := ReadMessage(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("read initialization frames: %v", err)
			}
			break
		}
		if message.Method == "initialized" {
			initialized++
		}
	}
	if initialized != 1 {
		t.Fatalf("initialized notification count = %d, want 1", initialized)
	}
}

func TestSanitizedLSPEnvironmentExcludesInheritedCredentials(t *testing.T) {
	t.Setenv("PATH", "/safe/bin")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")
	environment := sanitizedLSPEnvironment(nil)
	if environment["PATH"] != "/safe/bin" {
		t.Fatalf("PATH = %q", environment["PATH"])
	}
	for _, key := range []string{"OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY"} {
		if _, exists := environment[key]; exists {
			t.Fatalf("inherited credential %s leaked to LSP environment", key)
		}
	}
}

func TestReadMessageRejectsInvalidLength(t *testing.T) {
	for _, frame := range []string{
		"X-Test: value\r\n\r\n{}",
		"Content-Length: -1\r\n\r\n",
		"Content-Length: " + strconv.Itoa(maxLSPMessageBytes+1) + "\r\n\r\n",
	} {
		if _, err := ReadMessage(bufio.NewReader(bytes.NewBufferString(frame))); err == nil {
			t.Fatalf("expected invalid frame to fail: %q", frame)
		}
	}
}

func TestServerRequestsPreserveStringAndZeroIDs(t *testing.T) {
	for _, id := range []string{`0`, `"server-1"`, `""`, `"escaped\"id"`, `42`} {
		t.Run(id, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":` + id + `,"method":"test/echo","params":{}}`
			writer := &bufferWriteCloser{}
			client := testClient(writer)
			client.stdout = bufio.NewReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)))
			calls := 0
			client.RegisterServerRequestHandler("test/echo", func(json.RawMessage) (any, error) {
				calls++
				return "ok", nil
			})
			client.handleMessages()
			if calls != 1 {
				t.Fatalf("server handler called %d times, want 1", calls)
			}
			response, err := ReadMessage(bufio.NewReader(bytes.NewReader(writer.Bytes())))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["id"]) != id || string(fields["result"]) != `"ok"` {
				t.Fatalf("response = %s, want id %s and result ok", encoded, id)
			}
		})
	}
}

func TestRequestZeroIDAndNotificationAreDistinct(t *testing.T) {
	request, err := NewRequest(0, "test/request", nil)
	if err != nil {
		t.Fatal(err)
	}
	notification, err := NewNotification("test/notification", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		message *Message
		wantID  bool
	}{{request, true}, {notification, false}} {
		encoded, err := json.Marshal(test.message)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["id"]; exists != test.wantID {
			t.Errorf("message %s has ID = %v, want %v", encoded, exists, test.wantID)
		}
	}
}

func TestResponseIDTypesDoNotCollide(t *testing.T) {
	client := testClient(&bufferWriteCloser{})
	response := make(chan callResult, 1)
	if err := client.registerCall(1, response); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{`"1"`, `null`, `0`, `1`} {
		body := `{"jsonrpc":"2.0","id":` + id + `,"result":"ok"}`
		message, err := ReadMessage(bufio.NewReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))))
		if err != nil {
			t.Fatal(err)
		}
		if delivered := client.deliverResponse(message); delivered != (id == `1`) {
			t.Fatalf("response ID %s delivered = %v", id, delivered)
		}
	}
	select {
	case result := <-response:
		if string(result.message.Result) != `"ok"` {
			t.Fatalf("result = %s", result.message.Result)
		}
	default:
		t.Fatal("numeric response was not delivered")
	}
}

func TestZeroResponseIDMatchesPendingCall(t *testing.T) {
	client := testClient(&bufferWriteCloser{})
	response := make(chan callResult, 1)
	if err := client.registerCall(0, response); err != nil {
		t.Fatal(err)
	}
	if !client.deliverResponse(&Message{ID: json.RawMessage(`0`), Result: json.RawMessage(`null`)}) {
		t.Fatal("response with numeric zero ID was not delivered")
	}
}

func TestReadMessageRejectsInvalidIDTypes(t *testing.T) {
	for _, id := range []string{`true`, `{}`, `[]`, `1.5`, `2147483648`} {
		body := `{"jsonrpc":"2.0","id":` + id + `,"method":"test/request"}`
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)))); err == nil {
			t.Errorf("invalid LSP ID %s was accepted", id)
		}
	}
}
