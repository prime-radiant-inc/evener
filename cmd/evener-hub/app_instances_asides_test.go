package hub

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
)

// TestOAuthAsideInstanceKnowsBothMarkers: both aside shapes parse, and each
// reports whether the removal that made it had stood. A name whose stamp is not
// all digits, or whose record part does not end in .json, is not a copy -
// including a record for an instance whose own name holds a marker.
func TestOAuthAsideInstanceKnowsBothMarkers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		instance  string
		committed bool
		aside     bool
	}{
		{"work.json.removing-5", "work", false, true},
		{"work.json.removed-5", "work", true, true},
		{"x.removing-1.json", "", false, false},
		{"x.removed-1.json", "", false, false},
		{"notes.txt", "", false, false},
		{"x.removed-1.json.removing-5", "x.removed-1", false, true},
		{"x.removing-1.json.removed-5", "x.removing-1", true, true},
		{"work.json.removing-abc", "", false, false},
		{"work.json.removed-", "", false, false},
	} {
		inst, committed, aside := oauthAsideInstance(tc.name)
		if inst != tc.instance || committed != tc.committed || aside != tc.aside {
			t.Fatalf("oauthAsideInstance(%q) = (%q, %v, %v), want (%q, %v, %v)", tc.name, inst, committed, aside, tc.instance, tc.committed, tc.aside)
		}
	}
}

// committedAsideStamps returns the stamps of every COMMITTED copy filed for name
// in the fixture's auth directory, ascending.
func committedAsideStamps(t *testing.T, f *instancesFixture, name string) []int64 {
	t.Helper()
	var out []int64
	for _, entry := range authDirEntries(t, f) {
		inst, committed, aside := oauthAsideInstance(entry)
		if !aside || !committed || inst != name {
			continue
		}
		s, err := strconv.ParseInt(oauthAsideStampText(entry), 10, 64)
		if err != nil {
			t.Fatalf("committed copy %q carries an unparseable stamp: %v", entry, err)
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// TestRestoreUncommittedOAuthAsidesPutsBackACredentialOnlyImplicitRecord: an
// implicit Codex instance exists from its OAuth record alone and has no
// providers.toml entry to name it, so the config-carried test alone would strand
// exactly the record that made the instance exist. The record is put back when
// the registry still curates the name as an implicit Codex provider.
func TestRestoreUncommittedOAuthAsidesPutsBackACredentialOnlyImplicitRecord(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	if _, err := os.Stat(f.tomlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture wrote %s (stat err=%v); a credential-only instance has no entry", f.tomlPath, err)
	}
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	aside := path + oauthAsideMarker + "1757000000000000000"
	if err := os.Rename(path, aside); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	// The premise: with the record set aside the instance is gone, because the
	// record was the whole of what made it exist.
	if before, ok := f.ctl.reg.Get().Instance("openai-codex"); ok {
		t.Fatalf("fixture: openai-codex = %+v, want no instance while its record is set aside", before)
	}

	restored, err := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the credential-only record put back")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the record was not put back: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("restored bytes = %q, want the original %q", got, original)
	}
	if _, err := os.Lstat(aside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy is still on disk (Lstat = %v), want it moved rather than copied", err)
	}
}

// TestRestoreUncommittedOAuthAsidesLeavesACommittedCopy: a committed copy is the
// leftover of a removal that STOOD, so startup never puts it back - doing so
// would undo the removal the caller was told had happened. It stays for the
// name's next removal, which collects it.
func TestRestoreUncommittedOAuthAsidesLeavesACommittedCopy(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	committed := path + oauthCommittedMarker + "1757000000000000000"
	if err := os.WriteFile(committed, []byte("the removal that stood\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	restored, err := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want a committed copy never put back")
	}
	if _, err := os.Lstat(committed); err != nil {
		t.Fatalf("the committed copy was taken (%v), want it left for the next removal", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, current) {
		t.Fatalf("the record in place = %q (%v), want the live record untouched", got, err)
	}

	// The name's next removal collects it, beside the copy that removal makes.
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if left := authDirEntries(t, f); len(left) != 0 {
		t.Fatalf("the auth directory holds %v, want the next removal to collect the committed copy", left)
	}
}

// TestInstances_RemoveCommitsTheAsideBeforeDeletingIt: a removal that stands
// renames the in-flight copy to its committed name before deleting it, so a
// delete that fails leaves a copy startup will never put back - the crash mark
// beside the failed credential. The failure is still reported as the removal's
// own, because the removal stood and the caller is the only one left who can
// delete what the report names.
func TestInstances_RemoveCommitsTheAsideBeforeDeletingIt(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil || !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("Remove = %v, want the copy it could not delete reported", err)
	}
	if _, persisted := errors.AsType[removePersistedError](err); !persisted {
		t.Fatalf("Remove = %v (%T), want a removePersistedError so the removal is announced", err, err)
	}
	left := authDirEntries(t, f)
	if len(left) != 1 || !strings.HasPrefix(left[0], "openai-codex.json"+oauthCommittedMarker) {
		t.Fatalf("the auth directory holds %v, want one committed copy", left)
	}
	for _, entry := range left {
		if strings.Contains(entry, oauthAsideMarker) {
			t.Fatalf("the leftover %s is still in flight, so startup would put it back", entry)
		}
	}

	// Startup never puts a committed copy back, so the failed delete cannot undo
	// the standing removal.
	restored, rerr := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if restored {
		t.Fatal("startup put a committed copy back, undoing the standing removal")
	}
	if _, statErr := os.Lstat(filepath.Join(filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance")), left[0])); statErr != nil {
		t.Fatalf("the committed copy was taken (%v), want it left for the next removal", statErr)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record is back at its own path (Lstat = %v), want the removal to stand", statErr)
	}

	// Removing the name again collects the committed copy.
	f.ctl.auth.deleteAside = os.Remove
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("Remove(again): %v", err)
	}
	if left := authDirEntries(t, f); len(left) != 0 {
		t.Fatalf("the auth directory holds %v, want the committed copy reclaimed", left)
	}
}

// TestInstances_RemovalsStampLaterRevivalsAboveEarlierOnesWhenTheClockStepsBackward:
// startup puts back the NEWEST copy of a name, so a backward clock must not let
// a later removal file its copy under a smaller stamp than an earlier one's. Two
// removals are driven with a clock that steps backward between them, and the
// later copy's stamp must still be the greater. The copies are then put back into
// the in-flight shape a crash before the commit mark leaves, and startup must
// restore the later record, not the older one.
func TestInstances_RemovalsStampLaterRevivalsAboveEarlierOnesWhenTheClockStepsBackward(t *testing.T) {
	f := newInstancesFixture(t, nil)
	dir := filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	save := func(email string) []byte {
		t.Helper()
		if err := authopenai.SaveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", email)); err != nil {
			t.Fatalf("SaveAuth: %v", err)
		}
		if err := f.ctl.auth.reloadRegistry(); err != nil {
			t.Fatalf("reloadRegistry: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		return data
	}

	// The sweep cannot delete, so each removal leaves its committed copy for the
	// next one (and this test) to see.
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }
	high := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	low := high.Add(-time.Hour)

	save("first@example.com")
	f.ctl.auth.now = func() time.Time { return high }
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("first Remove = nil, want the sweep failure reported")
	}
	firstStamps := committedAsideStamps(t, f, "openai-codex")
	if len(firstStamps) != 1 {
		t.Fatalf("after the first removal, committed stamps = %v, want one", firstStamps)
	}

	second := save("second@example.com")
	f.ctl.auth.now = func() time.Time { return low }
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("second Remove = nil, want the sweep failure reported")
	}
	secondStamps := committedAsideStamps(t, f, "openai-codex")
	if len(secondStamps) != 2 {
		t.Fatalf("after the second removal, committed stamps = %v, want two", secondStamps)
	}
	var later int64
	for _, s := range secondStamps {
		if s != firstStamps[0] {
			later = s
		}
	}
	if later <= firstStamps[0] {
		t.Fatalf("the later removal's stamp = %d, want greater than the earlier %d: a backward clock filed the newer copy under the smaller stamp", later, firstStamps[0])
	}

	// The shape a crash before the commit mark would leave, so startup is what
	// chooses between them.
	for _, entry := range authDirEntries(t, f) {
		if !strings.Contains(entry, oauthCommittedMarker) {
			continue
		}
		inFlight := strings.Replace(entry, oauthCommittedMarker, oauthAsideMarker, 1)
		if err := os.Rename(filepath.Join(dir, entry), filepath.Join(dir, inFlight)); err != nil {
			t.Fatalf("Rename(%s -> %s): %v", entry, inFlight, err)
		}
	}
	restored, err := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if !restored {
		t.Fatal("startup put nothing back, want the newest copy restored")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the record was not put back: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Fatalf("startup put back %q, want the later removal's record %q", got, second)
	}
}

// TestRestoreUncommittedOAuthAsidesNeedsAReloadForACredentialOnlyInstance: the
// registry's instance list is computed at load, so a credential-only instance
// whose record came back after that load stays out of the list - and out of
// every listing over it - until the next Reload. This is the reload runMain adds
// after a restore. A config-carried instance needs none, because providers.toml
// carries it into the list either way; this pins the credential-only case.
func TestRestoreUncommittedOAuthAsidesNeedsAReloadForACredentialOnlyInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	if err := os.Rename(path, path+oauthAsideMarker+"1757000000000000000"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	if _, ok := f.ctl.reg.Get().Instance("openai-codex"); ok {
		t.Fatal("fixture: the set-aside instance is still in the registry list")
	}

	restored, err := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the record put back")
	}
	// Without the reload the credential is back but the instance is not: the list
	// was computed before the record returned.
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); ok {
		t.Fatalf("openai-codex = %+v, want it out of the stale list until the reload", inst)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	after, ok := f.ctl.reg.Get().Instance("openai-codex")
	if !ok || after.CredentialSource != "oauth" {
		t.Fatalf("openai-codex = %+v (ok = %v), want the restored credential-only instance back after the reload", after, ok)
	}
}
