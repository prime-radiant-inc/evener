package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"primeradiant.com/serf/agent/diagnostic"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

type queuedInputDrainContextKey struct{}
type queuedClientMutationContextKey struct{}

type queuedClientMutationIdentity struct {
	ClientMutationID string
	StableTurnID     string
	QueueEntryID     string
}

func withQueuedClientMutation(ctx context.Context, queued queuedInput) context.Context {
	return context.WithValue(ctx, queuedClientMutationContextKey{}, queuedClientMutationIdentity{
		ClientMutationID: queued.ClientMutationID,
		StableTurnID:     queued.StableTurnID,
		QueueEntryID:     queued.ID,
	})
}

func queuedClientMutationFromContext(ctx context.Context) queuedClientMutationIdentity {
	identity, _ := ctx.Value(queuedClientMutationContextKey{}).(queuedClientMutationIdentity)
	return identity
}

type queuedInputDrainConfig struct {
	rootCtx context.Context
	nextCtx func(context.Context) (context.Context, context.CancelFunc)
}

// WithQueuedInputDrainOnInterrupt marks ctx as a per-turn interrupt context
// whose queued input may continue under rootCtx after ctx is canceled.
func WithQueuedInputDrainOnInterrupt(ctx context.Context, rootCtx context.Context) context.Context {
	return WithQueuedInputDrainOnInterruptHandler(ctx, rootCtx, nil)
}

// WithQueuedInputDrainOnInterruptHandler marks ctx like
// WithQueuedInputDrainOnInterrupt and lets callers install a fresh cancelable
// context when queued input is drained after an interrupt.
func WithQueuedInputDrainOnInterruptHandler(ctx context.Context, rootCtx context.Context, nextCtx func(context.Context) (context.Context, context.CancelFunc)) context.Context {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return context.WithValue(ctx, queuedInputDrainContextKey{}, queuedInputDrainConfig{
		rootCtx: rootCtx,
		nextCtx: nextCtx,
	})
}

// steeringMessage is one entry on the steering queue. Text carries the
// system-reminder body; Images optionally carries attachments that flow
// alongside the text as additional ContentImage parts when the steering
// turn is appended to history (kata t5j6). Provenance carries the causal
// watch origin (nil for human/system-authored steering) so consuming the
// message folds its watch keys into the turn's active provenance.
type steeringMessage struct {
	Text             string             `json:"text,omitempty"`
	Images           []ImageAttachment  `json:"images,omitempty"`
	Provenance       *provenance.Causal `json:"provenance,omitempty"`
	ClientMutationID string             `json:"client_mutation_id,omitempty"`
	StableTurnID     string             `json:"stable_turn_id,omitempty"`
	// Source marks who sent the steering: events.SteeringSourceUser for
	// human-sent steering (the UI steer action, or queued user input
	// drained as steering), empty for daemon/system nudges. Surfaced on the
	// SteeringInjectedData event and persisted on the transcript turn so
	// UIs render user steering as a user message (issue #24).
	Source string `json:"source,omitempty"`
	// Kind names what the daemon injected (events.SteeringKind*), empty when
	// the caller did not say. Surfaced on SteeringInjectedData and persisted on
	// the turn so reload labels a steer the way the live path did.
	Kind string `json:"kind,omitempty"`
}

func steeringInjectedDataFromMessage(msg steeringMessage) events.SteeringInjectedData {
	return events.SteeringInjectedData{
		Text:             msg.Text,
		Images:           userInputImagesFromAttachments(msg.Images),
		ClientMutationID: msg.ClientMutationID,
		StableTurnID:     msg.StableTurnID,
		Source:           msg.Source,
		Kind:             msg.Kind,
	}
}

// Steer queues a text-only message to inject after the current tool round
// completes.
func (s *Session) Steer(msg string) {
	_ = s.trySteer(msg)
}

// SteerKind queues a text-only steering message naming what it is
// (events.SteeringKind*). Prefer it over Steer at every daemon injection site:
// the kind is what a reader's label is built from, and only the site knows it.
func (s *Session) SteerKind(msg, kind string) {
	_ = s.trySteerEnqueue(msg, nil, nil, "", kind)
}

func (s *Session) trySteer(msg string) bool {
	return s.trySteerWithImages(msg, nil)
}

// SteerWithProvenance queues a text-only steering message carrying the causal
// watch provenance that produced it (nil for human/system-authored steering).
// kind names what was injected (events.SteeringKind*), "" when the caller did
// not say.
func (s *Session) SteerWithProvenance(msg string, p *provenance.Causal, kind string) {
	_ = s.trySteerWithProvenance(msg, p, kind)
}

// SteerWithImages queues a steering message that carries optional image
// attachments alongside the text. The combined message is appended to
// session history as a TurnSteering with text + ContentImage parts when
// the steering queue is drained (kata t5j6).
func (s *Session) SteerWithImages(msg string, images []ImageAttachment) {
	_ = s.trySteerWithImages(msg, images)
}

// SteerFromUser queues a text-only steering message sent by the human user
// mid-turn (the UI steer action). Unlike daemon/system nudges queued via
// Steer, it is marked Source "user" so UIs render it as a user message
// rather than a system steering divider (issue #24).
func (s *Session) SteerFromUser(msg string) {
	s.SteerFromUserWithImages(msg, nil)
}

// SteerFromUserWithImages is SteerFromUser with optional image attachments,
// mirroring SteerWithImages for the human-sent path.
func (s *Session) SteerFromUserWithImages(msg string, images []ImageAttachment) {
	if strings.TrimSpace(msg) == "" && len(images) == 0 {
		return
	}
	_, err := s.clientMutationSteer(appwire.TurnSteerParams{
		Ref:              s.ID(),
		ClientMutationID: "legacy_" + newQueueEntryID(),
		Input:            clientMutationInput(msg, images),
	})
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("persist client steering failed: %v", err)})
	}
}

func (s *Session) trySteerWithImages(msg string, images []ImageAttachment) bool {
	return s.trySteerWithImagesAndProvenance(msg, images, nil, "")
}

func (s *Session) trySteerWithProvenance(msg string, p *provenance.Causal, kind string) bool {
	return s.trySteerWithImagesAndProvenance(msg, nil, p, kind)
}

func (s *Session) trySteerWithProvenanceAndNotify(msg string, p *provenance.Causal, kind string) bool {
	if !s.trySteerWithProvenance(msg, p, kind) {
		return false
	}
	s.notify()
	return true
}

func (s *Session) trySteerWithImagesAndProvenance(msg string, images []ImageAttachment, p *provenance.Causal, kind string) bool {
	return s.trySteerEnqueue(msg, images, p, "", kind)
}

// trySteerEnqueue is the steering-queue append primitive. source carries the
// steering provenance marker stored on the entry (events.SteeringSourceUser
// for human-sent steering, "" for daemon/system steering). kind names what the
// daemon injected (events.SteeringKind*), "" when the caller did not say.
func (s *Session) trySteerEnqueue(msg string, images []ImageAttachment, p *provenance.Causal, source string, kind string) bool {
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return false
	}
	if strings.TrimSpace(msg) == "" && len(images) == 0 {
		s.mu.Unlock()
		return false
	}
	entry := steeringMessage{Text: msg, Provenance: provenance.Clone(p), Source: source, Kind: kind}
	if len(images) > 0 {
		entry.Images = append([]ImageAttachment(nil), images...)
	}
	s.steeringQueue = append(s.steeringQueue, entry)
	s.mu.Unlock()
	// Client-authored steering is persisted by the mutation store. The legacy
	// snapshot has one remaining authority: daemon-authored steering.
	if source != events.SteeringSourceUser {
		s.persistQueuesSnapshot()
	}
	return true
}

// wrapHookContext frames hook-provided model context as a system reminder so the
// model treats it as context, not as user speech (matches Claude's "wrapped in a
// system reminder" delivery of additionalContext).
func wrapHookContext(text string) string {
	return "<SYSTEM-REMINDER>" + text + "</SYSTEM-REMINDER>"
}

// deliverHookContext enqueues hook model-context as a steering turn (survives to
// the next model turn for Stop/SubagentStop).
func (s *Session) deliverHookContext(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.SteerKind(wrapHookContext(text), events.SteeringKindHookContext)
}

// deliverHookUserMessage surfaces a hook's user-visible message via the
// diagnostic-warning channel (CLI/TUI/hub), WITHOUT firing the Notification hook
// (plain emit would re-enter it and recurse).
func (s *Session) deliverHookUserMessage(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.emitDiagnosticWarning(events.WarningData{Source: string(diagnostic.SourceHook), Message: text})
}

// FollowUp queues a message to process after the current input completes.
func (s *Session) FollowUp(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closingOrClosedLocked() {
		return
	}
	if strings.TrimSpace(msg) == "" {
		return
	}
	s.followups = append(s.followups, msg)
}

// queuedInput is one entry on the per-session input queue. Text and Images
// are forwarded together when the entry is drained as a fresh user turn.
// Provenance carries the causal watch origin (nil for human-typed input) so a
// DrainAsSteer collapse can fold it into the steering message it injects.
// ID is a stable per-entry identifier minted at enqueue time; it rides the
// queue snapshot so a promote-by-index request can verify the entry it meant
// is still the entry at that index (review F1, issue #22).
type queuedInput struct {
	ID               string             `json:"id"`
	ClientMutationID string             `json:"client_mutation_id,omitempty"`
	StableTurnID     string             `json:"stable_turn_id,omitempty"`
	Text             string             `json:"text,omitempty"`
	Images           []ImageAttachment  `json:"images,omitempty"`
	Provenance       *provenance.Causal `json:"provenance,omitempty"`
}

// queueEntrySeq guarantees queue-entry id uniqueness by construction,
// mirroring escalationSeq: a process-monotonic counter plus a random suffix.
var queueEntrySeq atomic.Uint64

// newQueueEntryID mints a unique opaque handle for one queued input entry.
func newQueueEntryID() string {
	seq := queueEntrySeq.Add(1)
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("q_%d_%s", seq, hex.EncodeToString(b[:]))
}

// Enqueue appends a text-only user message to the per-session input queue
// (kata 111a). See EnqueueWithImages for the variant that carries image
// attachments alongside the text.
func (s *Session) Enqueue(ctx context.Context, text string) error {
	return s.EnqueueWithImages(ctx, text, nil)
}

// EnqueueWithImages appends a user message (text + optional images) to the
// per-session input queue (kata t5j6 extension of kata 111a). Queued
// messages are processed as fresh user turns once the in-flight
// ProcessInput finishes its current turn. Enqueueing on an idle session is
// valid and behaves as a FIFO buffer that will be drained on the next call
// to ProcessInput's outer loop; callers that want immediate processing on
// an idle session should call ProcessInput directly. Returns an error if
// the session is closed or both text and images are empty.
func (s *Session) EnqueueWithImages(ctx context.Context, text string, images []ImageAttachment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return errors.New("queue: text or images required")
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("queue: session is closed")
	}
	s.mu.Unlock()
	_, err := s.clientMutationQueue(appwire.TurnQueueParams{
		Ref:              s.ID(),
		ClientMutationID: "legacy_" + newQueueEntryID(),
		Input:            clientMutationInput(text, images),
	})
	return err
}

// DrainAsSteer pops every queued message, joins them with a blank line, and
// injects the combined text as a single STEERING message to the in-flight
// turn (kata 0bq1 force-steer combined action). Returns an error if the
// queue is empty or the session is closed. Image attachments on any queued
// entry are forwarded as additional ContentImage parts on the steering
// message (kata t5j6).
func (s *Session) DrainAsSteer(ctx context.Context) error {
	return s.DrainAsSteerWithInput(ctx, "", nil)
}

// DrainAsSteerWithInput appends the supplied text/images to the queue and
// drains the full queue as one steering injection while holding the queue
// event lock. This is the atomic force-steer path used by clients that submit
// a composer payload together with the drain request.
func (s *Session) DrainAsSteerWithInput(ctx context.Context, text string, images []ImageAttachment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("drain: session is closed")
	}
	if s.state != SessionProcessing {
		s.mu.Unlock()
		return errors.New("drain: no active turn to steer")
	}
	s.mu.Unlock()
	if err := s.ensureClientMutationStore(); err != nil {
		return err
	}
	snapshot := s.clientMutations.snapshot()
	if len(snapshot.InputQueue) == 0 && strings.TrimSpace(text) == "" && len(images) == 0 {
		return errors.New("drain: queue is empty")
	}
	_, err := s.clientMutationDrain(appwire.TurnDrainAsSteerParams{
		Ref:                   s.ID(),
		ClientMutationID:      "legacy_" + newQueueEntryID(),
		ExpectedQueueRevision: snapshot.QueueRevision,
		Input:                 clientMutationInput(text, images),
	})
	if err != nil {
		return err
	}
	return nil
}

// PromoteQueuedAsSteer removes the single queued message at index and
// injects it as a user-sourced STEERING message into the in-flight turn
// (issue #22 per-message promote; the single-message counterpart of
// DrainAsSteer). Other queued messages stay queued in FIFO order. The
// steering entry keeps the queued message's images and causal provenance,
// and is marked Source "user" so UIs render it as user speech rather than a
// system steering divider (issue #24). When expectedID is non-empty it must
// match the id of the entry currently at index — the queue head can be
// consumed mid-turn, so a bare index captured from an earlier snapshot may
// otherwise resolve to the wrong message (review F1). Returns an error —
// leaving the queue untouched — when the session is closed, no turn is in
// flight, index is out of range, or the id mismatches, so a failed promote
// never silently loses or swaps the follow-up.
func (s *Session) PromoteQueuedAsSteer(ctx context.Context, index int, expectedID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("promote: session is closed")
	}
	if s.state != SessionProcessing {
		s.mu.Unlock()
		return errors.New("promote: no active turn to steer")
	}
	s.mu.Unlock()
	_, err := s.clientMutationPromote(appwire.TurnPromoteQueuedAsSteerParams{
		Ref:              s.ID(),
		Index:            index,
		ClientMutationID: "legacy_" + newQueueEntryID(),
		ExpectedEntryID:  expectedID,
	})
	return err
}

// CancelQueued removes the single queued message at index so it is never
// consumed (issue #23 per-message cancel; also the removal half of the web
// UI's edit-as-cancel-and-recompose action). Other queued messages stay
// queued in FIFO order. Unlike PromoteQueuedAsSteer, cancel does NOT
// require an in-flight turn: a queued entry is cancellable whenever it is
// still queued, including entries buffered on an idle session. When
// expectedID is non-empty it must match the id of the entry currently at
// index — the queue head can be consumed mid-turn, so a bare index captured
// from an earlier snapshot may otherwise resolve to the wrong message
// (review F1). On success it returns the removed entry's full untruncated
// text and image count so the caller can restore the text into a composer
// (edit) and warn about dropped attachments. It returns an error — leaving
// the queue untouched — when the session is closed, index is out of range,
// or the id mismatches, so a failed cancel never silently removes the wrong
// follow-up.
func (s *Session) CancelQueued(ctx context.Context, index int, expectedID string) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return "", 0, errors.New("cancel: session is closed")
	}
	s.mu.Unlock()
	response, err := s.clientMutationCancel(appwire.TurnCancelQueuedParams{
		Ref:              s.ID(),
		Index:            index,
		ClientMutationID: "legacy_" + newQueueEntryID(),
		ExpectedEntryID:  expectedID,
	})
	if err != nil {
		return "", 0, err
	}
	return response.RemovedText, response.RemovedImages, nil
}

// QueueDepth returns the number of messages currently in the input queue.
func (s *Session) QueueDepth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inputQueue)
}

// QueuePreview returns a copy of the queued messages in FIFO order with
// each entry collapsed to its first line and trimmed of trailing CR. The
// output is the user-facing preview shape consumed by both UIs via the
// appwire QueueState (kata r80p); callers that need the raw text should
// reach into the queue mutators directly. Image-only queue entries surface
// as a synthetic "[image]" placeholder so the preview row still renders a
// non-empty line.
func (s *Session) QueuePreview() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inputQueue) == 0 {
		return nil
	}
	out := make([]string, len(s.inputQueue))
	for i, entry := range s.inputQueue {
		out[i] = queuedEntryPreviewLine(entry)
	}
	return out
}

// firstQueueLine returns the first newline-terminated line of msg with a
// trailing CR trimmed. It does not bound the line length — clients are
// expected to apply their own visual truncation when rendering.
func firstQueueLine(msg string) string {
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		msg = msg[:idx]
	}
	return strings.TrimRight(msg, "\r")
}

// queuedEntryPreviewLine returns the preview-line representation of a
// queue entry. Text wins when present; otherwise we surface a synthetic
// "[image]" placeholder (count-prefixed when more than one) so consumers
// don't render an empty row for an image-only queued message.
func queuedEntryPreviewLine(entry queuedInput) string {
	if line := firstQueueLine(entry.Text); strings.TrimSpace(line) != "" {
		return line
	}
	if len(entry.Images) == 1 {
		return "[image]"
	}
	if len(entry.Images) > 1 {
		return fmt.Sprintf("[%d images]", len(entry.Images))
	}
	return ""
}

// popQueueHead removes and returns the next queued entry. Returns a zero
// value when the queue is empty.
func (s *Session) popQueueHead() queuedInput {
	if err := s.ensureClientMutationStore(); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("open client mutation store: %v", err)})
		return queuedInput{}
	}
	var queued queuedInput
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if len(snapshot.InputQueue) == 0 {
			return nil
		}
		entry := snapshot.InputQueue[0]
		if clientMutationQueueEntryReserved(snapshot, entry.ID) {
			return nil
		}
		record := snapshot.Journal[entry.ClientMutationID]
		if record.StableTurnID == "" {
			reserveClientMutationTurnID(snapshot, &record)
		}
		record.ExecutionState = "claimed"
		record.ProjectionState = acceptedClientMutationProjection(record.Method)
		snapshot.Journal[entry.ClientMutationID] = record
		snapshot.PendingExecutions[entry.ClientMutationID] = appwire.PendingMutation{
			ClientMutationID: entry.ClientMutationID,
			Method:           record.Method,
			Input:            cloneClientMutationInput(entry.Input),
			ExecutionState:   "claimed",
			TurnID:           record.StableTurnID,
			QueueEntryIDs:    []string{entry.ID},
			ProjectionState:  acceptedClientMutationProjection(record.Method),
		}
		snapshot.InputQueue = snapshot.InputQueue[1:]
		snapshot.QueueRevision++
		delete(snapshot.BudgetReservations, entry.ClientMutationID)
		snapshot.AcceptedTurns++
		snapshot.ActiveTurnID = record.StableTurnID
		queued = queuedInputFromClientMutation(entry)
		queued.ClientMutationID = entry.ClientMutationID
		queued.StableTurnID = record.StableTurnID
		return nil
	})
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("claim queued input failed: %v", err)})
		return queuedInput{}
	}
	if queued.ClientMutationID != "" {
		s.reflectDurableInputQueue()
	}
	return queued
}

func (s *Session) pushQueueHead(entry queuedInput) {
	if strings.TrimSpace(entry.Text) == "" && len(entry.Images) == 0 {
		return
	}
	if entry.ClientMutationID != "" {
		err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
			pending, ok := snapshot.PendingExecutions[entry.ClientMutationID]
			if !ok || pending.ExecutionState != "claimed" {
				return nil
			}
			record := snapshot.Journal[entry.ClientMutationID]
			record.ExecutionState = "accepted"
			// This moves the mutation BACKWARD to queued state -- no
			// transcript item describes it, so it must report pending, not
			// reflected. Unlike claimClientMutationStart's start-claim branch,
			// no conditional gating is needed here: the guard above already
			// restricts this whole function body to ExecutionState=="claimed"
			// (never "incorporated"), so there is no already-correctly-
			// reflected case sharing this path that a blind swap could
			// downgrade.
			record.ProjectionState = acceptedClientMutationProjection(record.Method)
			snapshot.Journal[entry.ClientMutationID] = record
			delete(snapshot.PendingExecutions, entry.ClientMutationID)
			snapshot.InputQueue = append([]clientMutationQueueEntry{{
				ID:               entry.ID,
				ClientMutationID: entry.ClientMutationID,
				Input:            clientMutationInput(entry.Text, entry.Images),
			}}, snapshot.InputQueue...)
			snapshot.QueueRevision++
			if snapshot.AcceptedTurns > 0 {
				snapshot.AcceptedTurns--
			}
			snapshot.BudgetReservations[entry.ClientMutationID] = clientMutationBudgetReservation{TurnID: entry.StableTurnID, Slots: 1}
			return nil
		})
		if err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("return claimed input failed: %v", err)})
			return
		}
		s.reflectDurableInputQueue()
		return
	}
	s.mu.Lock()
	s.inputQueue = append([]queuedInput{entry}, s.inputQueue...)
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	s.emit(events.EventQueueChanged, data)
}

// QueueIDs returns the stable per-entry ids of the queued messages in FIFO
// order, aligned with QueuePreview. Callers promoting a queued message by
// index should read the id from the same snapshot and pass it back as the
// expected identity so a shifted queue is rejected instead of promoting the
// wrong entry (review F1, issue #22).
func (s *Session) QueueIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inputQueue) == 0 {
		return nil
	}
	out := make([]string, len(s.inputQueue))
	for i, entry := range s.inputQueue {
		out[i] = entry.ID
	}
	return out
}

// QueueTexts returns the full untruncated text of the queued messages in
// FIFO order, aligned with QueuePreview and QueueIDs. It backs the edit
// affordance (issue #23): the client restores the full text into the
// composer before asking the daemon to remove the entry, so the text is
// never lost when the removal loses a race against consumption.
func (s *Session) QueueTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inputQueue) == 0 {
		return nil
	}
	out := make([]string, len(s.inputQueue))
	for i, entry := range s.inputQueue {
		out[i] = entry.Text
	}
	return out
}

// queueChangedDataLocked builds a QueueChangedData snapshot from the
// current inputQueue. The caller must hold s.mu.
func (s *Session) queueChangedDataLocked() events.QueueChangedData {
	data := events.QueueChangedData{Depth: len(s.inputQueue)}
	if len(s.inputQueue) > 0 {
		data.Preview = make([]string, len(s.inputQueue))
		data.IDs = make([]string, len(s.inputQueue))
		data.Texts = make([]string, len(s.inputQueue))
		for i, entry := range s.inputQueue {
			data.Preview[i] = queuedEntryPreviewLine(entry)
			data.IDs[i] = entry.ID
			data.Texts[i] = entry.Text
		}
	}
	return data
}
func queuedInputDrainContext(ctx context.Context, err error) (context.Context, bool) {
	cfg, ok := ctx.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok || cfg.rootCtx == nil {
		return nil, false
	}
	var abort *llm.AbortError
	isAbort := errors.As(err, &abort)
	// A bare context.Canceled is this turn's own cancellation and always drains.
	// An *AbortError wraps a cancellation that may have come from a sub-operation,
	// so it drains only when THIS turn's context was the one canceled. Post
	// honest-Unwrap an *AbortError satisfies errors.Is(_, context.Canceled), so
	// the abort case is discriminated explicitly to preserve that distinction.
	bareCanceled := errors.Is(err, context.Canceled) && !isAbort
	drainable := bareCanceled || (isAbort && errors.Is(ctx.Err(), context.Canceled))
	if !drainable || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, false
	}
	if cfg.rootCtx.Err() != nil {
		return nil, false
	}
	if cfg.nextCtx == nil {
		return cfg.rootCtx, true
	}
	nextCtx, _ := cfg.nextCtx(cfg.rootCtx)
	if nextCtx == nil {
		return nil, false
	}
	return nextCtx, true
}
func (s *Session) drainSteering() []steeringMessage {
	s.mu.Lock()
	if len(s.steeringQueue) == 0 {
		s.mu.Unlock()
		return nil
	}
	out := append([]steeringMessage{}, s.steeringQueue...)
	s.steeringQueue = nil
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	return out
}

// popSteeringHead removes and returns the next steering message, persisting the
// shrunk queue before returning. The second result is false when the queue is
// empty. It mirrors popQueueHead (input queue): injectDrainedSteering consumes
// the steering batch one message at a time so the persisted queue shrinks as
// each message is durably recorded, bounding a mid-drain crash's loss to the
// single in-flight message rather than the whole batch.
func (s *Session) popSteeringHead() (steeringMessage, bool) {
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	s.mu.Lock()
	if len(s.steeringQueue) == 0 {
		s.mu.Unlock()
		return steeringMessage{}, false
	}
	entry := s.steeringQueue[0]
	s.steeringQueue = s.steeringQueue[1:]
	s.mu.Unlock()
	if entry.ClientMutationID != "" {
		if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
			pending, ok := snapshot.PendingExecutions[entry.ClientMutationID]
			if !ok {
				return fmt.Errorf("client steering %q is not pending", entry.ClientMutationID)
			}
			pending.ExecutionState = "claimed"
			snapshot.PendingExecutions[entry.ClientMutationID] = pending
			record := snapshot.Journal[entry.ClientMutationID]
			record.ExecutionState = "claimed"
			snapshot.Journal[entry.ClientMutationID] = record
			return nil
		}); err != nil {
			s.mu.Lock()
			s.steeringQueue = append([]steeringMessage{entry}, s.steeringQueue...)
			s.mu.Unlock()
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("claim client steering failed: %v", err)})
			return steeringMessage{}, false
		}
		return entry, true
	}
	s.persistQueuesSnapshot()
	return entry, true
}

// injectDrainedSteering drains any pending steering messages at a turn
// boundary (or mid-turn injection point), records each as a steering turn in
// history and the transcript — preserving the message's provenance source so
// reload/hydration renders user-sent steering as user speech (issue #24) —
// and emits the steering-injected event. It is the single path every drain
// site uses so live and replayed steering stay consistent.
//
// Crash-window note (design review Important-1, kata 5em1): the steering batch
// is consumed pop-one/persist/consume per message — the persisted queue shrinks
// as each message is durably recorded — so a crash partway through this loop
// loses AT MOST the single message currently being consumed, matching
// popQueueHead's input-queue bound (loss, never duplication). Provenance is
// still folded into s.activeProvenance for the WHOLE batch UPFRONT
// (peekSteeringForTurn), before any message is consumed: interleaving the
// union with each per-message consume would reorder when a message's
// provenance lands relative to this loop's emit() calls (every emit stamps the
// CURRENT active provenance, session_events.go's emit/emitWithProvenance) — a
// change to the causal-provenance timeline the watch-loop-suppression
// machinery (agent/provenance) depends on. Peeking-then-popping preserves that
// timeline exactly while narrowing the crash window. The peeked count bounds
// how many messages this drain consumes, so a steering message appended
// concurrently at the tail is left for the next drain (and unioned then), not
// swept into this batch without its provenance. See
// TestQueuePersist_DrainSteering_CrashLosesAtMostInFlightItem for the pinned
// behavior.
func (s *Session) injectDrainedSteering() {
	for range s.peekSteeringForTurn() {
		msg, ok := s.popSteeringHead()
		if !ok {
			break
		}
		s.consumeSteeringMessage(msg)
	}
}

// consumeSteeringMessage durably records one drained steering message as a
// steering turn (history + transcript) and emits the steering-injected
// event. Factored out of injectDrainedSteering's loop body so a test can
// drive the exact per-message consumption step the production loop uses,
// rather than reimplementing it, when pinning the crash-window behavior
// documented above.
func (s *Session) consumeSteeringMessage(msg steeringMessage) bool {
	t := schema.NewTurn(schema.TurnSteering, steeringMessageToLLM(msg))
	t.SteeringSource = msg.Source
	t.SteeringKind = msg.Kind
	t.ClientMutationID = msg.ClientMutationID
	t.StableTurnID = msg.StableTurnID
	if msg.ClientMutationID != "" {
		if err := s.appendClientMutationTranscript(t); err != nil {
			_ = s.returnClaimedSteering(msg.ClientMutationID)
			s.reflectDurableClientSteering()
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
			return false
		}
		s.mu.Lock()
		s.history = append(s.history, t)
		s.mu.Unlock()
		if err := s.finalizeIncorporatedSteering(msg.ClientMutationID); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("steering incorporation failed: %v", err)})
			return true
		}
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
		return true
	}
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	if err := s.writeTranscript(t); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	return true
}

func (s *Session) returnClaimedSteering(clientMutationID string) error {
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok {
			return nil
		}
		pending.ExecutionState = "accepted"
		snapshot.PendingExecutions[clientMutationID] = pending
		record := snapshot.Journal[clientMutationID]
		record.ExecutionState = "accepted"
		snapshot.Journal[clientMutationID] = record
		return nil
	})
}

// appendSteeringTurn records a daemon steering turn and announces it,
// keeping the persisted turn's kind and the emitted event's kind in step: a
// reader reloading the session sees the same label the live transcript
// showed. For the sites that reach history directly — loop detection, task
// reminders, hook context, the interrupt marker — rather than through the
// steering queue's SteerKind/consumeSteeringMessage path, which already
// persists its own kind.
func (s *Session) appendSteeringTurn(text, kind string) {
	t := schema.NewTurn(schema.TurnSteering, llm.User(text))
	t.SteeringKind = kind
	s.recordTurn(t, t)
	s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: text, Kind: kind})
}

// appendSteeringTurnDurably is appendSteeringTurn's durable counterpart for
// the one direct-append site that must fsync before continuing: job
// notifications, where the durable write is what lets a crash mid-delivery
// re-token the notification instead of dropping it (spec §4.3). Returns the
// write error, so the caller can requeue rather than emit SteeringInjectedData
// for a turn that never made it to disk. The durable write happens before the
// in-memory history append, preserving the crash-window ordering.
func (s *Session) appendSteeringTurnDurably(text, kind string) error {
	t := schema.NewTurn(schema.TurnSteering, llm.User(text))
	t.SteeringKind = kind
	if err := s.writeTranscriptDurable(t); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		return err
	}
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	return nil
}

func (s *Session) hasPendingSteering() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.steeringQueue) > 0
}

func (s *Session) prependSteering(entries []steeringMessage) {
	if len(entries) == 0 {
		return
	}
	s.mu.Lock()
	s.steeringQueue = append(append([]steeringMessage{}, entries...), s.steeringQueue...)
	s.mu.Unlock()
	s.persistQueuesSnapshot()
}

// SteeringEntry is a read-only snapshot of one entry on the steering
// queue. It exists so callers outside the agent package (notably the
// server's wire tests for kata t5j6) can observe pending steering
// messages without reaching into private state. Text + Images are copies;
// mutating them is safe and has no effect on the queue.
type SteeringEntry struct {
	Text   string            // the steering message text
	Images []ImageAttachment // any images attached to the steering message
}

// SteeringQueueSnapshot returns a copy of the session's current steering
// queue. Used by integration tests; production callers should treat the
// steering queue as opaque and trigger flushes via Steer / DrainAsSteer.
func (s *Session) SteeringQueueSnapshot() []SteeringEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steeringQueue) == 0 {
		return nil
	}
	out := make([]SteeringEntry, len(s.steeringQueue))
	for i, entry := range s.steeringQueue {
		copyImages := append([]ImageAttachment(nil), entry.Images...)
		out[i] = SteeringEntry{Text: entry.Text, Images: copyImages}
	}
	return out
}

// steeringMessageToLLM converts a steeringMessage to an llm.Message
// (User-role) suitable for appendTurn(TurnSteering, ...). Text-only
// entries become llm.User(text); image-bearing entries become a
// multi-part message via buildUserInputMessage.
func steeringMessageToLLM(entry steeringMessage) llm.Message {
	if len(entry.Images) == 0 {
		return llm.User(entry.Text)
	}
	return buildUserInputMessage(entry.Text, entry.Images)
}

func (s *Session) popFollowUp() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.followups) == 0 {
		return ""
	}
	msg := s.followups[0]
	s.followups = s.followups[1:]
	return msg
}
