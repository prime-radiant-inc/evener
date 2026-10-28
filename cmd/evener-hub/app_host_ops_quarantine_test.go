package hub

import (
	"errors"
	"os"
	"path/filepath"
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

// TestOpenHostOpsStoreReportsAnIncompleteCustody pins the open's other
// refusal: a corrupt file whose custody cannot be shown complete is reported
// with the store-corrupt class, and nothing is renamed or rewritten.
func TestOpenHostOpsStoreReportsAnIncompleteCustody(t *testing.T) {
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
	if store != nil {
		t.Fatalf("openHostOpsStore returned a store for an unprovable custody snapshot")
	}
	if !errors.Is(err, hostops.ErrStoreCorrupt) {
		t.Fatalf("openHostOpsStore = %v, want an ErrStoreCorrupt refusal", err)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile(%s): %v", path, readErr)
	}
	if string(body) != truncated {
		t.Fatalf("the refused boot rewrote the corrupt file:\n%s", body)
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "operations.json" {
		t.Fatalf("the refused boot left quarantine artifacts behind: %v", entries)
	}
}
