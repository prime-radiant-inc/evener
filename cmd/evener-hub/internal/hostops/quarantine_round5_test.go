package hostops

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestQuarantineSignalNamesASurvivingCustodyAfterCleanup pins the round-five
// stale-signal finding's re-scan arm: a cleanup removes only the custody files
// whose quarantine is orphaned, and the operator-visible signal is re-derived
// over the survivors — so it still names the highest-epoch custody that exists,
// never a file the cleanup just deleted.
func TestQuarantineSignalNamesASurvivingCustodyAfterCleanup(t *testing.T) {
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	firstCustodyPath, _ := quarantineArtifact(t, filepath.Dir(path), ".custody-")
	if signal := first.Quarantine(); signal == nil || signal.CustodyFile != firstCustodyPath || signal.QuarantineEpoch != 1 {
		t.Fatalf("the first quarantine's signal = %+v, want %s at epoch 1", signal, firstCustodyPath)
	}

	// A clean store lands at the path, and a stale intent names a second, higher
	// epoch's custody whose rename never landed (no aside).
	writeRawStore(t, path, 0o600, validStoreJSON)
	stamp := quarantineStamp(time.Now().UTC().Add(time.Second))
	boundary := `[{"kind":"local-markerless","platform":"linux","nonce":"n"}]`
	custody := `{"quarantineEpoch":2,"quarantinedFile":` + jsonString(path) + `,` +
		`"custodiedAt":"2026-09-26T00:00:00Z","recordIds":[{"recordId":"00000000000000000009","host":"hB"}],` +
		`"allocatorHighWaterMark":9,` +
		`"fences":[{"recordId":"00000000000000000009","host":"hB","kind":"restart","clientOperationId":"op-hB",` +
		`"generation":3,"incarnationId":"inc-hB","quarantine":false,"boundary":` + boundary + `}],` +
		`"ownership":[{"quarantineRecordId":"00000000000000000009","host":"hB","kind":"restart","clientOperationId":"op-hB",` +
		`"generation":3,"highWaterMark":3,"incarnationId":"inc-hB"}]}`
	secondCustodyPath := quarantineCustodyPath(path, stamp)
	if err := os.WriteFile(secondCustodyPath, []byte(custody), 0o600); err != nil {
		t.Fatalf("WriteFile(custody): %v", err)
	}
	intent := `{"corruptFile":` + jsonString(path) + `,"custodyFile":` + jsonString(secondCustodyPath) +
		`,"asideFile":` + jsonString(quarantineAsidePath(path, stamp)) +
		`,"quarantinedAt":"2026-09-26T00:00:00Z","quarantineEpoch":2}`
	if err := os.WriteFile(quarantineIntentPath(path), []byte(intent), 0o600); err != nil {
		t.Fatalf("WriteFile(intent): %v", err)
	}
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open under the stale intent: %v", err)
	}
	// The stale intent's custody is gone; the aside-backed first quarantine's
	// custody survives and the signal names it, not the deleted file.
	if _, err := os.Stat(secondCustodyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stale intent's custody was not cleared (stat err = %v)", err)
	}
	signal := reopened.Quarantine()
	if signal == nil || signal.CustodyFile != firstCustodyPath || signal.QuarantineEpoch != 1 {
		t.Fatalf("Quarantine() = %+v, want the surviving %s at epoch 1", signal, firstCustodyPath)
	}
	if _, err := os.Stat(signal.CustodyFile); err != nil {
		t.Fatalf("the signal names a custody file that does not exist: %v", err)
	}
	if got := reopened.CursorEpoch().QuarantineEpoch; got != 2 {
		t.Fatalf("the durable epoch = %d, want the stale intent's 2 (it never regresses)", got)
	}
}
