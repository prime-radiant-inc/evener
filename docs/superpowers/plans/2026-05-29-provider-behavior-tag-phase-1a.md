# Provider Behavior-Tag Separation (Phase 1a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Separate the provider *behavior* (a `behaviorTag`) from the provider/instance *name* (`profile.ID()`/`req.Provider`), and move provider *switching* out of `WithModel` up to the session — as a **behavior-preserving refactor** (every default instance is still named after its type, so `instanceName == type == behaviorTag` and nothing changes observably). This is Phase 1a of the type/instance model; it introduces **no** `providers.toml`, no custom instances, no UI.

**Architecture:** A `behaviorTag` string lives on `ProviderProfile` (set by each recipe). Every provider-conditional behavior keys on the tag instead of the id literal. Identity (`resp.Provider`, error labels) keys on the instance name, stamped centrally in `llm.Client`. Cross-instance switching becomes `agent.ResolveProfileFromConfig` called by the session; `WithModel` only changes the model within an instance. A new `internal/providerconfig` leaf package holds `BehaviorTag(type, apiStyle)` so `llm`, `agent`, `cmdutil`, and `cmd/*` all agree without an import cycle.

**Tech Stack:** Go. Source of truth for the change-list is the spec `docs/superpowers/specs/2026-05-29-provider-type-instance-model-design.md` — especially the **§4.2 inventory table** (the starting set) and **§7 renamed-instance integration test** (the completeness backstop).

**Completeness method (read before starting):** Five prose-review rounds each found new provider-string sites; the §4.2 table is a *starting set, not a guarantee*. The real backstop is Task 2's integration test plus a final `grep` sweep (Task 11). Every task runs `go build ./... && go test ./...` and re-runs the Task 2 test; a task is done only when both pass.

---

## File Structure

- **Create** `internal/providerconfig/providerconfig.go` — leaf package: `Type`, `APIStyle`, `InstanceConfig`, `Config`, `BehaviorTag(type, apiStyle) string`, `NameToTag(Config) map[string]string`, `DefaultStateRoot() string` (relocated `hubStateRoot` resolver). No imports of `llm`/`agent`/`cmdutil`.
- **Modify** `agent/profile.go` — add `behaviorTag` field + `BehaviorTag()` to `baseProfile`/`anthropicProfile`; recipes stamp it; re-key `CheapModel`, `decidePrefixAction`, `rebuildOnSameProviderChange`, `WithModel` (×2), the `:930`/`:1007` helpers, the catalog lookup; remove the cross-provider switch arm.
- **Create** `agent/resolve.go` — `ResolveProfileFromConfig(cfg, ref) (ProviderProfile, error)` and the behavior-preserving re-application of session overrides.
- **Modify** `agent/session.go` — re-key prompt-cache/gemini/prompt-section/fallback sites onto `BehaviorTag()`; `SetModel`/subagent/fallback switch via the resolver + preserve overrides + re-run provider-conditional tools; `resp.Provider` identity.
- **Modify** `agent/subagents.go` — model override via the resolver.
- **Modify** `llm/client.go` — `Client` holds `NameToTag`; central error-provider+tag stamping in `Complete`/`Stream` (wrap `Events()`); drop the `gemini`→`google` rewrite in `normalizeProviderName`.
- **Modify** `llm/classify.go` — `isEndpointFallbackSignal` keys on the error's behavior tag.
- **Modify** `llm/model_catalog.go` — drop the `:67` lookup alias (keep `:243` ingest normalization).
- **Modify** `internal/diagnostic/diagnostic.go` + `cmd/serf-hub/assets/diagnostics.js` — classify on the structured error, not the message string.
- **Modify** `cmd/serf/launch_check.go`, `cmd/serf-hub/web.go`, `cmd/serf-hub/app_rpc.go` — picker/launch behavior filters + `launchProviderAllowsUnreportedModels` via `NameToTag`; resume by lookup.
- **Create** `agent/provider_instance_integration_test.go` — the renamed-instance backstop (Task 2).

---

## Task 1: `internal/providerconfig` leaf package

**Files:**
- Create: `internal/providerconfig/providerconfig.go`
- Test: `internal/providerconfig/providerconfig_test.go`

- [ ] **Step 1: Write the failing test** for `BehaviorTag`.

```go
package providerconfig

import "testing"

func TestBehaviorTag(t *testing.T) {
	cases := []struct{ typ, style, want string }{
		{"openai", "responses", "openai"},
		{"openai", "chat-completions", "openai-compatible"},
		{"openai", "", "openai"}, // default style = responses
		{"anthropic", "", "anthropic"},
		{"google", "", "google"},
		{"openrouter", "", "openrouter"},
		{"openrouter-anthropic", "", "openrouter-anthropic"},
		{"kimi", "", "kimi"},
		{"glm", "", "glm"},
		{"minimax", "", "minimax"},
		{"ollama", "", "ollama"},
	}
	for _, c := range cases {
		if got := BehaviorTag(c.typ, c.style); got != c.want {
			t.Errorf("BehaviorTag(%q,%q)=%q want %q", c.typ, c.style, got, c.want)
		}
	}
}

func TestNameToTagIdentityForTypeNames(t *testing.T) {
	cfg := Config{Instances: []InstanceConfig{
		{Name: "openai", Type: "openai"},
		{Name: "work", Type: "openai", APIStyle: "chat-completions"},
	}}
	m := NameToTag(cfg)
	if m["openai"] != "openai" || m["work"] != "openai-compatible" {
		t.Errorf("NameToTag = %v", m)
	}
}
```

- [ ] **Step 2: Run it, verify it fails** (package/functions undefined).

Run: `go test ./internal/providerconfig/ -run TestBehaviorTag -v`
Expected: FAIL (build error / undefined).

- [ ] **Step 3: Implement** the package.

```go
// Package providerconfig is the leaf type/behavior-tag vocabulary shared by
// llm, agent, cmdutil, and the cmd/* binaries. It imports none of them.
package providerconfig

import (
	"os"
	"path/filepath"
)

type Type string
type APIStyle string

const (
	StyleResponses       APIStyle = "responses"
	StyleChatCompletions APIStyle = "chat-completions"
)

type InstanceConfig struct {
	Name     string
	Type     Type
	APIStyle APIStyle
	BaseURL  string
	APIKey   string
}

type Config struct {
	Default   string
	Instances []InstanceConfig
}

// BehaviorTag is the internal behavior identity every provider-conditional
// behavior keys on. It equals the type for all types except openai, which
// splits by apiStyle.
func BehaviorTag(typ, style string) string {
	if typ == "openai" && style == string(StyleChatCompletions) {
		return "openai-compatible"
	}
	return typ
}

func NameToTag(cfg Config) map[string]string {
	m := make(map[string]string, len(cfg.Instances))
	for _, in := range cfg.Instances {
		m[in.Name] = BehaviorTag(string(in.Type), string(in.APIStyle))
	}
	return m
}

// DefaultStateRoot returns $hubStateRoot (default ~/.serf), relocated here so
// cmd/serf and cmd/serf-hub resolve the identical path.
func DefaultStateRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".serf")
	}
	return ".serf"
}
```

- [ ] **Step 4: Run, verify pass.** Run: `go test ./internal/providerconfig/ -v` → PASS.
- [ ] **Step 5: Commit.** `git add internal/providerconfig && git commit -m "feat(providerconfig): leaf behavior-tag vocabulary (PRI-1880)"`

---

## Task 2: Renamed-instance integration test (the failing backstop)

This test encodes Phase 1a's acceptance. It will FAIL until Tasks 3-10 land. Write it now so every later task measures against it.

**Files:**
- Create: `agent/provider_instance_integration_test.go`

- [ ] **Step 1: Write the test.** Construct a profile whose instance name differs from its type — an `openai`-behavior profile named `work` — using the same construction the resolver will use, and assert behavior keys on the tag, identity on the name. Use the project's existing test client/mocks (see `agent/session_dod_test.go` for the established harness and a fake `llm` transport).

```go
package agent

import "testing"

// Phase 1a backstop: an openai-behavior profile named "work" must behave like
// openai by TAG and identify as "work" by NAME. Any site still keyed on the
// literal "openai" fails here. Expand assertions as tasks land.
func TestRenamedInstance_BehaviorByTag_IdentityByName(t *testing.T) {
	p := newOpenAIProfileNamed(t, "work") // helper: builds an openai-recipe profile with id="work", behaviorTag="openai"

	if p.ID() != "work" {
		t.Fatalf("ID()=%q want work", p.ID())
	}
	if p.BehaviorTag() != "openai" {
		t.Fatalf("BehaviorTag()=%q want openai", p.BehaviorTag())
	}
	// Prompt-cache eligibility keys on the tag, not the name:
	if !openAIBehavior(p) { // helper asserting the §4.2 :1382 path treats p as openai
		t.Fatalf("renamed openai instance lost prompt-cache eligibility")
	}
	// CheapModel keys on the tag:
	if p.CheapModel() != "gpt-4.1-nano" {
		t.Fatalf("CheapModel()=%q want gpt-4.1-nano (tag-keyed)", p.CheapModel())
	}
}
```

- [ ] **Step 2: Add the helpers** `newOpenAIProfileNamed` and the behavior probes incrementally as the API firms up in Task 3. Initially they may not compile — that is the point (failing target).
- [ ] **Step 3: Run, confirm it fails** (no `BehaviorTag()` yet). Run: `go test ./agent/ -run TestRenamedInstance -v` → FAIL.
- [ ] **Step 4: Commit the failing test.** `git add agent/provider_instance_integration_test.go && git commit -m "test(agent): renamed-instance backstop (failing) (PRI-1880)"`

> Re-run this test at the end of every subsequent task. Expand it (a `work2` second instance, a streamed error reporting `work`, a `/model work→work2` switch, a resume) as the corresponding tasks land — by Task 10 it must exercise the full §7 scenario.

---

## Task 3: `BehaviorTag()` on profiles; recipes stamp it

**Files:**
- Modify: `agent/profile.go` (add field + accessor; `buildBaseProfile`/`profileSpec`; each constructor)
- Test: `agent/profile_test.go`

- [ ] **Step 1: Write failing tests** asserting each constructor stamps the right tag: `NewOpenAIProfile(...).BehaviorTag()=="openai"`, `NewAnthropicProfile`→`anthropic`, `NewGeminiProfile`→`google`, `NewOpenAICompatProfile("openrouter",...)`→`openrouter`, etc. Include a `WithProviderID`/named variant returning `ID()=="work"`, `BehaviorTag()=="openai"`.
- [ ] **Step 2: Run, verify fail** (`BehaviorTag` undefined).
- [ ] **Step 3: Implement.** Add `behaviorTag string` to `baseProfile` and `anthropicProfile`; add `func (p *baseProfile) BehaviorTag() string { return p.behaviorTag }` (and anthropic). Add `behaviorTag` to `profileSpec`; `buildBaseProfile` copies it. Each constructor sets it via `providerconfig.BehaviorTag(type, style)`. Add a `WithProviderID(p, name)` wrapper (mirroring `WithCommunicateOutputSchema`) that overrides `id` while keeping the tag. Add `BehaviorTag()` to the `ProviderProfile` interface (`profile.go:40`).
- [ ] **Step 4: Run profile tests + the Task 2 backstop.** Run: `go test ./agent/ -run 'Profile|RenamedInstance' -v`. Task 2's `BehaviorTag()`/`CheapModel` assertions should now compile; `CheapModel` still keyed on `id` so it may fail — that's Task 4.
- [ ] **Step 5: Commit.** `git add agent/profile.go agent/profile_test.go && git commit -m "feat(agent): BehaviorTag on profiles, recipes stamp it (PRI-1880)"`

---

## Task 4: Re-key `agent/profile.go` id-value branches onto the tag

Per spec §4.2 rows for `profile.go`. Each is `switch p.id`→`switch p.behaviorTag` (and the `case "gemini"`→`case "google"`), plus the catalog lookup and the prefix helpers.

**Files:**
- Modify: `agent/profile.go` (`:344 CheapModel`, `:396 decidePrefixAction`, `:498 rebuildOnSameProviderChange`, `:519/533/562 WithModel`, `:649 anthropic WithModel`, `:930`, `:955/964 catalog`, `:1007`)
- Test: `agent/profile_test.go`

- [ ] **Step 1: Write failing tests** with *renamed* instances: a `kimi`-behavior profile named `work` whose `CheapModel`, catalog context-window lookup, and `rebuildOnSameProviderChange` all behave as `kimi`; a `google`-named instance whose `CheapModel` returns `gemini-2.5-flash-lite`; an `openrouter`-named-`work` instance whose Codex/minimax gate (`:1007`) and meta-namespace keep (`:405`) fire by tag.
- [ ] **Step 2: Run, verify fail** (still keyed on `id`).
- [ ] **Step 3: Implement.** Mechanically apply each §4.2 row: `CheapModel` `switch p.behaviorTag` with `case "google"`; `decidePrefixAction(behaviorTag, instanceName, prefix)` — **keep** uses `behaviorTag` (meta-providers), self-prefix **strip** compares `prefix == instanceName`; `rebuildOnSameProviderChange(behaviorTag)`; `WithModel` passes `p.behaviorTag` + `p.id` and the rebuild constructor (`NewOpenAICompatProfile`) takes both name and tag (extend its signature or add a named variant); `:930` `behaviorTag == "ollama"`; catalog lookup uses `behaviorTag + "/" + model`; `:1007` `behaviorTag == "openrouter"`. **Remove the cross-provider switch arm** from both `WithModel`s (the `prefixActionSwitch` outcome) — switching now lives in the session (Task 8). `WithModel` returns within-instance results only.
- [ ] **Step 4: Run** `go test ./agent/ -run 'Profile|RenamedInstance|WithModel' -v`. Rewrite the now-obsolete cross-provider `WithModel` unit tests (`TestProviderProfile_WithModel_CrossProvider` `:366`, `:379`, `TestBaseProfile_WithModel_PreservesSlashOnMetaProviders` `:658`) — the meta-namespace *keep* test stays (assert verbatim within instance); the cross-provider *switch* tests move to Task 8 (session-level). Confirm the catalog rebuild still recomputes the context window for a same-instance model change.
- [ ] **Step 5: Commit.** `git add agent/profile.go agent/profile_test.go && git commit -m "refactor(agent): key profile.go behaviors on behaviorTag, drop WithModel switch (PRI-1880)"`

---

## Task 5: Re-key `agent/session.go` behavior sites onto the tag

**Files:**
- Modify: `agent/session.go` (`:1382` prompt-cache, `:4785` gemini, `:3972` prompt-section provider; the fallback guard `:4139`/execution `:2706` move to Task 8)
- Test: `agent/session_*_test.go`

- [ ] **Step 1: Write failing tests** (extend Task 2): a `work`-named openai instance gets 24h prompt-cache eligibility and the `tools.provider-openai_append.md` section; a `chat-completions` instance named `work` gets **neither** (tag `openai-compatible`); a `google`-named instance hits the gemini path.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** `:1382` `s.profile.BehaviorTag() == "openai"` (note: `applyModelRequestMetadata` runs on the session, so use `s.profile`, not `req.Provider`). `:4785` `s.profile.BehaviorTag() == "google"`. `:3972` `provider: s.profile.BehaviorTag()`. Leave `:3873 PromptData.Provider` (no section consumer) or set it to the tag — harmless.
- [ ] **Step 4: Run** `go test ./agent/ -run 'Session|RenamedInstance|PromptCache|Section' -v`.
- [ ] **Step 5: Commit.**

---

## Task 6: Central error + `resp.Provider` identity in `llm.Client`

**Files:**
- Modify: `llm/client.go` (hold `NameToTag`; stamp in `Complete` return + wrap `Stream`'s `Events()`); `agent/session.go:3358/3511/1492` for `resp.Provider`
- Test: `llm/client_test.go`, `agent` identity test

- [ ] **Step 1: Write failing tests:** a non-streaming `Complete` and a streamed `StreamEventError` from an adapter registered as `work` surface an error whose `Provider()=="work"` (not the adapter's hardcoded type); a `context.Canceled` stays unlabeled; the error carries `behaviorTag=="openai"` for `classify.go`.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** Add `nameToTag map[string]string` to `Client` (set by the constructor; identity for the env path). In `Complete`, after the adapter returns, `err = RewriteErrorProvider(err, req.Provider)` and stamp the tag (`nameToTag[req.Provider]`) onto the structured error (add a `behaviorTag` field + setter to the `providerSetter` errors). In `Stream`, wrap the returned stream so each `StreamEvent{Type: StreamEventError}` is stamped the same way — this covers `consumeModelStream` and `StreamGenerate`. Keep `RewriteErrorProvider`'s empty-Provider no-op. Set `resp.Provider = req.Provider` at `session.go:3358`/`:3511`/`:1492`.
- [ ] **Step 4: Run** `go test ./llm/ ./agent/ -run 'Error|Identity|Stream|RenamedInstance' -v`.
- [ ] **Step 5: Commit.**

---

## Task 7: Re-key `llm` routing/classify/catalog + `diagnostic`

**Files:**
- Modify: `llm/client.go:236` (`normalizeProviderName` drop gemini→google, keep lower/trim); `llm/classify.go:114` (tag); `llm/model_catalog.go:67` (drop lookup alias, keep `:243`); `internal/diagnostic/diagnostic.go` + `cmd/serf-hub/assets/diagnostics.js`
- Test: `llm/*_test.go`, `internal/diagnostic/*_test.go`

- [ ] **Step 1: Write failing tests:** `isEndpointFallbackSignal` true for an error with behavior tag `openai` regardless of provider name `work`; `normalizeProviderName("gemini")=="gemini"` (no rewrite); `diagnostic.Classify` of a `work`-provider structured `llm.Error` is `SourceProvider`.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** `classify.go:114` key on the error's behavior tag. `client.go:236` remove the `gemini`→`google` branch (keep lowercase/trim). `model_catalog.go:67` remove the lookup alias (keep ingest `:243`). `diagnostic.isProviderFailure` classify on the structured `llm.Error`'s provider/tag rather than `strings.Contains(msg, provider+" error")`; mirror the fix in `diagnostics.js`.
- [ ] **Step 4: Run** `go test ./llm/ ./internal/diagnostic/ -v`.
- [ ] **Step 5: Commit.**

---

## Task 8: Switching at the session (`ResolveProfileFromConfig`)

**Files:**
- Create: `agent/resolve.go`
- Modify: `agent/session.go` (`SetModel:1271`, fallback guard `:4139` + execution `:2706`, provider-conditional tool re-registration), `agent/subagents.go:159,163`, `cmdutil/cmdutil.go` (`SelectProfile` wraps the resolver), `cmd/serf/serve.go`/`run.go` (pass the env-derived single-instance config + identity NameToTag into `SessionConfig`)
- Test: `agent/session_*_test.go`, `agent/resolve_test.go`

- [ ] **Step 1: Write failing tests:** `SetModel("work2/gpt-5")` on a session whose config has `work`+`work2` (both openai) swaps to `work2` and preserves a `WithCommunicateOutputSchema` override; a subagent override to a different instance resolves via the resolver; a cross-*tag* `model_fallbacks` entry errors, a same-tag cross-instance one is allowed; switching to a `google` instance adds the gemini `web_search` tool.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** `agent.ResolveProfileFromConfig(cfg, ref)`: parse `ref`; first segment = instance name; build the type's profile via its recipe with `id=instanceName` + tag. `Session` gains `cfg providerconfig.Config` (via `SessionConfig`). `SetModel`: if the first segment is a different configured instance → resolver + swap `s.profile` + **re-apply the session's output-schema/allowed-decisions** + re-run provider-conditional tool registration; else `WithModel`. `subagents.go`: same. `model_fallbacks` guard + execution resolve `fbModel` via the resolver and compare `BehaviorTag()`. `cmdutil.SelectProfile` becomes a thin wrapper over the resolver + the schema/decisions wrappers. In Phase 1a the config is the single env-derived instance (name == type), so behavior is unchanged.
- [ ] **Step 4: Run** `go test ./agent/ ./cmdutil/ -run 'SetModel|Resolve|Fallback|Subagent|RenamedInstance' -v`. Move the cross-provider switch tests from Task 4 here (now session-level).
- [ ] **Step 5: Commit.**

---

## Task 9: Resume by lookup; launch/picker behavior filters via `NameToTag`

**Files:**
- Modify: `cmd/serf-hub/app_rpc.go` (`resumeProviderFromProfileID:1735` → lookup; `resumeRequestForConfig:1717` returns an error; `launchProviderAllowsUnreportedModels:1526` via `NameToTag`), `cmd/serf/launch_check.go:104,222`, `cmd/serf-hub/web.go:2038,2064`
- Test: `cmd/serf-hub/app_rpc_test.go`, `cmd/serf/launch_check_test.go`

- [ ] **Step 1: Write failing tests:** resume of a session whose `Meta.ProfileID=="work"` reconstructs the ref via config lookup (and errors on a vanished instance); the openrouter tools-filter / `openrouter-anthropic` skip fire for a renamed openrouter instance via `NameToTag`.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** Replace the resume allowlist with a `cfg` lookup; thread an error out of `resumeRequestForConfig`. Pass `NameToTag` to the launch-check subprocess output / web picker; re-key `:104/:222/:2038/:2064/:1526` on the tag.
- [ ] **Step 4: Run** `go test ./cmd/serf-hub/ ./cmd/serf/ -v`.
- [ ] **Step 5: Commit.**

---

## Task 10: Expand the integration test to the full §7 scenario

**Files:**
- Modify: `agent/provider_instance_integration_test.go` (+ a hub-level test if the picker/resume path needs the daemon)

- [ ] **Step 1: Expand** Task 2's test to the full §7 walk: `work`+`work2` openai instances; assemble a prompt (assert the `openai` section by tag); a streamed call; a streamed **error** reporting `work`; a `/model work→work2` switch preserving overrides; a resume of a `work` session. Add a `chat-completions`-named instance asserting it gets **none** of the `openai`-tag behavior.
- [ ] **Step 2: Run** `go test ./agent/ -run TestRenamedInstance -v` → PASS.
- [ ] **Step 3: Commit.**

---

## Task 11: Completeness sweep + full suite

**Files:** (whatever the sweep surfaces)

- [ ] **Step 1: Grep sweep** for residual provider literals the inventory missed:

Run: `grep -rnE '"(openai|anthropic|google|gemini|openrouter|openrouter-anthropic|kimi|glm|minimax|ollama)"' agent/ llm/ cmd/ cmdutil/ internal/ | grep -viE '_test|adapter\.go|providerconfig|BehaviorTag|catalog|QuirksPreset|case '`

Triage each hit: a routing/registration literal is fine; a *behavior* comparison on `profile.ID()`/`req.Provider`/`resp.Provider` is a bug — fix it (re-key on tag/`NameToTag`) with a test.

- [ ] **Step 2: Full build + test.** Run: `go build ./... && go test ./...` → all green.
- [ ] **Step 3: Run the app** (per the `run` skill): build, start a `serf` session against a default (type-named) instance, switch `/model`, confirm a live turn streams — proving the refactor is behavior-preserving end to end.
- [ ] **Step 4: Final commit.** `git commit -m "refactor(serf): complete Phase 1a behavior-tag separation (PRI-1880)"`

---

## Self-Review (done before handing off to execution)

- **Spec coverage:** every §4.2 row maps to Task 4/5/6/7/9; switching (§4.5-4.6) → Task 8; identity (§4.3) → Task 6; the leaf package (§4.1) → Task 1; the backstop (§7) → Tasks 2+10.
- **Behavior-preserving:** Phase 1a introduces no `providers.toml` and no custom instances at runtime — the single env-derived instance is named after its type, so `name==type==tag` and all behavior is unchanged; the renamed-instance assertions are *test-only* construction.
- **Deferred to 1b (not in this plan):** `providers.toml` + loader, `NewFromProviders`, per-instance OAuth + `AuthRecord.Validate` + `openai_login`, the `apiStyle` recipe + anthropic base-URL instances + the `openai-compatible` fold-in, spawn `SERF_PROVIDERS_CONFIG`, the relocated `DefaultStateRoot` *wiring* (the function exists in Task 1; its consumers are 1b).
- **Type consistency:** `BehaviorTag()` (method), `behaviorTag` (field), `providerconfig.BehaviorTag` (func), `NameToTag` used consistently.

## Execution Handoff

Two execution options:
1. **Subagent-Driven (recommended)** — fresh subagent per task, two-stage review between tasks.
2. **Inline Execution** — execute in this session with checkpoints.
