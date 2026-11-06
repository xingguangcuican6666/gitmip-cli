package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so creating a pull request is a POST to the
// repository's pulls collection.
//
// The differences from GitHub's createPullRequest mutation, which takes a
// repository node id and puts the branch pair and metadata in one input:
//
// The repository is named in the path, so no node id is resolved first.
//
// head and base are branch names. Gitea also accepts the "owner:branch" form for
// the head, which is how a cross-repository pull request is opened, so the caller
// passes whatever gh resolved.
//
// Metadata travels with the create rather than in a follow-up update: assignees
// and reviewers are logins, labels are numeric ids and the milestone is a numeric
// id. That is one request instead of the three GitHub needs, but it means the
// caller has to resolve ids itself.
//
// Gitea's create has no draft field. A draft pull request is created by putting
// a "[DRAFT]" or "WIP:" prefix on the title in the web UI, and the API does not
// accept the flag at all, so a caller asking for --draft is refused rather than
// silently given an ordinary pull request.

// CreatePullRequestGiteaInput is what gh can express of Gitea's
// CreatePullRequestOption.
type CreatePullRequestGiteaInput struct {
	Title string
	Body  string
	Base  string
	Head  string

	// Assignees and Reviewers are logins. Teams have no counterpart in this
	// input beyond team_reviewers, which needs org teams gh cannot enumerate on
	// a Gitea host.
	Assignees []string
	Reviewers []string

	// Labels and Milestone are numeric ids.
	Labels    []int64
	Milestone int64

	AllowMaintainerEdit bool
}

// giteaCreatePullRequestOption is Gitea's CreatePullRequestOption, with the
// fields gh can set.
type giteaCreatePullRequestOption struct {
	Title               string   `json:"title"`
	Body                string   `json:"body,omitempty"`
	Base                string   `json:"base"`
	Head                string   `json:"head"`
	Assignees           []string `json:"assignees,omitempty"`
	Reviewers           []string `json:"reviewers,omitempty"`
	Labels              []int64  `json:"labels,omitempty"`
	Milestone           int64    `json:"milestone,omitempty"`
	AllowMaintainerEdit bool     `json:"allow_maintainer_edit,omitempty"`
}

// CreatePullRequestGitea opens a pull request and returns it as the API
// describes it.
//
// Gitea answers 201 with the created pull request, so the caller gets the number
// and URL without a follow-up read.
func CreatePullRequestGitea(client *Client, repo ghrepo.Interface, in CreatePullRequestGiteaInput) (*PullRequest, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls", repo.RepoOwner(), repo.RepoName())

	payload, err := json.Marshal(giteaCreatePullRequestOption{
		Title:               in.Title,
		Body:                in.Body,
		Base:                in.Base,
		Head:                in.Head,
		Assignees:           in.Assignees,
		Reviewers:           in.Reviewers,
		Labels:              in.Labels,
		Milestone:           in.Milestone,
		AllowMaintainerEdit: in.AllowMaintainerEdit,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to serialize: %w", err)
	}

	// Request rather than REST, so a 201 whose body is empty is reported as a
	// pull request with no number rather than as a decode failure. The create
	// still happened in that case, and the caller reads the URL rather than the
	// object.
	resp, err := client.Request(repo.RepoHost(), http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var raw giteaPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return &PullRequest{}, nil
		}
		return nil, err
	}

	return mapGiteaPullRequest(raw, repo.RepoHost()), nil
}
