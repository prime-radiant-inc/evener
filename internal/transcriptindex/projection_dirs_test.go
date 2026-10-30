package transcriptindex

import (
	"errors"
	"os"
	"strings"
	"testing"
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
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("SidecarDirs missed %q (got %v)", want, got)
		}
	}
	for _, g := range got {
		if !strings.HasPrefix(g, path+".index") {
			t.Errorf("SidecarDirs returned %q, not a sidecar of this transcript", g)
		}
	}
}
