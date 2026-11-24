package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type uiMessageType int

const (
	userMessageType uiMessageType = iota
	assistantMessageType
	toolMessageType

	maxResultHeight = 10
)

type uiMessage struct {
	ID          string
	messageType uiMessageType
	position    int
	height      int
	content     string
}

func toMarkdown(content string, focused bool, width int) string {
	r := styles.GetMarkdownRenderer(width)
	rendered, err := r.Render(styles.SanitizeTerminalText(content))
	if err != nil {
		return styles.SanitizeTerminalText(content)
	}
	return rendered
}

func renderMessage(msg string, isUser bool, isFocused bool, width int, info ...string) string {
	t := theme.CurrentTheme()
	contentWidth := max(1, width-2)
	railLabel := "assistant"
	railColor := t.Primary()
	if isUser {
		railLabel = "you"
		railColor = t.Secondary()
	}

	style := styles.BaseStyle().
		Width(contentWidth).
		PaddingLeft(1).
		BorderLeft(true).
		Foreground(t.Text()).
		BorderForeground(railColor)
	label := styles.BaseStyle().Foreground(railColor).Bold(true).Render(railLabel)
	meta := styles.BaseStyle().Foreground(t.TextMuted()).Render("  ")

	// Apply markdown formatting and handle background color
	parts := []string{
		label + meta,
		styles.ForceReplaceBackgroundWithLipgloss(toMarkdown(msg, isFocused, contentWidth), t.Background()),
	}

	// Remove newline at the end
	parts[0] = strings.TrimSuffix(parts[0], "\n")
	if len(info) > 0 {
		parts = append(parts, info...)
	}

	rendered := style.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			parts...,
		),
	)

	return rendered
}

func renderUserMessage(msg message.Message, isFocused bool, width int, position int) uiMessage {
	var styledAttachments []string
	t := theme.CurrentTheme()
	attachmentStyles := styles.BaseStyle().
		MarginLeft(1).
		Background(t.BackgroundSecondary()).
		Foreground(t.Text())
	for _, attachment := range msg.BinaryContent() {
		file := styles.SanitizeTerminalText(filepath.Base(attachment.Path))
		filename := fmt.Sprintf(" %s %s", styles.DocumentIcon, ansi.Truncate(file, 12, "…"))
		styledAttachments = append(styledAttachments, attachmentStyles.Render(filename))
	}
	content := ""
	if len(styledAttachments) > 0 {
		attachmentContent := styles.BaseStyle().Width(width).Render(lipgloss.JoinHorizontal(lipgloss.Left, styledAttachments...))
		content = renderMessage(msg.Content().String(), true, isFocused, width, attachmentContent)
	} else {
		content = renderMessage(msg.Content().String(), true, isFocused, width)
	}
	userMsg := uiMessage{
		ID:          msg.ID,
		messageType: userMessageType,
		position:    position,
		height:      lipgloss.Height(content),
		content:     content,
	}
	return userMsg
}

// Returns multiple uiMessages because of the tool calls
func renderAssistantMessage(
	msg message.Message,
	msgIndex int,
	allMessages []message.Message, // we need this to get tool results and the user message
	messagesService message.Service, // We need this to get the task tool messages
	focusedUIMessageId string,
	isSummary bool,
	width int,
	position int,
) []uiMessage {
	messages := []uiMessage{}
	content := msg.Content().String()
	thinking := msg.IsThinking()
	finished := msg.IsFinished()
	finishData := msg.FinishPart()
	info := []string{}

	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	// Add finish info if available
	if finished {
		modelName := string(msg.Model)
		if model, ok := models.SupportedModels[msg.Model]; ok && model.Name != "" {
			modelName = model.Name
		}
		if modelName == "" {
			modelName = "assistant"
		}
		switch finishData.Reason {
		case message.FinishReasonEndTurn:
			took := formatTimestampDiff(msg.CreatedAt, finishData.Time)
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s · %s", modelName, took)),
			)
		case message.FinishReasonCanceled:
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s · canceled", modelName)),
			)
		case message.FinishReasonError:
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s · error", modelName)),
			)
		case message.FinishReasonPermissionDenied:
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s · permission denied", modelName)),
			)
		case message.FinishReasonMaxTokens:
			info = append(info, baseStyle.Width(max(1, width-1)).Foreground(t.Warning()).Render(" "+modelName+" · token limit"))
		case message.FinishReasonToolUse:
			info = append(info, baseStyle.Width(max(1, width-1)).Foreground(t.TextMuted()).Render(" "+modelName+" · tool use"))
		default:
			info = append(info, baseStyle.Width(max(1, width-1)).Foreground(t.TextMuted()).Render(" "+modelName+" · finished"))
		}
	}
	if content != "" || finished {
		if content == "" {
			content = finishStateMessage(finishData.Reason)
		}
		if isSummary {
			info = append(info, baseStyle.Width(width-1).Foreground(t.TextMuted()).Render(" (summary)"))
		}

		content = renderMessage(content, false, true, width, info...)
		messages = append(messages, uiMessage{
			ID:          msg.ID,
			messageType: assistantMessageType,
			position:    position,
			height:      lipgloss.Height(content),
			content:     content,
		})
		position += messages[0].height
		position++ // for the space
	} else if thinking {
		// Reasoning content can contain hidden model deliberation. Show only a
		// stable activity state while it is streaming; never place the raw
		// reasoning text in the terminal transcript.
		content = renderMessage("*Thinking…*", false, msg.ID == focusedUIMessageId, width)
		messages = append(messages, uiMessage{
			ID:          msg.ID,
			messageType: assistantMessageType,
			position:    position,
			height:      lipgloss.Height(content),
			content:     content,
		})
		position += messages[len(messages)-1].height + 1
	}

	for i, toolCall := range msg.ToolCalls() {
		toolCallContent := renderToolMessage(
			toolCall,
			allMessages,
			messagesService,
			focusedUIMessageId,
			false,
			width,
			i+1,
		)
		messages = append(messages, toolCallContent)
		position += toolCallContent.height
		position++ // for the space
	}
	return messages
}

func finishStateMessage(reason message.FinishReason) string {
	switch reason {
	case message.FinishReasonCanceled:
		return "*Response canceled — completed work is preserved.*"
	case message.FinishReasonError:
		return "*Response stopped because of an error.*"
	case message.FinishReasonPermissionDenied:
		return "*Response stopped because permission was denied.*"
	case message.FinishReasonMaxTokens:
		return "*Response stopped at the model's token limit.*"
	case message.FinishReasonToolUse:
		return "*Waiting for tool results…*"
	default:
		return "*Finished without output.*"
	}
}

func findToolResponse(toolCallID string, futureMessages []message.Message) *message.ToolResult {
	for _, msg := range futureMessages {
		for _, result := range msg.ToolResults() {
			if result.ToolCallID == toolCallID {
				return &result
			}
		}
	}
	return nil
}

func toolName(name string) string {
	switch name {
	case agent.AgentToolName:
		return "Task"
	case tools.BashToolName:
		return "Bash"
	case tools.EditToolName:
		return "Edit"
	case tools.FetchToolName:
		return "Fetch"
	case tools.GlobToolName:
		return "Glob"
	case tools.GrepToolName:
		return "Grep"
	case tools.LSToolName:
		return "List"
	case tools.SourcegraphToolName:
		return "Sourcegraph"
	case tools.ViewToolName:
		return "View"
	case tools.WriteToolName:
		return "Write"
	case tools.PatchToolName:
		return "Patch"
	}
	return name
}

func getToolAction(name string) string {
	switch name {
	case agent.AgentToolName:
		return "Preparing prompt..."
	case tools.BashToolName:
		return "Building command..."
	case tools.EditToolName:
		return "Preparing edit..."
	case tools.FetchToolName:
		return "Writing fetch..."
	case tools.GlobToolName:
		return "Finding files..."
	case tools.GrepToolName:
		return "Searching content..."
	case tools.LSToolName:
		return "Listing directory..."
	case tools.SourcegraphToolName:
		return "Searching code..."
	case tools.ViewToolName:
		return "Reading file..."
	case tools.WriteToolName:
		return "Preparing write..."
	case tools.PatchToolName:
		return "Preparing patch..."
	}
	return "Working..."
}

// renders params, params[0] (params[1]=params[2] ....)
func renderParams(paramsWidth int, params ...string) string {
	if len(params) == 0 || paramsWidth <= 0 {
		return ""
	}
	mainParam := ansi.Truncate(styles.SanitizeTerminalText(params[0]), paramsWidth, "…")

	if len(params) == 1 {
		return mainParam
	}
	otherParams := params[1:]
	// create pairs of key/value
	// if odd number of params, the last one is a key without value
	if len(otherParams)%2 != 0 {
		otherParams = append(otherParams, "")
	}
	parts := make([]string, 0, len(otherParams)/2)
	for i := 0; i < len(otherParams); i += 2 {
		key := styles.SanitizeTerminalText(otherParams[i])
		value := styles.SanitizeTerminalText(otherParams[i+1])
		if value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, value))
	}

	partsRendered := strings.Join(parts, ", ")
	remainingWidth := paramsWidth - lipgloss.Width(partsRendered) - 5 // for the space
	if remainingWidth < 30 {
		// No space for the params, just show the main
		return mainParam
	}

	if len(parts) > 0 {
		mainParam = fmt.Sprintf("%s (%s)", mainParam, strings.Join(parts, ", "))
	}

	return ansi.Truncate(mainParam, paramsWidth, "…")
}

func displayWorkspacePath(path string) string {
	path = styles.SanitizeTerminalText(path)
	if path == "" {
		return ""
	}

	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		// Relative paths already communicate their relationship to the
		// workspace. In particular, retain a leading ".." so an outside path
		// cannot be mistaken for a workspace-local file.
		return cleanPath
	}

	workingDir := filepath.Clean(config.WorkingDirectory())
	if !filepath.IsAbs(workingDir) {
		if absoluteWorkingDir, err := filepath.Abs(workingDir); err == nil {
			workingDir = absoluteWorkingDir
		}
	}
	rel, err := filepath.Rel(workingDir, cleanPath)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return rel
	}

	// Absolute paths outside the workspace remain absolute and explicit.
	return cleanPath
}

func renderToolParams(paramWidth int, toolCall message.ToolCall) string {
	params := ""
	switch toolCall.Name {
	case agent.AgentToolName:
		var params agent.AgentParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		prompt := strings.ReplaceAll(params.Prompt, "\n", " ")
		return renderParams(paramWidth, prompt)
	case tools.BashToolName:
		var params tools.BashParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		command := strings.ReplaceAll(params.Command, "\n", " ")
		return renderParams(paramWidth, command)
	case tools.EditToolName:
		var params tools.EditParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := displayWorkspacePath(params.FilePath)
		return renderParams(paramWidth, filePath)
	case tools.FetchToolName:
		var params tools.FetchParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		url := params.URL
		toolParams := []string{
			url,
		}
		if params.Format != "" {
			toolParams = append(toolParams, "format", params.Format)
		}
		if params.Timeout != 0 {
			toolParams = append(toolParams, "timeout", (time.Duration(params.Timeout) * time.Second).String())
		}
		return renderParams(paramWidth, toolParams...)
	case tools.GlobToolName:
		var params tools.GlobParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		pattern := params.Pattern
		toolParams := []string{
			pattern,
		}
		if params.Path != "" {
			toolParams = append(toolParams, "path", params.Path)
		}
		return renderParams(paramWidth, toolParams...)
	case tools.GrepToolName:
		var params tools.GrepParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		pattern := params.Pattern
		toolParams := []string{
			pattern,
		}
		if params.Path != "" {
			toolParams = append(toolParams, "path", params.Path)
		}
		if params.Include != "" {
			toolParams = append(toolParams, "include", params.Include)
		}
		if params.LiteralText {
			toolParams = append(toolParams, "literal", "true")
		}
		return renderParams(paramWidth, toolParams...)
	case tools.LSToolName:
		var params tools.LSParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		path := params.Path
		if path == "" {
			path = "."
		}
		return renderParams(paramWidth, path)
	case tools.SourcegraphToolName:
		var params tools.SourcegraphParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		return renderParams(paramWidth, params.Query)
	case tools.ViewToolName:
		var params tools.ViewParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := displayWorkspacePath(params.FilePath)
		toolParams := []string{
			filePath,
		}
		if params.Limit != 0 {
			toolParams = append(toolParams, "limit", fmt.Sprintf("%d", params.Limit))
		}
		if params.Offset != 0 {
			toolParams = append(toolParams, "offset", fmt.Sprintf("%d", params.Offset))
		}
		return renderParams(paramWidth, toolParams...)
	case tools.WriteToolName:
		var params tools.WriteParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := displayWorkspacePath(params.FilePath)
		return renderParams(paramWidth, filePath)
	default:
		input := strings.ReplaceAll(toolCall.Input, "\n", " ")
		params = renderParams(paramWidth, input)
	}
	return params
}

func truncateHeight(content string, height int) string {
	content = styles.SanitizeTerminalText(content)
	if height <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= height {
		return content
	}
	if height == 1 {
		return fmt.Sprintf("… %d lines omitted", len(lines))
	}

	// Reserve one row for the omission marker, then split the remaining
	// preview between the beginning and end. Command failures and tool results
	// commonly put the diagnosis or summary in their final lines.
	previewLines := height - 1
	headCount := (previewLines + 1) / 2
	tailCount := previewLines - headCount
	if tailCount == 0 {
		// Two-row previews still retain the final line by including it beside
		// the marker.
		omitted := len(lines) - 2
		return strings.Join([]string{
			lines[0],
			fmt.Sprintf("… %d lines omitted · last: %s", omitted, lines[len(lines)-1]),
		}, "\n")
	}

	omitted := len(lines) - headCount - tailCount
	preview := make([]string, 0, height)
	preview = append(preview, lines[:headCount]...)
	preview = append(preview, fmt.Sprintf("… %d lines omitted", omitted))
	preview = append(preview, lines[len(lines)-tailCount:]...)
	return strings.Join(preview, "\n")
}

func markdownFence(language, content string) string {
	content = styles.SanitizeTerminalText(content)
	longest := 0
	current := 0
	for _, r := range content {
		if r == '`' {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fmt.Sprintf("%s%s\n%s\n%s", fence, language, content, fence)
}

func renderToolResponse(toolCall message.ToolCall, response message.ToolResult, width int) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	if response.IsError {
		errContent := "Error: " + truncateHeight(response.Content, min(4, maxResultHeight))
		return baseStyle.
			Width(max(1, width)).
			Foreground(t.Error()).
			Render(errContent)
	}

	resultContent := truncateHeight(response.Content, maxResultHeight)
	switch toolCall.Name {
	case agent.AgentToolName:
		return styles.ForceReplaceBackgroundWithLipgloss(
			toMarkdown(resultContent, false, width),
			t.Background(),
		)
	case tools.BashToolName:
		resultContent = markdownFence("bash", resultContent)
		return styles.ForceReplaceBackgroundWithLipgloss(
			toMarkdown(resultContent, true, width),
			t.Background(),
		)
	case tools.EditToolName:
		metadata := tools.EditResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		formattedDiff, err := diff.FormatDiff(styles.SanitizeTerminalText(metadata.Diff), diff.WithTotalWidth(max(1, width)))
		if err != nil {
			return baseStyle.Width(max(1, width)).Foreground(t.Error()).Render("Unable to format diff: " + err.Error())
		}
		return truncateHeight(formattedDiff, maxResultHeight)
	case tools.FetchToolName:
		var params tools.FetchParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		mdFormat := "markdown"
		switch params.Format {
		case "text":
			mdFormat = "text"
		case "html":
			mdFormat = "html"
		}
		resultContent = markdownFence(mdFormat, resultContent)
		return styles.ForceReplaceBackgroundWithLipgloss(
			toMarkdown(resultContent, true, width),
			t.Background(),
		)
	case tools.GlobToolName:
		return baseStyle.Width(width).Foreground(t.TextMuted()).Render(resultContent)
	case tools.GrepToolName:
		return baseStyle.Width(width).Foreground(t.TextMuted()).Render(resultContent)
	case tools.LSToolName:
		return baseStyle.Width(width).Foreground(t.TextMuted()).Render(resultContent)
	case tools.SourcegraphToolName:
		return baseStyle.Width(width).Foreground(t.TextMuted()).Render(resultContent)
	case tools.ViewToolName:
		metadata := tools.ViewResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		ext := filepath.Ext(metadata.FilePath)
		if ext == "" {
			ext = ""
		} else {
			ext = strings.ToLower(ext[1:])
		}
		resultContent = markdownFence(ext, truncateHeight(metadata.Content, maxResultHeight))
		return styles.ForceReplaceBackgroundWithLipgloss(
			toMarkdown(resultContent, true, width),
			t.Background(),
		)
	case tools.WriteToolName:
		metadata := tools.WriteResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		if metadata.Diff != "" {
			formattedDiff, err := diff.FormatDiff(styles.SanitizeTerminalText(metadata.Diff), diff.WithTotalWidth(max(1, width)))
			if err == nil {
				return truncateHeight(formattedDiff, maxResultHeight)
			}
		}
		resultContent = markdownFence("text", resultContent)
		return styles.ForceReplaceBackgroundWithLipgloss(
			toMarkdown(resultContent, true, width),
			t.Background(),
		)
	case tools.PatchToolName:
		metadata := tools.PatchResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		files := make([]string, 0, len(metadata.FilesChanged))
		for _, file := range metadata.FilesChanged {
			files = append(files, displayWorkspacePath(file))
		}
		summary := fmt.Sprintf("%d files changed  +%d −%d", len(files), metadata.Additions, metadata.Removals)
		if len(files) > 0 {
			summary += "\n" + strings.Join(files, "\n")
		}
		return baseStyle.Width(max(1, width)).Foreground(t.TextMuted()).Render(truncateHeight(summary, maxResultHeight))
	default:
		resultContent = markdownFence("text", resultContent)
		return styles.ForceReplaceBackgroundWithLipgloss(
			toMarkdown(resultContent, true, width),
			t.Background(),
		)
	}
}

func renderToolMessage(
	toolCall message.ToolCall,
	allMessages []message.Message,
	messagesService message.Service,
	focusedUIMessageId string,
	nested bool,
	width int,
	position int,
) uiMessage {
	if nested {
		width = max(1, width-3)
	}
	width = max(1, width)

	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	style := baseStyle.
		Width(max(1, width-1)).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		PaddingLeft(1).
		BorderForeground(t.TextMuted())

	response := findToolResponse(toolCall.ID, allMessages)
	toolNameText := baseStyle.Foreground(t.TextMuted()).
		Render(fmt.Sprintf("%s: ", toolName(toolCall.Name)))
	availableWidth := max(0, width-2-lipgloss.Width(toolNameText))

	if !toolCall.Finished {
		// Get a brief description of what the tool is doing
		toolAction := getToolAction(toolCall.Name)

		progressText := baseStyle.
			Width(availableWidth).
			Foreground(t.TextMuted()).
			Render(fmt.Sprintf("%s", toolAction))

		content := style.Render(lipgloss.JoinHorizontal(lipgloss.Left, toolNameText, progressText))
		toolMsg := uiMessage{
			messageType: toolMessageType,
			position:    position,
			height:      lipgloss.Height(content),
			content:     content,
		}
		return toolMsg
	}

	params := renderToolParams(availableWidth, toolCall)
	responseContent := ""
	if response != nil {
		responseContent = renderToolResponse(toolCall, *response, max(1, width-2))
		responseContent = strings.TrimSuffix(responseContent, "\n")
	} else {
		responseContent = baseStyle.
			Italic(true).
			Width(max(1, width-2)).
			Foreground(t.TextMuted()).
			Render("Waiting for response...")
	}

	parts := []string{}
	if !nested {
		formattedParams := baseStyle.
			Width(availableWidth).
			Foreground(t.TextMuted()).
			Render(params)

		parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Left, toolNameText, formattedParams))
	} else {
		prefix := baseStyle.
			Foreground(t.TextMuted()).
			Render(" └ ")
		formattedParams := baseStyle.
			Width(availableWidth).
			Foreground(t.TextMuted()).
			Render(params)
		parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Left, prefix, toolNameText, formattedParams))
	}

	if toolCall.Name == agent.AgentToolName {
		taskMessages, _ := messagesService.List(context.Background(), toolCall.ID)
		toolCalls := []message.ToolCall{}
		for _, v := range taskMessages {
			toolCalls = append(toolCalls, v.ToolCalls()...)
		}
		for _, call := range toolCalls {
			rendered := renderToolMessage(call, taskMessages, messagesService, focusedUIMessageId, true, width, 0)
			parts = append(parts, rendered.content)
		}
	}
	if responseContent != "" && !nested {
		parts = append(parts, responseContent)
	}

	content := style.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			parts...,
		),
	)
	if nested {
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			parts...,
		)
	}
	toolMsg := uiMessage{
		ID:          toolCall.ID,
		messageType: toolMessageType,
		position:    position,
		height:      lipgloss.Height(content),
		content:     content,
	}
	return toolMsg
}

// Helper function to format the time difference between two Unix timestamps
func formatTimestampDiff(start, end int64) string {
	diffSeconds := end - start
	if diffSeconds <= 0 {
		return "<1s"
	}
	if diffSeconds < 60 {
		return fmt.Sprintf("%ds", diffSeconds)
	}
	minutes := diffSeconds / 60
	seconds := diffSeconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	}
	return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
}
