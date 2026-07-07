package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/owenps/watchtower/internal/config"
	"github.com/owenps/watchtower/internal/domain"
)

func TestViewKeepsStableFrameWhileMovingSelection(t *testing.T) {
	m := New(config.Config{TerminalBell: boolp(true)}, "", nil, nil, nil)
	m.width = 100
	m.height = 24
	m.loading = false
	m.incoming = []domain.InboxItem{
		testItem(1, strings.Repeat("first very long title ", 8)),
		testItem(2, strings.Repeat("second very long title ", 8)),
		testItem(3, strings.Repeat("third very long title ", 8)),
	}

	for selected := range m.incoming {
		m.selected = selected
		view := m.View()
		assertFrame(t, view, m.width, m.height)
	}
}

func testItem(number int, title string) domain.InboxItem {
	now := time.Now().Add(-time.Duration(number) * time.Minute)
	return domain.InboxItem{
		RawItem: domain.RawItem{
			TargetID:         "github.com/org/repo/pull/" + string(rune('0'+number)),
			Repo:             "org/repo",
			Kind:             domain.KindPR,
			Number:           number,
			Title:            title,
			URL:              "https://github.com/org/repo/pull/1234567890/with/a/really/long/path/that/should/not/wrap",
			UpdatedAt:        now,
			CheckState:       domain.CheckPass,
			Mergeable:        true,
			ReviewDecision:   "APPROVED",
			LastHumanAuthor:  "alice",
			LastHumanSummary: strings.Repeat("long comment ", 30),
		},
		Lane:     domain.LaneIncoming,
		Reason:   "ready for review",
		ActionAt: now,
	}
}

func TestDetailShowsBlockedMergeState(t *testing.T) {
	item := testItem(1, "blocked")
	item.MergeStateStatus = "BLOCKED"

	got := New(config.Config{}, "", nil, nil, nil).detailText(item, false)
	if !strings.Contains(got, "◌ merging blocked") {
		t.Fatalf("detail missing blocked merge state:\n%s", got)
	}
	if strings.Contains(got, "✓ mergeable") || strings.Contains(got, "✓ no merge conflicts") {
		t.Fatalf("detail should not show mergeable when blocked:\n%s", got)
	}
}

func assertFrame(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("height = %d, want %d", len(lines), height)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("line %d width = %d, want %d: %q", i, got, width, line)
		}
	}
}
