package completions

import (
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestFileCompletionProcessHelper(t *testing.T) {
	switch os.Getenv("RSCH_COMPLETION_PROCESS_HELPER") {
	case "empty":
		os.Exit(1)
	case "error":
		os.Exit(2)
	case "matches":
		fmt.Print("main.go\x00notes.md\x00")
		os.Exit(0)
	}
}

func TestRipgrepOnlyCompletions(t *testing.T) {
	for _, test := range []struct {
		mode      string
		want      []string
		wantError bool
	}{
		{mode: "empty"},
		{mode: "error", wantError: true},
		{mode: "matches", want: []string{"main.go"}},
	} {
		t.Run(test.mode, func(t *testing.T) {
			rgSpec := process.Spec{
				Argv:        []string{os.Args[0], "-test.run=^TestFileCompletionProcessHelper$"},
				Environment: map[string]string{"RSCH_COMPLETION_PROCESS_HELPER": test.mode},
			}
			matches, err := filesWithCommands("main", rgSpec, process.Spec{})
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, want error %v", err, test.wantError)
			}
			if !slices.Equal(matches, test.want) {
				t.Fatalf("matches = %v, want %v", matches, test.want)
			}
		})
	}
}
