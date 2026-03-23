package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"yap/internal/model"
)

func TestAppendTranscriptCompactsToLimit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := New(root)
	if err := store.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	for i := 0; i < TranscriptLimit+25; i++ {
		entry := model.TranscriptEntry{
			ID:         fmt.Sprintf("msg-%04d", i),
			SwarmID:    "swarm-1",
			Kind:       "chat",
			SenderName: "tester",
			Body:       fmt.Sprintf("body-%04d", i),
			SentAt:     time.Unix(int64(i), 0),
		}
		if err := store.AppendTranscript("swarm-1", entry); err != nil {
			t.Fatalf("AppendTranscript() error = %v", err)
		}
	}

	entries, err := store.LoadTranscript("swarm-1")
	if err != nil {
		t.Fatalf("LoadTranscript() error = %v", err)
	}

	if got, want := len(entries), TranscriptLimit; got != want {
		t.Fatalf("len(entries) = %d, want %d", got, want)
	}
	if got, want := entries[0].ID, "msg-0025"; got != want {
		t.Fatalf("first entry id = %q, want %q", got, want)
	}
	if got, want := entries[len(entries)-1].ID, "msg-1024"; got != want {
		t.Fatalf("last entry id = %q, want %q", got, want)
	}
}

func TestMergeTranscriptDedupesAndSorts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := New(root)
	if err := store.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	base := []model.TranscriptEntry{
		{ID: "b", SwarmID: "swarm-1", Kind: "chat", SenderName: "tester", Body: "two", SentAt: time.Unix(20, 0)},
		{ID: "a", SwarmID: "swarm-1", Kind: "chat", SenderName: "tester", Body: "one", SentAt: time.Unix(10, 0)},
	}
	for _, entry := range base {
		if err := store.AppendTranscript("swarm-1", entry); err != nil {
			t.Fatalf("AppendTranscript() error = %v", err)
		}
	}

	added, err := store.MergeTranscript("swarm-1", []model.TranscriptEntry{
		{ID: "a", SwarmID: "swarm-1", Kind: "chat", SenderName: "tester", Body: "duplicate", SentAt: time.Unix(10, 0)},
		{ID: "c", SwarmID: "swarm-1", Kind: "chat", SenderName: "tester", Body: "three", SentAt: time.Unix(15, 0)},
	})
	if err != nil {
		t.Fatalf("MergeTranscript() error = %v", err)
	}

	if got, want := len(added), 1; got != want {
		t.Fatalf("len(added) = %d, want %d", got, want)
	}
	if got, want := added[0].ID, "c"; got != want {
		t.Fatalf("added[0].ID = %q, want %q", got, want)
	}

	entries, err := store.LoadTranscript("swarm-1")
	if err != nil {
		t.Fatalf("LoadTranscript() error = %v", err)
	}
	if got, want := len(entries), 3; got != want {
		t.Fatalf("len(entries) = %d, want %d", got, want)
	}
	if got, want := entries[0].ID, "a"; got != want {
		t.Fatalf("entries[0].ID = %q, want %q", got, want)
	}
	if got, want := entries[1].ID, "c"; got != want {
		t.Fatalf("entries[1].ID = %q, want %q", got, want)
	}
	if got, want := entries[2].ID, "b"; got != want {
		t.Fatalf("entries[2].ID = %q, want %q", got, want)
	}
}

func TestDeleteSwarmAndTranscript(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := New(root)
	if err := store.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	swarm := model.Swarm{ID: "swarm-1", Name: "one"}
	if err := store.SaveSwarm(swarm); err != nil {
		t.Fatalf("SaveSwarm() error = %v", err)
	}
	if err := store.AppendTranscript("swarm-1", model.TranscriptEntry{
		ID: "entry-1", SwarmID: "swarm-1", Kind: "chat", SenderName: "tester", Body: "hi", SentAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatalf("AppendTranscript() error = %v", err)
	}

	if err := store.DeleteSwarm("swarm-1"); err != nil {
		t.Fatalf("DeleteSwarm() error = %v", err)
	}
	if err := store.DeleteTranscript("swarm-1"); err != nil {
		t.Fatalf("DeleteTranscript() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "swarms", "swarm-1.json")); !os.IsNotExist(err) {
		t.Fatalf("swarm file exists or wrong error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "transcripts", "swarm-1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("transcript file exists or wrong error: %v", err)
	}
}

func TestStoreSerializesConcurrentTranscriptWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := New(root)
	if err := store.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	const writers = 32
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			entry := model.TranscriptEntry{
				ID:         fmt.Sprintf("msg-%02d", i),
				SwarmID:    "swarm-1",
				Kind:       "chat",
				SenderName: "tester",
				Body:       fmt.Sprintf("body-%02d", i),
				SentAt:     time.Unix(int64(i), 0),
			}
			if i%2 == 0 {
				if err := store.AppendTranscript("swarm-1", entry); err != nil {
					t.Errorf("AppendTranscript() error = %v", err)
				}
				return
			}
			if _, err := store.MergeTranscript("swarm-1", []model.TranscriptEntry{entry}); err != nil {
				t.Errorf("MergeTranscript() error = %v", err)
			}
		}()
	}
	wg.Wait()

	entries, err := store.LoadTranscript("swarm-1")
	if err != nil {
		t.Fatalf("LoadTranscript() error = %v", err)
	}
	if got, want := len(entries), writers; got != want {
		t.Fatalf("len(entries) = %d, want %d", got, want)
	}
}
