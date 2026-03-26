package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	corecrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	relayclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	ma "github.com/multiformats/go-multiaddr"

	yapcrypto "yap/internal/crypto"
	"yap/internal/model"
	"yap/internal/store"
)

const (
	mdnsServiceName      = "yap-v2"
	cardProtocolID       = protocol.ID("/yap/card/2")
	pairProtocolID       = protocol.ID("/yap/pair/2")
	historyProtocolID    = protocol.ID("/yap/history/2")
	swarmProtocolID      = protocol.ID("/yap/swarm/1")
	heartbeatEvery       = 15 * time.Second
	staleAfter           = 45 * time.Second
	offlineAfter         = 90 * time.Second
	reconnectEvery       = 20 * time.Second
	historyCooldown      = 1 * time.Minute
	swarmSyncCooldown    = 30 * time.Second
	inviteTTL            = 5 * time.Minute
	defaultInviteUse     = 5
	nearbyRefreshEvery   = 10 * time.Second
	nearbyRetryEvery     = 10 * time.Second
	nearbyExpireAfter    = 30 * time.Second
	nearbyCandidateTTL   = 45 * time.Second
	cardStreamTimeout    = 5 * time.Second
	pairStreamTimeout    = 2 * time.Minute
	historyStreamTimeout = 15 * time.Second
	typingEvery          = 3 * time.Second
	typingExpireAfter    = 6 * time.Second
	relayRetryEvery      = 15 * time.Second
	relayRefreshLead     = 1 * time.Minute
	relayMinRefresh      = 30 * time.Second
	maxDisplayNameBytes  = 64
	maxChatBodyBytes     = 4096
	maxPeerAddrs         = 16
	maxSeenIDs           = 4096
	maxClockSkew         = 5 * time.Minute
)

// EventKind is a UI-facing classification for node events.
type EventKind string

const (
	EventNearby         EventKind = "nearby"
	EventNearbySnapshot EventKind = "nearby_snapshot"
	EventSystem         EventKind = "system"
	EventApproval       EventKind = "approval"
	EventPairComplete   EventKind = "pair_complete"
	EventTranscript     EventKind = "transcript"
	EventHistory        EventKind = "history"
	EventPresence       EventKind = "presence"
	EventSwarmUpdate    EventKind = "swarm_update"
	EventSwarmRevoked   EventKind = "swarm_revoked"
)

// Event describes a state change from the node.
type Event struct {
	Kind        EventKind
	SwarmID     string
	Message     string
	Nearby      *model.NearbyPeer
	NearbyPeers []model.NearbyPeer
	Approval    *Approval
	Pair        *PairResult
	SwarmUpdate *SwarmUpdate
	Revocation  *SwarmRevocation
	Entry       *model.TranscriptEntry
	Entries     []model.TranscriptEntry
	Presence    []model.Presence
	AutoOpen    bool
}

// Approval is emitted when the UI must confirm a pairing step.
type Approval struct {
	ID        string
	Direction string
	Peer      model.TrustedPeer
	SwarmName string
}

// PairResult is emitted after a successful pairing flow.
type PairResult struct {
	Direction string
	Swarm     model.Swarm
	Peer      model.TrustedPeer
}

// SwarmUpdate is emitted when the owner publishes a new roster or room key.
type SwarmUpdate struct {
	Swarm  model.Swarm
	Reason string
	Actor  model.TrustedPeer
	Target *model.TrustedPeer
}

// SwarmRevocation is emitted to the removed peer so they can close the old
// swarm locally and understand why it stopped working.
type SwarmRevocation struct {
	SwarmID   string
	SwarmName string
	Version   uint64
	Actor     model.TrustedPeer
}

// Node hosts discovery, pairing, pubsub, and room presence.
type Node struct {
	ctx      context.Context
	cancel   context.CancelFunc
	host     host.Host
	pubsub   *pubsub.PubSub
	signer   corecrypto.PrivKey
	identity model.Identity
	store    *store.Store

	events chan Event

	mu               sync.RWMutex
	nearby           map[string]model.NearbyPeer
	nearbyCandidates map[string]nearbyCandidate
	invites          map[string]*activeInvite
	pending          map[string]chan bool
	active           map[string]*activeSwarm
	closed           bool
	seen             map[string]struct{}
	seenOrder        []string
	historySync      map[string]time.Time
	swarmSync        map[string]swarmSyncRecord
	typingSent       map[string]time.Time
	typingState      map[string]bool
	relay            *configuredRelay
	relayAddrs       []string
}

type nearbyCandidate struct {
	Info        peer.AddrInfo
	LastSeen    time.Time
	LastAttempt time.Time
}

type activeInvite struct {
	Invite model.Invite
	Swarm  model.Swarm
}

type activeSwarm struct {
	Swarm    model.Swarm
	Topic    *pubsub.Topic
	Sub      *pubsub.Subscription
	Context  context.Context
	Cancel   context.CancelFunc
	Presence map[string]*presenceRecord
}

type configuredRelay struct {
	Info peer.AddrInfo
}

type swarmSyncRecord struct {
	Version uint64
	SentAt  time.Time
}

type presenceRecord struct {
	PeerID      string
	Name        string
	Fingerprint string
	State       string
	LastSeen    time.Time
	TypingUntil time.Time
	ClearTyping bool
}

type wirePeer struct {
	PeerID      string   `json:"peer_id"`
	Name        string   `json:"name"`
	Fingerprint string   `json:"fingerprint"`
	Addrs       []string `json:"addrs,omitempty"`
}

type pairRequest struct {
	Code      string   `json:"code"`
	Requester wirePeer `json:"requester"`
}

type pairOffer struct {
	Stage     string   `json:"stage"`
	Message   string   `json:"message,omitempty"`
	Responder wirePeer `json:"responder,omitempty"`
	SwarmName string   `json:"swarm_name,omitempty"`
}

type pairDecision struct {
	Accept bool `json:"accept"`
}

type pairSuccess struct {
	Stage string    `json:"stage"`
	Swarm wireSwarm `json:"swarm"`
	Peer  wirePeer  `json:"peer"`
}

type wireSwarm struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	RoomKey      string     `json:"room_key"`
	OwnerPeerID  string     `json:"owner_peer_id,omitempty"`
	Version      uint64     `json:"version,omitempty"`
	TrustedPeers []wirePeer `json:"trusted_peers,omitempty"`
}

type swarmUpdateFrame struct {
	Swarm  wireSwarm `json:"swarm"`
	Reason string    `json:"reason,omitempty"`
	Target *wirePeer `json:"target,omitempty"`
}

type envelope struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	SenderName string    `json:"sender_name"`
	SentAt     time.Time `json:"sent_at"`
	Signature  string    `json:"signature,omitempty"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
}

type envelopeBody struct {
	Body   string `json:"body,omitempty"`
	Typing *bool  `json:"typing,omitempty"`
}

// New creates a node with a persisted identity.
func New(parent context.Context, identity model.Identity, st *store.Store) (*Node, error) {
	ctx, cancel := context.WithCancel(parent)

	relayCfg, err := configuredRelayFromEnv()
	if err != nil {
		cancel()
		return nil, err
	}

	privateKeyBytes, err := corecrypto.ConfigDecodeKey(identity.PrivateKey)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	privateKey, err := corecrypto.UnmarshalPrivateKey(privateKeyBytes)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("unmarshal private key: %w", err)
	}

	h, err := libp2p.New(
		libp2p.Identity(privateKey),
		libp2p.ListenAddrStrings(listenAddrs()...),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create host: %w", err)
	}

	ps, err := pubsub.NewGossipSub(
		ctx,
		h,
		pubsub.WithMessageSignaturePolicy(pubsub.StrictSign),
		pubsub.WithStrictSignatureVerification(true),
		pubsub.WithMaxMessageSize(128<<10),
	)
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("create gossipsub: %w", err)
	}

	node := &Node{
		ctx:              ctx,
		cancel:           cancel,
		host:             h,
		pubsub:           ps,
		signer:           privateKey,
		identity:         identity,
		store:            st,
		events:           make(chan Event, 256),
		nearby:           make(map[string]model.NearbyPeer),
		nearbyCandidates: make(map[string]nearbyCandidate),
		invites:          make(map[string]*activeInvite),
		pending:          make(map[string]chan bool),
		active:           make(map[string]*activeSwarm),
		seen:             make(map[string]struct{}),
		seenOrder:        make([]string, 0, maxSeenIDs),
		historySync:      make(map[string]time.Time),
		swarmSync:        make(map[string]swarmSyncRecord),
		typingSent:       make(map[string]time.Time),
		typingState:      make(map[string]bool),
		relay:            relayCfg,
	}

	h.SetStreamHandler(cardProtocolID, node.handleCardStream)
	h.SetStreamHandler(pairProtocolID, node.handlePairStream)
	h.SetStreamHandler(historyProtocolID, node.handleHistoryStream)
	h.SetStreamHandler(swarmProtocolID, node.handleSwarmStream)

	service := mdns.NewMdnsService(h, mdnsServiceName, &discoveryNotifee{node: node})
	if err := service.Start(); err != nil {
		node.Close()
		return nil, fmt.Errorf("start mdns: %w", err)
	}

	go node.inviteJanitor()
	go node.nearbyLoop()
	if node.relay != nil {
		go node.relayLoop()
	}

	return node, nil
}

// Events returns the node event stream.
func (n *Node) Events() <-chan Event {
	return n.events
}

// Self returns the local peer identity card.
func (n *Node) Self() model.TrustedPeer {
	return n.selfPeer()
}

// NearbySnapshot returns all currently known nearby peers.
func (n *Node) NearbySnapshot() []model.NearbyPeer {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]model.NearbyPeer, 0, len(n.nearby))
	for _, peerInfo := range n.nearby {
		out = append(out, peerInfo)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// CreateInvite registers a short-lived invite for a swarm.
func (n *Node) CreateInvite(swarm model.Swarm) (model.Invite, error) {
	swarm = normalizeSwarmMetadata(swarm)
	if !swarmOwnedBy(swarm, n.host.ID().String()) {
		return model.Invite{}, fmt.Errorf("only the room owner can invite new members")
	}
	code, err := yapcrypto.NewInviteCode()
	if err != nil {
		return model.Invite{}, err
	}
	invite := model.Invite{
		Code:       code,
		ShareToken: yapcrypto.FormatInviteToken(n.host.ID().String(), code),
		SwarmID:    swarm.ID,
		SwarmName:  swarm.Name,
		ExpiresAt:  time.Now().Add(inviteTTL),
		MaxUses:    defaultInviteUse,
		CurrentUse: 0,
	}
	n.mu.Lock()
	n.invites[invite.Code] = &activeInvite{Invite: invite, Swarm: swarm}
	n.mu.Unlock()
	return invite, nil
}

// ResolveApproval completes a pending pairing decision.
func (n *Node) ResolveApproval(id string, accept bool) error {
	n.mu.Lock()
	ch, ok := n.pending[id]
	if ok {
		delete(n.pending, id)
	}
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown approval %q", id)
	}
	ch <- accept
	close(ch)
	return nil
}

func (n *Node) registerPendingDecision() (string, chan bool, func()) {
	id := mustID()
	ch := make(chan bool, 1)
	n.mu.Lock()
	n.pending[id] = ch
	n.mu.Unlock()
	return id, ch, func() {
		n.mu.Lock()
		if current, ok := n.pending[id]; ok && current == ch {
			delete(n.pending, id)
		}
		n.mu.Unlock()
	}
}

// PairWithPeer starts a pairing flow with a discovered peer.
func (n *Node) PairWithPeer(peerID, code string, autoOpen bool) error {
	code = yapcrypto.NormalizeInviteCode(code)
	if code == "" {
		return fmt.Errorf("invite code cannot be empty")
	}
	id, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("decode peer id: %w", err)
	}
	go n.runOutgoingPair(id, code, autoOpen)
	return nil
}

// PairWithInviteToken starts a pairing flow using a shareable invite token.
func (n *Node) PairWithInviteToken(token string, autoOpen bool) error {
	parsed, err := yapcrypto.ParseInviteToken(token)
	if err != nil {
		return err
	}
	if strings.TrimSpace(parsed.PeerID) == "" {
		return fmt.Errorf("invite token does not include an inviter peer")
	}
	id, err := peer.Decode(parsed.PeerID)
	if err != nil {
		return fmt.Errorf("decode inviter peer id: %w", err)
	}
	if !n.prepareRelayPeer(id) {
		return fmt.Errorf("YAP_RELAY_ADDR is not configured")
	}
	go n.runOutgoingPair(id, parsed.Code, autoOpen)
	return nil
}

// OpenSwarm joins the room topic and begins presence/publication loops.
func (n *Node) OpenSwarm(swarm model.Swarm) error {
	swarm = normalizeSwarmMetadata(swarm)
	if active := n.currentSwarm(swarm.ID); active != nil {
		if sameSwarmSession(active.Swarm, swarm) {
			return nil
		}
		if err := n.CloseSwarm(swarm.ID); err != nil {
			return err
		}
	}
	topicName := topicName(swarm.RoomKey)
	topic, err := n.pubsub.Join(topicName)
	if err != nil {
		return fmt.Errorf("join topic %s: %w", topicName, err)
	}
	sub, err := topic.Subscribe()
	if err != nil {
		_ = topic.Close()
		return fmt.Errorf("subscribe topic %s: %w", topicName, err)
	}
	ctx, cancel := context.WithCancel(n.ctx)
	active := &activeSwarm{
		Swarm:    swarm,
		Topic:    topic,
		Sub:      sub,
		Context:  ctx,
		Cancel:   cancel,
		Presence: make(map[string]*presenceRecord),
	}
	self := n.selfPeer()
	active.Presence[self.PeerID] = &presenceRecord{
		PeerID:      self.PeerID,
		Name:        self.Name,
		Fingerprint: self.Fingerprint,
		State:       "online",
		LastSeen:    time.Now(),
	}
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == self.PeerID {
			continue
		}
		active.Presence[trusted.PeerID] = &presenceRecord{
			PeerID:      trusted.PeerID,
			Name:        trusted.Name,
			Fingerprint: trusted.Fingerprint,
			State:       "offline",
			LastSeen:    trusted.LastSeen,
		}
		n.addTrustedPeerAddrs(trusted)
	}
	n.mu.Lock()
	n.active[swarm.ID] = active
	n.mu.Unlock()

	n.emitPresenceSnapshot(active)
	n.connectTrustedPeers(active.Swarm)
	go n.subscriptionLoop(active)
	go n.heartbeatLoop(active)
	go n.reconnectLoop(active)
	go n.presenceLoop(active)
	go n.syncSwarm(active)
	if err := n.publishKind(active, "join", ""); err != nil {
		n.emitSystem(fmt.Sprintf("failed to publish join: %v", err))
	}
	return nil
}

// CloseSwarm leaves the given room if it is active.
func (n *Node) CloseSwarm(swarmID string) error {
	n.mu.Lock()
	active := n.active[swarmID]
	delete(n.active, swarmID)
	n.mu.Unlock()
	if active == nil {
		return nil
	}
	_ = n.publishKind(active, "leave", "")
	_ = n.publishTypingState(active, false)
	active.Cancel()
	active.Sub.Cancel()
	if err := active.Topic.Close(); err != nil {
		return fmt.Errorf("close topic: %w", err)
	}
	return nil
}

// CloseAllSwarms leaves all active rooms.
func (n *Node) CloseAllSwarms() error {
	n.mu.Lock()
	active := make([]*activeSwarm, 0, len(n.active))
	for swarmID, session := range n.active {
		active = append(active, session)
		delete(n.active, swarmID)
	}
	n.mu.Unlock()

	var closeErr error
	for _, session := range active {
		_ = n.publishKind(session, "leave", "")
		_ = n.publishTypingState(session, false)
		session.Cancel()
		session.Sub.Cancel()
		if err := session.Topic.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("close topic: %w", err)
		}
	}
	return closeErr
}

// PublishChat encrypts and publishes a chat message to the given swarm.
func (n *Node) PublishChat(swarmID, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	active := n.currentSwarm(swarmID)
	if active == nil {
		return fmt.Errorf("swarm %q is not connected", swarmID)
	}
	messageID := mustID()
	sentAt := time.Now()
	env, err := n.newEnvelope(active, "chat", envelopeBody{
		Body: body,
	}, messageID, sentAt)
	if err != nil {
		return err
	}
	if err := n.publishEnvelope(active, env); err != nil {
		return err
	}
	entry := transcriptEntryFromEnvelope(active.Swarm.ID, n.host.ID().String(), env, body, true)
	n.emit(Event{Kind: EventTranscript, Entry: &entry})
	n.touchPresence(active, selfPresence(n.selfPeer()))
	_ = n.PublishTyping(swarmID, false)
	return nil
}

// PublishTyping broadcasts a throttled ephemeral typing state for the given swarm.
func (n *Node) PublishTyping(swarmID string, active bool) error {
	if strings.TrimSpace(swarmID) == "" {
		return nil
	}
	session := n.currentSwarm(swarmID)
	if session == nil {
		return nil
	}
	return n.publishTypingState(session, active)
}

func (n *Node) publishTypingState(session *activeSwarm, active bool) error {
	if session == nil {
		return nil
	}
	swarmID := session.Swarm.ID
	now := time.Now()
	n.mu.Lock()
	last := n.typingSent[swarmID]
	lastState := n.typingState[swarmID]
	if active == lastState {
		if !active || now.Sub(last) < typingEvery {
			n.mu.Unlock()
			return nil
		}
	}
	n.typingSent[swarmID] = now
	n.typingState[swarmID] = active
	n.mu.Unlock()

	env, err := n.newEnvelope(session, "typing", envelopeBody{
		Typing: boolPtr(active),
	}, "", now)
	if err != nil {
		return err
	}
	if err := n.publishEnvelope(session, env); err != nil {
		return err
	}

	update := presenceRecord{
		PeerID:      n.host.ID().String(),
		Name:        sanitizeDisplayName(n.identity.Name),
		Fingerprint: n.identity.Fingerprint,
		State:       "online",
		LastSeen:    now,
	}
	if active {
		update.TypingUntil = now.Add(typingExpireAfter)
	} else {
		update.ClearTyping = true
	}
	n.touchPresence(session, update)
	return nil
}

// Close shuts down the node and underlying host.
func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	n.mu.Unlock()
	_ = n.CloseAllSwarms()
	n.cancel()
	return n.host.Close()
}

// UpdateIdentityName updates the local display name used for cards, presence,
// and future chat messages.
func (n *Node) UpdateIdentityName(name string) {
	name = sanitizeDisplayName(name)
	if name == "" {
		return
	}

	selfAddrs := uniqueStrings(append(
		sanitizePeerAddrs(multiaddrStrings(n.host.Addrs())),
		n.currentRelayAddrs()...,
	))

	n.mu.Lock()
	oldName := sanitizeDisplayName(n.identity.Name)
	if name == oldName {
		n.mu.Unlock()
		return
	}
	n.identity.Name = name
	active := make([]*activeSwarm, 0, len(n.active))
	self := model.TrustedPeer{
		PeerID:      n.host.ID().String(),
		Name:        name,
		Fingerprint: n.identity.Fingerprint,
		Addrs:       sanitizePeerAddrs(selfAddrs),
		LastSeen:    time.Now(),
	}
	for _, session := range n.active {
		session.Swarm.TrustedPeers = mergeTrustedPeer(session.Swarm.TrustedPeers, self)
		if record, ok := session.Presence[self.PeerID]; ok {
			record.Name = name
			record.Fingerprint = self.Fingerprint
			record.LastSeen = self.LastSeen
			record.State = "online"
		} else {
			session.Presence[self.PeerID] = &presenceRecord{
				PeerID:      self.PeerID,
				Name:        name,
				Fingerprint: self.Fingerprint,
				LastSeen:    self.LastSeen,
				State:       "online",
			}
		}
		active = append(active, session)
	}
	n.mu.Unlock()

	for _, session := range active {
		env, err := n.newEnvelope(session, "rename", envelopeBody{Body: oldName}, "", time.Time{})
		if err != nil {
			n.emitSystem(fmt.Sprintf("failed to publish rename: %v", err))
		} else if err := n.publishEnvelope(session, env); err != nil {
			n.emitSystem(fmt.Sprintf("failed to publish rename: %v", err))
		} else {
			entry := transcriptEntryFromEnvelope(session.Swarm.ID, n.host.ID().String(), env, oldName, true)
			n.emit(Event{Kind: EventTranscript, Entry: &entry})
		}
		n.emitPresenceSnapshot(session)
		_ = n.publishKind(session, "heartbeat", "")
	}
}

func (n *Node) noteNearbyCandidate(info peer.AddrInfo) {
	if info.ID == n.host.ID() {
		return
	}

	now := time.Now()
	var inspect peer.AddrInfo
	shouldInspect := false

	n.mu.Lock()
	candidate := n.nearbyCandidates[info.ID.String()]
	candidate.Info = mergeAddrInfo(candidate.Info, info)
	candidate.LastSeen = now
	if candidate.LastAttempt.IsZero() || now.Sub(candidate.LastAttempt) >= nearbyRetryEvery {
		candidate.LastAttempt = now
		inspect = candidate.Info
		shouldInspect = true
	}
	n.nearbyCandidates[info.ID.String()] = candidate
	n.mu.Unlock()

	if shouldInspect {
		go n.inspectNearby(inspect)
	}
}

func (n *Node) inspectNearby(info peer.AddrInfo) {
	ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
	defer cancel()

	n.host.Peerstore().AddAddrs(info.ID, info.Addrs, 5*time.Minute)
	if err := n.host.Connect(ctx, info); err != nil {
		return
	}

	card, err := n.fetchPeerCard(ctx, info.ID)
	if err != nil {
		return
	}
	peerInfo := model.NearbyPeer{
		PeerID:      card.PeerID,
		Name:        card.Name,
		Fingerprint: card.Fingerprint,
		Addrs:       card.Addrs,
		LastSeen:    time.Now(),
	}
	n.mu.Lock()
	n.nearby[peerInfo.PeerID] = peerInfo
	candidate := n.nearbyCandidates[peerInfo.PeerID]
	candidate.Info = mergeAddrInfo(candidate.Info, info)
	candidate.LastSeen = peerInfo.LastSeen
	if candidate.LastAttempt.IsZero() {
		candidate.LastAttempt = peerInfo.LastSeen
	}
	n.nearbyCandidates[peerInfo.PeerID] = candidate
	n.mu.Unlock()
	n.emitNearbySnapshot()
}

func (n *Node) nearbyLoop() {
	ticker := time.NewTicker(nearbyRefreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.refreshNearby()
		}
	}
}

func (n *Node) refreshNearby() {
	now := time.Now()
	queue := make([]peer.AddrInfo, 0)
	removed := false

	n.mu.Lock()
	for id, candidate := range n.nearbyCandidates {
		if now.Sub(candidate.LastSeen) > nearbyCandidateTTL {
			delete(n.nearbyCandidates, id)
			continue
		}
		if now.Sub(candidate.LastAttempt) >= nearbyRetryEvery {
			candidate.LastAttempt = now
			n.nearbyCandidates[id] = candidate
			queue = append(queue, candidate.Info)
		}
	}
	for id, peerInfo := range n.nearby {
		if now.Sub(peerInfo.LastSeen) <= nearbyExpireAfter {
			continue
		}
		delete(n.nearby, id)
		removed = true
	}
	n.mu.Unlock()

	if removed {
		n.emitNearbySnapshot()
	}
	for _, info := range queue {
		go n.inspectNearby(info)
	}
}

func (n *Node) fetchPeerCard(ctx context.Context, peerID peer.ID) (wirePeer, error) {
	stream, err := n.host.NewStream(ctx, peerID, cardProtocolID)
	if err != nil {
		return wirePeer{}, fmt.Errorf("open card stream: %w", err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(cardStreamTimeout))

	var card wirePeer
	if err := json.NewDecoder(stream).Decode(&card); err != nil {
		return wirePeer{}, fmt.Errorf("decode card: %w", err)
	}
	verified, err := verifiedTrustedPeer(stream.Conn(), card)
	if err != nil {
		return wirePeer{}, err
	}
	return toWirePeer(verified), nil
}

func (n *Node) handleCardStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(cardStreamTimeout))
	_ = json.NewEncoder(stream).Encode(toWirePeer(n.selfPeer()))
}

// BroadcastSwarmUpdate pushes the current swarm roster and room key directly to
// the remaining trusted peers. Only the swarm owner is allowed to publish
// updates that other peers will accept.
func (n *Node) BroadcastSwarmUpdate(swarm model.Swarm, reason string, target *model.TrustedPeer) {
	swarm = normalizeSwarmMetadata(swarm)
	if !swarmOwnedBy(swarm, n.host.ID().String()) {
		return
	}
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == n.host.ID().String() {
			continue
		}
		trusted := trusted
		var targetCopy *model.TrustedPeer
		if target != nil {
			copy := *target
			targetCopy = &copy
		}
		go n.sendSwarmUpdate(swarm, trusted, normalizeSwarmUpdateReason(reason), targetCopy, false)
	}
	if normalizeSwarmUpdateReason(reason) == "revoke" && target != nil && target.PeerID != n.host.ID().String() && !swarmHasTrustedPeerID(swarm, target.PeerID) {
		targetCopy := *target
		go n.sendRevocationNotice(swarm, targetCopy)
	}
}

func (n *Node) handleSwarmStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(historyStreamTimeout))

	var frame swarmUpdateFrame
	if err := json.NewDecoder(stream).Decode(&frame); err != nil {
		return
	}
	if normalizeSwarmUpdateReason(frame.Reason) == "revoked" {
		n.handleRevocationNotice(stream, frame)
		return
	}

	updated, err := validatedSwarm(frame.Swarm)
	if err != nil {
		return
	}

	current, err := n.store.LoadSwarm(updated.ID)
	if err != nil {
		return
	}
	current = normalizeSwarmMetadata(current)
	updated = normalizeSwarmMetadata(updated)

	ownerPeerID := swarmOwnerPeerID(current)
	if ownerPeerID == "" || stream.Conn().RemotePeer().String() != ownerPeerID {
		return
	}
	if swarmOwnerPeerID(updated) != ownerPeerID {
		return
	}
	if swarmVersion(updated) <= swarmVersion(current) {
		return
	}
	if !swarmHasTrustedPeerID(updated, n.host.ID().String()) {
		return
	}
	if !swarmHasTrustedPeerID(updated, ownerPeerID) {
		return
	}

	actor, ok := trustedPeerByID(updated.TrustedPeers, ownerPeerID)
	if !ok {
		actor, _ = trustedPeerByID(current.TrustedPeers, ownerPeerID)
	}
	actor.Addrs = uniqueStrings(append(actor.Addrs, n.peerAddrs(ownerPeerID)...))
	if actor.LastSeen.IsZero() {
		actor.LastSeen = time.Now()
	}

	var target *model.TrustedPeer
	if frame.Target != nil {
		candidate := trustedPeerFromWire(*frame.Target)
		target = &candidate
	}

	n.emit(Event{
		Kind: EventSwarmUpdate,
		SwarmUpdate: &SwarmUpdate{
			Swarm:  updated,
			Reason: normalizeSwarmUpdateReason(frame.Reason),
			Actor:  actor,
			Target: target,
		},
	})
}

func (n *Node) handlePairStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(pairStreamTimeout))
	decisionCtx, cancel := context.WithTimeout(n.ctx, pairStreamTimeout)
	defer cancel()

	var request pairRequest
	if err := json.NewDecoder(stream).Decode(&request); err != nil {
		_ = json.NewEncoder(stream).Encode(pairOffer{Stage: "rejected", Message: "invalid request"})
		return
	}
	request.Code = yapcrypto.NormalizeInviteCode(request.Code)
	if request.Code == "" {
		_ = json.NewEncoder(stream).Encode(pairOffer{Stage: "rejected", Message: "invite code required"})
		return
	}

	invite, ok := n.lookupInvite(request.Code)
	if !ok {
		_ = json.NewEncoder(stream).Encode(pairOffer{Stage: "rejected", Message: "invite not found or expired"})
		return
	}
	swarm := n.currentInviteSwarm(invite)
	if !swarmOwnedBy(swarm, n.host.ID().String()) {
		_ = json.NewEncoder(stream).Encode(pairOffer{Stage: "rejected", Message: "only the room owner can invite new members"})
		return
	}

	requester, err := verifiedTrustedPeer(stream.Conn(), request.Requester)
	if err != nil {
		_ = json.NewEncoder(stream).Encode(pairOffer{Stage: "rejected", Message: err.Error()})
		return
	}
	requester.LastSeen = time.Now()
	approvalID, decision, cleanup := n.registerPendingDecision()
	defer cleanup()
	n.emit(Event{
		Kind: EventApproval,
		Approval: &Approval{
			ID:        approvalID,
			Direction: "incoming",
			Peer:      requester,
			SwarmName: swarm.Name,
		},
	})

	accepted, ok := waitForDecision(decisionCtx, decision)
	if !ok || !accepted {
		n.bumpInviteUse(invite.Invite.Code, false)
		_ = json.NewEncoder(stream).Encode(pairOffer{Stage: "rejected", Message: "pairing rejected"})
		return
	}

	offer := pairOffer{
		Stage:     "offer",
		Responder: toWirePeer(n.selfPeer()),
		SwarmName: swarm.Name,
	}
	if err := json.NewEncoder(stream).Encode(offer); err != nil {
		n.bumpInviteUse(invite.Invite.Code, false)
		return
	}

	var decisionFrame pairDecision
	if err := json.NewDecoder(stream).Decode(&decisionFrame); err != nil {
		n.bumpInviteUse(invite.Invite.Code, false)
		return
	}
	if !decisionFrame.Accept {
		n.bumpInviteUse(invite.Invite.Code, false)
		return
	}

	alreadyTrusted := swarmHasTrustedPeerID(swarm, requester.PeerID)
	swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, requester)
	if !alreadyTrusted {
		swarm.Version = nextSwarmVersion(swarm)
	}
	swarm = normalizeSwarmMetadata(swarm)

	success := pairSuccess{
		Stage: "success",
		Swarm: toWireSwarm(swarm),
		Peer:  toWirePeer(n.selfPeer()),
	}
	if err := json.NewEncoder(stream).Encode(success); err != nil {
		n.bumpInviteUse(invite.Invite.Code, false)
		return
	}

	n.bumpInviteUse(invite.Invite.Code, true)
	n.emit(Event{
		Kind: EventPairComplete,
		Pair: &PairResult{
			Direction: "incoming",
			Swarm:     swarm,
			Peer:      requester,
		},
	})
}

func (n *Node) runOutgoingPair(peerID peer.ID, code string, autoOpen bool) {
	ctx, cancel := context.WithTimeout(n.ctx, pairStreamTimeout)
	defer cancel()

	stream, err := n.host.NewStream(ctx, peerID, pairProtocolID)
	if err != nil {
		n.emitSystem(fmt.Sprintf("failed to start pairing with %s: %v", peerID, err))
		return
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(pairStreamTimeout))

	request := pairRequest{
		Code:      code,
		Requester: toWirePeer(n.selfPeer()),
	}
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		n.emitSystem(fmt.Sprintf("failed to send pairing request: %v", err))
		return
	}

	var offer pairOffer
	if err := json.NewDecoder(stream).Decode(&offer); err != nil {
		n.emitSystem(fmt.Sprintf("failed to read pairing response: %v", err))
		return
	}
	if offer.Stage == "rejected" {
		n.emitSystem(offer.Message)
		return
	}
	if offer.Stage != "offer" {
		n.emitSystem("unexpected pairing response")
		return
	}

	responder, err := verifiedTrustedPeer(stream.Conn(), offer.Responder)
	if err != nil {
		n.emitSystem(err.Error())
		return
	}
	approvalID, decision, cleanup := n.registerPendingDecision()
	defer cleanup()
	n.emit(Event{
		Kind: EventApproval,
		Approval: &Approval{
			ID:        approvalID,
			Direction: "outgoing",
			Peer:      responder,
			SwarmName: offer.SwarmName,
		},
	})

	accepted, ok := waitForDecision(ctx, decision)
	if !ok || !accepted {
		_ = json.NewEncoder(stream).Encode(pairDecision{Accept: false})
		return
	}
	if err := json.NewEncoder(stream).Encode(pairDecision{Accept: true}); err != nil {
		n.emitSystem(fmt.Sprintf("failed to confirm pairing: %v", err))
		return
	}

	var success pairSuccess
	if err := json.NewDecoder(stream).Decode(&success); err != nil {
		n.emitSystem(fmt.Sprintf("failed to read swarm bundle: %v", err))
		return
	}
	if success.Stage != "success" {
		n.emitSystem("unexpected pairing completion")
		return
	}
	verifiedPeer, err := verifiedTrustedPeer(stream.Conn(), success.Peer)
	if err != nil {
		n.emitSystem(err.Error())
		return
	}
	swarm, err := validatedSwarm(success.Swarm)
	if err != nil {
		n.emitSystem(err.Error())
		return
	}
	if swarm.OwnerPeerID == "" {
		swarm.OwnerPeerID = verifiedPeer.PeerID
	}
	swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, verifiedPeer)
	swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, n.selfPeer())
	swarm = normalizeSwarmMetadata(swarm)
	n.emit(Event{
		Kind: EventPairComplete,
		Pair: &PairResult{
			Direction: "outgoing",
			Swarm:     swarm,
			Peer:      responder,
		},
		AutoOpen: autoOpen,
	})
}

func (n *Node) subscriptionLoop(active *activeSwarm) {
	for {
		msg, err := active.Sub.Next(active.Context)
		if err != nil {
			return
		}
		var env envelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			continue
		}
		if n.markSeen(env.ID) {
			continue
		}
		author := msg.GetFrom()
		if author == "" {
			continue
		}
		if !swarmHasTrustedPeer(active.Swarm, author) {
			continue
		}
		bodyBytes, err := yapcrypto.Decrypt(active.Swarm.RoomKey, env.Nonce, env.Ciphertext)
		if err != nil {
			continue
		}
		var payload envelopeBody
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			continue
		}
		sentAt := clampEventTime(env.SentAt)
		senderPeerID := author.String()
		senderFingerprint := n.peerFingerprint(author)
		if strings.TrimSpace(env.Signature) != "" && !n.verifyEnvelopeSignature(active.Swarm.ID, author, env, payload) {
			continue
		}
		previousName, senderName := n.syncTrustedPeerIdentity(active, author, env.SenderName, senderFingerprint, sentAt)

		switch env.Kind {
		case "chat":
			body, ok := sanitizeChatBody(payload.Body)
			if !ok {
				continue
			}
			entry := model.TranscriptEntry{
				ID:           env.ID,
				SwarmID:      active.Swarm.ID,
				Kind:         "chat",
				SenderPeerID: senderPeerID,
				SenderName:   senderName,
				Body:         body,
				SentAt:       sentAt,
			}
			n.emit(Event{Kind: EventTranscript, Entry: &entry})
			n.touchPresence(active, presenceRecord{
				PeerID:      senderPeerID,
				Name:        senderName,
				Fingerprint: senderFingerprint,
				State:       "online",
				LastSeen:    sentAt,
				ClearTyping: true,
			})
		case "join", "leave":
			entry := model.TranscriptEntry{
				ID:           env.ID,
				SwarmID:      active.Swarm.ID,
				Kind:         env.Kind,
				SenderPeerID: senderPeerID,
				SenderName:   senderName,
				Body:         payload.Body,
				SentAt:       sentAt,
			}
			n.emit(Event{Kind: EventTranscript, Entry: &entry})
			state := "online"
			if env.Kind == "leave" {
				state = "offline"
			}
			n.touchPresence(active, presenceRecord{
				PeerID:      senderPeerID,
				Name:        senderName,
				Fingerprint: senderFingerprint,
				State:       state,
				LastSeen:    sentAt,
				ClearTyping: true,
			})
		case "rename":
			oldName := sanitizeDisplayName(payload.Body)
			if oldName == "" {
				oldName = previousName
			}
			entry := model.TranscriptEntry{
				ID:           env.ID,
				SwarmID:      active.Swarm.ID,
				Kind:         "rename",
				SenderPeerID: senderPeerID,
				SenderName:   senderName,
				Body:         oldName,
				SentAt:       sentAt,
			}
			n.emit(Event{Kind: EventTranscript, Entry: &entry})
			n.touchPresence(active, presenceRecord{
				PeerID:      senderPeerID,
				Name:        senderName,
				Fingerprint: senderFingerprint,
				State:       "online",
				LastSeen:    sentAt,
				ClearTyping: true,
			})
		case "heartbeat":
			n.touchPresence(active, presenceRecord{
				PeerID:      senderPeerID,
				Name:        senderName,
				Fingerprint: senderFingerprint,
				State:       "online",
				LastSeen:    sentAt,
			})
		case "typing":
			update := presenceRecord{
				PeerID:      senderPeerID,
				Name:        senderName,
				Fingerprint: senderFingerprint,
				State:       "online",
				LastSeen:    sentAt,
			}
			if payload.Typing != nil && *payload.Typing {
				update.TypingUntil = time.Now().Add(typingExpireAfter)
			} else {
				update.ClearTyping = true
			}
			n.touchPresence(active, update)
		}
	}
}

func (n *Node) heartbeatLoop(active *activeSwarm) {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-active.Context.Done():
			return
		case <-ticker.C:
			if err := n.publishKind(active, "heartbeat", ""); err != nil {
				n.emitSystem(fmt.Sprintf("heartbeat failed: %v", err))
			}
		}
	}
}

func (n *Node) reconnectLoop(active *activeSwarm) {
	ticker := time.NewTicker(reconnectEvery)
	defer ticker.Stop()
	for {
		select {
		case <-active.Context.Done():
			return
		case <-ticker.C:
			n.connectTrustedPeers(active.Swarm)
			go n.syncSwarm(active)
			go n.syncSwarmUpdates(active.Swarm)
		}
	}
}

func (n *Node) presenceLoop(active *activeSwarm) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-active.Context.Done():
			return
		case <-ticker.C:
			n.refreshPresence(active)
		}
	}
}

func (n *Node) publishKind(active *activeSwarm, kind, body string) error {
	env, err := n.newEnvelope(active, kind, envelopeBody{Body: body}, "", time.Time{})
	if err != nil {
		return err
	}
	return n.publishEnvelope(active, env)
}

func (n *Node) newEnvelope(active *activeSwarm, kind string, payload envelopeBody, id string, sentAt time.Time) (envelope, error) {
	if kind == "chat" {
		var ok bool
		payload.Body, ok = sanitizeChatBody(payload.Body)
		if !ok {
			return envelope{}, fmt.Errorf("chat body exceeds %d bytes", maxChatBodyBytes)
		}
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return envelope{}, fmt.Errorf("encode payload: %w", err)
	}
	nonce, ciphertext, err := yapcrypto.Encrypt(active.Swarm.RoomKey, payloadBytes)
	if err != nil {
		return envelope{}, err
	}
	if strings.TrimSpace(id) == "" {
		id = mustID()
	}
	if sentAt.IsZero() {
		sentAt = time.Now()
	}
	senderName := sanitizeDisplayName(n.identity.Name)
	signature, err := n.signEnvelope(active.Swarm.ID, id, kind, n.host.ID().String(), senderName, payload, sentAt)
	if err != nil {
		return envelope{}, err
	}
	return envelope{
		ID:         id,
		Kind:       kind,
		SenderName: senderName,
		SentAt:     sentAt,
		Signature:  signature,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

func (n *Node) publishEnvelope(active *activeSwarm, env envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}
	n.markSeen(env.ID)
	if err := active.Topic.Publish(active.Context, data); err != nil {
		return fmt.Errorf("publish envelope: %w", err)
	}
	return nil
}

func transcriptEntryFromEnvelope(swarmID string, senderPeerID string, env envelope, body string, local bool) model.TranscriptEntry {
	return model.TranscriptEntry{
		ID:           env.ID,
		SwarmID:      swarmID,
		Kind:         env.Kind,
		SenderPeerID: senderPeerID,
		SenderName:   env.SenderName,
		Body:         body,
		SentAt:       env.SentAt,
		Signature:    strings.TrimSpace(env.Signature),
		Local:        local,
	}
}

func (n *Node) syncTrustedPeerIdentity(active *activeSwarm, author peer.ID, claimedName, fingerprint string, seenAt time.Time) (string, string) {
	if active == nil || author == "" {
		return "", ""
	}
	claimedName = sanitizeDisplayName(claimedName)

	n.mu.Lock()
	defer n.mu.Unlock()

	previousName := resolvedPeerName(active.Swarm, author, "")
	active.Swarm.TrustedPeers = mergeTrustedPeer(active.Swarm.TrustedPeers, model.TrustedPeer{
		PeerID:      author.String(),
		Name:        claimedName,
		Fingerprint: fingerprint,
		LastSeen:    seenAt,
	})
	currentName := resolvedPeerName(active.Swarm, author, claimedName)
	return previousName, currentName
}

func (n *Node) syncSwarmUpdates(swarm model.Swarm) {
	swarm = normalizeSwarmMetadata(swarm)
	if !swarmOwnedBy(swarm, n.host.ID().String()) {
		return
	}
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == n.host.ID().String() {
			continue
		}
		trusted := trusted
		go n.sendSwarmUpdate(swarm, trusted, "sync", nil, true)
	}
}

func (n *Node) sendSwarmUpdate(swarm model.Swarm, trusted model.TrustedPeer, reason string, target *model.TrustedPeer, obeyCooldown bool) {
	swarm = normalizeSwarmMetadata(swarm)
	if strings.TrimSpace(trusted.PeerID) == "" || trusted.PeerID == n.host.ID().String() {
		return
	}
	if obeyCooldown && !n.shouldSyncSwarmUpdate(swarm.ID, trusted.PeerID, swarmVersion(swarm)) {
		return
	}

	peerID, err := peer.Decode(trusted.PeerID)
	if err != nil {
		return
	}
	n.addTrustedPeerAddrs(trusted)

	ctx, cancel := context.WithTimeout(n.ctx, historyStreamTimeout)
	defer cancel()

	stream, err := n.host.NewStream(ctx, peerID, swarmProtocolID)
	if err != nil {
		return
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(historyStreamTimeout))

	frame := swarmUpdateFrame{
		Swarm:  toWireSwarm(swarm),
		Reason: normalizeSwarmUpdateReason(reason),
	}
	if target != nil {
		copy := *target
		frame.Target = &wirePeer{
			PeerID:      copy.PeerID,
			Name:        copy.Name,
			Fingerprint: copy.Fingerprint,
			Addrs:       append([]string(nil), copy.Addrs...),
		}
	}
	_ = json.NewEncoder(stream).Encode(frame)
}

func (n *Node) sendRevocationNotice(swarm model.Swarm, removed model.TrustedPeer) {
	swarm = normalizeSwarmMetadata(swarm)
	if strings.TrimSpace(removed.PeerID) == "" || removed.PeerID == n.host.ID().String() {
		return
	}

	peerID, err := peer.Decode(removed.PeerID)
	if err != nil {
		return
	}
	n.addTrustedPeerAddrs(removed)

	ctx, cancel := context.WithTimeout(n.ctx, historyStreamTimeout)
	defer cancel()

	stream, err := n.host.NewStream(ctx, peerID, swarmProtocolID)
	if err != nil {
		return
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(historyStreamTimeout))

	frame := swarmUpdateFrame{
		Swarm: wireSwarm{
			ID:          swarm.ID,
			Name:        swarm.Name,
			OwnerPeerID: swarmOwnerPeerID(swarm),
			Version:     swarmVersion(swarm),
		},
		Reason: "revoked",
		Target: &wirePeer{
			PeerID:      removed.PeerID,
			Name:        removed.Name,
			Fingerprint: removed.Fingerprint,
			Addrs:       append([]string(nil), removed.Addrs...),
		},
	}
	_ = json.NewEncoder(stream).Encode(frame)
}

func (n *Node) handleRevocationNotice(stream network.Stream, frame swarmUpdateFrame) {
	current, err := n.store.LoadSwarm(strings.TrimSpace(frame.Swarm.ID))
	if err != nil {
		return
	}
	current = normalizeSwarmMetadata(current)
	ownerPeerID := swarmOwnerPeerID(current)
	if ownerPeerID == "" || stream.Conn().RemotePeer().String() != ownerPeerID {
		return
	}
	if frame.Target == nil || strings.TrimSpace(frame.Target.PeerID) != n.host.ID().String() {
		return
	}

	actor, ok := trustedPeerByID(current.TrustedPeers, ownerPeerID)
	if !ok {
		actor = model.TrustedPeer{PeerID: ownerPeerID}
	}
	actor.Addrs = uniqueStrings(append(actor.Addrs, n.peerAddrs(ownerPeerID)...))
	if actor.LastSeen.IsZero() {
		actor.LastSeen = time.Now()
	}

	n.emit(Event{
		Kind: EventSwarmRevoked,
		Revocation: &SwarmRevocation{
			SwarmID:   current.ID,
			SwarmName: current.Name,
			Version:   swarmVersion(model.Swarm{Version: frame.Swarm.Version}),
			Actor:     actor,
		},
	})
}

func (n *Node) connectTrustedPeers(swarm model.Swarm) {
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == n.host.ID().String() {
			continue
		}
		info, ok := n.addrInfoForTrustedPeer(trusted)
		if !ok || len(info.Addrs) == 0 {
			continue
		}
		n.prepareRelayPeer(info.ID)
		n.host.Peerstore().AddAddrs(info.ID, info.Addrs, 10*time.Minute)
		go func(info peer.AddrInfo) {
			ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
			defer cancel()
			_ = n.host.Connect(ctx, info)
		}(info)
	}
}

func (n *Node) touchPresence(active *activeSwarm, update presenceRecord) {
	n.mu.Lock()
	record, ok := active.Presence[update.PeerID]
	previousState := ""
	changed := false
	if !ok {
		record = &presenceRecord{
			PeerID:      update.PeerID,
			Name:        update.Name,
			Fingerprint: update.Fingerprint,
		}
		active.Presence[update.PeerID] = record
		changed = true
	} else {
		previousState = record.State
	}
	if update.Name != "" && update.Name != record.Name {
		record.Name = update.Name
		changed = true
	}
	if update.Fingerprint != "" && update.Fingerprint != record.Fingerprint {
		record.Fingerprint = update.Fingerprint
		changed = true
	}
	if !update.LastSeen.IsZero() && !update.LastSeen.Equal(record.LastSeen) {
		record.LastSeen = update.LastSeen
		changed = true
	}
	if update.State != "" && update.State != record.State {
		record.State = update.State
		changed = true
	}
	if update.ClearTyping {
		if !record.TypingUntil.IsZero() {
			record.TypingUntil = time.Time{}
			changed = true
		}
	} else if !update.TypingUntil.IsZero() && !update.TypingUntil.Equal(record.TypingUntil) {
		record.TypingUntil = update.TypingUntil
		changed = true
	}
	n.mu.Unlock()
	if !changed {
		return
	}
	n.emitPresenceSnapshot(active)
	if update.PeerID != n.host.ID().String() && record.State == "online" && previousState != "online" {
		go n.syncSwarm(active)
	}
}

func (n *Node) refreshPresence(active *activeSwarm) {
	n.mu.Lock()
	now := time.Now()
	changed := false
	for _, record := range active.Presence {
		if record.PeerID == n.host.ID().String() {
			if record.State != "online" {
				record.State = "online"
				changed = true
			}
			if !record.TypingUntil.IsZero() && now.After(record.TypingUntil) {
				record.TypingUntil = time.Time{}
				changed = true
			}
			continue
		}
		nextState := "offline"
		if record.LastSeen.IsZero() {
			if record.State != nextState {
				record.State = nextState
				changed = true
			}
			continue
		}
		age := now.Sub(record.LastSeen)
		switch {
		case age >= offlineAfter:
			nextState = "offline"
		case age >= staleAfter:
			nextState = "stale"
		default:
			nextState = "online"
		}
		if record.State != nextState {
			record.State = nextState
			changed = true
		}
		if !record.TypingUntil.IsZero() && now.After(record.TypingUntil) {
			record.TypingUntil = time.Time{}
			changed = true
		}
	}
	n.mu.Unlock()
	if changed {
		n.emitPresenceSnapshot(active)
	}
}

func (n *Node) emitPresenceSnapshot(active *activeSwarm) {
	n.mu.RLock()
	now := time.Now()
	presence := make([]model.Presence, 0, len(active.Presence))
	for _, record := range active.Presence {
		presence = append(presence, model.Presence{
			PeerID:      record.PeerID,
			Name:        record.Name,
			Fingerprint: record.Fingerprint,
			Addrs:       n.peerAddrs(record.PeerID),
			State:       record.State,
			Typing:      !record.TypingUntil.IsZero() && now.Before(record.TypingUntil),
			LastSeen:    record.LastSeen,
		})
	}
	n.mu.RUnlock()
	sort.Slice(presence, func(i, j int) bool {
		if presence[i].State == presence[j].State {
			return strings.ToLower(presence[i].Name) < strings.ToLower(presence[j].Name)
		}
		return presenceRank(presence[i].State) < presenceRank(presence[j].State)
	})
	n.emit(Event{Kind: EventPresence, SwarmID: active.Swarm.ID, Presence: presence})
}

func (n *Node) addTrustedPeerAddrs(trusted model.TrustedPeer) {
	info, ok := n.addrInfoForTrustedPeer(trusted)
	if !ok || len(info.Addrs) == 0 {
		return
	}
	n.prepareRelayPeer(info.ID)
	n.host.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
}

func (n *Node) currentSwarm(swarmID string) *activeSwarm {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.active[swarmID]
}

func (n *Node) emit(event Event) {
	switch event.Kind {
	case EventNearbySnapshot, EventPresence, EventSystem:
		select {
		case n.events <- event:
		case <-n.ctx.Done():
		default:
		}
		return
	}
	select {
	case n.events <- event:
	case <-n.ctx.Done():
	}
}

func (n *Node) emitSystem(message string) {
	n.emit(Event{Kind: EventSystem, Message: message})
}

func (n *Node) emitNearbySnapshot() {
	n.mu.RLock()
	nearby := make([]model.NearbyPeer, 0, len(n.nearby))
	for _, peerInfo := range n.nearby {
		nearby = append(nearby, peerInfo)
	}
	n.mu.RUnlock()
	sort.Slice(nearby, func(i, j int) bool {
		if nearby[i].LastSeen.Equal(nearby[j].LastSeen) {
			return strings.ToLower(nearbyLabel(nearby[i])) < strings.ToLower(nearbyLabel(nearby[j]))
		}
		return nearby[i].LastSeen.After(nearby[j].LastSeen)
	})
	n.emit(Event{Kind: EventNearbySnapshot, NearbyPeers: nearby})
}

func configuredRelayFromEnv() (*configuredRelay, error) {
	raw := strings.TrimSpace(os.Getenv("YAP_RELAY_ADDR"))
	if raw == "" {
		return nil, nil
	}
	info, err := peer.AddrInfoFromString(raw)
	if err != nil {
		return nil, fmt.Errorf("parse YAP_RELAY_ADDR: %w", err)
	}
	if info == nil || info.ID == "" || len(info.Addrs) == 0 {
		return nil, fmt.Errorf("YAP_RELAY_ADDR must include a relay peer id and at least one address")
	}
	return &configuredRelay{
		Info: peer.AddrInfo{
			ID:    info.ID,
			Addrs: append([]ma.Multiaddr(nil), info.Addrs...),
		},
	}, nil
}

func (n *Node) relayLoop() {
	if n.relay == nil {
		return
	}
	for {
		waitFor, err := n.refreshRelayReservation()
		if err != nil {
			n.setRelayAddrs(nil)
			waitFor = relayRetryEvery
		}
		select {
		case <-n.ctx.Done():
			return
		case <-time.After(waitFor):
		}
	}
}

func (n *Node) refreshRelayReservation() (time.Duration, error) {
	if n.relay == nil {
		return relayRetryEvery, fmt.Errorf("relay is not configured")
	}

	n.prepareRelayPeer(n.relay.Info.ID)

	ctx, cancel := context.WithTimeout(n.ctx, 20*time.Second)
	defer cancel()

	if err := n.host.Connect(ctx, n.relay.Info); err != nil {
		return relayRetryEvery, fmt.Errorf("connect relay: %w", err)
	}

	reservation, err := relayclient.Reserve(ctx, n.host, n.relay.Info)
	if err != nil {
		return relayRetryEvery, fmt.Errorf("reserve relay slot: %w", err)
	}

	n.setRelayAddrs(sanitizePeerAddrs(multiaddrStrings(reservation.Addrs)))

	refreshAfter := time.Until(reservation.Expiration) - relayRefreshLead
	if refreshAfter < relayMinRefresh {
		refreshAfter = relayMinRefresh
	}
	return refreshAfter, nil
}

func (n *Node) currentRelayAddrs() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]string(nil), n.relayAddrs...)
}

func (n *Node) setRelayAddrs(addrs []string) {
	n.mu.Lock()
	n.relayAddrs = sanitizePeerAddrs(addrs)
	n.mu.Unlock()
}

func (n *Node) prepareRelayPeer(target peer.ID) bool {
	if n.relay == nil {
		return false
	}
	n.host.Peerstore().AddAddrs(n.relay.Info.ID, n.relay.Info.Addrs, peerstore.PermanentAddrTTL)
	if target == "" || target == n.relay.Info.ID {
		return true
	}
	circuitAddrs := multiaddrsFromStrings(n.relayCircuitAddrs(target.String()))
	if len(circuitAddrs) == 0 {
		return false
	}
	n.host.Peerstore().AddAddrs(target, circuitAddrs, peerstore.PermanentAddrTTL)
	return true
}

func (n *Node) relayCircuitAddrs(peerID string) []string {
	if n.relay == nil || strings.TrimSpace(peerID) == "" || peerID == n.host.ID().String() {
		return nil
	}
	relayComponent, err := ma.NewMultiaddr("/p2p/" + n.relay.Info.ID.String())
	if err != nil {
		return nil
	}
	circuitComponent, err := ma.NewMultiaddr("/p2p-circuit")
	if err != nil {
		return nil
	}

	out := make([]string, 0, len(n.relay.Info.Addrs))
	for _, addr := range n.relay.Info.Addrs {
		out = append(out, addr.Encapsulate(relayComponent).Encapsulate(circuitComponent).String())
	}
	return sanitizePeerAddrs(out)
}

func (n *Node) addrInfoForTrustedPeer(trusted model.TrustedPeer) (peer.AddrInfo, bool) {
	id, err := peer.Decode(trusted.PeerID)
	if err != nil {
		return peer.AddrInfo{}, false
	}
	addrs := uniqueStrings(append(
		sanitizePeerAddrs(trusted.Addrs),
		n.relayCircuitAddrs(trusted.PeerID)...,
	))
	return peer.AddrInfo{ID: id, Addrs: multiaddrsFromStrings(addrs)}, true
}

func (n *Node) selfPeer() model.TrustedPeer {
	addrs := uniqueStrings(append(
		sanitizePeerAddrs(multiaddrStrings(n.host.Addrs())),
		n.currentRelayAddrs()...,
	))
	return model.TrustedPeer{
		PeerID:      n.host.ID().String(),
		Name:        sanitizeDisplayName(n.identity.Name),
		Fingerprint: n.identity.Fingerprint,
		Addrs:       sanitizePeerAddrs(addrs),
		LastSeen:    time.Now(),
	}
}

func (n *Node) lookupInvite(code string) (*activeInvite, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	invite, ok := n.invites[code]
	if !ok || time.Now().After(invite.Invite.ExpiresAt) {
		return nil, false
	}
	return invite, true
}

func (n *Node) bumpInviteUse(code string, success bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	invite, ok := n.invites[code]
	if !ok {
		return
	}
	if success {
		invite.Invite.CurrentUse++
	}
	if invite.Invite.CurrentUse >= invite.Invite.MaxUses || time.Now().After(invite.Invite.ExpiresAt) {
		delete(n.invites, code)
	}
}

func (n *Node) inviteJanitor() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			n.mu.Lock()
			for code, invite := range n.invites {
				if now.After(invite.Invite.ExpiresAt) {
					delete(n.invites, code)
				}
			}
			n.mu.Unlock()
		}
	}
}

func (n *Node) markSeen(id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.seen[id]; ok {
		return true
	}
	n.seen[id] = struct{}{}
	n.seenOrder = append(n.seenOrder, id)
	if len(n.seenOrder) > maxSeenIDs {
		evicted := n.seenOrder[0]
		n.seenOrder = n.seenOrder[1:]
		delete(n.seen, evicted)
	}
	return false
}

func waitForDecision(ctx context.Context, ch <-chan bool) (bool, bool) {
	select {
	case <-ctx.Done():
		return false, false
	case decision, ok := <-ch:
		return decision, ok
	}
}

func (n *Node) currentInviteSwarm(invite *activeInvite) model.Swarm {
	if invite == nil {
		return model.Swarm{}
	}
	if n != nil && n.store != nil && strings.TrimSpace(invite.Invite.SwarmID) != "" {
		if swarm, err := n.store.LoadSwarm(invite.Invite.SwarmID); err == nil {
			return normalizeSwarmMetadata(swarm)
		}
	}
	return normalizeSwarmMetadata(invite.Swarm)
}

func validatedSwarm(raw wireSwarm) (model.Swarm, error) {
	swarm := model.Swarm{
		ID:           strings.TrimSpace(raw.ID),
		Name:         sanitizeDisplayName(raw.Name),
		RoomKey:      strings.TrimSpace(raw.RoomKey),
		OwnerPeerID:  strings.TrimSpace(raw.OwnerPeerID),
		Version:      raw.Version,
		TrustedPeers: make([]model.TrustedPeer, 0, len(raw.TrustedPeers)),
	}
	if swarm.ID == "" {
		return model.Swarm{}, fmt.Errorf("invalid swarm id")
	}
	if swarm.Name == "" {
		return model.Swarm{}, fmt.Errorf("invalid swarm name")
	}
	if swarm.RoomKey == "" {
		return model.Swarm{}, fmt.Errorf("invalid room key")
	}
	if _, _, err := yapcrypto.Encrypt(swarm.RoomKey, []byte("{}")); err != nil {
		return model.Swarm{}, fmt.Errorf("invalid room key")
	}
	for _, rawPeer := range raw.TrustedPeers {
		swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, trustedPeerFromWire(rawPeer))
	}
	swarm = normalizeSwarmMetadata(swarm)
	if ownerPeerID := swarmOwnerPeerID(swarm); ownerPeerID != "" && !swarmHasTrustedPeerID(swarm, ownerPeerID) {
		return model.Swarm{}, fmt.Errorf("invalid swarm owner")
	}
	return swarm, nil
}

func verifiedTrustedPeer(conn network.Conn, claimed wirePeer) (model.TrustedPeer, error) {
	if conn == nil {
		return model.TrustedPeer{}, fmt.Errorf("missing authenticated connection")
	}
	addrs := sanitizePeerAddrs(claimed.Addrs)
	if remoteAddr := conn.RemoteMultiaddr(); remoteAddr != nil {
		addrs = uniqueStrings(append(addrs, remoteAddr.String()))
	}
	return verifiedClaimedPeer(conn.RemotePeer(), conn.RemotePublicKey(), addrs, claimed)
}

func verifiedClaimedPeer(remotePeer peer.ID, remoteKey corecrypto.PubKey, observedAddrs []string, claimed wirePeer) (model.TrustedPeer, error) {
	if remotePeer == "" {
		return model.TrustedPeer{}, fmt.Errorf("missing authenticated peer id")
	}
	if remoteKey == nil {
		return model.TrustedPeer{}, fmt.Errorf("missing authenticated peer key")
	}
	derivedPeerID, err := peer.IDFromPublicKey(remoteKey)
	if err != nil {
		return model.TrustedPeer{}, fmt.Errorf("derive authenticated peer id: %w", err)
	}
	if derivedPeerID != remotePeer {
		return model.TrustedPeer{}, fmt.Errorf("authenticated peer id mismatch")
	}
	publicKeyBytes, err := corecrypto.MarshalPublicKey(remoteKey)
	if err != nil {
		return model.TrustedPeer{}, fmt.Errorf("marshal authenticated peer key: %w", err)
	}
	fingerprint := yapcrypto.Fingerprint(publicKeyBytes)
	if claimedPeerID := strings.TrimSpace(claimed.PeerID); claimedPeerID != "" && claimedPeerID != remotePeer.String() {
		return model.TrustedPeer{}, fmt.Errorf("claimed peer id does not match authenticated peer")
	}
	if claimedFingerprint := strings.TrimSpace(claimed.Fingerprint); claimedFingerprint != "" && !strings.EqualFold(claimedFingerprint, fingerprint) {
		return model.TrustedPeer{}, fmt.Errorf("claimed fingerprint does not match authenticated peer")
	}
	return model.TrustedPeer{
		PeerID:      remotePeer.String(),
		Name:        sanitizeDisplayName(claimed.Name),
		Fingerprint: fingerprint,
		Addrs:       sanitizePeerAddrs(observedAddrs),
	}, nil
}

func toWirePeer(peerInfo model.TrustedPeer) wirePeer {
	return wirePeer{
		PeerID:      peerInfo.PeerID,
		Name:        sanitizeDisplayName(peerInfo.Name),
		Fingerprint: peerInfo.Fingerprint,
		Addrs:       sanitizePeerAddrs(peerInfo.Addrs),
	}
}

func trustedPeerFromWire(raw wirePeer) model.TrustedPeer {
	return model.TrustedPeer{
		PeerID:      strings.TrimSpace(raw.PeerID),
		Name:        sanitizeDisplayName(raw.Name),
		Fingerprint: strings.TrimSpace(raw.Fingerprint),
		Addrs:       sanitizePeerAddrs(raw.Addrs),
	}
}

func toWireSwarm(swarm model.Swarm) wireSwarm {
	swarm = normalizeSwarmMetadata(swarm)
	out := wireSwarm{
		ID:          swarm.ID,
		Name:        swarm.Name,
		RoomKey:     swarm.RoomKey,
		OwnerPeerID: swarmOwnerPeerID(swarm),
		Version:     swarmVersion(swarm),
	}
	out.TrustedPeers = make([]wirePeer, 0, len(swarm.TrustedPeers))
	for _, trusted := range swarm.TrustedPeers {
		out.TrustedPeers = append(out.TrustedPeers, toWirePeer(trusted))
	}
	return out
}

func mergeTrustedPeer(peers []model.TrustedPeer, peerInfo model.TrustedPeer) []model.TrustedPeer {
	for i, existing := range peers {
		if existing.PeerID != peerInfo.PeerID {
			continue
		}
		merged := existing
		if peerInfo.Name != "" {
			merged.Name = peerInfo.Name
		}
		if peerInfo.Fingerprint != "" {
			merged.Fingerprint = peerInfo.Fingerprint
		}
		if len(peerInfo.Addrs) > 0 {
			merged.Addrs = uniqueStrings(append(merged.Addrs, peerInfo.Addrs...))
		}
		if peerInfo.LastSeen.After(merged.LastSeen) {
			merged.LastSeen = peerInfo.LastSeen
		}
		peers[i] = merged
		return peers
	}
	return append(peers, peerInfo)
}

func sameSwarmSession(left, right model.Swarm) bool {
	left = normalizeSwarmMetadata(left)
	right = normalizeSwarmMetadata(right)
	if left.ID != right.ID ||
		left.Name != right.Name ||
		left.RoomKey != right.RoomKey ||
		swarmOwnerPeerID(left) != swarmOwnerPeerID(right) ||
		swarmVersion(left) != swarmVersion(right) ||
		left.Revoked != right.Revoked ||
		!left.RevokedAt.Equal(right.RevokedAt) {
		return false
	}
	leftPeers := sortedTrustedPeers(left.TrustedPeers)
	rightPeers := sortedTrustedPeers(right.TrustedPeers)
	if len(leftPeers) != len(rightPeers) {
		return false
	}
	for i := range leftPeers {
		l := leftPeers[i]
		r := rightPeers[i]
		if l.PeerID != r.PeerID ||
			l.Name != r.Name ||
			l.Fingerprint != r.Fingerprint ||
			!l.LastSeen.Equal(r.LastSeen) ||
			!sameStrings(l.Addrs, r.Addrs) {
			return false
		}
	}
	return true
}

func sortedTrustedPeers(peers []model.TrustedPeer) []model.TrustedPeer {
	out := append([]model.TrustedPeer(nil), peers...)
	for i := range out {
		out[i].Addrs = sanitizePeerAddrs(out[i].Addrs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PeerID == out[j].PeerID {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

func normalizeSwarmMetadata(swarm model.Swarm) model.Swarm {
	swarm.OwnerPeerID = swarmOwnerPeerID(swarm)
	if swarm.Version == 0 {
		swarm.Version = 1
	}
	if owner := swarm.OwnerPeerID; owner != "" && len(swarm.TrustedPeers) > 0 {
		reordered := make([]model.TrustedPeer, 0, len(swarm.TrustedPeers))
		if ownerPeer, ok := trustedPeerByID(swarm.TrustedPeers, owner); ok {
			reordered = append(reordered, ownerPeer)
		}
		others := make([]model.TrustedPeer, 0, len(swarm.TrustedPeers))
		for _, trusted := range swarm.TrustedPeers {
			if trusted.PeerID == owner {
				continue
			}
			others = append(others, trusted)
		}
		sort.Slice(others, func(i, j int) bool {
			return others[i].PeerID < others[j].PeerID
		})
		swarm.TrustedPeers = append(reordered, others...)
	}
	return swarm
}

func swarmOwnerPeerID(swarm model.Swarm) string {
	if ownerPeerID := strings.TrimSpace(swarm.OwnerPeerID); ownerPeerID != "" {
		return ownerPeerID
	}
	for _, trusted := range swarm.TrustedPeers {
		if peerID := strings.TrimSpace(trusted.PeerID); peerID != "" {
			return peerID
		}
	}
	return ""
}

func swarmVersion(swarm model.Swarm) uint64 {
	if swarm.Version == 0 {
		return 1
	}
	return swarm.Version
}

func nextSwarmVersion(swarm model.Swarm) uint64 {
	return swarmVersion(swarm) + 1
}

func swarmOwnedBy(swarm model.Swarm, peerID string) bool {
	peerID = strings.TrimSpace(peerID)
	return peerID != "" && swarmOwnerPeerID(swarm) == peerID
}

func trustedPeerByID(peers []model.TrustedPeer, peerID string) (model.TrustedPeer, bool) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return model.TrustedPeer{}, false
	}
	for _, trusted := range peers {
		if trusted.PeerID == peerID {
			return trusted, true
		}
	}
	return model.TrustedPeer{}, false
}

func (n *Node) shouldSyncSwarmUpdate(swarmID, peerID string, version uint64) bool {
	key := swarmID + "/" + peerID
	n.mu.Lock()
	defer n.mu.Unlock()
	last, ok := n.swarmSync[key]
	if ok && last.Version == version && time.Since(last.SentAt) < swarmSyncCooldown {
		return false
	}
	n.swarmSync[key] = swarmSyncRecord{Version: version, SentAt: time.Now()}
	return true
}

func normalizeSwarmUpdateReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "join", "revoke", "revoked", "rotate", "sync":
		return strings.TrimSpace(reason)
	default:
		return "sync"
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func multiaddrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.String())
	}
	sort.Strings(out)
	return out
}

func multiaddrsFromStrings(values []string) []ma.Multiaddr {
	out := make([]ma.Multiaddr, 0, len(values))
	for _, raw := range values {
		addr, err := ma.NewMultiaddr(raw)
		if err != nil {
			continue
		}
		out = append(out, addr)
	}
	return out
}

func mergeAddrInfo(current, next peer.AddrInfo) peer.AddrInfo {
	out := peer.AddrInfo{ID: next.ID}
	if out.ID == "" {
		out.ID = current.ID
	}
	merged := make([]ma.Multiaddr, 0, len(current.Addrs)+len(next.Addrs))
	seen := make(map[string]struct{}, len(current.Addrs)+len(next.Addrs))
	for _, addr := range append(append([]ma.Multiaddr(nil), current.Addrs...), next.Addrs...) {
		if addr == nil {
			continue
		}
		key := addr.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, addr)
	}
	out.Addrs = merged
	return out
}

func nearbyLabel(peerInfo model.NearbyPeer) string {
	if strings.TrimSpace(peerInfo.Name) != "" {
		return peerInfo.Name
	}
	return peerInfo.PeerID
}

func topicName(roomKey string) string {
	keyMaterial, err := base64.StdEncoding.DecodeString(strings.TrimSpace(roomKey))
	if err != nil || len(keyMaterial) == 0 {
		keyMaterial = []byte(strings.TrimSpace(roomKey))
	}
	sum := sha256.Sum256(append([]byte("yap-topic-v3:"), keyMaterial...))
	return "yap/swarm/" + hex.EncodeToString(sum[:]) + "/v3"
}

func listenAddrs() []string {
	// quic-go v0.49.0 can panic on Go 1.26 during the TLS session-ticket path.
	// Default to TCP until the libp2p dependency line can move to a fixed QUIC stack.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("YAP_TRANSPORT")), "quic") {
		return []string{
			"/ip4/0.0.0.0/udp/0/quic-v1",
			"/ip6/::/udp/0/quic-v1",
		}
	}
	return []string{
		"/ip4/0.0.0.0/tcp/0",
		"/ip6/::/tcp/0",
	}
}

func presenceRank(state string) int {
	switch state {
	case "online":
		return 0
	case "stale":
		return 1
	default:
		return 2
	}
}

func mustID() string {
	id, err := yapcrypto.RandomID(12)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return id
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func selfPresence(self model.TrustedPeer) presenceRecord {
	return presenceRecord{
		PeerID:      self.PeerID,
		Name:        self.Name,
		Fingerprint: self.Fingerprint,
		State:       "online",
		LastSeen:    time.Now(),
	}
}

func sanitizeDisplayName(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > maxDisplayNameBytes {
		value = string(runes[:maxDisplayNameBytes])
	}
	return value
}

func sanitizePeerAddrs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range uniqueStrings(values) {
		if len(out) >= maxPeerAddrs {
			break
		}
		if _, err := ma.NewMultiaddr(value); err != nil {
			continue
		}
		out = append(out, value)
	}
	return out
}

func sanitizeChatBody(body string) (string, bool) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if len(body) > maxChatBodyBytes {
		return "", false
	}
	return body, true
}

func clampEventTime(sentAt time.Time) time.Time {
	now := time.Now()
	if sentAt.IsZero() {
		return now
	}
	if sentAt.After(now.Add(maxClockSkew)) || sentAt.Before(now.Add(-maxClockSkew)) {
		return now
	}
	return sentAt
}

func resolvedPeerName(swarm model.Swarm, author peer.ID, claimed string) string {
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID != author.String() {
			continue
		}
		if name := sanitizeDisplayName(trusted.Name); name != "" {
			return name
		}
		break
	}
	if name := sanitizeDisplayName(claimed); name != "" {
		return name
	}
	return author.String()
}

func swarmHasTrustedPeer(swarm model.Swarm, author peer.ID) bool {
	if author == "" {
		return false
	}
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == author.String() {
			return true
		}
	}
	return false
}

func (n *Node) peerFingerprint(author peer.ID) string {
	if author == "" {
		return ""
	}
	if key := n.host.Peerstore().PubKey(author); key != nil {
		publicKeyBytes, err := corecrypto.MarshalPublicKey(key)
		if err == nil {
			return yapcrypto.Fingerprint(publicKeyBytes)
		}
	}
	return ""
}

func (n *Node) peerAddrs(peerID string) []string {
	if strings.TrimSpace(peerID) == "" {
		return nil
	}
	if peerID == n.host.ID().String() {
		return n.selfPeer().Addrs
	}
	decoded, err := peer.Decode(peerID)
	if err != nil {
		return nil
	}
	addrs := uniqueStrings(append(
		sanitizePeerAddrs(multiaddrStrings(n.host.Peerstore().Addrs(decoded))),
		n.relayCircuitAddrs(peerID)...,
	))
	return sanitizePeerAddrs(addrs)
}

func boolPtr(value bool) *bool {
	return &value
}

type signedTranscriptEnvelope struct {
	SwarmID         string `json:"swarm_id"`
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	SenderPeerID    string `json:"sender_peer_id"`
	SenderName      string `json:"sender_name"`
	Body            string `json:"body,omitempty"`
	Typing          *bool  `json:"typing,omitempty"`
	SentAtUnixMilli int64  `json:"sent_at_unix_milli"`
}

func (n *Node) signEnvelope(swarmID, id, kind, senderPeerID, senderName string, payload envelopeBody, sentAt time.Time) (string, error) {
	if n.signer == nil {
		return "", fmt.Errorf("missing signer")
	}
	data, err := json.Marshal(transcriptSignaturePayload(swarmID, id, kind, senderPeerID, senderName, payload.Body, payload.Typing, sentAt))
	if err != nil {
		return "", fmt.Errorf("encode envelope signature payload: %w", err)
	}
	signature, err := n.signer.Sign(data)
	if err != nil {
		return "", fmt.Errorf("sign envelope: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func (n *Node) signTranscriptEntry(entry model.TranscriptEntry) (string, error) {
	if n.signer == nil {
		return "", fmt.Errorf("missing signer")
	}
	data, err := json.Marshal(transcriptSignaturePayload(
		entry.SwarmID,
		entry.ID,
		entry.Kind,
		entry.SenderPeerID,
		entry.SenderName,
		entry.Body,
		nil,
		entry.SentAt,
	))
	if err != nil {
		return "", fmt.Errorf("encode transcript signature payload: %w", err)
	}
	signature, err := n.signer.Sign(data)
	if err != nil {
		return "", fmt.Errorf("sign transcript entry: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func (n *Node) verifyEnvelopeSignature(swarmID string, author peer.ID, env envelope, payload envelopeBody) bool {
	return n.verifyTranscriptSignature(
		author.String(),
		transcriptSignaturePayload(
			swarmID,
			env.ID,
			env.Kind,
			author.String(),
			env.SenderName,
			payload.Body,
			payload.Typing,
			env.SentAt,
		),
		env.Signature,
	)
}

func (n *Node) verifyTranscriptEntrySignature(entry model.TranscriptEntry) bool {
	return n.verifyTranscriptSignature(
		entry.SenderPeerID,
		transcriptSignaturePayload(
			entry.SwarmID,
			entry.ID,
			entry.Kind,
			entry.SenderPeerID,
			entry.SenderName,
			entry.Body,
			nil,
			entry.SentAt,
		),
		entry.Signature,
	)
}

func (n *Node) verifyTranscriptSignature(senderPeerID string, payload signedTranscriptEnvelope, signatureBase64 string) bool {
	signatureBase64 = strings.TrimSpace(signatureBase64)
	if senderPeerID == "" || signatureBase64 == "" {
		return false
	}
	publicKey := n.publicKeyForPeer(senderPeerID)
	if publicKey == nil {
		return false
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	signature, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil {
		return false
	}
	ok, err := publicKey.Verify(data, signature)
	return err == nil && ok
}

func (n *Node) publicKeyForPeer(peerID string) corecrypto.PubKey {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil
	}
	decoded, err := peer.Decode(peerID)
	if err != nil {
		return nil
	}
	if n.host != nil {
		if n.host.ID() == decoded && n.signer != nil {
			return n.signer.GetPublic()
		}
		if key := n.host.Peerstore().PubKey(decoded); key != nil {
			return key
		}
	}
	key, err := decoded.ExtractPublicKey()
	if err != nil {
		return nil
	}
	return key
}

func transcriptSignaturePayload(swarmID, id, kind, senderPeerID, senderName, body string, typing *bool, sentAt time.Time) signedTranscriptEnvelope {
	return signedTranscriptEnvelope{
		SwarmID:         strings.TrimSpace(swarmID),
		ID:              strings.TrimSpace(id),
		Kind:            strings.TrimSpace(kind),
		SenderPeerID:    strings.TrimSpace(senderPeerID),
		SenderName:      sanitizeDisplayName(senderName),
		Body:            body,
		Typing:          typing,
		SentAtUnixMilli: sentAt.UTC().UnixMilli(),
	}
}

type discoveryNotifee struct {
	node *Node
}

func (d *discoveryNotifee) HandlePeerFound(info peer.AddrInfo) {
	if d == nil || d.node == nil {
		return
	}
	if info.ID == d.node.host.ID() {
		return
	}
	d.node.noteNearbyCandidate(info)
}

var _ mdns.Notifee = (*discoveryNotifee)(nil)
var _ io.Closer = (*Node)(nil)
