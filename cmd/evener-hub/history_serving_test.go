package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/llm"
)

type servingLiveSource struct {
	pastFallbackRelaySource
	read appwire.ThreadReadResponse
	page appwire.ThreadTurnsListResponse
}

func (s *servingLiveSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return s.page, s.readErr
}

func (s *servingLiveSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if params.IncludeTurns && s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.read, nil
}

// The disk transcript and its index are real. Only the unavailable daemon is
// replaced at its transport boundary.
func TestHistoryServingSavedFallbackFacts(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "live-read-empty"
		if failed {
			name = "live-read-failed"
		}
		t.Run(name, func(t *testing.T) {
			cfg, entry, _ := seedIndexedPastSession(t,
				execution("older", schema.NewTurn(schema.TurnUserInput, llm.User("saved-only"))),
				execution("older", schema.NewTurn(schema.TurnAssistant, llm.Assistant("saved-recent"))),
				completedExecution("older"))
			sources := appsource.NewRegistry()
			source := &servingLiveSource{read: appwire.ThreadReadResponse{
				Thread:         appwire.Thread{ID: entry.Meta.ID, Evener: appwire.EvenerThread{Ref: "local:" + entry.Meta.ID, InstanceID: "new-instance", ActiveTurnID: "newer", LatestExecutionTurnID: "newer"}},
				BootGeneration: "42", Epoch: 7,
			}}
			source.canceled = make(chan struct{})
			if failed {
				source.readErr = appwire.SessionUnavailable("fixture")
			}
			sources.Add(source)
			app := newHubAppServer(cfg, sources)
			hub := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
			defer hub.Close()
			client := dialHubRPCInitialized(t, hub)
			// Decode independently of production response tags. encoding/json's
			// case-insensitive field matching accepts these wire keys.
			var response struct {
				appwire.ThreadReadResponse
				ServedFrom     string
				FallbackReason string
				AsOf           string
				InstanceID     string
			}
			err := client.Request(t.Context(), "evener/thread/history/read", appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 1}, &response)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Thread.Turns) == 0 {
				t.Fatal("saved scenario did not serve items")
			}
			if response.ServedFrom != "saved-index" || response.FallbackReason != name || response.Authoritative || response.InstanceID != "" {
				t.Fatalf("serving facts = %+v", response)
			}
			if response.LatestExecutionTurnID != "older" {
				t.Fatalf("saved frontier=%q, want older, not live newer", response.LatestExecutionTurnID)
			}
			if !failed && (response.BootGeneration != "42" || response.Epoch != 7) {
				t.Fatalf("empty-live fallback lost live stamps: boot=%q epoch=%d", response.BootGeneration, response.Epoch)
			}
			if _, err := time.Parse(time.RFC3339Nano, response.AsOf); err != nil {
				t.Fatalf("asOf missing or invalid: %v", err)
			}
			var page appwire.ThreadHistoryTurnsListResponse
			if response.OlderCursor == "" {
				t.Fatal("saved fallback did not return a real older cursor")
			}
			if err := client.Request(t.Context(), appwire.MethodThreadHistoryTurnsList, appwire.ThreadTurnsListParams{Ref: "local:" + entry.Meta.ID, Cursor: response.OlderCursor, ItemLimit: 1}, &page); err != nil {
				t.Fatal(err)
			}
			if page.ServedFrom != "saved-index" || page.FallbackReason != name || page.LatestExecutionTurnID != "older" || page.Authoritative || page.InstanceID != "" || len(page.Data) != 1 {
				t.Fatalf("saved fallback page=%+v", page)
			}
			var metadata appwire.ThreadHistoryReadResponse
			if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID}, &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.ServedFrom != "live-daemon" || metadata.InstanceID != "new-instance" || metadata.Thread.Evener.ActiveTurnID != "newer" || metadata.LatestExecutionTurnID != "newer" {
				t.Fatalf("live metadata facts=%+v", metadata)
			}
		})
	}
}

func TestHistoryServingNormalSavedShapesAndLegacyWire(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("A", schema.NewTurn(schema.TurnUserInput, llm.User("A"))), completedExecution("A"),
		execution("B", schema.NewTurn(schema.TurnUserInput, llm.User("B"))), completedExecution("B"))
	app := newHubAppServer(cfg, appsource.NewRegistry())
	hub := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	ref := "local:" + entry.Meta.ID
	var read appwire.ThreadHistoryReadResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: 1}, &read); err != nil {
		t.Fatal(err)
	}
	if read.ServedFrom != "saved-index" || read.FallbackReason != "" || read.InstanceID != "" || read.Authoritative || read.LatestExecutionTurnID != "B" || read.OlderCursor == "" {
		t.Fatalf("saved read=%+v", read)
	}
	var page appwire.ThreadHistoryTurnsListResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryTurnsList, appwire.ThreadTurnsListParams{Ref: ref, Cursor: read.OlderCursor, ItemLimit: 1}, &page); err != nil {
		t.Fatal(err)
	}
	if page.ServedFrom != "saved-index" || page.FallbackReason != "" || page.InstanceID != "" || page.Authoritative || page.LatestExecutionTurnID != "B" || len(page.Data) != 1 || page.Data[0].ID != "A" {
		t.Fatalf("saved older page=%+v", page)
	}
	var metadata appwire.ThreadHistoryReadResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{Ref: ref}, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.ServedFrom != "saved-index" || metadata.LatestExecutionTurnID != "B" || metadata.Authoritative || len(metadata.Thread.Turns) != 0 {
		t.Fatalf("saved metadata=%+v", metadata)
	}
	var continuation appwire.ThreadHistoryReadResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{Ref: ref, AnswerContinuation: &appwire.AnswerContinuationQuery{AncestorTurnID: "A", CandidateTurnID: "B"}}, &continuation); err != nil {
		t.Fatal(err)
	}
	if continuation.ServedFrom != "saved-index" || continuation.LatestExecutionTurnID != "B" || continuation.Authoritative || continuation.AnswerContinuation == nil {
		t.Fatalf("saved continuation facts=%+v", continuation)
	}
	for _, asOf := range []string{read.AsOf, page.AsOf, metadata.AsOf} {
		if _, err := time.Parse(time.RFC3339Nano, asOf); err != nil {
			t.Fatal(err)
		}
	}
	var oldRead map[string]json.RawMessage
	if err := client.Request(t.Context(), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: 1}, &oldRead); err != nil {
		t.Fatal(err)
	}
	if string(oldRead["authoritative"]) != "true" {
		t.Fatalf("legacy saved stamp=%s", oldRead["authoritative"])
	}
	var oldThread struct {
		Evener map[string]json.RawMessage `json:"evener"`
	}
	if err := json.Unmarshal(oldRead["thread"], &oldThread); err != nil {
		t.Fatal(err)
	}
	if _, added := oldThread.Evener["latestExecutionTurnId"]; added {
		t.Fatal("new row frontier leaked to legacy read")
	}
	var oldPage map[string]json.RawMessage
	if err := client.Request(t.Context(), appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: ref, Cursor: read.OlderCursor, ItemLimit: 1}, &oldPage); err != nil {
		t.Fatal(err)
	}
	legacyPageKeys := map[string]bool{"data": true, "nextCursor": true, "bootGeneration": true, "epoch": true, "snapshot": true, "authoritative": true}
	for key := range oldPage {
		if !legacyPageKeys[key] {
			t.Fatalf("new page field %q leaked to legacy exact-key decoder", key)
		}
	}
	if string(oldPage["authoritative"]) != "true" {
		t.Fatal("legacy saved page lost true stamp")
	}
	for _, key := range []string{"servedFrom", "fallbackReason", "asOf", "instanceId", "servingFactsUnavailableReason"} {
		if _, added := oldRead[key]; added {
			t.Fatalf("new field %q leaked to legacy read", key)
		}
	}
}

func TestHistoryServingSavedTurnWindowCarriesObservation(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("A", schema.NewTurn(schema.TurnUserInput, llm.User("A_SENTINEL"))), completedExecution("A"),
		execution("B", schema.NewTurn(schema.TurnUserInput, llm.User("B_SENTINEL"))), completedExecution("B"))
	app := newHubAppServer(cfg, appsource.NewRegistry())
	hub := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	ref := "local:" + entry.Meta.ID

	var read appwire.ThreadHistoryReadResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, TurnWindow: &appwire.ThreadTurnWindow{Latest: 1},
	}, &read); err != nil {
		t.Fatal(err)
	}
	if read.ServedFrom != "saved-index" || read.Authoritative {
		t.Fatalf("saved turn-window read facts=%+v", read)
	}
	if _, err := time.Parse(time.RFC3339Nano, read.AsOf); err != nil {
		t.Fatalf("saved turn-window read observation=%q err=%v", read.AsOf, err)
	}

	var page appwire.ThreadHistoryTurnsListResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryTurnsList, appwire.ThreadTurnsListParams{
		Ref: ref, TurnWindow: &appwire.ThreadTurnWindow{Latest: 1},
	}, &page); err != nil {
		t.Fatal(err)
	}
	if page.ServedFrom != "saved-index" {
		t.Fatalf("saved turn-window page facts=%+v", page)
	}
	if _, err := time.Parse(time.RFC3339Nano, page.AsOf); err != nil {
		t.Fatalf("saved turn-window page observation=%q err=%v", page.AsOf, err)
	}
}

func TestHistoryServingOlderLocalPageDoesNotBorrowMetadataInstance(t *testing.T) {
	source := &servingLiveSource{
		read: appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "replacement", Evener: appwire.EvenerThread{InstanceID: "replacement-instance"}}},
		page: appwire.ThreadTurnsListResponse{ServingFacts: &appwire.HistoryServingFacts{ServingFactsUnavailableReason: "older-source"}, Data: []appwire.Turn{{ID: "A", Items: []appwire.ThreadItem{{Type: "userMessage", ID: "old-item", TurnID: "A", TranscriptKey: "apptranscript-item-v2:A:0:0", Position: &appwire.ThreadItemPosition{}, Text: "page-only"}}}}},
	}
	page, served, err := listItemTurns(appwire.WithHistoryServingFacts(t.Context()), source, appwire.ThreadTurnsListParams{Ref: "local:workspace", ItemLimit: 1}, nil)
	if err != nil || !served {
		t.Fatalf("page scenario: served=%v error=%v", served, err)
	}
	if page.ServingFacts == nil || page.ServingFacts.ServedFrom != "live-daemon" || page.ServingFacts.AsOf == "" || page.ServingFacts.InstanceID != "" || len(page.Data) != 1 {
		t.Fatalf("older local page facts=%+v page=%+v", page.ServingFacts, page.Data)
	}
}

type managerServingPageSource struct {
	*servingLiveSource
	calls []appwire.ThreadTurnsListParams
}

func (s *managerServingPageSource) ListTurns(ctx context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	s.calls = append(s.calls, p)
	return s.servingLiveSource.ListTurns(ctx, p)
}

func TestManagerHistoryContinuationServingFacts(t *testing.T) {
	for _, scenario := range []string{"normal-saved", "live-read-failed", "live-success", "non-dead-error"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, entry, _ := seedIndexedPastSession(t,
				execution("A", schema.NewTurn(schema.TurnUserInput, llm.User("A_SENTINEL"))), completedExecution("A"),
				execution("B", schema.NewTurn(schema.TurnUserInput, llm.User("B_SENTINEL"))), completedExecution("B"),
				execution("C", schema.NewTurn(schema.TurnUserInput, llm.User("C_SENTINEL"))), completedExecution("C"))
			sources := appsource.NewRegistry()
			owner := &sessionOperations{cfg: cfg, sources: sources}
			caller := appwire.ManagerView{Manager: true, Ref: "local:manager", InstanceID: "manager-instance"}
			ref := "local:" + entry.Meta.ID
			initialBefore := time.Now()
			initial := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{Ref: ref, ItemLimit: 1}})
			initialAfter := time.Now()
			if initial.Turns == nil || initial.Turns.NextCursor == "" || len(initial.Turns.Data) != 1 || initial.Turns.Data[0].ID != "C" {
				t.Fatalf("initial Manager page did not mint a real saved cursor: %+v", initial)
			}
			if scenario == "normal-saved" {
				page := initial.Turns
				if len(sources.All()) != 0 || page.ServedFrom != "saved-index" || page.FallbackReason != "" || page.InstanceID != "" || page.LatestExecutionTurnID != "C" || page.Authoritative {
					t.Fatalf("normal saved browsing acquired live provenance: %+v", page)
				}
				observed, err := time.Parse(time.RFC3339Nano, page.AsOf)
				if err != nil || observed.Before(initialBefore) || observed.After(initialAfter) {
					t.Fatalf("saved initial observation=%q err=%v", page.AsOf, err)
				}
				return
			}
			params := appwire.ThreadTurnsListParams{Ref: ref, Cursor: initial.Turns.NextCursor, ItemLimit: 1}
			saved, ok, err := pastThreadTurnsList(appwire.WithHistoryServingFacts(t.Context()), cfg, params)
			if err != nil || !ok {
				t.Fatalf("real saved continuation: ok=%v err=%v", ok, err)
			}
			baseline := appwire.HistoryTurnsResponse(saved)
			if len(baseline.Data) != 1 || baseline.Data[0].ID != "B" || baseline.NextCursor == "" {
				t.Fatalf("saved continuation did not exercise a middle page: %+v", baseline)
			}
			// Only the external daemon answer changes. Disk/index reads, cursor
			// construction, Manager dispatch and response normalization stay real.
			liveFacts := &appwire.HistoryServingFacts{ServedFrom: "live-daemon", AsOf: "2026-11-08T00:00:00Z", InstanceID: "live-instance"}
			source := &managerServingPageSource{servingLiveSource: &servingLiveSource{
				read: appwire.ThreadReadResponse{Thread: appwire.Thread{ID: entry.Meta.ID, Evener: appwire.EvenerThread{Ref: ref, InstanceID: "metadata-instance"}}},
				page: appwire.ThreadTurnsListResponse{Data: baseline.Data, Snapshot: initial.Turns.Snapshot,
					BootGeneration: "42", Epoch: 7, ServingFacts: liveFacts, LatestExecutionTurnID: "live-frontier"},
			}}
			switch scenario {
			case "live-read-failed":
				source.readErr = appwire.SessionUnavailable(threadNotFoundMessagePrefix + entry.Meta.ID)
			case "non-dead-error":
				source.readErr = context.DeadlineExceeded
			}
			sources.Add(source)
			before := time.Now()
			result, err := owner.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Turns: &params})
			after := time.Now()
			if len(source.calls) != 1 {
				t.Fatalf("live page attempts=%d, want 1", len(source.calls))
			}
			if source.calls[0].Cursor != params.Cursor || source.calls[0].Ref != ref || source.calls[0].ItemLimit != 1 || source.calls[0].ItemsView != string(appwire.TurnItemsViewFragment) {
				t.Fatalf("Manager continuation lost its cursor/target/bound: %+v", source.calls[0])
			}
			if scenario == "non-dead-error" {
				if !errors.Is(err, context.DeadlineExceeded) || result.Turns != nil {
					t.Fatalf("non-dead error was hidden by saved history: result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			page := result.Turns
			if page == nil || !reflect.DeepEqual(page.Data, baseline.Data) || !reflect.DeepEqual(page.Snapshot, baseline.Snapshot) {
				t.Fatalf("continuation changed retained content, bounds or snapshot: %+v", page)
			}
			if scenario == "live-success" {
				if page.ServedFrom != liveFacts.ServedFrom || page.AsOf != liveFacts.AsOf || page.InstanceID != liveFacts.InstanceID || page.LatestExecutionTurnID != "live-frontier" || page.FallbackReason != "" || page.BootGeneration != "42" || page.Epoch != 7 || page.NextCursor != "" || len(result.FollowUps) != 0 || !page.Authoritative {
					t.Fatalf("live page evidence changed: %+v", page)
				}
				return
			}
			if page.NextCursor != baseline.NextCursor || len(result.FollowUps) != 1 || result.FollowUps[0].Arguments.Hub.Cursor != page.NextCursor || result.FollowUps[0].Arguments.Hub.Ref != ref || result.FollowUps[0].Arguments.Hub.ItemLimit != 1 {
				t.Fatalf("Manager follow-up lost the continuation: %+v", result.FollowUps)
			}
			wantReason := "live-read-failed"
			if page.ServedFrom != "saved-index" || page.FallbackReason != wantReason || page.InstanceID != "" || page.LatestExecutionTurnID != "C" || page.BootGeneration != appwire.DaemonlessBootGeneration || page.Epoch != 0 || page.Authoritative {
				t.Fatalf("saved continuation evidence=%+v, want fallbackReason=%q", page, wantReason)
			}
			observed, parseErr := time.Parse(time.RFC3339Nano, page.AsOf)
			if parseErr != nil || observed.Before(before) || observed.After(after) {
				t.Fatalf("saved asOf does not name this read's observation: asOf=%q err=%v", page.AsOf, parseErr)
			}
		})
	}
}
