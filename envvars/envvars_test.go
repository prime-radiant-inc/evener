package envvars

import "testing"

func TestSERFScratchDirIsRegisteredInternalVariable(t *testing.T) {
	if SERFScratchDir.Name != "SERF_SCRATCH_DIR" {
		t.Fatalf("SERFScratchDir.Name = %q", SERFScratchDir.Name)
	}
	if SERFScratchDir.Visibility != Internal {
		t.Fatalf("SERFScratchDir.Visibility = %q, want %q", SERFScratchDir.Visibility, Internal)
	}
	if got, ok := Find(SERFScratchDir.Name); !ok || got != SERFScratchDir {
		t.Fatalf("Find(%q) = %+v, %v", SERFScratchDir.Name, got, ok)
	}
}
