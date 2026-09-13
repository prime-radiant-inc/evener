package schema

import "slices"

// SkillInvocationControls records both flags, including explicit false values.
type SkillInvocationControls struct {
	DisableModelInvocation bool `json:"disable_model_invocation"`
	UserInvocable          bool `json:"user_invocable"`
}

// SkillContentIdentity pins the canonical name, declared identity and exact source.
// Digests identify file bytes and the complete rendered document, not a body copy.
type SkillContentIdentity struct {
	Name           string `json:"name"`
	DeclaredName   string `json:"declared_name"`
	Source         string `json:"source"`
	FileDigest     string `json:"file_digest"`
	RenderedDigest string `json:"rendered_digest"`
}

// OrdinarySkillActivation is the durable record of one ordinary activation:
// the exact content identity, description and controls observed at
// activation, the invocation route, the invocation ID, and whether the user
// explicitly authorized the skill.
type OrdinarySkillActivation struct {
	Identity       SkillContentIdentity    `json:"identity"`
	Description    string                  `json:"description"`
	Controls       SkillInvocationControls `json:"controls"`
	Route          string                  `json:"route"`
	InvocationID   string                  `json:"invocation_id"`
	UserAuthorized bool                    `json:"user_authorized"`
}

// FrozenSkillPreload has independent provenance and never grants ordinary
// invocation authorization. Legacy preloads retain empty, unknown provenance.
type FrozenSkillPreload struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	FileDigest     string `json:"file_digest"`
	RenderedDigest string `json:"rendered_digest"`
}

// SkillInventoryEntry is one canonical skill's state in the session
// inventory: its ordinary activation and/or frozen role preload, when present.
type SkillInventoryEntry struct {
	Ordinary *OrdinarySkillActivation `json:"ordinary,omitempty"`
	Preload  *FrozenSkillPreload      `json:"preload,omitempty"`
}

// SkillDeliveryObligation remains outstanding until final request admission,
// including for an already_present provisional outcome.
type SkillDeliveryObligation struct {
	InvocationID     string               `json:"invocation_id"`
	ToolCallID       string               `json:"tool_call_id"`
	ClientMutationID string               `json:"client_mutation_id"`
	AtomicGroupID    string               `json:"atomic_group_id"`
	Identity         SkillContentIdentity `json:"identity"`
	Route            string               `json:"route"`
}

// SkillReloadSelection is the parsed outcome of a reload-selection request.
// State distinguishes absent (empty/null), valid (including an explicit empty
// list), and invalid (malformed or naming an unknown skill); only a valid
// selection authorizes a reload, and invalid preserves the accompanying note.
type SkillReloadSelection struct {
	State     string   `json:"state"` // absent, valid, invalid
	Names     []string `json:"names"`
	ErrorCode string   `json:"error_code,omitempty"`
}

// SkillInventorySummary is the typed loaded-skill metadata handed to note
// elicitation so the model can choose reloads by canonical name.
type SkillInventorySummary struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Availability string `json:"availability"`
	HasOrdinary  bool   `json:"has_ordinary"`
	HasPreload   bool   `json:"has_preload"`
}

// SkillReloadReminder is the typed complete loaded-skill inventory a
// compaction handoff delivers when its selection authorized no reload: every
// inventory entry with its availability classification, linked to the
// publication whose fold dropped the bodies. It carries metadata only —
// never instruction bodies.
type SkillReloadReminder struct {
	Revision      uint64                  `json:"revision"`
	PublicationID string                  `json:"publication_id,omitempty"`
	Selection     SkillReloadSelection    `json:"selection"`
	Inventory     []SkillInventorySummary `json:"inventory"`
	// Diagnostics carries the discovery pass's per-source reasons an entry
	// classified unavailable (an unreadable or invalid source), so the typed
	// turn keeps the reason, not just the classification.
	Diagnostics []SkillInventoryDiagnostic `json:"diagnostics,omitempty"`
}

// SkillInventoryDiagnostic is the schema-level, machine-readable reason a
// discovery pass could not read or classify one skill source. It mirrors the
// skill package's own diagnostic without importing it (schema is the
// lower-level package).
type SkillInventoryDiagnostic struct {
	Category string `json:"category"`
	Name     string `json:"name,omitempty"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// SkillCompactionOperation is one generation-owned compaction intent: the
// note, steering instructions, and reload selection a compaction cycle must
// honor, recorded as operation metadata (never skill bodies). Generation comes
// from the lifecycle's NextOperationGen counter and is never reused across a
// restart. The slot holds at most one operation; it clears only on terminal
// cancellation or completed delivery, so a fold can claim exactly the
// generation it captured.
type SkillCompactionOperation struct {
	Generation     uint64               `json:"generation"`
	Origin         string               `json:"origin"` // forced or automatic
	Instructions   string               `json:"instructions"`
	NoteGeneration uint64               `json:"note_generation"`
	Selection      SkillReloadSelection `json:"selection"`
	Phase          string               `json:"phase"` // pending or published
	PublicationID  string               `json:"publication_id,omitempty"`
}

// SkillCompactionReceipt is the typed record of one compaction handoff: which
// publication (Revision + SessionID + the operation's PublicationID) carried
// which operation, and how far that handoff got. Phase is "published" (the
// winning fold publication claimed the operation and committed its receipt to
// the transcript), "delivered" (the handoff completed — the cycle's slot
// cleared and the selection consumed), or "cancelled" (a terminal cancellation
// retired the operation before any publication claimed it). A receipt whose
// Operation is the zero value except PublicationID is the absent-selection
// reminder a real compaction records when no operation was captured: it
// deliberately adopts no operation and no selection.
type SkillCompactionReceipt struct {
	Revision  uint64                   `json:"revision"`
	SessionID string                   `json:"session_id"`
	Operation SkillCompactionOperation `json:"operation"`
	Phase     string                   `json:"phase"` // published, delivered, cancelled
	Reason    string                   `json:"reason,omitempty"`
}

// SkillLifecycleSnapshot is the durable skill lifecycle state saved in
// session metadata: the activation inventory, outstanding delivery
// obligations, the pinned-note generation, the operation generation counter,
// and the pending reload selection, compaction operation and handoff receipts.
type SkillLifecycleSnapshot struct {
	Revision         uint64                         `json:"revision"`
	Inventory        map[string]SkillInventoryEntry `json:"inventory"`
	Obligations      []SkillDeliveryObligation      `json:"obligations"`
	PinnedNoteGen    uint64                         `json:"pinned_note_gen"`
	NextOperationGen uint64                         `json:"next_operation_gen"`
	// PendingSelection is the parsed reload selection awaiting consumption by
	// the next successfully published compaction; absent means no selection
	// was made (or a previous one was consumed).
	PendingSelection *SkillReloadSelection `json:"pending_selection,omitempty"`
	// PendingCompaction is the compaction operation owning the current cycle:
	// awaiting publication (phase "pending") or delivery (phase "published").
	// Nil means no compaction intent is outstanding.
	PendingCompaction *SkillCompactionOperation `json:"pending_compaction,omitempty"`
	// PendingHandoffs are the typed handoff receipts this session has
	// recorded — distinct from accepted unpublished intent (PendingCompaction):
	// a competing winner's handoff coexists with a still-pending losing
	// operation. Checkpoint/summary phases of the same winning publication
	// coalesce into its final handoff by publication identity.
	PendingHandoffs []SkillCompactionReceipt `json:"pending_handoffs,omitempty"`
}

// SkillInputRecord is the typed record of one input that carried explicit
// skill selections: the original text and arguments as submitted, the
// canonical names selected, and the atomic group tying preparation to that
// input.
type SkillInputRecord struct {
	OriginalText  string   `json:"original_text"`
	Arguments     string   `json:"arguments"`
	Names         []string `json:"names"`
	AtomicGroupID string   `json:"atomic_group_id"`
	// Prepared lists the typed invocations a successful preparation produced
	// for this selection, in admission order. Nil when preparation failed —
	// a failed input keeps the selection for correction and an explicit
	// new-intent retry, never re-delivery — and on records older than the
	// field. It is what lets a lost admission (a metadata save failure or a
	// crash between the input turn and the obligation persist) be reconciled
	// from the durable input record alone.
	Prepared []SkillSelectionInvocation `json:"prepared,omitempty"`
}

// SkillSelectionInvocation is one typed invocation a successful preparation
// produced for an input's selection: the canonical name it resolved to, the
// identity it admits under, and the route it was prepared for.
type SkillSelectionInvocation struct {
	Name         string `json:"name"`
	InvocationID string `json:"invocation_id"`
	Route        string `json:"route"`
}

// SkillActivationOutcome records operation metadata, not instruction bodies.
// Status is pending, already_present, delivered or failed. An unchanged-body
// satisfaction does not imply a new-body delivery event.
type SkillActivationOutcome struct {
	Revision         uint64                   `json:"revision"`
	SessionID        string                   `json:"session_id"`
	InvocationID     string                   `json:"invocation_id"`
	ToolCallID       string                   `json:"tool_call_id"`
	ClientMutationID string                   `json:"client_mutation_id"`
	Identity         SkillContentIdentity     `json:"identity"`
	Activation       *OrdinarySkillActivation `json:"activation,omitempty"`
	Status           string                   `json:"status"`
	// ErrorCode is source_missing, invalid_metadata, policy_denied, output_limit,
	// context_budget, source_changed or save_failed for failed outcomes.
	ErrorCode        string                   `json:"error_code"`
	PreviousIdentity *SkillContentIdentity    `json:"previous_identity,omitempty"`
	PreviousControls *SkillInvocationControls `json:"previous_controls,omitempty"`
}

// SkillTurnState carries a turn's typed skill records: the selected input,
// activation outcomes, outstanding delivery obligations, and the compaction
// handoff and reload reminder a fold records on its checkpoint turns.
type SkillTurnState struct {
	Input       *SkillInputRecord         `json:"input,omitempty"`
	Outcomes    []SkillActivationOutcome  `json:"outcomes"`
	Obligations []SkillDeliveryObligation `json:"obligations"`
	// Compaction carries the typed handoff receipt a winning fold publication
	// attaches to its checkpoint/summary turns; nil on every other turn.
	Compaction *SkillCompactionReceipt `json:"compaction,omitempty"`
	// ReloadReminder carries the typed inventory notification a compaction
	// handoff records when its selection is absent or invalid; nil on every
	// other turn.
	ReloadReminder *SkillReloadReminder `json:"reload_reminder,omitempty"`
}

// Clone detaches every mutable record and always initializes the inventory.
func (s SkillLifecycleSnapshot) Clone() SkillLifecycleSnapshot {
	out := s
	out.Inventory = make(map[string]SkillInventoryEntry, len(s.Inventory))
	for name, entry := range s.Inventory {
		if entry.Ordinary != nil {
			ordinary := *entry.Ordinary
			entry.Ordinary = &ordinary
		}
		if entry.Preload != nil {
			preload := *entry.Preload
			entry.Preload = &preload
		}
		out.Inventory[name] = entry
	}
	out.Obligations = slices.Clone(s.Obligations)
	if s.PendingSelection != nil {
		selection := *s.PendingSelection
		selection.Names = slices.Clone(selection.Names)
		out.PendingSelection = &selection
	}
	if s.PendingCompaction != nil {
		operation := *s.PendingCompaction
		operation.Selection.Names = slices.Clone(operation.Selection.Names)
		out.PendingCompaction = &operation
	}
	out.PendingHandoffs = make([]SkillCompactionReceipt, len(s.PendingHandoffs))
	for i, handoff := range s.PendingHandoffs {
		handoff.Operation.Selection.Names = slices.Clone(handoff.Operation.Selection.Names)
		out.PendingHandoffs[i] = handoff
	}
	return out
}

// Clone isolates turn metadata from caller-owned input and other turn views.
func (s *SkillTurnState) Clone() *SkillTurnState {
	if s == nil {
		return nil
	}
	out := *s
	if s.Input != nil {
		input := *s.Input
		input.Names = slices.Clone(input.Names)
		input.Prepared = slices.Clone(input.Prepared)
		out.Input = &input
	}
	out.Outcomes = slices.Clone(s.Outcomes)
	for i := range out.Outcomes {
		outcome := &out.Outcomes[i]
		if outcome.Activation != nil {
			activation := *outcome.Activation
			outcome.Activation = &activation
		}
		if outcome.PreviousIdentity != nil {
			identity := *outcome.PreviousIdentity
			outcome.PreviousIdentity = &identity
		}
		if outcome.PreviousControls != nil {
			controls := *outcome.PreviousControls
			outcome.PreviousControls = &controls
		}
	}
	out.Obligations = slices.Clone(s.Obligations)
	if s.Compaction != nil {
		compaction := *s.Compaction
		compaction.Operation.Selection.Names = slices.Clone(compaction.Operation.Selection.Names)
		out.Compaction = &compaction
	}
	if s.ReloadReminder != nil {
		reminder := *s.ReloadReminder
		reminder.Inventory = slices.Clone(s.ReloadReminder.Inventory)
		reminder.Diagnostics = slices.Clone(s.ReloadReminder.Diagnostics)
		reminder.Selection.Names = slices.Clone(s.ReloadReminder.Selection.Names)
		out.ReloadReminder = &reminder
	}
	return &out
}
