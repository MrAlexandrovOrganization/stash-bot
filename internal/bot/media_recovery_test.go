package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
)

type cacheUpdate struct{ id, fileID string }

// The fake backend exposes immutable originals and accepts only Telegram cache
// updates. Any other write fails, including an accidental upload or deletion.
func recoveryBackend(t *testing.T, items []*stash.Item) (*stash.Client, <-chan cacheUpdate, *atomic.Int32) {
	t.Helper()
	updates := make(chan cacheUpdate, 20)
	downloads := &atomic.Int32{}
	originals := make(map[string]stash.Item)
	for _, it := range items {
		originals[it.ID] = *it
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/items/")
		if r.Method == http.MethodGet && strings.HasSuffix(id, "/file") {
			id = strings.TrimSuffix(id, "/file")
			if _, ok := originals[id]; !ok {
				http.NotFound(w, r)
				return
			}
			downloads.Add(1)
			_, _ = io.WriteString(w, "original-"+id)
			return
		}
		item, ok := originals[id]
		if !ok {
			t.Error("unexpected Stash request")
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPatch {
			var fields map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || len(fields) != 1 || fields["telegram_file_id"] == nil {
				t.Error("recovery attempted to change original metadata")
				http.Error(w, "unexpected update", 400)
				return
			}
			var fid string
			_ = json.Unmarshal(fields["telegram_file_id"], &fid)
			item.TelegramFileID = &fid
			updates <- cacheUpdate{id, fid}
		} else if r.Method != http.MethodGet {
			t.Error("recovery attempted to write/delete an original")
			http.Error(w, "unexpected mutation", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(item)
	}))
	t.Cleanup(s.Close)
	return stash.NewClient(s.URL), updates, downloads
}

func awaitCacheUpdates(t *testing.T, updates <-chan cacheUpdate, want map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for range len(want) {
		select {
		case u := <-updates:
			got[u.id] = u.fileID
		case <-time.After(3 * time.Second):
			t.Fatal("recovered Telegram cache was not persisted")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cache updates = %v, want %v", got, want)
	}
}

func TestStoragePageRecoversRejectedAlbum(t *testing.T) {
	old := "obsolete-id"
	items := []*stash.Item{
		{ID: "a", Type: stash.MediaTypeImage, FileName: "same.jpg", TelegramFileID: &old},
		{ID: "b", Type: stash.MediaTypeVideo, FileName: "movie.mp4", TelegramFileID: &old},
		{ID: "c", Type: stash.MediaTypeImage, FileName: "same.jpg", TelegramFileID: &old},
	}
	backend, updates, downloads := recoveryBackend(t, items)
	var calls atomic.Int32
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMediaGroup") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":500}}`)
			return
		}
		n := calls.Add(1)
		if n == 1 {
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: invalid remote file identifier"}`)
			return
		}
		if n == 2 {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			var media []struct{ Media string }
			_ = json.Unmarshal([]byte(r.FormValue("media")), &media)
			if len(media) != len(items) {
				t.Errorf("retry album contains %d files", len(media))
			}
			for i, m := range media {
				f, _, err := r.FormFile(strings.TrimPrefix(m.Media, "attach://"))
				if err != nil {
					t.Error("retry did not upload an original file", err)
					continue
				}
				data, _ := io.ReadAll(f)
				_ = f.Close()
				if string(data) != "original-"+items[i].ID {
					t.Errorf("wrong original uploaded for album slot %d", i)
				}
			}
		} else {
			var body struct{ Media []struct{ Media string } }
			_ = json.NewDecoder(r.Body).Decode(&body)
			for i, m := range body.Media {
				if m.Media != "fresh-"+items[i].ID {
					t.Errorf("next gallery open used a wrong or obsolete identifier at %d", i)
				}
			}
		}
		// Completion order differs from album/message order.
		_, _ = io.WriteString(w, `{"ok":true,"result":[{"message_id":103,"photo":[{"file_id":"fresh-c"}]},{"message_id":101,"photo":[{"file_id":"fresh-a"}]},{"message_id":102,"video":{"file_id":"fresh-b"}}]}`)
	}))
	defer tg.Close()
	b, err := New(testTelegramToken, 1, backend, tg.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		b.cacheFileID(it.ID, old)
	}
	sess := &Session{Items: items}
	sendStoragePage(b, 1, sess, true)
	if len(sess.MediaMsgIDs) != 3 || sess.LastMsgID != 500 {
		t.Fatalf("storage did not render media and controls: %+v", sess)
	}
	awaitCacheUpdates(t, updates, map[string]string{"a": "fresh-a", "b": "fresh-b", "c": "fresh-c"})
	sendStoragePage(b, 1, sess, true)
	if downloads.Load() != 3 || calls.Load() != 3 {
		t.Fatalf("downloads=%d, Telegram calls=%d; expected one recovery and then cache reuse", downloads.Load(), calls.Load())
	}
}

func TestSingleMediaRecoversRejectedFileID(t *testing.T) {
	for _, mediaType := range []stash.MediaType{stash.MediaTypeImage, stash.MediaTypeVideo, stash.MediaTypeGIF, stash.MediaTypeDocument} {
		t.Run(string(mediaType), func(t *testing.T) {
			old := "obsolete"
			item := &stash.Item{ID: "item", Type: mediaType, FileName: "original.bin", TelegramFileID: &old}
			backend, updates, downloads := recoveryBackend(t, []*stash.Item{item})
			field := map[stash.MediaType]string{stash.MediaTypeImage: "photo", stash.MediaTypeVideo: "video", stash.MediaTypeGIF: "animation", stash.MediaTypeDocument: "document"}[mediaType]
			var calls atomic.Int32
			tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: wrong file identifier/HTTP URL specified"}`)
					return
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				f, _, err := r.FormFile(field)
				if err != nil {
					t.Error(err)
					return
				}
				data, _ := io.ReadAll(f)
				_ = f.Close()
				if string(data) != "original-item" {
					t.Error("wrong original file")
				}
				message := telego.Message{MessageID: 12}
				switch mediaType {
				case stash.MediaTypeImage:
					message.Photo = []telego.PhotoSize{{FileID: "fresh"}}
				case stash.MediaTypeVideo:
					message.Video = &telego.Video{FileID: "fresh"}
				case stash.MediaTypeGIF:
					message.Animation = &telego.Animation{FileID: "fresh"}
				default:
					message.Document = &telego.Document{FileID: "fresh"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": message})
			}))
			defer tg.Close()
			b, err := New(testTelegramToken, 1, backend, tg.URL)
			if err != nil {
				t.Fatal(err)
			}
			ids := sendPageFilesCollectIDs(b, 1, []*stash.Item{item})
			if !reflect.DeepEqual(ids, []int{12}) || calls.Load() != 2 || downloads.Load() != 1 {
				t.Fatalf("singleton recovery: ids=%v calls=%d downloads=%d", ids, calls.Load(), downloads.Load())
			}
			awaitCacheUpdates(t, updates, map[string]string{"item": "fresh"})
		})
	}
}

func TestAlbumCacheDoesNotAssignCachedPhotoToNewItem(t *testing.T) {
	old := "already-cached"
	items := []*stash.Item{
		{ID: "a", Type: stash.MediaTypeImage, TelegramFileID: &old},
		{ID: "b", Type: stash.MediaTypeImage},
	}
	backend, updates, _ := recoveryBackend(t, items)
	b := &Bot{stash: backend}
	cacheMediaGroupFileIDs(b, []mediaSlot{{items[0], false}, {items[1], true}}, []telego.Message{
		{MessageID: 22, Photo: []telego.PhotoSize{{FileID: "new-photo"}}},
		{MessageID: 21, Photo: []telego.PhotoSize{{FileID: old}}},
	})
	awaitCacheUpdates(t, updates, map[string]string{"b": "new-photo"})
	if *items[0].TelegramFileID != old {
		t.Fatal("existing photo cache changed")
	}
}

func TestMediaRecoveryIsBoundedAndDoesNotRetryOtherErrors(t *testing.T) {
	for _, tc := range []struct {
		code        int
		description string
		recover     bool
	}{
		{400, "invalid remote file identifier", true},
		{400, "caption is too long", false},
		{403, "invalid remote file identifier", false},
		{429, "invalid remote file identifier", false},
		{500, "invalid remote file identifier", false},
	} {
		t.Run(fmt.Sprint(tc.code, tc.description), func(t *testing.T) {
			old := "obsolete"
			items := []*stash.Item{{ID: "a", Type: stash.MediaTypeImage, TelegramFileID: &old}, {ID: "b", Type: stash.MediaTypeImage, TelegramFileID: &old}}
			backend, _, downloads := recoveryBackend(t, items)
			var calls atomic.Int32
			tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": tc.code, "description": tc.description})
			}))
			defer tg.Close()
			b, err := New(testTelegramToken, 1, backend, tg.URL)
			if err != nil {
				t.Fatal(err)
			}
			if ids := sendMediaGroupItems(b, 1, items); len(ids) != 0 {
				t.Fatal("failed request returned message IDs")
			}
			wantCalls, wantDownloads := int32(1), int32(0)
			if tc.recover {
				wantCalls, wantDownloads = 2, 2
			}
			if calls.Load() != wantCalls || downloads.Load() != wantDownloads {
				t.Fatalf("unexpected retry: calls=%d downloads=%d", calls.Load(), downloads.Load())
			}
		})
	}
}
