package jobstore

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"
)

// jsonEq reports whether a and b marshal to identical JSON. The fold outputs
// carry time.Time and *provenance.Causal, so a JSON compare (not
// reflect.DeepEqual) is required: a JSON-decoded time.Time has no monotonic
// reading and pointer identity is irrelevant after a persist round-trip.
func jsonEq(t *testing.T, a, b any) (bool, []byte, []byte) {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal lhs: %v", err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal rhs: %v", err)
	}
	return bytes.Equal(ab, bb), ab, bb
}

// canonicalWatchSends flattens a WatchSendRecord to a deterministic slice of its
// pending states. WatchSendRecord.Pending is keyed by a struct (WatchSendKey),
// which encoding/json cannot marshal as a map key; each state carries its Key as
// a field, so a key-sorted slice is the faithful, marshalable canonical form.
func canonicalWatchSends(rec WatchSendRecord) []*WatchSendState {
	states := make([]*WatchSendState, 0, len(rec.Pending))
	for _, s := range rec.Pending {
		states = append(states, s)
	}
	sort.Slice(states, func(i, j int) bool {
		ki, _ := json.Marshal(states[i].Key)
		kj, _ := json.Marshal(states[j].Key)
		return string(ki) < string(kj)
	})
	return states
}

// decodeEvents splits a JSONL blob into events, tolerantly skipping lines that
// do not decode (mirroring readAllLocked's per-line decode, minus the hard
// error). This is the fuzz entry point's view of arbitrary on-disk bytes.
func decodeEvents(raw []byte) []Event {
	var events []Event
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		events = append(events, e)
	}
	return events
}

// bySeqAscStripped returns a copy of events sorted by ascending original Seq
// with Seq zeroed. The store reassigns Seq 1..N in append order, so appending in
// ascending-original-Seq order makes the reloaded fold see the same relative
// ordering the in-memory fold imposes — otherwise the round-trip comparison is a
// false positive (the reassigned 1..N would reorder relative to original seqs).
func bySeqAscStripped(events []Event) []Event {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for i := range sorted {
		sorted[i].Seq = 0
	}
	return sorted
}

// FuzzJobEventLogReplay drives the jobstore event-log persistence and reducer
// seam. Input is a raw jobs.jsonl blob (one Event per line). Beyond no-panic it
// asserts, for the in-memory reducers and their on-disk loaders:
//
//   - Every reducer folds without panicking and is deterministic.
//   - PERSIST → RELOAD idempotence for ALL SIX reducer/loader pairs: appending
//     the events to a real fsync'd store and reloading must reproduce the
//     in-memory fold. This is the daemon-restart path at its purest reducer; a
//     field that Append writes but readAllLocked/applyEvent drops, a Seq-ordering
//     regression, or a marshal asymmetry on `any`/pointer fields diverges here.
//   - One-at-a-time Append == AppendBatch: the single and batch write paths must
//     assign Seq and persist identically.
func FuzzJobEventLogReplay(f *testing.F) {
	// Inline bootstrap carries VALID timestamps so the deep oracles are reached;
	// the load-bearing real-shape corpus arrives from 8.4 (testdata/fuzz). Note:
	// the 8.4 seeds scrub `ts` to non-RFC3339 placeholders, so they exercise the
	// decode-rejection floor; these seeds drive the fold/round-trip oracles.
	seeds := []string{
		// One job's full lifecycle, with provenance + structured result.
		`{"kind":"job_started","seq":1,"ts":"2026-06-01T10:00:00Z","job_id":"job_A","type":"shell","command":"ls","owner_session_id":"s1","visible_to_session_id":"s1","started_at":"2026-06-01T10:00:00Z","output_path":"/o/A"}
{"kind":"job_session_assigned","seq":2,"ts":"2026-06-01T10:00:01Z","job_id":"job_A","transcript_ref":"local:job_A","resumable":true}
{"kind":"job_finished","seq":3,"ts":"2026-06-01T10:00:05Z","job_id":"job_A","status":"completed","exit_code":0,"ended_at":"2026-06-01T10:00:05Z","output_bytes":42,"terminal_generation":"g1","structured_result":{"status":"DONE","data":{"n":1}},"structured_result_valid":true}`,
		// Duplicate job_finished: first-terminal-write-wins.
		`{"kind":"job_started","seq":1,"ts":"2026-06-01T10:00:00Z","job_id":"job_B","type":"delegate","owner_session_id":"s1","visible_to_session_id":"s1","delegate_id":"dlg_1","started_at":"2026-06-01T10:00:00Z"}
{"kind":"job_finished","seq":2,"ts":"2026-06-01T10:00:05Z","job_id":"job_B","status":"completed","terminal_generation":"g1"}
{"kind":"job_finished","seq":3,"ts":"2026-06-01T10:00:09Z","job_id":"job_B","status":"failed","terminal_generation":"g2"}`,
		// Out-of-order seqs: pins the sort-then-strip discipline.
		`{"kind":"job_finished","seq":9,"ts":"2026-06-01T10:00:05Z","job_id":"job_C","status":"stopped","terminal_generation":"g1"}
{"kind":"job_started","seq":2,"ts":"2026-06-01T10:00:00Z","job_id":"job_C","type":"shell","owner_session_id":"s1","visible_to_session_id":"s1"}`,
		// Delegate lifecycle (FoldDelegates).
		`{"kind":"delegate_created","seq":1,"ts":"2026-06-01T10:00:00Z","job_id":"job_D","delegate_id":"dlg_2","delegate":{"child_session_id":"c1","agent_type":"engineer","generation":"dg_1","resumable":true}}
{"kind":"job_started","seq":2,"ts":"2026-06-01T10:00:01Z","job_id":"job_D","delegate_id":"dlg_2","owner_session_id":"s1","visible_to_session_id":"s1"}
{"kind":"job_finished","seq":3,"ts":"2026-06-01T10:00:05Z","job_id":"job_D","status":"completed","terminal_generation":"g1"}`,
		// Watch register + clear (FoldWatches).
		`{"kind":"watch_registered","seq":1,"ts":"2026-06-01T10:00:00Z","watch_id":"watch_1","watch":{"generation":"wg_1","owner_session_id":"s1","visible_session_id":"s1","target":"job_A","config_hash":"h1","deliveries":2}}
{"kind":"watch_cleared","seq":2,"ts":"2026-06-01T10:00:05Z","watch_id":"watch_1","watch":{"generation":"wg_1","end_reason":"done"}}`,
		// Watch-send pending then delivered (FoldWatchSends coalescing).
		`{"kind":"watch_send_pending","seq":1,"ts":"2026-06-01T10:00:00Z","job_id":"job_A","watch_send":{"key":{"visible_session_id":"s1","watch_id":"watch_1","watch_target":"job_A","resolved_watched_identity":"job_A","resolved_send_to":"s1","watch_generation":"wg_1"},"delivery_id":"wd_1","update_seq":1,"message":"hi","created_at":"2026-06-01T10:00:00Z"}}
{"kind":"watch_send_delivered","seq":2,"ts":"2026-06-01T10:00:01Z","job_id":"job_A","watch_send":{"key":{"visible_session_id":"s1","watch_id":"watch_1","watch_target":"job_A","resolved_watched_identity":"job_A","resolved_send_to":"s1","watch_generation":"wg_1"},"delivery_id":"wd_1","update_seq":2}}`,
		// Read grants (FoldGrants, order-insensitive, dedup).
		`{"kind":"watch_read_grant","seq":1,"ts":"2026-06-01T10:00:00Z","job_id":"job_A","observer_session_id":"obs1"}
{"kind":"watch_read_grant","seq":2,"ts":"2026-06-01T10:00:01Z","job_id":"job_A","observer_session_id":"obs1"}`,
		`{}`,
		``,
		`not json
{"kind":"job_started","seq":1,"ts":"2026-06-01T10:00:00Z","job_id":"job_E","owner_session_id":"s1","visible_to_session_id":"s1"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		events := decodeEvents(raw)

		// No-panic + determinism across every reducer.
		assertFoldDeterministic(t, events)

		// Persist → reload idempotence for all six reducer/loader pairs.
		sorted := bySeqAscStripped(events)
		dir := t.TempDir()
		s, err := Open(filepath.Join(dir, "jobs.jsonl"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		if err := s.AppendBatch(append([]Event(nil), sorted...)); err != nil {
			t.Fatalf("append batch: %v", err)
		}
		assertReloadMatchesFold(t, s, sorted)
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		// One-at-a-time Append == AppendBatch.
		assertAppendEqualsBatch(t, sorted)
	})
}

func assertFoldDeterministic(t *testing.T, events []Event) {
	t.Helper()
	if eq, a, b := jsonEq(t, Fold(events), Fold(events)); !eq {
		t.Fatalf("Fold is non-deterministic:\n a=%s\n b=%s", a, b)
	}
	if eq, a, b := jsonEq(t, FoldOrdered(events), FoldOrdered(events)); !eq {
		t.Fatalf("FoldOrdered is non-deterministic:\n a=%s\n b=%s", a, b)
	}
	if eq, a, b := jsonEq(t, FoldDelegates(events), FoldDelegates(events)); !eq {
		t.Fatalf("FoldDelegates is non-deterministic:\n a=%s\n b=%s", a, b)
	}
	if eq, a, b := jsonEq(t, FoldWatches(events), FoldWatches(events)); !eq {
		t.Fatalf("FoldWatches is non-deterministic:\n a=%s\n b=%s", a, b)
	}
	if eq, a, b := jsonEq(t, canonicalWatchSends(FoldWatchSends(events)), canonicalWatchSends(FoldWatchSends(events))); !eq {
		t.Fatalf("FoldWatchSends is non-deterministic:\n a=%s\n b=%s", a, b)
	}
	if eq, a, b := jsonEq(t, FoldGrants(events), FoldGrants(events)); !eq {
		t.Fatalf("FoldGrants is non-deterministic:\n a=%s\n b=%s", a, b)
	}
}

// assertReloadMatchesFold compares each loader's reload against the in-memory
// fold of the same (sorted, seq-stripped) events. The store reassigns Seq in
// append order, which equals the in-memory fold's order because the input is
// already ascending; the fold output is otherwise Seq-independent.
func assertReloadMatchesFold(t *testing.T, s *Store, sorted []Event) {
	t.Helper()

	load, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if eq, a, b := jsonEq(t, Fold(sorted), load); !eq {
		t.Fatalf("Fold/Load persist→reload diverged:\n fold=%s\n load=%s", a, b)
	}

	loadOrdered, err := s.LoadOrdered()
	if err != nil {
		t.Fatalf("LoadOrdered: %v", err)
	}
	if eq, a, b := jsonEq(t, FoldOrdered(sorted), loadOrdered); !eq {
		t.Fatalf("FoldOrdered/LoadOrdered persist→reload diverged:\n fold=%s\n load=%s", a, b)
	}

	loadDelegates, err := s.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	if eq, a, b := jsonEq(t, FoldDelegates(sorted), loadDelegates); !eq {
		t.Fatalf("FoldDelegates/LoadDelegates persist→reload diverged:\n fold=%s\n load=%s", a, b)
	}

	loadWatches, err := s.LoadWatches()
	if err != nil {
		t.Fatalf("LoadWatches: %v", err)
	}
	if eq, a, b := jsonEq(t, FoldWatches(sorted), loadWatches); !eq {
		t.Fatalf("FoldWatches/LoadWatches persist→reload diverged:\n fold=%s\n load=%s", a, b)
	}

	loadWatchSends, err := s.LoadWatchSends()
	if err != nil {
		t.Fatalf("LoadWatchSends: %v", err)
	}
	if eq, a, b := jsonEq(t, canonicalWatchSends(FoldWatchSends(sorted)), canonicalWatchSends(loadWatchSends)); !eq {
		t.Fatalf("FoldWatchSends/LoadWatchSends persist→reload diverged:\n fold=%s\n load=%s", a, b)
	}

	loadGrants, err := s.LoadGrants()
	if err != nil {
		t.Fatalf("LoadGrants: %v", err)
	}
	if eq, a, b := jsonEq(t, FoldGrants(sorted), loadGrants); !eq {
		t.Fatalf("FoldGrants/LoadGrants persist→reload diverged:\n fold=%s\n load=%s", a, b)
	}
}

func assertAppendEqualsBatch(t *testing.T, sorted []Event) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open single-append store: %v", err)
	}
	for _, e := range sorted {
		if err := s.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	single, err := s.Load()
	if err != nil {
		t.Fatalf("load single-append store: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close single-append store: %v", err)
	}
	if eq, a, b := jsonEq(t, Fold(sorted), single); !eq {
		t.Fatalf("Append-one-at-a-time != AppendBatch fold:\n batch=%s\n single=%s", a, b)
	}
}
