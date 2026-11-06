package set

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/secret/shared"
)

// Gitea does not encrypt secrets client-side. GitHub hands out a per-scope
// libsodium public key at /actions/secrets/public-key and expects the value
// sealed against it before the PUT; Gitea has no such endpoint and takes the
// value as plaintext in the request body over TLS, storing it itself.
//
// That removes the key fetch, the sealed-box encryption and the base64
// encoding, and with them the `--no-store` option, which prints a value that
// only GitHub's key could have produced.
//
// The path also differs. Gitea stores Actions secrets under a fixed "actions"
// segment rather than a per-application one, since Actions is the only secret
// store it has, and PUT is create-or-update so no separate create call exists.

// errGiteaNoStoreUnsupported explains why --no-store cannot work on Gitea.
var errGiteaNoStoreUnsupported = errors.New(
	"`--no-store` is not supported on Gitea, which takes the secret value in plaintext rather than encrypted against a server key")

// giteaCreateOrUpdateSecretOption is Gitea's request body for PUT /secrets/{name}.
type giteaCreateOrUpdateSecretOption struct {
	Data string `json:"data"`
}

// setSecretGitea stores one secret in plaintext.
func setSecretGitea(host string, client *api.Client, baseRepo ghrepo.Interface, orgName string, entity shared.SecretEntity, app shared.App, secretKey string, secret []byte) (res setResult) {
	res.key = secretKey

	if err := shared.IsSupportedOnGitea(app, entity); err != nil {
		res.err = err
		return
	}

	path, err := giteaSecretPath(entity, orgName, baseRepo, secretKey)
	if err != nil {
		res.err = fmt.Errorf("failed to set secret %q: %w", secretKey, err)
		return
	}

	body, err := json.Marshal(giteaCreateOrUpdateSecretOption{Data: string(secret)})
	if err != nil {
		res.err = fmt.Errorf("failed to serialize: %w", err)
		return
	}

	// PUT is create-or-update on Gitea, so there is no 409 to branch on and no
	// separate create step. Request rather than REST, because Gitea answers a
	// successful create with 201 and an empty body, and REST decodes every 2xx
	// body as JSON.
	resp, err := client.Request(host, http.MethodPut, path.String(), bytes.NewReader(body))
	if err != nil {
		res.err = fmt.Errorf("failed to set secret %q: %w", secretKey, err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	return res
}

// giteaSecretPath builds the path for one secret. Gitea has no deployment
// environments, so that entity has no path.
func giteaSecretPath(entity shared.SecretEntity, orgName string, baseRepo ghrepo.Interface, secretName string) (safeurl.SafeURL, error) {
	switch entity {
	case shared.Organization:
		return safeurl.JoinPath("orgs", orgName, "actions", "secrets", secretName)
	case shared.User:
		return safeurl.JoinPath("user", "actions", "secrets", secretName)
	case shared.Repository:
		return safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), "actions", "secrets", secretName)
	default:
		return nil, fmt.Errorf("Gitea has no deployment environments, so `--env` is not supported")
	}
}

// setRunGitea stores every secret in the batch, mirroring the GitHub flow's
// output without the key fetch or encryption.
//
// The writes are sequential rather than the goroutine fan-out the GitHub path
// uses. Gitea has no per-request key material to fetch, so there is no slow
// step to overlap, and sequential writes keep the error reporting in the order
// the secrets were given.
func setRunGitea(opts *SetOptions, client *api.Client, host string, baseRepo ghrepo.Interface, orgName string, secrets map[string][]byte, app shared.App, entity shared.SecretEntity) error {
	if opts.DoNotStore {
		return errGiteaNoStoreUnsupported
	}

	var errs []error
	cs := opts.IO.ColorScheme()

	for secretKey, secret := range secrets {
		result := setSecretGitea(host, client, baseRepo, orgName, entity, app, secretKey, secret)
		if result.err != nil {
			errs = append(errs, result.err)
			continue
		}
		if !opts.IO.IsStdoutTTY() {
			continue
		}

		target := orgName
		if opts.UserSecrets {
			target = "your user"
		} else if orgName == "" {
			target = ghrepo.FullName(baseRepo)
		}
		fmt.Fprintf(opts.IO.Out, "%s Set Actions secret %s for %s\n", cs.SuccessIcon(), result.key, target)
	}

	return errors.Join(errs...)
}
