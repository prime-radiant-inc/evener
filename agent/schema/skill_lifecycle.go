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
	Name         string
	Description  string
	Availability string
	HasOrdinary  bool
	HasPreload   bool
}

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
}

type SkillInputRecord struct {
	OriginalText  string   `json:"original_text"`
	Arguments     string   `json:"arguments"`
	Names         []string `json:"names"`
	AtomicGroupID string   `json:"atomic_group_id"`
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

type SkillTurnState struct {
	Input       *SkillInputRecord         `json:"input,omitempty"`
	Outcomes    []SkillActivationOutcome  `json:"outcomes"`
	Obligations []SkillDeliveryObligation `json:"obligations"`
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
	return &out
}
