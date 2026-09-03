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
			var third string
			var semantic string
			for i := range 3 {
				res := sess.execTool(context.Background(), llm.ToolCallData{
					ID:        fmt.Sprintf("%s-%d", tc.name, i),
					Name:      tc.tool,
					Arguments: []byte(tc.args(i)),
				}, "")
				if i == 2 {
					third, semantic = res.Output, res.BreakerSemanticSignature
				}
			}
			if semantic == "" || !strings.Contains(third, "semantic failure loop") {
				t.Fatalf("prevalidation bypassed semantic breaker: signature=%q output=%q", semantic, third)
			}
		})
	}
}
