package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/performance"
	"github.com/spf13/cobra"
)

var performanceCmd = &cobra.Command{Use: "performance", Short: "Show runtime performance and budget capabilities", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	projectBudget, err := performance.LoadProjectBudget(root)
	if err != nil {
		return err
	}
	tracker := performance.New(performance.Budget{})
	usage := tracker.Snapshot()
	value := map[string]any{"platform": runtime.GOOS + "/" + runtime.GOARCH, "goRoutines": runtime.NumGoroutine(), "memory": runtime.MemStats{}, "usage": usage, "budget": projectBudget, "measurements": []performance.Measurement{}, "system": performance.InspectSystem(root)}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	value["memory"] = map[string]uint64{"alloc": stats.Alloc, "sys": stats.Sys, "heap": stats.HeapAlloc}
	value["io"] = performance.InspectIO(cmd.Context(), root)
	var database *sql.DB
	if opened, dbErr := db.ConnectAt(filepath.Join(root, ".rsch")); dbErr == nil {
		database = opened
		defer database.Close()
		value["database"] = performance.InspectDatabase(database)
	} else {
		value["database"] = performance.DatabaseReport{Reason: dbErr.Error()}
	}
	measureBuild, _ := cmd.Flags().GetBool("build")
	measureStartup, _ := cmd.Flags().GetBool("startup")
	checkBudgets, _ := cmd.Flags().GetBool("check-budgets")
	if checkBudgets {
		measureBuild = measureBuild || projectBudget.BuildTimeMS > 0
		measureStartup = measureStartup || projectBudget.StartupMS > 0
	}
	measurements := []performance.Measurement{}
	if measureBuild {
		measurements = append(measurements, performance.MeasureBuild(cmd.Context(), root, projectBudget.BuildTimeMS))
	}
	if measureStartup {
		measurements = append(measurements, performance.MeasureStartup(cmd.Context(), os.Args[0], projectBudget.StartupMS))
	}
	value["measurements"] = measurements
	budgetChecks := []performance.BudgetCheck{}
	budgetComplete := true
	if checkBudgets {
		budgetChecks = append(budgetChecks,
			measurementBudgetCheck("build", projectBudget.BuildTimeMS, measurements),
			measurementBudgetCheck("startup", projectBudget.StartupMS, measurements),
		)
		bundle := performance.InspectBundle(cmd.Context(), root)
		value["bundle"] = bundle
		budgetChecks = append(budgetChecks, performance.EvaluateBudget("bundle", projectBudget.BundleBytes, bundle.Bytes, bundle.Observed, bundle.Reason))
		budgetChecks = append(budgetChecks, performance.EvaluateBudget("memory", projectBudget.MemoryBytes, int64(stats.HeapAlloc), true, "runtime heap allocation observed"))
		databaseReport, _ := value["database"].(performance.DatabaseReport)
		budgetChecks = append(budgetChecks, performance.EvaluateBudget("query-count", projectBudget.QueryCount, databaseReport.QueryCount, databaseReport.QueryCountKnown, databaseReport.Reason))
		budgetChecks = append(budgetChecks, performance.EvaluateBudget("latency", projectBudget.LatencyMS, 0, false, "request latency requires an explicitly configured request target"))
		budgetComplete = budgetChecksComplete(budgetChecks)
	}
	value["budgetChecks"] = budgetChecks
	value["budgetComplete"] = budgetComplete
	evidenceStatus := "observed"
	if checkBudgets && !budgetComplete {
		evidenceStatus = "failed"
	}
	if err := recordEvidence(root, "performance", "rsch performance", evidenceStatus, "captured runtime performance evidence", "", value); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Fprintf(os.Stdout, "platform: %s\ngoroutines: %d\nheap: %d bytes\n", value["platform"], runtime.NumGoroutine(), stats.HeapAlloc)
	if system, ok := value["system"].(performance.SystemSnapshot); ok {
		fmt.Printf("cpu: logical=%d gomaxprocs=%d utilization-observed=%t\n", system.CPU.Logical, system.CPU.GOMAXPROCS, system.CPU.UtilizationObserved)
		if system.Disk.Observed {
			fmt.Printf("disk: free=%d total=%d\n", system.Disk.FreeBytes, system.Disk.TotalBytes)
		} else {
			fmt.Printf("disk: unavailable (%s)\n", system.Disk.Reason)
		}
		fmt.Printf("processes: observed=%t count=%d\nnetwork: observed=%t\n", system.Processes.Observed, system.Processes.Count, system.Network.Observed)
	}
	if ioReport, ok := value["io"].(performance.IOReport); ok {
		fmt.Printf("io: files=%d bytes=%d duration=%s\n", ioReport.Files, ioReport.Bytes, ioReport.Duration.Round(time.Millisecond))
	}
	if databaseReport, ok := value["database"].(performance.DatabaseReport); ok {
		fmt.Printf("database: connections=%d in-use=%d idle=%d query-count-known=%t\n", databaseReport.OpenConnections, databaseReport.InUse, databaseReport.Idle, databaseReport.QueryCountKnown)
	}
	if checkBudgets {
		fmt.Printf("budgets: complete=%t\n", budgetComplete)
		for _, check := range budgetChecks {
			state := "not configured"
			if check.Configured {
				state = "unavailable"
				if check.Observed && check.Within != nil {
					state = "within"
					if !*check.Within {
						state = "exceeded"
					}
				}
			}
			fmt.Printf("budget %s: %s", check.Name, state)
			if check.Reason != "" {
				fmt.Printf(" (%s)", check.Reason)
			}
			fmt.Println()
		}
	}
	if len(measurements) == 0 {
		fmt.Println("no build/startup measurement requested; use --build and/or --startup")
	}
	for _, measurement := range measurements {
		fmt.Printf("%s: %s", measurement.Kind, measurement.Duration.Round(time.Millisecond))
		if measurement.Within != nil {
			fmt.Printf(" within-budget=%t", *measurement.Within)
		}
		if measurement.Error != "" {
			fmt.Printf(" error=%s", measurement.Error)
		}
		fmt.Println()
	}
	if checkBudgets && !budgetComplete {
		return fmt.Errorf("configured performance budgets were not all observed within limits")
	}
	return nil
}}

func measurementBudgetCheck(name string, budget int64, measurements []performance.Measurement) performance.BudgetCheck {
	for _, measurement := range measurements {
		if measurement.Kind != name {
			continue
		}
		observed := measurement.Observed && measurement.Error == ""
		reason := measurement.Error
		if reason == "" {
			reason = "measurement completed"
		}
		return performance.EvaluateBudget(name, budget, measurement.Duration.Milliseconds(), observed, reason)
	}
	return performance.EvaluateBudget(name, budget, 0, false, "measurement was not requested")
}

func budgetChecksComplete(checks []performance.BudgetCheck) bool {
	for _, check := range checks {
		if !check.Configured {
			continue
		}
		if !check.Observed || check.Within == nil || !*check.Within {
			return false
		}
	}
	return true
}

func init() {
	performanceCmd.Flags().Bool("json", false, "Output structured JSON")
	performanceCmd.Flags().Bool("build", false, "Measure go build ./... duration")
	performanceCmd.Flags().Bool("startup", false, "Measure the RSCH binary startup path")
	performanceCmd.Flags().Bool("check-budgets", false, "Validate explicitly configured project performance budgets")
	rootCmd.AddCommand(performanceCmd)
}
