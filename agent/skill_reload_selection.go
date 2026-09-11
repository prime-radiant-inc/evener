package agent

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/schema"
)

// parseSkillReloadSelection interprets a raw reload_skills value against the
// session's successful-activation inventory. Presence distinguishes an absent
// selection (empty or null), a valid one (including an explicit empty array,
// meaning reload none), and an invalid one (malformed or naming a skill the
// inventory does not hold). Valid names keep request order with duplicates
// collapsed.
func parseSkillReloadSelection(raw json.RawMessage, inventory map[string]schema.SkillInventoryEntry) schema.SkillReloadSelection {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return schema.SkillReloadSelection{State: "absent"}
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return schema.SkillReloadSelection{State: "invalid", ErrorCode: "invalid_selection"}
	}
	result := schema.SkillReloadSelection{State: "valid", Names: []string{}}
	seen := map[string]bool{}
	for _, name := range names {
		if _, ok := inventory[name]; !ok {
			return schema.SkillReloadSelection{State: "invalid", ErrorCode: "unknown_skill"}
		}
		if !seen[name] {
			seen[name] = true
			result.Names = append(result.Names, name)
		}
	}
	return result
}

const (
	skillReloadSelectionOpen  = "<skill-reload-selection>"
	skillReloadSelectionClose = "</skill-reload-selection>"
)

// parseSkillReloadElicitation splits an elicited note into its free-text note
// and the reload selection carried by one <skill-reload-selection> block whose
// JSON object has a reload_skills field. Only a well-formed block (valid JSON,
// at most one block, selection not invalid) is removed from the handed-forward
// note; a missing, multiple, or malformed block authorizes no body load and
// preserves the note verbatim. Incidental skill mentions never count.
func parseSkillReloadElicitation(text string, inventory map[string]schema.SkillInventoryEntry) (string, schema.SkillReloadSelection) {
	invalid := func() (string, schema.SkillReloadSelection) {
		return text, schema.SkillReloadSelection{State: "invalid", ErrorCode: "invalid_selection"}
	}
	open := strings.Index(text, skillReloadSelectionOpen)
	if open < 0 {
		return text, schema.SkillReloadSelection{State: "absent"}
	}
	innerStart := open + len(skillReloadSelectionOpen)
	rel := strings.Index(text[innerStart:], skillReloadSelectionClose)
	if rel < 0 {
		return invalid() // an unterminated block is malformed
	}
	innerEnd := innerStart + rel
	rest := text[innerEnd+len(skillReloadSelectionClose):]
	if strings.Contains(rest, skillReloadSelectionOpen) {
		return invalid() // exactly one block may authorize a selection
	}
	var block struct {
		ReloadSkills json.RawMessage `json:"reload_skills"`
	}
	if err := json.Unmarshal([]byte(text[innerStart:innerEnd]), &block); err != nil {
		return invalid()
	}
	selection := parseSkillReloadSelection(block.ReloadSkills, inventory)
	if selection.State == "invalid" {
		return text, selection
	}
	return strings.TrimSpace(text[:open] + rest), selection
}

// skillInventorySnapshot returns a detached copy of the session's successful
// skill inventory (activation metadata only, never bodies).
func (s *Session) skillInventorySnapshot() map[string]schema.SkillInventoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.skillLifecycle.Inventory)
}

// pendingSkillReloadSelection reports the parsed selection awaiting the next
// published compaction; absent when none was recorded.
func (s *Session) pendingSkillReloadSelection() schema.SkillReloadSelection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillLifecycle.PendingSelection == nil {
		return schema.SkillReloadSelection{State: "absent"}
	}
	return *s.skillLifecycle.PendingSelection
}

// setPendingSkillReloadSelection records the parsed selection for the current
// compaction cycle. An absent selection clears the slot; consumption and
// cancellation live with the fold publication transaction.
func (s *Session) setPendingSkillReloadSelection(selection schema.SkillReloadSelection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if selection.State == "absent" {
		s.skillLifecycle.PendingSelection = nil
		return
	}
	selection.Names = slices.Clone(selection.Names)
	s.skillLifecycle.PendingSelection = &selection
}

// skillInventorySummaries flattens the inventory into deterministic,
// elicitation-ready loaded-skill metadata sorted by canonical name.
func skillInventorySummaries(inventory map[string]schema.SkillInventoryEntry) []schema.SkillInventorySummary {
	names := slices.Sorted(maps.Keys(inventory))
	summaries := make([]schema.SkillInventorySummary, 0, len(names))
	for _, name := range names {
		entry := inventory[name]
		summary := schema.SkillInventorySummary{
			Name:        name,
			HasOrdinary: entry.Ordinary != nil,
			HasPreload:  entry.Preload != nil,
		}
		switch {
		case summary.HasOrdinary && summary.HasPreload:
			summary.Availability = "ordinary+preload"
		case summary.HasOrdinary:
			summary.Availability = "ordinary"
		case summary.HasPreload:
			summary.Availability = "preload"
		}
		if entry.Ordinary != nil {
			summary.Description = entry.Ordinary.Description
		} else if entry.Preload != nil {
			summary.Description = entry.Preload.Description
		}
		summaries = append(summaries, summary)
	}
	return summaries
}
