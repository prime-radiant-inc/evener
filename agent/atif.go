package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/internal/atif"
)

// exportATIF reads a transcript JSONL file, converts it to an ATIF v1.7
// trajectory, and writes the result to outPath.
func exportATIF(transcriptPath, outPath string) error {
	header, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	traj := atif.Convert(header, entries)
	data, err := json.MarshalIndent(traj, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ATIF: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write ATIF: %w", err)
	}
	return nil
}
