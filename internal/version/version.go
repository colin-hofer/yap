package version

import (
	"runtime/debug"
	"strings"
)

const (
	RepositoryOwner = "colin-hofer"
	RepositoryName  = "yap"
	BinaryName      = "yap"
)

// Version is intended to be overridden at build time with -ldflags.
var Version = "dev"

// Current returns the best available build version.
func Current() string {
	if value := strings.TrimSpace(Version); value != "" && value != "dev" {
		return value
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	value := strings.TrimSpace(info.Main.Version)
	if value == "" || value == "(devel)" {
		return "dev"
	}
	return value
}
