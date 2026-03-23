package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"yap/internal/app"
	"yap/internal/model"
)

func newTestComposer() textarea.Model {
	composer := textarea.New()
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

func TestHandleChatDoesNotTreatDAsCommandWhileTranscriptFocused(t *testing.T) {
	composer := newTestComposer()

	m := &modelUI{
		mode:     "chat",
		focus:    "transcript",
		state:    app.State{SelectedSwarm: &model.Swarm{ID: "swarm-1", Name: "Alpha"}},
		composer: composer,
	}

	modelOut, _ := m.handleChat(tea.KeyPressMsg{Code: 'd', Text: "d"})
	got := modelOut.(*modelUI)

	if got.modal.Kind != "" {
		t.Fatalf("modal.Kind = %q, want empty", got.modal.Kind)
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
