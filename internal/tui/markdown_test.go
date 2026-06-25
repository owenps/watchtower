package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestCodeBlockQuoteGutterAndHighlightDoesNotLeakMarker(t *testing.T) {
	content := strings.Join(activityQuoteLines("before\n```sql\nselect * from table\n```\nafter"), "\n")
	got := renderBoxWithFooter(50, 8, content, "", 0)
	if strings.Contains(got, codeLinePrefix) {
		t.Fatalf("leaked code marker:\n%s", got)
	}
	if !strings.Contains(got, "│ │ select * from table") {
		t.Fatalf("missing quote gutter/code line:\n%s", got)
	}
	for i, line := range strings.Split(got, "\n") {
		if width := lipgloss.Width(line); width != 50 {
			t.Fatalf("line %d width=%d want=50: %q", i, width, line)
		}
	}
}

func TestInlineMarkdownDetailsAndImages(t *testing.T) {
	content := strings.Join(activityQuoteLines("**bold** and *italic* and `code`\n<details>\nsecret\n</details>\n![alt](https://example.com/a.png)\n<img src=\"x\">\nafter"), "\n")
	if strings.Contains(content, "**") || strings.Contains(content, "*italic*") {
		t.Fatalf("markdown markers were not removed:\n%s", content)
	}
	if strings.Contains(content, "secret") || strings.Contains(content, "details") {
		t.Fatalf("details were not removed:\n%s", content)
	}
	if strings.Contains(content, "example.com") || strings.Contains(content, "img src") || strings.Contains(content, "alt") {
		t.Fatalf("images were not removed:\n%s", content)
	}
	if !strings.Contains(content, "after") {
		t.Fatalf("content after details/images missing:\n%s", content)
	}
}
