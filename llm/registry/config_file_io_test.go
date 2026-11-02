package registry

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A model-level provider override that names the plain Vertex SDK swaps the
// row onto the vertex-gemini preset.
func TestFromModelsDevVertexGeminiOverride(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"prov":{"npm":"@ai-sdk/openai","models":{"m":{"provider":{"npm":"@ai-sdk/google-vertex"},"modalities":{"output":["text"]}}}}}`)
	provs, err := FromModelsDev(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 1 {
		t.Fatalf("providers = %d, want 1", len(provs))
	}
	m, ok := provs[0].Models["m"]
	if !ok {
		t.Fatal("model m missing")
	}
	if m.Protocol != ProtocolGoogle {
		t.Fatalf("protocol = %q, want %q", m.Protocol, ProtocolGoogle)
	}
	if m.Transport == nil || m.Transport.Preset != PresetVertexGemini {
		t.Fatalf("transport preset = %+v, want %q", m.Transport, PresetVertexGemini)
	}
}

// Baseline of an unknown protocol is nil: no prunable set exists to copy.
func TestBaselineUnknownProtocol(t *testing.T) {
	t.Parallel()
	if got := Baseline("no-such-protocol"); got != nil {
		t.Fatalf("Baseline(unknown) = %v, want nil", got)
	}
	if got := Baseline(ProtocolAnthropic); got == nil {
		t.Fatal("Baseline(anthropic) = nil, want the path table")
	}
}

func TestGetPathWalksNestedObjects(t *testing.T) {
	t.Parallel()
	body := map[string]any{"a": map[string]any{"b": 42}}
	if v, ok := getPath(body, "a.b"); !ok || v != 42 {
		t.Fatalf("getPath(a.b) = (%v, %v), want (42, true)", v, ok)
	}
	if _, ok := getPath(body, "a.c"); ok {
		t.Fatal("getPath(a.c) reported a missing leaf as present")
	}
	scalar := map[string]any{"a": 1}
	if _, ok := getPath(scalar, "a.b"); ok {
		t.Fatal("getPath through a scalar leaf must miss")
	}
}

// Ref renders the instance/model pair a toggle or listing names.
func TestRefString(t *testing.T) {
	t.Parallel()
	if got := (Ref{Instance: "openai", Model: "gpt-4o"}).String(); got != "openai/gpt-4o" {
		t.Fatalf("Ref.String() = %q", got)
	}
}

// A providers.toml this build cannot encode must be refused before it
// reaches disk: the transport body is author-supplied arbitrary TOML, and a
// value with no TOML representation (a function) fails the marshalling.
func TestMarshalConfigRefusesUnencodableBody(t *testing.T) {
	t.Parallel()
	l := &Layer{
		Providers: map[string]Provider{
			"p": {Transport: Transport{Body: map[string]any{"f": func() {}}}},
		},
	}
	if _, err := MarshalConfig(l); err == nil {
		t.Fatal("MarshalConfig accepted a body value with no TOML representation")
	}
}

func TestWriteConfigFileRefusesUnencodableBody(t *testing.T) {
	t.Parallel()
	l := &Layer{
		Providers: map[string]Provider{
			"p": {Transport: Transport{Body: map[string]any{"f": func() {}}}},
		},
	}
	if err := WriteConfigFile(filepath.Join(t.TempDir(), "providers.toml"), l); err == nil {
		t.Fatal("WriteConfigFile accepted a layer it could not marshal")
	}
}

// Reading a path that exists but is not a regular file is a real read
// failure, not "absent": a writer must never rewrite a file it could not
// read back.
func TestReadConfigFileDirectoryPath(t *testing.T) {
	t.Parallel()
	if _, _, err := ReadConfigFile(t.TempDir()); err == nil {
		t.Fatal("ReadConfigFile on a directory must fail")
	}
}

func TestWriteConfigFileUnwritableParent(t *testing.T) {
	t.Parallel()
	// Root and Windows write through a 0o500 directory, so the unwritable
	// parent cannot actually refuse the write there (the sibling guard in
	// write_test.go).
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs a directory the process cannot write")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Skipf("cannot drop write permission: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	err := WriteConfigFile(filepath.Join(ro, "sub", "providers.toml"), &Layer{})
	if err == nil {
		t.Fatal("WriteConfigFile into an unwritable parent must fail")
	}
}

func TestWriteConfigFileTargetIsDirectory(t *testing.T) {
	t.Parallel()
	if err := WriteConfigFile(t.TempDir(), &Layer{}); err == nil {
		t.Fatal("WriteConfigFile onto a directory path must fail")
	}
}
