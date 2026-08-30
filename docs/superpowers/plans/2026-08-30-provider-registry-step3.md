# Provider Registry Step 3 (Cut-over) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route every LLM call, agent profile, model listing, credential decision, hub pane, and CLI command through `llm/registry` and the step-2 protocol packages; delete the adapter stack, `providercfg`, the LiteLLM catalog, and the `envvars` provider roster; ship the §14.1 flag day (spec §14 step 3).

**Architecture:** Three seams change, in dependency order. (1) `llm.Client` resolves `req.Provider/req.Model` through the registry, shapes the request once (`ShapeRequest`), and dispatches to the protocol registered for `res.Protocol`; adapters registered with `Register` form an override map consulted by instance name first, which is how the ~650 scripted test doubles keep working. Continuation planning moves from the old OpenAI adapter into the client (`Resolved` + `BuildBody`). (2) `agent/provider.Profile` becomes a thin wrapper over `registry.Resolved` plus tool definitions, doc files, and the per-session overrides; every behavior-tag and catalog branch in the agent moves to exactly one of `Surface`, `Protocol`, `ProviderID`, `Instance`, or a `Caps` field (spec §7.5). (3) The hub, the CLI, the credentials store, and the spawn path read instances from the registry and write `providers.toml` through a new registry config writer. The old adapters, `providercfg`, the catalog, and the roster are deleted last, when nothing imports them.

**Tech Stack:** Go 1.27 `go.work` workspace (modules `.`, `llm`, `envvars`, `auth`, `identifier`, `invariant`, `fuzz`, `agent`); `primeradiant.com/evener/llm/registry`; the step-2 packages `llm/providers/{chatcompletions,responses,anthropic,google,tokenauth}` and `llm/providers/internal/protocolhttp`; `github.com/BurntSushi/toml`; `net/http/httptest`; the React/TypeScript hub frontend under `cmd/evener-hub/frontend` (`make test-web`); the appwire generators (`make generate` → `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/types.gen.ts`).

**Spec:** `docs/superpowers/specs/2026-08-28-provider-registry-design.md` (revision 12). Sections this plan implements: §5.1–§5.2 (instances, resolving without a credential), §7.3–§7.6 (unknown models, derived caps, what the agent reads, continuation), §8.1 (the client and the override map), §9.5 (Codex CLI defaults, stray records), §10 (`providers.toml` rules, tri-state `EVENER_PROVIDERS_CONFIG`, `EVENER_CREDENTIALS_CONFIG`, credential order), §11.2–§11.3 (`evener providers`, hub and appwire), §12 (error identity: `Provider()`/`Protocol()`, no `BehaviorTag()`), §13 rows "Flag day", "Client dispatch", "Continuation", §14 step 3, §14.1. Plans 1 and 2 (`docs/superpowers/plans/2026-08-29-provider-registry-step{1,2}.md`) landed `llm/registry`, `llm.ShapeRequest`, the four protocols, the authenticators, the classifier, and the wire goldens; this plan wires them in and removes what they replace.

## Global Constraints

- **Flag day, no compatibility code** (spec §14.1: "There is no migration code … None of this is detected or translated at runtime, and none of the old files are renamed or deleted"). An old-schema `providers.toml` fails to load with `registry.ErrOldSchema`'s pointer: the CLI exits with it; the hub starts with implicit instances only, shows it as a diagnostic, spawns children with `EVENER_PROVIDERS_CONFIG=` (empty) and `EVENER_CREDENTIALS_CONFIG` set, and refuses every instance write with the same pointer (§10). Nothing reads `type`/`api_style`/`quirks`/`compat`, `auth/openai.json` for the `openai` instance, `OPENAI_CHATGPT_BASE_URL`, `GEMINI_BASE_URL`, `KIMI_*`, `GLM_*`, or `OPENAI_COMPATIBLE_PROVIDER_QUIRKS`.
- **Tree green at the end of every task**, gated per workspace module (the root `go test ./...` covers only the root module): `for m in . llm envvars auth identifier invariant fuzz agent; do (cd "$m" && go test ./... ) || exit 1; done` (`agent/sandbox` bwrap tests fail in the execution environment before this plan — verify with `git stash` when in doubt). Two pieces of scaffolding are permitted and named: the `Surface()/Protocol()/ProviderID()` derivation on the old `Profile` (Task 4) and `WithResolved` on the old `Profile` (Task 5); Task 7 deletes both with the old profile.
- **One dispatch path** (§8.1): `Client` resolves the instance/model → `ShapeRequest` → protocol by `res.Protocol`. The override map is consulted by instance name **first**: "when an override exists and the name resolves, `ShapeRequest` runs and the override sees the shaped request; when the name does not resolve (`fake`, `other`, `off`, `broken`, and the other test doubles), the request passes through untouched and no error is raised; either way there is no body prune, and the override's `ResponsesContinuationPlanner` is honored".
- **Five keys, no tags** (§7.5): every branch that keys on `BehaviorTag()` or the LiteLLM catalog moves to exactly one of `Surface`, `Protocol`, `ProviderID`, `Instance`, or a `Caps` field per the §7.5 table; `BehaviorTag()` is deleted from `Profile` and from the `llm.Error` interface; `Provider()` returns the instance name and `llm.ErrorProtocol(err)` the protocol id (§12).
- **No injected effort** (§7.4, §7.5): the `ThinkingAlwaysOn → medium` injection is deleted; `ShapeRequest` never adds an effort; `Caps.ThinkingAlwaysOn` is a builder concern only.
- **Continuation** (§7.6): available iff `Protocol == openai-responses` and `Fields["previous_response_id"]` and `Fields["store"]` are both true after layering; the request fingerprint excludes `input`, `previous_response_id`, `conversation`, `stream`, and (public family) `store`, prefix `cont-req-v2`; the storage scope hashes the instance name, resolved base URL, endpoint path, the HMAC of `Credential.Value`/`Source`, the `OpenAI-Organization`/`OpenAI-Project` header values when present, the conversation id when set, and the OAuth account/workspace claims on the Codex transport; `EndpointFamily` is `openai_codex` iff `Transport.Auth == oauth-openai-codex`; `CanFallbackToChat`, `FullHistoryFallbackMessages`, `HistoryModeChatFallback`, `llm.ErrorClassFallback`, and `isEndpointFallbackSignal` are deleted with the Responses→Chat fallback.
- **Credentials** (§10): resolution order is the registry's (`api_key` → `credential_headers.Authorization` → store entry under the instance name → `APIKeyEnv` → `<NAME>_API_KEY` for non-registry names); the store is looked up by instance name only and no longer owns a provider roster; a resolved credential never lands on disk (`registry.WriteConfigFile` writes only what the caller put in the `Layer`); secrets never appear on a command line.
- **`EVENER_PROVIDERS_CONFIG` tri-state at every reader** (§10, §14.1): unset → default path; present and empty → "no user layer"; set → that path. `EVENER_CREDENTIALS_CONFIG` is a new public `envvars.Var` naming the store (unset → sibling of the providers path, else `<config-root>/credentials.toml`). Test mains derive their scrub lists from `envvars.All()`, so both are scrubbed automatically.
- **Unsupported is not failure** (§8.1): callers that list models or count tokens treat `llm.ErrModelListingUnsupported` / `llm.ErrInputTokenCountUnsupported` as "registry-only listing" / "estimate-only counting"; live-membership checks apply only when a live listing exists.
- **Tests are offline and hermetic**: registries in tests are built with `registry.Load(registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(), registry.WithStateRoot(t.TempDir()), registry.WithEnv(<fixture lookup>), registry.WithInstances(...))`; wire tests use `httptest`; nothing tests a mock's behavior; no test constructs real credentials; test output is pristine.
- **Repo conventions** (as in plans 1–2): `new(expr)` for optional scalars (golangci `modernize`); `defer func() { _ = x.Close() }()` (errcheck); doc comments on every exported identifier (revive); snake_case JSON tags (tagliatelle); never change whitespace that does not affect execution; gofmt every touched file with the toolchain gofmt and run `make lint` with `PATH="$(go env GOROOT)/bin:$PATH"` (the system gofmt is Go 1.22) before every commit; every new `Fuzz*` gets a row in `scripts/fuzz/fuzz-targets.txt` and every deleted one loses its row (`make fuzz-registry-check`); `make generate` must leave `git diff` clean (lint-generated); frontend tasks run `make test-web`; TDD per task; conventional-commit messages; never `git add -A`.
- **Plan snippets are not gofmt-authoritative** (plan-2 ruling): an alignment difference from a snippet is never a deviation; implementers gofmt.

## File Structure

| Path | Responsibility (this plan) |
|---|---|
| `llm/registry/types.go`, `instances.go`, `resolve.go` (modify); `llm/registry/write.go` (+ tests, create) | `Resolved.DefaultModel/CheapModel`, `(Caps).ReasoningDisabled`, `Instance.BaseURL/Vars/DefaultModel`, `(*Registry).Instance/StateRoot/ResolveInstance/StrayOAuthRecords`; `MarshalConfig`/`ReadConfigFile`/`WriteConfigFile` |
| `llm/client.go` (rewrite), `llm/client_registry_test.go` (create, `package llm_test`), `llm/registry_shape.go` (modify), `llm/continuation_context.go` (create) | registry dispatch, override map, `Models`, `ContinuationHasher`, `ShapeRequest` continuation store override, hasher-in-context |
| `llm/responses_continuation.go`, `llm/responses_continuation_plan.go` (create), `llm/client_continuation_test.go` (create) | `ResponsesEndpointFamilyFor`, `ResponsesRequestFingerprint` (moved from `responses`, v2), `AuthScopeProvider`, `Client.PlanResponsesContinuation` on `Resolved` |
| `llm/providers/responses/{fingerprint.go,complete.go,protocol.go}` (modify) | fingerprint helpers deleted (moved to `llm`); hasher read from the context |
| `llm/providers/tokenauth/codex.go` (modify) | `AuthScope` (account/workspace claims) |
| `llm/api_attempt.go`, `llm/apilog/record.go`, `llm/providers/internal/protocolhttp/call.go` (modify) | `Protocol` on the attempt record |
| `agent/provider/profile.go` (rewrite), `agent/provider/resolve.go` (rewrite), `agent/provider/embedded.go` (create), `agent/provider/*_test.go` (rewrite) | `Profile` over `Resolved`; `Resolve`, `NewOpenAIProfile`, `EmbeddedRegistry` |
| `agent/session_model_call.go`, `session.go`, `session_set_model.go`, `session_init.go`, `session_tool_registry.go`, `session_tools_web.go`, `session_tools.go`, `session_prompts.go`, `section_resolver.go`, `live_model_metadata.go`, `subagent_model_selection.go`, `responses_continuation_eligibility.go`, `schema/turn.go`, `sandbox/provider_web.go`, `internal/modelavailability/modelavailability.go`, `internal/cheapmodel/caller.go`, `internal/contextmgr/context_manager.go`, `internal/agenttest/agenttest.go`, `profile_testhelpers_test.go`, `testkit_test.go` (modify) | the §7.5 moves, listing on `Client.Models`, continuation fallback deletion, registry-backed test fixtures |
| `cmdutil/load_client.go` (rewrite), `cmdutil/cmdutil.go` (modify), `cmdutil/registry.go` (create), `cmdutil/seed.go`, `cmdutil/materialize.go` (delete) | `LoadRegistry`, `LoadClient`, `ResolveProfile`, `BuildResolveProfile`, `ModelDescriptorFromResolved`, credentials path, `storeCredentialSource`, build-version wiring |
| `envvars/envvars.go`, `envvars/providers.go` (delete), `envvars/ollama_host.go` (delete) | `EVENERCredentialsConfig`; provider vars and roster deleted |
| `internal/credentials/store.go` (modify) | file layer only: `Get`, `Set`, `Clear`, `Names`, `Path` |
| `cmd/evener/providers.go` (+ tests, create), `cmd/evener/models.go`, `cmd/evener/main.go`, `cmd/evener/run.go`, `cmd/evener/serve.go`, `cmd/evener/openai_{login,logout,status}.go`, `cmd/evener/internal/launchcheck/launchcheck.go`, `cmd/llmcall/main.go`, `tools/tool-fluency/cmd/evener-fluency/main.go`, `agent/internal/liveeval/paths.go` (modify) | `evener providers list|probe|add`, old-schema pointer, Codex defaults, loaders, tri-state |
| `cmd/evener-hub/{main.go,registry.go (create),app_instances.go,app_auth.go,app_credentials.go,app_models.go,spawn.go,app_rpc.go}`, `cmd/evener-hub/internal/hubcore/config.go`, `cmd/evener-hub/internal/launchconfig/env.go`, `appwire/types.go`, `appwire/protocol.go` (modify) | hub on the registry, diagnostics, spawn gate, wire types |
| `cmd/evener-hub/frontend/src/{stores/credentials.ts,panes/settings/sections/credentials/*}` (modify) | new instance wire shape, provider picker, diagnostics banner |
| `appwire/cost.go`, `server/appwire_runtime.go`, `internal/appprojector/appwire_projection.go`, `cmd/evener-hub/{app_threadread.go,web_format.go,web_workspace.go}`, `cmd/evener-tui/{hub_commands.go,main.go}` (modify); `llm/pricing.go` (modify) | cost from `registry.Cost`; TUI picker meta from descriptors |
| `llm/providers/{openai,openaicompat,kimi,kimi_anthropic,glm,minimax,ollama,openrouter,openrouter_anthropic,kimicoding,internal/providerfwd}` (delete); `llm/providers/anthropic/{adapter.go,…}`, `llm/providers/google/{adapter.go,…}` (delete the `Adapter` types and their tests); `llm/providercfg` (delete); `llm/model_catalog.go`, `llm/model_catalog_embedded.go`, `llm/data/*` (delete); `llm/env_registry.go`, `llm/providers_config.go` (delete); `llm/classify.go`, `llm/errors.go`, `llm/sdk_errors.go`, `llm/token_count.go` (modify); `llm/providers/all/all.go`, `scripts/fuzz/fuzz-targets.txt`, `cmd/evener-internalcheck/main.go` (modify); `llm/providers/responses/recompute.go`, `llm/providers/chatcompletions/recompute.go` (create), `agent/doctor/apilog.go` (modify) | deletions and the last consumer moves |
| `docs/llm-providers.md`, `docs/llm-provider-config-and-launch.md`, `docs/ollama.md`, `README.md`, `docs/getting-started.md`, `docs/evener-hub.md`, `docs/developing-evener/environment.md` (rewrite/modify) | documentation around §3–§10 |

Left for plan 4 on purpose: live verification of Azure, Bedrock, and Vertex (§14 step 4) and the `EVENER_LIVE_TESTS` smoke rows of §13 for the cloud transports.

---

### Task 1: Registry additions the cut-over needs

**Files:**
- Modify: `llm/registry/types.go` (`Resolved`, `Caps`), `llm/registry/instances.go` (`Instance`, `Instances`, new methods), `llm/registry/resolve.go` (`Resolve` error helper, `resolveOn` return, `ResolveInstance`)
- Create: `llm/registry/write.go`, `llm/registry/write_test.go`, `llm/registry/instance_resolve_test.go`, `llm/registry/stray_records_test.go`

**Interfaces:**
- Consumes: the existing internals `recordFor`, `record`, `capLayer`, `mergeCaps`, `seedFields`, `buildTransport`, `buildHeaders`, `credential`, `expandEnv`, `resolveBaseURL`, `rankedInstances` (all in `llm/registry`).
- Produces (every later task relies on these exact names):
  - `Resolved.DefaultModel string` and `Resolved.CheapModel string` (the instance's curated or user `default_model`/`cheap_model`); `Resolved.Synthesized bool` (true when the model matched no row and no live id, spec §7.3).
  - `func (c Caps) ReasoningDisabled() bool` — `Reasoning != nil && !*Reasoning`.
  - `Instance.BaseURL string` (resolved base URL, "" when hidden), `Instance.Vars map[string]string` (the user layer's `vars`), `Instance.DefaultModel string`.
  - `func (r *Registry) Instance(name string) (Instance, bool)`, `func (r *Registry) StateRoot() string`.
  - `func (r *Registry) ResolveInstance(name string) (Resolved, error)` — a model-less `Resolved` (protocol, transport, headers, credential, provider-level caps; `ModelID`/`WireID` empty) for `ListModels` and credential probes.
  - `func (r *Registry) StrayOAuthRecords() []string` — one notice per `auth/<name>.json` under the state root whose name is not an instance on the Codex transport (spec §9.5, §14.1).
  - `func MarshalConfig(l *Layer) ([]byte, error)`, `func ReadConfigFile(path string) (*Layer, bool, error)`, `func WriteConfigFile(path string, l *Layer) error`, `func ValidInstanceName(name string) bool` (spec §10: lowercase, no slash — the parser's own rule, exported for the hub and the CLI).

- [ ] **Step 1: Write the failing tests for `ResolveInstance`, `Instance`, and `StrayOAuthRecords`**

`llm/registry/instance_resolve_test.go`:

```go
package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cutoverRegistry(t *testing.T, env map[string]string, instances map[string]Provider) *Registry {
	t.Helper()
	r, err := Load(
		WithOffline(true), WithoutCache(), WithNoUserLayer(), WithStateRoot(t.TempDir()),
		WithEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

func TestResolveInstanceCarriesTransportCredentialAndProviderCaps(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"GROQ_API_KEY": "gk"}, nil)
	res, err := r.ResolveInstance("groq")
	if err != nil {
		t.Fatalf("ResolveInstance: %v", err)
	}
	if res.Instance != "groq" || res.ProviderID != "groq" || res.Protocol != ProtocolOpenAIChat {
		t.Fatalf("identity: %+v", res)
	}
	if res.ModelID != "" || res.WireID != "" {
		t.Fatalf("model-less resolve must leave ModelID/WireID empty: %q %q", res.ModelID, res.WireID)
	}
	if res.Transport.BaseURL != "https://api.groq.com/openai/v1" || res.Transport.ModelsEndpoint == "" {
		t.Fatalf("transport: %+v", res.Transport)
	}
	if res.Credential.Value != "gk" || res.Credential.Source != "env:GROQ_API_KEY" {
		t.Fatalf("credential: %+v", res.Credential)
	}
	if res.DefaultModel == "" || res.CheapModel == "" {
		t.Fatalf("groq is on the implicit list and must carry default_model and cheap_model: %+v", res)
	}
}

func TestResolveInstanceUnknownNamesAvailableInstances(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"GROQ_API_KEY": "gk"}, nil)
	_, err := r.ResolveInstance("nope")
	if err == nil || !strings.Contains(err.Error(), `unknown instance "nope"`) || !strings.Contains(err.Error(), "groq") {
		t.Fatalf("want unknown-instance error naming groq, got %v", err)
	}
}

func TestResolveCarriesDefaultAndCheapModel(t *testing.T) {
	r := cutoverRegistry(t, nil, map[string]Provider{"work": {Base: "openai", APIKey: "k", DefaultModel: "gpt-5.5", CheapModel: "gpt-4.1-nano", Transport: Transport{BaseURL: "https://gw.example.com/v1"}}})
	res, err := r.Resolve("work/gpt-5.5")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.DefaultModel != "gpt-5.5" || res.CheapModel != "gpt-4.1-nano" {
		t.Fatalf("default/cheap: %q %q", res.DefaultModel, res.CheapModel)
	}
	if res.Synthesized {
		t.Fatal("gpt-5.5 is a catalog row on the openai base")
	}
	if made, err := r.Resolve("work/never-heard-of-it"); err != nil || !made.Synthesized {
		t.Fatalf("an unknown id resolves as synthesized (spec §7.3): %v %+v", err, made.Synthesized)
	}
}

func TestInstanceViewCarriesBaseURLVarsAndDefaultModel(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "tok"}, map[string]Provider{
		"bedrock": {Base: "amazon-bedrock", Transport: Transport{Vars: map[string]string{"AWS_REGION": "us-east-1"}}},
	})
	inst, ok := r.Instance("bedrock")
	if !ok {
		t.Fatal("bedrock must be an instance")
	}
	if inst.Vars["AWS_REGION"] != "us-east-1" {
		t.Fatalf("vars: %+v", inst.Vars)
	}
	if !strings.Contains(inst.BaseURL, "us-east-1") {
		t.Fatalf("base url must be resolved with the var: %q", inst.BaseURL)
	}
	if inst.DefaultModel == "" {
		t.Fatalf("amazon-bedrock carries a curated default_model: %+v", inst)
	}
	if _, ok := r.Instance("nope"); ok {
		t.Fatal("unknown name must not be an instance")
	}
	if r.StateRoot() == "" {
		t.Fatal("StateRoot must be the load-time state root")
	}
}

func TestReasoningDisabled(t *testing.T) {
	if (Caps{}).ReasoningDisabled() {
		t.Fatal("nil is not disabled")
	}
	if (Caps{Reasoning: new(true)}).ReasoningDisabled() {
		t.Fatal("true is not disabled")
	}
	if !(Caps{Reasoning: new(false)}).ReasoningDisabled() {
		t.Fatal("false is disabled")
	}
}

func TestStrayOAuthRecords(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai.json", "openai-codex.json", "work.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, "auth", name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Load(WithOffline(true), WithoutCache(), WithNoUserLayer(), WithStateRoot(root),
		WithEnv(func(string) (string, bool) { return "", false }),
		WithInstances(map[string]Provider{"work": {Base: "openai", APIKey: "k", Transport: Transport{BaseURL: "https://gw.example.com/v1"}}}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := r.StrayOAuthRecords()
	if len(got) != 2 {
		t.Fatalf("want 2 notices (openai, work), got %d: %v", len(got), got)
	}
	for _, want := range []string{
		`"openai" is not an instance on the Codex transport`, "evener openai logout --instance openai",
		`"work" is not an instance on the Codex transport`, "evener openai logout --instance work",
	} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd llm && go test ./registry/ -run 'TestResolveInstance|TestResolveCarriesDefault|TestInstanceView|TestReasoningDisabled|TestStrayOAuth' 2>&1 | head -20`
Expected: compile errors (`ResolveInstance`, `Instance`, `StateRoot`, `StrayOAuthRecords`, `ReasoningDisabled`, `DefaultModel`, `Synthesized` undefined).

- [ ] **Step 3: Implement the type and instance additions**

`llm/registry/types.go` — add to `Resolved` after `Warnings`:

```go
	// DefaultModel and CheapModel are the instance's curated or configured
	// default_model / cheap_model (spec §6.2, §7.5); the agent's profile reads
	// them instead of a per-provider switch.
	DefaultModel string `json:"default_model,omitempty"`
	CheapModel   string `json:"cheap_model,omitempty"`
	// Synthesized is true when the reference matched no catalog row and no
	// live id (spec §7.3): the row was made up from provider-level caps.
	Synthesized bool `json:"synthesized,omitempty"`
```

and after `StringValue`:

```go
// ReasoningDisabled reports an explicit reasoning = false (spec §7.4): the
// row must send no reasoning control at all. nil means "unknown", which is
// not disabled.
func (c Caps) ReasoningDisabled() bool { return c.Reasoning != nil && !*c.Reasoning }
```

`llm/registry/instances.go` — extend `Instance`:

```go
type Instance struct {
	Name             string            `json:"name"`
	ProviderID       string            `json:"provider_id,omitempty"`
	Base             string            `json:"base,omitempty"`
	Protocol         string            `json:"protocol"`
	Surface          string            `json:"surface,omitempty"`
	Auth             string            `json:"auth"`
	BaseURL          string            `json:"base_url,omitempty"`
	Vars             map[string]string `json:"vars,omitempty"`
	DefaultModel     string            `json:"default_model,omitempty"`
	Implicit         bool              `json:"implicit"`
	Hidden           bool              `json:"hidden,omitempty"`
	Default          bool              `json:"default,omitempty"`
	CredentialSource string            `json:"credential_source"`
	Warnings         []string          `json:"warnings,omitempty"`
}
```

and in `Instances()` fill the three new fields (`base, _, _ := r.resolveBaseURL(inst.rec, h.Transport)` when the head is not hidden; `Vars: maps.Clone(inst.rec.userVars)`; `DefaultModel: h.DefaultModel`), then add:

```go
// Instance returns one instance's listing view.
func (r *Registry) Instance(name string) (Instance, bool) {
	for _, inst := range r.Instances() {
		if inst.Name == name {
			return inst, true
		}
	}
	return Instance{}, false
}

// StateRoot is the state root the registry was loaded with: OAuth records
// and the catalog cache live under it, and the Codex authenticator must
// read the same directory (spec §9.5).
func (r *Registry) StateRoot() string { return r.stateRoot }

// StrayOAuthRecords lists auth/<name>.json records under the state root
// whose <name> is not an instance on the Codex transport (spec §9.5, §14.1).
// Nothing reads such a record, so each notice says how to remove it.
func (r *Registry) StrayOAuthRecords() []string {
	dir := filepath.Join(r.stateRoot, "auth")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		if rec, found := r.recordFor(name); found && rec.head.Transport.Auth == AuthOAuthOpenAICodex {
			continue
		}
		out = append(out, fmt.Sprintf("stray OAuth record %s: %q is not an instance on the Codex transport; remove it with `evener openai logout --instance %s`", filepath.Join(dir, e.Name()), name, name))
	}
	sort.Strings(out)
	return out
}
```

`llm/registry/resolve.go` — extract the unknown-instance error from `Resolve` into a helper used by both:

```go
func (r *Registry) unknownInstance(name string) error {
	names := make([]string, 0, len(r.instances))
	for _, inst := range r.rankedInstances() {
		names = append(names, inst.name)
	}
	return fmt.Errorf("unknown instance %q (available: %s)", name, strings.Join(names, ", "))
}
```

set `DefaultModel: rec.head.DefaultModel, CheapModel: rec.head.CheapModel, Synthesized: hit.synthesized` in `resolveOn`'s returned `Resolved`, and add:

```go
// ResolveInstance resolves an instance without a model: what a model-less
// call (ListModels, a credential probe) needs — protocol, transport,
// headers, credential, and the provider-level caps — with no row
// (spec §8.1: ListModels takes a Resolved). ModelID and WireID stay empty.
func (r *Registry) ResolveInstance(name string) (Resolved, error) {
	rec, ok := r.recordFor(name)
	if !ok {
		return Resolved{}, r.unknownInstance(name)
	}
	caps := Caps{}
	prov := map[string]string{}
	for _, layer := range rec.layers {
		if layer.resetFields {
			caps.Fields = nil
		}
		mergeCaps(&caps, layer.provider, layer.tag+"/provider", prov)
	}
	seedFields(&caps, rec.head.Protocol)
	transport, warnings := r.buildTransport(rec, Model{}, rec.head.Protocol)
	cred, cw := r.credential(rec)
	warnings = append(warnings, cw...)
	credHeaders := map[string]string{}
	for k, v := range rec.head.CredentialHeaders {
		if e, missing := expandEnv(v, r.env); len(missing) == 0 && e != "" {
			credHeaders[k] = e
		}
	}
	providerID := rec.providerID
	if providerID == "" {
		providerID = rec.name
	}
	return Resolved{
		Instance: rec.name, ProviderID: providerID, Protocol: rec.head.Protocol, Surface: rec.head.Surface,
		Transport: transport, Caps: caps, Headers: r.buildHeaders(rec.head.Headers, nil),
		Credential: cred, CredentialHeaders: credHeaders, Provenance: prov, Warnings: warnings,
		DefaultModel: rec.head.DefaultModel, CheapModel: rec.head.CheapModel,
	}, nil
}
```

(The helper names above are the ones `resolveOn` already calls; match their real signatures when they differ — `buildTransport` returns the transport and its warnings, `buildHeaders` takes provider headers and row headers.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd llm && go test ./registry/ 2>&1 | tail -5`
Expected: PASS (the whole package, including the plan-1 goldens: `DefaultModel`/`CheapModel` are `omitempty` and the golden `Resolved` fixtures under `llm/registry/testdata` must be regenerated only if they include instances with a default model — regenerate with the package's `-update` flag when the golden test reports the two new keys, and commit the goldens with the code).

- [ ] **Step 5: Commit**

```bash
git add llm/registry/types.go llm/registry/instances.go llm/registry/resolve.go llm/registry/instance_resolve_test.go llm/registry/testdata
git commit -m "feat(registry): model-less instance resolution, stray OAuth notices, default/cheap models on Resolved"
```

- [ ] **Step 6: Write the failing tests for the config writer**

`llm/registry/write_test.go`:

```go
package registry

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writerFixtureLayer() *Layer {
	return &Layer{
		Tag:     LayerConfig,
		Default: "work",
		TopGlobs: map[string]Model{
			"*gemini-3*": {ID: "*gemini-3*", Caps: Caps{MultimodalToolResults: new(true)}},
		},
		Providers: map[string]Provider{
			"work": {
				ID: "work", Base: "openai", Protocol: ProtocolOpenAIChat, Surface: SurfaceGeneric,
				Headers:           map[string]string{"X-Portkey-Provider": "openai"},
				CredentialHeaders: map[string]string{"Authorization": "Bearer $PORTKEY_KEY"},
				APIKeyEnv:         []string{},
				DefaultModel:      "glm-5.2-nvfp4",
				Transport:         Transport{BaseURL: "https://gw.example.com/v1"},
				Caps:              Caps{Fields: map[string]bool{"stream_options": false}, ContextWindow: new(131072)},
				Models: map[string]Model{
					"glm-5.2-nvfp4": {ID: "glm-5.2-nvfp4", Caps: Caps{
						ContextWindow: new(1048576), MaxOutputTokens: new(131072),
						EffortValues: []string{"high", "max"}, ThinkingFormat: new("zai"),
						Cost: &Cost{Input: 0.5, Output: 1.5, Tiers: []CostTier{{InputTokensAbove: 200000, Input: 1, Output: 3}}},
					}},
				},
			},
			"bedrock": {ID: "bedrock", Base: "amazon-bedrock", Transport: Transport{Vars: map[string]string{"AWS_REGION": "us-east-1"}}},
			"local":   {ID: "local", Base: "openai-compatible", Transport: Transport{BaseURL: "http://localhost:8080/v1", Auth: AuthNone}},
		},
	}
}

func TestMarshalConfigRoundTrips(t *testing.T) {
	want := writerFixtureLayer()
	data, err := MarshalConfig(want)
	if err != nil {
		t.Fatalf("MarshalConfig: %v", err)
	}
	got, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("ParseConfig of marshaled output: %v\n%s", err, data)
	}
	if got.Default != want.Default {
		t.Fatalf("default: %q vs %q", got.Default, want.Default)
	}
	if !reflect.DeepEqual(got.TopGlobs, want.TopGlobs) {
		t.Fatalf("top globs differ:\n got %+v\nwant %+v", got.TopGlobs, want.TopGlobs)
	}
	for name, wp := range want.Providers {
		gp, ok := got.Providers[name]
		if !ok {
			t.Fatalf("provider %s missing after round trip:\n%s", name, data)
		}
		if !reflect.DeepEqual(gp, wp) {
			t.Fatalf("provider %s differs:\n got %+v\nwant %+v\n%s", name, gp, wp, data)
		}
	}
	if strings.Contains(string(data), `surface = ""`) || strings.Contains(string(data), `protocol = ""`) {
		t.Fatalf("unset scalars must not be written:\n%s", data)
	}
}

func TestMarshalConfigWritesExplicitEmptyAPIKeyEnvOnly(t *testing.T) {
	data, err := MarshalConfig(&Layer{Providers: map[string]Provider{
		"a": {ID: "a", Base: "openai", APIKeyEnv: []string{}},
		"b": {ID: "b", Base: "openai"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "api_key_env = []") {
		t.Fatalf("an explicit empty list is meaningful (spec §6.2) and must be written:\n%s", s)
	}
	if strings.Count(s, "api_key_env") != 1 {
		t.Fatalf("a nil list must not be written:\n%s", s)
	}
}

func TestReadWriteConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "providers.toml")
	l, exists, err := ReadConfigFile(path)
	if err != nil || exists || l == nil || l.Providers == nil {
		t.Fatalf("absent file must read as an empty layer: %v %v %+v", err, exists, l)
	}
	l.Default = "local"
	l.Providers["local"] = Provider{ID: "local", Base: "openai-compatible", Transport: Transport{BaseURL: "http://localhost:8080/v1", Auth: AuthNone}}
	if err := WriteConfigFile(path, l); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode: %v %v", err, info)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temp file must be renamed away")
	}
	back, exists, err := ReadConfigFile(path)
	if err != nil || !exists || back.Default != "local" || back.Providers["local"].Base != "openai-compatible" {
		t.Fatalf("read back: %v %v %+v", err, exists, back)
	}
}

func TestValidInstanceName(t *testing.T) {
	for name, want := range map[string]bool{"work": true, "kimi-for-coding": true, "a.b_c": true, "Work": false, "a/b": false, "": false, "-x": false} {
		if got := ValidInstanceName(name); got != want {
			t.Fatalf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestReadConfigFileReportsOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadConfigFile(path)
	if !errors.Is(err, ErrOldSchema) {
		t.Fatalf("want ErrOldSchema, got %v", err)
	}
}
```

- [ ] **Step 7: Run them to verify they fail**

Run: `cd llm && go test ./registry/ -run 'TestMarshalConfig|TestReadWriteConfigFile|TestReadConfigFileReportsOldSchema|TestValidInstanceName' 2>&1 | head`
Expected: compile errors (`MarshalConfig`, `ReadConfigFile`, `WriteConfigFile` undefined).

- [ ] **Step 8: Implement the writer**

`llm/registry/write.go`:

```go
package registry

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// MarshalConfig renders a user layer as providers.toml (spec §10). Only the
// keys the Layer sets are written — nil pointers, empty maps, empty
// strings, and nil slices are absent — so ParseConfig(MarshalConfig(l))
// yields l and no default or resolved value ever lands on disk. An explicit
// empty api_key_env list is kept: `api_key_env = []` is how a Codex-style
// entry says "no key variable" (spec §6.2).
func MarshalConfig(l *Layer) ([]byte, error) {
	doc := map[string]any{}
	if l.Default != "" {
		doc["default"] = l.Default
	}
	if len(l.TopGlobs) > 0 {
		doc["models"] = modelTables(l.TopGlobs)
	}
	if len(l.Providers) > 0 {
		providers := make(map[string]any, len(l.Providers))
		for name, p := range l.Providers {
			providers[name] = providerTable(p)
		}
		doc["providers"] = providers
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return nil, fmt.Errorf("marshal providers.toml: %w", err)
	}
	return buf.Bytes(), nil
}

func providerTable(p Provider) map[string]any {
	t := map[string]any{}
	setString(t, "base", p.Base)
	if p.InheritModels != nil {
		t["inherit_models"] = *p.InheritModels
	}
	setString(t, "protocol", p.Protocol)
	setString(t, "surface", p.Surface)
	setString(t, "family", p.Family)
	setString(t, "api_key", p.APIKey)
	if p.APIKeyEnv != nil {
		t["api_key_env"] = p.APIKeyEnv
	}
	setStringMap(t, "headers", p.Headers)
	setStringMap(t, "credential_headers", p.CredentialHeaders)
	setString(t, "default_model", p.DefaultModel)
	setString(t, "cheap_model", p.CheapModel)
	transportInto(t, p.Transport)
	capsInto(t, p.Caps)
	if len(p.Models) > 0 {
		t["models"] = modelTables(p.Models)
	}
	return t
}

func modelTables(rows map[string]Model) map[string]any {
	out := make(map[string]any, len(rows))
	for id, m := range rows {
		t := map[string]any{}
		setString(t, "alias_of", m.AliasOf)
		setString(t, "wire_id", m.WireID)
		setString(t, "family", m.Family)
		setString(t, "protocol", m.Protocol)
		setString(t, "surface", m.Surface)
		setStringMap(t, "headers", m.Headers)
		if m.Transport != nil {
			transportInto(t, *m.Transport)
		}
		capsInto(t, m.Caps)
		out[id] = t
	}
	return out
}

func transportInto(t map[string]any, tr Transport) {
	setString(t, "transport", tr.Preset)
	setString(t, "auth", tr.Auth)
	setString(t, "auth_header", tr.AuthHeader)
	setString(t, "base_url", tr.BaseURL)
	setString(t, "host_rule", tr.HostRule)
	setString(t, "endpoint", tr.Endpoint)
	setString(t, "stream_endpoint", tr.StreamEndpoint)
	setString(t, "models_endpoint", tr.ModelsEndpoint)
	setString(t, "count_tokens_endpoint", tr.CountTokensEndpoint)
	setStringMap(t, "vars", tr.Vars)
	setStringMap(t, "vars_env", tr.VarsEnv)
	if len(tr.Body) > 0 {
		t["body"] = tr.Body
	}
}

// capsInto writes every set cap under its toml tag: non-nil pointers are
// dereferenced, non-empty slices and maps are copied, Cost becomes a table.
func capsInto(t map[string]any, c Caps) {
	rv := reflect.ValueOf(c)
	rt := rv.Type()
	for i := range rt.NumField() {
		key, _, _ := strings.Cut(rt.Field(i).Tag.Get("toml"), ",")
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.Pointer:
			if f.IsNil() {
				continue
			}
			if cost, ok := f.Interface().(*Cost); ok {
				t[key] = costTable(*cost)
			} else {
				t[key] = f.Elem().Interface()
			}
		case reflect.Slice, reflect.Map:
			if f.Len() > 0 {
				t[key] = f.Interface()
			}
		}
	}
}

func costTable(c Cost) map[string]any {
	t := map[string]any{"input": c.Input, "output": c.Output, "cache_read": c.CacheRead, "cache_write": c.CacheWrite}
	if len(c.Tiers) > 0 {
		tiers := make([]map[string]any, 0, len(c.Tiers))
		for _, tier := range c.Tiers {
			tiers = append(tiers, map[string]any{"input_tokens_above": tier.InputTokensAbove, "input": tier.Input, "output": tier.Output, "cache_read": tier.CacheRead, "cache_write": tier.CacheWrite})
		}
		t["tiers"] = tiers
	}
	return t
}

func setString(t map[string]any, key, v string) {
	if v != "" {
		t[key] = v
	}
}

// ValidInstanceName is spec §10's instance-name rule (lowercase, no slash),
// the same predicate the parser applies, for callers that write entries.
func ValidInstanceName(name string) bool { return validProviderName(name) }

func setStringMap(t map[string]any, key string, m map[string]string) {
	if len(m) > 0 {
		t[key] = m
	}
}

// ReadConfigFile reads a providers.toml for editing. An absent file is an
// empty layer (exists = false); a parse error, including ErrOldSchema,
// propagates so no writer ever rewrites a file it could not read.
func ReadConfigFile(path string) (*Layer, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Layer{Tag: LayerConfig, Transports: map[string]Transport{}, TopGlobs: map[string]Model{}, Providers: map[string]Provider{}}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	l, err := ParseConfig(data)
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	return l, true, nil
}

// WriteConfigFile marshals l and writes it atomically (temp + rename, mode
// 0644, parent directories created). It writes exactly what l holds: the
// caller decides what a user authored (spec §10 "WriteFile keeps today's
// scrub-and-restore" is satisfied by never putting a resolved credential
// into the Layer in the first place).
func WriteConfigFile(path string, l *Layer) error {
	data, err := MarshalConfig(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("providers.toml: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("providers.toml: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("providers.toml: rename: %w", err)
	}
	return nil
}
```

Note for the round trip: the parser fills `Provider.ID` from the table name and leaves `Model.ID` as the key; the fixture sets both so `reflect.DeepEqual` holds. If `ParseConfig` normalizes anything else (for example an empty `Transport` on a model row becoming nil), adjust the fixture, not the writer — the contract is "what the parser produces is what the writer accepts".

- [ ] **Step 9: Run the tests to verify they pass**

Run: `cd llm && go test ./registry/ 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 10: Lint and commit**

```bash
export PATH="$(go env GOROOT)/bin:$PATH" && gofmt -l llm/registry && make lint-gofmt lint-golangci
git add llm/registry/write.go llm/registry/write_test.go
git commit -m "feat(registry): providers.toml writer for the hub and the providers CLI"
```

---

### Task 2: `llm.Client` dispatches through the registry

**Files:**
- Modify: `llm/client.go` (struct, constructor, `Register`, `DefaultProvider`, `ProviderNames`, `Complete`, `Stream`; new `Models`, `Resolve`, `Registry`, `ContinuationHasher`), `llm/token_count.go` (`CountInputTokens`), `llm/registry_shape.go` (`ShapeRequest` continuation store override), `llm/providers/responses/complete.go` and `stream.go` (hasher from context), `llm/env_registry.go` (`NewFromEnv` builds on `NewClient()` unchanged — no change needed unless it constructs `Client{}` literally)
- Create: `llm/continuation_context.go`, `llm/client_registry_test.go` (`package llm_test`), `llm/registry_shape_continuation_test.go`

**Interfaces:**
- Consumes: Task 1's `ResolveInstance`, `Resolved.DefaultModel`; `llm.ProtocolFor`, `llm.ShapeRequest`, `registry.Registry.{Resolve,DefaultInstance,Instances,ModelIDs,ApplyLive}`.
- Produces:
  - `type ClientOption func(*Client)`; `func WithRegistry(r *registry.Registry) ClientOption`; `func WithClientStateDir(dir string) ClientOption` (named to avoid the old `EnvOption` `WithStateDir`, which Task 13 deletes); `func NewClient(opts ...ClientOption) *Client`.
  - `func (c *Client) Registry() *registry.Registry` (lazy embedded, offline, no user layer, no cache, empty environment when none was given); `func (c *Client) Resolve(ref string) (registry.Resolved, error)`.
  - `type ModelListing struct { Live bool; Models []registry.Resolved }`; `func (c *Client) Models(ctx context.Context, instance string) (ModelListing, error)`; `type LiveModelLister interface { LiveModels(ctx context.Context) ([]registry.Model, error) }` (override seam).
  - `func (c *Client) ContinuationHasher() (*ContinuationHasher, error)`; `func ContextWithContinuationHasher(ctx context.Context, h *ContinuationHasher) context.Context`; `func ContinuationHasherFromContext(ctx context.Context) *ContinuationHasher`.
  - `Register`, `Use`, `Close`, `Initialize`, `ReleaseSessionAPILog`, `Complete`, `Stream`, `CountInputTokens`, `ListModels` (old, `[]ModelInfo`, kept until Task 13), `PlanResponsesContinuation` (override path unchanged here; Task 3 adds the registry path), `SetNameToTag`/`BehaviorTagOf` (kept until Task 13) keep their signatures.
  - `ShapeRequest` additionally sets `Store = true` on a planned Responses continuation (`HistoryMode == HistoryModeResponsesDelta`, non-empty `PreviousResponseID`, `Store == nil`).

- [ ] **Step 1: Write the failing dispatch tests**

`llm/client_registry_test.go` (external test package so it can import the protocol packages):

```go
package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

// fixtureRegistry injects an openai (responses) and a work (chat) instance
// that both point at srvURL, with no environment and no user layer.
func fixtureRegistry(t *testing.T, srvURL string, extra map[string]registry.Provider) *registry.Registry {
	t.Helper()
	instances := map[string]registry.Provider{
		"openai": {Base: "openai", APIKey: "test-key", Transport: registry.Transport{BaseURL: srvURL}},
		"work":   {Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric, APIKey: "work-key", Transport: registry.Transport{BaseURL: srvURL}},
	}
	for k, v := range extra {
		instances[k] = v
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

type recordedRequest struct {
	Path string
	Auth string
	Body map[string]any
}

// responsesServer answers /responses with one assistant message and
// /chat/completions with one choice, and records every request.
func responsesServer(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, recordedRequest{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/responses":
			_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-5.5","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"glm-5","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.5"},{"id":"live-only-model"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest { mu.Lock(); defer mu.Unlock(); return append([]recordedRequest(nil), seen...) }
}

type recordingAdapter struct {
	name string
	mu   sync.Mutex
	reqs []llm.Request
}

func (a *recordingAdapter) Name() string { return a.name }
func (a *recordingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, req)
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("ok")}, nil
}
func (a *recordingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *recordingAdapter) last() llm.Request { a.mu.Lock(); defer a.mu.Unlock(); return a.reqs[len(a.reqs)-1] }

func userRequest(provider, model string) llm.Request {
	return llm.Request{Provider: provider, Model: model, Messages: []llm.Message{llm.User("hello")}}
}

func TestClientDispatchesByProtocolWithCredential(t *testing.T) {
	srv, seen := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	resp, err := c.Complete(context.Background(), userRequest("openai", "gpt-5.5"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "openai" || resp.Message.TextContent() != "hi" {
		t.Fatalf("response: %+v", resp)
	}
	reqs := seen()
	if len(reqs) != 1 || reqs[0].Path != "/responses" || reqs[0].Auth != "Bearer test-key" {
		t.Fatalf("wire: %+v", reqs)
	}
	resp, err = c.Complete(context.Background(), userRequest("work", "glm-5"))
	if err != nil || resp.Provider != "work" {
		t.Fatalf("chat instance: %v %+v", err, resp)
	}
	if reqs := seen(); reqs[1].Path != "/chat/completions" || reqs[1].Auth != "Bearer work-key" {
		t.Fatalf("chat wire: %+v", reqs[1])
	}
}

func TestClientOverrideUnderResolvableNameSeesShapedRequest(t *testing.T) {
	srv, _ := responsesServer(t)
	r := fixtureRegistry(t, srv.URL, map[string]registry.Provider{
		"capped": {Base: "openai", APIKey: "k", Transport: registry.Transport{BaseURL: srv.URL}, Caps: registry.Caps{MaxOutputTokens: new(123), Sampling: new(false)}},
	})
	c := llm.NewClient(llm.WithRegistry(r))
	fake := &recordingAdapter{name: "capped"}
	c.Register(fake)
	req := userRequest("capped", "gpt-5.5")
	req.Temperature = new(0.5)
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := fake.last()
	if got.MaxTokens == nil || *got.MaxTokens != 123 {
		t.Fatalf("ShapeRequest must apply MaxOutputTokens before the override sees the request: %+v", got.MaxTokens)
	}
	if got.Temperature != nil {
		t.Fatal("ShapeRequest must drop sampling the row turned off")
	}
}

func TestClientOverrideUnderUnresolvableNamePassesThrough(t *testing.T) {
	c := llm.NewClient()
	fake := &recordingAdapter{name: "fake"}
	c.Register(fake)
	req := userRequest("fake", "anything")
	req.Temperature = new(0.5)
	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("an unresolvable override must not error: %v", err)
	}
	if resp.Provider != "fake" {
		t.Fatalf("provider stamp: %q", resp.Provider)
	}
	got := fake.last()
	if got.MaxTokens != nil || got.Temperature == nil {
		t.Fatalf("request must pass through untouched: %+v", got)
	}
	if c.DefaultProvider() != "fake" {
		t.Fatalf("the first override is the default when the registry has none: %q", c.DefaultProvider())
	}
}

func TestClientUnknownInstanceNamesAvailableOnes(t *testing.T) {
	srv, _ := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	_, err := c.Complete(context.Background(), userRequest("nope", "gpt-5.5"))
	var cfgErr *llm.ConfigurationError
	if !errors.As(err, &cfgErr) || !strings.Contains(err.Error(), `unknown instance "nope"`) || !strings.Contains(err.Error(), "openai") {
		t.Fatalf("want ConfigurationError naming the available instances, got %v", err)
	}
}

func TestClientEmbeddedRegistryIsHermetic(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "leak")
	c := llm.NewClient()
	res, err := c.Resolve("openai/gpt-5.5")
	if err != nil {
		t.Fatalf("a curated implicit id resolves without a credential (spec §5.2): %v", err)
	}
	if res.Credential.Value != "" || res.Credential.Source != "none" {
		t.Fatalf("the lazy registry must not read the process environment: %+v", res.Credential)
	}
	if c.DefaultProvider() != "" {
		t.Fatalf("no instances, no default: %q", c.DefaultProvider())
	}
}

func TestClientModelsAppliesLiveListingAndHidesToolLessRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"tools-ok","supported_parameters":["tools","temperature"]},{"id":"no-tools","supported_parameters":["temperature"]}]}`))
	}))
	t.Cleanup(srv.Close)
	r := fixtureRegistry(t, srv.URL, nil)
	c := llm.NewClient(llm.WithRegistry(r))
	listing, err := c.Models(context.Background(), "work")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if !listing.Live {
		t.Fatal("a transport with a models endpoint yields a live listing")
	}
	ids := map[string]bool{}
	for _, m := range listing.Models {
		ids[m.ModelID] = true
		if m.Instance != "work" {
			t.Fatalf("every row is resolved on the instance: %+v", m)
		}
	}
	if !ids["tools-ok"] || ids["no-tools"] {
		t.Fatalf("live Tools=false hides the row (spec §5, §7.5): %v", ids)
	}
	res, err := c.Resolve("work/tools-ok")
	if err != nil || res.Provenance["model"] != "live" {
		t.Fatalf("the live row must be resolvable afterwards: %v %v", err, res.Provenance["model"])
	}
}

func TestClientModelsRegistryOnlyWhenUnsupported(t *testing.T) {
	r := fixtureRegistry(t, "http://127.0.0.1:9", map[string]registry.Provider{
		"nolist": {Base: "openai", APIKey: "k", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9", ModelsEndpoint: registry.EndpointUnsupported}},
	})
	c := llm.NewClient(llm.WithRegistry(r))
	listing, err := c.Models(context.Background(), "nolist")
	if err != nil {
		t.Fatalf("an unsupported models endpoint is not a failure: %v", err)
	}
	if listing.Live || len(listing.Models) == 0 {
		t.Fatalf("registry-only listing must return the catalog rows: live=%v n=%d", listing.Live, len(listing.Models))
	}
}

func TestClientModelsOverrideLister(t *testing.T) {
	c := llm.NewClient()
	c.Register(&listingAdapter{recordingAdapter: recordingAdapter{name: "fake"}, models: []registry.Model{{ID: "m1"}, {ID: "m0"}}})
	listing, err := c.Models(context.Background(), "fake")
	if err != nil || !listing.Live || len(listing.Models) != 2 || listing.Models[0].ModelID != "m0" {
		t.Fatalf("override listing: %v %+v", err, listing)
	}
	c.Register(&recordingAdapter{name: "mute"})
	if _, err := c.Models(context.Background(), "mute"); err == nil {
		t.Fatal("an unresolvable override without LiveModels cannot list")
	}
}

type listingAdapter struct {
	recordingAdapter
	models []registry.Model
}

func (a *listingAdapter) LiveModels(context.Context) ([]registry.Model, error) { return a.models, nil }

func TestClientProviderNamesUnionsOverridesAndInstances(t *testing.T) {
	srv, _ := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	c.Register(&recordingAdapter{name: "fake"})
	names := strings.Join(c.ProviderNames(), ",")
	for _, want := range []string{"fake", "openai", "work"} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing %s in %s", want, names)
		}
	}
	if c.DefaultProvider() != "openai" {
		t.Fatalf("the registry ranks openai first (spec §5.1): %q", c.DefaultProvider())
	}
}
```

`llm/registry_shape_continuation_test.go` (`package llm`):

```go
package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestShapeRequestForcesStoreForPlannedContinuation(t *testing.T) {
	res := registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{Fields: map[string]bool{"store": true, "previous_response_id": true}}}
	req := Request{HistoryMode: HistoryModeResponsesDelta, PreviousResponseID: "resp_1"}
	if got := ShapeRequest(req, res); got.Store == nil || !*got.Store {
		t.Fatal("a planned continuation needs store = true (spec §7.6)")
	}
	explicit := Request{HistoryMode: HistoryModeResponsesDelta, PreviousResponseID: "resp_1", Store: new(false)}
	if got := ShapeRequest(explicit, res); *got.Store {
		t.Fatal("an explicit store decision is never overridden")
	}
	full := Request{HistoryMode: HistoryModeFullHistory}
	if got := ShapeRequest(full, res); got.Store != nil {
		t.Fatal("no continuation, no override")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd llm && go test . -run 'TestClient|TestShapeRequestForcesStore' 2>&1 | head -20`
Expected: compile errors (`WithRegistry`, `Models`, `Resolve`, `LiveModels`, `ModelListing` undefined).

- [ ] **Step 3: Implement the client**

`llm/continuation_context.go`:

```go
package llm

import "context"

type continuationHasherKey struct{}

// ContextWithContinuationHasher attaches the client's continuation hasher so
// a protocol can stamp resp.Raw["id_hash"] (spec §7.6) without holding
// per-client state; the protocols are process singletons.
func ContextWithContinuationHasher(ctx context.Context, h *ContinuationHasher) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, continuationHasherKey{}, h)
}

// ContinuationHasherFromContext returns the hasher attached by
// ContextWithContinuationHasher, or nil.
func ContinuationHasherFromContext(ctx context.Context) *ContinuationHasher {
	h, _ := ctx.Value(continuationHasherKey{}).(*ContinuationHasher)
	return h
}
```

`llm/client.go` — replace the struct, constructor, registration, and dispatch (keep `bindAPIAttemptSinkBeforeDispatch`, `providerOperation`, `Use`, `ReleaseSessionAPILog`, `Close`, `Initialize`, `SupportsToolChoice`, `ValidateModelCompatibility`, `ListModels`, `behaviorTagFor`, `BehaviorTagOf`, `SetNameToTag`, `providerStampStream`, and `normalizeProviderName` as they are; Task 13 deletes the tag machinery and the old listing):

```go
// Client routes LLM requests (spec §8.1): the instance half of a request
// resolves through the registry to a Resolved record whose Protocol names
// the wire implementation. Adapters registered with Register form an
// override map consulted by instance name first — when the name also
// resolves, the override receives the shaped request; when it does not,
// the request passes through untouched. Middleware, API-attempt logging,
// and provider stamping apply to both paths.
type Client struct {
	registry     *registry.Registry
	registryOnce sync.Once
	registryErr  error
	stateDir     string
	hasherOnce   sync.Once
	hasher       *ContinuationHasher
	hasherErr    error

	overrides       map[string]ProviderAdapter
	defaultOverride string
	middleware      []Middleware
	nameToTag       map[string]string
}

// ClientOption configures NewClient.
type ClientOption func(*Client)

// WithRegistry supplies the registry the client resolves instances against.
func WithRegistry(r *registry.Registry) ClientOption {
	return func(c *Client) { c.registry = r }
}

// WithClientStateDir names the session state directory that holds the
// continuation secret (spec §7.6: "the ContinuationHasher stays on the
// client, keyed by state dir").
func WithClientStateDir(dir string) ClientOption {
	return func(c *Client) { c.stateDir = dir }
}

// NewClient returns a client with no overrides. Without WithRegistry it
// lazily loads the embedded snapshot offline (spec §8.1).
func NewClient(opts ...ClientOption) *Client {
	c := &Client{overrides: map[string]ProviderAdapter{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Registry returns the client's registry. Without WithRegistry it is the
// embedded snapshot and overlay loaded offline with no user layer, no
// cache, and no environment, so a bare NewClient() resolves the same
// records on every machine and never reads a developer's keys.
func (c *Client) Registry() *registry.Registry {
	c.registryOnce.Do(func() {
		if c.registry != nil {
			return
		}
		c.registry, c.registryErr = registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
		)
	})
	return c.registry
}

// Resolve resolves an instance/model reference through the client's registry.
func (c *Client) Resolve(ref string) (registry.Resolved, error) {
	r := c.Registry()
	if r == nil {
		return registry.Resolved{}, fmt.Errorf("registry unavailable: %w", c.registryErr)
	}
	return r.Resolve(ref)
}

// ContinuationHasher returns the hasher for the client's state directory,
// creating the secret on first use; ErrContinuationSecretUnavailable when
// the client has no state directory.
func (c *Client) ContinuationHasher() (*ContinuationHasher, error) {
	c.hasherOnce.Do(func() {
		if strings.TrimSpace(c.stateDir) == "" {
			c.hasherErr = fmt.Errorf("%w: client has no state directory", ErrContinuationSecretUnavailable)
			return
		}
		c.hasher, c.hasherErr = ContinuationHasherForStateDir(c.stateDir)
	})
	return c.hasher, c.hasherErr
}

func (c *Client) withHasher(ctx context.Context) context.Context {
	if h, err := c.ContinuationHasher(); err == nil {
		return ContextWithContinuationHasher(ctx, h)
	}
	return ctx
}

// Register adds an override adapter keyed by its Name. The first override
// that does not implement NonDefaultEligible becomes the default when the
// registry has no default instance. Adapters implementing Initializer are
// initialized immediately with a background context.
func (c *Client) Register(adapter ProviderAdapter) {
	if c.overrides == nil {
		c.overrides = map[string]ProviderAdapter{}
	}
	name := normalizeProviderName(adapter.Name())
	c.overrides[name] = adapter
	if c.defaultOverride == "" {
		if _, skip := adapter.(NonDefaultEligible); !skip {
			c.defaultOverride = name
		}
	}
	if init, ok := adapter.(Initializer); ok {
		_ = init.Initialize(context.Background())
	}
}

// SetDefaultProvider pins the default instance name for requests that
// name none.
func (c *Client) SetDefaultProvider(name string) { c.defaultOverride = normalizeProviderName(name) }

// DefaultProvider is the pinned or first-registered override, else the
// registry's default instance (spec §5.1), else "".
func (c *Client) DefaultProvider() string {
	if c.defaultOverride != "" {
		return c.defaultOverride
	}
	r := c.Registry()
	if r == nil {
		return ""
	}
	name, _, err := r.DefaultInstance()
	if err != nil {
		return ""
	}
	return name
}

// ProviderNames lists every override and every registry instance, sorted.
func (c *Client) ProviderNames() []string {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	for name := range c.overrides {
		seen[name] = true
	}
	if r := c.Registry(); r != nil {
		for _, inst := range r.Instances() {
			seen[inst.Name] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// dispatchTarget is where one request goes: an override, a resolved record,
// or both (spec §8.1).
type dispatchTarget struct {
	name     string
	override ProviderAdapter
	res      registry.Resolved
	resolved bool
	protocol Protocol
}

func (c *Client) dispatchTarget(req Request) (dispatchTarget, error) {
	name := normalizeProviderName(req.Provider)
	if name == "" {
		name = c.DefaultProvider()
	}
	if name == "" {
		return dispatchTarget{}, &ConfigurationError{Message: "no provider specified and no default provider configured"}
	}
	t := dispatchTarget{name: name, override: c.overrides[name]}
	res, err := c.Resolve(name + "/" + req.Model)
	switch {
	case err == nil:
		t.res, t.resolved = res, true
	case t.override == nil:
		return dispatchTarget{}, &ConfigurationError{Message: err.Error()}
	}
	if t.override == nil {
		p, ok := ProtocolFor(t.res.Protocol)
		if !ok {
			return dispatchTarget{}, &ConfigurationError{Message: fmt.Sprintf("%s: protocol %q is not registered (import primeradiant.com/evener/llm/providers/all)", name, t.res.Protocol)}
		}
		t.protocol = p
	}
	return t, nil
}

// Complete validates the request, resolves its target, shapes it for the
// resolved row, runs it through the middleware chain and the override or
// protocol, and stamps the instance name onto the response and any error.
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	ctx = c.bindAPIAttemptSinkBeforeDispatch(ctx)
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	if req.AdapterTimeout == nil {
		req.AdapterTimeout = new(DefaultAdapterTimeout())
	}
	t, err := c.dispatchTarget(req)
	if err != nil {
		return Response{}, err
	}
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	tag := c.behaviorTagFor(t.name)
	base := func(ctx context.Context, req Request) (Response, error) {
		if t.override != nil {
			return t.override.Complete(ctx, req)
		}
		return t.protocol.Complete(c.withHasher(ctx), req, t.res)
	}
	handler := applyMiddlewareComplete(base, c.middleware)
	resp, err := handler(ctx, req)
	resp.Provider = t.name
	err = RewriteErrorProvider(err, t.name)
	err = StampErrorBehaviorTag(err, tag)
	return resp, err
}

// Stream is Complete's streaming twin; stream events carry the instance
// name through providerStampStream.
func (c *Client) Stream(ctx context.Context, req Request) (Stream, error) {
	ctx = c.bindAPIAttemptSinkBeforeDispatch(ctx)
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.AdapterTimeout == nil {
		req.AdapterTimeout = new(DefaultAdapterTimeout())
	}
	t, err := c.dispatchTarget(req)
	if err != nil {
		return nil, err
	}
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	tag := c.behaviorTagFor(t.name)
	base := func(ctx context.Context, req Request) (Stream, error) {
		if t.override != nil {
			return t.override.Stream(ctx, req)
		}
		return t.protocol.Stream(c.withHasher(ctx), req, t.res)
	}
	handler := applyMiddlewareStream(base, c.middleware)
	st, err := handler(ctx, req)
	if err != nil {
		err = RewriteErrorProvider(err, t.name)
		err = StampErrorBehaviorTag(err, tag)
		return nil, err
	}
	return newProviderStampStream(st, t.name, tag), nil
}

// ModelListing is what Models returns: the instance's visible rows after
// its live listing, when the transport has one, was applied to the registry.
type ModelListing struct {
	// Live is true when a live listing was fetched and applied; false means
	// registry-only (spec §8.1: an unsupported models endpoint is not a failure).
	Live bool
	// Models are the visible rows — hidden rows and rows a live listing marks
	// Tools = false are dropped (spec §5) — sorted by model id.
	Models []registry.Resolved
}

// LiveModelLister is the optional override interface for adapters that serve
// a live model listing (test doubles for Models).
type LiveModelLister interface {
	LiveModels(ctx context.Context) ([]registry.Model, error)
}

// Models lists an instance's models: the live listing is fetched through
// the protocol (or the override's LiveModels), applied to the registry so
// later Resolve calls see it, and every id the registry now knows for the
// instance is resolved and filtered by the §5 visibility rule.
func (c *Client) Models(ctx context.Context, instance string) (ModelListing, error) {
	instance = normalizeProviderName(instance)
	r := c.Registry()
	if r == nil {
		return ModelListing{}, fmt.Errorf("registry unavailable: %w", c.registryErr)
	}
	override := c.overrides[instance]
	res, resolveErr := r.ResolveInstance(instance)
	if resolveErr != nil && override == nil {
		return ModelListing{}, &ConfigurationError{Message: resolveErr.Error()}
	}
	var rows []registry.Model
	live := false
	if override != nil {
		lister, ok := override.(LiveModelLister)
		if !ok && resolveErr != nil {
			return ModelListing{}, &ConfigurationError{Message: fmt.Sprintf("provider %s does not support listing models", instance)}
		}
		if ok {
			opCtx, op := c.beginProviderOperation(ctx)
			var err error
			rows, err = lister.LiveModels(opCtx)
			op.settle(opCtx, err)
			if err != nil {
				return ModelListing{}, RewriteErrorProvider(err, instance)
			}
			live = true
		}
	} else {
		p, ok := ProtocolFor(res.Protocol)
		if !ok {
			return ModelListing{}, &ConfigurationError{Message: fmt.Sprintf("%s: protocol %q is not registered", instance, res.Protocol)}
		}
		opCtx, op := c.beginProviderOperation(ctx)
		var err error
		rows, err = p.ListModels(opCtx, res)
		op.settle(opCtx, err)
		switch {
		case errors.Is(err, ErrModelListingUnsupported):
		case err != nil:
			return ModelListing{}, RewriteErrorProvider(err, instance)
		default:
			live = true
		}
	}
	if resolveErr != nil {
		// An override with no registry record: its listing is the whole truth.
		out := make([]registry.Resolved, 0, len(rows))
		for _, m := range rows {
			out = append(out, registry.Resolved{Instance: instance, ModelID: m.ID, WireID: m.ID, Model: m, Caps: m.Caps})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
		return ModelListing{Live: live, Models: out}, nil
	}
	if live {
		r.ApplyLive(instance, rows)
	}
	ids, err := r.ModelIDs(instance)
	if err != nil {
		return ModelListing{}, &ConfigurationError{Message: err.Error()}
	}
	out := make([]registry.Resolved, 0, len(ids))
	for _, id := range ids {
		row, err := r.Resolve(instance + "/" + id)
		if err != nil || row.Model.Hidden || (row.Caps.Tools != nil && !*row.Caps.Tools) {
			continue
		}
		out = append(out, row)
	}
	return ModelListing{Live: live, Models: out}, nil
}
```

`llm/token_count.go` — `CountInputTokens` resolves through `dispatchTarget`; overrides keep the `InputTokenCounter` path; the protocol path calls `t.protocol.CountTokens(ctx, req, t.res)` inside `beginProviderOperation`/`settle`, maps `ErrInputTokenCountUnsupported` to `EstimateInputTokens`, and returns `InputTokenCount{Tokens: n, Exact: true, Source: TokenCountSourceProvider, Provider: t.name, Model: req.Model}`; errors are `RewriteErrorProvider(err, t.name)`.

`llm/registry_shape.go` — add before `return req`:

```go
	// A planned Responses continuation needs server-side storage; the
	// continuation store override (spec §7.6) turns store on unless the
	// caller decided it explicitly.
	if req.HistoryMode == HistoryModeResponsesDelta && strings.TrimSpace(req.PreviousResponseID) != "" && req.Store == nil {
		req.Store = new(true)
	}
```

`llm/providers/responses/complete.go` and `stream.go` — wherever `p.Hasher` is read to stamp `Raw["id_hash"]`, use `hasher := llm.ContinuationHasherFromContext(ctx); if hasher == nil { hasher = p.Hasher }`.

`llm/env_registry.go` `NewFromEnv` and `llm/providers_config.go` `newFromProviders` call `NewClient()` with no arguments and `Register` — they compile unchanged. `llm/client_test.go` and the other in-package tests that construct `&Client{providers: …}` literally must switch to `NewClient()` + `Register`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd llm && go test . ./providers/... 2>&1 | tail -5`
Expected: PASS. If `TestClientDispatchesByProtocolWithCredential` fails on the response decode, print the request the fake server received and fix the test fixture body (the responses protocol's decoder is the plan-2 port; the JSON above matches its `output[]`/`usage` shape), never the protocol.

- [ ] **Step 5: Run the whole workspace gate and commit**

```bash
for m in . llm envvars auth identifier invariant fuzz agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add llm/client.go llm/token_count.go llm/registry_shape.go llm/continuation_context.go llm/client_registry_test.go llm/registry_shape_continuation_test.go llm/providers/responses/complete.go llm/providers/responses/stream.go llm/client_test.go
git commit -m "feat(llm): route Client dispatch through the registry with an override map"
```

(Add any other in-package test file that had to move from struct literals to `NewClient()`; list them explicitly in the `git add`.)

---

### Task 3: Continuation planning from `Resolved` + `BuildBody`

**Files:**
- Create: `llm/responses_continuation_plan.go`, `llm/responses_fingerprint_test.go` (moved from `llm/providers/responses/fingerprint_test.go`), `llm/client_continuation_test.go` (`package llm_test`), `llm/providers/tokenauth/codex_scope_test.go`
- Modify: `llm/client.go` (`PlanResponsesContinuation`), `llm/providers/responses/fingerprint.go` (delete `RequestFingerprint` and `EndpointFamily`; the package calls `llm.ResponsesEndpointFamilyFor` where it labelled endpoint families), `llm/providers/tokenauth/codex.go` (`AuthScope`), `llm/registry_shape.go` (doc comment mentions the override; no code change)
- Delete: `llm/providers/responses/fingerprint_test.go` (moved)

**Interfaces:**
- Consumes: Task 2's `dispatchTarget`, `Client.ContinuationHasher`; `Protocol.BuildBody`; `ContinuationHasher.{HashContinuationScopeValue,HashContinuationStorageScope}`; `AuthenticatorFor`.
- Produces:
  - `func ResponsesEndpointFamilyFor(res registry.Resolved) ResponsesEndpointFamily` — `openai_codex` iff `res.Transport.Auth == registry.AuthOAuthOpenAICodex`.
  - `func ResponsesRequestFingerprint(family ResponsesEndpointFamily, body map[string]any) (string, error)` — prefix `cont-req-v2:`; excludes `input`, `previous_response_id`, `conversation`, `stream`, and on the public family `store`.
  - `type AuthScopeProvider interface { AuthScope(ctx context.Context, res registry.Resolved) (accountID, workspaceID string, err error) }` — implemented by `tokenauth.Codex`.
  - `Client.PlanResponsesContinuation(ctx, req)` on the registry path: `EndpointFamily`, `AuthScopeIdentity`, `OrgIDHash`, `ProjectIDHash`, `RequestFingerprint`, `StorageScope`, `StorageScopeFingerprint`, `StoragePolicyLabel`, `ContinuationStorageAllowed`; `CanFallbackToChat` is left false and Task 8 deletes the field.

- [ ] **Step 1: Move the fingerprint and family helpers into `llm` (test first)**

Create `llm/responses_fingerprint_test.go` by moving `llm/providers/responses/fingerprint_test.go` into package `llm`, renaming the calls to `ResponsesRequestFingerprint`/`ResponsesEndpointFamilyFor`, and changing every expected prefix from `cont-req-v1:` to `cont-req-v2:`. Add:

```go
func TestResponsesRequestFingerprintIgnoresStreamAndContinuationFields(t *testing.T) {
	base := map[string]any{"model": "gpt-5.5", "input": []any{"a"}, "temperature": 0.2}
	streamed := map[string]any{"model": "gpt-5.5", "input": []any{"b"}, "temperature": 0.2, "stream": true, "previous_response_id": "resp_1", "conversation": "conv_1", "store": true}
	a, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, streamed)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.HasPrefix(a, "cont-req-v2:") {
		t.Fatalf("fingerprints must agree across Complete/Stream and continuation fields: %s vs %s", a, b)
	}
	c, _ := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAICodex, streamed)
	d, _ := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAICodex, base)
	if c == d {
		t.Fatal("store is part of the fingerprint on the Codex family")
	}
}

func TestResponsesEndpointFamilyFor(t *testing.T) {
	if got := ResponsesEndpointFamilyFor(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthOAuthOpenAICodex}}); got != ResponsesEndpointFamilyOpenAICodex {
		t.Fatalf("codex transport: %s", got)
	}
	if got := ResponsesEndpointFamilyFor(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthBearer}}); got != ResponsesEndpointFamilyOpenAIPublic {
		t.Fatalf("bearer: %s", got)
	}
}
```

Run: `cd llm && go test . -run 'TestResponsesRequestFingerprint|TestResponsesEndpointFamilyFor' 2>&1 | head -5` → compile errors.

Then create `llm/responses_continuation_plan.go` with the two helpers moved verbatim from `llm/providers/responses/fingerprint.go` (same hashing: canonical JSON of the filtered body, SHA-256, hex), renamed and re-prefixed:

```go
const responsesRequestFingerprintPrefix = "cont-req-v2:"

// ResponsesEndpointFamilyFor is spec §7.6: openai_codex on the Codex
// transport, openai_public everywhere else.
func ResponsesEndpointFamilyFor(res registry.Resolved) ResponsesEndpointFamily {
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		return ResponsesEndpointFamilyOpenAICodex
	}
	return ResponsesEndpointFamilyOpenAIPublic
}

// ResponsesRequestFingerprint hashes a built Responses body minus the
// fields that differ between a continuation request and its full-history
// twin (spec §7.6): input, previous_response_id, conversation, stream, and
// on the public family store. The v2 prefix marks the cut-over: v1 was
// computed from the pre-registry builder.
func ResponsesRequestFingerprint(family ResponsesEndpointFamily, body map[string]any) (string, error) { /* moved */ }
```

Delete both from `responses/fingerprint.go`; where `responses` labelled the API-attempt endpoint family from `EndpointFamily(res)`, call `llm.ResponsesEndpointFamilyFor(res)`. Run the moved tests and `go test ./providers/responses/ ./providers/wirecapture/ ./providers/difftest/` → PASS (the goldens do not contain fingerprints).

Commit: `git add llm/responses_continuation_plan.go llm/responses_fingerprint_test.go llm/providers/responses/fingerprint.go llm/providers/responses/<callers> && git rm llm/providers/responses/fingerprint_test.go && git commit -m "refactor(llm): move the Responses continuation fingerprint into llm (cont-req-v2)"`.

- [ ] **Step 2: Write the failing Codex scope test**

`llm/providers/tokenauth/codex_scope_test.go`:

```go
package tokenauth

import (
	"context"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm/registry"
)

func TestCodexAuthScopeReadsTheInstanceRecord(t *testing.T) {
	stateDir := t.TempDir()
	record := authopenai.AuthRecord{Version: 1, Provider: "openai-codex", Source: authopenai.AuthSourceOAuth, AccessToken: "tok", AccountID: "acct_1", WorkspaceID: "ws_1", Expiry: time.Now().Add(time.Hour)}
	if err := authopenai.SaveAuth(stateDir, "openai-codex", record); err != nil {
		t.Fatal(err)
	}
	c := &Codex{StateDir: stateDir}
	account, workspace, err := c.AuthScope(context.Background(), registry.Resolved{Instance: "openai-codex", Credential: registry.Credential{Source: "oauth"}})
	if err != nil || account != "acct_1" || workspace != "ws_1" {
		t.Fatalf("AuthScope: %q %q %v", account, workspace, err)
	}
	if _, _, err := c.AuthScope(context.Background(), registry.Resolved{Instance: "work", Credential: registry.Credential{Source: "none"}}); err == nil {
		t.Fatal("no record, no scope")
	}
}
```

Run: `cd llm && go test ./providers/tokenauth/ -run TestCodexAuthScope` → compile error (`AuthScope` undefined).

- [ ] **Step 3: Implement `AuthScope` and the registry planner**

`llm/providers/tokenauth/codex.go`:

```go
// AuthScope implements llm.AuthScopeProvider: the account and workspace
// claims of the instance's OAuth record, which the continuation storage
// scope hashes (spec §7.6).
func (c *Codex) AuthScope(_ context.Context, res registry.Resolved) (string, string, error) {
	if res.Credential.Source != "oauth" {
		return "", "", notSignedIn(res.Instance)
	}
	record, err := authopenai.LoadAuth(c.stateDir(), res.Instance)
	if err != nil {
		return "", "", err
	}
	return record.AccountID, record.WorkspaceID, nil
}
```

(`c.stateDir()` is the existing helper that falls back to `authopenai.DefaultStateDir()`; use its real name.)

`llm/responses_continuation_plan.go` — add:

```go
// AuthScopeProvider is implemented by an authenticator whose credential
// carries an identity beyond the key value (the Codex transport): the
// claims the continuation storage scope hashes (spec §7.6).
type AuthScopeProvider interface {
	AuthScope(ctx context.Context, res registry.Resolved) (accountID, workspaceID string, err error)
}

// responsesStoragePolicy labels the built body's storage behaviour as
// today's adapter did: Codex is unproven, public with store = true is
// storable, else not.
func responsesStoragePolicy(family ResponsesEndpointFamily, body map[string]any) (string, bool) {
	if family == ResponsesEndpointFamilyOpenAICodex {
		return ResponsesStoragePolicyCodexUnproven, false
	}
	if store, _ := body["store"].(bool); store {
		return ResponsesStoragePolicyPublicOpenAIStore, true
	}
	return ResponsesStoragePolicyPublicOpenAINoStore, false
}

// continuationAvailable is spec §7.6's gate: the openai-responses protocol
// with previous_response_id and store both sendable after layering.
func continuationAvailable(res registry.Resolved) bool {
	return res.Protocol == registry.ProtocolOpenAIResponses && res.Caps.Fields["previous_response_id"] && res.Caps.Fields["store"]
}

func hashIfSet(h *ContinuationHasher, kind, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return h.HashContinuationScopeValue(kind, value)
}

// planContinuation computes the plan from Resolved and the built body
// (spec §7.6). Unavailable endpoints get a plan with the family and no
// storage permission, so the session falls back to full history.
func (c *Client) planContinuation(ctx context.Context, req Request, res registry.Resolved, p Protocol) (ResponsesContinuationPlan, error) {
	family := ResponsesEndpointFamilyFor(res)
	plan := ResponsesContinuationPlan{EndpointFamily: family}
	if family == ResponsesEndpointFamilyOpenAICodex {
		plan.StoragePolicyLabel = ResponsesStoragePolicyCodexUnproven
	}
	if !continuationAvailable(res) {
		return plan, nil
	}
	hasher, err := c.ContinuationHasher()
	if err != nil {
		return plan, err
	}
	body, err := p.BuildBody(req, res)
	if err != nil {
		return plan, err
	}
	fingerprint, err := ResponsesRequestFingerprint(family, body)
	if err != nil {
		return plan, err
	}
	policy, allowed := responsesStoragePolicy(family, body)

	authSource, credentialValue := "api_key", res.Credential.Source+"\x00"+res.Credential.Value
	account, workspace := "", ""
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		authSource = "oauth"
		if a, ok := AuthenticatorFor(res.Transport.Auth); ok {
			if sp, ok := a.(AuthScopeProvider); ok {
				if account, workspace, err = sp.AuthScope(ctx, res); err != nil {
					return plan, err
				}
			}
		}
		credentialValue = "oauth:" + strings.TrimSpace(account) + ":" + strings.TrimSpace(workspace)
	}
	credentialHash, err := hasher.HashContinuationScopeValue("credential", credentialValue)
	if err != nil {
		return plan, err
	}
	hashes := map[string]string{}
	for kind, value := range map[string]string{"account": account, "workspace": workspace, "org_id": res.Headers["OpenAI-Organization"], "project_id": res.Headers["OpenAI-Project"], "conversation_id": req.ConversationID} {
		if hashes[kind], err = hashIfSet(hasher, kind, value); err != nil {
			return plan, err
		}
	}
	scope := ContinuationStorageScope{
		HashVersion: ContinuationScopeHashVersion, Provider: res.Instance, EndpointFamily: string(family),
		BaseURL: strings.TrimRight(strings.TrimSpace(res.Transport.BaseURL), "/"), Path: res.Transport.Endpoint,
		AuthSource: authSource, OrgIDHash: hashes["org_id"], ProjectIDHash: hashes["project_id"],
		AccountHash: hashes["account"], WorkspaceHash: hashes["workspace"], CredentialHash: credentialHash,
		ConversationIDHash: hashes["conversation_id"], StoragePolicy: policy,
	}
	if scope.Fingerprint, err = hasher.HashContinuationStorageScope(scope); err != nil {
		return plan, err
	}
	plan.AuthScopeIdentity = AuthScopeIdentity{Version: ContinuationScopeHashVersion, AuthSource: authSource, CredentialHash: credentialHash, AccountHash: hashes["account"], WorkspaceHash: hashes["workspace"]}
	plan.OrgIDHash, plan.ProjectIDHash = hashes["org_id"], hashes["project_id"]
	plan.RequestFingerprint = fingerprint
	plan.StorageScope, plan.StorageScopeFingerprint = scope, scope.Fingerprint
	plan.StoragePolicyLabel, plan.ContinuationStorageAllowed = policy, allowed
	return plan, nil
}
```

`llm/client.go` — replace `PlanResponsesContinuation`:

```go
// PlanResponsesContinuation returns the continuation plan for req's
// instance (spec §7.6): an override's own planner when one is registered
// under the name, else the plan computed from Resolved and the built body.
func (c *Client) PlanResponsesContinuation(ctx context.Context, req Request) (ResponsesContinuationPlan, error) {
	t, err := c.dispatchTarget(req)
	if err != nil {
		return ResponsesContinuationPlan{}, err
	}
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	if t.override != nil {
		planner, ok := t.override.(ResponsesContinuationPlanner)
		if !ok {
			return ResponsesContinuationPlan{}, &ConfigurationError{Message: "provider does not support responses continuation planning: " + t.name}
		}
		return planner.PlanResponsesContinuation(req)
	}
	return c.planContinuation(ctx, req, t.res, t.protocol)
}
```

- [ ] **Step 4: Write the failing plan-derivation tests**

`llm/client_continuation_test.go` (`package llm_test`, reuse `fixtureRegistry`/`responsesServer` from Task 2's test file):

```go
func continuationClient(t *testing.T, r *registry.Registry) *llm.Client {
	t.Helper()
	return llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(t.TempDir()))
}

func TestPlanContinuationFromResolved(t *testing.T) {
	srv, _ := responsesServer(t)
	r := fixtureRegistry(t, srv.URL, map[string]registry.Provider{
		"groq":  {Base: "groq", Protocol: registry.ProtocolOpenAIResponses, APIKey: "gk"},
		"azure": {Base: "azure", APIKey: "ak", Transport: registry.Transport{Vars: map[string]string{"AZURE_RESOURCE_NAME": "res1"}}},
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
	third, _ := c.PlanResponsesContinuation(context.Background(), req)
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
	c.Register(&planningOverride{recordingAdapter: recordingAdapter{name: "openai"}, plan: want})
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
```

Run: `cd llm && go test . -run 'TestPlanContinuation' 2>&1 | head` → FAIL/compile errors, then after Step 3 → PASS. (If `azure/gpt55-prod` needs a variable the fixture lacks, read `llm/registry/data/providers_overlay.toml`'s `azure` entry and add the variable to `Transport.Vars`; the golden `Resolved` record for `azure/gpt55-prod` from plan 1 shows the exact set.)

- [ ] **Step 5: Gate, lint, commit**

```bash
cd llm && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'; cd ..
for m in . agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add llm/responses_continuation_plan.go llm/client.go llm/client_continuation_test.go llm/providers/tokenauth/codex.go llm/providers/tokenauth/codex_scope_test.go
git commit -m "feat(llm): plan Responses continuation from Resolved and the built body"
```

---

### Task 4: Agent tag branches, part A — surface, protocol, provider identity

**Files:**
- Modify: `agent/provider/profile.go` (scaffold accessors), `agent/session_set_model.go` (`unrepresentableContentKinds`), `agent/session_model_call.go` (`replayScope`, `builderFamily` deleted, `ModelAttemptMetadata.Protocol`, both `ThinkingAlwaysOn` injections, `providerWebSearchEnabled`), `agent/session.go` (`reapplyProviderSpecificTools`, `SetModel`, `applyModelRequestMetadata`, `appendAssistantTurn`), `agent/session_tool_registry.go` (`webSearchEnabled`), `agent/session_prompts.go` (section resolver provider), `agent/session_init.go` (`validateModelFallbackEntry`), `agent/sandbox/provider_web.go`, `agent/schema/turn.go` (`ResponseProtocol`), `llm/api_attempt.go` (`APIAttemptMeta.Protocol`), `llm/apilog/record.go` (`protocol` on the attempt record), `llm/providers/internal/protocolhttp/call.go` (`Prepare` sets `Protocol: res.Protocol`)
- Tests: the twelve `agent/*_test.go` files that assert on `BehaviorTag()` or build tag-keyed fixtures (`grep -l 'BehaviorTag(' agent/*_test.go`), `agent/sandbox/provider_web_test.go`, `agent/session_set_model_test.go`, `agent/session_model_call_*_test.go` (replay scope), `llm/apilog/*_test.go`, `llm/providers/internal/protocolhttp/*_test.go`

**Interfaces:**
- Consumes: Task 2's `Client.Registry()`/`Resolve`, `registry.Instance`.
- Produces (temporary until Task 7): `func (p *Profile) Surface() string`, `func (p *Profile) Protocol() string`, `func (p *Profile) ProviderID() string` on the old profile. Permanent: `schema.Turn.ResponseProtocol string` (`response_protocol`), `llm.APIAttemptMeta.Protocol string`, `apilog.APIAttemptRequest.Protocol string` (`protocol,omitempty`), `ModelAttemptMetadata.Protocol string`, `replayScope{Instance, Model, Protocol, InFlightFrom, protocolOf, canonicalModel}`, `unrepresentableContentKinds(protocol string)`, `(*Session).reapplyProviderSpecificTools(oldProfile, newProfile *provider.Profile)`.

The §7.5 rows this task moves: tool set / doc files (unchanged here: still keyed inside the old profile), prompt sections → `Surface`; the registered `web_search` function tool → `Protocol == google && Caps.WebSearch` (here `profile.SupportsWebSearch()`); `model_fallbacks` cross-provider refusal → `Surface` equality; `unrepresentableContentKinds` → `Protocol`; sandbox allowlist → `ProviderID`; `openAIPromptCacheSupported` → the request carries both fields and `ShapeRequest` gates them by `Fields` (Task 2); the `ThinkingAlwaysOn → medium` injection → deleted; replay scope → `Instance` + `Protocol` recorded on every turn.

- [ ] **Step 1: Scaffold accessors on the old profile (test first)**

`agent/provider/profile_registry_keys_test.go`:

```go
package provider

import (
	"testing"

	"primeradiant.com/evener/llm/providercfg"
	"primeradiant.com/evener/llm/registry"
)

func TestRegistryKeysFromBehaviorTag(t *testing.T) {
	cases := []struct{ typ, style, surface, protocol, providerID string }{
		{"openai", "responses", registry.SurfaceOpenAI, registry.ProtocolOpenAIResponses, "openai"},
		{"openai", "chat-completions", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "openai-compatible"},
		{"anthropic", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "anthropic"},
		{"google", "", registry.SurfaceGoogle, registry.ProtocolGoogle, "google"},
		{"minimax", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "minimax"},
		{"kimi-anthropic", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "kimi-for-coding"},
		{"openrouter-anthropic", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "openrouter"},
		{"openrouter", "", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "openrouter"},
		{"ollama", "", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "ollama"},
	}
	for _, tc := range cases {
		cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "inst", Type: providercfg.Type(tc.typ), APIStyle: providercfg.APIStyle(tc.style)}}}
		p, err := ResolveProfileFromConfig(cfg, "inst/some-model")
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.typ, tc.style, err)
		}
		if p.Surface() != tc.surface || p.Protocol() != tc.protocol || p.ProviderID() != tc.providerID {
			t.Fatalf("%s/%s: got %s %s %s", tc.typ, tc.style, p.Surface(), p.Protocol(), p.ProviderID())
		}
	}
}
```

Implementation in `agent/provider/profile.go` (import `primeradiant.com/evener/llm/registry`):

```go
// Surface, Protocol, and ProviderID derive the registry vocabulary from the
// behavior tag so the tag-keyed agent branches can move first (spec §7.5);
// the registry-backed Profile returns them from Resolved and deletes this
// table together with the behavior tag.
func (p *Profile) Surface() string {
	switch p.behaviorTag {
	case "openai":
		return registry.SurfaceOpenAI
	case "anthropic", "minimax", "kimi-anthropic", "openrouter-anthropic":
		return registry.SurfaceAnthropic
	case "google":
		return registry.SurfaceGoogle
	default:
		return registry.SurfaceGeneric
	}
}

// Protocol is the wire protocol the profile's instance speaks.
func (p *Profile) Protocol() string {
	switch p.behaviorTag {
	case "openai":
		return registry.ProtocolOpenAIResponses
	case "anthropic", "minimax", "kimi-anthropic", "openrouter-anthropic":
		return registry.ProtocolAnthropic
	case "google":
		return registry.ProtocolGoogle
	default:
		return registry.ProtocolOpenAIChat
	}
}

// ProviderID is the registry provider id behind the instance.
func (p *Profile) ProviderID() string {
	switch p.behaviorTag {
	case "kimi-anthropic":
		return "kimi-for-coding"
	case "openrouter-anthropic":
		return "openrouter"
	default:
		return p.behaviorTag
	}
}
```

Run `cd agent && go test ./provider/ -run TestRegistryKeysFromBehaviorTag` → PASS. Commit: `git add agent/provider/profile.go agent/provider/profile_registry_keys_test.go && git commit -m "feat(provider): surface, protocol, and provider id accessors ahead of the registry profile"`.

- [ ] **Step 2: Record the protocol on attempts and turns (test first)**

Add to `llm.APIAttemptMeta`: `Protocol string` (doc: "the wire protocol id of the resolved row; recorded as protocol on the attempt"), to `apilog.APIAttemptRequest`: `Protocol string \`json:"protocol,omitempty"\`` carried by the same code path that carries `EndpointFamily` (extend the existing apilog record test with the field), and in `protocolhttp.Prepare` set `Protocol: call.Res.Protocol` where the meta is built (extend `protocolhttp`'s attempt-record test: the recorded attempt carries `Protocol == res.Protocol`). Add `ResponseProtocol string \`json:"response_protocol,omitempty"\`` to `schema.Turn` next to `ResponseEndpointFamily`, `Protocol string` to `ModelAttemptMetadata`, fill it wherever `EndpointFamily` is filled from the attempt group, and set `ResponseProtocol: finalAttempt.Protocol` in `appendAssistantTurn`. Test in `agent`: a session over an `agenttest.FakeAdapter` records no protocol (overrides carry none) — assert the field is empty — and the schema round-trip test for `Turn` includes `response_protocol`.

Commit: `git commit -m "feat(llm,agent): record the wire protocol on API attempts and assistant turns"`.

- [ ] **Step 3: Move the branches (tests first, per site)**

For each site, first change the existing tests to the new key, run them to see them fail, then change the code:

1. `agent/session_set_model.go` — `unrepresentableContentKinds(protocol string)` (the table above keyed on `registry.Protocol*`: anthropic, google, openai-chat → document + audio; openai-responses → audio; else nil) and `unrepresentableHistoryKinds(history, protocol string)`; `SetModel` passes `nextProfile.Protocol()`. Delete `builderFamily`.
2. `agent/session_model_call.go` — `replayScope`:

```go
type replayScope struct {
	Instance     string // outgoing instance (req.Provider)
	Model        string // outgoing requested model (req.Model)
	Protocol     string // outgoing wire protocol; empty ⇒ no filtering
	InFlightFrom int
	// protocolOf resolves a stored turn's instance to the protocol it speaks
	// today, for turns written before ResponseProtocol existed; "" means the
	// instance is no longer configured and the turn is not eligible (spec §7.5).
	protocolOf     func(instance string) string
	canonicalModel func(string) string
}

func (rs replayScope) active() bool { return strings.TrimSpace(rs.Protocol) != "" }

func (rs replayScope) producerProtocol(t schema.Turn) string {
	if p := strings.TrimSpace(t.ResponseProtocol); p != "" {
		return p
	}
	if rs.protocolOf == nil {
		return ""
	}
	return rs.protocolOf(t.ResponseProvider)
}

func (rs replayScope) thinkingReplayEligible(t schema.Turn) bool {
	if strings.TrimSpace(t.ResponseProvider) == "" {
		return true
	}
	producer := rs.producerProtocol(t)
	switch rs.Protocol {
	case registry.ProtocolAnthropic:
		return producer == rs.Protocol && rs.Instance == t.ResponseProvider && rs.requestedModelMatches(t)
	case registry.ProtocolGoogle, registry.ProtocolOpenAIResponses:
		return producer == rs.Protocol && rs.Instance == t.ResponseProvider
	default:
		return true
	}
}

func (rs replayScope) webSearchReplayEligible(t schema.Turn) bool {
	if strings.TrimSpace(t.ResponseProvider) == "" {
		return true
	}
	return rs.producerProtocol(t) == rs.Protocol
}
```

   Construction at the model-call site: `replayScope{Instance: profile.ID(), Model: profile.Model(), Protocol: profile.Protocol(), InFlightFrom: inFlightFrom, protocolOf: s.instanceProtocol, canonicalModel: s.canonicalModelID}` with

```go
// instanceProtocol resolves an instance name to the protocol it speaks
// today; "" when it is no longer configured.
func (s *Session) instanceProtocol(name string) string {
	if s.client == nil {
		return ""
	}
	if inst, ok := s.client.Registry().Instance(name); ok {
		return inst.Protocol
	}
	return ""
}

// canonicalModelID canonicalizes a model ref through the registry so a
// requested alias and a provider-reported dated snapshot compare equal in
// the ResponseModel provenance fallback. Unknown refs compare by trimmed string.
func (s *Session) canonicalModelID(model string) string {
	trimmed := strings.TrimSpace(model)
	if s.client == nil {
		return trimmed
	}
	if res, err := s.client.Resolve(s.currentProfile().ID() + "/" + trimmed); err == nil && res.Model.ID != "" {
		return res.Model.ID
	}
	return trimmed
}
```

   Delete both `ThinkingAlwaysOn` branches (`buildModelRequest` and the fallback path): when `reasoningEffort == "" || !profile.SupportsReasoning()` the request carries no effort. `providerWebSearchEnabled` passes `profile.ProviderID()`.
3. `agent/session.go` — `reapplyProviderSpecificTools(oldProfile, newProfile *provider.Profile)`: `googleWebSearch := func(p *provider.Profile) bool { return p.Protocol() == registry.ProtocolGoogle && p.SupportsWebSearch() }`; register when `googleWebSearch(newProfile) && !googleWebSearch(oldProfile)`, remove when the reverse; `SetModel` passes the two profiles and `nextProfile.Protocol()` to the history preflight. `applyModelRequestMetadata`: when the session id is set, `PromptCacheKey = "evener-session-" + s.id` if empty and `PromptCacheRetention = "24h"` if empty, unconditionally (`ShapeRequest` drops what the row cannot send); delete `openAIModelSupports24hPromptCache` and `openAIModelFamilyMatch` and their tests; the existing prompt-cache tests move their assertions to a fake registered under a resolvable name whose row sets `Fields["prompt_cache_retention"]` (an injected `openai` instance with `Caps.Fields{"prompt_cache_retention": true}`) and one whose row does not (an injected `anthropic` instance) — the shaped request the fake receives carries or lacks the field accordingly.
4. `agent/session_tool_registry.go` — `webSearchEnabled: s.profile.Protocol() == registry.ProtocolGoogle && s.profile.SupportsWebSearch()`.
5. `agent/session_prompts.go` — `provider: s.profile.Surface()` (the shipped `tools.provider-openai_append.md.tmpl` keeps its name: `openai` is both the old tag and the surface).
6. `agent/sandbox/provider_web.go` — the map becomes `{"openai": true, "openai-codex": true, "anthropic": true, "google": true}` keyed by registry provider id (the `gemini` key is deleted); update its test table.
7. `agent/session_init.go` — `validateModelFallbackEntry`: for a slashed ref that is cross-instance, resolve it and refuse only when `fbProfile.Surface() != s.profile.Surface()`: `fmt.Errorf("model_fallbacks entry %q switches surface from %q to %q; cross-surface fallbacks are not supported because prompt/tool surfaces differ", fbModel, s.profile.Surface(), fbProfile.Surface())`; a same-surface cross-instance entry validates. Update `revalidateModelFallbacksLocked`'s doc and tests (a fallback from `openai` to a `work` instance whose surface is `openai` is kept; to an `anthropic` instance is dropped).

- [ ] **Step 4: Gate and commit**

```bash
for m in . llm agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add agent/session_set_model.go agent/session_model_call.go agent/session.go agent/session_tool_registry.go agent/session_prompts.go agent/session_init.go agent/sandbox/provider_web.go <the test files you changed>
git commit -m "refactor(agent): key surface, protocol, and provider decisions on registry vocabulary"
```

(Commit per site if the diff grows past ~600 lines; each commit must pass the agent package tests.)

---

### Task 5: Agent tag branches, part B — listings and catalog lookups on the registry

**Files:**
- Modify: `llm/client.go` (`CanServe`), `agent/provider/profile.go` (scaffold `WithResolved`), `agent/live_model_metadata.go`, `agent/session_set_model.go` (membership), `agent/session_init.go` (`captureModelAvailability`), `agent/internal/modelavailability/modelavailability.go` (+ tests), `agent/subagent_model_selection.go`, `agent/session_tools.go` (vision route effort lookup), `agent/session_model_call.go` (fallback effort clamp), `agent/internal/cheapmodel/caller.go` (`serves`), `agent/internal/agenttest/agenttest.go` (`FakeAdapter.LiveModels` seam)
- Tests: `agent/live_model_metadata_test.go`, `agent/session_set_model_test.go`, `agent/subagent_model_selection_test.go`, `agent/session_tools_vision_*_test.go`, `agent/internal/cheapmodel/caller_test.go`, the eight `agent/*_test.go` files that script `ListModels` on fakes (`grep -l ListModels agent/*_test.go`)

**Interfaces:**
- Consumes: Task 2's `Client.Models`, `Client.Resolve`, `Client.Registry`, `LiveModelLister`; Task 1's `Registry.FindModel` (existing), `Caps.ReasoningDisabled`, `Caps.EffortCapable`.
- Produces: `func (c *Client) CanServe(provider, model string) bool` (an override under the name, or a successful `Resolve`); `func (p *Profile) WithResolved(res registry.Resolved) *Profile` (temporary on the old profile; Task 7 keeps the name on the new one); `liveModelEnumeration{listing llm.ModelListing; err error}`; `liveModelFor(models []registry.Resolved, model string) (registry.Resolved, bool)`; `validateModelSwitchMembership(client *llm.Client, profile *provider.Profile, listing llm.ModelListing) error`; `modelavailability.Capture(parent context.Context, providers []string, requiredProvider string, fetch func(context.Context, string) ([]string, error), budget time.Duration) Snapshot`; `agenttest.FakeAdapter.LiveModelsFunc func(ctx context.Context) ([]registry.Model, error)`.

- [ ] **Step 1: `CanServe` and the fake's listing seam (tests first)**

`llm/client_registry_test.go` — add:

```go
func TestClientCanServe(t *testing.T) {
	srv, _ := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	c.Register(&recordingAdapter{name: "fake"})
	if !c.CanServe("fake", "anything") || !c.CanServe("openai", "never-heard-of-it") {
		t.Fatal("overrides and synthesized rows are served")
	}
	if c.CanServe("nope", "x") || c.CanServe("openai-codex", "not-on-the-allowlist") {
		t.Fatal("unknown instances and Codex ids off the allowlist are not (spec §7.3)")
	}
}
```

Implementation:

```go
// CanServe reports whether provider/model would dispatch: an override is
// registered under the name, or the reference resolves (spec §7.3: every
// model resolves except an id off the Codex allowlist).
func (c *Client) CanServe(provider, model string) bool {
	provider = normalizeProviderName(provider)
	if _, ok := c.overrides[provider]; ok {
		return true
	}
	_, err := c.Resolve(provider + "/" + model)
	return err == nil
}
```

`agent/internal/agenttest/agenttest.go` — add `LiveModelsFunc func(ctx context.Context) ([]registry.Model, error)` to `FakeAdapter` and:

```go
// LiveModels implements llm.LiveModelLister when a test scripts a listing.
func (a *FakeAdapter) LiveModels(ctx context.Context) ([]registry.Model, error) {
	if a.LiveModelsFunc == nil {
		return nil, errors.New("fake adapter does not list models")
	}
	return a.LiveModelsFunc(ctx)
}
```

(The package-`agent` `fakeAdapter` in `session_test.go` and `snapshotFakeAdapter` get the same optional func; tests that scripted `ListModels` returning `[]llm.ModelInfo` now script `[]registry.Model{{ID: "...", Caps: registry.Caps{ContextWindow: new(200000), Tools: new(true)}}}`.)

- [ ] **Step 2: `WithResolved` on the old profile (test first)**

`agent/provider/profile_with_resolved_test.go`:

```go
func TestWithResolvedOverlaysLiveFacts(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.5")
	res := registry.Resolved{Caps: registry.Caps{ContextWindow: new(272000), EffortValues: []string{"low", "high"}, Reasoning: new(true), ThinkingAlwaysOn: new(true), WebSearch: new(false)}}
	q := p.WithResolved(res)
	if q.ContextWindowSize() != 272000 || strings.Join(q.ReasoningEffortLevels(), ",") != "low,high" || !q.ThinkingAlwaysOn() || q.SupportsWebSearch() {
		t.Fatalf("overlay: %d %v %v %v", q.ContextWindowSize(), q.ReasoningEffortLevels(), q.ThinkingAlwaysOn(), q.SupportsWebSearch())
	}
	if p.ContextWindowSize() == 272000 {
		t.Fatal("WithResolved clones")
	}
	for _, td := range q.ToolDefinitions() {
		if td.Name == "task_list" && !strings.Contains(fmt.Sprint(td.Parameters), "high") {
			t.Fatal("task_list effort enum follows the ladder")
		}
	}
	if r := p.WithResolved(registry.Resolved{Caps: registry.Caps{Reasoning: new(false)}}); r.SupportsReasoning() || len(r.ReasoningEffortLevels()) != 0 {
		t.Fatal("reasoning = false clears the ladder")
	}
}
```

Implementation (old profile):

```go
// WithResolved returns a copy of the profile carrying the facts of a fresh
// registry resolution — after a live listing was applied, the registry's
// merged caps are the truth (spec §5: live facts never beat the user layer,
// so nothing here consults providers.toml).
func (p *Profile) WithResolved(res registry.Resolved) *Profile {
	if p == nil {
		return nil
	}
	clone := *p
	caps := res.Caps
	if caps.ContextWindow != nil && *caps.ContextWindow > 0 {
		clone.contextWindow = *caps.ContextWindow
	}
	if caps.Reasoning != nil {
		clone.reasoning = *caps.Reasoning
	}
	switch {
	case caps.ReasoningDisabled():
		clone.effortLevels = []string{}
	case len(caps.EffortValues) > 0:
		clone.effortLevels = append([]string(nil), caps.EffortValues...)
	}
	defs := append([]llm.ToolDefinition(nil), clone.toolDefs...)
	for i := range defs {
		if defs[i].Name == "task_list" {
			defs[i] = tool.DefTaskList(clone.effortLevels)
		}
	}
	clone.toolDefs = defs
	clone.thinkingAlwaysOn = registry.BoolValue(caps.ThinkingAlwaysOn)
	if caps.WebSearch != nil {
		clone.webSearch = *caps.WebSearch
	}
	return &clone
}
```

- [ ] **Step 3: Move the listing consumers (tests first, per site)**

1. `agent/live_model_metadata.go`:

```go
type liveModelEnumeration struct {
	listing llm.ModelListing
	err     error
}

func fillLiveModelMetadata(ctx context.Context, client *llm.Client, profile *provider.Profile) (*provider.Profile, liveModelEnumeration) {
	if client == nil || profile == nil {
		return profile, liveModelEnumeration{err: errors.New("live model metadata inputs are nil")}
	}
	listing, err := client.Models(ctx, profile.ID())
	if err != nil {
		return profile, liveModelEnumeration{err: err}
	}
	if res, err := client.Resolve(profile.ID() + "/" + profile.Model()); err == nil {
		profile = profile.WithResolved(res)
	}
	return profile, liveModelEnumeration{listing: listing}
}

func liveModelFor(models []registry.Resolved, model string) (registry.Resolved, bool) { /* exact, then trimmed, then EqualFold on ModelID, as liveModelInfoFor did */ }
```

   `resolveLiveModelProfileValidated` passes `enumeration.listing` to the membership check.
2. `agent/session_set_model.go`:

```go
func validateModelSwitchMembership(client *llm.Client, profile *provider.Profile, listing llm.ModelListing) error {
	if client == nil || profile == nil {
		return nil
	}
	if !client.CanServe(profile.ID(), profile.Model()) {
		_, err := client.Resolve(profile.ID() + "/" + profile.Model())
		return err
	}
	if !listing.Live {
		return nil // registry-only listing: every id resolves (spec §7.3, §8.1)
	}
	if _, ok := liveModelFor(listing.Models, profile.Model()); ok {
		return nil
	}
	return fmt.Errorf("model %s is not available from instance %s (available: %s)", profile.Model(), profile.ID(), formatModelAlternatives(listing.Models))
}
```

   `formatModelAlternatives(models []registry.Resolved)` lists sorted `ModelID`s capped at 20 with `+N more`; `modelSwitchVisible` and the `openrouter-anthropic` exception are deleted (the registry hides live `Tools = false` rows before they reach here).
3. `agent/session_init.go` `captureModelAvailability`: the fetch closure returns ids — `ids := make([]string, 0, len(listing.Models)); for _, m := range listing.Models { ids = append(ids, m.ModelID) }` — from `selectedModels.listing` for the current instance and `s.client.Models(ctx, name)` otherwise; `modelavailability.Capture` loses the `visible` callback and `boundedModelIDs` takes `[]string` (its safety bounds and sorting stay; update its tests to pass ids).
4. `agent/subagent_model_selection.go` — `resolvePluginAgentModel`:

```go
func (s *Session) resolvePluginAgentModel(ctx context.Context, base *provider.Profile, requested string) pluginAgentModelResolution {
	ref, reason := resolvePluginAgentRef(s.client.Registry(), base, requested)
	if reason != "" {
		return pluginAgentModelResolution{reason: reason}
	}
	candidate, crossInstance, err := s.resolveProfileForRef(base, ref)
	if err != nil {
		return pluginAgentModelResolution{reason: "unresolvable"}
	}
	if crossInstance {
		candidate = candidate.WithCommunicateOverridesFrom(base)
	}
	if candidate.ID() == base.ID() && candidate.Model() == base.Model() {
		return pluginAgentModelResolution{profile: base}
	}
	listCtx, cancel := context.WithTimeout(ctx, liveModelMetadataTimeout)
	defer cancel()
	listing, err := s.client.Models(listCtx, candidate.ID())
	if err != nil {
		return pluginAgentModelResolution{reason: "unverified"}
	}
	if listing.Live {
		if _, ok := liveModelFor(listing.Models, candidate.Model()); !ok {
			return pluginAgentModelResolution{reason: "unavailable"}
		}
	}
	if res, err := s.client.Resolve(candidate.ID() + "/" + candidate.Model()); err == nil {
		candidate = candidate.WithResolved(res)
	}
	return pluginAgentModelResolution{profile: candidate}
}

// resolvePluginAgentRef is spec §7.5's plugin-agent rule: instance/model
// resolves directly; a bare id resolves to the session's instance when it
// serves the id, else to the highest-ranked serving instance (Registry.FindModel),
// else it is unavailable.
func resolvePluginAgentRef(r *registry.Registry, base *provider.Profile, requested string) (ref, reason string) {
	requested = strings.TrimSpace(requested)
	if inst, _, ok := strings.Cut(requested, "/"); ok {
		if _, known := r.Instance(inst); known {
			return requested, ""
		}
	}
	refs := r.FindModel(requested)
	for _, ref := range refs {
		if ref.Instance == base.ID() {
			return requested, ""
		}
	}
	if len(refs) > 0 {
		return refs[0].String(), ""
	}
	return "", "unavailable"
}
```

   (`resolvePluginAgentCatalogRef` and its `catalog.ResolveAlias` path are deleted; `selectSubagentModel` is unchanged. Note the second `strings.Cut` case: a slashed ref whose prefix is not an instance — `anthropic/claude-opus-5` on an OpenRouter session — falls through to `FindModel`, which finds it on `openrouter` when the row exists.)
5. `agent/session_tools.go` — the vision route helpers take the session and resolve `provider/model` through `s.client.Resolve`: reasoning supported ⇔ `!res.Caps.ReasoningDisabled()`, levels ⇔ `res.Caps.EffortValues` (the profile's own accessors when the route is the session's own instance and model).
6. `agent/session_model_call.go` fallback clamp: `fbLevels := fbProfile.ReasoningEffortLevels()` only; delete the two `EmbeddedModelCatalog` blocks and the calls to `CatalogEffortFallbackEligible` (the method itself dies with the old profile in Task 7).
7. `agent/internal/cheapmodel/caller.go`: `serves` becomes `!refused && c.client.CanServe(r.provider, r.model)`; its doc comment names `CanServe`.

- [ ] **Step 4: Gate and commit**

```bash
for m in . llm agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add llm/client.go llm/client_registry_test.go agent/internal/agenttest/agenttest.go agent/provider/profile.go agent/provider/profile_with_resolved_test.go agent/live_model_metadata.go agent/session_set_model.go agent/session_init.go agent/internal/modelavailability/modelavailability.go agent/subagent_model_selection.go agent/session_tools.go agent/session_model_call.go agent/internal/cheapmodel/caller.go <changed tests>
git commit -m "refactor(agent): list, verify, and canonicalize models through the registry"
```

---

### Task 6: `cmdutil` registry loaders, the credentials path, and `EVENER_CREDENTIALS_CONFIG`

**Files:**
- Create: `cmdutil/registry.go`, `cmdutil/registry_test.go`
- Modify: `envvars/envvars.go` (`EVENERCredentialsConfig`), `envvars/envvars_test.go` (roster/visibility tests, if they enumerate public vars)

**Interfaces:**
- Consumes: Task 1's `Registry.StateRoot`; `registry.Load` options; `credentials.LoadStore`; `tokenauth.DefaultCodex.StateDir`, `tokenauth.ClientVersion`; `buildinfo.Version`.
- Produces:
  - `envvars.EVENERCredentialsConfig` (`EVENER_CREDENTIALS_CONFIG`, Public: "Path to credentials.toml; unset means the sibling of providers.toml.").
  - `func ProvidersConfigPath() (path string, noUserLayer bool)`; `func CredentialsPath() string`.
  - `type StoreCredentialSource struct{ Store *credentials.Store }` implementing `registry.CredentialSource`.
  - `func LoadRegistry(opts ...registry.Option) (*registry.Registry, *credentials.Store, error)` — credentials store from `CredentialsPath()`, state root `DefaultStateRoot()`, then the caller's options (tests pass `registry.WithOffline(true)`, `registry.WithoutCache()`; the CLI passes `WithOffline(true)`; sessions pass nothing so a stale cache refreshes in the background per §6.4). An old-schema file is returned as `registry.ErrOldSchema` (wrapped) for the caller to decide.
  - `func NewRegistryClient(r *registry.Registry, stateDir string) *llm.Client` — sets `tokenauth.DefaultCodex.StateDir = r.StateRoot()` and `tokenauth.ClientVersion = buildinfo.Version()`, imports `llm/providers/all` for its side effect, returns `llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(stateDir))`.

The old `LoadClient`/`LoadProviderConfig*` stay untouched until Task 7 replaces them.

- [ ] **Step 1: Write the failing tests**

`cmdutil/registry_test.go`:

```go
package cmdutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

func isolateRoots(t *testing.T) (configRoot, stateRoot string) {
	t.Helper()
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", t.TempDir())
	os.Unsetenv("EVENER_PROVIDERS_CONFIG")
	os.Unsetenv("EVENER_CREDENTIALS_CONFIG")
	return filepath.Join(configHome, "evener"), filepath.Join(stateHome, "evener")
}

func TestProvidersConfigPathTriState(t *testing.T) {
	configRoot, _ := isolateRoots(t)
	if path, none := ProvidersConfigPath(); none || path != filepath.Join(configRoot, "providers.toml") {
		t.Fatalf("unset: %q %v", path, none)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")
	if path, none := ProvidersConfigPath(); !none || path != "" {
		t.Fatalf("present and empty means no user layer (spec §10): %q %v", path, none)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "/x/providers.toml")
	if path, none := ProvidersConfigPath(); none || path != "/x/providers.toml" {
		t.Fatalf("set: %q %v", path, none)
	}
}

func TestCredentialsPathPrecedence(t *testing.T) {
	configRoot, _ := isolateRoots(t)
	if got := CredentialsPath(); got != filepath.Join(configRoot, "credentials.toml") {
		t.Fatalf("default: %q", got)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "/x/providers.toml")
	if got := CredentialsPath(); got != "/x/credentials.toml" {
		t.Fatalf("sibling of the providers path: %q", got)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")
	if got := CredentialsPath(); got != filepath.Join(configRoot, "credentials.toml") {
		t.Fatalf("no user layer falls back to the config root: %q", got)
	}
	t.Setenv("EVENER_CREDENTIALS_CONFIG", "/y/creds.toml")
	if got := CredentialsPath(); got != "/y/creds.toml" {
		t.Fatalf("explicit wins: %q", got)
	}
}

func TestLoadRegistryUsesStoreAndUserLayer(t *testing.T) {
	configRoot, stateRoot := isolateRoots(t)
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "providers.toml"), []byte("default = \"work\"\n[providers.work]\nbase = \"openai\"\nbase_url = \"https://gw.example.com/v1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "credentials.toml"), []byte("schema = 1\n[providers.work]\napi_key = \"from-store\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, store, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if store == nil || r.StateRoot() != stateRoot {
		t.Fatalf("store %v, state root %q", store, r.StateRoot())
	}
	res, err := r.Resolve("work/gpt-5.5")
	if err != nil || res.Credential.Source != "store" || res.Credential.Value != "from-store" {
		t.Fatalf("the store's file layer is looked up by instance name (spec §10): %v %+v", err, res.Credential)
	}
	if !strings.Contains(r.UserLayerNote(), "providers.toml") {
		t.Fatalf("user layer note: %q", r.UserLayerNote())
	}
}

func TestLoadRegistryReportsOldSchema(t *testing.T) {
	configRoot, _ := isolateRoots(t)
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "providers.toml"), []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if !errors.Is(err, registry.ErrOldSchema) {
		t.Fatalf("want ErrOldSchema, got %v", err)
	}
}

func TestLoadRegistryHonorsEmptyProvidersConfig(t *testing.T) {
	isolateRoots(t)
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")
	r, _, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.UserLayerNote(), "EVENER_PROVIDERS_CONFIG is empty") {
		t.Fatalf("tri-state note: %q", r.UserLayerNote())
	}
}

func TestNewRegistryClientWiresTheProcessSeams(t *testing.T) {
	_, stateRoot := isolateRoots(t)
	r, _, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if err != nil {
		t.Fatal(err)
	}
	c := NewRegistryClient(r, t.TempDir())
	if c == nil || c.Registry() != r {
		t.Fatal("client carries the registry")
	}
	if tokenauth.DefaultCodex.StateDir != stateRoot || tokenauth.ClientVersion != buildinfo.Version() {
		t.Fatalf("seams: %q %q", tokenauth.DefaultCodex.StateDir, tokenauth.ClientVersion)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmdutil/ -run 'TestProvidersConfigPath|TestCredentialsPath|TestLoadRegistry|TestNewRegistryClient' 2>&1 | head`
Expected: compile errors.

- [ ] **Step 3: Implement**

`envvars/envvars.go` — next to `EVENERProvidersConfig`:

```go
	EVENERCredentialsConfig           = Var{Name: "EVENER_CREDENTIALS_CONFIG", Summary: "Path to credentials.toml; unset means the sibling of providers.toml.", Visibility: Public}
```

`cmdutil/registry.go`:

```go
package cmdutil

import (
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all" // register every protocol and authenticator
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

// ProvidersConfigPath is the user layer's location under the tri-state
// rule (spec §10): unset → <config-root>/providers.toml; present and empty
// → no user layer at all; set → that path.
func ProvidersConfigPath() (path string, noUserLayer bool) {
	v, ok := envvars.EVENERProvidersConfig.LookupEnv()
	switch {
	case ok && strings.TrimSpace(v) == "":
		return "", true
	case ok:
		return v, false
	}
	return filepath.Join(DefaultConfigRoot(), "providers.toml"), false
}

// CredentialsPath is credentials.toml's location: EVENER_CREDENTIALS_CONFIG
// when set, else the sibling of the providers path, else
// <config-root>/credentials.toml (spec §10).
func CredentialsPath() string {
	if v, ok := envvars.EVENERCredentialsConfig.LookupEnv(); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if path, none := ProvidersConfigPath(); !none {
		return filepath.Join(filepath.Dir(path), "credentials.toml")
	}
	return filepath.Join(DefaultConfigRoot(), "credentials.toml")
}

// StoreCredentialSource exposes credentials.toml's file layer to the
// registry: entries are looked up by instance name only (spec §10).
type StoreCredentialSource struct{ Store *credentials.Store }

// Lookup implements registry.CredentialSource.
func (s StoreCredentialSource) Lookup(name string) (string, bool) {
	if s.Store == nil {
		return "", false
	}
	v, ok := s.Store.Get(name)
	return v, ok && v != ""
}

// LoadRegistry loads the registry the way every binary does: the
// credentials store from CredentialsPath, the state root from
// DefaultStateRoot, the user layer per the tri-state, then the caller's
// options. An old-schema providers.toml comes back as registry.ErrOldSchema
// (wrapped); the CLI exits with it and the hub degrades (spec §10, §14.1).
func LoadRegistry(opts ...registry.Option) (*registry.Registry, *credentials.Store, error) {
	store, err := credentials.LoadStore(CredentialsPath())
	if err != nil {
		return nil, nil, fmt.Errorf("credentials: %w", err)
	}
	all := append([]registry.Option{registry.WithCredentials(StoreCredentialSource{Store: store}), registry.WithStateRoot(DefaultStateRoot())}, opts...)
	r, err := registry.Load(all...)
	if err != nil {
		return nil, store, err
	}
	return r, store, nil
}

// NewRegistryClient builds the client every binary uses and wires the two
// process-wide seams from one state root and one build version (spec §9.5):
// the Codex authenticator reads OAuth records under the registry's state
// root and announces the build in its User-Agent.
func NewRegistryClient(r *registry.Registry, stateDir string) *llm.Client {
	tokenauth.DefaultCodex.StateDir = r.StateRoot()
	tokenauth.ClientVersion = buildinfo.Version()
	return llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(stateDir))
}
```

(`credentials.Store.Get` currently returns `(string, Source)`; until Task 11 rewrites the store, `Lookup` must check the file layer only: use `s.Store.Layers(name)` for `hasFile` exactly as `cmd/evener/models.go`'s `storeCredentialSource` does today, and delete that type from `models.go` in favour of this one.)

- [ ] **Step 4: Run, gate, commit**

```bash
go test ./cmdutil/ ./cmd/evener/ 2>&1 | tail -3
(cd envvars && go test ./...)
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add envvars/envvars.go cmdutil/registry.go cmdutil/registry_test.go cmd/evener/models.go
git commit -m "feat(cmdutil): registry loader, credentials path, and EVENER_CREDENTIALS_CONFIG"
```

---

### Task 7: `Profile` over `Resolved`, and every loader on the registry

**Files:**
- Rewrite: `agent/provider/profile.go`, `agent/provider/resolve.go`; create `agent/provider/embedded.go`
- Rewrite tests: `agent/provider/*_test.go` (the catalog, openrouter, instance-models, and `ResolveProfileFromConfig` tests are replaced by registry-backed ones; `profile_overrides_test.go`, `cheap_model_ref_test.go` keep their subjects), `agent/profile_testhelpers_test.go`, `agent/testkit_test.go`
- Modify: `cmdutil/load_client.go` (`LoadClient(stateDir string) (*llm.Client, error)`, `LoadClientAt(path, stateDir string)`, `ResolveProfile`, `BuildResolveProfile`), `cmdutil/cmdutil.go` (delete `isOpenAICompatTag`, `ResolveProfileWithLiveWindow`, `instanceConfiguresContextWindow`, `instanceEndpoint`, `ResolveProfileForProvider`, `queryModelContextWindow*`, `lookupProviderEnv`; `ListModelsFunc` on `client.Models`; `ModelDescriptorFromResolved` replaces `ModelDescriptorFromInfo`), `cmd/evener/run.go`, `cmd/evener/serve.go`, `cmd/evener/main.go` (drop the `openaiprovider.ClientVersion` line and import), `cmd/evener/internal/launchcheck/launchcheck.go` (+ tests), `cmd/llmcall/main.go` (+ tests), `tools/tool-fluency/cmd/evener-fluency/main.go`, `agent/core_tool_names.go`, `agent/internal/contextmgr/context_manager.go` (0 window = unknown), `cmd/evener-hub/app_models.go` (`fetchLiveModels` on `client.Models`), `cmd/evener-hub/app_credentials.go` (`loadCredentialTestClient` returns `(credentialProbeClient, providercfg.Config, error)` by loading the file itself — compile-preserving until Task 9)
- Delete: `agent/provider/catalog_internal_test.go`, `openai_catalog_resolution_test.go`, `fuzz_ap_openrouter_resolve_test.go`, `instance_models_test.go`, `resolve_fuzz_test.go` (replace `resolve_test.go` with registry cases), the Task 4/5 scaffold tests `profile_registry_keys_test.go` and `profile_with_resolved_test.go` (their subjects become the real accessors — fold the cases into `profile_test.go`)

**Interfaces:**
- Consumes: Tasks 1–6.
- Produces:
  - `func Resolve(r *registry.Registry, ref string) (*Profile, error)`; `func FromResolved(res registry.Resolved, r *registry.Registry) *Profile` (nil `r` → `EmbeddedRegistry()`); `func EmbeddedRegistry() *registry.Registry` (returns `llm.EmbeddedRegistry()`, the process-wide one Task 2 introduced); `func NewOpenAIProfile(model string) *Profile` (`openai/<model>` on the embedded registry; panics only on a registry load failure).
  - `Profile` methods (final): `ID`, `Model`, `Resolved`, `Surface`, `Protocol`, `ProviderID`, `ToolDefinitions`, `ToolNameMap`, `SupportsParallelToolCalls` (true), `ContextWindowSize` (override, else `Caps.ContextWindow`, else 0 = unknown), `MaxOutputTokens` (`Caps.MaxOutputTokens` or 0), `ProjectDocFiles`, `ProviderOptions` (protocol-keyed extras), `SupportsReasoning` (`!Caps.ReasoningDisabled()`), `ReasoningEffortLevels` (`Caps.EffortValues`, nil when disabled), `SupportsStreaming` (true), `SupportsWebSearch` (`Caps.WebSearch`), `DefaultCommandTimeoutMS` (120000), `KnowledgeCutoff` (`Caps.KnowledgeCutoff`), `Cost() *registry.Cost`, `InputModalities`, `Warnings`, `CheapModel` (configured, else `Resolved.CheapModel`, else the model), `ConfiguredCheapModel`, `CheapProvider`, `CheapModelRef`, `CheapModelRefString`, `WithModel`, `WithResolved`, `CrossProviderRef`, `WithCommunicateOverridesFrom`. Deleted: `BehaviorTag`, `ThinkingAlwaysOn`, `WithLiveModelInfo`, `WithAdvertisedModelInfo`, `CatalogEffortFallbackEligible`, `EffortLevelsConfigured`, `ResolveProfileFromConfig`, `WithProviderID`.
  - `cmdutil.LoadClient(stateDir string) (*llm.Client, error)`; `cmdutil.LoadClientAt(path, stateDir string) (*llm.Client, error)` (`registry.WithConfigPath(path)`); `cmdutil.ResolveProfile(client *llm.Client, ref string) (*provider.Profile, error)`; `cmdutil.BuildResolveProfile(client *llm.Client) func(string) (*provider.Profile, error)`; `cmdutil.ModelDescriptorFromResolved(res registry.Resolved) appwire.ModelDescriptor`; `cmdutil.ListModelsFunc(client, instance)`.
  - `agent/profile_testhelpers_test.go`: `testRegistry(t)` (injected instances `anthropic`, `google`, `minimax`, `kimi-for-coding`, `openrouter`, `ollama`, `work` (base openai, chat, generic), `orclaude` (the §14.1 recipe) plus any name a test registers an adapter under), `testProfile(instance, model string, contextWindow int)`, `testOpenAICompatProfile(id, model string, contextWindow int)`, `newAnthropicProfile`, `newGeminiProfile`, `newMiniMaxProfile`, `newKimiAnthropicProfile` (→ `kimi-for-coding`), `newOpenRouterAnthropicProfile` (→ `orclaude`), `newOpenAICompatProfile(id, model, _)`.

- [ ] **Step 1: Write the profile tests against the registry**

Replace `agent/provider/profile_test.go` with registry-backed cases (keep every existing assertion whose subject survives — tool definitions per surface, tool name maps, doc files, cheap-model routing, `WithModel` carry-overs, communicate overrides — re-pointed at `Resolve`). New cases that pin the §7.5 contract:

```go
package provider

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func fixtureRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(), registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"anthropic":  {APIKey: "k"},
			"google":     {APIKey: "k"},
			"openrouter": {APIKey: "k"},
			"work":       {Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric, APIKey: "k", Transport: registry.Transport{BaseURL: "https://gw.example.com/v1"}, DefaultModel: "glm-5", CheapModel: "glm-5-flash"},
			"orclaude":   {Base: "openrouter", Protocol: registry.ProtocolAnthropic, APIKey: "k", Models: map[string]registry.Model{"minimax/*": {Surface: registry.SurfaceAnthropic}}},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

func TestResolveProfileSurfaceConventions(t *testing.T) {
	r := fixtureRegistry(t)
	cases := []struct {
		ref, surface, protocol string
		docs                   []string
		shellTool              string
		webSearchTool          bool
	}{
		{"openai/gpt-5.5", registry.SurfaceOpenAI, registry.ProtocolOpenAIResponses, []string{"AGENTS.md", ".codex/instructions.md"}, "exec_command", false},
		{"anthropic/claude-opus-5", registry.SurfaceAnthropic, registry.ProtocolAnthropic, []string{"CLAUDE.md", "AGENTS.md"}, "", false},
		{"google/gemini-3-pro", registry.SurfaceGoogle, registry.ProtocolGoogle, []string{"GEMINI.md", "AGENTS.md"}, "run_shell_command", true},
		{"work/glm-5", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, []string{"AGENTS.md"}, "", false},
		{"orclaude/minimax/minimax-m3", registry.SurfaceAnthropic, registry.ProtocolAnthropic, []string{"CLAUDE.md", "AGENTS.md"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			p, err := Resolve(r, tc.ref)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if p.Surface() != tc.surface || p.Protocol() != tc.protocol {
				t.Fatalf("surface/protocol: %s %s", p.Surface(), p.Protocol())
			}
			if strings.Join(p.ProjectDocFiles(), ",") != strings.Join(tc.docs, ",") {
				t.Fatalf("docs: %v", p.ProjectDocFiles())
			}
			if got := p.ToolNameMap()["shell"]; got != tc.shellTool {
				t.Fatalf("shell tool name: %q", got)
			}
			hasWebSearch := false
			for _, td := range p.ToolDefinitions() {
				if td.Name == "web_search" {
					hasWebSearch = true
				}
			}
			if hasWebSearch != tc.webSearchTool {
				t.Fatalf("web_search function tool advertised: %v (google protocol + WebSearch only)", hasWebSearch)
			}
		})
	}
}

func TestProfileFactsComeFromCaps(t *testing.T) {
	p, err := Resolve(fixtureRegistry(t), "anthropic/claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	res := p.Resolved()
	if res.Caps.ContextWindow == nil || p.ContextWindowSize() != *res.Caps.ContextWindow {
		t.Fatalf("context window: %d vs %+v", p.ContextWindowSize(), res.Caps.ContextWindow)
	}
	if res.Caps.MaxOutputTokens == nil || p.MaxOutputTokens() != *res.Caps.MaxOutputTokens {
		t.Fatalf("max output: %d", p.MaxOutputTokens())
	}
	if !p.SupportsReasoning() || strings.Join(p.ReasoningEffortLevels(), ",") != strings.Join(res.Caps.EffortValues, ",") {
		t.Fatalf("effort ladder: %v", p.ReasoningEffortLevels())
	}
	if p.Cost() == nil || p.KnowledgeCutoff() == "" || len(p.InputModalities()) == 0 {
		t.Fatalf("cost/cutoff/modalities: %v %q %v", p.Cost(), p.KnowledgeCutoff(), p.InputModalities())
	}
	if p.CheapModel() == "" || p.CheapModel() == p.Model() {
		t.Fatalf("anthropic carries a curated cheap_model: %q", p.CheapModel())
	}
	unknown, err := Resolve(fixtureRegistry(t), "anthropic/claude-new-thing")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.ContextWindowSize() != 0 || len(unknown.Warnings()) == 0 {
		t.Fatalf("an unknown model has no window and a warning (spec §7.3): %d %v", unknown.ContextWindowSize(), unknown.Warnings())
	}
	if !unknown.SupportsReasoning() {
		t.Fatal("unknown reasoning is not disabled reasoning")
	}
}

func TestProfileProviderOptionsByProtocol(t *testing.T) {
	r := fixtureRegistry(t)
	openai, _ := Resolve(r, "openai/gpt-5.5")
	if opts, _ := openai.ProviderOptions()[registry.ProtocolOpenAIResponses].(map[string]any); opts["parallel_tool_calls"] != true {
		t.Fatalf("responses extras: %+v", openai.ProviderOptions())
	}
	google, _ := Resolve(r, "google/gemini-3-pro")
	if opts, _ := google.ProviderOptions()[registry.ProtocolGoogle].(map[string]any); opts["safetySettings"] == nil {
		t.Fatalf("google extras: %+v", google.ProviderOptions())
	}
	anthropic, _ := Resolve(r, "anthropic/claude-opus-5")
	if anthropic.ProviderOptions() != nil {
		t.Fatalf("anthropic sends no extras; caps carry max_tokens and betas: %+v", anthropic.ProviderOptions())
	}
}

func TestWithModelAndCrossProviderRef(t *testing.T) {
	r := fixtureRegistry(t)
	or, _ := Resolve(r, "openrouter/openai/gpt-5.5")
	if or.CrossProviderRef("anthropic/claude-opus-5") {
		t.Fatal("a namespaced id the instance serves stays on the instance")
	}
	if !or.CrossProviderRef("anthropic/model-nobody-has") {
		t.Fatal("an id the instance does not serve, under another instance's name, is cross-instance")
	}
	if or.CrossProviderRef("openrouter/anthropic/claude-opus-5") {
		t.Fatal("a redundant self-prefix is not cross-instance")
	}
	switched := or.WithModel("openrouter/anthropic/claude-opus-5")
	if switched.ID() != "openrouter" || switched.Model() != "anthropic/claude-opus-5" || switched.Surface() != registry.SurfaceAnthropic {
		t.Fatalf("self-prefix strip + re-resolve: %s/%s %s", switched.ID(), switched.Model(), switched.Surface())
	}
	ant, _ := Resolve(r, "anthropic/claude-opus-5")
	ant = WithCheapModel(ant, "claude-haiku-4-5")
	next := ant.WithModel("claude-sonnet-4-5[1m]")
	if next.ContextWindowSize() != 1000000 || next.CheapModel() != "claude-haiku-4-5" || next.ID() != "anthropic" {
		t.Fatalf("WithModel re-resolves caps and keeps routing: %d %q %s", next.ContextWindowSize(), next.CheapModel(), next.ID())
	}
	kept := ant.WithModel("google/gemini-3-pro")
	if kept.ID() != "anthropic" || kept.Model() != "google/gemini-3-pro" {
		t.Fatalf("a cross-instance ref is the session resolver's job; WithModel keeps it verbatim: %s/%s", kept.ID(), kept.Model())
	}
}

func TestWithResolvedReplacesFacts(t *testing.T) {
	p, _ := Resolve(fixtureRegistry(t), "work/glm-5")
	res := p.Resolved()
	res.Caps.ContextWindow = new(272000)
	res.Caps.EffortValues = []string{"low", "high"}
	q := p.WithResolved(res)
	if q.ContextWindowSize() != 272000 || strings.Join(q.ReasoningEffortLevels(), ",") != "low,high" || p.ContextWindowSize() == 272000 {
		t.Fatalf("WithResolved clones with the new record: %d %v", q.ContextWindowSize(), q.ReasoningEffortLevels())
	}
	if WithContextWindow(q, 4096).ContextWindowSize() != 4096 {
		t.Fatal("WithContextWindow still overrides")
	}
}

func TestNewOpenAIProfileUsesTheEmbeddedRegistry(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.5")
	if p.ID() != "openai" || p.Model() != "gpt-5.5" || p.Surface() != registry.SurfaceOpenAI || p.ContextWindowSize() == 0 {
		t.Fatalf("%s/%s %s %d", p.ID(), p.Model(), p.Surface(), p.ContextWindowSize())
	}
	if EmbeddedRegistry() != EmbeddedRegistry() {
		t.Fatal("one embedded registry per process")
	}
}
```

Replace `agent/provider/resolve_test.go` with:

```go
func TestResolveUnknownInstanceNamesTheAvailableOnes(t *testing.T) {
	_, err := Resolve(fixtureRegistry(t), "nope/model")
	if err == nil || !strings.Contains(err.Error(), `unknown instance "nope"`) || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("want the registry's unknown-instance error, got %v", err)
	}
	if _, err := Resolve(fixtureRegistry(t), "no-slash"); err == nil {
		t.Fatal("a bare model needs a default instance, which this registry has (anthropic) — so this must resolve; adjust: a ref with an empty model must fail")
	}
}
```

(Correct the second assertion to what the registry does: `Resolve("anthropic/")` fails with "empty model reference"; a bare `"claude-opus-5"` resolves on the default instance. Pin both.)

- [ ] **Step 2: Run them to verify they fail**

Run: `cd agent && go test ./provider/ 2>&1 | head` → compile errors.

- [ ] **Step 3: Rewrite the profile**

`agent/provider/embedded.go`:

```go
package provider

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// EmbeddedRegistry is the process-wide embedded registry llm loads once
// (offline, no user layer, no cache, no environment; Task 2): the resolver
// behind profiles built without a registry (NewOpenAIProfile in tests,
// CoreToolNames). It resolves every curated implicit id without a
// credential (spec §5.2) and is never mutated by a live listing, so sharing
// it is safe.
func EmbeddedRegistry() *registry.Registry { return llm.EmbeddedRegistry() }
```

`agent/provider/profile.go` (the struct, constructors, and accessors; `toolCapability`, the three capability sets, `toolDefinitionsForCapabilities`, the clone helpers, and `WithCommunicateOverridesFrom` stay as they are):

```go
// Profile is what the agent reads about the model it drives (spec §7.5):
// the registry's Resolved record plus the tool definitions, doc files, and
// per-session overrides that follow from its surface. Construct one with
// Resolve or FromResolved; the zero value is not usable.
type Profile struct {
	res      registry.Resolved
	registry *registry.Registry
	toolDefs      []llm.ToolDefinition
	toolNameMap   map[string]string
	docFiles      []string
	contextWindow int // WithContextWindow override; 0 means the row's
	cheapModel    string
	cheapProvider string
}

// Resolve builds the profile for an instance/model reference.
func Resolve(r *registry.Registry, ref string) (*Profile, error) {
	res, err := r.Resolve(ref)
	if err != nil {
		return nil, err
	}
	return FromResolved(res, r), nil
}

// FromResolved wraps a resolved record. r re-resolves for WithModel and
// CrossProviderRef; nil means the embedded registry.
func FromResolved(res registry.Resolved, r *registry.Registry) *Profile {
	if r == nil {
		r = EmbeddedRegistry()
	}
	p := &Profile{res: res, registry: r}
	p.docFiles, p.toolNameMap = surfaceConventions(res.Surface)
	p.toolDefs = toolDefinitionsForCapabilities(p.capabilities(), p.ReasoningEffortLevels())
	return p
}

// NewOpenAIProfile is the openai/<model> profile on the embedded registry:
// the fixture every session test starts from and CoreToolNames' input.
func NewOpenAIProfile(model string) *Profile {
	p, err := Resolve(EmbeddedRegistry(), "openai/"+strings.TrimSpace(model))
	if err != nil {
		panic("provider: NewOpenAIProfile: " + err.Error())
	}
	return p
}

// surfaceConventions are the trained-for vendor conventions (spec §7.5):
// the project doc files and the tool names a surface expects.
func surfaceConventions(surface string) (docFiles []string, toolNameMap map[string]string) {
	switch surface {
	case registry.SurfaceOpenAI:
		return []string{"AGENTS.md", ".codex/instructions.md"}, map[string]string{"shell": "exec_command", "grep": "grep_files", "glob": "find_files"}
	case registry.SurfaceAnthropic:
		return []string{"CLAUDE.md", "AGENTS.md"}, nil
	case registry.SurfaceGoogle:
		return []string{"GEMINI.md", "AGENTS.md"}, map[string]string{"shell": "run_shell_command", "grep": "grep_search", "list_dir": "list_directory"}
	default:
		return []string{"AGENTS.md"}, nil
	}
}

// capabilities is the surface's tool set; the web_search function tool is a
// google-protocol arrangement (spec §7.5) and rides only with it.
func (p *Profile) capabilities() []toolCapability {
	var caps []toolCapability
	switch p.res.Surface {
	case registry.SurfaceAnthropic:
		caps = anthropicStyleCapabilities
	case registry.SurfaceGoogle:
		caps = geminiStyleCapabilities
	default:
		caps = openAICodexCapabilities
	}
	out := make([]toolCapability, 0, len(caps))
	for _, c := range caps {
		if c == capabilityWebSearch && !(p.res.Protocol == registry.ProtocolGoogle && registry.BoolValue(p.res.Caps.WebSearch)) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ID is the instance name; Model the requested model id.
func (p *Profile) ID() string    { return p.res.Instance }
func (p *Profile) Model() string { return p.res.ModelID }

// Resolved is the registry record the profile wraps.
func (p *Profile) Resolved() registry.Resolved { return p.res }

// Surface, Protocol, and ProviderID are the three registry keys the agent
// branches on (spec §7.5).
func (p *Profile) Surface() string    { return p.res.Surface }
func (p *Profile) Protocol() string   { return p.res.Protocol }
func (p *Profile) ProviderID() string { return p.res.ProviderID }

// ContextWindowSize is the row's window, or 0 when unknown (spec §7.3): the
// context manager applies no compaction budget until a live listing or a
// user row supplies one.
func (p *Profile) ContextWindowSize() int {
	if p.contextWindow > 0 {
		return p.contextWindow
	}
	if p.res.Caps.ContextWindow != nil {
		return *p.res.Caps.ContextWindow
	}
	return 0
}

// MaxOutputTokens is the row's output cap, or 0 when the row has none.
func (p *Profile) MaxOutputTokens() int {
	if p.res.Caps.MaxOutputTokens != nil {
		return *p.res.Caps.MaxOutputTokens
	}
	return 0
}

// SupportsReasoning is false only for an explicit reasoning = false row.
func (p *Profile) SupportsReasoning() bool { return !p.res.Caps.ReasoningDisabled() }

// ReasoningEffortLevels is the row's effort ladder; empty passes any
// requested effort through unchanged (spec §7.4).
func (p *Profile) ReasoningEffortLevels() []string {
	if p.res.Caps.ReasoningDisabled() {
		return nil
	}
	return cloneStringSlice(p.res.Caps.EffortValues)
}

func (p *Profile) SupportsWebSearch() bool        { return registry.BoolValue(p.res.Caps.WebSearch) }
func (p *Profile) KnowledgeCutoff() string        { return registry.StringValue(p.res.Caps.KnowledgeCutoff) }
func (p *Profile) Cost() *registry.Cost           { return p.res.Caps.Cost }
func (p *Profile) InputModalities() []string      { return cloneStringSlice(p.res.Caps.InputModalities) }
func (p *Profile) Warnings() []string             { return cloneStringSlice(p.res.Warnings) }
func (p *Profile) SupportsParallelToolCalls() bool { return true }
func (p *Profile) SupportsStreaming() bool         { return true }
func (p *Profile) DefaultCommandTimeoutMS() int    { return 120_000 }

// ProviderOptions are the protocol extras the agent adds (spec §7.5):
// parallel tool calls on Responses and the safety settings on Gemini.
func (p *Profile) ProviderOptions() map[string]any {
	switch p.res.Protocol {
	case registry.ProtocolOpenAIResponses:
		return map[string]any{registry.ProtocolOpenAIResponses: map[string]any{"parallel_tool_calls": true}}
	case registry.ProtocolGoogle:
		return map[string]any{registry.ProtocolGoogle: map[string]any{"safetySettings": []map[string]any{
			{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"},
			{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_ONLY_HIGH"},
			{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_ONLY_HIGH"},
			{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_ONLY_HIGH"},
		}}}
	}
	return nil
}

// CheapModel is the configured cheap model, else the instance's curated or
// configured cheap_model, else the model itself.
func (p *Profile) CheapModel() string {
	if m := strings.TrimSpace(p.cheapModel); m != "" {
		return m
	}
	if p.res.CheapModel != "" {
		return p.res.CheapModel
	}
	return p.Model()
}

// WithResolved returns a copy carrying a fresh record for the same
// instance (after a live listing was applied); overrides and routing stay.
func (p *Profile) WithResolved(res registry.Resolved) *Profile {
	if p == nil {
		return nil
	}
	next := FromResolved(res, p.registry)
	next.contextWindow = p.contextWindow
	return next.WithCommunicateOverridesFrom(p).withCheapModelFrom(p)
}

// CrossProviderRef reports whether ref ("<prefix>/<model>") names another
// instance: the prefix differs from this instance and this instance does
// not serve the whole ref as a model id — a namespaced id the instance
// serves (OpenRouter's "anthropic/claude-opus-5") stays on the instance.
func (p *Profile) CrossProviderRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	prefix, _, ok := strings.Cut(ref, "/")
	if !ok || strings.EqualFold(prefix, p.ID()) {
		return false
	}
	res, err := p.registry.Resolve(p.ID() + "/" + ref)
	return err != nil || res.Synthesized
}

// WithModel returns the profile for another model on the same instance,
// re-resolved so every cap follows the model; a redundant self-prefix is
// stripped, a cross-instance ref is kept verbatim for the session resolver,
// and an unresolvable id (the Codex allowlist) is kept verbatim so the
// membership check reports it.
func (p *Profile) WithModel(model string) *Profile {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.Model()
	}
	if prefix, rest, ok := strings.Cut(model, "/"); ok {
		switch {
		case strings.EqualFold(prefix, p.ID()):
			model = rest
		case p.CrossProviderRef(model):
			return p.withModelID(model)
		}
	}
	res, err := p.registry.Resolve(p.ID() + "/" + model)
	if err != nil {
		return p.withModelID(model)
	}
	next := FromResolved(res, p.registry)
	return next.WithCommunicateOverridesFrom(p).withCheapModelFrom(p)
}

func (p *Profile) withModelID(model string) *Profile {
	clone := *p
	clone.res.ModelID, clone.res.WireID = model, model
	return &clone
}
```

(`ConfiguredCheapModel`, `CheapProvider`, `CheapModelRef`, `CheapModelRefString`, `withCheapModelFrom`, `ToolDefinitions`, `ToolNameMap`, `ProjectDocFiles` keep today's bodies over the new fields. `agent/provider/resolve.go` shrinks to `Resolve` if you prefer it there; delete `ResolveProfileFromConfig`, `decidePrefixAction`, `restampInstanceIdentity`, `rebuildOnSameProviderChange`, `resolveEffortLevels`, `resolveWebSearch`, `materializeInstanceModelConfig`, the openrouter/compat catalog resolvers, `anthropicProviderOpts`, `suppressBareCatalogLookup`, and the `providercfg` import.) `WithProviderID` is deleted from `profile_overrides.go`.

- [ ] **Step 4: Rewrite the loaders and every caller**

`cmdutil/load_client.go` — replace the file's contents:

```go
// LoadClient loads the registry and builds the session client. stateDir is
// the session's state directory (the continuation secret lives there); the
// registry's own state root is DefaultStateRoot.
func LoadClient(stateDir string) (*llm.Client, error) {
	r, _, err := LoadRegistry()
	if err != nil {
		return nil, err
	}
	return NewRegistryClient(r, stateDir), nil
}

// LoadClientAt is LoadClient for an explicit providers.toml path (the hub's
// credential probes inspect the same file the spawn path will).
func LoadClientAt(path, stateDir string) (*llm.Client, error) {
	r, _, err := LoadRegistry(registry.WithConfigPath(path))
	if err != nil {
		return nil, err
	}
	return NewRegistryClient(r, stateDir), nil
}

// ResolveProfile resolves an instance/model reference on the client's registry.
func ResolveProfile(client *llm.Client, ref string) (*provider.Profile, error) {
	return provider.Resolve(client.Registry(), ref)
}

// BuildResolveProfile is SessionConfig.ResolveProfile: cross-instance
// switches resolve on the same registry the session's client dispatches on.
func BuildResolveProfile(client *llm.Client) func(ref string) (*provider.Profile, error) {
	return func(ref string) (*provider.Profile, error) { return ResolveProfile(client, ref) }
}
```

`cmdutil/cmdutil.go`:

```go
// ListModelsFunc returns a function suitable for server.SetListModelsFunc
// that lists one instance's models as wire descriptors.
func ListModelsFunc(client *llm.Client, instance string) func(context.Context) ([]appwire.ModelDescriptor, error) {
	return func(ctx context.Context) ([]appwire.ModelDescriptor, error) {
		listing, err := client.Models(ctx, instance)
		if err != nil {
			return nil, err
		}
		items := make([]appwire.ModelDescriptor, 0, len(listing.Models))
		for _, m := range listing.Models {
			items = append(items, ModelDescriptorFromResolved(m))
		}
		return items, nil
	}
}

// ModelDescriptorFromResolved is the wire view of a resolved row (spec §11.3).
func ModelDescriptorFromResolved(res registry.Resolved) appwire.ModelDescriptor {
	caps := res.Caps
	d := appwire.ModelDescriptor{Provider: res.Instance, Model: res.ModelID, ReasoningEffortLevels: append([]string(nil), caps.EffortValues...)}
	if caps.ContextWindow != nil {
		d.ContextWindow = new(*caps.ContextWindow)
	}
	if caps.MaxOutputTokens != nil {
		d.MaxOutputTokens = new(*caps.MaxOutputTokens)
	}
	if caps.Tools != nil {
		d.SupportsTools = new(*caps.Tools)
	}
	if len(caps.InputModalities) > 0 {
		d.SupportsVision = new(slices.Contains(caps.InputModalities, "image"))
	}
	if caps.WebSearch != nil {
		d.SupportsWebSearch = new(*caps.WebSearch)
	}
	if caps.Reasoning != nil {
		d.SupportsReasoning = new(*caps.Reasoning)
	}
	if caps.Cost != nil {
		d.InputCostPerMillion = new(caps.Cost.Input)
		d.OutputCostPerMillion = new(caps.Cost.Output)
	}
	return d
}
```

Delete `isOpenAICompatTag`, `ResolveProfileWithLiveWindow`, `instanceConfiguresContextWindow`, `instanceEndpoint`, `ResolveProfileForProvider`, `queryModelContextWindow`, `queryModelContextWindowImpl`, `lookupProviderEnv`, `ModelDescriptorFromInfo`, and the `kimicoding`/`providercfg` imports; delete their tests (`cmdutil/cmdutil_test.go` cases on the live window probe, `load_client_test.go` cases on `providercfg` seeding — replace the latter with three cases: `LoadClient` on a registry file resolves `work/…`; `LoadClientAt` reads the explicit path; an old-schema file returns `registry.ErrOldSchema`).

Callers:
- `cmd/evener/run.go`: `client, err := runLoadClient(stateDir)`; the initial profile via `cmdutil.ResolveProfile(client, modelRef.Qualified())` (keep the `WithCommunicateOutputSchema`/`WithAllowedDecisions` layering exactly as `buildInitialProfile` does in `serve.go`; if `run.go` has its own copy, share `serve.go`'s); `ResolveProfile: cmdutil.BuildResolveProfile(client)`; after loading, print `for _, w := range append(client.Registry().Warnings(), client.Registry().StrayOAuthRecords()...) { fmt.Fprintln(stderr, "warning:", w) }` (the §9.5 startup notice). Same in `cmd/evener/serve.go` (`serveLoadClient`, `buildInitialProfile(client, modelRef, outputSchemaJSON)`). Test seams `runLoadClient`/`serveLoadClient` become `func(string) (*llm.Client, error)`; their tests inject a client built with `llm.NewClient(llm.WithRegistry(<fixture>))` and fakes.
- `cmd/evener/main.go`: delete `openaiprovider.ClientVersion = buildinfo.Version()` and the import.
- `cmd/evener/internal/launchcheck/launchcheck.go`: `launchCheckLoadClient = cmdutil.LoadClient` (`func(string) (*llm.Client, error)`); delete `launchCheckLoadProviderConfig` and `launchCheckLoadConfig`; `validateLaunchCheckProfile(ref)` = `client, err := launchCheckLoadClient("")` then `cmdutil.ResolveProfile(client, ref.Qualified())` (network-free: `Resolve` never touches the network); `launchCheckModels` iterates `client.Registry().Instances()` (skipping hidden), calls `client.Models(ctx, inst.Name)`, appends `launchCheckModel{Provider: inst.Name, Model: m.ModelID}` per visible row and a diagnostic per failed instance; `validateLaunchCheckModel(ref)`: `client.CanServe` else the `Resolve` error; then `client.Models`; membership only when `listing.Live`; `launchCheckModelListUnavailable` and `launchCheckCatalogModelInfo` deleted.
- `cmd/llmcall/main.go`: `newLLMClient = func() (*llm.Client, error) { return cmdutil.LoadClient("") }`; when `--provider`/`LLM_PROVIDER`/`EVENER_PROVIDER` are all empty, use `client.DefaultProvider()` and error only when that is empty too; delete the `openaiprovider` import and `ClientVersion` line.
- `tools/tool-fluency/cmd/evener-fluency/main.go`: replace the eleven provider blank imports with `_ "primeradiant.com/evener/llm/providers/all"`; `runnerInitialProfile(client, modelRef)` uses `cmdutil.ResolveProfile`; drop `providercfg`.
- `agent/core_tool_names.go`: unchanged call (`provider.NewOpenAIProfile("gpt-5")` now resolves on the embedded registry; `llm.NewClient()` has no overrides and `Models("openai")` fails offline — the live probe fails open exactly as before).
- `agent/internal/contextmgr/context_manager.go`: treat `ContextWindowSize() == 0` as unknown — no pressure, no budget-driven compaction (`ContextWindow`/`Remaining` metrics report 0); add `TestUnknownContextWindowNeverCompacts` in the package.
- `cmd/evener-hub/app_models.go` `fetchLiveModels`: `client, err := liveModelLoadClient("")`; `for _, inst := range client.Registry().Instances()`; `client.Models(listCtx, inst.Name)`; `cmdutil.ModelDescriptorFromResolved`; the `openrouter-anthropic` skip and `VisibleLiveModel` are gone; `enrichModelDescriptors` still runs on the result until Task 9 deletes it.
- `cmd/evener-hub/app_credentials.go`: `credentialProbeClient` gains `Models(context.Context, string) (llm.ModelListing, error)` and `Registry() *registry.Registry`; `loadCredentialTestClient(path)` calls `cmdutil.LoadClientAt(path, "")` (or `LoadClient("")` when `path == ""`) and, until Task 9, still returns `providercfg.LoadFile(path)`'s config for `configuredInstance`/`credentialRequired`; the probe calls `client.Models`.
- `agent/profile_testhelpers_test.go` and `agent/testkit_test.go`: build sessions on `llm.NewClient(llm.WithRegistry(testRegistry(t)))`; `withAdapter(a)` also injects `a.Name()` as an instance (`registry.Provider{Base: "openai", APIKey: "test", Transport: registry.Transport{BaseURL: "http://test.invalid/v1"}}`) unless the name is a curated id; every `newXProfile` helper resolves on `testRegistry(t)` (`kimi` → instance `kimi` with `Base: "moonshotai"`, `glm` → `Base: "zai"`, `openai-compatible` → `Base: "openai-compatible"` with a `BaseURL`).

- [ ] **Step 5: Gate, lint, commit**

```bash
for m in . llm agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add -u agent/provider cmdutil cmd/evener cmd/llmcall tools/tool-fluency agent/core_tool_names.go agent/internal/contextmgr agent/profile_testhelpers_test.go agent/testkit_test.go cmd/evener-hub/app_models.go cmd/evener-hub/app_credentials.go
git add agent/provider/embedded.go
git commit -m "feat(provider,cmdutil): profiles wrap registry.Resolved; loaders build registry clients"
```

(Commit in two or three steps if it helps review: provider package first (its own tests green), then cmdutil + cmd, then the agent fixtures. Each commit must leave `go build ./...` green in every module.)

---

### Task 8: Delete the Responses→Chat fallback; rewire the wire and continuation tests

**Files:**
- Modify: `llm/responses_continuation.go` (`CanFallbackToChat`, `HistoryModeChatFallback`, `ContinuationMetadata.ChatFallbackHistoryLen` deleted), `llm/types.go` (`Request.FullHistoryFallbackMessages` deleted), `llm/classify.go` (`ErrorClassFallback`, `isEndpointFallbackSignal` deleted), `llm/classify_test.go`, `llm/classify_fuzz_test.go`, `agent/session_model_call.go` (full-history retry keeps its messages in the call frame), `agent/session_init.go` (`modelFallbackEligible`), `agent/internal/contextmgr/context_manager.go` (`shouldFallbackSummarizationModel`), `agent/internal/agenttest/agenttest.go` (`CanFallbackToChat` deleted), `agent/apilog_read.go`, `agent/doctor/apilog.go` (no `chat_completions_fallback` tally)
- Rewrite tests: `agent/session_openai_continuation_phase{0a,4d,5b,9,10}_test.go`, `agent/session_openai_malformed_tool_call_test.go`, `agent/session_openai_prompt_cache_test.go`, `llm/provider_wire_{client_timeout,outcomes,redirect,wrapper}_test.go`, `agent/session_openai_continuation_phase12_live_test.go` (live; compile only)

**Interfaces:**
- Consumes: Tasks 2–3 (`WithRegistry`, injected instances, `PlanResponsesContinuation`), Task 7 fixtures.
- Produces: `applyResponsesContinuationAnchorPlanning(ctx, req, historyTurns, stream) (llm.Request, []llm.Message)` — the second value is the full-history message list retained for the anchor-rejection retry; `responsesContinuationFullHistoryFallbackRequest(req, fullHistory []llm.Message)`; a shared test helper `agent/registry_client_test.go`: `newRegistryClient(t, stateDir string, instances map[string]registry.Provider) *llm.Client` and `openaiInstance(srvURL string) registry.Provider`.

- [ ] **Step 1: Delete the fallback (tests first)**

Remove every test that asserts chat-completions fallback behaviour (`HistoryModeChatFallback`, `CanFallbackToChat`, `ChatFallbackHistoryLen`, `ErrorClassFallback`, `isEndpointFallbackSignal`); keep and adapt the anchor-rejection tests: a `previous_response_not_found` error on a delta request retries once with the full history (`HistoryModeFullHistoryFallback`). Then:

- `agent/session_model_call.go`: `applyResponsesContinuationAnchorPlanning` returns the retained `fullHistoryFallbackMessages` alongside the request instead of storing them on it; `shouldRetryResponsesContinuationAsFullHistory(req, err)` drops the `FullHistoryFallbackMessages` check; `responsesContinuationFullHistoryFallbackRequest(req, fullHistory)` and `responsesContinuationModelFallbackRequest(req, fullHistory)` take the messages as a parameter; the shadow-estimate path no longer clears the field.
- `agent/session_init.go` `modelFallbackEligible`: `case llm.ErrorClassPermanent:` only.
- `agent/internal/contextmgr/context_manager.go` `shouldFallbackSummarizationModel`: delete the `ErrorClassFallback` branch.
- `llm/classify.go`: delete `ErrorClassFallback` and `isEndpointFallbackSignal`; `Classify` returns `ErrorClassPermanent` where it returned the fallback class.

- [ ] **Step 2: Rewire the OpenAI session tests onto the registry client**

`agent/registry_client_test.go`:

```go
package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// openaiInstance is an explicit openai-based instance pointed at a test server.
func openaiInstance(srvURL string) registry.Provider {
	return registry.Provider{Base: "openai", APIKey: "test-key", Transport: registry.Transport{BaseURL: srvURL}}
}

// newRegistryClient builds a client over a hermetic registry with the given
// instances; stateDir holds the continuation secret.
func newRegistryClient(t *testing.T, stateDir string, instances map[string]registry.Provider) *llm.Client {
	t.Helper()
	r, err := registry.Load(registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()), registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(stateDir))
}
```

Every `&openai.Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}` registration becomes `client := newRegistryClient(t, dir, map[string]registry.Provider{"openai": openaiInstance(srv.URL)})`; the session profile becomes `provider.Resolve(client.Registry(), "openai/gpt-5.4")` (the request bodies those tests assert — `previous_response_id`, `store`, `input` deltas, `prompt_cache_key` — are produced by the `responses` protocol now; update any assertion that pinned an old-adapter-only field). The `phase9PlanningOpenAIAdapter` decorator becomes an override registered under `openai` on the same client that forwards `Complete`/`Stream` to a second registry client and implements `PlanResponsesContinuation` by calling `inner.PlanResponsesContinuation(context.Background(), req)` and adjusting the plan as the test needs.

- [ ] **Step 3: Rewire the `llm` wire tests**

`llm/provider_wire_{client_timeout,outcomes,redirect,wrapper}_test.go`: each provider leg becomes an injected instance on a hermetic registry — `openai` (base openai, responses), `anthropic` (`{Base: "anthropic", APIKey: "test-key", Transport: {BaseURL: srv.URL}}`), `google` (`GOOGLE_BASE_URL` semantics via `Transport.BaseURL`), `work` (base openai, `Protocol: openai-chat`) — and the calls go through `llm.NewClient(llm.WithRegistry(r)).Complete/Stream`; the assertions on attempt outcomes, credential redaction, timeouts, and redirects stay as they are. Where a test needs the `*http.Client` (redirect policy, timeouts), set `chatcompletions.DefaultProtocol.Client`/`responses.DefaultProtocol.Client`/`anthropic.DefaultProtocol.Client`/`google.DefaultProtocol.Client` for the test's duration and restore them in `t.Cleanup` (these tests already cannot run in parallel).

- [ ] **Step 4: Gate, lint, commit**

```bash
for m in . llm agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add -u llm agent
git add agent/registry_client_test.go
git commit -m "refactor(llm,agent): delete the Responses-to-Chat fallback; wire tests run on the registry client"
```

---

### Task 9: The hub on the registry — instances, auth, credential tests, model list, spawn gate, diagnostics

**Files:**
- Create: `cmd/evener-hub/registry.go` (+ `registry_test.go`), `cmd/evener-hub/internal/hubcore/registry.go` (the `ProviderRegistry` interface)
- Modify: `cmd/evener-hub/main.go`, `cmd/evener-hub/internal/hubcore/config.go`, `cmd/evener-hub/app_instances.go`, `cmd/evener-hub/app_auth.go`, `cmd/evener-hub/app_credentials.go`, `cmd/evener-hub/app_models.go`, `cmd/evener-hub/spawn.go`, `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/internal/launchconfig/env.go`, `appwire/types.go`, `appwire/protocol.go`; regenerate `docs/appwire-protocol.md` and `cmd/evener-hub/frontend/src/protocol/types.gen.ts` with `make generate`
- Tests: `cmd/evener-hub/registry_test.go`, `app_instances_test.go`, `app_auth_test.go` (+ the `app_auth_*_test.go` siblings), `app_credentials_test.go` (+ siblings), `app_models_test.go`, `spawn_test.go`, `main_test.go`, `internal/launchconfig/env_test.go`, `appwire/golden_test.go` goldens (regenerate with `make fuzz-goldens` if a corpus seed hits the changed params types), `internal/appwirets/emit_test.go` (`TestGeneratedFileCurrent`)

**Interfaces:**
- Consumes: Tasks 1, 6, 7 (`registry.ReadConfigFile/WriteConfigFile`, `Registry.Instance/Instances/Provider/ProviderIDs/StrayOAuthRecords/UserLayerNote/Warnings/ResolveInstance`, `cmdutil.LoadRegistry/ProvidersConfigPath/CredentialsPath/LoadClient/LoadClientAt/ModelDescriptorFromResolved`).
- Produces:
  - `hubcore.ProviderRegistry interface { Get() *registry.Registry; Reload() error; LoadError() error; WritesRefused() bool; Diagnostics() []string }`; `hubcore.WebConfig` fields `Registry ProviderRegistry`, `ProvidersConfigPath string`, `CredentialsPath string`, `NoUserLayer bool` (replacing `ProviderConfig`).
  - `appwire.InstanceEntry`, `appwire.ProviderDescriptor`, `appwire.InstanceListResponse`, `appwire.InstanceCreateParams`, `appwire.InstanceEditParams` in the shapes below; `appwire.AuthStatusResponse.ActiveSource`/`AuthModes` in the registry vocabulary.
  - `launchconfig.EnvInputs{ProvidersConfigPath string; NoUserLayer bool; CredentialsPath string}` (the `Creds`/`Provider` fields and `CredentialResolver` are deleted); `launchconfig.ToEnv` sets `EVENER_PROVIDERS_CONFIG` to the path, or to the empty string when `NoUserLayer`, and `EVENER_CREDENTIALS_CONFIG` to `CredentialsPath`.
  - `authModesFor(auth string) []string`: `oauth-openai-codex` → `["oauth"]`; `bearer`, `header` → `["apiKey"]`; `optional-bearer` → `["none", "apiKey"]`; `none` → `["none"]`; `gcp-adc` → `["adc"]`.

Wire shapes (spec §11.3):

```go
// InstanceEntry is one registry instance with its credential status (spec §11.3).
type InstanceEntry struct {
	Name               string            `json:"name"`
	Base               string            `json:"base,omitempty"`
	ProviderID         string            `json:"providerId"`
	Protocol           string            `json:"protocol"`
	Surface            string            `json:"surface,omitempty"`
	Auth               string            `json:"auth"`
	BaseURL            string            `json:"baseUrl,omitempty"`
	Vars               map[string]string `json:"vars,omitempty"`
	Implicit           bool              `json:"implicit"`
	Hidden             bool              `json:"hidden,omitempty"`
	IsDefault          bool              `json:"isDefault"`
	AuthModes          []string          `json:"authModes,omitempty"`
	ActiveSource       string            `json:"activeSource"`
	HasStoredFile      bool              `json:"hasStoredFile,omitempty"`
	HasStoredOAuth     bool              `json:"hasStoredOAuth"`
	EnvVar             string            `json:"envVar,omitempty"`
	StoredEmail        string            `json:"storedEmail,omitempty"`
	CredentialRequired bool              `json:"credentialRequired"`
	Warnings           []string          `json:"warnings,omitempty"`
}

// ProviderDescriptor is a registry provider the add form can build on.
type ProviderDescriptor struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Protocol  string   `json:"protocol"`
	Auth      string   `json:"auth"`
	VarsEnv   []string `json:"varsEnv,omitempty"`
	APIKeyEnv []string `json:"apiKeyEnv,omitempty"`
	Implicit  bool     `json:"implicit"`
}

// InstanceListResponse is the result of evener/instance/list.
type InstanceListResponse struct {
	Instances          []InstanceEntry      `json:"instances"`
	AvailableProviders []ProviderDescriptor `json:"availableProviders"`
	Diagnostics        []string             `json:"diagnostics,omitempty"`
	UserLayer          string               `json:"userLayer,omitempty"`
	WritesRefused      bool                 `json:"writesRefused,omitempty"`
}

// InstanceCreateParams is the params for evener/instance/create. APIKeyEnv
// is a variable name and CredentialHeader must reference a $VAR: secrets
// never cross this boundary (spec §11.2).
type InstanceCreateParams struct {
	Name             string            `json:"name"`
	Base             string            `json:"base"`
	BaseURL          string            `json:"baseUrl,omitempty"`
	Protocol         string            `json:"protocol,omitempty"`
	Surface          string            `json:"surface,omitempty"`
	Vars             map[string]string `json:"vars,omitempty"`
	APIKeyEnv        string            `json:"apiKeyEnv,omitempty"`
	CredentialHeader string            `json:"credentialHeader,omitempty"`
}

// InstanceEditParams is the params for evener/instance/edit; empty fields
// are unchanged. Editing an implicit instance writes a shadowing entry
// carrying only these fields (spec §11.3).
type InstanceEditParams struct {
	Name     string            `json:"name"`
	BaseURL  string            `json:"baseUrl,omitempty"`
	Protocol string            `json:"protocol,omitempty"`
	Surface  string            `json:"surface,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
}
```

- [ ] **Step 1: The hub registry holder (tests first)**

`cmd/evener-hub/registry_test.go`:

```go
func TestHubRegistryDegradesOnOldSchema(t *testing.T) {
	configRoot := t.TempDir()
	path := filepath.Join(configRoot, "providers.toml")
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "gk")
	h := newHubRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		return cmdutil.LoadRegistry(append(extra, registry.WithOffline(true), registry.WithoutCache())...)
	})
	if err := h.Reload(); err == nil || !errors.Is(err, registry.ErrOldSchema) {
		t.Fatalf("Reload reports the pointer: %v", err)
	}
	if h.Get() == nil || !h.WritesRefused() {
		t.Fatal("the hub keeps an implicit-only registry and refuses writes (spec §10)")
	}
	if _, ok := h.Get().Instance("groq"); !ok {
		t.Fatal("implicit instances still exist without the user layer")
	}
	diags := strings.Join(h.Diagnostics(), "\n")
	if !strings.Contains(diags, "§14.1") || !strings.Contains(diags, "user layer: none") {
		t.Fatalf("diagnostics carry the pointer and the user-layer note: %s", diags)
	}
	if err := os.WriteFile(path, []byte("default = \"groq\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil || h.WritesRefused() {
		t.Fatalf("a fixed file clears the refusal: %v %v", err, h.WritesRefused())
	}
}
```

`cmd/evener-hub/internal/hubcore/registry.go`:

```go
// ProviderRegistry is the hub's live view of the provider registry: the
// current instance set, reloaded after every providers.toml write, and the
// diagnostics the web UI shows (spec §11.3).
type ProviderRegistry interface {
	Get() *registry.Registry
	Reload() error
	LoadError() error
	WritesRefused() bool
	Diagnostics() []string
}
```

`cmd/evener-hub/registry.go`:

```go
type registryLoader func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error)

// hubRegistry implements hubcore.ProviderRegistry. When the user layer
// fails to load (an old-schema file) it holds an implicit-only registry,
// keeps the error for the diagnostics, and refuses writes until a reload
// succeeds (spec §10, §14.1).
type hubRegistry struct {
	load    registryLoader
	mu      sync.RWMutex
	current *registry.Registry
	loadErr error
}

func newHubRegistry(load registryLoader) *hubRegistry { return &hubRegistry{load: load} }

func (h *hubRegistry) Reload() error {
	r, _, err := h.load()
	if err != nil {
		fallback, _, ferr := h.load(registry.WithNoUserLayer())
		h.mu.Lock()
		defer h.mu.Unlock()
		h.loadErr = err
		if ferr == nil {
			h.current = fallback
		}
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.current, h.loadErr = r, nil
	return nil
}

func (h *hubRegistry) Get() *registry.Registry { h.mu.RLock(); defer h.mu.RUnlock(); return h.current }
func (h *hubRegistry) LoadError() error         { h.mu.RLock(); defer h.mu.RUnlock(); return h.loadErr }
func (h *hubRegistry) WritesRefused() bool      { return h.LoadError() != nil }

// Diagnostics is what the credentials pane shows above the instance list:
// the load error, the user-layer note, stray OAuth records, and warnings.
func (h *hubRegistry) Diagnostics() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	if h.loadErr != nil {
		out = append(out, "providers.toml: "+h.loadErr.Error()+" (instance writes are refused until the file is fixed)")
	}
	if h.current != nil {
		out = append(out, h.current.UserLayerNote())
		out = append(out, h.current.StrayOAuthRecords()...)
		out = append(out, h.current.Warnings()...)
	}
	return out
}
```

`cmd/evener-hub/main.go`: delete the `loadProviderConfig`/`materializeConfig` deps and the materialization block; `deps.loadRegistry registryLoader` defaults to `func(extra ...registry.Option) (...) { return cmdutil.LoadRegistry(extra...) }`; at startup `hubReg := newHubRegistry(deps.loadRegistry); if err := hubReg.Reload(); err != nil { fmt.Fprintf(stderr, "[hub] providers config: %v — starting with implicit instances only\n", err) }`; `providersConfigPath, noUserLayer := cmdutil.ProvidersConfigPath()`; `WebConfig{Registry: hubReg, ProvidersConfigPath: providersConfigPath, CredentialsPath: cmdutil.CredentialsPath(), NoUserLayer: noUserLayer || hubReg.WritesRefused(), CredsStore: credsStore}` (the store loads from `cmdutil.CredentialsPath()`); `HubSpawner{Registry: hubReg, ProvidersConfigPath, CredentialsPath, NoUserLayer}`. Test: `main_test.go` starts the hub with an old-schema file at `EVENER_PROVIDERS_CONFIG` and asserts it serves (no exit), that `evener/instance/list` returns `WritesRefused: true` with the pointer in `Diagnostics`, and that a spawned child's env (through the spawner seam) carries `EVENER_PROVIDERS_CONFIG=` (present, empty) and `EVENER_CREDENTIALS_CONFIG=<path>` — this is §13's "Flag day" row for the hub.

- [ ] **Step 2: Spawn env and credential gate (tests first)**

`launchconfig/env.go`: delete the credentials-store injection and `CredentialResolver`; `EnvInputs` gains `NoUserLayer bool` and `CredentialsPath string`:

```go
	switch {
	case in.NoUserLayer:
		out = setEnv(out, envvars.EVENERProvidersConfig.Name, "")
	case in.ProvidersConfigPath != "":
		out = setEnv(out, envvars.EVENERProvidersConfig.Name, in.ProvidersConfigPath)
	}
	if in.CredentialsPath != "" {
		out = setEnv(out, envvars.EVENERCredentialsConfig.Name, in.CredentialsPath)
	}
```

`env_test.go`: the empty-string case is asserted as a present `EVENER_PROVIDERS_CONFIG=` entry (not absent); the parent env's value is replaced.

`spawn.go` — `validateProviderCredentials(provider string, reg hubcore.ProviderRegistry) error`:

```go
func validateProviderCredentials(provider string, reg hubcore.ProviderRegistry) error {
	name := strings.ToLower(strings.TrimSpace(provider))
	if name == "" || reg == nil || reg.Get() == nil {
		return nil
	}
	r := reg.Get()
	if inst, ok := r.Instance(name); ok {
		switch inst.Auth {
		case registry.AuthNone, registry.AuthOptionalBearer:
			return nil
		}
		if inst.CredentialSource != "none" {
			return nil
		}
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: %s", name, strings.Join(inst.Warnings, "; ")))
	}
	if p, ok := r.Provider(name); ok && registry.BoolValue(p.Implicit) {
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set a key via evener/auth/apiKey/set or export one of %s", name, strings.Join(p.APIKeyEnv, ", ")))
	}
	return appwire.HubLaunchError(fmt.Sprintf("unknown instance %q: add a [providers.%s] entry to providers.toml", name, name))
}
```

(The Codex case falls out: `oauth-openai-codex`'s credential source is `oauth` when the record exists and `none` otherwise, and the instance's warning names `evener openai login --instance <name>`. `openAIStoredOAuthUsable`, `openAIInstanceOAuthUsable`, `providerCredentialInEnv`, `openAICompatibleBaseURLInEnv`, and the `noConfig` path are deleted; `spawn_test.go` cases become registry fixtures via a `registryLoader` that injects instances.)

- [ ] **Step 3: Instance CRUD on the registry (tests first)**

`app_instances.go` — the controller holds `reg hubcore.ProviderRegistry`, `providersConfigPath string`, `auth *hubAuthController`, seams `readConfig = registry.ReadConfigFile`, `writeConfig = registry.WriteConfigFile`.

```go
func (c *hubInstancesController) List() appwire.InstanceListResponse {
	r := c.reg.Get()
	entries := make([]appwire.InstanceEntry, 0)
	for _, inst := range r.Instances() {
		entries = append(entries, c.entryFor(inst))
	}
	providers := make([]appwire.ProviderDescriptor, 0)
	for _, id := range r.ProviderIDs() {
		p, _ := r.Provider(id)
		providers = append(providers, appwire.ProviderDescriptor{ID: id, Name: p.Name, Protocol: p.Protocol, Auth: p.Transport.Auth, VarsEnv: sortedValues(p.Transport.VarsEnv), APIKeyEnv: append([]string(nil), p.APIKeyEnv...), Implicit: registry.BoolValue(p.Implicit)})
	}
	// sortedValues is a file-local helper: the map's values, sorted.
	return appwire.InstanceListResponse{Instances: entries, AvailableProviders: providers, Diagnostics: c.reg.Diagnostics(), UserLayer: r.UserLayerNote(), WritesRefused: c.reg.WritesRefused()}
}

func (c *hubInstancesController) entryFor(inst registry.Instance) appwire.InstanceEntry {
	status := c.auth.instanceStatus(inst)
	return appwire.InstanceEntry{
		Name: inst.Name, Base: inst.Base, ProviderID: inst.ProviderID, Protocol: inst.Protocol, Surface: inst.Surface, Auth: inst.Auth,
		BaseURL: sanitizeEndpointURL(inst.BaseURL), Vars: inst.Vars, Implicit: inst.Implicit, Hidden: inst.Hidden, IsDefault: inst.Default,
		AuthModes: status.AuthModes, ActiveSource: status.ActiveSource, HasStoredFile: status.HasStoredFile, HasStoredOAuth: status.HasStoredOAuth,
		EnvVar: status.EnvVar, StoredEmail: status.StoredEmail,
		CredentialRequired: inst.Auth != registry.AuthNone && inst.Auth != registry.AuthOptionalBearer, Warnings: inst.Warnings,
	}
}

func (c *hubInstancesController) refuseWhenBroken() error {
	if c.reg.WritesRefused() {
		return fmt.Errorf("providers.toml cannot be edited until it loads: %w", c.reg.LoadError())
	}
	return nil
}

func (c *hubInstancesController) Create(params appwire.InstanceCreateParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if !registry.ValidInstanceName(name) {
		return fmt.Errorf("invalid instance name %q (lowercase, no slash)", params.Name)
	}
	if _, ok := c.reg.Get().Provider(strings.TrimSpace(params.Base)); !ok {
		return fmt.Errorf("unknown base provider %q", params.Base)
	}
	if params.CredentialHeader != "" && !strings.Contains(params.CredentialHeader, "$") {
		return errors.New("credential header must reference a $VARIABLE, never a literal secret")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	l, _, err := c.readConfig(c.providersConfigPath)
	if err != nil {
		return err
	}
	if _, exists := l.Providers[name]; exists {
		return fmt.Errorf("instance %q already exists", name)
	}
	p := registry.Provider{ID: name, Base: params.Base, Protocol: params.Protocol, Surface: params.Surface, Transport: registry.Transport{BaseURL: strings.TrimSpace(params.BaseURL), Vars: params.Vars}}
	if v := strings.TrimSpace(params.APIKeyEnv); v != "" {
		p.APIKeyEnv = []string{v}
	}
	if k, v, ok := strings.Cut(params.CredentialHeader, "="); ok && strings.TrimSpace(k) != "" {
		p.CredentialHeaders = map[string]string{strings.TrimSpace(k): strings.TrimSpace(v)}
	}
	l.Providers[name] = p
	if err := c.writeConfig(c.providersConfigPath, l); err != nil {
		return err
	}
	return c.reg.Reload()
}
```

`Edit`: refuse when broken; read the layer; the entry is `l.Providers[name]` when present, else (an implicit instance) a fresh `registry.Provider{ID: name}` — a shadowing entry that carries only the fields the edit sets (spec §11.3: "never a literal base_url the form merely displayed"); apply non-empty `BaseURL`/`Protocol`/`Surface` and merge `Vars`; write; reload. `Remove`: refuse when broken; `inst, ok := reg.Get().Instance(name)`; when `inst.Implicit` → `fmt.Errorf("%s exists from the environment (%s); unset it or remove the OAuth record instead of deleting the instance", name, describeImplicit(inst))` where `describeImplicit` names the credential source (`env:GROQ_API_KEY`, `oauth` record path, `store` entry); else delete the entry, clear the store key, delete the OAuth record, write, reload. `SetDefault`: refuse when broken; the name must be an instance; `l.Default = name`; write; reload. `reloadFromDisk`, `load`, `write`, `remove` seams and `providercfg` are gone. Tests: each write is asserted by re-reading the file with `registry.ReadConfigFile` and by the reloaded `List()`; the refusal test uses an old-schema file; the implicit-remove refusal names `GROQ_API_KEY`.

- [ ] **Step 4: Auth controller (tests first)**

`app_auth.go`: the controller holds `reg hubcore.ProviderRegistry` (set by `newHubAppServer` from `cfg.Registry`) and drops `providersConfigPath`, `resolveInstance`, `resolveInstanceBehaviorTag`, `instanceIsOpenAI`, `instanceStatusFor`. New:

```go
// instanceIsCodex reports whether name authenticates through the Codex
// OAuth flow (spec §9.5): its transport auth is oauth-openai-codex.
func (c *hubAuthController) instanceIsCodex(name string) bool {
	r := c.reg.Get()
	if inst, ok := r.Instance(name); ok {
		return inst.Auth == registry.AuthOAuthOpenAICodex
	}
	if p, ok := r.Provider(name); ok && registry.BoolValue(p.Implicit) {
		return p.Transport.Auth == registry.AuthOAuthOpenAICodex
	}
	return false
}

func normalizeAuthProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "openai-codex"
	}
	return provider
}

// instanceStatus is the credential status of one instance or curated
// implicit provider: the registry's credential source, the store's file
// layer, and for the Codex transport the OAuth record.
func (c *hubAuthController) instanceStatus(inst registry.Instance) appwire.AuthStatusResponse {
	if inst.Auth == registry.AuthOAuthOpenAICodex {
		resp, _ := c.openAIInstanceStatus(inst.Name)
		return resp
	}
	hasFile, _ := c.creds.Layers(inst.Name) // file layer only; Task 11 renames this to the store's file-only Get
	envVar := ""
	if strings.HasPrefix(inst.CredentialSource, "env:") {
		envVar = strings.TrimPrefix(inst.CredentialSource, "env:")
	}
	signedIn := inst.CredentialSource != "none"
	return appwire.AuthStatusResponse{Provider: inst.Name, Supported: true, SignedIn: signedIn, ActiveSource: inst.CredentialSource, AuthModes: authModesFor(inst.Auth), HasStoredFile: hasFile, EnvVar: envVar}
}
```

`Status(name)`: `inst, ok := r.Instance(name)`; when not an instance but a curated implicit provider, build a `registry.Instance` view from `r.ResolveInstance(name)` (`Auth: res.Transport.Auth`, `CredentialSource: res.Credential.Source`, `Warnings`) so the pane can show every implicit provider whether or not it has a credential (spec §11.3); unknown → `Supported: false`. `List()`: one row per curated implicit provider id (`r.ProviderIDs()` filtered by `Provider(id).Implicit`) then every explicit instance not already listed, each through `Status`. `requiresOpenAI` → `requiresCodex` (`instanceIsCodex`). `Logout`: Codex → delete the record (as today), else clear the store; both then `c.reg.Reload()`. `ApiKeySet` → `setCredential` then `c.reg.Reload()`. `openAIInstanceStatus(name)`: unchanged except `envvars.AuthModes("openai")` → `authModesFor(registry.AuthOAuthOpenAICodex)` and the `OPENAI_API_KEY` env fallback is deleted (Codex instances never read a key, spec §5.1). `LoginComplete`/`DevicePoll` save under the instance name (unchanged) and reload.

- [ ] **Step 5: Credential test and model list (tests first)**

`app_credentials.go`: `credentialProbeClient` = `interface { Models(context.Context, string) (llm.ModelListing, error); Close() error }`; `loadCredentialTestClient(path string) (credentialProbeClient, error)` (`cmdutil.LoadClientAt(path, "")` when `path != ""`, else `cmdutil.LoadClient("")`); `runCredentialTest(ctx, name, loader)`:

```go
	r := c.reg.Get()
	inst, ok := r.Instance(name)
	if !ok {
		res, err := r.ResolveInstance(name)
		if err != nil {
			return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage)
		}
		inst = registry.Instance{Name: name, Auth: res.Transport.Auth, CredentialSource: res.Credential.Source}
	}
	required := inst.Auth != registry.AuthNone && inst.Auth != registry.AuthOptionalBearer
	if required && inst.CredentialSource == "none" {
		return credentialTestResponse(name, appwire.AuthTestStatusMissing, credentialTestMissingMessage)
	}
	client, err := loader(c.providersConfigPath)
	if err != nil || client == nil {
		return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage)
	}
	defer func() { _ = client.Close() }()
	probeCtx, cancel := context.WithTimeout(ctx, credentialTestTimeout)
	defer cancel()
	listing, err := client.Models(probeCtx, name)
	if err != nil {
		status, message := classifyCredentialTestError(err)
		return credentialTestResponse(name, status, message)
	}
	if !listing.Live {
		return credentialTestResponse(name, appwire.AuthTestStatusUnsupported, credentialTestUnsupportedMessage)
	}
	return credentialTestResponse(name, appwire.AuthTestStatusSuccess, credentialTestSuccessMessage)
```

(`configuredInstance`, `credentialRequired`, `instanceHasEffectiveCredential`, `hasResolvedCredentialHeader`, `hasResolvedAuthorizationHeader` and the `providercfg`/`envvars` imports are deleted; `classifyCredentialTestError` drops the "does not support listing models" message sniff.)

`app_models.go`: delete `enrichModelListResponse`'s catalog enrichment (`enrichModelDescriptors`, `applyInstanceModelOverride`, `catalogModelInfo`, `behaviorTagFor`, `launchProviderAllowsUnreportedModels`); `hubModelList` still fills `DisplayName` with `prettifyModelDisplayName` when empty and sorts; `validateEvenerLaunchModel`: a provider absent from the contract is accepted when `cfg.Registry.Get().Instance(ref.Provider)` exists (registry-only listing), refused otherwise; a listed provider whose live list lacks the model is refused as today. `fetchLiveModels` (from Task 7) returns descriptors straight from `cmdutil.ModelDescriptorFromResolved`.

- [ ] **Step 6: Regenerate, gate, commit**

```bash
export PATH="$(go env GOROOT)/bin:$PATH" && make generate && git diff --stat -- docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
go test ./cmd/evener-hub/... ./appwire/... ./internal/appwirets/... 2>&1 | tail -5
for m in . llm agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
make lint
git add -u cmd/evener-hub appwire docs/appwire-protocol.md
git add cmd/evener-hub/registry.go cmd/evener-hub/registry_test.go cmd/evener-hub/internal/hubcore/registry.go
git commit -m "feat(hub): instances, auth, credential tests, model list, and the spawn gate on the registry"
```

(The frontend's own tests will fail against the regenerated `types.gen.ts` until Task 10 — run `make test-web` at the end of Task 10, not here.)

---

### Task 10: Hub frontend on the new instance wire shape

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/credentials.ts`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels.ts`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.module.css`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceRow.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.tsx`
- Test: `cmd/evener-hub/frontend/src/stores/credentials.test.ts`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels.test.ts`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.test.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.edge.test.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceRow.test.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.test.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/oauthDialogs.test.tsx` (incidental: two `availableTypes` → `availableProviders` fixture literals; `oauthDialogs.tsx` itself never references `InstanceEntry`/`InstanceListResponse` and is not touched), `cmd/evener-hub/frontend/src/panes/settings/Settings.test.tsx` (incidental: one `availableTypes` → `availableProviders` fixture, now a real `ProviderDescriptor` so the Add dialog it opens still renders a selectable option)

`clipboard.ts`, `clipboard.test.ts`, and `oauthDialogs.tsx` carry no reference to `InstanceEntry`/`InstanceListResponse`/`availableTypes` (verified by repo-wide grep) and are left untouched, per the plan's instruction.

**Interfaces:**
- Consumes (from `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, regenerated by Task 9 — this task never hand-edits that file): `InstanceEntry`, `ProviderDescriptor`, `InstanceListResponse`, `InstanceCreateParams`, `InstanceEditParams`, `InstanceRemoveParams`, `InstanceSetDefaultParams`, `AuthStatusResponse`, `AuthListResponse`, `AuthTestResponse`, `AuthDeviceStartResponse`, `AuthDevicePollResponse`, `AuthLoginStartResponse`, `AuthLoginCompleteResponse`, `AuthLogoutResponse`.
- Produces:
  - `credentialLabels.ts`: `activeSourceLabel(instance): string` (new — one label for every `ActiveSource` value), `credentialLayers(instance): CredentialLayerView[]` (rewritten), `keylessByDesign(instance): boolean` (simplified), `unconfiguredLabel(instance): string | null` (rewritten in terms of `activeSourceLabel`), `groupByProvider(instances): InstanceProviderGroup[]` (renamed from `groupByType`, keyed by `providerId`). `safeCredentialTestResult`/`safeCredentialTestMessage` are unchanged and still exported (AuthTestResponse's status vocabulary is untouched by this plan).
  - `stores/credentials.ts`: `CredentialsStoreState.availableProviders: ProviderDescriptor[]` (replaces `availableTypes: string[]`), `.diagnostics: string[]`, `.userLayer: string`, `.writesRefused: boolean`; `create`/`edit` now accept the new `InstanceCreateParams`/`InstanceEditParams` shapes.
  - `InstanceRow.tsx`: `InstanceRowProps.writesRefused?: boolean` (new).
  - `instanceDialogs.tsx`: `AddInstanceDialogProps.availableProviders: ProviderDescriptor[]` (replaces `availableTypes: string[]`).

Reference — the shapes `make generate` produces from the Go structs Task 9 lands (informational only; this task never authors `types.gen.ts`):

```ts
export interface InstanceEntry {
  name: string;
  base?: string;
  providerId: string;
  protocol: string;
  surface?: string;
  auth: string;
  baseUrl?: string;
  vars?: Record<string, string>;
  implicit: boolean;
  hidden?: boolean;
  isDefault: boolean;
  authModes?: string[];
  activeSource: string;
  hasStoredFile?: boolean;
  hasStoredOAuth: boolean;
  envVar?: string;
  storedEmail?: string;
  credentialRequired: boolean;
  warnings?: string[];
}
export interface ProviderDescriptor {
  id: string;
  name?: string;
  protocol: string;
  auth: string;
  varsEnv?: string[];
  apiKeyEnv?: string[];
  implicit: boolean;
}
export interface InstanceListResponse {
  instances: InstanceEntry[];
  availableProviders: ProviderDescriptor[];
  diagnostics?: string[];
  userLayer?: string;
  writesRefused?: boolean;
}
export interface InstanceCreateParams {
  name: string;
  base: string;
  baseUrl?: string;
  protocol?: string;
  surface?: string;
  vars?: Record<string, string>;
  apiKeyEnv?: string;
  credentialHeader?: string;
}
export interface InstanceEditParams {
  name: string;
  baseUrl?: string;
  protocol?: string;
  surface?: string;
  vars?: Record<string, string>;
}
```

Design decisions this task locks in (so the Step 1 tests and Step 3 code agree):

- **`ActiveSource` vocabulary retires `"absent"`.** The old wire had two "nothing configured" values (`"absent"` for a bearer-like scheme with no key, `"none"` for a scheme that never wants one); the new wire has one (`"none"`), disambiguated by the instance's own `credentialRequired` and `auth` fields. `activeSourceLabel` is the single function that turns every `ActiveSource` value into a label, including the three-way split on `"none"`.
- **OAuth no longer coexists with a stored key/env var on the same instance.** `authModesFor` (Task 9) maps each `auth` scheme to a fixed, non-overlapping `authModes` set — `oauth-openai-codex` → `["oauth"]` only, `bearer`/`header` → `["apiKey"]` only — so an instance is never simultaneously oauth- and key-capable (that split is now two separate instances, e.g. `openai` vs `openai-codex`, spec §14.1). `credentialLayers` therefore only has one remaining real "shadow" case: an environment variable left set behind a stored key that now wins (`store` beats `env` in the resolution order, spec §10).
- **`Auth` (transport scheme) is a real field now**, so `"file"` is renamed `"store"` in the vocabulary, and the old `type`/`apiStyle` fields are gone — `providerId` replaces `type` for display/grouping, `protocol` (always present) replaces `apiStyle` in the row's trailing text, and the openai-only API-style radio in the Add/Edit dialogs is deleted (Protocol is no longer openai-specific data the form special-cases).
- **`writesRefused` disables only `evener/instance/*` writes** (Add, Edit, Remove, Set default) — never Set key/Sign in/Clear/Test credentials, which write the credentials store or an OAuth record, not `providers.toml`.
- **The Edit dialog keeps Base URL only.** `InstanceEditParams` also carries `protocol`/`surface`/`vars` overrides, but the pane's only tested/spec-mandated way to set those is the Add form's provider-driven fields (spec §11.3 only calls out `VarsEnv` driving the add form); Edit's job stays "nudge an existing instance's endpoint," and it now sends `baseUrl` only when it actually changed (`InstanceEditParams`'s own "empty fields mean unchanged" contract).
- **`userLayer` is tracked in the store but not rendered separately.** The Go `Diagnostics()` list (Task 9) already includes the user-layer note as its first entry, so a separate on-screen line would duplicate it.

- [ ] **Step 1: Write the failing tests** — real vitest code, house style (`FakeClient`, `connectFakeClient()`, a local `instance()`/`provider()` fixture builder per file, `userEvent`, `screen`/`within`/`waitFor`). Every file below is a full replacement of the existing file at that path.

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels.test.ts`**

  ```ts
  // @vitest-environment node
  import { describe, expect, test } from "vitest";
  import type { InstanceEntry } from "../../../../protocol/types.gen";
  import {
    activeSourceLabel,
    credentialLayers,
    groupByProvider,
    keylessByDesign,
    unconfiguredLabel,
  } from "./credentialLabels";

  function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
    return {
      protocol: "openai-chat",
      auth: "bearer",
      implicit: false,
      isDefault: false,
      activeSource: "none",
      hasStoredOAuth: false,
      credentialRequired: true,
      ...overrides,
    };
  }

  describe("activeSourceLabel", () => {
    test.each([
      ["api_key", "Configured via providers.toml"],
      ["credential_headers", "Configured via a credential header"],
      ["store", "Configured via stored API key"],
      ["adc", "Configured via Application Default Credentials"],
    ] as const)("%s -> %s", (activeSource, label) => {
      expect(activeSourceLabel(instance({ name: "a", providerId: "x", activeSource }))).toBe(label);
    });

    test("env:<VAR> carries the variable name", () => {
      expect(activeSourceLabel(instance({ name: "a", providerId: "x", activeSource: "env:GROQ_API_KEY" }))).toBe(
        "Configured via environment variable (GROQ_API_KEY)",
      );
    });

    test("oauth includes the signed-in email when present", () => {
      expect(
        activeSourceLabel(
          instance({
            name: "a",
            providerId: "openai-codex",
            auth: "oauth-openai-codex",
            activeSource: "oauth",
            storedEmail: "me@example.com",
          }),
        ),
      ).toBe("Configured via OAuth (me@example.com)");
    });

    test("oauth with no stored email", () => {
      expect(
        activeSourceLabel(instance({ name: "a", providerId: "openai-codex", auth: "oauth-openai-codex", activeSource: "oauth" })),
      ).toBe("Configured via OAuth");
    });

    test("none + credentialRequired -> Not configured", () => {
      expect(
        activeSourceLabel(
          instance({ name: "a", providerId: "anthropic", auth: "bearer", activeSource: "none", credentialRequired: true }),
        ),
      ).toBe("Not configured");
    });

    test("none + auth none -> No credentials required", () => {
      expect(
        activeSourceLabel(
          instance({ name: "a", providerId: "ollama", auth: "none", activeSource: "none", credentialRequired: false }),
        ),
      ).toBe("No credentials required");
    });

    test("none + auth optional-bearer -> No key set · optional", () => {
      expect(
        activeSourceLabel(
          instance({
            name: "a",
            providerId: "openai-compatible",
            auth: "optional-bearer",
            activeSource: "none",
            credentialRequired: false,
          }),
        ),
      ).toBe("No key set · optional");
    });

    test("falls back to the raw value for an unrecognized activeSource", () => {
      expect(activeSourceLabel(instance({ name: "a", providerId: "x", activeSource: "mystery" }))).toBe("mystery");
    });
  });

  describe("credentialLayers", () => {
    test("empty when activeSource is none", () => {
      expect(credentialLayers(instance({ name: "a", providerId: "x", activeSource: "none" }))).toEqual([]);
    });

    test("a single effective layer matches activeSourceLabel", () => {
      const inst = instance({ name: "a", providerId: "anthropic", activeSource: "store", hasStoredFile: true });
      expect(credentialLayers(inst)).toEqual([
        { source: "store", label: "Configured via stored API key", effective: true },
      ]);
    });

    test("an environment variable left set behind a now-effective store entry shows as shadowed", () => {
      const inst = instance({
        name: "a",
        providerId: "anthropic",
        activeSource: "store",
        hasStoredFile: true,
        envVar: "ANTHROPIC_API_KEY",
      });
      expect(credentialLayers(inst)).toEqual([
        { source: "store", label: "Configured via stored API key", effective: true },
        {
          source: "env:ANTHROPIC_API_KEY",
          label: "Configured via environment variable (ANTHROPIC_API_KEY)",
          effective: false,
        },
      ]);
    });

    test("an env-effective instance with no store entry shows only the one env layer", () => {
      const inst = instance({
        name: "a",
        providerId: "anthropic",
        activeSource: "env:ANTHROPIC_API_KEY",
        envVar: "ANTHROPIC_API_KEY",
      });
      expect(credentialLayers(inst)).toEqual([
        { source: "env:ANTHROPIC_API_KEY", label: "Configured via environment variable (ANTHROPIC_API_KEY)", effective: true },
      ]);
    });

    test("an oauth-effective instance shows only the oauth layer - oauth-openai-codex never shares an instance with store/env", () => {
      const inst = instance({
        name: "a",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        activeSource: "oauth",
        hasStoredOAuth: true,
        storedEmail: "me@example.com",
      });
      expect(credentialLayers(inst)).toEqual([
        { source: "oauth", label: "Configured via OAuth (me@example.com)", effective: true },
      ]);
    });
  });

  describe("keylessByDesign", () => {
    test("true when nothing is active and no credential is required (auth: none)", () => {
      expect(
        keylessByDesign(
          instance({ name: "ollama", providerId: "ollama", auth: "none", activeSource: "none", credentialRequired: false }),
        ),
      ).toBe(true);
    });

    test("true when nothing is active and no credential is required (auth: optional-bearer)", () => {
      expect(
        keylessByDesign(
          instance({
            name: "llama",
            providerId: "openai-compatible",
            auth: "optional-bearer",
            activeSource: "none",
            credentialRequired: false,
          }),
        ),
      ).toBe(true);
    });

    test("false when a credential is required, even with nothing active", () => {
      expect(
        keylessByDesign(
          instance({ name: "a", providerId: "anthropic", auth: "bearer", activeSource: "none", credentialRequired: true }),
        ),
      ).toBe(false);
    });

    test("false once something is active, regardless of credentialRequired", () => {
      expect(
        keylessByDesign(
          instance({
            name: "a",
            providerId: "openai-compatible",
            auth: "optional-bearer",
            activeSource: "store",
            credentialRequired: false,
            hasStoredFile: true,
          }),
        ),
      ).toBe(false);
    });
  });

  describe("unconfiguredLabel", () => {
    test("null once a layer is active", () => {
      expect(
        unconfiguredLabel(instance({ name: "a", providerId: "x", activeSource: "store", hasStoredFile: true })),
      ).toBeNull();
    });

    test("mirrors activeSourceLabel when nothing is active", () => {
      expect(
        unconfiguredLabel(
          instance({ name: "a", providerId: "anthropic", auth: "bearer", activeSource: "none", credentialRequired: true }),
        ),
      ).toBe("Not configured");
    });
  });

  describe("groupByProvider", () => {
    test("groups instances by providerId in first-seen order, not re-sorted", () => {
      const openaiA = instance({ name: "work", providerId: "openai" });
      const anthropicA = instance({ name: "personal", providerId: "anthropic" });
      const openaiB = instance({ name: "side", providerId: "openai" });
      expect(groupByProvider([openaiA, anthropicA, openaiB])).toEqual([
        { providerId: "openai", instances: [openaiA, openaiB] },
        { providerId: "anthropic", instances: [anthropicA] },
      ]);
    });

    test("a custom-named instance's base never fragments the group - both land under the same providerId", () => {
      const implicitGroq = instance({ name: "groq", providerId: "groq", implicit: true });
      const customOnGroq = instance({ name: "work", providerId: "groq", base: "groq" });
      expect(groupByProvider([implicitGroq, customOnGroq])).toEqual([
        { providerId: "groq", instances: [implicitGroq, customOnGroq] },
      ]);
    });

    test("empty list yields no groups", () => {
      expect(groupByProvider([])).toEqual([]);
    });
  });
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceRow.test.tsx`**

  ```tsx
  import { cleanup, render, screen } from "@testing-library/react";
  import userEvent from "@testing-library/user-event";
  import { afterEach, describe, expect, test, vi } from "vitest";
  import type { InstanceEntry } from "../../../../protocol/types.gen";
  import { InstanceRow } from "./InstanceRow";

  afterEach(cleanup);

  function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
    return {
      protocol: "openai-chat",
      auth: "bearer",
      implicit: false,
      isDefault: false,
      activeSource: "none",
      hasStoredOAuth: false,
      credentialRequired: true,
      ...overrides,
    };
  }

  function noopHandlers() {
    return {
      onTestCredentials: vi.fn(),
      onSetApiKey: vi.fn(),
      onOAuthStart: vi.fn(),
      onEdit: vi.fn(),
      onClear: vi.fn(),
      onRemove: vi.fn(),
      onSetDefault: vi.fn(),
    };
  }

  describe("row actions are conditionally rendered", () => {
    test("Set key only when authModes includes apiKey", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "a", providerId: "x", authModes: ["oauth"] })} {...handlers} />);
      expect(screen.queryByRole("button", { name: /set key|replace key/i })).toBeNull();
    });

    test("Sign in… only when authModes includes oauth", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "a", providerId: "x", authModes: ["apiKey"] })} {...handlers} />);
      expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
    });

    test("Clear only when activeSource is store or oauth", () => {
      const handlers = noopHandlers();
      const { rerender } = render(
        <InstanceRow instance={instance({ name: "a", providerId: "x", activeSource: "env:X_API_KEY" })} {...handlers} />,
      );
      expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
      rerender(
        <InstanceRow
          instance={instance({ name: "a", providerId: "x", activeSource: "store", hasStoredFile: true })}
          {...handlers}
        />,
      );
      expect(screen.getByRole("button", { name: "Clear" })).toBeTruthy();
    });

    test("Edit and Remove are always present for a non-implicit instance", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "a", providerId: "x" })} {...handlers} />);
      expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
    });

    test("make default only when not already default", () => {
      const handlers = noopHandlers();
      const { rerender } = render(
        <InstanceRow instance={instance({ name: "a", providerId: "x", isDefault: false })} {...handlers} />,
      );
      expect(screen.getByRole("button", { name: /make default/i })).toBeTruthy();
      rerender(<InstanceRow instance={instance({ name: "a", providerId: "x", isDefault: true })} {...handlers} />);
      expect(screen.queryByRole("button", { name: /make default/i })).toBeNull();
    });
  });

  // implicit is the wire's own "exists from the environment, not from
  // providers.toml" flag (InstanceEntry, appwire/types.go) - removal of an
  // implicit instance is refused server-side (spec §11.3), so the row must
  // not even offer the button, and a "from environment" badge tells the user
  // why Edit here writes a shadow instead of changing the instance itself.
  describe("implicit instances", () => {
    test("hides Remove and shows a 'from environment' badge", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "groq", providerId: "groq", implicit: true })} {...handlers} />);
      expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
      expect(screen.getByText("from environment")).toBeTruthy();
    });

    test("a non-implicit instance shows Remove and no badge", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({ name: "work", providerId: "groq", base: "groq", implicit: false })}
          {...handlers}
        />,
      );
      expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
      expect(screen.queryByText("from environment")).toBeNull();
    });

    test("Edit is still offered on an implicit instance (writes a shadow)", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "groq", providerId: "groq", implicit: true })} {...handlers} />);
      expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
    });
  });

  describe("Set/Replace key label", () => {
    test("'Set key' when no file-sourced key exists", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({ name: "a", providerId: "x", authModes: ["apiKey"], hasStoredFile: false })}
          {...handlers}
        />,
      );
      expect(screen.getByRole("button", { name: "Set key" })).toBeTruthy();
    });

    test("'Replace key' whenever a file-sourced key exists", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "x",
            authModes: ["apiKey"],
            hasStoredFile: true,
            activeSource: "store",
          })}
          {...handlers}
        />,
      );
      expect(screen.getByRole("button", { name: "Replace key" })).toBeTruthy();
    });
  });

  describe("Sign in / Refresh OAuth label", () => {
    test("'Sign in…' when no OAuth is stored", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "openai-codex",
            auth: "oauth-openai-codex",
            authModes: ["oauth"],
            hasStoredOAuth: false,
          })}
          {...handlers}
        />,
      );
      expect(screen.getByRole("button", { name: "Sign in…" })).toBeTruthy();
    });

    test("'Refresh OAuth' once signed in", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "openai-codex",
            auth: "oauth-openai-codex",
            authModes: ["oauth"],
            hasStoredOAuth: true,
            activeSource: "oauth",
          })}
          {...handlers}
        />,
      );
      expect(screen.getByRole("button", { name: "Refresh OAuth" })).toBeTruthy();
    });

    test("a bearer-auth instance never offers Sign in, even with a stored file key", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "openai",
            auth: "bearer",
            authModes: ["apiKey"],
            hasStoredFile: true,
            activeSource: "store",
          })}
          {...handlers}
        />,
      );
      expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
    });
  });

  describe("credential display and badges", () => {
    test("an unconfigured instance shows 'Not configured' and no Clear button", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "a", providerId: "x", activeSource: "none" })} {...handlers} />);
      expect(screen.getByText("Not configured")).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
    });

    test("an environment variable left set behind a now-effective store entry shows effective + shadowed badges", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "anthropic",
            hasStoredFile: true,
            envVar: "ANTHROPIC_API_KEY",
            activeSource: "store",
          })}
          {...handlers}
        />,
      );
      expect(screen.getByText("effective")).toBeTruthy();
      expect(screen.getByText("shadowed")).toBeTruthy();
    });

    test("protocol and base URL trailing text", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({ name: "a", providerId: "openai", protocol: "openai-responses", baseUrl: "https://x" })}
          {...handlers}
        />,
      );
      expect(screen.getByText("openai-responses · base https://x")).toBeTruthy();
    });

    test("protocol alone when baseUrl is empty", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow instance={instance({ name: "a", providerId: "openai", protocol: "openai-chat" })} {...handlers} />,
      );
      expect(screen.getByText("openai-chat")).toBeTruthy();
    });

    test("the default badge shows when isDefault", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "a", providerId: "x", isDefault: true })} {...handlers} />);
      expect(screen.getByText(/default/i)).toBeTruthy();
    });
  });

  // The heading dot is the glyph half of what the credential label says in
  // words, so the two have to agree about the same instance. StatusDot's only
  // observable difference between "idle" and "ended" is its accessible name
  // (both states share the neutral token family - src/widgets/statusdot), so
  // that name is what these assert on.
  describe("the heading dot agrees with the credential label", () => {
    test("a keyless gateway - no key, none needed - is not announced as ended", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "llama",
            providerId: "openai-compatible",
            auth: "optional-bearer",
            baseUrl: "http://127.0.0.1:8080/v1",
            activeSource: "none",
            credentialRequired: false,
          })}
          {...handlers}
        />,
      );
      expect(screen.getByText("No key set · optional")).toBeTruthy();
      expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
    });

    test("an auth-none provider - one that authenticates nothing - is not announced as ended", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "ollama",
            providerId: "ollama",
            auth: "none",
            activeSource: "none",
            credentialRequired: false,
          })}
          {...handlers}
        />,
      );
      expect(screen.getByText("No credentials required")).toBeTruthy();
      expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
    });

    test("a provider whose required key is missing keeps the ended dot", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "anthropic",
            auth: "bearer",
            activeSource: "none",
            credentialRequired: true,
          })}
          {...handlers}
        />,
      );
      expect(screen.getByText("Not configured")).toBeTruthy();
      expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
    });
  });

  describe("action callbacks fire", () => {
    test("clicking each action calls its handler", async () => {
      const handlers = noopHandlers();
      const user = userEvent.setup();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "anthropic",
            authModes: ["apiKey"],
            hasStoredFile: true,
            activeSource: "store",
          })}
          {...handlers}
        />,
      );
      await user.click(screen.getByRole("button", { name: "Replace key" }));
      expect(handlers.onSetApiKey).toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "Clear" }));
      expect(handlers.onClear).toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "Edit" }));
      expect(handlers.onEdit).toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "Remove" }));
      expect(handlers.onRemove).toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: /make default/i }));
      expect(handlers.onSetDefault).toHaveBeenCalled();
    });

    test("clicking Sign in calls its handler", async () => {
      const handlers = noopHandlers();
      const user = userEvent.setup();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "openai-codex",
            auth: "oauth-openai-codex",
            authModes: ["oauth"],
          })}
          {...handlers}
        />,
      );
      await user.click(screen.getByRole("button", { name: "Sign in…" }));
      expect(handlers.onOAuthStart).toHaveBeenCalled();
    });

    test("clicking Test credentials calls its handler", async () => {
      const handlers = noopHandlers();
      const user = userEvent.setup();
      render(<InstanceRow instance={instance({ name: "a", providerId: "x" })} {...handlers} />);

      await user.click(screen.getByRole("button", { name: "Test credentials" }));

      expect(handlers.onTestCredentials).toHaveBeenCalledTimes(1);
    });

    test("pending verification disables only the Test credentials action", () => {
      const handlers = noopHandlers();
      render(<InstanceRow instance={instance({ name: "a", providerId: "x" })} {...handlers} testCredentialsPending />);

      expect((screen.getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(true);
      expect((screen.getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(false);
      expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
    });
  });

  describe("writesRefused disables instance-CRUD actions only", () => {
    test("disables Edit, Remove, and Set default", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({ name: "a", providerId: "x", isDefault: false })}
          {...handlers}
          writesRefused
        />,
      );
      expect((screen.getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(true);
      expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
      expect((screen.getByRole("button", { name: /make default/i }) as HTMLButtonElement).disabled).toBe(true);
    });

    test("leaves Test credentials, Set/Replace key, and Clear enabled", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({
            name: "a",
            providerId: "x",
            authModes: ["apiKey"],
            hasStoredFile: true,
            activeSource: "store",
          })}
          {...handlers}
          writesRefused
        />,
      );
      expect((screen.getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(false);
      expect((screen.getByRole("button", { name: "Replace key" }) as HTMLButtonElement).disabled).toBe(false);
      expect((screen.getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(false);
    });

    test("an implicit instance under writesRefused still has no Remove button at all", () => {
      const handlers = noopHandlers();
      render(
        <InstanceRow
          instance={instance({ name: "groq", providerId: "groq", implicit: true })}
          {...handlers}
          writesRefused
        />,
      );
      expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
    });
  });
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.test.tsx`**

  ```tsx
  import { cleanup, render, screen, waitFor } from "@testing-library/react";
  import userEvent from "@testing-library/user-event";
  import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
  import { FakeClient } from "../../../../protocol/testing/fakeClient";
  import type { InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
  import { connectionStore } from "../../../../stores/connection";
  import { resetCredentialsStoreForTests } from "../../../../stores/credentials";
  import { Toast } from "../../../../widgets";
  import { AddInstanceDialog, ApiKeyDialog, EditInstanceDialog } from "./instanceDialogs";

  function connectFakeClient(): FakeClient {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    return fake;
  }

  function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
    return {
      protocol: "openai-chat",
      auth: "bearer",
      implicit: false,
      isDefault: false,
      activeSource: "none",
      hasStoredOAuth: false,
      credentialRequired: true,
      ...overrides,
    };
  }

  function provider(overrides: Partial<ProviderDescriptor> & Pick<ProviderDescriptor, "id">): ProviderDescriptor {
    return {
      protocol: "openai-chat",
      auth: "bearer",
      implicit: true,
      ...overrides,
    };
  }

  const ANTHROPIC = provider({ id: "anthropic", protocol: "anthropic" });
  const VERTEX = provider({
    id: "google-vertex-anthropic",
    protocol: "anthropic",
    auth: "gcp-adc",
    varsEnv: ["GOOGLE_VERTEX_PROJECT", "GOOGLE_VERTEX_LOCATION"],
  });
  const BEDROCK = provider({ id: "amazon-bedrock", protocol: "anthropic", auth: "gcp-adc", varsEnv: ["AWS_REGION"] });

  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    resetCredentialsStoreForTests();
  });

  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    cleanup();
  });

  describe("AddInstanceDialog", () => {
    test("Base provider select is populated from availableProviders", () => {
      connectFakeClient();
      render(<AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
      const select = screen.getByLabelText("Base provider") as HTMLSelectElement;
      expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "anthropic", "google-vertex-anthropic"]);
    });

    test("a provider's display name is used as its option label when present", () => {
      connectFakeClient();
      render(
        <AddInstanceDialog
          availableProviders={[provider({ id: "anthropic", name: "Anthropic" })]}
          onCancel={() => {}}
          onSuccess={() => {}}
        />,
      );
      expect(screen.getByRole("option", { name: "Anthropic" })).toBeTruthy();
    });

    test("no variable inputs until a base with varsEnv is selected", () => {
      connectFakeClient();
      render(<AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
      expect(screen.queryByLabelText("GOOGLE_VERTEX_PROJECT")).toBeNull();
    });

    test("selecting a base renders one input per varsEnv entry, labeled by name", async () => {
      connectFakeClient();
      render(<AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
      await userEvent.setup().selectOptions(screen.getByLabelText("Base provider"), "google-vertex-anthropic");
      expect(screen.getByLabelText("GOOGLE_VERTEX_PROJECT")).toBeTruthy();
      expect(screen.getByLabelText("GOOGLE_VERTEX_LOCATION")).toBeTruthy();
    });

    test("switching base providers clears the previous base's variable inputs and values", async () => {
      connectFakeClient();
      const user = userEvent.setup();
      render(<AddInstanceDialog availableProviders={[BEDROCK, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
      await user.selectOptions(screen.getByLabelText("Base provider"), "amazon-bedrock");
      await user.type(screen.getByLabelText("AWS_REGION"), "us-east-1");
      await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-anthropic");
      expect(screen.queryByLabelText("AWS_REGION")).toBeNull();
      expect((screen.getByLabelText("GOOGLE_VERTEX_PROJECT") as HTMLInputElement).value).toBe("");
    });

    test("client-side validation: Base provider required, then Name required", async () => {
      connectFakeClient();
      const user = userEvent.setup();
      render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
      await user.click(screen.getByRole("button", { name: "Create" }));
      expect(screen.getByText("Base provider is required.")).toBeTruthy();
      await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
      await user.click(screen.getByRole("button", { name: "Create" }));
      expect(screen.getByText("Name is required.")).toBeTruthy();
    });

    test("a credential header without $ is rejected client-side, with no RPC", async () => {
      const fake = connectFakeClient();
      const create = vi.fn();
      fake.on("evener/instance/create", create);
      const user = userEvent.setup();
      render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
      await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
      await user.type(screen.getByLabelText("Name"), "work");
      await user.type(screen.getByLabelText(/credential header/i), "Authorization=Bearer secret");
      await user.click(screen.getByRole("button", { name: "Create" }));
      expect(screen.getByText("Credential header must reference a $VARIABLE, never a literal secret.")).toBeTruthy();
      expect(create).not.toHaveBeenCalled();
    });

    test("a credential header with $ is accepted and sent verbatim", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/create", (params) => {
        expect(params).toEqual({
          name: "work",
          base: "anthropic",
          baseUrl: "",
          credentialHeader: "Authorization=Bearer $PORTKEY_KEY",
        });
        return { instances: [], availableProviders: [] };
      });
      const user = userEvent.setup();
      render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
      await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
      await user.type(screen.getByLabelText("Name"), "work");
      await user.type(screen.getByLabelText(/credential header/i), "Authorization=Bearer $PORTKEY_KEY");
      await user.click(screen.getByRole("button", { name: "Create" }));
      await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
    });

    test("api-key-env sends the bare variable name", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/create", (params) => {
        expect(params).toEqual({ name: "work", base: "anthropic", baseUrl: "", apiKeyEnv: "PORTKEY_KEY" });
        return { instances: [], availableProviders: [] };
      });
      const user = userEvent.setup();
      render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
      await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
      await user.type(screen.getByLabelText("Name"), "work");
      await user.type(screen.getByLabelText(/api key environment variable/i), "PORTKEY_KEY");
      await user.click(screen.getByRole("button", { name: "Create" }));
      await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
    });

    test("variable inputs are sent trimmed, with blank ones omitted", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/create", (params) => {
        expect(params).toEqual({
          name: "vertex",
          base: "google-vertex-anthropic",
          baseUrl: "",
          vars: { GOOGLE_VERTEX_PROJECT: "my-proj" },
        });
        return { instances: [], availableProviders: [] };
      });
      const user = userEvent.setup();
      render(<AddInstanceDialog availableProviders={[VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
      await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-anthropic");
      await user.type(screen.getByLabelText("Name"), "vertex");
      await user.type(screen.getByLabelText("GOOGLE_VERTEX_PROJECT"), "  my-proj  ");
      await user.click(screen.getByRole("button", { name: "Create" }));
      await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
    });

    test("submit calls instanceCreate and, on success, toasts + calls onSuccess", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/create", (params) => {
        expect(params).toEqual({ name: "work", base: "anthropic", baseUrl: "https://x" });
        return { instances: [], availableProviders: [] };
      });
      const onSuccess = vi.fn();
      const user = userEvent.setup();
      render(
        <>
          <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
          <Toast />
        </>,
      );
      await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
      await user.type(screen.getByLabelText("Name"), "work");
      await user.type(screen.getByLabelText(/base url/i), "https://x");
      await user.click(screen.getByRole("button", { name: "Create" }));
      await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
      expect(screen.getAllByText("Created instance work").length).toBeGreaterThan(0);
    });

    test("a create failure shows an inline error and a 'Create failed' toast, without calling onSuccess", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/create", () => {
        throw new Error("name already exists");
      });
      const onSuccess = vi.fn();
      const user = userEvent.setup();
      render(
        <>
          <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
          <Toast />
        </>,
      );
      await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
      await user.type(screen.getByLabelText("Name"), "work");
      await user.click(screen.getByRole("button", { name: "Create" }));
      await screen.findByText("name already exists");
      expect(screen.getAllByText("Create failed: name already exists").length).toBeGreaterThan(0);
      expect(onSuccess).not.toHaveBeenCalled();
    });
  });

  describe("EditInstanceDialog", () => {
    test("Base URL is pre-filled from the instance", () => {
      connectFakeClient();
      render(
        <EditInstanceDialog
          instance={instance({ name: "work", providerId: "anthropic", baseUrl: "https://existing" })}
          onCancel={() => {}}
          onSuccess={() => {}}
        />,
      );
      expect(screen.getByLabelText(/base url/i)).toHaveProperty("value", "https://existing");
    });

    test("submit with an unchanged Base URL sends only { name }", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/edit", (params) => {
        expect(params).toEqual({ name: "work" });
        return { instances: [], availableProviders: [] };
      });
      const onSuccess = vi.fn();
      const user = userEvent.setup();
      render(
        <>
          <EditInstanceDialog
            instance={instance({ name: "work", providerId: "anthropic", baseUrl: "https://existing" })}
            onCancel={() => {}}
            onSuccess={onSuccess}
          />
          <Toast />
        </>,
      );
      await user.click(screen.getByRole("button", { name: "Save" }));
      await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    });

    test("submit with a changed Base URL sends { name, baseUrl }", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/edit", (params) => {
        expect(params).toEqual({ name: "work", baseUrl: "https://x" });
        return { instances: [], availableProviders: [] };
      });
      const onSuccess = vi.fn();
      const user = userEvent.setup();
      render(
        <>
          <EditInstanceDialog
            instance={instance({ name: "work", providerId: "anthropic" })}
            onCancel={() => {}}
            onSuccess={onSuccess}
          />
          <Toast />
        </>,
      );
      await user.clear(screen.getByLabelText(/base url/i));
      await user.type(screen.getByLabelText(/base url/i), "https://x");
      await user.click(screen.getByRole("button", { name: "Save" }));
      await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
      expect(screen.getAllByText("Saved work").length).toBeGreaterThan(0);
    });

    test("a failure shows an inline error and an 'Edit failed' toast", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/edit", () => {
        throw new Error("boom");
      });
      const user = userEvent.setup();
      render(
        <>
          <EditInstanceDialog
            instance={instance({ name: "work", providerId: "anthropic" })}
            onCancel={() => {}}
            onSuccess={() => {}}
          />
          <Toast />
        </>,
      );
      await user.click(screen.getByRole("button", { name: "Save" }));
      await screen.findByText("boom");
      expect(screen.getAllByText("Edit failed: boom").length).toBeGreaterThan(0);
    });
  });

  describe("ApiKeyDialog", () => {
    test("submitting an empty (trimmed) value silently cancels - no RPC", async () => {
      const fake = connectFakeClient();
      const setKey = vi.fn();
      fake.on("evener/auth/apiKey/set", setKey);
      const onCancel = vi.fn();
      const user = userEvent.setup();
      render(
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          onCancel={onCancel}
          onSuccess={() => {}}
        />,
      );
      await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "   ");
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(setKey).not.toHaveBeenCalled();
      expect(onCancel).toHaveBeenCalled();
    });

    test("a non-empty key calls authApiKeySet, refreshes, toasts, and calls onSuccess", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/apiKey/set", (params) => {
        expect(params).toEqual({ provider: "work", value: "sk-secret" });
        return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
      });
      fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
      const onSuccess = vi.fn();
      const user = userEvent.setup();
      render(
        <>
          <ApiKeyDialog
            instance={instance({ name: "work", providerId: "anthropic" })}
            onCancel={() => {}}
            onSuccess={onSuccess}
          />
          <Toast />
        </>,
      );
      await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
      await user.click(screen.getByRole("button", { name: "Save" }));
      await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
      expect(screen.getAllByText("API key saved for work").length).toBeGreaterThan(0);
    });

    test("a failure shows an inline error and a 'Save failed' toast", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/apiKey/set", () => {
        throw new Error("rejected");
      });
      const user = userEvent.setup();
      render(
        <>
          <ApiKeyDialog
            instance={instance({ name: "work", providerId: "anthropic" })}
            onCancel={() => {}}
            onSuccess={() => {}}
          />
          <Toast />
        </>,
      );
      await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "sk-bad");
      await user.click(screen.getByRole("button", { name: "Save" }));
      await screen.findByText("rejected");
      expect(screen.getAllByText("Save failed: rejected").length).toBeGreaterThan(0);
    });

    test("the API key input is type=password", () => {
      connectFakeClient();
      render(
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          onCancel={() => {}}
          onSuccess={() => {}}
        />,
      );
      expect(screen.getByLabelText(/api key/i, { selector: "input" }).getAttribute("type")).toBe("password");
    });
  });
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.test.tsx`**

  ```tsx
  import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
  import userEvent from "@testing-library/user-event";
  import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
  import { FakeClient } from "../../../../protocol/testing/fakeClient";
  import type { AuthTestResponse, InstanceEntry, InstanceListResponse } from "../../../../protocol/types.gen";
  import { connectionStore } from "../../../../stores/connection";
  import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
  import { Toast } from "../../../../widgets";
  import { CredentialsSection } from "./CredentialsSection";

  function connectFakeClient(): FakeClient {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    return fake;
  }

  function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
    return {
      protocol: "openai-chat",
      auth: "bearer",
      implicit: false,
      isDefault: false,
      activeSource: "none",
      hasStoredOAuth: false,
      credentialRequired: true,
      ...overrides,
    };
  }

  const WORK = instance({
    name: "work",
    providerId: "anthropic",
    authModes: ["apiKey"],
    isDefault: true,
    hasStoredFile: true,
    activeSource: "store",
  });
  const PERSONAL = instance({
    name: "personal",
    providerId: "openai-codex",
    auth: "oauth-openai-codex",
    authModes: ["oauth"],
  });
  const LIST: InstanceListResponse = { instances: [WORK, PERSONAL], availableProviders: [] };

  function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (reason?: unknown) => void;
    const promise = new Promise<T>((resolvePromise, rejectPromise) => {
      resolve = resolvePromise;
      reject = rejectPromise;
    });
    return { promise, resolve, reject };
  }

  async function advanceTime(milliseconds: number): Promise<void> {
    await act(() => vi.advanceTimersByTimeAsync(milliseconds));
  }

  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    resetCredentialsStoreForTests();
  });

  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    cleanup();
    vi.useRealTimers();
  });

  describe("initial load", () => {
    test("fetches evener/instance/list on mount and groups rows by providerId", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("work");
      expect(screen.getByText("anthropic")).toBeTruthy();
      expect(screen.getByText("openai-codex")).toBeTruthy();
      expect(screen.getByText("personal")).toBeTruthy();
    });

    test("empty state", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("No provider instances configured.");
    });

    test("load failure shows an error message", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => {
        throw new Error("network down");
      });
      render(<CredentialsSection sectionId="credentials" />);
      // error is converted via friendlyErrorMessage: raw JS errors become the generic message
      await screen.findByText(/Failed to load: Something went wrong/);
      // Assert the raw string no longer appears
      expect(screen.queryByText(/network down/)).toBeNull();
    });

    // The integration-level proof of useConnectedEffect: a direct deep link
    // to /credentials can mount this section before AppShell's own connect()
    // handshake finishes (see that hook's own doc comment) - the initial
    // fetch must defer until the connection is actually ready, then fire
    // exactly once, rather than throwing (unhandled) or never firing at all.
    test("mounting before the connection is ready defers the initial load, which then fires exactly once it becomes ready", async () => {
      const fake = new FakeClient("idle"); // NOT ready at mount
      connectionStore.getState().connect(fake);
      let calls = 0;
      fake.on("evener/instance/list", () => {
        calls += 1;
        return LIST;
      });
      render(<CredentialsSection sectionId="credentials" />);
      // Give any (wrongly) eager fetch attempt every chance to fire before
      // asserting it hasn't - a real bug here would throw synchronously into
      // an unhandled rejection, not silently pass this check.
      await act(() => Promise.resolve());
      expect(calls).toBe(0);

      act(() => {
        fake.emitReady();
      });

      await screen.findByText("work");
      expect(calls).toBe(1);
    });
  });

  describe("credential verification", () => {
    test("sends the exact custom instance name and shows local pending state until the deferred response arrives", async () => {
      const fake = connectFakeClient();
      const customName = "OpenAI / team-east:prod";
      const custom = instance({ name: customName, providerId: "openai", authModes: ["apiKey"] });
      const response = deferred<AuthTestResponse>();
      fake.on("evener/instance/list", () => ({ instances: [custom], availableProviders: [] }));
      fake.on("evener/auth/test", (params) => {
        expect(params).toEqual({ provider: customName });
        return response.promise;
      });
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText(customName);

      const row = screen.getByText(customName).closest("li");
      expect(row).not.toBeNull();
      const testButton = within(row!).getByRole("button", { name: "Test credentials" });
      await userEvent.setup().click(testButton);

      expect((within(row!).getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(
        true,
      );
      expect((within(row!).getByRole("button", { name: "Edit" }) as HTMLButtonElement).disabled).toBe(false);
      expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);

      response.resolve({ provider: customName, status: "success", message: "Credentials verified." });
      expect((await screen.findByRole("status")).textContent).toContain("Credentials verified.");
      expect((within(row!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
        false,
      );
    });

    test("suppresses duplicate clicks for one pending instance while another instance stays enabled", async () => {
      const fake = connectFakeClient();
      const workResponse = deferred<AuthTestResponse>();
      const personalResponse = deferred<AuthTestResponse>();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/auth/test", (params) => {
        if (params.provider === WORK.name) return workResponse.promise;
        if (params.provider === PERSONAL.name) return personalResponse.promise;
        throw new Error(`unexpected provider ${params.provider}`);
      });
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText(WORK.name);
      const user = userEvent.setup();
      const workRow = screen.getByText(WORK.name).closest("li");
      const personalRow = screen.getByText(PERSONAL.name).closest("li");
      expect(workRow).not.toBeNull();
      expect(personalRow).not.toBeNull();

      const workButton = within(workRow!).getByRole("button", { name: "Test credentials" });
      await user.click(workButton);
      await user.click(workButton);
      expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(1);
      expect(
        (within(workRow!).getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled,
      ).toBe(true);
      expect(
        (within(personalRow!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled,
      ).toBe(false);

      await user.click(within(personalRow!).getByRole("button", { name: "Test credentials" }));
      expect(fake.calls.filter((call) => call.method === "evener/auth/test")).toHaveLength(2);

      workResponse.resolve({ provider: WORK.name, status: "success", message: "Credentials verified." });
      personalResponse.resolve({ provider: PERSONAL.name, status: "success", message: "Credentials verified." });
      await waitFor(() => {
        expect((within(workRow!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(
          false,
        );
        expect(
          (within(personalRow!).getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled,
        ).toBe(false);
      });
    });

    test.each([
      ["success", "Credentials verified."],
      ["missing", "No credentials are configured for this instance. Add a key or sign in first."],
      ["auth_rejected", "The provider rejected these credentials. Replace the key or sign in again."],
      ["endpoint_failure", "The provider endpoint could not be reached. Check the endpoint and network connection."],
      ["configuration_failure", "Provider configuration could not be loaded. Check the instance settings."],
      ["unsupported", "This provider does not support harmless credential verification."],
    ] as const)("renders the safe %s status and message", async (status, message) => {
      const fake = connectFakeClient();
      const response = deferred<AuthTestResponse>();
      fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
      fake.on("evener/auth/test", () => response.promise);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText(WORK.name);
      await userEvent.setup().click(screen.getByRole("button", { name: "Test credentials" }));
      response.resolve({ provider: WORK.name, status, message });

      const statusNode = await screen.findByRole("status");
      expect(statusNode.textContent).toBe(`${status}: ${message}`);
    });

    test("does not render a supplied secret from a response message", async () => {
      const fake = connectFakeClient();
      const secret = "sk-live-do-not-render";
      fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
      fake.on("evener/auth/test", async () => ({ provider: WORK.name, status: "auth_rejected", message: secret }));
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText(WORK.name);
      await userEvent.setup().click(screen.getByRole("button", { name: "Test credentials" }));

      const status = await screen.findByRole("status");
      expect(status.textContent).toContain(
        "The provider rejected these credentials. Replace the key or sign in again.",
      );
      expect(document.body.textContent).not.toContain(secret);
    });

    test("does not render a raw RPC error string", async () => {
      const fake = connectFakeClient();
      const secret = "raw provider response containing sk-live-do-not-render";
      fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [] }));
      fake.on("evener/auth/test", async () => {
        throw new Error(secret);
      });
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText(WORK.name);
      await userEvent.setup().click(screen.getByRole("button", { name: "Test credentials" }));

      const status = await screen.findByRole("status");
      expect(status.textContent).toContain(
        "The provider endpoint could not be reached. Check the endpoint and network connection.",
      );
      expect(document.body.textContent).not.toContain(secret);
    });

    test("resets pending state and ignores a late result after same-name instance refresh", async () => {
      const fake = connectFakeClient();
      const oldInstance = instance({ name: "work", providerId: "anthropic", baseUrl: "https://old.example/v1" });
      const refreshedInstance = instance({ name: "work", providerId: "anthropic", baseUrl: "https://new.example/v1" });
      const response = deferred<AuthTestResponse>();
      let listCalls = 0;
      fake.on("evener/instance/list", () => {
        listCalls += 1;
        return listCalls === 1
          ? { instances: [oldInstance], availableProviders: [] }
          : { instances: [refreshedInstance], availableProviders: [] };
      });
      fake.on("evener/auth/test", () => response.promise);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("openai-chat · base https://old.example/v1");
      await userEvent.setup().click(screen.getByRole("button", { name: "Test credentials" }));
      expect(screen.getByRole("button", { name: "Testing credentials…" })).toBeTruthy();

      await act(async () => {
        await credentialsStore.getState().fetch();
      });
      await screen.findByText("openai-chat · base https://new.example/v1");
      const refreshedButton = screen.getByRole("button", { name: /Test(?:ing credentials…)?/ });
      expect((refreshedButton as HTMLButtonElement).disabled).toBe(false);
      response.resolve({ provider: "work", status: "success", message: "Credentials verified." });
      await act(async () => {
        await response.promise;
      });
      expect(screen.queryByRole("status")).toBeNull();
    });
  });

  describe("single-open-editor invariant", () => {
    test("opening the Add form, then Edit on a row, replaces it (only one editor open at a time)", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("work");
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "+ Add provider instance" }));
      expect(screen.getByRole("dialog", { name: "Add provider instance" })).toBeTruthy();
      await user.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
      expect(screen.queryByRole("dialog", { name: "Add provider instance" })).toBeNull();
      expect(screen.getByRole("dialog", { name: "Edit work" })).toBeTruthy();
    });
  });

  describe("OAuth start branches", () => {
    test("fallback:true opens the redirect (paste-back) editor using loginStart's own flowId/url", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/auth/device/start", () => ({
        provider: "personal",
        flowId: "device-flow",
        userCode: "X",
        verificationUrl: "https://verify",
        intervalSeconds: 5,
        fallback: true,
      }));
      fake.on("evener/auth/login/start", () => ({
        provider: "personal",
        flowId: "redirect-flow",
        url: "https://auth/start",
      }));
      const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("personal");
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "Sign in…" }));
      await screen.findByRole("dialog", { name: "Sign in to personal" });
      expect(openSpy).toHaveBeenCalledWith("https://auth/start", "_blank", "noopener");
      expect(screen.getByRole("link", { name: /re-open authorize url/i })).toBeTruthy();
    });

    test("fallback:false/absent opens the device-code editor", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/auth/device/start", () => ({
        provider: "personal",
        flowId: "device-flow",
        userCode: "ABCD-EFGH",
        verificationUrl: "https://verify",
        intervalSeconds: 5,
      }));
      fake.on("evener/auth/device/poll", () => ({ state: "pending" }));
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("personal");
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "Sign in…" }));
      await screen.findByText("ABCD-EFGH");
    });

    test("a deviceStart failure toasts 'Sign-in failed' and opens no editor", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/auth/device/start", () => {
        throw new Error("provider unavailable");
      });
      render(
        <>
          <CredentialsSection sectionId="credentials" />
          <Toast />
        </>,
      );
      await screen.findByText("personal");
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "Sign in…" }));
      // error is converted via friendlyErrorMessage: raw JS errors become the generic message
      await screen.findByText("Sign-in failed: Something went wrong.");
      // Assert the raw string no longer appears
      expect(screen.queryByText(/provider unavailable/)).toBeNull();
      expect(screen.queryByRole("dialog")).toBeNull();
    });

    // Proves the key={flowId} teardown DeviceCodeDialog's own doc comment
    // claims: expiring flow A, then "Start again" (a fresh evener/auth/device/
    // start -> a NEW flowId, same openEditor.kind==="device" throughout) must
    // both (a) reset DeviceCodeDialog's own local UI state (copied/expired/
    // error) rather than leaking flow A's "expired" straight into flow B's
    // first render, and (b) leave flow A's poll timer genuinely dead. Neither
    // holds for free: DeviceCodeDialog's internal poll effect already
    // restarts on a bare flowId prop change (flowId is one of its own deps),
    // which is enough to make (b) true even WITHOUT the key - only (a)
    // actually depends on key forcing a real remount (a mere prop update
    // would keep the same component instance, and therefore its stale local
    // state, across the transition).
    test("abandoning an expired device flow and starting a new one resets to a fresh state, not flow A's leftover 'expired' UI - and flow A's timer stays dead", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      let deviceStartCalls = 0;
      fake.on("evener/auth/device/start", () => {
        deviceStartCalls += 1;
        return deviceStartCalls === 1
          ? {
              provider: "personal",
              flowId: "flow-A",
              userCode: "AAAA-1111",
              verificationUrl: "https://verify",
              intervalSeconds: 1,
            }
          : {
              provider: "personal",
              flowId: "flow-B",
              userCode: "BBBB-2222",
              verificationUrl: "https://verify",
              intervalSeconds: 1,
            };
      });
      const pollCalls: string[] = [];
      fake.on("evener/auth/device/poll", (params) => {
        pollCalls.push(params.flowId);
        return params.flowId === "flow-A" ? { state: "expired" } : { state: "pending" };
      });
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("personal");
      vi.useFakeTimers();
      fireEvent.click(screen.getByRole("button", { name: "Sign in…" }));
      await vi.waitFor(() => expect(screen.getByText("AAAA-1111")).toBeTruthy());

      // Flow A expires.
      await advanceTime(1000);
      expect(screen.getByText(/Code expired/)).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: "Start again" }));

      // Flow B starts fresh: its own code, NOT flow A's leftover expired state.
      await vi.waitFor(() => expect(screen.getByText("BBBB-2222")).toBeTruthy());
      expect(screen.queryByText(/Code expired/)).toBeNull();
      expect(screen.getByRole("button", { name: /copy code/i })).toBeTruthy();

      // Flow B is genuinely polling under its own flowId.
      await advanceTime(1000);
      expect(pollCalls).toContain("flow-B");
      const flowACallsAtSwitch = pollCalls.filter((id) => id === "flow-A").length;

      await advanceTime(2200);
      expect(pollCalls.filter((id) => id === "flow-A").length).toBe(flowACallsAtSwitch);
    });
  });

  describe("set default", () => {
    test("calls instanceSetDefault directly with no confirm dialog and no success toast", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/instance/setDefault", (params) => {
        expect(params).toEqual({ name: "personal" });
        return { instances: [WORK, { ...PERSONAL, isDefault: true }], availableProviders: [] };
      });
      render(
        <>
          <CredentialsSection sectionId="credentials" />
          <Toast />
        </>,
      );
      await screen.findByText("personal");
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: /make default/i }));
      await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/setDefault")).toBe(true));
      expect(screen.queryByRole("alert")).toBeNull();
    });

    test("a setDefault failure toasts 'Set default failed'", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/instance/setDefault", () => {
        throw new Error("boom");
      });
      render(
        <>
          <CredentialsSection sectionId="credentials" />
          <Toast />
        </>,
      );
      await screen.findByText("personal");
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: /make default/i }));
      // error is converted via friendlyErrorMessage: raw JS errors become the generic message
      await screen.findByText("Set default failed: Something went wrong.");
      // Assert the raw string no longer appears
      expect(screen.queryByText(/boom/)).toBeNull();
    });
  });

  describe("Clear / Remove confirm dialogs", () => {
    test("Clear opens a ConfirmDialog naming the instance; confirming calls authLogout then refreshes", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/auth/logout", (params) => {
        expect(params).toEqual({ provider: "work" });
        return {
          removed: true,
          status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
        };
      });
      render(
        <>
          <CredentialsSection sectionId="credentials" />
          <Toast />
        </>,
      );
      await screen.findByText("work");
      const user = userEvent.setup();
      // WORK carries hasStoredFile+activeSource:"store" in the shared fixture,
      // so its row already offers Clear.
      await user.click(screen.getByRole("button", { name: "Clear" }));
      const dialog = screen.getByRole("dialog", { name: /clear/i });
      expect(dialog).toBeTruthy();
      // The row's own Clear button is still present behind the dialog, so
      // scope this second click to the dialog's own Clear/confirm button.
      await user.click(within(dialog).getByRole("button", { name: "Clear" }));
      await screen.findByText("Credentials cleared for work");
    });

    test("Remove opens a ConfirmDialog; confirming calls instanceRemove", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/instance/remove", (params) => {
        expect(params).toEqual({ name: "personal" });
        return { instances: [WORK], availableProviders: [] };
      });
      render(
        <>
          <CredentialsSection sectionId="credentials" />
          <Toast />
        </>,
      );
      await screen.findByText("personal");
      const user = userEvent.setup();
      const removeButtons = screen.getAllByRole("button", { name: "Remove" });
      await user.click(removeButtons[1]!); // personal's row
      const dialog = screen.getByRole("dialog", { name: /remove/i });
      expect(dialog).toBeTruthy();
      // The row's own Remove button is still present behind the dialog, so
      // scope this second click to the dialog's own Remove/confirm button.
      await user.click(within(dialog).getByRole("button", { name: "Remove" }));
      await screen.findByText("Removed instance personal");
    });

    test("cancelling a confirm dialog makes no RPC call", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      const removeCalls: unknown[] = [];
      fake.on("evener/instance/remove", (params) => {
        removeCalls.push(params);
        return { instances: [], availableProviders: [] };
      });
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("personal");
      const user = userEvent.setup();
      const removeButtons = screen.getAllByRole("button", { name: "Remove" });
      await user.click(removeButtons[1]!);
      await user.click(screen.getByRole("button", { name: "Cancel" }));
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(removeCalls).toEqual([]);
    });
  });

  describe("diagnostics and writesRefused", () => {
    test("renders every diagnostics entry from the list response", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => ({
        instances: [],
        availableProviders: [],
        diagnostics: [
          'providers.toml: unknown key "type" (instance writes are refused until the file is fixed)',
          "user layer: none (EVENER_PROVIDERS_CONFIG is empty)",
        ],
      }));
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText(/providers\.toml: unknown key "type"/);
      expect(screen.getByText("user layer: none (EVENER_PROVIDERS_CONFIG is empty)")).toBeTruthy();
    });

    test("no diagnostics banner when the list carries none", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("work");
      expect(screen.queryByText("Warnings")).toBeNull();
    });

    test("writesRefused disables Add and every row's Edit/Remove/Set default, but not Test credentials/Set key/Clear", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => ({
        instances: [WORK, PERSONAL],
        availableProviders: [],
        writesRefused: true,
      }));
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("work");

      expect((screen.getByRole("button", { name: "+ Add provider instance" }) as HTMLButtonElement).disabled).toBe(
        true,
      );
      for (const button of screen.getAllByRole("button", { name: "Edit" })) {
        expect((button as HTMLButtonElement).disabled).toBe(true);
      }
      for (const button of screen.getAllByRole("button", { name: "Remove" })) {
        expect((button as HTMLButtonElement).disabled).toBe(true);
      }
      // Only PERSONAL is non-default, so it's the only row offering "make default".
      expect((screen.getByRole("button", { name: /make default/i }) as HTMLButtonElement).disabled).toBe(true);
      // WORK has a stored file key, so its row offers Clear - unaffected by writesRefused.
      expect((screen.getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(false);
      for (const button of screen.getAllByRole("button", { name: "Test credentials" })) {
        expect((button as HTMLButtonElement).disabled).toBe(false);
      }
    });
  });
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.edge.test.tsx`**

  ```tsx
  // Edge cases for CredentialsSection.tsx uncovered lines:
  // - handleConfirmedAction clear failure error toast (lines 153-154)
  // - handleConfirmedAction remove failure error toast (lines 153-154)
  // - findInstance returns undefined for apiKey dialog when instance is gone (line 223)
  // - findInstance returns undefined for edit dialog when instance is gone (line 224)

  import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
  import userEvent from "@testing-library/user-event";
  import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
  import { FakeClient } from "../../../../protocol/testing/fakeClient";
  import type { InstanceEntry, InstanceListResponse } from "../../../../protocol/types.gen";
  import { connectionStore } from "../../../../stores/connection";
  import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
  import { Toast } from "../../../../widgets";
  import { resetToastStoreForTests } from "../../../../widgets/toast/store";
  import { CredentialsSection } from "./CredentialsSection";

  function connectFakeClient(): FakeClient {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    return fake;
  }

  function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
    return {
      protocol: "openai-chat",
      auth: "bearer",
      implicit: false,
      isDefault: false,
      activeSource: "none",
      hasStoredOAuth: false,
      credentialRequired: true,
      ...overrides,
    };
  }

  const WORK = instance({
    name: "work",
    providerId: "anthropic",
    authModes: ["apiKey"],
    isDefault: true,
    hasStoredFile: true,
    activeSource: "store",
  });
  const PERSONAL = instance({
    name: "personal",
    providerId: "openai-codex",
    auth: "oauth-openai-codex",
    authModes: ["oauth"],
  });
  const LIST: InstanceListResponse = { instances: [WORK, PERSONAL], availableProviders: [] };

  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    resetCredentialsStoreForTests();
    resetToastStoreForTests();
  });

  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // Lines 153-154: handleConfirmedAction clear failure error toast
  describe("CredentialsSection edge cases", () => {
    test("clear failure shows error toast", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/auth/logout", () => {
        throw new Error("logout denied");
      });
      render(
        <>
          <Toast />
          <CredentialsSection sectionId="credentials" />
        </>,
      );
      await screen.findByText("work");
      const user = userEvent.setup();
      // Click Clear on the work instance (has stored file → Clear available)
      const clearButtons = screen.getAllByRole("button", { name: "Clear" });
      await user.click(clearButtons[0]!);
      const dialog = screen.getByRole("dialog");
      await user.click(within(dialog).getByRole("button", { name: "Clear" }));
      await screen.findByText("Clear failed: Something went wrong.");
      expect(screen.getByRole("dialog", { name: "Clear credentials" })).toBeTruthy();
    });

    test("remove failure shows error toast", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      fake.on("evener/instance/remove", () => {
        throw new Error("remove denied");
      });
      render(
        <>
          <Toast />
          <CredentialsSection sectionId="credentials" />
        </>,
      );
      await screen.findByText("personal");
      const user = userEvent.setup();
      const removeButtons = screen.getAllByRole("button", { name: "Remove" });
      await user.click(removeButtons[1]!);
      const dialog = screen.getByRole("dialog");
      await user.click(within(dialog).getByRole("button", { name: "Remove" }));
      await screen.findByText("Remove failed: Something went wrong.");
      expect(screen.getByRole("dialog", { name: "Remove instance" })).toBeTruthy();
    });

    // Lines 194, 223-224: an open API-key editor stops rendering if its instance disappears.
    test("an API key dialog closes when a refreshed list removes its instance", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("work");
      const user = userEvent.setup();
      const setKeyButtons = screen.getAllByRole("button", { name: /replace key/i });
      await user.click(setKeyButtons[0]!);
      await screen.findByRole("dialog", { name: "Set API key for work" });

      act(() => credentialsStore.setState({ instances: [PERSONAL] }));

      await waitFor(() => expect(screen.queryByRole("dialog", { name: "Set API key for work" })).toBeNull());
    });

    // Lines 196, 228-229: an open edit dialog stops rendering if its instance disappears.
    test("an edit dialog closes when a refreshed list removes its instance", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST);
      render(<CredentialsSection sectionId="credentials" />);
      await screen.findByText("work");
      const user = userEvent.setup();
      const editButtons = screen.getAllByRole("button", { name: "Edit" });
      await user.click(editButtons[0]!);
      await screen.findByRole("dialog", { name: "Edit work" });

      act(() => credentialsStore.setState({ instances: [PERSONAL] }));

      await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit work" })).toBeNull());
    });
  });
  ```

  **File: `cmd/evener-hub/frontend/src/stores/credentials.test.ts`**

  ```ts
  import { cleanup, renderHook } from "@testing-library/react";
  import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
  import { FakeClient } from "../protocol/testing/fakeClient";
  import { threadStartedNotification } from "../protocol/testing/notifications";
  import type { AuthTestResponse, InstanceEntry, InstanceListResponse } from "../protocol/types.gen";
  import { connectionStore } from "./connection";
  import { credentialsStore, resetCredentialsStoreForTests, useCredentialsStore } from "./credentials";

  function connectFakeClient(): FakeClient {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    return fake;
  }

  const ONE_INSTANCE: InstanceEntry = {
    name: "work",
    providerId: "openai-codex",
    protocol: "openai-responses",
    auth: "oauth-openai-codex",
    isDefault: true,
    implicit: false,
    authModes: ["oauth"],
    activeSource: "oauth",
    hasStoredFile: false,
    hasStoredOAuth: true,
    envVar: "",
    storedEmail: "me@example.com",
    credentialRequired: true,
  };

  const LIST_RESPONSE: InstanceListResponse = {
    instances: [ONE_INSTANCE],
    availableProviders: [
      { id: "anthropic", protocol: "anthropic", auth: "bearer", implicit: true },
      { id: "openai-codex", protocol: "openai-responses", auth: "oauth-openai-codex", implicit: true },
    ],
  };

  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    resetCredentialsStoreForTests();
  });

  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    cleanup();
  });

  describe("fetch", () => {
    test("throws if no client is connected", async () => {
      await expect(credentialsStore.getState().fetch()).rejects.toThrow(/no client connected/);
    });

    test("populates instances/availableProviders/diagnostics/writesRefused from evener/instance/list on success", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => ({
        ...LIST_RESPONSE,
        diagnostics: ['providers.toml: unexpected key "type"'],
        userLayer: "user layer: /home/x/.config/evener/providers.toml",
        writesRefused: true,
      }));
      await credentialsStore.getState().fetch();
      const state = credentialsStore.getState();
      expect(state.instances).toEqual([ONE_INSTANCE]);
      expect(state.availableProviders).toEqual(LIST_RESPONSE.availableProviders);
      expect(state.diagnostics).toEqual(['providers.toml: unexpected key "type"']);
      expect(state.userLayer).toBe("user layer: /home/x/.config/evener/providers.toml");
      expect(state.writesRefused).toBe(true);
      expect(state.loading).toBe(false);
      expect(state.error).toBeNull();
    });

    test("defaults diagnostics/userLayer/writesRefused when the response omits them", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE); // no diagnostics/userLayer/writesRefused keys
      await credentialsStore.getState().fetch();
      const state = credentialsStore.getState();
      expect(state.diagnostics).toEqual([]);
      expect(state.userLayer).toBe("");
      expect(state.writesRefused).toBe(false);
    });

    test("sets loading true for the duration of the request", async () => {
      const fake = connectFakeClient();
      let resolveRequest: (() => void) | undefined;
      fake.on(
        "evener/instance/list",
        () =>
          new Promise<InstanceListResponse>((resolve) => {
            resolveRequest = () => resolve(LIST_RESPONSE);
          }),
      );
      const promise = credentialsStore.getState().fetch();
      await Promise.resolve();
      expect(credentialsStore.getState().loading).toBe(true);
      resolveRequest?.();
      await promise;
      expect(credentialsStore.getState().loading).toBe(false);
    });

    test("on failure, clears loading and sets error without touching prior instances", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE);
      await credentialsStore.getState().fetch();

      fake.on("evener/instance/list", () => {
        throw new Error("boom");
      });
      await credentialsStore.getState().fetch();
      const state = credentialsStore.getState();
      expect(state.loading).toBe(false);
      expect(state.error).toBe("boom");
      expect(state.instances).toEqual([ONE_INSTANCE]); // unchanged - not blanked
    });
  });

  describe("mutations returning the updated instance list", () => {
    test("create() calls evener/instance/create and applies the returned list", async () => {
      const fake = connectFakeClient();
      const created: InstanceListResponse = { instances: [ONE_INSTANCE], availableProviders: [] };
      fake.on("evener/instance/create", (params) => {
        expect(params).toEqual({ name: "work", base: "openai-codex", baseUrl: "" });
        return created;
      });
      await credentialsStore.getState().create({ name: "work", base: "openai-codex", baseUrl: "" });
      expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    });

    test("edit() calls evener/instance/edit and applies the returned list", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/edit", (params) => {
        expect(params).toEqual({ name: "work", baseUrl: "https://x" });
        return LIST_RESPONSE;
      });
      await credentialsStore.getState().edit({ name: "work", baseUrl: "https://x" });
      expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    });

    test("remove() calls evener/instance/remove and applies the returned list", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/remove", (params) => {
        expect(params).toEqual({ name: "work" });
        return { instances: [], availableProviders: [] };
      });
      await credentialsStore.getState().remove("work");
      expect(credentialsStore.getState().instances).toEqual([]);
    });

    test("setDefault() calls evener/instance/setDefault and applies the returned list", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/setDefault", (params) => {
        expect(params).toEqual({ name: "work" });
        return LIST_RESPONSE;
      });
      await credentialsStore.getState().setDefault("work");
      expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    });

    test("a mutation failure rejects and does not touch stored instances", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/create", () => {
        throw new Error("name already exists");
      });
      await expect(
        credentialsStore.getState().create({ name: "work", base: "openai-codex", baseUrl: "" }),
      ).rejects.toThrow("name already exists");
      expect(credentialsStore.getState().instances).toEqual([]);
    });
  });

  describe("auth RPCs: thin proxies, no local state mutation", () => {
    test("testCredentials() sends the exact configured instance name and returns the typed safe response", async () => {
      const fake = connectFakeClient();
      const response: AuthTestResponse = {
        provider: "custom / team-east",
        status: "success",
        message: "Credentials verified.",
      };
      fake.on("evener/auth/test", (params) => {
        expect(params).toEqual({ provider: "custom / team-east" });
        return response;
      });

      await expect(credentialsStore.getState().testCredentials("custom / team-east")).resolves.toEqual(response);
    });

    test("setApiKey() calls evener/auth/apiKey/set and returns its response", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/apiKey/set", (params) => {
        expect(params).toEqual({ provider: "work", value: "sk-secret" });
        return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
      });
      const result = await credentialsStore.getState().setApiKey("work", "sk-secret");
      expect(result.activeSource).toBe("store");
      // Never stored on the store itself - never-echo invariant.
      expect(JSON.stringify(credentialsStore.getState())).not.toContain("sk-secret");
    });

    test("logout() calls evener/auth/logout", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/logout", (params) => {
        expect(params).toEqual({ provider: "work" });
        return {
          removed: true,
          status: { provider: "work", supported: true, signedIn: false, activeSource: "none", hasStoredOAuth: false },
        };
      });
      const result = await credentialsStore.getState().logout("work");
      expect(result.removed).toBe(true);
    });

    test("loginStart() calls evener/auth/login/start", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/login/start", (params) => {
        expect(params).toEqual({ provider: "work" });
        return { provider: "work", flowId: "flow-1", url: "https://auth.example.com/start" };
      });
      const result = await credentialsStore.getState().loginStart("work");
      expect(result.url).toBe("https://auth.example.com/start");
    });

    test("loginComplete() calls evener/auth/login/complete", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/login/complete", (params) => {
        expect(params).toEqual({ provider: "work", flowId: "flow-1", redirectUrl: "https://redirect" });
        return {
          status: { provider: "work", supported: true, signedIn: true, activeSource: "oauth", hasStoredOAuth: true },
        };
      });
      const result = await credentialsStore.getState().loginComplete("work", "flow-1", "https://redirect");
      expect(result.status.signedIn).toBe(true);
    });

    test("deviceStart() calls evener/auth/device/start", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/device/start", (params) => {
        expect(params).toEqual({ provider: "work" });
        return {
          provider: "work",
          flowId: "flow-2",
          userCode: "ABCD-EFGH",
          verificationUrl: "https://verify",
          intervalSeconds: 5,
        };
      });
      const result = await credentialsStore.getState().deviceStart("work");
      expect(result.userCode).toBe("ABCD-EFGH");
    });

    test("devicePoll() calls evener/auth/device/poll", async () => {
      const fake = connectFakeClient();
      fake.on("evener/auth/device/poll", (params) => {
        expect(params).toEqual({ provider: "work", flowId: "flow-2" });
        return { state: "pending" };
      });
      const result = await credentialsStore.getState().devicePoll("work", "flow-2");
      expect(result.state).toBe("pending");
    });
  });

  describe("useCredentialsStore", () => {
    test("selector overload returns a derived value reactively", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE);
      const { result } = renderHook(() => useCredentialsStore((s) => s.instances.length));
      expect(result.current).toBe(0);
      await credentialsStore.getState().fetch();
      expect(result.current).toBe(1);
    });

    test("no-selector overload returns the whole state", () => {
      const { result } = renderHook(() => useCredentialsStore());
      expect(result.current.instances).toEqual([]);
      expect(typeof result.current.fetch).toBe("function");
    });
  });

  describe("notification-triggered refetch", () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });

    afterEach(() => {
      vi.useRealTimers();
    });

    test("evener/auth/updated schedules a debounced fetch, 250ms", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE);
      await credentialsStore.getState().fetch(); // initial load; also wires notification handling
      const updated: InstanceListResponse = {
        instances: [{ ...ONE_INSTANCE, hasStoredOAuth: false }],
        availableProviders: [],
      };
      fake.on("evener/instance/list", () => updated);

      fake.emitNotification({ method: "evener/auth/updated", params: {} });
      await vi.advanceTimersByTimeAsync(249);
      expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
      await vi.advanceTimersByTimeAsync(1);
      expect(credentialsStore.getState().instances).toEqual(updated.instances);
    });

    test("wiring attaches as soon as a client connects, with no prior fetch call required", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE);
      fake.emitNotification({ method: "evener/auth/updated", params: {} });
      await vi.advanceTimersByTimeAsync(250);
      expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    });

    test("an irrelevant notification does not trigger a refetch", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE);
      await credentialsStore.getState().fetch();
      const listSpy = vi.fn(() => LIST_RESPONSE);
      fake.on("evener/instance/list", listSpy);

      fake.emitNotification(threadStartedNotification());
      await vi.advanceTimersByTimeAsync(1000);
      expect(listSpy).not.toHaveBeenCalled();
    });

    test("a burst of evener/auth/updated coalesces into exactly one refetch", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE);
      await credentialsStore.getState().fetch();
      const listSpy = vi.fn(() => LIST_RESPONSE);
      fake.on("evener/instance/list", listSpy);

      fake.emitNotification({ method: "evener/auth/updated", params: {} });
      await vi.advanceTimersByTimeAsync(100);
      fake.emitNotification({ method: "evener/auth/updated", params: {} });
      await vi.advanceTimersByTimeAsync(100); // 200ms elapsed total, but the second notification reset the window
      expect(listSpy).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(150); // 250ms since the last notification
      expect(listSpy).toHaveBeenCalledTimes(1);
    });

    test("a background refetch race with no client connected is swallowed, not an unhandled rejection", async () => {
      const fake = connectFakeClient();
      fake.on("evener/instance/list", () => LIST_RESPONSE);
      await credentialsStore.getState().fetch();

      fake.emitNotification({ method: "evener/auth/updated", params: {} });
      // Disconnect before the debounce fires - fetch()'s own requireClient()
      // throws outside its try/catch by design (this store's own doc comment),
      // so the scheduled background call must swallow that rejection itself
      // rather than surfacing an unhandled promise rejection that would fail
      // this test even though nothing here ever awaits it directly.
      connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
      await vi.advanceTimersByTimeAsync(250);
      // Reaching this line (rather than the test failing on an unhandled
      // rejection) is the real assertion; this just also confirms the failed
      // background attempt left the prior successful load untouched.
      expect(credentialsStore.getState().instances).toEqual([ONE_INSTANCE]);
    });
  });
  ```

  **Incidental fixture fixes** (these two files keep every other test unchanged; only the `InstanceListResponse` literal's shape needs to agree with the new wire — write these now too, since Step 2's narrower command below does not include them and would otherwise hide their breakage until Step 4's full `make test-web`):

  In `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/oauthDialogs.test.tsx`, at both of the two occurrences (currently lines 76 and 185):
  ```ts
  // before (both occurrences)
  fake.on("evener/instance/list", () => ({ instances: [], availableTypes: [] }));
  // after
  fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  ```

  In `cmd/evener-hub/frontend/src/panes/settings/Settings.test.tsx`:
  ```ts
  // before
  function connectFakeClientWithNoInstances(): void {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({ instances: [], availableTypes: ["anthropic"] }));
    connectionStore.getState().connect(fake);
  }
  // after
  function connectFakeClientWithNoInstances(): void {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/list", () => ({
      instances: [],
      availableProviders: [{ id: "anthropic", protocol: "anthropic", auth: "bearer", implicit: true }],
    }));
    connectionStore.getState().connect(fake);
  }
  ```

- [ ] **Step 2: Run them to verify they fail**

  ```bash
  cd cmd/evener-hub/frontend && npm test -- src/panes/settings/sections/credentials src/stores/credentials.test.ts
  ```

  Every new/rewritten test fails at this point: `credentialLabels.ts`/`InstanceRow.tsx`/`instanceDialogs.tsx`/`CredentialsSection.tsx`/`stores/credentials.ts` still export the old names (`groupByType`, no `activeSourceLabel`, `AddInstanceDialogProps.availableTypes`, `CredentialsStoreState.availableTypes`, …), so the test files fail to resolve those imports or assert against behavior the old code doesn't implement (module-not-providing-export errors and assertion failures are both an acceptable red state — either way nothing here passes yet).

- [ ] **Step 3: Implement** — real TSX/TS code for each changed file.

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels.ts`** (full replacement)

  ```ts
  // credentialLabels.ts is the pure-logic half of the Credentials section
  // (parity-m7-settings.md §7c, updated for the provider registry's instance
  // wire shape - spec docs/superpowers/specs/2026-08-28-provider-registry-
  // design.md §11.3): computing the credential display from InstanceEntry's
  // activeSource/credentialRequired/auth fields and the providerId grouping -
  // no rendering, no store access, easily unit-tested in isolation.
  import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";

  export interface CredentialLayerView {
    source: string;
    label: string;
    effective: boolean;
  }

  // activeSourceLabel is the single source of truth for every ActiveSource
  // value the registry sends (spec §11.3's vocabulary: api_key |
  // credential_headers | store | env:<VAR> | oauth | adc | none). "env:<VAR>"
  // carries its variable name in the string itself, so it is matched by
  // prefix, not an exact value. "none" - nothing currently resolves - splits
  // three ways on credentialRequired and the instance's own auth scheme,
  // since a scheme that never wants a credential (auth: none) reads
  // differently from one that merely allows an optional one (optional-
  // bearer) or one that plainly lacks a required key.
  export function activeSourceLabel(instance: InstanceEntry): string {
    const source = instance.activeSource;
    if (source.startsWith("env:")) return `Configured via environment variable (${source.slice(4)})`;
    switch (source) {
      case "api_key":
        return "Configured via providers.toml";
      case "credential_headers":
        return "Configured via a credential header";
      case "store":
        return "Configured via stored API key";
      case "oauth":
        return instance.storedEmail ? `Configured via OAuth (${instance.storedEmail})` : "Configured via OAuth";
      case "adc":
        return "Configured via Application Default Credentials";
      case "none":
        if (instance.credentialRequired) return "Not configured";
        return instance.auth === "none" ? "No credentials required" : "No key set · optional";
      default:
        return source;
    }
  }

  // credentialLayers lists the credential line(s) InstanceRow shows: the
  // effective source first, plus - the one case that can still shadow another
  // under the registry's resolution order (spec §10: api_key >
  // credential_headers > store > env, and oauth-openai-codex/gcp-adc never
  // layer against any of those on the same instance) - an environment
  // variable left set behind a stored key that now wins. Empty when nothing
  // has ever resolved (activeSource "none"); see activeSourceLabel for that
  // case's own message.
  export function credentialLayers(instance: InstanceEntry): CredentialLayerView[] {
    if (instance.activeSource === "none") return [];
    const layers: CredentialLayerView[] = [
      { source: instance.activeSource, label: activeSourceLabel(instance), effective: true },
    ];
    if (instance.activeSource === "store" && instance.envVar) {
      layers.push({
        source: `env:${instance.envVar}`,
        label: `Configured via environment variable (${instance.envVar})`,
        effective: false,
      });
    }
    return layers;
  }

  // keylessByDesign: the instance holds no credential and none is wanted -
  // the hub's credentialRequired gate (InstanceEntry, appwire/types.go) says
  // there is nothing to look for, as with an auth-none provider or a gateway
  // on the optional-bearer scheme. Both halves of the row's display key on
  // this one bit: the words activeSourceLabel returns and the heading's
  // status dot, which otherwise disagreed about the same instance.
  export function keylessByDesign(instance: InstanceEntry): boolean {
    return instance.activeSource === "none" && !instance.credentialRequired;
  }

  // unconfiguredLabel: the single-line message shown INSTEAD of the layered
  // display when credentialLayers(instance) is empty - just activeSourceLabel
  // for the "none" case, which already covers required vs. optional vs.
  // never-wanted.
  export function unconfiguredLabel(instance: InstanceEntry): string | null {
    return instance.activeSource === "none" ? activeSourceLabel(instance) : null;
  }

  export interface InstanceProviderGroup {
    providerId: string;
    instances: InstanceEntry[];
  }

  // groupByProvider groups instances by their registry providerId, in
  // first-seen order from the RPC response - never re-sorted client-side
  // (parity-m7-settings.md §7b). providerId, not `base`, is the grouping
  // key: `base` is blank whenever an instance's own name already is the
  // registry id (InstanceEntry, appwire/types.go), so a custom-named
  // instance built on a curated provider (base: "groq") lands in the SAME
  // group as that provider's own implicit instance, not a group of its own.
  export function groupByProvider(instances: InstanceEntry[]): InstanceProviderGroup[] {
    const groups: InstanceProviderGroup[] = [];
    const byProvider = new Map<string, InstanceProviderGroup>();
    for (const instance of instances) {
      let group = byProvider.get(instance.providerId);
      if (!group) {
        group = { providerId: instance.providerId, instances: [] };
        byProvider.set(instance.providerId, group);
        groups.push(group);
      }
      group.instances.push(instance);
    }
    return groups;
  }

  const CREDENTIAL_TEST_MESSAGES: Record<string, string> = {
    success: "Credentials verified.",
    missing: "No credentials are configured for this instance. Add a key or sign in first.",
    auth_rejected: "The provider rejected these credentials. Replace the key or sign in again.",
    endpoint_failure: "The provider endpoint could not be reached. Check the endpoint and network connection.",
    configuration_failure: "Provider configuration could not be loaded. Check the instance settings.",
    unsupported: "This provider does not support harmless credential verification.",
  };
  const ENDPOINT_FAILURE_MESSAGE =
    "The provider endpoint could not be reached. Check the endpoint and network connection.";

  export function safeCredentialTestResult(provider: string, response: AuthTestResponse): AuthTestResponse {
    const message = CREDENTIAL_TEST_MESSAGES[response.status];
    if (message) return { provider, status: response.status, message };
    return { provider, status: "endpoint_failure", message: ENDPOINT_FAILURE_MESSAGE };
  }

  export function safeCredentialTestMessage(status: string): string {
    return CREDENTIAL_TEST_MESSAGES[status] ?? ENDPOINT_FAILURE_MESSAGE;
  }
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceRow.tsx`** (full replacement)

  ```tsx
  // InstanceRow.tsx: one provider instance's display + row actions
  // (parity-m7-settings.md §7c) - pure presentational component, no store
  // access of its own. CredentialsSection supplies every action as a plain
  // callback and owns what happens next (opening an editor, a confirm
  // dialog, or calling the store directly).
  import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";
  import { Button, Chip, StatusDot } from "../../../../widgets";
  import { requireClass } from "../../../../widgets/internal/requireClass";
  import {
    credentialLayers,
    keylessByDesign,
    safeCredentialTestMessage,
    safeCredentialTestResult,
    unconfiguredLabel,
  } from "./credentialLabels";
  import styles from "./InstanceRow.module.css";

  const CLASS = {
    row: requireClass(styles.row, "InstanceRow.module.css", "row"),
    heading: requireClass(styles.heading, "InstanceRow.module.css", "heading"),
    name: requireClass(styles.name, "InstanceRow.module.css", "name"),
    styleInfo: requireClass(styles.styleInfo, "InstanceRow.module.css", "styleInfo"),
    layers: requireClass(styles.layers, "InstanceRow.module.css", "layers"),
    layer: requireClass(styles.layer, "InstanceRow.module.css", "layer"),
    unconfigured: requireClass(styles.unconfigured, "InstanceRow.module.css", "unconfigured"),
    actions: requireClass(styles.actions, "InstanceRow.module.css", "actions"),
    testResult: requireClass(styles.testResult, "InstanceRow.module.css", "testResult"),
  };

  // styleInfoText: protocol is always present (InstanceEntry.protocol has no
  // omitempty), so this always has something to show - unlike the retired
  // apiStyle, which was blank for every non-openai instance.
  function styleInfoText(instance: InstanceEntry): string {
    return instance.baseUrl ? `${instance.protocol} · base ${instance.baseUrl}` : instance.protocol;
  }

  export interface InstanceRowProps {
    instance: InstanceEntry;
    onSetApiKey: () => void;
    onOAuthStart: () => void;
    onEdit: () => void;
    onClear: () => void;
    onRemove: () => void;
    onSetDefault: () => void;
    onTestCredentials: () => void;
    testCredentialsPending?: boolean;
    testCredentialsResult?: AuthTestResponse;
    /** Disables Edit/Remove/Set default while providers.toml fails to load
     * (InstanceListResponse.writesRefused, spec §11.3) - Set key/Sign in/
     * Clear/Test credentials are unaffected: they write the credentials
     * store or an OAuth record, never providers.toml. */
    writesRefused?: boolean;
  }

  export function InstanceRow({
    instance,
    onSetApiKey,
    onOAuthStart,
    onEdit,
    onClear,
    onRemove,
    onSetDefault,
    onTestCredentials,
    testCredentialsPending = false,
    testCredentialsResult,
    writesRefused = false,
  }: InstanceRowProps) {
    const supportsApiKey = (instance.authModes ?? []).includes("apiKey");
    const supportsOAuth = (instance.authModes ?? []).includes("oauth");
    const showClear = instance.activeSource === "store" || instance.activeSource === "oauth";
    const layers = credentialLayers(instance);
    const unconfigured = unconfiguredLabel(instance);
    const safeTestResult = testCredentialsResult
      ? safeCredentialTestResult(instance.name, testCredentialsResult)
      : undefined;

    return (
      <li className={CLASS.row}>
        <div className={CLASS.heading}>
          <StatusDot state={layers.length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
          <span className={CLASS.name}>{instance.name}</span>
          {instance.isDefault && <Chip>★ default</Chip>}
          {instance.implicit && <Chip>from environment</Chip>}
          <span className={CLASS.styleInfo}>{styleInfoText(instance)}</span>
        </div>
        {unconfigured ? (
          <p className={CLASS.unconfigured}>{unconfigured}</p>
        ) : (
          <div className={CLASS.layers}>
            {layers.map((layer) => (
              <div key={layer.source} className={CLASS.layer}>
                <span>↳ {layer.label}</span>
                <Chip tone={layer.effective ? "alive" : "neutral"}>{layer.effective ? "effective" : "shadowed"}</Chip>
              </div>
            ))}
          </div>
        )}
        <div className={CLASS.actions}>
          <Button variant="quiet" size="sm" onClick={onTestCredentials} disabled={testCredentialsPending}>
            {testCredentialsPending ? "Testing credentials…" : "Test credentials"}
          </Button>
          {supportsApiKey && (
            <Button variant="quiet" size="sm" onClick={onSetApiKey}>
              {instance.hasStoredFile ? "Replace key" : "Set key"}
            </Button>
          )}
          {supportsOAuth && (
            <Button variant="quiet" size="sm" onClick={onOAuthStart}>
              {instance.hasStoredOAuth ? "Refresh OAuth" : "Sign in…"}
            </Button>
          )}
          {showClear && (
            <Button variant="danger" size="sm" onClick={onClear}>
              Clear
            </Button>
          )}
          <Button variant="quiet" size="sm" onClick={onEdit} disabled={writesRefused}>
            Edit
          </Button>
          {!instance.implicit && (
            <Button variant="danger" size="sm" onClick={onRemove} disabled={writesRefused}>
              Remove
            </Button>
          )}
          {!instance.isDefault && (
            <Button variant="quiet" size="sm" onClick={onSetDefault} disabled={writesRefused}>
              ★ make default
            </Button>
          )}
        </div>
        {safeTestResult && (
          <p className={CLASS.testResult} role="status">
            {safeTestResult.status}: {safeCredentialTestMessage(safeTestResult.status)}
          </p>
        )}
      </li>
    );
  }
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.tsx`** (full replacement)

  ```tsx
  // instanceDialogs.tsx: the 3 instance-CRUD editors (parity-m7-settings.md
  // §7d-§7f) - Add, Edit, and Set/Replace API key. Each owns its own client
  // validation, store call, inline error, and toast; the parent
  // (CredentialsSection) only needs to close the single open editor via
  // `onSuccess`/`onCancel` - it never has to distinguish success from failure
  // itself.
  //
  // Updated for the provider registry's instance shape (spec §11.3): Type
  // becomes Base provider over availableProviders, the openai-only API-style
  // radio is gone (Protocol is no longer openai-specific data the form
  // special-cases), and the Add form gains a dynamic Input per the selected
  // provider's VarsEnv name plus api-key-env/credential-header fields
  // mirroring the CLI's --api-key-env/--credential-header flags (§11.2).
  import { type FormEvent, useState } from "react";
  import { errorText } from "../../../../protocol/errors";
  import type { InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
  import { credentialsStore } from "../../../../stores/credentials";
  import { Button, Dialog, FormRow, Input, Select, type SelectOption, useToasts } from "../../../../widgets";
  import { requireClass } from "../../../../widgets/internal/requireClass";
  import styles from "./instanceDialogs.module.css";

  const CLASS = {
    body: requireClass(styles.body, "instanceDialogs.module.css", "body"),
    actions: requireClass(styles.actions, "instanceDialogs.module.css", "actions"),
    error: requireClass(styles.error, "instanceDialogs.module.css", "error"),
  };

  // nonEmptyVars trims and drops blank entries before they reach the wire -
  // InstanceCreateParams.Vars only carries variables the user actually set
  // (spec §11.3); a blank templated field means "leave it to the
  // environment," not "set it to the empty string."
  function nonEmptyVars(vars: Record<string, string>): Record<string, string> | undefined {
    const entries = Object.entries(vars)
      .map(([key, value]) => [key, value.trim()] as const)
      .filter(([, value]) => value !== "");
    return entries.length > 0 ? Object.fromEntries(entries) : undefined;
  }

  export interface AddInstanceDialogProps {
    availableProviders: ProviderDescriptor[];
    onCancel: () => void;
    onSuccess: () => void;
  }

  /** The global "+ Add provider instance" form (parity-m7-settings.md §7f). */
  export function AddInstanceDialog({ availableProviders, onCancel, onSuccess }: AddInstanceDialogProps) {
    const [base, setBase] = useState("");
    const [name, setName] = useState("");
    const [baseUrl, setBaseUrl] = useState("");
    const [vars, setVars] = useState<Record<string, string>>({});
    const [apiKeyEnv, setApiKeyEnv] = useState("");
    const [credentialHeader, setCredentialHeader] = useState("");
    const [error, setError] = useState<string | null>(null);
    const [busy, setBusy] = useState(false);
    const toast = useToasts();

    const baseOptions: SelectOption[] = [
      { value: "", label: "" },
      ...availableProviders.map((p) => ({ value: p.id, label: p.name || p.id })),
    ];
    const varsEnv = availableProviders.find((p) => p.id === base)?.varsEnv ?? [];

    function handleBaseChange(nextBase: string): void {
      setBase(nextBase);
      setVars({}); // a var input from the previous base must not leak into the new one
    }

    function updateVar(varName: string, value: string): void {
      setVars((current) => ({ ...current, [varName]: value }));
    }

    async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
      event.preventDefault();
      if (!base) {
        setError("Base provider is required.");
        return;
      }
      const trimmedName = name.trim();
      if (!trimmedName) {
        setError("Name is required.");
        return;
      }
      const trimmedCredentialHeader = credentialHeader.trim();
      if (trimmedCredentialHeader && !trimmedCredentialHeader.includes("$")) {
        setError("Credential header must reference a $VARIABLE, never a literal secret.");
        return;
      }
      setError(null);
      setBusy(true);
      try {
        await credentialsStore.getState().create({
          name: trimmedName,
          base,
          baseUrl: baseUrl.trim(),
          vars: nonEmptyVars(vars),
          apiKeyEnv: apiKeyEnv.trim() || undefined,
          credentialHeader: trimmedCredentialHeader || undefined,
        });
        toast.push("success", `Created instance ${trimmedName}`);
        onSuccess();
      } catch (err) {
        const message = errorText(err);
        setError(message);
        toast.push("error", `Create failed: ${message}`);
      } finally {
        setBusy(false);
      }
    }

    return (
      <Dialog open onClose={onCancel} title="Add provider instance">
        <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
          <FormRow label="Base provider" htmlFor="add-instance-base">
            <Select
              id="add-instance-base"
              value={base}
              onChange={(event) => handleBaseChange(event.target.value)}
              options={baseOptions}
            />
          </FormRow>
          <FormRow label="Name" htmlFor="add-instance-name">
            <Input
              id="add-instance-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="e.g. work"
              disabled={busy}
            />
          </FormRow>
          <FormRow label="Base URL (optional)" htmlFor="add-instance-baseurl">
            <Input
              id="add-instance-baseurl"
              value={baseUrl}
              onChange={(event) => setBaseUrl(event.target.value)}
              placeholder="https://…"
              disabled={busy}
            />
          </FormRow>
          {varsEnv.map((varName) => (
            <FormRow key={varName} label={varName} htmlFor={`add-instance-var-${varName}`}>
              <Input
                id={`add-instance-var-${varName}`}
                value={vars[varName] ?? ""}
                onChange={(event) => updateVar(varName, event.target.value)}
                disabled={busy}
              />
            </FormRow>
          ))}
          <FormRow label="API key environment variable (optional)" htmlFor="add-instance-apikeyenv">
            <Input
              id="add-instance-apikeyenv"
              value={apiKeyEnv}
              onChange={(event) => setApiKeyEnv(event.target.value)}
              placeholder="e.g. PORTKEY_KEY"
              disabled={busy}
            />
          </FormRow>
          <FormRow label="Credential header (optional)" htmlFor="add-instance-credentialheader">
            <Input
              id="add-instance-credentialheader"
              value={credentialHeader}
              onChange={(event) => setCredentialHeader(event.target.value)}
              placeholder="Authorization=Bearer $VAR"
              disabled={busy}
            />
          </FormRow>
          {error && (
            <p className={CLASS.error} role="alert">
              {error}
            </p>
          )}
          <div className={CLASS.actions}>
            <Button type="submit" disabled={busy}>
              Create
            </Button>
            <Button type="button" variant="quiet" onClick={onCancel} disabled={busy}>
              Cancel
            </Button>
          </div>
        </form>
      </Dialog>
    );
  }

  export interface EditInstanceDialogProps {
    instance: InstanceEntry;
    onCancel: () => void;
    onSuccess: () => void;
  }

  /** The per-row Edit form (parity-m7-settings.md §7e, updated for the
   * registry's instance shape): Base URL only, sent only when it actually
   * changed. InstanceEditParams also carries protocol/surface/vars
   * overrides, but the pane's only spec-mandated way to set those is the Add
   * form's provider-driven fields (spec §11.3 only calls out VarsEnv driving
   * the add form) - Edit's job is nudging an existing instance's endpoint,
   * not re-deriving its whole shape, and editing an implicit instance
   * already writes a shadow that carries only what changed here. */
  export function EditInstanceDialog({ instance, onCancel, onSuccess }: EditInstanceDialogProps) {
    const [baseUrl, setBaseUrl] = useState(instance.baseUrl || "");
    const [error, setError] = useState<string | null>(null);
    const [busy, setBusy] = useState(false);
    const toast = useToasts();

    async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
      event.preventDefault();
      setError(null);
      setBusy(true);
      try {
        const trimmedBaseUrl = baseUrl.trim();
        await credentialsStore.getState().edit({
          name: instance.name,
          baseUrl: trimmedBaseUrl !== (instance.baseUrl || "") ? trimmedBaseUrl : undefined,
        });
        toast.push("success", `Saved ${instance.name}`);
        onSuccess();
      } catch (err) {
        const message = errorText(err);
        setError(message);
        toast.push("error", `Edit failed: ${message}`);
      } finally {
        setBusy(false);
      }
    }

    return (
      <Dialog open onClose={onCancel} title={`Edit ${instance.name}`}>
        <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
          <FormRow label="Base URL (optional)" htmlFor="edit-instance-baseurl">
            <Input
              id="edit-instance-baseurl"
              value={baseUrl}
              onChange={(event) => setBaseUrl(event.target.value)}
              placeholder="https://…"
              disabled={busy}
            />
          </FormRow>
          {error && (
            <p className={CLASS.error} role="alert">
              {error}
            </p>
          )}
          <div className={CLASS.actions}>
            <Button type="submit" disabled={busy}>
              Save
            </Button>
            <Button type="button" variant="quiet" onClick={onCancel} disabled={busy}>
              Cancel
            </Button>
          </div>
        </form>
      </Dialog>
    );
  }

  export interface ApiKeyDialogProps {
    instance: InstanceEntry;
    onCancel: () => void;
    onSuccess: () => void;
  }

  /** Set/Replace API key (parity-m7-settings.md §7d) - never echoes any
   * stored value; the field is write-only. Unaffected by the registry
   * cut-over: it only ever reads instance.name. */
  export function ApiKeyDialog({ instance, onCancel, onSuccess }: ApiKeyDialogProps) {
    const [value, setValue] = useState("");
    const [error, setError] = useState<string | null>(null);
    const [busy, setBusy] = useState(false);
    const toast = useToasts();

    async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
      event.preventDefault();
      const trimmed = value.trim();
      if (!trimmed) {
        onCancel(); // empty submit silently cancels, no RPC
        return;
      }
      setError(null);
      setBusy(true);
      try {
        await credentialsStore.getState().setApiKey(instance.name, trimmed);
        await credentialsStore.getState().fetch();
        toast.push("success", `API key saved for ${instance.name}`);
        onSuccess();
      } catch (err) {
        const message = errorText(err);
        setError(message);
        toast.push("error", `Save failed: ${message}`);
      } finally {
        setBusy(false);
      }
    }

    return (
      <Dialog open onClose={onCancel} title={`Set API key for ${instance.name}`}>
        <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
          <FormRow label={`API key for ${instance.name}`} htmlFor="api-key-value">
            <Input
              id="api-key-value"
              type="password"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder="paste key"
              disabled={busy}
            />
          </FormRow>
          {error && (
            <p className={CLASS.error} role="alert">
              {error}
            </p>
          )}
          <div className={CLASS.actions}>
            <Button type="submit" disabled={busy}>
              Save
            </Button>
            <Button type="button" variant="quiet" onClick={onCancel} disabled={busy}>
              Cancel
            </Button>
          </div>
        </form>
      </Dialog>
    );
  }
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx`** (full replacement)

  ```tsx
  // CredentialsSection (#7 - the dominant piece of the Agents & models
  // cluster): instance list/CRUD, API-key set, OAuth browser+device dual
  // flow, default-instance switch, remove - parity-m7-settings.md §7. Updated
  // for the provider registry's instance wire shape (spec §11.3): instances
  // group by providerId, the add form is fed availableProviders, and a
  // providers.toml load error surfaces as a diagnostics banner that disables
  // every instance-CRUD action until it clears (writesRefused) - Set key/
  // Sign in/Clear/Test credentials are unaffected, since none of them write
  // providers.toml.
  //
  // Single-mutable-editor invariant: `openEditor` is ONE section-level state
  // value (a discriminated union), so opening a second editor always replaces
  // whatever was open, matching the legacy's own single module-level
  // `openEditor` variable - no per-row state, no dirty-check on replace.
  import { useCallback, useEffect, useRef, useState } from "react";
  import { friendlyErrorMessage } from "../../../../protocol/errors";
  import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";
  import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
  import { Button, ConfirmDialog, EmptyState, Skeleton, useToasts } from "../../../../widgets";
  import { requireClass } from "../../../../widgets/internal/requireClass";
  import { useConnectedEffect } from "../useConnectedEffect";
  import styles from "./CredentialsSection.module.css";
  import { groupByProvider, safeCredentialTestResult } from "./credentialLabels";
  import { InstanceRow } from "./InstanceRow";
  import { AddInstanceDialog, ApiKeyDialog, EditInstanceDialog } from "./instanceDialogs";
  import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";

  const CLASS = {
    root: requireClass(styles.root, "CredentialsSection.module.css", "root"),
    headerRow: requireClass(styles.headerRow, "CredentialsSection.module.css", "headerRow"),
    error: requireClass(styles.error, "CredentialsSection.module.css", "error"),
    groups: requireClass(styles.groups, "CredentialsSection.module.css", "groups"),
    group: requireClass(styles.group, "CredentialsSection.module.css", "group"),
    groupHeader: requireClass(styles.groupHeader, "CredentialsSection.module.css", "groupHeader"),
    list: requireClass(styles.list, "CredentialsSection.module.css", "list"),
    diagnostics: requireClass(styles.diagnostics, "CredentialsSection.module.css", "diagnostics"),
    diagnosticsHeading: requireClass(styles.diagnosticsHeading, "CredentialsSection.module.css", "diagnosticsHeading"),
    diagnosticsList: requireClass(styles.diagnosticsList, "CredentialsSection.module.css", "diagnosticsList"),
  };

  type OpenEditor =
    | { kind: "add" }
    | { kind: "apiKey"; name: string }
    | { kind: "edit"; name: string }
    | { kind: "oauth-redirect"; name: string; flowId: string; authUrl: string }
    | { kind: "device"; name: string; flowId: string; userCode: string; verificationUrl: string; intervalSeconds: number }
    | null;

  type PendingConfirm = { kind: "clear" | "remove"; name: string } | null;
  type CredentialTestState = { version: number; pending: boolean; result?: AuthTestResponse };

  // Diagnostics: the providers.toml load-error pointer, the user-layer note,
  // stray OAuth record notices, and registry warnings (InstanceListResponse.
  // diagnostics, spec §11.3) - mirrors launchServer.tsx's own Diagnostics
  // component (this pane's sibling settings section), a flat unordered list
  // with no stable per-entry identity of its own.
  function Diagnostics({ diagnostics }: { diagnostics: string[] }) {
    if (diagnostics.length === 0) return null;
    return (
      <div className={CLASS.diagnostics} role="status" aria-live="polite">
        <p className={CLASS.diagnosticsHeading}>Warnings</p>
        <ul className={CLASS.diagnosticsList}>
          {diagnostics.map((d, index) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: diagnostics are a flat, unordered warning list with no stable identity of their own
            <li key={index}>{d}</li>
          ))}
        </ul>
      </div>
    );
  }

  export interface CredentialsSectionProps {
    /** Unused - kept so this component's signature matches every other
     * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
    sectionId: string;
  }

  export function CredentialsSection(_props: CredentialsSectionProps) {
    const { instances, availableProviders, diagnostics, writesRefused, loading, error, fetch } = useCredentialsStore();
    const [openEditor, setOpenEditor] = useState<OpenEditor>(null);
    const [pendingConfirm, setPendingConfirm] = useState<PendingConfirm>(null);
    const [confirmBusy, setConfirmBusy] = useState(false);
    const [credentialTests, setCredentialTests] = useState<Record<string, CredentialTestState>>({});
    const previousInstances = useRef(instances);
    const instanceVersion = useRef(0);
    if (previousInstances.current !== instances) {
      previousInstances.current = instances;
      instanceVersion.current += 1;
    }
    const toast = useToasts();

    // biome-ignore lint/correctness/useExhaustiveDependencies: instances is a deliberate trigger-only dependency; each refreshed list invalidates results from the prior provider configuration
    useEffect(() => {
      setCredentialTests({});
    }, [instances]);

    // useConnectedEffect (not a bare useEffect): a direct deep link to
    // /credentials can mount this section before AppShell's own connect()
    // handshake finishes, and credentialsStore.fetch() requires a connected
    // client (throws otherwise) - see that hook's own doc comment.
    useConnectedEffect(fetch, [fetch]);

    // handleOAuthStart is shared by a row's own "Sign in…"/"Refresh OAuth"
    // button and the device editor's "Start again" - always begins with
    // authDeviceStart, then branches on `fallback` exactly like the legacy's
    // startDeviceLogin (templates/partials/credentials.html:58-75).
    async function handleOAuthStart(name: string): Promise<void> {
      try {
        const resp = await credentialsStore.getState().deviceStart(name);
        if (resp.fallback) {
          const login = await credentialsStore.getState().loginStart(name);
          window.open(login.url, "_blank", "noopener");
          setOpenEditor({ kind: "oauth-redirect", name, flowId: login.flowId, authUrl: login.url });
        } else {
          setOpenEditor({
            kind: "device",
            name,
            flowId: resp.flowId,
            userCode: resp.userCode,
            verificationUrl: resp.verificationUrl,
            intervalSeconds: resp.intervalSeconds,
          });
        }
      } catch (err) {
        toast.push("error", `Sign-in failed: ${friendlyErrorMessage(err)}`);
      }
    }

    // "★ make default" has no confirm and no success toast (silent success -
    // only a failure toast exists), matching the legacy exactly.
    async function handleSetDefault(name: string): Promise<void> {
      try {
        await credentialsStore.getState().setDefault(name);
      } catch (err) {
        toast.push("error", `Set default failed: ${friendlyErrorMessage(err)}`);
      }
    }

    async function handleTestCredentials(name: string): Promise<void> {
      const version = instanceVersion.current;
      if (credentialTests[name]?.version === version && credentialTests[name]?.pending) return;
      setCredentialTests((current) => ({ ...current, [name]: { version, pending: true } }));
      try {
        const response = await credentialsStore.getState().testCredentials(name);
        setCredentialTests((current) => ({
          ...current,
          ...(current[name]?.version === version && current[name]?.pending
            ? { [name]: { version, pending: false, result: safeCredentialTestResult(name, response) } }
            : {}),
        }));
      } catch {
        setCredentialTests((current) => ({
          ...current,
          ...(current[name]?.version === version && current[name]?.pending
            ? {
                [name]: {
                  version,
                  pending: false,
                  result: safeCredentialTestResult(name, { provider: name, status: "endpoint_failure", message: "" }),
                },
              }
            : {}),
        }));
      }
    }

    async function handleConfirmedAction(): Promise<void> {
      if (!pendingConfirm) return;
      const { kind, name } = pendingConfirm;
      setConfirmBusy(true);
      try {
        if (kind === "clear") {
          await credentialsStore.getState().logout(name);
          await credentialsStore.getState().fetch();
          toast.push("success", `Credentials cleared for ${name}`);
        } else {
          await credentialsStore.getState().remove(name);
          toast.push("success", `Removed instance ${name}`);
        }
        setPendingConfirm(null);
      } catch (err) {
        const verb = kind === "clear" ? "Clear" : "Remove";
        toast.push("error", `${verb} failed: ${friendlyErrorMessage(err)}`);
      } finally {
        setConfirmBusy(false);
      }
    }

    function findInstance(name: string): InstanceEntry | undefined {
      return instances.find((i) => i.name === name);
    }

    const groups = groupByProvider(instances);
    // useCallback'd (not a plain inline arrow) so its identity stays stable
    // across CredentialsSection re-renders - DeviceCodeDialog's own poll
    // effect depends on the onSuccess it's given, and an unstable reference
    // here would restart that dialog's poll timer on every unrelated parent
    // re-render (see oauthDialogs.tsx's own comment on that effect).
    const closeEditor = useCallback(() => setOpenEditor(null), []);

    return (
      <div className={CLASS.root}>
        <div className={CLASS.headerRow}>
          <Button onClick={() => setOpenEditor({ kind: "add" })} disabled={writesRefused}>
            + Add provider instance
          </Button>
        </div>

        <Diagnostics diagnostics={diagnostics} />

        {loading && <Skeleton />}
        {error && <p className={CLASS.error}>Failed to load: {friendlyErrorMessage(error)}</p>}
        {!loading &&
          !error &&
          (instances.length === 0 ? (
            <EmptyState title="No provider instances configured." />
          ) : (
            <div className={CLASS.groups}>
              {groups.map((group) => (
                <div key={group.providerId} className={CLASS.group}>
                  <div className={CLASS.groupHeader}>{group.providerId}</div>
                  <ul className={CLASS.list}>
                    {group.instances.map((instance) => (
                      <InstanceRow
                        key={instance.name}
                        instance={instance}
                        writesRefused={writesRefused}
                        onSetApiKey={() => setOpenEditor({ kind: "apiKey", name: instance.name })}
                        onOAuthStart={() => void handleOAuthStart(instance.name)}
                        onEdit={() => setOpenEditor({ kind: "edit", name: instance.name })}
                        onClear={() => setPendingConfirm({ kind: "clear", name: instance.name })}
                        onRemove={() => setPendingConfirm({ kind: "remove", name: instance.name })}
                        onSetDefault={() => void handleSetDefault(instance.name)}
                        onTestCredentials={() => void handleTestCredentials(instance.name)}
                        testCredentialsPending={
                          credentialTests[instance.name]?.version === instanceVersion.current &&
                          (credentialTests[instance.name]?.pending ?? false)
                        }
                        testCredentialsResult={
                          credentialTests[instance.name]?.version === instanceVersion.current
                            ? credentialTests[instance.name]?.result
                            : undefined
                        }
                      />
                    ))}
                  </ul>
                </div>
              ))}
            </div>
          ))}

        {openEditor?.kind === "add" && (
          <AddInstanceDialog availableProviders={availableProviders} onCancel={closeEditor} onSuccess={closeEditor} />
        )}
        {openEditor?.kind === "apiKey" &&
          (() => {
            const target = findInstance(openEditor.name);
            return target ? <ApiKeyDialog instance={target} onCancel={closeEditor} onSuccess={closeEditor} /> : null;
          })()}
        {openEditor?.kind === "edit" &&
          (() => {
            const target = findInstance(openEditor.name);
            return target ? (
              <EditInstanceDialog instance={target} onCancel={closeEditor} onSuccess={closeEditor} />
            ) : null;
          })()}
        {openEditor?.kind === "oauth-redirect" && (
          <OAuthRedirectDialog
            name={openEditor.name}
            flowId={openEditor.flowId}
            authUrl={openEditor.authUrl}
            onCancel={closeEditor}
            onSuccess={closeEditor}
          />
        )}
        {openEditor?.kind === "device" && (
          <DeviceCodeDialog
            key={openEditor.flowId}
            name={openEditor.name}
            flowId={openEditor.flowId}
            userCode={openEditor.userCode}
            verificationUrl={openEditor.verificationUrl}
            intervalSeconds={openEditor.intervalSeconds}
            onCancel={closeEditor}
            onSuccess={closeEditor}
            onRestart={() => void handleOAuthStart(openEditor.name)}
          />
        )}

        <ConfirmDialog
          open={pendingConfirm !== null}
          title={pendingConfirm?.kind === "clear" ? "Clear credentials" : "Remove instance"}
          confirmLabel={pendingConfirm?.kind === "clear" ? "Clear" : "Remove"}
          busy={confirmBusy}
          onConfirm={() => void handleConfirmedAction()}
          onCancel={() => setPendingConfirm(null)}
        >
          {pendingConfirm?.kind === "clear"
            ? `Clear stored credentials for "${pendingConfirm.name}"?`
            : `Remove instance "${pendingConfirm?.name}"? This will also clear its stored credentials.`}
        </ConfirmDialog>
      </div>
    );
  }
  ```

  **File: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.module.css`** — append at the end (mirrors `launchServer.module.css`'s own `.diagnostics`/`.diagnosticsHeading`/`.diagnosticsList`, the sibling settings section's identical banner):

  ```css
  .diagnostics {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    padding: var(--space-3);
    border: 1px solid var(--edge);
    border-radius: var(--radius-pane);
    background: var(--surface-2);
  }

  .diagnosticsHeading {
    margin: 0;
    color: var(--ink-hi);
    font-size: var(--font-size-ui);
    font-weight: var(--font-weight-semibold);
  }

  .diagnosticsList {
    margin: 0;
    padding-left: var(--space-4);
    color: var(--ink-mid);
    font-size: var(--font-size-caption);
  }
  ```

  **File: `cmd/evener-hub/frontend/src/stores/credentials.ts`** (full replacement)

  ```ts
  // credentials.ts is the thin wire-truth gateway for the Providers &
  // credentials settings section: evener/instance/{list,create,edit,remove,
  // setDefault} plus the evener/auth/* RPCs the section's OAuth/API-key/device
  // flows drive. Follows stores/threads.ts's own requireClient()-via-
  // connectionStore pattern (this store has no connect() of its own).
  //
  // Every evener/instance/* mutation's Go handler returns the FULL updated
  // InstanceListResponse (appwire/types.go) - so create/edit/remove/setDefault
  // apply that response directly to `instances`/`availableProviders`/
  // `diagnostics`/`userLayer`/`writesRefused` instead of issuing a separate
  // evener/instance/list refetch, same round-trip the legacy credentials.html's
  // own instanceCreate/instanceEdit/... + refresh() pattern achieves in two
  // calls.
  //
  // Never-echo invariant: no method here stores a secret VALUE anywhere in
  // this store's state - setApiKey/loginComplete/deviceStart/devicePoll return
  // (and this store passes through) only AuthStatusResponse/AuthDeviceStart
  // Response/AuthDevicePollResponse shapes, none of which carry the secret
  // itself (write-only fields on the wire).
  import { useStore } from "zustand";
  import { createStore } from "zustand/vanilla";
  import { errorText } from "../protocol/errors";
  import type { AppwireClientLike } from "../protocol/testing/fakeClient";
  import type {
    AnyNotification,
    AuthDevicePollResponse,
    AuthDeviceStartResponse,
    AuthLoginCompleteResponse,
    AuthLoginStartResponse,
    AuthLogoutResponse,
    AuthStatusResponse,
    AuthTestResponse,
    InstanceCreateParams,
    InstanceEditParams,
    InstanceEntry,
    InstanceListResponse,
    ProviderDescriptor,
  } from "../protocol/types.gen";
  import { connectionStore } from "./connection";

  function requireClient(): AppwireClientLike {
    const client = connectionStore.getState().client;
    if (!client) {
      throw new Error("credentials store: no client connected; call useConnectionStore.getState().connect(client) first");
    }
    return client;
  }

  export interface CredentialsStoreState {
    instances: InstanceEntry[];
    availableProviders: ProviderDescriptor[];
    // diagnostics/userLayer/writesRefused mirror InstanceListResponse's own
    // optional fields (appwire/types.go), normalized here to always-present
    // values (spec §11.3) so components never need an `?? []`/`?? false`
    // fallback of their own.
    diagnostics: string[];
    userLayer: string;
    writesRefused: boolean;
    loading: boolean;
    error: string | null;
    fetch(): Promise<void>;
    create(params: InstanceCreateParams): Promise<void>;
    edit(params: InstanceEditParams): Promise<void>;
    remove(name: string): Promise<void>;
    setDefault(name: string): Promise<void>;
    // Auth mutations return the raw wire response and never touch
    // instances/availableProviders themselves - the caller (CredentialsSection)
    // re-fetches on success, matching the legacy's own "close editor +
    // refresh()" sequencing, and surfaces failures as inline errors/toasts
    // itself rather than this store swallowing them into an `error` field.
    setApiKey(provider: string, value: string): Promise<AuthStatusResponse>;
    logout(provider: string): Promise<AuthLogoutResponse>;
    loginStart(provider: string): Promise<AuthLoginStartResponse>;
    loginComplete(provider: string, flowId: string, redirectUrl: string): Promise<AuthLoginCompleteResponse>;
    deviceStart(provider: string): Promise<AuthDeviceStartResponse>;
    devicePoll(provider: string, flowId: string): Promise<AuthDevicePollResponse>;
    testCredentials(provider: string): Promise<AuthTestResponse>;
  }

  function applyList(resp: InstanceListResponse): void {
    credentialsStore.setState({
      instances: resp.instances,
      availableProviders: resp.availableProviders,
      diagnostics: resp.diagnostics ?? [],
      userLayer: resp.userLayer ?? "",
      writesRefused: resp.writesRefused ?? false,
    });
  }

  export const credentialsStore = createStore<CredentialsStoreState>((set) => ({
    instances: [],
    availableProviders: [],
    diagnostics: [],
    userLayer: "",
    writesRefused: false,
    loading: false,
    error: null,

    async fetch() {
      const client = requireClient();
      set({ loading: true, error: null });
      try {
        const resp = await client.request("evener/instance/list", {});
        set({
          instances: resp.instances,
          availableProviders: resp.availableProviders,
          diagnostics: resp.diagnostics ?? [],
          userLayer: resp.userLayer ?? "",
          writesRefused: resp.writesRefused ?? false,
          loading: false,
        });
      } catch (err) {
        set({ loading: false, error: errorText(err) });
      }
    },

    async create(params) {
      const client = requireClient();
      applyList(await client.request("evener/instance/create", params));
    },

    async edit(params) {
      const client = requireClient();
      applyList(await client.request("evener/instance/edit", params));
    },

    async remove(name) {
      const client = requireClient();
      applyList(await client.request("evener/instance/remove", { name }));
    },

    async setDefault(name) {
      const client = requireClient();
      applyList(await client.request("evener/instance/setDefault", { name }));
    },

    async setApiKey(provider, value) {
      const client = requireClient();
      return client.request("evener/auth/apiKey/set", { provider, value });
    },

    async logout(provider) {
      const client = requireClient();
      return client.request("evener/auth/logout", { provider });
    },

    async loginStart(provider) {
      const client = requireClient();
      return client.request("evener/auth/login/start", { provider });
    },

    async loginComplete(provider, flowId, redirectUrl) {
      const client = requireClient();
      return client.request("evener/auth/login/complete", { provider, flowId, redirectUrl });
    },

    async deviceStart(provider) {
      const client = requireClient();
      return client.request("evener/auth/device/start", { provider });
    },

    async devicePoll(provider, flowId) {
      const client = requireClient();
      return client.request("evener/auth/device/poll", { provider, flowId });
    },

    async testCredentials(provider) {
      const client = requireClient();
      return client.request("evener/auth/test", { provider });
    },
  }));

  export function useCredentialsStore(): CredentialsStoreState;
  export function useCredentialsStore<T>(selector: (state: CredentialsStoreState) => T): T;
  export function useCredentialsStore<T>(selector?: (state: CredentialsStoreState) => T): T | CredentialsStoreState {
    // Not a real conditional hook call - see stores/connection.ts's own
    // useConnectionStore for the full explanation.
    // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
    return selector ? useStore(credentialsStore, selector) : useStore(credentialsStore);
  }

  // --- notification-triggered refetch --------------------------------------
  //
  // evener/auth/updated BroadcastAlls to every connected client after a
  // successful auth mutation (login/logout/apiKey set/an authorized device
  // poll) from ANY of them - InstanceEntry's own activeSource/hasStoredOAuth/
  // hasStoredFile/storedEmail fields are exactly what such a mutation changes,
  // so a browser tab that already loaded the instance list goes stale
  // otherwise. Mirrors stores/extensions.ts's identical
  // wiring, applied here to this store's one wire-truth list. On the wire
  // evener/auth/updated carries {provider, activeSource} (notifyAuthUpdated,
  // cmd/evener-hub/app_rpc.go:764-767), but its generated
  // EvenerAuthUpdatedPayload type is empty ({}) because codegen can't see
  // into Go's untyped map[string]string - and this refetch is
  // payload-agnostic anyway (nothing reads those fields), so a debounced
  // evener/instance/list refetch is the only option, exactly like
  // evener/navigation/invalidated's own "just refetch" contract.
  const REFETCH_DEBOUNCE_MS = 250;

  let wiredClient: AppwireClientLike | null = null;
  let refetchTimer: ReturnType<typeof setTimeout> | undefined;

  function scheduleRefetch(): void {
    clearTimeout(refetchTimer);
    refetchTimer = setTimeout(() => {
      // fetch()'s own requireClient() throws outside its try/catch, by design
      // (see this file's own top comment) - a real rejection here would be an
      // unobserved background call with nothing awaiting it, so a rare
      // disconnect-during-the-debounce-window race must be swallowed here
      // rather than surfacing as an unhandled rejection.
      credentialsStore
        .getState()
        .fetch()
        .catch(() => {});
    }, REFETCH_DEBOUNCE_MS);
  }

  function handleNotification(n: AnyNotification): void {
    if (n.method === "evener/auth/updated") scheduleRefetch();
  }

  function attachNotifications(client: AppwireClientLike): void {
    if (client === wiredClient) return; // already wired to this exact client
    wiredClient = client;
    client.onNotification(handleNotification);
  }

  // Watches connectionStore for the client becoming available and attaches
  // this store's own notification handler to it - see stores/extensions.ts's
  // identical wiring for the full "why react to the store instead of reading
  // it once" rationale (a mount-order race between this module and AppShell's
  // own connect() effect).
  connectionStore.subscribe((state) => {
    if (state.client) attachNotifications(state.client);
  });
  const initialClient = connectionStore.getState().client;
  if (initialClient) attachNotifications(initialClient);

  // resetCredentialsStoreForTests resets this singleton store's state between
  // tests, including the module-private wiring/debounce bookkeeping above -
  // mirroring resetThreadsStoreForTests/resetTreeStoreForTests. No production
  // code should ever call this.
  export function resetCredentialsStoreForTests(): void {
    wiredClient = null;
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
    credentialsStore.setState({
      instances: [],
      availableProviders: [],
      diagnostics: [],
      userLayer: "",
      writesRefused: false,
      loading: false,
      error: null,
    });
  }
  ```

  **Incidental fixups** (apply the two diffs shown at the end of Step 1 now, to `oauthDialogs.test.tsx` and `Settings.test.tsx`) so the full suite compiles.

- [ ] **Step 4: Run the tests and `make test-web`**

  ```bash
  cd cmd/evener-hub/frontend && npm test -- src/panes/settings/sections/credentials src/stores/credentials.test.ts src/panes/settings/Settings.test.tsx
  ```

  All green, then from the repo root:

  ```bash
  make test-web
  ```

  `test-web.sh` runs typecheck, the full vitest suite, and lint concurrently — this is the gate that also catches any other file this task's grep missed.

- [ ] **Step 5: Commit**

  ```bash
  git add \
    cmd/evener-hub/frontend/src/stores/credentials.ts \
    cmd/evener-hub/frontend/src/stores/credentials.test.ts \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels.ts \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels.test.ts \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.module.css \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.test.tsx \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.edge.test.tsx \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceRow.tsx \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceRow.test.tsx \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.tsx \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.test.tsx \
    cmd/evener-hub/frontend/src/panes/settings/sections/credentials/oauthDialogs.test.tsx \
    cmd/evener-hub/frontend/src/panes/settings/Settings.test.tsx
  git commit -m "$(cat <<'EOF'
  feat(hub-frontend): move the credentials pane onto the registry's instance wire shape

  InstanceEntry/ProviderDescriptor/InstanceListResponse/InstanceCreateParams/
  InstanceEditParams changed shape in the provider registry cut-over (spec
  docs/superpowers/specs/2026-08-28-provider-registry-design.md §11.3): the
  credentials pane now groups by providerId, renders a diagnostics banner and
  disables instance CRUD while providers.toml fails to load, hides Remove and
  badges implicit instances, gates OAuth sign-in on the oauth-openai-codex
  auth scheme, and drives the add form from availableProviders with
  per-provider variable/api-key-env/credential-header inputs.
  EOF
  )"
  ```

---

### Task 11: `evener providers`, the flag-day CLI behaviour, and the credentials store's file layer

**Files:**
- Create: `cmd/evener/providers.go`, `cmd/evener/providers_test.go`
- Modify: `cmd/evener/main.go` (dispatch `providers`), `cmd/evener/models.go` (`loadRegistryForCLI` on `cmdutil.LoadRegistry`, no old-schema downgrade), `cmd/evener/openai_login.go`, `cmd/evener/openai_logout.go`, `cmd/evener/openai_status.go` (`--instance` defaults to `openai-codex`), `agent/internal/liveeval/paths.go` (tri-state), `internal/credentials/store.go` (+ tests; file layer only), `cmdutil/registry.go` (`StoreCredentialSource.Lookup` on the new `Get`), `cmd/evener-hub/app_auth.go` (`Layers` → `Get`)
- Tests: `cmd/evener/providers_test.go`, `cmd/evener/models_test.go` (old-schema case flips from "note" to error), `cmd/evener/openai_*_test.go` (default instance), `agent/internal/liveeval/paths_test.go`, `internal/credentials/store_test.go`

**Interfaces:**
- Consumes: Tasks 1, 2, 6, 7.
- Produces:
  - `credentials.Store`: `LoadStore(path) (*Store, error)`, `Get(name string) (value string, ok bool)` (file layer only), `Set`, `Clear`, `Names() []string` (sorted), `Path() string`. Deleted: `Source` and its constants, `Provider`, `EnvVars`, `Layers`, `InstanceLayers`, `List`, `ResolveKey`, `APIKeyFor`, and the `envvars` import.
  - `runProviders(args []string, stdin io.Reader, stdout, stderr io.Writer) error` with subcommands `list [--check]`, `probe <instance> [--write]`, `add <name> --base X [--base-url U] [--protocol P] [--surface S] [--var K=V]... [--api-key-env NAME] [--credential-header K=V] [--no-probe]`.
  - `liveeval.Paths(stateHome, userHome string) (stateHome, providerPath string, noUserLayer bool)`.

- [ ] **Step 1: The store's file layer (tests first)**

Rewrite `internal/credentials/store_test.go` around the surviving surface: `Get` returns `(value, true)` only for a non-empty file entry and never reads the environment (set `OPENAI_API_KEY` in the test and assert `Get("openai")` is `("", false)`); `Set`/`Clear` persist with mode 0600 and atomic rename; `Names()` lists the entries sorted; `Path()` returns the constructor's path. Then delete every roster-backed method and the `envvars` import from `store.go`:

```go
// Get returns the file-layer key stored under name (an instance name, spec
// §10). The environment is the registry's business, not the store's.
func (s *Store) Get(name string) (string, bool) {
	p, ok := s.data.Providers[strings.ToLower(name)]
	if !ok || strings.TrimSpace(p.APIKey) == "" {
		return "", false
	}
	return p.APIKey, true
}

// Names lists every entry, sorted, so a caller can report entries that
// name no instance (spec §14.1).
func (s *Store) Names() []string {
	out := make([]string, 0, len(s.data.Providers))
	for name := range s.data.Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Path is the file the store reads and writes.
func (s *Store) Path() string { return s.path }
```

`cmdutil.StoreCredentialSource.Lookup` becomes `return s.Store.Get(name)`; `app_auth.go`'s `hasFile, _ := c.creds.Layers(name)` becomes `_, hasFile := c.creds.Get(name)`.

- [ ] **Step 2: `evener providers` (tests first)**

`cmd/evener/providers_test.go` drives `runProviders` with `modelsLoadOptions`-style injection (reuse `cmd/evener/models.go`'s `modelsLoadOptions` — rename it `cliRegistryOptions` — to inject `registry.WithInstances`, `WithEnv`, `WithOffline(true)`, `WithoutCache()`, `WithStateRoot(t.TempDir())`, and an `httptest` server as an instance base URL):

```go
func TestProvidersListShowsInstancesNotesAndStrayEntries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "evener"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evener", "credentials.toml"), []byte("schema = 1\n[providers.kimi]\napi_key = \"old\"\n[providers.work]\napi_key = \"w\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cliRegistryOptions = []registry.Option{registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithEnv(func(k string) (string, bool) { if k == "GROQ_API_KEY" { return "gk", true }; return "", false }),
		registry.WithInstances(map[string]registry.Provider{"work": {Base: "openai", Transport: registry.Transport{BaseURL: "https://gw.example.com/v1"}}})}
	t.Cleanup(func() { cliRegistryOptions = nil })
	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"list"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("list: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"groq", "openai-chat", "env:GROQ_API_KEY", "work", "store", "user layer: none"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if !strings.Contains(stderr.String(), `credentials.toml entry "kimi" names no instance`) {
		t.Fatalf("stray entries are reported (spec §14.1):\n%s", stderr.String())
	}
}

func TestProvidersProbeReportsProtocols(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"c","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'max_output_tokens'","type":"invalid_request_error","param":"max_output_tokens"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	// ... inject instance "gw" {Base: "openai-compatible", Transport{BaseURL: srv.URL}, APIKey: "k"} as above ...
	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"probe", "gw"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("probe: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "openai-chat: ok") || !strings.Contains(out, "openai-responses: inconclusive") || !strings.Contains(out, "m1") {
		t.Fatalf("report:\n%s", out)
	}
}

func TestProvidersAddWritesEntryAndSkipsProbeWithoutCredential(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	// ... offline registry options ...
	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"add", "gw", "--base", "openai", "--base-url", "https://gw.example.com/v1", "--protocol", "openai-chat", "--surface", "generic", "--credential-header", "Authorization=Bearer $PORTKEY_KEY"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("add: %v\n%s", err, stderr.String())
	}
	l, exists, err := registry.ReadConfigFile(filepath.Join(root, "evener", "providers.toml"))
	if err != nil || !exists || l.Providers["gw"].CredentialHeaders["Authorization"] != "Bearer $PORTKEY_KEY" || l.Providers["gw"].Protocol != "openai-chat" {
		t.Fatalf("written entry: %v %v %+v", err, exists, l.Providers["gw"])
	}
	if !strings.Contains(stdout.String(), "PORTKEY_KEY") {
		t.Fatalf("add prints what to set when no credential resolves:\n%s", stdout.String())
	}
	err = runProviders([]string{"add", "bad", "--base", "openai", "--credential-header", "Authorization=Bearer literal-secret"}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("a credential header without a $VAR is refused")
	}
}
```

`cmd/evener/providers.go` — the shape (fill every branch; the probe request is spec §11.2's minimal request):

```go
func printProvidersUsage(w io.Writer) { /* list [--check] | probe <instance> [--write] | add <name> --base X ... */ }

func runProviders(args []string, _ io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 { printProvidersUsage(stderr); return nil }
	switch args[0] {
	case "list":
		return runProvidersList(args[1:], stdout, stderr)
	case "probe":
		return runProvidersProbe(args[1:], stdout, stderr)
	case "add":
		return runProvidersAdd(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printProvidersUsage(stderr); return nil
	}
	printProvidersUsage(stderr)
	return fmt.Errorf("unknown providers command %q", args[0])
}

// loadRegistryForCLI loads the registry offline with the store; an
// old-schema providers.toml is the §14.1 pointer and the CLI exits with it.
func loadRegistryForCLI(stderr io.Writer) (*registry.Registry, *credentials.Store, error) {
	r, store, err := cmdutil.LoadRegistry(append([]registry.Option{registry.WithOffline(true)}, cliRegistryOptions...)...)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range r.Warnings() {
		_, _ = fmt.Fprintln(stderr, "warning:", w)
	}
	for _, w := range r.StrayOAuthRecords() {
		_, _ = fmt.Fprintln(stderr, "warning:", w)
	}
	return r, store, nil
}

func runProvidersList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("providers list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "list each instance's models live and report reachability")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, store, err := loadRegistryForCLI(stderr)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, r.UserLayerNote())
	instances := r.Instances()
	known := map[string]bool{}
	for _, inst := range instances {
		known[inst.Name] = true
	}
	for _, name := range store.Names() {
		if !known[name] {
			if _, curated := r.Provider(name); !curated {
				_, _ = fmt.Fprintf(stderr, "warning: credentials.toml entry %q names no instance; re-enter it under the new instance name or delete it (spec §14.1)\n", name)
			}
		}
	}
	var client *llm.Client
	if *check {
		client = cmdutil.NewRegistryClient(r, "")
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	header := "NAME\tBASE\tPROTOCOL\tENDPOINT\tCREDENTIAL\tNOTES"
	if *check {
		header += "\tLIVE"
	}
	_, _ = fmt.Fprintln(tw, header)
	for _, inst := range instances {
		base := inst.Base
		if base == "" {
			base = inst.ProviderID
		}
		def := ""
		if inst.Default {
			def = "default"
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", inst.Name, base, inst.Protocol, inst.BaseURL, inst.CredentialSource, strings.Join(append(inst.Warnings, def), "; "))
		if *check {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			listing, err := client.Models(ctx, inst.Name)
			cancel()
			switch {
			case err != nil:
				row += "\terror: " + err.Error()
			case !listing.Live:
				row += fmt.Sprintf("\tregistry-only (%d models)", len(listing.Models))
			default:
				row += fmt.Sprintf("\tok (%d models)", len(listing.Models))
			}
		}
		_, _ = fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}
```

`runProvidersProbe(args, stdout, stderr)`: parse `--write`; load; `res, err := r.ResolveInstance(name)`; when `res.Transport.ModelsEndpoint != registry.EndpointUnsupported`, `client.Models` and print the ids ("discovered models are printed, never written"); for the OpenAI protocols (`res.Protocol` is `openai-chat` or `openai-responses`), probe **both**: for each protocol `p`, load a probe registry with the instance's own entry (from `registry.ReadConfigFile`, or `registry.Provider{ID: name}` for an implicit one) with `Protocol: p` injected through `registry.WithInstances`, resolve `name/<DefaultModel or first discovered id>`, and call `llm.ProtocolFor(p).Complete(ctx, probeRequest(), res)` with

```go
func probeRequest(model string) llm.Request {
	return llm.Request{
		Model: model, Messages: []llm.Message{llm.User("ping")}, MaxTokens: new(16),
		Tools: []llm.ToolDefinition{{Name: "noop", Description: "does nothing", Parameters: map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}}}},
		ResponseFormat: &llm.ResponseFormat{Type: "text"},
	}
}
```

classify each result: success → `ok`; an `llm.Error` whose message names the max-tokens field (`max_tokens`, `max_output_tokens`, `max_completion_tokens`) → `inconclusive`; anything else → `unsupported: <message>`; print `openai-chat: ok` / `openai-responses: inconclusive (…)` lines. With `--write`: when exactly one protocol succeeded, read the config layer, set (or create) `l.Providers[name]` with `Protocol` = that protocol, write, print "wrote protocol = … to <path>"; when both succeeded, keep the registry default and say both work; when none, write nothing. Non-OpenAI protocols probe only their own protocol.

`runProvidersAdd(args, stdout, stderr)`: flags `--base` (required, must be `r.Provider(base)`), `--base-url`, `--protocol`, `--surface`, repeated `--var K=V`, `--api-key-env NAME`, repeated `--credential-header K=V` (each value must contain `$` and no whitespace-free literal that looks like a key — the rule is "must reference a $VAR"), `--no-probe`; validate the name with `registry.ValidInstanceName`; refuse an existing entry; write the entry via `registry.ReadConfigFile`/`WriteConfigFile`; reload the registry; `res, _ := r.ResolveInstance(name)`; when `res.Credential.Source == "none"` and the auth scheme needs a credential, print `no credential resolves for <name>: set <NAME>_API_KEY, add --api-key-env, or enter a key with the hub's credentials pane (credentials.toml [providers.<name>])` and return without probing; else unless `--no-probe`, run `runProvidersProbe([]string{name, "--write"}, …)`.

`cmd/evener/main.go`: `case "providers": return true, "evener providers", runners.providers(args[1:], stdin, stdout, stderr)` and the runner wiring like `models`.

- [ ] **Step 3: The old-schema pointer, the Codex defaults, and the tri-state reader**

`cmd/evener/models.go`: `loadRegistryForCLI` is the shared one above (move it to `providers.go` or a small `cli_registry.go`); delete the `ErrOldSchema` note-and-retry; `models_test.go`'s old-schema case now asserts the command fails and the error contains `§14.1`. `openai_login.go`/`openai_logout.go`/`openai_status.go`: `fs.String("instance", "openai-codex", "instance name (default: openai-codex)")`; their tests assert the record lands at `auth/openai-codex.json`. `agent/internal/liveeval/paths.go`:

```go
// Paths resolves the state home and the provider-config path used by live
// evals under the runtime's tri-state rule (spec §10): EVENER_PROVIDERS_CONFIG
// unset → $XDG_CONFIG_HOME/evener/providers.toml (~/.config fallback);
// present and empty → no user layer (noUserLayer = true, providerPath "");
// set → that path.
func Paths(stateHome, userHome string) (string, string, bool) {
	stateHome = strings.TrimSpace(stateHome)
	userHome = strings.TrimSpace(userHome)
	if stateHome == "" {
		stateHome = filepath.Join(userHome, ".local", "state")
	}
	providerPath, ok := envvars.EVENERProvidersConfig.LookupEnv()
	switch {
	case ok && strings.TrimSpace(providerPath) == "":
		return stateHome, "", true
	case ok:
		return stateHome, providerPath, false
	}
	configHome := envvars.XDGConfigHome.Trimmed()
	if configHome == "" {
		configHome = filepath.Join(userHome, ".config")
	}
	return stateHome, filepath.Join(configHome, "evener", "providers.toml"), false
}
```

(update its two callers and `paths_test.go` with the three states).

- [ ] **Step 4: Gate, lint, commit**

```bash
for m in . llm agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add cmd/evener/providers.go cmd/evener/providers_test.go
git add -u cmd/evener agent/internal/liveeval internal/credentials cmdutil cmd/evener-hub/app_auth.go
git commit -m "feat(cli): evener providers list|probe|add, the flag-day pointer, Codex defaults, and a file-only credentials store"
```

---

### Task 12: Cost from the registry; TUI picker metadata from descriptors

**Files:**
- Modify: `llm/pricing.go` (+ `pricing_test.go`), `llm/lcfg_config_surface_fuzz_test.go` (`Fuzz_lcfg_GetPrice` becomes `Fuzz_lcfg_PriceFromCost` over fuzzed `registry.Cost` values; rename its row in `scripts/fuzz/fuzz-targets.txt`), `appwire/cost.go` (+ `cost_test.go`), `agent/events/payloads.go` (`AssistantTextEndData.Provider`), `agent/session_model_call.go` (emit it), `agent/session.go` or `agent/session_state.go` (`(*Session).CostFor`), `internal/appprojector/appwire_projection.go` (+ tests), `server/appwire_runtime.go` (+ tests), `cmd/evener-hub/app_threadread.go`, `cmd/evener-hub/web_format.go`, `cmd/evener-hub/web_workspace.go` (+ tests), `cmd/evener-tui/hub_commands.go`, `cmd/evener-tui/main.go` (+ tests)

**Interfaces:**
- Consumes: `registry.Cost`, `Profile.Cost`, `Client.Resolve`, `hubcore.WebConfig.Registry`, `appwire.ModelDescriptor` cost/capability fields (filled from `Resolved` since Task 9).
- Produces: `func PriceFromCost(c *registry.Cost) (Price, bool)` in `llm` (`GetPrice`, `DefaultPrice`, `priceFromModelInfo` deleted); `func EstimateCost(cost *registry.Cost, usage *EvenerUsage) string` in `appwire`; `func (s *Session) CostFor(ref string) *registry.Cost` (resolves `ref` on the session's client; nil when unresolvable or priceless); `events.AssistantTextEndData.Provider string` (`provider,omitempty`); `func (p *AppEventProjector) SetCostLookup(func(provider, model string) *registry.Cost)`; hub helper `func costFor(reg hubcore.ProviderRegistry, instance, model string) *registry.Cost`; TUI `modelInfoMetaTail(d appwire.ModelDescriptor) string` and `visionModelPickerItems(models []appwire.ModelDescriptor, items []tuipick.ModelPickerItem)`.

- [ ] **Step 1: Tests first**

`llm/pricing_test.go` replaces the catalog cases with: `PriceFromCost(nil)` → `(Price{}, false)`; a cost with input/output only → both rates and nil cache tiers; a cost with `CacheRead`/`CacheWrite` → `CacheReadPerM` and `CacheCreation5mPerM` set, `CacheCreation1hPerM` nil (models.dev carries one write rate); `EstimateCost` arithmetic unchanged. `appwire/cost_test.go`: `EstimateCost(nil, usage) == ""`, `EstimateCost(&registry.Cost{Input: 3, Output: 15}, &EvenerUsage{InputTokens: 1_000_000, OutputTokens: 100_000}) == "~$4.50"`. `internal/appprojector`: a projector with a cost lookup stamps `turn.Cost` from `(provider, model)` of the `AssistantTextEnd` event; without a lookup, no cost. `server`: the status payload's `Cost` comes from `sess.CostFor(status.Model)`. Hub: a past thread's cost resolves `ProfileID/Model` through `cfg.Registry`; `web_format` renders `thread.Evener.Cost` as delivered by the daemon instead of recomputing. TUI: `modelInfoMetaTail` renders `200K ctx · $3.00/$15.00 · tools,vision,reasoning` from a descriptor; `visionModelPickerItems` keeps only descriptors with `SupportsVision == true`.

- [ ] **Step 2: Implement**

```go
// PriceFromCost is the registry's cost as per-million rates; models.dev
// carries one cache-write rate, reported as the 5-minute tier.
func PriceFromCost(c *registry.Cost) (Price, bool) {
	if c == nil {
		return Price{}, false
	}
	p := Price{InputPerM: c.Input, OutputPerM: c.Output}
	if c.CacheRead > 0 {
		p.CacheReadPerM = new(c.CacheRead)
	}
	if c.CacheWrite > 0 {
		p.CacheCreation5mPerM = new(c.CacheWrite)
	}
	return p, true
}
```

```go
// EstimateCost returns a "~$X.XX" estimate for usage at the row's cost,
// or "" when either is missing (spec §7.5: cost comes from Resolved).
func EstimateCost(cost *registry.Cost, usage *EvenerUsage) string {
	if usage == nil {
		return ""
	}
	price, ok := llm.PriceFromCost(cost)
	if !ok {
		return ""
	}
	return fmt.Sprintf("~$%.2f", llm.EstimateCost(usage.InputTokens, usage.CacheReadTokens, usage.OutputTokens, price))
}
```

`agent`: `AssistantTextEndData{…, Provider: resp.Provider}` at the emit site; `func (s *Session) CostFor(ref string) *registry.Cost { res, err := s.client.Resolve(ref); if err != nil { return nil }; return res.Caps.Cost }`. `server/appwire_runtime.go`: `Cost: appwire.EstimateCost(sess.CostFor(status.Model), usage)` (`status.Model` is already `instance/model`); the projector constructed there gets `SetCostLookup(func(provider, model string) *registry.Cost { return sess.CostFor(provider + "/" + model) })`. Projector: track `activeTurnProvider` from the event and call the lookup in `stampTurnUsage`. Hub: `costFor(reg, instance, model)` = `reg.Get().Resolve(instance + "/" + model)` → `Caps.Cost` (nil on error); `app_threadread.go`'s three sites use `costFor(cfg.Registry, entry.Meta.ProfileID, entry.Meta.Model)` (and the per-turn model where a turn recorded one); `web_format.go`/`web_workspace.go` copy `thread.Evener.Cost`. TUI: delete `warmModelCatalog` and the `llm` import; `buildModelPickerItems` passes each descriptor to `modelInfoMetaTail`; `visionModelPickerItems` takes the descriptors.

- [ ] **Step 3: Gate and commit**

```bash
for m in . llm agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint
git add -u llm/pricing.go llm/pricing_test.go appwire agent/events agent/session_model_call.go agent/session.go internal/appprojector server cmd/evener-hub cmd/evener-tui
git commit -m "feat: price sessions and pickers from the registry's cost, not the LiteLLM catalog"
```

---

### Task 13: Delete the adapter stack, `providercfg`, the catalog, and the roster

**Files:**
- Delete: `llm/providers/{openai,openaicompat,kimi,kimi_anthropic,glm,minimax,ollama,openrouter,openrouter_anthropic,kimicoding,internal/providerfwd}/` (whole directories); in `llm/providers/anthropic` and `llm/providers/google`: `adapter.go`, the `Adapter`-only helpers, `adapter_test.go`, `wire_capture_test.go`, every `*_fuzz_test.go` and `*_test.go` that constructs `Adapter` (keep the files the `Protocol` shares — `request.go`'s builders, `stream.go`'s decoders, `models.go` parsers — and their tests); `llm/env_registry.go`, `llm/providers_config.go` (+ tests); `llm/providercfg/` (whole); `llm/model_catalog.go`, `llm/model_catalog_embedded.go`, `llm/data/` (+ tests); `envvars/providers.go`, `envvars/ollama_host.go` (+ tests); `cmdutil/seed.go`, `cmdutil/materialize.go` (+ tests); `llm/lcfg_config_surface_fuzz_test.go`'s `Fuzz_lcfg_NewFromEnv`/`Fuzz_lcfg_NewFromProviders`, `llm/client_config_edges_fuzz_test.go`, `cmdutil/coverage_program_fuzz_test.go`
- Create: `llm/providers/responses/recompute.go` (+ test), `llm/providers/chatcompletions/recompute.go` (+ test); the ported attempt-ordering tests `llm/providers/{anthropic,google,responses,chatcompletions}/stream_attempts_test.go`; `cmdutil/envvars_registry_test.go`
- Modify: `llm/client.go` (delete `nameToTag`, `SetNameToTag`, `behaviorTagFor`, `BehaviorTagOf`, `ListModels`, `ModelLister`, `SupportsToolChoice`, `ToolChoiceSupporter`, `ValidateModelCompatibility`, `ModelCompatibilityValidator`, `NonDefaultEligible`; `providerStampStream` loses its tag), `llm/errors.go`, `llm/sdk_errors.go` (`BehaviorTag()` off the `Error` interface, `StampErrorBehaviorTag`, `behaviorTagSetter`, the fields), `llm/token_count.go` (no tag stamp), `llm/client_capabilities_fuzz_test.go`, `llm/core_contracts_fuzz_test.go`, `llm/lcfg_config_surface_fuzz_test.go` (the `NewFromEnv`/`NewFromProviders` targets), `llm/providers/all/all.go`, `envvars/envvars.go` (retired vars deleted, new ones added), `cmd/evener/main.go` and `cmd/evener-hub/main.go` (`print*EnvVars`), `agent/doctor/apilog.go` (`recomputeExtractors`), `cmd/evener-internalcheck/main.go` (`libraryPackages` minus `providercfg`), `scripts/fuzz/fuzz-targets.txt`, `llm/go.mod`/`go.sum`, `go.work.sum` (tidy), `docs/superpowers/specs/…` untouched

**Interfaces:**
- Consumes: everything before.
- Produces: `responses.ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error)` (JSON object or Responses SSE), `chatcompletions.ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error)` (JSON object or Chat Completions SSE); `agent/doctor` `recomputeExtractors` keys `openai_public`, `openai_codex` → responses; `openai_chat_completions` → chatcompletions. `envvars` gains, as Public `Var`s, every variable the curated overlay names that it lacks today (`OPENAI_CODEX_BASE_URL`, `GOOGLE_BASE_URL`, `GROQ_API_KEY`, `GROQ_BASE_URL`, `XAI_API_KEY`, `XAI_BASE_URL`, `CEREBRAS_API_KEY`, `CEREBRAS_BASE_URL`, `MISTRAL_API_KEY`, `MISTRAL_BASE_URL`, `TOGETHER_API_KEY`, `TOGETHERAI_BASE_URL`, `DEEPSEEK_API_KEY`, `DEEPSEEK_BASE_URL`, `ZHIPU_API_KEY`, `ZAI_BASE_URL`, `ZAI_CODING_PLAN_BASE_URL`, `MOONSHOT_API_KEY`, `MOONSHOTAI_BASE_URL`, `KIMI_FOR_CODING_BASE_URL`, `AWS_BEARER_TOKEN_BEDROCK`, `AWS_REGION`, `AZURE_API_KEY`, `AZURE_RESOURCE_NAME`, `AZURE_COGNITIVE_SERVICES_RESOURCE_NAME`, `GOOGLE_VERTEX_HOST`, `GOOGLE_VERTEX_PROJECT`, `GOOGLE_VERTEX_LOCATION`, `GOOGLE_APPLICATION_CREDENTIALS`, `ANTHROPIC_COMPATIBLE_API_KEY`, `ANTHROPIC_COMPATIBLE_BASE_URL`, `GOOGLE_COMPATIBLE_API_KEY`, `GOOGLE_COMPATIBLE_BASE_URL`; key-shaped names are `Secret: true`), loses the retired ones (`OPENAI_CHATGPT_BASE_URL`, `OPENAI_COMPATIBLE_PROVIDER_QUIRKS`, `GEMINI_BASE_URL`, `GLM_API_KEY`, `GLM_BASE_URL`, `KIMI_BASE_URL`, `KIMI_CODING_API_KEY`, `KIMI_CODING_BASE_URL`, `OLLAMA_BASE_URL`), and `KIMI_API_KEY`'s summary reads "Kimi coding-plan API key (models.dev's convention)". `cmdutil/envvars_registry_test.go` pins it: every `APIKeyEnv` and `VarsEnv` value of every provider in `provider.EmbeddedRegistry()` (Task 7) with `Implicit == true` must be `envvars.Find`-able, so the scrub lists derived from `envvars.All()` cover every variable the registry reads.

- [ ] **Step 1: Port the attempt-ordering guarantees before deleting their old homes**

For each old `wire_capture_test.go` (`openai`, `openaicompat`, `anthropic`, `google`): read what it pins about API attempts — the attempt record is appended before the stream's terminal event reaches the consumer, and an SSE read timeout is classified as a stream-read timeout (not a connect or request timeout) — and write the same assertions against `Protocol.Stream(ctx, req, res)` in `stream_attempts_test.go` of `responses`, `chatcompletions`, `anthropic`, and `google` (an `httptest` server that streams two events then stalls; `llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(ctx, llm.NewAPIAttemptGroup(id)), sink)` and `llm.WaitForPriorAPIAttempts(ctx)` as the plan-2 tests do). Run them green, commit: `test(providers): pin attempt ordering and SSE timeout classification on the protocols`.

- [ ] **Step 2: Move the recorded-response extractors**

`responses/recompute.go`: `ExtractRecordedResponse` from `openai/responses_recompute.go` re-pointed at the package's own JSON decoder and SSE decoder (`fromResponses` and the stream decoder plan 2 ported); `chatcompletions/recompute.go`: one extractor that decodes a JSON object through the package's response decoder and a `data:` SSE stream through its stream decoder (the old `openai.ExtractRecordedChatCompletionsResponse` and `openaicompat.ExtractRecordedResponse` collapse into it, since the registry emits one endpoint family, `openai_chat_completions`, for every chat instance). Move their tests. `agent/doctor/apilog.go`: update `recomputeExtractors` and its comment; the `openai_compatible_chat_completions` key goes (no new record will carry it; a doctor run over an old log skips unknown families as it does today). Commit: `refactor(doctor): recompute recorded responses through the protocol packages`.

- [ ] **Step 3: Delete, then make the build green**

```bash
git rm -r llm/providers/openai llm/providers/openaicompat llm/providers/kimi llm/providers/kimi_anthropic llm/providers/glm llm/providers/minimax llm/providers/ollama llm/providers/openrouter llm/providers/openrouter_anthropic llm/providers/kimicoding llm/providers/internal/providerfwd llm/providercfg llm/data
git rm llm/env_registry.go llm/providers_config.go llm/model_catalog.go llm/model_catalog_embedded.go envvars/providers.go envvars/ollama_host.go cmdutil/seed.go cmdutil/materialize.go
git rm llm/providers/anthropic/adapter.go llm/providers/google/adapter.go   # plus their Adapter-only siblings and tests; keep the shared builders/decoders
```

then `go build ./... && (cd llm && go build ./...) && (cd envvars && go build ./...)` and `go vet ./...` in every module (test files count) and fix every remaining reference. Known stragglers: the shared `anthropic/models.go` and `google/models.go` parsers fill capability defaults from `llm.EmbeddedModelCatalog` — drop those fills (the registry's layers supply the facts now) and keep the parsers; every remaining `llm.NewFromEnv`/`NewFromAvailableProviders` caller in tests (`cmd/evener/main_test.go`, `agent/plugin_integration_live_test.go`, `agent/integration_smoke_test.go`, `agent/forced_note_live_test.go`, `agent/internal/contextmgr/*_eval_test.go`) builds its client as `llm.NewClient(llm.WithRegistry(r))` with `r, err := registry.Load()` (live and eval tests want the real environment and user layer) or `cmdutil.LoadClient("")` where `cmdutil` is importable; `llm/providers/openai/adapter_test.go`'s `llm.WithStateDir` use dies with the package. Fix every remaining reference (`grep -rn --include='*.go' 'providercfg\|EmbeddedModelCatalog\|ModelInfo\b\|BehaviorTag\|openaicompat\|providerfwd\|kimicoding\|NewFromEnv\|NewFromProviders\|NewFromAvailableProviders\|envvars\.Providers\|APIKeyVars\|RequiresNoCredential\|InjectAPIKeyVar\|BaseURLVar\|ResolveOllamaBaseURL\|NormalizeOllamaHost\|DefaultPrice\|GetPrice' .` must print nothing outside `docs/` and `.superpowers/`). `llm.DefaultClient`/`SetDefaultClient`: if any non-test caller remains (`grep -rn 'llm.DefaultClient()'`), keep them with `DefaultClient` lazily constructing `NewClient()`; otherwise delete. `llm/providers/all/all.go` imports only `chatcompletions`, `responses`, `anthropic`, `google`, `tokenauth`. Update `cmd/evener/main.go`'s `printRunEnvVars` and the hub's `printHubEnvVars` to the surviving vars plus one line `  <ID>_API_KEY / <ID>_BASE_URL\tany implicit provider's key or base URL (evener providers list)`. Rewrite the four generic fuzz tests against the registry client (`FuzzClientCapabilities` over `LiveModelLister`/`InputTokenCounter`/`ResponsesContinuationPlanner` overrides and `CanServe`; `FuzzClientConfigEdges` over `registry.WithInstances` edge records — unknown base, hidden pseudo-provider, `api_key_env = []`; `FuzzCmdutilCoverage` over `LoadRegistry` with fuzzed `providers.toml` bytes at `EVENER_PROVIDERS_CONFIG`) and update `scripts/fuzz/fuzz-targets.txt` (delete every row for a deleted package; add rows for `FuzzClientConfigEdges`'s replacement if renamed; `make fuzz-registry-check`). `cd llm && GOWORK=off go mod tidy` with the temporary `replace` lines the plan-2 ledger describes, then remove them (the workspace resolves siblings); `go work sync`.

- [ ] **Step 4: Gate, lint, commit**

```bash
for m in . llm envvars auth identifier invariant fuzz agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'); done
export PATH="$(go env GOROOT)/bin:$PATH" && make lint && make fuzz-registry-check
git add -A llm envvars cmdutil cmd agent scripts/fuzz/fuzz-targets.txt go.work.sum   # after `git status` shows only the intended deletions and edits
git commit -m "refactor: delete the provider adapters, providercfg, the LiteLLM catalog, and the envvars roster (flag day)"
```

(`git add -A` on named paths is acceptable here only after a `git status` review; the whole point of the task is the deletion set.)

---

### Task 14: Documentation around the registry

**Files:**
- Rewrite: docs/llm-providers.md, docs/llm-provider-config-and-launch.md, docs/ollama.md
- Modify: README.md:76-83,232,238-253, docs/getting-started.md:83-97, docs/evener-hub.md:228-277, docs/developing-evener/environment.md:18,39-68

**Interfaces:**
- Consumes: the commands and files the docs describe (`evener models list|inspect|refresh`, `evener providers list|probe|add`, `evener openai login --instance openai-codex`, `providers.toml`, `credentials.toml`, `EVENER_PROVIDERS_CONFIG`, `EVENER_CREDENTIALS_CONFIG`, `<ID>_API_KEY`, `<ID>_BASE_URL`).
- Produces: nothing code-level; every statement must match the spec.

Sourcing rule applied throughout this task: every variable name, instance id, and default value below is quoted from `docs/superpowers/specs/2026-08-28-provider-registry-design.md` (cited by section, e.g. "§6.2") or from `llm/registry/data/providers_overlay.toml` (cited as "overlay"). Comparing the two turned up no actual conflicts — every overlay value matches what the spec's prose describes. Where a fact (mainly a handful of `api_key_env` names) is asserted by **neither** source, it is marked `[UNCONFIRMED …]` rather than invented. Resolve every such bracket before writing a line of prose, and only from the registry itself: add a temporary test to `llm/registry` that loads the registry (`Load(WithOffline(true), WithoutCache(), WithNoUserLayer(), WithEnv(func(string) (string, bool) { return "", false }))`) and prints `Provider(id).APIKeyEnv`, `Provider(id).Transport.VarsEnv`, `DefaultModel`, and `CheapModel` for every id in the overlay's `default_order` (`go test ./registry/ -run TestPrintProviderVariables -v`), copy the printed values into the docs, and delete the test before committing. Never resolve a bracket by pattern-matching the `<ID>_API_KEY` convention — the spec explicitly warns against exactly that guess for `togetherai` (§10: "`TOGETHERAI_API_KEY` is not an undocumented alias of `TOGETHER_API_KEY`"). No bracket may survive into the committed docs.

- [ ] **Step 1: docs/llm-providers.md** — Full rewrite around the registry (spec §3–§4.1, §5–§5.2, §6.2, §7.1–§7.3, §8.4, §9, §10 (schema half), §11.1–§11.2, §12). Drop the "as of 2026-05, Phase 1a/1b/1c" dating from the intro; point at `docs/superpowers/specs/2026-08-28-provider-registry-design.md` instead of the two 2026-05-29 phase specs.

  **New outline, with the facts to write under each heading:**

  `## Concepts` (§3, §4.1)
  - The five nouns table verbatim in spirit: **Protocol** (wire format: encode/decode/list/count-tokens; one Go package per protocol, `llm/providers/{chatcompletions,responses,anthropic,google}`); **Transport** (how to reach an endpoint: auth scheme, URL templates, constant headers/body — `registry.Transport`); **Provider** (a named endpoint definition: id, display name, transport, default protocol, surface, family default, caps, models — `registry.Provider`); **Model** (a row under a provider — `registry.Model`); **Surface** (the agent-facing vendor family a model was trained for — `openai | anthropic | google | generic`; a model attribute, never changes the wire shape, only which doc files/tool names/prompt sections the agent uses).
  - One line on **Resolution**: merges layered `Provider` records into one `Resolved` record per `instance/model` reference; adapters consume `Resolved` and nothing else.
  - One line on **Caps** (§4.1): one flat struct shared by every protocol; fields a protocol doesn't use are ignored; `Fields map[string]bool` is a denylist ("send this optional wire field or not") for the transforms that can't be expressed any other way.
  - Sub-heads `### Why keyed by provider name, not endpoint` (§3.1 — a URL says nothing behind a gateway; behavior attaches to a named provider record, not a `baseUrl.includes(...)` check) and `### Why Surface is separate from Protocol and Provider` (§3.2 — Claude over OpenRouter's chat endpoint still wants the Anthropic tool set; Surface is derived from the model row's `family`, with overlay pins where a vendor's family doesn't imply the surface it wants — cite the Kimi/MiniMax anthropic-surface pin from §6.2 as the example, and the `orclaude` recipe from §14.1 as what replaces `openrouter-anthropic`).

  `## Layers` (§5)
  - The five-layer table verbatim: 1 **Upstream snapshot** (`llm/data/models.dev.json.gz`, refreshed by `make refresh-model-catalog`); 2 **Upstream cache** (`<state-root>/catalog/models.dev.json` + `.meta`, background 24h refresh); 3 **Curated overlay** (`llm/data/providers_overlay.toml` — note: the actual file is at `llm/registry/data/providers_overlay.toml` in this repo; use the real path, not the spec's shorthand); 4 **User config** (`<config-root>/providers.toml`, `credentials.toml` its sibling, OAuth records at `auth/<instance>.json` under the state root); **live** listing (the instance's `ModelsEndpoint`, cached per process).
  - Merge rule: layer 2 replaces layer 1 wholesale when newer, never merged; layers 3 and 4 overlay field-wise.
  - What the live layer does: establishes existence of models the catalog lacks; supplies `Tools`, `InputModalities`, `ContextWindow`, `MaxOutputTokens`, `EffortValues`, `Cost`, `Reasoning`, plus `ThinkingAlwaysOn` (only when OpenRouter's `reasoning.mandatory` is `true`); overrides catalog/curated facts but never a field the user layer (layer 4) set; never touches any other wire-shaping cap. Non-chat live rows (`embedding`, `whisper`, `tts`, `dall-e`, `moderation`, `audio`, `transcribe`, `image`, `realtime`, `davinci`, `babbage`, `sora`) are dropped.

  `## Instances` (§5.1, §5.2)
  - Definition: an instance is a named, usable provider — **explicit** (every `[providers.X]` entry in `providers.toml`) or **implicit** (every curated `implicit = true` overlay row not shadowed by an explicit entry of the same name, not `Hidden`, whose credential resolves without network access).
  - The credential test by auth scheme (quote exactly): `bearer`/`header` need a credentials-store entry for the id or one of the provider's own `APIKeyEnv` variables set; `oauth-openai-codex` needs the instance's OAuth record and nothing else; `gcp-adc` needs `GOOGLE_APPLICATION_CREDENTIALS` or the well-known ADC file (never a live metadata-server probe at load); `none`/`optional-bearer` need only the base URL resolving.
  - "Not Hidden" requirement: the cloud providers additionally need their location/region variables — `google-vertex*` needs `GOOGLE_VERTEX_PROJECT` and `GOOGLE_VERTEX_LOCATION`; `azure*` needs `AZURE_RESOURCE_NAME`; `amazon-bedrock` needs `AWS_REGION`.
  - What does **not** conjure an instance from the environment alone: `GITHUB_TOKEN`, `HF_TOKEN`, `DATABRICKS_TOKEN` (explicit negative examples from §5.1 — worth keeping, they're the kind of thing a user will try).
  - `### The default instance`: `default` from `providers.toml` when set; else the first instance (explicit or implicit) with a `DefaultModel`, ranked by the curated `default_order` (an explicit entry sharing a curated implicit id keeps that id's rank), then every other explicit instance by sorted name. `openai-codex` precedes `openai` in `default_order` (preserves "stored OAuth beats API key"). A `default` naming neither an explicit instance nor a curated implicit id is a load error; naming a curated implicit id whose credential doesn't resolve here is a warning that falls through the chain (so `evener models inspect` still works without a key). Error text when no instance exists at all: `no default instance: set default in providers.toml or export a provider key`; when instances exist but none has a default model, the error names them (example given in spec: `azure has no default model; pass azure/<deployment> or set default`).
  - `### Resolving without a credential` (§5.2): `Resolve` succeeds for any explicit/implicit instance or curated implicit id even with no credential — the record carries `Warnings: no credential` (omitted for `none`/`optional-bearer`) and an empty `Credential`; the actual "no credential for `<instance>`" error fires only at the first request. This is what makes `evener models inspect openai/gpt-5.5` work with no key configured.

  `## The implicit provider list` (§6.2 + overlay — full ordered `default_order`/implicit list, 21 entries)

  Write this as a table: **Instance id | Key variable(s) | Base-URL variable (curated default) | Notes**. Populate from the overlay file directly (`llm/registry/data/providers_overlay.toml`), row order = `default_order`:

  | id | key var(s) | base-URL var (default) | notes |
  |---|---|---|---|
  | `anthropic` | `ANTHROPIC_API_KEY` (§10) | `ANTHROPIC_BASE_URL` (`https://api.anthropic.com/v1`) | surface `anthropic`, family `claude`; `default_model = claude-opus-5`, `cheap_model = claude-haiku-4-5` |
  | `openai-codex` | none — OAuth only, `api_key_env = []` so a bare `OPENAI_API_KEY` never yields this instance (§5.1) | `OPENAI_CODEX_BASE_URL` (`https://chatgpt.com/backend-api/codex`) | see the Codex transport section below; `default_model = gpt-5.6`, `cheap_model = gpt-5.6-luna` |
  | `openai` | `OPENAI_API_KEY` (§5.1, §9.5) | `OPENAI_BASE_URL` (`https://api.openai.com/v1`) | surface `openai`; `default_model = gpt-5.6`, `cheap_model = gpt-4.1-nano` |
  | `google` | `GEMINI_API_KEY`, then `GOOGLE_API_KEY` (overlay pin, §6.2) | `GOOGLE_BASE_URL` (`https://generativelanguage.googleapis.com/v1beta`) | surface `google`; `default_model = gemini-3.7-flash`, `cheap_model = gemini-2.5-flash-lite` |
  | `groq` | `GROQ_API_KEY` (spec's own §10 example: "registry says `GROQ_API_KEY` already") | `GROQ_BASE_URL` (`https://api.groq.com/openai/v1`) | `default_model = openai/gpt-oss-120b`, `cheap_model = llama-3.1-8b-instant` |
  | `zai` | `ZHIPU_API_KEY` (§6.2 explicit) | `ZAI_BASE_URL` (`https://api.z.ai/api/paas/v4`) | `thinking_format = zai`; `default_model = glm-5.3`, `cheap_model = glm-4.7-flash` |
  | `deepseek` | `[UNCONFIRMED]` | `DEEPSEEK_BASE_URL` (`https://api.deepseek.com` — no version segment; a documented models.dev exception, §6.1) | `thinking_format = deepseek`; `default_model = deepseek-v4-pro`, `cheap_model = deepseek-v4-flash` |
  | `openrouter` | `[UNCONFIRMED — today's doc says `OPENROUTER_API_KEY`; re-verify rather than carry it forward unchecked]` | `OPENROUTER_BASE_URL` (`https://openrouter.ai/api/v1`) | `thinking_format = openrouter`; `default_model = anthropic/claude-opus-5`, `cheap_model = google/gemini-2.5-flash-lite` |
  | `xai` | `XAI_API_KEY` (§5.1, §13 examples) | `XAI_BASE_URL` (`https://api.x.ai/v1`) | `default_model = grok-4.6`, `cheap_model = grok-4.3` |
  | `mistral` | `[UNCONFIRMED]` | `MISTRAL_BASE_URL` (`https://api.mistral.ai/v1`) | `default_model = mistral-medium-latest`, `cheap_model = ministral-3b-latest` |
  | `cerebras` | `[UNCONFIRMED]` | `CEREBRAS_BASE_URL` (`https://api.cerebras.ai/v1`) | `default_model = gpt-oss-120b`, `cheap_model = gpt-oss-120b` |
  | `togetherai` | `[UNCONFIRMED — spec explicitly warns not to assume `TOGETHERAI_API_KEY` or `TOGETHER_API_KEY`, §10]` | `TOGETHERAI_BASE_URL` (`https://api.together.ai/v1`) | registry id is `togetherai` (no dash), not `together`; `default_model = moonshotai/Kimi-K3`, `cheap_model = openai/gpt-oss-20b` |
  | `moonshotai` | `MOONSHOT_API_KEY` (§6.2, §14.1) | `MOONSHOTAI_BASE_URL` (`https://api.moonshot.ai/v1`) | `default_model = kimi-k3`, `cheap_model = kimi-k2.5` |
  | `kimi-for-coding` | `KIMI_API_KEY` (§6.2, §14.1 — meaning changed at the flag day, see Step 5) | `KIMI_FOR_CODING_BASE_URL` (`https://api.kimi.com/coding/v1`) | anthropic protocol, surface `anthropic`; sends `Headers["User-Agent"] = "claude-cli/2.1.177 (external, cli)"`; `default_model = k3`, `cheap_model = kimi-for-coding` (yes, that's the overlay's literal cheap-model id — quote it as-is) |
  | `minimax` | `[UNCONFIRMED]` | `MINIMAX_BASE_URL` (`https://api.minimax.io/anthropic/v1`) | anthropic protocol, surface `anthropic`; `default_model = MiniMax-M3`, `cheap_model = MiniMax-M2.7` |
  | `zai-coding-plan` | `[UNCONFIRMED]` | `ZAI_CODING_PLAN_BASE_URL` (`https://api.z.ai/api/coding/paas/v4`) | `thinking_format = zai`; `default_model = glm-5.3`, `cheap_model = glm-5.3-flash` |
  | `google-vertex-anthropic` | none — `gcp-adc` (`GOOGLE_APPLICATION_CREDENTIALS` or ADC file) | n/a — host derived from `GOOGLE_VERTEX_LOCATION` (§9.4) | also needs `GOOGLE_VERTEX_PROJECT` to exist at all; surface `anthropic`, family `claude`; `default_model = claude-opus-5`, `cheap_model = claude-haiku-4-5@20251001` |
  | `google-vertex` | none — `gcp-adc` | n/a — same host derivation | also needs `GOOGLE_VERTEX_PROJECT`; surface `google`; `default_model = gemini-3.7-flash`, `cheap_model = gemini-2.5-flash-lite` |
  | `amazon-bedrock` | `AWS_BEARER_TOKEN_BEDROCK` (overlay pin, §6.2, §9.3) | n/a — host built from `AWS_REGION` | surface `anthropic`, family `claude`; `default_model = global.anthropic.claude-opus-5`, `cheap_model = global.anthropic.claude-haiku-4-5-20251001-v1:0` |
  | `azure` | none pinned — always needs an explicit `providers.toml` entry; the spec's own §9.2 example uses `api_key = "$AZURE_API_KEY"` | n/a — needs `AZURE_RESOURCE_NAME` | no curated `default_model` (deployment names are per-tenant, §5.1) — this instance is never usable from environment alone |
  | `ollama` | none required; `auth = optional-bearer`, `OLLAMA_API_KEY` optional | `OLLAMA_HOST` (default `localhost`, normalized by the `ollama-host` rule) or `OLLAMA_BASE_URL` (wins when set) | no curated `default_model` — see Step 3 for the "never the default" rule; provider-level `context_window = 131072` |

  Add a short note after the table: other overlay-defined, non-implicit providers exist and are usable via an explicit `providers.toml` entry with `base = "<id>"`: `azure-cognitive-services` (the AI Services host form, overlay comment: "not implicit, verified live in plan 4"), `moonshotai-cn`, `zhipuai`, `zhipuai-coding-plan`, `minimax-cn`, `minimax-coding-plan`, `minimax-cn-coding-plan` — confirm these ids and their fields against the overlay file at write time, since they're outside `default_order`.

  `## Reference syntax and model lookup` (§7.1, §7.2)
  - `instance/model`, split on the **first** slash (model half may itself contain slashes: `groq/openai/gpt-oss-120b`, `openrouter/anthropic/claude-opus-5`); bare model id resolves against the default instance; no suffix handling (`claude-sonnet-4-5[1m]` is an ordinary alias row, not parsed suffix magic); dated rows use their catalog spelling (`vertex/claude-sonnet-4-5@20250929`); id comparison is case-sensitive.
  - The 6-step lookup order inside the merged provider record, first hit wins, recorded in `Provenance["model"]`: (1) exact id in the instance's own `models`; (2) exact id in the provider's merged `models`; (3) cloud region prefix stripped (`us.`, `eu.`, `apac.`, `au.`, `jp.`, `global.`); (4) dated family suffix removed (`-YYYYMMDD`, `-YYYYMMDD-v<N>`, `-YYYYMMDD-v<N>:<M>`, `@YYYYMMDD`); (5) live listing (provider-level caps only); (6) unknown — synthesized (next section). No substring or longest-prefix matching anywhere.

  `### Unknown models` (§7.3)
  - A model id matching nothing is still resolvable: synthesized from provider-level caps, matching glob rows, the provider's Surface and Family; `Warnings` carries `model not in catalog`; wire id is the reference verbatim; context window unset (agent treats as "unknown," no compaction budget until live/user data supplies one); anthropic protocol's `max_tokens` falls back to 32000. This is how a model released this morning works before the cache refreshes. **Exception:** the `oauth-openai-codex` transport enforces a model allowlist — an unknown id there is a resolve error, not a synthesized row.

  `## Reasoning and thinking dialects` (§8.4 — outside this task's primary assigned reading list; read it before writing this section, do not reconstruct it from memory of today's doc)
  - The spec states plainly that the wire dialect table is "today's `applyThinkingFormat`... kept verbatim" — port forward the existing `docs/llm-providers.md` "`thinking_format`: exact wire JSON per dialect" table (today's lines 338-379) almost as-is. All nine dialects survive: `openai` (default), `openrouter`, `zai`, `deepseek`, `together`, `qwen`, `qwen-chat-template`, `chat-template`, `string-thinking`.
  - What changed: `thinking_format` is now a `Caps` field set in `providers.toml`/the overlay (provider- or model-level TOML key), not a `[instances.X.compat]` table entry. The old per-format `supports_reasoning_effort` boolean gate is gone; whether `reasoning_effort`/the effort value accompanies the dialect is now derived from *effort-capable* (`effort ∈ ReasoningControls`, itself derived by §7.4) rather than a separately configured flag.
  - `ThinkingShape` (anthropic protocol) has three values — `adaptive` (`thinking: {type: adaptive}` + `display`, plus `output_config.effort` when the caller set one), `budget` (`thinking: {type: enabled, budget_tokens}`), `budget+effort` (both — Opus 4.5's hybrid shape, and Kimi K3 per the `k3*` glob row in the overlay). Unset shape sends no thinking object.
  - `none` (the effort value) sends nothing on every protocol, every dialect — nothing ever forces thinking off on the wire.
  - Replay: `ReasoningField` (`reasoning_content | reasoning | reasoning_text` as a string field, or `reasoning_details` as OpenRouter's array form) — the value comes from models.dev's `interleaved.field` when present, else the field the text arrived on, else `reasoning_content`. A `thinking_levels` per-model map is gone: a wire-spelled `effort_values` ladder under the existing clamp behavior reproduces it (delete the old "`thinking_levels` semantics" section, today's lines 314-337, entirely — there is no replacement table to port, `effort_values` in a model row is the whole story).

  `## providers.toml` (§10 — schema half; the store/launch half goes in Step 2)
  - Paste the spec's §10 example **verbatim**, unmodified:
    ```toml
    default = "groq"

    [providers.groq]                        # name matches a registry id → inherits it
    api_key  = "$GROQ_API_KEY"              # optional; registry says GROQ_API_KEY already
    protocol = "openai-responses"           # override the registry default (openai-chat)

    [providers.work]                        # name differs → say what it is based on
    base     = "openai"
    base_url = "https://gw.example.com/v1"
    protocol = "openai-chat"
    surface  = "generic"                     # the gateway serves non-OpenAI models
    headers  = { "X-Portkey-Provider" = "openai" }
    credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }   # required: a gateway never inherits OpenAI's key
    [providers.work.fields]
    stream_options = false                   # this gateway rejects stream_options
    [providers.work.models."glm-5.2-nvfp4"]
    context_window    = 1048576
    max_output_tokens = 131072
    effort_values     = ["high", "max"]      # implies the effort control (§7.4)
    thinking_format   = "zai"

    [providers.local]
    base     = "openai-compatible"           # protocol-only pseudo-provider
    base_url = "http://localhost:8080/v1"
    auth     = "none"

    [providers.bedrock]
    base = "amazon-bedrock"
    [providers.bedrock.vars]
    AWS_REGION = "us-east-1"

    [providers.vertex]
    base = "google-vertex-anthropic"
    [providers.vertex.vars]
    GOOGLE_VERTEX_PROJECT  = "my-project"
    GOOGLE_VERTEX_LOCATION = "global"
    ```
  - Note this same example already covers the old doc's "Worked example: lunaroute GLM gateway" use case (a gateway instance with a per-model `glm-5.2-nvfp4` override) — do not invent a second worked example; point at `[providers.work]` above.
  - Key mapping, one line: `base`, `inherit_models`, `api_key`, `api_key_env`, `headers`, `credential_headers`, `surface`, `family`, `default_model`, `cheap_model` → `Provider`; `transport`, `base_url`, `host_rule`, `auth`, `auth_header`, `endpoint`, `stream_endpoint`, `models_endpoint`, `count_tokens_endpoint`, `vars`, `vars_env`, `body` → `Provider.Transport`; `protocol` → `Provider.Protocol`; every `Caps` field by its snake_case name at the instance level → `Provider.Caps`, inside `[providers.X.models."<id or glob>"]` → `Model.Caps` (plus `alias_of`, `wire_id`, `family`, `protocol`, `surface`, `headers`, transport keys there too); a top-level `[models."<glob>"]` table is accepted in both the overlay and `providers.toml`.
  - Load rules (bullet each, from §10's "Rules, enforced at load" list): names lowercase/no-slash/unique; `base` must name a registry id; `alias_of` must name an existing non-alias row; unknown key anywhere is a load error naming it (`toml.MetaData.Undecoded()` — no silent ignore of a leftover `thinking_levels` or `compat`); `protocol` must be registered, `surface` one of the four values; `auth ∈ bearer | optional-bearer | header | none | gcp-adc | oauth-openai-codex`; `fields` keys must be in the resolved protocol's prunable set (typo guard); `thinking_format`/`thinking_shape`/`max_tokens_field`/`cache_control`/`reasoning_field`/`host_rule`/`image_detail` validated against their vocabularies; `reasoning_controls` entries must be `effort`/`budget_tokens`/`toggle`; `effort_values` entries non-empty, `"off"` rejected; `$ENV`/`${ENV}`/`$$` expansion in `api_key`/`credential_headers`/`vars` happens at resolve time (one instance's missing variable never blocks another) — **this is a behavior change from today**: an unset variable in `api_key` used to be a hard load-time error; now it yields an empty `Credential` with a `Warnings: no credential (<NAME> unset)` entry and the error is deferred to the first request (§5.2) — call this out explicitly since it inverts the old doc's claim ("never a silently-empty key"); in `headers` an unset variable **drops the header** (today it's an error — another explicit behavior change) and an empty string removes an inherited header of that name; credential inheritance stops at the endpoint — an instance with a literal `base_url` different from its base's does not inherit the base's `APIKeyEnv`, so a gateway never receives a vendor key by accident; hub rewrites go through the registry's config writer and never persist a credential the user didn't author; when both `auth = bearer` and `credential_headers.Authorization` are set, the header wins.
  - One line: `type`, `api_style`, `quirks`, `[instances.*]`, `compat` are gone; a file using any of them fails to load with a message pointing at the spec (cross-reference Step 5's flag-day section).

  `### Credential resolution order` (§10, last paragraph — quote the order exactly)
  1. the instance's own `api_key` (literal or `$VAR`)
  2. `credential_headers.Authorization` (also suppresses any bearer)
  3. the credentials-store file entry under the instance name
  4. environment: the instance's resolved `APIKeyEnv`, then `<NAME>_API_KEY` under the uppercase rule **only for instance names that are not registry ids** (so `[providers.anthropic] base_url = gateway` cannot pick up `ANTHROPIC_API_KEY` through the name layer)
  5. `oauth-openai-codex` and `gcp-adc` ignore all of the above and use their own record.

  `## Cloud transports: Azure, Bedrock, Vertex` (§9.1–§9.4, condensed to what makes each an instance)
  - **Azure** (§9.2): `base_url = https://{AZURE_RESOURCE_NAME}.openai.azure.com/openai/v1`; `auth = header`, `auth_header = api-key` (Entra bearer tokens work through `auth = bearer` with the token in `api_key`); no `api-version` param on v1; `model` in the body is the **deployment name**; a deployment row uses `alias_of` to pull catalog facts (and, when the row sets no protocol/transport of its own, the target's protocol and endpoint too) — paste the §9.2 azure example:
    ```toml
    [providers.azure]
    api_key = "$AZURE_API_KEY"
    [providers.azure.vars]
    AZURE_RESOURCE_NAME = "contoso-prod"
    [providers.azure.models."gpt55-prod"]
    alias_of = "gpt-5.5"          # facts from the catalog row; wire id stays gpt55-prod; Responses endpoint
    [providers.azure.models."claude-prod"]
    alias_of = "claude-opus-4-5"  # facts, the anthropic protocol, the Foundry /anthropic/v1 endpoint, and the Opus 4.5 glob pin follow the target
    ```
  - **Bedrock** (§9.3): `amazon-bedrock` uses Anthropic's Messages API on `bedrock-mantle` (`https://bedrock-mantle.{AWS_REGION}.api.aws/anthropic/v1`), bearer token via `x-api-key`; global/regional routing is expressed in the model id (`global.`, `us.`, `eu.`, `jp.`, `au.` inference-profile ids), not the host; nine Mantle OpenAI-shaped rows (gpt-oss, gpt-5.x, grok) exist via a separate bearer-auth preset; token counting is estimate-only (exact counting tracked as issue #565 — cite that, don't invent a different number).
  - **Vertex** (§9.4): host derived from location (`global` → `aiplatform.googleapis.com`; `us`/`eu` → the `.rep.` regional host; anything else → `{loc}-aiplatform.googleapis.com`); `auth = gcp-adc`; `google-vertex-anthropic` uses the `vertex-anthropic` transport preset (`:rawPredict`/`:streamRawPredict`, `body.anthropic_version = "vertex-2023-10-16"`); `google-vertex` uses `vertex-gemini`; a non-`global`/`us`/`eu` region paired with a model newer than Sonnet 4.6 gets a `Warnings` entry.
  - One line: none of these three needs request signing or non-SSE framing — that's why they fit the same transport model as everything else.

  `## The Codex transport` (§9.5, condensed to user-facing facts — not the internal `RequestPreparer`/header mechanics)
  - Instance `openai-codex`; `base = "openai"`, OAuth-only credential; OAuth record at `auth/openai-codex.json` under the state root (today's default record is `auth/openai.json`, written for instance `openai` — that's the flag-day break, see Step 5).
  - `evener openai login`, `status`, `logout` all default `--instance` to `openai-codex` now (today it defaults to `openai`).
  - A stray record — `auth/<name>.json` where `<name>` isn't on the Codex transport (or doesn't exist) — produces a one-line startup notice naming the file, remedied with `evener openai logout --instance <name>` or deleting it by hand.
  - `openai/…` still means the platform API unless the user explicitly writes `[providers.openai] base = "openai-codex"`.
  - The backend enforces a model allowlist (unknown id → resolve error, not a synthesized row, per §7.3's exception).

  `## The fields denylist and evener models inspect` (§10 rules + §11.1)
  - `Fields map[string]bool` in `Caps`: "send this optional wire field or not," keyed by JSON path, merged key-wise across layers; every key must be in the row's resolved-protocol prunable set — an unknown key is a load-time typo-guard error, not a silent no-op.
  - `evener models inspect <ref>`: the full `Resolved` record with provenance per field, the pruned-field list the protocol would apply, and the request skeleton (endpoint, auth scheme, headers with secrets masked); works with no credential configured (§5.2).

  `## Commands: evener models and evener providers` (§11.1, §11.2 — this H2 goes beyond the task's explicit minimum list; add it because these are brand-new user-facing commands with no current documentation anywhere, and leaving them out would leave a real gap)
  - `evener models list [--provider X] [--all]` — resolved rows with protocol, surface, context, output cap, cost, effort ladder, warnings; hidden providers/rows and rows without `tool_call` need `--all`.
  - `evener models inspect <ref>` — see above.
  - `evener models refresh [--force]` — fetch models.dev into the cache now, print the diff.
  - `evener providers list [--check]` — instances (explicit and implicit), base, protocol, endpoint, credential source, live reachability with `--check`.
  - `evener providers probe <instance> [--write]` — `GET` the models endpoint when supported, then a minimal request against `/responses` and `/chat/completions` (OpenAI protocols only), reporting which succeed; `--write` records the working protocol into `providers.toml`; discovered models are printed, never written; the runtime never probes on its own.
  - `evener providers add <name> --base X [--base-url …] [--protocol …] [--var K=V] [--api-key-env NAME] [--credential-header K=V] [--surface S]` — writes the entry, then runs `probe --write` unless `--no-probe`; when no credential would resolve, it still writes the entry, skips the probe, and prints what to set; secrets never go on the command line (`--credential-header` must contain a `$VAR` reference, never a literal secret).

  `## Errors` (§12, condensed to the user-facing hint table — skip the internal evaluation-order prose about 413-vs-code-vs-status ordering, keep the hints)
  - Table: **Signal → Kind → what the user sees**. `KindContextLength` (413 in any form, `context_length_exceeded`/`request_too_large` codes, matching 400 messages, Anthropic's new "prompt is too long") — non-retryable, message verbatim. `KindQuotaExceeded` (`usage_limit_reached`, `insufficient_quota`, Kimi's quota 403, the 429 "usage limit" phrase) — non-retryable, carries reset time. `KindRateLimit` (`rate_limit_exceeded` on 429, other 429) — retryable, honors `retry-after`/`x-ratelimit-reset-*`. `KindInvalidRequest` from an unrecognized/unsupported parameter — the hint names the fix: if the bad param is the max-tokens field spelling, `Hint: set max_tokens_field = "<the other spelling>"`; if it's a field the row prunes, `Hint: run evener models inspect <ref> and set fields.<name> = false`; otherwise a generic hint: `Hint: run evener models inspect <ref>; this endpoint rejected a field the registry sends — compare the pruned-field list against the provider's documentation`.
  - One line: the provider's message is always included verbatim alongside the hint.

  **Old sections — mark each explicitly as DELETE, PORT (survives, reframed), or KEEP (orthogonal, do not touch):**

  DELETE entirely (behavior-tag/type-system machinery has no replacement, it's just gone):
  - `## The mental model to keep` (today's lines 11-41) — the name-vs-`BehaviorTag()` split; there is only one identity now, the instance name.
  - `## The behavior tag (internal/providerconfig)` (91-109).
  - The `[instances.work] type = "openai" api_style = "responses" quirks = "..."` example inside `## Config-driven instances` (111-188) — replaced by the `providers.toml` schema in the new `## providers.toml` section above.
  - `## OpenAI-compatible compat & per-model config` and its four sub-sections `### Overlay precedence`, `### [instances.X.compat] / [instances.X.models."<id>".compat] fields`, `### Prompt caching through gateways`, `### [instances.X.models."<id>"] fields` (190-313) — the three-layer quirks-preset/instance-compat/per-model-compat overlay is replaced by `Caps` merging across the five registry layers (§4.1, §5).
  - `### thinking_levels semantics` (314-337) — no replacement table; `effort_values` on a model row is the whole story now (see the Reasoning section above).
  - `### Stock catalog defaults for z.ai GLM & DeepSeek v4 (zero config)` (380-420) — this is the doc's description of `llm/data/evener_model_catalog_overrides.json` (the "Evener catalog overrides") layered on `llm/data/litellm_model_catalog.json` (the LiteLLM-vendored catalog the spec's own header names as replaced). Both files are gone; the equivalent facts now live in `llm/data/models.dev.json.gz` (layer 1) plus the curated overlay (layer 3, §6.2). Do not port the per-model table (`glm-4.5-air`, `deepseek-v4-flash`, …) forward — those facts come from models.dev now, and the doc shouldn't hardcode a copy that will drift.
  - `## Provider profiles (agent/profile.go)` (541-590) — the `BehaviorTag()`-per-constructor table, `WithProviderID`, `ProviderOptions` keyed by tag. Replaced by `Resolved` (§3, §4.4 — §4.4 is outside this task's assigned reading; don't invent its field list, just point at "`Resolved`, produced by `Resolve`" per §3's one-line description already quoted above).
  - `## What keys on what (the map to consult before touching identity)` (777-806) — entirely about the name/tag split; no replacement, there's one identity.
  - `## Phase 1a, 1b & 1c done / Phase 2 next` (807-836) — obsolete status tracking; delete outright, do not replace with "registry phase" status prose (that belongs in the spec's own §14, not in user docs).

  PORT forward (the underlying fact survives; reframe the language, don't reinvent the content):
  - `### thinking_format: exact wire JSON per dialect` (338-379) and `### Reasoning replay: same field it arrived on` (421-432) — see the new `## Reasoning and thinking dialects` section above; confirmed via §8.4 to survive almost verbatim.
  - `### $ENV / ${ENV} / $$ in api_key` (433-456) — the substitution rules are unchanged; update only the failure-mode sentence per the load-rules bullet above (load-time hard error → deferred warning + first-request error).
  - `### Instance request headers (all types)` (457-507) — `Headers`/`CredentialHeaders` still exist on `Provider` and `Model` (§4); keep only the confirmed §10 rules (unset `$VAR` in `headers` drops the header, empty string removes an inherited one) — do not port forward the old `MergeHeaders`/precedence prose unless it's re-verified against the implemented code, since this task's reading didn't cover a registry-era equivalent.
  - `### Worked example: lunaroute GLM gateway` (508-540) — superseded by the `[providers.work]` block already in the new `providers.toml` example; don't write a second one.
  - `## Adapters and the registry (llm/)` and `### Wire protocols (they differ — this matters)` (617-684) — rewrite the wire-protocols table using the confirmed §6.1 protocol-default-endpoint facts: `openai-chat` → `/chat/completions`; `openai-responses` → `/responses`; `anthropic` → `/messages`; `google` → `/models/{model}:generateContent`. Drop the `BehaviorTag()`/`nameToTag` identity-stamping paragraphs (tag is gone); keep the "Kimi coding plan — coding-agent User-Agent" fact but fold it into the implicit-provider table's `kimi-for-coding` row (already there above) instead of a standalone callout; keep the "Strict tool schemas" fact but fold it into the OpenAI row's notes (`StrictTools = true` per the overlay) instead of a standalone section.
  - `## Reasoning effort` (734-776) — the wire-mechanics half (provider mapping, per-model clamping) is now covered by `## Reasoning and thinking dialects` above. The session-level half ("Setting it": `--reasoning-effort`, `EVENER_REASONING_EFFORT`, the `/effort` command, `thread/reasoning-effort/set`, broadcast events, the transcript-marker behavior) is agent/session machinery **orthogonal to the registry** — keep it, lightly updated: the two example models it currently names (`opus-4-6`/`sonnet-4-6` for `output_config.effort`, `opus-4-5`/`kimi-for-coding` for `thinking.budget_tokens`) map onto `ThinkingShape` values `adaptive` and `budget` respectively, confirmed above; update the wording, don't delete the section.
  - `## Adding or changing a provider` (837-846) — rewrite as: a new implicit vendor with an existing protocol = a new `[providers.<id>]` block in `llm/registry/data/providers_overlay.toml` with `implicit = true`, a `base_url` template + `vars`/`vars_env`, `default_model`/`cheap_model`, any `Caps` corrections (models.dev supplies the rest); a new custom/gateway instance for one user = `evener providers add` (§11.2) or a hand-authored `providers.toml` entry (§10); a new wire protocol = a new Go package implementing the §8.1 interfaces (out of this doc's depth, point at the spec); "touching identity/routing" no longer needs the name/tag-split warning, since there's only one identity.

  KEEP unchanged (genuinely orthogonal to the provider registry — do not delete these just because the file is being "rewritten"):
  - `## Request wall-clock ceiling` (69-90) — documents `llm.AdapterTimeout`, nothing to do with providers/registry.
  - `## Switching providers happens at the session, not the profile` (591-616) — the registry spec doesn't specify agent-layer session-switching mechanics (`decidePrefixAction`, meta-provider prefix handling) at all; **do not rewrite this section from the registry spec alone** — verify it against the actually-implemented `agent/provider.Profile` wrapper and session code once the cut-over (spec §14 step 3) lands, since the spec explicitly says `Profile` continues to wrap `Resolved`.
  - `### Tool choice: evener never forces it` (686-733) — a regression-guarded agent-layer design decision, unrelated to wire protocols.

  Add a `## See also` pointing to `docs/llm-provider-config-and-launch.md`, `docs/ollama.md`, and `docs/superpowers/specs/2026-08-28-provider-registry-design.md`; drop the links to the two 2026-05-29 phase-design specs (they document deleted machinery); leave the `docs/audit-logs/` historical-audits bullet alone (unrelated to this task).

- [ ] **Step 2: docs/llm-provider-config-and-launch.md** — Full rewrite of the credentials/OAuth/launch half (spec §5 layer-4 note, §9.5, §10 store half, §11.3, §14.1 resume/instance-name angle, §15). Drop the "Phase 1c all-config-driven" dating from the intro; keep the "hub orchestrates, separate evener processes do the work" framing and the ASCII diagram — that part is unaffected by this redesign.

  **New outline, with the facts to write under each heading:**

  `## Overview`
  - The stores table, updated: **Credentials store** (`<config-root>/credentials.toml`, chmod 600, TOML, keyed by **instance name** now rather than provider type — call this out explicitly, it's a real reframing even though the file format looks the same); **OpenAI OAuth record** (`<state-root>/auth/<instance>.json`, chmod 600, JSON — default instance is now `openai-codex`, not `openai`); **Providers config** (`<config-root>/providers.toml`, new schema per Step 1); **Process environment** (fallback / base URLs / tuning, unchanged role).
  - Keep the "hub process never runs a model" paragraph (spawns `evener launch-check` to validate/list, `evener serve` to run a session) verbatim in spirit.

  `## Credentials store`
  - The example TOML (still `schema = 1`, `[providers.anthropic] api_key = "sk-ant-..."`, etc.) — still accurate syntactically, since implicit instance ids double as section names; add one sentence that a section name can also be a **custom** instance name from `providers.toml`, not just a registry id.
  - Resolution mechanics in registry terms: the store's file entry under the instance name is step 3 of the credential resolution order (Step 1 has the authoritative full order — link to it, don't restate all five steps here); `ollama`, `none`, `optional-bearer`, `gcp-adc`, and `oauth-openai-codex` instances need no credentials-store entry at all.
  - Keep the guarantee, restated for the new writer: the hub and `evener providers add|probe --write` write `providers.toml` through the registry's config writer, which writes exactly the entries it is given — a resolved credential is never persisted, only what the user authored (`api_key = "$VAR"` literals and `credential_headers` references).
  - Delete: the `envvars.Providers()` / `envvars.APIKeyVars` / `envvars.AuthModes` / "fixed provider set defined in code" paragraph and the `BehaviorTag` vs `CredentialTag` distinction (`### Which key a lookup uses`, today's lines 133-193) — there is no separate credential tag. State the new division of labour plainly: the store holds only its file layer, keyed by instance name (Task 11 of this plan); every environment lookup — the provider's `api_key_env` variables and the `<NAME>_API_KEY` rule for custom names — is the registry's, in the order Step 1 documents. (Spec §10's "(id → APIKeyEnv) table" is satisfied by the registry performing those lookups itself; the store never sees the table.)

  `## Environment-variable reference`
  - Two tables, **API keys** and **Base URLs**, containing only variables that exist after the flag day. This necessarily duplicates most of `docs/developing-evener/environment.md`'s "Provider Configuration" table (Step 4) — that's consistent with how these two docs already duplicate this content today, but flag it explicitly in a one-line editorial note so the two tables get kept in sync on future changes, rather than silently drifting.
  - Populate from the same confirmed/unconfirmed variable list built for Step 4 below — do not re-derive it independently, copy it, to avoid the two tables disagreeing.

  `## OpenAI OAuth and the Codex transport`
  - Reframe `### The OAuth record`: per-instance file (`auth/<instance>.json`); the *default* instance for the CLI (`evener openai login/status/logout`, no `--instance` flag) is now `openai-codex`, not `openai`; a record's validation no longer special-cases "openai-ness" by content, only by which transport the named instance is on.
  - Reframe `### Precedence`: this used to be "stored OAuth record > `OPENAI_API_KEY` env > (hub only) credentials.toml file key" **within one instance**. That framing is gone. `openai` (API key only) and `openai-codex` (OAuth only) are two separate instances that never share a credential. What used to feel like "OAuth wins" is now `openai-codex` simply ranking before `openai` in `default_order` (§5.1) — so a fresh sign-in becomes the default instance by ranking, not by precedence inside one instance's credential resolution.
  - `### How a user signs in` — keep the device-code-flow / paste-back-fallback mechanics and the CLI flow description verbatim (OAuth transport plumbing, unaffected by the registry); update only the default `--instance` value (`openai-codex`) and the gate condition (today: "behavior tag is `openai`"; now: "`Transport.Auth == oauth-openai-codex`", per §9.5's last bullet).
  - Add: the stray-record notice (`auth/<name>.json` for an instance not on the Codex transport) and its remedy (`evener openai logout --instance <name>`), from §9.5 and §14.1.

  `## Hub launch / spawn process model`
  - Keep the ASCII diagram's shape but correct: the hub **no longer materializes `providers.toml` at startup** — it passes `EVENER_PROVIDERS_CONFIG` to children only when a file already exists (§5.1: "the hub no longer materializes `providers.toml` at startup and passes nothing to children beyond `EVENER_PROVIDERS_CONFIG` when a file exists"). Delete the `MaterializeProvidersConfig` box/description from the diagram's prose.
  - Explain the `EVENER_PROVIDERS_CONFIG` **tri-state** precisely (§10): unset → default path; set to a path → that file; set and **empty** (`export EVENER_PROVIDERS_CONFIG=`) → "no user layer" (`os.LookupEnv`, not `Getenv`). A hub whose file failed to load sets it empty in every child environment, overriding any inherited value.
  - Introduce `EVENER_CREDENTIALS_CONFIG` (new variable): names `credentials.toml` explicitly; when unset, the store is the sibling of the providers path, as before.
  - Explain implicit instances are computed **identically and independently by every process** from the same inputs (environment + credentials store) — the hub no longer injects the launched instance's key into the child the way it used to (that mechanism, and the "roster," are deleted per §10).
  - Add the spawn credential gate rule from §11.3, keyed on `Transport.Auth`: `none`/`optional-bearer` need nothing; `oauth-openai-codex` is satisfied by the instance's OAuth record; `gcp-adc` by the ADC variable or file; everything else needs a resolved key or credential header.
  - Add: a `providers.toml` load error is a hub diagnostic, not a fatal hub crash — the hub starts with implicit instances only, launches sessions against that implicit set, and refuses every instance write until the file is fixed by hand (§10, §11.3).
  - Keep `### Spawning evener launch-check` and `### Spawning evener serve` mostly as-is (still `cmdutil.LoadClient`-driven, still config-aware) — update only the "config-aware" description to say it resolves via the registry's `Resolve`, not `ResolveProfileFromConfig`/`BehaviorTagOf`.

  `## Resume & persistence`
  - Mostly unchanged conceptually: session metadata still persists the instance name in `ProfileID` (the field keeps its name; it now holds the registry instance name); a resume whose stored name no longer exists gets the same "unknown instance" error, now explicitly listing available instances (confirmed at §14.1: "A saved session, `launch.toml`, `EVENER_MODEL`, or plugin `model:` declaration that names an old instance fails with the unknown instance error naming the available instances").
  - This is a natural place to forward-reference Step 5's flag-day section: a session saved before the upgrade that named `kimi`, `glm`, `kimi-anthropic`, or `openrouter-anthropic` will hit exactly this error on resume.

  `## Web / TUI provider surfaces`
  - Replace `### Two web settings screens (both render the same data)` — the duplicate type-based Providers/Credentials screens are gone; describe the one instance-aware CRUD screen instead (§11.3): hub instance CRUD (`cmd/evener-hub/app_instances.go`) calls the same functions as the CLI; editing an implicit instance or setting it default writes a **shadowing** entry carrying only the fields the user changed (never a literal `base_url` the form merely displayed); removing a pure-implicit instance is refused.
  - Add: the credentials pane lists **every curated implicit provider**, whether or not it currently has a credential (since §5.2 resolves them regardless) — this is where a fresh install signs in to `openai-codex` or enters its first key, not a screen that only shows what's already configured.
  - Add: the RPCs feeding this (`evener/auth/*`) now return one status per curated implicit provider plus every explicit instance, not the old `envvars` roster.
  - Keep `### Model strings and display` mostly as-is (the `abbreviateModel` hardcoded-prefix-stripping behavior in the TUI and web) but flag it: **the registry spec does not address this display code at all** — verify the actual prefix list against the implemented frontend/TUI code before asserting it still strips exactly `anthropic/`, `openai/`, `google/`, `openrouter/`, `openai-compatible/`, since some of those instance ids no longer exist under those names (see Step 5).

  Add a `## See also` mirroring Step 1's, plus a link back to `docs/llm-providers.md`.

- [ ] **Step 3: docs/ollama.md** — Rewrite around the `ollama` overlay row (spec §6.2, §9.1, §5.1, §15's ollama-default decision).

  **New outline, with the facts to write under each heading:**

  `# Using Evener with Ollama` (intro)
  - Replace "thin wrapper around Evener's `openai-compatible` adapter" with the registry framing: the `ollama` provider is `protocol = "openai-chat"`, `auth = "optional-bearer"`, `host_rule = "ollama-host"` (overlay). Functionally the same wire behavior, different plumbing description.

  `## Quick start`
  - Keep steps 1-4 (install, start daemon, pull a tool-calling model, run) unchanged — none of this is registry-specific.
  - Rewrite the "never becomes Evener's silent default" paragraph precisely, per §5.1 and §15 (do not reuse the task-prompt's shorthand "only instance / default names it" uncritically — it's imprecise against the actual rule): `ollama` carries **no curated `default_model`** (unlike every other implicit provider except `azure`), so it's excluded from the automatic default-instance ranking unless the user adds one — either `[providers.ollama] default_model = "..."` in `providers.toml` (which makes it "eligible like any other," ranked at its `default_order` position, which is last) or `default = "ollama"` explicitly. In practice that means `ollama` only becomes the live default when nothing else in the environment resolves to a credentialed, default-model-bearing instance, or when the user names it. **Being the only instance configured is not by itself sufficient** — if `ollama` is the sole instance and has no `default_model`, resolving a bare model id is a "no default model" error (§5.1's exact pattern: "azure has no default model; pass azure/<deployment> or set default"), not a silent fallback to `ollama`; the user still has to address it as `ollama/<model>` or add a `default_model`. Get this nuance right — it's an easy way to accidentally overstate the rule.

  `## How it works`
  - One paragraph replacing "openai-compatible adapter": `protocol = "openai-chat"` (the same Chat Completions protocol other implicit providers use), `auth = "optional-bearer"` (sends a bearer token when a key resolves, nothing otherwise), `host_rule = "ollama-host"` (the one normalizer described in the next section). Models, tool calls, multimodal images, and `/v1/models` listing all still flow through that protocol's normal code path — this sentence survives unchanged.

  `## Environment variables`
  - The table and the three variables (`OLLAMA_BASE_URL`, `OLLAMA_HOST`, `OLLAMA_API_KEY`) are **unchanged** — confirmed: the overlay's `host_rule = "ollama-host"` is explicitly "today's `envvars.ResolveOllamaBaseURL` + `NormalizeOllamaHost`... applied to the variable value before substitution" (§9.1), and the resolution order (`OLLAMA_BASE_URL` wins when set, else `OLLAMA_HOST` normalized: bare host → `http://host:11434/v1`, `localhost` → same, `::1` → bracketed, full URL kept, a URL already ending `/v1` preserved verbatim) matches today's doc exactly. Port this section forward with no substantive changes — just double check the `::1` case is currently documented (today's doc doesn't list it explicitly; add it, it's in §9.1).

  `## Examples`, `## Choosing a model` — unchanged, not registry-specific; port forward verbatim.

  `## Context length` — the one section that changes completely. Delete the embedded-catalog mechanism description (today's lines 122-157: "the embedded model catalog... `ollama/llama3.1` → 8192," the tag-stripping lookup, the "128K generic default," and the entire "Limitation: no override flag" discussion — that limitation no longer exists). Replace with:
  - Ollama's `/v1/models` still doesn't report `context_length`, so Evener still can't auto-detect a live model's real window from the API.
  - New resolution: every live-only model on `ollama` (or a pseudo-provider) budgets against a **provider-level default of `context_window = 131072`** (overlay; spec's own description: "today's 128K compat default... so compaction still fires for a live-only model whose listing reports no window").
  - The per-model LiteLLM-derived catalog (`8192` for `llama3.1`, tag-stripping, etc.) is gone entirely — there is no bundled per-model table anymore.
  - The override the old doc said didn't exist now does: pin the real window on a model row, using the spec's own example verbatim:
    ```toml
    [providers.ollama.models."llama3.1*"]
    context_window = 8192
    ```
  - This is a **flag-day narrowing**: anyone relying on the bundled `llama3.1` → 8192 default silently getting picked up needs to add this pin themselves now, or compaction won't fire until 131072 tokens instead of 8192 (cross-reference Step 5).

  `## Troubleshooting` — keep as-is, except update "Truncated long conversations" to point at the new pin mechanism (`[providers.ollama.models."<glob>"] context_window = ...`) instead of the old section's "See the Context length section above" pointing at a now-different explanation — same pointer, different destination content.

  `## See also` — keep both links, refresh the one-line descriptions to match Step 1/Step 2's new framing (registry-based, not adapter-based).

- [ ] **Step 4: README.md, docs/getting-started.md, docs/evener-hub.md, docs/developing-evener/environment.md** — per file, exact old text and exact replacement.

  **README.md**

  1. Lines 76-83, old text (quoted exactly):
     ```
     Install does not create provider credentials. Hosted/auth-required providers
     can be configured through the hub or TUI credentials UI, supported provider
     environment variables such as `OPENAI_API_KEY`, or OpenAI OAuth. Local/auth-none
     providers such as Ollama may not need credentials. The default credentials file
     is `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml`; when
     `EVENER_PROVIDERS_CONFIG` points to a custom `providers.toml`, the credentials
     file is beside it. See [docs/developing-evener/environment.md](docs/developing-evener/environment.md)
     for the complete environment variable reference.
     ```
     Replacement:
     ```
     Install does not create provider credentials. Hosted/auth-required providers
     can be configured through the hub or TUI credentials UI, supported provider
     environment variables such as `OPENAI_API_KEY`, or OpenAI OAuth (which signs
     in to the separate `openai-codex` instance, not `openai`). Local/auth-none
     providers such as Ollama may not need credentials. The default credentials file
     is `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml`; when
     `EVENER_PROVIDERS_CONFIG` points to a custom `providers.toml`, the credentials
     file is beside it unless `EVENER_CREDENTIALS_CONFIG` names a different one. See
     [docs/developing-evener/environment.md](docs/developing-evener/environment.md)
     for the complete environment variable reference.
     ```

  2. Line 232, old text (quoted exactly):
     ```
     Evener takes a provider-qualified model in one value: `--model <provider/model>`. Providers: `openai`, `anthropic`, `google`, `minimax`, `openrouter`, `openrouter-anthropic`, `kimi`, `glm`, `ollama`.
     ```
     Replacement:
     ```
     Evener takes a provider-qualified model in one value: `--model <provider/model>`. Every provider with a resolvable credential is usable with no config file: `anthropic`, `openai-codex`, `openai`, `google`, `groq`, `zai`, `deepseek`, `openrouter`, `xai`, `mistral`, `cerebras`, `togetherai`, `moonshotai`, `kimi-for-coding`, `minimax`, `zai-coding-plan`, `google-vertex-anthropic`, `google-vertex`, `amazon-bedrock`, `azure`, `ollama`. A `providers.toml` entry adds anything else. See [docs/llm-providers.md](docs/llm-providers.md).
     ```
     (This is `default_order` from the overlay, verbatim, 21 entries — do not abbreviate it, the old sentence enumerated every instance by name and this replacement keeps that property.)

  3. Lines 238-253 (the "Environment variables" table under `### Environment variables`), old text (quoted exactly):
     ```
     | Variable | Description |
     |---|---|
     | `EVENER_MODEL` | Default model as `provider/model` (used when `--model` is omitted) |
     | `EVENER_REASONING_EFFORT` | Default reasoning effort |
     | `EVENER_PROVIDERS_CONFIG` | Path to `providers.toml` |
     | `OPENAI_API_KEY` | OpenAI API key |
     | `ANTHROPIC_API_KEY` | Anthropic API key |
     | `GEMINI_API_KEY` | Google Gemini API key |
     | `OPENROUTER_API_KEY` | OpenRouter API key |
     | `OLLAMA_BASE_URL` | Ollama base URL (default `http://localhost:11434/v1`) |
     | `OLLAMA_HOST` | Ollama host (Ollama's canonical env var; used if `OLLAMA_BASE_URL` is unset) |
     | `OLLAMA_API_KEY` | Optional API key for authenticated Ollama proxies / Ollama Cloud |
     ```
     Replacement — only the `EVENER_PROVIDERS_CONFIG` row's description changes, and `OPENROUTER_API_KEY` gets an inline verify-me marker; every other row is confirmed unchanged, leave verbatim:
     ```
     | Variable | Description |
     |---|---|
     | `EVENER_MODEL` | Default model as `provider/model` (used when `--model` is omitted) |
     | `EVENER_REASONING_EFFORT` | Default reasoning effort |
     | `EVENER_PROVIDERS_CONFIG` | Path to `providers.toml`. Set and empty means "no user layer" — see [docs/llm-provider-config-and-launch.md](docs/llm-provider-config-and-launch.md) |
     | `OPENAI_API_KEY` | OpenAI API key |
     | `ANTHROPIC_API_KEY` | Anthropic API key |
     | `GEMINI_API_KEY` | Google Gemini API key |
     | `OPENROUTER_API_KEY` | OpenRouter API key |
     | `OLLAMA_BASE_URL` | Ollama base URL (default `http://localhost:11434/v1`) |
     | `OLLAMA_HOST` | Ollama host (Ollama's canonical env var; used if `OLLAMA_BASE_URL` is unset) |
     | `OLLAMA_API_KEY` | Optional API key for authenticated Ollama proxies / Ollama Cloud |
     ```
     Before finalizing, re-verify `OPENROUTER_API_KEY` is still correct (it's carried forward unconfirmed, per the sourcing rule at the top of this task) — if it turns out wrong, fix this row and the matching row in Step 2 and Step 4's environment.md table together.

  4. Lines 380-405 (the `llmcall` build/examples/env-fallback section): **no change**. Confirmed by direct read — the example command (`--provider openai --model gpt-5-mini-2025-08-07`) still names a valid instance, and `LLM_PROVIDER`/`EVENER_PROVIDER`/`LLM_MODEL`/`EVENER_MODEL` are `llmcall`'s own argument-resolution fallback, unrelated to the provider registry. Do not touch this range.

  **docs/getting-started.md**

  1. Lines 83-97, old text (quoted exactly):
     ```
     ## Add provider credentials

     Hosted LLM providers generally need a credential. Local/auth-none providers
     such as Ollama can run without one. For providers that need credentials, the
     web UI is the easiest place to add one: open
     `http://127.0.0.1:9180/credentials` and paste an API key. The page writes
     `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml` with owner-only
     permissions. If `EVENER_PROVIDERS_CONFIG` points to a custom `providers.toml`,
     Evener stores `credentials.toml` beside it.

     Two alternatives cover other workflows. Environment variables such as
     `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` work as a fallback when the
     credentials file has no entry for that provider. For OpenAI you can also sign
     in with OAuth from the credentials page instead of managing a key. For the
     full resolution order and provider-specific behavior, see
     [docs/llm-provider-config-and-launch.md](llm-provider-config-and-launch.md).
     ```
     Replacement:
     ```
     ## Add provider credentials

     Hosted LLM providers generally need a credential. Local/auth-none providers
     such as Ollama can run without one. For providers that need credentials, the
     web UI is the easiest place to add one: open
     `http://127.0.0.1:9180/credentials` and paste an API key. The page writes
     `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml` with owner-only
     permissions. If `EVENER_PROVIDERS_CONFIG` points to a custom `providers.toml`,
     Evener stores `credentials.toml` beside it, unless `EVENER_CREDENTIALS_CONFIG`
     names a different path.

     Two alternatives cover other workflows. Environment variables such as
     `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` work as a fallback when the
     credentials file has no entry for that instance. You can also sign in to
     OpenAI's ChatGPT/Codex subscription with OAuth from the credentials page —
     that authenticates the separate `openai-codex` instance, not `openai`, and
     `openai-codex` is preferred over `openai` when both are available. For the
     full resolution order and provider-specific behavior, see
     [docs/llm-provider-config-and-launch.md](llm-provider-config-and-launch.md).
     ```

  2. Lines 160-173 (the "Next steps" bullet list): **no change**. Confirmed by direct read — every bullet (including the `docs/llm-providers.md` one, "supported providers and models, including local models through Ollama") stays accurate under the new docs; it's a pointer, not a claim about internals.

  **docs/evener-hub.md**

  1. Lines 165-172 (the launch-config `Env map` bullet mentioning "OpenAI OAuth instead"): **no required change** — the sentence is generic enough ("a supported provider environment variable... or OpenAI OAuth instead") that it doesn't name anything stale. Optional polish only, not required: could tighten "OpenAI OAuth instead" to "OpenAI OAuth (the `openai-codex` instance) instead" for precision; leave it out if it doesn't fit the surrounding sentence's flow.

  2. Lines 228-277, old text (quoted exactly):
     ```
     ## Provider credentials

     > Architecture reference:
     > [`llm-providers.md`](llm-providers.md) (provider routing,
     > profiles, adapters) and
     > [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
     > (credentials, OAuth, and the hub launch/spawn model).

     The hub manages `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml`
     (chmod 600). The file's format is a small TOML document:

     ```toml
     schema = 1

     [providers.anthropic]
     api_key = "sk-ant-..."

     [providers.openai]
     api_key = "sk-..."

     [providers.openrouter]
     api_key = "..."
     ```

     The hub UI (`/credentials`) or TUI (`/credentials`) writes this file via
     the `evener/auth/apiKey/set` RPC. Process-env credentials (e.g.,
     `ANTHROPIC_API_KEY` exported in the shell) still work as a fallback when no
     file entry exists for the provider — matching the `hub.env` style for users
     who prefer external secret management.

     If `EVENER_PROVIDERS_CONFIG` points to a non-default `providers.toml`,
     `credentials.toml` is relocated beside that file. Otherwise it is beside the
     default providers config under the XDG config root. Keep both files private.

     ### OpenAI credential resolution

     OpenAI supports both an API key (stored in `credentials.toml` like any other
     provider, or via `OPENAI_API_KEY`) and OAuth (sign in via
     `evener/auth/login/start`; state stored in
     `${XDG_STATE_HOME:-$HOME/.local/state}/evener/auth/openai.json`). An explicit
     OAuth sign-in wins over the file key, which in turn shadows the environment
     variable.

     The two routes hit **different backends**: OAuth routes to the
     ChatGPT/Codex backend (`OPENAI_CHATGPT_BASE_URL`), while an API key routes to
     the standard OpenAI API backend (`OPENAI_BASE_URL`). They are not
     interchangeable credentials for one endpoint. See
     [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
     for the full resolution detail.
     ```
     Replacement:
     ```
     ## Provider credentials

     > Architecture reference:
     > [`llm-providers.md`](llm-providers.md) (the registry, layers, instances,
     > resolution) and
     > [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
     > (credentials, OAuth, and the hub launch/spawn model).

     The hub manages `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml`
     (chmod 600), keyed by **instance name**, not provider type. The file's
     format is a small TOML document:

     ```toml
     schema = 1

     [providers.anthropic]
     api_key = "sk-ant-..."

     [providers.openai]
     api_key = "sk-..."

     [providers.openrouter]
     api_key = "..."
     ```

     A section name matches either an implicit instance's id (`anthropic`,
     `openai`, `openrouter`, …) or a custom instance you defined in
     `providers.toml`.

     The hub UI (`/credentials`) or TUI (`/credentials`) writes this file via
     the `evener/auth/apiKey/set` RPC. Process-env credentials (e.g.,
     `ANTHROPIC_API_KEY` exported in the shell) still work as a fallback when no
     file entry exists for the instance — matching the `hub.env` style for users
     who prefer external secret management.

     If `EVENER_PROVIDERS_CONFIG` points to a non-default `providers.toml`,
     `credentials.toml` is relocated beside that file, unless
     `EVENER_CREDENTIALS_CONFIG` names a different path. Otherwise it is beside
     the default providers config under the XDG config root. Keep both files
     private.

     ### OpenAI credential resolution

     The platform API and the ChatGPT/Codex subscription are two separate
     **instances**, not one instance with two credential sources: `openai` (an
     API key, stored in `credentials.toml` like any other instance, or via
     `OPENAI_API_KEY`) and `openai-codex` (OAuth only — sign in via
     `evener/auth/login/start`; state stored per instance at
     `${XDG_STATE_HOME:-$HOME/.local/state}/evener/auth/openai-codex.json`).
     `openai-codex` precedes `openai` in the default-instance ranking, so a
     fresh sign-in becomes the default the same way a stored OAuth record used
     to win — but by ranking between two instances, not by a precedence check
     within one.

     The two instances hit **different backends**: `openai-codex` routes to the
     ChatGPT/Codex backend (`OPENAI_CODEX_BASE_URL`), while `openai` routes to
     the standard OpenAI API backend (`OPENAI_BASE_URL`). They are not
     interchangeable credentials for one endpoint, and signing in with
     `evener openai login` no longer touches the `openai` instance at all. See
     [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
     for the full resolution detail.
     ```

  **docs/developing-evener/environment.md**

  1. Line 18 (the `EVENER_PROVIDERS_CONFIG` row in the "Evener Commands" table), old text (quoted exactly):
     ```
     | `EVENER_PROVIDERS_CONFIG` | Path to `providers.toml`. |
     ```
     Replacement:
     ```
     | `EVENER_CREDENTIALS_CONFIG` | Path to `credentials.toml`. Unset means the sibling of the resolved providers-config path. |
     | `EVENER_PROVIDERS_CONFIG` | Path to `providers.toml`. Unset means the default path; set and empty (`EVENER_PROVIDERS_CONFIG=`) means no user layer at all. |
     ```
     (Two rows replace one — `EVENER_CREDENTIALS_CONFIG` is new and sorts alphabetically before `EVENER_HUB_ADDR` at line 11, i.e. immediately after `EVENER_ALLOWED_DECISIONS` at line 10 and before `EVENER_HUB_ADDR`; `EVENER_PROVIDERS_CONFIG`'s own row stays where it is at line 18, only its description changes.)

  2. Lines 39-68 (the entire "Provider Configuration" table), old text: quote the full table as read (today's lines 41-68, from `| Variable | Description |` through the `OPENROUTER_BASE_URL` row) — reproduce it exactly as it stands in the file today before replacing it, so the diff is legible.

     Replacement — the complete new table (every row sourced per the top-of-task sourcing rule; unconfirmed rows carry the bracket):
     ```
     | Variable | Description |
     |---|---|
     | `OPENAI_API_KEY` | OpenAI API key (`openai` instance). |
     | `OPENAI_BASE_URL` | OpenAI API-key backend base URL (default `https://api.openai.com/v1`). |
     | `OPENAI_CODEX_BASE_URL` | OpenAI ChatGPT/Codex backend base URL for the `openai-codex` instance (default `https://chatgpt.com/backend-api/codex`). Replaces `OPENAI_CHATGPT_BASE_URL`. |
     | `OPENAI_COMPATIBLE_API_KEY` | API key for the `openai-compatible` pseudo-provider instance. |
     | `OPENAI_COMPATIBLE_BASE_URL` | Base URL for the `openai-compatible` pseudo-provider instance; its presence is what makes that instance exist. |
     | `OPENAI_ORG_ID` | OpenAI org header (`OpenAI-Organization`) on the `openai` instance; dropped when unset. |
     | `OPENAI_PROJECT_ID` | OpenAI project header (`OpenAI-Project`) on the `openai` instance; dropped when unset. |
     | `ANTHROPIC_API_KEY` | Anthropic API key (`anthropic` instance). |
     | `ANTHROPIC_BASE_URL` | Anthropic API base URL override (default `https://api.anthropic.com/v1`). |
     | `ANTHROPIC_COMPATIBLE_API_KEY` | API key for the `anthropic-compatible` pseudo-provider instance. |
     | `ANTHROPIC_COMPATIBLE_BASE_URL` | Base URL for the `anthropic-compatible` pseudo-provider instance; its presence is what makes that instance exist. |
     | `GEMINI_API_KEY` | Google Gemini API key; checked before `GOOGLE_API_KEY` (`google` instance). |
     | `GOOGLE_API_KEY` | Google Gemini API key fallback. |
     | `GOOGLE_BASE_URL` | Google Gemini API base URL override (default `https://generativelanguage.googleapis.com/v1beta`). Replaces `GEMINI_BASE_URL`. |
     | `GOOGLE_COMPATIBLE_API_KEY` | API key for the `google-compatible` pseudo-provider instance. |
     | `GOOGLE_COMPATIBLE_BASE_URL` | Base URL for the `google-compatible` pseudo-provider instance; its presence is what makes that instance exist. |
     | `GOOGLE_VERTEX_PROJECT` | GCP project for the `google-vertex`/`google-vertex-anthropic` instances; required for either to exist. |
     | `GOOGLE_VERTEX_LOCATION` | GCP location for the `google-vertex`/`google-vertex-anthropic` instances; required for either to exist. |
     | `GOOGLE_APPLICATION_CREDENTIALS` | Path to a GCP service-account file for Application Default Credentials; the well-known ADC file also works without it. |
     | `ZHIPU_API_KEY` | z.ai/Zhipu API key (`zai` instance). Replaces `GLM_API_KEY`. |
     | `ZAI_BASE_URL` | z.ai base URL override (default `https://api.z.ai/api/paas/v4`). Replaces `GLM_BASE_URL`. |
     | `ZAI_CODING_PLAN_BASE_URL` | z.ai coding-plan base URL override (default `https://api.z.ai/api/coding/paas/v4`), for the `zai-coding-plan` instance. |
     | `MOONSHOT_API_KEY` | Moonshot's platform API key (`moonshotai` instance). New name for what `KIMI_API_KEY` used to mean. |
     | `MOONSHOTAI_BASE_URL` | Moonshot platform base URL override (default `https://api.moonshot.ai/v1`). |
     | `KIMI_API_KEY` | Kimi coding-plan API key (`kimi-for-coding` instance, Anthropic protocol). Meaning changed at the flag day — previously Moonshot's platform key. |
     | `KIMI_FOR_CODING_BASE_URL` | Kimi coding-plan base URL override (default `https://api.kimi.com/coding/v1`). Replaces `KIMI_CODING_BASE_URL`. |
     | `MINIMAX_API_KEY` | MiniMax API key. `[UNCONFIRMED against spec/overlay text — not pinned by the curated overlay; carried forward unchanged from today's doc on that basis, verify before publishing]` |
     | `MINIMAX_BASE_URL` | MiniMax API base URL override (default `https://api.minimax.io/anthropic/v1`). |
     | `GROQ_API_KEY` | Groq API key (`groq` instance; confirmed directly in spec §10's own example). |
     | `GROQ_BASE_URL` | Groq base URL override (default `https://api.groq.com/openai/v1`). |
     | `XAI_API_KEY` | xAI API key (`xai` instance). |
     | `XAI_BASE_URL` | xAI base URL override (default `https://api.x.ai/v1`). |
     | `CEREBRAS_API_KEY` | Cerebras API key. `[UNCONFIRMED — not pinned by the curated overlay, verify before publishing]` |
     | `CEREBRAS_BASE_URL` | Cerebras base URL override (default `https://api.cerebras.ai/v1`). |
     | `MISTRAL_API_KEY` | Mistral API key. `[UNCONFIRMED — not pinned by the curated overlay, verify before publishing]` |
     | `MISTRAL_BASE_URL` | Mistral base URL override (default `https://api.mistral.ai/v1`). |
     | `TOGETHERAI_API_KEY` | Together AI API key. `[UNCONFIRMED — the spec explicitly warns this is not confirmed to be `TOGETHERAI_API_KEY` or an alias `TOGETHER_API_KEY`; verify before publishing]` |
     | `TOGETHERAI_BASE_URL` | Together AI base URL override (default `https://api.together.ai/v1`; the registry id is `togetherai`). |
     | `DEEPSEEK_API_KEY` | DeepSeek API key. `[UNCONFIRMED — not pinned by the curated overlay, verify before publishing]` |
     | `DEEPSEEK_BASE_URL` | DeepSeek base URL override (default `https://api.deepseek.com`, no version segment — a documented models.dev exception). |
     | `OPENROUTER_API_KEY` | OpenRouter API key. `[UNCONFIRMED against spec/overlay text — carried forward unchanged from today's doc, verify before publishing]` |
     | `OPENROUTER_BASE_URL` | OpenRouter API base URL override (default `https://openrouter.ai/api/v1`). |
     | `AWS_BEARER_TOKEN_BEDROCK` | Bedrock bearer token (`amazon-bedrock` instance). |
     | `AWS_REGION` | AWS region for the `amazon-bedrock` instance; required for it to exist. |
     | `AZURE_RESOURCE_NAME` | Azure OpenAI/Foundry resource name; required for the `azure` instance to exist. |
     | `AZURE_API_KEY` | Example variable name from the spec's own `providers.toml` sample for an `azure` instance's `api_key`. Azure has no curated `api_key_env` and no curated default model, so a working `azure` instance always needs an explicit `providers.toml` entry — this variable name is a convention from the docs' own example, not an automatic default. |
     | `OLLAMA_API_KEY` | Optional API key for authenticated Ollama proxies or Ollama Cloud. Unchanged. |
     | `OLLAMA_BASE_URL` | Ollama OpenAI-compatible base URL; must include `/v1`. Unchanged. |
     | `OLLAMA_HOST` | Ollama canonical host; used when `OLLAMA_BASE_URL` is unset. Unchanged. |
     ```
     Rows removed outright (no longer read anywhere, confirmed at §14.1 — do not carry these forward even as a "removed" note inside the table, just don't include them): `OPENAI_CHATGPT_BASE_URL`, `OPENAI_COMPATIBLE_PROVIDER_QUIRKS`, `GEMINI_BASE_URL`, `GLM_API_KEY`, `GLM_BASE_URL`, `KIMI_BASE_URL`, `KIMI_CODING_API_KEY`, `KIMI_CODING_BASE_URL`.

- [ ] **Step 5: Flag-day section** — goes in `docs/llm-provider-config-and-launch.md`, as a new final `## Upgrading from the old schema` H2 (after "Web / TUI provider surfaces", before "See also"), so it reads as the doc's landing spot for "my config broke, what do I do." Cross-link to it from Step 1's `providers.toml` section (where the load-error is first mentioned) and from Step 3's Ollama context-length section. Content, bulleted, straight from spec §14.1 — quote every identifier exactly as the spec gives it:

  - **`providers.toml` fails to load.** An old-schema file (`[instances.*]`, `type`, `api_style`, `quirks`, `compat`) fails to load; the CLI exits with a pointer to `docs/superpowers/specs/2026-08-28-provider-registry-design.md`; the hub starts with implicit instances only, shows the error as a diagnostic, launches sessions against the implicit set, and refuses instance writes until the file is fixed. Fix it by hand: edit, delete, or move the file aside — nothing does this automatically. Most users need no file at all afterward: every implicit provider (Step 1's table) exists from its key alone, and `*_BASE_URL` variables cover proxies. Re-create a gateway or custom-named instance with `evener providers add … --api-key-env NAME` or `--credential-header K=V`; remember an instance with its own `base_url` never inherits the vendor key (today's `[instances.anthropic] base_url = …` shape did).
  - **Default instance ranking changed.** With more than one instance and no `default` set, the default now follows `default_order` (Step 1's table), then custom-named entries by sorted name — not today's alphabetical registration order or sorted-name-with-no-eligibility-skip rule. Concretely: `GEMINI_API_KEY` + `OPENAI_API_KEY` set together now defaults to `openai`, not `google`. Set `default` explicitly to keep the old pick.
  - **Instance names that are gone**, with their replacements:
    | Old name | Replacement |
    |---|---|
    | `kimi` | `moonshotai` |
    | `glm` | `zai` |
    | `kimi-anthropic` | `kimi-for-coding` |
    | `openrouter-anthropic` | the `orclaude` recipe (below) — there's no single renamed instance, it's a `providers.toml` entry now |
    | `openai-compatible` as a vendor/type name | still exists, but only as the protocol-only pseudo-provider instance, not a `type = "openai" api_style = "chat-completions"` recipe |

    The `orclaude` recipe, verbatim from the spec (the anthropic-protocol route to OpenRouter, for MiniMax's Anthropic-style tool calls):
    ```toml
    [providers.orclaude]
    base     = "openrouter"
    protocol = "anthropic"
    [providers.orclaude.models."minimax/*"]
    surface  = "anthropic"
    ```
    A saved session, `launch.toml`, `EVENER_MODEL`, or plugin `model:` declaration naming an old instance fails with the unknown-instance error, naming the instances that are actually available.
  - **Environment variables that changed meaning or disappeared** (this table intentionally overlaps Step 4's environment.md rewrite — keep the two in sync, don't let them diverge on a future edit):
    - `KIMI_API_KEY` now means the Kimi coding plan (`kimi-for-coding`), not Moonshot's platform key.
    - Moonshot's platform key is now `MOONSHOT_API_KEY`.
    - `GLM_API_KEY` is now `ZHIPU_API_KEY`.
    - No longer read at all: `KIMI_CODING_API_KEY`, `KIMI_BASE_URL`, `KIMI_CODING_BASE_URL`, `GLM_BASE_URL`, `GEMINI_BASE_URL` (now `GOOGLE_BASE_URL`), `OPENAI_CHATGPT_BASE_URL` (now `OPENAI_CODEX_BASE_URL`), `OPENAI_COMPATIBLE_PROVIDER_QUIRKS`.
    - Every `*_BASE_URL` value now includes the version segment (e.g. `https://api.anthropic.com/v1`, not `https://api.anthropic.com`) — except DeepSeek's, a documented exception.
  - **`auth/openai.json` vs `auth/openai-codex.json`.** OAuth records are per instance: `auth/openai.json` belongs to an instance literally named `openai`, which by default is the platform API and never reads it. `evener openai login` now writes `auth/openai-codex.json`, for the `openai-codex` instance. `openai/…` still means the platform API unless the user writes `[providers.openai] base = "openai-codex"` — in which case the *old* record is read as that instance's. A stray record — `auth/<name>.json` for any instance not on the Codex transport, including `auth/work.json` from a prior `evener openai login --instance work` — produces a startup notice until removed with `evener openai logout --instance <name>` or deleted by hand.
  - **`credentials.toml` entries under old names** are ignored and reported by `evener providers list`; re-enter them under the new names through the hub's credentials pane.
  - **`[1m]` references.** Only the Sonnet 4.5 and Opus 4.5 rows keep the `[1m]` suffix (`claude-sonnet-4-5[1m]`, `claude-sonnet-4-5-20250929[1m]`, `claude-opus-4-5[1m]`, `claude-opus-4-5-20251101[1m]`); `claude-opus-4-6[1m]` and later are unknown ids (the 4.6+ rows are 1M natively, no suffix needed or accepted).
  - **Sessions on Fable 5 with no `--reasoning-effort`** move from the injected `medium` to Anthropic's default `high`.
  - **Ollama and local-model context windows.** The bundled per-model catalog (8192 for `llama3.1`, tag-stripping) is gone; every live-only model on `ollama` or a pseudo-provider now budgets against the provider-level `131072` default. Pin the real window with `[providers.ollama.models."llama3.1*"] context_window = 8192` or compaction fires late (cross-reference Step 3's Ollama rewrite — this is the same fact, don't restate it differently in two places).
  - **`EVENER_PROVIDERS_CONFIG` tri-state.** `export EVENER_PROVIDERS_CONFIG=` (present, empty) now means "no user layer"; today it meant the default path. `evener providers list` and the hub diagnostics print `user layer: none (EVENER_PROVIDERS_CONFIG is empty)` so the state is visible.
  - **`gemini` is no longer accepted** as an alias of `google` in model references.
  - Close with the spec's own framing, quoted: "None of this is detected or translated at runtime, and none of the old files are renamed or deleted."

- [ ] **Step 6: Verify and commit**

  Run this from the repo root; every command must print nothing (an empty result confirms the stale terms are gone from every doc this task touches):
  ```bash
  grep -rn -P \
    'api_style|\bquirks\b|behavior tag|BehaviorTag|providerconfig\.|providercfg\.|LiteLLM|litellm_model_catalog|evener_model_catalog_overrides|Phase 1a|Phase 1b|Phase 1c|\[instances\.|thinking_levels|OPENAI_CHATGPT_BASE_URL|GEMINI_BASE_URL|GLM_API_KEY|GLM_BASE_URL|KIMI_BASE_URL|KIMI_CODING_API_KEY|KIMI_CODING_BASE_URL|OPENAI_COMPATIBLE_PROVIDER_QUIRKS|openrouter-anthropic|kimi-anthropic|\bkimi\b(?!-for-coding)' \
    README.md \
    docs/getting-started.md \
    docs/evener-hub.md \
    docs/developing-evener/environment.md \
    docs/llm-providers.md \
    docs/llm-provider-config-and-launch.md \
    docs/ollama.md
  ```
  (`-P` is GNU grep's PCRE mode, which the lookahead in `\bkimi\b(?!-for-coding)` needs; it catches a bare `kimi` instance-name reference while allowing `kimi-for-coding`.) Also re-run the same grep with `docs/superpowers/specs/2026-08-28-provider-registry-design.md` excluded but no other doc excluded, to make sure nothing was missed in a file this task didn't explicitly enumerate but that mentions providers in passing (spot-check with a broader repo-wide `grep -rln 'openrouter-anthropic\|kimi-anthropic' docs/ README.md` and fix anything that turns up outside this task's file list by filing it as a follow-up rather than silently expanding this task's scope).

  Then stage and commit only the seven files this task touches (never `git add -A`):
  ```bash
  git add README.md \
    docs/getting-started.md \
    docs/evener-hub.md \
    docs/developing-evener/environment.md \
    docs/llm-providers.md \
    docs/llm-provider-config-and-launch.md \
    docs/ollama.md
  git commit -m "docs: rewrite provider docs around the registry design"
  ```

---

### Task 15: Final gate and the flag-day checklist

**Files:** none new; this task runs the full gate and pins the §13 "Flag day" rows that earlier tasks left implicit.

- [ ] **Step 1: Flag-day assertions present**

Confirm each of these tests exists and passes (add any missing one in the package that owns the behaviour):
- `cmd/evener`: an old-schema `providers.toml` at the default path makes `evener models list` and `evener providers list` exit non-zero with the `§14.1` pointer (`models_test.go`, `providers_test.go`).
- `cmd/evener-hub`: startup with an old-schema file serves with `WritesRefused` and the pointer in `Diagnostics`; a spawned child's env carries `EVENER_PROVIDERS_CONFIG=` (present, empty) and `EVENER_CREDENTIALS_CONFIG=<the hub's store>`, at the default path and at a custom `EVENER_PROVIDERS_CONFIG` path; an instance write is refused with the pointer; a stray `auth/openai.json` produces exactly one notice (`registry_test.go`, `main_test.go`, `app_instances_test.go`).
- `cmd/evener`: a `credentials.toml` entry under an unknown name is reported by `providers list` and ignored (`providers_test.go`).
- `llm`: the LiteLLM data and `evener_model_catalog_overrides.json` are gone from the build (`ls llm/data` fails; `go list -deps ./... | grep -c litellm` is 0).
- `cmdutil`: `TestEnvvarsCoverRegistryVariables` (Task 13).

- [ ] **Step 2: The whole gate**

```bash
export PATH="$(go env GOROOT)/bin:$PATH"
for m in . llm envvars auth identifier invariant fuzz agent; do (cd "$m" && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files') || exit 1; done
(cd llm && go test -race ./ ./registry/ ./providers/... )
(cd agent && go test -race ./provider/ )
make lint && make fuzz-registry-check && make generate && git diff --exit-code --stat
make test-web
```

Expected: every line quiet, `make lint` 9/9 PASS, no generated drift, the frontend suite green.

- [ ] **Step 3: Commit anything the gate needed (none expected) and report**

The final message to the plan's operator lists: the commit range, every `Ruling:` recorded during execution, the deferred minors, and the live-verification rows §13 leaves to plan 4 (Azure, Bedrock, Vertex, Groq Responses, the `[1m]` beta, Kimi K3, MiniMax M3).

---

## Spec coverage

| Spec requirement | Task |
|---|---|
| §5.1 implicit instances, default ranking (used by `DefaultProvider`, `providers list`, hub list) | 2, 9, 11 |
| §5.2 resolve without a credential (`Resolve` on curated ids; hub pane lists implicit providers without keys) | 2, 9 |
| §7.3 unknown models resolvable; Codex allowlist error; window 0 = unknown | 1 (`Synthesized`), 5 (`CanServe`), 7 (contextmgr) |
| §7.4 no injected effort | 4 |
| §7.5 table: tool set/doc files/name map/prompt sections → `Surface`; `web_search` tool → `Protocol == google && WebSearch`; `model_fallbacks` → `Surface` equality; `unrepresentableContentKinds` → `Protocol`; `modelSwitchVisible` → live rule; sandbox allowlist → `ProviderID`; subagent target → `Instance`; plugin `model:` → `FindModel`; `ProviderOptions` key → `Protocol`; prompt-cache gates → `Fields`; `BehaviorTagOf` replay → `Instance` + `Protocol` on turns | 4, 5, 7 |
| §7.5 `ShapeRequest` continuation store override | 2 |
| §7.6 continuation from `Resolved` + `BuildBody`; override planner honored; chat fallback deleted | 3, 8 |
| §8.1 client dispatch, override map semantics, deletions (`nameToTag`, factories, `NewFromEnv`, `providerfwd`, `ErrorClassFallback`, `ModelCompatibilityValidator`), unsupported = registry-only | 2, 5, 8, 13 |
| §9.5 CLI defaults `openai-codex`; hub OAuth eligibility by `Transport.Auth`; stray-record notice | 9, 11 |
| §10 tri-state `EVENER_PROVIDERS_CONFIG` at every reader; `EVENER_CREDENTIALS_CONFIG`; hub child env; store looked up by instance name, roster deleted | 6, 9, 11, 13 |
| §11.2 `evener providers list|probe|add` | 11 |
| §11.3 hub instance CRUD (shadowing edit, refused remove), appwire shapes, `auth/list`, `normalizeAuthProvider`, spawn gate, `model/list` from `Resolved`, diagnostics, frontend, `llmcall` on `LoadClient` | 7 (`llmcall`), 9, 10 |
| §12 `BehaviorTag()` removed from errors; `Provider()` = instance | 13 |
| §13 "Flag day", "Client dispatch", "Continuation" rows | 2, 3, 9, 11, 15 |
| §14 step 3 deletions and docs | 13, 14 |
| §14.1 every bullet (schema, default instance, instance names, variables, Codex OAuth, credentials store, `[1m]`, Fable 5 effort, Ollama windows, tri-state) | 9, 11, 13, 14 |

## Rulings recorded in this plan (for the executing controller)

- The lazily loaded registry of a bare `NewClient()` uses an empty environment, no user layer, and no cache, so test doubles resolve the same curated records on every machine and never pick up a developer's keys (spec §8.1 says only "embedded snapshot offline"). Cost if wrong: a test that expected env-derived implicit instances from a bare client.
- An override under a resolvable name has its plan request shaped, like its dispatch request. Cost if wrong: a fake planner sees `MaxTokens` filled.
- `CrossProviderRef` is "the prefix is not this instance and this instance does not serve the whole ref as a known or live row"; a brand-new namespaced OpenRouter id that is in neither the catalog nor the live listing switches instances instead of being sent verbatim. Cost if wrong: one mid-session switch to the wrong instance for an id OpenRouter added minutes ago.
- The anthropic profile no longer forces `max_tokens = 16384`; the row's `MaxOutputTokens` is the cap. Cost if wrong: larger completions on Anthropic.
- The sandbox net-off allowlist keys on provider id with `openai`, `openai-codex`, `anthropic`, `google`; the cloud transports stay refused under `net=off` until plan 4 verifies their web tools. Cost if wrong: a Vertex/Bedrock session cannot use provider-native web search under `net=off`.
- Plugin-agent `model:` refs may resolve to another instance (spec §7.5's "highest-ranked serving instance"), so cross-instance delegates are now allowed for plugin agents. Cost if wrong: a plugin agent runs on an instance its author did not expect.
- `Client.Models` filters `Model.Hidden` and live `Tools = false` rows for every consumer; membership checks apply only to live listings. Cost if wrong: a registry-only instance never rejects a typo in a model id until the request fails.
- The credentials store keeps only its file layer (`Get`/`Set`/`Clear`/`Names`/`Path`); the "(id → APIKeyEnv) table" of spec §10 is unnecessary because the registry performs every environment lookup. Cost if wrong: none (fewer moving parts).
- `InstanceListResponse` carries the diagnostics (the pane that refuses writes explains why); no separate hub diagnostics RPC. Cost if wrong: other surfaces must fetch the instance list to see them.
- The `ContinuationHasher` reaches the `responses` protocol through the context, not through a mutable field on the process singleton. Cost if wrong: none.
- Hub set-default writes only `default = "<name>"`: spec §5.1 lets `default` name a curated implicit id directly, so no shadowing entry is needed (edit still writes one, spec §11.3). Cost if wrong: a `[providers.<id>]` stub the user did not ask for.
- The four `Adapter`-era `wire_capture_test.go` files are not ported as goldens (plan 2's `wirecapture` corpus is the golden set); only their attempt-ordering and SSE-timeout guarantees move onto the protocols (plan-2 ledger ruling, Task 13 step 1). Cost if wrong: none.
