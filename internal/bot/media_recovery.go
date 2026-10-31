package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
)

var errLoadStashOriginal = errors.New("load original from Stash")

// Retry only an explicit rejection of a cached identifier. Timeouts/5xx may
// follow a successful send, so retrying those could duplicate user messages.
func invalidTelegramFileID(err error) bool {
	var apiErr *telegoapi.Error
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != 400 {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return (strings.Contains(description, "file identifier") || strings.Contains(description, "file_id")) &&
		(strings.Contains(description, "invalid") || strings.Contains(description, "wrong"))
}

// sendItemMessage serves gallery singletons, animations/documents and the File
// button. A stale Telegram cache never replaces the original stored in Stash.
func sendItemMessage(b *Bot, chatID int64, item *stash.Item, caption string, forceUpload bool) (*telego.Message, error) {
	ctx, cancel := b.mediaDeadline()
	defer cancel()
	release, err := acquireMedia(ctx, b.mediaStore().sends)
	if err != nil {
		return nil, err
	}
	defer release()
	return sendItemMessageInContext(ctx, b, chatID, item, caption, forceUpload)
}

func sendItemMessageInContext(ctx context.Context, b *Bot, chatID int64, item *stash.Item, caption string, forceUpload bool) (*telego.Message, error) {
	fid, _ := b.lookupFileID(item.ID)
	if fid == "" && item.TelegramFileID != nil {
		fid = *item.TelegramFileID
	}
	if !forceUpload && fid != "" {
		sent, err := sendByTgFileID(ctx, b, chatID, fid, item.Type, caption)
		if err == nil {
			return sent, nil
		}
		if !invalidTelegramFileID(err) {
			return nil, err
		}
		slog.Warn("stale Telegram file_id, uploading original from Stash", "id", item.ID)
	}

	data, err := loadItemFile(ctx, b, item)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errLoadStashOriginal, err)
	}
	defer data.Close()
	sent, tgID, err := sendFileByType(ctx, b, chatID, item.Type, item.FileName, data, caption)
	if err != nil {
		return nil, err
	}
	if tgID != "" {
		persistTgFileID(b, item, tgID)
	}
	return sent, nil
}
