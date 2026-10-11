package hostops

// Probe-epoch tests (deploy pipeline 08b §6 step 2, §4, §7, §12's "Durable
// probe epoch" and "Fenced probe ordering" rows, as far as this slice ships
// them). The controller-side probe epoch is a fencing-epoch-shaped row — a
// (boot id, per-host op sequence) pair — persisted in its own atomic store
// write before the first `evener/host/running` call, bound to the host's
// current (generation, incarnation id) pair, superseded by the token mint, and
// deleted silently at boot when it is the only thing a crash left behind.
//
// The serving hub's guard-epoch row is the other half: §10 requires the
// serving hub to validate a presented epoch against the guard epoch it last
// admitted and refuse a stale same-boot epoch without persisting it or probing,
// persisting only an admitted epoch before the probe's write half. What the
// withdrawn fencing program once planned on top of both — takeover, bounded
// kill/wait, guard advance, the remote guard file's total order — was removed
// with it (Jesse, 2026-09-29; comp08), so those capabilities are absent by
// decision, not pending here.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// TestProbeEpochPersistsItsOwnAtomicWriteAndBindsThePair pins §6 step 2's
// durable epoch: it lands in its own store write before any probe call, carries
// the boot id and the per-host monotonic op sequence, and is bound to the
// host's current (generation, incarnation id) pair.
func TestProbeEpochPersistsItsOwnAtomicWriteAndBindsThePair(t *testing.T) {
	store, path := openTestStore(t)
	epoch, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "m4", BootID: "boot-1", Generation: 7, IncarnationID: "inc-m4",
	})
	if err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	switch {
	case epoch.Host != "m4":
		t.Fatalf("epoch host = %q", epoch.Host)
	case epoch.BootID != "boot-1":
		t.Fatalf("epoch bootID = %q", epoch.BootID)
	case epoch.OpSeq != 1:
		t.Fatalf("first epoch opSeq = %d, want 1", epoch.OpSeq)
	case epoch.Generation != 7 || epoch.IncarnationID != "inc-m4":
		t.Fatalf("epoch pair = %d/%q, want 7/inc-m4", epoch.Generation, epoch.IncarnationID)
	case epoch.CreatedAt.IsZero():
		t.Fatal("epoch carries no creation timestamp")
	}
	// The row is durable: a fresh load of the file carries it.
	reopened := reopenFresh(t, path)
	got, ok := reopened.ProbeEpoch("m4")
	if !ok {
		t.Fatal("the persisted probe epoch did not survive a reload")
	}
	if got != epoch {
		t.Fatalf("reloaded epoch = %+v, want %+v", got, epoch)
	}
}

// TestProbeEpochSupersedesPerHostAndAdvancesTheSequence pins the row family's
// one-row-per-host rule and the monotonic per-host sequence: a second persist
// replaces the first row and mints a strictly higher op sequence, and the
// sequence never moves backward even when the mint supersedes the row.
func TestProbeEpochSupersedesPerHostAndAdvancesTheSequence(t *testing.T) {
	store, path := openTestStore(t)
	first, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "m4", BootID: "boot-1", Generation: 7, IncarnationID: "inc-a"})
	if err != nil {
		t.Fatalf("first PersistProbeEpoch: %v", err)
	}
	second, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "m4", BootID: "boot-1", Generation: 8, IncarnationID: "inc-b"})
	if err != nil {
		t.Fatalf("second PersistProbeEpoch: %v", err)
	}
	if second.OpSeq != first.OpSeq+1 {
		t.Fatalf("second opSeq = %d, want %d", second.OpSeq, first.OpSeq+1)
	}
	rows := probeEpochRows(t, path)
	if len(rows) != 1 {
		t.Fatalf("the store file carries %d probe-epoch rows, want exactly one per host", len(rows))
	}
	if got := rows[0]["incarnationId"]; got != "inc-b" {
		t.Fatalf("the row carries incarnation %v, want the newest (inc-b)", got)
	}
	// The mint supersedes the row; the sequence keeps climbing for the next plan.
	mustMint(t, store, mintDefaults("m4", time.Now().UTC()))
	if _, ok := store.ProbeEpoch("m4"); ok {
		t.Fatal("the mint left the probe-epoch row behind")
	}
	third, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "m4", BootID: "boot-1", Generation: 8, IncarnationID: "inc-b"})
	if err != nil {
		t.Fatalf("third PersistProbeEpoch: %v", err)
	}
	if third.OpSeq <= second.OpSeq {
		t.Fatalf("opSeq after the mint = %d, want above the superseded %d", third.OpSeq, second.OpSeq)
	}
}

// TestMintSupersedesProbeEpochInTheSameWrite pins §6 step 2's "superseded by
// the eventual token mint": the mint's own atomic write drops the host's
// probe-epoch row, so no epoch without a token survives a completed plan.
func TestMintSupersedesProbeEpochInTheSameWrite(t *testing.T) {
	store, path := openTestStore(t)
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "m4", BootID: "boot-1", Generation: 7, IncarnationID: "inc-m4"}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	mustMint(t, store, mintDefaults("m4", time.Now().UTC()))
	if rows := probeEpochRows(t, path); len(rows) != 0 {
		t.Fatalf("the mint's write left %d probe-epoch rows, want none", len(rows))
	}
	// Another host's epoch is untouched: the supersede is per host.
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "other", BootID: "boot-1", Generation: 3, IncarnationID: "inc-other"}); err != nil {
		t.Fatalf("PersistProbeEpoch(other): %v", err)
	}
	mustMint(t, store, mintDefaults("m4", time.Now().UTC()))
	if _, ok := store.ProbeEpoch("other"); !ok {
		t.Fatal("minting for m4 dropped another host's probe epoch")
	}
}

// TestProbeEpochWriteFailureLeavesNoRowRefusesNothingElse pins §6's
// "a probe-epoch write failure refuses probe-failed with nothing launched":
// the store reports the failure and keeps the state it had.
func TestProbeEpochWriteFailureLeavesNoRowRefusesNothingElse(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := StorePath("/state-write-failure")
	store, err := openFS(fs, path, storeFaults{beforeRename: func() error { return errors.New("disk full") }})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	_, err = store.PersistProbeEpoch(ProbeEpochRequest{Host: "m4", BootID: "boot-1", Generation: 7, IncarnationID: "inc-m4"})
	if err == nil {
		t.Fatal("PersistProbeEpoch succeeded despite the injected write failure")
	}
	if _, ok := store.ProbeEpoch("m4"); ok {
		t.Fatal("a failed probe-epoch write left a row in memory")
	}
	if _, err := fs.Stat(path); err == nil {
		t.Fatal("a failed probe-epoch write put the store file on disk")
	}
}

// TestReapProbeEpochsDeletesSilently pins §7's boot disposition: an epoch-only
// row is deleted, never transitioned to interrupted, and the reap advances no
// sequence and touches no record — so a crash between the persist and the
// probe leaves nothing a later boot revives.
func TestReapProbeEpochsDeletesSilently(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "m4")
	awaiting := createTestRecord(t, store, "other")
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "m4", BootID: "boot-1", Generation: 7, IncarnationID: "incarnation-m4"}); err != nil {
		t.Fatalf("PersistProbeEpoch(m4): %v", err)
	}
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "other", BootID: "boot-1", Generation: 7, IncarnationID: "incarnation-other"}); err != nil {
		t.Fatalf("PersistProbeEpoch(other): %v", err)
	}
	// The interrupted transition runs first, as it does at boot: it moves the
	// in-flight records and leaves both epoch rows alone.
	if moved, err := store.RecoverInterrupted(); err != nil || moved != 2 {
		t.Fatalf("RecoverInterrupted = %d/%v, want 2/nil", moved, err)
	}
	if _, ok := store.ProbeEpoch("m4"); !ok {
		t.Fatal("the interrupted transition touched a probe-epoch row")
	}
	sequence := store.Sequence()
	reaped, err := store.ReapProbeEpochs()
	if err != nil || reaped != 2 {
		t.Fatalf("ReapProbeEpochs = %d/%v, want 2/nil", reaped, err)
	}
	if _, ok := store.ProbeEpoch("m4"); ok {
		t.Fatal("the boot reap left a probe-epoch row behind")
	}
	if got := store.Sequence(); got != sequence {
		t.Fatalf("the probe-epoch reap moved the sequence from %d to %d", sequence, got)
	}
	moved, ok := store.Record(record.ID)
	if !ok || moved.State != StateInterrupted {
		t.Fatalf("record %s = %+v, want the interrupted transition's outcome", record.ID, moved)
	}
	if _, ok := store.Record(awaiting.ID); !ok {
		t.Fatal("the reap dropped an unrelated record")
	}
	// Silent on disk too: a reload carries no epoch rows.
	if rows := probeEpochRows(t, reopenFresh(t, path).Path()); len(rows) != 0 {
		t.Fatalf("the reaped store file still carries %d probe-epoch rows", len(rows))
	}
}

// TestProbeEpochRowsAreSchemaChecked pins the row family against the store's
// refuse-always rule: a hand-written file carrying a malformed epoch row is
// schema-invalid and takes §4's custody-first quarantine, so the malformed row
// is never served — the replacement store carries no probe-epoch rows, and the
// original bytes are renamed aside.
func TestProbeEpochRowsAreSchemaChecked(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := StorePath("/state-schema")
	if err := fs.MkdirAll("/state-schema/hostops", 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	raw := `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],` +
		`"probeEpochs":[{"host":"m4","bootId":"","opSeq":0,"generation":7,"incarnationId":"inc-m4","createdAt":"2026-09-27T12:00:00Z"}]}`
	if err := afero.WriteFile(fs, path, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	store, err := openFS(fs, path, storeFaults{})
	if err != nil {
		t.Fatalf("openFS: %v, want the custody-first quarantine", err)
	}
	if store.Quarantine() == nil {
		t.Fatal("a quarantined store booted without its operator-visible signal")
	}
	if rows := storeSnapshotForTest(store).ProbeEpochs; len(rows) != 0 {
		t.Fatalf("the replacement store serves %d probe-epoch rows, want none", len(rows))
	}
	if _, aside := fsQuarantineArtifact(t, fs, filepath.Dir(path), ".quarantined-"); string(aside) != raw {
		t.Fatalf("the aside file does not hold the malformed file's bytes verbatim:\n%s", aside)
	}
}

// probeEpochRows decodes the probe-epoch rows of the store file at path.
func probeEpochRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	var doc struct {
		ProbeEpochs []map[string]any `json:"probeEpochs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode store %s: %v", path, err)
	}
	return doc.ProbeEpochs
}

// TestGuardEpochAdmitsAndRefusesStaleEpochs pins §10's serving-side semantics
// as far as the shipped machinery honestly allows: a presented epoch is
// validated and an admitted one persisted before the probe's write half; an
// epoch older than the host's current one for the same boot is refused without
// probing or writing; a newer one is admitted; and epochs from different boots
// are not comparable (the guard file that once defined that total order was
// withdrawn with the crash-fencing program, Jesse, 2026-09-29; comp08).
func TestGuardEpochAdmitsAndRefusesStaleEpochs(t *testing.T) {
	store, path := openTestStore(t)
	if _, ok := store.GuardEpoch(); ok {
		t.Fatal("a fresh store reports a guard epoch")
	}
	if err := store.AdmitGuardEpoch(GuardEpoch{BootID: "boot-1", OpSeq: 5}); err != nil {
		t.Fatalf("AdmitGuardEpoch(5): %v", err)
	}
	current, ok := store.GuardEpoch()
	if !ok || current != (GuardEpoch{BootID: "boot-1", OpSeq: 5}) {
		t.Fatalf("stored guard epoch = %+v/%v, want boot-1/5", current, ok)
	}
	// A replay of the current epoch is idempotent, not stale.
	if err := store.AdmitGuardEpoch(GuardEpoch{BootID: "boot-1", OpSeq: 5}); err != nil {
		t.Fatalf("replayed current epoch refused: %v", err)
	}
	// A stale epoch for the same boot is refused, and the durable row is
	// unchanged.
	if err := store.AdmitGuardEpoch(GuardEpoch{BootID: "boot-1", OpSeq: 4}); !errors.Is(err, ErrStaleGuardEpoch) {
		t.Fatalf("stale epoch = %v, want ErrStaleGuardEpoch", err)
	}
	if current, _ := store.GuardEpoch(); current.OpSeq != 5 {
		t.Fatalf("the stale refusal moved the stored epoch to %+v", current)
	}
	// A newer epoch is admitted and persisted.
	if err := store.AdmitGuardEpoch(GuardEpoch{BootID: "boot-1", OpSeq: 6}); err != nil {
		t.Fatalf("AdmitGuardEpoch(6): %v", err)
	}
	reopened := reopenFresh(t, path)
	if current, ok := reopened.GuardEpoch(); !ok || current.OpSeq != 6 {
		t.Fatalf("reloaded guard epoch = %+v/%v, want boot-1/6", current, ok)
	}
	// A different boot id has no defined order against the stored one, so it is
	// admitted; the guard file that once would have defined cross-boot ordering
	// was withdrawn with the crash-fencing program (comp08), so that ordering is
	// absent by decision.
	if err := reopened.AdmitGuardEpoch(GuardEpoch{BootID: "boot-2", OpSeq: 1}); err != nil {
		t.Fatalf("epoch from a fresh boot refused: %v", err)
	}
	// Malformed epochs never reach the store.
	for _, bad := range []GuardEpoch{{BootID: "", OpSeq: 3}, {BootID: "boot-2", OpSeq: 0}, {}} {
		if err := reopened.AdmitGuardEpoch(bad); !errors.Is(err, ErrInvalidGuardEpoch) {
			t.Fatalf("AdmitGuardEpoch(%+v) = %v, want ErrInvalidGuardEpoch", bad, err)
		}
	}
}

// TestProbeEpochPostRenameFailureReturnsTheLandedRow pins the signal the plan's
// refusal path keys on: a persist whose directory sync failed after its rename
// landed returns the durable row (OpSeq non-zero) with a RenameLanded error, so
// the caller can treat the epoch as written instead of leaving an epoch-only
// row behind on the refusal path.
func TestProbeEpochPostRenameFailureReturnsTheLandedRow(t *testing.T) {
	path := StorePath(t.TempDir())
	var syncErr error
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
		if dir != filepath.Dir(path) {
			return nil
		}
		return syncErr
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	// Only the persist below fails behind its rename.
	syncErr = errors.New("directory sync fault")
	epoch, err := store.PersistProbeEpoch(ProbeEpochRequest{Host: "m4", BootID: "boot-1", Generation: 7, IncarnationID: "inc-m4"})
	if err == nil {
		t.Fatal("a persist whose directory sync failed reported success")
	}
	if !RenameLanded(err) {
		t.Fatalf("the post-rename failure is not distinguishable: %v", err)
	}
	if epoch.OpSeq == 0 || epoch.Host != "m4" {
		t.Fatalf("the landed persist returned %+v, want the durable epoch row", epoch)
	}
	// The row is the file's contents: a fresh load carries it.
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	reopened, err := openFS(afero.NewOsFs(), path, storeFaults{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	row, ok := reopened.ProbeEpoch("m4")
	if !ok || row != epoch {
		t.Fatalf("reloaded epoch = %+v/%v, want the landed %+v", row, ok, epoch)
	}
}

// TestGuardEpochPostRenameFailureAdmitsTheLandedRow pins the second landed-write
// signal the hub branches on: a guard-epoch write whose rename landed but whose
// directory sync failed reports a RenameLanded error while the store has
// adopted the admitted epoch, so the serving hub can admit and log instead of
// refusing a host whose durable state already holds the epoch.
func TestGuardEpochPostRenameFailureAdmitsTheLandedRow(t *testing.T) {
	path := StorePath(t.TempDir())
	var syncErr error
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
		if dir != filepath.Dir(path) {
			return nil
		}
		return syncErr
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	syncErr = errors.New("directory sync fault")
	err = store.AdmitGuardEpoch(GuardEpoch{BootID: "boot-1", OpSeq: 5})
	if err == nil {
		t.Fatal("an admission whose directory sync failed reported success")
	}
	if !RenameLanded(err) {
		t.Fatalf("the post-rename failure is not distinguishable: %v", err)
	}
	if stored, ok := store.GuardEpoch(); !ok || stored != (GuardEpoch{BootID: "boot-1", OpSeq: 5}) {
		t.Fatalf("memory did not adopt the landed admission: %+v/%v", stored, ok)
	}
	// The row is the file's contents: a fresh load carries it, and a replay of
	// the same epoch is a no-op rather than a stale refusal.
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	reopened, err := openFS(afero.NewOsFs(), path, storeFaults{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if stored, ok := reopened.GuardEpoch(); !ok || stored.OpSeq != 5 {
		t.Fatalf("reloaded guard epoch = %+v/%v, want the landed one", stored, ok)
	}
	if err := reopened.AdmitGuardEpoch(GuardEpoch{BootID: "boot-1", OpSeq: 5}); err != nil {
		t.Fatalf("replaying the landed epoch: %v", err)
	}
}
