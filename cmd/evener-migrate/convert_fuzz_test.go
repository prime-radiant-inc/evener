package migrate

import (
	"bytes"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// FuzzConvertProvidersConfig drives the old-schema converter over arbitrary
// bytes. Oracles: never panics; conversion is deterministic; and whenever it
// reports success, the produced file loads through the registry's own parser
// — the dry-parse guarantee convertProvidersConfig promises its callers.
func FuzzConvertProvidersConfig(f *testing.F) {
	f.Add([]byte("default = \"kimi\"\n\n[instances.kimi]\ntype = \"kimi\"\nbase_url = \"https://api.kimi.com/coding/v1\"\n"))
	f.Add([]byte("[instances.gw]\ntype = \"openai\"\napi_style = \"chat-completions\"\nbase_url = \"http://localhost:1\"\napi_key = \"$VAR\"\n"))
	f.Add([]byte("[instances.x]\ntype = \"glm\"\nquirks = \"glm\"\n[instances.x.compat]\nthinking_format = \"zai\"\n"))
	f.Add([]byte("[instances.weird]\ntype = \"no-such-type\"\n"))
	f.Add([]byte("schema = 1\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		outA, notesA, errA := convertProvidersConfig(data)
		outB, notesB, errB := convertProvidersConfig(data)
		if (errA == nil) != (errB == nil) || !bytes.Equal(outA, outB) || len(notesA) != len(notesB) {
			t.Fatal("nondeterministic conversion")
		}
		if errA != nil {
			return
		}
		if _, err := registry.ParseConfig(outA); err != nil {
			t.Fatalf("converted output must load through the registry parser: %v\n%s", err, outA)
		}
	})
}
