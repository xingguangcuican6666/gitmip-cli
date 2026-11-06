package list

import (
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/hosttype"
)

type OrganizationList struct {
	Organizations []Organization
	TotalCount    int
	User          string
}

type Organization struct {
	Login string
}

func listOrgs(httpClient *http.Client, hostname string, limit int) (*OrganizationList, error) {
	if hosttype.IsGitea(hostname) {
		return listOrgsGitea(httpClient, hostname, limit)
	}

	type response struct {
		User struct {
			Login         string
			Organizations struct {
				TotalCount int
				Nodes      []Organization
				PageInfo   struct {
					HasNextPage bool
					EndCursor   string
				}
			} `graphql:"organizations(first: $limit, after: $endCursor)"`
		}
	}

	query := `query OrganizationList($user: String!, $limit: Int!, $endCursor: String) {
		user(login: $user) {
			login
			organizations(first: $limit, after: $endCursor) {
				totalCount
				nodes {
					login
				}
				pageInfo {
					hasNextPage
					endCursor
				}
			}
		}
	}`

	client := api.NewClientFromHTTP(httpClient)

	user, err := api.CurrentLoginName(client, hostname)
	if err != nil {
		return nil, err
	}

	listResult := OrganizationList{}
	listResult.User = user
	pageLimit := min(limit, 100)
	variables := map[string]any{
		"user": user,
	}

loop:
	for {
		variables["limit"] = pageLimit
		var data response
		err := client.GraphQL(hostname, query, variables, &data)
		if err != nil {
			return nil, err
		}

		listResult.TotalCount = data.User.Organizations.TotalCount

		for _, org := range data.User.Organizations.Nodes {
			listResult.Organizations = append(listResult.Organizations, org)
			if len(listResult.Organizations) == limit {
				break loop
			}
		}

		if data.User.Organizations.PageInfo.HasNextPage {
			variables["endCursor"] = data.User.Organizations.PageInfo.EndCursor
			pageLimit = min(pageLimit, limit-len(listResult.Organizations))
		} else {
			break
		}
	}

	return &listResult, nil
}

// listOrgsGitea lists the organizations the authenticated user belongs to.
//
// Gitea's GET /user/orgs has no GraphQL connection and reports no total count,
// so the count here is the number of organizations fetched: exact when the
// limit is not reached, a lower bound when it is. api.CurrentLoginNameAndOrgs
// owns that request sequence, and reusing it keeps the login lookup in one
// place rather than duplicating it here.
func listOrgsGitea(httpClient *http.Client, hostname string, limit int) (*OrganizationList, error) {
	client := api.NewClientFromHTTP(httpClient)

	user, orgNames, err := api.CurrentLoginNameAndOrgs(client, hostname)
	if err != nil {
		return nil, err
	}

	listResult := &OrganizationList{User: user}
	for _, login := range orgNames {
		if limit > 0 && len(listResult.Organizations) == limit {
			break
		}
		listResult.Organizations = append(listResult.Organizations, Organization{Login: login})
	}

	listResult.TotalCount = len(listResult.Organizations)
	return listResult, nil
}
