package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRegistry_AbsentReturnsEmptyV2(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("LoadRegistry absent: %v", err)
	}
	if r.Version != 2 || r.Plugins == nil || len(r.Plugins) != 0 {
		t.Fatalf("absent registry = %+v, want {Version:2, Plugins:{}}", r)
	}
}

func TestSaveLoadRegistry_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "installed_plugins.json")
	in := Registry{
		Version: 2,
		Plugins: map[string][]InstallEntry{
			"widget@acme": {{
				InstallPath:  "/store/cache/acme/widget/abc123",
				Version:      "1.0.0",
				GitCommitSha: "abc123",
				InstalledAt:  time.Unix(1000, 0).UTC(),
				LastUpdated:  time.Unix(2000, 0).UTC(),
				Enabled:      true,
				Source:       Source{Kind: SourceDirectory, Path: "/store/cache/acme/widget"},
			}},
		},
	}
	if err := SaveRegistry(p, in); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	out, err := LoadRegistry(p)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	got := out.Plugins["widget@acme"][0]
	if got.InstallPath != "/store/cache/acme/widget/abc123" || got.Version != "1.0.0" || !got.Enabled {
		t.Fatalf("round-trip entry = %+v", got)
	}
}

func TestLoadRegistry_RejectsUnknownVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "installed_plugins.json")
	os.WriteFile(p, []byte(`{"version":99,"plugins":{}}`), 0o644)
	if _, err := LoadRegistry(p); err == nil {
		t.Fatal("expected error for unsupported schema version 99")
	}
}
