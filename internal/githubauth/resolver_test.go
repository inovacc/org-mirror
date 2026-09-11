package githubauth

import (
	"encoding/base64"
	"fmt"
	"testing"
)

func TestResolverReadsActiveGitHubCLIAccountFromKeyring(t *testing.T) {
	encoded := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte("gho_example_token"))
	config := mapConfig{
		"hosts/github.com/user": "octocat",
	}
	var service, account string
	resolver := Resolver{
		Config: config,
		LookupEnv: func(string) (string, bool) {
			return "", false
		},
		KeyringGet: func(gotService, gotAccount string) (string, error) {
			service, account = gotService, gotAccount
			return encoded, nil
		},
	}

	token, user, err := resolver.Token("github.com")
	if err != nil {
		t.Fatalf("resolve token: %v", err)
	}
	if token != "gho_example_token" || user != "octocat" {
		t.Fatalf("token/user = %q/%q", token, user)
	}
	if service != "gh:github.com" || account != "octocat" {
		t.Fatalf("keyring lookup = %q/%q", service, account)
	}
}

type mapConfig map[string]string

func (c mapConfig) Get(keys []string) (string, error) {
	key := ""
	for index, part := range keys {
		if index > 0 {
			key += "/"
		}
		key += part
	}
	value, ok := c[key]
	if !ok {
		return "", fmt.Errorf("missing config key: %s", key)
	}
	return value, nil
}
