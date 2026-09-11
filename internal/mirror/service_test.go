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
		commandKey("", "git", []string{"clone", "https://github.com/floci-io/api.git", path}): {},
	}}
	source := &scriptedRepositorySource{repositories: []Repository{{
		Name: "api", NameWithOwner: "floci-io/api", CloneURL: "https://github.com/floci-io/api.git", DefaultBranch: "main",
	}}}

	metadata, err := NewService(runner, source).Mirror(context.Background(), "floci-io", root, false)
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
	repository := Repository{Name: "api", NameWithOwner: "floci-io/api", CloneURL: "https://github.com/floci-io/api.git"}
	path := filepath.Join(root, "api")
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		commandKey("", "git", []string{"clone", "https://github.com/floci-io/api.git", path}): {},
		commandKey(path, "git", []string{"rev-parse", "HEAD"}):                                {output: "local-sha\n"},
		commandKey(path, "git", []string{"rev-parse", "@{u}"}):                                {output: "remote-sha\n"},
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

func TestMirrorReportsDiscoveryAndRepositoryProgress(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{}
	source := &scriptedRepositorySource{repositories: []Repository{
		{Name: "api", NameWithOwner: "floci-io/api"},
		{Name: "web", NameWithOwner: "floci-io/web"},
	}}

	var events []ProgressEvent
	_, err := NewService(runner, source).MirrorWithProgress(context.Background(), "floci-io", root, true, func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("mirror organization: %v", err)
	}

	wantKinds := []ProgressKind{
		ProgressDiscoveryStarted,
		ProgressDiscoveryCompleted,
		ProgressRepositoryStarted,
		ProgressRepositoryCompleted,
		ProgressRepositoryStarted,
		ProgressRepositoryCompleted,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("progress events = %d, want %d: %#v", len(events), len(wantKinds), events)
	}
	for index, want := range wantKinds {
		if events[index].Kind != want {
			t.Fatalf("event %d kind = %q, want %q", index, events[index].Kind, want)
		}
	}
	if events[1].Total != 2 || events[3].Completed != 1 || events[5].Completed != 2 {
		t.Fatalf("unexpected progress counts: %#v", events)
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

func TestDiscoverUsesRepositorySourceWithoutExecutingGH(t *testing.T) {
	source := &scriptedRepositorySource{repositories: []Repository{{
		Name: "api", NameWithOwner: "floci-io/api", DefaultBranch: "main", Private: true,
	}}}

	repositories, err := NewService(&scriptedRunner{}, source).Discover(context.Background(), "floci-io")
	if err != nil {
		t.Fatalf("discover repositories: %v", err)
	}
	if len(repositories) != 1 || repositories[0].DefaultBranch != "main" || !repositories[0].Private {
		t.Fatalf("unexpected repositories: %#v", repositories)
	}
	if source.organization != "floci-io" {
		t.Fatalf("organization = %q, want floci-io", source.organization)
	}
}

type scriptedRepositorySource struct {
	organization string
	repositories []Repository
	err          error
}

func (s *scriptedRepositorySource) ListRepositories(_ context.Context, organization string) ([]Repository, error) {
	s.organization = organization
	return s.repositories, s.err
}
