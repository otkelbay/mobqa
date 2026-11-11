// Command mobqa-api serves the MobQA REST API, the hub WebSocket endpoint and WebRTC signalling.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	// Profiles validate IANA timezones; slim container images ship no zoneinfo of their own.
	_ "time/tzdata"

	"github.com/mobqa/backend/internal/app"
	"github.com/mobqa/backend/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.LoadAPI(os.LookupEnv)
	if err != nil {
		slog.Error("load config", "error", err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.RunAPI(ctx, cfg, logger); err != nil {
		logger.Error("api stopped", "error", err)
		return 1
	}
	return 0
}
