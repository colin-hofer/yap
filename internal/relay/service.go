package relay

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	corecrypto "github.com/libp2p/go-libp2p/core/crypto"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	defaultServerAddr = ":4001"
	defaultHealthAddr = "127.0.0.1:19081"
)

// Config controls the relay daemon.
type Config struct {
	ServerAddr string
	HealthAddr string
	StateDir   string
	PublicAddr string
}

// ConfigFromEnv loads relay settings from the environment.
func ConfigFromEnv() Config {
	return Config{
		ServerAddr: valueOrEnv("SERVER_ADDR", defaultServerAddr),
		HealthAddr: valueOrEnv("HEALTH_ADDR", defaultHealthAddr),
		StateDir:   strings.TrimSpace(os.Getenv("YAP_RELAY_STATE_DIR")),
		PublicAddr: strings.TrimSpace(os.Getenv("YAP_RELAY_PUBLIC_ADDR")),
	}
}

// Run starts the libp2p relay daemon and serves a local health endpoint.
func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	cfg = cfg.withDefaults()

	privateKey, err := loadOrCreateIdentity(cfg.StateDir)
	if err != nil {
		return err
	}

	listenAddr, err := multiaddrFromHostPort(cfg.ServerAddr)
	if err != nil {
		return err
	}

	opts := []libp2p.Option{
		libp2p.Identity(privateKey),
		libp2p.ListenAddrs(listenAddr),
		libp2p.EnableRelayService(),
		libp2p.EnableNATService(),
		libp2p.ForceReachabilityPublic(),
	}

	publicAddr, err := parsePublicAddr(cfg.PublicAddr)
	if err != nil {
		return err
	}
	if publicAddr != nil {
		opts = append(opts, libp2p.AddrsFactory(func([]ma.Multiaddr) []ma.Multiaddr {
			return []ma.Multiaddr{publicAddr}
		}))
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return fmt.Errorf("create relay host: %w", err)
	}
	defer h.Close()

	healthErr := make(chan error, 1)
	srv := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           healthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		err := srv.ListenAndServe()
		if err == nil || err == http.ErrServerClosed {
			healthErr <- nil
			return
		}
		healthErr <- err
	}()

	relayAddrs := append([]string(nil), multiaddrStrings(h.Addrs())...)
	connectAddr := ""
	if len(relayAddrs) > 0 {
		connectAddr = relayAddrs[0] + "/p2p/" + h.ID().String()
	}

	logger.Info("relay ready",
		slog.String("peer_id", h.ID().String()),
		slog.Any("listen_addrs", relayAddrs),
		slog.String("health_addr", cfg.HealthAddr),
		slog.String("connect_addr", connectAddr),
	)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-healthErr:
		if err != nil {
			return fmt.Errorf("start health server: %w", err)
		}
		return nil
	}
}

func healthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.ServerAddr) == "" {
		c.ServerAddr = defaultServerAddr
	}
	if strings.TrimSpace(c.HealthAddr) == "" {
		c.HealthAddr = defaultHealthAddr
	}
	if strings.TrimSpace(c.StateDir) == "" {
		c.StateDir = defaultStateDir()
	}
	return c
}

func parsePublicAddr(raw string) (ma.Multiaddr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	addr, err := ma.NewMultiaddr(raw)
	if err != nil {
		return nil, fmt.Errorf("parse YAP_RELAY_PUBLIC_ADDR: %w", err)
	}
	return addr, nil
}

func multiaddrFromHostPort(addr string) (ma.Multiaddr, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			host = ""
			port = strings.TrimPrefix(addr, ":")
		} else {
			return nil, fmt.Errorf("parse SERVER_ADDR: %w", err)
		}
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	port = strings.TrimSpace(port)
	if port == "" {
		return nil, fmt.Errorf("SERVER_ADDR must include a port")
	}

	switch {
	case host == "", host == "0.0.0.0":
		return ma.NewMultiaddr("/ip4/0.0.0.0/tcp/" + port)
	case host == "::":
		return ma.NewMultiaddr("/ip6/::/tcp/" + port)
	default:
		ip := net.ParseIP(host)
		if ip == nil {
			return nil, fmt.Errorf("SERVER_ADDR host %q must be an IP address", host)
		}
		if ip.To4() != nil {
			return ma.NewMultiaddr("/ip4/" + host + "/tcp/" + port)
		}
		return ma.NewMultiaddr("/ip6/" + host + "/tcp/" + port)
	}
}

func loadOrCreateIdentity(stateDir string) (corecrypto.PrivKey, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create relay state dir: %w", err)
	}

	path := filepath.Join(stateDir, "identity.key")
	if data, err := os.ReadFile(path); err == nil {
		keyBytes, err := corecrypto.ConfigDecodeKey(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("decode relay identity: %w", err)
		}
		key, err := corecrypto.UnmarshalPrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("unmarshal relay identity: %w", err)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read relay identity: %w", err)
	}

	key, _, err := corecrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate relay identity: %w", err)
	}
	keyBytes, err := corecrypto.MarshalPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal relay identity: %w", err)
	}
	if err := writeFileAtomic(path, []byte(corecrypto.ConfigEncodeKey(keyBytes)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write relay identity: %w", err)
	}
	return key, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "relay-identity-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func defaultStateDir() string {
	if env := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); env != "" {
		return filepath.Join(env, "yap-relay")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".yap-relay")
	}
	return filepath.Join(home, ".local", "state", "yap-relay")
}

func valueOrEnv(key, def string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return def
}

func multiaddrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.String())
	}
	return out
}
