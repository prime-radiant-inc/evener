package cmdutil

import (
	"fmt"
	"io"
	"time"

	"primeradiant.com/serf/llm"
)

var apiRawBodyEnabled = llm.RawBodyEnabled

// AttachAPILogger installs the standard Serf API logger on client. Entries
// route per session to <stateDir>/sessions/<session_id>.api.jsonl (raw HTTP
// bodies, when enabled, to <session_id>.api-raw.jsonl), sibling to the
// session's transcript. The legacy project-level api.jsonl is frozen: never
// written, migrated, or deleted. The returned function must be called by the
// caller during shutdown.
func AttachAPILogger(client *llm.Client, stateDir string, warnings io.Writer) (func() error, error) {
	apiLog, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		if warnings != nil {
			fmt.Fprintf(warnings, "warning: API logging disabled: %v\n", err) //nolint:errcheck
		}
		return func() error { return nil }, nil
	}

	apiLog.SyncInterval = 2 * time.Second
	if apiRawBodyEnabled() {
		apiLog.EnableSessionRawLogging()
	}
	client.Use(apiLog)
	return apiLog.Close, nil
}
