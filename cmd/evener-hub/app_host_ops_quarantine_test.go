package hub

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// quarantineFixtureJSON is a store file whose record set is whole (one open
// orphan-unverified fence and one terminal record, dense ids below the
// allocator's own high-water mark) but whose store-level sequence value sits
// below the stamp the terminal record carries: §4's corrupt-store quarantine
// earns its custody snapshot from exactly this shape.
const quarantineFixtureJSON = `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
	`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
	`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
	`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}]}`

// TestOpenHostOpsStoreQuarantinesAndSignals pins the hub's half of §4's
// corrupt-store boot: the store opens around a quarantine, the health signal is
// logged naming the quarantined file and its custody, and the replacement store
// serves the custody import set (never the corrupt records).
func TestOpenHostOpsStoreQuarantinesAndSignals(t *testing.T) {
	stateRoot := t.TempDir()
	path := hostops.StorePath(stateRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(quarantineFixtureJSON), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}

	var logged strings.Builder
	store, err := openHostOpsStore(stateRoot, &logged, hostops.RetentionPolicy{})
	if err != nil || store == nil {
		t.Fatalf("openHostOpsStore: store=%v err=%v (log: %s)", store, err, logged.String())
	}
	signal := store.Quarantine()
	if signal == nil || signal.QuarantinedFile != path || signal.QuarantineEpoch != 1 {
		t.Fatalf("store.Quarantine() = %+v, want the signal naming %s at epoch 1", signal, path)
	}
	logLine := logged.String()
	if !strings.Contains(logLine, path) || !strings.Contains(logLine, signal.CustodyFile) {
		t.Fatalf("the boot log does not carry the operator-visible health signal:\n%s", logLine)
	}
	if !strings.Contains(logLine, "orphan-unverified") {
		t.Fatalf("the health log does not name the custody import set the replacement serves:\n%s", logLine)
	}
	records := store.Records()
	if len(records) != 2 {
		t.Fatalf("the replacement store serves %d records, want the two custody imports: %+v", len(records), records)
	}
	for _, record := range records {
		if record.State != hostops.StateOrphanUnverified {
			t.Fatalf("the replacement store serves %+v, want only orphan-unverified imports", record)
		}
	}
}

// TestOpenHostOpsStoreReportsEveryOpenFailureWithoutAHandle pins the open's
// contract: a store that cannot be opened — a transient permission or kind
// problem, or a corrupt file whose custody cannot be shown whole — is reported
// with no handle, and the caller decides what to serve. Startup keeps serving
// the hosts and retries (TestHubKeepsServingHostsWhenTheStoreCannotOpen).
func TestOpenHostOpsStoreReportsEveryOpenFailureWithoutAHandle(t *testing.T) {
	cases := map[string]struct {
		prepare func(t *testing.T, path string)
		want    error
	}{
		"a store readable beyond its owner": {
			prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatalf("Chmod: %v", err)
				}
			},
			want: hostops.ErrStoreReadableBeyondOwner,
		},
		"a non-regular store path": {
			prepare: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stateRoot := t.TempDir()
			path := hostops.StorePath(stateRoot)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			tc.prepare(t, path)

			var logged strings.Builder
			store, err := openHostOpsStore(stateRoot, &logged, hostops.RetentionPolicy{})
			if err == nil {
				t.Fatalf("openHostOpsStore opened %s", name)
			}
			if store != nil {
				t.Fatalf("openHostOpsStore returned a handle for %s", name)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("openHostOpsStore = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestOpenHostOpsStoreSetsAsideAStoreWhoseCustodyCannotBeProven pins the open's
// answer to a corrupt file the custody-first quarantine refuses: the file is
// kept byte for byte beside the store, and a fresh, empty store opens at the
// path, so the hub keeps working and rebuilds what it can instead of waiting on
// a file nobody can repair.
func TestOpenHostOpsStoreSetsAsideAStoreWhoseCustodyCannotBeProven(t *testing.T) {
	stateRoot := t.TempDir()
	path := hostops.StorePath(stateRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	truncated := `{"version":1,"sequence":0,"allocatorHighWaterMark":2,"records":[`
	if err := os.WriteFile(path, []byte(truncated), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	var logged strings.Builder
	store, err := openHostOpsStore(stateRoot, &logged, hostops.RetentionPolicy{})
	if err != nil || store == nil {
		t.Fatalf("openHostOpsStore = (%v, %v), want a fresh store", store, err)
	}
	if records := store.Records(); len(records) != 0 {
		t.Fatalf("the fresh store carries records: %+v", records)
	}
	if _, _, imported := store.HostRecords(); imported {
		t.Fatal("the fresh store claims imported host records")
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	var kept []string
	for _, entry := range entries {
		if entry.Name() == "operations.json" {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), entry.Name()))
		if readErr != nil {
			t.Fatalf("ReadFile(%s): %v", entry.Name(), readErr)
		}
		if string(body) == truncated {
			kept = append(kept, entry.Name())
		}
	}
	if len(kept) != 1 {
		t.Fatalf("the corrupt bytes were kept in %d file(s) (%v), want exactly one", len(kept), kept)
	}
}

// TestOpenHostOpsStoreKeepsAHealthyStoreWhenOnlyItsQuarantineArtifactsAreBroken
// pins that a store file that loads is never set aside: an incomplete-custody
// refusal can come from the quarantine's own bookkeeping (here an intent naming
// a custody file that is gone) while the active store is fine, and moving the
// fine store away would lose it and still leave the bookkeeping broken.
func TestOpenHostOpsStoreKeepsAHealthyStoreWhenOnlyItsQuarantineArtifactsAreBroken(t *testing.T) {
	stateRoot := t.TempDir()
	path := hostops.StorePath(stateRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	healthy := `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`
	if err := os.WriteFile(path, []byte(healthy), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	const stamp = "20260101T000000.000000000Z"
	intent := `{"corruptFile":` + strconv.Quote(path) + `,"custodyFile":` + strconv.Quote(path+".custody-"+stamp+".json") +
		`,"asideFile":` + strconv.Quote(path+".quarantined-"+stamp) + `,"quarantinedAt":"2026-01-01T00:00:00Z","quarantineEpoch":1}`
	if err := os.WriteFile(path+".quarantine-intent.json", []byte(intent), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}
	before := dirNames(t, filepath.Dir(path))

	store, err := openHostOpsStore(stateRoot, new(strings.Builder), hostops.RetentionPolicy{})

	if store != nil || !errors.Is(err, hostops.ErrQuarantineIncomplete) {
		t.Fatalf("openHostOpsStore = (%v, %v), want the incomplete-custody refusal and no store", store, err)
	}
	if body, readErr := os.ReadFile(path); readErr != nil || string(body) != healthy {
		t.Fatalf("the healthy store was disturbed: %q, %v", body, readErr)
	}
	if after := dirNames(t, filepath.Dir(path)); !slices.Equal(before, after) {
		t.Fatalf("the open left artifacts: before %v after %v", before, after)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
