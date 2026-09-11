package mirror

import (
	"context"
	"errors"
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
	responses map[string]scriptedResponse
	calls     []call
	counts    map[string]int
	// succeedAfter maps a command key to the attempt number on which it starts
	// succeeding, so a transient failure can be scripted.
	succeedAfter map[string]int
}

type scriptedResponse struct {
	output string
	err    error
}

func (r *scriptedRunner) countFor(key string) int {
	return r.counts[key]
}

func (r *scriptedRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	r.calls = append(r.calls, call{dir: dir, name: name, args: args})
	key := commandKey(dir, name, args)
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[key]++
	if after, ok := r.succeedAfter[key]; ok && r.counts[key] >= after {
		return "", nil
	}
	response, ok := r.responses[key]
	if !ok {
		return "", fmt.Errorf("unexpected command: %s", key)
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

type countingWaiter struct {
	waits int
	err   error
}

func (w *countingWaiter) Wait(context.Context) error {
	w.waits++
	return w.err
}

func threeRepositories() *scriptedRepositorySource {
	return &scriptedRepositorySource{repositories: []Repository{
		{Name: "one", NameWithOwner: "acme/one", CloneURL: "https://github.com/acme/one.git"},
		{Name: "two", NameWithOwner: "acme/two", CloneURL: "https://github.com/acme/two.git"},
		{Name: "three", NameWithOwner: "acme/three", CloneURL: "https://github.com/acme/three.git"},
	}}
}

func clonesFor(root string, names ...string) map[string]scriptedResponse {
	responses := map[string]scriptedResponse{}
	for _, name := range names {
		path := filepath.Join(root, "acme", name)
		url := "https://github.com/acme/" + name + ".git"
		responses[commandKey("", "git", []string{"clone", url, path})] = scriptedResponse{}
		responses[commandKey(path, "git", []string{"rev-parse", "HEAD"})] = scriptedResponse{output: "local\n"}
		responses[commandKey(path, "git", []string{"rev-parse", "@{u}"})] = scriptedResponse{output: "remote\n"}
	}
	return responses
}

func TestMirrorPacesEveryRepositoryThatRunsGit(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one", "two", "three")}
	waiter := &countingWaiter{}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Pacer: waiter})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(metadata.Repositories) != 3 {
		t.Fatalf("results = %d, want 3", len(metadata.Repositories))
	}
	if waiter.waits != 3 {
		t.Fatalf("waits = %d, want one per repository", waiter.waits)
	}
}

func TestMirrorPacesAfterAFailedRepository(t *testing.T) {
	root := t.TempDir()
	responses := clonesFor(root, "two", "three")
	failedPath := filepath.Join(root, "acme", "one")
	responses[commandKey("", "git", []string{"clone", "https://github.com/acme/one.git", failedPath})] = scriptedResponse{
		output: "remote: Repository not found.", err: errors.New("exit status 128"),
	}
	runner := &scriptedRunner{responses: responses}
	waiter := &countingWaiter{}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Pacer: waiter})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if metadata.Repositories[0].Outcome != OutcomeError {
		t.Fatalf("first outcome = %s, want error", metadata.Repositories[0].Outcome)
	}
	if waiter.waits != 3 {
		t.Fatalf("waits = %d, want the error path to pace like any other", waiter.waits)
	}
}

func TestMirrorSkipsRepositoriesRecordedInAnEarlierRun(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "two", "three")}
	waiter := &countingWaiter{}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			Pacer: waiter,
			Skip:  map[string]string{"acme/one": "completed in run 7"},
		})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if metadata.Repositories[0].Outcome != OutcomeSkipped {
		t.Fatalf("first outcome = %s, want skipped", metadata.Repositories[0].Outcome)
	}
	if metadata.Repositories[0].Message != "completed in run 7" {
		t.Fatalf("message = %q", metadata.Repositories[0].Message)
	}
	if waiter.waits != 2 {
		t.Fatalf("waits = %d, want a skip not to pace", waiter.waits)
	}
}

func TestMirrorStopsAtTheRepositoryLimit(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one", "two")}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Limit: 2})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(metadata.Repositories) != 2 {
		t.Fatalf("results = %d, want 2", len(metadata.Repositories))
	}
	if !metadata.Truncated {
		t.Fatal("a capped run must report that work remains")
	}
}

func TestMirrorLimitCountsOnlyProcessedRepositories(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "two")}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			Limit: 1,
			Skip:  map[string]string{"acme/one": "already done"},
		})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(metadata.Repositories) != 2 {
		t.Fatalf("results = %d, want the skip plus one processed repository", len(metadata.Repositories))
	}
	if metadata.Repositories[1].Repository.Name != "two" {
		t.Fatalf("processed %q, want two", metadata.Repositories[1].Repository.Name)
	}
	if !metadata.Truncated {
		t.Fatal("a capped run must report that work remains")
	}
}

func TestMirrorCallsTheResultHookForEveryRepository(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "two", "three")}
	var recorded []string

	_, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			Skip: map[string]string{"acme/one": "already done"},
			OnResult: func(result Result) error {
				recorded = append(recorded, string(result.Outcome)+" "+result.Repository.Name)
				return nil
			},
		})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	want := []string{"skipped one", "cloned two", "cloned three"}
	if len(recorded) != len(want) {
		t.Fatalf("recorded %v, want %v", recorded, want)
	}
	for index := range want {
		if recorded[index] != want[index] {
			t.Fatalf("recorded %v, want %v", recorded, want)
		}
	}
}

func TestMirrorAbortsWhenTheResultHookFails(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one")}

	_, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			OnResult: func(Result) error { return errors.New("disk full") },
		})
	if err == nil {
		t.Fatal("a failed checkpoint must abort the run")
	}
}

func TestMirrorStopsWhenThePacerReportsCancellation(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one", "two", "three")}
	waiter := &countingWaiter{err: context.Canceled}

	_, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Pacer: waiter})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
