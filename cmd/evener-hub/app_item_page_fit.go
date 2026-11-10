package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Direction        transcriptWindowContinuationDirection
	TurnID           string
	AnchorTurnID     string
	Position         appwire.ThreadItemPosition
	Version          uint64
	EmptyRangeDigest string
	ResumeWindow     *appwire.ThreadTurnWindow
}

// transcriptWindowPackOptions keeps cursor encoding and view identity outside
// the content packer. Both read response wrappers pass this same shape into the
// one generic packItemCandidates implementation.
type transcriptWindowPackOptions struct {
	Params    appwire.ThreadWindowParams
	ItemLimit int
	AppliedBy string
	// StructuredFollowUpParams, when set, makes the packer reserve room for the
	// thread/turns response envelope: a windowed page can be served as a turns
	// page, whose per-turn and page-level cursors each add a recovery follow-up.
	// The content budget alone cannot see those follow-ups, so a page whose
	// content fits can still overflow the Manager response cap.
	StructuredFollowUpParams *appwire.ThreadTurnsListParams
	// fetchTokens lets a store-backed view owner assign the non-reused retained
	// entry token each fetch cursor carries.
	fetchTokens            *transcriptWindowFetchTokens
	CanonicalizeCandidates func([]appitempaging.TranscriptItemCandidate) ([]appitempaging.TranscriptItemCandidate, error)
	MintFetchCursor        func(appitempaging.TranscriptItemCandidate, string) (string, error)
	RetainFetchItem        func(string, appitempaging.TranscriptItemCandidate) error
	MintContinuationCursor func(transcriptWindowContinuation) (string, error)
}

const (
	transcriptWindowAppliedByServingDaemon = "serving-daemon"
	transcriptWindowAppliedByHubCompat     = "hub-compat"
	transcriptWindowAppliedBySavedIndex    = "saved-index"
)

func normalizeTranscriptWindowItemLimit(window *appwire.ThreadTurnWindow, requested int) (int, error) {
	if window != nil && requested == 0 {
		requested = appwire.ThreadWindowItemLimitDefault
	}
	return appwire.NormalizeTranscriptItemLimit(requested)
}

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
		fetchTokens: &transcriptWindowFetchTokens{identity: identity},
		MintFetchCursor: func(candidate appitempaging.TranscriptItemCandidate, token string) (string, error) {
			fetchIdentity := identity
			fetchIdentity.ProjectionVersion = appitempaging.TranscriptItemProjectionVersion
			return appitempaging.EncodeFetchCursor(fetchIdentity, appitempaging.FetchCursor{
				Position: candidate.Position, ItemVersion: candidate.Item.Version, Origin: fetchOrigin, Token: token,
			})
		},
		MintContinuationCursor: func(continuation transcriptWindowContinuation) (string, error) {
			direction := appitempaging.WindowCursorDirectionBackward
			if continuation.Direction == transcriptWindowContinuationForward {
				direction = appitempaging.WindowCursorDirectionForward
			}
			return appitempaging.EncodeWindowCursor(identity, direction, appitempaging.WindowCursor{
				AnchorTurnID: continuation.AnchorTurnID, Position: continuation.Position, ItemVersion: continuation.Version,
				EmptyRangeDigest: continuation.EmptyRangeDigest, ResumeWindow: continuation.ResumeWindow,
			})
		},
	}
}

func transcriptWindowCursorIdentity(identity appitempaging.CursorIdentity, params appwire.ThreadWindowParams, itemLimit int) appitempaging.CursorIdentity {
	identity.Incarnation = params.ViewIncarnation(identity.Incarnation, itemLimit)
	return identity
}

// transcriptTurnsListParamsFromRead projects a thread/read request into the
// thread/turns request a page consumer holds. A windowed read page is also a
// turns page, because turns serves its first page from read, so measuring that
// consumer's envelope keeps the read packer from serving a page the turns
// operation cannot return.
func transcriptTurnsListParamsFromRead(params appwire.ThreadReadParams) appwire.ThreadTurnsListParams {
	return appwire.ThreadTurnsListParams{
		ThreadContentParams: params.ThreadContentParams,
		ThreadID:            params.ThreadID,
		Ref:                 params.Ref,
		ItemsView:           params.ItemsView,
		ItemLimit:           params.ItemLimit,
		ThreadWindowParams:  params.ThreadWindowParams,
	}
}

// configureTranscriptWindowResponseFit makes a windowed page budget the
// thread/turns envelope its consumer receives. Every production window packer
// that can serve a turns page uses it.
func configureTranscriptWindowResponseFit(options *transcriptWindowPackOptions, params appwire.ThreadTurnsListParams) {
	fitParams := params
	options.StructuredFollowUpParams = &fitParams
}

// transcriptWindowStructuredPageSize measures a packed page as the thread/turns
// response a consumer receives, recovery follow-ups included. The fixed envelope
// margin covers the actor and response shell; a follow-up repeats the request
// shape for every cursor-bearing turn and can outgrow that margin on a page
// whose content alone fits.
func transcriptWindowStructuredPageSize(
	page appwire.ThreadTurnsListResponse,
	params appwire.ThreadTurnsListParams,
) (int, error) {
	encoded, err := json.Marshal(appwire.SessionOperationResponse{
		Turns:     &page,
		FollowUps: transcriptReadFollowUps(params, page),
	})
	if err != nil {
		return 0, fmt.Errorf("marshal transcript window page: %w", err)
	}
	return len(encoded), nil
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
		boundaryTurnID := ""
		for i := range candidates {
			if appitempaging.ComparePosition(candidates[i].Position, cursor.Position) >= 0 {
				first = i
				boundaryTurnID = candidates[i].TurnID
				break
			}
		}
		selected := append([]appitempaging.TranscriptItemCandidate(nil), candidates[first:]...)
		turnScoped := false
		for _, candidate := range candidates[:first] {
			if candidate.TurnID != boundaryTurnID {
				continue
			}
			selected = slices.DeleteFunc(selected, func(candidate appitempaging.TranscriptItemCandidate) bool {
				return candidate.TurnID != boundaryTurnID
			})
			turnScoped = true
			break
		}
		result.Candidates = appitempaging.TranscriptItemWindow{Candidates: selected}
		if turnScoped {
			result.TurnIDs = []string{boundaryTurnID}
		} else {
			result.TurnIDs, err = continuationTurnIDs(result, boundaryTurnID, transcriptWindowContinuationForward)
			if err != nil {
				return transcriptItemCandidateResult{}, err
			}
			selected = candidatesForTurnIDs(candidates, result.TurnIDs)
			result.Candidates = appitempaging.TranscriptItemWindow{Candidates: selected}
		}
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
	if len(result.TurnIDs) == 0 {
		result.Candidates = appitempaging.TranscriptItemWindow{
			Candidates: appitempaging.CandidatesBefore(candidates, cursor.Position, len(candidates)),
		}
		result.Exhausted = true
		return result, nil
	}
	boundaryTurnID := ""
	for _, candidate := range candidates {
		if candidate.Position == cursor.Position {
			boundaryTurnID = candidate.TurnID
			break
		}
	}
	turnIDs, err := continuationTurnIDs(result, boundaryTurnID, transcriptWindowContinuationBackward)
	if err != nil {
		return transcriptItemCandidateResult{}, err
	}
	selected := candidatesForTurnIDs(candidates, turnIDs)
	result.Candidates = appitempaging.TranscriptItemWindow{Candidates: selected}
	result.TurnIDs = turnIDs
	result.Exhausted = true
	return result, nil
}

func continuationTurnIDs(
	result transcriptItemCandidateResult,
	boundaryTurnID string,
	direction transcriptWindowContinuationDirection,
) ([]string, error) {
	turnIDs := result.TurnIDs
	if len(turnIDs) == 0 {
		seen := make(map[string]bool)
		for _, candidate := range result.Candidates.Candidates {
			if candidate.TurnID == "" || seen[candidate.TurnID] {
				continue
			}
			seen[candidate.TurnID] = true
			turnIDs = append(turnIDs, candidate.TurnID)
		}
	}
	boundaryIndex := slices.Index(turnIDs, boundaryTurnID)
	if boundaryIndex < 0 {
		return nil, appwire.TranscriptItemCursorStale()
	}
	if direction == transcriptWindowContinuationBackward {
		return append([]string(nil), turnIDs[:boundaryIndex]...), nil
	}
	return append([]string(nil), turnIDs[boundaryIndex:]...), nil
}

func candidatesForTurnIDs(
	candidates []appitempaging.TranscriptItemCandidate,
	turnIDs []string,
) []appitempaging.TranscriptItemCandidate {
	selectedTurns := make(map[string]bool, len(turnIDs))
	for _, turnID := range turnIDs {
		selectedTurns[turnID] = true
	}
	selected := make([]appitempaging.TranscriptItemCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if selectedTurns[candidate.TurnID] {
			selected = append(selected, candidate)
		}
	}
	return selected
}

type packItemCandidatesOptions struct {
	requestedLimit int
	window         *transcriptWindowPackOptions
}

type transcriptWindowCandidateTurn struct {
	turn       appwire.Turn
	candidates []appitempaging.TranscriptItemCandidate
}

func transcriptWindowCandidateTurns(
	result transcriptItemCandidateResult,
	candidates []appitempaging.TranscriptItemCandidate,
) []transcriptWindowCandidateTurn {
	candidatesByTurn := make(map[string][]appitempaging.TranscriptItemCandidate)
	fallbackShells := make([]appwire.Turn, 0)
	for _, candidate := range candidates {
		if len(candidatesByTurn[candidate.TurnID]) == 0 {
			fallbackShells = append(fallbackShells, candidate.Turn)
		}
		candidatesByTurn[candidate.TurnID] = append(candidatesByTurn[candidate.TurnID], candidate)
	}

	shells := make([]appwire.Turn, 0, len(result.TurnShells)+len(fallbackShells))
	seenShell := make(map[string]bool, len(result.TurnShells)+len(fallbackShells))
	for _, turn := range slices.Concat(result.TurnShells, fallbackShells) {
		if turn.ID == "" || seenShell[turn.ID] {
			continue
		}
		shells = append(shells, turn)
		seenShell[turn.ID] = true
	}
	shells = appitempaging.PreserveSelectedTurnShells(shells, result.TurnIDs)

	turns := make([]transcriptWindowCandidateTurn, 0, len(shells))
	for _, shell := range shells {
		turns = append(turns, transcriptWindowCandidateTurn{
			turn: shell, candidates: candidatesByTurn[shell.ID],
		})
	}
	return turns
}

func emptyTranscriptWindowDigest(result transcriptItemCandidateResult) (string, error) {
	turns := transcriptWindowCandidateTurns(result, result.Candidates.Candidates)
	shells := make([]appwire.Turn, len(turns))
	for index, turn := range turns {
		if len(turn.candidates) != 0 {
			return "", errors.New("empty transcript window digest includes positioned candidates")
		}
		shell := appwire.CloneTurn(turn.turn)
		shell.Items = nil
		shell.Elision = nil
		shells[index] = shell
	}
	encoded, err := json.Marshal(shells)
	if err != nil {
		return "", fmt.Errorf("marshal empty transcript window shells: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func transcriptWindowBoundaryCandidate(
	turns []transcriptWindowCandidateTurn,
	start int,
	end int,
) (appitempaging.TranscriptItemCandidate, error) {
	for index := start; index < end; index++ {
		if len(turns[index].candidates) > 0 {
			return turns[index].candidates[0], nil
		}
	}
	return appitempaging.TranscriptItemCandidate{}, errors.New("transcript window continuation has no positioned boundary item")
}

func setTranscriptWindowContinuationProof(
	continuation *transcriptWindowContinuation,
	turns []transcriptWindowCandidateTurn,
	boundaryStart int,
	boundaryEnd int,
	resumeStart int,
	resumeEnd int,
) error {
	// A resumed range with no positioned item has no version to fence, so its
	// proof is a digest of the shells it will replay. Anchoring a served-side
	// item instead would leave those shells unvalidated: a changed shell, or a
	// newly positioned item inside one, would replay as if nothing moved.
	if !transcriptWindowHasPositionedCandidates(turns, resumeStart, resumeEnd) {
		remaining := transcriptItemCandidateResult{}
		for _, turn := range turns[resumeStart:resumeEnd] {
			remaining.TurnIDs = append(remaining.TurnIDs, turn.turn.ID)
			remaining.TurnShells = append(remaining.TurnShells, turn.turn)
		}
		digest, err := emptyTranscriptWindowDigest(remaining)
		if err != nil {
			return err
		}
		continuation.EmptyRangeDigest = digest
		return nil
	}

	boundary, err := transcriptWindowBoundaryCandidate(turns, boundaryStart, boundaryEnd)
	if err != nil {
		boundary, err = transcriptWindowBoundaryCandidate(turns, resumeStart, resumeEnd)
	}
	if err != nil {
		return err
	}
	continuation.AnchorTurnID = boundary.TurnID
	continuation.Position = boundary.Position
	continuation.Version = boundary.Item.Version
	return nil
}

func transcriptWindowHasPositionedCandidates(turns []transcriptWindowCandidateTurn, start int, end int) bool {
	for index := start; index < end; index++ {
		if len(turns[index].candidates) > 0 {
			return true
		}
	}
	return false
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
	TurnIDs    []string
	TurnShells []appwire.Turn
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
		TurnIDs:    turnIDsFromTurns(turns),
		TurnShells: cloneTranscriptTurnShells(turns),
		Exhausted:  olderCursor == "",
	}, nil
}

func cloneTranscriptTurnShells(turns []appwire.Turn) []appwire.Turn {
	return appwire.CloneTurns(turns)
}

func turnIDsFromTurns(turns []appwire.Turn) []string {
	seen := make(map[string]bool, len(turns))
	turnIDs := make([]string, 0, len(turns))
	for _, turn := range turns {
		if turn.ID == "" || seen[turn.ID] {
			continue
		}
		seen[turn.ID] = true
		turnIDs = append(turnIDs, turn.ID)
	}
	return turnIDs
}

func sourceItemCandidateResultForRead(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, response appwire.ThreadReadResponse) (transcriptItemCandidateResult, error) {
	var result transcriptItemCandidateResult
	if itemSource, ok := source.(appsource.ItemReadCandidateSource); ok {
		sourceResult, err := itemSource.ItemCandidatesFromRead(ctx, params, response)
		if err != nil {
			return transcriptItemCandidateResult{}, err
		}
		result = transcriptItemCandidateResultFromSource(sourceResult)
		result.TurnIDs = turnIDsFromTurns(response.Thread.Turns)
		result.TurnShells = cloneTranscriptTurnShells(response.Thread.Turns)
	} else {
		var err error
		result, err = itemCandidateResultFromReadResponse(response)
		if err != nil {
			return transcriptItemCandidateResult{}, err
		}
	}
	if params.TurnWindow == nil {
		return result, nil
	}
	return appendReadOverlayCandidates(result, response)
}

// appendReadOverlayCandidates gives the existing window packer the selected
// live overlay beside the settled source snapshot. A running tool's overlay
// names the recorded call item it supersedes, so it replaces that candidate
// rather than joining it: the packer would otherwise count one call twice
// against the item limit and could serve the settled item while withholding
// its live output. Overlay items have no history position or version, so a
// merged item keeps the recorded position and an appended one gets an
// ephemeral position after the selected settled window; either can be
// displayed and bounded but never acquires a fetch cursor.
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
		if overlay.Kind == appwire.OverlayTool && overlay.HistoryKey != "" {
			if merged := mergeOverlayToolCandidate(candidates, overlay); merged {
				continue
			}
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

// mergeOverlayToolCandidate replaces the recorded candidate a running tool's
// overlay supersedes. The live item cannot be fetched: its bytes are not
// retained, so the merged candidate carries no version and no cursor while
// keeping the recorded position and turn.
func mergeOverlayToolCandidate(candidates []appitempaging.TranscriptItemCandidate, overlay appwire.OverlayItem) bool {
	for index := range candidates {
		if candidates[index].Item.TranscriptKey != overlay.HistoryKey {
			continue
		}
		merged := appwire.CloneThreadItem(overlay.Item)
		merged.TurnID = candidates[index].TurnID
		merged.TranscriptKey = candidates[index].Item.TranscriptKey
		position := candidates[index].Position
		merged.Position = &position
		merged.Version = 0
		merged.FetchCursor = ""
		candidates[index].Item = merged
		return true
	}
	return false
}

func sourceItemCandidateResultForList(ctx context.Context, source appsource.Source, params appwire.ThreadTurnsListParams, response appwire.ThreadTurnsListResponse) (transcriptItemCandidateResult, error) {
	if itemSource, ok := source.(appsource.ItemCandidateSource); ok {
		result, err := itemSource.ListItemCandidates(ctx, params)
		if err != nil {
			return transcriptItemCandidateResult{}, err
		}
		converted := transcriptItemCandidateResultFromSource(result)
		converted.TurnIDs = turnIDsFromTurns(response.Data)
		converted.TurnShells = cloneTranscriptTurnShells(response.Data)
		return converted, nil
	}
	result, err := itemCandidateResultFromTurns(response.Data, response.NextCursor)
	result.History = appsource.PageHistoryIdentity(response)
	return result, err
}

func threadWithPackedTurns(base appwire.Thread, turns []appwire.Turn) appwire.Thread {
	base.Turns = turns
	return base
}

func canonicalizeTranscriptItemCandidates(
	source appsource.Source,
	base appwire.Thread,
	candidates []appitempaging.TranscriptItemCandidate,
) ([]appitempaging.TranscriptItemCandidate, error) {
	return canonicalizeTranscriptItemCandidatesWith(base, candidates, func(thread appwire.Thread) appwire.Thread {
		return enrichSourcedThreadImages(source, thread)
	})
}

func canonicalizeTranscriptItemCandidatesWith(
	base appwire.Thread,
	candidates []appitempaging.TranscriptItemCandidate,
	canonicalize func(appwire.Thread) appwire.Thread,
) ([]appitempaging.TranscriptItemCandidate, error) {
	turns, err := appitempaging.RegroupTurnFragments(candidates)
	if err != nil {
		return nil, err
	}
	canonical := canonicalize(threadWithPackedTurns(base, turns))
	items := make(map[appwire.ThreadItemPosition]appwire.ThreadItem, len(candidates))
	for _, turn := range canonical.Turns {
		for _, item := range turn.Items {
			if item.Position != nil {
				items[*item.Position] = item
			}
		}
	}
	result := append([]appitempaging.TranscriptItemCandidate(nil), candidates...)
	for index := range result {
		item, ok := items[result[index].Position]
		if !ok {
			return nil, fmt.Errorf("canonical transcript item missing at position %+v", result[index].Position)
		}
		result[index].Item = item
	}
	return result, nil
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
	}, func(response appwire.ThreadReadResponse) []appwire.Turn { return response.Thread.Turns }, func(response appwire.ThreadReadResponse) appwire.ThreadTurnsListResponse {
		return appwire.ThreadTurnsListResponse{
			Data: response.Thread.Turns, NextCursor: response.OlderCursor, Window: response.Window,
			BootGeneration: response.BootGeneration, Epoch: response.Epoch, Snapshot: response.Snapshot,
			Authoritative: response.Authoritative,
		}
	}, func(response appwire.ThreadReadResponse, window *appwire.ThreadWindowResult) appwire.ThreadReadResponse {
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
	}, func(response appwire.ThreadTurnsListResponse) []appwire.Turn { return response.Data }, func(response appwire.ThreadTurnsListResponse) appwire.ThreadTurnsListResponse {
		return response
	}, func(response appwire.ThreadTurnsListResponse, window *appwire.ThreadWindowResult) appwire.ThreadTurnsListResponse {
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
	// page projects the response into the thread/turns page a consumer of the
	// same bytes receives, so the packer can measure that consumer's complete
	// envelope (its recovery follow-ups included) before serving a page.
	page func(T) appwire.ThreadTurnsListResponse,
	setWindow func(T, *appwire.ThreadWindowResult) T,
	options packItemCandidatesOptions,
) (T, error) {
	var zero T
	var err error
	candidates := append([]appitempaging.TranscriptItemCandidate(nil), result.Candidates.Candidates...)
	if options.window != nil && options.window.CanonicalizeCandidates != nil {
		candidates, err = options.window.CanonicalizeCandidates(candidates)
		if err != nil {
			return zero, fmt.Errorf("canonicalize transcript item candidates: %w", err)
		}
	}
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

		candidateTurns := transcriptWindowCandidateTurns(result, candidates)

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
				// An in-flight item has no version, so it cannot fence a
				// continuation. Anchor on the first settled withheld candidate;
				// withheld in-flight items are recovered by rereading the turn.
				for _, withheld := range selectedCandidates[itemLimit:] {
					if withheld.Item.Version != 0 {
						first := withheld
						preparedTurn.firstCapWithheld = &first
						break
					}
				}
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
						Direction:    transcriptWindowContinuationForward,
						TurnID:       preparedTurn.turn.ID,
						AnchorTurnID: firstContinuation.TurnID,
						Position:     firstContinuation.Position,
						Version:      firstContinuation.Item.Version,
						ResumeWindow: &appwire.ThreadTurnWindow{Turn: preparedTurn.turn.ID},
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
				if preparedTurn.capWithheld > 0 {
					elision.CapWithheldRecovery = appwire.ThreadElisionRecoveryRereadSettled
					if elision.ContinuationCursor != "" {
						elision.CapWithheldRecovery = appwire.ThreadElisionRecoveryContinuation
					}
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
					request.ResumeWindow = &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{
						TurnID: turns[0].ID, Limit: windowResult.WithheldTurns,
					}}
					if proofErr := setTranscriptWindowContinuationProof(&request, candidateTurns, start, end, 0, start); proofErr != nil {
						return zero, proofErr
					}
				} else if end < len(prepared) {
					windowResult.FirstWithheldTurnID = prepared[end].turn.ID
					windowResult.LastWithheldTurnID = prepared[len(prepared)-1].turn.ID
					request.TurnID = prepared[end].turn.ID
					request.ResumeWindow = &appwire.ThreadTurnWindow{
						Since: turns[len(turns)-1].ID, Limit: windowResult.WithheldTurns,
					}
					if proofErr := setTranscriptWindowContinuationProof(&request, candidateTurns, end, len(candidateTurns), end, len(candidateTurns)); proofErr != nil {
						return zero, proofErr
					}
				} else {
					// An incomplete latest source page proves how many requested turns
					// are absent but cannot name their IDs. Continue immediately before
					// the oldest served position through the source-owned identity.
					request.TurnID = prepared[0].turn.ID
					request.AnchorTurnID = candidateTurns[0].candidates[0].TurnID
					request.Position = candidateTurns[0].candidates[0].Position
					request.Version = candidateTurns[0].candidates[0].Item.Version
					request.ResumeWindow = &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{
						TurnID: turns[0].ID, Limit: windowResult.WithheldTurns,
					}}
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
			contentFits := len(encoded) <= effectiveBudget
			if contentFits && windowOptions.StructuredFollowUpParams != nil {
				structuredBytes, marshalErr := transcriptWindowStructuredPageSize(page(response), *windowOptions.StructuredFollowUpParams)
				if marshalErr != nil {
					return zero, marshalErr
				}
				if structuredBytes > transcriptWindowMaximumContentBudget {
					// Reserve the measured overflow in the content budget and
					// keep trimming against the cheaper content-only measure.
					// A dropped turn also drops its follow-up, so the reduced
					// budget stays an upper bound on the structured page and
					// no iteration returns an over-cap response.
					contentFits = false
					effectiveBudget = max(len(encoded)-(structuredBytes-transcriptWindowMaximumContentBudget), 0)
				}
			}
			if contentFits || end-start == 1 {
				return response, nil
			}
			if direction == transcriptWindowContinuationForward {
				end--
				for end > start+1 && len(candidateTurns[end].candidates) == 0 {
					end--
				}
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
		if hasOlder {
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
	token, err := options.fetchToken(candidate)
	if err != nil {
		return "", err
	}
	cursor, err := options.MintFetchCursor(candidate, token)
	if err != nil {
		return "", fmt.Errorf("mint transcript item fetch cursor: %w", err)
	}
	if cursor == "" {
		return "", errors.New("transcript item fetch cursor callback returned an empty cursor")
	}
	if options.RetainFetchItem != nil {
		if err := options.RetainFetchItem(token, candidate); err != nil {
			return "", fmt.Errorf("retain transcript item fetch bytes: %w", err)
		}
	}
	return cursor, nil
}

// transcriptWindowFetchTokens carries the window identity and the view owner's
// token assignment into the packer's mint callbacks.
type transcriptWindowFetchTokens struct {
	identity appitempaging.CursorIdentity
	assign   func(appitempaging.CursorIdentity, appitempaging.TranscriptItemCandidate) (string, error)
}

func (o transcriptWindowPackOptions) fetchToken(candidate appitempaging.TranscriptItemCandidate) (string, error) {
	if o.fetchTokens == nil {
		return defaultFetchToken(appitempaging.CursorIdentity{}, candidate), nil
	}
	if o.fetchTokens.assign != nil {
		return o.fetchTokens.assign(o.fetchTokens.identity, candidate)
	}
	return defaultFetchToken(o.fetchTokens.identity, candidate), nil
}

// defaultFetchToken is the deterministic token for a packer with no retained
// store. It never revives an evicted entry because such a packer retains none.
func defaultFetchToken(identity appitempaging.CursorIdentity, candidate appitempaging.TranscriptItemCandidate) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d", identity.ThreadRef, identity.Incarnation,
		candidate.Position.Entry, candidate.Position.Item, candidate.Item.Version)))
	return hex.EncodeToString(sum[:8]) + "-" + hex.EncodeToString(sum[8:16])
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
