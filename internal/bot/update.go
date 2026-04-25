package bot

import (
	"log/slog"
	"stash-bot/internal/stash"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Update routing ────────────────────────────────────────────────────────────

func (b *Bot) handleUpdate(update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		b.handleCallback(update.CallbackQuery)
		return
	}
	if update.Message == nil {
		return
	}
	msg := update.Message
	if msg.From.ID != b.rootID {
		slog.Warn("unauthorized", "user_id", msg.From.ID)
		return
	}

	sess := b.session(msg.From.ID, msg.Chat.ID)

	// Media uploads always go to stash, clearing any pending state.
	switch {
	case msg.Photo != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeImage)
		return
	case msg.Video != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeVideo)
		return
	case msg.Animation != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeGIF)
		return
	case msg.Document != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeDocument)
		return
	}

	if msg.Command() == "start" {
		sess.Pending = ""
		b.showMainMenu(msg.Chat.ID, sess)
		return
	}

	if msg.Text != "" {
		b.handleTextInput(msg, sess)
	}
}
