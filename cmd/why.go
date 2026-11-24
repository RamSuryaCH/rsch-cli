package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/audit"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/spf13/cobra"
)

type whyReport struct {
	Query       string         `json:"query"`
	Actions     []audit.Entry  `json:"actions"`
	Events      []events.Event `json:"evidence"`
	Constraints []string       `json:"constraints"`
}

var whyCmd = &cobra.Command{Use: "why [resource]", Short: "Explain actions using persisted evidence", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	query := ""
	if len(args) == 1 {
		query = args[0]
	}
	report := whyReport{Query: query, Actions: []audit.Entry{}, Events: []events.Event{}, Constraints: []string{}}
	if log, logErr := audit.New(root); logErr == nil {
		items, listErr := log.List(50)
		if listErr != nil {
			return listErr
		}
		for _, item := range items {
			if query == "" || strings.Contains(item.Action, query) || strings.Contains(item.Resource, query) {
				report.Actions = append(report.Actions, item)
			}
			if item.Decision != "" && (query == "" || strings.Contains(item.Action, query) || strings.Contains(item.Resource, query)) {
				report.Constraints = appendUnique(report.Constraints, "policy decision: "+item.Decision)
			}
		}
	}
	conn, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	items, err := events.NewSQLLog(conn).Replay(context.Background(), 0, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		encoded, _ := json.Marshal(item.Data)
		if query == "" || strings.Contains(string(encoded), query) || strings.Contains(string(item.Type), query) || item.TaskID == query || item.GoalID == query {
			report.Events = append(report.Events, item)
		}
		if item.Type == events.SandboxDenied || item.Type == events.ToolFailed || item.Type == events.ModelFailed || item.Type == events.TaskBlocked {
			report.Constraints = appendUnique(report.Constraints, "constraint: "+string(item.Type))
		}
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(report.Actions) == 0 && len(report.Events) == 0 {
		fmt.Println("no persisted evidence found")
		return nil
	}
	fmt.Printf("actions: %d\nevidence events: %d\n", len(report.Actions), len(report.Events))
	for _, item := range report.Actions {
		fmt.Printf("action: %s actor=%s decision=%s resource=%s\n", item.Action, item.Actor, item.Decision, item.Resource)
	}
	for _, item := range report.Events {
		fmt.Printf("evidence: %s at=%s actor=%s\n", item.Type, item.At.Format("15:04:05Z07:00"), item.Actor)
	}
	for _, constraint := range report.Constraints {
		fmt.Printf("constraint: %s\n", constraint)
	}
	return nil
}}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func init() {
	whyCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(whyCmd)
}
