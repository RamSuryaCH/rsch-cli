package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/spf13/cobra"
)

var costCmd = &cobra.Command{Use: "cost", Short: "Show persisted session cost and token totals", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	sessionService := session.NewServiceWithDB(db.New(conn), conn)
	items, err := sessionService.List(context.Background())
	if err != nil {
		return err
	}
	total := 0.0
	var inputTokens, outputTokens int64
	var earliest, latest int64
	for _, item := range items {
		total += item.Cost
		inputTokens += item.PromptTokens
		outputTokens += item.CompletionTokens
		if earliest == 0 || item.CreatedAt < earliest {
			earliest = item.CreatedAt
		}
		if item.UpdatedAt > latest {
			latest = item.UpdatedAt
		}
	}
	models := map[string]int{}
	providers := map[string]int{}
	var duration time.Duration
	taskCosts := map[string]float64{}
	usageItems, usageErr := sessionService.Usage(cmd.Context(), "")
	if usageErr == nil {
		for _, usage := range usageItems {
			if usage.Model != "" {
				models[usage.Model]++
			}
			if usage.Provider != "" {
				providers[usage.Provider]++
			}
			duration += usage.Duration
			if usage.TaskID != "" {
				taskCosts[usage.TaskID] += usage.Cost
			}
		}
	}
	var queryErr error
	if usageErr != nil || len(usageItems) == 0 {
		var rows *sql.Rows
		rows, queryErr = conn.QueryContext(cmd.Context(), `SELECT model, COUNT(*) FROM messages WHERE role = 'assistant' AND model IS NOT NULL GROUP BY model`)
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var model sql.NullString
				var count int
				if rows.Scan(&model, &count) == nil && model.Valid {
					models[model.String] = count
				}
			}
		}
	}
	budget := map[string]any{"task": map[string]any{"configured": false}, "session": map[string]any{"configured": false}}
	if loaded := config.Get(); loaded != nil {
		budget["task"] = map[string]any{"configured": loaded.Budget.Task > 0, "limit": loaded.Budget.Task}
		budget["session"] = map[string]any{"configured": loaded.Budget.Session > 0, "limit": loaded.Budget.Session, "actual": total, "passed": loaded.Budget.Session <= 0 || total <= loaded.Budget.Session}
	}
	result := map[string]any{"sessions": len(items), "total": total, "estimatedCost": total, "inputTokens": inputTokens, "outputTokens": outputTokens, "models": models, "providers": providers, "taskCosts": taskCosts, "duration": duration, "modelEvidence": queryErr == nil, "usageEvidence": usageErr == nil, "budget": budget, "timeRange": map[string]any{"start": time.Unix(earliest, 0).UTC(), "end": time.Unix(latest, 0).UTC()}}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("sessions: %d\ntotal cost (estimated): %.6f\ninput tokens: %d\noutput tokens: %d\nduration: %s\n", len(items), total, inputTokens, outputTokens, duration)
	for model, count := range models {
		fmt.Printf("model %s: %d assistant messages\n", model, count)
	}
	return nil
}}

func init() {
	costCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(costCmd)
}
