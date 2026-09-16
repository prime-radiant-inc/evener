//go:build linux || darwin

package hub

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

func TestThreadLifecycleLoggingFreshSpawnFinalIdentity(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Keep the executable alive on an external FIFO instead of a guessed sleep
	// while the real spawner awaits its rendezvous file. Only fixture paths and
	// the child PID returned by this launch are touched.
	gate := filepath.Join(dir, "gate")
	if err := unix.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	id := hubtest.SessionID(t)
	bin := filepath.Join(dir, "fake-evener")
	writeFakeEvener(t, bin, fmt.Sprintf("#!/bin/sh\nprintf '{\"pid\":%%s,\"session_id\":%q,\"started_at\":\"2999-01-01T00:00:00Z\"}\\n' \"$$\" > '%s/'\"$$\"'.json'\nexec /bin/cat '%s'\n", id, runDir, gate))
	var output bytes.Buffer
	entry, err := spawnDaemon(t.Context(), bin, runDir, hubcore.SpawnRequest{}, 0, &output)
	if err != nil {
		t.Fatal(err)
	}
	reapFakeDaemon(t, entry.PID)
	if entry.SessionID != id {
		t.Fatalf("fixture rendezvous session=%s, want %s", entry.SessionID, id)
	}
	records := assertThreadLifecycleRecords(t, output.String())
	assertThreadLifecycleOutcome(t, records, "daemon", "success", "none")
	final := records[len(records)-1]
	if final["stage"] != "daemon" || final["session_id"] != entry.SessionID {
		t.Fatalf("final spawn record lost discovered session identity: %#v", final)
	}
}
