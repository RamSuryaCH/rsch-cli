package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/server"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the RSCH HTTP and SSE service",
	RunE: func(cmd *cobra.Command, _ []string) error {
		port, _ := cmd.Flags().GetInt("port")
		host, _ := cmd.Flags().GetString("host")
		dataDir, _ := cmd.Flags().GetString("data-dir")
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid port %d", port)
		}
		if net.ParseIP(host) == nil && host != "localhost" {
			return fmt.Errorf("invalid bind host %q", host)
		}
		token := os.Getenv("RSCH_SERVER_TOKEN")
		if !isLoopbackHost(host) && token == "" {
			return fmt.Errorf("RSCH_SERVER_TOKEN is required when binding server to %s", host)
		}
		workspaceRoot, dataDir, err := serverDirectories(dataDir)
		if err != nil {
			return err
		}
		loaded, err := config.Load(workspaceRoot, false)
		if err != nil {
			return fmt.Errorf("load server policy: %w", err)
		}
		conn, err := db.ConnectAt(dataDir)
		if err != nil {
			return fmt.Errorf("open workflow database: %w", err)
		}
		defer conn.Close()
		bus := events.NewBus()
		deniedTools := make(map[string]bool)
		promptTools := make(map[string]bool)
		for name, decision := range loaded.Policy.Tool {
			switch strings.ToLower(strings.TrimSpace(decision)) {
			case string(execution.Deny):
				deniedTools[name] = true
			case string(execution.Prompt):
				promptTools[name] = true
			}
		}
		policy := execution.DefaultPolicy{
			DeniedTools:     deniedTools,
			PromptTools:     promptTools,
			AllowedPaths:    loaded.Policy.PathAllow,
			AllowedCommands: map[string]bool{},
			Production:      loaded.Policy.ProductionDenied,
		}
		for _, command := range loaded.Policy.CommandAllow {
			policy.AllowedCommands[command] = true
		}
		mutationPolicy := func(_ *http.Request, action string) error {
			risk := execution.RiskMedium
			if action == "tasks.replace" || action == "security.external" {
				risk = execution.RiskHigh
			}
			decision := policy.Evaluate(execution.Request{
				Tool: execution.Spec{Name: action, Risk: risk},
				Path: workspaceRoot,
			})
			switch decision {
			case execution.Deny:
				return fmt.Errorf("%w: %s", execution.ErrDenied, action)
			case execution.Prompt:
				return fmt.Errorf("%w: %s (HTTP callers cannot grant approval; update the trusted server policy)", execution.ErrApprovalRequired, action)
			default:
				return nil
			}
		}
		service := server.NewWithWorkspace(bus, engineering.NewSQLStore(conn), events.NewSQLLog(conn), workspaceRoot, conn).
			SetAuthToken(token).
			SetMutationPolicy(mutationPolicy)
		httpServer := newHTTPServer(host, port, service.Handler())
		var listenConfig net.ListenConfig
		listener, err := listenConfig.Listen(cmd.Context(), "tcp", httpServer.Addr)
		if err != nil {
			return err
		}
		return serveHTTPServer(cmd.Context(), httpServer, listener)
	},
}

// A storage override must not change the workspace whose policy and files the
// server exposes. The inherited --cwd flag selects that workspace explicitly.
func serverDirectories(dataDir string) (workspaceRoot, storageRoot string, err error) {
	workspaceRoot, err = os.Getwd()
	if err != nil {
		return "", "", err
	}
	if dataDir == "" {
		dataDir = ".rsch"
	}
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(workspaceRoot, dataDir)
	}
	return workspaceRoot, filepath.Clean(dataDir), nil
}

func serveHTTPServer(ctx context.Context, service *http.Server, listener net.Listener) error {
	defer listener.Close()
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	// Request cancellation also stops long-lived SSE handlers before shutdown
	// waits for active connections to drain.
	service.BaseContext = func(net.Listener) context.Context { return ctx }
	serveErr := make(chan error, 1)
	go func() { serveErr <- service.Serve(listener) }()
	select {
	case err := <-serveErr:
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Shutdown(shutdownCtx); err != nil {
			_ = service.Close()
		}
		<-serveErr
		return context.Cause(ctx)
	}
}

func newHTTPServer(host string, port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort(host, strconv.Itoa(port)),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func init() {
	serverCmd.Flags().Int("port", 4096, "HTTP port")
	serverCmd.Flags().String("host", "127.0.0.1", "Bind host; non-loopback hosts require RSCH_SERVER_TOKEN")
	serverCmd.Flags().String("data-dir", "", "RSCH data directory (default: .rsch)")
	rootCmd.AddCommand(serverCmd)
}
