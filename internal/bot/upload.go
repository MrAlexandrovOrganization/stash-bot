package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

	file, err := b.api.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		slog.Error("upload: get file", "error", err)
		send(b, msg.Chat.ID, "Не удалось получить файл из Telegram.")
		return
	}

	var data []byte
	var size int64

	if b.telegramAPIURL != "" {
		// The local Bot API server stores files on a shared volume
		// (telegram-bot-api-data, mounted read-only at
		// /var/lib/telegram-bot-api). GetFile returns the absolute path on
		// that volume, so read it directly from disk — no URL guessing, no
		// 404s. Fall back to HTTP if the mount is unavailable.
		if d, rerr := readFileLimited(file.FilePath); rerr == nil {
			data = d
			size = int64(len(d))
		} else {
			slog.Warn("upload: direct file read failed, falling back to HTTP",
				"error", rerr)
		}
	}

	if data == nil {
		filePath := file.FilePath
		if b.telegramAPIURL != "" {
			// Local Bot API server: the download URL path must be relative
			// to <download_dir>/<token>, i.e. the <rest> part of the
			// absolute path returned by GetFile.
			if i := strings.Index(filePath, b.token); i >= 0 {
				filePath = strings.TrimPrefix(filePath[i+len(b.token):], "/")
			}
		}
		fileURL := b.api.FileDownloadURL(filePath)
		resp, err := downloadTelegramHTTP(ctx, fileURL)
		if err != nil {
			slog.Error("upload: http get failed", "error", err)
			send(b, msg.Chat.ID, "Не удалось скачать файл.")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			// Telegram (or the local Bot API server) returned an error body;
			// without this check that body would be stored as the "image".
			slog.Error("upload: telegram download status", "status", resp.StatusCode, "file_url", fileURL)
			send(b, msg.Chat.ID, "Не удалось скачать файл из Telegram.")
			return
		}
		d, rerr := readLimited(resp.Body)
		if rerr != nil {
			slog.Error("upload: reading body", "error", rerr)
			send(b, msg.Chat.ID, "Не удалось скачать файл.")
			return
		}
		data = d
		size = int64(len(d))
	}

	slog.Info("upload: uploading to stash", "file_name", fileName, "size", size)

	item, err := b.stash.Upload(ctx, bytes.NewReader(data), fileName, contentType, size, stash.UploadMeta{
		Description:     description,
		Tags:            tags,
		Source:          forwardSource(msg),
		OriginalCaption: msg.Caption,
		FileUniqueID:    fileUniqueID,
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

	data, err := b.downloadFile(ctx, fileID)
	if err != nil {
		slog.Error("similar by image: download failed", "error", err)
		send(b, msg.Chat.ID, "Не удалось скачать картинку.")
		return
	}
	slog.Info("similar by image: querying stash", "size", len(data))

	runSimilar(ctx, b, msg.Chat.ID, sess, stash.SimilarRequest{ImageBytes: data, Limit: similarLimit})
}

// downloadFile fetches a Telegram file by id into memory, preferring the local
// Bot API shared volume when configured, and falling back to HTTP download.
func (b *Bot) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	file, err := b.api.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}

	if b.telegramAPIURL != "" {
		if d, rerr := readFileLimited(file.FilePath); rerr == nil {
			return d, nil
		} else {
			slog.Warn("downloadFile: direct read failed, falling back to HTTP",
				"error", rerr)
		}
	}

	filePath := file.FilePath
	if b.telegramAPIURL != "" {
		if i := strings.Index(filePath, b.token); i >= 0 {
			filePath = strings.TrimPrefix(filePath[i+len(b.token):], "/")
		}
	}
	fileURL := b.api.FileDownloadURL(filePath)
	resp, err := downloadTelegramHTTP(ctx, fileURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram download status %d", resp.StatusCode)
	}
	return readLimited(resp.Body)
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
		return nil, fmt.Errorf("telegram download request failed")
	}
	if resp.ContentLength > maxTelegramFileBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("telegram file exceeds %d bytes", maxTelegramFileBytes)
	}
	return resp, nil
}

func readFileLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file)
}

func readLimited(r io.Reader) ([]byte, error) {
	return readLimitedBytes(r, maxTelegramFileBytes)
}

func readLimitedBytes(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
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
