package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/llm"
)

// w2dlg_corruptSessionLog appends a garbage line to the session's durable job
// log so subsequent store loads fail.
func w2dlg_corruptSessionLog(t *testing.T, s *Session) {
	t.Helper()
	s1cov_corruptJobLog(t, filepath.Join(s.jobManager.dir, "jobs.jsonl"))
}

func w2dlg_session(t *testing.T) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	return newDelegateTestSession(t, c)
}

// ownerJobManagerFor returns no owner when the session has no job manager and
// when the underlying store load fails.
func TestW2Dlg_OwnerJobManagerFor_Guards(t *testing.T) {
	t.Parallel()
	if jm, rec := (&Session{}).ownerJobManagerFor("job_x"); jm != nil || rec != nil {
		t.Fatalf("no-jobmanager = (%v, %v), want (nil, nil)", jm, rec)
	}

	s := w2dlg_session(t)
	w2dlg_corruptSessionLog(t, s)
	if jm, rec := s.ownerJobManagerFor("job_x"); jm != nil || rec != nil {
		t.Fatalf("corrupt store = (%v, %v), want (nil, nil)", jm, rec)
	}
}

// nestedOrLocalJobManager, stopNestedOrLocal, stopChildren, and
// directDelegateJobForChild all surface the job-manager-unavailable failure when
// the session has no job manager.
func TestW2Dlg_NestedHelpers_NoJobManager(t *testing.T) {
	t.Parallel()
	bare := &Session{}

	if _, _, err := bare.nestedOrLocalJobManager("job_x"); err == nil {
		t.Fatal("nestedOrLocalJobManager: want error")
	}
	if _, err := bare.stopNestedOrLocal("job_x"); err == nil {
		t.Fatal("stopNestedOrLocal: want error")
	}
	if _, err := bare.stopChildren("job_x"); err == nil {
		t.Fatal("stopChildren: want error")
	}
	if handle := bare.directDelegateJobForChild("child_x"); handle != "" {
		t.Fatalf("directDelegateJobForChild = %q, want empty", handle)
	}
	if child := bare.delegateChildSessionToCascade("job_x"); child != nil {
		t.Fatalf("delegateChildSessionToCascade = %v, want nil", child)
	}
}

// The store-backed nested helpers surface a corrupt store rather than silently
// returning empty results.
func TestW2Dlg_NestedHelpers_StoreLoadError(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)
	w2dlg_corruptSessionLog(t, s)

	if _, err := s.stopChildren("job_del"); err == nil {
		t.Fatal("stopChildren corrupt store: want error")
	}
	if handle := s.directDelegateJobForChild("child_x"); handle != "" {
		t.Fatalf("directDelegateJobForChild corrupt store = %q, want empty", handle)
	}
	if child := s.delegateChildSessionToCascade("job_del"); child != nil {
		t.Fatalf("delegateChildSessionToCascade corrupt store = %v, want nil", child)
	}
}

// resolveDescendantJobOwner and directChildOwningDescendant are safe no-ops on a
// nil receiver.
func TestW2Dlg_DescendantResolvers_NilReceiver(t *testing.T) {
	t.Parallel()
	var nilSess *Session
	if _, _, _, _, ok := nilSess.resolveDescendantJobOwner("job_x"); ok {
		t.Fatal("resolveDescendantJobOwner(nil): want not-found")
	}
	if child := nilSess.directChildOwningDescendant("job_x"); child != nil {
		t.Fatalf("directChildOwningDescendant(nil) = %v, want nil", child)
	}
}

// stopDelegateSubtree is a no-op for a nil child session and for a child session
// with no live job manager.
func TestW2Dlg_StopDelegateSubtree_Guards(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)

	if recs, err := s.stopDelegateSubtree(nil); recs != nil || err != nil {
		t.Fatalf("nil child = (%v, %v), want (nil, nil)", recs, err)
	}
	if recs, err := s.stopDelegateSubtree(&Session{}); recs != nil || err != nil {
		t.Fatalf("jm-less child = (%v, %v), want (nil, nil)", recs, err)
	}
}

// liveSubagentSession returns nil for a nil manager and for an unknown id.
func TestW2Dlg_LiveSubagentSession_Guards(t *testing.T) {
	t.Parallel()
	if got := liveSubagentSession(nil, "id"); got != nil {
		t.Fatalf("nil mgr = %v, want nil", got)
	}
	s := w2dlg_session(t)
	if got := liveSubagentSession(s.subagents, "unknown_child"); got != nil {
		t.Fatalf("unknown id = %v, want nil", got)
	}
}

// walkDescendantJobs surfaces the caller's own (depth-0) store failure rather
// than reporting an empty success.
func TestW2Dlg_WalkDescendantJobs_Depth0Error(t *testing.T) {
	t.Parallel()
	if _, err := (&Session{}).walkDescendantJobs(listFilter{}); err == nil {
		t.Fatal("walkDescendantJobs depth-0 no jobmanager: want error")
	}

	s := w2dlg_session(t)
	w2dlg_corruptSessionLog(t, s)
	if _, err := s.walkDescendantJobs(listFilter{}); err == nil {
		t.Fatal("walkDescendantJobs depth-0 corrupt store: want error")
	}
}

// A delegate-cascade lookup returns nil when the named job is not the caller's
// own delegate job (here it is entirely unknown).
func TestW2Dlg_DelegateChildSessionToCascade_UnknownJob(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)
	if child := s.delegateChildSessionToCascade("job_unknown"); child != nil {
		t.Fatalf("unknown job = %v, want nil", child)
	}
}
