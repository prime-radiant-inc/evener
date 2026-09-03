package agent

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// This regression counterexample keeps raw shorthand at the accepted byte cap,
// while its private normalized representation grows past that cap. The three
// calls have different questions and therefore different semantic identities.
func TestReviewNearCapNormalizedAskDoesNotCollapseDistinctSemantics(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	repairedCh := drainRepairedEvents(sess)

	registered := sess.reg.Get("ask_user")
	if registered == nil {
		t.Fatal("ask_user not registered")
	}
	prefix := `{"question":"`
	suffix := `","options":[{"label":"Only","detail":"one"}]}`
	padding := tool.MaxToolArgumentBytes - len(prefix) - len(suffix)
	if padding <= 0 {
		t.Fatal("bad fixture overhead")
	}

	exact, semantic := map[string]bool{}, map[string]bool{}
	for i, ch := range []string{"A", "B", "C"} {
		raw := []byte(prefix + strings.Repeat(ch, padding) + suffix)
		if len(raw) != tool.MaxToolArgumentBytes {
			t.Fatalf("raw size = %d, want %d", len(raw), tool.MaxToolArgumentBytes)
		}
		call := llm.ToolCallData{ID: fmt.Sprintf("near-cap-%d", i), Name: "ask_user", Arguments: raw}
		prep := prepareToolCall(call, registered, sess.reg.Names(), "ask_user", sess.resultToolName(), "")
		if prep.PrevalErr == "" || prep.Boundary != "schema_validation" {
			t.Fatalf("call %d did not reach post-normalization schema failure: %+v", i+1, prep)
		}
		if len(prep.SemanticArguments) <= tool.MaxToolArgumentBytes {
			t.Fatalf("normalized semantic size = %d, want over %d", len(prep.SemanticArguments), tool.MaxToolArgumentBytes)
		}
		if len(prep.Changes) != 0 || !bytes.Equal(prep.Call.Arguments, raw) {
			t.Fatalf("call %d committed failed normalization/repair: changes=%+v raw changed=%t", i+1, prep.Changes, !bytes.Equal(prep.Call.Arguments, raw))
		}
		res := sess.execTool(context.Background(), call, "")
		if strings.Contains(res.Output, "semantic failure loop") {
			t.Fatalf("distinct semantic call %d was falsely parked: %#v", i+1, res)
		}
		if !res.IsError || !res.PrevalOnly || res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || len(res.BreakerExactSignature) > 96 || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("call %d lacks bounded prevalidation identity: %#v", i+1, res)
		}
		exact[res.BreakerExactSignature] = true
		semantic[res.BreakerSemanticSignature] = true
	}
	if len(exact) != 3 || len(semantic) != 3 {
		t.Fatalf("distinct near-cap calls produced exact/semantic identities %d/%d, want 3/3", len(exact), len(semantic))
	}
	sess.Close()
	if repaired := <-repairedCh; len(repaired) != 0 {
		t.Fatalf("near-cap schema failures emitted repair telemetry: %+v", repaired)
	}
}

func TestSession_NearCapEquivalentNormalizedAskFailuresStillPark(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	repairedCh := drainRepairedEvents(sess)

	registered := sess.reg.Get("ask_user")
	if registered == nil {
		t.Fatal("ask_user not registered")
	}
	const slack = 4
	prefix := `{"question":"`
	suffix := `","options":[{"label":"Only","detail":"one"}]}`
	padding := tool.MaxToolArgumentBytes - len(prefix) - len(suffix) - slack
	base := prefix + strings.Repeat("S", padding) + suffix
	raw := [][]byte{
		[]byte(base + strings.Repeat(" ", slack)),
		[]byte(" " + base + strings.Repeat(" ", slack-1)),
		[]byte("  " + base + strings.Repeat(" ", slack-2)),
	}

	var normalized string
	results := make([]tool.ExecResult, 0, len(raw))
	for i, arguments := range raw {
		if len(arguments) != tool.MaxToolArgumentBytes {
			t.Fatalf("raw call %d size = %d, want %d", i+1, len(arguments), tool.MaxToolArgumentBytes)
		}
		call := llm.ToolCallData{ID: fmt.Sprintf("near-cap-equivalent-%d", i), Name: "ask_user", Arguments: arguments}
		prep := prepareToolCall(call, registered, sess.reg.Names(), "ask_user", sess.resultToolName(), "")
		if prep.PrevalErr == "" || prep.Boundary != "schema_validation" || len(prep.SemanticArguments) <= tool.MaxToolArgumentBytes {
			t.Fatalf("call %d did not reach oversize post-normalization schema failure: %+v", i+1, prep)
		}
		if len(prep.Changes) != 0 || !bytes.Equal(prep.Call.Arguments, arguments) {
			t.Fatalf("call %d committed failed normalization/repair: changes=%+v raw changed=%t", i+1, prep.Changes, !bytes.Equal(prep.Call.Arguments, arguments))
		}
		if i == 0 {
			normalized = string(prep.SemanticArguments)
		} else if string(prep.SemanticArguments) != normalized {
			t.Fatalf("equivalent call %d normalized differently", i+1)
		}

		res := sess.execTool(context.Background(), call, "")
		results = append(results, res)
		if !res.IsError || !res.PrevalOnly {
			t.Fatalf("call %d = %#v, want prevalidation failure", i+1, res)
		}
		if i < 2 && strings.Contains(res.Output, "semantic failure loop") {
			t.Fatalf("equivalent call %d parked early: %#v", i+1, res)
		}
	}
	if !strings.Contains(results[2].Output, "semantic failure loop") {
		t.Fatalf("third equivalent near-cap failure was not parked: %#v", results[2])
	}
	exact := map[string]bool{}
	for i, res := range results {
		exact[res.BreakerExactSignature] = true
		if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" || res.BreakerSemanticSignature != results[0].BreakerSemanticSignature || len(res.BreakerExactSignature) > 96 || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("equivalent call %d identity = %#v, want bounded shared semantic identity", i+1, res)
		}
	}
	if len(exact) != len(raw) {
		t.Fatalf("distinct raw calls collapsed to %d exact identities, want %d", len(exact), len(raw))
	}
	sess.Close()
	if repaired := <-repairedCh; len(repaired) != 0 {
		t.Fatalf("equivalent near-cap schema failures emitted repair telemetry: %+v", repaired)
	}
}
