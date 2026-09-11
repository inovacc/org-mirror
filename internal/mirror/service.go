package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Service struct {
	runner Runner
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
		if _, err := s.runner.Run(ctx, "", "gh", "repo", "clone", repository.NameWithOwner, path); err != nil {
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
	if _, err := s.runner.Run(ctx, path, "git", "fetch", "origin"); err != nil {
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

func NewService(runner Runner) *Service {
	return &Service{runner: runner}
}

func (s *Service) Discover(ctx context.Context, organization string) ([]Repository, error) {
	if _, err := s.runner.Run(ctx, "", "gh", "auth", "status"); err != nil {
		return nil, fmt.Errorf("verify GitHub CLI authentication: %w", err)
	}

	output, err := s.runner.Run(ctx, "", "gh", "repo", "list", organization, "--limit", "1000", "--json", "nameWithOwner,name,defaultBranchRef,isPrivate,isArchived,isFork")
	if err != nil {
		return nil, fmt.Errorf("list repositories for %s: %w", organization, err)
	}

	var rows []struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
		DefaultBranch *struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
		Private  bool `json:"isPrivate"`
		Archived bool `json:"isArchived"`
		Fork     bool `json:"isFork"`
	}
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		return nil, fmt.Errorf("parse GitHub repository list: %w", err)
	}

	repositories := make([]Repository, 0, len(rows))
	for _, row := range rows {
		branch := ""
		if row.DefaultBranch != nil {
			branch = row.DefaultBranch.Name
		}
		repositories = append(repositories, Repository{
			Name: row.Name, NameWithOwner: row.NameWithOwner, DefaultBranch: branch,
			Private: row.Private, Archived: row.Archived, Fork: row.Fork,
		})
	}
	return repositories, nil
}

func (s *Service) Mirror(ctx context.Context, organization, root string, dryRun bool) (Metadata, error) {
	repositories, err := s.Discover(ctx, organization)
	if err != nil {
		return Metadata{}, err
	}

	metadata := Metadata{
		Organization: organization,
		GeneratedAt:  time.Now().UTC(),
		Repositories: make([]Result, 0, len(repositories)),
	}
	organizationRoot := filepath.Join(root, organization)
	for _, repository := range repositories {
		metadata.Repositories = append(metadata.Repositories, s.Sync(ctx, repository, organizationRoot, dryRun))
	}
	if dryRun {
		return metadata, nil
	}
	if err := WriteMetadata(filepath.Join(organizationRoot, "metadata.json"), metadata); err != nil {
		return metadata, err
	}
	return metadata, nil
}
