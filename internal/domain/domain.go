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
	Name                       string
	Enabled                    bool
	WatchMyPRs                 bool
	WatchMyIssues              bool
	WatchAssignedIssues        bool
	WatchReviewPRs             bool
	WatchPRDescriptionThumbsUp bool
	IgnoredActors              []string
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

	Draft                   bool
	Mergeable               bool
	MergeStateStatus        string
	Merged                  bool
	Closed                  bool
	ReviewDecision          string
	UnresolvedThreads       int
	CheckState              CheckState
	CheckStateAt            time.Time
	LastCommitAt            time.Time
	LastHumanAt             time.Time
	LastHumanAuthor         string
	LastHumanSummary        string
	LastHumanBody           string
	PRDescriptionThumbsUpAt time.Time
	PRDescriptionThumbsUpBy string
	AssignedToObserver      bool
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

		reason, actionAt := actionable(item, observer, rule)
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

func actionable(item RawItem, observer string, rule RepoRules) (string, time.Time) {
	if item.Kind == KindPR {
		if item.Draft {
			return "", time.Time{}
		}

		if item.Author == observer {
			var signals []actionSignal
			if item.LastHumanAuthor != "" && item.LastHumanAuthor != observer {
				signals = append(signals, actionSignal{Reason: item.LastHumanAuthor + " replied", At: item.LastHumanAt})
			}
			if rule.WatchPRDescriptionThumbsUp && item.PRDescriptionThumbsUpBy != "" {
				signals = append(signals, actionSignal{Reason: item.PRDescriptionThumbsUpBy + " thumbs-up on PR", At: item.PRDescriptionThumbsUpAt})
			}
			if item.CheckState == CheckFail {
				signals = append(signals, actionSignal{Reason: "checks failed", At: firstTime(item.CheckStateAt, item.UpdatedAt)})
			}
			if readyToMerge(item) {
				signals = append(signals, actionSignal{Reason: "ready to merge", At: latest(item.UpdatedAt, item.CheckStateAt, item.LastHumanAt)})
			}
			if !item.Mergeable {
				signals = append(signals, actionSignal{Reason: "merge conflict", At: item.UpdatedAt})
			}
			return latestSignal(signals)
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

type actionSignal struct {
	Reason string
	At     time.Time
}

func latestSignal(signals []actionSignal) (string, time.Time) {
	var latest actionSignal
	for _, signal := range signals {
		if signal.Reason == "" || signal.At.IsZero() {
			continue
		}
		if signal.At.After(latest.At) {
			latest = signal
		}
	}
	return latest.Reason, latest.At
}

func readyToMerge(item RawItem) bool {
	return item.CheckState == CheckPass && item.Mergeable && mergeStatePermitsMerge(item.MergeStateStatus) && item.ReviewDecision == "APPROVED" && item.UnresolvedThreads == 0
}

func mergeStatePermitsMerge(status string) bool {
	switch status {
	case "", "CLEAN", "HAS_HOOKS":
		return true
	default:
		return false
	}
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
