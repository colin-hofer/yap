package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"yap/internal/config"
)

func (c *CLI) runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	configPath := fs.String("config", config.DefaultPath(), "path to yap config file")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *configPath == "" {
		return errors.New("config path is required; use -config to set one")
	}

	store, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if store == nil {
		return errors.New("config storage unavailable")
	}

	current, err := config.ResolveProfile(store, "")
	if err != nil {
		return err
	}

	reader := bufio.NewReader(c.stdin())

	name, err := c.prompt(reader, "Display name", current.Name)
	if err != nil {
		return err
	}
	listen, err := c.prompt(reader, "Listen address", current.Listen)
	if err != nil {
		return err
	}
	secret, err := c.promptSecret(reader, current.Secret)
	if err != nil {
		return err
	}
	peersJoined := strings.Join(current.Peers, ", ")
	peersRaw, err := c.prompt(reader, "Bootstrap peers (comma separated)", peersJoined)
	if err != nil {
		return err
	}
	peers := parsePeers(peersRaw)

	snapshot := config.Snapshot(name, listen, secret, peers)

	if err := store.SaveDefault(snapshot); err != nil {
		return fmt.Errorf("save default config: %w", err)
	}

	fmt.Fprintf(c.stdout(), "Saved default configuration to %s\n", *configPath)
	for _, line := range config.Summary(snapshot) {
		fmt.Fprintln(c.stdout(), line)
	}

	return nil
}

func (c *CLI) prompt(reader *bufio.Reader, label, current string) (string, error) {
	if current != "" {
		fmt.Fprintf(c.stdout(), "%s [%s]: ", label, current)
	} else {
		fmt.Fprintf(c.stdout(), "%s: ", label)
	}

	input, err := reader.ReadString('\n')
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return current, nil
	}
	return input, nil
}

func (c *CLI) promptSecret(reader *bufio.Reader, current string) (string, error) {
	if current != "" {
		fmt.Fprintf(c.stdout(), "Shared secret [set] (blank to keep, type 'none' to disable): ")
	} else {
		fmt.Fprintf(c.stdout(), "Shared secret (leave blank for none): ")
	}

	input, err := reader.ReadString('\n')
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return current, nil
	}
	if strings.EqualFold(input, "none") {
		return "", nil
	}
	return input, nil
}

func parsePeers(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var peers []string
	seen := make(map[string]struct{})
	for _, part := range parts {
		peer := strings.TrimSpace(part)
		if peer == "" {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		peers = append(peers, peer)
	}
	return peers
}
