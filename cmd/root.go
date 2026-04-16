package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nagarjun226/prflow/internal/ai"
	"github.com/nagarjun226/prflow/internal/cache"
	"github.com/nagarjun226/prflow/internal/config"
	"github.com/nagarjun226/prflow/internal/deps"
	"github.com/nagarjun226/prflow/internal/gh"
	"github.com/nagarjun226/prflow/internal/tui"
	"github.com/nagarjun226/prflow/internal/watch"
)

// Version info set via ldflags at build time.
var Version, Commit, Date string

func Execute() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(VersionString())
			return nil
		case "setup":
			return tui.RunOnboarding()
		case "sync":
			return runSync()
		case "ls":
			jsonFlag := hasFlag(os.Args[2:], "--json")
			return runListTo(os.Stdout, jsonFlag)
		case "config":
			return runConfig()
		case "doctor":
			return runDoctor()
		case "watch":
			return runWatch()
		case "open":
			return runOpen()
		default:
			fmt.Printf("Unknown command: %s\n", os.Args[1])
			printUsage()
			return nil
		}
	}

	// Default: launch TUI
	cfg, err := config.Load()
	if err != nil || len(cfg.Repos) == 0 {
		// First run — launch onboarding
		if err := tui.RunOnboarding(); err != nil {
			return err
		}
		// Reload config after onboarding
		cfg, err = config.Load()
		if err != nil || len(cfg.Repos) == 0 {
			fmt.Println("Setup complete. Run 'prflow' again to launch the dashboard.")
			return nil
		}
	}
	return tui.RunDashboard(cfg)
}

func runSync() error {
	fmt.Println("Syncing PRs...")
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("no config found, run 'prflow setup' first")
	}
	cfg.Validate()

	db, err := cache.Open()
	if err != nil {
		return fmt.Errorf("failed to open cache: %w", err)
	}
	defer db.Close()

	username, _ := gh.CheckAuth()
	return syncToCache(db, cfg, username)
}

// syncToCache fetches all open PRs from GitHub, writes them to the DB, and
// purges any rows that are no longer open. Shared by runSync and runListTo.
func syncToCache(db *cache.DB, cfg *config.Config, username string) error {
	myPRs, _ := gh.SearchMyPRs()
	repoSet := make(map[string]bool)
	for _, pr := range myPRs {
		if pr.Repository.NameWithOwner != "" {
			repoSet[pr.Repository.NameWithOwner] = true
		}
	}
	for _, repo := range cfg.Repos {
		repoSet[repo] = true
	}

	reviewPRs, _ := gh.SearchReviewRequests()
	for _, pr := range reviewPRs {
		if pr.Repository.NameWithOwner != "" {
			repoSet[pr.Repository.NameWithOwner] = true
		}
	}

	openKeys := make(map[string]bool)
	synced := 0
	for repo := range repoSet {
		repoPRs, err := gh.ListPRsForRepo(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", repo, err)
			continue
		}
		for i := range repoPRs {
			pr := &repoPRs[i]
			section := classifyPR(pr, username)
			db.UpsertPR(pr, repo, section)
			openKeys[fmt.Sprintf("%s#%d", repo, pr.Number)] = true
			synced++
		}
		fmt.Fprintf(os.Stderr, "  ✓ %s (%d PRs)\n", repo, len(repoPRs))
	}

	for i := range reviewPRs {
		pr := &reviewPRs[i]
		key := fmt.Sprintf("%s#%d", pr.Repository.NameWithOwner, pr.Number)
		if !openKeys[key] {
			db.UpsertPR(pr, pr.Repository.NameWithOwner, "review")
			openKeys[key] = true
			synced++
		}
	}

	db.PurgeClosedPRs(openKeys)
	fmt.Fprintf(os.Stderr, "Sync complete: %d PRs cached across %d repos.\n", synced, len(repoSet))
	return nil
}

// classifyPR determines which section a PR belongs in.
func classifyPR(pr *gh.PR, username string) string {
	if strings.EqualFold(pr.Author.Login, username) {
		if pr.IsDraft {
			return "waiting"
		}
		if pr.ReviewDecision == "CHANGES_REQUESTED" {
			return "do_now"
		}
		if pr.Mergeable == "CONFLICTING" {
			return "do_now"
		}
		for _, check := range pr.StatusCheckRollup {
			if check.Conclusion == "FAILURE" || check.Conclusion == "TIMED_OUT" || check.Conclusion == "ACTION_REQUIRED" {
				return "do_now"
			}
		}
		if pr.ReviewDecision == "APPROVED" {
			ciOK := true
			for _, check := range pr.StatusCheckRollup {
				if check.Conclusion != "SUCCESS" && check.Conclusion != "NEUTRAL" &&
					check.Conclusion != "SKIPPED" && check.Status != "COMPLETED" {
					ciOK = false
					break
				}
			}
			if ciOK {
				return "do_now"
			}
			return "waiting"
		}
		return "waiting"
	}
	return "review"
}

// openArgs holds the parsed result of a prflow open argument.
type openArgs struct {
	Repo   string
	Number int
}

// parseOpenArgs parses the argument to prflow open.
func parseOpenArgs(arg string) (openArgs, error) {
	if arg == "" {
		return openArgs{}, nil
	}
	if idx := strings.Index(arg, "#"); idx >= 0 {
		numStr := arg[idx+1:]
		repo := arg[:idx]
		if numStr == "" {
			return openArgs{}, fmt.Errorf("missing PR number after #")
		}
		n, err := strconv.Atoi(numStr)
		if err != nil || n <= 0 {
			return openArgs{}, fmt.Errorf("invalid PR number: %s", numStr)
		}
		return openArgs{Repo: repo, Number: n}, nil
	}
	if strings.Contains(arg, "/") {
		return openArgs{Repo: arg, Number: 0}, nil
	}
	return openArgs{}, fmt.Errorf("invalid argument: %s (expected org/repo, #number, or org/repo#number)", arg)
}

// repoFromRemote infers the "org/repo" from the current directory's git remote.
var repoFromRemote = func() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("could not detect repo from git remote: %w", err)
	}
	return parseRepoFromURL(strings.TrimSpace(string(out)))
}

// parseRepoFromURL extracts "org/repo" from a git remote URL.
func parseRepoFromURL(rawURL string) (string, error) {
	u := rawURL
	if strings.HasPrefix(u, "git@") {
		u = strings.TrimPrefix(u, "git@")
		if i := strings.Index(u, ":"); i >= 0 {
			u = u[i+1:]
		}
		u = strings.TrimSuffix(u, ".git")
		if strings.Contains(u, "/") {
			return u, nil
		}
	}
	u = strings.TrimSuffix(u, ".git")
	parts := strings.Split(u, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
	}
	return "", fmt.Errorf("could not parse repo from URL: %s", rawURL)
}

func runOpen() error {
	arg := ""
	if len(os.Args) > 2 {
		arg = os.Args[2]
	}

	parsed, err := parseOpenArgs(arg)
	if err != nil {
		return err
	}

	repo := parsed.Repo
	if repo == "" {
		inferred, err := repoFromRemote()
		if err != nil {
			return err
		}
		repo = inferred
	}

	var url string
	if parsed.Number > 0 {
		url = fmt.Sprintf("https://github.com/%s/pull/%d", repo, parsed.Number)
	} else {
		url = fmt.Sprintf("https://github.com/%s/pulls", repo)
	}

	fmt.Printf("Opening %s\n", url)
	return gh.OpenInBrowser(url)
}

// hasFlag checks whether flag appears in args.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// jsonPR is the JSON-output representation of a cached PR.
type jsonPR struct {
	Repo           string `json:"repo"`
	Number         int    `json:"number"`
	Title          string `json:"title"`
	ReviewDecision string `json:"review_decision"`
	Mergeable      string `json:"mergeable"`
	UpdatedAt      string `json:"updated_at"`
}

// jsonOutput is the top-level JSON structure for prflow ls --json.
type jsonOutput struct {
	DoNow          []jsonPR `json:"do_now"`
	Waiting        []jsonPR `json:"waiting"`
	Review         []jsonPR `json:"review"`
	NeedsAttention []jsonPR `json:"needs_attention"`
}

func toJSONPRs(prs []cache.CachedPR) []jsonPR {
	out := make([]jsonPR, 0, len(prs))
	for _, p := range prs {
		out = append(out, jsonPR{
			Repo:           p.Repo,
			Number:         p.Number,
			Title:          p.Title,
			ReviewDecision: p.ReviewDecision,
			Mergeable:      p.Mergeable,
			UpdatedAt:      p.UpdatedAt,
		})
	}
	return out
}

// runListTo writes the PR list to w. When jsonMode is true it outputs JSON;
// otherwise it writes human-readable plaintext.
// It always syncs from GitHub first so the output is never stale.
func runListTo(w io.Writer, jsonMode bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("no config found, run 'prflow setup' first")
	}
	cfg.Validate()

	db, err := cache.Open()
	if err != nil {
		return fmt.Errorf("failed to open cache: %w", err)
	}
	defer db.Close()

	// Always fetch live from GitHub so `prflow ls` is never reading stale cache.
	if !jsonMode {
		fmt.Fprintln(w, "Syncing with GitHub...")
	}
	username, _ := gh.CheckAuth()
	if err := syncToCache(db, cfg, username); err != nil && !jsonMode {
		fmt.Fprintf(w, "Warning: sync error: %v\n", err)
	}

	sections := []struct {
		name string
		key  string
		icon string
	}{
		{"Do Now", "do_now", "⚡"},
		{"Waiting", "waiting", "⏳"},
		{"Review", "review", "👀"},
		{"Needs Attention", "needs_attention", "🔔"},
	}

	// Collect PRs per section.
	prsBySection := make(map[string][]cache.CachedPR)
	for _, sec := range sections {
		prs, err := db.GetPRsBySection(sec.key)
		if err != nil {
			prs = nil
		}
		prsBySection[sec.key] = prs
	}

	if jsonMode {
		out := jsonOutput{
			DoNow:          toJSONPRs(prsBySection["do_now"]),
			Waiting:        toJSONPRs(prsBySection["waiting"]),
			Review:         toJSONPRs(prsBySection["review"]),
			NeedsAttention: toJSONPRs(prsBySection["needs_attention"]),
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Plaintext output.
	total := 0
	for _, sec := range sections {
		prs := prsBySection[sec.key]
		if len(prs) == 0 {
			continue
		}
		total += len(prs)
		fmt.Fprintf(w, "\n%s %s (%d)\n", sec.icon, sec.name, len(prs))
		for _, pr := range prs {
			parts := strings.Split(pr.Repo, "/")
			repoShort := pr.Repo
			if len(parts) == 2 {
				repoShort = parts[1]
			}
			fmt.Fprintf(w, "  #%-5d %-15s %s\n", pr.Number, repoShort, pr.Title)
		}
	}

	if total == 0 {
		fmt.Fprintln(w, "No cached PRs. Run 'prflow sync' first.")
	}
	return nil
}

func runConfig() error {
	cfgPath := config.Path()
	fmt.Printf("Config: %s\n", cfgPath)
	fmt.Println("Open with: $EDITOR " + cfgPath)
	return nil
}

func runDoctor() error {
	fmt.Println(deps.PrintStatus())

	if ai.Available() {
		fmt.Println("🤖 AI features: ENABLED")
		fmt.Println("   Claude Code detected — PR analysis, review assistance, and auto-fix available.")
	} else {
		fmt.Println("🤖 AI features: DISABLED (optional)")
		fmt.Println("   Install Claude Code for AI-powered PR analysis:")
		fmt.Println("   npm install -g @anthropic-ai/claude-code")
		fmt.Println("   Then run: claude  (to complete auth)")
		fmt.Println("")
		fmt.Println("   Without it, PRFlow works as a standard PR dashboard.")
	}

	if err := deps.CheckRequired(); err != nil {
		fmt.Printf("\n⚠️  %v\n", err)
		return err
	}

	fmt.Println("\n✓ All required dependencies OK")
	return nil
}

func runWatch() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("no config found, run 'prflow setup' first")
	}

	// Parse optional interval from args (e.g. prflow watch 5m)
	interval := 2 * time.Minute
	if cfg.Settings.WatchInterval != "" {
		if d, err := time.ParseDuration(cfg.Settings.WatchInterval); err == nil {
			interval = d
		}
	}
	if len(os.Args) > 2 {
		if d, err := time.ParseDuration(os.Args[2]); err == nil {
			interval = d
		}
	}

	username, err := gh.CheckAuth()
	if err != nil {
		return fmt.Errorf("not authenticated: %w", err)
	}

	db, err := cache.Open()
	if err != nil {
		fmt.Printf("warning: cache unavailable: %v\n", err)
	}
	if db != nil {
		defer db.Close()
	}

	fmt.Printf("Watching PRs as %s (interval: %s). Press Ctrl+C to stop.\n", username, interval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w := watch.New(cfg, db, username, interval)
	return w.Run(ctx)
}

// VersionString returns a formatted version string.
func VersionString() string {
	v := Version
	if v == "" {
		v = "0.1.0"
	}
	s := fmt.Sprintf("prflow v%s", v)
	if Commit != "" || Date != "" {
		parts := ""
		if Commit != "" {
			parts += "commit " + Commit
		}
		if Date != "" {
			if parts != "" {
				parts += ", "
			}
			parts += "built " + Date
		}
		s += fmt.Sprintf(" (%s)", parts)
	}
	return s
}

func printUsage() {
	fmt.Println(`Usage: prflow [command]

Commands:
  (none)    Launch TUI dashboard
  setup     Run onboarding wizard
  sync      Force refresh PR cache
  ls        Quick list (no TUI)
  config    Show config path
  open      Open PR in browser (org/repo#42, #42, org/repo)
  doctor    Check dependencies (gh, git, claude)
  watch     Background mode with OS notifications
  version   Print version`)
}
