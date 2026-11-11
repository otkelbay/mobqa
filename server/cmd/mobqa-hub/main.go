// Command mobqa-hub runs on a machine with phones attached. It dials the API, reports the devices it
// finds and, in later steps, carries out the work the API sends to them.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mobqa/backend/internal/app"
	"github.com/mobqa/backend/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.LoadHub(os.LookupEnv)
	if err != nil {
		slog.Error("load config", "error", err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.RunHub(ctx, cfg, logger); err != nil {
		logger.Error("hub stopped", "error", err)
		return 1
	}
	return 0
}
