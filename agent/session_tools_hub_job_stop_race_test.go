package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

func TestManagerStopJobTerminalRace(t *testing.T) {
	t.Parallel()
	for _, status := range []jobstore.Status{jobstore.StatusCompleted, jobstore.StatusCancelled} {
		for _, includeChildren := range []bool{false, true} {
			name := string(status) + "/job_only"
			if includeChildren {
				name = string(status) + "/include_children"
			}
			t.Run(name, func(t *testing.T) {
				jm := newTestJM(t)
				t.Cleanup(func() { _ = jm.store.Close() })
				s := &Session{jobManager: jm, id: jm.sessionID}
				parent, err := jm.createShell(createShellOpts{Command: "parent"})
				if err != nil {
					t.Fatal(err)
				}
				jm.setParentJobID(parent.JobID)
				child, err := jm.createShell(createShellOpts{Command: "child"})
				jm.setParentJobID("")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = jm.finalize(child.JobID, jobstore.StatusCompleted, "finished", nil) })

				// The hub's earlier JobGet admits this live owned shell. The
				// daemon's own preflight runs only after the real job finishes.
				owner, precheck, err := s.nestedOrLocalJobManager(parent.JobID)
				if err != nil || owner != jm || precheck == nil || precheck.Status != jobstore.StatusRunning || precheck.Type != jobstore.JobShell || precheck.OwnerSessionID != s.id {
					t.Fatalf("live owned precheck: owner=%p record=%+v err=%v", owner, precheck, err)
				}
				if err := jm.finalize(parent.JobID, status, "finished", nil); err != nil {
					t.Fatal(err)
				}
				journal := filepath.Join(jm.dir, "jobs.jsonl")
				before, err := os.ReadFile(journal)
				if err != nil {
					t.Fatal(err)
				}
				result, err := s.StopJobForManager(t.Context(), appwire.SessionJobStopParams{JobID: parent.JobID, IncludeChildren: includeChildren})
				wire := appserver.WireError(err)
				if err == nil || wire.Code != appwire.CodeInvalidParams {
					t.Errorf("terminal stop result=%+v wire code=%d, want %d: %v", result, wire.Code, appwire.CodeInvalidParams, err)
				}
				after, err := os.ReadFile(journal)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("terminal stop appended to the job journal")
				}
				records, err := jm.store.Load()
				if err != nil {
					t.Fatal(err)
				}
				if records[parent.JobID].Status != status || records[child.JobID].Status != jobstore.StatusRunning || jm.running[child.JobID].stopStatus != "" {
					t.Fatalf("terminal stop affected parent or child: parent=%+v child=%+v child stop=%q", records[parent.JobID], records[child.JobID], jm.running[child.JobID].stopStatus)
				}
			})
		}
	}
	t.Run("unrelated_errors", func(t *testing.T) {
		jm := newTestJM(t)
		t.Cleanup(func() { _ = jm.store.Close() })
		s := &Session{jobManager: jm, id: jm.sessionID}
		for _, params := range []appwire.SessionJobStopParams{
			{JobID: "dlg_not_a_job"},
			{JobID: "job_unknown", MaxWaitMS: -1},
		} {
			_, err := s.StopJobForManager(t.Context(), params)
			if err == nil || appserver.WireError(err).Code != appwire.CodeInvalidParams {
				t.Fatalf("invalid job stop params=%+v: %v", params, err)
			}
		}
		_, err := s.StopJobForManager(t.Context(), appwire.SessionJobStopParams{JobID: "job_unknown"})
		if !errors.Is(err, errJobNotFoundSentinel) || appserver.WireError(err).Code != appwire.CodeInternalError {
			t.Fatalf("unknown job should retain not-found error and InternalError wire code: %v", err)
		}
		if err := jm.store.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = s.StopJobForManager(t.Context(), appwire.SessionJobStopParams{JobID: "job_unknown"})
		if !errors.Is(err, jobstore.ErrStoreClosed) || appserver.WireError(err).Code != appwire.CodeInternalError {
			t.Fatalf("closed store should retain storage error and InternalError wire code: %v", err)
		}
	})
}
