package resources

import (
	"fmt"
	"stash-bot/internal/tgfmt"
)

const (
	Loading             = "⏳ Загружаю…"
	SearchError         = "Не удалось выполнить поиск. Попробуй ещё раз."
	StorageError        = "Не удалось загрузить хранилище. Попробуй ещё раз."
	SimilarError        = "Поиск похожих временно недоступен. Обычный поиск по словам и тегам работает отдельно."
	IndexPending        = "Этот файл ещё обрабатывается. Поиск похожих станет доступен после индексации."
	EmptyStorage        = "📦 Хранилище пусто. Отправь файл, чтобы сохранить его."
	EmptySearch         = "Совпадений не найдено. Попробуй другие слова или теги."
	EmptySimilar        = "Похожих файлов не найдено. Попробуй другое описание или картинку."
	FallbackUnavailable = "По словам совпадений нет. Поиск по смыслу сейчас недоступен — попробуй позже или измени запрос."
	SearchPrompt        = "🔍 Введи слова или описание.\n\nМожно добавить #тег или -#исключить. Сначала ищу совпадения по словам, затем похожие по смыслу."
	SimilarPrompt       = "✨ Опиши, что хочешь найти:"
	ImagePrompt         = "🖼 Отправь картинку для поиска похожих файлов:"
	Menu                = "👋 Привет! Я твоё личное хранилище медиа.\n\nОтправь файл, чтобы сохранить его.\nНапиши слова или описание, чтобы найти."
	StorageButton       = "📦 Хранилище"
	SearchButton        = "🔍 Поиск"
	MenuButton          = "🏠 Меню"
	RetryButton         = "🔄 Повторить"
	StaleButton         = "Эта кнопка устарела. Используй последнее меню или /start."
)

type Page struct {
	Kind                 string
	Query                string
	Number, Pages, Total int
}

func PageHeader(p Page) string {
	title := "📦 Хранилище"
	if p.Kind == "search" {
		title = "🔍 Результаты поиска"
	}
	if p.Kind == "similar" {
		title = "✨ Похожие по смыслу или изображению"
	}
	parts := []tgfmt.HTML{tgfmt.Bold(tgfmt.Escape(title)), tgfmt.Escape(fmt.Sprintf("\nСтраница %d из %d · %d файлов\n", p.Number, p.Pages, p.Total))}
	if p.Query != "" {
		parts = append(parts, tgfmt.Escape("Запрос: "), tgfmt.Code(tgfmt.Escape(p.Query)), tgfmt.Escape("\n"))
	}
	parts = append(parts, tgfmt.Escape("\n"))
	return tgfmt.Join(parts...).String()
}
func PageItem(icon string, number int, label string) string {
	return tgfmt.Escape(fmt.Sprintf("%s %d. %s\n", icon, number, label)).String()
}
