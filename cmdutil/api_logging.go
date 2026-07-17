package cmdutil

import (
	"errors"
	"fmt"
	"io"

	"primeradiant.com/serf/llm"
)

// AttachAPILogger installs the standard Serf API logger on client. Entries
// route per session to <stateDir>/sessions/<session_id>.api.jsonl, sibling to
// the session transcript. The returned function must be called during shutdown.
func AttachAPILogger(client *llm.Client, stateDir string, warnings io.Writer, resumedSessionID ...string) (func() error, error) {
	if len(resumedSessionID) > 1 {
		return nil, errors.New("attach API logger accepts at most one resumed session ID")
	}
	apiLog, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		return nil, fmt.Errorf("initialize canonical API log: %w", err)
	}
	if len(resumedSessionID) == 1 {
		sessionID := resumedSessionID[0]
		if err := apiLog.ReserveSession(sessionID); err != nil {
			_ = apiLog.Close()
			if errors.Is(err, llm.ErrAPILogTargetLocked) {
				return nil, fmt.Errorf("session %s is already running; send work to the live session or fork it: %w", sessionID, err)
			}
			return nil, fmt.Errorf("reserve canonical API log for resumed session %s: %w", sessionID, err)
		}
	}

	if warnings != nil {
		apiLog.SetFailureObserver(func(failure llm.APILogFailure) {
			_, _ = fmt.Fprintf(warnings, "warning: canonical API log %s failed (session=%s group=%s attempt=%s): %v\n",
				failure.Operation, failure.SessionID, failure.AttemptGroupID, failure.AttemptID, failure.Err)
		})
	}
	client.Use(apiLog)
	return apiLog.Close, nil
}
