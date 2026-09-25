package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/foldcache"
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
	return func(_ context.Context, path string, _ int64, _ delegateAttentionFoldMemo) (delegateAttentionFoldMemo, int64, error) {
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
			return delegateAttentionFoldMemo{}, 0, err
		}
		// readDelegateAttentionFold is missing-as-empty for historical
		// callers; this boundary is strict, including a removal racing the
		// fold, so a transcript that vanished between the size stat above and
		// the fold's open errors here instead of serving (and caching) an
		// empty fold. The bypass in delegate_tree_attention.go carries the
		// same check for the sessions it serves directly.
		if _, err := os.Stat(path); err != nil {
			return delegateAttentionFoldMemo{}, 0, fmt.Errorf("stat delegate attention transcript after read: %w", err)
		}
		return delegateAttentionFoldMemo{sessionID: expectedSessionID, fold: fold}, consumed, nil
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
		if resolution.ResumeGeneration != 0 {
			for previousID, generation := range fold.resumeGenerations {
				if generation == resolution.ResumeGeneration && previousID != resolution.AttentionID {
					return delegateAttentionFold{}, fmt.Errorf("resume generation %d claims attention %q and %q", resolution.ResumeGeneration, previousID, resolution.AttentionID)
				}
			}
		}
		fold.resolutions[resolution.AttentionID] = disposition
		fold.resumeGenerations[resolution.AttentionID] = resolution.ResumeGeneration
	}
	return fold, nil
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
	for _, commit := range turn.DelegateDeliveryCommits {
		if commit.ToolCallID == "" || commit.DeliveryID == "" {
			return errors.New("delegate delivery commit identity is incomplete")
		}
		if _, exists := resultIDs[commit.ToolCallID]; !exists {
			return fmt.Errorf("delegate delivery %q references absent tool call %q", commit.DeliveryID, commit.ToolCallID)
		}
		if previous, exists := fold.deliveryCommits[commit.DeliveryID]; exists && previous != commit.ToolCallID {
			return fmt.Errorf("delegate delivery %q has conflicting tool calls", commit.DeliveryID)
		}
		for deliveryID, toolCallID := range fold.deliveryCommits {
			if toolCallID == commit.ToolCallID && deliveryID != commit.DeliveryID {
				return fmt.Errorf("delegate tool call %q has conflicting deliveries", commit.ToolCallID)
			}
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

func appendColdAttentionResolution(path, expectedSessionID string, ids []string, disposition delegateAttentionResolution) (err error) {
	return appendColdAttentionResolutionWithOpen(path, expectedSessionID, ids, disposition, transcript.OpenWriterForSession)
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
	preflight, err := readDelegateAttentionFold(path, expectedSessionID)
	if err != nil {
		return err
	}
	if err := validateDelegateAttentionResolutions(preflight, ids, disposition, resumeGeneration); err != nil {
		return err
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
	return appendDelegateAttentionResolutions(writer, transcript.PlaceDelivery, fold, ids, disposition, resumeGeneration)
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

// appendDelegateNotificationDurably appends one private, model-bound attention
// item. Replaying the same identity and content is a no-op; reusing an identity
// for different content is corruption.
func (s *Session) appendDelegateNotificationDurably(attentionID, content string) (appended bool, err error) {
	return s.appendDelegateAttentionMessageDurably(attentionID, llm.User(content))
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
	turn := schema.NewTurn(schema.TurnSteering, message)
	turn.Timestamp = s.sclock().Now().UTC()
	turn.AttentionID = attentionID
	turn.StableTurnID = newQueueEntryID()
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

func (s *Session) armDelegateAttention(attentionID string) error {
	if s == nil || attentionID == "" {
		return errors.New("delegate attention wake identity is incomplete")
	}
	err := s.armDelegateAttentionOnce(attentionID)
	s.attentionMu.Lock()
	if err != nil {
		if s.delegateAttentionArmIDs == nil {
			s.delegateAttentionArmIDs = make(map[string]struct{})
		}
		s.delegateAttentionArmIDs[attentionID] = struct{}{}
		s.scheduleDelegateAttentionArmRetryLocked()
	} else {
		delete(s.delegateAttentionArmIDs, attentionID)
		if len(s.delegateAttentionArmIDs) == 0 {
			s.resetDelegateAttentionArmRetryLocked()
		}
	}
	s.attentionMu.Unlock()
	return err
}

func (s *Session) armDelegateAttentionOnce(attentionID string) error {
	if s.isRootDelegateAttentionReceiver() {
		ids, err := s.pendingDelegateAttentionIDs()
		if err != nil {
			return err
		}
		if !slices.Contains(ids, attentionID) {
			return nil
		}
		s.armRootDelegateAttention(attentionID)
		return nil
	}
	if s.delegateController == nil || s.owningDelegateID == "" {
		return errors.New("delegate attention controller identity is incomplete")
	}
	for {
		s.attentionMu.Lock()
		ids, err := s.pendingDelegateAttentionIDsLocked()
		if err != nil {
			s.attentionMu.Unlock()
			return err
		}
		if !slices.Contains(ids, attentionID) {
			s.attentionMu.Unlock()
			return nil
		}
		_, blocker, plan, emit, err := s.delegateController.tryOpenDelegateAttention(s.owningDelegateID, attentionID)
		s.attentionMu.Unlock()
		if err != nil {
			return err
		}
		if blocker != nil {
			<-blocker
			continue
		}
		if emit {
			s.delegateController.emitDelegateUpdate(plan)
		}
		s.notify()
		return nil
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

func (s *Session) pendingDelegateAttentionIDsLocked() ([]string, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	ready := s.transcriptReady
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || sessionID == "" || stateDir == "" {
		return nil, nil
	}
	fold, err := s.readDelegateAttentionFold(transcriptPath(stateDir, sessionID), sessionID)
	if err != nil {
		return nil, err
	}
	return fold.pendingIDs(), nil
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
	if err := s.resolveAttentionDurablyForGeneration([]string{attentionID}, delegateAttentionConsumed, generation); err != nil {
		return err
	}
	pendingIDs, err := s.pendingDelegateAttentionIDs()
	if err != nil {
		return err
	}
	if err := s.delegateController.prepareAttentionStart(reservation, s, pendingIDs); err != nil {
		return err
	}
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

// armRootDelegateAttention caches one already-durable root attention ID and
// coalesces an autonomous notification wake. The cache is never authority: an
// append/readback succeeds before admission here, and restart replaces it from
// the receiver transcript fold.
func (s *Session) armRootDelegateAttention(attentionID string) {
	if attentionID == "" || !s.isRootDelegateAttentionReceiver() {
		return
	}
	s.attentionMu.Lock()
	if s.rootAttentionWakeIDs == nil {
		s.rootAttentionWakeIDs = make(map[string]struct{})
	}
	s.rootAttentionWakeIDs[attentionID] = struct{}{}
	shouldWake := !s.rootAttentionWake
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
	if s.rootAttentionRetry.active {
		s.rootAttentionRetry.generation++
		s.rootAttentionRetry.active = false
	}
	s.attentionMu.Unlock()
	sort.Strings(ids)
	return ids
}

// finishRootDelegateAttentionTurn consumes the exact selected IDs only after a
// successful model turn and durable resolution markers — plus any still-pending
// attention this turn's built requests already presented to the model, which
// needs no wake of its own. Failures keep the transcript-owned IDs pending and
// arrange a paced retry wake.
func (s *Session) finishRootDelegateAttentionTurn(ids []string, turnErr error) error {
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
		ids = s.unionCoveredRootDelegateAttention(ids)
	}
	if len(ids) == 0 {
		return nil
	}
	var resolutionErr error
	if turnErr == nil {
		err := s.resolveAttentionDurably(ids, delegateAttentionConsumed)
		if err == nil {
			s.attentionMu.Lock()
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
	s.attentionMu.Lock()
	// Clear the flag even while a wake is pending. This turn may have honored
	// that wake and then declined — its resolution failed — and the drain
	// skips the notification rung right after a notification turn, so the
	// flagged wake alone can strand the item. The retry owns the next wake.
	s.rootAttentionWake = false
	s.scheduleRootAttentionRetryLocked()
	s.attentionMu.Unlock()
	return resolutionErr
}

// stageRootDelegateAttentionCoverage records one built request's candidate
// coverage: the attention IDs the request presents that were not armed when
// this turn began. Staging is candidacy, not credit — the round loop promotes
// the staged set into rootAttentionCoveredIDs only when the round's call
// settles, because a request that never settled (a failed attempt, a
// content-filter retry whose compaction then folds the steering turn away)
// presented nothing the design may consume. Only the root receiver stages:
// this session marks only deliveries it owns, and a child's consumption is
// governed by the controller's generation markers, which a generation-less
// consumption marker would conflict with.
//
// A responses-continuation delta request carries only new items; any older
// steering turn lives in server-side state this session cannot verify. Nothing
// stages, and the items keep their wake for a full-history turn.
func (s *Session) stageRootDelegateAttentionCoverage(req llm.Request, historyTurns []schema.Turn) {
	if !s.isRootDelegateAttentionReceiver() {
		return
	}
	if req.HistoryMode == llm.HistoryModeResponsesDelta {
		return
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	var staged map[string]struct{}
	for _, turn := range historyTurns {
		if turn.AttentionID == "" {
			continue
		}
		if _, preTurn := s.rootAttentionPreTurnArmIDs[turn.AttentionID]; preTurn {
			continue
		}
		if staged == nil {
			staged = make(map[string]struct{})
		}
		staged[turn.AttentionID] = struct{}{}
	}
	s.rootAttentionStagedIDs = staged
}

// promoteStagedRootDelegateAttention credits the staged set of a round whose
// call settled. Called by the round loop on the success path only.
func (s *Session) promoteStagedRootDelegateAttention() {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if len(s.rootAttentionStagedIDs) == 0 {
		return
	}
	covered := s.rootAttentionCoveredIDs
	if covered == nil {
		covered = make(map[string]struct{}, len(s.rootAttentionStagedIDs))
	}
	maps.Copy(covered, s.rootAttentionStagedIDs)
	s.rootAttentionCoveredIDs = covered
	s.rootAttentionStagedIDs = nil
}

// resetRootDelegateAttentionCoverage clears the per-turn coverage at turn
// start and snapshots the armed set that marking excludes, so consumption
// credits only deliveries armed after this turn began. Guarded like the
// staging it pairs with: a child session tracks no root coverage, and the
// early return skips its per-turn lock and clone. maps.Clone(nil) is nil, and
// the field is lookup-only, so an empty armed set needs no special case.
func (s *Session) resetRootDelegateAttentionCoverage() {
	if !s.isRootDelegateAttentionReceiver() {
		return
	}
	s.attentionMu.Lock()
	s.rootAttentionCoveredIDs = nil
	s.rootAttentionStagedIDs = nil
	s.rootAttentionPreTurnArmIDs = maps.Clone(s.rootAttentionWakeIDs)
	s.attentionMu.Unlock()
}

// unionCoveredRootDelegateAttention adds to the selected IDs the covered
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
func (s *Session) unionCoveredRootDelegateAttention(ids []string) []string {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if len(s.rootAttentionCoveredIDs) == 0 {
		return ids
	}
	pending, err := s.pendingDelegateAttentionIDsLocked()
	if err != nil {
		return ids
	}
	out := slices.Clone(ids)
	for _, id := range pending {
		if _, ok := s.rootAttentionCoveredIDs[id]; ok {
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
	delay := s.rootAttentionRetry.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	s.rootAttentionRetry.active = true
	s.rootAttentionRetry.generation++
	generation := s.rootAttentionRetry.generation
	s.sclock().AfterFunc(delay, func() {
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
			if s.rootAttentionRetry.generation == generation {
				s.rootAttentionRetry.active = false
				s.scheduleRootAttentionRetryLocked()
			}
			s.attentionMu.Unlock()
			return
		}
		defer release()
		s.attentionMu.Lock()
		if s.rootAttentionRetry.generation != generation {
			s.attentionMu.Unlock()
			return
		}
		s.rootAttentionRetry.active = false
		pending := len(s.rootAttentionWakeIDs) != 0
		shouldWake := pending && !s.rootAttentionWake
		if shouldWake {
			s.rootAttentionWake = true
		}
		if pending {
			s.rootAttentionRetry.delay = min(delay*2, jobNotificationRetryMaxDelay)
		} else {
			s.rootAttentionRetry.delay = jobNotificationRetryInitialDelay
		}
		s.attentionMu.Unlock()
		if shouldWake {
			s.notify()
		}
	})
}

func (s *Session) resetRootAttentionRetryLocked() {
	s.rootAttentionRetry.generation++
	s.rootAttentionRetry.active = false
	s.rootAttentionRetry.delay = jobNotificationRetryInitialDelay
}

func (s *Session) scheduleDelegateAttentionArmRetryLocked() {
	if s.delegateAttentionArmRetry.active || len(s.delegateAttentionArmIDs) == 0 {
		return
	}
	delay := s.delegateAttentionArmRetry.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	s.delegateAttentionArmRetry.active = true
	s.delegateAttentionArmRetry.generation++
	generation := s.delegateAttentionArmRetry.generation
	s.sclock().AfterFunc(delay, func() {
		s.attentionMu.Lock()
		if s.delegateAttentionArmRetry.generation != generation {
			s.attentionMu.Unlock()
			return
		}
		s.delegateAttentionArmRetry.active = false
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
			s.delegateAttentionArmRetry.delay = min(delay*2, jobNotificationRetryMaxDelay)
			s.scheduleDelegateAttentionArmRetryLocked()
		}
		s.attentionMu.Unlock()
	})
}

func (s *Session) resetDelegateAttentionArmRetryLocked() {
	s.delegateAttentionArmRetry.generation++
	s.delegateAttentionArmRetry.active = false
	s.delegateAttentionArmRetry.delay = jobNotificationRetryInitialDelay
}

func (s *Session) scheduleStableDelegateAttentionRetry() {
	if s == nil {
		return
	}
	s.attentionMu.Lock()
	if s.stableAttentionRetry.active {
		s.attentionMu.Unlock()
		return
	}
	delay := s.stableAttentionRetry.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	s.stableAttentionRetry.active = true
	s.stableAttentionRetry.generation++
	generation := s.stableAttentionRetry.generation
	s.attentionMu.Unlock()
	s.sclock().AfterFunc(delay, func() {
		s.attentionMu.Lock()
		if s.stableAttentionRetry.generation != generation {
			s.attentionMu.Unlock()
			return
		}
		s.stableAttentionRetry.active = false
		s.attentionMu.Unlock()

		runnable := s.delegateController != nil && s.delegateController.hasRunnableDelegateAttention()
		if runnable {
			s.notify()
		}
		pending := s.delegateController != nil && s.delegateController.hasPendingDelegateAttention()
		s.attentionMu.Lock()
		if pending {
			s.stableAttentionRetry.delay = min(delay*2, jobNotificationRetryMaxDelay)
		} else {
			s.stableAttentionRetry.delay = jobNotificationRetryInitialDelay
		}
		s.attentionMu.Unlock()
		if pending {
			s.scheduleStableDelegateAttentionRetry()
		}
	})
}

func (s *Session) resetStableDelegateAttentionRetry() {
	if s == nil {
		return
	}
	s.attentionMu.Lock()
	s.stableAttentionRetry.generation++
	s.stableAttentionRetry.active = false
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
	for _, id := range ids {
		durable, ok := fold.turns[id]
		if !ok {
			continue
		}
		if err := s.retainDelegateAttentionTurn(durable); err != nil {
			s.attentionMu.Unlock()
			return err
		}
	}
	s.rootAttentionWakeIDs = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s.rootAttentionWakeIDs[id] = struct{}{}
	}
	shouldWake := len(ids) != 0 && !s.rootAttentionWake
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

func (s *Session) retainDelegateAttentionTurn(turn schema.Turn) error {
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
	s.history = append(s.history, turn)
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
	if err := appendDelegateAttentionResolutions(writer, transcript.PlaceAsync, fold, ids, disposition, resumeGeneration); err != nil {
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
	s.transcript = reopened
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
		if _, err := reopened.Record(delegateAttentionResolutionTurn(attentionID, disposition), transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceAsync}); err != nil {
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
	s.transcript = reopened
	s.mu.Unlock()
	adopted = true
	return writer.Close()
}

// appendDelegateAttentionResolutions appends a resolution for each of ids that
// fold does not already record, once per ID however often ids repeats it. It
// leaves fold unchanged: a resident Session's fold is its attention cursor's,
// which must hold only what the file holds, or the verify read after this
// append would confirm it from memory.
func appendDelegateAttentionResolutions(writer *transcript.Writer, place transcript.Placement, fold delegateAttentionFold, ids []string, disposition delegateAttentionResolution, resumeGeneration uint64) error {
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
		if _, err := writer.Record(delegateAttentionResolutionTurnForGeneration(attentionID, disposition, resumeGeneration), transcript.RecordOptions{Door: transcript.DoorSynced, Place: place}); err != nil {
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
		if resumeGeneration != 0 {
			for previousID, generation := range fold.resumeGenerations {
				if generation == resumeGeneration && previousID != attentionID {
					return fmt.Errorf("resume generation %d already claims attention %q", resumeGeneration, previousID)
				}
			}
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
