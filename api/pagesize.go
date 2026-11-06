package api

import (
	"net/url"
	"strconv"

	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/safeurl"
)

// PageSizeParam returns the query parameter a host uses to set page size.
//
// GitHub spells it per_page and Gitea spells it limit. Both answer a Link header
// naming the next page, so a paging loop that follows that header works on
// either; only the size parameter has to be named per host. A server ignores the
// spelling it does not know, which leaves its own default page size in force and
// hands a caller that asked for 100 a page of 30 instead.
func PageSizeParam(hostname string) string {
	if hosttype.IsGitea(hostname) {
		return "limit"
	}
	return "per_page"
}

// SetPageSizeValues sets the page size on a url.Values query.
func SetPageSizeValues(query url.Values, hostname string, size int) {
	query.Set(PageSizeParam(hostname), strconv.Itoa(size))
}

// SetPageSizeMutable sets the page size on a safeurl.MutableSafeURL.
func SetPageSizeMutable(query *safeurl.MutableSafeURL, hostname string, size int) {
	query.SetQuery(PageSizeParam(hostname), strconv.Itoa(size))
}
