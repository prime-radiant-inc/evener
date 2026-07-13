package installid

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/afero"
)

// FuzzLoadOrCreateInstallationIDWithFS exercises the installation-ID persistence
// contract over an in-memory filesystem. A successful first write must be a
// valid ULID and every later load from the same filesystem must return it.
func FuzzLoadOrCreateInstallationIDWithFS(f *testing.F) {
	f.Add("default", false, false)
	f.Add("existing", false, true)
	f.Add("ignored", true, false)
	f.Add("spaces and/slashes", false, false)

	f.Fuzz(func(t *testing.T, label string, emptyStateDir, preexisting bool) {
		if len(label) > 512 {
			label = label[:512]
		}

		stateDir := " \t"
		if !emptyStateDir {
			stateDir = filepath.Join(string(filepath.Separator), "state", hex.EncodeToString([]byte(label)))
		}
		fs := afero.NewMemMapFs()

		const existingID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		if preexisting && strings.TrimSpace(stateDir) != "" {
			if err := fs.MkdirAll(stateDir, 0o700); err != nil {
				t.Fatalf("seed state directory: %v", err)
			}
			if err := afero.WriteFile(fs, filepath.Join(stateDir, "installation_id"), []byte(existingID+"\n"), 0o600); err != nil {
				t.Fatalf("seed installation ID: %v", err)
			}
		}

		got := LoadOrCreateInstallationIDWithFS(fs, stateDir)
		if strings.TrimSpace(stateDir) == "" {
			if got != "" {
				t.Fatalf("empty state directory returned %q, want empty ID", got)
			}
			return
		}
		if got == "" {
			t.Fatal("non-empty state directory returned an empty ID")
		}
		if _, err := ulid.ParseStrict(got); err != nil {
			t.Fatalf("installation ID %q is not a ULID: %v", got, err)
		}
		if preexisting && got != existingID {
			t.Fatalf("preexisting ID = %q, want %q", got, existingID)
		}

		if again := LoadOrCreateInstallationIDWithFS(fs, stateDir); again != got {
			t.Fatalf("reloaded installation ID = %q, want stable %q", again, got)
		}
		stored, err := afero.ReadFile(fs, filepath.Join(stateDir, "installation_id"))
		if err != nil {
			t.Fatalf("read stored installation ID: %v", err)
		}
		if strings.TrimSpace(string(stored)) != got {
			t.Fatalf("stored installation ID = %q, want %q", stored, got)
		}

		// The production wrapper remains an OS-filesystem boundary, but the
		// temporary state directory keeps it isolated and deterministic.
		wrappedDir := filepath.Join(t.TempDir(), "state")
		wrapped := LoadOrCreateInstallationID(wrappedDir)
		if wrapped == "" || LoadOrCreateInstallationID(wrappedDir) != wrapped {
			t.Fatalf("production wrapper did not persist a stable ID: %q", wrapped)
		}

		if readonly := LoadOrCreateInstallationIDWithFS(afero.NewReadOnlyFs(afero.NewMemMapFs()), "/readonly"); readonly != "" {
			t.Fatalf("read-only filesystem returned %q, want empty ID", readonly)
		}
		if failed := LoadOrCreateInstallationIDWithFS(installationIDWriteFailFS{Fs: afero.NewMemMapFs()}, "/write-failure"); failed != "" {
			t.Fatalf("failed write returned %q, want empty ID", failed)
		}
		racingBase := afero.NewMemMapFs()
		racing := installationIDWriteFailFS{Fs: racingBase, replacement: []byte(existingID + "\n")}
		if recovered := LoadOrCreateInstallationIDWithFS(racing, "/write-race"); recovered != existingID {
			t.Fatalf("write-race recovery returned %q, want %q", recovered, existingID)
		}
	})
}

var errInstallationIDWrite = errors.New("injected installation ID write failure")

// installationIDWriteFailFS lets the persistence helper exercise its
// write-failure and read-after-failure paths without touching the host disk.
type installationIDWriteFailFS struct {
	afero.Fs
	replacement []byte
}

func (fs installationIDWriteFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if filepath.Base(name) == "installation_id" && flag&os.O_CREATE != 0 {
		if fs.replacement != nil {
			if err := afero.WriteFile(fs.Fs, name, fs.replacement, perm); err != nil {
				return nil, err
			}
		}
		return nil, errInstallationIDWrite
	}
	return fs.Fs.OpenFile(name, flag, perm)
}
