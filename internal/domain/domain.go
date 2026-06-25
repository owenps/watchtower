package domain

import "time"

type Kind string

const (
	KindPR    Kind = "pr"
	KindIssue Kind = "issue"
)

type Lane string

const (
	LaneIncoming Lane = "incoming"
	LaneWatching Lane = "watching"
)

type CheckState string

const (
	CheckUnknown CheckState = "unknown"
	CheckPending CheckState = "pending"
	CheckPass    CheckState = "pass"
	CheckFail    CheckState = "fail"
)

type RepoRules struct {
	Name                string
	Enabled             bool
	WatchMyPRs          bool
	WatchMyIssues       bool
	WatchAssignedIssues bool
	WatchReviewPRs      bool
}

type ItemState struct {
	TargetID         string
	Override         string // "", "watch", "unwatch"
	LastSeenActionAt time.Time
}

type RawItem struct {
	TargetID string
	Repo     string
	Kind     Kind
	Number   int
	Title    string
	URL      string
	State    string
	Author   string

	CreatedAt time.Time
	UpdatedAt time.Time

	Draft              bool
	Mergeable          bool
	Merged             bool
	Closed             bool
	ReviewDecision     string
	UnresolvedThreads  int
	CheckState         CheckState
	CheckStateAt       time.Time
	LastCommitAt       time.Time
	LastHumanAt        time.Time
	LastHumanAuthor    string
	LastHumanSummary   string
	LastHumanBody      string
	AssignedToObserver bool
}

type InboxItem struct {
	RawItem
	Lane     Lane
	Reason   string
	ActionAt time.Time
}

func Classify(items []RawItem, states map[string]ItemState, rules map[string]RepoRules, observer string) (incoming []InboxItem, watching []InboxItem, completed []RawItem) {
	for _, item := range items {
		if item.Closed || item.Merged || item.State == "CLOSED" || item.State == "MERGED" {
			completed = append(completed, item)
			continue
		}

		rule, ok := rules[item.Repo]
		if !ok || !rule.Enabled {
			continue
		}

		state := states[item.TargetID]
		if state.Override == "unwatch" {
			continue
		}

		watched := state.Override == "watch" || autoWatched(item, rule, observer)
		if !watched {
			continue
		}

		reason, actionAt := actionable(item, observer)
		if reason != "" && actionAt.After(state.LastSeenActionAt) {
			incoming = append(incoming, InboxItem{RawItem: item, Lane: LaneIncoming, Reason: reason, ActionAt: actionAt})
			continue
		}

		watching = append(watching, InboxItem{RawItem: item, Lane: LaneWatching})
	}

	return incoming, watching, completed
}

func autoWatched(item RawItem, rule RepoRules, observer string) bool {
	switch item.Kind {
	case KindPR:
		if item.Author == observer {
			return rule.WatchMyPRs
		}
		return rule.WatchReviewPRs
	case KindIssue:
		if item.Author == observer && rule.WatchMyIssues {
			return true
		}
		return item.AssignedToObserver && rule.WatchAssignedIssues
	default:
		return false
	}
}

func actionable(item RawItem, observer string) (string, time.Time) {
	if item.Kind == KindPR {
		if item.Draft {
			return "", time.Time{}
		}

		if item.Author == observer {
			if item.LastHumanAuthor != "" && item.LastHumanAuthor != observer {
				return item.LastHumanAuthor + " replied", item.LastHumanAt
			}
			if item.CheckState == CheckFail {
				return "checks failed", firstTime(item.CheckStateAt, item.UpdatedAt)
			}
			if readyToMerge(item) {
				return "ready to merge", latest(item.UpdatedAt, item.CheckStateAt, item.LastHumanAt)
			}
			if !item.Mergeable {
				return "merge conflict", item.UpdatedAt
			}
			return "", time.Time{}
		}

		if readyForReview(item) {
			return "ready for review", latest(item.LastCommitAt, item.LastHumanAt, item.UpdatedAt, item.CheckStateAt)
		}
		return "", time.Time{}
	}

	if item.Kind == KindIssue {
		if item.AssignedToObserver && item.Author != observer {
			return "assigned to you", item.CreatedAt
		}
		if item.LastHumanAuthor != "" && item.LastHumanAuthor != observer {
			return item.LastHumanAuthor + " replied", item.LastHumanAt
		}
	}

	return "", time.Time{}
}

func readyToMerge(item RawItem) bool {
	return item.CheckState == CheckPass && item.Mergeable && item.ReviewDecision == "APPROVED" && item.UnresolvedThreads == 0
}

func readyForReview(item RawItem) bool {
	return !item.Draft && item.CheckState == CheckPass && item.Mergeable
}

func latest(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if t.After(out) {
			out = t
		}
	}
	return out
}

func firstTime(preferred, fallback time.Time) time.Time {
	if !preferred.IsZero() {
		return preferred
	}
	return fallback
}
