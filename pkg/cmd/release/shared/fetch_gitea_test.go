package shared

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapGiteaRelease(t *testing.T) {
	raw := giteaRelease{
		DatabaseID:      42,
		TagName:         "v1.0.0",
		Name:            "the name",
		Body:            "the body",
		IsDraft:         false,
		IsPrerelease:    true,
		CreatedAt:       "2024-01-01T00:00:00Z",
		PublishedAt:     "2024-01-02T00:00:00Z",
		TargetCommitish: "abc123",
		APIURL:          "https://git.example.com/api/v1/repos/gitea/tea/releases/42",
		UploadURL:       "https://git.example.com/api/v1/repos/gitea/tea/releases/42/assets",
		URL:             "https://git.example.com/gitea/tea/releases/tag/v1.0.0",
		Assets: []giteaAsset{
			{ID: 7, Name: "file.tar.gz", Size: 100, DownloadCount: 3,
				BrowserDownloadURL: "https://git.example.com/gitea/tea/releases/download/v1.0.0/file.tar.gz"},
		},
	}
	raw.Author.Login = "someone"

	release := mapGiteaRelease(raw)

	assert.Equal(t, int64(42), release.DatabaseID)
	assert.Equal(t, "42", release.ID)
	assert.Equal(t, "v1.0.0", release.TagName)
	assert.Equal(t, "someone", release.Author.Login)
	assert.True(t, release.IsPrerelease)
	assert.False(t, release.IsDraft)
	require.NotNil(t, release.PublishedAt)
	assert.Equal(t, 2024, release.PublishedAt.Year())

	require.Len(t, release.Assets, 1)
	asset := release.Assets[0]
	assert.Equal(t, "7", asset.ID)
	assert.Equal(t, "file.tar.gz", asset.Name)
	assert.Equal(t, int64(100), asset.Size)
	assert.Equal(t, 3, asset.DownloadCount)
	// The API URL is rebuilt from the release's own, because Gitea's asset
	// response has no url field and every write to an asset is addressed by it.
	assert.Equal(t, "https://git.example.com/api/v1/repos/gitea/tea/releases/42/assets/7", asset.APIURL)
}

func TestMapGiteaReleaseToleratesMissingTimes(t *testing.T) {
	release := mapGiteaRelease(giteaRelease{DatabaseID: 1, CreatedAt: "not a time"})
	assert.True(t, release.CreatedAt.IsZero())
	assert.Nil(t, release.PublishedAt)
}

func TestCheckGiteaReleaseParams(t *testing.T) {
	t.Run("create allows its fields", func(t *testing.T) {
		err := CheckGiteaReleaseCreateParams(map[string]any{
			"tag_name": "v1", "body": "b", "draft": true, "prerelease": false,
		})
		assert.NoError(t, err)
	})

	t.Run("edit rejects make_latest when set", func(t *testing.T) {
		err := CheckGiteaReleaseEditParams(map[string]any{"make_latest": "true"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "latest-release marker")
	})

	t.Run("empty unsupported values pass", func(t *testing.T) {
		err := CheckGiteaReleaseCreateParams(map[string]any{
			"tag_name":                 "v1",
			"discussion_category_name": "",
			"generate_release_notes":   false,
			"make_latest":              "",
		})
		assert.NoError(t, err)
	})

	t.Run("unknown field is reported", func(t *testing.T) {
		err := CheckGiteaReleaseCreateParams(map[string]any{"something_new": "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no "something_new" field`)
	})
}

func TestNarrowGiteaReleaseParams(t *testing.T) {
	out := NarrowGiteaReleaseParams(map[string]any{
		"tag_name":                 "v1",
		"make_latest":              "true",
		"discussion_category_name": "General",
		"body":                     "b",
	})
	assert.Equal(t, map[string]any{"tag_name": "v1", "body": "b"}, out)
}

func TestGiteaAssetURLFromUpload(t *testing.T) {
	assert.Equal(t,
		"https://git.example.com/api/v1/repos/gitea/tea/releases/42/assets/7",
		giteaAssetURLFromUpload("https://git.example.com/api/v1/repos/gitea/tea/releases/42/assets?name=x", 7))
}
