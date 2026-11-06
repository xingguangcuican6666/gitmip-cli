package list

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmd/variable/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
)

// Gitea's actions variable list differs from GitHub's in ways the shared
// decoder cannot absorb.
//
// The response is a bare JSON array rather than an object wrapping a
// "variables" key, so decoding into the anonymous struct getVariables uses
// would always come back empty. The value field is called "data", not "value".
//
// ActionVariable carries name, data, description, owner_id and repo_id only, so
// there are no timestamps and no visibility: the UPDATED column would be blank
// for every row and the visibility column has no source. Both are dropped from
// the output rather than shown empty, and the list keeps the server's order.
//
// Gitea has no deployment environments, so `--env` has nothing to list.
//
// Pagination uses limit, and gitea.com caps a page at MAX_RESPONSE_ITEMS.
const giteaVariablePageSize = 50

// giteaActionVariable is the variable object Gitea returns.
type giteaActionVariable struct {
	Name        string `json:"name"`
	Data        string `json:"data"`
	Description string `json:"description"`
	OwnerID     int64  `json:"owner_id"`
	RepoID      int64  `json:"repo_id"`
}

func (g giteaActionVariable) toVariable() shared.Variable {
	return shared.Variable{
		Name:  g.Name,
		Value: g.Data,
	}
}

// listVariablesGitea fetches and prints the variables for a repository or an
// organization on a Gitea host.
func listVariablesGitea(opts *ListOptions, client *http.Client, host string, baseRepo ghrepo.Interface, variableEntity shared.VariableEntity, orgName string) error {
	var variables []shared.Variable
	var err error

	switch variableEntity {
	case shared.Repository:
		variables, err = getRepoVariablesGitea(client, host, baseRepo.RepoOwner(), baseRepo.RepoName())
	case shared.Organization:
		variables, err = getOrgVariablesGitea(client, host, orgName)
	default:
		return fmt.Errorf("Gitea has no deployment environments, so `--env` is not supported")
	}

	if err != nil {
		return fmt.Errorf("failed to get variables: %w", err)
	}

	if len(variables) == 0 && opts.Exporter == nil {
		return cmdutil.NewNoResultsError("no variables found")
	}

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "failed to start pager: %v\n", err)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, variables)
	}

	table := tableprinter.New(opts.IO, tableprinter.WithHeader("Name", "Value"))
	for _, variable := range variables {
		table.AddField(variable.Name)
		table.AddField(variable.Value)
		table.EndRow()
	}

	return table.Render()
}

// getRepoVariablesGitea lists a repository's actions variables.
func getRepoVariablesGitea(client *http.Client, host, owner, repo string) ([]shared.Variable, error) {
	u, err := safeurl.JoinPath("repos", owner, repo, "actions", "variables")
	if err != nil {
		return nil, err
	}
	return getVariablesGitea(client, host, u)
}

// getOrgVariablesGitea lists an organization's actions variables. Gitea applies
// no visibility model to them, so there are no selected repositories to count.
func getOrgVariablesGitea(client *http.Client, host, orgName string) ([]shared.Variable, error) {
	u, err := safeurl.JoinPath("orgs", orgName, "actions", "variables")
	if err != nil {
		return nil, err
	}
	return getVariablesGitea(client, host, u)
}

// getVariablesGitea pages through a Gitea variable list and maps it onto gh's
// Variable type.
func getVariablesGitea(client *http.Client, host string, u *safeurl.MutableSafeURL) ([]shared.Variable, error) {
	apiClient := api.NewClientFromHTTP(client)

	var results []shared.Variable
	for page := 1; ; page++ {
		pageURL := withVariablePage(u, page)

		// Request rather than REST, because the response is a bare array and
		// REST would be handed an object to decode it into.
		resp, err := apiClient.Request(host, http.MethodGet, pageURL.String(), nil)
		if err != nil {
			return nil, err
		}

		var pageResults []giteaActionVariable
		decodeErr := json.NewDecoder(resp.Body).Decode(&pageResults)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}

		for _, raw := range pageResults {
			results = append(results, raw.toVariable())
		}

		if len(pageResults) < giteaVariablePageSize {
			break
		}
	}

	return results, nil
}

// withVariablePage sets the page parameters on u and returns it. SetQuery
// replaces an existing value, so reusing one URL across pages is enough.
func withVariablePage(u *safeurl.MutableSafeURL, page int) *safeurl.MutableSafeURL {
	u.SetQuery("limit", strconv.Itoa(giteaVariablePageSize))
	u.SetQuery("page", strconv.Itoa(page))
	return u
}
