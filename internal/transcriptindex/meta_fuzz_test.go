package transcriptindex

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzReadMeta drives the real sidecar-meta seam: it writes fuzzed bytes as
// a build's meta.json and reads it through readMeta, exercising os.ReadFile,
// json.Unmarshal into meta and the format/projection check. The oracle is
// floor "no panic": corrupt or adversarial bytes on disk (a build directory
// another process wrote, or a crash mid-write) must yield a clean error,
// never a crash.
// Registry: native:.:./internal/transcriptindex:FuzzReadMeta
func FuzzReadMeta(f *testing.F) {
	f.Add([]byte(`{"format":4,"projection":"apptranscript-items-v1/entry-ordinal-positions-v2","incarnation":"inc-1"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Add([]byte(`{"format":"not an int"}`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`{"format":4,"projection":"apptranscript-items-v1/entry-ordinal-positions-v2"`)) // truncated

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		const build = "b"
		if err := os.Mkdir(filepath.Join(dir, build), 0o755); err != nil {
			t.Fatalf("create build dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, build, metaFile), raw, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		x := &Index{dir: dir, build: build}
		// readMeta either returns a clean error or a meta value; either way
		// it must not panic, and a successful parse must at least be a valid
		// meta struct (touch it to catch a partially-built value).
		if m, err := x.readMeta(); err == nil {
			_ = m.Format
		}
	})
}
