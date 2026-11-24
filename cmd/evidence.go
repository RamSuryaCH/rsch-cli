package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	rschevidence "github.com/opencode-ai/opencode/internal/evidence"
	"github.com/spf13/cobra"
)

func recordEvidence(root, kind, source, status, summary, reason string, value any) error {
	store, err := rschevidence.New(root)
	if err != nil {
		return err
	}
	var data []byte
	if value != nil {
		data, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	_, err = store.Record(rschevidence.Record{Kind: kind, Source: source, Status: rschevidence.Status(status), Summary: summary, Reason: reason, Data: data})
	return err
}

var evidenceCmd = &cobra.Command{
	Use:   "evidence",
	Short: "Inspect persisted analysis and verification evidence",
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		store, err := rschevidence.New(root)
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		kind, _ := cmd.Flags().GetString("kind")
		items, err := store.List(limit)
		if err != nil {
			return err
		}
		if kind != "" {
			filtered := items[:0]
			for _, item := range items {
				if item.Kind == kind {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			data, _ := json.MarshalIndent(items, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		if len(items) == 0 {
			fmt.Println("no persisted evidence")
			return nil
		}
		for _, item := range items {
			line := fmt.Sprintf("%s %s [%s] source=%s", item.CapturedAt.Format("2006-01-02T15:04:05Z07:00"), item.Kind, item.Status, item.Source)
			if strings.TrimSpace(item.Summary) != "" {
				line += " " + item.Summary
			}
			if strings.TrimSpace(item.Reason) != "" {
				line += " (" + item.Reason + ")"
			}
			fmt.Println(line)
		}
		return nil
	},
}

var evidenceLinkCmd = &cobra.Command{
	Use:   "link <evidence-id>",
	Short: "Attach evidence to a goal, task, or requirement",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		store, err := rschevidence.New(root)
		if err != nil {
			return err
		}
		goalID, _ := cmd.Flags().GetString("goal-id")
		taskID, _ := cmd.Flags().GetString("task-id")
		requirementID, _ := cmd.Flags().GetString("requirement-id")
		if goalID == "" && taskID == "" && requirementID == "" {
			return fmt.Errorf("one of --goal-id, --task-id, or --requirement-id is required")
		}
		link, err := store.CreateLink(rschevidence.Link{EvidenceID: args[0], GoalID: goalID, TaskID: taskID, RequirementID: requirementID})
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(link, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

var evidenceLinksCmd = &cobra.Command{
	Use:   "links",
	Short: "Inspect evidence links to workflow objects",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		store, err := rschevidence.New(root)
		if err != nil {
			return err
		}
		query := rschevidence.LinkQuery{}
		query.EvidenceID, _ = cmd.Flags().GetString("evidence-id")
		query.GoalID, _ = cmd.Flags().GetString("goal-id")
		query.TaskID, _ = cmd.Flags().GetString("task-id")
		query.RequirementID, _ = cmd.Flags().GetString("requirement-id")
		limit, _ := cmd.Flags().GetInt("limit")
		items, err := store.ListLinks(query, limit)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

func init() {
	evidenceCmd.Flags().Bool("json", false, "Output structured JSON")
	evidenceCmd.Flags().Int("limit", 100, "Maximum records to return; zero means unlimited")
	evidenceCmd.Flags().String("kind", "", "Filter by evidence kind")
	evidenceLinkCmd.Flags().String("goal-id", "", "Goal to attach the evidence to")
	evidenceLinkCmd.Flags().String("task-id", "", "Task to attach the evidence to")
	evidenceLinkCmd.Flags().String("requirement-id", "", "Requirement to attach the evidence to")
	evidenceLinksCmd.Flags().String("evidence-id", "", "Filter by evidence ID")
	evidenceLinksCmd.Flags().String("goal-id", "", "Filter by goal ID")
	evidenceLinksCmd.Flags().String("task-id", "", "Filter by task ID")
	evidenceLinksCmd.Flags().String("requirement-id", "", "Filter by requirement ID")
	evidenceLinksCmd.Flags().Int("limit", 100, "Maximum links to return; zero means unlimited")
	evidenceCmd.AddCommand(evidenceLinkCmd, evidenceLinksCmd)
	rootCmd.AddCommand(evidenceCmd)
}
