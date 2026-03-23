package ui

import (
	"context"
	"fmt"
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

	messages viewport.Model
	composer textarea.Model
	prompt   textinput.Model
	modal    modalState
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
		m.modal = modalState{
			Kind:  "update",
			Title: "Install Update",
			Message: fmt.Sprintf(
				"Update %s -> %s is available.\n\nQuit yap and install the latest GitHub release for this OS/arch?",
				displayVersion(msg.Result.PreviousVersion),
				displayVersion(msg.Result.LatestVersion),
			),
		}
		return m, nil
	case copyResultMsg:
		m.status = msg.Message
		return m, nil
	case tea.MouseWheelMsg:
		if m.mode == "chat" && m.modal.Kind == "" {
			var cmd tea.Cmd
			m.messages, cmd = m.messages.Update(msg)
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
		m.notifyTyping(false)
		if err := m.service.LeaveSwarm(); err != nil {
			m.status = err.Error()
		}
		m.mode = "home"
		m.focus = "swarms"
		m.composer.Blur()
		return m, nil
	case "tab":
		if m.applyMentionCompletion() {
			m.notifyTyping(strings.TrimSpace(m.composer.Value()) != "")
		}
		return m, nil
	case "pgup", "pageup":
		m.messages.PageUp()
		return m, nil
	case "pgdown", "pagedown":
		m.messages.PageDown()
		return m, nil
	case "home":
		m.messages.GotoTop()
		return m, nil
	case "end":
		m.messages.GotoBottom()
		return m, nil
	case "ctrl+u":
		m.messages.HalfPageUp()
		return m, nil
	case "ctrl+d":
		m.messages.HalfPageDown()
		return m, nil
	case "enter":
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
			m.modal = modalState{}
		}
		return m, nil
	case "approval":
		switch msg.String() {
		case "y", "enter":
			if m.modal.Approval != nil && m.modal.Approval.Approval != nil {
				_ = m.service.ResolveApproval(m.modal.Approval.Approval.ID, true)
			}
			m.modal = modalState{}
		case "n", "esc":
			if m.modal.Approval != nil && m.modal.Approval.Approval != nil {
				_ = m.service.ResolveApproval(m.modal.Approval.Approval.ID, false)
			}
			m.modal = modalState{}
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
			m.modal = modalState{}
		case "n", "esc":
			m.modal = modalState{}
		}
		return m, nil
	case "update":
		switch msg.String() {
		case "y", "enter":
			m.updateRequested = true
			m.status = "closing to install the latest release"
			m.modal = modalState{}
			return m, tea.Quit
		case "n", "esc":
			m.modal = modalState{}
			m.status = "update canceled"
		}
		return m, nil
	}
	return m, nil
}

func (m *modelUI) handleCreateSwarmModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.modal = modalState{}
		return m, nil
	case "enter":
		swarm, err := m.service.CreateSwarm(m.prompt.Value())
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.modal = modalState{}
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
		m.modal = modalState{}
		return m, nil
	case "enter":
		if err := m.service.RenameIdentity(m.prompt.Value()); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.modal = modalState{}
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
		m.modal = modalState{}
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
		m.modal = modalState{}
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
		m.modal = modalState{
			Kind:    "invite",
			Title:   title,
			Message: message,
			Invite:  event.Invite,
		}
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
		m.modal = modalState{
			Kind:     "approval",
			Title:    "Approve Pairing",
			Message:  approvalMessage(event),
			Approval: &event,
		}
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
		msgHeight := m.height - 11
		if msgHeight < 6 {
			msgHeight = 6
		}
		m.messages.SetWidth(chatWidth - 6)
		m.messages.SetHeight(msgHeight)
		m.composer.SetWidth(chatWidth - 6)
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
		return
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
	leftStyle, rightStyle := panelStyle, panelStyle
	if m.focus == "swarms" {
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
	msgHeight := height - 11
	if msgHeight < 8 {
		msgHeight = 8
	}
	m.messages.SetWidth(mainWidth - 6)
	m.messages.SetHeight(msgHeight)
	m.composer.SetWidth(mainWidth - 6)

	panelHeight := msgHeight + 4

	main := focusedPanelStyle.Width(mainWidth).Height(panelHeight).Render(
		m.messages.View() + "\n\n" + m.renderComposerArea(),
	)

	body := main
	if sidebarWidth > 0 {
		peers := panelStyle.Width(sidebarWidth).Height(panelHeight).Render(m.renderPresence())
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
	for _, presence := range m.state.Presence {
		name := presence.Name
		if strings.TrimSpace(name) == "" {
			name = shortID(presence.PeerID)
		}
		dot := presenceDot(presence.State)
		nameColor := colorStrong
		if presence.State == "stale" {
			nameColor = colorStale
		} else if presence.State == "offline" {
			nameColor = colorMuted
		}
		label := dot + " " + lipgloss.NewStyle().Foreground(nameColor).Render(name)
		handle := mentionToken(name, presence.PeerID)
		if handle != "" {
			label += mutedStyle.Render("  @" + handle)
		}
		if presence.Typing && presence.PeerID != m.state.Identity.PeerID {
			label += accentStyle.Render("  typing...")
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

		if !first {
			if sameSender {
				lines = append(lines, "")
			} else {
				lines = append(lines, "", "")
			}
		}
		if !insertedUnread && !lastOpened.IsZero() && entry.SentAt.After(lastOpened) && !entry.Local {
			if !first {
				lines = append(lines, "", "")
			}
			lines = append(lines, accentStyle.Render("── new messages ──"), "", "")
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
	if continuation {
		return renderChatBody(entry.Body, bodyStyle, selfHandle)
	}
	ts := mutedStyle.Render(entry.SentAt.Format("15:04"))
	sep := mutedStyle.Render(" · ")
	header := ts + sep + nameStyle.Render(entry.SenderName) + mentionBadge
	return header + "\n" + renderChatBody(entry.Body, bodyStyle, selfHandle)
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
	if m.focus == "nearby" {
		return "tab swarms · ↑↓ select · n new swarm · j join selected nearby · r rename self · u update · q quit"
	}
	return "tab nearby · ↑↓ select · enter open swarm · n new swarm · r rename self · i invite selected swarm · d remove selected swarm · u update · q quit"
}

func (m *modelUI) chatFooterHelp() string {
	parts := []string{"enter send", "tab complete @mention"}
	if m.keyDisambiguation {
		parts = append(parts, "shift+enter newline")
	} else {
		parts = append(parts, "multiline unavailable in this terminal")
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
	if hint := m.mentionCompletionHint(); hint != "" {
		parts = append(parts, accentStyle.Render(hint))
	}
	if typing := m.typingSummary(); typing != "" {
		parts = append(parts, accentStyle.Render(typing))
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
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0] + " is typing..."
	case 2:
		return names[0] + " and " + names[1] + " are typing..."
	default:
		return fmt.Sprintf("%s and %d others are typing...", names[0], len(names)-1)
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
	var out strings.Builder
	var token strings.Builder
	flushToken := func() {
		if token.Len() == 0 {
			return
		}
		value := token.String()
		if strings.HasPrefix(value, "@") {
			mentionStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
			if selfHandle != "" && strings.EqualFold(cleanMentionToken(value), selfHandle) {
				mentionStyle = lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
			}
			out.WriteString(mentionStyle.Render(value))
		} else {
			out.WriteString(bodyStyle.Render(value))
		}
		token.Reset()
	}
	for _, r := range body {
		if r == '\n' || r == '\t' || r == ' ' {
			flushToken()
			out.WriteString(bodyStyle.Render(string(r)))
			continue
		}
		token.WriteRune(r)
	}
	flushToken()
	return out.String()
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

func displayVersion(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
