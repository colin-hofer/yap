package p2p

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	corecrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	p2pnoise "github.com/libp2p/go-libp2p/p2p/security/noise"
	p2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	mss "github.com/multiformats/go-multistream"

	"yap/internal/debuglog"
)

const (
	defaultRelayPublicAddr = "/dns4/colinhofer.com/tcp/4001"
	relayProbeTimeout      = 5 * time.Second
)

var relayAddrInfoResolver = probeRelayAddrInfo

func configuredRelayFromEnv() (*configuredRelay, error) {
	raw := strings.TrimSpace(os.Getenv("YAP_RELAY_ADDR"))
	source := "YAP_RELAY_ADDR"
	if raw == "" {
		raw = defaultRelayPublicAddr
		source = "default"
	}
	if raw == "" {
		debuglog.Info("relay disabled; no relay address configured")
		return nil, nil
	}

	info, discovered, err := configuredRelayAddrInfo(raw)
	if err != nil {
		if source == "default" {
			debuglog.Warn("default relay discovery failed", "relay_addr", raw, "error", err.Error())
			return nil, nil
		}
		return nil, fmt.Errorf("configure relay from %s: %w", source, err)
	}

	fields := []any{
		"relay_peer", info.ID.String(),
		"relay_addrs", multiaddrStrings(info.Addrs),
	}
	if source == "default" {
		fields = append(fields, "source", defaultRelayPublicAddr)
		if discovered {
			debuglog.Info("default relay configured", fields...)
		} else {
			debuglog.Info("default relay override configured", fields...)
		}
	} else {
		if discovered {
			fields = append(fields, "source", source)
			debuglog.Info("relay configured from bare multiaddr", fields...)
		} else {
			debuglog.Info("relay configured", fields...)
		}
	}

	return &configuredRelay{
		Info: *info,
	}, nil
}

func configuredRelayAddrInfo(raw string) (*peer.AddrInfo, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}

	info, err := peer.AddrInfoFromString(raw)
	if err == nil {
		if info == nil || info.ID == "" || len(info.Addrs) == 0 {
			return nil, false, fmt.Errorf("relay address must include a peer id and at least one address")
		}
		return info, false, nil
	}

	addr, addrErr := ma.NewMultiaddr(raw)
	if addrErr != nil {
		return nil, false, fmt.Errorf("parse relay address: %w", addrErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), relayProbeTimeout)
	defer cancel()

	info, err = relayAddrInfoResolver(ctx, addr)
	if err != nil {
		return nil, false, err
	}
	if info == nil || info.ID == "" || len(info.Addrs) == 0 {
		return nil, false, fmt.Errorf("relay probe returned no peer id")
	}
	return info, true, nil
}

func probeRelayAddrInfo(ctx context.Context, addr ma.Multiaddr) (*peer.AddrInfo, error) {
	if addr == nil {
		return nil, fmt.Errorf("relay address is empty")
	}

	insecureConn, err := (&manet.Dialer{}).DialContext(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("dial relay address %s: %w", addr, err)
	}
	defer insecureConn.Close()

	selected, err := mss.SelectOneOf([]protocol.ID{p2pnoise.ID, p2ptls.ID}, insecureConn)
	if err != nil {
		return nil, fmt.Errorf("negotiate relay security: %w", err)
	}

	privateKey, _, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate relay probe identity: %w", err)
	}

	secureConn, err := secureRelayProbeConn(ctx, insecureConn, selected, privateKey)
	if err != nil {
		return nil, err
	}
	defer secureConn.Close()

	return &peer.AddrInfo{
		ID:    secureConn.RemotePeer(),
		Addrs: []ma.Multiaddr{addr},
	}, nil
}

type secureConn interface {
	net.Conn
	RemotePeer() peer.ID
}

func secureRelayProbeConn(ctx context.Context, conn net.Conn, security protocol.ID, privateKey corecrypto.PrivKey) (secureConn, error) {
	switch security {
	case p2pnoise.ID:
		transport, err := p2pnoise.New(p2pnoise.ID, privateKey, nil)
		if err != nil {
			return nil, fmt.Errorf("create noise transport: %w", err)
		}
		session, err := transport.WithSessionOptions(p2pnoise.DisablePeerIDCheck())
		if err != nil {
			return nil, fmt.Errorf("configure noise transport: %w", err)
		}
		secureConn, err := session.SecureOutbound(ctx, conn, "")
		if err != nil {
			return nil, fmt.Errorf("authenticate relay with noise: %w", err)
		}
		return secureConn, nil
	case p2ptls.ID:
		transport, err := p2ptls.New(p2ptls.ID, privateKey, nil)
		if err != nil {
			return nil, fmt.Errorf("create tls transport: %w", err)
		}
		secureConn, err := transport.SecureOutbound(ctx, conn, "")
		if err != nil {
			return nil, fmt.Errorf("authenticate relay with tls: %w", err)
		}
		return secureConn, nil
	default:
		return nil, fmt.Errorf("unsupported relay security protocol %q", security)
	}
}
