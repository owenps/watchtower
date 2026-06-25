package actions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/owenps/watchtower/internal/config"
	"github.com/owenps/watchtower/internal/domain"
)

type Result struct {
	Action string
	Output string
	Err    error
}

func Applicable(actions []config.ActionConfig, item domain.InboxItem) []config.ActionConfig {
	out := []config.ActionConfig{}
	for _, action := range actions {
		if len(action.AppliesTo) == 0 || contains(action.AppliesTo, string(item.Kind)) {
			out = append(out, action)
		}
	}
	return out
}

func Run(ctx context.Context, action config.ActionConfig, item domain.InboxItem) Result {
	dir, err := os.MkdirTemp("", "watchtower-action-*")
	if err != nil {
		return Result{Action: action.Name, Err: err}
	}
	defer os.RemoveAll(dir)

	promptPath := filepath.Join(dir, "prompt.md")
	contextPath := filepath.Join(dir, "context.md")
	if err := os.WriteFile(promptPath, []byte(action.Prompt), 0o600); err != nil {
		return Result{Action: action.Name, Err: err}
	}
	if err := os.WriteFile(contextPath, []byte(Context(item)), 0o600); err != nil {
		return Result{Action: action.Name, Err: err}
	}

	cmdline := render(action.Command, map[string]string{
		"prompt_file":  promptPath,
		"context_file": contextPath,
		"url":          item.URL,
		"repo":         item.Repo,
		"number":       fmt.Sprintf("%d", item.Number),
		"kind":         string(item.Kind),
		"title":        item.Title,
	})

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	out, err := cmd.CombinedOutput()
	return Result{Action: action.Name, Output: string(out), Err: err}
}

func Context(item domain.InboxItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s %s #%d: %s\n\n", item.Repo, strings.ToUpper(string(item.Kind)), item.Number, item.Title)
	fmt.Fprintf(&b, "URL: %s\n", item.URL)
	if item.Reason != "" {
		fmt.Fprintf(&b, "Why attention: %s\n", item.Reason)
	}
	if !item.ActionAt.IsZero() {
		fmt.Fprintf(&b, "Actionable at: %s\n", item.ActionAt.Format(time.RFC3339))
	}
	if item.LastHumanAuthor != "" {
		fmt.Fprintf(&b, "Latest human activity: %s", item.LastHumanAuthor)
		if !item.LastHumanAt.IsZero() {
			fmt.Fprintf(&b, " at %s", item.LastHumanAt.Format(time.RFC3339))
		}
		fmt.Fprintf(&b, "\n%s\n", item.LastHumanSummary)
	}
	fmt.Fprintf(&b, "\nStatus:\n")
	if item.Kind == domain.KindPR {
		fmt.Fprintf(&b, "- Draft: %t\n- Checks: %s\n- Mergeable: %t\n- Review decision: %s\n- Unresolved threads: %d\n", item.Draft, item.CheckState, item.Mergeable, item.ReviewDecision, item.UnresolvedThreads)
	}
	fmt.Fprintf(&b, "- Author: %s\n- Updated: %s\n", item.Author, item.UpdatedAt.Format(time.RFC3339))
	return b.String()
}

func render(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", shellQuote(v))
	}
	return s
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
