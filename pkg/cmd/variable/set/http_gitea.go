package set

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/variable/shared"
)

// Gitea's variable endpoints differ from GitHub's in shape, not just in fields.
//
// The variable name belongs in the path on both create and update, so there is
// no collection POST to add a variable to. Create is POST to
// /actions/variables/{name} and update is PUT to the same path, where GitHub
// posts to the collection and patches the named item.
//
// CreateVariableOption carries only value and description; the name is already
// in the path. UpdateVariableOption adds name, which Gitea reads as a rename.
// Visibility and selected repositories do not exist: a Gitea variable applies
// to the whole organization or repository it is defined on, so `--visibility`
// and `--repos` have nothing to set.
//
// POST answers 409 when the name already exists, which is the signal to update
// instead. That matches the create-then-update flow the GitHub path uses, so
// the operation reported back to the user stays accurate.

// giteaCreateVariableOption is Gitea's CreateVariableOption.
type giteaCreateVariableOption struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// giteaUpdateVariableOption is Gitea's UpdateVariableOption. An empty Name
// leaves the name unchanged.
type giteaUpdateVariableOption struct {
	Value       string `json:"value"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// giteaVariableScope locates the collection a variable belongs to. Gitea puts
// the owner in the path differently for an organization and a repository.
type giteaVariableScope struct {
	prefix []string
}

func giteaRepoScope(repo ghrepo.Interface) giteaVariableScope {
	return giteaVariableScope{prefix: []string{"repos", repo.RepoOwner(), repo.RepoName()}}
}

func giteaOrgScope(orgName string) giteaVariableScope {
	return giteaVariableScope{prefix: []string{"orgs", orgName}}
}

// path builds /.../actions/variables/{name} within the scope.
func (s giteaVariableScope) path(variableName string) (safeurl.SafeURL, error) {
	components := make([]string, 0, len(s.prefix)+3)
	components = append(components, s.prefix...)
	components = append(components, "actions", "variables", variableName)
	return safeurl.JoinPath(components...)
}

func setVariableGitea(client *api.Client, host string, opts setOptions) setResult {
	result := setResult{Operation: createdOperation, Key: opts.Key}

	var scope giteaVariableScope
	switch opts.Entity {
	case shared.Organization:
		scope = giteaOrgScope(opts.Organization)
	case shared.Repository:
		scope = giteaRepoScope(opts.Repository)
	default:
		result.Err = fmt.Errorf("failed to set variable %q: Gitea has no deployment environments, so `--env` is not supported", opts.Key)
		return result
	}

	path, err := scope.path(opts.Key)
	if err != nil {
		result.Err = fmt.Errorf("failed to set variable %q: %w", opts.Key, err)
		return result
	}

	if err := sendGiteaVariable(client, host, http.MethodPost, path,
		giteaCreateVariableOption{Value: opts.Value}); err != nil {
		if !isGiteaConflict(err) {
			result.Err = fmt.Errorf("failed to set variable %q: %w", opts.Key, err)
			return result
		}
		// A 409 means the name is taken, which is what an update is for.
		result.Operation = updatedOperation
		if err := sendGiteaVariable(client, host, http.MethodPut, path,
			giteaUpdateVariableOption{Value: opts.Value}); err != nil {
			result.Err = fmt.Errorf("failed to set variable %q: %w", opts.Key, err)
			return result
		}
	}

	return result
}

// sendGiteaVariable issues a write and discards the response.
//
// Request rather than REST, because Gitea answers a successful create with 201
// and an empty body, and REST decodes every 2xx body as JSON, which fails on
// nothing. Reading the response directly lets a 2xx stand as success without
// expecting a document Gitea does not send. Non-2xx still arrives as an
// api.HTTPError, so the 409 check works unchanged.
func sendGiteaVariable(client *api.Client, host, method string, path safeurl.SafeURL, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}

	resp, err := client.Request(host, method, path.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	drainGiteaResponse(resp)
	return nil
}

func drainGiteaResponse(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func isGiteaConflict(err error) bool {
	var httpErr api.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict
}
