package main

import (
	"log/slog"
	"os"

	"stash-bot/internal/bot"
	"stash-bot/internal/config"
	"stash-bot/internal/stash"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	stashClient := stash.NewClient(cfg.StashURL)

	b, err := bot.New(cfg.BotToken, cfg.RootID, stashClient, cfg.TelegramAPIURL)
	if err != nil {
		slog.Error("bot init", "error", err)
		os.Exit(1)
	}

	slog.Info("starting")
	b.Run()
}
