package agent

import (
	"context"
	"path/filepath"
	"testing"
)

// createDelegate defaults a nil context before validating, and an empty task is
// rejected as an invalid request.
func TestW2Dlg_CreateDelegate_NilContextEmptyTask(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)
	//nolint:staticcheck // exercising the nil-context defaulting arm on purpose
	res := s.createDelegate(nil, delegateArgs{Task: "   "})
	if res.Err == nil {
		t.Fatalf("empty task: want invalid_request error, got %+v", res)
	}
}

// createDelegate surfaces the job-manager-unavailable failure when the session
// has no job manager.
func TestW2Dlg_CreateDelegate_NoJobManager(t *testing.T) {
	t.Parallel()
	res := (&Session{}).createDelegate(context.Background(), delegateArgs{Task: "do work"})
	if res.Err == nil {
		t.Fatal("no job manager: want error")
	}
}

// outstandingDelegateCount propagates a store read error rather than reporting a
// quiescent zero count.
func TestW2Dlg_OutstandingDelegateCount_StoreError(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))
	if _, err := jm.outstandingDelegateCount(); err == nil {
		t.Fatal("corrupt store: want error")
	}
}

// treeHasOutstandingWork propagates the underlying store read error from the
// outstanding-delegate count.
func TestW2Dlg_TreeHasOutstandingWork_StoreError(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)
	w2dlg_corruptSessionLog(t, s)
	if _, err := s.treeHasOutstandingWork(); err == nil {
		t.Fatal("corrupt store: want error")
	}
}

// DrainJobTree is a no-op that returns an empty result when the session has no
// job manager.
func TestW2Dlg_DrainJobTree_NoJobManager(t *testing.T) {
	t.Parallel()
	res, err := (&Session{}).DrainJobTree(context.Background())
	if res != "" || err != nil {
		t.Fatalf("no job manager = (%q, %v), want (\"\", nil)", res, err)
	}
}
