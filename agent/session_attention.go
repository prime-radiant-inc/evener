package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
	observed          map[string]struct{}
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
			if previous, exists := fold.turns[turn.AttentionID]; exists {
				if err := validateDelegateAttentionIdentity(previous, turn.AttentionID, turn.Message, turn.NotificationProvenance); err != nil {
					return delegateAttentionFold{}, err
				}
			} else {
				fold.content[turn.AttentionID] = turn.Message
				fold.turns[turn.AttentionID] = turn
				fold.order = append(fold.order, turn.AttentionID)
			}
		}
		if len(turn.ObservedNotificationIDs) != 0 {
			if turn.Kind != schema.TurnAssistant || turn.Message.Role != llm.RoleAssistant {
				return delegateAttentionFold{}, errors.New("notification observation requires an assistant response")
			}
			for _, id := range turn.ObservedNotificationIDs {
				if _, accepted := fold.content[id]; !accepted {
					return delegateAttentionFold{}, fmt.Errorf("notification %q observed before acceptance", id)
				}
				fold.observed[id] = struct{}{}
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
		observed:          make(map[string]struct{}),
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
		observed:          maps.Clone(f.observed),
		resolutions:       maps.Clone(f.resolutions),
		resumeGenerations: maps.Clone(f.resumeGenerations),
		deliveryCommits:   maps.Clone(f.deliveryCommits),
	}
}

func (f delegateAttentionFold) pendingIDs() []string {
	pending := make([]string, 0, len(f.order))
	for _, attentionID := range f.order {
		_, observed := f.observed[attentionID]
		if _, resolved := f.resolutions[attentionID]; !resolved && !observed {
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
		_, observed := f.observed[attentionID]
		if (resolved || observed) && attentionID != keep {
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

// A duplicate owns the same message and provenance, including real nil
// provenance. Neither missing metadata nor a matching body alone proves custody.
func validateDelegateAttentionIdentity(previous schema.Turn, attentionID string, message llm.Message, causal *provenance.Causal) error {
	if previous.AttentionID != attentionID {
		return fmt.Errorf("attention %q has conflicting identity", attentionID)
	}
	if !reflect.DeepEqual(previous.Message, message) {
		return fmt.Errorf("attention %q has conflicting content", attentionID)
	}
	if !reflect.DeepEqual(previous.NotificationProvenance, provenance.Clone(causal)) {
		return fmt.Errorf("attention %q has conflicting provenance", attentionID)
	}
	return nil
}

func appendColdDelegateAttentionMessageDurablyWithOpen(path, expectedSessionID, attentionID string, message llm.Message, now time.Time, open delegateAttentionWriterOpener, causal ...*provenance.Causal) (appended bool, err error) {
	if expectedSessionID == "" || attentionID == "" {
		return false, errors.New("cold delegate attention identity is incomplete")
	}
	if message.Role != llm.RoleUser || len(message.Content) == 0 {
		return false, errors.New("cold delegate attention message is invalid")
	}
	if open == nil {
		return false, errors.New("cold delegate attention writer opener is nil")
	}
	var expectedProvenance *provenance.Causal
	if len(causal) != 0 {
		expectedProvenance = provenance.Clone(causal[0])
	}
	preflight, err := readDelegateAttentionFold(path, expectedSessionID)
	if err != nil {
		return false, err
	}
	if previous, exists := preflight.turns[attentionID]; exists {
		if err := validateDelegateAttentionIdentity(previous, attentionID, message, expectedProvenance); err != nil {
			return false, err
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
	if previous, exists := fold.turns[attentionID]; exists {
		if err := validateDelegateAttentionIdentity(previous, attentionID, message, expectedProvenance); err != nil {
			return false, err
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
	turn.NotificationProvenance = expectedProvenance
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
	persisted, exists := verified.turns[attentionID]
	if !exists {
		return false, fmt.Errorf("attention %q was not durably appended", attentionID)
	}
	if err := validateDelegateAttentionIdentity(persisted, attentionID, message, expectedProvenance); err != nil {
		return false, err
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
func (s *Session) appendDelegateAttentionMessageDurably(attentionID string, message llm.Message, causal ...*provenance.Causal) (appended bool, err error) {
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
	if ready && sessionID != "" && stateDir == "" {
		fold, _ := s.delegateAttentionFoldLocked()
		deliveryID := strings.TrimPrefix(attentionID, "delegate:")
		if deliveryID != attentionID && fold.deliveryCommits[deliveryID] != "" {
			return false, nil
		}
		if previous, exists := fold.content[attentionID]; exists {
			if !reflect.DeepEqual(previous, message) {
				return false, fmt.Errorf("attention %q has conflicting content", attentionID)
			}
			return false, s.retainDelegateAttentionTurn(fold.turns[attentionID])
		}
		turn := s.withEntryModel(delegateAttentionTurn(attentionID, message, s.sclock().Now()))
		if len(causal) != 0 {
			turn.NotificationProvenance = provenance.Clone(causal[0])
		}
		next, err := extendDelegateAttention(fold, []transcript.Entry{{Turn: turn}})
		if err != nil {
			return false, err
		}
		if err := s.retainDelegateAttentionTurn(turn); err != nil {
			return false, err
		}
		s.attentionFoldCursor.fold = next
		return true, nil
	}
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
	if len(causal) != 0 {
		turn.NotificationProvenance = provenance.Clone(causal[0])
	}
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

func (s *Session) pendingDelegateAttentionIDs() ([]string, error) {
	if s == nil {
		return nil, nil
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	return s.pendingDelegateAttentionIDsLocked()
}

func (s *Session) pendingDelegateAttentionIDsLocked() ([]string, error) {
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil, err
	}
	return fold.pendingIDs(), nil
}

// delegateAttentionFoldLocked folds the session's own transcript. When StateDir
// is intentionally empty, the same fold is canonical for this session's lifetime
// after transcript initialization, independent of compacted history. It makes no
// restart promise. The zero fold stands in before initialization. The caller
// holds attentionMu, before taking s.mu.
func (s *Session) delegateAttentionFoldLocked() (delegateAttentionFold, error) {
	if s == nil {
		return delegateAttentionFold{}, nil
	}
	s.mu.Lock()
	ready := s.transcriptReady
	writer := s.transcript
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || sessionID == "" {
		return delegateAttentionFold{}, nil
	}
	if stateDir == "" {
		if s.attentionFoldCursor.fold.content == nil {
			s.attentionFoldCursor.fold = newDelegateAttentionFold()
		}
		return s.attentionFoldCursor.fold, nil
	}
	fold, err := s.readDelegateAttentionFold(transcriptPath(stateDir, sessionID), sessionID)
	if err != nil {
		return delegateAttentionFold{}, err
	}
	if len(fold.observed) != 0 || len(fold.resolutions) != 0 {
		// A complete response or resolution can survive a failed sync and
		// rollback. Readability alone cannot authorize observation or clear
		// repair debt. attentionMu keeps this barrier and fold together with
		// every session append, without discarding or re-appending the record.
		if err := writer.EstablishDurability(); err != nil {
			return delegateAttentionFold{}, err
		}
	}
	return fold, nil
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

// withAttentionTurns restores to history the attention turns it does not hold.
// Each goes before the first held turn of a later-arriving item (attention is
// in arrival order), or at the end when none is held, so the model still reads
// the items in the order they were delivered. It also returns where each was
// inserted, in order, for the caller's in-flight boundary. It is the lock-free form of
// retainDelegateAttentionTurn's append, for a caller that already holds s.mu
// and owns the slice.
func withAttentionTurns(history, attention []schema.Turn) (_ []schema.Turn, restoredAt []int) {
	for i, turn := range attention {
		if slices.ContainsFunc(history, func(held schema.Turn) bool { return held.AttentionID == turn.AttentionID }) {
			continue
		}
		var at int
		history, at = insertAttentionTurn(history, turn, attention[i+1:])
		restoredAt = append(restoredAt, at)
	}
	return history, restoredAt
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
	s.attentionMu.Lock()
	s.attentionAcceptedID = attentionID
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

// acceptDelegateAttention keeps the controller's exact generation reservation
// while preparing its pending trigger and remaining FIFO wake IDs. Launch
// writes no observation marker, so the trigger stays pending until a synced
// successful response proves presentation.
func (s *Session) acceptDelegateAttention(reservation *delegateStartReservation) error {
	if s == nil || s.delegateController == nil {
		return errDelegateStaleLease
	}
	attentionID, _, err := s.delegateController.attentionReservationIdentity(reservation, s)
	if err != nil {
		return err
	}
	// The reservation claims the generation, not model observation. Keep the
	// trigger pending until a synced successful response proves presentation.
	s.holdAcceptedTrigger(attentionID)
	pendingIDs, err := s.pendingDelegateAttentionIDs()
	if err != nil {
		return err
	}
	// The existing controller claim takes the trigger separately from the
	// remaining FIFO wake IDs. Removing it here is not a transcript receipt.
	pendingIDs = slices.DeleteFunc(pendingIDs, func(id string) bool { return id == attentionID })
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

func (s *Session) hasPendingNotifications() bool {
	pending, err := s.hasUnackedNotifications()
	return err != nil || pending
}

func (s *Session) notificationsRunnable() bool {
	state := s.notificationExecutionState()
	return !state.held && !state.recovering && s.hasPendingNotifications()
}

func (s *Session) notificationSnapshot() []string {
	ids, _ := s.pendingNotificationIDs()
	return ids
}

// finishAttentionTurn repairs the derived index of synced response receipts.
// Earlier successful rounds remain observed when a later round fails. The
// root owns the paced retry and paused notice for work still unobserved.
func (s *Session) finishAttentionTurn(_ []string, turnErr error) error {
	return s.finishNotificationExecution(turnErr)
}

// announceNotificationsPaused says, once per deferral episode, that a
// permanent provider failure has paused background updates, which provider
// and how to resume. Without it the updates just stop arriving. It is
// visible at every level (its code is not informational) and goes to the
// daemon log too. Nothing a provider said reaches the warning: a provider's
// error body can carry anything, so the hint and the log line are the
// failure's kind and status only; the serve loop's own turn-failure line
// already records the failed turn.
func (s *Session) announceNotificationsPaused(turnErr error) {
	summary := llm.ProviderFailureSummary(turnErr)
	slog.Warn("background updates paused", "session", s.ID(), "failure", summary)
	s.emit(events.EventWarning, events.WarningData{
		Message: notificationsPausedMessage(turnErr),
		Source:  string(diagnostic.SourceEvener),
		Title:   "Background updates paused",
		Hint:    summary,
		Code:    events.WarningCodeAttentionPaused,
	})
}

// notificationsPausedMessage names the provider instance that refused (the
// name in Evener's own configuration) and what would resume delivery, by
// the failure's kind as llm classifies it.
func notificationsPausedMessage(turnErr error) string {
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
func (s *Session) stagePresentedAttentionCoverage(ctx context.Context, presentedTurns []schema.Turn, verifier ...func(*llm.ToolResultData) bool) {
	if !s.coversPresentedAttention(ctx) {
		return
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	staged := inlineNotificationPresentations(presentedTurns, verifier...)
	if len(staged) != 0 {
		// Old inline-only records have delivery commits but no canonical
		// acceptance. They remain readable history, never new observations.
		fold, err := s.delegateAttentionFoldLocked()
		for id := range staged {
			if _, accepted := fold.content[id]; err != nil || !accepted {
				delete(staged, id)
			}
		}
	}
	for _, turn := range presentedTurns {
		if turn.AttentionID == "" {
			continue
		}
		// Lineage is conservative, unlike durable observation credit. Union
		// the selected history before dispatch so resulting events retain both
		// this notification and the activation's existing origins. Retries and
		// continuation deltas may retain this lineage without earning coverage;
		// only promoteStagedAttentionCoverage can grant that credit.
		s.unionActiveProvenance(turn.NotificationProvenance)
		if staged == nil {
			staged = make(map[string]struct{})
		}
		staged[turn.AttentionID] = struct{}{}
	}
	s.attentionStagedIDs = staged
}

// inlineNotificationPresentations selects only carriers in this exact history
// snapshot whose result is actually projected. A waiter or a commit in some
// other snapshot cannot hide the canonical steering message or earn coverage.
func inlineNotificationPresentations(turns []schema.Turn, verifier ...func(*llm.ToolResultData) bool) map[string]struct{} {
	verify := delegateInlineBodyPresented
	if len(verifier) != 0 && verifier[0] != nil {
		verify = verifier[0]
	}
	var ids map[string]struct{}
	for _, turn := range turns {
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, commit := range turn.DelegateDeliveryCommits {
			if commit.DeliveryID == "" || commit.ToolCallID == "" {
				continue
			}
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == commit.ToolCallID && verify(part.ToolResult) {
					if ids == nil {
						ids = make(map[string]struct{})
					}
					ids[delegateAttentionID(commit.DeliveryID)] = struct{}{}
					break
				}
			}
		}
	}
	return ids
}

// Tool state is not presentation. It only supplies the original rendering to
// compare with the actual bounded body. This pure predicate accepts only whole
// bodies. Recovery-backed tails require the session-tree check below.
// UseNumber preserves JSON numbers when the result includes structured output.
func delegateInlineBodyPresented(result *llm.ToolResultData) bool {
	state, ok := delegateInlineState(result)
	if !ok {
		return false
	}
	body, ok := result.Content.(string)
	return ok && body == formatDelegateSend(state)
}

func delegateInlineState(result *llm.ToolResultData) (delegateSendResult, bool) {
	var state delegateSendResult
	if result == nil || len(result.ToolState) == 0 {
		return state, false
	}
	decoder := json.NewDecoder(bytes.NewReader(result.ToolState))
	decoder.UseNumber()
	err := decoder.Decode(&state)
	return state, err == nil && state.DelegateID != ""
}

func (s *Session) delegateInlineBodyPresented(result *llm.ToolResultData) bool {
	state, ok := delegateInlineState(result)
	if !ok {
		return false
	}
	body, ok := result.Content.(string)
	if !ok {
		return false
	}
	complete := formatDelegateSend(state)
	return body == complete || s.retainedDelegateInlineBodyPresented(result, complete)
}

// A fallback may only shrink an already bounded body. Keep the call/result
// pair and private state, but do not present a partial notification twice.
func delegateInlineStatusResult(result *llm.ToolResultData) *llm.ToolResultData {
	status := *result
	status.Content = ""
	if body, ok := result.Content.(string); ok && utf8.RuneCountInString(body) >= len("[Result available in notification]") {
		status.Content = "[Result available in notification]"
	}
	return &status
}

// stagedNotificationObservationIDs names only this settled request's candidates.
// A delta has no verified coverage of its server-side prefix. The returned IDs
// become authoritative only when recorded with the successful response.
func (s *Session) stagedNotificationObservationIDs(settled llm.Request) []string {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if settled.HistoryMode == llm.HistoryModeResponsesDelta {
		return nil
	}
	return slices.Sorted(maps.Keys(s.attentionStagedIDs))
}

// promoteStagedAttentionCoverage caches the staged set only after the successful
// response and its receipt have synced. settled is the attempt that answered.
//
// A responses-continuation delta carries only new items; any older steering
// turn lives in server-side state this session cannot verify, so a round that
// settled as a delta credits nothing and the items keep their wake for a
// full-history turn. A delta whose call fell back to a full-history request
// settled as that request, which carried every staged turn.
func (s *Session) promoteStagedAttentionCoverage(settled llm.Request) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	staged := s.attentionStagedIDs
	s.attentionStagedIDs = nil
	if len(staged) == 0 || settled.HistoryMode == llm.HistoryModeResponsesDelta {
		return
	}
	if _, presented := staged[s.attentionAcceptedID]; presented && s.attentionAcceptedID != "" {
		s.attentionAcceptedID = ""
	}
	covered := s.attentionCoveredIDs
	if covered == nil {
		covered = make(map[string]struct{}, len(staged))
	}
	maps.Copy(covered, staged)
	s.attentionCoveredIDs = covered
}

// resetAttentionCoverage clears the per-turn coverage at turn start. It runs
// for every turn, so a turn that covers nothing never inherits an earlier
// turn's coverage.
func (s *Session) resetAttentionCoverage() {
	s.attentionMu.Lock()
	s.attentionCoveredIDs = nil
	s.attentionStagedIDs = nil
	s.attentionMu.Unlock()
}

// unionCoveredAttention returns observed deliveries still missing a derived
// marker, in arrival order. Selected IDs alone never prove observation. The
// resolution filter excludes legacy and discarded records from repair batches.
func (s *Session) unionCoveredAttention(_ []string) []string {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range fold.order {
		if _, resolved := fold.resolutions[id]; resolved {
			continue
		}
		_, observed := fold.observed[id]
		if observed {
			out = append(out, id)
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

func (s *Session) suspendNotificationWake() {
	s.mu.Lock()
	s.notificationSchedule.version++
	s.notificationSchedule.hinted = false
	if s.notificationSchedule.releaseTurnID == "" {
		s.notificationSchedule.retry.generation++
		s.notificationSchedule.retry.active = false
	}
	s.mu.Unlock()
}

// notificationStopHeld reports whether an accepted Stop has parked the
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
func (s *Session) notificationStopHeld() bool {
	if s == nil || s.clientMutations == nil {
		return false
	}
	return s.clientMutations.queueHeld() || s.clientMutations.steeringHeld()
}

// restorePendingNotifications reconstructs the root wake cache
// from the only durable attention authority. It performs no provider or Session
// construction and is called after the root transcript is attached/replayed.
//
// entries is the final in-memory entry list restore produced (refreshed from
// disk when delegate delivery replay appended to the transcript): folding it
// instead of re-opening the file is what keeps resume from strict-decoding
// the whole transcript a second time. A nil list means the caller has no
// decoded list to fold (a fresh session, or a refresh that produced none);
// the fold then re-reads the file.
func (s *Session) restorePendingNotifications(entries []transcript.Entry) error {
	if !s.isRootDelegateAttentionReceiver() {
		return nil
	}
	s.mu.Lock()
	ready := s.transcriptReady
	writer := s.transcript
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || writer == nil || sessionID == "" || stateDir == "" {
		return nil
	}
	// A failed configured writer stays unavailable. Do not inspect or repair
	// its transcript until writer ownership is established.
	if err := s.ackNotifications(nil); err != nil {
		return err
	}
	s.attentionMu.Lock()
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
	s.attentionMu.Unlock()
	return s.requestNotificationWake()
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
// For intentional empty-StateDir sessions, only fold is used: it is the same
// lifetime's canonical attention state, extended at acceptance and successful
// response recording rather than reconstructed from compacted history. No file
// read, durable-record claim or restart survival is implied in that mode.
// The remaining cursor rules below apply to persisted sessions.
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
	f, err := os.Open(path)
	if err != nil {
		*c = delegateAttentionFoldCursor{}
		if os.IsNotExist(err) {
			return newDelegateAttentionFold(), nil
		}
		return delegateAttentionFold{}, fmt.Errorf("open delegate attention transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		*c = delegateAttentionFoldCursor{}
		return delegateAttentionFold{}, fmt.Errorf("stat delegate attention transcript: %w", err)
	}
	return c.readFile(f, info, sessionID)
}

// readFile resumes from the caller's freshly opened handle and its identity.
// Custody queries establish durability against this same file before folding,
// so a second pathname open must not select a different transcript.
func (c *delegateAttentionFoldCursor) readFile(f *os.File, info os.FileInfo, sessionID string) (delegateAttentionFold, error) {
	previous := *c
	*c = delegateAttentionFoldCursor{}
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
	if ready && sessionID != "" && stateDir == "" {
		fold, _ := s.delegateAttentionFoldLocked()
		if err := validateDelegateAttentionResolutions(fold, ids, disposition, resumeGeneration); err != nil {
			return err
		}
		var entries []transcript.Entry
		for _, id := range ids {
			if fold.resolutions[id] != disposition || fold.resumeGenerations[id] != resumeGeneration {
				entries = append(entries, transcript.Entry{Turn: delegateAttentionResolutionTurnForGeneration(id, disposition, resumeGeneration)})
			}
		}
		next, err := extendDelegateAttention(fold, entries)
		if err != nil {
			return err
		}
		s.attentionFoldCursor.fold = next
		return nil
	}
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
