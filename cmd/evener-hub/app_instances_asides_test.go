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

// The contract these tests pin is the CRASH-POINT MATRIX of one instance
// mutation's records: what a removal or a rename leaves on disk at each point it
// can stop, and what startup recovery makes of it. The records are one write-
// ahead intent (<inst>.json.intent-<stamp>, holding op/inst/kind/phase) plus the
// parked copies (<inst>.json.removing-<stamp>, whose names carry no
// classification at all), and the rules they are judged by live in
// restoreUncommittedOAuthAsides.

// ---- fixture helpers ----

// oauthDir is the directory every record and parked copy lives in.
func oauthDir(f *instancesFixture) string {
	return filepath.Dir(authopenai.AuthFilePath(f.stateDir, "instance"))
}

// stampedRecordPath returns the path of a stamped record name, whether or not
// its stamp parses: the corpus includes names no writer would produce.
func stampedRecordPath(f *instancesFixture, record, marker, stamp string) string {
	if n, err := strconv.ParseInt(stamp, 10, 64); err == nil {
		return filepath.Join(oauthDir(f), stampedName(record, marker, n))
	}
	return filepath.Join(oauthDir(f), record+marker+stamp)
}

// intentAt writes one record under the given name-marker the way a mutation
// leaves it, and returns its path. Nothing here validates the record: the point
// of several tests is what recovery makes of one it cannot read.
func intentAt(t *testing.T, f *instancesFixture, inst string, marker string, i oauthIntent, stamp string) string {
	t.Helper()
	if err := os.MkdirAll(oauthDir(f), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := stampedRecordPath(f, inst+".json", marker, stamp)
	if err := os.WriteFile(path, i.encode(), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

// removalAt writes a removal's record at the name-marker one crash point leaves:
// oauthLandedMarker when the removal had passed its commit point (landed), and
// the in-flight name otherwise.
func removalAt(t *testing.T, f *instancesFixture, inst string, configBacked, landed bool, stamp string) string {
	t.Helper()
	marker := oauthIntentMarker
	if landed {
		marker = oauthLandedMarker
	}
	return intentAt(t, f, inst, marker, removalIntent(inst, configBacked), stamp)
}

// renameAt writes a rename's record. A rename carries no commit marker: its
// landing is read from the config, so the record is always in flight.
func renameAt(t *testing.T, f *instancesFixture, oldName, newName, stamp string) string {
	t.Helper()
	return intentAt(t, f, oldName, oauthIntentMarker, renameIntent(oldName, newName), stamp)
}

// parkedAt writes a parked copy with the given bytes and returns its path.
func parkedAt(t *testing.T, f *instancesFixture, inst, stamp, content string) string {
	t.Helper()
	if err := os.MkdirAll(oauthDir(f), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := stampedRecordPath(f, inst+".json", oauthAsideMarker, stamp)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

// intentNames lists the records filed in the fixture's auth directory, under
// either name. A directory is not a record - the hub's own classification skips
// one, and a test that puts a directory where a record would go is building an
// obstacle - so directories are skipped here too.
func intentNames(t *testing.T, f *instancesFixture) []string {
	t.Helper()
	entries, err := os.ReadDir(oauthDir(f))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", oauthDir(f), err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, _, _, ok := parseOAuthIntent(e.Name()); ok {
			out = append(out, e.Name())
		}
	}
	return out
}

// parkedNames lists the parked copies filed for inst, in directory order. A
// directory is not a copy: the hub parks regular files, and a test that puts a
// directory where a copy would go is building an obstacle, not a copy.
func parkedNames(t *testing.T, f *instancesFixture, inst string) []string {
	t.Helper()
	entries, err := os.ReadDir(oauthDir(f))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", oauthDir(f), err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if who, _, ok := parseOAuthAside(e.Name()); ok && who == inst {
			out = append(out, e.Name())
		}
	}
	return out
}

// parkedStamps returns the stamps of every parked copy filed for name,
// ascending. A copy whose stamp cannot be ordered ends up as -1.
func parkedStamps(t *testing.T, f *instancesFixture, name string) []int64 {
	t.Helper()
	var out []int64
	for _, entry := range parkedNames(t, f, name) {
		_, stampText, _ := parseOAuthAside(entry)
		s, err := strconv.ParseInt(stampText, 10, 64)
		if err != nil {
			s = -1
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// readIntent reads one intent record back.
func readIntent(t *testing.T, path string) oauthIntent {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	i, err := parseOAuthIntentRecord(raw, path)
	if err != nil {
		t.Fatalf("parseOAuthIntentRecord(%s): %v", path, err)
	}
	return i
}

// recordIsLanded reports whether the record at path is filed under the landed
// name - the marker a mutation's commit point renames it to.
func recordIsLanded(t *testing.T, path string) bool {
	t.Helper()
	_, _, landed, ok := parseOAuthIntent(filepath.Base(path))
	if !ok {
		t.Fatalf("parseOAuthIntent(%s) = not a record", path)
	}
	return landed
}

// recordedRemovalKind reports the kind the removal record filed for inst
// carries, and whether one was found at all.
func recordedRemovalKind(t *testing.T, f *instancesFixture, inst string) (oauthRemovalKind, bool) {
	t.Helper()
	for _, name := range authDirEntries(t, f) {
		who, _, _, ok := parseOAuthIntent(name)
		if !ok || who != inst {
			continue
		}
		i := readIntent(t, filepath.Join(oauthDir(f), name))
		if i.op == oauthOpRemove {
			return i.kind, true
		}
	}
	return "", false
}

// assertNoIntentRecords fails when any intent record is still filed: the
// ordinary paths must not leak their transaction record.
func assertNoIntentRecords(t *testing.T, f *instancesFixture) {
	t.Helper()
	if left := intentNames(t, f); len(left) != 0 {
		t.Fatalf("the auth directory still holds the intent records %v", left)
	}
}

// ---- the two names, and the parser ----

// TestOAuthRecordNamesStayApart: the ONE stamped-name grammar parses a parked
// copy and an intent apart, and neither is mistaken for the other or for a
// record. A name whose stamp is not all digits, or whose record part does not
// end in .json, is neither - including a record for an instance whose own name
// holds a marker (a legal provider name).
func TestOAuthRecordNamesStayApart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		copyInst string
		copyOK   bool
		itInst   string
		itLanded bool
		itOK     bool
	}{
		{"work.json.removing-5", "work", true, "", false, false},
		{"work.json.intent-5", "", false, "work", false, true},
		{"work.json.landed-5", "", false, "work", true, true},
		{"x.removing-1.json", "", false, "", false, false},
		{"x.removing-1.json.removing-5", "x.removing-1", true, "", false, false},
		{"x.removing-cfg-1.json.intent-7", "", false, "x.removing-cfg-1", false, true},
		{"notes.txt", "", false, "", false, false},
		{"work.json.removing-abc", "", false, "", false, false},
		{"work.json.intent-", "", false, "", false, false},
		{"work.json.removed-5", "", false, "", false, false},
		{"work.json.removing-cfg-5", "", false, "", false, false},
	} {
		inst, _, ok := parseOAuthAside(tc.name)
		if inst != tc.copyInst || ok != tc.copyOK {
			t.Fatalf("parseOAuthAside(%q) = (%q, %v), want (%q, %v)", tc.name, inst, ok, tc.copyInst, tc.copyOK)
		}
		itInst, _, itLanded, itOK := parseOAuthIntent(tc.name)
		if itInst != tc.itInst || itLanded != tc.itLanded || itOK != tc.itOK {
			t.Fatalf("parseOAuthIntent(%q) = (%q, landed=%v, %v), want (%q, landed=%v, %v)", tc.name, itInst, itLanded, itOK, tc.itInst, tc.itLanded, tc.itOK)
		}
	}
}

// TestParseOAuthIntentRecordIsStrict: the record is parsed strictly, in one
// place for the reader and the writer. A duplicated key, an unknown key, an
// unknown value, a field the op does not carry, a missing field and an invalid
// instance name are all errors: recovery keeps such a record and reports it
// rather than guessing what the mutation was.
func TestParseOAuthIntentRecordIsStrict(t *testing.T) {
	good := []string{
		"op=remove\ninst=work\nkind=config-backed\n",
		"op=remove\ninst=work\nkind=credential-only\n",
	}
	for _, raw := range good {
		i, err := parseOAuthIntentRecord([]byte(raw), "rec")
		if err != nil {
			t.Fatalf("parseOAuthIntentRecord(%q) = %v, want a readable record", raw, err)
		}
		again, err := parseOAuthIntentRecord(i.encode(), "rec")
		if err != nil || again != i {
			t.Fatalf("encode/decode round trip of %q = (%+v, %v), want the record", raw, again, err)
		}
	}
	bad := []string{
		// How far a mutation got is NOT a field of this record - it is the name
		// the record is filed under - so a record claiming it in its content is
		// refused rather than half honoured.
		"op=remove\ninst=work\nkind=config-backed\nphase=landed\n",
		"op=rename\ninst=work\nnew=personal\nphase=started\n",
		"op=remove\ninst=work\nkind=config-backed\nextra=1\n",
		"op=remove\ninst=work\nkind=else\n",
		"op=remove\ninst=work\n",
		"op=rename\ninst=work\nnew=personal\nkind=config-backed\n",
		"op=rename\ninst=work\n",
		"op=remove\ninst=work\nkind=config-backed\nnew=personal\n",
		"op=remove\ninst=../etc/passwd\nkind=config-backed\n",
		"inst=work\nkind=config-backed\n",
		"op=remove\ninst=work\nkind=config-backed\nnot a pair\n",
		"\n",
	}
	for _, raw := range bad {
		if i, err := parseOAuthIntentRecord([]byte(raw), "rec"); err == nil {
			t.Fatalf("parseOAuthIntentRecord(%q) = (%+v, nil), want a refusal", raw, i)
		}
	}
}

// TestWriteIntentStepsAPastARepeatedStamp: the intent's name carries the
// clock's stamp, and a repeated stamp (a clock that did not advance, a state
// root restored from a snapshot) must not overwrite an earlier mutation's
// record: the second record is filed under a stepped name, so both mutations
// stay recoverable.
func TestWriteIntentStepsAPastARepeatedStamp(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	f.ctl.auth.now = func() time.Time { return fixed }

	first, err := f.ctl.writeIntent(renameIntent("work", "personal"), true)
	if err != nil {
		t.Fatalf("first writeIntent: %v", err)
	}
	// A second rename of the same name with the clock unchanged.
	second, err := f.ctl.writeIntent(renameIntent("work", "personal"), true)
	if err != nil {
		t.Fatalf("second writeIntent: %v", err)
	}
	if second == first {
		t.Fatalf("the second record took the first one's name %s, overwriting it", first)
	}
	for _, path := range []string{first, second} {
		got := readIntent(t, path)
		if got.op != oauthOpRename || got.inst != "work" || got.new != "personal" {
			t.Fatalf("the record %s = %+v, want the rename it recorded", path, got)
		}
	}
}

// ---- the kind the removal records ----

// TestInstances_RemovalRecordsTheKindInTheIntent: the kind is written into the
// removal's intent record before the first copy moves, so it is durable with no
// extra file. A removal of an AUTHORED instance (providers.toml carried the
// name) records a config-backed removal; one of a credential-only instance (no
// entry) records a credential-only one. The reclaim is refused so both records
// (and their copies) stay on disk to be inspected.
func TestInstances_RemovalRecordsTheKindInTheIntent(t *testing.T) {
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

	for _, tc := range []struct {
		inst string
		want oauthRemovalKind
	}{{"work", oauthKindConfigBacked}, {"openai-codex", oauthKindCredentialOnly}} {
		kind, ok := recordedRemovalKind(t, f, tc.inst)
		if !ok {
			t.Fatalf("the removal of %q left no intent record; the auth directory holds %v", tc.inst, authDirEntries(t, f))
		}
		if kind != tc.want {
			t.Fatalf("the removal of %q recorded kind=%q, want %q", tc.inst, kind, tc.want)
		}
	}
}

// TestInstances_RemovalRecordsTheKindFromTheLayerItWrites: the kind in the
// record and the decision to rewrite providers.toml are ONE answer, taken from
// ONE parse of the file. This pins both observable cases in one place - a
// `default` pointer the removal drops (config-backed, with the pointer really
// cleared) and a name nothing in providers.toml carries (credential-only, with
// the file really untouched).
func TestInstances_RemovalRecordsTheKindFromTheLayerItWrites(t *testing.T) {
	// (a) The pointer is the only thing carrying the name, so the removal drops
	// it: providers.toml is rewritten and the record must say config-backed.
	cfgCase := newInstancesFixture(t, nil)
	if err := os.WriteFile(cfgCase.tomlPath, []byte("default = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, cfgCase, "openai-codex", "codex@example.com")
	cfgCase.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }
	if err := cfgCase.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("Remove = nil, want the sweep failure reported")
	}
	kind, ok := recordedRemovalKind(t, cfgCase, "openai-codex")
	if !ok || kind != oauthKindConfigBacked {
		t.Fatalf("the default-pointer removal recorded (kind=%q, ok=%v), want one config-backed record: it rewrote providers.toml", kind, ok)
	}
	l, exists, err := registry.ReadConfigFile(cfgCase.tomlPath)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile = (%v, %v, %v), want the written config", l, exists, err)
	}
	if l.Default != "" {
		t.Fatalf("default = %q, want the removal to have cleared the pointer", l.Default)
	}

	// (b) Nothing in providers.toml carries the name, so the removal writes
	// nothing and the record must say credential-only - and the file it did not
	// write must be exactly as the removal found it.
	plainCase := newInstancesFixture(t, nil)
	seedOAuthRecord(t, plainCase, "openai-codex", "codex@example.com")
	before, beforeErr := os.ReadFile(plainCase.tomlPath)
	plainCase.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }
	if err := plainCase.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("Remove = nil, want the sweep failure reported")
	}
	kind, ok = recordedRemovalKind(t, plainCase, "openai-codex")
	if !ok || kind != oauthKindCredentialOnly {
		t.Fatalf("the credential-only removal recorded (kind=%q, ok=%v), want one credential-only record: it wrote no config", kind, ok)
	}
	after, afterErr := os.ReadFile(plainCase.tomlPath)
	if (afterErr == nil) != (beforeErr == nil) || !bytes.Equal(before, after) {
		t.Fatalf("providers.toml = %q (err %v), want it untouched (before: %q, err %v)", after, afterErr, before, beforeErr)
	}
}

// TestInstances_RemovalRecordsTheDefaultPointerAsConfigBacked: a removal that
// only cleared `default` still writes providers.toml, so a crash after that
// write but before the commit point would leave a copy startup restores -
// resurrecting the removed instance. The record's kind must therefore mean "this
// removal changed providers.toml": an authored entry OR a `default` pointer
// naming the instance.
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
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("Remove = nil, want the sweep failure reported")
	}
	kind, ok := recordedRemovalKind(t, f, "openai-codex")
	if !ok || kind != oauthKindConfigBacked {
		t.Fatalf("the default-only removal recorded (kind=%q, ok=%v), want a config-backed record, or startup would restore the removed instance", kind, ok)
	}
	l, exists, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile = (%v, %v, %v), want the written config", l, exists, err)
	}
	if l.Default != "" {
		t.Fatalf("default = %q, want the removal to have cleared the pointer", l.Default)
	}
}

// ---- parking (setAsideOAuthFile) ----

// TestLandOAuthIntentIsTheCommitPoint: the commit is the record's NAME, not a
// value inside it. Landing carries the record from its in-flight name to its
// landed one, is idempotent, never replaces a file already filed there (the
// no-replace rule, so a taken name fails the commit and the removal rolls back
// rather than losing bytes), and refuses a path that is not a record at all.
// The rollback's unland is the same act in reverse, so a record is filed under
// one name or the other and no torn or lost write can leave it claiming both.
func TestLandOAuthIntentIsTheCommitPoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, oauthIntentName("work", 7))
	if err := os.WriteFile(path, removalIntent("work", false).encode(), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}

	landed, err := landOAuthIntent(path)
	if err != nil {
		t.Fatalf("landOAuthIntent: %v", err)
	}
	if want := filepath.Join(dir, oauthLandedName("work", 7)); landed != want {
		t.Fatalf("landOAuthIntent = %q, want %q", landed, want)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the in-flight name survives (Lstat = %v), want the record renamed", err)
	}
	if !recordIsLanded(t, landed) {
		t.Fatal("the record is not filed under the landed name")
	}
	// Idempotent: landing a landed record hands back the same path, not a second
	// rename that could race the first.
	if again, err := landOAuthIntent(landed); err != nil || again != landed {
		t.Fatalf("second landOAuthIntent = (%q, %v), want the landed path unchanged", again, err)
	}
	// The rollback's unland is the reverse, and idempotent too.
	back, err := unlandOAuthIntent(landed)
	if err != nil || back != path {
		t.Fatalf("unlandOAuthIntent = (%q, %v), want %q", back, err, path)
	}
	if recordIsLanded(t, back) {
		t.Fatal("the record is still filed as landed after the unland")
	}
	if back2, err := unlandOAuthIntent(back); err != nil || back2 != back {
		t.Fatalf("second unlandOAuthIntent = (%q, %v), want the in-flight path unchanged", back2, err)
	}
	// A name that is not a record is refused rather than renamed.
	if got, err := landOAuthIntent(filepath.Join(dir, "notes.txt")); err == nil {
		t.Fatalf("landOAuthIntent(notes.txt) = (%q, nil), want a refusal", got)
	}
	// A taken landed name is never replaced: the commit fails, the caller rolls
	// the removal back, and the bytes already there survive.
	if err := os.WriteFile(landed, []byte("another mutation's record\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", landed, err)
	}
	if _, err := landOAuthIntent(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("landOAuthIntent over a taken landed name = %v, want an os.ErrExist refusal", err)
	}
	if b, rerr := os.ReadFile(landed); rerr != nil || string(b) != "another mutation's record\n" {
		t.Fatalf("the taken landed name = %q (%v), want its bytes untouched", b, rerr)
	}
	// An empty path is a mutation with no record: nothing to land, nothing to refuse.
	if got, err := landOAuthIntent(""); got != "" || err != nil {
		t.Fatalf("landOAuthIntent(\"\") = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestInstances_SetAsideOAuthFileRefusesANegativeStamp: a stamp from before the
// Unix epoch has a '-' in its decimal text, which parseOAuthAside does not
// parse - so the copy would be debris the reclaim skips, and a removal reported
// as successful could leave the credential on disk. The removal refuses before
// anything is deleted, mirroring the bounds the search enforces.
func TestInstances_SetAsideOAuthFileRefusesANegativeStamp(t *testing.T) {
	f := newInstancesFixture(t, nil)
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const content = "the record the removal must not lose\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f.ctl.auth.now = func() time.Time { return time.Unix(-1, 0) }

	aside, err := f.ctl.setAsideOAuthFile("openai-codex")
	if err == nil || !strings.Contains(err.Error(), "negative stamp") {
		t.Fatalf("setAsideOAuthFile = (%q, %v), want the negative stamp refused", aside, err)
	}
	if aside != "" {
		t.Fatalf("setAsideOAuthFile = %q, want no copy path on a refusal", aside)
	}
	if b, rerr := os.ReadFile(path); rerr != nil || string(b) != content {
		t.Fatalf("the record = %q (%v), want it left in place by the refusal", b, rerr)
	}
}

// TestInstances_SetAsideOAuthFileRefusesToStepPastTheMaximumStamp: the copy
// candidate loop steps one past a taken stamp. A step past maxAsideStamp wraps
// to a negative tail no recovery parses, so the loop must refuse rather than
// name a copy the reclaim skips - the same bound freeAsideName enforces. The
// candidate at the maximum stamp is already taken, so the only step left would
// exceed it.
func TestInstances_SetAsideOAuthFileRefusesToStepPastTheMaximumStamp(t *testing.T) {
	f := newInstancesFixture(t, nil)
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const content = "the record the removal must not lose\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	taken := filepath.Join(filepath.Dir(path), oauthAsideName("openai-codex", maxAsideStamp))
	if err := os.WriteFile(taken, []byte("a copy already at the maximum stamp\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", taken, err)
	}
	f.ctl.auth.now = func() time.Time { return time.Unix(0, maxAsideStamp) }
	if got := f.ctl.auth.now().UnixNano(); got != maxAsideStamp {
		t.Skipf("this clock cannot express the maximum stamp (UnixNano = %d); the premise needs it", got)
	}

	aside, err := f.ctl.setAsideOAuthFile("openai-codex")
	if err == nil || !strings.Contains(err.Error(), "no copy stamp at or below") {
		t.Fatalf("setAsideOAuthFile = (%q, %v), want the stepped-past-maximum refusal", aside, err)
	}
	if aside != "" {
		t.Fatalf("setAsideOAuthFile = %q, want no copy path on a refusal", aside)
	}
	if b, rerr := os.ReadFile(path); rerr != nil || string(b) != content {
		t.Fatalf("the record = %q (%v), want it left in place by the refusal", b, rerr)
	}
	if _, statErr := os.Lstat(taken); statErr != nil {
		t.Fatalf("the copy at the maximum stamp was taken (%v), want it left untouched", statErr)
	}
}

// TestFreeAsideNameRefusesToAllocateBelowAMaximumStamp: a copy already filed at
// maxAsideStamp is the greatest stamp a copy name can carry, so the search
// cannot step past it. It must refuse rather than fall back to the requested
// stamp, which is lower - recovery restores the newest copy of a name, so a
// lower stamp would make the older credential win.
func TestFreeAsideNameRefusesToAllocateBelowAMaximumStamp(t *testing.T) {
	dir := t.TempDir()
	taken := filepath.Join(dir, oauthAsideName("work", maxAsideStamp))
	if err := os.WriteFile(taken, []byte("a copy at the maximum stamp\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", taken, err)
	}

	got, ok := freeAsideName(dir, "work", 100)
	if ok {
		t.Fatalf("freeAsideName = (%q, true), want a refusal: the maximum is exhausted and 100 is lower", got)
	}
	if got != "" {
		t.Fatalf("freeAsideName = %q, want no path on a refusal", got)
	}
	if _, _, reason, _ := findFreeAsideName(dir, "work", 100); reason != asideSearchExhausted {
		t.Fatalf("findFreeAsideName reason = %v, want asideSearchExhausted", reason)
	}
	if b, rerr := os.ReadFile(taken); rerr != nil || string(b) != "a copy at the maximum stamp\n" {
		t.Fatalf("the copy = %q (%v), want it left untouched", b, rerr)
	}
}

// ---- the removal's crash points ----

// TestInstances_RemoveKeepsItsIntentWhenTheReclaimFails: a removal that stands
// leaves its intent at the commit phase when a copy could not be deleted, so a
// crash - or the next start, after the failed call - never puts the copy back.
// The delete failure is still reported as the removal's own, because the removal
// stood and the caller is the only one left who can delete what the report
// names.
func TestInstances_RemoveKeepsItsIntentWhenTheReclaimFails(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// A readable providers.toml that does not carry openai-codex: the release of
	// a credential-only record consults the record's own phase, and the file is
	// there so the other reads in the pass have a config.
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil || !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("Remove = %v, want the copy it could not delete reported", err)
	}
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("Remove = %v (%T), want a removeAppliedError so the removal is announced", err, err)
	}
	left := parkedNames(t, f, "openai-codex")
	if len(left) != 1 {
		t.Fatalf("the auth directory holds %v, want the one parked copy the removal could not delete", authDirEntries(t, f))
	}
	kind, ok := recordedRemovalKind(t, f, "openai-codex")
	if !ok || kind != oauthKindCredentialOnly {
		t.Fatalf("the record = (kind=%q, ok=%v), want the credential-only removal that stood", kind, ok)
	}
	// The commit point had renamed it: the record the failed reclaim left says
	// the removal stood, which is what the next start sweeps by.
	for _, name := range intentNames(t, f) {
		if inst, _, _, ok := parseOAuthIntent(name); !ok || inst != "openai-codex" {
			continue
		}
		if !recordIsLanded(t, filepath.Join(oauthDir(f), name)) {
			t.Fatalf("the record %s is not filed as landed, so startup would put the copy back", name)
		}
	}

	// The record says the removal landed, so startup sweeps the copy rather than
	// putting it back: a crash after the commit point cannot undo the removal the
	// caller was told had happened. The sweep reports the credential-only copy it
	// deletes with a free record path.
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr == nil || !strings.Contains(rerr.Error(), "deleted the parked copy "+left[0]) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the swept copy %s reported", restored, rerr, left[0])
	}
	if restored {
		t.Fatal("startup put a copy of a landed removal back, undoing the removal")
	}
	if _, statErr := os.Lstat(filepath.Join(oauthDir(f), left[0])); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the parked copy survived startup (Lstat = %v), want the landed removal's copy swept", statErr)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record is back at its own path (Lstat = %v), want the removal to stand", statErr)
	}
	assertNoIntentRecords(t, f)
}

// TestInstances_RemovalsStampLaterRevivalsAboveEarlierOnesWhenTheClockStepsBackward:
// startup restores the NEWEST copy of a name, so a backward clock must not let a
// later removal file its copy under a smaller stamp than an earlier one's. Two
// removals are driven with a clock that steps backward between them, and the
// later copy's stamp must still be the greater. Both records are then returned to
// the STARTED phase a crash before the commit point leaves, and startup must
// restore the later record, not the older one.
func TestInstances_RemovalsStampLaterRevivalsAboveEarlierOnesWhenTheClockStepsBackward(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
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

	// The reclaim cannot delete, so each removal leaves its copy for the next one
	// (and this test) to see.
	f.ctl.auth.deleteAside = func(string) error { return errors.New("delete refused") }
	high := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	low := high.Add(-time.Hour)

	save("first@example.com")
	f.ctl.auth.now = func() time.Time { return high }
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("first Remove = nil, want the sweep failure reported")
	}
	firstStamps := parkedStamps(t, f, "openai-codex")
	if len(firstStamps) != 1 {
		t.Fatalf("after the first removal, copy stamps = %v, want one", firstStamps)
	}

	second := save("second@example.com")
	f.ctl.auth.now = func() time.Time { return low }
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err == nil {
		t.Fatal("second Remove = nil, want the sweep failure reported")
	}
	secondStamps := parkedStamps(t, f, "openai-codex")
	if len(secondStamps) != 2 {
		t.Fatalf("after the second removal, copy stamps = %v, want two", secondStamps)
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

	// The shape a crash before the commit point leaves: both records still filed
	// under their in-flight name, so startup is what chooses between the copies.
	for _, name := range intentNames(t, f) {
		if _, err := unlandOAuthIntent(filepath.Join(oauthDir(f), name)); err != nil {
			t.Fatalf("unlandOAuthIntent(%s): %v", name, err)
		}
	}
	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
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
	assertNoIntentRecords(t, f)
}

// TestInstances_RemoveMarksItsIntentLandedBeforeTheReload pins the ordering the
// commit point rests on: a removal must rewrite its record's phase BEFORE the
// reload that publishes it. A crash anywhere from the commit point onward then
// leaves a record startup reads as one that stood; leaving the phase to the
// post-reload reclaim would put the whole reload inside a window where a crash
// resurrects an instance the user successfully removed.
//
// The registry loader reads the record's phase at the instant of every load, so
// the assertion is about what the removal's OWN reload saw rather than a
// restatement of where the call sits. Moving the phase rewrite back into the
// reclaim leaves the removal's reload staring at a started record and fails this
// test.
func TestInstances_RemoveMarksItsIntentLandedBeforeTheReload(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")

	var (
		mu     sync.Mutex
		phases []string
	)
	// observe takes no *testing.T: the registry loader can run on its own
	// goroutine, and a test helper that calls t.Fatalf there would race the test.
	observe := func() string {
		entries, err := os.ReadDir(oauthDir(f))
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			inst, _, landed, ok := parseOAuthIntent(e.Name())
			if !ok || inst != "openai-codex" {
				continue
			}
			raw, rerr := os.ReadFile(filepath.Join(oauthDir(f), e.Name()))
			if rerr != nil {
				return ""
			}
			if _, perr := parseOAuthIntentRecord(raw, filepath.Join(oauthDir(f), e.Name())); perr != nil {
				return ""
			}
			if landed {
				return "landed"
			}
			return "intent"
		}
		return ""
	}
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		mu.Lock()
		phases = append(phases, observe())
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
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(phases) < 2 {
		t.Fatalf("the loader ran %d time(s), want the removal's own reload among them", len(phases))
	}
	if last := phases[len(phases)-1]; last != "landed" {
		t.Fatalf("the removal's own reload saw the record %q, want it landed before the reload", last)
	}
	sawLanded := false
	for _, p := range phases {
		if p == "landed" {
			sawLanded = true
		}
	}
	if !sawLanded {
		t.Fatal("no load observed a landed record, so the ordering this test pins was never exercised")
	}
}

// TestInstances_RemoveRestoresTheRecordWhenTheReloadFails: the phase lands before
// the reload, so a reload that fails rolls back a removal whose record says it
// stood. The rollback restores the credential and returns the record to its
// started phase (or spends it, when nothing is left parked), so nothing is left
// for a later start to resurrect.
func TestInstances_RemoveRestoresTheRecordWhenTheReloadFails(t *testing.T) {
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
		t.Fatalf("the record was not restored: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("restored bytes = %q, want the original %q", after, before)
	}
	if left := parkedNames(t, f, "openai-codex"); len(left) != 0 {
		t.Fatalf("the rollback left the parked copies %v, want them renamed back to the record path", left)
	}
	assertNoIntentRecords(t, f)
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); !ok || inst.CredentialSource != "oauth" {
		t.Fatalf("openai-codex = %+v (ok = %v), want the instance back on its record after the retry reload", inst, ok)
	}
}

// TestInstances_RolledBackRemovalLeavesCopiesWhereRecoveryReadsThem: a removal
// whose reload fails rolls back. When the removal owns no copy of its own - the
// record was already parked by an earlier failed removal - the rollback restores
// nothing, but the pre-existing copy must stay where startup recovery reads it,
// and the removal's record must go back to the started phase, or a later start
// would sweep the instance's only credential. A record left at its landed phase
// would have startup read the removal as one that stood.
func TestInstances_RolledBackRemovalLeavesCopiesWhereRecoveryReadsThem(t *testing.T) {
	// Load 1 is the fixture's own; load 2 primes the config; load 3 is the
	// removal's reload, whose failure rolls the removal back; load 4 is the
	// rollback's retry, which succeeds.
	f := newFlakyReloadFixture(t, "", func(load int) bool { return load == 3 })
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	if _, ok := f.ctl.reg.Get().Instance("work"); !ok {
		t.Fatalf("fixture: work is not in the registry; instances = %+v", f.ctl.reg.Get().Instances())
	}

	// The record an earlier failed removal parked and could not put back: the
	// only credential work has.
	const content = "the only credential of work\n"
	parkedAt(t, f, "work", "1757000000000000000", content)
	if _, err := os.Lstat(authopenai.AuthFilePath(f.stateDir, "work")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: the record path holds a file (Lstat = %v), want the removal to own no copy", err)
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil || !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the removal reported as rolled back", err)
	}

	left := parkedNames(t, f, "work")
	if len(left) != 1 {
		t.Fatalf("the auth directory holds the copies %v, want exactly the pre-existing one", left)
	}
	// The record the rollback left says the removal did not stand, so startup
	// puts work's only credential back.
	for _, name := range intentNames(t, f) {
		if inst, _, _, _ := parseOAuthIntent(name); inst != "work" {
			continue
		}
		if recordIsLanded(t, filepath.Join(oauthDir(f), name)) {
			t.Fatal("the rollback left a landed record, want it back at its in-doubt name so startup restores the copy")
		}
	}
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup put nothing back, want work's only credential restored")
	}
	got, rerr := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "work"))
	if rerr != nil || string(got) != content {
		t.Fatalf("restored record = %q (%v), want %q", got, rerr, content)
	}
}

// TestInstances_RemoveAPhaseFailureRollsBackTheRemoval: the commit point is not
// optional bookkeeping. A record whose phase cannot be rewritten must fail the
// removal and roll it back the way a failed reload does - providers.toml still
// carrying the instance, the stored key and the record back under their own
// names - and must NEVER come back as a persisted removal. Otherwise a removal
// the caller was told had stood could leave a copy the next startup restores,
// resurrecting the credential the user removed.
//
// The failure is driven the way the disk drives it: a directory standing where
// the intent record is, so reading it back for the phase update fails.
func TestInstances_RemoveAPhaseFailureRollsBackTheRemoval(t *testing.T) {
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
	record := authopenai.AuthFilePath(f.stateDir, "work")
	// After the credential cleanup - which runs after the record is written and
	// the copy parked - occupy the name the commit point renames the record TO,
	// so the commit cannot land. The removal is then still in doubt and must roll
	// back rather than report itself as standing.
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 321, time.UTC)
	f.ctl.auth.now = func() time.Time { return fixed }
	landed := filepath.Join(oauthDir(f), oauthLandedName("work", fixed.UnixNano()))
	realDeleteAuth := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(stateDir, name string) (bool, error) {
		removed, err := realDeleteAuth(stateDir, name)
		if mkErr := os.Mkdir(landed, 0o700); mkErr != nil {
			t.Fatalf("Mkdir(%s): %v", landed, mkErr)
		}
		if wErr := os.WriteFile(filepath.Join(landed, "obstacle"), []byte("in the way"), 0o600); wErr != nil {
			t.Fatalf("WriteFile(obstacle): %v", wErr)
		}
		return removed, err
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the commit-point failure reported")
	}
	if _, persisted := errors.AsType[removeAppliedError](err); persisted {
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
	if left := parkedNames(t, f, "work"); len(left) != 0 {
		t.Fatalf("a parked copy %v survived the rollback, so startup would restore it", left)
	}
	assertNoIntentRecords(t, f)
}

// TestInstances_RemoveAPhaseFailureAndAFailedRollbackLeavesStartupToRecover: the
// rollback's own rename-back can fail too, and then the parked copy is the
// instance's only record. The removal must still be reported as FAILED - never
// as one that stood - so startup recovery (restoreUncommittedOAuthAsides) is what
// puts the record back. A persisted removal beside a copy startup restores is
// exactly the resurrection this design forbids.
func TestInstances_RemoveAPhaseFailureAndAFailedRollbackLeavesStartupToRecover(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// A readable providers.toml that does not carry openai-codex, so the startup
	// pass has config evidence to reason about.
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 654, time.UTC)
	f.ctl.auth.now = func() time.Time { return fixed }
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	// After the credential cleanup, occupy the name the commit point renames the
	// record TO (so the commit cannot land) and the record path (so the
	// rollback's rename-back cannot put the copy back either). The record path is
	// a directory, so the registry's instance scan skips it, and the test clears
	// both before startup recovery runs.
	landed := filepath.Join(oauthDir(f), oauthLandedName("openai-codex", fixed.UnixNano()))
	realDeleteAuth := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(stateDir, name string) (bool, error) {
		removed, err := realDeleteAuth(stateDir, name)
		if mkErr := os.Mkdir(landed, 0o700); mkErr != nil {
			t.Fatalf("Mkdir(%s): %v", landed, mkErr)
		}
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
		t.Fatal("Remove = nil, want the commit-point failure reported")
	}
	// The record never came back to its own path, so the instance does not
	// resolve from it and the landed contract reports the removal as standing
	// (main's discriminator, which every client already reads) - NOT as a plain
	// rollback. What the records add is the disk state: the copy is still there,
	// in the shape startup recovery reads, so the credential is never lost even
	// though the removal stands.
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("Remove = %v (%T), want the standing-removal discriminator: the carrying record is not back", err, err)
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing frame: the carrying record is not back", err)
	}
	left := parkedNames(t, f, "openai-codex")
	if len(left) != 1 {
		t.Fatalf("the parked copies = %v, want the one the failed rollback must leave for startup", left)
	}
	// The failed rollback could not return the record to its in-doubt name - the
	// commit's landed name is the one it holds, and the in-flight name is free
	// only once the obstacle goes - so repair the disk the way the disk would:
	// the copy is all that is left of the credential, and a removal that never
	// stood leaves its record in doubt.
	if rerr := os.RemoveAll(record); rerr != nil {
		t.Fatalf("RemoveAll(%s): %v", record, rerr)
	}
	if rerr := os.RemoveAll(landed); rerr != nil {
		t.Fatalf("RemoveAll(%s): %v", landed, rerr)
	}
	removalAt(t, f, "openai-codex", false, false, strconv.FormatInt(fixed.UnixNano(), 10))
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup did not put the parked record back")
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record was not restored to %s: %v", record, statErr)
	}
}

// TestInstances_RemoveAReloadFailureKeepsTheCopyRecoverable: the removal's commit
// point can land and the removal still fail (a reload that cannot resolve the
// config it wrote). When the rollback also cannot put the record back at its own
// path, the copy must stay in the shape startup recovery reads, with the record
// returned to its started phase - a landed record of a credential-only removal
// is swept, so leaving the phase alone would delete the instance's only record
// on the next start. The disk is asserted directly, then startup recovery is run
// and the record must come back at its own path.
//
// The fixture is a credential-only Codex instance, and the rename-back is refused
// the way the disk refuses it: a non-empty directory occupying the record path.
func TestInstances_RemoveAReloadFailureKeepsTheCopyRecoverable(t *testing.T) {
	// Load 1 primes the fixture, load 2 is seedOAuthRecord's, load 3 is the
	// removal's reload (the one made to fail), and load 4 is the rollback's retry.
	f := newFlakyReloadFixture(t, "", func(load int) bool { return load == 3 })
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
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
	// The record is not back at its own path, so the instance does not resolve
	// from it and the landed contract reports the removal as standing, with the
	// discriminator every client already reads. What the records guarantee is the
	// DISK: the copy is in the shape startup recovery reads, and the record says
	// the removal did not finish.
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("Remove = %v (%T), want the standing-removal discriminator: the record is not back", err, err)
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing frame: the record is not back", err)
	}
	left := parkedNames(t, f, "openai-codex")
	if len(left) != 1 {
		t.Fatalf("the parked copies = %v, want the one work startup recovery reads", left)
	}
	var markers []bool
	for _, name := range intentNames(t, f) {
		if inst, _, _, ok := parseOAuthIntent(name); !ok || inst != "openai-codex" {
			continue
		}
		markers = append(markers, recordIsLanded(t, filepath.Join(oauthDir(f), name)))
	}
	if len(markers) != 1 || markers[0] {
		t.Fatalf("the records = %v, want the one in-doubt record the failed rollback must leave", markers)
	}

	// Clear the obstacle as the disk would once the refusal passes, then let
	// startup recover. Were the record still landed, the sweep would delete the
	// copy instead.
	if rerr := os.RemoveAll(record); rerr != nil {
		t.Fatalf("RemoveAll(%s): %v", record, rerr)
	}
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup did not restore the copy the rollback left recoverable")
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record was not restored to %s: %v", record, statErr)
	}
}

// TestInstances_RemoveReportsADeleteFailureAsACredentialStillOnDisk: a copy the
// reclaim cannot delete is described as a credential still on disk, with the
// wording the removal's own message uses - the caller is the only one left who
// can delete it.
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
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("Remove = %v (%T), want a removeAppliedError so the removal is announced", err, err)
	}
	if left := parkedNames(t, f, "openai-codex"); len(left) != 1 {
		t.Fatalf("the auth directory holds %v, want the one parked copy the removal could not delete", authDirEntries(t, f))
	}
}

// TestInstances_RemoveLeavesNoIntent: the ordinary path must not leak the
// transaction record. A removal that stands deletes its copies and spends its
// record.
func TestInstances_RemoveLeavesNoIntent(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.store.Set("groq", "gk"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	seedOAuthRecord(t, f, "groq", "")
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	assertNoIntentRecords(t, f)
	if left := parkedNames(t, f, "groq"); len(left) != 0 {
		t.Fatalf("the auth directory holds %v, want the removal's copies reclaimed", left)
	}
}

// TestInstances_RolledBackRemovalSpendsItsIntent: a removal that fails after its
// commit point rolls back, and the rollback resolves the record it wrote - a
// record left classifying the name as landed would have the next startup sweep
// the very credential the rollback restored.
func TestInstances_RolledBackRemovalSpendsItsIntent(t *testing.T) {
	f := newFlakyReloadFixture(t, "", func(load int) bool { return load == 3 })
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	const content = "the only credential of work\n"
	parkedAt(t, f, "work", "1757000000000000000", content)

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil || !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the removal reported as rolled back", err)
	}
	if _, statErr := os.Lstat(filepath.Join(oauthDir(f), "work.json"+oauthAsideMarker+"1757000000000000000")); statErr != nil {
		t.Fatalf("the rollback took the parked copy (%v), want it left where recovery reads it", statErr)
	}
	// The copy is still parked, so the removal's record must stay - at its
	// started phase, which is what tells the next start the removal did not
	// stand and the copy is the instance's credential.
	records := intentNames(t, f)
	if len(records) != 1 {
		t.Fatalf("the rollback left the records %v, want the one that keeps the copy recoverable", records)
	}
	if recordIsLanded(t, filepath.Join(oauthDir(f), records[0])) {
		t.Fatal("the record is filed as landed, want its in-doubt name so startup puts the copy back rather than sweeping it")
	}
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil || !restored {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the copy the rollback left put back", restored, rerr)
	}
}

// TestInstances_RemoveReportsOneFailureFrame: the rollback's cause already names
// what failed, so the caller must not add a second frame of the same shape - the
// message reads "removing %q failed: <cause>" once, the way the phase-failure
// path reports it.
func TestInstances_RemoveReportsOneFailureFrame(t *testing.T) {
	f := newFlakyReloadFixture(t, "openai-codex", func(load int) bool { return load == 3 })
	if err := authopenai.SaveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	// Occupy the record path so the rollback cannot put the carrying record back:
	// the rollback then reports the STANDING removal, which is the branch whose
	// cause the caller had already framed.
	authPath := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		removed, err := originalDelete(dir, name)
		if mkErr := os.Mkdir(authPath, 0o700); mkErr != nil && !os.IsExist(mkErr) {
			t.Errorf("Mkdir(%s): %v", authPath, mkErr)
		}
		return removed, err
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"})
	if err == nil || !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing removal reported", err)
	}
	const frame = `removing "openai-codex" failed:`
	if got := strings.Count(err.Error(), frame); got != 1 {
		t.Fatalf("Remove = %v, want exactly one %q frame, got %d", err, frame, got)
	}
}

// ---- recovery: the one rule ----

// TestRestoreUncommittedOAuthAsidesPutsBackACredentialOnlyImplicitRecord: an
// implicit Codex instance exists from its OAuth record alone and has no
// providers.toml entry to name it. A removal of it that never reached its commit
// point leaves a started record, and startup puts the parked copy back - a
// credential-only removal's own record is the evidence, so the absent config
// deferred nothing and is not reported.
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
	parkedAt(t, f, "openai-codex", "1757000000000000000", string(original))
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(%s): %v", path, err)
	}
	removalAt(t, f, "openai-codex", false, false, "1757000000000000000")
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	// The premise: with the record set aside the instance is gone, because the
	// record was the whole of what made it exist.
	if before, ok := f.ctl.reg.Get().Instance("openai-codex"); ok {
		t.Fatalf("fixture: openai-codex = %+v, want no instance while its record is parked", before)
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the credential-only record restored with no config diagnostic", restored, err)
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
	if left := parkedNames(t, f, "openai-codex"); len(left) != 0 {
		t.Fatalf("the copy is still on disk as %v, want it moved rather than copied", left)
	}
	assertNoIntentRecords(t, f)
}

// TestRestoreUncommittedOAuthAsidesSweepsTheCopiesAStandingRemovalLeft: a
// removal record at its landed phase says the removal reached its commit point,
// so startup never puts its copy back - doing so would undo the removal the
// caller was told had happened. A credential-only copy a landed removal left is
// swept and REPORTED when the record path is free (it could have been the last
// bytes of that credential); a config-backed one is plain debris of a standing
// authored removal and is swept silently. A copy of a name the config carries is
// left exactly where it is: the instance still exists, and bytes at its record
// path are the record it has now.
func TestRestoreUncommittedOAuthAsidesSweepsTheCopiesAStandingRemovalLeft(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "work", "work@example.com")
	current, err := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "work"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	parkedAt(t, f, "work", "1757000000000000000", "the copy of a name the config carries\n")
	orphan := parkedAt(t, f, "retired", "1757000000000000001", "an orphaned removal\n")
	cfgOrphan := parkedAt(t, f, "gone", "1757000000000000002", "a standing authored removal\n")
	removalAt(t, f, "retired", false, true, "1757000000000000001")
	removalAt(t, f, "gone", true, true, "1757000000000000002")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil || !strings.Contains(err.Error(), "deleted the parked copy "+filepath.Base(orphan)) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the deleted credential-only copy %s reported", restored, err, filepath.Base(orphan))
	}
	if strings.Contains(err.Error(), filepath.Base(cfgOrphan)) {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want a config-backed delete not reported as a lone credential", err)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want a landed removal's copies never put back")
	}
	for _, path := range []string{orphan, cfgOrphan} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the parked copy %s was not swept (Lstat = %v), want it deleted with the removal that stood", path, statErr)
		}
	}
	if left := parkedNames(t, f, "work"); len(left) != 1 {
		t.Fatalf("work's copies = %v, want the one left while the config carries the name", left)
	}
	got, err := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "work"))
	if err != nil || !bytes.Equal(got, current) {
		t.Fatalf("the record in place = %q (%v), want the live record untouched", got, err)
	}
}

// TestRestoreUncommittedOAuthAsidesPutsBackAConfigBackedCopyTheConfigStillCarries:
// the other half of the kind rule. A config-backed removal record whose name
// providers.toml still carries describes a removal that never reached its
// providers.toml write - the config is the durable evidence that it did not - so
// the parked copy is put back, bytes intact, when the record path is free.
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
	aside := parkedAt(t, f, "work", "1757000000000000000", string(original))
	if err := os.Remove(record); err != nil {
		t.Fatalf("Remove(%s): %v", record, err)
	}
	removalAt(t, f, "work", true, false, "1757000000000000000")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
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
	assertNoIntentRecords(t, f)
}

// TestRestoreUncommittedOAuthAsidesPutsBackADefaultNamedConfigBackedCopy: the
// recovery half of the kind rule, with the name carried by a `default` pointer
// instead of an authored entry. A config-backed removal record whose name a
// `default` pointer still names is a removal that never reached its write - the
// config is the durable evidence - so it is put back.
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
	aside := parkedAt(t, f, "openai-codex", "1757000000000000000", string(original))
	if err := os.Remove(record); err != nil {
		t.Fatalf("Remove(%s): %v", record, err)
	}
	removalAt(t, f, "openai-codex", true, false, "1757000000000000000")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
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

// TestRestoreUncommittedOAuthAsidesNeedsAReloadForACredentialOnlyInstance: the
// registry's instance list is computed at load, so a credential-only instance
// whose record came back after that load stays out of the list - and out of
// every listing over it - until the next Reload. This is the reload main.go adds
// after a restore that set the returned bool. A config-carried instance needs
// none, because providers.toml carries it into the list either way; this pins
// the credential-only case.
func TestRestoreUncommittedOAuthAsidesNeedsAReloadForACredentialOnlyInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	path := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	parkedAt(t, f, "openai-codex", "1757000000000000000", string(original))
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(%s): %v", path, err)
	}
	removalAt(t, f, "openai-codex", false, false, "1757000000000000000")
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	if _, ok := f.ctl.reg.Get().Instance("openai-codex"); ok {
		t.Fatal("fixture: the parked instance is still in the registry list")
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
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

// TestRestoreUncommittedOAuthAsidesReportsNothingOnAFreshInstallWithoutAConfig:
// the default configuration has no providers.toml, and the pass used to report
// the missing config unconditionally - so every fresh install printed a
// diagnostic claiming recovery was deferred when there were no records at all,
// and a real recovery failure was indistinguishable from that noise. Nothing
// recorded and nothing parked is no recovery work for a config failure to have
// prevented, so nothing is reported.
func TestRestoreUncommittedOAuthAsidesReportsNothingOnAFreshInstallWithoutAConfig(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if _, err := os.Stat(f.tomlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: %s exists (stat err = %v), want a fresh install", f.tomlPath, err)
	}
	// No auth directory at all.
	if restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil || restored {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want nothing recovered and nothing reported on a fresh install without a providers.toml", restored, err)
	}
	// An auth directory that exists but holds no record and no copy.
	if err := os.MkdirAll(oauthDir(f), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oauthDir(f), "notes.txt"), []byte("not a copy\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil || restored {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want no config diagnostic when the auth directory holds nothing of the hub's", restored, err)
	}
}

// TestRestoreUncommittedOAuthAsidesReportsNoConfigFailureForACredentialOnlyRecovery:
// a fresh install has no providers.toml, and a credential-only removal needs no
// config to be resolved: the record's phase says whether the removal landed. The
// pass used to append the config-failure diagnostic whenever anything was set
// aside, so a successful restore on a fresh install was logged as "could not
// finish". With nothing config-dependent deferred, the config failure prevented
// no recovery work and must not be reported.
func TestRestoreUncommittedOAuthAsidesReportsNoConfigFailureForACredentialOnlyRecovery(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if _, err := os.Stat(f.tomlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: %s exists (stat err = %v), want a fresh install", f.tomlPath, err)
	}
	const stamp = "1757000000000000000"
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	const credentialBytes = "the only credential the instance ever had\n"
	inflight := parkedAt(t, f, "openai-codex", stamp, credentialBytes)
	removalAt(t, f, "openai-codex", false, false, "1757000000000000000")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the credential-only copy restored with no config diagnostic", restored, err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the credential-only copy put back")
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || string(got) != credentialBytes {
		t.Fatalf("the record = %q (%v), want the credential-only copy put back", got, rerr)
	}
	if _, statErr := os.Lstat(inflight); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the parked copy is still on disk (Lstat = %v), want it moved", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesRecoversCredentialOnlyCopiesWhenTheConfigCannotBeRead:
// a credential-only removal's own record does not need providers.toml - the
// record's phase is the evidence - so an unrelated config failure must not
// strand that credential. A config-dependent removal of ANOTHER name is deferred
// untouched, and the config failure is reported so a partial pass never reads as
// clean.
func TestRestoreUncommittedOAuthAsidesRecoversCredentialOnlyCopiesWhenTheConfigCannotBeRead(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// An unreadable providers.toml: a directory stands where the file was, so
	// ReadConfigFile fails rather than returning an empty layer.
	if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", f.tomlPath, err)
	}
	const stamp = "1757000000000000000"
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	const credentialBytes = "the only credential the instance ever had\n"
	inflight := parkedAt(t, f, "openai-codex", stamp, credentialBytes)
	removalAt(t, f, "openai-codex", false, false, "1757000000000000000")
	// A config-backed removal the config would have to judge: it must not be
	// resolved in a pass without one.
	deferred := parkedAt(t, f, "retired", stamp, "a config-backed removal in doubt\n")
	removalAt(t, f, "retired", true, false, "1757000000000000000")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
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
		t.Fatalf("the parked credential-only copy is still on disk (Lstat = %v), want it moved", statErr)
	}
	if _, statErr := os.Lstat(deferred); statErr != nil {
		t.Fatalf("the config-dependent copy was taken (%v), want it left for a pass that can read the config", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesDefersConfigBackedCopiesWithoutAConfigPath:
// main.go passes providersConfigPath == "" when EVENER_PROVIDERS_CONFIG is
// present and empty, which the tri-state rule reads as "no user layer at all".
// That is not evidence that a removal reached its providers.toml write, so
// recovery must treat it exactly as a config it could not read: resolve the
// removals whose own record is the evidence, defer every config-dependent one
// untouched, and report the situation so the partial pass is never read as
// clean.
func TestRestoreUncommittedOAuthAsidesDefersConfigBackedCopiesWithoutAConfigPath(t *testing.T) {
	f := newInstancesFixture(t, nil)
	const stamp = "1757000000000000000"
	// A config-backed removal: without a config it cannot be judged, so it must
	// not be resolved (and its copy not swept).
	configBacked := parkedAt(t, f, "work", stamp, "an in-doubt removal's only credential\n")
	removalAt(t, f, "work", true, false, "1757000000000000000")
	// A credential-only removal whose record says it landed: its phase is the
	// evidence, so the sweep runs (and reports the lone credential it takes)
	// without the config.
	landed := parkedAt(t, f, "retired", stamp, "a standing removal's copy\n")
	removalAt(t, f, "retired", false, true, "1757000000000000000")
	// A credential-only removal in doubt: its recovery does not consult the
	// config either, so it must complete.
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	const credentialBytes = "the credential-only instance's record\n"
	parkedAt(t, f, "openai-codex", stamp, credentialBytes)
	removalAt(t, f, "openai-codex", false, false, "1757000000000000000")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, "", f.store)
	if err == nil {
		t.Fatal(`restoreUncommittedOAuthAsides(state, "", nil) = nil, want the missing config path reported`)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the credential-only half completed without a config")
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || string(got) != credentialBytes {
		t.Fatalf("the credential-only record = %q (%v), want it put back without a config", got, rerr)
	}
	if b, rerr := os.ReadFile(configBacked); rerr != nil || string(b) != "an in-doubt removal's only credential\n" {
		t.Fatalf("the config-backed copy = %q (%v), want it deferred untouched without a config", b, rerr)
	}
	if _, statErr := os.Lstat(landed); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the landed removal's copy survived (Lstat = %v), want its own record's phase to have swept it", statErr)
	}
	if !strings.Contains(err.Error(), "deleted the parked copy "+filepath.Base(landed)) {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the swept credential-only copy reported", err)
	}
}

// TestRestoreUncommittedOAuthAsidesDefersWhenTheConfigFileIsMissing: the other
// absent-config spelling. For a non-empty path whose file does not exist,
// ReadConfigFile returns an empty layer with a nil error, which is not evidence
// that a removal reached its providers.toml write: a fresh install, a broken
// symlink, or a config removed while the auth directory kept its copies all read
// that way. The pass must treat it exactly as an unreadable config - resolve the
// credential-only half, defer every config-dependent record untouched, and
// report the missing file - not resolve config-backed removals forward.
func TestRestoreUncommittedOAuthAsidesDefersWhenTheConfigFileIsMissing(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// A path inside a directory that exists, where the file does not: distinct
	// from the empty-path spelling.
	if _, err := os.Stat(filepath.Dir(f.tomlPath)); err != nil {
		t.Fatalf("fixture: the config's directory is missing: %v", err)
	}
	if _, err := os.Stat(f.tomlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: %s exists (stat err = %v), want a missing config file", f.tomlPath, err)
	}
	const stamp = "1757000000000000000"
	configBacked := parkedAt(t, f, "work", stamp, "an in-doubt removal's only credential\n")
	removalAt(t, f, "work", true, false, "1757000000000000000")
	record := authopenai.AuthFilePath(f.stateDir, "openai-codex")
	const credentialBytes = "the credential-only instance's record\n"
	parkedAt(t, f, "openai-codex", stamp, credentialBytes)
	removalAt(t, f, "openai-codex", false, false, "1757000000000000000")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil || !strings.Contains(err.Error(), "no providers config file at "+f.tomlPath) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the missing config file named", restored, err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the credential-only half completed without the config")
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || string(got) != credentialBytes {
		t.Fatalf("the credential-only record = %q (%v), want it put back without a readable config", got, rerr)
	}
	if b, rerr := os.ReadFile(configBacked); rerr != nil || string(b) != "an in-doubt removal's only credential\n" {
		t.Fatalf("the config-backed copy = %q (%v), want it deferred untouched", b, rerr)
	}
}

// TestRestoreUncommittedOAuthAsidesDefersAWholeInstanceWhoseConfigCouldNotBeRead:
// a config that cannot be read defers every config-dependent record. A
// credential-only record of the SAME instance must be held back with it: its
// copy is older than the config-backed one, so resolving it first would take the
// record path, and when the config later reads, the newer copy would be skipped
// as the path is occupied - a stale credential standing in for the current one.
// The deferral applies to the whole instance, so nothing goes back until the
// config can judge it.
func TestRestoreUncommittedOAuthAsidesDefersAWholeInstanceWhoseConfigCouldNotBeRead(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// An unreadable providers.toml: a directory stands where the file was, so
	// ReadConfigFile fails rather than returning an empty layer.
	if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", f.tomlPath, err)
	}
	record := authopenai.AuthFilePath(f.stateDir, "work")
	older := parkedAt(t, f, "work", "100", "the stale credential-only copy\n")
	newer := parkedAt(t, f, "work", "200", "the current config-backed copy\n")
	removalAt(t, f, "work", false, false, "90")
	removalAt(t, f, "work", true, false, "190")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil || !strings.Contains(err.Error(), f.tomlPath) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the unreadable config reported", restored, err)
	}
	// The crux: the older credential-only copy must not have taken the record
	// path, which would leave a stale credential standing in for the current one.
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record path holds bytes (Lstat = %v), want neither copy put back while the config cannot judge the instance", statErr)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want nothing put back while the config cannot judge the instance")
	}
	for _, path := range []string{older, newer} {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("the deferred copy %s was taken (%v), want it left for a readable config", path, statErr)
		}
	}
	if !strings.Contains(err.Error(), "held back the credential-only records") {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the whole-instance deferral reported", err)
	}

	// A readable config that carries the name: the same classification the first
	// pass would have made had it deferred everything. The newer copy goes back,
	// and the older one is superseded by it - deleted, because the record path
	// now holds the newest bytes and nothing else would ever collect it.
	if err := os.Remove(f.tomlPath); err != nil {
		t.Fatalf("Remove(%s): %v", f.tomlPath, err)
	}
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	restored, err = restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the newer copy put back once the config reads")
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || string(got) != "the current config-backed copy\n" {
		t.Fatalf("restored bytes = %q (%v), want the newer copy %q", got, rerr, "the current config-backed copy\n")
	}
	if _, statErr := os.Lstat(older); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the older copy survives at %s (Lstat = %v), want the newer copy put back and the older deleted", older, statErr)
	}
	if _, statErr := os.Lstat(newer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the newer copy is still on disk (Lstat = %v), want it moved to the record path", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesResolvesAWholeInstanceForward: a removal that
// stood settles the instance, not just its own copy. A config-backed removal
// record whose name the config no longer carries proves the removal reached its
// providers.toml write, and that fact is dated by its own copy's stamp. An OLDER
// record of the same name - a different removal, a different kind, a smaller
// stamp - is covered by that proof and must be swept with it rather than
// restored: the instance the older credential belonged to is gone.
func TestRestoreUncommittedOAuthAsidesResolvesAWholeInstanceForward(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// A readable providers.toml that does not carry the instance's name.
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	record := authopenai.AuthFilePath(f.stateDir, "gone")
	older := parkedAt(t, f, "gone", "100", "the older credential-only copy\n")
	newer := parkedAt(t, f, "gone", "200", "the newer config-backed copy\n")
	removalAt(t, f, "gone", false, false, "90")
	removalAt(t, f, "gone", true, true, "190")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want the whole instance resolved forward rather than restored")
	}
	// The crux: the older credential-only copy must not have taken the record
	// path. It is swept by the later removal's proof, and the delete of a
	// credential-only copy with a free record path is the natural report.
	if err == nil || !strings.Contains(err.Error(), "deleted the parked copy "+filepath.Base(older)) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the swept credential-only copy %s reported", restored, err, filepath.Base(older))
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record path holds bytes (Lstat = %v), want the older credential-only copy swept rather than restored", statErr)
	}
	for _, name := range []string{older, newer} {
		if _, statErr := os.Lstat(name); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the copy %s survived (Lstat = %v), want both swept so nothing is left to restore", name, statErr)
		}
	}
	// Nothing of either removal may survive to be restored by a later pass.
	if restored2, err2 := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); restored2 || err2 != nil {
		t.Fatalf("a later pass = (%v, %v), want nothing left to restore", restored2, err2)
	}
}

// TestRestoreUncommittedOAuthAsidesRestoresACopyNewerThanTheProof: the proof a
// standing removal leaves is DATED. A config-backed removal record whose name
// the config no longer carries proves the removal reached its providers.toml
// write only as of its own copy's stamp. A NEWER record of the same name is a
// later, different removal, and its interrupted removal has no such proof -
// putting its credential back is exactly what keeps a re-sign-in from being lost.
// Resolving the whole instance forward would sweep it instead and permanently
// delete the newer credential.
func TestRestoreUncommittedOAuthAsidesRestoresACopyNewerThanTheProof(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// A readable providers.toml that does not carry the instance's name.
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	record := authopenai.AuthFilePath(f.stateDir, "gone")
	proof := parkedAt(t, f, "gone", "100", "the older config-backed proof\n")
	newer := parkedAt(t, f, "gone", "200", "the newer credential-only copy a later re-sign-in left\n")
	removalAt(t, f, "gone", true, true, "90")
	removalAt(t, f, "gone", false, false, "190")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the copy newer than the proof put back")
	}
	// The crux: the newer credential-only copy must not be reported as a swept
	// credential, because it is not one.
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want no problem reported", err)
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || string(got) != "the newer credential-only copy a later re-sign-in left\n" {
		t.Fatalf("the record = %q (%v), want the stamp-200 credential-only copy restored", got, rerr)
	}
	if _, statErr := os.Lstat(newer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the copy %s survived (Lstat = %v), want it moved to the record path", newer, statErr)
	}
	// The older config-backed proof is swept, silently: its removal stood.
	if _, statErr := os.Lstat(proof); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the proof copy %s survived (Lstat = %v), want it swept", proof, statErr)
	}
	if restored2, err2 := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); restored2 || err2 != nil {
		t.Fatalf("a later pass = (%v, %v), want nothing left to restore", restored2, err2)
	}
}

// TestRestoreUncommittedOAuthAsidesResolvesANewerConfigBackedCopy: every
// config-backed removal whose name the config does not carry resolves itself
// forward, whatever its stamp. Two of them with a proof recorded between them
// must both be swept, with no problem reported and nothing left for a later
// pass.
func TestRestoreUncommittedOAuthAsidesResolvesANewerConfigBackedCopy(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	record := authopenai.AuthFilePath(f.stateDir, "gone")
	older := parkedAt(t, f, "gone", "100", "the older config-backed proof\n")
	newer := parkedAt(t, f, "gone", "200", "the newer config-backed proof\n")
	removalAt(t, f, "gone", true, true, "90")
	removalAt(t, f, "gone", true, true, "190")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want every config-backed removal resolved forward")
	}
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want no problem reported", err)
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record path holds bytes (Lstat = %v), want nothing restored", statErr)
	}
	for _, name := range []string{older, newer} {
		if _, statErr := os.Lstat(name); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the copy %s survived (Lstat = %v), want both swept", name, statErr)
		}
	}
	if restored2, err2 := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); restored2 || err2 != nil {
		t.Fatalf("a later pass = (%v, %v), want nothing left to restore", restored2, err2)
	}
}

// TestRestoreUncommittedOAuthAsidesTreatsASameStampRecordAndCopyAsOneMutation:
// a mutation writes its record BEFORE it parks anything, and both stamps come
// from the same clock, so a clock too coarse to separate the two reads leaves the
// record and its copy carrying the SAME stamp. That equality is ownership, and
// read the only way the write order allows: a record at or below a copy's stamp
// is one that could have parked it. The two halves below show what that buys -
// the config-backed copy is swept SILENTLY (an orphan of an uncarried name would
// be reported), and the credential-only copy is put back although the config
// carries no entry for its instance (as an orphan it would need one).
func TestRestoreUncommittedOAuthAsidesTreatsASameStampRecordAndCopyAsOneMutation(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	// (a) A landed config-backed removal of a name the config does not carry, its
	// record and its copy at one stamp.
	swept := parkedAt(t, f, "gone", "100", "the copy parked in the record's own tick\n")
	removalAt(t, f, "gone", true, true, "100")
	// (b) A started credential-only removal, its record and its copy at one stamp.
	const content = "the credential-only instance's record\n"
	parkedAt(t, f, "openai-codex", "200", content)
	removalAt(t, f, "openai-codex", false, false, "200")

	got, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the config-backed sweep silent: a copy the record's own stamp claims is that removal's, not an orphan", got, err)
	}
	if !got {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the same-stamp credential-only copy put back")
	}
	if _, statErr := os.Lstat(swept); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the config-backed removal's copy survived (Lstat = %v), want it swept by the record that stood", statErr)
	}
	if b, rerr := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "openai-codex")); rerr != nil || string(b) != content {
		t.Fatalf("the credential-only record = %q (%v), want the same-stamp copy put back", b, rerr)
	}
	assertNoIntentRecords(t, f)
	if again, err2 := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); again || err2 != nil {
		t.Fatalf("a later pass = (%v, %v), want nothing left to resolve", again, err2)
	}
}

// TestRestoreUncommittedOAuthAsidesResolvesACopyAtTheProofStamp: the boundary
// the proof's OWN stamp defines, reachable because a record and the copy it
// parked can share a stamp - they take it from the same clock and are filed under
// different markers, so a clock too coarse to separate the two reads leaves
// `gone.json.intent-100` and `gone.json.removing-100` side by side. The standing
// removal's proof is dated at or before its own copy's stamp (at-or-before, not
// strictly before), so that copy is covered and swept rather than restored; it is
// REPORTED, because a started credential-only removal of the same name precedes it
// and a delete must not be silent when the bytes could have been a credential's
// only copy; and both records are spent, so nothing is left for a later pass.
func TestRestoreUncommittedOAuthAsidesResolvesACopyAtTheProofStamp(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	atProof := parkedAt(t, f, "gone", "100", "the copy parked at the proof's own stamp\n")
	removalAt(t, f, "gone", false, false, "90")
	removalAt(t, f, "gone", true, true, "100")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want the copy at the proof's stamp resolved forward")
	}
	if err == nil || !strings.Contains(err.Error(), "deleted the parked copy "+filepath.Base(atProof)) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the swept copy %s reported", restored, err, filepath.Base(atProof))
	}
	if _, statErr := os.Lstat(atProof); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the copy %s survived (Lstat = %v), want it swept at the proof's own stamp", atProof, statErr)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "gone")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record path holds bytes (Lstat = %v), want the standing removal left alone", statErr)
	}
	assertNoIntentRecords(t, f)
	if again, err2 := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); again || err2 != nil {
		t.Fatalf("a later pass = (%v, %v), want nothing left to resolve", again, err2)
	}
}

// TestRestoreUncommittedOAuthAsidesResolvesEveryCopyOfOneRemovalAsOne: a removal
// that stood settles ALL the copies filed under its name - the one it parked and
// any an earlier failed removal left behind - so nothing of it is left for a
// later pass to restore, and the sweep reports the copies that could have been
// the last bytes of that credential.
func TestRestoreUncommittedOAuthAsidesResolvesEveryCopyOfOneRemovalAsOne(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	first := parkedAt(t, f, "retired", "100", "the copy an earlier failed removal left\n")
	second := parkedAt(t, f, "retired", "200", "the copy the removal parked\n")
	removalAt(t, f, "retired", false, true, "90")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want no copy of a landed removal put back")
	}
	for _, path := range []string{first, second} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the copy %s survived (Lstat = %v), want every copy of the landed removal swept", path, statErr)
		}
		if !strings.Contains(err.Error(), filepath.Base(path)) {
			t.Fatalf("restoreUncommittedOAuthAsides = %v, want the swept copy %s reported", err, filepath.Base(path))
		}
	}
	assertNoIntentRecords(t, f)
	if restored2, err2 := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); restored2 || err2 != nil {
		t.Fatalf("a later pass = (%v, %v), want nothing left to restore", restored2, err2)
	}
}

// TestRestoreUncommittedOAuthAsidesKeepsAnUnreadableRecordAndDefersItsName: a
// record that does not read back is KEPT and reported, never guessed at. Its
// name is held back whole - nothing is restored and nothing swept - because a
// record that cannot be classified might have stood or might not, and either
// guess could delete a credential or resurrect a removal.
func TestRestoreUncommittedOAuthAsidesKeepsAnUnreadableRecordAndDefersItsName(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	const stamp = "1757000000000000000"
	copyPath := parkedAt(t, f, "retired", stamp, "the credential of an unreadable record\n")
	if err := os.MkdirAll(oauthDir(f), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	recordPath := filepath.Join(oauthDir(f), "retired.json"+oauthIntentMarker+"1757000000000000001")
	if err := os.WriteFile(recordPath, []byte("op=remove\ninst=retired\nkind=nonsense\nphase=started\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", recordPath, err)
	}

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil || !strings.Contains(err.Error(), recordPath) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the unreadable record %s reported", restored, err, recordPath)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want nothing resolved for a name whose record cannot be read")
	}
	if _, statErr := os.Lstat(copyPath); statErr != nil {
		t.Fatalf("the copy was taken (%v), want it kept for the pass that can classify it", statErr)
	}
	if _, statErr := os.Lstat(recordPath); statErr != nil {
		t.Fatalf("the record was taken (%v), want it kept and reported", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesReportsNothingRecoveredWhenTheDirectoryCannotBeListed:
// an auth directory that exists but cannot be listed leaves the pass with no
// entries at all: no copy was put back and none was classified, so a credential
// may still be sitting in flight under it. The report must say that - the
// wording it used to borrow describes which copies were put back and which were
// deferred, and claiming either would mask the credential still there.
func TestRestoreUncommittedOAuthAsidesReportsNothingRecoveredWhenTheDirectoryCannotBeListed(t *testing.T) {
	f := newInstancesFixture(t, nil)
	dir := oauthDir(f)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const content = "a credential still in flight\n"
	aside := parkedAt(t, f, "work", "1757000000000000000", content)
	// A config that cannot be read either, so both halves of the report run.
	if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", f.tomlPath, err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("Chmod(%s): %v", dir, err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("restore Chmod(%s): %v", dir, err)
		}
	}()

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil {
		t.Fatal("restoreUncommittedOAuthAsides = nil, want the unlistable directory reported")
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want nothing recovered")
	}
	if !strings.Contains(err.Error(), "nothing was recovered") {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the report to say nothing was recovered", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the directory it could not list named as %s", err, dir)
	}
	if !strings.Contains(err.Error(), f.tomlPath) {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the config it could not read named too", err)
	}
	if strings.Contains(err.Error(), "were put back") {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want no claim about what was put back: nothing was read", err)
	}
	// The bytes are still where they were, which is what the report is about.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Chmod(%s): %v", dir, err)
	}
	if got, rerr := os.ReadFile(aside); rerr != nil || string(got) != content {
		t.Fatalf("the parked copy = %q (%v), want it untouched at %s", got, rerr, aside)
	}
}

// ---- recovery: orphans (copies no record claims) ----

// TestRestoreUncommittedOAuthAsidesPutsBackTheNewestOrphanTheConfigCarries: a
// parked copy with no record at all is debris a completed removal left behind -
// or the leftover of a failed rollback whose record is gone - and the config is
// what decides it. A name the config carries gets its NEWEST copy back (an older
// one is a credential the instance no longer has), whether or not the record
// path is free: bytes already there are the record the instance has now.
func TestRestoreUncommittedOAuthAsidesPutsBackTheNewestOrphanTheConfigCarries(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	path := authopenai.AuthFilePath(f.stateDir, "work")
	older := parkedAt(t, f, "work", "1757000000000000000", "the older credential\n")
	newer := parkedAt(t, f, "work", "1757000000000000001", "the newer credential\n")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if !restored {
		t.Fatal("restoreUncommittedOAuthAsides = false, want the newest orphan put back")
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || string(got) != "the newer credential\n" {
		t.Fatalf("the record = %q (%v), want the newest orphan's bytes", got, rerr)
	}
	if _, statErr := os.Lstat(older); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the older copy survived (Lstat = %v), want it superseded by the one put back", statErr)
	}
	if _, statErr := os.Lstat(newer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the newer copy is still on disk (Lstat = %v), want it moved", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesSweepsAnOrphanTheConfigDoesNotCarry: a parked
// copy of a name nothing carries is debris of a removal that stood - no later
// removal of that name will ever collect it - so it is swept rather than left on
// disk forever. It could have been the last bytes of that credential, so the
// delete is REPORTED when the record path is free rather than silent.
func TestRestoreUncommittedOAuthAsidesSweepsAnOrphanTheConfigDoesNotCarry(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	orphan := parkedAt(t, f, "retired", "1757000000000000000", "an orphaned removal\n")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want an orphan of an uncarried name never put back")
	}
	if err == nil || !strings.Contains(err.Error(), "deleted the parked copy "+filepath.Base(orphan)) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the swept orphan %s reported", restored, err, filepath.Base(orphan))
	}
	if _, statErr := os.Lstat(orphan); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the orphan survived (Lstat = %v), want it swept", statErr)
	}
	if restored2, err2 := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); restored2 || err2 != nil {
		t.Fatalf("a later pass = (%v, %v), want nothing left to restore", restored2, err2)
	}
}

// TestRestoreUncommittedOAuthAsidesDefersAnOrphanWithoutAConfig: an orphan has
// no record to say what it is for, so the config is the whole of its
// classification - and a config the pass cannot read leaves it exactly where it
// is, with the failure reported. Guessing would either sweep a credential whose
// instance still exists or put back one whose removal stood.
func TestRestoreUncommittedOAuthAsidesDefersAnOrphanWithoutAConfig(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", f.tomlPath, err)
	}
	orphan := parkedAt(t, f, "retired", "1757000000000000000", "an orphan whose name nothing can judge\n")

	restored, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil || !strings.Contains(err.Error(), f.tomlPath) {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the unreadable config reported", restored, err)
	}
	if restored {
		t.Fatal("restoreUncommittedOAuthAsides = true, want nothing put back without a readable config")
	}
	if _, statErr := os.Lstat(orphan); statErr != nil {
		t.Fatalf("the orphan was taken (%v), want it left for a pass that can read the config", statErr)
	}
}

// ---- renameNoReplace ----

// TestRenameNoReplaceMovesWithoutHardLinking: the move is a single rename(2)
// after a destination check, so a filesystem without hard links (exFAT/FAT,
// some SMB/NFS configs) no longer matters - no link is attempted, and no crash
// can leave the bytes under both names the way link-then-unlink could. The move
// still lands the source's bytes at the destination and leaves the source gone.
func TestRenameNoReplaceMovesWithoutHardLinking(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	const content = "the only copy of the bytes\n"
	if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(src): %v", err)
	}

	if err := renameNoReplace(src, dst); err != nil {
		t.Fatalf("renameNoReplace = %v, want the single-syscall move", err)
	}
	if b, rerr := os.ReadFile(dst); rerr != nil || string(b) != content {
		t.Fatalf("destination bytes = %q (%v), want the source moved there", b, rerr)
	}
	if _, statErr := os.Lstat(src); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source still on disk (Lstat = %v), want it moved", statErr)
	}
}

// TestRenameNoReplaceRefusesATakenDestination: the no-replace guarantee stands -
// a taken destination is refused, never replaced, and neither file's bytes
// change.
func TestRenameNoReplaceRefusesATakenDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("source bytes\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(src): %v", err)
	}
	if err := os.WriteFile(dst, []byte("destination bytes\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(dst): %v", err)
	}
	if err := renameNoReplace(src, dst); !errors.Is(err, os.ErrExist) {
		t.Fatalf("renameNoReplace = %v, want an os.ErrExist refusal for a taken destination", err)
	}
	if b, rerr := os.ReadFile(dst); rerr != nil || string(b) != "destination bytes\n" {
		t.Fatalf("destination bytes = %q (%v), want the taken file left untouched", b, rerr)
	}
	if b, rerr := os.ReadFile(src); rerr != nil || string(b) != "source bytes\n" {
		t.Fatalf("source bytes = %q (%v), want the source left in place", b, rerr)
	}
}

// TestRenameNoReplaceRefusesSourceEqualsDestination: rename(2) on a path onto
// itself is a no-op success, and a caller that reaches for a name the source
// already holds has mistaken it for a free one: reporting such a move as landed
// when nothing happened is the failure this refusal keeps out.
func TestRenameNoReplaceRefusesSourceEqualsDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "work.json.removing-5")
	if err := os.WriteFile(path, []byte("the copy's bytes\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := renameNoReplace(path, path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("renameNoReplace = %v, want an os.ErrExist refusal when source equals destination", err)
	}
	if b, rerr := os.ReadFile(path); rerr != nil || string(b) != "the copy's bytes\n" {
		t.Fatalf("bytes = %q (%v), want the copy left untouched", b, rerr)
	}
}

// ---- the rename's live path ----

// TestInstances_EditRenameCarriesAnInFlightOAuthCopy: a failed removal can leave
// an instance's only record as a parked copy under its own name
// (restoreFailedRemoval's rename-back failed). A rename used to carry only the
// live record, so once providers.toml named only the new instance the copy was
// stranded: startup recovery no longer recognized the old name and the
// credential was unrecoverable. The rename must carry it, and - because the old
// record path is free - promote the newest copy to that path first so the carry
// reads it as the record and the credential is usable under the new name at
// once. No copy may be left filed under the old name.
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
	original, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	parkedAt(t, f, "work", "1757000000000000000", string(original))
	if err := os.Remove(record); err != nil {
		t.Fatalf("Remove(%s): %v", record, err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	// Premise: the parked copy is the instance's only record.
	if _, err := os.Lstat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: %s still exists (Lstat = %v), want the record only as a copy", record, err)
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
	if left := parkedNames(t, f, "work"); len(left) != 0 {
		t.Fatalf("the copies %v are still filed under the old name, stranding them", left)
	}
	assertNoIntentRecords(t, f)
}

// TestInstances_EditRenameReportsAnOAuthCopyItCouldNotCarry: a rename that
// cannot carry a copy to the new name still stands (providers.toml already names
// the new instance), so the failure is reported the way the other carry
// failures are - through Edit's renamePersistedError - and the copy's bytes must
// not be destroyed. The fallback that re-files it under the NEW name has no fresh
// name to take here (the copy's stamp is MaxInt64, the greatest a copy name can
// carry, and the new name's deterministic candidate is taken), so the report
// names the copy still filed under the old name - and the rename's own record
// stays, which is what keeps startup from reading those bytes as debris of a
// name the config no longer carries: the completion owns the name and reports the
// same failure again instead of letting the sweep take the renamed instance's
// only credential. A live record keeps the old record path occupied, so the copy
// is carried rather than promoted.
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
	dir := oauthDir(f)
	// The stamp no successor can pass, so the search cannot offer the carry a
	// fresh name once the new name holds a copy at the same stamp.
	stamp := strconv.FormatInt(maxAsideStamp, 10)
	source := filepath.Join(dir, "work.json"+oauthAsideMarker+stamp)
	const content = "the renamed instance's only credential copy\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", source, err)
	}
	// The obstacle occupies the only candidate the fresh-stamp search can offer,
	// so the carry cannot land rather than stepping past it.
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
	// There is no fresh name to re-file the copy under, so the report must say so
	// and name the copy still filed under the old name.
	if !strings.Contains(err.Error(), filepath.Base(source)) {
		t.Fatalf("Edit = %v, want it to name the copy still filed under the old name as %s", err, filepath.Base(source))
	}
	if got, rerr := os.ReadFile(source); rerr != nil || string(got) != content {
		t.Fatalf("the copy = %q (%v), want its bytes left under the old name", got, rerr)
	}
	records := intentNames(t, f)
	if len(records) != 1 {
		t.Fatalf("the rename's records = %v, want the one record that keeps the unfinished move recoverable", records)
	}
	authoredEntry(t, f.tomlPath, "personal")

	// Startup recovery resolves the copy the rename could not carry: the record
	// owns the name, so it promotes the copy onto the old record path and files it
	// under the NEW name (as a parked copy, since that name's record already
	// exists), then spends the record. The renamed instance's bytes are reachable
	// under the name the config carries rather than swept as debris of the old
	// one.
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil || !restored {
		t.Fatalf("restoreUncommittedOAuthAsides = (%v, %v), want the stranded copy resolved under the new name", restored, rerr)
	}
	refiled := parkedNames(t, f, "personal")
	if len(refiled) != 1 {
		t.Fatalf("personal's copies = %v, want the bytes filed there", refiled)
	}
	if got, rerr := os.ReadFile(filepath.Join(oauthDir(f), refiled[0])); rerr != nil || string(got) != content {
		t.Fatalf("the re-filed copy = %q (%v), want %q", got, rerr, content)
	}
	if _, statErr := os.Lstat(source); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the copy is still filed under the old name (Lstat = %v), want it resolved", statErr)
	}
	assertNoIntentRecords(t, f)
}

// TestInstances_EditRenameKeepsAnUnparseablePromotedRecordRecoverable: the
// rename promotes the newest parked copy to the old record path so
// moveCredentials can read it. When that read refuses the bytes (an unparseable
// record), leaving them at the canonical old-name path would strand them - no
// reader looks there once providers.toml names the new instance, and startup
// recovery does not recognize a plain record as a copy. The bytes must move
// back under a parked copy for the NEW name (stamp preserved), and the carry
// problem must be reported the way the other carry failures are.
func TestInstances_EditRenameKeepsAnUnparseablePromotedRecordRecoverable(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	const stamp = "1757000000000000000"
	const content = "a record the hub cannot parse\n"
	parkedAt(t, f, "work", stamp, content)

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the unparseable promoted record reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit = %v (%T), want a renamePersistedError", err, err)
	}
	newAside := "personal.json" + oauthAsideMarker + stamp
	if !strings.Contains(err.Error(), newAside) {
		t.Fatalf("Edit = %v, want it to name the parked copy %s", err, newAside)
	}
	got, rerr := os.ReadFile(filepath.Join(oauthDir(f), newAside))
	if rerr != nil {
		t.Fatalf("the bytes are not under the parked copy %s: %v", newAside, rerr)
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
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
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
// carry names the new instance's copy from the OLD copy's stamp, so a copy
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
	dir := oauthDir(f)
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

// TestInstances_EditRenameDoesNotClobberATakenRecoveryAside: when the promoted
// record cannot be read back, its bytes are re-filed as a parked copy for the
// new name, chosen from the copy's stamp. A file already at that deterministic
// name is another credential's bytes, so the move must pick a fresh name
// (freeAsideName) and never replace what is there (renameNoReplace). The taken
// file survives untouched, the carried bytes still land under a name startup
// recovery restores to the renamed instance, and the carry problem is reported
// the way the other carry problems are.
func TestInstances_EditRenameDoesNotClobberATakenRecoveryAside(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	const stamp = "1757000000000000000"
	const content = "a record the hub cannot parse\n"
	parkedAt(t, f, "work", stamp, content)
	taken := filepath.Join(oauthDir(f), "personal.json"+oauthAsideMarker+stamp)
	const takenBytes = "a stale copy already filed under the new name\n"
	if err := os.WriteFile(taken, []byte(takenBytes), 0o600); err != nil {
		t.Fatalf("WriteFile(taken): %v", err)
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the unparseable promoted record reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit = %v (%T), want a renamePersistedError", err, err)
	}
	if got, rerr := os.ReadFile(taken); rerr != nil || string(got) != takenBytes {
		t.Fatalf("the taken copy = %q (%v), want its bytes untouched", got, rerr)
	}
	fresh := "personal.json" + oauthAsideMarker + "1757000000000000001"
	if !strings.Contains(err.Error(), fresh) {
		t.Fatalf("Edit = %v, want the copy filed under the fresh copy name %s", err, fresh)
	}
	if got, rerr := os.ReadFile(filepath.Join(oauthDir(f), fresh)); rerr != nil || string(got) != content {
		t.Fatalf("the carried bytes = %q (%v), want %q under %s", got, rerr, content, fresh)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "work")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the unreadable record was left at the old canonical path (Lstat = %v)", statErr)
	}

	// Startup recovery puts the carried bytes back for the renamed instance.
	record := authopenai.AuthFilePath(f.stateDir, "personal")
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup did not restore the carried record under the new name")
	}
	if got, rerr := os.ReadFile(record); rerr != nil || string(got) != content {
		t.Fatalf("restored bytes = %q (%v), want the carried %q", got, rerr, content)
	}
}

// TestInstances_EditRenameCarriesCopiesInNumericStampOrder: os.ReadDir hands
// entries back lexically, so "...removing-10" precedes "...removing-9".
// Re-stamping in that order bumps the numerically older 9 above the newer 10
// (the search steps to highest+1 when the source stamp is not greater), and
// recovery restores the newest copy - so the older credential would win. A live
// record keeps the old record path occupied, so both copies are carried rather
// than one promoted. The newer copy must keep the greater carried stamp, and a
// subsequent recovery must restore it.
func TestInstances_EditRenameCarriesCopiesInNumericStampOrder(t *testing.T) {
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
	const olderBytes = "the older credential\n"
	const newerBytes = "the newer credential\n"
	parkedAt(t, f, "work", "9", olderBytes)
	parkedAt(t, f, "work", "10", newerBytes)

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}
	type copyAt struct {
		stamp int64
		body  string
	}
	var carried []copyAt
	for _, name := range parkedNames(t, f, "personal") {
		_, stampText, _ := parseOAuthAside(name)
		s, perr := strconv.ParseInt(stampText, 10, 64)
		if perr != nil {
			t.Fatalf("carried copy %q carries an unparseable stamp: %v", name, perr)
		}
		b, rerr := os.ReadFile(filepath.Join(oauthDir(f), name))
		if rerr != nil {
			t.Fatalf("ReadFile(%s): %v", name, rerr)
		}
		carried = append(carried, copyAt{s, string(b)})
	}
	if len(carried) != 2 {
		t.Fatalf("carried copies = %+v, want the two copies carried to the new name", carried)
	}
	var olderStamp, newerStamp int64
	for _, c := range carried {
		switch c.body {
		case olderBytes:
			olderStamp = c.stamp
		case newerBytes:
			newerStamp = c.stamp
		default:
			t.Fatalf("carried copy body = %q, want one of the two source copies", c.body)
		}
	}
	if newerStamp <= olderStamp {
		t.Fatalf("carried stamps older=%d newer=%d, want the newer copy to keep the greater stamp", olderStamp, newerStamp)
	}

	// Recovery puts the newer one back: remove the carried record so the newest
	// copy is the renamed instance's only credential.
	record := authopenai.AuthFilePath(f.stateDir, "personal")
	if err := os.Remove(record); err != nil {
		t.Fatalf("Remove(%s): %v", record, err)
	}
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup restored nothing, want the newest carried copy put back")
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || string(got) != newerBytes {
		t.Fatalf("restored bytes = %q (%v), want the newer credential %q", got, rerr, newerBytes)
	}
}

// TestInstances_EditRenameRefilesAnUncarriedUnrankableCopy: a stamp past an
// int64 is a name no removal wrote and one that cannot be ordered against the
// copies beside it. Giving it a fresh rank inside the carry's own ordering would
// turn an intentionally unorderable copy into the newest recovery candidate -
// but leaving it under the OLD name is not safe either: providers.toml now names
// only the new instance, so startup would read it as debris of a name the config
// no longer carries and sweep it, taking the renamed instance's only credential.
// It is re-filed under the NEW name (reported, bytes intact), where recovery
// leaves it for a human rather than ranking it.
func TestInstances_EditRenameRefilesAnUncarriedUnrankableCopy(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	const stamp = "99999999999999999999"
	const content = "the unorderable copy\n"
	source := parkedAt(t, f, "work", stamp, content)

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the un-carried copy reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit = %v (%T), want a renamePersistedError", err, err)
	}
	// The copy is no longer under the old name, and its bytes survived the move.
	if _, statErr := os.Lstat(source); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the copy is still at %s (Lstat = %v), want it re-filed", source, statErr)
	}
	refiled := parkedNames(t, f, "personal")
	if len(refiled) != 1 {
		t.Fatalf("copies filed under the renamed instance = %v, want the one re-filed copy", refiled)
	}
	if !strings.Contains(err.Error(), refiled[0]) {
		t.Fatalf("Edit = %v, want it to name the re-filed copy %s", err, refiled[0])
	}
	if got, rerr := os.ReadFile(filepath.Join(oauthDir(f), refiled[0])); rerr != nil || string(got) != content {
		t.Fatalf("the re-filed copy = %q (%v), want %q", got, rerr, content)
	}
}

// TestInstances_EditRenameFilesAStrandedUnreadableRecordUnderTheNewName: the
// live record can be unreadable (bytes the hub cannot parse) and the record path
// is what it sits at, so nothing is promoted and the carry has no copy to file.
// Leaving the file there strands it - providers.toml now names only the new
// instance, no recovery pass reads a plain record as a copy, and the sweep reads
// no other name - so it is filed under the NEW name as a parked copy recovery
// restores.
func TestInstances_EditRenameFilesAStrandedUnreadableRecordUnderTheNewName(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	const content = "a record the hub cannot parse\n"
	record := authopenai.AuthFilePath(f.stateDir, "work")
	if err := os.MkdirAll(oauthDir(f), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(record, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", record, err)
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the stranded record reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit = %v (%T), want a renamePersistedError", err, err)
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record is still at %s (Lstat = %v), want it filed under the new name", record, statErr)
	}
	refiled := parkedNames(t, f, "personal")
	if len(refiled) != 1 {
		t.Fatalf("copies filed under the renamed instance = %v, want the one re-filed record", refiled)
	}
	if !strings.Contains(err.Error(), refiled[0]) {
		t.Fatalf("Edit = %v, want it to name the copy it filed as %s", err, refiled[0])
	}

	// Startup recovery puts those bytes back for the renamed instance.
	restored, rerr := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if rerr != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", rerr)
	}
	if !restored {
		t.Fatal("startup restored nothing, want the renamed instance's record back")
	}
	got, rerr := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "personal"))
	if rerr != nil || string(got) != content {
		t.Fatalf("restored record = %q (%v), want %q", got, rerr, content)
	}
}

// TestInstances_EditRenameLeavesNoIntent: the ordinary path must not leak the
// transaction record.
func TestInstances_EditRenameLeavesNoIntent(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}
	assertNoIntentRecords(t, f)
}

// TestInstances_EditRenameRecordsAKeyOnlyRename: a stored key lives in
// credentials.toml, not in the auth directory, so a rename of a key-only
// instance still needs its record - a crash after providers.toml is written and
// before the key moves would otherwise strand the key with nothing to say it
// belongs to the new name. The record is written even when the auth directory
// does not exist yet: a rename creates it rather than skipping a key-only move.
func TestInstances_EditRenameRecordsAKeyOnlyRename(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := os.RemoveAll(oauthDir(f)); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	record, err := f.ctl.beginRename("work", "personal")
	if err != nil {
		t.Fatalf("beginRename: %v", err)
	}
	if record == "" {
		t.Fatal("beginRename = \"\", want a record: the stored key is a credential the rename moves")
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record is not on disk at %s: %v", record, statErr)
	}
	i := readIntent(t, record)
	if i.op != oauthOpRename || i.inst != "work" || i.new != "personal" {
		t.Fatalf("the record = %+v, want the rename of work to personal", i)
	}
	if recordIsLanded(t, record) {
		t.Fatal("the rename's record is filed as landed, want it in flight: a rename's landing is read from the config")
	}
}

// TestInstances_EditRenameKeepsItsRecordWhenTheKeyCannotMove: the record is the
// only thing that says the key still has to move, so it may not be spent while
// the key is still filed under the old name - otherwise the rename is recorded
// as finished and the credential never follows it.
func TestInstances_EditRenameKeepsItsRecordWhenTheKeyCannotMove(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// ONLY the stored-key move fails: the config write must still land, or Edit
	// returns at its write (the fixture keeps providers.toml beside the store)
	// before the spend this test is about is ever reached.
	breakCredentialWrites(t, f.credsPath)

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}); err == nil {
		t.Fatal("Edit = nil, want the key it could not move reported")
	}
	left := intentNames(t, f)
	if len(left) == 0 {
		t.Fatal("the rename spent its record although the stored key never moved")
	}
	if v, _ := f.store.Get("work"); v != "sk-work" {
		t.Fatalf("work = %q, want the key left in place by the failed move", v)
	}
}

// TestInstances_EditRenameResolvesAnEarlierRenameIntoTheNameFirst: a rename that
// never finished leaves its record naming the name a later rename is about to
// take away, with the credential still filed under the name it came from. The
// later rename resolves that record FIRST - moving the credential forward along
// the name chain - and only then records and performs its own rename, so nothing
// is left under a name neither the config nor any recovery pass reads.
func TestInstances_EditRenameResolvesAnEarlierRenameIntoTheNameFirst(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The earlier personal-to-work rename: its record, and a real record (so the
	// rename below can carry it) still filed under the name it came from.
	if err := authopenai.SaveAuth(f.stateDir, "personal", makeOAuthRecord("personal", "personal@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	earlier := renameAt(t, f, "personal", "work", "1757000000000000000")
	// The config names the name the earlier rename was heading for.
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "work2"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}
	if _, statErr := os.Lstat(earlier); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the earlier record survives at %s, want it resolved by the later rename", earlier)
	}
	rec, rerr := authopenai.LoadAuth(f.stateDir, "work2")
	if rerr != nil {
		t.Fatalf("the renamed instance has no record: %v", rerr)
	}
	if rec.AccessToken != "access-personal" || rec.Provider != "work2" {
		t.Fatalf("moved record = %+v, want the earlier credential carried along the name chain", rec)
	}
	if left := parkedNames(t, f, "personal"); len(left) != 0 {
		t.Fatalf("copies %v are still filed under the earlier name", left)
	}
	assertNoIntentRecords(t, f)
}

// TestInstances_EditRenameRefusesWhileAnEarlierRenameCannotBeFinished: when the
// earlier rename's record cannot be finished, the later rename must refuse
// rather than chain onto it - taking the name away would strand the credential
// under a name nothing reads. The refusal is a Conflict, and it names the
// unfinished rename and what it could not do.
func TestInstances_EditRenameRefusesWhileAnEarlierRenameCannotBeFinished(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The earlier rename still has a STORED KEY to move, and the credentials
	// store refuses writes, so its completion cannot land.
	if err := f.store.Set("personal", "sk-personal"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	earlier := renameAt(t, f, "personal", "work", "1757000000000000000")
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	credsDir := filepath.Dir(f.credsPath)
	if err := os.Chmod(credsDir, 0o555); err != nil {
		t.Fatalf("Chmod(%s): %v", credsDir, err)
	}
	defer func() {
		if err := os.Chmod(credsDir, 0o700); err != nil {
			t.Fatalf("restore Chmod(%s): %v", credsDir, err)
		}
	}()

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "work2"})
	if err == nil {
		t.Fatal("Edit = nil, want the unfinished earlier rename refused")
	}
	want := `renaming "work" to "work2" cannot start while an earlier rename into "work" is unfinished:`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Edit = %v, want it to carry %q", err, want)
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
		t.Fatalf("Edit = %v (%T), want appwire.Conflict", err, err)
	}
	if !strings.Contains(err.Error(), "stored key") {
		t.Fatalf("Edit = %v, want the unfinished work named", err)
	}
	// Nothing was taken and nothing new was recorded.
	if _, statErr := os.Lstat(earlier); statErr != nil {
		t.Fatalf("the earlier record was taken (%v), want it left for the pass that finishes it", statErr)
	}
	if got := intentNames(t, f); len(got) != 1 {
		t.Fatalf("the refused rename left the records %v, want only the earlier one", got)
	}
}

// ---- recovery: completing a rename ----

// TestRestoreUncommittedOAuthAsidesCompletesARenameWhosRecordWasPromoted: the
// carry promoted a copy to the OLD canonical record path and the hub died before
// moveCredentials saved it under the new name. providers.toml names only the new
// instance, so a record left at the old path is read by nothing; startup must
// complete the rename from the record, and spend it.
func TestRestoreUncommittedOAuthAsidesCompletesARenameWhosRecordWasPromoted(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	record := renameAt(t, f, "work", "personal", "1757000000000000000")
	const content = "the credential the rename carried\n"
	promoted := authopenai.AuthFilePath(f.stateDir, "work")
	if err := os.WriteFile(promoted, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", promoted, err)
	}

	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if _, statErr := os.Lstat(promoted); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record is still at the OLD path %s (Lstat = %v), want the rename completed", promoted, statErr)
	}
	got, rerr := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "personal"))
	if rerr != nil || string(got) != content {
		t.Fatalf("the renamed instance's record = %q (%v), want the promoted bytes at %s", got, rerr, authopenai.AuthFilePath(f.stateDir, "personal"))
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the rename record survives (Lstat = %v), want it spent once the rename completed", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesCompletesARenameBeforeTheCarryMovedAnything:
// the other crash point - the record is written and the carry has not run. The
// copies still filed under the old name belong to the renamed instance and must
// end up under the NEW name, where recovery reads them, rather than being
// resolved forward or swept as debris of a name the config no longer carries.
func TestRestoreUncommittedOAuthAsidesCompletesARenameBeforeTheCarryMovedAnything(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	record := renameAt(t, f, "work", "personal", "1757000000000000000")
	const content = "the only credential of work\n"
	inflight := parkedAt(t, f, "work", "1757000000000000000", content)

	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if _, statErr := os.Lstat(inflight); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the copy is still filed under the OLD name %s (Lstat = %v), want it carried", inflight, statErr)
	}
	// The completion carries the copy, promotes it (the old record path was free)
	// and moves it on to the renamed instance's record path, so the renamed
	// instance's credential is LIVE - the strongest outcome of the three, and the
	// one the promotion exists for.
	got, rerr := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "personal"))
	if rerr != nil || string(got) != content {
		t.Fatalf("the renamed instance's record = %q (%v), want the carried credential live", got, rerr)
	}
	if left := parkedNames(t, f, "work"); len(left) != 0 {
		t.Fatalf("copies %v are still filed under the old name", left)
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the rename record survives (Lstat = %v), want it spent once the rename completed", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesFinishesARenameWithTheNewRecordAlreadySaved:
// the third crash point - the record reached the new path and the hub died
// before the old one was deleted. The live record must survive untouched, and
// the stale bytes at the old path must not be lost either.
func TestRestoreUncommittedOAuthAsidesFinishesARenameWithTheNewRecordAlreadySaved(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	record := renameAt(t, f, "work", "personal", "1757000000000000000")
	const content = "the credential the rename carried\n"
	for _, path := range []string{authopenai.AuthFilePath(f.stateDir, "work"), authopenai.AuthFilePath(f.stateDir, "personal")} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}

	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	got, rerr := os.ReadFile(authopenai.AuthFilePath(f.stateDir, "personal"))
	if rerr != nil || string(got) != content {
		t.Fatalf("the renamed instance's record = %q (%v), want it left in place", got, rerr)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "work")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the stale record is still at the old path (Lstat = %v), want the rename finished", statErr)
	}
	if left := parkedNames(t, f, "personal"); len(left) != 1 {
		t.Fatalf("the new name's copies = %v, want the stale bytes preserved as one parked copy", left)
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the rename record survives (Lstat = %v), want it spent", statErr)
	}
}

// TestRestoreUncommittedOAuthAsidesCompletesARenameWithTheStoredKey: the record
// is written and providers.toml already names the new instance, but the move
// never ran. Both halves of the credential are still under the old name, and
// nothing but the record says so: startup must migrate the stored key
// (credentials.Store.Move) and the OAuth record, then spend the record - and it
// must be idempotent, so a second pass changes nothing.
func TestRestoreUncommittedOAuthAsidesCompletesARenameWithTheStoredKey(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	store := f.store
	if err := store.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	seedOAuthRecord(t, f, "work", "work@example.com")
	record := renameAt(t, f, "work", "personal", "1757000000000000000")

	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	store = f.store // the pass rewrote the file; read it back
	if v, _ := store.Get("work"); v != "" {
		t.Fatalf("the stored key is still under the old name: %q", v)
	}
	if v, _ := store.Get("personal"); v != "sk-work" {
		t.Fatalf("personal = %q, want the stored key the record migrated", v)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "work")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record is still under the old name (Lstat = %v)", statErr)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "personal")); statErr != nil {
		t.Fatalf("the record was not migrated to the new name: %v", statErr)
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record survives at %s, want it spent once its work landed", record)
	}

	// Idempotent: the state it produced is a state it leaves alone.
	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil {
		t.Fatalf("second restoreUncommittedOAuthAsides: %v", err)
	}
	if v, _ := f.store.Get("personal"); v != "sk-work" {
		t.Fatalf("the second pass changed the stored key: %q", v)
	}
}

// TestRestoreUncommittedOAuthAsidesSpendsARenameRecordTheConfigStillNamesOld:
// crash point (a) - the record is written and providers.toml was never saved, so
// the config still names the OLD instance. Completion must be harmless: the
// credential stays where the live configuration says it belongs, and the record
// is spent because the rename it recorded never happened.
func TestRestoreUncommittedOAuthAsidesSpendsARenameRecordTheConfigStillNamesOld(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte(codexInstanceToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	store := f.store
	if err := store.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	record := renameAt(t, f, "work", "personal", "1757000000000000000")

	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if v, _ := store.Get("work"); v != "sk-work" {
		t.Fatalf("work = %q, want the stored key left where the config names it", v)
	}
	if v, _ := store.Get("personal"); v != "" {
		t.Fatalf("personal = %q, want nothing migrated while the config still names work", v)
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record survives at %s, want it spent: the rename never landed", record)
	}
}

// TestRestoreUncommittedOAuthAsidesKeepsARenameRecordWhoseKeyCannotMove: a
// stored key that cannot be migrated is reported and keeps the record, so the
// next start tries again rather than leaving the credential under a name the
// config no longer carries.
func TestRestoreUncommittedOAuthAsidesKeepsARenameRecordWhoseKeyCannotMove(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	store := f.store
	if err := store.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	record := renameAt(t, f, "work", "personal", "1757000000000000000")
	credsDir := filepath.Dir(f.credsPath)
	if err := os.Chmod(credsDir, 0o555); err != nil {
		t.Fatalf("Chmod(%s): %v", credsDir, err)
	}
	defer func() {
		if err := os.Chmod(credsDir, 0o700); err != nil {
			t.Fatalf("restore Chmod(%s): %v", credsDir, err)
		}
	}()

	_, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil {
		t.Fatal("restoreUncommittedOAuthAsides = nil, want the key it could not move reported")
	}
	if !strings.Contains(err.Error(), "stored key") {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the stored key named", err)
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record was spent (%v) although its key could not move", statErr)
	}
	if v, _ := f.store.Get("work"); v != "sk-work" {
		t.Fatalf("work = %q, want the key left in place by the failed move", v)
	}
}

// TestRestoreUncommittedOAuthAsidesMovesTheKeyInTheStoreItIsHanded: the hub's
// real credentials store is cmdutil's, not one beside the auth directory, so the
// pass must move the key in the store it is HANDED. A pass that derives a path
// instead reads an empty store, spends the record and leaves the key under the
// old name - the rename silently losing the credential.
func TestRestoreUncommittedOAuthAsidesMovesTheKeyInTheStoreItIsHanded(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	store := f.store
	if err := store.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	seedOAuthRecord(t, f, "work", "work@example.com")
	record := renameAt(t, f, "work", "personal", "1757000000000000000")
	// The store the pass is handed is NOT at filepath.Join(filepath.Dir(stateDir),
	// "credentials.toml") unless the fixture says so: pin that the pass uses it.
	if derived := filepath.Join(filepath.Dir(f.stateDir), "credentials.toml"); derived == f.credsPath {
		t.Fatalf("fixture: the derived path and the real store coincide (%s); the test cannot tell them apart", derived)
	}

	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, store); err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	if v, _ := f.store.Get("personal"); v != "sk-work" {
		t.Fatalf("personal = %q, want the key moved in the store the pass was handed", v)
	}
	if v, _ := f.store.Get("work"); v != "" {
		t.Fatalf("work = %q, want nothing left under the old name", v)
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record survives, want it spent once both halves moved")
	}
}

// TestRestoreUncommittedOAuthAsidesRewritesTheProviderFieldWhenItFinishesARename:
// the bytes of a record a rename left under the old name are moved by recovery,
// and a record identifies the instance it belongs to. Recovery must rewrite that
// field the way the live rename does, or the renamed instance resolves against a
// name its own credential does not claim.
func TestRestoreUncommittedOAuthAsidesRewritesTheProviderFieldWhenItFinishesARename(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	record := renameAt(t, f, "work", "personal", "1757000000000000000")

	if _, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store); err != nil {
		t.Fatalf("restoreUncommittedOAuthAsides: %v", err)
	}
	rec, lerr := authopenai.LoadAuth(f.stateDir, "personal")
	if lerr != nil {
		t.Fatalf("the record was not moved to the new name: %v", lerr)
	}
	if rec.Provider != "personal" {
		t.Fatalf("provider = %q, want %q: the record still identifies the instance it came from", rec.Provider, "personal")
	}
	if _, statErr := os.Lstat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record survives, want it spent once the rename finished")
	}
}

// TestRestoreUncommittedOAuthAsidesLeavesANonRegularRecordWhereItIs: only a
// record is a file the hub reads. A directory (or any non-regular path) at the
// old record name was not written as one, and moving it into a copy's name or a
// canonical name would take it where the copy rules skip directories - so it is
// left where it is and reported.
func TestRestoreUncommittedOAuthAsidesLeavesANonRegularRecordWhereItIs(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.personal]\nbase = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	oldRecord := authopenai.AuthFilePath(f.stateDir, "work")
	if err := os.MkdirAll(oldRecord, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", oldRecord, err)
	}
	const content = "bytes inside the directory\n"
	if err := os.WriteFile(filepath.Join(oldRecord, "inside"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(inside): %v", err)
	}
	record := renameAt(t, f, "work", "personal", "1757000000000000000")

	_, err := restoreUncommittedOAuthAsides(f.stateDir, f.tomlPath, f.store)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("restoreUncommittedOAuthAsides = %v, want the non-regular path reported", err)
	}
	if info, statErr := os.Lstat(oldRecord); statErr != nil || !info.IsDir() {
		t.Fatalf("the path was moved or replaced (info %v, err %v), want it left where it is", info, statErr)
	}
	if got, rerr := os.ReadFile(filepath.Join(oldRecord, "inside")); rerr != nil || string(got) != content {
		t.Fatalf("the directory's contents = %q (%v), want them untouched", got, rerr)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "personal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("something was filed under the new name (Lstat = %v), want the move refused", statErr)
	}
	if _, statErr := os.Lstat(record); statErr != nil {
		t.Fatalf("the record was spent (%v), want it kept for the pass that can move the record", statErr)
	}
}

// ---- the rollback, driven directly ----

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

	err = f.ctl.rollBackFailedRemoval(before, "work", "", false, "", "", true, "the instance is still configured", supplyAny, errors.New("the commit record could not be written"))
	if err == nil {
		t.Fatal("rollBackFailedRemoval = nil, want the failed rollback reported")
	}
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("rollBackFailedRemoval = %v (%T), want a removeAppliedError so the removal is announced", err, err)
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

	err = f.ctl.rollBackFailedRemoval(before, "work", "", false, "", "", true, "the instance is still configured", supplyAny, errors.New("the commit record could not be written"))
	if err == nil {
		t.Fatal("rollBackFailedRemoval = nil, want the failed rollback reported")
	}
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("rollBackFailedRemoval = %v (%T), want a removeAppliedError", err, err)
	}
	if !strings.Contains(err.Error(), "could not be written") {
		t.Fatalf("rollBackFailedRemoval = %v, want the failed rollback write named", err)
	}
	if !strings.Contains(err.Error(), "could not be reloaded") {
		t.Fatalf("rollBackFailedRemoval = %v, want the failed reload named beside the failed write", err)
	}
}

// TestInstances_RollbackWriteFailureKeepsADefaultOnlyImplicitInstanceRemoved:
// when the config carries the removal out (the default pointer cleared) but the
// rollback write fails, the removal stands. A `default`-pointer instance with no
// authored entry exists only through its credential: restoring that credential
// would re-derive the row the caller was told is gone, so the credential stays
// deleted, its copy is reclaimed, and the reload must leave the listing without
// the row.
func TestInstances_RollbackWriteFailureKeepsADefaultOnlyImplicitInstanceRemoved(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("default = \"openai-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	before, _, err := f.ctl.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, authored := before.Providers["openai-codex"]; authored {
		t.Fatalf("fixture: before = %+v, want no authored openai-codex entry", before.Providers)
	}
	if before.Default != "openai-codex" {
		t.Fatalf("fixture: default = %q, want the openai-codex pointer", before.Default)
	}
	// The state the removal leaves once its durable work has landed: the record
	// parked, its record at the commit phase, and providers.toml with the pointer
	// cleared.
	aside, err := f.ctl.setAsideOAuthFile("openai-codex")
	if err != nil {
		t.Fatalf("setAsideOAuthFile: %v", err)
	}
	intentPath := removalAt(t, f, "openai-codex", true, true, "1757000000000000000")
	if err := registry.WriteConfigFile(f.tomlPath, &registry.Layer{}); err != nil {
		t.Fatalf("WriteConfigFile(removal output): %v", err)
	}
	// The rollback write cannot land: the providers.toml directory is read-only,
	// while the file still reads for the reload.
	tomlDir := filepath.Dir(f.tomlPath)
	if err := os.Chmod(tomlDir, 0o555); err != nil {
		t.Fatalf("Chmod(%s): %v", tomlDir, err)
	}
	defer func() {
		if err := os.Chmod(tomlDir, 0o700); err != nil {
			t.Fatalf("restore Chmod(%s): %v", tomlDir, err)
		}
	}()

	err = f.ctl.rollBackFailedRemoval(before, "openai-codex", "", false, intentPath, aside, true, "the instance is still configured", supplyAny, errors.New("the commit record could not be written"))
	if err == nil {
		t.Fatal("rollBackFailedRemoval = nil, want the failed rollback reported")
	}
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("rollBackFailedRemoval = %v (%T), want a removeAppliedError so the removal is announced", err, err)
	}
	if _, ok := f.ctl.reg.Get().Instance("openai-codex"); ok {
		t.Fatalf("the listing still contains openai-codex after a standing removal; registry = %+v", f.ctl.reg.Get().Instances())
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record was restored (Lstat = %v), want the credential kept deleted", statErr)
	}
	if _, statErr := os.Lstat(aside); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the parked copy %s survived (Lstat = %v), want the deleted credential reclaimed", aside, statErr)
	}
	assertNoIntentRecords(t, f)
}

// TestInstances_RollbackWriteFailureKeepsAnAuthoredCuratedProviderRemoved:
// `openai-codex` is a curated implicit provider, so the registry derives its row
// from the credential itself (computeInstances adds every curated id whose
// Implicit flag is set and whose credential resolves). When the removal's config
// write takes an authored [providers.openai-codex] entry away but the rollback
// write fails, restoring the OAuth record the removal deleted would re-derive
// the row the caller was just told is gone. Deciding by what re-derives the
// instance - the registry's provider view - rather than by whether an authored
// entry existed keeps the row out of the refreshed listing: the credential stays
// deleted and its copy is reclaimed.
func TestInstances_RollbackWriteFailureKeepsAnAuthoredCuratedProviderRemoved(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("[providers.openai-codex]\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")
	before, _, err := f.ctl.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, authored := before.Providers["openai-codex"]; !authored {
		t.Fatalf("fixture: before = %+v, want an authored openai-codex entry", before.Providers)
	}
	if p, ok := f.ctl.reg.Get().Provider("openai-codex"); !ok || !registry.BoolValue(p.Implicit) {
		t.Fatalf("fixture: registry provider view for openai-codex = (%+v, %v), want a curated implicit provider", p, ok)
	}
	aside, err := f.ctl.setAsideOAuthFile("openai-codex")
	if err != nil {
		t.Fatalf("setAsideOAuthFile: %v", err)
	}
	intentPath := removalAt(t, f, "openai-codex", true, true, "1757000000000000000")
	if err := registry.WriteConfigFile(f.tomlPath, &registry.Layer{}); err != nil {
		t.Fatalf("WriteConfigFile(removal output): %v", err)
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

	err = f.ctl.rollBackFailedRemoval(before, "openai-codex", "", false, intentPath, aside, true, "the instance is still configured", supplyAny, errors.New("the commit record could not be written"))
	if err == nil {
		t.Fatal("rollBackFailedRemoval = nil, want the failed rollback reported")
	}
	if _, persisted := errors.AsType[removeAppliedError](err); !persisted {
		t.Fatalf("rollBackFailedRemoval = %v (%T), want a removeAppliedError so the removal is announced", err, err)
	}
	if inst, ok := f.ctl.reg.Get().Instance("openai-codex"); ok {
		t.Fatalf("the refreshed listing still contains openai-codex (%+v) after a standing removal; the restored credential re-derived it", inst)
	}
	if _, statErr := os.Lstat(authopenai.AuthFilePath(f.stateDir, "openai-codex")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the record was restored (Lstat = %v), want the credential kept deleted", statErr)
	}
	if _, statErr := os.Lstat(aside); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the parked copy %s survived (Lstat = %v), want the deleted credential reclaimed", aside, statErr)
	}
}

// TestInstances_RolledBackRemovalReportsARecordItCannotSettle: the rollback can
// land and still leave a parked copy - the record path is taken by something the
// rename-back cannot replace. The removal's record must then be returned to its
// STARTED phase, or a credential-only removal's copy would be swept at the next
// start. A record that cannot be settled is REPORTED: the bytes would otherwise
// sit in the shape the sweep deletes rather than the one recovery reads.
func TestInstances_RolledBackRemovalReportsARecordItCannotSettle(t *testing.T) {
	f := newInstancesFixture(t, nil)
	const stamp = "1757000000000000000"
	own := parkedAt(t, f, "work", stamp, "the record the removal parked\n")
	// The removal reached its commit point, so its record is filed under the
	// landed name - and the in-doubt name the rollback has to return it to is
	// taken, so the rollback cannot put the record back in doubt.
	record := intentAt(t, f, "work", oauthLandedMarker, removalIntent("work", false), stamp)
	taken := filepath.Join(oauthDir(f), oauthIntentName("work", 1757000000000000000))
	if err := os.Mkdir(taken, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", taken, err)
	}
	if err := os.WriteFile(filepath.Join(taken, "obstacle"), []byte("in the way"), 0o600); err != nil {
		t.Fatalf("WriteFile(obstacle): %v", err)
	}
	// The carrying copy cannot go back to its own path either.
	path := authopenai.AuthFilePath(f.stateDir, "work")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "obstacle"), []byte("in the way"), 0o600); err != nil {
		t.Fatalf("WriteFile(obstacle): %v", err)
	}
	before, _, err := f.ctl.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	err = f.ctl.rollBackFailedRemoval(before, "work", "", false, record, own, false, "the instance is still configured", supplyAny, errors.New("the commit record could not be written"))
	if err == nil {
		t.Fatal("rollBackFailedRemoval = nil, want the unsettled record reported")
	}
	if !strings.Contains(err.Error(), "in-doubt name") {
		t.Fatalf("rollBackFailedRemoval = %v, want the name it could not return the record to named", err)
	}
	if _, statErr := os.Lstat(own); statErr != nil {
		t.Fatalf("the parked copy is gone (Lstat = %v), want it left where recovery reads it", statErr)
	}
}

// TestInstances_RestoreFailedRemovalReportsAFailedRenameBack: when the rename-back
// cannot land, the copy is left exactly where it is - the shape startup recovery
// reads - and the failure is reported with the copy's path, because those bytes
// are then the instance's only record. A file already at the record path is
// another credential's bytes: the move is a no-replace move and never takes them.
func TestInstances_RestoreFailedRemovalReportsAFailedRenameBack(t *testing.T) {
	f := newInstancesFixture(t, nil)
	const stamp = "1757000000000000000"
	parked := parkedAt(t, f, "work", stamp, "the record the removal parked\n")
	const taken = "another credential's bytes\n"
	path := authopenai.AuthFilePath(f.stateDir, "work")
	if err := os.WriteFile(path, []byte(taken), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}

	_, err := f.ctl.restoreFailedRemoval("work", "", false, parked, errors.New("the reload failed"), "the instance is still configured", supplyAny)
	if err == nil {
		t.Fatal("restoreFailedRemoval = nil, want the collision reported")
	}
	if !strings.Contains(err.Error(), "could not be restored") {
		t.Fatalf("restoreFailedRemoval = %v, want the failed rename-back named", err)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || string(got) != taken {
		t.Fatalf("the record path = %q (%v), want the other credential's bytes left untouched", got, rerr)
	}
	if got, rerr := os.ReadFile(parked); rerr != nil || string(got) != "the record the removal parked\n" {
		t.Fatalf("the parked copy = %q (%v), want it left where recovery reads it", got, rerr)
	}
}
