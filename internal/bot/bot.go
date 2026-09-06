package bot

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"stash-bot/internal/stash"
	"sync"

	"github.com/mymmrac/telego"
)

type Bot struct {
	api            *telego.Bot
	fileAPI        *telego.Bot
	stash          *stash.Client
	rootID         int64
	token          string
	telegramAPIURL string
	sessions       sync.Map // int64 (userID) → *Session
	fileCache      sync.Map // string (item ID) → []byte (prefetched raw bytes)
	fileIDCache    sync.Map // string (item ID) → string (Telegram file_id)
	userLocks      sync.Map // int64 (userID) → *sync.Mutex
	updateSem      chan struct{}
}

// cacheFileID records a known Telegram file_id for an item so it can later be
// re-sent instantly by file_id instead of re-downloading from the backend.
func (b *Bot) cacheFileID(itemID, tgFileID string) {
	b.fileIDCache.Store(itemID, tgFileID)
}

// lookupFileID returns a cached Telegram file_id for an item, if known.
func (b *Bot) lookupFileID(itemID string) (string, bool) {
	v, ok := b.fileIDCache.Load(itemID)
	if !ok {
		return "", false
	}
	fid, ok := v.(string)
	return fid, ok && fid != ""
}

func New(token string, rootID int64, stashClient *stash.Client, telegramAPIURL string) (*Bot, error) {
	// net/http honors HTTPS_PROXY (including SOCKS) and NO_PROXY, unlike
	// Telego's default fasthttp caller. Allow enough time for long polling.
	opts := []telego.BotOption{telego.WithHTTPClient(&http.Client{Timeout: telegramDownloadTimeout})}
	if telegramAPIURL != "" {
		opts = append(opts, telego.WithAPIServer(telegramAPIURL))
	}
	api, err := telego.NewBot(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram bot: %w", err)
	}
	// GetFile uses its own context deadline instead of a client-wide timeout.
	fileAPI, err := telego.NewBot(token, append(opts, telego.WithHTTPClient(&http.Client{}))...)
	if err != nil {
		return nil, fmt.Errorf("telegram file bot: %w", err)
	}
	return &Bot{
		api:            api,
		fileAPI:        fileAPI,
		stash:          stashClient,
		rootID:         rootID,
		token:          token,
		telegramAPIURL: telegramAPIURL,
		updateSem:      make(chan struct{}, 8),
	}, nil
}

func (b *Bot) Run(ctx context.Context) {
	updates, err := b.api.UpdatesViaLongPolling(ctx, nil)
	if err != nil {
		slog.Error("bot: long polling failed", "error", err)
		return
	}
	slog.Info("bot started", "username", b.api.Username())
	for update := range updates {
		b.updateSem <- struct{}{}
		go func(update telego.Update) {
			defer func() {
				<-b.updateSem
				if recovered := recover(); recovered != nil {
					slog.Error("update handler panic", "panic", recovered)
				}
			}()
			b.handleUpdate(update)
		}(update)
	}
}

func (b *Bot) lockUser(userID int64) func() {
	value, _ := b.userLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (b *Bot) session(userID, chatID int64) *Session {
	v, _ := b.sessions.LoadOrStore(userID, &Session{ChatID: chatID, Screen: ScreenMain})
	sess := v.(*Session)
	sess.ChatID = chatID
	return sess
}
