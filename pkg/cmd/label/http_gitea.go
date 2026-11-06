package label

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

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API, so labels come from REST. Three differences shape
// this file.
//
// Gitea addresses a label by numeric id, never by name, and offers no by-name
// endpoint. Editing and deleting therefore resolve a name to an id first, which
// costs a request GitHub does not need.
//
// Gitea places no uniqueness constraint on label names, so a repository can hold
// two labels called "bug". Its own by-name lookups refuse an ambiguous name,
// and the same rule is applied here: a name that matches more than one label is
// an error rather than a silent choice between them. Because duplicates are
// possible, creating a label checks for an existing one first, which keeps
// `--force` and the skip logic in `gh label clone` working the way they do on
// GitHub.
//
// Gitea's Label schema carries no timestamps and no default flag, so createdAt,
// updatedAt and isDefault cannot be exported, and `--sort created` is refused
// rather than quietly reinterpreted as a different ordering.

// giteaPageSize is the per-page maximum gitea.com enforces through
// MAX_RESPONSE_ITEMS.
const giteaPageSize = 50

// giteaLabelFieldsUnsupported lists the export fields Gitea cannot supply.
var giteaLabelFieldsUnsupported = []string{"createdAt", "isDefault", "updatedAt"}

// giteaLabel is the Label object Gitea returns.
type giteaLabel struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Exclusive   bool   `json:"exclusive"`
	IsArchived  bool   `json:"is_archived"`
}

func (g giteaLabel) toLabel() label {
	return label{
		ID:          strconv.FormatInt(g.ID, 10),
		Name:        g.Name,
		Color:       g.Color,
		Description: g.Description,
		URL:         g.URL,
	}
}

// listLabelsGitea lists a repository's labels from GET /repos/{o}/{r}/labels.
//
// Gitea's sort parameter takes name-based values only, so `--sort created` has
// no equivalent. `--search` is applied to each page locally, which means a
// search scans every page to report an accurate total; without one, the server's
// X-Total-Count is used and paging stops as soon as the limit is reached.
func listLabelsGitea(client *http.Client, repo ghrepo.Interface, opts listQueryOptions) ([]label, int, error) {
	if err := validateGiteaLabelOptions(opts); err != nil {
		return nil, 0, err
	}

	apiClient := api.NewClientFromHTTP(client)
	basePath, err := labelsPath(repo)
	if err != nil {
		return nil, 0, err
	}

	search := strings.ToLower(opts.Query)

	var labels []label
	totalCount := 0

	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("limit", strconv.Itoa(giteaPageSize))
		query.Set("page", strconv.Itoa(page))
		if sortType := giteaSortType(opts.Order); sortType != "" {
			query.Set("sort", sortType)
		}

		pageURL, err := withQuery(basePath, query)
		if err != nil {
			return nil, 0, err
		}

		raw, serverTotal, err := fetchGiteaLabelPage(apiClient, repo.RepoHost(), pageURL)
		if err != nil {
			return nil, 0, err
		}
		if page == 1 {
			totalCount = serverTotal
		}

		for _, g := range raw {
			l := g.toLabel()
			if search != "" && !matchesLabelSearch(l, search) {
				continue
			}
			labels = append(labels, l)
			if search == "" && len(labels) == opts.Limit {
				return labels, totalCount, nil
			}
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	if search != "" {
		// The server total counts unfiltered labels, so the match count is the
		// only accurate figure once a search has been applied.
		totalCount = len(labels)
		if opts.Limit > 0 && len(labels) > opts.Limit {
			labels = labels[:opts.Limit]
		}
	}

	return labels, totalCount, nil
}

// fetchGiteaLabelPage returns one page of labels and the server's total count.
// The count arrives only in a response header, so the response is read directly
// rather than decoded through api.Client.REST.
func fetchGiteaLabelPage(apiClient *api.Client, hostname string, pageURL safeurl.SafeURL) ([]giteaLabel, int, error) {
	resp, err := apiClient.Request(hostname, http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var raw []giteaLabel
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, 0, err
	}

	total, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	return raw, total, nil
}

func matchesLabelSearch(l label, search string) bool {
	return strings.Contains(strings.ToLower(l.Name), search) ||
		strings.Contains(strings.ToLower(l.Description), search)
}

// giteaSortType maps gh's order flag onto the values Gitea accepts, which are
// name-based: the default is ascending by name and "reversealphabetically" is
// descending. Gitea offers no creation-date ordering, which
// validateGiteaLabelOptions rejects before this is reached.
func giteaSortType(order string) string {
	if strings.EqualFold(order, "desc") {
		return "reversealphabetically"
	}
	return ""
}

func validateGiteaLabelOptions(opts listQueryOptions) error {
	// "created" is gh's default sort, so rejecting it outright would fail every
	// plain `gh label list`. Only an explicit request is refused; the default
	// falls through to Gitea's own ordering, which is ascending by name.
	if opts.sortSpecified && strings.EqualFold(opts.Sort, "created") {
		return fmt.Errorf("Gitea does not sort labels by creation date; use `--sort name`")
	}
	for _, field := range opts.fields {
		if isUnsupportedGiteaLabelField(field) {
			return fmt.Errorf("Gitea does not provide the %q field for labels; available fields are %s",
				field, strings.Join(giteaLabelSupportedFields(), ", "))
		}
	}
	return nil
}

func isUnsupportedGiteaLabelField(field string) bool {
	for _, f := range giteaLabelFieldsUnsupported {
		if strings.EqualFold(field, f) {
			return true
		}
	}
	return false
}

func giteaLabelSupportedFields() []string {
	var supported []string
	for _, field := range labelFields {
		if !isUnsupportedGiteaLabelField(field) {
			supported = append(supported, field)
		}
	}
	return supported
}

func labelsPath(repo ghrepo.Interface) (safeurl.SafeURL, error) {
	return safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "labels")
}

func labelIDPath(repo ghrepo.Interface, id int64) (safeurl.SafeURL, error) {
	return safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "labels", strconv.FormatInt(id, 10))
}

func withQuery(base safeurl.SafeURL, query url.Values) (safeurl.SafeURL, error) {
	mutable, ok := base.(*safeurl.MutableSafeURL)
	if !ok {
		return nil, errors.New("internal error: expected a mutable URL")
	}
	for key, values := range query {
		for _, value := range values {
			mutable.SetQuery(key, value)
		}
	}
	return mutable, nil
}

// giteaLabelID resolves a label name to its id. Gitea allows duplicate names, so
// a name matching more than one label is an error rather than a guess.
func giteaLabelID(apiClient *api.Client, repo ghrepo.Interface, name string) (int64, error) {
	path, err := labelsPath(repo)
	if err != nil {
		return 0, err
	}

	var matches []giteaLabel
	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("limit", strconv.Itoa(giteaPageSize))
		query.Set("page", strconv.Itoa(page))

		pageURL, err := withQuery(path, query)
		if err != nil {
			return 0, err
		}

		raw, _, err := fetchGiteaLabelPage(apiClient, repo.RepoHost(), pageURL)
		if err != nil {
			return 0, err
		}

		for _, g := range raw {
			if g.Name == name {
				matches = append(matches, g)
				if len(matches) > 1 {
					return 0, fmt.Errorf("label name %q is ambiguous in %s; %d labels share it",
						name, ghrepo.FullName(repo), len(matches))
				}
			}
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	if len(matches) == 0 {
		return 0, errGiteaLabelNotFound
	}
	return matches[0].ID, nil
}

var errGiteaLabelNotFound = errors.New("label not found")

// createLabelGitea creates a label, reporting errLabelAlreadyExists when the
// repository already holds one of that name. Gitea would accept the duplicate,
// so the check is what keeps `--force` and `gh label clone` behaving as they do
// on GitHub.
func createLabelGitea(client *http.Client, repo ghrepo.Interface, opts *createOptions) error {
	apiClient := api.NewClientFromHTTP(client)

	if _, err := giteaLabelID(apiClient, repo, opts.Name); err == nil {
		if opts.Force {
			return updateLabelGitea(apiClient, repo, &editOptions{
				Name:        opts.Name,
				Color:       opts.Color,
				Description: opts.Description,
			})
		}
		return errLabelAlreadyExists
	} else if !errors.Is(err, errGiteaLabelNotFound) {
		return err
	}

	path, err := labelsPath(repo)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"name":        opts.Name,
		"description": opts.Description,
		"color":       giteaColor(opts.Color),
	})
	if err != nil {
		return err
	}

	// Gitea answers a successful create with 201 and no body, and REST decodes
	// every 2xx body as JSON, so the response is drained instead.
	resp, err := apiClient.Request(repo.RepoHost(), http.MethodPost, path.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// updateLabelGitea edits a label. Gitea's EditLabelOption takes `name` as the
// new name, so gh's NewName maps onto it and the label is addressed by id.
func updateLabelGitea(apiClient *api.Client, repo ghrepo.Interface, opts *editOptions) error {
	id, err := giteaLabelID(apiClient, repo, opts.Name)
	if err != nil {
		if errors.Is(err, errGiteaLabelNotFound) {
			return fmt.Errorf("label %q not found in %s", opts.Name, ghrepo.FullName(repo))
		}
		return err
	}

	properties := map[string]any{}
	if opts.Description != "" {
		properties["description"] = opts.Description
	}
	if opts.Color != "" {
		properties["color"] = giteaColor(opts.Color)
	}
	if opts.NewName != "" {
		properties["name"] = opts.NewName
	}

	path, err := labelIDPath(repo, id)
	if err != nil {
		return err
	}

	body, err := json.Marshal(properties)
	if err != nil {
		return err
	}

	resp, err := apiClient.Request(repo.RepoHost(), http.MethodPatch, path.String(), bytes.NewReader(body))
	if err != nil {
		if isGiteaNotFound(err) {
			return fmt.Errorf("label %q not found in %s", opts.Name, ghrepo.FullName(repo))
		}
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// deleteLabelGitea deletes a label by first resolving its name to an id.
func deleteLabelGitea(client *http.Client, repo ghrepo.Interface, name string) error {
	apiClient := api.NewClientFromHTTP(client)

	id, err := giteaLabelID(apiClient, repo, name)
	if err != nil {
		if errors.Is(err, errGiteaLabelNotFound) {
			return fmt.Errorf("label %q not found in %s", name, ghrepo.FullName(repo))
		}
		return err
	}

	path, err := labelIDPath(repo, id)
	if err != nil {
		return err
	}

	return apiClient.REST(repo.RepoHost(), http.MethodDelete, path.String(), nil, nil)
}

// giteaColor normalizes a color for Gitea. Its API example shows "#00aabb" and
// the server normalizes on write, but returning the bare six digits is what its
// responses contain and what gh displays, so the prefix is dropped on the way in.
func isGiteaNotFound(err error) bool {
	var httpErr api.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == 404
}

func giteaColor(color string) string {
	return strings.TrimPrefix(color, "#")
}
