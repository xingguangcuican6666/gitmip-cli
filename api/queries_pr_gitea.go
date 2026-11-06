package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API, so pull request lookups are answered here from REST
// and mapped onto the same PullRequest type the GraphQL path produces.
//
// A Gitea pull request arrives whole from GET /repos/{o}/{r}/pulls/{index}, so
// the fields argument that drives GitHub's field selection is ignored: there is
// nothing to project. Callers keep passing it so both hosts share a signature.
//
// What cannot be mapped is left zero rather than invented. Gitea reports no
// review decision on the pull request itself, no merge queue, no auto-merge, no
// merge state status, no branch protection rule, and no author association on a
// comment or review. It does report mergeable as a boolean, which is coarser than
// GitHub's three-way MERGEABLE / CONFLICTING / UNKNOWN: true and false map onto
// the first two, and there is no way to say "not computed yet", so a caller that
// treats UNKNOWN specially will not see it.
//
// Commits, files and reviews are separate endpoints rather than parts of the
// pull request response, so they are fetched only when a caller asks.

// giteaPullRequest is the subset of Gitea's PullRequest schema gh maps.
type giteaPullRequest struct {
	ID       int64  `json:"id"`
	Number   int64  `json:"number"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	HTMLURL  string `json:"html_url"`
	State    string `json:"state"`
	Draft    bool   `json:"draft"`
	IsLocked bool   `json:"is_locked"`
	PinOrder int64  `json:"pin_order"`

	Comments       int64  `json:"comments"`
	ReviewComments int64  `json:"review_comments"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	ClosedAt       string `json:"closed_at"`

	Mergeable       bool   `json:"mergeable"`
	Merged          bool   `json:"merged"`
	MergedAt        string `json:"merged_at"`
	MergeCommitSHA  string `json:"merge_commit_sha"`
	MergeBase       string `json:"merge_base"`
	Additions       int64  `json:"additions"`
	Deletions       int64  `json:"deletions"`
	ChangedFiles    int64  `json:"changed_files"`
	AllowMaintainer bool   `json:"allow_maintainer_edit"`

	User      giteaUser   `json:"user"`
	MergedBy  *giteaUser  `json:"merged_by"`
	Assignees []giteaUser `json:"assignees"`

	Labels []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Color       string `json:"color"`
	} `json:"labels"`

	Milestone *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"milestone"`

	Base giteaBranchInfo `json:"base"`
	Head giteaBranchInfo `json:"head"`
}

// giteaBranchInfo is Gitea's PRBranchInfo, which names a side of a pull request.
type giteaBranchInfo struct {
	Label  string `json:"label"`
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	RepoID int64  `json:"repo_id"`
	Repo   *struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    *struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repo"`
}

// FetchPullRequestGitea returns one pull request by index.
func FetchPullRequestGitea(client *Client, repo ghrepo.Interface, number int) (*PullRequest, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", repo.RepoOwner(), repo.RepoName(), number)

	var raw giteaPullRequest
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}

	return mapGiteaPullRequest(raw, repo.RepoHost()), nil
}

func mapGiteaPullRequest(raw giteaPullRequest, hostname string) *PullRequest {
	pr := &PullRequest{
		ID:                  strconv.FormatInt(raw.ID, 10),
		FullDatabaseID:      strconv.FormatInt(raw.ID, 10),
		Number:              int(raw.Number),
		Title:               raw.Title,
		Body:                raw.Body,
		URL:                 raw.HTMLURL,
		State:               giteaPullRequestState(raw),
		Closed:              strings.EqualFold(raw.State, "closed"),
		IsDraft:             raw.Draft,
		BaseRefName:         raw.Base.Ref,
		BaseRefOid:          raw.Base.SHA,
		HeadRefName:         raw.Head.Ref,
		HeadRefOid:          raw.Head.SHA,
		Mergeable:           giteaMergeable(raw),
		Additions:           int(raw.Additions),
		Deletions:           int(raw.Deletions),
		ChangedFiles:        int(raw.ChangedFiles),
		MaintainerCanModify: raw.AllowMaintainer,
		CreatedAt:           parseGiteaTime(raw.CreatedAt),
		UpdatedAt:           parseGiteaTime(raw.UpdatedAt),
		Author: Author{
			ID:    strconv.FormatInt(raw.User.ID, 10),
			Login: raw.User.Login,
			Name:  raw.User.FullName,
		},
		Comments: Comments{TotalCount: int(raw.Comments)},
	}

	if raw.ClosedAt != "" {
		closed := parseGiteaTime(raw.ClosedAt)
		pr.ClosedAt = &closed
	}
	if raw.MergedAt != "" {
		merged := parseGiteaTime(raw.MergedAt)
		pr.MergedAt = &merged
	}
	if raw.MergedBy != nil {
		pr.MergedBy = &Author{
			ID:    strconv.FormatInt(raw.MergedBy.ID, 10),
			Login: raw.MergedBy.Login,
			Name:  raw.MergedBy.FullName,
		}
	}
	if raw.MergeCommitSHA != "" {
		pr.MergeCommit = &Commit{OID: raw.MergeCommitSHA}
	}

	for _, a := range raw.Assignees {
		pr.Assignees.Nodes = append(pr.Assignees.Nodes, GitHubUser{
			ID:         strconv.FormatInt(a.ID, 10),
			Login:      a.Login,
			Name:       a.FullName,
			DatabaseID: a.ID,
		})
	}
	pr.Assignees.TotalCount = len(pr.Assignees.Nodes)

	for _, l := range raw.Labels {
		pr.Labels.Nodes = append(pr.Labels.Nodes, IssueLabel{
			ID:          strconv.FormatInt(l.ID, 10),
			Name:        l.Name,
			Description: l.Description,
			Color:       l.Color,
		})
	}
	pr.Labels.TotalCount = len(pr.Labels.Nodes)

	if raw.Milestone != nil {
		pr.Milestone = &Milestone{
			Number: int(raw.Milestone.ID),
			Title:  raw.Milestone.Title,
		}
	}

	// HeadLabel is what `gh pr checkout` and the cross-repository display use,
	// and it needs the head owner and repository populated even when the pull
	// request is not cross-repository.
	if raw.Head.Repo != nil {
		if raw.Head.Repo.Owner != nil {
			pr.HeadRepositoryOwner = Owner{
				ID:    strconv.FormatInt(raw.Head.Repo.Owner.ID, 10),
				Login: raw.Head.Repo.Owner.Login,
			}
		}
		pr.HeadRepository = &PRRepository{
			ID:            strconv.FormatInt(raw.Head.Repo.ID, 10),
			Name:          raw.Head.Repo.Name,
			NameWithOwner: raw.Head.Repo.FullName,
			DatabaseID:    raw.Head.Repo.ID,
		}
	}
	if raw.Base.Repo != nil {
		pr.Repository = &PRRepository{
			ID:            strconv.FormatInt(raw.Base.Repo.ID, 10),
			Name:          raw.Base.Repo.Name,
			NameWithOwner: raw.Base.Repo.FullName,
			DatabaseID:    raw.Base.Repo.ID,
		}
	}

	pr.IsCrossRepository = raw.Head.RepoID != raw.Base.RepoID && raw.Head.RepoID != 0 && raw.Base.RepoID != 0

	return pr
}

// giteaPullRequestState maps Gitea's state onto GitHub's, which has three values
// where Gitea has two.
//
// A merged pull request is closed in Gitea and reports merged alongside it, so
// MERGED is derived rather than read. Without it `gh pr list --state merged`
// would filter on a state nothing ever carries and report no results.
func giteaPullRequestState(raw giteaPullRequest) string {
	if raw.Merged {
		return "MERGED"
	}
	return strings.ToUpper(raw.State)
}

// giteaMergeable translates Gitea's boolean into gh's three-way value.
//
// Gitea has no "not computed yet" state, so a pull request is either mergeable
// or conflicting and there is nothing to report as UNKNOWN. Leaving it UNKNOWN on
// a false would make `gh pr merge` refuse a pull request Gitea has already
// declared unmergeable without saying why.
func giteaMergeable(raw giteaPullRequest) PullRequestMergeable {
	if raw.Merged {
		return PullRequestMergeableMergeable
	}
	if raw.Mergeable {
		return PullRequestMergeableMergeable
	}
	return PullRequestMergeableConflicting
}

// giteaChangedFile is Gitea's ChangedFile, one entry per path in a diff.
type giteaChangedFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
	Changes   int64  `json:"changes"`
}

// PullRequestFilesGitea lists the files a pull request changes, paged.
//
// Gitea's status values are the words GitHub uses in lowercase, so each is
// upper-cased to the ChangeType a consumer compares against. A status Gitea does
// not send is passed through unchanged rather than mapped to a wrong word, since
// a consumer that does not recognize it reports absence instead of claiming a
// change kind that did not happen.
func PullRequestFilesGitea(client *Client, repo ghrepo.Interface, number int) ([]PullRequestFile, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/files", repo.RepoOwner(), repo.RepoName(), number)

	var files []PullRequestFile
	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s?limit=%d&page=%d", path, giteaPageSize, page)

		var raw []giteaChangedFile
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath, nil, &raw); err != nil {
			return nil, err
		}

		for _, f := range raw {
			files = append(files, PullRequestFile{
				Path:       f.Filename,
				Additions:  int(f.Additions),
				Deletions:  int(f.Deletions),
				ChangeType: giteaChangeType(f.Status),
			})
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	return files, nil
}

func giteaChangeType(status string) string {
	switch strings.ToLower(status) {
	case "added":
		return "ADDED"
	case "modified":
		return "MODIFIED"
	case "removed", "deleted":
		return "REMOVED"
	case "renamed":
		return "RENAMED"
	case "copied":
		return "COPIED"
	case "changed":
		return "CHANGED"
	case "unchanged":
		return "UNCHANGED"
	default:
		return strings.ToUpper(status)
	}
}

// giteaReviewCommit is Gitea's commit entry within a pull request.
type giteaReviewCommit struct {
	SHA    string `json:"sha"`
	Commit *struct {
		Author *struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"author"`
		Message string `json:"message"`
	} `json:"commit"`
	Author *giteaUser `json:"author"`
}

// PullRequestCommitsGitea lists the commits a pull request contains, paged.
//
// Only the oid and the author are mapped. Gitea reports no status check rollup
// per commit, so a consumer that reads one sees absence rather than a fabricated
// "no checks" result.
func PullRequestCommitsGitea(client *Client, repo ghrepo.Interface, number int) ([]PullRequestCommit, int, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/commits", repo.RepoOwner(), repo.RepoName(), number)

	var commits []PullRequestCommit
	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s?limit=%d&page=%d", path, giteaPageSize, page)

		var raw []giteaReviewCommit
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath, nil, &raw); err != nil {
			return nil, 0, err
		}

		for _, c := range raw {
			pc := PullRequestCommit{
				Commit: PullRequestCommitCommit{OID: c.SHA},
			}
			if c.Commit != nil && c.Commit.Author != nil {
				node := struct {
					Name  string
					Email string
					User  GitHubUser
				}{Name: c.Commit.Author.Name, Email: c.Commit.Author.Email}
				if c.Author != nil {
					node.User = GitHubUser{
						ID:         strconv.FormatInt(c.Author.ID, 10),
						Login:      c.Author.Login,
						Name:       c.Author.FullName,
						DatabaseID: c.Author.ID,
					}
				}
				pc.Commit.Authors.Nodes = append(pc.Commit.Authors.Nodes, node)
			}
			commits = append(commits, pc)
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	return commits, len(commits), nil
}

// giteaPullReview is Gitea's PullReview.
type giteaPullReview struct {
	ID          int64     `json:"id"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	CommitID    string    `json:"commit_id"`
	HTMLURL     string    `json:"html_url"`
	SubmittedAt string    `json:"submitted_at"`
	Dismissed   bool      `json:"dismissed"`
	User        giteaUser `json:"user"`
}

// PullRequestReviewsGitea lists the reviews on a pull request, paged.
//
// Gitea's state vocabulary differs from GitHub's: REQUEST_CHANGES is what GitHub
// calls CHANGES_REQUESTED and COMMENT is what it calls COMMENTED. A dismissed
// review is reported as DISMISSED, which Gitea carries as a separate flag rather
// than as a state, and a review that is both is dismissed: the reviewer's
// original verdict no longer stands.
//
// REQUEST_REVIEW is Gitea's record that a review was asked for, not a review. It
// is skipped rather than reported as a review with no verdict, because a consumer
// counting reviews would count a request as one.
func PullRequestReviewsGitea(client *Client, repo ghrepo.Interface, number int) (PullRequestReviews, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", repo.RepoOwner(), repo.RepoName(), number)

	var reviews PullRequestReviews
	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s?limit=%d&page=%d", path, giteaPageSize, page)

		var raw []giteaPullReview
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath, nil, &raw); err != nil {
			return PullRequestReviews{}, err
		}

		for _, r := range raw {
			if r.State == "REQUEST_REVIEW" {
				continue
			}

			review := PullRequestReview{
				ID:    strconv.FormatInt(r.ID, 10),
				Body:  r.Body,
				URL:   r.HTMLURL,
				State: giteaReviewState(r),
				Author: CommentAuthor{
					Login: r.User.Login,
				},
				Commit: Commit{OID: r.CommitID},
			}
			if r.SubmittedAt != "" {
				submitted := parseGiteaTime(r.SubmittedAt)
				review.SubmittedAt = &submitted
			}
			reviews.Nodes = append(reviews.Nodes, review)
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	reviews.TotalCount = len(reviews.Nodes)
	return reviews, nil
}

func giteaReviewState(r giteaPullReview) string {
	if r.Dismissed {
		return "DISMISSED"
	}
	switch r.State {
	case "REQUEST_CHANGES":
		return "CHANGES_REQUESTED"
	case "COMMENT":
		return "COMMENTED"
	case "APPROVED", "PENDING":
		return r.State
	default:
		return strings.ToUpper(r.State)
	}
}

// GiteaPageSize is the per-page maximum gitea.com enforces through
// MAX_RESPONSE_ITEMS. A larger request is capped server-side, so asking for more
// would silently yield fewer results than a paging loop expects.
const GiteaPageSize = giteaPageSize

// ListPullRequestsGitea returns one page of pull requests.
//
// Gitea filters by base branch, by author and by state, and takes one state per
// request. It has no head branch filter, no draft filter and no assignee filter,
// so a caller that needs one of those filters the result locally.
//
// The rows are the same object GET /pulls/{index} returns minus the diff counts,
// so additions, deletions and changedFiles are zero here. A caller that needs
// those fetches the pull request by index; the preload functions do that for
// reviews, comments, files and commits.
func ListPullRequestsGitea(client *Client, repo ghrepo.Interface, in ListPullRequestsGiteaInput) ([]PullRequest, int, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls", repo.RepoOwner(), repo.RepoName())

	query := url.Values{}
	query.Set("state", in.State)
	// Gitea's sort enum has no "newest". recentupdate is the closest to the
	// CREATED_AT ordering the GitHub path uses, and an invalid value is ignored
	// rather than rejected, which would silently change the order a caller relies
	// on.
	query.Set("sort", "recentupdate")
	query.Set("limit", strconv.Itoa(giteaPageSize))
	query.Set("page", strconv.Itoa(in.Page))
	if in.BaseBranch != "" {
		query.Set("base_branch", in.BaseBranch)
	}
	if in.Author != "" {
		query.Set("poster", in.Author)
	}

	resp, err := client.Request(repo.RepoHost(), http.MethodGet, path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var raw []giteaPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, 0, err
	}

	pullRequests := make([]PullRequest, 0, len(raw))
	for _, entry := range raw {
		pullRequests = append(pullRequests, *mapGiteaPullRequest(entry, repo.RepoHost()))
	}

	// The count arrives only in a header, and it is what lets a caller report
	// "19 open pull requests" while showing a page of them.
	total, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))

	return pullRequests, total, nil
}

// ListPullRequestsGiteaInput is the filtering Gitea's pull request list can do
// server-side. Everything else a caller filters locally.
type ListPullRequestsGiteaInput struct {
	// State is one of Gitea's own values: open, closed or all.
	State      string
	BaseBranch string
	Author     string
	// Page is 1-based.
	Page int
}

// BranchDeleteRemoteGitea deletes a branch on a Gitea host.
//
// GitHub's git refs endpoint does not exist on Gitea, which deletes a branch
// through its own path. The branch name goes in the path rather than in a
// reference prefix, so a name containing a slash is escaped as one segment.
func BranchDeleteRemoteGitea(client *Client, repo ghrepo.Interface, branch string) error {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "git", "branches", branch)
	if err != nil {
		return err
	}
	return client.REST(repo.RepoHost(), http.MethodDelete, path.String(), nil, nil)
}
