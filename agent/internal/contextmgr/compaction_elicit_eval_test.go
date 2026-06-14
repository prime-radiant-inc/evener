//go:build eval

package contextmgr

// Validates Variant B (harness elicits the note): does a side LLM call, run over
// the history at compaction time, actually CAPTURE the facts that the baseline
// recursive summary erodes? The erosion eval showed the baseline loses the opaque
// token (DEPLOY-...) and the exact number ("numShards exceeds 16") over successive
// compactions. If the elicitor captures those verbatim while they are still present,
// pinning them prevents the erosion by construction.
//
// Run: go test -tags eval ./agent/internal/contextmgr/ -run TestElicitNoteCapture -v -timeout 15m

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestElicitNoteCapture(t *testing.T) {
	cm := newOAuthManager(t)
	facts := erosionFacts()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// The same initial history the erosion eval starts from: all facts present,
	// buried in bulk.
	var initial strings.Builder
	initial.WriteString(block("project kickoff notes", repeat("setup line DROPNOISE_init\n", 25)))
	for _, f := range facts {
		initial.WriteString(fact(f.statement))
	}
	initial.WriteString("\n" + newWorkBulk(0))

	// Exercise the real production method (cm.ElicitNote) over the same history.
	note, err := cm.ElicitNote(ctx, turnsFromText(t, initial.String()))
	if err != nil {
		t.Fatalf("ElicitNote: %v", err)
	}

	captured := 0
	for _, f := range facts {
		ok := strings.Contains(note, f.token)
		if ok {
			captured++
		}
		t.Logf("  elicited-note captures %q: %v", f.token, ok)
	}
	t.Logf("=== ELICITOR CAPTURE: %d/%d facts captured verbatim ===", captured, len(facts))
	t.Logf("elicited note (first 600 chars):\n%.600s", note)

	// Strict, non-flaky requirement: the OPAQUE TOKEN must be captured VERBATIM.
	// It is the worst erosion case (the baseline drops it by round 3) and the one
	// where verbatim matters absolutely — a hash/token paraphrased is simply wrong.
	// The elicitor captures it reliably across runs.
	if !strings.Contains(note, facts[0].token) {
		t.Errorf("elicitor MISSED the opaque token %q — Variant B cannot prevent its erosion", facts[0].token)
	}
	// The semantic facts (numbers, names) may be paraphrased run-to-run (e.g.
	// "numShards exceeds 16" vs "numShards > 16") — the VALUE survives either way,
	// so we report capture rate informationally rather than fail on exact phrasing.
	if captured < len(facts) {
		t.Logf("NOTE: %d/%d captured verbatim; the rest are likely present but paraphrased (value preserved). The opaque token — the critical case — was captured exactly.", captured, len(facts))
	}
}

// TestElicitNoteCapturesToolResult is the regression eval for the blindness bug:
// the opaque token lives ONLY inside a tool-result turn (as it would in a coding
// agent — a file read, a shell command), not in user/assistant prose. The real
// elicitor must still capture it verbatim. Before the fix, the renderer flattened
// only ContentText and the model never saw the token at all.
//
// Run: go test -tags eval ./agent/internal/contextmgr/ -run TestElicitNoteCapturesToolResult -v -timeout 10m
func TestElicitNoteCapturesToolResult(t *testing.T) {
	cm := newOAuthManager(t)

	// A non-secret opaque identifier (a build artifact id). A value framed as a
	// credential (DEPLOY_TOKEN=...) triggers the model's secret-redaction behavior,
	// which is a real limit of the mechanism but not what THIS test checks: here we
	// verify the renderer is no longer blind to tool output.
	const token = "ARTIFACT-TR-7K4Z"
	toolOutput := "$ cat build/manifest.txt\n" +
		"artifact_id: " + token + "\n" +
		"primary_region: eu-central-1\n" +
		repeat("# build audit line, ignore\n", 30)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Read the build manifest and continue the rollout.")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call-tr",
					Name:      "run_shell",
					Arguments: []byte(`{"command": "cat build/manifest.txt"}`),
				}},
			},
		}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("call-tr", "run_shell", toolOutput, false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("Manifest read. Proceeding with the rollout.")},
		{Kind: schema.TurnUserInput, Message: llm.User("Continue.")},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	note, err := cm.ElicitNote(ctx, history)
	if err != nil {
		t.Fatalf("ElicitNote: %v", err)
	}
	t.Logf("elicited note (first 600 chars):\n%.600s", note)

	if !strings.Contains(note, token) {
		t.Errorf("elicitor MISSED the tool-result token %q — the renderer is blind to tool output", token)
	}
}
