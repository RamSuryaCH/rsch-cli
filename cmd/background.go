package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/opencode-ai/opencode/internal/background"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/spf13/cobra"
)

func loadBackgroundConfig(root string) (*config.Config, error) {
	if config.Get() != nil {
		return config.Get(), nil
	}
	return config.Load(root, false)
}

func backgroundNotificationConfig(loaded *config.Config) notification.Config {
	return notification.Config{
		WebhookURL:      loaded.Notifications.WebhookURL,
		Command:         loaded.Notifications.Command,
		Args:            loaded.Notifications.Args,
		Timeout:         time.Duration(loaded.Notifications.TimeoutMS) * time.Millisecond,
		AllowedHosts:    loaded.Network.AllowedHosts,
		AllowPrivate:    loaded.Network.AllowPrivate,
		AllowedCommands: loaded.Policy.CommandAllow,
	}
}

var runCmd = &cobra.Command{Use: "run <prompt>", Short: "Run an engineering prompt in the foreground or background", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	backgroundMode, _ := cmd.Flags().GetBool("background")
	if !backgroundMode {
		return fmt.Errorf("use the root --prompt flag for foreground runs; run --background is required here")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	loaded, err := loadBackgroundConfig(root)
	if err != nil {
		return err
	}
	manager, err := background.NewManagerWithNotificationsAndLimit(root, backgroundNotificationConfig(loaded), loaded.Resources.MaxBackgroundJobs)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	timeoutMS, _ := cmd.Flags().GetInt("timeout-ms")
	if timeoutMS < 0 {
		return fmt.Errorf("timeout-ms cannot be negative")
	}
	job, err := manager.StartWithTimeout(context.Background(), executable, args[0], time.Duration(timeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(job, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var jobsCmd = &cobra.Command{Use: "jobs", Short: "Inspect and control background jobs"}
var jobsListCmd = &cobra.Command{Use: "list", Short: "List persisted background jobs", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	loaded, err := loadBackgroundConfig(root)
	if err != nil {
		return err
	}
	manager, err := background.NewManagerWithNotificationsAndLimit(root, backgroundNotificationConfig(loaded), loaded.Resources.MaxBackgroundJobs)
	if err != nil {
		return err
	}
	jobs, err := manager.List()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(jobs, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var jobsStopCmd = &cobra.Command{Use: "stop <id>", Short: "Stop a persisted background job", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	manager, err := background.NewManager(root)
	if err != nil {
		return err
	}
	return manager.Stop(args[0])
}}
var jobsRecoverCmd = &cobra.Command{Use: "recover <id>", Short: "Recover persisted state for a background job", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	manager, err := background.NewManager(root)
	if err != nil {
		return err
	}
	job, err := manager.Recover(args[0])
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(job, "", "  ")
	fmt.Println(string(data))
	return nil
}}

var attachCmd = &cobra.Command{Use: "attach <id>", Short: "Inspect a background job and its persisted log", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	manager, err := background.NewManager(root)
	if err != nil {
		return err
	}
	job, log, err := manager.ReadLog(args[0], 0)
	if err != nil {
		return err
	}
	fmt.Printf("job: %s\nstatus: %s\nlog: %s\n%s", job.ID, job.Status, job.LogPath, log)
	return nil
}}

var stopCmd = &cobra.Command{Use: "stop <id>", Short: "Stop a persisted background job", Args: cobra.ExactArgs(1), RunE: jobsStopCmd.RunE}

func init() {
	runCmd.Flags().Bool("background", false, "Run as a detached persisted job")
	runCmd.Flags().Int("timeout-ms", 0, "Stop the background process after this duration (0 means no timeout)")
	jobsCmd.AddCommand(jobsListCmd, jobsStopCmd, jobsRecoverCmd)
	rootCmd.AddCommand(runCmd, jobsCmd, attachCmd, stopCmd)
}
