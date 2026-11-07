package create

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/internal/safeurl"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so creating a repository is a REST POST, and it is
// narrower than GitHub's create mutation in ways that have to be handled rather
// than ignored.
//
// CreateRepoOption takes name, description, private, auto_init, default_branch,
// gitignores, license, readme, issue_labels and trust_model. It has no fields for
// issues, wiki or homepage, which are all on EditRepoOption instead, so those go
// in a follow-up PATCH whenever the caller asked for something other than Gitea's
// defaults.
//
// There is no visibility field at all. Private is a boolean, and Gitea's third
// state, an internal repository, cannot be created through the API, so asking for
// one is refused rather than quietly created as private.
//
// A team cannot own a repository, so --team is refused. Gitea does have template
// repositories and a generate endpoint for them, which is a different path from a
// plain create and takes a different option type.

// ErrGiteaInternalVisibilityUnsupported explains why an internal repository cannot
// be created: Gitea's create endpoint takes only a private boolean.
var ErrGiteaInternalVisibilityUnsupported = errors.New("Gitea's API creates repositories as public or private only, so `--internal` is not supported")

// ErrGiteaTeamUnsupported explains why a team cannot own a repository: Gitea
// repositories are owned by a user or an organisation.
var ErrGiteaTeamUnsupported = errors.New("Gitea repositories are owned by a user or an organisation, so `--team` is not supported")

// giteaCreateRepoOption is Gitea's CreateRepoOption.
type giteaCreateRepoOption struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Private       bool   `json:"private,omitempty"`
	AutoInit      bool   `json:"auto_init,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Gitignores    string `json:"gitignores,omitempty"`
	License       string `json:"license,omitempty"`
	Readme        string `json:"readme,omitempty"`
	IssueLabels   string `json:"issue_labels,omitempty"`
	TrustModel    string `json:"trust_model,omitempty"`
	ObjectFormat  string `json:"object_format_name,omitempty"`
}

// giteaEditRepoOption carries the fields CreateRepoOption has no room for.
type giteaEditRepoOption struct {
	HasIssues *bool  `json:"has_issues,omitempty"`
	HasWiki   *bool  `json:"has_wiki,omitempty"`
	Website   string `json:"website,omitempty"`
}

// giteaRepository is the part of Gitea's Repository that the create response is
// read from.
type giteaRepository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	Internal      bool   `json:"internal"`
	Template      bool   `json:"template"`
	DefaultBranch string `json:"default_branch"`
	CreatedAt     string `json:"created_at"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// repoCreateGitea creates a repository on a Gitea host.
//
// ownerLogin names the organisation when the repository belongs to one; an empty
// value creates it under the authenticated user, which is what Gitea's
// /user/repos endpoint does.
func repoCreateGitea(httpClient *http.Client, hostname string, input repoCreateInput) (*api.Repository, error) {
	if input.TeamSlug != "" {
		return nil, ErrGiteaTeamUnsupported
	}
	if isGiteaInternalVisibility(input.Visibility) {
		return nil, ErrGiteaInternalVisibilityUnsupported
	}

	client := api.NewClientFromHTTP(httpClient)

	option := giteaCreateRepoOption{
		Name:        input.Name,
		Description: input.Description,
		Private:     giteaIsPrivate(input.Visibility),
	}
	if input.InitReadme {
		option.AutoInit = true
		option.Readme = "Default"
	}
	if input.GitIgnoreTemplate != "" {
		option.Gitignores = input.GitIgnoreTemplate
	}
	if input.LicenseTemplate != "" {
		option.License = input.LicenseTemplate
	}

	path, err := giteaRepoCreatePath(input.OwnerLogin)
	if err != nil {
		return nil, err
	}

	created, err := postGiteaRepository(client, hostname, path.String(), option)
	if err != nil {
		return nil, err
	}

	// Issues, wiki and homepage are not fields of the create request, so they are
	// set afterwards. The PATCH is skipped entirely when the caller asked for
	// nothing, since Gitea's own defaults are what the repository should keep.
	edit := giteaEditRepoOption{}
	needsEdit := false
	if input.HasIssuesEnabled != true || input.HasWikiEnabled != true {
		hasIssues := input.HasIssuesEnabled
		hasWiki := input.HasWikiEnabled
		edit.HasIssues = &hasIssues
		edit.HasWiki = &hasWiki
		needsEdit = true
	}
	if input.HomepageURL != "" {
		edit.Website = input.HomepageURL
		needsEdit = true
	}
	if needsEdit {
		edited, err := patchGiteaRepository(client, hostname, created, edit)
		if err != nil {
			// The repository exists by now, so the failure is reported with it
			// rather than as though nothing happened.
			return nil, fmt.Errorf("created %s but could not apply its settings: %w", created.FullName, err)
		}
		created = edited
	}

	return giteaRepositoryToAPI(created, hostname), nil
}

// repoCreateFromTemplateGitea creates a repository from a Gitea template.
//
// Gitea's generate endpoint takes owner and name as required fields, and
// git_content as the flag that says whether the template's files are copied. An
// empty git_content would generate a repository with nothing in it, so it is set
// unless the caller said otherwise.
func repoCreateFromTemplateGitea(httpClient *http.Client, hostname string, templateRepo ghrepo.Interface, input repoCreateInput) (*api.Repository, error) {
	if input.TeamSlug != "" {
		return nil, ErrGiteaTeamUnsupported
	}
	if isGiteaInternalVisibility(input.Visibility) {
		return nil, ErrGiteaInternalVisibilityUnsupported
	}

	owner := input.OwnerLogin
	if owner == "" {
		var err error
		owner, err = api.CurrentLoginName(api.NewClientFromHTTP(httpClient), hostname)
		if err != nil {
			return nil, err
		}
	}

	payload, err := json.Marshal(struct {
		Owner       string `json:"owner"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Private     bool   `json:"private,omitempty"`
		GitContent  bool   `json:"git_content"`
		GitHooks    bool   `json:"git_hooks,omitempty"`
		Webhooks    bool   `json:"webhooks,omitempty"`
		Topics      bool   `json:"topics,omitempty"`
	}{
		Owner:       owner,
		Name:        input.Name,
		Description: input.Description,
		Private:     giteaIsPrivate(input.Visibility),
		GitContent:  true,
		GitHooks:    input.IncludeAllBranches,
	})
	if err != nil {
		return nil, err
	}

	path, err := safeurl.JoinPath("repos", templateRepo.RepoOwner(), templateRepo.RepoName(), "generate")
	if err != nil {
		return nil, err
	}

	client := api.NewClientFromHTTP(httpClient)
	created, err := sendGiteaRepository(client, hostname, http.MethodPost, path.String(), payload)
	if err != nil {
		return nil, err
	}

	return giteaRepositoryToAPI(created, hostname), nil
}

// giteaRepoCreatePath picks the endpoint for the owner.
//
// An empty owner means the authenticated user, which is /user/repos. Otherwise the
// owner is an organisation, which is /orgs/{org}/repos. JoinPath rather than a
// formatted string, so an owner containing a slash cannot reach a different
// resource than the one named.
func giteaRepoCreatePath(ownerLogin string) (*safeurl.MutableSafeURL, error) {
	if ownerLogin == "" {
		return safeurl.JoinPath("user", "repos")
	}
	return safeurl.JoinPath("orgs", ownerLogin, "repos")
}

func postGiteaRepository(client *api.Client, hostname, path string, option giteaCreateRepoOption) (*giteaRepository, error) {
	payload, err := json.Marshal(option)
	if err != nil {
		return nil, err
	}
	return sendGiteaRepository(client, hostname, http.MethodPost, path, payload)
}

func patchGiteaRepository(client *api.Client, hostname string, created *giteaRepository, option giteaEditRepoOption) (*giteaRepository, error) {
	payload, err := json.Marshal(option)
	if err != nil {
		return nil, err
	}

	owner, name, _ := strings.Cut(created.FullName, "/")
	if owner == "" || name == "" {
		owner = created.Owner.Login
		name = created.Name
	}
	path, err := safeurl.JoinPath("repos", owner, name)
	if err != nil {
		return nil, err
	}

	return sendGiteaRepository(client, hostname, http.MethodPatch, path.String(), payload)
}

// sendGiteaRepository performs a write and decodes the repository it returns.
//
// Request rather than REST, because a Gitea write that succeeds with no body would
// otherwise be reported as a decode failure rather than as a created repository.
func sendGiteaRepository(client *api.Client, hostname, method, path string, payload []byte) (*giteaRepository, error) {
	resp, err := client.Request(hostname, method, path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var repo giteaRepository
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("Gitea created the repository but returned no description of it")
		}
		return nil, err
	}
	return &repo, nil
}

// giteaRepositoryToAPI maps a Gitea repository onto the shape the create command
// prints and clones from.
func giteaRepositoryToAPI(repo *giteaRepository, hostname string) *api.Repository {
	owner, name, _ := strings.Cut(repo.FullName, "/")
	if owner == "" {
		owner = repo.Owner.Login
	}
	if name == "" {
		name = repo.Name
	}

	out := &api.Repository{
		ID:            strconv.FormatInt(repo.ID, 10),
		DatabaseID:    repo.ID,
		Name:          name,
		NameWithOwner: repo.FullName,
		Owner: api.RepositoryOwner{
			Login: owner,
		},
		URL:       repo.HTMLURL,
		IsPrivate: repo.Private,
	}
	// The create command clones the template's default branch afterwards, and
	// reads it from here rather than asking again.
	if repo.DefaultBranch != "" {
		out.DefaultBranchRef.Name = repo.DefaultBranch
	}

	return api.InitRepoHostname(out, hostname)
}

// giteaIsPrivate reports whether a visibility value means private.
func giteaIsPrivate(visibility string) bool {
	switch visibility {
	case "", "PUBLIC":
		return false
	default:
		return true
	}
}

func isGiteaInternalVisibility(visibility string) bool {
	return visibility == "INTERNAL" || visibility == "internal" || visibility == "Internal"
}

// listTemplateRepositoriesGitea lists the template repositories an owner has.
//
// Gitea's repository list carries a template boolean, so the templates are found
// by filtering the owner's repositories rather than by a separate endpoint. An
// owner that is not a user or organisation, or one with no repositories, yields an
// empty list rather than an error.
func listTemplateRepositoriesGitea(httpClient *http.Client, hostname, owner string) ([]api.Repository, error) {
	client := api.NewClientFromHTTP(httpClient)

	u, err := safeurl.JoinPath("users", owner, "repos")
	if err != nil {
		return nil, err
	}
	api.SetPageSizeMutable(u, hostname, 50)

	var repos []giteaRepository
	if err := client.REST(hostname, http.MethodGet, u.String(), nil, &repos); err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}

	var templates []api.Repository
	for _, r := range repos {
		if !r.Template {
			continue
		}
		templates = append(templates, *giteaRepositoryToAPI(&r, hostname))
	}
	return templates, nil
}
