package chat

import (
	"net"
	"net/netip"
	"strings"
)

// canonicalAddrPort returns a best-effort canonicalised UDP address using optional fallback.
func canonicalAddrPort(advertised, fallback string) (netip.AddrPort, bool) {
	adv := strings.TrimSpace(advertised)
	fb := strings.TrimSpace(fallback)
	if adv != "" {
		if ap, err := netip.ParseAddrPort(adv); err == nil {
			if ap.Addr().IsUnspecified() && fb != "" {
				if fbPort, err := netip.ParseAddrPort(fb); err == nil {
					ap = netip.AddrPortFrom(fbPort.Addr(), ap.Port())
				}
			}
			return ap, true
		}
	}
	if fb != "" {
		if ap, err := netip.ParseAddrPort(fb); err == nil {
			return ap, true
		}
	}
	return netip.AddrPort{}, false
}

// addrPortFromNet converts a net.Addr into a canonicalised AddrPort.
func addrPortFromNet(addr net.Addr) (netip.AddrPort, bool) {
	if addr == nil {
		return netip.AddrPort{}, false
	}
	switch v := addr.(type) {
	case *net.UDPAddr:
		if v == nil || v.Port == 0 {
			break
		}
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return netip.AddrPortFrom(ip, uint16(v.Port)), true
		}
	}
	ap, err := netip.ParseAddrPort(strings.TrimSpace(addr.String()))
	if err != nil {
		return netip.AddrPort{}, false
	}
	return ap, true
}

// formatAddrPort renders an AddrPort as the canonical string, returning "" if invalid.
func formatAddrPort(ap netip.AddrPort) string {
	if !ap.IsValid() {
		return ""
	}
	return ap.String()
}

// canonicalAddrString normalises a string address without observing the network.
func canonicalAddrString(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if ap, ok := canonicalAddrPort(addr, addr); ok {
		return ap.String()
	}
	return addr
}

// canonicalNetAddr derives a canonical string representation from a net.Addr.
func canonicalNetAddr(addr net.Addr) string {
	if ap, ok := addrPortFromNet(addr); ok {
		return formatAddrPort(ap)
	}
	if addr == nil {
		return ""
	}
	return canonicalAddrString(addr.String())
}
