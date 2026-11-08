package shared

import (
	"fmt"
)

// Gitea supports Actions secrets only. It has no Codespaces, Dependabot or
// Agents secret stores, and no deployment environments, so three of the four
// entity and app combinations gm models have nothing to talk to.
//
// Rejecting them here rather than at the request keeps the error on the flag
// the user actually passed, and keeps the messages in one place across list,
// set and delete.

// IsSupportedOnGitea reports whether Gitea has a store for this entity and app,
// and returns the reason it does not when it has none.
func IsSupportedOnGitea(app App, entity SecretEntity) error {
	switch entity {
	case Repository, Organization, User:
	case Environment:
		return fmt.Errorf("Gitea has no deployment environments, so `--env` is not supported")
	default:
		return fmt.Errorf("unknown secret entity %q", entity)
	}

	switch app {
	case Actions:
		return nil
	case Codespaces:
		return fmt.Errorf("Gitea has no Codespaces secrets; only `--app actions` is available")
	case Dependabot:
		return fmt.Errorf("Gitea has no Dependabot secrets; only `--app actions` is available")
	case Agents:
		return fmt.Errorf("Gitea has no Agents secrets; only `--app actions` is available")
	default:
		return fmt.Errorf("Gitea supports only `--app actions`, not `--app %s`", app)
	}
}

// SupportsListingUserSecretsOnGitea reports whether user-level secrets can be
// listed. Gitea can set and delete them at /user/actions/secrets/{name} but has
// no collection endpoint, so there is nothing to list.
const SupportsListingUserSecretsOnGitea = false

// ErrUserSecretsListUnsupportedOnGitea explains why `gm secret list --user`
// cannot work against a Gitea host.
var ErrUserSecretsListUnsupportedOnGitea = fmt.Errorf(
	"Gitea has no endpoint for listing user-level secrets, so `gm secret list --user` is not supported")

// GiteaSecretsNeedEncryption reports whether secrets must be encrypted locally
// before they are sent. GitHub requires libsodium sealed-box encryption against
// a per-repository public key; Gitea takes the plaintext value in the request
// body over TLS and stores it itself, and exposes no public-key endpoint.
const GiteaSecretsNeedEncryption = false
