package bot

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ── Page media sending ────────────────────────────────────────────────────────

// sendPageFilesCollectIDs sends all media items for a storage page and returns
// all sent message IDs (used to track which messages to delete on navigation).
// Photos and videos go as a media group; GIFs and documents are sent individually.
func sendPageFilesCollectIDs(b *Bot, chatID int64, items []*stash.Item) []int {
	var mediaItems []*stash.Item
	var singleItems []*stash.Item
	for _, it := range items {
		if it.Type == stash.MediaTypeImage || it.Type == stash.MediaTypeVideo {
			mediaItems = append(mediaItems, it)
		} else {
			singleItems = append(singleItems, it)
		}
	}
	var ids []int
	if len(mediaItems) > 0 {
		ids = append(ids, sendMediaGroupItems(b, chatID, mediaItems)...)
	}

	// GIFs/documents are sent individually; do it concurrently so a slow upload
	// of one file doesn't delay the rest of the page.
	if len(singleItems) > 0 {
		results := make([]int, len(singleItems))
		var wg sync.WaitGroup
		for i, it := range singleItems {
			i, it := i, it
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = sendSingleItem(b, chatID, it)
			}()
		}
		wg.Wait()
		for _, id := range results {
			if id != 0 {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// mediaSlot tracks an item's position in the outgoing media group so we can
// map the response messages back to items and cache their Telegram file_ids.
type mediaSlot struct {
	item  *stash.Item
	isNew bool // downloaded fresh — file_id not yet cached
}

// sendMediaGroupItems sends photos/videos as a Telegram media group and returns
// all message IDs in the group. Inputs (and, for uncached items, the downloads
// from stash) are prepared concurrently; the group is then uploaded in one call.
func sendMediaGroupItems(b *Bot, chatID int64, items []*stash.Item) []int {
	type prepared struct {
		input telego.InputMedia
		isNew bool
	}
	preparedList := make([]prepared, len(items))

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, it := range items {
		i, it := i, it
		wg.Add(1)
		go func() {
			defer wg.Done()
			inp, isNew, err := prepareMediaInput(b, it)
			if err != nil {
				slog.Error("sendMediaGroupItems: prepare input", "id", it.ID, "error", err)
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			preparedList[i] = prepared{input: inp, isNew: isNew}
		}()
	}
	wg.Wait()

	// Assemble inputs in original order, preserving the item→slot mapping.
	var media []telego.InputMedia
	var slots []mediaSlot
	for i, p := range preparedList {
		if p.input == nil {
			continue
		}
		media = append(media, p.input)
		slots = append(slots, mediaSlot{items[i], p.isNew})
	}

	if len(media) == 0 {
		return nil
	}

	slog.Info("sendMediaGroupItems: sending group", "input_count", len(media))
	sentMsgs, err := b.api.SendMediaGroup(context.Background(), tu.MediaGroup(tu.ID(chatID), media...))
	if err != nil {
		slog.Error("sendMediaGroupItems: send", "error", err)
		return nil
	}
	if len(sentMsgs) != len(media) {
		slog.Warn("sendMediaGroupItems: sent/received count mismatch — some items may not be cached",
			"input_count", len(media), "sent_count", len(sentMsgs))
	}
	for i, m := range sentMsgs {
		slog.Info("media group result", "index", i,
			"has_photo", len(m.Photo) > 0,
			"has_video", m.Video != nil,
			"has_animation", m.Animation != nil,
			"has_document", m.Document != nil)
	}

	cacheMediaGroupFileIDs(b, slots, sentMsgs)

	ids := make([]int, len(sentMsgs))
	for i, msg := range sentMsgs {
		ids[i] = msg.MessageID
	}
	slog.Info("sendMediaGroupItems: done", "count", len(ids))
	return ids
}

// sendSingleItem sends a GIF or document for the gallery page view.
// No caption is set — the page text will be applied to the last message after all items are sent.
// Returns the sent message ID (0 on failure).
func sendSingleItem(b *Bot, chatID int64, it *stash.Item) int {
	// Fast path: already cached in Telegram.
	if fid, ok := b.lookupFileID(it.ID); ok {
		slog.Info("sendSingleItem: cached", "id", it.ID)
		it.TelegramFileID = &fid
		return sendSingleFromCache(b, chatID, it, "")
	}
	if it.TelegramFileID != nil && *it.TelegramFileID != "" {
		slog.Info("sendSingleItem: cached", "id", it.ID)
		return sendSingleFromCache(b, chatID, it, "")
	}

	// Slow path: load data, upload, cache result.
	slog.Info("sendSingleItem: uploading", "id", it.ID)
	data, err := loadItemData(b, it)
	if err != nil {
		slog.Error("sendSingleItem: load data", "id", it.ID, "error", err)
		return 0
	}
	return uploadSingleItem(b, chatID, it, data, "")
}

// ── File sending ──────────────────────────────────────────────────────────────

// sendFile sends a stash item to Telegram.
// If item.TelegramFileID is already set, the file is sent directly by Telegram file_id.
// On first upload the received file_id is saved back to stash asynchronously.
func sendFile(b *Bot, chatID int64, item *stash.Item) {
	ctx := context.Background()
	caption := sanitizeUTF8(item.Description)

	// Fast path: Telegram file_id already cached/persisted.
	if fid, ok := b.lookupFileID(item.ID); ok {
		slog.Info("sendFile: using cached tg file_id", "id", item.ID)
		sendByTgFileID(b, chatID, fid, item.Type, caption)
		return
	}
	if item.TelegramFileID != nil && *item.TelegramFileID != "" {
		slog.Info("sendFile: using cached tg file_id", "id", item.ID)
		sendByTgFileID(b, chatID, *item.TelegramFileID, item.Type, caption)
		return
	}

	// Slow path: download from stash, upload to Telegram, then persist file_id.
	slog.Info("sendFile: downloading from stash", "id", item.ID, "name", item.FileName)
	data, err := loadItemData(b, item)
	if err != nil {
		slog.Error("sendFile: load data", "id", item.ID, "error", err)
		send(b, chatID, "Не удалось получить файл.")
		return
	}

	slog.Info("sendFile: uploading to Telegram", "id", item.ID, "bytes", len(data))
	sent, tgID, err := sendFileByType(ctx, b, chatID, item.Type, item.FileName, data, caption)
	if err != nil {
		slog.Error("sendFile: send failed", "id", item.ID, "error", err)
		return
	}
	_ = sent
	if tgID != "" {
		persistTgFileID(b, item, tgID)
	}
	slog.Info("sendFile: done", "id", item.ID)
}

// ── Media group helpers ───────────────────────────────────────────────────────

// prepareMediaInput builds a Telegram media group input for one item.
// Returns (input, isNew, error) where isNew=true means file_id must be cached after send.
// No caption is set here — the page text is applied to the last message after the group is sent.
func prepareMediaInput(b *Bot, it *stash.Item) (telego.InputMedia, bool, error) {
	if fid, ok := b.lookupFileID(it.ID); ok {
		slog.Info("prepareMediaInput: cached (fileIDCache)", "id", it.ID, "type", it.Type)
		inp := buildGroupInput(it.Type, tu.FileFromID(fid))
		return inp, false, nil
	}
	if it.TelegramFileID != nil && *it.TelegramFileID != "" {
		slog.Info("prepareMediaInput: cached (item)", "id", it.ID, "type", it.Type)
		inp := buildGroupInput(it.Type, tu.FileFromID(*it.TelegramFileID))
		return inp, false, nil
	}

	slog.Info("prepareMediaInput: DOWNLOAD from backend required", "id", it.ID, "type", it.Type)
	data, err := loadItemData(b, it)
	if err != nil {
		return nil, false, err
	}
	inp := buildGroupInput(it.Type, tu.FileFromBytes(data, it.FileName))
	return inp, true, nil
}

// cacheMediaGroupFileIDs persists Telegram file_ids for freshly uploaded items in a group.
//
// Telegram may return album messages in an order different from the one we sent,
// or drop an element entirely. We therefore do NOT rely on positional mapping:
// each fresh slot is matched to an unused response message by media type first,
// then to any remaining message. This keeps video file_ids from being silently
// dropped (which previously caused re-downloads on every storage open).
func cacheMediaGroupFileIDs(b *Bot, slots []mediaSlot, sentMsgs []telego.Message) {
	used := make([]bool, len(sentMsgs))
	for _, slot := range slots {
		if !slot.isNew {
			continue
		}
		tgID := bestFileIDForType(slot.item.Type, sentMsgs, used)
		if tgID == "" {
			slog.Warn("cacheMediaGroupFileIDs: no file_id found for item",
				"id", slot.item.ID, "type", slot.item.Type, "sent_count", len(sentMsgs))
			continue
		}
		slog.Info("cacheMediaGroupFileIDs: cached file_id", "id", slot.item.ID, "type", slot.item.Type)
		persistTgFileID(b, slot.item, tgID)
	}
}

// bestFileIDForType returns a file_id for the given media type from an unused
// response message. It prefers an exact type match, then falls back to any media.
func bestFileIDForType(mt stash.MediaType, msgs []telego.Message, used []bool) string {
	for j, m := range msgs {
		if used[j] {
			continue
		}
		if id := exactTypeFileID(mt, m); id != "" {
			used[j] = true
			return id
		}
	}
	for j, m := range msgs {
		if used[j] {
			continue
		}
		if id := anyFileID(m); id != "" {
			used[j] = true
			return id
		}
	}
	return ""
}

// exactTypeFileID returns a file_id only if the message is of the expected type.
func exactTypeFileID(mt stash.MediaType, m telego.Message) string {
	switch mt {
	case stash.MediaTypeImage:
		if len(m.Photo) > 0 {
			return m.Photo[len(m.Photo)-1].FileID
		}
	case stash.MediaTypeVideo:
		if m.Video != nil {
			return m.Video.FileID
		}
	case stash.MediaTypeGIF:
		if m.Animation != nil {
			return m.Animation.FileID
		}
	default:
		if m.Document != nil {
			return m.Document.FileID
		}
	}
	return ""
}

// anyFileID returns whichever file_id a message carries, preferring video/document
// over photo (photos are cheap to re-fetch but we still need something stable).
func anyFileID(m telego.Message) string {
	if m.Video != nil {
		return m.Video.FileID
	}
	if m.Animation != nil {
		return m.Animation.FileID
	}
	if m.Document != nil {
		return m.Document.FileID
	}
	if len(m.Photo) > 0 {
		return m.Photo[len(m.Photo)-1].FileID
	}
	return ""
}

// ── Single item helpers ───────────────────────────────────────────────────────

// sendSingleFromCache sends a GIF or document using its cached Telegram file_id.
func sendSingleFromCache(b *Bot, chatID int64, it *stash.Item, caption string) int {
	ctx := context.Background()
	file := tu.FileFromID(*it.TelegramFileID)
	sent, err := sendSingleByType(ctx, b, chatID, it.Type, file, caption)
	if err != nil {
		slog.Error("sendSingleFromCache: send", "id", it.ID, "error", err)
		return 0
	}
	return sent.MessageID
}

// uploadSingleItem uploads a GIF or document from raw bytes and caches the returned file_id.
func uploadSingleItem(b *Bot, chatID int64, it *stash.Item, data []byte, caption string) int {
	ctx := context.Background()
	file := tu.FileFromBytes(data, it.FileName)
	sent, err := sendSingleByType(ctx, b, chatID, it.Type, file, caption)
	if err != nil {
		slog.Error("uploadSingleItem: send", "id", it.ID, "error", err)
		return 0
	}
	if tgID := extractSentFileID(it.Type, *sent); tgID != "" {
		persistTgFileID(b, it, tgID)
	}
	return sent.MessageID
}

// ── Low-level builders ────────────────────────────────────────────────────────

// loadItemData returns raw bytes for an item: from the prefetch cache if available,
// otherwise by downloading from stash.
func loadItemData(b *Bot, it *stash.Item) ([]byte, error) {
	if cached, ok := b.fileCache.LoadAndDelete(it.ID); ok {
		slog.Info("loadItemData: prefetch hit", "id", it.ID)
		return cached.([]byte), nil
	}
	slog.Info("loadItemData: downloading", "id", it.ID, "name", it.FileName)
	rc, _, err := b.stash.GetFile(context.Background(), it.ID)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	return data, err
}

// buildGroupInput builds an InputMediaPhoto or InputMediaVideo for use in a media group.
// No caption is set — the page text is applied to the last message after the group is sent.
func buildGroupInput(mediaType stash.MediaType, src telego.InputFile) telego.InputMedia {
	switch mediaType {
	case stash.MediaTypeImage:
		return tu.MediaPhoto(src)
	case stash.MediaTypeVideo:
		return tu.MediaVideo(src)
	}
	return nil
}

// sendSingleByType sends a GIF or document and returns the sent message.
func sendSingleByType(ctx context.Context, b *Bot, chatID int64, mediaType stash.MediaType, file telego.InputFile, caption string) (*telego.Message, error) {
	switch mediaType {
	case stash.MediaTypeGIF:
		params := tu.Animation(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption).WithParseMode(telego.ModeHTML)
		}
		return b.api.SendAnimation(ctx, params)
	default:
		params := tu.Document(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption).WithParseMode(telego.ModeHTML)
		}
		return b.api.SendDocument(ctx, params)
	}
}

// sendFileByType sends any media type and returns (message, file_id, error).
func sendFileByType(ctx context.Context, b *Bot, chatID int64, mediaType stash.MediaType, fileName string, data []byte, caption string) (*telego.Message, string, error) {
	file := tu.FileFromBytes(data, fileName)
	switch mediaType {
	case stash.MediaTypeImage:
		params := tu.Photo(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		sent, err := b.api.SendPhoto(ctx, params)
		if err != nil {
			return nil, "", err
		}
		tgID := ""
		if len(sent.Photo) > 0 {
			tgID = sent.Photo[len(sent.Photo)-1].FileID
		}
		return sent, tgID, nil
	case stash.MediaTypeVideo:
		params := tu.Video(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		sent, err := b.api.SendVideo(ctx, params)
		if err != nil {
			return nil, "", err
		}
		tgID := ""
		if sent.Video != nil {
			tgID = sent.Video.FileID
		}
		return sent, tgID, nil
	case stash.MediaTypeGIF:
		sent, err := b.api.SendAnimation(ctx, tu.Animation(tu.ID(chatID), file))
		if err != nil {
			return nil, "", err
		}
		tgID := ""
		if sent.Animation != nil {
			tgID = sent.Animation.FileID
		}
		return sent, tgID, nil
	default:
		params := tu.Document(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		sent, err := b.api.SendDocument(ctx, params)
		if err != nil {
			return nil, "", err
		}
		tgID := ""
		if sent.Document != nil {
			tgID = sent.Document.FileID
		}
		return sent, tgID, nil
	}
}

// extractSentFileID extracts the Telegram file_id from a sent message.
// It prefers the field matching the expected media type, but falls back to any
// media Telegram actually returned — a video can come back as a document
// message inside a media group, in which case only .Document is populated.
func extractSentFileID(mediaType stash.MediaType, sent telego.Message) string {
	switch mediaType {
	case stash.MediaTypeImage:
		if len(sent.Photo) > 0 {
			return sent.Photo[len(sent.Photo)-1].FileID
		}
	case stash.MediaTypeVideo:
		if sent.Video != nil {
			return sent.Video.FileID
		}
	case stash.MediaTypeGIF:
		if sent.Animation != nil {
			return sent.Animation.FileID
		}
	default:
		if sent.Document != nil {
			return sent.Document.FileID
		}
	}
	// Fallback: grab whichever media field Telegram populated.
	if sent.Video != nil {
		return sent.Video.FileID
	}
	if sent.Animation != nil {
		return sent.Animation.FileID
	}
	if sent.Document != nil {
		return sent.Document.FileID
	}
	if len(sent.Photo) > 0 {
		return sent.Photo[len(sent.Photo)-1].FileID
	}
	return ""
}

// persistTgFileID saves the Telegram file_id into the item (for in-session reuse),
// into the durable Bot cache, and asynchronously persists it to stash so it
// survives bot restarts and sess.Items being reset.
func persistTgFileID(b *Bot, item *stash.Item, tgFileID string) {
	item.TelegramFileID = &tgFileID
	b.cacheFileID(item.ID, tgFileID)
	go func() {
		if _, err := b.stash.Update(context.Background(), item.ID, stash.UpdateMeta{TelegramFileID: &tgFileID}); err != nil {
			slog.Error("persistTgFileID: stash update failed", "id", item.ID, "error", err)
		} else {
			slog.Info("persistTgFileID: saved to stash", "id", item.ID, "tg_id", tgFileID)
		}
	}()
}

// sendByTgFileID sends a file by its Telegram file_id (no re-upload).
func sendByTgFileID(b *Bot, chatID int64, fileID string, mediaType stash.MediaType, caption string) {
	ctx := context.Background()
	file := tu.FileFromID(fileID)
	switch mediaType {
	case stash.MediaTypeImage:
		params := tu.Photo(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		if _, err := b.api.SendPhoto(ctx, params); err != nil {
			slog.Error("sendByTgFileID: failed", "tg_id", fileID, "error", err)
		}
	case stash.MediaTypeVideo:
		params := tu.Video(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		if _, err := b.api.SendVideo(ctx, params); err != nil {
			slog.Error("sendByTgFileID: failed", "tg_id", fileID, "error", err)
		}
	case stash.MediaTypeGIF:
		if _, err := b.api.SendAnimation(ctx, tu.Animation(tu.ID(chatID), file)); err != nil {
			slog.Error("sendByTgFileID: failed", "tg_id", fileID, "error", err)
		}
	default:
		params := tu.Document(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		if _, err := b.api.SendDocument(ctx, params); err != nil {
			slog.Error("sendByTgFileID: failed", "tg_id", fileID, "error", err)
		}
	}
}

// prefetchItems downloads stash bytes for items that don't yet have a Telegram file_id,
// storing them in b.fileCache so the next page send can skip the stash download.
// Runs in a background goroutine with concurrent downloads.
func prefetchItems(b *Bot, items []*stash.Item) {
	var wg sync.WaitGroup
	for _, it := range items {
		it := it
		if it.TelegramFileID != nil && *it.TelegramFileID != "" {
			continue // already cached in Telegram — no download needed
		}
		if _, ok := b.fileCache.Load(it.ID); ok {
			continue // already prefetched
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.Info("prefetch: downloading", "id", it.ID)
			rc, _, err := b.stash.GetFile(context.Background(), it.ID)
			if err != nil {
				slog.Error("prefetch: get file", "id", it.ID, "error", err)
				return
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				slog.Error("prefetch: read", "id", it.ID, "error", err)
				return
			}
			b.fileCache.Store(it.ID, data)
			slog.Info("prefetch: cached", "id", it.ID, "bytes", len(data))
		}()
	}
	wg.Wait()
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
