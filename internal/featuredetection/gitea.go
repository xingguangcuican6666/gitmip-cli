package featuredetection

import (
	"net/http"

	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/hosttype"
)

// giteaDetector answers capability questions for a Gitea host.
//
// GitHub capability detection introspects the GraphQL schema, which Gitea does
// not have. Every flag here is therefore a constant that says what Gitea does
// and does not offer, so a command that branches on capabilities takes the
// right path without issuing a request that cannot succeed.
//
// Returning a detector rather than an error matters: gh asks these questions on
// paths that work fine on Gitea, such as listing repositories. A detection
// failure would turn a supported command into a hard error.
type giteaDetector struct {
	httpClient *http.Client
	host       string
}

// newGiteaDetector returns a detector for a Gitea host, or nil when host is not
// one. NewDetector uses it to keep host-type resolution in a single place.
func newGiteaDetector(httpClient *http.Client, host string) Detector {
	if !hosttype.IsGitea(host) {
		return nil
	}
	return &giteaDetector{httpClient: httpClient, host: host}
}

func (d *giteaDetector) IssueFeatures() (IssueFeatures, error) {
	// Gitea has no assignable-actor union type and no sub-issue or blocked-by
	// relationships, so both actor-based and relationship-based paths are off.
	return IssueFeatures{}, nil
}

func (d *giteaDetector) PullRequestFeatures() (PullRequestFeatures, error) {
	// No merge queue, and no check runs: Gitea reports commit status through a
	// combined-status endpoint that carries no per-context counts.
	return PullRequestFeatures{}, nil
}

func (d *giteaDetector) RepositoryFeatures() (RepositoryFeatures, error) {
	// Gitea has no pull request template query and no auto-merge. It does
	// distinguish private, internal and public repositories, and reports that
	// as the `internal` and `private` flags rather than a `visibility` field,
	// so VisibilityField stays false to keep callers off the GitHub-only shape.
	return RepositoryFeatures{}, nil
}

func (d *giteaDetector) ProjectsV1() gh.ProjectsV1Support {
	// Reported as unsupported even though Gitea has project boards. The flag
	// gates GitHub's GraphQL projectCards field, which a Gitea host cannot
	// serve; reporting support would send a query that fails rather than a
	// request the host can answer. Gitea boards live at
	// /repos/{owner}/{repo}/projects and are reached through their own path.
	return gh.ProjectsV1Unsupported
}

func (d *giteaDetector) ProjectFeatures() (ProjectFeatures, error) {
	// No GraphQL projectsV2, so the items query argument does not exist.
	return ProjectFeatures{}, nil
}

func (d *giteaDetector) SearchFeatures() (SearchFeatures, error) {
	// Gitea has no query syntax. /repos/issues/search takes enumerated
	// parameters such as assigned, mentioned and review_requested, which is
	// the same shape as GitHub's non-advanced issue search.
	return advancedIssueSearchNotSupported, nil
}

func (d *giteaDetector) ReleaseFeatures() (ReleaseFeatures, error) {
	// No immutable releases.
	return ReleaseFeatures{}, nil
}

func (d *giteaDetector) ActionsFeatures() (ActionsFeatures, error) {
	// Gitea accepts return_run_details, but as a query parameter rather than as
	// a field of the dispatch body. The flag says the server can answer with the
	// created run, which it can, and the workflow run command sends it in the
	// place Gitea looks for it.
	return ActionsFeatures{DispatchRunDetails: true}, nil
}
