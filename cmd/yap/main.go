package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"yap/internal/app"
	"yap/internal/ui"
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
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			printUsage()
			return nil
		}
		return errors.New("usage: yap | yap open <swarm> | yap join <invite>")
	}

	service, err := app.New(context.Background(), opts)
	if err != nil {
		return err
	}
	return ui.Run(service)
}

func usageError(command string) error {
	return fmt.Errorf("unknown command %q\n\nusage: yap | yap open <swarm> | yap join <invite>", command)
}

func printUsage() {
	fmt.Println("usage:")
	fmt.Println("  yap")
	fmt.Println("  yap open <swarm>")
	fmt.Println("  yap join <invite>")
}
