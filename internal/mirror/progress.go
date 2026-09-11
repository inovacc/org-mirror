package mirror

import "time"

type ProgressKind string

const (
	ProgressDiscoveryStarted    ProgressKind = "discovery_started"
	ProgressDiscoveryCompleted  ProgressKind = "discovery_completed"
	ProgressRepositoryStarted   ProgressKind = "repository_started"
	ProgressRepositoryCompleted ProgressKind = "repository_completed"
	ProgressMetadataWritten     ProgressKind = "metadata_written"
	ProgressWaiting             ProgressKind = "waiting"
)

type ProgressEvent struct {
	Kind         ProgressKind
	Organization string
	Repository   Repository
	Result       Result
	Completed    int
	Total        int
	Path         string
	// Message explains a ProgressWaiting event in one line.
	Message string
	// Until is when a ProgressWaiting event expects to resume. Zero when unknown.
	Until time.Time
}

type ProgressFunc func(ProgressEvent)

func reportProgress(report ProgressFunc, event ProgressEvent) {
	if report != nil {
		report(event)
	}
}
