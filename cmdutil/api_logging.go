package cmdutil

import (
	"fmt"
	"io"
	"time"

	"primeradiant.com/serf/llm"
)

// AttachAPILogger installs the standard Serf API logger on client. Entries
// route per session to <stateDir>/sessions/<session_id>.api.jsonl, sibling to
// the session transcript. The returned function must be called during shutdown.
func AttachAPILogger(client *llm.Client, stateDir string, warnings io.Writer) (func() error, error) {
	apiLog, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		if warnings != nil {
			fmt.Fprintf(warnings, "warning: API logging disabled: %v\n", err) //nolint:errcheck
		}
		return func() error { return nil }, nil
	}

	apiLog.SyncInterval = 2 * time.Second
	if warnings != nil {
		apiLog.SetFailureObserver(func(failure llm.APILogFailure) {
			fmt.Fprintf(warnings, "warning: canonical API log %s failed (session=%s group=%s attempt=%s): %v\n",
				failure.Operation, failure.SessionID, failure.AttemptGroupID, failure.AttemptID, failure.Err) //nolint:errcheck
		})
	}
	client.Use(apiLog)
	return apiLog.Close, nil
}
