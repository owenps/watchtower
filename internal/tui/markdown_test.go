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
