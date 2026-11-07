package shared

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGiteaRunFromTaskUsesWebURLRunID(t *testing.T) {
	// The task row's own id addresses the task; the run endpoints are addressed by
	// the id in the web URL. Mapping has to prefer the URL so `run view <id>` on an
	// id the task list handed out resolves.
	run := giteaRunFromTask(giteaTask{
		ID:        418397,
		URL:       "https://git.example.com/gitea/tea/actions/runs/1193053",
		Name:      "lychee",
		Status:    "success",
		CreatedAt: "2024-01-01T00:00:00Z",
	}, "git.example.com")

	assert.Equal(t, int64(1193053), run.ID)
}

func TestGiteaRunFromTaskFallsBackToTaskID(t *testing.T) {
	// A URL without a trailing number leaves the task id in place; the run
	// endpoints then answer 404 rather than the mapping guessing.
	run := giteaRunFromTask(giteaTask{
		ID:  418397,
		URL: "https://git.example.com/gitea/tea/actions",
	}, "git.example.com")

	assert.Equal(t, int64(418397), run.ID)
}
