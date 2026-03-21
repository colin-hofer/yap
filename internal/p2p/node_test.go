package p2p

import (
	"strings"
	"testing"
)

func TestListenAddrsDefaultsToTCP(t *testing.T) {
	t.Setenv("YAP_TRANSPORT", "")
	addrs := listenAddrs()
	if len(addrs) != 2 {
		t.Fatalf("len(addrs) = %d, want 2", len(addrs))
	}
	for _, addr := range addrs {
		if want := "/tcp/"; !strings.Contains(addr, want) {
			t.Fatalf("addr %q does not contain %q", addr, want)
		}
	}
}

func TestListenAddrsSupportsExplicitQUIC(t *testing.T) {
	t.Setenv("YAP_TRANSPORT", "quic")
	addrs := listenAddrs()
	if len(addrs) != 2 {
		t.Fatalf("len(addrs) = %d, want 2", len(addrs))
	}
	for _, addr := range addrs {
		if want := "/quic-v1"; !strings.Contains(addr, want) {
			t.Fatalf("addr %q does not contain %q", addr, want)
		}
	}
}
