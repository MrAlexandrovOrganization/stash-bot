package bot

import (
	"fmt"

	"stash-bot/internal/stash"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Keyboards ─────────────────────────────────────────────────────────────────

func storagePageKeyboard(page, totalPages int) tgbotapi.InlineKeyboardMarkup {
	var navRow []tgbotapi.InlineKeyboardButton
	if page > 0 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("◀", fmt.Sprintf("sp:%d", page-1)))
	}
	navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(
		fmt.Sprintf("%d / %d", page+1, totalPages), "noop",
	))
	if page < totalPages-1 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("▶", fmt.Sprintf("sp:%d", page+1)))
	}

	actionRow := tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Выбрать", "ssel"),
		tgbotapi.NewInlineKeyboardButtonData("🔍 Поиск", "search"),
		tgbotapi.NewInlineKeyboardButtonData("🏠 Меню", "menu"),
	)

	return tgbotapi.NewInlineKeyboardMarkup(navRow, actionRow)
}

func itemDetailKeyboard(item *stash.Item) tgbotapi.InlineKeyboardMarkup {
	editRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("✏️ Описание", "edit:desc"),
		tgbotapi.NewInlineKeyboardButtonData("🏷 Теги", "edit:tags"),
	}
	if item.Type == stash.MediaTypeVideo {
		editRow = append(editRow, tgbotapi.NewInlineKeyboardButtonData("📄 Расшифровка", "edit:tr"))
	}

	actionRow := tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("📥 Файл", "file"),
		tgbotapi.NewInlineKeyboardButtonData("🗑 Удалить", "del"),
		tgbotapi.NewInlineKeyboardButtonData("◀ Назад", "back"),
	)

	return tgbotapi.NewInlineKeyboardMarkup(editRow, actionRow)
}

func cancelKeyboard() *tgbotapi.InlineKeyboardMarkup {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Отмена", "cancel"),
		),
	)
	return &kb
}
