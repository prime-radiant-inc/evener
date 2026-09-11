package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

// Availability classifications for the complete fallback inventory a
// compaction handoff delivers when its selection authorized no reload.
const (
	// skillAvailabilityPermanent marks a name held only by a frozen role
	// preload: its body lives in the permanent prompt for the prompt's (or
	// frozen delegate's) lifetime and needs no reload.
	skillAvailabilityPermanent = "permanent"
	// skillAvailabilityAlreadyPresent marks a name whose complete current
	// content is already present in the retained tail or was restored by
	// another activation — reusing it needs no new body.
	skillAvailabilityAlreadyPresent = "already_present"
	// skillAvailabilityReloadable marks a name the session can reload by
	// itself: an authorized same-source continuation (regardless of current
	// flags), or an ordinary record whose current controls still admit the
	// compaction reload route.
	skillAvailabilityReloadable = "reloadable"
	// skillAvailabilityRequiresUserActivation marks a hidden but
	// user-invocable skill: current controls block the model's routes, and
	// only a genuine user invocation can restore it.
	skillAvailabilityRequiresUserActivation = "requires_user_activation"
	// skillAvailabilityUnavailable marks a skill whose current controls block
	// every ordinary route, or whose recorded source can no longer be read.
	skillAvailabilityUnavailable = "unavailable"
)

const (
	skillInventoryOpen  = "<skill-inventory>\n"
	skillInventoryClose = "\n</skill-inventory>"
)

// skillInventorySummary classifies every loaded-skill inventory entry from
// current source metadata and actual retained content: authorized
// same-source continuations are reloadable regardless of flags, unauthorized
// hidden-but-user-invocable skills require user activation, skills whose both
// flags block ordinary routes are unavailable, permanently present preloads
// are labeled separately, and content already retained is already present.
// Both provenances are represented when a name holds preload and ordinary
// state. The full list is kept — one diagnostic is reported per source that
// cannot be read, and nothing is trimmed.
func (s *Session) skillInventorySummary(ctx context.Context) ([]schema.SkillInventorySummary, []skill.Diagnostic) {
	_ = ctx // classification reads disk synchronously; no cancellation seam exists
	type ordinaryRecord struct {
		identity schema.SkillContentIdentity
		metadata schema.OrdinarySkillActivation
	}
	s.mu.Lock()
	names := make([]string, 0, len(s.skillLifecycle.Inventory))
	ordinary := make(map[string]ordinaryRecord, len(s.skillLifecycle.Inventory))
	preload := make(map[string]schema.FrozenSkillPreload, len(s.skillLifecycle.Inventory))
	for name, entry := range s.skillLifecycle.Inventory {
		names = append(names, name)
		if entry.Ordinary != nil {
			ordinary[name] = ordinaryRecord{identity: entry.Ordinary.Identity, metadata: *entry.Ordinary}
		}
		if entry.Preload != nil {
			preload[name] = *entry.Preload
		}
	}
	history := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()

	slices.Sort(names)
	summaries := make([]schema.SkillInventorySummary, 0, len(names))
	var diagnostics []skill.Diagnostic
	for _, name := range names {
		record, hasOrdinary := ordinary[name]
		preloadRecord, hasPreload := preload[name]
		if !hasOrdinary && !hasPreload {
			continue
		}
		summary := schema.SkillInventorySummary{
			Name:        name,
			HasOrdinary: hasOrdinary,
			HasPreload:  hasPreload,
		}
		if hasOrdinary {
			summary.Description = record.metadata.Description
		} else {
			summary.Description = preloadRecord.Description
		}
		switch {
		case !hasOrdinary:
			// A frozen preload keeps its permanent-prompt lifetime; compaction
			// never drops its body from the prompt it lives in.
			summary.Availability = skillAvailabilityPermanent
		case completeSkillContentInTurns(history, record.identity):
			summary.Availability = skillAvailabilityAlreadyPresent
		default:
			summary.Availability = skillAvailabilityUnavailable
			identity := record.identity
			if identity.Name == "" || identity.DeclaredName == "" || identity.Source == "" {
				diagnostics = append(diagnostics, skill.Diagnostic{
					Category: "invalid_metadata",
					Name:     name,
					Source:   identity.Source,
					Message:  "recorded activation lacks the declared identity needed to reopen its source",
				})
				break
			}
			loaded, loadDiagnostics, err := skill.Load(skill.Descriptor{
				CatalogName: identity.Name,
				Meta:        skill.SkillMeta{Name: identity.DeclaredName, SkillFile: identity.Source, Dir: filepath.Dir(identity.Source)},
			})
			diagnostics = append(diagnostics, loadDiagnostics...)
			if err != nil {
				break
			}
			controls := loaded.Descriptor.Controls
			switch {
			case record.metadata.UserAuthorized:
				// An authorized same-source continuation reloads regardless of
				// the source's current flags.
				summary.Availability = skillAvailabilityReloadable
			case controls.DisableModelInvocation && controls.UserInvocable:
				summary.Availability = skillAvailabilityRequiresUserActivation
			case controls.DisableModelInvocation && !controls.UserInvocable:
				summary.Availability = skillAvailabilityUnavailable
			default:
				summary.Availability = skillAvailabilityReloadable
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, diagnostics
}

// prepareCompactedSkillReloads consumes the session's pending compaction
// handoff receipts — every entry still recorded and not yet removed, whatever
// phase Task 8's publication or restart reconciliation left it in. Per the
// operation's own selection it either prepares exact reloads (the recorded
// ordinary source, never a catalog retarget) or reserves the complete typed
// metadata reminder. Preparation failures and reminders are recorded as
// durable typed notification turns here, before any body admission; the
// returned batch carries the successfully prepared reload bodies in selected
// order for the caller's budgeted admission, and the returned outcomes carry
// every reload attempt's typed metadata (failed or pending). A nil batch
// means no receipt awaited consumption.
//
// A receipt whose complete reminder cannot fit the remaining window is a
// visible error — the full list is kept, never trimmed. Cancelled receipts
// are terminal records: they authorize nothing and are left in place.
func (s *Session) prepareCompactedSkillReloads(ctx context.Context) (*skillActivationBatch, []schema.SkillActivationOutcome, error) {
	s.mu.Lock()
	handoffs := make([]schema.SkillCompactionReceipt, len(s.skillLifecycle.PendingHandoffs))
	for i, handoff := range s.skillLifecycle.PendingHandoffs {
		handoff.Operation.Selection.Names = slices.Clone(handoff.Operation.Selection.Names)
		handoffs[i] = handoff
	}
	s.mu.Unlock()
	if len(handoffs) == 0 {
		return nil, nil, nil
	}

	batch := &skillActivationBatch{}
	var outcomes []schema.SkillActivationOutcome
	reminderPublications := map[string]bool{}
	for _, receipt := range handoffs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if receipt.Phase == skillCompactionReceiptCancelled {
			continue // a terminal retirement record authorizes nothing
		}
		publicationID := receipt.Operation.PublicationID
		switch receipt.Operation.Selection.State {
		case "valid":
			for _, name := range receipt.Operation.Selection.Names {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				s.mu.Lock()
				ordinary := s.skillLifecycle.Inventory[name].Ordinary
				var prior *schema.OrdinarySkillActivation
				if ordinary != nil {
					record := *ordinary
					prior = &record
				}
				s.mu.Unlock()
				if prior == nil {
					// A preload-only name needs no disk load and records no
					// outcome: selecting it is a no-op.
					continue
				}
				if prior.Identity.Name == "" || prior.Identity.DeclaredName == "" || prior.Identity.Source == "" {
					// The record carries no complete recorded identity, so no
					// reload can be prepared against it — the same no-op as a
					// preload-only selection: nothing is attempted, nothing is
					// reported, and the receipt is left unconsumed.
					continue
				}
				source := prior.Identity
				invocation := skillInvocation{
					Name:         name,
					Route:        "compaction_reload",
					InvocationID: publicationID + ":" + name,
					Source:       &source,
				}
				prepared, err := s.prepareSkillActivations(ctx, []skillInvocation{invocation})
				if err != nil {
					outcome := schema.SkillActivationOutcome{
						SessionID:    s.id,
						InvocationID: invocation.InvocationID,
						Identity:     prior.Identity,
						Status:       "failed",
						ErrorCode:    skillActivationErrorCode(err),
					}
					s.recordSkillReloadNotification(
						systemNotificationf("Skill %q could not be reloaded after compaction: %v", name, err),
						outcome, nil)
					outcomes = append(outcomes, outcome)
					continue
				}
				item := prepared.Items[0]
				identity := skillContentIdentity(item)
				activation := ordinaryActivationRecord(item, prior)
				outcome := schema.SkillActivationOutcome{
					SessionID:    s.id,
					InvocationID: invocation.InvocationID,
					Identity:     identity,
					Activation:   &activation,
					Status:       "pending",
				}
				previousIdentity := prior.Identity
				previousControls := prior.Controls
				outcome.PreviousIdentity = &previousIdentity
				outcome.PreviousControls = &previousControls
				batch.Items = append(batch.Items, item)
				outcomes = append(outcomes, outcome)
			}
		default:
			// Absent or invalid: no reload is authorized. Reserve the complete
			// typed metadata notification — every inventory entry — and never
			// enqueue bodies here.
			summary, diagnostics := s.skillInventorySummary(ctx)
			_ = diagnostics // the reminder's entries carry their own availability; diagnostics ride the typed turn
			if len(summary) == 0 {
				// No skill is loaded: the complete reminder is an empty list
				// with nothing to notify. Consume the receipt without a turn
				// rather than appending vacuous history.
				reminderPublications[publicationID] = true
				continue
			}
			s.mu.Lock()
			revision := s.skillLifecycle.Revision
			s.mu.Unlock()
			reminder := schema.SkillReloadReminder{
				Revision:      revision,
				PublicationID: publicationID,
				Selection: schema.SkillReloadSelection{
					State:     receipt.Operation.Selection.State,
					Names:     slices.Clone(receipt.Operation.Selection.Names),
					ErrorCode: receipt.Operation.Selection.ErrorCode,
				},
				Inventory: summary,
			}
			content := renderSkillReloadReminder(reminder, s.canInstructTool("use_skill"))
			if !s.skillReloadReminderFits(content) {
				// Keep the full list and fail visibly; never trim older names.
				return nil, nil, fmt.Errorf("the complete post-compaction skill inventory (%d entries) does not fit the remaining context window", len(summary))
			}
			turn := schema.NewTurn(schema.TurnSystem, llm.User(content))
			turn.SkillState = &schema.SkillTurnState{ReloadReminder: &reminder}
			s.recordTurn(turn, turn)
			reminderPublications[publicationID] = true
		}
	}
	if len(reminderPublications) > 0 {
		// The reminder's durable admission is its recorded turn: consume its
		// receipts now so a retry or restart cannot repeat delivery.
		s.mu.Lock()
		removed := s.removeSkillCompactionHandoffsLocked(reminderPublications)
		if removed {
			s.skillLifecycle.Revision++
		}
		s.mu.Unlock()
		if removed {
			if err := s.saveMeta(); err != nil {
				s.emit(events.EventWarning, warningDataFromError("persisting the compaction skill reminder consumption failed", err))
				return nil, nil, err
			}
		}
	}
	return batch, outcomes, nil
}

// admitCompactedSkillReloads performs the budgeted body admission for a
// prepared reload batch: a NEW explicit activation already outranks the
// reloads because its carrier is part of the current input the caller's
// budget measures; the selected reloads are then admitted in order, each
// against the running total. Complete content already present in the retained
// tail or restored by another activation is reused — never duplicated; an
// over-budget reload fails individually with a typed context_budget outcome
// and never starts a second compaction or model-repair round. After the
// bodies and their obligations are durably recorded, the consumed reload
// receipts are removed by publication identity, disturbing no unrelated
// pending operation.
func (s *Session) admitCompactedSkillReloads(ctx context.Context, profile *provider.Profile, budget *llm.TokenBudget, batch *skillActivationBatch, outcomes []schema.SkillActivationOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if batch == nil {
		return nil
	}
	window := 0
	if profile != nil {
		window = profile.ContextWindowSize()
	}
	outcomeByID := make(map[string]*schema.SkillActivationOutcome, len(outcomes))
	for i := range outcomes {
		outcomeByID[outcomes[i].InvocationID] = &outcomes[i]
	}
	var obligations []schema.SkillDeliveryObligation
	for _, item := range batch.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity := skillContentIdentity(item)
		obligation := schema.SkillDeliveryObligation{
			InvocationID: item.Invocation.InvocationID,
			Identity:     identity,
			Route:        item.Invocation.Route,
		}
		outcome := outcomeByID[item.Invocation.InvocationID]
		if s.skillContentInLiveHistory(identity) {
			// Reuse complete content already present in the retained tail or
			// restored by another activation: no duplicate body, obligation
			// stands until final admission.
			if outcome != nil {
				outcome.Status = "already_present"
				s.recordSkillReloadNotification(
					systemNotificationf("Skill %q's complete current instructions are already present in this conversation; its reload reuses them.", identity.Name),
					*outcome, nil)
			}
			obligations = append(obligations, obligation)
			continue
		}
		bodyTokens := llm.EstimateMessagesInputTokens([]llm.Message{llm.User(item.Rendered.Content)}).Tokens
		if window > 0 && budget.InputTokens+bodyTokens+1 >= window {
			// The +1 covers the estimator's per-message rounding against the
			// dispatch seam's own admission arithmetic (llm.ApplyTokenBudget),
			// so an admitted reload can never push the rebuilt request over
			// the window and into a recovery round.
			if outcome != nil {
				outcome.Status = "failed"
				outcome.ErrorCode = "context_budget"
				s.recordSkillReloadNotification(
					systemNotificationf("Skill %q's complete instructions do not fit the remaining context window and were not reloaded after compaction.", identity.Name),
					*outcome, nil)
			}
			continue
		}
		budget.InputTokens += bodyTokens
		if outcome != nil {
			carrier := schema.NewTurn(schema.TurnSystem, llm.User(item.Rendered.Content))
			carrier.SkillState = &schema.SkillTurnState{
				Outcomes:    []schema.SkillActivationOutcome{*outcome},
				Obligations: []schema.SkillDeliveryObligation{obligation},
			}
			s.recordTurn(carrier, carrier)
		}
		obligations = append(obligations, obligation)
	}
	// Reload receipts are consumed only now, after the carriers and
	// obligations below are durable in the same save.
	s.mu.Lock()
	publications := s.consumedReloadPublicationsLocked(outcomes)
	removed := s.removeSkillCompactionHandoffsLocked(publications)
	if len(obligations) > 0 {
		s.skillLifecycle.Obligations = append(s.skillLifecycle.Obligations, obligations...)
	}
	if removed || len(obligations) > 0 {
		s.skillLifecycle.Revision++
	}
	s.mu.Unlock()
	if removed || len(obligations) > 0 {
		if err := s.saveMeta(); err != nil {
			s.emit(events.EventWarning, warningDataFromError("persisting the compacted skill reload admission failed", err))
			return err
		}
	}
	return nil
}

// consumedReloadPublicationsLocked derives the publication identities whose
// valid-selection receipts this preparation fully processed: every selected
// name either has no ordinary record (a preload-only no-op) or produced a
// typed outcome under the publication's invocation identity. Receipts that
// appeared after the preparation snapshot stay for the next round, and
// absent/invalid receipts were already consumed with their reminders.
// Callers hold s.mu.
func (s *Session) consumedReloadPublicationsLocked(outcomes []schema.SkillActivationOutcome) map[string]bool {
	seen := make(map[string]bool, len(outcomes))
	for _, outcome := range outcomes {
		seen[outcome.InvocationID] = true
	}
	publications := map[string]bool{}
	for _, handoff := range s.skillLifecycle.PendingHandoffs {
		if handoff.Operation.PublicationID == "" || handoff.Phase == skillCompactionReceiptCancelled {
			continue
		}
		if handoff.Operation.Selection.State != "valid" {
			continue // reminder receipts were consumed with their recorded notification
		}
		consumed := true
		for _, name := range handoff.Operation.Selection.Names {
			if s.skillLifecycle.Inventory[name].Ordinary == nil {
				continue // preload-only names are no-ops
			}
			if !seen[handoff.Operation.PublicationID+":"+name] {
				consumed = false
				break
			}
		}
		if consumed {
			publications[handoff.Operation.PublicationID] = true
		}
	}
	return publications
}

// skillContentInLiveHistory reports whether the live history carries the
// complete content for identity — the retained-tail reuse check.
func (s *Session) skillContentInLiveHistory(identity schema.SkillContentIdentity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return completeSkillContentInTurns(s.history, identity)
}

// recordSkillReloadNotification appends one typed reload notification turn to
// the real history: the model-facing notice plus the causal outcome, and the
// delivery obligation when the notification reserves one.
func (s *Session) recordSkillReloadNotification(message string, outcome schema.SkillActivationOutcome, obligation *schema.SkillDeliveryObligation) {
	turn := schema.NewTurn(schema.TurnSystem, llm.User(message))
	state := &schema.SkillTurnState{Outcomes: []schema.SkillActivationOutcome{outcome}}
	if obligation != nil {
		state.Obligations = []schema.SkillDeliveryObligation{*obligation}
	}
	turn.SkillState = state
	s.recordTurn(turn, turn)
}

// renderSkillReloadReminder renders the complete typed inventory as a
// structured document (JSON entries between opaque tags — a machine contract,
// not prose) followed by the remedy guidance for this session's tool surface.
func renderSkillReloadReminder(reminder schema.SkillReloadReminder, canUseSkill bool) string {
	encoded, _ := json.Marshal(reminder.Inventory)
	var b strings.Builder
	b.WriteString(skillInventoryOpen)
	b.Write(encoded)
	b.WriteString(skillInventoryClose)
	if canUseSkill {
		b.WriteString("\nCompaction may have dropped the instruction bodies of the skills listed above. ")
		b.WriteString("Availability marks what can happen now: already_present bodies need nothing; reloadable skills can be restored by invoking use_skill with their exact name; ")
		b.WriteString("requires_user_activation skills need a genuine user invocation; unavailable skills cannot be reloaded through any ordinary route; ")
		b.WriteString("permanent preloads keep their place in the system prompt. Re-invoke the ones you still need.")
		return b.String()
	}
	b.WriteString("\nCompaction may have dropped the instruction bodies of the skills listed above. ")
	b.WriteString("This session has no use_skill tool, so tracked activation cannot be restored from here: already_present bodies need nothing, and for the rest the untracked fallback is reading the skill's SKILL.md file directly with the file-reading tools. ")
	b.WriteString("A raw file read is not a tracked activation — it restores the text to the conversation only.")
	return b.String()
}

// skillReloadReminderFits reports whether the complete reminder metadata fits
// the remaining context window, measured with the session's own estimator
// (the context manager's accounting over the retained history and system
// prompt) plus the same safety reserve the dispatch seam holds back. The
// basis deliberately excludes the tool-definition schema the seam counts, so
// the check errs toward refusing rather than trimming.
func (s *Session) skillReloadReminderFits(content string) bool {
	s.mu.Lock()
	window := 0
	if s.profile != nil {
		window = s.profile.ContextWindowSize()
	}
	sys := s.cachedSystemPrompt
	history := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	if window <= 0 || s.contextMgr == nil {
		return true
	}
	metrics := s.contextMgr.EstimateUsage(history, len(sys))
	tokens := llm.EstimateMessagesInputTokens([]llm.Message{llm.User(content)}).Tokens
	return metrics.Used+tokens+skillReloadSafetyReserve(window) < window
}

// skillReloadSafetyReserve mirrors llm's unexported token safety reserve:
// max(1024, window rounded up to a percent), so the reminder check holds back
// the same headroom the dispatch seam's admission does.
func skillReloadSafetyReserve(window int) int {
	percent := window / 100
	if window%100 != 0 {
		percent++
	}
	if percent < 1024 {
		return 1024
	}
	return percent
}
