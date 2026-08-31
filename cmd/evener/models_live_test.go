package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/registry"
)

// liveSmokeModels names cheap models to exercise per instance. An instance
// with a credential that is not listed here uses the first ids its models
// endpoint returns, so naming one model is also what keeps a row to a single
// request instead of the four an auto-picked listing costs.
//
// The `anthropic` row deliberately stops at the cheap model: the
// claude-sonnet-4-5[1m] row has its own pin below, which asserts the resolved
// window as well as acceptance, so listing it here too would only buy a second
// request against a row already covered.
var liveSmokeModels = map[string][]string{
	"openrouter":      {"google/gemini-2.5-flash-lite", "anthropic/claude-haiku-4.5", "deepseek/deepseek-v4-flash", "minimax/minimax-m2.7", "openai/gpt-4.1-nano"},
	"orclaude":        {"minimax/minimax-m2.7", "anthropic/claude-haiku-4.5"},
	"kimi-for-coding": {"kimi-for-coding", "k3"},
	"kimi":            {"kimi-for-coding"},
	"anthropic":       {"claude-haiku-4-5"},
	"openai":          {"gpt-4.1-nano"},
	"groq":            {"openai/gpt-oss-120b"},
	"google":          {"gemini-2.5-flash-lite"},
	"deepseek":        {"deepseek-v4-flash"},
	"xai":             {"grok-4.3"},
	"mistral":         {"ministral-3b-latest"},
	"cerebras":        {"gpt-oss-120b"},
	"togetherai":      {"openai/gpt-oss-20b"},
	"moonshotai":      {"kimi-k2.5"},
	"zai":             {"glm-4.7-flash"},
	"minimax":         {"MiniMax-M2.7"},
}

// liveSmokeEndpoints pins the endpoint an instance's completions must be
// dispatched to, for the rows where the endpoint is itself the property under
// test (spec §13). `openai` is one: its surface selects the Responses API, so
// a row that quietly fell back to Chat Completions would still answer "pong"
// and hide the regression.
var liveSmokeEndpoints = map[string]string{"openai": "/responses"}

const liveSmokePrompt = "Reply with the single word: pong"

// liveConfig names the providers.toml the smoke loads (its credentials.toml
// sibling supplies the keys). A flag rather than EVENER_PROVIDERS_CONFIG
// because TestMain scrubs every EVENER_* variable and redirects HOME. It is
// also the whole gate on the live tests running at all, so it stays a flag
// nothing can supply by accident.
var liveConfig = flag.String("live-config", "", "providers.toml for the live tests (credentials.toml is its sibling)")

// liveCredentialsConfig is EVENER_CREDENTIALS_CONFIG as this process was
// started. TestMain clears every EVENER_* variable before the first test runs,
// so the value has to be captured at package initialization — which Go
// performs before it calls TestMain. scripts/live/with-live-env --store sets
// the variable to the developer's real store while --config copies the
// providers.toml into the run's fake home; without this capture those two
// flags could not be used together.
var liveCredentialsConfig = envvars.EVENERCredentialsConfig.Trimmed()

// liveStorePath is credentials.toml's location for a live run:
// EVENER_CREDENTIALS_CONFIG when one was set, else the sibling of the
// -live-config providers.toml. Same rule as cmdutil.CredentialsPath, which
// this package cannot call because TestMain has already cleared the variable.
func liveStorePath() string {
	if liveCredentialsConfig != "" {
		return liveCredentialsConfig
	}
	return filepath.Join(filepath.Dir(*liveConfig), "credentials.toml")
}

// requireLiveGate skips unless both halves of the gate are present: the
// EVENER_LIVE_TESTS environment gate and the -live-config flag. what names
// the test in the skip message.
func requireLiveGate(t *testing.T, what string) {
	t.Helper()
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skipf("set EVENER_LIVE_TESTS=1 to run %s", what)
	}
	if *liveConfig == "" {
		t.Skipf("pass -args -live-config=<providers.toml> to run %s", what)
	}
}

// loadLiveRegistry loads the registry a live test resolves against: the user
// layer -live-config names, the credential store liveStorePath names, and a
// throwaway state root. Offline, so no live run can start a catalog refresh.
// The log line carries paths and warnings, never a credential value.
func loadLiveRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	store, err := credentials.LoadStore(liveStorePath())
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	r, err := registry.Load(registry.WithConfigPath(*liveConfig), registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}),
		registry.WithStateRoot(t.TempDir()), registry.WithOffline(true))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("%s; credential store: %s; warnings=%v", r.UserLayerNote(), store.Path(), r.Warnings())
	return r
}

// TestLiveSmoke sends one minimal request per (instance, model) built from
// the registry's Resolved record — the resolved base URL, endpoint, auth
// scheme, headers, wire id, and body constants — against every instance the
// config has a credential for. It proves the registry's transport assembly
// against real endpoints before plan 2's builders exist. Run with:
//
//	EVENER_LIVE_TESTS=1 go test ./cmd/evener/ -run TestLiveSmoke -v -count=1 -args -live-config=/path/to/providers.toml
//
// It never prints a credential value: only URLs, header names, statuses,
// and short response excerpts.
func TestLiveSmoke(t *testing.T) {
	requireLiveGate(t, "the live provider smoke")
	r := loadLiveRegistry(t)
	client := &http.Client{Timeout: 90 * time.Second}
	ran := 0
	for _, inst := range r.Instances() {
		if inst.CredentialSource == "none" {
			t.Logf("skip %s: no credential", inst.Name)
			continue
		}
		switch inst.Auth {
		case registry.AuthBearer, registry.AuthOptionalBearer, registry.AuthHeader:
		default:
			t.Logf("skip %s: auth %s needs plan 2's authenticator", inst.Name, inst.Auth)
			continue
		}
		ran++
		t.Run(inst.Name, func(t *testing.T) { liveSmokeInstance(t, r, client, inst) })
	}
	if ran == 0 {
		t.Skip("no instance with a usable credential in this environment")
	}
}

func liveSmokeInstance(t *testing.T, r *registry.Registry, client *http.Client, inst registry.Instance) {
	t.Helper()
	models := liveSmokeModels[inst.Name]
	auto := false
	probeRef := inst.Name + "/probe"
	if len(models) > 0 {
		probeRef = inst.Name + "/" + models[0]
	}
	probe, err := r.Resolve(probeRef)
	if err != nil {
		t.Fatalf("resolve %s: %v", probeRef, err)
	}
	t.Logf("%s: protocol=%s auth=%s base=%s endpoint=%s models=%s credential=%s", inst.Name,
		probe.Protocol, probe.Transport.Auth, probe.Transport.BaseURL, probe.Transport.Endpoint, probe.Transport.ModelsEndpoint, probe.Credential.Source)

	// 1. The models endpoint, when the transport has one.
	if probe.Transport.ModelsEndpoint != registry.EndpointUnsupported {
		status, body := liveRequest(t, client, http.MethodGet, probe.Transport.BaseURL+probe.Transport.ModelsEndpoint, nil, probe)
		ids := liveModelIDs(body)
		if status/100 != 2 {
			t.Errorf("%s: GET %s → %d: %s", inst.Name, probe.Transport.ModelsEndpoint, status, excerpt(body, 200))
		} else {
			t.Logf("%s: GET %s → %d, %d ids", inst.Name, probe.Transport.ModelsEndpoint, status, len(ids))
			live := make([]registry.Model, 0, len(ids))
			for _, id := range ids {
				live = append(live, registry.Model{ID: id})
			}
			r.ApplyLive(inst.Name, live)
			if len(models) == 0 && len(ids) > 0 {
				// A listing says nothing about modality (a gateway may list
				// image models first), so auto-picked ids are tried in turn
				// and the instance passes when any of them answers.
				models = ids[:min(4, len(ids))]
				auto = true
			}
			// A listed id resolves through the live layer or a catalog row.
			if len(ids) > 0 {
				res, err := r.Resolve(inst.Name + "/" + ids[0])
				if err != nil {
					t.Errorf("%s: live id %q does not resolve: %v", inst.Name, ids[0], err)
				} else if m := res.Provenance["model"]; m != "live" && !strings.HasPrefix(m, "row:") && !strings.HasPrefix(m, "region:") && !strings.HasPrefix(m, "dated:") {
					t.Errorf("%s: live id %q resolved as %q, want live or a catalog row", inst.Name, ids[0], m)
				}
			}
		}
	}
	if len(models) == 0 {
		t.Logf("%s: no model to probe (no table entry and no listing)", inst.Name)
		return
	}

	// 2. One minimal completion per model, built from Resolved.
	answered := 0
	for _, model := range models {
		ref := inst.Name + "/" + model
		res, err := r.Resolve(ref)
		if err != nil {
			t.Errorf("resolve %s: %v", ref, err)
			continue
		}
		// Asserted here rather than off the probe above so it pins the
		// endpoint of the request actually sent below.
		if want, pinned := liveSmokeEndpoints[inst.Name]; pinned && res.Transport.Endpoint != want {
			t.Errorf("%s: dispatches to %q, want %q", ref, res.Transport.Endpoint, want)
		}
		url, body, ok := liveBody(res)
		if !ok {
			t.Logf("%s: protocol %s not covered by the smoke", ref, res.Protocol)
			continue
		}
		status, resp := liveRequest(t, client, http.MethodPost, url, body, res)
		if status/100 != 2 {
			if auto {
				t.Logf("%s: POST %s (wire %s) → %d: %s", ref, res.Transport.Endpoint, res.WireID, status, excerpt(resp, 200))
			} else {
				t.Errorf("%s: POST %s (wire %s) → %d: %s", ref, res.Transport.Endpoint, res.WireID, status, excerpt(resp, 300))
			}
			continue
		}
		answered++
		t.Logf("%s: POST %s (wire %s) → %d, text=%q warnings=%v", ref, res.Transport.Endpoint, res.WireID, status, excerpt(liveText(res.Protocol, resp), 60), res.Warnings)
	}
	if auto && answered == 0 {
		t.Errorf("%s: none of the auto-picked models answered", inst.Name)
	}
}

// oneMegaContextRef is the [1m] row the live pin below exercises.
const oneMegaContextRef = "anthropic/claude-sonnet-4-5[1m]"

// TestLiveOneMegaContextRowAccepted pins both halves of what the
// anthropic/claude-sonnet-4-5[1m] row promises: it resolves to the 1M context
// window, and a real /messages request assembled from that row is accepted.
// The request carries exactly the headers the row resolves to, so this test is
// what tells us the day the row's header set stops being the one Anthropic
// wants — whether that set is a beta opt-in or empty. Gated like TestLiveSmoke.
// Run with:
//
//	EVENER_LIVE_TESTS=1 go test ./cmd/evener/ -run TestLiveOneMegaContextRowAccepted -v -count=1 -args -live-config=/path/to/providers.toml
//
// The resolution and window assertions run before the credential check on
// purpose: they need no network and no credential, so the offline half of this
// pin is exercisable by running it with the gate set in an environment that has
// no Anthropic key. It never prints a credential value — only header names, the
// status, and the reply's length.
func TestLiveOneMegaContextRowAccepted(t *testing.T) {
	requireLiveGate(t, "the live [1m] pin")
	r := loadLiveRegistry(t)
	res, err := r.Resolve(oneMegaContextRef)
	if err != nil {
		t.Fatalf("resolve %s: %v", oneMegaContextRef, err)
	}
	if res.Caps.ContextWindow == nil {
		t.Fatalf("%s: no resolved context window, want at least 1000000", oneMegaContextRef)
	}
	if *res.Caps.ContextWindow < 1_000_000 {
		t.Fatalf("%s: context window %d, want at least 1000000", oneMegaContextRef, *res.Caps.ContextWindow)
	}
	t.Logf("%s: wire=%s window=%d headers=%v provenance=%s", oneMegaContextRef, res.WireID,
		*res.Caps.ContextWindow, sortedNames(res.Headers), res.Provenance["model"])
	if res.Credential.Value == "" {
		t.Skipf("%s: no credential in this environment (source %s)", oneMegaContextRef, res.Credential.Source)
	}
	url, body, ok := liveBody(res)
	if !ok {
		t.Fatalf("%s: protocol %s builds no request body", oneMegaContextRef, res.Protocol)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	status, resp := liveRequest(t, client, http.MethodPost, url, body, res)
	if status != http.StatusOK {
		t.Fatalf("%s: POST %s (wire %s) → %d: %s", oneMegaContextRef, res.Transport.Endpoint, res.WireID, status, excerpt(resp, 300))
	}
	text := strings.TrimSpace(string(liveText(res.Protocol, resp)))
	if text == "" {
		t.Fatalf("%s: POST %s → %d with no reply text: %s", oneMegaContextRef, res.Transport.Endpoint, status, excerpt(resp, 300))
	}
	t.Logf("%s: POST %s (wire %s) → %d, %d chars of reply text", oneMegaContextRef, res.Transport.Endpoint, res.WireID, status, len(text))
}

// kimiK3Ref is the row whose thinking shape spec §13 pins. The curated
// kimi-for-coding rows give k3* thinking_shape = "budget+effort", so a request
// carrying an effort must send both halves of the Anthropic thinking body —
// the budget object and output_config.effort — and the reply must come back
// with thinking content. The instance half is the name the coding endpoint has
// in the live smoke's providers.toml (`base = "kimi-for-coding"`); a config
// that names no such instance skips.
const kimiK3Ref = "kimi/k3"

// kimiK3Effort is the effort the pin sets: the cheapest level that still turns
// thinking on, so the property is proven with the smallest budget
// llm.ReasoningBudget hands out.
const kimiK3Effort = "low"

// liveThinkingPrompt wants one number back but a step of arithmetic to get
// there, so a thinking-enabled row has something to think about while still
// answering in a handful of tokens.
const liveThinkingPrompt = "What is 17 times 23? Reply with the number only."

// TestLiveKimiK3ThinkingShape pins Kimi K3's thinking shape end to end (spec
// §13): the registry resolves budget+effort for the row, the anthropic
// protocol turns that into the budget object plus output_config.effort, the
// wire body that leaves the process still carries both, and the endpoint
// answers with thinking content. It builds and sends through the shipping
// protocol, not a hand-rolled body, so it is the real request shape that is
// under test. Gated like TestLiveSmoke. Run with:
//
//	EVENER_LIVE_TESTS=1 go test ./cmd/evener/ -run TestLiveKimiK3ThinkingShape -v -count=1 -args -live-config=/path/to/providers.toml
//
// Everything down to the credential check is offline, so the shape half is
// exercisable with no key present and costs no request. It logs shape field
// names and lengths only: never the prompt, the thinking text, or a header
// value.
func TestLiveKimiK3ThinkingShape(t *testing.T) {
	requireLiveGate(t, "the Kimi K3 thinking-shape pin")
	r := loadLiveRegistry(t)
	instance, _, _ := strings.Cut(kimiK3Ref, "/")
	if _, ok := r.Instance(instance); !ok {
		t.Skipf("%s: this config has no %q instance", kimiK3Ref, instance)
	}
	res, err := r.Resolve(kimiK3Ref)
	if err != nil {
		t.Fatalf("resolve %s: %v", kimiK3Ref, err)
	}
	if got := registry.StringValue(res.Caps.ThinkingShape); got != "budget+effort" {
		t.Fatalf("%s: resolved thinking_shape %q, want %q", kimiK3Ref, got, "budget+effort")
	}
	effort := kimiK3Effort
	maxTokens := 64
	req := llm.Request{
		Model:           kimiK3Ref,
		Messages:        []llm.Message{llm.User(liveThinkingPrompt)},
		MaxTokens:       &maxTokens,
		ReasoningEffort: &effort,
	}
	body, err := anthropic.DefaultProtocol.BuildBody(req, res)
	if err != nil {
		t.Fatalf("%s: build body: %v", kimiK3Ref, err)
	}
	budget := assertThinkingShape(t, kimiK3Ref+" built body", body)
	t.Logf("%s: protocol=%s wire=%s shape=%s effort=%q budget_tokens=%d headers=%v provenance=%s", kimiK3Ref,
		res.Protocol, res.WireID, registry.StringValue(res.Caps.ThinkingShape), effort, budget, sortedNames(res.Headers), res.Provenance["model"])
	if res.Credential.Value == "" {
		t.Skipf("%s: no credential in this environment (source %s)", kimiK3Ref, res.Credential.Source)
	}

	// One request, through the shipping protocol so the prune, the body
	// constants, and the authenticator all run exactly as they do in
	// production. The recorder captures what actually went on the wire.
	rec := &liveWireRecorder{next: http.DefaultTransport}
	protocol := &anthropic.Protocol{Client: &http.Client{Timeout: 90 * time.Second, Transport: rec}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := protocol.Complete(ctx, req, res)
	if err != nil {
		t.Fatalf("%s: POST %s (wire %s): %v", kimiK3Ref, res.Transport.Endpoint, res.WireID, err)
	}
	var wire map[string]any
	if err := json.Unmarshal(rec.body, &wire); err != nil {
		t.Fatalf("%s: the recorded request body is not JSON: %v", kimiK3Ref, err)
	}
	assertThinkingShape(t, kimiK3Ref+" wire body", wire)

	parts, chars := 0, 0
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			parts++
			chars += len(p.Thinking.Text)
		}
	}
	if parts == 0 || chars == 0 {
		t.Fatalf("%s: reply carried no thinking content: %d content part(s), finish=%s", kimiK3Ref, len(resp.Message.Content), resp.Finish.Reason)
	}
	t.Logf("%s: POST %s (wire %s) → %d thinking part(s), %d chars of thinking, %d chars of reply text, finish=%s",
		kimiK3Ref, res.Transport.Endpoint, res.WireID, parts, chars, len(strings.TrimSpace(resp.Text())), resp.Finish.Reason)
}

// assertThinkingShape checks that body carries the whole budget+effort
// Anthropic thinking shape and returns the budget it committed to. what names
// which body is under test (the builder's or the wire's). It logs field names
// and the numeric budget, never a field carrying prompt or reply text.
func assertThinkingShape(t *testing.T, what string, body map[string]any) int {
	t.Helper()
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no thinking object; fields=%v", what, sortedNames(body))
	}
	if typ, _ := thinking["type"].(string); typ != "enabled" {
		t.Fatalf("%s: thinking.type = %q, want %q", what, typ, "enabled")
	}
	budget := 0
	switch v := thinking["budget_tokens"].(type) {
	case int:
		budget = v
	case float64: // the recorded wire body round-trips through JSON
		budget = int(v)
	}
	if budget <= 0 {
		t.Fatalf("%s: thinking.budget_tokens = %v, want a positive budget", what, thinking["budget_tokens"])
	}
	output, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no output_config object; fields=%v", what, sortedNames(body))
	}
	if got, _ := output["effort"].(string); got != kimiK3Effort {
		t.Fatalf("%s: output_config.effort = %q, want %q", what, got, kimiK3Effort)
	}
	t.Logf("%s: thinking fields=%v output_config fields=%v budget_tokens=%d", what, sortedNames(thinking), sortedNames(output), budget)
	return budget
}

// bedrockInstance is the implicit instance the amazon-bedrock rows activate
// as: a curated implicit provider takes its own id as its instance name, and
// the provider's credential (auth = header) has to resolve before the instance
// exists at all, so the pin below both needs and asserts a bearer.
const bedrockInstance = "amazon-bedrock"

// bedrockGlobalWire and bedrockRegionalWire spell the same Sonnet 5 row two
// ways: through the `global.` cross-region inference profile and through the
// in-region id. Naming one base id leaves the routing prefix as the only
// difference between the two legs below. Sonnet 5 is the cheapest catalog row
// that carries a `global.` prefix and is also served on the Mantle Anthropic
// endpoint (input 2 / output 10).
const (
	bedrockGlobalWire   = "global.anthropic.claude-sonnet-5"
	bedrockRegionalWire = "anthropic.claude-sonnet-5"
)

// TestLiveBedrockGlobalRouting pins how the amazon-bedrock rows reach Bedrock
// (spec §9.3, §15): the base URL is the regional Mantle host built from
// AWS_REGION, the credential travels as a bearer in x-api-key with no request
// signing anywhere in the path, and the model id reaches the wire verbatim —
// prefix included, since the routing prefix is the whole of what would select
// a cross-region profile. A normalizer that trimmed the prefix would leave a
// request that still answered 200 from the in-region id, so nothing short of a
// verbatim wire id catches that.
//
// Only the in-region leg sends a request, because on 2026-08-31 the Mantle
// Anthropic endpoint does not serve the prefixed ids at all. Its own catalog
// (GET https://bedrock-mantle.{region}.api.aws/v1/models, 200) lists six
// unprefixed Claude ids — anthropic.claude-{fable-5,haiku-4-5,opus-4-7,
// opus-4-8,opus-5,sonnet-5} — and asking /anthropic/v1/messages for
// global.anthropic.claude-sonnet-5 answers 404 not_found_error, "The model
// 'global.anthropic.claude-sonnet-5' does not exist". That is not an
// entitlement problem and not stale registry data: `aws bedrock
// list-inference-profiles` shows global.anthropic.claude-sonnet-5 ACTIVE and
// SYSTEM_DEFINED in the same account and region, and
// get-foundation-model-availability reports it AUTHORIZED and AVAILABLE. The
// two namespaces are simply different — inference-profile ids address
// bedrock-runtime's InvokeModel/Converse path, which spec §1 puts out of
// scope, while bedrock-mantle serves the flat catalog above. Spec §9.3 now
// records that, and the §6.1 converter marks the region-prefixed rows Hidden,
// so the global leg is pinned offline only: it still resolves, keeps its
// prefix verbatim, and sending it would only burn a request on a 404 every
// run.
//
// Gated like TestLiveSmoke, plus the two AWS variables the row is built from,
// without which no request can be assembled at all. Run with:
//
//	EVENER_LIVE_TESTS=1 go test ./cmd/evener/ -run TestLiveBedrockGlobalRouting -v -count=1 -args -live-config=/path/to/providers.toml
//
// It never prints a credential value: only the base URL, the wire id, header
// names, statuses, and the reply's length.
func TestLiveBedrockGlobalRouting(t *testing.T) {
	requireLiveGate(t, "the Bedrock global-routing pin")
	region := os.Getenv(envvars.AWSRegion.Name)
	if os.Getenv(envvars.AWSBearerTokenBedrock.Name) == "" || region == "" {
		t.Skipf("set %s and %s to run the Bedrock global-routing pin",
			envvars.AWSBearerTokenBedrock.Name, envvars.AWSRegion.Name)
	}
	r := loadLiveRegistry(t)
	wantBase := "https://bedrock-mantle." + region + ".api.aws/anthropic/v1"
	assertBedrockRouting(t, r, bedrockGlobalWire, wantBase)
	res := assertBedrockRouting(t, r, bedrockRegionalWire, wantBase)

	url, body, ok := liveBody(res)
	if !ok {
		t.Fatalf("%s: protocol %s builds no request body", res.ModelID, res.Protocol)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	status, resp := liveRequest(t, client, http.MethodPost, url, body, res)
	if status != http.StatusOK {
		t.Fatalf("%s: POST %s (wire %s) → %d: %s", res.ModelID, res.Transport.Endpoint, res.WireID, status, excerpt(resp, 300))
	}
	text := strings.TrimSpace(string(liveText(res.Protocol, resp)))
	if text == "" {
		t.Fatalf("%s: POST %s → %d with no reply text: %s", res.ModelID, res.Transport.Endpoint, status, excerpt(resp, 300))
	}
	t.Logf("%s: POST %s (wire %s) → %d, %d chars of reply text", res.ModelID, res.Transport.Endpoint, res.WireID, status, len(text))
}

// assertBedrockRouting checks everything the amazon-bedrock row promises about
// reaching the endpoint for the model id wire, which is both the model half of
// the reference it resolves and the id the request must carry unchanged. All
// of it is offline, so it costs no request on either leg.
func assertBedrockRouting(t *testing.T, r *registry.Registry, wire, wantBase string) registry.Resolved {
	t.Helper()
	ref := bedrockInstance + "/" + wire
	res, err := r.Resolve(ref)
	if err != nil {
		t.Fatalf("resolve %s: %v", ref, err)
	}
	if res.Synthesized {
		t.Fatalf("%s: the catalog lists no such row, so the reference was synthesized", ref)
	}
	if res.Transport.BaseURL != wantBase {
		t.Fatalf("%s: base URL %q, want %q", ref, res.Transport.BaseURL, wantBase)
	}
	if res.WireID != wire {
		t.Fatalf("%s: wire id %q, want %q verbatim — the routing prefix must survive resolution", ref, res.WireID, wire)
	}
	if res.Transport.Auth != registry.AuthHeader || res.Transport.AuthHeader != "x-api-key" {
		t.Fatalf("%s: auth %q in header %q, want %q in %q (spec §15: bearer only, never SigV4)",
			ref, res.Transport.Auth, res.Transport.AuthHeader, registry.AuthHeader, "x-api-key")
	}
	if res.Credential.Value == "" {
		t.Fatalf("%s: no credential resolved (source %s) though %s is set", ref, res.Credential.Source, envvars.AWSBearerTokenBedrock.Name)
	}
	t.Logf("%s: base=%s wire=%s protocol=%s auth=%s/%s headers=%v provenance=%s", ref, res.Transport.BaseURL,
		res.WireID, res.Protocol, res.Transport.Auth, res.Transport.AuthHeader, sortedNames(res.Headers), res.Provenance["model"])
	return res
}

// liveWireRecorder keeps the request body of the call it forwards so a
// property test can assert what actually went on the wire rather than what the
// builder returned. The bytes it holds carry the prompt and the request it
// forwards carries the credential, so nothing here is ever logged; only the
// recorded body's field names leave this file.
type liveWireRecorder struct {
	next http.RoundTripper
	body []byte
}

func (rec *liveWireRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.GetBody != nil {
		if rc, err := req.GetBody(); err == nil {
			raw, err := io.ReadAll(rc)
			_ = rc.Close()
			if err == nil {
				rec.body = raw
			}
		}
	}
	return rec.next.RoundTrip(req)
}

// sortedNames lists a map's keys, sorted. Names only, for every map a live
// test logs: a resolved header map can carry a credential-bearing value and a
// request body carries the prompt, so no value from either is ever printed.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// liveBody builds the smallest legal request per protocol from Resolved.
func liveBody(res registry.Resolved) (string, map[string]any, bool) {
	body := map[string]any{}
	switch res.Protocol {
	case registry.ProtocolOpenAIChat:
		field := "max_tokens"
		if res.Caps.MaxTokensField != nil && *res.Caps.MaxTokensField != "" {
			field = *res.Caps.MaxTokensField
		}
		body["model"] = res.WireID
		body["messages"] = []map[string]any{{"role": "user", "content": liveSmokePrompt}}
		body[field] = 64
	case registry.ProtocolOpenAIResponses:
		body["model"] = res.WireID
		body["input"] = liveSmokePrompt
		body["max_output_tokens"] = 64
	case registry.ProtocolAnthropic:
		body["model"] = res.WireID
		body["max_tokens"] = 64
		body["messages"] = []map[string]any{{"role": "user", "content": liveSmokePrompt}}
	case registry.ProtocolGoogle:
		body["contents"] = []map[string]any{{"role": "user", "parts": []map[string]any{{"text": liveSmokePrompt}}}}
		body["generationConfig"] = map[string]any{"maxOutputTokens": 64}
	default:
		return "", nil, false
	}
	for path, v := range res.Transport.Body {
		liveSetPath(body, path, v)
	}
	endpoint := strings.ReplaceAll(res.Transport.Endpoint, "{model}", res.WireID)
	if strings.Contains(res.Transport.Endpoint, "{model}") {
		delete(body, "model")
	}
	return res.Transport.BaseURL + endpoint, body, true
}

func liveSetPath(body map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	cur := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// liveRequest performs one request with the resolved auth and headers and
// returns the status and body. It never logs a credential value.
func liveRequest(t *testing.T, client *http.Client, method, url string, body map[string]any, res registry.Resolved) (int, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	switch res.Transport.Auth {
	case registry.AuthBearer, registry.AuthOptionalBearer:
		if res.Credential.Value != "" {
			req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
		}
	case registry.AuthHeader:
		req.Header.Set(res.Transport.AuthHeader, res.Credential.Value)
	}
	for k, v := range res.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range res.CredentialHeaders {
		req.Header.Set(k, v)
	}
	if res.Protocol == registry.ProtocolAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("%s %s: read: %v", method, url, err)
	}
	return resp.StatusCode, raw
}

// liveModelIDs reads a models listing in either shape the protocols here
// answer with: the OpenAI-shaped {"data":[{"id":…}]} (Anthropic's /models
// matches it) and Gemini's {"models":[{"name":"models/…"}]}, whose ids carry
// a "models/" prefix no reference outside that listing uses. Reading only the
// first shape made every Gemini listing look empty, which quietly skipped
// both the live-layer enrichment and the id-resolves check for the row.
func liveModelIDs(body []byte) []string {
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil
	}
	ids := make([]string, 0, len(listing.Data)+len(listing.Models))
	add := func(id string) {
		if id != "" && registry.IsChatModelID(id) {
			ids = append(ids, id)
		}
	}
	for _, m := range listing.Data {
		add(m.ID)
	}
	for _, m := range listing.Models {
		add(strings.TrimPrefix(m.Name, "models/"))
	}
	sort.Strings(ids)
	return ids
}

// liveText extracts the reply text per protocol for the log line.
func liveText(protocol string, body []byte) []byte {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	switch protocol {
	case registry.ProtocolOpenAIChat:
		if choices, ok := doc["choices"].([]any); ok && len(choices) > 0 {
			if msg, ok := choices[0].(map[string]any)["message"].(map[string]any); ok {
				return []byte(fmt.Sprint(msg["content"]))
			}
		}
	case registry.ProtocolOpenAIResponses:
		if out, ok := doc["output"].([]any); ok {
			for _, item := range out {
				if m, ok := item.(map[string]any); ok && m["type"] == "message" {
					if content, ok := m["content"].([]any); ok && len(content) > 0 {
						if c, ok := content[0].(map[string]any); ok {
							return []byte(fmt.Sprint(c["text"]))
						}
					}
				}
			}
		}
	case registry.ProtocolAnthropic:
		if content, ok := doc["content"].([]any); ok {
			for _, item := range content {
				if c, ok := item.(map[string]any); ok && c["type"] == "text" {
					return []byte(fmt.Sprint(c["text"]))
				}
			}
		}
	case registry.ProtocolGoogle:
		if cands, ok := doc["candidates"].([]any); ok && len(cands) > 0 {
			if content, ok := cands[0].(map[string]any)["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
					if p, ok := parts[0].(map[string]any); ok {
						return []byte(fmt.Sprint(p["text"]))
					}
				}
			}
		}
	}
	return body
}

func excerpt(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
