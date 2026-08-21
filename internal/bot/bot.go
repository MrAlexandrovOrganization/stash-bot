package bot

import (
	"context"
	"fmt"
	"log/slog"
	"stash-bot/internal/stash"
	"sync"

	"github.com/mymmrac/telego"
)

type Bot struct {
	api       *telego.Bot
	stash     *stash.Client
	rootID    int64
	sessions  sync.Map // int64 (userID) → *Session
	fileCache sync.Map // string (item ID) → []byte (prefetched raw bytes)
}

func New(token string, rootID int64, stashClient *stash.Client, telegramAPIURL string) (*Bot, error) {
	opts := []telego.BotOption{}
	if telegramAPIURL != "" {
		opts = append(opts, telego.WithAPIServer(telegramAPIURL))
	}
	api, err := telego.NewBot(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram bot: %w", err)
	}
	return &Bot{
		api:    api,
		stash:  stashClient,
		rootID: rootID,
	}, nil
}

func (b *Bot) Run() {
	ctx := context.Background()
	updates, err := b.api.UpdatesViaLongPolling(ctx, nil)
	if err != nil {
		slog.Error("bot: long polling failed", "error", err)
		return
	}
	slog.Info("bot started", "username", b.api.Username())
	for update := range updates {
		go b.handleUpdate(update)
	}
}

func (b *Bot) session(userID, chatID int64) *Session {
	v, _ := b.sessions.LoadOrStore(userID, &Session{ChatID: chatID, Screen: ScreenMain})
	sess := v.(*Session)
	sess.ChatID = chatID
	return sess
}
