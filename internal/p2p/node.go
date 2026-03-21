package p2p

import (
	"context"
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
	ma "github.com/multiformats/go-multiaddr"

	yapcrypto "yap/internal/crypto"
	"yap/internal/model"
)

const (
	mdnsServiceName  = "yap-v1"
	cardProtocolID   = protocol.ID("/yap/card/1")
	pairProtocolID   = protocol.ID("/yap/pair/1")
	heartbeatEvery   = 15 * time.Second
	staleAfter       = 45 * time.Second
	offlineAfter     = 90 * time.Second
	reconnectEvery   = 20 * time.Second
	inviteTTL        = 5 * time.Minute
	defaultInviteUse = 5
)

// EventKind is a UI-facing classification for node events.
type EventKind string

const (
	EventNearby       EventKind = "nearby"
	EventSystem       EventKind = "system"
	EventApproval     EventKind = "approval"
	EventPairComplete EventKind = "pair_complete"
	EventTranscript   EventKind = "transcript"
	EventPresence     EventKind = "presence"
)

// Event describes a state change from the node.
type Event struct {
	Kind     EventKind
	Message  string
	Nearby   *model.NearbyPeer
	Approval *Approval
	Pair     *PairResult
	Entry    *model.TranscriptEntry
	Presence []model.Presence
	AutoOpen bool
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

// Node hosts discovery, pairing, pubsub, and room presence.
type Node struct {
	ctx      context.Context
	cancel   context.CancelFunc
	host     host.Host
	pubsub   *pubsub.PubSub
	identity model.Identity

	events chan Event

	mu      sync.RWMutex
	nearby  map[string]model.NearbyPeer
	invites map[string]*activeInvite
	pending map[string]chan bool
	active  *activeSwarm
	closed  bool
	seen    map[string]struct{}
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

type presenceRecord struct {
	PeerID      string
	Name        string
	Fingerprint string
	State       string
	LastSeen    time.Time
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
	Stage string     `json:"stage"`
	Swarm wireSwarm  `json:"swarm"`
	Peer  wirePeer   `json:"peer"`
	Seeds []wirePeer `json:"seeds,omitempty"`
}

type wireSwarm struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	RoomKey string `json:"room_key"`
}

type envelope struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	SenderPeerID string    `json:"sender_peer_id"`
	SenderName   string    `json:"sender_name"`
	SentAt       time.Time `json:"sent_at"`
	Nonce        string    `json:"nonce"`
	Ciphertext   string    `json:"ciphertext"`
}

type envelopeBody struct {
	Body string `json:"body,omitempty"`
}

// New creates a node with a persisted identity.
func New(parent context.Context, identity model.Identity) (*Node, error) {
	ctx, cancel := context.WithCancel(parent)

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

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("create gossipsub: %w", err)
	}

	node := &Node{
		ctx:      ctx,
		cancel:   cancel,
		host:     h,
		pubsub:   ps,
		identity: identity,
		events:   make(chan Event, 256),
		nearby:   make(map[string]model.NearbyPeer),
		invites:  make(map[string]*activeInvite),
		pending:  make(map[string]chan bool),
		seen:     make(map[string]struct{}),
	}

	h.SetStreamHandler(cardProtocolID, node.handleCardStream)
	h.SetStreamHandler(pairProtocolID, node.handlePairStream)

	service := mdns.NewMdnsService(h, mdnsServiceName, &discoveryNotifee{node: node})
	if err := service.Start(); err != nil {
		node.Close()
		return nil, fmt.Errorf("start mdns: %w", err)
	}

	go node.inviteJanitor()

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
	code, err := yapcrypto.NewInviteCode()
	if err != nil {
		return model.Invite{}, err
	}
	invite := model.Invite{
		Code:       code,
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

// OpenSwarm joins the room topic and begins presence/publication loops.
func (n *Node) OpenSwarm(swarm model.Swarm) error {
	if err := n.CloseSwarm(); err != nil {
		return err
	}
	topicName := topicName(swarm.ID)
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
	n.active = active
	n.mu.Unlock()

	n.emitPresenceSnapshot(active)
	n.connectTrustedPeers(active.Swarm)
	go n.subscriptionLoop(active)
	go n.heartbeatLoop(active)
	go n.reconnectLoop(active)
	go n.presenceLoop(active)
	if err := n.publishEnvelope(active, "join", ""); err != nil {
		n.emitSystem(fmt.Sprintf("failed to publish join: %v", err))
	}
	return nil
}

// CloseSwarm leaves the current room if one is active.
func (n *Node) CloseSwarm() error {
	n.mu.Lock()
	active := n.active
	n.active = nil
	n.mu.Unlock()
	if active == nil {
		return nil
	}
	_ = n.publishEnvelope(active, "leave", "")
	active.Cancel()
	active.Sub.Cancel()
	if err := active.Topic.Close(); err != nil {
		return fmt.Errorf("close topic: %w", err)
	}
	return nil
}

// PublishChat encrypts and publishes a chat message to the active swarm.
func (n *Node) PublishChat(body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	active := n.currentActive()
	if active == nil {
		return fmt.Errorf("no swarm is open")
	}
	if err := n.publishEnvelope(active, "chat", body); err != nil {
		return err
	}
	entry := model.TranscriptEntry{
		ID:           mustID(),
		SwarmID:      active.Swarm.ID,
		Kind:         "chat",
		SenderPeerID: n.host.ID().String(),
		SenderName:   n.identity.Name,
		Body:         body,
		SentAt:       time.Now(),
		Local:        true,
	}
	n.emit(Event{Kind: EventTranscript, Entry: &entry})
	n.touchPresence(active, selfPresence(n.selfPeer()))
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
	_ = n.CloseSwarm()
	n.cancel()
	return n.host.Close()
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
	n.mu.Unlock()
	n.emit(Event{Kind: EventNearby, Nearby: &peerInfo})
}

func (n *Node) fetchPeerCard(ctx context.Context, peerID peer.ID) (wirePeer, error) {
	stream, err := n.host.NewStream(ctx, peerID, cardProtocolID)
	if err != nil {
		return wirePeer{}, fmt.Errorf("open card stream: %w", err)
	}
	defer stream.Close()

	var card wirePeer
	if err := json.NewDecoder(stream).Decode(&card); err != nil {
		return wirePeer{}, fmt.Errorf("decode card: %w", err)
	}
	return card, nil
}

func (n *Node) handleCardStream(stream network.Stream) {
	defer stream.Close()
	_ = json.NewEncoder(stream).Encode(toWirePeer(n.selfPeer()))
}

func (n *Node) handlePairStream(stream network.Stream) {
	defer stream.Close()

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

	requester := fromWirePeer(request.Requester)
	requester.LastSeen = time.Now()
	approvalID := mustID()
	decision := make(chan bool, 1)
	n.mu.Lock()
	n.pending[approvalID] = decision
	n.mu.Unlock()
	n.emit(Event{
		Kind: EventApproval,
		Approval: &Approval{
			ID:        approvalID,
			Direction: "incoming",
			Peer:      requester,
			SwarmName: invite.Swarm.Name,
		},
	})

	accepted, ok := waitForDecision(n.ctx, decision)
	if !ok || !accepted {
		n.bumpInviteUse(invite.Invite.Code, false)
		_ = json.NewEncoder(stream).Encode(pairOffer{Stage: "rejected", Message: "pairing rejected"})
		return
	}

	offer := pairOffer{
		Stage:     "offer",
		Responder: toWirePeer(n.selfPeer()),
		SwarmName: invite.Swarm.Name,
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

	success := pairSuccess{
		Stage: "success",
		Swarm: wireSwarm{
			ID:      invite.Swarm.ID,
			Name:    invite.Swarm.Name,
			RoomKey: invite.Swarm.RoomKey,
		},
		Peer:  toWirePeer(n.selfPeer()),
		Seeds: trustedPeersToWire(invite.Swarm.TrustedPeers),
	}
	if err := json.NewEncoder(stream).Encode(success); err != nil {
		n.bumpInviteUse(invite.Invite.Code, false)
		return
	}

	n.consumeInvite(invite.Invite.Code)
	n.emit(Event{
		Kind: EventPairComplete,
		Pair: &PairResult{
			Direction: "incoming",
			Swarm:     invite.Swarm,
			Peer:      requester,
		},
	})
}

func (n *Node) runOutgoingPair(peerID peer.ID, code string, autoOpen bool) {
	ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
	defer cancel()

	stream, err := n.host.NewStream(ctx, peerID, pairProtocolID)
	if err != nil {
		n.emitSystem(fmt.Sprintf("failed to start pairing with %s: %v", peerID, err))
		return
	}
	defer stream.Close()

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

	responder := fromWirePeer(offer.Responder)
	approvalID := mustID()
	decision := make(chan bool, 1)
	n.mu.Lock()
	n.pending[approvalID] = decision
	n.mu.Unlock()
	n.emit(Event{
		Kind: EventApproval,
		Approval: &Approval{
			ID:        approvalID,
			Direction: "outgoing",
			Peer:      responder,
			SwarmName: offer.SwarmName,
		},
	})

	accepted, ok := waitForDecision(n.ctx, decision)
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
	swarm := model.Swarm{
		ID:      success.Swarm.ID,
		Name:    success.Swarm.Name,
		RoomKey: success.Swarm.RoomKey,
	}
	swarm.TrustedPeers = append(swarm.TrustedPeers, fromWirePeer(success.Peer))
	for _, seed := range success.Seeds {
		swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, fromWirePeer(seed))
	}
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
		bodyBytes, err := yapcrypto.Decrypt(active.Swarm.RoomKey, env.Nonce, env.Ciphertext)
		if err != nil {
			continue
		}
		var payload envelopeBody
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			continue
		}

		switch env.Kind {
		case "chat":
			entry := model.TranscriptEntry{
				ID:           env.ID,
				SwarmID:      active.Swarm.ID,
				Kind:         "chat",
				SenderPeerID: env.SenderPeerID,
				SenderName:   env.SenderName,
				Body:         payload.Body,
				SentAt:       env.SentAt,
			}
			n.emit(Event{Kind: EventTranscript, Entry: &entry})
			n.touchPresence(active, presenceRecord{
				PeerID:   env.SenderPeerID,
				Name:     env.SenderName,
				State:    "online",
				LastSeen: env.SentAt,
			})
		case "join", "leave":
			entry := model.TranscriptEntry{
				ID:           env.ID,
				SwarmID:      active.Swarm.ID,
				Kind:         env.Kind,
				SenderPeerID: env.SenderPeerID,
				SenderName:   env.SenderName,
				Body:         payload.Body,
				SentAt:       env.SentAt,
			}
			n.emit(Event{Kind: EventTranscript, Entry: &entry})
			state := "online"
			if env.Kind == "leave" {
				state = "offline"
			}
			n.touchPresence(active, presenceRecord{
				PeerID:   env.SenderPeerID,
				Name:     env.SenderName,
				State:    state,
				LastSeen: env.SentAt,
			})
		case "heartbeat":
			n.touchPresence(active, presenceRecord{
				PeerID:   env.SenderPeerID,
				Name:     env.SenderName,
				State:    "online",
				LastSeen: env.SentAt,
			})
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
			if err := n.publishEnvelope(active, "heartbeat", ""); err != nil {
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

func (n *Node) publishEnvelope(active *activeSwarm, kind, body string) error {
	payloadBytes, err := json.Marshal(envelopeBody{Body: body})
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	nonce, ciphertext, err := yapcrypto.Encrypt(active.Swarm.RoomKey, payloadBytes)
	if err != nil {
		return err
	}
	id := mustID()
	env := envelope{
		ID:           id,
		Kind:         kind,
		SenderPeerID: n.host.ID().String(),
		SenderName:   n.identity.Name,
		SentAt:       time.Now(),
		Nonce:        nonce,
		Ciphertext:   ciphertext,
	}
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

func (n *Node) connectTrustedPeers(swarm model.Swarm) {
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == n.host.ID().String() {
			continue
		}
		id, err := peer.Decode(trusted.PeerID)
		if err != nil {
			continue
		}
		addrs := make([]ma.Multiaddr, 0, len(trusted.Addrs))
		for _, raw := range trusted.Addrs {
			addr, err := ma.NewMultiaddr(raw)
			if err != nil {
				continue
			}
			addrs = append(addrs, addr)
		}
		if len(addrs) == 0 {
			continue
		}
		info := peer.AddrInfo{ID: id, Addrs: addrs}
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
	if !ok {
		record = &presenceRecord{
			PeerID:      update.PeerID,
			Name:        update.Name,
			Fingerprint: update.Fingerprint,
		}
		active.Presence[update.PeerID] = record
	}
	if update.Name != "" {
		record.Name = update.Name
	}
	if update.Fingerprint != "" {
		record.Fingerprint = update.Fingerprint
	}
	if !update.LastSeen.IsZero() {
		record.LastSeen = update.LastSeen
	}
	if update.State != "" {
		record.State = update.State
	}
	n.mu.Unlock()
	n.emitPresenceSnapshot(active)
}

func (n *Node) refreshPresence(active *activeSwarm) {
	n.mu.Lock()
	now := time.Now()
	for _, record := range active.Presence {
		if record.PeerID == n.host.ID().String() {
			record.State = "online"
			record.LastSeen = now
			continue
		}
		if record.LastSeen.IsZero() {
			record.State = "offline"
			continue
		}
		age := now.Sub(record.LastSeen)
		switch {
		case age >= offlineAfter:
			record.State = "offline"
		case age >= staleAfter:
			record.State = "stale"
		default:
			record.State = "online"
		}
	}
	n.mu.Unlock()
	n.emitPresenceSnapshot(active)
}

func (n *Node) emitPresenceSnapshot(active *activeSwarm) {
	n.mu.RLock()
	presence := make([]model.Presence, 0, len(active.Presence))
	for _, record := range active.Presence {
		presence = append(presence, model.Presence{
			PeerID:      record.PeerID,
			Name:        record.Name,
			Fingerprint: record.Fingerprint,
			State:       record.State,
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
	n.emit(Event{Kind: EventPresence, Presence: presence})
}

func (n *Node) addTrustedPeerAddrs(trusted model.TrustedPeer) {
	id, err := peer.Decode(trusted.PeerID)
	if err != nil {
		return
	}
	var addrs []ma.Multiaddr
	for _, raw := range trusted.Addrs {
		addr, err := ma.NewMultiaddr(raw)
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		return
	}
	n.host.Peerstore().AddAddrs(id, addrs, peerstore.PermanentAddrTTL)
}

func (n *Node) currentActive() *activeSwarm {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.active
}

func (n *Node) emit(event Event) {
	select {
	case n.events <- event:
	case <-n.ctx.Done():
	}
}

func (n *Node) emitSystem(message string) {
	n.emit(Event{Kind: EventSystem, Message: message})
}

func (n *Node) selfPeer() model.TrustedPeer {
	return model.TrustedPeer{
		PeerID:      n.host.ID().String(),
		Name:        n.identity.Name,
		Fingerprint: n.identity.Fingerprint,
		Addrs:       multiaddrStrings(n.host.Addrs()),
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

func (n *Node) consumeInvite(code string) {
	n.mu.Lock()
	delete(n.invites, code)
	n.mu.Unlock()
}

func (n *Node) bumpInviteUse(code string, success bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	invite, ok := n.invites[code]
	if !ok {
		return
	}
	invite.Invite.CurrentUse++
	if success || invite.Invite.CurrentUse >= invite.Invite.MaxUses || time.Now().After(invite.Invite.ExpiresAt) {
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
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.seen[id]; ok {
		return true
	}
	n.seen[id] = struct{}{}
	if len(n.seen) > 4096 {
		n.seen = make(map[string]struct{})
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

func toWirePeer(peerInfo model.TrustedPeer) wirePeer {
	return wirePeer{
		PeerID:      peerInfo.PeerID,
		Name:        peerInfo.Name,
		Fingerprint: peerInfo.Fingerprint,
		Addrs:       append([]string(nil), peerInfo.Addrs...),
	}
}

func fromWirePeer(card wirePeer) model.TrustedPeer {
	return model.TrustedPeer{
		PeerID:      card.PeerID,
		Name:        card.Name,
		Fingerprint: card.Fingerprint,
		Addrs:       append([]string(nil), card.Addrs...),
	}
}

func trustedPeersToWire(peers []model.TrustedPeer) []wirePeer {
	out := make([]wirePeer, 0, len(peers))
	for _, peerInfo := range peers {
		out = append(out, toWirePeer(peerInfo))
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

func multiaddrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.String())
	}
	sort.Strings(out)
	return out
}

func topicName(swarmID string) string {
	return "yap/swarm/" + swarmID + "/v1"
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
	go d.node.inspectNearby(info)
}

var _ mdns.Notifee = (*discoveryNotifee)(nil)
var _ io.Closer = (*Node)(nil)
