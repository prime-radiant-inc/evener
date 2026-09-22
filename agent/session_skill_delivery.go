package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// skillDeliveryCommit is one final-admission transaction: the lifecycle
// revision it was planned against, the causal outcomes it records, and the
// invocation identities whose delivery obligations it satisfies.
type skillDeliveryCommit struct {
	Revision               uint64
	Outcomes               []schema.SkillActivationOutcome
	SatisfiedInvocationIDs []string

	// appendedNotifications reports that prepare appended typed activation
	// notification turns to the real history — restored carriers AND failure
	// explanations alike; the caller rebuilds the request so every appended
	// notification joins this dispatch.
	appendedNotifications bool
}

// completeSkillContentPresent reports whether the request carries one complete
// typed skill-context envelope whose canonical name, source, and rendered
// digest match identity exactly. It reads only well-formed envelope carriers;
// it never infers activation intent from arbitrary user text.
func completeSkillContentPresent(req llm.Request, identity schema.SkillContentIdentity) bool {
	if identity.Name == "" || identity.RenderedDigest == "" {
		return false
	}
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			var content string
			switch part.Kind {
			case llm.ContentText:
				content = part.Text
			case llm.ContentToolResult:
				if part.ToolResult == nil {
					continue
				}
				content, _ = part.ToolResult.Content.(string)
			default:
				continue
			}
			if envelopeCarriesIdentity(content, identity) {
				return true
			}
		}
	}
	return false
}

// completeSkillContentInTurns reports whether the retained history itself
// carries one complete typed skill-context envelope whose canonical name,
// source, and rendered digest match identity exactly — the reuse check for
// content that survived a fold in the retained tail or was restored by
// another activation. Like completeSkillContentPresent it reads only
// well-formed envelope carriers, never inferring intent from arbitrary text.
func completeSkillContentInTurns(turns []schema.Turn, identity schema.SkillContentIdentity) bool {
	if identity.Name == "" || identity.RenderedDigest == "" {
		return false
	}
	for _, turn := range turns {
		for _, part := range turn.Message.Content {
			var content string
			switch part.Kind {
			case llm.ContentText:
				content = part.Text
			case llm.ContentToolResult:
				if part.ToolResult == nil {
					continue
				}
				content, _ = part.ToolResult.Content.(string)
			default:
				continue
			}
			if envelopeCarriesIdentity(content, identity) {
				return true
			}
		}
	}
	return false
}

// envelopeCarriesIdentity scans content for complete skill-context envelopes
// and reports whether one matches identity's name, source, and rendered digest.
func envelopeCarriesIdentity(content string, identity schema.SkillContentIdentity) bool {
	const open, closeTag = "<skill-context>\n", "\n</skill-context>"
	rest := content
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return false
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, closeTag)
		if j < 0 {
			return false
		}
		raw := open + rest[:j] + closeTag
		var document struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal([]byte(rest[:j]), &document); err == nil &&
			document.Name == identity.Name && document.Source == identity.Source {
			digest := sha256.Sum256([]byte(raw))
			if hex.EncodeToString(digest[:]) == identity.RenderedDigest {
				return true
			}
		}
		rest = rest[j+len(closeTag):]
	}
}

// skillTurnStateForToolRound builds the round's typed skill state from the
// results' use_skill tool-state side channels and typed errors. A failed or
// output-shaped execution records a failed outcome and never an obligation:
// failed output shaping cannot leave a pending-success inventory record.
func (s *Session) skillTurnStateForToolRound(results []tool.ExecResult) *schema.SkillTurnState {
	var state *schema.SkillTurnState
	for _, res := range results {
		if res.ToolName != "use_skill" {
			continue
		}
		var recorded skillToolState
		if len(res.ToolState) > 0 {
			if err := json.Unmarshal(res.ToolState, &recorded); err != nil {
				continue
			}
		}
		if state == nil {
			state = &schema.SkillTurnState{}
		}
		if !res.IsError {
			if recorded.Outcome.InvocationID == "" {
				continue
			}
			state.Outcomes = append(state.Outcomes, recorded.Outcome)
			if recorded.Obligation != nil {
				state.Obligations = append(state.Obligations, *recorded.Obligation)
			}
			continue
		}
		// Failure: shape the typed outcome from the tool-state identity when
		// present, else from the typed activation error riding res.Err.
		outcome := recorded.Outcome
		if outcome.InvocationID == "" {
			if activationErr, ok := errors.AsType[*skillActivationError](res.Err); ok {
				outcome.InvocationID = activationErr.Invocation.InvocationID
				outcome.ToolCallID = activationErr.Invocation.ToolCallID
				outcome.ClientMutationID = activationErr.Invocation.ClientMutationID
				outcome.Identity = schema.SkillContentIdentity{Name: activationErr.Invocation.Name}
			} else {
				outcome.ToolCallID = res.CallID
			}
		}
		outcome.SessionID = s.id
		outcome.Status = "failed"
		outcome.ErrorCode = skillToolErrorCode(res.Err)
		state.Outcomes = append(state.Outcomes, outcome)
	}
	return state
}

// skillToolErrorCode maps a use_skill execution failure to its typed outcome
// code: the registry's complete-or-fail shaping, else the typed activation
// failure, else a generic metadata failure.
func skillToolErrorCode(err error) string {
	if errors.Is(err, tool.ErrCompleteOutputExceedsLimit) {
		return "output_limit"
	}
	return skillActivationErrorCode(err)
}

// withoutObligationsByInvocationID returns obligations minus every entry whose
// InvocationID appears in added, preserving order and reusing the backing
// array. Every rollback path that undoes an unpersisted obligation append uses
// it, so memory and the durable snapshot agree after a failed save.
func withoutObligationsByInvocationID(obligations, added []schema.SkillDeliveryObligation) []schema.SkillDeliveryObligation {
	if len(added) == 0 {
		return obligations
	}
	drop := make(map[string]bool, len(added))
	for _, obligation := range added {
		drop[obligation.InvocationID] = true
	}
	kept := obligations[:0]
	for _, obligation := range obligations {
		if !drop[obligation.InvocationID] {
			kept = append(kept, obligation)
		}
	}
	return kept
}

// persistSkillToolObligations records the round's new delivery obligations in
// the lifecycle snapshot and saves it before a retry or restart can lose them.
// A save failure is surfaced to the caller AND rolled back out of the live
// snapshot, so memory and the durable snapshot agree — both lack the
// obligations — and nothing half-admits. That rollback is what lets the caller
// refuse to publish a carrier for obligations that were not persisted.
func (s *Session) persistSkillToolObligations(state *schema.SkillTurnState) error {
	if state == nil || len(state.Obligations) == 0 {
		return nil
	}
	s.mu.Lock()
	s.skillLifecycle.Obligations = append(s.skillLifecycle.Obligations, state.Obligations...)
	s.skillLifecycle.Revision++
	s.mu.Unlock()
	if err := s.saveMeta(); err != nil {
		s.mu.Lock()
		s.skillLifecycle.Obligations = withoutObligationsByInvocationID(s.skillLifecycle.Obligations, state.Obligations)
		s.skillLifecycle.Revision++
		s.mu.Unlock()
		s.emit(events.EventWarning, warningDataFromError("saving skill delivery obligations failed", err))
		return err
	}
	return nil
}

// recordSkillCarrierDurably records one skill carrier turn — the turn whose
// SkillState carries the complete instructions a delivery obligation protects,
// or the typed explanation the model is owed — through the durable transcript
// door. The write is fsynced BEFORE the turn joins the live history, and a
// failed write leaves the history untouched and returns the error, so callers
// keep their obligations and receipts pending instead of advancing as if the
// body had been recorded. recordTurn's non-durable, error-swallowing append is
// the wrong door here: this turn may be the only copy of a skill's complete
// instructions.
func (s *Session) recordSkillCarrierDurably(live, persisted schema.Turn) error {
	live.SkillState = live.SkillState.Clone()
	persisted.SkillState = persisted.SkillState.Clone()
	err := s.appendTurnAfterTranscriptWrite(
		persisted,
		func() error { return s.writeTranscriptSyncedLocked(persisted) },
		func() { s.history = append(s.history, live) },
	)
	if err != nil {
		s.emit(events.EventWarning, warningDataFromError("recording a skill carrier turn failed", err))
	}
	return err
}

// recordSkillDeliveryNotification appends one typed activation notification to
// the real history: the model-facing carrier content plus the causal outcome
// and obligation, linked to the original invocation. Continuation
// re-expansion sees it because it is an ordinary recorded turn.
func (s *Session) recordSkillDeliveryNotification(message llm.Message, outcome schema.SkillActivationOutcome, obligation schema.SkillDeliveryObligation) error {
	turn := schema.NewTurn(schema.TurnSystem, message)
	turn.SkillState = &schema.SkillTurnState{
		Outcomes:    []schema.SkillActivationOutcome{outcome},
		Obligations: []schema.SkillDeliveryObligation{obligation},
	}
	return s.recordSkillCarrierDurably(turn, turn)
}

// skillDeliveryMessage builds the model-facing carrier for a skill's
// complete content. A non-empty note (the changed-on-disk explanation)
// prefixes the body inside one machinery-flagged part: the carrier is a
// TurnSystem notification the user never typed, and a single part keeps the
// wire text byte-identical to the former note+"\n\n"+body concatenation on
// every adapter — including ones that join separate text parts with a
// separator (chatcompletions' textFromParts).
func skillDeliveryMessage(note, body string) llm.Message {
	if note == "" {
		return llm.User(body)
	}
	return llm.UserMachinery(note + "\n\n" + body)
}

// finalizeSkillDeliveryFailure drops the obligation for an invocation whose
// delivery failed permanently (reload failure, budget rejection). The failed
// outcome is already recorded on its notification turn; the inventory keeps
// any earlier successful record.
func (s *Session) finalizeSkillDeliveryFailure(invocationIDs ...string) {
	if len(invocationIDs) == 0 {
		return
	}
	drop := map[string]bool{}
	for _, id := range invocationIDs {
		drop[id] = true
	}
	s.mu.Lock()
	kept := s.skillLifecycle.Obligations[:0]
	for _, obligation := range s.skillLifecycle.Obligations {
		if !drop[obligation.InvocationID] {
			kept = append(kept, obligation)
		}
	}
	s.skillLifecycle.Obligations = kept
	s.skillLifecycle.Revision++
	s.mu.Unlock()
}

// prepareSkillDelivery revalidates every pending delivery obligation against
// the actual outgoing request. An obligation whose complete content is still
// present is left for the dispatch-seam commit. One whose carrier left the
// shape (fold, projection, restore) is reloaded from its recorded source under
// the invocation's route policy and re-admitted through a typed causal
// activation notification appended to the real history; the caller rebuilds
// the request when commit.appendedNotifications is set. A reload failure
// records an explicit failed outcome and finalizes the obligation — never a
// false delivery — and its explanation is an appended notification too, so
// the model hears it in the dispatch that finalizes the obligation. It never
// starts another compact/reload cycle.
func (s *Session) prepareSkillDelivery(ctx context.Context, profile *provider.Profile, turns []schema.Turn, req llm.Request) (llm.Request, skillDeliveryCommit, error) {
	_ = profile
	_ = turns
	s.mu.Lock()
	if len(s.skillLifecycle.Obligations) == 0 {
		commit := skillDeliveryCommit{Revision: s.skillLifecycle.Revision}
		s.mu.Unlock()
		return req, commit, nil
	}
	obligations := append([]schema.SkillDeliveryObligation(nil), s.skillLifecycle.Obligations...)
	s.mu.Unlock()

	commit := skillDeliveryCommit{}
	var failed []string
	for _, obligation := range obligations {
		if err := ctx.Err(); err != nil {
			return req, commit, err
		}
		if completeSkillContentPresent(req, obligation.Identity) {
			continue
		}
		source := obligation.Identity
		invocation := skillInvocation{
			Name:             obligation.Identity.Name,
			Route:            obligation.Route,
			InvocationID:     obligation.InvocationID,
			ToolCallID:       obligation.ToolCallID,
			ClientMutationID: obligation.ClientMutationID,
			AtomicGroupID:    obligation.AtomicGroupID,
			Source:           &source,
		}
		batch, err := s.prepareSkillActivations(ctx, []skillInvocation{invocation})
		if err != nil {
			outcome := schema.SkillActivationOutcome{
				SessionID:        s.id,
				InvocationID:     obligation.InvocationID,
				ToolCallID:       obligation.ToolCallID,
				ClientMutationID: obligation.ClientMutationID,
				Identity:         obligation.Identity,
				Status:           "failed",
				ErrorCode:        skillActivationErrorCode(err),
			}
			if err := s.recordSkillDeliveryNotification(
				llm.UserMachinery(systemNotificationf("Skill %q is no longer available from %s: %v", obligation.Identity.Name, obligation.Identity.Source, err)),
				outcome, obligation); err != nil {
				return req, commit, err
			}
			failed = append(failed, obligation.InvocationID)
			commit.appendedNotifications = true
			continue
		}
		item := batch.Items[0]
		newIdentity := skillContentIdentity(item)
		s.mu.Lock()
		prior := s.skillLifecycle.Inventory[obligation.Identity.Name].Ordinary
		s.mu.Unlock()
		activation := ordinaryActivationRecord(item, prior)
		outcome := schema.SkillActivationOutcome{
			SessionID:        s.id,
			InvocationID:     obligation.InvocationID,
			ToolCallID:       obligation.ToolCallID,
			ClientMutationID: obligation.ClientMutationID,
			Identity:         newIdentity,
			Activation:       &activation,
			Status:           "delivered",
		}
		content := item.Rendered.Content
		var note string
		if newIdentity != obligation.Identity {
			// Changed disk content is reported explicitly; this notification
			// supersedes the earlier provisional outcome, so it records the
			// identity and controls the model last saw alongside the new ones.
			previousIdentity := obligation.Identity
			outcome.PreviousIdentity = &previousIdentity
			if prior != nil {
				previousControls := prior.Controls
				outcome.PreviousControls = &previousControls
			}
			note = systemNotificationf("Skill %q changed on disk since its earlier activation; the complete current instructions follow.", obligation.Identity.Name)
		}
		corrected := obligation
		corrected.Identity = newIdentity
		if err := s.recordSkillDeliveryNotification(skillDeliveryMessage(note, content), outcome, corrected); err != nil {
			// The body was not durably recorded, so the obligation must keep the
			// identity the dispatch still has to satisfy; the next attempt
			// re-prepares and re-records it.
			return req, commit, err
		}
		// Carry the corrected identity forward so the final admission checks
		// the bytes this dispatch actually delivers.
		s.mu.Lock()
		for i := range s.skillLifecycle.Obligations {
			if s.skillLifecycle.Obligations[i].InvocationID == obligation.InvocationID {
				s.skillLifecycle.Obligations[i].Identity = newIdentity
			}
		}
		s.skillLifecycle.Revision++
		s.mu.Unlock()
		commit.appendedNotifications = true
	}
	if len(failed) > 0 {
		s.finalizeSkillDeliveryFailure(failed...)
	}
	if commit.appendedNotifications {
		// Obligation identity corrections and failure finalizations must be
		// durable before a retry or restart can lose them.
		if err := s.saveMeta(); err != nil {
			s.emit(events.EventWarning, warningDataFromError("saving skill delivery state failed", err))
			return req, commit, err
		}
	}
	s.mu.Lock()
	commit.Revision = s.skillLifecycle.Revision
	s.mu.Unlock()
	return req, commit, nil
}

// deliveryActivationRecordLocked rebuilds the inventory record for a final
// admission from the current catalog, falling back to the prior record's
// metadata when the source no longer resolves (a continuation of an exact
// recorded source). Caller holds s.mu.
func (s *Session) deliveryActivationRecordLocked(obligation schema.SkillDeliveryObligation, prior *schema.OrdinarySkillActivation) *schema.OrdinarySkillActivation {
	description := ""
	controls := schema.SkillInvocationControls{}
	if prior != nil && prior.Identity.Name == obligation.Identity.Name && prior.Identity.Source == obligation.Identity.Source {
		description = prior.Description
		controls = prior.Controls
	}
	if descriptor, err := s.skills.ResolveExact(obligation.Identity.Name); err == nil {
		description = descriptor.Meta.Description
		controls = schema.SkillInvocationControls{
			DisableModelInvocation: descriptor.Controls.DisableModelInvocation,
			UserInvocable:          descriptor.Controls.UserInvocable,
		}
	}
	return &schema.OrdinarySkillActivation{
		Identity:       obligation.Identity,
		Description:    description,
		Controls:       controls,
		Route:          obligation.Route,
		InvocationID:   obligation.InvocationID,
		UserAuthorized: activationAuthorization(obligation.Route, prior, obligation.Identity),
	}
}

// planSkillDeliveryCommit builds the final-admission transaction for the
// actual dispatch shape. Every pending obligation's complete content must be
// present; a missing body is a visible failure, never a silent truncation.
func (s *Session) planSkillDeliveryCommit(req llm.Request) (skillDeliveryCommit, error) {
	s.mu.Lock()
	obligations := append([]schema.SkillDeliveryObligation(nil), s.skillLifecycle.Obligations...)
	revision := s.skillLifecycle.Revision
	s.mu.Unlock()
	commit := skillDeliveryCommit{Revision: revision}
	for _, obligation := range obligations {
		if !completeSkillContentPresent(req, obligation.Identity) {
			return commit, fmt.Errorf("skill %q complete instructions are not present in the dispatching request", obligation.Identity.Name)
		}
		s.mu.Lock()
		prior := s.skillLifecycle.Inventory[obligation.Identity.Name].Ordinary
		s.mu.Unlock()
		status := "already_present"
		var activation *schema.OrdinarySkillActivation
		if prior == nil || prior.Identity != obligation.Identity {
			// A genuinely new body joins the context.
			status = "delivered"
			s.mu.Lock()
			activation = s.deliveryActivationRecordLocked(obligation, prior)
			s.mu.Unlock()
		} else if (obligation.Route == "user_slash" || obligation.Route == "user_selection") && !prior.UserAuthorized {
			// An explicit user invocation reusing an unchanged body suppresses
			// the duplicate, but its source-scoped authorization must still be
			// recorded: without it a later source that disables model invocation
			// would deny the reload the user's own activation authorized.
			s.mu.Lock()
			activation = s.deliveryActivationRecordLocked(obligation, prior)
			s.mu.Unlock()
		}
		commit.Outcomes = append(commit.Outcomes, schema.SkillActivationOutcome{
			Revision:         revision,
			SessionID:        s.id,
			InvocationID:     obligation.InvocationID,
			ToolCallID:       obligation.ToolCallID,
			ClientMutationID: obligation.ClientMutationID,
			Identity:         obligation.Identity,
			Activation:       activation,
			Status:           status,
		})
		commit.SatisfiedInvocationIDs = append(commit.SatisfiedInvocationIDs, obligation.InvocationID)
	}
	return commit, nil
}

// commitSkillDelivery admits the whole explicit group atomically against the
// lifecycle revision: it updates the inventory, clears the satisfied
// obligations, persists the result, and publishes EventSkillActivated only for
// genuinely new bodies. A stale revision returns false so the caller
// revalidates; persisted delivery represents complete admission, not proof of
// a successful network response.
func (s *Session) commitSkillDelivery(ctx context.Context, commit skillDeliveryCommit) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(commit.Outcomes) == 0 && len(commit.SatisfiedInvocationIDs) == 0 {
		return true, nil
	}
	satisfied := map[string]bool{}
	for _, id := range commit.SatisfiedInvocationIDs {
		satisfied[id] = true
	}
	var newBodies []string
	s.mu.Lock()
	if s.skillLifecycle.Revision != commit.Revision {
		s.mu.Unlock()
		return false, nil
	}
	// Snapshot what this transaction is about to change so a failed metadata
	// save can restore it. Without the rollback the in-memory snapshot keeps
	// clean state while the durable one still holds the satisfied obligations,
	// and a same-process retry sees no pending delivery and skips the
	// revalidation those obligations exist to force.
	priorObligations := append([]schema.SkillDeliveryObligation(nil), s.skillLifecycle.Obligations...)
	priorInventory := make(map[string]schema.SkillInventoryEntry, len(s.skillLifecycle.Inventory))
	maps.Copy(priorInventory, s.skillLifecycle.Inventory)
	kept := s.skillLifecycle.Obligations[:0]
	for _, obligation := range s.skillLifecycle.Obligations {
		if !satisfied[obligation.InvocationID] {
			kept = append(kept, obligation)
		}
	}
	s.skillLifecycle.Obligations = kept
	for _, outcome := range commit.Outcomes {
		switch outcome.Status {
		case "delivered":
			entry := s.skillLifecycle.Inventory[outcome.Identity.Name]
			changed := entry.Ordinary == nil || entry.Ordinary.Identity != outcome.Identity
			if outcome.Activation != nil {
				record := *outcome.Activation
				entry.Ordinary = &record
			}
			s.skillLifecycle.Inventory[outcome.Identity.Name] = entry
			if changed {
				newBodies = append(newBodies, outcome.Identity.Name)
			}
		case "already_present":
			// Unchanged content satisfied an outstanding obligation: the body is
			// not re-delivered and fires no new-body event, but an explicit user
			// invocation's provenance still joins the inventory.
			if outcome.Activation != nil {
				entry := s.skillLifecycle.Inventory[outcome.Identity.Name]
				record := *outcome.Activation
				entry.Ordinary = &record
				s.skillLifecycle.Inventory[outcome.Identity.Name] = entry
			}
		case "failed":
			// A failed reinvocation preserves the earlier successful record.
		}
	}
	s.skillLifecycle.Revision++
	committedRevision := s.skillLifecycle.Revision
	s.mu.Unlock()
	if err := s.saveMeta(); err != nil {
		// Undo the whole transaction, but only when no concurrent writer touched
		// the lifecycle since: the snapshot is the previous map wholesale, so
		// replaying it over a newer revision would clobber that writer's work.
		s.mu.Lock()
		if s.skillLifecycle.Revision == committedRevision {
			s.skillLifecycle.Obligations = priorObligations
			s.skillLifecycle.Inventory = priorInventory
			s.skillLifecycle.Revision = commit.Revision
		}
		s.mu.Unlock()
		s.emit(events.EventWarning, warningDataFromError("saving skill delivery admission failed", err))
		return true, err
	}
	for _, name := range newBodies {
		s.emit(events.EventSkillActivated, events.SkillActivatedData{Name: name})
	}
	return true, nil
}

// finalizeSkillDelivery is the final dispatch seam: immediately before the
// client dispatch it revalidates the resulting request shape against every
// pending obligation and commits the admission. A stale lifecycle revision
// revalidates once; a missing protected body fails the dispatch visibly.
func (s *Session) finalizeSkillDelivery(ctx context.Context, req llm.Request) error {
	s.mu.Lock()
	pending := len(s.skillLifecycle.Obligations) > 0
	s.mu.Unlock()
	if !pending {
		return nil
	}
	for revalidation := 0; ; revalidation++ {
		commit, err := s.planSkillDeliveryCommit(req)
		if err != nil {
			return err
		}
		ok, err := s.commitSkillDelivery(ctx, commit)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if revalidation >= 1 {
			return errors.New("skill delivery admission conflicted with a concurrent lifecycle change")
		}
	}
}
