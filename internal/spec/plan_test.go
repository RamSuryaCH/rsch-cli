package spec

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestRunPlanUsesExecutionBoundary(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	if _, err := manager.New("safe-plan"); err != nil {
		t.Fatal(err)
	}
	command := []string{"true"}
	if runtime.GOOS == "windows" {
		command = []string{"cmd.exe", "/c", "exit", "0"}
	}
	if err := manager.SavePlan("safe-plan", Plan{Version: 1, Tasks: []PlanTask{{ID: "one", Title: "safe command", Command: command}}}); err != nil {
		t.Fatal(err)
	}
	results, err := manager.RunPlan(context.Background(), root, "safe-plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].ExitCode != 0 || results[0].Attempts != 1 {
		t.Fatalf("unexpected plan result: %+v", results)
	}
}

func TestPlanRejectsInvalidTasksBeforeSavingOrRunning(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Plan)
	}{
		{"unsupported version", func(p *Plan) { p.Version = 2 }},
		{"blank id", func(p *Plan) { p.Tasks[0].ID = " \t" }},
		{"blank title", func(p *Plan) { p.Tasks[0].Title = " \n" }},
		{"missing command", func(p *Plan) { p.Tasks[0].Command = nil }},
		{"blank executable", func(p *Plan) { p.Tasks[0].Command = []string{" "} }},
		{"invalid argument", func(p *Plan) { p.Tasks[0].Command = []string{"true", "bad\x00arg"} }},
		{"negative retry limit", func(p *Plan) { p.Tasks[0].RetryLimit = -1 }},
		{"negative timeout", func(p *Plan) { p.Tasks[0].Timeout = -time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manager := NewManager(root)
			if _, err := manager.New("feature"); err != nil {
				t.Fatal(err)
			}
			plan := Plan{Version: 1, Tasks: []PlanTask{{ID: "one", Title: "valid task", Command: []string{"true"}}}}
			test.edit(&plan)
			if err := manager.SavePlan("feature", plan); err == nil {
				t.Fatal("invalid plan saved")
			}
			saved, err := manager.LoadPlan("feature")
			if err != nil || len(saved.Tasks) != 0 {
				t.Fatalf("invalid save changed existing plan: %+v, %v", saved, err)
			}
			data, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			path, err := manager.planPath("feature")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.LoadPlan("feature"); err == nil {
				t.Fatal("invalid hand-edited plan loaded")
			}
			if results, err := manager.RunPlan(context.Background(), root, "feature"); err == nil || len(results) != 0 {
				t.Fatalf("invalid plan reached execution: %+v, %v", results, err)
			}
		})
	}
}

func TestSavePlanDefaultsVersionAndAllowsEmptyArguments(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.New("feature"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SavePlan("feature", Plan{Tasks: []PlanTask{{ID: "one", Title: "valid task", Command: []string{"printf", "%s", ""}}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := manager.LoadPlan("feature")
	if err != nil || plan.Version != 1 || plan.Tasks[0].Command[2] != "" {
		t.Fatalf("valid plan did not round trip: %+v, %v", plan, err)
	}
}
