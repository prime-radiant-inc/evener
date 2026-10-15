package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/envvars"
)

// TestTildeHomeUnderHome covers the path-starts-with-home branch (lines 39-41).
func TestTildeHomeUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sub := filepath.Join(home, "projects", "evener")
	got := tildeHome(sub)
	if got != "~/projects/evener" {
		t.Fatalf("tildeHome(%q) = %q, want ~/projects/evener", sub, got)
	}
}

// TestTildeHomeExactHome covers the path == home branch (lines 42-44).
func TestTildeHomeExactHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := tildeHome(home)
	if got != "~" {
		t.Fatalf("tildeHome(%q) = %q, want ~", home, got)
	}
}

// TestTildeHomeUnrelated covers the path not under home branch (line 45).
func TestTildeHomeUnrelated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := tildeHome("/etc/passwd")
	if got != "/etc/passwd" {
		t.Fatalf("tildeHome(/etc/passwd) = %q, want /etc/passwd", got)
	}
}

// TestDefaultMCPConfigPathXDGSet covers the path where XDG_CONFIG_HOME is set.
func TestDefaultMCPConfigPathXDGSet(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv(envvars.XDGConfigHome.Name, xdg)
	got := defaultMCPConfigPath()
	want := filepath.Join(xdg, "evener", "mcp.json")
	if got != want {
		t.Fatalf("defaultMCPConfigPath with XDG set = %q, want %q", got, want)
	}
}

// TestDefaultMCPConfigPathXDGFallback covers the path where XDG_CONFIG_HOME is
// empty and the home-based fallback is used (lines 93-97).
func TestDefaultMCPConfigPathXDGFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envvars.XDGConfigHome.Name, "")
	got := defaultMCPConfigPath()
	want := filepath.Join(home, ".config", "evener", "mcp.json")
	if got != want {
		t.Fatalf("defaultMCPConfigPath fallback = %q, want %q", got, want)
	}
}

// TestFileSizeHumanUnits covers the different size-unit branches.
func TestFileSizeHumanUnits(t *testing.T) {
	dir := t.TempDir()
	// Small file (< 1KB).
	small := filepath.Join(dir, "small")
	if err := os.WriteFile(small, make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fileSizeHuman(small); !strings.Contains(got, "B") {
		t.Fatalf("fileSizeHuman(512B) = %q, want contains 'B'", got)
	}
	// KB file.
	kb := filepath.Join(dir, "kb")
	if err := os.WriteFile(kb, make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fileSizeHuman(kb); !strings.Contains(got, "KB") {
		t.Fatalf("fileSizeHuman(2KB) = %q, want contains 'KB'", got)
	}
	// MB file.
	mb := filepath.Join(dir, "mb")
	if err := os.WriteFile(mb, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fileSizeHuman(mb); !strings.Contains(got, "MB") {
		t.Fatalf("fileSizeHuman(2MB) = %q, want contains 'MB'", got)
	}
}

// TestFileAgeHumanJustNow covers the "just now" branch (d < 2m): every age
// under two minutes reads the same, and one past it reads differently and
// counts its minutes.
func TestFileAgeHumanJustNow(t *testing.T) {
	dir := t.TempDir()
	aged := func(name string, age time.Duration) string {
		t.Helper()
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
		return f
	}
	recent := fileAgeHuman(aged("recent", 0))
	if recent == "" {
		t.Fatal("fileAgeHuman(recent) is empty, want an age")
	}
	if got := fileAgeHuman(aged("a-minute-old", time.Minute)); got != recent {
		t.Fatalf("fileAgeHuman(1m) = %q, want the same %q as a file just written", got, recent)
	}
	if got := fileAgeHuman(aged("five-minutes-old", 5*time.Minute)); got == recent || !strings.Contains(got, "5") {
		t.Fatalf("fileAgeHuman(5m) = %q, want a minute count distinct from %q", got, recent)
	}
}

// TestFileAgeHumanMissing covers the error path (line 52-53).
func TestFileAgeHumanMissing(t *testing.T) {
	got := fileAgeHuman(filepath.Join(t.TempDir(), "missing"))
	if got != "" {
		t.Fatalf("fileAgeHuman(missing) = %q, want empty", got)
	}
}

// TestFileSizeHumanMissing covers the error path (line 72-73).
func TestFileSizeHumanMissing(t *testing.T) {
	got := fileSizeHuman(filepath.Join(t.TempDir(), "missing"))
	if got != "" {
		t.Fatalf("fileSizeHuman(missing) = %q, want empty", got)
	}
}
