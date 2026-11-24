package layout

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type overflowingContent struct{}

func (overflowingContent) Init() tea.Cmd                         { return nil }
func (m overflowingContent) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (overflowingContent) View() string {
	return strings.Repeat("\x1b[31m界long content\x1b[0m\n", 20)
}

func TestContainerKeepsContentAndChromeWithinAllocatedSize(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {0, 10}, {10, 0}, {1, 1}, {2, 2}, {5, 3}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			container := NewContainer(overflowingContent{}, WithBorderAll(), WithPaddingAll(1))
			container.SetSize(size[0], size[1])
			view := container.View()
			if size[0] == 0 || size[1] == 0 {
				if view != "" {
					t.Fatalf("zero-area container rendered %q", view)
				}
				return
			}
			if width, height := lipgloss.Size(view); width > size[0] || height > size[1] {
				t.Fatalf("rendered %dx%d exceeds allocation %dx%d", width, height, size[0], size[1])
			}
		})
	}
}
