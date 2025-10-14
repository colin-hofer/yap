package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"yap/internal/config"
)

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

func (c *CLI) resolveArgs(args []string) (config.Config, config.Store, error) {
	var peers peerList

	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(c.stderr())

	name := fs.String("name", "", "your chat display name")
	listen := fs.String("listen", "", "UDP address to listen on")
	secret := fs.String("secret", "", "shared secret for end-to-end encryption")
	configPath := fs.String("config", config.DefaultPath(), "path to yap config file")
	profile := fs.String("group", "", "saved config name to load")
	fs.Var(&peers, "peer", "peer UDP address (repeatable)")

	if err := fs.Parse(args); err != nil {
		return config.Config{}, nil, err
	}

	store, err := config.Load(*configPath)
	if err != nil {
		return config.Config{}, nil, err
	}

	trimmedProfile := strings.TrimSpace(*profile)
	if store == nil && trimmedProfile != "" {
		return config.Config{}, nil, fmt.Errorf("group %q requested but config %q not found", trimmedProfile, *configPath)
	}

	base, err := config.ResolveProfile(store, trimmedProfile)
	if err != nil {
		return config.Config{}, store, err
	}

	overrides := config.Config{
		Name:   *name,
		Listen: *listen,
		Secret: *secret,
		Peers:  peers.slice(),
	}

	merged := config.Merge(base, overrides)
	return config.Normalize(merged), store, nil
}
