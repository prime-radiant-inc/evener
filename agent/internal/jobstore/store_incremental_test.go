package jobstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/agent/provenance"
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
	if !reflect.DeepEqual(FoldDelegates(got), FoldDelegates(want)) {
		t.Fatalf("%s: FoldDelegates differs between incremental and full reread", when)
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
	delegateID := fmt.Sprintf("dlg_%d", i%3)
	switch i % 6 {
	case 0:
		started := ts
		resumable := i%2 == 0
		return Event{
			Kind: EventJobStarted, TS: ts, JobID: jobID, Type: JobDelegate,
			Task: strings.Repeat("t", i%17), OwnerSessionID: "ses_owner",
			VisibleToSession: "ses_owner", DelegateID: delegateID, StartedAt: &started,
			OutputPath: "/tmp/out", TranscriptRef: "local:ses_child",
			DelegateRestore: &DelegateRestoreDescriptor{
				Version: 1, ChildSessionID: "ses_child", TranscriptRef: "local:ses_child",
				Task: "task", FrozenToolNames: []string{"read_file", "bash"},
				FrozenSkillBodies: []string{strings.Repeat("b", i%11)},
				ResultSchema:      map[string]any{"type": "object", "n": float64(i)},
				Sandbox:           &SandboxSnapshot{Mode: "workspace", Network: &resumable, DenylistAdd: []string{"x"}},
				Provenance:        &provenance.Causal{Chain: []provenance.Entry{{Kind: "watch", JobID: jobID}}},
			},
			Provenance: &provenance.Causal{WatchKeys: []provenance.WatchKey{{WatchID: "w1", WatchGeneration: "g1"}}},
		}
	case 1:
		resumable := i%3 == 0
		return Event{
			Kind: EventJobSessionAssigned, TS: ts, JobID: jobID,
			TranscriptRef: "local:ses_child", Resumable: &resumable, NotResumableWhy: "why",
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
			Kind: EventDelegateCreated, TS: ts, DelegateID: delegateID,
			Delegate: &DelegateEvent{
				ChildSessionID: "ses_child", TranscriptRef: "local:ses_child",
				OwnerSessionID: "ses_owner", VisibleSessionID: "ses_owner",
				AgentType: "worker", Generation: fmt.Sprintf("g%d", i), Resumable: true,
			},
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
				DeliveryID: fmt.Sprintf("d%d", i), UpdateSeq: uint64(i), Message: "m",
				CreatedAt: ts, UpdatedAt: ts,
				Provenance: &provenance.Causal{Chain: []provenance.Entry{{Kind: "watch", WatchID: "w1"}}},
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
			for step := 0; step < 30; step++ {
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
	// A distinct mtime is what tells the store the bytes are not its own; the
	// filesystem supplies it on write, this only makes the write happen.
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("rewrite log: %v", err)
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
// mutates a loaded event or record — agent tests reach into
// JobRecord.DelegateRestore to plant bogus durable state — must not change what
// the next load reports.
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
		if r.DelegateRestore != nil {
			r.DelegateRestore.WorkingDir = "/mutated"
			r.DelegateRestore.FrozenToolNames = []string{"mutated"}
		}
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
	if e.Resumable != nil {
		*e.Resumable = !*e.Resumable
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
	if d := e.DelegateRestore; d != nil {
		d.WorkingDir = "/mutated"
		if len(d.FrozenToolNames) > 0 {
			d.FrozenToolNames[0] = "mutated"
		}
		if len(d.FrozenSkillBodies) > 0 {
			d.FrozenSkillBodies[0] = "mutated"
		}
		if m, ok := d.ResultSchema.(map[string]any); ok {
			m["mutated"] = true
		}
		mutateCausal(d.Provenance)
		if sb := d.Sandbox; sb != nil {
			sb.Mode = "mutated"
			if sb.Network != nil {
				*sb.Network = !*sb.Network
			}
			if len(sb.DenylistAdd) > 0 {
				sb.DenylistAdd[0] = "mutated"
			}
		}
	}
	if ws := e.WatchSend; ws != nil {
		ws.Message = "mutated"
		mutateCausal(ws.Provenance)
	}
	if d := e.Delegate; d != nil {
		d.AgentType = "mutated"
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
}

// TestCloneEventCoversEveryReferenceField is the completeness net for
// cloneEvent: it walks Event's TYPE graph for every field that carries a
// reference (pointer, slice, map, interface) and fails when the set changes.
// A new reference field must be added to cloneEvent, to the fixture in
// incrementalTestEvent, and to mutateEventDeeply — then listed here.
func TestCloneEventCoversEveryReferenceField(t *testing.T) {
	t.Parallel()
	want := []string{
		"Event.Delegate",
		"Event.DelegateRestore",
		"Event.DelegateRestore.ExplicitToolGrants",
		"Event.DelegateRestore.FrozenSkillBodies",
		"Event.DelegateRestore.FrozenSkillNames",
		"Event.DelegateRestore.FrozenToolNames",
		"Event.DelegateRestore.Provenance",
		"Event.DelegateRestore.Provenance.Chain",
		"Event.DelegateRestore.Provenance.WatchKeys",
		"Event.DelegateRestore.ResultSchema",
		"Event.DelegateRestore.Sandbox",
		"Event.DelegateRestore.Sandbox.DenylistAdd",
		"Event.DelegateRestore.Sandbox.DenylistRemove",
		"Event.DelegateRestore.Sandbox.ExtraReadRoots",
		"Event.DelegateRestore.Sandbox.ExtraWritableRoots",
		"Event.DelegateRestore.Sandbox.Network",
		"Event.EndedAt",
		"Event.ExitCode",
		"Event.Provenance",
		"Event.Provenance.Chain",
		"Event.Provenance.WatchKeys",
		"Event.Resumable",
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
		"Event.WatchSend.Provenance.WatchKeys",
	}
	got := referenceFieldPaths(reflect.TypeOf(Event{}), "Event", map[reflect.Type]bool{})
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
	for i := range t.NumField() {
		f := t.Field(i)
		path := prefix + "." + f.Name
		ft := f.Type
		switch ft.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface:
			out = append(out, path)
			elem := ft
			for elem.Kind() == reflect.Ptr || elem.Kind() == reflect.Slice || elem.Kind() == reflect.Map {
				elem = elem.Elem()
			}
			if elem.Kind() == reflect.Struct && elem != reflect.TypeOf(time.Time{}) {
				out = append(out, referenceFieldPaths(elem, path, seen)...)
			}
		case reflect.Struct:
			if ft != reflect.TypeOf(time.Time{}) {
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
// prefix it read. Nothing in serf does this — the log is append-only by contract
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
