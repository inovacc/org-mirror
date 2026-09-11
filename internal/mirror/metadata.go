package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func WriteMetadata(path string, document Metadata) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".metadata-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary metadata file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		temporary.Close()
		return fmt.Errorf("encode metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary metadata file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace metadata file: %w", err)
	}
	return nil
}
