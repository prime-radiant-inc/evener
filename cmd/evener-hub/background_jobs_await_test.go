//go:build browserguard

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

const backgroundJobsAwaitRef = "local:owner"

type backgroundJobsAwaitRPC struct {
	client  *appwire.Client
	calls   func() []remoteHubCall
	push    func(string, any) error
	ctx     context.Context
	failure *appwire.WireError
}

// The scripted boundary is the RPC peer. The waiter, pagination and client
// receive loop remain real, including ordered response cuts after notifications.
func newBackgroundJobsAwaitRPC(t *testing.T, page func(int, string) appwire.SessionJobsResponse) *backgroundJobsAwaitRPC {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second) // Tripwire, not synchronization.
	t.Cleanup(cancel)
	rpc := &backgroundJobsAwaitRPC{ctx: ctx}
	reads := 0
	rpc.client, rpc.calls, rpc.push = newPushableScriptedRemoteHub(t, func(method string, raw json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodEvenerThreadJobsList:
			var params appwire.SessionActivityListParams
			if err := json.Unmarshal(raw, &params); err != nil {
				t.Error(err)
			}
			if params.Ref != backgroundJobsAwaitRef || params.Scope != "" {
				t.Errorf("waiter changed its session query: %+v", params)
			}
			reads++
			if rpc.failure != nil {
				return *rpc.failure
			}
			return page(reads, params.Cursor)
		case appwire.MethodEvenerThreadActivityRead:
			return appwire.SessionActivitySummary{}
		default:
			t.Errorf("unexpected waiter request %q", method)
			return appwire.WireError{Code: -32601, Message: "unexpected request"}
		}
	})
	return rpc
}

func (rpc *backgroundJobsAwaitRPC) queue(t *testing.T, notes ...appwire.Notification) {
	t.Helper()
	for _, note := range notes {
		if err := rpc.push(note.Method, note.Params); err != nil {
			t.Fatal(err)
		}
	}
	// The response is behind these notices in the same ordered receive stream.
	// Awaiting it proves the whole burst reached the client before continuing.
	if _, err := rpc.client.ThreadActivityRead(rpc.ctx, appwire.SessionActivityReadParams{Ref: backgroundJobsAwaitRef}); err != nil {
		t.Fatal(err)
	}
}

func (rpc *backgroundJobsAwaitRPC) wantCursors(t *testing.T, want []string) {
	t.Helper()
	var cursors []string
	for _, params := range scriptedRemoteHubParams[appwire.SessionActivityListParams](t, rpc.calls(), appwire.MethodEvenerThreadJobsList) {
		cursors = append(cursors, params.Cursor)
	}
	if !reflect.DeepEqual(cursors, want) {
		t.Errorf("Jobs reads = %q, want %q", cursors, want)
	}
}

func backgroundJobsAwaitPage(id, next string, terminal bool) appwire.SessionJobsResponse {
	return appwire.SessionJobsResponse{
		Context: appwire.SessionActivityContext{Ref: backgroundJobsAwaitRef, SessionID: "owner", RootRef: backgroundJobsAwaitRef, AncestryKnown: true, Epoch: "epoch", Availability: "live"},
		Scope:   appwire.SessionActivityScopeSession,
		Page:    appwire.SessionActivityPage{Complete: next == "", NextCursor: next},
		Jobs:    []appwire.JobActivityJob{{JobID: id, OwnerRef: backgroundJobsAwaitRef, Background: true, Terminal: terminal}},
	}
}

func backgroundJobsAwaitNotice(method, params string) appwire.Notification {
	return appwire.Notification{Method: method, Params: json.RawMessage(params)}
}

func TestBackgroundJobsAwaitReadyDoesNotConsumeNotifications(t *testing.T) {
	t.Parallel()
	rpc := newBackgroundJobsAwaitRPC(t, func(int, string) appwire.SessionJobsResponse {
		return backgroundJobsAwaitPage("ready", "", true)
	})
	rpc.queue(t, backgroundJobsAwaitNotice("evener/thread/activity/changed", `{"threadId":"owner","ref":"local:owner","sessionId":"owner","resources":["jobs"]}`))
	rows := backgroundJobsAwait(rpc.ctx, t, rpc.client, backgroundJobsAwaitRef, func(rows []appwire.JobActivityJob) bool {
		return len(rows) == 1 && rows[0].Terminal
	})
	if len(rows) != 1 || rows[0].JobID != "ready" || len(rpc.client.Notifications()) != 1 {
		t.Fatalf("ready result or pending notification changed: %+v", rows)
	}
	rpc.wantCursors(t, []string{""})
}

func TestBackgroundJobsAwaitCoalescesQueuedWakeups(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"evener/thread/activity/changed", "evener/thread/resync"} {
		t.Run(method, func(t *testing.T) {
			rpc := newBackgroundJobsAwaitRPC(t, func(int, string) appwire.SessionJobsResponse {
				return backgroundJobsAwaitPage("held", "", false)
			})
			walks := 0
			backgroundJobsAwait(rpc.ctx, t, rpc.client, backgroundJobsAwaitRef, func([]appwire.JobActivityJob) bool {
				walks++
				if walks == 1 {
					note := backgroundJobsAwaitNotice(method, `{"threadId":"owner","ref":"local:owner","sessionId":"owner","resources":["jobs"]}`)
					rpc.queue(t, note, note, note)
					return false
				}
				// Keep the readiness predicate false while stale wakeups remain.
				// Only the waiter's actual consumption can satisfy this condition.
				return len(rpc.client.Notifications()) == 0
			})
			rpc.wantCursors(t, []string{"", ""})
		})
	}
}

func TestBackgroundJobsAwaitKeepsChangesDuringPageWalk(t *testing.T) {
	t.Parallel()
	var rpc *backgroundJobsAwaitRPC
	rpc = newBackgroundJobsAwaitRPC(t, func(read int, cursor string) appwire.SessionJobsResponse {
		switch read {
		case 1:
			return backgroundJobsAwaitPage("first", "older", false)
		case 2:
			if err := rpc.push("evener/thread/activity/changed", json.RawMessage(`{"threadId":"owner","ref":"local:owner","sessionId":"owner","resources":["jobs"]}`)); err != nil {
				t.Error(err)
			}
			return backgroundJobsAwaitPage("later", "", false)
		case 3:
			return backgroundJobsAwaitPage("first", "fresh-older", true)
		case 4:
			if err := rpc.push("evener/thread/resync", json.RawMessage(`{"threadId":"owner","ref":"local:owner"}`)); err != nil {
				t.Error(err)
			}
			return backgroundJobsAwaitPage("later", "", false)
		case 5:
			return backgroundJobsAwaitPage("first", "latest-older", true)
		default:
			return backgroundJobsAwaitPage("later", "", true)
		}
	})
	rows := backgroundJobsAwait(rpc.ctx, t, rpc.client, backgroundJobsAwaitRef, func(rows []appwire.JobActivityJob) bool {
		return len(rows) == 2 && rows[0].Terminal && rows[1].Terminal
	})
	if len(rows) != 2 || rows[0].JobID != "first" || rows[1].JobID != "later" {
		t.Fatalf("later-page membership lost: %+v", rows)
	}
	rpc.wantCursors(t, []string{"", "older", "", "fresh-older", "", "latest-older"})
}

func TestBackgroundJobsAwaitNotificationSelection(t *testing.T) {
	t.Parallel()
	scope := appwire.SessionActivityContext{Ref: "local:resolved", SessionID: "owner"}
	for _, test := range []struct {
		name, method, params string
		want                 bool
	}{
		{"jobs", "evener/thread/activity/changed", `{"ref":"local:owner","sessionId":"owner","resources":["jobs"]}`, true},
		{"resolved ref", "evener/thread/activity/changed", `{"ref":"local:resolved","sessionId":"owner","resources":["summary","jobs"]}`, true},
		{"another ref", "evener/thread/activity/changed", `{"ref":"local:other","sessionId":"owner","resources":["jobs"]}`, false},
		{"descendant", "evener/thread/activity/changed", `{"ref":"local:owner","sessionId":"child","resources":["jobs"]}`, false},
		{"summary only", "evener/thread/activity/changed", `{"ref":"local:owner","sessionId":"owner","resources":["summary"]}`, false},
		{"other collections", "evener/thread/activity/changed", `{"ref":"local:owner","sessionId":"owner","resources":["delegates","watches"]}`, false},
		{"lifecycle", "thread/status/changed", `{"ref":"local:owner","sessionId":"owner","resources":["jobs"]}`, false},
		{"resync", "evener/thread/resync", `{"ref":"local:owner","threadId":"replacement"}`, true},
		{"resolved resync", "evener/thread/resync", `{"ref":"local:resolved","threadId":"replacement"}`, true},
		{"another resync", "evener/thread/resync", `{"ref":"local:other","threadId":"owner"}`, false},
		{"invalid change", "evener/thread/activity/changed", `{`, false},
		{"invalid resync", "evener/thread/resync", `{`, false},
		{"missing change", "evener/thread/activity/changed", `null`, false},
		{"missing resync", "evener/thread/resync", `null`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := backgroundJobsInvalidated(backgroundJobsAwaitNotice(test.method, test.params), backgroundJobsAwaitRef, scope); got != test.want {
				t.Errorf("notification selected = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBackgroundJobsAwaitPreservesReadFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"RPC", "page issue", "missing cursor", "repeated cursor"} {
		t.Run(name, func(t *testing.T) {
			rpc := newBackgroundJobsAwaitRPC(t, func(read int, cursor string) appwire.SessionJobsResponse {
				page := backgroundJobsAwaitPage("held", "next", false)
				switch name {
				case "page issue":
					page.Page.Issues = []appwire.SessionActivityIssue{{Ref: backgroundJobsAwaitRef, Code: "unavailable"}}
				case "missing cursor":
					page.Page.NextCursor = ""
				case "repeated cursor":
					if read > 1 {
						page.Page.NextCursor = cursor
					}
				}
				return page
			})
			if name == "RPC" {
				rpc.failure = &appwire.WireError{Code: -32000, Message: "read unavailable"}
			}
			readyCalls := 0
			rows, err := backgroundJobsReadUntil(rpc.ctx, rpc.client, backgroundJobsAwaitRef, func([]appwire.JobActivityJob) bool {
				readyCalls++
				return true
			})
			if err == nil || rows != nil || readyCalls != 0 {
				t.Fatalf("failed read reached readiness: rows=%+v, error=%v, readiness calls=%d", rows, err, readyCalls)
			}
			switch name {
			case "RPC":
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != -32000 {
					t.Fatalf("RPC failure lost: %v", err)
				}
			case "page issue":
				if !strings.Contains(err.Error(), "unavailable") {
					t.Fatalf("page issue lost: %v", err)
				}
			default:
				if err.Error() != "actual Jobs cursor does not advance" {
					t.Fatalf("cursor failure lost: %v", err)
				}
			}
			want := []string{""}
			if name == "repeated cursor" {
				want = []string{"", "next"}
			}
			rpc.wantCursors(t, want)
		})
	}
}

func TestBackgroundJobsAwaitPreservesWaitFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"cancellation", "closed subscription"} {
		t.Run(name, func(t *testing.T) {
			rpc := newBackgroundJobsAwaitRPC(t, func(int, string) appwire.SessionJobsResponse {
				return backgroundJobsAwaitPage("held", "", false)
			})
			ctx, cancel := context.WithCancel(rpc.ctx)
			defer cancel()
			rows, err := backgroundJobsReadUntil(ctx, rpc.client, backgroundJobsAwaitRef, func([]appwire.JobActivityJob) bool {
				if name == "cancellation" {
					cancel()
				} else if err := rpc.client.Close(); err != nil {
					t.Fatal(err)
				}
				return false
			})
			if rows != nil || err == nil {
				t.Fatalf("wait failure lost: rows=%+v, error=%v", rows, err)
			}
			if name == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if name == "closed subscription" && err.Error() != "actual Jobs subscription closed" {
				t.Fatalf("subscription closure lost: %v", err)
			}
			rpc.wantCursors(t, []string{""})
		})
	}
}
