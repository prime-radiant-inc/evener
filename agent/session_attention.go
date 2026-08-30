package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

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
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newDelegateAttentionFold(), nil
		}
		return delegateAttentionFold{}, fmt.Errorf("open delegate attention transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReaderSize(f, 64*1024)
	headerRead := false
	entries := make([]transcript.Entry, 0)
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if readErr != nil {
			return delegateAttentionFold{}, readErr
		}
		if !complete {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			header, err := transcript.DecodeHeader(line)
			if err != nil {
				return delegateAttentionFold{}, err
			}
			if header.SessionID != expectedSessionID {
				return delegateAttentionFold{}, fmt.Errorf("delegate attention transcript session %q, want %q", header.SessionID, expectedSessionID)
			}
			headerRead = true
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			return delegateAttentionFold{}, err
		}
		entries = append(entries, entry)
	}
	if !headerRead {
		return delegateAttentionFold{}, errors.New("delegate attention transcript has no header")
	}
	return foldDelegateAttention(entries)
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
	fold := newDelegateAttentionFold()
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
	if err := writer.AppendDurable(turn); err != nil {
		return false, err
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
	return appendDelegateAttentionResolutions(writer, fold, ids, disposition, resumeGeneration)
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
	if err := writer.AppendDurable(turn); err != nil {
		return false, err
	}
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
// the whole transcript a second time. A nil entries list falls back to the
// file read (the fresh-session construction path, which holds no decoded
// entry list).
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
		s.history[index] = turn
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
			s.history = append(s.history[:index], s.history[index+1:]...)
			return
		}
	}
}

func (s *Session) readDelegateAttentionFold(path, sessionID string) (delegateAttentionFold, error) {
	if readFold := s.cfg.testOnly.delegateAttentionReadFold; readFold != nil {
		return readFold(path, sessionID)
	}
	return readDelegateAttentionFold(path, sessionID)
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
	fold, err := readDelegateAttentionFold(path, sessionID)
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
	if err := appendDelegateAttentionResolutions(writer, fold, ids, disposition, resumeGeneration); err != nil {
		return err
	}
	verified, err := readDelegateAttentionFold(path, sessionID)
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

// reopenAttentionTranscriptDurably rebuilds the writer sequence from complete
// records, establishes a fresh filesystem barrier, and preserves the session's
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
// restores the writer's sequence from the transcript before stop can release
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
		if err := reopened.AppendDurable(delegateAttentionResolutionTurn(attentionID, disposition)); err != nil {
			return err
		}
	} else if err := reopened.EstablishDurability(); err != nil {
		return err
	}
	verified, err := readDelegateAttentionFold(path, sessionID)
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

func appendDelegateAttentionResolutions(writer *transcript.Writer, fold delegateAttentionFold, ids []string, disposition delegateAttentionResolution, resumeGeneration uint64) error {
	if err := validateDelegateAttentionResolutions(fold, ids, disposition, resumeGeneration); err != nil {
		return err
	}
	for _, attentionID := range ids {
		if previous, resolved := fold.resolutions[attentionID]; resolved && previous == disposition && fold.resumeGenerations[attentionID] == resumeGeneration {
			continue
		}
		if err := writer.AppendDurable(delegateAttentionResolutionTurnForGeneration(attentionID, disposition, resumeGeneration)); err != nil {
			return err
		}
		fold.resolutions[attentionID] = disposition
		fold.resumeGenerations[attentionID] = resumeGeneration
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
