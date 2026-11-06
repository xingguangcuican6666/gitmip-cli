package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so closing and reopening a pull request is a PATCH
// on the pull request itself with the new state.
//
// GitHub has dedicated close and reopen mutations, both addressed by node id.
// Gitea has neither: `state` is a field of EditPullRequestOption like any other,
// and the pull request is addressed by index. The two operations therefore look
// identical apart from the value.
//
// Gitea refuses to set the state of a merged pull request, answering 405. The
// commands check for that before calling, so a 405 here means something changed
// between the read and the write.

// SetPullRequestStateGitea sets a pull request's state to Gitea's open or closed.
func SetPullRequestStateGitea(client *Client, repo ghrepo.Interface, number int, state string) error {
	if state != "open" && state != "closed" {
		return fmt.Errorf("state must be open or closed, got %q", state)
	}

	path := fmt.Sprintf("repos/%s/%s/pulls/%d", repo.RepoOwner(), repo.RepoName(), number)

	payload, err := json.Marshal(struct {
		State string `json:"state"`
	}{State: state})
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}

	resp, err := client.Request(repo.RepoHost(), http.MethodPatch, path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// ClosePullRequestGitea closes a pull request by index.
func ClosePullRequestGitea(client *Client, repo ghrepo.Interface, number int) error {
	return SetPullRequestStateGitea(client, repo, number, "closed")
}

// ReopenPullRequestGitea reopens a pull request by index.
func ReopenPullRequestGitea(client *Client, repo ghrepo.Interface, number int) error {
	return SetPullRequestStateGitea(client, repo, number, "open")
}
