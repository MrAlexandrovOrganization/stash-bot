package bot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
)

const testTelegramToken = "123456:" + "abcdefghijklmnopqrstuvwxyz123456789"

type fileCallerFunc func(context.Context, string, *telegoapi.RequestData) (*telegoapi.Response, error)

func (f fileCallerFunc) Call(ctx context.Context, url string, data *telegoapi.RequestData) (*telegoapi.Response, error) {
	return f(ctx, url, data)
}

func TestGetFileTimeout(t *testing.T) {
	for _, parentTimeout := range []time.Duration{0, time.Second} {
		t.Run(parentTimeout.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				wantTimeout := telegramGetFileTimeout
				if parentTimeout != 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, parentTimeout)
					defer cancel()
					wantTimeout = parentTimeout
				}
				api, err := telego.NewBot(testTelegramToken, telego.WithAPICaller(fileCallerFunc(
					func(ctx context.Context, _ string, _ *telegoapi.RequestData) (*telegoapi.Response, error) {
						deadline, ok := ctx.Deadline()
						if !ok || time.Until(deadline) != wantTimeout {
							t.Fatalf("GetFile deadline = %v, present = %v; want timeout %v", deadline, ok, wantTimeout)
						}
						<-ctx.Done()
						return nil, ctx.Err()
					})))
				if err != nil {
					t.Fatal(err)
				}
				b := &Bot{fileAPI: api}
				start := time.Now()
				_, err = b.getFile(ctx, "file-id")
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != wantTimeout {
					t.Fatalf("getFile() error = %v, duration = %v", err, time.Since(start))
				}
			})
		})
	}
}

func TestGetFileInFlightCancellation(t *testing.T) {
	for _, upload := range []bool{false, true} {
		name := "downloadFile"
		if upload {
			name = "handleUpload"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					// Read the request body so the server can detect disconnects.
					_, _ = io.Copy(io.Discard, r.Body)
					close(started)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-release:
					}
					return
				}
				_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
			}))
			defer server.Close()
			defer close(release)
			b, err := New(testTelegramToken, 1, nil, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if upload {
					b.handleUpload(ctx, &telego.Message{Chat: telego.Chat{ID: 1},
						Document: &telego.Document{FileID: "file-id"}}, stash.MediaTypeDocument)
					done <- nil
					return
				}
				_, err := b.downloadFile(ctx, "file-id")
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("GetFile did not start")
			}
			cancel()
			select {
			case err := <-done:
				if !upload && (!errors.Is(err, context.Canceled) || strings.Contains(err.Error(), testTelegramToken)) {
					t.Fatalf("downloadFile() error = %v; want sanitized cancellation", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("GetFile did not return promptly after cancellation")
			}
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("GetFile HTTP request was not canceled")
			}
		})
	}
}

func TestDownloadFileHTTPFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot" + testTelegramToken + "/getFile":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"file-id","file_path":"/missing/`+testTelegramToken+`/documents/test.txt"}}`)
		case "/file/bot" + testTelegramToken + "/documents/test.txt":
			_, _ = io.WriteString(w, "file content")
		default:
			t.Errorf("unexpected request path")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	b, err := New(testTelegramToken, 1, nil, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := b.downloadFile(t.Context(), "file-id")
	if err != nil || string(data) != "file content" {
		t.Fatalf("downloadFile() = %q, %v", data, err)
	}
}

func TestDownloadTelegramHTTPPreservesSanitizedCause(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		want := context.Canceled
		if timeout {
			cancel()
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		}
		cancel()
		_, err := downloadTelegramHTTP(ctx, "http://127.0.0.1/file/bot"+testTelegramToken+"/test.txt")
		if !errors.Is(err, want) || strings.Contains(err.Error(), testTelegramToken) {
			t.Fatalf("downloadTelegramHTTP() error = %v; want sanitized %v", err, want)
		}
	}
}

func TestHandleUploadDownloadStatusDoesNotLogToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"file-id","file_path":"test.txt"}}`)
		case strings.Contains(r.URL.Path, "/file/"):
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		}
	}))
	defer server.Close()
	b, err := New(testTelegramToken, 1, nil, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	b.telegramAPIURL = "" // Exercise HTTP without attempting a shared-volume read.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	b.handleUpload(t.Context(), &telego.Message{Chat: telego.Chat{ID: 1},
		Document: &telego.Document{FileID: "file-id"}}, stash.MediaTypeDocument)
	if !strings.Contains(logs.String(), "status=502") {
		t.Fatalf("missing download status log: %s", &logs)
	}
	if strings.Contains(logs.String(), testTelegramToken) || strings.Contains(logs.String(), "file_url") {
		t.Fatal("download status log contains token or file URL")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestReadLimitedAcceptsLimit(t *testing.T) {
	got, err := readLimitedBytes(io.LimitReader(zeroReader{}, 4), 4)
	if err != nil {
		t.Fatalf("readLimited() error = %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("readLimited() returned %d bytes, want 4", len(got))
	}
}

func TestReadLimitedRejectsOversizedInput(t *testing.T) {
	if _, err := readLimitedBytes(io.LimitReader(zeroReader{}, 5), 4); err == nil {
		t.Fatal("readLimited() accepted oversized input")
	}
}
