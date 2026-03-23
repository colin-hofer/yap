package p2p

import (
	"context"
	"strings"
	"testing"
	"time"

	"yap/internal/model"
)

func TestListenAddrsDefaultsToTCP(t *testing.T) {
	t.Setenv("YAP_TRANSPORT", "")
	addrs := listenAddrs()
	if len(addrs) != 2 {
		t.Fatalf("len(addrs) = %d, want 2", len(addrs))
	}
	for _, addr := range addrs {
		if want := "/tcp/"; !strings.Contains(addr, want) {
			t.Fatalf("addr %q does not contain %q", addr, want)
		}
	}
}

func TestListenAddrsSupportsExplicitQUIC(t *testing.T) {
	t.Setenv("YAP_TRANSPORT", "quic")
	addrs := listenAddrs()
	if len(addrs) != 2 {
		t.Fatalf("len(addrs) = %d, want 2", len(addrs))
	}
	for _, addr := range addrs {
		if want := "/quic-v1"; !strings.Contains(addr, want) {
			t.Fatalf("addr %q does not contain %q", addr, want)
		}
	}
}

func TestTranscriptEntryFromEnvelopeUsesEnvelopeMetadata(t *testing.T) {
	sentAt := time.Unix(42, 0)
	env := envelope{
		ID:           "msg-1",
		Kind:         "chat",
		SenderPeerID: "peer-1",
		SenderName:   "Peer",
		SentAt:       sentAt,
	}

	entry := transcriptEntryFromEnvelope("swarm-1", env, "hello", true)

	if entry.ID != env.ID {
		t.Fatalf("entry.ID = %q, want %q", entry.ID, env.ID)
	}
	if !entry.SentAt.Equal(sentAt) {
		t.Fatalf("entry.SentAt = %v, want %v", entry.SentAt, sentAt)
	}
	if !entry.Local {
		t.Fatal("entry.Local = false, want true")
	}
}

func TestRefreshNearbyPrunesExpiredPeersAndEmitsSnapshot(t *testing.T) {
	node := &Node{
		ctx:              context.Background(),
		events:           make(chan Event, 1),
		nearby:           map[string]model.NearbyPeer{"peer-1": {PeerID: "peer-1", Name: "Peer", LastSeen: time.Now().Add(-nearbyExpireAfter - time.Second)}},
		nearbyCandidates: make(map[string]nearbyCandidate),
	}

	node.refreshNearby()

	if got := len(node.nearby); got != 0 {
		t.Fatalf("len(node.nearby) = %d, want 0", got)
	}

	select {
	case event := <-node.events:
		if event.Kind != EventNearbySnapshot {
			t.Fatalf("event.Kind = %q, want %q", event.Kind, EventNearbySnapshot)
		}
		if len(event.NearbyPeers) != 0 {
			t.Fatalf("len(event.NearbyPeers) = %d, want 0", len(event.NearbyPeers))
		}
	default:
		t.Fatal("expected nearby snapshot event")
	}
}
