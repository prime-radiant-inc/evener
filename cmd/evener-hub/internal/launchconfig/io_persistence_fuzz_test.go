package launchconfig

import (
	"bytes"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"
)

// FuzzLaunchConfigPersistence is the differential that proves the afero
// filesystem seam beneath LoadLayer/SaveLayer/LoadMeta/SaveMeta changes nothing:
// the same program of save/load operations, replayed through two filesystems
// whose ONLY difference is the injected afero.Fs — one an OS filesystem
// sandboxed under a t.TempDir (afero.NewBasePathFs over afero.NewOsFs), the
// other a pure in-memory afero.NewMemMapFs — must produce byte-identical
// persisted TOML and identical error outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to OsFs, so if the MemMapFs path ever diverges from the OsFs path the refactor
// is unsound) and (b) a new in-memory persistence fuzzer that drives the atomic
// write path (MkdirAll → OpenFile → Write → Sync → Close → Rename) with zero
// real-disk dependency in the mem lane.
//
// The fuzzed bytes decode into an operation sequence: each op either saves a
// Layer/Meta built deterministically from the bytes at one of a fixed set of
// non-colliding logical paths, or loads one back. Time-valued Meta fields are
// built from a deterministic epoch drawn from the bytes (no wall clock), so both
// lanes encode identical values.
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//   - byte-identical persistence: every persisted file (read back through each
//     lane's own fs at the same logical path) is byte-for-byte equal across the
//     two filesystems.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. All logical paths are
// fixed constants beneath a single root — no fuzz-controlled path, so no
// fs-escape. No network, no subprocess.
func FuzzLaunchConfigPersistence(f *testing.F) {
	f.Add([]byte{opSaveLayer, 0, 0xAB, opLoadLayer, 0, opSaveMeta, 1, 0x10, opLoadMeta, 1})
	f.Add([]byte{opSaveLayer, 1, 0xFF, opSaveLayer, 1, 0x00, opLoadLayer, 1})
	f.Add([]byte{opSaveMeta, 0, 0x7F, opSaveMeta, 1, 0x01, opLoadMeta, 0, opLoadMeta, 1})
	f.Add([]byte{opLoadLayer, 0, opLoadMeta, 1})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		r := &persistReader{b: program}
		const maxOps = 128

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opPersistCount {
			case opSaveLayer:
				path := layerPathFor(r.next())
				layer := buildLayer(r.next())
				errOS := saveLayerFS(osFs, path, layer)
				errMem := saveLayerFS(memFs, path, layer)
				requirePersistErrParity(t, "SaveLayer", errOS, errMem)
			case opLoadLayer:
				path := layerPathFor(r.next())
				gotOS, errOS := loadLayerFS(osFs, path)
				gotMem, errMem := loadLayerFS(memFs, path)
				requirePersistErrParity(t, "LoadLayer", errOS, errMem)
				// Layer has pointer/slice/map fields, so re-encode both loaded
				// values and compare bytes: the decode agrees across lanes iff
				// the same value round-trips to the same TOML.
				if !bytes.Equal(encodeLayer(t, gotOS), encodeLayer(t, gotMem)) {
					t.Fatalf("LoadLayer value mismatch: os=%+v mem=%+v", gotOS, gotMem)
				}
			case opSaveMeta:
				path := metaPathFor(r.next())
				meta := buildMeta(r.next())
				errOS := saveMetaFS(osFs, path, meta)
				errMem := saveMetaFS(memFs, path, meta)
				requirePersistErrParity(t, "SaveMeta", errOS, errMem)
			case opLoadMeta:
				path := metaPathFor(r.next())
				_, errOS := loadMetaFS(osFs, path)
				_, errMem := loadMetaFS(memFs, path)
				requirePersistErrParity(t, "LoadMeta", errOS, errMem)
			}

			requireSamePersistedTree(t, osFs, memFs)
		}
	})
}

// opPersistReader op codes.
const (
	opSaveLayer = iota
	opLoadLayer
	opSaveMeta
	opLoadMeta
	opPersistCount
)

// persistPaths are the fixed logical file targets. They live beneath a single
// root and none is a ".tmp" sibling of another, so the atomic write path can
// never have one op's temp file collide with another op's target.
var persistPaths = []string{
	"/cfg/a/launch.toml",
	"/cfg/b/launch.toml",
	"/cfg/a/meta.toml",
	"/cfg/b/meta.toml",
}

func layerPathFor(b byte) string { return persistPaths[int(b)%2] }
func metaPathFor(b byte) string  { return persistPaths[2+int(b)%2] }

// buildLayer derives a Layer from a single byte, exercising scalar, pointer,
// slice, and map TOML fields so the encoder output varies across inputs.
func buildLayer(b byte) Layer {
	l := Layer{
		Schema: int(b & 0x03),
		Model:  []string{"", "openai/gpt-5", "anthropic/claude", "x"}[(b>>2)&0x03],
	}
	if b&0x10 != 0 {
		n := int(b & 0x07)
		l.MaxRounds = &n
	}
	switch (b >> 5) & 0x03 {
	case 1:
		l.ModelFallbacks = &[]string{} // explicit clear
	case 2:
		l.ModelFallbacks = &[]string{"a", "b"}
	}
	if b&0x40 != 0 {
		l.Env = map[string]string{"K": "V"}
		l.MCPs = []MCPServerSpec{{Name: "n", Command: "c", Args: []string{"-x"}}}
	}
	return l
}

// buildMeta derives a Meta from a single byte. The timestamp is a deterministic
// epoch (no wall clock) so both lanes encode identical bytes.
func buildMeta(b byte) Meta {
	m := Meta{
		Schema:    int(b & 0x03),
		CWD:       []string{"/w", "/home/x", ""}[int(b>>2)%3],
		CreatedAt: time.Unix(int64(b), 0).UTC(),
	}
	if b&0x40 != 0 {
		m.Trust = MetaTrust{
			Hashes:    []string{"abc", "def"},
			Decision:  "trusted",
			DecidedAt: time.Unix(int64(b)+1, 0).UTC(),
		}
	}
	return m
}

// persistReader is a simple cursor over the fuzz program that yields bytes and
// reports exhaustion. A missing byte reads as 0.
type persistReader struct {
	b []byte
	i int
}

func (r *persistReader) more() bool { return r.i < len(r.b) }

func (r *persistReader) next() byte {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

// requirePersistErrParity fails unless both lanes agree on whether the op errored.
func requirePersistErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSamePersistedTree asserts every logical target file (and its atomic
// temp sibling) reads back byte-identically across the two filesystems. A file
// absent on both lanes reads as nil on each and matches.
func requireSamePersistedTree(t *testing.T, osFs, memFs afero.Fs) {
	t.Helper()
	for _, p := range persistPaths {
		for _, candidate := range []string{p, p + ".tmp"} {
			osBytes := readFileOrNil(osFs, candidate)
			memBytes := readFileOrNil(memFs, candidate)
			if !bytes.Equal(osBytes, memBytes) {
				t.Fatalf("persisted bytes diverge at %s:\n os =%q\n mem=%q", candidate, osBytes, memBytes)
			}
		}
	}
}

// readFileOrNil reads path through fs, returning nil for any error (missing file
// or a non-file path), which matches identically across lanes.
func readFileOrNil(fs afero.Fs, path string) []byte {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil
	}
	return data
}

// encodeLayer serializes a loaded Layer via the SaveLayer encoder for value
// comparison across lanes.
func encodeLayer(t *testing.T, l Layer) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		t.Fatalf("encode layer: %v", err)
	}
	return buf.Bytes()
}
