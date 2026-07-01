package llm

import (
	"context"
	"strings"
	"testing"
)

// fuzzCountAdapter is a stub ProviderAdapter that also implements
// InputTokenCounter, returning fuzzer-controlled outcomes. It honors the adapter
// contract (Complete/Stream never return nil,nil) so any panic reproduced
// through it is a real dispatch bug, not a harness artifact.
type fuzzCountAdapter struct {
	name   string
	out    InputTokenCount
	err    error
	called bool
}

func (a *fuzzCountAdapter) Name() string { return a.name }
func (a *fuzzCountAdapter) Complete(_ context.Context, _ Request) (Response, error) {
	return Response{}, nil
}
func (a *fuzzCountAdapter) Stream(_ context.Context, _ Request) (Stream, error) {
	return doneStream{}, nil
}
func (a *fuzzCountAdapter) CountInputTokens(_ context.Context, _ Request) (InputTokenCount, error) {
	a.called = true
	return a.out, a.err
}

// FuzzCountInputTokensDispatch drives Client.CountInputTokens — request
// validation, provider resolution/normalization, the InputTokenCounter
// type-assertion branch, the ErrInputTokenCountUnsupported local-estimate
// fallback, error rewriting/tagging, and the Source/Exact/Provider/Model
// stamping — over an arbitrary request and an arbitrary adapter outcome. A stub
// stands in for the network; the fuzzer never makes a real call.
//
// Oracles:
//   - never panics; the call is deterministic (same inputs -> same outcome).
//   - ErrInputTokenCountUnsupported yields the deterministic local estimate:
//     Source=local_estimate, Exact=false, non-negative Tokens equal to
//     EstimateInputTokens(req), with the resolved provider stamped, and a nil
//     error (the fallback swallows the sentinel).
//   - a nil-error provider result is always stamped: a non-empty Source, the
//     resolved Provider, and (when the adapter left it blank) the request Model;
//     when Source is "provider" the count is marked Exact.
//   - a non-sentinel error is surfaced as a non-nil error with an empty count.
func FuzzCountInputTokensDispatch(f *testing.F) {
	f.Add("openai", "gpt-5.2", "sys", "hello", 42, false, false, "")
	f.Add("Anthropic", "claude-opus-4-5", "", "hi", 0, true, false, "provider")
	f.Add("google", "gemini-2.5-pro", "s", "u", -7, false, true, "")
	f.Add("", "m", "", "x", 100, false, false, "local_estimate")

	f.Fuzz(func(t *testing.T, provider, model, sysText, userText string, tokens int, unsupported, otherErr bool, source string) {
		req := Request{
			Provider: "stub",
			Model:    model,
			Messages: []Message{System(sysText), User(userText)},
		}

		var advErr error
		switch {
		case unsupported:
			advErr = ErrInputTokenCountUnsupported
		case otherErr:
			advErr = ErrorFromHTTPStatus("stub", 429, "rate limited", nil, nil)
		}

		newAdapter := func() *fuzzCountAdapter {
			return &fuzzCountAdapter{
				name: "stub",
				out: InputTokenCount{
					Tokens:   tokens,
					Source:   TokenCountSource(source),
					Provider: provider,
				},
				err: advErr,
			}
		}

		newClient := func(a *fuzzCountAdapter) *Client {
			c := NewClient()
			c.Register(a)
			return c
		}

		a1 := newAdapter()
		got, err := newClient(a1).CountInputTokens(context.Background(), req)

		// Determinism: a second independent run must agree.
		a2 := newAdapter()
		got2, err2 := newClient(a2).CountInputTokens(context.Background(), req)
		if (err == nil) != (err2 == nil) ||
			got.Tokens != got2.Tokens || got.Exact != got2.Exact ||
			got.Source != got2.Source || got.Provider != got2.Provider || got.Model != got2.Model {
			t.Fatalf("dispatch not deterministic: (%+v,%v) vs (%+v,%v)", got, err, got2, err2)
		}

		// req.Provider is "stub"; the client resolves it via normalizeProviderName.
		wantProv := normalizeProviderName("stub")

		// Validation runs before dispatch: a blank model errors out before the
		// adapter is ever consulted.
		if strings.TrimSpace(model) == "" {
			if err == nil {
				t.Fatalf("blank model accepted; got %+v", got)
			}
			if a1.called {
				t.Fatalf("dispatch reached the adapter despite an invalid request")
			}
			return
		}

		switch {
		case unsupported:
			if err != nil {
				t.Fatalf("unsupported sentinel should fall back cleanly, got err=%v", err)
			}
			est := EstimateInputTokens(Request{Provider: wantProv, Model: model, Messages: req.Messages})
			if got.Source != TokenCountSourceLocalEstimate || got.Exact {
				t.Fatalf("unsupported fallback mislabeled: %+v", got)
			}
			if got.Tokens != est.Tokens || got.Tokens < 0 {
				t.Fatalf("unsupported fallback tokens=%d, want deterministic estimate %d (>=0)", got.Tokens, est.Tokens)
			}
			if got.Provider != wantProv {
				t.Fatalf("unsupported fallback provider=%q, want %q", got.Provider, wantProv)
			}
		case otherErr:
			if err == nil {
				t.Fatalf("non-sentinel adapter error swallowed; got %+v", got)
			}
			if got.Tokens != 0 || got.Exact || got.Source != "" || got.Provider != "" || got.Model != "" {
				t.Fatalf("error path returned a non-zero count: %+v", got)
			}
		default:
			if err != nil {
				t.Fatalf("nil adapter error but dispatch returned err=%v", err)
			}
			if got.Source == "" {
				t.Fatalf("success left Source unstamped: %+v", got)
			}
			// Dispatch stamps the resolved provider only when the adapter left it
			// blank; a non-empty adapter Provider is preserved verbatim.
			wantStamped := provider
			if provider == "" {
				wantStamped = wantProv
			}
			if got.Provider != wantStamped {
				t.Fatalf("success Provider=%q, want %q", got.Provider, wantStamped)
			}
			if got.Model == "" && model != "" {
				t.Fatalf("success left Model unstamped though req.Model=%q", model)
			}
			if got.Source == TokenCountSourceProvider && !got.Exact {
				t.Fatalf("provider-sourced count not marked Exact: %+v", got)
			}
		}

		if !a1.called {
			t.Fatalf("dispatch never reached the InputTokenCounter adapter")
		}
	})
}
