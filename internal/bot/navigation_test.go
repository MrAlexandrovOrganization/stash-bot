package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
	"stash-bot/internal/bot/resources"
	"stash-bot/internal/stash"
)

type navigationFixture struct {
	mu            sync.Mutex
	methods       []string
	texts         []string
	queries       []string
	deleted       []int
	failDelete    bool
	failSearch    bool
	similarStatus int
}

func navigationBot(t *testing.T) (*Bot, *navigationFixture) {
	t.Helper()
	f := &navigationFixture{}
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.methods = append(f.methods, method)
		var body struct {
			Text       string `json:"text"`
			MessageIDs []int  `json:"message_ids"`
			MessageID  int    `json:"message_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Text != "" {
			f.texts = append(f.texts, body.Text)
		}
		if method == "deleteMessages" || method == "deleteMessage" {
			if f.failDelete {
				_, _ = io.WriteString(w, `{"ok":false,"error_code":500,"description":"synthetic failure"}`)
				return
			}
			if method == "deleteMessages" {
				f.deleted = append(f.deleted, body.MessageIDs...)
			} else {
				f.deleted = append(f.deleted, body.MessageID)
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		}
		if method == "answerCallbackQuery" {
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		}
		id := body.MessageID
		if id == 0 {
			id = 1000 + len(f.methods)
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"document":{"file_id":"fixture-file","file_unique_id":"fixture-unique"}}}`, id)
	}))
	t.Cleanup(tg.Close)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.queries = append(f.queries, r.URL.String())
		if f.failSearch {
			http.Error(w, "synthetic failure", 500)
			return
		}
		if r.URL.Path == "/similar" {
			if f.similarStatus != 0 {
				w.WriteHeader(f.similarStatus)
				return
			}
			_, _ = io.WriteString(w, `[]`)
			return
		}
		if r.URL.Path != "/items/page" {
			t.Errorf("unexpected backend request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("q") == "absent" {
			_, _ = io.WriteString(w, `{"items":[],"total":0}`)
			return
		}
		id := "library-first"
		if r.URL.Query().Get("offset") == "1120" {
			id = "library-last"
		}
		_ = json.NewEncoder(w).Encode(stash.SearchPage{Items: []*stash.Item{{ID: id, Type: stash.MediaTypeDocument, FileName: "fixture.txt", TelegramFileID: ptrString("fixture-file")}}, Total: 1128})
	}))
	t.Cleanup(backend.Close)
	b, err := New(testTelegramToken, 1, stash.NewClient(backend.URL), tg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b, f
}
func ptrString(s string) *string { return &s }

func TestStorageReplacesSearchAndPagesBeyond1000(t *testing.T) {
	b, f := navigationBot(t)
	sess := b.session(1, 1)
	sess.Browse = BrowseSearch
	sess.Items = []*stash.Item{{ID: "old-search"}}
	sess.MediaMsgIDs = []int{10}
	sess.LastMsgID = 20
	handleStorage(t.Context(), b, CallbackContext{chatID: 1, session: sess})
	if len(sess.Items) != 1 || sess.Items[0].ID != "library-first" || sess.Browse != BrowseStorage || sess.Total != 1128 {
		t.Fatalf("wrong library: %#v", sess)
	}
	handleStoragePage(t.Context(), b, CallbackContext{chatID: 1, session: sess, data: "112"})
	if sess.CurrentPage != 112 || sess.Items[0].ID != "library-last" {
		t.Fatal("last page inaccessible")
	}
	handleSelectItem(t.Context(), b, CallbackContext{chatID: 1, session: sess, data: "0"})
	if sess.CurrentItem == nil || sess.CurrentItem.ID != "library-last" || sess.Back != ScreenStorage {
		t.Fatal("wrong item on remote page")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.deleted, 10) || !strings.Contains(f.queries[len(f.queries)-1], "offset=1120") {
		t.Fatal("old media or pagination request wrong")
	}
	if !strings.Contains(strings.Join(f.texts, "\n"), "1121.") {
		t.Fatal("global item numbering lost")
	}
}

func TestEmptyAndFailedSearchClearPreviousResults(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			b, f := navigationBot(t)
			f.failSearch = failure
			sess := b.session(1, 1)
			sess.Items = []*stash.Item{{ID: "old"}}
			sess.MediaMsgIDs = []int{10}
			sess.LastMsgID = 20
			doSearch(t.Context(), b, 1, sess, "absent")
			if len(sess.Items) != 0 || len(sess.MediaMsgIDs) != 0 || sess.CurrentItem != nil || sess.LastMsgID == 0 {
				t.Fatal("previous results survived")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if !slices.Contains(f.deleted, 10) {
				t.Fatal("previous media not deleted")
			}
			if failure && len(f.queries) != 1 {
				t.Fatal("backend failure was treated as no matches")
			}
		})
	}
}

func TestDeleteFailureKeepsIDsAndRetries(t *testing.T) {
	b, f := navigationBot(t)
	f.failDelete = true
	sess := &Session{MediaMsgIDs: []int{10, 11}}
	deleteMediaMessages(b, 1, sess)
	if !slices.Equal(sess.MediaMsgIDs, []int{10, 11}) {
		t.Fatal("lost failed deletion IDs")
	}
	f.mu.Lock()
	f.failDelete = false
	f.mu.Unlock()
	deleteMediaMessages(b, 1, sess)
	if len(sess.MediaMsgIDs) != 0 {
		t.Fatal("successful retry not cleared")
	}
}

func TestCancelReturnsToMenuAndStaleButtonsCannotSelect(t *testing.T) {
	b, _ := navigationBot(t)
	sess := b.session(1, 1)
	sess.Screen = ScreenMain
	sess.LastMsgID = 20
	handleSearch(t.Context(), b, CallbackContext{chatID: 1, session: sess})
	handleCancel(t.Context(), b, CallbackContext{chatID: 1, session: sess})
	if sess.Screen != ScreenMain || sess.Pending != "" {
		t.Fatal("cancel did not restore menu")
	}
	sess.Items = []*stash.Item{{ID: "should-not-select"}}
	b.handleCallback(t.Context(), &telego.CallbackQuery{ID: "fixture", From: telego.User{ID: 1}, Message: &telego.Message{MessageID: 19, Chat: telego.Chat{ID: 1}}, Data: "si:0"})
	if sess.CurrentItem != nil {
		t.Fatal("stale keyboard changed current state")
	}
}

func TestSimilarUnavailableClearsMediaAndExplainsPendingIndex(t *testing.T) {
	b, f := navigationBot(t)
	f.similarStatus = 409
	sess := b.session(1, 1)
	sess.MediaMsgIDs = []int{10}
	sess.LastMsgID = 20
	runSimilar(t.Context(), b, 1, sess, stash.SimilarRequest{ItemID: "pending"})
	if len(sess.Items) != 0 || len(sess.MediaMsgIDs) != 0 {
		t.Fatal("failed similarity kept results")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.texts, resources.IndexPending) {
		t.Fatal("missing index not explained")
	}
}

func TestBackgroundItemUpdateCannotRestoreOldSelection(t *testing.T) {
	updated := &stash.Item{ID: "old", Description: "updated"}
	for _, current := range []*stash.Item{nil, {ID: "new"}} {
		sess := &Session{Screen: ScreenMain, CurrentItem: current, Items: []*stash.Item{{ID: "old"}}}
		applyItemUpdate(sess, updated)
		if sess.CurrentItem != current || sess.Items[0] != updated {
			t.Fatal("background description changed selection")
		}
	}
}

func TestSearchParsesTagsAcrossUnicodeWhitespace(t *testing.T) {
	text, pos, neg := parseSearchQuery("кот\n#Дом\t-#Реклама\u2003диван")
	if text != "кот диван" || !slices.Equal(pos, []string{"Дом"}) || !slices.Equal(neg, []string{"Реклама"}) {
		t.Fatalf("bad query: %q %v %v", text, pos, neg)
	}
}

func TestSessionsAreIsolatedBetweenChats(t *testing.T) {
	b, _ := navigationBot(t)
	one, two := b.session(1, 1), b.session(1, 2)
	one.MediaMsgIDs = []int{10}
	if one == two || len(two.MediaMsgIDs) != 0 {
		t.Fatal("different chats share navigation state")
	}
}
