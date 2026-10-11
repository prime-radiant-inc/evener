package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdatedInstallDir(t *testing.T) {
	for _, tc := range []struct {
		name, previous, key, current string
		enabled, duplicate, want     bool
	}{
		{name: "same installed plugin", previous: "cache/market/scope/old", key: "scope@market", current: "cache/market/scope/new", enabled: true, want: true},
		{name: "explicitly selected default off", previous: "cache/market/scope/old", key: "scope@market", current: "cache/market/scope/new", want: true},
		{name: "other marketplace", previous: "cache/market/scope/old", key: "scope@other", current: "cache/other/scope/new", enabled: true},
		{name: "other plugin", previous: "cache/market/scope/old", key: "other@market", current: "cache/market/other/new", enabled: true},
		{name: "registry path changes marketplace", previous: "cache/market/scope/old", key: "scope@market", current: "cache/other/scope/new", enabled: true},
		{name: "registry path changes plugin", previous: "cache/market/scope/old", key: "scope@market", current: "cache/market/other/new", enabled: true},
		{name: "unchanged revision", previous: "cache/market/scope/old", key: "scope@market", current: "cache/market/scope/old", enabled: true},
		{name: "ambiguous installs", previous: "cache/market/scope/old", key: "scope@market", current: "cache/market/scope/new", enabled: true, duplicate: true},
		{name: "explicit directory", previous: "external/market/scope/old", key: "scope@market", current: "cache/market/scope/new", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			previous, current := filepath.Join(store, tc.previous), filepath.Join(store, tc.current)
			if err := os.MkdirAll(current, 0o755); err != nil {
				t.Fatal(err)
			}
			entries := []InstallEntry{{InstallPath: current, Enabled: tc.enabled, Source: Source{Kind: SourceDirectory, Path: current}}}
			if tc.duplicate {
				entries = append(entries, entries[0])
			}
			if err := SaveRegistry(filepath.Join(store, registryFileName), Registry{Plugins: map[string][]InstallEntry{tc.key: entries}}); err != nil {
				t.Fatal(err)
			}
			got, err := UpdatedInstallDir(previous)
			want := ""
			if tc.want {
				want = current
			}
			if err != nil || got != want {
				t.Fatalf("UpdatedInstallDir = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestUpdatedInstallDirUnavailable(t *testing.T) {
	for _, previous := range []string{"", "cache/market/scope/old", filepath.Join(t.TempDir(), "cache", "market", "scope", "old")} {
		got, err := UpdatedInstallDir(previous)
		if err != nil || got != "" {
			t.Fatalf("UpdatedInstallDir(%q) = %q, %v", previous, got, err)
		}
	}
}
