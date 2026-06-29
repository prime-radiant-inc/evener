package plugin

import (
	"bytes"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/fuzz/edgeseeds"
)

// FuzzPluginManifestParse drives plugin.ParseManifest over arbitrary JSON. The
// oracle is floor "no panic" plus structured-error/no-partial (a rejected input
// yields a non-nil error AND the zero Manifest), plus the success-invariant that
// a parsed name is kebab-case (guaranteed by validatePluginName), plus a
// decode→encode→decode fixed point on the Manifest (plain JSON with RawMessage
// fields — byte-stable after the first normalizing marshal).
func FuzzPluginManifestParse(f *testing.F) {
	f.Add([]byte(`{"name":"my-plugin","version":"1.0.0"}`))
	f.Add([]byte(`{"name":"x","mcpServers":{"s":{"command":"c"}},"hooks":{"PreToolUse":[]},"agents":["a.md"]}`))
	f.Add([]byte(`{"name":"x","author":{"name":"y"}}`))
	f.Add([]byte(`{"name":"x","author":"y"}`))
	// Error shapes.
	f.Add([]byte(`{}`))                  // empty name
	f.Add([]byte(`{"name":"Bad_Name"}`)) // not kebab-case
	f.Add([]byte(`{"name":"-x"}`))       // leading hyphen
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"name":123}`)) // type mismatch
	// Generic JSON decoder stressors (deep nesting, surrogates, dup keys, …).
	for _, s := range edgeseeds.JSON() {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := ParseManifest(raw)
		if err != nil {
			// No-partial: a rejected input leaves the zero Manifest.
			if !manifestIsZero(m) {
				t.Fatalf("ParseManifest error returned non-zero Manifest: %#v\n input=%q", m, raw)
			}
			return
		}

		// Success-invariant: the name is valid kebab-case.
		if !kebabCaseRe.MatchString(m.Name) {
			t.Fatalf("accepted name %q is not kebab-case\n input=%q", m.Name, raw)
		}

		// Round-trip fixed point: first marshal normalizes, then re-decode and
		// re-marshal must be byte-stable.
		encoded, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("re-marshal failed: %v\n input=%q\n value=%#v", err, raw, m)
		}
		m2, err := ParseManifest(encoded)
		if err != nil {
			t.Fatalf("re-parse of normalized manifest failed: %v\n encoded=%q", err, encoded)
		}
		encoded2, err := json.Marshal(m2)
		if err != nil {
			t.Fatalf("second re-marshal failed: %v\n encoded=%q", err, encoded)
		}
		if !bytes.Equal(encoded, encoded2) {
			t.Fatalf("manifest round-trip not idempotent:\n input=%q\n once=%q\n twice=%q",
				raw, encoded, encoded2)
		}
	})
}

func manifestIsZero(m Manifest) bool {
	return m.Name == "" && m.Version == "" && m.Description == "" &&
		m.Author == nil && m.Homepage == "" && m.Repository == "" &&
		m.License == "" && m.Keywords == nil && m.Commands == nil &&
		m.Agents == nil && m.Hooks == nil && m.MCPServers == nil
}
