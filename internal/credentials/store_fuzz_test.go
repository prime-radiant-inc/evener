package credentials

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

// FuzzCredentialsStoreDecode drives the in-package toml.Decode into fileShape —
// the exact decode LoadStore performs after reading the file, with the os.Stat /
// 0600-mode / os.ReadFile gate bypassed (fuzz bytes go straight in). The oracle
// is floor "no panic" plus, on a clean decode, a round-trip fixed point through
// the SAME encoder Store.save uses on disk: re-encode → re-decode → DeepEqual on
// the decoded values (compare values, not bytes; TOML map key order is
// non-deterministic). The map-nil-guard lives in LoadStore, not the decoder, so
// it is out of this oracle's scope.
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
		var data fileShape
		if _, err := toml.Decode(string(raw), &data); err != nil {
			return // rejected input — LoadStore discards whatever was left
		}

		// Round-trip through Store.save's encoder.
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(data); err != nil {
			t.Fatalf("re-encode of decoded fileShape failed: %v\n input=%q\n value=%#v", err, raw, data)
		}
		var data2 fileShape
		if _, err := toml.Decode(buf.String(), &data2); err != nil {
			t.Fatalf("re-decode of re-encoded fileShape failed: %v\n encoded=%q", err, buf.String())
		}
		if !reflect.DeepEqual(data, data2) {
			t.Fatalf("fileShape round-trip not stable:\n input=%q\n once=%#v\n twice=%#v",
				raw, data, data2)
		}
	})
}
