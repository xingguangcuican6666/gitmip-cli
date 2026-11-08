package list

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API, so repository listing is answered from REST. Two
// endpoints cover it: GET /users/{username} to resolve an owner handle to the
// numeric id Gitea's search endpoint requires, and GET /repos/search for the
// repositories themselves. When no owner is given, the authenticated user's id
// comes from GET /user.
//
// Gitea's search covers most of gm's filters server-side but not all of them.
// Language has no server-side filter, so it is applied to each page as it
// arrives, and the loop keeps paging until the limit is met rather than
// stopping at a page boundary. Several topics cannot be expressed either, since
// the topic filter takes the search keyword itself and there is only one
// keyword; asking for more than one returns an error rather than silently
// narrowing the result.

// giteaRepoSearchResponse is Gitea's wrapper for repository search results. It
// is not a bare array, unlike the issue search endpoint.
type giteaRepoSearchResponse struct {
	OK   bool                      `json:"ok"`
	Data []giteaRepositoryListItem `json:"data"`
}

// giteaRepositoryListItem is the subset of Gitea's Repository schema listing
// needs. It is decoded here rather than through api.Repository because the JSON
// names differ from GitHub's GraphQL names in both spelling and type.
type giteaRepositoryListItem struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Website     string `json:"website"`
	HTMLURL     string `json:"html_url"`
	SSHURL      string `json:"ssh_url"`

	Owner struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`

	DefaultBranch string  `json:"default_branch"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	ArchivedAt    *string `json:"archived_at"`

	Empty    bool `json:"empty"`
	Fork     bool `json:"fork"`
	Mirror   bool `json:"mirror"`
	Private  bool `json:"private"`
	Internal bool `json:"internal"`
	Template bool `json:"template"`
	Archived bool `json:"archived"`

	HasIssues bool `json:"has_issues"`
	HasWiki   bool `json:"has_wiki"`

	StarsCount    int `json:"stars_count"`
	ForksCount    int `json:"forks_count"`
	WatchersCount int `json:"watchers_count"`

	Language string   `json:"language"`
	Licenses []string `json:"licenses"`
	Topics   []string `json:"topics"`
	Size     int64    `json:"size"`
}

// giteaPageSize is the per-page maximum gitea.com enforces through
// MAX_RESPONSE_ITEMS. A larger request is capped server-side, so asking for
// more than this would silently yield fewer results than the loop expects.
const giteaPageSize = 50

func listReposGitea(client *http.Client, hostname string, limit int, owner string, filter FilterOptions) (*RepositoryList, error) {
	if len(filter.Topic) > 1 {
		return nil, fmt.Errorf("Gitea supports filtering repositories by one topic at a time")
	}

	ownerLogin, ownerID, err := resolveGiteaOwner(client, hostname, owner)
	if err != nil {
		return nil, err
	}

	listResult := &RepositoryList{Owner: ownerLogin}

	for page := 1; len(listResult.Repositories) < limit; page++ {
		repos, total, err := searchGiteaRepos(client, hostname, ownerID, filter, page, giteaPageSize)
		if err != nil {
			return nil, err
		}

		if page == 1 {
			// Gitea reports the full match count in X-Total-Count, which
			// GitHub does not. It counts server-side matches only, so a
			// client-side language filter can make the displayed total
			// larger than the number of rows below it.
			listResult.TotalCount = total
		}

		if len(repos) == 0 {
			break
		}

		for _, raw := range repos {
			repo := mapGiteaListRepo(raw, hostname)
			if !matchesClientSideFilter(repo, filter) {
				continue
			}
			listResult.Repositories = append(listResult.Repositories, *repo)
			if len(listResult.Repositories) >= limit {
				break
			}
		}

		// A short page means the server has no more results.
		if len(repos) < giteaPageSize {
			break
		}
	}

	return listResult, nil
}

// resolveGiteaOwner returns the login and numeric id of the owner to list. An
// empty handle means the authenticated user, resolved through GET /user.
func resolveGiteaOwner(client *http.Client, hostname string, owner string) (string, int64, error) {
	apiClient := api.NewClientFromHTTP(client)

	if owner == "" {
		var user struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		}
		if err := apiClient.REST(hostname, http.MethodGet, "user", nil, &user); err != nil {
			return "", 0, err
		}
		return user.Login, user.ID, nil
	}

	path, err := safeurl.JoinPath("users", owner)
	if err != nil {
		return "", 0, err
	}

	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := apiClient.REST(hostname, http.MethodGet, path.String(), nil, &user); err != nil {
		return "", 0, err
	}
	return user.Login, user.ID, nil
}

// searchGiteaRepos returns one page of repositories owned by uid, along with the
// server's total match count when it reports one.
func searchGiteaRepos(client *http.Client, hostname string, uid int64, filter FilterOptions, page, pageSize int) ([]giteaRepositoryListItem, int, error) {
	query := url.Values{}
	query.Set("uid", strconv.FormatInt(uid, 10))
	// Without exclusive, Gitea also returns repositories the user merely
	// contributes to, which is not what listing an owner's repositories means.
	query.Set("exclusive", "true")
	query.Set("private", "true")
	query.Set("limit", strconv.Itoa(pageSize))
	query.Set("page", strconv.Itoa(page))
	query.Set("sort", "updated")
	query.Set("order", "desc")

	if filter.Fork {
		query.Set("mode", "fork")
	} else if filter.Source {
		query.Set("mode", "source")
	}

	switch filter.Visibility {
	case "private":
		query.Set("is_private", "true")
	case "public":
		query.Set("is_private", "false")
	case "internal":
		// Not expressible server-side; filtered per page instead.
	}

	if filter.Archived {
		query.Set("archived", "true")
	} else if filter.NonArchived {
		query.Set("archived", "false")
	}

	if len(filter.Topic) == 1 {
		// The topic filter makes the keyword match a topic rather than a name.
		query.Set("q", filter.Topic[0])
		query.Set("topic", "true")
	}

	path, err := safeurl.JoinPath("repos", "search")
	if err != nil {
		return nil, 0, err
	}
	for key, values := range query {
		for _, value := range values {
			path.SetQuery(key, value)
		}
	}

	// Request rather than REST, because the total count only arrives in a
	// response header.
	apiClient := api.NewClientFromHTTP(client)
	resp, err := apiClient.Request(hostname, http.MethodGet, path.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var result giteaRepoSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, err
	}

	total, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	return result.Data, total, nil
}

// matchesClientSideFilter applies the filters Gitea's search endpoint cannot
// express.
func matchesClientSideFilter(repo *api.Repository, filter FilterOptions) bool {
	if filter.Language != "" &&
		(repo.PrimaryLanguage == nil || !strings.EqualFold(repo.PrimaryLanguage.Name, filter.Language)) {
		return false
	}

	// Gitea reports internal repositories with a separate flag rather than a
	// visibility value, so "public" here means neither private nor internal.
	switch filter.Visibility {
	case "internal":
		return repo.Visibility == "internal"
	case "public":
		return repo.Visibility == "public"
	case "private":
		return repo.Visibility == "private"
	}

	return true
}

func mapGiteaListRepo(raw giteaRepositoryListItem, hostname string) *api.Repository {
	updatedAt := parseGiteaTime(raw.UpdatedAt)

	repo := &api.Repository{
		ID:            strconv.FormatInt(raw.ID, 10),
		DatabaseID:    raw.ID,
		Name:          raw.Name,
		NameWithOwner: raw.FullName,
		Description:   raw.Description,
		HomepageURL:   raw.Website,
		URL:           raw.HTMLURL,
		SSHURL:        raw.SSHURL,
		CreatedAt:     parseGiteaTime(raw.CreatedAt),
		UpdatedAt:     updatedAt,
		// Gitea tracks no separate push time. The listing is sorted by
		// updated_at, so showing that timestamp keeps the displayed time
		// consistent with the ordering; falling back to created_at would show
		// years-old dates for actively updated repositories.
		PushedAt:         nonZeroTimePtr(updatedAt),
		ArchivedAt:       parseGiteaTimePtr(raw.ArchivedAt),
		IsArchived:       raw.Archived,
		IsEmpty:          raw.Empty,
		IsFork:           raw.Fork,
		IsMirror:         raw.Mirror,
		IsPrivate:        raw.Private,
		IsTemplate:       raw.Template,
		IsInOrganization: raw.Owner.Type == "Organization",
		HasIssuesEnabled: raw.HasIssues,
		HasWikiEnabled:   raw.HasWiki,
		DiskUsage:        int(raw.Size),
		ForkCount:        raw.ForksCount,
		StargazerCount:   raw.StarsCount,
		Visibility:       visibilityOf(raw.Private, raw.Internal),
		Owner: api.RepositoryOwner{
			ID:    strconv.FormatInt(raw.Owner.ID, 10),
			Login: raw.Owner.Login,
		},
		DefaultBranchRef: api.BranchRef{Name: raw.DefaultBranch},
	}

	repo.Watchers.TotalCount = raw.WatchersCount

	if raw.Language != "" {
		repo.PrimaryLanguage = &api.CodingLanguage{Name: raw.Language}
	}
	if len(raw.Licenses) > 0 {
		repo.LicenseInfo = &api.RepositoryLicense{Key: raw.Licenses[0], Name: raw.Licenses[0]}
	}

	return repo
}

// parseGiteaTime reads a Gitea timestamp. Gitea uses RFC 3339, which is what
// time.RFC3339 parses; an empty or unparseable value yields the zero time
// rather than failing the listing, since a missing timestamp is not a reason to
// hide a repository.
func parseGiteaTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nonZeroTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func parseGiteaTimePtr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t := parseGiteaTime(*s)
	if t.IsZero() {
		return nil
	}
	return &t
}

func visibilityOf(private, internal bool) string {
	switch {
	case private:
		return "private"
	case internal:
		return "internal"
	default:
		return "public"
	}
}
