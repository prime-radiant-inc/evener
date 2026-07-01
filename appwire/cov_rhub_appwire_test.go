package appwire

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/envvars"
)

// roundTrip drives one client wrapper method against the in-memory transport:
// it runs invoke in a goroutine, captures the request frame the client writes,
// asserts the JSON-RPC method, replies with response, and waits for invoke to
// return. It exercises the real params-marshal / response-unmarshal path of
// every thin Client.* wrapper without a live daemon.
func roundTrip(t *testing.T, wantMethod string, response any, invoke func(ctx context.Context, c *Client) error) Message {
	t.Helper()
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- invoke(ctx, client) }()

	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatalf("%s: request was not written", wantMethod)
	}
	if written.Request == nil {
		t.Fatalf("%s: expected a request frame, got %+v", wantMethod, written)
	}
	if written.Request.Method != wantMethod {
		t.Fatalf("method=%q, want %q", written.Request.Method, wantMethod)
	}

	transport.reads <- ResponseMessage(written.Request.ID, response)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("%s: %v", wantMethod, err)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s: response was not routed", wantMethod)
	}
	return written
}

func TestClientRequestWrappersRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		response any
		invoke   func(ctx context.Context, c *Client) error
	}{
		{"Request", MethodThreadList, ThreadListResponse{Data: []Thread{{ID: "th_1"}}}, func(ctx context.Context, c *Client) error {
			var out ThreadListResponse
			if err := c.Request(ctx, MethodThreadList, ThreadListParams{Limit: 3}, &out); err != nil {
				return err
			}
			if len(out.Data) != 1 || out.Data[0].ID != "th_1" {
				return errors.New("Request did not decode result")
			}
			return nil
		}},
		{"ThreadRead", MethodThreadRead, ThreadReadResponse{Thread: Thread{ID: "th_2"}, OlderCursor: "c1"}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadRead(ctx, ThreadReadParams{Ref: "local:th_2", IncludeTurns: true})
			if err != nil {
				return err
			}
			if out.Thread.ID != "th_2" || out.OlderCursor != "c1" {
				return errors.New("ThreadRead decode mismatch")
			}
			return nil
		}},
		{"ThreadTurnsList", MethodThreadTurnsList, ThreadTurnsListResponse{NextCursor: "n1"}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadTurnsList(ctx, ThreadTurnsListParams{Ref: "local:th", Limit: 5})
			if err != nil {
				return err
			}
			if out.NextCursor != "n1" {
				return errors.New("ThreadTurnsList decode mismatch")
			}
			return nil
		}},
		{"ThreadTurnItemsList", MethodThreadTurnItemsList, ThreadTurnItemsListResponse{NextCursor: "n2"}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadTurnItemsList(ctx, ThreadTurnItemsListParams{Ref: "local:th", TurnID: "tn_1"})
			if err != nil {
				return err
			}
			if out.NextCursor != "n2" {
				return errors.New("ThreadTurnItemsList decode mismatch")
			}
			return nil
		}},
		{"ThreadTranscriptList", MethodSerfThreadTranscriptsList, ThreadTranscriptListResponse{Data: []ThreadTranscriptTarget{{Ref: "local:th"}}}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadTranscriptList(ctx, ThreadTranscriptListParams{Ref: "local:th"})
			if err != nil {
				return err
			}
			if len(out.Data) != 1 {
				return errors.New("ThreadTranscriptList decode mismatch")
			}
			return nil
		}},
		{"ThreadStart", MethodThreadStart, ThreadStartResponse{Thread: Thread{ID: "th_3"}}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadStart(ctx, ThreadStartParams{CWD: "/tmp", Harness: "serf"})
			if err != nil {
				return err
			}
			if out.Thread.ID != "th_3" {
				return errors.New("ThreadStart decode mismatch")
			}
			return nil
		}},
		{"ThreadResume", MethodThreadResume, ThreadResumeResponse{Thread: Thread{ID: "th_4"}}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadResume(ctx, ThreadResumeParams{Session: "sess_1"})
			if err != nil {
				return err
			}
			if out.Thread.ID != "th_4" {
				return errors.New("ThreadResume decode mismatch")
			}
			return nil
		}},
		{"ThreadFork", MethodThreadFork, ThreadForkResponse{Thread: Thread{ID: "th_5"}}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadFork(ctx, ThreadForkParams{Ref: "local:th", SourceTurnID: "tn_1"})
			if err != nil {
				return err
			}
			if out.Thread.ID != "th_5" {
				return errors.New("ThreadFork decode mismatch")
			}
			return nil
		}},
		{"ThreadClear", MethodThreadClear, ThreadClearResponse{Ref: "local:th"}, func(ctx context.Context, c *Client) error {
			out, err := c.ThreadClear(ctx, ThreadClearParams{Ref: "local:th"})
			if err != nil {
				return err
			}
			if out.Ref != "local:th" {
				return errors.New("ThreadClear decode mismatch")
			}
			return nil
		}},
		{"ThreadModelSet", MethodThreadModelSet, EmptyResponse{}, func(ctx context.Context, c *Client) error {
			return c.ThreadModelSet(ctx, ThreadModelSetParams{Ref: "local:th", ModelProvider: "openai", Model: "gpt"})
		}},
		{"ThreadReasoningEffortSet", MethodThreadReasoningEffortSet, EmptyResponse{}, func(ctx context.Context, c *Client) error {
			return c.ThreadReasoningEffortSet(ctx, ThreadReasoningEffortSetParams{Ref: "local:th", ReasoningEffort: "high"})
		}},
		{"ThreadCompactStart", MethodThreadCompactStart, EmptyResponse{}, func(ctx context.Context, c *Client) error {
			return c.ThreadCompactStart(ctx, ThreadCompactStartParams{Ref: "local:th"})
		}},
		{"ThreadShutdown", MethodThreadShutdown, EmptyResponse{}, func(ctx context.Context, c *Client) error {
			return c.ThreadShutdown(ctx, ThreadShutdownParams{Ref: "local:th"})
		}},
		{"TurnInterrupt", MethodTurnInterrupt, EmptyResponse{}, func(ctx context.Context, c *Client) error {
			return c.TurnInterrupt(ctx, TurnInterruptParams{Ref: "local:th", ExpectedTurnID: "tn_1"})
		}},
		{"TasksList", MethodSerfTasksList, TaskListResponse{Data: []string{"t1"}}, func(ctx context.Context, c *Client) error {
			out, err := c.TasksList(ctx, TaskListParams{Ref: "local:th"})
			if err != nil {
				return err
			}
			if out.Data == nil {
				return errors.New("TasksList decode mismatch")
			}
			return nil
		}},
		{"DirsComplete", MethodSerfDirsComplete, DirsCompleteResponse{Data: []string{"/a", "/b"}}, func(ctx context.Context, c *Client) error {
			out, err := c.DirsComplete(ctx, DirsCompleteParams{Prefix: "/", Limit: 10})
			if err != nil {
				return err
			}
			if len(out.Data) != 2 {
				return errors.New("DirsComplete decode mismatch")
			}
			return nil
		}},
		{"HarnessList", MethodSerfHarnessesList, HarnessListResponse{Data: []HarnessDescriptor{{ID: "serf"}}}, func(ctx context.Context, c *Client) error {
			out, err := c.HarnessList(ctx, HarnessListParams{})
			if err != nil {
				return err
			}
			if len(out.Data) != 1 || out.Data[0].ID != "serf" {
				return errors.New("HarnessList decode mismatch")
			}
			return nil
		}},
		{"AuthStatus", MethodSerfAuthStatus, AuthStatusResponse{Provider: "openai", SignedIn: true}, func(ctx context.Context, c *Client) error {
			out, err := c.AuthStatus(ctx, AuthStatusParams{Provider: "openai"})
			if err != nil {
				return err
			}
			if out.Provider != "openai" || !out.SignedIn {
				return errors.New("AuthStatus decode mismatch")
			}
			return nil
		}},
		{"AuthLoginStart", MethodSerfAuthLoginStart, AuthLoginStartResponse{FlowID: "f1", URL: "https://x"}, func(ctx context.Context, c *Client) error {
			out, err := c.AuthLoginStart(ctx, AuthLoginStartParams{Provider: "openai"})
			if err != nil {
				return err
			}
			if out.FlowID != "f1" {
				return errors.New("AuthLoginStart decode mismatch")
			}
			return nil
		}},
		{"AuthLoginComplete", MethodSerfAuthLoginComplete, AuthLoginCompleteResponse{Status: AuthStatusResponse{SignedIn: true}}, func(ctx context.Context, c *Client) error {
			out, err := c.AuthLoginComplete(ctx, AuthLoginCompleteParams{Provider: "openai", FlowID: "f1"})
			if err != nil {
				return err
			}
			if !out.Status.SignedIn {
				return errors.New("AuthLoginComplete decode mismatch")
			}
			return nil
		}},
		{"AuthLogout", MethodSerfAuthLogout, AuthLogoutResponse{Removed: true}, func(ctx context.Context, c *Client) error {
			out, err := c.AuthLogout(ctx, AuthLogoutParams{Provider: "openai"})
			if err != nil {
				return err
			}
			if !out.Removed {
				return errors.New("AuthLogout decode mismatch")
			}
			return nil
		}},
		{"ModelList", MethodModelList, ModelListResponse{Data: []ModelDescriptor{{Provider: "openai", Model: "gpt"}}}, func(ctx context.Context, c *Client) error {
			out, err := c.ModelList(ctx, ModelListParams{Harness: "serf"})
			if err != nil {
				return err
			}
			if len(out.Data) != 1 || out.Data[0].Model != "gpt" {
				return errors.New("ModelList decode mismatch")
			}
			return nil
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roundTrip(t, tc.method, tc.response, tc.invoke)
		})
	}
}

// Initialize caches the server's advertised FeatureSet so later feature checks
// don't need another round trip.
func TestClientInitializeCachesFeatures(t *testing.T) {
	resp := InitializeResponse{
		ProtocolVersion: "1",
		SourceID:        "src",
		Features:        FeatureSet{ThreadList: true, TurnSteer: true},
	}
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	done := make(chan InitializeResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		out, err := client.Initialize(ctx, InitializeParams{ClientInfo: ClientInfo{Name: "test"}})
		if err != nil {
			errCh <- err
			return
		}
		done <- out
	}()

	written := <-transport.writes
	if written.Request.Method != MethodInitialize {
		t.Fatalf("method=%q", written.Request.Method)
	}
	transport.reads <- ResponseMessage(written.Request.ID, resp)

	select {
	case err := <-errCh:
		t.Fatalf("Initialize: %v", err)
	case out := <-done:
		if !out.Features.ThreadList || !out.Features.TurnSteer {
			t.Fatalf("features=%+v", out.Features)
		}
	case <-time.After(time.Second):
		t.Fatal("Initialize did not return")
	}

	client.featuresMu.RLock()
	cached := client.features
	client.featuresMu.RUnlock()
	if !cached.ThreadList || !cached.TurnSteer {
		t.Fatalf("cached features=%+v", cached)
	}
}

// Notify sends a fire-and-forget notification frame (no id, no pending entry).
func TestClientNotify(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx := context.Background()

	if err := client.Notify(ctx, NotifyThreadStatusChanged, map[string]string{"threadId": "th"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	select {
	case written := <-transport.writes:
		if written.Notification == nil {
			t.Fatalf("expected a notification frame, got %+v", written)
		}
		if written.Notification.Method != NotifyThreadStatusChanged {
			t.Fatalf("method=%q", written.Notification.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not written")
	}
}

// A wire error reply is surfaced with the method-qualified prefix and leaves no
// pending entry behind.
func TestClientRequestSurfacesWireError(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	errCh := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, ThreadReadParams{Ref: "local:th"})
		errCh <- err
	}()

	written := <-transport.writes
	transport.reads <- ErrorMessage(written.Request.ID, InternalError("boom"))

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected an error")
		}
		if got := err.Error(); got == "" || !contains(got, MethodThreadRead) || !contains(got, "boom") {
			t.Fatalf("error=%q, want method-qualified", got)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not fail")
	}

	client.pendingMu.Lock()
	n := len(client.pending)
	client.pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("pending entries left=%d, want 0", n)
	}
}

// Close delegates to the transport.
func TestClientClose(t *testing.T) {
	tr := &closeTrackingTransport{memoryTransport: newMemoryTransport()}
	client := NewClient(tr)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !tr.closed {
		t.Fatal("Close did not reach the transport")
	}
}

type closeTrackingTransport struct {
	*memoryTransport
	closed bool
}

func (t *closeTrackingTransport) Close() error {
	t.closed = true
	return nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Params accessor helpers are pure getters over the wire structs.
func TestInputParamAccessors(t *testing.T) {
	items := []InputItem{{Text: "hello"}}

	if got := (ThreadStartParams{Input: items}).EffectiveInput(); len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("ThreadStartParams.EffectiveInput=%+v", got)
	}
	ts := TurnStartParams{ThreadID: "th_1", Input: items}
	if got := ts.EffectiveInput(); len(got) != 1 {
		t.Fatalf("TurnStartParams.EffectiveInput=%+v", got)
	}
	if got := ts.TargetRef(); got != "th_1" {
		t.Fatalf("TurnStartParams.TargetRef=%q", got)
	}
	steer := TurnSteerParams{ThreadID: "th_2", ExpectedTurnID: "tn_9", Input: items}
	if got := steer.EffectiveInput(); len(got) != 1 {
		t.Fatalf("TurnSteerParams.EffectiveInput=%+v", got)
	}
	if got := steer.TargetRef(); got != "th_2" {
		t.Fatalf("TurnSteerParams.TargetRef=%q", got)
	}
	if got := steer.EffectiveTurnID(); got != "tn_9" {
		t.Fatalf("TurnSteerParams.EffectiveTurnID=%q", got)
	}
	interrupt := TurnInterruptParams{ThreadID: "th_3", ExpectedTurnID: "tn_3"}
	if got := interrupt.TargetRef(); got != "th_3" {
		t.Fatalf("TurnInterruptParams.TargetRef=%q", got)
	}
	if got := interrupt.EffectiveTurnID(); got != "tn_3" {
		t.Fatalf("TurnInterruptParams.EffectiveTurnID=%q", got)
	}
}

func TestInputTextPrefersFirstNonBlank(t *testing.T) {
	if got := inputText([]InputItem{{Text: "   "}, {Text: "real"}}); got != "real" {
		t.Fatalf("inputText=%q, want real", got)
	}
	if got := inputText(nil); got != "" {
		t.Fatalf("inputText(nil)=%q, want empty", got)
	}
}

func TestPendingTargetRefFallsBackToThreadID(t *testing.T) {
	if got := pendingTargetRef("ref1", "th"); got != "ref1" {
		t.Fatalf("pendingTargetRef with ref=%q", got)
	}
	if got := pendingTargetRef("   ", "th"); got != "th" {
		t.Fatalf("pendingTargetRef fallback=%q", got)
	}
}

// recorderStateRoot resolves SERF_STATE_DIR first, else the home-relative
// default; the recorder is a low-level codec helper with no dependency on the
// cmd layer, so this precedence must hold on its own.
func TestRecorderStateRoot(t *testing.T) {
	t.Setenv(envvars.SERFStateDir.Name, "/custom/state")
	if got := recorderStateRoot(); got != "/custom/state" {
		t.Fatalf("recorderStateRoot with SERF_STATE_DIR=%q", got)
	}

	t.Setenv(envvars.SERFStateDir.Name, "")
	t.Setenv("HOME", "/home/tester")
	if got := recorderStateRoot(); got != "/home/tester/.serf" {
		t.Fatalf("recorderStateRoot home fallback=%q", got)
	}
}

// A live FrameRecorder writes one JSONL line per recorded frame; a nil recorder
// is a safe no-op on every method.
func TestFrameRecorderRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	rec, err := NewFrameRecorder(path)
	if err != nil {
		t.Fatalf("NewFrameRecorder: %v", err)
	}
	rec.RecordSend([]byte(`{"a":1}`))
	rec.RecordRecv([]byte(`{"b":2}`))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var send, recv recordedFrame
	lines := splitLines(string(data))
	if len(lines) != 2 {
		t.Fatalf("lines=%d, want 2: %q", len(lines), string(data))
	}
	if err := json.Unmarshal([]byte(lines[0]), &send); err != nil {
		t.Fatalf("decode send line: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &recv); err != nil {
		t.Fatalf("decode recv line: %v", err)
	}
	if send.Dir != "send" || send.Frame != `{"a":1}` {
		t.Fatalf("send frame=%+v", send)
	}
	if recv.Dir != "recv" || recv.Frame != `{"b":2}` {
		t.Fatalf("recv frame=%+v", recv)
	}

	var nilRec *FrameRecorder
	nilRec.RecordSend([]byte("x"))
	nilRec.RecordRecv([]byte("y"))
	if err := nilRec.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestNewFrameRecorderRejectsBadPath(t *testing.T) {
	// A path whose parent is a regular file cannot be opened for append.
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := NewFrameRecorder(filepath.Join(parent, "frames.jsonl")); err == nil {
		t.Fatal("expected error opening under a regular file")
	}
}

// A transport whose Send fails must clear the pending entry the request
// reserved so a failed send never leaks a map slot.
func TestClientRequestSendErrorClearsPending(t *testing.T) {
	transport := &sendErrTransport{memoryTransport: newMemoryTransport(), err: errors.New("send failed")}
	client := NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	_, err := client.ThreadRead(ctx, ThreadReadParams{Ref: "local:th"})
	if err == nil || !contains(err.Error(), "send failed") {
		t.Fatalf("err=%v, want send failure", err)
	}
	client.pendingMu.Lock()
	n := len(client.pending)
	client.pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("pending entries=%d, want 0 after send failure", n)
	}
}

type sendErrTransport struct {
	*memoryTransport
	err error
}

func (t *sendErrTransport) Send(context.Context, Message) error { return t.err }

func TestMethodScopeRouted(t *testing.T) {
	routed := map[MethodScope]bool{
		ScopeBoth:          true,
		ScopeHub:           true,
		ScopeDaemon:        true,
		ScopeConnection:    false,
		ScopeUnimplemented: false,
	}
	for scope, want := range routed {
		if got := scope.Routed(); got != want {
			t.Fatalf("%s.Routed()=%v, want %v", scope, got, want)
		}
	}
}

// ConnectionMethodNames and CatalogMethodNames partition the catalog by scope;
// the router cross-check depends on that partition being exact.
func TestCatalogMethodNamePartition(t *testing.T) {
	conn := ConnectionMethodNames()
	if len(conn) == 0 {
		t.Fatal("expected at least one connection method (initialize/ping)")
	}
	for _, name := range conn {
		if scopeOfMethod(t, name) != ScopeConnection {
			t.Fatalf("%q surfaced by ConnectionMethodNames but is not connection-scoped", name)
		}
	}

	for _, scope := range []MethodScope{ScopeHub, ScopeDaemon} {
		names := CatalogMethodNames(scope)
		if len(names) == 0 {
			t.Fatalf("CatalogMethodNames(%s) returned nothing", scope)
		}
		for _, name := range names {
			s := scopeOfMethod(t, name)
			if s != scope && s != ScopeBoth {
				t.Fatalf("CatalogMethodNames(%s) included %q with scope %s", scope, name, s)
			}
		}
	}
}

func scopeOfMethod(t *testing.T, name string) MethodScope {
	t.Helper()
	for _, m := range Methods {
		if m.Name == name {
			return m.Scope
		}
	}
	t.Fatalf("method %q not found in catalog", name)
	return ""
}

func TestRefString(t *testing.T) {
	if got := (Ref{SourceID: "local", ThreadID: "th_1"}).String(); got != "local:th_1" {
		t.Fatalf("Ref.String=%q", got)
	}
	if got := (Ref{ThreadID: "th_1"}).String(); got != "" {
		t.Fatalf("Ref.String with empty source=%q, want empty", got)
	}
	if got := (Ref{SourceID: "local"}).String(); got != "" {
		t.Fatalf("Ref.String with empty thread=%q, want empty", got)
	}
}

// LaunchConfigLayer.MarshalJSON emits modelFallbacks as an explicit key (even
// empty) only when the slice is non-nil, so an intentional "clear fallbacks"
// override survives the wire while an absent one stays omitted.
func TestLaunchConfigLayerMarshalModelFallbacks(t *testing.T) {
	withNil := LaunchConfigLayer{Model: "gpt"}
	raw, err := json.Marshal(withNil)
	if err != nil {
		t.Fatalf("marshal nil fallbacks: %v", err)
	}
	if contains(string(raw), "modelFallbacks") {
		t.Fatalf("nil fallbacks must omit the key: %s", raw)
	}

	cleared := LaunchConfigLayer{Model: "gpt", ModelFallbacks: []string{}}
	raw, err = json.Marshal(cleared)
	if err != nil {
		t.Fatalf("marshal empty fallbacks: %v", err)
	}
	if !contains(string(raw), `"modelFallbacks":[]`) {
		t.Fatalf("empty (non-nil) fallbacks must emit []: %s", raw)
	}

	populated := LaunchConfigLayer{ModelFallbacks: []string{"a", "b"}}
	raw, err = json.Marshal(populated)
	if err != nil {
		t.Fatalf("marshal populated fallbacks: %v", err)
	}
	var back LaunchConfigLayer
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if len(back.ModelFallbacks) != 2 || back.ModelFallbacks[0] != "a" {
		t.Fatalf("fallbacks round-trip=%+v", back.ModelFallbacks)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
