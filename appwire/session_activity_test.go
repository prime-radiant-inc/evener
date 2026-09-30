package appwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionActivityMethodDiscovery(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"evener/thread/activity/read",
		"evener/thread/delegates/list",
		"evener/thread/jobs/list",
		"evener/thread/watches/list",
	} {
		var found *MethodSpec
		for i := range Methods {
			if Methods[i].Name == name {
				found = &Methods[i]
				break
			}
		}
		if found == nil {
			t.Errorf("method %s is absent from discovery", name)
			continue
		}
		if found.Scope != ScopeBoth || reflect.TypeOf(found.Result).Kind() != reflect.Struct {
			t.Errorf("method %s must expose a concrete response through daemon and hub: %+v", name, found)
		}
	}
}

func TestSessionActivityKnownEmptyArrays(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value any
		field string
	}{
		{"delegates", SessionDelegatesResponse{}, "delegates"},
		{"jobs", SessionJobsResponse{}, "jobs"},
		{"watches", SessionWatchesResponse{}, "watches"},
		{"page issues", SessionActivityPage{}, "issues"},
		{"ancestors", SessionActivityContext{}, "ancestors"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			var rows []json.RawMessage
			if err := json.Unmarshal(fields[tc.field], &rows); err != nil {
				t.Fatal(err)
			}
			if rows == nil || len(rows) != 0 {
				t.Fatalf("%s must be an explicit empty array: %s", tc.field, data)
			}
		})
	}
}

func TestSessionActivityTypedRows(t *testing.T) {
	t.Parallel()
	var delegates SessionDelegatesResponse
	if err := json.Unmarshal([]byte(`{"context":{"ref":"remote:child","sessionId":"child","rootRef":"remote:root","parentRef":"remote:root","delegateId":"delegate-1","ancestors":[{"ref":"remote:root","sessionId":"root","title":"Root"}],"epoch":"source-1","availability":"retained"},"scope":"subtree","page":{"complete":false,"nextCursor":"opaque","issues":[{"ref":"remote:grandchild","code":"unavailable"}]},"delegates":[{"delegateId":"delegate-1","ownerRef":"remote:root","rootRef":"remote:root","childRef":"remote:child","description":"work","task":"inspect","type":"agent","lifecycle":"idle","phase":"done","status":"completed","outcome":"success","terminal":true,"resumable":true,"usage":{"inputTokens":42},"worktree":{"path":"/scratch/work"}}]}`), &delegates); err != nil {
		t.Fatal(err)
	}
	if delegates.Scope != SessionActivityScopeSubtree || delegates.Context.SessionID != "child" || delegates.Context.ParentRef != "remote:root" || len(delegates.Context.Ancestors) != 1 || delegates.Context.Ancestors[0].Title != "Root" {
		t.Fatalf("context = %+v", delegates.Context)
	}
	if delegates.Page.Complete || delegates.Page.NextCursor != "opaque" || len(delegates.Page.Issues) != 1 || delegates.Page.Issues[0].Code != "unavailable" {
		t.Fatalf("page = %+v", delegates.Page)
	}
	if len(delegates.Delegates) != 1 {
		t.Fatalf("delegates = %+v", delegates.Delegates)
	}
	row := delegates.Delegates[0]
	if row.DelegateID != "delegate-1" || row.ChildRef != "remote:child" || !row.Terminal || !row.Resumable || row.Usage == nil || row.Worktree == nil || row.Worktree.Path != "/scratch/work" {
		t.Fatalf("delegate = %+v", row)
	}
	var jobs SessionJobsResponse
	if err := json.Unmarshal([]byte(`{"jobs":[{"jobId":"shell-1","ownerRef":"remote:child","type":"shell","status":"running","outputBytes":9007199254740993}]}`), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].JobID != "shell-1" || jobs.Jobs[0].OwnerRef != "remote:child" || jobs.Jobs[0].OutputBytes != 9007199254740993 {
		t.Fatalf("jobs = %+v", jobs.Jobs)
	}
	var watches SessionWatchesResponse
	if err := json.Unmarshal([]byte(`{"watches":[{"ownerRef":"remote:root","receiverRef":"remote:root","state":"ended","watch":{"id":"watch-1","source":"shell-1","deliveries":3,"active":false,"endReason":"runtime_lost","cadence":[{"kind":"every","seconds":30}]}}]}`), &watches); err != nil {
		t.Fatal(err)
	}
	if len(watches.Watches) != 1 || watches.Watches[0].ReceiverRef != "remote:root" || watches.Watches[0].State != SessionWatchStateEnded || watches.Watches[0].Watch.ID != "watch-1" || watches.Watches[0].Watch.Deliveries != 3 || watches.Watches[0].Watch.EndReason != "runtime_lost" || len(watches.Watches[0].Watch.Cadence) != 1 {
		t.Fatalf("watches = %+v", watches.Watches)
	}
}

func TestSessionActivityUnknownCounts(t *testing.T) {
	t.Parallel()
	var summary SessionActivitySummary
	if err := json.Unmarshal([]byte(`{"scope":"session","delegates":{"known":false},"jobs":{"known":true,"total":0,"active":0,"failed":0,"completed":0},"watches":{"known":true,"total":3,"active":1,"failed":1,"completed":1}}`), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Delegates.Known || !summary.Jobs.Known || summary.Jobs.Total != 0 || summary.Watches.Active != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSessionActivityChangedDiscovery(t *testing.T) {
	t.Parallel()
	for _, notification := range Notifications {
		if notification.Name != "evener/thread/activity/changed" {
			continue
		}
		if reflect.TypeOf(notification.Payload) != reflect.TypeFor[SessionActivityChangedParams]() {
			t.Fatalf("payload = %T", notification.Payload)
		}
		var changed SessionActivityChangedParams
		if err := json.Unmarshal([]byte(`{"threadId":"thread","ref":"remote:alias","sessionId":"child","resources":["summary","delegates","jobs","watches"]}`), &changed); err != nil {
			t.Fatal(err)
		}
		if changed.ThreadID != "thread" || changed.Ref != "remote:alias" || changed.SessionID != "child" || !reflect.DeepEqual(changed.Resources, []SessionActivityResource{SessionActivityResourceSummary, SessionActivityResourceDelegates, SessionActivityResourceJobs, SessionActivityResourceWatches}) {
			t.Fatalf("changed = %+v", changed)
		}
		return
	}
	t.Fatal("activity change notification is absent from discovery")
}

func TestSessionActivityClientScopedReads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		call     func(*Client) (any, error)
		wantType reflect.Type
		result   string
		list     bool
	}{
		{"evener/thread/activity/read", func(c *Client) (any, error) {
			return c.ThreadActivityRead(t.Context(), SessionActivityReadParams{Ref: "remote:child", Scope: SessionActivityScopeSubtree})
		}, reflect.TypeFor[SessionActivitySummary](), `{"context":{"sessionId":"child"},"delegates":{"known":false}}`, false},
		{"evener/thread/delegates/list", func(c *Client) (any, error) {
			return c.ThreadDelegatesList(t.Context(), SessionActivityListParams{Ref: "remote:child", Scope: SessionActivityScopeSubtree, Cursor: "cursor-1", Limit: 17})
		}, reflect.TypeFor[SessionDelegatesResponse](), `{"context":{"sessionId":"child"},"delegates":[],"page":{"complete":true,"issues":[]}}`, true},
		{"evener/thread/jobs/list", func(c *Client) (any, error) {
			return c.ThreadJobsList(t.Context(), SessionActivityListParams{Ref: "remote:child", Scope: SessionActivityScopeSubtree, Cursor: "cursor-1", Limit: 17})
		}, reflect.TypeFor[SessionJobsResponse](), `{"context":{"sessionId":"child"},"jobs":[],"page":{"complete":true,"issues":[]}}`, true},
		{"evener/thread/watches/list", func(c *Client) (any, error) {
			return c.ThreadWatchesList(t.Context(), SessionActivityListParams{Ref: "remote:child", Scope: SessionActivityScopeSubtree, Cursor: "cursor-1", Limit: 17})
		}, reflect.TypeFor[SessionWatchesResponse](), `{"context":{"sessionId":"child"},"watches":[],"page":{"complete":true,"issues":[]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newMemoryTransport()
			client := NewClient(transport)
			client.Start(t.Context())
			done := make(chan struct {
				value any
				err   error
			}, 1)
			go func() {
				value, err := tc.call(client)
				done <- struct {
					value any
					err   error
				}{value, err}
			}()
			var request Message
			select {
			case request = <-transport.writes:
			case <-t.Context().Done():
				t.Fatal("request canceled")
			}
			if request.Request.Method != tc.name {
				t.Fatalf("method = %s", request.Request.Method)
			}
			var params SessionActivityListParams
			if err := json.Unmarshal(request.Request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Ref != "remote:child" || params.Scope != SessionActivityScopeSubtree || (tc.list && (params.Cursor != "cursor-1" || params.Limit != 17)) {
				t.Fatalf("params = %+v", params)
			}
			transport.reads <- ResponseMessage(request.Request.ID, json.RawMessage(tc.result))
			result := <-done
			if result.err != nil {
				t.Fatal(result.err)
			}
			if reflect.TypeOf(result.value) != tc.wantType {
				t.Fatalf("response = %T", result.value)
			}
			data, err := json.Marshal(result.value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Context SessionActivityContext `json:"context"`
			}
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Context.SessionID != "child" {
				t.Fatalf("response = %s", data)
			}
		})
	}
}

func TestSessionActivityChangedRetargetPreservesResolvedSession(t *testing.T) {
	t.Parallel()
	original := SessionActivityChangedParams{ThreadID: "child", Ref: "local:child", SessionID: "child", Resources: []SessionActivityResource{SessionActivityResourceWatches}}
	targeted, ok := any(original).(NotificationTargeted)
	if !ok {
		t.Fatal("activity notification cannot be addressed by existing thread routing")
	}
	changed, ok := any(targeted.WithNotificationTarget("subscription-thread", "remote:workspace")).(SessionActivityChangedParams)
	if !ok || changed.ThreadID != "subscription-thread" || changed.Ref != "remote:workspace" || changed.SessionID != "child" || !reflect.DeepEqual(changed.Resources, original.Resources) {
		t.Fatalf("retargeted payload = %+v", changed)
	}
	if original.ThreadID != "child" || original.Ref != "local:child" {
		t.Fatalf("original payload mutated: %+v", original)
	}
}

func TestSessionActivityCursorErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		err   WireError
		info  ErrorInfo
		retry RetryDisposition
	}{
		{"stale source", SessionActivityCursorStale(), "sessionActivityCursorStale", RetryDispositionAutomatic},
		{"malformed cursor", InvalidParams("invalid cursor"), "invalidParams", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.err)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Code int       `json:"code"`
				Data ErrorData `json:"data"`
			}
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Code != CodeInvalidParams || decoded.Data.EvenerErrorInfo != tc.info || decoded.Data.RetryDisposition != tc.retry {
				t.Fatalf("wire error = %+v", decoded)
			}
		})
	}
}
