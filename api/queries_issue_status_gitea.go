package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so the three-bucket issue status view is answered
// here from three REST requests instead of one query.
//
// Gitea's list endpoint filters by assigned_by, mentioned_by and created_by,
// which map onto the assigned, mentioned and authored buckets directly. There is
// no combined query, so each bucket costs a request; they are issued in sequence
// because a self-hosted Gitea is likelier than GitHub to rate-limit a burst of
// parallel calls, and three requests is not enough to be worth overlapping.
//
// The total count for each bucket comes from X-Total-Count rather than from the
// length of the returned page, so a bucket with more issues than the limit still
// reports how many there are.

// giteaStatusPageSize is the per-page maximum gitea.com enforces, and also how
// many issues each bucket shows. The GraphQL path asks for ten per bucket; fifty
// is what one Gitea page holds, and the caller trims to whatever it displays.
const giteaStatusPageSize = 50

func IssueStatusGitea(client *Client, repo ghrepo.Interface, options IssueStatusOptions) (*IssuesPayload, error) {
	basePath := fmt.Sprintf("repos/%s/%s/issues", repo.RepoOwner(), repo.RepoName())

	payload := &IssuesPayload{}

	for _, bucket := range []struct {
		field  string
		target *IssuesAndTotalCount
	}{
		{"assigned_by", &payload.Assigned},
		{"mentioned_by", &payload.Mentioned},
		{"created_by", &payload.Authored},
	} {
		issues, total, err := fetchGiteaIssueBucket(client, repo.RepoHost(), basePath, bucket.field, options.Username)
		if err != nil {
			return nil, err
		}
		*bucket.target = IssuesAndTotalCount{Issues: issues, TotalCount: total}
	}

	return payload, nil
}

func fetchGiteaIssueBucket(client *Client, hostname, basePath, filter, username string) ([]Issue, int, error) {
	query := url.Values{}
	query.Set("type", "issues")
	query.Set("state", "open")
	query.Set(filter, username)
	query.Set("limit", strconv.Itoa(giteaStatusPageSize))
	query.Set("page", "1")

	path := basePath + "?" + query.Encode()

	// Request rather than REST, because the total count arrives in a response
	// header that REST does not expose to the caller.
	resp, err := client.Request(hostname, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var raw []giteaIssue
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, 0, err
	}

	issues := make([]Issue, 0, len(raw))
	for _, g := range raw {
		issues = append(issues, *mapGiteaIssue(g, hostname))
	}

	total, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	if total == 0 {
		total = len(issues)
	}

	return issues, total, nil
}
