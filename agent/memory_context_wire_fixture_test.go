package agent

// The memory-context wire corpus records what Session.appendMemoryContext
// actually writes, projected the way a reloaded client reaches it: the real
// producer authors every message, including the transitions (empty, missing,
// revoked) that only emit after an earlier observation of the same scope. The
// web and native memory-refresh tests read this file, so a hand-authored
// message here would pin shapes the daemon never sends. Each case is one
// projected item, which is one section of a recorded message. The boundaries
// golden beside it pins whole model-facing messages.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go), or directly:
//
//	go test ./agent -run 'MemoryContextWireFixtures$' -count=1 -update-wire

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// memoryContextWireFixturePath is the committed corpus the web reads.
const memoryContextWireFixturePath = "testdata/memorycontextwire/events.json"

type memoryContextWireCase struct {
	Case string             `json:"case"`
	Note string             `json:"note"`
	Item appwire.ThreadItem `json:"item"`

	// Test-only expectations, not marshaled into the corpus: they turn this
	// generator into a regression over the producer's real output.
	wantRaw     bool
	wantScope   string
	wantState   string
	wantTrunc   bool
	wantContent string
}

func TestMemoryContextWireFixtures(t *testing.T) {
	t.Parallel()

	s := newSession(t,
		withDir(t.TempDir()),
		withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: t.TempDir()}),
	)
	// capture appends one boundary's sections and returns its recorded turn
	// and projected items.
	var recorded []string
	capture := func(name string, sections ...memoryContextSection) (schema.Turn, []appwire.ThreadItem) {
		t.Helper()
		before := memoryContextCount(s)
		s.appendMemoryContext(sections)
		if got := memoryContextCount(s); got != before+1 {
			t.Fatalf("%s: appended %d memory contexts, want 1", name, got-before)
		}
		turn := latestMemoryContextTurn(t, s)
		turn.Timestamp = wireFixtureStart
		recorded = append(recorded, turn.Message.Text())
		return turn, apptranscript.ProjectTurn("turn_1", 1, turn, nil, nil, nil)
	}
	one := func(name string, items []appwire.ThreadItem) appwire.ThreadItem {
		t.Helper()
		if len(items) != 1 {
			t.Fatalf("%s: projected %d items, want 1", name, len(items))
		}
		return items[0]
	}
	project := func(p memoryProjection) memoryContextSection { return s.memoryProjectionSection(p) }

	// Session start: both scopes' full indexes arrive in one message.
	startTurn, startItems := capture("start",
		project(memoryProjection{Scope: "personal", Status: "current", Content: "- [a note](a-note.md) — opaque-personal-index\n"}),
		project(memoryProjection{Scope: "project", Status: "current", Content: "opaque-project-index-1\n"}))
	if len(startItems) != 2 {
		t.Fatalf("start: projected %d items, want 2", len(startItems))
	}

	// An empty current index differs from the seed above, so the transition
	// emits where a first, empty observation would be suppressed.
	_, emptyItems := capture("empty", project(memoryProjection{Scope: "project", Status: "current", Content: ""}))
	_, missingItems := capture("missing", project(memoryProjection{Scope: "project", Status: "missing"}))
	_, revokedItems := capture("revoked", project(memoryProjection{Scope: "project", Status: "revoked"}))
	_, unavailableItems := capture("unavailable", project(memoryProjection{Scope: "project", Status: "unavailable"}))

	var overflow []memoryPage
	for i := range 200 {
		tags := []string{"alpha"}
		if i%4 == 0 {
			tags = nil
		}
		overflow = append(overflow, memoryPage{Path: fmt.Sprintf("opaque-%03d.md", i), Title: fmt.Sprintf("opaque %03d", i),
			Description: "opaque-description " + strings.Repeat("x", 64), HasDescription: true, Tags: tags, Updated: "2026-10-01"})
	}
	partial, full, truncated := projectMemoryIndex(overflow, memoryProjectionCap)
	_, truncatedItems := capture("truncated", project(memoryProjection{Scope: "project", Status: "current", Truncated: truncated, Content: partial, Index: full}))

	quotedTurn, quotedItems := capture("quoted", project(memoryProjection{Scope: "project", Status: "current", Content: "Line one\nLine \"two\" \u2014 caf\u00e9\tend"}))

	// Malformed: the producer's own valid bytes, corrupted so extraction must
	// fail. The recorded text is preserved and no raw is fabricated.
	malformedText := strings.Replace(quotedTurn.Message.Text(), apptranscript.MemoryContextPreamble, apptranscript.MemoryContextPreamble+"?", 1)
	malformedItem := one("malformed", apptranscript.ProjectTurn("turn_1", 1, schema.Turn{Kind: schema.TurnMemoryContext, Message: llm.UserMachinery(malformedText)}, nil, nil, nil))

	// Another session changed both known indexes and one project page this
	// session read, and removed another: one message carries both scopes'
	// changed lines and the page notice, which names the pages and carries no
	// contents.
	s.memoryMu.Lock()
	s.memoryReadPages = map[string]map[string]memoryPageRecord{"project": {
		"opaque-notice-changed.md": {sum: sha256.Sum256([]byte("opaque-notice-before"))},
		"opaque-notice-removed.md": {sum: sha256.Sum256([]byte("opaque-notice-gone"))},
	}}
	s.memoryMu.Unlock()
	personalChanged := "- [a note](a-note.md) — opaque-personal-index\n- [added](added.md) — opaque-personal-added\n"
	projectBaseline := memoryIndexBaseline{status: "current", index: "- opaque-change-kept\n- opaque-change-removed\n"}
	projectChanged := "- opaque-change-kept\n- opaque-change-added\n"
	changedTurn, changedItems := capture("changed",
		s.knownMemoryIndexSection(memoryIndexBaseline{status: "current", index: "- [a note](a-note.md) — opaque-personal-index\n"},
			memoryProjection{Scope: "personal", Status: "current", Content: personalChanged, Index: personalChanged}),
		s.knownMemoryIndexSection(projectBaseline, memoryProjection{Scope: "project", Status: "current", Content: projectChanged, Index: projectChanged}),
		s.memoryPageChangesSection("project", map[string]memoryPageRecord{
			"opaque-notice-changed.md": {sum: sha256.Sum256([]byte("opaque-notice-after"))},
			"opaque-notice-removed.md": {absent: true},
		}))
	if len(changedItems) != 3 {
		t.Fatalf("changed: projected %d items, want 3", len(changedItems))
	}

	cases := []memoryContextWireCase{
		{Case: "current-personal", Note: "A current personal index, the first section of a session-start message.", Item: startItems[0],
			wantRaw: true, wantScope: "personal", wantState: "current", wantContent: "- [a note](a-note.md) — opaque-personal-index\n"},
		{Case: "current-project", Note: "A current project index, the second section of the same session-start message.", Item: startItems[1],
			wantRaw: true, wantScope: "project", wantState: "current", wantContent: "opaque-project-index-1\n"},
		{Case: "empty-project", Note: "A current index that is empty; content is the empty string.", Item: one("empty", emptyItems),
			wantRaw: true, wantScope: "project", wantState: "current", wantContent: ""},
		{Case: "missing-project", Note: "The project index does not exist yet.", Item: one("missing", missingItems),
			wantRaw: true, wantScope: "project", wantState: "missing"},
		{Case: "revoked-project", Note: "The project memory scope is unbound or revoked for this session.", Item: one("revoked", revokedItems),
			wantRaw: true, wantScope: "project", wantState: "revoked"},
		{Case: "unavailable-project", Note: "The project index could not be read.", Item: one("unavailable", unavailableItems),
			wantRaw: true, wantScope: "project", wantState: "unavailable"},
		{Case: "truncated-project", Note: "A current index whose pages do not all fit the 8192-byte projection: truncated true, the last line counts the pages not shown.", Item: one("truncated", truncatedItems),
			wantRaw: true, wantScope: "project", wantState: "current", wantTrunc: true, wantContent: partial},
		{Case: "quoted-project", Note: "A current index with newlines, quotes, an em dash, a tab and non-ASCII bytes.", Item: one("quoted", quotedItems),
			wantRaw: true, wantScope: "project", wantState: "current", wantContent: "Line one\nLine \"two\" \u2014 caf\u00e9\tend"},
		{Case: "malformed-project", Note: "The producer's own message with its preamble corrupted: one item keeps the exact text and carries no raw.", Item: malformedItem},
		{Case: "index-change-personal", Note: "Another session changed the known personal index: the first section of a message that also carries the project's changes.", Item: changedItems[0]},
		{Case: "index-change-project", Note: "Another session changed the known project index: the section lists only the added and removed lines and carries no raw.", Item: changedItems[1]},
		{Case: "page-notice-project", Note: "Another session changed one project page this session read and removed another: the section names both pages, carries no page contents and no raw.", Item: changedItems[2]},
	}

	// Regression assertions over the producer's real output, before pinning.
	for _, tc := range cases {
		if tc.Item.Type != "systemMessage" || !strings.HasPrefix(tc.Item.ID, "item_memory_context_1") {
			t.Fatalf("%s: identity item=%+v", tc.Case, tc.Item)
		}
		if tc.Item.EventKind != appwire.ThreadItemEventKindMemoryContext {
			t.Fatalf("%s: eventKind=%q", tc.Case, tc.Item.EventKind)
		}
		if tc.Case != "malformed-project" && !slices.ContainsFunc(recorded, func(text string) bool { return strings.Contains(text, "\n\n"+tc.Item.Text+"\n") }) {
			t.Fatalf("%s: text=%q is not a recorded section", tc.Case, tc.Item.Text)
		}
		if !tc.wantRaw {
			if len(tc.Item.Raw) != 0 {
				t.Fatalf("%s: raw=%s on a section without index data", tc.Case, tc.Item.Raw)
			}
			continue
		}
		scope, state, truncated, content := decodeWireMemoryContext(t, tc.Item)
		if scope != tc.wantScope || state != tc.wantState || truncated != tc.wantTrunc || content != tc.wantContent {
			t.Fatalf("%s: raw=(%q,%q,%t,%q), want (%q,%q,%t,%q)", tc.Case, scope, state, truncated, content, tc.wantScope, tc.wantState, tc.wantTrunc, tc.wantContent)
		}
	}
	if malformedItem.Text != malformedText {
		t.Fatalf("malformed: text=%q, want the recorded text", malformedItem.Text)
	}

	checkWireFixture(t, memoryContextWireFixturePath, cases, "the web and native memory-refresh tests that read it")
	var boundaries strings.Builder
	boundaries.WriteString("# Memory-context messages as the model receives them\n")
	for _, b := range []struct {
		name string
		turn schema.Turn
	}{{"Session start, both scopes", startTurn}, {"Both scopes changed by another session, and a read page", changedTurn}} {
		fmt.Fprintf(&boundaries, "\n## %s\n\n%s\n", b.name, b.turn.Message.Text())
	}
	checkGolden(t, memoryContextBoundariesPath, []byte(boundaries.String()), *updateWireFixtures,
		"Regenerate with `go test ./agent -run 'MemoryContextWireFixtures$' -count=1 -update-wire` and read the diff.")
}

// memoryContextBoundariesPath pins whole memory-context messages.
const memoryContextBoundariesPath = "testdata/memorycontextwire/boundaries.md"

// latestMemoryContextTurn returns the session's latest recorded
// MEMORY_CONTEXT turn, read from live history, which for a memory-context
// message is byte-identical to the durable entry recordTurn wrote.
func latestMemoryContextTurn(t *testing.T, s *Session) schema.Turn {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range slices.Backward(s.history) {
		if turn.Kind == schema.TurnMemoryContext {
			return turn
		}
	}
	t.Fatal("no memory context turn in history")
	return schema.Turn{}
}

func decodeWireMemoryContext(t *testing.T, item appwire.ThreadItem) (scope, state string, truncated bool, content string) {
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
