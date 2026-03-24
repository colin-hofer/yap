package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"yap/internal/model"
	"yap/internal/p2p"
	"yap/internal/store"
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

func TestPrepareSwarmRotationRequiresOwner(t *testing.T) {
	t.Parallel()

	_, _, err := prepareSwarmRotation(model.Swarm{
		ID:          "swarm-1",
		Name:        "Alpha",
		RoomKey:     "room-key",
		OwnerPeerID: "peer-owner",
		Version:     2,
		TrustedPeers: []model.TrustedPeer{
			{PeerID: "peer-owner", Name: "Owner"},
			{PeerID: "peer-self", Name: "Self"},
		},
	}, model.TrustedPeer{PeerID: "peer-self", Name: "Self"}, "")
	if err == nil {
		t.Fatal("prepareSwarmRotation() unexpectedly allowed a non-owner to rotate the room key")
	}
}

func TestPrepareSwarmRotationRemovesPeerAndBumpsVersion(t *testing.T) {
	t.Parallel()

	updated, removed, err := prepareSwarmRotation(model.Swarm{
		ID:          "swarm-1",
		Name:        "Alpha",
		RoomKey:     "room-key",
		OwnerPeerID: "peer-owner",
		Version:     4,
		TrustedPeers: []model.TrustedPeer{
			{PeerID: "peer-owner", Name: "Owner"},
			{PeerID: "peer-a", Name: "Alice"},
			{PeerID: "peer-b", Name: "Bob"},
		},
	}, model.TrustedPeer{PeerID: "peer-owner", Name: "Owner"}, "peer-b")
	if err != nil {
		t.Fatalf("prepareSwarmRotation() error = %v", err)
	}
	if removed == nil || removed.PeerID != "peer-b" {
		t.Fatalf("removed = %+v, want peer-b", removed)
	}
	if got, want := updated.Version, uint64(5); got != want {
		t.Fatalf("updated.Version = %d, want %d", got, want)
	}
	if updated.RoomKey == "room-key" {
		t.Fatal("updated.RoomKey unexpectedly unchanged")
	}
	if swarmHasTrustedPeerID(updated.TrustedPeers, "peer-b") {
		t.Fatal("updated swarm still trusts revoked peer")
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

func TestHandleNodeEventNearbySnapshotReplacesPeers(t *testing.T) {
	svc := &Service{
		ctx:          context.Background(),
		nearby:       map[string]model.NearbyPeer{"peer-old": {PeerID: "peer-old", Name: "Old"}},
		events:       make(chan Event, 8),
		unread:       make(map[string]int),
		connected:    make(map[string]bool),
		lastActivity: make(map[string]time.Time),
	}

	svc.handleNodeEvent(p2p.Event{
		Kind: p2p.EventNearbySnapshot,
		NearbyPeers: []model.NearbyPeer{
			{PeerID: "peer-new", Name: "New"},
		},
	})

	if got := len(svc.nearby); got != 1 {
		t.Fatalf("len(nearby) = %d, want 1", got)
	}
	if _, ok := svc.nearby["peer-old"]; ok {
		t.Fatal("peer-old still present after snapshot replacement")
	}
	if _, ok := svc.nearby["peer-new"]; !ok {
		t.Fatal("peer-new missing after snapshot replacement")
	}
}

func TestSnapshotSortsSwarmsByRecentActivity(t *testing.T) {
	svc := &Service{
		identity: model.Identity{Name: "me"},
		swarms: []model.Swarm{
			{ID: "swarm-1", Name: "Alpha"},
			{ID: "swarm-2", Name: "Beta"},
		},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       map[string]int{"swarm-1": 0, "swarm-2": 2},
		connected:    map[string]bool{"swarm-1": true, "swarm-2": true},
		lastActivity: map[string]time.Time{"swarm-1": time.Unix(10, 0), "swarm-2": time.Unix(20, 0)},
	}

	state := svc.snapshotLocked()
	if got, want := state.Swarms[0].Swarm.ID, "swarm-2"; got != want {
		t.Fatalf("state.Swarms[0].Swarm.ID = %q, want %q", got, want)
	}
}

func TestHandlePresenceEventUpdatesExistingTrustedPeerMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	st := store.New(root)
	if err := st.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	swarm := model.Swarm{
		ID:   "swarm-1",
		Name: "Alpha",
		TrustedPeers: []model.TrustedPeer{
			{PeerID: "peer-known", Name: "Known", Fingerprint: "known-fp"},
		},
	}
	if err := st.SaveSwarm(swarm); err != nil {
		t.Fatalf("SaveSwarm() error = %v", err)
	}
	svc := &Service{
		ctx:          context.Background(),
		store:        st,
		swarms:       []model.Swarm{swarm},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       make(map[string]int),
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		events:       make(chan Event, 8),
	}

	svc.handlePresenceEvent("swarm-1", []model.Presence{
		{
			PeerID:      "peer-known",
			Name:        "Renamed Peer",
			Fingerprint: "known-fp-2",
			Addrs:       []string{"/ip4/127.0.0.1/tcp/4001"},
			LastSeen:    time.Unix(20, 0),
		},
	})

	updated, ok := svc.findSwarmLocked("swarm-1")
	if !ok {
		t.Fatal("swarm not found after presence update")
	}
	if got, want := len(updated.TrustedPeers), 1; got != want {
		t.Fatalf("len(updated.TrustedPeers) = %d, want %d", got, want)
	}
	if got, want := updated.TrustedPeers[0].Name, "Renamed Peer"; got != want {
		t.Fatalf("updated.TrustedPeers[0].Name = %q, want %q", got, want)
	}
	if got, want := updated.TrustedPeers[0].Fingerprint, "known-fp-2"; got != want {
		t.Fatalf("updated.TrustedPeers[0].Fingerprint = %q, want %q", got, want)
	}
	if got, want := updated.TrustedPeers[0].Addrs[0], "/ip4/127.0.0.1/tcp/4001"; got != want {
		t.Fatalf("updated.TrustedPeers[0].Addrs[0] = %q, want %q", got, want)
	}
	if got, want := updated.TrustedPeers[0].LastSeen, time.Unix(20, 0); !got.Equal(want) {
		t.Fatalf("updated.TrustedPeers[0].LastSeen = %v, want %v", got, want)
	}
	if got, want := len(svc.presence["swarm-1"]), 1; got != want {
		t.Fatalf("len(svc.presence[swarm-1]) = %d, want %d", got, want)
	}
	if got, want := svc.presence["swarm-1"][0].PeerID, "peer-known"; got != want {
		t.Fatalf("svc.presence[swarm-1][0].PeerID = %q, want %q", got, want)
	}

	persisted, err := st.LoadSwarm("swarm-1")
	if err != nil {
		t.Fatalf("LoadSwarm() error = %v", err)
	}
	if got, want := persisted.TrustedPeers[0].Name, "Renamed Peer"; got != want {
		t.Fatalf("persisted.TrustedPeers[0].Name = %q, want %q", got, want)
	}
}

func TestHandlePresenceEventDoesNotAddUnknownTrustedPeer(t *testing.T) {
	swarm := model.Swarm{
		ID:   "swarm-1",
		Name: "Alpha",
		TrustedPeers: []model.TrustedPeer{
			{PeerID: "peer-known", Name: "Known", Fingerprint: "known-fp"},
		},
	}
	svc := &Service{
		ctx:          context.Background(),
		swarms:       []model.Swarm{swarm},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       make(map[string]int),
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		events:       make(chan Event, 8),
	}

	svc.handlePresenceEvent("swarm-1", []model.Presence{
		{
			PeerID:      "peer-1",
			Name:        "Peer",
			Fingerprint: "fp",
			Addrs:       []string{"/ip4/127.0.0.1/tcp/4001"},
		},
	})

	updated, ok := svc.findSwarmLocked("swarm-1")
	if !ok {
		t.Fatal("swarm not found after presence update")
	}
	if got, want := len(updated.TrustedPeers), 1; got != want {
		t.Fatalf("len(updated.TrustedPeers) = %d, want %d", got, want)
	}
	if got, want := updated.TrustedPeers[0].PeerID, "peer-known"; got != want {
		t.Fatalf("updated.TrustedPeers[0].PeerID = %q, want %q", got, want)
	}
	if got, want := len(svc.presence["swarm-1"]), 1; got != want {
		t.Fatalf("len(svc.presence[swarm-1]) = %d, want %d", got, want)
	}
	if got, want := svc.presence["swarm-1"][0].PeerID, "peer-1"; got != want {
		t.Fatalf("svc.presence[swarm-1][0].PeerID = %q, want %q", got, want)
	}
}

func TestHandlePresenceEventTracksOnlineRemoteConnectivity(t *testing.T) {
	svc := &Service{
		ctx:          context.Background(),
		identity:     model.Identity{PeerID: "self"},
		swarms:       []model.Swarm{{ID: "swarm-1", Name: "Alpha"}},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       make(map[string]int),
		connected:    make(map[string]bool),
		lastActivity: make(map[string]time.Time),
		events:       make(chan Event, 8),
	}

	svc.handlePresenceEvent("swarm-1", []model.Presence{
		{PeerID: "self", State: "online"},
		{PeerID: "peer-1", State: "stale"},
	})
	if svc.connected["swarm-1"] {
		t.Fatal("connected[swarm-1] = true, want false with no online remote peers")
	}

	svc.handlePresenceEvent("swarm-1", []model.Presence{
		{PeerID: "self", State: "online"},
		{PeerID: "peer-1", State: "online"},
	})
	if !svc.connected["swarm-1"] {
		t.Fatal("connected[swarm-1] = false, want true with an online remote peer")
	}
}

func TestLeaveSwarmMarksSelectedSwarmRead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	st := store.New(root)
	if err := st.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	swarm := model.Swarm{ID: "swarm-1", Name: "Alpha", LastOpened: time.Unix(10, 0)}
	if err := st.SaveSwarm(swarm); err != nil {
		t.Fatalf("SaveSwarm() error = %v", err)
	}

	svc := &Service{
		ctx:          context.Background(),
		store:        st,
		swarms:       []model.Swarm{swarm},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       map[string]int{"swarm-1": 3},
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		selectedID:   "swarm-1",
		events:       make(chan Event, 8),
	}

	if err := svc.LeaveSwarm(); err != nil {
		t.Fatalf("LeaveSwarm() error = %v", err)
	}
	if got := svc.selectedID; got != "" {
		t.Fatalf("selectedID = %q, want empty", got)
	}
	if got := svc.unread["swarm-1"]; got != 0 {
		t.Fatalf("unread = %d, want 0", got)
	}

	persisted, err := st.LoadSwarm("swarm-1")
	if err != nil {
		t.Fatalf("LoadSwarm() error = %v", err)
	}
	if !persisted.LastOpened.After(time.Unix(10, 0)) {
		t.Fatalf("LastOpened = %v, want later than initial timestamp", persisted.LastOpened)
	}
}

func TestHandleTranscriptEventEmitsMentionToastForBackgroundMessages(t *testing.T) {
	svc := &Service{
		ctx:          context.Background(),
		identity:     model.Identity{Name: "Colin", PeerID: "self"},
		swarms:       []model.Swarm{{ID: "swarm-1", Name: "Alpha"}},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       make(map[string]int),
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		events:       make(chan Event, 8),
	}

	svc.handleTranscriptEvent(model.TranscriptEntry{
		ID:           "msg-1",
		SwarmID:      "swarm-1",
		Kind:         "chat",
		SenderPeerID: "peer-1",
		SenderName:   "peer",
		Body:         "hey @colin can you check this?",
		SentAt:       time.Unix(10, 0),
	}, false)

	select {
	case event := <-svc.events:
		if event.Type != EventToast {
			t.Fatalf("event.Type = %q, want %q", event.Type, EventToast)
		}
		if got, want := event.Message, "mention in Alpha from peer"; got != want {
			t.Fatalf("event.Message = %q, want %q", got, want)
		}
	default:
		t.Fatal("expected mention toast event")
	}
}

func TestHandleTranscriptEventEmitsToastAndUnreadForBackgroundRename(t *testing.T) {
	svc := &Service{
		ctx:          context.Background(),
		identity:     model.Identity{Name: "me", PeerID: "self"},
		swarms:       []model.Swarm{{ID: "swarm-1", Name: "Alpha"}},
		nearby:       make(map[string]model.NearbyPeer),
		transcripts:  make(map[string][]model.TranscriptEntry),
		presence:     make(map[string][]model.Presence),
		unread:       make(map[string]int),
		connected:    map[string]bool{"swarm-1": true},
		lastActivity: make(map[string]time.Time),
		events:       make(chan Event, 8),
	}

	svc.handleTranscriptEvent(model.TranscriptEntry{
		ID:           "rename-1",
		SwarmID:      "swarm-1",
		Kind:         "rename",
		SenderPeerID: "peer-1",
		SenderName:   "New Name",
		Body:         "Old Name",
		SentAt:       time.Unix(10, 0),
	}, false)

	if got, want := svc.unread["swarm-1"], 1; got != want {
		t.Fatalf("unread = %d, want %d", got, want)
	}

	select {
	case event := <-svc.events:
		if event.Type != EventToast {
			t.Fatalf("event.Type = %q, want %q", event.Type, EventToast)
		}
		if got, want := event.Message, "Old Name is now New Name in Alpha"; got != want {
			t.Fatalf("event.Message = %q, want %q", got, want)
		}
	default:
		t.Fatal("expected rename toast event")
	}
}
