package agent

import (
	"os"

	"primeradiant.com/serf/agent/internal/artifactstore"
	"primeradiant.com/serf/agent/internal/tool"
)

type artifactStore interface {
	Put([]byte) (string, error)
	Open(string) (*os.File, error)
	Close() error
}

var _ artifactStore = (*artifactstore.Store)(nil)

var sessionArtifactStoreFactory = func() (artifactStore, error) {
	return artifactstore.New("")
}

func newSessionArtifactStore() (artifactStore, error) {
	return sessionArtifactStoreFactory()
}

func (s *Session) retainToolArtifact(res *tool.ExecResult) string {
	if !res.Truncated {
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
