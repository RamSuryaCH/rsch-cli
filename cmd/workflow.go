package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var tasksCmd = &cobra.Command{Use: "tasks [id]", Short: "Inspect persisted workflow tasks", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	items, err := engineering.NewSQLStore(conn).LoadTasks(context.Background(), "")
	if err != nil {
		return err
	}
	if len(args) == 1 {
		filtered := items[:0]
		for _, item := range items {
			if item.ID == args[0] {
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
		fmt.Println("no persisted tasks")
		return nil
	}
	for _, item := range items {
		fmt.Printf("%s [%s] %s goal=%s timeout=%s retries=%d deps=%s command=%s\n", item.ID, item.Status, item.Title, item.GoalID, item.Timeout, item.RetryLimit, strings.Join(item.Dependencies, ","), strings.Join(item.Command, " "))
	}
	return nil
}}

var taskShowCmd = &cobra.Command{Use: "show <id>", Short: "Show one persisted workflow task", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runTaskTransition(cmd, args[0], "") }}
var taskRetryCmd = &cobra.Command{Use: "retry <id>", Short: "Retry a failed or canceled workflow task", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runTaskTransition(cmd, args[0], engineering.StatusRetrying)
}}
var taskPauseCmd = &cobra.Command{Use: "pause <id>", Short: "Pause a running workflow task", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runTaskTransition(cmd, args[0], engineering.StatusPaused)
}}
var taskResumeCmd = &cobra.Command{Use: "resume <id>", Short: "Resume a paused workflow task", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runTaskTransition(cmd, args[0], engineering.StatusReady)
}}
var taskCancelCmd = &cobra.Command{Use: "cancel <id>", Short: "Cancel a workflow task", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runTaskTransition(cmd, args[0], engineering.StatusCanceled)
}}

func runTaskTransition(cmd *cobra.Command, id string, next engineering.Status) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	store := engineering.NewSQLStore(conn)
	var task engineering.Task
	if next == "" {
		items, loadErr := store.LoadTasks(cmd.Context(), "")
		if loadErr != nil {
			return loadErr
		}
		for _, item := range items {
			if item.ID == id {
				task = item
			}
		}
		if task.ID == "" {
			return fmt.Errorf("task %q not found", id)
		}
	} else {
		task, err = store.TransitionTask(cmd.Context(), id, next)
		if err != nil {
			return err
		}
		kind := taskEventTypeForCLI(next)
		event := events.New(kind, map[string]any{"status": next})
		event.GoalID, event.TaskID = task.GoalID, task.ID
		if err := events.NewSQLLog(conn).Append(cmd.Context(), event); err != nil {
			return err
		}
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(task, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%s [%s] %s\ngoal: %s\ndescription: %s\ncommand: %s\ntimeout: %s\nretries: %d\ndependencies: %s\nagent: %s\nverification: %s\n", task.ID, task.Status, task.Title, task.GoalID, task.Description, strings.Join(task.Command, " "), task.Timeout, task.RetryLimit, strings.Join(task.Dependencies, ", "), task.Agent, strings.Join(task.Verification, ", "))
	}
	return nil
}

func taskEventTypeForCLI(status engineering.Status) events.Type {
	switch status {
	case engineering.StatusCompleted, engineering.StatusVerified, engineering.StatusPassed:
		return events.TaskCompleted
	case engineering.StatusFailed:
		return events.TaskFailed
	case engineering.StatusCanceled:
		return events.TaskCanceled
	case engineering.StatusBlocked:
		return events.TaskBlocked
	case engineering.StatusRetrying:
		return events.TaskRetrying
	case engineering.StatusPaused:
		return events.TaskPaused
	default:
		return events.TaskStarted
	}
}

var agentsCmd = &cobra.Command{Use: "agents", Short: "Inspect active RSCH agents", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	items, err := orchestration.NewAgentStore(conn).List(context.Background())
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(items) == 0 {
		fmt.Println("no persisted agents")
		return nil
	}
	for _, item := range items {
		fmt.Printf("%s [%s] %s task=%s\n", item.ID, item.Status, item.Role, item.TaskID)
	}
	return nil
}}

var agentShowCmd = &cobra.Command{Use: "show <id>", Short: "Show one persisted agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	items, err := orchestration.NewAgentStore(conn).List(cmd.Context())
	if err != nil {
		return err
	}
	var found *orchestration.Agent
	for index := range items {
		if items[index].ID == args[0] {
			item := items[index]
			found = &item
			break
		}
	}
	if found == nil {
		return fmt.Errorf("agent %q not found", args[0])
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(found, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("id: %s\nrole: %s\nstatus: %s\ntask: %s\ngoal: %s\nmodel: %s\nprovider: %s\nskills: %s\ntools: %s\npermissions: %s\nrisk: %s\nbudget: %d\ntimeout: %s\nworktree: %s\ntokens: %d\ncost: %.6f\nduration: %s\nfiles: %s\nerrors: %s\nverification: %s\n", found.ID, found.Role, found.Status, found.TaskID, found.Goal, found.Model, found.Provider, strings.Join(found.Skills, ", "), strings.Join(found.Tools, ", "), strings.Join(found.Permissions, ", "), found.Risk, found.Budget, found.Timeout, found.Worktree, found.Tokens, found.Cost, found.Duration, strings.Join(found.Files, ", "), strings.Join(found.Errors, "; "), strings.Join(found.Verification, ", "))
	return nil
}}

var agentsCreateCmd = &cobra.Command{Use: "create <role>", Short: "Create a persisted agent contract", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	taskID, _ := cmd.Flags().GetString("task")
	agent := orchestration.NewAgent(args[0], taskID)
	agent.Goal, _ = cmd.Flags().GetString("goal")
	agent.Model, _ = cmd.Flags().GetString("model")
	agent.Provider, _ = cmd.Flags().GetString("provider")
	agent.Risk, _ = cmd.Flags().GetString("risk")
	agent.Budget, _ = cmd.Flags().GetInt64("budget")
	agent.Worktree, _ = cmd.Flags().GetString("worktree")
	agent.Skills, _ = cmd.Flags().GetStringSlice("skill")
	agent.Tools, _ = cmd.Flags().GetStringSlice("tool")
	agent.Permissions, _ = cmd.Flags().GetStringSlice("permission")
	timeout, _ := cmd.Flags().GetString("timeout")
	if timeout != "" {
		agent.Timeout, err = time.ParseDuration(timeout)
		if err != nil || agent.Timeout < 0 {
			return fmt.Errorf("timeout must be a non-negative duration")
		}
	}
	if agent.Budget < 0 {
		return fmt.Errorf("budget cannot be negative")
	}
	store := orchestration.NewAgentStore(conn)
	if loaded := config.Get(); loaded != nil && loaded.Resources.MaxAgents > 0 {
		if err := store.EnsureCapacity(cmd.Context(), loaded.Resources.MaxAgents, agent.ID, agent.Status); err != nil {
			return err
		}
	}
	if err := store.Save(cmd.Context(), agent); err != nil {
		return err
	}
	event := events.New(events.AgentCreated, map[string]any{"role": agent.Role, "taskId": agent.TaskID})
	event.Actor, event.TaskID = agent.ID, agent.TaskID
	if err := events.NewSQLLog(conn).Append(cmd.Context(), event); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(agent, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("created agent %s [%s] role=%s task=%s\n", agent.ID, agent.Status, agent.Role, agent.TaskID)
	}
	return nil
}}

func runAgentTransition(cmd *cobra.Command, id string, next orchestration.AgentStatus) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	store := orchestration.NewAgentStore(conn)
	items, err := store.List(cmd.Context())
	if err != nil {
		return err
	}
	var agent orchestration.Agent
	for _, item := range items {
		if item.ID == id {
			agent = item
			break
		}
	}
	if agent.ID == "" {
		return fmt.Errorf("agent %q not found", id)
	}
	if err := agent.Transition(next); err != nil {
		return err
	}
	if loaded := config.Get(); loaded != nil && loaded.Resources.MaxAgents > 0 {
		if err := store.EnsureCapacity(cmd.Context(), loaded.Resources.MaxAgents, agent.ID, agent.Status); err != nil {
			return err
		}
	}
	if err := store.Save(cmd.Context(), agent); err != nil {
		return err
	}
	event := events.New(agentEventTypeForCLI(next), map[string]any{"status": next})
	event.Actor, event.TaskID = agent.ID, agent.TaskID
	if err := events.NewSQLLog(conn).Append(cmd.Context(), event); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(agent, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%s [%s] %s\n", agent.ID, agent.Status, agent.Role)
	}
	return nil
}

func agentEventTypeForCLI(status orchestration.AgentStatus) events.Type {
	switch status {
	case orchestration.AgentPaused:
		return events.AgentPaused
	case orchestration.AgentResumed:
		return events.AgentResumed
	case orchestration.AgentCompleted:
		return events.AgentCompleted
	case orchestration.AgentFailed:
		return events.AgentFailed
	case orchestration.AgentCanceled:
		return events.AgentCanceled
	default:
		return events.AgentStarted
	}
}

var agentStartCmd = &cobra.Command{Use: "start <id>", Short: "Start a created or failed agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentStarting)
}}
var agentRunCmd = &cobra.Command{Use: "run <id>", Short: "Mark a starting agent running", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentRunning)
}}
var agentPauseCmd = &cobra.Command{Use: "pause <id>", Short: "Pause a running agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentPaused)
}}
var agentResumeCmd = &cobra.Command{Use: "resume <id>", Short: "Resume a paused agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentResumed)
}}
var agentCancelCmd = &cobra.Command{Use: "cancel <id>", Short: "Cancel an agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentCanceled)
}}
var agentRetryCmd = &cobra.Command{Use: "retry <id>", Short: "Retry a failed agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentStarting)
}}
var agentCompleteCmd = &cobra.Command{Use: "complete <id>", Short: "Complete a running agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentCompleted)
}}
var agentFailCmd = &cobra.Command{Use: "fail <id>", Short: "Mark a running agent failed", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return runAgentTransition(cmd, args[0], orchestration.AgentFailed)
}}
var agentsRecoverCmd = &cobra.Command{Use: "recover", Short: "Pause agents left running after an interrupted process", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	conn, err := db.ConnectAt(filepath.Join(cwd, ".rsch"))
	if err != nil {
		return err
	}
	defer conn.Close()
	store := orchestration.NewAgentStore(conn)
	items, err := store.List(cmd.Context())
	if err != nil {
		return err
	}
	recovered := make([]orchestration.Agent, 0)
	for _, agent := range items {
		if agent.Status != orchestration.AgentRunning {
			continue
		}
		agent.Status = orchestration.AgentPaused
		if err := store.Save(cmd.Context(), agent); err != nil {
			return err
		}
		event := events.New(events.AgentPaused, map[string]any{"status": agent.Status, "recovered": true})
		event.Actor, event.TaskID = agent.ID, agent.TaskID
		if err := events.NewSQLLog(conn).Append(cmd.Context(), event); err != nil {
			return err
		}
		recovered = append(recovered, agent)
	}
	data, _ := json.MarshalIndent(recovered, "", "  ")
	fmt.Println(string(data))
	return nil
}}

func init() {
	tasksCmd.Flags().Bool("json", false, "Output structured JSON")
	agentsCmd.Flags().Bool("json", false, "Output structured JSON")
	agentShowCmd.Flags().Bool("json", false, "Output structured JSON")
	for _, command := range []*cobra.Command{agentStartCmd, agentRunCmd, agentPauseCmd, agentResumeCmd, agentCancelCmd, agentRetryCmd, agentCompleteCmd, agentFailCmd} {
		command.Flags().Bool("json", false, "Output structured JSON")
	}
	for _, command := range []*cobra.Command{taskShowCmd, taskRetryCmd, taskPauseCmd, taskResumeCmd, taskCancelCmd} {
		command.Flags().Bool("json", false, "Output structured JSON")
	}
	tasksCmd.AddCommand(taskShowCmd, taskRetryCmd, taskPauseCmd, taskResumeCmd, taskCancelCmd)
	agentsCreateCmd.Flags().Bool("json", false, "Output structured JSON")
	agentsCreateCmd.Flags().String("task", "", "Task identifier")
	agentsCreateCmd.Flags().String("goal", "", "Goal identifier or description")
	agentsCreateCmd.Flags().String("model", "", "Preferred model")
	agentsCreateCmd.Flags().String("provider", "", "Preferred provider")
	agentsCreateCmd.Flags().String("risk", "", "Risk profile")
	agentsCreateCmd.Flags().Int64("budget", 0, "Token/cost budget metadata")
	agentsCreateCmd.Flags().String("timeout", "", "Agent timeout (for example 5m)")
	agentsCreateCmd.Flags().String("worktree", "", "Assigned worktree")
	agentsCreateCmd.Flags().StringSlice("skill", nil, "Skill (repeatable)")
	agentsCreateCmd.Flags().StringSlice("tool", nil, "Tool (repeatable)")
	agentsCreateCmd.Flags().StringSlice("permission", nil, "Permission (repeatable)")
	agentsCmd.AddCommand(agentsCreateCmd, agentShowCmd, agentStartCmd, agentRunCmd, agentPauseCmd, agentResumeCmd, agentCancelCmd, agentRetryCmd, agentCompleteCmd, agentFailCmd, agentsRecoverCmd)
	rootCmd.AddCommand(tasksCmd, agentsCmd)
}
