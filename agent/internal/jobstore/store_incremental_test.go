package jobstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/provenance"
)

// The incremental reload path (the tail cursor in store.go) must be
// indistinguishable from the full reread it replaces. Every test in this file
// compares a store that has been reloaded many times against a FRESH store
// opened on the same bytes, which has no cursor history to be wrong about.

// freshFullRead reads path with a store that has never seen it before, i.e. the
// full-reread result the cursor must reproduce.
func freshFullRead(t *testing.T, path string) []Event {
	t.Helper()
	fresh, err := OpenNoSync(path)
	if err != nil {
		t.Fatalf("open fresh store: %v", err)
	}
	defer func() {
		if err := fresh.Close(); err != nil {
			t.Fatalf("close fresh store: %v", err)
		}
	}()
	events, err := fresh.LoadEvents()
	if err != nil {
		t.Fatalf("fresh LoadEvents: %v", err)
	}
	return events
}

// requireIncrementalMatchesFullReread asserts the cursor-backed store agrees
// with a fresh full reread on the raw events and on every fold the store
// exposes.
func requireIncrementalMatchesFullReread(t *testing.T, s *Store, path, when string) {
	t.Helper()
	got, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("%s: LoadEvents: %v", when, err)
	}
	want := freshFullRead(t, path)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: incremental events differ from full reread:\n got %d events\nwant %d events\n got %+v\nwant %+v",
			when, len(got), len(want), got, want)
	}
	if !reflect.DeepEqual(Fold(got), Fold(want)) {
		t.Fatalf("%s: Fold differs between incremental and full reread", when)
	}
	if !reflect.DeepEqual(FoldOrdered(got), FoldOrdered(want)) {
		t.Fatalf("%s: FoldOrdered differs between incremental and full reread", when)
	}
	if !reflect.DeepEqual(FoldWatches(got), FoldWatches(want)) {
		t.Fatalf("%s: FoldWatches differs between incremental and full reread", when)
	}
	if !reflect.DeepEqual(FoldWatchSends(got), FoldWatchSends(want)) {
		t.Fatalf("%s: FoldWatchSends differs between incremental and full reread", when)
	}
}

// incrementalTestEvent builds a payload-carrying event of a kind chosen by i, so
// the generated logs exercise every fold and every pointer-bearing field rather
// than a single trivial kind.
func incrementalTestEvent(i int) Event {
	ts := time.Unix(1700000000, int64(i)).UTC()
	jobID := fmt.Sprintf("job_%d", i%7)
	switch i % 6 {
	case 0:
		started := ts
		processStarted := ts
		return Event{
			Kind: EventJobStarted, TS: ts, JobID: jobID, Type: JobShell,
			Task: strings.Repeat("t", i%17), OwnerSessionID: "ses_owner",
			VisibleToSession: "ses_owner", StartedAt: &started, OutputPath: "/tmp/out",
			Background: true, ProcessGroupID: 4242, ProcessStartTime: &processStarted,
			Provenance: &provenance.Causal{
				WatchKeys: []provenance.WatchKey{{WatchID: "w1", WatchGeneration: "g1"}},
				FireKeys:  []provenance.FireKey{{WatchID: "w1", WatchGeneration: "g1", DeliveryID: "d1"}},
			},
		}
	case 1:
		return Event{
			Kind: EventJobNotificationPending, TS: ts, JobID: jobID, TerminalGen: fmt.Sprintf("gen_%d", i),
			Provenance: &provenance.Causal{Chain: []provenance.Entry{{Kind: "job", JobID: jobID}}},
		}
	case 2:
		code := i % 5
		ended := ts
		valid := i%2 == 1
		return Event{
			Kind: EventJobFinished, TS: ts, JobID: jobID, Status: StatusCompleted,
			ExitCode: &code, EndedAt: &ended, OutputBytes: int64(i),
			TerminalGen: fmt.Sprintf("gen_%d", i), StructuredResultValid: &valid,
			StructuredResult: map[string]any{"ok": true, "items": []any{float64(i), "s"}},
			Provenance:       &provenance.Causal{ChainTruncated: true, Chain: []provenance.Entry{{Kind: "job", JobID: jobID}}},
		}
	case 3:
		return Event{
			Kind: EventJobNotificationDelivered, TS: ts, JobID: jobID, TerminalGen: fmt.Sprintf("gen_%d", i),
		}
	case 4:
		return Event{
			Kind: EventWatchRegistered, TS: ts, WatchID: fmt.Sprintf("w_%d", i%3),
			Watch: &WatchEvent{
				Generation: fmt.Sprintf("g%d", i), OwnerSessionID: "ses_owner",
				VisibleSessionID: "ses_owner", Target: "job:1", ConfigHash: "h",
				Condition: "done", Deliveries: i,
				Config: &WatchConfigSnapshot{
					Target: "job:1", Events: []string{"tool", "message"},
					EventFilter:       &WatchEventFilterSnapshot{ToolName: "bash", Status: "ok"},
					ReceiverSessionID: "ses_owner",
				},
			},
		}
	default:
		return Event{
			Kind: EventWatchSendPending, TS: ts,
			WatchSend: &WatchSendState{
				Key:        WatchSendKey{VisibleSessionID: "ses_owner", WatchID: fmt.Sprintf("w_%d", i%3), WatchTarget: "job:1", WatchGeneration: "g1"},
				DeliveryID: fmt.Sprintf("d%d", i), UpdateSeq: uint64(i), Frame: "m",
				CreatedAt: ts, UpdatedAt: ts,
				Provenance: &provenance.Causal{
					Chain:    []provenance.Entry{{Kind: "watch", WatchID: "w1"}},
					FireKeys: []provenance.FireKey{{WatchID: "w1", WatchGeneration: "g1", DeliveryID: "d1"}},
				},
			},
		}
	}
}

// TestStoreIncrementalReloadMatchesFullReread interleaves appends, batches and
// loads pseudo-randomly (fixed seeds, so a failure reproduces exactly) and
// checks after every step that the cursor-backed store still reports exactly
// what a fresh full reread does.
func TestStoreIncrementalReloadMatchesFullReread(t *testing.T) {
	t.Parallel()
	for _, seed := range []int64{1, 2, 3} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(seed))
			path := filepath.Join(t.TempDir(), "jobs.jsonl")
			s, err := OpenNoSync(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			}()
			requireIncrementalMatchesFullReread(t, s, path, "empty log")
			next := 0
			for step := range 30 {
				switch rng.Intn(4) {
				case 0:
					if err := s.Append(incrementalTestEvent(next)); err != nil {
						t.Fatalf("step %d: append: %v", step, err)
					}
					next++
				case 1:
					n := rng.Intn(4) + 1
					batch := make([]Event, 0, n)
					for range n {
						batch = append(batch, incrementalTestEvent(next))
						next++
					}
					if err := s.AppendBatch(batch); err != nil {
						t.Fatalf("step %d: append batch: %v", step, err)
					}
				default:
					// A bare load with no intervening append must also agree.
				}
				requireIncrementalMatchesFullReread(t, s, path, fmt.Sprintf("step %d", step))
			}
		})
	}
}

// TestStoreIncrementalReloadSeesOutOfBandRewrite pins the coherence rule that
// makes the cursor safe: the file on disk is the truth. A writer outside this
// Store — the agent tests rewrite jobs.jsonl in place to simulate corrupt
// durable state — must be visible on the next load, whether the rewrite grew,
// shrank, or kept the file's length — and whether or not the store appends to
// the rewritten log before anyone reloads it. That second ordering is the one
// with teeth: an append moves the file's length and mtime forward, so a store
// that only compared the file against its own last write would take the foreign
// rewrite for its own history and serve the pre-rewrite prefix forever.
func TestStoreIncrementalReloadSeesOutOfBandRewrite(t *testing.T) {
	t.Parallel()
	rewrites := map[string]func(events []Event) []Event{
		"grow": func(events []Event) []Event {
			events[0].Task = strings.Repeat("longer-task", 20)
			return events
		},
		"shrink": func(events []Event) []Event {
			return events[:1]
		},
		// Same total length, different bytes: only the file's modification time
		// distinguishes this rewrite from no rewrite at all.
		"same-length-body": func(events []Event) []Event {
			events[0].JobID = "job_X"
			events[0].OwnerSessionID = "ses_other"
			return events
		},
		"drop-tail": func(events []Event) []Event {
			return events[:len(events)-1]
		},
	}
	for name, rewrite := range rewrites {
		for _, loadBeforeAppend := range []bool{true, false} {
			order := "reload-then-append"
			if !loadBeforeAppend {
				order = "append-then-reload"
			}
			t.Run(name+"/"+order, func(t *testing.T) {
				t.Parallel()
				path := filepath.Join(t.TempDir(), "jobs.jsonl")
				s, err := OpenNoSync(path)
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				defer func() { _ = s.Close() }()
				seed := make([]Event, 0, 6)
				for i := range 6 {
					seed = append(seed, incrementalTestEvent(i))
				}
				if err := s.AppendBatch(seed); err != nil {
					t.Fatalf("append batch: %v", err)
				}
				// Prime the cursor, then rewrite the log behind the store's back.
				requireIncrementalMatchesFullReread(t, s, path, "primed")
				events := freshFullRead(t, path)
				rewriteLog(t, path, rewrite(events))
				if loadBeforeAppend {
					requireIncrementalMatchesFullReread(t, s, path, "after rewrite")
				}
				if err := s.Append(incrementalTestEvent(99)); err != nil {
					t.Fatalf("append after rewrite: %v", err)
				}
				requireIncrementalMatchesFullReread(t, s, path, "after append following rewrite")
			})
		}
	}
}

// rewriteLog replaces jobs.jsonl wholesale, the way the agent-package test
// helpers do, keeping each event's own seq so the log stays well-formed.
func rewriteLog(t *testing.T, path string, events []Event) {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	// A distinct mtime is what tells the store the bytes are not its own. The
	// write usually supplies it, but filesystem timestamps are coarser than this
	// test is fast: a rewrite landing in the same clock tick as the store's last
	// append is the documented same-size-same-mtime boundary
	// (TestStoreCursorSameSizeSameMTimeRewriteIsTheDocumentedBoundary), not the
	// out-of-band write these tests mean to stage. Bump the mtime until it
	// resolves.
	before, statErr := os.Stat(path)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("rewrite log: %v", err)
	}
	if statErr != nil {
		return
	}
	for delta := time.Millisecond; ; delta *= 2 {
		after, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat rewritten log: %v", err)
		}
		if !after.ModTime().Equal(before.ModTime()) {
			return
		}
		if err := os.Chtimes(path, time.Time{}, before.ModTime().Add(delta)); err != nil {
			t.Fatalf("bump rewritten log mtime: %v", err)
		}
	}
}

// TestStoreIncrementalReloadSeesDeletedLog covers the file vanishing under a
// primed cursor: the store reports the empty log the disk now holds, and
// recovers when the file comes back.
func TestStoreIncrementalReloadSeesDeletedLog(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := OpenNoSync(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got, err := s.LoadEvents(); err != nil || len(got) != 1 {
		t.Fatalf("primed load: got %d events, err %v", len(got), err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove log: %v", err)
	}
	got, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("load after delete: got %d events, want 0", len(got))
	}
	rewriteLog(t, path, []Event{incrementalTestEvent(1)})
	requireIncrementalMatchesFullReread(t, s, path, "after recreate")
}

// TestStoreIncrementalReloadTornTrailingLine drives the torn-write path with a
// primed cursor: a partial trailing line appended out of band is recovered
// exactly as it is on a cold open, and the cursor never commits an
// unterminated line.
func TestStoreIncrementalReloadTornTrailingLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := OpenNoSync(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.AppendBatch([]Event{incrementalTestEvent(0), incrementalTestEvent(1)}); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	requireIncrementalMatchesFullReread(t, s, path, "primed")

	// Tear a third line: a syntactically incomplete, unterminated JSON object.
	line, err := json.Marshal(incrementalTestEvent(2))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for tear: %v", err)
	}
	if _, err := f.Write(line[:len(line)/2]); err != nil {
		t.Fatalf("write torn line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close torn: %v", err)
	}

	got, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load after tear: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("load after tear: got %d events, want the 2 durable ones", len(got))
	}
	// Recovery truncated the torn tail, so a later append lands cleanly and the
	// cursor agrees with a cold read.
	if err := s.Append(incrementalTestEvent(3)); err != nil {
		t.Fatalf("append after tear: %v", err)
	}
	requireIncrementalMatchesFullReread(t, s, path, "after append following tear")
}

// TestStoreIncrementalReloadIndependentSnapshots pins the other half of the
// cursor's contract: each load hands back its own objects. A caller that
// mutates a loaded event or record must not change what the next load reports.
func TestStoreIncrementalReloadIndependentSnapshots(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := OpenNoSync(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	for i := range 6 {
		if err := s.Append(incrementalTestEvent(i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	before := freshFullRead(t, path)

	first, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	for i := range first {
		mutateEventDeeply(&first[i])
	}
	recs, err := s.Load()
	if err != nil {
		t.Fatalf("fold load: %v", err)
	}
	for _, r := range recs {
		r.StructuredResult = "mutated"
	}

	after, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("mutating a loaded snapshot changed a later load:\n got %+v\nwant %+v", after, before)
	}
}

// mutateEventDeeply writes through every reference-typed field an event can
// carry, so a shared pointer anywhere in the load path shows up as corruption.
func mutateEventDeeply(e *Event) {
	e.Task = "mutated"
	if e.StartedAt != nil {
		*e.StartedAt = time.Unix(0, 0)
	}
	if e.EndedAt != nil {
		*e.EndedAt = time.Unix(0, 0)
	}
	if e.ExitCode != nil {
		*e.ExitCode = 99
	}
	if e.ProcessStartTime != nil {
		*e.ProcessStartTime = time.Unix(0, 0)
	}
	if e.StructuredResultValid != nil {
		*e.StructuredResultValid = !*e.StructuredResultValid
	}
	if m, ok := e.StructuredResult.(map[string]any); ok {
		m["mutated"] = true
		if items, ok := m["items"].([]any); ok && len(items) > 0 {
			items[0] = "mutated"
		}
	}
	mutateCausal(e.Provenance)
	if ws := e.WatchSend; ws != nil {
		ws.Frame = "mutated"
		mutateCausal(ws.Provenance)
	}
	if w := e.Watch; w != nil {
		w.Condition = "mutated"
		if c := w.Config; c != nil {
			c.Target = "mutated"
			if len(c.Events) > 0 {
				c.Events[0] = "mutated"
			}
			if f := c.EventFilter; f != nil {
				f.ToolName = "mutated"
			}
		}
	}
}

func mutateCausal(p *provenance.Causal) {
	if p == nil {
		return
	}
	p.ChainTruncated = !p.ChainTruncated
	if len(p.Chain) > 0 {
		p.Chain[0].Kind = "mutated"
	}
	if len(p.WatchKeys) > 0 {
		p.WatchKeys[0].WatchID = "mutated"
	}
	if len(p.FireKeys) > 0 {
		p.FireKeys[0].WatchID = "mutated"
	}
}

// TestCloneEventCoversEveryReferenceField is the completeness net for
// cloneEvent: it walks Event's TYPE graph for every field that carries a
// reference (pointer, slice, map, interface) and fails when the set changes.
// A new reference field must be added to cloneEvent, to the fixture in
// incrementalTestEvent, and to mutateEventDeeply — then listed here.
func TestCloneEventCoversEveryReferenceField(t *testing.T) {
	t.Parallel()
	want := []string{
		"Event.EndedAt",
		"Event.ExitCode",
		"Event.ProcessStartTime",
		"Event.Provenance",
		"Event.Provenance.Chain",
		"Event.Provenance.FireKeys",
		"Event.Provenance.WatchKeys",
		"Event.StartedAt",
		"Event.StructuredResult",
		"Event.StructuredResultValid",
		"Event.Watch",
		"Event.Watch.Config",
		"Event.Watch.Config.EventFilter",
		"Event.Watch.Config.Events",
		"Event.WatchSend",
		"Event.WatchSend.Provenance",
		"Event.WatchSend.Provenance.Chain",
		"Event.WatchSend.Provenance.FireKeys",
		"Event.WatchSend.Provenance.WatchKeys",
	}
	got := referenceFieldPaths(reflect.TypeFor[Event](), "Event", map[reflect.Type]bool{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Event's reference fields changed; update cloneEvent, the test fixture and this list.\n got %#v\nwant %#v", got, want)
	}
}

// referenceFieldPaths lists, in sorted order, every field path under t that
// holds a pointer, slice, map or interface. time.Time is treated as a leaf
// value: it is copied by assignment and carries no mutable state a caller can
// reach.
func referenceFieldPaths(t reflect.Type, prefix string, seen map[reflect.Type]bool) []string {
	if seen[t] {
		return nil
	}
	seen[t] = true
	defer delete(seen, t)
	var out []string
	for f := range t.Fields() {
		path := prefix + "." + f.Name
		ft := f.Type
		switch ft.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
			out = append(out, path)
			elem := ft
			for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Slice || elem.Kind() == reflect.Map {
				elem = elem.Elem()
			}
			if elem.Kind() == reflect.Struct && elem != reflect.TypeFor[time.Time]() {
				out = append(out, referenceFieldPaths(elem, path, seen)...)
			}
		case reflect.Struct:
			if ft != reflect.TypeFor[time.Time]() {
				out = append(out, referenceFieldPaths(ft, path, seen)...)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestStoreCursorSameSizeSameMTimeRewriteIsTheDocumentedBoundary states, as a
// test rather than as a comment, exactly how far the cursor's coherence goes: a
// foreign rewrite that preserves the log's length AND restores its modification
// time is indistinguishable from no rewrite, and the store keeps serving the
// prefix it read. Nothing in evener does this — the log is append-only by contract
// and the owning store is its only sanctioned writer, and every rewrite the test
// suites actually perform changes the length or the mtime (see
// TestStoreIncrementalReloadSeesOutOfBandRewrite). Detecting this case would
// mean re-reading the prefix on every load, which is the cost the cursor exists
// to remove. If a future change strengthens the file identity, this test fails
// and should be replaced by one asserting detection.
func TestStoreCursorSameSizeSameMTimeRewriteIsTheDocumentedBoundary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := OpenNoSync(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	seed := make([]Event, 0, 6)
	for i := range 6 {
		seed = append(seed, incrementalTestEvent(i))
	}
	if err := s.AppendBatch(seed); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	primed, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("primed load: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	events := freshFullRead(t, path)
	events[0].JobID = "job_X"
	events[0].OwnerSessionID = "ses_other"
	rewriteLog(t, path, events)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after rewrite: %v", err)
	}
	if after.Size() != info.Size() {
		t.Fatalf("fixture rewrote %d bytes over %d; it must preserve the length to test the boundary", after.Size(), info.Size())
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}

	got, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load after undetectable rewrite: %v", err)
	}
	if !reflect.DeepEqual(got, primed) {
		t.Fatalf("the boundary moved: the store detected a same-length, same-mtime rewrite.\nUpdate the fileCursor doc comment and this test to assert detection.")
	}
	// The rewrite IS on disk; only this store's cursor is behind.
	if disk := freshFullRead(t, path); disk[0].JobID != "job_X" {
		t.Fatalf("fixture did not land the rewrite on disk: %+v", disk[0])
	}
}

// frozenTimeFs reports the same modification time forever, standing in for a
// filesystem whose timestamps cannot resolve a write: a coarse-granularity mount,
// or one caching attributes (NFS).
type frozenTimeFs struct {
	afero.Fs
}

func (f frozenTimeFs) Stat(name string) (os.FileInfo, error) {
	info, err := f.Fs.Stat(name)
	if err != nil {
		return nil, err
	}
	return frozenTimeInfo{FileInfo: info}, nil
}

type frozenTimeInfo struct {
	os.FileInfo
}

func (frozenTimeInfo) ModTime() time.Time { return time.Unix(1700000000, 0).UTC() }

// TestStoreCursorDisablesItselfWhenMTimeCannotResolveWrites covers the
// calibration that keeps the cursor honest on a filesystem whose timestamps
// cannot tell a write from no write. The store notices that one of its OWN
// appends changed the length without moving the mtime, gives the cursor up for
// good, and goes back to reading the whole file — so even a same-length foreign
// rewrite, which mtime could never have caught here, is still seen.
func TestStoreCursorDisablesItselfWhenMTimeCannotResolveWrites(t *testing.T) {
	t.Parallel()
	fs := frozenTimeFs{Fs: afero.NewMemMapFs()}
	const path = "/jobs.jsonl"
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := s.Load(); err != nil {
		t.Fatalf("prime load: %v", err)
	}
	if s.cursor.disabled {
		t.Fatal("cursor disabled before any append could prove the mtime useless")
	}
	if err := s.Append(incrementalTestEvent(1)); err != nil {
		t.Fatalf("second append: %v", err)
	}
	if !s.cursor.disabled {
		t.Fatal("an append that changed the length without moving the mtime must disable the cursor")
	}

	// From here the store must behave exactly as it did before the cursor existed:
	// full rereads, so an undetectable-by-mtime rewrite is still seen.
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load after disable: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("load after disable returned %d events, want 2", len(events))
	}
	raw, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	rewritten := bytes.Replace(raw, []byte(`"job_0"`), []byte(`"job_X"`), 1)
	if len(rewritten) != len(raw) {
		t.Fatalf("fixture rewrite changed the length (%d -> %d)", len(raw), len(rewritten))
	}
	if err := afero.WriteFile(fs, path, rewritten, 0o644); err != nil {
		t.Fatalf("rewrite log: %v", err)
	}
	after, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load after rewrite: %v", err)
	}
	if after[0].JobID != "job_X" {
		t.Fatalf("load after rewrite returned job %q, want the rewritten job_X", after[0].JobID)
	}
}

// staleStatFs answers every Stat with the metadata the file had when the fs was
// armed, standing in for a mount whose attributes lag behind its data (NFS
// attribute caching being the everyday example). The bytes it hands back on read
// are always current — only the metadata lies.
type staleStatFs struct {
	afero.Fs
	stale os.FileInfo
}

// snapshotFileInfo copies the metadata VALUES out of a FileInfo. afero's
// in-memory FileInfo is a live view of the file it names — its Size() tracks
// later writes — so a stale-metadata fixture has to hold its own copy.
type snapshotFileInfo struct {
	name string
	size int64
	mode os.FileMode
	mod  time.Time
}

func snapshotInfo(info os.FileInfo) os.FileInfo {
	return snapshotFileInfo{name: info.Name(), size: info.Size(), mode: info.Mode(), mod: info.ModTime()}
}

func (i snapshotFileInfo) Name() string       { return i.name }
func (i snapshotFileInfo) Size() int64        { return i.size }
func (i snapshotFileInfo) Mode() os.FileMode  { return i.mode }
func (i snapshotFileInfo) ModTime() time.Time { return i.mod }
func (i snapshotFileInfo) IsDir() bool        { return false }
func (i snapshotFileInfo) Sys() any           { return nil }

func (f *staleStatFs) Stat(name string) (os.FileInfo, error) {
	info, err := f.Fs.Stat(name)
	if err != nil {
		return nil, err
	}
	if f.stale != nil {
		return f.stale, nil
	}
	return info, nil
}

// TestStoreLoadSeesAppendedEventDespiteStaleStat is the load-side half of not
// trusting reported metadata. A filesystem that answers the post-append stat with
// the PRE-append size and mtime must not be able to convince the store that
// nothing was appended: the durable event is on disk, so the load must return it.
// Reported lengths decide nothing about what has been read — only bytes do.
func TestStoreLoadSeesAppendedEventDespiteStaleStat(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	base := afero.NewMemMapFs()
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	// Prime the cursor on an empty log, which is the worst case: the cursor holds
	// no events and a stale size of 0 would say there is nothing to read.
	if _, err := s.Load(); err != nil {
		t.Fatalf("prime load: %v", err)
	}
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)

	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load after append under stale stat: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("load returned %d events, want the 1 durably appended; a stale stat hid it", len(events))
	}
	// And it keeps working as the log grows, still under lying metadata.
	if err := s.AppendBatch([]Event{incrementalTestEvent(1), incrementalTestEvent(2)}); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	events, err = s.LoadEvents()
	if err != nil {
		t.Fatalf("second load under stale stat: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("load returned %d events, want 3", len(events))
	}
}

// TestStoreCursorHoldsOnlyDecodedValues pins the assumption cloneJSONValue
// rests on: the events the cursor keeps come from json.Unmarshal and nothing
// else, so an `any` payload is always a JSON container or scalar. A Go value
// that json cannot round-trip to itself comes back decoded, never as the
// caller's own object.
func TestStoreCursorHoldsOnlyDecodedValues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := OpenNoSync(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	type payload struct {
		Count int `json:"count"`
	}
	caller := &payload{Count: 1}
	if err := s.Append(Event{
		Kind: EventJobFinished, JobID: "job_A", Status: StatusCompleted,
		TerminalGen: "gen_1", StructuredResult: caller,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Mutating the appended object cannot reach the log or the cursor.
	caller.Count = 99

	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := events[0].StructuredResult.(map[string]any)
	if !ok {
		t.Fatalf("StructuredResult = %T, want a decoded map; the cursor is holding a caller's value", events[0].StructuredResult)
	}
	if got["count"] != float64(1) {
		t.Fatalf("decoded count = %v, want the appended 1", got["count"])
	}
}

// countingFs counts the bytes a store reads back out of the log, so a test can
// assert on the work a reload does rather than on how long it takes.
type countingFs struct {
	afero.Fs
	bytesRead *int64
}

func (c countingFs) Open(name string) (afero.File, error) {
	f, err := c.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return countingFile{File: f, bytesRead: c.bytesRead}, nil
}

type countingFile struct {
	afero.File
	bytesRead *int64
}

func (c countingFile) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	*c.bytesRead += int64(n)
	return n, err
}

func (c countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.File.ReadAt(p, off)
	*c.bytesRead += int64(n)
	return n, err
}

// TestStoreReloadReadsOnlyNewBytes is the property the tail cursor exists for,
// stated as work rather than as wall time: reloading a log after one append
// reads that append, not the whole file. Before the cursor every reload read the
// file from byte zero, which made a session's delegate creations quadratic in
// its own event count.
func TestStoreReloadReadsOnlyNewBytes(t *testing.T) {
	t.Parallel()
	var bytesRead int64
	fs := countingFs{Fs: afero.NewMemMapFs(), bytesRead: &bytesRead}
	const path = "/jobs.jsonl"
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()
	seed := make([]Event, 0, 200)
	for i := range 200 {
		seed = append(seed, incrementalTestEvent(i))
	}
	if err := s.AppendBatch(seed); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	if _, err := s.Load(); err != nil {
		t.Fatalf("prime load: %v", err)
	}
	info, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	logSize := info.Size()
	if logSize < 20*1024 {
		t.Fatalf("fixture log is only %d bytes; too small to tell a tail read from a full one", logSize)
	}

	// One more append, one more reload: the reload must read the new line, not
	// the log. The allowance covers the appended line plus the fixed trailing-byte
	// probe every read makes.
	before := bytesRead
	if err := s.Append(incrementalTestEvent(200)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	incremental := bytesRead - before
	if incremental > logSize/8 {
		t.Fatalf("reload after one append read %d bytes of a %d byte log; the cursor is not tailing", incremental, logSize)
	}

	// A store that has never seen the file has no cursor and must read all of it,
	// which is what the number above is being compared against.
	before = bytesRead
	cold, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("cold openFs: %v", err)
	}
	defer func() { _ = cold.Close() }()
	if full := bytesRead - before; full < logSize {
		t.Fatalf("cold open read %d bytes of a %d byte log; the fixture is not measuring reads", full, logSize)
	}
}

// BenchmarkStoreLoadAfterAppend is the shape of the production hot loop the
// cursor exists for: a session that appends an event and reloads its records,
// over and over, as each delegate is created. Before the cursor, ns/op grew
// with the total size of the log (every reload reread and re-decoded the whole
// file); with it, the per-reload decode is proportional to the ONE new event,
// so the numbers across log sizes flatten out.
func BenchmarkStoreLoadAfterAppend(b *testing.B) {
	for _, existing := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("existing=%d", existing), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "jobs.jsonl")
			s, err := OpenNoSync(path)
			if err != nil {
				b.Fatalf("open: %v", err)
			}
			defer func() { _ = s.Close() }()
			seed := make([]Event, 0, existing)
			for i := range existing {
				seed = append(seed, incrementalTestEvent(i))
			}
			if err := s.AppendBatch(seed); err != nil {
				b.Fatalf("append batch: %v", err)
			}
			if _, err := s.Load(); err != nil {
				b.Fatalf("prime load: %v", err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.Append(incrementalTestEvent(existing + i)); err != nil {
					b.Fatalf("append: %v", err)
				}
				if _, err := s.Load(); err != nil {
					b.Fatalf("load: %v", err)
				}
			}
		})
	}
}

// BenchmarkStoreAppendWithLiveCursor prices what the cursor costs the WRITE
// path: while a cursor is live, each append stats the log twice — once to check
// the file is still the one the cursor holds before extending it, once to record
// where it now ends. Run with -benchtime to compare against a store whose cursor
// was never primed (no load, so no stats), which is the shape of the append-only
// producer that never reloads.
func BenchmarkStoreAppendWithLiveCursor(b *testing.B) {
	for _, primed := range []bool{true, false} {
		name := "cursor=live"
		if !primed {
			name = "cursor=absent"
		}
		b.Run(name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "jobs.jsonl")
			s, err := OpenNoSync(path)
			if err != nil {
				b.Fatalf("open: %v", err)
			}
			defer func() { _ = s.Close() }()
			if primed {
				if _, err := s.Load(); err != nil {
					b.Fatalf("prime load: %v", err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.Append(incrementalTestEvent(i)); err != nil {
					b.Fatalf("append: %v", err)
				}
			}
		})
	}
}

// BenchmarkStoreRepeatLoad isolates the reload itself: no appends at all, so
// every iteration after the first is pure cursor bookkeeping.
func BenchmarkStoreRepeatLoad(b *testing.B) {
	for _, existing := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("existing=%d", existing), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "jobs.jsonl")
			s, err := OpenNoSync(path)
			if err != nil {
				b.Fatalf("open: %v", err)
			}
			defer func() { _ = s.Close() }()
			seed := make([]Event, 0, existing)
			for i := range existing {
				seed = append(seed, incrementalTestEvent(i))
			}
			if err := s.AppendBatch(seed); err != nil {
				b.Fatalf("append batch: %v", err)
			}
			if _, err := s.Load(); err != nil {
				b.Fatalf("prime load: %v", err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Load(); err != nil {
					b.Fatalf("load: %v", err)
				}
			}
		})
	}
}

// TestStoreFoldCacheSeesForeignAppendAfterStaleStat pins the hole closed in
// foldCurrentLocked: a cache hit requires the cursor to have consumed through
// the reported file size, not just to trust its recorded size and mtime. A
// filesystem whose stat lags can otherwise leave cursor.offset ahead of
// cursor.size while still reporting the old size; without the consumed-offset
// check the stale stat keeps matching and repeated folded loads would serve
// the old cached fold, hiding an out-of-band append that readAllLocked would
// rescan for (info.Size() != cursor.offset).
func TestStoreFoldCacheSeesForeignAppendAfterStaleStat(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	base := afero.NewMemMapFs()
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	first, err := s.Load()
	if err != nil {
		t.Fatalf("prime load: %v", err)
	}
	nFirst := len(first)
	// Repeat the folded load so the fold cache is populated and served.
	second, err := s.Load()
	if err != nil {
		t.Fatalf("repeat load: %v", err)
	}
	if len(second) != nFirst {
		t.Fatalf("repeat load returned %d jobs, want %d", len(second), nFirst)
	}
	// Freeze the stat, then append through the store: noteAppendedLocked sees
	// the stale size, drops the cursor, and the next load rescans to the real
	// EOF while the recorded size and mtime stay frozen — leaving
	// cursor.offset ahead of cursor.size with a stat that still matches both
	// recorded values, exactly what readAllLocked's rescan guard
	// (info.Size() != cursor.offset) is written to catch.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)
	if err := s.Append(incrementalTestEvent(1)); err != nil {
		t.Fatalf("append under stale stat: %v", err)
	}
	afterStale, err := s.Load()
	if err != nil {
		t.Fatalf("load under stale stat: %v", err)
	}
	if _, ok := afterStale["job_0"]; !ok {
		t.Fatalf("load under stale stat lost job_0: got %d jobs", len(afterStale))
	}
	// The load above rescanned to the real EOF while the stat still reports
	// the frozen size, so the cursor has consumed past its recorded size.
	if s.cursor.offset <= s.cursor.size {
		t.Fatalf("fixture did not diverge: offset=%d size=%d, want offset ahead of size",
			s.cursor.offset, s.cursor.size)
	}
	// Repeat the folded load so the (stale-based) fold is cached and served.
	if _, err := s.Load(); err != nil {
		t.Fatalf("repeat load under stale stat: %v", err)
	}
	// An out-of-band writer appends past both the stale size and the consumed
	// offset. The bytes are current on read; only the stat lies.
	// A job_started event folds into Load()'s job records (a watch event would
	// not), so the hidden-append assertion below is meaningful.
	foreign := Event{Kind: EventJobStarted, Seq: 3, JobID: "job_foreign_stale_stat", Type: JobShell}
	line, err := json.Marshal(foreign)
	if err != nil {
		t.Fatalf("marshal foreign event: %v", err)
	}
	raw, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatalf("read log for foreign append: %v", err)
	}
	raw = append(raw, append(line, '\n')...)
	if err := afero.WriteFile(base, path, raw, 0o644); err != nil {
		t.Fatalf("write foreign event: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("load after foreign append: %v", err)
	}
	if _, ok := got["job_foreign_stale_stat"]; !ok {
		t.Fatalf("load hid the foreign append: got %d jobs without job_foreign_stale_stat", len(got))
	}
}

// The committed boundary is the seam the live activity fold reads through: it
// says how far a reader outside the store may fold, and it carries the content
// generation that decides whether a previously folded prefix may be extended.
// These tests pin the three properties the fold depends on, at the store rather
// than through the fold: an append is growth (not a new generation), a
// truncation and an in-place rewrite each move it, and a torn tail is repaired
// so it cannot be joined onto.

// TestCommittedJournalOrdinaryAppendKeepsTheGeneration pins the property the
// incremental fold's whole economy rests on: two boundary reads across an
// ordinary append report the SAME generation and a longer committed prefix, so
// the fold extends its prefix instead of rebuilding it.
func TestCommittedJournalOrdinaryAppendKeepsTheGeneration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	before, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal before any append: %v", err)
	}
	if before.Path != path {
		t.Fatalf("boundary path = %q, want the journal's own %q", before.Path, path)
	}
	if before.Size != 0 {
		t.Fatalf("boundary size on an empty journal = %d, want 0", before.Size)
	}
	if !before.MetadataResolvesWrites {
		t.Fatalf("a fresh store reported MetadataResolvesWrites=false before any append observed otherwise")
	}

	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("first append: %v", err)
	}
	first, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after first append: %v", err)
	}
	if first.Size <= before.Size {
		t.Fatalf("boundary size = %d after an append, want it past the %d it started at", first.Size, before.Size)
	}
	if first.Generation != before.Generation {
		t.Fatalf("generation moved %d -> %d across an ordinary append: a fold would rebuild instead of extending", before.Generation, first.Generation)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if first.Size != info.Size() {
		t.Fatalf("boundary size = %d, want the journal's committed length %d", first.Size, info.Size())
	}

	// A second read of an unchanged journal reports the same thing again: the
	// boundary is a sample, not a one-shot event a reader consumes.
	again, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("repeated CommittedJournal: %v", err)
	}
	if again.Size != first.Size || again.Generation != first.Generation {
		t.Fatalf("repeated boundary read = (size %d, gen %d), want the unchanged (size %d, gen %d)", again.Size, again.Generation, first.Size, first.Generation)
	}

	// And the next append keeps extending the same generation.
	if err := s.Append(incrementalTestEvent(1)); err != nil {
		t.Fatalf("second append: %v", err)
	}
	second, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after second append: %v", err)
	}
	if second.Generation != first.Generation {
		t.Fatalf("generation moved %d -> %d across a second ordinary append", first.Generation, second.Generation)
	}
	if second.Size <= first.Size {
		t.Fatalf("boundary size = %d after a second append, want it past the %d it was", second.Size, first.Size)
	}
}

// TestCommittedJournalTruncationMovesTheGeneration pins the one change that
// must invalidate a fold: a journal that shrank is not the file any prefix
// describes, so its content generation has to move even though the store is
// still open on the same handle.
func TestCommittedJournalTruncationMovesTheGeneration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	for i := range 4 {
		if err := s.Append(incrementalTestEvent(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	full, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// Cut the journal in place, keeping the first record whole.
	raw, err := afero.ReadFile(afero.NewOsFs(), path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	cut := bytes.IndexByte(raw, '\n')
	if cut < 0 {
		t.Fatalf("fixture journal holds no complete record")
	}
	if err := os.Truncate(path, int64(cut+1)); err != nil {
		t.Fatalf("truncate journal: %v", err)
	}

	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after truncation: %v", err)
	}
	if after.Generation <= full.Generation {
		t.Fatalf("generation = %d after a truncation, want it past the %d it reported for the longer journal: a fold would extend a prefix the file no longer has", after.Generation, full.Generation)
	}
	if after.Size >= full.Size {
		t.Fatalf("boundary size = %d after truncation, want it below the previous %d", after.Size, full.Size)
	}
}

// TestCommittedJournalSameLengthRewriteMovesTheGeneration pins the case length
// alone cannot see: the journal was replaced with different bytes of the same
// length, so the only evidence is the modification time, and the generation
// must move for it.
func TestCommittedJournalSameLengthRewriteMovesTheGeneration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	first, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}

	// Rewrite the same LENGTH with different bytes, and put the mtime an hour
	// into the future so no clock granularity can hide the change.
	rewritten := bytes.Replace(raw, []byte(`"job_0"`), []byte(`"job_9"`), 1)
	if len(rewritten) != len(raw) {
		t.Fatalf("fixture rewrite changed the length (%d -> %d)", len(raw), len(rewritten))
	}
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		t.Fatalf("rewrite journal: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("stamp rewritten journal: %v", err)
	}

	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after rewrite: %v", err)
	}
	if after.Generation <= first.Generation {
		t.Fatalf("generation = %d after a same-length rewrite, want it past the %d it reported before: a fold would keep the rows the rewrite removed", after.Generation, first.Generation)
	}
	if after.Size != first.Size {
		t.Fatalf("boundary size = %d after a same-length rewrite, want the unchanged %d", after.Size, first.Size)
	}
}

// TestCommittedJournalRepairsATornTail pins that a partial trailing record is
// settled before the boundary is reported, so the record the next append writes
// lands on its own line instead of joining the fragment.
func TestCommittedJournalRepairsATornTail(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	committed, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// Leave a fragment of a record behind the committed prefix, the way an
	// out-of-band writer that was cut off mid-write does.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	fragment := []byte(`{"seq":2,"kind":"job_started","job_id":"job_torn`)
	if err := os.WriteFile(path, append(raw, fragment...), 0o644); err != nil {
		t.Fatalf("write torn tail: %v", err)
	}

	repaired, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal over a torn tail: %v", err)
	}
	if repaired.Size != committed.Size {
		t.Fatalf("boundary size = %d over a torn tail, want the %d that decodes", repaired.Size, committed.Size)
	}
	if repaired.Generation <= committed.Generation {
		t.Fatalf("generation = %d after a torn tail, want it past the %d it was", repaired.Generation, committed.Generation)
	}
	onDisk, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat repaired journal: %v", err)
	}
	if onDisk.Size() != committed.Size {
		t.Fatalf("journal still holds %d bytes after the boundary read, want the torn fragment cut back to %d", onDisk.Size(), committed.Size)
	}

	// The repaired journal must still accept an append and still decode, which
	// is the whole reason the fragment had to go.
	if err := s.Append(incrementalTestEvent(1)); err != nil {
		t.Fatalf("append after torn-tail repair: %v", err)
	}
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents after torn-tail repair: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("journal holds %d events after the repair and an append, want 2", len(events))
	}
}

// TestCommittedJournalReportsClosedAndUnusable pins that the boundary read
// answers with the same typed failures every other operation reports, rather
// than a zero snapshot a caller could mistake for an empty journal.
func TestCommittedJournalReportsClosedAndUnusable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.CommittedJournal(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("CommittedJournal on a closed store = %v, want ErrStoreClosed", err)
	}
}

// TestCommittedJournalAppendAfterSameLengthRewriteMovesTheGeneration pins the
// change the append path used to overwrite: a rewrite that kept the length, then
// an append, with the fold cached across both.
//
// The rewrite alone is caught by the boundary read that follows it — mtime moved
// — so the interesting shape is the one where the store's own append happens
// first. Booking only the append's post-sync sample replaces the baseline the
// rewrite invalidated with bytes the append wrote, and the next boundary read
// then finds length and time agreeing. The generation would never move, and the
// fold built from the rewritten prefix would be extended with the append rather
// than rebuilt — serving the rows the rewrite removed for as long as the file
// sits still.
func TestCommittedJournalAppendAfterSameLengthRewriteMovesTheGeneration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	first, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	// Rewrite the SAME length, an hour ahead so no clock granularity hides it.
	rewritten := bytes.Replace(raw, []byte(`"job_0"`), []byte(`"job_9"`), 1)
	if len(rewritten) != len(raw) {
		t.Fatalf("fixture rewrite changed the length (%d -> %d)", len(raw), len(rewritten))
	}
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		t.Fatalf("rewrite journal: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("stamp rewritten journal: %v", err)
	}

	// The append lands after the rewrite and before any boundary read: this is
	// the case that has to notice the change itself.
	if err := s.Append(incrementalTestEvent(1)); err != nil {
		t.Fatalf("append after rewrite: %v", err)
	}
	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}
	if after.Generation <= first.Generation {
		t.Fatalf("generation = %d after a same-length rewrite and an append, want it past the %d it reported before: a fold would extend the rewritten prefix instead of rebuilding it", after.Generation, first.Generation)
	}
}

// TestCommittedJournalAppendAfterTruncationMovesTheGeneration is the other half
// of the same hole, with the shape that loses rows outright rather than serving
// stale ones: the journal is cut short, a short append lands, and the two
// together can be no longer than the baseline the previous boundary reported.
// An append that booked only its own post-sync sample would then look like an
// ordinary continuation at the same length, and a fold would keep rows for jobs
// the truncation deleted while omitting the one that just committed.
func TestCommittedJournalAppendAfterTruncationMovesTheGeneration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	for i := range 4 {
		if err := s.Append(incrementalTestEvent(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	full, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// Keep the first record whole and cut the rest.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	cut := bytes.IndexByte(raw, '\n')
	if cut < 0 {
		t.Fatalf("fixture journal holds no complete record")
	}
	if err := os.Truncate(path, int64(cut+1)); err != nil {
		t.Fatalf("truncate journal: %v", err)
	}
	// A short append over the cut, still under the pre-truncation length.
	if err := s.Append(incrementalTestEvent(9)); err != nil {
		t.Fatalf("append after truncation: %v", err)
	}

	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}
	if after.Generation <= full.Generation {
		t.Fatalf("generation = %d after a truncation and an append (size %d against the earlier %d), want it past %d: a fold would keep the deleted rows and miss the new one", after.Generation, after.Size, full.Size, full.Generation)
	}
}

// TestCommittedJournalBlankTailKeepsTheGeneration pins that a tail holding no
// record is removed, and that the journal stays readable afterwards.
//
// An out-of-band writer can leave a lone carriage return, or plain spaces or
// tabs, after the last newline. Nothing there can be decoded. Terminating such
// a tail does not make it blank to the reader: the cursor strips only the line
// terminator, so a run of spaces or tabs becomes a non-empty line that fails to
// decode, and every later load of the journal fails on it. Cutting the tail is
// what leaves the bytes a reader can decode.
//
// The assertions are the file itself and the reads that follow: the boundary
// cuts the tail back to the committed end, repeated reads of the unchanged file
// report one generation, an append after the repair is readable, and a fresh
// store opened on the same path reads the same records.
func TestCommittedJournalBlankTailKeepsTheGeneration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		tail string
	}{
		{name: "spaces", tail: "   "},
		{name: "tabs", tail: "\t\t"},
		{name: "mixed", tail: " \t \t"},
		{name: "carriage_return", tail: "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "jobs.jsonl")
			s, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = s.Close() }()
			if err := s.Append(incrementalTestEvent(0)); err != nil {
				t.Fatalf("append: %v", err)
			}
			committed, err := s.CommittedJournal(context.Background())
			if err != nil {
				t.Fatalf("CommittedJournal: %v", err)
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				t.Fatalf("open journal: %v", err)
			}
			if _, err := f.WriteString(tc.tail); err != nil {
				_ = f.Close()
				t.Fatalf("write blank tail: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("close journal: %v", err)
			}

			settled, err := s.CommittedJournal(context.Background())
			if err != nil {
				t.Fatalf("CommittedJournal over a blank tail: %v", err)
			}
			// The record before the tail must still decode. This is the claim the
			// terminating implementation broke: a whitespace tail it appended a
			// newline to stayed a non-empty line, and every read that followed failed
			// on it.
			before, err := s.LoadEvents()
			if err != nil {
				t.Fatalf("LoadEvents over a blank tail: %v", err)
			}
			if len(before) != 1 {
				t.Fatalf("LoadEvents returned %d events over a blank tail, want the 1 record before it", len(before))
			}
			// The tail is cut, not terminated: it carries no record, so the
			// boundary is the committed end again.
			if settled.Size != committed.Size {
				t.Fatalf("boundary size = %d over a %d-byte blank tail, want the committed %d", settled.Size, len(tc.tail), committed.Size)
			}
			onDisk, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat repaired journal: %v", err)
			}
			if onDisk.Size() != committed.Size {
				t.Fatalf("journal holds %d bytes after the boundary read, want the blank tail cut back to %d", onDisk.Size(), committed.Size)
			}
			// This read is the one that may legitimately see a change: the foreign
			// whitespace is content the store did not write.
			if settled.Generation <= committed.Generation {
				t.Fatalf("generation = %d after a foreign blank tail, want it past the %d it was", settled.Generation, committed.Generation)
			}
			// From here the file is unchanged, so nothing may move again.
			for i := range 3 {
				again, err := s.CommittedJournal(context.Background())
				if err != nil {
					t.Fatalf("repeated CommittedJournal %d: %v", i, err)
				}
				if again.Generation != settled.Generation {
					t.Fatalf("repeated read %d of an unchanged journal moved the generation %d -> %d: a fold discards its completed scan and retries", i, settled.Generation, again.Generation)
				}
				if again.Size != settled.Size {
					t.Fatalf("repeated read %d reported size %d, want the unchanged %d", i, again.Size, settled.Size)
				}
			}
			// The repaired journal still accepts an append, and the record that
			// append writes decodes — which is the cost of leaving the tail in
			// place rather than cutting it.
			if err := s.Append(incrementalTestEvent(1)); err != nil {
				t.Fatalf("append after the blank-tail repair: %v", err)
			}
			events, err := s.LoadEvents()
			if err != nil {
				t.Fatalf("LoadEvents after an append over a blank tail: %v", err)
			}
			if len(events) != 2 {
				t.Fatalf("LoadEvents returned %d events after the repair and an append, want the 2 complete records", len(events))
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer func() { _ = reopened.Close() }()
			reloaded, err := reopened.LoadEvents()
			if err != nil {
				t.Fatalf("LoadEvents after reopening: %v", err)
			}
			if len(reloaded) != 2 {
				t.Fatalf("reopened store returned %d events, want the 2 complete records", len(reloaded))
			}
		})
	}
}

// TestCommittedJournalLaggedNameKeepsTheAppendEnd pins the case where the two
// samples an append takes disagree: the HANDLE reports the length the append
// made durable and the NAME still reports the length from before it.
//
// That disagreement is ordinary attribute caching — the bytes are durable, the
// path's metadata has not caught up — and clearing the confirmed end on it is
// what hides a job. The next boundary read then trusts the name's shorter
// length as the boundary, and the fold stops short of a row the append already
// committed, until the attributes catch up. The end has to be kept until a name
// sample agrees; the file's own EOF is what still makes a real truncation
// visible.
func TestCommittedJournalLaggedNameKeepsTheAppendEnd(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	base := afero.NewMemMapFs()
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Freeze the name at the pre-append length while the handle keeps reporting
	// the truth, which is exactly the divergence staleStatFs models.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stale = snapshotInfo(frozen)
	before, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if err := s.Append(incrementalTestEvent(1)); err != nil {
		t.Fatalf("append under a lagging name: %v", err)
	}
	onDisk, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat durable journal: %v", err)
	}

	// The durable bytes are the boundary, not the name's stale length.
	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal under a lagging name: %v", err)
	}
	if after.Size != onDisk.Size() {
		t.Fatalf("boundary size = %d under a lagging name, want the durable %d: the job the append committed is hidden until the attributes catch up", after.Size, onDisk.Size())
	}
	if after.Size <= before.Size {
		t.Fatalf("boundary size = %d, want it past the %d it was before the append", after.Size, before.Size)
	}
	// The lag is real and must still be reported: a reader holding a cached
	// prefix cannot trust this filesystem to tell it what changed.
	if after.MetadataResolvesWrites {
		t.Fatalf("a lagging name reported MetadataResolvesWrites=true, want false")
	}
	// The records the append made durable are visible at that boundary.
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents under a lagging name: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("LoadEvents returned %d events, want the 2 durably appended", len(events))
	}

	// A real truncation is still a truncation even while a claim about the
	// append's end is outstanding: the file's own EOF decides.
	cutter, err := base.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open durable journal to truncate: %v", err)
	}
	if err := cutter.Truncate(before.Size); err != nil {
		_ = cutter.Close()
		t.Fatalf("truncate durable journal: %v", err)
	}
	if err := cutter.Close(); err != nil {
		t.Fatalf("close durable journal: %v", err)
	}
	truncated, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after truncation: %v", err)
	}
	if truncated.Size != before.Size {
		t.Fatalf("boundary size = %d after truncation, want the %d the file now holds", truncated.Size, before.Size)
	}
}

// handleCountingFs counts bytes read through the WRITER's held handle, which is
// the path a boundary read's tail search takes.
//
// countingFs beside it wraps Open, which is how a load reads; openFs opens the
// journal through OpenFile and every boundary read goes through that handle, so a
// search that reads the whole file would be invisible to the older fixture.
type handleCountingFs struct {
	afero.Fs
	bytesRead *int64
}

func (f handleCountingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return handleCountingFile{File: file, bytesRead: f.bytesRead}, nil
}

type handleCountingFile struct {
	afero.File
	bytesRead *int64
}

func (f handleCountingFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	*f.bytesRead += int64(n)
	return n, err
}

func (f handleCountingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(p, off)
	*f.bytesRead += int64(n)
	return n, err
}

// TestCommittedJournalTailSearchReadsBoundedly pins that repairing a torn tail
// does not read the journal to find one newline.
//
// The boundary read holds the store mutex, so a whole-file read here is the
// defect #811 exists to remove wearing a different hat: it is uncancellable, it
// blocks every append for its duration, and the cost is the session's entire
// history at exactly the moment the journal is largest. The search must instead
// run backward from EOF, so its work tracks the last line's length.
//
// This is stated as bytes read, not as time: a timing assertion would measure the
// host, and the property that matters is that the read does not grow with the
// file.
func TestCommittedJournalTailSearchReadsBoundedly(t *testing.T) {
	t.Parallel()
	var bytesRead int64
	fs := handleCountingFs{Fs: afero.NewMemMapFs(), bytesRead: &bytesRead}
	const path = "/jobs.jsonl"
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	seed := make([]Event, 0, 2000)
	for i := range 2000 {
		seed = append(seed, incrementalTestEvent(i))
	}
	if err := s.AppendBatch(seed); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	if _, err := s.CommittedJournal(context.Background()); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	info, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	logSize := info.Size()
	if logSize < 8*trailingLineChunk {
		t.Fatalf("fixture journal is only %d bytes; too small to tell a bounded tail search from a full read", logSize)
	}

	// Leave a torn record behind the committed prefix, the way a cut-off writer
	// does. The tail search is what the next boundary read runs.
	file, err := fs.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := file.WriteString(`{"seq":99999,"kind":"job_started","job_id":"job_torn`); err != nil {
		_ = file.Close()
		t.Fatalf("write torn tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	bytesRead = 0
	if _, err := s.CommittedJournal(context.Background()); err != nil {
		t.Fatalf("CommittedJournal over a torn tail: %v", err)
	}
	// The search reads bounded windows from the end, and the repair then reads the
	// tail it found. Both are fixed-size: the total must be a small multiple of the
	// chunk, not a fraction of the journal. Stating it against the chunk is what
	// makes the property "independent of file size" rather than "smaller than this
	// fixture".
	if bytesRead > 2*trailingLineChunk {
		t.Fatalf("repairing a torn tail read %d bytes of a %d-byte journal: the tail search reads with the file's size instead of walking back from it", bytesRead, logSize)
	}
}

// TestCommittedJournalTailSearchHonoursCancellation pins the other half: an
// abandoned read must stop searching, and the store must be usable afterwards.
//
// The search runs under the store mutex, so a search that ignores its context
// holds up every append until it finishes on its own. Abandoning a refresh is
// the ordinary case — a page closed, a subscription dropped — and the writer must
// not pay for it.
func TestCommittedJournalTailSearchHonoursCancellation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	// A torn tail makes the next boundary read run the search at all.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := file.WriteString(`{"seq":2,"kind":"job_started","job_id":"job_torn`); err != nil {
		_ = file.Close()
		t.Fatalf("write torn tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	// A context that is already done must be observed before any read happens, so
	// the call answers without doing the work at all.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CommittedJournal(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommittedJournal with a canceled context = %v, want context.Canceled", err)
	}

	// The canceled read must leave the store exactly as it found it. That is what
	// makes cancellation safe here rather than merely fast: the handle is put back
	// at EOF, nothing was truncated, and the baseline was not spent.
	//
	// The append comes AFTER a real repair, because an append never repairs a tail
	// itself — that is deliberately the boundary read's job (see
	// recoverTrailingLineForBoundaryLocked), and doing it here would be testing a
	// behaviour this change does not add. What this asserts is that the abandoned
	// call did not poison the state a later one depends on.
	repaired, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after a canceled boundary read: %v", err)
	}
	if repaired.Size == 0 {
		t.Fatalf("boundary size = 0 after the repair, want the record that decodes")
	}
	if err := s.Append(incrementalTestEvent(1)); err != nil {
		t.Fatalf("append after a canceled boundary read: %v", err)
	}
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents after a canceled boundary read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("journal holds %d events after a canceled boundary read, a repair and an append, want the 2 complete records", len(events))
	}
}

// readSpan is one read a recording handle served: the offset it began at and how
// many bytes it returned.
type readSpan struct {
	offset int64
	length int64
}

// recordingFs records every read served through the handle it hands out, with
// the offset each read began at.
//
// The tail search's property is about how it READS, not about how many bytes
// pass through: a search that re-reads an ever-growing window spends its time
// and its allocations on bytes it has already seen. Recording each read's
// position is what tells the two apart.
type recordingFs struct {
	afero.Fs
	mu    sync.Mutex
	spans []readSpan
}

func (f *recordingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &recordingFile{File: file, fs: f}, nil
}

func (f *recordingFs) record(offset, length int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, readSpan{offset: offset, length: length})
}

func (f *recordingFs) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = nil
}

func (f *recordingFs) recorded() []readSpan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readSpan(nil), f.spans...)
}

type recordingFile struct {
	afero.File
	fs *recordingFs
}

func (f *recordingFile) Read(p []byte) (int, error) {
	at, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	n, err := f.File.Read(p)
	if n > 0 {
		f.fs.record(at, int64(n))
	}
	return n, err
}

// TestCommittedJournalTailSearchReadsEachBlockOnce pins that the backward tail
// search costs linear work in the tail's length.
//
// The boundary read holds the store mutex, so the cost of finding the last
// newline is paid by every append waiting behind it. A search that re-reads an
// ever-growing window ending at EOF spends that cost on quadratic I/O and
// repeated allocations, and it does so exactly where the journal is largest.
// The properties that rule it out are positional and quantitative: each block
// of the walk is read at its own offset, never a second time, and the total
// bytes read stay proportional to the tail rather than to its square.
//
// A tail spanning many chunks is what makes the difference visible. The
// assembled line is checked against the bytes on disk, so the test also proves
// the search found the real line and not a truncated piece of it.
func TestCommittedJournalTailSearchReadsEachBlockOnce(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	fs := &recordingFs{Fs: afero.NewMemMapFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.CommittedJournal(context.Background()); err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// A torn record several chunks long: the search must walk back through all of
	// it, which is where a growing-window search re-reads.
	const chunks = 12
	tail := bytes.Repeat([]byte("x"), chunks*trailingLineChunk+17)
	file, err := fs.Fs.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := file.Write(append([]byte(`{"seq":2,"kind":"job_started"`), tail...)); err != nil {
		_ = file.Close()
		t.Fatalf("write torn tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
	onDisk, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}

	fs.reset()
	size, line, err := s.trailingLineFromHandleLocked(context.Background(), onDisk.Size())
	if err != nil {
		t.Fatalf("trailingLineFromHandleLocked: %v", err)
	}
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		t.Fatalf("restore handle to EOF: %v", err)
	}
	raw, err := afero.ReadFile(fs.Fs, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if got, want := line, raw[size:]; !bytes.Equal(got, want) {
		t.Fatalf("trailing line = %d bytes, want the %d bytes after offset %d", len(got), len(want), size)
	}
	if bytes.IndexByte(line, '\n') >= 0 {
		t.Fatalf("trailing line holds a newline, so the search returned more than the final line")
	}

	// Every block the walk inspects has its own offset. A growing-window search
	// re-reads the same offsets as the window expands, which is the quadratic cost
	// this replaces.
	spans := fs.recorded()
	if len(spans) < 2 {
		t.Fatalf("tail search issued %d reads over a %d-byte tail, want a backward walk plus the line", len(spans), len(line))
	}
	first := map[int64]bool{}
	for i, span := range spans {
		if span.length > trailingLineChunk {
			// The one forward read of the assembled line. There may be exactly one
			// of these.
			continue
		}
		if first[span.offset] {
			t.Fatalf("read %d starts at offset %d a second time: the walk re-reads a block it already inspected", i, span.offset)
		}
		first[span.offset] = true
	}
	var total int64
	for _, span := range spans {
		total += span.length
	}
	// Linear work: a block walk over the tail, then the line read once. A
	// growing-window search over this tail reads about half again as many bytes as
	// the sum of its windows, which is quadratic in the number of chunks.
	if bound := 2*int64(len(line)) + int64(trailingLineChunk); total > bound {
		t.Fatalf("tail search read %d bytes for a %d-byte tail, want at most %d: the search reads a growing window instead of walking blocks", total, len(line), bound)
	}
}

// TestCommittedJournalRejectsAnOversizedTailWithoutAllocatingIt pins the other
// half of the same search: a tail larger than the journal's per-line limit is
// not a record any reader would accept, so the search must stop before holding
// it.
//
// The observable requirements are that the search refuses the tail as a tail
// rather than as corruption, that its work stops at the limit instead of
// growing with the file, and that every byte of the oversized tail is left on
// disk as evidence. Reading the whole tail into memory to discover it does not
// decode would allocate more than the journal accepts from one corrupt tail,
// under the store mutex.
func TestCommittedJournalRejectsAnOversizedTailWithoutAllocatingIt(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	fs := &recordingFs{Fs: afero.NewMemMapFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	committed, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// One unterminated line past the limit. It is written through the fs rather
	// than allocated twice: the fixture streams it in blocks.
	file, err := fs.Fs.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	block := bytes.Repeat([]byte("x"), 1<<20)
	oversized := int64(DefaultMaxLineBytes) + 1
	for written := int64(0); written < oversized; written += int64(len(block)) {
		chunk := min(int64(len(block)), oversized-written)
		if _, err := file.Write(block[:chunk]); err != nil {
			_ = file.Close()
			t.Fatalf("write oversized tail: %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
	before, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if before.Size() != committed.Size+oversized {
		t.Fatalf("fixture journal is %d bytes, want the committed %d plus the %d-byte tail", before.Size(), committed.Size, oversized)
	}

	fs.reset()
	if _, _, err := s.trailingLineFromHandleLocked(context.Background(), before.Size()); !errors.Is(err, errTrailingLineTooLong) {
		t.Fatalf("trailingLineFromHandleLocked over an oversized tail = %v, want errTrailingLineTooLong", err)
	}
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		t.Fatalf("restore handle to EOF: %v", err)
	}
	var read int64
	for _, span := range fs.recorded() {
		read += span.length
	}
	if limit := int64(DefaultMaxLineBytes) + 2*trailingLineChunk; read > limit {
		t.Fatalf("the tail search read %d bytes of a %d-byte tail, want at most %d: it must stop at the line limit instead of walking the whole tail", read, oversized, limit)
	}
	// Through the boundary, the refusal is an unsettled conclusion, not an error:
	// the read still answers, and the writer's baseline is left unbooked so the
	// next append inspects the tail rather than joining onto it.
	if _, err := s.CommittedJournal(context.Background()); err != nil {
		t.Fatalf("CommittedJournal over an oversized tail = %v, want a clean answer", err)
	}
	if s.sampled {
		t.Fatalf("sampled = true after an oversized tail was left in place; the writer's baseline must stay unverified")
	}
	after, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat journal after the boundary read: %v", err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("journal holds %d bytes after the rejected tail, want the %d it had: an unreadable tail is evidence and must not be cut", after.Size(), before.Size())
	}
}

// frozenStatFs serves one frozen metadata snapshot from every stat: the name's
// and the held handle's alike, which is what a mount whose attributes lag its
// data reports through both. Reads are never affected — the bytes it hands back
// are the file's real ones.
//
// Freezing both is what keeps the store's identity question answerable: with no
// Sys on either side, sameJournalFile falls back to length and time, which this
// fixture holds equal.
type frozenStatFs struct {
	afero.Fs
	frozen atomic.Pointer[snapshotFileInfo]
}

func (f *frozenStatFs) arm(info os.FileInfo) {
	snapshot := snapshotInfo(info).(snapshotFileInfo)
	f.frozen.Store(&snapshot)
}

func (f *frozenStatFs) Stat(name string) (os.FileInfo, error) {
	info, err := f.Fs.Stat(name)
	if err != nil {
		return nil, err
	}
	if frozen := f.frozen.Load(); frozen != nil {
		return *frozen, nil
	}
	return info, nil
}

func (f *frozenStatFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &frozenStatFile{File: file, fs: f}, nil
}

type frozenStatFile struct {
	afero.File
	fs *frozenStatFs
}

func (f *frozenStatFile) Stat() (os.FileInfo, error) {
	if frozen := f.fs.frozen.Load(); frozen != nil {
		return *frozen, nil
	}
	return f.File.Stat()
}

// TestCommittedJournalTruncatesOnlyTheFragmentWhenStatsLagEOF pins that the tail
// recovery measures the file's real end instead of trusting a reported length.
//
// The shape: a batch was appended, the path's attributes still report the
// length from before it, and a truncation then cut the batch mid-record. A
// reported length that ends on an earlier newline makes the region after it
// look blank when the search starts from that number — and a blank tail is
// settled by writing a terminator, at the physical end, which turns the
// fragment into a malformed line the next reader fails on. Measuring the end
// once is what puts the repair on the fragment.
//
// The assertions are the file's length, the boundary, and the records that
// decode afterwards: recovery must cut exactly the fragment and keep every
// complete record before it.
func TestCommittedJournalTruncatesOnlyTheFragmentWhenStatsLagEOF(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	base := afero.NewOsFs()
	fs := &frozenStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	// The length before the batch, and it ends on a newline: exactly what the
	// handle will keep reporting.
	preBatch, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if err := s.AppendBatch([]Event{incrementalTestEvent(1), incrementalTestEvent(2)}); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	raw, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(raw) <= int(preBatch.Size()) {
		t.Fatalf("fixture journal is %d bytes with a pre-batch length of %d", len(raw), preBatch.Size())
	}
	// Cut the batch mid-record, the way an interrupted writer leaves it.
	fragmentStart := int(preBatch.Size())
	firstRecordEnd := bytes.IndexByte(raw[fragmentStart:], '\n')
	if firstRecordEnd <= 0 {
		t.Fatalf("fixture batch holds no complete record")
	}
	cut := fragmentStart + firstRecordEnd/2
	if cut <= fragmentStart {
		t.Fatalf("fixture cut at %d is not inside the first batch record", cut)
	}
	if err := os.Truncate(path, int64(cut)); err != nil {
		t.Fatalf("truncate journal: %v", err)
	}
	fs.arm(preBatch)

	repaired, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal over a truncated batch: %v", err)
	}
	if repaired.Size != preBatch.Size() {
		t.Fatalf("boundary size = %d after a truncated batch, want the %d of complete records", repaired.Size, preBatch.Size())
	}
	onDisk, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat repaired journal: %v", err)
	}
	if onDisk.Size() != preBatch.Size() {
		t.Fatalf("journal holds %d bytes after the repair, want the fragment cut back to the %d of complete records", onDisk.Size(), preBatch.Size())
	}
	// The complete record before the batch is still there and still decodes.
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents after the repair: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("LoadEvents returned %d events after the repair, want the 1 complete record before the fragment", len(events))
	}
}

// TestCommittedJournalStaleLargerStatKeepsTheGenerationStable pins the direction
// of attribute lag the shorter-report case does not reach.
//
// The shape: the path's attributes are frozen at the length of an earlier
// write, so the append that follows records that they lag it, and a truncation
// then removes everything after the FIRST record. The file's real end is below
// the stale report, so from the second read on the reported length is the
// LARGER number. A reconciliation that only asks the file when the report is
// shorter never asks again: the boundary answers with a length past EOF, the
// generation moves on every read of the same bytes, and a fold that compares
// generations can never publish.
//
// The assertions are the boundary's size and its stability: repeated reads of
// the surviving prefix must report that prefix and one generation.
func TestCommittedJournalStaleLargerStatKeepsTheGenerationStable(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	base := afero.NewMemMapFs()
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	if err := s.AppendBatch([]Event{incrementalTestEvent(0), incrementalTestEvent(1)}); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	preBatch, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// afero's FileInfo is a live view of the file, so the numbers have to be
	// copied out before the batch moves them.
	fs.stale = snapshotInfo(preBatch)
	staleSize := fs.stale.Size()

	if err := s.AppendBatch([]Event{incrementalTestEvent(2), incrementalTestEvent(3)}); err != nil {
		t.Fatalf("append batch under a lagging name: %v", err)
	}
	confirmed := s.laggingAppendEnd
	if confirmed <= staleSize {
		t.Fatalf("fixture did not establish a lagging append end: %d against the stale %d", confirmed, staleSize)
	}
	raw, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	firstRecordEnd := bytes.IndexByte(raw, '\n')
	if firstRecordEnd < 0 {
		t.Fatalf("fixture journal holds no complete record")
	}
	// Keep the first record only. The survivor is a complete prefix and it is
	// SHORTER than the stale report, which is the direction this test covers.
	survivingEnd := int64(firstRecordEnd + 1)
	if survivingEnd >= staleSize {
		t.Fatalf("fixture survivor ends at %d, at or past the stale report %d: the reported length would not be the larger number", survivingEnd, staleSize)
	}
	if survivingEnd >= confirmed {
		t.Fatalf("fixture survivor ends at %d, not below the confirmed %d", survivingEnd, confirmed)
	}
	cutter, err := base.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open journal to truncate: %v", err)
	}
	if err := cutter.Truncate(survivingEnd); err != nil {
		_ = cutter.Close()
		t.Fatalf("truncate journal: %v", err)
	}
	if err := cutter.Close(); err != nil {
		t.Fatalf("close truncated journal: %v", err)
	}

	first, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after truncation: %v", err)
	}
	if first.Size != survivingEnd {
		t.Fatalf("boundary size = %d after truncation, want the %d that survived", first.Size, survivingEnd)
	}
	// Repeated reads of the same bytes must agree. This is the assertion the
	// one-directional check fails: with the stale report larger than the file, each
	// read rediscovered the same EOF, treated it as a fresh change, and moved the
	// generation.
	for i := range 3 {
		again, err := s.CommittedJournal(context.Background())
		if err != nil {
			t.Fatalf("repeated CommittedJournal %d: %v", i, err)
		}
		if again.Generation != first.Generation {
			t.Fatalf("repeated read %d moved the generation %d -> %d over the same surviving prefix: every scan is discarded and retried", i, first.Generation, again.Generation)
		}
		if again.Size != survivingEnd {
			t.Fatalf("repeated read %d reported size %d, want the surviving %d", i, again.Size, survivingEnd)
		}
	}

	// The same reconciliation has to survive an EMPTIED file, where the measured
	// end is zero: a claim keyed on a non-zero end cannot see it, so the stale
	// report would be answered as the boundary and the empty file would look like a
	// journal of that length on every read.
	emptier, err := base.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open journal to empty it: %v", err)
	}
	if err := emptier.Truncate(0); err != nil {
		_ = emptier.Close()
		t.Fatalf("empty the journal: %v", err)
	}
	if err := emptier.Close(); err != nil {
		t.Fatalf("close emptied journal: %v", err)
	}
	emptied, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal over an emptied journal: %v", err)
	}
	if emptied.Size != 0 {
		t.Fatalf("boundary size = %d over an emptied journal, want 0: a claim keyed on a nonzero end misses it", emptied.Size)
	}
	for i := range 3 {
		again, err := s.CommittedJournal(context.Background())
		if err != nil {
			t.Fatalf("repeated CommittedJournal %d over an emptied journal: %v", i, err)
		}
		if again.Generation != emptied.Generation {
			t.Fatalf("repeated read %d of an emptied journal moved the generation %d -> %d", i, emptied.Generation, again.Generation)
		}
		if again.Size != 0 {
			t.Fatalf("repeated read %d of an emptied journal reported size %d, want 0", i, again.Size)
		}
	}
}

// TestCommittedJournalAbsenceMovesTheGenerationOnce pins that the journal's
// absence is part of the reader's baseline.
//
// A journal moved aside and put back can return with the same inode, length and
// modification time, and then nothing in the file distinguishes it from one
// that never left. The absence is the evidence, and it exists only while the
// journal is missing: the reader has to record it then, and record it once, so
// that the disappearance is one change, repeated reads of the missing journal
// are none, and the return is one more.
//
// The assertions are generation transitions, which is what a fold keys on: the
// generation advances when the journal disappears, holds while it is gone, and
// advances again when it comes back.
func TestCommittedJournalAbsenceMovesTheGenerationOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	live, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if live.Size == 0 {
		t.Fatalf("boundary size = 0 over a journal with a record")
	}
	aside := filepath.Join(dir, "jobs.jsonl.aside")
	if err := os.Rename(path, aside); err != nil {
		t.Fatalf("move journal aside: %v", err)
	}

	gone, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal with the journal absent: %v", err)
	}
	if gone.Size != 0 {
		t.Fatalf("boundary size = %d with the journal absent, want 0", gone.Size)
	}
	if gone.Generation <= live.Generation {
		t.Fatalf("generation = %d when the journal went missing, want it past the %d it was: the return would look like an unchanged file", gone.Generation, live.Generation)
	}
	// A second read while it is still gone is the same absence, not a new one.
	stillGone, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("repeated CommittedJournal with the journal absent: %v", err)
	}
	if stillGone.Generation != gone.Generation {
		t.Fatalf("repeated read with the journal absent moved the generation %d -> %d: an unchanged absent journal must not invalidate a fold on every poll", gone.Generation, stillGone.Generation)
	}

	// Put the SAME file back: same inode, same length, same modification time.
	if err := os.Rename(aside, path); err != nil {
		t.Fatalf("restore journal: %v", err)
	}
	restored, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after restoring the journal: %v", err)
	}
	if restored.Generation <= gone.Generation {
		t.Fatalf("generation = %d after the journal came back, want it past the %d it was while absent: the return looks like an unchanged file", restored.Generation, gone.Generation)
	}
	if restored.Size != live.Size {
		t.Fatalf("boundary size = %d after the journal came back, want the %d it held", restored.Size, live.Size)
	}
	// The return is one change, and reads of the same restored file are stable.
	again, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("repeated CommittedJournal after the return: %v", err)
	}
	if again.Generation != restored.Generation {
		t.Fatalf("repeated read after the return moved the generation %d -> %d", restored.Generation, again.Generation)
	}
}

// unreachableHandleFs makes the WRITER's held handle fail its stat while the
// named journal stays perfectly readable.
//
// It models a platform or a workload where the descriptor the store appends
// through cannot be interrogated — fstat failing on a reclaimed handle, a FUSE
// mount that answers by path only — while the file itself is fine. The bytes are
// readable, so a reader has everything it needs to describe the content.
type unreachableHandleFs struct {
	afero.Fs
	handleStatErr error
	// failing turns the handle's stat failure on. openFs stats the handle to
	// establish its baseline, so the fixture has to open successfully and go blind
	// afterwards — which is also the realistic order: a handle that becomes
	// uninterpretable later, not one that never worked.
	failing atomic.Bool
}

func (f *unreachableHandleFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &unreachableHandleFile{File: file, fs: f, statErr: f.handleStatErr}, nil
}

type unreachableHandleFile struct {
	afero.File
	fs      *unreachableHandleFs
	statErr error
}

func (f *unreachableHandleFile) Stat() (os.FileInfo, error) {
	if f.fs.failing.Load() {
		return nil, f.statErr
	}
	return f.File.Stat()
}

// TestCommittedJournalStableWhenTheWriterHandleCannotBeSampled pins that a
// writer-side blind spot does not look like a changing journal.
//
// Whether the handle can be stat'ed is a question about the WRITER. Whether the
// journal's bytes changed is a question about the NAME, and the reader answers it
// from the name alone. Conflating them makes every sample of an unchanged file
// report a new generation, and a fold compares the generation before publishing —
// so each scan it completes is discarded and retried, and the read spins until
// the request is canceled. The journal is fine; only the writer is blind.
func TestCommittedJournalStableWhenTheWriterHandleCannotBeSampled(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	fs := &unreachableHandleFs{Fs: afero.NewMemMapFs(), handleStatErr: os.ErrInvalid}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()
	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("append: %v", err)
	}
	// From here the writer's handle cannot be interrogated; the name can.
	fs.failing.Store(true)

	first, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// The record is there and the boundary covers it, even though nothing could
	// confirm it through the handle.
	if first.Size == 0 {
		t.Fatalf("boundary size = 0 with an unsamplable handle, want the durable record's length")
	}
	// Repeated reads of the same bytes must agree. This is the assertion that the
	// spin fails: with the handle's blindness folded into the change test, every
	// read bumps the generation.
	for i := range 3 {
		again, err := s.CommittedJournal(context.Background())
		if err != nil {
			t.Fatalf("repeated CommittedJournal %d: %v", i, err)
		}
		if again.Generation != first.Generation {
			t.Fatalf("repeated read %d moved the generation %d -> %d with an unsamplable handle: a fold discards each completed scan and retries until it is canceled", i, first.Generation, again.Generation)
		}
		if again.Size != first.Size {
			t.Fatalf("repeated read %d reported size %d, want the unchanged %d", i, again.Size, first.Size)
		}
	}
	// The writer's own baseline is still unverified, which is the honest answer for
	// the append path: the next append must not treat an unsampled handle as
	// agreement.
	if s.sampled {
		t.Fatalf("sampled = true with an unsamplable handle; the writer's baseline must stay unverified")
	}
	events, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("LoadEvents returned %d events, want the 1 durable record", len(events))
	}
}

// preStatBlindFs makes the WRITER's handle report its post-write stat but fail
// its pre-write one, which is the shape an append sees when the descriptor is
// briefly uninterpretable.
//
// The store's append seeks to EOF to learn where its bytes begin, and that seek
// is evidence about the file's length. A fixture that hides only the stat leaves
// that evidence standing, which is exactly the case the append path must not
// ignore: the length the seek found can contradict the recorded baseline even
// when no stat confirms it.
type preStatBlindFs struct {
	afero.Fs
	// blind is set by the test after the store is open.
	blind atomic.Bool
	// stats counts handle samples since blind was turned on.
	stats atomic.Int64
	// stalePre, when set, is what the FIRST handle sample after blinding reports
	// instead of failing: metadata from before the write the test made. That is
	// the attribute-caching shape, and it is the harder one — the sample EXISTS
	// and agrees with the recorded baseline, so only the append's own EOF seek
	// contradicts it.
	stalePre os.FileInfo
}

func (f *preStatBlindFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &preStatBlindFile{File: file, fs: f}, nil
}

type preStatBlindFile struct {
	afero.File
	fs *preStatBlindFs
}

func (f *preStatBlindFile) Stat() (os.FileInfo, error) {
	if f.fs.blind.Load() {
		// The FIRST handle stat after blinding is the append's pre-write sample. It
		// either fails or reports the stale attributes the test supplied; later ones
		// report the truth.
		if f.fs.stats.Add(1) == 1 {
			if f.fs.stalePre != nil {
				return f.fs.stalePre, nil
			}
			return nil, os.ErrInvalid
		}
	}
	return f.File.Stat()
}

// TestCommittedJournalAppendAfterTruncationWithBlindPreStatMovesTheGeneration
// pins that the append consults the evidence IT produced.
//
// A truncation followed by an append has two witnesses: the pre-write stat, and
// the append's own EOF seek. When the stat fails, only the seek is left — and the
// seek is the better evidence anyway, since it reports where the file actually
// ends rather than an attribute a caching layer may have written late. Ignoring
// it lets the append book a post-sync baseline over a length it just replaced, so
// the next boundary read finds everything agreeing and keeps the generation. The
// fold then keeps rows the truncation deleted and omits the job that committed.
func TestCommittedJournalAppendAfterTruncationWithBlindPreStatMovesTheGeneration(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	fs := &preStatBlindFs{Fs: afero.NewMemMapFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	for i := range 4 {
		if err := s.Append(incrementalTestEvent(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	full, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// Cut the journal back to its first record.
	raw, err := afero.ReadFile(fs.Fs, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	cut := bytes.IndexByte(raw, '\n')
	if cut < 0 {
		t.Fatalf("fixture journal holds no complete record")
	}
	cutter, err := fs.Fs.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open journal to truncate: %v", err)
	}
	if err := cutter.Truncate(int64(cut + 1)); err != nil {
		_ = cutter.Close()
		t.Fatalf("truncate journal: %v", err)
	}
	if err := cutter.Close(); err != nil {
		t.Fatalf("close truncated journal: %v", err)
	}

	// From here the append's pre-write handle stat fails.
	fs.blind.Store(true)
	if err := s.Append(incrementalTestEvent(9)); err != nil {
		t.Fatalf("append after truncation: %v", err)
	}
	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}
	if after.Generation <= full.Generation {
		t.Fatalf("generation = %d after a truncation and an append whose pre-write stat failed (size %d against the earlier %d), want it past %d: the fold keeps the deleted rows and misses the new job", after.Generation, after.Size, full.Size, full.Generation)
	}
}

// TestCommittedJournalPartialBatchTruncationKeepsSurvivingRows pins that a
// boundary read reports the file's real length while the path's attributes lag.
//
// The shape: an append batch is confirmed, the path still reports the length from
// BEFORE the batch, and a truncation then removes only part of it. The real EOF
// sits strictly between the stale attribute and the confirmed end. Clearing the
// lag marker and trusting the stale size puts the boundary back before the batch,
// so the records that survived the truncation vanish from the fold — and the ones
// the truncation removed are still inside the reported prefix.
//
// The measured EOF is the only witness that gets this right, which is why it must
// survive the repair path's own re-stat.
func TestCommittedJournalPartialBatchTruncationKeepsSurvivingRows(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	base := afero.NewMemMapFs()
	fs := &staleStatFs{Fs: base}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	if err := s.Append(incrementalTestEvent(0)); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Freeze the name at the length of the first record: from here its attributes
	// are a fixed lie, which is what makes the later EOF the only truth.
	frozen, err := base.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	staleSize := frozen.Size()
	fs.stale = snapshotInfo(frozen)

	batch := []Event{incrementalTestEvent(1), incrementalTestEvent(2)}
	if err := s.AppendBatch(batch); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	confirmed := s.laggingAppendEnd
	if confirmed <= staleSize {
		t.Fatalf("fixture did not establish a lagging append end: %d against the stale %d", confirmed, staleSize)
	}

	// Cut the batch in half: the survivor's boundary lands between the stale size
	// and the confirmed end.
	raw, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(raw) <= int(staleSize) || int64(len(raw)) != confirmed {
		t.Fatalf("fixture journal is %d bytes with a stale size of %d and a confirmed end of %d", len(raw), staleSize, confirmed)
	}
	survivor := raw[staleSize:]
	cut := bytes.IndexByte(survivor, '\n')
	if cut < 0 {
		t.Fatalf("fixture batch holds no complete record")
	}
	survivingEnd := staleSize + int64(cut) + 1
	if survivingEnd <= staleSize || survivingEnd >= confirmed {
		t.Fatalf("fixture cut at %d is not between the stale %d and the confirmed %d", survivingEnd, staleSize, confirmed)
	}
	cutter, err := base.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open journal to truncate: %v", err)
	}
	if err := cutter.Truncate(survivingEnd); err != nil {
		_ = cutter.Close()
		t.Fatalf("truncate journal: %v", err)
	}
	if err := cutter.Close(); err != nil {
		t.Fatalf("close truncated journal: %v", err)
	}

	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after a partial batch truncation: %v", err)
	}
	if after.Size != survivingEnd {
		t.Fatalf("boundary size = %d after a partial batch truncation, want the %d that survived: the rows past it are already gone and the ones before it are hidden", after.Size, survivingEnd)
	}
	// And it keeps answering the same way while the attributes stay stale, so a
	// later validation pass does not restore the pre-batch length.
	again, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("repeated CommittedJournal: %v", err)
	}
	if again.Size != survivingEnd {
		t.Fatalf("repeated boundary size = %d, want the unchanged %d: validation would reject every scan of this journal", again.Size, survivingEnd)
	}
}

// TestCommittedJournalAppendAfterTruncationWithStalePreStatMovesTheGeneration is
// the harder half of the same finding.
//
// Here the pre-write sample EXISTS and agrees with the recorded baseline, because
// the filesystem reported attributes from before the truncation. Only the
// append's own EOF seek contradicts it: the file it appended to was shorter than
// the store remembered. Booking the post-sync sample as the new baseline without
// consulting that seek makes the next boundary read find everything agreeing, so
// the generation never moves and the fold keeps rows the truncation deleted while
// omitting the job that just committed.
//
// The append produced two facts about the file's length. This asserts the one it
// can trust is consulted even when the other lies.
func TestCommittedJournalAppendAfterTruncationWithStalePreStatMovesTheGeneration(t *testing.T) {
	t.Parallel()
	const path = "/jobs.jsonl"
	fs := &preStatBlindFs{Fs: afero.NewMemMapFs()}
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("openFs: %v", err)
	}
	s.disableSync = true
	defer func() { _ = s.Close() }()

	for i := range 4 {
		if err := s.Append(incrementalTestEvent(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	full, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// The attributes to report as "still current" after the truncation, captured
	// before it: a caching layer that has not learned about the cut answers with
	// exactly this.
	frozen, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	fs.stalePre = snapshotInfo(frozen)

	raw, err := afero.ReadFile(fs.Fs, path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	cut := bytes.IndexByte(raw, '\n')
	if cut < 0 {
		t.Fatalf("fixture journal holds no complete record")
	}
	cutter, err := fs.Fs.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open journal to truncate: %v", err)
	}
	if err := cutter.Truncate(int64(cut + 1)); err != nil {
		_ = cutter.Close()
		t.Fatalf("truncate journal: %v", err)
	}
	if err := cutter.Close(); err != nil {
		t.Fatalf("close truncated journal: %v", err)
	}

	fs.blind.Store(true)
	if err := s.Append(incrementalTestEvent(9)); err != nil {
		t.Fatalf("append after truncation: %v", err)
	}
	after, err := s.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}
	if after.Generation <= full.Generation {
		t.Fatalf("generation = %d after a truncation and an append whose pre-write sample reported stale attributes (size %d against the earlier %d), want it past %d: the fold keeps the deleted rows and misses the new job", after.Generation, after.Size, full.Size, full.Generation)
	}
}
