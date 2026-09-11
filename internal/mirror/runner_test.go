package mirror

import (
	"slices"
	"strings"
	"testing"
)

func TestPrepareGitCommandPassesCredentialOutsideArguments(t *testing.T) {
	args, environment := prepareCommand("git", []string{"clone", "https://github.com/inovacc/private.git", "target"}, "secret-token")

	if strings.Contains(strings.Join(args, " "), "secret-token") {
		t.Fatal("GitHub token must not appear in process arguments")
	}
	if !slices.Contains(environment, "ORG_MIRROR_GITHUB_TOKEN=secret-token") {
		t.Fatalf("credential environment missing: %#v", environment)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "credential.helper=") || !strings.Contains(joined, "ORG_MIRROR_GITHUB_TOKEN") {
		t.Fatalf("git credential helper was not configured: %#v", args)
	}
}
