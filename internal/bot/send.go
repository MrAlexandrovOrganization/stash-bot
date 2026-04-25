package bot

import (
	"log/slog"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Send helpers ──────────────────────────────────────────────────────────────

// editOrSendHTML tries to edit an existing message (msgID != 0) in place.
// Falls back to sending a new message when editing is not possible.
// Returns the message ID of the resulting message.
func (b *Bot) editOrSendHTML(chatID int64, msgID int, text string, kb *tgbotapi.InlineKeyboardMarkup) int {
	if msgID != 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = tgbotapi.ModeHTML
		edit.ReplyMarkup = kb
		if msg, err := b.api.Send(edit); err == nil {
			return msg.MessageID
		}
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	if kb != nil {
		msg.ReplyMarkup = kb
	}
	if sent, err := b.api.Send(msg); err == nil {
		return sent.MessageID
	}
	return msgID
}

func (b *Bot) send(chatID int64, text string) {
	if _, err := b.api.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		slog.Error("send", "error", err)
	}
}

func (b *Bot) sendHTML(chatID int64, text string, kb *tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	if kb != nil {
		msg.ReplyMarkup = kb
	}
	if _, err := b.api.Send(msg); err != nil {
		slog.Error("sendHTML", "error", err)
	}
}
