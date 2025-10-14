package chat

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	ansiReset     = "\033[0m"
	ansiPrompt    = "\033[38;5;180m"
	ansiName      = "\033[38;5;81m"
	ansiJoin      = "\033[38;5;47m"
	ansiLeave     = "\033[38;5;203m"
	ansiSystem    = "\033[38;5;213m"
	ansiError     = "\033[38;5;204m"
	ansiMessage   = "\033[38;5;251m"
	ansiOwnBody   = "\033[38;5;159m"
	ansiTimestamp = "\033[38;5;239m"
	borderSystem  = "\033[38;5;140m"
	borderOther   = "\033[38;5;24m"
	borderSelf    = "\033[38;5;39m"
)

// runBubbleUI starts the Bubble Tea interface and blocks until it exits.
func runBubbleUI(user string, events <-chan Message, submit func(string) error) error {
	m := newBubbleModel(user, events, submit)
	program := tea.NewProgram(m)
	_, err := program.Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, errQuit) {
		return nil
	}
	return err
}

// bubbleModel implements tea.Model and consumes chat events.
type bubbleModel struct {
	user     string
	input    []rune
	history  []block
	events   <-chan Message
	submit   func(string) error
	quitting bool
}

// newBubbleModel constructs the Bubble Tea state machine for the chat UI.
func newBubbleModel(user string, events <-chan Message, submit func(string) error) *bubbleModel {
	return &bubbleModel{
		user:    user,
		events:  events,
		submit:  submit,
		history: make([]block, 0, 256),
	}
}

// Init requests the first message from the event stream.
func (m *bubbleModel) Init() tea.Cmd {
	return waitForEvent(m.events)
}

// waitForEvent blocks until the next chat event or closure.
func waitForEvent(events <-chan Message) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return tea.Quit
		}
		return msg
	}
}

// Update handles key presses, incoming messages, and terminal signals.
func (m *bubbleModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(string(m.input))
			m.input = m.input[:0]
			if text != "" && m.submit != nil {
				if err := m.submit(text); err != nil && !errors.Is(err, errQuit) {
					m.append(renderSystem(err.Error()))
				}
			}
			return m, nil
		case tea.KeyBackspace, tea.KeyCtrlH:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
			return m, nil
		default:
			if s := msg.String(); s != "" && len([]rune(s)) == 1 && !msg.Alt {
				m.input = append(m.input, []rune(s)[0])
			}
			return m, nil
		}
	case Message:
		switch msg.Type {
		case promptMsg:
			if trimmed := strings.TrimSpace(msg.Body); trimmed != "" {
				m.user = trimmed
			}
			return m, waitForEvent(m.events)
		}
		m.append(renderMessage(m.user, msg))
		return m, waitForEvent(m.events)
	case tea.WindowSizeMsg:
		return m, nil
	case tea.QuitMsg:
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// View renders the chat history and input prompt.
func (m *bubbleModel) View() string {
	var b strings.Builder
	for _, blk := range m.history {
		b.WriteString(renderBlockString(blk))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(fmt.Sprintf("%s▸ %s%s %s", ansiPrompt, m.user, ansiReset, string(m.input)))
	return b.String()
}

// append adds a formatted block to the scrollback, coalescing similar entries.
func (m *bubbleModel) append(blk block) {
	if len(m.history) > 0 {
		last := m.history[len(m.history)-1]
		if last.key == blk.key && blk.timestamp.Sub(last.timestamp) <= groupWindow {
			last.lines = append(last.lines, blk.lines...)
			last.timestamp = blk.timestamp
			m.history[len(m.history)-1] = last
			return
		}
	}
	if len(m.history) > 500 {
		m.history = m.history[len(m.history)-500:]
	}
	m.history = append(m.history, blk)
}

// renderSystem formats a system notification block.
func renderSystem(text string) block {
	header := fmt.Sprintf("%s[%s]%s system", ansiTimestamp, time.Now().Format("15:04:05"), ansiReset)
	lines := strings.Split(text, "\n")
	colored := make([]string, len(lines))
	for i, line := range lines {
		colored[i] = ansiSystem + line + ansiReset
	}
	return block{key: "system", border: borderSystem, header: header, lines: colored, timestamp: time.Now()}
}

// renderMessage styles an incoming application message for display.
func renderMessage(user string, msg Message) block {
	ts := msg.Timestamp
	if ts == 0 {
		ts = time.Now().Unix()
	}
	timestamp := time.Unix(ts, 0).Format("15:04:05")

	border := borderOther
	bodyColor := ansiMessage
	label := fmt.Sprintf("@%s", msg.From)
	labelColor := ansiName

	switch msg.Type {
	case chatMsg:
		if msg.From == user {
			border = borderSelf
			bodyColor = ansiOwnBody
		}
	case joinMsg:
		border = borderSystem
		label = "status"
		labelColor = ansiSystem
		bodyColor = ansiJoin
	case leaveMsg:
		border = borderSystem
		label = "status"
		labelColor = ansiSystem
		bodyColor = ansiLeave
	case errorMsg:
		border = borderSystem
		label = "error"
		labelColor = ansiSystem
		bodyColor = ansiError
	case systemMsg:
		border = borderSystem
		label = "system"
		labelColor = ansiSystem
		bodyColor = ansiSystem
	default:
		border = borderSystem
		label = strings.ToUpper(string(msg.Type))
		labelColor = ansiSystem
	}

	header := fmt.Sprintf("%s[%s]%s %s%s%s", ansiTimestamp, timestamp, ansiReset, labelColor, label, ansiReset)
	lines := messageLines(msg.Type, msg.From, msg.Body, bodyColor)
	key := string(msg.Type)
	if msg.Type == chatMsg {
		key += ":" + msg.From
	}
	return block{key: key, border: border, header: header, lines: lines, timestamp: time.Unix(ts, 0)}
}

// messageLines splits and colorizes a message body by type.
func messageLines(kind msgType, from, body, color string) []string {
	var text string
	switch kind {
	case chatMsg:
		text = body
		if text == "" {
			text = "[empty message]"
		}
	case joinMsg:
		text = fmt.Sprintf("%s joined the chat", from)
	case leaveMsg:
		text = fmt.Sprintf("%s left the chat", from)
	case errorMsg, systemMsg:
		text = body
		if text == "" {
			text = "notification"
		}
	default:
		text = body
		if text == "" {
			text = fmt.Sprintf("(%s)", kind)
		}
	}
	raw := strings.Split(text, "\n")
	lines := make([]string, len(raw))
	for i, line := range raw {
		if line == "" {
			line = " "
		}
		lines[i] = color + line + ansiReset
	}
	return lines
}

const groupWindow = 30 * time.Second

type block struct {
	key       string
	border    string
	header    string
	lines     []string
	timestamp time.Time
}

// renderBlockString assembles the ANSI bordered block string for output.
func renderBlockString(blk block) string {
	var b strings.Builder
	b.WriteString(blk.border)
	b.WriteString("┌ ")
	b.WriteString(blk.header)
	b.WriteString("\n")
	for _, line := range blk.lines {
		b.WriteString(blk.border)
		b.WriteString("│ ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(blk.border)
	b.WriteString("└")
	b.WriteString(ansiReset)
	return b.String()
}
