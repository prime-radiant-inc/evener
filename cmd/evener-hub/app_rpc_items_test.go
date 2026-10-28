package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRPCItemReadAndListUseFinalPacker(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "codex:item-packing", Incarnation: "rpc-item-packing", ProjectionVersion: 1}
	turns, err := appitempaging.RegroupTurnFragments(testItemCandidates(45))
	if err != nil {
		t.Fatalf("group fixture: %v", err)
	}
	olderCursor, err := appitempaging.EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 0, Item: 0})
	if err != nil {
		t.Fatalf("encode fixture cursor: %v", err)
	}
	thread := appwire.Thread{
		ID:        "item-packing",
		SessionID: "item-packing",
		Source:    "codex",
		CWD:       "/tmp/item-packing",
		Evener:    appwire.EvenerThread{Ref: "codex:item-packing"},
		Turns:     turns,
	}
	source := &itemPackingRPCSource{
		read: appwire.ThreadReadResponse{
			Thread:      thread,
			OlderCursor: olderCursor,
		},
		list: appwire.ThreadTurnsListResponse{
			Data:       turns,
			NextCursor: olderCursor,
		},
		readCandidates: appsource.ItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(45), OlderCursor: olderCursor},
			Identity:   identity,
			Exhausted:  false,
		},
		listCandidates: func(context.Context, appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
			return appsource.ItemCandidateResult{
				Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(45), OlderCursor: olderCursor},
				Identity:   identity,
				Exhausted:  false,
			}, nil
		},
		rejectLegacyItemList: true,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(hubcore.WebConfig{}, sources)

	readRaw, err := json.Marshal(appwire.ThreadReadParams{
		Ref:          "codex:item-packing",
		IncludeTurns: true,
		ItemLimit:    40,
	})
	if err != nil {
		t.Fatal(err)
	}
	readValue, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodThreadRead,
		Params: readRaw,
	})
	if err != nil {
		t.Fatalf("item thread/read: %v", err)
	}
	read, ok := readValue.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("item thread/read response = %T", readValue)
	}
	if got := len(flattenTestItems(read.Thread.Turns)); got != 40 {
		t.Fatalf("item thread/read count = %d, want 40", got)
	}

	listRaw, err := json.Marshal(appwire.ThreadTurnsListParams{
		Ref:       "codex:item-packing",
		Cursor:    olderCursor,
		ItemLimit: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	listValue, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(2),
		Method: appwire.MethodThreadTurnsList,
		Params: listRaw,
	})
	if err != nil {
		t.Fatalf("item thread/turns/list: %v", err)
	}
	list, ok := listValue.(appwire.ThreadTurnsListResponse)
	if !ok {
		t.Fatalf("item thread/turns/list response = %T", listValue)
	}
	if got := len(flattenTestItems(list.Data)); got != 40 {
		t.Fatalf("item thread/turns/list count = %d, want 40", got)
	}
	if read.OlderCursor == "" || list.NextCursor == "" {
		t.Fatal("item pages did not retain an opaque older cursor")
	}
	if source.candidateReadCalls != 0 || source.candidateListCalls != 1 {
		t.Fatalf("candidate source calls = read %d/list %d, want read 0/list 1", source.candidateReadCalls, source.candidateListCalls)
	}
}

func TestHubRPCItemReadAndListHonorSmallerRequestedLimit(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "codex:small-limit", Incarnation: "small-limit", ProjectionVersion: 1}
	candidates := testItemCandidates(45)
	turns, err := appitempaging.RegroupTurnFragments(candidates)
	if err != nil {
		t.Fatalf("group fixture: %v", err)
	}
	sourceCursor, err := appitempaging.EncodeCursor(identity, candidates[0].Position)
	if err != nil {
		t.Fatalf("encode source cursor: %v", err)
	}
	source := &itemPackingRPCSource{
		read: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "small-limit", SessionID: "small-limit", Source: "codex", Evener: appwire.EvenerThread{Ref: identity.ThreadRef}, Turns: turns,
		}, OlderCursor: sourceCursor},
		readCandidates: appsource.ItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates}, Identity: identity, Exhausted: true,
		},
		listCandidates: func(context.Context, appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
			return appsource.ItemCandidateResult{Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates}, Identity: identity, Exhausted: true}, nil
		},
		rejectLegacyItemList: true,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(hubcore.WebConfig{}, sources)

	readValue, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead,
		Params: mustPagingJSON(t, appwire.ThreadReadParams{Ref: identity.ThreadRef, IncludeTurns: true, ItemLimit: 3}),
	})
	if err != nil {
		t.Fatalf("small-limit thread/read: %v", err)
	}
	read, ok := readValue.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("small-limit thread/read response = %T", readValue)
	}
	if got := len(flattenTestItems(read.Thread.Turns)); got != 3 {
		t.Fatalf("small-limit thread/read items = %d, want 3", got)
	}

	listValue, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(2), Method: appwire.MethodThreadTurnsList,
		Params: mustPagingJSON(t, appwire.ThreadTurnsListParams{Ref: identity.ThreadRef, Cursor: sourceCursor, ItemLimit: 3}),
	})
	if err != nil {
		t.Fatalf("small-limit thread/turns/list: %v", err)
	}
	list, ok := listValue.(appwire.ThreadTurnsListResponse)
	if !ok {
		t.Fatalf("small-limit thread/turns/list response = %T", listValue)
	}
	if got := len(flattenTestItems(list.Data)); got != 3 {
		t.Fatalf("small-limit thread/turns/list items = %d, want 3", got)
	}
}

func TestHubRPCInitialItemReadRejectsCompleteLegacyV3Metadata(t *testing.T) {
	const (
		sessionID = "02wMz5Txv733WHFsVy66SR"
		routeRef  = "local:legacy-initial-workspace"
		hubToken  = "legacy-initial-token"
	)
	legacyTurns := []appwire.Turn{
		{ID: "turn-0", Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "item-0-0", Text: "oldest"},
			{Type: "agentMessage", ID: "item-0-1", Text: "older"},
		}},
		{ID: "turn-with-zero-items"},
		{ID: "turn-2", Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "item-2-0", Text: "newer"},
			{Type: "agentMessage", ID: "item-2-1", Text: "newest"},
		}},
	}
	daemonRequest := make(chan appwire.ThreadReadParams, 1)
	authHeader := make(chan string, 1)
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "legacy-initial-v3-daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		daemonRequest <- params
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref}, Turns: legacyTurns,
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader <- r.Header.Get("Authorization")
		daemon.ServeWebSocket(w, r)
	}))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: 22 * 1000, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):],
		SourceID: "local", ThreadID: sessionID, SessionID: sessionID, WorkspaceRef: routeRef,
		InstanceID: "legacy-initial-v3-instance", HubToken: hubToken,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref: routeRef, ThreadID: sessionID, IncludeTurns: true, ItemsView: string(appwire.TurnItemsViewFull), ItemLimit: 3,
	})
	if err == nil {
		t.Fatalf("legacy-v3 initial item read = %+v, want the unpositioned items refused", response)
	}
	request := <-daemonRequest
	if request.Ref != routeRef || request.ThreadID != sessionID || !request.IncludeTurns || request.ItemsView != string(appwire.TurnItemsViewFull) || request.ItemLimit != 3 {
		t.Fatalf("legacy-v3 daemon request = %+v, want routed resolved item request", request)
	}
	if got := <-authHeader; got != "Bearer "+hubToken {
		t.Fatalf("legacy-v3 daemon authorization = %q, want bearer token", got)
	}
}

func TestHubRPCItemListPreservesSavedErrorsAndCursorFallback(t *testing.T) {
	cfg, entry := seedPastItemPagingThread(t)
	sessionID := entry.Meta.ID
	ref := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	savedFirst, found, err := pastThreadReadResponse(context.Background(), cfg, appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, ItemLimit: 40,
	})
	if err != nil || !found || len(savedFirst.Thread.Turns) == 0 || savedFirst.OlderCursor == "" {
		t.Fatalf("saved item fixture = (%+v, found=%v, err=%v), want non-empty page with continuation", savedFirst, found, err)
	}
	savedSecond, found, err := pastThreadTurnsList(context.Background(), cfg, appwire.ThreadTurnsListParams{
		Ref: ref, ItemLimit: 40, Cursor: savedFirst.OlderCursor,
	})
	if err != nil || !found || len(savedSecond.Data) == 0 {
		t.Fatalf("saved continuation fixture = (%+v, found=%v, err=%v), want non-empty page", savedSecond, found, err)
	}

	identity := appitempaging.CursorIdentity{ThreadRef: ref, Incarnation: "live-terminal", ProjectionVersion: 1}
	liveCursor, err := appitempaging.EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 100, Item: 0})
	if err != nil {
		t.Fatalf("encode live terminal cursor: %v", err)
	}
	var candidateCursors []string
	source := &localItemPackingRPCSource{itemPackingRPCSource{
		read: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: ref},
		}},
		listCandidates: func(_ context.Context, params appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
			candidateCursors = append(candidateCursors, params.Cursor)
			switch params.Cursor {
			case liveCursor:
				return appsource.ItemCandidateResult{Identity: identity, Exhausted: true}, nil
			case savedFirst.OlderCursor:
				return appsource.ItemCandidateResult{}, errors.New("cursor is not owned by live source")
			default:
				return appsource.ItemCandidateResult{}, fmt.Errorf("unexpected candidate cursor %q", params.Cursor)
			}
		},
		rejectLegacyItemList: true,
	}}
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(cfg, sources)

	dispatch := func(cursor string) (appwire.ThreadTurnsListResponse, error) {
		value, dispatchErr := server.Router().Dispatch(context.Background(), appwire.Request{
			ID: appwire.NewIntID(1), Method: appwire.MethodThreadTurnsList,
			Params: mustPagingJSON(t, appwire.ThreadTurnsListParams{
				Ref: ref, ItemLimit: 40, Cursor: cursor,
			}),
		})
		if dispatchErr != nil {
			return appwire.ThreadTurnsListResponse{}, dispatchErr
		}
		response, ok := value.(appwire.ThreadTurnsListResponse)
		if !ok {
			t.Fatalf("thread/turns/list response = %T", value)
		}
		return response, nil
	}

	_, err = dispatch(liveCursor)
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("source-accepted terminal cursor error = %T %v, want saved stale-cursor WireError", err, err)
	}
	data, ok := wireErr.Data.(appwire.HistoryReadErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale || data.BootGeneration != appwire.DaemonlessBootGeneration {
		t.Fatalf("source-accepted terminal cursor error data = %#v, want the saved read's daemonless stale-cursor info", wireErr.Data)
	}

	fallback, err := dispatch(savedFirst.OlderCursor)
	if err != nil {
		t.Fatalf("saved-owned cursor fallback: %v", err)
	}
	if !reflect.DeepEqual(fallback, savedSecond) {
		t.Fatalf("saved-owned cursor fallback = %+v, want %+v", fallback, savedSecond)
	}
	if !slices.Equal(candidateCursors, []string{liveCursor, savedFirst.OlderCursor}) {
		t.Fatalf("candidate cursors = %v, want live then saved", candidateCursors)
	}
}

type localItemPackingRPCSource struct {
	itemPackingRPCSource
}

func (*localItemPackingRPCSource) ID() string { return "local" }

type itemPackingRPCSource struct {
	relayLifecycleSource
	read                 appwire.ThreadReadResponse
	list                 appwire.ThreadTurnsListResponse
	readCandidates       appsource.ItemCandidateResult
	listCandidates       func(context.Context, appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error)
	candidateReadCalls   int
	candidateListCalls   int
	rejectLegacyItemList bool
}

func (s *itemPackingRPCSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return s.read, nil
}

func (*itemPackingRPCSource) RelayOnThreadRead() bool { return false }

func (s *itemPackingRPCSource) ListTurns(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	if s.rejectLegacyItemList {
		return appwire.ThreadTurnsListResponse{}, errors.New("legacy item list path must not be called")
	}
	return s.list, nil
}

func (s *itemPackingRPCSource) ReadItemCandidates(context.Context, appwire.ThreadReadParams) (appsource.ItemCandidateResult, error) {
	s.candidateReadCalls++
	return s.readCandidates, nil
}

func (s *itemPackingRPCSource) ItemCandidatesFromRead(context.Context, appwire.ThreadReadParams, appwire.ThreadReadResponse) (appsource.ItemCandidateResult, error) {
	return s.readCandidates, nil
}

func (s *itemPackingRPCSource) ListItemCandidates(ctx context.Context, params appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
	s.candidateListCalls++
	if s.listCandidates != nil {
		return s.listCandidates(ctx, params)
	}
	return s.readCandidates, nil
}

type metadataItemReadRPCSource struct {
	itemPackingRPCSource
	metadata appwire.ThreadReadResponse
}

func (s *metadataItemReadRPCSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if !params.IncludeTurns {
		return s.metadata, nil
	}
	return s.read, nil
}

func TestHubRPCItemMetadataReadPreservesMetadata(t *testing.T) {
	metadata := appwire.ThreadReadResponse{
		Thread:      appwire.Thread{ID: "item-metadata", SessionID: "item-metadata", Source: "codex", Evener: appwire.EvenerThread{Ref: "codex:item-metadata"}},
		OlderCursor: "metadata-cursor",
	}
	newServer := func(metadata appwire.ThreadReadResponse) *appserver.Server {
		source := &metadataItemReadRPCSource{
			read:     appwire.ThreadReadResponse{Thread: metadata.Thread},
			metadata: metadata,
		}
		sources := appsource.NewRegistry()
		sources.Add(source)
		return newHubAppServer(hubcore.WebConfig{}, sources)
	}

	t.Run("successful metadata response stamps item mode", func(t *testing.T) {
		value, err := newServer(metadata).Router().Dispatch(context.Background(), appwire.Request{
			ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead,
			Params: mustPagingJSON(t, appwire.ThreadReadParams{Ref: "codex:item-metadata", ItemLimit: 7}),
		})
		if err != nil {
			t.Fatalf("metadata item read: %v", err)
		}
		response, ok := value.(appwire.ThreadReadResponse)
		if !ok {
			t.Fatalf("metadata item read response = %T", value)
		}
		if response.Thread.Turns != nil {
			t.Fatalf("metadata turns = %+v, want nil", response.Thread.Turns)
		}
		if response.OlderCursor != metadata.OlderCursor {
			t.Fatalf("metadata cursor = %q, want %q", response.OlderCursor, metadata.OlderCursor)
		}
	})

	t.Run("metadata response carrying a full turn is rejected", func(t *testing.T) {
		invalid := metadata
		invalid.Thread.Turns = []appwire.Turn{{ID: "full-turn", ItemsView: appwire.TurnItemsViewFull}}
		_, err := newServer(invalid).Router().Dispatch(context.Background(), appwire.Request{
			ID: appwire.NewIntID(2), Method: appwire.MethodThreadRead,
			Params: mustPagingJSON(t, appwire.ThreadReadParams{Ref: "codex:item-metadata"}),
		})
		if err == nil {
			t.Fatal("metadata item read accepted a full turn while IncludeTurns was false")
		}
	})
}

type metadataErrorItemTurnsSource struct {
	itemPackingRPCSource
	metadataErr error
}

func (s *metadataErrorItemTurnsSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, s.metadataErr
}

func TestListItemTurnsPreservesPackedResponseAndLogsMetadataError(t *testing.T) {
	sentinel := errors.New("metadata sentinel")
	source := &metadataErrorItemTurnsSource{
		readCandidates: appsource.ItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(1)},
			Identity:   appitempaging.CursorIdentity{ThreadRef: "codex:metadata-error", Incarnation: "metadata-error", ProjectionVersion: 1},
			Exhausted:  true,
		},
		metadataErr: sentinel,
	}
	var logs []struct {
		format string
		args   []any
	}
	logf := func(format string, args ...any) {
		logs = append(logs, struct {
			format string
			args   []any
		}{format: format, args: args})
	}

	response, handled, err := listItemTurns(context.Background(), source, appwire.ThreadTurnsListParams{
		Ref: "codex:metadata-error",
	}, logf)
	if err != nil {
		t.Fatalf("listItemTurns: %v", err)
	}
	if !handled {
		t.Fatal("listItemTurns handled = false, want true")
	}
	items := flattenTestItems(response.Data)
	if len(items) != 1 || items[0].ID != "item-00" {
		t.Fatalf("packed items = %+v, want the valid source item", items)
	}
	for _, entry := range logs {
		for _, arg := range entry.args {
			if loggedErr, ok := arg.(error); ok && errors.Is(loggedErr, sentinel) {
				return
			}
		}
	}
	t.Fatalf("logger did not receive sentinel error as an argument: %+v", logs)
}

// metadataErrorRemoteImageRPCSource is a source whose images live on another hub
// (EnrichThreadFileBackedImages reports they are not files on this hub) and whose
// optional metadata read fails.
type metadataErrorRemoteImageRPCSource struct {
	itemPackingRPCSource
}

func (*metadataErrorRemoteImageRPCSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, errors.New("metadata sentinel")
}

func (*metadataErrorRemoteImageRPCSource) EnrichThreadFileBackedImages() bool { return false }

// Remote image routes must be neutralized independently of the optional metadata
// read. The routes arrive on the item page itself, and a root-relative route left
// on the response resolves against this hub's origin for a session this hub does
// not have (404, or another session's bytes on an id collision). Metadata only
// supplies the session id and CWD the local stamp/file-backed pass needs, so a
// failed metadata read must not skip the neutralization that does not need them.
// A source that translates its own payload (RemoteHubSource) has already
// rewritten its stamped routes onto the host-qualified controller form by the
// time this pass runs; the pass still strips a bare-session route no translator
// left behind.
func TestListItemTurnsNeutralizesRemoteImageRoutesWithoutMetadata(t *testing.T) {
	const external = "https://images.example.test/plot.png"
	route := "/s/remote-session/images/" + strings.Repeat("a", 64)
	candidates := testItemCandidates(1)
	candidates[0].Item.Images = []appwire.InputItem{{URL: route}, {URL: external}}
	candidates[0].Item.OutputImages = []appwire.OutputImage{
		{Source: "tool-result", URL: route},
		{Source: "written-file", Path: "plot.png", URL: "/doc/image?session=remote-session&path=plot.png"},
	}
	source := &metadataErrorRemoteImageRPCSource{itemPackingRPCSource{
		readCandidates: appsource.ItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
			Identity:   appitempaging.CursorIdentity{ThreadRef: "h1:t1", Incarnation: "route-incarnation", ProjectionVersion: 1},
			Exhausted:  true,
		},
	}}

	response, handled, err := listItemTurns(context.Background(), source, appwire.ThreadTurnsListParams{Ref: "h1:t1"}, nil)
	if err != nil {
		t.Fatalf("listItemTurns: %v", err)
	}
	if !handled {
		t.Fatal("listItemTurns handled = false, want true")
	}
	items := flattenTestItems(response.Data)
	if len(items) != 1 {
		t.Fatalf("packed items = %+v, want the single source item", items)
	}
	if len(items[0].Images) != 2 {
		t.Fatalf("input images = %+v, want both remote descriptors preserved", items[0].Images)
	}
	if got := items[0].Images[0].URL; got != "" {
		t.Fatalf("remote input image route = %q, want the controller-relative route neutralized", got)
	}
	if got := items[0].Images[1].URL; got != external {
		t.Fatalf("external input image URL = %q, want %q preserved", got, external)
	}
	if len(items[0].OutputImages) != 2 {
		t.Fatalf("output images = %+v, want both remote descriptors preserved", items[0].OutputImages)
	}
	if got := items[0].OutputImages[0].URL; got != "" {
		t.Fatalf("remote output image route = %q, want it neutralized", got)
	}
	if got := items[0].OutputImages[1].URL; got != "" {
		t.Fatalf("remote /doc/image URL = %q, want it neutralized (it would resolve against the controller origin)", got)
	}
	if got := items[0].OutputImages[1].Path; got != "plot.png" {
		t.Fatalf("remote /doc/image path = %q, want the relative path preserved for the staged proxy", got)
	}
}

func TestHubRPCItemByteTrimReturnsExcludedCandidateExactlyOnce(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "codex:byte-packing", Incarnation: "rpc-byte-packing", ProjectionVersion: 1}
	candidates := testItemCandidates(2)
	for i := range candidates {
		candidates[i].Item.Text = strings.Repeat("x", 600_000)
	}
	turns, err := appitempaging.RegroupTurnFragments(candidates)
	if err != nil {
		t.Fatalf("group response-derived candidates: %v", err)
	}
	sourceCursor, err := appitempaging.EncodeCursor(identity, candidates[0].Position)
	if err != nil {
		t.Fatalf("encode source cursor: %v", err)
	}
	all := appsource.ItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates, OlderCursor: sourceCursor},
		Identity:   identity,
		Exhausted:  false,
	}
	thread := appwire.Thread{
		ID:     "byte-packing",
		Source: "codex",
		Evener: appwire.EvenerThread{Ref: identity.ThreadRef},
		Turns:  turns,
	}
	source := &itemPackingRPCSource{
		read:                 appwire.ThreadReadResponse{Thread: thread, OlderCursor: sourceCursor},
		readCandidates:       all,
		rejectLegacyItemList: true,
		listCandidates: func(_ context.Context, params appwire.ThreadTurnsListParams) (appsource.ItemCandidateResult, error) {
			if params.Cursor == "" {
				return all, nil
			}
			before, err := appitempaging.DecodeCursor(params.Cursor, identity)
			if err != nil {
				return appsource.ItemCandidateResult{}, err
			}
			selected, hasOlder, err := appitempaging.SelectCandidates(candidates, &before, params.ItemLimit)
			if err != nil {
				return appsource.ItemCandidateResult{}, err
			}
			window := appitempaging.TranscriptItemWindow{Candidates: selected}
			if hasOlder && len(selected) > 0 {
				window.OlderCursor, err = appitempaging.EncodeCursor(identity, selected[0].Position)
				if err != nil {
					return appsource.ItemCandidateResult{}, err
				}
			}
			return appsource.ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: !hasOlder}, nil
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(hubcore.WebConfig{}, sources)

	readRaw, err := json.Marshal(appwire.ThreadReadParams{
		Ref:          identity.ThreadRef,
		IncludeTurns: true,
		ItemLimit:    40,
	})
	if err != nil {
		t.Fatal(err)
	}
	readValue, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodThreadRead,
		Params: readRaw,
	})
	if err != nil {
		t.Fatalf("item thread/read: %v", err)
	}
	read, ok := readValue.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("item thread/read response = %T", readValue)
	}
	readItems := flattenTestItems(read.Thread.Turns)
	if len(readItems) != 1 || readItems[0].ID != "item-01" {
		t.Fatalf("byte-trimmed read items = %+v, want only newest item-01", readItems)
	}
	if read.OlderCursor == "" {
		t.Fatal("byte-trimmed read omitted excluded-item cursor")
	}
	before, err := appitempaging.DecodeCursor(read.OlderCursor, identity)
	if err != nil {
		t.Fatalf("decode response-derived rebased cursor: %v", err)
	}
	if before != candidates[1].Position {
		t.Fatalf("response-derived rebased cursor = %+v, want oldest returned position %+v", before, candidates[1].Position)
	}

	listRaw, err := json.Marshal(appwire.ThreadTurnsListParams{
		Ref:       identity.ThreadRef,
		ItemLimit: 40,
		Cursor:    read.OlderCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	listValue, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(2),
		Method: appwire.MethodThreadTurnsList,
		Params: listRaw,
	})
	if err != nil {
		t.Fatalf("item thread/turns/list: %v", err)
	}
	list, ok := listValue.(appwire.ThreadTurnsListResponse)
	if !ok {
		t.Fatalf("item thread/turns/list response = %T", listValue)
	}
	listItems := flattenTestItems(list.Data)
	if len(listItems) != 1 || listItems[0].ID != "item-00" || list.NextCursor != "" {
		t.Fatalf("older byte-trimmed items = %+v, cursor=%q, want only item-00 with no cursor", listItems, list.NextCursor)
	}
	if source.candidateReadCalls != 0 || source.candidateListCalls != 1 {
		t.Fatalf("candidate source calls = read %d/list %d, want read 0/list 1", source.candidateReadCalls, source.candidateListCalls)
	}
}
