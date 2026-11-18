package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// boundedPageItems builds the newest page of an itemCount-item session: the
// last pageSize items a daemon page limit of pageSize would return.
func boundedPageItems(itemCount, pageSize uint64) []appwire.ThreadItem {
	return positionedItems(itemCount-pageSize+1, itemCount)
}

// nativeHistoryCandidates projects a full transcript into the positioned
// candidates a native history page selects from.
func nativeHistoryCandidates(t *testing.T, items []appwire.ThreadItem) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	candidates, err := localDaemonItemCandidates([]appwire.Turn{itemFragmentTurn(items)})
	if err != nil {
		t.Fatalf("build native history: %v", err)
	}
	return candidates
}

// nativeHistoryPage answers a thread/turns/list call the way a real daemon does
// for a long session: ItemLimit items strictly older than the request cursor,
// with the next cursor naming the page's first item, and an empty next cursor at
// the transcript's start.
func nativeHistoryPage(all []appitempaging.TranscriptItemCandidate, identity appitempaging.CursorIdentity) func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return func(_ int, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		before, err := appitempaging.DecodeCursor(params.Cursor, identity)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, appwire.InvalidParams(err.Error())
		}
		selected, hasOlder, err := appitempaging.SelectCandidates(all, &before, params.ItemLimit)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		next := ""
		if hasOlder {
			if next, err = appitempaging.EncodeCursor(identity, selected[0].Position); err != nil {
				return appwire.ThreadTurnsListResponse{}, err
			}
		}
		items := make([]appwire.ThreadItem, len(selected))
		for index, candidate := range selected {
			items[index] = candidate.Item
		}
		return itemPageResponse(items, next), nil
	}
}

// TestOverCapHiddenPrefixServesTheNewestPage pins that an over-cap hidden prefix
// still serves the page the daemon returned.
//
// A session's first read returns its whole transcript, so the hub retains a
// complete item snapshot. The transcript then grows far past the daemon page, so
// every later read returns only a bounded page whose hidden prefix is longer than
// the walk authenticates. That read still serves the daemon's newest page, under
// the rotated identity: refreshLocalDaemonItemSnapshot keeps the page in its
// snapshot, so a read answers with its candidates rather than zero candidates and
// no older cursor, which the hub's caller would read as an empty live thread and
// answer from the saved transcript.
func TestOverCapHiddenPrefixServesTheNewestPage(t *testing.T) {
	t.Parallel()
	const (
		itemCount = 400
		pageSize  = uint64(40)
	)
	for _, entry := range []struct {
		name string
		read func(context.Context, *LocalDaemonSource) (ItemCandidateResult, error)
	}{
		{
			name: "read item candidates",
			read: func(ctx context.Context, source *LocalDaemonSource) (ItemCandidateResult, error) {
				return source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: "local:thread"})
			},
		},
		{
			name: "cursor-less list item candidates",
			read: func(ctx context.Context, source *LocalDaemonSource) (ItemCandidateResult, error) {
				return source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread"})
			},
		},
	} {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()
			all := positionedItems(1, itemCount)
			newest := boundedPageItems(itemCount, pageSize)
			olderCursor := inverseCursor(t, itemNativeIdentity, itemPosition(itemCount-pageSize+1))
			daemon := &scriptedItemDaemon{
				read: func(call int, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					if call == 0 {
						return itemReadResponse(all, ""), nil
					}
					return itemReadResponse(newest, olderCursor), nil
				},
				page: nativeHistoryPage(nativeHistoryCandidates(t, all), itemNativeIdentity),
			}
			source := newScriptedItemSource(t, daemon, "thread")
			ctx := t.Context()

			if _, err := entry.read(ctx, source); err != nil {
				t.Fatalf("complete initial read: %v", err)
			}
			bounded, err := entry.read(ctx, source)
			if err != nil {
				t.Fatalf("bounded read: %v", err)
			}
			if len(bounded.Candidates.Candidates) != len(newest) {
				t.Fatalf("bounded read served %d candidates, want the daemon's newest %d-item page: the page was dropped",
					len(bounded.Candidates.Candidates), len(newest))
			}
			for index, candidate := range bounded.Candidates.Candidates {
				if candidate.Item.TranscriptKey != newest[index].TranscriptKey {
					t.Fatalf("bounded read item %d = %q, want the newest page's %q",
						index, candidate.Item.TranscriptKey, newest[index].TranscriptKey)
				}
			}
		})
	}
}

// TestOverCapReadRotatesIdentityOnce pins that an over-cap conversion rotates the
// identity exactly once.
//
// ItemCandidatesFromRead stores the rotated state from the bounded page the
// daemon returned, so that state carries the page's extent: the read that follows
// extends it rather than rotating again, and a cursor the first post-cap read
// minted still resolves after the next read.
func TestOverCapReadRotatesIdentityOnce(t *testing.T) {
	t.Parallel()
	const (
		itemCount = 400
		pageSize  = uint64(40)
	)
	all := positionedItems(1, itemCount)
	newest := boundedPageItems(itemCount, pageSize)
	olderCursor := inverseCursor(t, itemNativeIdentity, itemPosition(itemCount-pageSize+1))
	daemon := &scriptedItemDaemon{
		read: unexpectedReadCall,
		page: nativeHistoryPage(nativeHistoryCandidates(t, all), itemNativeIdentity),
	}
	source := newScriptedItemSource(t, daemon, "thread")
	params := appwire.ThreadReadParams{Ref: "local:thread"}
	ctx := t.Context()

	complete, err := source.ItemCandidatesFromRead(ctx, params, itemReadResponse(all, ""))
	if err != nil {
		t.Fatalf("complete initial read: %v", err)
	}
	first, err := source.ItemCandidatesFromRead(ctx, params, itemReadResponse(newest, olderCursor))
	if err != nil {
		t.Fatalf("first over-cap read: %v", err)
	}
	if len(first.Candidates.Candidates) != len(newest) {
		t.Fatalf("first over-cap read served %d candidates, want the daemon's newest %d-item page",
			len(first.Candidates.Candidates), len(newest))
	}
	if first.Identity == complete.Identity {
		t.Fatal("an over-cap read kept the identity of the span it could not authenticate")
	}
	// The cursor the first post-cap read minted must survive the read that
	// follows it, which is what a single rotation guarantees.
	outward, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode post-cap cursor: %v", err)
	}
	if _, err := source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: outward, ItemLimit: int(pageSize)}); err != nil {
		t.Fatalf("post-cap cursor continuation: %v", err)
	}
	second, err := source.ItemCandidatesFromRead(ctx, params, itemReadResponse(newest, olderCursor))
	if err != nil {
		t.Fatalf("second over-cap read: %v", err)
	}
	if second.Identity != first.Identity {
		t.Fatalf("two consecutive over-cap reads rotated twice: identity %+v then %+v",
			first.Identity, second.Identity)
	}
	if len(second.Candidates.Candidates) != len(newest) {
		t.Fatalf("second over-cap read served %d candidates, want %d",
			len(second.Candidates.Candidates), len(newest))
	}
	if _, err := source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: outward, ItemLimit: int(pageSize)}); err != nil {
		t.Fatalf("post-cap cursor after the following read: %v", err)
	}
}

// TestHiddenPrefixBandRotatesAtMostOnce pins the band just past the prefix cap.
//
// The prefix walk bounds the hidden history below the daemon page; the advance
// walk then re-reads from the retained span's last item, so it covers the visible
// page on top of that prefix. The advance walk's bound carries that page, so a
// session at this size keeps one identity across both walks: two consecutive
// bounded reads agree on the same identity and pay only those two walks.
func TestHiddenPrefixBandRotatesAtMostOnce(t *testing.T) {
	t.Parallel()
	const (
		itemCount = 170
		pageSize  = uint64(40)
	)
	all := positionedItems(1, itemCount)
	newest := boundedPageItems(itemCount, pageSize)
	daemon := &scriptedItemDaemon{
		read: func(call int, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if call == 0 {
				return itemReadResponse(all, ""), nil
			}
			return itemReadResponse(newest, inverseCursor(t, itemNativeIdentity, itemPosition(itemCount-pageSize+1))), nil
		},
		page: nativeHistoryPage(nativeHistoryCandidates(t, all), itemNativeIdentity),
	}
	source := newScriptedItemSource(t, daemon, "thread")
	ctx := t.Context()

	if _, err := source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: "local:thread"}); err != nil {
		t.Fatalf("complete initial read: %v", err)
	}
	first, err := readAgain(ctx, source)
	if err != nil {
		t.Fatalf("first bounded read: %v", err)
	}
	second, err := readAgain(ctx, source)
	if err != nil {
		t.Fatalf("second bounded read: %v", err)
	}
	if second.Identity != first.Identity {
		t.Fatalf("a session at %d items with a %d-item page rotated on both reads: %+v then %+v",
			itemCount, pageSize, first.Identity, second.Identity)
	}
	if len(second.Candidates.Candidates) != len(newest) {
		t.Fatalf("second bounded read served %d candidates, want %d",
			len(second.Candidates.Candidates), len(newest))
	}
}
