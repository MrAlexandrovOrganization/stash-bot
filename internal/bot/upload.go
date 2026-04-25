package bot

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"stash-bot/internal/stash"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Upload ────────────────────────────────────────────────────────────────────

func (b *Bot) handleUpload(msg *tgbotapi.Message, mediaType stash.MediaType) {
	ctx := context.Background()

	fileID, fileName, contentType := extractFileInfo(msg, mediaType)
	if fileID == "" {
		send(b, msg.Chat.ID, "Не удалось получить файл.")
		return
	}
	slog.Info("upload: downloading from Telegram", "file_name", fileName, "content_type", contentType, "media_type", mediaType)

	description, tags := parseCaption(msg.Caption)

	fileURL, err := b.api.GetFileDirectURL(fileID)
	if err != nil {
		slog.Error("upload: get file url", "error", err)
		send(b, msg.Chat.ID, "Не удалось скачать файл из Telegram.")
		return
	}

	resp, err := http.Get(fileURL) //nolint:noctx
	if err != nil {
		slog.Error("upload: http get failed", "error", err)
		send(b, msg.Chat.ID, "Не удалось скачать файл.")
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
		send(b, msg.Chat.ID, "Ошибка при сохранении файла.")
		return
	}
	slog.Info("upload: done", "id", item.ID, "file_name", item.FileName)

	// Invalidate cached items so next storage visit reloads.
	sess := b.session(msg.From.ID, msg.Chat.ID)
	sess.CurrentItem = item
	sess.Screen = ScreenItem
	sess.Back = ScreenStorage
	sess.Items = nil
	showItem(b, msg.Chat.ID, sess)
}

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
