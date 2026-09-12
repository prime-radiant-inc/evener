package appwire

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestDaemonLifecycleZeroAndUnknownAreDistinct pins the plan's core wire
// invariant: a zero TimeoutMillis (retirement timer disabled) must survive a
// wire round-trip distinctly from a nil Lifecycle (capability unknown, e.g. an
// older daemon that cannot answer daemon/status).
func TestDaemonLifecycleZeroAndUnknownAreDistinct(t *testing.T) {
	row := DaemonResident{Lifecycle: &DaemonLifecycle{
		Phase: "resident", TimeoutMillis: 0, Blockers: []DaemonBlocker{},
	}}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DaemonResident
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Lifecycle == nil || decoded.Lifecycle.TimeoutMillis != 0 {
		t.Fatalf("disabled became unknown: %s", raw)
	}
	if decoded.Lifecycle.Deadline != "" {
		t.Fatalf("disabled deadline: %s", raw)
	}
	row.Lifecycle = nil
	raw, err = json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	decoded = DaemonResident{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Lifecycle != nil {
		t.Fatal("unknown became enabled/disabled")
	}
}

func requireDaemonJSONEq(t *testing.T, want string, got []byte) {
	t.Helper()
	var wantV, gotV any
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("bad expectation: %v", err)
	}
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Fatalf("wire output does not decode: %v\n%s", err, got)
	}
	if !reflect.DeepEqual(wantV, gotV) {
		t.Fatalf("wire mismatch:\nwant %s\ngot  %s", want, got)
	}
}

// TestDaemonWireShapes pins the exact JSON key names of the lifecycle contract
// family so a struct-tag rename cannot silently break the TypeScript client.
func TestDaemonWireShapes(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		raw, err := json.Marshal(DaemonIdentity{
			Ref: "local:root", PID: 4242,
			StartedAt: "2026-09-12T10:00:00Z", Generation: "9f86d081",
		})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{
			"ref": "local:root", "pid": 4242,
			"startedAt": "2026-09-12T10:00:00Z", "generation": "9f86d081"
		}`, raw)
	})
	t.Run("lifecycle full", func(t *testing.T) {
		raw, err := json.Marshal(DaemonLifecycle{
			Phase: "preparing", TimeoutMillis: 900000,
			EligibleSince: "2026-09-12T09:50:00Z", Deadline: "2026-09-12T10:05:00Z",
			Blockers: []DaemonBlocker{{Category: "question", SessionID: "sess_1", DelegateID: "dlg_1"}},
			Failure:  "prepare_failed",
		})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{
			"phase": "preparing", "timeoutMillis": 900000,
			"eligibleSince": "2026-09-12T09:50:00Z", "deadline": "2026-09-12T10:05:00Z",
			"blockers": [{"category": "question", "sessionId": "sess_1", "delegateId": "dlg_1"}],
			"failure": "prepare_failed"
		}`, raw)
	})
	t.Run("lifecycle minimal omits empty", func(t *testing.T) {
		raw, err := json.Marshal(DaemonLifecycle{Phase: "resident", TimeoutMillis: 0, Blockers: []DaemonBlocker{}})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{"phase": "resident", "timeoutMillis": 0, "blockers": []}`, raw)
	})
	t.Run("blocker omits empty ids", func(t *testing.T) {
		raw, err := json.Marshal(DaemonBlocker{Category: "environment"})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{"category": "environment"}`, raw)
	})
	t.Run("resident", func(t *testing.T) {
		raw, err := json.Marshal(DaemonResident{
			Identity: DaemonIdentity{Ref: "local:root", PID: 4242, StartedAt: "2026-09-12T10:00:00Z", Generation: "9f86d081"},
			Name:     "evener serve", Protocol: "evener-appwire-v5",
			Compatibility: "compatible", Archived: false, ProbeState: "current",
			Lifecycle: &DaemonLifecycle{Phase: "resident", TimeoutMillis: 0, Blockers: []DaemonBlocker{}},
			CanRetire: true, CanForceStop: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{
			"identity": {"ref": "local:root", "pid": 4242, "startedAt": "2026-09-12T10:00:00Z", "generation": "9f86d081"},
			"name": "evener serve", "protocol": "evener-appwire-v5",
			"compatibility": "compatible", "archived": false, "probeState": "current",
			"lifecycle": {"phase": "resident", "timeoutMillis": 0, "blockers": []},
			"canRetire": true, "canForceStop": true
		}`, raw)
	})
	t.Run("resident unknown lifecycle omits key", func(t *testing.T) {
		raw, err := json.Marshal(DaemonResident{
			Identity: DaemonIdentity{Ref: "local:root"}, Compatibility: "unknown", ProbeState: "unknown",
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "lifecycle") {
			t.Fatalf("unknown lifecycle serialized: %s", raw)
		}
	})
	t.Run("list response", func(t *testing.T) {
		raw, err := json.Marshal(DaemonListResponse{
			DefaultTimeoutMillis: 900000,
			Daemons:              []DaemonResident{{Identity: DaemonIdentity{Ref: "local:root"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{
			"defaultTimeoutMillis": 900000,
			"daemons": [{
				"identity": {"ref": "local:root", "pid": 0, "startedAt": "", "generation": ""},
				"name": "", "protocol": "", "compatibility": "", "archived": false,
				"probeState": "", "canRetire": false, "canForceStop": false
			}]
		}`, raw)
	})
	t.Run("retire params and response", func(t *testing.T) {
		raw, err := json.Marshal(DaemonRetireParams{
			Identity: DaemonIdentity{Ref: "local:root", PID: 4242, StartedAt: "2026-09-12T10:00:00Z", Generation: "9f86d081"},
		})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{
			"identity": {"ref": "local:root", "pid": 4242, "startedAt": "2026-09-12T10:00:00Z", "generation": "9f86d081"}
		}`, raw)
		raw, err = json.Marshal(DaemonRetireResponse{
			Accepted:  false,
			Lifecycle: DaemonLifecycle{Phase: "resident", TimeoutMillis: 900000, Blockers: []DaemonBlocker{{Category: "question", SessionID: "sess_1"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{
			"accepted": false,
			"lifecycle": {"phase": "resident", "timeoutMillis": 900000,
				"blockers": [{"category": "question", "sessionId": "sess_1"}]}
		}`, raw)
	})
	t.Run("status response", func(t *testing.T) {
		raw, err := json.Marshal(DaemonStatusResponse{
			Lifecycle: DaemonLifecycle{Phase: "retiring", TimeoutMillis: 900000, Blockers: []DaemonBlocker{}},
		})
		if err != nil {
			t.Fatal(err)
		}
		requireDaemonJSONEq(t, `{
			"lifecycle": {"phase": "retiring", "timeoutMillis": 900000, "blockers": []}
		}`, raw)
	})
}

// TestLifecycleErrorDataWireShape pins the typed error a peer decodes when a
// lifecycle transition races: it composes the existing mutation error data
// (never overwriting it) with lifecycleReason and retryable.
func TestLifecycleErrorDataWireShape(t *testing.T) {
	werr := LifecycleUnavailable("preparing")
	if werr.Code != CodeUnavailable {
		t.Fatalf("code=%d, want CodeUnavailable", werr.Code)
	}
	if strings.TrimSpace(werr.Message) == "" {
		t.Fatal("message is empty")
	}
	raw, err := json.Marshal(werr.Data)
	if err != nil {
		t.Fatal(err)
	}
	requireDaemonJSONEq(t, `{
		"evenerErrorInfo": "actionUnavailable",
		"mutationOutcome": "notAccepted",
		"retryDisposition": "automatic",
		"lifecycleReason": "preparing",
		"retryable": true
	}`, raw)
}

// TestDaemonCatalogScopes pins the Task 7 intermediate catalog: the hub-side
// list is declared but unimplemented until its real handler lands, while
// status and retire are daemon-scoped. Task 10 promotes retire to ScopeBoth
// and list to ScopeHub.
func TestDaemonCatalogScopes(t *testing.T) {
	specs := map[string]MethodSpec{}
	for _, m := range Methods {
		specs[m.Name] = m
	}
	for name, want := range map[string]MethodScope{
		MethodEvenerDaemonList:   ScopeUnimplemented,
		MethodEvenerDaemonRetire: ScopeDaemon,
		MethodEvenerDaemonStatus: ScopeDaemon,
	} {
		spec, ok := specs[name]
		if !ok {
			t.Fatalf("method %q missing from catalog", name)
		}
		if spec.Scope != want {
			t.Errorf("method %q scope = %q, want %q", name, spec.Scope, want)
		}
		if spec.Summary == "" {
			t.Errorf("method %q catalog summary is empty", name)
		}
	}
	if _, ok := specs[MethodEvenerDaemonList].Params.(DaemonListParams); !ok {
		t.Errorf("daemon/list params type = %T", specs[MethodEvenerDaemonList].Params)
	}
	if _, ok := specs[MethodEvenerDaemonList].Result.(DaemonListResponse); !ok {
		t.Errorf("daemon/list result type = %T", specs[MethodEvenerDaemonList].Result)
	}
	if _, ok := specs[MethodEvenerDaemonRetire].Params.(DaemonRetireParams); !ok {
		t.Errorf("daemon/retire params type = %T", specs[MethodEvenerDaemonRetire].Params)
	}
	if _, ok := specs[MethodEvenerDaemonRetire].Result.(DaemonRetireResponse); !ok {
		t.Errorf("daemon/retire result type = %T", specs[MethodEvenerDaemonRetire].Result)
	}
	if _, ok := specs[MethodEvenerDaemonStatus].Params.(DaemonStatusParams); !ok {
		t.Errorf("daemon/status params type = %T", specs[MethodEvenerDaemonStatus].Params)
	}
	if _, ok := specs[MethodEvenerDaemonStatus].Result.(DaemonStatusResponse); !ok {
		t.Errorf("daemon/status result type = %T", specs[MethodEvenerDaemonStatus].Result)
	}
	daemon := map[string]bool{}
	for _, name := range CatalogMethodNames(ScopeDaemon) {
		daemon[name] = true
	}
	if !daemon[MethodEvenerDaemonRetire] || !daemon[MethodEvenerDaemonStatus] {
		t.Errorf("daemon catalog missing retire/status: %v", daemon)
	}
	if daemon[MethodEvenerDaemonList] {
		t.Error("unimplemented daemon/list must not be in the daemon catalog")
	}
	for _, name := range CatalogMethodNames(ScopeHub) {
		if name == MethodEvenerDaemonList || name == MethodEvenerDaemonRetire || name == MethodEvenerDaemonStatus {
			t.Errorf("method %q must not be in the hub catalog yet", name)
		}
	}
}

// daemonRoundTrip drives one typed client call against the in-memory
// transport, asserts the requested method, answers with result, and returns
// the decoded typed response plus the written request frame.
func daemonRoundTrip[Resp any](t *testing.T, wantMethod string, result any, call func(context.Context, *Client) (Resp, error)) (Resp, Message) {
	t.Helper()
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx := t.Context()
	client.Start(ctx)
	type outcome struct {
		resp Resp
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := call(ctx, client)
		done <- outcome{resp, err}
	}()
	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	if written.Request.Method != wantMethod {
		t.Fatalf("method=%q, want %q", written.Request.Method, wantMethod)
	}
	transport.reads <- ResponseMessage(written.Request.ID, result)
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("%s: %v", wantMethod, out.err)
		}
		return out.resp, written
	case <-time.After(time.Second):
		t.Fatal("response was not routed")
	}
	var zero Resp
	return zero, written
}

func TestClientDaemonStatusRoundTrip(t *testing.T) {
	resp, _ := daemonRoundTrip(t, MethodEvenerDaemonStatus,
		DaemonStatusResponse{Lifecycle: DaemonLifecycle{Phase: "resident", TimeoutMillis: 0, Blockers: []DaemonBlocker{}}},
		func(ctx context.Context, c *Client) (DaemonStatusResponse, error) {
			return c.DaemonStatus(ctx, DaemonStatusParams{})
		})
	if resp.Lifecycle.Phase != "resident" || resp.Lifecycle.Blockers == nil {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestClientDaemonRetireRoundTrip(t *testing.T) {
	identity := DaemonIdentity{Ref: "local:root", PID: 4242, StartedAt: "2026-09-12T10:00:00Z", Generation: "9f86d081"}
	resp, written := daemonRoundTrip(t, MethodEvenerDaemonRetire,
		DaemonRetireResponse{Accepted: true, Lifecycle: DaemonLifecycle{Phase: "preparing", TimeoutMillis: 900000, Blockers: []DaemonBlocker{}}},
		func(ctx context.Context, c *Client) (DaemonRetireResponse, error) {
			return c.DaemonRetire(ctx, DaemonRetireParams{Identity: identity})
		})
	if !resp.Accepted || resp.Lifecycle.Phase != "preparing" {
		t.Fatalf("resp=%+v", resp)
	}
	var params DaemonRetireParams
	if err := json.Unmarshal(written.Request.Params, &params); err != nil {
		t.Fatalf("params decode: %v", err)
	}
	if params.Identity != identity {
		t.Fatalf("identity on wire = %+v, want %+v", params.Identity, identity)
	}
}

func TestClientDaemonListRoundTrip(t *testing.T) {
	resp, _ := daemonRoundTrip(t, MethodEvenerDaemonList,
		DaemonListResponse{DefaultTimeoutMillis: 900000, Daemons: []DaemonResident{{
			Identity: DaemonIdentity{Ref: "local:root", PID: 4242}, Compatibility: "compatible", ProbeState: "current",
		}}},
		func(ctx context.Context, c *Client) (DaemonListResponse, error) {
			return c.DaemonList(ctx, DaemonListParams{})
		})
	if resp.DefaultTimeoutMillis != 900000 || len(resp.Daemons) != 1 || resp.Daemons[0].Identity.PID != 4242 {
		t.Fatalf("resp=%+v", resp)
	}
}

// TestThreadForceStopParamsExpectedDaemon pins the optional ownership evidence
// the resident UI attaches to force-stop, and that existing ref-only callers
// are unchanged on the wire.
func TestThreadForceStopParamsExpectedDaemon(t *testing.T) {
	raw, err := json.Marshal(ThreadForceStopParams{Ref: "local:root"})
	if err != nil {
		t.Fatal(err)
	}
	requireDaemonJSONEq(t, `{"ref": "local:root"}`, raw)

	raw, err = json.Marshal(ThreadForceStopParams{
		Ref:            "local:root",
		ExpectedDaemon: &DaemonIdentity{Ref: "local:root", PID: 4242, StartedAt: "2026-09-12T10:00:00Z", Generation: "9f86d081"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireDaemonJSONEq(t, `{
		"ref": "local:root",
		"expectedDaemon": {"ref": "local:root", "pid": 4242, "startedAt": "2026-09-12T10:00:00Z", "generation": "9f86d081"}
	}`, raw)

	var decoded ThreadForceStopParams
	if err := json.Unmarshal([]byte(`{"ref":"local:root"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ExpectedDaemon != nil {
		t.Fatalf("ref-only caller gained daemon evidence: %+v", decoded.ExpectedDaemon)
	}
}
