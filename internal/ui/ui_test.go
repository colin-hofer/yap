package ui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"yap/internal/app"
	"yap/internal/model"
)

func TestHandleChatDoesNotTreatDAsCommandWhileComposerFocused(t *testing.T) {
	composer := textarea.New()
	composer.Focus()

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
	composer := textarea.New()

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
