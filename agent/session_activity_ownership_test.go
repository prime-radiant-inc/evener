package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestSessionActivitySubtreeJobsPreserveEqualKeysAcrossOwners(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	owners := make(map[string]bool)
	for i := range 2 {
		id := fmt.Sprintf("dlg_equal_owner_%d", i)
		ownerID, store := newSessionActivityChildJournal(t, s, id, at)
		if err := store.AppendBatch([]jobstore.Event{
			{Kind: jobstore.EventJobStarted, JobID: "job_unmarked_before", Type: jobstore.JobShell, OwnerSessionID: ownerID, TS: at, StartedAt: &at},
			{Kind: jobstore.EventJobStarted, JobID: "job_equal", Type: jobstore.JobShell, Background: true, OwnerSessionID: ownerID, TS: at, StartedAt: &at, Command: fmt.Sprintf("owner-%d", i)},
			{Kind: jobstore.EventJobStarted, JobID: "job_unmarked_after", Type: jobstore.JobShell, OwnerSessionID: ownerID, TS: at, StartedAt: &at},
		}); err != nil {
			t.Fatal(err)
		}
		owner := encodeRef("", ownerID)
		owners[owner] = true
		direct, err := s.ListActivityJobs(t.Context(), appwire.SessionActivityListParams{Ref: owner})
		if err != nil || len(direct.Jobs) != 1 || direct.Jobs[0].OwnerRef != owner || direct.Jobs[0].JobID != "job_equal" {
			t.Fatalf("direct owner %s: jobs=%+v error=%v", owner, direct.Jobs, err)
		}
		summary, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: owner})
		if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 1 || summary.Jobs.Active != 1 {
			t.Fatalf("direct counts include excluded jobs or siblings: counts=%+v error=%v", summary.Jobs, err)
		}
	}
	ref := encodeRef("", s.ID())
	direct, err := s.ListActivityJobs(t.Context(), appwire.SessionActivityListParams{Ref: ref})
	if err != nil || len(direct.Jobs) != 0 || !direct.Page.Complete {
		t.Fatalf("root direct scope includes child jobs: %+v error=%v", direct, err)
	}
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			params := appwire.SessionActivityListParams{Ref: ref, Scope: appwire.SessionActivityScopeSubtree, Limit: limit}
			seen := make(map[string]bool)
			complete := false
			for range 3 {
				page, err := s.ListActivityJobs(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range page.Jobs {
					if !owners[row.OwnerRef] || row.JobID != "job_equal" || seen[row.OwnerRef] {
						t.Fatalf("lost or duplicated source-qualified identity: %+v", row)
					}
					seen[row.OwnerRef] = true
				}
				if page.Page.Complete {
					complete = true
					break
				}
				if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
					t.Fatalf("continuation did not advance: %+v", page.Page)
				}
				params.Cursor = page.Page.NextCursor
			}
			if !complete || len(seen) != len(owners) {
				t.Fatalf("subtree lost source-owned jobs: owners=%v complete=%v", seen, complete)
			}
		})
	}
	summary, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: ref, Scope: appwire.SessionActivityScopeSubtree})
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != len(owners) {
		t.Fatalf("subtree summary disagrees with rows: counts=%+v error=%v", summary.Jobs, err)
	}
}

func TestSessionActivityWatchesPreserveEqualKeysAcrossReceivers(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	receivers := make(map[string]bool)
	for i := range 2 {
		ownerID, store := newSessionActivityChildJournal(t, s, fmt.Sprintf("dlg_watch_owner_%d", i), at)
		if err := store.Append(jobstore.Event{Kind: jobstore.EventWatchRegistered, WatchID: "watch_equal", TS: at, Watch: &jobstore.WatchEvent{
			Generation: "g", OwnerSessionID: ownerID, VisibleSessionID: ownerID, Target: "timer", ConfigHash: "hash",
			Config: &jobstore.WatchConfigSnapshot{Target: "timer", ReceiverSessionID: ownerID},
		}}); err != nil {
			t.Fatal(err)
		}
		receivers[encodeRef("", ownerID)] = true
	}
	for receiver := range receivers {
		direct, err := s.ListActivityWatches(t.Context(), appwire.SessionActivityListParams{Ref: receiver})
		if err != nil || len(direct.Watches) != 1 || direct.Watches[0].ReceiverRef != receiver || direct.Watches[0].Watch.ID != "watch_equal" {
			t.Errorf("selected receiver %s lost its own watch: rows=%+v error=%v", receiver, direct.Watches, err)
		}
	}
	ref := encodeRef("", s.ID())
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			params := appwire.SessionActivityListParams{Ref: ref, Scope: appwire.SessionActivityScopeSubtree, Limit: limit}
			seen := make(map[string]bool)
			complete := false
			for range 3 {
				page, err := s.ListActivityWatches(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range page.Watches {
					if !receivers[row.ReceiverRef] || row.OwnerRef != row.ReceiverRef || row.Watch.ID != "watch_equal" || seen[row.ReceiverRef] {
						t.Fatalf("lost or duplicated receiver-qualified identity: %+v", row)
					}
					seen[row.ReceiverRef] = true
				}
				if page.Page.Complete {
					complete = true
					break
				}
				if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
					t.Fatalf("continuation did not advance: %+v", page.Page)
				}
				params.Cursor = page.Page.NextCursor
			}
			if !complete || len(seen) != len(receivers) {
				t.Fatalf("subtree lost receiver-owned watches: receivers=%v complete=%v", seen, complete)
			}
		})
	}
	summary, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: ref, Scope: appwire.SessionActivityScopeSubtree})
	if err != nil || summary.Watches.Total != len(receivers) || summary.Watches.Known {
		t.Fatalf("subtree lost registrations or claimed known armed state: counts=%+v error=%v", summary.Watches, err)
	}
}

func newSessionActivityChildJournal(t *testing.T, s *Session, delegateID string, at time.Time) (string, *jobstore.Store) {
	t.Helper()
	descriptor := stableToolDescriptor(s, delegateID, "")
	controller := s.delegateController
	controller.mu.Lock()
	_, err := controller.appendLocked(delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: delegateID, TS: at, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}})
	controller.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	dir := jobsDir(s.stateDir, descriptor.ChildSessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return descriptor.ChildSessionID, store
}

func TestSessionActivityWatchSourceRemainsPhysicalWithParentReceiver(t *testing.T) {
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	sourceID, store := newSessionActivityChildJournal(t, s, "dlg_watch_source", at)
	if err := store.Append(jobstore.Event{Kind: jobstore.EventWatchRegistered, WatchID: "watch_source", TS: at, Watch: &jobstore.WatchEvent{
		Generation: "g", OwnerSessionID: sourceID, VisibleSessionID: s.ID(), Target: "job_equal", ConfigHash: "hash",
		Config: &jobstore.WatchConfigSnapshot{Target: "job_equal", ReceiverSessionID: s.ID(), OutputMatch: "ready"},
	}}); err != nil {
		t.Fatal(err)
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
	live, err := s.ListActivityWatches(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	// A retained reader must recover source authority from the journal itself.
	key := s.stateDir + "\x00" + s.ID()
	sessionActivityIndexes.Lock()
	if cached := sessionActivityIndexes.entries[key]; cached != nil {
		delete(sessionActivityIndexes.entries, key)
		sessionActivityIndexes.order.Remove(cached.element)
	}
	sessionActivityIndexes.Unlock()
	retained, err := LoadSessionActivityWatches(t.Context(), s.stateDir, s.ID(), params)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []appwire.SessionWatchesResponse{live, retained} {
		if len(page.Watches) != 1 {
			t.Fatalf("watch rows = %+v", page)
		}
		row := page.Watches[0]
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		var source string
		if err := json.Unmarshal(fields["sourceRef"], &source); err != nil {
			t.Fatalf("missing physical source reference: %v", err)
		}
		if source != encodeRef("", sourceID) || row.OwnerRef != params.Ref || row.ReceiverRef != params.Ref || row.Watch.Target != "job_equal" {
			t.Fatalf("source/receiver authority lost: source=%s row=%+v", source, row)
		}
	}
}

func TestSessionActivityRoutedWatchPreservesPhysicalSource(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	params := appwire.SessionActivityListParams{Ref: encodeRef("", fixture.root.ID())}
	live, err := fixture.root.ListActivityWatches(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Watches) != 1 {
		t.Fatalf("routed watch missing: %+v", live)
	}
	row := live.Watches[0]
	if row.SourceRef != encodeRef("", fixture.source.ID()) || row.ReceiverRef != params.Ref || row.OwnerRef != params.Ref {
		t.Fatalf("routed watch authority lost: %+v", row)
	}
}
