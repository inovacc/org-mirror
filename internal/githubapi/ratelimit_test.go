package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// jsonClient answers each path with a canned JSON document.
type jsonClient struct {
	documents map[string]string
	requested []string
	err       error
}

func (c *jsonClient) Get(path string, response any) error {
	c.requested = append(c.requested, path)
	if c.err != nil {
		return c.err
	}
	document, ok := c.documents[path]
	if !ok {
		return errors.New("unexpected path: " + path)
	}
	return json.Unmarshal([]byte(document), response)
}

func TestRateLimitsParsesEveryResource(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"rate_limit": `{"resources":{
			"core":{"limit":5000,"used":120,"remaining":4880,"reset":1700000000},
			"search":{"limit":30,"used":1,"remaining":29,"reset":1700000060}
		}}`,
	}}

	limits, err := NewSource(client).RateLimits(context.Background())
	if err != nil {
		t.Fatalf("rate limits: %v", err)
	}
	core, ok := limits.Resources["core"]
	if !ok || core.Remaining != 4880 || core.Limit != 5000 || core.Used != 120 {
		t.Fatalf("core = %#v", core)
	}
	if want := time.Unix(1700000000, 0).UTC(); !core.ResetAt().Equal(want) {
		t.Fatalf("reset = %v, want %v", core.ResetAt(), want)
	}
}

func TestRateLimitsOrdersCoreFirstThenTheRestAlphabetically(t *testing.T) {
	limits := RateLimits{Resources: map[string]RateLimit{
		"search":      {Limit: 30},
		"core":        {Limit: 5000},
		"graphql":     {Limit: 5000},
		"code_search": {Limit: 10},
	}}

	var names []string
	for _, resource := range limits.Ordered() {
		names = append(names, resource.Name)
	}
	want := []string{"core", "code_search", "graphql", "search"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestRateLimitsToleratesAPayloadWithoutOptionalResources(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"rate_limit": `{"resources":{"core":{"limit":5000,"used":0,"remaining":5000,"reset":1700000000}}}`,
	}}

	limits, err := NewSource(client).RateLimits(context.Background())
	if err != nil {
		t.Fatalf("rate limits: %v", err)
	}
	if len(limits.Ordered()) != 1 {
		t.Fatalf("resources = %d, want 1", len(limits.Ordered()))
	}
}

func TestRateLimitsWrapsTheClientError(t *testing.T) {
	client := &jsonClient{err: errors.New("network down")}

	if _, err := NewSource(client).RateLimits(context.Background()); err == nil {
		t.Fatal("a client failure must surface")
	}
}

func TestRepositoryCountSumsPublicAndPrivateRepositories(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"orgs/acme": `{"public_repos":12,"total_private_repos":30}`,
	}}

	count, err := NewSource(client).RepositoryCount(context.Background(), "acme")
	if err != nil {
		t.Fatalf("repository count: %v", err)
	}
	if count != 42 {
		t.Fatalf("count = %d, want 42", count)
	}
}

func TestRepositoryCountEscapesTheOrganization(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"orgs/a%2Fb": `{"public_repos":1,"total_private_repos":0}`,
	}}

	if _, err := NewSource(client).RepositoryCount(context.Background(), "a/b"); err != nil {
		t.Fatalf("repository count: %v", err)
	}
	if client.requested[0] != "orgs/a%2Fb" {
		t.Fatalf("requested %q, want the escaped path", client.requested[0])
	}
}

func TestRateLimitsRespectsACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &jsonClient{}
	if _, err := NewSource(client).RateLimits(ctx); err == nil {
		t.Fatal("a cancelled context must stop the call")
	}
	if len(client.requested) != 0 {
		t.Fatalf("request was made despite cancelled context: %v", client.requested)
	}
}

func TestRepositoryCountRespectsACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &jsonClient{}
	if _, err := NewSource(client).RepositoryCount(ctx, "acme"); err == nil {
		t.Fatal("a cancelled context must stop the call")
	}
	if len(client.requested) != 0 {
		t.Fatalf("request was made despite cancelled context: %v", client.requested)
	}
}
