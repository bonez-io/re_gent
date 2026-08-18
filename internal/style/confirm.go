package style

import (
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

// Confirm opens a compact inline choice card. It deliberately stays out of the
// alternate screen so the setup history remains visible above the decision.
func Confirm(in io.Reader, out io.Writer, title, detail string) (bool, error) {
	width := 54
	if file, ok := out.(terminalWriter); ok {
		if terminalWidth, _, err := term.GetSize(file.Fd()); err == nil {
			width = min(max(terminalWidth-6, 34), 64)
		}
	}
	model := newConfirmModel(lipgloss.NewRenderer(out), title, detail, width)
	result, err := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return false, err
	}
	final, ok := result.(confirmModel)
	if !ok {
		return false, fmt.Errorf("terminal confirmation ended unexpectedly")
	}
	return final.yes, nil
}

type confirmModel struct {
	renderer *lipgloss.Renderer
	title    string
	detail   string
	width    int
	yes      bool
	done     bool
}

func newConfirmModel(renderer *lipgloss.Renderer, title, detail string, width int) confirmModel {
	return confirmModel{renderer: renderer, title: title, detail: detail, width: width}
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "left":
		m.yes = true
	case "right":
		m.yes = false
	case "tab", "shift+tab":
		m.yes = !m.yes
	case "y":
		m.yes, m.done = true, true
		return m, tea.Quit
	case "n", "esc", "q", "ctrl+c":
		m.yes, m.done = false, true
		return m, tea.Quit
	case "enter":
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m confirmModel) View() string {
	if m.done {
		return ""
	}
	r := m.renderer
	title := r.NewStyle().Bold(true).Render(m.title)
	detail := r.NewStyle().Foreground(flowMuted).Render(m.detail)
	yes := confirmChoice(r, "YES, COMMIT WIRING", m.yes)
	no := confirmChoice(r, "LATER", !m.yes)
	help := r.NewStyle().Foreground(flowMuted).Render("←/→ choose  •  enter confirm")
	body := strings.Join([]string{title, detail, "", yes + "  " + no, help}, "\n")
	return "\n" + r.NewStyle().
		Width(m.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(flowBlue).
		Padding(0, 1).
		Render(body) + "\n"
}

func confirmChoice(r *lipgloss.Renderer, label string, selected bool) string {
	style := r.NewStyle().Bold(true).Padding(0, 1)
	if selected {
		return style.Foreground(lipgloss.Color("#FFFFFF")).Background(flowBlue).Render(label)
	}
	return style.Foreground(flowMuted).Render(label)
}
