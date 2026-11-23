package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/foldcache"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type delegateAttentionWriterOpener func(string, string) (*transcript.Writer, []transcript.Entry, error)

func delegateTranscriptPathFromRef(stateDir, ref string) (string, string, error) {
	projectID, sessionID, err := decodeRef(ref)
	if err != nil {
		return "", "", err
	}
	if projectID != "" {
		return "", "", fmt.Errorf("delegate transcript ref %q leaves controller state directory", ref)
	}
	return transcriptPath(stateDir, sessionID), sessionID, nil
}

func readPendingDelegateAttention(path, expectedSessionID string) ([]string, error) {
	fold, err := readDelegateAttentionFold(path, expectedSessionID)
	if err != nil {
		return nil, err
	}
	return fold.pendingIDs(), nil
}

func readDelegateAttentionFold(path, expectedSessionID string) (delegateAttentionFold, error) {
	fold, _, err := readDelegateAttentionFoldConsuming(path, expectedSessionID)
	return fold, err
}

// readDelegateAttentionFoldConsuming is readDelegateAttentionFold reporting the
// byte offset through the last complete line the reader consumed. That count
// is the foldcache Extend contract's consumed-through offset: it excludes the
// unterminated trailing line ReadLine drains and discards, and it counts only
// bytes the reader actually saw, so an append or a repair truncation moving
// EOF mid-read is reflected exactly. A missing transcript reports the empty
// fold at offset 0, the same missing-as-empty semantics as
// readDelegateAttentionFold.
func readDelegateAttentionFoldConsuming(path, expectedSessionID string) (delegateAttentionFold, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newDelegateAttentionFold(), 0, nil
		}
		return delegateAttentionFold{}, 0, fmt.Errorf("open delegate attention transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	return extendDelegateAttentionFoldFrom(f, 0, newDelegateAttentionFold(), expectedSessionID)
}

// extendDelegateAttentionFoldFrom folds the complete lines r yields onto
// prior, where r is positioned at byte offset from of the transcript. From
// byte zero it reads and checks the header first; from anywhere else the
// header was checked by the read that produced prior. It reports the offset
// through the last complete line, the contract readDelegateAttentionFoldConsuming
// documents.
func extendDelegateAttentionFoldFrom(r io.Reader, from int64, prior delegateAttentionFold, expectedSessionID string) (delegateAttentionFold, int64, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	headerRead := from > 0
	consumed := from
	entries := make([]transcript.Entry, 0)
	for {
		line, complete, read, readErr := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if readErr != nil {
			return delegateAttentionFold{}, 0, readErr
		}
		if !complete {
			// The drained unterminated tail is not consumed: a torn line that
			// completes later grows the file, and the next read refolds.
			break
		}
		consumed += read
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			header, err := transcript.DecodeHeader(line)
			if err != nil {
				return delegateAttentionFold{}, 0, err
			}
			if header.SessionID != expectedSessionID {
				return delegateAttentionFold{}, 0, fmt.Errorf("delegate attention transcript session %q, want %q", header.SessionID, expectedSessionID)
			}
			headerRead = true
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			return delegateAttentionFold{}, 0, err
		}
		entries = append(entries, entry)
	}
	if !headerRead {
		return delegateAttentionFold{}, 0, errors.New("delegate attention transcript has no header")
	}
	fold, err := extendDelegateAttention(prior, entries)
	if err != nil {
		return delegateAttentionFold{}, 0, err
	}
	return fold, consumed, nil
}

// readExistingDelegateAttentionFoldCompute is the compute step behind the
// read-path fold memo (see readExistingDelegateAttentionFold): the fold plus
// the bytes it consumed, the foldcache Extend contract's toOffset. A package
// var, like scanDelegateJournal and scanJobJournal in jobs_activity_past.go, so
// tests can count fold computations without instrumenting the filesystem.
var readExistingDelegateAttentionFoldCompute = readDelegateAttentionFoldConsuming

// delegateAttentionFoldCacheEntries bounds the read-path fold memo by number of
// distinct transcripts retained. One memo entry holds one delegateAttentionFold
// — the attention turns, resolutions, and delivery commits of one child, not
// its whole transcript — so entries are small and bounded by that child's
// attention traffic, and a hub reading a fleet of delegate-heavy sessions keeps
// at most this many folds resident. Sizing follows the same distinct-journal
// budgeting style as historicalDelegateFoldCacheEntries: a generous count for
// a cheaply-sized payload.
const delegateAttentionFoldCacheEntries = 512

// delegateAttentionFoldMemo is one cached fold's payload. The session id rides
// in the payload because foldcache keys by path alone: a transcript path reused
// across sessions must never serve another session's fold, so
// readExistingDelegateAttentionFold checks it after a hit.
type delegateAttentionFoldMemo struct {
	sessionID string
	fold      delegateAttentionFold
}

// delegateAttentionFoldCache memoizes read-path attention folds by transcript
// path, with foldcache's staleness rules standing in for the hand-rolled
// size-and-mtime gate this used to be: an append-only transcript that changed
// recomputes, an unchanged one serves the memo. Without it, every status sweep
// re-read and re-decoded every eligible delegate child's full transcript —
// O(total delegate bytes) per thread/read (a 251-child session measured
// 175.6MB re-read per click). The cached fold is shared and MUST be treated as
// read-only by callers, the same contract TurnCache documents for its
// memoized slices.
var delegateAttentionFoldCache = foldcache.New[delegateAttentionFoldMemo](delegateAttentionFoldCacheEntries)

// extendDelegateAttentionFold is the Extend delegateAttentionFoldCache reads a
// transcript through. Every call refolds from byte zero and fromOffset/prior go
// unused, although the fold itself could resume (extendDelegateAttention is a
// left fold); the cache pays for itself by skipping the read entirely while a
// transcript is unchanged, the hot case in the per-click status sweeps. The
// ctx an Extend call runs with is detached from every caller by
// foldcache.Get, so there is no cancellation to check mid-read.
func extendDelegateAttentionFold(expectedSessionID string) foldcache.Extend[delegateAttentionFoldMemo] {
	return func(_ context.Context, path string, _ int64, _ delegateAttentionFoldMemo) (delegateAttentionFoldMemo, int64, bool, error) {
		// toOffset is the byte count the reader itself consumed, through the
		// last complete line — never a size stat'd against the file from the
		// outside. A stat can only bound the fold from beyond the reader: an
		// append racing the read moves EOF forward underneath it and a repair
		// truncation moves it back, and no stat before or after can say which
		// bytes the fold actually saw. The consumed count is exact by
		// construction (see readDelegateAttentionFoldConsuming): it excludes
		// the drained unterminated tail and includes only bytes read, so the
		// recorded offset can never claim content the fold did not fold. The
		// append race would otherwise serve a turn-less fold as an
		// unchanged-transcript hit indefinitely
		// (TestReadExistingDelegateAttentionFoldRacingAppendStaysVisible),
		// and the repair race would probe past the truncated file and fail
		// the read
		// (TestReadExistingDelegateAttentionFoldRepairRacingTheFoldSucceeds).
		fold, consumed, err := readExistingDelegateAttentionFoldCompute(path, expectedSessionID)
		if err != nil {
			return delegateAttentionFoldMemo{}, 0, false, err
		}
		// readDelegateAttentionFold is missing-as-empty for historical
		// callers; this boundary is strict, including a removal racing the
		// fold, so a transcript that vanished between the size stat above and
		// the fold's open errors here instead of serving (and caching) an
		// empty fold. The bypass in delegate_tree_attention.go carries the
		// same check for the sessions it serves directly.
		if _, err := os.Stat(path); err != nil {
			return delegateAttentionFoldMemo{}, 0, false, fmt.Errorf("stat delegate attention transcript after read: %w", err)
		}
		return delegateAttentionFoldMemo{sessionID: expectedSessionID, fold: fold}, consumed, false, nil
	}
}

type delegateAttentionFold struct {
	order             []string
	content           map[string]llm.Message
	turns             map[string]schema.Turn
	resolutions       map[string]delegateAttentionResolution
	resumeGenerations map[string]uint64
	deliveryCommits   map[string]string
}

func foldDelegateAttention(entries []transcript.Entry) (delegateAttentionFold, error) {
	return extendDelegateAttention(newDelegateAttentionFold(), entries)
}

// extendDelegateAttention continues the fold prior with entries. The fold is a
// strict left fold: each entry is checked and applied against the state the
// entries before it built, and nothing an entry does reaches back to change an
// earlier decision, so folding a transcript's prefix and then extending with
// the rest decides exactly what one fold over the whole transcript decides.
// prior is never modified; a caller may still hold it.
func extendDelegateAttention(prior delegateAttentionFold, entries []transcript.Entry) (delegateAttentionFold, error) {
	if len(entries) == 0 {
		return prior, nil
	}
	fold := prior.clone()
	for _, entry := range entries {
		turn := entry.Turn
		if err := foldDelegateDeliveryCommits(&fold, turn); err != nil {
			return delegateAttentionFold{}, err
		}
		if turn.AttentionID != "" {
			if turn.Kind != schema.TurnSteering || turn.AttentionResolution != nil {
				return delegateAttentionFold{}, fmt.Errorf("attention %q is not a steering turn", turn.AttentionID)
			}
			if previous, exists := fold.content[turn.AttentionID]; exists {
				if !reflect.DeepEqual(previous, turn.Message) {
					return delegateAttentionFold{}, fmt.Errorf("attention %q has conflicting content", turn.AttentionID)
				}
			} else {
				fold.content[turn.AttentionID] = turn.Message
				fold.turns[turn.AttentionID] = turn
				fold.order = append(fold.order, turn.AttentionID)
			}
		}
		resolution := turn.AttentionResolution
		if resolution == nil {
			if turn.Kind == schema.TurnAttentionResolution {
				return delegateAttentionFold{}, errors.New("attention resolution turn has no resolution")
			}
			continue
		}
		if turn.Kind != schema.TurnAttentionResolution || turn.AttentionID != "" || resolution.AttentionID == "" {
			return delegateAttentionFold{}, errors.New("invalid attention resolution turn")
		}
		disposition := delegateAttentionResolution(resolution.Disposition)
		if disposition != delegateAttentionConsumed && disposition != delegateAttentionDiscarded {
			return delegateAttentionFold{}, fmt.Errorf("attention %q has invalid resolution %q", resolution.AttentionID, resolution.Disposition)
		}
		if disposition == delegateAttentionDiscarded && resolution.ResumeGeneration != 0 {
			return delegateAttentionFold{}, fmt.Errorf("discarded attention %q has resume generation %d", resolution.AttentionID, resolution.ResumeGeneration)
		}
		if _, exists := fold.content[resolution.AttentionID]; !exists {
			return delegateAttentionFold{}, fmt.Errorf("attention %q resolved before it was appended", resolution.AttentionID)
		}
		if previous, exists := fold.resolutions[resolution.AttentionID]; exists {
			if previous != disposition || fold.resumeGenerations[resolution.AttentionID] != resolution.ResumeGeneration {
				return delegateAttentionFold{}, fmt.Errorf("attention %q has conflicting resolutions", resolution.AttentionID)
			}
			continue
		}
		if previousID, conflict := delegateAttentionResumeClaimConflict(fold, resolution.AttentionID, resolution.ResumeGeneration); conflict {
			return delegateAttentionFold{}, fmt.Errorf("resume generation %d claims attention %q and %q", resolution.ResumeGeneration, previousID, resolution.AttentionID)
		}
		fold.resolutions[resolution.AttentionID] = disposition
		fold.resumeGenerations[resolution.AttentionID] = resolution.ResumeGeneration
	}
	return fold, nil
}

// delegateAttentionResumeClaimConflict scans the fold's resume claims for a
// live claim on the same generation by an id other than attentionID. One
// generation presents one report, so two ids cannot both claim it. A claim
// is live until its own reopen row exists: the failed start re-delivered
// the same report under the fresh identity, so the claim it left binds
// nothing and any pending row may take the generation over — the same
// liveness the restore read applies to owed starts, so the two cannot
// disagree about which claims still bind. Map order cannot change the
// verdict: the scan visits every claim and conflicts on the first live one.
func delegateAttentionResumeClaimConflict(fold delegateAttentionFold, attentionID string, generation uint64) (string, bool) {
	if generation == 0 {
		return "", false
	}
	for previousID, claimed := range fold.resumeGenerations {
		if claimed != generation || previousID == attentionID {
			continue
		}
		if _, superseded := fold.content[delegateAttentionReopenID(previousID, claimed)]; superseded {
			continue
		}
		return previousID, true
	}
	return "", false
}

func foldDelegateDeliveryCommits(fold *delegateAttentionFold, turn schema.Turn) error {
	if len(turn.DelegateDeliveryCommits) == 0 {
		return nil
	}
	if turn.Kind != schema.TurnToolResults {
		return errors.New("delegate delivery commits require a tool-results turn")
	}
	resultIDs := make(map[string]struct{})
	for _, part := range turn.Message.Content {
		if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID != "" {
			resultIDs[part.ToolResult.ToolCallID] = struct{}{}
		}
	}
	// A tool call may commit several deliveries, all in its own turn (a
	// delegate_send wait carries earlier results ahead of its own, #3906); a
	// call an earlier turn committed takes no more.
	earlierCalls := make(map[string]struct{}, len(fold.deliveryCommits))
	for _, toolCallID := range fold.deliveryCommits {
		earlierCalls[toolCallID] = struct{}{}
	}
	for _, commit := range turn.DelegateDeliveryCommits {
		if commit.ToolCallID == "" || commit.DeliveryID == "" {
			return errors.New("delegate delivery commit identity is incomplete")
		}
		if _, exists := resultIDs[commit.ToolCallID]; !exists {
			return fmt.Errorf("delegate delivery %q references absent tool call %q", commit.DeliveryID, commit.ToolCallID)
		}
		previous, exists := fold.deliveryCommits[commit.DeliveryID]
		if exists && previous != commit.ToolCallID {
			return fmt.Errorf("delegate delivery %q has conflicting tool calls", commit.DeliveryID)
		}
		if _, committed := earlierCalls[commit.ToolCallID]; committed && !exists {
			return fmt.Errorf("delegate tool call %q has conflicting deliveries", commit.ToolCallID)
		}
		fold.deliveryCommits[commit.DeliveryID] = commit.ToolCallID
	}
	return nil
}

func newDelegateAttentionFold() delegateAttentionFold {
	return delegateAttentionFold{
		content:           make(map[string]llm.Message),
		turns:             make(map[string]schema.Turn),
		resolutions:       make(map[string]delegateAttentionResolution),
		resumeGenerations: make(map[string]uint64),
		deliveryCommits:   make(map[string]string),
	}
}

func (f delegateAttentionFold) clone() delegateAttentionFold {
	return delegateAttentionFold{
		order:             slices.Clone(f.order),
		content:           maps.Clone(f.content),
		turns:             maps.Clone(f.turns),
		resolutions:       maps.Clone(f.resolutions),
		resumeGenerations: maps.Clone(f.resumeGenerations),
		deliveryCommits:   maps.Clone(f.deliveryCommits),
	}
}

func (f delegateAttentionFold) pendingIDs() []string {
	pending := make([]string, 0, len(f.order))
	for _, attentionID := range f.order {
		if _, resolved := f.resolutions[attentionID]; !resolved {
			pending = append(pending, attentionID)
		}
	}
	return pending
}

// pendingTurns returns the durable model-visible turn behind every pending
// attention item, and behind the resolved item keep names, in arrival order.
func (f delegateAttentionFold) pendingTurns(keep string) []schema.Turn {
	var turns []schema.Turn
	for _, attentionID := range f.order {
		_, resolved := f.resolutions[attentionID]
		if resolved && attentionID != keep {
			continue
		}
		if turn, ok := f.turns[attentionID]; ok {
			turns = append(turns, turn)
		}
	}
	return turns
}

// turnsForArriving returns the durable model-visible turn behind each of ids in
// transcript arrival order, so an earlier update precedes a later delivery even
// though the delivery's id sorts first. An identity the transcript never
// carried is skipped.
func (f delegateAttentionFold) turnsForArriving(ids []string) []schema.Turn {
	if len(ids) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, attentionID := range ids {
		allowed[attentionID] = struct{}{}
	}
	turns := make([]schema.Turn, 0, len(ids))
	for _, attentionID := range f.order {
		if _, ok := allowed[attentionID]; !ok {
			continue
		}
		if turn, ok := f.turns[attentionID]; ok {
			turns = append(turns, turn)
		}
	}
	return turns
}

func appendColdDelegateNotificationDurablyWithOpen(path, expectedSessionID, attentionID, content string, now time.Time, open delegateAttentionWriterOpener) (appended bool, err error) {
	return appendColdDelegateAttentionMessageDurablyWithOpen(path, expectedSessionID, attentionID, llm.User(content), now, open)
}

func appendColdDelegateAttentionMessageDurablyWithOpen(path, expectedSessionID, attentionID string, message llm.Message, now time.Time, open delegateAttentionWriterOpener) (appended bool, err error) {
	if expectedSessionID == "" || attentionID == "" {
		return false, errors.New("cold delegate attention identity is incomplete")
	}
	if message.Role != llm.RoleUser || len(message.Content) == 0 {
		return false, errors.New("cold delegate attention message is invalid")
	}
	if open == nil {
		return false, errors.New("cold delegate attention writer opener is nil")
	}
	preflight, err := readDelegateAttentionFold(path, expectedSessionID)
	if err != nil {
		return false, err
	}
	if previous, exists := preflight.content[attentionID]; exists {
		if !reflect.DeepEqual(previous, message) {
			return false, fmt.Errorf("attention %q has conflicting content", attentionID)
		}
	}
	writer, entries, err := open(path, expectedSessionID)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	fold, err := foldDelegateAttention(entries)
	if err != nil {
		return false, err
	}
	if previous, exists := fold.content[attentionID]; exists {
		if !reflect.DeepEqual(previous, message) {
			return false, fmt.Errorf("attention %q has conflicting content", attentionID)
		}
		if err := writer.EstablishDurability(); err != nil {
			return false, err
		}
		return false, nil
	}
	turn := schema.NewTurn(schema.TurnSteering, message)
	turn.Timestamp = now.UTC()
	turn.AttentionID = attentionID
	turn.StableTurnID = newQueueEntryID()
	// The synced door is the durability owner's door: it records AND syncs,
	// or errors. A read-back would find a retained (recorded but unsynced)
	// line and wrongly call it durable, so this side of delegate attention asks
	// the writer rather than the file. A cold writer has no session, so the
	// entry is a delivery turn of its own.
	if _, err := writer.Record(turn, transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceDelivery}); err != nil {
		return false, fmt.Errorf("attention %q was not durably appended: %w", attentionID, err)
	}
	verified, err := readDelegateAttentionFold(path, expectedSessionID)
	if err != nil {
		return false, err
	}
	persisted, exists := verified.content[attentionID]
	if !exists || !reflect.DeepEqual(persisted, message) {
		return false, fmt.Errorf("attention %q was not durably appended", attentionID)
	}
	return true, nil
}

func delegateAttentionResolutionTurn(attentionID string, disposition delegateAttentionResolution) schema.Turn {
	return delegateAttentionResolutionTurnForGeneration(attentionID, disposition, 0)
}

func delegateAttentionResolutionTurnForGeneration(attentionID string, disposition delegateAttentionResolution, resumeGeneration uint64) schema.Turn {
	turn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("Attention resolved."))
	turn.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID:      attentionID,
		Disposition:      string(disposition),
		ResumeGeneration: resumeGeneration,
	}
	return turn
}

// discardUnlessResolvedResolution maps the discard-unless-resolved mode to the
// concrete resolution its fold decides: the transcript's own recorded
// disposition and resume generation when it has one, and a discard with no
// resume generation otherwise. Every other disposition passes through. It runs
// on the same fold the caller validates and appends against, so the mode never
// leaves a check/append gap.
func discardUnlessResolvedResolution(fold delegateAttentionFold, ids []string, disposition delegateAttentionResolution, resumeGeneration uint64) (delegateAttentionResolution, uint64, error) {
	if disposition != delegateAttentionDiscardUnlessResolved {
		return disposition, resumeGeneration, nil
	}
	if err := validateDiscardUnlessResolvedShape(disposition, ids, resumeGeneration); err != nil {
		return "", 0, err
	}
	if recorded, resolved := fold.resolutions[ids[0]]; resolved {
		return recorded, fold.resumeGenerations[ids[0]], nil
	}
	return delegateAttentionDiscarded, 0, nil
}

var errDiscardUnlessResolvedShape = errors.New("discard-unless-resolved resolution requires one attention ID and no resume generation")

// validateDiscardUnlessResolvedShape keeps the resolver mode's "one attention
// ID, no resume generation" rule in one place. A non-mode disposition passes
// through to validateDelegateAttentionResolutions.
func validateDiscardUnlessResolvedShape(disposition delegateAttentionResolution, ids []string, resumeGeneration uint64) error {
	if disposition == delegateAttentionDiscardUnlessResolved && (len(ids) != 1 || resumeGeneration != 0) {
		return errDiscardUnlessResolvedShape
	}
	return nil
}

// appendDelegateNotificationDurably appends one private, model-bound attention
// item. Replaying the same identity and content is a no-op; reusing an identity
// for different content is corruption.
func (s *Session) appendDelegateNotificationDurably(attentionID, content string) (appended bool, err error) {
	return s.appendDelegateAttentionMessageDurably(attentionID, llm.User(content))
}

// delegateAttentionTurn is the steering turn an attention message appends
// as: a subagent's report or quiet watchdog, or a stable shell job's
// completion. It carries its attention identity and a fresh stable turn id,
// and no steering kind.
func delegateAttentionTurn(attentionID string, message llm.Message, now time.Time) schema.Turn {
	turn := schema.NewTurn(schema.TurnSteering, message)
	turn.Timestamp = now.UTC()
	turn.AttentionID = attentionID
	turn.StableTurnID = newQueueEntryID()
	return turn
}

// appendDelegateAttentionMessageDurably is the message-preserving core of
// appendDelegateNotificationDurably. Escalation of a fenced delegate's
// attention transfers the exact original message under its original identity,
// so both the resident and the cold writers stay byte-identical on replay.
func (s *Session) appendDelegateAttentionMessageDurably(attentionID string, message llm.Message) (appended bool, err error) {
	if s == nil {
		return false, errors.New("delegate attention session is nil")
	}
	if attentionID == "" {
		return false, errors.New("delegate attention ID is empty")
	}
	// An attention report about a delegate is news: the agent's next status
	// check on a delegate is not a repeat.
	s.jobStatusPolls.forgetDelegates()
	if message.Role != llm.RoleUser || len(message.Content) == 0 {
		return false, errors.New("delegate attention message is invalid")
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	// A child delegate's queue in the delegate journal is the only authority
	// for what it still owes, so the message enters it in the same call that
	// makes the message durable. The root keeps its transcript rail.
	queueChildRow := func() error {
		if s.delegateController == nil || s.owningDelegateID == "" || delegateAttentionDeliveryBacked(attentionID) {
			return nil
		}
		return s.delegateController.ensureDelegateAttentionQueuedIfUnresolved(s.owningDelegateID, attentionID)
	}

	s.mu.Lock()
	ready := s.transcriptReady
	writer := s.transcript
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || writer == nil || sessionID == "" || stateDir == "" {
		return false, errors.New("delegate attention requires an attached transcript writer")
	}
	path := transcriptPath(stateDir, sessionID)
	fold, err := s.readDelegateAttentionFold(path, sessionID)
	if err != nil {
		return false, err
	}
	deliveryID := strings.TrimPrefix(attentionID, "delegate:")
	if deliveryID != attentionID && fold.deliveryCommits[deliveryID] != "" {
		_, durableFold, err := s.reopenAttentionTranscriptDurably(writer, path, sessionID)
		if err != nil {
			return false, err
		}
		if durableFold.deliveryCommits[deliveryID] == "" {
			return false, fmt.Errorf("delegate delivery %q changed during durability recovery", deliveryID)
		}
		return false, nil
	}
	if previous, exists := fold.content[attentionID]; exists {
		if !reflect.DeepEqual(previous, message) {
			return false, fmt.Errorf("attention %q has conflicting content", attentionID)
		}
		_, durableFold, err := s.reopenAttentionTranscriptDurably(writer, path, sessionID)
		if err != nil {
			return false, err
		}
		if previous := durableFold.content[attentionID]; !reflect.DeepEqual(previous, message) {
			return false, fmt.Errorf("attention %q changed during durability recovery", attentionID)
		}
		if err := s.retainDelegateAttentionTurn(durableFold.turns[attentionID]); err != nil {
			return false, err
		}
		// The message is durable but the journal row may not be: the append that
		// wrote the transcript can fail before its queue event lands. The repair
		// leaves a resolved row alone, so a repeat route of an already-presented
		// report is still a no-op.
		if err := queueChildRow(); err != nil {
			return false, fmt.Errorf("attention %q was not queued: %w", attentionID, err)
		}
		return false, nil
	}
	turn := delegateAttentionTurn(attentionID, message, s.sclock().Now())
	// The synced door records AND syncs, or errors; a retained (recorded but
	// unsynced) line the read-back would find is not durable. Attention
	// arrives from delivery goroutines at any time, so it joins the running
	// execution or takes a delivery turn of its own.
	rec, err := writer.Record(s.withEntryModel(turn), transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceAsync})
	if err != nil {
		return false, fmt.Errorf("attention %q was not durably appended: %w", attentionID, err)
	}
	// The resident turn is the one the file holds, identity included.
	turn = rec.Turn
	if err := s.retainDelegateAttentionTurn(turn); err != nil {
		return false, err
	}
	verified, err := s.readDelegateAttentionFold(path, sessionID)
	if err != nil {
		return false, err
	}
	persisted, exists := verified.content[attentionID]
	if !exists || !reflect.DeepEqual(persisted, message) {
		s.removeUnverifiedDelegateAttentionTurn(turn)
		return false, fmt.Errorf("attention %q was not durably appended", attentionID)
	}
	if err := s.retainDelegateAttentionTurn(verified.turns[attentionID]); err != nil {
		return false, err
	}
	if err := queueChildRow(); err != nil {
		return false, fmt.Errorf("attention %q was not queued: %w", attentionID, err)
	}
	return true, nil
}

// armDelegateAttention arms one attention ID. It is the single-ID form of
// armDelegateAttentions.
func (s *Session) armDelegateAttention(attentionID string) error {
	return s.armDelegateAttentions([]string{attentionID})
}

// armDelegateAttentions arms one receiver's post-commit wake for several
// attention IDs. The root receiver reads its pending IDs and notifies once for
// the batch; the stop's batched acknowledgment retry is the caller that has
// several IDs.
func (s *Session) armDelegateAttentions(attentionIDs []string) error {
	if s == nil {
		return errors.New("delegate attention wake identity is incomplete")
	}
	ids := attentionIDs
	if len(ids) == 1 {
		if ids[0] == "" {
			return errors.New("delegate attention wake identity is incomplete")
		}
	} else {
		if slices.Contains(ids, "") {
			return errors.New("delegate attention wake identity is incomplete")
		}
		ids = appendUniqueStrings(nil, ids...)
	}
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	armFault := s.cfg.testOnly.delegateAttentionArmFault
	s.mu.Unlock()
	var err error
	if armFault != nil {
		// The fault stands in for an arm the controller refused, so its
		// error flows through the same retention below: a faulted arm is
		// recorded for the paced retry exactly as a real refusal is, and
		// the retry re-arms through armDelegateAttentionOnce, past the
		// fault.
		err = armFault()
	}
	if err == nil {
		err = s.armDelegateAttentionsOnce(ids)
	}
	s.attentionMu.Lock()
	if err != nil {
		if s.delegateAttentionArmIDs == nil {
			s.delegateAttentionArmIDs = make(map[string]struct{})
		}
		for _, attentionID := range ids {
			s.delegateAttentionArmIDs[attentionID] = struct{}{}
		}
		s.scheduleDelegateAttentionArmRetryLocked()
	} else {
		for _, attentionID := range ids {
			delete(s.delegateAttentionArmIDs, attentionID)
		}
		if len(s.delegateAttentionArmIDs) == 0 {
			s.resetDelegateAttentionArmRetryLocked()
		}
	}
	s.attentionMu.Unlock()
	return err
}

// armDelegateAttentionsOnce arms a batch for a receiver. Only the root
// receiver has several IDs to arm; a child receiver's wake is its notify, so
// the non-root path arms every ID against the controller and then wakes the
// receiver once for the whole batch — the same single wake a one-ID arm's
// caller gets — instead of once per ID.
func (s *Session) armDelegateAttentionsOnce(attentionIDs []string) error {
	if !s.isRootDelegateAttentionReceiver() {
		armed := false
		for _, attentionID := range attentionIDs {
			idArmed, err := s.armDelegateAttentionInController(attentionID)
			if err != nil {
				return err
			}
			armed = armed || idArmed
		}
		if armed {
			// One wake for the batch. The arms above ended the user-stop
			// holds before this fires, because the notify func drives the
			// receiver synchronously: a hold still in place at the drive's
			// list read would present the armed row alone and leave the
			// stopped report for a later generation.
			s.notify()
		}
		return nil
	}
	pending, err := s.pendingDelegateAttentionIDs()
	if err != nil {
		return err
	}
	surviving := make([]string, 0, len(attentionIDs))
	for _, attentionID := range attentionIDs {
		if slices.Contains(pending, attentionID) {
			surviving = append(surviving, attentionID)
		}
	}
	if len(surviving) == 0 {
		return nil
	}
	s.armRootDelegateAttentions(surviving)
	return nil
}

func (s *Session) armDelegateAttentionOnce(attentionID string) error {
	if s.isRootDelegateAttentionReceiver() {
		return s.armDelegateAttentionsOnce([]string{attentionID})
	}
	armed, err := s.armDelegateAttentionInController(attentionID)
	if err != nil {
		return err
	}
	if armed {
		// The core above ended the user-stop holds before this fires,
		// because the notify func drives the receiver synchronously: a hold
		// still in place at the drive's list read would present the armed row
		// alone and leave the stopped report for a later generation.
		s.notify()
	}
	return nil
}

// armDelegateAttentionInController performs one non-root arm attempt without
// waking: it takes the receiver's pending attention to the controller (opening
// the delegate's attention there and outwaiting a racing attention start) and
// reports whether attentionID was still pending, so the caller decides how the
// wake fires — once for a batch, or the single-ID arm's own notify.
func (s *Session) armDelegateAttentionInController(attentionID string) (armed bool, err error) {
	if s.delegateController == nil || s.owningDelegateID == "" {
		return false, errors.New("delegate attention controller identity is incomplete")
	}
	for {
		s.attentionMu.Lock()
		ids, err := s.pendingDelegateAttentionIDsLocked()
		if err != nil {
			s.attentionMu.Unlock()
			return false, err
		}
		if !slices.Contains(ids, attentionID) {
			s.attentionMu.Unlock()
			return false, nil
		}
		_, blocker, plan, emit, err := s.delegateController.tryOpenDelegateAttention(s.owningDelegateID, attentionID)
		s.attentionMu.Unlock()
		if err != nil {
			return false, err
		}
		if blocker != nil {
			<-blocker
			continue
		}
		if emit {
			s.delegateController.emitDelegateUpdate(plan)
		}
		return true, nil
	}
}

func (s *Session) hasPendingDelegateAttentionArmRetry() bool {
	if s == nil {
		return false
	}
	s.attentionMu.Lock()
	pending := len(s.delegateAttentionArmIDs) != 0
	s.attentionMu.Unlock()
	return pending
}

func (s *Session) pendingDelegateAttentionIDs() ([]string, error) {
	if s == nil {
		return nil, nil
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	return s.pendingDelegateAttentionIDsLocked()
}

// delegateAttentionReopenID is the fresh identity a failed presentation start
// re-delivers its consumed report under: keyed by the failed start's generation
// so a replay of the same failure is idempotent and a later failure keys a
// fresh row. The validator recognizes the format so a reopened row can take
// over the resume claim its source left on the generation that never ran.
func delegateAttentionReopenID(sourceID string, generation uint64) string {
	return sourceID + delegateAttentionReopenSuffix(generation)
}

// delegateAttentionReopenMarker prefixes the generation in every reopen
// segment: the one definition of the segment's constant part, shared by the
// builder and the id stripper so the two cannot drift apart.
const delegateAttentionReopenMarker = "/reopened/"

// delegateAttentionReopenSuffix is the wire format of one reopen segment,
// keyed by the failed start's generation.
func delegateAttentionReopenSuffix(generation uint64) string {
	return fmt.Sprintf("%s%d", delegateAttentionReopenMarker, generation)
}

// stripDelegateAttentionReopenSegments reduces id to the identity its reopen
// chain was minted from: reopen segments are only ever appended, so trimming
// every trailing marker+generation segment leaves the original row's id. An
// id that does not end in a well-formed segment is returned unchanged.
func stripDelegateAttentionReopenSegments(id string) string {
	for {
		source, _, ok := cutDelegateAttentionReopenSegment(id)
		if !ok {
			return id
		}
		id = source
	}
}

// cutDelegateAttentionReopenSegment trims one trailing reopen segment off id,
// reporting the source identity the segment was minted from and the failed
// start's generation it keyed. An id that does not end in a well-formed
// segment reports itself whole with ok false — the one definition of the
// segment's shape, shared by the id stripper, which loops cuts down to the
// base, and the cold restore's derived hold, which keys the immediate
// source's resume claim.
func cutDelegateAttentionReopenSegment(id string) (source string, generation uint64, ok bool) {
	base, segment, found := strings.CutLast(id, delegateAttentionReopenMarker)
	if !found {
		return id, 0, false
	}
	generation, err := strconv.ParseUint(segment, 10, 64)
	if err != nil {
		return id, 0, false
	}
	return base, generation, true
}

// pendingDelegateAttentionIDsLocked lists the attention rows still owed by this
// session. The root keeps its transcript rail; a child delegate reads the
// unheld rows of its durable queue, which is the single authority for what it
// owes. Callers hold attentionMu.
func (s *Session) pendingDelegateAttentionIDsLocked() ([]string, error) {
	if s == nil {
		return nil, nil
	}
	if s.owningDelegateID != "" && s.delegateController != nil {
		return s.dropAwaitingAttentionResolutionLocked(s.delegateController.unheldDelegateAttentionIDs(s.owningDelegateID)), nil
	}
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil, err
	}
	return fold.pendingIDs(), nil
}

// residentAttentionTurnIDsLocked lists the attention identities whose steering
// turn the resident history holds. Callers hold attentionMu, which pins the
// history against the attention paths that add and remove those turns.
func (s *Session) residentAttentionTurnIDsLocked() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	resident := make(map[string]struct{}, len(s.history))
	for _, turn := range s.history {
		if turn.AttentionID != "" && turn.Kind == schema.TurnSteering {
			resident[turn.AttentionID] = struct{}{}
		}
	}
	return resident
}

// automaticDelegateAttentionIDs lists the pending rows an automatic drive may
// present: every unheld row in the delegate's durable queue. The store is the
// only authority; a stop-held row stays queued there but never appears here.
// No selectable row may reach the drive with no turn behind it, so the fold's
// turn for each is restored first.
func (s *Session) automaticDelegateAttentionIDs() ([]string, error) {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return nil, nil
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	ids := s.dropAwaitingAttentionResolutionLocked(s.delegateController.unheldDelegateAttentionIDs(s.owningDelegateID))
	if len(ids) == 0 {
		return nil, nil
	}
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil, err
	}
	if err := s.retainPendingAttentionTurnsLocked(fold, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// retainPendingAttentionTurnsLocked restores the turn behind every selectable
// pending row that the resident history does not hold. A restore anchors on the
// last compaction marker, so a row recorded before that marker has no turn in
// history even though the store still lists it pending; the drive would claim
// the row and attach a request with no report. ids is the caller's selectable
// list, in arrival order, so held and covered rows stay out. A resident turn
// keeps the position it already has, but still anchors a restored row that
// arrived before it, or a later row would read as current. The caller holds
// attentionMu.
func (s *Session) retainPendingAttentionTurnsLocked(fold delegateAttentionFold, ids []string) error {
	resident := s.residentAttentionTurnIDsLocked()
	return s.restoreAttentionTurns(fold.turnsForArriving(ids), resident)
}

// restorePendingDelegateAttentionTurns puts back the turns of the delegate's
// unheld pending rows that the resident history lost to a compaction, in
// transcript arrival order. A committed send is a newer event than the stop
// that held those rows: its start clears every hold in the same store Apply, so
// the generation must present them even though the compaction kept only the
// rows unheld when it ran. Called before the send's first request is built.
func (s *Session) restorePendingDelegateAttentionTurns() error {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return nil
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	turns, err := s.pendingAttentionTurnsLocked()
	if err != nil || len(turns) == 0 {
		return err
	}
	return s.restoreAttentionTurns(turns, s.residentAttentionTurnIDsLocked())
}

// restoreAttentionTurns puts turns, in arrival order, back into the resumed
// history, each anchored by the turns that follow it, which a compaction
// marker after them would otherwise have cut. A turn whose attention identity
// appears in resident keeps the position it already has and is not restored,
// but it still anchors the restored turns that arrived before it. A nil
// resident restores every turn.
func (s *Session) restoreAttentionTurns(turns []schema.Turn, resident map[string]struct{}) error {
	for i, turn := range turns {
		if _, present := resident[turn.AttentionID]; present {
			continue
		}
		if err := s.retainDelegateAttentionTurn(turn, turns[i+1:]...); err != nil {
			return err
		}
	}
	return nil
}

// delegateAutomaticAttentionHistorySnapshotWithCoverage is the projection an
// automatic attention drive asks for: the same transcript, coverage and
// in-flight boundary delegateModelHistorySnapshotWithCoverage returns, plus the
// user-stopped rows' turns kept out of it. The resident history can still hold
// a held row's steering turn, so without the filter the drive would present the
// stopped report through it — the immediate restart the stop exists to prevent.
// The resident history keeps the turn: an explicit send presents it,
// and so does the fresh arm that releases the hold.
//
// The held-row set comes from delegatestore, the single authority: a row a user
// stop holds stays durably queued there and never counts as a runnable wake, so
// its turn must stay out of the automatic drive's history too.
func (s *Session) delegateAutomaticAttentionHistorySnapshotWithCoverage() (projection delegateModelProjection, droppedAt []int) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	var held map[string]struct{}
	if s.delegateController != nil && s.owningDelegateID != "" {
		held = s.delegateController.heldDelegateAttentionIDs(s.owningDelegateID)
	}
	projection = s.delegateModelHistorySnapshotWithCoverageLocked()
	turns := projection.turns
	if len(held) == 0 {
		return projection, nil
	}
	kept := make([]schema.Turn, 0, len(turns))
	for i, turn := range turns {
		if _, stillHeld := held[turn.AttentionID]; turn.AttentionID != "" && stillHeld {
			// The positions are in the captured history's coordinates: a drop
			// shifts every later turn one position left, so the caller's
			// in-flight boundary — an index into the projection — moves with
			// each drop strictly before it.
			droppedAt = append(droppedAt, i)
			continue
		}
		kept = append(kept, turn)
	}
	projection.turns = kept
	return projection, droppedAt
}

// delegateAttentionFoldLocked folds the session's own transcript; the zero fold
// stands in until a transcript is attached. The caller holds attentionMu.
func (s *Session) delegateAttentionFoldLocked() (delegateAttentionFold, error) {
	if s == nil {
		return delegateAttentionFold{}, nil
	}
	s.mu.Lock()
	ready := s.transcriptReady
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || sessionID == "" || stateDir == "" {
		return delegateAttentionFold{}, nil
	}
	return s.readDelegateAttentionFold(transcriptPath(stateDir, sessionID), sessionID)
}

// pendingAttentionTurnsLocked returns the turns behind the pending attention
// items and the accepted trigger no settled request has presented. The caller
// holds attentionMu.
func (s *Session) pendingAttentionTurnsLocked() ([]schema.Turn, error) {
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil, err
	}
	if s.owningDelegateID == "" || s.delegateController == nil {
		return fold.pendingTurns(s.attentionAcceptedID), nil
	}
	// A presented row whose resolution is still awaiting retry stays out: the
	// model already read it, and re-injecting it would replay a message the
	// next request must not present again.
	ids := s.dropAwaitingAttentionResolutionLocked(s.delegateController.unheldDelegateAttentionIDs(s.owningDelegateID))
	if s.attentionAcceptedID != "" {
		ids = appendUniqueStrings(ids, s.attentionAcceptedID)
	}
	return fold.turnsForArriving(ids), nil
}

// attentionTurns returns the turns of history that carry an attention identity.
func attentionTurns(history []schema.Turn) []schema.Turn {
	var turns []schema.Turn
	for _, turn := range history {
		if turn.AttentionID != "" {
			turns = append(turns, turn)
		}
	}
	return turns
}

// insertAttentionTurn puts turn before the first held turn of a later-arriving
// item (later, in arrival order), or at the end when none is held, and returns
// the position it took.
func insertAttentionTurn(history []schema.Turn, turn schema.Turn, later []schema.Turn) ([]schema.Turn, int) {
	at := slices.IndexFunc(history, func(held schema.Turn) bool {
		return slices.ContainsFunc(later, func(next schema.Turn) bool { return next.AttentionID == held.AttentionID })
	})
	if at < 0 {
		at = len(history)
	}
	return slices.Insert(history, at, turn), at
}

// holdAcceptedTrigger latches an accepted generation's trigger so a fold keeps
// its turn until a settled request presents it. The accept path and the
// restart of an owed generation both set it.
func (s *Session) holdAcceptedTrigger(attentionID string) {
	// The trigger stays durably queued while its generation runs, so the
	// controller's copy of the cause can be dropped before the request that
	// presents the trigger is built; the latch keeps the cause for that
	// request. A re-hold of the same trigger (a commit whose journal append
	// failed and retried) finds the controller's copy already dropped, so it
	// keeps the cause the first hold latched instead of overwriting it with
	// none.
	var cause *provenance.Causal
	if s.delegateController != nil {
		cause = s.delegateController.attentionCauses.union([]string{attentionID})
	}
	s.attentionMu.Lock()
	if cause == nil && s.attentionAcceptedID == attentionID {
		cause = s.attentionAcceptedCause
	}
	s.attentionAcceptedID = attentionID
	s.attentionAcceptedCause = cause
	s.attentionMu.Unlock()
}

// restoreAcceptedTrigger latches the trigger of a restarted owed generation
// and puts its turn and the pending items' turns (turns, in arrival order)
// back into the resumed history, which a compaction marker after them would
// have cut.
func (s *Session) restoreAcceptedTrigger(attentionID string, turns []schema.Turn) error {
	s.holdAcceptedTrigger(attentionID)
	return s.restoreAttentionTurns(turns, nil)
}

// clearAcceptedDelegateTrigger drops the accepted trigger a generation latched
// but never ran. The row stays in delegatestore, so the next drive selects it
// again; only the in-memory latch is spent.
func (s *Session) clearAcceptedDelegateTrigger() {
	if s == nil {
		return
	}
	s.attentionMu.Lock()
	s.attentionAcceptedID = ""
	s.attentionAcceptedCause = nil
	s.attentionMu.Unlock()
}

// isRootDelegateAttentionReceiver reports whether this Session is the stable
// controller's root receiver. Stable delegate runtimes use controller-governed
// attention generations; only the root consumes attention through its existing
// EntryNotification loop.
func (s *Session) isRootDelegateAttentionReceiver() bool {
	if s == nil || s.delegateController == nil {
		return false
	}
	s.delegateController.mu.Lock()
	isRoot := s.delegateController.rootRuntime == s
	s.delegateController.mu.Unlock()
	return isRoot
}

// armRootDelegateAttentions caches already-durable root attention IDs and
// coalesces one autonomous notification wake for the batch. The cache is never
// authority: an append/readback succeeds before admission here, and restart
// replaces it from the receiver transcript fold.
func (s *Session) armRootDelegateAttentions(attentionIDs []string) {
	if !s.isRootDelegateAttentionReceiver() || len(attentionIDs) == 0 {
		return
	}
	ids := attentionIDs
	if len(ids) == 1 {
		if ids[0] == "" {
			return
		}
	} else {
		ids = appendUniqueStrings(nil, ids...)
	}
	if len(ids) == 0 {
		return
	}
	// The parked rail derives from the durable holds
	// (rootAttentionRailParked); read it before attentionMu so the store's
	// state mutex never nests inside it. A fresh arm on a parked rail caches
	// silently — the IDs wait for the re-engagement that clears the holds.
	parked := s.rootAttentionRailParked()
	s.attentionMu.Lock()
	if s.rootAttentionWakeIDs == nil {
		s.rootAttentionWakeIDs = make(map[string]struct{})
	}
	for _, attentionID := range ids {
		s.rootAttentionWakeIDs[attentionID] = struct{}{}
	}
	shouldWake := !s.rootAttentionWake && !parked
	if shouldWake {
		s.rootAttentionWake = true
	}
	s.attentionMu.Unlock()
	if shouldWake {
		s.notify()
	}
}

func (s *Session) hasPendingRootDelegateAttention() bool {
	if s == nil {
		return false
	}
	s.attentionMu.Lock()
	pending := len(s.rootAttentionWakeIDs) != 0
	s.attentionMu.Unlock()
	return pending
}

// pendingRootDelegateAttention is hasPendingRootDelegateAttention for "is
// this session working" reads: attention a Stop parked, or a permanent
// provider failure paused with no fresh wake or paced retry armed, is
// deferred to the user and reads false, the way pendingQueueDepth reads zero
// for a held queue (kata wms7). A fresh delegate event re-arms the wake, and
// a transient failure during that attempt arms the paced retry; either one
// counts as work again. The raw signal stays for the rail's own machinery,
// which must see the cached IDs to re-arm them on release.
func (s *Session) pendingRootDelegateAttention() bool {
	if s == nil {
		return false
	}
	s.attentionMu.Lock()
	deferred := s.rootAttentionPaused && !s.rootAttentionWake && !s.rootAttentionRetry.active
	pending := len(s.rootAttentionWakeIDs) != 0 && !deferred
	s.attentionMu.Unlock()
	return pending && !s.rootAttentionRailParked()
}

// beginRootDelegateAttentionTurn snapshots the exact durable IDs selected for
// this notification turn and consumes only the process-local wake. The IDs stay
// cached until a consumed-resolution fsync succeeds.
func (s *Session) beginRootDelegateAttentionTurn() []string {
	s.attentionMu.Lock()
	ids := make([]string, 0, len(s.rootAttentionWakeIDs))
	for id := range s.rootAttentionWakeIDs {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		s.attentionMu.Unlock()
		return nil
	}
	s.rootAttentionWake = false
	s.rootAttentionRetry.disarmIfArmed()
	s.attentionMu.Unlock()
	sort.Strings(ids)
	return ids
}

// finishAttentionTurn consumes, at the end of a turn, the attention the turn
// owes. The root runs its notification rail (finishRootDelegateAttentionTurn).
// Every child transition is ONE atomic delegatestore Apply, so
// NeedsAttention and the queue can never diverge:
//
//   - A successful turn forgets every row its settled requests presented,
//     including the accepted trigger the presentation moved to
//     attentionPresentedTrigger.
//   - A presented-then-aborted generation (mid-round stop, hook stop,
//     tool-round exhaustion, provider failure) forgets what its settled
//     requests presented: the model read the report, so a successor must not
//     replay it.
//   - An unpresented aborted generation keeps its accepted row pending. A user
//     or hook stop is settled by the finalize tail (settleUnpresentedDelegateTrigger)
//     with a durable hold; a plain transient failure leaves the row unheld for
//     the next drive to present.
func (s *Session) finishAttentionTurn(ids []string, turnErr error) error {
	if s.isRootDelegateAttentionReceiver() {
		return s.finishRootDelegateAttentionTurn(ids, turnErr)
	}
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return nil
	}
	var resolvedIDs []string
	defer s.settleFoldedCoveredAttention(&resolvedIDs)
	s.attentionMu.Lock()
	presented := s.attentionPresentedTrigger
	s.attentionPresentedTrigger = ""
	if turnErr == nil {
		// The trigger's turn and its latch are spent by a successful turn even
		// when a delta request settled it.
		if s.attentionAcceptedID == presented || presented == "" {
			s.attentionAcceptedID = ""
			s.attentionAcceptedCause = nil
		}
	}
	s.attentionMu.Unlock()
	covered := s.unionCoveredAttention(ids)
	if presented != "" {
		covered = appendUniqueStrings(covered, presented)
	}
	if len(covered) == 0 {
		return nil
	}
	// The queue is the durable authority for this turn, and the row must leave
	// while the generation still runs or the root keeps counting it as owed
	// work. A failed append is a warning, not a turn failure: the row stays
	// pending, and the ids join attentionAwaitingResolution so no later drive
	// presents attention the model already read. A paced retry
	// (flushAwaitingDelegateAttentionResolutions) resolves them once the
	// journal recovers.
	if err := s.delegateController.forgetDelegateAttention(s.owningDelegateID, covered...); err != nil {
		s.noteAttentionAwaitingResolution(covered)
		s.warnDelegateAttentionFailed(delegateAttentionResolveLabel, s.owningDelegateID, err)
		s.delegateController.retryDelegateAttentionLater()
		return nil
	}
	s.clearAttentionAwaitingResolution(covered)
	s.delegateAttentionWarningResolved(delegateAttentionResolveLabel, s.owningDelegateID)
	resolvedIDs = covered
	return nil
}

// noteAttentionAwaitingResolution records the presented rows whose journal
// resolution append failed, so no drive presents them again before the retry
// resolves them.
func (s *Session) noteAttentionAwaitingResolution(ids []string) {
	if s == nil || len(ids) == 0 {
		return
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if s.attentionAwaitingResolution == nil {
		s.attentionAwaitingResolution = make(map[string]struct{}, len(ids))
	}
	for _, id := range ids {
		s.attentionAwaitingResolution[id] = struct{}{}
	}
}

// clearAttentionAwaitingResolution ends the retry episode for ids whose
// resolution append finally landed.
func (s *Session) clearAttentionAwaitingResolution(ids []string) {
	if s == nil || len(ids) == 0 {
		return
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	for _, id := range ids {
		delete(s.attentionAwaitingResolution, id)
	}
}

// dropAwaitingAttentionResolutionLocked removes the presented-but-unresolved
// ids from a selectable list. Callers hold attentionMu; unheld rows that were
// never presented stay in the list, so a plain retry still presents them.
func (s *Session) dropAwaitingAttentionResolutionLocked(ids []string) []string {
	if len(s.attentionAwaitingResolution) == 0 {
		return ids
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, awaiting := s.attentionAwaitingResolution[id]; !awaiting {
			kept = append(kept, id)
		}
	}
	return kept
}

// flushAwaitingDelegateAttentionResolutions retries the journal resolution of
// every row a settled request presented whose first append failed. The model
// already read those rows, so the drive and the drain both withhold them from
// triggering another request until this retry lands and removes them from
// delegatestore. Ids leave the awaiting set only on a successful append, so a
// failing journal keeps them withheld instead of replaying them.
func (s *Session) flushAwaitingDelegateAttentionResolutions() error {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return nil
	}
	s.attentionMu.Lock()
	if len(s.attentionAwaitingResolution) == 0 {
		s.attentionMu.Unlock()
		return nil
	}
	awaiting := make([]string, 0, len(s.attentionAwaitingResolution))
	for id := range s.attentionAwaitingResolution {
		awaiting = append(awaiting, id)
	}
	s.attentionMu.Unlock()
	sort.Strings(awaiting)
	if err := s.delegateController.forgetDelegateAttention(s.owningDelegateID, awaiting...); err != nil {
		return err
	}
	s.clearAttentionAwaitingResolution(awaiting)
	s.delegateAttentionWarningResolved(delegateAttentionResolveLabel, s.owningDelegateID)
	return nil
}

// settleUnpresentedDelegateTrigger settles the accepted trigger a child
// generation latched but never presented. It runs once, from the finalize tail
// after the turn's own exit rule declined to act. A user or hook stop (quietAt
// names its settled generation) carries its durable hold in the generation's
// finish batch (holdAttentionID names the row), so the stopped delegate retires
// cleanly and the row presents only when a fresh wake or an explicit send
// clears the hold. A plain transient failure leaves the row unheld and pending
// for the next drive. If the finish batch itself failed, the latch stays: the
// ordinary drive must not accept the stopped row again before settlement
// recovery retries that batch, which carries the hold with the outcome.
// Callers that settled the row outside a finish batch pass an empty
// holdAttentionID, and the hold is stamped here directly.
func (s *Session) settleUnpresentedDelegateTrigger(quietAt uint64, holdAttentionID string, finishBatchLanded bool) {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return
	}
	s.attentionMu.Lock()
	trigger := s.attentionAcceptedID
	if trigger != "" && holdAttentionID != "" && !finishBatchLanded {
		s.attentionMu.Unlock()
		return
	}
	s.attentionAcceptedID = ""
	s.attentionAcceptedCause = nil
	s.attentionMu.Unlock()
	if trigger == "" || quietAt == 0 || holdAttentionID != "" {
		return
	}
	if err := s.delegateController.holdDelegateAttention(s.owningDelegateID, trigger, quietAt); err != nil {
		s.warnDelegateAttentionFailed(delegateAttentionResolveLabel, s.owningDelegateID, err)
	}
}

// acceptedDelegateAttentionTriggerID returns the accepted trigger the current
// generation has not presented yet, or "" when there is none. The finalize tail
// hands it to the finish batch so a stop's hold is durable with the outcome.
func (s *Session) acceptedDelegateAttentionTriggerID() string {
	if s == nil {
		return ""
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	return s.attentionAcceptedID
}

// finishRootDelegateAttentionTurn consumes the exact selected IDs only after a
// successful model turn and durable resolution markers — plus any still-pending
// attention this turn's built requests already presented to the model, which
// needs no wake of its own. Failures keep the transcript-owned IDs pending and
// arrange a paced retry wake.
func (s *Session) finishRootDelegateAttentionTurn(ids []string, turnErr error) error {
	var resolvedIDs []string
	defer s.settleFoldedCoveredAttention(&resolvedIDs)
	// A failed turn never resolves, so on that path the union serves only as
	// the emptiness gate that arms the paced-retry backstop — and a non-empty
	// snapshot already passes it. Skip the fold read there; keep it for an
	// empty snapshot, where a covered-but-pending item needs the backstop.
	// The union reads the fold only when this turn covered something.
	//
	// When it finds nothing, the early return below leaves a set wake flag
	// untouched on purpose. The flag coalesces; the liveness carrier is the
	// mid-turn arm's own guaranteed kick (armRootDelegateAttention → notify →
	// the serve loop's parked EntryNotification), not the retry scheduler the
	// flag suppresses.
	if turnErr == nil || len(ids) == 0 {
		ids = s.unionCoveredAttention(ids)
	}
	if len(ids) == 0 {
		return nil
	}
	var resolutionErr error
	if turnErr == nil {
		err := s.resolveAttentionDurably(ids, delegateAttentionConsumed)
		if err == nil {
			resolvedIDs = ids
			s.attentionMu.Lock()
			s.rootAttentionPaused = false
			for _, id := range ids {
				delete(s.rootAttentionWakeIDs, id)
			}
			if len(s.rootAttentionWakeIDs) == 0 {
				s.rootAttentionWake = false
				s.resetRootAttentionRetryLocked()
			}
			s.attentionMu.Unlock()
			return nil
		}
		resolutionErr = err
	}
	// A PERMANENT provider failure — a 401 from a dead credential, a
	// request the provider will refuse every time — cannot be retried into
	// success, and the paced retry never gives up on its own: re-arming it
	// against one is the livelock, every firing another doomed turn on the
	// wire. The IDs stay pending and cached; they ride the next genuine
	// wake — a user message, a fresh delegate event, re-engagement after a
	// Stop.
	//
	// A cancelled or user-aborted turn keeps the retry (roundWasCancelled,
	// the package's "the context went away, not the provider" predicate).
	// The rail's wake is not a re-request of the same call, it is the
	// delivery-liveness chain the retirement machinery pins: a cancelled
	// attention turn still owes its delivery. A user's Stop needs no help
	// from this branch either — the park the accepted Stop takes cancels
	// whatever the unwinding turn armed. llm.Classify answers the
	// transport's question (must the same request be re-issued?); the
	// carve-out is the rail's own.
	//
	// Transient failures keep the retry, which is their delivery
	// guarantee, and a resolution failure with no turn error is a storage
	// flap, retryable by nature.
	permanent := turnErr != nil && !roundWasCancelled(turnErr) && llm.Classify(turnErr) == llm.ErrorClassPermanent
	s.attentionMu.Lock()
	// Clear the flag even while a wake is pending. This turn may have honored
	// that wake and then declined — its resolution failed — and the drain
	// skips the notification rung right after a notification turn, so the
	// flagged wake alone can strand the item. The retry owns the next wake.
	s.rootAttentionWake = false
	announce := false
	if permanent {
		s.resetRootAttentionRetryLocked()
		announce = !s.rootAttentionPaused
		s.rootAttentionPaused = true
	} else {
		s.scheduleRootAttentionRetryLocked()
	}
	s.attentionMu.Unlock()
	if announce {
		s.announceRootAttentionPaused(turnErr)
	}
	return resolutionErr
}

// announceRootAttentionPaused says, once per deferral episode, that a
// permanent provider failure has paused background updates, which provider
// and how to resume. Without it the updates just stop arriving. It is
// visible at every level (its code is not informational) and goes to the
// daemon log too. Nothing a provider said reaches the warning: a provider's
// error body can carry anything, so the hint and the log line are the
// failure's kind and status only; the serve loop's own turn-failure line
// already records the failed turn.
func (s *Session) announceRootAttentionPaused(turnErr error) {
	summary := llm.ProviderFailureSummary(turnErr)
	slog.Warn("background updates paused", "session", s.ID(), "failure", summary)
	s.emit(events.WarningData{
		Message: rootAttentionPausedMessage(turnErr),
		Source:  string(diagnostic.SourceEvener),
		Title:   "Background updates paused",
		Hint:    summary,
		Code:    events.WarningCodeAttentionPaused,
	})
}

// rootAttentionPausedMessage names the provider instance that refused (the
// name in Evener's own configuration) and what would resume delivery, by
// the failure's kind as llm classifies it.
func rootAttentionPausedMessage(turnErr error) string {
	instance := "the provider"
	var providerErr interface{ Provider() string }
	if errors.As(turnErr, &providerErr) {
		if name := strings.TrimSpace(providerErr.Provider()); name != "" {
			instance = name
		}
	}
	const paused = "Background updates from subagents and jobs are paused: "
	kind := llm.Kind(turnErr)
	switch {
	case errors.Is(turnErr, llm.ErrSignInRequired) || kind == llm.KindAuthentication:
		return paused + instance + " rejected the credential. Sign in to " + instance + ", then send a message to continue."
	case kind == llm.KindAccessDenied:
		return paused + instance + " refused access. Check your access to " + instance + ", then send a message to continue."
	case kind == llm.KindQuotaExceeded:
		// UTC: the daemon may run in another zone than the person reading.
		if resetAt, ok := llm.UsageLimitResetAt(turnErr); ok {
			return paused + instance + "'s usage limit is reached until " + resetAt.UTC().Format("Jan 2 15:04 UTC") + ". Send a message after that, or switch models, to continue."
		}
		return paused + instance + "'s usage limit is reached. Switch models, or send a message once it resets, to continue."
	default:
		return paused + instance + " refused the request. Switch models or send a message to continue."
	}
}

// resumeRootAttentionAfterModelSwitch re-arms attention a permanent failure
// deferred: a new model is a change that can make it deliverable. A Stop's
// park still wins; its re-engagement re-arms instead.
//
// Only a permanent failure's deferral is resumed: attention waiting out a
// transient failure keeps its paced backoff. The parked read comes before
// attentionMu, the rail's lock order; a Stop that lands after it still wins,
// since its park stands a straggler wake down before any model turn.
func (s *Session) resumeRootAttentionAfterModelSwitch() {
	if s.rootAttentionRailParked() {
		return
	}
	s.attentionMu.Lock()
	paused := s.rootAttentionPaused
	s.rootAttentionPaused = false
	s.attentionMu.Unlock()
	if paused {
		s.unparkRootDelegateAttention()
	}
}

// coversPresentedAttention reports whether this turn consumes the attention
// its settled requests present. The root receiver's turns do. So does a
// stable delegate's turn running under its generation's lease: the lease is
// the delegate's only start claim, since a delegate holds one reservation at
// a time and no attention reservation is admitted while its generation holds
// a live binding (delegateAttentionWakeEligibleLocked). The generation-less
// consumed marker written at the turn's end therefore cannot conflict with a
// reserved attention generation. An unleased turn on a child session covers
// nothing.
func (s *Session) coversPresentedAttention(ctx context.Context) bool {
	if s.isRootDelegateAttentionReceiver() {
		return true
	}
	_, leased := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease)
	return leased
}

// stagePresentedAttentionCoverage records one built request's candidate
// coverage: the attention IDs among presentedTurns, the exact history the
// request projects. Arming time does not matter: the model has seen the
// message that would have woken it. Every request build replaces the staged
// set, so a failed attempt's candidates never outlive it. Staging is
// candidacy, not credit — promoteStagedAttentionCoverage credits the set only when the
// round's call settles, because a request that never settled (a failed
// attempt, a content-filter retry whose compaction then folds the steering
// turn away) presented nothing the design may consume. Only a turn that
// coversPresentedAttention stages.
//
// deltaTurns is the exact turn set the planned request carries when it is a
// responses-continuation delta, nil for a full-history request. An inline
// delivery staged from a turn the delta does not carry was not presented by
// this call: a later round's delta settles, and crediting that delivery would
// adopt the cause of something the model never saw while retiring it for good.
func (s *Session) stagePresentedAttentionCoverage(ctx context.Context, presentedTurns []schema.Turn, deltaTurns []schema.Turn) {
	if !s.coversPresentedAttention(ctx) {
		return
	}
	deltaInline := deltaInlineDeliveryIDs(deltaTurns)
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	var staged map[string]struct{}
	var inline map[string]struct{}
	var inlineInDelta map[string]struct{}
	for _, turn := range presentedTurns {
		if turn.AttentionID != "" {
			if staged == nil {
				staged = make(map[string]struct{})
			}
			staged[turn.AttentionID] = struct{}{}
		}
		// A delegate result a reply carried inline spends its delivery here
		// rather than in an attention turn: its commit on this turn is the
		// only record that the round presented it.
		for _, commit := range turn.DelegateDeliveryCommits {
			if commit.DeliveryID == "" {
				continue
			}
			if inline == nil {
				inline = make(map[string]struct{})
			}
			attentionID := delegateAttentionID(commit.DeliveryID)
			inline[attentionID] = struct{}{}
			if _, carried := deltaInline[commit.DeliveryID]; carried {
				if inlineInDelta == nil {
					inlineInDelta = make(map[string]struct{})
				}
				inlineInDelta[attentionID] = struct{}{}
			}
		}
	}
	s.attentionStagedIDs = staged
	s.attentionStagedInlineDeliveries = inline
	s.attentionStagedDeltaInlineDeliveries = inlineInDelta
}

// deltaInlineDeliveryIDs returns the delivery ids of the tool-results turns in
// deltaTurns, the turns a responses-continuation delta carries. nil deltaTurns
// reports no delta at all, so nothing is scoped in.
func deltaInlineDeliveryIDs(deltaTurns []schema.Turn) map[string]struct{} {
	if len(deltaTurns) == 0 {
		return nil
	}
	var ids map[string]struct{}
	for _, turn := range deltaTurns {
		for _, commit := range turn.DelegateDeliveryCommits {
			if commit.DeliveryID == "" {
				continue
			}
			if ids == nil {
				ids = make(map[string]struct{})
			}
			ids[commit.DeliveryID] = struct{}{}
		}
	}
	return ids
}

// promoteStagedAttentionCoverage credits the staged set of a round whose
// call settled as settled, the request that actually answered. Called by the
// round loop on the success path only.
//
// A responses-continuation delta never carries an attention delivery: every
// steering one is a steering turn, and the eligibility rules reject any delta
// whose suffix holds a steering turn. A round that settled as a delta therefore
// credits no steering coverage and clears no latch, so a latched trigger's
// reopen stays owed. Inline deliveries are the exception: a tool-results turn
// is delta-eligible, so a reply that spent a held report inline can ride a
// delta and must still adopt that report's cause and retire it. A delta whose
// call fell back to a full-history request settled as that request, which
// carried every staged turn.
func (s *Session) promoteStagedAttentionCoverage(settled llm.Request) {
	s.attentionMu.Lock()
	staged := s.attentionStagedIDs
	inline := s.attentionStagedInlineDeliveries
	inlineInDelta := s.attentionStagedDeltaInlineDeliveries
	s.attentionStagedIDs = nil
	s.attentionStagedInlineDeliveries = nil
	s.attentionStagedDeltaInlineDeliveries = nil
	if settled.HistoryMode == llm.HistoryModeResponsesDelta {
		// What a delta presents that a steering turn cannot carry: the inline
		// deliveries whose tool-results turn rode it. Only the ones the delta
		// actually carried count: a delivery staged from a turn before the
		// continuation anchor was not presented by this call, so adopting its
		// cause would credit something the model never saw and forget the cause
		// for good.
		s.attentionMu.Unlock()
		if len(inlineInDelta) != 0 {
			s.adoptInlineAttentionCauses(inlineInDelta)
		}
		return
	}
	if len(staged) == 0 && len(inline) == 0 {
		s.attentionMu.Unlock()
		return
	}
	var acceptedCause *provenance.Causal
	if _, presented := staged[s.attentionAcceptedID]; presented && s.attentionAcceptedID != "" {
		// A settled full-history request carried the accepted trigger to the
		// model. The row stays durably queued until the turn's exit rule
		// consumes it (finishAttentionTurn, or the finalize tail on an abort);
		// the latch only records that it was presented. It leaves the covered
		// set so the exit rule names it exactly once.
		s.attentionPresentedTrigger = s.attentionAcceptedID
		delete(staged, s.attentionAcceptedID)
		s.attentionAcceptedID = ""
		acceptedCause, s.attentionAcceptedCause = s.attentionAcceptedCause, nil
	}
	covered := s.attentionCoveredIDs
	if covered == nil {
		covered = make(map[string]struct{}, len(staged))
	}
	merged := s.attentionCausesMergedIDs
	if merged == nil {
		merged = make(map[string]struct{}, len(staged))
	}
	// Each item's cause joins the turn at the first settled request that
	// presents it; a later request of the same turn re-presenting the item
	// merges nothing.
	var fresh []string
	for id := range staged {
		if _, done := merged[id]; done {
			continue
		}
		merged[id] = struct{}{}
		fresh = append(fresh, id)
	}
	// An inline result is consumed by the turn that presented it, so its cause
	// joins the turn and then leaves the registry: nothing else will resolve it,
	// and a cause no attention can still arrive for would outlive its item.
	inlineFresh, spent := joinInlineDeliveries(merged, inline)
	fresh = append(fresh, inlineFresh...)
	maps.Copy(covered, staged)
	s.attentionCoveredIDs = covered
	s.attentionCausesMergedIDs = merged
	s.attentionMu.Unlock()
	// The model has now seen these items, so what the turn does next descends
	// from what caused them. This is where every attention item's cause joins
	// a turn once: a trigger the accept already resolved, an item presented
	// at turn start, and one that arrived mid-turn or inline alike.
	s.unionActiveProvenance(acceptedCause)
	if s.delegateController != nil && len(fresh) > 0 {
		s.unionActiveProvenance(s.delegateController.attentionCauses.union(fresh))
		s.delegateController.attentionCauses.forget(spent...)
	}
}

// joinInlineDeliveries marks each inline delivery in merged as joining this
// turn, skipping any an earlier settled request of the turn already merged, and
// returns the ones that joined plus the same ids as spent: an inline result is
// consumed by the turn that presented it, so nothing else will resolve it. The
// caller holds attentionMu.
func joinInlineDeliveries(merged, inline map[string]struct{}) (fresh, spent []string) {
	for id := range inline {
		if _, done := merged[id]; done {
			continue
		}
		merged[id] = struct{}{}
		fresh = append(fresh, id)
		spent = append(spent, id)
	}
	return fresh, spent
}

// adoptInlineAttentionCauses joins the causes of inline deliveries a settled
// delta presented, and forgets each: the reply that carried the result spent
// it, so no attention turn will ever resolve it. Steering items are untouched —
// the delta did not carry them, so their coverage stays owed. The caller holds
// no lock.
func (s *Session) adoptInlineAttentionCauses(inline map[string]struct{}) {
	s.attentionMu.Lock()
	merged := s.attentionCausesMergedIDs
	if merged == nil {
		merged = make(map[string]struct{}, len(inline))
	}
	fresh, spent := joinInlineDeliveries(merged, inline)
	s.attentionCausesMergedIDs = merged
	s.attentionMu.Unlock()
	if s.delegateController == nil || len(fresh) == 0 {
		return
	}
	s.unionActiveProvenance(s.delegateController.attentionCauses.union(fresh))
	s.delegateController.attentionCauses.forget(spent...)
}

type foldedCoveredAttentionTurn struct {
	turn    schema.Turn
	earlier []string
	later   []schema.Turn
}

func (s *Session) beginAttentionCoverageRounds() {
	s.attentionMu.Lock()
	s.attentionCoverageActive = true
	s.attentionMu.Unlock()
}

func (s *Session) endAttentionCoverageRounds() {
	s.attentionMu.Lock()
	s.attentionCoverageActive = false
	s.attentionMu.Unlock()
}

// resetAttentionCoverage clears the per-turn coverage at turn start. It runs
// for every turn, so a turn that covers nothing never inherits an earlier
// turn's coverage.
func (s *Session) resetAttentionCoverage() {
	s.attentionMu.Lock()
	s.attentionCoveredIDs = nil
	s.attentionStagedIDs = nil
	s.attentionStagedInlineDeliveries = nil
	s.attentionStagedDeltaInlineDeliveries = nil
	s.attentionCausesMergedIDs = nil
	s.attentionFoldedCovered = nil
	s.attentionMu.Unlock()
}

// restoreUncoveredAttentionLocked restores to published the attention turns it
// does not already hold, skipping any turn already presented in a settled round
// of the active turn (attentionCoveredIDs while attentionCoverageActive). Full
// arrival-ordered attention[i+1:] anchors every insertion so an older uncovered
// turn still lands ahead of a newer covered turn that survived in published.
// Suppressed covered turns absent from published are recorded in
// attentionFoldedCovered for restoration if they remain unconsumed when the
// turn exits. Caller holds attentionMu and s.mu.
func (s *Session) restoreUncoveredAttentionLocked(published, attention []schema.Turn) (_ []schema.Turn, restoredAt []int) {
	for i, turn := range attention {
		alreadyHeld := slices.ContainsFunc(published, func(held schema.Turn) bool {
			return held.AttentionID == turn.AttentionID
		})
		if s.attentionCoverageActive {
			if _, covered := s.attentionCoveredIDs[turn.AttentionID]; covered {
				if !alreadyHeld && !slices.ContainsFunc(s.attentionFoldedCovered, func(existing foldedCoveredAttentionTurn) bool {
					return existing.turn.AttentionID == turn.AttentionID
				}) {
					earlier := make([]string, 0, i)
					for _, prev := range attention[:i] {
						earlier = append(earlier, prev.AttentionID)
					}
					s.attentionFoldedCovered = append(s.attentionFoldedCovered, foldedCoveredAttentionTurn{
						turn:    turn,
						earlier: earlier,
						later:   slices.Clone(attention[i+1:]),
					})
				}
				continue
			}
		}
		if alreadyHeld {
			continue
		}
		var at int
		published, at = insertAttentionTurn(published, turn, attention[i+1:])
		restoredAt = append(restoredAt, at)
	}
	return published, restoredAt
}

// settleFoldedCoveredAttention clears mid-turn folded covered attention turns
// once all of them were durably resolved, or restores any still-pending ones
// back into resident history in arrival order when the turn exited without
// resolving them durably (including when unionCoveredAttention degraded on a
// fold-read error). Fast-paths without a transcript read when no mid-turn fold
// suppressed an unresolved turn.
func (s *Session) settleFoldedCoveredAttention(resolvedIDs *[]string) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if len(s.attentionFoldedCovered) == 0 {
		return
	}
	folded := s.attentionFoldedCovered
	s.attentionFoldedCovered = nil
	resolved := make(map[string]struct{}, len(*resolvedIDs))
	for _, id := range *resolvedIDs {
		resolved[id] = struct{}{}
	}
	unresolved := make(map[string]foldedCoveredAttentionTurn)
	for _, item := range folded {
		if _, ok := resolved[item.turn.AttentionID]; !ok {
			unresolved[item.turn.AttentionID] = item
		}
	}
	if len(unresolved) == 0 {
		return
	}
	pendingTurns, err := s.pendingAttentionTurnsLocked()
	if err != nil {
		s.mu.Lock()
		resident := attentionTurns(s.history)
		s.mu.Unlock()
		earlierIDs := make(map[string]struct{})
		for i, item := range folded {
			for _, id := range item.earlier {
				earlierIDs[id] = struct{}{}
			}
			if _, ok := unresolved[item.turn.AttentionID]; !ok {
				earlierIDs[item.turn.AttentionID] = struct{}{}
				continue
			}
			later := slices.Clone(item.later)
			for _, next := range folded[i+1:] {
				later = append(later, next.turn)
			}
			for _, resTurn := range resident {
				if resTurn.AttentionID == item.turn.AttentionID {
					continue
				}
				if _, wasEarlier := earlierIDs[resTurn.AttentionID]; !wasEarlier {
					later = append(later, resTurn)
				}
			}
			_ = s.retainDelegateAttentionTurn(item.turn, later...)
			earlierIDs[item.turn.AttentionID] = struct{}{}
		}
		return
	}
	for i, durable := range pendingTurns {
		if _, needRestore := unresolved[durable.AttentionID]; !needRestore {
			continue
		}
		_ = s.retainDelegateAttentionTurn(durable, pendingTurns[i+1:]...)
	}
}

// unionCoveredAttention adds to the selected IDs the covered
// deliveries still pending in the durable fold. A successful turn settles only
// when every round's call settled, so each covered item reached the model in
// a settled call of this very turn; an item appended after the final request
// was built stays uncovered and keeps the wake it armed.
//
// The pending filter is load-bearing. A presented steering turn whose item is
// already resolved under another disposition (a stop-drain discard, a
// conflicting cold resolution) would otherwise poison the batch: validation
// rejects the whole resolve atomically, and the wake would retry the item
// forever. A fold read failure degrades to the selected IDs alone; the wake
// path retries.
func (s *Session) unionCoveredAttention(ids []string) []string {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if len(s.attentionCoveredIDs) == 0 {
		return ids
	}
	pending, err := s.pendingDelegateAttentionIDsLocked()
	if err != nil {
		return ids
	}
	out := slices.Clone(ids)
	for _, id := range pending {
		if _, ok := s.attentionCoveredIDs[id]; ok {
			out = appendUniqueStrings(out, id)
		}
	}
	return out
}

// beginAttentionCallback retains local ownership even when the process
// controller is attached after this callback starts. Source receipt consumption
// and retry-generation resets do not settle an outstanding unlocked callback.
func (s *Session) beginAttentionCallback() (func(), error) {
	release, err := s.beginRetirementMutation("notification")
	if err != nil {
		return nil, err
	}
	s.attentionMu.Lock()
	s.attentionCallbacks++
	s.attentionMu.Unlock()
	return func() {
		s.attentionMu.Lock()
		s.attentionCallbacks--
		s.attentionMu.Unlock()
		release()
	}, nil
}

func (s *Session) scheduleRootAttentionRetryLocked() {
	if s.rootAttentionRetry.active || s.rootAttentionWake || len(s.rootAttentionWakeIDs) == 0 {
		return
	}
	delay, generation := s.rootAttentionRetry.arm()
	s.sclock().AfterFunc(delay, func() {
		// The rail parks with the durable holds, so read them before any
		// lock: a firing under a standing hold disarms without a wake, and
		// the cached IDs wait for the re-engagement that clears the holds.
		parkedAtFire := s.rootAttentionRailParked()
		release, err := s.beginAttentionCallback()
		if err != nil {
			// Admission was refused for this one-shot firing (a real TryClaim
			// preparing window). Do not consume the only firing: clear the
			// armed flag and synchronously re-arm under the owner lock so the
			// backoff chain survives the refusal and a later firing still
			// wakes the retained source. Only a still-current generation owns
			// the armed flag: a superseded one was invalidated by
			// resetRootAttentionRetryLocked or a newer schedule, and re-arming
			// it would resurrect a stale wake the owner already settled.
			s.attentionMu.Lock()
			if !s.rootAttentionRetry.superseded(generation) {
				s.rootAttentionRetry.active = false
				s.scheduleRootAttentionRetryLocked()
			}
			s.attentionMu.Unlock()
			return
		}
		defer release()
		s.attentionMu.Lock()
		if !s.rootAttentionRetry.fire(generation) {
			s.attentionMu.Unlock()
			return
		}
		if parkedAtFire {
			s.attentionMu.Unlock()
			return
		}
		pending := len(s.rootAttentionWakeIDs) != 0
		shouldWake := pending && !s.rootAttentionWake
		if shouldWake {
			s.rootAttentionWake = true
		}
		s.rootAttentionRetry.settle(pending, delay, jobNotificationRetryMaxDelay)
		s.attentionMu.Unlock()
		// The seam fires for a callback that passed every early return above --
		// live generation, rail not parked -- whether or not it then notifies. It
		// is the ownership lease, not the notify, that projects the notification
		// blocker, so a stale or parked timer must never reach it and a test's
		// pause can only belong to the live retry.
		if observe := s.cfg.testOnly.rootAttentionRetryCallback; observe != nil {
			observe()
		}
		if shouldWake {
			s.notify()
		}
	})
}

func (s *Session) resetRootAttentionRetryLocked() {
	s.rootAttentionRetry.reset()
}

// parkRootDelegateAttention silences the root attention rail when a Stop is
// accepted, mirroring the queue and steering holds that Stop parks. The park
// itself lives in those durable holds (rootAttentionRailParked derives from
// them), so this is the rail's side of the Stop: the wake is cleared and any
// paced retry cancelled; until the user re-engages, nothing on this rail may
// wake the session. Pending IDs are never dropped — they stay cached for
// unparkRootDelegateAttention to re-arm.
// The stable-delegate drive rail is deliberately outside this park: its
// wakes stand down at the notification admission gate, so they cost a no-op
// cycle, never a turn.
func (s *Session) parkRootDelegateAttention() {
	s.attentionMu.Lock()
	s.rootAttentionWake = false
	s.resetRootAttentionRetryLocked()
	s.attentionMu.Unlock()
}

// unparkRootDelegateAttention releases the rail a Stop parked, at the same
// re-engagement that releases QueueHeld (turn/start, turn/queue, drain,
// promote). The re-engagement's commit cleared the holds the park lives in;
// what remains is the rail's side of the release: deferred, not dropped,
// pending IDs re-arm the wake so a notification turn delivers them after the
// user's own.
func (s *Session) unparkRootDelegateAttention() {
	s.attentionMu.Lock()
	shouldWake := len(s.rootAttentionWakeIDs) != 0 && !s.rootAttentionWake
	if shouldWake {
		s.rootAttentionWake = true
	}
	s.attentionMu.Unlock()
	if shouldWake {
		s.notify()
	}
}

// rootAttentionRailParked reports whether an accepted Stop has parked the
// attention rail. The rail has no stored park of its own: parked is a
// projection of the durable holds a Stop sets (QueueHeld, SteeringHeld),
// read at evaluation time the same way pendingQueueDepth reads the held
// queue. Deriving the read keeps the rail consistent with the holds by
// construction — no interleaving of a Stop's park and a re-engagement's
// unpark can leave a stale in-memory copy disagreeing with what the user
// stopped, which is exactly the race the in-memory flag this replaced had.
// While a hold stands, fresh arms cache their IDs but neither set the wake
// flag nor notify, the paced retry stands down at fire, and an
// attention-only notification turn stands down — the deliveries the user
// stopped wait for the user, not the clock. The store's state mutex is read
// without attentionMu held, per the lock order the live Stop path
// established.
func (s *Session) rootAttentionRailParked() bool {
	if s == nil || s.clientMutations == nil {
		return false
	}
	return s.clientMutations.queueHeld() || s.clientMutations.steeringHeld()
}

func (s *Session) scheduleDelegateAttentionArmRetryLocked() {
	if s.delegateAttentionArmRetry.active || len(s.delegateAttentionArmIDs) == 0 {
		return
	}
	delay, generation := s.delegateAttentionArmRetry.arm()
	s.sclock().AfterFunc(delay, func() {
		s.attentionMu.Lock()
		if !s.delegateAttentionArmRetry.fire(generation) {
			s.attentionMu.Unlock()
			return
		}
		ids := make([]string, 0, len(s.delegateAttentionArmIDs))
		for id := range s.delegateAttentionArmIDs {
			ids = append(ids, id)
		}
		s.attentionMu.Unlock()
		sort.Strings(ids)

		resolved := make([]string, 0, len(ids))
		for _, id := range ids {
			if s.armDelegateAttentionOnce(id) == nil {
				resolved = append(resolved, id)
			}
		}
		s.attentionMu.Lock()
		for _, id := range resolved {
			delete(s.delegateAttentionArmIDs, id)
		}
		if len(s.delegateAttentionArmIDs) == 0 {
			s.resetDelegateAttentionArmRetryLocked()
		} else {
			s.delegateAttentionArmRetry.settle(true, delay, jobNotificationRetryMaxDelay)
			s.scheduleDelegateAttentionArmRetryLocked()
		}
		s.attentionMu.Unlock()
	})
}

func (s *Session) resetDelegateAttentionArmRetryLocked() {
	s.delegateAttentionArmRetry.reset()
}

func (s *Session) scheduleDelegateAttentionRetry() {
	if s == nil {
		return
	}
	s.attentionMu.Lock()
	if s.stableAttentionRetry.active {
		s.attentionMu.Unlock()
		return
	}
	delay, generation := s.stableAttentionRetry.arm()
	s.stableAttentionRetry.delay = delay
	s.attentionMu.Unlock()
	s.sclock().AfterFunc(delay, func() {
		s.attentionMu.Lock()
		if !s.stableAttentionRetry.fire(generation) {
			s.attentionMu.Unlock()
			return
		}
		s.attentionMu.Unlock()

		runnable := s.delegateController != nil && s.delegateController.hasRunnableDelegateAttention()
		if runnable {
			s.notify()
		}
		pending := s.delegateController != nil && s.delegateController.hasPendingDelegateAttention()
		s.attentionMu.Lock()
		// Runnable work the drive retries (a parked delegate's fenced
		// escalation is runnable without being pending) backs the next retry
		// off too: resetting here would re-run a persistent failure at the
		// initial delay forever. The backoff doubles the current delay, not
		// the one this retry was armed with, so a reset that landed while it
		// was armed holds.
		s.stableAttentionRetry.settle(pending || runnable, s.stableAttentionRetry.delay, jobNotificationRetryMaxDelay)
		s.attentionMu.Unlock()
		if pending {
			s.scheduleDelegateAttentionRetry()
		}
	})
}

func (s *Session) resetDelegateAttentionRetry() {
	if s == nil {
		return
	}
	s.attentionMu.Lock()
	s.stableAttentionRetry.reset()
	s.attentionMu.Unlock()
}

// resetDelegateAttentionRetryDelay ends a backoff episode whose work
// succeeded, so the next failure retries from the initial delay. It leaves an
// armed retry in place, since other delegates' work may still need it; that
// retry fires at the delay it was armed with and backs off from the reset
// delay.
func (s *Session) resetDelegateAttentionRetryDelay() {
	s.attentionMu.Lock()
	s.stableAttentionRetry.delay = jobNotificationRetryInitialDelay
	s.attentionMu.Unlock()
}

// rearmRootDelegateAttentionFromTranscript reconstructs the root wake cache
// from the only durable attention authority. It performs no provider or Session
// construction and is called after the root transcript is attached/replayed.
//
// entries is the final in-memory entry list restore produced (refreshed from
// disk when delegate delivery replay appended to the transcript): folding it
// instead of re-opening the file is what keeps resume from strict-decoding
// the whole transcript a second time. A nil list means the caller has no
// decoded list to fold (a fresh session, or a refresh that produced none);
// the fold then re-reads the file.
func (s *Session) rearmRootDelegateAttentionFromTranscript(entries []transcript.Entry) error {
	if !s.isRootDelegateAttentionReceiver() {
		return nil
	}
	// Read the durable holds before attentionMu — the order the live Stop
	// path established, where the store's state mutex never nests inside
	// attentionMu. A hold that survived the restart is a Stop the user has
	// not re-engaged; the rebuilt rail reads parked from those holds at
	// every gate, so it must come up unarmed while they stand, or a
	// transient provider failure re-opens the paced retry over a queue the
	// user parked.
	holdsStand := s.clientMutations != nil &&
		(s.clientMutations.queueHeld() || s.clientMutations.steeringHeld())
	s.attentionMu.Lock()
	s.mu.Lock()
	ready := s.transcriptReady
	writer := s.transcript
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || writer == nil || sessionID == "" || stateDir == "" {
		s.attentionMu.Unlock()
		return nil
	}
	var fold delegateAttentionFold
	var err error
	if entries != nil {
		fold, err = s.foldDelegateAttentionEntries(entries)
	} else {
		fold, err = s.readDelegateAttentionFold(transcriptPath(stateDir, sessionID), sessionID)
	}
	if err != nil {
		s.attentionMu.Unlock()
		return err
	}
	ids := fold.pendingIDs()
	// A pending attention's model-visible turn can be missing from the
	// resumed history: attention turns carry no fold-publication rewrite
	// (they are deliberately excluded from the pair log — their durability
	// is attention-owned), so ResumeHistory's last-marker anchor drops any
	// recorded before a compaction marker, and the attention would re-arm
	// with no content explaining what must be addressed. The attention
	// machinery owns its restart story, so restore the durable content here,
	// before arming the wake: retain is ID-keyed
	// and idempotent — a still-resident turn is replaced in place (no
	// duplicate on a boundary-free restart), a missing one is re-appended,
	// and resolved attentions are never pending, so nothing resurrects.
	pendingTurns := fold.pendingTurns("")
	if err := s.restoreAttentionTurns(pendingTurns, nil); err != nil {
		s.attentionMu.Unlock()
		return err
	}
	s.rootAttentionWakeIDs = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s.rootAttentionWakeIDs[id] = struct{}{}
	}
	shouldWake := len(ids) != 0 && !s.rootAttentionWake && !holdsStand
	if shouldWake {
		s.rootAttentionWake = true
	}
	s.attentionMu.Unlock()
	if shouldWake {
		s.notify()
	}
	return nil
}

// foldDelegateAttentionEntries is the entries form of
// readDelegateAttentionFold: same fold over the same entry list, no file
// read.
func (s *Session) foldDelegateAttentionEntries(entries []transcript.Entry) (delegateAttentionFold, error) {
	if foldEntries := s.cfg.testOnly.delegateAttentionFoldEntries; foldEntries != nil {
		return foldEntries(entries)
	}
	return foldDelegateAttention(entries)
}

// retainDelegateAttentionTurn puts the durable turn into the history: a resident
// copy is replaced in place, a missing one is restored. later names the items
// that arrived after turn, so a restored turn keeps its delivery order among
// the ones still resident.
func (s *Session) retainDelegateAttentionTurn(turn schema.Turn, later ...schema.Turn) error {
	if turn.Kind != schema.TurnSteering || turn.AttentionID == "" {
		return errors.New("durable delegate attention turn is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.history {
		resident := s.history[index]
		if resident.AttentionID != turn.AttentionID {
			continue
		}
		if resident.Kind != schema.TurnSteering || !reflect.DeepEqual(resident.Message, turn.Message) {
			return fmt.Errorf("resident attention %q conflicts with durable content", turn.AttentionID)
		}
		// In-place replacement, not an append: a fold snapshotted before this
		// must not be able to publish over it and silently resurrect the
		// stale resident turn.
		s.history[index] = turn
		s.bumpHistoryRevisionLocked()
		return nil
	}
	var at int
	s.history, at = insertAttentionTurn(s.history, turn, later)
	s.growTurnHistoryBaseline([]int{at})
	if at < len(s.history)-1 {
		s.bumpHistoryRevisionLocked()
	}
	return nil
}

func (s *Session) removeUnverifiedDelegateAttentionTurn(turn schema.Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.history {
		if reflect.DeepEqual(s.history[index], turn) {
			// A deletion, not an append: a fold snapshotted before this must
			// not be able to publish over it and silently resurrect the
			// removed turn.
			s.history = append(s.history[:index], s.history[index+1:]...)
			// Deleting a turn strictly before the N4 boundary shifts every
			// in-flight turn left by one, so the boundary moves with them —
			// atomically with the mutation.
			// Deleting AT the boundary leaves it correct: the next in-flight
			// turn slides into the boundary index.
			if index < s.turnHistoryBaseline {
				s.turnHistoryBaseline--
			}
			s.bumpHistoryRevisionLocked()
			return
		}
	}
}

func (s *Session) readDelegateAttentionFold(path, sessionID string) (delegateAttentionFold, error) {
	// The hook is read under s.mu because a test installs and clears it under
	// that same lock (updateSessionTestConfig). Every caller reaches here with
	// s.mu released -- the rows above read what the fold needs from it and drop
	// it again -- so taking it here nests under no caller's hold. attentionMu
	// stays the outer lock: nothing under s.mu takes it.
	s.mu.Lock()
	readFold := s.cfg.testOnly.delegateAttentionReadFold
	s.mu.Unlock()
	if readFold != nil {
		return readFold(path, sessionID)
	}
	return s.attentionFoldCursor.read(path, sessionID)
}

// delegateAttentionFoldTailBytes is how much of the folded prefix, ending at
// the cursor's offset, a resumed read re-reads and compares. One page costs
// the same read as a few bytes and covers the whole of most trailing records.
const delegateAttentionFoldTailBytes = 4096

// delegateAttentionFoldCursor keeps a resident Session's attention fold of its
// own transcript, so the check-append-verify reads of every delivery and
// resolution decode only what was appended since the last read instead of the
// whole transcript (a long root's runs to ~100MB, and it is read about five
// times per delegate report). The zero value is an empty cursor, and the
// Session's attentionMu guards it.
//
// Every read still reads the file: a resumed read decodes the bytes past the
// offset, so the verify after an append sees the appended record on disk
// exactly as a full read would. What a resumed read trusts is the prefix, and
// it trusts it only while the file is provably the one that was folded, grown
// by appends: the same session, the same file (device and inode, so a
// replacement renamed over the path refolds), at least as long as the offset
// (a truncation refolds), and the recorded trailing bytes still at the offset
// (a truncation that regrew past it refolds). Any other outcome, and any
// error, folds from byte zero.
//
// Those checks cover every change the transcript's writers make. The resident
// writer appends, and rolls back only its own failed append, all under
// attentionMu — which every read here also holds, so a read never sees a record
// that is later taken back. A cold writer appends, and on open trims an
// unterminated tail, which a fold never consumes. What the checks cannot see is
// an in-place rewrite before the tail window that keeps the file's identity,
// its length, and its last delegateAttentionFoldTailBytes; nothing writes a
// transcript that way.
//
// This is not delegateAttentionFoldCache (foldcache) for three reasons. It
// resumes only when a grown file's mtime moved, and a delivery's appends can
// land within one coarse mtime tick of the previous write, so the verify
// after an append would refold from zero; it has no file-identity check; and
// its shared LRU can evict the root's entry during a hub-wide sweep.
type delegateAttentionFoldCursor struct {
	sessionID string
	file      os.FileInfo
	offset    int64
	tail      []byte
	fold      delegateAttentionFold
}

// read returns the fold of the transcript at path, with readDelegateAttentionFold's
// decisions: the same fold, the same errors, and the empty fold for a missing
// file. The cursor is left empty unless this read succeeds on a regular file.
// The returned fold is the one the next read resumes from, so callers must not
// change it.
func (c *delegateAttentionFoldCursor) read(path, sessionID string) (delegateAttentionFold, error) {
	previous := *c
	*c = delegateAttentionFoldCursor{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newDelegateAttentionFold(), nil
		}
		return delegateAttentionFold{}, fmt.Errorf("open delegate attention transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return delegateAttentionFold{}, fmt.Errorf("stat delegate attention transcript: %w", err)
	}
	if !info.Mode().IsRegular() {
		// Only a regular file has offsets to resume from; anything else is
		// folded as a stream, from its start, every time.
		fold, _, err := extendDelegateAttentionFoldFrom(f, 0, newDelegateAttentionFold(), sessionID)
		return fold, err
	}
	from, prior := int64(0), newDelegateAttentionFold()
	if previous.resumes(f, info, sessionID) {
		from, prior = previous.offset, previous.fold
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return delegateAttentionFold{}, fmt.Errorf("seek delegate attention transcript: %w", err)
		}
	}
	fold, consumed, err := extendDelegateAttentionFoldFrom(f, from, prior, sessionID)
	if err != nil {
		return delegateAttentionFold{}, err
	}
	tail := make([]byte, min(consumed, delegateAttentionFoldTailBytes))
	if _, err := f.ReadAt(tail, consumed-int64(len(tail))); err != nil {
		return delegateAttentionFold{}, fmt.Errorf("read delegate attention transcript tail: %w", err)
	}
	*c = delegateAttentionFoldCursor{sessionID: sessionID, file: info, offset: consumed, tail: tail, fold: fold}
	return fold, nil
}

// resumes reports whether f is the file c folded, grown only by appends since.
func (c delegateAttentionFoldCursor) resumes(f *os.File, info os.FileInfo, sessionID string) bool {
	if c.file == nil || c.sessionID != sessionID || !os.SameFile(c.file, info) || info.Size() < c.offset {
		return false
	}
	tail := make([]byte, len(c.tail))
	if _, err := f.ReadAt(tail, c.offset-int64(len(tail))); err != nil {
		return false
	}
	return bytes.Equal(tail, c.tail)
}

// resolveAttentionDurably appends attention markers through the one writer
// already owned by a resident Session. A post-append fold proves the marker is
// durable because transcript.Writer deliberately treats a closed writer as a
// successful no-op.
func (s *Session) resolveAttentionDurably(ids []string, disposition delegateAttentionResolution) error {
	return s.resolveAttentionDurablyForGeneration(ids, disposition, 0)
}

func (s *Session) resolveAttentionDurablyForGeneration(ids []string, disposition delegateAttentionResolution, resumeGeneration uint64) error {
	if s == nil {
		return errors.New("attention resolution session is nil")
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()

	s.mu.Lock()
	ready := s.transcriptReady
	writer := s.transcript
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || writer == nil || sessionID == "" || stateDir == "" {
		return errors.New("attention resolution requires an attached transcript writer")
	}
	path := transcriptPath(stateDir, sessionID)
	fold, err := s.attentionFoldCursor.read(path, sessionID)
	if err != nil {
		return err
	}
	disposition, resumeGeneration, err = discardUnlessResolvedResolution(fold, ids, disposition, resumeGeneration)
	if err != nil {
		return err
	}
	if err := validateDelegateAttentionResolutions(fold, ids, disposition, resumeGeneration); err != nil {
		return err
	}
	for _, attentionID := range ids {
		if fold.resolutions[attentionID] != disposition || fold.resumeGenerations[attentionID] != resumeGeneration {
			continue
		}
		writer, fold, err = s.reopenAttentionTranscriptDurably(writer, path, sessionID)
		if err != nil {
			return err
		}
		break
	}
	// Resolutions reach the session's own transcript from delivery and stop
	// goroutines at any time: the running execution's, or a delivery turn.
	if err := appendDelegateAttentionResolutions(writer, transcript.PlaceAsync, s.withEntryModel(schema.Turn{}).Model, fold, ids, disposition, resumeGeneration); err != nil {
		return err
	}
	verified, err := s.attentionFoldCursor.read(path, sessionID)
	if err != nil {
		return err
	}
	for _, attentionID := range ids {
		if verified.resolutions[attentionID] != disposition || verified.resumeGenerations[attentionID] != resumeGeneration {
			return fmt.Errorf("attention %q resolution was not durably appended", attentionID)
		}
	}
	if s.delegateController != nil {
		s.delegateController.attentionCauses.forget(ids...)
	}
	return nil
}

// reopenAttentionTranscriptDurably reopens the writer from complete records
// (its sequence continues the file's shared tail, which the old writer still
// holds open), establishes a fresh filesystem barrier, and preserves the session's
// live failure accounting before publishing the replacement handle. The caller
// holds attentionMu, which serializes this lifecycle with every transcript use.
func (s *Session) reopenAttentionTranscriptDurably(writer *transcript.Writer, path, sessionID string) (*transcript.Writer, delegateAttentionFold, error) {
	open := s.cfg.testOnly.delegateAttentionOpenWriter
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	reopened, entries, err := open(path, sessionID)
	if err != nil {
		return nil, delegateAttentionFold{}, err
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = reopened.Close()
		}
	}()
	fold, err := foldDelegateAttention(entries)
	if err != nil {
		return nil, delegateAttentionFold{}, err
	}
	if err := reopened.EstablishDurability(); err != nil {
		return nil, delegateAttentionFold{}, err
	}
	reopened.SyncInterval = writer.SyncInterval
	reopened.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil, delegateAttentionFold{}, errors.New("attention transcript session is closed")
	}
	if s.transcript != writer {
		s.mu.Unlock()
		return nil, delegateAttentionFold{}, errors.New("attention transcript changed during durability recovery")
	}
	s.setTranscriptLocked(reopened)
	s.mu.Unlock()
	adopted = true
	if err := writer.Close(); err != nil {
		return reopened, fold, err
	}
	return reopened, fold, nil
}

// stabilizeAttentionForStop repairs a writer whose failed durable append also
// failed rollback. Closing and reopening establishes a durability boundary and
// continues the writer's sequence from the file's shared tail before stop can release
// the generation. If rollback removed the marker, the stop records a discard.
func (s *Session) stabilizeAttentionForStop(attentionID string) error {
	if s == nil || attentionID == "" {
		return errors.New("attention stabilization identity is incomplete")
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()

	s.mu.Lock()
	ready := s.transcriptReady
	writer := s.transcript
	sessionID := s.id
	stateDir := s.stateDir
	closed := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closed || !ready || writer == nil || sessionID == "" || stateDir == "" {
		return errors.New("attention stabilization requires an attached transcript writer")
	}
	path := transcriptPath(stateDir, sessionID)
	open := s.cfg.testOnly.delegateAttentionOpenWriter
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	reopened, entries, err := open(path, sessionID)
	if err != nil {
		return err
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = reopened.Close()
		}
	}()

	fold, err := foldDelegateAttention(entries)
	if err != nil {
		return err
	}
	disposition, resolved := fold.resolutions[attentionID]
	if !resolved {
		disposition = delegateAttentionDiscarded
	}
	if !resolved {
		if _, err := reopened.Record(s.withEntryModel(delegateAttentionResolutionTurn(attentionID, disposition)), transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceAsync}); err != nil {
			return fmt.Errorf("attention %q was not durably stabilized: %w", attentionID, err)
		}
	} else if err := reopened.EstablishDurability(); err != nil {
		return err
	}
	verified, err := s.attentionFoldCursor.read(path, sessionID)
	if err != nil {
		return err
	}
	if verified.resolutions[attentionID] != disposition {
		return fmt.Errorf("attention %q was not durably stabilized", attentionID)
	}
	reopened.SyncInterval = writer.SyncInterval
	reopened.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	if s.closingOrClosedLocked() || s.transcript != writer {
		s.mu.Unlock()
		return errors.New("attention stabilization transcript changed")
	}
	s.setTranscriptLocked(reopened)
	s.mu.Unlock()
	adopted = true
	return writer.Close()
}

// appendDelegateAttentionResolutions appends a resolution for each of ids that
// fold does not already record, once per ID however often ids repeats it. It
// leaves fold unchanged: a resident Session's fold is its attention cursor's,
// which must hold only what the file holds, or the verify read after this
// append would confirm it from memory.
// model is the resident session's model, "" for a cold writer, which has no
// session.
func appendDelegateAttentionResolutions(writer *transcript.Writer, place transcript.Placement, model string, fold delegateAttentionFold, ids []string, disposition delegateAttentionResolution, resumeGeneration uint64) error {
	if err := validateDelegateAttentionResolutions(fold, ids, disposition, resumeGeneration); err != nil {
		return err
	}
	appended := make(map[string]bool, len(ids))
	for _, attentionID := range ids {
		if appended[attentionID] {
			continue
		}
		if previous, resolved := fold.resolutions[attentionID]; resolved && previous == disposition && fold.resumeGenerations[attentionID] == resumeGeneration {
			continue
		}
		turn := delegateAttentionResolutionTurnForGeneration(attentionID, disposition, resumeGeneration)
		turn.Model = model
		if _, err := writer.Record(turn, transcript.RecordOptions{Door: transcript.DoorSynced, Place: place}); err != nil {
			return fmt.Errorf("attention %q resolution was not durably appended: %w", attentionID, err)
		}
		appended[attentionID] = true
	}
	return nil
}

func validateDelegateAttentionResolutions(fold delegateAttentionFold, ids []string, disposition delegateAttentionResolution, resumeGeneration uint64) error {
	if disposition != delegateAttentionConsumed && disposition != delegateAttentionDiscarded {
		return fmt.Errorf("invalid attention resolution %q", disposition)
	}
	if resumeGeneration != 0 && (disposition != delegateAttentionConsumed || len(ids) != 1) {
		return errors.New("resume generation requires one consumed attention ID")
	}
	for _, attentionID := range ids {
		if attentionID == "" {
			return errors.New("attention resolution ID is empty")
		}
		if previous, resolved := fold.resolutions[attentionID]; resolved {
			if previous != disposition || fold.resumeGenerations[attentionID] != resumeGeneration {
				return fmt.Errorf("attention %q has conflicting resolution %q", attentionID, previous)
			}
			continue
		}
		if _, pending := fold.content[attentionID]; !pending {
			return fmt.Errorf("attention %q is not pending", attentionID)
		}
		if previousID, conflict := delegateAttentionResumeClaimConflict(fold, attentionID, resumeGeneration); conflict {
			return fmt.Errorf("resume generation %d already claims attention %q", resumeGeneration, previousID)
		}
	}
	return nil
}

func attentionTransparentTurns(history []schema.Turn) []schema.Turn {
	visibleCount := 0
	for _, turn := range history {
		if turn.Kind != schema.TurnAttentionResolution {
			visibleCount++
		}
	}
	if visibleCount == len(history) {
		return history
	}
	visible := make([]schema.Turn, 0, visibleCount)
	for _, turn := range history {
		if turn.Kind != schema.TurnAttentionResolution {
			visible = append(visible, turn)
		}
	}
	return visible
}

func attentionTransparentRecentCutoff(history []schema.Turn, preserveRecent int) (int, bool) {
	if preserveRecent <= 0 {
		return len(history), len(history) != 0
	}
	seen := 0
	for i, turn := range slices.Backward(history) {
		if turn.Kind == schema.TurnAttentionResolution {
			continue
		}
		seen++
		if seen == preserveRecent {
			for j := i - 1; j >= 0; j-- {
				if history[j].Kind != schema.TurnAttentionResolution {
					return i, true
				}
			}
			return 0, false
		}
	}
	return 0, false
}

// dropUnleasedWatchAttentionTurns keeps unresolved WATCH attention out of an
// UNLEASED turn's projection, by durable identity (LIFE-01, #2340).
//
// Deferring a watch notice to durable attention appends it to resident history,
// because a durable attention item IS a model-visible turn. On a turn with no
// lease that defeats the gate from the other side: the queue is empty (the
// notice was moved off it), so queue-based admission has nothing to refuse, and
// the request then expands the very history that holds the notice. The turn also
// cannot CREDIT the presentation -- only a leased generation commits one -- so a
// later leased run would present it a second time.
//
// Identity, not content, is the test. This half appends under
// deferredWatchAttentionPrefix, and the fold's resolutions say which of those
// rows are still pending. Settled history -- including an item a leased
// generation already presented -- is preserved untouched.
//
// A projection with no watch row in it is the common case, and it needs no fold
// read at all: the prescan below answers it from the turns already in hand, so
// an unleased turn whose history holds no watch row costs nothing.
//
// A leased turn drops nothing: it IS the presentation the notice waits for.
func (s *Session) dropUnleasedWatchAttentionTurns(ctx context.Context, turns []schema.Turn) (kept []schema.Turn, droppedAt []int) {
	if !s.stableChildTurnNeedsLease(ctx) {
		return turns, nil
	}
	if !slices.ContainsFunc(turns, func(turn schema.Turn) bool {
		return isWatchNoticeAttentionID(turn.AttentionID)
	}) {
		return turns, nil
	}
	// The fold cursor behind this read is guarded by attentionMu: every append
	// and resolution that moves it holds that lock, so the read is taken under
	// it.
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return turns, nil
	}
	unresolved := func(turn schema.Turn) bool {
		if !isWatchNoticeAttentionID(turn.AttentionID) {
			return false
		}
		if _, known := fold.content[turn.AttentionID]; !known {
			// Not one of the fold's rows: nothing durable is waiting behind it,
			// so there is nothing to withhold.
			return false
		}
		_, resolved := fold.resolutions[turn.AttentionID]
		return !resolved
	}
	for i, turn := range turns {
		if unresolved(turn) {
			if kept == nil {
				kept = make([]schema.Turn, i, len(turns)-1)
				copy(kept, turns[:i])
			}
			droppedAt = append(droppedAt, i)
			continue
		}
		if kept != nil {
			kept = append(kept, turn)
		}
	}
	if kept == nil {
		return turns, nil
	}
	return kept, droppedAt
}

// watchSendAttentionPrefix marks the durable attention rows the send rail
// writes for the receiver of a watch frame.
const watchSendAttentionPrefix = "watch:"

// isWatchNoticeAttentionID reports whether id names a durable watch notice
// row. Two producers write one: the send rail, which delivers a frame to its
// receiver under watchSendAttentionPrefix, and an unleased turn that may not
// carry a notice, which parks it under deferredWatchAttentionPrefix. Withholding
// a live notice from a projection must answer for both producers, so the
// prefixes are decided in one place.
func isWatchNoticeAttentionID(id string) bool {
	return strings.HasPrefix(id, watchSendAttentionPrefix) || strings.HasPrefix(id, deferredWatchAttentionPrefix)
}
