package shared

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/set"
)

// Gitea has no GraphQL API, so the single-issue lookup that every issue
// subcommand starts from is answered here over REST.
//
// The fields argument is ignored. On GitHub it drives which parts of the issue
// the GraphQL query selects; Gitea returns the whole issue in one response, so
// there is nothing to project. Callers keep passing it so the two hosts share a
// signature.
//
// Comments and reactions are not part of that response and are fetched only when
// a caller asked for them, which is what the GraphQL path does too.

// giteaCommentFields lists the lookup fields that need a second request for
// comments. "lastComment" is a pseudo-field on the GraphQL path that selects the
// newest comment; Gitea has no equivalent selector, so the list is fetched and
// the newest taken from it.
var giteaCommentFields = []string{"comments", "lastComment"}

// FindIssueOrPRGitea loads one issue or pull request by index.
func FindIssueOrPRGitea(httpClient *http.Client, repo ghrepo.Interface, number int, fields []string) (*api.Issue, error) {
	client := api.NewClientFromHTTP(httpClient)

	issue, err := api.FetchIssueGitea(client, repo, number)
	if err != nil {
		return nil, giteaIssueLookupError(httpClient, repo, err)
	}

	requested := set.NewStringSet()
	requested.AddValues(fields)

	wantsComments := slices.ContainsFunc(giteaCommentFields, requested.Contains)

	if wantsComments {
		comments, err := api.IssueCommentsGitea(client, repo, number)
		if err != nil {
			return nil, err
		}
		issue.Comments = *comments
	}

	if requested.Contains("reactionGroups") {
		groups, err := api.IssueReactionGroupsGitea(client, repo, number)
		if err != nil {
			return nil, err
		}
		issue.ReactionGroups = groups
	}

	return issue, nil
}

// FindIssuesOrPRsGitea loads several issues by index.
//
// The lookups are sequential rather than the concurrent fan-out the GitHub path
// uses. Each Gitea issue is a single request with no follow-up query to overlap,
// and a self-hosted instance is likelier than GitHub to rate-limit a burst of
// parallel calls. Callers pass at most a handful of numbers, so the latency
// difference does not matter.
func FindIssuesOrPRsGitea(httpClient *http.Client, repo ghrepo.Interface, issueNumbers []int, fields []string) ([]*api.Issue, error) {
	issues := make([]*api.Issue, 0, len(issueNumbers))
	for _, number := range issueNumbers {
		issue, err := FindIssueOrPRGitea(httpClient, repo, number, fields)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// giteaIssueLookupError turns a failed lookup into the message callers expect.
//
// Gitea answers a missing issue and an issue in a repository with issues
// disabled both as 404, so the status alone cannot tell them apart. The
// repository's issues flag can, and it is only worth the extra request once the
// lookup has already failed. Anything other than a 404 is returned unchanged.
func giteaIssueLookupError(httpClient *http.Client, repo ghrepo.Interface, err error) error {
	var httpErr api.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		return err
	}

	enabled, lookupErr := giteaIssuesEnabled(httpClient, repo)
	if lookupErr != nil || enabled {
		return err
	}

	return fmt.Errorf("the '%s' repository has disabled issues", ghrepo.FullName(repo))
}

func giteaIssuesEnabled(httpClient *http.Client, repo ghrepo.Interface) (bool, error) {
	r, err := api.IssueRepoInfo(api.NewClientFromHTTP(httpClient), repo)
	if err != nil {
		return false, err
	}
	return r.HasIssuesEnabled, nil
}
