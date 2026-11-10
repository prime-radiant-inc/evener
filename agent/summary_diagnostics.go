package agent

import (
	"encoding/json"
	"log/slog"

	"primeradiant.com/evener/agent/events"
)

// logSummaryFailure records only the allowlisted diagnostic. Provider errors and
// warning copy can contain request content and must never enter this log.
func (s *Session) logSummaryFailure(data events.EventData) {
	var warning events.WarningData
	switch d := data.(type) {
	case events.WarningData:
		warning = d
	default:
		return
	}
	if warning.Code != events.WarningCodeSummaryFailure || warning.SummaryFailure == nil {
		return
	}
	// JSON keeps optional booleans and prior attempts machine-readable even
	// through the process's default text logger and inherited daemon stderr.
	payload, err := json.Marshal(warning.SummaryFailure)
	if err != nil {
		return
	}
	slog.Warn("Summary failed",
		slog.String("code", events.WarningCodeSummaryFailure),
		slog.String("session_id", s.id),
		slog.String("summary_failure", string(payload)))
}
