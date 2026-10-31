package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
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
	ctx, cancel := b.mediaDeadline()
	defer cancel()
	release, err := acquireMedia(ctx, b.mediaStore().sends)
	if err != nil {
		return nil
	}
	defer release()
	return sendMediaGroupItemsAttempt(ctx, b, chatID, items, false)
}

func sendMediaGroupItemsAttempt(ctx context.Context, b *Bot, chatID int64, items []*stash.Item, forceUpload bool) []int {
	// Telegram albums require at least two entries. Search results and the
	// last storage page can contain only one photo/video.
	if len(items) == 1 {
		sent, err := sendItemMessageInContext(ctx, b, chatID, items[0], "", forceUpload)
		if err != nil {
			slog.Error("sendMediaGroupItems: single item", "id", items[0].ID, "error", err)
			return nil
		}
		return []int{sent.MessageID}
	}
	type prepared struct {
		input telego.InputMedia
		isNew bool
		lease *mediaLease
		err   error
	}
	preparedList := make([]prepared, len(items))

	var wg sync.WaitGroup
	for i, it := range items {
		i, it := i, it
		wg.Add(1)
		go func() {
			defer wg.Done()
			inp, isNew, lease, err := prepareMediaInput(ctx, b, it, forceUpload)
			if err != nil {
				slog.Error("sendMediaGroupItems: prepare input", "id", it.ID, "error", err)
			}
			preparedList[i] = prepared{input: inp, isNew: isNew, lease: lease, err: err}
		}()
	}
	wg.Wait()
	closeFiles := func() {
		for _, p := range preparedList {
			if p.lease != nil {
				p.lease.Close()
			}
		}
	}
	defer closeFiles()
	for _, p := range preparedList {
		if p.err != nil {
			return nil
		}
	}

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
	if len(media) == 1 {
		closeFiles()
		return sendMediaGroupItemsAttempt(ctx, b, chatID, []*stash.Item{slots[0].item}, forceUpload)
	}

	slog.Info("sendMediaGroupItems: sending group", "input_count", len(media))
	sentMsgs, err := b.api.SendMediaGroup(ctx, tu.MediaGroup(tu.ID(chatID), media...))
	if err != nil && !forceUpload && invalidTelegramFileID(err) && slices.ContainsFunc(slots, func(s mediaSlot) bool { return !s.isNew }) {
		// A rejected cached ID rejects the whole album. Rebuild every input:
		// multipart readers from the first attempt may already be consumed.
		slog.Warn("sendMediaGroupItems: stale Telegram cache, retrying from Stash")
		closeFiles()
		return sendMediaGroupItemsAttempt(ctx, b, chatID, items, true)
	}
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
	sent, err := sendItemMessage(b, chatID, it, "", false)
	if err != nil {
		slog.Error("sendSingleItem: send", "id", it.ID, "error", err)
		return 0
	}
	return sent.MessageID
}

// ── File sending ──────────────────────────────────────────────────────────────

// sendFile sends a stash item to Telegram.
// If item.TelegramFileID is already set, the file is sent directly by Telegram file_id.
// On first upload the received file_id is saved back to stash asynchronously.
func sendFile(b *Bot, chatID int64, item *stash.Item) {
	caption := sanitizeUTF8(item.Description)
	_, err := sendItemMessage(b, chatID, item, caption, false)
	if err != nil {
		slog.Error("sendFile: send failed", "id", item.ID, "error", err)
		if errors.Is(err, errLoadStashOriginal) {
			send(b, chatID, "Не удалось получить файл.")
		}
		return
	}
	slog.Info("sendFile: done", "id", item.ID)
}

// ── Media group helpers ───────────────────────────────────────────────────────

// prepareMediaInput builds a Telegram media group input for one item.
// isNew means file_id must be cached after send. The caller holds the returned
// lease through sending and closes it before a retry or return.
// No caption is set here — the page text is applied to the last message after the group is sent.
func prepareMediaInput(ctx context.Context, b *Bot, it *stash.Item, forceUpload bool) (telego.InputMedia, bool, *mediaLease, error) {
	if fid, ok := b.lookupFileID(it.ID); ok && !forceUpload {
		return buildGroupInput(it.Type, tu.FileFromID(fid)), false, nil, nil
	}
	if !forceUpload && it.TelegramFileID != nil && *it.TelegramFileID != "" {
		return buildGroupInput(it.Type, tu.FileFromID(*it.TelegramFileID)), false, nil, nil
	}
	data, err := loadItemFile(ctx, b, it)
	if err != nil {
		return nil, false, nil, err
	}
	return buildGroupInput(it.Type, tu.FileFromReader(data, it.ID+"-"+it.FileName)), true, data, nil
}

// cacheMediaGroupFileIDs associates album replies with their input slots. Reserve
// cached slots too: matching only new slots by type can assign another photo's ID.
// Message IDs restore album order if completion callbacks arrive out of order.
func cacheMediaGroupFileIDs(b *Bot, slots []mediaSlot, sentMsgs []telego.Message) {
	if len(slots) != len(sentMsgs) {
		return // An incomplete response cannot be mapped safely.
	}
	ordered := slices.Clone(sentMsgs)
	slices.SortFunc(ordered, func(a, b telego.Message) int {
		if a.MessageID < b.MessageID {
			return -1
		}
		if a.MessageID > b.MessageID {
			return 1
		}
		return 0
	})
	for i, slot := range slots {
		if !slot.isNew {
			continue
		}
		tgID := exactTypeFileID(slot.item.Type, ordered[i])
		if tgID == "" {
			slog.Warn("cacheMediaGroupFileIDs: unexpected reply type", "id", slot.item.ID)
			continue
		}
		persistTgFileID(b, slot.item, tgID)
	}
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

// ── Low-level builders ────────────────────────────────────────────────────────

func mediaCacheKey(it *stash.Item) string { return it.ID + ":" + it.StoragePath }

func loadItemFile(ctx context.Context, b *Bot, it *stash.Item) (*mediaLease, error) {
	store := b.mediaStore()
	if it.Size > maxTelegramFileBytes {
		return nil, errMediaTooLarge
	}
	if hit := store.get(mediaCacheKey(it)); hit != nil {
		return hit, nil
	}
	release, err := acquireMedia(ctx, store.downloads)
	if err != nil {
		return nil, err
	}
	defer release()
	// Another foreground/prefetch operation may have completed while we waited.
	if hit := store.get(mediaCacheKey(it)); hit != nil {
		return hit, nil
	}
	rc, meta, err := b.stash.GetFile(ctx, it.ID)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return store.spool(ctx, rc, meta.Size, maxTelegramFileBytes)
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

// sendFileByType sends any media type and returns (message, file_id, error).
func sendFileByType(ctx context.Context, b *Bot, chatID int64, mediaType stash.MediaType, fileName string, data io.Reader, caption string) (*telego.Message, string, error) {
	file := tu.FileFromReader(data, fileName)
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
			slog.Info("persistTgFileID: saved to stash", "id", item.ID)
		}
	}()
}

// sendByTgFileID sends a file by its Telegram file_id (no re-upload).
func sendByTgFileID(ctx context.Context, b *Bot, chatID int64, fileID string, mediaType stash.MediaType, caption string) (*telego.Message, error) {
	file := tu.FileFromID(fileID)
	switch mediaType {
	case stash.MediaTypeImage:
		params := tu.Photo(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		return b.api.SendPhoto(ctx, params)
	case stash.MediaTypeVideo:
		params := tu.Video(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		return b.api.SendVideo(ctx, params)
	case stash.MediaTypeGIF:
		return b.api.SendAnimation(ctx, tu.Animation(tu.ID(chatID), file))
	default:
		params := tu.Document(tu.ID(chatID), file)
		if caption != "" {
			params = params.WithCaption(caption)
		}
		return b.api.SendDocument(ctx, params)
	}
}

// Prefetch is opportunistic: never queue behind a foreground transfer.
// Only cache-sized files are copied, with the same disk/concurrency budgets.
func prefetchItems(b *Bot, items []*stash.Item) {
	for _, it := range items {
		if it.TelegramFileID != nil && *it.TelegramFileID != "" {
			continue
		}
		store := b.mediaStore()
		if it.Size > store.cacheMax {
			continue
		}
		if hit := store.get(mediaCacheKey(it)); hit != nil {
			hit.Close()
			continue
		}
		select {
		case store.downloads <- struct{}{}:
		default:
			return
		}
		func() {
			defer func() { <-store.downloads }()
			ctx, cancel := b.mediaDeadline()
			defer cancel()
			rc, meta, err := b.stash.GetFile(ctx, it.ID)
			if err != nil {
				return
			}
			defer rc.Close()
			data, err := store.spool(ctx, rc, meta.Size, store.cacheMax)
			if err != nil {
				return
			}
			defer data.Close()
			store.put(mediaCacheKey(it), data)
		}()
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
