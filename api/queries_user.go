package api

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/internal/hosttype"
)

// The identity of the authenticated user is the one thing almost every command
// needs, and GitHub answers it through GraphQL while Gitea has no GraphQL at
// all. Each helper here therefore picks its own transport: GraphQL for GitHub,
// GET /user for Gitea. Callers stay unaware of the difference.

type Organization struct {
	Login string
}

func CurrentLoginName(client *Client, hostname string) (string, error) {
	if hosttype.IsGitea(hostname) {
		user, err := currentGiteaUser(client, hostname)
		return user.Login, err
	}

	var query struct {
		Viewer struct {
			Login string
		}
	}
	err := client.Query(hostname, "UserCurrent", &query, nil)
	return query.Viewer.Login, err
}

func CurrentLoginNameAndOrgs(client *Client, hostname string) (string, []string, error) {
	if hosttype.IsGitea(hostname) {
		return currentLoginNameAndOrgsGitea(client, hostname)
	}

	var query struct {
		Viewer struct {
			Login         string
			Organizations struct {
				Nodes []Organization
			} `graphql:"organizations(first: 100)"`
		}
	}
	err := client.Query(hostname, "UserCurrent", &query, nil)
	if err != nil {
		return "", nil, err
	}
	orgNames := []string{}
	for _, org := range query.Viewer.Organizations.Nodes {
		orgNames = append(orgNames, org.Login)
	}
	return query.Viewer.Login, orgNames, nil
}

func CurrentUserID(client *Client, hostname string) (string, error) {
	if hosttype.IsGitea(hostname) {
		user, err := currentGiteaUser(client, hostname)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", user.ID), nil
	}

	var query struct {
		Viewer struct {
			ID string
		}
	}
	err := client.Query(hostname, "UserCurrent", &query, nil)
	return query.Viewer.ID, err
}

// giteaUser is the subset of Gitea's User schema these helpers need. Gitea
// identifies users by a numeric id rather than a node id.
// giteaUser is the subset of Gitea's User schema gh reads. FullName is what
// Gitea calls the display name, and gh's Author and GitHubUser types both carry
// a Name alongside the Login.
type giteaUser struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
}

// currentGiteaUser reads the authenticated user from GET /user.
func currentGiteaUser(client *Client, hostname string) (giteaUser, error) {
	var user giteaUser
	err := client.REST(hostname, http.MethodGet, "user", nil, &user)
	return user, err
}

// currentLoginNameAndOrgsGitea reads the login and organization names from
// REST: GET /user for the login, GET /user/orgs for the organization list.
//
// GET /user/orgs is paginated with Gitea's limit parameter. The server caps the
// page size at its own maximum, so the loop follows the Link header that
// RESTWithNext returns rather than assuming how many entries a page holds.
func currentLoginNameAndOrgsGitea(client *Client, hostname string) (string, []string, error) {
	user, err := currentGiteaUser(client, hostname)
	if err != nil {
		return "", nil, err
	}

	orgNames := []string{}
	// An absolute next URL is passed straight back to RESTWithNext, which
	// requests absolute URLs as given.
	path := "user/orgs?limit=50"
	for path != "" {
		var orgs []struct {
			Login string `json:"login"`
		}
		next, err := client.RESTWithNext(hostname, http.MethodGet, path, nil, &orgs)
		if err != nil {
			return "", nil, err
		}
		for _, org := range orgs {
			orgNames = append(orgNames, org.Login)
		}
		path = next
	}

	return user.Login, orgNames, nil
}
