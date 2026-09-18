package hub

import (
	"bytes"
	"testing"
)

func TestArtifactServiceDispatchPrecedesHubStartup(t *testing.T) {
	called := false
	old := runArtifactService
	runArtifactService = func() error { called = true; return nil }
	t.Cleanup(func() { runArtifactService = old })
	if err := runMain([]string{"artifact-service"}, &bytes.Buffer{}, mainDeps{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("internal service entry was not dispatched")
	}
}
