package hub

import (
	"context"
	"slices"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

type transcriptWindowLoadedRange struct {
	Result transcriptItemCandidateResult
	Thread appwire.Thread
}

type transcriptWindowRangeLoader func(context.Context, *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error)

func resolveTranscriptWindowContinuation(
	ctx context.Context,
	params appwire.ThreadTurnsListParams,
	itemLimit int,
	load transcriptWindowRangeLoader,
) (transcriptWindowLoadedRange, appitempaging.WindowCursorDirection, error) {
	var zero transcriptWindowLoadedRange
	cursor, _, direction, err := appitempaging.DecodeWindowCursorForRef(params.Cursor, params.Ref)
	if err != nil {
		return zero, "", err
	}
	if !transcriptWindowContinuationDirectionMatches(params.TurnWindow, cursor.ResumeWindow, direction) {
		return zero, "", appwire.TranscriptItemCursorStale()
	}

	loaded, err := load(ctx, cursor.ResumeWindow)
	if err != nil {
		return zero, "", err
	}
	loaded.Result = normalizeTranscriptWindowLoadedResult(loaded.Result, loaded.Thread)
	identity := transcriptWindowCursorIdentity(loaded.Result.Identity, params.ThreadWindowParams, itemLimit)
	cursor, err = appitempaging.DecodeWindowCursor(params.Cursor, identity, direction)
	if err != nil {
		return zero, "", err
	}

	if cursor.EmptyRangeDigest != "" {
		if len(loaded.Result.Candidates.Candidates) != 0 {
			return zero, "", appwire.TranscriptItemCursorStale()
		}
		digest, digestErr := emptyTranscriptWindowDigest(loaded.Result)
		if digestErr != nil || digest != cursor.EmptyRangeDigest {
			return zero, "", appwire.TranscriptItemCursorStale()
		}
	} else {
		anchorResult := loaded.Result
		if !transcriptWindowContainsTurn(anchorResult, cursor.AnchorTurnID) {
			anchor, loadErr := load(ctx, &appwire.ThreadTurnWindow{Turn: cursor.AnchorTurnID})
			if loadErr != nil {
				return zero, "", loadErr
			}
			anchor.Result = normalizeTranscriptWindowLoadedResult(anchor.Result, anchor.Thread)
			if anchor.Result.Identity != loaded.Result.Identity {
				return zero, "", appwire.TranscriptItemCursorStale()
			}
			anchorResult = anchor.Result
		}
		anchorCandidates := make([]appitempaging.TranscriptItemCandidate, 0, len(anchorResult.Candidates.Candidates))
		for _, candidate := range anchorResult.Candidates.Candidates {
			if candidate.TurnID == cursor.AnchorTurnID {
				anchorCandidates = append(anchorCandidates, candidate)
			}
		}
		if err := appitempaging.ValidateWindowCursorBoundary(anchorCandidates, cursor); err != nil {
			return zero, "", err
		}
		// A whole-turn continuation carries the resolved logical range in
		// its Since/Before resume window and is validated by its anchor.
		// Position slicing compares physical positions, which interleaved
		// reopened turns do not order like their logical turns, so it
		// applies only where the cursor resumes inside one turn.
		if direction == appitempaging.WindowCursorDirectionForward && transcriptWindowContinuationResumesInsideTurn(cursor) {
			continued, spendErr := spendTranscriptWindowCursor(loaded.Result, params.ThreadWindowParams, itemLimit, params.Cursor)
			if spendErr != nil {
				return zero, "", spendErr
			}
			loaded.Result = continued
		}
	}
	// A continuation-relative range is complete only when the source transfer
	// delivered everything it selected. A range that still withholds turns
	// upstream stays open so the packer can continue from them.
	if loaded.Result.WithheldTurns == 0 {
		loaded.Result.Exhausted = true
	}
	return loaded, direction, nil
}

// transcriptWindowContinuationResumesInsideTurn reports whether a continuation
// cuts inside one turn rather than at a whole-turn boundary. A Turn resume
// window names that intra-turn cut; a Since or Before window carries the
// resolved logical range of whole turns.
func transcriptWindowContinuationResumesInsideTurn(cursor appitempaging.WindowCursor) bool {
	return cursor.ResumeWindow != nil && cursor.ResumeWindow.Turn != ""
}

func transcriptWindowContinuationDirectionFor(window *appwire.ThreadTurnWindow) appitempaging.WindowCursorDirection {
	if window == nil {
		return ""
	}
	if window.Latest > 0 || window.Before != nil {
		return appitempaging.WindowCursorDirectionBackward
	}
	if window.Turn != "" || window.Since != "" {
		return appitempaging.WindowCursorDirectionForward
	}
	return ""
}

func transcriptWindowContinuationDirectionMatches(
	original *appwire.ThreadTurnWindow,
	resume *appwire.ThreadTurnWindow,
	direction appitempaging.WindowCursorDirection,
) bool {
	switch direction {
	case appitempaging.WindowCursorDirectionBackward:
		return transcriptWindowContinuationDirectionFor(original) == direction && resume != nil && resume.Before != nil
	case appitempaging.WindowCursorDirectionForward:
		if resume == nil {
			return false
		}
		if resume.Turn != "" {
			return true
		}
		return resume.Since != "" && transcriptWindowContinuationDirectionFor(original) == direction
	default:
		return false
	}
}

func normalizeTranscriptWindowLoadedResult(result transcriptItemCandidateResult, thread appwire.Thread) transcriptItemCandidateResult {
	if len(result.TurnIDs) == 0 {
		result.TurnIDs = turnIDsFromTurns(thread.Turns)
	}
	if len(result.TurnShells) == 0 {
		result.TurnShells = cloneTranscriptTurnShells(thread.Turns)
	}
	return result
}

func transcriptWindowThreadFromResult(result transcriptItemCandidateResult) appwire.Thread {
	candidateTurns := transcriptWindowCandidateTurns(result, result.Candidates.Candidates)
	turns := make([]appwire.Turn, len(candidateTurns))
	for index := range candidateTurns {
		turns[index] = candidateTurns[index].turn
	}
	return appwire.Thread{Turns: turns}
}

func transcriptWindowContainsTurn(result transcriptItemCandidateResult, turnID string) bool {
	if turnID == "" {
		return false
	}
	if slices.Contains(result.TurnIDs, turnID) {
		return true
	}
	for _, candidate := range result.Candidates.Candidates {
		if candidate.TurnID == turnID {
			return true
		}
	}
	for _, turn := range result.TurnShells {
		if turn.ID == turnID {
			return true
		}
	}
	return false
}
