package ui

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"yap/internal/app"
	"yap/internal/ascii"
	"yap/internal/model"
)

func newTestComposer() textarea.Model {
	composer := textarea.New()
	composer.ShowLineNumbers = false
	km := composer.KeyMap
	km.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))
	composer.KeyMap = km
	composer.Focus()
	return composer
}

func TestHandleChatDoesNotTreatDAsCommandWhileComposerFocused(t *testing.T) {
	composer := newTestComposer()

	m := &modelUI{
		mode:     "chat",
		focus:    "composer",
		state:    app.State{SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"}},
		composer: composer,
	}

	modelOut, _ := m.handleChat(tea.KeyPressMsg{Code: 'd', Text: "d"})
	got := modelOut.(*modelUI)

	if got.modal.Kind != "" {
		t.Fatalf("modal.Kind = %q, want empty", got.modal.Kind)
	}
	if got.composer.Value() != "d" {
		t.Fatalf("composer.Value() = %q, want %q", got.composer.Value(), "d")
	}
}

func TestHandleChatShiftEnterInsertsNewline(t *testing.T) {
	composer := newTestComposer()

	m := &modelUI{
		mode:     "chat",
		focus:    "composer",
		state:    app.State{SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"}},
		composer: composer,
	}

	modelOut, _ := m.handleChat(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	got := modelOut.(*modelUI)

	if got.composer.Value() != "\n" {
		t.Fatalf("composer.Value() = %q, want newline", got.composer.Value())
	}
}

func TestUpdatePasteMsgInsertsASCIIArtAtCursor(t *testing.T) {
	t.Parallel()

	composer := newTestComposer()
	composer.SetValue("draft ")
	path := writeUITestPNG(t, "sample.png", 2, 2, color.Gray{Y: 255})

	m := &modelUI{
		mode:     "chat",
		focus:    "composer",
		width:    80,
		state:    app.State{SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"}},
		composer: composer,
	}

	expectedArt, err := ascii.Convert(path, m.asciiPasteWidth())
	if err != nil {
		t.Fatalf("ascii.Convert() error = %v", err)
	}

	modelOut, _ := m.Update(tea.PasteMsg{Content: path})
	got := modelOut.(*modelUI)

	if got.composer.Value() != "draft "+expectedArt {
		t.Fatalf("composer.Value() = %q, want %q", got.composer.Value(), "draft "+expectedArt)
	}
}

func TestUpdatePasteMsgLeavesPathTextOutsideComposerFocus(t *testing.T) {
	t.Parallel()

	composer := newTestComposer()
	path := writeUITestPNG(t, "sample.png", 2, 2, color.Gray{Y: 255})

	m := &modelUI{
		mode:     "chat",
		focus:    "peers",
		width:    80,
		state:    app.State{SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"}},
		composer: composer,
	}

	modelOut, _ := m.Update(tea.PasteMsg{Content: path})
	got := modelOut.(*modelUI)

	if got.composer.Value() != path {
		t.Fatalf("composer.Value() = %q, want %q", got.composer.Value(), path)
	}
}

func TestHandleHomeInviteRequiresSwarmFocus(t *testing.T) {
	m := &modelUI{
		mode:  "home",
		focus: "nearby",
		state: app.State{
			Identity: model.Identity{Name: "me"},
			Swarms:   []app.SwarmSummary{{Swarm: model.Swarm{ID: "swarm-1", Name: "Alpha"}}},
			Nearby:   []model.NearbyPeer{{PeerID: "peer-1", Name: "Peer"}},
		},
	}

	modelOut, _ := m.handleHome(tea.KeyPressMsg{Code: 'i', Text: "i"})
	got := modelOut.(*modelUI)

	if got.status != "focus swarms to invite a room" {
		t.Fatalf("status = %q", got.status)
	}
}

func TestHandleHomeJoinRequiresNearbyFocus(t *testing.T) {
	m := &modelUI{
		mode:  "home",
		focus: "swarms",
		state: app.State{
			Identity: model.Identity{Name: "me"},
			Swarms:   []app.SwarmSummary{{Swarm: model.Swarm{ID: "swarm-1", Name: "Alpha"}}},
			Nearby:   []model.NearbyPeer{{PeerID: "peer-1", Name: "Peer"}},
		},
	}

	modelOut, _ := m.handleHome(tea.KeyPressMsg{Code: 'j', Text: "j"})
	got := modelOut.(*modelUI)

	if got.status != "focus nearby to join with a code" {
		t.Fatalf("status = %q", got.status)
	}
}

func TestHandleHomeRenameOpensModal(t *testing.T) {
	m := &modelUI{
		mode:   "home",
		focus:  "swarms",
		prompt: textinput.New(),
		state: app.State{
			Identity: model.Identity{Name: "me"},
		},
	}

	modelOut, _ := m.handleHome(tea.KeyPressMsg{Code: 'r', Text: "r"})
	got := modelOut.(*modelUI)

	if got.modal.Kind != "rename" {
		t.Fatalf("modal.Kind = %q, want rename", got.modal.Kind)
	}
	if got.prompt.Value() != "me" {
		t.Fatalf("prompt.Value() = %q, want me", got.prompt.Value())
	}
}

func TestHandleChatTabCompletesMentionBeforeChangingFocus(t *testing.T) {
	composer := newTestComposer()
	composer.SetValue("@pe")

	m := &modelUI{
		mode:     "chat",
		focus:    "composer",
		state:    app.State{Identity: model.Identity{Name: "me", PeerID: "self"}, SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha", TrustedPeers: []model.TrustedPeer{{PeerID: "peer-1", Name: "Peer Name"}}}},
		composer: composer,
	}

	modelOut, _ := m.handleChat(tea.KeyPressMsg{Code: tea.KeyTab})
	got := modelOut.(*modelUI)

	if got.focus != "composer" {
		t.Fatalf("focus = %q, want composer", got.focus)
	}
	if got.composer.Value() != "@peer-name " {
		t.Fatalf("composer.Value() = %q, want %q", got.composer.Value(), "@peer-name ")
	}
}

func TestHandleChatTabCyclesFocusAcrossComposerPeersAndSwarms(t *testing.T) {
	composer := newTestComposer()

	m := &modelUI{
		mode:     "chat",
		focus:    "composer",
		state:    app.State{SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"}},
		composer: composer,
	}

	modelOut, _ := m.handleChat(tea.KeyPressMsg{Code: tea.KeyTab})
	got := modelOut.(*modelUI)
	if got.focus != "peers" {
		t.Fatalf("focus after first tab = %q, want peers", got.focus)
	}

	modelOut, _ = got.handleChat(tea.KeyPressMsg{Code: tea.KeyTab})
	got = modelOut.(*modelUI)
	if got.focus != "swarms" {
		t.Fatalf("focus after second tab = %q, want swarms", got.focus)
	}

	modelOut, _ = got.handleChat(tea.KeyPressMsg{Code: tea.KeyTab})
	got = modelOut.(*modelUI)
	if got.focus != "composer" {
		t.Fatalf("focus after third tab = %q, want composer", got.focus)
	}
}

func TestRenderTranscriptShowsUnreadSeparator(t *testing.T) {
	m := &modelUI{
		state: app.State{
			Identity:      model.Identity{Name: "me", PeerID: "self"},
			SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha", LastOpened: time.Unix(15, 0)},
		},
	}

	content := m.renderTranscript([]model.TranscriptEntry{
		{ID: "msg-1", Kind: "chat", SenderPeerID: "peer-1", SenderName: "Peer", Body: "hello there", SentAt: time.Unix(10, 0)},
		{ID: "msg-2", Kind: "chat", SenderPeerID: "peer-2", SenderName: "Other", Body: "replying", SentAt: time.Unix(20, 0)},
	}, 80)

	if !strings.Contains(content, "── new messages ──") {
		t.Fatalf("renderTranscript() missing unread separator:\n%s", content)
	}
}

func TestRenderTranscriptHidesJoinLeaveAndShowsRename(t *testing.T) {
	m := &modelUI{
		state: app.State{
			Identity:      model.Identity{Name: "me", PeerID: "self"},
			SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"},
		},
	}

	content := m.renderTranscript([]model.TranscriptEntry{
		{ID: "join-1", Kind: "join", SenderPeerID: "peer-1", SenderName: "Peer", SentAt: time.Unix(10, 0)},
		{ID: "rename-1", Kind: "rename", SenderPeerID: "peer-1", SenderName: "New Name", Body: "Old Name", SentAt: time.Unix(20, 0)},
		{ID: "leave-1", Kind: "leave", SenderPeerID: "peer-1", SenderName: "New Name", SentAt: time.Unix(30, 0)},
	}, 80)

	if strings.Contains(content, "joined") {
		t.Fatalf("renderTranscript() unexpectedly showed join event:\n%s", content)
	}
	if strings.Contains(content, "left") {
		t.Fatalf("renderTranscript() unexpectedly showed leave event:\n%s", content)
	}
	if !strings.Contains(content, "Old Name is now") || !strings.Contains(content, "New Name") {
		t.Fatalf("renderTranscript() missing rename entry:\n%s", content)
	}
}

func TestConnectionSummaryReflectsPeerHealth(t *testing.T) {
	m := &modelUI{
		state: app.State{
			Identity:      model.Identity{Name: "me", PeerID: "self"},
			SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"},
			Swarms:        []app.SwarmSummary{{Swarm: model.Swarm{ID: "swarm-1", Name: "Alpha"}, Connected: true}},
			Presence: []model.Presence{
				{PeerID: "self", State: "online"},
				{PeerID: "peer-1", State: "online"},
				{PeerID: "peer-2", State: "stale"},
			},
		},
	}

	if got := m.connectionSummary(); got != "◉ 2 online · 1 stale" {
		t.Fatalf("connectionSummary() = %q", got)
	}
}

func TestChatLayoutWidths(t *testing.T) {
	t.Parallel()

	mainWidth, sidebarWidth, contentWidth := chatLayoutWidths(80)
	if mainWidth != 50 || sidebarWidth != 30 || contentWidth != 44 {
		t.Fatalf("chatLayoutWidths(80) = (%d, %d, %d), want (50, 30, 44)", mainWidth, sidebarWidth, contentWidth)
	}
}

func TestASCIIPasteWidthUsesTextareaInnerWidth(t *testing.T) {
	t.Parallel()

	composer := newTestComposer()
	composer.SetWidth(44)

	m := &modelUI{composer: composer}
	if got := m.asciiPasteWidth(); got != 24 {
		t.Fatalf("asciiPasteWidth() = %d, want %d", got, 24)
	}
}

func TestASCIIPasteWidthRespectsTranscriptWidth(t *testing.T) {
	t.Parallel()

	composer := newTestComposer()
	composer.SetWidth(60)
	messages := viewport.New()
	messages.SetWidth(30)

	m := &modelUI{
		composer: composer,
		messages: messages,
	}
	if got := m.asciiPasteWidth(); got != 15 {
		t.Fatalf("asciiPasteWidth() = %d, want %d", got, 15)
	}
}

func TestRenderTranscriptDoesNotMarkLocalMessagesAsNew(t *testing.T) {
	m := &modelUI{
		state: app.State{
			Identity:      model.Identity{Name: "me", PeerID: "self"},
			SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha", LastOpened: time.Unix(15, 0)},
		},
	}

	content := m.renderTranscript([]model.TranscriptEntry{
		{ID: "msg-1", Kind: "chat", SenderPeerID: "self", SenderName: "Me", Body: "my update", Local: true, SentAt: time.Unix(20, 0)},
		{ID: "msg-2", Kind: "chat", SenderPeerID: "peer-1", SenderName: "Peer", Body: "reply", SentAt: time.Unix(25, 0)},
	}, 80)

	localIndex := strings.Index(content, "Me")
	dividerIndex := strings.Index(content, "── new messages ──")
	remoteIndex := strings.Index(content, "Peer")
	if !(localIndex >= 0 && dividerIndex > localIndex && remoteIndex > dividerIndex) {
		t.Fatalf("unexpected divider ordering:\n%s", content)
	}
}

func TestRenderTranscriptHighlightsMentionBadge(t *testing.T) {
	m := &modelUI{
		state: app.State{
			Identity:      model.Identity{Name: "Colin", PeerID: "self"},
			SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"},
		},
	}

	content := m.renderTranscript([]model.TranscriptEntry{
		{ID: "msg-1", Kind: "chat", SenderPeerID: "peer-1", SenderName: "Peer", Body: "hey @colin", SentAt: time.Unix(20, 0)},
	}, 80)

	if !strings.Contains(content, "@you") {
		t.Fatalf("renderTranscript() missing mention badge:\n%s", content)
	}
}

func TestApplyAppEventPreservesChatSidebarFocus(t *testing.T) {
	composer := newTestComposer()
	composer.Blur()

	m := &modelUI{
		mode:     "chat",
		focus:    "swarms",
		composer: composer,
		state: app.State{
			Version:       1,
			Identity:      model.Identity{Name: "me", PeerID: "self"},
			SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"},
			Swarms:        []app.SwarmSummary{{Swarm: model.Swarm{ID: "swarm-1", Name: "Alpha"}, Connected: true}},
		},
	}

	m.applyAppEvent(app.Event{
		Type: app.EventSync,
		Snapshot: app.State{
			Version:       2,
			Identity:      model.Identity{Name: "me", PeerID: "self"},
			SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"},
			Swarms:        []app.SwarmSummary{{Swarm: model.Swarm{ID: "swarm-1", Name: "Alpha"}, Connected: true}},
		},
	})

	if got, want := m.focus, "swarms"; got != want {
		t.Fatalf("focus = %q, want %q", got, want)
	}
}

func writeUITestPNG(t *testing.T, name string, width int, height int, fill color.Gray) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	img := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetGray(x, y, fill)
		}
	}

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create() error = %v", err)
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return path
}
