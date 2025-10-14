package chat

import (
	"net"
	"sync"
)

type PeerManager struct {
	mu    sync.RWMutex
	peers map[string]net.Addr
}

func newPeerManager() *PeerManager {
	return &PeerManager{peers: make(map[string]net.Addr)}
}

func (pm *PeerManager) Add(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	key := addr.String()
	pm.mu.Lock()
	_, existed := pm.peers[key]
	pm.peers[key] = addr
	pm.mu.Unlock()
	return !existed
}

func (pm *PeerManager) Drop(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	key := addr.String()
	pm.mu.Lock()
	_, existed := pm.peers[key]
	delete(pm.peers, key)
	pm.mu.Unlock()
	return existed
}

func (pm *PeerManager) Has(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	pm.mu.RLock()
	_, ok := pm.peers[addr.String()]
	pm.mu.RUnlock()
	return ok
}

func (pm *PeerManager) List(except net.Addr) []net.Addr {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var out []net.Addr
	excluded := ""
	if except != nil {
		excluded = except.String()
	}

	for key, addr := range pm.peers {
		if excluded != "" && key == excluded {
			continue
		}
		out = append(out, addr)
	}
	return out
}

func (pm *PeerManager) Snapshot() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	out := make([]string, 0, len(pm.peers))
	for key := range pm.peers {
		out = append(out, key)
	}
	return out
}
