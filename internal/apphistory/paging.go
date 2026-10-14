// Package apphistory selects the history a client receives while leaving the
// shared producer's complete transcript and subscription stream intact.
package apphistory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

type ReadFunc func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error)
type ListFunc func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error)

const cursorPrefix = "content1."
const snapshotPrefix = "content1:"
const pageContentBytes = 1 << 20

type contentCursor struct {
	Selection      string                     `json:"selection"`
	Snapshot       appwire.SnapshotIdentity   `json:"snapshot"`
	Ref            string                     `json:"ref,omitempty"`
	ThreadID       string                     `json:"thread_id,omitempty"`
	BootGeneration string                     `json:"boot_generation,omitempty"`
	Epoch          uint64                     `json:"epoch"`
	Before         appwire.ThreadItemPosition `json:"before"`
}

// Snapshot binds an opaque incarnation to its selected representation. A
// consumer's ordinary incarnation fence therefore also fences view changes.
func Snapshot(snapshot *appwire.SnapshotIdentity, selection appwire.ThreadContentSelection) *appwire.SnapshotIdentity {
	if snapshot == nil || selection == 0 {
		return snapshot
	}
	selected := *snapshot
	selected.Incarnation = snapshotPrefix + selection.Key() + ":" + base64.RawURLEncoding.EncodeToString([]byte(snapshot.Incarnation))
	return &selected
}

func sourceSnapshot(held *appwire.SnapshotIdentity, selection appwire.ThreadContentSelection) *appwire.SnapshotIdentity {
	if held == nil {
		return nil
	}
	if selection == 0 {
		if strings.HasPrefix(held.Incarnation, snapshotPrefix) {
			return nil
		}
		return held
	}
	prefix := snapshotPrefix + selection.Key() + ":"
	if !strings.HasPrefix(held.Incarnation, prefix) {
		return nil
	}
	incarnation, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(held.Incarnation, prefix))
	if err != nil || len(incarnation) == 0 {
		return nil
	}
	source := *held
	source.Incarnation = string(incarnation)
	return &source
}

// Read fills the public selected window from complete indexed source pages.
// Internal reads deliberately request full content: their subscription can be
// shared by downstream clients with different selections.
func Read(ctx context.Context, params appwire.ThreadReadParams, read ReadFunc, list ListFunc) (appwire.ThreadReadResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	if err := appwire.ValidateThreadReadParams(params); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	selection := params.Selection()
	source := params
	source.ThreadContentParams = appwire.ThreadContentParams{}
	source.HeldSnapshot = sourceSnapshot(params.HeldSnapshot, selection)
	if selection != 0 && params.IncludeTurns {
		source.ItemLimit = appwire.TranscriptItemPageLimit
	}
	response, err := read(ctx, source)
	if err != nil || selection == 0 {
		return response, err
	}
	sourceWindow := response.Thread.Turns
	if params.IncludeTurns {
		page := appwire.ThreadTurnsListResponse{Data: response.Thread.Turns, NextCursor: response.OlderCursor, Snapshot: response.Snapshot, BootGeneration: response.BootGeneration, Epoch: response.Epoch, Authoritative: response.Authoritative}
		page, err = fill(ctx, appwire.ThreadTurnsListParams{ThreadContentParams: params.ThreadContentParams, Ref: params.Ref, ThreadID: params.ThreadID, ItemLimit: params.ItemLimit, ItemsView: params.ItemsView}, page, list)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		response.Thread.Turns, response.OlderCursor = page.Data, page.NextCursor
	}
	if !selection.Diagnostics() {
		response.Thread.Evener.Diagnostics = nil
	}
	response.Snapshot = Snapshot(response.Snapshot, selection)
	response.Overlay = SelectOverlay(response.Overlay, selection)
	if response.Changes != nil {
		changes := *response.Changes
		changes.Items = selection.Items(changes.Items)
		changes.Turns = slices.Clone(changes.Turns)
		// The producer excludes its own latest window from Changes. Items
		// dropped by our smaller selected/byte-limited window still need to
		// update a client's previously loaded older pages.
		kept := make(map[string]bool)
		keptTurns := make(map[string]bool)
		for _, turn := range response.Thread.Turns {
			keptTurns[turn.ID] = true
			for _, item := range turn.Items {
				kept[item.TranscriptKey] = true
			}
		}
		for _, item := range changes.Items {
			kept[item.TranscriptKey] = true
		}
		for _, turn := range changes.Turns {
			keptTurns[turn.ID] = true
		}
		for _, turn := range sourceWindow {
			if !keptTurns[turn.ID] {
				scalars := turn
				scalars.Items = nil
				changes.Turns = append(changes.Turns, scalars)
				keptTurns[turn.ID] = true
			}
			for _, item := range turn.Items {
				if selected, keep := selection.Item(item); keep && !kept[item.TranscriptKey] {
					changes.Items = append(changes.Items, selected)
					kept[item.TranscriptKey] = true
				}
			}
		}
		response.Changes = &changes
	}
	return response, nil
}

func List(ctx context.Context, params appwire.ThreadTurnsListParams, list ListFunc) (appwire.ThreadTurnsListResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	if err := appwire.ValidateThreadTurnsListParams(params); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	selection := params.Selection()
	source := params
	source.ThreadContentParams = appwire.ThreadContentParams{}
	var fence *contentCursor
	if strings.HasPrefix(params.Cursor, cursorPrefix) {
		cursor, err := decodeCursor(params.Cursor, selection)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		if cursor.Ref != strings.TrimSpace(params.Ref) || cursor.ThreadID != strings.TrimSpace(params.ThreadID) {
			return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
		}
		fence = &cursor
		source.Cursor = ""
		if source.Before == nil {
			source.Before = &cursor.Before
		}
	} else if params.Cursor != "" && selection != 0 {
		return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
	}
	if selection != 0 {
		source.ItemLimit = appwire.TranscriptItemPageLimit
	}
	page, err := list(ctx, source)
	if err != nil {
		return page, err
	}
	if fence != nil && (!sameSnapshot(&fence.Snapshot, page.Snapshot) || fence.BootGeneration != page.BootGeneration || fence.Epoch != page.Epoch) {
		return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
	}
	if selection == 0 {
		return page, nil
	}
	page, err = fill(ctx, params, page, list)
	page.Snapshot = Snapshot(page.Snapshot, selection)
	return page, err
}

func fill(ctx context.Context, params appwire.ThreadTurnsListParams, page appwire.ThreadTurnsListResponse, list ListFunc) (appwire.ThreadTurnsListResponse, error) {
	selection := params.Selection()
	limit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	result := page
	var selected []appitempaging.TranscriptItemCandidate
	var oldest *appwire.ThreadItemPosition
	// One extra selected item proves whether another selected page exists.
	// Memory is bounded by two raw pages plus limit+1 selected items.
	for selection.HasItems() {
		if err := ctx.Err(); err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		candidates, err := appitempaging.CandidatesFromTurns(page.Data)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		if err := appitempaging.ValidateCandidates(candidates); err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		if oldest != nil && len(candidates) > 0 && !positionBefore(candidates[len(candidates)-1].Position, *oldest) {
			return appwire.ThreadTurnsListResponse{}, errors.New("history source did not advance its page boundary")
		}
		if len(candidates) > 0 {
			p := candidates[0].Position
			oldest = &p
		}
		for i := len(candidates) - 1; i >= 0 && len(selected) <= limit; i-- {
			if item, keep := selection.Item(candidates[i].Item); keep {
				candidate := candidates[i]
				candidate.Item = item
				candidate.Turn.Items = nil
				selected = append(selected, candidate)
			}
		}
		if len(selected) > limit || page.NextCursor == "" {
			break
		}
		if len(candidates) == 0 {
			return appwire.ThreadTurnsListResponse{}, errors.New("history source returned an empty continuation page")
		}
		page, err = list(ctx, appwire.ThreadTurnsListParams{Ref: params.Ref, ThreadID: params.ThreadID, ItemsView: params.ItemsView, ItemLimit: appwire.TranscriptItemPageLimit, Before: oldest})
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		if !sameSnapshot(result.Snapshot, page.Snapshot) || result.BootGeneration != page.BootGeneration || result.Epoch != page.Epoch {
			return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
		}
	}
	older := len(selected) > limit
	if older {
		selected = selected[:limit]
	}
	slices.Reverse(selected)
	// Item bytes alone are a lower bound on the encoded page. Measure each
	// payload once so oversized windows do not repeatedly clone and encode
	// large tool outputs while discarding their oldest items.
	itemSizes := make([]int, len(selected))
	itemBytes := 0
	for i, candidate := range selected {
		encoded, err := json.Marshal(candidate.Item)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		itemSizes[i] = len(encoded)
		itemBytes += len(encoded)
	}
	for itemBytes > pageContentBytes && len(selected) > 1 {
		itemBytes -= itemSizes[0]
		itemSizes = itemSizes[1:]
		selected = selected[1:]
		older = true
	}
	for {
		result.Data, err = appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(selected))
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		encoded, err := json.Marshal(result.Data)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		if len(encoded) <= pageContentBytes || len(selected) <= 1 {
			break
		}
		selected = selected[1:]
		older = true
	}
	result.NextCursor = ""
	if older {
		if result.Snapshot == nil {
			return appwire.ThreadTurnsListResponse{}, errors.New("selected history needs a source snapshot")
		}
		// Retained source cursors can expire while a sparse page is filled.
		// Page by position and fence the returned source history instead.
		cursor := contentCursor{Selection: selection.Key(), Ref: strings.TrimSpace(params.Ref), ThreadID: strings.TrimSpace(params.ThreadID), Snapshot: *result.Snapshot, BootGeneration: result.BootGeneration, Epoch: result.Epoch, Before: selected[0].Position}
		raw, err := json.Marshal(cursor)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		result.NextCursor = cursorPrefix + base64.RawURLEncoding.EncodeToString(raw)
	}
	return result, nil
}

func decodeCursor(encoded string, selection appwire.ThreadContentSelection) (contentCursor, error) {
	var cursor contentCursor
	if len(encoded) > 1<<20 {
		return cursor, appwire.TranscriptItemCursorStale()
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, cursorPrefix))
	if err != nil {
		return cursor, appwire.TranscriptItemCursorStale()
	}
	if err = json.Unmarshal(raw, &cursor); err != nil || cursor.Selection == "" || cursor.Selection != selection.Key() || cursor.Snapshot.Incarnation == "" {
		return contentCursor{}, appwire.TranscriptItemCursorStale()
	}
	return cursor, nil
}

func sameSnapshot(a, b *appwire.SnapshotIdentity) bool {
	return a != nil && b != nil && a.Incarnation == b.Incarnation && b.Length >= a.Length
}
func positionBefore(a, b appwire.ThreadItemPosition) bool {
	return appitempaging.ComparePosition(a, b) < 0
}

func SelectOverlay(items []appwire.OverlayItem, selection appwire.ThreadContentSelection) []appwire.OverlayItem {
	if selection == 0 {
		return items
	}
	selected := make([]appwire.OverlayItem, 0, len(items))
	for _, overlay := range items {
		if item, keep := selection.Item(overlay.Item); keep {
			overlay.Item = item
			selected = append(selected, overlay)
		}
	}
	return selected
}
