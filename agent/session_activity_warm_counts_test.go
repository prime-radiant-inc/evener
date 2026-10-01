package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func summaryRefreshPending(t *testing.T, summary appwire.SessionActivitySummary) bool {
	t.Helper()
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		RefreshPending bool `json:"refreshPending"` //nolint:tagliatelle // AppWire uses this established camelCase field.
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.RefreshPending
}

func TestSessionActivityWarmCountsRecoverDeliveredDescendantWatch(t *testing.T) {
	f := newStableWatchRuntimeFixture(t, nil)
	f.sourceJM.retirementOwner = f.source
	at := time.Unix(100, 0).UTC()
	if err := f.rootJM.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "root-shell", Type: jobstore.JobShell, OwnerSessionID: f.root.ID(), TS: at, StartedAt: &at}); err != nil {
		t.Fatal(err)
	}
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", f.root.ID())}
	page, err := f.root.ListActivityWatches(t.Context(), appwire.SessionActivityListParams{Ref: params.Ref})
	if err != nil || !page.Page.Complete || len(page.Watches) != 1 {
		t.Fatalf("warm watches: %+v error=%v", page, err)
	}
	before, err := f.root.ActivitySummary(t.Context(), params)
	if err != nil || !before.Jobs.Known || before.Jobs.Active != 1 || !before.Watches.Known || before.Watches.Total != 1 {
		t.Fatalf("initial summary=%+v error=%v", before, err)
	}
	onSessionEventKD(f.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "count recovery"})
	cfg := f.onlyWatchConfig(t)
	state := f.requireOnePending(t).state
	if err := f.sourceJM.settleWatchSendDelivered(cfg, state); err != nil {
		t.Fatal(err)
	}
	after, err := f.root.ActivitySummary(t.Context(), params)
	if err != nil || !after.Jobs.Known || after.Jobs != before.Jobs || !after.Watches.Known || after.Watches.Total != 1 || summaryRefreshPending(t, after) {
		t.Fatalf("summary-only recovery=%+v error=%v", after, err)
	}
	index, err := acquireSessionActivityIndex(t.Context(), f.root.stateDir+"\x00"+f.root.ID(), f.controller)
	if err != nil {
		t.Fatal(err)
	}
	defer index.release()
	if index.jobs[f.source.ID()].DeliveryCounts[cfg.watchID] != 1 {
		t.Fatal("summary did not fold accepted descendant delivery")
	}
}

func TestSessionActivityWarmCountsBoundedSuffixAcrossOwners(t *testing.T) {
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	stores := make(map[string]*jobstore.Store)
	for i := range 2 {
		owner, store := newSessionActivityChildJournal(t, s, fmt.Sprintf("dlg_warm_%d", i), at)
		stores[owner] = store
	}
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree}
	if page, err := s.ListActivityWatches(t.Context(), appwire.SessionActivityListParams{Ref: params.Ref, Scope: params.Scope}); err != nil || !page.Page.Complete {
		t.Fatalf("warm=%+v error=%v", page, err)
	}
	for owner, store := range stores {
		events := make([]jobstore.Event, activityMaxWorkUnits+1)
		for i := range events {
			events[i] = jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("shell_%d", i), Type: jobstore.JobShell, OwnerSessionID: owner, TS: at, StartedAt: &at}
		}
		if err := store.AppendBatch(events); err != nil {
			t.Fatal(err)
		}
	}
	s.emitSessionActivityChanged(s.ID(), appwire.SessionActivityResourceJobs)
	index, err := acquireSessionActivityIndex(t.Context(), s.stateDir+"\x00"+s.ID(), s.delegateController)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	folded := func() int {
		n := 0
		for _, source := range index.jobs {
			n += len(source.Jobs)
		}
		return n
	}
	first, err := s.ActivitySummary(t.Context(), params)
	if err != nil || first.Jobs.Known || !summaryRefreshPending(t, first) {
		t.Fatalf("bounded warm summary=%+v error=%v", first, err)
	}
	previous := folded()
	if previous > activityMaxWorkUnits {
		t.Fatalf("summary exceeded fold budget: %d", previous)
	}
	var busy string
	for owner := range stores {
		if len(index.jobs[owner].Jobs) > 0 {
			busy = owner
		}
	}
	extra := make([]jobstore.Event, activityMaxWorkUnits)
	for i := range extra {
		extra[i] = jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("new_shell_%d", i), Type: jobstore.JobShell, OwnerSessionID: busy, TS: at, StartedAt: &at}
	}
	if err := stores[busy].AppendBatch(extra); err != nil {
		t.Fatal(err)
	}
	for attempt := range 5 {
		next, err := s.ActivitySummary(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		current := folded()
		if current-previous > activityMaxWorkUnits {
			t.Fatalf("summary exceeded shared source budget: %d", current-previous)
		}
		previous = current
		if attempt == 0 {
			for owner := range stores {
				if owner != busy && len(index.jobs[owner].Jobs) == 0 {
					t.Fatal("growing first source starved the next warm owner")
				}
			}
		}
		if next.Jobs.Known {
			if next.Jobs.Total != 2*(activityMaxWorkUnits+1)+len(extra) || summaryRefreshPending(t, next) {
				t.Fatalf("recovered=%+v", next)
			}
			bytes, scans := index.rawBytes, index.scanCalls
			if _, err := s.ActivitySummary(t.Context(), params); err != nil {
				t.Fatal(err)
			}
			if index.rawBytes != bytes || index.scanCalls != scans {
				t.Fatal("current warm summary repeated source probes")
			}
			return
		}
		if !summaryRefreshPending(t, next) {
			t.Fatalf("lost warm demand at %d: %+v", attempt, next)
		}
	}
	t.Fatal("summary suffix recovery failed to reach every warm owner")
}

func TestSessionActivityWarmCountsColdReadsAndReplacementDoNotScan(t *testing.T) {
	stateDir := t.TempDir()
	id := "warmreplacement"
	savePastActivityMeta(t, stateDir, id, "Root")
	path := writeJobLogFast(t, stateDir, id, 1)
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", id)}
	for range 3 {
		summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, params)
		if err != nil || summary.Jobs.Known || summaryRefreshPending(t, summary) {
			t.Fatalf("cold summary=%+v error=%v", summary, err)
		}
	}
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.jobs) != 0 || index.scanCalls != 0 {
		t.Fatal("cold badge created or scanned sources")
	}
	index.release()
	if _, err := LoadSessionActivityJobs(t.Context(), stateDir, id, appwire.SessionActivityListParams{Ref: params.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	writeJobLogFast(t, stateDir, id, 3)
	index.revision.Add(1)
	for range 3 {
		summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, params)
		if err != nil || summary.Jobs.Known || summaryRefreshPending(t, summary) {
			t.Fatalf("replacement summary=%+v error=%v", summary, err)
		}
	}
	if index.scanCalls != 1 || len(index.jobs) != 0 {
		t.Fatal("replacement badge reconstructed cold authority")
	}
}

func TestSessionActivityWarmCountsEstablishedEmptyCatchesFirstJournal(t *testing.T) {
	stateDir := t.TempDir()
	id := "warmempty"
	savePastActivityMeta(t, stateDir, id, "Root")
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", id)}
	if _, err := LoadSessionActivityJobs(t.Context(), stateDir, id, appwire.SessionActivityListParams{Ref: params.Ref}); err != nil {
		t.Fatal(err)
	}
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	writeJobLogFast(t, stateDir, id, 2)
	index.revision.Add(1)
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, params)
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 2 || summaryRefreshPending(t, summary) {
		t.Fatalf("first journal summary=%+v error=%v", summary, err)
	}
}

func TestSessionActivityWarmCountsDoNotAdvancePartialColdSource(t *testing.T) {
	stateDir := t.TempDir()
	id := "warmpartialcold"
	savePastActivityMeta(t, stateDir, id, "Root")
	writeJobLogFast(t, stateDir, id, activityMaxWorkUnits+1)
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", id)}
	page, err := LoadSessionActivityJobs(t.Context(), stateDir, id, appwire.SessionActivityListParams{Ref: params.Ref})
	if err != nil || page.Page.Complete {
		t.Fatalf("partial cold=%+v error=%v", page, err)
	}
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	offset, scans := index.jobs[id].Cursor.Journal.Offset, index.scanCalls
	index.revision.Add(1)
	for range 3 {
		summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, params)
		if err != nil || summary.Jobs.Known || summaryRefreshPending(t, summary) {
			t.Fatalf("partial cold summary=%+v error=%v", summary, err)
		}
	}
	if index.jobs[id].Cursor.Journal.Offset != offset || index.scanCalls != scans {
		t.Fatal("badge continued unestablished reconstruction")
	}
}

func TestSessionActivityWarmCountsMissingAndShrunkenSourcesBecomeCold(t *testing.T) {
	for _, mutation := range []string{"missing", "shrunken"} {
		t.Run(mutation, func(t *testing.T) {
			stateDir := t.TempDir()
			id := "warmincarnation"
			savePastActivityMeta(t, stateDir, id, "Root")
			path := writeJobLogFast(t, stateDir, id, 2)
			params := appwire.SessionActivityReadParams{Ref: encodeRef("", id)}
			if _, err := LoadSessionActivityJobs(t.Context(), stateDir, id, appwire.SessionActivityListParams{Ref: params.Ref}); err != nil {
				t.Fatal(err)
			}
			index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
			if err != nil {
				t.Fatal(err)
			}
			index.release()
			if mutation == "missing" {
				err = os.Remove(path)
			} else {
				err = os.Truncate(path, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
			index.revision.Add(1)
			for range 2 {
				summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, params)
				if err != nil || summary.Jobs.Known || summaryRefreshPending(t, summary) {
					t.Fatalf("retired source=%+v error=%v", summary, err)
				}
			}
			if len(index.jobs) != 0 || index.scanCalls != 1 {
				t.Fatal("lost source evidence allowed cold reconstruction")
			}
		})
	}
}

func TestSessionActivityWarmCountsUnavailableSourceRetriesWithoutLosingCursor(t *testing.T) {
	stateDir := t.TempDir()
	id := "warmunavailable"
	savePastActivityMeta(t, stateDir, id, "Root")
	path := writeJobLogFast(t, stateDir, id, 1)
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", id)}
	if _, err := LoadSessionActivityJobs(t.Context(), stateDir, id, appwire.SessionActivityListParams{Ref: params.Ref}); err != nil {
		t.Fatal(err)
	}
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	offset := index.jobs[id].Cursor.Journal.Offset
	if err := os.Rename(path, path+".held"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	index.revision.Add(1)
	_, err = LoadSessionActivitySummary(t.Context(), stateDir, id, params)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("warm failure lost typed error: %v", err)
	}
	data, ok := appwire.ErrorDataOf(wire.Data)
	if !ok || data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("warm retry disposition=%+v", wire)
	}
	if index.jobs[id].Cursor.Journal.Offset != offset {
		t.Fatal("failed warm read lost accepted cursor")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".held", path); err != nil {
		t.Fatal(err)
	}
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, params)
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 1 || summaryRefreshPending(t, summary) {
		t.Fatalf("recovered=%+v error=%v", summary, err)
	}
}

// The callback runs at a fold boundary, without concurrent fixture mutation.
type sessionActivityInvalidateDuringFold struct {
	context.Context
	source     *sessionActivityJobIndex
	invalidate func()
	fired      bool
}

func (ctx *sessionActivityInvalidateDuringFold) Err() error {
	if !ctx.fired && ctx.source.PendingPosition == 1 {
		ctx.fired = true
		ctx.invalidate()
	}
	return ctx.Context.Err()
}

func TestSessionActivityWarmCountsInvalidationDuringFoldRetainsDemand(t *testing.T) {
	stateDir := t.TempDir()
	id := "warminvalidation"
	savePastActivityMeta(t, stateDir, id, "Root")
	path := writeJobLogFast(t, stateDir, id, 1)
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", id)}
	if _, err := LoadSessionActivityJobs(t.Context(), stateDir, id, appwire.SessionActivityListParams{Ref: params.Ref}); err != nil {
		t.Fatal(err)
	}
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Unix(100, 0).UTC()
	appendJob := func(id string) {
		t.Helper()
		if err := store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: id, Type: jobstore.JobShell, OwnerSessionID: "warminvalidation", TS: at, StartedAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
	appendJob("second")
	appendJob("third")
	index.revision.Add(1)
	ctx := &sessionActivityInvalidateDuringFold{Context: t.Context(), source: index.jobs[id], invalidate: func() { appendJob("fourth"); index.revision.Add(1) }}
	summary, err := LoadSessionActivitySummary(ctx, stateDir, id, params)
	if err != nil || !ctx.fired || summary.Jobs.Known || !summaryRefreshPending(t, summary) {
		t.Fatalf("raced summary=%+v fired=%v error=%v", summary, ctx.fired, err)
	}
	summary, err = LoadSessionActivitySummary(t.Context(), stateDir, id, params)
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 4 || summaryRefreshPending(t, summary) {
		t.Fatalf("recovered summary=%+v error=%v", summary, err)
	}
}

func TestSessionActivityWarmCountsUnavailableDescendantPreservesRootJobs(t *testing.T) {
	for _, failure := range []string{"io", "corrupt"} {
		t.Run(failure, func(t *testing.T) {
			s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md")}))
			child, store := newSessionActivityChildJournal(t, s, "dlg_unavailable", time.Unix(100, 0).UTC())
			writeJobLogFast(t, s.stateDir, s.ID(), 1)
			params := appwire.SessionActivityReadParams{Ref: encodeRef("", s.ID())}
			if _, err := s.ListActivityWatches(t.Context(), appwire.SessionActivityListParams{Ref: params.Ref}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(jobsDir(s.stateDir, child), "jobs.jsonl")
			intact, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "io" {
				if err := os.Rename(path, path+".held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, append(intact, []byte("{invalid}\n")...), 0o600); err != nil {
				t.Fatal(err)
			}
			s.emitSessionActivityChanged(child, appwire.SessionActivityResourceJobs)
			summary, err := s.ActivitySummary(t.Context(), params)
			if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 1 || summary.Watches.Known || len(summary.Issues) != 1 || summary.Issues[0].Ref != encodeRef("", child) {
				t.Fatalf("partial summary=%+v error=%v", summary, err)
			}
			if failure == "io" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".held", path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, intact, 0o600); err != nil {
				t.Fatal(err)
			}
			// A newly appended receiver watch must be folded even though this child
			// had no receiver watches before its journal became unavailable.
			if err := store.Append(jobstore.Event{Kind: jobstore.EventWatchRegistered, WatchID: "new-root-watch", TS: time.Unix(101, 0).UTC(), Watch: &jobstore.WatchEvent{Generation: "g", OwnerSessionID: child, VisibleSessionID: child, Target: "timer", ConfigHash: "hash", Config: &jobstore.WatchConfigSnapshot{Target: "timer", ReceiverSessionID: s.ID()}}}); err != nil {
				t.Fatal(err)
			}
			s.emitSessionActivityChanged(child, appwire.SessionActivityResourceWatches)
			summary, err = s.ActivitySummary(t.Context(), params)
			if err != nil || !summary.Jobs.Known || summary.Watches.Total != 1 || len(summary.Issues) != 0 {
				t.Fatalf("recovered summary=%+v error=%v", summary, err)
			}
		})
	}
}
