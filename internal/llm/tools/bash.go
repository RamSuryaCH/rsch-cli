package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/sandbox"
)

type BashParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type BashPermissionsParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type BashResponseMetadata struct {
	StartTime       int64        `json:"start_time"`
	EndTime         int64        `json:"end_time"`
	SandboxBackend  string       `json:"sandbox_backend,omitempty"`
	SandboxMode     sandbox.Mode `json:"sandbox_mode,omitempty"`
	NetworkIsolated bool         `json:"network_isolated"`
}

type bashSandboxProbe func(context.Context, string) sandbox.Capability
type bashSandboxRun func(context.Context, sandbox.PlatformRunner, []string, map[string]string) ([]byte, error)
type bashShellCommand func(string) ([]string, error)

type bashTool struct {
	permissions      permission.Service
	workingDirectory func() string
	probeSandbox     bashSandboxProbe
	runSandbox       bashSandboxRun
	shellCommand     bashShellCommand
	environment      func() map[string]string
}

const (
	BashToolName = "bash"

	DefaultTimeout  = 1 * 60 * 1000  // 1 minutes in milliseconds
	MaxTimeout      = 10 * 60 * 1000 // 10 minutes in milliseconds
	MaxOutputLength = 30000
)

var bannedCommands = []string{
	"alias", "curl", "curlie", "wget", "axel", "aria2c",
	"nc", "telnet", "lynx", "w3m", "links", "httpie", "xh",
	"http-prompt", "chrome", "firefox", "safari",
}

var safeReadOnlyCommands = []string{
	"ls", "echo", "pwd", "cal", "uptime", "whoami", "id", "groups", "printenv", "which", "type", "whereis",
	"whatis", "uname", "df", "du", "free", "top", "ps",
}

// wrapperCommands can turn a superficially harmless first token into a second
// command. Reject them rather than trying to recursively interpret every
// wrapper's command-line grammar.
var wrapperCommands = []string{
	"command", "builtin", "exec", "env", "xargs", "parallel", "nohup", "nice", "time", "timeout", "watch", "stdbuf", "setsid",
	"sudo", "doas", "su", "chroot", "busybox",
	"sh", "bash", "dash", "zsh", "ksh", "fish", "cmd", "powershell", "pwsh",
	"eval", "source", ".",
}

func bashDescription() string {
	bannedCommandsStr := strings.Join(bannedCommands, ", ")
	return fmt.Sprintf(`Executes one command in an OS-isolated, non-networked shell with an optional timeout.

Before executing the command, please follow these steps:

1. Directory Verification:
 - If the command will create new directories or files, first use the LS tool to verify the parent directory exists and is the correct location
 - For example, before running "mkdir foo/bar", first use LS to check that "foo" exists and is the intended parent directory

2. Security Check:
 - For security and to limit the threat of a prompt injection attack, some commands are limited or banned. If you use a disallowed command, you will receive an error message explaining the restriction. Explain the error to the User.
 - Verify that the command is not one of the banned commands: %s.

3. Command Execution:
 - After ensuring proper quoting, execute the command.
 - Capture the output of the command.

4. Output Processing:
 - If the output exceeds %d characters, output will be truncated before being returned to you.
 - Prepare the output for display to the user.

5. Return Result:
 - Provide the processed output of the command.
 - If any errors occurred during execution, include those in the output.

Usage notes:
- The command argument is required.
- You can specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). If not specified, commands time out after 1 minute.
- VERY IMPORTANT: You MUST avoid using search commands like 'find' and 'grep'. Instead use Grep, Glob, or Agent tools to search. You MUST avoid read tools like 'cat', 'head', 'tail', and 'ls', and use FileRead and LS tools to read files.
- Run exactly one command per call. Chaining, redirection, command substitution, wrappers, and background execution are rejected.
- Commands run in separate shell processes with an allowlisted environment. Shell state, directory changes, and environment changes do not persist between calls.
- Automatic read-only exemptions apply only to bare commands in a system shell without startup arguments and a trusted system PATH. Git and Go commands, explicit executable paths, and custom or login shells require normal permission approval.
- The workspace is writable, but repository control data is protected by the platform sandbox. Network access is disabled. If the required OS sandbox is unavailable, execution fails closed.
- Try to maintain your current working directory throughout the session by using absolute paths and avoiding usage of 'cd'. You may use 'cd' if the User explicitly requests it.
<good-example>
pytest /foo/bar/tests
</good-example>
<bad-example>
cd /foo/bar && pytest tests
</bad-example>

# Committing changes with git

When the user asks you to create a new git commit, follow these steps carefully:

1. Start with a single message that contains exactly three tool_use blocks that do the following (it is VERY IMPORTANT that you send these tool_use blocks in a single message, otherwise it will feel slow to the user!):
 - Run a git status command to see all untracked files.
 - Run a git diff command to see both staged and unstaged changes that will be committed.
 - Run a git log command to see recent commit messages, so that you can follow this repository's commit message style.

2. Use the git context at the start of this conversation to determine which files are relevant to your commit. Add relevant untracked files to the staging area. Do not commit files that were already modified at the start of this conversation, if they are not relevant to your commit.

3. Analyze all staged changes (both previously staged and newly added) and draft a commit message. Wrap your analysis process in <commit_analysis> tags:

<commit_analysis>
- List the files that have been changed or added
- Summarize the nature of the changes (eg. new feature, enhancement to an existing feature, bug fix, refactoring, test, docs, etc.)
- Brainstorm the purpose or motivation behind these changes
- Do not use tools to explore code, beyond what is available in the git context
- Assess the impact of these changes on the overall project
- Check for any sensitive information that shouldn't be committed
- Draft a concise (1-2 sentences) commit message that focuses on the "why" rather than the "what"
- Ensure your language is clear, concise, and to the point
- Ensure the message accurately reflects the changes and their purpose (i.e. "add" means a wholly new feature, "update" means an enhancement to an existing feature, "fix" means a bug fix, etc.)
- Ensure the message is not generic (avoid words like "Update" or "Fix" without context)
- Review the draft message to ensure it accurately reflects the changes and their purpose
</commit_analysis>

4. Create the commit with a message ending with:
🤖 Generated with opencode
Co-Authored-By: opencode <noreply@opencode.ai>

- In order to ensure good formatting, ALWAYS pass the commit message via a HEREDOC, a la this example:
<example>
git commit -m "$(cat <<'EOF'
 Commit message here.

 🤖 Generated with opencode
 Co-Authored-By: opencode <noreply@opencode.ai>
 EOF
 )"
</example>

5. If the commit fails due to pre-commit hook changes, retry the commit ONCE to include these automated changes. If it fails again, it usually means a pre-commit hook is preventing the commit. If the commit succeeds but you notice that files were modified by the pre-commit hook, you MUST amend your commit to include them.

6. Finally, run git status to make sure the commit succeeded.

Important notes:
- When possible, combine the "git add" and "git commit" commands into a single "git commit -am" command, to speed things up
- However, be careful not to stage files (e.g. with 'git add .') for commits that aren't part of the change, they may have untracked files they want to keep around, but not commit.
- NEVER update the git config
- DO NOT push to the remote repository
- IMPORTANT: Never use git commands with the -i flag (like git rebase -i or git add -i) since they require interactive input which is not supported.
- If there are no changes to commit (i.e., no untracked files and no modifications), do not create an empty commit
- Ensure your commit message is meaningful and concise. It should explain the purpose of the changes, not just describe them.
- Return an empty response - the user will see the git output directly

# Creating pull requests
Use the gh command via the Bash tool for ALL GitHub-related tasks including working with issues, pull requests, checks, and releases. If given a Github URL use the gh command to get the information needed.

IMPORTANT: When the user asks you to create a pull request, follow these steps carefully:

1. Understand the current state of the branch. Remember to send a single message that contains multiple tool_use blocks (it is VERY IMPORTANT that you do this in a single message, otherwise it will feel slow to the user!):
 - Run a git status command to see all untracked files.
 - Run a git diff command to see both staged and unstaged changes that will be committed.
 - Check if the current branch tracks a remote branch and is up to date with the remote, so you know if you need to push to the remote
 - Run a git log command and 'git diff main...HEAD' to understand the full commit history for the current branch (from the time it diverged from the 'main' branch.)

2. Create new branch if needed

3. Commit changes if needed

4. Push to remote with -u flag if needed

5. Analyze all changes that will be included in the pull request, making sure to look at all relevant commits (not just the latest commit, but all commits that will be included in the pull request!), and draft a pull request summary. Wrap your analysis process in <pr_analysis> tags:

<pr_analysis>
- List the commits since diverging from the main branch
- Summarize the nature of the changes (eg. new feature, enhancement to an existing feature, bug fix, refactoring, test, docs, etc.)
- Brainstorm the purpose or motivation behind these changes
- Assess the impact of these changes on the overall project
- Do not use tools to explore code, beyond what is available in the git context
- Check for any sensitive information that shouldn't be committed
- Draft a concise (1-2 bullet points) pull request summary that focuses on the "why" rather than the "what"
- Ensure the summary accurately reflects all changes since diverging from the main branch
- Ensure your language is clear, concise, and to the point
- Ensure the summary accurately reflects the changes and their purpose (ie. "add" means a wholly new feature, "update" means an enhancement to an existing feature, "fix" means a bug fix, etc.)
- Ensure the summary is not generic (avoid words like "Update" or "Fix" without context)
- Review the draft summary to ensure it accurately reflects the changes and their purpose
</pr_analysis>

6. Create PR using gh pr create with the format below. Use a HEREDOC to pass the body to ensure correct formatting.
<example>
gh pr create --title "the pr title" --body "$(cat <<'EOF'
## Summary
<1-3 bullet points>

## Test plan
[Checklist of TODOs for testing the pull request...]

🤖 Generated with opencode
EOF
)"
</example>

Important:
- Return an empty response - the user will see the gh output directly
- Never update git config`, bannedCommandsStr, MaxOutputLength)
}

func NewBashTool(permission permission.Service) BaseTool {
	return &bashTool{
		permissions:      permission,
		workingDirectory: config.WorkingDirectory,
		probeSandbox:     sandbox.Probe,
		runSandbox: func(ctx context.Context, runner sandbox.PlatformRunner, argv []string, environment map[string]string) ([]byte, error) {
			return runner.RunWithEnvironment(ctx, argv, environment)
		},
		shellCommand: bashCommandArgv,
		environment:  bashEnvironment,
	}
}

func (b *bashTool) Info() ToolInfo {
	return ToolInfo{
		Name:        BashToolName,
		Description: bashDescription(),
		Parameters: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The single command to execute in the isolated workspace shell",
				"minLength":   1,
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in milliseconds (max 600000)",
				"minimum":     1,
				"maximum":     MaxTimeout,
			},
		},
		Required:      []string{"command"},
		Permissions:   []string{"process-execution", "workspace-write"},
		Risk:          "high",
		Timeout:       time.Duration(MaxTimeout) * time.Millisecond,
		ExecutionMode: "sandboxed-one-shot",
		Capabilities:  []string{"filesystem-isolation", "network-isolation", "process-isolation"},
	}
}

func (b *bashTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return NewTextErrorResponseWithError(err)
	}
	var params BashParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}

	if params.Timeout > MaxTimeout {
		params.Timeout = MaxTimeout
	} else if params.Timeout <= 0 {
		params.Timeout = DefaultTimeout
	}

	if params.Command == "" {
		return NewTextErrorResponse("missing command"), nil
	}
	if err := validateShellCommand(params.Command); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	workingDirectory := config.WorkingDirectory()
	if b.workingDirectory != nil {
		workingDirectory = b.workingDirectory()
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewTextErrorResponseWithError(fmt.Errorf("session ID and message ID are required for executing a command"))
	}
	shellCommand := bashCommandArgv
	if b.shellCommand != nil {
		shellCommand = b.shellCommand
	}
	argv, err := shellCommand(params.Command)
	if err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("error preparing command: %w", err))
	}
	environment := bashEnvironment()
	if b.environment != nil {
		environment = b.environment()
	}
	isSafeReadOnly := isSafeReadOnlyInvocation(params.Command, argv, environment)
	if !isSafeReadOnly {
		if b.permissions == nil {
			return NewTextErrorResponseWithError(fmt.Errorf("permission service is unavailable"))
		}
		p := b.permissions.RequestContext(ctx,
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        workingDirectory,
				ScopeKind:   permission.ScopeDirectory,
				ToolName:    BashToolName,
				Action:      "execute",
				Description: fmt.Sprintf("Execute command: %s", params.Command),
				Params: BashPermissionsParams{
					Command: params.Command,
					Timeout: params.Timeout,
				},
			},
		)
		if !p {
			return ToolResponse{}, permission.ErrorPermissionDenied
		}
	}
	startTime := time.Now()
	probeSandbox := sandbox.Probe
	if b.probeSandbox != nil {
		probeSandbox = b.probeSandbox
	}
	capability := probeSandbox(ctx, workingDirectory)
	metadata := BashResponseMetadata{
		StartTime:       startTime.UnixMilli(),
		SandboxBackend:  capability.Backend,
		SandboxMode:     sandbox.WorkspaceWrite,
		NetworkIsolated: true,
	}
	if err := bashSandboxCapabilityError(capability); err != nil {
		metadata.EndTime = time.Now().UnixMilli()
		return bashExecutionError(err, metadata)
	}

	runner := sandbox.PlatformRunner{
		Capability:       capability,
		WorkingDirectory: workingDirectory,
		Mode:             sandbox.WorkspaceWrite,
		AllowNetwork:     false,
		MaxOutputBytes:   MaxOutputLength,
	}
	runSandbox := func(ctx context.Context, runner sandbox.PlatformRunner, argv []string, environment map[string]string) ([]byte, error) {
		return runner.RunWithEnvironment(ctx, argv, environment)
	}
	if b.runSandbox != nil {
		runSandbox = b.runSandbox
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(params.Timeout)*time.Millisecond)
	defer cancel()
	output, runErr := runSandbox(timeoutCtx, runner, argv, environment)
	metadata.EndTime = time.Now().UnixMilli()
	if err := ctx.Err(); err != nil {
		return bashExecutionError(err, metadata)
	}
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		return formatBashResponse(truncateOutput(string(output)), "", 124, true, metadata), nil
	}
	if runErr != nil {
		if errors.Is(runErr, process.ErrOutputLimitExceeded) {
			return formatBashResponse(string(output), "Command output exceeded the 30000-byte safety limit", 1, false, metadata), nil
		}
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode := exitError.ExitCode()
			if exitCode < 0 {
				exitCode = 1
			}
			return formatBashResponse(truncateOutput(string(output)), "", exitCode, false, metadata), nil
		}
		return bashExecutionError(runErr, metadata)
	}

	return formatBashResponse(truncateOutput(string(output)), "", 0, false, metadata), nil
}

func bashExecutionError(err error, metadata BashResponseMetadata) (ToolResponse, error) {
	wrapped := fmt.Errorf("error executing command: %w", err)
	return WithResponseMetadata(NewTextErrorResponse(wrapped.Error()), metadata), wrapped
}

func bashSandboxCapabilityError(capability sandbox.Capability) error {
	if !capability.Isolated {
		if capability.Reason != "" {
			return fmt.Errorf("%w: %s", sandbox.ErrUnavailable, capability.Reason)
		}
		return sandbox.ErrUnavailable
	}
	if !capability.Supported || capability.Backend == "" || !capability.FilesystemIsolation || !capability.NetworkIsolation || !capability.ProcessIsolation {
		return sandbox.ErrInsufficientCapability
	}
	return nil
}

func bashCommandArgv(command string) ([]string, error) {
	var shellPath string
	var shellArgs []string
	if cfg := config.Get(); cfg != nil {
		shellPath = strings.TrimSpace(cfg.Shell.Path)
		shellArgs = append(shellArgs, cfg.Shell.Args...)
	}
	if shellPath == "" {
		shellPath = strings.TrimSpace(os.Getenv("SHELL"))
	}
	if shellPath == "" && runtime.GOOS == "windows" {
		shellPath = strings.TrimSpace(os.Getenv("COMSPEC"))
		if shellPath == "" {
			shellPath = "cmd.exe"
		}
	}
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	if strings.ContainsAny(shellPath, "\x00\r\n") {
		return nil, fmt.Errorf("configured shell path is invalid")
	}

	executionFlag := "-c"
	switch normalizeExecutable(shellPath) {
	case "cmd":
		executionFlag = "/C"
	case "powershell", "pwsh":
		executionFlag = "-Command"
	}
	argv := make([]string, 0, len(shellArgs)+3)
	argv = append(argv, shellPath)
	argv = append(argv, shellArgs...)
	argv = append(argv, executionFlag, command)
	return argv, nil
}

func bashEnvironment() map[string]string {
	environment := map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_EDITOR":          "true",
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_PAGER":           "cat",
		"GIT_TERMINAL_PROMPT": "0",
		"PAGER":               "cat",
	}
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TERM", "TMPDIR", "TEMP", "TMP", "SystemRoot", "ComSpec"} {
		if value := os.Getenv(name); value != "" {
			environment[name] = value
		}
	}
	return environment
}

func formatBashResponse(stdout, stderr string, exitCode int, interrupted bool, metadata BashResponseMetadata) ToolResponse {
	errorMessage := stderr
	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	if errorMessage != "" {
		if stdout != "" {
			stdout += "\n"
		}
		stdout += errorMessage
	}
	if stdout == "" {
		stdout = "no output"
	}
	response := NewTextResponse(stdout)
	if interrupted || exitCode != 0 {
		response = NewTextErrorResponse(stdout)
	}
	return WithResponseMetadata(response, metadata)
}

func truncateOutput(content string) string {
	if len(content) <= MaxOutputLength {
		return content
	}

	halfLength := MaxOutputLength / 2
	start := content[:halfLength]
	end := content[len(content)-halfLength:]

	truncatedLinesCount := countLines(content[halfLength : len(content)-halfLength])
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLinesCount, end)
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// validateShellCommand prevents a command classified by its first token from
// smuggling a second operation through the persistent shell. Structured argv
// tools remain available for operations that genuinely need shell syntax.
func validateShellCommand(command string) error {
	for _, marker := range []string{";", "&", "|", ">", "<", "$", "`", "\n", "\r", "(", ")", "\x00"} {
		if strings.Contains(command, marker) {
			return fmt.Errorf("shell metacharacter %q is not allowed; run one command without chaining or substitution", marker)
		}
	}
	words, err := parseShellWords(command)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return fmt.Errorf("missing command")
	}
	_, executable, ok := executableWord(words)
	if !ok {
		return nil
	}
	if commandIn(executable, bannedCommands) {
		return fmt.Errorf("command %q is not allowed", executable)
	}
	if commandIn(executable, wrapperCommands) {
		return fmt.Errorf("command wrapper %q is not allowed", executable)
	}
	return nil
}

// isSafeReadOnlyCommand classifies parsed command tokens, not string prefixes.
// Anything ambiguous fails closed to the normal permission flow.
func isSafeReadOnlyCommand(command string) bool {
	words, err := parseShellWords(command)
	if err != nil {
		return false
	}
	index, executable, ok := executableWord(words)
	if !ok || index != 0 || words[0] != executable {
		// Explicit paths, case aliases, executable suffixes, and environment
		// assignments must not inherit trust from a normalized basename.
		return false
	}
	args := words[index+1:]
	if commandIn(executable, safeReadOnlyCommands) {
		return true
	}
	switch executable {
	case "date", "hostname":
		// Both commands can mutate host state when given operands/options.
		return len(args) == 0
	case "git":
		// Repository configuration can invoke helpers (for example,
		// core.fsmonitor or diff textconv) even for read-only subcommands.
		return false
	case "go":
		// Even go version can select and execute a toolchain from workspace
		// configuration, so it needs the same permission gate as builds.
		return false
	}
	return false
}

func isSafeReadOnlyInvocation(command string, argv []string, environment map[string]string) bool {
	if !isSafeReadOnlyCommand(command) || len(argv) != 3 || argv[1] != "-c" || argv[2] != command {
		return false
	}
	// Login arguments, custom interpreters, and shells such as zsh can run
	// startup files before the command. Only these non-login shells qualify.
	switch argv[0] {
	case "/bin/sh", "/usr/bin/sh", "/bin/bash", "/usr/bin/bash", "/bin/dash", "/usr/bin/dash":
	default:
		return false
	}
	for name := range environment {
		if name == "BASH_ENV" || name == "ENV" || name == "SHELLOPTS" || name == "BASHOPTS" || strings.HasPrefix(name, "BASH_FUNC_") {
			return false
		}
	}
	words, _ := parseShellWords(command)
	if words[0] == "echo" || words[0] == "pwd" || words[0] == "type" {
		return true // Builtins in the qualifying shells do not consult PATH.
	}
	if environment["PATH"] == "" {
		return false
	}
	// Inspect every entry, including entries before a currently resolved
	// executable: a workspace could create a shadow executable after a probe.
	for _, directory := range filepath.SplitList(environment["PATH"]) {
		switch directory {
		case "/bin", "/usr/bin", "/sbin", "/usr/sbin":
		default:
			return false
		}
	}
	return true
}

func commandIn(command string, commands []string) bool {
	for _, candidate := range commands {
		if command == candidate {
			return true
		}
	}
	return false
}

func executableWord(words []string) (int, string, bool) {
	index := 0
	for index < len(words) && isShellAssignment(words[index]) {
		index++
	}
	if index >= len(words) {
		return 0, "", false
	}
	return index, normalizeExecutable(words[index]), true
}

func normalizeExecutable(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.ToLower(filepath.Base(value))
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".com"} {
		value = strings.TrimSuffix(value, suffix)
	}
	return value
}

func isShellAssignment(value string) bool {
	index := strings.IndexByte(value, '=')
	// This is intentionally conservative. Shells support multiple assignment
	// grammars (including += and array syntax); treating any leading non-option
	// word containing '=' as an assignment prevents it from hiding the actual
	// executable even if a particular shell would reject that assignment form.
	return index > 0 && value[0] != '-'
}

// parseShellWords handles the quoting needed to identify the executable and
// subcommand. It intentionally does not expand variables or substitutions;
// validateShellCommand rejects those forms before this parser is called.
func parseShellWords(command string) ([]string, error) {
	words := make([]string, 0, 4)
	var word strings.Builder
	var quote rune
	escaped := false
	started := false
	flush := func() {
		if !started {
			return
		}
		words = append(words, word.String())
		word.Reset()
		started = false
	}

	for _, char := range command {
		if escaped {
			// POSIX double quotes preserve a backslash unless it escapes one
			// of these special characters. Dropping it can identify a different
			// executable from the one the shell actually runs.
			if quote == '"' && char != '\\' && char != '"' && char != '$' && char != '`' && char != '\n' {
				word.WriteRune('\\')
			}
			word.WriteRune(char)
			started = true
			escaped = false
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
				started = true
				continue
			}
			if char == '\\' && quote == '"' {
				escaped = true
				continue
			}
			word.WriteRune(char)
			started = true
			continue
		}
		switch {
		case char == '\\':
			escaped = true
			started = true
		case char == '\'' || char == '"':
			quote = char
			started = true
		case char == ' ' || char == '\t' || char == '\n':
			flush()
		default:
			word.WriteRune(char)
			started = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("unterminated shell escape")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated shell quote")
	}
	flush()
	return words, nil
}
