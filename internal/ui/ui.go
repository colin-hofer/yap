package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"yap/internal/app"
	"yap/internal/model"
)

var (
	colorMuted   = lipgloss.Color("#6A7A87")
	colorAccent  = lipgloss.Color("#7FDBB6")
	colorAccent2 = lipgloss.Color("#F2C879")
	colorBorder  = lipgloss.Color("#31424D")
	colorStrong  = lipgloss.Color("#E8F0F2")
	colorDanger  = lipgloss.Color("#F16E5B")
	colorSelf    = lipgloss.Color("#9AD1FF")
	colorPeer    = lipgloss.Color("#F4F7F8")
	colorJoin    = lipgloss.Color("#9BE48D")
	colorLeave   = lipgloss.Color("#FFB199")
	colorStale   = lipgloss.Color("#E0BE75")

	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Padding(1, 2)
	modalStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(1, 2)
	titleStyle    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	statusStyle   = lipgloss.NewStyle().Foreground(colorAccent2)
	dangerStyle   = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
	selectedStyle = lipgloss.NewStyle().Foreground(colorStrong).Background(lipgloss.Color("#24343E")).Padding(0, 1)
)

// Run starts the Bubble Tea program.
func Run(service *app.Service) error {
	m := newModel(service)
	program := tea.NewProgram(m)
	_, err := program.Run()
	if err != nil {
		return err
	}
	return service.Shutdown()
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

	messages viewport.Model
	composer textarea.Model
	prompt   textinput.Model
	modal    modalState
}

type modalState struct {
	Kind     string
	Title    string
	Message  string
	Approval *app.Event
	Invite   *model.Invite
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

	prompt := textinput.New()
	prompt.Placeholder = "Type here"
	prompt.Focus()

	state := service.Snapshot()
	mode := "home"
	if state.ActiveSwarm != nil {
		mode = "chat"
		composer.Focus()
	}
	return &modelUI{
		service:  service,
		state:    state,
		events:   service.Events(),
		mode:     mode,
		focus:    "swarms",
		status:   "ready",
		messages: viewport.New(),
		composer: composer,
		prompt:   prompt,
	}
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

func (m *modelUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncLayout()
		return m, nil
	case app.Event:
		cmd := waitForAppEvent(m.events)
		m.applyAppEvent(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if m.modal.Kind != "" {
			return m.handleModal(msg)
		}
		if m.mode == "chat" {
			return m.handleChat(msg)
		}
		return m.handleHome(msg)
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
	if m.mode == "chat" && m.state.ActiveSwarm != nil {
		content = m.renderChat(width, height)
	} else {
		content = m.renderHome(width, height)
	}
	if m.modal.Kind != "" {
		content = m.renderModal(content, width, height)
	}
	v := tea.NewView(base.Render(content))
	v.AltScreen = true
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
		if swarm := m.selectedSwarm(); swarm != nil {
			if err := m.service.OpenSwarm(swarm.ID); err != nil {
				m.status = err.Error()
			}
		}
	case "n":
		m.prompt.SetValue("")
		m.prompt.Placeholder = "Swarm name"
		m.prompt.Focus()
		m.modal = modalState{Kind: "new", Title: "Create Swarm", Message: "Name your new swarm."}
	case "i":
		swarm := m.selectedSwarm()
		if swarm == nil {
			m.status = "select a swarm first"
			return m, nil
		}
		if _, err := m.service.GenerateInvite(swarm.ID); err != nil {
			m.status = err.Error()
		}
	case "j":
		if m.selectedNearby() == nil {
			m.status = "select a nearby peer first"
			return m, nil
		}
		m.prompt.SetValue("")
		m.prompt.Placeholder = "Invite code"
		m.prompt.Focus()
		m.modal = modalState{Kind: "join", Title: "Join Swarm", Message: "Enter the invite code from a nearby peer."}
	}
	return m, nil
}

func (m *modelUI) handleChat(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if err := m.service.LeaveSwarm(); err != nil {
			m.status = err.Error()
		}
		m.mode = "home"
		m.composer.Blur()
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
		m.syncTranscript()
		return m, nil
	}
	// Everything else (including shift+enter for newline) goes to textarea.
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	return m, cmd
}

func (m *modelUI) handleModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.modal.Kind {
	case "new":
		return m.handleCreateSwarmModal(msg)
	case "join":
		return m.handleJoinModal(msg)
	case "invite":
		if msg.String() == "esc" || msg.String() == "enter" || msg.String() == " " {
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

func (m *modelUI) applyAppEvent(event app.Event) {
	switch event.Type {
	case app.EventSync:
		m.state = event.Snapshot
		if m.state.ActiveSwarm != nil {
			m.mode = "chat"
			m.composer.Focus()
		} else {
			m.mode = "home"
			m.composer.Blur()
		}
		if m.swarmIdx >= len(m.state.Swarms) && len(m.state.Swarms) > 0 {
			m.swarmIdx = len(m.state.Swarms) - 1
		}
		if m.nearbyIdx >= len(m.state.Nearby) && len(m.state.Nearby) > 0 {
			m.nearbyIdx = len(m.state.Nearby) - 1
		}
		m.syncLayout()
	case app.EventToast:
		m.state = event.Snapshot
		m.status = event.Message
		m.syncLayout()
	case app.EventInvite:
		m.state = event.Snapshot
		m.modal = modalState{
			Kind:    "invite",
			Title:   "Invite Ready",
			Message: "Share this code with a nearby peer.",
			Invite:  event.Invite,
		}
	case app.EventApproval:
		m.state = event.Snapshot
		m.modal = modalState{
			Kind:     "approval",
			Title:    "Approve Pairing",
			Message:  approvalMessage(event),
			Approval: &event,
		}
	}
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
		m.syncTranscript()
	}
}

func (m *modelUI) syncTranscript() {
	m.messages.SetContent(m.renderTranscript(m.state.Transcript, m.messages.Width()))
	m.messages.GotoBottom()
}

// ---------------------------------------------------------------------------
// Render: Home
// ---------------------------------------------------------------------------

func (m *modelUI) renderHome(width, height int) string {
	header := lipgloss.NewStyle().Padding(1, 2, 0, 2).Width(width).Render(
		titleStyle.Render("yap") + mutedStyle.Render("  ") +
			lipgloss.NewStyle().Foreground(colorStrong).Bold(true).Render(m.state.Identity.Name) +
			mutedStyle.Render("  "+shortID(m.state.Identity.PeerID)),
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
	swarms := panelStyle.Width(leftWidth).Height(panelHeight).Render(m.renderSwarms())
	nearby := panelStyle.Width(rightWidth).Height(panelHeight).Render(m.renderNearby())

	body := lipgloss.JoinHorizontal(lipgloss.Top, swarms, nearby)

	footer := lipgloss.NewStyle().Padding(0, 2).Width(width).Render(
		statusStyle.Render(m.status) + "\n" +
			mutedStyle.Render("tab focus  ·  ↑↓ select  ·  enter open  ·  n new  ·  i invite  ·  j join  ·  q quit"),
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *modelUI) renderSwarms() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Swarms"))
	if len(m.state.Swarms) == 0 {
		lines = append(lines, "", mutedStyle.Render("No swarms yet. Press n to create one."))
		return strings.Join(lines, "\n")
	}
	for i, swarm := range m.state.Swarms {
		label := fmt.Sprintf("%s  %s", swarm.Name, mutedStyle.Render(shortID(swarm.ID)))
		if i == m.swarmIdx && m.focus == "swarms" {
			label = selectedStyle.Render(label)
		}
		lines = append(lines, "")
		lines = append(lines, label)
		meta := fmt.Sprintf("%d peers", len(swarm.TrustedPeers))
		if !swarm.LastOpened.IsZero() {
			meta += " · " + swarm.LastOpened.Format("Jan 2 15:04")
		}
		lines = append(lines, mutedStyle.Render(meta))
	}
	return strings.Join(lines, "\n")
}

func (m *modelUI) renderNearby() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Nearby"))
	if len(m.state.Nearby) == 0 {
		lines = append(lines, "", mutedStyle.Render("Scanning..."))
		return strings.Join(lines, "\n")
	}
	for i, peerInfo := range m.state.Nearby {
		label := nearbyLabel(peerInfo)
		if i == m.nearbyIdx && m.focus == "nearby" {
			label = selectedStyle.Render(label)
		}
		lines = append(lines, "")
		lines = append(lines, label)
		lines = append(lines, mutedStyle.Render(shortID(peerInfo.PeerID)+"  seen "+peerInfo.LastSeen.Format("15:04")))
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Render: Chat
// ---------------------------------------------------------------------------

func (m *modelUI) renderChat(width, height int) string {
	active := m.state.ActiveSwarm
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
			mutedStyle.Render(fmt.Sprintf("  %d %s", peerCount, peerWord)) +
			"  " + statusStyle.Render(m.status),
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
	m.syncTranscript()

	main := panelStyle.Width(mainWidth).Height(msgHeight + 6).Render(
		m.messages.View() + "\n" + m.composer.View(),
	)

	body := main
	if sidebarWidth > 0 {
		peers := panelStyle.Width(sidebarWidth).Height(msgHeight + 6).Render(m.renderPresence())
		body = lipgloss.JoinHorizontal(lipgloss.Top, main, peers)
	}

	footer := lipgloss.NewStyle().Padding(0, 2).Width(width).Render(
		mutedStyle.Render("enter send  ·  shift+enter newline  ·  esc back  ·  ctrl+c quit"),
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
		lines = append(lines, dot+"  "+lipgloss.NewStyle().Foreground(colorStrong).Render(name))
	}
	return strings.Join(lines, "\n")
}

func (m *modelUI) renderTranscript(entries []model.TranscriptEntry, width int) string {
	if width <= 0 {
		width = 70
	}
	var lines []string
	if len(entries) == 0 {
		lines = append(lines, mutedStyle.Render("No messages yet."))
	}
	for _, entry := range entries {
		switch entry.Kind {
		case "chat":
			lines = append(lines, renderChatEntry(entry))
		case "join":
			lines = append(lines, lipgloss.NewStyle().Foreground(colorJoin).Render(fmt.Sprintf("%s joined", entry.SenderName)))
		case "leave":
			lines = append(lines, lipgloss.NewStyle().Foreground(colorLeave).Render(fmt.Sprintf("%s left", entry.SenderName)))
		default:
			if strings.TrimSpace(entry.Body) != "" {
				lines = append(lines, mutedStyle.Render(entry.Body))
			}
		}
	}
	return strings.Join(lines, "\n\n")
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
	case "new", "join":
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
		lines = append(lines, mutedStyle.Render("enter or esc to close"))
	case "approval":
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("y accept  ·  n reject"))
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (m *modelUI) selectedSwarm() *model.Swarm {
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

func renderChatEntry(entry model.TranscriptEntry) string {
	header := mutedStyle.Render(entry.SentAt.Format("15:04")) + "  "
	nameStyle := lipgloss.NewStyle().Foreground(colorPeer).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorPeer)
	if entry.Local {
		nameStyle = lipgloss.NewStyle().Foreground(colorSelf).Bold(true)
		bodyStyle = lipgloss.NewStyle().Foreground(colorSelf)
	}
	return header + nameStyle.Render(entry.SenderName) + "\n" + bodyStyle.Render(entry.Body)
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
