package bot

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	mediaDiskBytes         = 1 << 30
	mediaCacheBytes        = 128 << 20
	mediaCacheEntries      = 128
	mediaParallelDownloads = 2
	mediaParallelSends     = 2
)

var (
	errMediaBudget   = errors.New("temporary media byte budget exhausted")
	errMediaTooLarge = errors.New("media file exceeds size limit")
)

// All bytes, including active readers and cached files, share one disk budget.
// Files are unlinked immediately: Close or process termination releases them,
// including after SIGKILL. This directory must be backed by disk, not tmpfs.
type mediaStore struct {
	mu                               sync.Mutex
	directory                        string
	maxBytes, cacheMax, used, cached int64
	cache                            map[string]*list.Element
	lru                              list.List
	closed                           bool
	downloads, sends                 chan struct{}
}

type mediaBlob struct {
	file            *os.File
	size, allocated int64
	refs            int
	key             string
}

type mediaLease struct {
	*io.SectionReader
	store *mediaStore
	blob  *mediaBlob
	once  sync.Once
}

func newMediaStore(directory string, maxBytes, cacheBytes int64) *mediaStore {
	return &mediaStore{directory: directory, maxBytes: maxBytes, cacheMax: cacheBytes,
		cache: make(map[string]*list.Element), downloads: make(chan struct{}, mediaParallelDownloads),
		sends: make(chan struct{}, mediaParallelSends)}
}

func (b *Bot) mediaStore() *mediaStore {
	b.mediaOnce.Do(func() {
		if b.media == nil {
			b.media = newMediaStore("", mediaDiskBytes, mediaCacheBytes)
		}
	})
	return b.media
}

func acquireMedia(ctx context.Context, sem chan struct{}) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-sem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *mediaStore) leaseLocked(blob *mediaBlob) *mediaLease {
	blob.refs++
	return &mediaLease{SectionReader: io.NewSectionReader(blob.file, 0, blob.size), store: s, blob: blob}
}

func (l *mediaLease) Close() error {
	l.once.Do(func() {
		l.store.mu.Lock()
		defer l.store.mu.Unlock()
		l.blob.refs--
		l.store.releaseLocked(l.blob)
	})
	return nil
}

func (s *mediaStore) releaseLocked(blob *mediaBlob) {
	if blob.refs == 0 && blob.key == "" {
		blob.file.Close()
		s.used -= blob.allocated
		blob.allocated = 0
	}
}

func (s *mediaStore) evictLocked(element *list.Element) {
	blob := element.Value.(*mediaBlob)
	delete(s.cache, blob.key)
	s.lru.Remove(element)
	s.cached -= blob.size
	blob.key = ""
	s.releaseLocked(blob)
}

func (s *mediaStore) get(key string) *mediaLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if element := s.cache[key]; element != nil {
		s.lru.MoveToFront(element)
		return s.leaseLocked(element.Value.(*mediaBlob))
	}
	return nil
}

func (s *mediaStore) put(key string, lease *mediaLease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob := lease.blob
	if s.closed || blob.size > s.cacheMax || blob.key != "" {
		return
	}
	if previous := s.cache[key]; previous != nil {
		s.evictLocked(previous)
	}
	for s.lru.Len() > 0 && (s.cached+blob.size > s.cacheMax || s.lru.Len() >= mediaCacheEntries) {
		s.evictLocked(s.lru.Back())
	}
	blob.key = key
	s.cache[key] = s.lru.PushFront(blob)
	s.cached += blob.size
}

func (s *mediaStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for s.lru.Len() > 0 {
		s.evictLocked(s.lru.Back())
	}
}

type mediaWriter struct {
	ctx   context.Context
	store *mediaStore
	blob  *mediaBlob
	limit int64
}

func (w *mediaWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.limit-w.blob.size {
		return 0, errMediaTooLarge
	}
	s := w.store
	s.mu.Lock()
	for !s.closed && int64(len(data)) > s.maxBytes-s.used && s.lru.Len() > 0 {
		s.evictLocked(s.lru.Back())
	}
	if s.closed || int64(len(data)) > s.maxBytes-s.used {
		s.mu.Unlock()
		return 0, errMediaBudget
	}
	s.used += int64(len(data))
	w.blob.allocated += int64(len(data))
	s.mu.Unlock()
	n, err := w.blob.file.Write(data)
	w.blob.size += int64(n)
	return n, err
}

func (s *mediaStore) spool(ctx context.Context, src io.Reader, knownSize, limit int64) (*mediaLease, error) {
	if knownSize > limit {
		return nil, errMediaTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(s.directory, "stash-bot-media-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary media file: %w", err)
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, fmt.Errorf("unlink temporary media file: %w", err)
	}
	blob := &mediaBlob{file: f, refs: 1}
	lease := &mediaLease{store: s, blob: blob}
	_, err = io.CopyBuffer(&mediaWriter{ctx: ctx, store: s, blob: blob, limit: limit}, io.LimitReader(src, limit+1), make([]byte, 32<<10))
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		lease.Close()
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil, fmt.Errorf("read media file: %w", pathErr.Err)
		}
		return nil, err
	}
	lease.SectionReader = io.NewSectionReader(f, 0, blob.size)
	return lease, nil
}
