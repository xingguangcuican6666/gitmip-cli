package merge

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
)

// Gitea has no GraphQL API, so merging is a REST POST to
// /repos/{o}/{r}/pulls/{index}/merge.
//
// Three things differ from GitHub's mergePullRequest mutation in ways a caller
// can notice.
//
// Auto-merge is a field on the merge request rather than a separate endpoint, so
// there is no way to turn it back off through the API. `--disable-auto` is
// refused rather than sent as a request that would silently do nothing.
//
// There is no merge queue, and Gitea's repository features report none, so
// shouldAddToMergeQueue is always false here and the queue path is never taken.
//
// The commit message text GitHub generates server-side has no counterpart. The
// editor is seeded from the pull request's own title and body, which is what
// Gitea uses as its default, so a user who opens the editor sees the text that
// would be committed rather than an empty buffer.
//
// Branch deletion is the other difference. gh deletes the remote branch itself
// after merging on GitHub; Gitea's merge endpoint takes
// delete_branch_after_merge, which is what makes the deletion part of the same
// request that is allowed to fail. Asking for it here and skipping the separate
// delete keeps one request instead of two.

// mergePullRequestGitea merges a pull request on a Gitea host.
//
// number is the pull request index, which is how Gitea addresses one; the node
// id in payload.pullRequestID is meaningless there. useAdmin maps onto Gitea's
// force_merge, which bypasses branch protection and is what gh's --admin means.
func mergePullRequestGitea(client *http.Client, payload mergePayload, number int, useAdmin, deleteBranch bool) error {
	method, err := giteaMergeMethod(payload.method)
	if err != nil {
		return err
	}

	in := api.MergePullRequestGiteaInput{
		Method:                 method,
		MergeTitle:             payload.commitSubject,
		MergeMessage:           payload.commitBody,
		SetMessage:             payload.setCommitBody,
		HeadCommitID:           payload.expectedHeadOid,
		DeleteBranchAfterMerge: deleteBranch,
		MergeWhenChecksSucceed: payload.auto,
	}

	apiClient := api.NewClientFromHTTP(client)
	return api.MergePullRequestGitea(apiClient, payload.repo, number, in, useAdmin)
}

// giteaMergeMethod maps gh's merge method onto Gitea's `do` enum. Gitea offers
// two strategies gh has no flag for (rebase-merge and fast-forward-only), and
// every strategy gh can ask for has a counterpart, so the mapping is total in the
// direction that matters.
func giteaMergeMethod(method PullRequestMergeMethod) (api.PullRequestMergeMethodGitea, error) {
	switch method {
	case PullRequestMergeMethodMerge:
		return api.GiteaMergeMethodMerge, nil
	case PullRequestMergeMethodRebase:
		return api.GiteaMergeMethodRebase, nil
	case PullRequestMergeMethodSquash:
		return api.GiteaMergeMethodSquash, nil
	default:
		return "", fmt.Errorf("unknown merge method %d", method)
	}
}

// giteaMergeText seeds the commit message editor on a Gitea host.
//
// GitHub answers viewerMergeHeadlineText and viewerMergeBodyText from a query;
// Gitea generates its merge message on the server when the field is left empty,
// and offers no way to read what it would be. The pull request's own title and
// body are the closest thing to that default, and they are already loaded, so
// this costs no request.
func giteaMergeText(pr *api.PullRequest) (headline, body string) {
	return pr.Title, pr.Body
}

// mergeTextForHost returns the subject and body the commit message editor is
// seeded with.
//
// pr is the pull request being merged, which only the Gitea path reads: GitHub
// asks the server for the text it would commit, addressed by node id, while
// Gitea has nothing to ask and falls back to the pull request's own title and
// body.
func mergeTextForHost(client *http.Client, repo ghrepo.Interface, prID string, method PullRequestMergeMethod, pr *api.PullRequest) (string, string, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		headline, body := giteaMergeText(pr)
		return headline, body, nil
	}
	return getMergeText(client, repo, prID, method)
}

// giteaBlockedReason reports why a pull request on a Gitea host cannot be
// merged, or "" when nothing visible blocks it.
//
// Gitea has no mergeStateStatus, so blockedReason always answers "" there and a
// conflicting pull request would reach the server, which answers 405 without
// saying what is wrong. mergeable is the signal Gitea does give, and CONFLICTING
// is the value the mapping derives from it.
func giteaBlockedReason(pr *api.PullRequest) string {
	if pr.State == MergeStateStatusMerged {
		return ""
	}
	if pr.Mergeable == api.PullRequestMergeableConflicting {
		return "the merge commit cannot be cleanly created"
	}
	return ""
}
