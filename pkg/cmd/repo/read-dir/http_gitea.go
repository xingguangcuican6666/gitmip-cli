package readdir

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API, which is where the GitHub path reads a directory
// from. Its REST equivalent is the git trees endpoint, which takes a tree
// identifier in the same ref:path form git itself uses and answers with the tree's
// entries.
//
// Three differences carry into the result.
//
// A tree has no id. GitHub's Tree carries a node id and gm exports it; Gitea's
// response has only the tree's SHA, so the exported id is empty. Filling it with
// the SHA would put a different kind of identifier in a field that means a node id
// everywhere else.
//
// The mode arrives as an octal string rather than a number, so it is parsed. A
// mode Gitea cannot express as octal would be a server bug, and it is reported
// rather than read as zero, which would make a file look like a directory.
//
// A submodule has no metadata. Gitea's tree entry for a submodule is a commit
// type with a SHA and nothing else, so the submodule object reports the commit but
// cannot give the URL or branch GitHub's does.

// giteaTreeEntry is one entry of Gitea's git tree response.
type giteaTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	Size int    `json:"size"`
	SHA  string `json:"sha"`
	URL  string `json:"url"`
}

// giteaTreeResponse is Gitea's GitTreeResponse.
type giteaTreeResponse struct {
	SHA        string           `json:"sha"`
	TotalCount int              `json:"total_count"`
	Page       int              `json:"page"`
	Entries    []giteaTreeEntry `json:"tree"`
}

// fetchTreeGitea lists a directory through Gitea's git trees endpoint.
//
// The identifier is ref:path, or just ref when no path was given, which is the
// same expression the GitHub path builds. Gitea answers 400 for an identifier it
// cannot resolve, and unlike GraphQL it names the identifier in the message, so
// the caller's own message is kept and the server's is left to the API error.
func fetchTreeGitea(httpClient *http.Client, repo ghrepo.Interface, dirPath, ref string) (*repoDir, error) {
	client := api.NewClientFromHTTP(httpClient)

	expression := ref
	if expression == "" {
		expression = "HEAD"
	}
	if trimmed := strings.TrimPrefix(dirPath, "/"); trimmed != "" {
		expression = expression + ":" + trimmed
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "git", "trees", expression)
	if err != nil {
		return nil, err
	}
	// Only the entries directly under the directory are wanted; recursive would
	// flatten the whole subtree into one list.
	path.SetQuery("recursive", "false")
	api.SetPageSizeMutable(path, repo.RepoHost(), giteaTreePageSize)

	dir := &repoDir{Entries: make([]dirEntry, 0)}

	for page := 1; ; page++ {
		path.SetQuery("page", strconv.Itoa(page))

		var response giteaTreeResponse
		if err := client.REST(repo.RepoHost(), http.MethodGet, path.String(), nil, &response); err != nil {
			if isGiteaTreeNotFound(err) {
				if ref != "" {
					return nil, fmt.Errorf("could not find %q at %q in %s (the path or ref may not exist)", dirPath, ref, ghrepo.FullName(repo))
				}
				return nil, fmt.Errorf("could not find %q in %s", dirPath, ghrepo.FullName(repo))
			}
			return nil, err
		}

		dir.GitSHA = response.SHA

		for _, e := range response.Entries {
			entry, err := giteaTreeEntryToDirEntry(e, repo)
			if err != nil {
				return nil, err
			}
			dir.Entries = append(dir.Entries, entry)
		}

		// Gitea reports the entry count for the whole tree, so a page shorter than
		// the request is the end; the total is also checked in case a tree has
		// exactly a whole page of entries.
		if len(response.Entries) < giteaTreePageSize || len(dir.Entries) >= response.TotalCount {
			break
		}
	}

	return dir, nil
}

// giteaTreePageSize is the per-page maximum to ask for.
//
// Gitea caps a page at MAX_RESPONSE_ITEMS, which is 50 on gitea.com. Asking for
// more is answered with fewer entries, and the paging loop would read that as the
// end of the tree and silently drop the rest.
const giteaTreePageSize = 50

// giteaTreeEntryToDirEntry maps one tree entry.
func giteaTreeEntryToDirEntry(e giteaTreeEntry, repo ghrepo.Interface) (dirEntry, error) {
	mode, err := strconv.ParseInt(e.Mode, 8, 64)
	if err != nil {
		return dirEntry{}, fmt.Errorf("Gitea returned mode %q for %q, which is not octal", e.Mode, e.Path)
	}

	name := e.Path
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}

	entry := dirEntry{
		Name:    name,
		Path:    e.Path,
		NameRaw: name,
		PathRaw: e.Path,
		Type:    entryTypeFromMode(int(mode)),
		GitType: e.Type,
		Mode:    int(mode),
		GitSHA:  e.SHA,
		Size:    e.Size,
	}

	if entry.Type == "submodule" {
		// Gitea reports a submodule as a commit entry with a SHA and no metadata,
		// so the URL and branch GitHub would give are left unset rather than
		// guessed at from the repository being listed.
		entry.Submodule = &submodule{
			SubprojectCommitOid: e.SHA,
		}
	}

	return entry, nil
}

// isGiteaTreeNotFound reports whether Gitea could not resolve the tree.
//
// Gitea answers 400 for an unresolvable ref:path rather than 404, because it
// reports the failure as a bad identifier rather than a missing resource.
func isGiteaTreeNotFound(err error) bool {
	var httpErr api.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusBadRequest
}
