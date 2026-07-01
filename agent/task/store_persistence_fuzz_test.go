package task

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// FuzzTaskStorePersistence is the differential that proves the afero filesystem
// seam changes nothing: the same program of store operations, replayed through
// two TaskStores whose ONLY difference is the injected afero.Fs — one an OS
// filesystem sandboxed under a t.TempDir (afero.NewBasePathFs over
// afero.NewOsFs), the other a pure in-memory afero.NewMemMapFs — must produce
// byte-identical persisted JSON and identical error outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to OsFs, so if the MemMapFs path ever diverges from the OsFs path the refactor
// is unsound) and (b) a new in-memory persistence fuzzer that drives
// TaskStore.save/Load with zero real-disk dependency in the mem lane.
//
// The op decoding, dependency/status parameter drawing, and seed programs are
// shared with FuzzTaskStore (same package) via opReader; here the oracle is the
// cross-filesystem differential rather than the structural invariants.
//
// A deterministic, monotonically advancing clock is installed on BOTH stores so
// minted CreatedAt/UpdatedAt/CompletedAt timestamps match: the two stores run
// the identical op program through identical in-memory logic, so stamp() is
// called the same number of times in the same order on each.
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//   - byte-identical persistence: the persisted task file (read back through each
//     store's own fs at the same logical path) is byte-for-byte equal across the
//     two filesystems.
//
// After the full program, both stores are reloaded through a fresh TaskStore on
// the same fs and their View() JSON is required to match, proving Load agrees
// across the two filesystems too.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. No network, no
// subprocess.
func FuzzTaskStorePersistence(f *testing.F) {
	f.Add([]byte{opAppend, 2, 0, opAppend, 1, 1, 1, opUpdate, 1, 1, 2, opAppend, 1, 0})
	f.Add([]byte{opAppend, 3, 0, opUpdate, 1, 2, 0, opUpdate, 2, 1, 0})
	f.Add([]byte{opAppend, 1, 0, opAppend, 1, 1, 1, opUpdateDep, 2, 1, 1, opUpdateDep, 1, 1, 2})
	f.Add([]byte{opAppend, 4, 0, opUpdateDep, 1, 0, 1, opUpdate, 1, 3, 1})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		osStore := newFsStore(afero.NewBasePathFs(afero.NewOsFs(), t.TempDir()))
		memStore := newFsStore(afero.NewMemMapFs())

		r := &opReader{b: program}
		const maxOps = 96
		created := 0 // tasks ever created → upper bound on referable IDs

		for ops := 0; ops < maxOps && r.more(); ops++ {
			op := r.next() % opCount
			switch op {
			case opAppend:
				inputs := r.taskInputs(created)
				_, errOS := osStore.Append(inputs)
				_, errMem := memStore.Append(inputs)
				requireErrParity(t, "Append", errOS, errMem)
				if errOS == nil {
					created += len(inputs)
				}
			case opUpdate, opUpdateDep:
				if created == 0 {
					break
				}
				updates := r.taskUpdates(created, op == opUpdateDep)
				errOS := osStore.Update(updates)
				errMem := memStore.Update(updates)
				requireErrParity(t, "Update", errOS, errMem)
			case opPopulate:
				n := int(r.next()%4) + 1
				tmpls := make([]TaskTemplate, n)
				for i := range tmpls {
					tmpls[i] = TaskTemplate{Title: "t", Prompt: "p"}
				}
				errOS := osStore.PopulateFromTemplates(tmpls, nil)
				errMem := memStore.PopulateFromTemplates(tmpls, nil)
				requireErrParity(t, "PopulateFromTemplates", errOS, errMem)
				if v := memStore.View(); len(v) > created {
					created = len(v)
				}
			}

			requireSamePersistedBytes(t, osStore, memStore)
		}

		// Load must also agree across the two filesystems: reload each persisted
		// file through a fresh store on the same fs and compare the views.
		requireSameReload(t, osStore)
		requireSameReload(t, memStore)
		requireSameView(t, osStore, memStore)
	})
}

// newFsStore builds a TaskStore rooted at "/" (so its path is the fixed logical
// "/tasks/fuzz.json" on either filesystem) with the given fs and a deterministic
// clock. Both lanes get independent clocks with identical behavior, so equal op
// programs mint equal timestamps.
func newFsStore(fs afero.Fs) *TaskStore {
	var tick int64
	return NewTaskStore("/", "fuzz").
		SetFs(fs).
		SetClock(func() time.Time {
			tick++
			return time.Unix(tick, 0).UTC()
		})
}

// requireErrParity fails unless both lanes agree on whether the op errored.
func requireErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSamePersistedBytes reads each store's persisted file through its own fs
// at the shared logical path and asserts the bytes are identical.
func requireSamePersistedBytes(t *testing.T, osStore, memStore *TaskStore) {
	t.Helper()
	osBytes := readStoreFile(t, osStore)
	memBytes := readStoreFile(t, memStore)
	if !bytes.Equal(osBytes, memBytes) {
		t.Fatalf("persisted bytes diverge across filesystems:\n os =%s\n mem=%s", osBytes, memBytes)
	}
}

// readStoreFile reads the persisted task file through the store's own fs.
// A missing file (nothing persisted yet) reads as nil, matching on both lanes.
func readStoreFile(t *testing.T, s *TaskStore) []byte {
	t.Helper()
	data, err := afero.ReadFile(s.fs, s.path)
	if err != nil {
		return nil
	}
	return data
}

// requireSameReload reloads the persisted file through a fresh store on the same
// fs and asserts the reloaded view marshals to the same bytes as the live store,
// proving Load round-trips what save wrote on that filesystem.
func requireSameReload(t *testing.T, s *TaskStore) {
	t.Helper()
	reloaded := NewTaskStore("/", "fuzz").SetFs(s.fs)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if !bytes.Equal(marshalView(t, s), marshalView(t, reloaded)) {
		t.Fatalf("reload view diverges from live view")
	}
}

// requireSameView asserts the two live stores hold the same tasks.
func requireSameView(t *testing.T, osStore, memStore *TaskStore) {
	t.Helper()
	if !bytes.Equal(marshalView(t, osStore), marshalView(t, memStore)) {
		t.Fatalf("live views diverge across filesystems")
	}
}

// marshalView serializes a store's task view for structural comparison.
func marshalView(t *testing.T, s *TaskStore) []byte {
	t.Helper()
	data, err := json.Marshal(s.View())
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	return data
}
