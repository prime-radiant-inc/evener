package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
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
	for index := len(calls) - 1; index >= 0; index-- {
		if calls[index].method == method {
			return calls[index].params
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
	if remote.ThreadID != "t2" {
		t.Fatalf("remote ThreadID = %q, want t2", remote.ThreadID)
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
