package bot

import (
	"context"
	"log/slog"
	"strings"

	"stash-bot/internal/stash"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Text input ────────────────────────────────────────────────────────────────

func (b *Bot) handleTextInput(msg *tgbotapi.Message, sess *Session) {
	ctx := context.Background()
	text := strings.TrimSpace(msg.Text)

	switch sess.Pending {
	case "search":
		sess.Pending = ""
		b.doSearch(msg.Chat.ID, sess, text)

	case "desc":
		sess.Pending = ""
		if sess.CurrentItem == nil {
			return
		}
		updated, err := b.stash.Update(ctx, sess.CurrentItem.ID, stash.UpdateMeta{Description: &text})
		if err != nil {
			slog.Error("update desc", "error", err)
			b.send(msg.Chat.ID, "Ошибка при обновлении описания.")
			return
		}
		b.applyItemUpdate(sess, updated)
		b.showItem(msg.Chat.ID, sess)

	case "tags":
		sess.Pending = ""
		if sess.CurrentItem == nil {
			return
		}
		tags := parseTags(text)
		updated, err := b.stash.Update(ctx, sess.CurrentItem.ID, stash.UpdateMeta{Tags: tags})
		if err != nil {
			slog.Error("update tags", "error", err)
			b.send(msg.Chat.ID, "Ошибка при обновлении тегов.")
			return
		}
		b.applyItemUpdate(sess, updated)
		b.showItem(msg.Chat.ID, sess)

	case "tr":
		sess.Pending = ""
		if sess.CurrentItem == nil {
			return
		}
		updated, err := b.stash.Update(ctx, sess.CurrentItem.ID, stash.UpdateMeta{Transcript: &text})
		if err != nil {
			slog.Error("update transcript", "error", err)
			b.send(msg.Chat.ID, "Ошибка при обновлении расшифровки.")
			return
		}
		b.applyItemUpdate(sess, updated)
		b.showItem(msg.Chat.ID, sess)

	default:
		// No pending state: treat message as a search query.
		b.doSearch(msg.Chat.ID, sess, text)
	}
}

// applyItemUpdate refreshes the item in the session's item list and CurrentItem.
func (b *Bot) applyItemUpdate(sess *Session, updated *stash.Item) {
	sess.CurrentItem = updated
	for i, it := range sess.Items {
		if it.ID == updated.ID {
			sess.Items[i] = updated
			break
		}
	}
}

// ── Search ────────────────────────────────────────────────────────────────────

func (b *Bot) doSearch(chatID int64, sess *Session, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		b.send(chatID, "Введи текст для поиска.")
		return
	}

	ctx := context.Background()
	text, posTags, negTags := parseSearchQuery(query)
	slog.Info("search", "text", text, "pos_tags", posTags, "neg_tags", negTags)

	items, err := b.stash.Search(ctx, stash.SearchQuery{Text: text, Tags: posTags})
	if err != nil {
		slog.Error("search: stash failed", "error", err)
		b.send(chatID, "Ошибка поиска.")
		return
	}
	slog.Info("search: got results", "count", len(items))

	if len(negTags) > 0 {
		filtered := items[:0]
		for _, it := range items {
			if !hasAnyTag(it.Tags, negTags) {
				filtered = append(filtered, it)
			}
		}
		slog.Info("search: after negative filter", "count", len(filtered))
		items = filtered
	}

	if len(items) == 0 {
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔍 Поиск ещё раз", "search"),
				tgbotapi.NewInlineKeyboardButtonData("🏠 Меню", "menu"),
			),
		)
		b.sendHTML(chatID, "Ничего не найдено.", &kb)
		return
	}

	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	sess.Items = items
	sess.CurrentPage = 0
	b.sendStoragePage(chatID, sess, true)
}
