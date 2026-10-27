package bot

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ── Update routing ────────────────────────────────────────────────────────────

func (b *Bot) handleUpdate(update telego.Update) {
	ctx, span := otel.Tracer("stash-bot").Start(context.Background(), "telegram.update")
	defer span.End()
	userID := updateUserID(update)
	if userID != 0 {
		defer b.lockUser(userID)()
	}
	if update.CallbackQuery != nil {
		b.handleCallback(ctx, update.CallbackQuery)
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

	// Commands must take priority over pending input. A command reply must
	// appear below /start, rather than silently editing an old control message.
	cmd, _, _ := tu.ParseCommand(msg.Text)
	if cmd == "start" {
		started := time.Now()
		slog.Info("start command")
		sess.Pending = ""
		sess.Screen = ScreenMain
		sess.LastMsgID = 0
		showMainMenu(b, msg.Chat.ID, sess)
		if sess.LastMsgID != 0 {
			slog.Info("start menu sent", "duration", time.Since(started))
		}
		return
	}

	// If the user armed "find similar by image", route the photo to a
	// similarity query instead of saving it.
	if sess.Pending == "simimg" {
		sess.Pending = ""
		if msg.Photo != nil {
			b.handleSimilarByImage(ctx, msg)
			return
		}
		send(b, msg.Chat.ID, "Отправь именно картинку для поиска похожих.")
		return
	}

	// Media uploads always go to stash, clearing any pending state.
	switch {
	case msg.Photo != nil:
		sess.Pending = ""
		b.handleUpload(ctx, msg, stash.MediaTypeImage)
		return
	case msg.Video != nil:
		sess.Pending = ""
		b.handleUpload(ctx, msg, stash.MediaTypeVideo)
		return
	case msg.Animation != nil:
		sess.Pending = ""
		b.handleUpload(ctx, msg, stash.MediaTypeGIF)
		return
	case msg.Document != nil:
		sess.Pending = ""
		b.handleUpload(ctx, msg, stash.MediaTypeDocument)
		return
	}

	if msg.Text != "" {
		b.handleTextInput(ctx, msg)
	}
}

func updateUserID(update telego.Update) int64 {
	if update.CallbackQuery != nil {
		return update.CallbackQuery.From.ID
	}
	if update.Message != nil && update.Message.From != nil {
		return update.Message.From.ID
	}
	return 0
}
