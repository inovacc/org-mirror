package mirror

import (
	"context"
	"strings"
	"time"
)

// transientGitMessages are substrings of git's own output that mean the
// network failed rather than the request being wrong. Retrying these is safe;
// retrying anything else wastes time and can look like abuse.
var transientGitMessages = []string{
	"connection reset",
	"connection was reset",
	"connection refused",
	"early eof",
	"the remote end hung up",
	"rpc failed",
	"could not resolve host",
	"could not resolve proxy",
	"operation timed out",
	"timed out",
	"network is unreachable",
	"temporary failure in name resolution",
	"gnutls_handshake() failed",
	"ssl_read",
	"http 429",
	"http 500",
	"http 502",
	"http 503",
	"http 504",
	"unexpected disconnect",
	"transfer closed",
	"empty reply from server",
}

// IsTransientGitFailure reports whether a failed git command is worth retrying.
// It is false when err is nil, because a command that succeeded is not a failure.
func IsTransientGitFailure(output string, err error) bool {
	if err == nil {
		return false
	}
	haystack := strings.ToLower(output + " " + err.Error())
	for _, message := range transientGitMessages {
		if strings.Contains(haystack, message) {
			return true
		}
	}
	return false
}

// RetryPolicy governs how often a transient git failure is retried.
// The zero value performs a single attempt and never sleeps.
type RetryPolicy struct {
	// Attempts counts the first try. Values below 1 mean 1.
	Attempts int
	// Backoff returns how long to wait before the given attempt, counting from 1.
	Backoff func(attempt int) time.Duration
	// Sleep performs the wait and must honour the context.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (p RetryPolicy) attempts() int {
	if p.Attempts < 1 {
		return 1
	}
	return p.Attempts
}

func (p RetryPolicy) wait(ctx context.Context, attempt int) error {
	if p.Backoff == nil || p.Sleep == nil {
		return nil
	}
	delay := p.Backoff(attempt)
	if delay <= 0 {
		return nil
	}
	return p.Sleep(ctx, delay)
}
