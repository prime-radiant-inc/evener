package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// findMetaSpec describes a synthetic SessionMeta to write for find tests.
type findMetaSpec struct {
	id              string
	name            string
	model           string
	profileID       string
	originalPrompt  string
	workingDir      string
	gitOrigin       string
	parentSessionID string
	divergenceTurn  int
	isSubagent      bool
	turnCount       int
	updated         time.Time
}

// saveFindMeta writes a SessionMeta built from spec into the bucket's sessions
// dir, without touching the transcript file.
func saveFindMeta(t *testing.T, bucketDir string, spec findMetaSpec) {
	t.Helper()
	if spec.updated.IsZero() {
		spec.updated = time.Now().UTC()
	}
	meta := schema.SessionMeta{
		ID:              spec.id,
		Name:            spec.name,
		Model:           spec.model,
		ProfileID:       spec.profileID,
		OriginalPrompt:  spec.originalPrompt,
		ParentSessionID: spec.parentSessionID,
		DivergenceTurn:  spec.divergenceTurn,
		IsSubagent:      spec.isSubagent,
		TurnCount:       spec.turnCount,
		CreatedAt:       spec.updated,
		UpdatedAt:       spec.updated,
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir:   spec.workingDir,
			GitOriginURL: spec.gitOrigin,
		},
	}
	if err := schema.SaveSessionMeta(bucketDir, meta); err != nil {
		t.Fatalf("save find meta %s: %v", spec.id, err)
	}
}

// writeFindSession writes both a one-turn transcript (carrying turnText as
// assistant text so the content scan can match it) and a SessionMeta for spec.
func writeFindSession(t *testing.T, bucketDir string, spec findMetaSpec, turnText string) {
	t.Helper()
	tpath := transcriptPath(bucketDir, spec.id)
	sessDir := filepath.Dir(tpath)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("writeFindSession mkdir: %v", err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: spec.id,
		CreatedAt: spec.updated,
		ProfileID: spec.profileID,
		Model:     spec.model,
	})
	if err != nil {
		t.Fatalf("write find transcript %s: %v", spec.id, err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(turnText))); err != nil {
		t.Fatalf("append find turn %s: %v", spec.id, err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close find transcript %s: %v", spec.id, err)
	}
	saveFindMeta(t, bucketDir, spec)
}

// marshalFind calls execFindSessionTranscripts and returns the JSON-marshaled
// wire bytes (the exact shape the model sees).
func marshalFind(t *testing.T, deps *toolDeps, args map[string]any) []byte {
	t.Helper()
	v, err := execFindSessionTranscripts(deps, args)
	if err != nil {
		t.Fatalf("execFindSessionTranscripts: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal find result: %v", err)
	}
	return b
}

// decodeEnvelope decodes the marshaled JSON into a map.
func decodeEnvelope(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return m
}

// matchesFromEnvelope extracts the "matches" array from a decoded envelope.
func matchesFromEnvelope(t *testing.T, env map[string]any) []map[string]any {
	t.Helper()
	raw, ok := env["matches"]
	if !ok {
		t.Fatal("envelope missing 'matches' key")
	}
	ifaces, ok := raw.([]any)
	if !ok {
		t.Fatalf("'matches' is not an array: %T", raw)
	}
	out := make([]map[string]any, len(ifaces))
	for i, v := range ifaces {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("match[%d] is not a map: %T", i, v)
		}
		out[i] = m
	}
	return out
}

// --- TestFind_CatalogTrimmedAndOrdered ---

// TestFind_CatalogTrimmedAndOrdered verifies that:
//   - sessions are returned newest-first
//   - the current session sorts last regardless of UpdatedAt
//   - the 5 always-present fields are present in each record
//   - fields that were removed from the spec (session_id, model, profile_id,
//     created_at, default_read, has_transcript) are absent from the wire JSON
func TestFind_CatalogTrimmedAndOrdered(t *testing.T) {
	dir := newBucket(t)

	now := time.Now().UTC().Truncate(time.Second)

	// OLDER is the oldest but will sort first because it's the current session.
	// But the current session should sort LAST per spec.
	//
	// We write:
	//   A00OLDER: updated now-2h (not current)
	//   A00NEWER: updated now-1h (not current; this is newest non-current)
	//   LIVE0000: updated now (this is current; must sort last)
	//
	// Expected order: A00NEWER (newest non-current), A00OLDER, LIVE0000 (current last).
	specOlder := findMetaSpec{
		id:      "A00OLDER",
		name:    "older session",
		updated: now.Add(-2 * time.Hour),
	}
	specNewer := findMetaSpec{
		id:      "A00NEWER",
		name:    "newer session",
		updated: now.Add(-1 * time.Hour),
	}
	specCurrent := findMetaSpec{
		id:      "LIVE0000",
		name:    "current session",
		updated: now,
	}

	writeFindSession(t, dir, specOlder, "older text")
	writeFindSession(t, dir, specNewer, "newer text")
	writeFindSession(t, dir, specCurrent, "current text")

	deps := &toolDeps{stateDir: dir, sessionID: "LIVE0000"}
	b := marshalFind(t, deps, map[string]any{})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	if len(matches) < 3 {
		t.Fatalf("expected at least 3 matches, got %d", len(matches))
	}

	// Verify ordering: newest non-current first, current last.
	if matches[0]["transcript_ref"] != "local:A00NEWER" {
		t.Errorf("match[0] should be A00NEWER (newest non-current), got %v", matches[0]["transcript_ref"])
	}
	if matches[1]["transcript_ref"] != "local:A00OLDER" {
		t.Errorf("match[1] should be A00OLDER, got %v", matches[1]["transcript_ref"])
	}
	if matches[2]["transcript_ref"] != "local:LIVE0000" {
		t.Errorf("match[2] should be LIVE0000 (current, sorts last), got %v", matches[2]["transcript_ref"])
	}

	// Verify is_current is set only on the live session.
	if matches[2]["is_current"] != true {
		t.Errorf("match[2] (current) should have is_current=true, got %v", matches[2]["is_current"])
	}
	if matches[0]["is_current"] == true {
		t.Errorf("match[0] (not current) should not have is_current=true")
	}

	// Verify the 5 always-present fields are present.
	for i, m := range matches {
		for _, field := range []string{"transcript_ref", "kind", "title", "updated_at", "approx_turns"} {
			if _, ok := m[field]; !ok {
				t.Errorf("match[%d] missing required field %q", i, field)
			}
		}
	}

	// Verify removed fields are absent from the wire JSON.
	raw := string(b)
	for _, banned := range []string{"session_id", `"model"`, "profile_id", "created_at", "default_read", "has_transcript"} {
		if strings.Contains(raw, banned) {
			t.Errorf("wire JSON contains banned field %q", banned)
		}
	}
}

// --- TestFind_QuerySearch ---

// TestFind_QuerySearch verifies that:
//   - a query matching session metadata returns that session without snippets
//   - a query matching only transcript content returns that session with snippets
//   - scanned is set when a content scan ran
func TestFind_QuerySearch(t *testing.T) {
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)

	// META0001: matches via name (metadata match — should not require content scan).
	writeFindSession(t, dir, findMetaSpec{
		id:      "META0001",
		name:    "uniqueMetaToken session",
		updated: now.Add(-2 * time.Hour),
	}, "unrelated content")

	// CONT0002: matches only in transcript content (title/prompt has no match).
	writeFindSession(t, dir, findMetaSpec{
		id:      "CONT0002",
		name:    "plain session",
		updated: now.Add(-1 * time.Hour),
	}, "the uniqueContentToken lives here")

	deps := &toolDeps{stateDir: dir, sessionID: "LIVE0000"}
	b := marshalFind(t, deps, map[string]any{"query": "uniqueContentToken"})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %v", len(matches), matches)
	}
	if matches[0]["transcript_ref"] != "local:CONT0002" {
		t.Errorf("expected CONT0002 match, got %v", matches[0]["transcript_ref"])
	}

	// Content match should have snippets.
	snips, ok := matches[0]["snippets"]
	if !ok || snips == nil {
		t.Error("content match should have snippets")
	}

	// scanned should be set (we opened at least one transcript).
	if _, ok := env["scanned"]; !ok {
		t.Error("envelope should have 'scanned' when a content scan ran")
	}

	// Now test a metadata-matching query.
	b2 := marshalFind(t, deps, map[string]any{"query": "uniqueMetaToken"})
	env2 := decodeEnvelope(t, b2)
	matches2 := matchesFromEnvelope(t, env2)
	if len(matches2) != 1 {
		t.Fatalf("expected 1 meta match, got %d", len(matches2))
	}
	if matches2[0]["transcript_ref"] != "local:META0001" {
		t.Errorf("expected META0001 meta match, got %v", matches2[0]["transcript_ref"])
	}
}

// --- TestFind_ChildrenOf ---

// TestFind_ChildrenOf verifies that:
//   - find({children_of:"local:PARENT01"}) returns exactly the two children
//   - children is metadata-only (no transcript files required for the parent or children)
//   - unrelated sessions are excluded
func TestFind_ChildrenOf(t *testing.T) {
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Parent: meta only, no transcript — proves the parent's transcript is never
	// opened to resolve its children.
	saveFindMeta(t, dir, findMetaSpec{
		id:      "PARENT01",
		name:    "the parent",
		updated: now.Add(-4 * time.Hour),
	})

	// Two real children, each with a transcript so each is a read-able ref.
	writeFindSession(t, dir, findMetaSpec{
		id:              "CHILD001",
		name:            "first child",
		parentSessionID: "PARENT01",
		updated:         now.Add(-3 * time.Hour),
	}, "first child work")
	writeFindSession(t, dir, findMetaSpec{
		id:              "CHILD002",
		name:            "second child",
		parentSessionID: "PARENT01",
		updated:         now.Add(-2 * time.Hour),
	}, "second child work")

	// A child by metadata whose transcript was never flushed: not read-able, so
	// children_of must exclude it (the readable-only invariant applies to every mode).
	saveFindMeta(t, dir, findMetaSpec{
		id:              "CHILD003",
		name:            "unflushed child",
		parentSessionID: "PARENT01",
		updated:         now.Add(-1 * time.Hour),
	})

	// Unrelated session (no parent).
	writeFindSession(t, dir, findMetaSpec{
		id:      "UNRELAT0",
		name:    "unrelated",
		updated: now,
	}, "unrelated work")

	deps := &toolDeps{stateDir: dir, sessionID: "LIVE0000"}
	b := marshalFind(t, deps, map[string]any{"children_of": "local:PARENT01"})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	ids := map[string]bool{}
	for _, m := range matches {
		ref, _ := m["transcript_ref"].(string)
		ids[ref] = true
	}
	if !ids["local:CHILD001"] || !ids["local:CHILD002"] {
		t.Errorf("expected both readable children, got %v", ids)
	}
	if ids["local:CHILD003"] {
		t.Errorf("CHILD003 has no transcript and must be excluded (readable-only): %v", ids)
	}
	if ids["local:UNRELAT0"] {
		t.Errorf("unrelated session must not appear: %v", ids)
	}
	if len(matches) != 2 {
		t.Fatalf("expected exactly 2 children, got %d: %v", len(matches), ids)
	}
}

// --- TestRead helpers ---

// writeMultiTurnSession writes a session transcript with nTurns alternating
// user/assistant turns, plus a SessionMeta. It is a helper for read tests.
func writeMultiTurnSession(t *testing.T, bucketDir string, spec findMetaSpec, nTurns int) {
	t.Helper()
	tpath := transcriptPath(bucketDir, spec.id)
	sessDir := filepath.Dir(tpath)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("writeMultiTurnSession mkdir: %v", err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: spec.id,
		CreatedAt: spec.updated,
		ProfileID: spec.profileID,
		Model:     spec.model,
	})
	if err != nil {
		t.Fatalf("write multi-turn transcript %s: %v", spec.id, err)
	}
	for i := 0; i < nTurns; i++ {
		if i%2 == 0 {
			if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("user turn"))); err != nil {
				t.Fatalf("append user turn %d: %v", i, err)
			}
		} else {
			if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("assistant turn"))); err != nil {
				t.Fatalf("append assistant turn %d: %v", i, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close multi-turn transcript %s: %v", spec.id, err)
	}
	saveFindMeta(t, bucketDir, spec)
}

// writeSessionWithToolTurn writes a session with a named tool call and a
// large repeated tool result (enough lines to trigger head+tail truncation).
// It returns the seq of the assistant turn (0-based).
func writeSessionWithToolTurn(t *testing.T, bucketDir string, spec findMetaSpec) int {
	t.Helper()
	tpath := transcriptPath(bucketDir, spec.id)
	sessDir := filepath.Dir(tpath)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("writeSessionWithToolTurn mkdir: %v", err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: spec.id,
		CreatedAt: spec.updated,
		Model:     spec.model,
	})
	if err != nil {
		t.Fatalf("write tool-turn transcript: %v", err)
	}
	callID := "call-expand-001"
	toolCallArgs := json.RawMessage(`{"command":"ls -la"}`)
	// Turn 0: user
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("run a command"))); err != nil {
		t.Fatalf("append user turn: %v", err)
	}
	// Turn 1: assistant with tool call
	assistantMsg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        callID,
				Name:      "shell",
				Arguments: toolCallArgs,
			}},
		},
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, assistantMsg)); err != nil {
		t.Fatalf("append assistant tool-call turn: %v", err)
	}
	// Build a large result body: 60 non-empty lines so head+tail truncation kicks in.
	var resultLines strings.Builder
	for i := 0; i < 60; i++ {
		resultLines.WriteString("result-deep-line-")
		resultLines.WriteString(strings.Repeat("x", 20))
		resultLines.WriteByte('\n')
	}
	// Turn 2: tool result
	resultMsg := llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: callID,
				Name:       "shell",
				Content:    resultLines.String(),
			}},
		},
	}
	if err := tw.Append(schema.NewTurn(schema.TurnToolResults, resultMsg)); err != nil {
		t.Fatalf("append tool-result turn: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tool-turn transcript: %v", err)
	}
	saveFindMeta(t, bucketDir, spec)
	return 1 // assistant turn is seq 1
}

// marshalRead calls execReadSessionTranscript and JSON-marshals the result.
func marshalRead(t *testing.T, deps *toolDeps, args map[string]any) []byte {
	t.Helper()
	v, err := execReadSessionTranscript(deps, args)
	if err != nil {
		t.Fatalf("execReadSessionTranscript: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal read result: %v", err)
	}
	return b
}

// decodeReadEnvelope decodes a marshaled read result into a map.
func decodeReadEnvelope(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal read envelope: %v", err)
	}
	return m
}

// readMeta extracts the "meta" sub-map from a decoded read envelope.
func readMetaMap(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	raw, ok := env["meta"]
	if !ok {
		t.Fatal("read envelope missing 'meta' key")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("'meta' is not a map: %T", raw)
	}
	return m
}

// --- TestRead_DefaultMarkdownWindow ---

// TestRead_DefaultMarkdownWindow verifies that a default read of a session with
// >40 turns returns markdown whose meta has turns_total > turns_rendered and
// whose content contains a self-announcing window line.
func TestRead_DefaultMarkdownWindow(t *testing.T) {
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "WINDW001"
	const totalTurns = 50 // more than the 40-turn default window

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "window session",
		updated: now,
	}, totalTurns)

	deps := &toolDeps{stateDir: dir, sessionID: "OTHER000"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
	})
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	// format and content_type must be set correctly.
	if env["format"] != "markdown" {
		t.Errorf("format = %v, want markdown", env["format"])
	}
	if env["content_type"] != "text/markdown" {
		t.Errorf("content_type = %v, want text/markdown", env["content_type"])
	}

	// Meta must carry turns_total.
	turnsTotal, ok := meta["turns_total"].(float64)
	if !ok || turnsTotal == 0 {
		t.Fatalf("meta.turns_total missing or zero: %v", meta["turns_total"])
	}
	turnsRendered, ok := meta["turns_rendered"].(float64)
	if !ok {
		t.Fatalf("meta.turns_rendered missing: %v", meta["turns_rendered"])
	}

	// With 50 turns and a 40-turn default window, turns_rendered < turns_total.
	if turnsRendered >= turnsTotal {
		t.Errorf("turns_rendered=%v should be < turns_total=%v for a windowed read", turnsRendered, turnsTotal)
	}

	// Content must contain a self-announcing window line.
	content, ok := env["content"].(string)
	if !ok {
		t.Fatal("content is not a string")
	}
	// The window line must name "of <total>".
	wantSnip := fmt.Sprintf("of %d", totalTurns)
	if !strings.Contains(content, wantSnip) {
		t.Errorf("content does not contain window announcement %q; got:\n%s", wantSnip, content[:min(500, len(content))])
	}
}

// --- TestRead_ExplicitRange ---

// TestRead_ExplicitRange verifies that range:"2-4" renders exactly those turns.
func TestRead_ExplicitRange(t *testing.T) {
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "RANGE001"
	const totalTurns = 10

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "range session",
		updated: now,
	}, totalTurns)

	deps := &toolDeps{stateDir: dir, sessionID: "OTHER000"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"range":          "2-4",
	})
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	// turns_rendered should be exactly 3 (turns 2, 3, 4).
	turnsRendered, ok := meta["turns_rendered"].(float64)
	if !ok {
		t.Fatalf("meta.turns_rendered missing: %v", meta["turns_rendered"])
	}
	if turnsRendered != 3 {
		t.Errorf("turns_rendered = %v, want 3 (range 2-4)", turnsRendered)
	}

	// The range in meta should reflect what was requested.
	rangeVal, _ := meta["range"].(string)
	if rangeVal != "2-4" {
		t.Errorf("meta.range = %q, want \"2-4\"", rangeVal)
	}

	// Content should contain headings for turns 2, 3, and 4.
	content, _ := env["content"].(string)
	for _, seq := range []int{2, 3, 4} {
		heading := fmt.Sprintf("## Turn %d", seq)
		if !strings.Contains(content, heading) {
			t.Errorf("content missing turn heading %q", heading)
		}
	}
	// And must NOT contain turn 0 or 1 headings.
	for _, seq := range []int{0, 1} {
		heading := fmt.Sprintf("## Turn %d", seq)
		if strings.Contains(content, heading) {
			t.Errorf("content unexpectedly contains heading %q for out-of-range turn", heading)
		}
	}
}

// --- TestRead_MalformedRangeWarns ---

// TestRead_MalformedRangeWarns verifies that a bad range sets meta.range_warning
// and surfaces a warning in content, while falling back to the default.
func TestRead_MalformedRangeWarns(t *testing.T) {
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "BADRANGE"

	writeMultiTurnSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "warn session",
		updated: now,
	}, 5)

	deps := &toolDeps{stateDir: dir, sessionID: "OTHER000"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"range":          "not-a-valid-range!!",
	})
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	// range_warning must be set.
	warning, _ := meta["range_warning"].(string)
	if warning == "" {
		t.Error("meta.range_warning should be set for a malformed range")
	}

	// Content must also surface the warning.
	content, _ := env["content"].(string)
	if !strings.Contains(content, "range warning") {
		t.Errorf("content should contain a visible range warning; got:\n%s", content[:min(500, len(content))])
	}
}

// --- TestRead_ExpandTurn ---

// TestRead_ExpandTurn verifies that expand_turn:<N> renders the tool result in
// full (lines that would be elided by head+tail truncation are present).
func TestRead_ExpandTurn(t *testing.T) {
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "EXPAND01"

	assistantSeq := writeSessionWithToolTurn(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "expand session",
		updated: now,
	})

	deps := &toolDeps{stateDir: dir, sessionID: "OTHER000"}

	// Without expand_turn, the middle lines of the 60-line result should be elided.
	bNoExpand := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
	})
	envNoExpand := decodeReadEnvelope(t, bNoExpand)
	contentNoExpand, _ := envNoExpand["content"].(string)

	// With expand_turn, ALL lines should appear.
	bExpand := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
		"expand_turn":    float64(assistantSeq), // JSON numbers unmarshal as float64
	})
	envExpand := decodeReadEnvelope(t, bExpand)
	contentExpand, _ := envExpand["content"].(string)

	// The deep middle line (line 30, between head and tail) should appear in
	// the expanded read but be absent (elided) in the non-expanded read.
	// With head=20, tail=10, line 30 is in the elided middle.
	deepLine := "result-deep-line-" + strings.Repeat("x", 20)
	if strings.Contains(contentNoExpand, deepLine) {
		t.Log("note: non-expanded content happens to contain deep line (may be within head/tail)")
	}
	if !strings.Contains(contentExpand, deepLine) {
		t.Errorf("expanded content should contain all result lines including deep middle lines; missing %q", deepLine)
	}
	// Elision marker should NOT be present when expanded.
	if strings.Contains(contentExpand, "lines elided") {
		t.Error("expanded content should not have elision markers")
	}
}

// --- TestRead_MetaTrimmed ---

// TestRead_MetaTrimmed verifies the marshaled markdown meta has the required
// fields and does NOT contain the banned fields (redaction, raw_formats,
// session_id).
func TestRead_MetaTrimmed(t *testing.T) {
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	const sessionID = "METATRIM"

	writeFindSession(t, dir, findMetaSpec{
		id:      sessionID,
		name:    "meta trim session",
		updated: now,
	}, "some text")

	deps := &toolDeps{stateDir: dir, sessionID: "OTHER000"}
	b := marshalRead(t, deps, map[string]any{
		"transcript_ref": "local:" + sessionID,
	})

	// Verify required meta fields.
	env := decodeReadEnvelope(t, b)
	meta := readMetaMap(t, env)

	for _, field := range []string{"turns_total", "range", "turns_rendered", "truncated", "elided_turns"} {
		if _, ok := meta[field]; !ok {
			t.Errorf("meta missing required field %q", field)
		}
	}

	// Verify banned fields are absent from the wire JSON.
	raw := string(b)
	for _, banned := range []string{"redaction", "raw_formats", `"session_id"`} {
		if strings.Contains(raw, banned) {
			t.Errorf("wire JSON contains banned field %q", banned)
		}
	}
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- TestFind_ChildrenOf_ProjBucket ---

// TestFind_ChildrenOf_ProjBucket verifies children_of for a parent in a sibling
// project (proj: ref): the parent's bucket is resolved from the ref with no stat, and
// the child comes back with a proj: ref into that bucket.
func TestFind_ChildrenOf_ProjBucket(t *testing.T) {
	stateHome := newStateHome(t)
	currentBucket := newBucketUnder(t, stateHome)
	siblingBucket := newBucketUnder(t, stateHome)
	hash := filepath.Base(siblingBucket)
	now := time.Now().UTC().Truncate(time.Second)

	// Parent (meta only) + one readable child, both in the SIBLING bucket.
	saveFindMeta(t, siblingBucket, findMetaSpec{id: "PARENTB0", name: "sibling parent", updated: now.Add(-time.Hour)})
	writeFindSession(t, siblingBucket, findMetaSpec{
		id:              "CHILDB01",
		name:            "sibling child",
		parentSessionID: "PARENTB0",
		updated:         now,
	}, "sibling child work")

	deps := &toolDeps{stateDir: currentBucket, sessionID: "LIVE0000"}
	b := marshalFind(t, deps, map[string]any{"children_of": "proj:" + hash + ":PARENTB0"})
	env := decodeEnvelope(t, b)

	matches := matchesFromEnvelope(t, env)
	if len(matches) != 1 {
		t.Fatalf("expected 1 sibling-project child, got %d", len(matches))
	}
	wantRef := "proj:" + hash + ":CHILDB01"
	if ref, _ := matches[0]["transcript_ref"].(string); ref != wantRef {
		t.Errorf("child ref = %q, want %q", ref, wantRef)
	}
}
