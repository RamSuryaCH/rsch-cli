package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	xterm "github.com/charmbracelet/x/term"
	zone "github.com/lrstanley/bubblezone"
	application "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/background"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/spec"
	"github.com/opencode-ai/opencode/internal/tui"
	"github.com/opencode-ai/opencode/internal/version"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "rsch",
	Short: "RSCH AI software engineering operating system",
	Long: `RSCH is a provider-neutral AI software engineering operating system that helps with software development tasks.
It provides an interactive chat interface with AI capabilities, code analysis, and LSP integration
to assist developers in writing, debugging, and understanding code directly from the terminal.`,
	Example: `
  # Run in interactive mode
  rsch

  # Run with debug logging
  rsch -d

  # Run with debug logging in a specific directory
  rsch -d -c /path/to/project

  # Print version
  rsch -v

  # Run a single non-interactive prompt
  rsch exec "Explain the use of context in Go"

  # Emit versioned JSONL lifecycle records
  rsch exec "Explain the use of context in Go" -f jsonl

  # Validate the final response against a workspace-local JSON Schema
  rsch exec "Summarize the repository" --output-schema ./result.schema.json -f json

  # Explicitly opt in to unattended permission approval
  rsch -p "Run the tests" --auto-approve
  `,
	RunE: func(cmd *cobra.Command, args []string) (runErr error) {
		// If the help flag is set, show the help message
		if cmd.Flag("help").Changed {
			return cmd.Help()
		}
		if cmd.Flag("version").Changed {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.Version)
			return err
		}

		// Load the config
		debug, _ := cmd.Flags().GetBool("debug")
		cwd, _ := cmd.Flags().GetString("cwd")
		prompt, _ := cmd.Flags().GetString("prompt")
		jobID, _ := cmd.Flags().GetString("job-id")
		if cmd.Flags().Changed("prompt") && len(args) > 0 {
			return newUsageError("provide the prompt with either --prompt or positional arguments, not both")
		}
		promptProvided := cmd.Flags().Changed("prompt") || len(args) > 0
		if prompt == "" && len(args) > 0 {
			prompt = strings.Join(args, " ")
		}
		prompt = strings.TrimSpace(prompt)
		outputFormat, _ := cmd.Flags().GetString("output-format")
		outputSchemaPath, _ := cmd.Flags().GetString("output-schema")
		quiet, _ := cmd.Flags().GetBool("quiet")
		autoApprove, _ := cmd.Flags().GetBool("auto-approve")
		yes, _ := cmd.Flags().GetBool("yes")
		noInput, _ := cmd.Flags().GetBool("no-input")

		// Validate format option
		if !format.IsValid(outputFormat) {
			return newUsageError("invalid format option: %s\n%s", outputFormat, format.GetHelpText())
		}
		parsedOutputFormat, _ := format.Parse(outputFormat)

		// A redirected stdin is an explicit non-interactive invocation. Read it as
		// the prompt instead of trying to open the TUI in a headless environment.
		if strings.TrimSpace(prompt) == "" {
			if noInput {
				return newUsageError("no prompt provided while --no-input is enabled")
			}
			if promptProvided {
				return newUsageError("prompt must not be empty")
			}
			stdinPrompt, redirected, err := readPromptInput(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if redirected {
				if stdinPrompt == "" {
					return newUsageError("no prompt provided on non-interactive stdin")
				}
				prompt = stdinPrompt
			}
		}

		// PersistentPreRunE applies --cwd for root and subcommands alike. Use
		// the resolved process directory so relative paths are not applied twice.
		c, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %v", err)
		}
		cwd = c
		var jobManager *background.Manager
		if jobID != "" {
			jobManager, _ = background.NewManager(cwd)
			defer func() {
				if jobManager != nil {
					status := background.Completed
					if runErr != nil {
						status = background.Failed
					}
					_ = jobManager.Mark(jobID, status, runErr)
				}
			}()
		}
		loadedConfig, err := config.Load(cwd, debug)
		if err != nil {
			return err
		}
		if prompt != "" {
			// Say which provider runs the prompt. Stderr keeps stdout clean for
			// --output-format json and piping.
			if detail, ok := config.ActiveProvider(loadedConfig); ok {
				fmt.Fprintf(cmd.ErrOrStderr(), "rsch: using %s\n", detail)
			}
		}
		var outputSchema *format.OutputSchema
		if strings.TrimSpace(outputSchemaPath) != "" {
			if prompt == "" {
				return newUsageError("--output-schema requires a non-interactive prompt")
			}
			resolvedSchema, resolveErr := security.ResolveWorkspacePath(cwd, outputSchemaPath)
			if resolveErr != nil {
				return newUsageError("invalid output schema path: %v", resolveErr)
			}
			outputSchema, err = format.LoadOutputSchema(resolvedSchema)
			if err != nil {
				return newUsageError("%v", err)
			}
		}
		// Apply the validated global plan concurrency limit to executable specs.
		spec.MaxConcurrency = config.Get().Resources.MaxConcurrentTools

		// Connect DB, this will also run migrations
		conn, err := db.Connect()
		if err != nil {
			return err
		}

		// Inherit the signal-aware command context in both interactive and
		// non-interactive modes so cancellation reaches model and tool work.
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()

		app, err := application.New(ctx, conn)
		if err != nil {
			logging.Error("Failed to create app: %v", err)
			return err
		}
		// Defer shutdown here so it runs for both interactive and non-interactive modes
		defer app.Shutdown()

		// Initialize MCP tools early for both modes
		initMCPTools(ctx, app)

		// Non-interactive mode
		if prompt != "" {
			// Run non-interactive flow using the App method
			runErr = app.RunNonInteractiveWithOptions(ctx, prompt, application.NonInteractiveOptions{
				OutputFormat: parsedOutputFormat,
				OutputSchema: outputSchema,
				Stdout:       cmd.OutOrStdout(),
				Stderr:       cmd.ErrOrStderr(),
				Quiet:        quiet,
				AutoApprove:  autoApprove || yes,
			})
			if ctx.Err() != nil {
				if cause := context.Cause(ctx); cause != nil {
					return cause
				}
				return ctx.Err()
			}
			return runErr
		}

		// Interactive mode
		// Set up the TUI
		zone.NewGlobal()
		program := tea.NewProgram(
			tui.New(app),
			tea.WithAltScreen(),
			tea.WithContext(ctx),
		)

		// Setup the subscriptions, this will send services events to the TUI
		ch, cancelSubs := setupSubscriptions(app, ctx)

		// Create a context for the TUI message handler
		tuiCtx, tuiCancel := context.WithCancel(ctx)
		var tuiWg sync.WaitGroup
		tuiWg.Add(1)

		// Set up message handling for the TUI
		go func() {
			defer tuiWg.Done()
			defer logging.RecoverPanic("TUI-message-handler", func() {
				attemptTUIRecovery(program)
			})

			for {
				select {
				case <-tuiCtx.Done():
					logging.Info("TUI message handler shutting down")
					return
				case msg, ok := <-ch:
					if !ok {
						logging.Info("TUI message channel closed")
						return
					}
					program.Send(msg)
				}
			}
		}()

		// Cleanup function for when the program exits
		cleanup := func() {
			// Shutdown the app
			app.Shutdown()

			// Cancel subscriptions first
			cancelSubs()

			// Then cancel TUI message handler
			tuiCancel()

			// Wait for TUI message handler to finish
			tuiWg.Wait()

			logging.Info("All goroutines cleaned up")
		}

		// Run the TUI
		result, err := program.Run()
		cleanup()

		if err != nil {
			logging.Error("TUI error: %v", err)
			return fmt.Errorf("TUI error: %v", err)
		}

		logging.Info("TUI exited with result: %v", result)
		return nil
	},
}

const maxStdinPromptBytes = 1 << 20

type cliUsageError struct {
	err error
}

func (e *cliUsageError) Error() string { return e.err.Error() }
func (e *cliUsageError) Unwrap() error { return e.err }

func newUsageError(formatString string, args ...any) error {
	return &cliUsageError{err: fmt.Errorf(formatString, args...)}
}

// readPromptInput reports redirected=false for an interactive terminal. Other
// readers are bounded before being accepted as a non-interactive prompt.
func readPromptInput(input io.Reader) (prompt string, redirected bool, err error) {
	if input == nil {
		return "", true, nil
	}
	if file, ok := input.(*os.File); ok {
		if xterm.IsTerminal(file.Fd()) {
			return "", false, nil
		}
	} else if file, ok := input.(interface{ Stat() (os.FileInfo, error) }); ok {
		info, statErr := file.Stat()
		if statErr != nil {
			return "", true, fmt.Errorf("inspect stdin: %w", statErr)
		}
		if info.Mode()&os.ModeCharDevice != 0 {
			return "", false, nil
		}
	}

	data, readErr := io.ReadAll(io.LimitReader(input, maxStdinPromptBytes+1))
	if readErr != nil {
		return "", true, fmt.Errorf("read prompt from stdin: %w", readErr)
	}
	if len(data) > maxStdinPromptBytes {
		return "", true, newUsageError("stdin prompt exceeds %d bytes", maxStdinPromptBytes)
	}
	if !utf8.Valid(data) {
		return "", true, newUsageError("stdin prompt must be valid UTF-8")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", true, newUsageError("stdin prompt must not contain NUL bytes")
	}
	return strings.TrimSpace(string(data)), true, nil
}

// attemptTUIRecovery tries to recover the TUI after a panic
func attemptTUIRecovery(program *tea.Program) {
	logging.Info("Attempting to recover TUI after panic")

	// We could try to restart the TUI or gracefully exit
	// For now, we'll just quit the program to avoid further issues
	program.Quit()
}

func initMCPTools(ctx context.Context, app *application.App) {
	go func() {
		defer logging.RecoverPanic("MCP-goroutine", nil)

		// Create a context with timeout for the initial MCP tools fetch
		ctxWithTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		// Set this up once with proper error handling
		agent.GetMcpTools(ctxWithTimeout, app.Permissions)
		logging.Info("MCP message handling goroutine exiting")
	}()
}

const (
	tuiEventBufferSize        = 100
	tuiCoalescedEventLimit    = 256
	tuiPriorityReserve        = 16
	tuiCoalescedRetryInterval = 5 * time.Millisecond
)

// tuiEventQueue separates lossless control and terminal events from bounded
// streaming updates. Permission prompts and terminal state must remain queued
// until the TUI accepts them or its context is cancelled. Intermediate message
// and agent progress can be replaced by a newer snapshot for the same key.
type tuiEventQueue struct {
	mu sync.Mutex

	reliable       []tea.Msg
	coalesced      map[string]tea.Msg
	coalescedOrder []string
	coalescedLimit int
	wake           chan struct{}
}

func newTUIEventQueue(coalescedLimit int) *tuiEventQueue {
	if coalescedLimit <= 0 {
		coalescedLimit = tuiCoalescedEventLimit
	}
	return &tuiEventQueue{
		coalesced:      make(map[string]tea.Msg),
		coalescedLimit: coalescedLimit,
		wake:           make(chan struct{}, 1),
	}
}

func (q *tuiEventQueue) enqueueReliable(msg tea.Msg) {
	q.mu.Lock()
	q.reliable = append(q.reliable, msg)
	q.mu.Unlock()
	q.signal()
}

func (q *tuiEventQueue) enqueueCoalesced(key string, msg tea.Msg) {
	if key == "" {
		q.enqueueReliable(msg)
		return
	}

	q.mu.Lock()
	if _, exists := q.coalesced[key]; exists {
		for i, queuedKey := range q.coalescedOrder {
			if queuedKey == key {
				q.coalescedOrder = append(q.coalescedOrder[:i], q.coalescedOrder[i+1:]...)
				break
			}
		}
	} else {
		if len(q.coalesced) >= q.coalescedLimit {
			oldest := q.coalescedOrder[0]
			q.coalescedOrder = q.coalescedOrder[1:]
			delete(q.coalesced, oldest)
		}
	}
	q.coalescedOrder = append(q.coalescedOrder, key)
	q.coalesced[key] = msg
	q.mu.Unlock()
	q.signal()
}

func (q *tuiEventQueue) discardCoalesced(key string) {
	if key == "" {
		return
	}
	q.mu.Lock()
	delete(q.coalesced, key)
	for i, queuedKey := range q.coalescedOrder {
		if queuedKey == key {
			q.coalescedOrder = append(q.coalescedOrder[:i], q.coalescedOrder[i+1:]...)
			break
		}
	}
	q.mu.Unlock()
}

func (q *tuiEventQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *tuiEventQueue) dequeue(allowCoalesced bool) (tea.Msg, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.reliable) > 0 {
		msg := q.reliable[0]
		q.reliable[0] = nil
		q.reliable = q.reliable[1:]
		return msg, true
	}
	if !allowCoalesced {
		return nil, false
	}
	for len(q.coalescedOrder) > 0 {
		key := q.coalescedOrder[0]
		q.coalescedOrder = q.coalescedOrder[1:]
		msg, ok := q.coalesced[key]
		if !ok {
			continue
		}
		delete(q.coalesced, key)
		return msg, true
	}
	return nil, false
}

func (q *tuiEventQueue) hasCoalesced() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.coalesced) > 0
}

func (q *tuiEventQueue) run(ctx context.Context, outputCh chan<- tea.Msg) {
	for {
		allowCoalesced := coalescedOutputAvailable(outputCh)
		msg, ok := q.dequeue(allowCoalesced)
		if ok {
			select {
			case outputCh <- msg:
				continue
			case <-ctx.Done():
				return
			}
		}

		if q.hasCoalesced() && !allowCoalesced {
			select {
			case <-q.wake:
			case <-time.After(tuiCoalescedRetryInterval):
			case <-ctx.Done():
				return
			}
			continue
		}

		select {
		case <-q.wake:
		case <-ctx.Done():
			return
		}
	}
}

func coalescedOutputAvailable(outputCh chan<- tea.Msg) bool {
	capacity := cap(outputCh)
	if capacity == 0 {
		return true
	}
	if capacity == 1 {
		return len(outputCh) < capacity
	}
	reserve := min(tuiPriorityReserve, max(1, capacity/4))
	return len(outputCh) < capacity-reserve
}

func enqueueMessageEvent(queue *tuiEventQueue, event pubsub.Event[message.Message]) {
	payload := event.Payload
	key := "message:" + payload.ID
	if event.Type == pubsub.UpdatedEvent && payload.State == message.StatePending && payload.ID != "" {
		queue.enqueueCoalesced(key, event)
		return
	}
	queue.discardCoalesced(key)
	queue.enqueueReliable(event)
}

func enqueueAgentEvent(queue *tuiEventQueue, event pubsub.Event[agent.AgentEvent]) {
	payload := event.Payload
	key := "agent:" + payload.SessionID + ":" + string(payload.Type)
	if !payload.Done && payload.Error == nil && payload.SessionID != "" {
		queue.enqueueCoalesced(key, event)
		return
	}
	queue.discardCoalesced(key)
	queue.enqueueReliable(event)
}

func setupSubscriber[T any](
	ctx context.Context,
	wg *sync.WaitGroup,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
	enqueue func(pubsub.Event[T]),
) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer logging.RecoverPanic(fmt.Sprintf("subscription-%s", name), nil)

		subCh := subscriber(ctx)

		for {
			select {
			case event, ok := <-subCh:
				if !ok {
					logging.Info("subscription channel closed", "name", name)
					return
				}

				enqueue(event)
			case <-ctx.Done():
				logging.Info("subscription cancelled", "name", name)
				return
			}
		}
	}()
}

func setupSubscriptions(app *application.App, parentCtx context.Context) (chan tea.Msg, func()) {
	ch := make(chan tea.Msg, tuiEventBufferSize)

	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(parentCtx) // Inherit from parent context
	queue := newTUIEventQueue(tuiCoalescedEventLimit)
	wg.Add(1)
	go func() {
		defer wg.Done()
		queue.run(ctx, ch)
	}()

	setupSubscriber(ctx, &wg, "logging", logging.Subscribe, func(event pubsub.Event[logging.LogMessage]) {
		queue.enqueueReliable(event)
	})
	setupSubscriber(ctx, &wg, "sessions", app.Sessions.Subscribe, func(event pubsub.Event[session.Session]) {
		queue.enqueueReliable(event)
	})
	setupSubscriber(ctx, &wg, "messages", app.Messages.Subscribe, func(event pubsub.Event[message.Message]) {
		enqueueMessageEvent(queue, event)
	})
	setupSubscriber(ctx, &wg, "permissions", app.Permissions.Subscribe, func(event pubsub.Event[permission.PermissionRequest]) {
		queue.enqueueReliable(event)
	})
	setupSubscriber(ctx, &wg, "coderAgent", app.CoderAgent.Subscribe, func(event pubsub.Event[agent.AgentEvent]) {
		enqueueAgentEvent(queue, event)
	})
	var eventCancel func()
	if app.EventBus != nil {
		var eventStream <-chan events.Event
		eventStream, eventCancel = app.EventBus.Subscribe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer logging.RecoverPanic("subscription-events", nil)
			for {
				select {
				case event, ok := <-eventStream:
					if !ok {
						return
					}
					queue.enqueueReliable(event)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	cleanupFunc := func() {
		logging.Info("Cancelling all subscriptions")
		cancel() // Signal all goroutines to stop
		if eventCancel != nil {
			eventCancel()
		}

		waitCh := make(chan struct{})
		go func() {
			defer logging.RecoverPanic("subscription-cleanup", nil)
			wg.Wait()
			close(waitCh)
		}()

		select {
		case <-waitCh:
			logging.Info("All subscription goroutines completed successfully")
			close(ch) // Only close after all writers are confirmed done
		case <-time.After(5 * time.Second):
			logging.Warn("Timed out waiting for some subscription goroutines to complete")
			// A timed-out producer may still be running. Leave the channel open so it
			// cannot panic by racing a final send; the TUI handler has its own
			// cancellation path and will stop independently.
		}
	}
	return ch, cleanupFunc
}

type cliErrorEnvelope struct {
	Error      bool   `json:"error"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

type terminationSignalError struct {
	signal os.Signal
}

func (e *terminationSignalError) Error() string {
	return fmt.Sprintf("command interrupted by %s", e.signal)
}

func (e *terminationSignalError) Unwrap() error { return context.Canceled }

func Execute() {
	ctx, cancel := context.WithCancelCause(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case received := <-signals:
			cancel(&terminationSignalError{signal: received})
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(signals)
		close(done)
		cancel(nil)
	}()

	if exitCode := executeCommand(ctx, rootCmd, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); exitCode != 0 {
		os.Exit(exitCode)
	}
}

// executeCommand owns the process-facing CLI contract while remaining
// testable without calling os.Exit. Command data is written to stdout and the
// single structured error envelope is written to stderr.
func executeCommand(ctx context.Context, command *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) (exitCode int) {
	command.SetArgs(args)
	command.SetIn(stdin)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SilenceErrors = true
	command.SilenceUsage = true

	defer func() {
		if recovered := recover(); recovered != nil {
			exitCode = 1
			writeCLIError(stderr, cliErrorEnvelope{
				Error:      true,
				Code:       "INTERNAL_PANIC",
				Message:    fmt.Sprintf("internal command failure: %v", recovered),
				Suggestion: "Retry with --debug and inspect the persisted panic log.",
			})
		}
	}()

	err := command.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	if ctx != nil && ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
	}

	exitCode, envelope := classifyCLIError(err)
	writeCLIError(stderr, envelope)
	return exitCode
}

func classifyCLIError(err error) (int, cliErrorEnvelope) {
	var signalErr *terminationSignalError
	if errors.As(err, &signalErr) {
		exitCode := 130
		code := "INTERRUPTED"
		message := "command interrupted"
		if signalErr.signal == syscall.SIGTERM {
			exitCode = 143
			code = "TERMINATED"
			message = "command terminated"
		}
		return exitCode, cliErrorEnvelope{
			Error:      true,
			Code:       code,
			Message:    message,
			Suggestion: "Retry the command; no successful result was produced.",
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return 124, cliErrorEnvelope{
			Error:      true,
			Code:       "TIMED_OUT",
			Message:    "command timed out",
			Suggestion: "Retry with a smaller task or a larger configured timeout.",
		}
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, agent.ErrRequestCancelled) {
		return 130, cliErrorEnvelope{
			Error:      true,
			Code:       "INTERRUPTED",
			Message:    "command interrupted",
			Suggestion: "Retry the command; no successful result was produced.",
		}
	}

	if errors.Is(err, permission.ErrorPermissionDenied) || errors.Is(err, execution.ErrDenied) {
		return 11, cliErrorEnvelope{
			Error:      true,
			Code:       "PERMISSION_DENIED",
			Message:    err.Error(),
			Suggestion: "Review policy and rerun only with the minimum explicit approval required.",
		}
	}

	if errors.Is(err, format.ErrOutputSchemaMismatch) {
		return 1, cliErrorEnvelope{
			Error:      true,
			Code:       "OUTPUT_SCHEMA_MISMATCH",
			Message:    err.Error(),
			Suggestion: "Refine the prompt or schema and retry; invalid structured output was not accepted.",
		}
	}

	if isUsageError(err) {
		return 2, cliErrorEnvelope{
			Error:      true,
			Code:       usageErrorCode(err),
			Message:    err.Error(),
			Suggestion: "Run 'rsch --help' or 'rsch <command> --help' for usage.",
		}
	}

	return 1, cliErrorEnvelope{
		Error:      true,
		Code:       "COMMAND_FAILED",
		Message:    err.Error(),
		Suggestion: "Retry with --debug or inspect the command's prerequisites.",
	}
}

func isUsageError(err error) bool {
	var target *cliUsageError
	if errors.As(err, &target) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"unknown flag:",
		"unknown shorthand flag:",
		"unknown command ",
		"flag needs an argument:",
		"required flag(s)",
		"invalid argument ",
		"accepts ",
		"requires at least ",
		"requires at most ",
		"requires exactly ",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func usageErrorCode(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unknown flag:"), strings.Contains(message, "unknown shorthand flag:"):
		return "UNKNOWN_FLAG"
	case strings.Contains(message, "unknown command "):
		return "UNKNOWN_COMMAND"
	case strings.Contains(message, "requires "), strings.Contains(message, "required flag(s)"), strings.Contains(message, "flag needs an argument:"), strings.Contains(message, "accepts "):
		return "MISSING_REQUIRED"
	default:
		return "INVALID_ARGUMENT"
	}
}

func writeCLIError(stderr io.Writer, envelope cliErrorEnvelope) {
	if stderr == nil {
		return
	}
	_ = json.NewEncoder(stderr).Encode(envelope)
}

func writeCLIJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func init() {
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &cliUsageError{err: err}
	})
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		cwd, err := cmd.Flags().GetString("cwd")
		if err != nil || cwd == "" {
			return nil
		}
		if err := os.Chdir(cwd); err != nil {
			return fmt.Errorf("failed to change directory: %v", err)
		}
		return nil
	}
	rootCmd.PersistentFlags().BoolP("help", "h", false, "Help")
	rootCmd.PersistentFlags().BoolP("version", "v", false, "Version")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Debug")
	rootCmd.PersistentFlags().StringP("cwd", "c", "", "Current working directory")
	rootCmd.PersistentFlags().StringP("prompt", "p", "", "Prompt to run in non-interactive mode")
	rootCmd.PersistentFlags().String("job-id", "", "Internal persisted background job identifier")

	// Add format flag with validation logic
	rootCmd.PersistentFlags().StringP("output-format", "f", format.Text.String(),
		"Output format for non-interactive mode (text, json, jsonl)")
	rootCmd.PersistentFlags().String("output-schema", "",
		"Workspace-local JSON Schema that the final model output must satisfy")

	// Add quiet flag to hide spinner in non-interactive mode
	rootCmd.PersistentFlags().BoolP("quiet", "q", false, "Hide spinner in non-interactive mode")

	rootCmd.PersistentFlags().Bool("auto-approve", false,
		"Automatically approve permission requests in non-interactive mode (unsafe; default: deny)")
	rootCmd.PersistentFlags().Bool("yes", false,
		"Alias for --auto-approve; explicitly allow unattended permission approval")
	rootCmd.Flags().Bool("no-input", false,
		"Do not read stdin or launch the TUI; fail if no prompt is supplied")

	// Register custom validation for the format flag
	rootCmd.RegisterFlagCompletionFunc("output-format", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return format.SupportedFormats, cobra.ShellCompDirectiveNoFileComp
	})
}
