package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
)

const (
	telegramGetFileTimeout  = 5 * time.Minute
	telegramDownloadTimeout = 5 * time.Minute
	maxTelegramFileBytes    = 512 << 20
)

var telegramHTTPClient = &http.Client{Timeout: telegramDownloadTimeout}

// ── Upload ────────────────────────────────────────────────────────────────────

func (b *Bot) handleUpload(ctx context.Context, msg *telego.Message, mediaType stash.MediaType) {

	fileID, fileName, contentType, fileUniqueID := extractFileInfo(msg, mediaType)
	if fileID == "" {
		send(b, msg.Chat.ID, "Не удалось получить файл.")
		return
	}
	slog.Info("upload: downloading from Telegram", "file_name", fileName, "content_type", contentType, "media_type", mediaType)

	description, tags := parseCaption(msg.Caption)

	if messageFileSize(msg, mediaType) > maxTelegramFileBytes {
		send(b, msg.Chat.ID, "Не удалось скачать файл.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*telegramDownloadTimeout)
	defer cancel()
	release, err := acquireMedia(ctx, b.mediaStore().downloads)
	if err != nil {
		return
	}
	defer release()
	data, err := b.downloadTelegramFile(ctx, fileID, messageFileSize(msg, mediaType), maxTelegramFileBytes)
	if err != nil {
		slog.Error("upload: telegram download failed", "error", err)
		send(b, msg.Chat.ID, "Не удалось скачать файл.")
		return
	}
	defer data.Close()
	item, err := b.stash.Upload(ctx, data, fileName, contentType, data.Size(), stash.UploadMeta{
		Description: description, Tags: tags, Source: forwardSource(msg),
		OriginalCaption: msg.Caption, FileUniqueID: fileUniqueID,
	})
	release()
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

// handleSimilarByImage downloads the photo from the sent message and finds
// visually similar media in stash without saving the photo itself.
func (b *Bot) handleSimilarByImage(ctx context.Context, msg *telego.Message) {
	sess := b.session(msg.From.ID, msg.Chat.ID)

	fileID, _, _, _ := extractFileInfo(msg, stash.MediaTypeImage)
	if fileID == "" {
		send(b, msg.Chat.ID, "Не удалось получить картинку.")
		return
	}
	slog.Info("similar by image: downloading", "file_id", fileID)

	ctx, cancel := context.WithTimeout(ctx, 2*telegramDownloadTimeout)
	defer cancel()
	release, err := acquireMedia(ctx, b.mediaStore().downloads)
	if err != nil {
		return
	}
	defer release()
	data, err := b.downloadTelegramFile(ctx, fileID, messageFileSize(msg, stash.MediaTypeImage), maxSimilarImageBytes)
	if err != nil {
		slog.Error("similar by image: download failed", "error", err)
		send(b, msg.Chat.ID, "Не удалось скачать картинку.")
		return
	}
	defer data.Close()
	req := stash.SimilarRequest{ImageReader: data, Limit: similarLimit}
	items, err := b.stash.Similar(ctx, req)
	data.Close()
	release()
	showSimilarResults(ctx, b, msg.Chat.ID, sess, req, items, err)
}

const maxSimilarImageBytes = 16 << 20

func (b *Bot) downloadTelegramFile(ctx context.Context, fileID string, knownSize, limit int64) (*mediaLease, error) {
	if knownSize > limit {
		return nil, errMediaTooLarge
	}
	file, err := b.getFile(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if file.FileSize > limit {
		return nil, errMediaTooLarge
	}
	if b.telegramAPIURL != "" {
		src, err := openTelegramFile(file.FilePath)
		if err == nil {
			defer src.Close()
			info, err := src.Stat()
			if err != nil {
				return nil, fmt.Errorf("stat Telegram file failed")
			}
			return b.mediaStore().spool(ctx, src, info.Size(), limit)
		}
		slog.Warn("upload: direct read unavailable, falling back to HTTP", "error", err)
	}
	filePath := file.FilePath
	if b.telegramAPIURL != "" {
		if i := strings.Index(filePath, b.token); i >= 0 {
			filePath = strings.TrimPrefix(filePath[i+len(b.token):], "/")
		}
	}
	resp, err := downloadTelegramHTTP(ctx, b.api.FileDownloadURL(filePath))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Error("upload: telegram download status", "status", resp.StatusCode)
		return nil, fmt.Errorf("telegram download status %d", resp.StatusCode)
	}
	return b.mediaStore().spool(ctx, resp.Body, resp.ContentLength, limit)
}

// messageFileSize rejects a known oversize file before getFile can make the
// local Telegram API download it. Unknown/misreported sizes are bounded on copy.
func messageFileSize(msg *telego.Message, kind stash.MediaType) int64 {
	switch kind {
	case stash.MediaTypeImage:
		if len(msg.Photo) > 0 {
			return int64(msg.Photo[len(msg.Photo)-1].FileSize)
		}
	case stash.MediaTypeVideo:
		if msg.Video != nil {
			return msg.Video.FileSize
		}
	case stash.MediaTypeGIF:
		if msg.Animation != nil {
			return msg.Animation.FileSize
		}
	case stash.MediaTypeDocument:
		if msg.Document != nil {
			return msg.Document.FileSize
		}
	}
	return 0
}

func (b *Bot) getFile(ctx context.Context, fileID string) (*telego.File, error) {
	// A local Bot API server may download the file during GetFile itself.
	ctx, cancel := context.WithTimeout(ctx, telegramGetFileTimeout)
	defer cancel()
	file, err := b.fileAPI.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("telegram get file: %w", telegramRequestCause(err))
	}
	return file, nil
}

// net/http includes the token-bearing request URL in url.Error. Keep the
// underlying transport cause so cancellation and timeout remain inspectable.
func telegramRequestCause(err error) error {
	var urlErr *url.Error
	for errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return err
}

func downloadTelegramHTTP(ctx context.Context, rawURL string) (*http.Response, error) {
	_, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("telegram download URL is invalid")
	}
	// The URL contains a bearer token. Keep it out of errors and logs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram download request is invalid")
	}
	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram download request failed: %w", telegramRequestCause(err))
	}
	if resp.ContentLength > maxTelegramFileBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("telegram file exceeds %d bytes", maxTelegramFileBytes)
	}
	return resp, nil
}

func openTelegramFile(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil, fmt.Errorf("open Telegram file: %w", pathErr.Err)
		}
		return nil, fmt.Errorf("open Telegram file failed")
	}
	return file, nil
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

func extractFileInfo(msg *telego.Message, mediaType stash.MediaType) (fileID, fileName, contentType, fileUniqueID string) {
	switch mediaType {
	case stash.MediaTypeImage:
		if len(msg.Photo) == 0 {
			return "", "", "", ""
		}
		p := msg.Photo[len(msg.Photo)-1]
		return p.FileID, fmt.Sprintf("photo_%s.jpg", p.FileUniqueID), "image/jpeg", p.FileUniqueID
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
		return v.FileID, name, ct, v.FileUniqueID
	case stash.MediaTypeGIF:
		a := msg.Animation
		name := a.FileName
		if name == "" {
			name = fmt.Sprintf("animation_%s.mp4", a.FileUniqueID)
		}
		return a.FileID, name, "video/mp4", a.FileUniqueID
	case stash.MediaTypeDocument:
		d := msg.Document
		ct := d.MimeType
		if ct == "" {
			ct = "application/octet-stream"
		}
		return d.FileID, d.FileName, ct, d.FileUniqueID
	}
	return "", "", "", ""
}
