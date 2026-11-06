package shared

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/go-gh/v2/pkg/asciisanitizer"
	"golang.org/x/text/transform"
)

const (
	Active             WorkflowState = "active"
	DisabledManually   WorkflowState = "disabled_manually"
	DisabledInactivity WorkflowState = "disabled_inactivity"
)

type iprompter interface {
	Select(string, string, []string) (int, error)
}

type WorkflowState string

type Workflow struct {
	Name string
	ID   int64
	// StringID is Gitea's workflow identifier, which is the workflow's file name
	// rather than a number. GitHub's is always numeric and lives in ID. A JSON
	// decoder would reject a string for an int64 field, so the two are kept
	// separate and Gitea responses are mapped rather than decoded into this
	// struct directly.
	StringID string `json:"-"`
	Path     string
	State    WorkflowState
}

// Identifier returns the id to put in an Actions path.
//
// Gitea addresses a workflow by the same name it is listed under, so a workflow
// that came from Gitea is addressed by that rather than by a number that Gitea
// never had.
func (w *Workflow) Identifier() string {
	if w.StringID != "" {
		return w.StringID
	}
	return strconv.FormatInt(w.ID, 10)
}

type WorkflowsPayload struct {
	Workflows []Workflow
}

func (w *Workflow) Disabled() bool {
	return w.State != Active
}

func (w *Workflow) Base() string {
	return path.Base(w.Path)
}

func (w *Workflow) ExportData(fields []string) map[string]any {
	data := cmdutil.StructExportData(w, fields)
	// The exported id follows the host. On Gitea it is the workflow's name, which
	// is what every other part of that command line takes as an id; on GitHub it
	// is the number the API reported.
	if w.StringID != "" {
		data["id"] = w.StringID
	}
	return data
}

// giteaWorkflow is Gitea's ActionWorkflow.
type giteaWorkflow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
}

type giteaWorkflowsPayload struct {
	TotalCount int             `json:"total_count"`
	Workflows  []giteaWorkflow `json:"workflows"`
}

// GetWorkflowsGitea lists a repository's workflows.
//
// Gitea identifies a workflow by its file name and has no numeric id, so the rows
// are mapped onto Workflow with StringID set and ID left zero. A caller that put
// ID in a path would produce a request Gitea cannot resolve.
func GetWorkflowsGitea(client *api.Client, repo ghrepo.Interface, limit int) ([]Workflow, error) {
	const pageSize = 50

	workflows := []Workflow{}
	for page := 1; ; page++ {
		if limit > 0 && len(workflows) >= limit {
			break
		}

		u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "workflows")
		if err != nil {
			return nil, err
		}
		u.SetQuery("page", strconv.Itoa(page))
		api.SetPageSizeMutable(u, repo.RepoHost(), pageSize)

		var result giteaWorkflowsPayload
		if err := client.REST(repo.RepoHost(), "GET", u.String(), nil, &result); err != nil {
			return nil, err
		}

		for _, raw := range result.Workflows {
			workflows = append(workflows, Workflow{
				Name:     raw.Name,
				StringID: raw.ID,
				Path:     raw.Path,
				State:    WorkflowState(raw.State),
			})
			if limit > 0 && len(workflows) == limit {
				break
			}
		}

		if len(result.Workflows) < pageSize {
			break
		}
	}

	return workflows, nil
}

// GetWorkflowGitea fetches one workflow by the name or file path Gitea uses.
func GetWorkflowGitea(client *api.Client, repo ghrepo.Interface, id string) (*Workflow, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "workflows", id)
	if err != nil {
		return nil, err
	}

	var raw giteaWorkflow
	if err := client.REST(repo.RepoHost(), "GET", path.String(), nil, &raw); err != nil {
		return nil, err
	}

	workflow := Workflow{
		Name:     raw.Name,
		StringID: raw.ID,
		Path:     raw.Path,
		State:    WorkflowState(raw.State),
	}
	// A response that carries no id is addressed by the path it was fetched with,
	// so the caller can still use Identifier() in a further request.
	if workflow.StringID == "" {
		workflow.StringID = id
	}
	return &workflow, nil
}

func GetWorkflows(client *api.Client, repo ghrepo.Interface, limit int) ([]Workflow, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return GetWorkflowsGitea(client, repo, limit)
	}

	perPage := limit
	page := 1
	if limit > 100 || limit == 0 {
		perPage = 100
	}

	workflows := []Workflow{}

	for {
		if limit > 0 && len(workflows) == limit {
			break
		}
		var result WorkflowsPayload

		u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "workflows")
		if err != nil {
			return nil, err
		}
		api.SetPageSizeMutable(u, repo.RepoHost(), perPage)
		u.SetQuery("page", strconv.Itoa(page))

		err = client.REST(repo.RepoHost(), "GET", u.String(), nil, &result)
		if err != nil {
			return nil, err
		}

		for _, workflow := range result.Workflows {
			workflows = append(workflows, workflow)
			if limit > 0 && len(workflows) == limit {
				break
			}
		}

		if len(result.Workflows) < perPage {
			break
		}

		page++
	}

	return workflows, nil
}

type FilteredAllError struct {
	error
}

func selectWorkflow(p iprompter, workflows []Workflow, promptMsg string, states []WorkflowState) (*Workflow, error) {
	filtered := []Workflow{}
	candidates := []string{}
	for _, workflow := range workflows {
		if slices.Contains(states, workflow.State) {
			filtered = append(filtered, workflow)
			candidates = append(candidates, fmt.Sprintf("%s (%s)", workflow.Name, workflow.Base()))
		}
	}

	if len(candidates) == 0 {
		return nil, FilteredAllError{errors.New("")}
	}

	selected, err := p.Select(promptMsg, "", candidates)
	if err != nil {
		return nil, err
	}

	return &filtered[selected], nil
}

// FindWorkflow looks up a workflow either by numeric database ID, file name, or its Name field
func FindWorkflow(client *api.Client, repo ghrepo.Interface, workflowSelector string, states []WorkflowState) ([]Workflow, error) {
	if workflowSelector == "" {
		return nil, errors.New("empty workflow selector")
	}

	if _, err := strconv.Atoi(workflowSelector); err == nil || isWorkflowFile(workflowSelector) {
		workflow, err := getWorkflowByID(client, repo, workflowSelector)
		if err != nil {
			if httpErr, ok := errors.AsType[api.HTTPError](err); ok {
				if httpErr.StatusCode == 404 {
					httpErr.Message = fmt.Sprintf("workflow %s not found on the default branch", workflowSelector)
				}
				return nil, httpErr
			}
			return nil, err
		}
		return []Workflow{*workflow}, nil
	}

	return getWorkflowsByName(client, repo, workflowSelector, states)
}

func GetWorkflow(client *api.Client, repo ghrepo.Interface, workflowID int64) (*Workflow, error) {
	return getWorkflowByID(client, repo, strconv.FormatInt(workflowID, 10))
}

func isWorkflowFile(f string) bool {
	name := strings.ToLower(f)
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// ID can be either a numeric database ID or the workflow file name
func getWorkflowByID(client *api.Client, repo ghrepo.Interface, ID string) (*Workflow, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return GetWorkflowGitea(client, repo, ID)
	}

	var workflow Workflow

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "workflows", ID)
	if err != nil {
		return nil, err
	}
	if err := client.REST(repo.RepoHost(), "GET", path.String(), nil, &workflow); err != nil {
		return nil, err
	}

	return &workflow, nil
}

func getWorkflowsByName(client *api.Client, repo ghrepo.Interface, name string, states []WorkflowState) ([]Workflow, error) {
	workflows, err := GetWorkflows(client, repo, 0)
	if err != nil {
		return nil, fmt.Errorf("couldn't fetch workflows for %s: %w", ghrepo.FullName(repo), err)
	}

	var filtered []Workflow
	for _, workflow := range workflows {
		if !strings.EqualFold(workflow.Name, name) {
			continue
		}
		if slices.Contains(states, workflow.State) {
			filtered = append(filtered, workflow)
		}
	}

	return filtered, nil
}

func ResolveWorkflow(p iprompter, io *iostreams.IOStreams, client *api.Client, repo ghrepo.Interface, prompt bool, workflowSelector string, states []WorkflowState) (*Workflow, error) {
	if prompt {
		workflows, err := GetWorkflows(client, repo, 0)
		if len(workflows) == 0 {
			err = errors.New("no workflows are enabled")
		}

		if err != nil {
			var httpErr api.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
				err = errors.New("no workflows are enabled")
			}

			return nil, fmt.Errorf("could not fetch workflows for %s: %w", ghrepo.FullName(repo), err)
		}

		return selectWorkflow(p, workflows, "Select a workflow", states)
	}

	workflows, err := FindWorkflow(client, repo, workflowSelector, states)
	if err != nil {
		return nil, err
	}

	if len(workflows) == 0 {
		return nil, fmt.Errorf("could not find any workflows named %s", workflowSelector)
	}

	if len(workflows) == 1 {
		return &workflows[0], nil
	}

	if !io.CanPrompt() {
		var errMsg strings.Builder
		errMsg.WriteString("could not resolve to a unique workflow; found:")
		for _, workflow := range workflows {
			errMsg.WriteString(fmt.Sprintf(" %s", workflow.Base()))
		}
		return nil, errors.New(errMsg.String())
	}

	return selectWorkflow(p, workflows, "Which workflow do you mean?", states)
}

func GetWorkflowContent(client *api.Client, repo ghrepo.Interface, workflow Workflow, ref string) ([]byte, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "contents", workflow.Path)
	if err != nil {
		return nil, err
	}
	if ref != "" {
		path.SetQuery("ref", ref)
	}

	type Result struct {
		Content string
	}

	var result Result
	err = client.REST(repo.RepoHost(), "GET", path.String(), nil, &result)
	if err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(result.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode workflow file: %w", err)
	}

	sanitized, err := io.ReadAll(transform.NewReader(bytes.NewReader(decoded), &asciisanitizer.Sanitizer{}))
	if err != nil {
		return nil, err
	}

	return sanitized, nil
}
