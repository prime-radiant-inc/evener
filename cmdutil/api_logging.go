package cmdutil

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"primeradiant.com/serf/llm"
)

// AttachAPILogger installs the standard Serf API logger on client.
// The returned function must be called by the caller during shutdown.
func AttachAPILogger(client *llm.Client, stateDir string, warnings io.Writer) (func() error, error) {
	apiLogPath := filepath.Join(stateDir, "api.jsonl")
	apiLog, err := llm.NewAPILogger(apiLogPath)
	if err != nil {
		if warnings != nil {
			fmt.Fprintf(warnings, "warning: API logging disabled: %v\n", err) //nolint:errcheck
		}
		return func() error { return nil }, nil
	}

	apiLog.SyncInterval = 2 * time.Second
	if llm.RawBodyEnabled() {
		rawLogPath := filepath.Join(stateDir, "api-raw.jsonl")
		if err := apiLog.EnableRawLogging(rawLogPath); err != nil && warnings != nil {
			fmt.Fprintf(warnings, "warning: raw API logging disabled: %v\n", err) //nolint:errcheck
		}
	}
	client.Use(apiLog)
	return apiLog.Close, nil
}
