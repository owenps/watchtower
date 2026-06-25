package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/owenps/watchtower/internal/domain"
)

type Source struct{}

func NewSource() Source { return Source{} }

func (s Source) Viewer(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "api", "graphql", "-f", "query=query { viewer { login } }").Output()
	if err != nil {
		return "", fmt.Errorf("gh viewer: %w", err)
	}
	var resp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", err
	}
	if resp.Data.Viewer.Login == "" {
		return "", fmt.Errorf("empty GitHub viewer login")
	}
	return resp.Data.Viewer.Login, nil
}

func (s Source) FetchRepo(ctx context.Context, repo, observer string, includePRReactions bool) ([]domain.RawItem, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("invalid repo %q; want owner/name", repo)
	}

	cmd := exec.CommandContext(ctx, "gh", "api", "graphql",
		"-f", "query="+query,
		"-F", "owner="+owner,
		"-F", "name="+name,
		"-F", fmt.Sprintf("includePRReactions=%t", includePRReactions),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh fetch %s: %w: %s", repo, err, strings.TrimSpace(stderr.String()))
	}

	var resp graphResp
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("github graphql %s: %s", repo, resp.Errors[0].Message)
	}
	if resp.Data.Repository.Name == "" {
		return nil, fmt.Errorf("repo not found: %s", repo)
	}

	items := make([]domain.RawItem, 0, len(resp.Data.Repository.PullRequests.Nodes)+len(resp.Data.Repository.Issues.Nodes))
	for _, pr := range resp.Data.Repository.PullRequests.Nodes {
		items = append(items, normalizePR(repo, observer, pr))
	}
	for _, issue := range resp.Data.Repository.Issues.Nodes {
		items = append(items, normalizeIssue(repo, observer, issue))
	}
	return items, nil
}

func normalizePR(repo, observer string, pr graphPR) domain.RawItem {
	item := domain.RawItem{
		TargetID:       fmt.Sprintf("github.com/%s/pull/%d", repo, pr.Number),
		Repo:           repo,
		Kind:           domain.KindPR,
		Number:         pr.Number,
		Title:          pr.Title,
		URL:            pr.URL,
		State:          pr.State,
		Author:         pr.Author.Login,
		CreatedAt:      parseTime(pr.CreatedAt),
		UpdatedAt:      parseTime(pr.UpdatedAt),
		Draft:          pr.IsDraft,
		Mergeable:      pr.Mergeable == "MERGEABLE",
		Merged:         pr.Merged,
		ReviewDecision: pr.ReviewDecision,
		CheckState:     normalizeCheck(pr.Commits.Nodes),
	}
	item.CheckStateAt = item.UpdatedAt
	if len(pr.Commits.Nodes) > 0 {
		item.LastCommitAt = parseTime(pr.Commits.Nodes[len(pr.Commits.Nodes)-1].Commit.CommittedDate)
		if !item.LastCommitAt.IsZero() {
			item.CheckStateAt = item.LastCommitAt
		}
	}

	var acts []activity
	for _, c := range pr.Comments.Nodes {
		acts = append(acts, activity{At: parseTime(c.CreatedAt), Author: c.Author.Login, Text: summary(c.BodyText), Body: cleanBody(c.BodyText)})
	}
	for _, r := range pr.LatestReviews.Nodes {
		if r.State != "COMMENTED" && r.State != "CHANGES_REQUESTED" && r.State != "APPROVED" {
			continue
		}
		body := cleanBody(r.BodyText)
		text := summary(r.BodyText)
		if text == "" {
			if r.State == "COMMENTED" {
				continue
			}
			text = strings.ToLower(strings.ReplaceAll(r.State, "_", " "))
		}
		acts = append(acts, activity{At: parseTime(r.SubmittedAt), Author: r.Author.Login, Text: text, Body: body})
	}
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved {
			item.UnresolvedThreads++
		}
		for _, c := range t.Comments.Nodes {
			acts = append(acts, activity{At: parseTime(c.CreatedAt), Author: c.Author.Login, Text: summary(c.BodyText), Body: cleanBody(c.BodyText)})
		}
	}
	setLatestDescriptionThumbsUp(&item, pr.Reactions.Nodes, observer)
	setLatestHuman(&item, acts, observer)
	return item
}

func normalizeIssue(repo, observer string, issue graphIssue) domain.RawItem {
	item := domain.RawItem{
		TargetID:  fmt.Sprintf("github.com/%s/issues/%d", repo, issue.Number),
		Repo:      repo,
		Kind:      domain.KindIssue,
		Number:    issue.Number,
		Title:     issue.Title,
		URL:       issue.URL,
		State:     issue.State,
		Author:    issue.Author.Login,
		CreatedAt: parseTime(issue.CreatedAt),
		UpdatedAt: parseTime(issue.UpdatedAt),
	}
	for _, a := range issue.Assignees.Nodes {
		if a.Login == observer {
			item.AssignedToObserver = true
			break
		}
	}
	var acts []activity
	for _, c := range issue.Comments.Nodes {
		acts = append(acts, activity{At: parseTime(c.CreatedAt), Author: c.Author.Login, Text: summary(c.BodyText), Body: cleanBody(c.BodyText)})
	}
	setLatestHuman(&item, acts, observer)
	return item
}

func setLatestDescriptionThumbsUp(item *domain.RawItem, reactions []graphReaction, observer string) {
	for _, reaction := range reactions {
		login := reaction.User.Login
		at := parseTime(reaction.CreatedAt)
		if login == "" || login == observer || at.IsZero() {
			continue
		}
		if at.After(item.PRDescriptionThumbsUpAt) {
			item.PRDescriptionThumbsUpAt = at
			item.PRDescriptionThumbsUpBy = login
		}
	}
}

func setLatestHuman(item *domain.RawItem, acts []activity, observer string) {
	sort.Slice(acts, func(i, j int) bool { return acts[i].At.After(acts[j].At) })
	for _, a := range acts {
		if a.Author == "" || strings.HasSuffix(strings.ToLower(a.Author), "[bot]") {
			continue
		}
		item.LastHumanAt = a.At
		item.LastHumanAuthor = a.Author
		item.LastHumanSummary = a.Text
		item.LastHumanBody = a.Body
		if item.LastHumanBody == "" {
			item.LastHumanBody = a.Text
		}
		return
	}
}

func normalizeCheck(nodes []graphCommitNode) domain.CheckState {
	if len(nodes) == 0 || nodes[len(nodes)-1].Commit.StatusCheckRollup == nil {
		return domain.CheckUnknown
	}
	switch nodes[len(nodes)-1].Commit.StatusCheckRollup.State {
	case "SUCCESS":
		return domain.CheckPass
	case "FAILURE", "ERROR":
		return domain.CheckFail
	case "PENDING", "EXPECTED":
		return domain.CheckPending
	default:
		return domain.CheckUnknown
	}
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func summary(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func cleanBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

type activity struct {
	At     time.Time
	Author string
	Text   string
	Body   string
}

type graphResp struct {
	Data struct {
		Repository struct {
			Name         string `json:"name"`
			PullRequests struct {
				Nodes []graphPR `json:"nodes"`
			} `json:"pullRequests"`
			Issues struct {
				Nodes []graphIssue `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type graphActor struct {
	Login string `json:"login"`
}

type graphPR struct {
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	State          string     `json:"state"`
	IsDraft        bool       `json:"isDraft"`
	Mergeable      string     `json:"mergeable"`
	Merged         bool       `json:"merged"`
	ReviewDecision string     `json:"reviewDecision"`
	CreatedAt      string     `json:"createdAt"`
	UpdatedAt      string     `json:"updatedAt"`
	Author         graphActor `json:"author"`
	Comments       struct {
		Nodes []graphComment `json:"nodes"`
	} `json:"comments"`
	LatestReviews struct {
		Nodes []graphReview `json:"nodes"`
	} `json:"latestReviews"`
	ReviewThreads struct {
		Nodes []graphThread `json:"nodes"`
	} `json:"reviewThreads"`
	Commits struct {
		Nodes []graphCommitNode `json:"nodes"`
	} `json:"commits"`
	Reactions struct {
		Nodes []graphReaction `json:"nodes"`
	} `json:"reactions"`
}

type graphIssue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	URL       string     `json:"url"`
	State     string     `json:"state"`
	CreatedAt string     `json:"createdAt"`
	UpdatedAt string     `json:"updatedAt"`
	Author    graphActor `json:"author"`
	Assignees struct {
		Nodes []graphActor `json:"nodes"`
	} `json:"assignees"`
	Comments struct {
		Nodes []graphComment `json:"nodes"`
	} `json:"comments"`
}

type graphComment struct {
	Author    graphActor `json:"author"`
	CreatedAt string     `json:"createdAt"`
	BodyText  string     `json:"body"`
}

type graphReview struct {
	State       string     `json:"state"`
	SubmittedAt string     `json:"submittedAt"`
	BodyText    string     `json:"body"`
	Author      graphActor `json:"author"`
}

type graphReaction struct {
	CreatedAt string     `json:"createdAt"`
	User      graphActor `json:"user"`
}

type graphThread struct {
	IsResolved bool `json:"isResolved"`
	Comments   struct {
		Nodes []graphComment `json:"nodes"`
	} `json:"comments"`
}

type graphCommitNode struct {
	Commit struct {
		CommittedDate     string `json:"committedDate"`
		StatusCheckRollup *struct {
			State string `json:"state"`
		} `json:"statusCheckRollup"`
	} `json:"commit"`
}

const query = `query($owner: String!, $name: String!, $includePRReactions: Boolean!) {
  repository(owner: $owner, name: $name) {
    name
    pullRequests(states: OPEN, first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes {
        number title url state isDraft mergeable merged reviewDecision createdAt updatedAt
        author { login }
        comments(last: 10) { nodes { author { login } createdAt body } }
        latestReviews(first: 20) { nodes { state submittedAt body author { login } } }
        reviewThreads(first: 50) { nodes { isResolved comments(last: 1) { nodes { author { login } createdAt body } } } }
        commits(last: 1) { nodes { commit { committedDate statusCheckRollup { state } } } }
        reactions(content: THUMBS_UP, last: 20) @include(if: $includePRReactions) { nodes { createdAt user { login } } }
      }
    }
    issues(states: OPEN, first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes {
        number title url state createdAt updatedAt
        author { login }
        assignees(first: 20) { nodes { login } }
        comments(last: 10) { nodes { author { login } createdAt body } }
      }
    }
  }
}`
