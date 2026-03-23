package p2p

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	corecrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

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
		ID:         "msg-1",
		Kind:       "chat",
		SenderName: "Peer",
		SentAt:     sentAt,
	}

	entry := transcriptEntryFromEnvelope("swarm-1", "peer-1", env, "hello", " msg-0 ", true)

	if entry.ID != env.ID {
		t.Fatalf("entry.ID = %q, want %q", entry.ID, env.ID)
	}
	if !entry.SentAt.Equal(sentAt) {
		t.Fatalf("entry.SentAt = %v, want %v", entry.SentAt, sentAt)
	}
	if !entry.Local {
		t.Fatal("entry.Local = false, want true")
	}
	if got, want := entry.ReplyTo, "msg-0"; got != want {
		t.Fatalf("entry.ReplyTo = %q, want %q", got, want)
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

func TestVerifiedClaimedPeerRejectsMismatchedPeerID(t *testing.T) {
	t.Parallel()

	_, publicKey, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	remotePeer, err := peer.IDFromPublicKey(publicKey)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}

	_, otherPublicKey, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	otherPeer, err := peer.IDFromPublicKey(otherPublicKey)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}

	_, err = verifiedClaimedPeer(remotePeer, publicKey, nil, wirePeer{PeerID: otherPeer.String()})
	if err == nil {
		t.Fatal("verifiedClaimedPeer() unexpectedly accepted mismatched peer id")
	}
}

func TestVerifiedClaimedPeerUsesAuthenticatedIdentity(t *testing.T) {
	t.Parallel()

	_, publicKey, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	remotePeer, err := peer.IDFromPublicKey(publicKey)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}

	peerInfo, err := verifiedClaimedPeer(remotePeer, publicKey, []string{
		"/ip4/127.0.0.1/tcp/4001",
		"not-a-multiaddr",
	}, wirePeer{
		Name:  "  Peer   Name  ",
		Addrs: []string{"/ip4/127.0.0.1/tcp/4001"},
	})
	if err != nil {
		t.Fatalf("verifiedClaimedPeer() error = %v", err)
	}
	if got, want := peerInfo.PeerID, remotePeer.String(); got != want {
		t.Fatalf("peerInfo.PeerID = %q, want %q", got, want)
	}
	if got, want := peerInfo.Name, "Peer Name"; got != want {
		t.Fatalf("peerInfo.Name = %q, want %q", got, want)
	}
	if peerInfo.Fingerprint == "" {
		t.Fatal("peerInfo.Fingerprint unexpectedly empty")
	}
	if got, want := len(peerInfo.Addrs), 1; got != want {
		t.Fatalf("len(peerInfo.Addrs) = %d, want %d", got, want)
	}
}

func TestClampEventTimeRejectsLargeSkew(t *testing.T) {
	t.Parallel()

	clamped := clampEventTime(time.Now().Add(2 * maxClockSkew))
	if time.Since(clamped) > time.Second {
		t.Fatalf("clamped time too far from now: %v", clamped)
	}
}

func TestValidatedSwarmRejectsInvalidRoomKey(t *testing.T) {
	t.Parallel()

	_, err := validatedSwarm(wireSwarm{
		ID:      "swarm-1",
		Name:    "Alpha",
		RoomKey: "not-base64",
	})
	if err == nil {
		t.Fatal("validatedSwarm() unexpectedly accepted invalid room key")
	}
}

func TestSanitizeDisplayNameCollapsesWhitespace(t *testing.T) {
	t.Parallel()

	if got, want := sanitizeDisplayName("  alpha \n beta\tgamma  "), "alpha beta gamma"; got != want {
		t.Fatalf("sanitizeDisplayName() = %q, want %q", got, want)
	}
}

func TestSanitizePeerAddrsDropsInvalidValues(t *testing.T) {
	t.Parallel()

	got := sanitizePeerAddrs([]string{
		"/ip4/127.0.0.1/tcp/4001",
		"bad-addr",
		"/ip4/127.0.0.1/tcp/4001",
	})
	if len(got) != 1 {
		t.Fatalf("len(sanitizePeerAddrs()) = %d, want 1", len(got))
	}
}

func TestSanitizeChatBodyRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	_, ok := sanitizeChatBody(strings.Repeat("a", maxChatBodyBytes+1))
	if ok {
		t.Fatal("sanitizeChatBody() unexpectedly accepted oversized body")
	}
}

func TestMarkSeenEvictsOldestIDs(t *testing.T) {
	t.Parallel()

	node := &Node{
		seen:      make(map[string]struct{}),
		seenOrder: make([]string, 0, maxSeenIDs),
	}
	for i := 0; i < maxSeenIDs+1; i++ {
		if node.markSeen(mustID()) {
			t.Fatal("markSeen() unexpectedly reported duplicate")
		}
	}
	if got, want := len(node.seen), maxSeenIDs; got != want {
		t.Fatalf("len(node.seen) = %d, want %d", got, want)
	}
}
