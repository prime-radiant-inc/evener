package launchconfig

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSaveLayer_PersistsExplicitEmptyModelFallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.toml")
	if err := SaveLayer(path, Layer{Model: "mentions model_fallbacks", ModelFallbacksSet: true, ModelFallbacks: []string{}}); err != nil {
		t.Fatalf("SaveLayer: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "model_fallbacks = []") {
		t.Fatalf("saved layer missing explicit empty model_fallbacks:\n%s", data)
	}
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	if !got.ModelFallbacksSet {
		t.Fatal("ModelFallbacksSet = false, want true")
	}
	if got.ModelFallbacks == nil || len(got.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %#v, want explicit empty slice", got.ModelFallbacks)
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

func TestLoadLayer_ReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := os.WriteFile(path, []byte("schema = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)
	_, err := LoadLayer(path)
	if err == nil {
		t.Fatal("expected error when file is unreadable")
	}
}

func TestLoadLayer_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := os.WriteFile(path, []byte("invalid {{{ toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadLayer(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

func TestLoadMeta_ReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := os.WriteFile(path, []byte("schema = 1\ncwd = \"/tmp\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)
	_, err := LoadMeta(path)
	if err == nil {
		t.Fatal("expected error when file is unreadable")
	}
}

func TestLoadMeta_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := os.WriteFile(path, []byte("invalid {{{ toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMeta(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

func TestSaveLayer_WriteError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	path := filepath.Join(dir, "launch.toml")
	if err := SaveLayer(path, Layer{Schema: 1, Model: "x"}); err == nil {
		t.Fatal("expected error when directory is read-only")
	}
}

func TestSaveMeta_WriteError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	path := filepath.Join(dir, "meta.toml")
	if err := SaveMeta(path, Meta{Schema: 1, CWD: "/x"}); err == nil {
		t.Fatal("expected error when directory is read-only")
	}
}

func TestSaveLayer_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "sub", "launch.toml")
	if err := SaveLayer(path, Layer{Schema: 1, Model: "x"}); err == nil {
		t.Fatal("expected error when MkdirAll parent is a file")
	}
}

func TestSaveMeta_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "sub", "meta.toml")
	if err := SaveMeta(path, Meta{Schema: 1, CWD: "/x"}); err == nil {
		t.Fatal("expected error when MkdirAll parent is a file")
	}
}

func TestSaveLayer_RenameError(t *testing.T) {
	dir := t.TempDir()
	// Create path as a non-empty directory so Rename fails.
	path := filepath.Join(dir, "launch.toml")
	if err := os.MkdirAll(filepath.Join(path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "sub", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveLayer(path, Layer{Schema: 1, Model: "x"}); err == nil {
		t.Fatal("expected error when target is a non-empty directory")
	}
}

func TestSaveMeta_RenameError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := os.MkdirAll(filepath.Join(path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "sub", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveMeta(path, Meta{Schema: 1, CWD: "/x"}); err == nil {
		t.Fatal("expected error when target is a non-empty directory")
	}
}
