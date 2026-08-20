package bot

import (
	"context"
	"log/slog"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ── Send helpers ──────────────────────────────────────────────────────────────

// editOrSendHTML tries to edit an existing message (msgID != 0) in place.
// Falls back to sending a new message when editing is not possible.
// Returns the message ID of the resulting message.
func editOrSendHTML(b *Bot, chatID int64, msgID int, text string, kb *telego.InlineKeyboardMarkup) int {
	ctx := context.Background()
	if msgID != 0 {
		params := tu.EditMessageText(tu.ID(chatID), msgID, text).
			WithParseMode(telego.ModeHTML).
			WithReplyMarkup(kb)
		if msg, err := b.api.EditMessageText(ctx, params); err == nil {
			return msg.MessageID
		}
	}
	params := tu.Message(tu.ID(chatID), text).WithParseMode(telego.ModeHTML)
	if kb != nil {
		params = params.WithReplyMarkup(kb)
	}
	if sent, err := b.api.SendMessage(ctx, params); err == nil {
		return sent.MessageID
	}
	return msgID
}

func send(b *Bot, chatID int64, text string) {
	if _, err := b.api.SendMessage(context.Background(), tu.Message(tu.ID(chatID), text)); err != nil {
		slog.Error("send", "error", err)
	}
}

func sendHTML(b *Bot, chatID int64, text string, kb *telego.InlineKeyboardMarkup) {
	params := tu.Message(tu.ID(chatID), text).WithParseMode(telego.ModeHTML)
	if kb != nil {
		params = params.WithReplyMarkup(kb)
	}
	if _, err := b.api.SendMessage(context.Background(), params); err != nil {
		slog.Error("sendHTML", "error", err)
	}
}
