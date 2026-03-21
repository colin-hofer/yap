package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"yap/internal/app"
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

func run(args []string) error {
	var opts app.Options

	switch len(args) {
	case 0:
	case 1:
		switch args[0] {
		case "-h", "--help", "help":
			printUsage()
			return nil
		case "-v", "--version", "version":
			fmt.Println(version.Current())
			return nil
		case "update":
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
		return errors.New("usage: yap | yap open <swarm> | yap join <invite> | yap update | yap version")
	}

	service, err := app.New(context.Background(), opts)
	if err != nil {
		return err
	}
	result, err := ui.Run(service)
	if err != nil {
		return err
	}
	if result.UpdateRequested {
		return runUpdate(context.Background())
	}
	return nil
}

func usageError(command string) error {
	return fmt.Errorf("unknown command %q\n\nusage: yap | yap open <swarm> | yap join <invite> | yap update | yap version", command)
}

func printUsage() {
	fmt.Println("usage:")
	fmt.Println("  yap")
	fmt.Println("  yap open <swarm>")
	fmt.Println("  yap join <invite>")
	fmt.Println("  yap update")
	fmt.Println("  yap version")
}

func runUpdate(ctx context.Context) error {
	updater, err := update.New(update.Config{
		RepoOwner:      version.RepositoryOwner,
		RepoName:       version.RepositoryName,
		BinaryName:     version.BinaryName,
		CurrentVersion: version.Current(),
	})
	if err != nil {
		return err
	}

	fmt.Printf("checking latest release for %s/%s...\n", version.RepositoryOwner, version.RepositoryName)
	result, err := updater.Update(ctx)
	if err != nil {
		return err
	}
	if !result.Updated {
		fmt.Printf("already up to date (%s)\n", result.LatestVersion)
		return nil
	}
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
