package bot

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"stash-bot/internal/bot/resources"
	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
)

// ── Text input ────────────────────────────────────────────────────────────────

func (b *Bot) handleTextInput(ctx context.Context, msg *telego.Message) {
	text := strings.TrimSpace(msg.Text)
	sess := b.session(msg.From.ID, msg.Chat.ID)

	switch sess.Pending {
	case "search":
		sess.Pending = ""
		doSearch(ctx, b, msg.Chat.ID, sess, text)

	case "simq":
		sess.Pending = ""
		runSimilar(ctx, b, msg.Chat.ID, sess, stash.SimilarRequest{Text: text, Limit: similarLimit})

	case "desc":
		sess.Pending = ""
		updateCurrentItem(b, ctx, msg.Chat.ID, sess, stash.UpdateMeta{Description: &text}, "Ошибка при обновлении описания.")

	case "tags":
		sess.Pending = ""
		tags := parseTags(text)
		updateCurrentItem(b, ctx, msg.Chat.ID, sess, stash.UpdateMeta{Tags: tags}, "Ошибка при обновлении тегов.")

	default:
		// No pending state: treat message as a search query.
		doSearch(ctx, b, msg.Chat.ID, sess, text)
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
	if sess.CurrentItem != nil && sess.CurrentItem.ID == updated.ID {
		sess.CurrentItem = updated
	}
	for i, it := range sess.Items {
		if it.ID == updated.ID {
			sess.Items[i] = updated
			break
		}
	}
}

// ── Search ────────────────────────────────────────────────────────────────────

func doSearch(ctx context.Context, b *Bot, chatID int64, sess *Session, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	text, pos, neg := parseSearchQuery(query)
	sess.Browse = BrowseSearch
	sess.Query = stash.SearchQuery{Text: text, Tags: pos, ExcludeTags: neg}
	sess.CurrentItem = nil
	sess.CurrentPage = 0
	sess.Pending = ""
	loadBrowsePage(ctx, b, chatID, sess, 0)
	if sess.Paged && sess.Total == 0 && text != "" {
		sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, resources.Loading, nil)
		items, err := b.stash.Similar(ctx, stash.SimilarRequest{Text: text, Tags: pos, ExcludeTags: neg, Limit: similarLimit})
		if err != nil {
			clearBrowseResult(b, chatID, sess, resources.FallbackUnavailable)
			return
		}
		showSimilarResults(ctx, b, chatID, sess, stash.SimilarRequest{Text: text, Tags: pos, ExcludeTags: neg}, items, nil)
	}
}

// runSimilar executes a similarity search, drops the current media from the
// results (when searching by item) and shows the page.
func runSimilar(ctx context.Context, b *Bot, chatID int64, sess *Session, req stash.SimilarRequest) {
	deleteMediaMessages(b, chatID, sess)
	sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, resources.Loading, nil)
	items, err := b.stash.Similar(ctx, req)
	showSimilarResults(ctx, b, chatID, sess, req, items, err)
}

func showSimilarResults(ctx context.Context, b *Bot, chatID int64, sess *Session, req stash.SimilarRequest, items []*stash.Item, err error) {
	sess.SimilarQuery = req
	sess.SimilarQuery.ImageReader = nil // Uploaded query readers are closed after inference.
	sess.Browse = BrowseSimilar
	sess.Paged = false
	sess.Query = stash.SearchQuery{Text: req.Text, Tags: req.Tags, ExcludeTags: req.ExcludeTags}
	sess.CurrentItem = nil
	sess.Pending = ""
	if err != nil {
		slog.Warn("similar search failed", "error", err)
		text := resources.SimilarError
		var status *stash.StatusError
		if errors.As(err, &status) && status.Status == 409 {
			text = resources.IndexPending
		}
		clearBrowseResult(b, chatID, sess, text)
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
		clearBrowseResult(b, chatID, sess, resources.EmptySimilar)
		return
	}

	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	sess.Items = items
	sess.CurrentPage = 0
	sendStoragePage(b, chatID, sess, true)
}
