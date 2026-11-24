package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/opencode-ai/opencode/internal/background"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/recovery"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

func loadTaskEvents(ctx context.Context, taskID string) ([]events.Event, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	items, err := events.NewSQLLog(conn).Replay(ctx, 0, 10000)
	if err != nil {
		return nil, err
	}
	filtered := make([]events.Event, 0)
	for _, item := range items {
		if taskID == "" || item.TaskID == taskID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

type replayReport struct {
	TaskID       string                `json:"taskId"`
	Tasks        []engineering.Task    `json:"tasks"`
	Agents       []orchestration.Agent `json:"agents"`
	Events       []events.Event        `json:"events"`
	Messages     []events.Event        `json:"messages"`
	ToolActions  []events.Event        `json:"toolActions"`
	Failures     []events.Event        `json:"failures"`
	Repairs      []events.Event        `json:"repairs"`
	Verification []events.Event        `json:"verification"`
	FinalResult  *events.Event         `json:"finalResult,omitempty"`
}

func loadTaskReplay(ctx context.Context, taskID string) (replayReport, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return replayReport{}, err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return replayReport{}, err
	}
	defer conn.Close()
	items, err := events.NewSQLLog(conn).Replay(ctx, 0, 10000)
	if err != nil {
		return replayReport{}, err
	}
	report := replayReport{TaskID: taskID, Tasks: []engineering.Task{}, Agents: []orchestration.Agent{}, Events: []events.Event{}, Messages: []events.Event{}, ToolActions: []events.Event{}, Failures: []events.Event{}, Repairs: []events.Event{}, Verification: []events.Event{}}
	for i := range items {
		item := items[i]
		if item.TaskID != taskID {
			continue
		}
		report.Events = append(report.Events, item)
		switch item.Type {
		case events.AgentMessage:
			report.Messages = append(report.Messages, item)
		case events.ToolBefore, events.ToolAfter, events.ToolFailed:
			report.ToolActions = append(report.ToolActions, item)
			if item.Type == events.ToolFailed {
				report.Failures = append(report.Failures, item)
			}
		case events.TaskFailed, events.AgentFailed, events.ModelFailed:
			report.Failures = append(report.Failures, item)
		case events.TaskStarted, events.AgentResumed:
			report.Repairs = append(report.Repairs, item)
		case events.TestStarted, events.TestPassed, events.TestFailed, events.SecurityScanStarted, events.SecurityScanCompleted:
			report.Verification = append(report.Verification, item)
		}
	}
	if len(report.Events) > 0 {
		report.FinalResult = &report.Events[len(report.Events)-1]
	}
	if tasks, err := engineering.NewSQLStore(conn).LoadTasks(ctx, ""); err == nil {
		for _, task := range tasks {
			if task.ID == taskID {
				report.Tasks = append(report.Tasks, task)
			}
		}
	}
	if agents, err := orchestration.NewAgentStore(conn).List(ctx); err == nil {
		for _, agent := range agents {
			if agent.TaskID == taskID {
				report.Agents = append(report.Agents, agent)
			}
		}
	}
	return report, nil
}

var replayCmd = &cobra.Command{Use: "replay <task-id>", Short: "Inspect a task execution replay without re-executing actions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	report, err := loadTaskReplay(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(report.Events) == 0 {
		fmt.Println("no replay events found")
		return nil
	}
	fmt.Printf("Task %s: %d events, %d agents, %d tool actions, %d failures, %d verification events\n", report.TaskID, len(report.Events), len(report.Agents), len(report.ToolActions), len(report.Failures), len(report.Verification))
	for _, item := range report.Events {
		fmt.Printf("%s %s actor=%s task=%s\n", item.At.Format("15:04:05Z07:00"), item.Type, item.Actor, item.TaskID)
	}
	return nil
}}
var recoverCmd = &cobra.Command{Use: "recover <task-id>", Short: "Create a safe, inspection-only recovery summary", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	items, err := loadTaskEvents(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	if len(items) == 0 {
		root, rootErr := os.Getwd()
		if rootErr == nil {
			if manager, managerErr := background.NewManager(root); managerErr == nil {
				job, recoverErr := manager.Recover(args[0])
				if recoverErr == nil {
					jsonOutput, _ := cmd.Flags().GetBool("json")
					if jsonOutput {
						data, _ := json.MarshalIndent(job, "", "  ")
						fmt.Println(string(data))
					} else {
						fmt.Printf("recovered background job %s [%s]\nlog: %s\n", job.ID, job.Status, job.LogPath)
					}
					return nil
				}
			}
		}
		return fmt.Errorf("no persisted state found for task or background job %q", args[0])
	}
	plan := recovery.Analyze(items, args[0])
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("Task %s has %d persisted events and %d tracked tool actions.\n", args[0], plan.EventCount, len(plan.Actions))
	fmt.Printf("Incomplete or failed actions: %d\n", len(plan.Incomplete))
	fmt.Println("Recovery is inspection-only; explicit re-execution requires reconstructed requests and the normal permission, policy, risk, approval, and sandbox pipeline.")
	return nil
}}

func init() {
	replayCmd.Flags().Bool("json", false, "Output structured JSON")
	recoverCmd.Flags().Bool("json", false, "Output a machine-readable recovery plan")
	rootCmd.AddCommand(replayCmd, recoverCmd)
}
