package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// AsideSession creates a new session branched from a parent session at its
// tip: the child's transcript is a complete copy of the parent's, with no
// divergent edit. The child inherits the parent's profile, model, and config
// (sandbox mode, tool allowances — everything persisted in the session meta),
// records the parent in its lineage, and gets its own fresh session ID, so it
// runs as a side thread of the main session. The parent's transcript and meta
// are left untouched.
//
// Use case: asking a distracting question about the main session without
// derailing it — the user asks in the aside thread while the parent continues.
func AsideSession(stateDir, parentID string) (string, error) {
	return asideSessionFSWithConfig(afero.NewOsFs(), stateDir, parentID, nil)
}

func asideSessionFS(fs afero.Fs, stateDir, parentID string) (string, error) {
	return asideSessionFSWithConfig(fs, stateDir, parentID, nil)
}

// AsideSessionWithConfig creates a full-tip child with config already selected
// for that child. The final snapshot is supplied to the child writer before its
// transcript and metadata are published, so a restart cannot observe a stale
// source selection.
func AsideSessionWithConfig(stateDir, parentID string, config schema.ConfigSnapshot) (string, error) {
	return asideSessionFSWithConfig(afero.NewOsFs(), stateDir, parentID, &config)
}

func asideSessionFSWithConfig(fs afero.Fs, stateDir, parentID string, config *schema.ConfigSnapshot) (string, error) {
	parentHeader, allEntries, err := readForkParent(fs, stateDir, parentID, 10*1024*1024)
	if err != nil {
		return "", err
	}

	// Load parent meta — required for copying fields to the child.
	parentMeta, err := schema.LoadSessionMetaWithFS(fs, stateDir, parentID)
	if err != nil {
		return "", fmt.Errorf("load parent session meta: %w", err)
	}

	deps := forkSessionDeps{
		newWriter: func(fs afero.Fs, path string, header transcript.Header) (forkTranscriptWriter, error) {
			return transcript.NewWriterWithFS(fs, path, header)
		},
		saveMeta:     schema.SaveSessionMetaWithFS,
		maxScanToken: 10 * 1024 * 1024,
	}
	// The child shares the parent's full transcript, so the first turn unique
	// to this branch is one past the parent's tip.
	return writeForkChildWithConfig(fs, stateDir, parentID, parentHeader, parentMeta, allEntries, len(allEntries)+1, nil, config, deps)
}

// RemoveSessionArtifacts removes the artifacts belonging to a newly-created
// session. The ID is validated before it is joined to any path; callers use this
// only to roll back a child that has not completed launch.
func RemoveSessionArtifacts(stateDir, sessionID string) error {
	if stateDir == "" {
		return fmt.Errorf("state directory is empty")
	}
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return err
	}
	base := filepath.Join(stateDir, "sessions")
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".api.jsonl", ".log.jsonl"} {
		if err := os.Remove(filepath.Join(base, sessionID+suffix)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(base, sessionID)); err != nil {
		return err
	}
	return nil
}
