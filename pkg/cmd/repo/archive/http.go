package archive

import (
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/shurcooL/githubv4"
)

// archiveRepo archives the repository.
//
// GitHub archives through a mutation addressed by node id. Gitea has no GraphQL
// API, and archiving there is a field of the repository edit, so the same PATCH
// that renames a repository sets archived. Gitea addresses the repository by its
// path rather than by an id.
func archiveRepo(client *http.Client, repo *api.Repository) error {
	if hosttype.IsGitea(repo.RepoHost()) {
		return api.SetRepositoryArchivedGitea(api.NewClientFromHTTP(client), repo, true)
	}

	var mutation struct {
		ArchiveRepository struct {
			Repository struct {
				ID string
			}
		} `graphql:"archiveRepository(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.ArchiveRepositoryInput{
			RepositoryID: repo.ID,
		},
	}

	gql := api.NewClientFromHTTP(client)
	err := gql.Mutate(repo.RepoHost(), "ArchiveRepository", &mutation, variables)
	return err
}
