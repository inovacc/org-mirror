package mirror

import "time"

type Repository struct {
	Name          string `json:"name"`
	NameWithOwner string `json:"nameWithOwner"`
	CloneURL      string `json:"cloneURL"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	OpenIssues    int    `json:"openIssues"`
}

type Outcome string

const (
	OutcomeCloned              Outcome = "cloned"
	OutcomeUpdated             Outcome = "updated"
	OutcomeUnchanged           Outcome = "unchanged"
	OutcomeConflict            Outcome = "conflict"
	OutcomeError               Outcome = "error"
	OutcomeSkipped             Outcome = "skipped"
	OutcomeAbsentFromDiscovery Outcome = "absent_from_discovery"
)

type Result struct {
	Repository Repository `json:"repository"`
	Path       string     `json:"path,omitempty"`
	Outcome    Outcome    `json:"outcome"`
	Message    string     `json:"message,omitempty"`
	LocalSHA   string     `json:"localSHA,omitempty"`
	RemoteSHA  string     `json:"remoteSHA,omitempty"`
}

type Metadata struct {
	Organization string    `json:"organization"`
	GeneratedAt  time.Time `json:"generatedAt"`
	Repositories []Result  `json:"repositories"`
	// Truncated reports that a repository cap stopped the run before every
	// repository was processed, so the next run has work to continue.
	Truncated bool `json:"truncated,omitempty"`
}
