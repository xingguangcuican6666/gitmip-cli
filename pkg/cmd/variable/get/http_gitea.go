package get

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/pkg/cmd/variable/shared"
)

// giteaActionVariable is the single-variable object Gitea returns from
// GET /repos/{o}/{r}/actions/variables/{name} and its org counterpart.
//
// The value field is "data", not "value", and there are no timestamps and no
// visibility, so the fields gh exports for GitHub cannot all be filled.
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

func getVariableGitea(opts *GetOptions, client *api.Client, host, path string) error {
	var raw giteaActionVariable
	if err := client.REST(host, http.MethodGet, path, nil, &raw); err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("variable %s was not found", opts.VariableName)
		}
		return fmt.Errorf("failed to get variable %s: %w", opts.VariableName, err)
	}

	variable := raw.toVariable()

	if opts.Exporter != nil {
		// Selected repositories have no Gitea counterpart: visibility is not
		// modelled, so there is nothing to count and no URL to follow.
		return opts.Exporter.Write(opts.IO, &variable)
	}

	fmt.Fprintf(opts.IO.Out, "%s\n", variable.Value)
	return nil
}
