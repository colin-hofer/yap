package membership

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

type Status int

const (
	Pending Status = iota
	Active
)

type Member struct {
	Addr     string
	Name     string
	Status   Status
	LastSeen time.Time
}

type Info struct {
	Addr string `json:"addr"`
	Name string `json:"name,omitempty"`
}

type joinPayload struct {
	Member Info   `json:"member"`
	Peers  []Info `json:"peers,omitempty"`
}

type peersPayload struct {
	Peers []Info `json:"peers,omitempty"`
}

type Manager struct {
	mu        sync.RWMutex
	localAddr string
	localPort uint16
	localIP   netip.Addr
	localName string
	members   map[string]*Member
}

func New(localAddr, localName string) *Manager {
	mgr := &Manager{
		localName: localName,
		members:   make(map[string]*Member),
	}
	mgr.setLocalAddr(localAddr)
	return mgr
}

func (m *Manager) UpdateLocalName(name string) {
	m.mu.Lock()
	m.localName = name
	m.setLocalAddrLocked(m.localAddr)
	m.mu.Unlock()
}

func (m *Manager) setLocalAddr(addr string) {
	m.mu.Lock()
	m.setLocalAddrLocked(addr)
	m.mu.Unlock()
}

func (m *Manager) setLocalAddrLocked(addr string) {
	canon, ok := normalizeAddr(addr, addr)
	if !ok {
		canon = strings.TrimSpace(addr)
	}
	m.localAddr = canon
	m.localIP = netip.Addr{}
	m.localPort = 0
	if ap, err := netip.ParseAddrPort(canon); err == nil {
		m.localIP = ap.Addr()
		m.localPort = ap.Port()
	}
	if canon != "" {
		member := m.members[canon]
		if member == nil {
			member = &Member{Addr: canon}
			m.members[canon] = member
		}
		member.Name = m.localName
		member.Status = Active
		member.LastSeen = time.Now()
	}
}

func (m *Manager) LocalInfo() Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Info{Addr: m.localAddr, Name: m.localName}
}

func (m *Manager) IsLocal(addr string) bool {
	addr, ok := normalizeAddr(addr, addr)
	if !ok {
		addr = strings.TrimSpace(addr)
	}
	m.mu.RLock()
	localAddr := m.localAddr
	localIP := m.localIP
	localPort := m.localPort
	m.mu.RUnlock()
	if addr == "" || localAddr == "" {
		return false
	}
	if addr == localAddr {
		return true
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return false
	}
	if localPort != 0 && ap.Port() != localPort {
		return false
	}
	if !localIP.IsValid() || localIP.IsUnspecified() {
		return true
	}
	if ap.Addr() == localIP {
		return true
	}
	if localIP.IsLoopback() && ap.Addr().IsLoopback() {
		return true
	}
	return false
}

func (m *Manager) AddPending(addr string) bool {
	addr, ok := normalizeAddr(addr, addr)
	if !ok || m.IsLocal(addr) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	member, ok := m.members[addr]
	if !ok {
		m.members[addr] = &Member{Addr: addr, Status: Pending, LastSeen: time.Now()}
		return true
	}
	if member.Status != Pending {
		member.Status = Pending
		member.LastSeen = time.Now()
		return true
	}
	return false
}

func (m *Manager) MarkActive(addr, name string) bool {
	addr, ok := normalizeAddr(addr, addr)
	if !ok || m.IsLocal(addr) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	member, ok := m.members[addr]
	if !ok {
		member = &Member{Addr: addr}
		m.members[addr] = member
	}
	changed := member.Status != Active
	member.Status = Active
	if name != "" {
		member.Name = name
	}
	member.LastSeen = time.Now()
	return changed
}

func (m *Manager) MarkFailed(addr string) bool {
	addr, ok := normalizeAddr(addr, addr)
	if !ok || m.IsLocal(addr) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	member, ok := m.members[addr]
	if !ok {
		return false
	}
	member.Status = Pending
	member.LastSeen = time.Now()
	return true
}

func (m *Manager) Remove(addr string) bool {
	addr, ok := normalizeAddr(addr, addr)
	if !ok || m.IsLocal(addr) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[addr]; !ok {
		return false
	}
	delete(m.members, addr)
	return true
}

func (m *Manager) Has(addr string) bool {
	addr, ok := normalizeAddr(addr, addr)
	if !ok || m.IsLocal(addr) {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.members[addr]
	return exists
}

func (m *Manager) ActiveAddrs(excludes ...string) []string {
	excludeSet := make(map[string]struct{}, len(excludes)+1)
	if m.localAddr != "" {
		excludeSet[m.localAddr] = struct{}{}
	}
	for _, ex := range excludes {
		if norm, ok := normalizeAddr(ex, ex); ok && norm != "" {
			excludeSet[norm] = struct{}{}
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for addr, member := range m.members {
		if member.Status == Active {
			if _, skip := excludeSet[addr]; skip {
				continue
			}
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Manager) PendingAddrs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for addr, member := range m.members {
		if member.Status == Pending {
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Manager) Snapshot() (active []Member, pending []Member) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, member := range m.members {
		copy := *member
		switch member.Status {
		case Active:
			active = append(active, copy)
		case Pending:
			pending = append(pending, copy)
		}
	}
	sortMembers(active)
	sortMembers(pending)
	return active, pending
}

func sortMembers(members []Member) {
	sort.Slice(members, func(i, j int) bool { return members[i].Addr < members[j].Addr })
}

func (m *Manager) BuildJoinPayload() ([]byte, error) {
	payload := joinPayload{
		Member: m.LocalInfo(),
		Peers:  m.activeInfos(""),
	}
	return json.Marshal(payload)
}

func (m *Manager) BuildPeersPayload(exclude string) ([]byte, error) {
	payload := peersPayload{
		Peers: m.activeInfos(exclude),
	}
	return json.Marshal(payload)
}

func (m *Manager) HandleJoin(data []byte, remoteAddr, remoteName string) ([]byte, []string, error) {
	var payload joinPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, err
	}
	addr, ok := normalizeAddr(payload.Member.Addr, remoteAddr)
	if !ok {
		addr = strings.TrimSpace(remoteAddr)
	}
	name := payload.Member.Name
	if name == "" {
		name = remoteName
	}
	if addr != "" && !m.IsLocal(addr) {
		m.MarkActive(addr, name)
	}

	additional := m.collectUnknown(payload.Peers, addr)
	response, err := m.BuildPeersPayload(addr)
	if err != nil {
		return nil, additional, err
	}
	return response, additional, nil
}

func (m *Manager) HandlePeers(data []byte, remoteAddr string) ([]string, error) {
	var payload peersPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	additional := m.collectUnknown(payload.Peers, remoteAddr)
	return additional, nil
}

func (m *Manager) collectUnknown(infos []Info, remote string) []string {
	remoteCanon, okRemote := normalizeAddr(remote, remote)
	var out []string
	for _, info := range infos {
		addr, ok := normalizeAddr(info.Addr, remote)
		if !ok {
			continue
		}
		if (okRemote && addr == remoteCanon) || m.IsLocal(addr) {
			continue
		}
		if m.MarkActive(addr, info.Name) {
			out = append(out, addr)
			continue
		}
		if !m.Has(addr) {
			if m.AddPending(addr) {
				out = append(out, addr)
			}
		}
	}
	return out
}

func (m *Manager) activeInfos(exclude string) []Info {
	exclude = strings.TrimSpace(exclude)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var infos []Info
	for _, member := range m.members {
		if member.Status != Active {
			continue
		}
		if member.Addr == exclude || member.Addr == m.localAddr {
			continue
		}
		infos = append(infos, Info{Addr: member.Addr, Name: member.Name})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Addr < infos[j].Addr })
	return infos
}

func (m *Manager) HintAddrs() []string {
	return m.ActiveAddrs()
}

func normalizeAddr(advertised, fallback string) (string, bool) {
	adv := strings.TrimSpace(advertised)
	fb := strings.TrimSpace(fallback)
	if adv != "" {
		if ap, err := netip.ParseAddrPort(adv); err == nil {
			if ap.Addr().IsUnspecified() && fb != "" {
				if fp, err2 := netip.ParseAddrPort(fb); err2 == nil && !fp.Addr().IsUnspecified() {
					ap = netip.AddrPortFrom(fp.Addr(), ap.Port())
				}
			}
			return ap.String(), true
		}
	}
	if fb != "" {
		if fp, err := netip.ParseAddrPort(fb); err == nil {
			if adv != "" {
				if host, err2 := netip.ParseAddr(adv); err2 == nil {
					ap := netip.AddrPortFrom(host, fp.Port())
					return ap.String(), true
				}
			}
			return fp.String(), true
		}
	}
	if adv != "" {
		return adv, false
	}
	if fb != "" {
		return fb, false
	}
	return "", false
}
