package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

type remoteCall struct {
	method string
	params json.RawMessage
}

type scriptedReply struct {
	result    any
	wireErr   *appwire.WireError
	closeConn bool
}

// newScriptedRemote wires a RemoteHubSource to an in-memory StreamTransport
// whose server side answers canned responses. No SSH, no network, no host.
func newScriptedRemote(t *testing.T, id string, handle func(method string, params json.RawMessage) scriptedReply) (*RemoteHubSource, func() []remoteCall) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	var mu sync.Mutex
	var calls []remoteCall

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			mu.Lock()
			calls = append(calls, remoteCall{method: msg.Request.Method, params: msg.Request.Params})
			mu.Unlock()
			if msg.Request.Method == appwire.MethodInitialize {
				data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
				if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
					return
				}
				continue
			}
			reply := handle(msg.Request.Method, msg.Request.Params)
			if reply.closeConn {
				_ = server.Close()
				return
			}
			if reply.wireErr != nil {
				if err := server.Send(ctx, appwire.ErrorMessage(msg.Request.ID, *reply.wireErr)); err != nil {
					return
				}
				continue
			}
			data, err := json.Marshal(reply.result)
			if err != nil {
				return
			}
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		cancel()
		t.Fatalf("initialize scripted remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})

	source := NewRemoteHubSource(id, nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	return source, func() []remoteCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]remoteCall, len(calls))
		copy(out, calls)
		return out
	}
}

// lastMethodCall returns the params of the most recent call of method, failing
// the test when none was recorded.
func lastMethodCall(t *testing.T, calls []remoteCall, method string) json.RawMessage {
	t.Helper()
	for _, call := range slices.Backward(calls) {
		if call.method == method {
			return call.params
		}
	}
	t.Fatalf("no %s call recorded; calls = %+v", method, calls)
	return nil
}

func TestRemoteHubSourceListThreads(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadList {
			t.Errorf("method = %q, want %q", method, appwire.MethodThreadList)
		}
		return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{{
			ID:     "t1",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:t1", InstanceID: "inst-1"},
		}}}}
	})

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{SourceIDs: []string{"host"}})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}

	var remote appwire.ThreadListParams
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadList), &remote); err != nil {
		t.Fatalf("decode remote params: %v", err)
	}
	if len(remote.SourceIDs) != 1 || remote.SourceIDs[0] != "local" {
		t.Fatalf("remote SourceIDs = %v, want [local]", remote.SourceIDs)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data = %+v, want one thread", resp.Data)
	}
	if resp.Data[0].Source != "host" || resp.Data[0].Evener.Ref != "host:t1" {
		t.Fatalf("translated thread = %+v, want source host and ref host:t1", resp.Data[0])
	}
	if resp.Data[0].Evener.InstanceID != "inst-1" {
		t.Fatalf("InstanceID = %q, want inst-1", resp.Data[0].Evener.InstanceID)
	}
}

// A controller request that names only other sources must not widen into an
// unfiltered remote list: remapRemoteSourceIDs drops every entry, so the
// request would otherwise be forwarded with no filter at all.
func TestRemoteHubSourceListThreadsForeignFilterReturnsEmptyWithoutCall(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{{
			ID:     "t1",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:t1"},
		}}}}
	})
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{SourceIDs: []string{"other"}})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("Data = %+v, want an empty page", resp.Data)
	}
	for _, call := range calls() {
		if call.method == appwire.MethodThreadList {
			t.Fatalf("foreign-only filter was forwarded: %+v", calls())
		}
	}
}

// An unfiltered controller list is scoped to the remote hub's own "local"
// namespace. Forwarding no SourceIDs makes a nested remote hub return its own
// remote refs, which translateOut refuses and which abort the whole response.
func TestRemoteHubSourceListThreadsUnfilteredScopesRemoteToLocal(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadListResponse{}}
	})
	if _, err := source.ListThreads(context.Background(), appwire.ThreadListParams{}); err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	var remote appwire.ThreadListParams
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadList), &remote); err != nil {
		t.Fatalf("decode remote params: %v", err)
	}
	if !slices.Equal(remote.SourceIDs, []string{remoteHubNamespace}) {
		t.Fatalf("remote SourceIDs = %v, want [%s]", remote.SourceIDs, remoteHubNamespace)
	}
}

func TestRemoteHubTransportTextRequiresTokenBoundary(t *testing.T) {
	if remoteHubTransportText("internal error: eoffice closed unexpectedly") {
		t.Fatal("bare eof substring was reclassified as a transport failure")
	}
	for _, text := range []string{"unexpected eof", "read: eof", "eof", "websocket: unexpected eof"} {
		if !remoteHubTransportText(text) {
			t.Fatalf("%q was not classified as a transport failure", text)
		}
	}
}

// mapCallError mirrors localDaemonCallError: a request deadline is the
// caller's own context expiring, not host unavailability. The attach/dial step
// keeps the localDaemonDialError mapping.
func TestRemoteHubSourceCallErrorKeepsRequestDeadlineRaw(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	if err := source.mapCallError(context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mapCallError(deadline) = %v, want context.DeadlineExceeded", err)
	}
	if err := source.mapCallError(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("mapCallError(canceled) = %v, want context.Canceled", err)
	}
	connect := source.mapConnectError(context.DeadlineExceeded)
	var wire appwire.WireError
	if !errors.As(connect, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("mapConnectError(deadline) = %v, want SessionUnavailable", connect)
	}
}

func TestRemoteHubSourceReadThread(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("method = %q, want %q", method, appwire.MethodThreadRead)
		}
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "t2",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:t2", ParentRef: "local:owner", InstanceID: "inst-2"},
		}}}
	})

	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "host:t2", IncludeTurns: true})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}

	var remote appwire.ThreadReadParams
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadRead), &remote); err != nil {
		t.Fatalf("decode remote params: %v", err)
	}
	if remote.Ref != "local:t2" {
		t.Fatalf("remote Ref = %q, want local:t2", remote.Ref)
	}
	// The caller addressed the thread by ref alone, so the forwarded ThreadID
	// stays empty: the remote resolves the ref itself, which is stable-aware,
	// while a bare threadId it cannot resolve is rejected outright.
	if remote.ThreadID != "" {
		t.Fatalf("remote ThreadID = %q, want empty (the caller sent no thread id)", remote.ThreadID)
	}
	if !remote.IncludeTurns {
		t.Fatal("IncludeTurns was not forwarded")
	}
	if resp.Thread.Source != "host" || resp.Thread.Evener.Ref != "host:t2" {
		t.Fatalf("translated thread = %+v, want source host and ref host:t2", resp.Thread)
	}
	if resp.Thread.Evener.ParentRef != "host:owner" {
		t.Fatalf("ParentRef = %q, want host:owner", resp.Thread.Evener.ParentRef)
	}
	if resp.Thread.Evener.InstanceID != "inst-2" {
		t.Fatalf("InstanceID = %q, want inst-2", resp.Thread.Evener.InstanceID)
	}
}

// Subscription fan-out is staged until 05b, so a controller read's subscribe
// intent must not reach the remote hub: it would register a remote subscription
// the controller can never retire. The controller-side relay gate is
// RelayOnThreadRead, which reports false for this source.
func TestRemoteHubSourceReadThreadStripsSubscription(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "t1",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:t1"},
		}}}
	})
	if _, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref:                 "host:t1",
		Subscribe:           true,
		ReplaceSubscription: true,
	}); err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	var remote appwire.ThreadReadParams
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadRead), &remote); err != nil {
		t.Fatalf("decode remote params: %v", err)
	}
	if remote.Subscribe || remote.ReplaceSubscription {
		t.Fatalf("forwarded subscription intent = %+v, want both cleared", remote)
	}
	if source.RelayOnThreadRead() {
		t.Fatal("RelayOnThreadRead = true; the hub would start a staged relay on a plain read")
	}
	if source.SupportsThreadRelay() {
		t.Fatal("SupportsThreadRelay = true; the hub would start a staged relay on a subscribe read")
	}
}

func TestRemoteHubSourceListTurns(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadTurnsList {
			t.Errorf("method = %q, want %q", method, appwire.MethodThreadTurnsList)
		}
		return scriptedReply{result: appwire.ThreadTurnsListResponse{NextCursor: "next"}}
	})

	resp, err := source.ListTurns(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t2", Cursor: "cur"})
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}

	var remote appwire.ThreadTurnsListParams
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadTurnsList), &remote); err != nil {
		t.Fatalf("decode remote params: %v", err)
	}
	if remote.Ref != "local:t2" {
		t.Fatalf("remote Ref = %q, want local:t2", remote.Ref)
	}
	if remote.Cursor != "cur" {
		t.Fatalf("remote Cursor = %q, want cur", remote.Cursor)
	}
	if resp.NextCursor != "next" {
		t.Fatalf("NextCursor = %q, want next", resp.NextCursor)
	}
}

func TestRemoteHubSourceListModels(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodModelList {
			t.Errorf("method = %q, want %q", method, appwire.MethodModelList)
		}
		return scriptedReply{result: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: "m"}}}}
	})

	resp, err := source.ListModels(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	lastMethodCall(t, calls(), appwire.MethodModelList)
	if len(resp.Data) != 1 || resp.Data[0].Model != "m" {
		t.Fatalf("Data = %+v, want one model m", resp.Data)
	}
}

func TestRemoteHubSourceForeignRefRefusedWithoutCall(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "other:t1"})
	if err == nil {
		t.Fatal("ReadThread accepted a foreign ref")
	}
	for _, call := range calls() {
		if call.method == appwire.MethodThreadRead {
			t.Fatalf("foreign ref was forwarded: %+v", calls())
		}
	}
}

func TestRemoteHubSourceNestedNonLocalRefRefused(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "t3",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "other:t3"},
		}}}
	})
	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "host:t3"})
	if err == nil {
		t.Fatal("ReadThread accepted a nested non-local ref")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError {
		t.Fatalf("error = %T %v, want InternalError WireError", err, err)
	}
}

func TestRemoteHubSourceEOFBecomesSessionUnavailable(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})
	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"})
	if err == nil {
		t.Fatal("ReadThread succeeded after the remote closed the pipe")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
}

func TestRemoteHubSourcePreservesSemanticWireError(t *testing.T) {
	semantic := appwire.InvalidParams("boom")
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{wireErr: &semantic}
	})
	_, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err == nil {
		t.Fatal("ListThreads succeeded despite a semantic error")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d (semantic error remapped)", wire.Code, appwire.CodeInvalidParams)
	}
	if !strings.Contains(wire.Message, "boom") {
		t.Fatalf("message = %q, want it to preserve %q", wire.Message, "boom")
	}
}

// A component-04 attach failure to start the SSH bridge is host unavailability,
// not a generic internal error: the AppWire layer must see SessionUnavailable so
// the fleet view and the auto-resume gate can attribute and recover it. The
// terminal sshconn classes (authentication, protocol) stay raw so a host that
// can never attach is not retried forever.
func TestRemoteHubSourceMapsSSHAttachFailureToSessionUnavailable(t *testing.T) {
	start := fmt.Errorf("%w: host %q: %w: %s", sshconn.ErrSSHStart, "host", errors.New("exit status 255"), "ssh: connect to host host port 22: Connection refused")
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return nil, start
	})
	_, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err == nil {
		t.Fatal("ListThreads succeeded despite an SSH start failure")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || wireErrorInfo(wire) != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("ssh start failure = %T %v, want SessionUnavailable", err, err)
	}

	for _, terminal := range []struct {
		name string
		err  error
	}{
		{"authentication", fmt.Errorf("%w: host %q: %s", sshconn.ErrSSHAuth, "host", "Permission denied (publickey)")},
		{"protocol", fmt.Errorf("%w: host %q", sshconn.ErrProtocolIncompatible, "host")},
	} {
		t.Run(terminal.name, func(t *testing.T) {
			source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
				return nil, terminal.err
			})
			_, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
			if err == nil {
				t.Fatal("ListThreads succeeded despite a terminal attach failure")
			}
			if _, mapped := errors.AsType[appwire.WireError](err); mapped {
				t.Fatalf("%s failure = %v, want the raw terminal error", terminal.name, err)
			}
		})
	}
}

// A failed hub restart is transient, not terminal: sshconn keeps retrying on the
// next Ensure (ErrRestart is not in isTerminal). The attach step surfaces it
// wrapped, so it must map like any other unreachable host and become
// SessionUnavailable, or the recovery/auto-resume gate never sees the host go
// down. Terminal configuration and authentication classes stay raw.
func TestRemoteHubSourceMapsRestartFailureToSessionUnavailable(t *testing.T) {
	restart := fmt.Errorf("%w: host %q relaunch: %w: %s", sshconn.ErrRestart, "host", errors.New("exit status 1"), "hub did not come up")
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return nil, restart
	})
	_, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err == nil {
		t.Fatal("ListThreads succeeded despite a restart failure")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || wireErrorInfo(wire) != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("restart failure = %T %v, want SessionUnavailable", err, err)
	}
}

// wireErrorInfo extracts evenerErrorInfo whether the error was synthesized
// locally (appwire.ErrorData) or decoded from the wire (map[string]any).
func wireErrorInfo(wire appwire.WireError) string {
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		return string(data.EvenerErrorInfo)
	case map[string]any:
		info, _ := data["evenerErrorInfo"].(string)
		return info
	default:
		return ""
	}
}

// TestRemoteHubSourceReadThreadTranslatesNestedDiagnostics pins the read-path
// half of nested session-ref translation: a thread's Evener.Diagnostics carries
// the transcript handles of the child sessions (and jobs) a remote hub hosts, so
// they must move into the controller namespace with the rest of the thread.
// Opaque handles are left alone.
func TestRemoteHubSourceReadThreadTranslatesNestedDiagnostics(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("method = %q, want %q", method, appwire.MethodThreadRead)
		}
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "t4",
			Source: "local",
			Evener: appwire.EvenerThread{
				Ref: "local:t4",
				Diagnostics: &appwire.EvenerDiagnostics{
					Jobs: []appwire.EvenerJobInfo{
						{JobID: "j1", TranscriptRef: "local:child"},
						{JobID: "j2", TranscriptRef: "job:job_abc"},
					},
					Delegates: []appwire.EvenerDelegateInfo{
						{DelegateID: "d1", TranscriptRef: "local:child"},
					},
				},
			},
		}}}
	})

	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "host:t4"})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	diagnostics := resp.Thread.Evener.Diagnostics
	if diagnostics == nil {
		t.Fatal("diagnostics were dropped")
	}
	if got := diagnostics.Jobs[0].TranscriptRef; got != "host:child" {
		t.Fatalf("job transcriptRef = %q, want host:child", got)
	}
	if got := diagnostics.Jobs[1].TranscriptRef; got != "job:job_abc" {
		t.Fatalf("opaque job transcriptRef = %q, want job:job_abc untouched", got)
	}
	if got := diagnostics.Delegates[0].TranscriptRef; got != "host:child" {
		t.Fatalf("delegate transcriptRef = %q, want host:child", got)
	}
}

// TestRemoteHubSourceReadThreadTranslatesPendingEscalationRefs pins the
// escalation-card half of the read path: a thread snapshot's
// Evener.PendingEscalations entries carry the ref a client routes the card by
// (SandboxEscalationRequested.Ref), so it must move into the controller
// namespace with the rest of the thread. Opaque handles are left alone.
func TestRemoteHubSourceReadThreadTranslatesPendingEscalationRefs(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("method = %q, want %q", method, appwire.MethodThreadRead)
		}
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "t5",
			Source: "local",
			Evener: appwire.EvenerThread{
				Ref: "local:t5",
				PendingEscalations: []appwire.SandboxEscalationRequested{
					{ThreadID: "child", Ref: "local:child", EscalationID: "e1", DeniedPath: "/tmp/denied"},
					{ThreadID: "t5", Ref: "job:job_abc", EscalationID: "e2"},
				},
			},
		}}}
	})

	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "host:t5"})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	escalations := resp.Thread.Evener.PendingEscalations
	if len(escalations) != 2 {
		t.Fatalf("pendingEscalations = %+v, want two entries", escalations)
	}
	if got := escalations[0].Ref; got != "host:child" {
		t.Fatalf("escalation ref = %q, want host:child", got)
	}
	if got := escalations[0].ThreadID; got != "child" {
		t.Fatalf("escalation threadId = %q, want child (never rewritten)", got)
	}
	if got := escalations[0].DeniedPath; got != "/tmp/denied" {
		t.Fatalf("escalation deniedPath = %q, want the payload preserved", got)
	}
	if got := escalations[1].Ref; got != "job:job_abc" {
		t.Fatalf("opaque escalation ref = %q, want job:job_abc untouched", got)
	}
}
