package api

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so updating a pull request's head branch is a POST
// to /repos/{o}/{r}/pulls/{index}/update.
//
// The operation differs from GitHub's updatePullRequestBranch in one way that
// matters: Gitea merges the base branch into the head branch and offers no
// rebase. GitHub takes an updateMethod of MERGE or REBASE; Gitea's endpoint has
// no equivalent field. A caller that asked to rebase is told so rather than
// silently getting a merge.
//
// The endpoint is addressed by index, not by node id, and takes no body: there is
// no way to pin the expected head, so Gitea updates whatever the head is when the
// request lands.

// UpdatePullRequestBranchGitea merges the base branch into the pull request's
// head branch.
func UpdatePullRequestBranchGitea(client *Client, repo ghrepo.Interface, number int) error {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/update", repo.RepoOwner(), repo.RepoName(), number)

	resp, err := client.Request(repo.RepoHost(), http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// giteaCompare is the part of Gitea's Compare that gh reads.
type giteaCompare struct {
	TotalCommits int `json:"total_commits"`
}

// compareCountGitea counts the commits one ref is ahead of another.
//
// Gitea's compare endpoint reports only the commits it found, with no ahead_by or
// behind_by, so each count is one call and the direction is what decides which
// count it is.
func compareCountGitea(client *Client, repo ghrepo.Interface, base, head string) (int, error) {
	basehead := base + "..." + head
	path := fmt.Sprintf("repos/%s/%s/compare/%s", repo.RepoOwner(), repo.RepoName(), url.PathEscape(basehead))

	var cmp giteaCompare
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &cmp); err != nil {
		return 0, err
	}
	return cmp.TotalCommits, nil
}

// ComparePullRequestBaseBranchGitea reports how a pull request's head relates to
// its base.
//
// The two counts are not symmetric in Gitea. A head in a fork is reachable from
// the base repository as `owner:branch`, so base...head works, but the reverse
// direction `owner:branch...base` is not: Gitea resolves the first ref only in the
// repository being compared and answers 404 for the second. The behind count is
// therefore taken from the merge base instead. Every commit the base has that the
// merge base does not is exactly one the head is behind by, and both of those are
// plain refs in the base repository.
func ComparePullRequestBaseBranchGitea(client *Client, repo ghrepo.Interface, number int) (*RefComparison, error) {
	pr, err := FetchPullRequestGitea(client, repo, number)
	if err != nil {
		return nil, err
	}

	base := pr.BaseRefName
	head := pr.HeadRefName

	if pr.HeadRepository != nil && pr.HeadRepository.NameWithOwner != "" {
		if owner, _, ok := strings.Cut(pr.HeadRepository.NameWithOwner, "/"); ok && owner != "" && owner != repo.RepoOwner() {
			head = owner + ":" + head
		}
	}

	ahead, err := compareCountGitea(client, repo, base, head)
	if err != nil {
		return nil, err
	}

	behind, err := behindCountGitea(client, repo, number, base)
	if err != nil {
		return nil, err
	}

	status := "diverged"
	switch {
	case ahead > 0 && behind == 0:
		status = "ahead"
	case ahead == 0 && behind > 0:
		status = "behind"
	case ahead == 0 && behind == 0:
		status = "identical"
	}

	return &RefComparison{AheadBy: ahead, BehindBy: behind, Status: status}, nil
}

// behindCountGitea counts the base commits the head does not contain.
//
// Gitea reports merge_base on the pull request, which is the commit the two sides
// agree on. Every base commit after it is one the head is behind by, and both refs
// in that comparison live in the base repository, which is what makes it work for
// a head in a fork.
func behindCountGitea(client *Client, repo ghrepo.Interface, number int, base string) (int, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", repo.RepoOwner(), repo.RepoName(), number)

	var raw struct {
		MergeBase string `json:"merge_base"`
	}
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &raw); err != nil {
		return 0, err
	}
	if raw.MergeBase == "" {
		return 0, nil
	}

	return compareCountGitea(client, repo, raw.MergeBase, base)
}
