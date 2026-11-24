# RSCH terminal workspace

The interactive UI remains a Bubble Tea application. The redesign keeps the
agent, session, permission, message, and tool event services authoritative; the
TUI reacts to those services rather than duplicating their state.

## Workspace rules

- `internal/tui/page/chat.go` owns the continuous chat workspace and delegates
  message/editor behavior to the existing chat components.
- `internal/tui/page/workspace.go` renders the compact, live context header.
- `internal/tui/layout/responsive.go` is the shared source of truth for
  sidebar visibility, composer height, and readable content width.
- The secondary file/session context panel appears at 148 columns and above;
  the conversation and composer remain the priority below that breakpoint.
- The composer is intentionally compact and grows only within the existing
  textarea behavior, preserving multiline input and external-editor support.
- `/` and `Alt+P` open the same searchable quick-action palette. Full pasted
  slash commands still use the existing command dispatcher.

## Keyboard surface

The root shortcuts preserve readline editing in the composer: `Ctrl+C`
opens quit confirmation, `Alt+P` opens quick actions, `Alt+S` switches
sessions, `Alt+M` switches models, `F5` attaches an image, `F6` opens logs,
`F8` switches themes, and `F1` opens help. In particular, native `Alt+F`,
`Alt+L`, and `Alt+T` editing chords are never intercepted.
Palette actions use arrow keys, Enter, and Escape. Contextual hints are
rendered in the footer so the UI does not reserve a permanent shortcut
dashboard.

## Responsive verification

Layout metrics are tested across compact and wide boundary cases, including
80×24, 120×40, 147/148 columns, and 160×50 equivalents in
`internal/tui/layout/responsive_test.go`. The chat page also has a compact
render test covering the header after resize. Changes to the TUI should extend
those tests when a new breakpoint or modal is introduced.

## Interaction and rendering contract

- Conversation content is centered and capped at a readable 108 columns.
  The context rail appears only at 148 columns and is capped at 44 columns.
- Streaming follows the latest output only while the reader is already at the
  bottom. When the reader scrolls away, the viewport stays anchored and shows
  a `new output` indicator.
- Tool previews are compact, but truncation is explicit: an omitted-line count
  is part of the rendered result. Edit and write tools render the resulting
  diff; patch tools render changed-file and addition/removal totals.
- Untrusted model/tool/path text is stripped of terminal-control and bidi
  controls before it reaches Markdown or terminal styling.
- Permission prompts default to Deny, require selection plus confirmation for
  approval, show the requested action and scope, and keep Escape as a reliable
  deny action. Ordinary letter keys never approve a prompt that appeared while
  the user was typing.
- Composer shortcuts preserve readline editing. `Ctrl+J` inserts a newline;
  external-editor results return to the draft for review instead of being sent
  automatically.

## Design benchmark

The interaction model was reviewed against current product documentation and
official screenshots on 2026-09-01:

- [OpenCode TUI](https://opencode.ai/docs/tui/) — restrained transcript,
  responsive context rail, searchable actions, compact tool timelines, and
  explicit agent/mode state.
- [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode)
  — readline-compatible editing, queued follow-ups, history search, transcript
  expansion, and context-sensitive Escape behavior.
- [Gemini CLI](https://github.com/google-gemini/gemini-cli) — strong composer
  focus, visible sandbox/mode status, and contained tool-result cards.
- [Crush](https://github.com/charmbracelet/crush) — high-quality Bubble Tea
  visual hierarchy, searchable model selection, and sparse semantic color.

These are references, not claims of feature parity. RSCH keeps its own service,
policy, event, and fail-closed permission boundaries authoritative.
