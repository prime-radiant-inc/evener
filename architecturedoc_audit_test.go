package evener_test

import (
	"os"
	"strings"
	"testing"
)

// architectureDocPath is the canonical, living layout reference the audit below
// reads. It is hand-written (not generated), so nothing else in the suite would
// notice it drifting back to the old control-path description (issue #2408,
// audit WIRE-06).
const architectureDocPath = "docs/architecture.md"

// TestArchitectureDocTUITalksAppWireOverHTTP pins the canonical layout
// reference's description of the TUI's control connection. The binaries table
// once named hubapi (HTTP) as the TUI-to-hub contract, but the actual control
// path is AppWire over WebSocket: the TUI dials the hub's /rpc socket and drives
// it through an appwire.Client (hub_start.go's dialHubRPC), while hubapi is only
// the best-effort environment health probe. A reader who trusts the old row
// misplaces reconnect, mutation-recovery, and ordered-event responsibilities, so
// the table is pinned here rather than left to drift back.
func TestArchitectureDocTUITalksAppWireOverHTTP(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(architectureDocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", architectureDocPath, err)
	}
	doc := string(raw)

	const tuiRow = "| `evener tui` |"
	row := ""
	for line := range strings.SplitSeq(doc, "\n") {
		if strings.HasPrefix(line, tuiRow) {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("%s has no %q row — the binaries table this audit guards was "+
			"renamed or removed", architectureDocPath, tuiRow)
	}
	if !strings.Contains(row, "AppWire") {
		t.Errorf("the `evener tui` table row must name AppWire as the TUI-to-hub "+
			"contract; got %q", row)
	}
	if strings.Contains(row, "hubapi (HTTP)") {
		t.Errorf("the `evener tui` table row still names hubapi (HTTP) as the "+
			"control path; got %q", row)
	}
	// hubapi must stay documented — it is still the hub's HTTP surface — so
	// deleting the mention instead of correcting it cannot pass as fixed.
	if !strings.Contains(doc, "hubapi") {
		t.Errorf("%s no longer mentions hubapi at all — the hub's HTTP surface "+
			"(health, navigation, refs, attention) still exists", architectureDocPath)
	}
}

// TestTUIHubControlDialsAppWire anchors the document's claim to the code it
// describes, so the audit above cannot stay green while the TUI quietly changes
// transports. dialHubRPC is the TUI's control connection and must dial AppWire;
// checkHubEnvironment is the one hubapi (HTTP) call the TUI makes and must stay
// the health probe alone.
func TestTUIHubControlDialsAppWire(t *testing.T) {
	t.Parallel()
	const source = "cmd/evener-tui/internal/hubstart/hub_start.go"
	dial := goFunctionBody(t, source, "dialHubRPC")
	if !strings.Contains(dial, "appwire.DialWebSocket") {
		t.Errorf("%s's dialHubRPC no longer dials AppWire (appwire.DialWebSocket) — "+
			"the architecture table's AppWire claim would be stale", source)
	}
	if !strings.Contains(dial, "appwire.NewClient") {
		t.Errorf("%s's dialHubRPC no longer builds an appwire.Client — the TUI "+
			"control path is not AppWire", source)
	}
	env := goFunctionBody(t, source, "checkHubEnvironment")
	if !strings.Contains(env, "hubapi.NewClient") {
		t.Errorf("%s's checkHubEnvironment no longer uses hubapi.NewClient — the "+
			"architecture table's description of the HTTP health probe is stale", source)
	}
}

// goFunctionBody returns the text of a top-level Go function, from its
// declaration line to the line before the next top-level func. It is enough for
// this file's simple declaration shapes and fails loudly if the declaration is
// missing.
func goFunctionBody(t *testing.T, path, name string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "func "+name+"(") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s declares no func %s — the code this audit anchors to moved", path, name)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "func ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
