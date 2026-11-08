package schema

import (
	"os"
	"path/filepath"
	"strings"
)

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

// sessionDelegateJournalName names the per-session delegates journal, the
// lifecycle authority every reader of a session's delegate subtree folds.
const sessionDelegateJournalName = "delegates.jsonl"

// SessionArtifactSuffixes lists every flat file a session owns that goes away
// with it. The tombstone (SessionMetaTombstoneSuffix) is excluded: it must
// outlive the removal.
var SessionArtifactSuffixes = []string{
	SessionMetaSuffix,
	SessionMetaLockSuffix,
	SessionTranscriptSuffix,
	SessionAPILogSuffix,
	SessionLogSuffix,
	SessionLivelockSuffix,
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

// SessionDelegateJournalPath returns the session's delegates journal path:
// <stateDir>/sessions/<sessionID>/delegates.jsonl.
func SessionDelegateJournalPath(stateDir, sessionID string) string {
	return filepath.Join(SessionWorkDir(stateDir, sessionID), sessionDelegateJournalName)
}

// SessionWorkDir returns the session's own directory under sessions/, where its
// jobs, delegates journal, and worktrees live: <stateDir>/sessions/<sessionID>.
// An unpersisted session (no durable state directory) keeps that directory under
// the evener-owned temp base instead, so its files never resolve against the
// process working directory.
func SessionWorkDir(stateDir, sessionID string) string {
	if strings.TrimSpace(stateDir) == "" {
		return filepath.Join(os.TempDir(), "evener-jobs", sessionID)
	}
	return filepath.Join(SessionsDir(stateDir), sessionID)
}
