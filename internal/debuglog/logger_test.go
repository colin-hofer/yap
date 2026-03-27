package debuglog

import (
	"os"
	"strings"
	"testing"
)

func TestEnableWritesDebugLog(t *testing.T) {
	t.Cleanup(func() {
		_ = Close()
	})

	root := t.TempDir()
	path, err := Enable(root)
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if !strings.HasSuffix(path, "debug.log") {
		t.Fatalf("Enable() path = %q, want debug.log suffix", path)
	}

	Info("hello world", "peer_id", "peer-1")
	if err := Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "hello world") {
		t.Fatalf("debug log missing message:\n%s", content)
	}
	if !strings.Contains(content, "peer-1") {
		t.Fatalf("debug log missing attribute:\n%s", content)
	}
}
