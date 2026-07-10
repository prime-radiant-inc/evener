package jobstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/fuzz/fault"
)

// FuzzTask8OutputRecovery drives the OutputStore retained-tail state machine,
// metadata reopen path, forensic ReadEvents semantics, and Store append
// rollback through bounded deterministic programs.
//
// The primary output/store lanes are pure afero.MemMapFs fixtures. The only OS
// files are short-lived paths below t.TempDir used to exercise the public
// ReadEvents and output helper wrappers; no ambient path, process, provider, or
// network is consulted. The semantic oracles assert retained-tail bytes and
// lifetime offsets after each append/reopen, public wrapper parity, tolerance of
// partial/corrupt trailing event lines, and preservation of a committed event
// after injected append failures and recovery.
func FuzzTask8OutputRecovery(f *testing.F) {
	// Forces several retained-tail prunes and a complete trailing event without
	// its newline, which the Store must terminate on reopen.
	f.Add([]byte{0, 1, 0, 2}, []byte("alpha\nbeta\ngamma\n"))
	// Exercises the corrupt trailing-event forensic path and different chunking.
	f.Add([]byte{77, 2, 2, 31}, []byte("\x00longer payload\nwith lines\n"))

	f.Fuzz(func(t *testing.T, program, payload []byte) {
		if len(program) > 128 || len(payload) > 512 {
			return
		}
		t8RunOutputRecovery(t, program, payload)
	})
}

func t8RunOutputRecovery(t *testing.T, program, payload []byte) {
	t.Helper()
	capBytes := int64(16 + int(t8OutputProgramByte(program, 0))%80)
	stream := make([]byte, 0, len(payload)+32)
	stream = append(stream, []byte("USER_EVIDENCE\n")...)
	stream = append(stream, payload...)
	if len(stream) == 0 || stream[len(stream)-1] != '\n' {
		stream = append(stream, '\n')
	}
	stream = append(stream, []byte("__T8_END__\n")...)

	fs := afero.NewMemMapFs()
	const outputPath = "/task8-output.log"
	store, err := openOutputFsNoSync(fs, outputPath, capBytes)
	if err != nil {
		t.Fatalf("open memory output: %v", err)
	}
	var lifetime []byte
	for offset, step := 0, 0; offset < len(stream); step++ {
		chunkLen := 1 + int(t8OutputProgramByte(program, step+1))%31
		if chunkLen > len(stream)-offset {
			chunkLen = len(stream) - offset
		}
		chunk := stream[offset : offset+chunkLen]
		n, err := store.Append(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("append step %d = %d/%v, want %d/nil", step, n, err, len(chunk))
		}
		lifetime = append(lifetime, chunk...)
		t8AssertOutputState(t, store, lifetime, capBytes, int(t8OutputProgramByte(program, step+4)))
		offset += chunkLen

		// Reopen frequently enough to exercise metadata validation between
		// normal appends, not just after the final retained tail exists.
		if t8OutputProgramByte(program, step+2)%2 == 0 {
			if err := store.Close(); err != nil {
				t.Fatalf("close output step %d: %v", step, err)
			}
			store, err = openOutputFsNoSync(fs, outputPath, capBytes)
			if err != nil {
				t.Fatalf("reopen output step %d: %v", step, err)
			}
			t8AssertOutputState(t, store, lifetime, capBytes, int(t8OutputProgramByte(program, step+5)))
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close final output: %v", err)
	}
	store, err = openOutputFsNoSync(fs, outputPath, capBytes)
	if err != nil {
		t.Fatalf("final reopen output: %v", err)
	}
	defer func() { _ = store.Close() }()
	t8AssertOutputState(t, store, lifetime, capBytes, int(t8OutputProgramByte(program, 9)))

	matches, err := store.Grep(regexp.MustCompile(`^__T8_END__$`), len(lifetime)+16)
	if err != nil {
		t.Fatalf("grep retained tail: %v", err)
	}
	terminalOffset := int64(len(lifetime) - len("__T8_END__\n"))
	foundTerminal := false
	for _, match := range matches {
		if match.Line == "__T8_END__" && match.ByteOffset == terminalOffset {
			foundTerminal = true
			break
		}
	}
	if !foundTerminal {
		t.Fatalf("retained grep lost terminal lifetime offset %d: %+v", terminalOffset, matches)
	}

	t8AssertPublicOutputWrappers(t)
	t8AssertReadEventsRecovery(t, int(t8OutputProgramByte(program, 1)%3))
	t8AssertStoreFaultRollback(t)
}

func t8AssertOutputState(t *testing.T, store *OutputStore, lifetime []byte, capBytes int64, rawLimit int) {
	t.Helper()
	retained, retainedStart := t8RetainedTail(lifetime, capBytes)
	if got := store.Len(); got != int64(len(lifetime)) {
		t.Fatalf("lifetime Len = %d, want %d", got, len(lifetime))
	}
	if got := store.RetainedStart(); got != retainedStart {
		t.Fatalf("RetainedStart = %d, want %d", got, retainedStart)
	}
	limit := rawLimit % (len(retained) + 3)

	tail, total, tailTruncated, err := store.Tail(limit)
	if err != nil {
		t.Fatalf("Tail(%d): %v", limit, err)
	}
	wantTail := retained
	if len(wantTail) > limit {
		wantTail = wantTail[len(wantTail)-limit:]
	}
	wantTruncated := retainedStart > 0 || len(retained) > limit
	if total != int64(len(lifetime)) || tailTruncated != wantTruncated || !bytes.Equal(tail, wantTail) {
		t.Fatalf("Tail(%d) = %q/%d/%v, want %q/%d/%v", limit, tail, total, tailTruncated, wantTail, len(lifetime), wantTruncated)
	}

	head, headTotal, headTruncated, err := store.Head(limit)
	if err != nil {
		t.Fatalf("Head(%d): %v", limit, err)
	}
	wantHead := retained
	if len(wantHead) > limit {
		wantHead = wantHead[:limit]
	}
	if headTotal != int64(len(lifetime)) || headTruncated != wantTruncated || !bytes.Equal(head, wantHead) {
		t.Fatalf("Head(%d) = %q/%d/%v, want %q/%d/%v", limit, head, headTotal, headTruncated, wantHead, len(lifetime), wantTruncated)
	}
}

func t8RetainedTail(lifetime []byte, capBytes int64) ([]byte, int64) {
	if capBytes > 0 && int64(len(lifetime)) > capBytes {
		start := int64(len(lifetime)) - capBytes
		return append([]byte(nil), lifetime[start:]...), start
	}
	return append([]byte(nil), lifetime...), 0
}

func t8AssertPublicOutputWrappers(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	noSyncPath := filepath.Join(dir, "no-sync.log")
	noSync, err := OpenOutputNoSync(noSyncPath, 0)
	if err != nil {
		t.Fatalf("OpenOutputNoSync: %v", err)
	}
	if _, err := noSync.Append([]byte("public output\n")); err != nil {
		t.Fatalf("public no-sync append: %v", err)
	}
	if err := noSync.Close(); err != nil {
		t.Fatalf("public no-sync close: %v", err)
	}
	total, start, err := OutputFileStats(noSyncPath)
	if err != nil || total != int64(len("public output\n")) || start != 0 {
		t.Fatalf("OutputFileStats = %d/%d/%v", total, start, err)
	}
	re := regexp.MustCompile(`^public output$`)
	for _, grep := range []func(string, *regexp.Regexp, int, int, int) ([]Match, error){
		GrepFileLimit,
		func(path string, re *regexp.Regexp, limitBytes, maxMatches, maxLineBytes int) ([]Match, error) {
			return GrepFileLimitAt(path, re, limitBytes, maxMatches, maxLineBytes, 0)
		},
	} {
		matches, err := grep(noSyncPath, re, 1024, 0, 1024)
		if err != nil || len(matches) != 1 || matches[0].ByteOffset != 0 {
			t.Fatalf("public grep = %+v/%v", matches, err)
		}
	}
	if err := RemoveOutputArtifacts(noSyncPath); err != nil {
		t.Fatalf("RemoveOutputArtifacts: %v", err)
	}
	if _, err := os.Stat(noSyncPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output still exists after cleanup: %v", err)
	}

	// The sync wrapper is a separate public entry point. It gets a tiny bounded
	// fixture so the fuzzer covers its production default without relying on a
	// host path outside this test's private directory.
	syncPath := filepath.Join(dir, "sync.log")
	syncStore, err := OpenOutput(syncPath, 0)
	if err != nil {
		t.Fatalf("OpenOutput: %v", err)
	}
	if _, err := syncStore.Append([]byte("sync output\n")); err != nil {
		t.Fatalf("public sync append: %v", err)
	}
	if err := syncStore.Close(); err != nil {
		t.Fatalf("public sync close: %v", err)
	}
}

func t8AssertReadEventsRecovery(t *testing.T, mode int) {
	t.Helper()
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	first := Event{
		Kind:             EventJobStarted,
		TS:               startedAt,
		JobID:            "job_task8",
		Type:             JobShell,
		Command:          "echo task8",
		OwnerSessionID:   "session_task8",
		VisibleToSession: "session_task8",
		StartedAt:        &startedAt,
	}
	second := Event{Kind: EventJobFinished, TS: startedAt.Add(time.Second), JobID: first.JobID, Status: StatusFailed, TerminalGen: "task8-gen"}
	firstLine := t8MarshalEventLine(t, first)
	secondLine := t8MarshalEventLine(t, second)

	var tail []byte
	wantForensic, wantStore := 1, 1
	switch mode {
	case 0:
		// A complete JSON record without its final newline must be retained and
		// newline-terminated by Store recovery.
		tail = secondLine
		wantForensic, wantStore = 2, 2
	case 1:
		// An incomplete final JSON append is discarded by Store recovery.
		tail = []byte(`{"kind":"job_finished","job_id":"job_task8"`)
	case 2:
		// The forensic reader tolerates an arbitrary final fragment, while Store
		// reports durable corruption rather than pretending it was committed.
		tail = []byte("not-json")
	}
	raw := append(append(append([]byte(nil), firstLine...), '\n'), tail...)

	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write forensic fixture: %v", err)
	}
	forensic, err := ReadEvents(path)
	if err != nil || len(forensic) != wantForensic {
		t.Fatalf("ReadEvents mode %d = %d/%v, want %d/nil", mode, len(forensic), err, wantForensic)
	}

	mem := afero.NewMemMapFs()
	if err := afero.WriteFile(mem, "/jobs.jsonl", raw, 0o644); err != nil {
		t.Fatalf("write memory event fixture: %v", err)
	}
	store, openErr := openFs(mem, "/jobs.jsonl")
	if mode == 2 {
		if openErr == nil {
			t.Fatal("Store accepted corrupt trailing event")
		}
	} else {
		if openErr != nil {
			t.Fatalf("Store recovery mode %d: %v", mode, openErr)
		}
		store.disableSync = true
		events, err := store.LoadEvents()
		if err != nil || len(events) != wantStore {
			t.Fatalf("Store recovered mode %d = %d/%v, want %d/nil", mode, len(events), err, wantStore)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close recovered store: %v", err)
		}
	}

	// Corruption before a later complete line is not a trailing partial write;
	// the public forensic API must surface it rather than silently skipping it.
	middlePath := filepath.Join(t.TempDir(), "mid-file-corrupt.jsonl")
	middle := append(append(append(append([]byte(nil), firstLine...), '\n'), []byte("not-json\n")...), append(secondLine, '\n')...)
	if err := os.WriteFile(middlePath, middle, 0o644); err != nil {
		t.Fatalf("write mid-file corruption: %v", err)
	}
	if _, err := ReadEvents(middlePath); err == nil {
		t.Fatal("ReadEvents accepted mid-file corruption")
	}
}

func t8AssertStoreFaultRollback(t *testing.T) {
	t.Helper()
	sawAppendFault := false
	for failAt := 0; failAt < 48; failAt++ {
		base := afero.NewMemMapFs()
		const path = "/task8-jobs.jsonl"
		seed, err := openFs(base, path)
		if err != nil {
			t.Fatalf("open clean store: %v", err)
		}
		seed.disableSync = true
		if err := seed.Append(t8RollbackEvent("committed")); err != nil {
			t.Fatalf("append committed event: %v", err)
		}
		if err := seed.Close(); err != nil {
			t.Fatalf("close clean store: %v", err)
		}

		faulted, openErr := openFs(fault.FS(base, fault.FromBytes(t8FaultAt(failAt))), path)
		if openErr == nil {
			faulted.disableSync = true
			appendErr := faulted.Append(t8RollbackEvent("candidate"))
			if appendErr != nil && errors.Is(appendErr, fault.ErrInjected) {
				sawAppendFault = true
			}
			_ = faulted.Close()
		}

		// A clean reopen is the rollback oracle: even if the error arrived after
		// a partial write, the committed record must remain readable and the tail
		// must be recoverable before a later daemon restart sees it.
		recovered, err := openFs(base, path)
		if err != nil {
			t.Fatalf("recover after fault %d: %v", failAt, err)
		}
		recovered.disableSync = true
		events, err := recovered.LoadEvents()
		if err != nil {
			t.Fatalf("load after fault %d: %v", failAt, err)
		}
		if err := recovered.Close(); err != nil {
			t.Fatalf("close recovered store %d: %v", failAt, err)
		}
		committed := false
		for _, event := range events {
			if event.JobID == "job_task8_committed" {
				committed = true
				break
			}
		}
		if !committed {
			t.Fatalf("fault %d lost the committed event: %+v", failAt, events)
		}
	}
	if !sawAppendFault {
		t.Fatal("fault sweep did not reach an append rollback path")
	}
}

func t8RollbackEvent(label string) Event {
	return Event{
		Kind:             EventJobStarted,
		TS:               time.Unix(1_700_100_000, 0).UTC(),
		JobID:            "job_task8_" + label,
		Type:             JobShell,
		Command:          "echo " + label,
		OwnerSessionID:   "session_task8",
		VisibleToSession: "session_task8",
	}
}

func t8FaultAt(index int) []byte {
	plan := make([]byte, index+1)
	for i := range plan {
		plan[i] = 1
	}
	plan[index] = 0
	return plan
}

func t8MarshalEventLine(t *testing.T, event Event) []byte {
	t.Helper()
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return b
}

func t8OutputProgramByte(program []byte, index int) byte {
	if len(program) == 0 {
		return 0
	}
	return program[index%len(program)]
}
