package shared

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/safeurl"
	workflowShared "github.com/cli/cli/v2/pkg/cmd/workflow/shared"
	"github.com/cli/cli/v2/pkg/iostreams"
)

type Prompter interface {
	Select(string, string, []string) (int, error)
}

const (
	// Run statuses
	Queued     Status = "queued"
	Completed  Status = "completed"
	InProgress Status = "in_progress"
	Requested  Status = "requested"
	Waiting    Status = "waiting"
	Pending    Status = "pending"

	// Run conclusions
	ActionRequired Conclusion = "action_required"
	Cancelled      Conclusion = "cancelled"
	Failure        Conclusion = "failure"
	Neutral        Conclusion = "neutral"
	Skipped        Conclusion = "skipped"
	Stale          Conclusion = "stale"
	StartupFailure Conclusion = "startup_failure"
	Success        Conclusion = "success"
	TimedOut       Conclusion = "timed_out"

	AnnotationFailure Level = "failure"
	AnnotationWarning Level = "warning"
)

type Status string
type Conclusion string
type Level string

var AllStatuses = []string{
	"queued",
	"completed",
	"in_progress",
	"requested",
	"waiting",
	"pending",
	"action_required",
	"cancelled",
	"failure",
	"neutral",
	"skipped",
	"stale",
	"startup_failure",
	"success",
	"timed_out",
}

var RunFields = []string{
	"name",
	"displayTitle",
	"headBranch",
	"headSha",
	"createdAt",
	"updatedAt",
	"startedAt",
	"attempt",
	"status",
	"conclusion",
	"event",
	"number",
	"databaseId",
	"workflowDatabaseId",
	"workflowName",
	"url",
}

var SingleRunFields = append(RunFields, "jobs")

type Run struct {
	Name           string    `json:"name"` // the semantics of this field are unclear
	DisplayTitle   string    `json:"display_title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	StartedAt      time.Time `json:"run_started_at"`
	Status         Status
	Conclusion     Conclusion
	Event          string
	ID             int64
	workflowName   string // cache column
	WorkflowID     int64  `json:"workflow_id"`
	Number         int64  `json:"run_number"`
	Attempt        uint64 `json:"run_attempt"`
	HeadBranch     string `json:"head_branch"`
	JobsURL        string `json:"jobs_url"`
	HeadCommit     Commit `json:"head_commit"`
	HeadSha        string `json:"head_sha"`
	URL            string `json:"html_url"`
	HeadRepository Repo   `json:"head_repository"`
	Jobs           []Job  `json:"-"` // Populated manually (separate from fetching the run)

	// workflowFile is the workflow's file name, which Gitea reports where GitHub
	// reports a numeric workflow id. It is kept so a --workflow filter naming the
	// file still matches.
	workflowFile string
}

func (r *Run) StartedTime() time.Time {
	if r.StartedAt.IsZero() {
		return r.CreatedAt
	}
	return r.StartedAt
}

func (r *Run) Duration(now time.Time) time.Duration {
	endTime := r.UpdatedAt
	if r.Status != Completed {
		endTime = now
	}
	d := endTime.Sub(r.StartedTime())
	if d < 0 {
		return 0
	}
	return d.Round(time.Second)
}

type Repo struct {
	Owner struct {
		Login string
	}
	Name string
}

type Commit struct {
	Message string
}

// Title is the display title for a run, falling back to the commit subject if unavailable
func (r Run) Title() string {
	if r.DisplayTitle != "" {
		return r.DisplayTitle
	}

	commitLines := strings.Split(r.HeadCommit.Message, "\n")
	if len(commitLines) > 0 {
		return commitLines[0]
	} else {
		return r.HeadSha[0:8]
	}
}

// WorkflowName returns the human-readable name of the workflow that this run belongs to.
// TODO: consider lazy-loading the underlying API data to avoid extra API calls unless necessary
func (r Run) WorkflowName() string {
	return r.workflowName
}

func (r *Run) ExportData(fields []string) map[string]any {
	v := reflect.ValueOf(r).Elem()
	fieldByName := func(v reflect.Value, field string) reflect.Value {
		return v.FieldByNameFunc(func(s string) bool {
			return strings.EqualFold(field, s)
		})
	}
	data := map[string]any{}

	for _, f := range fields {
		switch f {
		case "databaseId":
			data[f] = r.ID
		case "workflowDatabaseId":
			data[f] = r.WorkflowID
		case "workflowName":
			data[f] = r.WorkflowName()
		case "jobs":
			jobs := make([]any, 0, len(r.Jobs))
			for _, j := range r.Jobs {
				steps := make([]any, 0, len(j.Steps))
				for _, s := range j.Steps {
					var stepCompletedAt time.Time
					if !s.CompletedAt.IsZero() {
						stepCompletedAt = s.CompletedAt
					}
					steps = append(steps, map[string]any{
						"name":        s.Name,
						"status":      s.Status,
						"conclusion":  s.Conclusion,
						"number":      s.Number,
						"startedAt":   s.StartedAt,
						"completedAt": stepCompletedAt,
					})
				}
				var jobCompletedAt time.Time
				if !j.CompletedAt.IsZero() {
					jobCompletedAt = j.CompletedAt
				}
				jobs = append(jobs, map[string]any{
					"databaseId":  j.ID,
					"status":      j.Status,
					"conclusion":  j.Conclusion,
					"name":        j.Name,
					"steps":       steps,
					"startedAt":   j.StartedAt,
					"completedAt": jobCompletedAt,
					"url":         j.URL,
				})
			}
			data[f] = jobs
		default:
			sf := fieldByName(v, f)
			data[f] = sf.Interface()
		}
	}

	return data
}

type Job struct {
	ID          int64
	Status      Status
	Conclusion  Conclusion
	Name        string
	Steps       Steps
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	URL         string    `json:"html_url"`
	RunID       int64     `json:"run_id"`
}

type Step struct {
	Name        string
	Status      Status
	Conclusion  Conclusion
	Number      int
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

type Steps []Step

func (s Steps) Len() int           { return len(s) }
func (s Steps) Less(i, j int) bool { return s[i].Number < s[j].Number }
func (s Steps) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type Annotation struct {
	JobName   string
	Message   string
	Path      string
	Level     Level `json:"annotation_level"`
	StartLine int   `json:"start_line"`
}

func AnnotationSymbol(cs *iostreams.ColorScheme, a Annotation) string {
	switch a.Level {
	case AnnotationFailure:
		return cs.FailureIcon()
	case AnnotationWarning:
		return cs.WarningIcon()
	default:
		return "-"
	}
}

type CheckRun struct {
	ID int64
}

var ErrMissingAnnotationsPermissions = errors.New("missing annotations permissions error")

// GetAnnotations fetches annotations from the REST API.
//
// If the job has no annotations, an empty slice is returned.
// If the API returns a 403, a custom ErrMissingAnnotationsPermissions error is returned.
//
// When fine-grained PATs support checks:read permission, we can remove the need for this at the call sites.
func GetAnnotations(client *api.Client, repo ghrepo.Interface, job Job) ([]Annotation, error) {
	// Gitea has no check-runs endpoint, so there is nothing to ask. An empty
	// result is reported rather than an error: the run has no annotations to
	// show, which is what the caller would print anyway.
	if hosttype.IsGitea(repo.RepoHost()) {
		return []Annotation{}, nil
	}

	var result []*Annotation

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "check-runs", strconv.FormatInt(job.ID, 10), "annotations")
	if err != nil {
		return nil, err
	}

	err = client.REST(repo.RepoHost(), "GET", path.String(), nil, &result)
	if err != nil {
		var httpError api.HTTPError
		if !errors.As(err, &httpError) {
			return nil, err
		}

		if httpError.StatusCode == http.StatusNotFound {
			return []Annotation{}, nil
		}

		if httpError.StatusCode == http.StatusForbidden {
			return nil, ErrMissingAnnotationsPermissions
		}

		return nil, err
	}

	out := []Annotation{}

	for _, annotation := range result {
		annotation.JobName = job.Name
		out = append(out, *annotation)
	}

	return out, nil
}

func IsFailureState(c Conclusion) bool {
	switch c {
	case ActionRequired, Failure, StartupFailure, TimedOut:
		return true
	default:
		return false
	}
}

func IsSkipped(c Conclusion) bool {
	return c == Skipped
}

type RunsPayload struct {
	TotalCount   int   `json:"total_count"`
	WorkflowRuns []Run `json:"workflow_runs"`
}

type FilterOptions struct {
	Branch     string
	Actor      string
	WorkflowID int64
	// avoid loading workflow name separately and use the provided one
	WorkflowName string
	Status       string
	Event        string
	Created      string
	Commit       string
}

// GetRunsWithFilter fetches 50 runs from the API and filters them in-memory
func GetRunsWithFilter(client *api.Client, repo ghrepo.Interface, opts *FilterOptions, limit int, f func(Run) bool) ([]Run, error) {
	runs, err := GetRuns(client, repo, opts, 50)
	if err != nil {
		return nil, err
	}

	var filtered []Run
	for _, run := range runs.WorkflowRuns {
		if f(run) {
			filtered = append(filtered, run)
		}
		if len(filtered) == limit {
			break
		}
	}

	return filtered, nil
}

func GetRuns(client *api.Client, repo ghrepo.Interface, opts *FilterOptions, limit int) (*RunsPayload, error) {
	// Gitea serves runs from its task list while unauthenticated, and that list
	// has no filter parameters, so the filtering path is different.
	if hosttype.IsGitea(repo.RepoHost()) {
		return GetRunsGitea(client, repo, opts, limit)
	}

	u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "runs")
	if err != nil {
		return nil, err
	}
	if opts != nil && opts.WorkflowID > 0 {
		u, err = safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "workflows", strconv.FormatInt(opts.WorkflowID, 10), "runs")
		if err != nil {
			return nil, err
		}
	}

	perPage := min(limit, 100)
	api.SetPageSizeMutable(u, repo.RepoHost(), perPage)
	u.SetQuery("exclude_pull_requests", "true") // significantly reduces payload size

	if opts != nil {
		if opts.Branch != "" {
			u.SetQuery("branch", opts.Branch)
		}
		if opts.Actor != "" {
			u.SetQuery("actor", opts.Actor)
		}
		if opts.Status != "" {
			u.SetQuery("status", opts.Status)
		}
		if opts.Event != "" {
			u.SetQuery("event", opts.Event)
		}
		if opts.Created != "" {
			u.SetQuery("created", opts.Created)
		}
		if opts.Commit != "" {
			u.SetQuery("head_sha", opts.Commit)
		}
	}
	var pageURL safeurl.SafeURL = u

	var result *RunsPayload

pagination:
	for pageURL.String() != "" {
		var response RunsPayload
		next, err := client.RESTWithNext(repo.RepoHost(), "GET", pageURL.String(), nil, &response)
		if err != nil {
			return nil, err
		}
		pageURL = safeurl.NewImmutableSafeURL(next)

		if result == nil {
			result = &response
			if len(result.WorkflowRuns) == limit {
				break pagination
			}
		} else {
			for _, run := range response.WorkflowRuns {
				result.WorkflowRuns = append(result.WorkflowRuns, run)
				if len(result.WorkflowRuns) == limit {
					break pagination
				}
			}
		}
	}

	if opts != nil && opts.WorkflowName != "" {
		for i := range result.WorkflowRuns {
			result.WorkflowRuns[i].workflowName = opts.WorkflowName
		}
	} else if len(result.WorkflowRuns) > 0 {
		if err := preloadWorkflowNames(client, repo, result.WorkflowRuns); err != nil {
			return result, err
		}
	}

	return result, nil
}

func preloadWorkflowNames(client *api.Client, repo ghrepo.Interface, runs []Run) error {
	workflows, err := workflowShared.GetWorkflows(client, repo, 0)
	if err != nil {
		return err
	}

	workflowMap := map[int64]string{}
	for _, wf := range workflows {
		workflowMap[wf.ID] = wf.Name
	}

	for i, run := range runs {
		if _, ok := workflowMap[run.WorkflowID]; !ok {
			// Look up workflow by ID because it may have been deleted
			workflow, err := workflowShared.GetWorkflow(client, repo, run.WorkflowID)
			// If the error is an httpError and it is a 404, this is likely a
			// organization or enterprise ruleset workflow. The user does not
			// have permissions to view the details of the workflow, so we cannot
			// look it up directly without receiving a 404, but it is nonetheless
			// in the workflow run list. To handle this, we set the workflow name
			// to an empty string.
			// Deciding to put this here instead of in GetWorkflow to allow
			// the caller to decide what a 404 means.
			if httpErr, ok := err.(api.HTTPError); ok && httpErr.StatusCode == 404 {
				workflowMap[run.WorkflowID] = ""
				continue
			}
			if err != nil {
				return err
			}
			workflowMap[run.WorkflowID] = workflow.Name
		}
		runs[i].workflowName = workflowMap[run.WorkflowID]
	}
	return nil
}

type JobsPayload struct {
	TotalCount int `json:"total_count"`
	Jobs       []Job
}

func GetJobs(client *api.Client, repo ghrepo.Interface, runID int64, jobsURL safeurl.SafeURL, attempt uint64) ([]Job, error) {
	// Gitea's task rows carry no jobs URL, and its runs endpoint addresses jobs by
	// index, so the path is built here rather than read from the run.
	if hosttype.IsGitea(repo.RepoHost()) {
		return getJobsGitea(client, repo, runID)
	}

	var jobsPath safeurl.SafeURL
	if attempt > 0 {
		p, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "runs", strconv.FormatInt(runID, 10), "attempts", strconv.FormatUint(attempt, 10), "jobs")
		if err != nil {
			return nil, err
		}
		api.SetPageSizeMutable(p, repo.RepoHost(), 100)
		jobsPath = p
	} else {
		u, err := url.Parse(jobsURL.String())
		if err != nil {
			return nil, err
		}
		query := url.Values{}
		api.SetPageSizeValues(query, repo.RepoHost(), 100)
		u.RawQuery = query.Encode()
		// Since u is derived from jobsURL, an already-trusted safeurl.SafeURL, the resulting URL is safe to declare as such.
		jobsPath = safeurl.NewImmutableSafeURL(u.String())
	}

	// A non-nil empty slice is returned so callers can tell that jobs were fetched and there are none (if len is zero).
	jobs := []Job{}
	for jobsPath.String() != "" {
		var resp JobsPayload
		next, err := client.RESTWithNext(repo.RepoHost(), http.MethodGet, jobsPath.String(), nil, &resp)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, resp.Jobs...)
		jobsPath = safeurl.NewImmutableSafeURL(next)
	}
	return jobs, nil
}

func GetJob(client *api.Client, repo ghrepo.Interface, jobID string) (*Job, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return GetJobGitea(client, repo, jobID)
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "jobs", jobID)
	if err != nil {
		return nil, err
	}

	var result Job
	err = client.REST(repo.RepoHost(), "GET", path.String(), nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// SelectRun prompts the user to select a run from a list of runs by using the recommended prompter interface
func SelectRun(p Prompter, cs *iostreams.ColorScheme, runs []Run) (string, error) {
	now := time.Now()

	candidates := []string{}

	for _, run := range runs {
		symbol, _ := Symbol(cs, run.Status, run.Conclusion)
		candidates = append(candidates,
			// TODO truncate commit message, long ones look terrible
			fmt.Sprintf("%s %s, %s [%s] %s", symbol, run.Title(), run.WorkflowName(), run.HeadBranch, preciseAgo(now, run.StartedTime())))
	}

	selected, err := p.Select("Select a workflow run", "", candidates)

	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%d", runs[selected].ID), nil
}

func GetRun(client *api.Client, repo ghrepo.Interface, runID string, attempt uint64) (*Run, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return GetRunGitea(client, repo, runID, attempt)
	}

	var result Run

	u, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "runs", runID)
	if err != nil {
		return nil, err
	}

	if attempt > 0 {
		u, err = safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "runs", runID, "attempts", strconv.FormatUint(attempt, 10))
		if err != nil {
			return nil, err
		}
	}
	u.SetQuery("exclude_pull_requests", "true")

	err = client.REST(repo.RepoHost(), "GET", u.String(), nil, &result)
	if err != nil {
		return nil, err
	}

	if attempt > 0 {
		result.URL, err = url.JoinPath(result.URL, fmt.Sprintf("/attempts/%d", attempt))
		if err != nil {
			return nil, err
		}
	}

	// Set name to workflow name
	workflow, err := workflowShared.GetWorkflow(client, repo, result.WorkflowID)
	if err != nil {
		return nil, err
	} else {
		result.workflowName = workflow.Name
	}

	return &result, nil
}

type colorFunc func(string) string

func Symbol(cs *iostreams.ColorScheme, status Status, conclusion Conclusion) (string, colorFunc) {
	noColor := func(s string) string { return s }
	if status == Completed {
		switch conclusion {
		case Success:
			return cs.SuccessIconWithColor(noColor), cs.Green
		case Skipped, Neutral:
			return "-", cs.Muted
		default:
			return cs.FailureIconWithColor(noColor), cs.Red
		}
	}

	return "*", cs.Yellow
}

// giteaTask is one row of Gitea's action task list.
//
// The task list is the only Actions view Gitea serves without authentication, and
// it is flatter than a workflow run: it carries no conclusion field, and its
// status holds the finished outcome once the run is done. workflow_id is the
// workflow's file name rather than a number.
type giteaTask struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	DisplayTitle string `json:"display_title"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	RunNumber    int64  `json:"run_number"`
	Event        string `json:"event"`
	Status       string `json:"status"`
	WorkflowID   string `json:"workflow_id"`
	URL          string `json:"url"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	RunStartedAt string `json:"run_started_at"`
}

type giteaTasksPayload struct {
	TotalCount   int         `json:"total_count"`
	WorkflowRuns []giteaTask `json:"workflow_runs"`
}

// giteaRunFromTask maps a task row onto the Run gm works with.
//
// The id needs care: the task row's id is the task's own identifier, while every
// per-run endpoint (GET a run, its jobs, its logs) is addressed by the run id,
// which the row carries in its web URL (.../actions/runs/<run id>). Reading the
// run id from the URL is what keeps `gm run view <id>` and the jobs fetch working
// for ids the task list handed out; falling back to the task id only happens when
// the URL does not contain one, and those commands then answer 404 rather than
// silently addressing a different resource.
//
// Two other fields cannot be filled honestly. Run.WorkflowID is a number on GitHub
// and the task row carries a file name, so it stays zero and the name goes where
// gm caches the workflow name. Status and conclusion are one field on Gitea: the
// row's status is a run status such as queued or in_progress while the run is
// live, and a conclusion such as success or failure once it is done, so the
// mapping decides which of the two gm fields it belongs in.
func giteaRunFromTask(task giteaTask, hostname string) Run {
	id := task.ID
	if runID, ok := runIDFromWebURL(task.URL); ok {
		id = runID
	}

	run := Run{
		ID:           id,
		Name:         task.Name,
		DisplayTitle: task.DisplayTitle,
		HeadBranch:   task.HeadBranch,
		HeadSha:      task.HeadSHA,
		Number:       task.RunNumber,
		Event:        task.Event,
		URL:          task.URL,
		CreatedAt:    giteaRunTime(task.CreatedAt),
		UpdatedAt:    giteaRunTime(task.UpdatedAt),
	}
	run.StartedAt = giteaRunTime(task.RunStartedAt)
	run.Status, run.Conclusion = giteaRunState(task.Status)
	// Gitea's task row carries the workflow's display name in name and its file
	// name in workflow_id. gm shows the display name, which is what WorkflowName
	// is for, and keeps the file name so a --workflow filter naming either one
	// still matches.
	run.workflowName = task.Name
	run.workflowFile = task.WorkflowID
	return run
}

// runIDFromWebURL reads the run id out of a task row's web URL, whose last path
// segment is the id the run endpoints are addressed by.
func runIDFromWebURL(webURL string) (int64, bool) {
	last := webURL[strings.LastIndex(webURL, "/")+1:]
	id, err := strconv.ParseInt(last, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// giteaRunState splits Gitea's single status word into the two fields gm keeps.
//
// queued, waiting, requested, in_progress and pending are run statuses. Everything
// else is a finished outcome, which Gitea spells with the same words GitHub uses
// for a conclusion.
func giteaRunState(status string) (Status, Conclusion) {
	switch Status(status) {
	case Queued, Waiting, Requested, InProgress, Pending:
		return Status(status), ""
	}
	return Completed, Conclusion(status)
}

func giteaRunTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// listGiteaTasks fetches one page of action tasks.
func listGiteaTasks(client *api.Client, repo ghrepo.Interface, page, limit int) (*giteaTasksPayload, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "tasks")
	if err != nil {
		return nil, err
	}
	path.SetQuery("page", strconv.Itoa(page))
	api.SetPageSizeMutable(path, repo.RepoHost(), limit)

	var payload giteaTasksPayload
	if err := client.REST(repo.RepoHost(), http.MethodGet, path.String(), nil, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// GetRunsGitea fetches runs from Gitea's action task list.
//
// Gitea's task list takes no filters beyond paging, so every filter the caller
// set is applied here rather than by the server. That means a filtered list can
// need several pages before it is full, and the count reported back is the
// server's unfiltered total.
func GetRunsGitea(client *api.Client, repo ghrepo.Interface, opts *FilterOptions, limit int) (*RunsPayload, error) {
	const pageSize = 50

	result := &RunsPayload{}
	for page := 1; len(result.WorkflowRuns) < limit; page++ {
		payload, err := listGiteaTasks(client, repo, page, pageSize)
		if err != nil {
			return nil, err
		}
		if page == 1 {
			result.TotalCount = payload.TotalCount
		}
		if len(payload.WorkflowRuns) == 0 {
			break
		}

		for _, task := range payload.WorkflowRuns {
			run := giteaRunFromTask(task, repo.RepoHost())
			if !giteaRunMatches(run, opts) {
				continue
			}
			result.WorkflowRuns = append(result.WorkflowRuns, run)
			if len(result.WorkflowRuns) == limit {
				break
			}
		}

		if len(payload.WorkflowRuns) < pageSize {
			break
		}
	}

	return result, nil
}

// giteaRunMatches applies the filters Gitea's task list cannot.
func giteaRunMatches(run Run, opts *FilterOptions) bool {
	if opts == nil {
		return true
	}
	if opts.Branch != "" && run.HeadBranch != opts.Branch {
		return false
	}
	if opts.Commit != "" && run.HeadSha != opts.Commit {
		return false
	}
	if opts.Event != "" && run.Event != opts.Event {
		return false
	}
	if opts.Status != "" {
		// The status filter on GitHub is a run status or a conclusion, and Gitea
		// keeps whichever applies in one field, so both are matched here.
		statusMatches := strings.EqualFold(string(run.Status), opts.Status)
		conclusionMatches := run.Conclusion != "" && strings.EqualFold(string(run.Conclusion), opts.Status)
		if !statusMatches && !conclusionMatches {
			return false
		}
	}
	if opts.WorkflowName != "" &&
		run.workflowName != opts.WorkflowName &&
		run.workflowFile != opts.WorkflowName &&
		path.Base(run.workflowFile) != opts.WorkflowName {
		return false
	}
	return true
}

func PullRequestForRun(client *api.Client, repo ghrepo.Interface, run Run) (int, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return pullRequestForRunGitea(client, repo, run)
	}

	type response struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number         int
					HeadRepository struct {
						Owner struct {
							Login string
						}
						Name string
					}
				}
			}
		}
		Number int
	}

	variables := map[string]any{
		"owner":       repo.RepoOwner(),
		"repo":        repo.RepoName(),
		"headRefName": run.HeadBranch,
	}

	query := `
		query PullRequestForRun($owner: String!, $repo: String!, $headRefName: String!) {
			repository(owner: $owner, name: $repo) {
				pullRequests(headRefName: $headRefName, first: 1, orderBy: { field: CREATED_AT, direction: DESC }) {
					nodes {
						number
						headRepository {
							owner {
								login
							}
							name
						}
					}
				}
			}
		}`

	var resp response

	err := client.GraphQL(repo.RepoHost(), query, variables, &resp)
	if err != nil {
		return -1, err
	}

	prs := resp.Repository.PullRequests.Nodes
	if len(prs) == 0 {
		return -1, fmt.Errorf("no matching PR found for %s", run.HeadBranch)
	}

	number := -1

	for _, pr := range prs {
		if pr.HeadRepository.Owner.Login == run.HeadRepository.Owner.Login && pr.HeadRepository.Name == run.HeadRepository.Name {
			number = pr.Number
		}
	}

	if number == -1 {
		return number, fmt.Errorf("no matching PR found for %s", run.HeadBranch)
	}

	return number, nil
}

func preciseAgo(now time.Time, createdAt time.Time) string {
	ago := now.Sub(createdAt)

	if ago < 30*24*time.Hour {
		s := ago.Truncate(time.Second).String()
		return fmt.Sprintf("%s ago", s)
	}

	return createdAt.Format("Jan _2, 2006")
}

// pullRequestForRunGitea finds the pull request a run belongs to.
//
// GitHub answers this with a GraphQL query filtered by head ref. Gitea's run has
// no pull request field that survives the task list, and its pull request list
// filters by base branch rather than head, so the candidates are paged through and
// matched locally on the head ref and the head repository.
//
// The head ref on Gitea can be the pull ref itself (refs/pull/206/head), which is
// what a run triggered from a fork's pull request carries; the number in that ref
// is the answer, so it is read directly rather than searched for.
func pullRequestForRunGitea(client *api.Client, repo ghrepo.Interface, run Run) (int, error) {
	if number, ok := pullRefNumber(run.HeadBranch); ok {
		return number, nil
	}

	prs, _, err := api.ListPullRequestsGitea(client, repo, api.ListPullRequestsGiteaInput{
		State: "all",
		Page:  1,
	})
	if err != nil {
		return -1, err
	}

	for _, pr := range prs {
		if pr.HeadRefName != run.HeadBranch {
			continue
		}
		if run.HeadRepository.Owner.Login != "" && pr.HeadRepositoryOwner.Login != "" &&
			pr.HeadRepositoryOwner.Login != run.HeadRepository.Owner.Login {
			continue
		}
		return pr.Number, nil
	}

	return -1, fmt.Errorf("no matching PR found for %s", run.HeadBranch)
}

// pullRefNumber reads the pull request number out of a refs/pull/N/head ref.
func pullRefNumber(ref string) (int, bool) {
	const prefix = "refs/pull/"
	if !strings.HasPrefix(ref, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(ref, prefix)
	numberText, _, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, false
	}
	number, err := strconv.Atoi(numberText)
	if err != nil {
		return 0, false
	}
	return number, true
}

// giteaJob is Gitea's ActionWorkflowJob.
type giteaJob struct {
	ID          int64  `json:"id"`
	RunID       int64  `json:"run_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	HTMLURL     string `json:"html_url"`
	Steps       []struct {
		Name        string `json:"name"`
		Status      string `json:"status"`
		Conclusion  string `json:"conclusion"`
		Number      int    `json:"number"`
		StartedAt   string `json:"started_at"`
		CompletedAt string `json:"completed_at"`
	} `json:"steps"`
}

type giteaJobsPayload struct {
	TotalCount int        `json:"total_count"`
	Jobs       []giteaJob `json:"jobs"`
}

func giteaJobFromRaw(raw giteaJob) Job {
	job := Job{
		ID:          raw.ID,
		Name:        raw.Name,
		Status:      Status(raw.Status),
		Conclusion:  Conclusion(raw.Conclusion),
		StartedAt:   giteaRunTime(raw.StartedAt),
		CompletedAt: giteaRunTime(raw.CompletedAt),
		URL:         raw.HTMLURL,
		RunID:       raw.RunID,
	}
	for _, s := range raw.Steps {
		job.Steps = append(job.Steps, Step{
			Name:        s.Name,
			Status:      Status(s.Status),
			Conclusion:  Conclusion(s.Conclusion),
			Number:      s.Number,
			StartedAt:   giteaRunTime(s.StartedAt),
			CompletedAt: giteaRunTime(s.CompletedAt),
		})
	}
	return job
}

// getJobsGitea lists a run's jobs.
//
// Gitea splits status and conclusion the same way GitHub does for jobs, so both
// map directly. There is no attempt-scoped jobs endpoint on Gitea, so attempt is
// ignored rather than honoured: a rerun's earlier attempt cannot be inspected.
func getJobsGitea(client *api.Client, repo ghrepo.Interface, runID int64) ([]Job, error) {
	const pageSize = 50

	jobs := []Job{}
	for page := 1; ; page++ {
		path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "runs", strconv.FormatInt(runID, 10), "jobs")
		if err != nil {
			return nil, err
		}
		path.SetQuery("page", strconv.Itoa(page))
		api.SetPageSizeMutable(path, repo.RepoHost(), pageSize)

		var payload giteaJobsPayload
		if err := client.REST(repo.RepoHost(), http.MethodGet, path.String(), nil, &payload); err != nil {
			return nil, err
		}

		for _, raw := range payload.Jobs {
			jobs = append(jobs, giteaJobFromRaw(raw))
		}

		if len(payload.Jobs) < pageSize {
			break
		}
	}

	return jobs, nil
}

// GetJobGitea fetches one job on a Gitea host.
//
// The path is the same shape as GitHub's; only the response body differs, since
// Gitea orders steps with a number field rather than by array position.
func GetJobGitea(client *api.Client, repo ghrepo.Interface, jobID string) (*Job, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "jobs", jobID)
	if err != nil {
		return nil, err
	}

	var raw giteaJob
	if err := client.REST(repo.RepoHost(), http.MethodGet, path.String(), nil, &raw); err != nil {
		return nil, err
	}

	job := giteaJobFromRaw(raw)
	return &job, nil
}

// giteaWorkflowRun is Gitea's ActionWorkflowRun, the answer of GET on a run.
type giteaWorkflowRun struct {
	ID           int64  `json:"id"`
	DisplayTitle string `json:"display_title"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	RunNumber    int64  `json:"run_number"`
	RunAttempt   int64  `json:"run_attempt"`
	Event        string `json:"event"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	URL          string `json:"html_url"`
	JobsURL      string `json:"jobs_url"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	StartedAt    string `json:"started_at"`
	CompletedAt  string `json:"completed_at"`
	Path         string `json:"path"`
}

// GetRunGitea fetches one run by id.
//
// Unlike the task list, this endpoint separates status and conclusion, so both are
// read directly. Gitea has no attempt-scoped run endpoint, so a nonzero attempt is
// reported rather than silently ignored.
func GetRunGitea(client *api.Client, repo ghrepo.Interface, runID string, attempt uint64) (*Run, error) {
	if attempt > 0 {
		return nil, fmt.Errorf("Gitea keeps no per-attempt run history, so `--attempt` is not supported")
	}

	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "actions", "runs", runID)
	if err != nil {
		return nil, err
	}

	var raw giteaWorkflowRun
	if err := client.REST(repo.RepoHost(), http.MethodGet, path.String(), nil, &raw); err != nil {
		return nil, err
	}

	run := Run{
		ID:           raw.ID,
		DisplayTitle: raw.DisplayTitle,
		HeadBranch:   raw.HeadBranch,
		HeadSha:      raw.HeadSHA,
		Number:       raw.RunNumber,
		Attempt:      uint64(raw.RunAttempt),
		Event:        raw.Event,
		Status:       Status(raw.Status),
		Conclusion:   Conclusion(raw.Conclusion),
		URL:          raw.URL,
		JobsURL:      raw.JobsURL,
		CreatedAt:    giteaRunTime(raw.CreatedAt),
		UpdatedAt:    giteaRunTime(raw.UpdatedAt),
		StartedAt:    giteaRunTime(raw.StartedAt),
	}
	run.Name = raw.Path
	return &run, nil
}
