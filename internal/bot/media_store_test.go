package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mymmrac/telego"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stash-bot/internal/stash"
)

func mediaUsage(s *mediaStore) (used, cached int64, entries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.cached, len(s.cache)
}

func spoolFixture(t *testing.T, s *mediaStore, body string) *mediaLease {
	t.Helper()
	l, err := s.spool(t.Context(), strings.NewReader(body), int64(len(body)), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestMediaCacheEvictionAndActiveReaders(t *testing.T) {
	dir := t.TempDir()
	s := newMediaStore(dir, 12, 8)
	defer s.Close()
	a := spoolFixture(t, s, "aaaa")
	s.put("a", a)
	a.Close()
	b := spoolFixture(t, s, "bbbb")
	s.put("b", b)
	b.Close()
	active := s.get("a") // Touch a; b is now least recently used.
	c := spoolFixture(t, s, "cccc")
	s.put("c", c)
	c.Close()
	if hit := s.get("b"); hit != nil {
		hit.Close()
		t.Fatal("LRU cache retained b")
	}
	// Eviction must not close an active reader or forget its allocated bytes.
	s.Close()
	if used, cached, _ := mediaUsage(s); used != 4 || cached != 0 {
		t.Fatalf("active bytes after eviction: %d %d", used, cached)
	}
	data, err := io.ReadAll(active)
	if err != nil || string(data) != "aaaa" {
		t.Fatalf("evicted active reader: %q %v", data, err)
	}
	active.Close()
	active.Close()
	if used, _, _ := mediaUsage(s); used != 0 {
		t.Fatal("lease leaked temporary bytes")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary names survive while descriptors are open")
	}
}

func TestMediaBudgetCountsLeasedFilesAndFailsWithoutWaiting(t *testing.T) {
	s := newMediaStore(t.TempDir(), 8, 8)
	defer s.Close()
	a := spoolFixture(t, s, "aaaaaaaa")
	s.put("a", a)
	if _, err := s.spool(t.Context(), strings.NewReader("x"), 1, 1); !errors.Is(err, errMediaBudget) {
		t.Fatalf("active bytes bypassed budget: %v", err)
	}
	// a was evicted but stays readable and charged until its lease closes.
	if used, cached, _ := mediaUsage(s); used != 8 || cached != 0 {
		t.Fatalf("accounting: %d %d", used, cached)
	}
	a.Close()
	b := spoolFixture(t, s, "bbbbbbbb")
	b.Close()
	if used, _, _ := mediaUsage(s); used != 0 {
		t.Fatal("budget did not recover")
	}
}

type mediaErrorReader struct{ err error }

func (r mediaErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestMediaCopyErrorsCancellationAndMetadata(t *testing.T) {
	s := newMediaStore(t.TempDir(), 64, 16)
	defer s.Close()
	if _, err := s.spool(t.Context(), mediaErrorReader{errors.New("must not read")}, 65, 64); !errors.Is(err, errMediaTooLarge) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.spool(ctx, strings.NewReader("data"), 4, 64); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	readErr := &os.PathError{Op: "read", Path: "/private/" + testTelegramToken, Err: os.ErrPermission}
	if _, err := s.spool(t.Context(), io.MultiReader(strings.NewReader("partial"), mediaErrorReader{readErr}), -1, 64); !errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), testTelegramToken) {
		t.Fatalf("unsanitized copy error: %v", err)
	}
	if used, _, _ := mediaUsage(s); used != 0 {
		t.Fatal("failed/cancelled copies leaked disk budget")
	}
}

func TestMediaCopyAllocationDoesNotScaleWithFileSize(t *testing.T) {
	s := newMediaStore(t.TempDir(), 64<<20, 0)
	defer s.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	l, err := s.spool(t.Context(), io.LimitReader(zeroReader{}, 32<<20), 32<<20, 32<<20)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("32 MiB copy allocated %d heap bytes", allocated)
	}
}

func TestConcurrentMediaCopiesRespectSharedBudget(t *testing.T) {
	s := newMediaStore(t.TempDir(), 64, 0)
	defer s.Close()
	results := make(chan *mediaLease, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			l, err := s.spool(t.Context(), io.LimitReader(zeroReader{}, 16), 16, 16)
			if err != nil && !errors.Is(err, errMediaBudget) {
				t.Error(err)
			}
			results <- l
		})
	}
	wg.Wait()
	close(results)
	var leases []*mediaLease
	for l := range results {
		if l != nil {
			leases = append(leases, l)
		}
	}
	if used, _, _ := mediaUsage(s); used != 64 || len(leases) != 4 {
		t.Fatalf("concurrent accounting: bytes=%d leases=%d", used, len(leases))
	}
	for _, l := range leases {
		l.Close()
	}
	if used, _, _ := mediaUsage(s); used != 0 {
		t.Fatal("copies leaked the shared budget")
	}
}

func TestMediaDownloadConcurrencyAndCancellation(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/file") {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			io.WriteString(w, "fixture")
			return
		}
		json.NewEncoder(w).Encode(stash.Item{ID: strings.TrimPrefix(r.URL.Path, "/items/"), Size: 7})
	}))
	defer server.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	b := &Bot{stash: stash.NewClient(server.URL)}
	defer b.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			l, err := loadItemFile(ctx, b, &stash.Item{ID: fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
				return
			}
			l.Close()
		})
	}
	for range mediaParallelDownloads {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("downloads did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("download admission exceeded limit")
	default:
	}
	// An already cancelled waiter must not take a newly available slot.
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if release, err := acquireMedia(cancelled, b.mediaStore().downloads); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	if peak.Load() > mediaParallelDownloads {
		t.Fatalf("parallel downloads=%d", peak.Load())
	}
	if used, _, _ := mediaUsage(b.mediaStore()); used != 0 {
		t.Fatal("completed transfers leaked disk bytes")
	}
}

func TestKnownOversizeUploadDoesNotCallGetFile(t *testing.T) {
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			gets.Add(1)
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()
	b, err := New(testTelegramToken, 1, nil, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.handleUpload(t.Context(), largeDocumentMessage(), stash.MediaTypeDocument)
	if gets.Load() != 0 {
		t.Fatal("oversize message reached getFile")
	}
}

func largeDocumentMessage() *telego.Message {
	return &telego.Message{Chat: telego.Chat{ID: 1}, Document: &telego.Document{FileID: "oversize", FileName: "large.bin", FileSize: maxTelegramFileBytes + 1}}
}

func TestAlbumBudgetFailureCleansEveryPreparedFile(t *testing.T) {
	items := []*stash.Item{{ID: "a", Type: stash.MediaTypeImage}, {ID: "b", Type: stash.MediaTypeImage}}
	backend, _, _ := recoveryBackend(t, items)
	var calls atomic.Int32
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"ok":true,"result":[]}`)
	}))
	defer tg.Close()
	b, err := New(testTelegramToken, 1, backend, tg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.media = newMediaStore(t.TempDir(), 15, 0)
	if ids := sendMediaGroupItems(b, 1, items); len(ids) != 0 || calls.Load() != 0 {
		t.Fatal("part of an over-budget album was sent")
	}
	if used, _, _ := mediaUsage(b.mediaStore()); used != 0 {
		t.Fatal("failed album leaked prepared files")
	}
}

func TestAlbumStaleIDRetryReleasesFirstAttempt(t *testing.T) {
	old := "obsolete"
	items := []*stash.Item{{ID: "a", Type: stash.MediaTypeImage, TelegramFileID: &old}, {ID: "b", Type: stash.MediaTypeImage}}
	backend, _, _ := recoveryBackend(t, items)
	var calls atomic.Int32
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		drainMediaMultipart(t, r)
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"ok":false,"error_code":400,"description":"invalid file identifier"}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":[{"message_id":1},{"message_id":2}]}`)
	}))
	defer tg.Close()
	b, err := New(testTelegramToken, 1, backend, tg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.media = newMediaStore(t.TempDir(), 20, 0)
	if ids := sendMediaGroupItems(b, 1, items); len(ids) != 2 || calls.Load() != 2 {
		t.Fatal("first attempt kept bytes reserved during retry")
	}
	if used, _, _ := mediaUsage(b.mediaStore()); used != 0 {
		t.Fatal("album retry leaked temporary files")
	}
}

func TestFailedMediaSendReleasesFilesAndAdmission(t *testing.T) {
	item := &stash.Item{ID: "a", Type: stash.MediaTypeDocument}
	backend, _, _ := recoveryBackend(t, []*stash.Item{item})
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		drainMediaMultipart(t, r)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer tg.Close()
	b, err := New(testTelegramToken, 1, backend, tg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := sendItemMessage(b, 1, item, "", false); err == nil {
		t.Fatal("expected failed send")
	}
	s := b.mediaStore()
	if used, _, _ := mediaUsage(s); used != 0 || len(s.sends) != 0 || len(s.downloads) != 0 {
		t.Fatal("failed send leaked files or admission")
	}
}

func TestMediaSendConcurrency(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer tg.Close()
	var once sync.Once
	defer once.Do(func() { close(release) })
	b, err := New(testTelegramToken, 1, nil, tg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			fid := "cached"
			_, err := sendItemMessage(b, 1, &stash.Item{ID: fmt.Sprint(i), Type: stash.MediaTypeDocument, TelegramFileID: &fid}, "", false)
			if err != nil {
				t.Error(err)
			}
		})
	}
	for range mediaParallelSends {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("send did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("send admission exceeded limit")
	default:
	}
	once.Do(func() { close(release) })
	wg.Wait()
	if peak.Load() > mediaParallelSends {
		t.Fatalf("parallel sends=%d", peak.Load())
	}
}

// This full-size fixture also runs in CI inside a container with 512 MiB RAM.
// Both fake services stream/discard multipart parts without buffering media.
func TestMediaLargeTransferWithinMemoryLimit(t *testing.T) {
	if os.Getenv("STASH_MEDIA_MEMORY_TEST") != "1" {
		t.Skip("run make media-memory-test for the 512 MiB container fixture")
	}
	items := []*stash.Item{{ID: "large", Type: stash.MediaTypeDocument, FileName: "large.bin", Size: maxTelegramFileBytes}}
	for i := range 10 {
		items = append(items, &stash.Item{ID: fmt.Sprint(i), Type: stash.MediaTypeImage, FileName: "same.jpg", Size: 16 << 20})
	}
	var uploaded atomic.Int64
	patches := make(chan struct{}, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/items" {
			uploaded.Add(drainMediaMultipart(t, r))
			json.NewEncoder(w).Encode(items[0])
			return
		}
		if r.Method == http.MethodPatch {
			patches <- struct{}{}
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/items/"), "/file")
		var item *stash.Item
		for _, it := range items {
			if it.ID == id {
				copy := *it
				item = &copy
				break
			}
		}
		if item == nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/file") {
			io.CopyN(w, zeroReader{}, item.Size)
			return
		}
		json.NewEncoder(w).Encode(item)
	}))
	defer backend.Close()
	var bytes atomic.Int64
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"fixture","file_path":"large.bin","file_size":%d}}`, maxTelegramFileBytes)
			return
		}
		if strings.Contains(r.URL.Path, "/file/") {
			io.CopyN(w, zeroReader{}, maxTelegramFileBytes)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Error(err)
					return
				}
				n, err := io.Copy(io.Discard, p)
				if err != nil {
					t.Error(err)
					return
				}
				if p.FileName() != "" {
					bytes.Add(n)
				}
				p.Close()
			}
		}
		if strings.HasSuffix(r.URL.Path, "/sendMediaGroup") {
			io.WriteString(w, `{"ok":true,"result":[{"message_id":1},{"message_id":2},{"message_id":3},{"message_id":4},{"message_id":5},{"message_id":6},{"message_id":7},{"message_id":8},{"message_id":9},{"message_id":10}]}`)
		} else {
			io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
		}
	}))
	defer tg.Close()
	b, err := New(testTelegramToken, 1, stash.NewClient(backend.URL), tg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.handleUpload(t.Context(), &telego.Message{From: &telego.User{ID: 1}, Chat: telego.Chat{ID: 1}, Document: &telego.Document{FileID: "fixture", FileName: "large.bin", FileSize: maxTelegramFileBytes}}, stash.MediaTypeDocument)
	if uploaded.Load() != maxTelegramFileBytes {
		t.Fatalf("uploaded %d bytes", uploaded.Load())
	}
	select {
	case <-patches:
	case <-time.After(3 * time.Second):
		t.Fatal("uploaded file cache was not persisted")
	}
	b.fileIDCache.Delete("large")
	if _, err := sendItemMessage(b, 1, items[0], "", false); err != nil {
		t.Fatal(err)
	}
	prefetchItems(b, items[1:])
	if _, cached, _ := mediaUsage(b.mediaStore()); cached > mediaCacheBytes {
		t.Fatal("prefetch exceeded cache limit")
	}
	if ids := sendMediaGroupItems(b, 1, items[1:]); len(ids) != 10 {
		t.Fatalf("album sent %d messages", len(ids))
	}
	if bytes.Load() != maxTelegramFileBytes+10*(16<<20) {
		t.Fatalf("streamed %d bytes", bytes.Load())
	}
	b.Close()
	if used, _, _ := mediaUsage(b.mediaStore()); used != 0 {
		t.Fatalf("fixture leaked %d temporary bytes", used)
	}
}

func drainMediaMultipart(t *testing.T, r *http.Request) int64 {
	t.Helper()
	mr, err := r.MultipartReader()
	if err != nil {
		t.Error(err)
		return 0
	}
	var bytes int64
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return bytes
		}
		if err != nil {
			t.Error(err)
			return bytes
		}
		n, err := io.Copy(io.Discard, p)
		if err != nil {
			t.Error(err)
			return bytes
		}
		if p.FileName() != "" {
			bytes += n
		}
		p.Close()
	}
}
