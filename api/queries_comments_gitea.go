package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so the comment mutations gm issues as GraphQL
// mutations are REST calls here.
//
// Creating a comment addresses the issue by repository path plus index, because
// Gitea has no node IDs. Pull requests share the issue index space, so the same
// path serves both and the caller does not branch on type.
//
// Updating and deleting address the comment by its numeric id, which is what
// mapGiteaComment puts in Comment.ID. That id is per-instance rather than global,
// which is why these take the repository as well.
//
// Successful writes answer 201 with the comment or 204 with nothing, so the
// response is drained rather than decoded by api.Client.REST, which would fail on
// an empty body.

// CreateCommentGitea posts a comment on an issue or pull request and returns its
// web URL.
func CreateCommentGitea(client *Client, repo ghrepo.Interface, number int, body string) (string, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments", repo.RepoOwner(), repo.RepoName(), number)

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", fmt.Errorf("failed to serialize: %w", err)
	}

	resp, err := client.Request(repo.RepoHost(), http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer func() { resp.Body.Close() }()

	// A 204 would carry no comment to report a URL from, which Gitea does not
	// send here; a decode failure is reported rather than masked so a change in
	// that behaviour is visible.
	var comment giteaComment
	if err := json.NewDecoder(resp.Body).Decode(&comment); err != nil {
		return "", err
	}
	return comment.HTMLURL, nil
}

// UpdateCommentGitea replaces the body of a comment and returns its web URL.
func UpdateCommentGitea(client *Client, repo ghrepo.Interface, commentID int64, body string) (string, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/comments/%d", repo.RepoOwner(), repo.RepoName(), commentID)

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", fmt.Errorf("failed to serialize: %w", err)
	}

	resp, err := client.Request(repo.RepoHost(), http.MethodPatch, path, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer func() { resp.Body.Close() }()

	var comment giteaComment
	if err := json.NewDecoder(resp.Body).Decode(&comment); err != nil {
		return "", err
	}
	return comment.HTMLURL, nil
}

// DeleteCommentGitea removes a comment.
func DeleteCommentGitea(client *Client, repo ghrepo.Interface, commentID int64) error {
	path := fmt.Sprintf("repos/%s/%s/issues/comments/%d", repo.RepoOwner(), repo.RepoName(), commentID)

	resp, err := client.Request(repo.RepoHost(), http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}
