package domain

import (
	"testing"
	"time"
)

func TestClassifyPRDescriptionThumbsUpOptIn(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	item := RawItem{
		TargetID:                "github.com/o/r/pull/1",
		Repo:                    "o/r",
		Kind:                    KindPR,
		Number:                  1,
		Author:                  "me",
		UpdatedAt:               now,
		Mergeable:               true,
		PRDescriptionThumbsUpAt: now,
		PRDescriptionThumbsUpBy: "alice",
	}
	rules := map[string]RepoRules{"o/r": {Name: "o/r", Enabled: true, WatchMyPRs: true}}

	incoming, watching, _ := Classify([]RawItem{item}, nil, rules, "me")
	if len(incoming) != 0 || len(watching) != 1 {
		t.Fatalf("off by default: incoming=%d watching=%d", len(incoming), len(watching))
	}

	rule := rules["o/r"]
	rule.WatchPRDescriptionThumbsUp = true
	rules["o/r"] = rule
	incoming, watching, _ = Classify([]RawItem{item}, nil, rules, "me")
	if len(incoming) != 1 || len(watching) != 0 {
		t.Fatalf("opted in: incoming=%d watching=%d", len(incoming), len(watching))
	}
	if incoming[0].Reason != "alice 👍 on PR" || !incoming[0].ActionAt.Equal(now) {
		t.Fatalf("incoming = %#v", incoming[0])
	}
}

func TestClassifyUsesNewerThumbsUpAfterSeenReply(t *testing.T) {
	replyAt := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	thumbAt := replyAt.Add(2 * time.Hour)
	item := RawItem{
		TargetID:                "github.com/o/r/pull/1",
		Repo:                    "o/r",
		Kind:                    KindPR,
		Author:                  "me",
		UpdatedAt:               thumbAt,
		Mergeable:               true,
		LastHumanAt:             replyAt,
		LastHumanAuthor:         "alice",
		PRDescriptionThumbsUpAt: thumbAt,
		PRDescriptionThumbsUpBy: "bot",
	}
	rules := map[string]RepoRules{"o/r": {Name: "o/r", Enabled: true, WatchMyPRs: true, WatchPRDescriptionThumbsUp: true}}
	states := map[string]ItemState{item.TargetID: {TargetID: item.TargetID, LastSeenActionAt: replyAt}}

	incoming, watching, _ := Classify([]RawItem{item}, states, rules, "me")
	if len(incoming) != 1 || len(watching) != 0 {
		t.Fatalf("incoming=%d watching=%d", len(incoming), len(watching))
	}
	if incoming[0].Reason != "bot 👍 on PR" || !incoming[0].ActionAt.Equal(thumbAt) {
		t.Fatalf("incoming = %#v", incoming[0])
	}
}
