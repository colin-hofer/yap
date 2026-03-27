package p2p

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	corecrypto "github.com/libp2p/go-libp2p/core/crypto"
	corehost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	yapcrypto "yap/internal/crypto"
	"yap/internal/model"
)

type blockingHost struct {
	corehost.Host
	addrsStarted chan struct{}
	releaseAddrs chan struct{}
	once         sync.Once
}

func (h *blockingHost) Addrs() []ma.Multiaddr {
	h.once.Do(func() { close(h.addrsStarted) })
	<-h.releaseAddrs
	return h.Host.Addrs()
}

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

func TestTopicNameDerivesFromRoomKey(t *testing.T) {
	t.Parallel()

	roomKey, err := yapcrypto.NewRoomKey()
	if err != nil {
		t.Fatalf("NewRoomKey() error = %v", err)
	}
	otherRoomKey, err := yapcrypto.NewRoomKey()
	if err != nil {
		t.Fatalf("NewRoomKey() error = %v", err)
	}

	first := topicName(roomKey)
	if got := topicName(roomKey); got != first {
		t.Fatalf("topicName(roomKey) = %q, want stable %q", got, first)
	}
	if first == topicName(otherRoomKey) {
		t.Fatal("topicName() produced the same topic for different room keys")
	}
	if strings.Contains(first, roomKey) {
		t.Fatalf("topicName(%q) leaked room key in %q", roomKey, first)
	}
}

func TestNormalizeSwarmMetadataInfersOwnerAndVersion(t *testing.T) {
	t.Parallel()

	swarm := normalizeSwarmMetadata(model.Swarm{
		ID:      "swarm-1",
		Name:    "Alpha",
		RoomKey: "room-key",
		TrustedPeers: []model.TrustedPeer{
			{PeerID: "peer-b", Name: "B"},
			{PeerID: "peer-a", Name: "A"},
		},
	})

	if got, want := swarm.OwnerPeerID, "peer-b"; got != want {
		t.Fatalf("OwnerPeerID = %q, want %q", got, want)
	}
	if got, want := swarm.Version, uint64(1); got != want {
		t.Fatalf("Version = %d, want %d", got, want)
	}
	if got, want := swarm.TrustedPeers[0].PeerID, "peer-b"; got != want {
		t.Fatalf("TrustedPeers[0].PeerID = %q, want %q", got, want)
	}
}

func TestSameSwarmSessionIgnoresTrustedPeerOrder(t *testing.T) {
	t.Parallel()

	left := model.Swarm{
		ID:          "swarm-1",
		Name:        "Alpha",
		RoomKey:     "room-key",
		OwnerPeerID: "peer-a",
		Version:     3,
		TrustedPeers: []model.TrustedPeer{
			{PeerID: "peer-a", Name: "A"},
			{PeerID: "peer-b", Name: "B"},
		},
	}
	right := model.Swarm{
		ID:          "swarm-1",
		Name:        "Alpha",
		RoomKey:     "room-key",
		OwnerPeerID: "peer-a",
		Version:     3,
		TrustedPeers: []model.TrustedPeer{
			{PeerID: "peer-b", Name: "B"},
			{PeerID: "peer-a", Name: "A"},
		},
	}

	if !sameSwarmSession(left, right) {
		t.Fatal("sameSwarmSession() = false, want true for identical membership in different order")
	}
}

func TestTranscriptEntryFromEnvelopeUsesEnvelopeMetadata(t *testing.T) {
	sentAt := time.Unix(42, 0)
	env := envelope{
		ID:         "msg-1",
		Kind:       "chat",
		SenderName: "Peer",
		SentAt:     sentAt,
		Signature:  "sig-1",
	}

	entry := transcriptEntryFromEnvelope("swarm-1", "peer-1", env, "hello", true)

	if entry.ID != env.ID {
		t.Fatalf("entry.ID = %q, want %q", entry.ID, env.ID)
	}
	if !entry.SentAt.Equal(sentAt) {
		t.Fatalf("entry.SentAt = %v, want %v", entry.SentAt, sentAt)
	}
	if !entry.Local {
		t.Fatal("entry.Local = false, want true")
	}
	if got, want := entry.Signature, "sig-1"; got != want {
		t.Fatalf("entry.Signature = %q, want %q", got, want)
	}
}

func TestSyncTrustedPeerIdentityUpdatesActiveSwarmName(t *testing.T) {
	author := peer.ID("peer-1")
	node := &Node{}
	active := &activeSwarm{
		Swarm: model.Swarm{
			ID:   "swarm-1",
			Name: "Alpha",
			TrustedPeers: []model.TrustedPeer{
				{PeerID: author.String(), Name: "Old Name"},
			},
		},
	}

	previous, current := node.syncTrustedPeerIdentity(active, author, "New Name", "fp-1", time.Unix(20, 0))

	if got, want := previous, "Old Name"; got != want {
		t.Fatalf("previous = %q, want %q", got, want)
	}
	if got, want := current, "New Name"; got != want {
		t.Fatalf("current = %q, want %q", got, want)
	}
	if got, want := active.Swarm.TrustedPeers[0].Name, "New Name"; got != want {
		t.Fatalf("active.Swarm.TrustedPeers[0].Name = %q, want %q", got, want)
	}
	if got, want := active.Swarm.TrustedPeers[0].Fingerprint, "fp-1"; got != want {
		t.Fatalf("active.Swarm.TrustedPeers[0].Fingerprint = %q, want %q", got, want)
	}
	if got, want := active.Swarm.TrustedPeers[0].LastSeen, time.Unix(20, 0); !got.Equal(want) {
		t.Fatalf("active.Swarm.TrustedPeers[0].LastSeen = %v, want %v", got, want)
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

func TestEmitPresenceSnapshotDoesNotDeadlockWhenWriterQueues(t *testing.T) {
	baseHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("libp2p.New() error = %v", err)
	}
	defer baseHost.Close()

	host := &blockingHost{
		Host:         baseHost,
		addrsStarted: make(chan struct{}),
		releaseAddrs: make(chan struct{}),
	}
	node := &Node{
		ctx:        context.Background(),
		host:       host,
		events:     make(chan Event, 1),
		relayAddrs: []string{"/dns4/relay.example.com/tcp/4001/p2p-circuit"},
	}
	active := &activeSwarm{
		Swarm: model.Swarm{ID: "swarm-1"},
		Presence: map[string]*presenceRecord{
			baseHost.ID().String(): {
				PeerID:   baseHost.ID().String(),
				Name:     "self",
				State:    "online",
				LastSeen: time.Now(),
			},
		},
	}

	done := make(chan struct{})
	go func() {
		node.emitPresenceSnapshot(active)
		close(done)
	}()

	select {
	case <-host.addrsStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("emitPresenceSnapshot() never reached host.Addrs()")
	}

	writerDone := make(chan struct{})
	go func() {
		node.mu.Lock()
		node.mu.Unlock()
		close(writerDone)
	}()

	time.Sleep(25 * time.Millisecond)
	close(host.releaseAddrs)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emitPresenceSnapshot() deadlocked with queued writer")
	}

	select {
	case event := <-node.events:
		if event.Kind != EventPresence {
			t.Fatalf("event.Kind = %q, want %q", event.Kind, EventPresence)
		}
		if got, want := len(event.Presence), 1; got != want {
			t.Fatalf("len(event.Presence) = %d, want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected presence event")
	}

	select {
	case <-writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("writer remained blocked after snapshot")
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

func TestSwarmHasTrustedPeer(t *testing.T) {
	t.Parallel()

	_, publicKeyA, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	peerA, err := peer.IDFromPublicKey(publicKeyA)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}
	_, publicKeyB, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	peerB, err := peer.IDFromPublicKey(publicKeyB)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}
	_, publicKeyC, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	peerC, err := peer.IDFromPublicKey(publicKeyC)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}

	swarm := model.Swarm{
		ID:   "swarm-1",
		Name: "Alpha",
		TrustedPeers: []model.TrustedPeer{
			{PeerID: peerA.String()},
			{PeerID: peerB.String()},
		},
	}

	if !swarmHasTrustedPeer(swarm, peerA) {
		t.Fatal("swarmHasTrustedPeer() = false, want true for trusted peer")
	}
	if swarmHasTrustedPeer(swarm, peerC) {
		t.Fatal("swarmHasTrustedPeer() = true, want false for untrusted peer")
	}
}

func TestResolvedPeerNamePrefersTrustedNameOverClaimed(t *testing.T) {
	t.Parallel()

	_, publicKey, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	author, err := peer.IDFromPublicKey(publicKey)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}

	swarm := model.Swarm{
		ID:   "swarm-1",
		Name: "Alpha",
		TrustedPeers: []model.TrustedPeer{
			{PeerID: author.String(), Name: "Trusted Name"},
		},
	}

	if got, want := resolvedPeerName(swarm, author, "Claimed Name"), "Trusted Name"; got != want {
		t.Fatalf("resolvedPeerName() = %q, want %q", got, want)
	}
}

func TestVerifiedTranscriptEntriesRejectsForgedSender(t *testing.T) {
	t.Parallel()

	signer, publicKey, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	authorPeer, err := peer.IDFromPublicKey(publicKey)
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

	signingNode := &Node{signer: signer}
	entry := model.TranscriptEntry{
		ID:           "msg-1",
		SwarmID:      "swarm-1",
		Kind:         "chat",
		SenderPeerID: authorPeer.String(),
		SenderName:   "Author",
		Body:         "hello",
		SentAt:       time.Unix(20, 0),
	}
	signature, err := signingNode.signTranscriptEntry(entry)
	if err != nil {
		t.Fatalf("signTranscriptEntry() error = %v", err)
	}

	verifyNode := &Node{}
	swarm := model.Swarm{
		ID:   "swarm-1",
		Name: "Alpha",
		TrustedPeers: []model.TrustedPeer{
			{PeerID: authorPeer.String(), Name: "Author"},
			{PeerID: otherPeer.String(), Name: "Other"},
		},
	}
	entries := verifyNode.verifiedTranscriptEntries(swarm, []wireTranscriptEntry{{
		ID:           entry.ID,
		SwarmID:      entry.SwarmID,
		Kind:         entry.Kind,
		SenderPeerID: otherPeer.String(),
		SenderName:   entry.SenderName,
		Body:         entry.Body,
		SentAt:       entry.SentAt,
		Signature:    signature,
	}}, "self")

	if got := len(entries); got != 0 {
		t.Fatalf("len(entries) = %d, want 0", got)
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
