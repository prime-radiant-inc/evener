package hub

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// The source keeps distinct session-owned rows. These tests check hub routing
// and refusal; agent/session_tools_hub_job_stop_test.go checks the real owner.
type qualifiedJobStopSource struct {
	*scriptedAppSource
	contexts     map[string]appwire.SessionActivityContext
	jobs         map[string]appwire.JobActivityJob
	delegates    map[string]string
	lastOwnerRef string
}

func (s *qualifiedJobStopSource) ThreadActivityRead(_ context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	context, ok := s.contexts[params.Ref]
	if !ok {
		return appwire.SessionActivitySummary{}, appwire.SessionUnavailable("thread unavailable")
	}
	return appwire.SessionActivitySummary{Context: context}, nil
}

func (s *qualifiedJobStopSource) JobGet(_ context.Context, params appwire.JobsGetParams) (appwire.JobsGetResponse, error) {
	job, ok := s.jobs[params.JobID]
	if !ok {
		return appwire.JobsGetResponse{}, appwire.InvalidParams("job not found")
	}
	return appwire.JobsGetResponse{Data: job}, nil
}

func (s *qualifiedJobStopSource) StopJob(_ context.Context, request appwire.SessionStopRequest) (appwire.SessionStopResponse, error) {
	if request.OwnedDelegate != nil {
		s.lastOwnerRef = request.OwnedDelegate.Ref
		id := request.OwnedDelegate.DelegateID
		s.delegates[id] = "stopping"
		return appwire.SessionStopResponse{Job: &appwire.SessionJobStopResponse{JobID: id, Status: "stopping", Outcome: "stop_requested"}}, nil
	}
	if request.Job == nil {
		return appwire.SessionStopResponse{}, appwire.InvalidParams("job required")
	}
	s.lastOwnerRef = request.Job.Ref
	job := s.jobs[request.Job.JobID]
	job.Status = "cancelled"
	job.Terminal = true
	s.jobs[request.Job.JobID] = job
	return appwire.SessionStopResponse{Job: &appwire.SessionJobStopResponse{JobID: job.JobID, Status: job.Status, Outcome: "cancelled_by_request"}}, nil
}

func TestManagerStopOwnedDelegateResolvedOwnerRef(t *testing.T) {
	source := &qualifiedJobStopSource{
		scriptedAppSource: &scriptedAppSource{id: "local"},
		contexts: map[string]appwire.SessionActivityContext{
			"local:root":  {Ref: "local:root", SessionID: "root", RootRef: "local:root", AncestryKnown: true},
			"local:child": {Ref: "local:child", SessionID: "child", RootRef: "local:root", DelegateID: "dlg_child", AncestryKnown: true},
		},
		delegates: map[string]string{"dlg_child": "running", "dlg_other": "running"},
	}
	registry := appsource.NewRegistry()
	registry.Add(source)
	stop := func(id string) (appwire.SessionStopResponse, error) {
		return (&sessionOperations{cfg: hubcore.WebConfig{}, sources: registry}).Stop(t.Context(), appwire.SessionStopRequest{
			Mode: "owned_delegate", OwnedDelegate: &appwire.SessionOwnedDelegateStopParams{Ref: "local:child", DelegateID: id},
		})
	}
	before := source.delegates["dlg_other"]
	if _, err := stop("dlg_other"); err == nil || source.delegates["dlg_other"] != before || source.lastOwnerRef != "" {
		t.Fatalf("foreign descendant stop changed state: err=%v delegates=%v", err, source.delegates)
	}
	result, err := stop("dlg_child")
	if err != nil || result.Job == nil || result.Job.JobID != "dlg_child" || source.delegates["dlg_child"] != "stopping" || source.lastOwnerRef != "local:root" {
		t.Fatalf("owned delegate stop=%+v err=%v owner=%q", result, err, source.lastOwnerRef)
	}
}

func TestManagerStopJobResolvedOwnerRef(t *testing.T) {
	for _, tc := range []struct {
		name, ref, jobID, ownerRef string
		accepted                   bool
	}{
		{"own", "local:manager", "job_manager", "local:manager", true},
		{"other", "local:worker", "job_worker", "local:worker", true},
		{"owned_delegate_child", "local:child", "job_child", "local:manager", true},
		{"foreign_job", "local:manager", "job_worker", "", false},
		{"unknown_job", "local:manager", "job_unknown", "", false},
		{"terminal_job", "local:manager", "job_terminal", "", false},
		{"non_shell", "local:manager", "job_delegate", "", false},
		{"watch", "local:manager", "watch_a", "", false},
		{"wrong_prefix", "local:manager", "dlg_child", "", false},
		{"unreachable_session", "local:unreachable", "job_manager", "", false},
		{"unreachable_source", "other:worker", "job_worker", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &qualifiedJobStopSource{
				scriptedAppSource: &scriptedAppSource{id: "local"},
				contexts: map[string]appwire.SessionActivityContext{
					"local:manager": {Ref: "local:manager", SessionID: "manager", RootRef: "local:manager", AncestryKnown: true},
					"local:worker":  {Ref: "local:worker", SessionID: "worker", RootRef: "local:worker", AncestryKnown: true},
					"local:child":   {Ref: "local:child", SessionID: "child", RootRef: "local:manager", DelegateID: "dlg_child", AncestryKnown: true},
				},
				jobs: map[string]appwire.JobActivityJob{
					"job_manager":  {JobID: "job_manager", OwnerRef: "local:manager", OwnerSessionID: "manager", Type: "shell", Status: "running"},
					"job_worker":   {JobID: "job_worker", OwnerRef: "local:worker", OwnerSessionID: "worker", Type: "shell", Status: "running"},
					"job_child":    {JobID: "job_child", OwnerRef: "local:child", OwnerSessionID: "child", Type: "shell", Status: "running"},
					"job_terminal": {JobID: "job_terminal", OwnerRef: "local:manager", OwnerSessionID: "manager", Type: "shell", Status: "completed", Terminal: true},
					"job_delegate": {JobID: "job_delegate", OwnerRef: "local:manager", OwnerSessionID: "manager", Type: "delegate", Status: "running"},
				},
			}
			registry := appsource.NewRegistry()
			registry.Add(source)
			before, err := json.Marshal(source.jobs)
			if err != nil {
				t.Fatal(err)
			}
			result, err := (&sessionOperations{cfg: hubcore.WebConfig{}, sources: registry}).Stop(t.Context(), appwire.SessionStopRequest{Mode: "job", Job: &appwire.SessionJobStopParams{Ref: tc.ref, JobID: tc.jobID}})
			if tc.accepted {
				if err != nil || result.Job == nil || result.Job.Status != "cancelled" || source.lastOwnerRef != tc.ownerRef || !source.jobs[tc.jobID].Terminal {
					t.Fatalf("resolved stop=%+v err=%v owner=%q jobs=%v", result, err, source.lastOwnerRef, source.jobs)
				}
				return
			}
			if err == nil || source.lastOwnerRef != "" {
				t.Fatalf("rejected stop=%+v err=%v owner=%q", result, err, source.lastOwnerRef)
			}
			after, err := json.Marshal(source.jobs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected stop changed statuses: before=%s after=%s", before, after)
			}
		})
	}
}
