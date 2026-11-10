package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"stash-bot/internal/bot/resources"
	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

const pageSize = 10

// similarLimit bounds results returned by a similarity search.
const similarLimit = 50

// ── Screens ───────────────────────────────────────────────────────────────────

func showMainMenu(b *Bot, chatID int64, sess *Session) {
	// Hide the currently displayed storage media when returning to the menu,
	// so the gallery doesn't linger in the chat. The next storage open re-sends
	// it (instantly, via the cached Telegram file_id).
	deleteMediaMessages(b, chatID, sess)
	sess.Screen = ScreenMain
	sess.Pending = ""
	sess.CurrentItem = nil
	text := resources.Menu
	kb := tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton("📦 Хранилище").WithCallbackData("storage"),
			tu.InlineKeyboardButton("🔍 Поиск").WithCallbackData("search"),
		),
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton("✨ Похожие по описанию").WithCallbackData("simq"),
			tu.InlineKeyboardButton("🖼 Похожие по картинке").WithCallbackData("simimg"),
		),
	)
	sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, text, kb)
}

// deleteMediaMessages deletes all previously sent page media messages in one
// batch. Bundled deletion is intentional: deleting one-by-one makes the chat
// flicker as each message disappears individually.
func deleteMediaMessages(b *Bot, chatID int64, sess *Session) {
	if len(sess.MediaMsgIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	remaining := make([]int, 0)
	for start := 0; start < len(sess.MediaMsgIDs); start += 100 {
		ids := sess.MediaMsgIDs[start:min(start+100, len(sess.MediaMsgIDs))]
		err := b.api.DeleteMessages(ctx, &telego.DeleteMessagesParams{ChatID: tu.ID(chatID), MessageIDs: ids})
		if err == nil {
			continue
		}
		slog.Warn("delete media batch failed", "error", err)
		for _, id := range ids {
			err := b.api.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: id})
			if err != nil && !strings.Contains(strings.ToLower(err.Error()), "message to delete not found") {
				remaining = append(remaining, id)
				slog.Warn("delete media deferred", "message_id", id, "error", err)
			}
		}
	}
	sess.MediaMsgIDs = remaining
}

func loadStorageAndShow(ctx context.Context, b *Bot, chatID int64, sess *Session) {
	sess.Browse = BrowseStorage
	sess.Query = stash.SearchQuery{}
	sess.CurrentItem = nil
	sess.Pending = ""
	loadBrowsePage(ctx, b, chatID, sess, 0)
}

func browseTotal(sess *Session) int {
	if sess.Paged {
		return sess.Total
	}
	return len(sess.Items)
}

func loadBrowsePage(ctx context.Context, b *Bot, chatID int64, sess *Session, page int) {
	sess.Paged = false
	deleteMediaMessages(b, chatID, sess)
	sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, resources.Loading, nil)
	q := sess.Query
	q.Limit = pageSize
	q.Offset = page * pageSize
	result, err := b.stash.SearchPage(ctx, q)
	if err != nil {
		slog.Error("load browse page", "error", err)
		text := resources.SearchError
		if sess.Browse == BrowseStorage {
			text = resources.StorageError
		}
		clearBrowseResult(b, chatID, sess, text)
		return
	}
	if len(result.Items) == 0 && page > 0 && result.Total > 0 {
		loadBrowsePage(ctx, b, chatID, sess, 0)
		return
	}
	sess.Items = result.Items
	sess.Total = result.Total
	sess.Paged = true
	sess.CurrentPage = page
	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	for _, it := range result.Items {
		if it.TelegramFileID != nil && *it.TelegramFileID != "" {
			b.cacheFileID(it.ID, *it.TelegramFileID)
		}
	}
	sendStoragePage(b, chatID, sess, true)
}

func resultKeyboard() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(tu.InlineKeyboardRow(
		tu.InlineKeyboardButton(resources.RetryButton).WithCallbackData("refresh"),
		tu.InlineKeyboardButton(resources.StorageButton).WithCallbackData("storage"),
		tu.InlineKeyboardButton(resources.SearchButton).WithCallbackData("search"),
		tu.InlineKeyboardButton(resources.MenuButton).WithCallbackData("menu"),
	))
}

func clearBrowseResult(b *Bot, chatID int64, sess *Session, text string) {
	deleteMediaMessages(b, chatID, sess)
	sess.Items = nil
	sess.Total = 0
	sess.CurrentItem = nil
	sess.CurrentPage = 0
	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, text, resultKeyboard())
}

// sendStoragePage shows the current page.
// When sendFiles is true, media files are sent first and the keyboard is attached to the last one.
// When sendFiles is false (e.g. going back), only the control message is updated in place.
func sendStoragePage(b *Bot, chatID int64, sess *Session, sendFiles bool) {
	if len(sess.Items) == 0 {
		text := resources.EmptyStorage
		if sess.Browse == BrowseSearch {
			text = resources.EmptySearch
		}
		if sess.Browse == BrowseSimilar {
			text = resources.EmptySimilar
		}
		clearBrowseResult(b, chatID, sess, text)
		return
	}

	start, end := currentPageBounds(sess)
	pageItems := sess.Items[start:end]
	totalPages := (browseTotal(sess) + pageSize - 1) / pageSize

	// Diagnostic: report, for each item on the page, where its Telegram file_id
	// will come from. "DOWNLOAD" means the bot will re-fetch the file from the
	// backend — i.e. a cache miss that shows up as slow on a phone.
	for _, it := range pageItems {
		source := "DOWNLOAD"
		if fid, ok := b.lookupFileID(it.ID); ok && fid != "" {
			source = "cache"
		} else if it.TelegramFileID != nil && *it.TelegramFileID != "" {
			source = "item"
		}
		slog.Info("storage page item", "id", it.ID, "type", it.Type, "file_id_source", source)
	}

	slog.Info("sendStoragePage", "page", sess.CurrentPage, "total_pages", totalPages, "items_on_page", len(pageItems), "send_files", sendFiles)

	text := buildStoragePageText(sess, pageItems, start, totalPages)
	kb := storagePageKeyboard(sess.CurrentPage, totalPages)

	if sendFiles {
		sess.LastMsgID = sendFilesAndAttachKeyboard(b, chatID, sess, pageItems, text, kb)
	} else {
		sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, text, kb)
	}
}

// showSelectMode replaces the navigation keyboard with numbered item buttons.
func showSelectMode(b *Bot, chatID int64, sess *Session) {
	start, end := currentPageBounds(sess)
	pageItems := sess.Items[start:end]

	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton
	for i := range pageItems {
		row = append(row, tu.InlineKeyboardButton(strconv.Itoa(i+1)).WithCallbackData(fmt.Sprintf("si:%d", i)))
		if len(row) == 5 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []telego.InlineKeyboardButton{
		tu.InlineKeyboardButton("Отмена").WithCallbackData(fmt.Sprintf("sp:%d", sess.CurrentPage)),
	})

	kb := tu.InlineKeyboardGrid(rows)
	sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, "Выбери файл:", kb)
}

// showItem sends (or edits) the item detail message.
func showItem(b *Bot, chatID int64, sess *Session) {
	if sess.CurrentItem == nil {
		slog.Warn("showItem: no current item, falling back to main menu")
		showMainMenu(b, chatID, sess)
		return
	}
	slog.Info("showItem", "id", sess.CurrentItem.ID, "name", sess.CurrentItem.FileName)
	text := formatItemDetail(sess.CurrentItem)
	kb := itemDetailKeyboard(sess.CurrentItem)
	sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, text, kb)
}

// ── Page helpers ──────────────────────────────────────────────────────────────

// currentPageBounds returns the [start, end) indices for the current page,
// resetting to page 0 if start is out of range.
func currentPageBounds(sess *Session) (start, end int) {
	if sess.Paged {
		return 0, len(sess.Items)
	}
	start = max(0, sess.CurrentPage) * pageSize
	if start >= len(sess.Items) {
		sess.CurrentPage = 0
		start = 0
	}
	end = min(start+pageSize, len(sess.Items))
	return start, end
}

// buildStoragePageText builds the text for a storage page listing.
func buildStoragePageText(sess *Session, pageItems []*stash.Item, start, totalPages int) string {
	if sess.Paged {
		start = sess.CurrentPage * pageSize
	}
	var sb strings.Builder
	queryParts := []string{sess.Query.Text}
	for _, tag := range sess.Query.Tags {
		queryParts = append(queryParts, "#"+tag)
	}
	for _, tag := range sess.Query.ExcludeTags {
		queryParts = append(queryParts, "-#"+tag)
	}
	sb.WriteString(resources.PageHeader(resources.Page{Kind: string(sess.Browse), Query: strings.TrimSpace(strings.Join(queryParts, " ")), Number: sess.CurrentPage + 1, Pages: totalPages, Total: browseTotal(sess)}))
	for i, it := range pageItems {
		label := sanitizeUTF8(it.Description)
		if label == "" {
			label = sanitizeUTF8(it.FileName)
		}
		if len([]rune(label)) > 55 {
			label = string([]rune(label)[:55]) + "…"
		}
		sb.WriteString(resources.PageItem(mediaIcon(it.Type), start+i+1, label))
	}
	return sb.String()
}

// sendFilesAndAttachKeyboard deletes old page media, sends new media for the current page,
// starts prefetch for the next page, then edits (or sends) the control message with
// text + navigation keyboard. Media and controls are kept as separate messages so the
// keyboard always works regardless of media type.
// Returns the message ID of the control message.
func sendFilesAndAttachKeyboard(b *Bot, chatID int64, sess *Session, pageItems []*stash.Item, text string, kb *telego.InlineKeyboardMarkup) int {
	deleteMediaMessages(b, chatID, sess)

	// Delete the control message so it can be re-sent below the media.
	if sess.LastMsgID != 0 {
		err := b.api.DeleteMessage(context.Background(), &telego.DeleteMessageParams{
			ChatID:    tu.ID(chatID),
			MessageID: sess.LastMsgID,
		})
		if err != nil {
			_, _ = b.api.EditMessageReplyMarkup(context.Background(), &telego.EditMessageReplyMarkupParams{ChatID: tu.ID(chatID), MessageID: sess.LastMsgID, ReplyMarkup: tu.InlineKeyboard()})
		}
		sess.LastMsgID = 0
	}

	mediaIDs := sendPageFilesCollectIDs(b, chatID, pageItems)
	sess.MediaMsgIDs = append(sess.MediaMsgIDs, mediaIDs...)

	nextStart := (sess.CurrentPage + 1) * pageSize
	if !sess.Paged && nextStart < len(sess.Items) {
		// Snapshot under the user's lock: foreground sends can update cached
		// Telegram IDs while prefetch is running on the next page.
		var next []*stash.Item
		for _, item := range sess.Items[nextStart:min(nextStart+pageSize, len(sess.Items))] {
			copy := *item
			next = append(next, &copy)
		}
		go prefetchItems(b, next)
	}

	// Send fresh control message — always appears below the media.
	return editOrSendHTML(b, chatID, 0, text, kb)
}
