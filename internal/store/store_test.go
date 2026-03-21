package store

import (
	"fmt"
	"path/filepath"
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

	for i := 0; i < transcriptLimit+25; i++ {
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

	if got, want := len(entries), transcriptLimit; got != want {
		t.Fatalf("len(entries) = %d, want %d", got, want)
	}
	if got, want := entries[0].ID, "msg-0025"; got != want {
		t.Fatalf("first entry id = %q, want %q", got, want)
	}
	if got, want := entries[len(entries)-1].ID, "msg-1024"; got != want {
		t.Fatalf("last entry id = %q, want %q", got, want)
	}
}
