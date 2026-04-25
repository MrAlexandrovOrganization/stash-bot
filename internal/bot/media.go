package bot

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"

	"stash-bot/internal/stash"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Page media sending ────────────────────────────────────────────────────────

// sendPageFiles sends all media items for a storage page and returns the message ID
// of the last sent message (used to attach navigation controls to it).
// Photos and videos go as a media group; GIFs and documents are sent individually.
// Items with a cached Telegram file_id are sent instantly without touching stash.
func sendPageFiles(b *Bot, chatID int64, items []*stash.Item) int {
	var mediaItems []*stash.Item
	var singleItems []*stash.Item
	for _, it := range items {
		if it.Type == stash.MediaTypeImage || it.Type == stash.MediaTypeVideo {
			mediaItems = append(mediaItems, it)
		} else {
			singleItems = append(singleItems, it)
		}
	}
	var lastMsgID int
	if len(mediaItems) > 0 {
		lastMsgID = sendMediaGroupItems(b, chatID, mediaItems)
	}
	for _, it := range singleItems {
		if id := sendSingleItem(b, chatID, it); id != 0 {
			lastMsgID = id
		}
	}
	return lastMsgID
}

// mediaSlot tracks an item's position in the outgoing media group so we can
// map the response messages back to items and cache their Telegram file_ids.
type mediaSlot struct {
	item  *stash.Item
	isNew bool // downloaded fresh — file_id not yet cached
}

// sendMediaGroupItems sends photos/videos as a Telegram media group and returns the
// message ID of the last message in the group (for attaching navigation controls).
func sendMediaGroupItems(b *Bot, chatID int64, items []*stash.Item) int {
	ctx := context.Background()
	media := make([]any, 0, len(items))
	slots := make([]mediaSlot, 0, len(items))

	for _, it := range items {
		caption := buildItemCaption(it)

		if it.TelegramFileID != nil && *it.TelegramFileID != "" {
			// Fast path: already known to Telegram.
			slog.Info("sendMediaGroupItems: cached", "id", it.ID)
			fid := tgbotapi.FileID(*it.TelegramFileID)
			var inp any
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

		// Slow(er) path: use prefetched cache or download from stash.
		var data []byte
		if cached, ok := b.fileCache.LoadAndDelete(it.ID); ok {
			slog.Info("sendMediaGroupItems: using prefetch cache", "id", it.ID)
			data = cached.([]byte)
		} else {
			slog.Info("sendMediaGroupItems: downloading", "id", it.ID, "name", it.FileName)
			rc, _, err := b.stash.GetFile(ctx, it.ID)
			if err != nil {
				slog.Error("sendMediaGroupItems: get file", "id", it.ID, "error", err)
				continue
			}
			var readErr error
			data, readErr = io.ReadAll(rc)
			rc.Close()
			if readErr != nil {
				slog.Error("sendMediaGroupItems: read", "id", it.ID, "error", readErr)
				continue
			}
		}
		fr := tgbotapi.FileReader{Name: it.FileName, Reader: bytes.NewReader(data)}
		var inp any
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
		return 0
	}

	slog.Info("sendMediaGroupItems: sending group", "count", len(media))
	sentMsgs, err := b.api.SendMediaGroup(tgbotapi.NewMediaGroup(chatID, media))
	if err != nil {
		slog.Error("sendMediaGroupItems: send", "error", err)
		return 0
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
			persistTgFileID(b, slot.item, tgID)
		}
	}
	slog.Info("sendMediaGroupItems: done")
	return sentMsgs[len(sentMsgs)-1].MessageID
}

// sendSingleItem sends a GIF or document, using cached Telegram file_id when available.
// Returns the sent message ID (0 on failure).
func sendSingleItem(b *Bot, chatID int64, it *stash.Item) int {
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
		sent, err := b.api.Send(msg)
		if err != nil {
			slog.Error("sendSingleItem: send by file_id", "id", it.ID, "error", err)
			return 0
		}
		return sent.MessageID
	}

	ctx := context.Background()
	var data []byte
	if cached, ok := b.fileCache.LoadAndDelete(it.ID); ok {
		slog.Info("sendSingleItem: using prefetch cache", "id", it.ID)
		data = cached.([]byte)
	} else {
		slog.Info("sendSingleItem: downloading", "id", it.ID, "name", it.FileName)
		rc, _, err := b.stash.GetFile(ctx, it.ID)
		if err != nil {
			slog.Error("sendSingleItem: get file", "id", it.ID, "error", err)
			return 0
		}
		var readErr error
		data, readErr = io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			slog.Error("sendSingleItem: read", "id", it.ID, "error", readErr)
			return 0
		}
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
			persistTgFileID(b, it, sentMsg.Animation.FileID)
		}
	default:
		m := tgbotapi.NewDocument(chatID, fr)
		m.Caption = caption
		m.ParseMode = tgbotapi.ModeHTML
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && sentMsg.Document != nil {
			persistTgFileID(b, it, sentMsg.Document.FileID)
		}
	}
	if sendErr != nil {
		slog.Error("sendSingleItem: send", "id", it.ID, "error", sendErr)
		return 0
	}
	return sentMsg.MessageID
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
func sendFile(b *Bot, chatID int64, item *stash.Item) {
	caption := sanitizeUTF8(item.Description)

	// Fast path: Telegram file_id already persisted in stash.
	if item.TelegramFileID != nil && *item.TelegramFileID != "" {
		slog.Info("sendFile: using persisted tg file_id", "id", item.ID, "tg_id", *item.TelegramFileID)
		sendByTgFileID(b, chatID, *item.TelegramFileID, item.Type, caption)
		return
	}

	// Slow path: download from stash, upload to Telegram, then persist file_id.
	ctx := context.Background()
	slog.Info("sendFile: downloading from stash", "id", item.ID, "name", item.FileName)

	rc, _, err := b.stash.GetFile(ctx, item.ID)
	if err != nil {
		slog.Error("sendFile: get file failed", "id", item.ID, "error", err)
		send(b, chatID, "Не удалось получить файл.")
		return
	}

	// Buffer fully so the stash connection is closed before Telegram upload starts.
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		slog.Error("sendFile: read failed", "id", item.ID, "error", err)
		send(b, chatID, "Ошибка при чтении файла.")
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
			persistTgFileID(b, item, sentMsg.Photo[len(sentMsg.Photo)-1].FileID)
		}
	case stash.MediaTypeVideo:
		m := tgbotapi.NewVideo(chatID, fr)
		m.Caption = caption
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && sentMsg.Video != nil {
			persistTgFileID(b, item, sentMsg.Video.FileID)
		}
	case stash.MediaTypeGIF:
		sentMsg, sendErr = b.api.Send(tgbotapi.NewAnimation(chatID, fr))
		if sendErr == nil && sentMsg.Animation != nil {
			persistTgFileID(b, item, sentMsg.Animation.FileID)
		}
	default:
		m := tgbotapi.NewDocument(chatID, fr)
		m.Caption = caption
		sentMsg, sendErr = b.api.Send(m)
		if sendErr == nil && sentMsg.Document != nil {
			persistTgFileID(b, item, sentMsg.Document.FileID)
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
func persistTgFileID(b *Bot, item *stash.Item, tgFileID string) {
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
func sendByTgFileID(b *Bot, chatID int64, fileID string, mediaType stash.MediaType, caption string) {
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

// prefetchItems downloads stash bytes for items that don't yet have a Telegram file_id,
// storing them in b.fileCache so the next sendMediaGroupItems / sendSingleItem call
// can skip the stash download entirely. Runs in a background goroutine.
func prefetchItems(b *Bot, items []*stash.Item) {
	ctx := context.Background()
	for _, it := range items {
		if it.TelegramFileID != nil && *it.TelegramFileID != "" {
			continue // already cached in Telegram — no download needed
		}
		if _, ok := b.fileCache.Load(it.ID); ok {
			continue // already prefetched
		}
		slog.Info("prefetch: downloading", "id", it.ID)
		rc, _, err := b.stash.GetFile(ctx, it.ID)
		if err != nil {
			slog.Error("prefetch: get file", "id", it.ID, "error", err)
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			slog.Error("prefetch: read", "id", it.ID, "error", err)
			continue
		}
		b.fileCache.Store(it.ID, data)
		slog.Info("prefetch: cached", "id", it.ID, "bytes", len(data))
	}
}
