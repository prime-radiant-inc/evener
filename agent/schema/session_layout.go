package schema

import "path/filepath"

// SessionsSubdir is the directory, under a state directory, where every
// session's flat files live: the transcript and event-log JSONL, the API log,
// and the meta.json snapshot with its lock.
const SessionsSubdir = "sessions"

const (
	// SessionTranscriptSuffix names a session's transcript JSONL.
	SessionTranscriptSuffix = ".transcript.jsonl"
	// SessionMetaSuffix names a session's meta.json snapshot.
	SessionMetaSuffix = ".meta.json"
	// SessionMetaLockSuffix names the cross-process lock beside the meta.json.
	SessionMetaLockSuffix = SessionMetaSuffix + ".lock"
	// SessionAPILogSuffix names a session's provider API log.
	SessionAPILogSuffix = ".api.jsonl"
	// SessionLogSuffix names a session's event log.
	SessionLogSuffix = ".log.jsonl"
)

// SessionArtifactSuffixes lists every flat file a session owns that goes away
// with it. The tombstone (SessionMetaTombstoneSuffix) is excluded: it must
// outlive the removal.
var SessionArtifactSuffixes = []string{
	SessionMetaSuffix,
	SessionMetaLockSuffix,
	SessionTranscriptSuffix,
	SessionAPILogSuffix,
	SessionLogSuffix,
}

// SessionsDir returns <stateDir>/sessions.
func SessionsDir(stateDir string) string {
	return filepath.Join(stateDir, SessionsSubdir)
}

// SessionTranscriptPath returns the session's transcript JSONL path.
func SessionTranscriptPath(stateDir, sessionID string) string {
	return filepath.Join(SessionsDir(stateDir), sessionID+SessionTranscriptSuffix)
}

// SessionMetaPath returns the session's meta.json path.
func SessionMetaPath(stateDir, sessionID string) string {
	return filepath.Join(SessionsDir(stateDir), sessionID+SessionMetaSuffix)
}

// SessionAPILogPath returns the session's API log path.
func SessionAPILogPath(stateDir, sessionID string) string {
	return filepath.Join(SessionsDir(stateDir), sessionID+SessionAPILogSuffix)
}

// SessionLogPath returns the session's event log path.
func SessionLogPath(stateDir, sessionID string) string {
	return filepath.Join(SessionsDir(stateDir), sessionID+SessionLogSuffix)
}
