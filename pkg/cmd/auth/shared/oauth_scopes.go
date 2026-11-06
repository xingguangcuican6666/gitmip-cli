package shared

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/safeurl"
)

type MissingScopesError struct {
	MissingScopes []string
}

func (e MissingScopesError) Error() string {
	var missing []string
	for _, s := range e.MissingScopes {
		missing = append(missing, fmt.Sprintf("'%s'", s))
	}
	scopes := strings.Join(missing, ", ")

	if len(e.MissingScopes) == 1 {
		return "missing required scope " + scopes
	}
	return "missing required scopes " + scopes
}

// GetScopes returns the scopes granted to authToken on hostname, as a comma
// separated list.
//
// GitHub reports scopes in the X-Oauth-Scopes response header. Gitea does not
// send that header; it reports the scopes of the current token in the body of
// GET /token instead. Both are asked here because the call sites that need
// scopes, login and status, must work on either kind of host.
//
// The token is passed explicitly because this runs before the token is stored in config, so
// the transport has nothing to attach. The transport only sets Authorization when it is
// absent, so the header set here wins.
func GetScopes(httpClient *http.Client, hostname, authToken string) (string, error) {
	if hosttype.IsGitea(hostname) {
		return giteaTokenScopes(httpClient, hostname, authToken)
	}

	// TODO(api-client-rollout)
	// This line of code is part of a mechanical roll out of the api client.
	// As a follow up, consider whether the api client can be injected to this call site, rather than constructed
	res, err := api.NewClientFromHTTP(httpClient).Request(hostname, http.MethodGet, safeurl.NewImmutableSafeURL("").String(), nil,
		api.WithHeader("Authorization", "token "+authToken))
	if err != nil {
		return "", err
	}

	defer func() {
		// Ensure the response body is fully read and closed
		// before we reconnect, so that we reuse the same TCPconnection.
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}()

	if res.StatusCode != 200 {
		return "", api.UnexpectedStatusError(res)
	}

	return res.Header.Get("X-Oauth-Scopes"), nil
}

// giteaCurrentToken is the subset of Gitea's CurrentAccessToken schema this
// package reads.
type giteaCurrentToken struct {
	Scopes []string `json:"scopes"`
}

// giteaTokenScopes reads the scopes of authToken from GET /token.
//
// Gitea names scopes by resource ("read:repository", "write:issue") rather than
// by GitHub's coarser categories, so the joined list is not interchangeable
// with a GitHub scope header. HasMinimumScopes interprets each host's list with
// its own vocabulary for that reason.
func giteaTokenScopes(httpClient *http.Client, hostname, authToken string) (string, error) {
	res, err := api.NewClientFromHTTP(httpClient).Request(hostname, http.MethodGet, "token", nil,
		api.WithHeader("Authorization", "token "+authToken))
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}()

	var token giteaCurrentToken
	if err := json.NewDecoder(res.Body).Decode(&token); err != nil {
		return "", err
	}
	return strings.Join(token.Scopes, ","), nil
}

// HasMinimumScopes performs a GitHub API request and returns an error if the token used in the request
// lacks the minimum required scopes for performing API operations with gh.
func HasMinimumScopes(httpClient *http.Client, hostname, authToken string) error {
	scopesHeader, err := GetScopes(httpClient, hostname, authToken)
	if err != nil {
		return err
	}

	if hosttype.IsGitea(hostname) {
		return giteaHeaderHasMinimumScopes(scopesHeader)
	}
	return HeaderHasMinimumScopes(scopesHeader)
}

// giteaHeaderHasMinimumScopes reports whether a Gitea token can do the work gh
// does on a repository: read and write repository content, issues and pull
// requests, and read the organizations the user belongs to.
//
// "all" grants everything. Gitea's "write:x" implies "read:x", so only the
// write scope needs checking for operations that modify. An empty list means
// the server reported no scopes, which is treated as unknown rather than
// insufficient, matching how HeaderHasMinimumScopes treats an absent header.
func giteaHeaderHasMinimumScopes(scopesHeader string) error {
	if scopesHeader == "" {
		return nil
	}

	granted := map[string]bool{}
	for s := range strings.SplitSeq(scopesHeader, ",") {
		granted[strings.TrimSpace(s)] = true
	}

	if granted["all"] {
		return nil
	}

	var missingScopes []string
	if !granted["read:repository"] && !granted["write:repository"] {
		missingScopes = append(missingScopes, "read:repository")
	}
	if !granted["write:repository"] {
		missingScopes = append(missingScopes, "write:repository")
	}
	if !granted["read:organization"] && !granted["write:organization"] {
		missingScopes = append(missingScopes, "read:organization")
	}
	if !granted["read:user"] && !granted["write:user"] {
		missingScopes = append(missingScopes, "read:user")
	}

	if len(missingScopes) > 0 {
		return &MissingScopesError{MissingScopes: missingScopes}
	}
	return nil
}

// HeaderHasMinimumScopes parses the comma separated scopesHeader string and returns an error
// if it lacks the minimum required scopes for performing API operations with gh.
func HeaderHasMinimumScopes(scopesHeader string) error {
	if scopesHeader == "" {
		// if the token reports no scopes, assume that it's an integration token and give up on
		// detecting its capabilities
		return nil
	}

	search := map[string]bool{
		"repo":      false,
		"read:org":  false,
		"admin:org": false,
	}
	for s := range strings.SplitSeq(scopesHeader, ",") {
		search[strings.TrimSpace(s)] = true
	}

	var missingScopes []string
	if !search["repo"] {
		missingScopes = append(missingScopes, "repo")
	}

	if !search["read:org"] && !search["write:org"] && !search["admin:org"] {
		missingScopes = append(missingScopes, "read:org")
	}

	if len(missingScopes) > 0 {
		return &MissingScopesError{MissingScopes: missingScopes}
	}
	return nil
}
