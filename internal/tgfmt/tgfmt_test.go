package tgfmt

import "testing"

func TestDynamicHTMLIsEscapedBeforeComposition(t *testing.T) {
	got := Join(Bold(Escape(`<script>&"`)), Code(Escape("<x>")), Blockquote(Escape("&")), Link(Escape("<link>"), `https://example.test/?a="&b=1`)).String()
	want := `<b>&lt;script&gt;&amp;&#34;</b><code>&lt;x&gt;</code><blockquote>&amp;</blockquote><a href="https://example.test/?a=&#34;&amp;b=1">&lt;link&gt;</a>`
	if got != want {
		t.Fatalf("HTML = %s", got)
	}
}
