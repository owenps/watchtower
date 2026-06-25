package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/owenps/watchtower/internal/domain"
)

func TestBoxHeights(t *testing.T) {
	content := strings.Repeat("long title line that should be ellipsized and never wrap ", 4) + "\nurl\nwhy\nstatus"
	footer := detailActions(domain.InboxItem{Lane: domain.LaneIncoming})
	for h := 3; h < 20; h++ {
		got := renderBoxWithFooter(40, h, content, footer, 0)
		if lipgloss.Height(got) != h {
			t.Fatalf("renderBoxWithFooter height %d got %d\n%s", h, lipgloss.Height(got), got)
		}
	}
}
