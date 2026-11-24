package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
)

func TestWorkflowCommandDescriptions(t *testing.T) {
	commands := []*cobra.Command{
		jobsListCmd,
		jobsStopCmd,
		jobsRecoverCmd,
		customAgentListCmd,
		taskShowCmd,
		taskRetryCmd,
		taskPauseCmd,
		taskResumeCmd,
		taskCancelCmd,
		specNewCmd,
		specShowCmd,
		specValidateCmd,
		specRunCmd,
		specTaskAddCmd,
		specTraceCmd,
		specDriftCmd,
		specArchiveCmd,
		memoryAddCmd,
		memoryListCmd,
		memorySearchCmd,
		memoryRemoveCmd,
		memoryExplainCmd,
	}

	for _, command := range commands {
		command := command
		t.Run(command.CommandPath(), func(t *testing.T) {
			if strings.TrimSpace(command.Short) == "" {
				t.Fatalf("command %q must have a short description", command.CommandPath())
			}
		})
	}
}

func TestInspectionCommandsHonorOutputWriter(t *testing.T) {
	prepareInspectionWorkspace(t)
	service, closeDB, err := openSessionService(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.Create(context.Background(), "Inspection fixture")
	closeDB()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		source     *cobra.Command
		args       []string
		jsonOutput bool
	}{
		{"status JSON", statusCmd, nil, true},
		{"status text", statusCmd, nil, false},
		{"config show", configShowCmd, nil, true},
		{"config hierarchy JSON", configHierarchyCmd, nil, true},
		{"config hierarchy text", configHierarchyCmd, nil, false},
		{"sessions list JSON", sessionsListCmd, nil, true},
		{"sessions list text", sessionsListCmd, nil, false},
		{"sessions show JSON", sessionsShowCmd, []string{item.ID}, true},
		{"sessions show text", sessionsShowCmd, []string{item.ID}, false},
		{"resume JSON", resumeCmd, []string{item.ID}, true},
		{"resume text", resumeCmd, []string{item.ID}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := newInspectionTestCommand(test.source, test.jsonOutput)
			var output, diagnostics bytes.Buffer
			exit := executeCommand(context.Background(), command, test.args, strings.NewReader(""), &output, &diagnostics)
			if exit != 0 {
				t.Fatalf("exit = %d, stderr = %s", exit, diagnostics.String())
			}
			if output.Len() == 0 {
				t.Fatal("command bypassed the selected output writer")
			}
			if test.jsonOutput && !json.Valid(output.Bytes()) {
				t.Fatalf("output is not one JSON document: %s", output.String())
			}
			if strings.Contains(output.String(), "fixture-sensitive-value") || strings.Contains(output.String(), "fixture-header-value") {
				t.Fatal("configuration inspection exposed a credential fixture")
			}
			if diagnostics.Len() != 0 {
				t.Fatalf("successful command wrote diagnostics: %s", diagnostics.String())
			}

			command = newInspectionTestCommand(test.source, test.jsonOutput)
			diagnostics.Reset()
			exit = executeCommand(context.Background(), command, test.args, strings.NewReader(""), failingCLIOutput{}, &diagnostics)
			if exit != 1 {
				t.Fatalf("failed output write returned exit %d, want 1", exit)
			}
			if envelope := decodeCLIError(t, diagnostics.Bytes()); envelope.Code != "COMMAND_FAILED" || !strings.Contains(envelope.Message, "output unavailable") {
				t.Fatalf("unexpected write failure envelope: %+v", envelope)
			}
		})
	}
}

func TestEmptySessionListHonorsOutputWriter(t *testing.T) {
	prepareInspectionWorkspace(t)
	for _, jsonOutput := range []bool{false, true} {
		command := newInspectionTestCommand(sessionsListCmd, jsonOutput)
		var output, diagnostics bytes.Buffer
		exit := executeCommand(context.Background(), command, nil, strings.NewReader(""), &output, &diagnostics)
		if exit != 0 {
			t.Fatalf("exit = %d, stderr = %s", exit, diagnostics.String())
		}
		want := "no persisted sessions\n"
		if jsonOutput {
			want = "[]\n"
		}
		if output.String() != want {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
}

func TestCanceledInspectionCommandsDoNotInitializeStorage(t *testing.T) {
	root := prepareInspectionWorkspace(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(&terminationSignalError{signal: syscall.SIGTERM})
	for _, source := range []*cobra.Command{statusCmd, doctorCmd, sessionsListCmd, sessionsShowCmd, sessionsDeleteCmd, resumeCmd} {
		t.Run(source.Name(), func(t *testing.T) {
			command := newInspectionTestCommand(source, true)
			var args []string
			if source == sessionsShowCmd || source == sessionsDeleteCmd || source == resumeCmd {
				args = []string{"canceled-inspection-fixture"}
			}
			var output, diagnostics bytes.Buffer
			exit := executeCommand(ctx, command, args, strings.NewReader(""), &output, &diagnostics)
			if exit != 143 || output.Len() != 0 {
				t.Fatalf("canceled command returned exit=%d output=%q stderr=%s", exit, output.String(), diagnostics.String())
			}
			if envelope := decodeCLIError(t, diagnostics.Bytes()); envelope.Code != "TERMINATED" {
				t.Fatalf("cancellation cause was lost: %+v", envelope)
			}
			if _, err := os.Stat(filepath.Join(root, ".rsch")); !os.IsNotExist(err) {
				t.Fatalf("canceled command initialized storage: %v", err)
			}
		})
	}
}

func prepareInspectionWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	configuration := filepath.Join(root, "inspection-config.json")
	if err := os.WriteFile(configuration, []byte(`{"data":{"directory":".rsch"},"mcpServers":{"fixture":{"env":["TOKEN=fixture-sensitive-value"],"headers":{"Authorization":"Bearer fixture-header-value"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RSCH_SESSION_CONFIG", "")
	t.Setenv("RSCH_COMMAND_CONFIG", configuration)
	return root
}

func newInspectionTestCommand(source *cobra.Command, jsonOutput bool) *cobra.Command {
	command := &cobra.Command{Use: source.Use, Args: source.Args, RunE: source.RunE}
	command.Flags().Bool("json", jsonOutput, "JSON output")
	command.Flags().String("format", "text", "output format")
	return command
}

type failingCLIOutput struct{}

func (failingCLIOutput) Write([]byte) (int, error) {
	return 0, errors.New("output unavailable")
}

func TestNoArgumentCommandsRejectUnexpectedArguments(t *testing.T) {
	commands := []*cobra.Command{
		jobsListCmd,
		customAgentListCmd,
		agentsCmd,
		memoryListCmd,
	}

	for _, command := range commands {
		command := command
		t.Run(command.CommandPath(), func(t *testing.T) {
			if command.Args == nil {
				t.Fatalf("command %q must declare an argument contract", command.CommandPath())
			}
			if err := command.Args(command, nil); err != nil {
				t.Fatalf("command %q rejected an empty argument list: %v", command.CommandPath(), err)
			}
			if err := command.Args(command, []string{"unexpected"}); err == nil {
				t.Fatalf("command %q accepted an unexpected argument", command.CommandPath())
			}
		})
	}
}
