package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A delegate's projected thread carries the quota summary its live
// announcement recorded (the record-events path seeds it onto the row). The
// list path samples the delegate's LIVE wait through the resolver the way it
// samples its watches, and a delegate that is no longer waiting - its Stop
// cleared the live wait before the execution-end event projected - must not
// keep the cached summary on its list row: an empty resolver answer for an
// installed resolver clears it, the same way a non-empty answer overwrites
// it. The stored projection keeps its summary for thread/read, which serves
// the projected thread as recorded.
func TestThreadListClearsADelegatesEndedQuotaWait(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	resumesAt := int64(4_102_400_000)
	srv.mu.Lock()
	srv.appDescendants["child"] = &appDescendantProjection{thread: appwire.Thread{
		ID: "child", SessionID: "child", Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:child", Kind: "subagent",
			Failure: &appwire.ThreadFailure{Title: "Usage limit reached", ResumesAt: resumesAt}},
	}}
	srv.mu.Unlock()
	// The live resolver answers for the whole page and finds no wait: the
	// delegate's Stop cleared it.
	srv.SetDescendantQuotaWaitsFunc(func([]string) map[string]*appwire.ThreadFailure {
		return nil
	})

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if len(response.Data) != 2 || response.Data[1].ID != "child" {
		t.Fatalf("thread list = %+v, want root then child", response.Data)
	}
	if got := response.Data[1].Evener.Failure; got != nil {
		t.Fatalf("child list row failure = %+v, want the ended wait cleared from the row", got)
	}
	// The stored projection keeps its recorded summary: thread/read serves the
	// projection as it stands, and a read between the Stop and the
	// execution-end event still reports the wait the announcement recorded.
	read, ok := srv.appThreadForID("child")
	if !ok {
		t.Fatal("appThreadForID(child) = false")
	}
	if read.Evener.Failure == nil || read.Evener.Failure.ResumesAt != resumesAt {
		t.Fatalf("child read failure = %+v, want the stored summary kept for thread/read", read.Evener.Failure)
	}
}
