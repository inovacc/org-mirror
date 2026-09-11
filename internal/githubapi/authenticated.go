package githubapi

import (
	"fmt"
	"os"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/config"
	"github.com/inovacc/org-mirror/internal/githubauth"
	"github.com/zalando/go-keyring"
)

func NewAuthenticatedSource(host string) (*Source, string, string, error) {
	configuration, err := config.Read(nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("read GitHub CLI configuration: %w", err)
	}
	resolver := githubauth.Resolver{
		Config:     configuration,
		LookupEnv:  os.LookupEnv,
		KeyringGet: keyring.Get,
	}
	token, user, err := resolver.Token(host)
	if err != nil {
		return nil, "", user, err
	}
	client, err := api.NewRESTClient(api.ClientOptions{
		Host:         host,
		AuthToken:    token,
		Timeout:      30 * time.Second,
		LogIgnoreEnv: true,
	})
	if err != nil {
		return nil, "", user, fmt.Errorf("create GitHub API client: %w", err)
	}
	return NewSource(client), token, user, nil
}
