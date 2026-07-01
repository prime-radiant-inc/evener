package jobstore

import (
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/serf/fuzz/fault"
)

// FuzzStoreFaultTolerance drives Store.Append/AppendBatch/reopen while a
// fuzzer-driven fault.Schedule injects filesystem errors (failed writes, reads,
// stats, renames) into the store's afero seam. It proves what the afero
// differential (FuzzStorePersistence) structurally cannot, because MemMapFs never
// fails:
//
//  1. no injected I/O error makes the store panic; and
//  2. transient faults leave the log RECOVERABLE — after the program, reopening
//     the persisted bytes on the same, now fault-free, filesystem must
//     LoadOrdered cleanly. A failed append that left the tail torn beyond the
//     store's own trailing-line recovery, or a rollback that corrupted state,
//     would trip this.
//
// This drives the rollbackAppendLocked and trailing-recovery branches under
// adversarial failure timing — error paths the unit tests and the mem-only
// differential leave at 0% fuzz. It is the first consumer of fuzz/fault: the
// whole fault rig is the single fault.FS wrap below.
//
// SAFETY: pure in-memory (MemMapFs) under a fault wrapper; no disk, network, or
// subprocess.
func FuzzStoreFaultTolerance(f *testing.F) {
	// Four passes clear the initial openFs (which consumes 4 fs ops), then the
	// fails land on the Append writes — deterministically driving the
	// write-error rollback path (rollbackAppendLocked), the whole point.
	f.Add([]byte{0x01, 0x01, 0x01, 0x01, 0x00, 0x00}, []byte{opAppend, 0, 0, 0, opAppend, 1, 1, 1})
	f.Add([]byte{0x00, 0x00}, []byte{opAppend, 0, opAppend, 1, opAppend, 2})
	f.Add([]byte{0x04, 0x01}, []byte{opBatch, 3, 0, opReopen, opAppend, 1})
	f.Add([]byte{0x08}, []byte{opAppend, 0, opReopen, opAppend, 1, opReopen})
	f.Add([]byte{}, []byte{opAppend, 0})

	f.Fuzz(func(t *testing.T, faultPlan, program []byte) {
		// Bound the input so the fuzzer spends its budget on failure-timing
		// interleavings, not on pathologically long op programs (each opReopen
		// re-reads and folds the whole log, so a huge program is O(ops x log)).
		if len(program) > 512 || len(faultPlan) > 128 {
			return
		}
		base := afero.NewMemMapFs()
		fs := fault.FS(base, fault.FromBytes(faultPlan))
		const path = "/jobs.jsonl"

		s, err := openFs(fs, path)
		if err != nil {
			return // the initial open faulted; nothing persisted to check
		}

		r := &progReader{b: program}
		ec := 0
		for ops := 0; ops < 128 && r.more(); ops++ {
			switch r.next() % 3 {
			case 0:
				_ = s.Append(drawEvent(r, &ec)) // may fault; must not panic
			case 1:
				n := int(r.next()%3) + 1
				batch := make([]Event, n)
				for i := range batch {
					batch[i] = drawEvent(r, &ec)
				}
				_ = s.AppendBatch(batch) // may fault; must not panic
			case 2:
				_ = s.Close()
				if s2, oerr := openFs(fs, path); oerr == nil {
					s = s2
				} else if s2, oerr := openFs(base, path); oerr == nil {
					s = s2 // reopen faulted; fall back to the clean fs to keep driving
				} else {
					return
				}
			}
		}
		_ = s.Close()

		// Recoverability oracle: the persisted bytes, reopened on the fault-free
		// base fs, must load cleanly. A transient write fault that left the log
		// unrecoverable — or a rollback that corrupted committed state — trips here.
		clean, err := openFs(base, path)
		if err != nil {
			t.Fatalf("log unrecoverable after transient faults: reopen: %v", err)
		}
		defer clean.Close() //nolint:errcheck
		if _, err := clean.LoadOrdered(); err != nil {
			t.Fatalf("log unrecoverable after transient faults: LoadOrdered: %v", err)
		}
	})
}
