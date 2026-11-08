package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so the issue mutations gm issues as GraphQL
// mutations are REST calls here.
//
// Every one of them addresses the issue by repository path plus index, since
// that is the only handle Gitea offers. GitHub's mutations take a node ID, which
// is why the callers pass an *Issue rather than an id string: the index lives on
// the issue.
//
// Closing and reopening go through PATCH /issues/{index} with a state field.
// That endpoint handles pull requests too; Gitea's implementation loads the pull
// request when the index belongs to one and refuses to change the state of a
// merged one, so the caller does not need to branch on issue type.
//
// Gitea records no close reason. `gm issue close --reason` and `--duplicate-of`
// therefore have nowhere to put their value, and the callers reject them rather
// than accepting a flag that would be silently dropped.
//
// Successful writes answer 204 with no body, or 201 with the updated object, so
// the response is drained rather than decoded: api.Client.REST parses every 2xx
// body as JSON and fails on an empty one.

// SetIssueStateGitea closes or reopens an issue or pull request by index.
func SetIssueStateGitea(client *Client, repo ghrepo.Interface, number int, state string) error {
	path, err := giteaIssuePath(repo, number)
	if err != nil {
		return err
	}
	return sendGiteaIssueMutation(client, repo.RepoHost(), http.MethodPatch, path,
		map[string]string{"state": state})
}

// DeleteIssueGitea deletes an issue by index.
func DeleteIssueGitea(client *Client, repo ghrepo.Interface, number int) error {
	path, err := giteaIssuePath(repo, number)
	if err != nil {
		return err
	}
	return sendGiteaIssueMutation(client, repo.RepoHost(), http.MethodDelete, path, nil)
}

// LockIssueGitea locks an issue. Gitea stores the reason as free text rather
// than an enum, and does not validate it, so GitHub's reason values are passed
// through unchanged.
func LockIssueGitea(client *Client, repo ghrepo.Interface, number int, reason string) error {
	path, err := giteaIssueSubPath(repo, number, "lock")
	if err != nil {
		return err
	}

	payload := map[string]string{}
	if reason != "" {
		payload["lock_reason"] = reason
	}

	return sendGiteaIssueMutation(client, repo.RepoHost(), http.MethodPut, path, payload)
}

// UnlockIssueGitea unlocks an issue.
func UnlockIssueGitea(client *Client, repo ghrepo.Interface, number int) error {
	path, err := giteaIssueSubPath(repo, number, "lock")
	if err != nil {
		return err
	}
	return sendGiteaIssueMutation(client, repo.RepoHost(), http.MethodDelete, path, nil)
}

// PinIssueGitea pins an issue to the repository's pinned list.
func PinIssueGitea(client *Client, repo ghrepo.Interface, number int) error {
	path, err := giteaIssueSubPath(repo, number, "pin")
	if err != nil {
		return err
	}
	return sendGiteaIssueMutation(client, repo.RepoHost(), http.MethodPost, path, nil)
}

// UnpinIssueGitea removes an issue from the repository's pinned list.
func UnpinIssueGitea(client *Client, repo ghrepo.Interface, number int) error {
	path, err := giteaIssueSubPath(repo, number, "pin")
	if err != nil {
		return err
	}
	return sendGiteaIssueMutation(client, repo.RepoHost(), http.MethodDelete, path, nil)
}

// ErrGiteaIssueTransferUnsupported explains why `gm issue transfer` cannot work
// on Gitea: it has an endpoint for transferring a repository and none for moving
// an issue between repositories.
var ErrGiteaIssueTransferUnsupported = fmt.Errorf(
	"Gitea has no endpoint for transferring an issue between repositories")

func giteaIssuePath(repo ghrepo.Interface, number int) (string, error) {
	return fmt.Sprintf("repos/%s/%s/issues/%d", repo.RepoOwner(), repo.RepoName(), number), nil
}

func giteaIssueSubPath(repo ghrepo.Interface, number int, sub string) (string, error) {
	base, err := giteaIssuePath(repo, number)
	if err != nil {
		return "", err
	}
	return base + "/" + sub, nil
}

func sendGiteaIssueMutation(client *Client, hostname, method, path string, payload any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to serialize: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	resp, err := client.Request(hostname, method, path, body)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}
