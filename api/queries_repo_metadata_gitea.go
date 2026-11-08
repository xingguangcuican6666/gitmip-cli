package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so the metadata gm resolves names against before
// creating or editing an issue is fetched here over REST.
//
// What exists differs from GitHub's.
//
// Assignees: Gitea answers GET /repos/{o}/{r}/assignees with the users who may
// be assigned, and takes assignee logins rather than ids on write, so the login
// is what gets carried through.
//
// Labels and milestones: both exist and page with limit. Gitea's issue create
// takes label and milestone ids, so the ids are kept even though the write
// parameter names differ from GitHub's.
//
// Projects: Gitea has repository and organization projects, but no board-per-
// issue relationship in the shape gm's project titles assume, and no v1/v2
// distinction. The project fetches report nothing rather than inventing one.
//
// Teams and reviewers: Gitea has no pull request reviewer concept on an issue and
// no team assignment, so both come back empty.

// RepoAssignableUsersGitea lists the users who may be assigned work in a
// repository.
func RepoAssignableUsersGitea(client *Client, repo ghrepo.Interface) ([]AssignableUser, error) {
	var raw []struct {
		ID       int64  `json:"id"`
		Login    string `json:"login"`
		FullName string `json:"full_name"`
	}

	path := fmt.Sprintf("repos/%s/%s/assignees", repo.RepoOwner(), repo.RepoName())
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}

	users := make([]AssignableUser, 0, len(raw))
	for _, u := range raw {
		users = append(users, NewAssignableUser(strconv.FormatInt(u.ID, 10), u.Login, u.FullName))
	}
	return users, nil
}

// RepoLabelsGitea lists every label in a repository, paged.
func RepoLabelsGitea(client *Client, repo ghrepo.Interface) ([]RepoLabel, error) {
	path := fmt.Sprintf("repos/%s/%s/labels", repo.RepoOwner(), repo.RepoName())

	var labels []RepoLabel
	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s?limit=%d&page=%d", path, giteaPageSize, page)

		var raw []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath, nil, &raw); err != nil {
			return nil, err
		}

		for _, l := range raw {
			labels = append(labels, RepoLabel{
				ID:   strconv.FormatInt(l.ID, 10),
				Name: l.Name,
			})
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	return labels, nil
}

// RepoMilestonesGitea lists milestones in a repository, paged.
//
// state filters by Gitea's own open/closed/all vocabulary, which uses the same
// words the GraphQL path does in lowercase. Gitea returns both states by default,
// so a caller asking for one gets a filtered request rather than a filtered
// result set.
func RepoMilestonesGitea(client *Client, repo ghrepo.Interface, state string) ([]RepoMilestone, error) {
	switch state {
	case "open", "closed", "all":
	default:
		return nil, fmt.Errorf("invalid state: %s", state)
	}

	path := fmt.Sprintf("repos/%s/%s/milestones", repo.RepoOwner(), repo.RepoName())

	var milestones []RepoMilestone
	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s?state=%s&limit=%d&page=%d", path, state, giteaPageSize, page)

		var raw []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		}
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath, nil, &raw); err != nil {
			return nil, err
		}

		for _, m := range raw {
			milestones = append(milestones, RepoMilestone{
				ID:    strconv.FormatInt(m.ID, 10),
				Title: m.Title,
			})
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	return milestones, nil
}
