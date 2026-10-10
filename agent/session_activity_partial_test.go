package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestSessionActivityUnavailableBranchPreservesHealthyResources(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"jobs", "watches"} {
		t.Run(resource, func(t *testing.T) {
			s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
			at := time.Unix(100, 0).UTC()
			var ownerRefs []string
			var badPath string
			var intact []byte
			for _, delegateID := range []string{"dlg_healthy", "dlg_unavailable"} {
				ownerID, store := newSessionActivityChildJournal(t, s, delegateID, at)
				ownerRefs = append(ownerRefs, encodeRef("", ownerID))
				event := jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_equal", Type: jobstore.JobShell, Background: true, OwnerSessionID: ownerID, TS: at, StartedAt: &at}
				if resource == "watches" {
					event = jobstore.Event{Kind: jobstore.EventWatchRegistered, WatchID: "watch_equal", TS: at, Watch: &jobstore.WatchEvent{
						Generation: "g", OwnerSessionID: ownerID, VisibleSessionID: ownerID, Target: "timer", ConfigHash: "hash",
						Config: &jobstore.WatchConfigSnapshot{Target: "timer", ReceiverSessionID: ownerID},
					}}
				}
				if err := store.Append(event); err != nil {
					t.Fatal(err)
				}
				if resource == "jobs" {
					if err := store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_unmarked", Type: jobstore.JobShell, OwnerSessionID: ownerID, TS: at, StartedAt: &at}); err != nil {
						t.Fatal(err)
					}
				}
				if delegateID == "dlg_unavailable" {
					badPath = filepath.Join(jobsDir(s.stateDir, ownerID), "jobs.jsonl")
					var err error
					intact, err = os.ReadFile(badPath)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(badPath, []byte("{invalid}\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			list := func(params appwire.SessionActivityListParams) ([]string, appwire.SessionActivityPage, error) {
				var owners []string
				if resource == "watches" {
					page, err := s.ListActivityWatches(t.Context(), params)
					for _, row := range page.Watches {
						owners = append(owners, row.ReceiverRef)
					}
					return owners, page.Page, err
				}
				page, err := s.ListActivityJobs(t.Context(), params)
				for _, row := range page.Jobs {
					owners = append(owners, row.OwnerRef)
				}
				return owners, page.Page, err
			}
			params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree, Limit: 1}
			seen := make(map[string]bool)
			var last appwire.SessionActivityPage
			for range 4 {
				owners, page, err := list(params)
				if err != nil {
					t.Fatalf("one unavailable branch parked healthy %s: %v", resource, err)
				}
				for _, owner := range owners {
					if owner != ownerRefs[0] || seen[owner] {
						t.Fatalf("unavailable or duplicate source row: %s", owner)
					}
					seen[owner] = true
				}
				if page.Complete || len(page.Issues) != 1 || page.Issues[0].Ref != ownerRefs[1] || page.Issues[0].Code != "unavailable" {
					t.Fatalf("missing truthful branch issue: %+v", page)
				}
				last = page
				if page.NextCursor == "" {
					break
				}
				if page.NextCursor == params.Cursor {
					t.Fatal("partial collection cursor did not advance")
				}
				params.Cursor = page.NextCursor
			}
			if len(seen) != 1 || last.NextCursor != "" {
				t.Fatalf("healthy branch unreachable: owners=%v page=%+v", seen, last)
			}
			if resource == "jobs" {
				summary, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: params.Ref, Scope: params.Scope})
				if err != nil || summary.Jobs.Known {
					t.Fatalf("partial counts claimed authority: %+v error=%v", summary, err)
				}
			}

			if err := os.WriteFile(badPath, intact, 0o600); err != nil {
				t.Fatal(err)
			}
			params.Cursor = ""
			params.Limit = 2
			owners, recovered, err := list(params)
			if err != nil || len(owners) != 2 || owners[0] == owners[1] || !recovered.Complete || len(recovered.Issues) != 0 {
				t.Fatalf("fresh read did not recover repaired branch: owners=%v page=%+v error=%v", owners, recovered, err)
			}
			if resource == "jobs" {
				summary, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: params.Ref, Scope: params.Scope})
				if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 2 || summary.Jobs.Active != 2 || len(summary.Issues) != 0 {
					t.Fatalf("recovered eligibility counts remain unknown or include foreground: %+v error=%v", summary, err)
				}
			}
		})
	}
}

func TestSessionActivityFailedScansShareInputBudget(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	for _, id := range []string{"dlg_a_unavailable", "dlg_b_unavailable", "dlg_z_healthy"} {
		owner, store := newSessionActivityChildJournal(t, s, id, at)
		if id == "dlg_z_healthy" {
			if err := store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_healthy", Type: jobstore.JobShell, Background: true, OwnerSessionID: owner, TS: at, StartedAt: &at}); err != nil {
				t.Fatal(err)
			}
		} else {
			// Two terminated malformed records exceed the shared four MiB
			// allowance even though neither scan can accept a cursor.
			if err := os.WriteFile(filepath.Join(jobsDir(s.stateDir, owner), "jobs.jsonl"), []byte(strings.Repeat(" ", 3<<20)+"{invalid}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree}
	for call := range 3 {
		page, err := s.ListActivityJobs(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Page.Issues) != min(call+1, 2) || page.Page.Complete {
			t.Fatalf("failed sources did not share read allowance: call=%d page=%+v", call, page.Page)
		}
		if call < 2 {
			if len(page.Jobs) != 0 || page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
				t.Fatalf("failed scan neither yielded nor advanced: call=%d page=%+v", call, page)
			}
		} else if len(page.Jobs) != 1 || page.Jobs[0].JobID != "job_healthy" || page.Page.NextCursor != "" {
			t.Fatalf("healthy source not reachable after bounded failures: %+v", page)
		}
		params.Cursor = page.Page.NextCursor
	}
}

func TestSessionActivityUnavailableBranchInvalidatesWarmCounts(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"append", "stat"} {
		t.Run(failure, func(t *testing.T) {
			s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
			at := time.Unix(100, 0).UTC()
			var badPath string
			for _, id := range []string{"dlg_healthy", "dlg_unavailable"} {
				owner, store := newSessionActivityChildJournal(t, s, id, at)
				if err := store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_equal", Type: jobstore.JobShell, Background: true, OwnerSessionID: owner, TS: at, StartedAt: &at}); err != nil {
					t.Fatal(err)
				}
				if id == "dlg_unavailable" {
					badPath = filepath.Join(jobsDir(s.stateDir, owner), "jobs.jsonl")
				}
			}
			params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree}
			warm, err := s.ListActivityJobs(t.Context(), params)
			if err != nil || len(warm.Jobs) != 2 || !warm.Page.Complete {
				t.Fatalf("warm fixture: %+v error=%v", warm, err)
			}
			if failure == "append" {
				intact, err := os.ReadFile(badPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(badPath, append(intact, []byte("{invalid}\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(badPath, badPath+".held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("jobs.jsonl", badPath); err != nil {
					t.Fatal(err)
				}
			}
			page, err := s.ListActivityJobs(t.Context(), params)
			if err != nil || page.Page.Complete || len(page.Page.Issues) != 1 {
				t.Fatalf("warm source failure was not isolated: %+v error=%v", page, err)
			}
			summary, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: params.Ref, Scope: params.Scope})
			if summary.Jobs.Known || summary.Watches.Known {
				t.Fatalf("source failure left trusted cached counts: %+v error=%v", summary, err)
			}
			if err != nil || len(summary.Issues) != 1 || summary.Issues[0].Code != "unavailable" || summary.Issues[0].Ref != page.Page.Issues[0].Ref {
				t.Fatalf("partial summary lost source issue: %+v error=%v", summary, err)
			}
		})
	}
}
