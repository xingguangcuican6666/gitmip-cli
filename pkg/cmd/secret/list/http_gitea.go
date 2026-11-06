package list

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea's actions secret list differs from GitHub's in three ways.
//
// The response is a bare JSON array rather than an object wrapping a "secrets"
// key, so the anonymous struct getSecrets decodes into would always come back
// empty.
//
// The timestamp field is created_at, not updated_at. Gitea's Secret schema
// carries name, description and created_at only, so there is no updated_at to
// read and the UPDATED column reports the creation time instead.
//
// There is no visibility and no selected-repositories URL. A Gitea secret
// applies to the whole repository or organization it is defined on, so the
// visibility column is dropped rather than shown empty.
//
// Pagination uses limit, and gitea.com caps a page at MAX_RESPONSE_ITEMS.
const giteaSecretPageSize = 50

// giteaSecret is the Secret object Gitea returns.
type giteaSecret struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

// getRepoSecretsGitea lists a repository's Actions secrets.
func getRepoSecretsGitea(client *http.Client, host, owner, repo string) ([]Secret, error) {
	u, err := safeurl.JoinPath("repos", owner, repo, "actions", "secrets")
	if err != nil {
		return nil, err
	}
	return getSecretsGitea(client, host, u)
}

// getOrgSecretsGitea lists an organization's Actions secrets.
func getOrgSecretsGitea(client *http.Client, host, orgName string) ([]Secret, error) {
	u, err := safeurl.JoinPath("orgs", orgName, "actions", "secrets")
	if err != nil {
		return nil, err
	}
	return getSecretsGitea(client, host, u)
}

// getSecretsGitea pages through a Gitea secret list and maps it onto gh's
// Secret type.
func getSecretsGitea(client *http.Client, host string, u *safeurl.MutableSafeURL) ([]Secret, error) {
	apiClient := api.NewClientFromHTTP(client)

	var results []Secret
	for page := 1; ; page++ {
		u.SetQuery("limit", strconv.Itoa(giteaSecretPageSize))
		u.SetQuery("page", strconv.Itoa(page))

		// Request rather than REST, because the response is a bare array and
		// REST would be handed an object to decode it into.
		resp, err := apiClient.Request(host, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}

		var pageResults []giteaSecret
		decodeErr := json.NewDecoder(resp.Body).Decode(&pageResults)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}

		for _, raw := range pageResults {
			results = append(results, Secret{
				Name:      raw.Name,
				UpdatedAt: parseGiteaSecretTime(raw.CreatedAt),
			})
		}

		if len(pageResults) < giteaSecretPageSize {
			break
		}
	}

	return results, nil
}

// parseGiteaSecretTime reads Gitea's RFC 3339 timestamp. An empty or
// unparseable value yields the zero time rather than failing the listing,
// since a missing timestamp is not a reason to hide a secret.
func parseGiteaSecretTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
