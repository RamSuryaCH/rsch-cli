package cmd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestHTTPServerUsesBoundedTimeouts(t *testing.T) {
	service := newHTTPServer("127.0.0.1", 0, http.NewServeMux())
	if service.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("read header timeout=%s", service.ReadHeaderTimeout)
	}
	if service.ReadTimeout != 30*time.Second {
		t.Fatalf("read timeout=%s", service.ReadTimeout)
	}
	if service.WriteTimeout != 30*time.Second {
		t.Fatalf("write timeout=%s", service.WriteTimeout)
	}
	if service.IdleTimeout != 2*time.Minute {
		t.Fatalf("idle timeout=%s", service.IdleTimeout)
	}
	if service.MaxHeaderBytes != 1<<20 {
		t.Fatalf("max header bytes=%d", service.MaxHeaderBytes)
	}
}

func TestServerStorageOverridePreservesWorkspace(t *testing.T) {
	t.Chdir(t.TempDir())
	workspace, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	externalStorage := filepath.Join(t.TempDir(), "state")
	for _, test := range []struct {
		name, dataDir, wantStorage string
	}{
		{"default", "", filepath.Join(workspace, ".rsch")},
		{"nested relative", filepath.Join("cache", "state"), filepath.Join(workspace, "cache", "state")},
		{"external absolute", externalStorage, externalStorage},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, storage, err := serverDirectories(test.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if root != workspace {
				t.Fatalf("server workspace = %q, want selected workspace %q", root, workspace)
			}
			if storage != test.wantStorage {
				t.Fatalf("server storage = %q, want %q", storage, test.wantStorage)
			}
		})
	}
}

func TestHTTPServerIPv6Address(t *testing.T) {
	service := newHTTPServer("::1", 4096, http.NewServeMux())
	host, port, err := net.SplitHostPort(service.Addr)
	if err != nil {
		t.Fatalf("invalid IPv6 listen address %q: %v", service.Addr, err)
	}
	if host != "::1" || port != "4096" {
		t.Fatalf("listen address = %q", service.Addr)
	}
}

func TestHTTPServerCancellationStopsActiveRequestAndPreservesCause(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	requestStarted := make(chan struct{})
	requestStopped := make(chan struct{})
	service := newHTTPServer("127.0.0.1", 0, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestStopped)
	}))
	defer service.Close()
	serverDone := make(chan error, 1)
	go func() { serverDone <- serveHTTPServer(ctx, service, listener) }()
	clientDone := make(chan error, 1)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	go func() {
		response, err := client.Get("http://" + listener.Addr().String())
		if response != nil {
			response.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start serving requests")
	}
	cause := &terminationSignalError{signal: syscall.SIGTERM}
	cancel(cause)
	select {
	case err := <-serverDone:
		if !errors.Is(err, cause) {
			t.Fatalf("server error = %v, want original termination cause %v", err, cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down after cancellation")
	}
	select {
	case <-requestStopped:
	default:
		t.Fatal("active HTTP request did not inherit command cancellation")
	}
	select {
	case <-clientDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client request remained open after server shutdown")
	}
}

func TestHTTPServerReportsServeFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	err = serveHTTPServer(context.Background(), newHTTPServer("127.0.0.1", 0, http.NewServeMux()), listener)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("serve failure = %v, want closed listener error", err)
	}
}
