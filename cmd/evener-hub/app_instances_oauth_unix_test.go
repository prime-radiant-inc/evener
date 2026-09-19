//go:build unix

package hub

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
)

// unreadableOAuthRecord writes a record for an implicit Codex instance and
// makes it unreadable, returning its path. It skips when the process can still
// read a 0000 file (root), because the premise needs an unreadable one.
func unreadableOAuthRecord(t *testing.T, f *instancesFixture) string {
	t.Helper()
	if err := authopenai.SaveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", "codex@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this process can read a 0000 file (running as root?); the premise needs an unreadable record")
	}
	return path
}

// TestInstances_RemoveDeletesAnUnreadableOAuthRecord: the record is taken away
// by path, so one the hub cannot read - a permission fault, an I/O error - is
// still one a removal has to be able to take away. What refused it was the
// rollback copy, which read the bytes before deleting anything; the removal now
// moves the file aside instead, which preserves bytes a read cannot reach. The
// row is offered as removable either way, because an implicit Codex instance
// exists from the record file's presence, so without this the offer was one the
// hub could not honour.
func TestInstances_RemoveDeletesAnUnreadableOAuthRecord(t *testing.T) {
	f := newInstancesFixture(t, nil)
	path := unreadableOAuthRecord(t, f)
	// The premise: the row the panes offer Remove for is on the listing.
	if before := entry(t, f.ctl.List(), "openai-codex"); !before.Implicit {
		t.Fatalf("fixture: openai-codex = %+v, want an implicit instance", before)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unreadable record survived the removal (Lstat = %v)", err)
	}
	if listedInstance(f.ctl.List(), "openai-codex") {
		t.Fatal("openai-codex is still listed after its record was removed")
	}
	// The copy the removal moved aside does not outlive it.
	entries, err := os.ReadDir(filepath.Join(f.stateDir, "auth"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".removing") {
			t.Fatalf("the removal left its preserved copy behind: %s", e.Name())
		}
	}
}

// TestInstances_RemoveRestoresAnUnreadableOAuthRecordWhenTheCleanupFails: the
// aside copy IS the rollback, so a removal that fails after moving the record
// must put those bytes back - including one the hub cannot read. The cleanup is
// failed through its seam, the way the readable-record case does it, so nothing
// here depends on permissions beyond making the file unreadable.
func TestInstances_RemoveRestoresAnUnreadableOAuthRecordWhenTheCleanupFails(t *testing.T) {
	f := newInstancesFixture(t, nil)
	path := unreadableOAuthRecord(t, f)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	f.ctl.auth.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete refused") }

	err = f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil || !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("Remove = %v, want the cleanup failure", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the unreadable record was not restored: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored bytes = %q, want the original %q", restored, original)
	}
}

// TestInstances_SetAsideOAuthFileRefusesWhenTheCandidateCannotBeChecked: naming
// the path a record is set aside at is a check followed by a rename, and a stat
// that fails for anything but "not there" - a candidate past the name limit, a
// path this process cannot search - cannot say whether the destination is free.
// Stepping past that error would spin while the removal holds credMu, so the
// call refuses with the cause named instead. The timeout is the guard: a
// regression to spinning fails here rather than hanging the suite.
//
// The candidate is driven past the name limit, because the record path is
// stat'ed before the candidate is named: a record path this process cannot
// stat is refused there, before the candidate is named, and never reaches this
// one. The premise is checked, so a filesystem that takes the over-long name
// skips rather than failing.
func TestInstances_SetAsideOAuthFileRefusesWhenTheCandidateCannotBeChecked(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// The record component stays inside the 255-byte per-component limit these
	// filesystems enforce, and the aside built from it - the record's whole path
	// plus the marker and a 19-digit stamp - does not.
	name := strings.Repeat("a", 240)
	path := authopenai.AuthFilePath(f.stateDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	probe := path + oauthAsideMarker + "1757000000000000000"
	if _, err := os.Lstat(probe); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Skipf("this filesystem takes an over-long component (Lstat(%s) = %v); the premise needs one that does not", probe, err)
	}

	type aside struct {
		path string
		err  error
	}
	done := make(chan aside, 1)
	go func() {
		asidePath, err := f.ctl.setAsideOAuthFile(name, false)
		done <- aside{asidePath, err}
	}()
	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "is free to set its OAuth state aside") {
			t.Fatalf("setAsideOAuthFile = (%q, %v), want the refusal naming the check that could not be made", got.path, got.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("setAsideOAuthFile did not return: the aside-candidate loop is spinning while holding credMu")
	}
}

// TestInstances_SetAsideOAuthFileRefusesWhenTheDirectoryCannotBeListed: the seed
// for an aside's stamp steps past the copies already filed for the name, so it
// cannot be trusted when the directory cannot be enumerated - a higher-stamped
// copy already there plus a backward clock would order the copies wrongly, and
// startup would restore the older credential instead of the current one. The
// removal must refuse rather than guess, before anything is deleted.
//
// The directory is made write+execute but not readable: the record can still be
// stat'ed and renamed by name, while the directory cannot be listed. The premise
// is checked, so a process that still lists it (root) skips.
func TestInstances_SetAsideOAuthFileRefusesWhenTheDirectoryCannotBeListed(t *testing.T) {
	f := newInstancesFixture(t, nil)
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const content = "the record the removal must not lose\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatalf("Chmod(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("this process can list a 0311 directory (running as root?); the premise needs one it cannot")
	}

	aside, err := f.ctl.setAsideOAuthFile("openai-codex", false)
	if err == nil || !strings.Contains(err.Error(), "to order its OAuth copies") {
		t.Fatalf("setAsideOAuthFile = (%q, %v), want the refusal naming the directory that could not be listed", aside, err)
	}
	if b, rerr := os.ReadFile(path); rerr != nil || string(b) != content {
		t.Fatalf("the record = %q (%v), want it left in place by the refusal", b, rerr)
	}
}

// TestFreeAsideNameRefusesWhenTheDirectoryCannotBeListed: freeAsideName steps a
// candidate past the stamps already filed for a name, so an unreadable directory
// must make it report the copy as un-carriable rather than land a name that could
// collide. The premise is checked, so a process that still lists the directory
// (root) skips.
func TestFreeAsideNameRefusesWhenTheDirectoryCannotBeListed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openai-codex.json"+oauthAsideMarker+"7"), []byte("a copy\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatalf("Chmod(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("this process can list a 0311 directory (running as root?); the premise needs one it cannot")
	}

	got, ok := freeAsideName(dir, "openai-codex", false, 7)
	if ok {
		t.Fatalf("freeAsideName = (%q, true), want a refusal when the directory cannot be listed", got)
	}
	if got != "" {
		t.Fatalf("freeAsideName = (%q, false), want no candidate on a refusal", got)
	}
}
