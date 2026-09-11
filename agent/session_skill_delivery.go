package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

	// reloaded reports that prepare appended typed activation notification
	// turns to the real history; the caller rebuilds the request so the
	// restored carriers join this dispatch.
	reloaded bool
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
			var activationErr *skillActivationError
			if errors.As(res.Err, &activationErr) {
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

// persistSkillToolObligations records the round's new delivery obligations in
// the lifecycle snapshot and saves it before a retry or restart can lose
// them. A save failure is surfaced to the caller.
func (s *Session) persistSkillToolObligations(state *schema.SkillTurnState) error {
	if state == nil || len(state.Obligations) == 0 {
		return nil
	}
	s.mu.Lock()
	s.skillLifecycle.Obligations = append(s.skillLifecycle.Obligations, state.Obligations...)
	s.skillLifecycle.Revision++
	s.mu.Unlock()
	if err := s.saveMeta(); err != nil {
		s.emit(events.EventWarning, warningDataFromError("saving skill delivery obligations failed", err))
		return err
	}
	return nil
}

// recordSkillDeliveryNotification appends one typed activation notification to
// the real history: the model-facing carrier content plus the causal outcome
// and obligation, linked to the original invocation. Continuation
// re-expansion sees it because it is an ordinary recorded turn.
func (s *Session) recordSkillDeliveryNotification(message llm.Message, outcome schema.SkillActivationOutcome, obligation schema.SkillDeliveryObligation) {
	turn := schema.NewTurn(schema.TurnSystem, message)
	turn.SkillState = &schema.SkillTurnState{
		Outcomes:    []schema.SkillActivationOutcome{outcome},
		Obligations: []schema.SkillDeliveryObligation{obligation},
	}
	s.recordTurn(turn, turn)
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
// the request when commit.reloaded is set. A reload failure records an
// explicit failed outcome and finalizes the obligation — never a false
// delivery. It never starts another compact/reload cycle.
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
			s.recordSkillDeliveryNotification(
				llm.User(systemNotificationf("Skill %q is no longer available from %s: %v", obligation.Identity.Name, obligation.Identity.Source, err)),
				outcome, obligation)
			failed = append(failed, obligation.InvocationID)
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
		if newIdentity != obligation.Identity {
			// Changed disk content is reported explicitly; this notification
			// supersedes the earlier provisional outcome.
			content = systemNotificationf("Skill %q changed on disk since its earlier activation; the complete current instructions follow.", obligation.Identity.Name) + "\n\n" + content
		}
		corrected := obligation
		corrected.Identity = newIdentity
		s.recordSkillDeliveryNotification(llm.User(content), outcome, corrected)
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
		commit.reloaded = true
	}
	if len(failed) > 0 {
		s.finalizeSkillDeliveryFailure(failed...)
	}
	if commit.reloaded || len(failed) > 0 {
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
			// Unchanged content satisfied an outstanding obligation: no
			// inventory change and no new-body event.
		case "failed":
			// A failed reinvocation preserves the earlier successful record.
		}
	}
	s.skillLifecycle.Revision++
	s.mu.Unlock()
	if err := s.saveMeta(); err != nil {
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
