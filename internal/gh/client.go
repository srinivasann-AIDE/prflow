package gh

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// CheckAuth verifies gh CLI is authenticated and returns the username
func CheckAuth() (string, error) {
	// First try: gh auth status (works on most versions)
	out, err := run("auth", "status")
	if err != nil {
		return "", fmt.Errorf("not authenticated: %s", out)
	}

	// Try to extract username from various output formats
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// Format: "Logged in to github.com account username ..."
		if strings.Contains(line, "account") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "account" && i+1 < len(parts) {
					username := strings.Trim(parts[i+1], "().,")
					if username != "" {
						return username, nil
					}
				}
			}
		}
		// Format: "Logged in to github.com as username ..."
		if strings.Contains(line, " as ") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "as" && i+1 < len(parts) {
					username := strings.Trim(parts[i+1], "().,")
					if username != "" {
						return username, nil
					}
				}
			}
		}
	}

	// Fallback: use gh api to get current user
	apiOut, apiErr := run("api", "user", "--jq", ".login")
	if apiErr == nil && apiOut != "" {
		return strings.TrimSpace(apiOut), nil
	}

	return "user", nil // authenticated but couldn't parse username
}

// PR represents a pull request
type PR struct {
	Number            int            `json:"number"`
	Title             string         `json:"title"`
	State             string         `json:"state"`
	URL               string         `json:"url"`
	HeadRefName       string         `json:"headRefName"`
	BaseRefName       string         `json:"baseRefName"`
	Author            Author         `json:"author"`
	CreatedAt         string         `json:"createdAt"`
	UpdatedAt         string         `json:"updatedAt"`
	ReviewDecision    string         `json:"reviewDecision"`
	Mergeable         string         `json:"mergeable"`
	IsDraft           bool           `json:"isDraft"`
	Repository        RepoRef        `json:"repository"`
	Reviews           Reviews        `json:"reviews"`
	ReviewRequests    ReviewRequests `json:"reviewRequests"`
	StatusCheckRollup []StatusCheck  `json:"statusCheckRollup"`
	Comments          Comments       `json:"comments"`
}

type Author struct {
	Login string `json:"login"`
}

type RepoRef struct {
	NameWithOwner string `json:"nameWithOwner"`
}

// Reviews handles both array format and {nodes:[]} format
type Reviews struct {
	Nodes []Review
}

func (r *Reviews) UnmarshalJSON(data []byte) error {
	var arr []Review
	if err := json.Unmarshal(data, &arr); err == nil {
		r.Nodes = arr
		return nil
	}
	var obj struct {
		Nodes []Review `json:"nodes"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		r.Nodes = obj.Nodes
		return nil
	}
	r.Nodes = nil
	return nil
}

type Review struct {
	Author      Author `json:"author"`
	State       string `json:"state"`
	SubmittedAt string `json:"submittedAt"`
}

// ReviewRequests handles both array format and {nodes:[]} format
type ReviewRequests struct {
	Nodes []ReviewRequest
}

func (r *ReviewRequests) UnmarshalJSON(data []byte) error {
	var arr []ReviewRequest
	if err := json.Unmarshal(data, &arr); err == nil {
		r.Nodes = arr
		return nil
	}
	var obj struct {
		Nodes []ReviewRequest `json:"nodes"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		r.Nodes = obj.Nodes
		return nil
	}
	r.Nodes = nil
	return nil
}

type ReviewRequest struct {
	RequestedReviewer Author `json:"requestedReviewer"`
}

type StatusCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// Comments handles both array format (gh pr view) and {nodes:[]} format (GraphQL)
type Comments struct {
	Items []Comment
}

func (c *Comments) UnmarshalJSON(data []byte) error {
	// Try array first (gh pr view format)
	var arr []Comment
	if err := json.Unmarshal(data, &arr); err == nil {
		c.Items = arr
		return nil
	}
	// Try {nodes:[]} format (GraphQL)
	var obj struct {
		Nodes []Comment `json:"nodes"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		c.Items = obj.Nodes
		return nil
	}
	// Give up gracefully
	c.Items = nil
	return nil
}

type Comment struct {
	Author    Author `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	URL       string `json:"url"`
}

// ReviewThread from GraphQL
type ReviewThread struct {
	ID         string          `json:"id"`
	Path       string          `json:"path"`
	Line       int             `json:"line"`
	IsResolved bool            `json:"isResolved"`
	Comments   []ThreadComment `json:"comments"`
}

type ThreadComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	URL       string `json:"url"`
}

// SearchMyPRs returns all open PRs authored by the current user
func SearchMyPRs() ([]PR, error) {
	// gh search prs has limited --json fields. Use only valid ones.
	out, err := run("search", "prs",
		"--author=@me",
		"--state=open",
		"--limit", "100",
		"--json", "number,title,state,url,repository,createdAt,updatedAt",
	)
	if err != nil {
		// Fallback: try using gh api directly
		return searchPRsViaAPI("author:@me")
	}
	var results []struct {
		Number     int     `json:"number"`
		Title      string  `json:"title"`
		State      string  `json:"state"`
		URL        string  `json:"url"`
		CreatedAt  string  `json:"createdAt"`
		UpdatedAt  string  `json:"updatedAt"`
		Repository RepoRef `json:"repository"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		return nil, fmt.Errorf("parse PRs failed: %w", err)
	}

	var prs []PR
	for _, r := range results {
		prs = append(prs, PR{
			Number:     r.Number,
			Title:      r.Title,
			State:      r.State,
			URL:        r.URL,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
			Repository: r.Repository,
		})
	}
	return prs, nil
}

// SearchReviewRequests returns PRs where review is requested from current user
func SearchReviewRequests() ([]PR, error) {
	out, err := run("search", "prs",
		"--review-requested=@me",
		"--state=open",
		"--limit", "100",
		"--json", "number,title,state,url,repository,createdAt,updatedAt",
	)
	if err != nil {
		return searchPRsViaAPI("review-requested:@me")
	}
	var results []struct {
		Number     int     `json:"number"`
		Title      string  `json:"title"`
		State      string  `json:"state"`
		URL        string  `json:"url"`
		CreatedAt  string  `json:"createdAt"`
		UpdatedAt  string  `json:"updatedAt"`
		Repository RepoRef `json:"repository"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		return nil, fmt.Errorf("parse review requests failed: %w", err)
	}

	var prs []PR
	for _, r := range results {
		prs = append(prs, PR{
			Number:     r.Number,
			Title:      r.Title,
			State:      r.State,
			URL:        r.URL,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
			Repository: r.Repository,
		})
	}
	return prs, nil
}

// searchPRsViaAPI is a fallback that uses GitHub search API directly
func searchPRsViaAPI(qualifier string) ([]PR, error) {
	out, err := run("api", "search/issues",
		"-X", "GET",
		"-f", fmt.Sprintf("q=is:pr is:open %s", qualifier),
		"-f", "per_page=100",
		"--jq", ".items[] | {number, title, state, html_url, created_at, updated_at, repository_url}",
	)
	if err != nil {
		return nil, fmt.Errorf("API search failed: %w", err)
	}
	if out == "" {
		return []PR{}, nil
	}

	// Parse line-by-line JSON objects
	var prs []PR
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item struct {
			Number        int    `json:"number"`
			Title         string `json:"title"`
			State         string `json:"state"`
			HTMLURL       string `json:"html_url"`
			CreatedAt     string `json:"created_at"`
			UpdatedAt     string `json:"updated_at"`
			RepositoryURL string `json:"repository_url"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		// Extract repo name from URL: https://api.github.com/repos/org/repo
		repoName := ""
		parts := strings.Split(item.RepositoryURL, "/repos/")
		if len(parts) == 2 {
			repoName = parts[1]
		}
		prs = append(prs, PR{
			Number:     item.Number,
			Title:      item.Title,
			State:      item.State,
			URL:        item.HTMLURL,
			CreatedAt:  item.CreatedAt,
			UpdatedAt:  item.UpdatedAt,
			Repository: RepoRef{NameWithOwner: repoName},
		})
	}
	return prs, nil
}

// ListPRsForRepo lists open PRs for a specific repo (more reliable than search)
func ListPRsForRepo(repo string) ([]PR, error) {
	out, err := run("pr", "list",
		"-R", repo,
		"--state", "open",
		"--json", "number,title,state,url,headRefName,baseRefName,author,createdAt,updatedAt,reviewDecision,mergeable,isDraft,reviews,reviewRequests,statusCheckRollup",
		"--limit", "50",
	)
	if err != nil {
		return nil, fmt.Errorf("list PRs for %s failed: %w", repo, err)
	}
	var prs []PR
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("parse PRs failed: %w", err)
	}
	for i := range prs {
		prs[i].Repository.NameWithOwner = repo
	}
	return prs, nil
}

// GetPRDetail gets full PR details for a specific PR
func GetPRDetail(repo string, number int) (*PR, error) {
	out, err := run("pr", "view",
		fmt.Sprintf("%d", number),
		"-R", repo,
		"--json", "number,title,state,url,headRefName,baseRefName,author,createdAt,updatedAt,reviewDecision,mergeable,isDraft,reviews,reviewRequests,statusCheckRollup,comments",
	)
	if err != nil {
		return nil, fmt.Errorf("get PR detail failed: %w", err)
	}
	var pr PR
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return nil, fmt.Errorf("parse PR detail failed: %w", err)
	}
	pr.Repository.NameWithOwner = repo
	return &pr, nil
}

// GetReviewThreads fetches review threads via GraphQL
func GetReviewThreads(repo string, number int) ([]ReviewThread, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format: %s", repo)
	}
	// Escape quotes in repo owner/name to prevent GraphQL injection
	owner := strings.ReplaceAll(parts[0], `"`, `\"`)
	name := strings.ReplaceAll(parts[1], `"`, `\"`)
	query := fmt.Sprintf(`query {
		repository(owner: "%s", name: "%s") {
			pullRequest(number: %d) {
				reviewThreads(first: 50) {
					nodes {
						id
						path
						line
						isResolved
						comments(first: 20) {
							nodes {
								author { login }
								body
								createdAt
								url
							}
						}
					}
				}
			}
		}
	}`, owner, name, number)

	out, err := run("api", "graphql", "-f", fmt.Sprintf("query=%s", query))
	if err != nil {
		return nil, fmt.Errorf("graphql query failed: %w", err)
	}

	var result struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							Path       string `json:"path"`
							Line       int    `json:"line"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									Author    Author `json:"author"`
									Body      string `json:"body"`
									CreatedAt string `json:"createdAt"`
									URL       string `json:"url"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}

	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return nil, fmt.Errorf("parse graphql result failed: %w", err)
	}

	var threads []ReviewThread
	for _, t := range result.Data.Repository.PullRequest.ReviewThreads.Nodes {
		thread := ReviewThread{
			ID:         t.ID,
			Path:       t.Path,
			Line:       t.Line,
			IsResolved: t.IsResolved,
		}
		for _, c := range t.Comments.Nodes {
			thread.Comments = append(thread.Comments, ThreadComment{
				Author:    c.Author.Login,
				Body:      c.Body,
				CreatedAt: c.CreatedAt,
				URL:       c.URL,
			})
		}
		threads = append(threads, thread)
	}
	return threads, nil
}

// ListUserRepos lists repos the user has access to
func ListUserRepos() ([]string, error) {
	out, err := run("repo", "list", "--json", "nameWithOwner", "--limit", "100")
	if err != nil {
		return nil, fmt.Errorf("list repos failed: %w", err)
	}
	var repos []struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal([]byte(out), &repos); err != nil {
		return nil, fmt.Errorf("parse repos failed: %w", err)
	}
	result := make([]string, len(repos))
	for i, r := range repos {
		result[i] = r.NameWithOwner
	}
	return result, nil
}

// GetPRDiff returns the diff for a PR
func GetPRDiff(repo string, number int) (string, error) {
	out, err := run("pr", "diff", fmt.Sprintf("%d", number), "-R", repo)
	if err != nil {
		return "", fmt.Errorf("get diff failed: %w", err)
	}
	return out, nil
}

// SearchOrgRepos searches repos in an org matching a query
func SearchOrgRepos(query string) ([]string, error) {
	// Search across all repos the user has access to
	out, err := run("search", "repos",
		query,
		"--json", "nameWithOwner",
		"--limit", "30",
	)
	if err != nil {
		// Fallback: list org repos filtered
		return searchOrgReposFallback(query)
	}
	var repos []struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal([]byte(out), &repos); err != nil {
		return nil, fmt.Errorf("parse search results failed: %w", err)
	}
	result := make([]string, len(repos))
	for i, r := range repos {
		result[i] = r.NameWithOwner
	}
	return result, nil
}

func searchOrgReposFallback(query string) ([]string, error) {
	out, err := run("repo", "list", "--json", "nameWithOwner", "--limit", "200")
	if err != nil {
		return nil, err
	}
	var repos []struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal([]byte(out), &repos); err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var matches []string
	for _, r := range repos {
		if strings.Contains(strings.ToLower(r.NameWithOwner), q) {
			matches = append(matches, r.NameWithOwner)
		}
	}
	return matches, nil
}

// CloneRepo clones a repo into the given directory
func CloneRepo(repo, destDir string) error {
	_, err := run("repo", "clone", repo, destDir)
	return err
}

// CheckoutPR checks out a PR branch locally
func CheckoutPR(repo string, number int) error {
	_, err := run("pr", "checkout", fmt.Sprintf("%d", number), "-R", repo)
	return err
}

// SearchReviewedPRs returns open PRs where the current user has submitted a review
// (but is not the author)
func SearchReviewedPRs() ([]PR, error) {
	out, err := run("search", "prs",
		"--reviewed-by=@me",
		"--state=open",
		"--limit", "50",
		"--sort", "updated",
		"--json", "number,title,state,url,repository,createdAt,updatedAt",
	)
	if err != nil {
		return nil, err
	}
	var results []struct {
		Number     int     `json:"number"`
		Title      string  `json:"title"`
		State      string  `json:"state"`
		URL        string  `json:"url"`
		CreatedAt  string  `json:"createdAt"`
		UpdatedAt  string  `json:"updatedAt"`
		Repository RepoRef `json:"repository"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		return nil, err
	}
	var prs []PR
	for _, r := range results {
		prs = append(prs, PR{
			Number:     r.Number,
			Title:      r.Title,
			State:      r.State,
			URL:        r.URL,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
			Repository: r.Repository,
		})
	}
	return prs, nil
}

// OpenInBrowser opens a URL in the default browser (cross-platform)
func OpenInBrowser(url string) error {
	// Use gh browse which is cross-platform
	// Fallback to OS-specific commands
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("open", url)
	}
	return cmd.Start()
}

// ApprovePR approves a pull request
func ApprovePR(repo string, number int, body string) error {
	args := []string{"pr", "review", fmt.Sprintf("%d", number), "-R", repo, "--approve"}
	if body != "" {
		args = append(args, "-b", body)
	}
	_, err := run(args...)
	return err
}

// MergePR merges a pull request with the specified strategy
// strategy can be: "merge" (default), "squash", or "rebase"
func MergePR(repo string, number int, strategy string, autoMerge bool) error {
	args := []string{"pr", "merge", fmt.Sprintf("%d", number), "-R", repo}

	switch strategy {
	case "squash":
		args = append(args, "--squash")
	case "rebase":
		args = append(args, "--rebase")
	default:
		args = append(args, "--merge")
	}

	if autoMerge {
		args = append(args, "--auto")
	}

	_, err := run(args...)
	return err
}

// ResolveThread marks a review thread as resolved using GraphQL
func ResolveThread(threadID string) error {
	query := `mutation($threadId: ID!) {
		resolveReviewThread(input: { threadId: $threadId }) {
			thread {
				id
				isResolved
			}
		}
	}`

	_, err := run("api", "graphql", "-F", fmt.Sprintf("threadId=%s", threadID), "-f", fmt.Sprintf("query=%s", query))
	return err
}

// UnresolveThread marks a review thread as unresolved using GraphQL
func UnresolveThread(threadID string) error {
	query := `mutation($threadId: ID!) {
		unresolveReviewThread(input: { threadId: $threadId }) {
			thread {
				id
				isResolved
			}
		}
	}`

	_, err := run("api", "graphql", "-F", fmt.Sprintf("threadId=%s", threadID), "-f", fmt.Sprintf("query=%s", query))
	return err
}

// ReplyToComment adds a reply to a review thread using GraphQL
func ReplyToComment(repo string, prNumber int, commentID string, body string) error {
	// Need to get the pull request ID first (GraphQL node ID, not number)
	prIDQuery := fmt.Sprintf(`
	query {
		repository(owner: "%s", name: "%s") {
			pullRequest(number: %d) {
				id
			}
		}
	}`, repoOwner(repo), repoName(repo), prNumber)

	prIDOut, err := run("api", "graphql", "-f", fmt.Sprintf("query=%s", prIDQuery))
	if err != nil {
		return fmt.Errorf("failed to get PR ID: %w", err)
	}

	// Extract PR ID from JSON response
	var prIDResp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ID string `json:"id"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(prIDOut), &prIDResp); err != nil {
		return fmt.Errorf("failed to parse PR ID: %w", err)
	}
	prID := prIDResp.Data.Repository.PullRequest.ID

	// Now add the reply using the comment ID and PR ID
	mutation := `mutation($pullRequestId: ID!, $pullRequestReviewThreadId: ID!, $body: String!) {
		addPullRequestReviewThreadReply(input: {
			pullRequestId: $pullRequestId,
			pullRequestReviewThreadId: $pullRequestReviewThreadId,
			body: $body
		}) {
			comment {
				id
				body
			}
		}
	}`

	_, err = run("api", "graphql",
		"-f", fmt.Sprintf("query=%s", mutation),
		"-F", fmt.Sprintf("pullRequestId=%s", prID),
		"-F", fmt.Sprintf("pullRequestReviewThreadId=%s", commentID),
		"-F", fmt.Sprintf("body=%s", body))

	return err
}

func repoOwner(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) == 2 {
		return parts[0]
	}
	return ""
}

func repoName(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return repo
}

// NudgeReviewer posts a friendly nudge comment on a PR mentioning the reviewer.
func NudgeReviewer(repo string, number int, reviewer string, waitDays int) error {
	body := fmt.Sprintf("@%s friendly nudge — this PR has been waiting for your review for %d days", reviewer, waitDays)
	_, err := run("pr", "comment", fmt.Sprintf("%d", number), "-R", repo, "-b", body)
	return err
}

// run is defined in runner.go
