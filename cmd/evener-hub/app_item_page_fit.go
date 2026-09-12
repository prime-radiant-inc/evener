package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/internal/appitempaging"
)

const transcriptRPCResultSoftLimit = 1 << 20

// transcriptItemCandidateResult is the private boundary between a source's
// positioned item projection and the public AppWire response. None of these
// fields are serialized into the response.
type transcriptItemCandidateResult struct {
	Candidates appitempaging.TranscriptItemWindow
	Identity   appitempaging.CursorIdentity
	Exhausted  bool
}

func transcriptItemCandidateResultFromSource(result appsource.ItemCandidateResult) transcriptItemCandidateResult {
	return transcriptItemCandidateResult{
		Candidates: result.Candidates,
		Identity:   result.Identity,
		Exhausted:  result.Exhausted,
	}
}

func itemCandidateResultFromReadResponse(response appwire.ThreadReadResponse) (transcriptItemCandidateResult, error) {
	return itemCandidateResultFromTurns(response.Thread.Turns, response.OlderCursor)
}

func itemCandidateResultFromTurns(turns []appwire.Turn, olderCursor string) (transcriptItemCandidateResult, error) {
	candidates, err := appitempaging.CandidatesFromTurns(turns)
	if err != nil {
		return transcriptItemCandidateResult{}, err
	}
	return transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{
			Candidates:  candidates,
			OlderCursor: olderCursor,
		},
		Exhausted: olderCursor == "",
	}, nil
}

func sourceItemCandidateResultForRead(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, response appwire.ThreadReadResponse) (transcriptItemCandidateResult, error) {
	if itemSource, ok := source.(appsource.ItemReadCandidateSource); ok {
		result, err := itemSource.ItemCandidatesFromRead(ctx, params, response)
		if err != nil {
			return transcriptItemCandidateResult{}, err
		}
		return transcriptItemCandidateResultFromSource(result), nil
	}
	return itemCandidateResultFromReadResponse(response)
}

func sourceItemCandidateResultForList(ctx context.Context, source appsource.Source, params appwire.ThreadTurnsListParams, response appwire.ThreadTurnsListResponse) (transcriptItemCandidateResult, error) {
	if itemSource, ok := source.(appsource.ItemCandidateSource); ok {
		result, err := itemSource.ListItemCandidates(ctx, params)
		if err != nil {
			return transcriptItemCandidateResult{}, err
		}
		return transcriptItemCandidateResultFromSource(result), nil
	}
	return itemCandidateResultFromTurns(response.Data, response.NextCursor)
}

func threadWithPackedTurns(base appwire.Thread, turns []appwire.Turn) appwire.Thread {
	base.Turns = turns
	return base
}

func packThreadReadItemCandidates(
	candidates transcriptItemCandidateResult,
	enrich func(appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error),
	requestedLimit ...int,
) (appwire.ThreadReadResponse, error) {
	response, err := packItemCandidates(candidates, enrich, func(turns []appwire.Turn, olderCursor string) appwire.ThreadReadResponse {
		return appwire.ThreadReadResponse{
			Thread:      appwire.Thread{Turns: turns},
			OlderCursor: olderCursor,
		}
	}, requestedLimit...)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	if err := appwire.ValidateThreadReadItemResponse(response); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return response, nil
}

func packThreadTurnsItemCandidates(
	candidates transcriptItemCandidateResult,
	enrich func(appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error),
	requestedLimit ...int,
) (appwire.ThreadTurnsListResponse, error) {
	response, err := packItemCandidates(candidates, enrich, func(turns []appwire.Turn, olderCursor string) appwire.ThreadTurnsListResponse {
		return appwire.ThreadTurnsListResponse{
			Data:       turns,
			NextCursor: olderCursor,
		}
	}, requestedLimit...)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	if err := appwire.ValidateThreadTurnsListItemResponse(response); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	return response, nil
}

func packItemCandidates[T any](
	result transcriptItemCandidateResult,
	enrich func(T) (T, error),
	build func([]appwire.Turn, string) T,
	requestedLimit ...int,
) (T, error) {
	var zero T
	candidates := append([]appitempaging.TranscriptItemCandidate(nil), result.Candidates.Candidates...)
	if _, err := appitempaging.RegroupTurnFragments(candidates); err != nil {
		return zero, fmt.Errorf("invalid transcript item candidates: %w", err)
	}
	if err := validateSourceItemContinuation(result, candidates); err != nil {
		return zero, err
	}

	itemLimit := appwire.TranscriptItemPageLimit
	if len(requestedLimit) > 0 {
		itemLimit = requestedLimit[0]
	}
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(itemLimit)
	if err != nil {
		return zero, err
	}
	selected, hasOlder, err := appitempaging.SelectCandidates(candidates, nil, itemLimit)
	if err != nil {
		return zero, fmt.Errorf("select transcript item candidates: %w", err)
	}
	// A source may have already selected its bounded window. In that case the
	// candidate slice itself has no omitted prefix, but Exhausted=false (and the
	// source cursor) still says that older positions remain outside the window.
	hasOlder = hasOlder || !result.Exhausted
	if len(selected) == 0 {
		if hasOlder || !result.Exhausted {
			return zero, errors.New("transcript item source reports older candidates without a page")
		}
		response := build([]appwire.Turn{}, "")
		if enrich != nil {
			response, err = enrich(response)
			if err != nil {
				return zero, err
			}
		}
		if sizeErr := packedResultSize(response); sizeErr != nil {
			return zero, sizeErr
		}
		return response, nil
	}

	// SelectCandidates returns the newest candidates in chronological order.
	// Remove only the oldest selected item when enrichment makes the typed
	// result too large. The nearest item is always retained, even if it alone
	// exceeds the soft target.
	selectedStart := len(candidates) - len(selected)
	for {
		selected = append([]appitempaging.TranscriptItemCandidate(nil), candidates[selectedStart:]...)
		dropped := selectedStart
		older := hasOlder || dropped > 0
		olderCursor, cursorErr := packedOlderCursor(result, selected[0].Position, older)
		if cursorErr != nil {
			return zero, cursorErr
		}
		var regroupErr error
		selected = appitempaging.NormalizeProjectedItemCompleteness(selected)
		turns, regroupErr := appitempaging.RegroupTurnFragments(selected)
		if regroupErr != nil {
			return zero, fmt.Errorf("regroup transcript item fragments: %w", regroupErr)
		}
		response := build(turns, olderCursor)
		if enrich != nil {
			response, err = enrich(response)
			if err != nil {
				return zero, err
			}
		}
		size, marshalErr := packedResultSizeValue(response)
		if marshalErr != nil {
			return zero, marshalErr
		}
		if size <= transcriptRPCResultSoftLimit || len(selected) == 1 {
			return response, nil
		}
		selectedStart++
	}
}

func validateSourceItemContinuation(result transcriptItemCandidateResult, candidates []appitempaging.TranscriptItemCandidate) error {
	cursor := result.Candidates.OlderCursor
	if cursor == "" {
		if !result.Exhausted && len(candidates) == 0 {
			return errors.New("transcript item source reports a non-exhausted empty page")
		}
		return nil
	}
	if len(candidates) == 0 {
		return errors.New("transcript item source returned a cursor without candidates")
	}
	if result.Exhausted {
		return errors.New("transcript item source returned an older cursor after exhaustion")
	}
	if result.Identity.ThreadRef != "" || result.Identity.Incarnation != "" || result.Identity.ProjectionVersion != 0 {
		before, err := appitempaging.DecodeCursor(cursor, result.Identity)
		if err != nil {
			return fmt.Errorf("transcript item source cursor disagrees with identity: %w", err)
		}
		if before != candidates[0].Position {
			return errors.New("transcript item source cursor disagrees with first candidate")
		}
	}
	return nil
}

func packedOlderCursor(result transcriptItemCandidateResult, before appwire.ThreadItemPosition, older bool) (string, error) {
	if !older {
		return "", nil
	}
	if result.Identity.ThreadRef != "" || result.Identity.Incarnation != "" || result.Identity.ProjectionVersion != 0 {
		cursor, err := appitempaging.EncodeCursor(result.Identity, before)
		if err != nil {
			return "", fmt.Errorf("encode transcript item cursor: %w", err)
		}
		return cursor, nil
	}
	if result.Candidates.OlderCursor == "" {
		return "", errors.New("transcript item source has older candidates without cursor identity")
	}
	return "", errors.New("legacy transcript item source cannot page without cursor identity")
}

func packedResultSize[T any](response T) error {
	_, err := packedResultSizeValue(response)
	return err
}

func packedResultSizeValue[T any](response T) (int, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return 0, fmt.Errorf("marshal enriched transcript item result: %w", err)
	}
	return len(encoded), nil
}
