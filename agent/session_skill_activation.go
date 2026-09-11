package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

// skillInvocation is server-created operation metadata. Only genuine user
// dispatch may assign user_slash or user_selection; client fields, model text,
// copied history and role input cannot establish that provenance.
type skillInvocation struct {
	Name, Route, InvocationID, ToolCallID, ClientMutationID, AtomicGroupID string
	Source                                                                 *schema.SkillContentIdentity // Non-nil only for continuation of this exact source.
}

type preparedSkillActivation struct {
	Invocation skillInvocation
	Loaded     skill.LoadedSkill
	Rendered   skill.RenderedSkill
}

type skillActivationBatch struct{ Items []preparedSkillActivation }

// durableSkillSelection carries a claimed durable input's prepared skill
// selection into the turn that consumes it. Err holds the preparation failure
// for a selection that could not be prepared: the turn records a visible
// failed input instead of dispatching dependent work, and the Selection still
// keeps the names and original prose.
type durableSkillSelection struct {
	Selection *schema.SkillInputRecord
	Batch     *skillActivationBatch
	Err       error
}

type durableSkillSelectionContextKey struct{}

func withDurableSkillSelection(ctx context.Context, selection *durableSkillSelection) context.Context {
	return context.WithValue(ctx, durableSkillSelectionContextKey{}, selection)
}

func durableSkillSelectionFromContext(ctx context.Context) *durableSkillSelection {
	selection, _ := ctx.Value(durableSkillSelectionContextKey{}).(*durableSkillSelection)
	return selection
}

// durableSkillGroupID returns the durable identity one consumed input's
// invocations are grouped under: the queue entry's stable ID when the input
// was queued, otherwise the stable turn the mutation reserved (turn/start and
// every steering mutation reserve one), and the mutation ID as a last resort.
func durableSkillGroupID(input queuedInput) string {
	if input.ID != "" {
		return input.ID
	}
	if input.StableTurnID != "" {
		return input.StableTurnID
	}
	return input.ClientMutationID
}

// skillInputRecordFromQueued builds the typed input record for a durable
// selection: the user's original prose, the canonical names, and the atomic
// group identity the consumption tied the invocations to.
func skillInputRecordFromQueued(input queuedInput) *schema.SkillInputRecord {
	return &schema.SkillInputRecord{
		OriginalText:  input.Text,
		Names:         append([]string(nil), input.SkillNames...),
		AtomicGroupID: durableSkillGroupID(input),
	}
}

// prepareSelectedInput prepares the skill activations a durable client input
// selected, as ONE atomic group tied to the input's durable identity: every
// invocation shares the input's group ID and carries the causal client
// mutation. Catalog.ResolveExact pins the canonical identity the client named
// -- a bare suffix or unknown name is a consumption-time failure, never a
// silent retarget to a different collision winner. Route is supplied by the
// trusted session caller. Preparation, policy, and combined final admission
// are all-or-nothing: preparation publishes no inventory, so a failure needs
// no rollback.
func (s *Session) prepareSelectedInput(ctx context.Context, input queuedInput, route string) (*skillActivationBatch, error) {
	if len(input.SkillNames) == 0 {
		return nil, nil
	}
	inputID := durableSkillGroupID(input)
	invocations := make([]skillInvocation, 0, len(input.SkillNames))
	for _, name := range input.SkillNames {
		descriptor, err := s.skills.ResolveExact(name)
		if err != nil {
			return nil, &skillActivationError{
				Invocation: skillInvocation{Name: name, Route: route},
				Code:       "source_missing",
				Err:        err,
			}
		}
		invocations = append(invocations, skillInvocation{
			Name:             descriptor.CatalogName,
			Route:            route,
			InvocationID:     inputID + ":" + name,
			ClientMutationID: input.ClientMutationID,
			AtomicGroupID:    inputID,
		})
	}
	return s.prepareSkillActivations(ctx, invocations)
}

// contextWithSelectedSkills prepares a claimed durable input's skill selection
// and hands it to the consuming turn through the context. A preparation
// failure rides the same value: the turn records the visible failed input and
// dispatches no dependent work.
func (s *Session) contextWithSelectedSkills(ctx context.Context, queued queuedInput) context.Context {
	if len(queued.SkillNames) == 0 {
		return ctx
	}
	batch, err := s.prepareSelectedInput(ctx, queued, "user_selection")
	return withDurableSkillSelection(ctx, &durableSkillSelection{
		Selection: skillInputRecordFromQueued(queued),
		Batch:     batch,
		Err:       err,
	})
}

// admitSteeringSelectionBatch admits a prepared selection a consumed steering
// message carried. Failure warns: the steering turn is already durably
// delivered, and the obligation machinery revalidates at the next dispatch.
func (s *Session) admitSteeringSelectionBatch(batch *skillActivationBatch) {
	if batch == nil {
		return
	}
	if err := s.admitSkillActivationBatch(batch); err != nil {
		s.emit(events.EventWarning, warningDataFromError("admitting steering skill selection failed", err))
	}
}

// skillActivationError preserves machine-readable failure identity and the
// original error for callers. Preparation never claims a successful delivery.
type skillActivationError struct {
	Invocation skillInvocation
	Code       string
	Err        error
}

func (e *skillActivationError) Error() string {
	return fmt.Sprintf("skill %q (%s): %v", e.Invocation.Name, e.Code, e.Err)
}

func (e *skillActivationError) Unwrap() error { return e.Err }

func skillInvocationAllowed(route string, controls skill.InvocationControls, authorized bool) bool {
	switch route {
	case "user_slash", "user_selection":
		return controls.UserInvocable
	case "model_tool", "compaction_reload":
		return authorized || !controls.DisableModelInvocation
	case "role_preload":
		return true
	default:
		return false
	}
}

// prepareSkillActivations reads complete sources and checks current controls.
// It neither changes inventory/obligations nor persists or publishes success.
// Final admission and obligation satisfaction are separate transactions.
func (s *Session) prepareSkillActivations(ctx context.Context, invocations []skillInvocation) (*skillActivationBatch, error) {
	batch := &skillActivationBatch{Items: make([]preparedSkillActivation, 0, len(invocations))}
	for _, invocation := range invocations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var descriptor skill.Descriptor
		if source := invocation.Source; source != nil {
			// A legacy name/body has no reliable declared identity or source. Never
			// infer these from the current catalog or silently retarget a collision.
			if source.Name == "" || source.DeclaredName == "" || source.Source == "" {
				return nil, &skillActivationError{Invocation: invocation, Code: "invalid_metadata", Err: errors.New("continuation requires recorded name, declared name and source")}
			}
			copiedSource := *source
			invocation.Source = &copiedSource
			descriptor = skill.Descriptor{CatalogName: source.Name, Meta: skill.SkillMeta{Name: source.DeclaredName, SkillFile: source.Source, Dir: filepath.Dir(source.Source)}}
		} else {
			s.mu.Lock()
			resolved, err := s.skills.Resolve(invocation.Name)
			s.mu.Unlock()
			if err != nil {
				code := "source_missing"
				var resolution *skill.ResolutionError
				if errors.As(err, &resolution) && resolution.Kind == "ambiguous" {
					code = "invalid_metadata"
				}
				return nil, &skillActivationError{Invocation: invocation, Code: code, Err: err}
			}
			descriptor = resolved
		}
		loaded, diagnostics, err := skill.Load(descriptor)
		if err != nil {
			code := "invalid_metadata"
			for _, diagnostic := range diagnostics {
				switch diagnostic.Category {
				case "unreadable_source":
					code = "source_missing"
				case "source_identity_changed":
					code = "source_changed"
				}
			}
			return nil, &skillActivationError{Invocation: invocation, Code: code, Err: err}
		}
		s.mu.Lock()
		entry := s.skillLifecycle.Inventory[loaded.Descriptor.CatalogName]
		authorized := entry.Ordinary != nil && entry.Ordinary.UserAuthorized &&
			entry.Ordinary.Identity.Name == loaded.Descriptor.CatalogName &&
			entry.Ordinary.Identity.Source == loaded.Descriptor.Meta.SkillFile
		s.mu.Unlock()
		if !skillInvocationAllowed(invocation.Route, loaded.Descriptor.Controls, authorized) {
			return nil, &skillActivationError{Invocation: invocation, Code: "policy_denied", Err: errors.New("current invocation controls deny this route")}
		}
		rendered := skill.Render(loaded)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch.Items = append(batch.Items, preparedSkillActivation{Invocation: invocation, Loaded: loaded, Rendered: rendered})
	}
	return batch, nil
}

// mintSkillOperationID assigns the session's next persisted lifecycle
// operation identity. The counter lives in the lifecycle snapshot so an
// identity is never reused across a restart; it is not random and never comes
// from client fields, model text, or copied history.
func (s *Session) mintSkillOperationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skillLifecycle.NextOperationGen++
	return fmt.Sprintf("skill-op-%d", s.skillLifecycle.NextOperationGen)
}

// skillContentIdentity pins a prepared activation's canonical name, declared
// identity, exact source, and content digests.
func skillContentIdentity(item preparedSkillActivation) schema.SkillContentIdentity {
	return schema.SkillContentIdentity{
		Name:           item.Loaded.Descriptor.CatalogName,
		DeclaredName:   item.Loaded.Descriptor.Meta.Name,
		Source:         item.Loaded.Descriptor.Meta.SkillFile,
		FileDigest:     item.Loaded.Digest,
		RenderedDigest: item.Rendered.Digest,
	}
}

// activationAuthorization decides whether a successful activation records
// explicit user authorization. Only genuine user routes grant it; a
// continuation of the same source preserves the prior record's answer.
func activationAuthorization(route string, prior *schema.OrdinarySkillActivation, identity schema.SkillContentIdentity) bool {
	if route == "user_slash" || route == "user_selection" {
		return true
	}
	if prior != nil && prior.Identity.Name == identity.Name && prior.Identity.Source == identity.Source {
		return prior.UserAuthorized
	}
	return false
}

// ordinaryActivationRecord builds the inventory record a successful final
// admission writes. It carries operation metadata, never instruction bodies.
func ordinaryActivationRecord(item preparedSkillActivation, prior *schema.OrdinarySkillActivation) schema.OrdinarySkillActivation {
	identity := skillContentIdentity(item)
	return schema.OrdinarySkillActivation{
		Identity:    identity,
		Description: item.Loaded.Descriptor.Meta.Description,
		Controls: schema.SkillInvocationControls{
			DisableModelInvocation: item.Loaded.Descriptor.Controls.DisableModelInvocation,
			UserInvocable:          item.Loaded.Descriptor.Controls.UserInvocable,
		},
		Route:          item.Invocation.Route,
		InvocationID:   item.Invocation.InvocationID,
		UserAuthorized: activationAuthorization(item.Invocation.Route, prior, identity),
	}
}

// admitSkillActivationBatch admits a prepared user-route batch: each
// activation's complete rendered instructions are recorded as a separately
// identifiable typed context turn, and the pending delivery obligations are
// persisted before any dispatch or restart can lose them. An activation whose
// identical content is already recorded omits the duplicate body but keeps
// its obligation. No success event fires here: EventSkillActivated is
// published only by the final dispatch admission (commitSkillDelivery).
func (s *Session) admitSkillActivationBatch(batch *skillActivationBatch) error {
	if batch == nil {
		return nil
	}
	for _, item := range batch.Items {
		identity := skillContentIdentity(item)
		obligation := schema.SkillDeliveryObligation{
			InvocationID:     item.Invocation.InvocationID,
			ToolCallID:       item.Invocation.ToolCallID,
			ClientMutationID: item.Invocation.ClientMutationID,
			AtomicGroupID:    item.Invocation.AtomicGroupID,
			Identity:         identity,
			Route:            item.Invocation.Route,
		}
		s.mu.Lock()
		prior := s.skillLifecycle.Inventory[identity.Name].Ordinary
		duplicate := prior != nil && prior.Identity == identity
		s.skillLifecycle.Obligations = append(s.skillLifecycle.Obligations, obligation)
		s.skillLifecycle.Revision++
		s.mu.Unlock()
		if duplicate {
			// Identical complete content was already admitted; the obligation
			// stands and final dispatch revalidation decides whether the carrier
			// survived. No duplicate body joins the history.
			continue
		}
		outcome := schema.SkillActivationOutcome{
			SessionID:        s.id,
			InvocationID:     item.Invocation.InvocationID,
			ToolCallID:       item.Invocation.ToolCallID,
			ClientMutationID: item.Invocation.ClientMutationID,
			Identity:         identity,
			Status:           "pending",
		}
		carrier := schema.NewTurn(schema.TurnSystem, llm.User(item.Rendered.Content))
		carrier.SkillState = &schema.SkillTurnState{
			Outcomes:    []schema.SkillActivationOutcome{outcome},
			Obligations: []schema.SkillDeliveryObligation{obligation},
		}
		s.recordTurn(carrier, carrier)
	}
	// Obligations must be durable before a retry or restart can lose them.
	if err := s.saveMeta(); err != nil {
		s.emit(events.EventWarning, warningDataFromError("saving skill delivery obligations failed", err))
		return err
	}
	return nil
}

// frozenPreloadRecord builds the typed provenance metadata for one fresh
// role preload from its prepared activation.
func frozenPreloadRecord(item preparedSkillActivation) schema.FrozenSkillPreload {
	return schema.FrozenSkillPreload{
		Name:           item.Loaded.Descriptor.CatalogName,
		Description:    item.Loaded.Descriptor.Meta.Description,
		Source:         item.Loaded.Descriptor.Meta.SkillFile,
		FileDigest:     item.Loaded.Digest,
		RenderedDigest: item.Rendered.Digest,
	}
}

// seedFrozenSkillPreloads enters this session's role preloads into the skill
// lifecycle inventory. It runs only after the permanent prompt carrying them
// was admitted. Preloads keep independent provenance and never grant ordinary
// invocation authorization; an existing record (restored state) is never
// overwritten.
func (s *Session) seedFrozenSkillPreloads() {
	if len(s.cfg.spawn.frozenSkillMetadata) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, metadata := range s.cfg.spawn.frozenSkillMetadata {
		if metadata.Name == "" {
			continue
		}
		entry := s.skillLifecycle.Inventory[metadata.Name]
		if entry.Preload != nil {
			continue
		}
		record := metadata
		entry.Preload = &record
		s.skillLifecycle.Inventory[metadata.Name] = entry
		changed = true
	}
	if changed {
		s.skillLifecycle.Revision++
	}
}

// skillToolState is the typed pending identity a use_skill execution hands
// the session through the tool-state side channel (TOOL_CALL_END's tool_state;
// never model-facing prose). The round's tool-results append decodes it into
// the turn's SkillTurnState and persists the obligation.
type skillToolState struct {
	Outcome    schema.SkillActivationOutcome   `json:"outcome"`
	Obligation *schema.SkillDeliveryObligation `json:"obligation,omitempty"`
}

// skillActivationErrorCode extracts the machine-readable failure code.
func skillActivationErrorCode(err error) string {
	var failure *skillActivationError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return "invalid_metadata"
}

// skillToolActivate is the use_skill handler body: it resolves against the
// full canonical catalog (hidden authorized names stay resolvable), prepares
// the activation through the shared loader/renderer under current controls,
// and returns the complete rendered document with its pending identity. A
// successful identical prior activation yields a provisional already-present
// notice; the delivery obligation stands either way. No success event fires
// here — EventSkillActivated belongs to final dispatch admission.
func (s *Session) skillToolActivate(ctx context.Context, skillName, toolCallID string) (any, error) {
	descriptor, err := s.skills.Resolve(skillName)
	if err != nil {
		return nil, err
	}
	invocationID := s.mintSkillOperationID()
	invocation := skillInvocation{
		Name:          descriptor.CatalogName,
		Route:         "model_tool",
		InvocationID:  invocationID,
		ToolCallID:    toolCallID,
		AtomicGroupID: invocationID,
	}
	batch, err := s.prepareSkillActivations(ctx, []skillInvocation{invocation})
	if err != nil {
		return nil, err
	}
	item := batch.Items[0]
	identity := skillContentIdentity(item)
	s.mu.Lock()
	prior := s.skillLifecycle.Inventory[identity.Name].Ordinary
	provisional := prior != nil && prior.Identity == identity
	s.mu.Unlock()
	activation := ordinaryActivationRecord(item, prior)
	outcome := schema.SkillActivationOutcome{
		SessionID:    s.id,
		InvocationID: invocationID,
		ToolCallID:   toolCallID,
		Identity:     identity,
		Activation:   &activation,
		Status:       "pending",
	}
	obligation := &schema.SkillDeliveryObligation{
		InvocationID:  invocationID,
		ToolCallID:    toolCallID,
		AtomicGroupID: invocationID,
		Identity:      identity,
		Route:         "model_tool",
	}
	if provisional {
		// The hit is provisional: compaction or projection can still remove the
		// body before dispatch. The obligation stands until final admission.
		outcome.Status = "already_present"
		return tool.StateResult{
			Output: systemNotificationf("Skill %q is already active with identical content (source %s); its complete instructions remain in this conversation.", identity.Name, identity.Source),
			State:  skillToolState{Outcome: outcome, Obligation: obligation},
		}, nil
	}
	return tool.StateResult{
		Output:                item.Rendered.Content,
		RequireCompleteOutput: true,
		State:                 skillToolState{Outcome: outcome, Obligation: obligation},
	}, nil
}
