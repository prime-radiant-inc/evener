package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
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
// turn is appended to history (kata t5j6).
type steeringMessage struct {
	Text   string
	Images []ImageAttachment
}

func steeringInjectedDataFromMessage(msg steeringMessage) events.SteeringInjectedData {
	return events.SteeringInjectedData{
		Text:   msg.Text,
		Images: userInputImagesFromAttachments(msg.Images),
	}
}

// Steer queues a text-only message to inject after the current tool round
// completes.
func (s *Session) Steer(msg string) {
	s.SteerWithImages(msg, nil)
}

// SteerWithImages queues a steering message that carries optional image
// attachments alongside the text. The combined message is appended to
// session history as a TurnSteering with text + ContentImage parts when
// the steering queue is drained (kata t5j6).
func (s *Session) SteerWithImages(msg string, images []ImageAttachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closingOrClosedLocked() {
		return
	}
	if strings.TrimSpace(msg) == "" && len(images) == 0 {
		return
	}
	entry := steeringMessage{Text: msg}
	if len(images) > 0 {
		entry.Images = append([]ImageAttachment(nil), images...)
	}
	s.steeringQueue = append(s.steeringQueue, entry)
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
	s.emitDiagnosticWarning(events.WarningData{Source: "hook", Message: text})
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
type queuedInput struct {
	Text   string
	Images []ImageAttachment
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
	entry := queuedInput{Text: text}
	if len(images) > 0 {
		entry.Images = append([]ImageAttachment(nil), images...)
	}
	s.inputQueue = append(s.inputQueue, entry)
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
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
		entry := queuedInput{Text: text}
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
	s.emit(events.EventQueueChanged, data)
	texts := make([]string, 0, len(entries))
	var drainedImages []ImageAttachment
	for _, entry := range entries {
		if strings.TrimSpace(entry.Text) != "" {
			texts = append(texts, entry.Text)
		}
		drainedImages = append(drainedImages, entry.Images...)
	}
	combined := strings.Join(texts, "\n\n")
	if len(drainedImages) == 0 {
		s.Steer(combined)
	} else {
		s.SteerWithImages(combined, drainedImages)
	}
	return nil
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
	s.emit(events.EventQueueChanged, data)
}

// queueChangedDataLocked builds a QueueChangedData snapshot from the
// current inputQueue. The caller must hold s.mu.
func (s *Session) queueChangedDataLocked() events.QueueChangedData {
	data := events.QueueChangedData{Depth: len(s.inputQueue)}
	if len(s.inputQueue) > 0 {
		data.Preview = make([]string, len(s.inputQueue))
		for i, entry := range s.inputQueue {
			data.Preview[i] = queuedEntryPreviewLine(entry)
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
	defer s.mu.Unlock()
	if len(s.steeringQueue) == 0 {
		return nil
	}
	out := append([]steeringMessage{}, s.steeringQueue...)
	s.steeringQueue = nil
	return out
}

func (s *Session) prependSteering(entries []steeringMessage) {
	if len(entries) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steeringQueue = append(append([]steeringMessage{}, entries...), s.steeringQueue...)
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
