package shared

import (
	"errors"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

// RemoteTagExistsGitea reports whether a tag exists on a Gitea host.
//
// GitHub answers this through a GraphQL ref lookup. Gitea has no GraphQL API, but
// it does have a git refs endpoint that takes the qualified ref name and answers
// 404 for a ref that is not there, which is the same question with the same
// answer.
func RemoteTagExistsGitea(httpClient *http.Client, repo ghrepo.Interface, tagName string) (bool, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "git", "refs", "tags", tagName)
	if err != nil {
		return false, err
	}

	client := api.NewClientFromHTTP(httpClient)

	// A HEAD request has no body to decode, so Request rather than REST.
	resp, err := client.Request(repo.RepoHost(), http.MethodHead, path.String(), nil)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, api.UnexpectedStatusError(resp)
	}

	return true, nil
}
