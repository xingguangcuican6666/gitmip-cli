package shared

import (
	"strings"

	"github.com/cli/cli/v2/internal/gm"
)

// AuthTokenRefreshable reports whether the token is stored by gm and can be
// renewed with `gm auth refresh`.
//
// TODO: this matches a token prefix itself. It could ask
// gm.AuthConfig.ActiveTokenType instead.
func AuthTokenRefreshable(token, src string) bool {
	return token != "" && !strings.HasSuffix(src, "_TOKEN") && strings.HasPrefix(token, "gho_")
}

func AuthTokenWriteable(authCfg gm.AuthConfig, hostname string) (string, bool) {
	cred := authCfg.ActiveToken(hostname)
	return cred.Source, (cred.Token == "" || !strings.HasSuffix(cred.Source, "_TOKEN"))
}
