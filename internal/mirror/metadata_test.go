package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteMetadataWritesJSONAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orgs", "floci-io", "metadata.json")
	document := Metadata{Organization: "floci-io", GeneratedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}

	if err := WriteMetadata(path, document); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if !strings.Contains(string(content), "\"organization\": \"floci-io\"") {
		t.Fatalf("metadata content missing organization: %s", content)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".metadata-*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary metadata file remains: %v, %v", matches, err)
	}
}
