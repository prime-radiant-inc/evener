package credentials

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// FuzzCredentialsStoreDecode drives the REAL LoadStore seam: it writes the
// fuzzed bytes to a 0600 temp file and loads it, exercising the stat / mode-gate
// / ReadFile / toml.Decode / nil-guard path in store.go (not just the toml
// library's decode of the type). Oracles: floor "no panic" on arbitrary file
// contents and on the accessors a caller reaches for; and, on a clean load, a
// round-trip fixed point through the real save encoder — re-save to a second
// 0600 file, reload, and compare the decoded values (TOML map key order is
// non-deterministic, so compare values not bytes).
func FuzzCredentialsStoreDecode(f *testing.F) {
	f.Add([]byte("schema = 1\n[providers.openai]\napi_key = \"sk-x\"\n"))
	f.Add([]byte("[providers.anthropic]\napi_key = \"k\"\n[providers.openai]\napi_key = \"j\"\n"))
	f.Add([]byte("schema = 2\n")) // no providers
	// Degenerate / error shapes.
	f.Add([]byte(""))
	f.Add([]byte("not toml ["))
	f.Add([]byte("schema = \"x\"\n"))               // type mismatch
	f.Add([]byte("[providers.x]\napi_key = 123\n")) // api_key type mismatch
	f.Add([]byte("[providers]\nx = \"y\"\n"))       // providers as scalar

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "credentials.toml")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}

		// Floor: LoadStore must never panic on arbitrary file contents.
		s, err := LoadStore(path)
		if err != nil {
			return // rejected input (parse/type error) — nothing further to assert
		}

		// Read accessors a caller reaches for must never panic — across the
		// well-known providers AND the names actually present in the loaded file,
		// so the file-hit and env-fallback branches of each resolver are driven.
		_ = s.List()
		names := []string{"openai", "anthropic", "openai-compatible", "ollama"}
		for name := range s.data.Providers {
			names = append(names, name)
		}
		for _, name := range names {
			_ = EnvVars(name)
			_, _ = s.Get(name)
			_, _ = s.Layers(name)
			_, _ = s.InstanceLayers(name, "openai")
			_, _ = s.ResolveKey(name, "openai")
			_, _ = s.APIKeyFor(name)
		}

		// Round-trip through the real save encoder: re-save to a fresh 0600 file,
		// reload, and compare. Exercises both LoadStore and Store.save.
		path2 := filepath.Join(dir, "credentials2.toml")
		s.path = path2
		if err := s.save(); err != nil {
			t.Fatalf("save of loaded store failed: %v\n input=%q\n data=%#v", err, raw, s.data)
		}
		s2, err := LoadStore(path2)
		if err != nil {
			t.Fatalf("reload of saved store failed: %v\n saved-from=%q", err, raw)
		}
		if !reflect.DeepEqual(s.data, s2.data) {
			t.Fatalf("store data round-trip not stable:\n input=%q\n once=%#v\n twice=%#v",
				raw, s.data, s2.data)
		}

		// Mutators must round-trip through save without error (s.path is path2).
		if err := s.Set("openai", "set-by-fuzz"); err != nil {
			t.Fatalf("Set failed: %v\n input=%q", err, raw)
		}
		if err := s.Clear("openai"); err != nil {
			t.Fatalf("Clear failed: %v\n input=%q", err, raw)
		}
	})
}
