package launchconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLayer_Missing(t *testing.T) {
	got, err := LoadLayer(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("LoadLayer missing = %v, want nil", err)
	}
	if got.Schema != 0 || got.Model != "" {
		t.Errorf("LoadLayer missing returned non-zero Layer: %+v", got)
	}
}

func TestLoadLayer_Parses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := os.WriteFile(path, []byte("schema = 1\nmodel = \"openai/gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model = %q", got.Model)
	}
}

func TestLoadLayer_TracksExplicitEmptyModelFallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.toml")
	if err := os.WriteFile(path, []byte("model_fallbacks = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	if !got.ModelFallbacksSet {
		t.Fatal("ModelFallbacksSet = false, want true")
	}
	if got.ModelFallbacks == nil {
		t.Fatal("ModelFallbacks = nil, want explicit empty slice")
	}
	if len(got.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %v, want empty", got.ModelFallbacks)
	}
}

func TestSaveLayer_AtomicAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := SaveLayer(path, Layer{Schema: 1, Model: "openai/gpt-5"}); err != nil {
		t.Fatalf("SaveLayer: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	// Temp file must not linger.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file still present")
	}
	// Round-trip.
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("round-trip Model = %q", got.Model)
	}
}

func TestSaveMeta_AtomicAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := SaveMeta(path, Meta{Schema: 1, CWD: "/cwd"}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file still present")
	}
	got, err := LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.CWD != "/cwd" {
		t.Errorf("round-trip CWD = %q", got.CWD)
	}
}
