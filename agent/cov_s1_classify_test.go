package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// The runtime-alias / handle-shape arms of classifyRestoredWatchSendTarget
// resolve without touching the store.
func TestS1Cov_classifyRestoredWatchSendTarget_EarlyArms(t *testing.T) {
	// A nil job manager classifies as busy (keep the frame pending).
	if class, _ := (&Session{}).classifyRestoredWatchSendTarget("dlg_x"); class != watchSendBusy {
		t.Fatalf("nil jobManager → %v, want busy", class)
	}

	jm := newTestJM(t)
	s := &Session{id: jm.sessionID, jobManager: jm}

	tests := []struct {
		name       string
		target     string
		wantClass  watchSendDeliveryClass
		wantReason string
	}{
		{"caller_alias", runtimeMessageAliasCaller, watchSendDelivered, ""},
		{"empty", "   ", watchSendHardFailure, "target is required"},
		{"unsupported_main", "main", watchSendHardFailure, "not found"},
		{"unsupported_watched", runtimeMessageAliasWatched, watchSendHardFailure, "not found"},
		{"job_handle", "job_abc", watchSendHardFailure, "job_id is a job/turn handle"},
		{"unknown_delegate", "dlg_ghost", watchSendHardFailure, "not found"},
		{"unknown_job_token", "somejob", watchSendHardFailure, "target_not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class, reason := s.classifyRestoredWatchSendTarget(tc.target)
			if class != tc.wantClass {
				t.Fatalf("target %q: class = %v, want %v (reason %q)", tc.target, class, tc.wantClass, reason)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("target %q: reason = %q, want containing %q", tc.target, reason, tc.wantReason)
			}
		})
	}
}

// A delegate owned by a different session is not controllable from here.
func TestS1Cov_classifyRestoredWatchSendTarget_NotControllable(t *testing.T) {
	jm := newTestJM(t)
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_other",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "child_o",
			TranscriptRef:    encodeRef("", "child_o"),
			OwnerSessionID:   "SOMEONE_ELSE",
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_1",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate: %v", err)
	}
	s := &Session{id: jm.sessionID, jobManager: jm}
	class, reason := s.classifyRestoredWatchSendTarget("dlg_other")
	if class != watchSendHardFailure || !strings.Contains(reason, "not_controllable") {
		t.Fatalf("class/reason = %v/%q, want hard failure not_controllable", class, reason)
	}
}

// A delegate whose concrete job is owned by a descendant session is not
// controllable from the owning session.
func TestS1Cov_classifyRestoredWatchSendTarget_JobOwnedByDescendant(t *testing.T) {
	jm := newTestJM(t)
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_desc",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "child_d",
			TranscriptRef:    encodeRef("", "child_d"),
			OwnerSessionID:   jm.sessionID,
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_1",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            "job_desc",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_desc",
		OwnerSessionID:   "DESCENDANT",
		VisibleToSession: jm.sessionID,
		TranscriptRef:    encodeRef("", "child_d"),
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("append job: %v", err)
	}
	s := &Session{id: jm.sessionID, jobManager: jm}
	class, reason := s.classifyRestoredWatchSendTarget("dlg_desc")
	if class != watchSendHardFailure || !strings.Contains(reason, "not_controllable") {
		t.Fatalf("class/reason = %v/%q, want hard failure not_controllable", class, reason)
	}
}

// A corrupt delegate log classifies as busy (keep the frame pending), never a
// hard failure.
func TestS1Cov_classifyRestoredWatchSendTarget_LoadDelegatesError(t *testing.T) {
	jm := newTestJM(t)
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_corrupt",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "child_c",
			TranscriptRef:    encodeRef("", "child_c"),
			OwnerSessionID:   jm.sessionID,
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_1",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate: %v", err)
	}
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))
	s := &Session{id: jm.sessionID, jobManager: jm}
	if class, _ := s.classifyRestoredWatchSendTarget("dlg_corrupt"); class != watchSendBusy {
		t.Fatalf("corrupt log → %v, want busy", class)
	}
}

// A delegate with no job history yet is not resumable.
func TestS1Cov_classifyRestoredWatchSendTarget_NoJobHistory(t *testing.T) {
	jm := newTestJM(t)
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_nohist",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "child_n",
			TranscriptRef:    encodeRef("", "child_n"),
			OwnerSessionID:   jm.sessionID,
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_1",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate: %v", err)
	}
	s := &Session{id: jm.sessionID, jobManager: jm}
	class, reason := s.classifyRestoredWatchSendTarget("dlg_nohist")
	if class != watchSendHardFailure || !strings.Contains(reason, "no job history") {
		t.Fatalf("class/reason = %v/%q, want hard failure no-job-history", class, reason)
	}
}
