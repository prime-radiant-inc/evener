package transcriptindex

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sidecarWithProjectionVersion builds a healthy sidecar for a one-entry
// transcript, then stamps its live build as written by a binary whose
// projection version is the current one plus delta.
func sidecarWithProjectionVersion(t *testing.T, delta int) (path, dir string) {
	t.Helper()
	path = writeFixture(t, fixture{name: "versioned", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	dir = DirFor(path)
	x := openIndex(t, path, dir)
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	version, ok := projectionVersion(currentProjection())
	if !ok {
		t.Fatalf("projection %q carries no version", currentProjection())
	}
	stamp := "transcript-read-model-v" + strconv.Itoa(version+delta) + ":" + schemaID
	rewriteMetaField(t, dir, "projection", strconv.Quote(stamp))
	return path, dir
}

// sidecarBytes is every file under dir, by relative path: the sidecar's whole
// observable state, for asserting a handle left it untouched.
func sidecarBytes(t testing.TB, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func requireSidecarUntouched(t testing.TB, dir string, before map[string]string) {
	t.Helper()
	after := sidecarBytes(t, dir)
	if len(after) != len(before) {
		t.Fatalf("sidecar files = %d, want the %d it had", len(after), len(before))
	}
	for name, data := range before {
		if after[name] != data {
			t.Fatalf("sidecar file %s changed", name)
		}
	}
}

// A binary with an older projection than the sidecar's live build never
// rebuilds over it, whichever way it asks: an ordinary open, an explicit
// rebuild, or a refusing handle's catch-up.
func TestANewerSidecarIsNeverRebuiltByAnOlderBinary(t *testing.T) {
	path, dir := sidecarWithProjectionVersion(t, +1)
	before := sidecarBytes(t, dir)

	if _, err := Open(path, dir); !errors.Is(err, ErrNewerSidecar) {
		t.Fatalf("Open = %v, want ErrNewerSidecar", err)
	}
	requireSidecarUntouched(t, dir, before)

	x, err := OpenWithoutCatchUp(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Rebuild(info.Size()); !errors.Is(err, ErrNewerSidecar) {
		t.Fatalf("Rebuild = %v, want ErrNewerSidecar", err)
	}
	if err := x.RebuildIfStillRefused(info.Size(), filepath.Base(liveBuild(t, dir))); !errors.Is(err, ErrNewerSidecar) {
		t.Fatalf("RebuildIfStillRefused = %v, want ErrNewerSidecar", err)
	}
	requireSidecarUntouched(t, dir, before)

	refusing := openRefusing(t, path, dir)
	err = refusing.CatchUp()
	if !errors.Is(err, ErrNewerSidecar) || errors.Is(err, ErrRebuildRequired) {
		t.Fatalf("refusing CatchUp = %v, want ErrNewerSidecar and no rebuild request", err)
	}
	requireSidecarUntouched(t, dir, before)
}

// A newer layout of the sidecar is as foreign to an older binary as a newer
// projection.
func TestASidecarOfANewerFormatIsNeverRebuiltByAnOlderBinary(t *testing.T) {
	path, dir := sidecarWithProjectionVersion(t, 0)
	rewriteMetaField(t, dir, "format", strconv.Itoa(formatVersion+1))
	before := sidecarBytes(t, dir)
	if _, err := Open(path, dir); !errors.Is(err, ErrNewerSidecar) {
		t.Fatalf("Open = %v, want ErrNewerSidecar", err)
	}
	requireSidecarUntouched(t, dir, before)
}

// An older sidecar, or one of the same version whose schema fingerprint
// differs, is rebuilt by this binary into its own projection.
func TestAnOlderOrSameVersionSidecarIsRebuilt(t *testing.T) {
	for name, delta := range map[string]int{"older": -1, "same version": 0} {
		t.Run(name, func(t *testing.T) {
			path, dir := sidecarWithProjectionVersion(t, delta)
			if delta == 0 {
				// Same version, another schema fingerprint: not orderable.
				rewriteMetaField(t, dir, "projection", strconv.Quote(projectionID+":other-schema"))
			}
			x := openIndex(t, path, dir)
			if x.rebuilds == 0 {
				t.Fatal("the sidecar was not rebuilt")
			}
			data, err := os.ReadFile(filepath.Join(liveBuild(t, dir), metaFile))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), currentProjection()) {
				t.Fatalf("rebuilt meta = %s, want this binary's projection", data)
			}
		})
	}
}
