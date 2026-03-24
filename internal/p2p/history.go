package p2p

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"yap/internal/model"
	"yap/internal/store"
)

type historyRequest struct {
	SwarmID string                `json:"swarm_id"`
	Entries []wireTranscriptEntry `json:"entries,omitempty"`
}

type historyResponse struct {
	Error   string                `json:"error,omitempty"`
	Entries []wireTranscriptEntry `json:"entries,omitempty"`
}

type wireTranscriptEntry struct {
	ID           string    `json:"id"`
	SwarmID      string    `json:"swarm_id"`
	Kind         string    `json:"kind"`
	SenderPeerID string    `json:"sender_peer_id"`
	SenderName   string    `json:"sender_name"`
	Body         string    `json:"body"`
	SentAt       time.Time `json:"sent_at"`
	Signature    string    `json:"signature,omitempty"`
}

func (n *Node) handleHistoryStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(historyStreamTimeout))

	var request historyRequest
	if err := json.NewDecoder(stream).Decode(&request); err != nil {
		_ = json.NewEncoder(stream).Encode(historyResponse{Error: "invalid history request"})
		return
	}
	request.SwarmID = strings.TrimSpace(request.SwarmID)
	if request.SwarmID == "" {
		_ = json.NewEncoder(stream).Encode(historyResponse{Error: "swarm id required"})
		return
	}

	remotePeer := stream.Conn().RemotePeer()
	swarm, ok := n.loadTrustedSwarm(request.SwarmID, remotePeer)
	if !ok {
		_ = json.NewEncoder(stream).Encode(historyResponse{Error: "peer is not trusted for swarm"})
		return
	}

	localEntries, err := n.store.LoadTranscript(request.SwarmID)
	if err != nil {
		_ = json.NewEncoder(stream).Encode(historyResponse{Error: "failed to load transcript"})
		return
	}
	request.Entries = clampWireTranscriptEntries(request.Entries)
	remoteEntries := n.verifiedTranscriptEntries(swarm, request.Entries, n.host.ID().String())
	added, err := n.store.MergeTranscript(request.SwarmID, remoteEntries)
	if err != nil {
		_ = json.NewEncoder(stream).Encode(historyResponse{Error: "failed to merge transcript"})
		return
	}
	if len(added) > 0 {
		n.emit(Event{Kind: EventHistory, SwarmID: request.SwarmID, Entries: added})
	}

	response := historyResponse{
		Entries: n.toWireTranscriptEntries(diffTranscriptEntries(localEntries, remoteEntries)),
	}
	_ = json.NewEncoder(stream).Encode(response)
}

func (n *Node) syncSwarm(active *activeSwarm) {
	if active == nil {
		return
	}
	select {
	case <-active.Context.Done():
		return
	default:
	}
	for _, trusted := range active.Swarm.TrustedPeers {
		if trusted.PeerID == n.host.ID().String() {
			continue
		}
		n.syncSwarmPeer(active.Swarm, trusted)
	}
}

func (n *Node) syncSwarmPeer(swarm model.Swarm, trusted model.TrustedPeer) {
	if !n.shouldSyncHistory(swarm.ID, trusted.PeerID) {
		return
	}
	peerID, err := peer.Decode(trusted.PeerID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	defer cancel()

	stream, err := n.host.NewStream(ctx, peerID, historyProtocolID)
	if err != nil {
		return
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(historyStreamTimeout))

	localEntries, err := n.store.LoadTranscript(swarm.ID)
	if err != nil {
		return
	}
	request := historyRequest{
		SwarmID: swarm.ID,
		Entries: n.toWireTranscriptEntries(localEntries),
	}
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		return
	}

	var response historyResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		return
	}
	if strings.TrimSpace(response.Error) != "" {
		return
	}

	response.Entries = clampWireTranscriptEntries(response.Entries)
	remoteEntries := n.verifiedTranscriptEntries(swarm, response.Entries, n.host.ID().String())
	added, err := n.store.MergeTranscript(swarm.ID, remoteEntries)
	if err != nil || len(added) == 0 {
		return
	}
	n.emit(Event{Kind: EventHistory, SwarmID: swarm.ID, Entries: added})
}

func (n *Node) shouldSyncHistory(swarmID, peerID string) bool {
	key := swarmID + "/" + peerID
	n.mu.Lock()
	defer n.mu.Unlock()
	last, ok := n.historySync[key]
	if ok && time.Since(last) < historyCooldown {
		return false
	}
	n.historySync[key] = time.Now()
	return true
}

func (n *Node) loadTrustedSwarm(swarmID string, peerID peer.ID) (model.Swarm, bool) {
	swarm, err := n.store.LoadSwarm(swarmID)
	if err != nil {
		return model.Swarm{}, false
	}
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == peerID.String() {
			return swarm, true
		}
	}
	return model.Swarm{}, false
}

func diffTranscriptEntries(source, other []model.TranscriptEntry) []model.TranscriptEntry {
	known := make(map[string]struct{}, len(other))
	for _, entry := range other {
		known[entry.ID] = struct{}{}
	}
	out := make([]model.TranscriptEntry, 0, len(source))
	for _, entry := range source {
		if _, ok := known[entry.ID]; ok {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (n *Node) toWireTranscriptEntries(entries []model.TranscriptEntry) []wireTranscriptEntry {
	if len(entries) > store.TranscriptLimit {
		entries = entries[len(entries)-store.TranscriptLimit:]
	}
	selfPeerID := ""
	if n != nil && n.host != nil {
		selfPeerID = n.host.ID().String()
	}
	out := make([]wireTranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		if !allowedTranscriptKind(entry.Kind) {
			continue
		}
		body, ok := sanitizeChatBody(entry.Body)
		if entry.Kind == "chat" && !ok {
			continue
		}
		signature := strings.TrimSpace(entry.Signature)
		if signature == "" && selfPeerID != "" && entry.SenderPeerID == selfPeerID {
			signed := entry
			signed.Body = body
			signed.Signature, _ = n.signTranscriptEntry(signed)
			signature = strings.TrimSpace(signed.Signature)
		}
		if signature == "" {
			continue
		}
		entry.Body = body
		entry.Signature = signature
		if n != nil && !n.verifyTranscriptEntrySignature(entry) {
			continue
		}
		out = append(out, wireTranscriptEntry{
			ID:           entry.ID,
			SwarmID:      entry.SwarmID,
			Kind:         entry.Kind,
			SenderPeerID: entry.SenderPeerID,
			SenderName:   sanitizeDisplayName(entry.SenderName),
			Body:         body,
			SentAt:       clampEventTime(entry.SentAt),
			Signature:    signature,
		})
	}
	return out
}

func (n *Node) verifiedTranscriptEntries(swarm model.Swarm, entries []wireTranscriptEntry, localPeerID string) []model.TranscriptEntry {
	out := make([]model.TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		swarmID := strings.TrimSpace(entry.SwarmID)
		senderPeerID := strings.TrimSpace(entry.SenderPeerID)
		if strings.TrimSpace(entry.ID) == "" || swarmID != swarm.ID || !allowedTranscriptKind(entry.Kind) {
			continue
		}
		if !swarmHasTrustedPeerID(swarm, senderPeerID) {
			continue
		}
		body, ok := sanitizeChatBody(entry.Body)
		if !ok && entry.Kind == "chat" {
			continue
		}
		item := model.TranscriptEntry{
			ID:           entry.ID,
			SwarmID:      swarmID,
			Kind:         entry.Kind,
			SenderPeerID: senderPeerID,
			SenderName:   sanitizeDisplayName(entry.SenderName),
			Body:         body,
			SentAt:       clampEventTime(entry.SentAt),
			Signature:    strings.TrimSpace(entry.Signature),
			Local:        senderPeerID == localPeerID,
		}
		if !n.verifyTranscriptEntrySignature(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func clampWireTranscriptEntries(entries []wireTranscriptEntry) []wireTranscriptEntry {
	if len(entries) > store.TranscriptLimit {
		return entries[len(entries)-store.TranscriptLimit:]
	}
	return entries
}

func allowedTranscriptKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "chat", "join", "leave", "rename":
		return true
	default:
		return false
	}
}

func swarmHasTrustedPeerID(swarm model.Swarm, peerID string) bool {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return false
	}
	for _, trusted := range swarm.TrustedPeers {
		if trusted.PeerID == peerID {
			return true
		}
	}
	return false
}
