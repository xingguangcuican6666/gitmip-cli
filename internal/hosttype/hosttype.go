// Package hosttype resolves which kind of git host a configured hostname is,
// so that endpoint construction can differ between them.
//
// The resolution is configuration driven rather than lexical. A hostname such
// as "git.example.com" says nothing about whether it serves GitHub's API or
// Gitea's, and guessing from the name would send requests to a path prefix the
// server does not have. The host type is therefore stored per host in
// hosts.yml, next to api_host:
//
//	hosts:
//	    git.example.com:
//	        host_type: gitea
//
// An unconfigured host is GitHub, which keeps the default behaviour of every
// existing host unchanged.
package hosttype

import (
	"strings"
	"sync"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	ghConfig "github.com/cli/go-gh/v2/pkg/config"
)

// Type identifies the API shape a host serves.
type Type string

const (
	// GitHub is the default host type. It covers github.com, github.localhost,
	// tenancy hosts and GitHub Enterprise Server, which all speak the same API.
	GitHub Type = "github"
	// Gitea is a self-hosted Gitea or Forgejo instance, which serves its API
	// under /api/v1 and has no GraphQL endpoint.
	Gitea Type = "gitea"
)

const (
	hostsKey    = "hosts"
	hostTypeKey = "host_type"
)

// resolved caches the host type per normalized hostname. go-gh already caches
// the parsed config files for the life of the process, so this only avoids
// repeating the map walk and normalization on every request.
var resolved sync.Map

// override, when set, replaces config-based resolution entirely.
//
// A single pointer rather than a mutex is deliberate: gh commands resolve hosts
// serially, and the override exists so tests can control resolution without
// writing a hosts.yml. It is never set in production.
var override func(normalized string) (Type, bool)

// readConfig is the config source. Tests replace it so they can supply a
// parsed config without touching the filesystem.
var readConfig = func() *ghConfig.Config {
	cfg, err := ghConfig.Read(nil)
	if err != nil {
		return nil
	}
	return cfg
}

// SetHostTypeOverride replaces host type resolution with lookups in table,
// keyed by hostname, and returns a function that restores config-based
// resolution. A hostname absent from table resolves as GitHub. Cached
// resolutions are bypassed while the override is installed. Use for testing
// purposes only.
func SetHostTypeOverride(table map[string]Type) func() {
	previous := override
	override = func(normalized string) (Type, bool) {
		t, ok := table[normalized]
		return t, ok
	}
	return func() {
		override = previous
	}
}

// For returns the host type configured for hostname, or GitHub when the host
// has no host_type set or its value is unrecognized.
//
// An unrecognized value is treated as GitHub rather than rejected so that a
// config written by a newer gh cannot break an older one. gh config set
// validates the value on the way in.
func For(hostname string) Type {
	normalized := ghauth.NormalizeHostname(hostname)
	if normalized == "" {
		return GitHub
	}

	if override != nil {
		if t, ok := override(normalized); ok {
			return t
		}
		return GitHub
	}

	if cached, ok := resolved.Load(normalized); ok {
		return cached.(Type)
	}

	t := lookup(normalized)
	resolved.Store(normalized, t)
	return t
}

// IsGitea reports whether hostname is configured as a Gitea host.
func IsGitea(hostname string) bool {
	return For(hostname) == Gitea
}

func lookup(normalized string) Type {
	cfg := readConfig()
	if cfg == nil {
		return GitHub
	}

	configured, err := cfg.Get([]string{hostsKey, normalized, hostTypeKey})
	if err != nil {
		return GitHub
	}

	switch Type(strings.ToLower(strings.TrimSpace(configured))) {
	case Gitea:
		return Gitea
	default:
		return GitHub
	}
}
