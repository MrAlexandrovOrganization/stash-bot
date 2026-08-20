package bot

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ── Callback routing ──────────────────────────────────────────────────────────

var callbackHandlers = map[string]func(context.Context, *Bot, CallbackContext){
	"noop":    handleNoop,
	"menu":    handleMenu,
	"storage": handleStorage,
	"search":  handleSearch,
	"sp":      handleStoragePage,
	"ssel":    handleSelectMode,
	"si":      handleSelectItem,
	"edit":    handleEdit,
	"file":    handleFile,
	"del":     handleDelete,
	"back":    handleBack,
	"cancel":  handleCancel,
}

type CallbackContext struct {
	data    string
	chatID  int64
	userID  int64
	session *Session
}

func handleNoop(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("noop callback", "data", cc.data)
}

func handleMenu(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("menu callback", "data", cc.data)
	cc.session.Pending = ""
	showMainMenu(b, cc.chatID, cc.session)
}

func handleStorage(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("storage callback", "data", cc.data)
	cc.session.Pending = ""
	loadStorageAndShow(b, cc.chatID, cc.session, true)
}

func handleSearch(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("search callback", "data", cc.data)
	cc.session.Pending = "search"
	cc.session.LastMsgID = editOrSendHTML(b, cc.chatID, cc.session.LastMsgID, "🔍 Введи поисковый запрос:\n\nФормат: <code>текст #тег -#исключить</code>", cancelKeyboard())
}

func handleStoragePage(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("storage page callback", "data", cc.data)

	page, err := strconv.Atoi(cc.data)
	if err != nil {
		slog.Error("callback sp: bad page", "payload", cc.data)
		return
	}
	totalPages := (len(cc.session.Items) + pageSize - 1) / pageSize
	if page < 0 || page >= totalPages {
		slog.Warn("callback sp: page out of range", "page", page, "total", totalPages)
		return
	}
	slog.Info("navigate page", "page", page, "total_pages", totalPages)
	cc.session.CurrentPage = page
	sendStoragePage(b, cc.chatID, cc.session, true)
}

func handleSelectMode(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("select mode callback", "data", cc.data, "page", cc.session.CurrentPage)
	cc.session.Screen = ScreenSelect
	showSelectMode(b, cc.chatID, cc.session)
}

func handleSelectItem(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("select item callback", "data", cc.data)

	idx, err := strconv.Atoi(cc.data)
	if err != nil {
		slog.Error("callback si: bad index", "payload", cc.data)
		return
	}
	globalIdx := cc.session.CurrentPage*pageSize + idx
	if globalIdx < 0 || globalIdx >= len(cc.session.Items) {
		slog.Error("callback si: index out of range", "global_idx", globalIdx, "total", len(cc.session.Items))
		return
	}
	cc.session.CurrentItem = cc.session.Items[globalIdx]
	cc.session.Back = cc.session.Screen
	cc.session.Screen = ScreenItem
	slog.Info("selected item", "id", cc.session.CurrentItem.ID, "name", cc.session.CurrentItem.FileName)
	showItem(b, cc.chatID, cc.session)
}

func handleEdit(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("edit callback", "data", cc.data)

	if cc.session.CurrentItem == nil {
		return
	}
	prompt := buildEditPrompt(cc.data, cc.session)
	if prompt == "" {
		slog.Error("callback edit: unknown field", "field", cc.data)
		return
	}
	slog.Info("start field edit", "field", cc.data)
	cc.session.Pending = cc.data
	cc.session.LastMsgID = editOrSendHTML(b, cc.chatID, cc.session.LastMsgID, prompt, cancelKeyboard())
}

func handleFile(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("callback file", "data", cc.data)

	if cc.session.CurrentItem == nil {
		slog.Warn("callback file: no current item")
		return
	}
	slog.Info("send file", "id", cc.session.CurrentItem.ID, "name", cc.session.CurrentItem.FileName)
	sendFile(b, cc.chatID, cc.session.CurrentItem)
}

func handleDelete(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("callback del", "data", cc.data)

	if cc.session.CurrentItem == nil {
		slog.Warn("callback del: no current item")
		return
	}
	itemID := cc.session.CurrentItem.ID
	slog.Info("deleting item", "id", itemID)
	if err := b.stash.Delete(context.Background(), itemID); err != nil {
		slog.Error("delete failed", "id", itemID, "error", err)
		send(b, cc.chatID, "Ошибка при удалении.")
		return
	}
	slog.Info("item deleted", "id", itemID)
	removeFromSession(cc.session, itemID)
	cc.session.CurrentItem = nil
	cc.session.Screen = cc.session.Back
	send(b, cc.chatID, "🗑 Удалено.")
	sendStoragePage(b, cc.chatID, cc.session, true)
}

func handleBack(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("callback back", "data", cc.data)

	cc.session.Pending = ""
	slog.Info("back", "from", cc.session.Screen, "to", cc.session.Back)
	switch cc.session.Back {
	case ScreenStorage:
		cc.session.Screen = ScreenStorage
		sendStoragePage(b, cc.chatID, cc.session, false)
	default:
		showMainMenu(b, cc.chatID, cc.session)
	}
}

func handleCancel(ctx context.Context, b *Bot, cc CallbackContext) {
	cc.session.Pending = ""
	slog.Info("cancel pending input")
	if cc.session.Screen == ScreenItem && cc.session.CurrentItem != nil {
		showItem(b, cc.chatID, cc.session)
	} else {
		sendStoragePage(b, cc.chatID, cc.session, false)
	}
}

func (b *Bot) handleCallback(cb *telego.CallbackQuery) {
	_ = b.api.AnswerCallbackQuery(context.Background(), tu.CallbackQuery(cb.ID))

	if cb.From.ID != b.rootID {
		slog.Warn("callback: unauthorized", "user_id", cb.From.ID)
		return
	}

	msg, ok := cb.Message.(*telego.Message)
	if !ok || msg == nil {
		return
	}

	parts := strings.SplitN(cb.Data, ":", 2)
	action := parts[0]
	payload := ""
	if len(parts) == 2 {
		payload = parts[1]
	}

	chatID := msg.Chat.ID
	userID := cb.From.ID
	sess := b.session(userID, chatID)

	slog.Info("callback", "action", action, "payload", payload, "screen", sess.Screen)

	if callback, ok := callbackHandlers[action]; ok {
		callback(context.Background(), b, CallbackContext{
			chatID:  chatID,
			userID:  userID,
			session: sess,
			data:    payload,
		})
	} else {
		slog.Warn("callback: unknown action", "action", action)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// buildEditPrompt returns the prompt text for editing a specific item field.
// Returns an empty string for unknown fields.
func buildEditPrompt(field string, sess *Session) string {
	it := sess.CurrentItem
	switch field {
	case "desc":
		prompt := "Введи новое описание:"
		if it.Description != "" {
			prompt += "\n\nТекущее: " + escapeHTML(sanitizeUTF8(it.Description))
		}
		return prompt
	case "tags":
		prompt := "Введи теги через запятую или с #:\n<i>Пример: #утро, отдых, #лето</i>"
		if len(it.Tags) > 0 {
			current := make([]string, len(it.Tags))
			for i, t := range it.Tags {
				current[i] = "#" + t
			}
			prompt += "\n\nТекущие: " + escapeHTML(strings.Join(current, " "))
		}
		return prompt
	case "tr":
		prompt := "Введи расшифровку:"
		if it.Transcript != nil && *it.Transcript != "" {
			t := sanitizeUTF8(*it.Transcript)
			if len([]rune(t)) > 200 {
				t = string([]rune(t)[:200]) + "…"
			}
			prompt += "\n\nТекущая: " + escapeHTML(t)
		}
		return prompt
	}
	return ""
}

// removeFromSession removes an item by ID from the session's item list.
func removeFromSession(sess *Session, itemID string) {
	for i, it := range sess.Items {
		if it.ID == itemID {
			sess.Items = append(sess.Items[:i], sess.Items[i+1:]...)
			return
		}
	}
}
