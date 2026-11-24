package dialog

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
)

func permissionResponse(t *testing.T, cmd tea.Cmd) PermissionResponseMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected permission response command")
	}
	msg := cmd()
	response, ok := msg.(PermissionResponseMsg)
	if !ok {
		t.Fatalf("permission command returned %T, want PermissionResponseMsg", msg)
	}
	return response
}

func TestPermissionDialogDefaultsConfirmationToDeny(t *testing.T) {
	dialog := NewPermissionDialogCmp().(*permissionDialogCmp)
	request := permission.PermissionRequest{ID: "permission-1", ToolName: "test"}
	dialog.SetPermissions(request)

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEnter})
	response := permissionResponse(t, cmd)
	if response.Action != PermissionDeny {
		t.Fatalf("default confirmation action = %q, want %q", response.Action, PermissionDeny)
	}
	if response.Permission.ID != request.ID {
		t.Fatalf("response permission ID = %q, want %q", response.Permission.ID, request.ID)
	}
}

func TestPermissionDialogResetsSelectionForEveryRequest(t *testing.T) {
	dialog := NewPermissionDialogCmp().(*permissionDialogCmp)
	dialog.SetPermissions(permission.PermissionRequest{ID: "permission-1", ToolName: "test"})

	// Right moves from the default Deny option to Allow.
	dialog.Update(tea.KeyMsg{Type: tea.KeyRight})
	_, allowCmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if response := permissionResponse(t, allowCmd); response.Action != PermissionAllow {
		t.Fatalf("selected action = %q, want %q", response.Action, PermissionAllow)
	}

	request := permission.PermissionRequest{ID: "permission-2", ToolName: "test"}
	dialog.SetPermissions(request)
	_, denyCmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEnter})
	response := permissionResponse(t, denyCmd)
	if response.Action != PermissionDeny {
		t.Fatalf("new request Enter action = %q, want %q", response.Action, PermissionDeny)
	}
	if response.Permission.ID != request.ID {
		t.Fatalf("response permission ID = %q, want %q", response.Permission.ID, request.ID)
	}
}

func TestPermissionDialogRequiresExplicitSelectionForApproval(t *testing.T) {
	dialog := NewPermissionDialogCmp().(*permissionDialogCmp)
	dialog.SetPermissions(permission.PermissionRequest{ID: "permission-1", ToolName: "test"})

	for _, shortcut := range []rune{'a', 's'} {
		_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{shortcut}})
		if cmd != nil {
			t.Fatalf("typing %q unexpectedly authorized an asynchronous permission prompt", shortcut)
		}
	}

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if response := permissionResponse(t, cmd); response.Action != PermissionDeny {
		t.Fatalf("escape action = %q, want %q", response.Action, PermissionDeny)
	}
}

func TestPermissionDialogPagerKeysNeverDecide(t *testing.T) {
	dialog := NewPermissionDialogCmp().(*permissionDialogCmp)
	dialog.SetPermissions(permission.PermissionRequest{ID: "permission-1", ToolName: "test"})
	dialog.contentViewPort.Width = 40
	dialog.contentViewPort.Height = 2
	dialog.contentViewPort.SetContent("one\ntwo\nthree\nfour\nfive")

	for _, keyMsg := range []tea.KeyMsg{
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'d'}},
	} {
		dialog.contentViewPort.GotoTop()
		_, cmd := dialog.Update(keyMsg)
		if cmd != nil {
			t.Fatalf("pager key %q unexpectedly returned a permission decision", keyMsg.String())
		}
		if dialog.contentViewPort.YOffset == 0 {
			t.Fatalf("pager key %q did not scroll the review", keyMsg.String())
		}
		if dialog.selectedOption != permissionOptionDeny {
			t.Fatalf("pager key %q changed selected option to %v", keyMsg.String(), dialog.selectedOption)
		}
	}
}

func TestPermissionDialogExplainsSessionGrantScopeAndScrollPosition(t *testing.T) {
	dialog := NewPermissionDialogCmp().(*permissionDialogCmp)
	dialog.width = 100
	dialog.contentViewPort.Width = 96
	dialog.contentViewPort.Height = 2
	dialog.contentViewPort.SetContent("one\ntwo\nthree\nfour")
	dialog.permission = permission.PermissionRequest{
		ToolName:  tools.BashToolName,
		Action:    "execute",
		ScopeKind: permission.ScopeDirectory,
		Path:      "/workspace",
	}

	notice := strings.Join(strings.Fields(ansi.Strip(dialog.renderSessionTrustNotice())), " ")
	for _, want := range []string{`tool "bash"`, `"execute"`, `directory scope "/workspace"`, "until this session ends"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("session trust notice %q does not contain %q", notice, want)
		}
	}
	if hint := ansi.Strip(dialog.renderReviewHint()); !strings.Contains(hint, "1-2/4") || !strings.Contains(hint, "space/pgup/pgdn page") {
		t.Fatalf("review hint does not expose keys and position: %q", hint)
	}

	dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	if hint := ansi.Strip(dialog.renderReviewHint()); !strings.Contains(hint, "2-3/4") {
		t.Fatalf("review hint did not advance with viewport: %q", hint)
	}
}

func TestPermissionDialogMarksSelectionWithoutColor(t *testing.T) {
	dialog := NewPermissionDialogCmp().(*permissionDialogCmp)
	dialog.width = 80

	buttons := dialog.renderButtons()
	if !strings.Contains(buttons, "> Deny") {
		t.Fatalf("default button rendering does not visibly mark Deny: %q", buttons)
	}
	if strings.Contains(buttons, "> Allow (a)") {
		t.Fatalf("default button rendering marks Allow: %q", buttons)
	}
}

func TestPermissionDialogFitsAndShowsPayloadAtCommonSizes(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		{Width: 20, Height: 8},
		{Width: 40, Height: 10},
		{Width: 80, Height: 24},
	} {
		dialog := NewPermissionDialogCmp().(*permissionDialogCmp)
		dialog.Update(size)
		dialog.SetPermissions(permission.PermissionRequest{
			ID:       "permission-1",
			ToolName: tools.BashToolName,
			Action:   "execute",
			Path:     "/workspace",
			Params:   tools.BashPermissionsParams{Command: "echo world"},
		})

		view := dialog.View()
		if size.Width >= 40 && !strings.Contains(ansi.Strip(view), "echo world") {
			t.Fatalf("%dx%d permission view hides command payload: %q", size.Width, size.Height, ansi.Strip(view))
		}
		if got := lipgloss.Width(view); got > size.Width {
			t.Fatalf("%dx%d permission view width = %d", size.Width, size.Height, got)
		}
	}
}
