package dialog

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/tui/image"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

const (
	maxAttachmentSize = int64(5 * 1024 * 1024) // 5MB
	downArrow         = "down"
	upArrow           = "up"
)

type FilePrickerKeyMap struct {
	Enter          key.Binding
	Down           key.Binding
	Up             key.Binding
	Forward        key.Binding
	Backward       key.Binding
	OpenFilePicker key.Binding
	Esc            key.Binding
	InsertCWD      key.Binding
}

var filePickerKeyMap = FilePrickerKeyMap{
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select file/enter directory"),
	),
	Down: key.NewBinding(
		key.WithKeys("j", downArrow),
		key.WithHelp("↓/j", "down"),
	),
	Up: key.NewBinding(
		key.WithKeys("k", upArrow),
		key.WithHelp("↑/k", "up"),
	),
	Forward: key.NewBinding(
		key.WithKeys("l"),
		key.WithHelp("l", "enter directory"),
	),
	Backward: key.NewBinding(
		key.WithKeys("h", "backspace"),
		key.WithHelp("h/backspace", "go back"),
	),
	OpenFilePicker: key.NewBinding(
		key.WithKeys("f5"),
		key.WithHelp("f5", "open file picker"),
	),
	Esc: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close/exit"),
	),
	InsertCWD: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "manual path input"),
	),
}

type filepickerCmp struct {
	basePath       string
	width          int
	height         int
	cursor         int
	err            error
	cursorChain    stack
	viewport       viewport.Model
	dirs           []os.DirEntry
	cwdDetails     *DirNode
	selectedFile   string
	cwd            textinput.Model
	ShowFilePicker bool
	app            *app.App

	previewGeneration uint64
	previewPath       string
	previewLoader     func(width int, path string) (string, error)
}

type DirNode struct {
	parent    *DirNode
	child     *DirNode
	directory string
}
type stack []int

func (s stack) Push(v int) stack {
	return append(s, v)
}

func (s stack) Pop() (stack, int) {
	l := len(s)
	return s[:l-1], s[l-1]
}

type AttachmentAddedMsg struct {
	Attachment message.Attachment
}

// filePreviewResultMsg is produced by a Bubble Tea command after the preview
// has been rendered. Commands may complete out of order, so both the request
// generation and selected path travel with the result.
type filePreviewResultMsg struct {
	generation uint64
	path       string
	content    string
	err        error
}

func (f *filepickerCmp) Init() tea.Cmd {
	return nil
}

func (f *filepickerCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd, previewCmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.width = max(1, msg.Width)
		f.height = max(1, msg.Height)
		_, previewWidth, _, showPreview := f.layoutMetrics()
		if showPreview {
			f.viewport.Width = previewWidth
			f.viewport.Height = max(1, f.height-7)
		} else {
			f.viewport.Width = 0
			f.viewport.Height = 0
		}
		f.clampCursor()
		previewCmd = f.loadCurrentPreview()
	case filePreviewResultMsg:
		// Rendering happens asynchronously and older requests can finish after
		// the cursor or preview width has changed. Only the result belonging to
		// the current request and current selection may update model state.
		currentPath, _, selected := f.currentPreviewSelection()
		if msg.generation != f.previewGeneration ||
			msg.path != f.previewPath ||
			!selected || msg.path != currentPath {
			return f, nil
		}
		if msg.err != nil {
			logging.Error(msg.err.Error())
			f.viewport.SetContent("Preview unavailable")
			return f, nil
		}
		f.viewport.SetContent(msg.content)
		return f, nil
	case tea.KeyMsg:
		if f.cwd.Focused() {
			f.cwd, cmd = f.cwd.Update(msg)
		}
		switch {
		case key.Matches(msg, filePickerKeyMap.InsertCWD):
			f.cwd.Focus()
			return f, cmd
		case key.Matches(msg, filePickerKeyMap.Esc):
			if f.cwd.Focused() {
				f.cwd.Blur()
			}
		case key.Matches(msg, filePickerKeyMap.Down):
			if !f.cwd.Focused() || msg.String() == downArrow {
				if f.cursor < len(f.dirs)-1 {
					f.cursor++
					previewCmd = f.loadCurrentPreview()
				}
			}
		case key.Matches(msg, filePickerKeyMap.Up):
			if !f.cwd.Focused() || msg.String() == upArrow {
				if f.cursor > 0 {
					f.cursor--
					previewCmd = f.loadCurrentPreview()
				}
			}
		case key.Matches(msg, filePickerKeyMap.Enter):
			var path string
			var isPathDir bool
			if f.cwd.Focused() {
				path = f.cwd.Value()
				fileInfo, err := os.Stat(path)
				if err != nil {
					logging.ErrorPersist("Invalid path")
					return f, cmd
				}
				isPathDir = fileInfo.IsDir()
			} else {
				if !f.hasSelection() {
					return f, util.ReportWarn("No selectable files in this directory")
				}
				path = filepath.Join(f.cwdDetails.directory, "/", f.dirs[f.cursor].Name())
				isPathDir = f.dirs[f.cursor].IsDir()
			}
			if isPathDir {
				newWorkingDir := DirNode{parent: f.cwdDetails, directory: path}
				f.cwdDetails.child = &newWorkingDir
				f.cwdDetails = f.cwdDetails.child
				f.cursorChain = f.cursorChain.Push(f.cursor)
				f.dirs = readDir(f.cwdDetails.directory, false)
				f.cursor = 0
				f.cwd.SetValue(f.cwdDetails.directory)
				previewCmd = f.loadCurrentPreview()
			} else {
				f.selectedFile = path
				return f.addAttachmentToMessage()
			}
		case key.Matches(msg, filePickerKeyMap.Esc):
			if !f.cwd.Focused() {
				f.cursorChain = make(stack, 0)
				f.cursor = 0
			} else {
				f.cwd.Blur()
			}
		case key.Matches(msg, filePickerKeyMap.Forward):
			if !f.cwd.Focused() && f.hasSelection() {
				if f.dirs[f.cursor].IsDir() {
					path := filepath.Join(f.cwdDetails.directory, "/", f.dirs[f.cursor].Name())
					newWorkingDir := DirNode{parent: f.cwdDetails, directory: path}
					f.cwdDetails.child = &newWorkingDir
					f.cwdDetails = f.cwdDetails.child
					f.cursorChain = f.cursorChain.Push(f.cursor)
					f.dirs = readDir(f.cwdDetails.directory, false)
					f.cursor = 0
					f.cwd.SetValue(f.cwdDetails.directory)
					previewCmd = f.loadCurrentPreview()
				}
			}
		case key.Matches(msg, filePickerKeyMap.Backward):
			if !f.cwd.Focused() {
				if len(f.cursorChain) != 0 && f.cwdDetails.parent != nil {
					f.cursorChain, f.cursor = f.cursorChain.Pop()
					f.cwdDetails = f.cwdDetails.parent
					f.cwdDetails.child = nil
					f.dirs = readDir(f.cwdDetails.directory, false)
					f.clampCursor()
					f.cwd.SetValue(f.cwdDetails.directory)
					previewCmd = f.loadCurrentPreview()
				}
			}
		case key.Matches(msg, filePickerKeyMap.OpenFilePicker):
			f.dirs = readDir(f.cwdDetails.directory, false)
			f.cursor = 0
			previewCmd = f.loadCurrentPreview()
		}
	}
	return f, tea.Batch(cmd, previewCmd)
}

func (f *filepickerCmp) addAttachmentToMessage() (tea.Model, tea.Cmd) {
	modeInfo := GetSelectedModel(config.Get())
	if !modeInfo.SupportsAttachments {
		logging.ErrorPersist(fmt.Sprintf("Model %s doesn't support attachments", modeInfo.Name))
		return f, nil
	}

	selectedFilePath := f.selectedFile
	if !isExtSupported(selectedFilePath) {
		logging.ErrorPersist("Unsupported file")
		return f, nil
	}

	isFileLarge, err := image.ValidateFileSize(selectedFilePath, maxAttachmentSize)
	if err != nil {
		logging.ErrorPersist("unable to read the image")
		return f, nil
	}
	if isFileLarge {
		logging.ErrorPersist("file too large, max 5MB")
		return f, nil
	}

	content, err := os.ReadFile(selectedFilePath)
	if err != nil {
		logging.ErrorPersist("Unable read selected file")
		return f, nil
	}

	mimeBufferSize := min(512, len(content))
	mimeType := http.DetectContentType(content[:mimeBufferSize])
	fileName := filepath.Base(selectedFilePath)
	attachment := message.Attachment{FilePath: selectedFilePath, FileName: fileName, MimeType: mimeType, Content: content}
	f.selectedFile = ""
	return f, util.CmdHandler(AttachmentAddedMsg{attachment})
}

func (f *filepickerCmp) View() string {
	t := theme.CurrentTheme()
	adjustedWidth, previewWidth, maxVisibleDirs, showPreview := f.layoutMetrics()

	files := make([]string, 0, maxVisibleDirs)
	startIdx := 0

	if len(f.dirs) > maxVisibleDirs {
		halfVisible := maxVisibleDirs / 2
		if f.cursor >= halfVisible && f.cursor < len(f.dirs)-halfVisible {
			startIdx = f.cursor - halfVisible
		} else if f.cursor >= len(f.dirs)-halfVisible {
			startIdx = len(f.dirs) - maxVisibleDirs
		}
	}

	endIdx := min(startIdx+maxVisibleDirs, len(f.dirs))

	if len(f.dirs) == 0 {
		files = append(files, styles.BaseStyle().
			Foreground(t.TextMuted()).
			Width(adjustedWidth).
			Render(ansi.Truncate("No supported files in this directory", adjustedWidth, "…")))
	}

	for i := startIdx; i < endIdx; i++ {
		file := f.dirs[i]
		itemStyle := styles.BaseStyle().Width(max(1, adjustedWidth-2))

		if i == f.cursor {
			itemStyle = itemStyle.
				Background(t.Primary()).
				Foreground(t.Background()).
				Bold(true)
		}
		filename := sanitizeFilepickerLabel(file.Name())
		if file.IsDir() {
			filename = filename + "/"
		}
		filename = ansi.Truncate(filename, max(1, adjustedWidth-2), "…")

		files = append(files, itemStyle.Padding(0, 1).Render(filename))
	}

	// Keep the dialog height stable as the selection moves between directories.
	for len(files) < maxVisibleDirs {
		files = append(files, styles.BaseStyle().Width(adjustedWidth).Render(""))
	}

	f.cwd.Width = max(1, adjustedWidth-lipgloss.Width(f.cwd.Prompt))
	// Keep the real path in the input model for navigation, but render a copy
	// whose untrusted filesystem/user text cannot inject terminal controls.
	displayCWD := f.cwd
	displayCWD.SetValue(sanitizeFilepickerLabel(f.cwd.Value()))
	pathView := displayCWD.View()
	if f.cwd.Focused() && lipgloss.Width(pathView) > adjustedWidth {
		pathView = ansi.TruncateLeft(pathView, lipgloss.Width(pathView)-adjustedWidth+1, "…")
	} else {
		pathView = ansi.Truncate(pathView, adjustedWidth, "…")
	}
	currentPath := styles.BaseStyle().
		Width(adjustedWidth).
		Render(pathView)

	var insertExitText string
	if f.IsCWDFocused() {
		insertExitText = "Press esc to exit typing path"
	} else {
		insertExitText = "Press i to start typing path"
	}

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		currentPath,
		styles.BaseStyle().Width(adjustedWidth).Render(""),
		styles.BaseStyle().Width(adjustedWidth).Render(lipgloss.JoinVertical(lipgloss.Left, files...)),
		styles.BaseStyle().Width(adjustedWidth).Render(""),
		styles.BaseStyle().Foreground(t.TextMuted()).Width(adjustedWidth).
			Render(ansi.Truncate(insertExitText, adjustedWidth, "…")),
	)

	contentStyle := styles.BaseStyle().Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Width(adjustedWidth + 4)
	fileList := contentStyle.Render(content)

	if !showPreview {
		return fileList
	}

	f.viewport.Width = previewWidth
	f.viewport.Height = max(1, f.height-7)
	preview := lipgloss.NewStyle().
		Width(previewWidth + 4).
		Background(t.Background()).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.TextMuted()).
		BorderBackground(t.Background()).
		Padding(2).
		Render(f.viewport.View())

	return lipgloss.JoinHorizontal(lipgloss.Center, fileList, styles.BaseStyle().Render(" "), preview)
}

// layoutMetrics returns content widths rather than outer box widths. Border and
// padding consume six columns per pane, so the final rendered view stays inside
// the terminal. Preview is deliberately hidden at compact widths.
func (f *filepickerCmp) layoutMetrics() (listWidth, previewWidth, visibleDirs int, showPreview bool) {
	width := f.width
	if width <= 0 {
		width = 80
	}
	height := f.height
	if height <= 0 {
		height = 24
	}

	availableWidth := max(1, width-2)
	visibleDirs = max(1, min(20, height-8))
	showPreview = availableWidth >= 100
	if !showPreview {
		return max(1, availableWidth-6), 0, visibleDirs, false
	}

	leftOuterWidth := min(58, (availableWidth-1)/2)
	rightOuterWidth := availableWidth - leftOuterWidth - 1
	return max(1, leftOuterWidth-6), max(1, rightOuterWidth-6), visibleDirs, true
}

func (f *filepickerCmp) hasSelection() bool {
	return f.cursor >= 0 && f.cursor < len(f.dirs)
}

func (f *filepickerCmp) clampCursor() {
	if len(f.dirs) == 0 {
		f.cursor = 0
		return
	}
	f.cursor = max(0, min(f.cursor, len(f.dirs)-1))
}

type FilepickerCmp interface {
	tea.Model
	ToggleFilepicker(showFilepicker bool)
	IsCWDFocused() bool
}

func (f *filepickerCmp) ToggleFilepicker(showFilepicker bool) {
	f.ShowFilePicker = showFilepicker
}

func (f *filepickerCmp) IsCWDFocused() bool {
	return f.cwd.Focused()
}

func NewFilepickerCmp(app *app.App) FilepickerCmp {
	basePath := config.WorkingDirectory()
	if !isExistingDirectory(basePath) {
		var err error
		basePath, err = os.UserHomeDir()
		if err != nil {
			logging.Error("error loading user files")
			return nil
		}
	}
	baseDir := DirNode{parent: nil, directory: basePath}
	dirs := readDir(basePath, false)
	viewport := viewport.New(0, 0)
	currentDirectory := textinput.New()
	currentDirectory.CharLimit = 200
	currentDirectory.Width = 44
	currentDirectory.Cursor.Blink = true
	currentDirectory.SetValue(baseDir.directory)
	return &filepickerCmp{
		cwdDetails:    &baseDir,
		dirs:          dirs,
		cursorChain:   make(stack, 0),
		viewport:      viewport,
		cwd:           currentDirectory,
		app:           app,
		previewLoader: image.ImagePreview,
	}
}

func isExistingDirectory(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func sanitizeFilepickerLabel(value string) string {
	value = styles.SanitizeTerminalText(value)
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(value)
}

func (f *filepickerCmp) currentPreviewSelection() (path string, width int, ok bool) {
	if f.viewport.Width < 5 || !f.hasSelection() || f.cwdDetails == nil {
		return "", 0, false
	}

	entry := f.dirs[f.cursor]
	if entry.IsDir() || !isExtSupported(entry.Name()) {
		return "", 0, false
	}
	return filepath.Join(f.cwdDetails.directory, entry.Name()), f.viewport.Width - 4, true
}

// loadCurrentPreview prepares a preview request without doing image work in
// Update. Bubble Tea runs the returned command asynchronously and sends the
// tagged result back through Update, which keeps the UI responsive and model
// mutation single-threaded.
func (f *filepickerCmp) loadCurrentPreview() tea.Cmd {
	f.previewGeneration++
	f.previewPath = ""

	path, width, ok := f.currentPreviewSelection()
	if !ok {
		if f.viewport.Width < 5 {
			f.viewport.SetContent("Preview hidden at this terminal width")
		} else {
			f.viewport.SetContent("Preview unavailable")
		}
		return nil
	}

	generation := f.previewGeneration
	f.previewPath = path
	f.viewport.SetContent("Loading preview...")
	loader := f.previewLoader
	if loader == nil {
		loader = image.ImagePreview
	}
	return func() tea.Msg {
		content, err := loader(width, path)
		return filePreviewResultMsg{
			generation: generation,
			path:       path,
			content:    content,
			err:        err,
		}
	}
}

func readDir(path string, showHidden bool) []os.DirEntry {
	logging.Info(fmt.Sprintf("Reading directory: %s", path))

	entriesChan := make(chan []os.DirEntry, 1)
	errChan := make(chan error, 1)

	go func() {
		dirEntries, err := os.ReadDir(path)
		if err != nil {
			logging.ErrorPersist(err.Error())
			errChan <- err
			return
		}
		entriesChan <- dirEntries
	}()

	select {
	case dirEntries := <-entriesChan:
		sort.Slice(dirEntries, func(i, j int) bool {
			if dirEntries[i].IsDir() == dirEntries[j].IsDir() {
				return dirEntries[i].Name() < dirEntries[j].Name()
			}
			return dirEntries[i].IsDir()
		})

		if showHidden {
			return dirEntries
		}

		var sanitizedDirEntries []os.DirEntry
		for _, dirEntry := range dirEntries {
			isHidden, _ := IsHidden(dirEntry.Name())
			if !isHidden {
				if dirEntry.IsDir() || isExtSupported(dirEntry.Name()) {
					sanitizedDirEntries = append(sanitizedDirEntries, dirEntry)
				}
			}
		}

		return sanitizedDirEntries

	case err := <-errChan:
		logging.ErrorPersist(fmt.Sprintf("Error reading directory %s", path), err)
		return []os.DirEntry{}

	case <-time.After(5 * time.Second):
		logging.ErrorPersist(fmt.Sprintf("Timeout reading directory %s", path), nil)
		return []os.DirEntry{}
	}
}

func IsHidden(file string) (bool, error) {
	return strings.HasPrefix(file, "."), nil
}

func isExtSupported(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return (ext == ".jpg" || ext == ".jpeg" || ext == ".webp" || ext == ".png")
}
