// Command repair-media fixes items whose media bytes were never stored
// correctly (old uploads saved the Telegram API error body instead of the
// file itself). For every such item it re-downloads the original from
// Telegram via the cached telegram_file_id and re-uploads it to stash.
//
// The backend deduplicates by file_unique_id, so the re-upload overwrites the
// existing record in place (same id, description and tags preserved) and
// re-triggers AI description / transcription processing.
//
// Run inside the bot container:
//
//	DRY_RUN=1 docker compose run --rm --entrypoint /repair-media stash-bot   # preview only
//	docker compose run --rm --entrypoint /repair-media stash-bot             # repair
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"stash-bot/internal/logx"
	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
)

func main() {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		slog.Error("BOT_TOKEN is required")
		os.Exit(1)
	}
	logx.Setup("stash-bot-repair", token)
	slog.Info("repair-media starting")
	stashURL := os.Getenv("STASH_URL")
	if stashURL == "" {
		stashURL = "http://stash:8080"
	}
	localAPI := os.Getenv("TELEGRAM_LOCAL_API_URL")
	dryRun := os.Getenv("DRY_RUN") == "1"
	if dryRun {
		slog.Info("DRY RUN: no changes will be made")
	}

	opts := []telego.BotOption{}
	if localAPI != "" {
		opts = append(opts, telego.WithAPIServer(localAPI))
	}
	api, err := telego.NewBot(token, opts...)
	if err != nil {
		slog.Error("telegram bot init", "error", err)
		os.Exit(1)
	}
	client := stash.NewClient(stashURL)
	ctx := context.Background()

	items, err := client.Search(ctx, stash.SearchQuery{})
	if err != nil {
		slog.Error("list items", "error", err)
		os.Exit(1)
	}
	slog.Info("loaded items", "count", len(items))

	var healthy, corrupt, repaired, failed int
	for _, it := range items {
		if it.TelegramFileID == nil || *it.TelegramFileID == "" {
			continue // cannot restore without a Telegram source
		}

		stored, err := downloadStash(client, ctx, it.ID)
		if err != nil {
			slog.Error("stash download", "id", it.ID, "error", err)
			failed++
			continue
		}
		if !isCorrupt(stored) {
			healthy++
			continue
		}
		corrupt++
		slog.Warn("corrupt item found", "id", it.ID, "name", it.FileName,
			"stored_bytes", len(stored), "db_size", it.Size)
		if dryRun {
			continue
		}

		fuid := it.FileUniqueID
		if fuid == nil || *fuid == "" {
			// Old items uploaded before file_unique_id was captured: fetch it
			// from Telegram by file_id and persist it first, so the re-upload
			// below hits the dedup path and overwrites the record in place.
			tgFile, err := api.GetFile(ctx, &telego.GetFileParams{FileID: *it.TelegramFileID})
			if err != nil {
				slog.Error("get telegram file meta", "id", it.ID, "error", err)
				failed++
				continue
			}
			if tgFile.FileUniqueID == "" {
				slog.Error("telegram returned empty file_unique_id, skipping", "id", it.ID)
				failed++
				continue
			}
			if _, err := client.Update(ctx, it.ID, stash.UpdateMeta{FileUniqueID: &tgFile.FileUniqueID}); err != nil {
				slog.Error("persist file_unique_id", "id", it.ID, "error", err)
				failed++
				continue
			}
			slog.Info("persisted missing file_unique_id", "id", it.ID)
			fuid = &tgFile.FileUniqueID
		}

		fixed, err := downloadTelegram(api, localAPI, ctx, *it.TelegramFileID)
		if err != nil {
			slog.Error("telegram download", "id", it.ID, "error", err)
			failed++
			continue
		}
		if isCorrupt(fixed) {
			slog.Error("telegram returned corrupt-looking data, skipping", "id", it.ID, "bytes", len(fixed))
			failed++
			continue
		}

		updated, err := client.Upload(ctx, bytes.NewReader(fixed), it.FileName, it.ContentType, int64(len(fixed)), stash.UploadMeta{
			Description:     it.Description,
			Tags:            it.Tags,
			Source:          it.Source,
			OriginalCaption: it.OriginalCaption,
			FileUniqueID:    *fuid,
		})
		if err != nil {
			slog.Error("re-upload", "id", it.ID, "error", err)
			failed++
			continue
		}
		repaired++
		slog.Info("repaired", "id", it.ID, "name", it.FileName,
			"bytes", len(fixed), "kept_id", updated.ID == it.ID)
	}

	slog.Info("done", "healthy", healthy, "corrupt", corrupt,
		"repaired", repaired, "failed", failed, "dry_run", dryRun)
}

// isCorrupt reports whether the payload looks like a JSON error body rather
// than actual media (JPEG/PNG/MP4 files never start with '{').
func isCorrupt(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return true // empty
}

func downloadStash(client *stash.Client, ctx context.Context, id string) ([]byte, error) {
	rc, _, err := client.GetFile(ctx, id)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readLimited(rc)
}

func downloadTelegram(api *telego.Bot, localAPI string, ctx context.Context, fileID string) ([]byte, error) {
	f, err := api.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}

	if localAPI != "" {
		// Local Bot API server: GetFile returns an absolute path on the
		// shared telegram-bot-api-data volume (mounted read-only).
		if data, err := readFileLimited(f.FilePath); err == nil {
			return data, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api.FileDownloadURL(f.FilePath), nil)
	if err != nil {
		return nil, fmt.Errorf("telegram download request is invalid")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram download request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram download status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxRepairFileBytes {
		return nil, fmt.Errorf("telegram file exceeds %d bytes", maxRepairFileBytes)
	}
	return readLimited(resp.Body)
}

const maxRepairFileBytes = 512 << 20

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxRepairFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxRepairFileBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxRepairFileBytes)
	}
	return data, nil
}

func readFileLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file)
}
