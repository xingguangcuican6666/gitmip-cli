package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so creating an issue is a REST POST.
//
// The request body is Gitea's CreateIssueOption, which differs from GitHub's
// createIssue input in three ways that matter.
//
// Assignees are logins rather than ids, so no user lookup is needed to assign
// someone by name. Labels and projects are numeric ids, and a milestone is a
// numeric id rather than a title, so those three are resolved against the
// repository's own lists before the request is sent.
//
// There is no issue template parameter. A template is still useful for its body,
// which the caller sends as the body, but naming one is not something Gitea
// accepts.
//
// There is no issue type, no project-by-title and no parent or sub-issue
// relationship, so those flags are refused by the caller rather than dropped
// here: a create that silently ignored them would look like it had worked.

// IssueCreateGiteaInput is the set of fields Gitea's create endpoint accepts
// that gh can fill.
type IssueCreateGiteaInput struct {
	Title       string
	Body        string
	Assignees   []string
	LabelIDs    []int64
	ProjectIDs  []int64
	MilestoneID int64
}

// giteaCreateIssueOption is Gitea's CreateIssueOption. Fields left out are
// omitted rather than sent empty, because an empty title or an absent assignee
// list has different meanings on the wire and a zero milestone would clear one.
type giteaCreateIssueOption struct {
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Labels    []int64  `json:"labels,omitempty"`
	Projects  []int64  `json:"projects,omitempty"`
	Milestone int64    `json:"milestone,omitempty"`
}

// CreateIssueGitea creates an issue and returns it.
func CreateIssueGitea(client *Client, repo ghrepo.Interface, in IssueCreateGiteaInput) (*Issue, error) {
	path := fmt.Sprintf("repos/%s/%s/issues", repo.RepoOwner(), repo.RepoName())

	option := giteaCreateIssueOption{
		Title:     in.Title,
		Body:      in.Body,
		Assignees: in.Assignees,
		Labels:    in.LabelIDs,
		Projects:  in.ProjectIDs,
	}
	if in.MilestoneID != 0 {
		option.Milestone = in.MilestoneID
	}

	payload, err := json.Marshal(option)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize: %w", err)
	}

	// Request rather than REST, so the created issue is read from a body that a
	// future Gitea release might return empty on success without turning the
	// create into a decode failure. A 201 with no body still yields no URL, and
	// that is reported rather than guessed at.
	resp, err := client.Request(repo.RepoHost(), http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var raw giteaIssue
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("issue was created but its details could not be read: %w", err)
	}

	return mapGiteaIssue(raw, repo.RepoHost()), nil
}
