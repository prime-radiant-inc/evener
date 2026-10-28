package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// planStubProtocol is a Protocol whose BuildBody is scripted, so the plan
// computation can be driven past each of its error arms without a wire.
type planStubProtocol struct {
	body     map[string]any
	buildErr error
}

func (planStubProtocol) ID() string              { return "plan-stub" }
func (planStubProtocol) PrunablePaths() []string { return nil }
func (s planStubProtocol) BuildBody(Request, registry.Resolved) (map[string]any, error) {
	if s.buildErr != nil {
		return nil, s.buildErr
	}
	return s.body, nil
}
func (planStubProtocol) Complete(context.Context, Request, registry.Resolved) (Response, error) {
	return Response{}, nil
}
func (planStubProtocol) Stream(context.Context, Request, registry.Resolved) (Stream, error) {
	return nil, ErrStreamUnsupported
}
func (planStubProtocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return nil, ErrModelListingUnsupported
}
func (planStubProtocol) CountTokens(context.Context, Request, registry.Resolved) (int, error) {
	return 0, ErrInputTokenCountUnsupported
}

func continuationCapableRow(auth string) registry.Resolved {
	return registry.Resolved{
		Instance: "gateway",
		ModelID:  "gpt-5.5",
		Protocol: registry.ProtocolOpenAIResponses,
		Transport: registry.Transport{
			BaseURL:  "https://gateway.test",
			Endpoint: "/responses",
			Auth:     auth,
		},
		Caps: registry.Caps{Fields: map[string]bool{"previous_response_id": true, "store": true}},
	}
}

func planningClient(t *testing.T) *Client {
	t.Helper()
	return NewClient(WithClientStateDir(t.TempDir()))
}

// A body that cannot be fingerprinted fails the plan: the fingerprint helper
// reports the marshal failure rather than hashing a partial body.
func TestResponsesRequestFingerprintRejectsAnUnmarshalableBody(t *testing.T) {
	_, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Fatal("fingerprint of an unmarshalable body succeeded")
	}
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want the json marshal failure wrapped", err)
	}
}

// Each failure the plan computation can meet must surface as the plan's own
// error, with the endpoint family still set so the caller can fall back to
// full history.
func TestPlanContinuationSurfacesEachBuildFailure(t *testing.T) {
	buildFailed := errors.New("no body could be built")
	cases := []struct {
		name     string
		protocol Protocol
		wantErr  error
	}{
		{"build failure", planStubProtocol{buildErr: buildFailed}, buildFailed},
		{"unfingerprintable body", planStubProtocol{body: map[string]any{"input": "hi", "bad": make(chan int)}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := continuationCapableRow("")
			plan, err := planningClient(t).planContinuation(context.Background(), Request{Model: "gpt-5.5"}, res, tc.protocol)
			if err == nil {
				t.Fatal("plan succeeded despite the scripted failure")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want the scripted build failure", err)
			}
			if plan.EndpointFamily != ResponsesEndpointFamilyOpenAIPublic {
				t.Fatalf("endpoint family = %q, want it preserved for the fallback", plan.EndpointFamily)
			}
			if plan.RequestFingerprint != "" || plan.ContinuationStorageAllowed {
				t.Fatalf("a failed plan must not carry fingerprints or storage permission: %+v", plan)
			}
		})
	}
}

// A public-family body that does not set store plans as no-store: the label
// and the storage permission both record that the provider keeps nothing.
func TestPlanContinuationLabelsAPublicNoStoreBody(t *testing.T) {
	res := continuationCapableRow("")
	plan, err := planningClient(t).planContinuation(context.Background(), Request{Model: "gpt-5.5"}, res,
		planStubProtocol{body: map[string]any{"input": "hi"}})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.StoragePolicyLabel != ResponsesStoragePolicyPublicOpenAINoStore || plan.ContinuationStorageAllowed {
		t.Fatalf("plan = %+v, want the no-store label without storage permission", plan)
	}
	if plan.RequestFingerprint == "" || plan.StorageScopeFingerprint == "" {
		t.Fatalf("a computed plan carries fingerprints: %+v", plan)
	}
}

// A Codex-scheme row whose credential is not the OAuth record the scope reads
// fails the plan with the sign-in sentinel: the account and workspace claims
// are part of the storage scope, and a row that cannot name them cannot plan.
func TestPlanContinuationCodexScopeWithoutOAuthFailsWithSignInRequired(t *testing.T) {
	if _, ok := AuthenticatorFor(registry.AuthOAuthOpenAICodex); !ok {
		t.Fatal("the codex authenticator must be registered (the llm_test package imports tokenauth); without it the scope seam this test drives does not exist")
	}
	res := continuationCapableRow(registry.AuthOAuthOpenAICodex)
	res.Credential.Source = "api_key"
	plan, err := planningClient(t).planContinuation(context.Background(), Request{Model: "gpt-5.6"}, res,
		planStubProtocol{body: map[string]any{"input": "hi"}})
	if !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v, want ErrSignInRequired from the codex scope read", err)
	}
	if plan.ContinuationStorageAllowed {
		t.Fatalf("plan = %+v, want no storage permission alongside the failure", plan)
	}
}
