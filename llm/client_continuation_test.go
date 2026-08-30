package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

func continuationClient(t *testing.T, r *registry.Registry) *llm.Client {
	t.Helper()
	return llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(t.TempDir()))
}

func TestPlanContinuationFromResolved(t *testing.T) {
	srv, _ := responsesServer(t)
	r := fixtureRegistry(t, srv.URL, map[string]registry.Provider{
		"groq": {Base: "groq", Protocol: registry.ProtocolOpenAIResponses, APIKey: "gk"},
		"azure": {Base: "azure", APIKey: "ak",
			Transport: registry.Transport{Vars: map[string]string{"AZURE_RESOURCE_NAME": "res1"}},
			Models:    map[string]registry.Model{"gpt55-prod": {AliasOf: "gpt-5.5"}}},
	})
	c := continuationClient(t, r)
	cases := []struct {
		ref     string
		family  llm.ResponsesEndpointFamily
		allowed bool
		policy  string
	}{
		{"openai/gpt-5.5", llm.ResponsesEndpointFamilyOpenAIPublic, true, llm.ResponsesStoragePolicyPublicOpenAIStore},
		{"groq/openai/gpt-oss-120b", llm.ResponsesEndpointFamilyOpenAIPublic, false, ""},
		{"work/glm-5", llm.ResponsesEndpointFamilyOpenAIPublic, false, ""},
		{"azure/gpt55-prod", llm.ResponsesEndpointFamilyOpenAIPublic, true, llm.ResponsesStoragePolicyPublicOpenAIStore},
		{"openai-codex/gpt-5.6", llm.ResponsesEndpointFamilyOpenAICodex, false, llm.ResponsesStoragePolicyCodexUnproven},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			inst, model, _ := strings.Cut(tc.ref, "/")
			req := userRequest(inst, model)
			req.Store = new(true)
			plan, err := c.PlanResponsesContinuation(context.Background(), req)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if plan.EndpointFamily != tc.family || plan.ContinuationStorageAllowed != tc.allowed || plan.StoragePolicyLabel != tc.policy {
				t.Fatalf("plan: %+v", plan)
			}
			if tc.allowed && (plan.RequestFingerprint == "" || plan.StorageScopeFingerprint == "" || plan.StorageScope.Provider != inst || plan.AuthScopeIdentity.CredentialHash == "") {
				t.Fatalf("an allowed plan carries fingerprints and scope: %+v", plan)
			}
			if !tc.allowed && plan.RequestFingerprint != "" {
				t.Fatalf("an unavailable endpoint has no fingerprint: %+v", plan)
			}
		})
	}
}

func TestPlanContinuationIsStableAcrossBuilds(t *testing.T) {
	srv, _ := responsesServer(t)
	c := continuationClient(t, fixtureRegistry(t, srv.URL, nil))
	req := userRequest("openai", "gpt-5.5")
	req.Store = new(true)
	first, err := c.PlanResponsesContinuation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	again := req
	again.Messages = []llm.Message{llm.User("different input")}
	again.PreviousResponseID = "resp_9"
	second, err := c.PlanResponsesContinuation(context.Background(), again)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestFingerprint != second.RequestFingerprint || first.StorageScopeFingerprint != second.StorageScopeFingerprint {
		t.Fatalf("fingerprints must ignore input and the anchor: %+v vs %+v", first, second)
	}
	req.ConversationID = "conv_1"
	third, err := c.PlanResponsesContinuation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if third.StorageScopeFingerprint == first.StorageScopeFingerprint {
		t.Fatal("the conversation id scopes storage")
	}
}

type planningOverride struct {
	recordingAdapter
	plan llm.ResponsesContinuationPlan
}

func (a *planningOverride) PlanResponsesContinuation(llm.Request) (llm.ResponsesContinuationPlan, error) {
	return a.plan, nil
}

func TestPlanContinuationHonorsOverridePlanner(t *testing.T) {
	srv, _ := responsesServer(t)
	c := continuationClient(t, fixtureRegistry(t, srv.URL, nil))
	want := llm.ResponsesContinuationPlan{EndpointFamily: llm.ResponsesEndpointFamilyOpenAIPublic, RequestFingerprint: "cont-req-v2:override"}
	override := &planningOverride{plan: want}
	override.name = "openai"
	c.Register(override)
	got, err := c.PlanResponsesContinuation(context.Background(), userRequest("openai", "gpt-5.5"))
	if err != nil || got.RequestFingerprint != want.RequestFingerprint {
		t.Fatalf("override planner: %v %+v", err, got)
	}
	c.Register(&recordingAdapter{name: "mute"})
	if _, err := c.PlanResponsesContinuation(context.Background(), userRequest("mute", "x")); err == nil {
		t.Fatal("an override without a planner cannot plan")
	}
}

func TestPlanContinuationNeedsAStateDir(t *testing.T) {
	srv, _ := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	_, err := c.PlanResponsesContinuation(context.Background(), userRequest("openai", "gpt-5.5"))
	if !errors.Is(err, llm.ErrContinuationSecretUnavailable) {
		t.Fatalf("want ErrContinuationSecretUnavailable, got %v", err)
	}
}

// TestPlanContinuationHashesTheCodexAuthScope drives the oauth half of the
// plan: on an instance whose Codex row can carry the anchor, the registered
// authenticator's AuthScope supplies the account and workspace claims, which
// reach the plan and the storage scope as hashes.
func TestPlanContinuationHashesTheCodexAuthScope(t *testing.T) {
	stateRoot := t.TempDir()
	record := authopenai.AuthRecord{Version: 1, Provider: "openai-codex", Source: authopenai.AuthSourceOAuth, TokenType: "Bearer", AccessToken: "tok", RefreshToken: "refresh", AccountID: "acct_1", WorkspaceID: "ws_1", ObtainedAt: time.Now(), Expiry: time.Now().Add(time.Hour)}
	if err := authopenai.SaveAuth(stateRoot, "codex", record); err != nil {
		t.Fatal(err)
	}
	previous := tokenauth.DefaultCodex.StateDir
	tokenauth.DefaultCodex.StateDir = stateRoot
	t.Cleanup(func() { tokenauth.DefaultCodex.StateDir = previous })

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(stateRoot),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"codex": {Base: "openai-codex", Caps: registry.Caps{Fields: map[string]bool{"previous_response_id": true, "store": true}}},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := continuationClient(t, r)
	req := userRequest("codex", "gpt-5.6")
	req.Store = new(true)
	plan, err := c.PlanResponsesContinuation(context.Background(), req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.EndpointFamily != llm.ResponsesEndpointFamilyOpenAICodex || plan.AuthScopeIdentity.AuthSource != "oauth" {
		t.Fatalf("plan: %+v", plan)
	}
	if plan.AuthScopeIdentity.AccountHash == "" || plan.AuthScopeIdentity.WorkspaceHash == "" || plan.StorageScope.AccountHash != plan.AuthScopeIdentity.AccountHash {
		t.Fatalf("the oauth claims must reach the plan as hashes: %+v", plan)
	}
	if plan.RequestFingerprint == "" || plan.ContinuationStorageAllowed || plan.StoragePolicyLabel != llm.ResponsesStoragePolicyCodexUnproven {
		t.Fatalf("codex storage stays unproven: %+v", plan)
	}

	// Every scope value is hashed the way it was tested for emptiness:
	// trimmed. A record whose claims carry stray whitespace must land on the
	// same anchor scope as one without, rather than hashing the account one
	// way into AccountHash and another into the credential composition.
	record.AccountID, record.WorkspaceID = "  acct_1 ", "\tws_1\n"
	if err := authopenai.SaveAuth(stateRoot, "codex", record); err != nil {
		t.Fatal(err)
	}
	spaced, err := c.PlanResponsesContinuation(context.Background(), req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if spaced.AuthScopeIdentity != plan.AuthScopeIdentity || spaced.StorageScopeFingerprint != plan.StorageScopeFingerprint {
		t.Fatalf("stray space must not move the scope: %+v vs %+v", spaced, plan)
	}
}

// TestPlanContinuationNormalizesTheScopeEndpoint pins that the storage scope
// records the transport the way it hashes it: the base URL without its
// trailing slash, the endpoint trimmed.
func TestPlanContinuationNormalizesTheScopeEndpoint(t *testing.T) {
	srv, _ := responsesServer(t)
	r := fixtureRegistry(t, srv.URL, map[string]registry.Provider{
		"padded": {Base: "openai", APIKey: "k", Transport: registry.Transport{BaseURL: srv.URL + "/", Endpoint: " /responses "}},
	})
	req := userRequest("padded", "gpt-5.5")
	req.Store = new(true)
	plan, err := continuationClient(t, r).PlanResponsesContinuation(context.Background(), req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.StorageScope.BaseURL != srv.URL || plan.StorageScope.Path != "/responses" {
		t.Fatalf("the scope records a normalized transport: %q %q", plan.StorageScope.BaseURL, plan.StorageScope.Path)
	}
}
