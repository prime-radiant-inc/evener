# Phase 1c — All-Config-Driven Provider Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `providers.toml` the single, always-present source of truth for provider instances — materialized (shallow, descriptors-only) from env on first run, with secrets resolved separately and injected at load time.

**Architecture:** A pure seed/marshal in the `internal/providerconfig` leaf; the env-detection + write + credential-injection orchestrated in `cmdutil.LoadClient` (which may import `llm` and `internal/credentials`). Adapters are unchanged except the openai factory restoring its env tunables. The env path (`NewFromEnv`) survives only as the materializer's detection input.

**Tech Stack:** Go, BurntSushi/toml, the existing `providerconfig`/`credentials`/`llm` packages.

Spec: [`../specs/2026-05-29-provider-instances-phase-1c-all-config.md`](../specs/2026-05-29-provider-instances-phase-1c-all-config.md).

---

## File Structure

- `internal/providerconfig/materialize.go` (new) — pure: `Seed(...) Config`, `Marshal(Config) ([]byte,error)`, `baseURLEnvVar(typ) string`. Leaf, no `llm` import.
- `internal/providerconfig/materialize_test.go` (new).
- `internal/credentials/store.go` (modify) — add `ResolveKey(name, typ) (string, Source)`.
- `llm/client.go` (modify) — add `DefaultProvider() string` getter (if absent).
- `llm/providers/openai/adapter.go` (modify) — instance factory restores `OPENAI_ORG_ID`/`OPENAI_PROJECT_ID`/`OPENAI_CHATGPT_BASE_URL` from env.
- `cmdutil/load_client.go` (modify) — materialize-if-absent, inject credentials, always `NewFromProviders`.
- `cmdutil/materialize.go` (new) — `materializeProvidersConfig(path string, opts ...llm.EnvOption) (providerconfig.Config, error)` (env detection → `Seed` → `Marshal` → atomic write).
- Tests alongside each.

DRY/YAGNI: reuse `providerconfig.LoadFile`, `credentials.providerEnvVars`, the existing atomic-write pattern in `credentials/store.go:184`.

---

### Task 1: Pure descriptor seed + base-URL env map (`internal/providerconfig`)

**Files:**
- Create: `internal/providerconfig/materialize.go`
- Test: `internal/providerconfig/materialize_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSeedDescriptorsOnly(t *testing.T) {
	getBase := func(typ string) string {
		switch typ {
		case "openai-compatible":
			return "https://vllm.local/v1"
		case "ollama":
			return "http://localhost:11434/v1"
		}
		return "" // others: type default
	}
	cfg := Seed([]string{"anthropic", "openai", "openai-compatible", "ollama"}, "anthropic", getBase)

	// default preserved
	if cfg.Default != "anthropic" {
		t.Fatalf("default = %q, want anthropic", cfg.Default)
	}
	byName := map[string]InstanceConfig{}
	for _, i := range cfg.Instances {
		byName[i.Name] = i
		if i.APIKey != "" {
			t.Errorf("instance %q carries a secret api_key", i.Name) // descriptors-only
		}
	}
	// openai-compatible folds into openai/chat-completions with its base URL
	oc := byName["openai-compatible"]
	if oc.Type != "openai" || oc.APIStyle != StyleChatCompletions || oc.BaseURL != "https://vllm.local/v1" {
		t.Errorf("openai-compatible seed = %+v", oc)
	}
	// plain openai: type default base URL omitted, responses style
	if byName["openai"].BaseURL != "" || byName["openai"].APIStyle != StyleResponses {
		t.Errorf("openai seed = %+v", byName["openai"])
	}
	// ollama base captured (required)
	if byName["ollama"].BaseURL != "http://localhost:11434/v1" {
		t.Errorf("ollama base = %q", byName["ollama"].BaseURL)
	}
}
```

- [ ] **Step 2: Run it, verify it fails** — `go test ./internal/providerconfig/ -run TestSeedDescriptorsOnly` → FAIL (undefined: Seed).

- [ ] **Step 3: Implement `Seed` + `baseURLEnvVar`**

`Seed(providerNames []string, defaultName string, getBaseURL func(typ string) string) Config`:
- For each name: the env-path registration name equals the type/tag. Map `"openai-compatible"` → `InstanceConfig{Name:"openai-compatible", Type:"openai", APIStyle:StyleChatCompletions, BaseURL:getBaseURL("openai-compatible")}`. Map `"openai"` → `{Name:"openai", Type:"openai", APIStyle:StyleResponses}`. All others → `{Name:typ, Type:Type(typ), BaseURL:getBaseURL(typ)}` (BaseURL only if non-empty).
- Set `Default = defaultName`. Sort instances by name for determinism. Never set `APIKey`.

`baseURLEnvVar(typ string) string` returns the env var name (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `GEMINI_BASE_URL`, `KIMI_BASE_URL`, `GLM_BASE_URL`, `OPENROUTER_BASE_URL`, `MINIMAX_BASE_URL`, `OPENAI_COMPATIBLE_BASE_URL`; ollama handled by caller via `OLLAMA_BASE_URL`/`OLLAMA_HOST`).

- [ ] **Step 4: Run test → PASS.**

- [ ] **Step 5: Commit** — `git add internal/providerconfig/materialize.go internal/providerconfig/materialize_test.go && git commit -m "feat(providerconfig): pure descriptor seed for materialization"`

---

### Task 2: Descriptors-only Marshal + round-trip

**Files:** `internal/providerconfig/materialize.go` (+ test).

- [ ] **Step 1: Failing test** — Marshal a `Config` (with a defensively-set `APIKey`) and assert (a) it round-trips through `Load` to the same descriptors, and (b) the output bytes contain **no** `api_key`:

```go
func TestMarshalDescriptorsOnly(t *testing.T) {
	cfg := Config{Default: "openai", Instances: []InstanceConfig{
		{Name: "openai", Type: "openai", APIStyle: StyleResponses, APIKey: "sk-LEAK"},
		{Name: "vllm", Type: "openai", APIStyle: StyleChatCompletions, BaseURL: "https://vllm.local/v1"},
	}}
	data, err := Marshal(cfg)
	if err != nil { t.Fatal(err) }
	if strings.Contains(string(data), "sk-LEAK") || strings.Contains(string(data), "api_key") {
		t.Fatalf("Marshal leaked a secret:\n%s", data)
	}
	got, err := Load(data)
	if err != nil { t.Fatal(err) }
	if got.Default != "openai" || len(got.Instances) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
```

- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement `Marshal`** — emit `default` then a `[instances.<name>]` table per instance with `type`, `api_style` (only when non-empty), `base_url` (only when non-empty), `quirks` (only when non-empty). **Never emit `api_key`.** Use `toml.Marshal` on a shaped struct that omits `APIKey`, or hand-emit.
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(providerconfig): descriptors-only Marshal (never writes secrets)"`

---

### Task 3: `DefaultProvider()` getter on `llm.Client`

**Files:** `llm/client.go` (+ test). *(Skip if a getter already exists — grep first.)*

- [ ] **Step 1: Failing test** — register two adapters, set a default, assert `client.DefaultProvider()` returns it; with no explicit default, returns the first-registered non-`NonDefaultEligible`.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** the getter returning the client's current default provider name.
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(llm): expose Client.DefaultProvider()"`

---

### Task 4: `credentials.Store.ResolveKey(name, typ)`

**Files:** `internal/credentials/store.go` (+ test).

- [ ] **Step 1: Failing test**

```go
func TestResolveKeyNameThenTypeEnv(t *testing.T) {
	dir := t.TempDir()
	writeStore(t, dir, "schema=1\n[providers.work]\napi_key=\"file-work\"\n") // helper
	s, _ := LoadStore(filepath.Join(dir, "credentials.toml"))

	// file entry for instance name wins
	if v, src := s.ResolveKey("work", "openai"); v != "file-work" || src != SourceFile {
		t.Fatalf("name lookup = %q/%v", v, src)
	}
	// custom instance with no file entry → env by TYPE
	t.Setenv("OPENAI_API_KEY", "env-openai")
	if v, src := s.ResolveKey("work2", "openai"); v != "env-openai" || src != SourceEnv {
		t.Fatalf("type-env fallback = %q/%v", v, src)
	}
}
```

- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement `ResolveKey(name, typ string) (string, Source)`** — file entry for `name` (`SourceFile`); else first non-empty env var in `providerEnvVars[typ]` (`SourceEnv`); else `("", SourceAbsent)`.
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(credentials): ResolveKey(name,type) — name-first, type-env fallback"`

---

### Task 5: OpenAI instance factory restores env tunables

**Files:** `llm/providers/openai/adapter.go` (factory at `:156-163`) (+ test).

- [ ] **Step 1: Failing test** — with `t.Setenv("OPENAI_API_KEY","k")`, `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID` set, build via the registered `("openai","responses")` factory from an `InstanceConfig{Name:"openai",Type:"openai"}` and assert the adapter carries `OrgID`/`ProjectID`; with OAuth + `OPENAI_CHATGPT_BASE_URL`, the chatgpt base is honored.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — the factory reads `OPENAI_ORG_ID`/`OPENAI_PROJECT_ID`/`OPENAI_CHATGPT_BASE_URL` from env and threads them into `OpenAIInstanceParams` (mirroring `NewFromEnv`). *Do not* read the api key from env here — that is injected upstream (Task 6).
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "fix(openai): config instance honors org/project/chatgpt-base env tunables"`

---

### Task 6: `materializeProvidersConfig` (cmdutil)

**Files:** `cmdutil/materialize.go` (new) (+ test).

- [ ] **Step 1: Failing test** — in a temp dir with no `providers.toml` and `t.Setenv("OPENAI_API_KEY","k")` + `t.Setenv("ANTHROPIC_API_KEY","k")`: call `materializeProvidersConfig(path)` → assert the file now exists, mode `0644`, parses via `LoadFile`, contains `openai` + `anthropic` instances, **no `api_key`**, and `default` matches the env client's default.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — build a detection client via `llm.NewFromEnv(opts...)`; `names := client.ProviderNames()`; `def := client.DefaultProvider()`; `cfg := providerconfig.Seed(names, def, func(typ string) string { ... read baseURLEnvVar(typ) / OLLAMA_* ... })`; `data,_ := providerconfig.Marshal(cfg)`; atomic write `0644` (temp+rename); return the loaded `cfg`. Idempotent caller responsibility (Task 7 only calls when absent).
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(cmdutil): materialize descriptors-only providers.toml from env"`

---

### Task 7: `LoadClient` — materialize-if-absent + inject credentials + always config

**Files:** `cmdutil/load_client.go` (+ test). This is the integration core.

- [ ] **Step 1: Failing test**

```go
func TestLoadClientMaterializesAndInjects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERF_PROVIDERS_CONFIG", filepath.Join(dir, "providers.toml"))
	t.Setenv("OPENAI_API_KEY", "sk-env")
	// no providers.toml, no credentials.toml yet
	client, cfg, hasConfig, err := LoadClient(llm.WithStateDir(dir))
	if err != nil { t.Fatal(err) }
	if !hasConfig { t.Fatal("hasConfig must be true once materialized") }
	if _, err := os.Stat(filepath.Join(dir, "providers.toml")); err != nil {
		t.Fatal("providers.toml not materialized")
	}
	// the openai instance resolved its key from env (descriptors-only file)
	if !contains(client.ProviderNames(), "openai") { t.Fatal("openai not registered") }
	// in-memory cfg got the injected key; the file did not
	data, _ := os.ReadFile(filepath.Join(dir, "providers.toml"))
	if strings.Contains(string(data), "sk-env") { t.Fatal("secret leaked to providers.toml") }
}
```

- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** the new `LoadClient` flow:
  1. Resolve `path` (unchanged: `SERF_PROVIDERS_CONFIG` else `DefaultStateRoot()/providers.toml`).
  2. `cfg, exists, err := providerconfig.LoadFile(path)`; corrupt → error (unchanged).
  3. If `!exists`: `cfg, err = materializeProvidersConfig(path, opts...)` (writes the file, returns the descriptor cfg).
  4. Load the credentials store from the same root: `store, _ := credentials.LoadStore(filepath.Join(filepath.Dir(path), "credentials.toml"))` (a missing file is an empty store, not an error).
  5. **Inject:** for each `inst` in `cfg.Instances` where `inst.APIKey == ""`, set `inst.APIKey, _ = store.ResolveKey(inst.Name, string(inst.Type))` on the **in-memory** cfg copy.
  6. `client, err := llm.NewFromProviders(cfg, opts...)`; return `(client, cfg, true, nil)`.
  - Remove the `NewFromEnv` "absent" branch. Update the doc comment.
  - **Path alignment note:** confirm `filepath.Dir(path)` is the correct credentials root for hub-spawned children (they get `SERF_PROVIDERS_CONFIG` + `SERF_STATE_DIR` from the hub). If the hub's credentials root differs, align via the same state-dir the OAuth records use. Verify against `cmd/serf-hub/main.go` + `internal/launchconfig/env.go`.
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(cmdutil): LoadClient always config — materialize + inject credentials"`

---

### Task 8: Retire env-default in callers + wire-through

**Files:** `cmd/serf/serve.go` (`buildInitialProfile:440`), `cmd/serf/run.go:138`, `cmdutil.BuildResolveProfile`, any `hasConfig`/`hasProvConfig` branches.

- [ ] **Step 1: Survey** — grep for `hasConfig`, `hasProvConfig`, `NewFromEnv` callers, and the env branches in `buildInitialProfile`/`BuildResolveProfile`. List each.
- [ ] **Step 2: Failing/region tests** — assert `buildInitialProfile` resolves a materialized instance (e.g. `openai/gpt-5`) via the config path; `BuildResolveProfile` always uses `ResolveProfileFromConfig`.
- [ ] **Step 3: Implement** — since `LoadClient` now always returns `hasConfig == true`, simplify the env branches to dead-code removal (keep one logged defensive fallback only if `materialize` fails). Keep `cmdutil.SelectProfile` for the alias/validation paths that still need it (e.g. `gemini` alias), but the *initial profile* + resolver go through config.
- [ ] **Step 4: Run** — `go build ./... && go test ./cmd/serf/... ./cmdutil/...` → PASS.
- [ ] **Step 5: Commit** — `git commit -m "refactor: retire env-path-as-default; config is always-on"`

---

### Task 9: Integration + behavior-preservation sweep

**Files:** `agent/` or `cmdutil/` integration test (+ grep sweep).

- [ ] **Step 1: Integration test** — no `providers.toml`, env keys set → `LoadClient` materializes → a session/profile for each seeded instance builds and routes identically to the pre-1c env path (same base URL, same key source); the **renamed-instance integration test** (`agent/provider_instance_integration_test.go`) stays green.
- [ ] **Step 2: openai-compatible + ollama** — with `OPENAI_COMPATIBLE_BASE_URL` and `OLLAMA_BASE_URL` set, the materialized file has the folded `openai`/`chat-completions` instance and the ollama instance with captured base URLs; ollama is never `default`.
- [ ] **Step 3: Full suite** — `go test ./...` → green. Pristine output (the materialize-failure path logs an asserted message).
- [ ] **Step 4: Grep sweep** — `grep -rn "NewFromEnv" cmd/ cmdutil/ agent/` and confirm every remaining use is the materializer's detection or a logged fallback, not a runtime default.
- [ ] **Step 5: Commit** — `git commit -m "test(phase-1c): from-env materialization is behavior-preserving"`

---

## Self-Review

- **Spec coverage:** materialization (T1-3,6), descriptors-only (T1-2), credential injection (T4,7), openai env tunables (T5), always-config (T7-8), edge cases incl. openai-compatible/ollama/default (T1,9), behavior preservation (T9). ✓
- **Type consistency:** `Seed`/`Marshal` operate on `providerconfig.Config`/`InstanceConfig` (existing); `ResolveKey` returns `(string, Source)` (existing `Source`); `DefaultProvider()` returns the registration name. ✓
- **No placeholders:** every task has concrete files, test assertions, and a commit. The two flagged *verify-against-code* points (credentials root alignment in T7; existing `DefaultProvider` getter in T3) are explicit checks, not gaps.
