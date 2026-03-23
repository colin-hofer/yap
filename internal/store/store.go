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
	"sync"

	"yap/internal/model"
)

const TranscriptLimit = 1000

// Store manages persisted app state.
type Store struct {
	root string
	mu   sync.Mutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadIdentityLocked()
}

func (s *Store) loadIdentityLocked() (model.Identity, bool, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONLocked(s.identityPath(), identity)
}

// SaveSwarm atomically persists a swarm profile.
func (s *Store) SaveSwarm(swarm model.Swarm) error {
	if strings.TrimSpace(swarm.ID) == "" {
		return fmt.Errorf("swarm id cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONLocked(s.swarmPath(swarm.ID), swarm)
}

// LoadSwarm reads a persisted swarm by id.
func (s *Store) LoadSwarm(id string) (model.Swarm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSwarmLocked(id)
}

func (s *Store) loadSwarmLocked(id string) (model.Swarm, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listSwarmsLocked()
}

func (s *Store) listSwarmsLocked() ([]model.Swarm, error) {
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
		swarm, err := s.loadSwarmLocked(strings.TrimSuffix(entry.Name(), ".json"))
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadTranscriptLocked(swarmID)
}

func (s *Store) loadTranscriptLocked(swarmID string) ([]model.TranscriptEntry, error) {
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
	known := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry model.TranscriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("decode transcript line: %w", err)
		}
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		if _, ok := known[entry.ID]; ok {
			continue
		}
		known[entry.ID] = struct{}{}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	sortTranscript(entries)
	if len(entries) > TranscriptLimit {
		entries = entries[len(entries)-TranscriptLimit:]
	}
	return entries, nil
}

// AppendTranscript appends an entry to the transcript journal.
func (s *Store) AppendTranscript(swarmID string, entry model.TranscriptEntry) error {
	if strings.TrimSpace(entry.ID) == "" {
		return fmt.Errorf("transcript entry id cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendTranscriptLocked(s.transcriptPath(swarmID), entry)
}

// MergeTranscript merges transcript entries by ID, keeps them in chronological
// order, and returns the entries that were newly added and retained.
func (s *Store) MergeTranscript(swarmID string, incoming []model.TranscriptEntry) ([]model.TranscriptEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mergeTranscriptLocked(swarmID, incoming)
}

// ReplaceTranscript rewrites a transcript snapshot atomically.
func (s *Store) ReplaceTranscript(swarmID string, entries []model.TranscriptEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeTranscriptLocked(s.transcriptPath(swarmID), entries)
}

func (s *Store) mergeTranscriptLocked(swarmID string, incoming []model.TranscriptEntry) ([]model.TranscriptEntry, error) {
	if len(incoming) == 0 {
		return nil, nil
	}
	existing, err := s.loadTranscriptLocked(swarmID)
	if err != nil {
		return nil, err
	}

	known := make(map[string]struct{}, len(existing))
	for _, entry := range existing {
		known[entry.ID] = struct{}{}
	}

	merged := append([]model.TranscriptEntry(nil), existing...)
	candidates := make([]model.TranscriptEntry, 0, len(incoming))
	for _, entry := range incoming {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		if _, ok := known[entry.ID]; ok {
			continue
		}
		known[entry.ID] = struct{}{}
		merged = append(merged, entry)
		candidates = append(candidates, entry)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sortTranscript(merged)
	if len(merged) > TranscriptLimit {
		merged = merged[len(merged)-TranscriptLimit:]
	}
	if err := s.writeTranscriptLocked(s.transcriptPath(swarmID), merged); err != nil {
		return nil, err
	}

	retained := make(map[string]struct{}, len(merged))
	for _, entry := range merged {
		retained[entry.ID] = struct{}{}
	}
	added := make([]model.TranscriptEntry, 0, len(candidates))
	for _, entry := range candidates {
		if _, ok := retained[entry.ID]; ok {
			added = append(added, entry)
		}
	}
	sortTranscript(added)
	return added, nil
}

// DeleteSwarm removes a persisted swarm profile.
func (s *Store) DeleteSwarm(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.swarmPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete swarm: %w", err)
	}
	return nil
}

// DeleteTranscript removes a persisted transcript file.
func (s *Store) DeleteTranscript(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.transcriptPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete transcript: %w", err)
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

func (s *Store) writeJSONLocked(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return writeFileAtomic(path, 0o600, data)
}

func (s *Store) writeTranscriptLocked(path string, entries []model.TranscriptEntry) error {
	entries = append([]model.TranscriptEntry(nil), entries...)
	sortTranscript(entries)
	if len(entries) > TranscriptLimit {
		entries = entries[len(entries)-TranscriptLimit:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create transcript dir: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("open transcript temp file: %w", err)
	}
	tmp := file.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmp)
		}
	}()
	writer := bufio.NewWriter(file)
	for _, item := range entries {
		data, err := json.Marshal(item)
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("encode transcript entry: %w", err)
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			_ = file.Close()
			return fmt.Errorf("write transcript entry: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush transcript: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync transcript: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close transcript: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("persist transcript: %w", err)
	}
	success = true
	return nil
}

func (s *Store) appendTranscriptLocked(path string, entry model.TranscriptEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create transcript dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open transcript append file: %w", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("encode transcript entry: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("append transcript entry: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync transcript append: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close transcript append file: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, perm os.FileMode, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := file.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := file.Chmod(perm); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("persist file: %w", err)
	}
	success = true
	return nil
}

func sortTranscript(entries []model.TranscriptEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SentAt.Equal(entries[j].SentAt) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].SentAt.Before(entries[j].SentAt)
	})
}
