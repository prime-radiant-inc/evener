package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// findOne runs execFindSessionTranscripts and returns the envelope, failing the
// test on error.
func findOne(t *testing.T, deps *toolDeps, args map[string]any) findSessionsEnvelope {
	t.Helper()
	v, err := execFindSessionTranscripts(deps, args)
	if err != nil {
		t.Fatalf("find %v: %v", args, err)
	}
	env, ok := v.(findSessionsEnvelope)
	if !ok {
		t.Fatalf("find %v returned %T", args, v)
	}
	return env
}

// findRefSet returns the transcript_ref values of a find envelope as a set.
func findRefSet(t *testing.T, deps *toolDeps, args map[string]any) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, m := range findOne(t, deps, args).Matches {
		set[m.TranscriptRef] = true
	}
	return set
}

// writeFindTranscript writes a transcript holding exactly the given turns.
func writeFindTranscript(t *testing.T, bucketDir, id string, created time.Time, turns ...schema.Turn) {
	t.Helper()
	tpath := transcriptPath(bucketDir, id)
	if err := os.MkdirAll(filepath.Dir(tpath), 0o755); err != nil {
		t.Fatalf("writeFindTranscript mkdir: %v", err)
	}
	w, err := transcript.NewWriter(tpath, transcript.Header{SessionID: id, CreatedAt: created})
	if err != nil {
		t.Fatalf("writeFindTranscript %s: %v", id, err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			_ = w.Close()
			t.Fatalf("append %s: %v", id, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close %s: %v", id, err)
	}
}

// TestFind_MetadataFilters covers the typed metadata-only filters: kind,
// has_children, min_turns/max_turns, and updated_after. Each filter is applied
// before any transcript is opened, so it composes with the catalog and the
// content-search path alike.
func TestFind_MetadataFilters(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	rootID := identifier.MustNewSessionID()
	childID := identifier.MustNewSessionID()
	forkID := identifier.MustNewSessionID()
	loneID := identifier.MustNewSessionID()

	writeFindSession(t, dir, findMetaSpec{id: rootID, name: "root", updated: now.Add(-4 * time.Hour), turnCount: 30}, "root body")
	writeFindSession(t, dir, findMetaSpec{id: childID, name: "child", isSubagent: true, parentSessionID: rootID, updated: now.Add(-3 * time.Hour), turnCount: 5}, "child body")
	writeFindSession(t, dir, findMetaSpec{id: forkID, name: "fork", parentSessionID: rootID, divergenceTurn: 1, updated: now.Add(-2 * time.Hour), turnCount: 12}, "fork body")
	writeFindSession(t, dir, findMetaSpec{id: loneID, name: "lone", updated: now.Add(-1 * time.Hour), turnCount: 20}, "lone body")

	deps := &toolDeps{stateDir: dir}

	if got := findRefSet(t, deps, map[string]any{"kind": "root"}); len(got) != 2 || !got[refFor("", rootID)] || !got[refFor("", loneID)] {
		t.Fatalf("kind=root refs = %v, want root+lone", got)
	}
	if got := findRefSet(t, deps, map[string]any{"kind": "subagent"}); len(got) != 1 || !got[refFor("", childID)] {
		t.Fatalf("kind=subagent refs = %v, want only child", got)
	}
	if got := findRefSet(t, deps, map[string]any{"kind": "fork"}); len(got) != 1 || !got[refFor("", forkID)] {
		t.Fatalf("kind=fork refs = %v, want only fork", got)
	}
	if got := findRefSet(t, deps, map[string]any{"has_children": true}); len(got) != 1 || !got[refFor("", rootID)] {
		t.Fatalf("has_children=true refs = %v, want only root", got)
	}
	if got := findRefSet(t, deps, map[string]any{"has_children": false}); len(got) != 3 || got[refFor("", rootID)] {
		t.Fatalf("has_children=false refs = %v, want child/fork/lone", got)
	}
	if got := findRefSet(t, deps, map[string]any{"min_turns": float64(15)}); len(got) != 2 || !got[refFor("", rootID)] || !got[refFor("", loneID)] {
		t.Fatalf("min_turns=15 refs = %v, want root+lone", got)
	}
	if got := findRefSet(t, deps, map[string]any{"max_turns": float64(10)}); len(got) != 1 || !got[refFor("", childID)] {
		t.Fatalf("max_turns=10 refs = %v, want only child", got)
	}
	after := now.Add(-150 * time.Minute).Format(time.RFC3339)
	if got := findRefSet(t, deps, map[string]any{"updated_after": after}); len(got) != 2 || !got[refFor("", forkID)] || !got[refFor("", loneID)] {
		t.Fatalf("updated_after=%s refs = %v, want fork+lone", after, got)
	}
	before := now.Add(-150 * time.Minute).Format(time.RFC3339)
	if got := findRefSet(t, deps, map[string]any{"updated_before": before}); len(got) != 2 || !got[refFor("", rootID)] || !got[refFor("", childID)] {
		t.Fatalf("updated_before=%s refs = %v, want root+child", before, got)
	}

	// A content query must respect the same filters: kind=root drops the
	// subagent even though its body matches.
	if got := findRefSet(t, deps, map[string]any{"query": "body", "kind": "root"}); len(got) != 2 || !got[refFor("", rootID)] || !got[refFor("", loneID)] {
		t.Fatalf("query+kind=root refs = %v, want root+lone", got)
	}
}

// TestFind_TimeBoundsInclusive pins that updated_after/updated_before are
// inclusive at the boundary and that fractional-second (RFC3339Nano)
// timestamps parse instead of erroring.
func TestFind_TimeBoundsInclusive(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	at := time.Now().UTC().Truncate(time.Second)
	id := identifier.MustNewSessionID()
	writeFindSession(t, dir, findMetaSpec{id: id, name: "at", updated: at, turnCount: 3}, "body")
	deps := &toolDeps{stateDir: dir}
	stamp := at.Format(time.RFC3339)
	for _, args := range []map[string]any{
		{"updated_after": stamp, "updated_before": stamp},
		{"updated_after": stamp},
		{"updated_before": stamp},
	} {
		if got := findRefSet(t, deps, args); len(got) != 1 || !got[refFor("", id)] {
			t.Fatalf("bounds %v refs = %v, want the session updated exactly at the bound", args, got)
		}
	}
	// A fractional-second bound strictly after the session excludes it, proving
	// RFC3339Nano parsing accepts the fractional form rather than erroring.
	frac := at.Add(500 * time.Millisecond).Format(time.RFC3339Nano)
	if got := findRefSet(t, deps, map[string]any{"updated_after": frac}); len(got) != 0 {
		t.Fatalf("updated_after=%s refs = %v, want none (session is older)", frac, got)
	}
}

// TestFind_ChildrenOfComposesWithFilters pins that the metadata filters apply
// under children_of (which is a filter, not a separate mode) and that invalid
// filters are still rejected there.
func TestFind_ChildrenOfComposesWithFilters(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	rootID := identifier.MustNewSessionID()
	subID := identifier.MustNewSessionID()
	forkID := identifier.MustNewSessionID()
	writeFindSession(t, dir, findMetaSpec{id: rootID, name: "root", updated: now.Add(-3 * time.Hour), turnCount: 10}, "root body")
	writeFindSession(t, dir, findMetaSpec{id: subID, name: "sub", isSubagent: true, parentSessionID: rootID, updated: now.Add(-2 * time.Hour), turnCount: 4}, "sub body")
	writeFindSession(t, dir, findMetaSpec{id: forkID, name: "fork", parentSessionID: rootID, divergenceTurn: 1, updated: now.Add(-1 * time.Hour), turnCount: 40}, "fork body")
	deps := &toolDeps{stateDir: dir}
	rootRef := refFor("", rootID)

	if got := findRefSet(t, deps, map[string]any{"children_of": rootRef}); len(got) != 2 {
		t.Fatalf("children_of refs = %v, want sub+fork", got)
	}
	if got := findRefSet(t, deps, map[string]any{"children_of": rootRef, "kind": "subagent"}); len(got) != 1 || !got[refFor("", subID)] {
		t.Fatalf("children_of+kind=subagent refs = %v, want only sub", got)
	}
	if got := findRefSet(t, deps, map[string]any{"children_of": rootRef, "min_turns": float64(20)}); len(got) != 1 || !got[refFor("", forkID)] {
		t.Fatalf("children_of+min_turns refs = %v, want only fork", got)
	}
	if _, err := execFindSessionTranscripts(deps, map[string]any{"children_of": rootRef, "kind": "bogus"}); err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("children_of+bogus kind err = %v, want invalid_request", err)
	}
}

// TestFind_FilterValidation rejects invalid filter combinations and values
// rather than silently ignoring them.
func TestFind_FilterValidation(t *testing.T) {
	t.Parallel()
	deps := &toolDeps{stateDir: newBucket(t)}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"min_turns over max_turns", map[string]any{"min_turns": float64(5), "max_turns": float64(2)}},
		{"updated_after after updated_before", map[string]any{"updated_after": "2026-06-07T00:00:00Z", "updated_before": "2026-06-01T00:00:00Z"}},
		{"unknown kind", map[string]any{"kind": "bogus"}},
		{"unparsable timestamp", map[string]any{"updated_after": "not-a-time"}},
	}
	for _, c := range cases {
		if _, err := execFindSessionTranscripts(deps, c.args); err == nil || !strings.Contains(err.Error(), "invalid_request") {
			t.Fatalf("%s: err = %v, want invalid_request", c.name, err)
		}
	}
}

// TestFind_MetadataHitHasPromptSnippet pins the ranked-evidence fix for a
// metadata-only match: instead of a context-free record, a prompt/title match
// carries a bounded snippet so two sessions that share a long prompt are told
// apart.
func TestFind_MetadataHitHasPromptSnippet(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := identifier.MustNewSessionID()
	writeFindSession(t, dir, findMetaSpec{
		id:             id,
		name:           "mux session",
		originalPrompt: "summarize the mux provider comparison",
		updated:        now,
	}, "unrelated body")

	env := findOne(t, &toolDeps{stateDir: dir}, map[string]any{"query": "mux"})
	if len(env.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(env.Matches))
	}
	snips := env.Matches[0].Snippets
	if len(snips) != 1 {
		t.Fatalf("metadata hit snippets = %v, want one prompt snippet", snips)
	}
	if snips[0].Role != "user" {
		t.Fatalf("metadata snippet role = %q, want user", snips[0].Role)
	}
	if !strings.Contains(snips[0].Snippet, "mux") {
		t.Fatalf("metadata snippet = %q, want the prompt text", snips[0].Snippet)
	}
}

// TestFind_ContentSnippetsRankedByRole pins the per-session snippet ranking: a
// user match outranks an assistant match even when the assistant match sits at
// an earlier sequence, with sequence as the tie-break.
func TestFind_ContentSnippetsRankedByRole(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := identifier.MustNewSessionID()
	writeFindTranscript(t, dir, id, now,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("needle spoken by assistant")),
		schema.NewTurn(schema.TurnUserInput, llm.User("needle asked by user")),
	)
	saveFindMeta(t, dir, findMetaSpec{id: id, name: "ranked", updated: now})

	env := findOne(t, &toolDeps{stateDir: dir}, map[string]any{"query": "needle"})
	if len(env.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(env.Matches))
	}
	snips := env.Matches[0].Snippets
	if len(snips) != 2 {
		t.Fatalf("snippets = %v, want two matches", snips)
	}
	if snips[0].Role != "user" {
		t.Fatalf("first snippet role = %q, want user ranked ahead of assistant", snips[0].Role)
	}
	if snips[0].Seq <= snips[1].Seq {
		t.Fatalf("snippet order %v is still seq order, not role-ranked", snips)
	}
}
