package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so a review is a POST to the pull request's reviews
// collection.
//
// The shape differs from GitHub's addPullRequestReview mutation in three ways.
//
// The pull request is addressed by index rather than by node id.
//
// The verdict is the event field, whose vocabulary is Gitea's own:
// APPROVED, COMMENT and REQUEST_CHANGES for the three things gh can ask for.
// Gitea also accepts PENDING and REQUEST_REVIEW, which are not reviews and are
// never sent from here.
//
// Gitea takes one body for the review and a separate comments array for inline
// comments. gh's review command only writes the body, so the array is left out.
// An empty body is omitted rather than sent as an empty string, which Gitea
// treats as a review with no text rather than as a missing field.

// GiteaReviewEvent names the verdict values Gitea's review endpoint accepts that
// gh can ask for.
const (
	GiteaReviewEventApproved       = "APPROVED"
	GiteaReviewEventComment        = "COMMENT"
	GiteaReviewEventRequestChanges = "REQUEST_CHANGES"
)

// giteaCreateReviewOption is Gitea's CreatePullReviewOptions.
type giteaCreateReviewOption struct {
	Event    string `json:"event"`
	Body     string `json:"body,omitempty"`
	CommitID string `json:"commit_id,omitempty"`
}

// AddReviewGitea creates a review on a pull request.
//
// commitID pins the review to a commit; Gitea accepts it empty and then reviews
// whatever the head is when the request arrives, which is what gh does by
// default.
func AddReviewGitea(client *Client, repo ghrepo.Interface, number int, event, body, commitID string) error {
	switch event {
	case GiteaReviewEventApproved, GiteaReviewEventComment, GiteaReviewEventRequestChanges:
	default:
		return fmt.Errorf("unsupported review event %q", event)
	}

	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", repo.RepoOwner(), repo.RepoName(), number)

	payload, err := json.Marshal(giteaCreateReviewOption{
		Event:    event,
		Body:     body,
		CommitID: commitID,
	})
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

// GiteaReviewEventFor maps gh's review state onto Gitea's event vocabulary.
func GiteaReviewEventFor(state PullRequestReviewState) string {
	switch state {
	case ReviewApprove:
		return GiteaReviewEventApproved
	case ReviewRequestChanges:
		return GiteaReviewEventRequestChanges
	default:
		return GiteaReviewEventComment
	}
}
