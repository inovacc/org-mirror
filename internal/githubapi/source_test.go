package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestSourceListsAndMapsOrganizationRepositories(t *testing.T) {
	client := &scriptedRESTClient{responses: map[string]string{
		"orgs/inovacc/repos?per_page=100&page=1&type=all": `[
			{"name":"private-api","full_name":"inovacc/private-api","clone_url":"https://github.com/inovacc/private-api.git","default_branch":"main","private":true,"archived":false,"fork":false},
			{"name":"legacy","full_name":"inovacc/legacy","clone_url":"https://github.com/inovacc/legacy.git","default_branch":"master","private":false,"archived":true,"fork":true}
		]`,
	}}

	repositories, err := NewSource(client).ListRepositories(context.Background(), "inovacc")
	if err != nil {
		t.Fatalf("list repositories: %v", err)
	}
	if len(repositories) != 2 {
		t.Fatalf("repository count = %d, want 2", len(repositories))
	}
	if repositories[0].NameWithOwner != "inovacc/private-api" || !repositories[0].Private {
		t.Fatalf("private repository mapped incorrectly: %#v", repositories[0])
	}
	if repositories[0].CloneURL != "https://github.com/inovacc/private-api.git" {
		t.Fatalf("clone URL mapped incorrectly: %#v", repositories[0])
	}
	if repositories[1].DefaultBranch != "master" || !repositories[1].Archived || !repositories[1].Fork {
		t.Fatalf("repository properties mapped incorrectly: %#v", repositories[1])
	}
	if len(client.paths) != 1 {
		t.Fatalf("request paths = %#v, want one page", client.paths)
	}
}

type scriptedRESTClient struct {
	responses map[string]string
	paths     []string
}

func (c *scriptedRESTClient) Get(path string, response interface{}) error {
	c.paths = append(c.paths, path)
	payload, ok := c.responses[path]
	if !ok {
		return fmt.Errorf("unexpected API path: %s", path)
	}
	return json.Unmarshal([]byte(payload), response)
}
