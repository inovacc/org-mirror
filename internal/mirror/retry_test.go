package mirror

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestIsTransientGitFailure(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "connection reset", output: "fatal: unable to access 'https://github.com/acme/api.git/': Recv failure: Connection reset by peer", want: true},
		{name: "early EOF", output: "fatal: the remote end hung up unexpectedly\nfatal: early EOF", want: true},
		{name: "rpc failed", output: "error: RPC failed; curl 92 HTTP/2 stream 5 was not closed cleanly", want: true},
		{name: "unresolved host", output: "fatal: unable to access 'https://github.com/acme/api.git/': Could not resolve host: github.com", want: true},
		{name: "timeout", output: "fatal: unable to access 'https://github.com/acme/api.git/': Operation timed out after 30000 milliseconds", want: true},
		{name: "server error", output: "error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502", want: true},
		{name: "too many requests", output: "error: RPC failed; HTTP 429 curl 22 The requested URL returned error: 429", want: true},
		{name: "tls handshake", output: "fatal: unable to access 'https://github.com/acme/api.git/': OpenSSL SSL_read: Connection was reset", want: true},
		{name: "authentication", output: "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/acme/api.git/'", want: false},
		{name: "missing repository", output: "remote: Repository not found.\nfatal: repository 'https://github.com/acme/api.git/' not found", want: false},
		{name: "permission denied", output: "remote: Permission to acme/api.git denied to nobody.", want: false},
		{name: "not fast forward", output: "fatal: Not possible to fast-forward, aborting.", want: false},
		{name: "empty", output: "", want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsTransientGitFailure(testCase.output, errors.New("exit status 128")); got != testCase.want {
				t.Fatalf("IsTransientGitFailure(%q) = %v, want %v", testCase.output, got, testCase.want)
			}
		})
	}
}

func TestIsTransientGitFailureIsFalseWithoutAnError(t *testing.T) {
	if IsTransientGitFailure("Connection reset by peer", nil) {
		t.Fatal("a successful command is never a transient failure")
	}
}

func TestSyncRetriesATransientCloneFailure(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Name: "api", NameWithOwner: "acme/api", CloneURL: "https://github.com/acme/api.git"}
	path := filepath.Join(root, "api")
	cloneKey := commandKey("", "git", []string{"clone", repository.CloneURL, path})
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		cloneKey: {output: "fatal: early EOF", err: errors.New("exit status 128")},
		commandKey(path, "git", []string{"rev-parse", "HEAD"}): {output: "local-sha\n"},
		commandKey(path, "git", []string{"rev-parse", "@{u}"}): {output: "remote-sha\n"},
	}}
	runner.succeedAfter = map[string]int{cloneKey: 2}

	var slept []time.Duration
	service := NewServiceWithOptions(runner, nil, RetryPolicy{
		Attempts: 3,
		Backoff:  func(attempt int) time.Duration { return time.Duration(attempt) * time.Second },
		Sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	})

	result := service.Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeCloned {
		t.Fatalf("outcome = %s (%s), want cloned", result.Outcome, result.Message)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept %v, want one backoff of 1s", slept)
	}
}

func TestSyncDoesNotRetryAPermanentCloneFailure(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Name: "api", NameWithOwner: "acme/api", CloneURL: "https://github.com/acme/api.git"}
	path := filepath.Join(root, "api")
	cloneKey := commandKey("", "git", []string{"clone", repository.CloneURL, path})
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		cloneKey: {output: "remote: Repository not found.", err: errors.New("exit status 128")},
	}}

	service := NewServiceWithOptions(runner, nil, RetryPolicy{
		Attempts: 3,
		Backoff:  func(int) time.Duration { return time.Second },
		Sleep:    func(context.Context, time.Duration) error { return nil },
	})

	result := service.Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeError {
		t.Fatalf("outcome = %s, want error", result.Outcome)
	}
	if got := runner.countFor(cloneKey); got != 1 {
		t.Fatalf("clone attempted %d times, want 1", got)
	}
}

func TestSyncGivesUpAfterTheRetryBudget(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Name: "api", NameWithOwner: "acme/api", CloneURL: "https://github.com/acme/api.git"}
	path := filepath.Join(root, "api")
	cloneKey := commandKey("", "git", []string{"clone", repository.CloneURL, path})
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		cloneKey: {output: "fatal: early EOF", err: errors.New("exit status 128")},
	}}

	service := NewServiceWithOptions(runner, nil, RetryPolicy{
		Attempts: 3,
		Backoff:  func(int) time.Duration { return time.Second },
		Sleep:    func(context.Context, time.Duration) error { return nil },
	})

	result := service.Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeError {
		t.Fatalf("outcome = %s, want error", result.Outcome)
	}
	if got := runner.countFor(cloneKey); got != 3 {
		t.Fatalf("clone attempted %d times, want 3", got)
	}
}
