package create

import (
	"fmt"
	"strconv"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	issueShared "github.com/cli/cli/v2/pkg/cmd/issue/shared"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
)

// Gitea has no GraphQL API, so creating an issue is a REST POST, and three
// things about it differ from GitHub's createIssue mutation.
//
// Assignees are logins, so the @me and Copilot expansion the caller already did
// produces exactly what the wire wants and no user id lookup is needed. Labels,
// projects and a milestone are numeric ids, so those three are resolved against
// the repository's own lists before the request is sent.
//
// There is no issue template parameter, no issue type, and no sub-issue or
// blocked-by relationship in the create body. Relationships go through their own
// endpoints after the issue exists, which is what updateGiteaRelationships does.
//
// An attachment cannot be uploaded before the issue exists: Gitea's asset
// endpoint is /repos/{o}/{r}/issues/{index}/assets, so it needs an index. The
// caller refuses --attach rather than creating an issue whose body promises a
// file that never uploaded.

// createGiteaIssue creates one issue and applies the relationship flags that
// Gitea supports.
func createGiteaIssue(apiClient *api.Client, baseRepo ghrepo.Interface, tb prShared.IssueMetadataState, opts *CreateOptions) (*api.Issue, error) {
	in := api.IssueCreateGiteaInput{
		Title:     tb.Title,
		Body:      tb.Body,
		Assignees: tb.Assignees,
	}

	if len(tb.Labels) > 0 {
		ids, err := giteaLabelIDs(apiClient, baseRepo, tb.Labels)
		if err != nil {
			return nil, err
		}
		in.LabelIDs = ids
	}

	if len(tb.ProjectTitles) > 0 {
		return nil, fmt.Errorf("Gitea projects cannot be addressed by title, so `--project` is not supported")
	}

	if len(tb.Milestones) > 0 {
		id, err := giteaMilestoneID(apiClient, baseRepo, tb.Milestones[0])
		if err != nil {
			return nil, err
		}
		in.MilestoneID = id
	}

	issue, err := api.CreateIssueGitea(apiClient, baseRepo, in)
	if err != nil {
		return nil, err
	}

	if err := applyGiteaCreateRelationships(apiClient, baseRepo, issue, opts); err != nil {
		// The issue exists, so the failure is reported alongside it rather than
		// replacing the URL the user needs.
		return issue, err
	}

	return issue, nil
}

// applyGiteaCreateRelationships applies the relationship flags that Gitea
// supports, and refuses the ones it does not.
func applyGiteaCreateRelationships(apiClient *api.Client, baseRepo ghrepo.Interface, issue *api.Issue, opts *CreateOptions) error {
	if opts.IssueType != "" {
		return issueShared.ErrGiteaIssueTypeUnsupported
	}
	if opts.Parent != "" {
		return issueShared.ErrGiteaSubIssuesUnsupported
	}

	changes := issueShared.GiteaRelationshipChanges{
		AddBlockedBy: opts.BlockedBy,
		AddBlocking:  opts.Blocking,
	}
	if !changes.HasChanges() {
		return nil
	}

	return issueShared.UpdateGiteaRelationships(apiClient, baseRepo, issue.Number, changes)
}

// giteaLabelIDs resolves label names to the ids Gitea's create body wants.
func giteaLabelIDs(apiClient *api.Client, baseRepo ghrepo.Interface, names []string) ([]int64, error) {
	labels, err := api.RepoLabelsGitea(apiClient, baseRepo)
	if err != nil {
		return nil, fmt.Errorf("could not add label: %w", err)
	}

	byName := make(map[string]string, len(labels))
	for _, l := range labels {
		byName[l.Name] = l.ID
	}

	ids := make([]int64, 0, len(names))
	for _, name := range names {
		id, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("could not add label: '%s' not found", name)
		}
		parsed, err := parseGiteaID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, parsed)
	}

	return ids, nil
}

// giteaMilestoneID resolves a milestone title or number to the id Gitea wants.
// An empty value means no milestone, which is what leaving the field out says.
func giteaMilestoneID(apiClient *api.Client, baseRepo ghrepo.Interface, value string) (int64, error) {
	if value == "" {
		return 0, nil
	}

	if id, err := parseGiteaID(value); err == nil {
		return id, nil
	}

	milestones, err := api.RepoMilestonesGitea(apiClient, baseRepo, "all")
	if err != nil {
		return 0, fmt.Errorf("could not add to milestone '%s': %w", value, err)
	}
	for _, m := range milestones {
		if m.Title == value {
			return parseGiteaID(m.ID)
		}
	}

	return 0, fmt.Errorf("could not add to milestone: '%s' not found", value)
}

// parseGiteaID reads one of Gitea's numeric ids, which the metadata helpers
// return as strings because GitHub's are node ids in the same fields.
func parseGiteaID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id %q", value)
	}
	return id, nil
}
