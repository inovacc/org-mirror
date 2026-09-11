package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	dir  string
	name string
	args []string
}

func TestMirrorWritesAResultForEveryDiscoveredRepository(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "floci-io", "api")
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		commandKey("", "gh", []string{"auth", "status"}): {},
		commandKey("", "gh", []string{"repo", "list", "floci-io", "--limit", "1000", "--json", "nameWithOwner,name,defaultBranchRef,isPrivate,isArchived,isFork"}): {
			output: `[{"name":"api","nameWithOwner":"floci-io/api","defaultBranchRef":{"name":"main"}}]`,
		},
		commandKey("", "gh", []string{"repo", "clone", "floci-io/api", path}): {},
	}}

	metadata, err := NewService(runner).Mirror(context.Background(), "floci-io", root, false)
	if err != nil {
		t.Fatalf("mirror organization: %v", err)
	}
	if len(metadata.Repositories) != 1 || metadata.Repositories[0].Outcome != OutcomeCloned {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if _, err := os.Stat(filepath.Join(root, "floci-io", "metadata.json")); err != nil {
		t.Fatalf("metadata file was not written: %v", err)
	}
}

func TestSyncClonesMissingRepository(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Name: "api", NameWithOwner: "floci-io/api"}
	path := filepath.Join(root, "api")
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		commandKey("", "gh", []string{"repo", "clone", "floci-io/api", path}): {},
		commandKey(path, "git", []string{"rev-parse", "HEAD"}):                {output: "local-sha\n"},
		commandKey(path, "git", []string{"rev-parse", "@{u}"}):                {output: "remote-sha\n"},
	}}

	result := NewService(runner).Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeCloned || result.Path != path || result.LocalSHA != "local-sha" || result.RemoteSHA != "remote-sha" {
		t.Fatalf("unexpected clone result: %#v", result)
	}
}

func TestSyncFastForwardsCleanBehindWorkingCopy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "api")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	repository := Repository{Name: "api", NameWithOwner: "floci-io/api"}
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		commandKey(path, "git", []string{"status", "--branch", "--porcelain"}): {output: "## main...origin/main [behind 1]\n"},
		commandKey(path, "git", []string{"fetch", "origin"}):                   {},
		commandKey(path, "git", []string{"merge", "--ff-only", "@{u}"}):        {},
		commandKey(path, "git", []string{"rev-parse", "HEAD"}):                 {output: "local-sha\n"},
		commandKey(path, "git", []string{"rev-parse", "@{u}"}):                 {output: "remote-sha\n"},
	}}

	result := NewService(runner).Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeUpdated || result.LocalSHA != "local-sha" || result.RemoteSHA != "remote-sha" {
		t.Fatalf("unexpected update result: %#v", result)
	}
}

func TestSyncPreservesDirtyWorkingCopyAsConflict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "api")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		commandKey(path, "git", []string{"status", "--branch", "--porcelain"}): {output: "## main...origin/main\n M README.md\n"},
		commandKey(path, "git", []string{"rev-parse", "HEAD"}):                 {output: "local-sha\n"},
		commandKey(path, "git", []string{"rev-parse", "@{u}"}):                 {output: "remote-sha\n"},
	}}

	result := NewService(runner).Sync(context.Background(), Repository{Name: "api"}, root, false)
	if result.Outcome != OutcomeConflict || !strings.Contains(result.Message, "uncommitted") {
		t.Fatalf("unexpected conflict result: %#v", result)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("conflicted repository must not fetch or merge: %#v", runner.calls)
	}
}

type scriptedRunner struct {
	calls     []call
	responses map[string]scriptedResponse
}

type scriptedResponse struct {
	output string
	err    error
}

func (r *scriptedRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	r.calls = append(r.calls, call{dir: dir, name: name, args: args})
	response, ok := r.responses[commandKey(dir, name, args)]
	if !ok {
		return "", fmt.Errorf("unexpected command: %s", commandKey(dir, name, args))
	}
	return response.output, response.err
}

func commandKey(dir, name string, args []string) string {
	return strings.Join(append([]string{dir, name}, args...), " ")
}

func TestDiscoverChecksAuthenticationAndParsesRepositories(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		commandKey("", "gh", []string{"auth", "status"}): {},
		commandKey("", "gh", []string{"repo", "list", "floci-io", "--limit", "1000", "--json", "nameWithOwner,name,defaultBranchRef,isPrivate,isArchived,isFork"}): {
			output: `[{"name":"api","nameWithOwner":"floci-io/api","defaultBranchRef":{"name":"main"},"isPrivate":true,"isArchived":false,"isFork":false}]`,
		},
	}}

	repositories, err := NewService(runner).Discover(context.Background(), "floci-io")
	if err != nil {
		t.Fatalf("discover repositories: %v", err)
	}
	if len(repositories) != 1 || repositories[0].DefaultBranch != "main" || !repositories[0].Private {
		t.Fatalf("unexpected repositories: %#v", repositories)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected authentication plus listing commands, got %#v", runner.calls)
	}
}
