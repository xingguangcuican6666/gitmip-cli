package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so merging a pull request is a REST POST.
//
// POST /repos/{o}/{r}/pulls/{index}/merge takes a MergePullRequestOption, which
// differs from GitHub's mergePullRequest input in the ways that matter here.
//
// The merge method is a string enum rather than a separate field per method.
// Gitea offers more methods than gh exposes: rebase-merge and fast-forward-only
// have no gh flag, and gh's three map onto merge, rebase and squash.
//
// A merge is addressed by index rather than by node id, so callers pass the
// number.
//
// Commit subject and body are merge_title_field and merge_message_field.
//
// head_commit_id is Gitea's equivalent of expectedHeadOid: the merge is refused
// if the head has moved since. That is what makes --match-head-commit safe here
// rather than a no-op.
//
// merge_when_checks_succeed is Gitea's auto-merge. It is a field on the merge
// request rather than a separate enable/disable endpoint, so there is no way to
// turn it back off through the API: a caller that asks for --disable-auto is told
// so rather than getting a request that silently does nothing.
//
// A successful merge answers 200 with the updated pull request or 204 with
// nothing, depending on the Gitea version, so the body is drained rather than
// decoded.

// PullRequestMergeMethodGitea names the merge strategies Gitea's `do` field
// accepts that gh can ask for.
type PullRequestMergeMethodGitea string

const (
	GiteaMergeMethodMerge           PullRequestMergeMethodGitea = "merge"
	GiteaMergeMethodRebase          PullRequestMergeMethodGitea = "rebase"
	GiteaMergeMethodSquash          PullRequestMergeMethodGitea = "squash"
	GiteaMergeMethodFastForwardOnly PullRequestMergeMethodGitea = "fast-forward-only"
	GiteaMergeMethodRebaseMerge     PullRequestMergeMethodGitea = "rebase-merge"
)

// MergePullRequestGiteaInput is what gh can express of Gitea's merge options.
type MergePullRequestGiteaInput struct {
	Method PullRequestMergeMethodGitea

	// MergeTitle and MergeMessage set the merge commit's subject and body. An
	// empty MergeTitle leaves Gitea's own default in place, which is what a
	// caller that did not pass --subject wants.
	MergeTitle   string
	MergeMessage string
	SetMessage   bool

	// HeadCommitID refuses the merge if the head has moved, which is what
	// --match-head-commit asks for.
	HeadCommitID string

	DeleteBranchAfterMerge bool

	// MergeWhenChecksSucceed defers the merge until the pull request's checks
	// pass. Gitea has no way to cancel it afterwards through the API.
	MergeWhenChecksSucceed bool
}

// giteaMergePullRequestOption is Gitea's MergePullRequestOption.
type giteaMergePullRequestOption struct {
	Do                     string `json:"do"`
	ForceMerge             bool   `json:"force_merge,omitempty"`
	MergeTitleField        string `json:"merge_title_field,omitempty"`
	MergeMessageField      string `json:"merge_message_field,omitempty"`
	HeadCommitID           string `json:"head_commit_id,omitempty"`
	DeleteBranchAfterMerge bool   `json:"delete_branch_after_merge,omitempty"`
	MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed,omitempty"`
}

// MergePullRequestGitea merges one pull request by index.
//
// force bypasses branch protection, which is Gitea's force_merge and what gh's
// --admin asks for. It is an argument rather than a field on the input because
// nothing else in the input depends on privileges: a caller that is not an admin
// passes false and Gitea answers 405 rather than merging.
func MergePullRequestGitea(client *Client, repo ghrepo.Interface, number int, in MergePullRequestGiteaInput, force bool) error {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/merge", repo.RepoOwner(), repo.RepoName(), number)

	option := giteaMergePullRequestOption{
		Do:                     string(in.Method),
		ForceMerge:             force,
		MergeTitleField:        in.MergeTitle,
		HeadCommitID:           in.HeadCommitID,
		DeleteBranchAfterMerge: in.DeleteBranchAfterMerge,
		MergeWhenChecksSucceed: in.MergeWhenChecksSucceed,
	}

	// An unset body and an explicitly empty one are different: the first keeps
	// Gitea's generated message, the second asks for none. SetMessage is what
	// distinguishes them, and the field is only sent when it is set.
	if in.SetMessage {
		option.MergeMessageField = in.MergeMessage
	}

	payload, err := json.Marshal(option)
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}

	resp, err := client.Request(repo.RepoHost(), http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// ErrGiteaAutoMergeDisableUnsupported explains why --disable-auto cannot work on
// a Gitea host: auto-merge is a field on the merge request, and Gitea exposes no
// endpoint that clears it.
var ErrGiteaAutoMergeDisableUnsupported = fmt.Errorf(
	"Gitea has no endpoint for disabling auto-merge, so `--disable-auto` is not supported")
