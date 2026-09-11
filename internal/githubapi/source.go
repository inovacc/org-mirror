package githubapi

import (
	"context"
	"fmt"
	"net/url"

	"github.com/inovacc/org-mirror/internal/mirror"
)

type RESTClient interface {
	Get(path string, response interface{}) error
}

type Source struct {
	client RESTClient
}

func NewSource(client RESTClient) *Source {
	return &Source{client: client}
}

func (s *Source) ListRepositories(ctx context.Context, organization string) ([]mirror.Repository, error) {
	var repositories []mirror.Repository
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var rows []struct {
			Name          string `json:"name"`
			NameWithOwner string `json:"full_name"`
			CloneURL      string `json:"clone_url"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
			Archived      bool   `json:"archived"`
			Fork          bool   `json:"fork"`
			OpenIssues    int    `json:"open_issues_count"`
		}
		path := fmt.Sprintf("orgs/%s/repos?per_page=100&page=%d&type=all", url.PathEscape(organization), page)
		if err := s.client.Get(path, &rows); err != nil {
			return nil, fmt.Errorf("list repositories for %s: %w", organization, err)
		}
		for _, row := range rows {
			repositories = append(repositories, mirror.Repository{
				Name:          row.Name,
				NameWithOwner: row.NameWithOwner,
				CloneURL:      row.CloneURL,
				DefaultBranch: row.DefaultBranch,
				Private:       row.Private,
				Archived:      row.Archived,
				Fork:          row.Fork,
				OpenIssues:    row.OpenIssues,
			})
		}
		if len(rows) < 100 {
			return repositories, nil
		}
	}
}
