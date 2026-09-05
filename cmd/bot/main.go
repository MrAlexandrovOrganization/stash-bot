package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"stash-bot/internal/bot"
	"stash-bot/internal/config"
	"stash-bot/internal/logx"
	"stash-bot/internal/stash"
	"stash-bot/internal/telemetry"
)

func main() {
	logx.Setup("stash-bot")
	shutdownTelemetry, err := telemetry.Setup(context.Background())
	if err != nil {
		slog.Error("telemetry init", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdownTelemetry(ctx); err != nil {
			slog.Error("telemetry shutdown", "error", err)
		}
	}()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	logx.Setup("stash-bot", cfg.BotToken)
	stashClient := stash.NewClient(cfg.StashURL)

	b, err := bot.New(cfg.BotToken, cfg.RootID, stashClient, cfg.TelegramAPIURL)
	if err != nil {
		slog.Error("bot init", "error", err)
		os.Exit(1)
	}

	slog.Info("starting")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	b.Run(ctx)
}
