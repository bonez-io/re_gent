package style

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestConfirmModelKeyboardSelection(t *testing.T) {
	model := newConfirmModel(lipgloss.NewRenderer(&bytes.Buffer{}), "Share?", "Never pushes.", 54)
	if model.yes {
		t.Fatal("Later must remain the safe initial selection")
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	model = updated.(confirmModel)
	if !model.yes {
		t.Fatal("left arrow did not select commit wiring")
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(confirmModel)
	if !model.done || !model.yes || cmd == nil {
		t.Fatalf("enter did not confirm commit wiring: done=%v yes=%v cmd=%v", model.done, model.yes, cmd)
	}
}

func TestConfirmModelDirectYesAndNo(t *testing.T) {
	for _, tc := range []struct {
		key  rune
		want bool
	}{{key: 'y', want: true}, {key: 'n', want: false}} {
		model := newConfirmModel(lipgloss.NewRenderer(&bytes.Buffer{}), "Share?", "Never pushes.", 54)
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
		got := updated.(confirmModel)
		if !got.done || got.yes != tc.want {
			t.Errorf("key %q: done=%v yes=%v, want yes=%v", tc.key, got.done, got.yes, tc.want)
		}
	}
}
