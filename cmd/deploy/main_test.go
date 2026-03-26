package main

import (
	"strings"
	"testing"
)

func TestSystemdUnitIncludesRelayEnvironment(t *testing.T) {
	unit := systemdUnit(
		"yap-relay",
		slotBlue,
		"127.0.0.1:18081",
		"127.0.0.1:19081",
		"/dns4/colinhofer.com/tcp/4001",
		"/var/lib/yap-relay/state",
	)

	if !strings.Contains(unit, "EnvironmentFile=-/etc/yap-relay/config.env") {
		t.Fatalf("systemdUnit() missing EnvironmentFile:\n%s", unit)
	}
	if !strings.Contains(unit, "Environment=HEALTH_ADDR=127.0.0.1:19081") {
		t.Fatalf("systemdUnit() missing HEALTH_ADDR:\n%s", unit)
	}
	if !strings.Contains(unit, "Environment=YAP_RELAY_PUBLIC_ADDR=/dns4/colinhofer.com/tcp/4001") {
		t.Fatalf("systemdUnit() missing YAP_RELAY_PUBLIC_ADDR:\n%s", unit)
	}
}

func TestHAProxyConfigUsesPublicAndTargetAddr(t *testing.T) {
	cfg := haproxyConfig(":4001", "127.0.0.1:18081")

	if !strings.Contains(cfg, "bind :4001") {
		t.Fatalf("haproxyConfig() missing bind:\n%s", cfg)
	}
	if !strings.Contains(cfg, "server active 127.0.0.1:18081 check") {
		t.Fatalf("haproxyConfig() missing backend target:\n%s", cfg)
	}
}

func TestTCPProbeCommandUsesLoopbackForWildcardBind(t *testing.T) {
	cmd, err := tcpProbeCommand(":4001")
	if err != nil {
		t.Fatalf("tcpProbeCommand() error = %v", err)
	}
	if !strings.Contains(cmd, "/dev/tcp/127.0.0.1/4001") {
		t.Fatalf("tcpProbeCommand() = %q, want loopback probe", cmd)
	}
}
