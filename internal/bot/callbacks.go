package bot

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

	b.showMainMenu(cc.chatID, cc.session)
}

func handleStorage(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("storage callback", "data", cc.data)
	cc.session.Pending = ""

	b.loadStorageAndShow(cc.chatID, cc.session, true)
}

func handleSearch(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("search callback", "data", cc.data)

	cc.session.Pending = "search"
	cc.session.LastMsgID = b.editOrSendHTML(cc.chatID, cc.session.LastMsgID, "🔍 Введи поисковый запрос:\n\nФормат: <code>текст #тег -#исключить</code>", cancelKeyboard())
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
	b.sendStoragePage(cc.chatID, cc.session, true)
}

func handleSelectMode(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("select mode callback", "data", cc.data, "page", cc.session.CurrentPage)

	cc.session.Screen = ScreenSelect
	b.showSelectMode(cc.chatID, cc.session)
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
	b.showItem(cc.chatID, cc.session)

}

func handleEdit(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("edit callback", "data", cc.data)

	if cc.session.CurrentItem == nil {
		return
	}
	var prompt string
	switch cc.data {
	case "desc":
		prompt = "Введи новое описание:"
		if cc.session.CurrentItem.Description != "" {
			prompt += "\n\nТекущее: " + escapeHTML(sanitizeUTF8(cc.session.CurrentItem.Description))
		}
	case "tags":
		prompt = "Введи теги через запятую или с #:\n<i>Пример: #утро, отдых, #лето</i>"
		if len(cc.session.CurrentItem.Tags) > 0 {
			current := make([]string, len(cc.session.CurrentItem.Tags))
			for i, t := range cc.session.CurrentItem.Tags {
				current[i] = "#" + t
			}
			prompt += "\n\nТекущие: " + escapeHTML(strings.Join(current, " "))
		}
	case "tr":
		prompt = "Введи расшифровку:"
		if cc.session.CurrentItem.Transcript != nil && *cc.session.CurrentItem.Transcript != "" {
			t := sanitizeUTF8(*cc.session.CurrentItem.Transcript)
			if len([]rune(t)) > 200 {
				t = string([]rune(t)[:200]) + "…"
			}
			prompt += "\n\nТекущая: " + escapeHTML(t)
		}
	default:
		slog.Error("callback edit: unknown field", "field", cc.data)
		return
	}
	slog.Info("start field edit", "field", cc.data)
	cc.session.Pending = cc.data
	cc.session.LastMsgID = b.editOrSendHTML(cc.chatID, cc.session.LastMsgID, prompt, cancelKeyboard())
}

func handleFile(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("callback file", "data", cc.data)

	if cc.session.CurrentItem == nil {
		slog.Warn("callback file: no current item")
		return
	}
	slog.Info("send file", "id", cc.session.CurrentItem.ID, "name", cc.session.CurrentItem.FileName)
	b.sendFile(cc.chatID, cc.session.CurrentItem)
}

func handleDelete(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("callback del", "data", cc.data)

	if cc.session.CurrentItem == nil {
		slog.Warn("callback del: no current item")
		return
	}
	ctx = context.Background()
	slog.Info("deleting item", "id", cc.session.CurrentItem.ID)
	if err := b.stash.Delete(ctx, cc.session.CurrentItem.ID); err != nil {
		slog.Error("delete failed", "id", cc.session.CurrentItem.ID, "error", err)
		b.send(cc.chatID, "Ошибка при удалении.")
		return
	}
	slog.Info("item deleted", "id", cc.session.CurrentItem.ID)
	for i, it := range cc.session.Items {
		if it.ID == cc.session.CurrentItem.ID {
			cc.session.Items = append(cc.session.Items[:i], cc.session.Items[i+1:]...)
			break
		}
	}
	cc.session.CurrentItem = nil
	cc.session.Screen = cc.session.Back
	b.send(cc.chatID, "🗑 Удалено.")
	b.sendStoragePage(cc.chatID, cc.session, true)
}

func handleBack(ctx context.Context, b *Bot, cc CallbackContext) {
	slog.Info("callback back", "data", cc.data)

	cc.session.Pending = ""
	slog.Info("back", "from", cc.session.Screen, "to", cc.session.Back)
	switch cc.session.Back {
	case ScreenStorage:
		cc.session.Screen = ScreenStorage
		b.sendStoragePage(cc.chatID, cc.session, false)
	default:
		b.showMainMenu(cc.chatID, cc.session)
	}
}

func handleCancel(ctx context.Context, b *Bot, cc CallbackContext) {

	cc.session.Pending = ""
	slog.Info("cancel pending input")
	if cc.session.Screen == ScreenItem && cc.session.CurrentItem != nil {
		b.showItem(cc.chatID, cc.session)
	} else {
		b.sendStoragePage(cc.chatID, cc.session, false)
	}
}

func (b *Bot) handleCallback(cb *tgbotapi.CallbackQuery) {
	b.api.Send(tgbotapi.NewCallback(cb.ID, ""))

	if cb.From.ID != b.rootID {
		slog.Warn("callback: unauthorized", "user_id", cb.From.ID)
		return
	}

	parts := strings.SplitN(cb.Data, ":", 2)
	action := parts[0]
	payload := ""
	if len(parts) == 2 {
		payload = parts[1]
	}

	chatID := cb.Message.Chat.ID
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
