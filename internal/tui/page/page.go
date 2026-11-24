package page

import tea "github.com/charmbracelet/bubbletea"

type PageID string

// PageChangeMsg is used to change the current page
type PageChangeMsg struct {
	ID PageID
}

// Lifecycle is implemented by pages that own recurring work. The root model
// activates only the visible page, allowing hidden pages to invalidate timers
// and cancel in-flight refreshes without losing their rendered state.
type Lifecycle interface {
	Activate() tea.Cmd
	Deactivate()
}
