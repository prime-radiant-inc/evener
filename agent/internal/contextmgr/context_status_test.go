package contextmgr

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// textHistory builds n assistant turns of exactly chars ASCII characters each.
// The char/4 estimator bills a text-only turn at len(chars)/4, so 400-char
// turns are exactly 100 tokens apiece.
func textHistory(n, chars int) []schema.Turn {
	history := make([]schema.Turn, 0, n)
	for range n {
		history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("x", chars))))
	}
	return history
}

// TestContextManager_EstimateStatusMatchesFixtureValues pins EstimateStatus
// against values authored here, not computed by the estimator it exercises.
// The measured rows pin today's rule that the API-measurement path ignores
// sysPromptChars; the overflow row pins Remaining == 0 while pressure exceeds
// 1.0. EstimateUsage and Pressure are checked only as a secondary preservation
// check, so the fixture arithmetic above is the primary oracle.
func TestContextManager_EstimateStatusMatchesFixtureValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		profile        *provider.Profile
		history        []schema.Turn
		sysPromptChars int
		recordTokens   int
		recordLen      int
		want           schema.ContextMetrics
		wantPressure   float64
	}{
		{
			name:    "nil profile",
			profile: nil,
			history: textHistory(3, 400),
			want:    schema.ContextMetrics{},
		},
		{
			name:           "known window",
			profile:        testProfile("openai", "test", 1000),
			history:        textHistory(3, 400),
			sysPromptChars: 400,
			want:           schema.ContextMetrics{Used: 400, Window: 1000, Remaining: 600},
			wantPressure:   0.4,
		},
		{
			name:           "measured",
			profile:        testProfile("openai", "test", 1000),
			history:        textHistory(3, 400),
			sysPromptChars: 400,
			recordTokens:   500,
			recordLen:      1,
			want:           schema.ContextMetrics{Used: 700, Window: 1000, Remaining: 300},
			wantPressure:   0.7,
		},
		{
			name:           "measured length beyond history",
			profile:        testProfile("openai", "test", 1000),
			history:        textHistory(3, 400),
			sysPromptChars: 400,
			recordTokens:   500,
			recordLen:      99,
			want:           schema.ContextMetrics{Used: 400, Window: 1000, Remaining: 600},
			wantPressure:   0.4,
		},
		{
			name:    "overflow",
			profile: testProfile("openai", "test", 1000),
			history: textHistory(3, 4000),
			want:    schema.ContextMetrics{Used: 3000, Window: 1000, Remaining: 0},
			// Pressure is allowed to exceed 1.0.
			wantPressure: 3.0,
		},
		{
			name:    "assumed window",
			profile: &provider.Profile{},
			history: textHistory(3, 400),
			want: schema.ContextMetrics{
				Used:      300,
				Window:    AssumedContextWindow,
				Remaining: 127700,
				Assumed:   true,
			},
			wantPressure: 300.0 / 128000.0,
		},
		{
			name:           "empty history",
			profile:        testProfile("openai", "test", 1000),
			sysPromptChars: 400,
			want:           schema.ContextMetrics{Used: 100, Window: 1000, Remaining: 900},
			wantPressure:   0.1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cm := NewManager(tc.profile, nil, nil)
			if tc.recordTokens != 0 {
				cm.RecordInputTokens(tc.recordTokens, tc.recordLen)
			}

			got, gotPressure := cm.EstimateStatus(tc.history, tc.sysPromptChars)
			if got != tc.want {
				t.Fatalf("EstimateStatus metrics = %+v, want %+v", got, tc.want)
			}
			if gotPressure != tc.wantPressure {
				t.Fatalf("EstimateStatus pressure = %v, want %v", gotPressure, tc.wantPressure)
			}

			if wantRemaining := max(got.Window-got.Used, 0); got.Remaining != wantRemaining {
				t.Fatalf("Remaining = %d, want max(Window-Used, 0) = %d", got.Remaining, wantRemaining)
			}
			wantRatio := 0.0
			if got.Window != 0 {
				wantRatio = float64(got.Used) / float64(got.Window)
			}
			if gotPressure != wantRatio {
				t.Fatalf("pressure = %v, want Used/Window = %v", gotPressure, wantRatio)
			}

			// Secondary preservation checks: the existing readers must agree
			// with the combined calculation.
			if usage := cm.EstimateUsage(tc.history, tc.sysPromptChars); usage != got {
				t.Fatalf("EstimateUsage = %+v, want EstimateStatus metrics %+v", usage, got)
			}
			if p := cm.Pressure(tc.history, tc.sysPromptChars); p != gotPressure {
				t.Fatalf("Pressure = %v, want EstimateStatus pressure %v", p, gotPressure)
			}
			if p := cm.EstimatePressure(tc.history, tc.sysPromptChars); p != gotPressure {
				t.Fatalf("EstimatePressure = %v, want EstimateStatus pressure %v", p, gotPressure)
			}
		})
	}
}
