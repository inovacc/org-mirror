package mirror

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMetadataMarshalsConflictResult(t *testing.T) {
	document := Metadata{
		Organization: "floci-io",
		GeneratedAt:  time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
		Repositories: []Result{{
			Repository: Repository{Name: "api", NameWithOwner: "floci-io/api"},
			Outcome:    OutcomeConflict,
			Message:    "working tree has uncommitted changes",
		}},
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if string(encoded) == "" {
		t.Fatal("metadata must not marshal to an empty JSON document")
	}
}

func TestSkippedOutcomeHasItsWireValue(t *testing.T) {
	if OutcomeSkipped != "skipped" {
		t.Fatalf("OutcomeSkipped = %q, want skipped", OutcomeSkipped)
	}
}
