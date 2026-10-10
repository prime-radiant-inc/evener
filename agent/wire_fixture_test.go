package agent

// The agent's wire corpora (agent/testdata/*wire) record what the daemon's
// real producers put on the wire, projected the way history reaches clients,
// so the clients' tests read shapes the daemon actually sends. Each corpus
// test encodes its items and hands them to checkWireFixture, which pins them
// against the committed file.
//
// Regenerate every corpus after an intentional change with `make
// fuzz-goldens`, or directly:
//
//	go test ./agent -run 'WireFixtures$' -update-wire

import (
	"encoding/json"
	"flag"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

var updateWireFixtures = flag.Bool("update-wire", false,
	"rewrite the agent/testdata/*wire corpora from the current producers")

// wireFixtureStart is the instant every corpus's turns and runs start from,
// so the recorded times never depend on the clock.
var wireFixtureStart = time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)

// checkWireFixture encodes value as a corpus and pins it to path, relative to
// the agent package: -update-wire rewrites the file, and otherwise any drift
// fails with both versions. readers names the tests to re-run after a
// regeneration.
func checkWireFixture(t *testing.T, path string, value any, readers string) {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	agenttest.CheckGolden(t, path, append(encoded, '\n'), *updateWireFixtures,
		fmt.Sprintf("Regenerate with `go test ./agent -run 'WireFixtures$' -update-wire`, then re-run %s.", readers))
}
