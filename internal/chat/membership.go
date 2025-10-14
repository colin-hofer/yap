package chat

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type status int

const (
	statusPending status = iota
	statusActive
)

type member struct {
	Addr     string
	Name     string
	Status   status
	LastSeen time.Time
	endpoint netip.AddrPort
}

// String returns the canonical address label for the member.
func (m *member) String() string {
	if m == nil {
		return ""
	}
	return m.Addr
}

// info converts the member into a lightweight advertising payload.
func (m *member) Info() memberInfo {
	if m == nil {
		return memberInfo{}
	}
	return memberInfo{Addr: m.Addr, Name: m.Name}
}

// Payload aliases Info to make intent explicit at call sites.
func (m *member) Payload() memberInfo {
	return m.Info()
}

// AddrPort returns the last known UDP endpoint for the member, if any.
func (m *member) AddrPort() (netip.AddrPort, bool) {
	if m == nil || !m.endpoint.IsValid() {
		return netip.AddrPort{}, false
	}
	return m.endpoint, true
}

// SetAddrPort records the member's reachable UDP endpoint.
func (m *member) SetAddrPort(ap netip.AddrPort) {
	if m == nil || !ap.IsValid() {
		return
	}
	m.endpoint = ap
}

// ClearAddrPort forgets the member's cached UDP endpoint.
func (m *member) ClearAddrPort() {
	if m == nil {
		return
	}
	m.endpoint = netip.AddrPort{}
}

type memberInfo struct {
	Addr string `json:"addr"`
	Name string `json:"name,omitempty"`
}

type memberEndpoint struct {
	key string
	ap  netip.AddrPort
}

type joinPayload struct {
	Member memberInfo   `json:"member"`
	Peers  []memberInfo `json:"peers,omitempty"`
}

type peersPayload struct {
	Peers []memberInfo `json:"peers,omitempty"`
}

// resetMembership reinitialises the member map and refreshes the local entry.
func (s *session) resetMembership(localAddr string) {
	if s == nil {
		return
	}
	s.membersMu.Lock()
	s.members = make(map[string]*member)
	s.setLocalAddrLocked(localAddr)
	s.membersMu.Unlock()
}

// setLocalAddr updates the local advertised address and member record.
func (s *session) setLocalAddr(addr string) {
	if s == nil {
		return
	}
	s.membersMu.Lock()
	if s.members == nil {
		s.members = make(map[string]*member)
	}
	s.setLocalAddrLocked(addr)
	s.membersMu.Unlock()
}

// refreshLocalIdentity reapplies the local member metadata after config changes.
func (s *session) refreshLocalIdentity() {
	if s == nil {
		return
	}
	s.membersMu.Lock()
	if s.members == nil {
		s.members = make(map[string]*member)
	}
	s.setLocalAddrLocked(s.localAddr)
	s.membersMu.Unlock()
}

// setLocalAddrLocked normalises and stores the local address under lock.
func (s *session) setLocalAddrLocked(addr string) {
	canon, ok := normalizeAddr(addr, addr)
	if !ok {
		canon = strings.TrimSpace(addr)
	}

	s.localAddr = canon
	s.localIP = netip.Addr{}
	s.localPort = 0
	var parsed netip.AddrPort
	if ap, err := netip.ParseAddrPort(canon); err == nil {
		s.localIP = ap.Addr()
		s.localPort = ap.Port()
		parsed = ap
	}

	if canon == "" || s.members == nil {
		return
	}

	rec := s.members[canon]
	if rec == nil {
		rec = &member{Addr: canon}
		s.members[canon] = rec
	}
	rec.Name = s.cfg.Name
	rec.Status = statusActive
	rec.LastSeen = time.Now()
	if parsed.IsValid() {
		rec.SetAddrPort(parsed)
	}
}

// localInfo builds an Info payload for the local participant.
func (s *session) localInfo() memberInfo {
	if s == nil {
		return memberInfo{}
	}
	s.membersMu.RLock()
	addr := s.localAddr
	name := s.cfg.Name
	s.membersMu.RUnlock()
	return memberInfo{Addr: addr, Name: name}
}

// isLocal reports whether the provided address resolves to this session.
func (s *session) isLocal(raw string) bool {
	if s == nil {
		return false
	}
	addr, ok := normalizeAddr(raw, raw)
	if !ok {
		addr = strings.TrimSpace(raw)
	}
	s.membersMu.RLock()
	localAddr := s.localAddr
	localIP := s.localIP
	localPort := s.localPort
	s.membersMu.RUnlock()
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

// addPendingMember records a member hint in the pending state.
func (s *session) addPendingMember(raw string) bool {
	if s == nil || s.isLocal(raw) {
		return false
	}
	addr, ok := normalizeAddr(raw, raw)
	if !ok {
		addr = strings.TrimSpace(raw)
	}
	s.membersMu.Lock()
	defer s.membersMu.Unlock()
	if s.members == nil {
		s.members = make(map[string]*member)
	}
	rec, ok := s.members[addr]
	if !ok {
		s.members[addr] = &member{Addr: addr, Status: statusPending, LastSeen: time.Now()}
		return true
	}
	if rec.Status != statusPending {
		rec.Status = statusPending
		rec.LastSeen = time.Now()
		return true
	}
	return false
}

// markMemberActive transitions a member into the active set, updating metadata.
func (s *session) markMemberActive(raw, name string) bool {
	if s == nil || s.isLocal(raw) {
		return false
	}
	addr, ok := normalizeAddr(raw, raw)
	if !ok {
		addr = strings.TrimSpace(raw)
	}
	s.membersMu.Lock()
	defer s.membersMu.Unlock()
	if s.members == nil {
		s.members = make(map[string]*member)
	}
	rec := s.members[addr]
	if rec == nil {
		rec = &member{Addr: addr}
		s.members[addr] = rec
	}
	rec.Addr = addr
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		rec.SetAddrPort(ap)
	}
	changed := rec.Status != statusActive
	rec.Status = statusActive
	if name != "" {
		rec.Name = name
	}
	rec.LastSeen = time.Now()
	return changed
}

// setMemberEndpoint caches the last reachable UDP endpoint for a member.
func (s *session) setMemberEndpoint(addr string, ap netip.AddrPort) {
	addr = strings.TrimSpace(addr)
	if s == nil || !ap.IsValid() || addr == "" {
		return
	}
	s.membersMu.Lock()
	if s.members == nil {
		s.members = make(map[string]*member)
	}
	rec := s.members[addr]
	if rec == nil {
		rec = &member{Addr: addr}
		s.members[addr] = rec
	} else {
		rec.Addr = addr
	}
	rec.SetAddrPort(ap)
	s.membersMu.Unlock()
}

// markMemberFailed marks a member as pending after a delivery failure.
func (s *session) markMemberFailed(raw string) bool {
	if s == nil || s.isLocal(raw) {
		return false
	}
	addr, ok := normalizeAddr(raw, raw)
	if !ok {
		addr = strings.TrimSpace(raw)
	}
	s.membersMu.Lock()
	defer s.membersMu.Unlock()
	if s.members == nil {
		s.members = make(map[string]*member)
	}
	rec, ok := s.members[addr]
	if !ok {
		return false
	}
	rec.Status = statusPending
	rec.LastSeen = time.Now()
	rec.ClearAddrPort()
	return true
}

// removeMember deletes a member from the map entirely.
func (s *session) removeMember(raw string) bool {
	if s == nil || s.isLocal(raw) {
		return false
	}
	addr, ok := normalizeAddr(raw, raw)
	if !ok {
		addr = strings.TrimSpace(raw)
	}
	s.membersMu.Lock()
	defer s.membersMu.Unlock()
	if s.members == nil {
		s.members = make(map[string]*member)
	}
	if _, ok := s.members[addr]; !ok {
		return false
	}
	delete(s.members, addr)
	return true
}

// hasMember reports whether the member is known to the session.
func (s *session) hasMember(raw string) bool {
	if s == nil || s.isLocal(raw) {
		return false
	}
	addr, ok := normalizeAddr(raw, raw)
	if !ok {
		addr = strings.TrimSpace(raw)
	}
	s.membersMu.RLock()
	defer s.membersMu.RUnlock()
	_, exists := s.members[addr]
	return exists
}

// activeAddrs returns sorted active peer addresses, ignoring exclusions.
func (s *session) activeAddrs(excludes ...string) []string {
	if s == nil {
		return nil
	}
	excludeSet := make(map[string]struct{}, len(excludes)+1)
	for _, ex := range excludes {
		if norm, ok := normalizeAddr(ex, ex); ok && norm != "" {
			excludeSet[norm] = struct{}{}
		}
	}
	s.membersMu.RLock()
	local := s.localAddr
	if local != "" {
		excludeSet[local] = struct{}{}
	}
	var out []string
	for addr, member := range s.members {
		if member.Status == statusActive {
			if _, skip := excludeSet[addr]; skip {
				continue
			}
			out = append(out, addr)
		}
	}
	s.membersMu.RUnlock()
	sort.Strings(out)
	return out
}

// activeEndpoints returns active peers with cached endpoints suitable for send.
func (s *session) activeEndpoints(exclude string) []memberEndpoint {
	if s == nil {
		return nil
	}
	exclude = strings.TrimSpace(exclude)
	s.membersMu.RLock()
	defer s.membersMu.RUnlock()
	local := s.localAddr
	var targets []memberEndpoint
	for key, member := range s.members {
		if member.Status != statusActive {
			continue
		}
		if key == exclude || key == local {
			continue
		}
		if ap, ok := member.AddrPort(); ok {
			targets = append(targets, memberEndpoint{key: key, ap: ap})
		}
	}
	return targets
}

// pendingAddrs returns sorted addresses currently in the pending state.
func (s *session) pendingAddrs() []string {
	if s == nil {
		return nil
	}
	s.membersMu.RLock()
	var out []string
	for addr, member := range s.members {
		if member.Status == statusPending {
			out = append(out, addr)
		}
	}
	s.membersMu.RUnlock()
	sort.Strings(out)
	return out
}

// membersSnapshot copies active and pending members for presentation.
func (s *session) membersSnapshot() (active []member, pending []member) {
	if s == nil {
		return nil, nil
	}
	s.membersMu.RLock()
	for _, member := range s.members {
		copy := *member
		switch member.Status {
		case statusActive:
			active = append(active, copy)
		case statusPending:
			pending = append(pending, copy)
		}
	}
	s.membersMu.RUnlock()
	sortMembers(active)
	sortMembers(pending)
	return active, pending
}

// sortMembers orders members by canonical address.
func sortMembers(members []member) {
	sort.Slice(members, func(i, j int) bool { return members[i].Addr < members[j].Addr })
}

// buildJoinPayloadData encodes the join payload describing this peer.
func (s *session) buildJoinPayloadData() ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	payload := joinPayload{
		Member: s.localInfo(),
		Peers:  s.activeInfos(""),
	}
	return json.Marshal(payload)
}

// buildPeersPayloadData encodes a peer list excluding the provided address.
func (s *session) buildPeersPayloadData(exclude string) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	payload := peersPayload{
		Peers: s.activeInfos(exclude),
	}
	return json.Marshal(payload)
}

// processJoinPayload updates membership from a join message and prepares a response.
func (s *session) processJoinPayload(data []byte, remoteAddr, remoteName string) ([]byte, []string, error) {
	if s == nil {
		return nil, nil, nil
	}
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
	if addr != "" && !s.isLocal(addr) {
		s.markMemberActive(addr, name)
	}

	additional := s.collectUnknown(payload.Peers, addr)
	response, err := s.buildPeersPayloadData(addr)
	if err != nil {
		return nil, additional, err
	}
	return response, additional, nil
}

// processPeersPayload integrates a peers message and returns new contacts to pursue.
func (s *session) processPeersPayload(data []byte, remoteAddr string) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	var payload peersPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	additional := s.collectUnknown(payload.Peers, remoteAddr)
	return additional, nil
}

// collectUnknown records any peers we have not seen and returns addresses to contact.
func (s *session) collectUnknown(infos []memberInfo, remote string) []string {
	if s == nil {
		return nil
	}
	remoteCanon, okRemote := normalizeAddr(remote, remote)
	var out []string
	for _, info := range infos {
		addr, ok := normalizeAddr(info.Addr, remote)
		if !ok {
			continue
		}
		if (okRemote && addr == remoteCanon) || s.isLocal(addr) {
			continue
		}
		if s.markMemberActive(addr, info.Name) {
			out = append(out, addr)
			continue
		}
		if !s.hasMember(addr) {
			if s.addPendingMember(addr) {
				out = append(out, addr)
			}
		}
	}
	return out
}

// activeInfos produces Info payloads for the active membership, excluding the target.
func (s *session) activeInfos(exclude string) []memberInfo {
	if s == nil {
		return nil
	}
	exclude = strings.TrimSpace(exclude)
	s.membersMu.RLock()
	local := s.localAddr
	var infos []memberInfo
	for _, member := range s.members {
		if member.Status != statusActive {
			continue
		}
		if member.Addr == exclude || member.Addr == local {
			continue
		}
		infos = append(infos, member.Info())
	}
	s.membersMu.RUnlock()
	sort.Slice(infos, func(i, j int) bool { return infos[i].Addr < infos[j].Addr })
	return infos
}

// hintAddrs exposes active addresses as connection hints.
func (s *session) hintAddrs() []string {
	return s.activeAddrs()
}

// normalizeAddr canonicalises a possibly incomplete advertised address.
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
