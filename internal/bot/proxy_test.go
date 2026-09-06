package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestNewUsesStandardHTTPProxyTransport(t *testing.T) {
	// Keep this test serial: all three clients must inherit DefaultTransport.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request bypassed proxy: %s", r.URL.Path)
		http.Error(w, "proxy required", http.StatusBadGateway)
	}))
	defer origin.Close()
	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(chan string, 10)
	mainPath := "/bot" + testTelegramToken + "/getMe"
	filePath := "/bot" + testTelegramToken + "/getFile"
	downloadPath := "/file/bot" + testTelegramToken + "/documents/test.txt"
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Scheme != "http" || r.URL.Host != originURL.Host {
			t.Errorf("proxy received URL %s; want absolute URL for %s", r.URL, origin.URL)
		}
		paths <- r.URL.Path
		switch r.URL.Path {
		case mainPath:
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":123456,"is_bot":true,"first_name":"Test","username":"test_bot"}}`)
		case filePath:
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"file-id","file_path":"documents/test.txt"}}`)
		case downloadPath:
			_, _ = io.WriteString(w, "file content")
		default:
			t.Errorf("unexpected proxy request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() {
		http.DefaultTransport = previous
		transport.CloseIdleConnections()
	}()

	b, err := New(testTelegramToken, 1, nil, origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	me, err := b.api.GetMe(ctx)
	if err != nil {
		t.Fatalf("main API GetMe: %v", err)
	}
	if me.Username != "test_bot" {
		t.Fatalf("GetMe username = %q; want test_bot", me.Username)
	}
	data, err := b.downloadFile(ctx, "file-id")
	if err != nil || string(data) != "file content" {
		t.Fatalf("downloadFile() = %q, %v; want file content", data, err)
	}
	if len(paths) != 3 {
		t.Fatalf("proxy received %d requests; want 3", len(paths))
	}
	for _, want := range []string{mainPath, filePath, downloadPath} {
		if got := <-paths; got != want {
			t.Errorf("proxy request path = %q; want %q", got, want)
		}
	}
}
