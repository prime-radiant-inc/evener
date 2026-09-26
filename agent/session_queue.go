package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type queuedInputDrainContextKey struct{}
type queuedClientMutationContextKey struct{}

type queuedClientMutationIdentity struct {
	ClientMutationID string
	StableTurnID     string
	QueueEntryID     string
	// SteeringCarrier marks the synthetic entry claimSteeringCarrierInput
	// builds for pending user steering: ClientMutationID is the steer's,
	// StableTurnID the id it reserved, and there is no content of its own.
	SteeringCarrier bool
}

func withQueuedClientMutation(ctx context.Context, queued queuedInput) context.Context {
	return context.WithValue(ctx, queuedClientMutationContextKey{}, queuedClientMutationIdentityOf(queued))
}

// queuedClientMutationIdentityOf is withQueuedClientMutation's identity for a
// queued input, for a caller that holds the input rather than the context.
func queuedClientMutationIdentityOf(queued queuedInput) queuedClientMutationIdentity {
	return queuedClientMutationIdentity{
		ClientMutationID: queued.ClientMutationID,
		StableTurnID:     queued.StableTurnID,
		QueueEntryID:     queued.ID,
		SteeringCarrier:  queued.SteeringCarrier,
	}
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
//
// A nil nextCtx, or one that returns a nil context, means the drained turn runs
// under rootCtx — the same as installing no handler at all. It does NOT mean
// "do not drain": the recovery classifies the interrupt before it claims the
// queue head, so by the time this factory is consulted the entry is already
// claimed and refusing would strand it. A host that needs its own cancelation
// registered for the drained turn must return a context here; under rootCtx no
// per-turn cancel is registered, so an out-of-band interrupt will not reach it.
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
	Text   string            `json:"text,omitempty"`
	Images []ImageAttachment `json:"images,omitempty"`
	// SkillNames carries the canonical skill identities a durable client
	// steering input selected. Identities only, never bodies: the complete
	// instructions load from the recorded sources at consumption.
	SkillNames       []string           `json:"skill_names,omitempty"`
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
	// TaskCompletion carries typed dependency state for tasks-done steering so
	// consumers do not need to parse the system-reminder prose.
	TaskCompletion *events.TaskCompletionSteeringData `json:"task_completion,omitempty"`
	turnOwner      *struct{ _ byte }                  `json:"-"`
}

func (s *Session) removeAllTurnOwnedSteering() {
	s.mu.Lock()
	if len(s.visionTurnOwners) == 0 {
		s.mu.Unlock()
		return
	}
	owners := make(map[*struct{ _ byte }]struct{}, len(s.visionTurnOwners))
	for _, owner := range s.visionTurnOwners {
		owners[owner] = struct{}{}
	}
	filtered := s.steeringQueue[:0]
	for _, entry := range s.steeringQueue {
		if _, ok := owners[entry.turnOwner]; !ok {
			filtered = append(filtered, entry)
		}
	}
	s.steeringQueue = filtered
	s.visionTurnOwners = nil
	s.mu.Unlock()
	s.persistQueuesSnapshot()
}

func (s *Session) finishTurnOwnedSteering() {
	s.mu.Lock()
	s.visionTurnOwners = nil
	s.mu.Unlock()
}

func (s *Session) trySteerTurnOwnedMessage(entry steeringMessage, owner *struct{ _ byte }) bool {
	release, admissionErr := s.beginRetirementMutation("input")
	if admissionErr != nil {
		s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("input admission failed: %v", admissionErr)})
		return false
	}
	defer release()
	entry.turnOwner = owner
	s.mu.Lock()
	if s.closingOrClosedLocked() || !inputHasContent(entry.Text, entry.Images, entry.SkillNames) {
		s.mu.Unlock()
		return false
	}
	s.steeringQueue = append(s.steeringQueue, entry)
	s.visionTurnOwners = append(s.visionTurnOwners, owner)
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	return true
}

func steeringInjectedDataFromMessage(msg steeringMessage) events.SteeringInjectedData {
	return events.SteeringInjectedData{
		Text:             msg.Text,
		Images:           userInputImagesFromAttachments(msg.Images),
		ClientMutationID: msg.ClientMutationID,
		StableTurnID:     msg.StableTurnID,
		Source:           msg.Source,
		Kind:             msg.Kind,
		TaskCompletion:   cloneTaskCompletionSteeringData(msg.TaskCompletion),
	}
}

func cloneTaskCompletionSteeringData(data *events.TaskCompletionSteeringData) *events.TaskCompletionSteeringData {
	if data == nil {
		return nil
	}
	return &events.TaskCompletionSteeringData{
		CompletionState:     data.CompletionState,
		BlockingDelegateIDs: append([]string{}, data.BlockingDelegateIDs...),
	}
}

// Steer queues a text-only message to inject after the current tool round
// completes.
func (s *Session) Steer(msg string) error {
	_, err := s.trySteer(msg)
	return err
}

// SteerKind queues a text-only steering message naming what it is
// (events.SteeringKind*). Prefer it over Steer at every daemon injection site:
// the kind is what a reader's label is built from, and only the site knows it.
func (s *Session) SteerKind(msg, kind string) error {
	_, err := s.trySteerEnqueue(msg, nil, nil, "", kind)
	return err
}

// SteerTaskCompletion queues tasks-done steering with its blocking delegate
// dependencies available as typed data as well as rendered model context.
func (s *Session) SteerTaskCompletion(msg string, blockingDelegateIDs []string) error {
	completion := taskCompletionSteeringData(blockingDelegateIDs)
	_, err := s.trySteerMessage(steeringMessage{
		Text:           msg,
		Kind:           events.SteeringKindTasksDone,
		TaskCompletion: &completion,
	})
	return err
}

// routeSystemNotification delivers a daemon-authored system notification to
// another session in the live session tree. Most callback receivers are the
// session that installed the watch, so routing moves upward toward an
// ancestor; a source:"parent" (or other descendant-receiver) watch installs on
// an ANCESTOR's job manager with a descendant recorded as receiver instead
// (40ea153b9), so routing also checks the live subtree before giving up.
func (s *Session) routeSystemNotification(receiverSessionID, message string) bool {
	if s == nil || strings.TrimSpace(receiverSessionID) == "" {
		return false
	}
	if s.id == receiverSessionID {
		return s.enqueueSystemNotification(message)
	}
	for _, descendant := range s.liveDescendantSessions() {
		if descendant.id == receiverSessionID {
			return descendant.enqueueSystemNotification(message)
		}
	}
	if parent := s.cfg.spawn.parentSystemNotification; parent != nil {
		return parent(receiverSessionID, message)
	}
	return false
}

func (s *Session) enqueueSystemNotification(message string) bool {
	return s.trySteerWithProvenanceAndNotify(message, nil, events.SteeringKindNotification)
}

func (s *Session) trySteer(msg string) (bool, error) {
	return s.trySteerWithImages(msg, nil)
}

// SteerWithProvenance queues a text-only steering message carrying the causal
// watch provenance that produced it (nil for human/system-authored steering).
// kind names what was injected (events.SteeringKind*), "" when the caller did
// not say.
func (s *Session) SteerWithProvenance(msg string, p *provenance.Causal, kind string) error {
	_, err := s.trySteerWithProvenance(msg, p, kind)
	return err
}

// SteerWithImages queues a steering message that carries optional image
// attachments alongside the text. The combined message is appended to
// session history as a TurnSteering with text + ContentImage parts when
// the steering queue is drained (kata t5j6).
func (s *Session) SteerWithImages(msg string, images []ImageAttachment) error {
	_, err := s.trySteerWithImages(msg, images)
	return err
}

// SteerFromUser queues a text-only steering message sent by the human user
// mid-turn (the UI steer action). Unlike daemon/system nudges queued via
// Steer, it is marked Source "user" so UIs render it as a user message
// rather than a system steering divider (issue #24).
func (s *Session) SteerFromUser(msg string) error {
	return s.SteerFromUserWithImages(msg, nil)
}

// SteerFromUserWithImages is SteerFromUser with optional image attachments,
// mirroring SteerWithImages for the human-sent path.
func (s *Session) SteerFromUserWithImages(msg string, images []ImageAttachment) error {
	if strings.TrimSpace(msg) == "" && len(images) == 0 {
		return nil
	}
	_, err := s.clientMutationSteer(appwire.TurnSteerParams{
		Ref:              s.ID(),
		ClientMutationID: "legacy_" + newQueueEntryID(),
		Input:            clientMutationInput(msg, images, nil),
	})
	return err
}

func (s *Session) trySteerWithImages(msg string, images []ImageAttachment) (bool, error) {
	return s.trySteerWithImagesAndProvenance(msg, images, nil, "")
}

func (s *Session) trySteerWithProvenance(msg string, p *provenance.Causal, kind string) (bool, error) {
	return s.trySteerWithImagesAndProvenance(msg, nil, p, kind)
}

func (s *Session) trySteerWithProvenanceAndNotify(msg string, p *provenance.Causal, kind string) bool {
	ok, err := s.trySteerWithProvenance(msg, p, kind)
	if err != nil {
		s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("steering admission failed: %v", err)})
		return false
	}
	if !ok {
		return false
	}
	s.notify()
	return true
}

// enqueueDelegateCallerSteeringDurably admits a delegate's root-caller update
// only after the existing root steering-queue snapshot contains it. Holding
// the queue persistence lock and Session lock across that one write keeps the
// entry invisible to the running root until a crash can recover it.
func (s *Session) enqueueDelegateCallerSteeringDurably(msg string, p *provenance.Causal) error {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return err
	}
	defer release()
	if strings.TrimSpace(msg) == "" {
		return errors.New("invalid_request: message is required")
	}
	entry := steeringMessage{
		Text:       msg,
		Provenance: provenance.Clone(p),
		Kind:       events.SteeringKindAgentMessage,
	}
	s.queuePersistMu.Lock()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		s.queuePersistMu.Unlock()
		return errors.New("caller unavailable")
	}
	prospective := append(append([]steeringMessage(nil), s.steeringQueue...), entry)
	if err := saveQueues(s.stateDir, s.id, daemonSourcedSteering(prospective), nil); err != nil {
		s.mu.Unlock()
		s.queuePersistMu.Unlock()
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("queue persist failed: %v", err)})
		return fmt.Errorf("persist caller steering: %w", err)
	}
	s.steeringQueue = append(s.steeringQueue, entry)
	s.mu.Unlock()
	s.queuePersistMu.Unlock()
	s.notify()
	return nil
}

func (s *Session) trySteerWithImagesAndProvenance(msg string, images []ImageAttachment, p *provenance.Causal, kind string) (bool, error) {
	return s.trySteerEnqueue(msg, images, p, "", kind)
}

// trySteerEnqueue is the steering-queue append primitive. source carries the
// steering provenance marker stored on the entry (events.SteeringSourceUser
// for human-sent steering, "" for daemon/system steering). kind names what the
// daemon injected (events.SteeringKind*), "" when the caller did not say.
func (s *Session) trySteerEnqueue(msg string, images []ImageAttachment, p *provenance.Causal, source string, kind string) (bool, error) {
	entry := steeringMessage{Text: msg, Provenance: provenance.Clone(p), Source: source, Kind: kind}
	if len(images) > 0 {
		entry.Images = append([]ImageAttachment(nil), images...)
	}
	return s.trySteerMessage(entry)
}

func (s *Session) trySteerMessage(entry steeringMessage) (bool, error) {
	return s.trySteerMessageUnlessSuperseded(entry, ungatedFoldRevision)
}

// steerKindForFold is SteerKind for a fold flush's last-write-wins steering
// (the task-list and transcript reminders): the steering is refused, at the
// moment it would be enqueued, once a newer fold has published.
func (s *Session) steerKindForFold(msg, kind string, publishedRevision int) error {
	_, err := s.trySteerMessageUnlessSuperseded(steeringMessage{Text: msg, Kind: kind}, publishedRevision)
	return err
}

// trySteerMessageUnlessSuperseded enqueues entry unless publishedRevision
// (a fold's publication revision, or ungatedFoldRevision for steering that
// is not tied to a fold) is older than the newest published fold. The
// publication-order check runs under the SAME s.mu hold that appends to the
// queue, so a newer publication cannot slip in between the check and the
// enqueue: a stale fold's steering is refused rather than landing after
// the newer fold's own.
func (s *Session) trySteerMessageUnlessSuperseded(entry steeringMessage, publishedRevision int) (bool, error) {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return false, err
	}
	defer release()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return false, nil
	}
	if !inputHasContent(entry.Text, entry.Images, entry.SkillNames) {
		s.mu.Unlock()
		return false, nil
	}
	if publishedRevision != ungatedFoldRevision && publishedRevision < s.newestPublishedFoldRevision {
		s.mu.Unlock()
		return false, nil
	}
	s.steeringQueue = append(s.steeringQueue, entry)
	s.mu.Unlock()
	// Client-authored steering is persisted by the mutation store. The legacy
	// snapshot has one remaining authority: daemon-authored steering.
	if entry.Source != events.SteeringSourceUser {
		s.persistQueuesSnapshot()
	}
	return true, nil
}

// wrapHookContext frames hook-provided model context as a system reminder so the
// model treats it as context, not as user speech (matches Claude's "wrapped in a
// system reminder" delivery of additionalContext).
func wrapHookContext(text string) string {
	return "<SYSTEM-REMINDER>" + text + "</SYSTEM-REMINDER>"
}

// deliverHookContext enqueues hook model-context as a steering turn (survives to
// the next model turn for Stop/SubagentStop).
func (s *Session) deliverHookContext(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return s.SteerKind(wrapHookContext(text), events.SteeringKindHookContext)
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
func (s *Session) FollowUp(msg string) error {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return err
	}
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closingOrClosedLocked() {
		return nil
	}
	if strings.TrimSpace(msg) == "" {
		return nil
	}
	s.followups = append(s.followups, msg)
	return nil
}

// queuedInput is one entry on the per-session input queue. Text and Images
// are forwarded together when the entry is drained as a fresh user turn.
// Provenance carries the causal watch origin (nil for human-typed input) so a
// DrainAsSteer collapse can fold it into the steering message it injects.
// ID is a stable per-entry identifier minted at enqueue time; it rides the
// queue snapshot so a promote-by-index request can verify the entry it meant
// is still the entry at that index (review F1, issue #22).
type queuedInput struct {
	ID               string            `json:"id"`
	ClientMutationID string            `json:"client_mutation_id,omitempty"`
	StableTurnID     string            `json:"stable_turn_id,omitempty"`
	Text             string            `json:"text,omitempty"`
	Images           []ImageAttachment `json:"images,omitempty"`
	// SkillNames carries the canonical skill identities this durable input
	// selected. Identities only, never bodies: the complete instructions load
	// from the recorded sources at actual consumption.
	SkillNames []string           `json:"skill_names,omitempty"`
	Provenance *provenance.Causal `json:"provenance,omitempty"`
	// SteeringCarrier marks the entry claimSteeringCarrierInput synthesizes
	// to run pending user steering as a turn of its own: never queued, never
	// persisted, and carrying no content -- the steering it exists for is
	// drained at the turn's acceptance like any queued message's.
	SteeringCarrier bool `json:"-"`
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
	release, admissionErr := s.beginRetirementMutation("input")
	if admissionErr != nil {
		return admissionErr
	}
	defer release()
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
		Input:            clientMutationInput(text, images, nil),
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
	release, admissionErr := s.beginRetirementMutation("input")
	if admissionErr != nil {
		return admissionErr
	}
	defer release()
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
		Input:                 clientMutationInput(text, images, nil),
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
	release, admissionErr := s.beginRetirementMutation("input")
	if admissionErr != nil {
		return admissionErr
	}
	defer release()
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
	release, admissionErr := s.beginRetirementMutation("input")
	if admissionErr != nil {
		return "", 0, admissionErr
	}
	defer release()
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

// pendingQueueDepth is QueueDepth for the purpose of "is this session
// working": it reports zero for a queue parked by a Stop, because parked
// messages are not work in progress -- nothing will run them until the user
// asks.
//
// QueueDepth itself still counts them. The messages ARE there and the queue
// strip must keep showing them; what changes is only whether their presence
// makes the session claim to be busy (kata wms7).
func (s *Session) pendingQueueDepth() int {
	if s.clientMutations != nil && s.clientMutations.queueHeld() {
		return 0
	}
	return s.QueueDepth()
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
	if len(entry.SkillNames) == 1 {
		return "[skill]"
	}
	if len(entry.SkillNames) > 1 {
		return fmt.Sprintf("[%d skills]", len(entry.SkillNames))
	}
	return ""
}

// popQueueHead removes and returns the next queued entry. Returns a zero
// value when the queue is empty.
//
// The claim refuses a poisoned transcript on the same store generation it
// claims on (popQueueHeadRefusingPoison); this form drops that refusal, which
// claims nothing and leaves the head queued. A caller that would announce the
// turn it claimed -- the wake and the drain loop -- reads the refusing form
// instead, so no call can announce a turn the transcript cannot record.
func (s *Session) popQueueHead() queuedInput {
	queued, _ := s.popQueueHeadRefusingPoison()
	return queued
}

// popQueueHeadRefusingPoison is the queue head's claim: it returns the entry it
// took, or the poisoned-transcript refusal that stopped it, or neither when the
// head is not claimable at all. The refusal is decided on the same clone the
// claim acts on, not on a snapshot the caller read a step earlier, so a Stop,
// a poisoning, or a queue change that lands between the caller's view of the
// work and this claim cannot make the refusal and the claim disagree: whenever
// this would claim, it also refuses a poisoned transcript, and a claimable
// state that never materializes is quiet rather than an error.
func (s *Session) popQueueHeadRefusingPoison() (queuedInput, error) {
	release, admissionErr := s.beginRetirementMutation("input")
	if admissionErr != nil {
		s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("input admission failed: %v", admissionErr)})
		return queuedInput{}, nil
	}
	defer release()
	if err := s.ensureClientMutationStore(); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("open client mutation store: %v", err)})
		return queuedInput{}, nil
	}
	// The writer is sampled under s.mu here, before the serializer takes
	// clientMutations.mu; the claim reads only the writer's own lock inside, so
	// the serializer never waits on s.mu.
	writer := s.attachedTranscript()
	if s.cfg.testOnly.queueHeadClaimSampled != nil {
		s.cfg.testOnly.queueHeadClaimSampled()
	}
	var queued queuedInput
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if s.cfg.testOnly.queueHeadClaimInSerializer != nil {
			s.cfg.testOnly.queueHeadClaimInSerializer()
		}
		if !queueHeadClaimable(snapshot) {
			return nil
		}
		// The transcript's refusal is part of the claim decision, read on the
		// generation this claim commits against. Returning it from the mutation
		// is what keeps the refusal from committing anything -- a nil return
		// would save the generation the claim then declined to change.
		if refusal := refuseOnUnhealthyTranscript(writer); refusal != nil {
			return refusal
		}
		entry := snapshot.InputQueue[0]
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
	// A refusal is the transcript's, not the store's: distinguish it from a
	// claim write that failed, which stands down with a warning instead.
	if transcriptRefusedClaim(err) {
		return queuedInput{}, err
	}
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("claim queued input failed: %v", err)})
		return queuedInput{}, nil
	}
	if queued.ClientMutationID != "" {
		s.reflectDurableInputQueue()
	}
	return queued, nil
}

// queueHeadClaimable reports whether the queue head is one popQueueHead may
// take. It is the whole of that decision, so every caller that needs to know
// whether this session has a queued message it could actually run asks the same
// question the pop asks -- a wake that answered it differently would act on work
// the pop would refuse.
//
// A Stop is accepted before it is finalized, and the runner is being cancelled
// for the whole of that window. Claiming the queue head there hands the message
// to a turn that is already dying, so it is refused -- the same rule
// AcceptClientMutationStart and claimClientMutationStart hold at the other two
// claim sites. QueueHeld is the same refusal held past the fence's finalize: a
// Stop parks the queue until the user asks for something to run, and this is the
// single gate both restart rails claim through -- the drain loop and
// ProcessPendingUserInput behind the wake (kata wms7).
func queueHeadClaimable(snapshot *clientMutationSnapshot) bool {
	if len(snapshot.InputQueue) == 0 || snapshot.InterruptFence != nil || snapshot.QueueHeld {
		return false
	}
	return !clientMutationQueueEntryReserved(snapshot, snapshot.InputQueue[0].ID)
}

func (s *Session) pushQueueHead(entry queuedInput) error {
	if !inputHasContent(entry.Text, entry.Images, entry.SkillNames) {
		return nil
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
				Input:            clientMutationInput(entry.Text, entry.Images, entry.SkillNames),
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
			return err
		}
		s.reflectDurableInputQueue()
		return nil
	}
	s.mu.Lock()
	s.inputQueue = append([]queuedInput{entry}, s.inputQueue...)
	data := s.queueChangedDataLocked()
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	s.emit(events.EventQueueChanged, data)
	return nil
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
		data.SkillNames = make([][]string, len(s.inputQueue))
		for i, entry := range s.inputQueue {
			data.Preview[i] = queuedEntryPreviewLine(entry)
			data.IDs[i] = entry.ID
			data.Texts[i] = entry.Text
			data.SkillNames[i] = slices.Clone(entry.SkillNames)
		}
	}
	return data
}

// interruptDrainConfig reports whether an interrupted turn may consume the
// queue head, and hands back the drain handler to run it under.
//
// It is a pure function of THIS turn's context and error: it reads no queue
// state, and it commits nothing. That is what lets the interrupted-turn
// recovery settle the question before it claims. Claiming first and asking
// afterwards costs a durable commit to remove the head and a second to put it
// back, and between them the durable queue is observably missing an entry that
// is about to return -- which a turn/promoteQueuedAsSteer can sample and commit
// against (kata 9f5x).
func interruptDrainConfig(ctx context.Context, err error) (queuedInputDrainConfig, bool) {
	cfg, ok := ctx.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok || cfg.rootCtx == nil {
		return queuedInputDrainConfig{}, false
	}
	isAbort := isAbortError(err)
	// A bare context.Canceled is this turn's own cancellation and always drains.
	// An *AbortError wraps a cancellation that may have come from a sub-operation,
	// so it drains only when THIS turn's context was the one canceled. Post
	// honest-Unwrap an *AbortError satisfies errors.Is(_, context.Canceled), so
	// the abort case is discriminated explicitly to preserve that distinction.
	bareCanceled := errors.Is(err, context.Canceled) && !isAbort
	drainable := bareCanceled || (isAbort && errors.Is(ctx.Err(), context.Canceled))
	if !drainable || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return queuedInputDrainConfig{}, false
	}
	if cfg.rootCtx.Err() != nil {
		return queuedInputDrainConfig{}, false
	}
	return cfg, true
}

// nextTurnContext returns the context the drained queue head runs under. It
// carries the side effect of announcing the new turn to the host (cmd/evener's
// handler publishes processing state and registers the turn's cancel), so it
// runs only once the head is really claimed.
//
// It is total, and the totality is load-bearing: the head is already claimed
// when this is called, so a failure here would have to put the entry back, and
// that restore is the second durable commit this path exists to avoid. A
// handler that installs no factory already drained under the root context
// (WithQueuedInputDrainOnInterrupt); a factory that declines to build one now
// does the same, which is the only answer that keeps the claim honest.
func (cfg queuedInputDrainConfig) nextTurnContext() context.Context {
	if cfg.nextCtx == nil {
		return cfg.rootCtx
	}
	next, _ := cfg.nextCtx(cfg.rootCtx)
	if next == nil {
		return cfg.rootCtx
	}
	return next
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

// popSteeringHead removes and returns the next steering message. The second
// result is false when the queue is empty. Daemon steering persists the shrunk
// queue before returning, mirroring popQueueHead (input queue):
// injectDrainedSteering consumes the steering batch one message at a time so
// the persisted queue shrinks as each message is durably recorded, bounding a
// mid-drain crash's loss to the single in-flight message rather than the whole
// batch.
//
// A client steer writes nothing here: the store keeps it accepted until its
// transcript append lands and consumeSteeringMessage finalizes it, so the
// transcript is the only record of whether it was delivered. Between the pop
// and that finalization the steer is in flight (steeringInFlight), which is
// what keeps reflectDurableClientSteering from putting it back in the queue.
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
	if entry.ClientMutationID != "" {
		if s.steeringInFlight == nil {
			s.steeringInFlight = map[string]string{}
		}
		s.steeringInFlight[entry.ClientMutationID] = ""
		s.mu.Unlock()
		return entry, true
	}
	s.mu.Unlock()
	s.persistQueuesSnapshot()
	return entry, true
}

// steeringLanded ends a client steer's in-flight window: it is either
// incorporated (gone from the store) or, when its append failed, still
// accepted there, and the reflect that follows puts an accepted steer back in
// the queue at its place in the order.
func (s *Session) steeringLanded(clientMutationID string) {
	s.mu.Lock()
	delete(s.steeringInFlight, clientMutationID)
	s.mu.Unlock()
	s.reflectDurableClientSteering()
}

// markSteeringRecorded notes that an in-flight steer's transcript append landed
// and only the store's write did not: recorded, awaiting its mark, which is
// the terminal state the transcript entry stands for ("incorporated" for a
// steering turn, "failed" for a selection failure).
func (s *Session) markSteeringRecorded(clientMutationID, terminalState string) {
	s.mu.Lock()
	s.steeringInFlight[clientMutationID] = terminalState
	s.mu.Unlock()
}

// reconcileRecordedSteering writes the store marks recorded in-flight steers
// are waiting for, through the steering table (rule 0), and ends their
// in-flight windows. A no-op, and no store write, when none is waiting.
func (s *Session) reconcileRecordedSteering() {
	ids, recorded := s.recordedSteeringAwaitingMark()
	if len(ids) == 0 || s.clientMutations == nil {
		return
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		reconcileClientSteering(snapshot, recorded, false)
		return nil
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("mark recorded steering incorporated: %v; the transcript holds it", err)})
		return
	}
	s.steeringMarked(ids)
}

// parkSteering records a failed attempt at running pending user steering (see
// Session.steeringParked); the next wake sender unparks.
func (s *Session) parkSteering() {
	s.mu.Lock()
	s.steeringParked = true
	s.mu.Unlock()
}

// unparkSteering clears Session.steeringParked: an attempt at running user
// steering succeeded (whatever turn made it), so the park is over.
func (s *Session) unparkSteering() {
	s.mu.Lock()
	s.steeringParked = false
	s.mu.Unlock()
}

// steeringParkedNow reports whether user steering is parked: a failed attempt
// left Session.steeringParked set and the steer is still queued. Every
// autonomous path -- the drain ladder's notification and goal rungs, the
// deferred continuation, the settle's goal kick, the entry gate for a
// daemon-started notification or continuation -- asks this and stands down.
func (s *Session) steeringParkedNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steeringParked && s.hasPendingUserSteeringLocked()
}

// steeringInFlightSample copies the in-flight set under s.mu, for a caller
// about to decide steering eligibility inside the store's serializer (which
// must never wait on s.mu).
func (s *Session) steeringInFlightSample() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.steeringInFlight)
}

// steeringMarked ends the in-flight window of steers a Stop's finalization
// just marked incorporated (recordedSteeringAwaitingMark's sample).
func (s *Session) steeringMarked(ids []string) {
	for _, id := range ids {
		s.steeringLanded(id)
	}
}

// recordedSteeringAwaitingMark samples, under s.mu, the in-flight steers whose
// transcript append landed but whose incorporation write the store refused,
// and returns the question over that sample for reconcileClientSteering.
// Taken before a store mutate, never inside one.
func (s *Session) recordedSteeringAwaitingMark() (ids []string, recorded func(string) string) {
	states := map[string]string{}
	s.mu.Lock()
	for id, state := range s.steeringInFlight {
		if state != "" {
			states[id] = state
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	return ids, func(id string) string { return states[id] }
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
//
// It reports whether this pass delivered anything: at least one steering turn
// is now in the transcript that no model request has read. A pass that only
// retired failed selections, or drained nothing, delivered nothing.
func (s *Session) injectDrainedSteering() (delivered bool) {
	for range s.peekSteeringForTurn() {
		msg, ok := s.popSteeringHead()
		if !ok {
			break
		}
		switch s.consumeSteeringMessage(msg) {
		case steeringAppendFailed:
			// The failed steer is back at the head of the queue; popping again
			// would take the same steer and fail the same way inside this
			// turn. The next external wake owns the next attempt, for this
			// steer and for everything queued behind it.
			return delivered
		case steeringDelivered:
			delivered = true
		}
	}
	return delivered
}

// steeringConsumption is what consumeSteeringMessage did with one message.
type steeringConsumption int

const (
	// steeringAppendFailed: the transcript refused the append; a client steer
	// is back in the queue, accepted, and the drain must stop here.
	steeringAppendFailed steeringConsumption = iota
	// steeringDelivered: a steering turn is in the transcript.
	steeringDelivered
	// steeringRetired: the steer's skill selection could not be prepared; the
	// failure is recorded and the steer retired. Nothing reached the model.
	steeringRetired
)

// consumeSteeringMessage durably records one drained steering message as a
// steering turn (history + transcript) and emits the steering-injected
// event. Factored out of injectDrainedSteering's loop body so a test can
// drive the exact per-message consumption step the production loop uses,
// rather than reimplementing it, when pinning the crash-window behavior
// documented above.
func (s *Session) consumeSteeringMessage(msg steeringMessage) steeringConsumption {
	// A skill-bearing steering message is prepared at actual consumption, as
	// one atomic group tied to the steering's durable identity. A failed
	// preparation delivers NONE of the steering input to the in-flight turn --
	// not the bodies and not the prose -- and is never downgraded to text; a
	// prominent failure record persists before the pending execution clears.
	var selectionBatch *skillActivationBatch
	var selectionRecord *schema.SkillInputRecord
	if len(msg.SkillNames) > 0 {
		queued := queuedInputFromSteering(msg)
		selectionRecord = skillInputRecordFromQueued(queued)
		batch, prepareErr := s.prepareSelectedInput(context.Background(), queued, "user_selection")
		if prepareErr != nil {
			if !s.recordFailedSteeringSelection(msg, prepareErr) {
				return steeringAppendFailed
			}
			return steeringRetired
		}
		recordPreparedSelection(selectionRecord, batch)
		selectionBatch = batch
	}
	// Persist the steering's attachments before its turn is built so the
	// message can name their durable paths (agent/image_persist.go).
	msg.Images = s.persistInputImages(msg.Images)
	t := schema.NewTurn(schema.TurnSteering, steeringMessageToLLM(msg))
	t.SteeringSource = msg.Source
	t.SteeringKind = msg.Kind
	t.ClientMutationID = msg.ClientMutationID
	t.StableTurnID = msg.StableTurnID
	if len(msg.SkillNames) > 0 {
		t.SkillState = &schema.SkillTurnState{Input: selectionRecord}
	}
	if s.clientMutations != nil {
		// ActiveTurnID is the actual logical owner at delivery time. For an
		// inline steer it is the already-running turn; for a carrier it is the
		// carrier's reserved mutation turn. Both identities must be durable so
		// replay can distinguish the two boundaries. System steering uses the
		// same active turn when it is drained by a named daemon turn.
		t.OwningTurnID = s.clientMutations.snapshot().ActiveTurnID
	}
	if msg.ClientMutationID != "" {
		if err := s.appendTurnAfterTranscriptWrite(
			t,
			func() error { return s.appendClientMutationTranscriptLocked(t) },
			func() { s.history = append(s.history, t) },
		); err != nil {
			// The steer did not land. The store never left accepted, so ending
			// its in-flight window puts it back in the queue, parked runnable:
			// hasRunnableUserSteering keeps the session from resting, and the
			// next external wake -- attach, or any accepted client mutation --
			// carries it. Nothing here re-arms a wake: a write that keeps
			// failing without poisoning the writer (ENOSPC before a byte
			// lands) would otherwise loop failed carrier turns with no pause,
			// and a paced retry is not this design's answer.
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
			s.steeringLanded(msg.ClientMutationID)
			s.parkSteering()
			return steeringAppendFailed
		}
		if err := s.finalizeIncorporatedSteering(msg.ClientMutationID); err != nil {
			// Recorded, so delivered: the transcript holds the steer and the
			// model reads it. Only the store's incorporation mark is missing;
			// the steer stays in flight, marked recorded, so no reflect
			// re-queues it and no carrier claims it, and the input's settle,
			// the next wake, a Stop or restore writes the mark through the
			// steering table (reconcileRecordedSteering).
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("steering incorporation failed: %v; the transcript holds the steer", err)})
			s.markSteeringRecorded(msg.ClientMutationID, "incorporated")
		} else {
			s.steeringLanded(msg.ClientMutationID)
		}
		// The clear must land before the event publishes: the server refreshes
		// its ask facet on EventSteeringInjected (server/thread_envelope.go),
		// so emitting first lets that refresh read a stale askPending=true
		// until the next ask change (RoboRev #1806 member-3 Medium). Only the
		// clear moves ahead of the emit -- admit/unpark stay after it, their
		// original order, so a skill-admission failure's own EventWarning
		// still publishes after EventSteeringInjected rather than before it.
		s.clearAskPendingForResolvingSteer(t)
		if hook := s.cfg.testOnly.beforeSteeringInjectedPublish; hook != nil {
			hook()
		}
		s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
		s.admitPreparedSkillSelection(selectionBatch)
		s.unparkSteering()
		return steeringDelivered
	}
	s.recordTurn(t, t)
	// Same ordering requirement as the client-mutation branch above: clear
	// before the event that triggers the server's ask-facet refresh, admit
	// after it (unchanged order).
	s.clearAskPendingForResolvingSteer(t)
	if hook := s.cfg.testOnly.beforeSteeringInjectedPublish; hook != nil {
		hook()
	}
	s.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	s.admitPreparedSkillSelection(selectionBatch)
	return steeringDelivered
}

// queuedInputFromSteering projects a steering message into the queuedInput
// shape prepareSelectedInput consumes, keeping the durable ownership identity.
func queuedInputFromSteering(msg steeringMessage) queuedInput {
	return queuedInput{
		ClientMutationID: msg.ClientMutationID,
		StableTurnID:     msg.StableTurnID,
		Text:             msg.Text,
		Images:           msg.Images,
		SkillNames:       msg.SkillNames,
	}
}

// setSteeringCarrierClaimDrain records which steer (by client mutation id) a
// claimed steering-carrier turn (acceptSteeringCarrierInput) is currently
// draining, for steeringSelectionFailureIsCarrierClaim below to read; ""
// clears it once the drain returns.
func (s *Session) setSteeringCarrierClaimDrain(clientMutationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steeringCarrierClaimClientMutationID = clientMutationID
}

// steeringSelectionFailureIsCarrierClaim reports whether clientMutationID is
// the steer a claimed steering-carrier turn is currently draining
// (setSteeringCarrierClaimDrain above) — the turn whose mere acceptance
// cleared askPending before this selection failure ran, PROVIDED that steer
// itself answers the ask (steeringCarrierClaimAnswersAsk): the caller
// (recordFailedSteeringSelection) checks both before tagging the TurnFailure
// a resolution boundary (schema.TurnFailureInfo.SteeringCarrier) — a
// human-note carrier's entry clear left askPending set, so this alone is not
// sufficient.
func (s *Session) steeringSelectionFailureIsCarrierClaim(clientMutationID string) bool {
	if clientMutationID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steeringCarrierClaimClientMutationID == clientMutationID
}

// recordFailedSteeringSelection durably records a steering input whose skill
// selection failed preparation, then clears its pending execution. The
// prominent failure record lands BEFORE the clear, keeping both the names and
// the original prose for correction and an explicit new-intent retry; a crash
// between the two leaves the pending execution for restore to settle against
// the already-persisted failure turn. The in-flight turn keeps running and
// receives none of this steering input.
// recordFailedSteeringSelection records a steer whose skill selection could
// not be prepared as a failure turn and retires it from the store. It reports
// whether the failure landed: false means the record itself could not be
// appended, the steer is back in the queue for the next drain, and the caller
// must stop draining (popping again would take the same steer).
func (s *Session) recordFailedSteeringSelection(msg steeringMessage, cause error) bool {
	input := queuedInputFromSteering(msg)
	turn := schema.NewTurn(schema.TurnFailure, llm.System(cause.Error()))
	turn.ClientMutationID = msg.ClientMutationID
	turn.StableTurnID = msg.StableTurnID
	// Tagged only when this IS the claimed carrier's own steer (
	// steeringSelectionFailureIsCarrierClaim) AND that steer answers the ask
	// (steeringCarrierClaimAnswersAsk, the same journal-kind check the entry
	// clear uses): a human-note carrier's entry clear already left askPending
	// set, so its failure must not be a resolution boundary either.
	steeringCarrier := s.steeringSelectionFailureIsCarrierClaim(msg.ClientMutationID) &&
		s.steeringCarrierClaimAnswersAsk(queuedClientMutationIdentity{ClientMutationID: msg.ClientMutationID, SteeringCarrier: true})
	turn.Error = &schema.TurnFailureInfo{
		Message:         cause.Error(),
		SteeringCarrier: steeringCarrier,
	}
	turn.SkillState = &schema.SkillTurnState{Input: skillInputRecordFromQueued(input)}
	if err := s.appendTurnAfterTranscriptWrite(
		turn,
		func() error { return s.appendClientMutationTranscriptLocked(turn) },
		func() { s.history = append(s.history, turn) },
	); err != nil {
		// The prominent record did not persist. Put the steering back so a
		// later drain retries the failure instead of silently dropping it: a
		// client steer lands like any failed append (still accepted in the
		// store, so the reflect re-queues it at its place in the order);
		// daemon steering has no store and goes back to the head directly.
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("persist failed steering selection: %v", err)})
		if msg.ClientMutationID != "" {
			s.steeringLanded(msg.ClientMutationID)
			s.parkSteering()
		} else {
			s.mu.Lock()
			s.steeringQueue = append([]steeringMessage{msg}, s.steeringQueue...)
			s.mu.Unlock()
		}
		return false
	}
	s.emit(events.EventError, errorDataFromError(cause))
	if msg.ClientMutationID == "" {
		return true
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		record, ok := snapshot.Journal[msg.ClientMutationID]
		if !ok {
			return nil
		}
		record.OperationState = clientMutationOperationTerminal
		record.ExecutionState = "failed"
		record.ProjectionState = appwire.MutationProjectionReflected
		record.Payload = nil
		snapshot.Journal[msg.ClientMutationID] = record
		delete(snapshot.PendingExecutions, msg.ClientMutationID)
		removeClientMutationSteeringOrder(snapshot, msg.ClientMutationID)
		return nil
	}); err != nil {
		// The failure is in the transcript and the retirement is not in the
		// store: recorded, unmarked, and reconciled like an incorporation
		// write the store refused.
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("clear failed steering execution: %v", err)})
		s.markSteeringRecorded(msg.ClientMutationID, "failed")
		return true
	}
	s.steeringLanded(msg.ClientMutationID)
	s.unparkSteering()
	return true
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
	t.OwningTurnID = s.activeTurnOwner()
	s.recordTurn(t, t)
	s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: text, Kind: kind})
}

func (s *Session) activeTurnOwner() string {
	if s.clientMutations == nil {
		return ""
	}
	return s.clientMutations.snapshot().ActiveTurnID
}

// appendSteeringTurnDurably records a daemon steering turn through the synced
// door for the current active-turn owner: job notifications commit their
// delivery (markJobNotificationsDelivered) on the strength of this record, so
// it must be durable, not merely recorded, before that commit (spec §4.3). It
// therefore refuses a pre-attach held turn; its production caller
// (acceptNotificationInput) always runs after attach.
func (s *Session) appendSteeringTurnDurably(text, kind string) error {
	return s.appendSteeringTurnDurablyForOwner(text, kind, s.activeTurnOwner())
}

// appendSteeringTurnDurablyForOwner durably records a daemon steering turn
// with the logical turn that owns it. Notification reminders use the caller's
// supplied turn id because client steering arriving during that notification
// turn is grouped by the same durable owner.
func (s *Session) appendSteeringTurnDurablyForOwner(text, kind, owningTurnID string) error {
	t := schema.NewTurn(schema.TurnSteering, llm.User(text))
	t.SteeringKind = kind
	t.OwningTurnID = owningTurnID
	err := s.appendTurnAfterTranscriptWrite(
		t,
		func() error { return s.writeTranscriptSyncedLocked(t) },
		func() { s.history = append(s.history, t) },
	)
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	return err
}

func (s *Session) hasPendingSteering() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.steeringQueue) > 0
}

// hasRunnableUserSteering reports whether user steering is pending that the
// daemon will run on its own: a wake carries it, or the next turn drains it.
// A steer parked by a Stop (SteeringHeld) is not that -- it waits on the user
// (issue #174) -- so it counts neither as work to wake for nor as autonomy
// that keeps the session from resting awaiting.
func (s *Session) hasRunnableUserSteering() bool {
	if s.clientMutations != nil && s.clientMutations.steeringHeld() {
		return false
	}
	return s.hasPendingUserSteering()
}

// hasPendingUserSteering reports whether any queued steering came from the
// human rather than the daemon. Only that kind justifies starting a turn to
// carry it: daemon-authored steering -- the current-task reminder, hook
// context, a transcript pointer -- is context for whatever turn happens next,
// and a turn started to deliver it would take the session's turn identity and
// refuse the user's own first message with "turn is already active".
func (s *Session) hasPendingUserSteering() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasPendingUserSteeringLocked()
}

// hasPendingUserSteeringLocked is hasPendingUserSteering for a caller holding
// s.mu.
func (s *Session) hasPendingUserSteeringLocked() bool {
	for _, msg := range s.steeringQueue {
		if msg.Source == events.SteeringSourceUser {
			return true
		}
	}
	return false
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
	Text       string            // the steering message text
	Images     []ImageAttachment // any images attached to the steering message
	SkillNames []string          // canonical skill identities selected with the steering
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
		out[i] = SteeringEntry{Text: entry.Text, Images: copyImages, SkillNames: append([]string(nil), entry.SkillNames...)}
	}
	return out
}

// steeringMessageToLLM converts a steeringMessage to an llm.Message
// (User-role) suitable for appendTurn(TurnSteering, ...). Text-only
// entries become llm.User(text); image-bearing entries become a
// multi-part message via buildUserInputMessage. A selection-only entry
// renders the bracketed selection marker so the steering turn is never
// an empty user message.
func steeringMessageToLLM(entry steeringMessage) llm.Message {
	msg := buildSelectedUserInputMessage(entry.Text, entry.Images, entry.SkillNames)
	if entry.Kind != events.SteeringKindNotification {
		return msg
	}
	// Notification-kind steering (e.g. the cancelled-callback-watches
	// restart notice) is session machinery: every text part is
	// session-authored, so flag them at construction for the display-side
	// filter.
	for i := range msg.Content {
		if msg.Content[i].Kind == llm.ContentText {
			msg.Content[i].Machinery = true
		}
	}
	return msg
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
