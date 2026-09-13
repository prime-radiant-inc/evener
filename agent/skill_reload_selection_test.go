package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestSkillReloadSelection_Presence is the exact selection presence table:
// absent (empty/null) vs valid (including an explicit empty array) vs invalid
// (malformed or unknown name), with ordered deduplication.
func TestSkillReloadSelection_Presence(t *testing.T) {
	inventory := map[string]schema.SkillInventoryEntry{"pkg:probe": {}}
	for _, tc := range []struct {
		raw, state string
		names      []string
	}{
		{"", "absent", nil}, {"null", "absent", nil}, {"[]", "valid", []string{}},
		{`["pkg:probe","pkg:probe"]`, "valid", []string{"pkg:probe"}},
		{`["missing"]`, "invalid", nil}, {`"pkg:probe"`, "invalid", nil},
	} {
		got := parseSkillReloadSelection(json.RawMessage(tc.raw), inventory)
		if got.State != tc.state || !reflect.DeepEqual(got.Names, tc.names) {
			t.Fatalf("raw=%q result=%+v", tc.raw, got)
		}
	}
}

// TestSkillReloadSelection_ErrorCodes pins the machine error codes that
// distinguish a malformed selection from an unknown skill name.
func TestSkillReloadSelection_ErrorCodes(t *testing.T) {
	inventory := map[string]schema.SkillInventoryEntry{"pkg:probe": {}}
	for _, tc := range []struct {
		raw, errCode string
	}{
		{"", ""}, {"null", ""}, {"[]", ""},
		{`["missing"]`, "unknown_skill"},
		{`"pkg:probe"`, "invalid_selection"},
		{`["pkg:probe",1]`, "invalid_selection"},
		{`[`, "invalid_selection"},
	} {
		got := parseSkillReloadSelection(json.RawMessage(tc.raw), inventory)
		if got.ErrorCode != tc.errCode {
			t.Fatalf("raw=%q ErrorCode=%q, want %q (result %+v)", tc.raw, got.ErrorCode, tc.errCode, got)
		}
	}
}

// TestSkillReloadElicitation_Blocks covers the elicitation protocol: exactly
// one <skill-reload-selection> block carrying a JSON object with reload_skills.
// Only a valid block is removed from the handed-forward note; missing,
// multiple, or malformed blocks authorize no body load and preserve the note.
func TestSkillReloadElicitation_Blocks(t *testing.T) {
	inventory := map[string]schema.SkillInventoryEntry{"pkg:probe": {}}
	block := func(inner string) string {
		return "<skill-reload-selection>" + inner + "</skill-reload-selection>"
	}
	for _, tc := range []struct {
		name, text, note, state, errCode string
		names                            []string
	}{
		{
			name:  "single valid block removed from opaque free text",
			text:  "keep the token OPAQUE-77\n\n" + block(`{"reload_skills":["pkg:probe","pkg:probe"]}`) + "\n",
			note:  "keep the token OPAQUE-77",
			state: "valid", names: []string{"pkg:probe"},
		},
		{
			name:  "no block leaves note untouched",
			text:  "just a note with no selection",
			note:  "just a note with no selection",
			state: "absent",
		},
		{
			name:    "multiple blocks authorize nothing and preserve the note",
			text:    block(`{"reload_skills":["pkg:probe"]}`) + "\n" + block(`{"reload_skills":[]}`),
			note:    block(`{"reload_skills":["pkg:probe"]}`) + "\n" + block(`{"reload_skills":[]}`),
			state:   "invalid",
			errCode: "invalid_selection",
		},
		{
			name:    "malformed JSON preserves the note",
			text:    "note text\n" + block(`{"reload_skills":["pkg:probe"]`),
			note:    "note text\n" + block(`{"reload_skills":["pkg:probe"]`),
			state:   "invalid",
			errCode: "invalid_selection",
		},
		{
			name:    "unknown names preserve the valid note",
			text:    "keep the token OPAQUE-77\n" + block(`{"reload_skills":["pkg:missing"]}`),
			note:    "keep the token OPAQUE-77\n" + block(`{"reload_skills":["pkg:missing"]}`),
			state:   "invalid",
			errCode: "unknown_skill",
		},
		{
			name:  "incidental skill mentions are not a selection",
			text:  "remember to lean on pkg:probe and its reload_skills behavior later",
			note:  "remember to lean on pkg:probe and its reload_skills behavior later",
			state: "absent",
		},
		{
			name:  "a lone closing tag is prose, not a block",
			text:  "the marker </skill-reload-selection> closes the block",
			note:  "the marker </skill-reload-selection> closes the block",
			state: "absent",
		},
		{
			name:    "an unterminated block is malformed",
			text:    "note text\n<skill-reload-selection>" + `{"reload_skills":[]}`,
			note:    "note text\n<skill-reload-selection>" + `{"reload_skills":[]}`,
			state:   "invalid",
			errCode: "invalid_selection",
		},
		{
			name:  "null selection is absent but the well-formed block is removed",
			text:  "keep the token OPAQUE-77\n" + block(`{"reload_skills":null}`),
			note:  "keep the token OPAQUE-77",
			state: "absent",
		},
		{
			name:  "selection-only explicit empty array reloads none",
			text:  block(`{"reload_skills":[]}`),
			note:  "",
			state: "valid", names: []string{},
		},
		{
			name:    "a non-array selection is invalid and preserves the note",
			text:    "note text\n" + block(`{"reload_skills":"pkg:probe"}`),
			note:    "note text\n" + block(`{"reload_skills":"pkg:probe"}`),
			state:   "invalid",
			errCode: "invalid_selection",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note, sel := parseSkillReloadElicitation(tc.text, inventory)
			if note != tc.note || sel.State != tc.state || !reflect.DeepEqual(sel.Names, tc.names) || sel.ErrorCode != tc.errCode {
				t.Fatalf("text=%q\nnote=%q want %q\nselection=%+v want state=%q names=%v err=%q",
					tc.text, note, tc.note, sel, tc.state, tc.names, tc.errCode)
			}
		})
	}
}

// TestSkillReloadSelection_CompactToolStoresValidSelection drives the real
// compact_context handler: a schema-valid call with a selection pins the note,
// schedules the compaction, and records the parsed (ordered, deduplicated)
// selection as pending operation metadata.
func TestSkillReloadSelection_CompactToolStoresValidSelection(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	rt := s.reg.Get("compact_context")
	if rt == nil {
		t.Fatal("compact tool not registered")
	}
	_, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":  "keep the plan",
		"reload_skills": []any{"scope:probe", "scope:probe"},
	})
	if err != nil {
		t.Fatalf("compact exec: %v", err)
	}
	if s.PinnedNote() != "keep the plan" {
		t.Fatalf("note not pinned: %q", s.PinnedNote())
	}
	if _, ok := s.takeForceRequest(); !ok {
		t.Fatal("compaction not scheduled")
	}
	sel := s.pendingSkillReloadSelection()
	if sel.State != "valid" || !reflect.DeepEqual(sel.Names, []string{"scope:probe"}) {
		t.Fatalf("pending selection = %+v, want valid [scope:probe]", sel)
	}
}

// TestSkillReloadSelection_CompactToolUnknownNameKeepsNote proves an accepted
// call whose selection names an unknown skill still pins the valid note and
// schedules the compaction; only the selection comes out invalid.
func TestSkillReloadSelection_CompactToolUnknownNameKeepsNote(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rt := s.reg.Get("compact_context")
	_, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":  "keep the plan",
		"reload_skills": []any{"scope:missing"},
	})
	if err != nil {
		t.Fatalf("compact exec: %v", err)
	}
	if s.PinnedNote() != "keep the plan" {
		t.Fatalf("invalid selection must not discard the valid note, got %q", s.PinnedNote())
	}
	if _, ok := s.takeForceRequest(); !ok {
		t.Fatal("compaction not scheduled")
	}
	sel := s.pendingSkillReloadSelection()
	if sel.State != "invalid" || sel.ErrorCode != "unknown_skill" {
		t.Fatalf("pending selection = %+v, want invalid/unknown_skill", sel)
	}
}

// TestSkillReloadSelection_CompactToolSelectionOnlyRequestsCompaction: an
// explicit empty selection with an empty note still requests the compaction
// (selection presence is independent of note presence), while an absent or
// null selection keeps today's clear-note-without-compaction behavior.
func TestSkillReloadSelection_CompactToolSelectionOnlyRequestsCompaction(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rt := s.reg.Get("compact_context")
	s.setPinnedNote("old")
	if _, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":  "",
		"reload_skills": []any{},
	}); err != nil {
		t.Fatalf("selection-only exec: %v", err)
	}
	if s.PinnedNote() != "" {
		t.Fatalf("empty note must clear, got %q", s.PinnedNote())
	}
	if _, ok := s.takeForceRequest(); !ok {
		t.Fatal("an explicit empty selection must still request the compaction")
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "valid" || len(sel.Names) != 0 {
		t.Fatalf("pending selection = %+v, want valid empty", sel)
	}

	if _, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":  "",
		"reload_skills": nil,
	}); err != nil {
		t.Fatalf("null-selection exec: %v", err)
	}
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("a null selection is absent: clear-note must not request a compaction")
	}
}

// TestSkillReloadSelection_CompactToolDoubleCallKeepsFirstSelection: a rejected
// second pending request overwrites neither the first note nor its selection.
func TestSkillReloadSelection_CompactToolDoubleCallKeepsFirstSelection(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	rt := s.reg.Get("compact_context")
	if _, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":  "first",
		"reload_skills": []any{"scope:probe"},
	}); err != nil {
		t.Fatalf("first compact: %v", err)
	}
	if _, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":  "second",
		"reload_skills": []any{},
	}); err == nil {
		t.Fatal("second compact in the same round must error")
	}
	if got := s.PinnedNote(); got != "first" {
		t.Fatalf("rejected double-call mutated the note: %q", got)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "valid" || !reflect.DeepEqual(sel.Names, []string{"scope:probe"}) {
		t.Fatalf("rejected double-call mutated the selection: %+v", sel)
	}
}

// TestSkillReloadSelection_CompactToolSchemaShape pins the wire contract of
// the compact_context parameters: note_to_self stays required and reload_skills
// is an optional array whose elements validate as strings.
func TestSkillReloadSelection_CompactToolSchemaShape(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rt := s.reg.Get("compact_context")
	if rt == nil {
		t.Fatal("compact tool not registered")
	}
	props, _ := rt.Definition.Parameters["properties"].(map[string]any)
	sel, _ := props["reload_skills"].(map[string]any)
	if sel["type"] != "array" {
		t.Fatalf("reload_skills schema = %v, want an array property", sel)
	}
	items, _ := sel["items"].(map[string]any)
	if items["type"] != "string" {
		t.Fatalf("reload_skills items schema = %v, want string elements", items)
	}
	required, _ := rt.Definition.Parameters["required"].([]string)
	if !reflect.DeepEqual(required, []string{"note_to_self"}) {
		t.Fatalf("required = %v, want exactly [note_to_self]", required)
	}
}

// TestSkillReloadSelection_CompactToolSchemaInvalidSchedulesNothing drives the
// real registry dispatch: reload_skills with a non-string element (or a missing
// required note_to_self) fails schema validation, so the handler never runs and
// no compaction is scheduled.
func TestSkillReloadSelection_CompactToolSchemaInvalidSchedulesNothing(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	for _, tc := range []struct{ name, args string }{
		{"non-string element", `{"note_to_self":"keep","reload_skills":[42]}`},
		{"missing note_to_self", `{"reload_skills":[]}`},
		{"selection not an array", `{"note_to_self":"keep","reload_skills":"scope:probe"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := s.reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
				ID:        "call-" + tc.name,
				Name:      "compact_context",
				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("schema-invalid call was not rejected: %q", res.Output)
			}
			if s.PinnedNote() != "" {
				t.Fatalf("schema-invalid call pinned a note: %q", s.PinnedNote())
			}
			if _, ok := s.takeForceRequest(); ok {
				t.Fatal("schema-invalid call scheduled a compaction")
			}
		})
	}
}

// TestSkillReloadElicitation_SessionParsesBlockAndStoresSelection runs the real
// maybeElicitNoteBeforeCompaction path: the elicited free-text note is pinned
// without the valid selection block, and the parsed selection is recorded.
func TestSkillReloadElicitation_SessionParsesBlockAndStoresSelection(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		return "keep the token OPAQUE-77\n<skill-reload-selection>{\"reload_skills\":[\"scope:probe\"]}</skill-reload-selection>", nil
	}
	seedSessionHistory(t, s, 10)
	forcePressureAbove(t, s, s.contextMgr.CheckpointThreshold)

	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if got := s.PinnedNote(); got != "keep the token OPAQUE-77" {
		t.Fatalf("pinned note = %q, want the free text without the selection block", got)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "valid" || !reflect.DeepEqual(sel.Names, []string{"scope:probe"}) {
		t.Fatalf("pending selection = %+v, want valid [scope:probe]", sel)
	}
}

// TestSkillReloadElicitation_SessionInvalidSelectionPreservesNote: an
// elicitation whose block is invalid pins the note verbatim (block included)
// and records the invalid selection — it never authorizes a body load.
func TestSkillReloadElicitation_SessionInvalidSelectionPreservesNote(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	const raw = "keep the token OPAQUE-77\n<skill-reload-selection>{\"reload_skills\":[\"scope:missing\"]}</skill-reload-selection>"
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return raw, nil }
	seedSessionHistory(t, s, 10)
	forcePressureAbove(t, s, s.contextMgr.CheckpointThreshold)

	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if got := s.PinnedNote(); got != raw {
		t.Fatalf("invalid parsing must preserve the note verbatim, got %q", got)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "invalid" || sel.ErrorCode != "unknown_skill" {
		t.Fatalf("pending selection = %+v, want invalid/unknown_skill", sel)
	}
}

// TestSkillReloadSelection_NudgeListsLoadedSkills pins the structural contract
// of the pressure nudge: loaded skills are listed either way, the compact-tool
// remedy references the reload_skills machine parameter, and the no-tool remedy
// never requests a structured selection the model cannot submit.
func TestSkillReloadSelection_NudgeListsLoadedSkills(t *testing.T) {
	t.Parallel()
	loaded := []schema.SkillInventorySummary{
		{Name: "scope:probe", Description: "opaque-probe-description", Availability: "ordinary", HasOrdinary: true},
	}
	withTool := selfCompactNudge(true, loaded)
	if !strings.Contains(withTool, "scope:probe") {
		t.Fatal("compact-tool nudge must list the loaded skill")
	}
	if !strings.Contains(withTool, "reload_skills") {
		t.Fatal("compact-tool nudge must name the reload_skills selection parameter")
	}

	withoutTool := selfCompactNudge(false, loaded)
	if !strings.Contains(withoutTool, "scope:probe") {
		t.Fatal("no-tool nudge must still list the loaded skill for awareness")
	}
	if strings.Contains(withoutTool, "reload_skills") || strings.Contains(withoutTool, "skill-reload-selection") {
		t.Fatal("no-tool nudge must not request a structured selection the model cannot submit")
	}
}
