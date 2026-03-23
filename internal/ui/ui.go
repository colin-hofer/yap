package ui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/atotto/clipboard"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"yap/internal/app"
	"yap/internal/emoji"
	"yap/internal/model"
	"yap/internal/update"
	"yap/internal/version"
)

var (
	colorMuted   = lipgloss.Color("#6A7A87")
	colorAccent  = lipgloss.Color("#7FDBB6")
	colorAccent2 = lipgloss.Color("#F2C879")
	colorBorder  = lipgloss.Color("#2A3842")
	colorFocus   = lipgloss.Color("#4A9E80")
	colorStrong  = lipgloss.Color("#E8F0F2")
	colorDanger  = lipgloss.Color("#F16E5B")
	colorSelf    = lipgloss.Color("#9AD1FF")
	colorPeer    = lipgloss.Color("#D4DDE0")
	colorJoin    = lipgloss.Color("#9BE48D")
	colorLeave   = lipgloss.Color("#FFB199")
	colorStale   = lipgloss.Color("#E0BE75")
	colorSelBg   = lipgloss.Color("#1E3040")
	colorCodeBg  = lipgloss.Color("#1A2530")
	colorCodeFg  = lipgloss.Color("#C8D8E8")

	panelStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Padding(1, 2)
	focusedPanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorFocus).Padding(1, 2)
	modalStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(1, 2)
	titleStyle        = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle        = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle       = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	statusStyle       = lipgloss.NewStyle().Foreground(colorAccent2)
	dangerStyle       = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
	selectedStyle     = lipgloss.NewStyle().Foreground(colorStrong).Background(colorSelBg).Padding(0, 1)
)

// Run starts the Bubble Tea program.
func Run(service *app.Service) (RunResult, error) {
	m := newModel(service)
	program := tea.NewProgram(m)
	finalModel, err := program.Run()
	shutdownErr := service.Shutdown()
	if err != nil {
		return RunResult{}, err
	}
	if shutdownErr != nil {
		return RunResult{}, shutdownErr
	}
	result := RunResult{}
	if model, ok := finalModel.(*modelUI); ok {
		result.UpdateRequested = model.updateRequested
	}
	return result, nil
}

type RunResult struct {
	UpdateRequested bool
}

type modelUI struct {
	service *app.Service
	state   app.State
	events  <-chan app.Event

	width  int
	height int

	mode      string
	focus     string
	swarmIdx  int
	nearbyIdx int
	status    string

	updateRequested   bool
	checkingUpdate    bool
	keyDisambiguation bool

	typingFrame   int
	typingTicking bool

	transitionFrame int

	emojiResults []emoji.Entry
	emojiIdx     int

	messages    viewport.Model
	composer    textarea.Model
	prompt      textinput.Model
	modal       modalState
	modalQueue  []modalState
	hasNewBelow bool
}

type modalState struct {
	Kind      string
	Title     string
	Message   string
	Approval  *app.Event
	Invite    *model.Invite
	SwarmID   string
	SwarmName string
}

type updateCheckMsg struct {
	Result update.Result
	Err    error
}

type copyResultMsg struct {
	Message string
}

type typingTickMsg struct{}
type transitionTickMsg struct{}

type transcriptRender struct {
	Lines []string
}

func newModel(service *app.Service) *modelUI {
	composer := textarea.New()
	composer.Placeholder = "Type a message..."
	composer.CharLimit = 4000
	composer.SetHeight(3)
	composer.ShowLineNumbers = false

	// Remap InsertNewline from enter to shift+enter so enter can send.
	km := composer.KeyMap
	km.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))
	composer.KeyMap = km

	styles := composer.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	composer.SetStyles(styles)
	composer.Blur()

	messages := viewport.New()
	messages.SoftWrap = true
	messages.MouseWheelEnabled = true
	messages.MouseWheelDelta = 2
	messages.LeftGutterFunc = func(ctx viewport.GutterContext) string {
		return " "
	}

	prompt := textinput.New()
	prompt.Placeholder = "Type here"
	prompt.Focus()

	state := service.Snapshot()
	mode := "home"
	focus := "swarms"
	if state.SelectedSwarm != nil {
		mode = "chat"
		focus = "composer"
		composer.Focus()
	}
	model := &modelUI{
		service:  service,
		state:    state,
		events:   service.Events(),
		mode:     mode,
		focus:    focus,
		status:   "ready",
		messages: messages,
		composer: composer,
		prompt:   prompt,
	}
	return model
}

func (m *modelUI) Init() tea.Cmd {
	return waitForAppEvent(m.events)
}

func waitForAppEvent(events <-chan app.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return tea.Quit()
		}
		return event
	}
}

func checkForUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		updater, err := update.New(update.Config{
			RepoOwner:      version.RepositoryOwner,
			RepoName:       version.RepositoryName,
			BinaryName:     version.BinaryName,
			CurrentVersion: version.Current(),
		})
		if err != nil {
			return updateCheckMsg{Err: err}
		}
		result, err := updater.Check(context.Background())
		return updateCheckMsg{Result: result, Err: err}
	}
}

func typingTickCmd() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
		return typingTickMsg{}
	})
}

func transitionTickCmd() tea.Cmd {
	return tea.Tick(60*time.Millisecond, func(time.Time) tea.Msg {
		return transitionTickMsg{}
	})
}

func (m *modelUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncLayout()
		if m.mode == "chat" {
			m.syncTranscript(false)
		}
		return m, nil
	case tea.KeyboardEnhancementsMsg:
		m.keyDisambiguation = msg.SupportsKeyDisambiguation()
		return m, nil
	case app.Event:
		cmd := waitForAppEvent(m.events)
		follow := m.applyAppEvent(msg)
		return m, tea.Batch(cmd, follow)
	case updateCheckMsg:
		m.checkingUpdate = false
		if msg.Err != nil {
			m.status = msg.Err.Error()
			return m, nil
		}
		if !msg.Result.Available {
			m.status = fmt.Sprintf("already up to date (%s)", displayVersion(msg.Result.LatestVersion))
			return m, nil
		}
		m.pushModal(modalState{
			Kind:  "update",
			Title: "Install Update",
			Message: fmt.Sprintf(
				"Update %s -> %s is available.\n\nQuit yap and install the latest GitHub release for this OS/arch?",
				displayVersion(msg.Result.PreviousVersion),
				displayVersion(msg.Result.LatestVersion),
			),
		})
		return m, nil
	case typingTickMsg:
		m.typingFrame = (m.typingFrame + 1) % 3
		if m.anyoneTyping() && m.mode == "chat" {
			return m, typingTickCmd()
		}
		m.typingTicking = false
		return m, nil
	case transitionTickMsg:
		if m.transitionFrame > 0 {
			m.transitionFrame--
			if m.transitionFrame > 0 {
				return m, transitionTickCmd()
			}
		}
		return m, nil
	case copyResultMsg:
		m.status = msg.Message
		return m, nil
	case tea.MouseWheelMsg:
		if m.mode == "chat" && m.modal.Kind == "" {
			var cmd tea.Cmd
			m.messages, cmd = m.messages.Update(msg)
			if m.messages.AtBottom() {
				m.hasNewBelow = false
			}
			return m, cmd
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.modal.Kind != "" {
			return m.handleModal(msg)
		}
		if m.mode == "chat" {
			return m.handleChat(msg)
		}
		return m.handleHome(msg)
	}
	if m.modal.Kind == "new" || m.modal.Kind == "join" || m.modal.Kind == "rename" {
		var cmd tea.Cmd
		m.prompt, cmd = m.prompt.Update(msg)
		return m, cmd
	}
	if m.mode == "chat" {
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *modelUI) View() tea.View {
	width := m.width
	height := m.height
	if width == 0 {
		width = 110
	}
	if height == 0 {
		height = 34
	}

	base := lipgloss.NewStyle().Foreground(colorStrong).Width(width).Height(height)
	var content string
	if m.mode == "chat" && m.state.SelectedSwarm != nil {
		content = m.renderChat(width, height)
	} else {
		content = m.renderHome(width, height)
	}
	if m.modal.Kind != "" {
		content = m.renderModal(content, width, height)
	}
	v := tea.NewView(base.Render(content))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// ---------------------------------------------------------------------------
// Key handlers
// ---------------------------------------------------------------------------

func (m *modelUI) handleHome(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "tab":
		if m.focus == "swarms" {
			m.focus = "nearby"
		} else {
			m.focus = "swarms"
		}
	case "up":
		if m.focus == "swarms" && m.swarmIdx > 0 {
			m.swarmIdx--
		}
		if m.focus == "nearby" && m.nearbyIdx > 0 {
			m.nearbyIdx--
		}
	case "down":
		if m.focus == "swarms" && m.swarmIdx < len(m.state.Swarms)-1 {
			m.swarmIdx++
		}
		if m.focus == "nearby" && m.nearbyIdx < len(m.state.Nearby)-1 {
			m.nearbyIdx++
		}
	case "enter":
		if m.focus != "swarms" {
			m.status = "focus swarms to open a room"
			return m, nil
		}
		if swarm := m.selectedSwarm(); swarm != nil {
			if err := m.service.OpenSwarm(swarm.Swarm.ID); err != nil {
				m.status = err.Error()
			}
		}
	case "n":
		m.prompt.SetValue("")
		m.prompt.Placeholder = "Swarm name"
		m.prompt.Focus()
		m.modal = modalState{Kind: "new", Title: "Create Swarm", Message: "Name your new swarm."}
	case "r":
		m.prompt.SetValue(m.state.Identity.Name)
		m.prompt.Placeholder = "Display name"
		m.prompt.Focus()
		m.modal = modalState{Kind: "rename", Title: "Rename Yourself", Message: "Update the name nearby peers and swarms see for this device."}
	case "i":
		if m.focus != "swarms" {
			m.status = "focus swarms to invite a room"
			return m, nil
		}
		swarm := m.selectedSwarm()
		if swarm == nil {
			m.status = "select a swarm first"
			return m, nil
		}
		if _, err := m.service.GenerateInvite(swarm.Swarm.ID); err != nil {
			m.status = err.Error()
		}
	case "d":
		if m.focus != "swarms" {
			m.status = "focus swarms to remove a room"
			return m, nil
		}
		swarm := m.selectedSwarm()
		if swarm == nil {
			m.status = "select a swarm first"
			return m, nil
		}
		m.modal = modalState{
			Kind:      "remove",
			Title:     "Remove Swarm",
			Message:   fmt.Sprintf("Forget %s on this device and delete its local transcript?", swarm.Swarm.Name),
			SwarmID:   swarm.Swarm.ID,
			SwarmName: swarm.Swarm.Name,
		}
	case "j":
		if m.focus != "nearby" {
			m.status = "focus nearby to join with a code"
			return m, nil
		}
		if m.selectedNearby() == nil {
			m.status = "select a nearby peer first"
			return m, nil
		}
		m.prompt.SetValue("")
		m.prompt.Placeholder = "Invite code"
		m.prompt.Focus()
		m.modal = modalState{Kind: "join", Title: "Join Swarm", Message: "Enter the invite code from a nearby peer."}
	case "u":
		if m.checkingUpdate {
			return m, nil
		}
		m.status = "checking latest release"
		m.checkingUpdate = true
		return m, checkForUpdateCmd()
	}
	return m, nil
}

func (m *modelUI) handleChat(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.emojiActive() {
			// Clear the /e command from the composer
			value := m.composer.Value()
			idx := strings.LastIndex(value, "/e")
			if idx >= 0 {
				m.composer.SetValue(strings.TrimRight(value[:idx], " "))
			}
			m.emojiResults = nil
			m.emojiIdx = 0
			return m, nil
		}
		m.notifyTyping(false)
		if err := m.service.LeaveSwarm(); err != nil {
			m.status = err.Error()
		}
		m.mode = "home"
		m.focus = "swarms"
		m.composer.Blur()
		m.transitionFrame = 3
		return m, transitionTickCmd()
	case "pgup", "pageup":
		m.messages.PageUp()
		return m, nil
	case "pgdown", "pagedown":
		m.messages.PageDown()
		if m.messages.AtBottom() {
			m.hasNewBelow = false
		}
		return m, nil
	case "home":
		m.messages.GotoTop()
		return m, nil
	case "end":
		m.messages.GotoBottom()
		m.hasNewBelow = false
		return m, nil
	case "ctrl+u":
		m.messages.HalfPageUp()
		return m, nil
	case "ctrl+d":
		m.messages.HalfPageDown()
		if m.messages.AtBottom() {
			m.hasNewBelow = false
		}
		return m, nil
	case "up":
		if m.emojiActive() {
			if m.emojiIdx > 0 {
				m.emojiIdx--
			}
			return m, nil
		}
	case "down":
		if m.emojiActive() {
			if m.emojiIdx < len(m.emojiResults)-1 {
				m.emojiIdx++
			}
			return m, nil
		}
	case "tab":
		if m.emojiActive() {
			m.applyEmojiCompletion()
			return m, nil
		}
		if m.applyMentionCompletion() {
			m.notifyTyping(strings.TrimSpace(m.composer.Value()) != "")
		}
		return m, nil
	case "enter":
		if m.emojiActive() {
			m.applyEmojiCompletion()
			return m, nil
		}
		body := strings.TrimSpace(m.composer.Value())
		if body == "" {
			return m, nil
		}
		if err := m.service.SendChat(body); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.composer.SetValue("")
		m.updateComposerPlaceholder()
		m.notifyTyping(false)
		m.syncTranscript(true)
		return m, nil
	}
	// Everything else (including shift+enter for newline) goes to textarea.
	var cmd tea.Cmd
	before := m.composer.Value()
	m.composer, cmd = m.composer.Update(msg)
	if before != m.composer.Value() {
		m.updateComposerPlaceholder()
		m.notifyTyping(strings.TrimSpace(m.composer.Value()) != "")
		m.refreshEmojiResults()
	}
	return m, cmd
}

func (m *modelUI) handleModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.modal.Kind {
	case "new":
		return m.handleCreateSwarmModal(msg)
	case "join":
		return m.handleJoinModal(msg)
	case "rename":
		return m.handleRenameModal(msg)
	case "invite":
		switch msg.String() {
		case "y", "c":
			if m.modal.Invite != nil {
				return m, copyInviteCmd(*m.modal.Invite)
			}
		case "esc", "enter", " ":
			m.dismissModal()
		}
		return m, nil
	case "approval":
		switch msg.String() {
		case "y", "enter":
			if m.modal.Approval != nil && m.modal.Approval.Approval != nil {
				_ = m.service.ResolveApproval(m.modal.Approval.Approval.ID, true)
			}
			m.dismissModal()
		case "n", "esc":
			if m.modal.Approval != nil && m.modal.Approval.Approval != nil {
				_ = m.service.ResolveApproval(m.modal.Approval.Approval.ID, false)
			}
			m.dismissModal()
		}
		return m, nil
	case "remove":
		switch msg.String() {
		case "y", "enter":
			if err := m.service.RemoveSwarm(m.modal.SwarmID); err != nil {
				m.status = err.Error()
			} else {
				m.status = fmt.Sprintf("removed %s locally", m.modal.SwarmName)
			}
			m.dismissModal()
		case "n", "esc":
			m.dismissModal()
		}
		return m, nil
	case "update":
		switch msg.String() {
		case "y", "enter":
			m.updateRequested = true
			m.status = "closing to install the latest release"
			m.dismissModal()
			return m, tea.Quit
		case "n", "esc":
			m.dismissModal()
			m.status = "update canceled"
		}
		return m, nil
	}
	return m, nil
}

func (m *modelUI) handleCreateSwarmModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dismissModal()
		return m, nil
	case "enter":
		swarm, err := m.service.CreateSwarm(m.prompt.Value())
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.dismissModal()
		if err := m.service.OpenSwarm(swarm.ID); err != nil {
			m.status = err.Error()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

func (m *modelUI) handleRenameModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dismissModal()
		return m, nil
	case "enter":
		if err := m.service.RenameIdentity(m.prompt.Value()); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.dismissModal()
		m.status = "updated display name"
		return m, nil
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

func (m *modelUI) handleJoinModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dismissModal()
		return m, nil
	case "enter":
		peerInfo := m.selectedNearby()
		if peerInfo == nil {
			m.status = "select a nearby peer first"
			return m, nil
		}
		if err := m.service.StartPair(peerInfo.PeerID, m.prompt.Value(), false); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.dismissModal()
		m.status = fmt.Sprintf("pairing with %s", nearbyLabel(*peerInfo))
		return m, nil
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

// ---------------------------------------------------------------------------
// Event handling
// ---------------------------------------------------------------------------

func (m *modelUI) applyAppEvent(event app.Event) tea.Cmd {
	switch event.Type {
	case app.EventSync:
		if event.Snapshot.Version < m.state.Version {
			return nil
		}
		prevState := m.state
		prevMode := m.mode
		prevSwarmID := m.highlightedSwarmID()
		prevNearbyID := m.highlightedNearbyID()
		m.state = event.Snapshot
		if m.state.SelectedSwarm != nil {
			m.mode = "chat"
			m.focus = "composer"
			m.composer.Focus()
		} else {
			m.mode = "home"
			m.focus = "swarms"
			m.composer.Blur()
		}
		m.restoreHighlightedSwarm(prevSwarmID)
		m.restoreHighlightedNearby(prevNearbyID)
		m.syncLayout()
		if selectedSwarmID(prevState) != selectedSwarmID(m.state) || !sameTranscriptEntries(prevState.Transcript, m.state.Transcript) {
			m.syncTranscript(false)
		}
		var cmds []tea.Cmd
		if m.mode != prevMode {
			m.transitionFrame = 3
			cmds = append(cmds, transitionTickCmd())
		}
		if m.anyoneTyping() && m.mode == "chat" && !m.typingTicking {
			m.typingTicking = true
			cmds = append(cmds, typingTickCmd())
		}
		if len(cmds) > 0 {
			return tea.Batch(cmds...)
		}
		return nil
	case app.EventToast:
		prevState := m.state
		prevSwarmID := m.highlightedSwarmID()
		prevNearbyID := m.highlightedNearbyID()
		if event.Snapshot.Version >= m.state.Version {
			m.state = event.Snapshot
		}
		m.status = event.Message
		m.restoreHighlightedSwarm(prevSwarmID)
		m.restoreHighlightedNearby(prevNearbyID)
		m.syncLayout()
		if selectedSwarmID(prevState) != selectedSwarmID(m.state) || !sameTranscriptEntries(prevState.Transcript, m.state.Transcript) {
			m.syncTranscript(false)
		}
		if m.anyoneTyping() && m.mode == "chat" && !m.typingTicking {
			m.typingTicking = true
			return typingTickCmd()
		}
		return nil
	case app.EventInvite:
		if event.Snapshot.Version < m.state.Version {
			return nil
		}
		if event.Snapshot.Version >= m.state.Version {
			m.state = event.Snapshot
		}
		title := "Invite Ready"
		message := "Share this code with a nearby peer."
		if event.Invite != nil {
			title = "Invite " + event.Invite.SwarmName
			message = fmt.Sprintf("Share this code to invite someone into %s.", event.Invite.SwarmName)
		}
		m.pushModal(modalState{
			Kind:    "invite",
			Title:   title,
			Message: message,
			Invite:  event.Invite,
		})
		if event.Invite != nil {
			return copyInviteCmd(*event.Invite)
		}
		return nil
	case app.EventApproval:
		if event.Snapshot.Version < m.state.Version {
			return nil
		}
		if event.Snapshot.Version >= m.state.Version {
			m.state = event.Snapshot
		}
		m.pushModal(modalState{
			Kind:     "approval",
			Title:    "Approve Pairing",
			Message:  approvalMessage(event),
			Approval: &event,
		})
		return nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

func (m *modelUI) syncLayout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	if m.mode == "chat" {
		sidebarWidth := 26
		chatWidth := m.width - sidebarWidth
		if chatWidth < 30 {
			chatWidth = m.width
		}
		contentWidth := chatWidth - 6
		if contentWidth < 10 {
			contentWidth = 10
		}
		msgHeight := m.height - 12
		if msgHeight < 4 {
			msgHeight = 4
		}
		m.messages.SetWidth(contentWidth)
		m.messages.SetHeight(msgHeight)
		m.composer.SetWidth(contentWidth)
	}
}

func (m *modelUI) syncTranscript(forceBottom bool) {
	width := m.messages.Width()
	if width <= 0 {
		return
	}
	rendered := m.renderTranscriptView(m.state.Transcript, width)
	wasAtBottom := forceBottom || m.messages.AtBottom()
	offset := m.messages.YOffset()
	m.messages.SetContentLines(rendered.Lines)
	if wasAtBottom {
		m.messages.GotoBottom()
		m.hasNewBelow = false
		return
	}
	// New content arrived while scrolled up.
	m.hasNewBelow = true
	maxOffset := len(rendered.Lines) - m.messages.Height()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	m.messages.SetYOffset(offset)
}

// ---------------------------------------------------------------------------
// Render: Home
// ---------------------------------------------------------------------------

func (m *modelUI) renderHome(width, height int) string {
	header := lipgloss.NewStyle().Padding(1, 2, 0, 2).Width(width).Render(
		titleStyle.Render("yap") +
			mutedStyle.Render(" · ") +
			lipgloss.NewStyle().Foreground(colorStrong).Bold(true).Render(m.state.Identity.Name) +
			mutedStyle.Render(" · "+shortID(m.state.Identity.PeerID)),
	)

	leftWidth := width / 2
	if leftWidth < 40 {
		leftWidth = width
	}
	rightWidth := width - leftWidth
	if rightWidth < 32 {
		rightWidth = width
	}

	panelHeight := height - 6
	if panelHeight < 4 {
		panelHeight = 4
	}
	leftStyle, rightStyle := panelStyle, panelStyle
	if m.transitionFrame > 0 {
		transStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.transitionBorderColor()).Padding(1, 2)
		leftStyle = transStyle
		rightStyle = transStyle
	} else if m.focus == "swarms" {
		leftStyle = focusedPanelStyle
	} else {
		rightStyle = focusedPanelStyle
	}
	swarms := leftStyle.Width(leftWidth).Height(panelHeight).Render(m.renderSwarms())
	nearby := rightStyle.Width(rightWidth).Height(panelHeight).Render(m.renderNearby())

	body := lipgloss.JoinHorizontal(lipgloss.Top, swarms, nearby)

	footer := lipgloss.NewStyle().Padding(0, 2).Width(width).Render(
		statusStyle.Render(m.status) + "\n" +
			mutedStyle.Render(m.homeFooterHelp()),
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *modelUI) renderSwarms() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Swarms"))
	if len(m.state.Swarms) == 0 {
		lines = append(lines, "", mutedStyle.Render("No swarms yet. Press n to create one or focus Nearby and press j to join with a code."))
		return strings.Join(lines, "\n")
	}
	for i, swarm := range m.state.Swarms {
		selected := i == m.swarmIdx && m.focus == "swarms"

		dot := "●"
		if !swarm.Connected {
			dot = "○"
		}
		name := swarm.Swarm.Name
		id := shortID(swarm.Swarm.ID)

		var label string
		if selected {
			label = selectedStyle.Render(dot + " " + name + "  " + id)
		} else {
			label = swarmDot(swarm) + name + "  " + mutedStyle.Render(id)
		}
		lines = append(lines, "", label)

		var metaParts []string
		metaParts = append(metaParts, fmt.Sprintf("%d peers", len(swarm.Swarm.TrustedPeers)))
		if swarm.Unread > 0 {
			metaParts = append(metaParts, accentStyle.Render(fmt.Sprintf("%d unread", swarm.Unread)))
		}
		if !swarm.LastActivity.IsZero() {
			metaParts = append(metaParts, swarm.LastActivity.Format("Jan 2 15:04"))
		}
		lines = append(lines, mutedStyle.Render("  ")+mutedStyle.Render(strings.Join(metaParts, mutedStyle.Render(" · "))))
	}
	return strings.Join(lines, "\n")
}

func (m *modelUI) renderNearby() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Nearby"))
	if len(m.state.Nearby) == 0 {
		lines = append(lines, "", mutedStyle.Render("Scanning for peers on your local network..."))
		return strings.Join(lines, "\n")
	}
	for i, peerInfo := range m.state.Nearby {
		selected := i == m.nearbyIdx && m.focus == "nearby"
		name := nearbyLabel(peerInfo)

		var label string
		if selected {
			label = selectedStyle.Render(name)
		} else {
			label = lipgloss.NewStyle().Foreground(colorStrong).Render(name)
		}
		lines = append(lines, "", label)
		lines = append(lines, mutedStyle.Render("  "+shortID(peerInfo.PeerID)+" · seen "+peerInfo.LastSeen.Format("15:04")))
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Render: Chat
// ---------------------------------------------------------------------------

func (m *modelUI) renderChat(width, height int) string {
	active := m.state.SelectedSwarm
	if active == nil {
		return ""
	}

	peerCount := len(m.state.Presence)
	peerWord := "peers"
	if peerCount == 1 {
		peerWord = "peer"
	}
	header := lipgloss.NewStyle().Padding(1, 2, 0, 2).Width(width).Render(
		titleStyle.Render(active.Name) +
			mutedStyle.Render(fmt.Sprintf(" · %d %s", peerCount, peerWord)) +
			"  " + statusStyle.Render(m.connectionSummary()) +
			"  " + mutedStyle.Render(m.status),
	)

	sidebarWidth := 26
	mainWidth := width - sidebarWidth
	if mainWidth < 30 {
		mainWidth = width
		sidebarWidth = 0
	}
	contentWidth := mainWidth - 6
	if contentWidth < 10 {
		contentWidth = 10
	}
	msgHeight := height - 12 // always reserve 1 line for typing indicator
	// Reserve additional space for emoji picker above composer
	emojiLines := m.emojiExtraLines()
	msgHeight -= emojiLines
	if msgHeight < 4 {
		msgHeight = 4
	}
	m.messages.SetWidth(contentWidth)
	m.messages.SetHeight(msgHeight)
	m.composer.SetWidth(contentWidth)

	newMsgLine := 0
	if m.hasNewBelow && !m.messages.AtBottom() {
		newMsgLine = 1
	}
	panelHeight := msgHeight + 5 + emojiLines + newMsgLine // +5 = composer(3) + gap(1) + typing(1)

	mainStyle := focusedPanelStyle
	if m.transitionFrame > 0 {
		mainStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.transitionBorderColor()).Padding(1, 2)
	}

	var chatContent string
	if m.hasNewBelow && !m.messages.AtBottom() {
		pill := accentStyle.Render("↓ new messages")
		chatContent = m.messages.View() + "\n" + pill + "\n" + m.renderComposerArea()
	} else {
		chatContent = m.messages.View() + "\n\n" + m.renderComposerArea()
	}
	main := mainStyle.Width(mainWidth).Height(panelHeight).Render(chatContent)

	body := main
	if sidebarWidth > 0 {
		sideStyle := panelStyle
		if m.transitionFrame > 0 {
			sideStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.transitionBorderColor()).Padding(1, 2)
		}
		peers := sideStyle.Width(sidebarWidth).Height(panelHeight).Render(m.renderPresence())
		body = lipgloss.JoinHorizontal(lipgloss.Top, main, peers)
	}

	footer := lipgloss.NewStyle().Padding(0, 2).Width(width).Render(
		mutedStyle.Render(m.chatFooterHelp()),
	)

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *modelUI) renderPresence() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Peers"))
	if len(m.state.Presence) == 0 {
		lines = append(lines, "", mutedStyle.Render("No peers yet."))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "")
	// Sidebar content width: 26 total - 2 border - 4 padding = 20 usable.
	const sidebarContent = 20
	for _, presence := range m.state.Presence {
		name := presence.Name
		if strings.TrimSpace(name) == "" {
			name = shortID(presence.PeerID)
		}
		name = truncate(name, sidebarContent-2) // 2 for dot+space
		dot := presenceDot(presence.State)
		nameColor := colorStrong
		if presence.State == "stale" {
			nameColor = colorStale
		} else if presence.State == "offline" {
			nameColor = colorMuted
		}
		label := dot + " " + lipgloss.NewStyle().Foreground(nameColor).Render(name)
		handle := mentionToken(presence.Name, presence.PeerID)
		if handle != "" {
			label += mutedStyle.Render("  @" + truncate(handle, sidebarContent-6))
		}
		if presence.Typing && presence.PeerID != m.state.Identity.PeerID {
			label += accentStyle.Render("  " + m.typingDots())
		}
		lines = append(lines, label)
	}
	return strings.Join(lines, "\n")
}

func (m *modelUI) renderTranscript(entries []model.TranscriptEntry, width int) string {
	rendered := m.renderTranscriptView(entries, width)
	return strings.Join(rendered.Lines, "\n")
}

func (m *modelUI) renderTranscriptView(entries []model.TranscriptEntry, width int) transcriptRender {
	if width <= 0 {
		width = 70
	}
	if len(entries) == 0 {
		return transcriptRender{Lines: []string{mutedStyle.Render("No messages yet.")}}
	}
	lines := make([]string, 0, len(entries)*4)
	prevSender := ""
	prevKind := ""
	first := true
	insertedUnread := false
	lastOpened := time.Time{}
	if m.state.SelectedSwarm != nil {
		lastOpened = m.state.SelectedSwarm.LastOpened
	}
	for _, entry := range entries {
		if entry.Kind != "chat" && entry.Kind != "join" && entry.Kind != "leave" {
			if strings.TrimSpace(entry.Body) == "" {
				continue
			}
		}

		// Consecutive chat messages from the same sender get collapsed.
		sameSender := entry.Kind == "chat" && prevKind == "chat" && entry.SenderPeerID == prevSender

		if !first && !sameSender {
			lines = append(lines, "")
		}
		if !insertedUnread && !lastOpened.IsZero() && entry.SentAt.After(lastOpened) && !entry.Local {
			if !first {
				lines = append(lines, "")
			}
			lines = append(lines, accentStyle.Render("── new messages ──"), "")
			insertedUnread = true
		}

		lines = append(lines, strings.Split(renderTranscriptEntry(entry, sameSender, m.selfMentionHandle()), "\n")...)

		if entry.Kind == "chat" {
			prevSender = entry.SenderPeerID
		} else {
			prevSender = ""
		}
		prevKind = entry.Kind
		first = false
	}
	return transcriptRender{Lines: lines}
}

// ---------------------------------------------------------------------------
// Render: Modal
// ---------------------------------------------------------------------------

func (m *modelUI) renderModal(content string, width, height int) string {
	boxWidth := width / 2
	if boxWidth < 42 {
		boxWidth = width - 8
	}
	box := modalStyle.Width(boxWidth).Render(m.modalBody())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box, lipgloss.WithWhitespaceChars(" "))
}

func (m *modelUI) modalBody() string {
	var lines []string
	lines = append(lines, titleStyle.Render(m.modal.Title))
	lines = append(lines, "")
	lines = append(lines, m.modal.Message)
	switch m.modal.Kind {
	case "new", "join", "rename":
		lines = append(lines, "")
		lines = append(lines, m.prompt.View())
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("enter confirm  ·  esc cancel"))
	case "invite":
		if m.modal.Invite != nil {
			lines = append(lines, "")
			lines = append(lines, accentStyle.Render(m.modal.Invite.Code))
			lines = append(lines, mutedStyle.Render("expires "+m.modal.Invite.ExpiresAt.Format(time.Kitchen)))
		}
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("y/c copy  ·  enter/esc close"))
	case "approval":
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("y accept  ·  n reject"))
	case "update":
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("enter/y confirm  ·  esc/n cancel"))
	case "remove":
		lines = append(lines, "")
		lines = append(lines, dangerStyle.Render("local only"))
		lines = append(lines, mutedStyle.Render("enter/y confirm  ·  esc/n cancel"))
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (m *modelUI) selectedSwarm() *app.SwarmSummary {
	if len(m.state.Swarms) == 0 || m.swarmIdx < 0 || m.swarmIdx >= len(m.state.Swarms) {
		return nil
	}
	swarm := m.state.Swarms[m.swarmIdx]
	return &swarm
}

func (m *modelUI) selectedNearby() *model.NearbyPeer {
	if len(m.state.Nearby) == 0 || m.nearbyIdx < 0 || m.nearbyIdx >= len(m.state.Nearby) {
		return nil
	}
	peerInfo := m.state.Nearby[m.nearbyIdx]
	return &peerInfo
}

func renderChatEntry(entry model.TranscriptEntry, continuation bool, selfHandle string) string {
	nameStyle := lipgloss.NewStyle().Foreground(colorPeer).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorPeer)
	if entry.Local {
		nameStyle = lipgloss.NewStyle().Foreground(colorSelf).Bold(true)
		bodyStyle = lipgloss.NewStyle().Foreground(colorSelf)
	}
	mentionBadge := ""
	if selfHandle != "" && !entry.Local && mentionsHandle(entry.Body, selfHandle) {
		mentionBadge = accentStyle.Render(" @you")
	}
	var raw string
	if continuation {
		raw = renderChatBody(entry.Body, bodyStyle, selfHandle)
	} else {
		ts := mutedStyle.Render(entry.SentAt.Format("15:04"))
		sep := mutedStyle.Render(" · ")
		header := ts + sep + nameStyle.Render(entry.SenderName) + mentionBadge
		raw = header + "\n" + renderChatBody(entry.Body, bodyStyle, selfHandle)
	}
	// Apply gradient left border
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		barColor := messageGradient(entry.Local, i)
		bar := lipgloss.NewStyle().Foreground(barColor).Render("▎")
		lines[i] = bar + " " + line
	}
	return strings.Join(lines, "\n")
}

func renderTranscriptEntry(entry model.TranscriptEntry, continuation bool, selfHandle string) string {
	switch entry.Kind {
	case "chat":
		return renderChatEntry(entry, continuation, selfHandle)
	case "join":
		return lipgloss.NewStyle().Foreground(colorJoin).Render("→ " + entry.SenderName + " joined")
	case "leave":
		return lipgloss.NewStyle().Foreground(colorLeave).Render("← " + entry.SenderName + " left")
	default:
		if strings.TrimSpace(entry.Body) != "" {
			return mutedStyle.Render(entry.Body)
		}
		return ""
	}
}

func copyInviteCmd(invite model.Invite) tea.Cmd {
	status := "copied invite code"
	if strings.TrimSpace(invite.SwarmName) != "" {
		status = "copied invite code for " + invite.SwarmName
	}
	return copyTextCmd(invite.Code, status)
}

func copyTextCmd(text, status string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return tea.Batch(
		func() tea.Msg {
			_ = clipboard.WriteAll(text)
			return copyResultMsg{Message: status}
		},
		tea.SetClipboard(text),
	)
}

func (m *modelUI) homeFooterHelp() string {
	w := m.width
	if m.focus == "nearby" {
		if w > 0 && w < 80 {
			return "tab swarms · ↑↓ select · n new · j join · r rename\nu update · q quit"
		}
		return "tab swarms · ↑↓ select · n new swarm · j join selected nearby · r rename self · u update · q quit"
	}
	if w > 0 && w < 100 {
		return "tab nearby · ↑↓ select · enter open · n new · r rename\ni invite · d remove · u update · q quit"
	}
	return "tab nearby · ↑↓ select · enter open swarm · n new swarm · r rename self · i invite · d remove · u update · q quit"
}

func (m *modelUI) chatFooterHelp() string {
	parts := []string{"enter send", "tab @mention", "/e emoji"}
	if m.keyDisambiguation {
		parts = append(parts, "shift+enter newline")
	}
	parts = append(parts, "pgup/pgdn scroll", "esc back")
	return strings.Join(parts, " · ")
}

func swarmDot(swarm app.SwarmSummary) string {
	if swarm.Connected {
		return lipgloss.NewStyle().Foreground(colorJoin).Render("● ")
	}
	return lipgloss.NewStyle().Foreground(colorMuted).Render("○ ")
}

func approvalMessage(event app.Event) string {
	if event.Approval == nil {
		return ""
	}
	peerInfo := event.Approval.Peer
	direction := "Approve this pairing request?"
	if event.Approval.Direction == "outgoing" {
		direction = "Confirm the inviter before joining?"
	}
	return direction + "\n\n" +
		accentStyle.Render(displayTrustedName(peerInfo)) + "\n" +
		mutedStyle.Render("fingerprint "+peerInfo.Fingerprint) + "\n" +
		mutedStyle.Render("swarm "+event.Approval.SwarmName)
}

func presenceDot(state string) string {
	switch state {
	case "online":
		return lipgloss.NewStyle().Foreground(colorJoin).Render("●")
	case "stale":
		return lipgloss.NewStyle().Foreground(colorStale).Render("●")
	default:
		return lipgloss.NewStyle().Foreground(colorMuted).Render("○")
	}
}

func nearbyLabel(peerInfo model.NearbyPeer) string {
	if strings.TrimSpace(peerInfo.Name) != "" {
		return peerInfo.Name
	}
	return shortID(peerInfo.PeerID)
}

func displayTrustedName(peerInfo model.TrustedPeer) string {
	if strings.TrimSpace(peerInfo.Name) != "" {
		return peerInfo.Name
	}
	return shortID(peerInfo.PeerID)
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}

func truncate(s string, max int) string {
	if max < 1 {
		max = 1
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

func selectedSwarmID(state app.State) string {
	if state.SelectedSwarm == nil {
		return ""
	}
	return state.SelectedSwarm.ID
}

func sameTranscriptEntries(left, right []model.TranscriptEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ID != right[i].ID ||
			left[i].SwarmID != right[i].SwarmID ||
			left[i].Kind != right[i].Kind ||
			left[i].SenderPeerID != right[i].SenderPeerID ||
			left[i].SenderName != right[i].SenderName ||
			left[i].Body != right[i].Body ||
			!left[i].SentAt.Equal(right[i].SentAt) ||
			left[i].Local != right[i].Local {
			return false
		}
	}
	return true
}

func (m *modelUI) renderComposerArea() string {
	var parts []string
	if m.emojiActive() {
		parts = append(parts, m.renderEmojiHint())
	} else if hint := m.mentionCompletionHint(); hint != "" {
		parts = append(parts, accentStyle.Render(hint))
	}
	// Always render a typing line to keep layout stable
	if typing := m.typingSummary(); typing != "" {
		parts = append(parts, accentStyle.Render(typing))
	} else {
		parts = append(parts, "")
	}
	parts = append(parts, m.composer.View())
	return strings.Join(parts, "\n")
}

func (m *modelUI) typingSummary() string {
	names := make([]string, 0, len(m.state.Presence))
	for _, presence := range m.state.Presence {
		if presence.PeerID == m.state.Identity.PeerID || !presence.Typing || presence.State == "offline" {
			continue
		}
		name := strings.TrimSpace(presence.Name)
		if name == "" {
			name = shortID(presence.PeerID)
		}
		names = append(names, name)
	}
	dots := m.typingDots()
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0] + " is typing " + dots
	case 2:
		return names[0] + " and " + names[1] + " are typing " + dots
	default:
		return fmt.Sprintf("%s and %d others are typing %s", names[0], len(names)-1, dots)
	}
}

func (m *modelUI) connectionSummary() string {
	selected := m.selectedSwarmSummary()
	if selected == nil || !selected.Connected {
		return "✕ offline"
	}
	online := 0
	stale := 0
	for _, presence := range m.state.Presence {
		switch presence.State {
		case "online":
			online++
		case "stale":
			stale++
		}
	}
	if online <= 1 {
		return "◎ connecting..."
	}
	summary := fmt.Sprintf("◉ %d online", online)
	if stale > 0 {
		summary += fmt.Sprintf(" · %d stale", stale)
	}
	return summary
}

func (m *modelUI) selectedSwarmSummary() *app.SwarmSummary {
	if m.state.SelectedSwarm == nil {
		return nil
	}
	for i := range m.state.Swarms {
		if m.state.Swarms[i].Swarm.ID == m.state.SelectedSwarm.ID {
			return &m.state.Swarms[i]
		}
	}
	return nil
}

func (m *modelUI) updateComposerPlaceholder() {
	m.composer.Placeholder = "Type a message..."
}

func (m *modelUI) applyMentionCompletion() bool {
	if m.focus != "composer" {
		return false
	}
	value := m.composer.Value()
	if value == "" {
		return false
	}
	start := strings.LastIndexAny(value, " \n\t")
	token := value[start+1:]
	if !strings.HasPrefix(token, "@") {
		return false
	}
	prefix := strings.TrimPrefix(token, "@")
	for _, candidate := range m.mentionCandidates() {
		if prefix == "" || strings.HasPrefix(candidate, strings.ToLower(prefix)) {
			m.composer.SetValue(value[:start+1] + "@" + candidate + " ")
			return true
		}
	}
	return false
}

func (m *modelUI) mentionCompletionHint() string {
	if m.focus != "composer" {
		return ""
	}
	value := m.composer.Value()
	if value == "" {
		return ""
	}
	start := strings.LastIndexAny(value, " \n\t")
	token := value[start+1:]
	if !strings.HasPrefix(token, "@") {
		return ""
	}
	prefix := strings.TrimPrefix(token, "@")
	for _, candidate := range m.mentionCandidates() {
		if prefix == "" || strings.HasPrefix(candidate, strings.ToLower(prefix)) {
			return "tab complete @" + candidate
		}
	}
	return ""
}

func (m *modelUI) mentionCandidates() []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	appendCandidate := func(name, peerID string) {
		handle := mentionToken(name, peerID)
		if handle == "" {
			return
		}
		if _, ok := seen[handle]; ok {
			return
		}
		seen[handle] = struct{}{}
		out = append(out, handle)
	}
	if m.state.SelectedSwarm != nil {
		for _, trusted := range m.state.SelectedSwarm.TrustedPeers {
			if trusted.PeerID == m.state.Identity.PeerID {
				continue
			}
			appendCandidate(trusted.Name, trusted.PeerID)
		}
	}
	for _, presence := range m.state.Presence {
		if presence.PeerID == m.state.Identity.PeerID {
			continue
		}
		appendCandidate(presence.Name, presence.PeerID)
	}
	return out
}

func (m *modelUI) selfMentionHandle() string {
	return mentionToken(m.state.Identity.Name, m.state.Identity.PeerID)
}

func mentionToken(name, peerID string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return strings.ToLower(shortID(peerID))
	}
	var b strings.Builder
	dashed := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dashed = false
		case r == '-' || r == '_':
			if !dashed && b.Len() > 0 {
				b.WriteRune(r)
				dashed = true
			}
		default:
			if !dashed && b.Len() > 0 {
				b.WriteByte('-')
				dashed = true
			}
		}
	}
	handle := strings.Trim(b.String(), "-_")
	if handle == "" {
		return strings.ToLower(shortID(peerID))
	}
	return handle
}

func mentionsHandle(body, handle string) bool {
	if handle == "" {
		return false
	}
	for _, token := range strings.Fields(body) {
		if !strings.HasPrefix(token, "@") {
			continue
		}
		candidate := cleanMentionToken(token)
		if strings.EqualFold(candidate, handle) {
			return true
		}
	}
	return false
}

func renderChatBody(body string, bodyStyle lipgloss.Style, selfHandle string) string {
	// Split on code blocks (```) first
	parts := strings.Split(body, "```")
	var out strings.Builder
	codeBlockStyle := lipgloss.NewStyle().Foreground(colorCodeFg).Background(colorCodeBg)
	for i, part := range parts {
		if i%2 == 1 {
			// Code block: strip optional language identifier
			code := part
			if nl := strings.Index(code, "\n"); nl > 0 {
				lang := code[:nl]
				if !strings.Contains(lang, " ") && len(lang) < 20 {
					code = code[nl+1:]
				}
			}
			code = strings.TrimRight(code, "\n")
			out.WriteString(codeBlockStyle.Render(code))
			continue
		}
		out.WriteString(renderInlineMarkdown(part, bodyStyle, selfHandle))
	}
	return out.String()
}

// inlineSegment is a parsed piece of inline text with formatting.
type inlineSegment struct {
	text string
	kind string // plain, bold, italic, code, strike, mention, selfmention
}

func renderInlineMarkdown(text string, baseStyle lipgloss.Style, selfHandle string) string {
	segments := parseInlineSegments(text, selfHandle)
	codeStyle := lipgloss.NewStyle().Foreground(colorCodeFg).Background(colorCodeBg)
	var out strings.Builder
	for _, seg := range segments {
		switch seg.kind {
		case "bold":
			out.WriteString(baseStyle.Bold(true).Render(seg.text))
		case "italic":
			out.WriteString(baseStyle.Italic(true).Render(seg.text))
		case "code":
			out.WriteString(codeStyle.Render(seg.text))
		case "strike":
			out.WriteString(baseStyle.Strikethrough(true).Render(seg.text))
		case "mention":
			out.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(seg.text))
		case "selfmention":
			out.WriteString(lipgloss.NewStyle().Foreground(colorAccent2).Bold(true).Render(seg.text))
		default:
			out.WriteString(baseStyle.Render(seg.text))
		}
	}
	return out.String()
}

func parseInlineSegments(text string, selfHandle string) []inlineSegment {
	var result []inlineSegment
	i := 0
	n := len(text)
	var buf strings.Builder

	flushBuf := func() {
		if buf.Len() > 0 {
			result = append(result, tokenizeMentions(buf.String(), selfHandle)...)
			buf.Reset()
		}
	}

	for i < n {
		// Inline code: `...`
		if text[i] == '`' {
			flushBuf()
			end := strings.Index(text[i+1:], "`")
			if end >= 0 {
				result = append(result, inlineSegment{text[i+1 : i+1+end], "code"})
				i += end + 2
				continue
			}
		}
		// Bold: **...**
		if i+1 < n && text[i] == '*' && text[i+1] == '*' {
			flushBuf()
			end := strings.Index(text[i+2:], "**")
			if end >= 0 {
				result = append(result, inlineSegment{text[i+2 : i+2+end], "bold"})
				i += end + 4
				continue
			}
		}
		// Strikethrough: ~~...~~
		if i+1 < n && text[i] == '~' && text[i+1] == '~' {
			flushBuf()
			end := strings.Index(text[i+2:], "~~")
			if end >= 0 {
				result = append(result, inlineSegment{text[i+2 : i+2+end], "strike"})
				i += end + 4
				continue
			}
		}
		// Italic: *...* (but not **)
		if text[i] == '*' && (i+1 >= n || text[i+1] != '*') {
			flushBuf()
			rest := text[i+1:]
			end := -1
			for j := 0; j < len(rest); j++ {
				if rest[j] == '*' && (j+1 >= len(rest) || rest[j+1] != '*') {
					end = j
					break
				}
			}
			if end >= 0 {
				result = append(result, inlineSegment{text[i+1 : i+1+end], "italic"})
				i += end + 2
				continue
			}
		}
		buf.WriteByte(text[i])
		i++
	}
	flushBuf()
	return result
}

func tokenizeMentions(text string, selfHandle string) []inlineSegment {
	var result []inlineSegment
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		value := token.String()
		if strings.HasPrefix(value, "@") {
			kind := "mention"
			if selfHandle != "" && strings.EqualFold(cleanMentionToken(value), selfHandle) {
				kind = "selfmention"
			}
			result = append(result, inlineSegment{value, kind})
		} else {
			result = append(result, inlineSegment{value, "plain"})
		}
		token.Reset()
	}
	for _, r := range text {
		if r == ' ' || r == '\n' || r == '\t' {
			flush()
			result = append(result, inlineSegment{string(r), "plain"})
			continue
		}
		token.WriteRune(r)
	}
	flush()
	return result
}

func cleanMentionToken(value string) string {
	return strings.Trim(strings.TrimPrefix(value, "@"), ".,:;!?()[]{}<>\"'")
}

func (m *modelUI) highlightedSwarmID() string {
	if swarm := m.selectedSwarm(); swarm != nil {
		return swarm.Swarm.ID
	}
	return ""
}

func (m *modelUI) highlightedNearbyID() string {
	if peerInfo := m.selectedNearby(); peerInfo != nil {
		return peerInfo.PeerID
	}
	return ""
}

func (m *modelUI) restoreHighlightedSwarm(previous string) {
	if previous == "" {
		if m.swarmIdx >= len(m.state.Swarms) && len(m.state.Swarms) > 0 {
			m.swarmIdx = len(m.state.Swarms) - 1
		}
		return
	}
	for i := range m.state.Swarms {
		if m.state.Swarms[i].Swarm.ID == previous {
			m.swarmIdx = i
			return
		}
	}
	if m.swarmIdx >= len(m.state.Swarms) && len(m.state.Swarms) > 0 {
		m.swarmIdx = len(m.state.Swarms) - 1
	}
}

func (m *modelUI) restoreHighlightedNearby(previous string) {
	if previous == "" {
		if m.nearbyIdx >= len(m.state.Nearby) && len(m.state.Nearby) > 0 {
			m.nearbyIdx = len(m.state.Nearby) - 1
		}
		return
	}
	for i := range m.state.Nearby {
		if m.state.Nearby[i].PeerID == previous {
			m.nearbyIdx = i
			return
		}
	}
	if m.nearbyIdx >= len(m.state.Nearby) && len(m.state.Nearby) > 0 {
		m.nearbyIdx = len(m.state.Nearby) - 1
	}
}

func (m *modelUI) notifyTyping(active bool) {
	if m.service == nil {
		return
	}
	m.service.NotifyTyping(active)
}

// pushModal shows a modal immediately if none is active, otherwise queues it.
func (m *modelUI) pushModal(ms modalState) {
	if m.modal.Kind == "" {
		m.modal = ms
		return
	}
	m.modalQueue = append(m.modalQueue, ms)
}

// dismissModal closes the current modal and shows the next queued one if any.
func (m *modelUI) dismissModal() {
	if len(m.modalQueue) > 0 {
		m.modal = m.modalQueue[0]
		m.modalQueue = m.modalQueue[1:]
		return
	}
	m.dismissModal()
}

func displayVersion(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

// ---------------------------------------------------------------------------
// Animated typing indicator
// ---------------------------------------------------------------------------

func (m *modelUI) typingDots() string {
	frames := []string{"·  ", "·· ", "···"}
	return frames[m.typingFrame%3]
}

func (m *modelUI) anyoneTyping() bool {
	for _, p := range m.state.Presence {
		if p.Typing && p.PeerID != m.state.Identity.PeerID {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// View transitions
// ---------------------------------------------------------------------------

func (m *modelUI) transitionBorderColor() color.Color {
	switch m.transitionFrame {
	case 3:
		return lipgloss.Color("#A0FBDA")
	case 2:
		return colorAccent
	default:
		return colorFocus
	}
}

// ---------------------------------------------------------------------------
// Gradient message bubbles
// ---------------------------------------------------------------------------

func messageGradient(local bool, lineIdx int) color.Color {
	var colors []color.Color
	if local {
		colors = []color.Color{
			lipgloss.Color("#9AD1FF"),
			lipgloss.Color("#7AB8E6"),
			lipgloss.Color("#5A9FCC"),
			lipgloss.Color("#4A8DBF"),
		}
	} else {
		colors = []color.Color{
			lipgloss.Color("#7FDBB6"),
			lipgloss.Color("#5FBB96"),
			lipgloss.Color("#3F9B76"),
			lipgloss.Color("#2F8B66"),
		}
	}
	if lineIdx >= len(colors) {
		lineIdx = len(colors) - 1
	}
	return colors[lineIdx]
}

// ---------------------------------------------------------------------------
// Inline emoji completion
// ---------------------------------------------------------------------------

// emojiActive returns true when the composer contains a /e query.
func (m *modelUI) emojiActive() bool {
	if m.focus != "composer" {
		return false
	}
	_, ok := m.emojiQuery()
	return ok
}

// emojiQuery extracts the /e search query from the composer text.
// It looks for "/e" or "/e <query>" anywhere as the last token sequence.
func (m *modelUI) emojiQuery() (string, bool) {
	value := m.composer.Value()
	// Find the last occurrence of "/e" preceded by start-of-string or whitespace
	idx := strings.LastIndex(value, "/e")
	if idx < 0 {
		return "", false
	}
	if idx > 0 {
		prev := value[idx-1]
		if prev != ' ' && prev != '\n' && prev != '\t' {
			return "", false
		}
	}
	rest := value[idx+2:]
	// "/e" must be followed by a space (require "/e " to activate)
	if len(rest) == 0 || rest[0] != ' ' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// refreshEmojiResults updates the emoji result list based on composer content.
func (m *modelUI) refreshEmojiResults() {
	query, ok := m.emojiQuery()
	if !ok {
		m.emojiResults = nil
		m.emojiIdx = 0
		return
	}
	m.emojiResults = emoji.Search(query, 8)
	if m.emojiIdx >= len(m.emojiResults) {
		m.emojiIdx = 0
	}
}

// applyEmojiCompletion inserts the selected emoji, replacing the /e command.
func (m *modelUI) applyEmojiCompletion() {
	if len(m.emojiResults) == 0 || m.emojiIdx >= len(m.emojiResults) {
		return
	}
	selected := m.emojiResults[m.emojiIdx]
	value := m.composer.Value()
	idx := strings.LastIndex(value, "/e")
	if idx < 0 {
		return
	}
	prefix := value[:idx]
	m.composer.SetValue(prefix + selected.Char + " ")
	m.emojiResults = nil
	m.emojiIdx = 0
}

// emojiExtraLines returns the number of extra lines the emoji picker adds
// above the composer (beyond the always-reserved typing indicator line).
func (m *modelUI) emojiExtraLines() int {
	if !m.emojiActive() {
		return 0
	}
	n := 0
	if len(m.emojiResults) > 0 {
		n += len(m.emojiResults)
	} else {
		n++ // "no matches" or "type to search"
	}
	n++ // help line
	return n
}

// renderEmojiHint renders the inline emoji picker above the composer.
func (m *modelUI) renderEmojiHint() string {
	var lines []string
	for i, e := range m.emojiResults {
		label := e.Char + "  " + e.Name
		if i == m.emojiIdx {
			lines = append(lines, selectedStyle.Render(label))
		} else {
			lines = append(lines, mutedStyle.Render(label))
		}
	}
	if len(m.emojiResults) == 0 {
		q, _ := m.emojiQuery()
		if q != "" {
			lines = append(lines, mutedStyle.Render("no matches"))
		} else {
			lines = append(lines, mutedStyle.Render("type to search emojis"))
		}
	}
	lines = append(lines, mutedStyle.Render("↑↓ select · enter/tab insert · esc clear"))
	return strings.Join(lines, "\n")
}
