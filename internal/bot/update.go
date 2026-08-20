package bot

import (
	"log/slog"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ── Update routing ────────────────────────────────────────────────────────────

func (b *Bot) handleUpdate(update telego.Update) {
	if update.CallbackQuery != nil {
		b.handleCallback(update.CallbackQuery)
		return
	}
	if update.Message == nil {
		return
	}
	msg := update.Message
	if msg.From == nil || msg.From.ID != b.rootID {
		slog.Warn("unauthorized", "user_id", func() int64 {
			if msg.From != nil {
				return msg.From.ID
			}
			return 0
		}())
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

	cmd, _, _ := tu.ParseCommand(msg.Text)
	if cmd == "start" {
		sess.Pending = ""
		showMainMenu(b, msg.Chat.ID, sess)
		return
	}

	if msg.Text != "" {
		b.handleTextInput(msg)
	}
}
