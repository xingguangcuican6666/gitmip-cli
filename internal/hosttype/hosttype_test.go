package hosttype

import (
	"testing"

	ghConfig "github.com/cli/go-gh/v2/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setTestConfig replaces the config source with one parsed from hostsYAML and
// clears the resolution cache, restoring both when the test ends.
func setTestConfig(t *testing.T, hostsYAML string) {
	t.Helper()

	originalRead := readConfig
	readConfig = func() *ghConfig.Config {
		return ghConfig.ReadFromString(hostsYAML)
	}
	resolved.Range(func(key, _ any) bool {
		resolved.Delete(key)
		return true
	})

	t.Cleanup(func() {
		readConfig = originalRead
		resolved.Range(func(key, _ any) bool {
			resolved.Delete(key)
			return true
		})
	})
}

func TestFor(t *testing.T) {
	tests := []struct {
		name      string
		hostsYAML string
		hostname  string
		want      Type
	}{
		{
			name:      "unconfigured host is GitHub",
			hostsYAML: "hosts:\n    github.com:\n        user: octocat\n",
			hostname:  "github.com",
			want:      GitHub,
		},
		{
			name:      "configured gitea host",
			hostsYAML: "hosts:\n    git.example.com:\n        host_type: gitea\n",
			hostname:  "git.example.com",
			want:      Gitea,
		},
		{
			name:      "gitea value is case insensitive",
			hostsYAML: "hosts:\n    git.example.com:\n        host_type: Gitea\n",
			hostname:  "git.example.com",
			want:      Gitea,
		},
		{
			name:      "surrounding whitespace is trimmed",
			hostsYAML: "hosts:\n    git.example.com:\n        host_type: '  gitea  '\n",
			hostname:  "git.example.com",
			want:      Gitea,
		},
		{
			name:      "explicit github value",
			hostsYAML: "hosts:\n    ghes.example.com:\n        host_type: github\n",
			hostname:  "ghes.example.com",
			want:      GitHub,
		},
		{
			name:      "unrecognized value falls back to GitHub",
			hostsYAML: "hosts:\n    git.example.com:\n        host_type: gitea-future\n",
			hostname:  "git.example.com",
			want:      GitHub,
		},
		{
			name:      "empty value falls back to GitHub",
			hostsYAML: "hosts:\n    git.example.com:\n        host_type: ''\n",
			hostname:  "git.example.com",
			want:      GitHub,
		},
		{
			name:      "host absent from config is GitHub",
			hostsYAML: "hosts:\n    git.example.com:\n        host_type: gitea\n",
			hostname:  "other.example.com",
			want:      GitHub,
		},
		{
			name:      "hostname is normalized before lookup",
			hostsYAML: "hosts:\n    github.com:\n        host_type: gitea\n",
			hostname:  "api.github.com",
			want:      Gitea,
		},
		{
			name:      "empty hostname is GitHub",
			hostsYAML: "hosts:\n    git.example.com:\n        host_type: gitea\n",
			hostname:  "",
			want:      GitHub,
		},
		{
			name:      "missing config file is GitHub",
			hostsYAML: "",
			hostname:  "git.example.com",
			want:      GitHub,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestConfig(t, tt.hostsYAML)
			assert.Equal(t, tt.want, For(tt.hostname))
		})
	}
}

func TestIsGitea(t *testing.T) {
	setTestConfig(t, "hosts:\n    git.example.com:\n        host_type: gitea\n")

	assert.True(t, IsGitea("git.example.com"))
	assert.False(t, IsGitea("github.com"))
}

func TestSetHostTypeOverride(t *testing.T) {
	// A configured gitea host that the override does not mention must resolve
	// as GitHub, proving the override replaces config resolution rather than
	// adding to it.
	setTestConfig(t, "hosts:\n    git.example.com:\n        host_type: gitea\n")

	restore := SetHostTypeOverride(map[string]Type{
		"code.example.org": Gitea,
	})
	t.Cleanup(restore)

	assert.Equal(t, Gitea, For("code.example.org"))
	assert.Equal(t, GitHub, For("git.example.com"))

	restore()
	assert.Equal(t, Gitea, For("git.example.com"))
	assert.Equal(t, GitHub, For("code.example.org"))
}

func TestSetHostTypeOverrideRestoresPreviousOverride(t *testing.T) {
	restoreFirst := SetHostTypeOverride(map[string]Type{"one.example.com": Gitea})
	restoreSecond := SetHostTypeOverride(map[string]Type{"two.example.com": Gitea})

	assert.Equal(t, GitHub, For("one.example.com"))
	assert.Equal(t, Gitea, For("two.example.com"))

	restoreSecond()
	assert.Equal(t, Gitea, For("one.example.com"))
	assert.Equal(t, GitHub, For("two.example.com"))

	restoreFirst()
	assert.Equal(t, GitHub, For("one.example.com"))
}

func TestForCachesResolution(t *testing.T) {
	readCount := 0
	originalRead := readConfig
	readConfig = func() *ghConfig.Config {
		readCount++
		return ghConfig.ReadFromString("hosts:\n    git.example.com:\n        host_type: gitea\n")
	}
	resolved.Range(func(key, _ any) bool {
		resolved.Delete(key)
		return true
	})
	t.Cleanup(func() {
		readConfig = originalRead
		resolved.Range(func(key, _ any) bool {
			resolved.Delete(key)
			return true
		})
	})

	require.Equal(t, Gitea, For("git.example.com"))
	require.Equal(t, Gitea, For("git.example.com"))
	// A second lookup of the same normalized host must not read config again.
	assert.Equal(t, 1, readCount)

	require.Equal(t, GitHub, For("github.com"))
	assert.Equal(t, 2, readCount)
}

func TestForget(t *testing.T) {
	// Forget exists for the login command, which writes host_type and then
	// needs the new value in the same process. Without it the cached
	// resolution from before the write would keep answering.
	configured := "hosts:\n    git.example.com:\n        user: octocat\n"
	originalRead := readConfig
	readConfig = func() *ghConfig.Config {
		return ghConfig.ReadFromString(configured)
	}
	resolved.Range(func(key, _ any) bool {
		resolved.Delete(key)
		return true
	})
	t.Cleanup(func() {
		readConfig = originalRead
		resolved.Range(func(key, _ any) bool {
			resolved.Delete(key)
			return true
		})
	})

	require.Equal(t, GitHub, For("git.example.com"))

	// Simulate the login command writing host_type to configuration.
	configured = "hosts:\n    git.example.com:\n        host_type: gitea\n"

	Forget("git.example.com")
	assert.Equal(t, Gitea, For("git.example.com"))

	// The hostname is normalized before it reaches the cache, so a
	// differently cased spelling of the same host drops the same entry.
	configured = "hosts:\n    git.example.com:\n        user: octocat\n"
	Forget("Git.Example.COM")
	assert.Equal(t, GitHub, For("git.example.com"))
}
