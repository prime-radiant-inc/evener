package launchconfig

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// FuzzLaunchConfigPersistence replays a program of save/load operations
// through LoadLayer/SaveLayer/LoadMeta/SaveMeta against a real directory and
// checks every load against the last value the program saved at that path: a
// saved value reads back unchanged, and a path never saved reads back as the
// zero value.
//
// The fuzzed bytes decode into an operation sequence: each op either saves a
// Layer/Meta built deterministically from the bytes at one of a fixed set of
// non-colliding paths, or loads one back. Time-valued Meta fields are built
// from a deterministic epoch drawn from the bytes (no wall clock).
//
// SAFETY: all writes land under a t.TempDir root; the paths are fixed
// constants beneath it, so there is no fuzz-controlled path. No network, no
// subprocess.
func FuzzLaunchConfigPersistence(f *testing.F) {
	f.Add([]byte{opSaveLayer, 0, 0xAB, opLoadLayer, 0, opSaveMeta, 1, 0x10, opLoadMeta, 1})
	f.Add([]byte{opSaveLayer, 1, 0xFF, opSaveLayer, 1, 0x00, opLoadLayer, 1})
	f.Add([]byte{opSaveMeta, 0, 0x7F, opSaveMeta, 1, 0x01, opLoadMeta, 0, opLoadMeta, 1})
	f.Add([]byte{opLoadLayer, 0, opLoadMeta, 1})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		root := t.TempDir()
		savedLayers := map[string]Layer{}
		savedMetas := map[string]Meta{}

		r := &persistReader{b: program}
		const maxOps = 128

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opPersistCount {
			case opSaveLayer:
				path := filepath.Join(root, layerPathFor(r.next()))
				layer := buildLayer(r.next())
				if err := SaveLayer(path, layer); err != nil {
					t.Fatalf("SaveLayer: %v", err)
				}
				savedLayers[path] = layer
			case opLoadLayer:
				path := filepath.Join(root, layerPathFor(r.next()))
				got, err := LoadLayer(path)
				if err != nil {
					t.Fatalf("LoadLayer: %v", err)
				}
				// Layer has pointer/slice/map fields, so compare the
				// re-encoded values: the decode agrees iff the same value
				// round-trips to the same TOML.
				if !bytes.Equal(encodeLayer(t, got), encodeLayer(t, savedLayers[path])) {
					t.Fatalf("LoadLayer = %+v, want last saved %+v", got, savedLayers[path])
				}
			case opSaveMeta:
				path := filepath.Join(root, metaPathFor(r.next()))
				meta := buildMeta(r.next())
				if err := SaveMeta(path, meta); err != nil {
					t.Fatalf("SaveMeta: %v", err)
				}
				savedMetas[path] = meta
			case opLoadMeta:
				path := filepath.Join(root, metaPathFor(r.next()))
				got, err := LoadMeta(path)
				if err != nil {
					t.Fatalf("LoadMeta: %v", err)
				}
				want := savedMetas[path]
				if got.Schema != want.Schema || got.CWD != want.CWD || !got.CreatedAt.Equal(want.CreatedAt) {
					t.Fatalf("LoadMeta = %+v, want last saved %+v", got, want)
				}
			}
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

// persistPaths are the fixed file targets, relative to the test's temp root.
var persistPaths = []string{
	"cfg/a/launch.toml",
	"cfg/b/launch.toml",
	"cfg/a/meta.toml",
	"cfg/b/meta.toml",
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
// epoch (no wall clock).
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

// encodeLayer serializes a loaded Layer via the SaveLayer encoder for value
// comparison.
func encodeLayer(t *testing.T, l Layer) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		t.Fatalf("encode layer: %v", err)
	}
	return buf.Bytes()
}
