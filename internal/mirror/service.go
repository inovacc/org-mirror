package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Service struct {
	runner Runner
	source RepositorySource
	retry  RetryPolicy
}

func (s *Service) Sync(ctx context.Context, repository Repository, root string, dryRun bool) Result {
	path := filepath.Join(root, repository.Name)
	result := Result{Repository: repository, Path: path}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if dryRun {
			result.Outcome = OutcomeCloned
			result.Message = "dry-run: would clone repository"
			return result
		}
		if _, err := s.runGit(ctx, "", "clone", repository.CloneURL, path); err != nil {
			result.Outcome = OutcomeError
			result.Message = fmt.Sprintf("clone repository: %v", err)
			return result
		}
		result.Outcome = OutcomeCloned
		result.LocalSHA, result.RemoteSHA = s.repositorySHAs(ctx, path)
		return result
	} else if err != nil {
		result.Outcome = OutcomeError
		result.Message = fmt.Sprintf("inspect repository path: %v", err)
		return result
	}

	status, err := s.runner.Run(ctx, path, "git", "status", "--branch", "--porcelain")
	if err != nil {
		result.Outcome = OutcomeError
		result.Message = fmt.Sprintf("inspect Git status: %v", err)
		return result
	}
	result.LocalSHA, result.RemoteSHA = s.repositorySHAs(ctx, path)
	if message := conflictMessage(status); message != "" {
		result.Outcome = OutcomeConflict
		result.Message = message
		return result
	}
	if dryRun {
		result.Outcome = OutcomeUpdated
		result.Message = "dry-run: would fetch and fast-forward"
		return result
	}
	if _, err := s.runGit(ctx, path, "fetch", "origin"); err != nil {
		result.Outcome = OutcomeError
		result.Message = fmt.Sprintf("fetch origin: %v", err)
		return result
	}
	if _, err := s.runner.Run(ctx, path, "git", "merge", "--ff-only", "@{u}"); err != nil {
		result.Outcome = OutcomeError
		result.Message = fmt.Sprintf("fast-forward working copy: %v", err)
		return result
	}
	result.LocalSHA, result.RemoteSHA = s.repositorySHAs(ctx, path)
	result.Outcome = OutcomeUpdated
	return result
}

func (s *Service) repositorySHAs(ctx context.Context, path string) (string, string) {
	local, localErr := s.runner.Run(ctx, path, "git", "rev-parse", "HEAD")
	remote, remoteErr := s.runner.Run(ctx, path, "git", "rev-parse", "@{u}")
	if localErr != nil {
		local = ""
	}
	if remoteErr != nil {
		remote = ""
	}
	return strings.TrimSpace(local), strings.TrimSpace(remote)
}

func conflictMessage(status string) string {
	lines := strings.Split(strings.TrimSuffix(status, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return ""
	}
	branch := lines[0]
	if strings.Contains(branch, "HEAD (no branch)") {
		return "working copy is detached from a branch"
	}
	if strings.Contains(branch, "diverged") {
		return "working copy has diverged from its upstream"
	}
	if strings.Contains(branch, "ahead ") {
		return "working copy is ahead of its upstream"
	}
	if len(lines) > 1 {
		return "working tree has uncommitted changes"
	}
	return ""
}

type RepositorySource interface {
	ListRepositories(ctx context.Context, organization string) ([]Repository, error)
}

func NewService(runner Runner, sources ...RepositorySource) *Service {
	service := &Service{runner: runner}
	if len(sources) > 0 {
		service.source = sources[0]
	}
	return service
}

// NewServiceWithOptions builds a service that retries transient git failures.
// A nil source means discovery is unavailable, which suits Sync-only callers.
func NewServiceWithOptions(runner Runner, source RepositorySource, retry RetryPolicy) *Service {
	return &Service{runner: runner, source: source, retry: retry}
}

// runGit runs a network-touching git command, retrying transient failures.
// Local commands call the runner directly, because a local failure is real.
func (s *Service) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	attempts := s.retry.attempts()
	var output string
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		output, err = s.runner.Run(ctx, dir, "git", args...)
		if err == nil {
			return output, nil
		}
		if attempt == attempts || !IsTransientGitFailure(output, err) {
			return output, err
		}
		if waitErr := s.retry.wait(ctx, attempt); waitErr != nil {
			return output, waitErr
		}
	}
	return output, err
}

func (s *Service) Discover(ctx context.Context, organization string) ([]Repository, error) {
	if s.source == nil {
		return nil, errors.New("repository source is not configured")
	}
	return s.source.ListRepositories(ctx, organization)
}

func (s *Service) Mirror(ctx context.Context, organization, root string, dryRun bool) (Metadata, error) {
	return s.MirrorWithProgress(ctx, organization, root, dryRun, nil)
}

func (s *Service) MirrorWithProgress(ctx context.Context, organization, root string, dryRun bool, report ProgressFunc) (Metadata, error) {
	reportProgress(report, ProgressEvent{Kind: ProgressDiscoveryStarted, Organization: organization})
	repositories, err := s.Discover(ctx, organization)
	if err != nil {
		return Metadata{}, err
	}
	reportProgress(report, ProgressEvent{Kind: ProgressDiscoveryCompleted, Organization: organization, Total: len(repositories)})

	metadata := Metadata{
		Organization: organization,
		GeneratedAt:  time.Now().UTC(),
		Repositories: make([]Result, 0, len(repositories)),
	}
	organizationRoot := filepath.Join(root, organization)
	for index, repository := range repositories {
		reportProgress(report, ProgressEvent{Kind: ProgressRepositoryStarted, Organization: organization, Repository: repository, Completed: index, Total: len(repositories)})
		result := s.Sync(ctx, repository, organizationRoot, dryRun)
		metadata.Repositories = append(metadata.Repositories, result)
		reportProgress(report, ProgressEvent{Kind: ProgressRepositoryCompleted, Organization: organization, Repository: repository, Result: result, Completed: index + 1, Total: len(repositories)})
	}
	if dryRun {
		return metadata, nil
	}
	if err := WriteMetadata(filepath.Join(organizationRoot, "metadata.json"), metadata); err != nil {
		return metadata, err
	}
	reportProgress(report, ProgressEvent{Kind: ProgressMetadataWritten, Organization: organization, Completed: len(repositories), Total: len(repositories), Path: filepath.Join(organizationRoot, "metadata.json")})
	return metadata, nil
}
