package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultListen = ":4000"

// Config represents chat runtime configuration.
type Config struct {
	Name   string   `json:"name,omitempty"`
	Listen string   `json:"listen,omitempty"`
	Secret string   `json:"secret,omitempty"`
	Peers  []string `json:"peers,omitempty"`
}

// Store provides access to persisted configurations.
type Store interface {
	Default() (Config, bool)
	Load(name string) (Config, bool)
	Save(name string, cfg Config) error
	SaveDefault(cfg Config) error
}

type fileStore struct {
	path string
	mu   sync.Mutex
	data map[string]Config
}

// Load opens or creates a config store at the provided path.
func Load(path string) (Store, error) {
	if path == "" {
		return nil, nil
	}

	store := &fileStore{path: path, data: make(map[string]Config)}

	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(bytes, &store.data); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return store, nil
}

// ResolveProfile merges the default config with a named profile.
func ResolveProfile(store Store, name string) (Config, error) {
	merged := Config{}
	trimmed := strings.TrimSpace(name)

	if store != nil {
		if base, ok := store.Default(); ok {
			merged = Merge(merged, base)
		}
		if trimmed != "" && !strings.EqualFold(trimmed, "default") {
			cfg, ok := store.Load(trimmed)
			if !ok {
				return Config{}, fmt.Errorf("unknown config %q", trimmed)
			}
			merged = Merge(merged, cfg)
		}
	} else if trimmed != "" {
		return Config{}, fmt.Errorf("unknown config %q", trimmed)
	}

	return Normalize(merged), nil
}

// Merge overlays non-zero fields from overlay onto base, deduplicating peers.
func Merge(base, overlay Config) Config {
	result := base
	if overlay.Name != "" {
		result.Name = overlay.Name
	}
	if overlay.Listen != "" {
		result.Listen = overlay.Listen
	}
	if overlay.Secret != "" {
		result.Secret = overlay.Secret
	}
	result.Peers = MergePeers(base.Peers, overlay.Peers)
	return result
}

// Normalize fills in default values and deduplicates peers.
func Normalize(cfg Config) Config {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Name == "" {
		cfg.Name = defaultName()
	}
	cfg.Peers = MergePeers(cfg.Peers)
	return cfg
}

// MergePeers merges peer lists removing duplicates and blanks.
func MergePeers(parts ...[]string) []string {
	seen := make(map[string]struct{})
	var merged []string
	for _, list := range parts {
		for _, peer := range list {
			peer = strings.TrimSpace(peer)
			if peer == "" {
				continue
			}
			if _, ok := seen[peer]; ok {
				continue
			}
			seen[peer] = struct{}{}
			merged = append(merged, peer)
		}
	}
	return merged
}

// Snapshot builds a Config from runtime state.
func Snapshot(name, listen, secret string, lists ...[]string) Config {
	return Config{
		Name:   name,
		Listen: listen,
		Secret: secret,
		Peers:  MergePeers(lists...),
	}
}

// Summary returns human-friendly summary lines for display.
func Summary(cfg Config) []string {
	lines := []string{
		"  name: " + cfg.Name,
		"  listen: " + cfg.Listen,
	}
	if cfg.Secret != "" {
		lines = append(lines, "  encryption: enabled")
	} else {
		lines = append(lines, "  encryption: disabled")
	}
	if len(cfg.Peers) > 0 {
		lines = append(lines, "  peers: "+strings.Join(cfg.Peers, ", "))
	} else {
		lines = append(lines, "  peers: none configured yet")
	}
	return lines
}

// DefaultPath returns the default config file path in the user's home directory.
func DefaultPath() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, ".yap.json")
}

func (f *fileStore) Save(name string, cfg Config) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("config name cannot be empty")
	}
	if strings.EqualFold(trimmed, "default") {
		return errors.New("config name \"default\" is reserved")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.data == nil {
		f.data = make(map[string]Config)
	}

	f.data[trimmed] = Config{
		Name:   cfg.Name,
		Listen: cfg.Listen,
		Secret: cfg.Secret,
		Peers:  MergePeers(cfg.Peers),
	}

	return f.persist()
}

func (f *fileStore) Default() (Config, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.data["default"]
	if !ok {
		return Config{}, false
	}
	return cloneConfig(cfg), true
}

func (f *fileStore) Load(name string) (Config, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return Config{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.data[trimmed]
	if !ok {
		return Config{}, false
	}
	return cloneConfig(cfg), true
}

func (f *fileStore) SaveDefault(cfg Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.data == nil {
		f.data = make(map[string]Config)
	}

	f.data["default"] = Config{
		Name:   cfg.Name,
		Listen: cfg.Listen,
		Secret: cfg.Secret,
		Peers:  MergePeers(cfg.Peers),
	}

	return f.persist()
}

func (f *fileStore) persist() error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	bytes, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	if err := os.Rename(tmp, f.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("persist config: %w", err)
	}

	return nil
}

func cloneConfig(cfg Config) Config {
	return Config{
		Name:   cfg.Name,
		Listen: cfg.Listen,
		Secret: cfg.Secret,
		Peers:  MergePeers(cfg.Peers),
	}
}

func defaultName() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return fmt.Sprintf("anon-%d", time.Now().Unix()%1000)
}
