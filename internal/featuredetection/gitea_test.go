package featuredetection

import (
	"testing"

	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDetectorReturnsGiteaDetector(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	d := NewDetector(nil, "git.example.com")
	require.IsType(t, &giteaDetector{}, d)
}

func TestNewDetectorReturnsGitHubDetector(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(nil)
	t.Cleanup(restore)

	for _, host := range []string{"github.com", "ghe.io", "tenant.ghe.com"} {
		t.Run(host, func(t *testing.T) {
			assert.IsType(t, &detector{}, NewDetector(nil, host))
		})
	}
}

// Every capability Gitea lacks is reported as absent so commands take the REST
// path rather than issuing a GraphQL request that cannot succeed. None of these
// must error: gh asks them on paths Gitea does support, such as listing
// repositories, and a detection failure would break those commands.
func TestGiteaDetectorCapabilities(t *testing.T) {
	d := &giteaDetector{host: "git.example.com"}

	t.Run("IssueFeatures", func(t *testing.T) {
		f, err := d.IssueFeatures()
		require.NoError(t, err)
		assert.False(t, f.ApiActorsSupported)
		assert.False(t, f.IssueRelationshipsSupported)
	})

	t.Run("PullRequestFeatures", func(t *testing.T) {
		f, err := d.PullRequestFeatures()
		require.NoError(t, err)
		assert.False(t, f.MergeQueue)
		assert.False(t, f.CheckRunAndStatusContextCounts)
		assert.False(t, f.CheckRunEvent)
	})

	t.Run("RepositoryFeatures", func(t *testing.T) {
		f, err := d.RepositoryFeatures()
		require.NoError(t, err)
		assert.False(t, f.PullRequestTemplateQuery)
		assert.False(t, f.VisibilityField)
		assert.False(t, f.AutoMerge)
	})

	t.Run("ProjectsV1", func(t *testing.T) {
		assert.Equal(t, gh.ProjectsV1Unsupported, d.ProjectsV1())
	})

	t.Run("ProjectFeatures", func(t *testing.T) {
		f, err := d.ProjectFeatures()
		require.NoError(t, err)
		assert.False(t, f.ProjectItemQuery)
	})

	t.Run("SearchFeatures", func(t *testing.T) {
		f, err := d.SearchFeatures()
		require.NoError(t, err)
		assert.False(t, f.AdvancedIssueSearchAPI)
		assert.False(t, f.SemanticSearch)
		assert.False(t, f.HybridSearch)
	})

	t.Run("ReleaseFeatures", func(t *testing.T) {
		f, err := d.ReleaseFeatures()
		require.NoError(t, err)
		assert.False(t, f.ImmutableReleases)
	})

	t.Run("ActionsFeatures", func(t *testing.T) {
		f, err := d.ActionsFeatures()
		require.NoError(t, err)
		// Gitea answers with the created run when return_run_details is sent as a
		// query parameter, which the workflow run command does on this host.
		assert.True(t, f.DispatchRunDetails)
	})
}
