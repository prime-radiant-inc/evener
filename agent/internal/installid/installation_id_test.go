package installid

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/serf/identifier"
)

func TestLoadOrCreateInstallationID_ReplacesLegacyAndInvalidValues(t *testing.T) {
	for name, stored := range map[string]string{
		"legacy ULID": "01ARZ3NDEKTSV4RRFFQ69G5FAV\n",
		"invalid":     "not-an-installation-id\n",
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			const dir = "/state"
			if err := fs.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "installation_id")
			if err := afero.WriteFile(fs, path, []byte(stored), 0o600); err != nil {
				t.Fatal(err)
			}
			got := LoadOrCreateInstallationIDWithFS(fs, dir)
			if err := identifier.ValidateInstallationID(got); err != nil {
				t.Fatalf("generated ID %q: %v", got, err)
			}
			if got == stored[:len(stored)-1] {
				t.Fatal("invalid stored ID was reused")
			}
		})
	}
}

func TestLoadOrCreateInstallationID_ReusesValidValueAndStores0600(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/state"
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	want := identifier.MustNewInstallationID()
	path := filepath.Join(dir, "installation_id")
	if err := afero.WriteFile(fs, path, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadOrCreateInstallationIDWithFS(fs, dir); got != want {
		t.Fatalf("reused ID = %q, want %q", got, want)
	}
	info, err := fs.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadOrCreateInstallationID_AtomicReplacementLeavesNoTemporaryFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	got := LoadOrCreateInstallationIDWithFS(fs, "/state")
	if err := identifier.ValidateInstallationID(got); err != nil {
		t.Fatalf("generated ID %q: %v", got, err)
	}
	entries, err := afero.ReadDir(fs, "/state")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "installation_id" {
		t.Fatalf("state directory entries = %#v", entries)
	}
	info, err := fs.Stat(filepath.Join("/state", "installation_id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generated file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadOrCreateInstallationID_EmptyStateDirAndFilesystemFailure(t *testing.T) {
	if got := LoadOrCreateInstallationIDWithFS(afero.NewMemMapFs(), " \t"); got != "" {
		t.Fatalf("empty state dir = %q, want empty", got)
	}
	if got := LoadOrCreateInstallationIDWithFS(afero.NewReadOnlyFs(afero.NewMemMapFs()), "/state"); got != "" {
		t.Fatalf("read-only fs = %q, want empty", got)
	}
}
