package agent

import (
	"os"

	"primeradiant.com/serf/agent/internal/artifactstore"
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
