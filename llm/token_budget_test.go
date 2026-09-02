package llm

import (
	"errors"
	"math"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestResolveContextBudgetIdentity(t *testing.T) {
	tests := []struct {
		name         string
		req          Request
		res          registry.Resolved
		wantProvider string
		wantModel    string
	}{
		{
			name:         "resolved instance and requested model take precedence",
			req:          Request{Provider: " request-instance ", Model: " requested-model "},
			res:          registry.Resolved{Instance: " resolved-instance ", ModelID: "resolved-model"},
			wantProvider: "resolved-instance",
			wantModel:    "requested-model",
		},
		{
			name:         "request provider is the instance fallback",
			req:          Request{Provider: " request-instance ", Model: "requested-model"},
			res:          registry.Resolved{ModelID: "resolved-model"},
			wantProvider: "request-instance",
			wantModel:    "requested-model",
		},
		{
			name:         "resolved model is the request fallback",
			req:          Request{Provider: "request-instance"},
			res:          registry.Resolved{Instance: "resolved-instance", ModelID: " resolved-model "},
			wantProvider: "resolved-instance",
			wantModel:    "resolved-model",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model := ResolveContextBudgetIdentity(tt.req, tt.res)
			if provider != tt.wantProvider || model != tt.wantModel {
				t.Fatalf("identity = %q/%q, want %q/%q", provider, model, tt.wantProvider, tt.wantModel)
			}
		})
	}
}

func TestApplyTokenBudget_IncidentMaxInputAndTotalClamp(t *testing.T) {
	req := Request{
		Model:               "glm-5.2-vision",
		Messages:            []Message{User("x")},
		InputTokensEstimate: 393_217,
		MaxTokens:           new(131_072),
	}
	res := registry.Resolved{Caps: registry.Caps{
		ContextWindow:   new(524_288),
		MaxInputTokens:  new(393_216),
		MaxOutputTokens: new(131_072),
	}}

	_, budget, err := ApplyTokenBudget(req, res)
	var budgetErr *ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("ApplyTokenBudget error = %v, want *ContextBudgetError", err)
	}
	if budgetErr.Limit != "max_input" || budgetErr.Maximum != 393_216 {
		t.Fatalf("ContextBudgetError = %+v, want max-input limit 393216", budgetErr)
	}
	if Kind(err) != KindContextLength || budgetErr.Retryable() {
		t.Fatalf("local budget error kind/retryability = %v/%v, want context_length/false", Kind(err), budgetErr.Retryable())
	}
	if Classify(err) != ErrorClassPermanent || DeclaredKind(err) != KindContextLength {
		t.Fatalf("local budget error classification = %v/%v, want permanent/context_length", Classify(err), DeclaredKind(err))
	}
	rewritten := RewriteErrorProvider(err, "remote-looking")
	var rewrittenBudgetErr *ContextBudgetError
	if !errors.As(rewritten, &rewrittenBudgetErr) || rewrittenBudgetErr != budgetErr {
		t.Fatalf("local budget error was rewritten as a provider error: %T %v", rewritten, rewritten)
	}
	if budget.InputTokens <= 393_217 {
		t.Fatalf("budget.InputTokens = %d, want estimate plus safety reserve", budget.InputTokens)
	}

	res.Caps.MaxInputTokens = nil
	shaped, budget, err := ApplyTokenBudget(req, res)
	if err != nil {
		t.Fatalf("ApplyTokenBudget without max-input cap: %v", err)
	}
	if budget.InputTokens+budget.AdmittedOutput > 524_288 {
		t.Fatalf("admitted total = %d + %d = %d, exceeds 524288", budget.InputTokens, budget.AdmittedOutput, budget.InputTokens+budget.AdmittedOutput)
	}
	if !budget.LimitedOutput || shaped.MaxTokens == nil || *shaped.MaxTokens != budget.AdmittedOutput {
		t.Fatalf("total cap did not reduce output: shaped=%v budget=%+v", shaped.MaxTokens, budget)
	}
}

func TestApplyTokenBudget_CapCombinationsAndBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		req           Request
		caps          registry.Caps
		wantInput     int
		wantSafety    int
		wantRequested int
		wantAdmitted  int
		wantLimited   bool
		wantMax       *int
		wantErrLimit  string
		wantErrMax    int
	}{
		{
			name:          "no caps",
			req:           Request{InputTokensEstimate: 100, MaxTokens: new(5_000)},
			wantInput:     1_124,
			wantSafety:    1_024,
			wantRequested: 5_000,
			wantAdmitted:  5_000,
			wantMax:       new(5_000),
		},
		{
			name:          "output only",
			req:           Request{MaxTokens: new(8_192)},
			caps:          registry.Caps{MaxOutputTokens: new(4_096)},
			wantInput:     1_024,
			wantSafety:    1_024,
			wantRequested: 8_192,
			wantAdmitted:  4_096,
			wantLimited:   true,
			wantMax:       new(4_096),
		},
		{
			name:          "input only",
			req:           Request{InputTokensEstimate: 3_000, MaxTokens: new(777)},
			caps:          registry.Caps{MaxInputTokens: new(5_000)},
			wantInput:     4_024,
			wantSafety:    1_024,
			wantRequested: 777,
			wantAdmitted:  777,
			wantMax:       new(777),
		},
		{
			name:         "total only with unset output",
			req:          Request{InputTokensEstimate: 2_000},
			caps:         registry.Caps{ContextWindow: new(10_000)},
			wantInput:    3_024,
			wantSafety:   1_024,
			wantAdmitted: 6_976,
			wantMax:      new(6_976),
		},
		{
			name:          "all caps",
			req:           Request{InputTokensEstimate: 10_000, MaxTokens: new(8_000)},
			caps:          registry.Caps{ContextWindow: new(20_000), MaxInputTokens: new(15_000), MaxOutputTokens: new(6_000)},
			wantInput:     11_024,
			wantSafety:    1_024,
			wantRequested: 8_000,
			wantAdmitted:  6_000,
			wantLimited:   true,
			wantMax:       new(6_000),
		},
		{
			name:          "exact boundary",
			req:           Request{InputTokensEstimate: 8_976, MaxTokens: new(1_000)},
			caps:          registry.Caps{ContextWindow: new(11_000), MaxInputTokens: new(10_000)},
			wantInput:     10_000,
			wantSafety:    1_024,
			wantRequested: 1_000,
			wantAdmitted:  1_000,
			wantMax:       new(1_000),
		},
		{
			name:          "one-token max-input overflow",
			req:           Request{InputTokensEstimate: 8_977, MaxTokens: new(1)},
			caps:          registry.Caps{MaxInputTokens: new(10_000)},
			wantInput:     10_001,
			wantSafety:    1_024,
			wantRequested: 1,
			wantAdmitted:  1,
			wantMax:       new(1),
			wantErrLimit:  "max_input",
			wantErrMax:    10_000,
		},
		{
			name:          "non-positive request max uses known output",
			req:           Request{MaxTokens: new(-1)},
			caps:          registry.Caps{MaxOutputTokens: new(4_096)},
			wantInput:     1_024,
			wantSafety:    1_024,
			wantRequested: 4_096,
			wantAdmitted:  4_096,
			wantMax:       new(4_096),
		},
		{
			name:          "continuation shadow larger than delta",
			req:           Request{InputTokensEstimate: 1_000, FullHistoryInputTokensEstimate: 4_000, MaxTokens: new(100)},
			caps:          registry.Caps{MaxInputTokens: new(5_024)},
			wantInput:     5_024,
			wantSafety:    1_024,
			wantRequested: 100,
			wantAdmitted:  100,
			wantMax:       new(100),
		},
		{
			name:          "smallest applicable limit sets percentage reserve",
			req:           Request{InputTokensEstimate: 100_000, MaxTokens: new(1)},
			caps:          registry.Caps{ContextWindow: new(500_000), MaxInputTokens: new(200_000)},
			wantInput:     102_000,
			wantSafety:    2_000,
			wantRequested: 1,
			wantAdmitted:  1,
			wantMax:       new(1),
		},
		{
			name:          "no positive total headroom",
			req:           Request{InputTokensEstimate: 8_976, MaxTokens: new(1)},
			caps:          registry.Caps{ContextWindow: new(10_000)},
			wantInput:     10_000,
			wantSafety:    1_024,
			wantRequested: 1,
			wantAdmitted:  0,
			wantLimited:   true,
			wantMax:       new(1),
			wantErrLimit:  "context_window",
			wantErrMax:    10_000,
		},
		{
			name:          "very large integers saturate instead of wrapping",
			req:           Request{InputTokensEstimate: math.MaxInt - 1, MaxTokens: new(math.MaxInt)},
			caps:          registry.Caps{ContextWindow: new(math.MaxInt), MaxOutputTokens: new(math.MaxInt)},
			wantInput:     math.MaxInt,
			wantSafety:    math.MaxInt/100 + 1,
			wantRequested: math.MaxInt,
			wantAdmitted:  0,
			wantLimited:   true,
			wantMax:       new(math.MaxInt),
			wantErrLimit:  "context_window",
			wantErrMax:    math.MaxInt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.Model = "model"
			tt.req.Messages = []Message{User("x")}
			originalMax := tt.req.MaxTokens
			gotReq, gotBudget, err := ApplyTokenBudget(tt.req, registry.Resolved{Instance: "instance", Caps: tt.caps})

			if tt.wantErrLimit == "" {
				if err != nil {
					t.Fatalf("ApplyTokenBudget: %v", err)
				}
			} else {
				var budgetErr *ContextBudgetError
				if !errors.As(err, &budgetErr) || budgetErr.Limit != tt.wantErrLimit || budgetErr.Maximum != tt.wantErrMax {
					t.Fatalf("error = %v, want ContextBudgetError limit=%q maximum=%d", err, tt.wantErrLimit, tt.wantErrMax)
				}
			}
			if gotBudget.InputTokens != tt.wantInput || gotBudget.SafetyTokens != tt.wantSafety ||
				gotBudget.RequestedOutput != tt.wantRequested || gotBudget.AdmittedOutput != tt.wantAdmitted ||
				gotBudget.LimitedOutput != tt.wantLimited {
				t.Fatalf("budget = %+v, want input=%d safety=%d requested=%d admitted=%d limited=%v",
					gotBudget, tt.wantInput, tt.wantSafety, tt.wantRequested, tt.wantAdmitted, tt.wantLimited)
			}
			if (gotReq.MaxTokens == nil) != (tt.wantMax == nil) || (gotReq.MaxTokens != nil && *gotReq.MaxTokens != *tt.wantMax) {
				t.Fatalf("request MaxTokens = %v, want %v", gotReq.MaxTokens, tt.wantMax)
			}
			if err == nil && gotReq.MaxTokens != nil && originalMax != nil && gotReq.MaxTokens == originalMax {
				t.Fatal("ApplyTokenBudget wrote through the caller's MaxTokens pointer")
			}
		})
	}
}

func TestShapeRequestClampsKnownOutputCap(t *testing.T) {
	requested := 8_192
	req := Request{MaxTokens: &requested}
	got := ShapeRequest(req, registry.Resolved{Caps: registry.Caps{MaxOutputTokens: new(4_096)}})
	if got.MaxTokens == nil || *got.MaxTokens != 4_096 {
		t.Fatalf("ShapeRequest MaxTokens = %v, want 4096", got.MaxTokens)
	}
	if requested != 8_192 || got.MaxTokens == req.MaxTokens {
		t.Fatal("ShapeRequest must return a constrained copy without writing through the caller's pointer")
	}
}
