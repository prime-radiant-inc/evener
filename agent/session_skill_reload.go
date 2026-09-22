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

// skillInventoryDiagnostics maps the discovery pass's diagnostics onto the
// reminder's schema-level records, so the typed turn carries the reason an
// entry classified unavailable — an unreadable or invalid source — instead
// of discarding it.
func skillInventoryDiagnostics(diagnostics []skill.Diagnostic) []schema.SkillInventoryDiagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	out := make([]schema.SkillInventoryDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		out = append(out, schema.SkillInventoryDiagnostic{
			Category: diagnostic.Category,
			Name:     diagnostic.Name,
			Source:   diagnostic.Source,
			Message:  diagnostic.Message,
		})
	}
	return out
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
// means no receipt awaited consumption. The returned token count is the
// input-token cost of every turn preparation appended (reload failure
// notifications and the fallback reminder); the caller folds it into the
// admission budget so those staged turns count against the same window the
// reload bodies do.
//
// A receipt whose complete reminder cannot fit the remaining window is a
// visible error — the full list is kept, never trimmed. Cancelled receipts
// are terminal records: they authorize nothing, and this scan retires them so
// they cannot accumulate.
func (s *Session) prepareCompactedSkillReloads(ctx context.Context) (*skillActivationBatch, []schema.SkillActivationOutcome, int, error) {
	// A terminal cancellation receipt is a pure retirement record — the save that
	// recorded it already cleared the operation slot — and, carrying no
	// publication identity, it never coalesces. Retire every one this request
	// observes so PendingHandoffs and this scan stay bounded over a long-lived
	// session; a failed retirement save only warns and leaves them for the next
	// request.
	s.retireSkillCompactionCancellations()
	s.mu.Lock()
	handoffs := make([]schema.SkillCompactionReceipt, len(s.skillLifecycle.PendingHandoffs))
	for i, handoff := range s.skillLifecycle.PendingHandoffs {
		handoff.Operation.Selection.Names = slices.Clone(handoff.Operation.Selection.Names)
		handoffs[i] = handoff
	}
	s.mu.Unlock()
	if len(handoffs) == 0 {
		return nil, nil, 0, nil
	}

	batch := &skillActivationBatch{}
	var outcomes []schema.SkillActivationOutcome
	// stagedTokens is the input-token cost of every notification turn appended
	// below. The caller folds it into the admission budget, because these turns
	// join the same request the body admission is measured against.
	stagedTokens := 0
	// consumedReminders counts the reminder receipts this call consumed, so the
	// lifecycle is saved once at the end when any were.
	consumedReminders := 0
	for _, receipt := range handoffs {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		if receipt.Phase == skillCompactionReceiptCancelled {
			continue // a cancellation recorded after this snapshot authorizes nothing
		}
		publicationID := receipt.Operation.PublicationID
		switch receipt.Operation.Selection.State {
		case "valid":
			for _, name := range receipt.Operation.Selection.Names {
				if err := ctx.Err(); err != nil {
					return nil, nil, 0, err
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
				// A record with no complete recorded identity (a legacy
				// activation) is reported by prepareSkillActivations as a typed
				// invalid_metadata failure rather than skipped silently: a silent
				// skip left the publication unconsumed forever, so every later
				// request re-processed the same selection and re-announced its
				// reloadable names, growing history and the obligation list
				// without bound.
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
					notice := systemNotificationf("Skill %q could not be reloaded after compaction: %v", name, err)
					// The explanation is durably recorded before the receipt is
					// consumed. A retry after a failed consumption save re-derives
					// this deterministic invocation, so it must recognize the notice
					// already recorded for it instead of appending a second copy.
					if !s.skillReloadOutcomeRecorded(invocation.InvocationID) {
						if err := s.recordSkillReloadNotification(notice, outcome, nil); err != nil {
							return nil, nil, 0, err
						}
						stagedTokens += skillReloadTurnTokens(notice)
					}
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
			if len(summary) == 0 {
				// No skill is loaded: the complete reminder is an empty list
				// with nothing to notify. Consume the receipt without a turn
				// rather than appending vacuous history.
				s.mu.Lock()
				s.consumeSkillReloadReminderLocked(publicationID)
				s.mu.Unlock()
				consumedReminders++
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
				Inventory:   summary,
				Diagnostics: skillInventoryDiagnostics(diagnostics),
			}
			content := renderSkillReloadReminder(reminder, s.canInstructTool("use_skill"))
			if !s.skillReloadReminderFits(content) {
				// Keep the full list and fail visibly; never trim older names.
				// Reminders already admitted in this call left with their
				// receipts, so a retry re-appends none of them and spends no
				// more of the very window this check measures.
				return nil, nil, 0, fmt.Errorf("the complete post-compaction skill inventory (%d entries) does not fit the remaining context window", len(summary))
			}
			turn := schema.NewTurn(schema.TurnSystem, llm.User(content))
			turn.SkillState = &schema.SkillTurnState{ReloadReminder: &reminder}
			// The reminder's durable admission is this turn, so append through
			// the durable pair -- transcript write first, live append only on
			// success -- and consume the receipt in that same commit: a
			// receipt whose reminder exists is never visible to a retry, so
			// nothing can deliver the same inventory twice. A failed write
			// must NOT consume the receipt: the handoff stays pending so a
			// retry (or a restart) can still deliver the only reminder for it,
			// instead of recording nothing and forgetting the handoff forever.
			live, persisted := turn, turn
			live.SkillState = live.SkillState.Clone()
			persisted.SkillState = persisted.SkillState.Clone()
			if err := s.appendTurnAfterTranscriptWrite(
				persisted,
				func() error { return s.writeTranscriptSyncedLocked(persisted) },
				func() {
					s.history = append(s.history, live)
					s.consumeSkillReloadReminderLocked(publicationID)
				},
			); err != nil {
				s.emit(events.EventWarning, warningDataFromError("recording the post-compaction skill reminder failed", err))
				return nil, nil, 0, fmt.Errorf("recording the post-compaction skill reminder: %w", err)
			}
			stagedTokens += skillReloadTurnTokens(content)
			consumedReminders++
		}
	}
	if consumedReminders > 0 {
		if err := s.persistSkillReloadReminderConsumption(); err != nil {
			return nil, nil, 0, err
		}
	}
	return batch, outcomes, stagedTokens, nil
}

// consumeSkillReloadReminderLocked retires the handoff whose reminder was just
// admitted (or needed no turn), so neither a retry nor a restart can deliver
// the same inventory notification twice. Callers hold s.mu.
func (s *Session) consumeSkillReloadReminderLocked(publicationID string) {
	if len(s.removeSkillCompactionHandoffsLocked(map[string]bool{publicationID: true})) > 0 {
		s.skillLifecycle.Revision++
	}
}

// persistSkillReloadReminderConsumption saves the lifecycle after reminder
// receipts were consumed in memory. A failed save is reported and rolls
// nothing back: the receipt left with its reminder's durable commit, so a
// retry finds no receipt and appends no second reminder, and a restart
// reconciles a snapshot that still holds the receipt from the durable reminder
// turn (reconcileSkillCompactionReceipts).
func (s *Session) persistSkillReloadReminderConsumption() error {
	if err := s.saveMeta(); err != nil {
		s.emit(events.EventWarning, warningDataFromError("persisting the compaction skill reminder consumption failed", err))
		return err
	}
	return nil
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
// pending operation. stagedInputTokens is preparation's cost for the turns it
// already appended; it joins the running total here, and every notification
// this admission appends joins it too, so the final total covers the complete
// staged request.
func (s *Session) admitCompactedSkillReloads(ctx context.Context, profile *provider.Profile, budget *llm.TokenBudget, stagedInputTokens int, batch *skillActivationBatch, outcomes []schema.SkillActivationOutcome) error {
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
	// Fold the turns preparation already appended into the same running total
	// the body admission measures, then reserve — before any body is admitted —
	// the space every notification this admission can still append: each item
	// contributes at most one small typed notice (a reuse notice or a
	// context_budget explanation). Counting the whole remaining allowance in
	// every admission check is what keeps the COMPLETE staged request inside the
	// window: a body is admitted only when its carrier AND the explanations the
	// items after it may still owe both fit, so the rebuild that follows
	// admission can never overflow the window and fail the whole turn after the
	// receipts were already consumed.
	budget.InputTokens += stagedInputTokens
	pendingNotifications := 0
	for _, item := range batch.Items {
		pendingNotifications += skillReloadNotificationReserve(skillContentIdentity(item).Name)
	}
	outcomeByID := make(map[string]*schema.SkillActivationOutcome, len(outcomes))
	for i := range outcomes {
		outcomeByID[outcomes[i].InvocationID] = &outcomes[i]
	}
	var obligations []schema.SkillDeliveryObligation
	// Carriers embed the COMPLETE body, so they are recorded only after the
	// obligations that keep those bodies alive are durable. Recording them here
	// would publish a durable carrier whose obligation a failed save (or a crash
	// before it) could lose.
	var carriers []schema.Turn
	// A body staged in this batch is not in the live history yet, so the
	// history-based reuse check alone would admit a second complete body for
	// another selection naming the same skill. Track what this batch staged so
	// the reuse decision matches what the history would show once the carriers
	// are recorded.
	stagedBodies := map[schema.SkillContentIdentity]bool{}
	for _, item := range batch.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity := skillContentIdentity(item)
		// This item's own notification, if it turns out to need one, is no
		// longer pending: the allowance now covers only the items after it.
		pendingNotifications -= skillReloadNotificationReserve(identity.Name)
		obligation := schema.SkillDeliveryObligation{
			InvocationID: item.Invocation.InvocationID,
			Identity:     identity,
			Route:        item.Invocation.Route,
		}
		outcome := outcomeByID[item.Invocation.InvocationID]
		if s.skillContentInLiveHistory(identity) || stagedBodies[identity] {
			// Reuse complete content already present in the retained tail or
			// restored by another activation, or staged earlier in this same
			// batch: no duplicate body, obligation stands until final admission.
			if outcome != nil {
				outcome.Status = "already_present"
				notice := skillReloadReuseNotification(identity.Name)
				if !s.skillReloadOutcomeRecorded(outcome.InvocationID) {
					if err := s.recordSkillReloadNotification(notice, *outcome, nil); err != nil {
						return err
					}
					budget.InputTokens += skillReloadTurnTokens(notice)
				}
			}
			obligations = append(obligations, obligation)
			continue
		}
		bodyTokens := llm.EstimateMessagesInputTokens([]llm.Message{llm.User(item.Rendered.Content)}).Tokens
		if window > 0 && budget.InputTokens+pendingNotifications+bodyTokens+1 >= window {
			// The +1 covers the estimator's per-message rounding against the
			// dispatch seam's own admission arithmetic (llm.ApplyTokenBudget),
			// and pendingNotifications covers every notification the items
			// after this one can still append, so an admitted reload plus the
			// rest of the staged request can never push the rebuilt request
			// over the window and into a recovery round.
			if outcome != nil {
				outcome.Status = "failed"
				outcome.ErrorCode = "context_budget"
				notice := skillReloadContextBudgetNotification(identity.Name)
				if !s.skillReloadOutcomeRecorded(outcome.InvocationID) {
					if err := s.recordSkillReloadNotification(notice, *outcome, nil); err != nil {
						return err
					}
					budget.InputTokens += skillReloadTurnTokens(notice)
				}
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
			carriers = append(carriers, carrier)
			stagedBodies[identity] = true
		}
		obligations = append(obligations, obligation)
	}
	// The obligations are persisted before their carriers are recorded, exactly
	// like every other activation route; the reverse window (an obligation whose
	// carrier never landed) re-delivers from the recorded source at the next
	// dispatch seam and is the safe one.
	// The receipts are consumed and the obligations admitted only once the
	// metadata recording both is durable, so mutation, save and rollback are
	// one critical section under metaSaveMu: a concurrent autosave must not
	// persist the transient state between a failed save and its restore. The
	// rollback takes back exactly what this admission changed. s.mu is not
	// held while the save runs, so a fold publishing in that window can record
	// a handoff of its own, and both a wholesale pre-admission snapshot and a
	// rollback skipped because the lifecycle moved would lose a receipt.
	if err := func() error {
		s.metaSaveMu.Lock()
		defer s.metaSaveMu.Unlock()
		s.mu.Lock()
		publications := s.consumedReloadPublicationsLocked(outcomes)
		removed := s.removeSkillCompactionHandoffsLocked(publications)
		if len(obligations) > 0 {
			s.skillLifecycle.Obligations = append(s.skillLifecycle.Obligations, obligations...)
		}
		changed := len(removed) > 0 || len(obligations) > 0
		if changed {
			s.skillLifecycle.Revision++
		}
		s.mu.Unlock()
		if !changed {
			return nil
		}
		err := s.autoSaveMetaLocked()
		if err != nil {
			s.mu.Lock()
			s.restoreSkillCompactionHandoffsLocked(removed)
			s.skillLifecycle.Obligations = withoutObligationsByInvocationID(s.skillLifecycle.Obligations, obligations)
			s.skillLifecycle.Revision++
			s.mu.Unlock()
		}
		return err
	}(); err != nil {
		s.emit(events.EventWarning, warningDataFromError("persisting the compacted skill reload admission failed", err))
		return err
	}
	for _, carrier := range carriers {
		// A failed write returns: the obligations are durable, so the body is
		// recoverable from its recorded source at the next dispatch seam, and
		// the caller must not treat the reload as delivered.
		if err := s.recordSkillCarrierDurably(carrier, carrier); err != nil {
			return err
		}
	}
	return nil
}

// consumedReloadPublicationsLocked derives the publication identities whose
// valid-selection receipts this preparation fully processed: every selected
// name either has no ordinary record (a preload-only no-op) or produced a
// typed outcome under the publication's invocation identity — including the
// typed invalid_metadata outcome a legacy record with no complete identity now
// produces. A name with no outcome at all would leave the receipt pending for a
// later round. Receipts that appeared after the preparation snapshot stay for
// the next round, and absent/invalid receipts were already consumed with their
// reminders.
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
//
// A non-nil obligation makes this turn a carrier, so the caller must have
// persisted that obligation before calling — every current call site passes
// nil and relies on the body-carrying admission below instead. The turn goes
// through the durable transcript door and its write failure is returned, so a
// caller never consumes a receipt whose explanation the model never received.
func (s *Session) recordSkillReloadNotification(message string, outcome schema.SkillActivationOutcome, obligation *schema.SkillDeliveryObligation) error {
	turn := schema.NewTurn(schema.TurnSystem, llm.UserMachinery(message))
	state := &schema.SkillTurnState{Outcomes: []schema.SkillActivationOutcome{outcome}}
	if obligation != nil {
		state.Obligations = []schema.SkillDeliveryObligation{*obligation}
	}
	turn.SkillState = state
	return s.recordSkillCarrierDurably(turn, turn)
}

// skillReloadTurnTokens estimates the input tokens one recorded reload
// notification or reminder turn contributes to the request. Every turn this
// route appends is a single user-role text message, so the same estimator the
// body admission already uses applies unchanged.
func skillReloadTurnTokens(content string) int {
	return llm.EstimateMessagesInputTokens([]llm.Message{llm.User(content)}).Tokens
}

// skillReloadOutcomeRecorded reports whether the live history already carries a
// typed outcome for invocationID — the durable record that this reload's
// notification (a failure explanation, a reuse notice, or a context_budget
// explanation) has already been appended. Reload invocation identities are
// deterministic (publication:name) and the notice is written durably BEFORE the
// admission save that consumes its receipt, so a save failure followed by a
// retry re-derives the same identity. Reading it back is what keeps a retry,
// however many times it repeats, from appending the same notification turn
// again and growing the history without bound.
func (s *Session) skillReloadOutcomeRecorded(invocationID string) bool {
	if invocationID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		state := turn.SkillState
		if state == nil {
			continue
		}
		for _, outcome := range state.Outcomes {
			if outcome.InvocationID == invocationID {
				return true
			}
		}
	}
	return false
}

// skillReloadReuseNotification renders the typed notice recorded when a
// reload's complete content is already present in the retained tail (or was
// restored by another activation), so its body is reused rather than duplicated.
func skillReloadReuseNotification(name string) string {
	return systemNotificationf("Skill %q's complete current instructions are already present in this conversation; its reload reuses them.", name)
}

// skillReloadContextBudgetNotification renders the typed explanation recorded
// when a reload body cannot fit the remaining context window.
func skillReloadContextBudgetNotification(name string) string {
	return systemNotificationf("Skill %q's complete instructions do not fit the remaining context window and were not reloaded after compaction.", name)
}

// skillReloadNotificationReserve bounds the input tokens one admission decision
// can append as a notification: a reuse notice or a context_budget explanation,
// whichever is larger for the skill. Admission reserves this much for every
// item still to be processed, so whatever a later decision appends has space
// even when an earlier body was admitted right up to the window.
func skillReloadNotificationReserve(name string) int {
	return max(skillReloadTurnTokens(skillReloadReuseNotification(name)), skillReloadTurnTokens(skillReloadContextBudgetNotification(name)))
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
