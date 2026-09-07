package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/selfupdate"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRPCPluginPreviewRoute(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	registerPluginHandlers(server, newHubPluginsController(t.TempDir(), t.TempDir()))
	out, err := server.Router().Dispatch(context.Background(), appwire.Request{
		Method: appwire.MethodEvenerPluginPreview,
		Params: json.RawMessage(`{"cwd":"/tmp"}`),
	})
	if err != nil {
		t.Fatalf("preview route: %v", err)
	}
	if _, ok := out.(appwire.PluginPreviewResponse); !ok {
		t.Fatalf("preview route response = %T, want PluginPreviewResponse", out)
	}
}

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
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref: routeRef, ThreadID: sessionID, IncludeTurns: true, ItemsView: string(appwire.TurnItemsViewFull), ItemLimit: 3,
	})
	if err == nil || !strings.Contains(err.Error(), "unpositioned item") {
		t.Fatalf("legacy-v3 initial item read = (%+v, %v), want unpositioned item identity error", response, err)
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
	data, ok := wireErr.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
		t.Fatalf("source-accepted terminal cursor error data = %#v, want stale-cursor info", wireErr.Data)
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

func TestHubRPCThreadListUsesAppWireRendezvous(t *testing.T) {
	runDir := t.TempDir()
	entry := rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:  entry,
		Status: appwire.ThreadStatusActive,
		RunningJobs: []appwire.EvenerJobInfo{{
			JobID: "job_shell", JobType: "shell", Status: "running",
		}},
	})

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "test", Version: "test"}})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if init.ProtocolVersion != appwire.ProtocolVersion {
		t.Fatalf("protocol=%q", init.ProtocolVersion)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" || resp.Data[0].Evener.Ref != "local:th_1" {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].Evener.Diagnostics == nil || len(resp.Data[0].Evener.Diagnostics.Jobs) != 1 {
		t.Fatalf("typed hub status omitted running non-agent jobs: %+v", resp.Data[0].Evener.Diagnostics)
	}
	job := resp.Data[0].Evener.Diagnostics.Jobs[0]
	if job.JobID != "job_shell" || job.JobType != "shell" || job.Status != "running" {
		t.Fatalf("typed hub running job = %+v, want shell identity and status", job)
	}
}

func TestHubRPCSteersSurvivingDaemonAfterHubRestart(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		t.Run(fmt.Sprint("cleared=", cleared), func(t *testing.T) { testHubSteersSurvivingDaemon(t, cleared) })
	}
}

func testHubSteersSurvivingDaemon(t *testing.T, cleared bool) {
	const (
		daemonProtocol = appwire.ProtocolVersion
		threadID       = "th_compatible"
		mutationID     = "mutation-compatible-steer"
	)

	steered := make(chan appwire.TurnSteerParams, 1)
	runDir := t.TempDir()
	daemonHTTP := startAppwireTestDaemonWithProtocol(t, runDir, threadID, daemonProtocol, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			steered <- params
			return appwire.TurnSteerResponse{Receipt: appwire.MutationReceipt{
				ClientMutationID: params.ClientMutationID,
				Disposition:      appwire.MutationDispositionApplied,
				ThreadID:         threadID,
				ProjectionState:  appwire.MutationProjectionReflected,
			}}, nil
		})
	})
	defer daemonHTTP.Close()

	resumeID := threadID
	if cleared {
		resumeID = "workspace_before_clear"
		entries, err := rendezvous.List(runDir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("rendezvous entries=%+v error=%v", entries, err)
		}
		entry := entries[0]
		entry.WorkspaceRef = "local:" + resumeID
		writeRendezvous(t, runDir, entry)
	}

	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: threadID, status: appwire.ThreadStatusActive})
	roster.Refresh()

	resumeCalls := 0
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		Past:        hubcore.NewPastIndex(""),
		ResumeLocks: hubcore.NewResumeLocks(),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalls++
			return rendezvous.Entry{}, errors.New("surviving daemon must be reused")
		}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resumed, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: "local:" + resumeID})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resumed.Thread.ID != threadID || resumed.Thread.Evener.Ref != "local:"+threadID || resumeCalls != 0 {
		t.Fatalf("resume = %+v, replacement calls = %d", resumed.Thread, resumeCalls)
	}

	var response appwire.TurnSteerResponse
	err = client.Request(t.Context(), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:                "local:" + threadID,
		ThreadID:           threadID,
		ClientMutationID:   mutationID,
		ExpectedInstanceID: threadID,
		Input:              []appwire.InputItem{{Type: "text", Text: "survived the hub restart"}},
	}, &response)
	if err != nil {
		t.Fatalf("TurnSteer: %v", err)
	}
	if response.Receipt.ClientMutationID != mutationID || response.Receipt.ThreadID != threadID {
		t.Fatalf("receipt = %+v", response.Receipt)
	}
	params := <-steered
	if params.ClientMutationID != mutationID || inputTextForTest(params.Input) != "survived the hub restart" {
		t.Fatalf("daemon steer params = %+v", params)
	}
}

func TestHubRPCAdvertisesTurnListsWithHandlers(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// thread/turns/list is implemented (lazy transcript loading), so the hub
	// must advertise the capability.
	if !init.Features.ThreadTurnsList {
		t.Fatalf("ThreadTurnsList not advertised despite Hub handlers: %+v", init.Features)
	}
}

func TestDeletionFenceRejectsSourceResolution(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(webTestSessionID)
	if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: webTestSessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}
	sources := newHubSourceRegistry(cfg)

	_, err = sourceForThreadWithDeletionFence(cfg, sources, ref, webTestSessionID)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("deleting source resolution error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok ||
		data.MutationOutcome != appwire.MutationOutcomeTargetDeleted ||
		data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("deleting source resolution wire error = %#v", wire)
	}
}

func TestDeletionFenceDoesNotFallBackToPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	store, err := hubcore.NewDeletionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(sessionID)
	if _, err := store.Begin(project.ID, []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: sessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
		Past:          past,
	}
	server := newHubAppServer(cfg, newHubSourceRegistry(cfg))
	raw, err := json.Marshal(appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}

	_, err = server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodThreadRead,
		Params: raw,
	})
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting past read error = %T %v, want targetDeleted", err, err)
	}
}

func TestDeletionFenceRejectsResumeBeforeSpawner(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	store, err := hubcore.NewDeletionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(sessionID)
	if _, err := store.Begin(project.ID, []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: sessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	resumeCalls := 0
	spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		resumeCalls++
		return rendezvous.Entry{}, errors.New("deleting target reached spawner")
	}}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
		Past:          past,
		Spawner:       spawner,
	}

	_, err = hubThreadResume(context.Background(), cfg, newHubSourceRegistry(cfg), appwire.ThreadResumeParams{Ref: ref})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("deleting resume error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeTargetDeleted {
		t.Fatalf("deleting resume wire error = %#v", wire)
	}
	if resumeCalls != 0 {
		t.Fatalf("deleting resume reached spawner %d times", resumeCalls)
	}
}

func TestDeletionFenceRejectsResumeWithNilSpawner(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(sessionID)
	if _, err := store.Begin("project-resume-nil-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: sessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}

	_, err = hubThreadResume(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadResumeParams{Ref: ref})
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting resume error with nil spawner = %T %v, want targetDeleted", err, err)
	}
}

func TestDeletionFenceRejectsMutationWithTargetDeletedOutcome(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := localAppRef(webTestSessionID)
	if _, err := store.Begin("project-mutate-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: webTestSessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}
	source := &deletionFenceMutationSource{}
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(cfg, sources)
	tests := []struct {
		method string
		id     string
		params any
	}{
		{appwire.MethodTurnStart, "mutation-start-after-delete", appwire.TurnStartParams{
			Ref: ref, ClientMutationID: "mutation-start-after-delete",
			Input: []appwire.InputItem{{Type: "text", Text: "start"}},
		}},
		{appwire.MethodTurnSteer, "mutation-steer-after-delete", appwire.TurnSteerParams{
			Ref: ref, ClientMutationID: "mutation-steer-after-delete", Input: []appwire.InputItem{{Type: "text", Text: "steer"}},
		}},
		{appwire.MethodTurnInterrupt, "mutation-interrupt-after-delete", appwire.TurnInterruptParams{
			Ref: ref, ClientMutationID: "mutation-interrupt-after-delete"}},
		{appwire.MethodTurnQueue, "mutation-queue-after-delete", appwire.TurnQueueParams{
			Ref: ref, ClientMutationID: "mutation-queue-after-delete", Input: []appwire.InputItem{{Type: "text", Text: "queue"}},
		}},
		{appwire.MethodTurnDrainAsSteer, "mutation-drain-after-delete", appwire.TurnDrainAsSteerParams{
			Ref: ref, ClientMutationID: "mutation-drain-after-delete",
			ExpectedQueueRevision: 1,
		}},
		{appwire.MethodTurnPromoteQueuedAsSteer, "mutation-promote-after-delete", appwire.TurnPromoteQueuedAsSteerParams{
			Ref: ref, ClientMutationID: "mutation-promote-after-delete",
			ExpectedEntryID: "queue-before-delete",
		}},
		{appwire.MethodTurnCancelQueued, "mutation-cancel-after-delete", appwire.TurnCancelQueuedParams{
			Ref: ref, ClientMutationID: "mutation-cancel-after-delete",
			ExpectedEntryID: "queue-before-delete",
		}},
	}
	for i, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			raw, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			_, err = server.Router().Dispatch(context.Background(), appwire.Request{
				ID:     appwire.NewIntID(int64(i + 1)),
				Method: tc.method,
				Params: raw,
			})
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("deleting mutation error = %T %v, want WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok ||
				data.ClientMutationID != tc.id ||
				data.MutationOutcome != appwire.MutationOutcomeTargetDeleted ||
				data.RetryDisposition != appwire.RetryDispositionNone {
				t.Fatalf("deleting mutation wire error = %#v", wire)
			}
			if source.calls[tc.method] != 0 {
				t.Fatalf("deleting mutation reached %s source %d times", tc.method, source.calls[tc.method])
			}
		})
	}
}

func TestDeletionFenceRejectsRelayBeforeSubscribe(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	if _, err := store.Begin("project-relay-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: threadID,
	}}); err != nil {
		t.Fatal(err)
	}
	source := &deletionFenceRelaySource{
		thread: appwire.Thread{
			ID:     threadID,
			Evener: appwire.EvenerThread{Ref: ref},
		},
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())

	err = relays.startRelay(context.Background(), source, appwire.ThreadReadParams{Ref: ref}, source.thread)
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting relay error = %T %v, want targetDeleted", err, err)
	}
	if source.subscribeCalls != 0 {
		t.Fatalf("deleting relay subscribed %d times", source.subscribeCalls)
	}
}

func TestDeletionFenceCanCommitDuringInitialRelayIOAndBlocksPublication(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	resumeLocks := hubcore.NewResumeLocks()
	source := &deletionOwnershipProbeRelaySource{
		thread: appwire.Thread{
			ID:     threadID,
			Evener: appwire.EvenerThread{Ref: ref},
		},
		store:       store,
		resumeLocks: resumeLocks,
		projectID:   "project-relay-initial-0123456789",
		ref:         ref,
		threadID:    threadID,
		probeOnCall: 1,
		probed:      make(chan deletionOwnershipProbeResult, 1),
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   resumeLocks,
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())

	relayErr := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{Ref: ref}, source.thread)
	result := <-source.probed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.deletedBeforeSubscribe {
		t.Fatal("relay held deletion ownership across upstream I/O")
	}
	if !isTargetDeletedError(relayErr) {
		t.Fatalf("relay error = %T %v, want targetDeleted after deletion won the I/O race", relayErr, relayErr)
	}
}

func TestDeletionFenceCanCommitDuringRecoveryRelayIOAndStopsPublication(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	resumeLocks := hubcore.NewResumeLocks()
	initialNotifications := make(chan appwire.Notification)
	source := &deletionOwnershipProbeRelaySource{
		thread: appwire.Thread{
			ID:     threadID,
			Evener: appwire.EvenerThread{Ref: ref},
		},
		store:                store,
		resumeLocks:          resumeLocks,
		projectID:            "project-relay-recovery-0123456789",
		ref:                  ref,
		threadID:             threadID,
		probeOnCall:          2,
		initialNotifications: initialNotifications,
		probed:               make(chan deletionOwnershipProbeResult, 1),
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   resumeLocks,
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())
	defer relays.stopRelay("local:" + threadID)

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{Ref: ref}, source.thread); err != nil {
		t.Fatal(err)
	}
	close(initialNotifications)
	result := <-source.probed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.deletedBeforeSubscribe {
		t.Fatal("relay held deletion ownership across recovery I/O")
	}
}

func TestDeletionFenceTurnStartDoesNotWaitForRelayWhileOwningTarget(t *testing.T) {
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	resumeLocks := hubcore.NewResumeLocks()
	placeholderPublished := make(chan struct{})
	releaseInitializer := make(chan struct{})
	source := &inheritedDeletionOwnershipRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: ref},
		},
		notifications: make(chan appwire.Notification),
		subscribed:    make(chan struct{}, 1),
	}
	cfg := hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		ResumeLocks:  resumeLocks,
		RelayHooks: hubcore.RelayLifecycleHooks{
			AfterPlaceholder: func(gotThreadID string) {
				if gotThreadID != threadID {
					return
				}
				close(placeholderPublished)
				<-releaseInitializer
			},
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	var relays hubRelayFunctions
	previousObserveFunctions := observeHubRelayFunctions
	observeHubRelayFunctions = func(got hubRelayFunctions) {
		relays = got
	}
	t.Cleanup(func() {
		observeHubRelayFunctions = previousObserveFunctions
	})
	server := newHubAppServer(cfg, sources)
	observeHubRelayFunctions = previousObserveFunctions
	defer relays.stopRelay("local:" + threadID)

	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() {
		waiterJoined <- struct{}{}
	}
	t.Cleanup(func() {
		observeHubRelayWait = previousObserveWait
	})

	initializerDone := make(chan error, 1)
	go func() {
		initializerDone <- relays.startRelay(
			context.Background(),
			source,
			appwire.ThreadReadParams{Ref: ref},
			source.thread,
		)
	}()
	<-placeholderPublished

	raw, err := json.Marshal(appwire.TurnStartParams{
		ThreadID:           threadID,
		Ref:                ref,
		ClientMutationID:   "turn-start-relay-ownership",
		ExpectedInstanceID: threadID,
		Input:              []appwire.InputItem{{Type: "text", Text: "continue"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turnDone := make(chan error, 1)
	go func() {
		_, err := server.Router().Dispatch(context.Background(), appwire.Request{
			ID:     appwire.NewIntID(1),
			Method: appwire.MethodTurnStart,
			Params: raw,
		})
		turnDone <- err
	}()
	<-waiterJoined

	targetLock := resumeLocks.For(threadID)
	targetWasFree := targetLock.TryLock()
	if targetWasFree {
		targetLock.Unlock()
	} else {
		relays.stopRelay("local:" + threadID)
	}
	close(releaseInitializer)
	initializerErr := <-initializerDone
	turnErr := <-turnDone

	if !targetWasFree {
		t.Fatal("turn/start waited for the relay initializer while owning the deletion target")
	}
	if initializerErr != nil {
		t.Fatalf("initialize relay: %v", initializerErr)
	}
	if turnErr != nil {
		t.Fatalf("turn/start: %v", turnErr)
	}
	select {
	case <-source.subscribed:
	default:
		t.Fatal("relay initializer did not subscribe")
	}
}

func TestDeletionFenceRejectsForkBeforeLaunchingChild(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	if _, err := store.Begin("project-fork-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: threadID,
	}}); err != nil {
		t.Fatal(err)
	}
	oldAside := hubAsideSession
	t.Cleanup(func() { hubAsideSession = oldAside })
	launchCalls := 0
	hubAsideSession = func(string, string) (string, error) {
		launchCalls++
		return "", errors.New("deleting fork launched child")
	}
	cfg := hubcore.WebConfig{
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
		StateDir:      t.TempDir(),
	}

	_, err = hubThreadFork(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadForkParams{
		Ref:   ref,
		Aside: true,
	})
	if !isTargetDeletedError(err) {
		t.Fatalf("deleting fork error = %T %v, want targetDeleted", err, err)
	}
	if launchCalls != 0 {
		t.Fatalf("deleting fork launched %d children", launchCalls)
	}
}

type deletionFenceRelaySource struct {
	relayLifecycleSource
	subscribeCalls int
}

func (s *deletionFenceRelaySource) ID() string {
	return "local"
}

func (s *deletionFenceRelaySource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.subscribeCalls++
	return nil, errors.New("deleting relay reached source subscription")
}

type inheritedDeletionOwnershipRelaySource struct {
	relayLifecycleSource
	notifications <-chan appwire.Notification
	subscribed    chan struct{}
}

func (s *inheritedDeletionOwnershipRelaySource) ID() string {
	return "local"
}

func (s *inheritedDeletionOwnershipRelaySource) SubscribeThread(
	context.Context,
	appwire.ThreadReadParams,
) (<-chan appwire.Notification, error) {
	s.subscribed <- struct{}{}
	return s.notifications, nil
}

func (s *inheritedDeletionOwnershipRelaySource) StartTurn(
	context.Context,
	appwire.TurnStartParams,
) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_started"}}, nil
}

type deletionOwnershipProbeResult struct {
	deletedBeforeSubscribe bool
	err                    error
}

type deletionOwnershipProbeRelaySource struct {
	relayLifecycleSource
	store                *hubcore.DeletionStore
	resumeLocks          *hubcore.ResumeLocks
	projectID            string
	ref                  string
	threadID             string
	probeOnCall          int
	initialNotifications <-chan appwire.Notification
	probed               chan deletionOwnershipProbeResult
	mu                   sync.Mutex
	subscribeCalls       int
}

func (s *deletionOwnershipProbeRelaySource) ID() string {
	return "local"
}

func (s *deletionOwnershipProbeRelaySource) SubscribeThread(
	context.Context,
	appwire.ThreadReadParams,
) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.subscribeCalls++
	call := s.subscribeCalls
	s.mu.Unlock()
	if call < s.probeOnCall {
		return s.initialNotifications, nil
	}

	lock := s.resumeLocks.For(s.threadID)
	if !lock.TryLock() {
		s.probed <- deletionOwnershipProbeResult{}
		return nil, errors.New("stop relay ownership probe")
	}
	_, err := s.store.Begin(s.projectID, []hubcore.DeletionTarget{{
		Ref:      s.ref,
		ThreadID: s.threadID,
	}})
	lock.Unlock()
	if err != nil {
		s.probed <- deletionOwnershipProbeResult{err: err}
		return nil, err
	}
	_, deleted := s.store.TargetState(s.ref, s.threadID)
	s.probed <- deletionOwnershipProbeResult{deletedBeforeSubscribe: deleted}
	return nil, errors.New("stop relay ownership probe")
}

type deletionFenceMutationSource struct {
	relayLifecycleSource
	calls map[string]int
}

func (s *deletionFenceMutationSource) ID() string {
	return "local"
}

func (s *deletionFenceMutationSource) QueueTurn(
	context.Context,
	appwire.TurnQueueParams,
) (appwire.TurnQueueResponse, error) {
	s.recordCall(appwire.MethodTurnQueue)
	return appwire.TurnQueueResponse{}, nil
}

func (s *deletionFenceMutationSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.recordCall(appwire.MethodTurnStart)
	return appwire.TurnStartResponse{}, nil
}

func (s *deletionFenceMutationSource) SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	s.recordCall(appwire.MethodTurnSteer)
	return appwire.TurnSteerResponse{}, nil
}

func (s *deletionFenceMutationSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	s.recordCall(appwire.MethodTurnInterrupt)
	return appwire.TurnInterruptResponse{}, nil
}

func (s *deletionFenceMutationSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	s.recordCall(appwire.MethodTurnDrainAsSteer)
	return appwire.TurnDrainAsSteerResponse{}, nil
}

func (s *deletionFenceMutationSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	s.recordCall(appwire.MethodTurnPromoteQueuedAsSteer)
	return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
}

func (s *deletionFenceMutationSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	s.recordCall(appwire.MethodTurnCancelQueued)
	return appwire.TurnCancelQueuedResponse{}, nil
}

func (s *deletionFenceMutationSource) recordCall(method string) {
	if s.calls == nil {
		s.calls = make(map[string]int)
	}
	s.calls[method]++
}

func TestHubRPCUpgradeRunsSelfUpdater(t *testing.T) {
	var got selfupdate.Options
	previous := runHubSelfUpgrade
	runHubSelfUpgrade = func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		got = opts
		return selfupdate.Result{
			Release:        "snapshot",
			Channel:        "snapshot",
			Archive:        "evener_linux_amd64.tar.gz",
			ShareBinDir:    "/tmp/share/evener/bin",
			BinDir:         "/tmp/bin",
			RestartMessage: "Restart evener-tui and evener-hub to use the upgraded binaries.",
		}, nil
	}
	t.Cleanup(func() { runHubSelfUpgrade = previous })

	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	params, err := json.Marshal(appwire.UpgradeParams{Requested: "snapshot"})
	if err != nil {
		t.Fatalf("MarshalParams: %v", err)
	}
	raw, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerUpgrade,
		Params: params,
	})
	if err != nil {
		t.Fatalf("Dispatch upgrade: %v", err)
	}
	resp, ok := raw.(appwire.UpgradeResponse)
	if !ok {
		t.Fatalf("response type=%T", raw)
	}
	if resp.Channel != "snapshot" || resp.Archive != "evener_linux_amd64.tar.gz" {
		t.Fatalf("response=%+v", resp)
	}
	if got.Requested != "snapshot" {
		t.Fatalf("Requested=%q, want snapshot", got.Requested)
	}
	if got.CurrentChannel == "" {
		t.Fatal("CurrentChannel is empty")
	}
}

func TestAppItemsFromReplayTurnConvertsCommunicateToAgentMessage(t *testing.T) {
	toolNames := map[string]string{}
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "ASSISTANT",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "tool_call",
			ToolCall: &llm.ToolCallData{
				ID:        "call_1",
				Name:      "communicate",
				Arguments: []byte(`{"message":"done","end_turn":true}`),
			},
		}}},
	}, toolNames)

	if len(items) != 1 || items[0].Type != "agentMessage" || items[0].Text != "done" {
		t.Fatalf("communicate items=%+v", items)
	}

	results := appItemsFromReplayTurn("turn_2", 2, schema.Turn{
		Kind: "TOOL_RESULTS",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind:       "tool_result",
			ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Content: `{"accepted":true}`},
		}}},
	}, toolNames)
	if len(results) != 0 {
		t.Fatalf("communicate tool results should be hidden, got %+v", results)
	}
}

func TestAppItemsFromReplayTurnCarriesToolStateRaw(t *testing.T) {
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "TOOL_RESULTS",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "tool_result",
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_delegate_send",
				Name:       "delegate_send",
				Content:    "started delegate turn",
				ToolState:  []byte(`{"job_id":"job_1","status":"running"}`),
			},
		}}},
	}, map[string]string{})

	if len(items) != 1 || items[0].ToolName != "delegate_send" || items[0].Output != "started delegate turn" {
		t.Fatalf("tool result items=%+v", items)
	}
	if string(items[0].Raw) != `{"job_id":"job_1","status":"running"}` {
		t.Fatalf("tool result Raw = %s, want replay tool_state", items[0].Raw)
	}
}

func TestAppItemsFromReplayTurnProjectsThinking(t *testing.T) {
	var entry transcript.Entry
	raw := []byte(`{"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[` +
		`{"kind":"thinking","thinking":{"text":"Let me plan this out."}},` +
		`{"kind":"text","text":"The answer is 42."}` +
		`]}}}`)
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, map[string]string{})

	if len(items) != 2 {
		t.Fatalf("expected reasoning + agentMessage, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "Let me plan this out." {
		t.Fatalf("reasoning item=%+v", items[0])
	}
	if items[1].Type != "agentMessage" || items[1].Text != "The answer is 42." {
		t.Fatalf("agent message item=%+v", items[1])
	}
}

func TestAppItemsFromReplayTurnProjectsRedactedThinking(t *testing.T) {
	var entry transcript.Entry
	raw := []byte(`{"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[` +
		`{"kind":"redacted_thinking","thinking":{"redacted":true,"encrypted_content":"xyz"}},` +
		`{"kind":"text","text":"ok"}` +
		`]}}}`)
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, map[string]string{})

	if len(items) != 2 {
		t.Fatalf("expected reasoning + agentMessage, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "[redacted thinking]" {
		t.Fatalf("redacted reasoning item=%+v", items[0])
	}
}

func TestAppItemsFromReplayTurnProjectsWebSearch(t *testing.T) {
	var entry transcript.Entry
	raw := []byte(`{"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[` +
		`{"kind":"web_search","web_search":{"query":"go context","raw":{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://go.dev/ctx","title":"Context"}]}}}` +
		`]}}}`)
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, map[string]string{})
	if len(items) != 1 || items[0].Type != "commandExecution" || items[0].ToolName != "web_search" {
		t.Fatalf("web_search items=%+v", items)
	}
	if !strings.Contains(items[0].ArgumentsJSON, "go context") {
		t.Fatalf("args missing query: %s", items[0].ArgumentsJSON)
	}
	if !strings.Contains(items[0].Output, "Context") || !strings.Contains(items[0].Output, "https://go.dev/ctx") {
		t.Fatalf("output missing results: %q", items[0].Output)
	}
}

// A transcript file can hold any content kind the schema can decode. Replay
// still hands the client only what it can render: audio and document parts
// reach Images no more than they reach the live EventUserInput payload, which
// has no field for them at all.
func TestAppItemsFromReplayTurnKeepsNonImagePartsOutOfImages(t *testing.T) {
	var entry transcript.Entry
	raw := []byte(`{"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[` +
		`{"kind":"text","text":"summarize"},` +
		`{"kind":"document","document":{"file_name":"report.pdf","media_type":"application/pdf"}},` +
		`{"kind":"audio","audio":{"media_type":"audio/wav"}},` +
		`{"kind":"image","image":{"media_type":"image/png","data":"cG5n"}}` +
		`]}}}`)
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, map[string]string{})
	if len(items) != 1 || items[0].Type != "userMessage" {
		t.Fatalf("expected userMessage, got %+v", items)
	}
	images := items[0].Images
	if len(images) != 1 {
		t.Fatalf("expected the picture alone, got %+v", images)
	}
	if images[0].Type != "input_image" || images[0].MediaType != "image/png" {
		t.Fatalf("image=%+v", images[0])
	}
}

func TestAppItemsFromReplayTurnDoesNotAcceptLegacyToolCallKind(t *testing.T) {
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "ASSISTANT",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "commandExecution",
			ToolCall: &llm.ToolCallData{
				ID:        "call_legacy",
				Name:      "read_file",
				Arguments: []byte(`{"file_path":"/tmp/example.txt"}`),
			},
		}}},
	}, map[string]string{})

	if len(items) != 0 {
		t.Fatalf("legacy commandExecution transcript part should be ignored, got %+v", items)
	}
}

func TestAppItemsFromReplayTurnAcceptsCurrentToolCallKind(t *testing.T) {
	toolNames := map[string]string{}
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "ASSISTANT",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "tool_call",
			ToolCall: &llm.ToolCallData{
				ID:        "call_read",
				Name:      "read_file",
				Arguments: []byte(`{"file_path":"/tmp/example.txt","intent":"Inspect example output."}`),
			},
		}}},
	}, toolNames)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want one tool item", items)
	}
	if got := items[0]; got.Type != "commandExecution" || got.CallID != "call_read" || !strings.Contains(got.ArgumentsJSON, "/tmp/example.txt") {
		t.Fatalf("tool item=%+v", got)
	}
	if items[0].Description != "Inspect example output." {
		t.Fatalf("tool description=%q", items[0].Description)
	}
}

func TestAppItemsFromReplayTurnSteeringCarriesImageMetadata(t *testing.T) {
	img := []byte("png")
	items := appItemsFromReplayTurn("turn_3", 3, schema.Turn{
		Kind: "STEERING",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "image",
			Image: &llm.ImageData{
				Data:      img,
				MediaType: "image/png",
			},
		}}},
	}, map[string]string{})

	if len(items) != 1 {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	got := items[0]
	if got.Type != "steering" || got.Text != "[image]" || len(got.Images) != 1 {
		t.Fatalf("steering item=%+v, want image placeholder and image metadata", got)
	}
	if got.Images[0].Metadata["sha"] != imageSha(img) || got.Images[0].Metadata["size"] != strconv.Itoa(len(img)) {
		t.Fatalf("image metadata=%+v, want sha/size", got.Images[0].Metadata)
	}
}

// A steer the human typed is a person speaking, and reload must say so.
// Decoded from the real on-disk JSON shape (agent/schema.Turn's
// steering_source tag) so the wire tag is under test, not just the field:
// the web UI's SteeringItem branches on source === "user" to render the steer
// exactly like a prompt (issue #24), and an empty Source is stripped by
// omitempty, leaving the client no source at all rather than a wrong one.
func TestAppItemsFromReplayTurnSteeringCarriesUserSource(t *testing.T) {
	var entry transcript.Entry
	raw := []byte(`{"turn":{"kind":"STEERING","steering_source":"user","message":{"role":"user","content":[` +
		`{"kind":"text","text":"new worktree"}` +
		`]}}}`)
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	items := appItemsFromReplayTurn("turn_3", 3, entry.Turn, map[string]string{})

	if len(items) != 1 {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	if items[0].Type != "steering" || items[0].Source != events.SteeringSourceUser {
		t.Fatalf("steering item=%+v, want type steering with source %q", items[0], events.SteeringSourceUser)
	}
}

// Daemon nudges carry no steering_source on disk, and must stay anonymous:
// they render as the quiet collapsible divider, never as user speech.
func TestAppItemsFromReplayTurnSteeringWithoutSourceStaysAnonymous(t *testing.T) {
	var entry transcript.Entry
	raw := []byte(`{"turn":{"kind":"STEERING","message":{"role":"user","content":[` +
		`{"kind":"text","text":"<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>"}` +
		`]}}}`)
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	items := appItemsFromReplayTurn("turn_4", 4, entry.Turn, map[string]string{})

	if len(items) != 1 {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	if items[0].Source != "" {
		t.Fatalf("steering item source=%q, want empty for a daemon nudge", items[0].Source)
	}
}

func TestAppItemsFromReplayTurnIncludesCompactionTurns(t *testing.T) {
	checkpoint := appItemsFromReplayTurn("turn_4", 4, schema.Turn{
		Kind:    "CHECKPOINT",
		Message: llm.Message{Content: []llm.ContentPart{{Kind: "text", Text: "[CONTEXT CHECKPOINT]\nfirst compacted state"}}},
	}, map[string]string{})
	if len(checkpoint) != 1 {
		t.Fatalf("checkpoint items=%+v", checkpoint)
	}
	if got := checkpoint[0]; got.Type != "systemMessage" || got.Description != "Context checkpoint" || !strings.Contains(got.Text, "first compacted state") {
		t.Fatalf("checkpoint item=%+v", got)
	}

	summary := appItemsFromReplayTurn("turn_5", 5, schema.Turn{
		Kind:    "SUMMARY",
		Message: llm.Message{Content: []llm.ContentPart{{Kind: "text", Text: "[CONTEXT SUMMARY]\nsecond compacted state"}}},
	}, map[string]string{})
	if len(summary) != 1 {
		t.Fatalf("summary items=%+v", summary)
	}
	if got := summary[0]; got.Type != "systemMessage" || got.Description != "Context summary" || !strings.Contains(got.Text, "second compacted state") {
		t.Fatalf("summary item=%+v", got)
	}
}

func TestHubRPCThreadListUsesRosterStatusAndSessionID(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv2enqVTitaig6F",
		SessionID: "02wMz5Txv2enqVTitaig6F",
	})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: "02wMz5Txv1C3Hut0M8GCeB", status: appwire.ThreadStatusAwaiting})
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	thread := resp.Data[0]
	if thread.ID != "02wMz5Txv1C3Hut0M8GCeB" || thread.SessionID != "02wMz5Txv1C3Hut0M8GCeB" || thread.Evener.Ref != "local:02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("thread identity=%+v", thread)
	}
	if thread.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("status=%q, want %q", thread.Status.Type, appwire.ThreadStatusAwaiting)
	}
}

func TestHubRPCThreadListIncludesPastThreads(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{SearchTerm: "second task"})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != sessionID || resp.Data[0].Status.Type != appwire.ThreadStatusNotLoaded {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubThreadListProjectsRunningSubagentActive(t *testing.T) {
	cfg, childID := runningSubagentProjectionConfig(t)
	resp, err := hubThreadList(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	for _, thread := range resp.Data {
		if thread.ID == childID {
			if thread.Status.Type != appwire.ThreadStatusActive {
				t.Fatalf("running subagent status = %q, want %q", thread.Status.Type, appwire.ThreadStatusActive)
			}
			return
		}
	}
	t.Fatalf("running subagent %s missing from thread list: %+v", childID, resp.Data)
}

func TestHubThreadListProjectsIdleSubagentIdle(t *testing.T) {
	cfg, childID := runningSubagentProjectionConfigWithState(t, appwire.ThreadStatusIdle)
	resp, err := hubThreadList(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	for _, thread := range resp.Data {
		if thread.ID == childID {
			if thread.Status.Type != appwire.ThreadStatusIdle {
				t.Fatalf("idle subagent status = %q, want %q", thread.Status.Type, appwire.ThreadStatusIdle)
			}
			return
		}
	}
	t.Fatalf("idle subagent %s missing from thread list: %+v", childID, resp.Data)
}

func TestHubRPCThreadListOrdersLiveThreadsDeterministically(t *testing.T) {
	runDir := t.TempDir()
	base := time.Now().UTC()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv2enqVTitaig6F",
		SessionID: "02wMz5Txv2enqVTitaig6F",
		StartedAt: base.Add(-time.Hour),
	})
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       102,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:2/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv47YP64RR3B9YJ",
		SessionID: "02wMz5Txv47YP64RR3B9YJ",
		StartedAt: base,
	})

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].ID != "02wMz5Txv47YP64RR3B9YJ" || resp.Data[1].ID != "02wMz5Txv2enqVTitaig6F" {
		t.Fatalf("order=%s,%s", resp.Data[0].ID, resp.Data[1].ID)
	}
}

func TestHubThreadListIncludesEveryRegisteredSource(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	entries := []rendezvous.Entry{
		{
			PID:       101,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:1/rpc",
			SourceID:  "local",
			ThreadID:  "01EVENER",
			SessionID: "01EVENER",
			StartedAt: base.Add(-time.Minute),
		},
		{
			PID:       102,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:2/rpc",
			SourceID:  "codex",
			ThreadID:  "02CODEX",
			SessionID: "02CODEX",
			StartedAt: base,
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry { return entries }, nil))
	sources.Add(appsource.NewLocalDaemonSource("codex", func() []rendezvous.Entry { return entries }, nil))

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].Evener.Ref != "codex:02CODEX" || resp.Data[1].Evener.Ref != "local:01EVENER" {
		t.Fatalf("refs=%s,%s", resp.Data[0].Evener.Ref, resp.Data[1].Evener.Ref)
	}
}

func TestHubThreadListContinuesWhenOptionalSourceFails(t *testing.T) {
	localThread := appwire.Thread{
		ID:        "01LOCAL",
		SessionID: "01LOCAL",
		Source:    "local",
		Preview:   "local thread",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener:    appwire.EvenerThread{Ref: "local:01LOCAL"},
	}
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "local", thread: localThread})
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "local:01LOCAL" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubThreadListReturnsErrorWhenOnlySelectedSourceFails(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	_, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{SourceIDs: []string{"codex"}})
	if err == nil || !strings.Contains(err.Error(), "codex offline") {
		t.Fatalf("hubThreadList error=%v, want codex offline", err)
	}
}

func TestHubThreadListReturnsErrorWhenAnySelectedSourceFails(t *testing.T) {
	localThread := appwire.Thread{
		ID:        "01LOCAL",
		SessionID: "01LOCAL",
		Source:    "local",
		Preview:   "local thread",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener:    appwire.EvenerThread{Ref: "local:01LOCAL"},
	}
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "local", thread: localThread})
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	_, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{SourceIDs: []string{"local", "codex"}})
	if err == nil || !strings.Contains(err.Error(), "codex offline") {
		t.Fatalf("hubThreadList error=%v, want codex offline", err)
	}
}

func TestHubThreadListOrdersPastSearchByUpdatedCreatedTitleAndID(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	for _, meta := range []schema.SessionMeta{
		{ID: "02wMz5Txv5aIxgf9yVdd0N", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated, OriginalPrompt: "beta task"},
		{ID: "02wMz5Txv1C3Hut0M8GCeB", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, OriginalPrompt: "alpha task"},
		{ID: "02wMz5Txv8Vo4rqb3QYZuV", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "bravo task"},
		{ID: "02wMz5Txv733WHFsVy66SR", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "alpha task"},
	} {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: past}, sources, appwire.ThreadListParams{SearchTerm: "task"})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	got := make([]string, 0, len(resp.Data))
	for _, thread := range resp.Data {
		got = append(got, thread.ID)
	}
	want := []string{"02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv5aIxgf9yVdd0N", "02wMz5Txv733WHFsVy66SR", "02wMz5Txv8Vo4rqb3QYZuV"}
	if len(got) != len(want) {
		t.Fatalf("order=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v, want %v", got, want)
		}
	}
}

func TestHubThreadListSearchMatchesProviderOnlyProfile(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex-local", thread: appwire.Thread{
		ID:        "th_codex",
		SessionID: "th_codex",
		Source:    "codex-local",
		Preview:   "codex replay",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
		Evener: appwire.EvenerThread{
			Ref:     "codex-local:th_codex",
			Profile: "openai",
		},
	}})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{SearchTerm: "openai"})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "codex-local:th_codex" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubThreadListOrdersLiveThreadsUsingPastTimestamps(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	liveUpdated := base
	pastUpdated := base.Add(-time.Hour)
	liveStarted := base.Add(-24 * time.Hour)

	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             "02wMz5Txv9yYdSRJat13MZ",
		CreatedAt:      base.Add(-2 * time.Hour),
		UpdatedAt:      liveUpdated,
		OriginalPrompt: "live task",
	}); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             "02wMz5TxvBRJC3228LTWod",
		CreatedAt:      base.Add(-3 * time.Hour),
		UpdatedAt:      pastUpdated,
		OriginalPrompt: "past task",
	}); err != nil {
		t.Fatal(err)
	}
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       501,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:501/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv9yYdSRJat13MZ",
		SessionID: "02wMz5Txv9yYdSRJat13MZ",
		StartedAt: liveStarted,
	})
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	sources := newHubSourceRegistry(hubcore.WebConfig{RunDir: runDir})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: past}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].ID != "02wMz5Txv9yYdSRJat13MZ" || resp.Data[1].ID != "02wMz5TxvBRJC3228LTWod" {
		t.Fatalf("order=%s,%s", resp.Data[0].ID, resp.Data[1].ID)
	}
	if resp.Data[0].UpdatedAt != liveUpdated.Unix() || resp.Data[0].CreatedAt != base.Add(-2*time.Hour).Unix() {
		t.Fatalf("live timestamps=%+v", resp.Data[0])
	}
}

// TestMergePastMetadataForList_PropagatesContextCancellation covers the
// contract that mergePastMetadataForList must propagate ctx cancellation
// and deadline errors from pastEntryThread rather than silently falling
// back to the unenriched live thread -- a canceled thread-list request must
// stop, not keep sweeping later threads. The delegate journal fixture
// (seedPastSessionWithActivity) matters here: without it, pastEntryThread's
// own ctx-aware step (pastEntryDelegateStatus -> agent.LoadSessionDelegateStatus,
// gated on delegates.jsonl existing at all) is never reached, so a canceled
// ctx would go unnoticed for a different, uninteresting reason.
func TestMergePastMetadataForList_PropagatesContextCancellation(t *testing.T) {
	cfg, rootID, _, _ := seedPastSessionWithActivity(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live := appwire.Thread{ID: rootID}

	_, err := mergePastMetadataForList(ctx, cfg, "local", live)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled -- a canceled request must not silently fall back to the live thread unenriched", err)
	}
}

func TestHubRPCThreadReadRoutesToDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Evener: appwire.EvenerThread{Ref: "local:th_1"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       102,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.ID != "th_1" || resp.Thread.Evener.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadReadRoutesReachableErroredDaemon(t *testing.T) {
	const sessionID = "02wMz5Txv9yYdSRJat13MZ"
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID:       103,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  sessionID,
			SessionID: sessionID,
		},
		SessionID: sessionID,
		Status:    "errored",
	})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("thread/read: %v", err)
	}
	if response.Thread.ID != sessionID {
		t.Fatalf("thread = %+v, want reachable errored daemon", response.Thread)
	}
}

func TestHubRPCThreadReadReturnsPastTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	// The user+assistant exchange is one logical turn; the trailing user
	// input is its own open group.
	if resp.Thread.ID != sessionID || len(resp.Thread.Turns) != 2 {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "userMessage" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
	if got := resp.Thread.Turns[0].Items[1]; got.Type != "agentMessage" || got.Text != "first reply" {
		t.Fatalf("second item=%+v", got)
	}
	if got := resp.Thread.Turns[1].Items[0]; got.Type != "userMessage" || got.Text != "second task" {
		t.Fatalf("third item=%+v", got)
	}
}

func TestHubRPCSubscribedReadReturnsPastForCrashMarker(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-crashed-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID:       104,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:1/rpc",
			SourceID:  "local",
			ThreadID:  sessionID,
			SessionID: sessionID,
		},
		SessionID: sessionID,
		Status:    "errored",
		Crashed:   true,
	})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "local:" + sessionID,
		IncludeTurns: true,
		ItemsView:    "full",
		Subscribe:    true,
		ItemLimit:    40,
	})
	if err != nil {
		t.Fatalf("subscribed thread/read: %v", err)
	}
	// The user+assistant exchange is one logical turn; the trailing user
	// input is its own open group.
	if response.Thread.ID != sessionID || len(response.Thread.Turns) != 2 {
		t.Fatalf("saved thread = %+v", response.Thread)
	}
}

func TestHubRPCSubscribedAtomicFailuresDoNotFallBackToPastAndCanRetry(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-atomic-retry-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:" + sessionID},
	}
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
	}
	var lease *scriptedRelaySessionLease
	lease = &scriptedRelaySessionLease{
		readErr:    appwire.SessionUnavailable("canonical actor read failed"),
		deliveries: make(chan appsource.RelayDelivery),
		readHook: func() {
			lease.mu.Lock()
			lease.readErr = nil
			lease.readResult = appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
			}
			lease.readHook = func() {
				lease.mu.Lock()
				lease.readHook = nil
				lease.readResult.Handoff = handoff
				lease.mu.Unlock()
			}
			lease.mu.Unlock()
		},
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	params := appwire.ThreadReadParams{
		Ref:          thread.Evener.Ref,
		IncludeTurns: true,
		ItemsView:    "full",
		Subscribe:    true,
	}
	if response, err := client.ThreadRead(context.Background(), params); err == nil {
		t.Errorf("first subscribed thread/read returned saved transcript %+v after its canonical actor failed", response.Thread)
	}
	if response, err := client.ThreadRead(context.Background(), params); err == nil {
		t.Errorf("second subscribed thread/read returned saved transcript %+v without a live handoff", response.Thread)
	}
	response, err := client.ThreadRead(context.Background(), params)
	if err != nil {
		t.Fatalf("retry subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID {
		t.Fatalf("retry thread ID = %q, want %q", response.Thread.ID, sessionID)
	}
	// The hub commits the handoff after the response enters the connection's
	// send queue, so the client can observe the response before Commit runs.
	// Wait for the commit signal instead of assuming that ordering.
	select {
	case <-handoff.committed:
	case <-time.After(time.Second):
		t.Fatal("successful retry did not commit its live handoff")
	}
	if got := lease.readCallCount(); got != 3 {
		t.Fatalf("canonical actor read calls = %d, want read failure, missing handoff, and successful retry", got)
	}
}

func TestHubRPCNonSubscribedAtomicReadFailureCanReturnPastTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-nonsubscribed-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:" + sessionID},
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease: &scriptedRelaySessionLease{
			readErr:    appwire.SessionUnavailable("canonical actor read failed"),
			deliveries: make(chan appsource.RelayDelivery),
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          thread.Evener.Ref,
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err != nil {
		t.Fatalf("non-subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID || len(response.Thread.Turns) != 2 {
		t.Fatalf("non-subscribed saved thread = %+v", response.Thread)
	}
}

func TestHubRPCSubscribedNonAtomicReadFailureCanReturnPastTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-non-atomic-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	thread := appwire.Thread{
		ID:        sessionID,
		SessionID: sessionID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:" + sessionID},
	}
	sources := appsource.NewRegistry()
	sources.Add(&pastFallbackRelaySource{
		thread:  thread,
		readErr: appwire.SessionUnavailable("non-atomic live read failed"),
	})
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          thread.Evener.Ref,
		IncludeTurns: true,
		ItemsView:    "full",
		Subscribe:    true,
	})
	if err != nil {
		t.Fatalf("non-atomic subscribed thread/read: %v", err)
	}
	if response.Thread.ID != sessionID || len(response.Thread.Turns) != 2 {
		t.Fatalf("non-atomic saved thread = %+v", response.Thread)
	}
}

func TestHubRPCThreadReadEnrichesReplayToolOutputImagesFromFiles(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-images-0000000000")
	cwd := filepath.Join(root, "work")
	sessionID := "02wMz5TxvCu3kdckfnw0Gh"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: cwd},
		CreatedAt: now,
		UpdatedAt: now,
		TurnCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID:  sessionID,
		CreatedAt:  now,
		ProfileID:  "openai",
		Model:      "gpt-5",
		WorkingDir: cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_plot",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"python plot.py"}`),
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	toolPNG := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 't', 'o', 'o', 'l'}
	if err := writer.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_plot", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_plot",
				Name:           "shell",
				Content:        "created plot.png",
				ImageData:      toolPNG,
				ImageMediaType: "image/png",
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	// The assistant tool call and its result are one logical turn with one
	// merged command-execution item.
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", resp.Thread.Turns)
	}
	item := resp.Thread.Turns[0].Items[0]
	if len(item.OutputImages) != 2 {
		t.Fatalf("OutputImages=%+v, want tool-result then file-backed descriptors", item.OutputImages)
	}
	if item.OutputImages[0].Source != "tool-result" || item.OutputImages[0].URL == "" {
		t.Fatalf("first output image=%+v, want existing tool-result descriptor first", item.OutputImages[0])
	}
	if item.OutputImages[1].Source != "shell-path" || item.OutputImages[1].Path != "plot.png" || item.OutputImages[1].URL != "/doc/image?session="+sessionID+"&path=plot.png" {
		t.Fatalf("second output image=%+v, want shell-path plot.png descriptor", item.OutputImages[1])
	}
}

func TestHubRPCThreadReadEnrichesLiveToolOutputImagesFromFiles(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID := "02wMz5Txv9yYdSRJat13MZ"
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			CWD:       cwd,
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: params.Ref},
			Turns: []appwire.Turn{{
				ID: "turn_1",
				Items: []appwire.ThreadItem{{
					Type:          "commandExecution",
					ID:            "item_shell",
					TurnID:        "turn_1",
					Position:      &appwire.ThreadItemPosition{Entry: 0, Item: 0},
					TranscriptKey: "live-item:item_shell",
					ToolName:      "shell",
					CallID:        "call_shell",
					ArgumentsJSON: `{}`,
					Output:        "created plot.png",
					Status:        appwire.TurnStatusCompleted,
				}},
				Status: appwire.TurnStatusCompleted,
			}},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       17 * 1000,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", resp.Thread.Turns)
	}
	imgs := resp.Thread.Turns[0].Items[0].OutputImages
	if len(imgs) != 1 || imgs[0].Source != "shell-path" || imgs[0].Path != "plot.png" || imgs[0].URL != "/doc/image?session="+sessionID+"&path=plot.png" {
		t.Fatalf("OutputImages=%+v, want live shell-path plot.png descriptor", imgs)
	}
}

// TestHubRPCThreadReadStampsTheSHARouteOnLiveDaemonTurns is the reload-into-a-
// running-session half of the sha-addressed path. The daemon's own snapshot
// carries tool-result descriptors that name their bytes by sha and no route;
// the hub is what turns them into something the browser can fetch.
func TestHubRPCThreadReadStampsTheSHARouteOnLiveDaemonTurns(t *testing.T) {
	sessionID := "02wMz5Txv9yYdSRJat13MZ"
	sha := strings.Repeat("f", 64)
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: params.Ref},
			Turns: []appwire.Turn{{
				ID: "turn_1",
				Items: []appwire.ThreadItem{{
					Type: "commandExecution", ID: "item_shot", TurnID: "turn_1",
					Position: &appwire.ThreadItemPosition{Entry: 0, Item: 0}, TranscriptKey: "live-item:item_shot",
					ToolName: "screenshot", CallID: "call_shot", ArgumentsJSON: `{}`,
					Status:       appwire.TurnStatusCompleted,
					OutputImages: []appwire.OutputImage{{Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 12, SHA: sha}},
				}},
				Status: appwire.TurnStatusCompleted,
			}},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       19 * 1000,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", resp.Thread.Turns)
	}
	imgs := resp.Thread.Turns[0].Items[0].OutputImages
	if len(imgs) != 1 || imgs[0].URL != "/s/"+sessionID+"/images/"+sha {
		t.Fatalf("OutputImages=%+v, want the sha route stamped onto the daemon's descriptor", imgs)
	}
}

func TestHubRPCThreadReadMergesPastTurnsForLiveDaemon(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       103,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.Status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("status=%q", resp.Thread.Status.Type)
	}
	if len(resp.Thread.Turns) != 2 {
		t.Fatalf("turns=%d thread=%+v", len(resp.Thread.Turns), resp.Thread)
	}
	if got := resp.Thread.Turns[0].Items[0]; got.Type != "userMessage" || got.Text != "first task" {
		t.Fatalf("first item=%+v", got)
	}
}

func TestHubRPCThreadReadDoesNotReturnLocalPastForNonLocalMissingSource(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-local-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + sessionID, IncludeTurns: true})
	if err == nil {
		t.Fatalf("ThreadRead returned local past for codex ref: %+v", resp.Thread)
	}
}

func TestHubRPCThreadReadDoesNotMergeLocalPastIntoNonLocalLiveThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-local-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "codex",
			Preview:   "live codex thread",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: "codex:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: past})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + sessionID, IncludeTurns: true})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 0 {
		t.Fatalf("non-local live thread received local past turns: %+v", resp.Thread.Turns)
	}
	if resp.Thread.Preview != "live codex thread" || resp.Thread.Evener.Ref != "codex:"+sessionID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadReadRelaysDaemonNotifications(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, "th_1")
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Evener: appwire.EvenerThread{Ref: "local:th_1"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       103,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	daemon.Broadcast("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_1",
		Ref:      "local:th_1",
		TurnID:   "turn_1",
		ItemID:   "item_1",
		Delta:    "hi",
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed notification")
	}
}

func TestHubRPCThreadReadRelaysEnrichedOutputImageNotification(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID := "01RELAYIMG"
	source := &relayBroadcastSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "th_img",
			SessionID: sessionID,
			CWD:       cwd,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: "local:th_img", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_img"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- *appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"turnId": "turn_1",
		"item": appwire.ThreadItem{
			Type:          "commandExecution",
			ID:            "item_write",
			ToolName:      "write_file",
			CallID:        "call_write",
			ArgumentsJSON: `{"file_path":"plot.png"}`,
			Status:        appwire.TurnStatusInProgress,
		},
	}).Notification
	source.notifications <- *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
		"turnId": "turn_1",
		"item": appwire.ThreadItem{
			Type:     "commandExecution",
			ID:       "item_write",
			ToolName: "write_file",
			CallID:   "call_write",
			Output:   "wrote",
			Status:   appwire.TurnStatusCompleted,
		},
	}).Notification

	var completed appwire.Notification
	for i := 0; i < 2; i++ {
		select {
		case got := <-client.Notifications():
			if got.Method == appwire.NotifyItemCompleted {
				completed = got
				i = 2
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for relayed completed notification")
		}
	}
	if completed.Method == "" {
		t.Fatal("completed notification was not relayed")
	}
	var params struct {
		Item appwire.ThreadItem `json:"item"`
	}
	if err := json.Unmarshal(completed.Params, &params); err != nil {
		t.Fatalf("unmarshal completed params: %v", err)
	}
	imgs := params.Item.OutputImages
	if len(imgs) != 1 || imgs[0].Source != "written-file" || imgs[0].Path != "plot.png" || imgs[0].URL != "/doc/image?session="+sessionID+"&path=plot.png" {
		t.Fatalf("OutputImages=%+v, want written-file plot.png /doc/image descriptor", imgs)
	}
}

// TestHubRPCRelaysSHARoutedToolResultImageFromARealDaemon is the same check as
// TestHubRPCThreadReadRelaysSHARoutedToolResultImage on the fanout a REAL local
// daemon takes: LocalDaemonSource is a RelaySessionSource, so its frames reach
// the browser through the acknowledged-fanout loop rather than the
// non-atomic broadcast loop, and the two are separate call sites.
func TestHubRPCRelaysSHARoutedToolResultImageFromARealDaemon(t *testing.T) {
	sessionID := "02wMz5Txv733WHFsVy66SR"
	sha := strings.Repeat("b", 64)
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Evener: appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       21 * 1000,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	daemon.Broadcast(sessionID, appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		ThreadID: sessionID, Ref: "local:" + sessionID, TurnID: "turn_1",
		Item: appwire.ThreadItem{
			Type: "commandExecution", ID: "item_shot", TurnID: "turn_1",
			ToolName: "screenshot", CallID: "call_shot", Status: appwire.TurnStatusCompleted,
			OutputImages: []appwire.OutputImage{{Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 11, SHA: sha}},
		},
	})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-client.Notifications():
			if got.Method != appwire.NotifyItemCompleted {
				continue
			}
			var params struct {
				Item appwire.ThreadItem `json:"item"`
			}
			if err := json.Unmarshal(got.Params, &params); err != nil {
				t.Fatalf("unmarshal completed params: %v", err)
			}
			imgs := params.Item.OutputImages
			if len(imgs) != 1 || imgs[0].URL != "/s/"+sessionID+"/images/"+sha {
				t.Fatalf("OutputImages=%+v, want the sha route stamped on the fanned-out descriptor", imgs)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for the relayed completed notification")
		}
	}
}

// TestHubRPCThreadReadRelaysSHARoutedToolResultImage is the live-streaming path
// end to end through the relay (kata 2fxm): the daemon publishes an
// item/completed whose tool-result image is named by sha and nothing else, and
// the browser must receive a descriptor it can actually fetch.
func TestHubRPCThreadReadRelaysSHARoutedToolResultImage(t *testing.T) {
	sessionID := "02wMz5Txv733WHFsVy66SR"
	sha := strings.Repeat("a", 64)
	source := &relayBroadcastSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "th_shot",
			SessionID: sessionID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: "local:th_shot", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_shot"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
		"turnId": "turn_1",
		"item": appwire.ThreadItem{
			Type: "commandExecution", ID: "item_shot", ToolName: "screenshot", CallID: "call_shot",
			Output: "captured", Status: appwire.TurnStatusCompleted,
			OutputImages: []appwire.OutputImage{{
				Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 11, SHA: sha,
			}},
		},
	}).Notification

	var completed appwire.Notification
	for range 3 {
		select {
		case got := <-client.Notifications():
			if got.Method == appwire.NotifyItemCompleted {
				completed = got
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for relayed completed notification")
		}
		if completed.Method != "" {
			break
		}
	}
	if completed.Method == "" {
		t.Fatal("completed notification was not relayed")
	}
	var params struct {
		Item appwire.ThreadItem `json:"item"`
	}
	if err := json.Unmarshal(completed.Params, &params); err != nil {
		t.Fatalf("unmarshal completed params: %v", err)
	}
	imgs := params.Item.OutputImages
	if len(imgs) != 1 || imgs[0].URL != "/s/"+sessionID+"/images/"+sha {
		t.Fatalf("OutputImages=%+v, want the sha route stamped on the relayed descriptor", imgs)
	}
}

func TestHubRPCThreadReadRelaysNotificationsBySourceQualifiedThread(t *testing.T) {
	threadID := "shared_thread"
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(sourceA)
	web.sources.Add(sourceB)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	clientA := dialHubRPC(t, srv)
	defer clientA.Close()
	if _, err := clientA.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize clientA: %v", err)
	}
	if _, err := clientA.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:" + threadID}); err != nil {
		t.Fatalf("ThreadRead clientA: %v", err)
	}
	clientB := dialHubRPC(t, srv)
	defer clientB.Close()
	if _, err := clientB.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize clientB: %v", err)
	}
	if _, err := clientB.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:" + threadID}); err != nil {
		t.Fatalf("ThreadRead clientB: %v", err)
	}

	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex-b:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "from source b",
		}),
	}

	select {
	case got := <-clientB.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("clientB method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for source b notification")
	}
	// Prove isolation: send a sentinel on sourceA so clientA receives something.
	// If sourceB's notification had leaked to clientA it would have arrived
	// first (it was broadcast before the sentinel), so receiving the sentinel
	// as the first notification structurally proves no cross-source leak.
	sourceA.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex-a:" + threadID,
			TurnID:   "turn_sentinel",
			ItemID:   "item_sentinel",
			Delta:    "sentinel",
		}),
	}
	select {
	case got := <-clientA.Notifications():
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("clientA: unmarshal params: %v", err)
		}
		if params.Ref != "codex-a:"+threadID {
			t.Fatalf("clientA received cross-source notification before sentinel: ref=%q", params.Ref)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sentinel notification on clientA")
	}
}

func TestHubRPCThreadReadSubscribeOverridesSourceReadRelayPolicy(t *testing.T) {
	threadID := "th_codex_live"
	source := &readRelayDisabledSource{
		id: "codex",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "from codex",
		}),
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribed codex notification")
	}
}

// thread/unsubscribe must drop the calling connection's downstream
// subscription — the registry entry thread/read's relay created — so the
// relay's idle ticker sees SubscriberCount zero and can retire, and a second
// call stays a quiet no-op.
func TestHubRPCThreadUnsubscribeDropsDownstreamSubscription(t *testing.T) {
	const threadID = "th_unsub"
	source := &relayBroadcastSource{
		id: "codex",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if got := web.appRPC.SubscriberCount("codex:" + threadID); got != 1 {
		t.Fatalf("subscriber count after subscribed read = %d, want 1", got)
	}

	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadUnsubscribe: %v", err)
	}
	if got := web.appRPC.SubscriberCount("codex:" + threadID); got != 0 {
		t.Fatalf("subscriber count after unsubscribe = %d, want 0", got)
	}

	// The relay's notifications no longer reach this connection.
	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "after unsubscribe",
		}),
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("notification delivered after unsubscribe: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	// Idempotent: unsubscribing again succeeds quietly.
	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("second ThreadUnsubscribe: %v", err)
	}
	if got := web.appRPC.SubscriberCount("codex:" + threadID); got != 0 {
		t.Fatalf("subscriber count after second unsubscribe = %d, want 0", got)
	}
}

// The fallback branch: when no source resolves (an exited session removed
// its rendezvous entry), an unsubscribe still quietly succeeds. The handler
// resolves through the plain registry (sourceForThread, never the
// managed-launch path), so no launcher is even consulted — an unsubscribe is
// a "stop caring" operation and must not spawn.
func TestHubRPCThreadUnsubscribeUnresolvedSourceIsQuiet(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// A ref whose source never existed: quiet success, no error.
	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: "codex:th_missing"}); err != nil {
		t.Fatalf("ThreadUnsubscribe for unresolvable source: %v", err)
	}
	// The same for a bare threadID with no ref.
	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{ThreadID: "th_missing"}); err != nil {
		t.Fatalf("ThreadUnsubscribe for unresolvable thread: %v", err)
	}
}

func TestHubRPCThreadReadRecoversEstablishedRelayAfterSourceClose(t *testing.T) {
	const threadID = "th_recover"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	notificationsA <- relayDeltaNotification(t, threadID, "event A")
	expectRelayDelta(t, client.Notifications(), "event A")
	close(notificationsA)

	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	notificationsB <- relayDeltaNotification(t, threadID, "event B")
	expectRelayDelta(t, client.Notifications(), "event B")
}

func TestHubRelayRecoveryEmitsThreadResyncBeforeReplacementNotifications(t *testing.T) {
	const threadID = "th_resync"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("initial relay emitted notification %+v, want none", got)
	default:
	}
	notificationsA <- relayDeltaNotification(t, threadID, "initial event")
	expectRelayDelta(t, client.Notifications(), "initial event")

	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("replacement unavailable")}
	retryClock.releaseWait(t, 100*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)

	notificationsB <- relayDeltaNotification(t, threadID, "replacement event")
	expectRelayDelta(t, client.Notifications(), "replacement event")
}

func TestRelayRetryClockWaitStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newRelayRetryClock().Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error=%v, want context canceled", err)
	}
}

func TestRelayRetryBackoffCapsAtFiveSeconds(t *testing.T) {
	var backoff relayRetryBackoff
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
		5 * time.Second,
		5 * time.Second,
	}
	for i, delay := range want {
		if got := backoff.Next(); got != delay {
			t.Fatalf("Next call %d=%s, want %s", i+1, got, delay)
		}
	}
}

func TestHubRPCThreadReadRelayRecoveryBackoffAndReset(t *testing.T) {
	const threadID = "th_retry_backoff"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed once")}
	retryClock.releaseWait(t, 100*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed twice")}
	retryClock.releaseWait(t, 200*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	notificationsB <- relayDeltaNotification(t, threadID, "recovered")
	expectRelayDelta(t, client.Notifications(), "recovered")

	close(notificationsB)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed after success")}
	retryClock.expectWait(t, 100*time.Millisecond)
}

// relayTurnStartedNotification builds the turn/started notification the relay
// forwards from a real source, used to seed the relay's activeTurnID tracking
// the same way a live daemon would.
func relayTurnStartedNotification(t *testing.T, threadID, turnID string) appwire.Notification {
	t.Helper()
	return appwire.Notification{
		Method: appwire.NotifyTurnStarted,
		Params: testRawJSON(t, appwire.TurnStartedParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			Turn:     appwire.Turn{ID: turnID, Status: appwire.TurnStatusInProgress},
		}),
	}
}

// expectRelaySynthesizedTurnFailure asserts the next notification is the
// hub-authored turn/completed(failed) kata 3h02 synthesizes once a mid-turn
// daemon stops answering: the same shape TurnFailureEndCap already renders
// for a real daemon failure (connection-class, so its "Reconnect & retry"
// button appears).
func expectRelaySynthesizedTurnFailure(t *testing.T, notifications <-chan appwire.Notification, wantTurnID, wantMessageContains string) {
	t.Helper()
	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyTurnCompleted {
			t.Fatalf("notification method=%q, want %q", got.Method, appwire.NotifyTurnCompleted)
		}
		var params appwire.TurnCompletedParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal turn/completed: %v", err)
		}
		if params.Turn.ID != wantTurnID {
			t.Fatalf("turn.id=%q, want %q", params.Turn.ID, wantTurnID)
		}
		if params.Turn.Status != appwire.TurnStatusFailed {
			t.Fatalf("turn.status=%q, want %q", params.Turn.Status, appwire.TurnStatusFailed)
		}
		if params.Turn.Error == nil {
			t.Fatal("turn.error is nil, want a connection-class TurnError")
		}
		if params.Turn.Error.Source != "hub" {
			t.Fatalf("turn.error.source=%q, want %q", params.Turn.Error.Source, "hub")
		}
		if !strings.Contains(params.Turn.Error.Message, wantMessageContains) {
			t.Fatalf("turn.error.message=%q, want it to contain %q", params.Turn.Error.Message, wantMessageContains)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the synthesized turn failure")
	}
}

// TestHubRelaySynthesizesConnectionFailureForActiveTurnAfterRepeatedRedialFailures
// covers kata 3h02: a daemon SIGKILLed mid-turn leaves the recovery loop
// re-dialing a socket nothing answers, forever, with no diagnostic. After
// relayGiveUpAfterFailures consecutive re-dial failures while a turn is
// in-progress, the relay must synthesize a failed turn/completed for that
// turn (source "hub") instead of retrying in total silence - and must fire
// it exactly once per stall, not on every subsequent retry.
func TestHubRelaySynthesizesConnectionFailureForActiveTurnAfterRepeatedRedialFailures(t *testing.T) {
	const threadID = "th_dead_mid_turn"
	const turnID = "turn_dead"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	// The turn opens; the reader is now watching a spinner. Then the daemon
	// is gone (kill -9): its notification channel closes with no error and
	// no persisted TurnFailure, exactly like a SIGKILLed process.
	notifications <- relayTurnStartedNotification(t, threadID, turnID)
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyTurnStarted {
			t.Fatalf("notification method=%q, want %q", got.Method, appwire.NotifyTurnStarted)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for turn/started")
	}
	close(notifications)

	// Two re-dial failures is a blip: no diagnostic yet, no button - the
	// spinner is exactly as legible (or illegible) as it always was.
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (1)")}
	retryClock.releaseWait(t, 100*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (2)")}
	retryClock.releaseWait(t, 200*time.Millisecond)

	// The third consecutive failure crosses relayGiveUpAfterFailures: the
	// relay must stop retrying in silence and tell the reader the turn died.
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (3)")}
	expectRelaySynthesizedTurnFailure(t, client.Notifications(), turnID, "connection refused (3)")
	retryClock.releaseWait(t, 400*time.Millisecond)

	// The loop keeps retrying afterward (recovery is still worth having if
	// the reader clicks "Reconnect & retry" and a fresh relay never
	// replaces this one before it retires) but must not re-broadcast the
	// same failure it already reported.
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (4)")}
	select {
	case got := <-client.Notifications():
		t.Fatalf("unexpected second notification after give-up: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	retryClock.expectWait(t, 800*time.Millisecond)
}

// TestHubRelayNoSyntheticFailureWithoutActiveTurn covers the scoping half of
// kata 3h02's fix: a daemon that dies BETWEEN turns (no spinner on screen,
// nothing for the reader to be confused by) must not manufacture a failed
// turn out of nothing. The relay keeps retrying in silence exactly as
// before - there is no turn id to attach a failure to, and no ambiguity for
// the reader to resolve.
func TestHubRelayNoSyntheticFailureWithoutActiveTurn(t *testing.T) {
	const threadID = "th_dead_between_turns"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}
	close(notifications)

	for i, delay := range []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
	} {
		awaitRelaySubscribeCall(t, subscribeCalls)
		results <- relaySubscribeResult{err: fmt.Errorf("local daemon unavailable: connection refused (%d)", i+1)}
		retryClock.releaseWait(t, delay)
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("unexpected notification with no active turn: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestHubRPCThreadReadRecoveryBacksOffUnusableChannelsWithoutDroppingFirstNotification(t *testing.T) {
	const threadID = "th_retry_unusable"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	established := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: established}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(established)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{notifications: nil}
	retryClock.releaseWait(t, 100*time.Millisecond)

	awaitRelaySubscribeCall(t, subscribeCalls)
	closed := make(chan appwire.Notification)
	close(closed)
	results <- relaySubscribeResult{notifications: closed}
	retryClock.releaseWait(t, 200*time.Millisecond)

	awaitRelaySubscribeCall(t, subscribeCalls)
	buffered := make(chan appwire.Notification, 1)
	buffered <- relayDeltaNotification(t, threadID, "first notification")
	close(buffered)
	results <- relaySubscribeResult{notifications: buffered}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	expectRelayDelta(t, client.Notifications(), "first notification")

	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("failed after usable channel")}
	retryClock.expectWait(t, 100*time.Millisecond)
}

func TestHubRPCThreadReadClientCloseCancelsRelayRecoveryWait(t *testing.T) {
	const threadID = "th_retry_cancel"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	idleExit := make(chan struct{})
	afterIdleDelete := make(chan struct{})
	var idleOnce sync.Once
	var afterIdleOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	cfg.RelayHooks.IdleExit = func(gotThreadID string) {
		if gotThreadID == threadID {
			idleOnce.Do(func() { close(idleExit) })
		}
	}
	cfg.RelayHooks.AfterIdleDelete = func(gotThreadID string) {
		if gotThreadID == threadID {
			afterIdleOnce.Do(func() { close(afterIdleDelete) })
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(notifications)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed")}
	wait := retryClock.nextWait(t)
	if wait.delay != relayRetryMinDelay {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, relayRetryMinDelay)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	select {
	case <-idleExit:
	case <-time.After(time.Second):
		t.Fatal("relay did not observe zero subscribers while retry waited")
	}
	select {
	case <-afterIdleDelete:
	case <-time.After(time.Second):
		t.Fatal("relay handle was not removed while retry waited")
	}
	select {
	case <-wait.canceled:
	case <-time.After(time.Second):
		t.Fatal("relay retry wait was not canceled")
	}
	select {
	case <-subscribeCalls:
		t.Fatal("relay subscribed again after recovery cancellation")
	default:
	}

	replacementClient := dialHubRPC(t, srv)
	defer replacementClient.Close()
	if _, err := replacementClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize replacement client: %v", err)
	}
	replacementReadErr := make(chan error, 1)
	go func() {
		_, err := replacementClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		replacementReadErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	replacementNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: replacementNotifications}
	select {
	case err := <-replacementReadErr:
		if err != nil {
			t.Fatalf("replacement ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement ThreadRead")
	}
}

func TestHubRPCThreadReadClientCloseCancelsBlockingRecoverySubscribe(t *testing.T) {
	const threadID = "th_blocking_recovery_cancel"
	established := make(chan appwire.Notification)
	replacementResults := make(chan relaySubscribeResult)
	source := &blockingRecoveryRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		established:        established,
		recoveryStarted:    make(chan struct{}),
		recoveryCanceled:   make(chan struct{}),
		recoveryReturned:   make(chan struct{}),
		replacementStarted: make(chan struct{}),
		replacementResults: replacementResults,
	}
	idleExit := make(chan struct{})
	afterIdleDelete := make(chan struct{})
	var idleOnce sync.Once
	var deleteOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.IdleExit = func(gotThreadID string) {
		if gotThreadID == threadID {
			idleOnce.Do(func() { close(idleExit) })
		}
	}
	cfg.RelayHooks.AfterIdleDelete = func(gotThreadID string) {
		if gotThreadID == threadID {
			deleteOnce.Do(func() { close(afterIdleDelete) })
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	close(established)
	select {
	case <-source.recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery subscribe did not start")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	select {
	case <-idleExit:
	case <-time.After(time.Second):
		t.Fatal("relay did not service idle tick during blocking recovery subscribe")
	}
	select {
	case <-source.recoveryCanceled:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery source context was not canceled")
	}
	select {
	case <-source.recoveryReturned:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery subscribe was not joined")
	}
	select {
	case <-afterIdleDelete:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery relay handle was not removed")
	}

	replacementClient := dialHubRPC(t, srv)
	defer replacementClient.Close()
	if _, err := replacementClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize replacement: %v", err)
	}
	replacementRead := make(chan error, 1)
	go func() {
		_, err := replacementClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		replacementRead <- err
	}()
	select {
	case <-source.replacementStarted:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement relay did not subscribe")
	}
	replacementNotifications := make(chan appwire.Notification)
	replacementResults <- relaySubscribeResult{notifications: replacementNotifications}
	select {
	case err := <-replacementRead:
		if err != nil {
			t.Fatalf("replacement ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replacement relay did not become ready")
	}
}

func TestHubRPCThreadReadRereadJoinsRelayRecovery(t *testing.T) {
	const threadID = "th_retry_join"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed")}
	wait := retryClock.nextWait(t)
	if wait.delay != relayRetryMinDelay {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, relayRetryMinDelay)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("concurrent recovery ThreadRead: %v", err)
	}
	select {
	case <-subscribeCalls:
		t.Fatal("reread created a duplicate relay supervisor")
	default:
	}
	close(wait.release)
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	notificationsB <- relayDeltaNotification(t, threadID, "after joined recovery")
	expectRelayDelta(t, client.Notifications(), "after joined recovery")
}

func TestHubRPCThreadReadReplacementStopsOldRelayRecovery(t *testing.T) {
	const oldThreadID = "th_retry_replaced"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	oldSource := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        oldThreadID,
			SessionID: oldThreadID,
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:" + oldThreadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		id:             "codex-a",
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	newSource := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        "th_active_replacement",
			SessionID: "th_active_replacement",
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:th_active_replacement", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	idleExit := make(chan struct{})
	afterIdleDelete := make(chan struct{})
	var idleOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	cfg.RelayHooks.IdleExit = func(threadID string) {
		if threadID == oldThreadID {
			idleOnce.Do(func() { close(idleExit) })
		}
	}
	cfg.RelayHooks.AfterIdleDelete = func(threadID string) {
		if threadID == oldThreadID {
			close(afterIdleDelete)
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(oldSource)
	web.sources.Add(newSource)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:" + oldThreadID, Subscribe: true, ReplaceSubscription: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	oldNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: oldNotifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("old ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for old ThreadRead")
	}
	close(oldNotifications)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("old recovery failed")}
	wait := retryClock.nextWait(t)

	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:th_active_replacement", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("replacement ThreadRead: %v", err)
	}
	expectRelaySubscription(t, newSource.subscribed)
	select {
	case <-idleExit:
	case <-time.After(time.Second):
		t.Fatal("replaced relay did not reach zero-subscriber idle retirement")
	}
	select {
	case <-afterIdleDelete:
	case <-time.After(time.Second):
		t.Fatal("replaced relay handle was not removed")
	}
	select {
	case <-wait.canceled:
	case <-time.After(time.Second):
		t.Fatal("replaced relay recovery wait was not canceled")
	}
	select {
	case <-subscribeCalls:
		t.Fatal("replaced relay subscribed again after idle retirement")
	default:
	}

	newSource.notifications <- relayDeltaNotification(t, "th_active_replacement", "active replacement")
	expectRelayDelta(t, client.Notifications(), "active replacement")
}

func TestHubRelayCanceledRecoveryDoesNotSubscribeAgain(t *testing.T) {
	const threadID = "th_stop_no_resubscribe"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := subscribeRelayRecovery(ctx, source, appwire.ThreadReadParams{Ref: "codex:" + threadID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery subscribe error=%v, want context canceled", err)
	}
	select {
	case <-subscribeCalls:
		t.Fatal("canceled relay supervisor subscribed again")
	default:
	}
}

func TestHubRelayStopDuringInitializationCancelsSharedHandleAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_stop_initializing"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	placeholderPublished := make(chan struct{})
	releaseInitializer := make(chan struct{})
	var placeholderOnce sync.Once
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	cfg.RelayHooks.AfterPlaceholder = func(gotThreadID string) {
		if gotThreadID == threadID {
			placeholderOnce.Do(func() { close(placeholderPublished) })
			<-releaseInitializer
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())

	startResults := make(chan error, 2)
	go func() {
		startResults <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-placeholderPublished:
	case <-time.After(time.Second):
		t.Fatal("initial relay placeholder was not published")
	}
	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { waiterJoined <- struct{}{} }
	defer func() { observeHubRelayWait = previousObserveWait }()
	go func() {
		startResults <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-waiterJoined:
	case <-time.After(time.Second):
		t.Fatal("concurrent initializer did not join shared relay handle")
	}

	relays.stopRelay("codex:" + threadID)
	select {
	case err := <-startResults:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shared stop error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		close(releaseInitializer)
		awaitRelaySubscribeCall(t, subscribeCalls)
		initial := make(chan appwire.Notification)
		results <- relaySubscribeResult{notifications: initial}
		t.Fatal("stopRelay did not unblock initialization waiter")
	}
	close(releaseInitializer)
	select {
	case err := <-startResults:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initial owner stop error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial relay owner did not return shared cancellation")
	}
	select {
	case <-subscribeCalls:
		t.Fatal("canceled placeholder entered initial SubscribeThread")
	default:
	}

	observeHubRelayWait = previousObserveWait
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	freshNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: freshNotifications}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh start after initialization stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh relay did not become ready after initialization stop")
	}
}

func TestHubRelayKeyStopKeepsCanonicalSiblingAndListener(t *testing.T) {
	const (
		rootRef  = "local:stop-root"
		childRef = "local:stop-child"
	)
	lease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			ref, err := appwire.ParseRef(params.Ref)
			if err != nil {
				return appsource.RelayReadResult{}, err
			}
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: ref.ThreadID, Source: ref.SourceID,
					Evener: appwire.EvenerThread{Ref: params.Ref},
				}},
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		lease: lease,
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			return appwire.ParseRef(rootRef)
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	read := func(ref string) {
		t.Helper()
		result, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
		if err != nil {
			t.Fatalf("readThread(%q): %v", ref, err)
		}
		result.finish(false)
	}
	read(rootRef)
	read(childRef)
	if got := source.acquireCallCount(); got != 1 {
		t.Fatalf("initial relay acquisitions = %d, want one canonical handle", got)
	}

	relays.stopRelay(childRef)
	if got := lease.closeCallCount(); got != 0 {
		t.Fatalf("lease closes after child-key stop = %d, want 0 while root remains", got)
	}
	read(childRef)
	if got := source.acquireCallCount(); got != 1 {
		t.Fatalf("relay acquisitions after child rebind = %d, want existing canonical handle", got)
	}
	if got := lease.listenCallCount(); got != 1 {
		t.Fatalf("RelaySession Listen calls after child rebind = %d, want one retained listener", got)
	}
	relays.stopRelay(rootRef)
	relays.stopRelay(childRef)
}

func TestHubRelayKeyStopDefersFinalTeardownForInFlightCommand(t *testing.T) {
	const relayKey = "local:stop-in-flight"
	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	leaseClosed := make(chan struct{})
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "stop-in-flight", Source: "local",
				Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		},
		readHook: func() {
			close(readEntered)
			<-releaseRead
		},
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(leaseClosed) },
	}
	source := &relaySessionTestSource{lease: lease}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type readOutcome struct {
		read *hubThreadReadResult
		err  error
	}
	readResult := make(chan readOutcome, 1)
	go func() {
		read, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
		readResult <- readOutcome{read: read, err: err}
	}()
	<-readEntered

	relays.stopRelay(relayKey)
	if got := relays.relayCommandCount(relayKey); got != 1 {
		close(releaseRead)
		t.Fatalf("command owners after deferred relay-key stop = %d, want 1", got)
	}
	if got := lease.closeCallCount(); got != 0 {
		close(releaseRead)
		t.Fatalf("lease closes while stopped key command is in flight = %d, want 0", got)
	}
	select {
	case <-leaseClosed:
		close(releaseRead)
		t.Fatal("final canonical lease closed before the in-flight command released")
	default:
	}

	close(releaseRead)
	result := <-readResult
	if result.err != nil {
		t.Fatalf("readThread: %v", result.err)
	}
	result.read.finish(false)
	select {
	case <-leaseClosed:
	case <-time.After(time.Second):
		t.Fatal("deferred relay-key stop did not close the final lease after command release")
	}
	if got := lease.closeCallCount(); got != 1 {
		t.Fatalf("final lease closes = %d, want 1", got)
	}
}

func TestHubRelayPostStopCommandWaitsForFreshGeneration(t *testing.T) {
	const relayKey = "local:stop-fresh-generation"
	readResult := func() appsource.RelayReadResult {
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "stop-fresh-generation", Source: "local",
				Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	oldReadEntered := make(chan struct{})
	releaseOldRead := make(chan struct{})
	oldLeaseClosed := make(chan struct{})
	var oldReadOnce sync.Once
	oldLease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			oldReadOnce.Do(func() {
				close(oldReadEntered)
				<-releaseOldRead
			})
			return readResult(), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(oldLeaseClosed) },
	}
	freshLease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return readResult(), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type readOutcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func() <-chan readOutcome {
		out := make(chan readOutcome, 1)
		go func() {
			read, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- readOutcome{read: read, err: err}
		}()
		return out
	}
	oldResult := startRead()
	<-oldReadEntered
	relays.stopRelay(relayKey)

	postStopDeferred := make(chan struct{})
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { close(postStopDeferred) }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStopResult := startRead()
	select {
	case <-postStopDeferred:
	case <-time.After(time.Second):
		close(releaseOldRead)
		t.Fatal("post-stop command joined the stopped generation instead of waiting")
	}
	if got := relays.relayCommandCount(relayKey); got != 1 {
		close(releaseOldRead)
		t.Fatalf("stopped generation command owners = %d, want only the original command", got)
	}
	if got := oldLease.readCallCount(); got != 1 {
		close(releaseOldRead)
		t.Fatalf("stopped generation Read calls = %d, want 1", got)
	}
	if got := oldLease.closeCallCount(); got != 0 {
		close(releaseOldRead)
		t.Fatalf("old lease closes while original command is in flight = %d, want 0", got)
	}

	close(releaseOldRead)
	old := <-oldResult
	if old.err != nil {
		t.Fatalf("old readThread: %v", old.err)
	}
	old.read.finish(false)
	select {
	case <-oldLeaseClosed:
	case <-time.After(time.Second):
		t.Fatal("stopped generation did not close after its original command released")
	}
	postStop := <-postStopResult
	if postStop.err != nil {
		t.Fatalf("post-stop readThread: %v", postStop.err)
	}
	postStop.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("relay acquisitions after stopped generation retired = %d, want fresh second generation", got)
	}
	if got := oldLease.readCallCount(); got != 1 {
		t.Fatalf("old generation Read calls after fresh read = %d, want 1", got)
	}
	if got := freshLease.readCallCount(); got != 1 || freshLease.listenCallCount() != 1 {
		t.Fatalf("fresh generation calls: Read=%d Listen=%d, want 1/1", got, freshLease.listenCallCount())
	}

	rejoined, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatalf("fresh generation rejoin: %v", err)
	}
	rejoined.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("relay acquisitions after fresh generation rejoin = %d, want 2", got)
	}
	if got := freshLease.listenCallCount(); got != 1 {
		t.Fatalf("fresh generation Listen calls after rejoin = %d, want 1", got)
	}
	relays.stopRelay(relayKey)
}

func TestHubRelayStopCoversOverlappingPendingGenerations(t *testing.T) {
	const relayKey = "local:overlapping-pending-stop"
	canonicalA := appwire.Ref{SourceID: "local", ThreadID: "overlapping-canonical-a"}
	canonicalB := appwire.Ref{SourceID: "local", ThreadID: "overlapping-canonical-b"}
	readResult := func() appsource.RelayReadResult {
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "overlapping-pending-stop", Source: "local",
				Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	type heldLease struct {
		lease   *scriptedRelaySessionLease
		entered chan struct{}
		release func()
		closed  chan struct{}
	}
	newHeldLease := func() heldLease {
		entered := make(chan struct{})
		releaseRead := make(chan struct{})
		closed := make(chan struct{})
		var releaseOnce sync.Once
		lease := &scriptedRelaySessionLease{
			readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
				close(entered)
				<-releaseRead
				return readResult(), nil
			},
			deliveries: make(chan appsource.RelayDelivery),
			closeHook:  func() { close(closed) },
		}
		return heldLease{
			lease:   lease,
			entered: entered,
			release: func() { releaseOnce.Do(func() { close(releaseRead) }) },
			closed:  closed,
		}
	}
	heldA := newHeldLease()
	heldB := newHeldLease()
	defer heldA.release()
	defer heldB.release()
	freshLease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return readResult(), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var resolveMu sync.Mutex
	resolved := canonicalA
	acquisitionsA := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			resolveMu.Lock()
			defer resolveMu.Unlock()
			return resolved, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalB {
				return routeAwareTestLease(heldB.lease), nil
			}
			acquisitionsA++
			if acquisitionsA == 1 {
				return routeAwareTestLease(heldA.lease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type readOutcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func() <-chan readOutcome {
		out := make(chan readOutcome, 1)
		go func() {
			read, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- readOutcome{read: read, err: err}
		}()
		return out
	}
	readA := startRead()
	<-heldA.entered
	resolveMu.Lock()
	resolved = canonicalB
	resolveMu.Unlock()
	readB := startRead()
	<-heldB.entered
	if got := relays.relayCommandCount(relayKey); got != 2 {
		t.Fatalf("overlapping pending command owners = %d, want 2", got)
	}

	relays.stopRelay(relayKey)
	if got := relays.relayCommandCount(relayKey); got != 2 {
		t.Fatalf("stopped overlapping pending command owners = %d, want 2", got)
	}
	resolveMu.Lock()
	resolved = canonicalA
	resolveMu.Unlock()
	deferred := make(chan struct{}, 3)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { deferred <- struct{}{} }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStop := startRead()
	select {
	case <-deferred:
	case <-time.After(time.Second):
		t.Fatal("post-stop command did not defer behind the older hidden pending generation")
	}
	if got := heldA.lease.readCallCount(); got != 1 {
		t.Fatalf("older stopped pending Read calls = %d, want 1", got)
	}
	if got := heldB.lease.readCallCount(); got != 1 {
		t.Fatalf("newer stopped pending Read calls = %d, want 1", got)
	}

	heldA.release()
	resultA := <-readA
	if resultA.err != nil {
		t.Fatalf("older pending read: %v", resultA.err)
	}
	if relays.relayPublished(relayKey) {
		t.Fatal("older stopped pending read published downstream ownership")
	}
	resultA.read.finish(false)
	select {
	case <-heldA.closed:
	case <-time.After(time.Second):
		t.Fatal("older stopped pending handle did not close after its own command released")
	}
	select {
	case <-deferred:
	case <-time.After(time.Second):
		t.Fatal("post-stop command did not remain deferred behind the newer pending generation")
	}
	if got := relays.relayCommandCount(relayKey); got != 1 {
		t.Fatalf("pending command owners after older release = %d, want newer owner only", got)
	}
	if got := heldB.lease.closeCallCount(); got != 0 {
		t.Fatalf("newer pending lease closes before its command release = %d, want 0", got)
	}

	heldB.release()
	resultB := <-readB
	if resultB.err != nil {
		t.Fatalf("newer pending read: %v", resultB.err)
	}
	if relays.relayPublished(relayKey) {
		t.Fatal("newer stopped pending read published downstream ownership")
	}
	resultB.read.finish(false)
	select {
	case <-heldB.closed:
	case <-time.After(time.Second):
		t.Fatal("newer stopped pending handle did not close after its own command released")
	}
	fresh := <-postStop
	if fresh.err != nil {
		t.Fatalf("fresh post-stop read: %v", fresh.err)
	}
	if !relays.relayPublished(relayKey) {
		t.Fatal("fresh post-stop generation did not publish downstream ownership")
	}
	fresh.read.finish(false)
	if got := source.acquireCallCount(); got != 3 {
		t.Fatalf("relay acquisitions after overlapping stop = %d, want two old plus one fresh", got)
	}
	if got := freshLease.readCallCount(); got != 1 || freshLease.listenCallCount() != 1 {
		t.Fatalf("fresh generation calls: Read=%d Listen=%d, want 1/1", got, freshLease.listenCallCount())
	}
	if got := heldA.lease.readCallCount(); got != 1 {
		t.Fatalf("older stale generation Read calls = %d, want no post-stop join", got)
	}
	if got := heldB.lease.readCallCount(); got != 1 {
		t.Fatalf("newer stale generation Read calls = %d, want no post-stop join", got)
	}

	rejoined, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatalf("fresh generation rejoin: %v", err)
	}
	rejoined.finish(false)
	if got := source.acquireCallCount(); got != 3 {
		t.Fatalf("relay acquisitions after fresh rejoin = %d, want 3", got)
	}
	if got := freshLease.listenCallCount(); got != 1 {
		t.Fatalf("fresh generation listeners after rejoin = %d, want 1", got)
	}
	relays.stopRelay(relayKey)
}

func TestHubRelayCanonicalStopRetiresOnlyNamedHandle(t *testing.T) {
	canonicalA := appwire.Ref{SourceID: "local", ThreadID: "stop-canonical-a"}
	canonicalB := appwire.Ref{SourceID: "local", ThreadID: "stop-canonical-b"}
	newLease := func() *scriptedRelaySessionLease {
		return &scriptedRelaySessionLease{
			readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
				ref, err := appwire.ParseRef(params.Ref)
				if err != nil {
					return appsource.RelayReadResult{}, err
				}
				return appsource.RelayReadResult{
					Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
						ID: ref.ThreadID, Source: ref.SourceID,
						Evener: appwire.EvenerThread{Ref: params.Ref},
					}},
					Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
				}, nil
			},
			deliveries: make(chan appsource.RelayDelivery),
		}
	}
	leaseA := newLease()
	leaseB := newLease()
	source := &relaySessionTestSource{
		resolveRelay: func(params appwire.ThreadReadParams) (appwire.Ref, error) {
			if params.Ref == canonicalB.String() {
				return canonicalB, nil
			}
			return canonicalA, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalB {
				return routeAwareTestLease(leaseB), nil
			}
			return routeAwareTestLease(leaseA), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	read := func(ref string) {
		t.Helper()
		result, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
		if err != nil {
			t.Fatalf("readThread(%q): %v", ref, err)
		}
		result.finish(false)
	}
	read(canonicalA.String())
	read(canonicalB.String())

	relays.stopCanonicalRelay(canonicalA)
	if got := leaseA.closeCallCount(); got != 1 {
		t.Fatalf("named canonical lease closes = %d, want 1", got)
	}
	if got := leaseB.closeCallCount(); got != 0 {
		t.Fatalf("unrelated canonical lease closes = %d, want 0", got)
	}
	read(canonicalB.String())
	if got := leaseB.listenCallCount(); got != 1 {
		t.Fatalf("unrelated canonical listener starts = %d, want retained single listener", got)
	}
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after unrelated read = %d, want two original canonical handles", got)
	}
	relays.stopCanonicalRelay(canonicalB)
}

func TestHubRelayCanonicalStopRetainsBusyPublishedHandleUntilFreshGeneration(t *testing.T) {
	const relayKey = "local:canonical-stop-busy"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-busy"}
	readResult := func() appsource.RelayReadResult {
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "canonical-stop-busy", Source: "local", Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	oldClosed := make(chan struct{})
	oldLease := &scriptedRelaySessionLease{
		readFunc:   func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) { return readResult(), nil },
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(oldClosed) },
	}
	freshLease := &scriptedRelaySessionLease{
		readFunc:   func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) { return readResult(), nil },
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-busy", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	initial, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	initial.finish(false)

	busyEntered := make(chan struct{})
	releaseBusy := make(chan struct{})
	oldLease.mu.Lock()
	oldLease.readHook = func() {
		close(busyEntered)
		<-releaseBusy
	}
	oldLease.mu.Unlock()
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	busyResult := startRead()
	<-busyEntered
	relays.stopCanonicalRelay(canonical)
	if got := oldLease.closeCallCount(); got != 0 {
		close(releaseBusy)
		t.Fatalf("canonical stop closed busy published lease: %d", got)
	}
	if got := relays.relayCommandCount(relayKey); got != 1 {
		close(releaseBusy)
		t.Fatalf("busy published command owners after canonical stop = %d, want 1", got)
	}

	deferred := make(chan struct{})
	var deferredOnce sync.Once
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { deferredOnce.Do(func() { close(deferred) }) }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStopResult := startRead()
	select {
	case <-deferred:
	case <-time.After(time.Second):
		close(releaseBusy)
		t.Fatal("post-canonical-stop command did not defer behind busy handle")
	}
	if got := oldLease.readCallCount(); got != 2 {
		close(releaseBusy)
		t.Fatalf("old generation Read calls after stop = %d, want initial plus busy only", got)
	}
	close(releaseBusy)
	busy := <-busyResult
	if busy.err != nil {
		t.Fatal(busy.err)
	}
	if got := oldLease.closeCallCount(); got != 0 {
		t.Fatalf("canonical stop closed lease before busy handoff released: %d", got)
	}
	busy.read.finish(false)
	select {
	case <-oldClosed:
	case <-time.After(time.Second):
		t.Fatal("busy canonical generation did not close after exact owner release")
	}
	postStop := <-postStopResult
	if postStop.err != nil {
		t.Fatal(postStop.err)
	}
	postStop.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after canonical stop drain = %d, want fresh generation", got)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("old canonical lease closes = %d, want once", got)
	}
	if got := freshLease.readCallCount(); got != 1 || freshLease.listenCallCount() != 1 {
		t.Fatalf("fresh canonical calls: Read=%d Listen=%d, want 1/1", got, freshLease.listenCallCount())
	}
}

func TestHubRelayCanonicalStopRetainsOverlappingPendingStates(t *testing.T) {
	const (
		relayA = "local:canonical-stop-pending-a"
		relayB = "local:canonical-stop-pending-b"
	)
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-pending-owner"}
	type gate struct {
		entered chan struct{}
		release chan struct{}
	}
	gates := map[string]gate{
		relayA: {entered: make(chan struct{}), release: make(chan struct{})},
		relayB: {entered: make(chan struct{}), release: make(chan struct{})},
	}
	resultFor := func(ref string) appsource.RelayReadResult {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: parsed.ThreadID, Source: parsed.SourceID, Evener: appwire.EvenerThread{Ref: ref},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	oldClosed := make(chan struct{})
	oldLease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			g := gates[params.Ref]
			close(g.entered)
			<-g.release
			return resultFor(params.Ref), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
		closeHook:  func() { close(oldClosed) },
	}
	freshLease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return resultFor(params.Ref), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquisitions int
	var acquireMu sync.Mutex
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-pending", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	startRead := func(ref string) <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	resultA := startRead(relayA)
	<-gates[relayA].entered
	resultB := startRead(relayB)
	<-gates[relayB].entered
	if got := relays.relayCommandCount(relayA) + relays.relayCommandCount(relayB); got != 2 {
		t.Fatalf("overlapping canonical pending owners = %d, want 2", got)
	}
	relays.stopCanonicalRelay(canonical)
	if got := oldLease.closeCallCount(); got != 0 {
		t.Fatalf("canonical stop closed overlapping pending lease: %d", got)
	}

	deferred := make(chan struct{})
	var deferredOnce sync.Once
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { deferredOnce.Do(func() { close(deferred) }) }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	postStop := startRead(relayA)
	select {
	case <-deferred:
	case <-time.After(time.Second):
		close(gates[relayA].release)
		close(gates[relayB].release)
		t.Fatal("post-stop command did not defer behind overlapping pending canonical states")
	}

	close(gates[relayA].release)
	first := <-resultA
	if first.err != nil {
		t.Fatal(first.err)
	}
	if relays.relayPublished(relayA) {
		t.Fatal("stopped pending relay A published")
	}
	first.read.finish(false)
	if got := oldLease.closeCallCount(); got != 0 {
		t.Fatalf("canonical lease closed with second pending owner: %d", got)
	}
	close(gates[relayB].release)
	second := <-resultB
	if second.err != nil {
		t.Fatal(second.err)
	}
	if relays.relayPublished(relayB) {
		t.Fatal("stopped pending relay B published")
	}
	second.read.finish(false)
	select {
	case <-oldClosed:
	case <-time.After(time.Second):
		t.Fatal("overlapping pending canonical handle did not close after all exact owners released")
	}
	fresh := <-postStop
	if fresh.err != nil {
		t.Fatal(fresh.err)
	}
	fresh.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("canonical acquisitions after overlapping drain = %d, want fresh second generation", got)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("old overlapping lease closes = %d, want once", got)
	}
}

func TestHubRelayCanonicalStopWaitsForInitializingAcquire(t *testing.T) {
	const relayKey = "local:canonical-stop-initializing"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-initializing"}
	readResult := appsource.RelayReadResult{
		Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: canonical.ThreadID, Source: canonical.SourceID, Evener: appwire.EvenerThread{Ref: relayKey},
		}},
		Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
	}
	firstAcquireEntered := make(chan struct{})
	releaseFirstAcquire := make(chan struct{})
	secondAcquireEntered := make(chan struct{})
	oldLease := &scriptedRelaySessionLease{readResult: readResult, deliveries: make(chan appsource.RelayDelivery)}
	freshLease := &scriptedRelaySessionLease{readResult: readResult, deliveries: make(chan appsource.RelayDelivery)}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			acquisitions++
			call := acquisitions
			acquireMu.Unlock()
			if call == 1 {
				close(firstAcquireEntered)
				<-releaseFirstAcquire
				return routeAwareTestLease(oldLease), nil
			}
			close(secondAcquireEntered)
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-initializing", SourceID: "local"}),
		hubcore.WebConfig{}, appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	start := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	initial := start()
	<-firstAcquireEntered
	relays.stopCanonicalRelay(canonical)
	waiting := make(chan struct{})
	var waitingOnce sync.Once
	previousWait := observeHubRelayWait
	observeHubRelayWait = func() { waitingOnce.Do(func() { close(waiting) }) }
	t.Cleanup(func() { observeHubRelayWait = previousWait })
	later := start()
	select {
	case <-waiting:
	case <-secondAcquireEntered:
		close(releaseFirstAcquire)
		t.Fatal("post-stop join acquired a fresh lease before stopped initializing acquire drained")
	case <-time.After(time.Second):
		close(releaseFirstAcquire)
		t.Fatal("post-stop join did not reach initializing handle drain wait")
	}
	select {
	case <-secondAcquireEntered:
		close(releaseFirstAcquire)
		t.Fatal("fresh acquisition started while stopped initializing acquire remained blocked")
	default:
	}
	close(releaseFirstAcquire)
	first := <-initial
	if !errors.Is(first.err, context.Canceled) {
		if first.read != nil {
			first.read.finish(false)
		}
		t.Fatalf("stopped initializing read error = %v, want context.Canceled", first.err)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("late old lease closes = %d, want 1", got)
	}
	if got := oldLease.listenCallCount(); got != 0 {
		t.Fatalf("late old lease Listen calls = %d, want 0 after stop", got)
	}
	select {
	case <-secondAcquireEntered:
	case <-time.After(time.Second):
		t.Fatal("fresh acquisition did not start after initializing handle drained")
	}
	next := <-later
	if next.err != nil {
		t.Fatal(next.err)
	}
	next.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after initializing stop = %d, want old plus one fresh", got)
	}
	if got := freshLease.listenCallCount(); got != 1 {
		t.Fatalf("fresh listener starts = %d, want 1", got)
	}
}

func TestHubRelayCanonicalStopInitializingAcquireErrorUnblocksFreshGeneration(t *testing.T) {
	const relayKey = "local:canonical-stop-initializing-error"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-initializing-error"}
	firstAcquireEntered := make(chan struct{})
	releaseFirstAcquire := make(chan struct{})
	acquireFailure := errors.New("initial relay acquisition failed")
	freshLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: canonical.ThreadID, Source: canonical.SourceID, Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			acquisitions++
			call := acquisitions
			acquireMu.Unlock()
			if call == 1 {
				close(firstAcquireEntered)
				<-releaseFirstAcquire
				return nil, acquireFailure
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-initializing-error", SourceID: "local"}),
		hubcore.WebConfig{}, appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	start := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	initial := start()
	<-firstAcquireEntered
	relays.stopCanonicalRelay(canonical)
	later := start()
	close(releaseFirstAcquire)
	first := <-initial
	if !errors.Is(first.err, acquireFailure) {
		t.Fatalf("initial acquisition error = %v, want %v", first.err, acquireFailure)
	}
	next := <-later
	if next.err != nil {
		t.Fatal(next.err)
	}
	next.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after stopped initialization error = %d, want 2", got)
	}
}

type blockingInitializingListenLease struct {
	*scriptedRelaySessionLease
	listenEntered  chan struct{}
	listenCanceled chan struct{}
	releaseListen  chan struct{}
}

func (l *blockingInitializingListenLease) Listen(ctx context.Context) (<-chan appsource.RelayDelivery, error) {
	close(l.listenEntered)
	<-ctx.Done()
	close(l.listenCanceled)
	<-l.releaseListen
	return nil, ctx.Err()
}

func TestHubRelayCanonicalStopRetainsInitializingHandleUntilListenCancellationReturns(t *testing.T) {
	const relayKey = "local:canonical-stop-initializing-listen"
	canonical := appwire.Ref{SourceID: "local", ThreadID: "canonical-stop-initializing-listen"}
	oldBase := &scriptedRelaySessionLease{deliveries: make(chan appsource.RelayDelivery)}
	oldLease := &blockingInitializingListenLease{
		scriptedRelaySessionLease: oldBase,
		listenEntered:             make(chan struct{}),
		listenCanceled:            make(chan struct{}),
		releaseListen:             make(chan struct{}),
	}
	freshLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: canonical.ThreadID, Source: canonical.SourceID, Evener: appwire.EvenerThread{Ref: relayKey},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var acquireMu sync.Mutex
	acquisitions := 0
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
		acquireRelay: func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			acquireMu.Lock()
			defer acquireMu.Unlock()
			acquisitions++
			if acquisitions == 1 {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(freshLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "canonical-stop-initializing-listen", SourceID: "local"}),
		hubcore.WebConfig{}, appsource.NewRegistry(),
	)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	start := func() <-chan outcome {
		out := make(chan outcome, 1)
		go func() {
			read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
			out <- outcome{read: read, err: err}
		}()
		return out
	}
	initial := start()
	<-oldLease.listenEntered
	relays.stopCanonicalRelay(canonical)
	<-oldLease.listenCanceled
	if got := oldLease.closeCallCount(); got != 0 {
		close(oldLease.releaseListen)
		t.Fatalf("initializing lease closed before Listen cancellation returned: %d", got)
	}
	waiting := make(chan struct{})
	var waitOnce sync.Once
	previousWait := observeHubRelayWait
	observeHubRelayWait = func() { waitOnce.Do(func() { close(waiting) }) }
	t.Cleanup(func() { observeHubRelayWait = previousWait })
	later := start()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		close(oldLease.releaseListen)
		t.Fatal("post-stop join did not wait for initializing Listen termination")
	}
	close(oldLease.releaseListen)
	first := <-initial
	if !errors.Is(first.err, context.Canceled) {
		t.Fatalf("stopped initializing Listen error = %v, want context.Canceled", first.err)
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("stopped initializing Listen lease closes = %d, want 1", got)
	}
	next := <-later
	if next.err != nil {
		t.Fatal(next.err)
	}
	next.read.finish(false)
	if got := source.acquireCallCount(); got != 2 {
		t.Fatalf("acquisitions after Listen drain = %d, want old plus one fresh", got)
	}
}

func TestHubRelayInitiatingRequestCancellationStopsInitialSubscribeAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_request_canceled_initializing"
	initialRelease := make(chan struct{})
	var releaseInitial sync.Once
	source := &initialRequestCancelRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		initialStarted:     make(chan struct{}),
		initialCanceled:    make(chan struct{}),
		initialRelease:     initialRelease,
		initialReturned:    make(chan struct{}),
		freshStarted:       make(chan struct{}),
		freshNotifications: make(chan appwire.Notification),
	}
	registrations := make(chan struct{}, 2)
	supervisors := make(chan struct{}, 2)
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool {
		registrations <- struct{}{}
		return true
	}
	cfg.RelayHooks.BeforeSupervisor = func(gotThreadID string) {
		if gotThreadID == threadID {
			supervisors <- struct{}{}
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())
	defer releaseInitial.Do(func() { close(initialRelease) })
	defer relays.stopRelay("codex:" + threadID)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	startResults := make(chan error, 2)
	go func() {
		startResults <- relays.startRelay(requestCtx, source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.initialStarted:
	case <-time.After(time.Second):
		t.Fatal("initial SubscribeThread did not start")
	}

	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { waiterJoined <- struct{}{} }
	defer func() { observeHubRelayWait = previousObserveWait }()
	go func() {
		startResults <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-waiterJoined:
	case <-time.After(time.Second):
		t.Fatal("concurrent requester did not join the initializing relay")
	}

	cancelRequest()
	select {
	case <-source.initialCanceled:
	case <-time.After(time.Second):
		t.Fatal("initiating request cancellation did not cancel initial SubscribeThread")
	}
	select {
	case err := <-startResults:
		t.Fatalf("relay returned before canceled initial SubscribeThread joined: %v", err)
	default:
	}
	select {
	case <-registrations:
		t.Fatal("canceled initializer registered a downstream subscription")
	default:
	}
	select {
	case <-supervisors:
		t.Fatal("canceled initializer launched a supervisor")
	default:
	}

	observeHubRelayWait = previousObserveWait
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.freshStarted:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not start after requester-canceled initialization")
	}
	select {
	case <-registrations:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not register downstream")
	}
	select {
	case <-supervisors:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not launch a supervisor")
	}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh replacement start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not become ready")
	}

	releaseInitial.Do(func() { close(initialRelease) })
	select {
	case <-source.initialReturned:
	case <-time.After(time.Second):
		t.Fatal("canceled initial SubscribeThread did not return")
	}
	for range 2 {
		select {
		case err := <-startResults:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("shared initialization result=%v, want context canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("initializer and waiter did not receive shared cancellation")
		}
	}
	select {
	case <-registrations:
		t.Fatal("canceled initializer registered downstream after SubscribeThread returned")
	default:
	}
	select {
	case <-supervisors:
		t.Fatal("canceled initializer launched a supervisor after SubscribeThread returned")
	default:
	}
}

func TestHubRelaySurvivesInitiatingRequestCancellationAfterAttachment(t *testing.T) {
	const threadID = "th_request_canceled_established"
	notifications := make(chan appwire.Notification)
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: notifications,
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())
	defer relays.stopRelay("codex:" + threadID)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	if err := relays.startRelay(requestCtx, source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("start relay: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	cancelRequest()
	select {
	case <-source.canceled:
		t.Fatal("established relay was canceled with its initiating request")
	default:
	}

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("join established relay after initiating request cancellation: %v", err)
	}
	select {
	case <-source.subscribed:
		t.Fatal("initiating request cancellation caused a replacement upstream subscription")
	default:
	}
	notifications <- relayDeltaNotification(t, threadID, "survived request cancellation")
}

func TestHubRelayStoppedInitializerRejectsSuccessfulSubscribeAndLeavesReplacement(t *testing.T) {
	const threadID = "th_stale_initializer"
	oldNotifications := make(chan appwire.Notification)
	freshNotifications := make(chan appwire.Notification)
	source := &successfulAfterCancelRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		initialStarted:     make(chan struct{}),
		initialCanceled:    make(chan struct{}),
		releaseInitial:     make(chan struct{}),
		initialReturned:    make(chan struct{}),
		oldNotifications:   oldNotifications,
		freshStarted:       make(chan struct{}),
		freshNotifications: freshNotifications,
	}
	registrationCalls := make(chan struct{}, 2)
	supervisorStarts := make(chan struct{}, 2)
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool {
		registrationCalls <- struct{}{}
		return true
	}
	cfg.RelayHooks.BeforeSupervisor = func(gotThreadID string) {
		if gotThreadID == threadID {
			supervisorStarts <- struct{}{}
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())

	oldResult := make(chan error, 1)
	go func() {
		oldResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.initialStarted:
	case <-time.After(time.Second):
		t.Fatal("initial SubscribeThread did not start")
	}
	relays.stopRelay("codex:" + threadID)
	select {
	case <-source.initialCanceled:
	case <-time.After(time.Second):
		t.Fatal("stopped initializer source context was not canceled")
	}

	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-source.freshStarted:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement SubscribeThread did not start")
	}
	select {
	case <-registrationCalls:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not register downstream")
	}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh replacement start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replacement did not become ready")
	}
	select {
	case <-supervisorStarts:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement supervisor did not start")
	}

	close(source.releaseInitial)
	select {
	case <-source.initialReturned:
	case <-time.After(time.Second):
		t.Fatal("stale initial SubscribeThread did not return success")
	}
	select {
	case err := <-oldResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stale initializer error=%v, want shared context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale initializer did not return shared cancellation")
	}
	select {
	case <-registrationCalls:
		t.Fatal("stale initializer registered downstream after losing ownership")
	default:
	}
	select {
	case <-supervisorStarts:
		t.Fatal("stale initializer started a supervisor after losing ownership")
	default:
	}

	freshNotifications <- relayDeltaNotification(t, threadID, "fresh remains live")
}

func TestHubRelayStopImmediatelyAfterReadinessAllowsFreshStart(t *testing.T) {
	const threadID = "th_stop_ready"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	readyReached := make(chan struct{})
	releaseReady := make(chan struct{})
	blockFirstReady := make(chan struct{}, 1)
	blockFirstReady <- struct{}{}
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	cfg.RelayHooks.AfterReady = func(gotThreadID string) {
		if gotThreadID != threadID {
			return
		}
		select {
		case <-blockFirstReady:
			close(readyReached)
			<-releaseReady
		default:
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())
	initialResult := make(chan error, 1)
	go func() {
		initialResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	initialNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: initialNotifications}
	select {
	case <-readyReached:
	case <-time.After(time.Second):
		t.Fatal("initial relay did not reach readiness boundary")
	}

	relays.stopRelay("codex:" + threadID)
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	select {
	case <-subscribeCalls:
	case err := <-freshResult:
		close(releaseReady)
		t.Fatalf("fresh start returned without subscribing after ready stop: %v", err)
	case <-time.After(time.Second):
		close(releaseReady)
		t.Fatal("fresh start did not replace ready stopped relay")
	}
	freshNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: freshNotifications}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh start after ready stop: %v", err)
		}
	case <-time.After(time.Second):
		close(releaseReady)
		t.Fatal("fresh relay did not become ready after ready stop")
	}
	close(releaseReady)
	select {
	case err := <-initialResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initial ready relay stop error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial ready start did not return shared cancellation")
	}
}

func TestHubRelayStopBeforeLaunchCommitPreventsSupervisorAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_stop_launch_commit"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	launchBoundary := make(chan struct{})
	releaseLaunch := make(chan struct{})
	launches := make(chan struct{}, 2)
	blockFirst := make(chan struct{}, 1)
	blockFirst <- struct{}{}
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool { return true }
	cfg.RelayHooks.BeforeLaunchCommit = func(gotThreadID string) {
		if gotThreadID != threadID {
			return
		}
		select {
		case <-blockFirst:
			close(launchBoundary)
			<-releaseLaunch
		default:
		}
	}
	cfg.RelayHooks.BeforeSupervisor = func(gotThreadID string) {
		if gotThreadID == threadID {
			launches <- struct{}{}
		}
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())

	oldResult := make(chan error, 1)
	go func() {
		oldResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	oldNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: oldNotifications}
	select {
	case <-launchBoundary:
	case <-time.After(time.Second):
		t.Fatal("old initializer did not reach post-validation pre-launch boundary")
	}

	relays.stopRelay("codex:" + threadID)
	freshResult := make(chan error, 1)
	go func() {
		freshResult <- relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	freshNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: freshNotifications}
	select {
	case err := <-freshResult:
		if err != nil {
			t.Fatalf("fresh relay start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh relay did not become ready")
	}
	select {
	case <-launches:
	case <-time.After(time.Second):
		t.Fatal("fresh relay supervisor did not launch")
	}

	close(releaseLaunch)
	select {
	case err := <-oldResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("old initializer error=%v, want shared context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old initializer did not return after losing launch commitment")
	}
	select {
	case <-launches:
		t.Fatal("old initializer launched supervisor after stop won")
	default:
	}

	freshNotifications <- relayDeltaNotification(t, threadID, "fresh survives old launch")
}

func TestHubRelayInitialRegistrationFailureCancelsAndAllowsFreshStart(t *testing.T) {
	const threadID = "th_registration_initial"
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification),
		subscribed:    make(chan struct{}, 2),
		canceled:      make(chan struct{}, 2),
	}
	registrationCalls := make(chan bool, 2)
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(_ context.Context, _ string, replace bool) bool {
		registrationCalls <- replace
		return len(registrationCalls) > 1
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())

	err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("initial registration error=%v, want context canceled", err)
	}
	expectRelaySubscription(t, source.subscribed)
	select {
	case <-source.canceled:
	case <-time.After(time.Second):
		t.Fatal("failed initial registration did not cancel upstream relay")
	}

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("fresh relay after initial registration failure: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
}

func TestHubRelayExistingRegistrationFailureDoesNotReportAttachment(t *testing.T) {
	const threadID = "th_registration_existing"
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	registrationResults := make(chan bool, 2)
	registrationResults <- true
	registrationResults <- false
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.RegisterSubscription = func(context.Context, string, bool) bool {
		return <-registrationResults
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, cfg, appsource.NewRegistry())

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); err != nil {
		t.Fatalf("initial startRelay: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, source.thread); !errors.Is(err, context.Canceled) {
		t.Fatalf("existing registration error=%v, want context canceled", err)
	}
}

func TestHubRPCThreadReadReplaceSubscriptionDropsPreviousRelaySubscriber(t *testing.T) {
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        "th_a",
			SessionID: "th_a",
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:th_a", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        "th_b",
			SessionID: "th_b",
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:th_b", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(sourceA)
	web.sources.Add(sourceB)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:th_a", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("ThreadRead sourceA: %v", err)
	}
	expectRelaySubscription(t, sourceA.subscribed)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:th_b", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("ThreadRead sourceB: %v", err)
	}
	expectRelaySubscription(t, sourceB.subscribed)

	sourceA.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_a",
			Ref:      "codex-a:th_a",
			TurnID:   "turn_a",
			ItemID:   "item_a",
			Delta:    "from source a",
		}),
	}
	// Send sourceB notification immediately after sourceA. If the replaced
	// subscription leaked, sourceA's notification would arrive first; the
	// client must receive sourceB's as the first notification.
	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_b",
			Ref:      "codex-b:th_b",
			TurnID:   "turn_b",
			ItemID:   "item_b",
			Delta:    "from source b",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("notification method=%q", got.Method)
		}
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		if params.Ref != "codex-b:th_b" {
			t.Fatalf("client received notification for replaced subscription before sentinel: ref=%q", params.Ref)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active replacement subscription notification")
	}
	select {
	case <-sourceA.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("replaced relay subscriber did not retire the old source relay")
	}
}

func TestHubRPCThreadReadAdditiveSubscriptionsReceiveBothRelays(t *testing.T) {
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        "th_a",
			SessionID: "th_a",
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:th_a", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        "th_b",
			SessionID: "th_b",
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:th_b", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(sourceA)
	web.sources.Add(sourceB)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:th_a", Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead sourceA: %v", err)
	}
	expectRelaySubscription(t, sourceA.subscribed)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:th_b", Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead sourceB: %v", err)
	}
	expectRelaySubscription(t, sourceB.subscribed)

	sourceA.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_a",
			Ref:      "codex-a:th_a",
			TurnID:   "turn_a",
			ItemID:   "item_a",
			Delta:    "from source a",
		}),
	}
	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_b",
			Ref:      "codex-b:th_b",
			TurnID:   "turn_b",
			ItemID:   "item_b",
			Delta:    "from source b",
		}),
	}

	gotRefs := make(map[string]bool, 2)
	for len(gotRefs) < 2 {
		select {
		case got := <-client.Notifications():
			if got.Method != appwire.NotifyAgentMessageDelta {
				continue
			}
			var params appwire.AgentMessageDeltaParams
			if err := json.Unmarshal(got.Params, &params); err != nil {
				t.Fatalf("unmarshal params: %v", err)
			}
			gotRefs[params.Ref] = true
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for both relay notifications; got refs %v", gotRefs)
		}
	}
	if !gotRefs["codex-a:th_a"] || !gotRefs["codex-b:th_b"] {
		t.Fatalf("received refs %v, want both additive subscriptions", gotRefs)
	}
}

func TestHubSourceRegistryRoutesRunningSubagentThroughOwnerDaemon(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        ref.ThreadID,
			SessionID: ref.ThreadID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		PID:       1,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + httpServer.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "root",
		SessionID: "root",
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:              entry,
		SessionID:          "root",
		RunningSubagentIDs: []string{"child"},
		RunningJobs: []appwire.EvenerJobInfo{{
			JobID: "job_shell", JobType: "shell", Status: "running",
		}},
	})
	registry := newHubSourceRegistry(hubcore.WebConfig{Roster: roster})
	source, ok := registry.Source("local")
	if !ok {
		t.Fatal("local source missing")
	}
	listed, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("list root and running subagent: %v", err)
	}
	rootCarriedJob := false
	childListed := false
	for _, thread := range listed.Data {
		switch thread.ID {
		case "root":
			rootCarriedJob = thread.Evener.Diagnostics != nil && len(thread.Evener.Diagnostics.Jobs) == 1
		case "child":
			childListed = true
			if thread.Evener.Diagnostics != nil {
				t.Fatalf("child alias duplicated owner jobs: %+v", thread.Evener.Diagnostics.Jobs)
			}
		}
	}
	if !rootCarriedJob || !childListed {
		t.Fatalf("listed threads = %+v, want root job and job-free child alias", listed.Data)
	}
	read, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "local:child"})
	if err != nil {
		t.Fatalf("read running subagent through owner daemon: %v", err)
	}
	if read.Thread.ID != "child" || read.Thread.Evener.Ref != "local:child" {
		t.Fatalf("child read = %+v", read.Thread)
	}
	_, err = source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "local:child", ClientMutationID: "must-not-hit-root", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("child mutation error = %T %v, want session unavailable instead of owner mutation", err, err)
	}
	data, _ := wire.Data.(appwire.ErrorData)
	if data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("child mutation error = %T %v, want session unavailable instead of owner mutation", err, err)
	}
}

func TestHubRPCThreadReadRetiresRelayWhenClientDisconnects(t *testing.T) {
	source := &relayLifecycleSource{
		thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "th_1",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_1", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		canceled: make(chan struct{}),
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}

	select {
	case <-source.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("source relay context was not canceled after client disconnect")
	}
}

func TestHubRPCThreadReadKeepsRelayWhenSubscriberArrivesDuringIdleRetirement(t *testing.T) {
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "th_1",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_1", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)

	idleReached := make(chan struct{})
	releaseIdle := make(chan struct{})
	var idleOnce sync.Once
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.IdleExit = func(threadID string) {
		if threadID != "th_1" {
			return
		}
		idleOnce.Do(func() { close(idleReached) })
		<-releaseIdle
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}

	select {
	case <-idleReached:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not reach idle retirement window")
	}

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead client2: %v", err)
	}
	close(releaseIdle)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_1",
			Ref:      "codex:th_1",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "still live",
		}),
	}

	select {
	case got := <-client2.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-source.canceled:
		t.Fatal("relay was canceled despite a new subscriber")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification after idle-race subscriber")
	}
}

func TestHubRPCThreadReadSerializesRereadRegistrationAgainstIdleRetirement(t *testing.T) {
	const threadID = "th_join_idle"
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	reregistrationReached := make(chan struct{})
	releaseReregistration := make(chan struct{})
	idleReached := make(chan struct{})
	var registrationOnce sync.Once
	var idleOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.BeforeExistingRegistration = func(gotThreadID string) {
		if gotThreadID == threadID {
			registrationOnce.Do(func() {
				close(reregistrationReached)
				<-releaseReregistration
			})
		}
	}
	cfg.RelayHooks.IdleExit = func(gotThreadID string) {
		if gotThreadID == threadID {
			idleOnce.Do(func() { close(idleReached) })
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	rereadResult := make(chan error, 1)
	go func() {
		_, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		rereadResult <- err
	}()
	select {
	case <-reregistrationReached:
	case <-time.After(time.Second):
		t.Fatal("reread did not reach post-ready pre-registration boundary")
	}
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}
	select {
	case <-idleReached:
	case <-time.After(time.Second):
		t.Fatal("relay did not reach idle retirement while reread registration paused")
	}
	close(releaseReregistration)
	select {
	case err := <-rereadResult:
		if err != nil {
			t.Fatalf("reread ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reread registration did not complete")
	}

	source.notifications <- relayDeltaNotification(t, threadID, "joined before idle delete")
	expectRelayDelta(t, client2.Notifications(), "joined before idle delete")
	select {
	case <-source.canceled:
		t.Fatal("idle retirement canceled relay after serialized reread registration")
	default:
	}
}

func TestHubRPCThreadReadKeepsReplacementRelayTrackedAfterIdleCleanup(t *testing.T) {
	const threadID = "th_cleanup"
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)

	afterIdleDelete := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupDone := make(chan struct{})
	var idleOnce sync.Once
	var cleanupOnce sync.Once
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.AfterIdleDelete = func(threadID string) {
		if threadID != "th_cleanup" {
			return
		}
		idleOnce.Do(func() { close(afterIdleDelete) })
		<-releaseCleanup
		cleanupOnce.Do(func() { close(cleanupDone) })
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}

	select {
	case <-afterIdleDelete:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not reach post-delete cleanup window")
	}

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client2: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	close(releaseCleanup)
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("idle cleanup goroutine did not complete")
	}
	drainRelaySubscriptions(source.subscribed)

	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client2 again: %v", err)
	}
	select {
	case <-source.subscribed:
		t.Fatal("second read started a duplicate replacement relay")
	default:
	}
}

func TestHubRPCThreadReadPropagatesInFlightRelaySubscribeFailure(t *testing.T) {
	thread := appwire.Thread{
		ID:        "th_subscribe_fail",
		SessionID: "th_subscribe_fail",
		Source:    "codex",
		Evener:    appwire.EvenerThread{Ref: "codex:th_subscribe_fail", Capabilities: appwire.ThreadCapabilities{Send: true}},
	}
	source := &blockingFailingRelaySource{
		thread:  thread,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { waiterJoined <- struct{}{} }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	defer client1.Close()
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}

	readErrs := make(chan error, 2)
	go func() {
		_, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_subscribe_fail"})
		readErrs <- err
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("first relay subscribe did not start")
	}

	go func() {
		_, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_subscribe_fail"})
		readErrs <- err
	}()
	select {
	case <-waiterJoined:
	case <-time.After(time.Second):
		t.Fatal("concurrent read did not join in-flight relay subscribe")
	}
	close(source.release)

	for range 2 {
		select {
		case err := <-readErrs:
			if err == nil || !strings.Contains(err.Error(), "subscribe failed") {
				t.Fatalf("read error=%v, want subscribe failed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for relay subscribe error")
		}
	}
	if calls := source.subscribeCalls(); calls != 1 {
		t.Fatalf("subscribe calls=%d, want 1", calls)
	}
}

func TestHubRPCThreadReadSubscribeFailureDoesNotLeaveClientSubscribed(t *testing.T) {
	threadID := "th_retry_subscribe"
	source := &failFirstRelaySource{
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	failedClient := dialHubRPC(t, srv)
	defer failedClient.Close()
	if _, err := failedClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize failedClient: %v", err)
	}
	if _, err := failedClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err == nil || !strings.Contains(err.Error(), "subscribe failed once") {
		t.Fatalf("ThreadRead failedClient error=%v, want subscribe failed once", err)
	}

	okClient := dialHubRPC(t, srv)
	defer okClient.Close()
	if _, err := okClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize okClient: %v", err)
	}
	if _, err := okClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead okClient: %v", err)
	}
	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "after retry",
		}),
	}
	select {
	case got := <-okClient.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("okClient method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry relay notification")
	}
	// Send a sentinel and wait for okClient to receive it. After okClient
	// receives the sentinel the relay pipeline has fully flushed: any
	// notification that failedClient (if wrongly subscribed) would have
	// received from the first broadcast is already in its buffer, because
	// Broadcast enqueues to all subscribers synchronously before moving on.
	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			TurnID:   "turn_sentinel",
			ItemID:   "item_sentinel",
			Delta:    "sentinel",
		}),
	}
	select {
	case <-okClient.Notifications():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sentinel notification")
	}
	select {
	case got := <-failedClient.Notifications():
		t.Fatalf("failed client received stale relay notification: %+v", got)
	default:
	}
}

func TestHubThreadListKeepsLocalPastWhenNonLocalLiveIDCollides(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-local-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()
	sources.Add(&relayBroadcastSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "codex",
			Preview:   "live codex thread",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: "codex:" + sessionID},
		},
	})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: past}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	var foundLocalPast, foundCodexLive bool
	for _, thread := range resp.Data {
		switch thread.Evener.Ref {
		case "local:" + sessionID:
			foundLocalPast = true
		case "codex:" + sessionID:
			foundCodexLive = true
		}
	}
	if !foundLocalPast || !foundCodexLive {
		t.Fatalf("found local past=%v codex live=%v threads=%+v", foundLocalPast, foundCodexLive, resp.Data)
	}
}

func TestHubThreadListMatchesCodexNativeStatusFilters(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex", thread: appwire.Thread{
		ID:        "th_codex",
		SessionID: "th_codex",
		Source:    "codex",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener:    appwire.EvenerThread{Ref: "codex:th_codex"},
	}})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{
		Statuses: []string{"active"},
	})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "codex:th_codex" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

type relayLifecycleSource struct {
	thread   appwire.Thread
	canceled chan struct{}
}

type pastFallbackRelaySource struct {
	relayLifecycleSource
	readErr error
}

func (s *pastFallbackRelaySource) ID() string {
	return "local"
}

func (s *pastFallbackRelaySource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, s.readErr
}

type relaySubscribeResult struct {
	notifications <-chan appwire.Notification
	err           error
}

type scriptedRelaySource struct {
	relayLifecycleSource
	id             string
	results        <-chan relaySubscribeResult
	subscribeCalls chan<- struct{}
}

type blockingRecoveryRelaySource struct {
	relayLifecycleSource
	mu                 sync.Mutex
	calls              int
	established        <-chan appwire.Notification
	recoveryStarted    chan struct{}
	recoveryCanceled   chan struct{}
	recoveryReturned   chan struct{}
	replacementStarted chan struct{}
	replacementResults <-chan relaySubscribeResult
}

type successfulAfterCancelRelaySource struct {
	relayLifecycleSource
	mu                 sync.Mutex
	calls              int
	initialStarted     chan struct{}
	initialCanceled    chan struct{}
	releaseInitial     chan struct{}
	initialReturned    chan struct{}
	oldNotifications   <-chan appwire.Notification
	freshStarted       chan struct{}
	freshNotifications <-chan appwire.Notification
}

type initialRequestCancelRelaySource struct {
	relayLifecycleSource
	mu                 sync.Mutex
	calls              int
	initialStarted     chan struct{}
	initialCanceled    chan struct{}
	initialRelease     chan struct{}
	initialReturned    chan struct{}
	freshStarted       chan struct{}
	freshNotifications <-chan appwire.Notification
}

func (s *initialRequestCancelRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.initialStarted)
		<-ctx.Done()
		close(s.initialCanceled)
		<-s.initialRelease
		close(s.initialReturned)
		return nil, ctx.Err()
	}
	close(s.freshStarted)
	return s.freshNotifications, nil
}

func (s *successfulAfterCancelRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.initialStarted)
		go func() {
			<-ctx.Done()
			close(s.initialCanceled)
		}()
		<-s.releaseInitial
		close(s.initialReturned)
		return s.oldNotifications, nil
	}
	close(s.freshStarted)
	return s.freshNotifications, nil
}

func (s *blockingRecoveryRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	switch call {
	case 1:
		return s.established, nil
	case 2:
		close(s.recoveryStarted)
		<-ctx.Done()
		close(s.recoveryCanceled)
		close(s.recoveryReturned)
		return nil, ctx.Err()
	default:
		if call == 3 {
			close(s.replacementStarted)
		}
		select {
		case result := <-s.replacementResults:
			return result.notifications, result.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *scriptedRelaySource) ID() string {
	if s.id != "" {
		return s.id
	}
	return s.relayLifecycleSource.ID()
}

type relayRetryWait struct {
	delay    time.Duration
	release  chan struct{}
	canceled chan struct{}
}

type scriptedRelayRetryClock struct {
	waits chan relayRetryWait
}

func newScriptedRelayRetryClock() *scriptedRelayRetryClock {
	return &scriptedRelayRetryClock{waits: make(chan relayRetryWait)}
}

func (c *scriptedRelayRetryClock) Wait(ctx context.Context, delay time.Duration) error {
	wait := relayRetryWait{delay: delay, release: make(chan struct{}), canceled: make(chan struct{})}
	select {
	case c.waits <- wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-wait.release:
		return nil
	case <-ctx.Done():
		close(wait.canceled)
		return ctx.Err()
	}
}

func (c *scriptedRelayRetryClock) nextWait(t *testing.T) relayRetryWait {
	t.Helper()
	select {
	case wait := <-c.waits:
		return wait
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay retry delay")
		return relayRetryWait{}
	}
}

func (c *scriptedRelayRetryClock) releaseWait(t *testing.T, want time.Duration) {
	t.Helper()
	wait := c.nextWait(t)
	if wait.delay != want {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, want)
	}
	close(wait.release)
}

func (c *scriptedRelayRetryClock) expectWait(t *testing.T, want time.Duration) {
	t.Helper()
	wait := c.nextWait(t)
	if wait.delay != want {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, want)
	}
}

func (s *scriptedRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	select {
	case s.subscribeCalls <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case result := <-s.results:
		return result.notifications, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func awaitRelaySubscribeCall(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay subscribe call")
	}
}

func relayDeltaNotification(t *testing.T, threadID, delta string) appwire.Notification {
	t.Helper()
	return appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    delta,
		}),
	}
}

func expectRelayResync(t *testing.T, notifications <-chan appwire.Notification, wantThreadID, wantRef string) {
	t.Helper()
	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyEvenerThreadResync {
			t.Fatalf("recovery notification method=%q, want %q", got.Method, appwire.NotifyEvenerThreadResync)
		}
		var params appwire.ThreadResyncParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal thread resync: %v", err)
		}
		want := appwire.ThreadResyncParams{ThreadID: wantThreadID, Ref: wantRef}
		if params != want {
			t.Fatalf("thread resync params=%+v, want %+v", params, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thread resync after relay recovery")
	}
}

func expectRelayDelta(t *testing.T, notifications <-chan appwire.Notification, want string) {
	t.Helper()
	select {
	case got := <-notifications:
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal relay notification: %v", err)
		}
		if params.Delta != want {
			t.Fatalf("relay delta=%q, want %q", params.Delta, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for relay delta %q", want)
	}
}

type listThreadSource struct {
	relayLifecycleSource
	id      string
	thread  appwire.Thread
	listErr error
}

func (s *listThreadSource) ID() string { return s.id }

func (s *listThreadSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}

func (s *listThreadSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.listErr != nil {
		return appwire.ThreadListResponse{}, s.listErr
	}
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayLifecycleSource) ID() string { return "codex" }

func (s *relayLifecycleSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}

func (s *relayLifecycleSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayLifecycleSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *relayLifecycleSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, appwire.Unavailable("relay lifecycle source does not start threads")
}

func (s *relayLifecycleSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, appwire.Unavailable("relay lifecycle source does not resume threads")
}

func (s *relayLifecycleSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, appwire.Unavailable("relay lifecycle source does not fork threads")
}

func (s *relayLifecycleSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{}, appwire.Unavailable("relay lifecycle source does not start turns")
}

func (s *relayLifecycleSource) SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	return appwire.TurnSteerResponse{}, appwire.Unavailable("relay lifecycle source does not steer turns")
}

func (s *relayLifecycleSource) ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error {
	return appwire.Unavailable("relay lifecycle source does not resolve escalations")
}

func (s *relayLifecycleSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	return appwire.TurnInterruptResponse{}, appwire.Unavailable("relay lifecycle source does not interrupt turns")
}

func (s *relayLifecycleSource) QueueTurn(context.Context, appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	return appwire.TurnQueueResponse{}, appwire.Unavailable("relay lifecycle source does not queue turns")
}

func (s *relayLifecycleSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	return appwire.TurnDrainAsSteerResponse{}, appwire.Unavailable("relay lifecycle source does not drain as steer")
}

func (s *relayLifecycleSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.Unavailable("relay lifecycle source does not promote queued messages")
}

func (s *relayLifecycleSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return appwire.TurnCancelQueuedResponse{}, appwire.Unavailable("relay lifecycle source does not cancel queued messages")
}

func (s *relayLifecycleSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return appwire.Unavailable("relay lifecycle source does not compact threads")
}

func (s *relayLifecycleSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return appwire.Unavailable("relay lifecycle source does not shut down threads")
}

func (s *relayLifecycleSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set models")
}

func (s *relayLifecycleSource) SetThreadVisionModel(context.Context, appwire.ThreadVisionModelSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set vision models")
}

func (s *relayLifecycleSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set names")
}

func (s *relayLifecycleSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set reasoning effort")
}

func (s *relayLifecycleSource) GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return appwire.GoalSetResponse{}, appwire.Unavailable("relay lifecycle source does not set goals")
}

func (s *relayLifecycleSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("relay lifecycle source does not clear threads")
}

func (s *relayLifecycleSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return appwire.ModelListResponse{}, appwire.Unavailable("relay lifecycle source does not list models")
}

func (s *relayLifecycleSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, appwire.Unavailable("relay lifecycle source does not list tasks")
}

func (s *relayLifecycleSource) ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return appwire.JobsListResponse{}, appwire.Unavailable("relay lifecycle source does not list jobs")
}

func (s *relayLifecycleSource) JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return appwire.JobsOutputResponse{}, appwire.Unavailable("relay lifecycle source does not read job output")
}

func (s *relayLifecycleSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	out := make(chan appwire.Notification)
	go func() {
		defer close(out)
		<-ctx.Done()
		close(s.canceled)
	}()
	return out, nil
}

type relayBroadcastSource struct {
	relayLifecycleSource
	id            string
	thread        appwire.Thread
	notifications chan appwire.Notification
	subscribed    chan struct{}
	canceled      chan struct{}
}

type readRelayDisabledSource struct {
	relayBroadcastSource
}

func (s *readRelayDisabledSource) RelayOnThreadRead() bool {
	return false
}

type blockingFailingRelaySource struct {
	relayLifecycleSource
	mu      sync.Mutex
	calls   int
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

type failFirstRelaySource struct {
	relayBroadcastSource
	mu    sync.Mutex
	calls int
}

type resumeAfterSubscribeUnavailableSource struct {
	relayBroadcastSource
	mu          sync.Mutex
	calls       int
	startPrompt string
}

func inputTextForTest(input []appwire.InputItem) string {
	for _, item := range input {
		if item.Text != "" {
			return item.Text
		}
	}
	return ""
}

func (s *resumeAfterSubscribeUnavailableSource) ID() string { return "local" }

func (s *resumeAfterSubscribeUnavailableSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if calls == 1 {
		return nil, appwire.SessionUnavailable("stale relay subscription")
	}
	return s.relayBroadcastSource.SubscribeThread(ctx, params)
}

func (s *resumeAfterSubscribeUnavailableSource) StartTurn(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startPrompt = inputTextForTest(params.Input)
	s.mu.Unlock()
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_resumed"}}, nil
}

func (s *resumeAfterSubscribeUnavailableSource) subscribeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *resumeAfterSubscribeUnavailableSource) lastStartPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startPrompt
}

func (s *failFirstRelaySource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if calls == 1 {
		return nil, errors.New("subscribe failed once")
	}
	return s.relayBroadcastSource.SubscribeThread(ctx, params)
}

func (s *blockingFailingRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return nil, errors.New("subscribe failed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *blockingFailingRelaySource) subscribeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *relayBroadcastSource) ID() string {
	if s.id != "" {
		return s.id
	}
	return "codex"
}

func (s *relayBroadcastSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}

func (s *relayBroadcastSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayBroadcastSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *relayBroadcastSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	out := make(chan appwire.Notification, 4)
	if s.subscribed != nil {
		s.subscribed <- struct{}{}
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				select {
				case s.canceled <- struct{}{}:
				default:
				}
				return
			case notification := <-s.notifications:
				select {
				case out <- notification:
				case <-ctx.Done():
					select {
					case s.canceled <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()
	return out, nil
}

type startResumeRelaySource struct {
	relayBroadcastSource
}

func (s *startResumeRelaySource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{
		Thread: s.thread,
		Turn:   appwire.Turn{ID: "turn_start"},
	}, nil
}

func (s *startResumeRelaySource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

type startRelayFailureSource struct {
	startResumeRelaySource
}

func (s *startRelayFailureSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	return nil, errors.New("subscribe failed after start")
}

type forkingRelaySource struct {
	relayBroadcastSource
	forkCalled bool
	forkParams appwire.ThreadForkParams
	response   appwire.ThreadForkResponse
}

func (s *forkingRelaySource) ForkThread(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	s.forkCalled = true
	s.forkParams = params
	return s.response, nil
}

func expectRelaySubscription(t *testing.T, subscribed <-chan struct{}) {
	t.Helper()
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay subscription")
	}
}

func drainRelaySubscriptions(subscribed <-chan struct{}) {
	for {
		select {
		case <-subscribed:
		default:
			return
		}
	}
}

func TestHubRPCThreadActionsRouteToDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	compactCalled := false
	shutdownCalled := false
	modelCalled := ""
	goalObjective := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("read ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref: "local:th_1",
				Capabilities: appwire.ThreadCapabilities{
					Send:         true,
					Steer:        true,
					Interrupt:    true,
					Compact:      true,
					Clear:        true,
					ForkFromTurn: true,
					Shutdown:     true,
					ChangeModel:  true,
					Goal:         true,
				},
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("compact ref=%q", params.Ref)
		}
		compactCalled = true
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("model ref=%q", params.Ref)
		}
		modelCalled = params.ModelProvider + "/" + params.Model
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadShutdown, func(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("shutdown ref=%q", params.Ref)
		}
		shutdownCalled = true
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("goal ref=%q", params.Ref)
		}
		goalObjective = params.Objective
		return appwire.GoalSetResponse{Started: true}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       104,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadCompactStart: %v", err)
	}
	if !compactCalled {
		t.Fatal("compact was not routed")
	}
	if err := client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Ref:           "local:th_1",
		ModelProvider: "openai",
		Model:         "gpt-5",
	}); err != nil {
		t.Fatalf("ThreadModelSet: %v", err)
	}
	if modelCalled != "openai/gpt-5" {
		t.Fatalf("modelCalled=%q", modelCalled)
	}
	goalResp, err := client.GoalSet(context.Background(), appwire.GoalSetParams{
		Ref:       "local:th_1",
		Objective: "improve coverage",
	})
	if err != nil {
		t.Fatalf("GoalSet: %v", err)
	}
	if goalObjective != "improve coverage" {
		t.Fatalf("goalObjective=%q", goalObjective)
	}
	if !goalResp.Started {
		t.Fatal("GoalSet response Started not propagated")
	}
	if err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadShutdown: %v", err)
	}
	if !shutdownCalled {
		t.Fatal("shutdown was not routed")
	}
}

func TestHubRPCTurnMutationsForwardWithoutDynamicCapabilityGates(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{},
			},
		}}, nil
	})
	called := make(map[string]int)
	receipt := func(method, mutationID string) appwire.MutationReceipt {
		called[method]++
		return appwire.MutationReceipt{
			ClientMutationID: mutationID,
			Disposition:      appwire.MutationDispositionReplayed,
			ThreadID:         "th_1",
			TurnID:           "turn_1",
			QueueEntryIDs:    []string{"queue_1"},
			ProjectionState:  appwire.MutationProjectionReflected,
		}
	}
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}, Receipt: receipt(appwire.MethodTurnStart, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		return appwire.TurnSteerResponse{Receipt: receipt(appwire.MethodTurnSteer, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnInterrupt, func(_ context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		return appwire.TurnInterruptResponse{Receipt: receipt(appwire.MethodTurnInterrupt, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		if inputTextForTest(params.Input) == "reject" {
			return appwire.TurnQueueResponse{}, appwire.WireError{
				Code:    appwire.CodeConflict,
				Message: "turn changed",
				Data: appwire.ErrorData{
					EvenerErrorInfo:  appwire.ErrorConflict,
					ClientMutationID: params.ClientMutationID,
					MutationOutcome:  appwire.MutationOutcomeNotAccepted,
					RetryDisposition: appwire.RetryDispositionNone,
				},
			}
		}
		return appwire.TurnQueueResponse{Receipt: receipt(appwire.MethodTurnQueue, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		return appwire.TurnDrainAsSteerResponse{Receipt: receipt(appwire.MethodTurnDrainAsSteer, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnPromoteQueuedAsSteer, func(_ context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
		return appwire.TurnPromoteQueuedAsSteerResponse{Receipt: receipt(appwire.MethodTurnPromoteQueuedAsSteer, params.ClientMutationID)}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnCancelQueued, func(_ context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
		return appwire.TurnCancelQueuedResponse{RemovedText: "queued", RemovedImages: 1, Receipt: receipt(appwire.MethodTurnCancelQueued, params.ClientMutationID)}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       os.Getpid(),
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tests := []struct {
		name   string
		method string
		id     string
		params any
		result any
	}{
		{"start after response loss", appwire.MethodTurnStart, "mutation-start", appwire.TurnStartParams{Ref: "local:th_1", ClientMutationID: "mutation-start", ExpectedInstanceID: "sess_1", Input: []appwire.InputItem{{Type: "text", Text: "start"}}}, &appwire.TurnStartResponse{}},
		{"steer", appwire.MethodTurnSteer, "mutation-steer", appwire.TurnSteerParams{Ref: "local:th_1", ClientMutationID: "mutation-steer", ExpectedInstanceID: "sess_1", Input: []appwire.InputItem{{Type: "text", Text: "steer"}}}, &appwire.TurnSteerResponse{}},
		{"interrupt", appwire.MethodTurnInterrupt, "mutation-interrupt", appwire.TurnInterruptParams{Ref: "local:th_1", ClientMutationID: "mutation-interrupt", ExpectedInstanceID: "sess_1"}, &appwire.TurnInterruptResponse{}},
		{"queue", appwire.MethodTurnQueue, "mutation-queue", appwire.TurnQueueParams{Ref: "local:th_1", ClientMutationID: "mutation-queue", ExpectedInstanceID: "sess_1", Input: []appwire.InputItem{{Type: "text", Text: "queue"}}}, &appwire.TurnQueueResponse{}},
		{"drain", appwire.MethodTurnDrainAsSteer, "mutation-drain", appwire.TurnDrainAsSteerParams{Ref: "local:th_1", ClientMutationID: "mutation-drain", ExpectedInstanceID: "sess_1", ExpectedQueueRevision: 1}, &appwire.TurnDrainAsSteerResponse{}},
		{"promote", appwire.MethodTurnPromoteQueuedAsSteer, "mutation-promote", appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", ClientMutationID: "mutation-promote", ExpectedInstanceID: "sess_1", ExpectedEntryID: "queue_1", Index: 0}, &appwire.TurnPromoteQueuedAsSteerResponse{}},
		{"cancel", appwire.MethodTurnCancelQueued, "mutation-cancel", appwire.TurnCancelQueuedParams{Ref: "local:th_1", ClientMutationID: "mutation-cancel", ExpectedInstanceID: "sess_1", ExpectedEntryID: "queue_1", Index: 0}, &appwire.TurnCancelQueuedResponse{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := client.Request(context.Background(), tc.method, tc.params, tc.result); err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}
			if called[tc.method] != 1 {
				t.Fatalf("%s daemon calls = %d, want 1", tc.method, called[tc.method])
			}
			var got appwire.MutationReceipt
			switch response := tc.result.(type) {
			case *appwire.TurnStartResponse:
				got = response.Receipt
			case *appwire.TurnSteerResponse:
				got = response.Receipt
			case *appwire.TurnInterruptResponse:
				got = response.Receipt
			case *appwire.TurnQueueResponse:
				got = response.Receipt
			case *appwire.TurnDrainAsSteerResponse:
				got = response.Receipt
			case *appwire.TurnPromoteQueuedAsSteerResponse:
				got = response.Receipt
			case *appwire.TurnCancelQueuedResponse:
				got = response.Receipt
				if response.RemovedText != "queued" || response.RemovedImages != 1 {
					t.Fatalf("cancel response = %+v", response)
				}
			default:
				t.Fatalf("unexpected response type %T", tc.result)
			}
			if got.ClientMutationID != tc.id ||
				got.Disposition != appwire.MutationDispositionReplayed ||
				got.TurnID != "turn_1" ||
				!slices.Equal(got.QueueEntryIDs, []string{"queue_1"}) ||
				got.ProjectionState != appwire.MutationProjectionReflected {
				t.Fatalf("receipt = %+v", got)
			}
		})
	}

	rejected := appwire.TurnQueueParams{
		Ref:                "local:th_1",
		ClientMutationID:   "mutation-reject",
		ExpectedInstanceID: "sess_1",
		Input:              []appwire.InputItem{{Type: "text", Text: "reject"}},
	}
	var response appwire.TurnQueueResponse
	err := client.Request(context.Background(), appwire.MethodTurnQueue, rejected, &response)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("TurnQueue rejection %T=%v, want WireError", err, err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok ||
		data["clientMutationId"] != rejected.ClientMutationID ||
		data["mutationOutcome"] != string(appwire.MutationOutcomeNotAccepted) ||
		data["retryDisposition"] != string(appwire.RetryDispositionNone) {
		t.Fatalf("wire=%+v data=%T %#v", wire, wire.Data, wire.Data)
	}
}

func TestHubRPCTurnSteerMissingLocalSessionReturnsTerminalRejection(t *testing.T) {
	runDir := t.TempDir()
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	params := appwire.TurnSteerParams{
		Ref:                "local:missing",
		ClientMutationID:   "mutation-missing-session",
		ExpectedInstanceID: "missing",
		Input:              []appwire.InputItem{{Type: "text", Text: "steer"}},
	}
	var response appwire.TurnSteerResponse
	err := client.Request(context.Background(), appwire.MethodTurnSteer, params, &response)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("TurnSteer error %T=%v, want WireError", err, err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok ||
		data["evenerErrorInfo"] != string(appwire.ErrorSessionUnavailable) ||
		data["clientMutationId"] != params.ClientMutationID ||
		data["mutationOutcome"] != string(appwire.MutationOutcomeNotAccepted) ||
		data["retryDisposition"] != string(appwire.RetryDispositionNone) {
		t.Fatalf("wire=%+v data=%T %#v", wire, wire.Data, wire.Data)
	}
}

func TestHubRPCThreadCompactStartResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{Compact: true},
			},
		}}, nil
	})
	compactCalled := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("compact ref=%q", params.Ref)
		}
		compactCalled = true
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	var gotReq hubcore.ResumeRequest
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			gotReq = req
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        106,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "local:" + sessionID}); err != nil {
		t.Fatalf("ThreadCompactStart: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if gotReq.SessionID != sessionID || gotReq.StateDir != stateDir || gotReq.WorkingDir != workingDir {
		t.Fatalf("resume request=%+v", gotReq)
	}
	if !compactCalled {
		t.Fatal("compact was not routed after resume")
	}
}

func TestHubRPCThreadModelSetResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{ChangeModel: true},
			},
		}}, nil
	})
	modelCalled := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("model ref=%q", params.Ref)
		}
		modelCalled = params.ModelProvider + "/" + params.Model
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID || req.StateDir != stateDir || req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        106,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Ref:           "local:" + sessionID,
		ModelProvider: "openai",
		Model:         "gpt-5.6-sol",
	}); err != nil {
		t.Fatalf("ThreadModelSet: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if modelCalled != "openai/gpt-5.6-sol" {
		t.Fatalf("modelCalled=%q", modelCalled)
	}
}

func TestHubRPCThreadVisionModelSetResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{ChangeVisionModel: true},
			},
		}}, nil
	})
	visionModelCalled := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadVisionModelSet, func(_ context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("vision model ref=%q", params.Ref)
		}
		visionModelCalled = params.VisionModel
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID || req.StateDir != stateDir || req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        106,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{
		Ref:         "local:" + sessionID,
		VisionModel: "off",
	}); err != nil {
		t.Fatalf("ThreadVisionModelSet: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if visionModelCalled != "off" {
		t.Fatalf("visionModelCalled=%q", visionModelCalled)
	}
}

func TestHubRPCUnsupportedThreadActionReturnsStructuredUnavailable(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	shutdownCalled := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_1" {
			t.Fatalf("read ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref: "local:th_1",
				Capabilities: appwire.ThreadCapabilities{
					Send:    true,
					Compact: true,
				},
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadShutdown, func(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		shutdownCalled = true
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       105,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "local:th_1"})
	if err == nil {
		t.Fatal("ThreadShutdown succeeded for unsupported action")
	}
	if shutdownCalled {
		t.Fatal("unsupported shutdown reached source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["evenerErrorInfo"] != string(appwire.ErrorActionUnavailable) {
		t.Fatalf("wire data=%#v", wire.Data)
	}
}

// TestHubRPCGoalSetGatedByCapability pins /par A6: goal/set is pre-flight gated
// like every sibling thread action. A source whose ThreadRead reports caps without
// Goal (e.g. codex) must have goal/set rejected with a structured Unavailable
// BEFORE the call reaches the source's GoalSet.
func TestHubRPCGoalSetGatedByCapability(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	goalReached := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
				Ref:          "local:th_1",
				Capabilities: appwire.ThreadCapabilities{Send: true}, // no Goal
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodGoalSet, func(_ context.Context, _ appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		goalReached = true
		return appwire.GoalSetResponse{Started: true}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       106,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.GoalSet(context.Background(), appwire.GoalSetParams{Ref: "local:th_1", Objective: "do it"})
	if err == nil {
		t.Fatal("GoalSet succeeded despite missing Goal capability")
	}
	if goalReached {
		t.Fatal("gated goal/set still reached the source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

func TestHubRPCModelListUsesEvenerLaunchContractWhenDaemonFails(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, appwire.InternalError("provider unavailable")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       104,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "th_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	spawner := &fakeRPCSpawner{
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Roster:  roster,
		Spawner: spawner,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListFallsBackToLocalDaemonWithoutLaunchContract(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       105,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "th_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-daemon" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListPrefersEvenerLaunchContract(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-stale"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       105,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "th_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	spawner := &fakeRPCSpawner{
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListUsesWorkingDirForEvenerLaunchContract(t *testing.T) {
	spawner := &fakeRPCWorkingDirModelContractSpawner{
		fallback: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "stale", Model: "wrong"}},
		},
		contractForWorkingDir: func(_ context.Context, cwd string) (appwire.ModelListResponse, error) {
			if cwd != "/tmp/project-with-oauth" {
				return appwire.ModelListResponse{}, fmt.Errorf("cwd=%q, want /tmp/project-with-oauth", cwd)
			}
			return appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-visible-to-agent"}},
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Spawner: spawner})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: "/tmp/project-with-oauth"})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-visible-to-agent" {
		t.Fatalf("models=%+v", resp.Data)
	}
}

func TestHubRPCModelListDoesNotUseLocalDaemonWhenLaunchContractIsEmpty(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       105,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "th_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Roster:  roster,
		Spawner: &fakeRPCModelContractSpawner{contract: appwire.ModelListResponse{}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCModelListDoesNotUseLocalDaemonWhenLaunchContractHasOnlyDiagnostics(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-daemon"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       105,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "th_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()

	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("models=%+v", resp.Data)
	}
	if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Provider != "openai" || !strings.Contains(resp.Diagnostics[0].Message, "403") {
		t.Fatalf("diagnostics=%+v", resp.Diagnostics)
	}
}

func TestHubRPCModelListReportsEvenerLaunchDiagnostics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
	  printf '{"protocol":"evener-appwire-v4","models":[{"provider":"ollama","model":"local"}],"diagnostics":[{"provider":"openai","source":"provider","title":"Provider error","message":"HTTP 403"}]}\n'
  exit 0
fi
exit 2
`
	writeFakeEvener(t, bin, script)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  t.TempDir(),
		Spawner: &HubSpawner{Cfg: DefaultConfig(), EvenerBinary: bin, RunDir: t.TempDir(), HubToken: "generated-token"},
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp struct {
		Data []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"data"`
		Diagnostics []struct {
			Provider string `json:"provider"`
			Source   string `json:"source"`
			Title    string `json:"title"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := client.Request(context.Background(), appwire.MethodModelList, appwire.ModelListParams{}, &resp); err != nil {
		t.Fatalf("model/list: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Provider != "ollama" || resp.Data[0].Model != "local" {
		t.Fatalf("models=%+v", resp.Data)
	}
	if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Provider != "openai" || resp.Diagnostics[0].Source != "provider" || !strings.Contains(resp.Diagnostics[0].Message, "403") {
		t.Fatalf("diagnostics=%+v", resp.Diagnostics)
	}
}

func TestHubRPCThreadStartKeepsProviderForModelIDsWithSlashes(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       106,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_slash_model",
				SessionID: "sess_slash_model",
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openrouter",
		Model:         "deepseek/deepseek-v4-flash",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openrouter/deepseek/deepseek-v4-flash" {
		t.Fatalf("spawn model=%q, want openrouter/deepseek/deepseek-v4-flash", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartDeliversPromptWhenFirstRosterProbeFails(t *testing.T) {
	for _, fault := range []string{"probe", "listing"} {
		t.Run(fault, func(t *testing.T) {
			const sessionID = "033snFBSHFr78ZbQQMAeBD"
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID:        sessionID,
					SessionID: sessionID,
					Source:    "local",
					Evener: appwire.EvenerThread{
						Ref:          params.Ref,
						Capabilities: appwire.ThreadCapabilities{Send: true},
					},
				}}, nil
			})
			var gotPrompt string
			var turns int
			appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
				gotPrompt = inputTextForTest(params.Input)
				turns++
				return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
			})
			daemonHTTP := httptest.NewUnstartedServer(http.HandlerFunc(daemon.ServeWebSocket))
			dropper := &dropFirstConnectionListener{
				Listener: daemonHTTP.Listener,
				dropped:  make(chan struct{}),
			}
			daemonHTTP.Listener = dropper
			daemonHTTP.Start()
			defer daemonHTTP.Close()

			runDir := t.TempDir()
			entry := rendezvous.Entry{
				PID:       os.Getpid(),
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}
			var spawns int
			spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				spawns++
				writeRendezvous(t, runDir, entry)
				if fault == "listing" {
					if err := os.WriteFile(filepath.Join(runDir, "1.json"), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return entry, nil
			}}
			roster := hubcore.NewRoster(runDir, failedRPCProber{})
			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:  runDir,
				Roster:  roster,
				Spawner: spawner,
				Past:    hubcore.NewPastIndex(""),
			})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()

			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
				Model: "openai/gpt-5",
				CWD:   "/tmp",
				Input: []appwire.InputItem{{Type: "text", Text: "review the open PRs"}},
			})
			if err != nil {
				t.Fatalf("ThreadStart: %v", err)
			}
			select {
			case <-dropper.dropped:
			default:
				t.Fatal("startup test did not drop the first daemon connection")
			}
			if gotPrompt != "review the open PRs" {
				t.Fatalf("prompt=%q, want review the open PRs", gotPrompt)
			}
			if resp.Thread.Evener.Ref != "local:"+sessionID || resp.Turn.ID != "turn_1" {
				t.Fatalf("response=%+v", resp)
			}

			if spawns != 1 || turns != 1 {
				t.Fatalf("spawns=%d turns=%d", spawns, turns)
			}
		})
	}
}

func TestHubRPCThreadStartPassesExplicitNonInteractive(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       107,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_noninteractive",
				SessionID: "sess_noninteractive",
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	nonInteractive := true
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model:          "openai/gpt-5.2",
		CWD:            "/tmp",
		NonInteractive: &nonInteractive,
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.NonInteractive == nil || !*got.Resolved.Effective.NonInteractive {
		t.Fatalf("spawn non_interactive = %v, want true", got.Resolved.Effective.NonInteractive)
	}
}

func TestHubRPCThreadStartPassesNonInteractiveLaunchOverride(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       108,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_noninteractive_override",
				SessionID: "sess_noninteractive_override",
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	nonInteractive := true
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model: "openai/gpt-5.2",
		CWD:   "/tmp",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			NonInteractive: &nonInteractive,
		},
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.NonInteractive == nil || !*got.Resolved.Effective.NonInteractive {
		t.Fatalf("spawn non_interactive = %v, want true", got.Resolved.Effective.NonInteractive)
	}
}

func TestHubRPCThreadStartPropagatesSpawnerStderrAsHubLaunchError(t *testing.T) {
	runDir := t.TempDir()
	spawnErr := strings.Join([]string{
		"daemon spawn failed",
		"process exited before rendezvous",
		"exit status 1",
		`evener serve: session creation: plugin initialization: resolving plugin dir "/Users/jesse/git/superpowers/superpowers": lstat /Users: no such file or directory`,
	}, ": ")
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{}, errors.New(spawnErr)
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "plugin initialization: resolving plugin dir") {
		t.Fatalf("error did not include daemon stderr: %v", err)
	}
}

func TestValidateLaunchPathRejectsMissingPluginDir(t *testing.T) {
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: filepath.Join(t.TempDir(), "missing"), Kind: "dir"})
	if resp.Valid {
		t.Fatalf("valid=%v, want false", resp.Valid)
	}
	if !strings.Contains(resp.Error, "no such file") {
		t.Fatalf("error=%q", resp.Error)
	}
}

func TestValidateLaunchPathAcceptsExecutableCommand(t *testing.T) {
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: "sh", Kind: "command"})
	if !resp.Valid {
		t.Fatalf("valid=false error=%q", resp.Error)
	}
	if resp.Path == "" || !filepath.IsAbs(resp.Path) {
		t.Fatalf("resolved command path=%q, want absolute", resp.Path)
	}
}

func TestValidateLaunchPathAcceptsMissingOutputFileWithWritableParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: path, Kind: "output-file"})
	if !resp.Valid {
		t.Fatalf("valid=false error=%q", resp.Error)
	}
	if resp.Path != path {
		t.Fatalf("path=%q, want %q", resp.Path, path)
	}
}

func TestValidateLaunchPathRejectsOutputFileWithMissingParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "trace.jsonl")
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: path, Kind: "output-file"})
	if resp.Valid {
		t.Fatalf("valid=true, want false")
	}
	if !strings.Contains(resp.Error, "no such file") {
		t.Fatalf("error=%q", resp.Error)
	}
}

func TestHubRPCThreadStartRejectsModelOutsideEvenerLaunchContractBeforeSpawn(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			spawnCalled = true
			return rendezvous.Entry{}, nil
		},
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Spawner: spawner,
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-stale",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if spawnCalled {
		t.Fatal("spawn was called for a model outside the Evener launch contract")
	}
}

func TestHubRPCThreadStartAllowsModelWhenProviderDoesNotEnumerateLaunchModels(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{
			PID:       107,
			Protocol:  appwire.ProtocolVersion,
			SourceID:  "local",
			ThreadID:  "th_non_enumerable_model",
			SessionID: "sess_non_enumerable_model",
		}, nil
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5.5",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5.5" {
		t.Fatalf("spawn model=%q, want openai/gpt-5.5", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartAllowsModelWhenProviderHasLaunchDiagnostic(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{
			PID:       108,
			Protocol:  appwire.ProtocolVersion,
			SourceID:  "local",
			ThreadID:  "th_degraded_model",
			SessionID: "sess_degraded_model",
		}, nil
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5.5",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5.5" {
		t.Fatalf("spawn model=%q, want openai/gpt-5.5", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartRejectsProviderMissingFromDegradedLaunchContract(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		spawnCalled = true
		return rendezvous.Entry{}, nil
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "anthropic",
		Model:         "claude-test",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "not reported by the Evener launch harness") {
		t.Fatalf("error=%v", err)
	}
	if spawnCalled {
		t.Fatal("spawn was called for provider missing from degraded launch contract")
	}
}

// TestHubRPCThreadStartAllowsIntentionallySkippedLaunchProvider: a provider
// the launch contract never enumerated is still launchable when the registry
// holds the instance (spec §11.3) — an endpoint that lists no models is
// configured, not broken.
func TestHubRPCThreadStartAllowsIntentionallySkippedLaunchProvider(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{PID: 301, ThreadID: "th_orclaude", SessionID: "th_orclaude"}, nil
	}
	reg := newSpawnGateRegistry(t, t.TempDir(), map[string]string{"OPENROUTER_API_KEY": "k"}, map[string]registry.Provider{
		"orclaude": {Base: "openrouter", Protocol: registry.ProtocolAnthropic},
	})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex(""), Registry: reg})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		ModelProvider: "orclaude",
		Model:         "anthropic/claude-3-5-sonnet",
		CWD:           "/tmp",
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "orclaude/anthropic/claude-3-5-sonnet" {
		t.Fatalf("spawn model=%q", got.Resolved.Effective.Model)
	}
	if resp.Thread.Evener.Ref != "local:th_orclaude" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadStartRejectsMalformedModelBeforeSpawn(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			spawnCalled = true
			return rendezvous.Entry{}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Spawner: spawner,
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model: "openrouter",
		CWD:   "/tmp",
	})
	if err == nil {
		t.Fatal("ThreadStart succeeded for malformed model")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v", wire)
	}
	if spawnCalled {
		t.Fatal("spawn was called for malformed model")
	}
}

func TestThreadStart_LaunchOverridesApplied(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       200,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_overrides",
				SessionID: "sess_overrides",
			}, nil
		},
	}
	maxRounds := 7
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:       runDir,
		HubStateRoot: t.TempDir(),
		Spawner:      spawner,
		Past:         hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   "/tmp",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			SkillsDirs: []string{"/per-launch"},
			MaxRounds:  &maxRounds,
		},
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	eff := got.Resolved.Effective
	if !slices.Contains(eff.SkillsDirs, "/per-launch") {
		t.Errorf("SkillsDirs = %v, want /per-launch", eff.SkillsDirs)
	}
	if eff.MaxRounds == nil || *eff.MaxRounds != 7 {
		t.Errorf("MaxRounds = %v, want 7", eff.MaxRounds)
	}
	// Legacy scalar wins: model comes from params.Model, not launchOverrides.
	if eff.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", eff.Model)
	}
}

func TestHubRPCThreadStartUsesGlobalLaunchDefaultModel(t *testing.T) {
	runDir := t.TempDir()
	// stateRoot and launchRoot are deliberately distinct: HubStateRoot and
	// LaunchConfigRoot must resolve independently. Aliasing them (as a
	// previous version of this test did) would let a regression that wires
	// ThreadStart's launch resolution back onto HubStateRoot pass silently,
	// since both fields would coincidentally point at the same directory.
	stateRoot := t.TempDir()
	launchRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(launchRoot)
	if _, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:    cwd,
		Layer:  "global",
		Config: appwire.LaunchConfigLayer{Model: "openai/gpt-5"},
	}); err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       201,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_default_model",
				SessionID: "sess_default_model",
			}, nil
		},
		contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{
			Provider: "openai",
			Model:    "gpt-5",
		}}},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:           runDir,
		HubStateRoot:     stateRoot,
		LaunchConfigRoot: launchRoot,
		Spawner:          spawner,
		Past:             hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		CWD: cwd,
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", got.Resolved.Effective.Model)
	}
	if got.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", got.Provider)
	}
}

func makeResumeSession(t *testing.T, root, sessionID, profileID, model string) (string, *hubcore.PastIndex) {
	t.Helper()
	stateDir := filepath.Join(root, "projects", "project-resume-0000000000")
	workingDir := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: profileID,
		Model:     model,
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return stateDir, past
}

func TestResumeRequestForConfigPassesThroughOpenAIProfileID(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	stateDir, past := makeResumeSession(t, root, sessionID, "openai", "gpt-4o")

	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != "openai" {
		t.Fatalf("provider=%q, want %q", req.Provider, "openai")
	}
	if req.Resolved.Effective.Model != "openai/gpt-4o" {
		t.Fatalf("model=%q, want %q", req.Resolved.Effective.Model, "openai/gpt-4o")
	}
	if req.WorkingDir == "" || req.StateDir != stateDir {
		t.Fatalf("resume request=%+v", req)
	}
}

func TestResumeRequestForConfigPassesThroughCustomProfileID(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv2enqVTitaig6F"
	_, past := makeResumeSession(t, root, sessionID, "work", "gpt-4o")

	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != "work" {
		t.Fatalf("provider=%q, want %q", req.Provider, "work")
	}
	if req.Resolved.Effective.Model != "work/gpt-4o" {
		t.Fatalf("model=%q, want %q", req.Resolved.Effective.Model, "work/gpt-4o")
	}
}

func TestResumeRequestForConfigErrorsOnEmptyProfileID(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	_, past := makeResumeSession(t, root, sessionID, "", "gpt-4o")

	_, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err == nil {
		t.Fatal("expected error for empty profile id, got nil")
	}
}

// TestResumeRequestForConfigUsesRestoreRootWhenWorktreeActive proves the
// native worktree tools spec §7 "Hub consumers" migration: a session
// actively inside a worktree must resume with `--dir` set to its restore
// root, not the worktree path — Task 18's resume re-entry logic takes the
// session back into the worktree itself; hub-driven `--dir` must not launch
// straight into it (or a deleted corpse of it), bypassing the lock and
// validation rules.
func TestResumeRequestForConfigUsesRestoreRootWhenWorktreeActive(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	stateDir := filepath.Join(root, "projects", "project-resume-0000000000")
	restoreRoot := t.TempDir()
	worktreePath := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:                  sessionID,
		ProfileID:           "openai",
		Model:               "gpt-4o",
		EnvInfo:             schema.EnvironmentInfo{WorkingDir: worktreePath},
		WorktreePath:        worktreePath,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: restoreRoot,
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.WorkingDir != restoreRoot {
		t.Fatalf("resume dir=%q, want restore root %q (not the worktree path)", req.WorkingDir, restoreRoot)
	}
}

func TestHubRPCThreadResumeSpawnsAndReadsDaemon(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "local:th_resumed" {
			t.Fatalf("ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_resumed", SessionID: "sess_resumed", Evener: appwire.EvenerThread{Ref: "local:th_resumed"}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != "sess_old" {
				t.Fatalf("resume session=%q", req.SessionID)
			}
			entry := rendezvous.Entry{
				PID:       105,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  "th_resumed",
				SessionID: "sess_resumed",
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Session: "sess_old"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resp.Thread.ID != "th_resumed" || resp.Thread.Evener.Ref != "local:th_resumed" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadResumeReplacesIncompatibleRosterDaemon(t *testing.T) {
	const sessionID = "sess_old"

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       104,
		Protocol:  "evener-appwire-v1",
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
	roster.Refresh()
	if stale, ok := roster.Find(sessionID); !ok || stale.Status == "errored" {
		t.Fatalf("stale roster entry = %+v, %v; want non-errored incompatible daemon", stale, ok)
	}

	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID {
				t.Fatalf("resume session=%q", req.SessionID)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:       105,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}
			writeRendezvous(t, runDir, entry)
			roster.Refresh()
			return entry, nil
		},
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		Spawner:     spawner,
		Past:        hubcore.NewPastIndex(""),
		ResumeLocks: hubcore.NewResumeLocks(),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Session: sessionID})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if resp.Thread.ID != sessionID || resp.Thread.Evener.Ref != "local:"+sessionID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

// A replacement spawn cannot win the session's exclusive API-log reservation
// while the daemon it is replacing is still running, so the operator's only
// way forward is to stop that process. The hub holds its pid and address in
// the roster at the moment it refuses to reuse it; the failure must hand both
// over instead of leaving the operator to find the holder with lsof (kata
// ew86, demonstrated live against a real v1 daemon).
func TestHubRPCThreadResumeNamesLiveIncompatibleDaemonWhenReplacementFails(t *testing.T) {
	const (
		sessionID   = "sess_old"
		blockerPID  = 104
		blockerHTTP = "127.0.0.1:61535"
		// Verbatim shape of the failure a real replacement daemon dies with.
		spawnFailure = "resume failed: process exited before rendezvous: exit status 1: " +
			"evener serve: session sess_old is already running; send work to the live session or fork it: " +
			"API log target is already running: /state/sessions/sess_old.api.jsonl"
	)

	tests := []struct {
		name        string
		rosterEntry *rendezvous.Entry
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "live incompatible daemon still owns the session",
			rosterEntry: &rendezvous.Entry{
				PID:       blockerPID,
				Protocol:  "evener-appwire-v1",
				Address:   blockerHTTP,
				Endpoint:  "ws://" + blockerHTTP + "/rpc",
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			},
			wantContain: []string{
				// the holder
				"pid 104",
				// why the hub will not just talk to it
				"evener-appwire-v1",
				appwire.ProtocolVersion,
				// the remedy the operator can actually run
				"kill 104",
				// the underlying cause, preserved
				spawnFailure,
			},
		},
		{
			name:        "no live daemon to blame",
			rosterEntry: nil,
			wantContain: []string{spawnFailure},
			// Nothing died holding the session, so the hub must not invent a
			// holder or a pid.
			wantAbsent: []string{"pid ", "/shutdown"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			var roster *hubcore.Roster
			if tc.rosterEntry != nil {
				writeRendezvous(t, runDir, *tc.rosterEntry)
				roster = hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
			} else {
				roster = hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
			}
			roster.Refresh()

			spawner := &fakeRPCSpawner{
				resume: func(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
					return rendezvous.Entry{}, errors.New(spawnFailure)
				},
			}

			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:      runDir,
				Roster:      roster,
				Spawner:     spawner,
				Past:        hubcore.NewPastIndex(""),
				ResumeLocks: hubcore.NewResumeLocks(),
			})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()

			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			_, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Session: sessionID})
			if err == nil {
				t.Fatal("ThreadResume succeeded, want the replacement spawn to fail")
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("ThreadResume error %T=%v, want WireError", err, err)
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(wire.Message, want) {
					t.Fatalf("resume error is missing %q:\n%s", want, wire.Message)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(wire.Message, absent) {
					t.Fatalf("resume error should not contain %q:\n%s", absent, wire.Message)
				}
			}
		})
	}
}

func TestHubRPCThreadStartRelaysReturnedSourceThread(t *testing.T) {
	source := &startResumeRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_start_relay",
			SessionID: "th_start_relay",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_start_relay", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Harness: "codex", CWD: "/work", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Evener.Ref != "codex:th_start_relay" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_start_relay",
			Ref:      "codex:th_start_relay",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "after start",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start relay notification")
	}
}

func TestHubRPCThreadStartReturnsThreadWhenPostStartRelayFails(t *testing.T) {
	source := &startRelayFailureSource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_start_relay_fail",
			SessionID: "th_start_relay_fail",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_start_relay_fail", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Harness: "codex", CWD: "/work", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Evener.Ref != "codex:th_start_relay_fail" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyWarning {
			t.Fatalf("method=%q, want warning", got.Method)
		}
		if !strings.Contains(string(got.Params), "subscribe failed after start") || !strings.Contains(string(got.Params), `"source":"hub"`) {
			t.Fatalf("warning params=%s", got.Params)
		}
		payload := warningPayload(got.Params)
		if payload["source"] != "hub" || payload["title"] != "Live updates unavailable" {
			t.Fatalf("warning payload=%+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay warning")
	}
}

func TestHubRPCThreadResumeRelaysReturnedSourceThread(t *testing.T) {
	source := &startResumeRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_resume_relay",
			SessionID: "th_resume_relay",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_resume_relay", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Ref: "codex:th_resume_relay"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resp.Thread.Evener.Ref != "codex:th_resume_relay" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyAgentMessageDelta,
		Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
			ThreadID: "th_resume_relay",
			Ref:      "codex:th_resume_relay",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "after resume",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resume relay notification")
	}
}

func TestHubRPCTurnStartBlocksUnknownMutationWhenAutoResumeFails(t *testing.T) {
	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() {
		resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
	})

	const (
		mutationID    = "mutation-resume-failed"
		resumeMessage = "restore session: incompatible mutation snapshot"
	)
	tests := []struct {
		name           string
		configure      func(*int)
		wantStartCalls int
	}{
		{
			name: "initial source resolution",
			configure: func(_ *int) {
				resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
					return nil, errors.New("source unavailable")
				}
			},
		},
		{
			name: "session unavailable while starting turn",
			configure: func(startCalls *int) {
				source := &scriptedAppSource{
					id: "local",
					startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
						(*startCalls)++
						return appwire.TurnStartResponse{}, appwire.SessionUnavailable("daemon went away")
					},
				}
				resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
					return source, nil
				}
			},
			wantStartCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			startCalls := 0
			resumeCalls := 0
			tc.configure(&startCalls)
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
				resumeCalls++
				return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(resumeMessage)
			}

			server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
			_, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
				ClientMutationID: mutationID,
			})

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("TurnStart error %T=%v, want WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok ||
				wire.Code != appwire.CodeInternalError ||
				wire.Message != resumeMessage ||
				data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
				data.ClientMutationID != mutationID ||
				data.MutationOutcome != appwire.MutationOutcomeUnknown ||
				data.RetryDisposition != appwire.RetryDispositionBlocked ||
				data.Cause != "persistenceUnavailable" {
				t.Fatalf("wire code=%d message=%q data=%#v", wire.Code, wire.Message, wire.Data)
			}
			if resumeCalls != 1 {
				t.Fatalf("resume calls=%d, want 1", resumeCalls)
			}
			if startCalls != tc.wantStartCalls {
				t.Fatalf("start calls=%d, want %d", startCalls, tc.wantStartCalls)
			}
		})
	}
}

func TestHubRPCTurnStartResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	var gotPrompt string
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		gotPrompt = inputTextForTest(params.Input)
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_4"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			entry := rendezvous.Entry{
				PID:        106,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if gotPrompt != "resume work" {
		t.Fatalf("prompt=%q", gotPrompt)
	}
}

func TestHubRPCTurnStartResumesPastThreadAfterRelaySubscribeUnavailable(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	source := &resumeAfterSubscribeUnavailableSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
	}
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			return rendezvous.Entry{
				Protocol:   appwire.ProtocolVersion,
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}, nil
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Spawner: spawner, Past: past})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()
	client := dialHubRPC(t, srv)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume after relay"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if prompt := source.lastStartPrompt(); prompt != "resume after relay" {
		t.Fatalf("start prompt=%q", prompt)
	}
	if calls := source.subscribeCalls(); calls != 2 {
		t.Fatalf("subscribe calls=%d, want 2", calls)
	}
}

func TestHubRPCTurnStartDoesNotResumePastThreadOnLiveStartError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, appwire.Unavailable("session is processing")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       107,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, errors.New("resume should not be called")
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
	if err == nil || !strings.Contains(err.Error(), "session is processing") {
		t.Fatalf("TurnStart err=%v, want live start error", err)
	}
	if resumeCalled {
		t.Fatal("resume was called for a non-stale live StartTurn error")
	}
}

func TestHubRPCTurnStartDoesNotResumePastThreadOnGenericSubstringError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, appwire.InternalError("tool output included connection refused")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       108,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, errors.New("resume should not be called")
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("TurnStart err=%v, want live start error", err)
	}
	if resumeCalled {
		t.Fatal("resume was called for a generic live StartTurn error")
	}
}

func TestHubRPCTurnStartResumesPastThreadAndRelaysNotifications(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_4"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			entry := rendezvous.Entry{
				PID:        107,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	daemon.Broadcast(sessionID, appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: sessionID,
		Ref:      "local:" + sessionID,
		TurnID:   "turn_4",
		ItemID:   "item_1",
		Delta:    "live update",
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resumed turn notification")
	}
}

func TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError(t *testing.T) {
	for _, refreshFailure := range []bool{false, true} {
		t.Run(fmt.Sprint("refresh failure=", refreshFailure), func(t *testing.T) {
			root := t.TempDir()
			workingDir := t.TempDir()
			stateDir := filepath.Join(root, "projects", "project-past-0000000000")
			sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			staleAddress := ln.Addr().String()
			staleEndpoint := "ws://" + ln.Addr().String() + "/rpc"
			if err := ln.Close(); err != nil {
				t.Fatal(err)
			}

			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				appserver.Subscribe(ctx, sessionID)
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
			})
			appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
				return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_recovered"}}, nil
			})
			daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer daemonHTTP.Close()

			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID:       -1,
				Address:   staleAddress,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  staleEndpoint,
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
				StartedAt: time.Now().UTC(), // fresh crash: within the roster's crash-retention window
			})
			prober := perAddrProber{byAddr: map[string]struct{ SessionID, Status string }{}}
			roster := hubcore.NewRoster(runDir, prober)
			roster.Refresh()
			if stale, ok := roster.Find(sessionID); !ok || stale.Status != "errored" || !stale.Crashed {
				t.Fatalf("stale roster entry = %+v, %v; want retained crash marker", stale, ok)
			}
			resumeCalled := false
			spawner := &fakeRPCSpawner{
				resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
					if req.WorkingDir != workingDir {
						t.Fatalf("resume request=%+v", req)
					}
					resumeCalled = true
					entry := rendezvous.Entry{
						PID:        110,
						Address:    daemonHTTP.Listener.Addr().String(),
						Protocol:   appwire.ProtocolVersion,
						Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
						SourceID:   "local",
						ThreadID:   sessionID,
						SessionID:  sessionID,
						WorkingDir: workingDir,
						StartedAt:  time.Now().UTC(), // a real spawn stamps StartedAt; it must outrank the stale crashed entry
					}
					prober.byAddr[entry.Address] = struct{ SessionID, Status string }{SessionID: sessionID, Status: "idle"}
					writeRendezvous(t, runDir, entry)
					if refreshFailure {
						if err := os.WriteFile(filepath.Join(runDir, "1.json"), []byte("{"), 0600); err != nil {
							t.Fatal(err)
						}
					}

					roster.Refresh()
					return entry, nil
				},
			}
			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:      runDir,
				Roster:      roster,
				Spawner:     spawner,
				Past:        past,
				ResumeLocks: hubcore.NewResumeLocks(),
			})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()

			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
			if err != nil {
				t.Fatalf("TurnStart: %v", err)
			}
			if !resumeCalled {
				t.Fatal("resume was not called after local transport error")
			}
			if resp.Turn.ID != "turn_recovered" {
				t.Fatalf("turn=%+v", resp.Turn)
			}

		})
	}
}

// sessionUnavailableOnceSource returns SessionUnavailable on the first
// StartTurn and tracks ResumeThread calls.
type sessionUnavailableOnceSource struct {
	relayLifecycleSource
	id          string
	mu          sync.Mutex
	startCalls  int
	resumeCalls int
	thread      appwire.Thread
}

func (s *sessionUnavailableOnceSource) ID() string { return s.id }

func (s *sessionUnavailableOnceSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *sessionUnavailableOnceSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

func (s *sessionUnavailableOnceSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startCalls++
	calls := s.startCalls
	s.mu.Unlock()
	if calls == 1 {
		return appwire.TurnStartResponse{}, appwire.SessionUnavailable("daemon went away")
	}
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_recovered"}}, nil
}

func (s *sessionUnavailableOnceSource) counts() (start, resume int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCalls, s.resumeCalls
}

// TestHubRPCTurnStartDoesNotResumeUnknownNonLocalRef confirms the local
// past-index retry gate refuses non-local refs. The hub should bubble up the
// original SessionUnavailable error without attempting a resume.
func TestHubRPCTurnStartDoesNotResumeUnknownNonLocalRef(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	fake := &sessionUnavailableOnceSource{
		canceled: make(chan struct{}, 1),
		id:       "remote",
		thread: appwire.Thread{
			ID:        "th_unknown",
			SessionID: "th_unknown",
			Source:    "remote",
			Evener: appwire.EvenerThread{
				Ref:          "remote:th_unknown",
				Capabilities: appwire.ThreadCapabilities{Send: true},
			},
		},
	}
	web.sources.Add(fake)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation",
		ExpectedInstanceID: "th_unknown",
		Ref:                "remote:th_unknown",
		Input:              []appwire.InputItem{{Type: "text", Text: "should not resume"}},
	})
	if err == nil {
		t.Fatal("TurnStart succeeded, want SessionUnavailable error")
	}
	if !strings.Contains(err.Error(), "daemon went away") {
		t.Fatalf("err=%v, want daemon went away", err)
	}
	starts, resumes := fake.counts()
	if starts != 1 {
		t.Fatalf("StartTurn calls=%d, want 1 (no retry for unknown ref)", starts)
	}
	if resumes != 0 {
		t.Fatalf("ResumeThread calls=%d, want 0 (gate must reject unknown non-local ref)", resumes)
	}
}

func TestHubRPCPathsCompleteReturnsMatchingDirectories(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha")
	if err := os.Mkdir(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpine.txt"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 35 {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("child-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	newProject := filepath.Join(root, "new-project")
	if err := os.Mkdir(newProject, 0o755); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.PathsComplete(context.Background(), appwire.PathsCompleteParams{Prefix: filepath.Join(root, "alph")})
	if err != nil {
		t.Fatalf("PathsComplete: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0] != alpha {
		t.Fatalf("dirs=%+v, want [%s]", resp.Data, alpha)
	}

	all, err := client.PathsComplete(context.Background(), appwire.PathsCompleteParams{Prefix: root + "/"})
	if err != nil {
		t.Fatalf("PathsComplete all children: %v", err)
	}
	if len(all.Data) != 38 {
		t.Fatalf("all dirs=%d, want every one of 38 children", len(all.Data))
	}

	fuzzy, err := client.PathsComplete(context.Background(), appwire.PathsCompleteParams{Prefix: filepath.Join(root, "nwprj")})
	if err != nil {
		t.Fatalf("PathsComplete fuzzy: %v", err)
	}
	if len(fuzzy.Data) != 1 || fuzzy.Data[0] != newProject {
		t.Fatalf("fuzzy dirs=%+v, want [%s]", fuzzy.Data, newProject)
	}
}

// TestHubRPCProjectsRecentReturnsMostRecentDirs covers the session creation
// flows' recent-project source (issue #35): evener/projects/recent serves the
// past index's distinct working dirs, most-recently-used first, defaulting to
// the 15-option cap when the request carries no limit.
func TestHubRPCProjectsRecentReturnsMostRecentDirs(t *testing.T) {
	// RecentProjectDirs drops dirs that no longer exist on disk (issue #50),
	// so every seeded WorkingDir must be a real directory.
	root := t.TempDir()
	mkdir := func(name string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		return dir
	}
	alpha := mkdir("alpha")
	beta := mkdir("beta")

	past := hubcore.NewPastIndex("")
	now := time.Now().UTC()
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: now.Add(-1 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: alpha}},
		{ID: "02wMz5Txv2enqVTitaig6F", UpdatedAt: now.Add(-2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: beta}},
		{ID: "02wMz5Txv5aIxgf9yVdd0N", UpdatedAt: now.Add(-3 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: alpha}}, // older dup — dropped
	}
	for n := range 20 {
		metas = append(metas, schema.SessionMeta{
			ID:        fmt.Sprintf("02wMz5Txv1C3Hut0M8GC%02d", n),
			UpdatedAt: now.Add(-time.Duration(n+4) * time.Minute),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: mkdir(fmt.Sprintf("proj-%02d", n))},
		})
	}
	past.SeedForTest(metas)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ProjectsRecent(context.Background(), appwire.ProjectsRecentParams{})
	if err != nil {
		t.Fatalf("ProjectsRecent: %v", err)
	}
	if len(resp.Data) != 15 {
		t.Fatalf("recent dirs=%d, want the default 15-option cap", len(resp.Data))
	}
	if resp.Data[0] != alpha || resp.Data[1] != beta {
		t.Fatalf("recent dirs[0:2]=%v, want [%s %s] (most recently used first)", resp.Data[:2], alpha, beta)
	}

	limited, err := client.ProjectsRecent(context.Background(), appwire.ProjectsRecentParams{Limit: 2})
	if err != nil {
		t.Fatalf("ProjectsRecent limit=2: %v", err)
	}
	if len(limited.Data) != 2 || limited.Data[0] != alpha || limited.Data[1] != beta {
		t.Fatalf("recent dirs limit=2 = %v, want [%s %s]", limited.Data, alpha, beta)
	}
}

// TestHubRPCProjectsRecentEmptyMarshalsAsEmptyArray pins the WIRE shape of a
// hub with no remembered projects. A nil Data slice marshals as JSON `null`,
// but the wire type declares `data: string[]` (non-nullable in the generated
// TypeScript), so a null reaches the browser as a value the type system
// promised was impossible and the first `.length` on it crashes the pane.
func TestHubRPCProjectsRecentEmptyMarshalsAsEmptyArray(t *testing.T) {
	for name, cfg := range map[string]hubcore.WebConfig{
		"no past index": {},
		"empty past":    {Past: hubcore.NewPastIndex("")},
	} {
		t.Run(name, func(t *testing.T) {
			server := newHubAppServer(cfg, appsource.NewRegistry())
			raw, err := server.Router().Dispatch(context.Background(), appwire.Request{
				ID:     appwire.NewIntID(1),
				Method: appwire.MethodEvenerProjectsRecent,
				Params: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatalf("Dispatch projects/recent: %v", err)
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != `{"data":[]}` {
				t.Fatalf("marshalled empty recents = %s, want {\"data\":[]}", encoded)
			}
		})
	}
}

func TestHubRPCThreadForkRoutesNonLocalCapableSource(t *testing.T) {
	source := &forkingRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_fork",
			SessionID: "th_fork",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_fork", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		canceled:      make(chan struct{}, 1),
		response: appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        "th_child",
			SessionID: "th_child",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_child"},
		}},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "codex:th_fork",
		SourceTurnID: "codex-turn-1",
		Model:        "gpt-5-codex",
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if !source.forkCalled {
		t.Fatal("non-local source ForkThread was not called")
	}
	if source.forkParams.SourceTurnID != "codex-turn-1" || source.forkParams.EditedInput != "" {
		t.Fatalf("fork params=%+v", source.forkParams)
	}
	if resp.Thread.Evener.Ref != "codex:th_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkRoutesNonLocalWholeThreadForkWithoutTurnForkCapability(t *testing.T) {
	source := &forkingRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_whole_fork",
			SessionID: "th_whole_fork",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_whole_fork", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		canceled:      make(chan struct{}, 1),
		response: appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        "th_whole_child",
			SessionID: "th_whole_child",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_whole_child"},
		}},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{Ref: "codex:th_whole_fork"})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if !source.forkCalled {
		t.Fatal("whole-thread fork was not routed to source")
	}
	if source.forkParams.SourceTurnID != "" || source.forkParams.EditedInput != "" || source.forkParams.Label != "" {
		t.Fatalf("fork params=%+v", source.forkParams)
	}
	if resp.Thread.Evener.Ref != "codex:th_whole_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkReturnsUnavailableWhenNonLocalSourceCannotFork(t *testing.T) {
	source := &forkingRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_no_fork",
			SessionID: "th_no_fork",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_no_fork", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:          "codex:th_no_fork",
		SourceTurnID: "codex-turn-1",
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork succeeded for source without fork capability")
	}
	if source.forkCalled {
		t.Fatal("fork reached source despite missing capability")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

func TestHubRPCThreadForkCreatesForkedThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "local:" + parentID,
		SourceTurnID: "3",
		EditedInput:  "second task, edited",
		Label:        "before edit",
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == parentID || resp.Thread.Evener.Ref != "local:"+resp.Thread.ID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID || childMeta.DivergenceTurn != 3 {
		t.Fatalf("child meta=%+v", childMeta)
	}
}

// TestHubRPCThreadForkDeferInput verifies the fork-from-message flow (issue
// #42): deferInput forks the thread at the source turn WITHOUT appending a
// replacement message, so the child transcript holds only the prefix and the
// response carries the original input text for the client to stage in its
// composer. The forked session must not auto-run the message.
func TestHubRPCThreadForkDeferInput(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "local:" + parentID,
		SourceTurnID: "3",
		DeferInput:   true,
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == parentID || resp.Thread.Evener.Ref != "local:"+resp.Thread.ID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	if resp.OriginalInput != "second task" {
		t.Fatalf("OriginalInput=%q, want %q", resp.OriginalInput, "second task")
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID || childMeta.DivergenceTurn != 3 {
		t.Fatalf("child meta=%+v", childMeta)
	}
	// The child transcript must contain only the prefix entries [U1, A1]:
	// no trailing USER_INPUT turn that would auto-run on open.
	raw, err := os.ReadFile(filepath.Join(stateDir, "sessions", resp.Thread.ID+".transcript.jsonl"))
	if err != nil {
		t.Fatalf("read child transcript: %v", err)
	}
	if strings.Contains(string(raw), "second task") {
		t.Fatalf("deferred fork must not copy the diverging user message:\n%s", raw)
	}
}

// TestHubRPCThreadForkDeferInputRejectsEditedInput verifies that deferInput
// and editedInput are mutually exclusive: one either replaces the message
// inline (editedInput) or hands it back for editing (deferInput), never both.
func TestHubRPCThreadForkDeferInputRejectsEditedInput(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:          "local:" + parentID,
		SourceTurnID: "3",
		EditedInput:  "second task, edited",
		DeferInput:   true,
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork with both editedInput and deferInput should fail")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error=%v, want InvalidParams", err)
	}
}

type fakeRPCSpawner struct {
	spawn        func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error)
	resume       func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error)
	launchModels func(context.Context) ([]appwire.ModelDescriptor, error)
}

type failedRPCProber struct{}

func (failedRPCProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	return hubcore.ProbeResult{}
}

type dropFirstConnectionListener struct {
	net.Listener
	once    sync.Once
	dropped chan struct{}
}

func (l *dropFirstConnectionListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		dropped := false
		l.once.Do(func() {
			dropped = true
			_ = conn.Close()
			close(l.dropped)
		})
		if dropped {
			continue
		}
		return conn, nil
	}
}

type fakeRPCModelContractSpawner struct {
	fakeRPCSpawner
	contract appwire.ModelListResponse
	err      error
}

func (f *fakeRPCModelContractSpawner) ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error) {
	if f.err != nil {
		return appwire.ModelListResponse{}, f.err
	}
	return f.contract, nil
}

type fakeRPCWorkingDirModelContractSpawner struct {
	fakeRPCSpawner
	fallback              appwire.ModelListResponse
	contractForWorkingDir func(context.Context, string) (appwire.ModelListResponse, error)
}

func (f *fakeRPCWorkingDirModelContractSpawner) ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error) {
	return f.fallback, nil
}

func (f *fakeRPCWorkingDirModelContractSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, cwd string) (appwire.ModelListResponse, error) {
	if f.contractForWorkingDir == nil {
		return appwire.ModelListResponse{}, nil
	}
	return f.contractForWorkingDir(ctx, cwd)
}

func (f *fakeRPCSpawner) Spawn(ctx context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
	if f.spawn != nil {
		return f.spawn(ctx, req)
	}
	return rendezvous.Entry{}, appwire.Unavailable("spawn not configured")
}

func (f *fakeRPCSpawner) Resume(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
	if f.resume != nil {
		return f.resume(ctx, req)
	}
	return rendezvous.Entry{}, appwire.Unavailable("resume not configured")
}

func (f *fakeRPCSpawner) ListLaunchModels(ctx context.Context) ([]appwire.ModelDescriptor, error) {
	if f.launchModels != nil {
		return f.launchModels(ctx)
	}
	return nil, appwire.Unavailable("launch model contract not configured")
}

func buildRPCParentSession(t *testing.T, stateDir string) string {
	t.Helper()
	return buildRPCParentSessionWithWorkingDir(t, stateDir, t.TempDir())
}

func buildRPCParentSessionWithWorkingDir(t *testing.T, stateDir, workingDir string) string {
	t.Helper()
	parentID := "02wMz5Txv1C3Hut0M8GCeB"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl"), transcript.Header{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5",
		WorkingDir: workingDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
	} {
		if err := writer.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             parentID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: workingDir},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		TurnCount:      2,
		OriginalPrompt: "second task",
	}); err != nil {
		t.Fatal(err)
	}
	return parentID
}

// TestLaunchInstanceExists_AcceptsAProviderTheContractDidNotEnumerate pins
// the registry-only rule (spec §11.3): a launch model contract that never
// listed an instance does not make that instance unlaunchable, as long as the
// registry has it. A name the registry does not have is still refused.
func TestLaunchInstanceExists_AcceptsAProviderTheContractDidNotEnumerate(t *testing.T) {
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, "[providers.work]\nbase = \"anthropic\"\napi_key = \"sk-inline\"\n")
	cfg := hubcore.WebConfig{Registry: newTestRegistry(t, t.TempDir(), tomlPath, nil, nil)}

	if !launchInstanceExists(cfg, "work") {
		t.Error("an instance the registry holds is launchable even when the contract omits it")
	}
	if !launchInstanceExists(cfg, "WORK") {
		t.Error("the instance name is matched case-insensitively, as the launch ref is")
	}
	if launchInstanceExists(cfg, "nowhere") {
		t.Error("a name the registry does not have must not be launchable")
	}
	if launchInstanceExists(hubcore.WebConfig{}, "work") {
		t.Error("with no registry there is nothing to accept on")
	}
}

func TestHubRPCInstanceListRoutesToController(t *testing.T) {
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, "[providers.my-openai]\nbase = \"openai\"\napi_key = \"sk-inline\"\n")
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, nil, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceList, appwire.EmptyParams{}, &resp); err != nil {
		t.Fatalf("evener/instance/list: %v", err)
	}
	found := false
	for _, inst := range resp.Instances {
		if inst.Name == "my-openai" {
			found = true
		}
	}
	if !found {
		t.Fatalf("instances=%+v, want the authored my-openai entry", resp.Instances)
	}
	if len(resp.AvailableProviders) == 0 {
		t.Error("AvailableProviders must be non-empty in list response")
	}
	hasOpenAI := false
	for _, p := range resp.AvailableProviders {
		if p.ID == "openai" {
			hasOpenAI = true
		}
	}
	if !hasOpenAI {
		t.Errorf("AvailableProviders=%+v missing the openai registry id", resp.AvailableProviders)
	}
}

// TestHubRPCInstanceCreateBroadcastsAuthUpdated proves the "multiple browsers
// stay in sync" founding requirement for provider-instance CRUD: a successful
// evener/instance/create must broadcast evener/auth/updated so every other
// connected client refetches its now-stale instance list. The notification is
// reused rather than a new one minted for instances: notifications.js already
// treats evener/auth/updated as payload-agnostic ("something about credentials
// or instances changed, refetch"), reloading both the instances panel and the
// providers settings tab on receipt.
func TestHubRPCInstanceCreateBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, nil, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate, appwire.InstanceCreateParams{Base: "anthropic", Name: "mywork"}, &resp); err != nil {
		t.Fatalf("evener/instance/create: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance create")
	}
}

// TestHubRPCInstanceEditBroadcastsAuthUpdated is the evener/instance/edit sibling
// of TestHubRPCInstanceCreateBroadcastsAuthUpdated; see its doc comment for why
// evener/auth/updated is the right (reused) notification.
func TestHubRPCInstanceEditBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, nil, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{Name: "base", BaseURL: "https://example.test"}, &resp); err != nil {
		t.Fatalf("evener/instance/edit: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance edit")
	}
}

// TestHubRPCInstanceRemoveBroadcastsAuthUpdated is the evener/instance/remove
// sibling of TestHubRPCInstanceCreateBroadcastsAuthUpdated; see its doc
// comment for why evener/auth/updated is the right (reused) notification.
func TestHubRPCInstanceRemoveBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, nil, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceRemove, appwire.InstanceRemoveParams{Name: "base"}, &resp); err != nil {
		t.Fatalf("evener/instance/remove: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance remove")
	}
}

// TestHubRPCInstanceSetDefaultBroadcastsAuthUpdated is the
// evener/instance/setDefault sibling of TestHubRPCInstanceCreateBroadcastsAuthUpdated;
// see its doc comment for why evener/auth/updated is the right (reused)
// notification.
func TestHubRPCInstanceSetDefaultBroadcastsAuthUpdated(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, nil, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceSetDefault, appwire.InstanceSetDefaultParams{Name: "base"}, &resp); err != nil {
		t.Fatalf("evener/instance/setDefault: %v", err)
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evener/auth/updated broadcast after instance setDefault")
	}
}

func dialHubRPC(t *testing.T, hub *httptest.Server) *appwire.Client {
	t.Helper()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+hub.URL[len("http"):]+"/rpc", hub.Client())
	if err != nil {
		t.Fatalf("dial hub rpc: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	return client
}

func newHubRPCTestServer(t *testing.T, cfg hubcore.WebConfig) *httptest.Server {
	t.Helper()
	srv, _ := newHubRPCTestServerWithWeb(t, cfg)
	return srv
}

// newHubRPCTestServerWithWeb behaves like newHubRPCTestServer but also
// returns the constructed *WebServer, for tests that need to wire an
// onChange hook on one of its cfg stores (e.g. past.SetOnChange, mirroring
// runMain's composed evener/tree/changed wiring in main.go) before the server
// starts serving requests.
func newHubRPCTestServerWithWeb(t *testing.T, cfg hubcore.WebConfig) (*httptest.Server, *WebServer) {
	t.Helper()
	if cfg.Registry == nil {
		// Every auth and instance answer comes from the registry, so a hub
		// fixture without one answers nothing. Offline, uncached and with no
		// user layer: what the test's own environment and state root say, and
		// nothing from the developer's providers.toml.
		cfg.Registry = hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
			return cmdutil.LoadRegistry(append(extra,
				registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer())...)
		})
		if err := cfg.Registry.Reload(); err != nil {
			t.Fatalf("registry: %v", err)
		}
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg.HubAddr = srv.Listener.Addr().String()
	web := NewWebServer(cfg)
	srv.Config.Handler = web.Handler()
	srv.Start()
	return srv, web
}

// TestHubRPCRegistersExpectedHandlerSet locks in the exact set of RPC methods
// the hub app server registers (with a providers config present so the
// instance handlers register too): the router's method set must equal the
// list below, so both a dropped registration and one nobody has named here
// fail. That guards the constructor decomposition (registerThreadHandlers /
// Auth / Instance / Launch / Plugin / Misc / PluginAutoUpgrade) in both
// directions.
//
// Every named method is then dispatched over the wire and must not answer
// methodNotFound — reachability the router set alone cannot show — except the
// handlers listed in notDispatched, which act outside the process.
func TestHubRPCRegistersExpectedHandlerSet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, "[providers.my-openai]\nbase = \"openai\"\napi_key = \"sk-inline\"\n")
	// A temp-dir-backed credentials store, shared with the registry below:
	// the dispatch loop further down calls every expected method with empty
	// params, including the credential-mutating evener/auth/* handlers
	// (apiKey/set, logout, apiKey/clear). A nil CredsStore makes
	// newHubAuthControllerWithStore fall back to the real on-disk default
	// (~/.config/evener/credentials.toml via the ambient HOME/XDG env) -
	// this test must never read or write a developer's actual store.
	credsStore, loadErr := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	if loadErr != nil {
		t.Fatalf("LoadStore: %v", loadErr)
	}
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	expected := []string{
		appwire.MethodThreadList,
		appwire.MethodThreadRead,
		appwire.MethodThreadUnsubscribe,
		appwire.MethodThreadTurnsList,
		appwire.MethodEvenerSubagentPreview,
		appwire.MethodThreadStart,
		appwire.MethodThreadResume,
		appwire.MethodThreadFork,
		appwire.MethodTurnStart,
		appwire.MethodTurnSteer,
		appwire.MethodTurnInterrupt,
		appwire.MethodEvenerSandboxEscalationResolve,
		appwire.MethodTurnQueue,
		appwire.MethodTurnDrainAsSteer,
		appwire.MethodTurnPromoteQueuedAsSteer,
		appwire.MethodTurnCancelQueued,
		appwire.MethodThreadClear,
		appwire.MethodThreadCompactStart,
		appwire.MethodThreadShutdown,
		appwire.MethodThreadModelSet,
		appwire.MethodThreadVisionModelSet,
		appwire.MethodEvenerThreadNameSet,
		appwire.MethodThreadReasoningEffortSet,
		appwire.MethodGoalSet,
		appwire.MethodEvenerAuthStatus,
		appwire.MethodEvenerAuthTest,
		appwire.MethodEvenerAuthLoginStart,
		appwire.MethodEvenerAuthLoginComplete,
		appwire.MethodEvenerAuthLogout,
		appwire.MethodEvenerAuthList,
		appwire.MethodEvenerAuthApiKeySet,
		appwire.MethodEvenerAuthApiKeyClear,
		appwire.MethodEvenerAuthCredentialJsonSet,
		appwire.MethodEvenerAuthDeviceStart,
		appwire.MethodEvenerAuthDevicePoll,
		appwire.MethodEvenerNavigationRead,
		appwire.MethodEvenerFavoriteSet,
		appwire.MethodEvenerArchiveSet,
		appwire.MethodEvenerProjectDelete,
		appwire.MethodEvenerSessionDelete,
		appwire.MethodEvenerPinSectionRename,
		appwire.MethodEvenerPinSectionDelete,
		appwire.MethodEvenerSessionPinAssign,
		appwire.MethodEvenerSessionPinUnpin,
		appwire.MethodEvenerSearch,
		appwire.MethodEvenerInstanceList,
		appwire.MethodEvenerInstanceCreate,
		appwire.MethodEvenerInstanceEdit,
		appwire.MethodEvenerInstanceRemove,
		appwire.MethodEvenerInstanceSetDefault,
		appwire.MethodEvenerLaunchResolve,
		appwire.MethodEvenerLaunchSchema,
		appwire.MethodEvenerLaunchGetLayer,
		appwire.MethodEvenerLaunchSetLayer,
		appwire.MethodEvenerLaunchTrustRepo,
		appwire.MethodEvenerUpgrade,
		appwire.MethodModelList,
		appwire.MethodEvenerTasksList,
		appwire.MethodEvenerJobsList,
		appwire.MethodEvenerJobsOutput,
		appwire.MethodEvenerThreadTranscriptsList,
		appwire.MethodEvenerPathsComplete,
		appwire.MethodEvenerDirsCreate,
		appwire.MethodEvenerProjectsRecent,
		appwire.MethodEvenerPathValidate,
		appwire.MethodEvenerGitHead,
		appwire.MethodEvenerMobilePairing,
		appwire.MethodEvenerHarnessesList,
		appwire.MethodEvenerCommandList,
		appwire.MethodEvenerSettingsOverview,
		appwire.MethodEvenerSettingsTranscriptDisplayGet,
		appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		appwire.MethodEvenerSettingsKeybindingsGet,
		appwire.MethodEvenerSettingsKeybindingsPatch,
		appwire.MethodEvenerMarketplaceList,
		appwire.MethodEvenerMarketplaceAdd,
		appwire.MethodEvenerMarketplaceRemove,
		appwire.MethodEvenerMarketplaceRefresh,
		appwire.MethodEvenerMarketplaceBrowse,
		appwire.MethodEvenerPluginList,
		appwire.MethodEvenerPluginInstall,
		appwire.MethodEvenerPluginUpgrade,
		appwire.MethodEvenerPluginRemove,
		appwire.MethodEvenerPluginEnable,
		appwire.MethodEvenerPluginDisable,
		appwire.MethodEvenerPluginSetAutoUpgrade,
		appwire.MethodEvenerPluginCheckNow,
		appwire.MethodEvenerPluginPreview,
	}

	// The list is a lock, not a sample: nothing may be registered that it does
	// not name, so a handler arriving without a test naming it fails here too.
	registered := excludeHubMethods(web.appRPC.Router().Methods(), appwire.ConnectionMethodNames())
	if missing, extra := setDiff(expected, registered); len(missing) > 0 || len(extra) > 0 {
		t.Errorf("hub handler set differs from the set this test names:\n  named but NOT registered: %v\n  registered but NOT named: %v", missing, extra)
	}

	// notDispatched are named above but never called: their handlers act
	// outside this process. evener/upgrade runs the real self-update (fetch and
	// install over the running binary), and the marketplace, plugin and
	// auto-upgrade handlers work against the plugin root — which, with no
	// PluginRoot configured, is the developer's own plugins.DefaultRoot — and
	// fetch its remote sources. app_plugins_test.go and
	// app_plugin_autoupgrade_test.go drive those against fixture roots.
	notDispatched := map[string]bool{
		appwire.MethodEvenerUpgrade:              true,
		appwire.MethodEvenerMarketplaceList:      true,
		appwire.MethodEvenerMarketplaceAdd:       true,
		appwire.MethodEvenerMarketplaceRemove:    true,
		appwire.MethodEvenerMarketplaceRefresh:   true,
		appwire.MethodEvenerMarketplaceBrowse:    true,
		appwire.MethodEvenerPluginList:           true,
		appwire.MethodEvenerPluginInstall:        true,
		appwire.MethodEvenerPluginUpgrade:        true,
		appwire.MethodEvenerPluginRemove:         true,
		appwire.MethodEvenerPluginEnable:         true,
		appwire.MethodEvenerPluginDisable:        true,
		appwire.MethodEvenerPluginSetAutoUpgrade: true,
		appwire.MethodEvenerPluginCheckNow:       true,
	}

	for _, method := range expected {
		if notDispatched[method] {
			continue
		}
		// We only care about the dispatch outcome, not the response body, so
		// pass a nil out. A registered handler may succeed or reject the empty
		// params with some other error; what it must never return is
		// methodNotFound.
		err := client.Request(context.Background(), method, appwire.EmptyParams{}, nil)
		var wire appwire.WireError
		if errors.As(err, &wire) && wire.Code == appwire.CodeMethodNotFound {
			t.Errorf("method %q is not registered (methodNotFound)", method)
		}
	}

	// Sanity check: an unregistered method must report methodNotFound, proving
	// the assertion above is meaningful.
	err := client.Request(context.Background(), "evener/__definitely_not_registered__", appwire.EmptyParams{}, nil)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeMethodNotFound {
		t.Fatalf("expected methodNotFound for unknown method, got %T: %v", err, err)
	}
}

func TestHubRPCThreadStartEnvModelSatisfiesRequiredGate(t *testing.T) {
	// A spawn with no model in any launch layer still launches when
	// EVENER_MODEL is set for the hub process: the agent's own fallback
	// chain (flag > env > none) would use it. The "model is required"
	// gate must not reject what the child would have run with anyway —
	// and the resolve RPC's "(default)" label already names that model,
	// so the gate and the label must agree.
	runDir := t.TempDir()
	launchRoot := t.TempDir()
	t.Setenv("EVENER_MODEL", "openai/gpt-5")
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       202,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_env_model",
				SessionID: "sess_env_model",
			}, nil
		},
		contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{
			Provider: "openai",
			Model:    "gpt-5",
		}}},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:           runDir,
		HubStateRoot:     t.TempDir(),
		LaunchConfigRoot: launchRoot,
		Spawner:          spawner,
		Past:             hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		CWD: t.TempDir(),
	}); err != nil {
		t.Fatalf("ThreadStart should accept env-model spawn: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want env model threaded into spawn", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartEmptyModelRejected(t *testing.T) {
	// The counterpart to TestHubRPCThreadStartEnvModelSatisfiesRequiredGate:
	// with no model in any layer, no EVENER_MODEL env, and no per-launch
	// override, the spawn gate rejects with "model is required". This guards
	// the branch the env path exists to satisfy.
	t.Setenv("EVENER_MODEL", "") // ensure ambient env cannot leak a model in
	runDir := t.TempDir()
	launchRoot := t.TempDir()
	spawner := &fakeRPCModelContractSpawner{
		spawn: func(_ context.Context, _ hubcore.SpawnRequest) (rendezvous.Entry, error) {
			t.Fatal("spawner should not be called when model is empty")
			return rendezvous.Entry{}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:           runDir,
		HubStateRoot:     t.TempDir(),
		LaunchConfigRoot: launchRoot,
		Spawner:          spawner,
		Past:             hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		CWD: t.TempDir(),
	})
	if err == nil {
		t.Fatal("ThreadStart should fail when model resolves to empty")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("error = %v, want error containing \"model is required\"", err)
	}
}
