package bot

import (
	"fmt"
	"log/slog"
	"stash-bot/internal/stash"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	api       *tgbotapi.BotAPI
	stash     *stash.Client
	rootID    int64
	sessions  sync.Map // int64 (userID) → *Session
	fileCache sync.Map // string (item ID) → []byte (prefetched raw bytes)
}

func New(token string, rootID int64, stashClient *stash.Client) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
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
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)
	slog.Info("bot started", "username", b.api.Self.UserName)
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
