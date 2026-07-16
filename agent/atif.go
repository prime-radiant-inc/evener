package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/internal/atif"
)

var (
	atifMarshalIndent = json.MarshalIndent
	atifMkdirAll      = os.MkdirAll
	atifWriteFile     = os.WriteFile
)

// exportATIF reads a transcript JSONL file, converts it to an ATIF v1.7
// trajectory, and writes the result to outPath.
func exportATIF(transcriptPath, outPath, providerHandleMode string) error {
	mode, err := atif.NormalizeProviderHandleMode(providerHandleMode)
	if err != nil {
		return err
	}
	transcriptData, err := readTranscriptFull(transcriptPath)
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	traj := atif.ConvertTranscriptWithOptions(transcriptData.Header, transcriptData.Entries, atif.Options{ProviderHandles: mode})
	data, err := atifMarshalIndent(traj, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ATIF: %w", err)
	}
	if err := atifMkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := atifWriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write ATIF: %w", err)
	}
	return nil
}
