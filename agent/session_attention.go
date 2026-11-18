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
	"primeradiant.com/evener/agent/internal/delegatestore"
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

func appendColdAttentionResolutionWithOpen(path, expectedSessionID string, ids []string, disposition delegateAttentionResolution, open delegateAttentionWriterOpener) (err error) {
	return appendColdAttentionResolutionForGenerationWithOpen(path, expectedSessionID, ids, disposition, 0, open)
}

func appendColdAttentionResolutionForGenerationWithOpen(path, expectedSessionID string, ids []string, disposition delegateAttentionResolution, resumeGeneration uint64, open delegateAttentionWriterOpener) (err error) {
	if expectedSessionID == "" {
		return errors.New("cold attention resolution session ID is empty")
	}
	if open == nil {
		return errors.New("cold attention resolution writer opener is nil")
	}
	// The discard-unless-resolved mode takes its concrete resolution from the
	// transcript it is about to write, so its one fold read is the writer's own
	// (below). Every other request keeps the cheap preflight refusal before a
	// writer is opened.
	if disposition == delegateAttentionDiscardUnlessResolved {
		if err := validateDiscardUnlessResolvedShape(disposition, ids, resumeGeneration); err != nil {
			return err
		}
	} else {
		preflight, err := readDelegateAttentionFold(path, expectedSessionID)
		if err != nil {
			return err
		}
		if err := validateDelegateAttentionResolutions(preflight, ids, disposition, resumeGeneration); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	writer, entries, err := open(path, expectedSessionID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	fold, err := foldDelegateAttention(entries)
	if err != nil {
		return err
	}
	disposition, resumeGeneration, err = discardUnlessResolvedResolution(fold, ids, disposition, resumeGeneration)
	if err != nil {
		return err
	}
	allResolved := true
	for _, attentionID := range ids {
		if fold.resolutions[attentionID] != disposition || fold.resumeGenerations[attentionID] != resumeGeneration {
			allResolved = false
			break
		}
	}
	if allResolved {
		return writer.EstablishDurability()
	}
	// A cold writer has no session: each resolution is a delivery turn.
	return appendDelegateAttentionResolutions(writer, transcript.PlaceDelivery, "", fold, ids, disposition, resumeGeneration)
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
		// The handoff arms this frame by ID alone, and the frame can have landed
		// after the attach folded the pending set: the restore that loaded the
		// other rows' turns ran before it. Its turn is read here and published
		// below, only once the arm passes its own gate: a frame put into history
		// before a reservation refuses the arm is presented by the successor that
		// blocked it, which resolves the row, and the retry then finds nothing to
		// arm and journals no release — so the older held report misses the very
		// request that carried the fresh frame. Publishing under the same hold
		// that fires the release keeps the frame and the release one moment.
		fold, foldErr := s.delegateAttentionFoldLocked()
		if foldErr != nil {
			s.attentionMu.Unlock()
			return false, foldErr
		}
		turn, hasTurn := fold.turns[attentionID]
		_, blocker, plan, emit, wakeGeneration, err := s.delegateController.tryOpenDelegateAttention(s.owningDelegateID, attentionID)
		if err == nil && blocker == nil && hasTurn {
			err = s.retainDelegateAttentionTurn(turn)
		}
		s.attentionMu.Unlock()
		if err != nil {
			return false, err
		}
		if blocker != nil {
			// The arm cannot pass its gate until the reservation settles. Read
			// the seam with attentionMu released: it takes s.mu, and the
			// capture elsewhere takes attentionMu then s.mu.
			s.mu.Lock()
			blocked := s.cfg.testOnly.delegateAttentionArmBlocked
			s.mu.Unlock()
			if blocked != nil {
				blocked()
			}
			<-blocker
			continue
		}
		if emit {
			s.delegateController.emitDelegateUpdate(plan)
		}
		// The holds end here, before the caller's wake: the notify func
		// drives the receiver synchronously, and a hold still in place at
		// the drive's list read would present the armed row alone and leave
		// the stopped report for a later generation. The generation the arm
		// superseded is the controller's own, sampled where it journaled the
		// release, so a stop that settled after this admission keeps its hold.
		// A failed arm never reaches this point; the holds stand.
		s.releaseAutomaticDriveHolds(delegateHoldReleaseArm, wakeGeneration)
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

// reopenConsumedDelegateAttention restores an attention item a presentation
// start consumed but will never present: the start failed after the consume
// marker landed. The marker is irreversible by design — it fences concurrent
// double presentation — so the recovery re-delivers the same content under a
// fresh identity keyed by the failed start's generation. Replaying the same
// failed generation is idempotent (the same identity and content is a no-op),
// and a later failed generation keys a fresh identity, so however many starts
// fail, the report reads as pending instead of silently being lost. Which
// drive presents it depends on the failure: a refused commit never replaced
// the controller's wake, so the paced retry re-drives it in-process; a start
// that committed and then lost its launch to a refused lane revalidation
// leaves the child stamped drain-abandoned, and that stamp deliberately quiets
// every drive loop instead of churning against the foreign-held lane — the row
// is presented when a later attention row for the delegate re-arms a wake,
// when a restart reconciles the wake from the transcript, or when an explicit
// send revives the lane. An item that survived its failed start needs nothing:
// the ordinary retry still presents it, and re-delivering it would present
// the same report twice. A stop or close admitted between the caller's
// teardown guards and the append is re-checked after it, and the row that
// teardown now owns is resolved as discarded instead of surviving it.
// The reopened row's identity is returned, empty when nothing was
// re-delivered.
// quietAt names the settled generation whose stop owns the reopened row's
// quiet, or zero when the row must present at once. The quiet is installed
// before the append that publishes the row, so there is no instant at which a
// drive can select the row while the projection still holds it out: a marker
// written in that window would consume a report the model never sees.
func (s *Session) reopenConsumedDelegateAttention(attentionID string, quietAt uint64) (string, error) {
	if s == nil {
		return "", errors.New("delegate attention session is nil")
	}
	if attentionID == "" {
		return "", errors.New("delegate attention reopen identity is incomplete")
	}
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		s.attentionMu.Unlock()
		return "", err
	}
	disposition, consumed := fold.resolutions[attentionID]
	message, hasContent := fold.content[attentionID]
	generation := fold.resumeGenerations[attentionID]
	s.attentionMu.Unlock()
	if !consumed || disposition != delegateAttentionConsumed {
		return "", nil
	}
	if !hasContent {
		return "", fmt.Errorf("attention %q was consumed without content to reopen", attentionID)
	}
	reopenID := delegateAttentionReopenID(attentionID, generation)
	if _, resolved := fold.resolutions[reopenID]; resolved {
		// This failed generation already reopened and the reopened row was
		// consumed again; its own failed start reopens under its own key.
		return "", nil
	}
	// The seam is runtime-mutable through updateSessionTestConfig, so its
	// read shares the setter's session mutex.
	s.mu.Lock()
	observer := s.cfg.testOnly.delegateAttentionBeforeReopenAppend
	s.mu.Unlock()
	if quietAt != 0 {
		s.holdDelegateAttentionFromAutomaticDriveAt(reopenID, quietAt)
	}
	if observer != nil {
		// A test seam, nil in production: the teardown guards have all run by
		// here, so a test can admit a stop into exactly the window between
		// them and the durable append below.
		observer()
	}
	_, err = s.appendDelegateAttentionMessageDurably(reopenID, message)
	if err == nil {
		// The teardown guards ran before the append, but a stop or close can
		// admit while it was in flight, and the fresh id is invisible to that
		// stop's plan-time enumeration (no wake notes it), so the row would
		// survive as pending on a deliberately stopped delegate. Re-check the
		// same predicates and hand the row to the teardown that now owns it:
		// the discard resolves it durably, the same resolution the stop's own
		// machinery would have written had the row existed at plan time.
		s.mu.Lock()
		receiverClosed := s.closingOrClosedLocked()
		s.mu.Unlock()
		if receiverClosed {
			// A closed writer treats the append as a successful no-op, so the
			// row never landed: the receiver's own teardown owns everything
			// its transcript still held, and there is nothing to resolve.
			return "", nil
		}
		refused := s.delegateController.attentionReopenRefused(s.owningDelegateID)
		// The reopened row supersedes the latched trigger — same content,
		// fresh identity — and the fold keeps the latched trigger's turn
		// alongside every pending row, so an unleased turn in this window
		// would render the identical report twice in one request. The latch
		// and its cause both move to the fresh identity; the next presenting
		// request clears them.
		//
		// The original identity resolved before this reopen, which dropped the
		// controller's copy, so the latch is the only place its lineage
		// survived. The cause moves to the fresh identity two ways: latched,
		// for the presenting request, and in the controller's registry, so it
		// still survives an accept that replaces the single latch slot first.
		// It moves unchanged — the reopened row is the same message re-keyed,
		// not a new one, so it earns no additional message hop.
		//
		// A refused reopen leaves no latch behind. The row it would name is the
		// one this teardown discards, and pendingTurns keeps the row it is told
		// to keep even when resolved, so a latch left on either identity would
		// re-present a stopped delegate's report and survive a compaction that
		// should drop it.
		if refused {
			s.attentionMu.Lock()
			s.attentionAcceptedID = ""
			s.attentionAcceptedCause = nil
			s.attentionMu.Unlock()
		} else {
			s.attentionMu.Lock()
			if s.attentionAcceptedID == attentionID {
				s.attentionAcceptedID = reopenID
				if s.attentionAcceptedCause != nil {
					s.delegateController.attentionCauses.remember(reopenID, s.attentionAcceptedCause)
				}
			}
			s.attentionMu.Unlock()
		}
		// The superseded source's resident copy never reached a request —
		// every reopen fires only after a start that failed before presenting
		// — and the reopened row that just landed carries the same content to
		// the next one, so the resident copy would only duplicate the report
		// in every later request. Drop it from the resident history; the
		// transcript row stays, because the fold, not the history, is the
		// delivery ledger.
		s.attentionMu.Lock()
		s.dropResidentDelegateAttentionTurnLocked(attentionID)
		s.attentionMu.Unlock()
		if refused {
			if resolveErr := s.resolveAttentionDurably([]string{reopenID}, delegateAttentionDiscarded); resolveErr != nil {
				return "", resolveErr
			}
		}
		return reopenID, nil
	}
	return "", err
}

// reopenLatchedDelegateAttention reopens the accepted trigger a failed
// attention-triggered run latched but never presented: the presentation turn
// died before its request could carry the report. A settled full-history
// request presented the report and cleared the latch, so it reopens nothing;
// a responses-continuation delta carries no attention and clears no latch,
// so a delta-settled round still leaves the reopen owed. A receiver whose own
// teardown is discarding its pending attention, and a receiver a deliberate
// stop or the tree's close owns, are left to that teardown — re-delivering
// into either would race the discard and strand a fresh row the teardown's
// plan-time enumeration never knew, the same policy reopenFailedDelegateAttention
// enforces for the drive arms. The reopened identity is returned, empty when
// nothing was reopened.
func (s *Session) reopenLatchedDelegateAttention(quietAt uint64) (string, error) {
	if s == nil {
		return "", errors.New("delegate attention session is nil")
	}
	s.mu.Lock()
	closed := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closed {
		return "", nil
	}
	if s.delegateController.attentionReopenRefused(s.owningDelegateID) {
		return "", nil
	}
	s.attentionMu.Lock()
	latched := s.attentionAcceptedID
	s.attentionMu.Unlock()
	if latched == "" {
		return "", nil
	}
	return s.reopenConsumedDelegateAttention(latched, quietAt)
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

func (s *Session) pendingDelegateAttentionIDsLocked() ([]string, error) {
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil, err
	}
	return fold.pendingIDs(), nil
}

// delegateAttentionPendingIDsLocked is pendingDelegateAttentionIDsLocked
// without the error. A presentation read must not fail because the transcript
// could not be folded: the fold's answer only narrows which rows the admission
// gate may withhold, and a nil set leaves that gate exactly as strict as the
// wake set alone, which is the stricter of the two. The caller holds attentionMu.
func (s *Session) delegateAttentionPendingIDsLocked() []string {
	ids, err := s.pendingDelegateAttentionIDsLocked()
	if err != nil {
		return nil
	}
	if ids == nil {
		// A fold that ran and found nothing is reported as an empty, non-nil
		// slice, so the caller's set is non-nil and the gate withholds nothing.
		return []string{}
	}
	return ids
}

// delegateAttentionHeldFromAutomaticDriveLocked reports whether a user's
// stop quieted the row from automatic drives. The settle tail's hold write
// and the drive-side reads share this one predicate. Callers must hold
// attentionMu.
func (s *Session) delegateAttentionHeldFromAutomaticDriveLocked(attentionID string) bool {
	_, held := s.attentionAutomaticDriveHolds[attentionID]
	return held
}

// delegateAttentionRowIsPresentableLocked answers the one question every
// presentation path asks about a pending row: may a request carry it now?
//
// There are exactly two reasons a pending row is withheld, and they must not
// be confused with each other:
//
//   - Nothing has admitted it yet. A stable watch frame is written to a cold
//     receiver's transcript by the watch rail, which holds no controller, and
//     a reopened row is minted by a stop; neither is an arm. A row of this
//     kind presents when its own wake passes the gate — an admission notes it,
//     or a release makes it eligible — and until then it must stay out of both
//     selection and the request, because a generation that presented and
//     consumed it would spend the row on a request that never showed it.
//     own generation is not yet superseded by a release, so the quiet stands
//     and the row waits for a newer event. That is the quiet #5713 protects.
//
// A row is presentable when neither applies: it is an accepted trigger, an
// admitted wake or rider, a released row, or an ordinary pending row whose
// identity no stop and no admission gate owns. Callers must hold attentionMu,
// because the answer is made of this session's holds and the controller's
// admission state, and a reader that took them separately would answer about a
// moment the request is not showing.
func (s *Session) delegateAttentionRowIsPresentableLocked(attentionID string, pendingIDs map[string]struct{}) bool {
	if attentionID == "" {
		return true
	}
	if s.delegateAttentionHeldFromAutomaticDriveLocked(attentionID) {
		return false
	}
	return !s.delegateAttentionAwaitingAdmissionLocked(attentionID, pendingIDs)
}

// delegateAttentionAwaitingAdmissionLocked reports whether attentionID is an
// original stable watch frame whose wake no admission has reached.
//
// The question is asked only about a row the transcript still holds PENDING,
// because that is the only state in which "no admission has reached it" is a
// statement about the row rather than about history. Admission is a fact that
// happened; the controller's wake set reports only what is still owed. A row
// that was admitted and then consumed — the trigger acceptance resolves before
// its generation runs — leaves the wake set, and so does a frame an earlier
// request already presented. Reading absence from that set as "never admitted"
// would answer a monotonic question from evidence that shrinks, and the
// projection would then drop the very turn the generation accepted: the run
// settles having consumed a report no request carried. Pending is the monotonic
// witness: a row stays pending until something admits it, and once it is resolved
// it is history whose order the release path already owns.
//
// The answer is derived from the controller's own admission state rather than
// recorded on the row: the restore that finds the frame and the arm that admits
// it both run against the controller, so a session-side note would be a second
// source of truth that a restart, a reclaim, or an arm landing in between could
// leave stale. A reopened row is not an original frame, so this never covers a
// stop's own quiet — that is the hold's job. Callers must hold attentionMu.
//
// pendingIDs is the set of rows the caller's fold found unresolved. A nil set
// means the caller did not fold the transcript, and the answer falls back to the
// wake set alone, which is the pre-existing behaviour for callers that only ask
// about rows they know are pending.
func (s *Session) delegateAttentionAwaitingAdmissionLocked(attentionID string, pendingIDs map[string]struct{}) bool {
	if !isOriginalStableWatchFrame(attentionID) {
		return false
	}
	if pendingIDs != nil {
		if _, pending := pendingIDs[attentionID]; !pending {
			return false
		}
	}
	controller := s.delegateController
	if controller == nil || s.owningDelegateID == "" {
		return false
	}
	admitted := controller.admittedAttentionRows(s.owningDelegateID)
	if _, known := admitted[attentionID]; known {
		return false
	}
	return true
}

// delegateAttentionPendingSet indexes the rows a fold found unresolved, so the
// presentation gates can ask "is this row still pending" without re-reading the
// transcript. The set answers the monotonic half of the admission question: a
// row is in it exactly while nothing has admitted or presented it, which is the
// only window in which the controller's shrinking wake set is evidence about
// the row itself.
func delegateAttentionPendingSet(ids []string) map[string]struct{} {
	// An empty fold is an ANSWER, not a missing one: it says the transcript holds
	// no pending row, which is exactly what a generation that just consumed its
	// sole pending row produces. Returning nil here would make that answer
	// indistinguishable from a fold that could not run, and the admission gate
	// treats nil as "unknown, fall back to the wake set" — which drops the very
	// trigger acceptance just consumed. Only a caller that could not read the
	// transcript passes nil.
	//
	// The other direction costs more. A fold that could not run must stay nil:
	// turning it into an authoritative empty set tells the gate that no row is
	// pending, which the gate reads as "not awaiting admission" and admits a
	// frame whose wake nothing confirmed. That would present a row on an
	// unconfirmed read, so the unavailable answer keeps its own identity here.
	if ids == nil {
		return nil
	}
	pending := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		pending[id] = struct{}{}
	}
	return pending
}

// delegateAttentionAwaitingAdmissionStamp is the value the projection's
// withheld set carries for a row withheld because nothing has admitted it yet.
// It is not a generation and is never compared as one: the set answers
// membership only, and the projection's filter drops by presence. A stop's hold
// carries its own settled generation instead, which the release path does
// compare.
const delegateAttentionAwaitingAdmissionStamp = ^uint64(0)

// delegateAttentionHoldStands reports whether a hold stamped with held still
// outranks the newest wake admitted at admitted. A hold stands exactly while
// it is strictly newer than every admitted wake: at equal generations the wake
// supersedes the quiet the hold carries.
func delegateAttentionHoldStands(held, admitted uint64) bool {
	return held > admitted
}

// reconcileAutomaticDriveHoldsLocked ends every hold the delegate journal's
// release floor already superseded. Presentation is the one point that cannot
// lose the race against a release: a wake journals its release before it
// publishes anything, so a release that matters has always landed by the time
// a row is selected or projected, while an install or a clear has to predict a
// release that may still be in flight. The reads therefore reconcile instead
// of trusting what the settles and wakes left in the map. A hold this keeps is
// one no admitted wake outranked, so the stop that installed it is still the
// newest evidence and its row stays quiet.
//
// Ending a hold here is the same event as ending it at the release, so it
// performs the same history-tail move first: the wake journaled its release
// before this read, and its release callback has not run yet — it will find
// no hold left to move. Reconciling without the move would leave the report
// buried under the exchanges of the generation the hold stood through, and
// the request that first presents it would end on a later assistant turn
// instead of on the report. Whichever path ends the quiet, the move happens
// exactly once, before the hold's entry is deleted and before anything reads
// the history it orders. Callers must hold attentionMu.
func (s *Session) reconcileAutomaticDriveHoldsLocked() {
	if len(s.attentionAutomaticDriveHolds) == 0 {
		return
	}
	controller := s.delegateController
	if controller == nil || s.owningDelegateID == "" {
		return
	}
	controller.mu.Lock()
	floor := uint64(0)
	if aggregate := controller.durable[s.owningDelegateID]; aggregate != nil {
		floor = aggregate.AttentionHoldReleasedGeneration
	}
	// An admitted wake supersedes a settled stop even when the journal refused
	// its release record, which it does for the whole of an open run. The
	// controller keeps that admission, so a request captured between the wake
	// and the release callback still reads the quiet as ended instead of
	// presenting the fresh row while filtering out the report the wake freed.
	if state := controller.attention[s.owningDelegateID]; state != nil &&
		state.releasedGeneration > floor {
		floor = state.releasedGeneration
	}
	controller.mu.Unlock()
	var ended []string
	for id, held := range s.attentionAutomaticDriveHolds {
		if !delegateAttentionHoldStands(held, floor) {
			ended = append(ended, id)
		}
	}
	if len(ended) == 0 {
		return
	}
	sort.Strings(ended)
	// The released rows move to the tail, so any admitted frame that is not in
	// history yet is restored first: a frame appended afterwards would land
	// behind the report and become the last message, reversing the order the same
	// wake produces when its runtime is resident. Restoring here covers a cold
	// arm that committed while no runtime existed to retain on.
	s.retainAdmittedPendingTurnsLocked()
	s.moveHeldDelegateAttentionTurnsToTail(ended)
	for _, id := range ended {
		delete(s.attentionAutomaticDriveHolds, id)
	}
}

// retainAdmittedPendingTurnsLocked puts every admitted wake's pending turn into
// the resident history. It runs before a release moves a free'd report to the
// tail, because a frame that is not resident when the move happens lands behind
// that report afterwards and the next request ends on the frame instead of the
// report the release delivered. Rows a stop still holds are skipped: they are
// not presentable yet, and their turn's position belongs to the release that
// will eventually move it. The retain is ID-keyed, so a resident turn keeps the
// position it already has. Callers hold attentionMu.
func (s *Session) retainAdmittedPendingTurnsLocked() {
	controller := s.delegateController
	if controller == nil || s.owningDelegateID == "" {
		return
	}
	admitted := controller.admittedAttentionRows(s.owningDelegateID)
	if len(admitted) == 0 {
		return
	}
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return
	}
	for _, attentionID := range fold.order {
		if _, known := admitted[attentionID]; !known {
			continue
		}
		if _, resolved := fold.resolutions[attentionID]; resolved {
			continue
		}
		if turn, ok := fold.turns[attentionID]; ok {
			// A row already resident keeps its position; the retain only
			// appends what history is missing.
			_ = s.retainDelegateAttentionTurn(turn)
		}
	}
}

// clearDelegateAttentionHold ends a hold whose quiet the derivation's own
// evidence says a newer event already superseded, performing the release path's
// history-tail move first so the report is not left at the position the quiet
// put it in. Ending a quiet is one event whichever path notices it, and the
// move is what makes the next request end on the report rather than on a later
// assistant turn. The row's own arm presents it from there.
func (s *Session) clearDelegateAttentionHold(attentionID string) {
	if s == nil || attentionID == "" {
		return
	}
	s.attentionMu.Lock()
	if _, held := s.attentionAutomaticDriveHolds[attentionID]; !held {
		s.attentionMu.Unlock()
		return
	}
	s.moveHeldDelegateAttentionTurnsToTail([]string{attentionID})
	delete(s.attentionAutomaticDriveHolds, attentionID)
	s.attentionMu.Unlock()
}

// rearmDelegateAttentionHoldsFromWakeSet ends every automatic-drive hold whose
// row the controller has since noted a wake for. The derivation installs a hold
// for a row its generation says a stop quieted, and it decides from the noted set
// it can see. A daemon restart breaks that: bootstrap rebuilds an idle ancestor
// to recover a descendant's owed start — which derives the ancestor's quiet — and
// only then reconciles the transcripts, which is what notes the pending reports'
// wakes. The note is the restart's re-arm. Nothing journals a release for it,
// because the run it supersedes never settled, so presentation's reconcile
// against release generations can never end the hold: the ancestor keeps
// suppressing the very report the restart re-armed.
//
// This is the pass that can see the note, and it is idempotent by construction —
// it ends only holds the wake set already supersedes, so a second call with the
// same evidence does nothing. Bootstrap calls it once, after reconciliation and
// before any owed work launches. It reads the note rather than predicting it,
// which is what keeps an ordinary same-process reclaim — where the noted set is
// the live one and the hold is the stop's current quiet — deciding exactly as
// the derivation did.
func (s *Session) rearmDelegateAttentionHoldsFromWakeSet() {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return
	}
	s.attentionMu.Lock()
	if len(s.attentionAutomaticDriveHolds) == 0 {
		s.attentionMu.Unlock()
		return
	}
	controller := s.delegateController
	controller.mu.Lock()
	var noted map[string]struct{}
	if state := controller.attention[s.owningDelegateID]; state != nil {
		noted = maps.Clone(state.wakeIDs)
	}
	controller.mu.Unlock()
	var rearmed []string
	for attentionID := range s.attentionAutomaticDriveHolds {
		if _, wakeNoted := noted[attentionID]; wakeNoted {
			rearmed = append(rearmed, attentionID)
		}
	}
	s.attentionMu.Unlock()
	sort.Strings(rearmed)
	for _, attentionID := range rearmed {
		s.clearDelegateAttentionHold(attentionID)
	}
}

// holdDelegateAttentionFromAutomaticDriveAt quiets a reopened row a user's
// stop owns: the stopped generation's commit replaced the controller's wake
// with the pending rows it excluded, so the stale armed wake still drives
// for them once the announce releases — the tail's skipped re-arm disarms
// nothing — and without the hold the reopened report would ride the next
// generation that wake starts, the immediate restart the stop exists to
// prevent. The hold carries the settled generation whose quiet it stands for,
// because that stamp is what decides whether a later wake supersedes it.
func (s *Session) holdDelegateAttentionFromAutomaticDriveAt(attentionID string, generation uint64) {
	if s == nil || attentionID == "" {
		return
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if s.attentionAutomaticDriveHolds == nil {
		s.attentionAutomaticDriveHolds = make(map[string]uint64)
	}
	s.attentionAutomaticDriveHolds[attentionID] = generation
}

// delegateHoldReleaseSite names the wake a hold release serves.
type delegateHoldReleaseSite string

const (
	// delegateHoldReleaseDelivery tags the release a committed child
	// delivery fires.
	delegateHoldReleaseDelivery delegateHoldReleaseSite = "delivery"
	// delegateHoldReleaseArm tags the release a successful arm fires.
	delegateHoldReleaseArm delegateHoldReleaseSite = "arm"
)

// releaseAutomaticDriveHolds ends the user-stop holds a new event's wake
// supersedes: a committed child delivery or a successful arm may present a
// row the stop held out of the automatic drives, so the holds end with the
// event that arrived — the site names which wake, and admittedGeneration is
// the settled generation that wake superseded, sampled where the wake was
// admitted.
//
// The invariant is that a hold stands exactly while it is newer than every
// wake admitted so far. A hold stamped above admittedGeneration belongs to a
// stop that settled after this wake was admitted, so it survives: an arm that
// was already in flight when the stop landed cannot clear the quiet that stop
// just installed. A hold at or below it names a generation this wake is newer
// evidence than, so it ends, and its row becomes presentable. The rows the
// stop never touched keep their presentation either way, and a failed arm or
// an uncommitted delivery woke nothing, so the holds stand.
//
// This filter is not the reconcile's duplicate: the release is the only place
// that can act on a wake younger than a hold, and dropping is destructive. A
// presentation read prunes holds the journal already outranked, but it cannot
// restore one this filter wrongly deleted, so the filter must still refuse to
// delete a hold the wake did not supersede.
//
// The release is durable before it is in-memory: the record names the settled
// generation whose quiet this event superseded, so a runtime the idle release
// later reclaims cannot have the derive rebuild a quiet this release already
// ended. A record that cannot land releases no in-memory hold either: the
// wake never published, so the held generation still outranks it and the
// quiet stands on the resident runtime and on the next restore alike.
func (s *Session) releaseAutomaticDriveHolds(site delegateHoldReleaseSite, admittedGeneration uint64) {
	// The seam is runtime-mutable through updateSessionTestConfig, so its
	// read shares the setter's session mutex — the convention every
	// mid-run-mutable test seam follows.
	s.mu.Lock()
	hook := s.cfg.testOnly.delegateAttentionHoldReleased
	s.mu.Unlock()
	var released []string
	s.attentionMu.Lock()
	for id, held := range s.attentionAutomaticDriveHolds {
		if delegateAttentionHoldStands(held, admittedGeneration) {
			continue
		}
		released = append(released, id)
	}
	sort.Strings(released)
	if len(released) != 0 {
		s.moveHeldDelegateAttentionTurnsToTail(released)
		for _, id := range released {
			delete(s.attentionAutomaticDriveHolds, id)
		}
	}
	s.attentionMu.Unlock()
	if hook != nil && released != nil {
		hook(site, released)
	}
}

// moveHeldDelegateAttentionTurnsToTail moves each still-pending held row's
// steering turn to the resident history's tail. The reopen appended the turn
// where the settle left it, and the exchanges of any generation that ran while
// the hold stood append after it, so the generation that later presents the
// row would end its request on those later exchanges — a trailing assistant
// message the model treats as a prefill to continue instead of the report it
// owes. The move puts the report back at the end, so the presenting request
// ends on it. Resolved rows keep their presented positions. The moves follow
// the fold's arrival order, so rows held by back-to-back stops re-append
// deterministically and the presenting request shows them in the order they
// arrived. Only the rows in released move: a hold a newer stop installed
// outlives this wake, so its turn keeps the position the holding stop left it
// in. The caller holds attentionMu.
func (s *Session) moveHeldDelegateAttentionTurnsToTail(released []string) {
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		// Best-effort: the rows keep their positions, and the release still
		// ends the quiet.
		return
	}
	pending := fold.pendingIDs()
	releasable := make(map[string]struct{}, len(released))
	for _, id := range released {
		releasable[id] = struct{}{}
	}
	for _, attentionID := range pending {
		if _, ok := releasable[attentionID]; !ok {
			continue
		}
		var held schema.Turn
		s.mu.Lock()
		for index := range s.history {
			if s.history[index].AttentionID == attentionID && s.history[index].Kind == schema.TurnSteering {
				held = s.history[index]
				break
			}
		}
		s.mu.Unlock()
		if held.AttentionID == "" {
			continue
		}
		s.dropResidentDelegateAttentionTurnLocked(attentionID)
		if err := s.retainDelegateAttentionTurn(held); err != nil {
			s.emit(warningDataFromError("move held delegate attention turn to the history tail", err))
		}
	}
}

// retainPendingDelegateAttentionTurnsLocked restores every still-pending row's
// durable turn into the history. The point of the restore is that a row's turn
// and its pending status come from one fold: a request that presents a row must
// carry its turn, and a row whose turn predates the resumed history's last
// compaction marker would otherwise be pending with nothing to present.
// Restoration is ID-keyed and idempotent, so a turn the resumed history already
// holds is replaced in place rather than duplicated. Every restore path calls
// this before it attaches the runtime: an explicit send or an automatic arm
// that attached first would build its request from a history that never got
// them, and the rows would wait for a generation that may never come. The
// caller holds attentionMu.
func (s *Session) retainPendingDelegateAttentionTurnsLocked(fold delegateAttentionFold) error {
	return s.retainPendingDelegateAttentionTurnsExceptLocked(fold, nil)
}

// retainPendingDelegateAttentionTurnsExceptLocked is
// retainPendingDelegateAttentionTurnsLocked without the rows named in skip.
// A caller uses it for a row whose wake admission did not complete: publishing
// that row's turn would make a frame visible before the gate that admits it,
// and the successor that presented it would consume it while the wake still
// owed a release. The row keeps its durable turn and its arm publishes it once
// its own gate passes. The caller holds attentionMu.
func (s *Session) retainPendingDelegateAttentionTurnsExceptLocked(fold delegateAttentionFold, skip []string) error {
	pendingTurns := fold.pendingTurns("")
	if len(skip) != 0 {
		kept := make([]schema.Turn, 0, len(pendingTurns))
		for _, durable := range pendingTurns {
			if durable.AttentionID != "" && slices.Contains(skip, durable.AttentionID) {
				continue
			}
			kept = append(kept, durable)
		}
		pendingTurns = kept
	}
	for i, durable := range pendingTurns {
		if err := s.retainDelegateAttentionTurn(durable, pendingTurns[i+1:]...); err != nil {
			return err
		}
	}
	return nil
}

// admitPendingWatchFramesLocked admits the wake of every stable watch frame
// the receiver's transcript already holds and no admission has reached, and
// returns the rows it could not admit so the caller keeps their turns out of
// the history it is about to publish.
//
// A stable watch frame is appended to a cold receiver's transcript by the
// watch rail, which holds no controller: the row is durable before any arm of
// its own runs. A restore that folded that transcript and published every
// pending turn would therefore make the frame visible while its wake — and the
// release that wake owes a settled stop — had not been journaled. An older
// wake could then start a generation that presents and consumes the frame
// while omitting the stopped report the frame's own wake releases, and the
// delayed arm would find the frame resolved and skip its release.
//
// Admission here is what removes that window instead of classifying the rows
// that fall in it: by the time anything is visible, the frame's wake has
// passed its gate and its release is in the journal, so no reader has to tell
// an unadmitted frame from an ordinary pending row. The caller holds
// attentionMu.
func (s *Session) admitPendingWatchFramesLocked(fold delegateAttentionFold) ([]string, []delegateUpdatePlan) {
	controller := s.delegateController
	admitted := controller.admittedAttentionRows(s.owningDelegateID)
	var deferred []string
	var plans []delegateUpdatePlan
	for _, attentionID := range fold.pendingIDs() {
		// Only an original frame is a row nothing has admitted yet. A reopen
		// minted from a frame is a stop's own row: its eligibility is decided
		// by its own generation against the release floor, never by admitting a
		// wake for it. Treating a reopen as a fresh frame would journal a
		// release for the very stop that reopened it, and the stopped work
		// would restart with no new event.
		if !isOriginalStableWatchFrame(attentionID) {
			continue
		}
		if _, known := admitted[attentionID]; known {
			continue
		}
		_, blocker, plan, emit, _, err := controller.tryOpenDelegateAttention(s.owningDelegateID, attentionID)
		if err != nil || blocker != nil {
			deferred = append(deferred, attentionID)
			continue
		}
		if emit {
			plans = append(plans, plan)
		}
	}
	return deferred, plans
}

// delegateQuietedRow is a pending reopened report together with the generation
// whose stop quieted it. A row carries its own generation: several rows can be
// unreleased at once, after successive stops, and the generation a later stop
// settled says nothing about an earlier row's quiet.
type delegateQuietedRow struct {
	attentionID string
	generation  uint64
}

// stopQuietedDelegateAttentionRowsLocked lists the pending rows a user stop
// quieted, each with its own generation. The stop that owns a reopened row is
// the generation its immediate source resumes at, and that generation must be
// one whose own run was attention-triggered and settled cancelled: the same
// evidence the resident quiet was built from, asked of the row rather than of
// the delegate's newest generation. Which of them a restore rebuilds a hold for
// is a separate question, answered per row against the release floor and the
// noted wake set. The caller holds attentionMu and passes the run index read
// under the controller lock with the aggregate.
func (s *Session) stopQuietedDelegateAttentionRowsLocked(fold delegateAttentionFold, generation uint64, runStarts map[delegateLease]delegatestore.RunTrigger, runOutcomes map[delegateLease]delegatestore.Outcome, delegateID string) []delegateQuietedRow {
	var rows []delegateQuietedRow
	for _, id := range fold.pendingIDs() {
		source, _, ok := cutDelegateAttentionReopenSegment(id)
		if !ok {
			continue
		}
		claimed := fold.resumeGenerations[source]
		if claimed == 0 || claimed > generation {
			continue
		}
		lease := delegateLease{delegateID: delegateID, generation: claimed}
		if runStarts[lease] != delegatestore.TriggerAttention {
			continue
		}
		if outcome, settled := runOutcomes[lease]; !settled || outcome.Status != delegatestore.OutcomeCancelled {
			continue
		}
		rows = append(rows, delegateQuietedRow{attentionID: id, generation: claimed})
	}
	return rows
}

// deriveHeldDelegateAttentionFromOutcome rebuilds, on a session reconstructed
// after an idle release, the user-stop quiet the released runtime held only in
// memory. The idle release is transparent reclaim, not a wake from a new
// event, so the restored session must mirror the resident quiet: the durable
// outcome of the stopped run and the reopened rows the settle left pending are
// enough to find the rows the stop owns. A pending row is held when it is a
// well-formed reopen whose immediate source's resume claim sits at the
// delegate's current durable generation — the generation the user-stopped run
// settled — and that generation's last run was attention-triggered and settled
// cancelled, and the controller does not already note the row's wake: the
// resident quiet holds an attention run's report alone, and a send that
// commits a recycled generation settles the same cancelled outcome without
// owing that quiet. A noted wake is a new event's arm (a restart reconcile or
// a fresh delivery), which presents the row exactly as it would have
// presented it against the resident hold. Every other reopen stays
// presentable on the restore: a busy reopen whose send settled the recycled
// generation matches the outcome and generation exactly, so the run's trigger
// — not a generation mismatch — is what leaves it presentable; refused-launch
// reopens key to a generation the journal never reached, and a subtree stop's
// rows keep their own sticky quiet. The derived hold writes the same map the
// settle tail wrote, so the arm that presents any remaining pending row
// releases it exactly as it releases the resident one.
//
// The derivation's inputs are unordered snapshots — the aggregate, the fold,
// and the in-memory wake set — so a cancelled outcome alone cannot say whether
// its quiet is still current. The invariant it must hold is that the derived
// hold reflects the newest authoritative evidence: the quiet stands only while
// no event newer than the stopped generation's settle ended it. The durable
// release record (delegatestore.AttentionHoldReleasedGeneration) supplies that
// order, and it is read from the same locked aggregate snapshot the outcome
// comes from, so a release that landed after a stop is visible to the derive
// even though the wake set it emptied did not survive the reclaim.
func (s *Session) deriveHeldDelegateAttentionFromOutcome() {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return
	}
	controller := s.delegateController
	// A stable watch frame is durable in this transcript before any arm of its
	// own runs, so the fold can hold one whose wake nothing has admitted yet.
	// Its wake is admitted here, before this pass reads the release floor and
	// before any turn is published. Two orderings depend on that:
	//
	//   - The floor this pass derives from must already include the release the
	//     frame's wake journals. Deriving first would rebuild the quiet for a
	//     stop that admission just superseded, and the held report would stay
	//     silent behind a frame that was admitted to release it.
	//   - The frame's turn must not be visible before the gate that admits it.
	//     A successor that presented it would consume it while the wake still
	//     owed that release, and the delayed arm would find the row resolved and
	//     skip it.
	//
	// Admission is therefore what removes this window rather than an
	// investigation of which rows fell in it: nothing is visible that has not
	// passed its gate. A row whose gate cannot pass yet keeps its turn out of
	// the history and its own arm publishes it.
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		s.attentionMu.Unlock()
		return
	}
	deferred, admissionPlans := s.admitPendingWatchFramesLocked(fold)
	controller.mu.Lock()
	generation := uint64(0)
	released := uint64(0)
	if aggregate := controller.durable[s.owningDelegateID]; aggregate != nil {
		generation = aggregate.Generation
		released = aggregate.AttentionHoldReleasedGeneration
	}
	// Each row's own generation decides its quiet, so the evidence is read per
	// generation here, in the same locked snapshot as the aggregate.
	runStarts := maps.Clone(controller.runStarts)
	runOutcomes := maps.Clone(controller.runOutcomes)
	var noted map[string]struct{}
	if state := controller.attention[s.owningDelegateID]; state != nil {
		noted = maps.Clone(state.wakeIDs)
	}
	controller.mu.Unlock()
	// The resumed history anchors on the last compaction marker, so a still
	// pending row recorded before that marker has no turn in the history. Every
	// pending row is restored here, before automatic selection can consume one:
	// a row the drive accepts without its turn is consumed durably by a request
	// that never carried the report, and the user is left with a silent
	// attention. The restore is ID-keyed and idempotent, so a turn the resumed
	// history already holds is replaced in place rather than duplicated.
	if err := s.retainPendingDelegateAttentionTurnsExceptLocked(fold, deferred); err != nil {
		s.emit(warningDataFromError("restore a pending delegate attention turn", err))
	}
	// Only a stop's own rows are in question here. Every other restored session
	// presents its pending rows exactly as the resident one would, and has no
	// quiet to re-impose or end.
	if generation == 0 {
		s.attentionMu.Unlock()
		for _, plan := range admissionPlans {
			controller.emitDelegateUpdate(plan)
		}
		return
	}
	quieted := s.stopQuietedDelegateAttentionRowsLocked(fold, generation, runStarts, runOutcomes, s.owningDelegateID)
	// Each row's own generation decides its quiet, because several rows can be
	// unreleased at once after successive stops and one generation's answer is
	// not another's. A release at or past the row's generation is newer evidence
	// than the stop that quieted it, so that quiet must not come back: the row
	// presents, and presenting it means ending the request on it, which is what
	// the release path's tail move exists for. A row whose generation the floor
	// has not reached keeps its quiet. A wake the controller already notes is a
	// new event's arm that presents the row on its own, so it needs no hold —
	// but that question is asked only of rows whose quiet still stands, never of
	// rows a release freed, because a restart leaves both the freed report and
	// the fresh frame noted and the report still has to move.
	var releasedRows, heldRows, rearmedRows []string
	for _, row := range quieted {
		if delegateAttentionHoldStands(row.generation, released) {
			if _, wakeNoted := noted[row.attentionID]; wakeNoted {
				// A noted wake is a new event's arm, and that arm presents the
				// row on its own — so this row needs no hold. A hold found here
				// was installed by an earlier pass of this same derivation, made
				// before the controller had noted the wake: a daemon restart
				// rebuilds ancestors for an owed start and derives their quiet
				// before bootstrap reconciles the transcript's wakes. The pass
				// that can see the wake must therefore also undo that hold, or
				// the ancestor keeps suppressing the very report the restart
				// re-armed. The evidence is read here, not predicted, so an
				// ordinary same-process reclaim — where the noted set is the
				// live one — decides exactly as it did before.
				rearmedRows = append(rearmedRows, row.attentionID)
				continue
			}
			heldRows = append(heldRows, row.attentionID)
			continue
		}
		releasedRows = append(releasedRows, row.attentionID)
	}
	if len(releasedRows) != 0 {
		s.moveHeldDelegateAttentionTurnsToTail(releasedRows)
	}
	s.attentionMu.Unlock()
	// The admissions above published their wake rows under the hold that also
	// ordered the retention of their turns, so the emit is delivered once that
	// hold is released, as the resident arm delivers its own.
	for _, plan := range admissionPlans {
		controller.emitDelegateUpdate(plan)
	}
	for _, attentionID := range rearmedRows {
		s.clearDelegateAttentionHold(attentionID)
	}
	// The stamp is each row's own generation — the one its stop settled and its
	// reopen claims — so a wake admitted at a later generation ends that hold and
	// one admitted before it never does.
	if len(heldRows) != 0 {
		s.mu.Lock()
		beforeInstall := s.cfg.testOnly.delegateAttentionBeforeDerivedHoldInstall
		s.mu.Unlock()
		if beforeInstall != nil {
			beforeInstall()
		}
	}
	for _, row := range quieted {
		if !slices.Contains(heldRows, row.attentionID) {
			continue
		}
		s.holdDelegateAttentionFromAutomaticDriveAt(row.attentionID, row.generation)
	}
}

// automaticDelegateAttentionIDs lists the pending rows an automatic drive may
// present: every pending row except those a user's stop holds. The drive's
// selection and the accept's rider capture both read this list, so a held row
// neither claims a generation nor rides another row's, while every other
// pending row still presents on the same wake.
func (s *Session) automaticDelegateAttentionIDs() ([]string, error) {
	pending, err := s.pendingDelegateAttentionIDs()
	if err != nil {
		return nil, err
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	s.reconcileAutomaticDriveHoldsLocked()
	// pending is the same list this selection started from, so a row it offers is
	// by construction one the transcript still holds pending: the admission gate
	// below is asked about a pending row and never about history.
	pendingSet := delegateAttentionPendingSet(pending)
	kept := make([]string, 0, len(pending))
	for _, id := range pending {
		if !s.delegateAttentionRowIsPresentableLocked(id, pendingSet) {
			continue
		}
		kept = append(kept, id)
	}
	return kept, nil
}

// filterHeldDelegateAttentionTurns applies an already-captured hold map to an
// already-captured turn list. Splitting the filter out lets a caller take the
// turns and the holds under one lock and then answer from that pair alone: a
// filter that re-read the holds map after the turns were copied would answer a
// question about a different moment, and a release in between would leave it
// keeping a report at a position the release's tail move had abandoned.
// Presence decides the drop because the capture already reconciled every hold
// the journal superseded.
func filterHeldDelegateAttentionTurns(turns []schema.Turn, holds map[string]uint64) (kept []schema.Turn, droppedAt []int) {
	if len(holds) == 0 {
		return turns, nil
	}
	kept = make([]schema.Turn, 0, len(turns))
	for i, turn := range turns {
		if turn.AttentionID != "" {
			if _, held := holds[turn.AttentionID]; held {
				droppedAt = append(droppedAt, i)
				continue
			}
		}
		kept = append(kept, turn)
	}
	return kept, droppedAt
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
	return fold.pendingTurns(s.attentionAcceptedID), nil
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
	// The accept resolves the trigger before its generation runs, and the
	// resolution drops the controller's copy of its cause; the latch keeps the
	// cause for the request that presents the trigger. A re-hold of the same
	// trigger (a commit whose journal append failed and retried) finds the
	// controller's copy already dropped, so it keeps the cause the first hold
	// latched instead of overwriting it with none.
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
	for i, turn := range turns {
		if err := s.retainDelegateAttentionTurn(turn, turns[i+1:]...); err != nil {
			return err
		}
	}
	return nil
}

// acceptDelegateAttention records the exact reserved generation in the child
// transcript before asking the controller to publish that generation. The
// reservation remains the per-child claim across both durability boundaries;
// a journal append failure can therefore replay this same marker and batch.
func (s *Session) acceptDelegateAttention(reservation *delegateStartReservation) error {
	if s == nil || s.delegateController == nil {
		return errDelegateStaleLease
	}
	attentionID, generation, err := s.delegateController.attentionReservationIdentity(reservation, s)
	if err != nil {
		return err
	}
	// The latch goes up before the consumed marker can land: a fold that sees
	// the item resolved must already find it latched. It stays up when the
	// resolution fails, since a failed verification read can follow a marker
	// that did land, and the retry needs the trigger's turn.
	s.holdAcceptedTrigger(attentionID)
	// The turn goes into history before the marker can land. A cold restore
	// folds the pending rows once, and a watch frame appended after that fold is
	// armed by ID alone, so the drive can select a row whose turn no request
	// will carry: the marker lands, the row reads delivered, and the model never
	// saw it. Restoring first orders the two, so a crash between them leaves the
	// row pending with its turn present rather than consumed with no trace.
	if err := s.retainSelectedDelegateAttentionTurn(attentionID, reservation.delegateID); err != nil {
		return err
	}
	if err := s.resolveAttentionDurablyForGeneration([]string{attentionID}, delegateAttentionConsumed, generation); err != nil {
		return err
	}
	pendingIDs, err := s.automaticDelegateAttentionIDs()
	if err != nil {
		return err
	}
	// The generation carries the trigger and the rows whose wakes were already
	// admitted: those are the rows it can present, and the ones whose release
	// this wake's admission covers. A frame that has landed in the transcript
	// but whose arm has not passed its gate yet is not one of them — capture it
	// here and the successor presents it, resolving the row, so the handoff arm
	// that would have released the older report's quiet finds nothing pending.
	// That frame's own arm admits its wake and its release together.
	// The reservation names the delegate this generation runs for: it is the
	// controller's own record, resolved from the runtime it was reserved
	// against. The session's owningDelegateID mirror is not a substitute here:
	// an unresolved mirror would make the admitted set empty, the capture empty,
	// and the committed batch would then lower NeedsAttention for rows the wake
	// never touched.
	admitted := s.delegateController.admittedAttentionRows(reservation.delegateID)
	// A released row is eligible evidence even though no wake carries it. A
	// reopening stop mints its report under /reopened/<generation> and never
	// arms it, so the wake set alone reads it as a frame no admission reached
	// and the capture would drop it from the generation that must present it.
	// With the row dropped, CommitStart computes attentionNeeded false for a
	// delegate that has pending attention, appends NeedsAttention:false beside
	// RunStarted, and replaces the wake set with the empty capture: a stop
	// landing before presentation then has nothing to rearm and the released
	// report waits for an unrelated event. The release floor is the fact that
	// separates the two cases — a row it has reached was made presentable by a
	// release, while a fresh watch frame it has not reached is still waiting
	// for its own arm.
	released := s.delegateController.attentionReleaseFloor(reservation.delegateID)
	captured := make([]string, 0, len(pendingIDs))
	for _, pendingID := range pendingIDs {
		if pendingID == attentionID {
			captured = append(captured, pendingID)
			continue
		}
		if _, known := admitted[pendingID]; known {
			captured = append(captured, pendingID)
			continue
		}
		// A reopen is eligible on its own evidence, and that evidence is not
		// only the release floor. A row reopened by a failed start claims a
		// generation that never began — no RunStarted was ever journaled for it
		// — so no stop settled it and none quieted it. Waiting for a release
		// that no event owes would drop the row from the successor's owed set,
		// and the commit would then clear NeedsAttention and the wake set while
		// the report is still pending, leaving it for unrelated work to wake.
		// A row reopened by a stop's own settle does claim a generation that
		// began, and that row still waits for the release that supersedes the
		// stop: the same floor, asked of the same generation.
		if _, generation, ok := cutDelegateAttentionReopenSegment(pendingID); ok {
			// A generation an explicit send reused is the successor's, not the
			// send's: the send wrote RunStarted for it and its own commit cleared
			// the wake set, so the report has nowhere else to be presented from.
			// Carrying it here is the same eligibility decision automatic
			// selection makes — the quiet still stands only while no release has
			// reached the row's generation, and a send-reused generation is never
			// a stop's own quiet, so there is nothing to wait for.
			sendReused := s.delegateAttentionGenerationTriggerLocked(reservation.delegateID, generation) == delegatestore.TriggerOwnerInput
			if released >= generation || sendReused || !s.delegateAttentionGenerationStartedLocked(reservation.delegateID, generation) {
				captured = append(captured, pendingID)
			}
		}
	}
	if err := s.delegateController.prepareAttentionStart(reservation, s, captured); err != nil {
		return err
	}
	// The riders this generation captured have their IDs published above; their
	// turns are published here, in the same act, so the request that carries the
	// IDs carries the turns. A frame the capture did not take is left out of
	// both until its own arm admits its wake, which is what keeps a just-appended
	// watch frame from riding a generation whose arm never passed its gate.
	if err := s.retainCapturedDelegateAttentionTurns(captured); err != nil {
		return err
	}
	return nil
}

// delegateAttentionGenerationStartedLocked reports whether the reservation's
// delegate ever journaled a RunStarted for generation. A reopen whose claimed
// generation never began was minted by a failed start, not by a stop's settle,
// so nothing quieted it and no release is owed for it — its eligibility is its
// own. A generation that did begin and was stopped is a different row, and that
// one waits for a release.
func (s *Session) delegateAttentionGenerationStartedLocked(delegateID string, generation uint64) bool {
	if delegateID == "" {
		return false
	}
	if generation == 0 {
		return false
	}
	controller := s.delegateController
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	_, started := controller.runStarts[delegateLease{delegateID: delegateID, generation: generation}]
	return started
}

// delegateAttentionGenerationTriggerLocked returns the trigger that began
// generation, or the empty trigger when no RunStarted was journaled for it. A
// generation an explicit send reused reports TriggerOwnerInput, which is what
// distinguishes it from an attention run's own generation.
func (s *Session) delegateAttentionGenerationTriggerLocked(delegateID string, generation uint64) delegatestore.RunTrigger {
	if delegateID == "" || generation == 0 {
		return ""
	}
	controller := s.delegateController
	if controller == nil {
		return ""
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.runStarts[delegateLease{delegateID: delegateID, generation: generation}]
}

// retainCapturedDelegateAttentionTurns puts every captured row's turn into the
// resident history. The capture published these IDs as the rows this generation
// carries, so their turns belong in the request it builds. The retain is
// ID-keyed, so a row the attach already restored keeps its position.
func (s *Session) retainCapturedDelegateAttentionTurns(attentionIDs []string) error {
	if s == nil || len(attentionIDs) == 0 {
		return nil
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return err
	}
	for _, attentionID := range attentionIDs {
		turn, ok := fold.turns[attentionID]
		if !ok {
			continue
		}
		if err := s.retainDelegateAttentionTurn(turn); err != nil {
			return err
		}
	}
	return nil
}

// retainAdmittedDelegateAttentionTurn puts one admitted wake's turn into the
// resident history if the history does not hold it yet. A cold arm can admit a
// frame the runtime's attach never folded, and the release that follows moves a
// superseded stop's report to the tail: the frame has to be resident before that
// move, or it lands behind the report the wake released and the next request
// ends on the frame instead of the report. The retain is ID-keyed, so a turn the
// attach already restored keeps the position it has.
func (s *Session) retainAdmittedDelegateAttentionTurn(attentionID string) error {
	if s == nil || attentionID == "" {
		return nil
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return err
	}
	turn, ok := fold.turns[attentionID]
	if !ok {
		return nil
	}
	// A row this fold already resolved is not this arm's to publish. A cold arm
	// reads the transcript outside the controller lock, so a concurrent drive can
	// attach the receiver and consume the frame before the arm's post-commit
	// callback runs; if the user stops that generation, finalization drops the
	// original turn and reopens the report under a held ID. The fold still holds
	// the resolved original, so restoring it here would put a consumed report back
	// into history, and a later request would present it while the reopened copy
	// stays quiet. A resolved row's turn belongs to whatever resolved it; the
	// accepted-trigger path restores those, not this one.
	if _, resolved := fold.resolutions[attentionID]; resolved {
		return nil
	}
	return s.retainDelegateAttentionTurn(turn)
}

// retainSelectedDelegateAttentionTurn puts attentionID's turn, and every other
// pending row's, into history from the fold that validates them, before anything
// can mark the selected one consumed. A row can reach automatic selection
// without ever passing the restore that puts pending turns into the resumed
// history: a watch frame appended after a cold restore folded the pending set is
// armed by ID alone, so its turn stays out of history and the drive captures it
// as a rider by ID. Selecting the trigger then consumes it durably against a
// request that never carried it, and the first request omits the frame. The
// trigger alone is not enough: the request this generation builds carries every
// row it captures, so every captured row's turn is restored here. The retain is
// ID-keyed, so a turn already resident keeps its position and a missing one is
// appended.
func (s *Session) retainSelectedDelegateAttentionTurn(attentionID, delegateID string) error {
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		s.attentionMu.Unlock()
		return err
	}
	defer s.attentionMu.Unlock()
	// Only rows this generation can carry are restored: the selected trigger and
	// the rows the controller has already admitted as a wake or captured as a
	// rider. A frame that has landed in the transcript with no admission behind
	// it belongs to no request yet — restoring it here would put it in this
	// generation's request ahead of the gate that admits its wake, so the
	// successor presents it and resolves the row, and the arm that would have
	// released the older report's quiet then finds nothing pending.
	known := s.delegateController.admittedAttentionRows(delegateID)
	pendingTurns := fold.pendingTurns("")
	for i, turn := range pendingTurns {
		if turn.AttentionID != attentionID {
			if _, admitted := known[turn.AttentionID]; !admitted {
				continue
			}
		}
		if err := s.retainDelegateAttentionTurn(turn, pendingTurns[i+1:]...); err != nil {
			// The selected row is the one this generation cannot run without;
			// another row's conflict is not a reason to consume a marker against
			// a request that omits the trigger.
			if turn.AttentionID == attentionID {
				return err
			}
		}
	}
	if _, ok := fold.turns[attentionID]; ok {
		return nil
	}
	// A row the fold does not know holds no turn; the marker still orders
	// against whatever the transcript has.
	return nil
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
// owes: the root runs its notification rail (finishRootDelegateAttentionTurn).
// Any other session consumes only what its settled requests presented
// (coversPresentedAttention), and only when the turn succeeded; a failed
// delegate turn leaves the items pending for a later generation, and the
// paced retry and the paused notice stay the root's.
func (s *Session) finishAttentionTurn(ids []string, turnErr error) error {
	if s.isRootDelegateAttentionReceiver() {
		return s.finishRootDelegateAttentionTurn(ids, turnErr)
	}
	var resolvedIDs []string
	defer s.settleFoldedCoveredAttention(&resolvedIDs)
	if turnErr != nil {
		return nil
	}
	covered := s.unionCoveredAttention(ids)
	if len(covered) == 0 {
		return nil
	}
	// The delegate's noted wakes stay; see clearResolvedDelegateAttention.
	if err := s.resolveAttentionDurably(covered, delegateAttentionConsumed); err != nil {
		return err
	}
	resolvedIDs = covered
	return nil
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
	for i, durable := range pendingTurns {
		if err := s.retainDelegateAttentionTurn(durable, pendingTurns[i+1:]...); err != nil {
			s.attentionMu.Unlock()
			return err
		}
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

// dropResidentDelegateAttentionTurnLocked removes attentionID's steering turn
// from the resident history only — the transcript row stays, so the fold still
// reads the row's content and resolution. The caller holds attentionMu. The
// turnHistoryBaseline shift mirrors removeUnverifiedDelegateAttentionTurn: a
// deletion strictly before the boundary moves the boundary with it.
func (s *Session) dropResidentDelegateAttentionTurnLocked(attentionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.history {
		if s.history[index].AttentionID != attentionID || s.history[index].Kind != schema.TurnSteering {
			continue
		}
		s.history = append(s.history[:index], s.history[index+1:]...)
		if index < s.turnHistoryBaseline {
			s.turnHistoryBaseline--
		}
		s.bumpHistoryRevisionLocked()
		return
	}
}

func (s *Session) readDelegateAttentionFold(path, sessionID string) (delegateAttentionFold, error) {
	if readFold := s.cfg.testOnly.delegateAttentionReadFold; readFold != nil {
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
