package api

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so a pull request's checks come from its head
// commit's statuses.
//
// The shape differs from GitHub's in ways the caller has to absorb.
//
// GitHub folds every check run and status context reachable from the head commit
// into one statusCheckRollup list, each entry carrying its workflow run and a
// required bit. Gitea keeps only commit statuses, one flat list per commit, so
// there is no workflow run to report and no notion of a required check.
//
// Gitea's status vocabulary is its own: pending, success, error, failure,
// warning and skipped. The four that overlap GitHub's mean the same thing and are
// upper-cased; warning and skipped are kept as words of their own rather than
// folded onto a GitHub conclusion, so a caller that buckets them can do so
// knowingly instead of receiving a wrong GitHub value.
//
// A status carries created_at and updated_at but no start or finish time, so
// StartedAt and CompletedAt stay zero and a caller that prints elapsed time
// prints nothing rather than a fabricated duration.

// giteaCombinedStatus is Gitea's CombinedStatus.
type giteaCombinedStatus struct {
	SHA      string `json:"sha"`
	State    string `json:"state"`
	TotalCnt int    `json:"total_count"`
	Statuses []struct {
		Context     string `json:"context"`
		Status      string `json:"status"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		CreatedAt   string `json:"created_at"`
		UpdatedAt   string `json:"updated_at"`
	} `json:"statuses"`
}

// PullRequestStatusChecksGitea returns the commit statuses on one commit, mapped
// onto the same CheckContexts the GraphQL path produces.
//
// The combined endpoint already carries every status for the commit, so there is
// no cursor to follow and no paging. Gitea's own per-status list endpoint exists
// too, but the combined one answers in a single request.
func PullRequestStatusChecksGitea(client *Client, repo ghrepo.Interface, sha string) (CheckContexts, error) {
	path := fmt.Sprintf("repos/%s/%s/commits/%s/status", repo.RepoOwner(), repo.RepoName(), url.PathEscape(sha))

	var raw giteaCombinedStatus
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &raw); err != nil {
		return CheckContexts{}, err
	}

	var contexts CheckContexts
	for _, s := range raw.Statuses {
		context := s.Context
		if context == "" {
			// A status with no context has nothing to identify it by but its
			// target, which is what a caller would show anyway.
			context = s.TargetURL
		}

		contexts.Nodes = append(contexts.Nodes, CheckContext{
			TypeName:    "StatusContext",
			Context:     context,
			Description: s.Description,
			State:       giteaStatusState(s.Status),
			TargetURL:   s.TargetURL,
			CreatedAt:   parseGiteaTime(s.CreatedAt),
		})
	}

	contexts.StatusContextCount = len(contexts.Nodes)
	return contexts, nil
}

// giteaStatusState maps Gitea's commit status onto GitHub's StatusState.
//
// The four shared words are upper-cased. warning and skipped have no GitHub
// StatusState counterpart: GitHub reports a non-failing warning as a check run's
// NEUTRAL conclusion, and SKIPPED exists only as a check conclusion, never as a
// status state. Both are passed through as their own word so the bucket mapping
// can decide what they mean.
func giteaStatusState(status string) StatusState {
	switch status {
	case "pending":
		return StatusStatePending
	case "success":
		return StatusStateSuccess
	case "error":
		return StatusStateError
	case "failure":
		return StatusStateFailure
	case "warning":
		return StatusStateWarning
	case "skipped":
		return StatusStateSkipped
	default:
		return StatusState(status)
	}
}
