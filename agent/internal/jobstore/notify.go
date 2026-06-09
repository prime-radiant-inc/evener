package jobstore

import "github.com/oklog/ulid/v2"

// NewTerminalGeneration mints the stable identity of a job's first terminal
// event. It is minted once at finalize and copied verbatim onto the job's
// pending/delivered notification events (never re-derived from the event seq).
func NewTerminalGeneration() string {
	return ulid.Make().String()
}

// DedupeKey is the durable terminal-notification dedupe identity.
type DedupeKey struct {
	VisibleSessionID string
	JobID            string
	TerminalGen      string
}

// DedupeKey returns the record's terminal-notification dedupe key.
func (r *JobRecord) DedupeKey() DedupeKey {
	return DedupeKey{
		VisibleSessionID: r.VisibleToSession,
		JobID:            r.JobID,
		TerminalGen:      r.TerminalGen,
	}
}

// ShouldDeliver reports whether the record's terminal notification still needs
// to be injected into the visible session.
func ShouldDeliver(r *JobRecord) bool {
	return r.NotifyState == NotifyPending
}
