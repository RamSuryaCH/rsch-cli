package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/opencode-ai/opencode/internal/background"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/feature"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/project"
	"github.com/opencode-ai/opencode/internal/sandbox"
	"github.com/spf13/cobra"
)

// status and doctor are deliberately read-only. They provide a safe machine
// and human inspection surface without starting the TUI or an agent session.
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show RSCH runtime and workspace status",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		result := map[string]any{"product": "RSCH CLI", "platform": runtime.GOOS + "/" + runtime.GOARCH, "cwd": cwd}
		hierarchyPaths := config.HierarchyPaths(cwd)
		result["configHierarchy"] = hierarchyPaths
		hierarchy, hierarchyErr := config.LoadHierarchy(hierarchyPaths...)
		if hierarchyErr != nil {
			result["configHierarchyValid"] = false
			result["configHierarchyError"] = hierarchyErr.Error()
		} else {
			result["configHierarchyValid"] = true
		}
		dataDirectory := ""
		if hierarchyErr == nil {
			if data, ok := hierarchy["data"].(map[string]any); ok {
				dataDirectory, _ = data["directory"].(string)
			}
		}
		dataDirectory = config.ResolveDataDirectory(cwd, dataDirectory)
		result["storage"] = dataDirectory
		if _, err := os.Stat(filepath.Join(cwd, "rsch.json")); err == nil {
			result["config"] = filepath.Join(cwd, "rsch.json")
		} else {
			result["config"] = "not found"
		}
		if conn, dbErr := db.OpenReadOnlyAt(dataDirectory); dbErr == nil {
			defer conn.Close()
			ctx := cmd.Context()
			result["goals"] = tableCount(ctx, conn, "goals")
			taskTotal := tableCount(ctx, conn, "workflow_tasks")
			result["tasks"] = taskTotal
			result["agents"] = tableCount(ctx, conn, "workflow_agents")
			result["sessions"] = tableCount(ctx, conn, "sessions")
			result["events"] = tableCount(ctx, conn, "event_log")
			var completedTasks int
			_ = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_tasks WHERE status IN ('completed','verified','passed')`).Scan(&completedTasks)
			progress := 0.0
			if taskTotal > 0 {
				progress = float64(completedTasks) / float64(taskTotal) * 100
			}
			result["progress"] = progress
			var totalCost float64
			_ = conn.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost), 0) FROM sessions`).Scan(&totalCost)
			result["cost"] = totalCost
			var goalTitle, goalStatus string
			if err := conn.QueryRowContext(ctx, `SELECT title,status FROM goals ORDER BY updated_at DESC LIMIT 1`).Scan(&goalTitle, &goalStatus); err == nil {
				result["goal"] = map[string]string{"title": goalTitle, "status": goalStatus}
			}
			if logEvents, eventErr := events.NewSQLLog(conn).Replay(ctx, 0, 100); eventErr == nil {
				blocked := 0
				for _, item := range logEvents {
					if item.Type == events.TaskBlocked || item.Type == events.TaskFailed || item.Type == events.AgentFailed || item.Type == events.ModelFailed {
						blocked++
					}
				}
				result["blockingErrors"] = blocked
				result["recentEvents"] = len(logEvents)
				if blocked > 0 {
					result["risk"] = "blocked"
				} else {
					result["risk"] = "normal"
				}
			}
		}
		if _, evalErr := os.Stat(filepath.Join(cwd, ".rsch", "evaluation", "latest.json")); evalErr == nil {
			result["verification"] = "evaluation evidence recorded"
		} else {
			result["verification"] = "no evaluation evidence recorded"
		}
		if jobs, listErr := readPersistedJobs(filepath.Join(cwd, ".rsch", "jobs")); listErr == nil {
			running := 0
			for _, job := range jobs {
				if job.Status == background.Running {
					running++
				}
			}
			result["jobs"] = len(jobs)
			result["currentOperation"] = running
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		if format, _ := cmd.Flags().GetString("format"); format == "json" || jsonOutput {
			return writeCLIJSON(cmd.OutOrStdout(), result)
		}
		for _, key := range []string{"product", "platform", "cwd", "config", "configHierarchyValid", "goal", "progress", "agents", "tasks", "sessions", "jobs", "currentOperation", "events", "blockingErrors", "risk", "verification", "cost"} {
			if value, ok := result[key]; ok {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %v\n", key, value); err != nil {
					return err
				}
			}
		}
		return nil
	},
}

func tableCount(ctx context.Context, conn *sql.DB, table string) int {
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		return 0
	}
	return count
}

func readPersistedJobs(root string) ([]background.Job, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	jobs := []background.Job{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var job background.Job
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run read-only local capability checks",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		capability := sandbox.Probe(cmd.Context(), cwd)
		checks := []doctorCheck{
			requiredDoctorCheck("installation", executableAvailable(), "RSCH executable"),
			requiredDoctorCheck("workspace-readable", cwd != "", cwd),
			requiredDoctorCheck("workspace-writable", isWritable(cwd), "workspace directory"),
			requiredDoctorCheck("rsch-config-valid-path", configPathIsSafe(cwd), "rsch.json"),
			requiredDoctorCheck("git", commandExists("git"), "git executable"),
			requiredDoctorCheck("runtime", true, runtime.Version()),
			optionalDoctorCheck("Bun", commandExists("bun"), "bun executable; optional for Go-only workflows"),
		}
		loaded, configErr := config.Load(cwd, false)
		storageDirectory := ".rsch"
		if loaded != nil {
			storageDirectory = loaded.Data.Directory
		}
		storageDirectory = config.ResolveDataDirectory(cwd, storageDirectory)
		resources, resourceErr := project.Discover(cwd)
		featureRegistry, featureErr := feature.Load(cwd)
		checks = append(checks,
			requiredDoctorCheck("configuration", configErr == nil, configErrorDetail(configErr)),
			requiredDoctorCheck("provider-configuration", configErr == nil, fmt.Sprintf("%d providers configured", providerCount(loaded))),
			requiredDoctorCheck("model-configuration", configErr == nil && agentCount(loaded) > 0, fmt.Sprintf("%d agents configured", agentCount(loaded))),
			requiredDoctorCheck("authentication", authenticationConfigured(loaded), authenticationDetail(loaded)),
			providerInUseCheck(loaded),
			requiredDoctorCheck("MCP-configuration", configErr == nil, fmt.Sprintf("%d MCP servers configured", mcpCount(loaded))),
			requiredDoctorCheck("storage", isWritableOrCreatable(storageDirectory), storageDirectory),
			optionalDoctorCheck("cache", isWritableOrCreatable(filepath.Join(storageDirectory, "cache")), filepath.Join(storageDirectory, "cache")+"; cache is created on demand"),
			requiredDoctorCheck("repository-index", true, indexEvidenceDetail(cwd)),
			requiredDoctorCheck("permissions", configErr == nil, "policy and permission configuration loaded"),
			requiredDoctorCheck("policy", configErr == nil, "policy configuration loaded"),
			requiredDoctorCheck("network", configErr == nil, networkDetail(loaded)),
			requiredDoctorCheck("project-resources", resourceErr == nil, resourceErrorDetail(resourceErr, resources)),
			requiredDoctorCheck("feature-registry", featureErr == nil || os.IsNotExist(featureErr), featureRegistryDetail(featureErr, featureRegistry)),
			optionalDoctorCheck("sandbox", capability.Isolated, sandboxDetail(capability)),
			optionalDoctorCheck("platform-capabilities", capability.State != sandbox.Unsupported, capability.Platform+" / "+string(capability.State)),
		)
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		failed := 0
		warnings := 0
		for _, check := range checks {
			state := "ok"
			if !check.OK {
				if check.Severity == "warning" {
					state = "warning"
					warnings++
				} else {
					state = "failed"
					failed++
				}
			}
			if !jsonOutput {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s)\n", check.Name, state, check.Detail); err != nil {
					return err
				}
			}
		}
		if jsonOutput {
			result := map[string]any{
				"product":   "RSCH CLI",
				"workspace": cwd,
				"platform": map[string]any{
					"os":       runtime.GOOS,
					"arch":     runtime.GOARCH,
					"runtime":  runtime.Version(),
					"compiler": runtime.Compiler,
					"cpuCount": runtime.NumCPU(),
				},
				"checks":       checks,
				"failedCount":  failed,
				"warningCount": warnings,
				"sandbox":      capability,
			}
			if err := writeCLIJSON(cmd.OutOrStdout(), result); err != nil {
				return err
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d capability check(s) failed", failed)
		}
		return nil
	},
}

type doctorCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"`
}

func providerInUseCheck(loaded *config.Config) doctorCheck {
	detail, ok := config.ActiveProvider(loaded)
	return requiredDoctorCheck("provider-in-use", ok, detail)
}

func requiredDoctorCheck(name string, ok bool, detail string) doctorCheck {
	return doctorCheck{Name: name, OK: ok, Detail: detail, Severity: "error"}
}

func optionalDoctorCheck(name string, ok bool, detail string) doctorCheck {
	return doctorCheck{Name: name, OK: ok, Detail: detail, Severity: "warning"}
}

func configErrorDetail(err error) string {
	if err != nil {
		return err.Error()
	}
	return "valid"
}

func resourceErrorDetail(err error, resources project.Resources) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("agents=%d skills=%d hooks=%d plugins=%d", len(resources.Agents), len(resources.Skills), len(resources.Hooks), len(resources.Plugins))
}

func featureRegistryDetail(err error, registry feature.Registry) string {
	if err != nil {
		if os.IsNotExist(err) {
			return "not initialized; run feature registry setup"
		}
		return err.Error()
	}
	return fmt.Sprintf("%d registered features", len(registry.Features))
}
func providerCount(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	count := 0
	for _, provider := range cfg.Providers {
		if !provider.Disabled {
			count++
		}
	}
	return count
}
func mcpCount(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	return len(cfg.MCPServers)
}

func agentCount(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	return len(cfg.Agents)
}

func authenticationConfigured(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for provider, value := range cfg.Providers {
		if value.Disabled {
			continue
		}
		if provider == "local" || strings.TrimSpace(value.APIKey) != "" {
			return true
		}
	}
	return false
}

func authenticationDetail(cfg *config.Config) string {
	if authenticationConfigured(cfg) {
		return "at least one enabled provider has credentials or uses a local runtime"
	}
	return "no enabled provider credentials found"
}

func networkDetail(cfg *config.Config) string {
	if cfg == nil {
		return "configuration unavailable"
	}
	return fmt.Sprintf("allowed hosts=%d private targets=%t timeout=%dms", len(cfg.Network.AllowedHosts), cfg.Network.AllowPrivate, cfg.Network.TimeoutMS)
}

func sandboxDetail(capability sandbox.Capability) string {
	if capability.Isolated {
		return fmt.Sprintf("%s via %s", capability.State, capability.Backend)
	}
	if capability.Reason != "" {
		return string(capability.State) + ": " + capability.Reason
	}
	return string(capability.State)
}
func indexEvidenceDetail(root string) string {
	if _, err := os.Stat(filepath.Join(root, ".rsch", "intelligence", "index.json")); err == nil {
		return "cache available"
	}
	return "not built; run rsch search or rsch architecture"
}

func executableAvailable() bool   { path, err := os.Executable(); return err == nil && path != "" }
func isWritable(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }

func isWritableOrCreatable(path string) bool {
	path = filepath.Clean(path)
	for {
		info, err := os.Stat(path)
		if err == nil {
			return info.IsDir() && isWritable(path)
		}
		if !os.IsNotExist(err) {
			return false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}
func commandExists(name string) bool { return process.Exists(name) }
func configPathIsSafe(cwd string) bool {
	path := filepath.Join(cwd, "rsch.json")
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() || os.IsNotExist(err)
}

func init() {
	statusCmd.Flags().String("format", "text", "Output format (text or json)")
	statusCmd.Flags().Bool("json", false, "Output structured JSON")
	doctorCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(statusCmd, doctorCmd)
}
