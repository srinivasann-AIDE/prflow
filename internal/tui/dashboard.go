package tui

import (
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nagarjun226/prflow/internal/ai"
	"github.com/nagarjun226/prflow/internal/cache"
	"github.com/nagarjun226/prflow/internal/config"
	"github.com/nagarjun226/prflow/internal/gh"
)

type section int

const (
	sectionDoNow section = iota
	sectionWaiting
	sectionReview
	sectionWorkspace
	sectionNeedsAttention
	sectionCount // used for modulo navigation
)

func (s section) String() string {
	switch s {
	case sectionDoNow:
		return "⚡ Do Now"
	case sectionWaiting:
		return "⏳ Waiting"
	case sectionReview:
		return "👀 Review"
	case sectionWorkspace:
		return "📂 Workspace"
	case sectionNeedsAttention:
		return "🔔 Needs Attention Again"
	}
	return ""
}

type viewMode int

const (
	viewList viewMode = iota
	viewDetail
	viewSearch
	viewReply
)

type dashModel struct {
	cfg      *config.Config
	db       *cache.DB
	username string

	// Navigation
	section  section
	cursor   int
	viewMode viewMode

	// Data
	doNow          []cache.CachedPR
	waiting        []cache.CachedPR
	review         []cache.CachedPR
	needsAttention []cache.CachedPR
	workspace      []RepoStatus

	// Detail view
	detailPR      *cache.CachedPR
	detailThreads []gh.ReviewThread
	threadCursor  int

	// Search mode
	searchQuery   string
	searchResults []string
	searchCursor  int
	searching     bool

	// Reply mode
	replyText     string
	replyThreadID string

	// AI analysis
	aiAnalysis  *ai.PRAnalysis
	aiThread    *ai.ThreadAnalysis
	aiLoading   bool
	aiAvailable bool

	// Stale branch delete state
	deleteStalePending bool // true when waiting for 'd' confirmation

	// Nudge state
	nudgePending  bool   // true when waiting for confirmation
	nudgeReviewer string // reviewer login to nudge
	nudgeWaitDays int    // days waiting

	// State
	loading    bool
	lastSync   time.Time
	width      int
	height     int
	err        string
	statusMsg  string // temporary status message (git ops feedback)
	spinner    int
	spinFrames []string
}

type syncDoneMsg struct {
	doNow          []cache.CachedPR
	waiting        []cache.CachedPR
	review         []cache.CachedPR
	needsAttention []cache.CachedPR
	err            error
}

type workspaceScanMsg struct {
	repos []RepoStatus
}

type detailLoadedMsg struct {
	pr      *cache.CachedPR
	threads []gh.ReviewThread
	err     error
}

type gitOpDoneMsg struct {
	msg string
	err error
}

type searchResultsMsg struct {
	repos []string
	err   error
}

type cloneDoneMsg struct {
	repo string
	path string
	err  error
}

type checkoutDoneMsg struct {
	repo   string
	branch string
	err    error
}

type aiAnalysisDoneMsg struct {
	analysis *ai.PRAnalysis
	err      error
}

type aiThreadDoneMsg struct {
	analysis *ai.ThreadAnalysis
	err      error
}

type prActionDoneMsg struct {
	action string
	err    error
}

type dashTickMsg time.Time

func RunDashboard(cfg *config.Config) error {
	cfg.Validate()

	db, err := cache.Open()
	if err != nil {
		return fmt.Errorf("failed to open cache: %w", err)
	}
	defer db.Close()

	username, _ := gh.CheckAuth()

	SetTheme(cfg.Settings.Theme)

	m := dashModel{
		cfg:         cfg,
		db:          db,
		username:    username,
		loading:     true,
		aiAvailable: ai.Available(),
		spinFrames:  []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func dashTickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return dashTickMsg(t)
	})
}

func (m dashModel) Init() tea.Cmd {
	return tea.Batch(
		syncPRs(m.db, m.cfg, m.username),
		scanWorkspace(m.cfg),
		dashTickCmd(),
	)
}

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case dashTickMsg:
		m.spinner = (m.spinner + 1) % len(m.spinFrames)
		return m, dashTickCmd()

	case tea.KeyMsg:
		// Search mode handles its own keys
		if m.viewMode == viewSearch {
			return m.updateSearch(msg)
		}

		// Reply mode handles its own keys
		if m.viewMode == viewReply {
			return m.updateReply(msg)
		}

		// Clear status message on any keypress (except confirmation keys)
		key := msg.String()
		if key != "m" && key != "d" && key != "n" {
			m.statusMsg = ""
		}
		// Reset delete-stale confirmation unless the key is 'd'
		if key != "d" {
			m.deleteStalePending = false
		}
		// Reset nudge confirmation unless the key is 'n'
		if key != "n" {
			m.nudgePending = false
			m.nudgeReviewer = ""
			m.nudgeWaitDays = 0
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.viewMode == viewDetail {
				m.viewMode = viewList
				m.detailPR = nil
				m.aiAnalysis = nil
				m.aiThread = nil
				return m, nil
			}
			return m, tea.Quit
		case "esc":
			if m.viewMode == viewDetail {
				m.viewMode = viewList
				m.detailPR = nil
				m.aiAnalysis = nil
				m.aiThread = nil
				return m, nil
			}
		case "tab":
			m.section = (m.section + 1) % sectionCount
			m.cursor = 0
			m.viewMode = viewList
		case "shift+tab":
			m.section = (m.section + sectionCount - 1) % sectionCount
			m.cursor = 0
			m.viewMode = viewList
		case "up", "k":
			if m.viewMode == viewDetail {
				if m.threadCursor > 0 {
					m.threadCursor--
				}
			} else if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.viewMode == viewDetail {
				// Count unresolved threads only
				unresolvedCount := 0
				for _, t := range m.detailThreads {
					if !t.IsResolved {
						unresolvedCount++
					}
				}
				maxThread := unresolvedCount - 1
				if maxThread < 0 {
					maxThread = 0
				}
				if m.threadCursor < maxThread {
					m.threadCursor++
				}
			} else {
				max := m.currentListLen() - 1
				if max < 0 {
					max = 0
				}
				if m.cursor < max {
					m.cursor++
				}
			}
		case "enter":
			if m.viewMode == viewList {
				return m.openDetail()
			}
		case "o":
			m.openInBrowser()
		case "c":
			// Checkout PR branch locally
			if m.section != sectionWorkspace {
				return m, m.checkoutPRCmd()
			}
		case "a":
			// Approve PR (only in detail view, not draft)
			if m.viewMode == viewDetail && m.detailPR != nil {
				pr := m.detailPR
				if pr.IsDraft {
					m.statusMsg = "✗ Cannot approve draft PR"
					return m, nil
				}
				return m, func() tea.Msg {
					err := gh.ApprovePR(pr.Repo, pr.Number, "")
					return prActionDoneMsg{action: "approved", err: err}
				}
			}
		case "m":
			// Merge PR (only in detail view, not draft, requires confirmation)
			if m.viewMode == viewDetail && m.detailPR != nil {
				pr := m.detailPR
				if pr.IsDraft {
					m.statusMsg = "✗ Cannot merge draft PR"
					return m, nil
				}
				mergeMethod := m.cfg.Settings.MergeMethod
				if mergeMethod == "" {
					mergeMethod = "squash"
				}
				confirmMsg := fmt.Sprintf("⚠️ Press 'm' again to confirm merge (%s)", mergeMethod)
				if m.statusMsg == confirmMsg {
					m.statusMsg = "Merging..."
					return m, func() tea.Msg {
						err := gh.MergePR(pr.Repo, pr.Number, mergeMethod, false)
						return prActionDoneMsg{action: "merged", err: err}
					}
				} else {
					m.statusMsg = confirmMsg
					return m, nil
				}
			}
		case "r":
			// Resolve thread (only in detail view with threads)
			if m.viewMode == viewDetail && len(m.detailThreads) > 0 {
				unresolvedIdx := 0
				for _, t := range m.detailThreads {
					if t.IsResolved {
						continue
					}
					if unresolvedIdx == m.threadCursor {
						threadID := t.ID
						// Validate threadID before calling GraphQL
						if threadID == "" {
							m.statusMsg = "✗ Thread ID is empty"
							return m, nil
						}
						return m, func() tea.Msg {
							err := gh.ResolveThread(threadID)
							return prActionDoneMsg{action: "resolved thread", err: err}
						}
					}
					unresolvedIdx++
				}
			}
		case "A":
			// AI analysis (check cache first)
			if m.aiAvailable && m.viewMode == viewDetail && m.detailPR != nil {
				pr := m.detailPR
				// Check cache first (1 hour TTL)
				if m.db != nil {
					cached, err := m.db.GetAIAnalysis(pr.Repo, pr.Number, 1*time.Hour)
					if err == nil && cached != nil {
						m.aiAnalysis = &ai.PRAnalysis{
							Summary:       cached.Summary,
							ActionNeeded:  cached.ActionNeeded,
							ReviewSummary: cached.ReviewSummary,
							RiskLevel:     cached.RiskLevel,
							BlockedBy:     cached.BlockedBy,
						}
						// Parse suggested fixes from JSON
						if cached.SuggestedFixes != "" {
							json.Unmarshal([]byte(cached.SuggestedFixes), &m.aiAnalysis.SuggestedFixes)
						}
						m.statusMsg = "✓ AI analysis (cached)"
						return m, nil
					}
				}
				m.aiLoading = true
				m.aiAnalysis = nil
				m.aiThread = nil
				repoPath := m.findLocalRepo(pr.Repo)
				return m, func() tea.Msg {
					analysis, err := ai.AnalyzePR(pr.Repo, pr.Number, repoPath)
					return aiAnalysisDoneMsg{analysis: analysis, err: err}
				}
			} else if m.aiAvailable && m.viewMode == viewDetail && len(m.detailThreads) > 0 {
				// Analyze the selected thread
				m.aiLoading = true
				m.aiThread = nil
				pr := m.detailPR
				repoPath := m.findLocalRepo(pr.Repo)
				unresolvedIdx := 0
				for _, t := range m.detailThreads {
					if t.IsResolved {
						continue
					}
					if unresolvedIdx == m.threadCursor {
						thread := t
						return m, func() tea.Msg {
							analysis, err := ai.AnalyzeThread(pr.Repo, pr.Number, thread, repoPath)
							return aiThreadDoneMsg{analysis: analysis, err: err}
						}
					}
					unresolvedIdx++
				}
			}
		case "/":
			// Enter search mode (search org repos to clone)
			m.viewMode = viewSearch
			m.searchQuery = ""
			m.searchResults = nil
			m.searchCursor = 0
			m.searching = false
			return m, nil
		case "n":
			// Nudge stale reviewer (only in list view for waiting/doNow sections)
			if m.viewMode == viewList && (m.section == sectionWaiting || m.section == sectionDoNow) {
				var pr *cache.CachedPR
				switch m.section {
				case sectionWaiting:
					if m.cursor < len(m.waiting) {
						pr = &m.waiting[m.cursor]
					}
				case sectionDoNow:
					if m.cursor < len(m.doNow) {
						pr = &m.doNow[m.cursor]
					}
				}
				if pr == nil {
					return m, nil
				}

				if m.nudgePending && m.nudgeReviewer != "" {
					// Second press — confirm and send nudge
					reviewer := m.nudgeReviewer
					waitDays := m.nudgeWaitDays
					repo := pr.Repo
					number := pr.Number
					db := m.db
					m.nudgePending = false
					m.nudgeReviewer = ""
					m.nudgeWaitDays = 0
					m.statusMsg = fmt.Sprintf("Nudging @%s...", reviewer)
					return m, func() tea.Msg {
						err := gh.NudgeReviewer(repo, number, reviewer, waitDays)
						if err != nil {
							return prActionDoneMsg{action: fmt.Sprintf("nudge @%s", reviewer), err: err}
						}
						db.RecordNudge(repo, number, reviewer)
						return prActionDoneMsg{action: fmt.Sprintf("nudged @%s on #%d", reviewer, number), err: nil}
					}
				}

				// First press — find stalest reviewer and check cooldown
				statuses := CalculateReviewerStatus(&pr.PR)
				if len(statuses) == 0 {
					m.statusMsg = "No reviewers to nudge"
					return m, nil
				}
				stalest := statuses[0]
				if !m.db.CanNudge(pr.Repo, pr.Number, stalest.Login, 24) {
					m.statusMsg = fmt.Sprintf("@%s was already nudged within 24h", stalest.Login)
					return m, nil
				}
				m.nudgePending = true
				m.nudgeReviewer = stalest.Login
				m.nudgeWaitDays = stalest.WaitDays
				m.statusMsg = fmt.Sprintf("Nudge @%s? Press 'n' again to confirm", stalest.Login)
				return m, nil
			}
		case "C":
			// Clone the repo of the selected PR
			if m.section != sectionWorkspace {
				return m, m.cloneCurrentPRRepo()
			}
		case "R":
			// Context-aware: Reply in detail view, Refresh in list view
			if m.viewMode == viewDetail && len(m.detailThreads) > 0 && m.detailPR != nil {
				// Enter reply mode for the selected thread
				unresolvedIdx := 0
				for _, t := range m.detailThreads {
					if t.IsResolved {
						continue
					}
					if unresolvedIdx == m.threadCursor {
						threadID := t.ID
						if threadID == "" {
							m.statusMsg = "✗ Thread ID is empty"
							return m, nil
						}
						// Enter reply input mode
						m.viewMode = viewReply
						m.replyThreadID = threadID
						m.replyText = ""
						// Pre-fill with AI draft if available
						if m.aiThread != nil && m.aiThread.DraftReply != "" {
							m.replyText = m.aiThread.DraftReply
						}
						return m, nil
					}
					unresolvedIdx++
				}
			} else {
				// List view: Refresh
				m.loading = true
				m.err = ""
				return m, tea.Batch(syncPRs(m.db, m.cfg, m.username), scanWorkspace(m.cfg))
			}
		case "p":
			if m.section == sectionWorkspace {
				return m, m.gitPullCmd()
			}
		case "P":
			if m.section == sectionWorkspace {
				return m, m.gitPushCmd()
			}
		case "f":
			if m.section == sectionWorkspace {
				m.statusMsg = "Fetching all repos..."
				return m, m.fetchAllCmd()
			}
		case "d":
			if m.section == sectionWorkspace && m.viewMode == viewList {
				if m.cursor < len(m.workspace) {
					ws := &m.workspace[m.cursor]
					if len(ws.StaleBranches) == 0 {
						m.statusMsg = "No stale branches to delete"
						return m, nil
					}
					if m.deleteStalePending {
						// Second press: perform deletion
						m.deleteStalePending = false
						path := ws.Path
						branches := ws.StaleBranches
						return m, func() tea.Msg {
							deleted, err := DeleteStaleBranches(path, branches)
							if err != nil {
								return gitOpDoneMsg{msg: fmt.Sprintf("Deleted %d stale branches (some failed)", deleted), err: nil}
							}
							return gitOpDoneMsg{msg: fmt.Sprintf("Deleted %d stale branches", deleted)}
						}
					} else {
						// First press: show confirmation
						m.deleteStalePending = true
						m.statusMsg = fmt.Sprintf("Delete %d stale branches? Press 'd' again", len(ws.StaleBranches))
						return m, nil
					}
				}
			}
		}

	case syncDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.doNow = msg.doNow
			m.waiting = msg.waiting
			m.review = msg.review
			m.needsAttention = msg.needsAttention
			m.lastSync = time.Now()
			m.err = ""
			// Link workspace repos to their open PRs by branch name
			m.linkWorkspacePRs()
		}

	case workspaceScanMsg:
		m.workspace = msg.repos
		// Re-link after workspace scan (PRs may already be loaded)
		m.linkWorkspacePRs()

	case detailLoadedMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.detailPR = msg.pr
			m.detailThreads = msg.threads
			m.viewMode = viewDetail
			m.threadCursor = 0
		}

	case gitOpDoneMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("✗ %s: %v", msg.msg, msg.err)
		} else {
			m.statusMsg = fmt.Sprintf("✓ %s", msg.msg)
		}
		// Re-scan workspace after git ops
		return m, scanWorkspace(m.cfg)

	case searchResultsMsg:
		m.searching = false
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Search failed: %v", msg.err)
		} else {
			m.searchResults = msg.repos
			m.searchCursor = 0
		}

	case cloneDoneMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("✗ Clone %s failed: %v", msg.repo, msg.err)
		} else {
			m.statusMsg = fmt.Sprintf("✓ Cloned %s → %s", msg.repo, msg.path)
			m.viewMode = viewList
		}
		return m, scanWorkspace(m.cfg)

	case checkoutDoneMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("✗ Checkout failed: %v", msg.err)
		} else {
			m.statusMsg = fmt.Sprintf("✓ Checked out %s on %s", msg.branch, msg.repo)
		}
		return m, scanWorkspace(m.cfg)

	case aiAnalysisDoneMsg:
		m.aiLoading = false
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("✗ AI analysis failed: %v", msg.err)
		} else {
			m.aiAnalysis = msg.analysis
			m.statusMsg = "✓ AI analysis complete"
			// Cache the result
			if m.db != nil && m.detailPR != nil && msg.analysis != nil {
				fixesJSON, _ := json.Marshal(msg.analysis.SuggestedFixes)
				m.db.UpsertAIAnalysis(m.detailPR.Repo, m.detailPR.Number, &cache.CachedAIAnalysis{
					Summary:        msg.analysis.Summary,
					ActionNeeded:   msg.analysis.ActionNeeded,
					ReviewSummary:  msg.analysis.ReviewSummary,
					RiskLevel:      msg.analysis.RiskLevel,
					SuggestedFixes: string(fixesJSON),
					BlockedBy:      msg.analysis.BlockedBy,
				})
			}
		}

	case aiThreadDoneMsg:
		m.aiLoading = false
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("✗ AI thread analysis failed: %v", msg.err)
		} else {
			m.aiThread = msg.analysis
			m.statusMsg = "✓ Thread analysis complete"
		}

	case prActionDoneMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("✗ %s failed: %v", msg.action, msg.err)
		} else {
			m.statusMsg = fmt.Sprintf("✓ PR %s", msg.action)
			// Reload PR detail to reflect changes
			if m.detailPR != nil {
				return m, loadDetail(m.detailPR)
			}
		}
	}

	return m, nil
}

func (m dashModel) currentListLen() int {
	switch m.section {
	case sectionDoNow:
		return len(m.doNow)
	case sectionWaiting:
		return len(m.waiting)
	case sectionReview:
		return len(m.review)
	case sectionWorkspace:
		return len(m.workspace)
	case sectionNeedsAttention:
		return len(m.needsAttention)
	}
	return 0
}
