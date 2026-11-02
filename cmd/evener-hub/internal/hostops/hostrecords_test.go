package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// familyOf builds a family with one distinct record in each of the eight maps,
// so a round trip that crosses two maps would be seen.
func familyOf(tag string) HostRecords {
	record := func(kind string) map[string]json.RawMessage {
		return map[string]json.RawMessage{
			kind + "-key": json.RawMessage(`{"kind":"` + kind + `","tag":"` + tag + `"}`),
		}
	}
	return HostRecords{
		Hosts:          record("hosts"),
		Generations:    record("generations"),
		Receipts:       record("receipts"),
		Tombstones:     record("tombstones"),
		PrunedReceipts: record("prunedReceipts"),
		StagedReceipts: record("stagedReceipts"),
		Remnants:       record("remnants"),
		Attempts:       record("attempts"),
	}
}

func requireFamily(t *testing.T, store *Store, want HostRecords) {
	t.Helper()
	got, importedAt, imported := store.HostRecords()
	if !imported || importedAt.IsZero() {
		t.Fatalf("HostRecords() imported = %v at %v, want an imported family", imported, importedAt)
	}
	wantCanonical, err := want.canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if !got.equal(wantCanonical) {
		t.Fatalf("HostRecords() = %+v, want %+v", got, wantCanonical)
	}
}

func TestHostRecordsRoundTripEveryMapThroughAReload(t *testing.T) {
	store, path := openTestStore(t)
	if _, _, imported := store.HostRecords(); imported {
		t.Fatal("a fresh store reports an imported family")
	}
	family := familyOf("first")
	if err := store.ImportHostRecords(family, HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	requireFamily(t, store, family)

	reopened := reopenFresh(t, path)
	requireFamily(t, reopened, family)
	_, importedAt, _ := store.HostRecords()
	if _, reloadedAt, _ := reopened.HostRecords(); !reloadedAt.Equal(importedAt) {
		t.Fatalf("the import instant moved across the reload: %v then %v", importedAt, reloadedAt)
	}
}

// TestHostRecordsSurviveEveryOtherStoreWrite pins that the family rides every
// write of the store, not only its own: operation records, boundary mirrors and
// token passes rewrite the whole file, and none may drop the records.
func TestHostRecordsSurviveEveryOtherStoreWrite(t *testing.T) {
	store, path := openTestStore(t)
	family := familyOf("kept")
	if err := store.ImportHostRecords(family, HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	record := createTestRecord(t, store, "h1")
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := store.MirrorBoundaries(map[string]Boundary{"h1": {Generation: 1, IncarnationID: "inc-h1", PresenceEpoch: 1}}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	mustMint(t, store, mintDefaults("h1", store.now()))
	requireFamily(t, store, family)
	requireFamily(t, reopenFresh(t, path), family)
}

// TestHostRecordsReadsAreCopies pins that a caller cannot reach the store's own
// records through what it read.
func TestHostRecordsReadsAreCopies(t *testing.T) {
	store, _ := openTestStore(t)
	family := familyOf("kept")
	if err := store.ImportHostRecords(family, HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	family.Hosts["hosts-key"][2] = 'X'
	read, _, _ := store.HostRecords()
	read.Hosts["hosts-key"][2] = 'Y'
	delete(read.Receipts, "receipts-key")
	requireFamily(t, store, familyOf("kept"))
}

func TestHostRecordsCommitReplacesTheFamilyAndKeepsTheImportInstant(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.ImportHostRecords(familyOf("first"), HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	_, importedAt, _ := store.HostRecords()

	next := familyOf("second")
	next.Receipts["receipts-extra"] = json.RawMessage(`{"kind":"receipts","tag":"extra"}`)
	delete(next.Attempts, "attempts-key")
	if err := store.CommitHostRecords(next, HostMirror{}); err != nil {
		t.Fatalf("CommitHostRecords: %v", err)
	}
	requireFamily(t, store, next)
	requireFamily(t, reopenFresh(t, path), next)
	if _, committedAt, _ := store.HostRecords(); !committedAt.Equal(importedAt) {
		t.Fatalf("a commit moved the import instant: %v then %v", importedAt, committedAt)
	}
}

func TestHostRecordsCommitLandsTheFamilyAndTheMirrorTogether(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.ImportHostRecords(familyOf("first"), HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	boundary := Boundary{Generation: 4, IncarnationID: "inc-h1", PresenceEpoch: 2}
	next := familyOf("second")

	store.faults.beforeRename = func() error { return errors.New("injected crash before the rename") }
	if err := store.CommitHostRecords(next, HostMirror{Boundaries: map[string]Boundary{"h1": boundary}}); err == nil {
		t.Fatal("CommitHostRecords succeeded through an injected pre-rename fault")
	}
	store.faults.beforeRename = nil
	for label, probe := range map[string]*Store{"live handle": store, "reloaded": reopenFresh(t, path)} {
		got, _, _ := probe.HostRecords()
		if got.equal(mustCanonical(t, next)) {
			t.Fatalf("%s: the family landed although the write failed", label)
		}
		if _, ok := probe.Boundary("h1"); ok {
			t.Fatalf("%s: the mirror landed although the write failed", label)
		}
	}

	if err := store.CommitHostRecords(next, HostMirror{Boundaries: map[string]Boundary{"h1": boundary}}); err != nil {
		t.Fatalf("CommitHostRecords: %v", err)
	}
	reopened := reopenFresh(t, path)
	requireFamily(t, reopened, next)
	if got, ok := reopened.Boundary("h1"); !ok || got != boundary {
		t.Fatalf("reloaded boundary = %+v, %v; want %+v", got, ok, boundary)
	}
}

func mustCanonical(t *testing.T, family HostRecords) HostRecords {
	t.Helper()
	canonical, err := family.canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	return canonical
}

func TestHostRecordsUnchangedCommitWritesNothing(t *testing.T) {
	store, _ := openTestStore(t)
	family := familyOf("first")
	if err := store.ImportHostRecords(family, HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	// A value spelled differently but equal after canonicalization is the same
	// record, so a rewrite of the same family must not reach the file.
	family.Hosts["hosts-key"] = json.RawMessage(`{ "kind" : "hosts", "tag" : "first" }`)
	store.faults.beforeRename = func() error { return errors.New("a write was attempted") }
	if err := store.CommitHostRecords(family, HostMirror{}); err != nil {
		t.Fatalf("an unchanged commit wrote: %v", err)
	}
}

func TestHostRecordsCommitRequiresTheImportAndImportHappensOnce(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.CommitHostRecords(familyOf("first"), HostMirror{}); !errors.Is(err, ErrHostRecordsNotImported) {
		t.Fatalf("CommitHostRecords before the import = %v, want ErrHostRecordsNotImported", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused commit created the store file")
	}
	if err := store.ImportHostRecords(familyOf("first"), HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	if err := store.ImportHostRecords(familyOf("second"), HostMirror{}); !errors.Is(err, ErrHostRecordsImported) {
		t.Fatalf("a second ImportHostRecords = %v, want ErrHostRecordsImported", err)
	}
	requireFamily(t, store, familyOf("first"))
}

func TestHostRecordsAStoreWithoutTheFamilyLoadsAsNotImported(t *testing.T) {
	store, path := openTestStore(t)
	createTestRecord(t, store, "h1")
	reopened := reopenFresh(t, path)
	if _, _, imported := reopened.HostRecords(); imported {
		t.Fatal("a store written before the family existed reports an import")
	}
	if strings.Contains(string(mustReadFile(t, path)), "hostRecords") {
		t.Fatal("a store that imported nothing wrote the family key")
	}
}

func TestHostRecordsBoundsAreEnforcedBeforeAnythingIsWritten(t *testing.T) {
	object := func(size int) json.RawMessage {
		return json.RawMessage(`{"pad":"` + strings.Repeat("x", size) + `"}`)
	}
	cases := map[string]func(*HostRecords){
		"an empty key": func(r *HostRecords) { r.Hosts[""] = object(1) },
		"an over-long host name": func(r *HostRecords) {
			r.Hosts[strings.Repeat("h", MaxHostNameBytes+1)] = object(1)
		},
		"an over-long scoped key": func(r *HostRecords) {
			r.Receipts[strings.Repeat("k", MaxHostRecordKeyBytes+1)] = object(1)
		},
		"a key that is not UTF-8": func(r *HostRecords) { r.Remnants["\xff\xfe"] = object(1) },
		"a record over its bound": func(r *HostRecords) {
			r.Tombstones["big"] = object(MaxHostRecordBytes)
		},
		"a value that is not JSON":      func(r *HostRecords) { r.Attempts["a"] = json.RawMessage(`{"open":`) },
		"a value that is not an object": func(r *HostRecords) { r.Attempts["a"] = json.RawMessage(`["list"]`) },
		"a family over its total bound": func(r *HostRecords) {
			for i := range MaxHostRecordsBytes/(MaxHostRecordBytes/2) + 1 {
				r.Receipts[strings.Repeat("r", 8)+string(rune('a'+i%26))+strings.Repeat("s", i)] = object(MaxHostRecordBytes / 2)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store, path := openTestStore(t)
			family := familyOf("ok")
			mutate(&family)
			if err := store.ImportHostRecords(family, HostMirror{}); !errors.Is(err, ErrInvalidHostRecords) {
				t.Fatalf("ImportHostRecords = %v, want ErrInvalidHostRecords", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a refused family reached the store file")
			}
			if _, _, imported := store.HostRecords(); imported {
				t.Fatal("a refused family was imported")
			}
		})
	}
}

// TestHostRecordsBoundsApplyToTheSizeAWriteEmits pins that a record is bounded
// by its written form: the encoder spells each <, > and & as six bytes, so a
// record near the bound in the file's spelling must be refused on write and
// treated as corrupt on load, never accepted and then written past the bound.
func TestHostRecordsBoundsApplyToTheSizeAWriteEmits(t *testing.T) {
	t.Run("keys count as they are spelled in the file", func(t *testing.T) {
		store, path := openTestStore(t)
		family := familyOf("ok")
		// Each key is 2000 bytes of <, six times that once encoded: the family is
		// well under its bound in decoded bytes and well over it as written.
		for i := range MaxHostRecordsBytes / (6 * 2000) * 2 {
			family.Receipts[fmt.Sprintf("%s%05d", strings.Repeat("<", 2000), i)] = json.RawMessage(`{"k":1}`)
		}
		if err := store.ImportHostRecords(family, HostMirror{}); !errors.Is(err, ErrInvalidHostRecords) {
			t.Fatalf("ImportHostRecords = %v, want ErrInvalidHostRecords", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a refused family reached the store file")
		}
	})
	escapes := func(n int) json.RawMessage {
		return json.RawMessage(`{"pad":"` + strings.Repeat("<", n) + `"}`)
	}
	t.Run("a write that would expand past the bound is refused", func(t *testing.T) {
		store, path := openTestStore(t)
		family := familyOf("ok")
		family.Tombstones["big"] = escapes(MaxHostRecordBytes / 2)
		if err := store.ImportHostRecords(family, HostMirror{}); !errors.Is(err, ErrInvalidHostRecords) {
			t.Fatalf("ImportHostRecords = %v, want ErrInvalidHostRecords", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a refused family reached the store file")
		}
	})
	t.Run("a file whose record would expand past the bound is corrupt", func(t *testing.T) {
		store, path := openTestStore(t)
		if err := store.ImportHostRecords(familyOf("ok"), HostMirror{}); err != nil {
			t.Fatalf("ImportHostRecords: %v", err)
		}
		raw := string(mustReadFile(t, path))
		big := string(escapes(MaxHostRecordBytes / 2))
		grown := strings.Replace(raw, `{"kind":"tombstones","tag":"ok"}`, big, 1)
		if grown == raw {
			t.Fatalf("the fixture did not carry the expected tombstone:\n%s", raw)
		}
		writeRawStore(t, path, 0o600, grown)
		if _, err := loadFS(afero.NewOsFs(), path); !errors.Is(err, ErrStoreCorrupt) {
			t.Fatalf("loadFS = %v, want ErrStoreCorrupt", err)
		}
	})
	t.Run("a file that is within the bound once written loads in its written form", func(t *testing.T) {
		store, path := openTestStore(t)
		if err := store.ImportHostRecords(familyOf("ok"), HostMirror{}); err != nil {
			t.Fatalf("ImportHostRecords: %v", err)
		}
		raw := string(mustReadFile(t, path))
		spelled := strings.Replace(raw, `{"kind":"tombstones","tag":"ok"}`, `{"kind":"<tombstones>","tag":"ok"}`, 1)
		writeRawStore(t, path, 0o600, spelled)
		loaded, err := loadFS(afero.NewOsFs(), path)
		if err != nil {
			t.Fatalf("loadFS: %v", err)
		}
		if got := string(loaded.HostRecords.Tombstones["tombstones-key"]); strings.Contains(got, "<") {
			t.Fatalf("a loaded record keeps the file's spelling %s, want the written form", got)
		}
	})
}

// TestHostRecordsAFamilyMissingAMapIsCorrupt pins the file-level rule: a file
// that omits one of the eight maps is not one this store wrote, and reading it
// as an empty map would let the next write drop the records the map held.
func TestHostRecordsAFamilyMissingAMapIsCorrupt(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.ImportHostRecords(familyOf("first"), HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	raw := string(mustReadFile(t, path))
	stripped := strings.Replace(raw, `"attempts":{"attempts-key":{"kind":"attempts","tag":"first"}}`, `"attempts":null`, 1)
	if stripped == raw {
		t.Fatalf("the fixture file does not carry the expected attempts map:\n%s", raw)
	}
	writeRawStore(t, path, 0o600, stripped)
	if _, err := loadFS(afero.NewOsFs(), path); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("loadFS = %v, want ErrStoreCorrupt", err)
	}
}

// TestHostRecordsSurviveAQuarantine pins that a corrupt operation store takes
// its family through custody: the replacement opens with every record and the
// import marker, the corrupt bytes stay aside verbatim, and the records keep
// committing afterwards.
func TestHostRecordsSurviveAQuarantine(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	family := familyOf("kept")
	if err := store.ImportHostRecords(family, HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	_, importedAt, _ := store.HostRecords()

	raw := string(mustReadFile(t, path))
	// The corruption is the store-level sequence value, below the stamp the
	// terminal record carries: every record and the family stay valid.
	corrupted := strings.Replace(raw, `"sequence":1`, `"sequence":0`, 1)
	if corrupted == raw {
		t.Fatalf("the fixture did not carry the expected sequence value:\n%s", raw)
	}
	writeRawStore(t, path, 0o600, corrupted)
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a corrupt store carrying the family: %v, want the custody-first quarantine", err)
	}
	wantQuarantined(t, reopened, path, corrupted)
	requireFamily(t, reopened, family)
	if _, replacedAt, _ := reopened.HostRecords(); !replacedAt.Equal(importedAt) {
		t.Fatalf("the quarantine moved the import instant: %v then %v", importedAt, replacedAt)
	}
	requireFamily(t, reopenFresh(t, path), family)

	next := familyOf("after")
	if err := reopened.CommitHostRecords(next, HostMirror{}); err != nil {
		t.Fatalf("CommitHostRecords after the quarantine: %v", err)
	}
	requireFamily(t, reopenFresh(t, path), next)
}

// TestHostRecordsQuarantineSetsAsideAnUnreadableFamily pins that a family
// region the store cannot read leaves the custody unproven: the file is set
// aside verbatim and the replacement serves empty, with a signal saying so.
func TestHostRecordsQuarantineSetsAsideAnUnreadableFamily(t *testing.T) {
	cases := map[string]func(raw string) string{
		"a record that is not an object": func(raw string) string {
			return strings.Replace(raw, `{"kind":"receipts","tag":"first"}`, `["not","an","object"]`, 1)
		},
		"a zero import instant": func(raw string) string {
			return regexp.MustCompile(`"importedAt":"[^"]*"`).ReplaceAllString(raw, `"importedAt":"0001-01-01T00:00:00Z"`)
		},
		"a family that is null": func(raw string) string {
			return regexp.MustCompile(`"hostRecords":\{.*\}\}`).ReplaceAllString(raw, `"hostRecords":null}`)
		},
		"a field spelled in two cases": func(raw string) string {
			return strings.Replace(raw, `"hosts":{`, `"Hosts":{"hosts-key":{"kind":"stale"}},"hosts":{`, 1)
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			store, path := openTestStore(t)
			if err := store.ImportHostRecords(familyOf("first"), HostMirror{}); err != nil {
				t.Fatalf("ImportHostRecords: %v", err)
			}
			raw := string(mustReadFile(t, path))
			broken := corrupt(raw)
			if broken == raw {
				t.Fatalf("the fixture did not carry the expected text:\n%s", raw)
			}
			writeRawStore(t, path, 0o600, broken)
			if err := forgetStore(path); err != nil {
				t.Fatalf("forgetStore: %v", err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatalf("Open = %v, want the unproven set-aside", err)
			}
			_, aside := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
			if !bytes.Equal(aside, []byte(broken)) {
				t.Fatal("the set-aside file does not hold the unreadable family verbatim")
			}
			if signal := reopened.Quarantine(); signal == nil || !signal.Unproven {
				t.Fatalf("Quarantine() = %+v, want an unproven signal", signal)
			}
			if family, _, imported := reopened.HostRecords(); imported || len(family.Hosts) != 0 {
				t.Fatalf("the replacement carries host records: %+v", family)
			}
		})
	}
}
