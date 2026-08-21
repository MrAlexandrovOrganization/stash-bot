package bot

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
)

// ── Upload ────────────────────────────────────────────────────────────────────

func (b *Bot) handleUpload(msg *telego.Message, mediaType stash.MediaType) {
	ctx := context.Background()

	fileID, fileName, contentType := extractFileInfo(msg, mediaType)
	if fileID == "" {
		send(b, msg.Chat.ID, "Не удалось получить файл.")
		return
	}
	slog.Info("upload: downloading from Telegram", "file_name", fileName, "content_type", contentType, "media_type", mediaType)

	description, tags := parseCaption(msg.Caption)

	file, err := b.api.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		slog.Error("upload: get file", "error", err)
		send(b, msg.Chat.ID, "Не удалось получить файл из Telegram.")
		return
	}

	fileURL := b.api.FileDownloadURL(file.FilePath)
	resp, err := http.Get(fileURL) //nolint:noctx
	if err != nil {
		slog.Error("upload: http get failed", "error", err)
		send(b, msg.Chat.ID, "Не удалось скачать файл.")
		return
	}
	defer resp.Body.Close()
	slog.Info("upload: uploading to stash", "file_name", fileName, "size", resp.ContentLength)

	item, err := b.stash.Upload(ctx, resp.Body, fileName, contentType, resp.ContentLength, stash.UploadMeta{
		Description:     description,
		Tags:            tags,
		Source:          forwardSource(msg),
		OriginalCaption: msg.Caption,
	})
	if err != nil {
		slog.Error("upload: stash upload failed", "error", err)
		send(b, msg.Chat.ID, "Ошибка при сохранении файла.")
		return
	}
	slog.Info("upload: done", "id", item.ID, "file_name", item.FileName)

	// Cache the original Telegram file_id so storage view doesn't re-download.
	persistTgFileID(b, item, fileID)

	// Invalidate cached items so next storage visit reloads.
	sess := b.session(msg.From.ID, msg.Chat.ID)
	sess.CurrentItem = item
	sess.Screen = ScreenItem
	sess.Back = ScreenStorage
	sess.Items = nil
	showItem(b, msg.Chat.ID, sess)
}

// forwardSource returns a human-readable name of the original sender for
// forwarded messages. Empty for messages sent directly to the bot.
func forwardSource(msg *telego.Message) string {
	switch o := msg.ForwardOrigin.(type) {
	case *telego.MessageOriginUser:
		name := strings.TrimSpace(o.SenderUser.FirstName + " " + o.SenderUser.LastName)
		if o.SenderUser.Username != "" {
			name += fmt.Sprintf(" (@%s)", o.SenderUser.Username)
		}
		return name
	case *telego.MessageOriginHiddenUser:
		return o.SenderUserName
	case *telego.MessageOriginChat:
		if o.AuthorSignature != "" {
			return strings.TrimSpace(o.SenderChat.Title + " · " + o.AuthorSignature)
		}
		return o.SenderChat.Title
	case *telego.MessageOriginChannel:
		if o.AuthorSignature != "" {
			return strings.TrimSpace(o.Chat.Title + " · " + o.AuthorSignature)
		}
		return o.Chat.Title
	}
	return ""
}

func extractFileInfo(msg *telego.Message, mediaType stash.MediaType) (fileID, fileName, contentType string) {
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
