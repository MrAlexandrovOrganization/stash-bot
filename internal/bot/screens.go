package bot

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

const pageSize = 10

// ── Screens ───────────────────────────────────────────────────────────────────

func showMainMenu(b *Bot, chatID int64, sess *Session) {
	// Note: we deliberately keep the media messages (sess.MediaMsgIDs) in the
	// chat instead of deleting them, so returning to storage can reuse them
	// without re-sending. A reload (handled in loadStorageAndShow) still
	// re-sends and clears them when needed.
	text := "👋 Привет! Я твоё личное хранилище медиа.\n\nЧтобы сохранить файл — просто отправь его сюда.\nЧтобы найти — напиши текст или теги."
	kb := tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton("📦 Хранилище").WithCallbackData("storage"),
			tu.InlineKeyboardButton("🔍 Поиск").WithCallbackData("search"),
		),
	)
	sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, text, kb)
}

// deleteMediaMessages deletes all previously sent page media messages.
// Done one-by-one so a single failure (e.g. a message older than 48h or already
// removed) doesn't abort the rest and leak orphaned media into the chat. IDs
// that couldn't be deleted are kept so a later refresh can retry them.
func deleteMediaMessages(b *Bot, chatID int64, sess *Session) {
	if len(sess.MediaMsgIDs) == 0 {
		return
	}
	slog.Info("deleteMediaMessages", "count", len(sess.MediaMsgIDs))
	remaining := make([]int, 0, len(sess.MediaMsgIDs))
	for _, id := range sess.MediaMsgIDs {
		if err := b.api.DeleteMessage(context.Background(), &telego.DeleteMessageParams{
			ChatID:    tu.ID(chatID),
			MessageID: id,
		}); err != nil {
			slog.Error("deleteMediaMessages: failed", "id", id, "error", err)
			remaining = append(remaining, id)
		}
	}
	sess.MediaMsgIDs = remaining
}

// loadStorageAndShow loads all items from stash (optionally force-reloading) and shows page 0.
func loadStorageAndShow(b *Bot, chatID int64, sess *Session, reload bool) {
	if reload || sess.Items == nil {
		slog.Info("loading storage from stash")
		items, err := b.stash.Search(context.Background(), stash.SearchQuery{})
		if err != nil {
			slog.Error("load storage: stash search failed", "error", err)
			send(b, chatID, "Ошибка загрузки хранилища.")
			return
		}
		slices.SortFunc(items, func(a, b *stash.Item) int {
			return b.CreatedAt.Compare(a.CreatedAt)
		})
		slog.Info("storage loaded", "count", len(items))
		sess.Items = items
		// Seed the durable file_id cache from whatever the backend already has,
		// so a previously persisted file_id survives sess.Items being reset.
		for _, it := range items {
			if it.TelegramFileID != nil && *it.TelegramFileID != "" {
				b.cacheFileID(it.ID, *it.TelegramFileID)
			}
		}
		sess.CurrentPage = 0
	} else {
		slog.Info("storage: using cached items", "count", len(sess.Items))
	}
	sess.Screen = ScreenStorage
	sess.Back = ScreenMain

	// Reuse already-sent media when returning to the same page without
	// reloading the item list (e.g. menu → storage). This avoids re-sending
	// and thus re-downloading media on every open. A reload (new upload, first
	// open) always re-sends, which also clears any stale media first.
	reuseMedia := !reload && len(sess.MediaMsgIDs) > 0
	sendStoragePage(b, chatID, sess, !reuseMedia)
}

// sendStoragePage shows the current page.
// When sendFiles is true, media files are sent first and the keyboard is attached to the last one.
// When sendFiles is false (e.g. going back), only the control message is updated in place.
func sendStoragePage(b *Bot, chatID int64, sess *Session, sendFiles bool) {
	if len(sess.Items) == 0 {
		slog.Info("sendStoragePage: empty")
		kb := tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				tu.InlineKeyboardButton("🔍 Поиск").WithCallbackData("search"),
				tu.InlineKeyboardButton("🏠 Меню").WithCallbackData("menu"),
			),
		)
		sess.LastMsgID = editOrSendHTML(b, chatID, sess.LastMsgID, "📦 Хранилище пусто.", kb)
		return
	}

	start, end := currentPageBounds(sess)
	pageItems := sess.Items[start:end]
	totalPages := (len(sess.Items) + pageSize - 1) / pageSize

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
	start := sess.CurrentPage * pageSize
	end := min(start+pageSize, len(sess.Items))
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
	start = sess.CurrentPage * pageSize
	if start >= len(sess.Items) {
		sess.CurrentPage = 0
		start = 0
	}
	end = min(start+pageSize, len(sess.Items))
	return start, end
}

// buildStoragePageText builds the text for a storage page listing.
func buildStoragePageText(sess *Session, pageItems []*stash.Item, start, totalPages int) string {
	title := "📦 <b>Хранилище</b>"
	if sess.Back != ScreenMain {
		title = "🔍 <b>Результаты поиска</b>"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n", title)
	fmt.Fprintf(&sb, "Страница %d из %d · %d файлов\n\n", sess.CurrentPage+1, totalPages, len(sess.Items))
	for i, it := range pageItems {
		label := sanitizeUTF8(it.Description)
		if label == "" {
			label = sanitizeUTF8(it.FileName)
		}
		if len([]rune(label)) > 55 {
			label = string([]rune(label)[:55]) + "…"
		}
		fmt.Fprintf(&sb, "%s %d. %s\n", mediaIcon(it.Type), start+i+1, escapeHTML(label))
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
		_ = b.api.DeleteMessage(context.Background(), &telego.DeleteMessageParams{
			ChatID:    tu.ID(chatID),
			MessageID: sess.LastMsgID,
		})
		sess.LastMsgID = 0
	}

	mediaIDs := sendPageFilesCollectIDs(b, chatID, pageItems)
	sess.MediaMsgIDs = mediaIDs

	nextStart := (sess.CurrentPage + 1) * pageSize
	if nextStart < len(sess.Items) {
		go prefetchItems(b, sess.Items[nextStart:min(nextStart+pageSize, len(sess.Items))])
	}

	// Send fresh control message — always appears below the media.
	return editOrSendHTML(b, chatID, 0, text, kb)
}
