package apptranscript

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// memoryContextRecord wraps sections the way agent/session_memory.go records
// a memory-context message: one system-notification block around the
// preamble and the sections. The agent-side producer fixture test
// (agent/memory_context_wire_fixture_test.go) pins that the two agree against
// the real Session.
func memoryContextRecord(sections ...string) string {
	return llm.SystemNotificationOpenTag + "\n" + MemoryContextBody(sections) + "\n" + llm.SystemNotificationCloseTag
}

func memoryContextTurn(text string) schema.Turn {
	return schema.Turn{Kind: schema.TurnMemoryContext, Message: llm.UserMachinery(text)}
}

func projectMemoryContext(t *testing.T, turn schema.Turn) appwire.ThreadItem {
	t.Helper()
	items := ProjectTurn("memory-turn", 3, turn, NewToolCallRegistry(), nil, nil)
	if len(items) != 1 {
		t.Fatalf("projected %d items, want 1: %+v", len(items), items)
	}
	return items[0]
}

func decodeMemoryContextRaw(t *testing.T, item appwire.ThreadItem) (scope, state string, truncated bool, content string) {
	t.Helper()
	var payload struct {
		MemoryContext struct {
			Scope     string `json:"scope"`
			State     string `json:"state"`
			Truncated bool   `json:"truncated"`
			Content   string `json:"content"`
		} `json:"memoryContext"` //nolint:tagliatelle // AppWire Raw fixture asserts the public camelCase wire contract.
	}
	if err := json.Unmarshal(item.Raw, &payload); err != nil {
		t.Fatalf("raw %s does not decode: %v", item.Raw, err)
	}
	m := payload.MemoryContext
	return m.Scope, m.State, m.Truncated, m.Content
}

// TestMemoryContextProjectionExtractsMetadata pins the frozen consumption
// contract for an index section: a systemMessage with eventKind
// memory-context, the item_memory_context_<index> id, the section's exact
// recorded text, and the {"memoryContext":{scope,state,truncated,content}} raw
// payload.
func TestMemoryContextProjectionExtractsMetadata(t *testing.T) {
	t.Parallel()
	quoted := "Line one\nLine \"two\" \u2014 caf\u00e9\tend"
	cases := []struct {
		name      string
		scope     string
		state     string
		truncated bool
		content   string
	}{
		{name: "current-personal", scope: "personal", state: "current", content: "- [a](a.md) — a note\n"},
		{name: "current-project", scope: "project", state: "current", content: "opaque-project-index\n"},
		{name: "empty-project", scope: "project", state: "current", content: ""},
		{name: "missing-project", scope: "project", state: "missing", content: ""},
		{name: "revoked-project", scope: "project", state: "revoked", content: ""},
		{name: "unavailable-project", scope: "project", state: "unavailable", content: ""},
		{name: "truncated-project", scope: "project", state: "current", truncated: true, content: strings.Repeat("x", 8192) + "..."},
		{name: "quoted-project", scope: "project", state: "current", content: quoted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			section := MemoryIndexSection(tc.scope, tc.state, tc.truncated, tc.content)
			item := projectMemoryContext(t, memoryContextTurn(memoryContextRecord(section)))
			if item.Type != "systemMessage" {
				t.Fatalf("type=%q, want systemMessage", item.Type)
			}
			if item.ID != "item_memory_context_3" {
				t.Fatalf("id=%q, want item_memory_context_3", item.ID)
			}
			if item.EventKind != appwire.ThreadItemEventKindMemoryContext {
				t.Fatalf("eventKind=%q, want %q", item.EventKind, appwire.ThreadItemEventKindMemoryContext)
			}
			if item.Text != section {
				t.Fatalf("text=%q, want the exact recorded section %q", item.Text, section)
			}
			if item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("status=%q", item.Status)
			}
			if len(item.Raw) == 0 {
				t.Fatal("raw is absent on a decodable record")
			}
			scope, state, truncated, content := decodeMemoryContextRaw(t, item)
			if scope != tc.scope || state != tc.state || truncated != tc.truncated || content != tc.content {
				t.Fatalf("raw=(%q,%q,%t,%q), want (%q,%q,%t,%q)", scope, state, truncated, content, tc.scope, tc.state, tc.truncated, tc.content)
			}
		})
	}
}

// One message carrying every scope's news projects one item per section, in
// message order: index sections with their raw, change and page sections with
// their recorded text and no raw.
func TestMemoryContextProjectionSplitsSections(t *testing.T) {
	t.Parallel()
	sections := []string{
		MemoryIndexSection("personal", "current", false, "- [a](a.md) — opaque-personal\n"),
		MemoryIndexChangesSection("project", []string{"- [new](new.md) — opaque-added"}, []string{"- [old](old.md) — opaque-removed"}),
		MemoryIndexChangeCountsSection("personal", 150, 3),
		MemoryPageChangesSection("project", []MemoryPageChange{{Path: "opaque-changed.md"}, {Path: "opaque-gone.md", Removed: true}}),
	}
	items := ProjectTurn("memory-turn", 3, memoryContextTurn(memoryContextRecord(sections...)), NewToolCallRegistry(), nil, nil)
	if len(items) != len(sections) {
		t.Fatalf("projected %d items, want one per section: %+v", len(items), items)
	}
	for i, item := range items {
		wantID := "item_memory_context_3"
		if i > 0 {
			wantID += "_" + strconv.Itoa(i)
		}
		if item.ID != wantID || item.Text != sections[i] || item.EventKind != appwire.ThreadItemEventKindMemoryContext {
			t.Fatalf("item %d=%+v, want id %s and text %q", i, item, wantID, sections[i])
		}
		if hasRaw := len(item.Raw) != 0; hasRaw != (i == 0) {
			t.Fatalf("item %d raw=%s; only the index section carries raw", i, item.Raw)
		}
	}
	parsed, ok := ParseMemoryContext(memoryContextRecord(sections...))
	if !ok {
		t.Fatal("combined message does not parse")
	}
	var kinds []string
	for _, section := range parsed {
		kinds = append(kinds, section.Scope+"/"+section.Kind)
	}
	if got := strings.Join(kinds, " "); got != "personal/index project/changes personal/changes project/pages" {
		t.Fatalf("sections=%s", got)
	}
}

// TestMemoryContextProjectionRejectsMalformed pins the fallback: an
// undecodable message keeps the compact identity and the complete original
// text, with no fabricated raw payload. Each malformed text is the producer's
// own shape, only corrupted.
func TestMemoryContextProjectionRejectsMalformed(t *testing.T) {
	t.Parallel()
	index := MemoryIndexSection("project", "current", false, "opaque-index\n")
	valid := memoryContextRecord(index)
	cases := []struct {
		name string
		text string
	}{
		{name: "no-notification", text: MemoryContextBody([]string{index})},
		{name: "no-sections", text: memoryContextRecord()},
		{name: "other-preamble", text: strings.Replace(valid, MemoryContextPreamble, "Memory notes.", 1)},
		{name: "truncated-literal", text: strings.Replace(valid, `\n"`, `\n`, 1)},
		{name: "unknown-scope", text: memoryContextRecord(strings.Replace(index, "Project", "Session", 1))},
		{name: "unknown-state", text: memoryContextRecord("Project memory: gone.")},
		{name: "trailing-garbage", text: memoryContextRecord(index + " trailing")},
		{name: "index-with-more-lines", text: memoryContextRecord(index + "\nmore")},
		{name: "bad-change-line", text: memoryContextRecord(MemoryIndexChangesSection("project", []string{"x"}, nil) + "\nchanged \"y\"")},
		{name: "empty-change-list", text: memoryContextRecord(MemoryIndexChangesSection("project", nil, nil))},
		{name: "bad-page-state", text: memoryContextRecord(strings.Replace(MemoryPageChangesSection("project", []MemoryPageChange{{Path: "a.md"}}), " changed", " edited", 1))},
		{name: "plain-text", text: "opaque-memory-display-78"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := projectMemoryContext(t, memoryContextTurn(tc.text))
			if item.EventKind != appwire.ThreadItemEventKindMemoryContext {
				t.Fatalf("eventKind=%q, want %q", item.EventKind, appwire.ThreadItemEventKindMemoryContext)
			}
			if item.Text != tc.text {
				t.Fatalf("text=%q, want the exact original %q", item.Text, tc.text)
			}
			if len(item.Raw) != 0 {
				t.Fatalf("raw=%s on an undecodable body; want absent", item.Raw)
			}
		})
	}
}

// TestMemoryContextProjectionPreservesBoundaryWhitespace pins that a
// whitespace-only recorded body is not trimmed away and is preserved verbatim,
// while an empty body projects nothing.
func TestMemoryContextProjectionPreservesBoundaryWhitespace(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"   ", "\n\t\n", "  \n  "} {
		item := projectMemoryContext(t, memoryContextTurn(text))
		if item.Text != text {
			t.Fatalf("text=%q, want %q", item.Text, text)
		}
		if len(item.Raw) != 0 {
			t.Fatalf("raw=%s on whitespace-only body; want absent", item.Raw)
		}
	}
	if items := ProjectTurn("memory-turn", 3, memoryContextTurn(""), NewToolCallRegistry(), nil, nil); len(items) != 0 {
		t.Fatalf("empty body projected %+v, want nothing", items)
	}
}

// TestMemoryContextProjectionLeavesOtherContextTrimmed pins that only the
// memory-context kind changes: environment and shared-notes still trim.
func TestMemoryContextProjectionLeavesOtherContextTrimmed(t *testing.T) {
	t.Parallel()
	for _, kind := range []schema.TurnKind{schema.TurnEnvironment, schema.TurnNotesContext} {
		turn := schema.Turn{Kind: kind, Message: llm.System("  padded  ")}
		items := ProjectTurn("turn", 1, turn, NewToolCallRegistry(), nil, nil)
		if len(items) != 1 || items[0].Text != "padded" {
			t.Fatalf("kind=%s items=%+v, want one trimmed item", kind, items)
		}
	}
}

// TestMemoryContextProjectionReloadParity writes a produced memory-context
// turn to a real transcript and projects it back from the file, proving the
// live projection and the reloaded history reach the same item.
func TestMemoryContextProjectionReloadParity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "memory-parity"})
	if err != nil {
		t.Fatal(err)
	}
	text := memoryContextRecord(MemoryIndexSection("project", "current", true, "opaque-parity\n"), MemoryPageChangesSection("project", []MemoryPageChange{{Path: "opaque-parity.md"}}))
	turn := memoryContextTurn(text)
	if err := w.Append(turn); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_, entries, err := transcript.OpenWriterForSession(path, "memory-parity")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("read %d entries, want 1", len(entries))
	}
	direct := ProjectTurn("memory-turn", 3, turn, NewToolCallRegistry(), nil, nil)
	reloaded := ProjectTurn("memory-turn", 3, entries[0].Turn, NewToolCallRegistry(), nil, nil)
	if len(direct) != 2 || len(reloaded) != len(direct) {
		t.Fatalf("direct %d items, reloaded %d, want 2 each", len(direct), len(reloaded))
	}
	for i := range direct {
		if reloaded[i].ID != direct[i].ID || reloaded[i].Text != direct[i].Text || reloaded[i].EventKind != direct[i].EventKind || string(reloaded[i].Raw) != string(direct[i].Raw) {
			t.Fatalf("item %d reloaded=%+v direct=%+v", i, reloaded[i], direct[i])
		}
	}
}
