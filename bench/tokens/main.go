// Command tokens measures how many prompt tokens RSCH's optimizer saves.
//
// It replays a fixed set of agent trajectories (tool calls an agent makes
// for a task) against this repository and a small sample repository. Every
// tool output comes from running RSCH's real tools (view, grep, ls, glob,
// bash) on the files at measurement time. For each step the benchmark
// assembles the history the agent would send to the model, with the
// optimizer on and off, and counts tokens with tiktoken (o200k_base and
// cl100k_base) for the system prompt plus history. Tool definitions are
// sent unchanged in both modes and are not counted.
//
// No model is called, so the numbers are deterministic for a given commit.
// Run it with: go run ./bench/tokens  (see bench/tokens/README.md)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/optimizer"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/tiktoken-go/tokenizer"
)

type step struct {
	Tool  string
	Input map[string]any
}

type task struct {
	Name   string
	Repo   string // "rsch" or "sample"
	Prompt string
	Steps  []step
}

func view(path string) step { return step{"view", map[string]any{"file_path": path}} }
func grep(pattern, path string) step {
	return step{"grep", map[string]any{"pattern": pattern, "path": path, "include": "*.go"}}
}
func bash(command string) step { return step{"bash", map[string]any{"command": command}} }
func ls(path string) step      { return step{"ls", map[string]any{"path": path}} }
func glob(pattern string) step { return step{"glob", map[string]any{"pattern": pattern}} }

// tasks is the fixed task set. Re-reads (the same file or command twice) are
// included only where an agent typically repeats them: re-reading a file
// before editing it, and re-running tests after a change.
var tasks = []task{
	{"explain-background-stop", "rsch", "How are background jobs stopped? Explain the process-identity check.", []step{
		grep(`func \(m \*Manager\) Stop`, "internal"), view("internal/background/jobs.go"),
		grep("processIdentityMatches", "internal"), view("internal/background/process_identity_unix.go"),
		view("internal/background/jobs.go"),
	}},
	{"review-optimizer", "rsch", "Review the optimizer and its tests for gaps.", []step{
		bash("git log --oneline -15"), view("internal/optimizer/optimizer.go"),
		view("internal/optimizer/optimizer_test.go"), bash("grep -rn 'optimizer\\.' --include=*.go internal cmd"),
		view("internal/optimizer/optimizer.go"),
	}},
	{"summarize-repo", "rsch", "Summarize this repository for a new contributor.", []step{
		ls("."), view("README.md"), view("go.mod"), glob("cmd/*.go"), view("cmd/root.go"),
	}},
	{"recent-changes", "rsch", "What changed in the last few commits?", []step{
		bash("git log --stat -10"), bash("git show --stat HEAD~1"), view("internal/config/local_detect.go"),
	}},
	{"list-providers", "rsch", "Which model providers are supported and how is the local one wired?", []step{
		grep("case models.Provider", "internal"), view("internal/llm/provider/provider.go"),
		view("internal/llm/provider/openai.go"), view("internal/llm/models/local.go"),
	}},
	{"sample-explain", "sample", "Explain what this package does.", []step{
		ls("."), view("calc.go"), view("calc_test.go"),
	}},
	{"sample-test-and-fix", "sample", "Run the tests and make sure Divide is covered.", []step{
		bash("go test ./..."), view("calc.go"), view("calc_test.go"), bash("go test ./..."),
	}},
	{"sample-summarize", "sample", "Summarize this repository.", []step{
		view("README.md"), view("cmd/calc/main.go"),
	}},
}

type measurement struct {
	Task      string  `json:"task"`
	Repo      string  `json:"repo"`
	Requests  int     `json:"requests"`
	OffTokens int     `json:"offTokens"`
	OnTokens  int     `json:"onTokens"`
	Reduction float64 `json:"reductionPercent"`
	Trimmed   int     `json:"trimmed"`
	Deduped   int     `json:"deduplicated"`
	Compacted int     `json:"compacted"`
}

func main() {
	contextWindow := flag.Int64("context", 16384, "model context window used for compaction (tokens)")
	encoding := flag.String("encoding", "o200k_base", "tiktoken encoding: o200k_base or cl100k_base")
	jsonOut := flag.Bool("json", false, "print JSON instead of a table")
	only := flag.String("only", "", "internal: measure one task in this process and print its JSON")
	flag.Parse()

	root, err := os.Getwd()
	check(err)
	repos := map[string]string{"rsch": root, "sample": filepath.Join(root, "bench", "tokens", "testdata", "sample")}
	codec, err := tokenizer.Get(tokenizer.Encoding(*encoding))
	check(err)
	count := func(text string) int {
		ids, _, err := codec.Encode(text)
		check(err)
		return len(ids)
	}

	if *only != "" {
		for _, t := range tasks {
			if t.Name == *only {
				out, _ := json.Marshal(run(t, repos[t.Repo], *contextWindow, count))
				fmt.Println(string(out))
				return
			}
		}
		check(fmt.Errorf("unknown task %q", *only))
	}

	// Each task runs in its own process because RSCH's configuration (and
	// with it the working directory the tools use) is loaded once per process.
	self, err := os.Executable()
	check(err)
	results := make([]measurement, 0, len(tasks))
	for _, t := range tasks {
		child := exec.Command(self, "-only", t.Name, "-context", fmt.Sprint(*contextWindow), "-encoding", *encoding)
		child.Dir, child.Stderr = root, os.Stderr
		out, err := child.Output()
		check(err)
		var m measurement
		check(json.Unmarshal(out, &m))
		results = append(results, m)
	}

	commit, _ := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if *jsonOut {
		out, _ := json.MarshalIndent(map[string]any{"commit": strings.TrimSpace(string(commit)), "encoding": *encoding,
			"contextWindow": *contextWindow, "platform": runtime.GOOS + "/" + runtime.GOARCH, "tasks": results,
			"medianReductionPercent": median(results)}, "", "  ")
		fmt.Println(string(out))
		return
	}
	fmt.Printf("commit %s, %s/%s, encoding %s, context window %d, deterministic replay (no model calls)\n\n",
		strings.TrimSpace(string(commit)), runtime.GOOS, runtime.GOARCH, *encoding, *contextWindow)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "task\trepo\trequests\toff tokens\ton tokens\treduction\ttrimmed\tdeduped\tcompacted\t")
	for _, m := range results {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%.1f%%\t%d\t%d\t%d\t\n", m.Task, m.Repo, m.Requests, m.OffTokens, m.OnTokens, m.Reduction, m.Trimmed, m.Deduped, m.Compacted)
	}
	w.Flush()
	fmt.Printf("\nmedian reduction: %.1f%% (tokens are summed over every request in a task: system prompt + history)\n", median(results))
}

func run(t task, repo string, contextWindow int64, count func(string) int) measurement {
	check(os.Chdir(repo))
	_ = os.Setenv("RSCH_NO_LOCAL_DETECT", "1")
	_, err := config.Load(repo, false)
	check(err)
	system := prompt.CoderPrompt(models.ProviderLocal)
	systemTokens := count(system)
	if os.Getenv("BENCH_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "%s: system prompt %d tokens, cwd %s\n", t.Name, systemTokens, config.WorkingDirectory())
	}

	sessionID := "bench-" + t.Name
	permissions := permission.NewPermissionService()
	permissions.AutoApproveSession(sessionID)
	available := map[string]tools.BaseTool{
		"view": tools.NewViewTool(nil), "grep": tools.NewGrepTool(), "ls": tools.NewLsTool(),
		"glob": tools.NewGlobTool(), "bash": tools.NewBashTool(permissions),
	}
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "bench-message")

	history := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: t.Prompt}}}}
	opt := optimizer.New(optimizer.Options{})
	m := measurement{Task: t.Name, Repo: t.Repo}
	measure := func() {
		m.Requests++
		m.OffTokens += systemTokens + historyTokens(history, count)
		sent, stats := opt.Optimize(sessionID, history, contextWindow)
		m.OnTokens += systemTokens + historyTokens(sent, count)
		m.Trimmed += stats.Trimmed
		m.Deduped += stats.Deduplicated
		m.Compacted += stats.Compacted
	}
	for i, s := range t.Steps {
		measure() // the request that produces this tool call
		input, _ := json.Marshal(s.Input)
		id := fmt.Sprintf("call_%d", i+1)
		response, err := available[s.Tool].Run(ctx, tools.ToolCall{ID: id, Name: s.Tool, Input: string(input)})
		check(err)
		if os.Getenv("BENCH_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "%s step %d %s: %d bytes, error=%v: %.120q ... %.300q\n", t.Name, i+1, s.Tool, len(response.Content), response.IsError, response.Content, tail(response.Content))
		}
		history = append(history,
			message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: id, Name: s.Tool, Input: string(input), Finished: true}}},
			message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: s.Tool, Content: response.Content, IsError: response.IsError}}},
		)
	}
	measure() // the final request that produces the answer
	m.Reduction = 100 * float64(m.OffTokens-m.OnTokens) / float64(m.OffTokens)
	return m
}

func historyTokens(history []message.Message, count func(string) int) int {
	total := 0
	for _, msg := range history {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case message.TextContent:
				total += count(p.Text)
			case message.ToolCall:
				total += count(p.Name) + count(p.Input)
			case message.ToolResult:
				total += count(p.Content)
			}
		}
	}
	return total
}

func median(results []measurement) float64 {
	values := make([]float64, len(results))
	for i, m := range results {
		values[i] = m.Reduction
	}
	sort.Float64s(values)
	if len(values) == 0 {
		return 0
	}
	if len(values)%2 == 1 {
		return values[len(values)/2]
	}
	return (values[len(values)/2-1] + values[len(values)/2]) / 2
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench/tokens:", err)
		os.Exit(1)
	}
}

func tail(s string) string {
	if len(s) > 300 {
		return s[len(s)-300:]
	}
	return s
}
