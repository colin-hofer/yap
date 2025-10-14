package chat

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultListen = ":4000"

type Config struct {
	Name   string   `json:"name,omitempty"`
	Listen string   `json:"listen,omitempty"`
	Secret string   `json:"secret,omitempty"`
	Peers  []string `json:"peers,omitempty"`
}

type ConfigStore interface {
	Default() (Config, bool)
	Load(name string) (Config, bool)
	Save(name string, cfg Config) error
	SaveDefault(cfg Config) error
}

type fileConfig struct {
	path string
	mu   sync.Mutex
	data map[string]Config
}

func LoadConfig(path string) (ConfigStore, error) {
	if path == "" {
		return nil, nil
	}

	store := &fileConfig{path: path, data: make(map[string]Config)}

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

type peerList []string

func (p *peerList) String() string {
	return strings.Join(*p, ",")
}

func (p *peerList) Set(value string) error {
	if value == "" {
		return errors.New("peer address cannot be empty")
	}
	*p = append(*p, value)
	return nil
}

func (p peerList) slice() []string {
	return append([]string(nil), p...)
}

func ResolveArgs(args []string) (Config, ConfigStore, error) {
	var peers peerList

	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("name", "", "your chat display name")
	listen := fs.String("listen", "", "UDP address to listen on")
	secret := fs.String("secret", "", "shared secret for end-to-end encryption")
	configPath := fs.String("config", DefaultConfigPath(), "path to yap config file")
	profile := fs.String("group", "", "saved config name to load")
	fs.Var(&peers, "peer", "peer UDP address (repeatable)")

	if err := fs.Parse(args); err != nil {
		return Config{}, nil, err
	}

	store, err := LoadConfig(*configPath)
	if err != nil {
		return Config{}, nil, err
	}

	trimmedProfile := strings.TrimSpace(*profile)
	if store == nil && trimmedProfile != "" {
		return Config{}, nil, fmt.Errorf("group %q requested but config %q not found", trimmedProfile, *configPath)
	}

	base, err := ResolveProfile(store, trimmedProfile)
	if err != nil {
		return Config{}, store, err
	}

	overrides := Config{
		Name:   *name,
		Listen: *listen,
		Secret: *secret,
		Peers:  peers.slice(),
	}
	merged := mergeConfig(base, overrides)
	return applyDefaults(merged), store, nil
}

func ResolveProfile(store ConfigStore, name string) (Config, error) {
	merged := Config{}
	trimmed := strings.TrimSpace(name)

	if store != nil {
		if base, ok := store.Default(); ok {
			merged = mergeConfig(merged, base)
		}
		if trimmed != "" && !strings.EqualFold(trimmed, "default") {
			cfg, ok := store.Load(trimmed)
			if !ok {
				return Config{}, fmt.Errorf("unknown config %q", trimmed)
			}
			merged = mergeConfig(merged, cfg)
		}
	} else if trimmed != "" {
		return Config{}, fmt.Errorf("unknown config %q", trimmed)
	}

	return applyDefaults(merged), nil
}

func (f *fileConfig) Save(name string, cfg Config) error {
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
		Peers:  mergePeers(cfg.Peers),
	}

	return f.persist()
}

func (f *fileConfig) Default() (Config, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.data["default"]
	if !ok {
		return Config{}, false
	}
	return cloneConfig(cfg), true
}

func (f *fileConfig) Load(name string) (Config, bool) {
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

func (f *fileConfig) SaveDefault(cfg Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.data == nil {
		f.data = make(map[string]Config)
	}

	f.data["default"] = Config{
		Name:   cfg.Name,
		Listen: cfg.Listen,
		Secret: cfg.Secret,
		Peers:  mergePeers(cfg.Peers),
	}

	return f.persist()
}

func (f *fileConfig) persist() error {
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

func mergeConfig(base Config, overlay Config) Config {
	merged := Config{}
	merged.Name = firstNonEmpty(overlay.Name, base.Name)
	merged.Listen = firstNonEmpty(overlay.Listen, base.Listen)
	merged.Secret = firstNonEmpty(overlay.Secret, base.Secret)
	merged.Peers = mergePeers(base.Peers, overlay.Peers)
	return merged
}

func cloneConfig(cfg Config) Config {
	return Config{
		Name:   cfg.Name,
		Listen: cfg.Listen,
		Secret: cfg.Secret,
		Peers:  mergePeers(cfg.Peers),
	}
}

func applyDefaults(cfg Config) Config {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Name == "" {
		cfg.Name = DefaultName()
	}
	cfg.Peers = mergePeers(cfg.Peers)
	return cfg
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func mergePeers(parts ...[]string) []string {
	seen := make(map[string]struct{})
	var merged []string
	for _, list := range parts {
		for _, peer := range list {
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

func DefaultName() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return fmt.Sprintf("anon-%d", time.Now().Unix()%1000)
}

func DefaultConfigPath() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, ".yap.json")
}
