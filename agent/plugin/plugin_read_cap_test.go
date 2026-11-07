package plugin

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// TestLoadRefusesAManifestOverTheWholeFileCap pins the cap the shared reader
// brings to every plugin load: a manifest past the cap fails the load instead
// of being pulled into memory whole.
func TestLoadRefusesAManifestOverTheWholeFileCap(t *testing.T) {
	dir := makePluginDir(t, "oversize-manifest")
	orig := execenv.MaxWholeFileReadBytes
	execenv.MaxWholeFileReadBytes = 8
	t.Cleanup(func() { execenv.MaxWholeFileReadBytes = orig })

	if _, err := Load(dir); err == nil {
		t.Fatal("a manifest over the whole-file cap loaded")
	}
}
