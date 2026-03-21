package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"yap/internal/model"
)

const transcriptLimit = 1000

// Store manages persisted app state.
type Store struct {
	root string
}

// New constructs a store rooted at the provided directory.
func New(root string) *Store {
	return &Store{root: root}
}

// DefaultRoot returns the default platform-aware state directory.
func DefaultRoot() string {
	if env := strings.TrimSpace(os.Getenv("YAP_DATA_DIR")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); env != "" {
		return filepath.Join(env, "yap")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".yap")
	}
	return filepath.Join(home, ".local", "state", "yap")
}

// Ensure creates the expected directory structure.
func (s *Store) Ensure() error {
	for _, dir := range []string{s.root, s.swarmsDir(), s.transcriptsDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// LoadIdentity reads the local identity if it exists.
func (s *Store) LoadIdentity() (model.Identity, bool, error) {
	var identity model.Identity
	path := s.identityPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return identity, false, nil
		}
		return identity, false, fmt.Errorf("read identity: %w", err)
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return identity, false, fmt.Errorf("decode identity: %w", err)
	}
	return identity, true, nil
}

// SaveIdentity atomically writes the local identity.
func (s *Store) SaveIdentity(identity model.Identity) error {
	return s.writeJSON(s.identityPath(), identity)
}

// SaveSwarm atomically persists a swarm profile.
func (s *Store) SaveSwarm(swarm model.Swarm) error {
	if strings.TrimSpace(swarm.ID) == "" {
		return fmt.Errorf("swarm id cannot be empty")
	}
	return s.writeJSON(s.swarmPath(swarm.ID), swarm)
}

// LoadSwarm reads a persisted swarm by id.
func (s *Store) LoadSwarm(id string) (model.Swarm, error) {
	var swarm model.Swarm
	data, err := os.ReadFile(s.swarmPath(id))
	if err != nil {
		return swarm, fmt.Errorf("read swarm: %w", err)
	}
	if err := json.Unmarshal(data, &swarm); err != nil {
		return swarm, fmt.Errorf("decode swarm: %w", err)
	}
	return swarm, nil
}

// ListSwarms returns all saved swarms ordered by last-opened then name.
func (s *Store) ListSwarms() ([]model.Swarm, error) {
	entries, err := os.ReadDir(s.swarmsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read swarms dir: %w", err)
	}
	swarms := make([]model.Swarm, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		swarm, err := s.LoadSwarm(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		swarms = append(swarms, swarm)
	}
	sort.Slice(swarms, func(i, j int) bool {
		if swarms[i].LastOpened.Equal(swarms[j].LastOpened) {
			return strings.ToLower(swarms[i].Name) < strings.ToLower(swarms[j].Name)
		}
		return swarms[i].LastOpened.After(swarms[j].LastOpened)
	})
	return swarms, nil
}

// LoadTranscript returns the most recent transcript entries for a swarm.
func (s *Store) LoadTranscript(swarmID string) ([]model.TranscriptEntry, error) {
	path := s.transcriptPath(swarmID)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	var entries []model.TranscriptEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry model.TranscriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("decode transcript line: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	if len(entries) > transcriptLimit {
		entries = entries[len(entries)-transcriptLimit:]
	}
	return entries, nil
}

// AppendTranscript appends an entry and compacts the file to the most recent limit.
func (s *Store) AppendTranscript(swarmID string, entry model.TranscriptEntry) error {
	entries, err := s.LoadTranscript(swarmID)
	if err != nil {
		return err
	}
	entries = append(entries, entry)
	if len(entries) > transcriptLimit {
		entries = entries[len(entries)-transcriptLimit:]
	}
	path := s.transcriptPath(swarmID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create transcript dir: %w", err)
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open transcript temp file: %w", err)
	}
	writer := bufio.NewWriter(file)
	for _, item := range entries {
		data, err := json.Marshal(item)
		if err != nil {
			file.Close()
			return fmt.Errorf("encode transcript entry: %w", err)
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			file.Close()
			return fmt.Errorf("write transcript entry: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return fmt.Errorf("flush transcript: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close transcript: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("persist transcript: %w", err)
	}
	return nil
}

func (s *Store) identityPath() string {
	return filepath.Join(s.root, "identity.json")
}

func (s *Store) swarmsDir() string {
	return filepath.Join(s.root, "swarms")
}

func (s *Store) transcriptsDir() string {
	return filepath.Join(s.root, "transcripts")
}

func (s *Store) swarmPath(id string) string {
	return filepath.Join(s.swarmsDir(), id+".json")
}

func (s *Store) transcriptPath(id string) string {
	return filepath.Join(s.transcriptsDir(), id+".jsonl")
}

func (s *Store) writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("persist file: %w", err)
	}
	return nil
}
