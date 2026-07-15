package installid

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	info, err := fs.Stat("/state/installation_id")
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

func TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton(t *testing.T) {
	const callers = 8
	fs := newInstallationIDOverlapFS(afero.NewOsFs(), callers)
	dir := t.TempDir()
	start := make(chan struct{})
	results := make(chan string, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- LoadOrCreateInstallationIDWithFS(fs, dir)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var want string
	for got := range results {
		if err := identifier.ValidateInstallationID(got); err != nil {
			t.Fatalf("caller returned invalid ID %q: %v", got, err)
		}
		if want == "" {
			want = got
		} else if got != want {
			t.Fatalf("callers returned different IDs: first %q, got %q", want, got)
		}
	}
	stored, err := afero.ReadFile(fs, filepath.Join(dir, "installation_id"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(stored)); got != want {
		t.Fatalf("stored singleton %q, want %q", got, want)
	}
	if fs.tempWrites < 1 {
		t.Fatal("concurrency seam did not observe a temporary write")
	}
}

func TestLoadOrCreateInstallationID_ContentionWinnerIsReread(t *testing.T) {
	fs := installationIDContentionFS{Fs: afero.NewMemMapFs(), winner: identifier.MustNewInstallationID()}
	got := LoadOrCreateInstallationIDWithFS(fs, "/state")
	if got != fs.winner {
		t.Fatalf("contention winner = %q, want %q", got, fs.winner)
	}
	if _, err := fs.Stat("/state/installation_id.lock"); !os.IsNotExist(err) {
		t.Fatalf("unowned lock remains: %v", err)
	}
}

func TestLoadOrCreateInstallationID_WaitsForHeldLockOwner(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/state"
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "installation_id.lock")
	if err := afero.WriteFile(fs, lockPath, []byte("owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	winner := identifier.MustNewInstallationID()
	waiterObserved := make(chan struct{})
	ownerRelease := make(chan struct{})
	var observed sync.Once
	previousWait := installationIDContentionWait
	installationIDContentionWait = func(int) {
		observed.Do(func() { close(waiterObserved) })
		<-ownerRelease
	}
	defer func() { installationIDContentionWait = previousWait }()

	result := make(chan string, 1)
	go func() { result <- LoadOrCreateInstallationIDWithFS(fs, dir) }()
	<-waiterObserved
	if err := afero.WriteFile(fs, filepath.Join(dir, "installation_id"), []byte(winner+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	close(ownerRelease)

	got := <-result
	if got != winner {
		t.Fatalf("waiter returned %q, want owner winner %q", got, winner)
	}
	if got := readValidInstallationID(fs, filepath.Join(dir, "installation_id")); got != winner {
		t.Fatalf("stored singleton = %q, want %q", got, winner)
	}
}

func TestLoadOrCreateInstallationID_CleansOwnedLockAfterWriteAndRenameFailure(t *testing.T) {
	for name, failRename := range map[string]bool{"write": false, "rename": true} {
		t.Run(name, func(t *testing.T) {
			fs := installationIDPhaseFailFS{Fs: afero.NewMemMapFs(), failRename: failRename, failWrite: !failRename}
			if got := LoadOrCreateInstallationIDWithFS(fs, "/state"); got != "" {
				t.Fatalf("failed persistence returned %q", got)
			}
			if _, err := fs.Stat("/state/installation_id.lock"); !os.IsNotExist(err) {
				t.Fatalf("owned lock remains: %v", err)
			}
			entries, err := afero.ReadDir(fs, "/state")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".installation_id.") {
					t.Fatalf("temporary file remains: %q", entry.Name())
				}
			}
		})
	}
}

type installationIDOverlapFS struct {
	afero.Fs
	callers      int
	mu           sync.Mutex
	initialReads int
	initialReady chan struct{}
	tempWrites   int
	tempReady    chan struct{}
	renameReady  chan struct{}
	renameTurn   chan struct{}
	postReadAcks chan struct{}
}

func newInstallationIDOverlapFS(base afero.Fs, callers int) *installationIDOverlapFS {
	fs := &installationIDOverlapFS{
		Fs:           base,
		callers:      callers,
		initialReady: make(chan struct{}),
		tempReady:    make(chan struct{}),
		renameReady:  make(chan struct{}),
		renameTurn:   make(chan struct{}, 1),
		postReadAcks: make(chan struct{}, callers),
	}
	fs.renameTurn <- struct{}{}
	return fs
}

func (fs *installationIDOverlapFS) Open(name string) (afero.File, error) {
	if filepath.Base(name) == "installation_id" {
		fs.mu.Lock()
		if fs.initialReads < fs.callers {
			fs.initialReads++
			if fs.initialReads == fs.callers {
				close(fs.initialReady)
			}
			fs.mu.Unlock()
			<-fs.initialReady
		} else {
			fs.mu.Unlock()
			if _, err := fs.Stat(filepath.Join(filepath.Dir(name), "installation_id.lock")); os.IsNotExist(err) {
				fs.postReadAcks <- struct{}{}
			}
		}
	}
	return fs.Fs.Open(name)
}

func (fs *installationIDOverlapFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	base := filepath.Base(name)
	if strings.HasPrefix(base, ".installation_id.") && flag&os.O_CREATE != 0 {
		fs.mu.Lock()
		fs.tempWrites++
		fs.mu.Unlock()
		_, lockErr := fs.Stat(filepath.Join(filepath.Dir(name), "installation_id.lock"))
		control := os.IsNotExist(lockErr)
		if control {
			fs.mu.Lock()
			if fs.tempWrites == fs.callers {
				close(fs.tempReady)
			}
			fs.mu.Unlock()
		}
		if control {
			<-fs.tempReady
		}
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

func (fs *installationIDOverlapFS) Rename(oldname, newname string) error {
	if strings.HasPrefix(filepath.Base(oldname), ".installation_id.") {
		_, lockErr := fs.Stat(filepath.Join(filepath.Dir(newname), "installation_id.lock"))
		control := os.IsNotExist(lockErr)
		if control {
			<-fs.renameTurn
			if err := fs.Fs.Rename(oldname, newname); err != nil {
				return err
			}
			return nil
		}
	}
	return fs.Fs.Rename(oldname, newname)
}

type installationIDContentionFS struct {
	afero.Fs
	winner string
}

func (fs installationIDContentionFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if filepath.Base(name) == "installation_id.lock" && flag&os.O_EXCL != 0 {
		if err := fs.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return nil, err
		}
		if err := afero.WriteFile(fs.Fs, filepath.Join(filepath.Dir(name), "installation_id"), []byte(fs.winner+"\n"), 0o600); err != nil {
			return nil, err
		}
		return nil, os.ErrExist
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

type installationIDPhaseFailFS struct {
	afero.Fs
	failWrite  bool
	failRename bool
}

func (fs installationIDPhaseFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if fs.failWrite && strings.HasPrefix(filepath.Base(name), ".installation_id.") && flag&os.O_CREATE != 0 {
		return nil, errors.New("injected temporary write failure")
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

func (fs installationIDPhaseFailFS) Rename(oldname, newname string) error {
	if fs.failRename && filepath.Base(newname) == "installation_id" {
		return errors.New("injected installation rename failure")
	}
	return fs.Fs.Rename(oldname, newname)
}
