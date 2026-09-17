package hub

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
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

// TestRestoreUncommittedOAuthAsidesSweepsCommittedCopiesTheConfigDoesNotCarry:
// a committed copy is the leftover of a removal that STOOD, so startup never
// puts it back - doing so would undo the removal the caller was told had
// happened. When providers.toml does not carry the name, nothing will ever want
// it either (no later removal of that name will collect it), so startup deletes
// it rather than letting the credential sit under auth/<name>.json.removed-<stamp>
// forever. A committed copy whose name the config DOES carry is left exactly
// where it is: after a removal that failed between its commit mark and its
// rollback, that copy can be the instance's only record, and leaving it is the
// conservative rule. (This replaces an earlier test that left every committed
// copy for the name's next removal; change 3 sweeps the ones no config carries.)
func TestRestoreUncommittedOAuthAsidesSweepsCommittedCopiesTheConfigDoesNotCarry(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "work", "work@example.com")
	dir := filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
	record := authopenai.AuthFilePath(f.stateDir, "work")
	current, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	carried := "work.json" + oauthCommittedMarker + "1757000000000000000"
	orphan := "retired.json" + oauthCommittedMarker + "1757000000000000001"
	for name, body := range map[string]string{carried: "the removal that stood\n", orphan: "an orphaned removal\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	restored, err := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want a committed copy never put back")
	}
	if _, err := os.Lstat(filepath.Join(dir, orphan)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the committed copy %s was not swept (Lstat = %v), want it deleted with no config entry", orphan, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, carried)); err != nil {
		t.Fatalf("the committed copy %s was taken (%v), want it left while the config carries the name", carried, err)
	}
	got, err := os.ReadFile(record)
	if err != nil || !bytes.Equal(got, current) {
		t.Fatalf("the record in place = %q (%v), want the live record untouched", got, err)
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
	// the standing removal. With no providers.toml entry carrying the name,
	// startup also sweeps the copy, so the credential does not sit on disk forever
	// with no removal left to collect it.
	restored, rerr := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if restored {
		t.Fatal("startup put a committed copy back, undoing the standing removal")
	}
	if _, statErr := os.Lstat(filepath.Join(filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance")), left[0])); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the committed copy survived startup (Lstat = %v), want the no-config sweep to delete it", statErr)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record is back at its own path (Lstat = %v), want the removal to stand", statErr)
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

// asideCopyNames returns the base names of the aside copies - either shape - in
// the fixture's auth directory, or nil when the directory is absent. A registry
// loader closure records it to pin what a load saw on disk; it takes no *testing.T
// because a loader may be called from the registry's own goroutine.
func asideCopyNames(f *instancesFixture) []string {
	dir := filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, _, aside := oauthAsideInstance(e.Name()); aside {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestInstances_RemoveMarksTheAsideCommittedBeforeTheReload pins the ordering the
// medium's fix rests on: a removal must mark its aside committed BEFORE the
// reload that publishes it. A crash anywhere from the commit point onward then
// leaves a copy startup never puts back (restoreUncommittedOAuthAsides); leaving
// the mark to the post-reload reclaim would put the whole reload inside a window
// where a crash resurrects an instance the user successfully removed.
//
// The registry loader records the copies on disk at the instant of every load, so
// the assertion is about what the removal's OWN reload saw rather than a
// restatement of where the call sits. Moving the mark back into the reclaim leaves
// the removal's reload staring at a `.removing-` copy and fails this test.
func TestInstances_RemoveMarksTheAsideCommittedBeforeTheReload(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")

	var (
		mu     sync.Mutex
		atLoad [][]string
	)
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		mu.Lock()
		atLoad = append(atLoad, asideCopyNames(f))
		mu.Unlock()
		opts := append(
			testProbeRegistryOptions(f.stateDir, f.store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(f.tomlPath),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, f.store, err
	}
	replacement := hubcore.NewProviderRegistry(loadFn)
	f.ctl.reg = replacement
	f.ctl.auth.reg = replacement
	// The priming load runs with only the record on disk, so its snapshot has no
	// copy; the removal's reload is the load this test is about.
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	sawCopy := false
	for _, copies := range atLoad {
		for _, name := range copies {
			sawCopy = true
			if strings.Contains(name, oauthAsideMarker) {
				t.Fatalf("a load saw %s still in flight; the removal must mark the copy committed before the reload", name)
			}
			if !strings.Contains(name, oauthCommittedMarker) {
				t.Fatalf("a load saw %s, want a committed copy", name)
			}
		}
	}
	if !sawCopy {
		t.Fatal("no load observed a copy on disk, so the ordering this test pins was never exercised")
	}
}

// TestInstances_RemoveRestoresTheRecordWhenTheReloadFailsAgainstACommittedCopy:
// the commit mark now lands before the reload, so a reload that fails rolls back
// a COMMITTED copy. restoreFailedRemoval renames whatever path it is handed back
// to the record path, so the rollback has to be handed the committed name. This
// drives the reload failure for a credential-only instance and asserts the record
// comes back under its own name with no aside left behind.
func TestInstances_RemoveRestoresTheRecordWhenTheReloadFailsAgainstACommittedCopy(t *testing.T) {
	// Load 1 is the fixture's own, load 2 is seedOAuthRecord's; the removal's
	// reload is load 3, and its retry is load 4.
	f := newFlakyReloadFixture(t, "", func(load int) bool { return load == 3 })
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("fixture: openai-codex = %+v (ok = %v), want a credential-only instance", inst, ok)
	}
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	before, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", record, err)
	}

	err = f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil || !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the removal reported as rolled back", err)
	}
	after, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the record was not restored from the committed copy: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("restored bytes = %q, want the original %q", after, before)
	}
	for _, name := range authDirEntries(t, f) {
		if _, _, aside := oauthAsideInstance(name); aside {
			t.Fatalf("the rollback left %s, want the committed copy renamed back to the record path", name)
		}
	}
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("openai-codex = %+v (ok = %v), want the instance back on its record after the retry reload", inst, ok)
	}
}

// TestInstances_RemoveAMarkFailureRollsBackTheRemoval: the commit mark is not
// optional cleanup. A copy that cannot be renamed to its committed shape at the
// commit point must fail the removal and roll it back the way a failed reload
// does - providers.toml still carrying the instance, the stored key and the
// record back under their own names - and must NEVER come back as a persisted
// removal. Otherwise a removal the caller was told had stood could leave an
// in-flight copy that the next startup restores, resurrecting the credential the
// user removed. (This replaces an earlier test that pinned the old, swallowed
// failure, where the removal stood and the reclaim reported the unset mark.)
//
// The rename failure is driven the way the disk drives it: an existing directory
// at the committed name, which os.Rename refuses to replace with a file.
func TestInstances_RemoveAMarkFailureRollsBackTheRemoval(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 321, time.UTC)
	f.ctl.auth.now = func() time.Time { return fixed }
	record := authopenai.AuthFilePath(f.stateDir, "work")
	stamp := strconv.FormatInt(fixed.UnixNano(), 10)
	committed := record + oauthCommittedMarker + stamp
	if err := os.Mkdir(committed, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", committed, err)
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the commit-mark failure reported")
	}
	if _, persisted := errors.AsType[removePersistedError](err); persisted {
		t.Fatalf("Remove = %v (%T), want a plain failure, never a persisted removal beside a copy startup can restore", err, err)
	}
	if !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the removal reported as rolled back", err)
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record was not put back at %s: %v", record, statErr)
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("stored key = %q, want the rollback to have restored it", v)
	}
	authoredEntry(t, f.tomlPath, "work")
	for _, name := range authDirEntries(t, f) {
		if _, committed, aside := oauthAsideInstance(name); aside && !committed {
			t.Fatalf("an in-flight copy %s survived the rollback, so startup would restore it", name)
		}
	}
}

// TestInstances_RemoveAMarkFailureAndAFailedRollbackLeavesStartupToRecover: the
// rollback's own rename-back can fail too, and then the in-flight copy is the
// instance's only record. The removal must still be reported as FAILED - never as
// one that stood - so startup recovery (restoreUncommittedOAuthAsides) is what
// puts the record back. A persisted removal beside a copy startup restores is
// exactly the resurrection this design forbids.
func TestInstances_RemoveAMarkFailureAndAFailedRollbackLeavesStartupToRecover(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 654, time.UTC)
	f.ctl.auth.now = func() time.Time { return fixed }
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	stamp := strconv.FormatInt(fixed.UnixNano(), 10)
	inflight := record + oauthAsideMarker + stamp
	committed := record + oauthCommittedMarker + stamp
	if err := os.Mkdir(committed, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", committed, err)
	}
	// After the credential cleanup, occupy the record path with a non-empty
	// directory: the rollback's rename-back cannot replace it, the way a genuine
	// disk refusal would. It is a directory so the registry's instance scan skips
	// it, and the test clears it before startup recovery runs.
	realDeleteAuth := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(stateDir, name string) (bool, error) {
		removed, err := realDeleteAuth(stateDir, name)
		obstacle := authopenai.AuthFilePath(stateDir, name)
		if mkErr := os.Mkdir(obstacle, 0o700); mkErr != nil {
			t.Fatalf("Mkdir(%s): %v", obstacle, mkErr)
		}
		if wErr := os.WriteFile(filepath.Join(obstacle, "obstacle"), []byte("in the way"), 0o600); wErr != nil {
			t.Fatalf("WriteFile: %v", wErr)
		}
		return removed, err
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil {
		t.Fatal("Remove = nil, want the commit-mark failure reported")
	}
	if _, persisted := errors.AsType[removePersistedError](err); persisted {
		t.Fatalf("Remove = %v (%T), want a plain failure, never a persisted removal", err, err)
	}
	if !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the removal reported as rolled back", err)
	}
	if _, statErr := os.Lstat(inflight); statErr != nil {
		t.Fatalf("the in-flight copy %s is gone (%v); the failed rollback must have left it for startup", inflight, statErr)
	}

	// Clear the obstacle the way the disk would once the refusal passes, then let
	// startup recover the record.
	if rerr := os.RemoveAll(record); rerr != nil {
		t.Fatalf("RemoveAll(%s): %v", record, rerr)
	}
	restored, rerr := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup did not put the in-flight record back")
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record was not restored to %s: %v", record, statErr)
	}
}

// TestInstances_RemoveAReloadFailureReturnsACommittedCopyToInFlight: the commit
// mark can succeed and the removal still fail (a reload that cannot resolve the
// config it wrote). When the rollback also cannot rename the committed copy back
// to the record path, the copy must be returned to its IN-FLIGHT shape, not left
// committed: a committed copy means the removal STOOD, and for a credential-only
// instance - whose name providers.toml never carries - the startup sweep would
// otherwise delete the instance's only record on the next start. The shape on
// disk is asserted directly, then startup recovery is run and the record must
// come back at its own path.
//
// The fixture is a credential-only Codex instance (no providers.toml), and the
// rename-back is refused the way the disk refuses it: a non-empty directory
// occupying the record path.
func TestInstances_RemoveAReloadFailureReturnsACommittedCopyToInFlight(t *testing.T) {
	// Load 1 primes the fixture, load 2 is seedOAuthRecord's, load 3 is the
	// removal's reload (the one made to fail), and load 4 is the rollback's retry.
	f := newFlakyReloadFixture(t, "", func(load int) bool { return load == 3 })
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 987, time.UTC)
	f.ctl.auth.now = func() time.Time { return fixed }
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	dir := filepath.Dir(record)
	stamp := strconv.FormatInt(fixed.UnixNano(), 10)
	inflight := "openai-codex.json" + oauthAsideMarker + stamp
	committed := record + oauthCommittedMarker + stamp
	// After the credential cleanup, occupy the record path with a non-empty
	// directory so the rollback's rename-back cannot replace it.
	realDeleteAuth := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(stateDir, name string) (bool, error) {
		removed, err := realDeleteAuth(stateDir, name)
		obstacle := authopenai.AuthFilePath(stateDir, name)
		if mkErr := os.Mkdir(obstacle, 0o700); mkErr != nil {
			t.Fatalf("Mkdir(%s): %v", obstacle, mkErr)
		}
		if wErr := os.WriteFile(filepath.Join(obstacle, "obstacle"), []byte("in the way"), 0o600); wErr != nil {
			t.Fatalf("WriteFile: %v", wErr)
		}
		return removed, err
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil {
		t.Fatal("Remove = nil, want the reload failure reported")
	}
	if _, persisted := errors.AsType[removePersistedError](err); persisted {
		t.Fatalf("Remove = %v (%T), want a plain failure", err, err)
	}
	if !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the removal reported as rolled back", err)
	}
	if !strings.Contains(err.Error(), oauthAsideMarker) {
		t.Fatalf("Remove = %v, want the returned-to-in-flight fallback named", err)
	}
	// The on-disk shape says the removal did not stand.
	if _, statErr := os.Lstat(filepath.Join(dir, inflight)); statErr != nil {
		t.Fatalf("the copy is not in its in-flight shape at %s: %v", inflight, statErr)
	}
	if _, statErr := os.Lstat(committed); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a committed copy survives at %s (Lstat = %v), want the copy returned to in-flight", committed, statErr)
	}

	// Clear the obstacle as the disk would once the refusal passes, then let
	// startup recover. Were the copy still committed, the no-config sweep would
	// delete it instead.
	if rerr := os.RemoveAll(record); rerr != nil {
		t.Fatalf("RemoveAll(%s): %v", record, rerr)
	}
	restored, rerr := restoreUncommittedOAuthAsides(f.ctl.reg, f.stateDir, f.tomlPath)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup did not restore the in-flight copy the rollback returned")
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record was not restored to %s: %v", record, statErr)
	}
}

// TestInstances_RemoveReportsADeleteFailureAsACredentialStillOnDisk: a copy the
// sweep commits but cannot delete is described as a credential still on disk,
// with the wording the removal's own message uses - the caller is the only one
// left who can delete it. A commit-rename failure no longer reaches the sweep:
// change 1 rolls the removal back and reports it as a failed removal instead
// (TestInstances_RemoveAMarkFailureRollsBackTheRemoval).
func TestInstances_RemoveReportsADeleteFailureAsACredentialStillOnDisk(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil || !strings.Contains(err.Error(), "still on disk") {
		t.Fatalf("Remove = %v, want the copy reported as a credential still on disk", err)
	}
	if !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("Remove = %v, want the delete failure named", err)
	}
	if strings.Contains(err.Error(), "could not be marked committed") {
		t.Fatalf("Remove = %v, want the successful commit mark not reported as a failure", err)
	}
	if _, persisted := errors.AsType[removePersistedError](err); !persisted {
		t.Fatalf("Remove = %v (%T), want a removePersistedError so the removal is announced", err, err)
	}
	left := authDirEntries(t, f)
	if len(left) != 1 || !strings.HasPrefix(left[0], "openai-codex.json"+oauthCommittedMarker) {
		t.Fatalf("the auth directory holds %v, want the one committed copy the removal could not delete", left)
	}
}

// TestInstances_EditRenameCarriesAnInFlightOAuthCopy: a failed removal can leave
// an instance's only record as an in-flight copy under its own name
// (restoreFailedRemoval's rename-back failed). A rename used to carry only the
// live record, so once providers.toml named only the new instance the copy was
// stranded: startup recovery no longer recognized the old name and the
// credential was unrecoverable. The rename must carry it, and - because the old
// record path is free - promote the newest copy to that path first so the carry
// reads it as the record and the credential is usable under the new name at
// once. No in-flight copy may remain filed under the old name.
func TestInstances_EditRenameCarriesAnInFlightOAuthCopy(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	record := authopenai.AuthFilePath(f.stateDir, "work")
	stamp := "1757000000000000000"
	aside := record + oauthAsideMarker + stamp
	if err := os.Rename(record, aside); err != nil {
		t.Fatalf("Rename(%s -> %s): %v", record, aside, err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	// Premise: the in-flight copy is the instance's only record.
	if _, err := os.Lstat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: %s still exists (Lstat = %v), want the record only as an aside", record, err)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}
	moved, err := authopenai.LoadAuth(f.stateDir, "personal")
	if err != nil {
		t.Fatalf("the credential is not usable under the new name: %v", err)
	}
	if moved.AccessToken != "access-work" || moved.Provider != "personal" {
		t.Fatalf("moved record = %+v, want the carried record with Provider personal", moved)
	}
	for _, name := range authDirEntries(t, f) {
		inst, _, aside := oauthAsideInstance(name)
		if !aside || inst != "work" {
			continue
		}
		t.Fatalf("an in-flight copy %s is still filed under the old name, stranding it", name)
	}
}

// TestInstances_EditRenameReportsAnOAuthCopyItCouldNotCarry: a rename that cannot
// carry an in-flight copy to the new name still stands (providers.toml already
// names the new instance), so the failure is reported the way the other carry
// failures are - through Edit's renamePersistedError, naming the copy the caller
// must deal with. The carry is refused the way the disk refuses it: an existing
// directory at the destination copy name. A live record keeps the old record path
// occupied, so the copy is carried rather than promoted.
func TestInstances_EditRenameReportsAnOAuthCopyItCouldNotCarry(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	dir := filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
	stamp := "1757000000000000000"
	copyName := "work.json" + oauthAsideMarker + stamp
	if err := os.WriteFile(filepath.Join(dir, copyName), []byte("stray copy\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", copyName, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "personal.json"+oauthAsideMarker+stamp), 0o700); err != nil {
		t.Fatalf("Mkdir(destination): %v", err)
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the un-carried copy reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit = %v (%T), want a renamePersistedError", err, err)
	}
	if !strings.Contains(err.Error(), copyName) {
		t.Fatalf("Edit = %v, want it to name the copy %s left behind", err, copyName)
	}
	authoredEntry(t, f.tomlPath, "personal")
}
