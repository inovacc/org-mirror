package mirror

type ProgressKind string

const (
	ProgressDiscoveryStarted    ProgressKind = "discovery_started"
	ProgressDiscoveryCompleted  ProgressKind = "discovery_completed"
	ProgressRepositoryStarted   ProgressKind = "repository_started"
	ProgressRepositoryCompleted ProgressKind = "repository_completed"
	ProgressMetadataWritten     ProgressKind = "metadata_written"
)

type ProgressEvent struct {
	Kind         ProgressKind
	Organization string
	Repository   Repository
	Result       Result
	Completed    int
	Total        int
	Path         string
}

type ProgressFunc func(ProgressEvent)

func reportProgress(report ProgressFunc, event ProgressEvent) {
	if report != nil {
		report(event)
	}
}
