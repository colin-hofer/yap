package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRelayDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.env")
	if err := os.WriteFile(path, []byte("DEPLOY_HOST=root@example.com\nYAP_RELAY_PUBLIC_ADDR=/dns4/example.com/tcp/4001\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got, want := cfg.AppName, "yap-relay"; got != want {
		t.Fatalf("AppName = %q, want %q", got, want)
	}
	if got, want := cfg.BuildPackage, "./cmd/yap-relay"; got != want {
		t.Fatalf("BuildPackage = %q, want %q", got, want)
	}
	if got, want := cfg.BuildWorkdir, "."; got != want {
		t.Fatalf("BuildWorkdir = %q, want %q", got, want)
	}
	if got, want := cfg.PublicAddr, ":4001"; got != want {
		t.Fatalf("PublicAddr = %q, want %q", got, want)
	}
	if got, want := cfg.RelayStateDir, "/var/lib/yap-relay/state"; got != want {
		t.Fatalf("RelayStateDir = %q, want %q", got, want)
	}
}

func TestLoadCustomRelayFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.env")
	contents := "" +
		"APP_NAME=test-relay\n" +
		"DEPLOY_HOST=root@example.com\n" +
		"BUILD_WORKDIR=.\n" +
		"BUILD_PACKAGE=./cmd/custom-relay\n" +
		"PUBLIC_ADDR=:5001\n" +
		"BLUE_HEALTH_ADDR=127.0.0.1:29081\n" +
		"GREEN_HEALTH_ADDR=127.0.0.1:29082\n" +
		"YAP_RELAY_PUBLIC_ADDR=/dns4/example.com/tcp/5001\n" +
		"YAP_RELAY_STATE_DIR=/srv/test-relay/state\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got, want := cfg.AppName, "test-relay"; got != want {
		t.Fatalf("AppName = %q, want %q", got, want)
	}
	if got, want := cfg.BuildPackage, "./cmd/custom-relay"; got != want {
		t.Fatalf("BuildPackage = %q, want %q", got, want)
	}
	if got, want := cfg.PublicAddr, ":5001"; got != want {
		t.Fatalf("PublicAddr = %q, want %q", got, want)
	}
	if got, want := cfg.BlueHealthAddr, "127.0.0.1:29081"; got != want {
		t.Fatalf("BlueHealthAddr = %q, want %q", got, want)
	}
	if got, want := cfg.RelayPublicAddr, "/dns4/example.com/tcp/5001"; got != want {
		t.Fatalf("RelayPublicAddr = %q, want %q", got, want)
	}
	if got, want := cfg.RelayStateDir, "/srv/test-relay/state"; got != want {
		t.Fatalf("RelayStateDir = %q, want %q", got, want)
	}
}
