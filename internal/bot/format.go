package bot

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"stash-bot/internal/stash"
)

// ── Formatting ────────────────────────────────────────────────────────────────

// itemFields defines the display fields for an item — add/remove here to extend.
var itemFields = []struct {
	label   string
	extract func(*stash.Item) string
}{
	{"📝 Описание", func(it *stash.Item) string { return it.Description }},
	{"🏷 Теги", func(it *stash.Item) string {
		if len(it.Tags) == 0 {
			return ""
		}
		tagged := make([]string, len(it.Tags))
		for i, t := range it.Tags {
			tagged[i] = "#" + t
		}
		return strings.Join(tagged, " ")
	}},
	{"📄 Расшифровка", func(it *stash.Item) string {
		if it.Transcript != nil {
			return *it.Transcript
		}
		return ""
	}},
}

func formatItemDetail(item *stash.Item) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>%s</b>\n\n", mediaIcon(item.Type), escapeHTML(sanitizeUTF8(item.FileName)))

	for _, f := range itemFields {
		val := sanitizeUTF8(f.extract(item))
		if val != "" {
			if len([]rune(val)) > 400 {
				val = string([]rune(val)[:400]) + "…"
			}
			fmt.Fprintf(&sb, "<b>%s:</b> %s\n", f.label, escapeHTML(val))
		} else {
			fmt.Fprintf(&sb, "<b>%s:</b> <i>не задано</i>\n", f.label)
		}
	}

	fmt.Fprintf(&sb, "\n<code>%s</code>", item.ID)
	return sb.String()
}

func mediaIcon(t stash.MediaType) string {
	switch t {
	case stash.MediaTypeImage:
		return "🖼"
	case stash.MediaTypeVideo:
		return "🎬"
	case stash.MediaTypeGIF:
		return "🎞"
	default:
		return "📄"
	}
}

// ── Parse helpers ─────────────────────────────────────────────────────────────

// parseCaption splits a file caption into description and #tags.
func parseCaption(text string) (description string, tags []string) {
	var descParts []string
	for word := range strings.SplitSeq(text, " ") {
		if strings.HasPrefix(word, "#") {
			if tag := strings.TrimPrefix(word, "#"); tag != "" {
				tags = append(tags, tag)
			}
		} else {
			descParts = append(descParts, word)
		}
	}
	return strings.TrimSpace(strings.Join(descParts, " ")), tags
}

// parseSearchQuery splits a search query into text, positive tags (#tag), and negative tags (-#tag).
func parseSearchQuery(text string) (description string, posTags, negTags []string) {
	var descParts []string
	for word := range strings.SplitSeq(text, " ") {
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}
		switch {
		case strings.HasPrefix(word, "-#"):
			if tag := strings.TrimPrefix(word, "-#"); tag != "" {
				negTags = append(negTags, tag)
			}
		case strings.HasPrefix(word, "#"):
			if tag := strings.TrimPrefix(word, "#"); tag != "" {
				posTags = append(posTags, tag)
			}
		default:
			descParts = append(descParts, word)
		}
	}
	return strings.TrimSpace(strings.Join(descParts, " ")), posTags, negTags
}

// parseTags parses comma- or space-separated tags, with or without #.
func parseTags(text string) []string {
	text = strings.ReplaceAll(text, ",", " ")
	var tags []string
	for part := range strings.SplitSeq(text, " ") {
		if t := strings.TrimSpace(strings.TrimPrefix(part, "#")); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func hasAnyTag(itemTags, checkTags []string) bool {
	for _, ct := range checkTags {
		ctLow := strings.ToLower(ct)
		for _, it := range itemTags {
			if strings.ToLower(it) == ctLow {
				return true
			}
		}
	}
	return false
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// sanitizeUTF8 replaces invalid UTF-8 sequences with "?" to prevent Telegram API errors.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "?")
}
