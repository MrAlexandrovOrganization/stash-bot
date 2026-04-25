package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"stash-bot/internal/stash"
)

const pageSize = 10

type Bot struct {
	api      *tgbotapi.BotAPI
	stash    *stash.Client
	rootID   int64
	sessions sync.Map // int64 (userID) → *Session
}

func New(token string, rootID int64, stashClient *stash.Client) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram bot: %w", err)
	}
	return &Bot{
		api:    api,
		stash:  stashClient,
		rootID: rootID,
	}, nil
}

func (b *Bot) Run() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)
	slog.Info("bot started", "username", b.api.Self.UserName)
	for update := range updates {
		go b.handleUpdate(update)
	}
}

func (b *Bot) session(userID, chatID int64) *Session {
	v, _ := b.sessions.LoadOrStore(userID, &Session{ChatID: chatID, Screen: ScreenMain})
	sess := v.(*Session)
	sess.ChatID = chatID
	return sess
}

// ── Update routing ────────────────────────────────────────────────────────────

func (b *Bot) handleUpdate(update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		b.handleCallback(update.CallbackQuery)
		return
	}
	if update.Message == nil {
		return
	}
	msg := update.Message
	if msg.From.ID != b.rootID {
		slog.Warn("unauthorized", "user_id", msg.From.ID)
		return
	}

	sess := b.session(msg.From.ID, msg.Chat.ID)

	// Media uploads always go to stash, clearing any pending state.
	switch {
	case msg.Photo != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeImage)
		return
	case msg.Video != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeVideo)
		return
	case msg.Animation != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeGIF)
		return
	case msg.Document != nil:
		sess.Pending = ""
		b.handleUpload(msg, stash.MediaTypeDocument)
		return
	}

	if msg.Command() == "start" {
		sess.Pending = ""
		b.showMainMenu(msg.Chat.ID)
		return
	}

	if msg.Text != "" {
		b.handleTextInput(msg, sess)
	}
}

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

// ── Callback routing ──────────────────────────────────────────────────────────

func (b *Bot) handleCallback(cb *tgbotapi.CallbackQuery) {
	b.api.Send(tgbotapi.NewCallback(cb.ID, ""))

	if cb.From.ID != b.rootID {
		slog.Warn("callback: unauthorized", "user_id", cb.From.ID)
		return
	}

	parts := strings.SplitN(cb.Data, ":", 2)
	action := parts[0]
	payload := ""
	if len(parts) == 2 {
		payload = parts[1]
	}

	chatID := cb.Message.Chat.ID
	userID := cb.From.ID
	sess := b.session(userID, chatID)

	slog.Info("callback", "action", action, "payload", payload, "screen", sess.Screen)

	switch action {
	case "noop":
		// Page indicator button — do nothing.

	case "menu":
		sess.Pending = ""
		b.showMainMenu(chatID)

	case "storage":
		sess.Pending = ""
		b.loadStorageAndShow(chatID, sess, true)

	case "search":
		sess.Pending = "search"
		b.sendHTML(chatID, "🔍 Введи поисковый запрос:\n\nФормат: <code>текст #тег -#исключить</code>", cancelKeyboard())

	case "sp": // storage page
		page, err := strconv.Atoi(payload)
		if err != nil {
			slog.Error("callback sp: bad page", "payload", payload)
			return
		}
		totalPages := (len(sess.Items) + pageSize - 1) / pageSize
		if page < 0 || page >= totalPages {
			slog.Warn("callback sp: page out of range", "page", page, "total", totalPages)
			return
		}
		slog.Info("navigate page", "page", page, "total_pages", totalPages)
		sess.CurrentPage = page
		b.sendStoragePage(chatID, sess)

	case "ssel": // enter select mode
		slog.Info("enter select mode", "page", sess.CurrentPage)
		sess.Screen = ScreenSelect
		b.showSelectMode(chatID, sess)

	case "si": // select item by page-index
		idx, err := strconv.Atoi(payload)
		if err != nil {
			slog.Error("callback si: bad index", "payload", payload)
			return
		}
		globalIdx := sess.CurrentPage*pageSize + idx
		if globalIdx < 0 || globalIdx >= len(sess.Items) {
			slog.Error("callback si: index out of range", "global_idx", globalIdx, "total", len(sess.Items))
			return
		}
		sess.CurrentItem = sess.Items[globalIdx]
		sess.Back = sess.Screen
		sess.Screen = ScreenItem
		slog.Info("selected item", "id", sess.CurrentItem.ID, "name", sess.CurrentItem.FileName)
		b.showItem(chatID, sess)

	case "edit":
		if sess.CurrentItem == nil {
			return
		}
		var prompt string
		switch payload {
		case "desc":
			prompt = "Введи новое описание:"
			if sess.CurrentItem.Description != "" {
				prompt += "\n\nТекущее: " + escapeHTML(sanitizeUTF8(sess.CurrentItem.Description))
			}
		case "tags":
			prompt = "Введи теги через запятую или с #:\n<i>Пример: #утро, отдых, #лето</i>"
			if len(sess.CurrentItem.Tags) > 0 {
				current := make([]string, len(sess.CurrentItem.Tags))
				for i, t := range sess.CurrentItem.Tags {
					current[i] = "#" + t
				}
				prompt += "\n\nТекущие: " + escapeHTML(strings.Join(current, " "))
			}
		case "tr":
			prompt = "Введи расшифровку:"
			if sess.CurrentItem.Transcript != nil && *sess.CurrentItem.Transcript != "" {
				t := sanitizeUTF8(*sess.CurrentItem.Transcript)
				if len([]rune(t)) > 200 {
					t = string([]rune(t)[:200]) + "…"
				}
				prompt += "\n\nТекущая: " + escapeHTML(t)
			}
		default:
			slog.Error("callback edit: unknown field", "field", payload)
			return
		}
		slog.Info("start field edit", "field", payload)
		sess.Pending = payload
		b.sendHTML(chatID, prompt, cancelKeyboard())

	case "file":
		if sess.CurrentItem == nil {
			slog.Warn("callback file: no current item")
			return
		}
		slog.Info("send file", "id", sess.CurrentItem.ID, "name", sess.CurrentItem.FileName)
		b.sendFile(chatID, sess.CurrentItem)

	case "del":
		if sess.CurrentItem == nil {
			slog.Warn("callback del: no current item")
			return
		}
		ctx := context.Background()
		slog.Info("deleting item", "id", sess.CurrentItem.ID)
		if err := b.stash.Delete(ctx, sess.CurrentItem.ID); err != nil {
			slog.Error("delete failed", "id", sess.CurrentItem.ID, "error", err)
			b.send(chatID, "Ошибка при удалении.")
			return
		}
		slog.Info("item deleted", "id", sess.CurrentItem.ID)
		for i, it := range sess.Items {
			if it.ID == sess.CurrentItem.ID {
				sess.Items = append(sess.Items[:i], sess.Items[i+1:]...)
				break
			}
		}
		sess.CurrentItem = nil
		sess.Screen = sess.Back
		b.send(chatID, "🗑 Удалено.")
		b.sendStoragePage(chatID, sess)

	case "back":
		sess.Pending = ""
		slog.Info("back", "from", sess.Screen, "to", sess.Back)
		switch sess.Back {
		case ScreenStorage:
			sess.Screen = ScreenStorage
			b.sendStoragePage(chatID, sess)
		default:
			b.showMainMenu(chatID)
		}

	case "cancel":
		sess.Pending = ""
		slog.Info("cancel pending input")
		if sess.Screen == ScreenItem && sess.CurrentItem != nil {
			b.showItem(chatID, sess)
		} else {
			b.sendStoragePage(chatID, sess)
		}
	}
}

// ── Screens ───────────────────────────────────────────────────────────────────

func (b *Bot) showMainMenu(chatID int64) {
	text := "👋 Привет! Я твоё личное хранилище медиа.\n\nЧтобы сохранить файл — просто отправь его сюда.\nЧтобы найти — напиши текст или теги."
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📦 Хранилище", "storage"),
			tgbotapi.NewInlineKeyboardButtonData("🔍 Поиск", "search"),
		),
	)
	b.sendHTML(chatID, text, &kb)
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
	b.sendStoragePage(chatID, sess)
}

// sendStoragePage sends a text list of the current page items with navigation buttons.
// Files are NOT downloaded here — only metadata is shown. Files are sent on demand via "📥 Файл".
func (b *Bot) sendStoragePage(chatID int64, sess *Session) {
	if len(sess.Items) == 0 {
		slog.Info("sendStoragePage: empty")
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔍 Поиск", "search"),
				tgbotapi.NewInlineKeyboardButtonData("🏠 Меню", "menu"),
			),
		)
		b.sendHTML(chatID, "📦 Хранилище пусто.", &kb)
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
	slog.Info("sendStoragePage", "page", sess.CurrentPage, "total_pages", totalPages, "items_on_page", len(pageItems))

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

	// Send files first so the control message with buttons ends up at the bottom.
	b.sendPageFiles(chatID, pageItems)

	kb := storagePageKeyboard(sess.CurrentPage, totalPages)
	b.sendHTML(chatID, sb.String(), &kb)
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
	b.sendHTML(chatID, "Выбери файл:", &kb)
}

// showItem sends the item detail message.
func (b *Bot) showItem(chatID int64, sess *Session) {
	if sess.CurrentItem == nil {
		slog.Warn("showItem: no current item, falling back to main menu")
		b.showMainMenu(chatID)
		return
	}
	slog.Info("showItem", "id", sess.CurrentItem.ID, "name", sess.CurrentItem.FileName)
	text := formatItemDetail(sess.CurrentItem)
	kb := itemDetailKeyboard(sess.CurrentItem)
	b.sendHTML(chatID, text, &kb)
}

// ── Upload ────────────────────────────────────────────────────────────────────

func (b *Bot) handleUpload(msg *tgbotapi.Message, mediaType stash.MediaType) {
	ctx := context.Background()

	fileID, fileName, contentType := extractFileInfo(msg, mediaType)
	if fileID == "" {
		b.send(msg.Chat.ID, "Не удалось получить файл.")
		return
	}
	slog.Info("upload: downloading from Telegram", "file_name", fileName, "content_type", contentType, "media_type", mediaType)

	description, tags := parseCaption(msg.Caption)

	fileURL, err := b.api.GetFileDirectURL(fileID)
	if err != nil {
		slog.Error("upload: get file url", "error", err)
		b.send(msg.Chat.ID, "Не удалось скачать файл из Telegram.")
		return
	}

	resp, err := http.Get(fileURL) //nolint:noctx
	if err != nil {
		slog.Error("upload: http get failed", "error", err)
		b.send(msg.Chat.ID, "Не удалось скачать файл.")
		return
	}
	defer resp.Body.Close()
	slog.Info("upload: uploading to stash", "file_name", fileName, "size", resp.ContentLength)

	item, err := b.stash.Upload(ctx, resp.Body, fileName, contentType, resp.ContentLength, stash.UploadMeta{
		Description: description,
		Tags:        tags,
	})
	if err != nil {
		slog.Error("upload: stash upload failed", "error", err)
		b.send(msg.Chat.ID, "Ошибка при сохранении файла.")
		return
	}
	slog.Info("upload: done", "id", item.ID, "file_name", item.FileName)

	// Invalidate cached items so next storage visit reloads.
	sess := b.session(msg.From.ID, msg.Chat.ID)
	sess.CurrentItem = item
	sess.Screen = ScreenItem
	sess.Back = ScreenStorage
	sess.Items = nil
	b.showItem(msg.Chat.ID, sess)
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
	b.sendStoragePage(chatID, sess)
}

// ── Page media sending ────────────────────────────────────────────────────────

// sendPageFiles sends all media items for a storage page.
// Photos and videos go as a media group; GIFs and documents are sent individually.
// Items with a cached Telegram file_id are sent instantly without touching stash.
func (b *Bot) sendPageFiles(chatID int64, items []*stash.Item) {
	var mediaItems []*stash.Item
	var singleItems []*stash.Item
	for _, it := range items {
		if it.Type == stash.MediaTypeImage || it.Type == stash.MediaTypeVideo {
			mediaItems = append(mediaItems, it)
		} else {
			singleItems = append(singleItems, it)
		}
	}
	if len(mediaItems) > 0 {
		b.sendMediaGroupItems(chatID, mediaItems)
	}
	for _, it := range singleItems {
		b.sendSingleItem(chatID, it)
	}
}

// mediaSlot tracks an item's position in the outgoing media group so we can
// map the response messages back to items and cache their Telegram file_ids.
type mediaSlot struct {
	item  *stash.Item
	isNew bool // downloaded fresh — file_id not yet cached
}

// sendMediaGroupItems sends photos/videos as a Telegram media group.
// Items with TelegramFileID are sent by ID (instant); others are downloaded first.
func (b *Bot) sendMediaGroupItems(chatID int64, items []*stash.Item) {
	ctx := context.Background()
	media := make([]interface{}, 0, len(items))
	slots := make([]mediaSlot, 0, len(items))

	for _, it := range items {
		caption := buildItemCaption(it)

		if it.TelegramFileID != nil && *it.TelegramFileID != "" {
			// Fast path: already known to Telegram.
			slog.Info("sendMediaGroupItems: cached", "id", it.ID)
			fid := tgbotapi.FileID(*it.TelegramFileID)
			var inp interface{}
			switch it.Type {
			case stash.MediaTypeImage:
				m := tgbotapi.NewInputMediaPhoto(fid)
				m.Caption = caption
				m.ParseMode = tgbotapi.ModeHTML
				inp = m
			case stash.MediaTypeVideo:
				m := tgbotapi.NewInputMediaVideo(fid)
				m.Caption = caption
				m.ParseMode = tgbotapi.ModeHTML
				inp = m
			}
			if inp != nil {
				media = append(media, inp)
				slots = append(slots, mediaSlot{it, false})
			}
			continue
		}

		// Slow path: download from stash and buffer in memory.
		slog.Info("sendMediaGroupItems: downloading", "id", it.ID, "name", it.FileName)
		rc, _, err := b.stash.GetFile(ctx, it.ID)
		if err != nil {
			slog.Error("sendMediaGroupItems: get file", "id", it.ID, "error", err)
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			slog.Error("sendMediaGroupItems: read", "id", it.ID, "error", err)
			continue
		}
		fr := tgbotapi.FileReader{Name: it.FileName, Reader: bytes.NewReader(data)}
		var inp interface{}
		switch it.Type {
		case stash.MediaTypeImage:
			m := tgbotapi.NewInputMediaPhoto(fr)
			m.Caption = caption
			m.ParseMode = tgbotapi.ModeHTML
			inp = m
		case stash.MediaTypeVideo:
			m := tgbotapi.NewInputMediaVideo(fr)
			m.Caption = caption
			m.ParseMode = tgbotapi.ModeHTML
			inp = m
		}
		if inp != nil {
			media = append(media, inp)
			slots = append(slots, mediaSlot{it, true})
		}
	}

	if len(media) == 0 {
		return
	}

	slog.Info("sendMediaGroupItems: sending group", "count", len(media))
	sentMsgs, err := b.api.SendMediaGroup(tgbotapi.NewMediaGroup(chatID, media))
	if err != nil {
		slog.Error("sendMediaGroupItems: send", "error", err)
		return
	}

	// Persist file_ids for freshly uploaded items.
	for i, slot := range slots {
		if !slot.isNew || i >= len(sentMsgs) {
			continue
		}
		msg := sentMsgs[i]
		var tgID string
		if len(msg.Photo) > 0 {
			tgID = msg.Photo[len(msg.Photo)-1].FileID
		} else if msg.Video != nil {
			tgID = msg.Video.FileID
		}
		if tgID != "" {
			b.persistTgFileID(slot.item, tgID)
		}
	}
	slog.Info("sendMediaGroupItems: done")
}

// sendSingleItem sends a GIF or document, using cached Telegram file_id when available.
func (b *Bot) sendSingleItem(chatID int64, it *stash.Item) {
	caption := buildItemCaption(it)

	if it.TelegramFileID != nil && *it.TelegramFileID != "" {
		slog.Info("sendSingleItem: cached", "id", it.ID)
		fid := tgbotapi.FileID(*it.TelegramFileID)
		var msg tgbotapi.Chattable
		switch it.Type {
		case stash.MediaTypeGIF:
			m := tgbotapi.NewAnimation(chatID, fid)
			m.Caption = caption
			m.ParseMode = tgbotapi.ModeHTML
			msg = m
		default:
			m := tgbotapi.NewDocument(chatID, fid)
			m.Caption = caption
			m.ParseMode = tgbotapi.ModeHTML
			msg = m
		}
		if _, err := b.api.Send(msg); err != nil {
			slog.Error("sendSingleItem: send by file_id", "id", it.ID, "error", err)
		}
		return
	}

	slog.Info("sendSingleItem: downloading", "id", it.ID, "name", it.FileName)
	ctx := context.Background()
	rc, _, err := b.stash.GetFile(ctx, it.ID)
	if err != nil {
		slog.Error("sendSingleItem: get file", "id", it.ID, "error", err)
		return
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		slog.Error("sendSingleItem: read", "id", it.ID, "error", err)
		return
	}

	fr := tgbotapi.FileReader{Name: it.FileName, Reader: bytes.NewReader(data)}
	var sentMsg tgbotapi.Message
	var sendErr error
	switch it.Type {
	case stash.MediaTypeGIF:
		m := tgbotapi.NewAnimation(chatID, fr)
		m.Caption = caption
		m.ParseMode = tgbotapi.ModeHTML
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && sentMsg.Animation != nil {
			b.persistTgFileID(it, sentMsg.Animation.FileID)
		}
	default:
		m := tgbotapi.NewDocument(chatID, fr)
		m.Caption = caption
		m.ParseMode = tgbotapi.ModeHTML
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && sentMsg.Document != nil {
			b.persistTgFileID(it, sentMsg.Document.FileID)
		}
	}
	if sendErr != nil {
		slog.Error("sendSingleItem: send", "id", it.ID, "error", sendErr)
	}
}

// buildItemCaption returns a short caption for an item (description + tags).
func buildItemCaption(it *stash.Item) string {
	var parts []string
	if d := sanitizeUTF8(it.Description); d != "" {
		parts = append(parts, escapeHTML(d))
	}
	if len(it.Tags) > 0 {
		tagged := make([]string, len(it.Tags))
		for i, t := range it.Tags {
			tagged[i] = "#" + t
		}
		parts = append(parts, escapeHTML(strings.Join(tagged, " ")))
	}
	result := strings.Join(parts, "\n")
	if len(result) > 1020 {
		result = result[:1020] + "…"
	}
	return result
}

// ── File sending ──────────────────────────────────────────────────────────────

// sendFile sends a stash item to Telegram.
// If item.TelegramFileID is already set (persisted in stash DB), the file is sent
// directly by Telegram file_id — no download required.
// On first upload the received file_id is saved back to stash asynchronously.
func (b *Bot) sendFile(chatID int64, item *stash.Item) {
	caption := sanitizeUTF8(item.Description)

	// Fast path: Telegram file_id already persisted in stash.
	if item.TelegramFileID != nil && *item.TelegramFileID != "" {
		slog.Info("sendFile: using persisted tg file_id", "id", item.ID, "tg_id", *item.TelegramFileID)
		b.sendByTgFileID(chatID, *item.TelegramFileID, item.Type, caption)
		return
	}

	// Slow path: download from stash, upload to Telegram, then persist file_id.
	ctx := context.Background()
	slog.Info("sendFile: downloading from stash", "id", item.ID, "name", item.FileName)

	rc, _, err := b.stash.GetFile(ctx, item.ID)
	if err != nil {
		slog.Error("sendFile: get file failed", "id", item.ID, "error", err)
		b.send(chatID, "Не удалось получить файл.")
		return
	}

	// Buffer fully so the stash connection is closed before Telegram upload starts.
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		slog.Error("sendFile: read failed", "id", item.ID, "error", err)
		b.send(chatID, "Ошибка при чтении файла.")
		return
	}
	slog.Info("sendFile: uploading to Telegram", "id", item.ID, "bytes", len(data))

	fr := tgbotapi.FileReader{Name: item.FileName, Reader: bytes.NewReader(data)}
	var sentMsg tgbotapi.Message
	var sendErr error

	switch item.Type {
	case stash.MediaTypeImage:
		m := tgbotapi.NewPhoto(chatID, fr)
		m.Caption = caption
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && len(sentMsg.Photo) > 0 {
			b.persistTgFileID(item, sentMsg.Photo[len(sentMsg.Photo)-1].FileID)
		}
	case stash.MediaTypeVideo:
		m := tgbotapi.NewVideo(chatID, fr)
		m.Caption = caption
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && sentMsg.Video != nil {
			b.persistTgFileID(item, sentMsg.Video.FileID)
		}
	case stash.MediaTypeGIF:
		sentMsg, sendErr = b.api.Send(tgbotapi.NewAnimation(chatID, fr))
		if sendErr == nil && sentMsg.Animation != nil {
			b.persistTgFileID(item, sentMsg.Animation.FileID)
		}
	default:
		m := tgbotapi.NewDocument(chatID, fr)
		m.Caption = caption
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && sentMsg.Document != nil {
			b.persistTgFileID(item, sentMsg.Document.FileID)
		}
	}

	if sendErr != nil {
		slog.Error("sendFile: send failed", "id", item.ID, "error", sendErr)
	} else {
		slog.Info("sendFile: done", "id", item.ID)
	}
}

// persistTgFileID saves the Telegram file_id into the item (for in-session reuse)
// and asynchronously persists it to stash so it survives bot restarts.
func (b *Bot) persistTgFileID(item *stash.Item, tgFileID string) {
	item.TelegramFileID = &tgFileID // update in-memory item (session cache)
	go func() {
		ctx := context.Background()
		if _, err := b.stash.Update(ctx, item.ID, stash.UpdateMeta{TelegramFileID: &tgFileID}); err != nil {
			slog.Error("persistTgFileID: stash update failed", "id", item.ID, "error", err)
		} else {
			slog.Info("persistTgFileID: saved to stash", "id", item.ID, "tg_id", tgFileID)
		}
	}()
}

// sendByTgFileID sends a file by its Telegram file_id (no re-upload).
func (b *Bot) sendByTgFileID(chatID int64, fileID string, mediaType stash.MediaType, caption string) {
	var msg tgbotapi.Chattable
	switch mediaType {
	case stash.MediaTypeImage:
		m := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(fileID))
		m.Caption = caption
		msg = m
	case stash.MediaTypeVideo:
		m := tgbotapi.NewVideo(chatID, tgbotapi.FileID(fileID))
		m.Caption = caption
		msg = m
	case stash.MediaTypeGIF:
		msg = tgbotapi.NewAnimation(chatID, tgbotapi.FileID(fileID))
	default:
		m := tgbotapi.NewDocument(chatID, tgbotapi.FileID(fileID))
		m.Caption = caption
		msg = m
	}
	if _, err := b.api.Send(msg); err != nil {
		slog.Error("sendByTgFileID: failed", "tg_id", fileID, "error", err)
	}
}

// ── Keyboards ─────────────────────────────────────────────────────────────────

func storagePageKeyboard(page, totalPages int) tgbotapi.InlineKeyboardMarkup {
	var navRow []tgbotapi.InlineKeyboardButton
	if page > 0 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("◀", fmt.Sprintf("sp:%d", page-1)))
	}
	navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(
		fmt.Sprintf("%d / %d", page+1, totalPages), "noop",
	))
	if page < totalPages-1 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("▶", fmt.Sprintf("sp:%d", page+1)))
	}

	actionRow := tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Выбрать", "ssel"),
		tgbotapi.NewInlineKeyboardButtonData("🔍 Поиск", "search"),
		tgbotapi.NewInlineKeyboardButtonData("🏠 Меню", "menu"),
	)

	return tgbotapi.NewInlineKeyboardMarkup(navRow, actionRow)
}

func itemDetailKeyboard(item *stash.Item) tgbotapi.InlineKeyboardMarkup {
	// Edit buttons — one per editable field.
	editRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("✏️ Описание", "edit:desc"),
		tgbotapi.NewInlineKeyboardButtonData("🏷 Теги", "edit:tags"),
	}
	if item.Type == stash.MediaTypeVideo {
		editRow = append(editRow, tgbotapi.NewInlineKeyboardButtonData("📄 Расшифровка", "edit:tr"))
	}

	actionRow := tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("📥 Файл", "file"),
		tgbotapi.NewInlineKeyboardButtonData("🗑 Удалить", "del"),
		tgbotapi.NewInlineKeyboardButtonData("◀ Назад", "back"),
	)

	return tgbotapi.NewInlineKeyboardMarkup(editRow, actionRow)
}

func cancelKeyboard() *tgbotapi.InlineKeyboardMarkup {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Отмена", "cancel"),
		),
	)
	return &kb
}

// ── Formatting ────────────────────────────────────────────────────────────────

// itemFields defines the display fields for an item — add/remove here to extend.
var itemFields = []struct {
	label   string
	extract func(*stash.Item) string
}{
	{"📝 Описание", func(it *stash.Item) string { return it.Description }},
	{"🏷 Теги", func(it *stash.Item) string {
		if len(it.Tags) == 0 {
			return ""
		}
		tagged := make([]string, len(it.Tags))
		for i, t := range it.Tags {
			tagged[i] = "#" + t
		}
		return strings.Join(tagged, " ")
	}},
	{"📄 Расшифровка", func(it *stash.Item) string {
		if it.Transcript != nil {
			return *it.Transcript
		}
		return ""
	}},
}

func formatItemDetail(item *stash.Item) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>%s</b>\n\n", mediaIcon(item.Type), escapeHTML(sanitizeUTF8(item.FileName)))

	for _, f := range itemFields {
		val := sanitizeUTF8(f.extract(item))
		if val != "" {
			if len([]rune(val)) > 400 {
				val = string([]rune(val)[:400]) + "…"
			}
			fmt.Fprintf(&sb, "<b>%s:</b> %s\n", f.label, escapeHTML(val))
		} else {
			fmt.Fprintf(&sb, "<b>%s:</b> <i>не задано</i>\n", f.label)
		}
	}

	fmt.Fprintf(&sb, "\n<code>%s</code>", item.ID)
	return sb.String()
}

func mediaIcon(t stash.MediaType) string {
	switch t {
	case stash.MediaTypeImage:
		return "🖼"
	case stash.MediaTypeVideo:
		return "🎬"
	case stash.MediaTypeGIF:
		return "🎞"
	default:
		return "📄"
	}
}

// ── Parse helpers ─────────────────────────────────────────────────────────────

// parseCaption splits a file caption into description and #tags.
func parseCaption(text string) (description string, tags []string) {
	var descParts []string
	for word := range strings.SplitSeq(text, " ") {
		if strings.HasPrefix(word, "#") {
			if tag := strings.TrimPrefix(word, "#"); tag != "" {
				tags = append(tags, tag)
			}
		} else {
			descParts = append(descParts, word)
		}
	}
	return strings.TrimSpace(strings.Join(descParts, " ")), tags
}

// parseSearchQuery splits a search query into text, positive tags (#tag), and negative tags (-#tag).
func parseSearchQuery(text string) (description string, posTags, negTags []string) {
	var descParts []string
	for word := range strings.SplitSeq(text, " ") {
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}
		switch {
		case strings.HasPrefix(word, "-#"):
			if tag := strings.TrimPrefix(word, "-#"); tag != "" {
				negTags = append(negTags, tag)
			}
		case strings.HasPrefix(word, "#"):
			if tag := strings.TrimPrefix(word, "#"); tag != "" {
				posTags = append(posTags, tag)
			}
		default:
			descParts = append(descParts, word)
		}
	}
	return strings.TrimSpace(strings.Join(descParts, " ")), posTags, negTags
}

// parseTags parses comma- or space-separated tags, with or without #.
func parseTags(text string) []string {
	text = strings.ReplaceAll(text, ",", " ")
	var tags []string
	for part := range strings.SplitSeq(text, " ") {
		if t := strings.TrimSpace(strings.TrimPrefix(part, "#")); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func hasAnyTag(itemTags, checkTags []string) bool {
	for _, ct := range checkTags {
		ctLow := strings.ToLower(ct)
		for _, it := range itemTags {
			if strings.ToLower(it) == ctLow {
				return true
			}
		}
	}
	return false
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// sanitizeUTF8 replaces invalid UTF-8 sequences with "?" to prevent Telegram API errors.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "?")
}

// ── Send helpers ──────────────────────────────────────────────────────────────

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

// ── File extraction ───────────────────────────────────────────────────────────

func extractFileInfo(msg *tgbotapi.Message, mediaType stash.MediaType) (fileID, fileName, contentType string) {
	switch mediaType {
	case stash.MediaTypeImage:
		if len(msg.Photo) == 0 {
			return "", "", ""
		}
		p := msg.Photo[len(msg.Photo)-1]
		return p.FileID, fmt.Sprintf("photo_%s.jpg", p.FileUniqueID), "image/jpeg"
	case stash.MediaTypeVideo:
		v := msg.Video
		name := v.FileName
		if name == "" {
			name = fmt.Sprintf("video_%s.mp4", v.FileUniqueID)
		}
		ct := v.MimeType
		if ct == "" {
			ct = "video/mp4"
		}
		return v.FileID, name, ct
	case stash.MediaTypeGIF:
		a := msg.Animation
		name := a.FileName
		if name == "" {
			name = fmt.Sprintf("animation_%s.mp4", a.FileUniqueID)
		}
		return a.FileID, name, "video/mp4"
	case stash.MediaTypeDocument:
		d := msg.Document
		ct := d.MimeType
		if ct == "" {
			ct = "application/octet-stream"
		}
		return d.FileID, d.FileName, ct
	}
	return "", "", ""
}
