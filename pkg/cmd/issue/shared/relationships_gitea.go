package shared

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so the issue relationships that GitHub applies as
// mutations are applied here as REST calls.
//
// Two of the four relationships gm models exist on Gitea. Blocked-by and
// blocking map onto Gitea's dependencies and blocks endpoints, which name the
// other issue by owner, repo and index rather than by a node id.
//
// Parent and sub-issue do not exist: Gitea has no issue hierarchy. Issue type
// does not exist either. Callers refuse those flags rather than dropping them,
// because a flag that silently did nothing would look like it had worked.

// ErrGiteaSubIssuesUnsupported explains why --parent and --add-sub-issue cannot
// work on a Gitea host.
var ErrGiteaSubIssuesUnsupported = errors.New("Gitea has no sub-issues, so `--parent`, `--remove-parent`, `--add-sub-issue` and `--remove-sub-issue` are not supported")

// ErrGiteaIssueTypeUnsupported explains why --type cannot work on a Gitea host.
var ErrGiteaIssueTypeUnsupported = errors.New("Gitea has no issue types, so `--type` is not supported")

// giteaIssueMeta is Gitea's IssueMeta, which names an issue by repository and
// index rather than by a global id.
type giteaIssueMeta struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Index int64  `json:"index"`
}

// GiteaRelationshipChanges is the set of relationship edits to apply to one
// issue. Each list holds issue references: a number, a "#number", or a URL.
type GiteaRelationshipChanges struct {
	AddBlockedBy    []string
	RemoveBlockedBy []string
	AddBlocking     []string
	RemoveBlocking  []string
}

// HasChanges reports whether there is anything to apply.
func (c GiteaRelationshipChanges) HasChanges() bool {
	return len(c.AddBlockedBy) > 0 || len(c.RemoveBlockedBy) > 0 ||
		len(c.AddBlocking) > 0 || len(c.RemoveBlocking) > 0
}

// UpdateGiteaRelationships applies a set of blocked-by and blocking changes to
// one issue.
//
// Gitea models "A is blocked by B" as a dependency of A and "A blocks B" as a
// block of A. That is the same pairing the GitHub path expresses by swapping the
// arguments of one mutation, but here the two halves go to different endpoints.
//
// Failures are collected rather than aborting the rest, matching the joined
// error the GitHub path returns, so one bad reference does not hide the others.
func UpdateGiteaRelationships(client *api.Client, repo ghrepo.Interface, issueNumber int, changes GiteaRelationshipChanges) error {
	type operation struct {
		path   string
		method string
		refs   []string
	}

	operations := []operation{
		{"dependencies", http.MethodPost, changes.AddBlockedBy},
		{"dependencies", http.MethodDelete, changes.RemoveBlockedBy},
		{"blocks", http.MethodPost, changes.AddBlocking},
		{"blocks", http.MethodDelete, changes.RemoveBlocking},
	}

	var errs []error
	for _, op := range operations {
		for _, ref := range op.refs {
			meta, err := giteaIssueMetaForRef(repo, ref)
			if err != nil {
				errs = append(errs, err)
				continue
			}

			path := fmt.Sprintf("repos/%s/%s/issues/%d/%s",
				repo.RepoOwner(), repo.RepoName(), issueNumber, op.path)
			if err := sendGiteaRelationship(client, repo.RepoHost(), op.method, path, meta); err != nil {
				errs = append(errs, fmt.Errorf("failed to update %s for %q: %w", op.path, ref, err))
			}
		}
	}

	return errors.Join(errs...)
}

// giteaIssueMetaForRef resolves an issue reference to the owner, repo and index
// Gitea wants. A reference may name a different repository on the same host, so
// the parsed repository is used when the reference carried one.
func giteaIssueMetaForRef(baseRepo ghrepo.Interface, ref string) (giteaIssueMeta, error) {
	number, repo, err := ParseIssueFromArg(ref)
	if err != nil {
		return giteaIssueMeta{}, err
	}

	targetRepo := baseRepo
	if r, ok := repo.Value(); ok {
		if r.RepoHost() != baseRepo.RepoHost() {
			return giteaIssueMeta{}, fmt.Errorf(
				"issue reference %q belongs to a different host (%s) than the current repository (%s)",
				ref, r.RepoHost(), baseRepo.RepoHost())
		}
		targetRepo = r
	}

	return giteaIssueMeta{
		Owner: targetRepo.RepoOwner(),
		Repo:  targetRepo.RepoName(),
		Index: int64(number),
	}, nil
}

func sendGiteaRelationship(client *api.Client, hostname, method, path string, meta giteaIssueMeta) error {
	body, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}

	// Request rather than REST, because the response is the updated dependency or
	// block list and the caller does not need it: REST would decode every 2xx
	// body into a receiver there is none for.
	resp, err := client.Request(hostname, method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}
