// Command mobqa-worker runs the background job pools: build analysis, and later runs and authoring.
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
	cfg, err := config.LoadWorker(os.LookupEnv)
	if err != nil {
		slog.Error("load config", "error", err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.RunWorker(ctx, cfg, logger); err != nil {
		logger.Error("worker stopped", "error", err)
		return 1
	}
	return 0
}
