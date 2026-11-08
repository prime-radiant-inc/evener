package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/internal/appitempaging"
)

const (
	transcriptRPCResultSoftLimit                = 1 << 20
	transcriptWindowDefaultPreviewBytes         = 2 << 10
	transcriptWindowDefaultMessageBytes         = 8 << 10
	transcriptWindowDefaultItemLimit            = 10
	transcriptWindowCursorLineLimit             = 10
	transcriptWindowDefaultBudgetBytes          = 192 << 10
	transcriptWindowBudgetFloorBytes            = 16 << 10
	transcriptWindowMeasuredEnvelopeMarginBytes = 32 << 10
	transcriptWindowMaximumContentBudget        = managerReadMaxJSONBytes - transcriptWindowMeasuredEnvelopeMarginBytes
)

type transcriptWindowContinuationDirection string

const (
	transcriptWindowContinuationBackward transcriptWindowContinuationDirection = "backward"
	transcriptWindowContinuationForward  transcriptWindowContinuationDirection = "forward"
)

// transcriptWindowContinuation is the cursor-minting boundary between the
// content packer and the view owner. TurnID names the first withheld turn for
// reporting. A forward Position names the first withheld item; a backward
// Position names the first served item as its exclusive boundary.
type transcriptWindowContinuation struct {
	Direction transcriptWindowContinuationDirection
	TurnID    string
	Position  appwire.ThreadItemPosition
	Version   uint64
}

// transcriptWindowPackOptions keeps cursor encoding and view identity outside
// the content packer. Both read response wrappers pass this same shape into the
// one generic packItemCandidates implementation.
type transcriptWindowPackOptions struct {
	Params                 appwire.ThreadWindowParams
	ItemLimit              int
	AppliedBy              string
	MintFetchCursor        func(appitempaging.TranscriptItemCandidate) (string, error)
	MintContinuationCursor func(transcriptWindowContinuation) (string, error)
}

const (
	transcriptWindowAppliedByServingDaemon = "serving-daemon"
	transcriptWindowAppliedByHubCompat     = "hub-compat"
	transcriptWindowAppliedBySavedIndex    = "saved-index"
)

func transcriptWindowOptionsFor(
	result transcriptItemCandidateResult,
	params appwire.ThreadWindowParams,
	itemLimit int,
	appliedBy string,
	fetchOrigin appitempaging.FetchCursorOrigin,
) transcriptWindowPackOptions {
	identity := transcriptWindowCursorIdentity(result.Identity, params, itemLimit)
	return transcriptWindowPackOptions{
		Params: params, ItemLimit: itemLimit, AppliedBy: appliedBy,
		MintFetchCursor: func(candidate appitempaging.TranscriptItemCandidate) (string, error) {
			fetchIdentity := identity
			fetchIdentity.ProjectionVersion = appitempaging.TranscriptItemProjectionVersion
			return appitempaging.EncodeFetchCursor(fetchIdentity, appitempaging.FetchCursor{
				Position: candidate.Position, ItemVersion: candidate.Item.Version, Origin: fetchOrigin,
			})
		},
		MintContinuationCursor: func(continuation transcriptWindowContinuation) (string, error) {
			direction := appitempaging.WindowCursorDirectionBackward
			if continuation.Direction == transcriptWindowContinuationForward {
				direction = appitempaging.WindowCursorDirectionForward
			}
			return appitempaging.EncodeWindowCursor(identity, direction, appitempaging.WindowCursor{
				Position: continuation.Position, ItemVersion: continuation.Version,
			})
		},
	}
}

func transcriptWindowCursorIdentity(identity appitempaging.CursorIdentity, params appwire.ThreadWindowParams, itemLimit int) appitempaging.CursorIdentity {
	identity.Incarnation = params.ViewIncarnation(identity.Incarnation, itemLimit)
	return identity
}

// spendTranscriptWindowCursor selects the remaining candidates from the same
// resolved turn window. Forward cursors name the first withheld item and are
// inclusive. Backward cursors name the first served item and are exclusive.
func spendTranscriptWindowCursor(result transcriptItemCandidateResult, params appwire.ThreadWindowParams, itemLimit int, encoded string) (transcriptItemCandidateResult, error) {
	identity := transcriptWindowCursorIdentity(result.Identity, params, itemLimit)
	candidates := result.Candidates.Candidates
	cursor, err := appitempaging.DecodeWindowCursor(encoded, identity, appitempaging.WindowCursorDirectionForward)
	if err == nil {
		if err := appitempaging.ValidateWindowCursorBoundary(candidates, cursor); err != nil {
			return transcriptItemCandidateResult{}, err
		}
		first := len(candidates)
		for i := range candidates {
			if appitempaging.ComparePosition(candidates[i].Position, cursor.Position) >= 0 {
				first = i
				break
			}
		}
		result.Candidates = appitempaging.TranscriptItemWindow{Candidates: append([]appitempaging.TranscriptItemCandidate(nil), candidates[first:]...)}
		result.Exhausted = true
		return result, nil
	}
	if params.TurnWindow == nil || (params.TurnWindow.Latest == 0 && params.TurnWindow.Before == nil) {
		return transcriptItemCandidateResult{}, err
	}
	cursor, err = appitempaging.DecodeWindowCursor(encoded, identity, appitempaging.WindowCursorDirectionBackward)
	if err != nil {
		return transcriptItemCandidateResult{}, err
	}
	if err := appitempaging.ValidateWindowCursorBoundary(candidates, cursor); err != nil {
		return transcriptItemCandidateResult{}, err
	}
	result.Candidates = appitempaging.TranscriptItemWindow{Candidates: appitempaging.CandidatesBefore(candidates, cursor.Position, len(candidates))}
	result.Exhausted = true
	return result, nil
}

type packItemCandidatesOptions struct {
	requestedLimit int
	window         *transcriptWindowPackOptions
}

type transcriptWindowCandidateTurn struct {
	turn       appwire.Turn
	candidates []appitempaging.TranscriptItemCandidate
}

type transcriptWindowSelectedItem struct {
	candidate      appitempaging.TranscriptItemCandidate
	item           appwire.ThreadItem
	bodyBytes      int
	boundTruncated bool
}

type transcriptWindowPreparedTurn struct {
	turn             appwire.Turn
	selected         []transcriptWindowSelectedItem
	excluded         map[string]int
	capWithheld      int
	firstCapWithheld *appitempaging.TranscriptItemCandidate
}

// transcriptItemCandidateResult is the private boundary between a source's
// positioned item projection and the public AppWire response. None of these
// fields are serialized into the response.
type transcriptItemCandidateResult struct {
	Candidates appitempaging.TranscriptItemWindow
	Identity   appitempaging.CursorIdentity
	Exhausted  bool
	History    appsource.HistoryIdentity
}

func transcriptItemCandidateResultFromSource(result appsource.ItemCandidateResult) transcriptItemCandidateResult {
	return transcriptItemCandidateResult{
		Candidates: result.Candidates,
		Identity:   result.Identity,
		Exhausted:  result.Exhausted,
		History:    result.History,
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
		return appendReadOverlayCandidates(transcriptItemCandidateResultFromSource(result), response)
	}
	result, err := itemCandidateResultFromReadResponse(response)
	if err != nil {
		return transcriptItemCandidateResult{}, err
	}
	return appendReadOverlayCandidates(result, response)
}

// appendReadOverlayCandidates gives the existing window packer the selected
// live overlay beside the settled source snapshot. Overlay items have no
// history position or version, so their positions are ephemeral and sort after
// the selected settled window; they can be displayed and bounded but never
// acquire a fetch cursor.
func appendReadOverlayCandidates(result transcriptItemCandidateResult, response appwire.ThreadReadResponse) (transcriptItemCandidateResult, error) {
	if len(response.Overlay) == 0 {
		return result, nil
	}
	turns := make(map[string]appwire.Turn, len(response.Thread.Turns))
	for _, turn := range response.Thread.Turns {
		turns[turn.ID] = turn
	}
	candidates := append([]appitempaging.TranscriptItemCandidate(nil), result.Candidates.Candidates...)
	position := appwire.ThreadItemPosition{Item: appwire.NoticeAnchorItem}
	if len(candidates) > 0 {
		last := candidates[len(candidates)-1].Position
		position.Entry = last.Entry
		if last.Item >= appwire.NoticeAnchorItem {
			position.Item = last.Item
			position.Sub = last.Sub
		}
	}
	for _, overlay := range response.Overlay {
		turnID := overlay.TurnID
		if turnID == "" {
			turnID = overlay.Item.TurnID
		}
		turn, selected := turns[turnID]
		if !selected {
			continue
		}
		position.Sub++
		item := appwire.CloneThreadItem(overlay.Item)
		item.TurnID = turnID
		if item.TranscriptKey == "" {
			item.TranscriptKey = overlay.Key
		}
		item.Position = new(appwire.ThreadItemPosition)
		*item.Position = position
		item.Version = 0
		item.FetchCursor = ""
		candidates = append(candidates, appitempaging.TranscriptItemCandidate{
			TurnID: turnID, Turn: turn, Item: item, Position: position, HasEarlierItems: true,
		})
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return transcriptItemCandidateResult{}, err
	}
	result.Candidates.Candidates = candidates
	return result, nil
}

func sourceItemCandidateResultForList(ctx context.Context, source appsource.Source, params appwire.ThreadTurnsListParams, response appwire.ThreadTurnsListResponse) (transcriptItemCandidateResult, error) {
	if itemSource, ok := source.(appsource.ItemCandidateSource); ok {
		result, err := itemSource.ListItemCandidates(ctx, params)
		if err != nil {
			return transcriptItemCandidateResult{}, err
		}
		return transcriptItemCandidateResultFromSource(result), nil
	}
	result, err := itemCandidateResultFromTurns(response.Data, response.NextCursor)
	result.History = appsource.PageHistoryIdentity(response)
	return result, err
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
	options := packItemCandidatesOptions{}
	if len(requestedLimit) > 0 {
		options.requestedLimit = requestedLimit[0]
	}
	return packThreadReadItemCandidatesWithOptions(candidates, enrich, options)
}

func packThreadReadWindowItemCandidates(
	candidates transcriptItemCandidateResult,
	enrich func(appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error),
	window transcriptWindowPackOptions,
) (appwire.ThreadReadResponse, error) {
	return packThreadReadItemCandidatesWithOptions(candidates, enrich, packItemCandidatesOptions{window: &window})
}

func packThreadReadItemCandidatesWithOptions(
	candidates transcriptItemCandidateResult,
	enrich func(appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error),
	options packItemCandidatesOptions,
) (appwire.ThreadReadResponse, error) {
	response, err := packItemCandidates(candidates, enrich, func(turns []appwire.Turn, olderCursor string) appwire.ThreadReadResponse {
		return appwire.ThreadReadResponse{
			Thread:      appwire.Thread{Turns: turns},
			OlderCursor: olderCursor,
		}
	}, func(response appwire.ThreadReadResponse) []appwire.Turn { return response.Thread.Turns }, func(response appwire.ThreadReadResponse, window *appwire.ThreadWindowResult) appwire.ThreadReadResponse {
		response.Window = window
		return response
	}, options)
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
	options := packItemCandidatesOptions{}
	if len(requestedLimit) > 0 {
		options.requestedLimit = requestedLimit[0]
	}
	return packThreadTurnsItemCandidatesWithOptions(candidates, enrich, options)
}

func packThreadTurnsWindowItemCandidates(
	candidates transcriptItemCandidateResult,
	enrich func(appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error),
	window transcriptWindowPackOptions,
) (appwire.ThreadTurnsListResponse, error) {
	return packThreadTurnsItemCandidatesWithOptions(candidates, enrich, packItemCandidatesOptions{window: &window})
}

func packThreadTurnsItemCandidatesWithOptions(
	candidates transcriptItemCandidateResult,
	enrich func(appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error),
	options packItemCandidatesOptions,
) (appwire.ThreadTurnsListResponse, error) {
	response, err := packItemCandidates(candidates, enrich, func(turns []appwire.Turn, olderCursor string) appwire.ThreadTurnsListResponse {
		return appwire.ThreadTurnsListResponse{
			Data:       turns,
			NextCursor: olderCursor,
		}
	}, func(response appwire.ThreadTurnsListResponse) []appwire.Turn { return response.Data }, func(response appwire.ThreadTurnsListResponse, window *appwire.ThreadWindowResult) appwire.ThreadTurnsListResponse {
		response.Window = window
		return response
	}, options)
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
	// contentTurns reports the transcript content the packer shrinks by
	// dropping items — the turns the response carries, read post-enrich so the
	// measurement tracks what the page would actually transmit, not the fixed
	// envelope (delegate statuses, task aggregates) a thread/read enrich grafts
	// around them. The soft limit exists to bound what a page costs to
	// transmit and to shrink when too large; an envelope no drop can remove
	// must not drive the decision, or every window of a delegate-heavy session
	// strips to a single item.
	contentTurns func(T) []appwire.Turn,
	setWindow func(T, *appwire.ThreadWindowResult) T,
	options packItemCandidatesOptions,
) (T, error) {
	var zero T
	var err error
	candidates := append([]appitempaging.TranscriptItemCandidate(nil), result.Candidates.Candidates...)
	if _, err := appitempaging.RegroupTurnFragments(candidates); err != nil {
		return zero, fmt.Errorf("invalid transcript item candidates: %w", err)
	}
	if err := validateSourceItemContinuation(result, candidates); err != nil {
		return zero, err
	}

	if options.window != nil {
		windowOptions := *options.window
		if err := appwire.ValidateThreadWindowParams(windowOptions.Params, windowOptions.ItemLimit); err != nil {
			return zero, err
		}
		if windowOptions.Params.TurnWindow == nil {
			return zero, appwire.InvalidParams("window packing requires turnWindow")
		}
		itemLimit := windowOptions.ItemLimit
		if itemLimit == 0 {
			itemLimit = transcriptWindowDefaultItemLimit
		}
		previewBytes := windowOptions.Params.PreviewBytes
		if previewBytes == 0 {
			previewBytes = transcriptWindowDefaultPreviewBytes
		}
		messageBytes := windowOptions.Params.MessageBytes
		if messageBytes == 0 {
			messageBytes = transcriptWindowDefaultMessageBytes
		}
		effectiveBudget := windowOptions.Params.WindowBudgetBytes
		if effectiveBudget == 0 {
			effectiveBudget = transcriptWindowDefaultBudgetBytes
		}
		if effectiveBudget < transcriptWindowBudgetFloorBytes {
			effectiveBudget = transcriptWindowBudgetFloorBytes
		}
		if effectiveBudget > transcriptWindowMaximumContentBudget {
			effectiveBudget = transcriptWindowMaximumContentBudget
		}

		var candidateTurns []transcriptWindowCandidateTurn
		for _, candidate := range candidates {
			if len(candidateTurns) == 0 || candidateTurns[len(candidateTurns)-1].turn.ID != candidate.TurnID {
				candidateTurns = append(candidateTurns, transcriptWindowCandidateTurn{turn: candidate.Turn})
			}
			candidateTurns[len(candidateTurns)-1].candidates = append(candidateTurns[len(candidateTurns)-1].candidates, candidate)
		}

		prepared := make([]transcriptWindowPreparedTurn, 0, len(candidateTurns))
		for _, candidateTurn := range candidateTurns {
			preparedTurn := transcriptWindowPreparedTurn{turn: candidateTurn.turn}
			var selectedCandidates []appitempaging.TranscriptItemCandidate
			for _, candidate := range candidateTurn.candidates {
				if !transcriptWindowIncludesItem(windowOptions.Params.ItemTypes, candidate.Item) {
					if preparedTurn.excluded == nil {
						preparedTurn.excluded = make(map[string]int)
					}
					preparedTurn.excluded[candidate.Item.Type]++
					continue
				}
				selectedCandidates = append(selectedCandidates, candidate)
			}
			if len(selectedCandidates) > itemLimit {
				preparedTurn.capWithheld = len(selectedCandidates) - itemLimit
				first := selectedCandidates[itemLimit]
				preparedTurn.firstCapWithheld = &first
				selectedCandidates = selectedCandidates[:itemLimit]
			}
			for _, candidate := range selectedCandidates {
				item, bodyBytes, truncated := transcriptWindowBoundItem(candidate.Item, previewBytes, messageBytes)
				preparedTurn.selected = append(preparedTurn.selected, transcriptWindowSelectedItem{
					candidate: candidate, item: item, bodyBytes: bodyBytes, boundTruncated: truncated,
				})
			}
			prepared = append(prepared, preparedTurn)
		}

		windowResult := &appwire.ThreadWindowResult{EffectiveBudgetBytes: effectiveBudget, AppliedBy: windowOptions.AppliedBy}
		if len(prepared) == 0 {
			response := setWindow(build([]appwire.Turn{}, ""), windowResult)
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

		direction := transcriptWindowContinuationBackward
		trimmedEnd := "oldest"
		if windowOptions.Params.TurnWindow != nil && windowOptions.Params.TurnWindow.Since != "" {
			direction = transcriptWindowContinuationForward
			trimmedEnd = "newest"
		}
		sourceWithheldTurns := 0
		if !result.Exhausted && windowOptions.Params.TurnWindow != nil && windowOptions.Params.TurnWindow.Latest > len(prepared) {
			sourceWithheldTurns = windowOptions.Params.TurnWindow.Latest - len(prepared)
		}
		start, end := 0, len(prepared)
		for {
			cursorLines := 0
			turns := make([]appwire.Turn, 0, end-start)
			for turnIndex := start; turnIndex < end; turnIndex++ {
				preparedTurn := prepared[turnIndex]
				turn := preparedTurn.turn
				turn.Items = nil
				turn.ItemsView = appwire.TurnItemsViewFragment
				turn.Elision = nil
				elision := &appwire.ThreadTurnElision{}
				if len(preparedTurn.excluded) > 0 {
					elision.ExcludedByType = preparedTurn.excluded
					elision.TypeExcludedRecovery = appwire.ThreadElisionRecoveryWiderItemTypes
				}
				elision.CapWithheld = preparedTurn.capWithheld
				if preparedTurn.capWithheld > 0 {
					elision.CapWithheldRecovery = appwire.ThreadElisionRecoveryContinuation
				}
				var firstContinuation *appitempaging.TranscriptItemCandidate
				if preparedTurn.firstCapWithheld != nil {
					first := *preparedTurn.firstCapWithheld
					firstContinuation = &first
				}
				boundTruncated := make([]bool, 0, len(preparedTurn.selected))
				bodyBytes := make([]int, 0, len(preparedTurn.selected))
				lineCandidates := make([]appitempaging.TranscriptItemCandidate, 0, len(preparedTurn.selected))
				for _, selectedItem := range preparedTurn.selected {
					item := selectedItem.item
					if selectedItem.boundTruncated {
						item.Truncated = true
						item.TruncatedBytes = selectedItem.bodyBytes
						if item.Version == 0 {
							item.InFlight = true
						} else if cursorLines < transcriptWindowCursorLineLimit {
							cursor, cursorErr := mintTranscriptWindowFetchCursor(windowOptions, selectedItem.candidate)
							if cursorErr != nil {
								return zero, cursorErr
							}
							item.FetchCursor = cursor
							cursorLines++
						} else {
							elision.TruncatedBeyondCursorLimit++
							elision.TruncatedBeyondCursorLimitRecovery = appwire.ThreadElisionRecoveryWiderByteBound
						}
					}
					turn.Items = append(turn.Items, item)
					boundTruncated = append(boundTruncated, selectedItem.boundTruncated)
					bodyBytes = append(bodyBytes, selectedItem.bodyBytes)
					lineCandidates = append(lineCandidates, selectedItem.candidate)
				}

				continuationKey := appwire.ThreadItemPosition{}
				continuationSet := false
				setContinuation := func() error {
					if firstContinuation == nil {
						elision.ContinuationCursor = ""
						continuationSet = false
						return nil
					}
					if continuationSet && continuationKey == firstContinuation.Position {
						return nil
					}
					cursor, cursorErr := mintTranscriptWindowContinuation(windowOptions, transcriptWindowContinuation{
						Direction: transcriptWindowContinuationForward,
						TurnID:    preparedTurn.turn.ID,
						Position:  firstContinuation.Position,
						Version:   firstContinuation.Item.Version,
					})
					if cursorErr != nil {
						return cursorErr
					}
					elision.ContinuationCursor = cursor
					continuationKey = firstContinuation.Position
					continuationSet = true
					return nil
				}
				if err := setContinuation(); err != nil {
					return zero, err
				}
				attachTranscriptWindowElision(&turn, elision)

				for {
					turnSize, sizeErr := transcriptWindowTurnEncodedSize(turn)
					if sizeErr != nil {
						return zero, sizeErr
					}
					if turnSize <= effectiveBudget {
						break
					}
					itemIndex := -1
					for i := range turn.Items {
						if !transcriptWindowItemHasBody(turn.Items[i]) {
							continue
						}
						prefix := turn
						prefix.Items = turn.Items[:i+1]
						prefixSize, prefixErr := transcriptWindowTurnEncodedSize(prefix)
						if prefixErr != nil {
							return zero, prefixErr
						}
						if prefixSize > effectiveBudget {
							itemIndex = i
							break
						}
					}
					if itemIndex < 0 {
						for i, v := range slices.Backward(turn.Items) {
							if transcriptWindowItemHasBody(v) {
								itemIndex = i
								break
							}
						}
					}
					if itemIndex < 0 {
						break
					}
					item := transcriptWindowBodylessItem(turn.Items[itemIndex], bodyBytes[itemIndex])
					if boundTruncated[itemIndex] {
						turn.Items[itemIndex] = item
						continue
					}
					candidate := lineCandidates[itemIndex]
					if item.Version == 0 {
						item.InFlight = true
						turn.Items[itemIndex] = item
					} else if cursorLines < transcriptWindowCursorLineLimit {
						cursor, cursorErr := mintTranscriptWindowFetchCursor(windowOptions, candidate)
						if cursorErr != nil {
							return zero, cursorErr
						}
						item.FetchCursor = cursor
						cursorLines++
						turn.Items[itemIndex] = item
					} else {
						turn.Items = append(turn.Items[:itemIndex], turn.Items[itemIndex+1:]...)
						boundTruncated = append(boundTruncated[:itemIndex], boundTruncated[itemIndex+1:]...)
						bodyBytes = append(bodyBytes[:itemIndex], bodyBytes[itemIndex+1:]...)
						lineCandidates = append(lineCandidates[:itemIndex], lineCandidates[itemIndex+1:]...)
						elision.ElidedBeyondCursorLimit++
						elision.ElidedBeyondCursorLimitRecovery = appwire.ThreadElisionRecoveryContinuation
						first := candidate
						if firstContinuation == nil || appitempaging.ComparePosition(first.Position, firstContinuation.Position) < 0 {
							firstContinuation = &first
						}
						if err := setContinuation(); err != nil {
							return zero, err
						}
					}
					attachTranscriptWindowElision(&turn, elision)
				}
				turns = append(turns, turn)
			}

			windowResult.ServedTurns = len(turns)
			windowResult.WithheldTurns = len(prepared) - len(turns) + sourceWithheldTurns
			windowResult.FirstServedTurnID = turns[0].ID
			windowResult.LastServedTurnID = turns[len(turns)-1].ID
			windowResult.FirstWithheldTurnID = ""
			windowResult.LastWithheldTurnID = ""
			windowResult.TrimmedEnd = ""
			windowResult.ContinuationCursor = ""
			if windowResult.WithheldTurns > 0 {
				windowResult.TrimmedEnd = trimmedEnd
				request := transcriptWindowContinuation{Direction: direction}
				if start > 0 {
					windowResult.FirstWithheldTurnID = prepared[0].turn.ID
					windowResult.LastWithheldTurnID = prepared[start-1].turn.ID
					request.TurnID = prepared[0].turn.ID
					request.Position = candidateTurns[start].candidates[0].Position
					request.Version = candidateTurns[start].candidates[0].Item.Version
				} else if end < len(prepared) {
					windowResult.FirstWithheldTurnID = prepared[end].turn.ID
					windowResult.LastWithheldTurnID = prepared[len(prepared)-1].turn.ID
					request.TurnID = prepared[end].turn.ID
					request.Position = candidateTurns[end].candidates[0].Position
					request.Version = candidateTurns[end].candidates[0].Item.Version
				} else {
					// An incomplete latest source page proves how many requested turns
					// are absent but cannot name their IDs. Continue immediately before
					// the oldest served position through the source-owned identity.
					request.TurnID = prepared[0].turn.ID
					request.Position = candidateTurns[0].candidates[0].Position
					request.Version = candidateTurns[0].candidates[0].Item.Version
				}
				cursor, cursorErr := mintTranscriptWindowContinuation(windowOptions, request)
				if cursorErr != nil {
					return zero, cursorErr
				}
				windowResult.ContinuationCursor = cursor
			}

			response := setWindow(build(turns, ""), windowResult)
			if enrich != nil {
				response, err = enrich(response)
				if err != nil {
					return zero, err
				}
			}
			encoded, marshalErr := json.Marshal(contentTurns(response))
			if marshalErr != nil {
				return zero, fmt.Errorf("marshal transcript window turns: %w", marshalErr)
			}
			if len(encoded) <= effectiveBudget || end-start == 1 {
				return response, nil
			}
			if direction == transcriptWindowContinuationForward {
				end--
			} else {
				start++
			}
		}
	}

	itemLimit := appwire.TranscriptItemPageLimit
	if options.requestedLimit != 0 {
		itemLimit = options.requestedLimit
	}
	itemLimit, err = appwire.NormalizeTranscriptItemLimit(itemLimit)
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
		encoded, marshalErr := json.Marshal(contentTurns(response))
		if marshalErr != nil {
			return zero, fmt.Errorf("marshal transcript item turns: %w", marshalErr)
		}
		if len(encoded) <= transcriptRPCResultSoftLimit || len(selected) == 1 {
			return response, nil
		}
		selectedStart++
	}
}

func transcriptWindowIncludesItem(itemTypes []string, item appwire.ThreadItem) bool {
	if len(itemTypes) == 0 {
		return true
	}
	for _, itemType := range itemTypes {
		if itemType == item.Type {
			return true
		}
		if itemType == "messages" {
			switch item.Type {
			case "agentMessage", "userMessage":
				return true
			case "steering":
				if item.Source == "user" {
					return true
				}
			}
		}
	}
	return false
}

func transcriptWindowBoundItem(item appwire.ThreadItem, previewBytes, messageBytes int) (appwire.ThreadItem, int, bool) {
	bodyBytes := transcriptWindowItemBodyBytes(item)
	truncated := false
	if (item.Type == "agentMessage" || item.Type == "userMessage") && len(item.Text) > messageBytes {
		item.Text = transcriptWindowUTF8Prefix(item.Text, messageBytes)
		truncated = true
	}
	if item.Output != "" && len(item.Output) > previewBytes {
		item.Output = transcriptWindowUTF8Prefix(item.Output, previewBytes)
		truncated = true
	}
	return item, bodyBytes, truncated
}

func transcriptWindowUTF8Prefix(value string, limit int) string {
	if limit >= len(value) {
		return value
	}
	if limit <= 0 {
		return ""
	}
	prefix := value[:limit]
	for !utf8.ValidString(prefix) {
		_, size := utf8.DecodeLastRuneInString(prefix)
		if size == 0 {
			return ""
		}
		prefix = prefix[:len(prefix)-size]
	}
	return prefix
}

func transcriptWindowItemBodyBytes(item appwire.ThreadItem) int {
	size := len(item.Text) + len(item.Delta) + len(item.ArgumentsJSON) + len(item.Description) + len(item.Output) + len(item.Error) + len(item.Raw)
	if encoded, err := json.Marshal(item.Images); err == nil && len(item.Images) > 0 {
		size += len(encoded)
	}
	if encoded, err := json.Marshal(item.OutputImages); err == nil && len(item.OutputImages) > 0 {
		size += len(encoded)
	}
	return size
}

func transcriptWindowItemHasBody(item appwire.ThreadItem) bool {
	return item.Text != "" || item.Delta != "" || len(item.Images) > 0 || item.ArgumentsJSON != "" || item.Description != "" || item.Output != "" || item.Error != "" || len(item.OutputImages) > 0 || len(item.Raw) > 0
}

func transcriptWindowBodylessItem(item appwire.ThreadItem, bodyBytes int) appwire.ThreadItem {
	item.Text = ""
	item.Delta = ""
	item.Images = nil
	item.ArgumentsJSON = ""
	item.Description = ""
	item.Output = ""
	item.Error = ""
	item.OutputImages = nil
	item.Raw = nil
	item.Truncated = true
	item.TruncatedBytes = bodyBytes
	return item
}

func mintTranscriptWindowFetchCursor(options transcriptWindowPackOptions, candidate appitempaging.TranscriptItemCandidate) (string, error) {
	if options.MintFetchCursor == nil {
		return "", errors.New("transcript window requires a fetch cursor callback")
	}
	cursor, err := options.MintFetchCursor(candidate)
	if err != nil {
		return "", fmt.Errorf("mint transcript item fetch cursor: %w", err)
	}
	if cursor == "" {
		return "", errors.New("transcript item fetch cursor callback returned an empty cursor")
	}
	return cursor, nil
}

func mintTranscriptWindowContinuation(options transcriptWindowPackOptions, request transcriptWindowContinuation) (string, error) {
	if options.MintContinuationCursor == nil {
		return "", errors.New("transcript window requires a continuation cursor callback")
	}
	cursor, err := options.MintContinuationCursor(request)
	if err != nil {
		return "", fmt.Errorf("mint transcript window continuation cursor: %w", err)
	}
	if cursor == "" {
		return "", errors.New("transcript window continuation callback returned an empty cursor")
	}
	return cursor, nil
}

func attachTranscriptWindowElision(turn *appwire.Turn, elision *appwire.ThreadTurnElision) {
	if len(elision.ExcludedByType) == 0 && elision.CapWithheld == 0 && elision.TruncatedBeyondCursorLimit == 0 && elision.ElidedBeyondCursorLimit == 0 {
		turn.Elision = nil
		return
	}
	turn.Elision = elision
}

func transcriptWindowTurnEncodedSize(turn appwire.Turn) (int, error) {
	encoded, err := json.Marshal([]appwire.Turn{turn})
	if err != nil {
		return 0, fmt.Errorf("marshal transcript window turn: %w", err)
	}
	return len(encoded), nil
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
	return "", errLegacyItemSourceUnpageable
}

// errLegacyItemSourceUnpageable refuses paging a legacy transcript item
// source whose page carries no cursor identity.
var errLegacyItemSourceUnpageable = errors.New("legacy transcript item source cannot page without cursor identity")

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
