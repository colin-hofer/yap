package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"yap/internal/chat"
	"yap/internal/cli"
)

func main() {
	args := os.Args[1:]

	program := cli.New(os.Stdin, os.Stdout, os.Stderr, chat.Run)
	if err := program.Run(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
