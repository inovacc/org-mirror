package githubapi

import (
	"fmt"
	"os"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/config"
	"github.com/inovacc/org-mirror/internal/githubauth"
	"github.com/inovacc/org-mirror/internal/ratelimit"
	"github.com/zalando/go-keyring"
)

// ClientOptions tunes the rate limiting applied to every API request.
type ClientOptions struct {
	// Delay is the minimum interval between requests. Zero disables pacing.
	Delay time.Duration
	// MaxWait caps a single rate-limit wait. Zero uses the package default.
	MaxWait time.Duration
	// Notify, when set, is called before each wait so a caller can show it.
	Notify func(ratelimit.Wait)
}

// NewAuthenticatedSource builds a source whose transport honours GitHub's rate
// limits. The transport is returned so a caller can read the current budget.
func NewAuthenticatedSource(host string, options ClientOptions) (*Source, *ratelimit.Transport, string, string, error) {
	configuration, err := config.Read(nil)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("read GitHub CLI configuration: %w", err)
	}
	resolver := githubauth.Resolver{
		Config:     configuration,
		LookupEnv:  os.LookupEnv,
		KeyringGet: keyring.Get,
	}
	token, user, err := resolver.Token(host)
	if err != nil {
		return nil, nil, "", user, err
	}

	transport := ratelimit.NewTransport(ratelimit.TransportOptions{
		Pacer:   ratelimit.NewPacer(ratelimit.PacerOptions{Interval: options.Delay, Jitter: 0.3}),
		MaxWait: options.MaxWait,
		Notify:  options.Notify,
	})
	client, err := api.NewRESTClient(api.ClientOptions{
		Host:         host,
		AuthToken:    token,
		Timeout:      30 * time.Second,
		LogIgnoreEnv: true,
		Transport:    transport,
	})
	if err != nil {
		return nil, nil, "", user, fmt.Errorf("create GitHub API client: %w", err)
	}
	return NewSource(client), transport, token, user, nil
}
