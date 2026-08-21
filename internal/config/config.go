package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BotToken string
	RootID   int64
	StashURL string

	// TelegramAPIURL is the base URL of a local Telegram Bot API server.
	// Empty means the default https://api.telegram.org.
	TelegramAPIURL string
}

func Load() (*Config, error) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required")
	}

	rootIDStr := os.Getenv("ROOT_ID")
	if rootIDStr == "" {
		return nil, fmt.Errorf("ROOT_ID is required")
	}
	rootID, err := strconv.ParseInt(rootIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ROOT_ID must be a number: %w", err)
	}

	stashURL := os.Getenv("STASH_URL")
	if stashURL == "" {
		return nil, fmt.Errorf("STASH_URL is required")
	}

	return &Config{
		BotToken:       token,
		RootID:         rootID,
		StashURL:       stashURL,
		TelegramAPIURL: strings.TrimRight(os.Getenv("TELEGRAM_LOCAL_API_URL"), "/"),
	}, nil
}
