package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

var goalCmd = &cobra.Command{Use: "goal <objective>", Short: "Persist an engineering goal for planning and execution", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	goal := engineering.PrepareGoal(engineering.Goal{ID: uuid.NewString(), Title: args[0], Description: args[0]})
	store := engineering.NewSQLStore(conn)
	if err := store.SaveGoal(context.Background(), goal); err != nil {
		return err
	}
	event := events.New(events.GoalCreated, map[string]any{"title": goal.Title})
	event.GoalID = goal.ID
	if err := events.NewSQLLog(conn).Append(context.Background(), event); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(goal, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("created goal %s: %s\n", goal.ID, goal.Title)
	}
	return nil
}}

var goalListCmd = &cobra.Command{Use: "list", Short: "List persisted engineering goals", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	goals := engineering.NewSQLStore(conn).Goals()
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(goals, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(goals) == 0 {
		fmt.Println("no persisted goals")
		return nil
	}
	for _, goal := range goals {
		fmt.Printf("%s [%s] %s\n", goal.ID, goal.Status, goal.Title)
	}
	return nil
}}

var goalShowCmd = &cobra.Command{Use: "show <id>", Short: "Show a goal with its requirements and task plan", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	store := engineering.NewSQLStore(conn)
	var goal engineering.Goal
	for _, item := range store.Goals() {
		if item.ID == args[0] {
			goal = item
			break
		}
	}
	if goal.ID == "" {
		return fmt.Errorf("goal %q not found", args[0])
	}
	requirements, err := store.Requirements(cmd.Context(), goal.ID)
	if err != nil {
		return err
	}
	tasks, err := store.LoadTasks(cmd.Context(), goal.ID)
	if err != nil {
		return err
	}
	evidence := make(map[string][]engineering.Evidence)
	for _, task := range tasks {
		items, evidenceErr := store.LoadEvidence(cmd.Context(), task.ID)
		if evidenceErr != nil {
			return evidenceErr
		}
		evidence[task.ID] = items
	}
	value := map[string]any{"goal": goal, "requirements": requirements, "tasks": tasks, "evidence": evidence}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("%s [%s] %s\n%s\n", goal.ID, goal.Status, goal.Title, goal.Description)
	fmt.Printf("requirements: %d\ntasks: %d\nevidence records: %d\n", len(requirements), len(tasks), countGoalEvidence(evidence))
	for _, task := range tasks {
		fmt.Printf("  %s [%s] %s\n", task.ID, task.Status, task.Title)
	}
	return nil
}}

func countGoalEvidence(items map[string][]engineering.Evidence) int {
	count := 0
	for _, records := range items {
		count += len(records)
	}
	return count
}

func init() {
	goalCmd.Flags().Bool("json", false, "Output structured JSON")
	goalListCmd.Flags().Bool("json", false, "Output structured JSON")
	goalShowCmd.Flags().Bool("json", false, "Output structured JSON")
	goalCmd.AddCommand(goalListCmd, goalShowCmd)
	rootCmd.AddCommand(goalCmd)
}
