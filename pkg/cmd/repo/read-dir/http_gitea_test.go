package readdir

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var giteaReadDirRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func withGiteaReadDirHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

func TestFetchTreeGitea(t *testing.T) {
	withGiteaReadDirHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/git/trees/main", url.Values{
			"recursive": []string{"false"},
		}),
		httpmock.StringResponse(`{
			"sha": "treesha",
			"total_count": 4,
			"page": 1,
			"tree": [
				{"path": "README.md", "mode": "100644", "type": "blob", "size": 12, "sha": "blobsha"},
				{"path": "docs", "mode": "040000", "type": "tree", "size": 0, "sha": "dirsha"},
				{"path": "build.sh", "mode": "100755", "type": "blob", "size": 4, "sha": "buildsha"},
				{"path": "vendor", "mode": "160000", "type": "commit", "size": 0, "sha": "subsha"}
			]
		}`),
	)

	dir, err := fetchTreeGitea(&http.Client{Transport: reg}, giteaReadDirRepo, "", "main")
	require.NoError(t, err)

	assert.Equal(t, "treesha", dir.GitSHA)
	// Gitea has no node id for a tree, so the exported id stays empty rather than
	// being filled with a SHA that means something else everywhere else.
	assert.Empty(t, dir.ID)

	require.Len(t, dir.Entries, 4)
	assert.Equal(t, "file", dir.Entries[0].Type)
	assert.Equal(t, "README.md", dir.Entries[0].Name)
	assert.Equal(t, 0o100644, dir.Entries[0].Mode)
	assert.Equal(t, "100644", dir.Entries[0].modeOctal())
	assert.Equal(t, 12, dir.Entries[0].Size)

	assert.Equal(t, "dir", dir.Entries[1].Type)
	assert.True(t, dir.Entries[2].isExecutable())
	assert.Equal(t, "submodule", dir.Entries[3].Type)
	require.NotNil(t, dir.Entries[3].Submodule)
	assert.Equal(t, "subsha", dir.Entries[3].Submodule.SubprojectCommitOid)
	assert.Empty(t, dir.Entries[3].Submodule.GitURL)
}

func TestFetchTreeGiteaSubdirectory(t *testing.T) {
	withGiteaReadDirHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// The identifier is ref:path, the same expression the GitHub path builds.
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/git/trees/v1.0:docs", url.Values{
			"recursive": []string{"false"},
		}),
		httpmock.StringResponse(`{
			"sha": "subtreesha",
			"total_count": 1,
			"page": 1,
			"tree": [{"path": "guide.md", "mode": "100644", "type": "blob", "size": 8, "sha": "guidesha"}]
		}`),
	)

	dir, err := fetchTreeGitea(&http.Client{Transport: reg}, giteaReadDirRepo, "/docs", "v1.0")
	require.NoError(t, err)

	require.Len(t, dir.Entries, 1)
	assert.Equal(t, "guide.md", dir.Entries[0].Name)
	assert.Equal(t, "guide.md", dir.Entries[0].Path)
}

func TestFetchTreeGiteaPages(t *testing.T) {
	withGiteaReadDirHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	first := make([]string, giteaTreePageSize)
	for i := range first {
		first[i] = `{"path": "f` + itoaTest(i) + `", "mode": "100644", "type": "blob", "size": 1, "sha": "s"}`
	}

	// Page 1 is full, so the loop has to ask for page 2 even though the total says
	// there is one more entry.
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/git/trees/main", url.Values{
			"recursive": []string{"false"}, "page": []string{"1"},
		}),
		httpmock.StringResponse(`{"sha":"t","total_count":`+itoaTest(giteaTreePageSize+1)+`,"page":1,"tree":[`+joinTest(first)+`]}`),
	)
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/git/trees/main", url.Values{
			"recursive": []string{"false"}, "page": []string{"2"},
		}),
		httpmock.StringResponse(`{"sha":"t","total_count":`+itoaTest(giteaTreePageSize+1)+`,"page":2,"tree":[{"path":"last","mode":"100644","type":"blob","size":1,"sha":"s"}]}`),
	)

	dir, err := fetchTreeGitea(&http.Client{Transport: reg}, giteaReadDirRepo, "", "main")
	require.NoError(t, err)
	assert.Len(t, dir.Entries, giteaTreePageSize+1)
}

func TestFetchTreeGiteaUnresolvable(t *testing.T) {
	withGiteaReadDirHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// Gitea answers 400 for an identifier it cannot resolve.
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/git/trees/main:nope", url.Values{
			"recursive": []string{"false"},
		}),
		httpmock.StatusStringResponse(400, `{"message":"sha not found [main:nope]"}`),
	)

	_, err := fetchTreeGitea(&http.Client{Transport: reg}, giteaReadDirRepo, "nope", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `could not find "nope" at "main"`)
}

func TestFetchTreeGiteaRejectsNonOctalMode(t *testing.T) {
	withGiteaReadDirHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/git/trees/main", url.Values{
			"recursive": []string{"false"},
		}),
		httpmock.StringResponse(`{"sha":"t","total_count":1,"tree":[{"path":"f","mode":"rwx","type":"blob","sha":"s"}]}`),
	)

	_, err := fetchTreeGitea(&http.Client{Transport: reg}, giteaReadDirRepo, "", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not octal")
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func joinTest(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}
