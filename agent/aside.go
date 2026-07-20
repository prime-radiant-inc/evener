package agent

import (
	"fmt"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
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
	return asideSessionFS(afero.NewOsFs(), stateDir, parentID)
}

func asideSessionFS(fs afero.Fs, stateDir, parentID string) (string, error) {
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
	return writeForkChild(fs, stateDir, parentID, parentHeader, parentMeta, allEntries, len(allEntries)+1, nil, deps)
}
