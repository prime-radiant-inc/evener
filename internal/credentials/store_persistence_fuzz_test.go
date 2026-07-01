package credentials

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/spf13/afero"
)

// FuzzCredentialsStorePersistence is the differential that proves the afero
// filesystem seam changes nothing: the same program of Set/Clear/reload
// operations, replayed through two Stores whose ONLY difference is the injected
// afero.Fs — one an OS filesystem sandboxed under a t.TempDir
// (afero.NewBasePathFs over afero.NewOsFs), the other a pure in-memory
// afero.NewMemMapFs — must produce byte-identical persisted TOML and identical
// error outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to afero.NewOsFs, whose methods forward straight to the os package, so if the
// MemMapFs path ever diverges from the OsFs path the refactor is unsound) and
// (b) a new in-memory persistence fuzzer that drives Store.save/loadStoreFS with
// zero real-disk dependency in the mem lane.
//
// Byte parity is meaningful because BurntSushi/toml's encoder sorts map keys, so
// two independent Stores holding equal provider maps encode to identical bytes.
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//   - byte-identical persistence: the persisted credentials.toml (read back
//     through each lane's own fs at the same logical path) is byte-for-byte equal
//     across the two filesystems.
//
// After the full program, both lanes are reloaded through a fresh loadStoreFS on
// the same fs and their decoded data is required to match, proving loadStoreFS
// agrees across the two filesystems too.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. No network, no
// subprocess. No clock is involved (the store mints no timestamps).
func FuzzCredentialsStorePersistence(f *testing.F) {
	f.Add([]byte{opCredSet, 0, 3, opCredSet, 1, 4, opCredClear, 0, 0, opCredReload, 0, 0})
	f.Add([]byte{opCredSet, 2, 0, opCredSet, 2, 5, opCredReload, 0, 0, opCredClear, 2, 0})
	f.Add([]byte{opCredSet, 4, 1, opCredSet, 3, 2, opCredSet, 1, 0, opCredReload, 0, 0})
	f.Add([]byte{opCredClear, 0, 0})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		const path = "/creds/credentials.toml"
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		osStore := mustLoadFS(t, osFs, path)
		memStore := mustLoadFS(t, memFs, path)

		r := &credOpReader{b: program}
		const maxOps = 96

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opCredCount {
			case opCredSet:
				provider := credProviders[int(r.next())%len(credProviders)]
				value := credValues[int(r.next())%len(credValues)]
				errOS := osStore.Set(provider, value)
				errMem := memStore.Set(provider, value)
				requireCredErrParity(t, "Set", errOS, errMem)
			case opCredClear:
				provider := credProviders[int(r.next())%len(credProviders)]
				_ = r.next()
				errOS := osStore.Clear(provider)
				errMem := memStore.Clear(provider)
				requireCredErrParity(t, "Clear", errOS, errMem)
			case opCredReload:
				_ = r.next()
				_ = r.next()
				// Reload each lane from its own fs through the construction seam.
				// The persisted bytes were equal after the prior op, so the reload
				// must succeed (or fail) identically and decode equal data.
				os2, errOS := loadStoreFS(osFs, path)
				mem2, errMem := loadStoreFS(memFs, path)
				requireCredErrParity(t, "Reload", errOS, errMem)
				if errOS == nil {
					osStore, memStore = os2, mem2
				}
			}

			requireSameCredBytes(t, osFs, memFs, path)
		}

		// loadStoreFS must also agree across the two filesystems: reload each
		// persisted file and compare the decoded data.
		requireSameCredReload(t, osFs, memFs, path)
	})
}

// Operation opcodes for the fuzzer's byte program.
const (
	opCredSet = iota
	opCredClear
	opCredReload
	opCredCount
)

// credProviders is a small pool so the program reuses names (collisions +
// overwrites + clears of the same key exercise map churn on both lanes).
var credProviders = []string{"openai", "anthropic", "openrouter", "kimi", "work"}

// credValues includes an empty and a whitespace-only value: Set trims, so both
// persist as an empty api_key, and both lanes must agree on that.
var credValues = []string{"sk-1", "sk-two", "", "  ", "k3", "value-with-spaces inside"}

// credOpReader draws bytes from the fuzzed program, returning 0 once exhausted.
type credOpReader struct {
	b []byte
	i int
}

func (r *credOpReader) more() bool { return r.i < len(r.b) }

func (r *credOpReader) next() byte {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

// mustLoadFS builds a Store over fs, failing the test on error.
func mustLoadFS(t *testing.T, fs afero.Fs, path string) *Store {
	t.Helper()
	s, err := loadStoreFS(fs, path)
	if err != nil {
		t.Fatalf("loadStoreFS: %v", err)
	}
	return s
}

// requireCredErrParity fails unless both lanes agree on whether the op errored.
func requireCredErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSameCredBytes reads each lane's persisted file through its own fs at the
// shared logical path and asserts the bytes are identical. A missing file
// (nothing persisted yet) reads as nil on both lanes.
func requireSameCredBytes(t *testing.T, osFs, memFs afero.Fs, path string) {
	t.Helper()
	osBytes := readCredFile(osFs, path)
	memBytes := readCredFile(memFs, path)
	if !bytes.Equal(osBytes, memBytes) {
		t.Fatalf("persisted bytes diverge across filesystems:\n os =%q\n mem=%q", osBytes, memBytes)
	}
}

// readCredFile reads path through fs, returning nil on any error (e.g. missing).
func readCredFile(fs afero.Fs, path string) []byte {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil
	}
	return data
}

// requireSameCredReload reloads both lanes' persisted files and asserts the
// decoded store data is deeply equal, proving loadStoreFS round-trips identically
// across the two filesystems.
func requireSameCredReload(t *testing.T, osFs, memFs afero.Fs, path string) {
	t.Helper()
	os2, errOS := loadStoreFS(osFs, path)
	mem2, errMem := loadStoreFS(memFs, path)
	requireCredErrParity(t, "final reload", errOS, errMem)
	if errOS != nil {
		return
	}
	if !reflect.DeepEqual(os2.data, mem2.data) {
		t.Fatalf("reloaded data diverges across filesystems:\n os =%#v\n mem=%#v", os2.data, mem2.data)
	}
}
