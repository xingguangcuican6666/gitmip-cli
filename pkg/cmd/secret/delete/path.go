package delete

import (
	"fmt"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/gm"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmd/secret/shared"
)

// resolveSecretHost returns the host a secret operation is aimed at. An
// organization or user secret names no repository, so the default host decides
// in those cases; a repository secret always belongs to its own host.
func resolveSecretHost(cfg gm.Config, baseRepo ghrepo.Interface) string {
	if baseRepo != nil {
		return baseRepo.RepoHost()
	}
	host, _ := cfg.Authentication().DefaultHost()
	return host
}

// secretPath builds the path for one secret.
//
// Gitea stores Actions secrets at /{orgs|user|repos/...}/actions/secrets/{name}
// with no application segment, since Actions is the only store it has. GitHub
// puts the application in the path for every entity, and uses codespaces for a
// user secret.
func secretPath(host string, entity shared.SecretEntity, app shared.App, orgName string, baseRepo ghrepo.Interface, envName, secretName string) (safeurl.SafeURL, error) {
	gitea := hosttype.IsGitea(host)

	switch entity {
	case shared.Organization:
		if gitea {
			return safeurl.JoinPath("orgs", orgName, "actions", "secrets", secretName)
		}
		return safeurl.JoinPath("orgs", orgName, string(app), "secrets", secretName)

	case shared.Environment:
		if gitea {
			return nil, fmt.Errorf("Gitea has no deployment environments, so `--env` is not supported")
		}
		return safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), "environments", envName, "secrets", secretName)

	case shared.User:
		if gitea {
			return safeurl.JoinPath("user", "actions", "secrets", secretName)
		}
		return safeurl.JoinPath("user", "codespaces", "secrets", secretName)

	case shared.Repository:
		if gitea {
			return safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), "actions", "secrets", secretName)
		}
		return safeurl.JoinPath("repos", baseRepo.RepoOwner(), baseRepo.RepoName(), string(app), "secrets", secretName)

	default:
		return nil, fmt.Errorf("unknown secret entity %q", entity)
	}
}
