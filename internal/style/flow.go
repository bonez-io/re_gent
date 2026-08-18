package style

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

const (
	flowPurple = lipgloss.Color("#9B59D0")
	flowBlue   = lipgloss.Color("#5B7FFF")
	flowGreen  = lipgloss.Color("#10B981")
	flowAmber  = lipgloss.Color("#F59E0B")
	flowRed    = lipgloss.Color("#EF4444")
	flowMuted  = lipgloss.Color("#7C8498")
)

// Flow renders onboarding as an inline TUI. On a real terminal it uses a
// persistent progress rail, panels, rich type hierarchy, and animated task
// rows. In a pipe, test, or CI log it becomes stable line-oriented text with no
// cursor control or ANSI sequences.
type Flow struct {
	out      io.Writer
	live     bool
	width    int
	renderer *lipgloss.Renderer
}

type terminalWriter interface {
	Fd() uintptr
}

func NewFlow(out io.Writer) *Flow {
	if out == nil {
		out = io.Discard
	}
	f := &Flow{out: out, width: 54, renderer: lipgloss.NewRenderer(out)}
	if file, ok := out.(terminalWriter); ok && term.IsTerminal(file.Fd()) && os.Getenv("TERM") != "dumb" {
		f.live = true
		if width, _, err := term.GetSize(file.Fd()); err == nil {
			f.width = min(max(width-6, 34), 64)
		}
	}
	return f
}

// Header opens the flow with a compact brand card. Terminal fonts are owned by
// the user's emulator, so hierarchy comes from weight, case, spacing, color,
// and borders rather than pretending the CLI can replace their font.
func (f *Flow) Header(action, subject string) {
	if !f.live {
		fmt.Fprintf(f.out, "\n  re_gent / %s\n", action)
		if subject != "" {
			fmt.Fprintf(f.out, "  %s\n", subject)
		}
		fmt.Fprintln(f.out)
		return
	}

	r := f.renderer
	mark := r.NewStyle().Foreground(flowPurple).Bold(true).Render("◆")
	brand := r.NewStyle().Foreground(flowPurple).Bold(true).Render("RE_GENT")
	tagline := ""
	if f.width >= 48 {
		tagline = "  " + r.NewStyle().Foreground(flowMuted).Render("AGENT VERSION CONTROL")
	}
	badge := r.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(flowBlue).
		Bold(true).
		Padding(0, 1).
		Render(strings.ToUpper(action))

	top := mark + "  " + brand + tagline
	bottom := badge
	if subject != "" {
		available := max(8, f.width-lipgloss.Width(badge)-4)
		bottom += "  " + r.NewStyle().Bold(true).Render(ansi.Truncate(subject, available, "…"))
	}
	card := r.NewStyle().
		Width(f.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(flowPurple).
		Padding(0, 1).
		Render(top + "\n" + bottom)
	fmt.Fprintf(f.out, "\n%s\n\n", card)
}

// Run presents one animated unit of work. A live terminal keeps the animation
// visible briefly even when local work is instant; without that floor the UI
// flashes between states too quickly to read and feels like styled logging.
// Non-terminal execution never waits.
func (f *Flow) Run(label string, action func() error) error {
	return f.run(label, action, true)
}

// Wait animates work but lets the caller print a more specific result derived
// from the operation. It avoids vague duplicate rows such as "configured" and
// then "Claude + Codex configured" while preserving the loading experience.
func (f *Flow) Wait(label string, action func() error) error {
	return f.run(label, action, false)
}

func (f *Flow) run(label string, action func() error, persist bool) error {
	if action == nil {
		if persist {
			f.Step(label)
		}
		return nil
	}
	if !f.live {
		err := action()
		if err != nil {
			f.Failure(label)
			return err
		}
		if persist {
			f.Step(label)
		}
		return nil
	}

	done := make(chan error, 1)
	go func() {
		started := time.Now()
		err := action()
		if remaining := 320*time.Millisecond - time.Since(started); remaining > 0 {
			time.Sleep(remaining)
		}
		done <- err
	}()

	model := newTaskModel(f.renderer, label, done)
	result, programErr := tea.NewProgram(model, tea.WithOutput(f.out), tea.WithInput(nil)).Run()
	if programErr != nil {
		f.Failure(label)
		return programErr
	}
	final, ok := result.(taskModel)
	if !ok {
		f.Failure(label)
		return fmt.Errorf("terminal task ended unexpectedly")
	}
	if final.err != nil {
		f.Failure(label)
		return final.err
	}
	if persist {
		f.Step(label)
	}
	return nil
}

func (f *Flow) Step(label string) {
	if !f.live {
		fmt.Fprintf(f.out, "  ✓ %s\n", label)
		return
	}
	fmt.Fprintf(f.out, "  %s  %s  %s\n",
		f.renderer.NewStyle().Foreground(flowPurple).Render("│"),
		f.renderer.NewStyle().Foreground(flowGreen).Bold(true).Render("✓"),
		f.renderer.NewStyle().Bold(true).Render(label),
	)
}

func (f *Flow) Failure(label string) {
	if !f.live {
		fmt.Fprintf(f.out, "  ✗ %s\n", label)
		return
	}
	fmt.Fprintf(f.out, "  %s  %s  %s\n",
		f.renderer.NewStyle().Foreground(flowPurple).Render("│"),
		f.renderer.NewStyle().Foreground(flowRed).Bold(true).Render("✗"),
		f.renderer.NewStyle().Foreground(flowRed).Bold(true).Render(label),
	)
}

func (f *Flow) Warning(label string) {
	if !f.live {
		fmt.Fprintf(f.out, "  ⚠ %s\n", label)
		return
	}
	fmt.Fprintf(f.out, "  %s  %s  %s\n",
		f.renderer.NewStyle().Foreground(flowPurple).Render("│"),
		f.renderer.NewStyle().Foreground(flowAmber).Bold(true).Render("!"),
		f.renderer.NewStyle().Foreground(flowAmber).Render(label),
	)
}

func (f *Flow) Detail(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	if !f.live {
		fmt.Fprintf(f.out, "  %-9s %s\n", label, value)
		return
	}
	key := f.renderer.NewStyle().Foreground(flowBlue).Bold(true).Width(9).Render(strings.ToUpper(label))
	value = f.renderer.NewStyle().Foreground(flowMuted).Render(value)
	fmt.Fprintf(f.out, "  %s     %s %s\n", f.renderer.NewStyle().Foreground(flowPurple).Render("│"), key, value)
}

func (f *Flow) Complete(label string) {
	if !f.live {
		fmt.Fprintf(f.out, "\n  ✓ %s\n", label)
		return
	}
	ready := f.renderer.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(flowGreen).
		Bold(true).
		Padding(0, 1).
		Render("READY")
	message := f.renderer.NewStyle().Bold(true).Render(label)
	panel := f.renderer.NewStyle().
		Width(f.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(flowGreen).
		Padding(0, 1).
		Render(ready + "  " + message)
	fmt.Fprintf(f.out, "\n%s\n", panel)
}

func (f *Flow) Next(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if !f.live {
		fmt.Fprintf(f.out, "  Next %s\n", text)
		return
	}
	arrow := f.renderer.NewStyle().Foreground(flowBlue).Bold(true).Render("→")
	next := f.renderer.NewStyle().Foreground(flowBlue).Bold(true).Render("NEXT")
	fmt.Fprintf(f.out, "  %s  %s  %s\n", arrow, next, text)
}

func (f *Flow) Hint(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if !f.live {
		fmt.Fprintf(f.out, "  %s\n", text)
		return
	}
	fmt.Fprintf(f.out, "     %s\n", f.renderer.NewStyle().Foreground(flowMuted).Italic(true).Render(text))
}

func (f *Flow) End() { fmt.Fprintln(f.out) }

type taskResultMsg struct{ err error }

type taskModel struct {
	spinner spinner.Model
	label   string
	done    <-chan error
	result  bool
	err     error
	style   lipgloss.Style
}

func newTaskModel(renderer *lipgloss.Renderer, label string, done <-chan error) taskModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = renderer.NewStyle().Foreground(flowPurple)
	return taskModel{
		spinner: s,
		label:   label,
		done:    done,
		style:   renderer.NewStyle().Foreground(flowMuted),
	}
}

func (m taskModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, func() tea.Msg { return taskResultMsg{err: <-m.done} })
}

func (m taskModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case taskResultMsg:
		m.result = true
		m.err = msg.err
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m taskModel) View() string {
	if m.result {
		return ""
	}
	return fmt.Sprintf("  %s  %s  %s", "│", m.spinner.View(), m.style.Render(m.label))
}
