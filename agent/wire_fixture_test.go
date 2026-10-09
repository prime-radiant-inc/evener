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
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	checkGolden(t, path, append(encoded, '\n'), *updateWireFixtures,
		fmt.Sprintf("Regenerate with `go test ./agent -run 'WireFixtures$' -update-wire`, then re-run %s.", readers))
}

// checkGolden pins got to the golden file at path, relative to the agent
// package: update rewrites the file, and otherwise any drift fails with both
// versions and hint, which says how to regenerate.
func checkGolden(t *testing.T, path string, got []byte, update bool, hint string) {
	t.Helper()
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v. %s", path, err, hint)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s drifted.\n got: %s\nwant: %s\n%s", path, got, want, hint)
	}
}
