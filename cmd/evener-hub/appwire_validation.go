package hub

import (
	"context"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func validateAppWireInputItems(items []appwire.InputItem) error {
	return validateAppWireInputItemsWithExisting(items, 0)
}

func validateAppWireInputItemsWithExisting(items []appwire.InputItem, imageCount int) error {
	for i, it := range items {
		if !isImageInputItem(it) {
			continue
		}
		imageCount++
		if imageCount > hubcore.SendMaxImageItems {
			return fmt.Errorf("items exceeds %d-image limit", hubcore.SendMaxImageItems)
		}
		if len(it.Data) > hubcore.SendMaxImageBytes {
			return fmt.Errorf("items[%d] %q exceeds %d-byte limit", i, it.Name, hubcore.SendMaxImageBytes)
		}
	}
	return nil
}

func isImageInputItem(it appwire.InputItem) bool {
	return it.Type == "image" || it.Type == "input_image"
}

// containsSkillInputItem reports whether an input carries a canonical skill
// selection, the condition under which the hub must prove the target's
// SkillInput capability before forwarding.
func containsSkillInputItem(items []appwire.InputItem) bool {
	for _, item := range items {
		if item.Type == "skill" {
			return true
		}
	}
	return false
}

// ensureSkillInputSupported gates a skill-bearing input on the capability the
// target itself advertises, mirroring ensureThreadActionAvailable: the read
// carries no subscription and no turns, only the capability verdict. Fail
// closed — a target whose capability cannot be read, or that advertises none
// (an older daemon), cannot authorize a skill selection. The selection is
// rejected verbatim rather than degraded into slash prose or a text-only
// mutation, so the client keeps what it composed for an explicit retry.
func ensureSkillInputSupported(ctx context.Context, source appsource.Source, ref, threadID string, input []appwire.InputItem) error {
	if !containsSkillInputItem(input) {
		return nil
	}
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, ThreadID: threadID, IncludeTurns: false})
	if err != nil {
		return err
	}
	if err := appwire.ValidateSkillInputSupport(input, resp.Thread.Evener.Capabilities.SkillInput); err != nil {
		return appwire.InvalidParams(err.Error())
	}
	return nil
}
