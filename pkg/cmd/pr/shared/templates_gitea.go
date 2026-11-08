package shared

import (
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
)

// Gitea has no GraphQL API, so issue templates come from REST.
//
// GET /repos/{o}/{r}/issue_templates answers with null rather than an empty
// array when a repository has none, which a JSON decode into a slice handles as
// an empty slice, so there is no special case for it.
//
// Gitea's issue create takes no template parameter. The template is therefore
// useful for its body, which is what the interactive flow asks for, and
// NameForSubmit returns nothing so the submit path does not put a template name
// in a parameter the host would reject. The body still reaches the issue: the
// caller seeds the survey with it, and the survey's result is what gets sent.
//
// A template's structured form fields are not translated into a body. Gitea
// renders those in its own web UI from the body array; here the template's
// content field is what carries pre-written markdown, and a template with only
// fields and no content simply offers an empty body.

// giteaIssueTemplate is the subset of Gitea's IssueTemplate gm uses.
type giteaIssueTemplate struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	FileName string `json:"file_name"`
	About    string `json:"about"`
}

// giteaIssueTemplate adapts a Gitea template to the Template interface.
type giteaIssueTemplateAdapter struct {
	template giteaIssueTemplate
}

func (t *giteaIssueTemplateAdapter) Name() string {
	if t.template.Name != "" {
		return t.template.Name
	}
	return t.template.FileName
}

// NameForSubmit returns nothing: Gitea's issue create has no template
// parameter, so naming one would be a parameter the host rejects.
func (t *giteaIssueTemplateAdapter) NameForSubmit() string {
	return ""
}

func (t *giteaIssueTemplateAdapter) Body() []byte {
	return []byte(t.template.Content)
}

func (t *giteaIssueTemplateAdapter) Title() string {
	return t.template.Title
}

func listIssueTemplatesGitea(httpClient *http.Client, repo ghrepo.Interface) ([]Template, error) {
	var raw []giteaIssueTemplate

	path := "repos/" + repo.RepoOwner() + "/" + repo.RepoName() + "/issue_templates"
	err := api.NewClientFromHTTP(httpClient).REST(repo.RepoHost(), http.MethodGet, path, nil, &raw)
	if err != nil {
		return nil, err
	}

	templates := make([]Template, 0, len(raw))
	for i := range raw {
		templates = append(templates, &giteaIssueTemplateAdapter{template: raw[i]})
	}
	return templates, nil
}

// listTemplates dispatches on host type. Gitea has no pull request template
// query, so a pull request template request falls back to the filesystem, which
// is what hasAPI already reports as unsupported.
func listIssueTemplatesForHost(httpClient *http.Client, repo ghrepo.Interface) ([]Template, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return listIssueTemplatesGitea(httpClient, repo)
	}
	return listIssueTemplates(httpClient, repo)
}
