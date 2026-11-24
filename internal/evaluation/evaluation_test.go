package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
)

func TestRunSecurityFailsOnHighSeveritySecret(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.go")
	secret := "1234" + "56789012"
	source := "package config\nconst apiKey = \"" + secret + "\""
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	check := runSecurity(context.Background(), root)
	if check.Passed || securityFailureCount(check) != 1 {
		t.Fatalf("expected security failure, got %+v", check)
	}
}

func TestRunSecurityPassesCleanWorkspace(t *testing.T) {
	check := runSecurity(context.Background(), t.TempDir())
	if !check.Passed || securityFailureCount(check) != 0 {
		t.Fatalf("expected clean security result, got %+v", check)
	}
}

func TestRunSecurityRejectsIncompleteCoverage(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"oversized file", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "large.txt"), make([]byte, (2<<20)+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"file limit", func(t *testing.T, root string) {
			for i := 0; i <= 2000; i++ {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d.txt", i)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"unreadable file", func(t *testing.T, root string) {
			path := filepath.Join(root, "unreadable.txt")
			if err := os.WriteFile(path, []byte("unreadable"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			if _, err := os.ReadFile(path); err == nil {
				t.Skip("current user can read files without read permission")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			check := runSecurity(context.Background(), root)
			if check.Passed || check.Error == "" {
				t.Fatalf("incomplete security scan was reported as successful: %+v", check)
			}
			if check.Coverage == nil || check.Coverage.Complete {
				t.Fatalf("missing incomplete coverage metadata: %+v", check)
			}
		})
	}
}

func TestCollectTelemetryDoesNotInventUnavailableDimensions(t *testing.T) {
	started := time.Now().UTC()
	telemetry := collectTelemetry(t.TempDir(), started, started.Add(time.Second))
	if telemetry.TokensObserved || telemetry.CostObserved || telemetry.HumanInterventionObserved {
		t.Fatalf("unavailable telemetry was reported as observed: %+v", telemetry)
	}
	if telemetry.Scope != "workspace-time-window" {
		t.Fatalf("scope=%q", telemetry.Scope)
	}
}

func TestReportDoesNotClaimAgentSuccessOrRegressionRate(t *testing.T) {
	report := newReport(time.Now().UTC())
	report.RepositoryQualityPassed = true
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	for _, misleading := range []string{"taskSuccess", "regressionRate"} {
		if strings.Contains(encoded, misleading) {
			t.Fatalf("repository-quality report contains misleading %q field: %s", misleading, encoded)
		}
	}
	if report.EvaluationType != RepositoryQualityEvaluation || report.AgentTaskEvaluated || report.RegressionEvaluated {
		t.Fatalf("unexpected report semantics: %+v", report)
	}
	if report.RunID == "" || !strings.Contains(report.Artifact, report.RunID) {
		t.Fatalf("run identity is not reflected in artifact: %+v", report)
	}
}

func TestSaveCreatesImmutableRunArtifactAndLatestView(t *testing.T) {
	root := t.TempDir()
	report := newReport(time.Now().UTC())
	if err := Save(root, report); err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(root, filepath.FromSlash(report.Artifact))
	if _, err := os.Stat(runPath); err != nil {
		t.Fatalf("immutable artifact: %v", err)
	}
	latestPath := filepath.Join(root, ".rsch", "evaluation", "latest.json")
	if _, err := os.Stat(latestPath); err != nil {
		t.Fatalf("latest view: %v", err)
	}
	if err := Save(root, report); err == nil {
		t.Fatal("saving the same run id twice must not overwrite its artifact")
	}
}

func TestCollectTelemetryUsesOnlyRunWindowAndDoesNotCallAutoGrantHuman(t *testing.T) {
	root := t.TempDir()
	connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	started := time.Now().UTC().Truncate(time.Millisecond)
	finished := started.Add(2 * time.Second)
	if _, err := connection.Exec(`INSERT INTO sessions (id,parent_session_id,title,message_count,prompt_tokens,completion_tokens,cost,updated_at,created_at) VALUES ('in-run',NULL,'in run',0,0,0,0,?,?)`, started.Unix(), started.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO sessions (id,parent_session_id,title,message_count,prompt_tokens,completion_tokens,cost,updated_at,created_at) VALUES ('old',NULL,'old',0,0,0,0,?,?)`, started.Add(-time.Hour).Unix(), started.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO model_usage (id,session_id,task_id,agent_id,model,provider,input_tokens,output_tokens,cost,duration_ns,started_at,finished_at) VALUES ('usage-in','in-run','','','model','provider',10,20,0.5,1,?,?)`, started.UnixMilli(), finished.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO model_usage (id,session_id,task_id,agent_id,model,provider,input_tokens,output_tokens,cost,duration_ns,started_at,finished_at) VALUES ('usage-old','old','','','model','provider',100,200,5,1,?,?)`, started.Add(-time.Hour).UnixMilli(), started.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO event_log (id,version,type,occurred_at,data_json) VALUES ('event-in',1,'permission.auto_granted',?,'{}')`, started.Add(time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO event_log (id,version,type,occurred_at,data_json) VALUES ('event-old',1,'permission.granted',?,'{}')`, started.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	telemetry := collectTelemetry(root, started, finished)
	if telemetry.ModelObservations != 1 || telemetry.Sessions != 1 || telemetry.PromptTokens != 10 || telemetry.CompletionTokens != 20 || telemetry.Cost != 0.5 {
		t.Fatalf("run-scoped usage=%+v", telemetry)
	}
	if telemetry.EventCount != 1 || telemetry.PermissionDecisions != 1 {
		t.Fatalf("run-scoped events=%+v", telemetry)
	}
	if telemetry.HumanInterventionObserved {
		t.Fatalf("auto-grant was mislabeled as human intervention: %+v", telemetry)
	}
}
