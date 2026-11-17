package transcriptindex

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// asBinaryWithSchema runs fn as a binary whose transcript schema fingerprint
// is id: the only way one test process can play an older daemon beside the
// current hub.
func asBinaryWithSchema(id string, fn func()) {
	saved := schemaID
	schemaID = id
	defer func() { schemaID = saved }()
	fn()
}

// A daemon started from an older build keeps writing its own projection of
// the transcript while the hub, on a newer build, repairs and reads its own.
// Sharing one sidecar made each rebuild the other's build on every cycle; each
// projection keeps a sidecar of its own, so neither rebuilds the other's.
func TestBinariesOfDifferentProjectionsKeepSeparateSidecars(t *testing.T) {
	path := writeFixture(t, fixture{name: "mixed", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})

	var older *Index
	var olderDir string
	asBinaryWithSchema("older-binary", func() {
		olderDir = DirFor(path)
		older = openIndex(t, path, olderDir)
	})
	olderBuilds := older.rebuilds

	currentDir := DirFor(path)
	if currentDir == olderDir {
		t.Fatalf("both projections share the sidecar %q", currentDir)
	}
	hub := openRefusing(t, path, currentDir)
	var refusal *RebuildRequiredError
	if err := hub.CatchUp(); !errors.As(err, &refusal) {
		t.Fatalf("hub on a transcript with no sidecar of its projection = %v, want a rebuild refusal", err)
	}
	repair, err := OpenWithoutCatchUp(path, currentDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repair.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repair.RebuildIfStillRefused(info.Size(), refusal.Build); err != nil {
		t.Fatal(err)
	}
	catchUp(t, hub)

	// The live daemon extends its own sidecar; the hub follows the append.
	appendBytes(t, path, encodeEntry(t, 2, user("more")))
	asBinaryWithSchema("older-binary", func() { catchUp(t, older) })
	if older.rebuilds != olderBuilds {
		t.Fatalf("the older binary rebuilt its sidecar %d times beside the hub's", older.rebuilds-olderBuilds)
	}
	catchUp(t, hub)
}

// Every projection's sidecar sits beside the transcript under a name that
// starts with the transcript's, so one prefix match finds them all.
func TestSidecarDirsListsEveryProjectionsSidecar(t *testing.T) {
	path := writeFixture(t, fixture{name: "listed", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	var dirs []string
	asBinaryWithSchema("older-binary", func() {
		dirs = append(dirs, DirFor(path))
		openIndex(t, path, DirFor(path))
	})
	dirs = append(dirs, DirFor(path))
	openIndex(t, path, DirFor(path))
	// The unsuffixed name daemons built before per-projection sidecars write.
	legacy := path + ".index"
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	dirs = append(dirs, legacy)

	got, err := SidecarDirs(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range dirs {
		if !slices.Contains(got, want) {
			t.Errorf("SidecarDirs missed %q (got %v)", want, got)
		}
	}
	for _, g := range got {
		if !strings.HasPrefix(g, path+".index") {
			t.Errorf("SidecarDirs returned %q, not a sidecar of this transcript", g)
		}
	}
}

// ageSidecar moves every file and directory in a sidecar past any grace period
// a pruner could use, so the test states the age a superseded sidecar must
// reach rather than the grace period's value.
func ageSidecar(t testing.TB, dir string) {
	t.Helper()
	when := time.Now().Add(-365 * 24 * time.Hour)
	if err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, when, when)
	}); err != nil {
		t.Fatal(err)
	}
}

// sidecarContents names what a sidecar directory still holds, so a test can
// say a superseded one was emptied of everything but the lock a returning
// handle serializes on.
func sidecarContents(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// assertSupersededEmptied pins what reclaiming a superseded sidecar leaves:
// the lock file and nothing else. The directory and its lock inode survive so
// a handle that comes back rebuilds into the same directory and serializes on
// the same lock, instead of racing a second lock inode into existence.
func assertSupersededEmptied(t testing.TB, dir string) {
	t.Helper()
	if got := sidecarContents(t, dir); !slices.Equal(got, []string{lockFileName}) {
		t.Fatalf("superseded sidecar %q holds %v, want only %q", dir, got, lockFileName)
	}
}

// A rebuild that commits a new build reclaims the sidecars of superseded
// projections: a sidecar no one has used for far longer than any reader could
// hold one is emptied of its builds and pointer, so an install stops paying
// for every projection it has ever run.
func TestASuccessfulRebuildReclaimsASupersededSidecar(t *testing.T) {
	path := writeFixture(t, fixture{name: "prune", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	var superseded string
	asBinaryWithSchema("older-binary", func() {
		superseded = DirFor(path)
		catchUp(t, openIndex(t, path, superseded))
	})
	current := DirFor(path)
	if superseded == current {
		t.Fatalf("both projections share the sidecar %q", current)
	}
	ageSidecar(t, superseded)

	hub := openIndex(t, path, current)
	catchUp(t, hub)
	if err := hub.Rebuild(hub.meta.Length); err != nil {
		t.Fatal(err)
	}

	assertSupersededEmptied(t, superseded)
	// The rebuild's own projection is untouched and still serves.
	if _, err := os.Stat(filepath.Join(current, currentFileName)); err != nil {
		t.Fatalf("the live sidecar lost its pointer: %v", err)
	}
	assertAllWindows(t, hub, path)
}

// The unsuffixed ".index" directory is the sidecar every daemon built before
// per-projection sidecars wrote; an upgraded install carries one per session.
// A rebuild reclaims it once it is old enough, which is the population that
// actually costs disk space on a real install.
func TestASuccessfulRebuildReclaimsTheLegacySidecar(t *testing.T) {
	path := writeFixture(t, fixture{name: "legacy-prune", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	legacy := path + sidecarSuffix
	asBinaryWithSchema("pre-per-projection", func() {
		catchUp(t, openIndex(t, path, legacy))
	})
	ageSidecar(t, legacy)

	hub := openIndex(t, path, DirFor(path))
	catchUp(t, hub)
	if err := hub.Rebuild(hub.meta.Length); err != nil {
		t.Fatal(err)
	}

	assertSupersededEmptied(t, legacy)
	assertAllWindows(t, hub, path)
}

// Nothing used recently is reclaimed. The grace period is what keeps a
// projection another binary is still running from losing its sidecar, so a
// fresh one must survive a rebuild untouched.
func TestARecentlyUsedSupersededSidecarIsKept(t *testing.T) {
	path := writeFixture(t, fixture{name: "fresh-superseded", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	var superseded string
	asBinaryWithSchema("older-binary", func() {
		superseded = DirFor(path)
		catchUp(t, openIndex(t, path, superseded))
	})
	before := sidecarContents(t, superseded)

	hub := openIndex(t, path, DirFor(path))
	catchUp(t, hub)
	if err := hub.Rebuild(hub.meta.Length); err != nil {
		t.Fatal(err)
	}

	if got := sidecarContents(t, superseded); !slices.Equal(got, before) {
		t.Fatalf("a just-built superseded sidecar holds %v, want its %v kept", got, before)
	}
}

// The projection binary that owns the sidecar is still running it. Its reads
// stamp the sidecar as used, so the split stays even after the grace period:
// the old sidecar is not reclaimed while anything is still reading it, which is
// the failure this whole feature would otherwise create.
func TestASupersededSidecarStillBeingReadIsKept(t *testing.T) {
	path := writeFixture(t, everything())
	var superseded string
	asBinaryWithSchema("older-binary", func() {
		superseded = DirFor(path)
		catchUp(t, openIndex(t, path, superseded))
	})
	ageSidecar(t, superseded)
	before := sidecarContents(t, superseded)

	// The older binary keeps serving from its own sidecar, aged out from its
	// last build but very much in use.
	asBinaryWithSchema("older-binary", func() {
		assertAllWindows(t, openIndex(t, path, superseded), path)
	})

	hub := openIndex(t, path, DirFor(path))
	catchUp(t, hub)
	if err := hub.Rebuild(hub.meta.Length); err != nil {
		t.Fatal(err)
	}

	if got := sidecarContents(t, superseded); !slices.Equal(got, before) {
		t.Fatalf("a sidecar in use was reclaimed, holding %v, want its %v kept", got, before)
	}
}

// A writer holds its sidecar's exclusive lock while it rebuilds or extends.
// The pruner probes that lock without waiting and leaves the sidecar alone, so
// two rebuilds cannot delete each other's live files.
func TestAPrunerSkipsASidecarAWriterHolds(t *testing.T) {
	path := writeFixture(t, fixture{name: "held-superseded", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	var superseded string
	asBinaryWithSchema("older-binary", func() {
		superseded = DirFor(path)
		catchUp(t, openIndex(t, path, superseded))
	})
	ageSidecar(t, superseded)
	before := sidecarContents(t, superseded)

	release, err := HoldSidecarLockForTest(superseded)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	hub := openIndex(t, path, DirFor(path))
	catchUp(t, hub)
	if err := hub.Rebuild(hub.meta.Length); err != nil {
		t.Fatal(err)
	}

	if got := sidecarContents(t, superseded); !slices.Equal(got, before) {
		t.Fatalf("a held superseded sidecar holds %v, want its %v kept", got, before)
	}
}

// A reader that is inside a read when the sidecar is reclaimed is not
// disturbed: its open file survives the directory entry's removal, and it
// reads the same bytes it would have read before. A handle that comes back
// afterwards heals by rebuilding into the same directory rather than failing,
// which is what keeps a reclaimed sidecar from being a read error.
func TestAReaderOfAReclaimedSidecarKeepsReading(t *testing.T) {
	path := writeFixture(t, fixture{name: "pruned-reader", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	var superseded string
	var reader *Index
	var items *os.File
	var before []byte
	asBinaryWithSchema("older-binary", func() {
		superseded = DirFor(path)
		reader = openIndex(t, path, superseded)
		// The fd stands for a read already in flight: everything the handle
		// needs from the build is open on it.
		var err error
		items, err = os.Open(filepath.Join(liveBuild(t, superseded), itemsFile))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = items.Close() })
		if before, err = io.ReadAll(items); err != nil {
			t.Fatal(err)
		}
	})
	ageSidecar(t, superseded)

	hub := openIndex(t, path, DirFor(path))
	catchUp(t, hub)
	if err := hub.Rebuild(hub.meta.Length); err != nil {
		t.Fatal(err)
	}
	assertSupersededEmptied(t, superseded)

	// The read in flight: the fd's inode outlives its directory entry.
	if _, err := items.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	after, err := io.ReadAll(items)
	if err != nil {
		t.Fatalf("reading the reclaimed sidecar's open file: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("the open file's bytes changed under a reader in flight: %d bytes before, %d after", len(before), len(after))
	}

	// The handle that comes back afterwards heals into the same directory.
	asBinaryWithSchema("older-binary", func() {
		assertAllWindows(t, reader, path)
	})
}

// A rebuild killed before it commits leaves the superseded sidecar exactly as
// it was: reclaiming it is only safe once the new build and its pointer are
// durable, so a crash in the middle must cost nothing. The install keeps a
// usable index either way — the old sidecar still serves its own projection,
// and the interrupted rebuild's own projection rebuilds from the transcript.
func TestAKilledRebuildKeepsTheSupersededSidecar(t *testing.T) {
	path := writeFixture(t, everything())
	current := DirFor(path)
	x := openIndex(t, path, current)
	catchUp(t, x)

	// A superseded sidecar the current projection has not reclaimed yet: it
	// was built after the current one, so this rebuild is the one that would
	// reclaim it.
	var superseded string
	asBinaryWithSchema("older-binary", func() {
		superseded = DirFor(path)
		catchUp(t, openIndex(t, path, superseded))
	})
	ageSidecar(t, superseded)
	before := sidecarContents(t, superseded)

	// The kill lands after the records are flushed and before the meta commit:
	// the point a rebuild is furthest from having anything to fall back on.
	simulateKill(t)
	err := x.Rebuild(x.meta.Length)
	testKillAfterRecordWrites = nil
	if err == nil {
		t.Fatal("the killed rebuild reported success")
	}

	if got := sidecarContents(t, superseded); !slices.Equal(got, before) {
		t.Fatalf("a killed rebuild left the superseded sidecar holding %v, want its %v kept", got, before)
	}

	// The old projection still serves from its own sidecar, which is the index
	// the killed rebuild did not take away.
	asBinaryWithSchema("older-binary", func() {
		assertAllWindows(t, openIndex(t, path, superseded), path)
	})

	// That read used the sidecar, so the current projection's next rebuild
	// leaves it alone: the age check keys on last use, which is what protects
	// a projection another binary is still running.
	recovered := openIndex(t, path, current)
	catchUp(t, recovered)
	if got := sidecarContents(t, superseded); !slices.Equal(got, before) {
		t.Fatalf("a rebuild reclaimed a sidecar in use, holding %v, want its %v kept", got, before)
	}

	// Once it goes unused for longer than the grace period, the next rebuild
	// reclaims it; the current projection serves either way.
	ageSidecar(t, superseded)
	if err := recovered.Rebuild(recovered.meta.Length); err != nil {
		t.Fatal(err)
	}
	assertSupersededEmptied(t, superseded)
	assertAllWindows(t, recovered, path)
}
