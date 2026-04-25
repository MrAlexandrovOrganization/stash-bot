package bot

import (
	"context"
	"log/slog"
	"strings"

	"stash-bot/internal/stash"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Text input ────────────────────────────────────────────────────────────────

func (b *Bot) handleTextInput(msg *tgbotapi.Message) {
	ctx := context.Background()
	text := strings.TrimSpace(msg.Text)
	sess := b.session(msg.From.ID, msg.Chat.ID)

	switch sess.Pending {
	case "search":
		sess.Pending = ""
		doSearch(b, msg.Chat.ID, sess, text)

	case "desc":
		sess.Pending = ""
		if sess.CurrentItem == nil {
			return
		}
		updated, err := b.stash.Update(ctx, sess.CurrentItem.ID, stash.UpdateMeta{Description: &text})
		if err != nil {
			slog.Error("update desc", "error", err)
			send(b, msg.Chat.ID, "Ошибка при обновлении описания.")
			return
		}
		applyItemUpdate(sess, updated)
		showItem(b, msg.Chat.ID, sess)

	case "tags":
		sess.Pending = ""
		if sess.CurrentItem == nil {
			return
		}
		tags := parseTags(text)
		updated, err := b.stash.Update(ctx, sess.CurrentItem.ID, stash.UpdateMeta{Tags: tags})
		if err != nil {
			slog.Error("update tags", "error", err)
			send(b, msg.Chat.ID, "Ошибка при обновлении тегов.")
			return
		}
		applyItemUpdate(sess, updated)
		showItem(b, msg.Chat.ID, sess)

	case "tr":
		sess.Pending = ""
		if sess.CurrentItem == nil {
			return
		}
		updated, err := b.stash.Update(ctx, sess.CurrentItem.ID, stash.UpdateMeta{Transcript: &text})
		if err != nil {
			slog.Error("update transcript", "error", err)
			send(b, msg.Chat.ID, "Ошибка при обновлении расшифровки.")
			return
		}
		applyItemUpdate(sess, updated)
		showItem(b, msg.Chat.ID, sess)

	default:
		// No pending state: treat message as a search query.
		doSearch(b, msg.Chat.ID, sess, text)
	}
}

// applyItemUpdate refreshes the item in the session's item list and CurrentItem.
func applyItemUpdate(sess *Session, updated *stash.Item) {
	sess.CurrentItem = updated
	for i, it := range sess.Items {
		if it.ID == updated.ID {
			sess.Items[i] = updated
			break
		}
	}
}

// ── Search ────────────────────────────────────────────────────────────────────

func doSearch(b *Bot, chatID int64, sess *Session, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		send(b, chatID, "Введи текст для поиска.")
		return
	}

	ctx := context.Background()
	text, posTags, negTags := parseSearchQuery(query)
	slog.Info("search", "text", text, "pos_tags", posTags, "neg_tags", negTags)

	items, err := b.stash.Search(ctx, stash.SearchQuery{Text: text, Tags: posTags})
	if err != nil {
		slog.Error("search: stash failed", "error", err)
		send(b, chatID, "Ошибка поиска.")
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
		sendHTML(b, chatID, "Ничего не найдено.", &kb)
		return
	}

	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	sess.Items = items
	sess.CurrentPage = 0
	sendStoragePage(b, chatID, sess, true)
}
