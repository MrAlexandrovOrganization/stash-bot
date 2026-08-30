package bot

import (
	"context"
	"log/slog"
	"strings"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ── Text input ────────────────────────────────────────────────────────────────

func (b *Bot) handleTextInput(msg *telego.Message) {
	ctx := context.Background()
	text := strings.TrimSpace(msg.Text)
	sess := b.session(msg.From.ID, msg.Chat.ID)

	switch sess.Pending {
	case "search":
		sess.Pending = ""
		doSearch(b, msg.Chat.ID, sess, text)

	case "simq":
		sess.Pending = ""
		runSimilar(b, msg.Chat.ID, sess, stash.SimilarRequest{Text: text, Limit: similarLimit})

	case "desc":
		sess.Pending = ""
		updateCurrentItem(b, ctx, msg.Chat.ID, sess, stash.UpdateMeta{Description: &text}, "Ошибка при обновлении описания.")

	case "tags":
		sess.Pending = ""
		tags := parseTags(text)
		updateCurrentItem(b, ctx, msg.Chat.ID, sess, stash.UpdateMeta{Tags: tags}, "Ошибка при обновлении тегов.")

	default:
		// No pending state: treat message as a search query.
		doSearch(b, msg.Chat.ID, sess, text)
	}
}

// updateCurrentItem updates a field of the current item, refreshes the session, and shows the item.
func updateCurrentItem(b *Bot, ctx context.Context, chatID int64, sess *Session, meta stash.UpdateMeta, errMsg string) {
	if sess.CurrentItem == nil {
		return
	}
	updated, err := b.stash.Update(ctx, sess.CurrentItem.ID, meta)
	if err != nil {
		slog.Error("updateCurrentItem", "error", err)
		send(b, chatID, errMsg)
		return
	}
	applyItemUpdate(sess, updated)
	showItem(b, chatID, sess)
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

	text, posTags, negTags := parseSearchQuery(query)
	slog.Info("search", "text", text, "pos_tags", posTags, "neg_tags", negTags)

	items, err := b.stash.Search(context.Background(), stash.SearchQuery{Text: text, Tags: posTags})
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
		kb := tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				tu.InlineKeyboardButton("🔍 Поиск ещё раз").WithCallbackData("search"),
				tu.InlineKeyboardButton("🏠 Меню").WithCallbackData("menu"),
			),
		)
		sendHTML(b, chatID, "Ничего не найдено.", kb)
		return
	}

	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	sess.Items = items
	sess.CurrentPage = 0
	sendStoragePage(b, chatID, sess, true)
}

// runSimilar executes a similarity search, drops the current media from the
// results (when searching by item) and shows the page.
func runSimilar(b *Bot, chatID int64, sess *Session, req stash.SimilarRequest) {
	items, err := b.stash.Similar(context.Background(), req)
	if err != nil {
		slog.Error("similar: stash failed", "error", err)
		send(b, chatID, "Ошибка похожего поиска.")
		return
	}
	slog.Info("similar: got results", "count", len(items))

	// When searching by an existing item, exclude the query item itself.
	if req.ItemID != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.ID != req.ItemID {
				filtered = append(filtered, it)
			}
		}
		slog.Info("similar: after self-exclusion", "count", len(filtered))
		items = filtered
	}

	if len(items) == 0 {
		kb := tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				tu.InlineKeyboardButton("🔍 Найти ещё").WithCallbackData("search"),
				tu.InlineKeyboardButton("🏠 Меню").WithCallbackData("menu"),
			),
		)
		sendHTML(b, chatID, "Похожих медиа не найдено.", kb)
		return
	}

	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	sess.Items = items
	sess.CurrentPage = 0
	sendStoragePage(b, chatID, sess, true)
}
