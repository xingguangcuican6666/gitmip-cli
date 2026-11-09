package hosttype

import (
	ghauth "github.com/cli/go-gh/v2/pkg/auth"
)

// Forget drops any cached resolution for hostname, so a host_type written to
// configuration during this process is observed immediately.
//
// Resolutions are cached for the life of the process because they are asked on
// every request. That is wrong for the one command that writes host_type itself,
// which would otherwise keep reading the value it had before the write.
func Forget(hostname string) {
	resolved.Delete(ghauth.NormalizeHostname(hostname))
}
