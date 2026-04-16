package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nagarjun226/prflow/internal/cache"
	"github.com/nagarjun226/prflow/internal/gh"
)

// linkWorkspacePRs cross-references local workspace branches against open PRs.
// Populates RepoStatus.LinkedPR so the Workspace tab shows which PR a branch belongs to.
func (m *dashModel) linkWorkspacePRs() {
	if len(m.workspace) == 0 {
		return
	}
	// Build branch→PR map from all sections
	type prRef struct {
		number   int
		section  string
		decision string
	}
	branchMap := make(map[string]prRef) // "org/repo:branch" -> prRef
	allPRs := make([]cache.CachedPR, 0, len(m.doNow)+len(m.waiting)+len(m.review)+len(m.needsAttention))
	allPRs = append(allPRs, m.doNow...)
	allPRs = append(allPRs, m.waiting...)
	allPRs = append(allPRs, m.review...)
	allPRs = append(allPRs, m.needsAttention...)
	for _, pr := range allPRs {
		if pr.HeadRefName == "" {
			continue
		}
		key := pr.Repo + ":" + pr.HeadRefName
		branchMap[key] = prRef{number: pr.Number, section: pr.Section, decision: pr.ReviewDecision}
	}

	for i := range m.workspace {
		ws := &m.workspace[i]
		key := ws.Name + ":" + ws.Branch
		if ref, ok := branchMap[key]; ok {
			label := fmt.Sprintf("#%d", ref.number)
			switch ref.section {
			case "do_now":
				switch ref.decision {
				case "APPROVED":
					label += " (approved — merge!)"
				case "CHANGES_REQUESTED":
					label += " (changes requested)"
				default:
					label += " (needs action)"
				}
			case "waiting":
				label += " (waiting for review)"
			case "review":
				label += " (review requested)"
			case "needs_attention":
				label += " (needs re-review)"
			}
			ws.LinkedPR = label
		} else {
			ws.LinkedPR = ""
		}
	}
}

func (m *dashModel) openDetail() (tea.Model, tea.Cmd) {
	var pr *cache.CachedPR
	switch m.section {
	case sectionDoNow:
		if m.cursor < len(m.doNow) {
			pr = &m.doNow[m.cursor]
		}
	case sectionWaiting:
		if m.cursor < len(m.waiting) {
			pr = &m.waiting[m.cursor]
		}
	case sectionReview:
		if m.cursor < len(m.review) {
			pr = &m.review[m.cursor]
		}
	case sectionNeedsAttention:
		if m.cursor < len(m.needsAttention) {
			pr = &m.needsAttention[m.cursor]
		}
	case sectionWorkspace:
		// Workspace items show path info and open in browser
		if m.cursor < len(m.workspace) {
			ws := m.workspace[m.cursor]
			m.statusMsg = fmt.Sprintf("📁 %s", ws.Path)
			if ws.HasRemote {
				gh.OpenInBrowser(fmt.Sprintf("https://github.com/%s", ws.Name))
			}
		}
		return m, nil
	}
	if pr == nil {
		return m, nil
	}

	return m, loadDetail(pr)
}

func loadDetail(pr *cache.CachedPR) tea.Cmd {
	return func() tea.Msg {
		detail, err := gh.GetPRDetail(pr.Repo, pr.Number)
		if err != nil {
			return detailLoadedMsg{err: err}
		}
		cached := &cache.CachedPR{PR: *detail, Repo: pr.Repo, Section: pr.Section}

		threads, _ := gh.GetReviewThreads(pr.Repo, pr.Number)

		return detailLoadedMsg{pr: cached, threads: threads}
	}
}

func (m *dashModel) openInBrowser() {
	// In detail view, open the selected thread URL if available
	if m.viewMode == viewDetail {
		if m.detailPR != nil {
			// Try to open the thread URL at the cursor
			if len(m.detailThreads) > 0 {
				unresolvedIdx := 0
				for _, t := range m.detailThreads {
					if t.IsResolved {
						continue
					}
					if unresolvedIdx == m.threadCursor {
						if len(t.Comments) > 0 {
							gh.OpenInBrowser(t.Comments[0].URL)
							return
						}
					}
					unresolvedIdx++
				}
			}
			// Fallback: open the PR URL
			gh.OpenInBrowser(m.detailPR.URL)
		}
		return
	}

	switch m.section {
	case sectionDoNow:
		if m.cursor < len(m.doNow) {
			gh.OpenInBrowser(m.doNow[m.cursor].URL)
		}
	case sectionWaiting:
		if m.cursor < len(m.waiting) {
			gh.OpenInBrowser(m.waiting[m.cursor].URL)
		}
	case sectionReview:
		if m.cursor < len(m.review) {
			gh.OpenInBrowser(m.review[m.cursor].URL)
		}
	case sectionNeedsAttention:
		if m.cursor < len(m.needsAttention) {
			gh.OpenInBrowser(m.needsAttention[m.cursor].URL)
		}
	case sectionWorkspace:
		if m.cursor < len(m.workspace) {
			ws := m.workspace[m.cursor]
			if ws.HasRemote {
				gh.OpenInBrowser(fmt.Sprintf("https://github.com/%s", ws.Name))
			}
		}
	}
}

func (m *dashModel) gitPullCmd() tea.Cmd {
	if m.cursor >= len(m.workspace) {
		return nil
	}
	ws := m.workspace[m.cursor]
	return func() tea.Msg {
		_, err := gitCmd(ws.Path, "pull", "origin", ws.Branch)
		return gitOpDoneMsg{msg: fmt.Sprintf("pull %s/%s", ws.Name, ws.Branch), err: err}
	}
}

func (m *dashModel) gitPushCmd() tea.Cmd {
	if m.cursor >= len(m.workspace) {
		return nil
	}
	ws := m.workspace[m.cursor]
	return func() tea.Msg {
		_, err := gitCmd(ws.Path, "push", "origin", ws.Branch)
		return gitOpDoneMsg{msg: fmt.Sprintf("push %s/%s", ws.Name, ws.Branch), err: err}
	}
}

func (m *dashModel) fetchAllCmd() tea.Cmd {
	workspace := m.workspace
	return func() tea.Msg {
		failed := 0
		for _, ws := range workspace {
			_, err := gitCmd(ws.Path, "fetch", "--all")
			if err != nil {
				failed++
			}
		}
		msg := fmt.Sprintf("fetched %d repos", len(workspace))
		var err error
		if failed > 0 {
			msg = fmt.Sprintf("fetched %d repos (%d failed)", len(workspace), failed)
		}
		return gitOpDoneMsg{msg: msg, err: err}
	}
}

// updateSearch handles key input in search mode
func (m *dashModel) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.viewMode = viewList
		return m, nil
	case "enter":
		if len(m.searchResults) > 0 && m.searchCursor < len(m.searchResults) {
			// Clone selected repo
			repo := m.searchResults[m.searchCursor]
			m.statusMsg = fmt.Sprintf("Cloning %s...", repo)
			return m, m.cloneRepoCmd(repo)
		}
		if len(m.searchQuery) > 0 && !m.searching {
			// Execute search
			m.searching = true
			q := m.searchQuery
			return m, func() tea.Msg {
				repos, err := gh.SearchOrgRepos(q)
				return searchResultsMsg{repos: repos, err: err}
			}
		}
	case "up":
		if m.searchCursor > 0 {
			m.searchCursor--
		}
	case "down":
		if len(m.searchResults) > 0 && m.searchCursor < len(m.searchResults)-1 {
			m.searchCursor++
		}
	case "backspace":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
			m.searchResults = nil
		}
	default:
		ch := msg.String()
		if len(ch) == 1 && ch[0] >= 32 {
			m.searchQuery += ch
			m.searchResults = nil
		}
	}
	return m, nil
}

// updateReply handles key input in reply mode
func (m *dashModel) updateReply(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		// Cancel reply and return to detail view
		m.viewMode = viewDetail
		m.replyText = ""
		m.replyThreadID = ""
		return m, nil
	case "enter":
		// Submit reply if text is not empty
		if len(strings.TrimSpace(m.replyText)) > 0 && m.detailPR != nil {
			pr := m.detailPR
			threadID := m.replyThreadID
			replyText := m.replyText

			// Return to detail view and post reply
			m.viewMode = viewDetail
			m.replyText = ""
			m.replyThreadID = ""
			m.statusMsg = "Posting reply..."

			return m, func() tea.Msg {
				err := gh.ReplyToComment(pr.Repo, pr.Number, threadID, replyText)
				return prActionDoneMsg{action: "replied", err: err}
			}
		}
		return m, nil
	case "backspace":
		if len(m.replyText) > 0 {
			m.replyText = m.replyText[:len(m.replyText)-1]
		}
	case "ctrl+u":
		// Clear entire input
		m.replyText = ""
	default:
		// Add typed character to reply text
		ch := msg.String()
		if len(ch) == 1 && ch[0] >= 32 {
			m.replyText += ch
		}
	}
	return m, nil
}

func (m *dashModel) checkoutPRCmd() tea.Cmd {
	var pr *cache.CachedPR
	switch m.section {
	case sectionDoNow:
		if m.cursor < len(m.doNow) {
			pr = &m.doNow[m.cursor]
		}
	case sectionWaiting:
		if m.cursor < len(m.waiting) {
			pr = &m.waiting[m.cursor]
		}
	case sectionReview:
		if m.cursor < len(m.review) {
			pr = &m.review[m.cursor]
		}
	case sectionNeedsAttention:
		if m.cursor < len(m.needsAttention) {
			pr = &m.needsAttention[m.cursor]
		}
	}
	if pr == nil {
		return nil
	}

	repo := pr.Repo
	branch := pr.HeadRefName
	number := pr.Number

	// Capture workspace scan dirs for use inside the closure
	localPath := m.findLocalRepo(repo)
	reposDir := m.cfg.Settings.ReposDir

	m.statusMsg = fmt.Sprintf("Checking out PR #%d...", number)
	return func() tea.Msg {
		// If branch name is unknown (search results), fetch it first
		if branch == "" {
			detail, err := gh.GetPRDetail(repo, number)
			if err != nil || detail.HeadRefName == "" {
				return checkoutDoneMsg{repo: repo, branch: "unknown", err: fmt.Errorf("can't determine branch for PR #%d", number)}
			}
			branch = detail.HeadRefName
		}

		// If repo exists locally, checkout there
		if localPath != "" {
			gitCmd(localPath, "fetch", "--all")
			_, err := gitCmd(localPath, "checkout", branch)
			if err != nil {
				_, err = gitCmd(localPath, "checkout", "-b", branch, "origin/"+branch)
			}
			return checkoutDoneMsg{repo: repo, branch: branch, err: err}
		}

		// Not found locally — clone first, then checkout
		dest := reposDir + "/" + repo
		if !isGitRepo(dest) {
			err := gh.CloneRepo(repo, dest)
			if err != nil {
				return checkoutDoneMsg{repo: repo, branch: branch, err: fmt.Errorf("clone failed: %w", err)}
			}
		}
		gitCmd(dest, "fetch", "--all")
		_, err := gitCmd(dest, "checkout", branch)
		if err != nil {
			_, err = gitCmd(dest, "checkout", "-b", branch, "origin/"+branch)
		}
		return checkoutDoneMsg{repo: repo, branch: branch, err: err}
	}
}

func (m *dashModel) cloneCurrentPRRepo() tea.Cmd {
	var repo string
	switch m.section {
	case sectionDoNow:
		if m.cursor < len(m.doNow) {
			repo = m.doNow[m.cursor].Repo
		}
	case sectionWaiting:
		if m.cursor < len(m.waiting) {
			repo = m.waiting[m.cursor].Repo
		}
	case sectionReview:
		if m.cursor < len(m.review) {
			repo = m.review[m.cursor].Repo
		}
	case sectionNeedsAttention:
		if m.cursor < len(m.needsAttention) {
			repo = m.needsAttention[m.cursor].Repo
		}
	}
	if repo == "" {
		return nil
	}
	return m.cloneRepoCmd(repo)
}

func (m *dashModel) cloneRepoCmd(repo string) tea.Cmd {
	// Check if already exists locally
	localPath := m.findLocalRepo(repo)
	if localPath != "" {
		m.statusMsg = fmt.Sprintf("✓ Already cloned at %s", localPath)
		return nil
	}

	reposDir := m.cfg.Settings.ReposDir
	dest := reposDir + "/" + repo
	m.statusMsg = fmt.Sprintf("Cloning %s...", repo)
	return func() tea.Msg {
		// Double-check destination doesn't exist
		if isGitRepo(dest) {
			return cloneDoneMsg{repo: repo, path: dest, err: nil}
		}
		err := gh.CloneRepo(repo, dest)
		return cloneDoneMsg{repo: repo, path: dest, err: err}
	}
}

// findLocalRepo searches workspace scan dirs for a repo matching the given name
func (m *dashModel) findLocalRepo(repo string) string {
	parts := strings.Split(repo, "/")
	repoName := repo
	if len(parts) == 2 {
		repoName = parts[1]
	}

	// Fast path: check already-scanned workspace results first (no subprocess)
	for _, ws := range m.workspace {
		if ws.Name == repo || ws.Name == repoName {
			return ws.Path
		}
	}

	// Check explicit workspace repos mapping
	if path, ok := m.cfg.Workspace.Repos[repo]; ok {
		if isGitRepo(path) {
			return path
		}
	}

	// Check in the configured repos dir
	reposDir := m.cfg.Settings.ReposDir
	if reposDir != "" {
		path := reposDir + "/" + repo
		if isGitRepo(path) {
			return path
		}
		path = reposDir + "/" + repoName
		if isGitRepo(path) {
			return path
		}
	}

	// Slow path: scan dirs (subprocess per check)
	for _, dir := range m.cfg.Workspace.ScanDirs {
		path := dir + "/" + repoName
		if isGitRepo(path) {
			return path
		}
		if len(parts) == 2 {
			path = dir + "/" + parts[0] + "/" + parts[1]
			if isGitRepo(path) {
				return path
			}
		}
	}

	return ""
}
