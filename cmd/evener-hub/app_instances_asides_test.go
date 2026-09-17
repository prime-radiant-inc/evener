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

// TestOAuthAsideInstanceKnowsBothMarkers: every aside shape parses, each reports
// whether the removal that made it had stood and whether that removal was
// config-backed. A name whose stamp is not all digits, or whose record part does
// not end in .json, is not a copy - including a record for an instance whose own
// name holds a marker.
func TestOAuthAsideInstanceKnowsBothMarkers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		instance     string
		committed    bool
		configBacked bool
		aside        bool
	}{
		{"work.json.removing-5", "work", false, false, true},
		{"work.json.removed-5", "work", true, false, true},
		{"work.json.removing-cfg-5", "work", false, true, true},
		{"work.json.removed-cfg-5", "work", true, true, true},
		{"x.removing-1.json", "", false, false, false},
		{"x.removed-1.json", "", false, false, false},
		{"notes.txt", "", false, false, false},
		{"x.removed-1.json.removing-5", "x.removed-1", false, false, true},
		{"x.removing-1.json.removed-5", "x.removing-1", true, false, true},
		{"x.removing-cfg-1.json.removing-5", "x.removing-cfg-1", false, false, true},
		{"work.json.removing-abc", "", false, false, false},
		{"work.json.removed-", "", false, false, false},
		{"work.json.removing-cfg-abc", "", false, false, false},
	} {
		inst, committed, configBacked, aside := oauthAsideInstance(tc.name)
		if inst != tc.instance || committed != tc.committed || configBacked != tc.configBacked || aside != tc.aside {
			t.Fatalf("oauthAsideInstance(%q) = (%q, %v, %v, %v), want (%q, %v, %v, %v)", tc.name, inst, committed, configBacked, aside, tc.instance, tc.committed, tc.configBacked, tc.aside)
		}
	}
}

// committedAsideStamps returns the stamps of every COMMITTED copy filed for name
// in the fixture's auth directory, ascending.
func committedAsideStamps(t *testing.T, f *instancesFixture, name string) []int64 {
	t.Helper()
	var out []int64
	for _, entry := range authDirEntries(t, f) {
		inst, committed, _, aside := oauthAsideInstance(entry)
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

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
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
// conservative rule.
//
// A CREDENTIAL-ONLY committed copy with no config entry and a free record path
// could be a failed rollback's only surviving credential, so its delete is
// REPORTED rather than silent (change 3). A config-backed one is plain debris of
// a standing authored removal and is deleted without a report.
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
	cfgOrphan := "gone.json" + oauthConfigCommittedMarker + "1757000000000000002"
	for name, body := range map[string]string{carried: "the removal that stood\n", orphan: "an orphaned removal\n", cfgOrphan: "a standing authored removal\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if err == nil || !strings.Contains(err.Error(), "deleted the committed credential-only copy "+orphan) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the deleted credential-only committed copy %s reported", restored, err, orphan)
	}
	if strings.Contains(err.Error(), cfgOrphan) {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want a config-backed delete not reported as a lone credential", err)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want a committed copy never put back")
	}
	for _, name := range []string{orphan, cfgOrphan} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the committed copy %s was not swept (Lstat = %v), want it deleted with no config entry", name, err)
		}
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
	// with no removal left to collect it. This copy is credential-only with a free
	// record path, so the delete is reported rather than silent.
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if rerr == nil || !strings.Contains(rerr.Error(), "deleted the committed credential-only copy "+left[0]) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the swept credential-only copy %s reported", restored, rerr, left[0])
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
	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
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

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
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
		if _, _, _, aside := oauthAsideInstance(e.Name()); aside {
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
		if _, _, _, aside := oauthAsideInstance(name); aside {
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
	// "work" is authored, so its removal is the config-backed kind and the
	// commit destination carries the cfg marker: the obstacle must sit there.
	committed := record + oauthConfigCommittedMarker + stamp
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
		if _, committed, _, aside := oauthAsideInstance(name); aside && !committed {
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
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
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
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
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
		inst, _, _, aside := oauthAsideInstance(name)
		if !aside || inst != "work" {
			continue
		}
		t.Fatalf("an in-flight copy %s is still filed under the old name, stranding it", name)
	}
}

// TestInstances_EditRenameReportsAnOAuthCopyItCouldNotCarry: a rename that cannot
// carry an in-flight copy to the new name still stands (providers.toml already
// names the new instance), so the failure is reported the way the other carry
// failures are - through Edit's renamePersistedError. The copy must NOT be left in
// a shape startup recovery resolves forward and deletes: the rename already wrote
// providers.toml with the new name, so a CONFIG-BACKED copy still filed under the
// old name would be resolved forward on the next start and swept, destroying what
// may be the renamed instance's only credential. The carry re-files it to the
// CREDENTIAL-ONLY in-flight shape instead, which recovery restores while the
// record path is free - the alternative is deletion without the user asking.
//
// The carry is refused the way the search refuses it: the copy's stamp is
// MaxInt64 and the new name already holds a copy at that same stamp, so no fresh
// stamp can be stepped to. A live record keeps the old record path occupied, so
// the copy is carried rather than promoted. Startup recovery then runs and the
// bytes must come back rather than be deleted.
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
	// The stamp no successor can pass, so freeAsideName cannot offer the carry a
	// fresh name once the new name holds a copy at the same stamp.
	const stamp = "9223372036854775807"
	source := "work.json" + oauthConfigAsideMarker + stamp
	const content = "the renamed instance's only credential copy\n"
	if err := os.WriteFile(filepath.Join(dir, source), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", source, err)
	}
	// The obstacle occupies the only candidate the fresh-stamp search can offer,
	// so the carry cannot land rather than stepping past it.
	if err := os.Mkdir(filepath.Join(dir, "personal.json"+oauthConfigAsideMarker+stamp), 0o700); err != nil {
		t.Fatalf("Mkdir(destination): %v", err)
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the un-carried copy reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit = %v (%T), want a renamePersistedError", err, err)
	}
	remark := "work.json" + oauthAsideMarker + stamp
	if !strings.Contains(err.Error(), remark) {
		t.Fatalf("Edit = %v, want it to name the copy re-filed as %s", err, remark)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, source)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the config-backed copy %s is still on disk (%v), want it re-filed to the credential-only shape", source, statErr)
	}
	authoredEntry(t, f.tomlPath, "personal")

	// Startup recovery reads the credential-only in-flight shape and restores the
	// bytes under the old name's record path (the rename freed it), rather than
	// resolving a config-backed copy forward and sweeping it away.
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup did not restore the re-filed copy; the rename destroyed the renamed instance's only credential")
	}
	got, rerr := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "work"))
	if rerr != nil {
		t.Fatalf("the re-filed bytes were not restored: %v", rerr)
	}
	if string(got) != content {
		t.Fatalf("restored bytes = %q, want %q", got, content)
	}
}

// TestRestoreUncommittedOAuthAsidesPutsBackAConfigBackedCopyTheConfigStillCarries:
// the other half of the kind rule. A CONFIG-BACKED in-flight copy whose name
// providers.toml still carries is a removal that never reached its providers.toml
// write - the config is the durable evidence that it did not - so it is put back,
// bytes intact, when the record path is free.
func TestRestoreUncommittedOAuthAsidesPutsBackAConfigBackedCopyTheConfigStillCarries(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	record := authopenai.AuthFilePath(f.stateDir, "work")
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	original, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	aside := record + oauthConfigAsideMarker + "1757000000000000000"
	if err := os.Rename(record, aside); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the config-backed copy the config still carries put back")
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the record was not put back: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("restored bytes = %q, want the original %q", got, original)
	}
	if _, err := os.Lstat(aside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy is still on disk (Lstat = %v), want it moved", err)
	}
}

// TestRestoreUncommittedOAuthAsidesLeavesACopyWhoseCommittedNameIsTaken: the
// forward resolution of a config-backed in-flight copy must never overwrite a
// file already filed under the copy's committed name. A partial prior rename can
// leave both paths present, and POSIX rename(2) would silently replace the bytes
// already there - a copy of the same instance's record, or another copy's only
// surviving credential. The promotion refuses the taken destination
// (renameNoReplace), leaves BOTH the in-flight copy and the committed bytes
// exactly where they are, and reports both paths through the pass's problems
// path. The committed copy must not be swept either: deleting it here would lose
// precisely what the no-replace move protected.
func TestRestoreUncommittedOAuthAsidesLeavesACopyWhoseCommittedNameIsTaken(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	dir := filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const stamp = "1757000000000000000"
	inflight := "gone.json" + oauthConfigAsideMarker + stamp
	committedName := "gone.json" + oauthConfigCommittedMarker + stamp
	const committedBytes = "the bytes already at the committed name\n"
	const inflightBytes = "the config-backed copy still in flight\n"
	if err := os.WriteFile(filepath.Join(dir, committedName), []byte(committedBytes), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", committedName, err)
	}
	if err := os.WriteFile(filepath.Join(dir, inflight), []byte(inflightBytes), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", inflight, err)
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if err == nil || !strings.Contains(err.Error(), inflight) || !strings.Contains(err.Error(), committedName) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the taken committed name %s reported with the copy %s", restored, err, committedName, inflight)
	}
	if !strings.Contains(err.Error(), "stayed in flight") {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the copy reported as stayed in flight", err)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want the config-backed copy resolved forward rather than put back")
	}
	gotCommitted, rerr := os.ReadFile(filepath.Join(dir, committedName))
	if rerr != nil || string(gotCommitted) != committedBytes {
		t.Fatalf("the committed file = %q (%v), want its bytes untouched by the refused promotion", gotCommitted, rerr)
	}
	gotInflight, rerr := os.ReadFile(filepath.Join(dir, inflight))
	if rerr != nil || string(gotInflight) != inflightBytes {
		t.Fatalf("the in-flight copy = %q (%v), want it left in flight for a later pass", gotInflight, rerr)
	}
	if _, rerr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "gone")); !errors.Is(rerr, os.ErrNotExist) {
		t.Fatalf("the config-backed copy was restored at its record path (Lstat = %v), want the standing removal left alone", rerr)
	}
}

// TestRestoreUncommittedOAuthAsidesRecoversCredentialOnlyCopiesWhenTheConfigCannotBeRead:
// a credential-only in-flight copy's recovery does not need providers.toml - the
// record file was the whole of what made the instance exist, and the copy goes
// back whenever its record path is free. An unrelated config failure must not
// strand that credential. The pass still completes the credential-only half,
// defers every config-dependent copy untouched (here, the committed-copy sweep),
// and reports the config failure so a partial pass never reads as clean.
func TestRestoreUncommittedOAuthAsidesRecoversCredentialOnlyCopiesWhenTheConfigCannotBeRead(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// An unreadable providers.toml: a directory stands where the file was, so
	// ReadConfigFile fails rather than returning an empty layer.
	if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", f.tomlPath, err)
	}
	dir := filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const stamp = "1757000000000000000"
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	inflight := filepath.Join(dir, "openai-codex.json"+oauthAsideMarker+stamp)
	const credentialBytes = "the only credential the instance ever had\n"
	if err := os.WriteFile(inflight, []byte(credentialBytes), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", inflight, err)
	}
	// A committed copy the config would not carry: the sweep that deletes it needs
	// the config, so it must not run in a pass without one.
	swept := "retired.json" + oauthCommittedMarker + stamp
	if err := os.WriteFile(filepath.Join(dir, swept), []byte("a standing removal's copy\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", swept, err)
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if err == nil || !strings.Contains(err.Error(), f.tomlPath) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the config failure naming %s reported", restored, err, f.tomlPath)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the credential-only copy put back without the config")
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || string(got) != credentialBytes {
		t.Fatalf("the record = %q (%v), want the credential-only copy put back", got, rerr)
	}
	if _, statErr := os.Lstat(inflight); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the in-flight copy is still on disk (Lstat = %v), want it moved", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, swept)); statErr != nil {
		t.Fatalf("the committed copy %s was swept (Lstat = %v), want the config-dependent sweep deferred", swept, statErr)
	}
}

// TestInstances_RemovalRecordsTheAsideKind: the kind is written into the aside
// name by the removal's own rename, so it is durable with no extra file. A
// removal of an AUTHORED instance (providers.toml carried the name) sets aside a
// config-backed copy; one of a credential-only instance (no entry) does not. The
// delete is refused so each committed copy stays on disk to be inspected.
func TestInstances_RemovalRecordsTheAsideKind(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth(work): %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}); err == nil {
		t.Fatal("Remove(work) = nil, want the sweep failure reported")
	}
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("Remove(openai-codex) = nil, want the sweep failure reported")
	}

	var sawCfg, sawPlain bool
	for _, name := range authDirEntries(t, f) {
		inst, committed, configBacked, aside := oauthAsideInstance(name)
		if !aside || !committed {
			continue
		}
		switch {
		case inst == "work" && configBacked:
			sawCfg = true
		case inst == "openai-codex" && !configBacked:
			sawPlain = true
		case inst == "work":
			t.Fatalf("the authored removal's copy %s is not config-backed", name)
		case inst == "openai-codex":
			t.Fatalf("the credential-only removal's copy %s is config-backed", name)
		}
	}
	if !sawCfg {
		t.Fatal("the authored removal did not record a config-backed aside")
	}
	if !sawPlain {
		t.Fatal("the credential-only removal did not record a credential-only aside")
	}
}

// TestInstances_EditRenameKeepsAnUnparseablePromotedRecordRecoverable: the rename
// promotes the newest in-flight copy to the old record path so moveCredentials
// can read it. When that read refuses the bytes (an unparseable record), leaving
// them at the canonical old-name path would strand them - no reader looks there
// once providers.toml names the new instance, and startup recovery does not
// recognize a plain record as an aside. The bytes must move back under a
// recovery-recognized aside for the NEW name (kind and stamp preserved), and the
// carry problem must be reported the way the other carry failures are.
func TestInstances_EditRenameKeepsAnUnparseablePromotedRecordRecoverable(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dir := filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	stamp := "1757000000000000000"
	aside := "work.json" + oauthConfigAsideMarker + stamp
	const content = "a record the hub cannot parse\n"
	if err := os.WriteFile(filepath.Join(dir, aside), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", aside, err)
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the unparseable promoted record reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit = %v (%T), want a renamePersistedError", err, err)
	}
	newAside := "personal.json" + oauthConfigAsideMarker + stamp
	if !strings.Contains(err.Error(), newAside) {
		t.Fatalf("Edit = %v, want it to name the recovery-recognized aside %s", err, newAside)
	}
	got, rerr := os.ReadFile(filepath.Join(dir, newAside))
	if rerr != nil {
		t.Fatalf("the bytes are not under the recovery-recognized aside %s: %v", newAside, rerr)
	}
	if string(got) != content {
		t.Fatalf("the carried bytes = %q, want %q", got, content)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "work")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the unreadable record was left at the old canonical path (Lstat = %v)", statErr)
	}
	authoredEntry(t, f.tomlPath, "personal")

	// Startup recovery reads that name and puts the bytes back for the renamed
	// instance, so the credential is reachable again.
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup did not restore the renamed instance's carried record")
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "personal")); statErr != nil {
		t.Fatalf("the carried record was not restored under the new name: %v", statErr)
	}
}

// TestInstances_EditRenameCarriesAnOAuthCopyWithoutClobberingATakenName: the
// carry names the new instance's aside from the OLD copy's stamp, so a copy
// already filed under the new name at that stamp is a real collision. os.Rename
// replaces an existing destination, which would silently destroy those stale
// bytes; the carry must instead land the copy under a fresh stamp, leaving the
// taken destination exactly as it was. A live record keeps the old record path
// occupied, so the copy is carried rather than promoted.
func TestInstances_EditRenameCarriesAnOAuthCopyWithoutClobberingATakenName(t *testing.T) {
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
	const stamp = "1757000000000000000"
	const carried = "the renamed instance's live copy\n"
	const stale = "a stale copy already filed under the new name\n"
	if err := os.WriteFile(filepath.Join(dir, "work.json"+oauthAsideMarker+stamp), []byte(carried), 0o600); err != nil {
		t.Fatalf("WriteFile(source): %v", err)
	}
	taken := filepath.Join(dir, "personal.json"+oauthAsideMarker+stamp)
	if err := os.WriteFile(taken, []byte(stale), 0o600); err != nil {
		t.Fatalf("WriteFile(taken destination): %v", err)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}
	gotStale, err := os.ReadFile(taken)
	if err != nil {
		t.Fatalf("the taken destination was destroyed: %v", err)
	}
	if string(gotStale) != stale {
		t.Fatalf("taken destination bytes = %q, want the stale copy left untouched", gotStale)
	}
	fresh := filepath.Join(dir, "personal.json"+oauthAsideMarker+"1757000000000000001")
	gotCarried, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatalf("the copy did not land under a fresh stamp %s: %v", filepath.Base(fresh), err)
	}
	if string(gotCarried) != carried {
		t.Fatalf("carried bytes = %q, want %q", gotCarried, carried)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, "work.json"+oauthAsideMarker+stamp)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the source copy was left under the old name (Lstat = %v)", statErr)
	}
}

// TestCommitOAuthAsideRefusesATakenCommittedPath: commitOAuthAside is the single
// rename the removal's commit mark and its reclaim both perform. It must never
// replace an existing file at the committed path - that file is another copy's
// bytes, and POSIX rename would silently destroy them. A taken destination fails
// the commit and leaves both the in-flight copy and the taken path untouched.
func TestCommitOAuthAsideRefusesATakenCommittedPath(t *testing.T) {
	dir := t.TempDir()
	inflight := filepath.Join(dir, "work.json"+oauthAsideMarker+"5")
	committed := filepath.Join(dir, "work.json"+oauthCommittedMarker+"5")
	if err := os.WriteFile(inflight, []byte("the in-flight copy\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(inflight): %v", err)
	}
	if err := os.WriteFile(committed, []byte("bytes already at the committed path\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(committed): %v", err)
	}

	got, err := commitOAuthAside(inflight)
	if err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("commitOAuthAside = (%q, %v), want an os.ErrExist refusal for the taken committed path", got, err)
	}
	if got != inflight {
		t.Fatalf("commitOAuthAside returned %q, want the in-flight path unchanged on failure", got)
	}
	if b, rerr := os.ReadFile(committed); rerr != nil || string(b) != "bytes already at the committed path\n" {
		t.Fatalf("committed path bytes = %q (%v), want the taken file left untouched", b, rerr)
	}
	if b, rerr := os.ReadFile(inflight); rerr != nil || string(b) != "the in-flight copy\n" {
		t.Fatalf("in-flight bytes = %q (%v), want the source left in place", b, rerr)
	}
}

// TestInstances_RollbackWriteFailureReloadsToMatchTheWrittenConfig: when a
// removal reaches its rollback but the rollback's providers.toml write fails, the
// file on disk is still the one the REMOVAL wrote - it no longer carries the
// instance. The registry must be reloaded over that file, or the hub keeps
// serving an instance the config no longer has. The reload is driven directly
// here: the providers.toml directory is made read-only so the rollback's write
// fails while the subsequent reload still reads the file.
func TestInstances_RollbackWriteFailureReloadsToMatchTheWrittenConfig(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, _, err := f.ctl.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, ok := before.Providers["work"]; !ok {
		t.Fatalf("fixture: before = %+v, want the authored work entry", before.Providers)
	}
	// The file the removal would have written: no work entry. The registry is not
	// reloaded, so it still lists work - the stale view the fix must repair.
	if err := registry.WriteConfigFile(f.tomlPath, &registry.Layer{}); err != nil {
		t.Fatalf("WriteConfigFile(removal output): %v", err)
	}
	if _, ok := f.ctl.reg.Get().Instance("work"); !ok {
		t.Fatal("fixture: the registry did not hold the pre-removal work instance")
	}
	tomlDir := filepath.Dir(f.tomlPath)
	if err := os.Chmod(tomlDir, 0o555); err != nil {
		t.Fatalf("Chmod(%s): %v", tomlDir, err)
	}
	defer func() {
		if err := os.Chmod(tomlDir, 0o700); err != nil {
			t.Fatalf("restore Chmod(%s): %v", tomlDir, err)
		}
	}()

	err = f.ctl.rollBackFailedRemoval(before, "work", "", false, "", true, errors.New("commit mark failed"))
	if err == nil {
		t.Fatal("rollBackFailedRemoval = nil, want the failed rollback reported")
	}
	if _, persisted := errors.AsType[removePersistedError](err); !persisted {
		t.Fatalf("rollBackFailedRemoval = %v (%T), want a removePersistedError so the removal is announced", err, err)
	}
	if !strings.Contains(err.Error(), "could not be written") {
		t.Fatalf("rollBackFailedRemoval = %v, want the failed rollback write named", err)
	}
	if _, ok := f.ctl.reg.Get().Instance("work"); ok {
		t.Fatalf("the registry still serves work after a failed rollback write; it must be reloaded over the config that stands (registry = %+v)", f.ctl.reg.Get().Instances())
	}
}

// TestInstances_RollbackWriteFailureReportsAFailedReloadToo: the reload the
// failed rollback now runs can itself fail. The caller must be told both that the
// rollback write failed and that the registry could not be reloaded, so nobody
// reads a half-repaired hub as healthy. providers.toml is replaced with a
// directory: the write and the reload both fail on it.
func TestInstances_RollbackWriteFailureReportsAFailedReloadToo(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, _, err := f.ctl.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.Remove(f.tomlPath); err != nil {
		t.Fatalf("Remove(%s): %v", f.tomlPath, err)
	}
	if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", f.tomlPath, err)
	}
	if err := os.WriteFile(filepath.Join(f.tomlPath, "obstacle"), []byte("in the way"), 0o600); err != nil {
		t.Fatalf("WriteFile(obstacle): %v", err)
	}

	err = f.ctl.rollBackFailedRemoval(before, "work", "", false, "", true, errors.New("commit mark failed"))
	if err == nil {
		t.Fatal("rollBackFailedRemoval = nil, want the failed rollback reported")
	}
	if _, persisted := errors.AsType[removePersistedError](err); !persisted {
		t.Fatalf("rollBackFailedRemoval = %v (%T), want a removePersistedError", err, err)
	}
	if !strings.Contains(err.Error(), "could not be written") {
		t.Fatalf("rollBackFailedRemoval = %v, want the failed rollback write named", err)
	}
	if !strings.Contains(err.Error(), "could not be reloaded") {
		t.Fatalf("rollBackFailedRemoval = %v, want the failed reload named beside the failed write", err)
	}
}

// TestInstances_RemovalRecordsTheDefaultPointerAsConfigBacked: a removal that
// only cleared `default` still writes providers.toml, so a crash after that write
// but before the commit mark would leave an in-flight copy startup restores -
// resurrecting the removed instance. The aside's kind must therefore mean "this
// removal changed providers.toml": an authored entry OR a `default` pointer
// naming the instance. This drives the default-only case and asserts the copy is
// config-backed.
func TestInstances_RemovalRecordsTheDefaultPointerAsConfigBacked(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("default = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	inst, ok := f.ctl.reg.Get().Instance("openai-codex")
	if !ok || !inst.Default {
		t.Fatalf("fixture: openai-codex = %+v (ok = %v), want the default implicit instance", inst, ok)
	}
	// The sweep cannot delete, so the committed copy stays on disk to inspect.
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("Remove = nil, want the sweep failure reported")
	}
	var sawCfg bool
	for _, name := range authDirEntries(t, f) {
		inst, committed, configBacked, aside := oauthAsideInstance(name)
		if !aside || !committed || inst != "openai-codex" {
			continue
		}
		if !configBacked {
			t.Fatalf("the default-only removal's copy %s is credential-only, so startup would restore the removed instance", name)
		}
		sawCfg = true
	}
	if !sawCfg {
		t.Fatal("the default-only removal recorded no config-backed aside")
	}
	l, exists, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile = (%v, %v, %v), want the written config", l, exists, err)
	}
	if l.Default != "" {
		t.Fatalf("default = %q, want the removal to have cleared the pointer", l.Default)
	}
}

// TestRestoreUncommittedOAuthAsidesPutsBackADefaultNamedConfigBackedCopy: the
// recovery half of the kind rule. A config-backed in-flight copy whose name a
// `default` pointer still names is a removal that never reached its write - the
// config is the durable evidence - so it is put back, exactly as when an authored
// entry carries the name. Before the fix, recovery asked only for an authored
// entry, resolved the copy forward, and the sweep deleted the record.
func TestRestoreUncommittedOAuthAsidesPutsBackADefaultNamedConfigBackedCopy(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("default = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	original, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	aside := record + oauthConfigAsideMarker + "1757000000000000000"
	if err := os.Rename(record, aside); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the default-named config-backed copy put back")
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the record was not put back: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("restored bytes = %q, want the original %q", got, original)
	}
	if _, err := os.Lstat(aside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy is still on disk (Lstat = %v), want it moved", err)
	}
}
