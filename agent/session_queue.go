package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/diagnostic"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

type queuedInputDrainContextKey struct{}

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
	Text       string             `json:"text,omitempty"`
	Images     []ImageAttachment  `json:"images,omitempty"`
	Provenance *provenance.Causal `json:"provenance,omitempty"`
	// Source marks who sent the steering: events.SteeringSourceUser for
	// human-sent steering (the UI steer action, or queued user input
	// drained as steering), empty for daemon/system nudges. Surfaced on the
	// SteeringInjectedData event and persisted on the transcript turn so
	// UIs render user steering as a user message (issue #24).
	Source string `json:"source,omitempty"`
}

func steeringInjectedDataFromMessage(msg steeringMessage) events.SteeringInjectedData {
	return events.SteeringInjectedData{
		Text:   msg.Text,
		Images: userInputImagesFromAttachments(msg.Images),
		Source: msg.Source,
	}
}

// Steer queues a text-only message to inject after the current tool round
// completes.
func (s *Session) Steer(msg string) {
	_ = s.trySteer(msg)
}

func (s *Session) trySteer(msg string) bool {
	return s.trySteerWithImages(msg, nil)
}

// SteerWithProvenance queues a text-only steering message carrying the causal
// watch provenance that produced it (nil for human/system-authored steering).
func (s *Session) SteerWithProvenance(msg string, p *provenance.Causal) {
	_ = s.trySteerWithProvenance(msg, p)
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
	_ = s.trySteerEnqueue(msg, images, nil, events.SteeringSourceUser)
}

func (s *Session) trySteerWithImages(msg string, images []ImageAttachment) bool {
	return s.trySteerWithImagesAndProvenance(msg, images, nil)
}

func (s *Session) trySteerWithProvenance(msg string, p *provenance.Causal) bool {
	return s.trySteerWithImagesAndProvenance(msg, nil, p)
}

func (s *Session) trySteerWithProvenanceAndNotify(msg string, p *provenance.Causal) bool {
	if !s.trySteerWithProvenance(msg, p) {
		return false
	}
	s.notify()
	return true
}

func (s *Session) trySteerWithImagesAndProvenance(msg string, images []ImageAttachment, p *provenance.Causal) bool {
	return s.trySteerEnqueue(msg, images, p, "")
}

// trySteerEnqueue is the steering-queue append primitive. source carries the
// steering provenance marker stored on the entry (events.SteeringSourceUser
// for human-sent steering, "" for daemon/system steering).
func (s *Session) trySteerEnqueue(msg string, images []ImageAttachment, p *provenance.Causal, source string) bool {
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return false
	}
	if strings.TrimSpace(msg) == "" && len(images) == 0 {
		s.mu.Unlock()
		return false
	}
	entry := steeringMessage{Text: msg, Provenance: provenance.Clone(p), Source: source}
	if len(images) > 0 {
		entry.Images = append([]ImageAttachment(nil), images...)
	}
	s.steeringQueue = append(s.steeringQueue, entry)
	s.mu.Unlock()
	s.persistQueuesSnapshot()
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
	s.Steer(wrapHookContext(text))
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
	ID         string             `json:"id"`
	Text       string             `json:"text,omitempty"`
	Images     []ImageAttachment  `json:"images,omitempty"`
	Provenance *provenance.Causal `json:"provenance,omitempty"`
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
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("queue: session is closed")
	}
	entry := queuedInput{ID: newQueueEntryID(), Text: text}
	if len(images) > 0 {
		entry.Images = append([]ImageAttachment(nil), images...)
	}
	s.inputQueue = append(s.inputQueue, entry)
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	s.emit(events.EventQueueChanged, data)
	return nil
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
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("drain: session is closed")
	}
	if s.state != SessionProcessing {
		s.mu.Unlock()
		return errors.New("drain: no active turn to steer")
	}
	if strings.TrimSpace(text) != "" || len(images) > 0 {
		entry := queuedInput{ID: newQueueEntryID(), Text: text}
		if len(images) > 0 {
			entry.Images = append([]ImageAttachment(nil), images...)
		}
		s.inputQueue = append(s.inputQueue, entry)
	}
	if len(s.inputQueue) == 0 {
		s.mu.Unlock()
		return errors.New("drain: queue is empty")
	}
	entries := append([]queuedInput{}, s.inputQueue...)
	s.inputQueue = nil
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	s.emit(events.EventQueueChanged, data)
	texts := make([]string, 0, len(entries))
	var drainedImages []ImageAttachment
	var combinedProvenance *provenance.Causal
	for _, entry := range entries {
		if strings.TrimSpace(entry.Text) != "" {
			texts = append(texts, entry.Text)
		}
		drainedImages = append(drainedImages, entry.Images...)
		combinedProvenance = provenance.Union(combinedProvenance, entry.Provenance)
	}
	combined := strings.Join(texts, "\n\n")
	// The drained queue is user-typed input force-steered into the in-flight
	// turn by a user action (turn/drainAsSteer), so the combined steering
	// message keeps user provenance for rendering (issue #24).
	s.trySteerEnqueue(combined, drainedImages, combinedProvenance, events.SteeringSourceUser)
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
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("promote: session is closed")
	}
	if s.state != SessionProcessing {
		s.mu.Unlock()
		return errors.New("promote: no active turn to steer")
	}
	if index < 0 || index >= len(s.inputQueue) {
		s.mu.Unlock()
		return fmt.Errorf("promote: queue index %d out of range (depth %d)", index, len(s.inputQueue))
	}
	entry := s.inputQueue[index]
	if expectedID != "" && entry.ID != expectedID {
		s.mu.Unlock()
		return fmt.Errorf("promote: queue entry at index %d no longer matches the snapshot (queue changed)", index)
	}
	s.inputQueue = append(s.inputQueue[:index], s.inputQueue[index+1:]...)
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	s.emit(events.EventQueueChanged, data)
	// The promoted entry is user-typed input steered into the in-flight turn
	// by a user action, so it keeps user provenance for rendering — same as
	// the DrainAsSteer collapse (issue #24).
	s.trySteerEnqueue(entry.Text, entry.Images, entry.Provenance, events.SteeringSourceUser)
	return nil
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
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return "", 0, errors.New("cancel: session is closed")
	}
	if index < 0 || index >= len(s.inputQueue) {
		s.mu.Unlock()
		return "", 0, fmt.Errorf("cancel: queue index %d out of range (depth %d)", index, len(s.inputQueue))
	}
	entry := s.inputQueue[index]
	if expectedID != "" && entry.ID != expectedID {
		s.mu.Unlock()
		return "", 0, fmt.Errorf("cancel: queue entry at index %d no longer matches the snapshot (queue changed)", index)
	}
	s.inputQueue = append(s.inputQueue[:index], s.inputQueue[index+1:]...)
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	s.emit(events.EventQueueChanged, data)
	return entry.Text, len(entry.Images), nil
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
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	s.mu.Lock()
	if len(s.inputQueue) == 0 {
		s.mu.Unlock()
		return queuedInput{}
	}
	entry := s.inputQueue[0]
	s.inputQueue = s.inputQueue[1:]
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	s.emit(events.EventQueueChanged, data)
	return entry
}

func (s *Session) pushQueueHead(entry queuedInput) {
	if strings.TrimSpace(entry.Text) == "" && len(entry.Images) == 0 {
		return
	}
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
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

// injectDrainedSteering drains any pending steering messages at a turn
// boundary (or mid-turn injection point), records each as a steering turn in
// history and the transcript — preserving the message's provenance source so
// reload/hydration renders user-sent steering as user speech (issue #24) —
// and emits the steering-injected event. It is the single path every drain
// site uses so live and replayed steering stay consistent.
func (s *Session) injectDrainedSteering() {
	for _, msg := range s.drainSteeringForTurn() {
		t := schema.NewTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		t.SteeringSource = msg.Source
		s.mu.Lock()
		s.history = append(s.history, t)
		s.mu.Unlock()
		if err := s.transcript.Append(t); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		}
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
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
