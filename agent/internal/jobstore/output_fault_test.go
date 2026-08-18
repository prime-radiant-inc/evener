package jobstore

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/fuzz/fault"
)

// The OutputStore prune/persist path is a chain of durable filesystem steps —
// write the pending metadata, stage the retained tail in a sibling file, fsync,
// rename it over the output, rename metadata — each
// guarded by an `if err != nil` arm that a MemMapFs (which never fails) leaves at
// 0% coverage. These tests inject a fault filesystem through the afero seam
// added to OutputStore, failing exactly one operation at a time, to drive those
// previously-unreachable write-error arms to execution and prove each returns the
// wrapped injected error rather than panicking or swallowing it.

// failAtPlan builds a fault.Schedule byte plan that lets every operation proceed
// except the k-th, which fails. The plan is k+1 bytes long, so operations past k
// wrap back onto the proceed bytes: exactly one operation faults per run.
func failAtPlan(k int) []byte {
	plan := make([]byte, k+1)
	for i := range plan {
		plan[i] = 1 // proceed
	}
	plan[k] = 0 // fail with fault.ErrInjected
	return plan
}

// faultSweep runs drive against a fault filesystem that fails exactly op k, for
// every k in [0, maxK). setup, when non-nil, seeds the clean base first so its
// operations never fault. It fails the test if any injected fault panics and
// returns the resulting error for each k.
func faultSweep(t *testing.T, maxK int, setup func(base afero.Fs), drive func(fs afero.Fs) error) []error {
	t.Helper()
	errs := make([]error, maxK)
	for k := range maxK {
		base := afero.NewMemMapFs()
		if setup != nil {
			setup(base)
		}
		fs := fault.FS(base, fault.FromBytes(failAtPlan(k)))
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("injected fault at op %d panicked: %v", k, r)
				}
			}()
			errs[k] = drive(fs)
		}()
	}
	return errs
}

// assertArmReached asserts that some run in the sweep returned an error naming
// the given arm, and that it wraps the injected fault (proving the arm both fired
// and propagated the error rather than swallowing it).
func assertArmReached(t *testing.T, errs []error, arm string) {
	t.Helper()
	for _, err := range errs {
		if err == nil || !strings.Contains(err.Error(), arm) {
			continue
		}
		if !errors.Is(err, fault.ErrInjected) {
			t.Fatalf("arm %q returned an error that does not wrap the injected fault: %v", arm, err)
		}
		return
	}
	t.Fatalf("no injected fault reached the %q arm across the sweep", arm)
}

const outputFaultPath = "/out.log"

// TestOutputStoreAppendPathFaultArms drives the Append -> persistMetaLocked ->
// writeOutputMetaFile chain and proves each durable-write arm surfaces the
// injected fault: the data write, the pre-metadata fsync, and the metadata
// file's open/write/fsync/rename.
func TestOutputStoreAppendPathFaultArms(t *testing.T) {
	errs := faultSweep(t, 48, nil, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, outputFaultPath, 0)
		if err != nil {
			return err
		}
		defer o.Close() //nolint:errcheck
		_, err = o.Append([]byte("payload"))
		return err
	})

	assertArmReached(t, errs, "jobstore: append output")               // o.f.Write
	assertArmReached(t, errs, "jobstore: sync output before metadata") // o.f.Sync in persistMetaLocked
	assertArmReached(t, errs, "jobstore: open output metadata")        // writeOutputMetaFile tmp open
	assertArmReached(t, errs, "jobstore: write output metadata")       // writeOutputMetaFile write
	assertArmReached(t, errs, "jobstore: sync output metadata")        // writeOutputMetaFile fsync
	assertArmReached(t, errs, "jobstore: replace output metadata")     // writeOutputMetaFile rename
}

// TestOutputStorePrunePathFaultArms opens an oversized file so pruneLocked must
// reduce it to the retained tail, and proves the stage/sync/replace arms of
// that path surface the injected fault.
func TestOutputStorePrunePathFaultArms(t *testing.T) {
	setup := func(base afero.Fs) {
		if err := afero.WriteFile(base, outputFaultPath, bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
			t.Fatalf("seed oversized output: %v", err)
		}
	}
	errs := faultSweep(t, 56, setup, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, outputFaultPath, 10) // cap 10 < 100 forces a prune
		if err != nil {
			return err
		}
		return o.Close()
	})

	assertArmReached(t, errs, "jobstore: seek output prune tail")            // Seek before reading tail
	assertArmReached(t, errs, "jobstore: read output prune tail")            // ReadFull of the tail
	assertArmReached(t, errs, "jobstore: open output tail rewrite")          // open the staging file
	assertArmReached(t, errs, "jobstore: rewrite output tail")               // write the retained tail into it
	assertArmReached(t, errs, "jobstore: sync output tail rewrite")          // fsync the staged tail
	assertArmReached(t, errs, "jobstore: replace output with retained tail") // rename over the output
	// The close-replaced-output arm has no assertion: the fault seam does not
	// intercept Close, so no injected fault can reach it.
}

// TestOutputStorePruneWritesPendingMetadata proves the pending-metadata write
// pruneLocked performs before it rewrites the file (its crash-recovery journal)
// also propagates injected faults through the seam.
func TestOutputStorePruneWritesPendingMetadataFault(t *testing.T) {
	setup := func(base afero.Fs) {
		if err := afero.WriteFile(base, outputFaultPath, bytes.Repeat([]byte("y"), 100), 0o644); err != nil {
			t.Fatalf("seed oversized output: %v", err)
		}
	}
	errs := faultSweep(t, 56, setup, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, outputFaultPath, 10)
		if err != nil {
			return err
		}
		return o.Close()
	})

	assertArmReached(t, errs, "jobstore: hash output metadata") // SHA256 over the file for metadata
}

// TestOutputStorePruneFaultNeverLosesRetainedTail pins the prune protocol's
// durability contract: no single failed filesystem operation during an
// appending prune may lose retained bytes the store already committed, and the
// store must reopen afterwards. For every fault point the disk must hold one of
// the two coherent histories — the pre-append commit or the post-append commit
// — never a bricked or bald state.
func TestOutputStorePruneFaultNeverLosesRetainedTail(t *testing.T) {
	const capBytes = 10
	committed := []byte("0123456789")
	appended := []byte("ABCDE")
	lifetime := append(append([]byte(nil), committed...), appended...)
	for k := range 64 {
		base := afero.NewMemMapFs()
		seed, err := openOutputFsNoSync(base, outputFaultPath, capBytes)
		if err != nil {
			t.Fatalf("seed output: %v", err)
		}
		if _, err := seed.Append(committed); err != nil {
			t.Fatalf("seed append: %v", err)
		}
		if err := seed.Close(); err != nil {
			t.Fatalf("seed close: %v", err)
		}

		fs := fault.FS(base, fault.FromBytes(failAtPlan(k)))
		appendErr := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("injected fault at op %d panicked: %v", k, r)
				}
			}()
			o, err := openOutputFsNoSync(fs, outputFaultPath, capBytes)
			if err != nil {
				return err
			}
			defer o.Close() //nolint:errcheck
			_, err = o.Append(appended)
			return err
		}()

		// The faulted run is over; reopen on the clean base, as after a crash.
		o, err := openOutputFsNoSync(base, outputFaultPath, capBytes)
		if err != nil {
			t.Fatalf("fault at op %d bricked reopen (append err %v): %v", k, appendErr, err)
		}
		tail, total, _, err := o.Tail(1024)
		if err != nil {
			t.Fatalf("fault at op %d: tail after reopen: %v", k, err)
		}
		if err := o.Close(); err != nil {
			t.Fatalf("fault at op %d: close after reopen: %v", k, err)
		}
		wantTail := lifetime[max(0, total-capBytes):total:total]
		if total != int64(len(committed)) && total != int64(len(lifetime)) {
			t.Fatalf("fault at op %d (append err %v): reopened total = %d, want %d or %d",
				k, appendErr, total, len(committed), len(lifetime))
		}
		if appendErr == nil && total != int64(len(lifetime)) {
			t.Fatalf("fault at op %d: append reported success but reopened total = %d, want %d",
				k, total, len(lifetime))
		}
		if !bytes.Equal(tail, wantTail) {
			t.Fatalf("fault at op %d (append err %v): reopened tail = %q, want %q",
				k, appendErr, tail, wantTail)
		}
	}
}

// TestOutputStoreFaultsNeverSwallowed sweeps both the append and prune paths and
// asserts the global invariant: every operation that faults surfaces an error
// wrapping the injected fault. A swallowed fault (error dropped, nil returned
// while state advanced) or a panic would fail here. faultSweep already fails on
// any panic; this pins the no-swallow half.
func TestOutputStoreFaultsNeverSwallowed(t *testing.T) {
	appendErrs := faultSweep(t, 48, nil, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, outputFaultPath, 0)
		if err != nil {
			return err
		}
		defer o.Close() //nolint:errcheck
		_, err = o.Append([]byte("payload"))
		return err
	})
	assertEveryErrorIsInjected(t, "append", appendErrs)

	setup := func(base afero.Fs) {
		if err := afero.WriteFile(base, outputFaultPath, bytes.Repeat([]byte("z"), 100), 0o644); err != nil {
			t.Fatalf("seed oversized output: %v", err)
		}
	}
	pruneErrs := faultSweep(t, 56, setup, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, outputFaultPath, 10)
		if err != nil {
			return err
		}
		return o.Close()
	})
	assertEveryErrorIsInjected(t, "prune", pruneErrs)
}

func assertEveryErrorIsInjected(t *testing.T, label string, errs []error) {
	t.Helper()
	sawFault := false
	for k, err := range errs {
		if err == nil {
			continue
		}
		sawFault = true
		if !errors.Is(err, fault.ErrInjected) {
			t.Fatalf("%s fault at op %d returned an error not wrapping the injected fault: %v", label, k, err)
		}
	}
	if !sawFault {
		t.Fatalf("%s sweep injected no faults; the seam is not being exercised", label)
	}
}
