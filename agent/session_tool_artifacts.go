package agent

import (
	"os"
	"strings"

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
		res.Output += "\n[retention_failed: full output could not be retained: " + conciseError(err) + "]"
		return ""
	}
	res.Output += "\nFull output: " + ref +
		"\nRead with: read_transcript(transcript_ref=\"" + ref + "\")"
	return ref
}

func conciseError(err error) string {
	text := strings.TrimSpace(err.Error())
	if end := strings.IndexAny(text, "\r\n"); end >= 0 {
		text = strings.TrimSpace(text[:end])
	}
	if text == "" {
		return "unknown error"
	}
	const maxRunes = 160
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-3]) + "..."
	}
	return text
}
