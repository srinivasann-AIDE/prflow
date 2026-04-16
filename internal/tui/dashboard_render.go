package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/nagarjun226/prflow/internal/cache"
	"github.com/nagarjun226/prflow/internal/config"
)

// View renders the dashboard
func (m dashModel) View() string {
	if m.viewMode == viewSearch {
		return m.viewSearchMode()
	}
	if m.viewMode == viewReply {
		return m.viewReplyMode()
	}
	if m.viewMode == viewDetail && m.detailPR != nil {
		return m.viewDetail()
	}
	return m.viewDashboard()
}

func (m dashModel) viewSearchMode() string {
	width := m.width
	if width < 60 {
		width = 80
	}

	var s strings.Builder

	header := headerStyle.Width(width).Render(" 🔍 Search Repos")
	s.WriteString(header + "\n\n")

	// Search input
	cursor := "█"
	if m.searching {
		cursor = m.spinFrames[m.spinner%len(m.spinFrames)]
	}
	s.WriteString(fmt.Sprintf("  Search: %s%s\n\n", m.searchQuery, cursor))

	if m.searching {
		s.WriteString(fmt.Sprintf("  %s Searching...\n", m.spinFrames[m.spinner%len(m.spinFrames)]))
	} else if len(m.searchResults) > 0 {
		s.WriteString(fmt.Sprintf("  %d repos found:\n\n", len(m.searchResults)))

		maxShow := m.height - 10
		if maxShow < 5 {
			maxShow = 15
		}

		for i, repo := range m.searchResults {
			if i >= maxShow {
				s.WriteString(fmt.Sprintf("\n  + %d more...\n", len(m.searchResults)-maxShow))
				break
			}
			cursor := "  "
			if i == m.searchCursor {
				cursor = prTitleSelectedStyle.Render("▸ ")
			}

			// Check if repo exists locally
			localPath := m.findLocalRepo(repo)
			localTag := ""
			if localPath != "" {
				localTag = wsCleanStyle.Render(" (local)")
			}

			if i == m.searchCursor {
				s.WriteString(fmt.Sprintf("  %s%s%s\n", cursor, prTitleSelectedStyle.Render(repo), localTag))
			} else {
				s.WriteString(fmt.Sprintf("  %s%s%s\n", cursor, repo, localTag))
			}
		}
	} else if m.searchQuery != "" && !m.searching {
		s.WriteString("  Press [enter] to search\n")
	} else {
		s.WriteString("  Type a query to search repos in your orgs.\n")
		s.WriteString("  Select a result and press [enter] to clone.\n")
	}

	s.WriteString("\n")
	s.WriteString(helpStyle.Render("  " + strings.Join([]string{
		helpPair("enter", "search/clone"),
		helpPair("↑↓", "nav"),
		helpPair("esc", "back"),
	}, "  ")))

	return s.String()
}

func (m dashModel) viewReplyMode() string {
	width := m.width
	if width < 60 {
		width = 80
	}

	var s strings.Builder

	// Header
	header := headerStyle.Width(width).Render(" 💬 Reply to Review Comment")
	s.WriteString(header + "\n\n")

	// Show current thread context if available
	if m.detailPR != nil && m.replyThreadID != "" {
		// Find the thread
		for _, t := range m.detailThreads {
			if t.ID == m.replyThreadID && len(t.Comments) > 0 {
				firstComment := t.Comments[0]
				s.WriteString(detailLabelStyle.Render("Original Comment") + "\n")
				s.WriteString(fmt.Sprintf("  %s: %s\n\n", threadAuthorStyle.Render("@"+firstComment.Author), threadBodyStyle.Render(firstComment.Body)))
				break
			}
		}
	}

	// Reply input box
	s.WriteString(detailLabelStyle.Render("Your Reply") + "\n")
	cursor := "█"
	replyLines := strings.Split(m.replyText, "\n")
	if len(replyLines) == 0 || m.replyText == "" {
		s.WriteString(fmt.Sprintf("  %s%s\n", m.replyText, cursor))
	} else {
		for i, line := range replyLines {
			if i == len(replyLines)-1 {
				s.WriteString(fmt.Sprintf("  %s%s\n", line, cursor))
			} else {
				s.WriteString(fmt.Sprintf("  %s\n", line))
			}
		}
	}

	s.WriteString("\n")

	// Character count
	charCount := len(m.replyText)
	countStyle := wsMetaStyle
	if charCount > 500 {
		countStyle = wsDirtyStyle
	}
	if charCount > 1000 {
		countStyle = wsBehindStyle
	}
	s.WriteString(fmt.Sprintf("  %s\n\n", countStyle.Render(fmt.Sprintf("%d characters", charCount))))

	// Help text
	s.WriteString(helpStyle.Render("  " + strings.Join([]string{
		helpPair("enter", "post"),
		helpPair("ctrl+u", "clear"),
		helpPair("esc", "cancel"),
	}, "  ")))

	return s.String()
}

func (m dashModel) viewDashboard() string {
	// Header Bar
	syncAgo := ""
	if !m.lastSync.IsZero() {
		syncAgo = fmt.Sprintf("  ⟳ %s", timeSince(m.lastSync))
	}
	totalPRs := len(m.doNow) + len(m.waiting) + len(m.review)

	headerWidth := m.width
	if headerWidth < 60 {
		headerWidth = 80
	}
	header := headerStyle.Width(headerWidth).Render(
		fmt.Sprintf(" ⚡ PRFlow    @%s  ·  %d active PRs%s", m.username, totalPRs, syncAgo))

	// Morning summary line
	var summaryParts []string
	if len(m.doNow) > 0 {
		summaryParts = append(summaryParts, wsBehindStyle.Render(fmt.Sprintf("%d need your action", len(m.doNow))))
	}
	if len(m.waiting) > 0 {
		staleCount := 0
		staleDays := config.ParseStaleThresholdDays(m.cfg.Settings.StaleThreshold)
		for _, pr := range m.waiting {
			statuses := CalculateReviewerStatus(&pr.PR)
			for _, s := range statuses {
				if s.WaitDays >= staleDays {
					staleCount++
					break
				}
			}
		}
		waitMsg := fmt.Sprintf("%d waiting on reviewers", len(m.waiting))
		if staleCount > 0 {
			waitMsg += fmt.Sprintf(" (%d stale)", staleCount)
		}
		summaryParts = append(summaryParts, wsDirtyStyle.Render(waitMsg))
	}
	if len(m.review) > 0 {
		summaryParts = append(summaryParts, wsCleanStyle.Render(fmt.Sprintf("%d reviews requested", len(m.review))))
	}
	if len(m.needsAttention) > 0 {
		summaryParts = append(summaryParts, wsMetaStyle.Render(fmt.Sprintf("%d need re-review", len(m.needsAttention))))
	}
	summaryLine := ""
	if len(summaryParts) > 0 {
		summaryLine = "\n  " + strings.Join(summaryParts, "  ·  ")
	}

	// Sidebar
	sidebar := m.renderSidebar()

	// Main Panel
	mainWidth := m.width - 28
	if mainWidth < 40 {
		mainWidth = 60
	}
	main := m.renderMainPanel(mainWidth)

	// Status Bar
	statusBar := m.renderStatusBar()

	// Help Bar
	help := m.renderHelp()

	// Compose Layout
	content := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)

	return header + summaryLine + "\n" + content + "\n" + statusBar + help
}

func (m dashModel) renderSidebar() string {
	var s strings.Builder

	type sidebarEntry struct {
		icon  string
		label string
		count int
		sec   section
	}

	entries := []sidebarEntry{
		{"⚡", "Do Now", len(m.doNow), sectionDoNow},
		{"⏳", "Waiting", len(m.waiting), sectionWaiting},
		{"👀", "Review", len(m.review), sectionReview},
		{"📂", "Workspace", len(m.workspace), sectionWorkspace},
		{"🔔", "Needs Attention", len(m.needsAttention), sectionNeedsAttention},
	}

	for _, e := range entries {
		countStr := sidebarCountStyle.Render(fmt.Sprintf(" %d", e.count))
		if e.sec == m.section {
			s.WriteString(sidebarActiveStyle.Render(
				fmt.Sprintf("▸ %s %s", e.icon, e.label)) + countStr + "\n")
		} else {
			s.WriteString(sidebarSectionStyle.Render(
				fmt.Sprintf("  %s %s", e.icon, e.label)) + countStr + "\n")
		}
	}

	// Favorites
	if len(m.cfg.Favorites) > 0 {
		s.WriteString("\n")
		s.WriteString(favHeaderStyle.Render("★ Favorites") + "\n")
		for _, fav := range m.cfg.Favorites {
			parts := strings.Split(fav, "/")
			name := fav
			if len(parts) == 2 {
				name = parts[1]
			}
			s.WriteString(favItemStyle.Render(name) + "\n")
		}
	}

	return sidebarStyle.Render(s.String())
}

func (m dashModel) renderMainPanel(width int) string {
	var s strings.Builder

	// Section header with rule
	s.WriteString(sectionHeader.Width(width - 4).Render(m.section.String()))
	s.WriteString("\n")

	if m.loading {
		spin := m.spinFrames[m.spinner%len(m.spinFrames)]
		s.WriteString(fmt.Sprintf("\n %s Syncing with GitHub...\n", spin))
		return mainPanelStyle.Width(width).Render(s.String())
	}

	switch m.section {
	case sectionDoNow:
		s.WriteString(m.renderPRCards(m.doNow, width))
	case sectionWaiting:
		s.WriteString(m.renderPRCards(m.waiting, width))
	case sectionReview:
		s.WriteString(m.renderPRCards(m.review, width))
	case sectionWorkspace:
		s.WriteString(m.renderWorkspaceCards(width))
	case sectionNeedsAttention:
		s.WriteString(m.renderPRCards(m.needsAttention, width))
	}

	return mainPanelStyle.Width(width).Render(s.String())
}

func (m dashModel) renderPRCards(prs []cache.CachedPR, width int) string {
	if len(prs) == 0 {
		return emptyStyle.Render("Nothing here — you're all caught up! 🎉")
	}

	var s strings.Builder
	maxShow := m.height - 10
	if maxShow < 5 {
		maxShow = 15
	}

	// Scroll window: keep cursor visible
	start := 0
	if m.cursor >= maxShow {
		start = m.cursor - maxShow + 1
	}

	for i := start; i < len(prs); i++ {
		if i-start >= maxShow {
			s.WriteString(fmt.Sprintf("\n  %s\n",
				repoStyle.Render(fmt.Sprintf("+ %d more...", len(prs)-i))))
			break
		}
		s.WriteString(m.renderPRCard(prs[i], i == m.cursor, width-6))
	}
	return s.String()
}

func (m dashModel) renderPRCard(pr cache.CachedPR, selected bool, width int) string {
	// Repo short name
	parts := strings.Split(pr.Repo, "/")
	repoShort := pr.Repo
	if len(parts) == 2 {
		repoShort = parts[1]
	}

	// Line 1: repo  #number  title
	numStr := prNumberStyle.Render(fmt.Sprintf("#%d", pr.Number))
	repoStr := prRepoStyle.Render(repoShort)

	maxTitle := width - len(repoShort) - 10
	if maxTitle < 20 {
		maxTitle = 30
	}
	title := truncate(pr.Title, maxTitle)

	titleStr := prTitleStyle.Render(title)
	if selected {
		titleStr = prTitleSelectedStyle.Render(title)
	}

	line1 := fmt.Sprintf("%s  %s  %s", repoStr, numStr, titleStr)

	// Line 2: status badge + time + section-specific context
	badge := m.prBadge(pr)
	timeAgo := ""
	if pr.UpdatedAt != "" {
		timeAgo = wsMetaStyle.Render(fmt.Sprintf("  updated %s", formatTimeAgo(pr.UpdatedAt)))
	}

	staleDays := config.ParseStaleThresholdDays(m.cfg.Settings.StaleThreshold)
	var contextStr string
	switch pr.Section {
	case "waiting":
		// Show stalest reviewer and their wait time
		reviewerStatuses := CalculateReviewerStatus(&pr.PR)
		if len(reviewerStatuses) > 0 {
			stalest := reviewerStatuses[0]
			if stalest.WaitDays >= staleDays {
				contextStr = wsBehindStyle.Render(fmt.Sprintf("  ⏰ @%s (%dd)", stalest.Login, stalest.WaitDays))
			} else if stalest.IsPending {
				contextStr = wsMetaStyle.Render(fmt.Sprintf("  ⏳ @%s waiting %dd", stalest.Login, stalest.WaitDays))
			}
		}
	case "do_now":
		// Show the specific reason action is needed
		switch {
		case pr.Mergeable == "CONFLICTING":
			contextStr = wsBehindStyle.Render("  ✗ merge conflict")
		case pr.ReviewDecision == "CHANGES_REQUESTED":
			// show who requested changes
			for _, rev := range pr.Reviews.Nodes {
				if rev.State == "CHANGES_REQUESTED" {
					contextStr = wsBehindStyle.Render(fmt.Sprintf("  ✗ changes requested by @%s", rev.Author.Login))
					break
				}
			}
			if contextStr == "" {
				contextStr = wsBehindStyle.Render("  ✗ changes requested")
			}
		case pr.ReviewDecision == "APPROVED":
			contextStr = wsCleanStyle.Render("  ✓ approved — ready to merge")
		default:
			// CI failure
			for _, check := range pr.StatusCheckRollup {
				if check.Conclusion == "FAILURE" || check.Conclusion == "TIMED_OUT" || check.Conclusion == "ACTION_REQUIRED" {
					contextStr = wsBehindStyle.Render(fmt.Sprintf("  ✗ CI: %s", check.Name))
					break
				}
			}
		}
	case "review":
		// Show how long the author has been waiting for your review.
		// Use updatedAt if available (more recent activity), else createdAt.
		waitDays := DaysSinceUpdate(pr.UpdatedAt)
		if pr.UpdatedAt == "" {
			waitDays = DaysSinceUpdate(pr.CreatedAt)
		}
		if waitDays >= staleDays {
			contextStr = wsBehindStyle.Render(fmt.Sprintf("  ⏰ @%s waiting %dd for review", pr.Author.Login, waitDays))
		} else {
			contextStr = wsMetaStyle.Render(fmt.Sprintf("  by @%s", pr.Author.Login))
		}
	case "needs_attention":
		contextStr = wsDirtyStyle.Render(fmt.Sprintf("  ↺ updated after your review %s", formatTimeAgo(pr.UpdatedAt)))
	}

	line2 := badge + timeAgo + contextStr

	content := line1 + "\n" + line2

	if selected {
		return prCardSelectedStyle.Width(width).Render(content)
	}
	return prCardStyle.Width(width).Render(content)
}

func (m dashModel) prBadge(pr cache.CachedPR) string {
	switch {
	case pr.ReviewDecision == "APPROVED" && pr.Mergeable != "CONFLICTING":
		// Check CI status for approved PRs
		if HasPassingCI(pr) {
			return badgeMerge.Render("READY ✓CI")
		} else {
			// CI failing or pending
			hasFailedCI := false
			for _, check := range pr.StatusCheckRollup {
				if check.Conclusion == "FAILURE" || check.Conclusion == "TIMED_OUT" || check.Conclusion == "ACTION_REQUIRED" {
					hasFailedCI = true
					break
				}
			}
			if hasFailedCI {
				return badgeConflict.Render("READY ✗CI")
			}
			// CI pending/in-progress
			return badgeWaiting.Render("READY ⏳CI")
		}
	case pr.ReviewDecision == "APPROVED" && pr.Mergeable == "CONFLICTING":
		return badgeConflict.Render("CONFLICT")
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		return badgeChanges.Render("CHANGES REQUESTED")
	case pr.Mergeable == "CONFLICTING":
		return badgeConflict.Render("CONFLICT")
	case pr.IsDraft:
		return badgeDraft.Render("DRAFT")
	case pr.ReviewDecision == "REVIEW_REQUIRED":
		return badgeWaiting.Render("AWAITING REVIEW")
	default:
		return badgeWaiting.Render("IN REVIEW")
	}
}

func (m dashModel) renderWorkspaceCards(width int) string {
	if len(m.workspace) == 0 {
		return emptyStyle.Render("No repos found.\nConfigure workspace.scan_dirs in ~/.config/prflow/config.yaml")
	}

	var s strings.Builder
	maxShow := m.height - 10
	if maxShow < 5 {
		maxShow = 15
	}

	// Scroll window: keep cursor visible
	start := 0
	if m.cursor >= maxShow {
		start = m.cursor - maxShow + 1
	}

	for i := start; i < len(m.workspace); i++ {
		if i-start >= maxShow {
			s.WriteString(fmt.Sprintf("\n  %s\n",
				repoStyle.Render(fmt.Sprintf("+ %d more...", len(m.workspace)-i))))
			break
		}
		ws := m.workspace[i]
		s.WriteString(m.renderWorkspaceCard(&ws, i == m.cursor, width-6))
	}
	return s.String()
}

func (m dashModel) renderWorkspaceCard(ws *RepoStatus, selected bool, width int) string {
	nameStr := prNumberStyle.Render(ws.Name)

	// Branch
	branchStr := wsMetaStyle.Render("on ") + detailValueStyle.Render(ws.Branch)

	// Behind/ahead
	var baStr string
	if ws.Behind > 0 && ws.Behind > 20 {
		baStr = wsBehindStyle.Render(fmt.Sprintf("↓%d behind", ws.Behind)) + "  "
	} else if ws.Behind > 0 {
		baStr = wsDirtyStyle.Render(fmt.Sprintf("↓%d behind", ws.Behind)) + "  "
	}
	if ws.Ahead > 0 {
		baStr += wsCleanStyle.Render(fmt.Sprintf("↑%d ahead", ws.Ahead))
	}
	if ws.Behind == 0 && ws.Ahead == 0 {
		baStr = wsCleanStyle.Render("up to date")
	}

	// Working tree
	var treeStr string
	if ws.Clean {
		treeStr = wsCleanStyle.Render("✓ clean")
	} else {
		var parts []string
		if ws.Modified > 0 {
			parts = append(parts, fmt.Sprintf("%d modified", ws.Modified))
		}
		if ws.Staged > 0 {
			parts = append(parts, fmt.Sprintf("%d staged", ws.Staged))
		}
		if ws.Untracked > 0 {
			parts = append(parts, fmt.Sprintf("%d untracked", ws.Untracked))
		}
		treeStr = wsDirtyStyle.Render(strings.Join(parts, " · "))
	}

	// Unpushed
	unpushedStr := ""
	if ws.Unpushed > 0 {
		unpushedStr = "\n" + wsDirtyStyle.Render(fmt.Sprintf("⬆ %d unpushed", ws.Unpushed))
	}

	// Stale branches
	staleStr := ""
	if len(ws.StaleBranches) > 0 {
		staleStr = "\n" + wsDirtyStyle.Render(fmt.Sprintf("⚠ %d stale branches (merged)", len(ws.StaleBranches)))
	}

	// Last commit
	commitStr := ""
	if ws.LastCommit != "" {
		commitStr = "\n" + wsMetaStyle.Render(ws.LastCommit)
	}

	content := nameStr + "  " + branchStr + "\n" + baStr + "  " + treeStr + unpushedStr + staleStr + commitStr

	if selected {
		return wsCardSelectedStyle.Width(width).Render(content)
	}
	return wsCardStyle.Width(width).Render(content)
}

func (m dashModel) viewDetail() string {
	pr := m.detailPR
	var s strings.Builder

	width := m.width
	if width < 60 {
		width = 80
	}

	// Header
	header := headerStyle.Width(width).Render(
		fmt.Sprintf(" %s  #%d", pr.Repo, pr.Number))
	s.WriteString(header + "\n\n")

	// Title
	s.WriteString("  " + lipgloss.NewStyle().Bold(true).Foreground(colorWhite).Render(pr.Title) + "\n\n")

	// Info grid
	s.WriteString(fmt.Sprintf("  %s %s → %s\n",
		detailLabelStyle.Render("Branch"),
		detailValueStyle.Render(pr.HeadRefName),
		wsMetaStyle.Render(pr.BaseRefName)))

	// Status badge
	s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Status"), m.prBadge(cache.CachedPR{PR: pr.PR})))

	// Mergeable
	if pr.Mergeable != "" {
		mergeIcon := "  "
		if pr.Mergeable == "MERGEABLE" {
			mergeIcon = wsCleanStyle.Render("✓ MERGEABLE")
		} else if pr.Mergeable == "CONFLICTING" {
			mergeIcon = wsBehindStyle.Render("✗ CONFLICTING")
		} else {
			mergeIcon = wsMetaStyle.Render(pr.Mergeable)
		}
		s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Merge"), mergeIcon))
	}

	// CI Checks
	if len(pr.StatusCheckRollup) > 0 {
		s.WriteString(fmt.Sprintf("\n  %s\n", detailLabelStyle.Render("CI Checks")))
		for _, check := range pr.StatusCheckRollup {
			var icon string
			switch check.Conclusion {
			case "SUCCESS":
				icon = wsCleanStyle.Render("✓")
			case "FAILURE":
				icon = wsBehindStyle.Render("✗")
			default:
				icon = wsDirtyStyle.Render("⏳")
			}
			s.WriteString(fmt.Sprintf("    %s %s\n", icon, check.Name))
		}
	}

	// Reviewers with wait times
	reviewerStatuses := CalculateReviewerStatus(&pr.PR)
	if len(reviewerStatuses) > 0 {
		s.WriteString(fmt.Sprintf("\n  %s\n", detailLabelStyle.Render("Reviewers")))
		for _, status := range reviewerStatuses {
			icon := reviewerIcon(status.State)
			nameStr := threadAuthorStyle.Render("@" + status.Login)
			stateStr := formatReviewState(status.State)
			waitStr := formatWaitTime(status.WaitDays, status.IsPending)
			s.WriteString(fmt.Sprintf("    %s %s  %s  %s\n", icon, nameStr, stateStr, waitStr))
		}
	}

	// Review Threads
	if len(m.detailThreads) > 0 {
		unresolved := 0
		resolved := 0
		for _, t := range m.detailThreads {
			if t.IsResolved {
				resolved++
			} else {
				unresolved++
			}
		}

		s.WriteString("\n" + threadHeaderStyle.Render(
			fmt.Sprintf("  📝 Unresolved Threads (%d)", unresolved)) + "\n\n")

		threadIdx := 0
		for _, t := range m.detailThreads {
			if t.IsResolved {
				continue
			}
			selected := threadIdx == m.threadCursor

			fileStr := threadFileStyle.Render(fmt.Sprintf("%s:%d", t.Path, t.Line))
			var body string
			if len(t.Comments) > 0 {
				last := t.Comments[len(t.Comments)-1]
				body = threadAuthorStyle.Render("@"+last.Author) + "  " +
					threadBodyStyle.Render(truncate(last.Body, 70))
			}

			content := fileStr + "\n" + body
			cardWidth := width - 8
			if cardWidth < 40 {
				cardWidth = 60
			}

			if selected {
				s.WriteString("  " + threadCardSelectedStyle.Width(cardWidth).Render(content) + "\n")
			} else {
				s.WriteString("  " + threadCardStyle.Width(cardWidth).Render(content) + "\n")
			}
			threadIdx++
		}

		if resolved > 0 {
			s.WriteString(fmt.Sprintf("  %s\n",
				wsMetaStyle.Render(fmt.Sprintf("  ✅ %d resolved threads (collapsed)", resolved))))
		}
	}

	// AI Analysis (if available)
	if m.aiLoading {
		s.WriteString(fmt.Sprintf("\n  %s %s Analyzing with Claude Code...\n",
			threadHeaderStyle.Render("🤖 AI Analysis"),
			m.spinFrames[m.spinner%len(m.spinFrames)]))
	} else if m.aiAnalysis != nil {
		a := m.aiAnalysis
		s.WriteString("\n" + threadHeaderStyle.Render("  🤖 AI Analysis") + "\n\n")

		if a.Summary != "" {
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Summary"), detailValueStyle.Render(a.Summary)))
		}
		if a.ActionNeeded != "" {
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Next Action"), wsCleanStyle.Render(a.ActionNeeded)))
		}
		if a.ReviewSummary != "" {
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Reviews"), detailValueStyle.Render(a.ReviewSummary)))
		}
		if a.RiskLevel != "" {
			riskStyle := wsCleanStyle
			if a.RiskLevel == "medium" {
				riskStyle = wsDirtyStyle
			} else if a.RiskLevel == "high" {
				riskStyle = wsBehindStyle
			}
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Risk"), riskStyle.Render(a.RiskLevel)))
		}
		if a.BlockedBy != "" {
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Blocked By"), wsBehindStyle.Render(a.BlockedBy)))
		}
		if len(a.SuggestedFixes) > 0 {
			s.WriteString(fmt.Sprintf("  %s\n", detailLabelStyle.Render("Suggestions")))
			for _, fix := range a.SuggestedFixes {
				s.WriteString(fmt.Sprintf("    → %s\n", detailValueStyle.Render(fix)))
			}
		}
	}

	// AI Thread Analysis (if available)
	if m.aiThread != nil {
		t := m.aiThread
		s.WriteString("\n" + threadHeaderStyle.Render("  🤖 Thread Analysis") + "\n\n")
		if t.Intent != "" {
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Intent"), detailValueStyle.Render(t.Intent)))
		}
		if t.Complexity != "" {
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Complexity"), detailValueStyle.Render(t.Complexity)))
		}
		if t.Suggestion != "" {
			s.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Approach"), wsCleanStyle.Render(t.Suggestion)))
		}
		if t.DraftReply != "" {
			s.WriteString(fmt.Sprintf("\n  %s\n", detailLabelStyle.Render("Draft Reply")))
			s.WriteString(fmt.Sprintf("  %s\n", threadBodyStyle.Render(t.DraftReply)))
		}
	}

	// URL
	s.WriteString(fmt.Sprintf("\n  %s %s\n", detailLabelStyle.Render("URL"), urlStyle.Render(pr.URL)))

	// Help
	s.WriteString("\n" + m.renderDetailHelp())

	return s.String()
}

func (m dashModel) renderStatusBar() string {
	var parts []string
	if m.statusMsg != "" {
		parts = append(parts, statusBarStyle.Render("  "+m.statusMsg))
	}
	if m.err != "" {
		parts = append(parts, statusErrorStyle.Render("  ⚠ "+m.err))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "  ") + "\n"
}

func (m dashModel) renderHelp() string {
	var pairs []string

	pairs = append(pairs, helpPair("↑↓", "nav"))
	pairs = append(pairs, helpPair("tab", "section"))
	pairs = append(pairs, helpPair("enter", "expand"))
	pairs = append(pairs, helpPair("o", "open"))

	if m.section == sectionWorkspace {
		pairs = append(pairs, helpPair("p", "pull"))
		pairs = append(pairs, helpPair("P", "push"))
		pairs = append(pairs, helpPair("f", "fetch"))
	} else {
		pairs = append(pairs, helpPair("c", "checkout"))
		pairs = append(pairs, helpPair("C", "clone"))
	}

	pairs = append(pairs, helpPair("/", "search"))
	pairs = append(pairs, helpPair("R", "refresh"))
	pairs = append(pairs, helpPair("q", "quit"))

	return helpStyle.Render("  " + strings.Join(pairs, "  "))
}

func (m dashModel) renderDetailHelp() string {
	pairs := []string{
		helpPair("o", "open in browser"),
		helpPair("c", "checkout"),
		helpPair("a", "approve"),
		helpPair("m", "merge"),
	}

	// Only show resolve if there are unresolved threads
	if len(m.detailThreads) > 0 {
		hasUnresolved := false
		for _, t := range m.detailThreads {
			if !t.IsResolved {
				hasUnresolved = true
				break
			}
		}
		if hasUnresolved {
			pairs = append(pairs, helpPair("r", "resolve"))
			pairs = append(pairs, helpPair("R", "reply"))
		}
	}

	if m.aiAvailable {
		pairs = append(pairs, helpPair("A", "AI analyze"))
	}
	pairs = append(pairs,
		helpPair("esc", "back"),
		helpPair("q", "quit"),
	)
	return helpStyle.Render("  " + strings.Join(pairs, "  "))
}

func timeSince(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		return "just now"
	}
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	hours := int(d.Hours())
	if hours < 24 {
		return fmt.Sprintf("%dh ago", hours)
	}
	days := hours / 24
	if days == 1 {
		return "yesterday"
	}
	if days < 30 {
		return fmt.Sprintf("%dd ago", days)
	}
	months := days / 30
	if months == 1 {
		return "1 month ago"
	}
	if months < 12 {
		return fmt.Sprintf("%d months ago", months)
	}
	years := months / 12
	if years == 1 {
		return "1 year ago"
	}
	return fmt.Sprintf("%d years ago", years)
}

func formatTimeAgo(isoTime string) string {
	if isoTime == "" {
		return ""
	}
	// Try multiple time formats
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02 15:04:05 -0700 MST",
		time.RFC3339Nano,
	}
	for _, layout := range formats {
		t, err := time.Parse(layout, isoTime)
		if err == nil {
			return timeSince(t)
		}
	}
	return ""
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if max < 4 {
		if len(s) > max {
			return s[:max]
		}
		return s
	}
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
