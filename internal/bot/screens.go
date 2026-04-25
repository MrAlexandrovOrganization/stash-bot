package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"stash-bot/internal/stash"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const pageSize = 10

// ── Screens ───────────────────────────────────────────────────────────────────

func (b *Bot) showMainMenu(chatID int64, sess *Session) {
	text := "👋 Привет! Я твоё личное хранилище медиа.\n\nЧтобы сохранить файл — просто отправь его сюда.\nЧтобы найти — напиши текст или теги."
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📦 Хранилище", "storage"),
			tgbotapi.NewInlineKeyboardButtonData("🔍 Поиск", "search"),
		),
	)
	sess.LastMsgID = b.editOrSendHTML(chatID, sess.LastMsgID, text, &kb)
}

// loadStorageAndShow loads all items from stash (optionally force-reloading) and shows page 0.
func (b *Bot) loadStorageAndShow(chatID int64, sess *Session, reload bool) {
	if reload || sess.Items == nil {
		slog.Info("loading storage from stash")
		ctx := context.Background()
		items, err := b.stash.Search(ctx, stash.SearchQuery{})
		if err != nil {
			slog.Error("load storage: stash search failed", "error", err)
			b.send(chatID, "Ошибка загрузки хранилища.")
			return
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
		slog.Info("storage loaded", "count", len(items))
		sess.Items = items
		sess.CurrentPage = 0
	} else {
		slog.Info("storage: using cached items", "count", len(sess.Items))
	}
	sess.Screen = ScreenStorage
	sess.Back = ScreenMain
	b.sendStoragePage(chatID, sess, true)
}

// sendStoragePage shows the current page.
// When sendFiles is true, media files are sent first (new messages) and the control
// message is always a new message (so it appears below the media).
// When sendFiles is false (e.g. going back from item detail), only the control
// message is updated — it is edited in place if possible.
func (b *Bot) sendStoragePage(chatID int64, sess *Session, sendFiles bool) {
	if len(sess.Items) == 0 {
		slog.Info("sendStoragePage: empty")
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔍 Поиск", "search"),
				tgbotapi.NewInlineKeyboardButtonData("🏠 Меню", "menu"),
			),
		)
		sess.LastMsgID = b.editOrSendHTML(chatID, sess.LastMsgID, "📦 Хранилище пусто.", &kb)
		return
	}

	start := sess.CurrentPage * pageSize
	if start >= len(sess.Items) {
		sess.CurrentPage = 0
		start = 0
	}
	end := min(start+pageSize, len(sess.Items))
	pageItems := sess.Items[start:end]

	total := len(sess.Items)
	totalPages := (total + pageSize - 1) / pageSize
	slog.Info("sendStoragePage", "page", sess.CurrentPage, "total_pages", totalPages, "items_on_page", len(pageItems), "send_files", sendFiles)

	title := "📦 <b>Хранилище</b>"
	if sess.Back != ScreenMain {
		title = "🔍 <b>Результаты поиска</b>"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n", title)
	fmt.Fprintf(&sb, "Страница %d из %d · %d файлов\n\n", sess.CurrentPage+1, totalPages, total)
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

	kb := storagePageKeyboard(sess.CurrentPage, totalPages)

	if sendFiles {
		// Send media files first; get the ID of the last sent message.
		lastMsgID := b.sendPageFiles(chatID, pageItems)
		// Kick off background prefetch for the next page.
		nextStart := (sess.CurrentPage + 1) * pageSize
		if nextStart < len(sess.Items) {
			nextEnd := min(nextStart+pageSize, len(sess.Items))
			go b.prefetchItems(sess.Items[nextStart:nextEnd])
		}
		if lastMsgID != 0 {
			// Attach navigation keyboard to the last media message so there is only
			// one message total.
			editKb := tgbotapi.NewEditMessageReplyMarkup(chatID, lastMsgID, kb)
			if _, err := b.api.Send(editKb); err != nil {
				slog.Error("sendStoragePage: attach keyboard to media", "error", err)
				// Fallback: send a separate control message.
				lastMsgID = b.editOrSendHTML(chatID, 0, sb.String(), &kb)
			}
			sess.LastMsgID = lastMsgID
		} else {
			// No media was sent (empty page or all failed) — send a plain text message.
			sess.LastMsgID = b.editOrSendHTML(chatID, 0, sb.String(), &kb)
		}
	} else {
		// No new media — edit the existing control message if possible.
		sess.LastMsgID = b.editOrSendHTML(chatID, sess.LastMsgID, sb.String(), &kb)
	}
}

// showSelectMode replaces the navigation keyboard with numbered item buttons.
func (b *Bot) showSelectMode(chatID int64, sess *Session) {
	start := sess.CurrentPage * pageSize
	end := min(start+pageSize, len(sess.Items))
	pageItems := sess.Items[start:end]

	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton
	for i := range pageItems {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(
			strconv.Itoa(i+1),
			fmt.Sprintf("si:%d", i),
		))
		if len(row) == 5 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("Отмена", fmt.Sprintf("sp:%d", sess.CurrentPage)),
	})

	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	sess.LastMsgID = b.editOrSendHTML(chatID, sess.LastMsgID, "Выбери файл:", &kb)
}

// showItem sends (or edits) the item detail message.
func (b *Bot) showItem(chatID int64, sess *Session) {
	if sess.CurrentItem == nil {
		slog.Warn("showItem: no current item, falling back to main menu")
		b.showMainMenu(chatID, sess)
		return
	}
	slog.Info("showItem", "id", sess.CurrentItem.ID, "name", sess.CurrentItem.FileName)
	text := formatItemDetail(sess.CurrentItem)
	kb := itemDetailKeyboard(sess.CurrentItem)
	sess.LastMsgID = b.editOrSendHTML(chatID, sess.LastMsgID, text, &kb)
}
