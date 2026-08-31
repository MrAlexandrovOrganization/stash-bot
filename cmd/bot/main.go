package main

import (
	"context"
	"log/slog"
	"os"

	"stash-bot/internal/bot"
	"stash-bot/internal/config"
	"stash-bot/internal/logx"
	"stash-bot/internal/stash"
	"stash-bot/internal/telemetry"
)

func main() {
	shutdownTelemetry, err := telemetry.Setup(context.Background())
	if err != nil {
		slog.Error("telemetry init", "error", err)
		os.Exit(1)
	}
	defer shutdownTelemetry(context.Background())

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
	b.Run()
}
