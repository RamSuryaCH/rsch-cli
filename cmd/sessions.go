package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/spf13/cobra"
)

func openSessionService(ctx context.Context) (session.Service, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	dataDir, err := config.ResolveTrustedDataDirectory(root)
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.ConnectAt(dataDir)
	if err != nil {
		return nil, nil, err
	}
	return session.NewServiceWithDB(db.New(conn), conn), func() { _ = conn.Close() }, nil
}

var sessionsCmd = &cobra.Command{Use: "sessions", Short: "Inspect persisted resumable sessions"}
var resumeCmd = &cobra.Command{Use: "resume <id>", Short: "Resume a persisted session by loading its durable state", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	service, closeDB, err := openSessionService(cmd.Context())
	if err != nil {
		return err
	}
	defer closeDB()
	item, err := service.Resume(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return writeCLIJSON(cmd.OutOrStdout(), map[string]any{"resumed": true, "session": item})
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "resumed session %s\ntitle: %s\nmessages: %d\ncost: %.4f\n", item.ID, item.Title, item.MessageCount, item.Cost)
	return err
}}
var sessionsListCmd = &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, _ []string) error {
	service, closeDB, err := openSessionService(cmd.Context())
	if err != nil {
		return err
	}
	defer closeDB()
	items, err := service.List(cmd.Context())
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return writeCLIJSON(cmd.OutOrStdout(), items)
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no persisted sessions")
		return err
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s messages=%d cost=%.4f\n", item.ID, item.Title, item.MessageCount, item.Cost); err != nil {
			return err
		}
	}
	return nil
}}
var sessionsShowCmd = &cobra.Command{Use: "show <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	service, closeDB, err := openSessionService(cmd.Context())
	if err != nil {
		return err
	}
	defer closeDB()
	item, err := service.Get(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return writeCLIJSON(cmd.OutOrStdout(), item)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "id: %s\ntitle: %s\nmessages: %d\ncost: %.4f\n", item.ID, item.Title, item.MessageCount, item.Cost)
	return err
}}
var sessionsDeleteCmd = &cobra.Command{Use: "delete <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	service, closeDB, err := openSessionService(cmd.Context())
	if err != nil {
		return err
	}
	defer closeDB()
	return service.Delete(cmd.Context(), args[0])
}}

func init() {
	sessionsListCmd.Flags().Bool("json", false, "Output structured JSON")
	sessionsShowCmd.Flags().Bool("json", false, "Output structured JSON")
	resumeCmd.Flags().Bool("json", false, "Output structured JSON")
	sessionsCmd.AddCommand(sessionsListCmd, sessionsShowCmd, sessionsDeleteCmd)
	rootCmd.AddCommand(sessionsCmd, resumeCmd)
}
