package shared

import "errors"

// ErrGiteaAutolinkUnsupported explains why the autolink commands cannot run
// against a Gitea host: its API has no autolink endpoints, so every request
// would 404. The commands' own 404 handling suggests missing admin rights,
// which would send the user looking for a permission they already have.
var ErrGiteaAutolinkUnsupported = errors.New("Gitea has no autolink endpoint, so `gm repo autolink` cannot run against it")
