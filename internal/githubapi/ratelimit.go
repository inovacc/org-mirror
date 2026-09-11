package githubapi

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"
)

// RateLimit is one resource's budget as GitHub reports it.
type RateLimit struct {
	Limit     int   `json:"limit"`
	Used      int   `json:"used"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

// ResetAt is when the budget refills.
func (l RateLimit) ResetAt() time.Time { return time.Unix(l.Reset, 0).UTC() }

// NamedRateLimit pairs a resource with its name for ordered rendering.
type NamedRateLimit struct {
	Name string
	RateLimit
}

// RateLimits is the whole rate_limit payload.
type RateLimits struct {
	Resources map[string]RateLimit `json:"resources"`
}

// Ordered lists core first, because it is the budget a sync spends, then the
// remaining resources alphabetically. Absent resources are simply not listed.
func (l RateLimits) Ordered() []NamedRateLimit {
	names := make([]string, 0, len(l.Resources))
	for name := range l.Resources {
		names = append(names, name)
	}
	sort.Slice(names, func(first, second int) bool {
		if names[first] == "core" {
			return true
		}
		if names[second] == "core" {
			return false
		}
		return names[first] < names[second]
	})
	ordered := make([]NamedRateLimit, 0, len(names))
	for _, name := range names {
		ordered = append(ordered, NamedRateLimit{Name: name, RateLimit: l.Resources[name]})
	}
	return ordered
}

// RateLimits reads the authenticated account's remaining budget. The call does
// not itself consume the core budget.
func (s *Source) RateLimits(ctx context.Context) (RateLimits, error) {
	if err := ctx.Err(); err != nil {
		return RateLimits{}, err
	}
	var limits RateLimits
	if err := s.client.Get("rate_limit", &limits); err != nil {
		return RateLimits{}, fmt.Errorf("read rate limits: %w", err)
	}
	return limits, nil
}

// RepositoryCount reports how many repositories an organization holds, which
// is what turns a remaining budget into an answer about a planned sync.
func (s *Source) RepositoryCount(ctx context.Context, organization string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var payload struct {
		PublicRepos  int `json:"public_repos"`
		TotalPrivate int `json:"total_private_repos"`
	}
	path := fmt.Sprintf("orgs/%s", url.PathEscape(organization))
	if err := s.client.Get(path, &payload); err != nil {
		return 0, fmt.Errorf("read organization %s: %w", organization, err)
	}
	return payload.PublicRepos + payload.TotalPrivate, nil
}
