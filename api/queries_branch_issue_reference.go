package api

import (
	"fmt"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/shurcooL/githubv4"
)

type LinkedBranch struct {
	BranchName string
	URL        string
}

func CreateLinkedBranch(client *Client, host string, repoID, issueID, branchID, branchName string) (string, error) {
	var mutation struct {
		CreateLinkedBranch struct {
			LinkedBranch struct {
				ID  string
				Ref struct {
					Name string
				}
			}
		} `graphql:"createLinkedBranch(input: $input)"`
	}

	input := githubv4.CreateLinkedBranchInput{
		IssueID: githubv4.ID(issueID),
		Oid:     githubv4.GitObjectID(branchID),
	}
	if repoID != "" {
		repo := githubv4.ID(repoID)
		input.RepositoryID = &repo
	}
	if branchName != "" {
		name := githubv4.String(branchName)
		input.Name = &name
	}
	variables := map[string]any{
		"input": input,
	}

	err := client.Mutate(host, "CreateLinkedBranch", &mutation, variables)
	if err != nil {
		return "", err
	}

	return mutation.CreateLinkedBranch.LinkedBranch.Ref.Name, nil
}

func ListLinkedBranches(client *Client, repo ghrepo.Interface, issueNumber int) ([]LinkedBranch, error) {
	var query struct {
		Repository struct {
			Issue struct {
				LinkedBranches struct {
					Nodes []struct {
						Ref struct {
							Name       string
							Repository struct {
								Url string
							}
						}
					}
				} `graphql:"linkedBranches(first: 30)"`
			} `graphql:"issue(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]any{
		"number": githubv4.Int(issueNumber),
		"owner":  githubv4.String(repo.RepoOwner()),
		"name":   githubv4.String(repo.RepoName()),
	}

	if err := client.Query(repo.RepoHost(), "ListLinkedBranches", &query, variables); err != nil {
		return []LinkedBranch{}, err
	}

	var branchNames []LinkedBranch

	for _, node := range query.Repository.Issue.LinkedBranches.Nodes {
		branch := LinkedBranch{
			BranchName: node.Ref.Name,
			URL:        fmt.Sprintf("%s/tree/%s", node.Ref.Repository.Url, node.Ref.Name),
		}
		branchNames = append(branchNames, branch)
	}

	return branchNames, nil
}

// CheckLinkedBranchFeature reports whether the host supports linked branches.
//
// Gitea does not: it has no equivalent of GitHub's LinkedBranch type, so there
// is no branch that can be recorded as belonging to an issue. The check is
// answered here rather than by introspection, which would fail on a host with no
// GraphQL endpoint and report the failure as a transport error rather than as a
// missing feature.
func CheckLinkedBranchFeature(client *Client, host string) error {
	if hosttype.IsGitea(host) {
		return ErrGiteaLinkedBranchUnsupported
	}

	var query struct {
		Name struct {
			Fields []struct {
				Name string
			}
		} `graphql:"LinkedBranch: __type(name: \"LinkedBranch\")"`
	}

	if err := client.Query(host, "LinkedBranchFeature", &query, nil); err != nil {
		return err
	}

	if len(query.Name.Fields) == 0 {
		return fmt.Errorf("the `gm issue develop` command is not currently available")
	}

	return nil
}

// ErrGiteaLinkedBranchUnsupported explains why `gm issue develop` cannot work on
// a Gitea host.
var ErrGiteaLinkedBranchUnsupported = fmt.Errorf(
	"the `gm issue develop` command is not available on Gitea, which has no linked branches")

func FindRepoBranchID(client *Client, repo ghrepo.Interface, ref string) (string, string, error) {
	var query struct {
		Repository struct {
			Id               string
			DefaultBranchRef struct {
				Target struct {
					Oid string
				}
			}
			Ref struct {
				Target struct {
					Oid string
				}
			} `graphql:"ref(qualifiedName: $ref)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]any{
		"ref":   githubv4.String(ref),
		"owner": githubv4.String(repo.RepoOwner()),
		"name":  githubv4.String(repo.RepoName()),
	}

	if err := client.Query(repo.RepoHost(), "FindRepoBranchID", &query, variables); err != nil {
		return "", "", err
	}

	branchID := query.Repository.Ref.Target.Oid
	if branchID == "" {
		if ref != "" {
			return "", "", fmt.Errorf("could not find branch %q in %s", ref, ghrepo.FullName(repo))
		}
		branchID = query.Repository.DefaultBranchRef.Target.Oid
	}

	return query.Repository.Id, branchID, nil
}
