package app

import (
	"context"
	"testing"
	"time"

	"yap/internal/model"
)

func TestHandleTranscriptEventTracksUnreadForUnselectedSwarm(t *testing.T) {
	svc := &Service{
		ctx:          context.Background(),
		identity:     model.Identity{Name: "me"},
		swarms:       []model.Swarm{{ID: "swarm-1", Name: "Alpha"}},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       make(map[string]int),
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		events:       make(chan Event, 8),
	}

	entry := model.TranscriptEntry{
		ID:           "msg-1",
		SwarmID:      "swarm-1",
		Kind:         "chat",
		SenderPeerID: "peer-1",
		SenderName:   "peer",
		Body:         "hello",
		SentAt:       time.Unix(10, 0),
	}
	svc.handleTranscriptEvent(entry, false)

	if got, want := svc.unread["swarm-1"], 1; got != want {
		t.Fatalf("unread = %d, want %d", got, want)
	}
	if got, want := len(svc.transcripts["swarm-1"]), 1; got != want {
		t.Fatalf("len(transcripts) = %d, want %d", got, want)
	}
	if got, want := svc.lastActivity["swarm-1"], entry.SentAt; !got.Equal(want) {
		t.Fatalf("lastActivity = %v, want %v", got, want)
	}
}

func TestHandleHistoryImportCountsOnlyChatEntries(t *testing.T) {
	svc := &Service{
		ctx:          context.Background(),
		identity:     model.Identity{Name: "me"},
		swarms:       []model.Swarm{{ID: "swarm-1", Name: "Alpha"}},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       make(map[string]int),
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		events:       make(chan Event, 8),
	}

	svc.handleHistoryImport("swarm-1", []model.TranscriptEntry{
		{ID: "join-1", SwarmID: "swarm-1", Kind: "join", SenderName: "peer", SentAt: time.Unix(5, 0)},
		{ID: "msg-1", SwarmID: "swarm-1", Kind: "chat", SenderName: "peer", Body: "hello", SentAt: time.Unix(10, 0)},
	})

	if got, want := svc.unread["swarm-1"], 1; got != want {
		t.Fatalf("unread = %d, want %d", got, want)
	}
	if got, want := len(svc.transcripts["swarm-1"]), 2; got != want {
		t.Fatalf("len(transcripts) = %d, want %d", got, want)
	}
}

func TestSnapshotWithEmptySelectionHasNoSelectedSwarm(t *testing.T) {
	svc := &Service{
		identity:     model.Identity{Name: "me"},
		swarms:       []model.Swarm{{ID: "swarm-1", Name: "Alpha"}},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  map[string][]model.TranscriptEntry{"swarm-1": {{ID: "msg-1", SwarmID: "swarm-1"}}},
		presence:     map[string][]model.Presence{"swarm-1": {{PeerID: "peer-1", Name: "peer"}}},
		unread:       make(map[string]int),
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		selectedID:   "",
	}

	state := svc.snapshotLocked()
	if state.SelectedSwarm != nil {
		t.Fatalf("SelectedSwarm = %+v, want nil", *state.SelectedSwarm)
	}
	if got := len(state.Transcript); got != 0 {
		t.Fatalf("len(Transcript) = %d, want 0", got)
	}
	if got := len(state.Presence); got != 0 {
		t.Fatalf("len(Presence) = %d, want 0", got)
	}
}

func TestFindSwarmLockedRejectsEmptyReference(t *testing.T) {
	svc := &Service{
		swarms: []model.Swarm{{ID: "swarm-1", Name: "Alpha"}},
	}

	if _, ok := svc.findSwarmLocked(""); ok {
		t.Fatal("findSwarmLocked(\"\") unexpectedly matched a swarm")
	}
	if _, ok := svc.findSwarmLocked("   "); ok {
		t.Fatal("findSwarmLocked(\"   \") unexpectedly matched a swarm")
	}
}
