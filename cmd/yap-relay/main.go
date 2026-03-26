package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"yap/internal/relay"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relay.Run(ctx, relay.ConfigFromEnv(), logger); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
