package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
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
