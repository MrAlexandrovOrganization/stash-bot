package bot

import (
	"fmt"

	"stash-bot/internal/stash"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ── Keyboards ─────────────────────────────────────────────────────────────────

func storagePageKeyboard(page, totalPages int) *telego.InlineKeyboardMarkup {
	var navRow []telego.InlineKeyboardButton
	if page > 0 {
		navRow = append(navRow, tu.InlineKeyboardButton("◀").WithCallbackData(fmt.Sprintf("sp:%d", page-1)))
	}
	navRow = append(navRow, tu.InlineKeyboardButton(fmt.Sprintf("%d / %d", page+1, totalPages)).WithCallbackData("noop"))
	if page < totalPages-1 {
		navRow = append(navRow, tu.InlineKeyboardButton("▶").WithCallbackData(fmt.Sprintf("sp:%d", page+1)))
	}

	actionRow := tu.InlineKeyboardRow(
		tu.InlineKeyboardButton("✅ Выбрать").WithCallbackData("ssel"),
		tu.InlineKeyboardButton("🔍 Поиск").WithCallbackData("search"),
		tu.InlineKeyboardButton("🔄").WithCallbackData("refresh"),
		tu.InlineKeyboardButton("🏠 Меню").WithCallbackData("menu"),
	)

	return tu.InlineKeyboard(navRow, actionRow)
}

func itemDetailKeyboard(item *stash.Item) *telego.InlineKeyboardMarkup {
	editRow := []telego.InlineKeyboardButton{
		tu.InlineKeyboardButton("✏️ Описание").WithCallbackData("edit:desc"),
		tu.InlineKeyboardButton("🏷 Теги").WithCallbackData("edit:tags"),
	}
	if item.Type == stash.MediaTypeVideo {
		editRow = append(editRow, tu.InlineKeyboardButton("📄 Расшифровка").WithCallbackData("edit:tr"))
	}

	actionRow := tu.InlineKeyboardRow(
		tu.InlineKeyboardButton("📥 Файл").WithCallbackData("file"),
		tu.InlineKeyboardButton("🗑 Удалить").WithCallbackData("del"),
		tu.InlineKeyboardButton("◀ Назад").WithCallbackData("back"),
	)

	return tu.InlineKeyboard(editRow, actionRow)
}

func cancelKeyboard() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton("Отмена").WithCallbackData("cancel"),
		),
	)
}
