package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/nagarjun226/prflow/internal/gh"
)

type DB struct {
	db *sql.DB
}

type CachedPR struct {
	gh.PR
	Repo      string
	Section   string // do_now, waiting, review, done
	CIStatus  string
	FetchedAt time.Time
}

func Open() (*DB, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "prflow")
	os.MkdirAll(dir, 0755)
	dbPath := filepath.Join(dir, "prflow.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL")
	if err != nil {
		return nil, err
	}

	if err := migrate(db); err != nil {
		return nil, err
	}

	return &DB{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS prs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			repo TEXT NOT NULL,
			number INTEGER NOT NULL,
			title TEXT,
			state TEXT,
			author TEXT,
			branch TEXT,
			base_branch TEXT,
			url TEXT,
			created_at TEXT,
			updated_at TEXT,
			mergeable TEXT,
			review_decision TEXT,
			ci_status TEXT,
			is_draft INTEGER DEFAULT 0,
			section TEXT,
			raw_json TEXT,
			fetched_at TEXT,
			UNIQUE(repo, number)
		);

		CREATE TABLE IF NOT EXISTS review_threads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			repo TEXT NOT NULL,
			pr_number INTEGER NOT NULL,
			thread_id TEXT,
			path TEXT,
			line INTEGER,
			is_resolved INTEGER DEFAULT 0,
			last_author TEXT,
			last_body TEXT,
			needs_my_reply INTEGER DEFAULT 0,
			url TEXT,
			raw_json TEXT,
			UNIQUE(repo, pr_number, thread_id)
		);

		CREATE TABLE IF NOT EXISTS favorites (
			repo TEXT PRIMARY KEY,
			added_at TEXT
		);

		CREATE TABLE IF NOT EXISTS ai_analyses (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			repo TEXT NOT NULL,
			pr_number INTEGER NOT NULL,
			summary TEXT,
			action_needed TEXT,
			review_summary TEXT,
			risk_level TEXT,
			suggested_fixes TEXT,
			blocked_by TEXT,
			analyzed_at TEXT,
			UNIQUE(repo, pr_number)
		);

		CREATE TABLE IF NOT EXISTS nudge_log (
			repo TEXT,
			pr_number INTEGER,
			reviewer TEXT,
			nudged_at TEXT,
			UNIQUE(repo, pr_number, reviewer)
		);

		CREATE INDEX IF NOT EXISTS idx_prs_section_updated ON prs(section, updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_prs_repo_number ON prs(repo, number);
		CREATE INDEX IF NOT EXISTS idx_threads_repo_pr ON review_threads(repo, pr_number);
		CREATE INDEX IF NOT EXISTS idx_ai_repo_pr ON ai_analyses(repo, pr_number);
	`)
	return err
}

func (d *DB) UpsertPR(pr *gh.PR, repo, section string) error {
	raw, _ := json.Marshal(pr)
	ciStatus := "UNKNOWN"
	for _, check := range pr.StatusCheckRollup {
		if check.Conclusion == "FAILURE" {
			ciStatus = "FAILURE"
			break
		}
		if check.Status == "IN_PROGRESS" {
			ciStatus = "PENDING"
		}
		if check.Conclusion == "SUCCESS" && ciStatus != "PENDING" {
			ciStatus = "SUCCESS"
		}
	}

	_, err := d.db.Exec(`
		INSERT INTO prs (repo, number, title, state, author, branch, base_branch, url,
			created_at, updated_at, mergeable, review_decision, ci_status, is_draft, section, raw_json, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, number) DO UPDATE SET
			title=excluded.title, state=excluded.state, author=excluded.author,
			branch=excluded.branch, base_branch=excluded.base_branch, url=excluded.url,
			updated_at=excluded.updated_at, mergeable=excluded.mergeable,
			review_decision=excluded.review_decision, ci_status=excluded.ci_status,
			is_draft=excluded.is_draft, section=excluded.section, raw_json=excluded.raw_json,
			fetched_at=excluded.fetched_at
	`,
		repo, pr.Number, pr.Title, pr.State, pr.Author.Login,
		pr.HeadRefName, pr.BaseRefName, pr.URL,
		pr.CreatedAt, pr.UpdatedAt, pr.Mergeable, pr.ReviewDecision,
		ciStatus, pr.IsDraft, section, string(raw), time.Now().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetPRsBySection(section string) ([]CachedPR, error) {
	rows, err := d.db.Query(`
		SELECT repo, number, title, state, author, branch, base_branch, url,
			created_at, updated_at, mergeable, review_decision, ci_status, is_draft, section, fetched_at
		FROM prs WHERE section = ? ORDER BY updated_at DESC
	`, section)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prs []CachedPR
	for rows.Next() {
		var p CachedPR
		var isDraft int
		var fetchedAt string
		err := rows.Scan(
			&p.Repo, &p.Number, &p.Title, &p.State, &p.Author.Login,
			&p.HeadRefName, &p.BaseRefName, &p.URL,
			&p.CreatedAt, &p.UpdatedAt, &p.Mergeable, &p.PR.ReviewDecision,
			&p.CIStatus, &isDraft, &p.Section, &fetchedAt,
		)
		if err != nil {
			continue
		}
		p.IsDraft = isDraft == 1
		p.FetchedAt, _ = time.Parse(time.RFC3339, fetchedAt)
		prs = append(prs, p)
	}
	return prs, nil
}

func (d *DB) GetAllPRs() ([]CachedPR, error) {
	rows, err := d.db.Query(`
		SELECT repo, number, title, state, author, branch, base_branch, url,
			created_at, updated_at, mergeable, review_decision, ci_status, is_draft, section, fetched_at
		FROM prs WHERE state = 'OPEN' OR state = 'open' ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prs []CachedPR
	for rows.Next() {
		var p CachedPR
		var isDraft int
		var fetchedAt string
		err := rows.Scan(
			&p.Repo, &p.Number, &p.Title, &p.State, &p.Author.Login,
			&p.HeadRefName, &p.BaseRefName, &p.URL,
			&p.CreatedAt, &p.UpdatedAt, &p.Mergeable, &p.PR.ReviewDecision,
			&p.CIStatus, &isDraft, &p.Section, &fetchedAt,
		)
		if err != nil {
			continue
		}
		p.IsDraft = isDraft == 1
		p.FetchedAt, _ = time.Parse(time.RFC3339, fetchedAt)
		prs = append(prs, p)
	}
	return prs, nil
}

func (d *DB) AddFavorite(repo string) error {
	_, err := d.db.Exec(`
		INSERT OR IGNORE INTO favorites (repo, added_at) VALUES (?, ?)
	`, repo, time.Now().Format(time.RFC3339))
	return err
}

func (d *DB) RemoveFavorite(repo string) error {
	_, err := d.db.Exec(`DELETE FROM favorites WHERE repo = ?`, repo)
	return err
}

func (d *DB) GetFavorites() ([]string, error) {
	rows, err := d.db.Query(`SELECT repo FROM favorites ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var favs []string
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err == nil {
			favs = append(favs, repo)
		}
	}
	return favs, nil
}

// CachedAIAnalysis represents a cached AI analysis for a PR
type CachedAIAnalysis struct {
	Repo           string
	PRNumber       int
	Summary        string
	ActionNeeded   string
	ReviewSummary  string
	RiskLevel      string
	SuggestedFixes string // JSON array stored as string
	BlockedBy      string
	AnalyzedAt     time.Time
}

// UpsertAIAnalysis stores or updates an AI analysis for a PR
func (d *DB) UpsertAIAnalysis(repo string, prNumber int, analysis *CachedAIAnalysis) error {
	_, err := d.db.Exec(`
		INSERT INTO ai_analyses (repo, pr_number, summary, action_needed, review_summary,
			risk_level, suggested_fixes, blocked_by, analyzed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, pr_number) DO UPDATE SET
			summary=excluded.summary, action_needed=excluded.action_needed,
			review_summary=excluded.review_summary, risk_level=excluded.risk_level,
			suggested_fixes=excluded.suggested_fixes, blocked_by=excluded.blocked_by,
			analyzed_at=excluded.analyzed_at
	`, repo, prNumber, analysis.Summary, analysis.ActionNeeded, analysis.ReviewSummary,
		analysis.RiskLevel, analysis.SuggestedFixes, analysis.BlockedBy,
		time.Now().Format(time.RFC3339))
	return err
}

// GetAIAnalysis retrieves a cached AI analysis. Returns nil if not found or expired.
// maxAge controls how old a cached analysis can be (0 = no expiry).
func (d *DB) GetAIAnalysis(repo string, prNumber int, maxAge time.Duration) (*CachedAIAnalysis, error) {
	var a CachedAIAnalysis
	var analyzedAt string
	err := d.db.QueryRow(`
		SELECT repo, pr_number, summary, action_needed, review_summary,
			risk_level, suggested_fixes, blocked_by, analyzed_at
		FROM ai_analyses WHERE repo = ? AND pr_number = ?
	`, repo, prNumber).Scan(
		&a.Repo, &a.PRNumber, &a.Summary, &a.ActionNeeded, &a.ReviewSummary,
		&a.RiskLevel, &a.SuggestedFixes, &a.BlockedBy, &analyzedAt)
	if err != nil {
		return nil, err
	}
	a.AnalyzedAt, _ = time.Parse(time.RFC3339, analyzedAt)

	// Check expiry
	if maxAge > 0 && time.Since(a.AnalyzedAt) > maxAge {
		return nil, nil // expired
	}
	return &a, nil
}

// RecordNudge records that a reviewer was nudged on a PR.
func (d *DB) RecordNudge(repo string, prNumber int, reviewer string) error {
	_, err := d.db.Exec(`
		INSERT INTO nudge_log (repo, pr_number, reviewer, nudged_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(repo, pr_number, reviewer) DO UPDATE SET nudged_at=excluded.nudged_at
	`, repo, prNumber, reviewer, time.Now().Format(time.RFC3339))
	return err
}

// CanNudge returns true if the reviewer has not been nudged within the cooldown period.
func (d *DB) CanNudge(repo string, prNumber int, reviewer string, cooldownHours int) bool {
	var nudgedAt string
	err := d.db.QueryRow(`
		SELECT nudged_at FROM nudge_log
		WHERE repo = ? AND pr_number = ? AND reviewer = ?
	`, repo, prNumber, reviewer).Scan(&nudgedAt)
	if err != nil {
		// No record found — can nudge
		return true
	}
	t, err := time.Parse(time.RFC3339, nudgedAt)
	if err != nil {
		return true
	}
	return time.Since(t) >= time.Duration(cooldownHours)*time.Hour
}

// PurgeClosedPRs deletes cache rows for PRs not in the given set of open "repo#number" keys.
// Call this after a full sync so stale closed/merged PRs don't linger in the cache.
func (d *DB) PurgeClosedPRs(openKeys map[string]bool) error {
	rows, err := d.db.Query(`SELECT repo, number FROM prs`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var toDelete [][2]interface{}
	for rows.Next() {
		var repo string
		var number int
		if err := rows.Scan(&repo, &number); err != nil {
			continue
		}
		key := fmt.Sprintf("%s#%d", repo, number)
		if !openKeys[key] {
			toDelete = append(toDelete, [2]interface{}{repo, number})
		}
	}
	rows.Close()

	for _, pair := range toDelete {
		d.db.Exec(`DELETE FROM prs WHERE repo = ? AND number = ?`, pair[0], pair[1])
	}
	return nil
}

// OpenTestDB creates a temporary in-memory DB for testing from other packages.
func OpenTestDB(t interface{ TempDir() string }) (*DB, error) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}
