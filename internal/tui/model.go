package tui

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/owenps/watchtower/internal/actions"
	"github.com/owenps/watchtower/internal/config"
	"github.com/owenps/watchtower/internal/domain"
	gh "github.com/owenps/watchtower/internal/github"
	"github.com/owenps/watchtower/internal/store"
)

type Model struct {
	cfg      config.Config
	cfgPath  string
	rules    map[string]domain.RepoRules
	store    *store.Store
	source   gh.Source
	logger   *log.Logger
	observer string

	incoming []domain.InboxItem
	watching []domain.InboxItem
	view     domain.Lane
	selected int

	width  int
	height int

	loading      bool
	spinnerFrame int
	status       string
	err          error
	filter       string
	mode         mode

	pending               map[string]pending
	toasts                []toast
	actions               []config.ActionConfig
	actIndex              int
	settingsTab           int
	settingsIndex         int
	settingsOffset        int
	detailScroll          int
	repoInput             string
	repoInputFromSettings bool
	removeRepoIndex       int
	output                string
	confirm               *config.ActionConfig
	initial               bool
}

type mode int

const (
	modeNormal mode = iota
	modeFilter
	modeActions
	modeConfirm
	modeFullDetail
	modeSettings
	modeRepoInput
	modeRemoveRepoConfirm
)

type pending struct {
	kind string
	item domain.InboxItem
}

type toast struct {
	msg       string
	expiresAt time.Time
}

type fetchMsg struct {
	observer  string
	incoming  []domain.InboxItem
	watching  []domain.InboxItem
	completed []domain.RawItem
	err       error
}

type finalizeMsg struct {
	targetID string
	kind     string
}

type actionMsg actions.Result

type tickMsg struct{}

type toastMsg struct{}

type spinnerMsg struct{}

type statusClearMsg struct{}

type settingsSavedMsg struct {
	rules map[string]domain.RepoRules
	err   error
}

type settingEntry struct {
	text       string
	selectable bool
	kind       string
	repoIndex  int
	field      int
}

func New(cfg config.Config, cfgPath string, rules map[string]domain.RepoRules, st *store.Store, logger *log.Logger) Model {
	m := Model{
		cfg:     cfg,
		cfgPath: cfgPath,
		rules:   rules,
		store:   st,
		source:  gh.NewSource(),
		logger:  logger,
		view:    domain.LaneIncoming,
		loading: true,
		pending: map[string]pending{},
		initial: true,
	}
	if len(cfg.Repos) == 0 {
		m.mode = modeRepoInput
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(setTitle(), m.fetch(), m.tick(), m.spinnerTick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case fetchMsg:
		m.loading = false
		m.err = msg.err
		if msg.err != nil {
			m.status = msg.err.Error()
			m.logf("refresh error: %v", msg.err)
			return m, nil
		}
		oldIncoming := len(m.incoming)
		m.observer = msg.observer
		m.incoming = msg.incoming
		m.watching = msg.watching
		m.sortItems()
		m.clamp()
		m.status = fmt.Sprintf("refreshed %s", time.Now().Format("15:04"))
		cmds := []tea.Cmd{}
		for _, item := range msg.completed {
			m.toasts = append(m.toasts, toast{msg: fmt.Sprintf("Completed: %s #%d %s", item.Repo, item.Number, item.State), expiresAt: time.Now().Add(5 * time.Second)})
		}
		if len(msg.completed) > 0 {
			cmds = append(cmds, m.toastTick())
		}
		if !m.initial && len(m.incoming) > oldIncoming && m.cfg.TerminalBell != nil && *m.cfg.TerminalBell {
			cmds = append(cmds, newIncomingSoundCmd())
		}
		m.initial = false
		return m, tea.Batch(cmds...)
	case tickMsg:
		if m.cfg.RefreshDuration() < time.Minute {
			return m, nil
		}
		m.loading = true
		return m, tea.Batch(m.fetch(), m.tick(), m.spinnerTick())
	case spinnerMsg:
		if !m.loading {
			return m, nil
		}
		m.spinnerFrame++
		return m, m.spinnerTick()
	case toastMsg:
		m.expireToasts()
		if len(m.toasts) > 0 {
			return m, m.toastTick()
		}
		return m, nil
	case settingsSavedMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		m.rules = msg.rules
		m.status = "✓"
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return statusClearMsg{} })
	case statusClearMsg:
		if m.status == "✓" || m.status == "sound test" {
			m.status = ""
		}
		return m, nil
	case finalizeMsg:
		p, ok := m.pending[msg.targetID]
		if !ok || p.kind != msg.kind {
			return m, nil
		}
		delete(m.pending, msg.targetID)
		if msg.kind == "seen" {
			if err := m.store.MarkSeen(context.Background(), msg.targetID, p.item.ActionAt); err != nil {
				m.status = err.Error()
			} else {
				m.removeCurrent(msg.targetID)
			}
		} else if msg.kind == "unwatch" {
			if err := m.store.SetOverride(context.Background(), msg.targetID, "unwatch"); err != nil {
				m.status = err.Error()
			} else {
				m.removeCurrent(msg.targetID)
			}
		}
		m.clamp()
		return m, nil
	case actionMsg:
		m.output = fmt.Sprintf("$ %s\n", msg.Action)
		if msg.Output != "" {
			m.output += msg.Output
		}
		if msg.Err != nil {
			m.output += "\nerror: " + msg.Err.Error()
		}
		m.mode = modeNormal
		m.status = "action finished"
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.mode == modeFilter {
		switch key {
		case "esc", "enter":
			m.mode = modeNormal
		case "backspace", "ctrl+h":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
			}
		default:
			if len(key) == 1 {
				m.filter += key
			}
		}
		m.clamp()
		return m, nil
	}
	if m.mode == modeActions {
		switch key {
		case "esc":
			m.mode = modeNormal
		case "up", "k":
			if m.actIndex > 0 {
				m.actIndex--
			}
		case "down", "j":
			if m.actIndex < len(m.actions)-1 {
				m.actIndex++
			}
		case "enter":
			if len(m.actions) == 0 {
				return m, nil
			}
			a := m.actions[m.actIndex]
			if a.Risk == "write" {
				m.confirm = &a
				m.mode = modeConfirm
				return m, nil
			}
			m.mode = modeNormal
			m.status = "running action..."
			return m, m.runAction(a)
		}
		return m, nil
	}
	if m.mode == modeConfirm {
		switch key {
		case "y", "Y":
			a := *m.confirm
			m.confirm = nil
			m.mode = modeNormal
			m.status = "running action..."
			return m, m.runAction(a)
		case "n", "N", "esc":
			m.confirm = nil
			m.mode = modeNormal
		}
		return m, nil
	}
	if m.mode == modeRemoveRepoConfirm {
		switch key {
		case "y", "Y":
			return m.removeRepo(m.removeRepoIndex)
		case "n", "N", "esc":
			m.mode = modeSettings
		}
		return m, nil
	}
	if m.mode == modeFullDetail {
		switch key {
		case "esc", "enter":
			m.mode = modeNormal
		case "up", "k":
			if m.detailScroll > 0 {
				m.detailScroll--
			}
		case "down", "j":
			m.detailScroll++
		case "o":
			if item := m.current(); item != nil {
				return m, openURL(item.URL)
			}
		case "s":
			if item := m.current(); item != nil && m.view == domain.LaneIncoming {
				return m.togglePending("seen", *item)
			}
		case "u":
			if item := m.current(); item != nil {
				return m.togglePending("unwatch", *item)
			}
		case "a":
			if item := m.current(); item != nil {
				m.actions = actions.Applicable(m.cfg.Actions, *item)
				m.actIndex = 0
				m.mode = modeActions
			}
		case "r":
			m.loading = true
			return m, tea.Batch(m.fetch(), m.spinnerTick())
		}
		return m, nil
	}
	if m.mode == modeSettings {
		switch key {
		case "esc", "?":
			m.mode = modeNormal
		case "tab":
			m.nextSettingsTab()
		case "up", "k":
			m.moveSettings(-1)
		case "down", "j":
			m.moveSettings(1)
		case "right", "l", "+", "=", "]":
			return m, m.changeSelectedSetting(1)
		case "left", "h", "-", "[":
			return m, m.changeSelectedSetting(-1)
		case "enter", " ":
			return m, m.changeSelectedSetting(1)
		}
		return m, nil
	}
	if m.mode == modeRepoInput {
		switch key {
		case "esc":
			if len(m.cfg.Repos) == 0 && !m.repoInputFromSettings {
				return m, tea.Quit
			}
			m.repoInputFromSettings = false
			m.mode = modeSettings
		case "enter":
			return m.addRepoFromInput()
		case "backspace", "ctrl+h":
			if len(m.repoInput) > 0 {
				m.repoInput = m.repoInput[:len(m.repoInput)-1]
			}
		default:
			if len(key) == 1 {
				m.repoInput += key
			}
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		if m.view == domain.LaneIncoming {
			m.view = domain.LaneWatching
		} else {
			m.view = domain.LaneIncoming
		}
		m.selected = 0
	case "up", "k":
		if m.selected > 0 {
			m.selected--
			m.detailScroll = 0
		}
	case "down", "j":
		if m.selected < len(m.visible())-1 {
			m.selected++
			m.detailScroll = 0
		}
	case "r":
		m.loading = true
		return m, tea.Batch(m.fetch(), m.spinnerTick())
	case "/":
		m.mode = modeFilter
	case "?":
		m.mode = modeSettings
		m.clampSettingsTab()
		m.settingsIndex = 0
		m.settingsOffset = 0
		m.ensureSettingsSelection(1)
	case "enter":
		if m.current() != nil {
			m.mode = modeFullDetail
			m.detailScroll = 0
		}
	case "o":
		if item := m.current(); item != nil {
			return m, openURL(item.URL)
		}
	case "s":
		if item := m.current(); item != nil && m.view == domain.LaneIncoming {
			return m.togglePending("seen", *item)
		}
	case "u":
		if item := m.current(); item != nil {
			return m.togglePending("unwatch", *item)
		}
	case "a":
		if item := m.current(); item != nil {
			m.actions = actions.Applicable(m.cfg.Actions, *item)
			m.actIndex = 0
			m.mode = modeActions
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "♜\n"
	}
	if m.mode == modeFullDetail {
		return m.detail(true)
	}
	if m.mode == modeSettings {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModal())
	}
	if m.mode == modeRepoInput {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.repoInputModal())
	}
	if m.mode == modeRemoveRepoConfirm {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.removeRepoConfirmModal())
	}

	header := m.topBar()

	footer := m.footer()
	if m.mode == modeFilter {
		footer = m.searchInput()
	}
	if m.mode == modeActions {
		footer = m.actionPalette()
	}
	if m.mode == modeConfirm {
		footer = confirmStyle.Render("Run write action? y/n")
	}
	if m.output != "" {
		footer += "\n" + outputStyle.Width(m.width-2).MaxHeight(6).Render(m.output)
	}
	footerH := max(1, lipgloss.Height(footer))
	bodyH := max(3, m.height-1-footerH)

	leftW := max(40, m.width/2)
	rightW := max(20, m.width-leftW-2)
	left := fitBlock(m.list(leftW, bodyH), leftW, bodyH)
	right := fitBlock(m.preview(rightW, bodyH), rightW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)

	return fitFrame(header+"\n"+body+"\n"+footer, m.width, m.height)
}

func (m Model) topBar() string {
	incoming := fmt.Sprintf("Incoming %d", len(m.incoming))
	watching := fmt.Sprintf("Watching %d", len(m.watching))
	if m.view == domain.LaneIncoming {
		return headerStyle.Render("♜  ["+incoming+"]") + mutedStyle.Render("  "+watching)
	}
	return headerStyle.Render("♜  ") + mutedStyle.Render(incoming+"  ") + headerStyle.Render("["+watching+"]")
}

func (m Model) list(w, h int) string {
	items := m.visible()
	lines := []string{}
	if len(items) == 0 {
		if m.loading {
			lines = append(lines, mutedStyle.Render("Loading items..."))
		} else {
			lines = append(lines, mutedStyle.Render("No items"))
		}
	}
	start := 0
	if m.selected >= h {
		start = m.selected - h + 1
	}
	end := min(len(items), start+h)
	for i := start; i < end; i++ {
		item := items[i]
		cursor := " "
		if i == m.selected {
			cursor = ">"
		}
		kind := "I"
		if item.Kind == domain.KindPR {
			kind = "PR"
		}
		repo := shortRepo(item.Repo)
		age := relativeTime(item.UpdatedAt)
		if item.Lane == domain.LaneIncoming && !item.ActionAt.IsZero() {
			age = relativeTime(item.ActionAt)
		}
		icon := " "
		if item.Lane == domain.LaneIncoming {
			icon = reasonIcon(item.Reason)
			if i != m.selected {
				icon = reasonIconStyle(item.Reason).Render(icon)
			}
		}
		text := fmt.Sprintf("%s %s %-12s %-2s #%-5d (%s) %s", cursor, icon, repo, kind, item.Number, age, item.Title)
		text = fitLine(text, w)
		if _, ok := m.pending[item.TargetID]; ok {
			text = dimStyle.Render(text)
		} else if i == m.selected {
			text = selectedStyle.Render(text)
		} else if item.Lane == domain.LaneWatching {
			text = dimStyle.Render(text)
		}
		lines = append(lines, text)
	}
	return lipgloss.NewStyle().Width(w).Height(h).Render(strings.Join(lines, "\n"))
}

func (m Model) preview(w, h int) string {
	if item := m.current(); item != nil {
		return renderBoxWithFooter(w, h, m.detailText(*item, false), detailActions(*item), 0)
	}
	return renderBox(w, h, "No selection")
}

func (m Model) detail(full bool) string {
	item := m.current()
	if item == nil {
		return "No selection"
	}
	content := m.detailText(*item, true)
	if full {
		return renderTitledBoxWithFooter(max(40, m.width-2), max(8, m.height-1), "", content, detailFooter(*item), m.detailScroll)
	}
	return content
}

func (m Model) detailText(item domain.InboxItem, full bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s #%d %s\n", strings.ToUpper(string(item.Kind)), item.Number, item.Title)
	fmt.Fprintf(&b, "%s\n\n", mutedStyle.Render(item.URL))
	if item.Lane == domain.LaneIncoming {
		fmt.Fprintf(&b, "%s\n", headerStyle.Render("Why"))
		fmt.Fprintf(&b, "%s %s\n", reasonIcon(item.Reason), item.Reason)
		if item.LastHumanAuthor == "" && !item.ActionAt.IsZero() {
			fmt.Fprintf(&b, "◷ %s\n", mutedStyle.Render(displayTime(item.ActionAt)))
		}
		fmt.Fprintln(&b)
	}
	if item.LastHumanAuthor != "" {
		fmt.Fprintf(&b, "%s\n", headerStyle.Render("Activity"))
		byline := item.LastHumanAuthor
		if !item.LastHumanAt.IsZero() {
			byline += " · " + displayTime(item.LastHumanAt)
		}
		fmt.Fprintf(&b, "%s\n", mutedStyle.Render(byline))
		body := item.LastHumanBody
		if body == "" {
			body = item.LastHumanSummary
		}
		for _, line := range activityQuoteLines(body) {
			fmt.Fprintln(&b, line)
		}
		fmt.Fprintln(&b)
	}
	if item.Kind == domain.KindPR {
		fmt.Fprintf(&b, "%s\n", headerStyle.Render("Status"))
		fmt.Fprintf(&b, "%s\n", checkStatusLine(item.CheckState))
		fmt.Fprintf(&b, "%s\n", mergeStatusLine(item))
		if item.ReviewDecision != "" {
			fmt.Fprintf(&b, "%s\n", reviewStatusLine(item.ReviewDecision))
		}
		fmt.Fprintf(&b, "◌ unresolved %d\n\n", item.UnresolvedThreads)
	}
	return b.String()
}

func (m Model) footer() string {
	parts := []string{"tab switch", "j/k move", "enter detail", "/ search", "? settings", "q quit"}
	if m.filter != "" {
		parts = append(parts, "filter: "+m.filter)
	}
	if m.loading {
		parts = append(parts, headerStyle.Render(m.spinner())+" loading")
	}
	if m.status != "" {
		parts = append(parts, m.status)
	}
	for _, t := range m.toasts {
		parts = append(parts, t.msg)
	}
	return mutedStyle.Render(strings.Join(parts, " · "))
}

func (m Model) searchInput() string {
	return renderTitledBox(m.width, 3, "search", "> "+m.filter)
}

func (m Model) actionPalette() string {
	if len(m.actions) == 0 {
		return boxStyle.Render("No actions configured for item. esc close")
	}
	lines := []string{"Actions:"}
	for i, a := range m.actions {
		prefix := " "
		if i == m.actIndex {
			prefix = ">"
		}
		lines = append(lines, fmt.Sprintf("%s %s [%s]", prefix, a.Label, a.Risk))
	}
	return boxStyle.Render(strings.Join(lines, "\n"))
}

func detailActions(item domain.InboxItem) string {
	return mutedStyle.Render(strings.Join(detailActionParts(item), " · "))
}

func detailFooter(item domain.InboxItem) string {
	parts := append(detailActionParts(item), "esc/enter close")
	return mutedStyle.Render(strings.Join(parts, " · "))
}

func detailActionParts(item domain.InboxItem) []string {
	parts := []string{"u unwatch", "a actions", "o open", "r refresh"}
	if item.Lane == domain.LaneIncoming {
		parts = append([]string{"s seen"}, parts...)
	}
	return parts
}

func (m Model) settingsModal() string {
	width := m.settingsModalWidth()
	entries := m.settingsEntries(width - 4)
	visible := m.visibleSettingsRows()
	start := m.settingsOffset
	end := min(len(entries), start+visible)
	shown := []string{}
	for i := start; i < end; i++ {
		entry := entries[i]
		row := entry.text
		cursor := " "
		if entry.selectable && i == m.settingsIndex {
			cursor = ">"
			row = selectedStyle.Render(row)
		}
		shown = append(shown, cursor+" "+row)
	}
	footer := "tab switch · j/k select · ←/→ change · enter select · ?/esc close"
	if len(entries) > visible {
		footer = fmt.Sprintf("%d/%d · %s", m.settingsIndex+1, len(entries), footer)
	}
	if m.status != "" {
		footer += " · " + m.status
	}
	lines := []string{m.settingsTabs(width - 4), "", strings.Join(shown, "\n"), "", footer}
	content := strings.Join(lines, "\n")
	return renderTitledBox(width, len(strings.Split(content, "\n"))+2, "settings", content)
}

func (m Model) settingsModalWidth() int {
	return min(78, max(44, m.width-4))
}

func (m Model) settingsTabs(rowWidth int) string {
	tab := m.clampedSettingsTab()
	labels := []string{"General"}
	for _, repo := range m.cfg.Repos {
		labels = append(labels, shortRepo(repo.Name))
	}
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		if i == tab {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return ansi.Truncate(strings.Join(parts, "  "), rowWidth, "…")
}

func (m Model) settingsEntries(rowWidth int) []settingEntry {
	if m.clampedSettingsTab() == 0 {
		entries := []settingEntry{
			{text: settingRow("refresh interval", m.refreshLabel(), rowWidth), selectable: true, kind: "refresh"},
			{text: settingRow("sound for new incoming (macOS)", onOff(m.cfg.TerminalBell != nil && *m.cfg.TerminalBell), rowWidth), selectable: true, kind: "bell"},
		}
		if len(m.cfg.Repos) == 0 {
			entries = append(entries, settingEntry{text: mutedStyle.Render("no repos configured")})
		}
		entries = append(entries, settingEntry{text: "+ add repo", selectable: true, kind: "add"})
		return entries
	}

	repoIndex := m.clampedSettingsTab() - 1
	repo := m.cfg.Repos[repoIndex]
	return []settingEntry{
		{text: settingRow("enabled", onOff(value(repo.Enabled)), rowWidth), selectable: true, kind: "repo", repoIndex: repoIndex, field: 0},
		{text: settingRow("watch PRs I opened", onOff(value(repo.WatchMyPRs)), rowWidth), selectable: true, kind: "repo", repoIndex: repoIndex, field: 1},
		{text: settingRow("watch issues I opened", onOff(value(repo.WatchMyIssues)), rowWidth), selectable: true, kind: "repo", repoIndex: repoIndex, field: 2},
		{text: settingRow("watch issues assigned to me", onOff(value(repo.WatchAssignedIssues)), rowWidth), selectable: true, kind: "repo", repoIndex: repoIndex, field: 3},
		{text: settingRow("watch PRs ready for my review", onOff(value(repo.WatchReviewPRs)), rowWidth), selectable: true, kind: "repo", repoIndex: repoIndex, field: 4},
		{text: settingRow("watch thumbs-up on my PR description", onOff(value(repo.WatchPRDescriptionThumbsUp)), rowWidth), selectable: true, kind: "repo", repoIndex: repoIndex, field: 5},
		{text: ""},
		{text: "remove repo", selectable: true, kind: "remove", repoIndex: repoIndex},
	}
}

func (m Model) settingsCount() int { return len(m.settingsEntries(m.settingsModalWidth() - 4)) }

func (m Model) clampedSettingsTab() int {
	if m.settingsTab < 0 {
		return 0
	}
	if m.settingsTab > len(m.cfg.Repos) {
		return len(m.cfg.Repos)
	}
	return m.settingsTab
}

func (m *Model) clampSettingsTab() {
	m.settingsTab = m.clampedSettingsTab()
}

func (m *Model) nextSettingsTab() {
	count := len(m.cfg.Repos) + 1
	if count <= 1 {
		return
	}
	m.settingsTab = (m.clampedSettingsTab() + 1) % count
	m.settingsIndex = 0
	m.settingsOffset = 0
	m.ensureSettingsSelection(1)
}

func (m Model) visibleSettingsRows() int {
	return max(3, min(12, m.height-7))
}

func (m *Model) moveSettings(delta int) {
	m.settingsIndex += delta
	m.ensureSettingsSelection(delta)
	m.adjustSettingsOffset()
}

func (m *Model) ensureSettingsSelection(delta int) {
	entries := m.settingsEntries(m.settingsModalWidth() - 4)
	if len(entries) == 0 {
		m.settingsIndex = 0
		return
	}
	if delta == 0 {
		delta = 1
	}
	for m.settingsIndex >= 0 && m.settingsIndex < len(entries) && !entries[m.settingsIndex].selectable {
		m.settingsIndex += delta
	}
	if m.settingsIndex < 0 {
		m.settingsIndex = 0
		for m.settingsIndex < len(entries) && !entries[m.settingsIndex].selectable {
			m.settingsIndex++
		}
	}
	if m.settingsIndex >= len(entries) {
		m.settingsIndex = len(entries) - 1
		for m.settingsIndex >= 0 && !entries[m.settingsIndex].selectable {
			m.settingsIndex--
		}
	}
}

func (m *Model) adjustSettingsOffset() {
	visible := m.visibleSettingsRows()
	if m.settingsIndex < m.settingsOffset {
		m.settingsOffset = m.settingsIndex
	}
	if m.settingsIndex >= m.settingsOffset+visible {
		m.settingsOffset = m.settingsIndex - visible + 1
	}
}

func (m *Model) changeSelectedSetting(delta int) tea.Cmd {
	entries := m.settingsEntries(m.settingsModalWidth() - 4)
	if m.settingsIndex < 0 || m.settingsIndex >= len(entries) {
		return nil
	}
	entry := entries[m.settingsIndex]
	switch entry.kind {
	case "refresh":
		m.shiftRefresh(delta)
		return m.saveSettings()
	case "bell":
		v := !(m.cfg.TerminalBell != nil && *m.cfg.TerminalBell)
		m.cfg.TerminalBell = boolp(v)
		if v {
			m.status = "sound test"
			return tea.Batch(m.saveSettings(), newIncomingSoundCmd(), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return statusClearMsg{} }))
		}
		return m.saveSettings()
	case "repo":
		m.toggleRepoField(entry.repoIndex, entry.field)
		return m.saveSettings()
	case "add":
		m.repoInput = ""
		m.repoInputFromSettings = true
		m.mode = modeRepoInput
	case "remove":
		m.removeRepoIndex = entry.repoIndex
		m.mode = modeRemoveRepoConfirm
	}
	return nil
}

func (m *Model) toggleRepoField(repoIndex, field int) {
	if repoIndex < 0 || repoIndex >= len(m.cfg.Repos) {
		return
	}
	repo := &m.cfg.Repos[repoIndex]
	switch field {
	case 0:
		repo.Enabled = boolp(!value(repo.Enabled))
	case 1:
		repo.WatchMyPRs = boolp(!value(repo.WatchMyPRs))
	case 2:
		repo.WatchMyIssues = boolp(!value(repo.WatchMyIssues))
	case 3:
		repo.WatchAssignedIssues = boolp(!value(repo.WatchAssignedIssues))
	case 4:
		repo.WatchReviewPRs = boolp(!value(repo.WatchReviewPRs))
	case 5:
		repo.WatchPRDescriptionThumbsUp = boolp(!value(repo.WatchPRDescriptionThumbsUp))
	}
}

func (m Model) repoInputModal() string {
	title := "add repo"
	intro := "Add a repo to watch."
	if len(m.cfg.Repos) == 0 && !m.repoInputFromSettings {
		title = "setup"
		intro = "No repos watched."
	}
	lines := []string{
		intro,
		"",
		"Repo as owner/name:",
		selectedStyle.Render("> " + m.repoInput),
	}
	if m.status != "" {
		lines = append(lines, "", mutedStyle.Render(m.status))
	}
	lines = append(lines, "", "enter add · esc quit/close")
	return renderTitledBox(min(58, max(34, m.width-4)), len(lines)+2, title, strings.Join(lines, "\n"))
}

func (m Model) removeRepoConfirmModal() string {
	name := "repo"
	if m.removeRepoIndex >= 0 && m.removeRepoIndex < len(m.cfg.Repos) {
		name = m.cfg.Repos[m.removeRepoIndex].Name
	}
	lines := []string{
		"Remove " + name + "?",
		"",
		"y remove · n/esc cancel",
	}
	return renderTitledBox(min(58, max(34, m.width-4)), len(lines)+2, "remove repo", strings.Join(lines, "\n"))
}

func (m Model) addRepoFromInput() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.repoInput)
	name = strings.TrimPrefix(name, "https://github.com/")
	name = strings.TrimSuffix(name, ".git")
	name = strings.Trim(name, "/")
	owner, repo, ok := strings.Cut(name, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		m.status = "repo must be owner/name"
		return m, nil
	}
	name = owner + "/" + repo
	for _, existing := range m.cfg.Repos {
		if strings.EqualFold(existing.Name, name) {
			m.status = "repo already configured"
			return m, nil
		}
	}
	m.cfg.Repos = append(m.cfg.Repos, config.Repo{
		Name:                       name,
		Enabled:                    boolp(true),
		WatchMyPRs:                 boolp(true),
		WatchMyIssues:              boolp(true),
		WatchAssignedIssues:        boolp(true),
		WatchReviewPRs:             boolp(false),
		WatchPRDescriptionThumbsUp: boolp(false),
	})
	rules, err := m.cfg.RepoRules()
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.rules = rules
	m.settingsTab = len(m.cfg.Repos)
	m.settingsIndex = 0
	m.settingsOffset = 0
	m.repoInput = ""
	if m.repoInputFromSettings {
		m.mode = modeSettings
		m.repoInputFromSettings = false
	} else {
		m.mode = modeNormal
	}
	m.loading = true
	m.status = "repo added"
	return m, tea.Batch(m.saveSettings(), m.fetch())
}

func (m Model) removeRepo(repoIndex int) (tea.Model, tea.Cmd) {
	if repoIndex < 0 || repoIndex >= len(m.cfg.Repos) {
		m.mode = modeSettings
		m.status = "repo not found"
		return m, nil
	}
	name := m.cfg.Repos[repoIndex].Name
	m.cfg.Repos = append(m.cfg.Repos[:repoIndex], m.cfg.Repos[repoIndex+1:]...)
	m.settingsTab = min(m.settingsTab, len(m.cfg.Repos))
	m.settingsIndex = 0
	m.settingsOffset = 0
	rules, err := m.cfg.RepoRules()
	if err != nil {
		m.mode = modeSettings
		m.status = err.Error()
		return m, nil
	}
	m.rules = rules
	m.mode = modeSettings
	m.loading = true
	m.status = "removed " + name
	return m, tea.Batch(m.saveSettings(), m.fetch())
}

func (m *Model) shiftRefresh(delta int) {
	minutes := int(m.cfg.RefreshDuration() / time.Minute)
	minutes += delta
	if minutes < 1 {
		m.cfg.RefreshInterval = "off"
		return
	}
	m.cfg.RefreshInterval = fmt.Sprintf("%dm", minutes)
}

func (m Model) refreshLabel() string {
	minutes := int(m.cfg.RefreshDuration() / time.Minute)
	if minutes < 1 {
		return "off"
	}
	return fmt.Sprintf("%dm", minutes)
}

func (m Model) saveSettings() tea.Cmd {
	return func() tea.Msg {
		if err := m.cfg.Save(m.cfgPath); err != nil {
			return settingsSavedMsg{err: err}
		}
		rules, err := m.cfg.RepoRules()
		return settingsSavedMsg{rules: rules, err: err}
	}
}

func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		observer := m.observer
		var err error
		if observer == "" {
			observer, err = m.source.Viewer(ctx)
			if err != nil {
				return fetchMsg{err: err}
			}
		}
		states, err := m.store.States(ctx)
		if err != nil {
			return fetchMsg{err: err}
		}
		var raw []domain.RawItem
		for _, rule := range m.rules {
			if !rule.Enabled {
				continue
			}
			items, err := m.source.FetchRepo(ctx, rule.Name, observer, rule.WatchPRDescriptionThumbsUp)
			if err != nil {
				return fetchMsg{err: err}
			}
			raw = append(raw, items...)
		}
		incoming, watching, completed := domain.Classify(raw, states, m.rules, observer)
		newCompleted := []domain.RawItem{}
		for _, item := range completed {
			fresh, err := m.store.RecordCompleted(ctx, item)
			if err != nil {
				return fetchMsg{err: err}
			}
			if fresh {
				newCompleted = append(newCompleted, item)
			}
		}
		return fetchMsg{observer: observer, incoming: incoming, watching: watching, completed: newCompleted}
	}
}

func (m Model) tick() tea.Cmd {
	if m.cfg.RefreshDuration() < time.Minute {
		return nil
	}
	return tea.Tick(m.cfg.RefreshDuration(), func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) spinnerTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinnerMsg{} })
}

func newIncomingSoundCmd() tea.Cmd {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return func() tea.Msg {
		_ = exec.Command("afplay", "/System/Library/Sounds/Glass.aiff").Run()
		return nil
	}
}

func (m Model) toastTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return toastMsg{} })
}

func (m Model) togglePending(kind string, item domain.InboxItem) (tea.Model, tea.Cmd) {
	if p, ok := m.pending[item.TargetID]; ok && p.kind == kind {
		delete(m.pending, item.TargetID)
		m.status = "undone"
		return m, nil
	}
	m.pending[item.TargetID] = pending{kind: kind, item: item}
	m.status = kind + " pending"
	return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return finalizeMsg{targetID: item.TargetID, kind: kind} })
}

func (m Model) runAction(a config.ActionConfig) tea.Cmd {
	item := *m.current()
	return func() tea.Msg { return actionMsg(actions.Run(context.Background(), a, item)) }
}

func (m *Model) sortItems() {
	sort.Slice(m.incoming, func(i, j int) bool { return m.incoming[i].ActionAt.After(m.incoming[j].ActionAt) })
	sort.Slice(m.watching, func(i, j int) bool { return m.watching[i].UpdatedAt.After(m.watching[j].UpdatedAt) })
}

func (m Model) visible() []domain.InboxItem {
	var items []domain.InboxItem
	if m.view == domain.LaneIncoming {
		items = m.incoming
	} else {
		items = m.watching
	}
	if m.filter == "" {
		return items
	}
	q := strings.ToLower(m.filter)
	out := []domain.InboxItem{}
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Title), q) || strings.Contains(strings.ToLower(item.Repo), q) || strings.Contains(fmt.Sprintf("%d", item.Number), q) {
			out = append(out, item)
		}
	}
	return out
}

func (m Model) current() *domain.InboxItem {
	items := m.visible()
	if m.selected < 0 || m.selected >= len(items) {
		return nil
	}
	return &items[m.selected]
}

func (m *Model) clamp() {
	if m.selected >= len(m.visible()) {
		m.selected = len(m.visible()) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
}

func (m *Model) removeCurrent(targetID string) {
	m.incoming = remove(m.incoming, targetID)
	m.watching = remove(m.watching, targetID)
}

func remove(items []domain.InboxItem, targetID string) []domain.InboxItem {
	out := items[:0]
	for _, item := range items {
		if item.TargetID != targetID {
			out = append(out, item)
		}
	}
	return out
}

func (m *Model) expireToasts() {
	now := time.Now()
	out := m.toasts[:0]
	for _, t := range m.toasts {
		if now.Before(t.expiresAt) {
			out = append(out, t)
		}
	}
	m.toasts = out
}

func (m Model) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Printf(format, args...)
	}
}

func (m Model) spinner() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[m.spinnerFrame%len(frames)]
}

func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		if runtime.GOOS == "darwin" {
			cmd = exec.Command("open", url)
		} else {
			cmd = exec.Command("xdg-open", url)
		}
		_ = cmd.Start()
		return nil
	}
}

func setTitle() tea.Cmd {
	return func() tea.Msg {
		fmt.Print("\033]0;♜ Watchtower\007")
		return nil
	}
}

func renderBox(w, h int, content string) string {
	styleW := max(1, w-2)
	contentW := max(1, w-4)
	contentH := max(1, h-2)
	lines := visibleWrappedContentLines(content, contentW, contentH, 0)
	for i := range lines {
		lines[i] = fitLine(lines[i], contentW)
	}
	return boxStyle.Width(styleW).Height(contentH).Render(strings.Join(lines, "\n"))
}

func renderBoxWithFooter(w, h int, content, footer string, scroll int) string {
	styleW := max(1, w-2)
	contentW := max(1, w-4)
	contentH := max(1, h-2)
	footerParts := wrapFooter(footer, contentW)
	if len(footerParts) > contentH {
		footerParts = footerParts[:contentH]
	}
	lines := visibleWrappedContentLines(content, contentW, max(0, contentH-len(footerParts)), scroll)
	for i := range lines {
		lines[i] = fitLine(lines[i], contentW)
	}
	for _, line := range footerParts {
		lines = append(lines, fitLine(line, contentW))
	}
	return boxStyle.Width(styleW).Height(contentH).Render(strings.Join(lines, "\n"))
}

func renderTitledBoxWithFooter(w, h int, title, content, footer string, scroll int) string {
	w = max(w, lipgloss.Width(title)+8)
	h = max(h, 3)
	innerW := max(1, w-2)
	innerH := max(1, h-2)
	topText := "╭" + strings.Repeat("─", innerW) + "╮"
	if title != "" {
		topText = "╭─ " + title + " " + strings.Repeat("─", max(0, innerW-lipgloss.Width(title)-3)) + "╮"
	}
	top := headerStyle.Render(topText)
	bottom := headerStyle.Render("╰" + strings.Repeat("─", innerW) + "╯")
	footerParts := wrapFooter(footer, innerW)
	if len(footerParts) > innerH {
		footerParts = footerParts[:innerH]
	}
	contentH := innerH - len(footerParts)
	lines := []string{top}
	for _, line := range visibleWrappedContentLines(content, innerW, max(1, contentH), scroll) {
		lines = append(lines, headerStyle.Render("│")+fitLine(line, innerW)+headerStyle.Render("│"))
	}
	for _, line := range footerParts {
		lines = append(lines, headerStyle.Render("│")+fitLine(line, innerW)+headerStyle.Render("│"))
	}
	lines = append(lines, bottom)
	return strings.Join(lines, "\n")
}

func wrapFooter(footer string, width int) []string {
	if footer == "" {
		return nil
	}
	return wrapLines(footer, width)
}

func visibleWrappedContentLines(content string, width, height, scroll int) []string {
	return visibleLines(wrapLines(content, width), height, scroll)
}

func wrapLines(content string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	raw := strings.Split(strings.TrimRight(content, "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if line == "" {
			out = append(out, "")
			continue
		}
		if strings.HasPrefix(line, codeLinePrefix) {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "│ ") {
			wrapped := strings.Split(ansi.Wordwrap(strings.TrimPrefix(line, "│ "), max(1, width-2), " "), "\n")
			for _, part := range wrapped {
				out = append(out, "│ "+part)
			}
			continue
		}
		out = append(out, strings.Split(ansi.Wordwrap(line, width, " "), "\n")...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func visibleContentLines(content string, height, scroll int) []string {
	return visibleLines(strings.Split(strings.TrimRight(content, "\n"), "\n"), height, scroll)
}

func visibleLines(contentLines []string, height, scroll int) []string {
	if scroll < 0 {
		scroll = 0
	}
	if scroll > max(0, len(contentLines)-height) {
		scroll = max(0, len(contentLines)-height)
	}
	lines := make([]string, 0, height)
	for i := 0; i < height; i++ {
		idx := scroll + i
		line := ""
		if idx < len(contentLines) {
			line = contentLines[idx]
		}
		lines = append(lines, line)
	}
	return lines
}

func renderTitledBox(w, h int, title, content string) string {
	w = max(w, lipgloss.Width(title)+8)
	h = max(h, 3)
	innerW := max(1, w-2)
	innerH := max(1, h-2)
	topText := "╭" + strings.Repeat("─", innerW) + "╮"
	if title != "" {
		topText = "╭─ " + title + " " + strings.Repeat("─", max(0, innerW-lipgloss.Width(title)-3)) + "╮"
	}
	top := headerStyle.Render(topText)
	bottom := headerStyle.Render("╰" + strings.Repeat("─", innerW) + "╯")

	contentLines := strings.Split(content, "\n")
	lines := []string{top}
	for i := 0; i < innerH; i++ {
		line := ""
		if i < len(contentLines) {
			line = contentLines[i]
		}
		lines = append(lines, headerStyle.Render("│")+fitLine(line, innerW)+headerStyle.Render("│"))
	}
	lines = append(lines, bottom)
	return strings.Join(lines, "\n")
}

func fitLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if strings.HasPrefix(s, codeLinePrefix) {
		code := strings.TrimPrefix(s, codeLinePrefix)
		return "│ " + codeBlockStyle.Render(fitPlainLine(code, max(1, w-2)))
	}
	return fitPlainLine(s, w)
}

func fitPlainLine(s string, w int) string {
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func fitFrame(s string, w, h int) string {
	return fitBlock(s, w, h)
}

func fitBlock(s string, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	raw := strings.Split(s, "\n")
	lines := make([]string, 0, h)
	for i := 0; i < h; i++ {
		line := ""
		if i < len(raw) {
			line = raw[i]
		}
		lines = append(lines, fitLine(line, w))
	}
	return strings.Join(lines, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func boolp(v bool) *bool { return &v }

func value(v *bool) bool { return v != nil && *v }

func onOff(v bool) string {
	if v {
		return "●"
	}
	return "○"
}

func settingRow(label, value string, width int) string {
	if width <= 0 {
		return ""
	}
	valueW := lipgloss.Width(value)
	labelW := max(0, width-valueW-1)
	label = ansi.Truncate(label, labelW, "…")
	gap := max(1, width-lipgloss.Width(label)-valueW)
	return label + strings.Repeat(" ", gap) + value
}

func shortRepo(repo string) string {
	_, name, ok := strings.Cut(repo, "/")
	if !ok || name == "" {
		return repo
	}
	return name
}

func reasonIcon(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "thumbs-up"):
		return "↻"
	case strings.Contains(r, "ready to merge"):
		return "✓"
	case strings.Contains(r, "failed") || strings.Contains(r, "conflict") || strings.Contains(r, "changes requested"):
		return "✕"
	case strings.Contains(r, "assigned"):
		return "◌"
	case strings.Contains(r, "replied") || strings.Contains(r, "ready for review") || strings.Contains(r, "review"):
		return "↻"
	default:
		return "!"
	}
}

func reasonIconStyle(reason string) lipgloss.Style {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "thumbs-up"):
		return headerStyle
	case strings.Contains(r, "ready to merge"):
		return greenStyle
	case strings.Contains(r, "failed") || strings.Contains(r, "conflict") || strings.Contains(r, "changes requested"):
		return redStyle
	case strings.Contains(r, "assigned"):
		return mutedStyle
	case strings.Contains(r, "replied") || strings.Contains(r, "ready for review") || strings.Contains(r, "review"):
		return headerStyle
	default:
		return headerStyle
	}
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}

func displayTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	label := t.Format("Jan 2")
	if day.Equal(today) {
		label = "Today"
	} else if day.Equal(today.AddDate(0, 0, -1)) {
		label = "Yesterday"
	}
	return label + " " + strings.ToLower(t.Format("3:04pm"))
}

func activityQuoteLines(body string) []string {
	body = sanitizeCommentHTML(body)
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines)+2)
	inCode := false
	inDetails := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "<details") {
			inDetails = !strings.Contains(lower, "</details>")
			continue
		}
		if inDetails {
			if strings.Contains(lower, "</details>") {
				inDetails = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, codeLinePrefix+line)
			continue
		}
		if line == "" {
			out = append(out, "│")
			continue
		}
		line = stripImages(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, "│ "+styleInlineMarkdown(line))
	}
	return out
}

func sanitizeCommentHTML(s string) string {
	s = htmlCommentRE.ReplaceAllString(s, "")
	for _, re := range noisyHTMLBlockREs {
		s = re.ReplaceAllString(s, "")
	}
	s = htmlBreakRE.ReplaceAllString(s, "\n")
	s = htmlParagraphRE.ReplaceAllString(s, "\n")
	s = htmlTagRE.ReplaceAllString(s, "")
	return s
}

func stripImages(s string) string {
	s = markdownImageRE.ReplaceAllString(s, "")
	s = htmlImageRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func styleInlineMarkdown(s string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, "`")
		if start < 0 {
			b.WriteString(styleEmphasis(s))
			break
		}
		end := strings.Index(s[start+1:], "`")
		if end < 0 {
			b.WriteString(styleEmphasis(s))
			break
		}
		end += start + 1
		b.WriteString(styleEmphasis(s[:start]))
		b.WriteString(inlineCodeStyle.Render(s[start : end+1]))
		s = s[end+1:]
	}
	return b.String()
}

func styleEmphasis(s string) string {
	s = applyDelimitedStyle(s, "**", boldStyle)
	s = applyDelimitedStyle(s, "__", boldStyle)
	s = applyDelimitedStyle(s, "*", italicStyle)
	s = applyDelimitedStyle(s, "_", italicStyle)
	return s
}

func applyDelimitedStyle(s, delim string, style lipgloss.Style) string {
	var b strings.Builder
	for {
		start := strings.Index(s, delim)
		if start < 0 {
			b.WriteString(s)
			break
		}
		end := strings.Index(s[start+len(delim):], delim)
		if end < 0 {
			b.WriteString(s)
			break
		}
		end += start + len(delim)
		b.WriteString(s[:start])
		b.WriteString(style.Render(s[start+len(delim) : end]))
		s = s[end+len(delim):]
	}
	return b.String()
}

func checkStatusLine(state domain.CheckState) string {
	switch state {
	case domain.CheckPass:
		return "✓ checks pass"
	case domain.CheckFail:
		return "✕ checks fail"
	case domain.CheckPending:
		return "◌ checks pending"
	default:
		return mutedStyle.Render("◌ checks unknown")
	}
}

func mergeStatusLine(item domain.InboxItem) string {
	switch item.MergeStateStatus {
	case "CLEAN":
		return "✓ mergeable"
	case "HAS_HOOKS":
		return "✓ mergeable after hooks"
	case "BLOCKED":
		return "◌ merging blocked"
	case "BEHIND":
		return "◌ branch behind"
	case "DIRTY":
		return "✕ merge conflict"
	case "DRAFT":
		return "◌ draft"
	case "UNSTABLE":
		return "◌ checks blocking merge"
	case "UNKNOWN":
		return mutedStyle.Render("◌ merge unknown")
	}
	if item.Mergeable {
		return "✓ no merge conflicts"
	}
	return "✕ merge conflict"
}

func reviewStatusLine(decision string) string {
	s := strings.ToLower(strings.ReplaceAll(decision, "_", " "))
	switch decision {
	case "APPROVED":
		return "✓ review approved"
	case "CHANGES_REQUESTED":
		return "✕ changes requested"
	case "REVIEW_REQUIRED":
		return "◌ review required"
	default:
		return "◌ review " + s
	}
}

const codeLinePrefix = "__WT_CODE__"

var (
	markdownImageRE   = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	htmlImageRE       = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	htmlCommentRE     = regexp.MustCompile(`(?is)<!--.*?-->`)
	htmlBreakRE       = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlParagraphRE   = regexp.MustCompile(`(?i)</?p\b[^>]*>`)
	htmlTagRE         = regexp.MustCompile(`(?is)<[^>]+>`)
	noisyHTMLBlockREs = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<details\b[^>]*>.*?</details>`),
		regexp.MustCompile(`(?is)<picture\b[^>]*>.*?</picture>`),
		regexp.MustCompile(`(?is)<video\b[^>]*>.*?</video>`),
		regexp.MustCompile(`(?is)<audio\b[^>]*>.*?</audio>`),
		regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</iframe>`),
		regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg>`),
		regexp.MustCompile(`(?is)<table\b[^>]*>.*?</table>`),
	}

	brandColor = lipgloss.Color("208") // orange

	headerStyle     = lipgloss.NewStyle().Bold(true).Foreground(brandColor)
	mutedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selectedStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(brandColor)
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	greenStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	redStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	inlineCodeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("223")).Background(lipgloss.Color("235"))
	boldStyle       = lipgloss.NewStyle().Bold(true)
	italicStyle     = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("250"))
	codeBlockStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("223")).Background(lipgloss.Color("235"))
	boxStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(brandColor).Padding(0, 1)
	outputStyle     = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(brandColor).Padding(0, 1)
	confirmStyle    = lipgloss.NewStyle().Foreground(brandColor).Bold(true)
)
