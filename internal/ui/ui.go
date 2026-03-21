package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"yap/internal/app"
	"yap/internal/model"
)

var (
	colorBg       = lipgloss.Color("#0F1417")
	colorPanel    = lipgloss.Color("#172026")
	colorMuted    = lipgloss.Color("#6A7A87")
	colorAccent   = lipgloss.Color("#7FDBB6")
	colorAccent2  = lipgloss.Color("#F2C879")
	colorBorder   = lipgloss.Color("#31424D")
	colorStrong   = lipgloss.Color("#E8F0F2")
	colorDanger   = lipgloss.Color("#F16E5B")
	colorSelf     = lipgloss.Color("#9AD1FF")
	colorPeer     = lipgloss.Color("#F4F7F8")
	colorJoin     = lipgloss.Color("#9BE48D")
	colorLeave    = lipgloss.Color("#FFB199")
	colorStale    = lipgloss.Color("#E0BE75")
	panelStyle    = lipgloss.NewStyle().Background(colorPanel).Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Padding(1, 2)
	titleStyle    = lipgloss.NewStyle().Foreground(colorStrong).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	statusStyle   = lipgloss.NewStyle().Foreground(colorAccent2)
	dangerStyle   = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
	selectedStyle = lipgloss.NewStyle().Foreground(colorStrong).Background(lipgloss.Color("#24343E")).Padding(0, 1)
)

// Run starts the Bubble Tea program.
func Run(service *app.Service) error {
	model := newModel(service)
	program := tea.NewProgram(model, tea.WithAltScreen())
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
	composer.Placeholder = "Write a message. Ctrl+S sends."
	composer.CharLimit = 4000
	composer.SetHeight(4)
	composer.ShowLineNumbers = false
	composer.FocusedStyle.CursorLine = lipgloss.NewStyle()
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
		messages: viewport.New(0, 0),
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
	case tea.KeyMsg:
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

func (m *modelUI) View() string {
	width := m.width
	height := m.height
	if width == 0 {
		width = 110
	}
	if height == 0 {
		height = 34
	}

	base := lipgloss.NewStyle().Background(colorBg).Foreground(colorStrong).Width(width).Height(height)
	var content string
	if m.mode == "chat" && m.state.ActiveSwarm != nil {
		content = m.renderChat(width, height)
	} else {
		content = m.renderHome(width, height)
	}
	if m.modal.Kind != "" {
		content = m.renderModal(content, width, height)
	}
	return base.Render(content)
}

func (m *modelUI) handleHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		m.modal = modalState{Kind: "new", Title: "Create Swarm", Message: "Create a saved room with a persistent room key."}
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
		m.modal = modalState{Kind: "join", Title: "Join with Invite", Message: "Enter the invite code shared by the selected nearby peer."}
	}
	return m, nil
}

func (m *modelUI) handleChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	case "ctrl+s":
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
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	return m, cmd
}

func (m *modelUI) handleModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m *modelUI) handleCreateSwarmModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m *modelUI) handleJoinModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
			Message: "Share this code with a nearby peer. The invite expires automatically.",
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

func (m *modelUI) syncLayout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	if m.mode == "chat" {
		chatWidth := m.width - 34
		if chatWidth < 20 {
			chatWidth = m.width
		}
		msgHeight := m.height - 13
		if msgHeight < 6 {
			msgHeight = 6
		}
		m.messages.Width = chatWidth - 4
		m.messages.Height = msgHeight
		m.composer.SetWidth(chatWidth - 6)
		m.syncTranscript()
	}
}

func (m *modelUI) syncTranscript() {
	m.messages.SetContent(m.renderTranscript(m.state.Transcript, m.messages.Width))
	m.messages.GotoBottom()
}

func (m *modelUI) renderHome(width, height int) string {
	header := lipgloss.NewStyle().Padding(1, 2).Render(
		titleStyle.Render("yap") + "  " +
			mutedStyle.Render("LAN chat") + "\n" +
			accentStyle.Render(m.state.Identity.Name) + "  " + mutedStyle.Render(shortID(m.state.Identity.PeerID)),
	)

	leftWidth := width / 2
	if leftWidth < 40 {
		leftWidth = width
	}
	rightWidth := width - leftWidth
	if rightWidth < 32 {
		rightWidth = width
	}

	swarms := panelStyle.Width(leftWidth - 2).Height(height - 8).Render(m.renderSwarms())
	nearby := panelStyle.Width(rightWidth - 2).Height(height - 8).Render(m.renderNearby())

	body := lipgloss.JoinHorizontal(lipgloss.Top, swarms, nearby)
	footer := panelStyle.Width(width - 4).Render(
		statusStyle.Render("status") + "  " + m.status + "\n" +
			mutedStyle.Render("tab focus  •  enter open  •  n new swarm  •  i invite  •  j join with code  •  q quit"),
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *modelUI) renderChat(width, height int) string {
	active := m.state.ActiveSwarm
	if active == nil {
		return ""
	}
	header := panelStyle.Width(width - 4).Render(
		titleStyle.Render(active.Name) + "  " + mutedStyle.Render(shortID(active.ID)) + "\n" +
			mutedStyle.Render(fmt.Sprintf("%d peers", len(m.state.Presence))) + "  " +
			statusStyle.Render(m.status),
	)

	sidebarWidth := 30
	mainWidth := width - sidebarWidth - 4
	if mainWidth < 30 {
		mainWidth = width - 4
		sidebarWidth = 0
	}
	msgHeight := height - 14
	if msgHeight < 8 {
		msgHeight = 8
	}
	m.messages.Width = mainWidth - 4
	m.messages.Height = msgHeight
	m.composer.SetWidth(mainWidth - 6)
	m.syncTranscript()

	main := panelStyle.Width(mainWidth - 2).Height(msgHeight + 7).Render(
		m.messages.View() + "\n\n" + m.composer.View(),
	)
	peers := panelStyle.Width(sidebarWidth - 2).Height(msgHeight + 7).Render(m.renderPresence())
	footer := panelStyle.Width(width - 4).Render(mutedStyle.Render("ctrl+s send  •  enter newline  •  esc leave chat  •  ctrl+c quit"))

	body := main
	if sidebarWidth > 0 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, main, peers)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *modelUI) renderSwarms() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Saved Swarms"))
	lines = append(lines, mutedStyle.Render("Persistent rooms on this device."))
	if len(m.state.Swarms) == 0 {
		lines = append(lines, "", mutedStyle.Render("No swarms yet. Press n to create one."))
		return strings.Join(lines, "\n")
	}
	for i, swarm := range m.state.Swarms {
		label := fmt.Sprintf("%s  %s", swarm.Name, mutedStyle.Render(shortID(swarm.ID)))
		if i == m.swarmIdx && m.focus == "swarms" {
			label = selectedStyle.Render(label)
		}
		lines = append(lines, "", label)
		lines = append(lines, mutedStyle.Render(fmt.Sprintf("trusted peers %d", len(swarm.TrustedPeers))))
		if !swarm.LastOpened.IsZero() {
			lines = append(lines, mutedStyle.Render("opened "+swarm.LastOpened.Format("Jan 2 15:04")))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *modelUI) renderNearby() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Nearby Peers"))
	lines = append(lines, mutedStyle.Render("mDNS discovery on the local network."))
	if len(m.state.Nearby) == 0 {
		lines = append(lines, "", mutedStyle.Render("Waiting for other yap clients..."))
		return strings.Join(lines, "\n")
	}
	for i, peerInfo := range m.state.Nearby {
		label := nearbyLabel(peerInfo)
		if i == m.nearbyIdx && m.focus == "nearby" {
			label = selectedStyle.Render(label)
		}
		lines = append(lines, "", label)
		lines = append(lines, mutedStyle.Render(shortID(peerInfo.PeerID)))
		lines = append(lines, mutedStyle.Render("seen "+peerInfo.LastSeen.Format("15:04:05")))
	}
	return strings.Join(lines, "\n")
}

func (m *modelUI) renderPresence() string {
	var lines []string
	lines = append(lines, titleStyle.Render("Presence"))
	lines = append(lines, mutedStyle.Render("Room members by heartbeat state."))
	if len(m.state.Presence) == 0 {
		lines = append(lines, "", mutedStyle.Render("No presence data yet."))
		return strings.Join(lines, "\n")
	}
	for _, presence := range m.state.Presence {
		lines = append(lines, "")
		lines = append(lines, presenceStyle(presence.State).Render(presenceLabel(presence)))
		lines = append(lines, mutedStyle.Render(shortID(presence.PeerID)))
		if !presence.LastSeen.IsZero() {
			lines = append(lines, mutedStyle.Render("seen "+presence.LastSeen.Format("15:04:05")))
		}
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

func (m *modelUI) renderModal(content string, width, height int) string {
	boxWidth := width / 2
	if boxWidth < 42 {
		boxWidth = width - 8
	}
	box := panelStyle.Width(boxWidth).Render(m.modalBody())
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
		lines = append(lines, mutedStyle.Render("enter confirm  •  esc cancel"))
	case "invite":
		if m.modal.Invite != nil {
			lines = append(lines, "")
			lines = append(lines, accentStyle.Render("Code: "+m.modal.Invite.Code))
			lines = append(lines, mutedStyle.Render("Expires "+m.modal.Invite.ExpiresAt.Format(time.Kitchen)))
		}
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("enter or esc closes this card"))
	case "approval":
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("y accept  •  n reject"))
	}
	return strings.Join(lines, "\n")
}

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
	header := mutedStyle.Render(entry.SentAt.Format("15:04")) + " "
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

func presenceStyle(state string) lipgloss.Style {
	switch state {
	case "online":
		return lipgloss.NewStyle().Foreground(colorJoin).Bold(true)
	case "stale":
		return lipgloss.NewStyle().Foreground(colorStale).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(colorMuted)
	}
}

func presenceLabel(presence model.Presence) string {
	name := presence.Name
	if strings.TrimSpace(name) == "" {
		name = shortID(presence.PeerID)
	}
	return fmt.Sprintf("%s  [%s]", name, presence.State)
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
