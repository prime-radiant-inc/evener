package repoaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scan must never prune the directory it starts at. A checkout whose own
// directory name begins with a dot is ordinary — a hidden clone, a CI workspace
// — and skipping it would leave the scan visiting nothing, with the
// proof-of-reaching check in mcpTransportBuiltInOnePackage as the only thing
// that notices.
func TestTransportConstructionScanDoesNotPruneItsRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), ".evener")
	fixture := filepath.Join(root, "agent", "mcpprobe")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	source := "package mcpprobe\n\nimport (\n\tmcpsdk \"" + sdkImportPath + "\"\n)\n\n" +
		"var _ = mcpsdk.SSEClientTransport{}\n"
	if err := os.WriteFile(filepath.Join(fixture, "transport.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, _, err := transportConstructionSites(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || !strings.Contains(sites[0], "SSEClientTransport") {
		t.Fatalf("a dot-named root's production sources were not scanned: sites = %v", sites)
	}
}
