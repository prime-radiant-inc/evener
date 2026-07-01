package providercfg

import (
	"bytes"
	"testing"

	"github.com/spf13/afero"
)

// FuzzProvidersTOMLPersistence is the differential that proves the afero
// filesystem seam beneath WriteFile/LoadFile changes nothing: the same program
// of write/load operations, replayed through two lanes whose ONLY difference is
// the injected afero.Fs — one an OS filesystem sandboxed under a t.TempDir
// (afero.NewBasePathFs over afero.NewOsFs), the other a pure in-memory
// afero.NewMemMapFs — must produce byte-identical persisted TOML and identical
// error/exists outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to OsFs, so if the MemMapFs path ever diverges from the OsFs path the refactor
// is unsound) and (b) a new in-memory persistence fuzzer that drives
// writeFileFS/loadFileFS with zero real-disk dependency in the mem lane.
//
// Oracle checked after EVERY operation:
//   - WriteFile error parity: an op errors on the OS lane iff it errors on mem.
//   - LoadFile (exists, err, cfg) parity across the two lanes.
//   - byte-identical persistence: for every candidate config path, the bytes
//     read back through each lane's own fs are byte-for-byte equal.
//
// The persisted content is Marshal(cfg), which is filesystem-independent, so any
// divergence can only come from the atomic write machinery (MkdirAll, TempFile,
// Chmod, Sync, Rename) behaving differently across the two filesystems — exactly
// what the seam must keep identical.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. Candidate paths are a
// fixed set of clean logical paths, so no path can escape the sandbox. No
// network, no subprocess. Temp files (random-named) are never compared, so their
// nondeterministic names cannot cause a false divergence.
func FuzzProvidersTOMLPersistence(f *testing.F) {
	f.Add([]byte{opWrite, 0, 2, 1, 0})
	f.Add([]byte{opWrite, 1, 1, 0, opLoad, 1, opWrite, 1, 3, 2, 0})
	f.Add([]byte{opWrite, 2, 3, 0, 1, 2, opLoad, 2, opLoad, 3})
	f.Add([]byte{opLoad, 0, opWrite, 3, 1, 5, opWrite, 3, 1, 5})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		r := &persistReader{b: program}
		const maxOps = 96

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % persistOpCount {
			case opWrite:
				path := candidatePaths[int(r.next())%len(candidatePaths)]
				cfg := r.config()
				errOS := writeFileFS(osFs, path, cfg)
				errMem := writeFileFS(memFs, path, cfg)
				requireWriteParity(t, path, errOS, errMem)
			case opLoad:
				path := candidatePaths[int(r.next())%len(candidatePaths)]
				cfgOS, existsOS, errOS := loadFileFS(osFs, path)
				cfgMem, existsMem, errMem := loadFileFS(memFs, path)
				requireLoadParity(t, path, cfgOS, existsOS, errOS, cfgMem, existsMem, errMem)
			}

			requireSamePersisted(t, osFs, memFs)
		}
	})
}

// candidatePaths is the fixed set of clean logical config paths the fuzzer
// writes to and reads from. Varying depth exercises MkdirAll; reusing paths
// exercises rename-over-existing. All are absolute and clean so BasePathFs keeps
// them inside the sandbox.
var candidatePaths = []string{
	"/providers.toml",
	"/sub/providers.toml",
	"/a/b/providers.toml",
	"/sub/other.toml",
}

// candidateTypes is a fixed pool of provider type values to draw from. Marshal
// does not validate the type, so any value round-trips; drawing from real type
// names keeps the corpus realistic.
var candidateTypes = []Type{"openai", "anthropic", "google", "kimi-anthropic", "ollama"}

// candidateNames is a fixed pool of instance names to draw from.
var candidateNames = []string{"a", "b", "openai", "primary", "x"}

const (
	opWrite = iota
	opLoad
	persistOpCount
)

// persistReader draws operands from the fuzz-provided byte program. Reads past
// the end return 0, so a short program simply draws zeros (a valid, if dull,
// operation stream).
type persistReader struct {
	b   []byte
	pos int
}

func (r *persistReader) more() bool { return r.pos < len(r.b) }

func (r *persistReader) next() byte {
	if r.pos >= len(r.b) {
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

// config draws a Config: 0..3 instances each with a name/type and optional
// api_style, base_url, and quirks fields, plus a default. The exact same Config
// is written through both lanes, so the persisted bytes are lane-independent by
// construction and any divergence must come from the filesystem machinery.
func (r *persistReader) config() Config {
	n := int(r.next()) % 4
	styles := []APIStyle{"", StyleResponses, StyleChatCompletions, StyleAuto}
	insts := make([]InstanceConfig, 0, n)
	for i := 0; i < n; i++ {
		inst := InstanceConfig{
			Name:     candidateNames[int(r.next())%len(candidateNames)],
			Type:     candidateTypes[int(r.next())%len(candidateTypes)],
			APIStyle: styles[int(r.next())%len(styles)],
		}
		flags := r.next()
		if flags&1 != 0 {
			inst.BaseURL = "https://example.test/v1"
		}
		if flags&2 != 0 {
			inst.Quirks = "no-stream"
		}
		if flags&4 != 0 {
			inst.APIKey = "sk-secret" // must never reach the persisted bytes
		}
		insts = append(insts, inst)
	}
	def := ""
	if n > 0 {
		def = insts[int(r.next())%n].Name
	}
	return Config{Default: def, Instances: insts}
}

// requireWriteParity fails unless both lanes agree on whether the write errored.
func requireWriteParity(t *testing.T, path string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("WriteFile(%q) error parity broken: os=%v mem=%v", path, errOS, errMem)
	}
}

// requireLoadParity fails unless both lanes agree on the load outcome: the same
// exists flag, the same error-ness, and the same parsed Config.
func requireLoadParity(t *testing.T, path string, cfgOS Config, existsOS bool, errOS error, cfgMem Config, existsMem bool, errMem error) {
	t.Helper()
	if existsOS != existsMem {
		t.Fatalf("LoadFile(%q) exists parity broken: os=%v mem=%v", path, existsOS, existsMem)
	}
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("LoadFile(%q) error parity broken: os=%v mem=%v", path, errOS, errMem)
	}
	osBytes, _ := Marshal(cfgOS)
	memBytes, _ := Marshal(cfgMem)
	if !bytes.Equal(osBytes, memBytes) {
		t.Fatalf("LoadFile(%q) config diverges:\n os =%s\n mem=%s", path, osBytes, memBytes)
	}
}

// requireSamePersisted asserts that, for every candidate config path, the bytes
// read back through each lane's own fs are byte-for-byte identical. A missing
// file reads as nil on both lanes.
func requireSamePersisted(t *testing.T, osFs, memFs afero.Fs) {
	t.Helper()
	for _, path := range candidatePaths {
		osBytes := readOrNil(osFs, path)
		memBytes := readOrNil(memFs, path)
		if !bytes.Equal(osBytes, memBytes) {
			t.Fatalf("persisted bytes diverge at %q:\n os =%q\n mem=%q", path, osBytes, memBytes)
		}
	}
}

// readOrNil reads path through fs, returning nil for any error (a missing file
// reads identically as nil on both lanes).
func readOrNil(fs afero.Fs, path string) []byte {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil
	}
	return data
}
