package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/opencode-ai/opencode/internal/optimizer"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/project"
	"github.com/opencode-ai/opencode/internal/sandbox"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type App struct {
	Sessions    session.Service
	Messages    message.Service
	History     history.Service
	Permissions permission.Service

	CoderAgent agent.Service
	Agents     *orchestration.Registry
	Workflow   *engineering.SQLStore
	Hooks      *hooks.Manager
	EventBus   *events.Bus

	LSPClients *lsp.ClientRegistry

	watcherCancelFuncs []context.CancelFunc
	cancelFuncsMutex   sync.Mutex
	watcherWG          sync.WaitGroup
	lspInitWG          sync.WaitGroup
	lspCancel          context.CancelFunc
}

func New(ctx context.Context, conn *sql.DB) (*App, error) {
	var err error
	eventBus := events.NewBus()
	basePermissions := permission.NewPermissionService(eventBus)
	// Persist the application-wide event stream before tools and agents are
	// constructed so tool, model, sandbox, hook, and workflow events all share
	// one replayable evidence log.
	eventBus.AddSink(events.NewSQLLog(conn))
	policy := configuredExecutionPolicy(config.Get().Policy)
	var notifier notification.Provider
	notifier, err = notification.NewConfigured(config.WorkingDirectory(), notification.Config{
		WebhookURL:      config.Get().Notifications.WebhookURL,
		Command:         config.Get().Notifications.Command,
		Args:            config.Get().Notifications.Args,
		Timeout:         time.Duration(config.Get().Notifications.TimeoutMS) * time.Millisecond,
		AllowedHosts:    config.Get().Network.AllowedHosts,
		AllowPrivate:    config.Get().Network.AllowPrivate,
		AllowedCommands: config.Get().Policy.CommandAllow,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize notifications: %w", err)
	}
	gate, err := execution.New(execution.Config{Policy: policy, Sandbox: execution.LocalSandbox{AllowedCommands: map[string]bool{"true": true}}, Autonomy: execution.Autonomy(config.Get().Autonomy), Notifier: notifier, Bus: eventBus})
	if err != nil {
		return nil, fmt.Errorf("initialize permission execution gate: %w", err)
	}
	q := db.New(conn)
	sessions := session.NewServiceWithDB(q, conn)
	messages := message.NewService(q)
	files := history.NewService(q, conn)

	app := &App{
		Sessions:    sessions,
		Messages:    messages,
		History:     files,
		Permissions: permission.NewGovernedService(basePermissions, gate),
		LSPClients:  lsp.NewClientRegistry(),
		Workflow:    engineering.NewSQLStore(conn),
		EventBus:    eventBus,
	}
	app.Agents, err = orchestration.NewPersistentRegistry(ctx, conn, eventBus, config.Get().Resources.MaxAgents)
	if err != nil {
		return nil, fmt.Errorf("initialize agent registry: %w", err)
	}
	app.Agents.RecoverInterrupted()
	resources, resourceErr := project.Discover(config.WorkingDirectory())
	if resourceErr != nil {
		return nil, fmt.Errorf("discover project resources: %w", resourceErr)
	}
	approvedResources := make(map[string]bool)
	if config.WorkspaceTrusted() {
		for _, resource := range append(resources.Agents, resources.Hooks...) {
			if resource.Manifest != nil {
				approvedResources[project.ApprovalKey(resource.Kind, resource.Manifest.ID)] = true
			}
		}
		if approvedPlugins := config.Get().Governance.ApprovedPlugins; len(approvedPlugins) > 0 {
			for _, resource := range resources.Plugins {
				if resource.Manifest == nil {
					continue
				}
				for _, approved := range approvedPlugins {
					if approved == resource.Manifest.ID || approved == resource.Manifest.Name {
						approvedResources[project.ApprovalKey(resource.Kind, resource.Manifest.ID)] = true
						break
					}
				}
			}
		}
	}
	// Repository entrypoints are activated only after the user has explicitly
	// trusted this exact workspace. Every approved resource still executes in a
	// platform sandbox; an unavailable sandbox fails application initialization.
	if len(approvedResources) > 0 {
		resourceRuntime, runtimeErr := project.NewRuntime(config.WorkingDirectory(), project.RuntimePolicy{
			Timeout:           2 * time.Minute,
			MaxOutput:         1 << 20,
			ApprovedResources: approvedResources,
			RequireSandbox:    true,
			SandboxMode:       sandbox.WorkspaceWrite,
			AllowNetwork:      false,
		})
		if runtimeErr != nil {
			return nil, fmt.Errorf("initialize project resource runtime: %w", runtimeErr)
		}
		app.Hooks, err = hooks.New(config.WorkingDirectory(), resourceRuntime, eventBus)
		if err != nil {
			return nil, fmt.Errorf("load project hooks: %w", err)
		}
		app.Hooks.Start(ctx)
	}

	// Initialize theme based on configuration
	app.initTheme()

	app.CoderAgent, err = agent.NewAgent(
		config.AgentCoder,
		app.Sessions,
		app.Messages,
		agent.CoderAgentToolsOnBusWithClientProvider(
			app.Permissions,
			app.Sessions,
			app.Messages,
			app.History,
			app.LSPClients,
			eventBus,
			app.Agents,
		),
		eventBus,
	)
	if err != nil {
		logging.Error("Failed to create coder agent", err)
		return nil, err
	}

	// Start language servers only after the agent and its dynamically backed
	// tools exist. The owned context and join make shutdown deterministic even
	// when initialization is still in flight.
	lspCtx, lspCancel := context.WithCancel(ctx)
	app.lspCancel = lspCancel
	app.lspInitWG.Add(1)
	go func() {
		defer app.lspInitWG.Done()
		app.initLSPClients(lspCtx)
	}()

	return app, nil
}

func configuredExecutionPolicy(cfg config.PolicyConfig) execution.DefaultPolicy {
	policy := execution.DefaultPolicy{DeniedTools: map[string]bool{}, PromptTools: map[string]bool{}, Production: cfg.ProductionDenied, AllowedPaths: cfg.PathAllow, AllowedCommands: map[string]bool{}}
	for tool, decision := range cfg.Tool {
		switch strings.ToLower(strings.TrimSpace(decision)) {
		case string(execution.Deny):
			policy.DeniedTools[tool] = true
		case string(execution.Prompt):
			policy.PromptTools[tool] = true
		}
	}
	for _, command := range cfg.CommandAllow {
		command = strings.TrimSpace(command)
		if command != "" {
			policy.AllowedCommands[command] = true
		}
	}
	return policy
}

// initTheme sets the application theme based on the configuration
func (app *App) initTheme() {
	cfg := config.Get()
	if cfg == nil || cfg.TUI.Theme == "" {
		return // Use default theme
	}

	// Try to set the theme from config
	err := theme.SetTheme(cfg.TUI.Theme)
	if err != nil {
		logging.Warn("Failed to set theme from config, using default theme", "theme", cfg.TUI.Theme, "error", err)
	} else {
		logging.Debug("Set theme from config", "theme", cfg.TUI.Theme)
	}
}

type NonInteractiveOptions struct {
	OutputFormat format.OutputFormat
	Stdout       io.Writer
	Stderr       io.Writer
	Quiet        bool
	AutoApprove  bool
	OutputSchema *format.OutputSchema
}

// RunNonInteractive preserves the original API for callers embedding App.
// Process-facing code should use RunNonInteractiveWithOptions so the command,
// rather than a global fmt/os writer, owns stdout and stderr.
func (a *App) RunNonInteractive(ctx context.Context, prompt string, outputFormat string, quiet, autoApprove bool) error {
	parsed, err := format.Parse(outputFormat)
	if err != nil {
		return err
	}
	return a.RunNonInteractiveWithOptions(ctx, prompt, NonInteractiveOptions{
		OutputFormat: parsed,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Quiet:        quiet,
		AutoApprove:  autoApprove,
	})
}

// RunNonInteractiveWithOptions executes one prompt with a versioned headless
// output contract. Unattended approval must be explicitly enabled by the
// caller. Structured modes never draw a spinner.
func (a *App) RunNonInteractiveWithOptions(ctx context.Context, prompt string, opts NonInteractiveOptions) error {
	logging.Info("Running in non-interactive mode")
	if opts.Stdout == nil {
		return errors.New("non-interactive stdout is required")
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.OutputFormat == "" {
		opts.OutputFormat = format.Text
	}
	parsedFormat, err := format.Parse(opts.OutputFormat.String())
	if err != nil {
		return err
	}
	opts.OutputFormat = parsedFormat
	startedAt := time.Now().UTC()

	// Start spinner if not in quiet mode
	var spinner *format.Spinner
	if !opts.Quiet && opts.OutputFormat == format.Text {
		spinner = format.NewSpinnerWithWriter("Thinking...", opts.Stderr)
		spinner.Start()
		defer func() {
			if spinner != nil {
				spinner.Stop()
			}
		}()
	}

	const maxPromptLengthForTitle = 100
	titlePrefix := "Non-interactive: "
	titleSuffix := prompt
	promptRunes := []rune(prompt)
	if len(promptRunes) > maxPromptLengthForTitle {
		titleSuffix = string(promptRunes[:maxPromptLengthForTitle]) + "..."
	}
	title := titlePrefix + titleSuffix

	sess, err := a.Sessions.Create(ctx, title)
	if err != nil {
		return fmt.Errorf("failed to create session for non-interactive mode: %w", err)
	}
	logging.Info("Created session for non-interactive run", "session_id", sess.ID)
	workflow, err := a.beginPromptWorkflow(ctx, prompt, sess.ID)
	if err != nil {
		return fmt.Errorf("initialize durable prompt workflow: %w", err)
	}
	sequence := 0
	emitEvent := func(event format.ExecutionEvent) error {
		sequence++
		event.Sequence = sequence
		event.SessionID = sess.ID
		event.GoalID = workflow.Goal.ID
		event.TaskID = workflow.Task.ID
		return format.WriteExecutionEvent(opts.Stdout, event)
	}
	if opts.OutputFormat == format.JSONL {
		if err := emitEvent(format.ExecutionEvent{
			Type:      "run.started",
			Timestamp: startedAt,
			Status:    "in_progress",
			Model:     string(a.CoderAgent.Model().ID),
			Provider:  string(a.CoderAgent.Model().Provider),
		}); err != nil {
			return fmt.Errorf("write execution start event: %w", errors.Join(err, a.finishPromptWorkflow(workflow, false, err)))
		}
	}
	emitTerminalError := func(runErr error) error {
		if opts.OutputFormat != format.JSONL {
			return nil
		}
		status := "failed"
		if promptRunCanceled(runErr) {
			status = "cancelled"
		}
		model := a.CoderAgent.Model()
		completedAt := time.Now().UTC()
		latest := sess
		if updated, sessionErr := a.Sessions.Get(context.Background(), sess.ID); sessionErr == nil {
			latest = updated
		}
		outputErr := emitEvent(format.ExecutionEvent{
			Type:      "run." + status,
			Timestamp: completedAt,
			Status:    status,
			Model:     string(model.ID),
			Provider:  string(model.Provider),
			Usage: format.ExecutionUsage{
				InputTokens:  latest.PromptTokens,
				OutputTokens: latest.CompletionTokens,
				CostUSD:      latest.Cost,
			},
			DurationMS: completedAt.Sub(startedAt).Milliseconds(),
			Error:      security.RedactText(runErr.Error()),
		})
		if outputErr != nil {
			return fmt.Errorf("write execution terminal event: %w", outputErr)
		}
		return nil
	}
	failRun := func(runErr error) error {
		runErr = errors.Join(runErr, a.finishPromptWorkflow(workflow, false, runErr))
		return errors.Join(runErr, emitTerminalError(runErr))
	}

	var approvalRequired <-chan struct{}
	if opts.AutoApprove {
		a.Permissions.AutoApproveSession(sess.ID)
	} else {
		approvalCtx, stopApprovalWatch := context.WithCancel(ctx)
		defer stopApprovalWatch()
		approvalRequired = a.denyNonInteractiveApprovals(approvalCtx, sess.ID)
	}

	executionPrompt := prompt
	if opts.OutputSchema != nil {
		executionPrompt += opts.OutputSchema.PromptInstruction()
	}
	done, err := a.CoderAgent.Run(ctx, sess.ID, executionPrompt)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(err, promptContextError(ctx))
		}
		return fmt.Errorf("failed to start agent processing stream: %w", failRun(err))
	}

	var result agent.AgentEvent
	select {
	case received, ok := <-done:
		if !ok {
			err = agent.ErrIncomplete
			return fmt.Errorf("agent processing stream closed without a result: %w", failRun(err))
		}
		result = received
	case <-ctx.Done():
		a.CoderAgent.Cancel(sess.ID)
		err = promptContextError(ctx)
		return failRun(err)
	}
	if ctx.Err() != nil {
		a.CoderAgent.Cancel(sess.ID)
		err = promptContextError(ctx)
		return failRun(err)
	}
	if permissionRequired(approvalRequired) {
		return fmt.Errorf("non-interactive execution requires approval; rerun with --auto-approve to explicitly allow unattended approval: %w", failRun(permission.ErrorPermissionDenied))
	}
	if result.Error != nil {
		if promptRunCanceled(result.Error) {
			logging.Info("Agent processing cancelled", "session_id", sess.ID)
			return failRun(result.Error)
		}
		return fmt.Errorf("agent processing failed: %w", failRun(result.Error))
	}

	// Get the text content from the response
	content := result.Message.Content().String()
	var structured any
	if opts.OutputSchema != nil {
		structured, err = opts.OutputSchema.Validate(content)
		if err != nil {
			return failRun(err)
		}
	}
	if err := a.finishPromptWorkflow(workflow, true, nil); err != nil {
		return errors.Join(err, emitTerminalError(err))
	}

	// Stop progress before emitting the final stdout record.
	if spinner != nil {
		spinner.Stop()
		spinner = nil
	}
	completedAt := time.Now().UTC()
	latest := sess
	if updated, sessionErr := a.Sessions.Get(ctx, sess.ID); sessionErr == nil {
		latest = updated
	}
	model := a.CoderAgent.Model()
	terminal := format.ExecutionEvent{
		Type:         "run.completed",
		Timestamp:    completedAt,
		Status:       "completed",
		MessageID:    result.Message.ID,
		Model:        string(model.ID),
		Provider:     string(model.Provider),
		FinishReason: string(result.Message.FinishReason()),
		Response:     content,
		Structured:   structured,
		Usage: format.ExecutionUsage{
			InputTokens:  latest.PromptTokens,
			OutputTokens: latest.CompletionTokens,
			CostUSD:      latest.Cost,
		},
		DurationMS: completedAt.Sub(startedAt).Milliseconds(),
	}
	switch opts.OutputFormat {
	case format.Text:
		_, err = fmt.Fprintln(opts.Stdout, content)
	case format.JSON, format.JSONL:
		err = emitEvent(terminal)
	default:
		err = fmt.Errorf("unsupported output format %q", opts.OutputFormat)
	}
	if err != nil {
		return fmt.Errorf("write non-interactive result: %w", err)
	}

	if totals := optimizer.Default.Totals(sess.ID); totals.Requests > 0 {
		fmt.Fprintln(opts.Stderr, optimizerSummary(totals))
	}
	logging.Info("Non-interactive run completed", "session_id", sess.ID)

	return nil
}

// denyNonInteractiveApprovals makes the default script behavior fail closed.
// There is no TUI in non-interactive mode to answer a permission request, so
// leaving the request pending would make a script wait for the permission
// service timeout instead of failing safely.
func (a *App) denyNonInteractiveApprovals(ctx context.Context, sessionID string) <-chan struct{} {
	denied := make(chan struct{}, 1)
	permissionEvents := a.Permissions.Subscribe(ctx)
	go func() {
		for {
			select {
			case request, ok := <-permissionEvents:
				if !ok {
					return
				}
				if request.Payload.SessionID != sessionID {
					continue
				}
				select {
				case denied <- struct{}{}:
				default:
				}
				a.Permissions.Deny(request.Payload)
				// A run may request several tools concurrently or retry after a
				// denial. Keep denying until the owning run stops this watcher.
			case <-ctx.Done():
				return
			}
		}
	}()
	return denied
}

func promptRunCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, agent.ErrRequestCancelled)
}

// Keep cancellation recognizable to callers and the workflow state machine
// while preserving the caller's optional diagnostic cause.
func promptContextError(ctx context.Context) error {
	cause := context.Cause(ctx)
	if errors.Is(cause, ctx.Err()) {
		return cause
	}
	return errors.Join(ctx.Err(), cause)
}

func permissionRequired(approvalRequired <-chan struct{}) bool {
	if approvalRequired == nil {
		return false
	}
	select {
	case <-approvalRequired:
		return true
	default:
		return false
	}
}

type promptWorkflow struct {
	Goal engineering.Goal
	Task engineering.Task
}

// TransitionTask is the application-owned task mutation seam for interactive
// clients. It keeps the durable state machine and the shared event stream in
// lockstep for transitions that do not belong to a prompt-run lifecycle.
func (a *App) TransitionTask(ctx context.Context, id string, next engineering.Status) (engineering.Task, error) {
	if a == nil || a.Workflow == nil {
		return engineering.Task{}, errors.New("workflow store is unavailable")
	}
	task, err := a.Workflow.TransitionTask(ctx, id, next)
	if err != nil {
		return engineering.Task{}, err
	}
	kind := events.TaskStarted
	switch next {
	case engineering.StatusCompleted, engineering.StatusVerified, engineering.StatusPassed:
		kind = events.TaskCompleted
	case engineering.StatusFailed:
		kind = events.TaskFailed
	case engineering.StatusCanceled:
		kind = events.TaskCanceled
	case engineering.StatusBlocked:
		kind = events.TaskBlocked
	case engineering.StatusRetrying:
		kind = events.TaskRetrying
	case engineering.StatusPaused:
		kind = events.TaskPaused
	}
	a.publishWorkflowEvent(kind, task.GoalID, "task state transitioned", map[string]any{"taskId": task.ID, "status": next})
	return task, nil
}

// beginPromptWorkflow turns a non-interactive natural-language run into a
// durable goal/task lifecycle. The agent remains responsible for the actual
// engineering work; this record makes planning, execution, recovery, and
// evidence inspectable instead of leaving the run as an untracked session.
func (a *App) beginPromptWorkflow(ctx context.Context, prompt, sessionID string) (promptWorkflow, error) {
	if a.Workflow == nil {
		return promptWorkflow{}, errors.New("workflow store is unavailable")
	}
	goal := engineering.PrepareGoal(engineering.Goal{ID: uuid.NewString(), Title: prompt, Description: prompt})
	if err := a.Workflow.SaveGoal(ctx, goal); err != nil {
		return promptWorkflow{}, err
	}
	a.publishWorkflowEvent(events.GoalCreated, goal.ID, "prompt workflow created", map[string]any{"sessionId": sessionID})
	for _, next := range []engineering.Status{engineering.StatusAnalyzing, engineering.StatusPlanning, engineering.StatusExecuting} {
		if err := engineering.TransitionGoal(goal.Status, next); err != nil {
			return promptWorkflow{}, err
		}
		goal.Status = next
		if err := a.Workflow.SaveGoal(ctx, goal); err != nil {
			return promptWorkflow{}, err
		}
		if next == engineering.StatusExecuting {
			a.publishWorkflowEvent(events.GoalStarted, goal.ID, "prompt workflow execution started", map[string]any{"sessionId": sessionID})
		}
	}
	requirement := engineering.Requirement{
		ID:         uuid.NewString(),
		GoalID:     goal.ID,
		Statement:  prompt,
		Priority:   "high",
		Acceptance: []string{"natural-language agent execution completed", "verification evidence recorded"},
		Risk:       "medium",
		Status:     engineering.StatusPending,
		Confirmed:  true,
	}
	if err := a.Workflow.SaveRequirement(ctx, requirement); err != nil {
		return promptWorkflow{}, err
	}
	a.publishWorkflowEvent(events.RequirementUpdated, goal.ID, "prompt requirement confirmed from explicit user request", map[string]any{"requirementId": requirement.ID, "sessionId": sessionID})
	task := engineering.Task{ID: uuid.NewString(), GoalID: goal.ID, Title: "Execute natural-language engineering goal", Description: prompt, Status: engineering.StatusPending, Agent: "coder-agent", Risk: "medium", Verification: []string{"agent response completed"}}
	if err := a.Workflow.ReplaceTasks(ctx, goal.ID, []engineering.Task{task}); err != nil {
		return promptWorkflow{}, err
	}
	a.publishWorkflowEvent(events.TaskCreated, goal.ID, "prompt task created", map[string]any{"taskId": task.ID, "sessionId": sessionID})
	for _, next := range []engineering.Status{engineering.StatusReady, engineering.StatusRunning} {
		updated, err := a.Workflow.TransitionTask(ctx, task.ID, next)
		if err != nil {
			return promptWorkflow{}, err
		}
		task = updated
		if next == engineering.StatusRunning {
			a.publishWorkflowEvent(events.TaskStarted, goal.ID, "prompt task started", map[string]any{"taskId": task.ID, "sessionId": sessionID})
		}
	}
	return promptWorkflow{Goal: goal, Task: task}, nil
}

func (a *App) finishPromptWorkflow(workflow promptWorkflow, passed bool, runErr error) error {
	if a.Workflow == nil || workflow.Goal.ID == "" || workflow.Task.ID == "" {
		return errors.New("prompt workflow is unavailable")
	}
	ctx := context.Background()
	if passed {
		// Persist the proof before advancing the task into terminal states;
		// otherwise a failed evidence write leaves an unverified run verified.
		if err := a.Workflow.AddEvidence(ctx, engineering.Evidence{ID: uuid.NewString(), TaskID: workflow.Task.ID, Kind: "agent-run", Description: "natural-language agent execution completed", Command: "coder-agent", Passed: true}); err != nil {
			runErr := fmt.Errorf("persist prompt evidence: %w", err)
			return errors.Join(runErr, a.finishPromptWorkflow(workflow, false, runErr))
		}
		task, err := a.Workflow.TransitionTask(ctx, workflow.Task.ID, engineering.StatusCompleted)
		if err != nil {
			return fmt.Errorf("complete prompt task: %w", err)
		}
		workflow.Task = task
		task, err = a.Workflow.TransitionTask(ctx, workflow.Task.ID, engineering.StatusVerified)
		if err != nil {
			return fmt.Errorf("verify prompt task: %w", err)
		}
		workflow.Task = task
		requirements, requirementsErr := a.Workflow.Requirements(ctx, workflow.Goal.ID)
		tasks, tasksErr := a.Workflow.LoadTasks(ctx, workflow.Goal.ID)
		evidence, evidenceErr := a.Workflow.LoadEvidence(ctx, workflow.Task.ID)
		completionErr := errors.Join(requirementsErr, tasksErr, evidenceErr)
		if completionErr == nil {
			completionErr = engineering.ValidateCompletion(workflow.Goal, requirements, tasks, evidence)
		}
		if completionErr != nil {
			workflow.Goal.Status = engineering.StatusFailed
			if err := a.Workflow.SaveGoal(ctx, workflow.Goal); err != nil {
				completionErr = errors.Join(completionErr, err)
			}
			a.publishWorkflowEvent(events.GoalFailed, workflow.Goal.ID, "prompt workflow completion evidence was insufficient", map[string]any{"taskId": workflow.Task.ID})
			return fmt.Errorf("validate prompt workflow completion: %w", completionErr)
		}
		workflow.Goal.Status = engineering.StatusVerifying
		if err := a.Workflow.SaveGoal(ctx, workflow.Goal); err != nil {
			return fmt.Errorf("save verifying prompt goal: %w", err)
		}
		workflow.Goal.Status = engineering.StatusCompleted
		if err := a.Workflow.SaveGoal(ctx, workflow.Goal); err != nil {
			return fmt.Errorf("save completed prompt goal: %w", err)
		}
		a.publishWorkflowEvent(events.TaskCompleted, workflow.Goal.ID, "prompt task completed", map[string]any{"taskId": workflow.Task.ID})
		a.publishWorkflowEvent(events.GoalCompleted, workflow.Goal.ID, "prompt workflow completed", map[string]any{"taskId": workflow.Task.ID})
		return nil
	}
	failedStatus := engineering.StatusFailed
	if promptRunCanceled(runErr) {
		failedStatus = engineering.StatusCanceled
	}
	description := "natural-language agent execution failed"
	if runErr != nil {
		description += ": " + runErr.Error()
	}
	var finalizationErr error
	if task, err := a.Workflow.TransitionTask(ctx, workflow.Task.ID, failedStatus); err != nil {
		finalizationErr = errors.Join(finalizationErr, fmt.Errorf("transition failed prompt task: %w", err))
	} else {
		workflow.Task = task
		taskEvent := events.TaskFailed
		if failedStatus == engineering.StatusCanceled {
			taskEvent = events.TaskCanceled
		}
		a.publishWorkflowEvent(taskEvent, workflow.Goal.ID, description, map[string]any{"taskId": workflow.Task.ID, "status": failedStatus})
	}
	if err := a.Workflow.AddEvidence(ctx, engineering.Evidence{ID: uuid.NewString(), TaskID: workflow.Task.ID, Kind: "agent-run", Description: description, Command: "coder-agent", Passed: false}); err != nil {
		finalizationErr = errors.Join(finalizationErr, fmt.Errorf("persist failed prompt evidence: %w", err))
	}
	workflow.Goal.Status = failedStatus
	if err := a.Workflow.SaveGoal(ctx, workflow.Goal); err != nil {
		finalizationErr = errors.Join(finalizationErr, fmt.Errorf("save failed prompt goal: %w", err))
	}
	a.publishWorkflowEvent(events.GoalFailed, workflow.Goal.ID, description, map[string]any{"taskId": workflow.Task.ID, "status": failedStatus})
	return finalizationErr
}

func (a *App) publishWorkflowEvent(kind events.Type, goalID, message string, data map[string]any) {
	if a.EventBus == nil {
		return
	}
	event := events.New(kind, data)
	event.GoalID = goalID
	event.Actor = "workflow"
	if taskID, ok := data["taskId"].(string); ok {
		event.TaskID = taskID
	}
	if sessionID, ok := data["sessionId"].(string); ok {
		event.SessionID = sessionID
	}
	event.Data["message"] = security.RedactText(message)
	a.EventBus.Publish(event)
}

// Shutdown performs a clean shutdown of the application
func (app *App) Shutdown() {
	if app.Hooks != nil {
		app.Hooks.Close()
	}
	if app.EventBus != nil {
		app.EventBus.Close()
	}
	if app.lspCancel != nil {
		app.lspCancel()
	}
	app.lspInitWG.Wait()

	// Cancel all watcher goroutines
	app.cancelFuncsMutex.Lock()
	for _, cancel := range app.watcherCancelFuncs {
		cancel()
	}
	app.cancelFuncsMutex.Unlock()
	app.watcherWG.Wait()

	// Perform additional cleanup for LSP clients
	clients := app.LSPClients.Snapshot()

	for name, client := range clients {
		if err := shutdownLSPClient(client); err != nil {
			logging.Error("Failed to shutdown LSP client", "name", name, "error", err)
		}
	}
}

// optimizerSummary is the per-run token line printed to stderr by
// `rsch exec`. Counts are estimates (about 4 bytes per token) over the
// message history only; the system prompt and tool definitions are not
// included because the optimizer does not change them.
func optimizerSummary(totals optimizer.Stats) string {
	if !totals.Enabled {
		return fmt.Sprintf("rsch: optimizer off; history ~%d tokens over %d requests (estimate, 4 bytes/token)", totals.TokensBefore, totals.Requests)
	}
	percent := 0.0
	if totals.TokensBefore > 0 {
		percent = 100 * float64(totals.Saved()) / float64(totals.TokensBefore)
	}
	return fmt.Sprintf("rsch: optimizer saved ~%d of ~%d history tokens (%.1f%%) over %d requests (estimate, 4 bytes/token; %d trimmed, %d deduplicated, %d compacted)",
		totals.Saved(), totals.TokensBefore, percent, totals.Requests, totals.Trimmed, totals.Deduplicated, totals.Compacted)
}
