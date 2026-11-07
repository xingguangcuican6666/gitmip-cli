package shared

import "fmt"

// Gitea has no GraphQL API, and its release endpoints take a narrower set of
// fields than GitHub's do.
//
// CreateReleaseOption accepts tag_name, target_commitish, body, name, draft,
// prerelease and tag_message. EditReleaseOption accepts the same minus
// tag_message. Everything else gh can ask for has no counterpart, and Gitea
// rejects a request carrying a field it does not know rather than ignoring it, so
// the unsupported ones are refused here rather than dropped silently. Dropping
// --latest or --discussion-category would create a release that is not the one
// the caller described.

// giteaReleaseCreateFields is what CreateReleaseOption accepts.
var giteaReleaseCreateFields = map[string]bool{
	"tag_name":         true,
	"target_commitish": true,
	"body":             true,
	"name":             true,
	"draft":            true,
	"prerelease":       true,
	"tag_message":      true,
}

// giteaReleaseEditFields is what EditReleaseOption accepts.
var giteaReleaseEditFields = map[string]bool{
	"tag_name":         true,
	"target_commitish": true,
	"body":             true,
	"name":             true,
	"draft":            true,
	"prerelease":       true,
}

// giteaUnsupportedReleaseFields names why each unsupported field cannot be sent.
var giteaUnsupportedReleaseFields = map[string]string{
	"make_latest":              "Gitea has no latest-release marker; the newest published release is the latest",
	"discussion_category_name": "Gitea has no discussions",
	"generate_release_notes":   "Gitea has no release-notes generation endpoint",
	"immutable":                "Gitea releases are not immutable",
	"body_path":                "Gitea has no body_path field",
}

// CheckGiteaReleaseParams reports the first parameter Gitea cannot accept.
//
// A parameter that is present but empty is not a problem: gh only sets the fields
// the caller asked for, so an empty value means the caller did not ask.
func CheckGiteaReleaseParams(params map[string]any, allowed map[string]bool) error {
	for key, value := range params {
		if allowed[key] {
			continue
		}
		if isEmptyReleaseParam(value) {
			continue
		}
		if why, ok := giteaUnsupportedReleaseFields[key]; ok {
			return fmt.Errorf("Gitea cannot %s", why)
		}
		return fmt.Errorf("Gitea's release API has no %q field", key)
	}
	return nil
}

// CheckGiteaReleaseCreateParams checks a create request's parameters.
func CheckGiteaReleaseCreateParams(params map[string]any) error {
	return CheckGiteaReleaseParams(params, giteaReleaseCreateFields)
}

// CheckGiteaReleaseEditParams checks an edit request's parameters.
func CheckGiteaReleaseEditParams(params map[string]any) error {
	return CheckGiteaReleaseParams(params, giteaReleaseEditFields)
}

func isEmptyReleaseParam(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	default:
		return false
	}
}

// NarrowGiteaReleaseParams returns only the parameters Gitea's release endpoints
// accept. It is applied after CheckGiteaReleaseParams has confirmed that any
// unsupported parameter the caller set was empty.
func NarrowGiteaReleaseParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for key, value := range params {
		if giteaReleaseCreateFields[key] {
			out[key] = value
		}
	}
	return out
}
