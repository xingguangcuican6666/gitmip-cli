package ghinstance

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cli/cli/v2/internal/hosttype"
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
)

// DefaultHostname is the domain name of the default GitHub instance.
const defaultHostname = "github.com"

// Localhost is the domain name of a local GitHub instance.
const localhost = "github.localhost"

// TenancyHost is the domain name of a tenancy GitHub instance.
const tenancyHost = "ghe.com"

// giteaAPIRoot is the path prefix a Gitea instance serves its API under. Unlike
// GitHub, Gitea serves the API from the host itself, so there is no "api."
// subdomain to add and no separate GraphQL root.
const giteaAPIRoot = "/api/v1/"

// Default returns the host name of the default GitHub instance.
func Default() string {
	return defaultHostname
}

// TenantName extracts the tenant name from tenancy host name and
// reports whether it found the tenant name.
func TenantName(h string) (string, bool) {
	normalizedHostName := ghauth.NormalizeHostname(h)
	return strings.CutSuffix(normalizedHostName, "."+tenancyHost)
}

func isGarage(h string) bool {
	return strings.EqualFold(h, "garage.github.com")
}

func HostnameValidator(hostname string) error {
	if len(strings.TrimSpace(hostname)) < 1 {
		return errors.New("a value is required")
	}
	if strings.ContainsRune(hostname, '/') || strings.ContainsRune(hostname, ':') {
		return errors.New("invalid hostname")
	}
	return nil
}

// GraphQLEndpoint returns the GraphQL API root for hostname, or an empty string
// when the host serves no GraphQL API. Gitea has no GraphQL endpoint, so
// callers must check for the empty string rather than assuming a URL.
func GraphQLEndpoint(hostname string) string {
	if hosttype.IsGitea(hostname) {
		return ""
	}
	if isGarage(hostname) {
		return fmt.Sprintf("https://%s/api/graphql", hostname)
	}
	if ghauth.IsEnterprise(hostname) {
		return fmt.Sprintf("https://%s/api/graphql", hostname)
	}
	if strings.EqualFold(hostname, localhost) {
		return fmt.Sprintf("http://api.%s/graphql", hostname)
	}
	return fmt.Sprintf("https://api.%s/graphql", hostname)
}

// RESTPrefix returns the REST API root for hostname, including the trailing
// slash. Gitea serves its API from the host itself under /api/v1, rather than
// from an "api." subdomain, so it is checked before the host shape rules that
// decide between GitHub's subdomain and GHES's path prefix.
func RESTPrefix(hostname string) string {
	if hosttype.IsGitea(hostname) {
		return fmt.Sprintf("https://%s%s", hostname, giteaAPIRoot)
	}
	if isGarage(hostname) {
		return fmt.Sprintf("https://%s/api/v3/", hostname)
	}
	if ghauth.IsEnterprise(hostname) {
		return fmt.Sprintf("https://%s/api/v3/", hostname)
	}
	if strings.EqualFold(hostname, localhost) {
		return fmt.Sprintf("http://api.%s/", hostname)
	}
	return fmt.Sprintf("https://api.%s/", hostname)
}

// GistPrefix returns the gist URL prefix for hostname. Gitea has no gist
// service, so gist commands are not usable against a Gitea host.
func GistPrefix(hostname string) string {
	prefix := "https://"
	if strings.EqualFold(hostname, localhost) {
		prefix = "http://"
	}
	return prefix + GistHost(hostname)
}

func GistHost(hostname string) string {
	if isGarage(hostname) {
		return fmt.Sprintf("%s/gist/", hostname)
	}
	if ghauth.IsEnterprise(hostname) {
		return fmt.Sprintf("%s/gist/", hostname)
	}
	if strings.EqualFold(hostname, localhost) {
		return fmt.Sprintf("%s/gist/", hostname)
	}
	return fmt.Sprintf("gist.%s/", hostname)
}

// UserAssetUploadPrefix returns the URL prefix for user asset uploads.
// GHES does not support this endpoint.
func UserAssetUploadPrefix(hostname string) string {
	if strings.EqualFold(hostname, localhost) {
		return fmt.Sprintf("http://uploads.%s/", hostname)
	}
	return fmt.Sprintf("https://uploads.%s/", hostname)
}

func HostPrefix(hostname string) string {
	if strings.EqualFold(hostname, localhost) {
		return fmt.Sprintf("http://%s/", hostname)
	}
	return fmt.Sprintf("https://%s/", hostname)
}

func CategorizeHost(host string) string {
	if hosttype.IsGitea(host) {
		return "gitea"
	}

	if host == defaultHostname {
		return "github.com"
	}

	if ghauth.IsEnterprise(host) {
		return "ghes"
	}

	if ghauth.IsTenancy(host) {
		return "tenancy"
	}

	return "uncategorized"
}
