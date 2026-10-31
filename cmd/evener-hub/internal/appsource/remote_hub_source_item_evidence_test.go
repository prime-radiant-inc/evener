package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// The tests here cover the remote hub source's item-read path, its shared call
// and attach-error classification guards, and the paging-validation edge arms
// that only a malformed or hostile remote answer reaches.

func TestRemoteHubSourceReadItemCandidatesForcesItemModeAndStripsSubscription(t *testing.T) {
	t.Parallel()
	readCursor := remoteItemCursor(t, 10)
	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("unexpected remote method %q", method)
			return scriptedReply{}
		}
		var remote appwire.ThreadReadParams
		if err := json.Unmarshal(params, &remote); err != nil {
			t.Errorf("decode read params: %v", err)
		}
		if !remote.IncludeTurns {
			t.Error("item read did not force IncludeTurns")
		}
		if remote.Subscribe {
			t.Error("item read forwarded the subscription flag")
		}
		if remote.Ref != "local:t1" {
			t.Errorf("item read ref = %q, want the translated remote-local ref", remote.Ref)
		}
		return scriptedReply{result: appwire.ThreadReadResponse{
			Thread:      appwire.Thread{Turns: itemPageWithCursor(10, "").Data},
			OlderCursor: readCursor,
		}}
	})

	result, err := source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{
		Ref: "host:t1", Subscribe: true,
	})
	if err != nil {
		t.Fatalf("ReadItemCandidates: %v", err)
	}
	if result.Exhausted || result.Candidates.OlderCursor == "" {
		t.Fatalf("item read result = %+v, want a live continuation cursor", result)
	}
	if result.Identity.ThreadRef == "" || result.Identity.Incarnation == "" || result.Identity.ProjectionVersion == 0 {
		t.Fatalf("item read identity = %+v, want a complete controller identity", result.Identity)
	}
	if _, err := appitempaging.DecodeCursor(result.Candidates.OlderCursor, result.Identity); err != nil {
		t.Fatalf("decode read-minted cursor: %v", err)
	}
	if len(wireCalls(calls())) == 0 {
		t.Fatal("item read made no remote call")
	}
}

func TestRemoteHubSourceReadItemCandidatesEmptyPageMintsIdentity(t *testing.T) {
	t.Parallel()
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("unexpected remote method %q", method)
			return scriptedReply{}
		}
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	result, err := source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"})
	if err != nil {
		t.Fatalf("ReadItemCandidates with an empty page: %v", err)
	}
	if !result.Exhausted || result.Candidates.OlderCursor != "" {
		t.Fatalf("empty item read result = %+v, want an exhausted page", result)
	}
	if result.Identity.Incarnation == "" {
		t.Fatalf("empty item read identity = %+v, want a freshly minted incarnation", result.Identity)
	}
}

func TestRemoteHubSourceCallGuardsKeepCallerCancellationRaw(t *testing.T) {
	t.Parallel()
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out appwire.ThreadReadResponse
	if err := source.call(ctx, appwire.MethodThreadRead, appwire.ThreadReadParams{}, &out); !errors.Is(err, context.Canceled) {
		t.Fatalf("call with a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := source.resolveClient(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("resolveClient with a cancelled context = %v, want context.Canceled", err)
	}
	client, err := source.resolveClient(context.Background())
	if err != nil {
		t.Fatalf("resolveClient: %v", err)
	}
	if err := source.callOn(ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{}, &out); !errors.Is(err, context.Canceled) {
		t.Fatalf("callOn with a cancelled context = %v, want context.Canceled", err)
	}
	var raw json.RawMessage
	if err := source.AdminCall(ctx, "hub/admin", json.RawMessage("{}"), &raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("AdminCall with a cancelled context = %v, want context.Canceled", err)
	}

	if err := source.mapCallError(nil); err != nil {
		t.Fatalf("mapCallError(nil) = %v, want nil", err)
	}
	if err := source.mapConnectError(nil); err != nil {
		t.Fatalf("mapConnectError(nil) = %v, want nil", err)
	}
	if err := source.mapConnectError(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("mapConnectError(canceled) = %v, want it raw", err)
	}
	if err := source.MapAttachError(nil); err != nil {
		t.Fatalf("MapAttachError(nil) = %v, want nil", err)
	}
	if err := source.transportUnavailable(nil); err != nil {
		t.Fatalf("transportUnavailable(nil) = %v, want nil", err)
	}
}

func TestRemoteHubSourceAttachErrorClassification(t *testing.T) {
	t.Parallel()
	source := &RemoteHubSource{id: "host"}

	for name, err := range map[string]error{
		"dial timeout":       fakeTimeoutError{msg: "dial timed out"},
		"transport text eof": errors.New("peer sent eof"),
	} {
		mapped := source.MapAttachError(err)
		var wire appwire.WireError
		if !errors.As(mapped, &wire) || wire.Code != appwire.CodeUnavailable {
			t.Fatalf("%s: mapped = %T=%v, want a SessionUnavailable wire error", name, mapped, mapped)
		}
	}

	if mapped := source.MapAttachError(context.Canceled); !errors.Is(mapped, context.Canceled) {
		t.Fatalf("caller cancellation = %v, want it raw", mapped)
	}
	plain := errors.New("odd remote failure")
	if mapped := source.MapAttachError(plain); !errors.Is(mapped, plain) {
		t.Fatalf("unclassified failure = %v, want it unchanged", mapped)
	}
}

func TestRemoteHubSourceAdminMutationOutcomeMapping(t *testing.T) {
	t.Parallel()
	source := &RemoteHubSource{id: "host"}

	plain := errors.New("odd remote failure")
	if mapped := source.remoteHubAdminMutationCallError(plain); !errors.Is(mapped, plain) {
		t.Fatalf("unmapped failure = %v, want the classified error unchanged", mapped)
	}

	lost := source.remoteHubAdminMutationCallError(errors.New("connection reset by peer"))
	var wire appwire.WireError
	if !errors.As(lost, &wire) {
		t.Fatalf("transport-shaped mutation loss = %T=%v, want a wire error", lost, lost)
	}
	data, ok := appwire.ErrorDataOf(wire.Data)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("transport-shaped mutation loss data = %+v, want an unknown outcome with blocked retry", data)
	}
}

func remoteTestCandidates(t *testing.T) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	candidates, err := appitempaging.CandidatesFromTurns(itemPageWithPositions("", 5, 4).Data)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("test candidates = (%v, %v), want two positioned items", candidates, err)
	}
	return candidates
}

func TestRemoteItemPagingValidationEdgeArms(t *testing.T) {
	t.Parallel()
	candidates := remoteTestCandidates(t)
	before := appwire.ThreadItemPosition{Entry: 5}

	if err := validateRemotePageCursor("not-a-cursor", candidates); err == nil {
		t.Fatal("garbage page cursor was accepted")
	}

	garbageNext := "also-not-a-cursor"
	if _, err := validateRemoteContinuationPage("not-a-cursor", garbageNext, before, candidates); err == nil {
		t.Fatal("garbage request cursor was accepted")
	}
	validRequest := remoteItemCursorFor(t, "inc-a", 5)
	if _, err := validateRemoteContinuationPage(validRequest, garbageNext, before, candidates); err == nil {
		t.Fatal("garbage continuation cursor was accepted")
	}
	foreignNext := remoteItemCursorFor(t, "inc-b", 4)
	if _, err := validateRemoteContinuationPage(validRequest, foreignNext, before, candidates); err == nil {
		t.Fatal("continuation under a foreign identity was accepted")
	}

	if remotePositionsAdjacent((appwire.ThreadItemPosition{Entry: 5, Item: 1}), (appwire.ThreadItemPosition{Entry: 5, Item: 2})) != true ||
		remotePositionsAdjacent((appwire.ThreadItemPosition{Entry: 5, Item: 1}), (appwire.ThreadItemPosition{Entry: 5, Item: 3})) != false ||
		remotePositionsAdjacent((appwire.ThreadItemPosition{Entry: 5, Item: 1}), (appwire.ThreadItemPosition{Entry: 6, Item: 0})) != true ||
		remotePositionsAdjacent((appwire.ThreadItemPosition{Entry: 5, Item: 1}), (appwire.ThreadItemPosition{Entry: 6, Item: 1})) != false {
		t.Fatal("position adjacency table is wrong")
	}

	spans := []remoteItemSpan{{lower: appwire.ThreadItemPosition{Entry: 1}, upper: appwire.ThreadItemPosition{Entry: 2}}}
	if got := remoteItemSpansAfterObserving(nil, spans, nil, nil); len(got) != len(spans) {
		t.Fatalf("empty observation = %+v, want the spans unchanged", got)
	}
	if got := remoteItemSpansForCandidates(nil); got != nil {
		t.Fatalf("spans for an empty page = %+v, want nil", got)
	}
	if _, ok := remoteCandidateAt(candidates, appwire.ThreadItemPosition{Entry: 99}); ok {
		t.Fatal("missing candidate was found")
	}
	if _, ok := remoteItemPageHead(nil); ok {
		t.Fatal("empty page reported a head")
	}
	if got := remoteContiguousPrefix(remoteItemPagingState{}); got != nil {
		t.Fatalf("contiguous prefix of an empty window = %+v, want nil", got)
	}
	unsupported := remoteItemPagingState{
		candidates: []appitempaging.TranscriptItemCandidate{candidates[0]},
	}
	if got := remoteContiguousPrefix(unsupported); len(got) != 1 || got[0].Position != candidates[0].Position {
		t.Fatalf("contiguous prefix without a recorded run = %+v, want the first candidate alone", got)
	}
}

func TestRemoteItemCompletePageContinuationEdgeArms(t *testing.T) {
	t.Parallel()
	candidates := remoteTestCandidates(t)
	identity := remoteCursorIdentity()
	state := remoteItemPagingState{
		identity:   identity,
		candidates: candidates,
		spans:      remoteItemSpansForCandidates(candidates),
		complete:   true,
	}

	if _, err := continueCompleteRemoteItemPage("not-a-cursor", state, 10); err == nil {
		t.Fatal("complete-page continuation accepted a garbage cursor")
	}

	atBottom, err := appitempaging.EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 0})
	if err != nil {
		t.Fatalf("encode bottom cursor: %v", err)
	}
	if _, err := continueCompleteRemoteItemPage(atBottom, state, 10); err == nil {
		t.Fatal("complete-page continuation served a boundary below the window")
	}
}

func TestRemoteHubSourceRecordBeforePageEdgeArms(t *testing.T) {
	t.Parallel()
	source := &RemoteHubSource{id: "host"}
	key := remoteItemPagingKey("host", "t1")
	before := appwire.ThreadItemPosition{Entry: 5}

	// An empty page before a boundary with no retained window answers
	// exhausted under a freshly minted identity.
	empty, err := source.recordRemoteBeforePage(key, before, nil, "", HistoryIdentity{})
	if err != nil {
		t.Fatalf("empty before page: %v", err)
	}
	if !empty.Exhausted || empty.Identity.Incarnation == "" {
		t.Fatalf("empty before page = %+v, want exhausted under a minted identity", empty)
	}

	// An empty page that still names a continuation cursor contradicts its
	// own emptiness and fails closed.
	if _, err := source.recordRemoteBeforePage(key, before, nil, "not-a-cursor", HistoryIdentity{}); err == nil {
		t.Fatal("empty before page with a garbage cursor was accepted")
	}

	// A page whose only item is the boundary itself has nothing older to
	// serve; a cursor that cannot canonically name the page is refused before
	// the window is touched.
	if _, err := source.recordRemoteBeforePage(key, before, remoteTestCandidates(t), "not-a-cursor", HistoryIdentity{}); err == nil {
		t.Fatal("before page with a garbage native cursor was accepted")
	}
}
