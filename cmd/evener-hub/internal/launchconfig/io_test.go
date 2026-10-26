package launchconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

func checkLoadLayer_Missing(t *testing.T) {
	got, err := LoadLayer(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("LoadLayer missing = %v, want nil", err)
	}
	if got.Schema != 0 || got.Model != "" {
		t.Errorf("LoadLayer missing returned non-zero Layer: %+v", got)
	}
}

func checkLoadLayer_Parses(t *testing.T) {
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

func checkLoadLayer_TracksExplicitEmptyModelFallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.toml")
	if err := os.WriteFile(path, []byte("model_fallbacks = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	if got.ModelFallbacks == nil {
		t.Fatal("ModelFallbacks = nil, want explicit empty slice")
	}
	if len(*got.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %v, want empty", *got.ModelFallbacks)
	}
}

func checkSaveLayer_AtomicAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := SaveLayer(path, Layer{Schema: 1, Model: "openai/gpt-5"}); err != nil {
		t.Fatalf("SaveLayer: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	hubtest.AssertFileMode0600(t, info, "the saved layer")
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

func checkSaveLayer_PersistsExplicitEmptyModelFallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.toml")
	if err := SaveLayer(path, Layer{Model: "mentions model_fallbacks", ModelFallbacks: &[]string{}}); err != nil {
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
	if got.ModelFallbacks == nil || len(*got.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %#v, want explicit empty slice", got.ModelFallbacks)
	}
}

func checkSaveMeta_AtomicAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := SaveMeta(path, Meta{Schema: 1, CWD: "/cwd"}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	hubtest.AssertFileMode0600(t, info, "the saved meta")
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

func checkLoadLayer_ReadError(t *testing.T) {
	// Root-proof read failure: a directory cannot be read as a file
	// (EISDIR), and it is not ErrNotExist so it never hits the
	// missing-file no-op branch. chmod-based injection would no-op as root.
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := LoadLayer(path)
	if err == nil {
		t.Fatal("expected error when path is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "launchconfig: read "+path) {
		t.Fatalf("expected a launchconfig read error for %s, got %v", path, err)
	}
}

func checkLoadLayer_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := os.WriteFile(path, []byte("invalid {{{ toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadLayer(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
	if !strings.Contains(err.Error(), "launchconfig: parse "+path) {
		t.Fatalf("expected a launchconfig parse error for %s, got %v", path, err)
	}
}

func checkLoadMeta_ReadError(t *testing.T) {
	// Root-proof read failure: reading a directory as a file errors
	// (EISDIR) for any uid, unlike chmod which root bypasses.
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMeta(path)
	if err == nil {
		t.Fatal("expected error when path is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "launchconfig: read "+path) {
		t.Fatalf("expected a launchconfig read error for %s, got %v", path, err)
	}
}

func checkLoadMeta_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := os.WriteFile(path, []byte("invalid {{{ toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMeta(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
	if !strings.Contains(err.Error(), "launchconfig: parse "+path) {
		t.Fatalf("expected a launchconfig parse error for %s, got %v", path, err)
	}
}

func checkSaveLayer_DoesNotFollowSymlinkAtTempName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	victim := plantTempSymlink(t, dir, path)
	if err := SaveLayer(path, Layer{Schema: 1, Model: "x"}); err != nil {
		t.Fatalf("SaveLayer: %v", err)
	}
	requireVictimUntouched(t, victim)
	if got, err := LoadLayer(path); err != nil || got.Model != "x" {
		t.Fatalf("LoadLayer = %+v, %v; want the saved layer", got, err)
	}
}

func checkSaveMeta_DoesNotFollowSymlinkAtTempName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	victim := plantTempSymlink(t, dir, path)
	if err := SaveMeta(path, Meta{Schema: 1, CWD: "/x"}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	requireVictimUntouched(t, victim)
	if got, err := LoadMeta(path); err != nil || got.CWD != "/x" {
		t.Fatalf("LoadMeta = %+v, %v; want the saved meta", got, err)
	}
}

// plantTempSymlink puts a symlink at the fixed "<path>.tmp" name a naive
// atomic writer would use, pointing at a file the save must never touch.
func plantTempSymlink(t *testing.T, dir, path string) (victim string) {
	t.Helper()
	victim = filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	return victim
}

func requireVictimUntouched(t *testing.T, victim string) {
	t.Helper()
	if got, _ := os.ReadFile(victim); string(got) != "untouched" {
		t.Fatalf("save followed the planted symlink: victim = %q", got)
	}
}

func checkSaveLayer_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "sub", "launch.toml")
	err := SaveLayer(path, Layer{Schema: 1, Model: "x"})
	if err == nil {
		t.Fatal("expected error when MkdirAll parent is a file")
	}
}

func checkSaveMeta_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "sub", "meta.toml")
	err := SaveMeta(path, Meta{Schema: 1, CWD: "/x"})
	if err == nil {
		t.Fatal("expected error when MkdirAll parent is a file")
	}
}

func checkSaveLayer_RenameError(t *testing.T) {
	dir := t.TempDir()
	// Create path as a non-empty directory so Rename fails.
	path := filepath.Join(dir, "launch.toml")
	if err := os.MkdirAll(filepath.Join(path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "sub", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := SaveLayer(path, Layer{Schema: 1, Model: "x"})
	if err == nil {
		t.Fatal("expected error when target is a non-empty directory")
	}
}

func checkSaveMeta_RenameError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.toml")
	if err := os.MkdirAll(filepath.Join(path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "sub", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := SaveMeta(path, Meta{Schema: 1, CWD: "/x"})
	if err == nil {
		t.Fatal("expected error when target is a non-empty directory")
	}
}
