package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestSessionActivityPageBudgetEscapedRowsRemainReachable(t *testing.T) {
	t.Parallel()
	const total = 75
	for _, resource := range []string{"delegates", "jobs", "watches"} {
		t.Run(resource, func(t *testing.T) {
			stateDir := t.TempDir()
			root := "escapedbudget"
			ref := encodeRef("", root)
			savePastActivityMeta(t, stateDir, root, "Root")
			prose := strings.Repeat("<>&界\"\\\n", 600)
			prefix := ""
			if resource == "delegates" {
				prefix = "dlg_budget_"
				descriptors := make([]delegatestore.Descriptor, total)
				for i := range descriptors {
					descriptors[i] = pastStableDescriptor(root, fmt.Sprintf("childbudget_%03d", i), prose)
					descriptors[i].Description = prose
				}
				writePastStableDelegates(t, stateDir, root, descriptors...)
			} else {
				if err := os.MkdirAll(jobsDir(stateDir, root), 0o700); err != nil {
					t.Fatal(err)
				}
				store, err := jobstore.Open(filepath.Join(jobsDir(stateDir, root), "jobs.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				batch := make([]jobstore.Event, total)
				for i := range batch {
					at := time.Unix(int64(i+1), 0).UTC()
					if resource == "jobs" {
						prefix = "job_budget_"
						batch[i] = jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("%s%03d", prefix, i), TS: at, StartedAt: &at, Type: jobstore.JobShell, Background: true, OwnerSessionID: root, Description: prose, Command: prose, Task: prose}
					} else {
						prefix = "watch_budget_"
						batch[i] = jobstore.Event{Kind: jobstore.EventWatchRegistered, WatchID: fmt.Sprintf("%s%03d", prefix, i), TS: at, Watch: &jobstore.WatchEvent{Generation: "g", OwnerSessionID: root, VisibleSessionID: root, Target: "timer", ConfigHash: "hash", Config: &jobstore.WatchConfigSnapshot{Target: "timer", ReceiverSessionID: root, Note: prose, RepeatSeconds: 3}}}
					}
				}
				if err := store.AppendBatch(batch); err != nil {
					t.Fatal(err)
				}
			}
			params := appwire.SessionActivityListParams{Ref: ref, Scope: appwire.SessionActivityScopeSession, Limit: 200}
			// A single cold-source work unit must yield before publishing a partial
			// index. The public loader then resumes that same admitted walk.
			read, err := retainedActivityRead(t.Context(), stateDir, root, appwire.SessionActivityReadParams{Ref: ref, Scope: params.Scope})
			if err != nil {
				t.Fatal(err)
			}
			read.budget = 1
			var cold appwire.SessionActivityPage
			var coldRows int
			switch resource {
			case "delegates":
				value, readErr := read.delegatesPage(t.Context(), params)
				cold, coldRows, err = value.Page, len(value.Delegates), readErr
			case "jobs":
				value, readErr := read.jobsPage(t.Context(), params)
				cold, coldRows, err = value.Page, len(value.Jobs), readErr
			default:
				value, readErr := read.watchesPage(t.Context(), params)
				cold, coldRows, err = value.Page, len(value.Watches), readErr
			}
			read.index.release()
			if err != nil || read.budget != 0 || coldRows != 0 || cold.Complete || cold.NextCursor == "" {
				t.Fatalf("single work unit leaked partial rows: budget=%d rows=%d page=%+v err=%v", read.budget, coldRows, cold, err)
			}
			params.Cursor = cold.NextCursor
			seen := 0
			complete, byteReduced := false, false
			for range 200 {
				var response any
				var page appwire.SessionActivityPage
				var ids []string
				switch resource {
				case "delegates":
					value, err := LoadSessionActivityDelegates(t.Context(), stateDir, root, params)
					if err != nil {
						t.Fatal(err)
					}
					response, page = value, value.Page
					for _, row := range value.Delegates {
						if row.OwnerRef != ref || row.RootRef != ref || row.ChildRef != "local:child"+strings.TrimPrefix(row.DelegateID, "dlg_") {
							t.Fatalf("delegate lost authority: %+v", row)
						}
						ids = append(ids, row.DelegateID)
					}
				case "jobs":
					value, err := LoadSessionActivityJobs(t.Context(), stateDir, root, params)
					if err != nil {
						t.Fatal(err)
					}
					response, page = value, value.Page
					for _, row := range value.Jobs {
						if row.OwnerRef != ref || row.OwnerSessionID != root {
							t.Fatalf("job lost authority: %+v", row)
						}
						ids = append(ids, row.JobID)
					}
				default:
					value, err := LoadSessionActivityWatches(t.Context(), stateDir, root, params)
					if err != nil {
						t.Fatal(err)
					}
					response, page = value, value.Page
					for _, row := range value.Watches {
						if row.OwnerRef != ref || row.ReceiverRef != ref || len(row.Watch.Cadence) != 1 || row.Watch.Cadence[0].Seconds != 3 {
							t.Fatalf("watch lost receiver/cadence: %+v", row)
						}
						ids = append(ids, row.Watch.ID)
					}
				}
				encoded, err := json.Marshal(response)
				if err != nil || len(encoded) > sessionActivityPageBytes {
					t.Fatalf("public envelope bytes=%d err=%v", len(encoded), err)
				}
				for _, id := range ids {
					want := fmt.Sprintf("%s%03d", prefix, total-1-seen)
					if id != want {
						t.Fatalf("candidate lost, duplicated or reordered: got=%s want=%s", id, want)
					}
					seen++
				}
				byteReduced = byteReduced || (len(ids) > 0 && len(ids) < params.Limit && !page.Complete)
				if page.Complete {
					complete = true
					break
				}
				if page.NextCursor == "" || page.NextCursor == params.Cursor {
					t.Fatal("continuation did not advance")
				}
				params.Cursor = page.NextCursor
			}
			if !complete || seen != total || !byteReduced {
				t.Fatalf("bounded paging complete=%v rows=%d byteReduced=%v", complete, seen, byteReduced)
			}
		})
	}
}
