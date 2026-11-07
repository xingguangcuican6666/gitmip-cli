package unarchive

import (
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/shurcooL/githubv4"
)

// unarchiveRepo clears a repository's archived flag.
//
// GitHub has a mutation for this and Gitea has a field on the repository edit;
// the archive package holds the Gitea call so both directions live together.
func unarchiveRepo(client *http.Client, repo *api.Repository) error {
	if hosttype.IsGitea(repo.RepoHost()) {
		return api.SetRepositoryArchivedGitea(api.NewClientFromHTTP(client), repo, false)
	}

	var mutation struct {
		UnarchiveRepository struct {
			Repository struct {
				ID string
			}
		} `graphql:"unarchiveRepository(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.UnarchiveRepositoryInput{
			RepositoryID: repo.ID,
		},
	}

	gql := api.NewClientFromHTTP(client)
	err := gql.Mutate(repo.RepoHost(), "UnarchiveRepository", &mutation, variables)
	return err
}
