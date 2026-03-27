package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"yap/internal/app"
	"yap/internal/debuglog"
	"yap/internal/store"
	"yap/internal/ui"
	"yap/internal/update"
	"yap/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(rawArgs []string) (err error) {
	args, debugEnabled := splitDebugFlags(rawArgs)
	var opts app.Options
	root := store.DefaultRoot()
	opts.RootDir = root

	if debugEnabled {
		path, enableErr := debuglog.Enable(root)
		if enableErr != nil {
			return enableErr
		}
		fmt.Fprintf(os.Stderr, "debug logging enabled: %s\n", path)
		debuglog.Info("client start",
			"version", version.Current(),
			"root", root,
			"command", summarizeArgs(args),
		)
		defer func() {
			if err != nil {
				debuglog.Error("client exit", "status", "error", "error", err.Error())
			} else {
				debuglog.Info("client exit", "status", "ok")
			}
			_ = debuglog.Close()
		}()
	}

	switch len(args) {
	case 0:
	case 1:
		switch args[0] {
		case "-h", "--help", "help":
			debuglog.Info("help requested")
			printUsage()
			return nil
		case "-v", "--version", "version":
			debuglog.Info("version requested", "version", version.Current())
			fmt.Println(version.Current())
			return nil
		case "update":
			debuglog.Info("update command requested")
			return runUpdate(context.Background())
		default:
			return usageError(args[0])
		}
	case 2:
		switch args[0] {
		case "open":
			opts.OpenSwarm = args[1]
		case "join":
			opts.JoinCode = args[1]
		default:
			return usageError(args[0])
		}
	default:
		return errors.New("usage: yap [--debug] | yap [--debug] open <swarm> | yap [--debug] join <invite> | yap [--debug] update | yap [--debug] version")
	}

	debuglog.Info("starting app service",
		"open_swarm", strings.TrimSpace(opts.OpenSwarm),
		"join_mode", summarizeJoinArg(opts.JoinCode),
	)
	service, err := app.New(context.Background(), opts)
	if err != nil {
		debuglog.Error("app service start failed", "error", err.Error())
		return err
	}
	debuglog.Info("starting terminal ui")
	result, err := ui.Run(service)
	if err != nil {
		debuglog.Error("terminal ui exited with error", "error", err.Error())
		return err
	}
	debuglog.Info("terminal ui exited", "update_requested", result.UpdateRequested)
	if result.UpdateRequested {
		debuglog.Info("running updater after ui exit")
		return runUpdate(context.Background())
	}
	return nil
}

func usageError(command string) error {
	return fmt.Errorf("unknown command %q\n\nusage: yap [--debug] | yap [--debug] open <swarm> | yap [--debug] join <invite> | yap [--debug] update | yap [--debug] version", command)
}

func printUsage() {
	fmt.Println("usage:")
	fmt.Println("  yap [--debug]")
	fmt.Println("  yap [--debug] open <swarm>")
	fmt.Println("  yap [--debug] join <invite>")
	fmt.Println("  yap [--debug] update")
	fmt.Println("  yap [--debug] version")
}

func runUpdate(ctx context.Context) error {
	debuglog.Info("update flow started")
	updater, err := update.New(update.Config{
		RepoOwner:      version.RepositoryOwner,
		RepoName:       version.RepositoryName,
		BinaryName:     version.BinaryName,
		CurrentVersion: version.Current(),
	})
	if err != nil {
		debuglog.Error("update initialization failed", "error", err.Error())
		return err
	}

	fmt.Printf("checking latest release for %s/%s...\n", version.RepositoryOwner, version.RepositoryName)
	result, err := updater.Update(ctx)
	if err != nil {
		debuglog.Error("update flow failed", "error", err.Error())
		return err
	}
	if !result.Updated {
		debuglog.Info("update flow finished", "updated", false, "latest_version", result.LatestVersion)
		fmt.Printf("already up to date (%s)\n", result.LatestVersion)
		return nil
	}
	debuglog.Info("update flow finished",
		"updated", true,
		"previous_version", displayVersion(result.PreviousVersion),
		"latest_version", result.LatestVersion,
		"asset_name", result.AssetName,
	)
	fmt.Printf("updated %s -> %s using %s\n", displayVersion(result.PreviousVersion), result.LatestVersion, result.AssetName)
	fmt.Printf("installed binary: %s\n", result.ExecutablePath)
	if result.RestartRequired {
		fmt.Println("the new binary has been staged and will replace the old one as yap exits")
	}
	fmt.Println("restart yap to use the new version")
	return nil
}

func displayVersion(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func splitDebugFlags(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	debug := false
	for _, arg := range args {
		switch arg {
		case "--debug", "-d":
			debug = true
		default:
			out = append(out, arg)
		}
	}
	return out, debug
}

func summarizeArgs(args []string) string {
	if len(args) == 0 {
		return "interactive"
	}
	out := append([]string(nil), args...)
	if len(out) >= 2 && out[0] == "join" {
		out[1] = "<invite>"
	}
	return strings.Join(out, " ")
}

func summarizeJoinArg(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "none"
	}
	return "<invite>"
}
