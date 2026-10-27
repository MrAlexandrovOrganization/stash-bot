package bot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

func TestStartSendsFreshMenuAndCancelsPendingInput(t *testing.T) {
	for _, pending := range []string{"", "search", "simimg", "desc"} {
		t.Run("pending="+pending, func(t *testing.T) {
			var methods []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				methods = append(methods, method)
				var body struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !strings.Contains(body.Text, "личное хранилище") {
					t.Error("/start did not show the main menu")
				}
				_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":200}}`)
			}))
			defer server.Close()
			b, err := New(testTelegramToken, 1, nil, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			sess := b.session(1, 1)
			sess.LastMsgID = 100 // An old control message above the user's command.
			sess.Pending = pending
			sess.Screen = ScreenItem
			update := telego.Update{Message: &telego.Message{
				From: &telego.User{ID: 1}, Chat: telego.Chat{ID: 1}, Text: "/start",
			}}
			b.handleUpdate(update)
			b.handleUpdate(update) // Repeated /start must also create a visible reply.
			if len(methods) != 2 || methods[0] != "sendMessage" || methods[1] != "sendMessage" {
				t.Fatalf("/start requests = %v, want two fresh sendMessage calls", methods)
			}
			if sess.Pending != "" || sess.Screen != ScreenMain || sess.LastMsgID != 200 {
				t.Fatalf("/start did not reset navigation: %#v", sess)
			}
		})
	}
}
