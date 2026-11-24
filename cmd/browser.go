package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/browser"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/spf13/cobra"
)

var browserCmd = &cobra.Command{Use: "browser", Short: "Inspect WebDriver browser capability"}
var browserCheckCmd = &cobra.Command{Use: "check", RunE: func(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()
	endpoint := strings.TrimSpace(os.Getenv("RSCH_WEBDRIVER_URL"))
	root, _ := os.Getwd()
	if endpoint == "" {
		if err := recordEvidence(root, "browser", "rsch browser check", "unavailable", "WebDriver is not configured", "set RSCH_WEBDRIVER_URL", map[string]any{"configured": false}); err != nil {
			return err
		}
		fmt.Println("WebDriver unavailable: set RSCH_WEBDRIVER_URL")
		return browser.ErrBackendUnavailable
	}
	allowedHosts := []string(nil)
	if loaded := config.Get(); loaded != nil {
		allowedHosts = append(allowedHosts, loaded.Network.AllowedHosts...)
	}
	client, err := browser.New(endpoint, browser.Policy{AllowedHosts: allowedHosts, AllowPrivate: false})
	if err != nil {
		_ = recordEvidence(root, "browser", "rsch browser check", "failed", "WebDriver endpoint is invalid", err.Error(), map[string]any{"configured": true})
		return err
	}
	health := client.Health(ctx)
	if !health.Available || !health.Ready {
		reason := health.Message
		if reason == "" {
			reason = "WebDriver is not ready"
		}
		if recordErr := recordEvidence(root, "browser", "rsch browser check", "failed", "WebDriver health check failed", reason, map[string]any{"configured": true, "available": health.Available, "ready": health.Ready}); recordErr != nil {
			return recordErr
		}
		return fmt.Errorf("WebDriver capability unavailable: %s", reason)
	}
	if err := client.Start(ctx, os.Getenv("RSCH_BROWSER")); err != nil {
		if recordErr := recordEvidence(root, "browser", "rsch browser check", "failed", "WebDriver session failed", err.Error(), map[string]any{"configured": true}); recordErr != nil {
			return recordErr
		}
		return err
	}
	defer client.Close(context.Background())
	if err := recordEvidence(root, "browser", "rsch browser check", "observed", "WebDriver session available", "", map[string]any{"configured": true, "browser": os.Getenv("RSCH_BROWSER")}); err != nil {
		return err
	}
	fmt.Println("WebDriver session available")
	return nil
}}

func init() { browserCmd.AddCommand(browserCheckCmd); rootCmd.AddCommand(browserCmd) }
