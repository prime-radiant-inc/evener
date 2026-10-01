package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"

	"primeradiant.com/evener/appwire"
)

func TestSessionActivityRemoteReferences(t *testing.T) {
	source := &RemoteHubSource{id: "remote"}
	context := appwire.SessionActivityContext{Ref: "local:child", SessionID: "child", RootRef: "local:root", ParentRef: "local:root", Ancestors: []appwire.SessionActivityAncestor{{Ref: "local:root", SessionID: "root"}}, Epoch: "opaque", AncestryKnown: true}
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
	summary := appwire.SessionActivitySummary{Context: context, Issues: page.Issues}
	tests = append(tests, translationCase{"summary", &summary, func(t *testing.T) {
		if !reflect.DeepEqual(summary.Context, wantContext) || summary.Issues[0].Ref != "remote:grandchild" {
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
	watches := appwire.SessionWatchesResponse{Context: context, Page: page, Watches: []appwire.SessionWatch{{SourceRef: "local:child", OwnerRef: "local:root", ReceiverRef: "local:root", Watch: appwire.EvenerWatchInfo{ID: "opaque", Source: "shell-1"}}}}
	tests = append(tests, translationCase{"watches", &watches, func(t *testing.T) {
		if !reflect.DeepEqual(watches.Context, wantContext) || watches.Page.Issues[0].Ref != "remote:grandchild" || watches.Watches[0].OwnerRef != "remote:root" || watches.Watches[0].ReceiverRef != "remote:root" || watches.Watches[0].Watch.Source != "shell-1" || watches.Watches[0].SourceRef != "remote:child" {
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

func sessionActivitySourceRead(t *testing.T, source Source, method, ref string) appwire.SessionActivityContext {
	t.Helper()
	params := appwire.SessionActivityListParams{Ref: ref, Scope: appwire.SessionActivityScopeSubtree, Cursor: "cursor-a", Limit: 17}
	switch method {
	case appwire.MethodEvenerThreadActivityRead:
		response, err := source.ThreadActivityRead(t.Context(), appwire.SessionActivityReadParams{Ref: ref, Scope: params.Scope})
		if err != nil {
			t.Fatal(err)
		}
		return response.Context
	case appwire.MethodEvenerThreadDelegatesList:
		response, err := source.ThreadDelegatesList(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		return response.Context
	case appwire.MethodEvenerThreadJobsList:
		response, err := source.ThreadJobsList(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		return response.Context
	case appwire.MethodEvenerThreadWatchesList:
		response, err := source.ThreadWatchesList(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		return response.Context
	default:
		t.Fatalf("unknown activity read %s", method)
		return appwire.SessionActivityContext{}
	}
}

func TestSessionActivityLocalDescendantReadRouting(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	received := make(chan appwire.SessionActivityListParams, 1)
	activityContext := appwire.SessionActivityContext{Ref: "local:grandchild", SessionID: "grandchild", RootRef: "local:root", ParentRef: "local:child", Availability: "live", AncestryKnown: true, Ancestors: []appwire.SessionActivityAncestor{}}
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerThreadActivityRead, func(_ context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
		received <- appwire.SessionActivityListParams{Ref: params.Ref, Scope: params.Scope}
		return appwire.SessionActivitySummary{Context: activityContext}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerThreadDelegatesList, func(_ context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
		received <- params
		return appwire.SessionDelegatesResponse{Context: activityContext}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerThreadJobsList, func(_ context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
		received <- params
		return appwire.SessionJobsResponse{Context: activityContext}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerThreadWatchesList, func(_ context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
		received <- params
		return appwire.SessionWatchesResponse{Context: activityContext}, nil
	})
	host := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(host.Close)
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: host.URL, SourceID: "local", ThreadID: "root", SessionID: "root", WorkspaceRef: "local:workspace"}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry, SessionID: "root"}, {Entry: entry, SessionID: "grandchild", OwnerSessionID: "root", ReadOnlyAlias: true}}
	}, host.Client())
	for _, method := range []string{appwire.MethodEvenerThreadActivityRead, appwire.MethodEvenerThreadDelegatesList, appwire.MethodEvenerThreadJobsList, appwire.MethodEvenerThreadWatchesList} {
		t.Run(method, func(t *testing.T) {
			response := sessionActivitySourceRead(t, source, method, "local:grandchild")
			if !reflect.DeepEqual(response, activityContext) {
				t.Fatalf("descendant context = %+v", response)
			}
			params := <-received
			if params.Ref != "local:grandchild" || params.Scope != appwire.SessionActivityScopeSubtree || (method != appwire.MethodEvenerThreadActivityRead && (params.Cursor != "cursor-a" || params.Limit != 17)) {
				t.Fatalf("forwarded child params = %+v", params)
			}
		})
	}
}

func TestSessionActivityRemoteSourceIsolation(t *testing.T) {
	for _, id := range []string{"east", "west"} {
		t.Run(id, func(t *testing.T) {
			source, calls := newScriptedRemote(t, id, func(method string, raw json.RawMessage) scriptedReply {
				var params appwire.SessionActivityListParams
				if err := json.Unmarshal(raw, &params); err != nil {
					return scriptedReply{wireErr: new(appwire.InvalidParams(err.Error()))}
				}
				activityContext := appwire.SessionActivityContext{Ref: params.Ref, SessionID: "same-id", RootRef: "local:root", Epoch: id, Availability: "live", AncestryKnown: true}
				switch method {
				case appwire.MethodEvenerThreadActivityRead:
					return scriptedReply{result: appwire.SessionActivitySummary{Context: activityContext}}
				case appwire.MethodEvenerThreadDelegatesList:
					return scriptedReply{result: appwire.SessionDelegatesResponse{Context: activityContext}}
				case appwire.MethodEvenerThreadJobsList:
					return scriptedReply{result: appwire.SessionJobsResponse{Context: activityContext}}
				case appwire.MethodEvenerThreadWatchesList:
					return scriptedReply{result: appwire.SessionWatchesResponse{Context: activityContext}}
				default:
					return scriptedReply{wireErr: new(appwire.MethodNotFound(method))}
				}
			})
			for _, method := range []string{appwire.MethodEvenerThreadActivityRead, appwire.MethodEvenerThreadDelegatesList, appwire.MethodEvenerThreadJobsList, appwire.MethodEvenerThreadWatchesList} {
				response := sessionActivitySourceRead(t, source, method, id+":same-id")
				if response.Ref != id+":same-id" || response.SessionID != "same-id" || response.RootRef != id+":root" || response.Epoch != id || !response.AncestryKnown {
					t.Fatalf("qualified remote context = %+v", response)
				}
				var params appwire.SessionActivityListParams
				if err := json.Unmarshal(lastMethodCall(t, calls(), method), &params); err != nil {
					t.Fatal(err)
				}
				if params.Ref != "local:same-id" || params.Scope != appwire.SessionActivityScopeSubtree || (method != appwire.MethodEvenerThreadActivityRead && (params.Cursor != "cursor-a" || params.Limit != 17)) {
					t.Fatalf("remote params = %+v", params)
				}
			}
			count := len(calls())
			if _, err := source.ThreadActivityRead(t.Context(), appwire.SessionActivityReadParams{Ref: "local:same-id"}); err == nil || len(calls()) != count {
				t.Fatal("foreign source read reached remote")
			}
		})
	}
}

func TestSessionActivityLocalReadCancellation(t *testing.T) {
	ctx, finish := context.WithTimeout(t.Context(), 5*time.Second)
	defer finish()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerThreadJobsList, func(ctx context.Context, _ appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(canceled)
			return appwire.SessionJobsResponse{}, ctx.Err()
		case <-release:
			return appwire.SessionJobsResponse{}, nil
		}
	})
	host := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(host.Close)
	t.Cleanup(func() { close(release) })
	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{Protocol: appwire.ProtocolVersion, Endpoint: host.URL, SourceID: "local", ThreadID: "root", SessionID: "root"}}
	}, host.Client())
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := source.ThreadJobsList(request, appwire.SessionActivityListParams{Ref: "local:root"})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("local read never reached domain hook")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("canceled local read did not return")
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("owned local connection did not cancel daemon hook")
	}
}

type activityObservedSendTransport struct {
	appwire.Transport
	sent chan struct{}
	once sync.Once
}

func (transport *activityObservedSendTransport) Send(ctx context.Context, message appwire.Message) error {
	err := transport.Transport.Send(ctx, message)
	if err == nil && message.Request != nil && message.Request.Method == appwire.MethodEvenerThreadActivityRead {
		transport.once.Do(func() { close(transport.sent) })
	}
	return err
}

func TestSessionActivityRemoteCallerCancellationKeepsHostClient(t *testing.T) {
	ctx, finish := context.WithTimeout(t.Context(), 5*time.Second)
	defer finish()
	entered, release := make(chan context.Context, 1), make(chan struct{})
	var first sync.Once
	hub := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	appserver.HandleTyped(hub.Router(), appwire.MethodEvenerThreadActivityRead, func(ctx context.Context, _ appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
		first.Do(func() {
			entered <- ctx
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		return appwire.SessionActivitySummary{Context: appwire.SessionActivityContext{Ref: "local:root", SessionID: "root", RootRef: "local:root", Epoch: "same-connection"}}, nil
	})
	host := httptest.NewServer(http.HandlerFunc(hub.ServeWebSocket))
	t.Cleanup(host.Close)
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	transport, err := appwire.DialWebSocket(ctx, host.URL, host.Client())
	if err != nil {
		t.Fatal(err)
	}
	observed := &activityObservedSendTransport{Transport: transport, sent: make(chan struct{})}
	client := appwire.NewClient(observed)
	client.Start(ctx)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := source.ThreadActivityRead(request, appwire.SessionActivityReadParams{Ref: "host:root"})
		done <- err
	}()
	var handlerContext context.Context
	select {
	case handlerContext = <-entered:
	case <-ctx.Done():
		t.Fatal("remote read never reached handler")
	}
	select {
	case <-observed.sent:
	case <-ctx.Done():
		t.Fatal("remote request write did not complete")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("remote caller cancellation = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("remote caller kept waiting after cancellation")
	}
	// AppWire has no per-request wire cancel. The late response is harmless;
	// the shared host connection must still answer the next owner's read.
	if err := handlerContext.Err(); err != nil {
		t.Fatalf("caller cancellation closed shared host connection: %v", err)
	}
	unblock()
	response, err := source.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: "host:root"})
	if err != nil || response.Context.Epoch != "same-connection" {
		t.Fatalf("host connection after caller cancellation = %+v,%v", response, err)
	}
}
