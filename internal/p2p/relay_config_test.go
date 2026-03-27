package p2p

import (
	"context"
	"crypto/rand"
	"net"
	"testing"

	corecrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	p2pnoise "github.com/libp2p/go-libp2p/p2p/security/noise"
	p2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	ma "github.com/multiformats/go-multiaddr"
)

func TestConfiguredRelayAddrInfoAcceptsFullRelayAddr(t *testing.T) {
	t.Parallel()

	_, relayID := testPeerIdentity(t)

	info, discovered, err := configuredRelayAddrInfo("/dns4/relay.example.com/tcp/4001/p2p/" + relayID.String())
	if err != nil {
		t.Fatalf("configuredRelayAddrInfo() error = %v", err)
	}
	if discovered {
		t.Fatal("configuredRelayAddrInfo() discovered = true, want false")
	}
	if got, want := info.ID.String(), relayID.String(); got != want {
		t.Fatalf("relay peer = %q, want %q", got, want)
	}
	if got, want := len(info.Addrs), 1; got != want {
		t.Fatalf("len(info.Addrs) = %d, want %d", got, want)
	}
	if got, want := info.Addrs[0].String(), "/dns4/relay.example.com/tcp/4001"; got != want {
		t.Fatalf("relay addr = %q, want %q", got, want)
	}
}

func TestConfiguredRelayAddrInfoDiscoversPeerIDFromBareAddr(t *testing.T) {
	t.Parallel()

	raw := ma.StringCast("/dns4/relay.example.com/tcp/4001")
	_, relayID := testPeerIdentity(t)

	original := relayAddrInfoResolver
	t.Cleanup(func() {
		relayAddrInfoResolver = original
	})
	relayAddrInfoResolver = func(_ context.Context, addr ma.Multiaddr) (*peer.AddrInfo, error) {
		return &peer.AddrInfo{
			ID:    relayID,
			Addrs: []ma.Multiaddr{addr},
		}, nil
	}

	info, discovered, err := configuredRelayAddrInfo(raw.String())
	if err != nil {
		t.Fatalf("configuredRelayAddrInfo() error = %v", err)
	}
	if !discovered {
		t.Fatal("configuredRelayAddrInfo() discovered = false, want true")
	}
	if got, want := info.ID, relayID; got != want {
		t.Fatalf("relay peer = %q, want %q", got, want)
	}
	if got, want := len(info.Addrs), 1; got != want {
		t.Fatalf("len(info.Addrs) = %d, want %d", got, want)
	}
	if got, want := info.Addrs[0].String(), raw.String(); got != want {
		t.Fatalf("relay addr = %q, want %q", got, want)
	}
}

func TestConfiguredRelayFromEnvFallsBackToDefaultRelay(t *testing.T) {
	t.Setenv("YAP_RELAY_ADDR", "")

	_, relayID := testPeerIdentity(t)

	original := relayAddrInfoResolver
	t.Cleanup(func() {
		relayAddrInfoResolver = original
	})

	relayAddrInfoResolver = func(_ context.Context, addr ma.Multiaddr) (*peer.AddrInfo, error) {
		if got, want := addr.String(), defaultRelayPublicAddr; got != want {
			t.Fatalf("default relay addr = %q, want %q", got, want)
		}
		return &peer.AddrInfo{
			ID:    relayID,
			Addrs: []ma.Multiaddr{addr},
		}, nil
	}

	cfg, err := configuredRelayFromEnv()
	if err != nil {
		t.Fatalf("configuredRelayFromEnv() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("configuredRelayFromEnv() = nil, want relay config")
	}
	if got, want := cfg.Info.ID.String(), relayID.String(); got != want {
		t.Fatalf("relay peer = %q, want %q", got, want)
	}
	if got, want := len(cfg.Info.Addrs), 1; got != want {
		t.Fatalf("len(cfg.Info.Addrs) = %d, want %d", got, want)
	}
	if got, want := cfg.Info.Addrs[0].String(), defaultRelayPublicAddr; got != want {
		t.Fatalf("relay addr = %q, want %q", got, want)
	}
}

func TestSecureRelayProbeConnAuthenticatesRemotePeer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		security protocol.ID
		inbound  func(context.Context, net.Conn, corecrypto.PrivKey) (secureConn, error)
	}{
		{
			name:     "noise",
			security: p2pnoise.ID,
			inbound: func(ctx context.Context, conn net.Conn, privateKey corecrypto.PrivKey) (secureConn, error) {
				transport, err := p2pnoise.New(p2pnoise.ID, privateKey, nil)
				if err != nil {
					return nil, err
				}
				session, err := transport.WithSessionOptions(p2pnoise.DisablePeerIDCheck())
				if err != nil {
					return nil, err
				}
				return session.SecureInbound(ctx, conn, "")
			},
		},
		{
			name:     "tls",
			security: p2ptls.ID,
			inbound: func(ctx context.Context, conn net.Conn, privateKey corecrypto.PrivKey) (secureConn, error) {
				transport, err := p2ptls.New(p2ptls.ID, privateKey, nil)
				if err != nil {
					return nil, err
				}
				return transport.SecureInbound(ctx, conn, "")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			serverKey, serverID := testPeerIdentity(t)
			clientKey, _ := testPeerIdentity(t)
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()

			serverErr := make(chan error, 1)
			go func() {
				sconn, err := tt.inbound(context.Background(), serverConn, serverKey)
				if err == nil {
					_ = sconn.Close()
				}
				serverErr <- err
			}()

			sconn, err := secureRelayProbeConn(context.Background(), clientConn, tt.security, clientKey)
			if err != nil {
				t.Fatalf("secureRelayProbeConn() error = %v", err)
			}
			if got, want := sconn.RemotePeer(), serverID; got != want {
				t.Fatalf("remote peer = %q, want %q", got, want)
			}
			_ = sconn.Close()

			if err := <-serverErr; err != nil {
				t.Fatalf("server handshake error = %v", err)
			}
		})
	}
}

func testPeerIdentity(t *testing.T) (corecrypto.PrivKey, peer.ID) {
	t.Helper()

	privateKey, publicKey, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key() error = %v", err)
	}
	peerID, err := peer.IDFromPublicKey(publicKey)
	if err != nil {
		t.Fatalf("IDFromPublicKey() error = %v", err)
	}
	return privateKey, peerID
}
