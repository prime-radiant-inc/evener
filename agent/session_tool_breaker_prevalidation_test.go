package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestSessionPrevalidationFailuresReachSemanticBreaker(t *testing.T) {
	sess := newSession(t, withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))
	defer sess.Close()

	tests := []struct {
		name string
		tool string
		args func(int) string
	}{
		{"unknown", "definitely_unknown_829", func(i int) string { return fmt.Sprintf(`{"intent":"variant %d"}`, i) }},
		{"schema", "job_stop", func(i int) string { return fmt.Sprintf(`{"intent":"variant %d"}`, i) }},
		{"json", "job_stop", func(int) string { return `{"target":` }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var first string
			var second string
			var third string
			var semantic string
			for i := range 3 {
				res := sess.execTool(context.Background(), llm.ToolCallData{
					ID:        fmt.Sprintf("%s-%d", tc.name, i),
					Name:      tc.tool,
					Arguments: []byte(tc.args(i)),
				}, "")
				if !res.PrevalOnly {
					t.Fatalf("prevalidation result %d lost its no-dispatch marker: %#v", i+1, res)
				}
				switch i {
				case 0:
					first = res.Output
				case 1:
					second = res.Output
				case 2:
					third, semantic = res.Output, res.BreakerSemanticSignature
				}
			}
			if strings.Contains(first, "You just ran") || strings.Contains(first, "did not execute") {
				t.Fatalf("first prevalidation failure unexpectedly advanced breaker: %q", first)
			}
			if !strings.Contains(second, "You just ran") || strings.Contains(second, "did not execute") {
				t.Fatalf("second prevalidation failure did not produce its breaker nudge: %q", second)
			}
			if semantic == "" || !strings.Contains(third, "semantic failure loop") {
				t.Fatalf("prevalidation bypassed semantic breaker: signature=%q output=%q", semantic, third)
			}
		})
	}
}

func TestSessionRegisterToolReplacementDoesNotInheritShellDefaults(t *testing.T) {
	sess := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}))
	defer sess.Close()
	calls := 0
	sess.RegisterTool("shell", "custom shell", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		calls++
		return nil, fmt.Errorf("custom shell failure")
	})
	for i, args := range []string{`{"command":"false"}`, `{"command":"false","mode":"foreground"}`, `{"command":"false","intent":"retry"}`} {
		res := sess.execTool(context.Background(), llm.ToolCallData{ID: fmt.Sprintf("custom-shell-%d", i), Name: "shell", Arguments: []byte(args)}, "")
		if strings.Contains(res.Output, "semantic failure loop") {
			t.Fatalf("custom shell replacement inherited core defaults: %#v", res)
		}
	}
	if calls != 3 {
		t.Fatalf("custom shell calls=%d, want 3", calls)
	}
}
