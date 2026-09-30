package appsource

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSessionActivityRemoteReferences(t *testing.T) {
	source := &RemoteHubSource{id: "remote"}
	context := appwire.SessionActivityContext{Ref: "local:child", SessionID: "child", RootRef: "local:root", ParentRef: "local:root", Ancestors: []appwire.SessionActivityAncestor{{Ref: "local:root", SessionID: "root"}}, Epoch: "opaque"}
	page := appwire.SessionActivityPage{NextCursor: "opaque", Issues: []appwire.SessionActivityIssue{{Ref: "local:grandchild", Code: "unavailable"}}}
	wantContext := context
	wantContext.Ref, wantContext.RootRef, wantContext.ParentRef = "remote:child", "remote:root", "remote:root"
	wantContext.Ancestors = []appwire.SessionActivityAncestor{{Ref: "remote:root", SessionID: "root"}}
	type translationCase struct {
		name  string
		value any
		check func(*testing.T)
	}
	tests := []translationCase{}
	summary := appwire.SessionActivitySummary{Context: context}
	tests = append(tests, translationCase{"summary", &summary, func(t *testing.T) {
		if !reflect.DeepEqual(summary.Context, wantContext) {
			t.Fatalf("context = %+v", summary.Context)
		}
	}})
	delegates := appwire.SessionDelegatesResponse{Context: context, Page: page, Delegates: []appwire.SessionDelegate{{OwnerRef: "local:child", RootRef: "local:root", ChildRef: "local:grandchild"}}}
	tests = append(tests, translationCase{"delegates", &delegates, func(t *testing.T) {
		if !reflect.DeepEqual(delegates.Context, wantContext) || delegates.Page.Issues[0].Ref != "remote:grandchild" || delegates.Delegates[0].OwnerRef != "remote:child" || delegates.Delegates[0].RootRef != "remote:root" || delegates.Delegates[0].ChildRef != "remote:grandchild" || delegates.Page.NextCursor != "opaque" {
			t.Fatalf("delegates = %+v", delegates)
		}
	}})
	jobs := appwire.SessionJobsResponse{Context: context, Page: page, Jobs: []appwire.JobActivityJob{{OwnerRef: "local:child", TranscriptRef: "local:child", JobID: "opaque"}}}
	tests = append(tests, translationCase{"jobs", &jobs, func(t *testing.T) {
		if !reflect.DeepEqual(jobs.Context, wantContext) || jobs.Page.Issues[0].Ref != "remote:grandchild" || jobs.Jobs[0].OwnerRef != "remote:child" || jobs.Jobs[0].TranscriptRef != "remote:child" || jobs.Jobs[0].JobID != "opaque" {
			t.Fatalf("jobs = %+v", jobs)
		}
	}})
	watches := appwire.SessionWatchesResponse{Context: context, Page: page, Watches: []appwire.SessionWatch{{OwnerRef: "local:root", ReceiverRef: "local:root", Watch: appwire.EvenerWatchInfo{ID: "opaque", Source: "shell-1"}}}}
	tests = append(tests, translationCase{"watches", &watches, func(t *testing.T) {
		if !reflect.DeepEqual(watches.Context, wantContext) || watches.Page.Issues[0].Ref != "remote:grandchild" || watches.Watches[0].OwnerRef != "remote:root" || watches.Watches[0].ReceiverRef != "remote:root" || watches.Watches[0].Watch.Source != "shell-1" {
			t.Fatalf("watches = %+v", watches)
		}
	}})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := source.translateOut(tc.value); err != nil {
				t.Fatal(err)
			}
			tc.check(t)
		})
	}
}
