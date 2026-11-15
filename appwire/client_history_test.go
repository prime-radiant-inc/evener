package appwire

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

type historyCallResult struct {
	facts         HistoryServingFacts
	boot          string
	epoch         uint64
	authoritative bool
	err           error
}

func startHistoryCall(ctx context.Context, client *Client, method string) <-chan historyCallResult {
	result := make(chan historyCallResult, 1)
	go func() {
		if method == MethodThreadHistoryRead {
			response, err := client.ThreadHistoryRead(ctx, ThreadReadParams{Ref: "local:lane", IncludeTurns: true})
			result <- historyCallResult{response.HistoryServingFacts, response.BootGeneration, response.Epoch, response.Authoritative, err}
		} else {
			response, err := client.ThreadHistoryTurnsList(ctx, ThreadTurnsListParams{Ref: "local:lane"})
			result <- historyCallResult{response.HistoryServingFacts, response.BootGeneration, response.Epoch, response.Authoritative, err}
		}
	}()
	return result
}

func historyFrame(t *testing.T, transport *memoryTransport) Request {
	t.Helper()
	synctest.Wait()
	select {
	case frame := <-transport.writes:
		if frame.Request == nil {
			t.Fatalf("history call wrote a non-request frame: %+v", frame)
		}
		return *frame.Request
	default:
		t.Fatal("history call never reached the RPC boundary")
		return Request{}
	}
}

func historyResult(t *testing.T, result <-chan historyCallResult) historyCallResult {
	t.Helper()
	synctest.Wait()
	select {
	case response := <-result:
		return response
	default:
		t.Fatal("history call did not finish")
		return historyCallResult{}
	}
}

func replyHistory(t *testing.T, transport *memoryTransport, request Request, legacy bool) {
	t.Helper()
	facts := HistoryServingFacts{ServedFrom: "live-daemon", AsOf: "2026-11-08T01:02:03Z", InstanceID: "instance-A"}
	switch request.Method {
	case MethodThreadRead:
		if !legacy {
			t.Fatal("unexpected legacy read")
		}
		transport.reads <- ResponseMessage(request.ID, ThreadReadResponse{Thread: Thread{ID: "lane"}, BootGeneration: "daemonless", Epoch: 9, Authoritative: true})
	case MethodThreadTurnsList:
		if !legacy {
			t.Fatal("unexpected legacy page")
		}
		transport.reads <- ResponseMessage(request.ID, ThreadTurnsListResponse{Data: []Turn{}, BootGeneration: "daemonless", Epoch: 9, Authoritative: true})
	case MethodThreadHistoryRead:
		if legacy {
			t.Fatal("unexpected duplicate new read")
		}
		read := ThreadHistoryReadResponse{}
		read.Thread = Thread{ID: "lane"}
		read.HistoryServingFacts = facts
		transport.reads <- ResponseMessage(request.ID, read)
	case MethodThreadHistoryTurnsList:
		if legacy {
			t.Fatal("unexpected duplicate new page")
		}
		page := ThreadHistoryTurnsListResponse{}
		page.Data = []Turn{}
		page.HistoryServingFacts = facts
		transport.reads <- ResponseMessage(request.ID, page)
	default:
		t.Fatalf("unexpected method %q", request.Method)
	}
}

func TestClientHistoryConcurrentProbesCacheLegacyTransition(t *testing.T) {
	for _, firstMethod := range []string{MethodThreadHistoryRead, MethodThreadHistoryTurnsList} {
		t.Run(firstMethod, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				transport := newMemoryTransport()
				client := NewClient(transport)
				client.Start(ctx)
				firstResult := startHistoryCall(ctx, client, firstMethod)
				first := historyFrame(t, transport)
				if first.Method != firstMethod {
					t.Fatalf("first method=%q", first.Method)
				}
				secondMethod := MethodThreadHistoryTurnsList
				if firstMethod == secondMethod {
					secondMethod = MethodThreadHistoryRead
				}
				secondResult := startHistoryCall(ctx, client, secondMethod)
				second := historyFrame(t, transport)
				if second.Method != secondMethod {
					t.Fatalf("second initial method=%q, want %q before either response", second.Method, secondMethod)
				}
				transport.reads <- ErrorMessage(first.ID, MethodNotFound(first.Method))
				firstFallback := historyFrame(t, transport)
				if firstFallback.Method != legacyHistoryMethod(firstMethod) || !bytes.Equal(firstFallback.Params, first.Params) {
					t.Fatalf("first fallback=%+v, initial=%+v", firstFallback, first)
				}
				// The other initial request is still in flight. Later reads and
				// pages must already use the shared cached legacy mode.
				for _, method := range []string{MethodThreadHistoryRead, MethodThreadHistoryTurnsList} {
					later := startHistoryCall(ctx, client, method)
					request := historyFrame(t, transport)
					if request.Method != legacyHistoryMethod(method) {
						t.Fatalf("after first degradation method=%q", request.Method)
					}
					replyHistory(t, transport, request, true)
					if response := historyResult(t, later); response.err != nil {
						t.Fatal(response.err)
					}
				}
				transport.reads <- ErrorMessage(second.ID, MethodNotFound(second.Method))
				secondFallback := historyFrame(t, transport)
				if secondFallback.Method != legacyHistoryMethod(secondMethod) || !bytes.Equal(secondFallback.Params, second.Params) {
					t.Fatalf("second fallback=%+v, initial=%+v", secondFallback, second)
				}
				replyHistory(t, transport, secondFallback, true)
				replyHistory(t, transport, firstFallback, true)
				for _, result := range []<-chan historyCallResult{firstResult, secondResult} {
					response := historyResult(t, result)
					if response.err != nil || response.facts != (HistoryServingFacts{ServingFactsUnavailableReason: "older-source"}) || response.boot != "daemonless" || response.epoch != 9 || !response.authoritative {
						t.Fatalf("legacy response was changed: %+v", response)
					}
				}
				later := startHistoryCall(ctx, client, firstMethod)
				replyHistory(t, transport, historyFrame(t, transport), true)
				if response := historyResult(t, later); response.err != nil {
					t.Fatal(response.err)
				}
				if len(transport.writes) != 0 {
					t.Fatal("degraded reads left an extra request or retry loop")
				}
			})
		})
	}
}

func legacyHistoryMethod(method string) string {
	if method == MethodThreadHistoryRead {
		return MethodThreadRead
	}
	return MethodThreadTurnsList
}

func TestClientHistoryConcurrentInitialCallCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := newMemoryTransport()
		client := NewClient(transport)
		client.Start(ctx)
		probeResult := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		probe := historyFrame(t, transport)
		waitCtx, stopWait := context.WithCancel(ctx)
		waiter := startHistoryCall(waitCtx, client, MethodThreadHistoryTurnsList)
		if request := historyFrame(t, transport); request.Method != MethodThreadHistoryTurnsList {
			t.Fatalf("concurrent initial method=%q", request.Method)
		}
		stopWait()
		response := historyResult(t, waiter)
		var notSent RequestNotSentError
		if errors.As(response.err, &notSent) || !errors.Is(response.err, context.Canceled) {
			t.Fatalf("concurrent call error=%T %v, want canceled after sending", response.err, response.err)
		}
		if len(transport.writes) != 0 {
			t.Fatal("canceled concurrent call triggered another request")
		}
		replyHistory(t, transport, probe, false)
		if response := historyResult(t, probeResult); response.err != nil {
			t.Fatal(response.err)
		}
	})
}

func TestClientHistoryCancelledProbeLetsWaiterNegotiate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := newMemoryTransport()
		client := NewClient(transport)
		client.Start(ctx)
		probeCtx, stopProbe := context.WithCancel(ctx)
		probeResult := startHistoryCall(probeCtx, client, MethodThreadHistoryRead)
		_ = historyFrame(t, transport)
		waiter := startHistoryCall(ctx, client, MethodThreadHistoryTurnsList)
		synctest.Wait()
		stopProbe()
		if response := historyResult(t, probeResult); !errors.Is(response.err, context.Canceled) {
			t.Fatalf("probe error=%v", response.err)
		}
		next := historyFrame(t, transport)
		if next.Method != MethodThreadHistoryTurnsList {
			t.Fatalf("after cancellation method=%q", next.Method)
		}
		transport.reads <- ErrorMessage(next.ID, MethodNotFound(next.Method))
		replyHistory(t, transport, historyFrame(t, transport), true)
		if response := historyResult(t, waiter); response.err != nil || response.facts.ServingFactsUnavailableReason != "older-source" {
			t.Fatalf("waiter=%+v", response)
		}
	})
}

func TestClientHistorySupportedRequestsRemainConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := newMemoryTransport()
		client := NewClient(transport)
		client.Start(ctx)
		probeResult := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		replyHistory(t, transport, historyFrame(t, transport), false)
		if response := historyResult(t, probeResult); response.err != nil {
			t.Fatal(response.err)
		}
		read := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		page := startHistoryCall(ctx, client, MethodThreadHistoryTurnsList)
		synctest.Wait()
		if len(transport.writes) != 2 {
			t.Fatalf("supported requests in flight=%d want 2 before either reply", len(transport.writes))
		}
		for range 2 {
			replyHistory(t, transport, historyFrame(t, transport), false)
		}
		for _, result := range []<-chan historyCallResult{read, page} {
			if response := historyResult(t, result); response.err != nil || response.facts.InstanceID != "instance-A" {
				t.Fatalf("supported result=%+v", response)
			}
		}
	})
}

func TestClientHistoryNonMethodErrorDoesNotDegrade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := newMemoryTransport()
		client := NewClient(transport)
		client.Start(ctx)
		firstResult := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		first := historyFrame(t, transport)
		wire := SessionUnavailable("fixture")
		transport.reads <- ErrorMessage(first.ID, wire)
		if response := historyResult(t, firstResult); response.err == nil || historyMethodMissing(response.err) {
			t.Fatalf("non-method result=%+v", response)
		}
		if len(transport.writes) != 0 {
			t.Fatal("non-method error triggered legacy retry")
		}
		nextResult := startHistoryCall(ctx, client, MethodThreadHistoryTurnsList)
		next := historyFrame(t, transport)
		if next.Method != MethodThreadHistoryTurnsList {
			t.Fatalf("after non-method error method=%q", next.Method)
		}
		replyHistory(t, transport, next, false)
		if response := historyResult(t, nextResult); response.err != nil {
			t.Fatal(response.err)
		}
	})
}

func TestClientHistoryInitialReadsRemainConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := newMemoryTransport()
		client := NewClient(transport)
		client.Start(ctx)
		first := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		second := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		synctest.Wait()
		if len(transport.writes) != 2 {
			t.Fatalf("initial reads in flight=%d, want 2 before either response", len(transport.writes))
		}
		for range 2 {
			replyHistory(t, transport, historyFrame(t, transport), false)
		}
		for _, result := range []<-chan historyCallResult{first, second} {
			if response := historyResult(t, result); response.err != nil || response.facts.InstanceID != "instance-A" {
				t.Fatalf("initial result=%+v", response)
			}
		}
	})
}

func TestClientHistoryLateInitialSuccessDoesNotUndoLegacy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := newMemoryTransport()
		client := NewClient(transport)
		client.Start(ctx)
		first := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		firstRequest := historyFrame(t, transport)
		late := startHistoryCall(ctx, client, MethodThreadHistoryTurnsList)
		lateRequest := historyFrame(t, transport)
		transport.reads <- ErrorMessage(firstRequest.ID, MethodNotFound(firstRequest.Method))
		replyHistory(t, transport, historyFrame(t, transport), true)
		if response := historyResult(t, first); response.err != nil || response.facts.ServingFactsUnavailableReason != "older-source" {
			t.Fatalf("degraded response=%+v", response)
		}
		replyHistory(t, transport, lateRequest, false)
		if response := historyResult(t, late); response.err != nil || response.facts.InstanceID != "instance-A" {
			t.Fatalf("late supported response=%+v", response)
		}
		for _, method := range []string{MethodThreadHistoryRead, MethodThreadHistoryTurnsList} {
			result := startHistoryCall(ctx, client, method)
			replyHistory(t, transport, historyFrame(t, transport), true)
			if response := historyResult(t, result); response.err != nil || response.facts.ServingFactsUnavailableReason != "older-source" {
				t.Fatalf("response after late success=%+v", response)
			}
		}
	})
}

func TestClientHistoryNewConnectionCanProbeAfterLegacy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		oldCtx, closeOld := context.WithCancel(t.Context())
		oldTransport := newMemoryTransport()
		old := NewClient(oldTransport)
		old.Start(oldCtx)
		result := startHistoryCall(oldCtx, old, MethodThreadHistoryRead)
		request := historyFrame(t, oldTransport)
		oldTransport.reads <- ErrorMessage(request.ID, MethodNotFound(request.Method))
		replyHistory(t, oldTransport, historyFrame(t, oldTransport), true)
		if response := historyResult(t, result); response.err != nil {
			t.Fatal(response.err)
		}
		closeOld()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transport := newMemoryTransport()
		client := NewClient(transport)
		client.Start(ctx)
		next := startHistoryCall(ctx, client, MethodThreadHistoryTurnsList)
		replyHistory(t, transport, historyFrame(t, transport), false)
		if response := historyResult(t, next); response.err != nil || response.facts.InstanceID != "instance-A" {
			t.Fatalf("new connection result=%+v", response)
		}
	})
}

type historySendFailureTransport struct {
	*memoryTransport
	failure error
}

func (t *historySendFailureTransport) Send(ctx context.Context, msg Message) error {
	if t.failure != nil {
		err := t.failure
		t.failure = nil
		return err
	}
	return t.memoryTransport.Send(ctx, msg)
}

func TestClientHistoryGenericMethodNotFoundDoesNotDegrade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		failure := errors.New("method not found")
		transport := &historySendFailureTransport{memoryTransport: newMemoryTransport(), failure: failure}
		client := NewClient(transport)
		client.Start(ctx)
		result := startHistoryCall(ctx, client, MethodThreadHistoryRead)
		if response := historyResult(t, result); !errors.Is(response.err, failure) {
			t.Fatalf("generic failure=%v, want original transport cause", response.err)
		}
		if len(transport.writes) != 0 {
			t.Fatal("generic error triggered a legacy retry")
		}
		next := startHistoryCall(ctx, client, MethodThreadHistoryTurnsList)
		replyHistory(t, transport.memoryTransport, historyFrame(t, transport.memoryTransport), false)
		if response := historyResult(t, next); response.err != nil {
			t.Fatal(response.err)
		}
	})
}
