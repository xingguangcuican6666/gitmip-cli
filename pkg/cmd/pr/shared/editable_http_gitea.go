package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"golang.org/x/sync/errgroup"
)

// Gitea has no GraphQL API, so the issue edit that GitHub performs as a set of
// mutations is performed here as a set of REST calls.
//
// Three differences shape this.
//
// Labels are replaced rather than patched. PUT /issues/{index}/labels sets the
// whole list, so an edit that adds or removes a label has to send the union of
// what stays and what is added, minus what is removed. The names come from the
// issue's own labels, which mapGiteaIssue already populated, so no extra request
// is needed to know what is there now. Gitea accepts names in that list, which
// avoids resolving ids for labels that were never fetched.
//
// Assignees are added and removed through two endpoints that take logins. Gitea
// has no id-based assignee mutation, and the logins are what gm already has.
//
// A milestone is a numeric id on the wire. `--milestone ""` clears it, which
// Gitea spells as milestone 0; leaving the field out would not clear anything,
// since an omitted field means no change.
//
// Issue type, project and reviewer have no counterpart and are refused by
// giteaIssueEditUnsupported, which callers run before any request.

// UpdateIssueGitea applies an edit to one issue on a Gitea host.
//
// The caller passes the issue rather than an id, because labels and assignees
// are replaced against what is on the issue now, and because Gitea addresses an
// issue by its index while the mapped Issue.ID is its database id. Taking the
// issue keeps that distinction inside one function.
func UpdateIssueGitea(httpClient *http.Client, repo ghrepo.Interface, issue *api.Issue, options Editable) error {
	if err := giteaIssueEditUnsupported(options); err != nil {
		return err
	}

	client := api.NewClientFromHTTP(httpClient)
	basePath := fmt.Sprintf("repos/%s/%s/issues/%d", repo.RepoOwner(), repo.RepoName(), issue.Number)

	var wg errgroup.Group

	// Labels are one replacement rather than two mutations, so the add and
	// remove halves are computed together before either is sent.
	if options.Labels.Edited {
		names, err := giteaLabelNames(issue, options)
		if err != nil {
			return err
		}
		wg.Go(func() error {
			return sendGiteaIssueEdit(client, repo.RepoHost(), http.MethodPut, basePath+"/labels",
				map[string]any{"labels": names})
		})
	}

	if options.Assignees.Edited {
		add, remove, err := giteaAssigneeChanges(issue, options)
		if err != nil {
			return err
		}
		if len(add) > 0 {
			wg.Go(func() error {
				return sendGiteaIssueEdit(client, repo.RepoHost(), http.MethodPost, basePath+"/assignees",
					map[string]any{"assignees": add})
			})
		}
		if len(remove) > 0 {
			wg.Go(func() error {
				return sendGiteaIssueEdit(client, repo.RepoHost(), http.MethodDelete, basePath+"/assignees",
					map[string]any{"assignees": remove})
			})
		}
	}

	// Title, body and milestone go through one PATCH, because Gitea has no
	// per-field endpoint and sending an unedited field would overwrite it.
	patch := map[string]any{}
	if options.Title.Edited {
		patch["title"] = options.Title.Value
	}
	if options.Body.Edited {
		patch["body"] = options.Body.Value
	}
	if options.Milestone.Edited {
		id, err := giteaMilestoneID(client, repo, options.Milestone.Value)
		if err != nil {
			return err
		}
		patch["milestone"] = id
	}
	if len(patch) > 0 {
		wg.Go(func() error {
			return sendGiteaIssueEdit(client, repo.RepoHost(), http.MethodPatch, basePath, patch)
		})
	}

	return wg.Wait()
}

// giteaLabelNames works out the label list that replaces the issue's current one.
func giteaLabelNames(issue *api.Issue, options Editable) ([]string, error) {
	current := make([]string, 0, len(issue.Labels.Nodes))
	onIssue := map[string]bool{}
	for _, l := range issue.Labels.Nodes {
		current = append(current, l.Name)
		onIssue[l.Name] = true
	}

	// A removal that is not on the issue is reported rather than ignored, since
	// a silently dropped --remove-label looks like success to the user.
	removals := map[string]bool{}
	for _, r := range options.Labels.Remove {
		if !onIssue[r] {
			return nil, fmt.Errorf("label %q is not on this issue", r)
		}
		removals[r] = true
	}

	kept := make([]string, 0, len(current))
	seen := map[string]bool{}
	for _, name := range current {
		if removals[name] {
			continue
		}
		kept = append(kept, name)
		seen[name] = true
	}

	for _, add := range options.Labels.Add {
		if !seen[add] {
			kept = append(kept, add)
			seen[add] = true
		}
	}

	// A value-only edit, which is what the interactive survey produces, sets
	// Value with no Add or Remove, so it replaces the list outright.
	if len(options.Labels.Add) == 0 && len(options.Labels.Remove) == 0 && options.Labels.Value != nil {
		kept = kept[:0]
		seen = map[string]bool{}
		for _, name := range options.Labels.Value {
			if !seen[name] {
				kept = append(kept, name)
				seen[name] = true
			}
		}
	}

	return kept, nil
}

// giteaAssigneeChanges splits an assignee edit into the logins to add and the
// logins to remove, relative to what is assigned now.
func giteaAssigneeChanges(issue *api.Issue, options Editable) (add, remove []string, err error) {
	current := map[string]bool{}
	for _, a := range issue.Assignees.Nodes {
		current[a.Login] = true
	}

	// A value-only edit sets Value without Add or Remove, so the difference has
	// to be computed from it.
	if len(options.Assignees.Add) == 0 && len(options.Assignees.Remove) == 0 && options.Assignees.Value != nil {
		wanted := map[string]bool{}
		for _, login := range options.Assignees.Value {
			wanted[login] = true
			if !current[login] {
				add = append(add, login)
			}
		}
		for login := range current {
			if !wanted[login] {
				remove = append(remove, login)
			}
		}
		return add, remove, nil
	}

	for _, login := range options.Assignees.Add {
		if login != "" && !current[login] {
			add = append(add, login)
		}
	}
	for _, login := range options.Assignees.Remove {
		if login != "" && current[login] {
			remove = append(remove, login)
		}
	}

	return add, remove, nil
}

// giteaMilestoneID resolves a milestone to the numeric id the PATCH wants.
//
// An empty value or noMilestone clears the milestone, which Gitea spells as 0.
// A title is looked up against the repository's milestones, matching what the
// GitHub path resolves through RepoMetadataResult.
func giteaMilestoneID(client *api.Client, repo ghrepo.Interface, value string) (int64, error) {
	if value == "" || value == noMilestone {
		return 0, nil
	}

	if id, err := strconv.ParseInt(value, 10, 64); err == nil {
		return id, nil
	}

	milestones, err := api.RepoMilestonesGitea(client, repo, "all")
	if err != nil {
		return 0, err
	}
	for _, m := range milestones {
		if m.Title == value {
			id, err := strconv.ParseInt(m.ID, 10, 64)
			if err != nil {
				return 0, err
			}
			return id, nil
		}
	}

	return 0, fmt.Errorf("could not find milestone %q in %s", value, ghrepo.FullName(repo))
}

func sendGiteaIssueEdit(client *api.Client, hostname, method, path string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}

	// Request rather than REST, because a successful write answers 200 or 201
	// with the updated issue or with nothing at all, and REST decodes every 2xx
	// body as JSON into a receiver there is none for.
	resp, err := client.Request(hostname, method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// giteaIssueEditUnsupported reports why an edit cannot be applied on a Gitea
// host. Callers run it before any request, so the user hears it about the flag
// they passed rather than after a lookup has already happened.
func giteaIssueEditUnsupported(options Editable) error {
	var errs []error
	if options.IssueType.Edited {
		errs = append(errs, fmt.Errorf("Gitea has no issue types, so `--type` is not supported"))
	}
	if options.Reviewers.Edited {
		errs = append(errs, fmt.Errorf("Gitea has no pull request reviewers on an issue, so `--reviewer` is not supported"))
	}
	if options.Projects.Edited && (len(options.Projects.Value) > 0 || len(options.Projects.Add) > 0) {
		errs = append(errs, fmt.Errorf("Gitea projects cannot be addressed by title, so `--project` is not supported"))
	}
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}
