package github

import (
	"testing"
	"time"
)

func TestNormalizePRTracksLatestDescriptionThumbsUpFromSomeoneElse(t *testing.T) {
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	var pr graphPR
	pr.Number = 1
	pr.Title = "test"
	pr.URL = "https://github.com/o/r/pull/1"
	pr.State = "OPEN"
	pr.Mergeable = "MERGEABLE"
	pr.Author.Login = "me"
	pr.CreatedAt = base.Add(-time.Hour).Format(time.RFC3339)
	pr.UpdatedAt = base.Format(time.RFC3339)
	pr.Reactions.Nodes = []graphReaction{
		{CreatedAt: base.Add(time.Minute).Format(time.RFC3339), User: graphActor{Login: "me"}},
		{CreatedAt: base.Add(2 * time.Minute).Format(time.RFC3339), User: graphActor{Login: "alice"}},
		{CreatedAt: base.Add(3 * time.Minute).Format(time.RFC3339), User: graphActor{Login: "bot"}},
	}

	item := normalizePR("o/r", "me", pr)
	wantAt := base.Add(3 * time.Minute)
	if item.PRDescriptionThumbsUpBy != "bot" || !item.PRDescriptionThumbsUpAt.Equal(wantAt) {
		t.Fatalf("thumbs-up = %q %s, want bot %s", item.PRDescriptionThumbsUpBy, item.PRDescriptionThumbsUpAt, wantAt)
	}
}
