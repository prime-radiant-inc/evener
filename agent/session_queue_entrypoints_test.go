package agent

// Test-only entry points over the client-mutation handlers. Production input
// arrives through the AcceptClientMutation* methods; these wrappers mint a
// client mutation id and drive the same handlers so tests can queue, steer,
// promote and cancel without building wire params by hand.

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

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
	_, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		Ref:              s.ID(),
		ClientMutationID: "legacy_" + newQueueEntryID(),
		Input:            clientMutationInput(msg, images, nil),
	})
	return err
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
	_, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
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
	_, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
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
// leaving the queue untouched — when the session is closed, index is out of
// range, or the id mismatches, so a failed promote never silently loses or
// swaps the follow-up. Unlike steer, no turn in flight is required: a queue
// a Stop parked still offers the queue strip's "run this now", and the
// promoted message becomes pending steering for the next turn.
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
	s.mu.Unlock()
	_, err := s.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{
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
	response, err := s.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{
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

// WithQueuedInputDrainOnInterrupt marks ctx as a per-turn interrupt context
// whose queued input may continue under rootCtx after ctx is canceled.
func WithQueuedInputDrainOnInterrupt(ctx context.Context, rootCtx context.Context) context.Context {
	return WithQueuedInputDrainOnInterruptHandler(ctx, rootCtx, nil)
}

// DroppedModelFallbacksFromLastSwitch returns the cfg.ModelFallbacks entries
// dropped by the most recent SetModel call's post-switch revalidation (nil if
// none were dropped, or no switch has occurred yet). A later marker (Task 5)
// surfaces this as a warning line; this method only exposes the names.
func (s *Session) DroppedModelFallbacksFromLastSwitch() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDroppedModelFallbacks
}

// RestoreSessionFromMeta creates a Session from a SessionMeta, recovering
// history exclusively from the transcript JSONL. If no transcript exists,
// the session starts with empty history (no snapshot fallback).
func RestoreSessionFromMeta(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, meta schema.SessionMeta, stateDir string) (*Session, error) {
	return RestoreSessionFromMetaWithConfig(client, profile, env, meta, RestoreSessionConfig{StateDir: stateDir})
}

func clientMutationInput(text string, images []ImageAttachment, skillNames []string, commandNames ...[]string) []appwire.InputItem {
	return clientMutationInputWithMentions(text, images, skillNames, nil, commandNames...)
}
