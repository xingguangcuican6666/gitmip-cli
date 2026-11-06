package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so a pull request edit that GitHub performs as
// updatePullRequest plus separate label, assignee, milestone and reviewer
// mutations is performed here as REST calls.
//
// The differences from the issue path are what a pull request adds:
//
// The base branch is a field of EditPullRequestOption, so it travels with the
// other scalar fields instead of being its own mutation.
//
// Reviewers are not part of the pull request body at all. Gitea takes them on
// their own endpoint, POST and DELETE /pulls/{index}/requested_reviewers, and a
// reviewer is added and removed by login. That is also what the GitHub REST
// fallback does, so the shape is familiar even though the address differs.
//
// Everything else, including the label replacement and the milestone id
// clearing, is shared with the issue path and reused from there.

// UpdatePullRequestGitea applies an edit to one pull request on a Gitea host.
func UpdatePullRequestGitea(httpClient *http.Client, repo ghrepo.Interface, pr *api.PullRequest, editable Editable) error {
	// The reviewers are not part of the pull request body, so they are applied
	// by their own endpoints after the body has been written. Doing them in the
	// other order would leave a pull request whose reviewers were granted against
	// a title that is about to change.
	reviewers := editable.Reviewers
	editable.Reviewers = EditableReviewers{}

	client := api.NewClientFromHTTP(httpClient)

	if err := UpdateIssueGitea(httpClient, repo, issueForPullRequest(pr), editable); err != nil {
		return err
	}

	// The base branch is the one field the issue path cannot carry, because it
	// only exists on a pull request. It goes in its own PATCH for the same reason
	// the other scalars do: an unedited field must not be present.
	if editable.Base.Edited {
		path := fmt.Sprintf("repos/%s/%s/pulls/%d", repo.RepoOwner(), repo.RepoName(), pr.Number)
		payload, err := json.Marshal(struct {
			Base string `json:"base"`
		}{Base: editable.Base.Value})
		if err != nil {
			return fmt.Errorf("failed to serialize: %w", err)
		}

		resp, err := client.Request(repo.RepoHost(), http.MethodPatch, path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	if !reviewers.Edited {
		return nil
	}

	add := reviewers.Add
	remove := reviewers.Remove
	if len(add) == 0 && len(remove) == 0 {
		// An interactive edit sets Value rather than Add and Remove, so the
		// difference against what is there now is what has to change.
		value := reviewers.Value
		current := reviewers.DefaultLogins
		if len(current) == 0 {
			current = reviewers.Default
		}
		add = difference(value, current)
		remove = difference(current, value)
	}

	if len(add) > 0 {
		if err := requestReviewersGitea(client, repo, pr.Number, add, true); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if err := requestReviewersGitea(client, repo, pr.Number, remove, false); err != nil {
			return err
		}
	}

	return nil
}

// issueForPullRequest adapts a pull request to the issue shape the shared edit
// path works on. They differ only in the base branch, which the caller has
// already put into the editable.
func issueForPullRequest(pr *api.PullRequest) *api.Issue {
	issue := &api.Issue{
		ID:        pr.ID,
		Number:    pr.Number,
		Title:     pr.Title,
		URL:       pr.URL,
		Assignees: pr.Assignees,
		Labels:    pr.Labels,
		Milestone: pr.Milestone,
	}
	return issue
}

// requestReviewersGitea adds or removes review requests by login.
//
// Gitea's payload names the reviewers array and the teams array separately. gh
// cannot tell a team from a user on a Gitea host, where teams are org entities
// the reviewer search does not return, so everything is sent as a reviewer login.
func requestReviewersGitea(client *api.Client, repo ghrepo.Interface, number int, reviewers []string, add bool) error {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/requested_reviewers", repo.RepoOwner(), repo.RepoName(), number)

	payload, err := json.Marshal(struct {
		Reviewers []string `json:"reviewers"`
	}{Reviewers: reviewers})
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}

	method := http.MethodPost
	if !add {
		method = http.MethodDelete
	}

	resp, err := client.Request(repo.RepoHost(), method, path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// difference returns the items in a that are not in b, preserving a's order.
func difference(a, b []string) []string {
	seen := make(map[string]struct{}, len(b))
	for _, s := range b {
		seen[s] = struct{}{}
	}

	var out []string
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		out = append(out, s)
	}
	return out
}
