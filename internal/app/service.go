package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	corecrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	yapcrypto "yap/internal/crypto"
	"yap/internal/model"
	"yap/internal/p2p"
	"yap/internal/store"
)

// Options configure the app service on startup.
type Options struct {
	RootDir   string
	OpenSwarm string
	JoinCode  string
}

// EventType classifies events emitted to the UI.
type EventType string

const (
	EventSync     EventType = "sync"
	EventToast    EventType = "toast"
	EventInvite   EventType = "invite"
	EventApproval EventType = "approval"
)

// Event is consumed by the UI layer.
type Event struct {
	Type     EventType
	Snapshot State
	Message  string
	Invite   *model.Invite
	Approval *p2p.Approval
}

// State is a snapshot of UI-visible application state.
type State struct {
	Identity    model.Identity
	Swarms      []model.Swarm
	Nearby      []model.NearbyPeer
	ActiveSwarm *model.Swarm
	Transcript  []model.TranscriptEntry
	Presence    []model.Presence
}

// Service owns persistence and the node lifecycle.
type Service struct {
	ctx    context.Context
	cancel context.CancelFunc

	store *store.Store
	node  *p2p.Node

	mu         sync.RWMutex
	identity   model.Identity
	swarms     []model.Swarm
	nearby     map[string]model.NearbyPeer
	active     *model.Swarm
	transcript []model.TranscriptEntry
	presence   []model.Presence

	events chan Event

	startupOpen   string
	startupJoin   string
	autoJoinTried map[string]struct{}
}

// New creates the app service and starts background event handling.
func New(parent context.Context, opts Options) (*Service, error) {
	ctx, cancel := context.WithCancel(parent)

	root := opts.RootDir
	if strings.TrimSpace(root) == "" {
		root = store.DefaultRoot()
	}
	st := store.New(root)
	if err := st.Ensure(); err != nil {
		cancel()
		return nil, err
	}

	identity, err := loadOrCreateIdentity(st)
	if err != nil {
		cancel()
		return nil, err
	}

	node, err := p2p.New(ctx, identity)
	if err != nil {
		cancel()
		return nil, err
	}

	swarms, err := st.ListSwarms()
	if err != nil {
		node.Close()
		cancel()
		return nil, err
	}

	svc := &Service{
		ctx:           ctx,
		cancel:        cancel,
		store:         st,
		node:          node,
		identity:      identity,
		swarms:        swarms,
		nearby:        make(map[string]model.NearbyPeer),
		events:        make(chan Event, 256),
		startupOpen:   strings.TrimSpace(opts.OpenSwarm),
		startupJoin:   yapcrypto.NormalizeInviteCode(opts.JoinCode),
		autoJoinTried: make(map[string]struct{}),
	}

	for _, peerInfo := range node.NearbySnapshot() {
		svc.nearby[peerInfo.PeerID] = peerInfo
	}

	go svc.consumeNodeEvents()

	if svc.startupOpen != "" {
		if err := svc.OpenSwarm(svc.startupOpen); err != nil {
			svc.emitToast(err.Error())
		}
	} else if svc.startupJoin != "" {
		svc.emitToast(fmt.Sprintf("waiting for an inviter advertising code %s", svc.startupJoin))
	}
	svc.emitSync()
	return svc, nil
}

// Events returns the UI event stream.
func (s *Service) Events() <-chan Event {
	return s.events
}

// Snapshot returns the current state snapshot.
func (s *Service) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

// CreateSwarm creates and persists a new swarm profile.
func (s *Service) CreateSwarm(name string) (model.Swarm, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Swarm{}, fmt.Errorf("swarm name cannot be empty")
	}
	id, err := yapcrypto.RandomID(8)
	if err != nil {
		return model.Swarm{}, err
	}
	roomKey, err := yapcrypto.NewRoomKey()
	if err != nil {
		return model.Swarm{}, err
	}
	self := s.node.Self()
	swarm := model.Swarm{
		ID:           id,
		Name:         name,
		RoomKey:      roomKey,
		TrustedPeers: []model.TrustedPeer{self},
		LastOpened:   time.Now(),
	}
	if err := s.store.SaveSwarm(swarm); err != nil {
		return model.Swarm{}, err
	}
	s.mu.Lock()
	if err := s.reloadSwarmsLocked(); err != nil {
		s.mu.Unlock()
		return model.Swarm{}, err
	}
	s.mu.Unlock()
	s.emitSync()
	return swarm, nil
}

// OpenSwarm loads and joins the selected swarm.
func (s *Service) OpenSwarm(ref string) error {
	swarm, err := s.findSwarm(ref)
	if err != nil {
		return err
	}
	transcript, err := s.store.LoadTranscript(swarm.ID)
	if err != nil {
		return err
	}
	swarm.LastOpened = time.Now()
	swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, s.node.Self())
	if err := s.store.SaveSwarm(swarm); err != nil {
		return err
	}
	if err := s.node.OpenSwarm(swarm); err != nil {
		return err
	}
	s.mu.Lock()
	s.active = &swarm
	s.transcript = transcript
	s.presence = nil
	if err := s.reloadSwarmsLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	s.emitSync()
	return nil
}

// LeaveSwarm leaves the current room and returns to the home state.
func (s *Service) LeaveSwarm() error {
	if err := s.node.CloseSwarm(); err != nil {
		return err
	}
	s.mu.Lock()
	s.active = nil
	s.transcript = nil
	s.presence = nil
	s.mu.Unlock()
	s.emitSync()
	return nil
}

// GenerateInvite creates a new invite for a swarm and notifies the UI.
func (s *Service) GenerateInvite(ref string) (model.Invite, error) {
	swarm, err := s.findSwarm(ref)
	if err != nil {
		return model.Invite{}, err
	}
	invite, err := s.node.CreateInvite(swarm)
	if err != nil {
		return model.Invite{}, err
	}
	select {
	case s.events <- Event{Type: EventInvite, Invite: &invite, Snapshot: s.Snapshot()}:
	case <-s.ctx.Done():
	}
	return invite, nil
}

// StartPair begins pairing with a nearby peer.
func (s *Service) StartPair(peerID, code string, autoOpen bool) error {
	return s.node.PairWithPeer(peerID, code, autoOpen)
}

// ResolveApproval answers a pending approval prompt.
func (s *Service) ResolveApproval(id string, accept bool) error {
	return s.node.ResolveApproval(id, accept)
}

// SendChat publishes a chat message to the active swarm.
func (s *Service) SendChat(body string) error {
	return s.node.PublishChat(body)
}

// Shutdown stops background work and closes the node.
func (s *Service) Shutdown() error {
	s.cancel()
	return s.node.Close()
}

func (s *Service) consumeNodeEvents() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case event := <-s.node.Events():
			s.handleNodeEvent(event)
		}
	}
}

func (s *Service) handleNodeEvent(event p2p.Event) {
	switch event.Kind {
	case p2p.EventNearby:
		if event.Nearby == nil {
			return
		}
		s.mu.Lock()
		s.nearby[event.Nearby.PeerID] = *event.Nearby
		s.mu.Unlock()
		s.maybeAutoJoin(event.Nearby.PeerID)
		s.emitSync()
	case p2p.EventSystem:
		if event.Message != "" {
			s.emitToast(event.Message)
		}
	case p2p.EventApproval:
		select {
		case s.events <- Event{
			Type:     EventApproval,
			Approval: event.Approval,
			Snapshot: s.Snapshot(),
		}:
		case <-s.ctx.Done():
		}
	case p2p.EventPairComplete:
		if event.Pair == nil {
			return
		}
		s.handlePairComplete(*event.Pair, event.AutoOpen)
	case p2p.EventTranscript:
		if event.Entry == nil {
			return
		}
		s.mu.Lock()
		if s.active != nil && s.active.ID == event.Entry.SwarmID {
			s.transcript = append(s.transcript, *event.Entry)
			if len(s.transcript) > 1000 {
				s.transcript = s.transcript[len(s.transcript)-1000:]
			}
		}
		s.mu.Unlock()
		_ = s.store.AppendTranscript(event.Entry.SwarmID, *event.Entry)
		s.emitSync()
	case p2p.EventPresence:
		s.mu.Lock()
		s.presence = append([]model.Presence(nil), event.Presence...)
		if s.active != nil {
			active := *s.active
			updated := false
			for _, presence := range event.Presence {
				if presence.PeerID == "" {
					continue
				}
				peerInfo := model.TrustedPeer{
					PeerID:      presence.PeerID,
					Name:        presence.Name,
					Fingerprint: presence.Fingerprint,
					LastSeen:    presence.LastSeen,
				}
				active.TrustedPeers = mergeTrustedPeer(active.TrustedPeers, peerInfo)
				updated = true
			}
			if updated {
				s.active = &active
				_ = s.store.SaveSwarm(active)
			}
		}
		s.mu.Unlock()
		s.emitSync()
	}
}

func (s *Service) handlePairComplete(result p2p.PairResult, autoOpen bool) {
	switch result.Direction {
	case "incoming":
		swarm, err := s.findSwarm(result.Swarm.ID)
		if err != nil {
			s.emitToast(err.Error())
			return
		}
		swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, result.Peer)
		if err := s.store.SaveSwarm(swarm); err != nil {
			s.emitToast(err.Error())
			return
		}
		s.mu.Lock()
		if s.active != nil && s.active.ID == swarm.ID {
			s.active = &swarm
		}
		_ = s.reloadSwarmsLocked()
		s.mu.Unlock()
		s.emitToast(fmt.Sprintf("%s joined %s", displayName(result.Peer), swarm.Name))
		s.emitSync()
	case "outgoing":
		swarm := result.Swarm
		swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, result.Peer)
		swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, s.node.Self())
		swarm.LastOpened = time.Now()
		if err := s.store.SaveSwarm(swarm); err != nil {
			s.emitToast(err.Error())
			return
		}
		s.mu.Lock()
		_ = s.reloadSwarmsLocked()
		s.mu.Unlock()
		s.emitToast(fmt.Sprintf("paired with %s and saved %s", displayName(result.Peer), swarm.Name))
		if autoOpen || s.startupJoin != "" {
			s.startupJoin = ""
			if err := s.OpenSwarm(swarm.ID); err != nil {
				s.emitToast(err.Error())
			}
			return
		}
		s.emitSync()
	}
}

func (s *Service) maybeAutoJoin(peerID string) {
	if s.startupJoin == "" {
		return
	}
	s.mu.RLock()
	_, tried := s.autoJoinTried[peerID]
	_, ok := s.nearby[peerID]
	s.mu.RUnlock()
	if tried || !ok {
		return
	}
	s.mu.Lock()
	s.autoJoinTried[peerID] = struct{}{}
	s.mu.Unlock()
	if err := s.StartPair(peerID, s.startupJoin, true); err != nil {
		s.emitToast(err.Error())
	}
}

func (s *Service) emitSync() {
	select {
	case s.events <- Event{Type: EventSync, Snapshot: s.Snapshot()}:
	case <-s.ctx.Done():
	}
}

func (s *Service) emitToast(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	select {
	case s.events <- Event{Type: EventToast, Message: message, Snapshot: s.Snapshot()}:
	case <-s.ctx.Done():
	}
}

func (s *Service) snapshotLocked() State {
	nearby := make([]model.NearbyPeer, 0, len(s.nearby))
	for _, peerInfo := range s.nearby {
		nearby = append(nearby, peerInfo)
	}
	sort.Slice(nearby, func(i, j int) bool {
		if nearby[i].LastSeen.Equal(nearby[j].LastSeen) {
			return strings.ToLower(displayNearbyName(nearby[i])) < strings.ToLower(displayNearbyName(nearby[j]))
		}
		return nearby[i].LastSeen.After(nearby[j].LastSeen)
	})

	swarms := append([]model.Swarm(nil), s.swarms...)
	transcript := append([]model.TranscriptEntry(nil), s.transcript...)
	presence := append([]model.Presence(nil), s.presence...)

	var active *model.Swarm
	if s.active != nil {
		copy := *s.active
		active = &copy
	}

	return State{
		Identity:    s.identity,
		Swarms:      swarms,
		Nearby:      nearby,
		ActiveSwarm: active,
		Transcript:  transcript,
		Presence:    presence,
	}
}

func (s *Service) reloadSwarmsLocked() error {
	swarms, err := s.store.ListSwarms()
	if err != nil {
		return err
	}
	s.swarms = swarms
	return nil
}

func (s *Service) findSwarm(ref string) (model.Swarm, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Swarm{}, fmt.Errorf("swarm reference cannot be empty")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, swarm := range s.swarms {
		if swarm.ID == ref || strings.EqualFold(swarm.Name, ref) {
			return swarm, nil
		}
	}
	for _, swarm := range s.swarms {
		if strings.HasPrefix(strings.ToLower(swarm.ID), strings.ToLower(ref)) {
			return swarm, nil
		}
	}
	return model.Swarm{}, fmt.Errorf("swarm %q not found", ref)
}

func loadOrCreateIdentity(st *store.Store) (model.Identity, error) {
	identity, ok, err := st.LoadIdentity()
	if err != nil {
		return model.Identity{}, err
	}
	if ok {
		return identity, nil
	}
	privateKey, publicKey, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return model.Identity{}, fmt.Errorf("generate ed25519 identity: %w", err)
	}
	privateKeyBytes, err := corecrypto.MarshalPrivateKey(privateKey)
	if err != nil {
		return model.Identity{}, fmt.Errorf("marshal private key: %w", err)
	}
	publicKeyBytes, err := corecrypto.MarshalPublicKey(publicKey)
	if err != nil {
		return model.Identity{}, fmt.Errorf("marshal public key: %w", err)
	}
	peerID, err := peer.IDFromPublicKey(publicKey)
	if err != nil {
		return model.Identity{}, fmt.Errorf("derive peer id: %w", err)
	}
	identity = model.Identity{
		Name:        defaultName(),
		PeerID:      peerID.String(),
		PrivateKey:  corecrypto.ConfigEncodeKey(privateKeyBytes),
		Fingerprint: yapcrypto.Fingerprint(publicKeyBytes),
	}
	if err := st.SaveIdentity(identity); err != nil {
		return model.Identity{}, err
	}
	return identity, nil
}

func defaultName() string {
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		return user
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	id, err := yapcrypto.RandomID(4)
	if err != nil {
		return "anon"
	}
	return "anon-" + strings.ToLower(id)
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

func displayName(peerInfo model.TrustedPeer) string {
	if strings.TrimSpace(peerInfo.Name) != "" {
		return peerInfo.Name
	}
	return peerInfo.PeerID
}

func displayNearbyName(peerInfo model.NearbyPeer) string {
	if strings.TrimSpace(peerInfo.Name) != "" {
		return peerInfo.Name
	}
	return peerInfo.PeerID
}
