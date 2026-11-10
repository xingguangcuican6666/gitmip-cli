package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

// Gitea has no GraphQL API, so the repository queries that gm builds as GraphQL
// fragments are answered here by GET /repos/{owner}/{repo} instead. The result
// is mapped onto the same Repository type the GraphQL path produces, so callers
// do not need to know which host they are talking to.
//
// The mapping is not lossless. Fields with no Gitea counterpart are left zero:
// Gitea reports no pushed-at timestamp, no code of conduct, no funding or
// contact links, no issue and pull request templates, no milestones, projects,
// assignable or mentionable users, and no Open Graph image. A caller that
// displays one of them shows absence rather than an error, which is the honest
// result: the host does not have the data.

// giteaRepository is the subset of Gitea's Repository schema this package maps.
type giteaRepository struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Website     string `json:"website"`
	HTMLURL     string `json:"html_url"`
	SSHURL      string `json:"ssh_url"`
	CloneURL    string `json:"clone_url"`

	Owner struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`

	DefaultBranch string     `json:"default_branch"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ArchivedAt    *time.Time `json:"archived_at"`

	Empty           bool `json:"empty"`
	Fork            bool `json:"fork"`
	Mirror          bool `json:"mirror"`
	Private         bool `json:"private"`
	Internal        bool `json:"internal"`
	Template        bool `json:"template"`
	Archived        bool `json:"archived"`
	HasCode         bool `json:"has_code"`
	HasIssues       bool `json:"has_issues"`
	HasWiki         bool `json:"has_wiki"`
	HasProjects     bool `json:"has_projects"`
	HasPullRequests bool `json:"has_pull_requests"`
	HasReleases     bool `json:"has_releases"`

	AllowMergeCommits bool `json:"allow_merge_commits"`
	AllowSquashMerge  bool `json:"allow_squash_merge"`
	AllowRebase       bool `json:"allow_rebase"`

	DefaultDeleteBranchAfterMerge bool `json:"default_delete_branch_after_merge"`

	Size            int64 `json:"size"`
	StarsCount      int   `json:"stars_count"`
	ForksCount      int   `json:"forks_count"`
	WatchersCount   int   `json:"watchers_count"`
	OpenIssuesCount int   `json:"open_issues_count"`
	OpenPRCounter   int   `json:"open_pr_counter"`

	Topics   []string `json:"topics"`
	Language string   `json:"language"`
	Licenses []string `json:"licenses"`

	Permissions struct {
		Admin bool `json:"admin"`
		Push  bool `json:"push"`
		Pull  bool `json:"pull"`
	} `json:"permissions"`

	Parent *giteaRepository `json:"parent"`
}

// toRepository maps a Gitea repository onto gm's Repository type.
func (g giteaRepository) toRepository() *Repository {
	r := &Repository{
		ID:                  strconv.FormatInt(g.ID, 10),
		DatabaseID:          g.ID,
		Name:                g.Name,
		NameWithOwner:       g.FullName,
		Description:         g.Description,
		HomepageURL:         g.Website,
		URL:                 g.HTMLURL,
		SSHURL:              g.SSHURL,
		CreatedAt:           g.CreatedAt,
		UpdatedAt:           g.UpdatedAt,
		ArchivedAt:          g.ArchivedAt,
		IsArchived:          g.Archived,
		IsEmpty:             g.Empty,
		IsFork:              g.Fork,
		IsMirror:            g.Mirror,
		IsPrivate:           g.Private,
		IsTemplate:          g.Template,
		IsInOrganization:    g.Owner.Type == "Organization",
		HasIssuesEnabled:    g.HasIssues,
		HasWikiEnabled:      g.HasWiki,
		HasProjectsEnabled:  g.HasProjects,
		MergeCommitAllowed:  g.AllowMergeCommits,
		SquashMergeAllowed:  g.AllowSquashMerge,
		RebaseMergeAllowed:  g.AllowRebase,
		DeleteBranchOnMerge: g.DefaultDeleteBranchAfterMerge,
		DiskUsage:           int(g.Size),
		ForkCount:           g.ForksCount,
		StargazerCount:      g.StarsCount,
		PrimaryLanguage:     nilIfEmptyLanguage(g.Language),
		Visibility:          visibilityFor(g.Private, g.Internal),
		Owner: RepositoryOwner{
			ID:    strconv.FormatInt(g.Owner.ID, 10),
			Login: g.Owner.Login,
		},
		DefaultBranchRef: BranchRef{Name: g.DefaultBranch},
	}

	r.Watchers.TotalCount = g.WatchersCount
	r.Issues.TotalCount = g.OpenIssuesCount
	r.PullRequests.TotalCount = g.OpenPRCounter

	if len(g.Licenses) > 0 {
		// Gitea reports license identifiers only, without a display name or
		// SPDX nickname, so the key doubles as the name.
		r.LicenseInfo = &RepositoryLicense{Key: g.Licenses[0], Name: g.Licenses[0]}
	}

	if len(g.Topics) > 0 {
		r.RepositoryTopics.Nodes = make([]struct {
			Topic RepositoryTopic
		}, 0, len(g.Topics))
		for _, topic := range g.Topics {
			r.RepositoryTopics.Nodes = append(r.RepositoryTopics.Nodes, struct {
				Topic RepositoryTopic
			}{Topic: RepositoryTopic{Name: topic}})
		}
	}

	// Gitea reports a single permission triple rather than GitHub's named
	// permission levels. push implies write access to repository content, and
	// admin implies everything below it.
	switch {
	case g.Permissions.Admin:
		r.ViewerPermission = "ADMIN"
		r.ViewerCanAdminister = true
	case g.Permissions.Push:
		r.ViewerPermission = "WRITE"
	case g.Permissions.Pull:
		r.ViewerPermission = "READ"
	}

	if g.Parent != nil {
		r.Parent = g.Parent.toRepository()
	}

	return r
}

func visibilityFor(private, internal bool) string {
	switch {
	case private:
		return "private"
	case internal:
		return "internal"
	default:
		return "public"
	}
}

func nilIfEmptyLanguage(name string) *CodingLanguage {
	if name == "" {
		return nil
	}
	return &CodingLanguage{Name: name}
}

// fetchGiteaRepository reads one repository from GET /repos/{owner}/{repo}.
//
// The fields argument is accepted for signature parity with the GraphQL path.
// Gitea returns the whole repository object in one response, so there is no
// partial selection to perform and no cost to ignoring it.
func fetchGiteaRepository(client *Client, repo ghrepo.Interface) (*Repository, error) {
	var raw giteaRepository
	path := fmt.Sprintf("repos/%s/%s", repo.RepoOwner(), repo.RepoName())
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	return InitRepoHostname(raw.toRepository(), repo.RepoHost()), nil
}

// lastCommitGitea returns the newest commit on the repository's default branch.
//
// GitHub answers this through GraphQL by walking
// repository.defaultBranchRef.target. Gitea exposes the default branch on the
// repository object and the commit list on GET /commits?sha=<branch>. Two
// round trips where GitHub needs one, but both are cheap and the default branch
// name is required to scope the listing.
func lastCommitGitea(client *Client, repo ghrepo.Interface) (*Commit, error) {
	giteaRepo, err := fetchGiteaRepository(client, repo)
	if err != nil {
		return nil, err
	}
	branch := giteaRepo.DefaultBranchRef.Name
	if branch == "" {
		return nil, fmt.Errorf("repository %s has no default branch", ghrepo.FullName(repo))
	}

	var commits []struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("repos/%s/%s/commits?limit=1&sha=%s",
		repo.RepoOwner(), repo.RepoName(), url.QueryEscape(branch))
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &commits); err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, fmt.Errorf("repository %s has no commits on branch %q", ghrepo.FullName(repo), branch)
	}

	return &Commit{OID: commits[0].SHA}, nil
}

// FetchRepository returns a repository, using Gitea's REST endpoint or GitHub's
// GraphQL query depending on the host.
func FetchRepository(client *Client, repo ghrepo.Interface, fields []string) (*Repository, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return fetchGiteaRepository(client, repo)
	}

	query := fmt.Sprintf(`query RepositoryInfo($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {%s}
	}`, RepositoryGraphQL(fields))

	variables := map[string]any{
		"owner": repo.RepoOwner(),
		"name":  repo.RepoName(),
	}

	var result struct {
		Repository *Repository
	}
	if err := client.GraphQL(repo.RepoHost(), query, variables, &result); err != nil {
		return nil, err
	}
	// The GraphQL API should have returned an error in case of a missing repository, but this isn't
	// guaranteed to happen when an authentication token with insufficient permissions is being used.
	if result.Repository == nil {
		return nil, GraphQLError{
			GraphQLError: &ghAPI.GraphQLError{
				Errors: []ghAPI.GraphQLErrorItem{{
					Type:    "NOT_FOUND",
					Message: fmt.Sprintf("Could not resolve to a Repository with the name '%s/%s'.", repo.RepoOwner(), repo.RepoName()),
				}},
			},
		}
	}

	return InitRepoHostname(result.Repository, repo.RepoHost()), nil
}

// SetRepositoryArchivedGitea sets a repository's archived flag.
//
// GitHub has archiveRepository and unarchiveRepository mutations, both addressed
// by node id. Gitea has neither: archived is a field of EditRepoOption, so both
// directions are the same PATCH with a boolean, and the repository is addressed by
// its path.
//
// The body carries only archived. Gitea treats an omitted field as no change, and
// sending the fields gm happened to have loaded would risk overwriting an edit
// made between the read and the write.
func SetRepositoryArchivedGitea(client *Client, repo ghrepo.Interface, archived bool) error {
	path := fmt.Sprintf("repos/%s/%s", repo.RepoOwner(), repo.RepoName())

	payload, err := json.Marshal(struct {
		Archived bool `json:"archived"`
	}{Archived: archived})
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}

	resp, err := client.Request(repo.RepoHost(), http.MethodPatch, path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	// A successful edit answers 200 with the repository body, which the caller does
	// not need: the command prints its own confirmation.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// repoNetworkGitea answers RepoNetwork for a Gitea host.
//
// GitHub resolves the whole network in one GraphQL query keyed repo_000,
// repo_001, ...; a Gitea host has no such query, so each repository is read
// separately from GET /repos/{owner}/{repo}. The response already carries the
// parent repository and the viewer's permission, which are the two relationships
// the network exists to discover.
//
// A repository that cannot be read is treated the way GitHub's NOT_FOUND is: it
// stays in place as a nil entry, so the result keeps the caller's ordering. Any
// other failure is returned with whatever was read so far, as the GraphQL path
// does.
func repoNetworkGitea(client *Client, repos []ghrepo.Interface) (RepoNetworkResult, error) {
	var result RepoNetworkResult
	if len(repos) == 0 {
		return result, nil
	}
	hostname := repos[0].RepoHost()

	// The viewer login is informational here. Gitea serves public repositories
	// without a token, so an anonymous run can still resolve the network; it just
	// does not know who the viewer is.
	login, err := CurrentLoginName(client, hostname)
	if err != nil && !isGiteaUnauthenticated(err) {
		return result, err
	}
	result.ViewerLogin = login

	for _, repo := range repos {
		r, err := fetchGiteaRepository(client, repo)
		if err != nil {
			if httpErr, ok := errors.AsType[HTTPError](err); ok && httpErr.StatusCode == http.StatusNotFound {
				result.Repositories = append(result.Repositories, nil)
				continue
			}
			return result, err
		}
		result.Repositories = append(result.Repositories, r)
	}

	return result, nil
}

// repoFindForksGitea answers RepoFindForks for a Gitea host.
//
// GitHub filters the fork list by affiliations [OWNER, COLLABORATOR] server-side.
// GET /repos/{owner}/{repo}/forks has no such filter: it lists every accessible
// fork, so a popular repository would take hundreds of pages of other people's
// read-only forks to reach the viewer's own. Searching the viewer's repositories
// instead is both the closer match and the cheaper one. /repos/search with
// uid=<viewer> returns the repositories the viewer owns or contributes to, and
// mode=fork narrows that to forks. Each result carries its parent, so the forks
// of the requested repository are picked out by full name.
//
// The ViewerCanPush check RepoFindForks applies to GitHub results is kept as a
// guard, for the same reason: the affiliation filter is expected to imply it.
func repoFindForksGitea(client *Client, repo ghrepo.Interface, limit int) ([]*Repository, error) {
	if limit <= 0 {
		return nil, nil
	}
	hostname := repo.RepoHost()

	viewerID, err := CurrentUserID(client, hostname)
	if err != nil {
		return nil, err
	}

	parentName := ghrepo.FullName(repo)
	var results []*Repository
	for page := 1; len(results) < limit; page++ {
		path := fmt.Sprintf("repos/search?uid=%s&mode=fork&limit=%d&page=%d",
			url.QueryEscape(viewerID), giteaPageSize, page)

		var raw struct {
			Data []giteaRepository `json:"data"`
		}
		if err := client.REST(hostname, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}

		for _, fork := range raw.Data {
			if fork.Parent == nil || !strings.EqualFold(fork.Parent.FullName, parentName) {
				continue
			}
			r := InitRepoHostname(fork.toRepository(), hostname)
			if !r.ViewerCanPush() {
				continue
			}
			results = append(results, r)
			if len(results) == limit {
				break
			}
		}

		if len(raw.Data) < giteaPageSize {
			break
		}
	}

	return results, nil
}
