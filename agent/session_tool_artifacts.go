package agent

import (
	"errors"
	"fmt"
	"os"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/artifactstore"
	"primeradiant.com/evener/agent/internal/tool"
)

type artifactStore interface {
	Put([]byte) (string, error)
	Open(string) (*os.File, error)
	Close() error
}

var _ artifactStore = (*artifactstore.Store)(nil)

// sessionArtifactStoreBasesFn lists the bases tried in order for the store that
// retains full tool output. The process temp dir comes first and the user cache
// dir second, the same walk session scratch makes (sandbox.sessionScratchBase):
// a temp dir that outlived its directory — an inherited TMPDIR pointing at a
// deleted session container — must not cost the session its retention for its
// whole life.
var sessionArtifactStoreBasesFn = func() []string {
	bases := []string{""}
	if cache, err := os.UserCacheDir(); err == nil {
		if info, statErr := os.Stat(cache); statErr == nil && info.IsDir() {
			bases = append(bases, cache)
		}
	}
	return bases
}

var sessionArtifactStoreFactory = func() (artifactStore, error) {
	var failures []error
	for _, base := range sessionArtifactStoreBasesFn() {
		store, err := artifactstore.New(base)
		if err == nil {
			return store, nil
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(failures...)
}

func newSessionArtifactStore() (artifactStore, error) {
	return sessionArtifactStoreFactory()
}

// resolveArtifactStore resolves the store a session retains full tool output
// in, reporting whether the session owns what it returns. Full-output retention
// is a convenience — retainToolArtifact already degrades to retention_failed
// when a Put fails — so a store that cannot be created leaves the session
// running without one, with the cause returned for its session-start warning.
// The live cause is a TMPDIR that outlived the directory it named.
func resolveArtifactStore(inherited artifactStore) (store artifactStore, owns bool, err error) {
	if inherited != nil {
		return inherited, false, nil
	}
	created, err := newSessionArtifactStore()
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// bufferArtifactStoreWarning queues the session-start notice for a store that
// could not be created. Like every other construction-time diagnostic it waits
// for SESSION_START, the stream's promised first event.
func (s *Session) bufferArtifactStoreWarning(err error) {
	if err == nil {
		return
	}
	s.pendingArtifactStoreWarnings = append(s.pendingArtifactStoreWarnings, events.WarningData{
		Message: fmt.Sprintf("full tool output will not be retained: %v", err),
	})
}

func (s *Session) retainToolArtifact(res *tool.ExecResult) string {
	if !res.Truncated {
		return ""
	}
	if s.artifactStore == nil {
		res.Output += "\n[retention_failed: full output could not be retained]"
		return ""
	}
	ref, err := s.artifactStore.Put([]byte(res.RecoverableOutput))
	if err != nil {
		res.Output += "\n[retention_failed: full output could not be retained]"
		return ""
	}
	res.Output += "\nFull output: " + ref +
		"\nRead with: read_transcript(transcript_ref=\"" + ref + "\")"
	return ref
}
