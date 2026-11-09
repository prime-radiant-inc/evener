package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func TestPackTranscriptItemPageCountLimit(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-1", ProjectionVersion: 1}
	sourceCount := appwire.TranscriptItemPageLimit + 5
	window := appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(sourceCount)}
	var err error
	window.OlderCursor, err = appitempaging.EncodeCursor(identity, window.Candidates[0].Position)
	if err != nil {
		t.Fatalf("source cursor: %v", err)
	}
	got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
		Candidates: window,
		Identity:   identity,
		Exhausted:  false,
	}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		return response, nil
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	items := flattenTestItems(got.Thread.Turns)
	if len(items) != appwire.TranscriptItemPageLimit {
		t.Fatalf("item count = %d, want %d", len(items), appwire.TranscriptItemPageLimit)
	}
	if got, want := items[0].ID, fmt.Sprintf("item-%02d", sourceCount-appwire.TranscriptItemPageLimit); got != want {
		t.Fatalf("first item = %q, want %q", got, want)
	}
	if got, want := items[len(items)-1].ID, fmt.Sprintf("item-%02d", sourceCount-1); got != want {
		t.Fatalf("last item = %q, want %q", got, want)
	}
	if got.OlderCursor == "" || got.OlderCursor == "source-cursor" {
		t.Fatalf("older cursor = %q, want a rebased opaque cursor", got.OlderCursor)
	}
	position, err := appitempaging.DecodeCursor(got.OlderCursor, identity)
	if err != nil {
		t.Fatalf("decode older cursor: %v", err)
	}
	if position != (appwire.ThreadItemPosition{Entry: 5, Item: 0}) {
		t.Fatalf("cursor position = %+v, want entry 5 item 0", position)
	}
}

func TestPackTranscriptItemPageHonorsSmallerRequestedLimit(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "requested-limit", ProjectionVersion: 1}
	got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(45)},
		Identity:   identity,
		Exhausted:  true,
	}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		return response, nil
	}, 3)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if got := len(flattenTestItems(got.Thread.Turns)); got != 3 {
		t.Fatalf("item count = %d, want requested limit 3", got)
	}
}

func TestPackTranscriptItemPageSoftLimit(t *testing.T) {
	t.Run("enrichment can trim an otherwise fitting page", func(t *testing.T) {
		identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-2", ProjectionVersion: 1}
		candidates := testItemCandidates(2)
		withoutEnrichment, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
			Identity:   identity,
			Exhausted:  true,
		}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
			return response, nil
		})
		if err != nil {
			t.Fatalf("baseline pack: %v", err)
		}
		baseline, err := json.Marshal(withoutEnrichment)
		if err != nil {
			t.Fatal(err)
		}
		if len(baseline) >= transcriptRPCResultSoftLimit {
			t.Fatalf("baseline unexpectedly reaches soft limit: %d", len(baseline))
		}

		got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
			Identity:   identity,
			Exhausted:  true,
		}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
			for ti := range response.Thread.Turns {
				for ii := range response.Thread.Turns[ti].Items {
					response.Thread.Turns[ti].Items[ii].OutputImages = []appwire.OutputImage{{URL: strings.Repeat("u", transcriptRPCResultSoftLimit)}}
				}
			}
			return response, nil
		})
		if err != nil {
			t.Fatalf("enriched pack: %v", err)
		}
		if got.OlderCursor == "" {
			t.Fatal("enrichment-trimmed page has no cursor")
		}
		if len(flattenTestItems(got.Thread.Turns)) != 1 {
			t.Fatalf("enrichment-trimmed item count = %d, want 1", len(flattenTestItems(got.Thread.Turns)))
		}
	})

	t.Run("exactly one MiB is accepted", func(t *testing.T) {
		identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-3", ProjectionVersion: 1}
		candidate := testItemCandidates(1)[0]
		payloadLength := findTextLengthForResultSize(t, candidate, transcriptRPCResultSoftLimit)
		candidate.Item.Text = strings.Repeat("x", payloadLength)
		got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: []appitempaging.TranscriptItemCandidate{candidate}},
			Identity:   identity,
			Exhausted:  true,
		}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
			return response, nil
		})
		if err != nil {
			t.Fatalf("exact-limit pack: %v", err)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) != transcriptRPCResultSoftLimit {
			t.Fatalf("result bytes = %d, want %d", len(encoded), transcriptRPCResultSoftLimit)
		}
	})

	t.Run("one oversized item is retained", func(t *testing.T) {
		identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-4", ProjectionVersion: 1}
		candidates := testItemCandidates(1)
		candidates[0].Item.Text = strings.Repeat("x", transcriptRPCResultSoftLimit)
		got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
			Identity:   identity,
			Exhausted:  true,
		}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
			return response, nil
		})
		if err != nil {
			t.Fatalf("oversized pack: %v", err)
		}
		if len(flattenTestItems(got.Thread.Turns)) != 1 {
			t.Fatal("oversized item was dropped")
		}
	})
}

func TestPackTranscriptItemPageTurnsListUsesSameRules(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-5", ProjectionVersion: 1}
	result := transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(45)},
		Identity:   identity,
		Exhausted:  true,
	}
	read, err := packThreadReadItemCandidates(result, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		return response, nil
	})
	if err != nil {
		t.Fatalf("read pack: %v", err)
	}
	list, err := packThreadTurnsItemCandidates(result, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		return response, nil
	})
	if err != nil {
		t.Fatalf("list pack: %v", err)
	}
	if got := flattenTestItems(read.Thread.Turns); len(got) != len(flattenTestItems(list.Data)) {
		t.Fatalf("read/list item counts differ: %d/%d", len(got), len(flattenTestItems(list.Data)))
	}
	for i, item := range flattenTestItems(list.Data) {
		if item.ID != flattenTestItems(read.Thread.Turns)[i].ID {
			t.Fatalf("item %d differs: read=%q list=%q", i, flattenTestItems(read.Thread.Turns)[i].ID, item.ID)
		}
	}
}

// TestPackTranscriptItemPageIgnoresFixedEnvelopeInSoftLimit pins the soft
// limit's decision basis: it bounds the transcript CONTENT the packer can
// actually shrink by dropping items — not the fixed thread envelope (delegate
// statuses, task aggregates) that enrich grafts onto the response and that no
// number of drops can remove. A delegate-heavy past session carries megabytes
// of envelope; measuring it made the packer strip every window to a single
// item (denying the webui its initial scrollback page) while re-marshaling the
// whole envelope once per dropped item.
func TestPackTranscriptItemPageIgnoresFixedEnvelopeInSoftLimit(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-envelope", ProjectionVersion: 1}
	candidates := testItemCandidates(appwire.TranscriptItemPageLimit)
	got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
		Identity:   identity,
		Exhausted:  true,
	}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		// The grafted envelope is fixed no matter which items the packer keeps.
		response.Thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{
			Delegates: []appwire.EvenerDelegateInfo{{
				Task: strings.Repeat("d", transcriptRPCResultSoftLimit),
			}},
		}
		return response, nil
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if got := len(flattenTestItems(got.Thread.Turns)); got != appwire.TranscriptItemPageLimit {
		t.Fatalf("item count = %d, want the full %d-item page: a fixed envelope the packer cannot shrink must not drive item drops", got, appwire.TranscriptItemPageLimit)
	}
	if got.OlderCursor != "" {
		t.Fatalf("older cursor = %q, want none for an exhausted page", got.OlderCursor)
	}
}

func TestPackTranscriptItemWrappersValidateFinalResponses(t *testing.T) {
	tests := []struct {
		name       string
		mutateRead func(*appwire.ThreadReadResponse)
		mutateList func(*appwire.ThreadTurnsListResponse)
	}{
		{
			name: "valid",
		},
		{
			name: "full items view",
			mutateRead: func(response *appwire.ThreadReadResponse) {
				response.Thread.Turns[0].ItemsView = appwire.TurnItemsViewFull
			},
			mutateList: func(response *appwire.ThreadTurnsListResponse) {
				response.Data[0].ItemsView = appwire.TurnItemsViewFull
			},
		},
		{
			name: "empty transcript key",
			mutateRead: func(response *appwire.ThreadReadResponse) {
				response.Thread.Turns[0].Items[0].TranscriptKey = ""
			},
			mutateList: func(response *appwire.ThreadTurnsListResponse) {
				response.Data[0].Items[0].TranscriptKey = ""
			},
		},
		{
			name: "nil position",
			mutateRead: func(response *appwire.ThreadReadResponse) {
				response.Thread.Turns[0].Items[0].Position = nil
			},
			mutateList: func(response *appwire.ThreadTurnsListResponse) {
				response.Data[0].Items[0].Position = nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name+" read", func(t *testing.T) {
			_, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
				Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(1)},
				Identity:   appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "wrapper-validation", ProjectionVersion: 1},
				Exhausted:  true,
			}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
				if test.mutateRead != nil {
					test.mutateRead(&response)
				}
				return response, nil
			})
			if test.name == "valid" {
				if err != nil {
					t.Fatalf("valid read pack: %v", err)
				}
				return
			}
			assertInvalidItemResponseError(t, err)
		})
		t.Run(test.name+" list", func(t *testing.T) {
			_, err := packThreadTurnsItemCandidates(transcriptItemCandidateResult{
				Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(1)},
				Identity:   appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "wrapper-validation", ProjectionVersion: 1},
				Exhausted:  true,
			}, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
				if test.mutateList != nil {
					test.mutateList(&response)
				}
				return response, nil
			})
			if test.name == "valid" {
				if err != nil {
					t.Fatalf("valid list pack: %v", err)
				}
				return
			}
			assertInvalidItemResponseError(t, err)
		})
	}
}

func assertInvalidItemResponseError(t *testing.T, err error) {
	t.Helper()
	wireErr, ok := errors.AsType[appwire.WireError](err)
	if !ok || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %T %v, want invalid params", err, err)
	}
}

func TestPackTranscriptItemPageRejectsOpaqueCursorWithoutIdentity(t *testing.T) {
	const secret = "opaque-legacy-cursor-secret"
	_, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(41), OlderCursor: secret},
		Exhausted:  false,
	}, nil)
	if err == nil {
		t.Fatal("zero-identity opaque cursor was accepted")
	}
	if _, ok := errors.AsType[appwire.WireError](err); ok {
		t.Fatalf("zero-identity opaque cursor returned stale wire error: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("zero-identity opaque cursor leaked secret: %v", err)
	}
}

func TestPackTranscriptItemPageDoesNotMarshalInternalCandidateState(t *testing.T) {
	identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-6", ProjectionVersion: 1}
	got, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: testItemCandidates(1)},
		Identity:   identity,
		Exhausted:  true,
	}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		return response, nil
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Candidates", "Identity", "Exhausted", "incarnation-6"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public result leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestPackTranscriptItemWindowFiltersAndBoundsBodies(t *testing.T) {
	candidates := testWindowTurnCandidates("turn-1", []appwire.ThreadItem{
		{Type: "agentMessage", ID: "agent", Text: strings.Repeat("a", transcriptWindowDefaultMessageBytes+1), Version: 1},
		{Type: "userMessage", ID: "user", Text: "user", Version: 2},
		{Type: "steering", ID: "user-steering", Text: "visible", Source: "user", Version: 3},
		{Type: "steering", ID: "system-steering", Text: "hidden", Version: 4},
		{Type: "commandExecution", ID: "tool", Output: strings.Repeat("o", transcriptWindowDefaultPreviewBytes+1), Version: 5},
		{Type: "reasoning", ID: "reasoning", Text: "hidden", Version: 6},
	})
	options := testWindowPackOptions(appwire.ThreadWindowParams{
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 1},
		ItemTypes:  []string{"messages"},
	})

	read, err := packThreadReadWindowItemCandidates(testWindowCandidateResult(candidates), nil, options)
	if err != nil {
		t.Fatalf("pack messages window: %v", err)
	}
	items := flattenTestItems(read.Thread.Turns)
	if got, want := testItemIDs(items), []string{"agent", "user", "user-steering"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("item ids = %v, want %v", got, want)
	}
	if got := len(items[0].Text); got != transcriptWindowDefaultMessageBytes {
		t.Fatalf("bounded agent text bytes = %d, want %d", got, transcriptWindowDefaultMessageBytes)
	}
	if !items[0].Truncated || items[0].TruncatedBytes != transcriptWindowDefaultMessageBytes+1 || items[0].FetchCursor != "fetch-agent" {
		t.Fatalf("bounded agent label = %+v", items[0])
	}
	elision := read.Thread.Turns[0].Elision
	if elision == nil {
		t.Fatal("filtered turn has no aggregate elision")
	}
	wantExcluded := map[string]int{"steering": 1, "commandExecution": 1, "reasoning": 1}
	if !reflect.DeepEqual(elision.ExcludedByType, wantExcluded) {
		t.Fatalf("excluded populations = %v, want %v", elision.ExcludedByType, wantExcluded)
	}
	if elision.TypeExcludedRecovery != appwire.ThreadElisionRecoveryWiderItemTypes {
		t.Fatalf("type recovery = %q", elision.TypeExcludedRecovery)
	}

	options.Params.ItemTypes = []string{"commandExecution"}
	list, err := packThreadTurnsWindowItemCandidates(testWindowCandidateResult(candidates), nil, options)
	if err != nil {
		t.Fatalf("pack tool window: %v", err)
	}
	toolItems := flattenTestItems(list.Data)
	if got, want := testItemIDs(toolItems), []string{"tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool item ids = %v, want %v", got, want)
	}
	if got := len(toolItems[0].Output); got != transcriptWindowDefaultPreviewBytes {
		t.Fatalf("bounded tool output bytes = %d, want %d", got, transcriptWindowDefaultPreviewBytes)
	}
	if !toolItems[0].Truncated || toolItems[0].TruncatedBytes != transcriptWindowDefaultPreviewBytes+1 || toolItems[0].FetchCursor != "fetch-tool" {
		t.Fatalf("bounded tool label = %+v", toolItems[0])
	}
}

func TestPackTranscriptItemWindowCapsFiftyItemTurnInOrder(t *testing.T) {
	items := make([]appwire.ThreadItem, 50)
	for i := range items {
		items[i] = appwire.ThreadItem{Type: "agentMessage", ID: fmt.Sprintf("item-%02d", i), Text: "x", Version: uint64(i + 1)}
	}
	var continuations []transcriptWindowContinuation
	options := testWindowPackOptions(appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-50"}})
	options.MintContinuationCursor = func(request transcriptWindowContinuation) (string, error) {
		continuations = append(continuations, request)
		return "continue-turn-50", nil
	}
	got, err := packThreadTurnsWindowItemCandidates(testWindowCandidateResult(testWindowTurnCandidates("turn-50", items)), nil, options)
	if err != nil {
		t.Fatalf("pack 50-item turn: %v", err)
	}
	wantIDs := make([]string, transcriptWindowDefaultItemLimit)
	for i := range wantIDs {
		wantIDs[i] = fmt.Sprintf("item-%02d", i)
	}
	if ids := testItemIDs(flattenTestItems(got.Data)); !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("served item ids = %v, want %v", ids, wantIDs)
	}
	elision := got.Data[0].Elision
	if elision == nil || elision.CapWithheld != 40 || elision.ContinuationCursor != "continue-turn-50" || elision.CapWithheldRecovery != appwire.ThreadElisionRecoveryContinuation {
		t.Fatalf("cap elision = %+v", elision)
	}
	if len(continuations) != 1 || continuations[0].Direction != transcriptWindowContinuationForward || continuations[0].TurnID != "turn-50" || continuations[0].Position != (appwire.ThreadItemPosition{Entry: 10}) {
		t.Fatalf("continuations = %+v, want first withheld item", continuations)
	}
}

func TestPackTranscriptItemWindowGroupsInterleavedLogicalTurn(t *testing.T) {
	turnA := testWindowTurnCandidates("turn-a", []appwire.ThreadItem{
		{Type: "agentMessage", ID: "a-1", Text: "first", Version: 1},
		{Type: "agentMessage", ID: "a-2", Text: "second", Version: 1},
	})
	turnB := testWindowTurnCandidates("turn-b", []appwire.ThreadItem{
		{Type: "agentMessage", ID: "b-1", Text: "middle", Version: 1},
	})
	candidates := []appitempaging.TranscriptItemCandidate{turnA[0], turnB[0], turnA[1]}
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(i)}
		candidates[i].Position = position
		candidates[i].Item.Position = &position
	}
	result := testWindowCandidateResult(candidates)
	result.TurnIDs = []string{"turn-a", "turn-b"}
	options := testWindowPackOptions(appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}})
	options.ItemLimit = 1

	got, err := packThreadTurnsWindowItemCandidates(result, nil, options)
	if err != nil {
		t.Fatalf("pack interleaved logical turn: %v", err)
	}
	if len(got.Data) != 2 || got.Data[0].ID != "turn-a" || got.Data[1].ID != "turn-b" {
		t.Fatalf("packed logical turns = %+v, want turn-a then turn-b once each", got.Data)
	}
	if ids := testItemIDs(got.Data[0].Items); !reflect.DeepEqual(ids, []string{"a-1"}) {
		t.Fatalf("turn-a item ids = %v, want first capped item", ids)
	}
	if elision := got.Data[0].Elision; elision == nil || elision.CapWithheld != 1 || elision.ContinuationCursor == "" {
		t.Fatalf("turn-a elision = %+v, want one withheld item and continuation", elision)
	}
	if got.Window == nil || got.Window.ServedTurns != 2 {
		t.Fatalf("window metadata = %+v, want two served logical turns", got.Window)
	}
}

func TestTranscriptWindowContinuationKeepsInterleavedCapWithinLogicalTurn(t *testing.T) {
	turnA := testWindowTurnCandidates("turn-a", []appwire.ThreadItem{
		{Type: "agentMessage", ID: "a-1", Text: "first", Version: 1},
		{Type: "agentMessage", ID: "a-2", Text: "second", Version: 1},
		{Type: "agentMessage", ID: "a-3", Text: "third", Version: 1},
	})
	turnB := testWindowTurnCandidates("turn-b", []appwire.ThreadItem{
		{Type: "agentMessage", ID: "b-1", Text: "middle", Version: 1},
	})
	candidates := []appitempaging.TranscriptItemCandidate{turnA[0], turnA[1], turnB[0], turnA[2]}
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(i)}
		candidates[i].Position = position
		candidates[i].Item.Position = &position
	}
	result := testWindowCandidateResult(candidates)
	result.TurnIDs = []string{"turn-a", "turn-b"}
	params := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}}
	options := transcriptWindowOptionsFor(
		result, params, 1, transcriptWindowAppliedByServingDaemon,
		appitempaging.FetchCursorOriginServingDaemon,
	)

	initial, err := packThreadTurnsWindowItemCandidates(result, nil, options)
	if err != nil {
		t.Fatalf("pack initial interleaved window: %v", err)
	}
	if got, want := testItemIDs(flattenTestItems(initial.Data)), []string{"a-1", "b-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial item ids = %v, want %v", got, want)
	}
	cursor := initial.Data[0].Elision.ContinuationCursor
	identity := transcriptWindowCursorIdentity(result.Identity, params, 1)
	decoded, err := appitempaging.DecodeWindowCursor(cursor, identity, appitempaging.WindowCursorDirectionForward)
	if err != nil {
		t.Fatalf("decode interleaved cap continuation: %v", err)
	}
	if decoded.AnchorTurnID != "turn-a" || decoded.Position != turnA[1].Position || decoded.ItemVersion != turnA[1].Item.Version || decoded.EmptyRangeDigest != "" {
		t.Fatalf("forward continuation proof = %+v, want first withheld turn-a item", decoded)
	}
	var continuedIDs []string
	for cursor != "" {
		continued, err := spendTranscriptWindowCursor(result, params, 1, cursor)
		if err != nil {
			t.Fatalf("spend interleaved cap continuation: %v", err)
		}
		page, err := packThreadTurnsWindowItemCandidates(continued, nil, transcriptWindowOptionsFor(
			continued, params, 1, transcriptWindowAppliedByServingDaemon,
			appitempaging.FetchCursorOriginServingDaemon,
		))
		if err != nil {
			t.Fatalf("pack interleaved cap continuation: %v", err)
		}
		if len(page.Data) != 1 || page.Data[0].ID != "turn-a" {
			t.Fatalf("continued logical turns = %+v, want only turn-a", page.Data)
		}
		continuedIDs = append(continuedIDs, testItemIDs(page.Data[0].Items)...)
		cursor = ""
		if page.Data[0].Elision != nil {
			cursor = page.Data[0].Elision.ContinuationCursor
		}
	}
	if want := []string{"a-2", "a-3"}; !reflect.DeepEqual(continuedIDs, want) {
		t.Fatalf("continued item ids = %v, want %v without replaying turn-b", continuedIDs, want)
	}
}

func TestTranscriptWindowBackwardContinuationKeepsInterleavedLogicalTurn(t *testing.T) {
	turnA := testWindowTurnCandidates("turn-a", []appwire.ThreadItem{
		{Type: "reasoning", ID: "a-1", Text: strings.Repeat("a", 6_000), Version: 1},
		{Type: "reasoning", ID: "a-2", Text: strings.Repeat("b", 6_000), Version: 1},
	})
	turnC := testWindowTurnCandidates("turn-c", []appwire.ThreadItem{
		{Type: "reasoning", ID: "c-1", Text: strings.Repeat("c", 10_000), Version: 1},
	})
	candidates := []appitempaging.TranscriptItemCandidate{turnA[0], turnC[0], turnA[1]}
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(i)}
		candidates[i].Position = position
		candidates[i].Item.Position = &position
	}
	result := testWindowCandidateResult(candidates)
	result.TurnIDs = []string{"turn-a", "turn-b", "turn-c"}
	result.TurnShells = []appwire.Turn{
		{ID: "turn-a", Status: appwire.TurnStatusCompleted},
		{ID: "turn-b", Status: appwire.TurnStatusCompleted},
		{ID: "turn-c", Status: appwire.TurnStatusCompleted},
	}
	params := appwire.ThreadWindowParams{
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 3}, WindowBudgetBytes: 1,
	}
	options := transcriptWindowOptionsFor(
		result, params, transcriptWindowDefaultItemLimit, transcriptWindowAppliedByServingDaemon,
		appitempaging.FetchCursorOriginServingDaemon,
	)

	initial, err := packThreadTurnsWindowItemCandidates(result, nil, options)
	if err != nil {
		t.Fatalf("pack interleaved budget window: %v", err)
	}
	if got, want := testItemIDs(flattenTestItems(initial.Data)), []string{"c-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial item ids = %v, want %v", got, want)
	}
	if initial.Window == nil || initial.Window.ContinuationCursor == "" {
		t.Fatalf("initial window = %+v, want backward continuation", initial.Window)
	}
	identity := transcriptWindowCursorIdentity(result.Identity, params, transcriptWindowDefaultItemLimit)
	decoded, err := appitempaging.DecodeWindowCursor(initial.Window.ContinuationCursor, identity, appitempaging.WindowCursorDirectionBackward)
	if err != nil {
		t.Fatalf("decode interleaved budget continuation: %v", err)
	}
	if decoded.ResumeWindow == nil || decoded.ResumeWindow.Before == nil || decoded.ResumeWindow.Before.TurnID != "turn-b" {
		t.Fatalf("resume window = %+v, want before empty turn-b", decoded.ResumeWindow)
	}
	if decoded.AnchorTurnID != "turn-c" || decoded.Position != candidates[1].Position || decoded.ItemVersion != candidates[1].Item.Version || decoded.EmptyRangeDigest != "" {
		t.Fatalf("backward continuation proof = %+v, want positioned turn-c anchor", decoded)
	}
	continued, err := spendTranscriptWindowCursor(result, params, transcriptWindowDefaultItemLimit, initial.Window.ContinuationCursor)
	if err != nil {
		t.Fatalf("spend interleaved budget continuation: %v", err)
	}
	continuedIDs := testCandidateIDs(continued.Candidates.Candidates)
	if want := []string{"a-1", "a-2"}; !reflect.DeepEqual(continuedIDs, want) {
		t.Fatalf("continued item ids = %v, want complete logical turn %v", continuedIDs, want)
	}
	if !reflect.DeepEqual(continued.TurnIDs, []string{"turn-a", "turn-b"}) {
		t.Fatalf("continued turn ids = %v, want turn-a plus empty turn-b", continued.TurnIDs)
	}
}

func TestTranscriptWindowForwardContinuationExcludesInterleavedServedTurn(t *testing.T) {
	candidates := testInterleavedLogicalTurnCandidates()
	result := testWindowCandidateResult(candidates)
	result.TurnIDs = []string{"turn-a", "turn-b"}
	params := appwire.ThreadWindowParams{
		TurnWindow: &appwire.ThreadTurnWindow{Since: "turn-before-a", Limit: 2}, WindowBudgetBytes: 1,
	}
	options := transcriptWindowOptionsFor(
		result, params, transcriptWindowDefaultItemLimit, transcriptWindowAppliedByServingDaemon,
		appitempaging.FetchCursorOriginServingDaemon,
	)

	initial, err := packThreadTurnsWindowItemCandidates(result, nil, options)
	if err != nil {
		t.Fatalf("pack interleaved forward budget window: %v", err)
	}
	if got, want := testItemIDs(flattenTestItems(initial.Data)), []string{"a-1", "a-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial item ids = %v, want %v", got, want)
	}
	if initial.Window == nil || initial.Window.ContinuationCursor == "" {
		t.Fatalf("initial window = %+v, want forward continuation", initial.Window)
	}
	continued, err := spendTranscriptWindowCursor(result, params, transcriptWindowDefaultItemLimit, initial.Window.ContinuationCursor)
	if err != nil {
		t.Fatalf("spend interleaved forward continuation: %v", err)
	}
	continuedIDs := testCandidateIDs(continued.Candidates.Candidates)
	if want := []string{"b-1"}; !reflect.DeepEqual(continuedIDs, want) {
		t.Fatalf("continued item ids = %v, want withheld logical turn %v", continuedIDs, want)
	}
	if !reflect.DeepEqual(continued.TurnIDs, []string{"turn-b"}) {
		t.Fatalf("continued turn ids = %v, want turn-b", continued.TurnIDs)
	}
}

func testInterleavedLogicalTurnCandidates() []appitempaging.TranscriptItemCandidate {
	turnA := testWindowTurnCandidates("turn-a", []appwire.ThreadItem{
		{Type: "reasoning", ID: "a-1", Text: strings.Repeat("a", 6_000), Version: 1},
		{Type: "reasoning", ID: "a-2", Text: strings.Repeat("b", 6_000), Version: 1},
	})
	turnB := testWindowTurnCandidates("turn-b", []appwire.ThreadItem{
		{Type: "reasoning", ID: "b-1", Text: strings.Repeat("c", 10_000), Version: 1},
	})
	candidates := []appitempaging.TranscriptItemCandidate{turnA[0], turnB[0], turnA[1]}
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(i)}
		candidates[i].Position = position
		candidates[i].Item.Position = &position
	}
	return candidates
}

func TestPackTranscriptItemWindowRequiresTurnWindow(t *testing.T) {
	_, err := packThreadTurnsWindowItemCandidates(
		testWindowCandidateResult(testWindowTurnCandidates("turn-1", []appwire.ThreadItem{{Type: "agentMessage", ID: "item-1", Text: "x", Version: 1}})),
		nil,
		testWindowPackOptions(appwire.ThreadWindowParams{}),
	)
	assertInvalidItemResponseError(t, err)
}

func TestPackTranscriptItemWindowSharesCursorLineLimitAcrossTruncationAndElision(t *testing.T) {
	items := make([]appwire.ThreadItem, 0, 12)
	for i := range 11 {
		items = append(items, appwire.ThreadItem{Type: "agentMessage", ID: fmt.Sprintf("message-%02d", i), Text: "truncate", Version: uint64(i + 1)})
	}
	items = append(items, appwire.ThreadItem{Type: "reasoning", ID: "elided-beyond-limit", Text: strings.Repeat("s", transcriptWindowBudgetFloorBytes*2), Version: 12})
	options := testWindowPackOptions(appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Turn: "turn-k"},
		MessageBytes:      4,
		WindowBudgetBytes: 1,
	})
	options.ItemLimit = appwire.TranscriptItemPageLimit
	got, err := packThreadTurnsWindowItemCandidates(testWindowCandidateResult(testWindowTurnCandidates("turn-k", items)), nil, options)
	if err != nil {
		t.Fatalf("pack cursor-bound turn: %v", err)
	}
	served := flattenTestItems(got.Data)
	if got, want := len(served), 11; got != want {
		t.Fatalf("served lines = %d, want %d", got, want)
	}
	cursorLines := 0
	for _, item := range served {
		if item.FetchCursor != "" {
			cursorLines++
		}
	}
	if cursorLines != transcriptWindowCursorLineLimit {
		t.Fatalf("cursor-bearing lines = %d, want %d", cursorLines, transcriptWindowCursorLineLimit)
	}
	if got := served[len(served)-1]; got.ID != "message-10" || !got.Truncated || got.FetchCursor != "" {
		t.Fatalf("labeled cursorless truncation line = %+v", got)
	}
	elision := got.Data[0].Elision
	if elision == nil || elision.TruncatedBeyondCursorLimit != 1 || elision.TruncatedBeyondCursorLimitRecovery != appwire.ThreadElisionRecoveryWiderByteBound || elision.ElidedBeyondCursorLimit != 1 || elision.ElidedBeyondCursorLimitRecovery != appwire.ThreadElisionRecoveryContinuation {
		t.Fatalf("cursor overflow elision = %+v", elision)
	}
}

func TestTranscriptWindowContinuationRejectsChangedBoundaryItem(t *testing.T) {
	for _, test := range []struct {
		name          string
		items         []appwire.ThreadItem
		params        appwire.ThreadWindowParams
		itemLimit     int
		boundaryIndex int
	}{
		{
			name: "cap withheld",
			items: func() []appwire.ThreadItem {
				items := make([]appwire.ThreadItem, 12)
				for index := range items {
					items[index] = appwire.ThreadItem{Type: "agentMessage", ID: fmt.Sprintf("cap-%02d", index), Text: "x", Version: 1}
				}
				return items
			}(),
			params:        appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-fence"}},
			itemLimit:     transcriptWindowDefaultItemLimit,
			boundaryIndex: transcriptWindowDefaultItemLimit,
		},
		{
			name: "beyond cursor line limit",
			items: func() []appwire.ThreadItem {
				items := make([]appwire.ThreadItem, 12)
				for index := range 11 {
					items[index] = appwire.ThreadItem{Type: "agentMessage", ID: fmt.Sprintf("message-%02d", index), Text: "truncate", Version: 1}
				}
				items[11] = appwire.ThreadItem{Type: "reasoning", ID: "elided-beyond-limit", Text: strings.Repeat("s", transcriptWindowBudgetFloorBytes*2), Version: 1}
				return items
			}(),
			params: appwire.ThreadWindowParams{
				TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn-fence"}, MessageBytes: 4, WindowBudgetBytes: 1,
			},
			itemLimit:     appwire.TranscriptItemPageLimit,
			boundaryIndex: 11,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates := testWindowTurnCandidates("turn-fence", test.items)
			result := testWindowCandidateResult(candidates)
			options := transcriptWindowOptionsFor(result, test.params, test.itemLimit, transcriptWindowAppliedByServingDaemon, appitempaging.FetchCursorOriginServingDaemon)
			packed, err := packThreadTurnsWindowItemCandidates(result, nil, options)
			if err != nil {
				t.Fatalf("pack version 1 window: %v", err)
			}
			if len(packed.Data) != 1 || packed.Data[0].Elision == nil || packed.Data[0].Elision.ContinuationCursor == "" {
				t.Fatalf("version 1 elision = %+v, want continuation", packed.Data)
			}

			changed := append([]appitempaging.TranscriptItemCandidate(nil), candidates...)
			changed[test.boundaryIndex].Item.Version = 2
			result.Candidates.Candidates = changed
			_, err = spendTranscriptWindowCursor(result, test.params, test.itemLimit, packed.Data[0].Elision.ContinuationCursor)
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok {
				t.Fatalf("changed continuation error = %T %v, want typed item-changed", err, err)
			}
			data, ok := wire.Data.(appwire.TranscriptItemChangedErrorData)
			if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemChanged || data.NewVersion != 2 {
				t.Fatalf("changed continuation error = %+v, want item-changed version 2", wire)
			}
		})
	}
}

func TestPackTranscriptItemWindowBudgetMeasuresEncodedTurnsAndTrimsByMode(t *testing.T) {
	candidates := make([]appitempaging.TranscriptItemCandidate, 0, 3)
	for i := 1; i <= 3; i++ {
		candidates = append(candidates, testWindowTurnCandidates(fmt.Sprintf("turn-%d", i), []appwire.ThreadItem{{
			Type: "reasoning", ID: fmt.Sprintf("item-%d", i), Text: strings.Repeat(string(rune('a'+i)), 10_000), Version: uint64(i),
		}})...)
	}
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(i)}
		candidates[i].Position = position
		candidates[i].Item.Position = &position
	}

	tests := []struct {
		name         string
		window       *appwire.ThreadTurnWindow
		wantIDs      []string
		wantTrimmed  string
		wantFirst    string
		wantLast     string
		wantServed   string
		wantDir      transcriptWindowContinuationDirection
		wantPosition appwire.ThreadItemPosition
	}{
		{name: "latest drops oldest", window: &appwire.ThreadTurnWindow{Latest: 3}, wantIDs: []string{"item-3"}, wantTrimmed: "oldest", wantFirst: "turn-1", wantLast: "turn-2", wantServed: "turn-3", wantDir: transcriptWindowContinuationBackward, wantPosition: appwire.ThreadItemPosition{Entry: 2}},
		{name: "since drops newest", window: &appwire.ThreadTurnWindow{Since: "turn-0", Limit: 3}, wantIDs: []string{"item-1"}, wantTrimmed: "newest", wantFirst: "turn-2", wantLast: "turn-3", wantServed: "turn-1", wantDir: transcriptWindowContinuationForward, wantPosition: appwire.ThreadItemPosition{Entry: 1}},
		{name: "before drops oldest", window: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-4", Limit: 3}}, wantIDs: []string{"item-3"}, wantTrimmed: "oldest", wantFirst: "turn-1", wantLast: "turn-2", wantServed: "turn-3", wantDir: transcriptWindowContinuationBackward, wantPosition: appwire.ThreadItemPosition{Entry: 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var continuation transcriptWindowContinuation
			options := testWindowPackOptions(appwire.ThreadWindowParams{TurnWindow: test.window, WindowBudgetBytes: 1})
			options.MintContinuationCursor = func(request transcriptWindowContinuation) (string, error) {
				continuation = request
				return "window-continuation", nil
			}
			got, err := packThreadTurnsWindowItemCandidates(testWindowCandidateResult(candidates), nil, options)
			if err != nil {
				t.Fatalf("pack: %v", err)
			}
			if ids := testItemIDs(flattenTestItems(got.Data)); !reflect.DeepEqual(ids, test.wantIDs) {
				t.Fatalf("served ids = %v, want %v", ids, test.wantIDs)
			}
			if got.Window == nil {
				t.Fatal("window result is nil")
			}
			if got.Window.EffectiveBudgetBytes != transcriptWindowBudgetFloorBytes || got.Window.ServedTurns != 1 || got.Window.WithheldTurns != 2 || got.Window.FirstServedTurnID != test.wantServed || got.Window.LastServedTurnID != test.wantServed || got.Window.TrimmedEnd != test.wantTrimmed || got.Window.FirstWithheldTurnID != test.wantFirst || got.Window.LastWithheldTurnID != test.wantLast || got.Window.ContinuationCursor != "window-continuation" {
				t.Fatalf("window result = %+v", *got.Window)
			}
			if continuation.Direction != test.wantDir || continuation.TurnID != test.wantFirst || continuation.Position != test.wantPosition {
				t.Fatalf("continuation request = %+v", continuation)
			}
			encodedTurns, err := json.Marshal(got.Data)
			if err != nil {
				t.Fatal(err)
			}
			if len(encodedTurns) > got.Window.EffectiveBudgetBytes {
				t.Fatalf("encoded window content = %d bytes, effective budget = %d", len(encodedTurns), got.Window.EffectiveBudgetBytes)
			}
			encodedResponse, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if len(encodedResponse) > managerReadMaxJSONBytes {
				t.Fatalf("encoded response = %d bytes, outer cap = %d", len(encodedResponse), managerReadMaxJSONBytes)
			}
		})
	}

	defaultBudget, err := packThreadTurnsWindowItemCandidates(testWindowCandidateResult(candidates[:1]), nil, testWindowPackOptions(appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}}))
	if err != nil {
		t.Fatalf("pack default budget: %v", err)
	}
	if defaultBudget.Window == nil || defaultBudget.Window.EffectiveBudgetBytes != transcriptWindowDefaultBudgetBytes {
		t.Fatalf("default window result = %+v", defaultBudget.Window)
	}
	maximumBudgetOptions := testWindowPackOptions(appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Latest: 1},
		WindowBudgetBytes: managerReadMaxJSONBytes,
	})
	maximumBudget, err := packThreadTurnsWindowItemCandidates(testWindowCandidateResult(candidates[:1]), nil, maximumBudgetOptions)
	if err != nil {
		t.Fatalf("pack maximum budget: %v", err)
	}
	if maximumBudget.Window == nil || maximumBudget.Window.EffectiveBudgetBytes != managerReadMaxJSONBytes-transcriptWindowMeasuredEnvelopeMarginBytes {
		t.Fatalf("maximum window result = %+v", maximumBudget.Window)
	}
}

func TestPackTranscriptItemWindowKeepsEmptyBoundaryOnServedPage(t *testing.T) {
	candidates := append(
		testWindowTurnCandidates("turn-1", []appwire.ThreadItem{{
			Type: "reasoning", ID: "item-1", Text: strings.Repeat("a", 10_000), Version: 1,
		}}),
		testWindowTurnCandidates("turn-3", []appwire.ThreadItem{{
			Type: "reasoning", ID: "item-3", Text: strings.Repeat("c", 10_000), Version: 1,
		}})...,
	)
	for index := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(index)}
		candidates[index].Position = position
		candidates[index].Item.Position = &position
	}
	result := testWindowCandidateResult(candidates)
	result.TurnIDs = []string{"turn-1", "turn-2", "turn-3"}
	var continuation transcriptWindowContinuation
	options := testWindowPackOptions(appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Latest: 3},
		WindowBudgetBytes: 1,
	})
	options.MintContinuationCursor = func(request transcriptWindowContinuation) (string, error) {
		continuation = request
		return "window-continuation", nil
	}

	got, err := packThreadTurnsWindowItemCandidates(result, nil, options)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	servedTurnIDs := make([]string, len(got.Data))
	for index := range got.Data {
		servedTurnIDs[index] = got.Data[index].ID
	}
	if !reflect.DeepEqual(servedTurnIDs, []string{"turn-2", "turn-3"}) {
		t.Fatalf("served turns = %v, want empty turn with newest positioned turn", servedTurnIDs)
	}
	if got.Window == nil || got.Window.FirstWithheldTurnID != "turn-1" || got.Window.LastWithheldTurnID != "turn-1" {
		t.Fatalf("window result = %+v, want only oldest positioned turn withheld", got.Window)
	}
	if continuation.Direction != transcriptWindowContinuationBackward || continuation.Position != (appwire.ThreadItemPosition{Entry: 1}) {
		t.Fatalf("continuation = %+v, want boundary at first served item", continuation)
	}
}

func TestPackTranscriptItemWindowMovesTrailingEmptyTurnWithForwardContinuation(t *testing.T) {
	candidates := append(
		testWindowTurnCandidates("turn-1", []appwire.ThreadItem{{
			Type: "reasoning", ID: "item-1", Text: strings.Repeat("a", 10_000), Version: 1,
		}}),
		testWindowTurnCandidates("turn-2", []appwire.ThreadItem{{
			Type: "reasoning", ID: "item-2", Text: strings.Repeat("b", 10_000), Version: 1,
		}})...,
	)
	for index := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(index)}
		candidates[index].Position = position
		candidates[index].Item.Position = &position
	}
	result := testWindowCandidateResult(candidates)
	result.TurnIDs = []string{"turn-1", "turn-2", "turn-3"}
	var continuation transcriptWindowContinuation
	options := testWindowPackOptions(appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Since: "turn-0", Limit: 3},
		WindowBudgetBytes: 1,
	})
	options.MintContinuationCursor = func(request transcriptWindowContinuation) (string, error) {
		continuation = request
		return "window-continuation", nil
	}

	got, err := packThreadTurnsWindowItemCandidates(result, nil, options)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	servedTurnIDs := make([]string, len(got.Data))
	for index := range got.Data {
		servedTurnIDs[index] = got.Data[index].ID
	}
	if !reflect.DeepEqual(servedTurnIDs, []string{"turn-1"}) {
		t.Fatalf("served turns = %v, want oldest positioned turn", servedTurnIDs)
	}
	if got.Window == nil || got.Window.FirstWithheldTurnID != "turn-2" || got.Window.LastWithheldTurnID != "turn-3" {
		t.Fatalf("window result = %+v, want trailing empty turn on withheld page", got.Window)
	}
	if continuation.Direction != transcriptWindowContinuationForward || continuation.Position != (appwire.ThreadItemPosition{Entry: 1}) {
		t.Fatalf("continuation = %+v, want boundary at first withheld item", continuation)
	}
}

func TestPackTranscriptWindowAllEmptyBudgetContinuationUsesDigest(t *testing.T) {
	const shellCount = 200
	shells := make([]appwire.Turn, shellCount)
	turnIDs := make([]string, shellCount)
	for index := range shells {
		turnID := fmt.Sprintf("turn-empty-%03d", index)
		turnIDs[index] = turnID
		shells[index] = appwire.Turn{
			ID: turnID, ItemsView: appwire.TurnItemsViewFragment,
			Status: strings.Repeat("settled-", 16), Version: uint64(index + 1),
		}
	}
	result := testWindowCandidateResult(nil)
	result.TurnIDs = turnIDs
	result.TurnShells = shells
	params := appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Since: "turn-before-empty", Limit: shellCount},
		WindowBudgetBytes: 1,
	}
	options := transcriptWindowOptionsFor(
		result, params, transcriptWindowDefaultItemLimit, transcriptWindowAppliedByServingDaemon,
		appitempaging.FetchCursorOriginServingDaemon,
	)

	got, err := packThreadTurnsWindowItemCandidates(result, nil, options)
	if err != nil {
		t.Fatalf("pack all-empty budget window: %v", err)
	}
	if len(got.Data) == 0 || len(got.Data) >= len(shells) {
		t.Fatalf("served empty turns = %d, want a nonempty bounded prefix of %d", len(got.Data), len(shells))
	}
	for index := range got.Data {
		if got.Data[index].ID != turnIDs[index] {
			t.Fatalf("served turn %d = %q, want ordered prefix %q", index, got.Data[index].ID, turnIDs[index])
		}
	}
	if got.Window == nil || got.Window.ContinuationCursor == "" {
		t.Fatalf("all-empty window = %+v, want continuation", got.Window)
	}
	if len(got.Window.ContinuationCursor) > 4096 {
		t.Fatalf("all-empty cursor length = %d, maximum 4096", len(got.Window.ContinuationCursor))
	}
	identity := transcriptWindowCursorIdentity(result.Identity, params, transcriptWindowDefaultItemLimit)
	cursor, err := appitempaging.DecodeWindowCursor(got.Window.ContinuationCursor, identity, appitempaging.WindowCursorDirectionForward)
	if err != nil {
		t.Fatalf("decode all-empty continuation: %v", err)
	}
	if cursor.EmptyRangeDigest == "" || cursor.AnchorTurnID != "" || cursor.Position != (appwire.ThreadItemPosition{}) || cursor.ItemVersion != 0 {
		t.Fatalf("all-empty continuation proof = %+v, want digest only", cursor)
	}

	digest, err := emptyTranscriptWindowDigest(result)
	if err != nil {
		t.Fatalf("digest all-empty result: %v", err)
	}
	changed := result
	changed.TurnShells = append([]appwire.Turn(nil), result.TurnShells...)
	changed.TurnShells[0].Status += "changed"
	changedDigest, err := emptyTranscriptWindowDigest(changed)
	if err != nil {
		t.Fatalf("digest changed all-empty result: %v", err)
	}
	if changedDigest == digest {
		t.Fatal("all-empty digest did not change with shell state")
	}
}

func TestPackTranscriptItemWindowBudgetsEnrichedImages(t *testing.T) {
	candidates := testWindowCandidateResult(testWindowTurnCandidates("turn-image", []appwire.ThreadItem{{
		Type: "commandExecution", ID: "item-image", Version: 1, Output: "created image",
	}}))
	options := testWindowPackOptions(appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Turn: "turn-image"},
		WindowBudgetBytes: 1,
	})
	options.CanonicalizeCandidates = func(candidates []appitempaging.TranscriptItemCandidate) ([]appitempaging.TranscriptItemCandidate, error) {
		candidates[0].Item.OutputImages = []appwire.OutputImage{{URL: strings.Repeat("u", transcriptWindowBudgetFloorBytes)}}
		return candidates, nil
	}
	got, err := packThreadTurnsWindowItemCandidates(candidates, nil, options)
	if err != nil {
		t.Fatalf("pack enriched image: %v", err)
	}
	encoded, err := json.Marshal(got.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > got.Window.EffectiveBudgetBytes {
		t.Fatalf("encoded enriched window content = %d bytes, effective budget = %d", len(encoded), got.Window.EffectiveBudgetBytes)
	}
}

func TestCanonicalizeTranscriptItemCandidatesOwnsNestedImageState(t *testing.T) {
	candidates := testWindowTurnCandidates("turn-image", []appwire.ThreadItem{{
		Type: "commandExecution", ID: "item-image", Version: 1,
		OutputImages: []appwire.OutputImage{{URL: "source-route"}},
	}})

	canonical, err := canonicalizeTranscriptItemCandidatesWith(appwire.Thread{}, candidates, func(thread appwire.Thread) appwire.Thread {
		thread.Turns[0].Items[0].OutputImages[0].URL = "canonical-route"
		return thread
	})
	if err != nil {
		t.Fatalf("canonicalize candidates: %v", err)
	}
	if got := canonical[0].Item.OutputImages[0].URL; got != "canonical-route" {
		t.Fatalf("canonical image route = %q", got)
	}
	if got := candidates[0].Item.OutputImages[0].URL; got != "source-route" {
		t.Fatalf("source image route was mutated to %q", got)
	}
}

func TestSpendTranscriptWindowCursorKeepsTheRequestedViewAndDirection(t *testing.T) {
	candidates := testItemCandidates(5)
	for index := range candidates {
		candidates[index].Item.Version = 1
	}
	result := transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
		Identity:   appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "window", ProjectionVersion: 1},
		Exhausted:  true,
	}
	params := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 1}, ItemTypes: []string{"messages"}}
	identity := result.Identity
	identity.Incarnation = params.ViewIncarnation(identity.Incarnation, transcriptWindowDefaultItemLimit)

	forward, err := appitempaging.EncodeWindowCursor(identity, appitempaging.WindowCursorDirectionForward, appitempaging.WindowCursor{
		AnchorTurnID: "turn-1", Position: candidates[2].Position, ItemVersion: candidates[2].Item.Version,
		ResumeWindow: &appwire.ThreadTurnWindow{Turn: "turn-forward"},
	})
	if err != nil {
		t.Fatal(err)
	}
	forwardPage, err := spendTranscriptWindowCursor(result, params, transcriptWindowDefaultItemLimit, forward)
	if err != nil {
		t.Fatalf("spend forward cursor: %v", err)
	}
	if got := testCandidateIDs(forwardPage.Candidates.Candidates); !reflect.DeepEqual(got, []string{"item-02", "item-03", "item-04"}) {
		t.Fatalf("forward continuation = %v, want first withheld item through newest", got)
	}

	backward, err := appitempaging.EncodeWindowCursor(identity, appitempaging.WindowCursorDirectionBackward, appitempaging.WindowCursor{
		AnchorTurnID: "turn-1", Position: candidates[3].Position, ItemVersion: candidates[3].Item.Version,
		ResumeWindow: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-boundary", Limit: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backwardPage, err := spendTranscriptWindowCursor(result, params, transcriptWindowDefaultItemLimit, backward)
	if err != nil {
		t.Fatalf("spend backward cursor: %v", err)
	}
	if got := testCandidateIDs(backwardPage.Candidates.Candidates); !reflect.DeepEqual(got, []string{"item-00", "item-01", "item-02"}) {
		t.Fatalf("backward continuation = %v, want items strictly older than served boundary", got)
	}

	otherView := params
	otherView.ItemTypes = []string{"reasoning"}
	if _, err := spendTranscriptWindowCursor(result, otherView, transcriptWindowDefaultItemLimit, forward); !errors.Is(err, appwire.TranscriptItemCursorStale()) {
		t.Fatalf("cross-view cursor error = %v, want stale", err)
	}
}

func TestPackTranscriptItemWindowAlwaysServesOneBoundedFragment(t *testing.T) {
	items := []appwire.ThreadItem{
		{Type: "reasoning", ID: "settled", Text: strings.Repeat("x", transcriptWindowBudgetFloorBytes*2), Version: 1},
		{Type: "reasoning", ID: "in-flight", Text: strings.Repeat("y", transcriptWindowBudgetFloorBytes*2)},
	}
	options := testWindowPackOptions(appwire.ThreadWindowParams{
		TurnWindow:        &appwire.ThreadTurnWindow{Turn: "turn-large"},
		WindowBudgetBytes: 1,
	})
	got, err := packThreadTurnsWindowItemCandidates(testWindowCandidateResult(testWindowTurnCandidates("turn-large", items)), nil, options)
	if err != nil {
		t.Fatalf("pack oversized turn: %v", err)
	}
	if len(got.Data) != 1 {
		t.Fatalf("served turn fragments = %d, want 1", len(got.Data))
	}
	served := flattenTestItems(got.Data)
	if len(served) != 2 {
		t.Fatalf("served item lines = %d, want 2", len(served))
	}
	if settled := served[0]; settled.Text != "" || !settled.Truncated || settled.TruncatedBytes != transcriptWindowBudgetFloorBytes*2 || settled.FetchCursor != "fetch-settled" || settled.InFlight {
		t.Fatalf("settled bodyless line = %+v", settled)
	}
	if inFlight := served[1]; inFlight.Text != "" || !inFlight.Truncated || inFlight.TruncatedBytes != transcriptWindowBudgetFloorBytes*2 || inFlight.FetchCursor != "" || !inFlight.InFlight {
		t.Fatalf("in-flight bodyless line = %+v", inFlight)
	}
	encoded, err := json.Marshal(got.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > got.Window.EffectiveBudgetBytes {
		t.Fatalf("bodyless fragment = %d bytes, effective budget = %d", len(encoded), got.Window.EffectiveBudgetBytes)
	}
}

func TestPackTranscriptItemLegacyWrappersKeepExactBytes(t *testing.T) {
	candidates := testItemCandidates(45)
	identity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "legacy-parity", ProjectionVersion: 1}
	result := transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
		Identity:   identity,
		Exhausted:  true,
	}
	selected := append([]appitempaging.TranscriptItemCandidate(nil), candidates[5:]...)
	selected = appitempaging.NormalizeProjectedItemCompleteness(selected)
	wantTurns, err := appitempaging.RegroupTurnFragments(selected)
	if err != nil {
		t.Fatal(err)
	}
	wantCursor, err := appitempaging.EncodeCursor(identity, candidates[5].Position)
	if err != nil {
		t.Fatal(err)
	}
	wantRead, err := json.Marshal(appwire.ThreadReadResponse{Thread: appwire.Thread{Turns: wantTurns}, OlderCursor: wantCursor})
	if err != nil {
		t.Fatal(err)
	}
	wantList, err := json.Marshal(appwire.ThreadTurnsListResponse{Data: wantTurns, NextCursor: wantCursor})
	if err != nil {
		t.Fatal(err)
	}
	read, err := packThreadReadItemCandidates(result, nil)
	if err != nil {
		t.Fatalf("pack legacy read: %v", err)
	}
	list, err := packThreadTurnsItemCandidates(result, nil)
	if err != nil {
		t.Fatalf("pack legacy list: %v", err)
	}
	gotRead, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	gotList, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRead, wantRead) {
		t.Fatalf("legacy read bytes changed:\n got %s\nwant %s", gotRead, wantRead)
	}
	if !bytes.Equal(gotList, wantList) {
		t.Fatalf("legacy list bytes changed:\n got %s\nwant %s", gotList, wantList)
	}
}

func testWindowCandidateResult(candidates []appitempaging.TranscriptItemCandidate) transcriptItemCandidateResult {
	return transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
		Identity:   appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "window", ProjectionVersion: 1},
		Exhausted:  true,
	}
}

func testWindowTurnCandidates(turnID string, items []appwire.ThreadItem) []appitempaging.TranscriptItemCandidate {
	turn := appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted}
	candidates := make([]appitempaging.TranscriptItemCandidate, len(items))
	for i := range items {
		position := appwire.ThreadItemPosition{Entry: uint64(i), Item: 0}
		if len(items[i].TranscriptKey) == 0 {
			items[i].TranscriptKey = "key-" + items[i].ID
		}
		items[i].Position = &position
		items[i].Status = appwire.TurnStatusCompleted
		candidates[i] = appitempaging.TranscriptItemCandidate{TurnID: turnID, Turn: turn, Item: items[i], Position: position}
	}
	return candidates
}

func testWindowPackOptions(params appwire.ThreadWindowParams) transcriptWindowPackOptions {
	return transcriptWindowPackOptions{
		Params:    params,
		AppliedBy: "saved-index",
		MintFetchCursor: func(candidate appitempaging.TranscriptItemCandidate) (string, error) {
			return "fetch-" + candidate.Item.ID, nil
		},
		MintContinuationCursor: func(request transcriptWindowContinuation) (string, error) {
			return "continue-" + request.TurnID, nil
		},
	}
}

func testItemIDs(items []appwire.ThreadItem) []string {
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	return ids
}

func testCandidateIDs(candidates []appitempaging.TranscriptItemCandidate) []string {
	ids := make([]string, len(candidates))
	for i := range candidates {
		ids[i] = candidates[i].Item.ID
	}
	return ids
}

func testItemCandidates(count int) []appitempaging.TranscriptItemCandidate {
	turn := appwire.Turn{ID: "turn-1", Status: appwire.TurnStatusCompleted}
	candidates := make([]appitempaging.TranscriptItemCandidate, count)
	for i := range candidates {
		position := appwire.ThreadItemPosition{Entry: uint64(i), Item: 0}
		item := appwire.ThreadItem{
			Type:          "agentMessage",
			ID:            fmt.Sprintf("item-%02d", i),
			TranscriptKey: fmt.Sprintf("key-%02d", i),
			Position:      &position,
			Text:          fmt.Sprintf("text-%02d", i),
			Status:        appwire.TurnStatusCompleted,
		}
		candidates[i] = appitempaging.TranscriptItemCandidate{
			TurnID:   turn.ID,
			Turn:     turn,
			Item:     item,
			Position: position,
		}
	}
	return candidates
}

func flattenTestItems(turns []appwire.Turn) []appwire.ThreadItem {
	var items []appwire.ThreadItem
	for _, turn := range turns {
		items = append(items, turn.Items...)
	}
	return items
}

func findTextLengthForResultSize(t *testing.T, candidate appitempaging.TranscriptItemCandidate, want int) int {
	t.Helper()
	low, high := 0, want
	for low <= high {
		middle := low + (high-low)/2
		candidate.Item.Text = strings.Repeat("x", middle)
		response, err := packThreadReadItemCandidates(transcriptItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: []appitempaging.TranscriptItemCandidate{candidate}},
			Identity:   appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "size-search", ProjectionVersion: 1},
			Exhausted:  true,
		}, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
			return response, nil
		})
		if err != nil {
			t.Fatalf("size search pack: %v", err)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) < want {
			low = middle + 1
			continue
		}
		if len(encoded) > want {
			high = middle - 1
			continue
		}
		return middle
	}
	t.Fatalf("could not construct a result of exactly %d bytes", want)
	return 0
}
