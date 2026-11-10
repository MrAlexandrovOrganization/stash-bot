// Package tgfmt composes Telegram HTML. Escape dynamic values before wrapping.
package tgfmt

import (
	"html"
	"strings"
)

type HTML string

func (h HTML) String() string { return string(h) }
func Escape(s string) HTML    { return HTML(html.EscapeString(s)) }
func Join(parts ...HTML) HTML {
	var out strings.Builder
	for _, part := range parts {
		out.WriteString(string(part))
	}
	return HTML(out.String())
}
func Bold(h HTML) HTML       { return "<b>" + h + "</b>" }
func Code(h HTML) HTML       { return "<code>" + h + "</code>" }
func Blockquote(h HTML) HTML { return "<blockquote>" + h + "</blockquote>" }
func Link(text HTML, url string) HTML {
	return HTML(`<a href="` + html.EscapeString(url) + `">` + string(text) + `</a>`)
}
