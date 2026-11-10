package resources

import (
	"strings"
	"testing"
)

func TestPageEscapesUserQueryAndLabels(t *testing.T) {
	header := PageHeader(Page{Kind: "search", Query: `<script>&"`, Number: 101, Pages: 113, Total: 1128})
	if strings.Contains(header, "<script>") || !strings.Contains(header, "&lt;script&gt;&amp;") || !strings.Contains(header, "101 из 113") {
		t.Fatal(header)
	}
	if got := PageItem("🖼", 1001, "<tag>&"); got != "🖼 1001. &lt;tag&gt;&amp;\n" {
		t.Fatal(got)
	}
}
