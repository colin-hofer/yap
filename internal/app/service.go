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

// SwarmSummary is the UI-facing view of a saved swarm.
type SwarmSummary struct {
	Swarm        model.Swarm
	Connected    bool
	Unread       int
	LastActivity time.Time
}

// State is a snapshot of UI-visible application state.
type State struct {
	Version       uint64
	Identity      model.Identity
	Swarms        []SwarmSummary
	Nearby        []model.NearbyPeer
	SelectedSwarm *model.Swarm
	Transcript    []model.TranscriptEntry
	Presence      []model.Presence
}

// Service owns persistence and the node lifecycle.
type Service struct {
	ctx    context.Context
	cancel context.CancelFunc

	store *store.Store
	node  *p2p.Node

	mu            sync.RWMutex
	identity      model.Identity
	swarms        []model.Swarm
	nearby        map[string]model.NearbyPeer
	selectedID    string
	transcripts   map[string][]model.TranscriptEntry
	transcriptLag map[string]int
	presence      map[string][]model.Presence
	unread        map[string]int
	connected     map[string]bool
	lastActivity  map[string]time.Time
	version       uint64

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

	node, err := p2p.New(ctx, identity, st)
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
		transcripts:   make(map[string][]model.TranscriptEntry),
		transcriptLag: make(map[string]int),
		presence:      make(map[string][]model.Presence),
		unread:        make(map[string]int),
		connected:     make(map[string]bool),
		lastActivity:  make(map[string]time.Time),
		events:        make(chan Event, 256),
		startupOpen:   strings.TrimSpace(opts.OpenSwarm),
		startupJoin:   yapcrypto.NormalizeInviteCode(opts.JoinCode),
		autoJoinTried: make(map[string]struct{}),
	}

	for _, swarm := range swarms {
		transcript, err := st.LoadTranscript(swarm.ID)
		if err != nil {
			node.Close()
			cancel()
			return nil, err
		}
		svc.transcripts[swarm.ID] = transcript
		svc.lastActivity[swarm.ID] = activityTime(swarm, transcript)
	}

	for _, peerInfo := range node.NearbySnapshot() {
		svc.nearby[peerInfo.PeerID] = peerInfo
	}

	go svc.consumeNodeEvents()

	for _, swarm := range swarms {
		if err := svc.node.OpenSwarm(swarm); err != nil {
			svc.emitToast(fmt.Sprintf("failed to connect %s: %v", swarm.Name, err))
			continue
		}
		svc.mu.Lock()
		svc.connected[swarm.ID] = true
		svc.mu.Unlock()
	}

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

func (s *Service) nextSnapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
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
	s.transcripts[swarm.ID] = nil
	s.transcriptLag[swarm.ID] = 0
	s.lastActivity[swarm.ID] = swarm.LastOpened
	if err := s.reloadSwarmsLocked(); err != nil {
		s.mu.Unlock()
		return model.Swarm{}, err
	}
	s.mu.Unlock()
	s.emitSync()
	return swarm, nil
}

// RenameIdentity updates the local display name and persists it.
func (s *Service) RenameIdentity(name string) error {
	name = normalizeIdentityName(name)
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	s.mu.RLock()
	identity := s.identity
	swarms := append([]model.Swarm(nil), s.swarms...)
	s.mu.RUnlock()

	identity.Name = name
	self := model.TrustedPeer{
		PeerID:      identity.PeerID,
		Name:        identity.Name,
		Fingerprint: identity.Fingerprint,
	}
	updatedSwarms := make([]model.Swarm, 0, len(swarms))
	for _, swarm := range swarms {
		swarm.TrustedPeers = mergeTrustedPeer(swarm.TrustedPeers, self)
		updatedSwarms = append(updatedSwarms, swarm)
	}

	if err := s.store.SaveIdentity(identity); err != nil {
		return err
	}
	for _, swarm := range updatedSwarms {
		if err := s.store.SaveSwarm(swarm); err != nil {
			return err
		}
	}

	s.node.UpdateIdentityName(identity.Name)

	s.mu.Lock()
	s.identity = identity
	s.swarms = updatedSwarms
	s.mu.Unlock()
	s.emitSync()
	return nil
}

// OpenSwarm selects a swarm and ensures it is connected in the background.
func (s *Service) OpenSwarm(ref string) error {
	swarm, err := s.findSwarm(ref)
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
	s.selectedID = swarm.ID
	s.connected[swarm.ID] = true
	s.unread[swarm.ID] = 0
	if _, ok := s.transcripts[swarm.ID]; !ok {
		transcript, err := s.store.LoadTranscript(swarm.ID)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		s.transcripts[swarm.ID] = transcript
	}
	if err := s.reloadSwarmsLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	s.emitSync()
	return nil
}

// LeaveSwarm clears the current selection and returns to the home state.
func (s *Service) LeaveSwarm() error {
	_ = s.markSelectedSwarmRead(time.Now())
	s.mu.Lock()
	s.selectedID = ""
	s.mu.Unlock()
	s.emitSync()
	return nil
}

// RemoveSwarm forgets a saved swarm locally and deletes its transcript.
func (s *Service) RemoveSwarm(ref string) error {
	swarm, err := s.findSwarm(ref)
	if err != nil {
		return err
	}
	if err := s.node.CloseSwarm(swarm.ID); err != nil {
		return err
	}
	if err := s.store.DeleteTranscript(swarm.ID); err != nil {
		return err
	}
	if err := s.store.DeleteSwarm(swarm.ID); err != nil {
		return err
	}

	s.mu.Lock()
	delete(s.transcripts, swarm.ID)
	delete(s.transcriptLag, swarm.ID)
	delete(s.presence, swarm.ID)
	delete(s.unread, swarm.ID)
	delete(s.connected, swarm.ID)
	delete(s.lastActivity, swarm.ID)
	if s.selectedID == swarm.ID {
		s.selectedID = ""
	}
	if err := s.reloadSwarmsLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
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
	case s.events <- Event{Type: EventInvite, Invite: &invite, Snapshot: s.nextSnapshot()}:
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

// SendChat publishes a chat message to the selected swarm.
func (s *Service) SendChat(body string) error {
	s.mu.RLock()
	selectedID := s.selectedID
	s.mu.RUnlock()
	if strings.TrimSpace(selectedID) == "" {
		return fmt.Errorf("no swarm selected")
	}
	return s.node.PublishChat(selectedID, body)
}

// NotifyTyping publishes ephemeral typing state for the selected swarm.
func (s *Service) NotifyTyping(active bool) {
	s.mu.RLock()
	selectedID := s.selectedID
	s.mu.RUnlock()
	if strings.TrimSpace(selectedID) == "" {
		return
	}
	_ = s.node.PublishTyping(selectedID, active)
}

// Shutdown stops background work and closes the node.
func (s *Service) Shutdown() error {
	_ = s.markSelectedSwarmRead(time.Now())
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
	case p2p.EventNearbySnapshot:
		s.mu.Lock()
		s.nearby = make(map[string]model.NearbyPeer, len(event.NearbyPeers))
		for _, peerInfo := range event.NearbyPeers {
			s.nearby[peerInfo.PeerID] = peerInfo
		}
		s.mu.Unlock()
		for _, peerInfo := range event.NearbyPeers {
			s.maybeAutoJoin(peerInfo.PeerID)
		}
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
			Snapshot: s.nextSnapshot(),
		}:
		case <-s.ctx.Done():
		}
	case p2p.EventPairComplete:
		if event.Pair == nil {
			return
		}
		s.handlePairComplete(*event.Pair, event.AutoOpen)
	case p2p.EventTranscript:
		if event.Entry != nil {
			s.handleTranscriptEvent(*event.Entry, true)
		}
	case p2p.EventHistory:
		s.handleHistoryImport(event.SwarmID, event.Entries)
	case p2p.EventPresence:
		s.handlePresenceEvent(event.SwarmID, event.Presence)
	}
}

func (s *Service) handleTranscriptEvent(entry model.TranscriptEntry, persist bool) {
	s.mu.Lock()
	if s.transcriptLag == nil {
		s.transcriptLag = make(map[string]int)
	}
	merged, added := mergeTranscriptEntries(s.transcripts[entry.SwarmID], []model.TranscriptEntry{entry})
	s.transcripts[entry.SwarmID] = merged
	shouldCompact := false
	if len(added) > 0 {
		s.lastActivity[entry.SwarmID] = activityTimeForEntries(s.lastActivity[entry.SwarmID], added)
		if len(merged) >= store.TranscriptLimit {
			nextLag := s.transcriptLag[entry.SwarmID] + 1
			if nextLag >= 64 {
				shouldCompact = true
				nextLag = 0
			}
			s.transcriptLag[entry.SwarmID] = nextLag
		} else {
			s.transcriptLag[entry.SwarmID] = 0
		}
	}
	selected := s.selectedID == entry.SwarmID
	swarmName := s.swarmNameLocked(entry.SwarmID)
	mentioned := s.entryMentionsIdentityLocked(entry)
	if !selected && len(added) > 0 && entry.Kind == "chat" {
		s.unread[entry.SwarmID] += len(added)
	}
	s.mu.Unlock()

	if persist && len(added) > 0 {
		var err error
		if shouldCompact {
			err = s.store.ReplaceTranscript(entry.SwarmID, merged)
		} else {
			err = s.store.AppendTranscript(entry.SwarmID, entry)
		}
		if err != nil {
			s.emitToast(err.Error())
		}
	}
	if !selected && entry.Kind == "chat" && swarmName != "" {
		if mentioned {
			s.emitToast(fmt.Sprintf("mention in %s from %s", swarmName, entry.SenderName))
			return
		}
		s.emitToast(fmt.Sprintf("new message in %s from %s", swarmName, entry.SenderName))
		return
	}
	s.emitSync()
}

func (s *Service) handleHistoryImport(swarmID string, entries []model.TranscriptEntry) {
	if strings.TrimSpace(swarmID) == "" || len(entries) == 0 {
		return
	}
	s.mu.Lock()
	if s.transcriptLag == nil {
		s.transcriptLag = make(map[string]int)
	}
	merged, added := mergeTranscriptEntries(s.transcripts[swarmID], entries)
	if len(added) == 0 {
		s.mu.Unlock()
		return
	}
	s.transcripts[swarmID] = merged
	s.transcriptLag[swarmID] = 0
	s.lastActivity[swarmID] = activityTimeForEntries(s.lastActivity[swarmID], added)
	if s.selectedID != swarmID {
		s.unread[swarmID] += countChatEntries(added)
	}
	s.mu.Unlock()
	s.emitSync()
}

func (s *Service) handlePresenceEvent(swarmID string, presence []model.Presence) {
	if strings.TrimSpace(swarmID) == "" {
		return
	}
	s.mu.Lock()
	s.presence[swarmID] = append([]model.Presence(nil), presence...)
	s.mu.Unlock()
	s.emitSync()
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
		if err := s.node.OpenSwarm(swarm); err != nil {
			s.emitToast(err.Error())
		}
		s.mu.Lock()
		s.connected[swarm.ID] = true
		s.replaceSwarmLocked(swarm)
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
		if err := s.node.OpenSwarm(swarm); err != nil {
			s.emitToast(err.Error())
		}

		s.mu.Lock()
		if s.transcriptLag == nil {
			s.transcriptLag = make(map[string]int)
		}
		if _, ok := s.transcripts[swarm.ID]; !ok {
			s.transcripts[swarm.ID] = nil
		}
		s.transcriptLag[swarm.ID] = 0
		s.lastActivity[swarm.ID] = swarm.LastOpened
		s.connected[swarm.ID] = true
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
	case s.events <- Event{Type: EventSync, Snapshot: s.nextSnapshot()}:
	case <-s.ctx.Done():
	}
}

func (s *Service) emitToast(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	select {
	case s.events <- Event{Type: EventToast, Message: message, Snapshot: s.nextSnapshot()}:
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

	swarms := make([]SwarmSummary, 0, len(s.swarms))
	for _, swarm := range s.swarms {
		lastActivity := s.lastActivity[swarm.ID]
		if lastActivity.IsZero() {
			lastActivity = swarm.LastOpened
		}
		swarms = append(swarms, SwarmSummary{
			Swarm:        swarm,
			Connected:    s.connected[swarm.ID],
			Unread:       s.unread[swarm.ID],
			LastActivity: lastActivity,
		})
	}
	sort.Slice(swarms, func(i, j int) bool {
		if swarms[i].LastActivity.Equal(swarms[j].LastActivity) {
			if swarms[i].Unread == swarms[j].Unread {
				return strings.ToLower(swarms[i].Swarm.Name) < strings.ToLower(swarms[j].Swarm.Name)
			}
			return swarms[i].Unread > swarms[j].Unread
		}
		return swarms[i].LastActivity.After(swarms[j].LastActivity)
	})

	var selected *model.Swarm
	if strings.TrimSpace(s.selectedID) != "" {
		if swarm, ok := s.findSwarmLocked(s.selectedID); ok {
			copy := swarm
			selected = &copy
		}
	}

	transcript := append([]model.TranscriptEntry(nil), s.transcripts[s.selectedID]...)
	presence := append([]model.Presence(nil), s.presence[s.selectedID]...)

	return State{
		Version:       s.version,
		Identity:      s.identity,
		Swarms:        swarms,
		Nearby:        nearby,
		SelectedSwarm: selected,
		Transcript:    transcript,
		Presence:      presence,
	}
}

func (s *Service) markSelectedSwarmRead(seenAt time.Time) error {
	s.mu.RLock()
	selectedID := s.selectedID
	s.mu.RUnlock()
	if strings.TrimSpace(selectedID) == "" {
		return nil
	}

	s.mu.Lock()
	swarm, ok := s.findSwarmLocked(selectedID)
	if !ok {
		s.mu.Unlock()
		return nil
	}
	if !seenAt.After(swarm.LastOpened) {
		seenAt = time.Now()
	}
	swarm.LastOpened = seenAt
	s.unread[selectedID] = 0
	s.replaceSwarmLocked(swarm)
	s.mu.Unlock()
	return s.store.SaveSwarm(swarm)
}

func (s *Service) entryMentionsIdentityLocked(entry model.TranscriptEntry) bool {
	if entry.Local || entry.Kind != "chat" {
		return false
	}
	handle := mentionToken(s.identity.Name, s.identity.PeerID)
	if handle == "" {
		return false
	}
	return mentionsHandle(entry.Body, handle)
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
	swarm, ok := s.findSwarmLocked(ref)
	if !ok {
		return model.Swarm{}, fmt.Errorf("swarm %q not found", ref)
	}
	return swarm, nil
}

func (s *Service) findSwarmLocked(ref string) (model.Swarm, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Swarm{}, false
	}
	for _, swarm := range s.swarms {
		if swarm.ID == ref || strings.EqualFold(swarm.Name, ref) {
			return swarm, true
		}
	}
	for _, swarm := range s.swarms {
		if strings.HasPrefix(strings.ToLower(swarm.ID), strings.ToLower(ref)) {
			return swarm, true
		}
	}
	return model.Swarm{}, false
}

func (s *Service) replaceSwarmLocked(updated model.Swarm) {
	for i, swarm := range s.swarms {
		if swarm.ID == updated.ID {
			s.swarms[i] = updated
			return
		}
	}
	s.swarms = append(s.swarms, updated)
}

func (s *Service) swarmNameLocked(id string) string {
	for _, swarm := range s.swarms {
		if swarm.ID == id {
			return swarm.Name
		}
	}
	return ""
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

func normalizeIdentityName(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > 64 {
		value = string(runes[:64])
	}
	return value
}

func activityTime(swarm model.Swarm, transcript []model.TranscriptEntry) time.Time {
	last := swarm.LastOpened
	for _, entry := range transcript {
		if entry.SentAt.After(last) {
			last = entry.SentAt
		}
	}
	return last
}

func activityTimeForEntries(current time.Time, entries []model.TranscriptEntry) time.Time {
	last := current
	for _, entry := range entries {
		if entry.SentAt.After(last) {
			last = entry.SentAt
		}
	}
	return last
}

func mergeTranscriptEntries(existing, incoming []model.TranscriptEntry) ([]model.TranscriptEntry, []model.TranscriptEntry) {
	known := make(map[string]struct{}, len(existing))
	merged := append([]model.TranscriptEntry(nil), existing...)
	for _, entry := range existing {
		known[entry.ID] = struct{}{}
	}

	added := make([]model.TranscriptEntry, 0, len(incoming))
	for _, entry := range incoming {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		if _, ok := known[entry.ID]; ok {
			continue
		}
		known[entry.ID] = struct{}{}
		merged = append(merged, entry)
		added = append(added, entry)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].SentAt.Equal(merged[j].SentAt) {
			return merged[i].ID < merged[j].ID
		}
		return merged[i].SentAt.Before(merged[j].SentAt)
	})
	if len(merged) > store.TranscriptLimit {
		merged = merged[len(merged)-store.TranscriptLimit:]
	}
	retained := make(map[string]struct{}, len(merged))
	for _, entry := range merged {
		retained[entry.ID] = struct{}{}
	}
	filtered := make([]model.TranscriptEntry, 0, len(added))
	for _, entry := range added {
		if _, ok := retained[entry.ID]; ok {
			filtered = append(filtered, entry)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].SentAt.Equal(filtered[j].SentAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].SentAt.Before(filtered[j].SentAt)
	})
	return merged, filtered
}

func countChatEntries(entries []model.TranscriptEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Kind == "chat" {
			count++
		}
	}
	return count
}

func mentionToken(name, peerID string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return strings.ToLower(shortID(peerID))
	}
	var b strings.Builder
	dashed := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dashed = false
		case r == '-' || r == '_':
			if !dashed && b.Len() > 0 {
				b.WriteRune(r)
				dashed = true
			}
		default:
			if !dashed && b.Len() > 0 {
				b.WriteByte('-')
				dashed = true
			}
		}
	}
	handle := strings.Trim(b.String(), "-_")
	if handle == "" {
		return strings.ToLower(shortID(peerID))
	}
	return handle
}

func mentionsHandle(body, handle string) bool {
	if handle == "" {
		return false
	}
	for _, token := range strings.Fields(body) {
		if !strings.HasPrefix(token, "@") {
			continue
		}
		candidate := strings.Trim(strings.TrimPrefix(token, "@"), ".,:;!?()[]{}<>\"'")
		if strings.EqualFold(candidate, handle) {
			return true
		}
	}
	return false
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}

func trustedPeersEqual(left, right []model.TrustedPeer) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].PeerID != right[i].PeerID ||
			left[i].Name != right[i].Name ||
			left[i].Fingerprint != right[i].Fingerprint ||
			!left[i].LastSeen.Equal(right[i].LastSeen) ||
			!stringSlicesEqual(left[i].Addrs, right[i].Addrs) {
			return false
		}
	}
	return true
}

func stringSlicesEqual(left, right []string) bool {
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
