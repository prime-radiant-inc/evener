package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoreRelocationCompletedCustody(t *testing.T) {
	t.Parallel()
	testStoreRelocation(t, true)
}

func TestStoreRelocationSettledJournal(t *testing.T) {
	t.Parallel()
	testStoreRelocation(t, false)
}

// This is an offline fixture recipe, not a migration API. All stores and files
// are disposable, and no source handle writes after the settled snapshot.
func testStoreRelocation(t *testing.T, withCustody bool) {
	t.Helper()
	sourcePath := StorePath(t.TempDir())
	store, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if withCustody {
		relocationComplete(t, store, "custodied", "relocation-original")
		// The existing quarantine tests use this parseable inconsistency:
		// records remain complete, but the sequence trails a terminal stamp.
		top := decodeCustodyBody(t, mustReadFile(t, sourcePath))
		top["sequence"] = json.RawMessage(`0`)
		raw, err := json.Marshal(top)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		store = reopenFresh(t, sourcePath)
		if signal := store.Quarantine(); signal == nil || signal.QuarantineEpoch != 1 {
			t.Fatalf("fixture did not complete quarantine: %+v", signal)
		}
		imports := store.Records()
		if len(imports) != 1 || imports[0].State != StateOrphanUnverified || imports[0].Host != "custodied" {
			t.Fatalf("missing custody ownership import: %+v", imports)
		}
	}

	removed := relocationComplete(t, store, "retired", "relocation-retired")
	current := relocationComplete(t, store, "current", "relocation-current")
	if err := store.MirrorBoundaries(map[string]Boundary{
		"retired": {Generation: 7, IncarnationID: "inc-retired", PresenceEpoch: 9},
		"current": {Generation: 7, IncarnationID: "inc-current", PresenceEpoch: 11},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if marked, dropped, err := store.ApplyHostRemovedPass(map[string]HostRemovedMark{
		"retired": {Generation: 7, IncarnationID: "inc-retired"},
	}); err != nil || marked != 1 || dropped != 0 {
		t.Fatalf("removal pass = %d, %d, %v", marked, dropped, err)
	}
	if _, err := os.Stat(quarantineIntentPath(sourcePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine is not settled: %v", err)
	}
	// Exercise preservation of a stricter-than-default mode as well as 0600.
	if err := os.Chmod(sourcePath, 0o400); err != nil {
		t.Fatal(err)
	}
	store = reopenFresh(t, sourcePath)
	original := storeSnapshotForTest(store)
	originalEpoch := store.CursorEpoch()
	originalSignal := store.Quarantine()
	if len(original.Compensations) != 0 || original.Sequence != 2 || original.AllocatorHighWaterMark == 0 {
		t.Fatalf("fixture is not settled meaningful authority: %+v", original)
	}
	sourceFiles := relocationFiles(t, filepath.Dir(sourcePath))

	destinationPath := StorePath(t.TempDir())
	relocationCopy(t, sourceFiles, filepath.Dir(destinationPath))
	if got := relocationFiles(t, filepath.Dir(destinationPath)); !reflect.DeepEqual(got, sourceFiles) {
		t.Fatal("initial copy changed bytes or modes")
	}
	if withCustody {
		refused, err := Open(destinationPath)
		if refused != nil || !errors.Is(err, ErrStoreCorrupt) || !errors.Is(err, ErrQuarantineIncomplete) {
			t.Fatalf("naive copy = %v, %v, want typed custody refusal", refused, err)
		}
		if got := relocationFiles(t, filepath.Dir(destinationPath)); !reflect.DeepEqual(got, sourceFiles) {
			t.Fatal("refused open mutated destination")
		}
		if got := relocationFiles(t, filepath.Dir(sourcePath)); !reflect.DeepEqual(got, sourceFiles) {
			t.Fatal("refused open mutated source")
		}
		adjusted := 0
		for name, file := range sourceFiles {
			if !strings.HasPrefix(name, "operations.json.custody-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			raw, err := relocationCustodyPath(file.body, sourcePath, destinationPath)
			if err != nil {
				t.Fatal(err)
			}
			// Independent oracle: all non-path fields, including ownership,
			// allocator and epoch, must retain their exact encoded payloads.
			before := decodeCustodyBody(t, file.body)
			after := decodeCustodyBody(t, raw)
			var path string
			if err := json.Unmarshal(after["quarantinedFile"], &path); err != nil || path != destinationPath {
				t.Fatalf("adjusted path = %q, %v", path, err)
			}
			delete(before, "quarantinedFile")
			delete(after, "quarantinedFile")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("adjustment changed another custody payload")
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(destinationPath), name), raw, file.mode); err != nil {
				t.Fatal(err)
			}
			adjusted++
		}
		if adjusted != 1 {
			t.Fatalf("adjusted %d custody files, want one completed fixture", adjusted)
		}
	}
	adjustedFiles := relocationFiles(t, filepath.Dir(destinationPath))
	for name, before := range sourceFiles {
		after := adjustedFiles[name]
		if before.mode != after.mode || (!strings.Contains(name, ".custody-") && !bytes.Equal(before.body, after.body)) {
			t.Fatalf("adjustment changed bytes or mode of %s", name)
		}
	}
	moved, err := Open(destinationPath)
	if err != nil {
		t.Fatalf("corrected copy cannot open: %v", err)
	}
	if got := storeSnapshotForTest(moved); !reflect.DeepEqual(got, original) {
		t.Fatalf("relocation changed authority:\n got %+v\nwant %+v", got, original)
	}
	if got := moved.CursorEpoch(); got != originalEpoch {
		t.Fatalf("cursor epoch = %+v, want %+v", got, originalEpoch)
	}
	if originalSignal == nil {
		if moved.Quarantine() != nil {
			t.Fatal("ordinary copy acquired a quarantine signal")
		}
	} else if got := moved.Quarantine(); got == nil || got.QuarantineEpoch != originalSignal.QuarantineEpoch ||
		got.QuarantinedFile != destinationPath || filepath.Base(got.CustodyFile) != filepath.Base(originalSignal.CustodyFile) {
		t.Fatalf("relocated quarantine signal = %+v", got)
	}
	if got := relocationFiles(t, filepath.Dir(destinationPath)); !reflect.DeepEqual(got, adjustedFiles) {
		t.Fatal("successful open rewrote the settled copy")
	}

	// Terminal outcomes still fence transitions, removal still fences dedup,
	// and custody imports still refuse a successful outcome.
	if _, err := moved.Transition(current.ID, StateRunning, nil); !errors.Is(err, ErrRecordTerminal) {
		t.Fatalf("terminal transition fence lost: %v", err)
	}
	if _, _, err := moved.LookupOperation(OperationDedupQuery{Host: removed.Host, Kind: removed.Kind,
		ClientOperationID: removed.ClientOperationID, Current: OperationPair{Generation: removed.Generation, IncarnationID: removed.IncarnationID}}); !errors.Is(err, ErrConflictingOperationID) {
		t.Fatalf("removed-host dedup fence lost: %v", err)
	}
	if withCustody {
		if _, err := moved.Transition(original.Records[0].ID, StateComplete, nil); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("custody transition fence lost: %v", err)
		}
	}
	if got := relocationFiles(t, filepath.Dir(destinationPath)); !reflect.DeepEqual(got, adjustedFiles) {
		t.Fatal("fenced operations mutated the copy")
	}

	next := relocationComplete(t, moved, "current", "relocation-next")
	id, err := parseAllocatorID(next.ID)
	if err != nil || id != original.AllocatorHighWaterMark+1 || moved.Sequence() != original.Sequence+1 {
		t.Fatalf("next allocation/sequence = %s, %d, %v", next.ID, moved.Sequence(), err)
	}
	fresh := reopenFresh(t, destinationPath)
	if got, ok := fresh.Record(next.ID); !ok || !reflect.DeepEqual(got, next) {
		t.Fatalf("next operation was not durable: %+v, %t", got, ok)
	}
	for _, record := range original.Records {
		if got, ok := fresh.Record(record.ID); !ok || !reflect.DeepEqual(got, record) {
			t.Fatalf("next operation reused or changed history %s: %+v", record.ID, got)
		}
	}
	if got := relocationFiles(t, filepath.Dir(sourcePath)); !reflect.DeepEqual(got, sourceFiles) {
		t.Fatal("relocation or destination writes changed source bytes/modes")
	}
	if info, err := os.Stat(destinationPath); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("destination write widened restrictive mode: %v, %v", info, err)
	}
}

func relocationComplete(t *testing.T, store *Store, host, operation string) Record {
	t.Helper()
	record, err := store.Create(NewRecord{ClientOperationID: operation, Host: host, Kind: KindRestart, Generation: 7, IncarnationID: "inc-" + host})
	if err != nil {
		t.Fatal(err)
	}
	// Unknown nested payload keys deliberately contain the old StorePath. They
	// are operation evidence, never candidates for path substitution.
	epoch, err := json.Marshal(map[string]any{"bootId": "fixture-boot", "opSeq": 17,
		"opaque": map[string]string{"quarantinedFile": store.Path()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(record.ID, StateRunning, func(r *Record) { r.FencingEpoch = epoch }); err != nil {
		t.Fatal(err)
	}
	record, err = store.TransitionToState(record.ID, StateComplete, &Result{OK: true, Message: "fixture-result"}, "fixture-progress")
	if err != nil {
		t.Fatal(err)
	}
	return record
}

type relocationFile struct {
	body []byte
	mode os.FileMode
}

// The hostops fixture is flat; include its directory itself to catch mode drift.
func relocationFiles(t *testing.T, dir string) map[string]relocationFile {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]relocationFile{".": {mode: info.Mode().Perm()}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("non-private fixture file %s: %v, %v", entry.Name(), info, err)
		}
		files[entry.Name()] = relocationFile{body: mustReadFile(t, filepath.Join(dir, entry.Name())), mode: info.Mode().Perm()}
	}
	return files
}

func relocationCopy(t *testing.T, files map[string]relocationFile, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, files["."].mode); err != nil {
		t.Fatal(err)
	}
	for name, file := range files {
		if name == "." {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, file.mode)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.Write(file.body)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("copy %s: write %v, close %v", name, writeErr, closeErr)
		}
	}
}

// relocationCustodyPath changes only the exact top-level string token. Keeping
// the surrounding bytes avoids re-encoding unknown/nested evidence. This is
// test-only and assumes a completed custody document validated at its source.
func relocationCustodyPath(raw []byte, original, destination string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, errors.New("invalid fixture object")
	}
	start, end := -1, -1
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if key == "quarantinedFile" {
			var path string
			if err := json.Unmarshal(value, &path); err != nil || path != original || start != -1 {
				return nil, errors.New("fixture custody does not name the exact original store")
			}
			end = int(decoder.InputOffset())
			start = end - len(value)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || start == -1 {
		return nil, errors.New("incomplete fixture custody or trailing data")
	}
	path, err := json.Marshal(destination)
	if err != nil {
		return nil, err
	}
	result := append([]byte(nil), raw[:start]...)
	result = append(result, path...)
	return append(result, raw[end:]...), nil
}

func TestStoreRelocationPathAdjustmentPreservesOpaqueBytes(t *testing.T) {
	t.Parallel()
	// Unknown top-level custody keys are not accepted by Open. This separate
	// helper check proves they cannot be dropped or globally rewritten either.
	raw := []byte("{\n  \"unknown\": {\"quarantinedFile\":\"/old/operations.json\", \"number\":1e+02},\n  \"quarantinedFile\" : \"/old/operations.json\"\n}\n")
	want := []byte("{\n  \"unknown\": {\"quarantinedFile\":\"/old/operations.json\", \"number\":1e+02},\n  \"quarantinedFile\" : \"/new/operations.json\"\n}\n")
	before := bytes.Clone(raw)
	got, err := relocationCustodyPath(raw, "/old/operations.json", "/new/operations.json")
	if err != nil || !bytes.Equal(got, want) || !bytes.Equal(raw, before) {
		t.Fatalf("path adjustment changed opaque bytes or input: %s, %v", got, err)
	}
	if _, err := relocationCustodyPath(raw, "/wrong/operations.json", "/new/operations.json"); err == nil {
		t.Fatal("adjustment accepted an unexpected original path")
	}
}
