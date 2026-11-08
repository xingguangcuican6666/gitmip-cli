package create

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
)

// createGiteaPullRequest opens the pull request on a Gitea host.
//
// The metadata gm collects through AddMetadataToIssueParams is expressed here as
// logins and numeric ids, which is what Gitea's create endpoint takes. The
// params map those helpers build is GitHub-shaped: node ids, project ids and a
// separate update call afterwards. None of that applies, so the metadata state
// is read directly instead of going through the map.
//
// Gitea's create has no draft field, so --draft is refused before any request.
func createGiteaPullRequest(client *api.Client, repo ghrepo.Interface, opts CreateOptions, ctx CreateContext, state shared.IssueMetadataState) (*api.PullRequest, error) {
	if state.Draft {
		return nil, errors.New("Gitea's pull request API has no draft field, so `--draft` cannot be applied")
	}

	if len(state.ProjectTitles) > 0 {
		return nil, errors.New("Gitea has no project field on a pull request, so `--project` cannot be applied")
	}

	labelIDs, err := giteaLabelIDs(state)
	if err != nil {
		return nil, err
	}

	milestoneID, err := giteaCreateMilestoneID(state)
	if err != nil {
		return nil, err
	}

	assignees, reviewers, err := giteaAssigneesAndReviewers(state)
	if err != nil {
		return nil, err
	}

	in := api.CreatePullRequestGiteaInput{
		Title:               state.Title,
		Body:                state.Body,
		Base:                ctx.PRRefs.BaseRef(),
		Head:                ctx.PRRefs.QualifiedHeadRef(),
		Assignees:           assignees,
		Reviewers:           reviewers,
		Labels:              labelIDs,
		Milestone:           milestoneID,
		AllowMaintainerEdit: opts.MaintainerCanModify,
	}

	return api.CreatePullRequestGitea(client, repo, in)
}

// giteaLabelIDs resolves the label names the caller gave to Gitea's numeric ids.
//
// Gitea takes ids on write, so unlike the read path there is nothing to fall back
// on. RepoMetadataResult already resolved the names against the repository's
// labels, and for Gitea those ids are the numeric label ids rendered as strings.
func giteaLabelIDs(state shared.IssueMetadataState) ([]int64, error) {
	if len(state.Labels) == 0 {
		return nil, nil
	}
	if state.MetadataResult == nil {
		return nil, fmt.Errorf("the repository's labels were not loaded, so %v cannot be resolved", state.Labels)
	}

	rawIDs, err := state.MetadataResult.LabelsToIDs(state.Labels)
	if err != nil {
		return nil, fmt.Errorf("could not add label: %w", err)
	}

	ids := make([]int64, 0, len(rawIDs))
	for i, raw := range rawIDs {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("label %q has a non-numeric id %q, which Gitea cannot use on create", state.Labels[i], raw)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// giteaCreateMilestoneID resolves the one milestone name the caller gave.
func giteaCreateMilestoneID(state shared.IssueMetadataState) (int64, error) {
	if len(state.Milestones) == 0 {
		return 0, nil
	}
	if state.MetadataResult == nil {
		return 0, fmt.Errorf("the repository's milestones were not loaded, so %q cannot be resolved", state.Milestones[0])
	}

	raw, err := state.MetadataResult.MilestoneToID(state.Milestones[0])
	if err != nil {
		return 0, fmt.Errorf("could not add to milestone '%s': %w", state.Milestones[0], err)
	}

	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("milestone %q has a non-numeric id %q, which Gitea cannot use on create", state.Milestones[0], raw)
	}
	return id, nil
}

// giteaAssigneesAndReviewers splits the metadata into the two login lists Gitea
// takes.
//
// A reviewer with a slash in it is a team on GitHub. Gitea does have
// team_reviewers, but gm cannot enumerate an organisation's teams on a Gitea
// host, so such a reviewer is refused rather than dropped.
func giteaAssigneesAndReviewers(state shared.IssueMetadataState) (assignees, reviewers []string, err error) {
	assignees = append(assignees, state.Assignees...)

	for _, r := range state.Reviewers {
		if strings.ContainsRune(r, '/') {
			return nil, nil, fmt.Errorf("Gitea has no team reviewers gm can resolve, so %q cannot be requested", r)
		}
		reviewers = append(reviewers, r)
	}

	return assignees, reviewers, nil
}
