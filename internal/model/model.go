package model

import "time"

// Identity is the persisted local device identity.
type Identity struct {
	Name        string `json:"name"`
	PeerID      string `json:"peer_id"`
	PrivateKey  string `json:"private_key"`
	Fingerprint string `json:"fingerprint"`
}

// TrustedPeer represents a peer saved in a swarm profile.
type TrustedPeer struct {
	PeerID      string    `json:"peer_id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Addrs       []string  `json:"addrs,omitempty"`
	LastSeen    time.Time `json:"last_seen,omitempty"`
}

// NearbyPeer represents a currently discoverable peer on the LAN.
type NearbyPeer struct {
	PeerID      string    `json:"peer_id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Addrs       []string  `json:"addrs,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
}

// Invite stores a user-visible invite created for a swarm.
type Invite struct {
	Code       string    `json:"code"`
	SwarmID    string    `json:"swarm_id"`
	SwarmName  string    `json:"swarm_name"`
	ExpiresAt  time.Time `json:"expires_at"`
	MaxUses    int       `json:"max_uses"`
	CurrentUse int       `json:"current_use"`
}

// Swarm is the persisted room profile.
type Swarm struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	RoomKey      string        `json:"room_key"`
	OwnerPeerID  string        `json:"owner_peer_id,omitempty"`
	Version      uint64        `json:"version,omitempty"`
	TrustedPeers []TrustedPeer `json:"trusted_peers,omitempty"`
	LastOpened   time.Time     `json:"last_opened,omitempty"`
}

// TranscriptEntry is a local chat event stored per swarm.
type TranscriptEntry struct {
	ID           string    `json:"id"`
	SwarmID      string    `json:"swarm_id"`
	Kind         string    `json:"kind"`
	SenderPeerID string    `json:"sender_peer_id"`
	SenderName   string    `json:"sender_name"`
	Body         string    `json:"body"`
	SentAt       time.Time `json:"sent_at"`
	Signature    string    `json:"signature,omitempty"`
	Local        bool      `json:"local,omitempty"`
}

// Presence is the UI-facing presence status for a peer in an open swarm.
type Presence struct {
	PeerID      string    `json:"peer_id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Addrs       []string  `json:"addrs,omitempty"`
	State       string    `json:"state"`
	Typing      bool      `json:"typing,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
}
