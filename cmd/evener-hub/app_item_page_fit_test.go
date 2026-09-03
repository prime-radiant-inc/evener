package hub

import (
	"encoding/json"
	"fmt"
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
	if got.PageUnit != appwire.TranscriptPageUnitItem {
		t.Fatalf("page unit = %q, want item", got.PageUnit)
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
