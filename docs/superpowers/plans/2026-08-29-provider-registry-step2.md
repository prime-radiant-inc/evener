# Provider Registry Step 2 (Protocol Packages) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the four `Resolved`-driven protocol implementations (`chatcompletions`, `responses`, and protocol types inside `anthropic` and `google`), every authenticator including `gcp-adc` and the Codex transport, the §12 error classifier, wire-capture goldens from `Resolved` inputs, and the adapted cross-provider differential, all beside the old adapters, which keep running untouched (spec §14 step 2).

**Architecture:** Every protocol package owns exactly two things: the body shape (`BuildBody`, a pure function of the shaped `llm.Request` and `registry.Resolved`) and the response/stream decoding. Everything else that today's adapters repeat four times — prune, body constants, authenticator, request preparer, API-attempt logging, error classification, timeouts, SSE bring-up — lives once in `llm/providers/internal/protocolhttp`. Authenticators are looked up by `res.Transport.Auth` through a registry in `llm` (the four trivial schemes live in `llm` itself; `gcp-adc` and `oauth-openai-codex`, which mint bearer tokens from local credential stores, live in `llm/providers/tokenauth`). Protocols are singletons registered at init and looked up by `res.Protocol`; step 3 wires `llm.Client` to them.

**Tech Stack:** Go 1.27 `go.work` workspace; `primeradiant.com/evener/llm` (packages `llm`, `llm/registry`, `llm/providers/*`); `golang.org/x/oauth2` (`google.FindDefaultCredentials`, promoted to a direct dependency of the `llm` module); `primeradiant.com/evener/auth/openai` (OAuth record, refresh, claims); `net/http/httptest` for every transport test; `go test -fuzz` targets registered in `scripts/fuzz/fuzz-targets.txt`.

**Spec:** `docs/superpowers/specs/2026-08-28-provider-registry-design.md` (revision 12). Sections this plan implements: §8 (protocol adapters: interfaces, pruner order, prefix branches → caps, reasoning), §9.1 (URL assembly), §9.5 (Codex transport), §12 (errors), §13 rows "Pruner", "Wire captures", "Continuation", "Error classifier", "Cross-adapter differential", §14 step 2. Plan 1 (`docs/superpowers/plans/2026-08-29-provider-registry-step1.md`) landed `llm/registry`, `llm.ShapeRequest`, and the `Protocol`/`Authenticator`/`RequestPreparer` interfaces in `llm/registry_shape.go`; this plan implements those interfaces.

## Global Constraints

- **Tree green at every commit; nothing deleted.** The old adapters (`llm/providers/openai`, `openaicompat`, `anthropic.Adapter`, `google.Adapter`, the wrapper packages, `providerfwd`) keep compiling and their tests keep passing. Deletions, `llm.Client` dispatch, and every agent/hub change are step 3 (spec §14). The only edits to old code are refactors that let the new types share it (extracting a decoder into a package function, moving `strictifyJSONSchema` to `internal/openaichat`).
- **Request shape is a pure function of `(ShapeRequest(req, res), res)`** (spec §7.5, §8.2). Builders read caps only: never `llm.EmbeddedModelCatalog`, `providercfg`, `envvars`, `os.Getenv`, and never a model-id prefix (`strings.HasPrefix(model, "gpt-5.6")` and friends are gone; spec §8.3 turned each one into a cap: `ResponsesLite`, `ImageDetail`, `ReasoningSummary`, `ThinkingShape`, `ThinkingAlwaysOn`, `ThinkingDisplay`, `MultimodalToolResults`, `WireID`).
- **Body assembly order is fixed** (spec §8.2): 1 build → 2 `registry.Prune(body, res.Caps)` → 3 `registry.ApplyBodyConstants(body, res.Transport.Body)` → 4 `RequestPreparer.PrepareRequest` (Codex only). `Authenticator.Apply` runs before step 4. Only `protocolhttp.Prepare` performs steps 2–4; protocol packages never call `Prune` themselves.
- **Prunable paths are emitted, not gated, by builders**: a builder writes `store`, `metadata`, `stream_options`, `include`, … whenever the request carries them; the prune removes what the row turns off. Cap-governed structure (`strict`, `reasoning.*`, `thinking`, `detail`, lite items) is decided in the builder. `PrunablePaths()` in each package is its own literal list and a test asserts it equals `registry.PrunablePaths(ID())`.
- **Provider identity**: `Response.Provider`, `Error.Provider()`, stream error stamps, and `APIAttemptMeta.ProviderInstance` carry `res.Instance`. `llm.ErrorProtocol(err)` returns the protocol id. `BehaviorTag()` stays on the interface until step 3 removes it.
- **`ProviderOptions` are keyed by protocol id** (`openai-chat`, `openai-responses`, `anthropic`, `google`; spec §7.5 "options are protocol extras"). Step 3 updates the agent's keys.
- **Protocols hold no per-provider state** (spec §8.1): one `RegisterProtocol` per package at init; the only field is `Client *http.Client` (nil → `protocolhttp.DefaultClient`) so tests can inject `httptest` clients and recorders.
- **Credentials never reach logs or goldens**: every wire request's `APILogCredentialMaterial` names the auth header (`Authorization` or `res.Transport.AuthHeader`), every `res.CredentialHeaders` name, and carries `res.Credential.Value` plus whatever value the authenticator set on the auth header. Wire-capture goldens store credential header values as `<credential>`.
- **Flag day, no compatibility code** (spec §14.1): no reading of `OPENAI_CHATGPT_BASE_URL`, `auth/openai.json`, `type`/`api_style`/`quirks`, and no "try Responses, fall back to Chat" logic in any new package — the client dispatches by `res.Protocol`.
- **Tests are offline.** `httptest.Server` or a recording `http.RoundTripper` for every wire test; token sources and OAuth services are injected through exported seams; no test constructs a real `google.Credentials` from the environment.
- **Fuzz registry**: every new `Fuzz*` function gets a `native:llm:./providers/<pkg>:<Name>` row in `scripts/fuzz/fuzz-targets.txt`; `make fuzz-registry-check` must pass.
- **Repo conventions** (as in plan 1): `new(expr)` for optional scalars (golangci `modernize` rejects pointer helpers); `defer func() { _ = x.Close() }()` (errcheck); snake_case JSON tags (tagliatelle); doc comments on every exported identifier (revive); never change whitespace that doesn't affect execution; run `make lint` with `PATH="$(go env GOROOT)/bin:$PATH"` (the system `gofmt` is Go 1.22); TDD per task; commit per task with conventional-commit messages.

## File Structure

| Path | Responsibility |
|---|---|
| `llm/protocols.go` (+ `protocols_test.go`) | `RegisterProtocol`/`ProtocolFor`, `RegisterAuthenticator`/`AuthenticatorFor`/`RequestPreparerFor` |
| `llm/authenticators.go` (+ test) | the trivial schemes `bearer`, `optional-bearer`, `header`, `none`, registered at init |
| `llm/classify_http.go` (+ `classify_http_test.go`, `classify_http_fuzz_test.go`) | `ClassifyHTTPError` (spec §12), hints, `ErrorHint`, `ErrorProtocol`; `httpBaseError` gains `protocol`/`hint` |
| `llm/errors.go` (modify) | `classifyByMessage` rows for `maximum context`, `reduce the length`, `prompt is too long` |
| `llm/registry/prune.go` (modify, + test) | `ApplyBodyConstants` |
| `llm/api_attempt.go`, `llm/apilog/record.go` (modify, + tests) | `PrunedFields` on the attempt record (`pruned_fields`) |
| `llm/providers/internal/protocolhttp/{call.go,exchange.go,stream.go}` (+ tests) | shared runner: `Prepare`, `Do`, `Stream`, `CompleteViaStream`, `URL`, `ModelInBody`, `RequiresStreamingComplete` |
| `llm/providers/internal/openaichat/strict.go` (+ test) | `StrictifyJSONSchema` (moved from `openai/responses.go`, shared by `chatcompletions` and `responses`) |
| `llm/providers/tokenauth/{gcpadc.go,codex.go}` (+ tests) | `GCPADC` (x/oauth2 ADC) and `Codex` (Authenticator + RequestPreparer); `DefaultGCPADC`, `DefaultCodex` registered at init |
| `llm/providers/chatcompletions/{protocol.go,request.go,messages.go,reasoning.go,response.go,stream.go,rescue.go,models.go}` (+ tests, fuzz) | the `openai-chat` protocol (consolidates `openaicompat` and `openai/chatcompletions.go`) |
| `llm/providers/responses/{protocol.go,request.go,input.go,response.go,stream.go,models.go,tokens.go,fingerprint.go}` (+ tests, fuzz) | the `openai-responses` protocol (moved from `openai/responses.go`, `models.go`, `token_count.go`, `responses_continuation_fingerprint.go`) |
| `llm/providers/anthropic/{protocol.go,protocol_request.go,protocol_stream.go}` (+ tests, fuzz); `adapter.go` refactor | `anthropic.Protocol` beside `anthropic.Adapter`; `decodeStream` becomes the shared package function `decodeMessagesStream` |
| `llm/providers/google/{protocol.go,protocol_request.go,protocol_stream.go}` (+ tests, fuzz); `adapter.go`, `request.go` refactor | `google.Protocol` beside `google.Adapter`; `decodeStream` → `decodeGenerateContentStream`; `toGeminiContents` takes the multimodal cap |
| `llm/providers/wirecapture/{doc.go,wirecapture_test.go,testdata/golden/*.json}` | golden wire requests from `Resolved` inputs (spec §13 "Wire captures") |
| `llm/providers/difftest/*` (modify), `testdata/golden/conformance.json` (regenerate) | legs driven through `Protocol.Stream(ctx, req, res)`; `openaicompat` leg → `chatcompletions`, `openai` leg → `responses` |
| `llm/providers/all/all.go`, `scripts/fuzz/fuzz-targets.txt`, `llm/go.mod`, `llm/go.sum`, `go.work.sum` (modify) | registrations, fuzz rows, `golang.org/x/oauth2` |

Left for step 3 on purpose (do not do here): `llm.Client` dispatch and the override map; `ResponsesContinuationPlanner` on the new packages (they export `responses.RequestFingerprint`/`EndpointFamily` for it); `ExtractRecordedResponse`/`ExtractRecordedChatCompletionsResponse` (apilog recompute) moving out of `openai`/`openaicompat`; `tokenauth.ClientVersion` wiring from the binaries; deleting `openai`, `openaicompat`, the wrappers, `providerfwd`, and the old `Adapter` types; docs.

---

### Task 1: Protocol and authenticator registries

**Files:**
- Create: `llm/protocols.go`
- Test: `llm/protocols_test.go`

**Interfaces:**
- Consumes: `llm.Protocol`, `llm.Authenticator`, `llm.RequestPreparer` (`llm/registry_shape.go`).
- Produces: `RegisterProtocol(p Protocol)`, `ProtocolFor(id string) (Protocol, bool)`, `RegisterAuthenticator(scheme string, a Authenticator)`, `AuthenticatorFor(scheme string) (Authenticator, bool)`, `RequestPreparerFor(scheme string) (RequestPreparer, bool)`. Every later task registers through these and `protocolhttp` looks authenticators up through them.

- [ ] **Step 1: Write the failing tests**

`llm/protocols_test.go`:

```go
package llm

import (
	"context"
	"net/http"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

type stubProtocol struct{ id string }

func (s stubProtocol) ID() string                { return s.id }
func (stubProtocol) PrunablePaths() []string     { return nil }
func (stubProtocol) BuildBody(Request, registry.Resolved) (map[string]any, error) {
	return map[string]any{}, nil
}
func (stubProtocol) Complete(context.Context, Request, registry.Resolved) (Response, error) {
	return Response{}, nil
}
func (stubProtocol) Stream(context.Context, Request, registry.Resolved) (Stream, error) {
	return nil, ErrStreamUnsupported
}
func (stubProtocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return nil, ErrModelListingUnsupported
}
func (stubProtocol) CountTokens(context.Context, Request, registry.Resolved) (int, error) {
	return 0, ErrInputTokenCountUnsupported
}

type stubAuth struct{ preparer bool }

func (stubAuth) Apply(context.Context, *http.Request, registry.Resolved) error { return nil }

type stubPreparer struct{ stubAuth }

func (stubPreparer) PrepareRequest(context.Context, *http.Request, map[string]any, Request, registry.Resolved) error {
	return nil
}
func (stubPreparer) RequiresStreamingComplete() bool { return true }

func TestRegisterProtocolAndLookup(t *testing.T) {
	RegisterProtocol(stubProtocol{id: "test-proto-lookup"})
	p, ok := ProtocolFor("test-proto-lookup")
	if !ok || p.ID() != "test-proto-lookup" {
		t.Fatalf("ProtocolFor = %v, %v", p, ok)
	}
	if _, ok := ProtocolFor("test-proto-missing"); ok {
		t.Fatal("unknown protocol must not resolve")
	}
}

func TestRegisterProtocolRejectsDuplicatesAndEmptyIDs(t *testing.T) {
	RegisterProtocol(stubProtocol{id: "test-proto-dup"})
	assertPanics(t, "duplicate", func() { RegisterProtocol(stubProtocol{id: "test-proto-dup"}) })
	assertPanics(t, "empty id", func() { RegisterProtocol(stubProtocol{}) })
}

func TestRegisterAuthenticatorAndPreparer(t *testing.T) {
	RegisterAuthenticator("test-auth-plain", stubAuth{})
	RegisterAuthenticator("test-auth-preparer", stubPreparer{})
	if _, ok := AuthenticatorFor("test-auth-plain"); !ok {
		t.Fatal("plain authenticator not found")
	}
	if _, ok := RequestPreparerFor("test-auth-plain"); ok {
		t.Fatal("plain authenticator must not be a preparer")
	}
	prep, ok := RequestPreparerFor("test-auth-preparer")
	if !ok || !prep.RequiresStreamingComplete() {
		t.Fatalf("preparer lookup = %v, %v", prep, ok)
	}
	if _, ok := AuthenticatorFor("test-auth-missing"); ok {
		t.Fatal("unknown scheme must not resolve")
	}
	assertPanics(t, "duplicate scheme", func() { RegisterAuthenticator("test-auth-plain", stubAuth{}) })
	assertPanics(t, "empty scheme", func() { RegisterAuthenticator("", stubAuth{}) })
}

func assertPanics(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected panic", what)
		}
	}()
	fn()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/ -run 'TestRegisterProtocol|TestRegisterAuthenticator' -count=1`
Expected: FAIL to compile (`undefined: RegisterProtocol`).

- [ ] **Step 3: Write the registries**

`llm/protocols.go`:

```go
package llm

import (
	"fmt"
	"sync"
)

var (
	registryMu     sync.RWMutex
	protocols      = map[string]Protocol{}
	authenticators = map[string]Authenticator{}
)

// RegisterProtocol registers the single implementation of a wire protocol
// under p.ID() (spec §8.1). Registering an id twice panics: two packages
// claiming one protocol is a build mistake, not a runtime condition.
func RegisterProtocol(p Protocol) {
	id := p.ID()
	if id == "" {
		panic("llm: RegisterProtocol: empty protocol id")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := protocols[id]; dup {
		panic(fmt.Sprintf("llm: RegisterProtocol: protocol %q registered twice", id))
	}
	protocols[id] = p
}

// ProtocolFor returns the registered protocol for an id.
func ProtocolFor(id string) (Protocol, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := protocols[id]
	return p, ok
}

// RegisterAuthenticator registers the implementation of one auth scheme
// (registry.AuthBearer, registry.AuthGCPADC, ...). Registering a scheme
// twice panics for the same reason RegisterProtocol does.
func RegisterAuthenticator(scheme string, a Authenticator) {
	if scheme == "" {
		panic("llm: RegisterAuthenticator: empty scheme")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := authenticators[scheme]; dup {
		panic(fmt.Sprintf("llm: RegisterAuthenticator: scheme %q registered twice", scheme))
	}
	authenticators[scheme] = a
}

// AuthenticatorFor returns the registered authenticator for a scheme.
func AuthenticatorFor(scheme string) (Authenticator, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	a, ok := authenticators[scheme]
	return a, ok
}

// RequestPreparerFor returns the scheme's authenticator as a RequestPreparer
// when it implements the optional interface (spec §8.1: only the Codex
// transport does).
func RequestPreparerFor(scheme string) (RequestPreparer, bool) {
	a, ok := AuthenticatorFor(scheme)
	if !ok {
		return nil, false
	}
	p, ok := a.(RequestPreparer)
	return p, ok
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./llm/ -run 'TestRegisterProtocol|TestRegisterAuthenticator' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add llm/protocols.go llm/protocols_test.go
git commit -m "feat(llm): protocol and authenticator registries"
```

---

### Task 2: Body constants, `pruned_fields`, and the trivial authenticators

**Files:**
- Modify: `llm/registry/prune.go` (append after `Prune`), `llm/registry/derive.go` (append `EffortCapable`), `llm/registry/types.go` (append `BoolValue`, `StringValue`)
- Modify: `llm/api_attempt.go:43-55` (`APIAttemptMeta`), `llm/api_attempt.go:486-537` (`buildAPIAttemptRecord`)
- Modify: `llm/apilog/record.go:30-38` (`APIAttemptRequest`), `llm/apilog/record.go:248` (the forbidden-evidence scan list)
- Create: `llm/authenticators.go`
- Test: `llm/registry/prune_constants_test.go`, `llm/api_attempt_pruned_fields_test.go`, `llm/apilog/record_pruned_fields_test.go`, `llm/authenticators_test.go`

**Interfaces:**
- Produces: `registry.ApplyBodyConstants(body map[string]any, constants map[string]any)`; `registry.BoolValue(*bool) bool`, `registry.StringValue(*string) string` (optional-cap readers every protocol package uses); `(Caps) EffortCapable() bool`; `llm.APIAttemptMeta.PrunedFields []string`; `apilog.APIAttemptRequest.PrunedFields []string` (`json:"pruned_fields,omitempty"`); the authenticators registered under `registry.AuthBearer`, `registry.AuthOptionalBearer`, `registry.AuthHeader`, `registry.AuthNone`.

- [ ] **Step 1: Write the failing tests**

`llm/registry/prune_constants_test.go`:

```go
package registry

import (
	"reflect"
	"testing"
)

func TestOptionalCapReadersAndEffortCapable(t *testing.T) {
	if BoolValue(nil) || !BoolValue(new(true)) || BoolValue(new(false)) {
		t.Fatal("BoolValue")
	}
	if StringValue(nil) != "" || StringValue(new("x")) != "x" {
		t.Fatal("StringValue")
	}
	if !(Caps{ReasoningControls: []string{"effort"}}).EffortCapable() || (Caps{ReasoningControls: []string{"toggle"}}).EffortCapable() {
		t.Fatal("effort ∈ ReasoningControls decides")
	}
	if !(Caps{}).EffortCapable() || (Caps{Reasoning: new(true)}).EffortCapable() || (Caps{Reasoning: new(false)}).EffortCapable() {
		t.Fatal("only a row with no verdict and no controls passes an effort through")
	}
}

func TestApplyBodyConstantsCreatesParentsAndOverrides(t *testing.T) {
	body := map[string]any{"text": map[string]any{"format": map[string]any{"type": "text"}}, "parallel_tool_calls": true}
	ApplyBodyConstants(body, map[string]any{
		"reasoning.context":   "all_turns",
		"text.verbosity":      "low",
		"parallel_tool_calls": false,
		"anthropic_version":   "vertex-2023-10-16",
	})
	want := map[string]any{
		"text":                map[string]any{"format": map[string]any{"type": "text"}, "verbosity": "low"},
		"reasoning":           map[string]any{"context": "all_turns"},
		"parallel_tool_calls": false,
		"anthropic_version":   "vertex-2023-10-16",
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %#v\nwant %#v", body, want)
	}
}

func TestApplyBodyConstantsSurvivesPrune(t *testing.T) {
	// Spec §8.2: constants run after the prune, so a constant under a pruned
	// parent still lands (the Codex rows prune nothing under reasoning, but
	// the ordering contract must hold for any row).
	caps := Caps{Fields: map[string]bool{"metadata": false}}
	body := map[string]any{"metadata": map[string]any{"a": "b"}}
	Prune(body, caps)
	ApplyBodyConstants(body, map[string]any{"metadata.trace": "on"})
	if got := body["metadata"]; !reflect.DeepEqual(got, map[string]any{"trace": "on"}) {
		t.Fatalf("metadata = %#v", got)
	}
	ApplyBodyConstants(body, nil)
}
```

`llm/api_attempt_pruned_fields_test.go`:

```go
package llm

import (
	"reflect"
	"testing"
	"time"
)

func TestAPIAttemptRecordCarriesPrunedFields(t *testing.T) {
	meta := APIAttemptMeta{ProviderInstance: "groq", RequestModel: "m", StartedAt: time.Now(), PrunedFields: []string{"store", "stream_options"}}
	record := buildAPIAttemptRecord("ag_1", "at_1", 0, meta, APIAttemptResult{StatusCode: 200, FinishedAt: time.Now()})
	if !reflect.DeepEqual(record.Request.PrunedFields, []string{"store", "stream_options"}) {
		t.Fatalf("pruned fields = %v", record.Request.PrunedFields)
	}
	empty := buildAPIAttemptRecord("ag_1", "at_2", 1, APIAttemptMeta{StartedAt: time.Now()}, APIAttemptResult{FinishedAt: time.Now()})
	if empty.Request.PrunedFields != nil {
		t.Fatalf("expected no pruned fields, got %v", empty.Request.PrunedFields)
	}
}
```

`llm/apilog/record_pruned_fields_test.go`:

```go
package apilog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAPIAttemptRequestPrunedFieldsJSON(t *testing.T) {
	raw, err := json.Marshal(APIAttemptRequest{Method: "POST", Endpoint: "https://x/v1", PrunedFields: []string{"store"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"pruned_fields":["store"]`) {
		t.Fatalf("json = %s", raw)
	}
	raw, _ = json.Marshal(APIAttemptRequest{Method: "POST", Endpoint: "https://x/v1"})
	if strings.Contains(string(raw), "pruned_fields") {
		t.Fatalf("empty pruned_fields must be omitted: %s", raw)
	}
}
```

`llm/authenticators_test.go`:

```go
package llm

import (
	"context"
	"net/http"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func applyScheme(t *testing.T, scheme string, res registry.Resolved) (*http.Request, error) {
	t.Helper()
	a, ok := AuthenticatorFor(scheme)
	if !ok {
		t.Fatalf("scheme %q not registered", scheme)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/v1", nil)
	err := a.Apply(context.Background(), req, res)
	return req, err
}

func TestTrivialAuthenticators(t *testing.T) {
	withKey := registry.Resolved{Instance: "groq", Credential: registry.Credential{Value: "k-1", Source: "env:GROQ_API_KEY"}}
	noKey := registry.Resolved{Instance: "groq", Warnings: []string{"no credential (GROQ_API_KEY unset)"}}

	req, err := applyScheme(t, registry.AuthBearer, withKey)
	if err != nil || req.Header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("bearer: %v %q", err, req.Header.Get("Authorization"))
	}
	if _, err := applyScheme(t, registry.AuthBearer, noKey); err == nil || !contains(err.Error(), "GROQ_API_KEY unset") {
		t.Fatalf("bearer without credential: %v", err)
	}

	req, err = applyScheme(t, registry.AuthOptionalBearer, noKey)
	if err != nil || req.Header.Get("Authorization") != "" {
		t.Fatalf("optional-bearer without credential: %v %q", err, req.Header.Get("Authorization"))
	}
	req, _ = applyScheme(t, registry.AuthOptionalBearer, withKey)
	if req.Header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("optional-bearer with credential: %q", req.Header.Get("Authorization"))
	}

	header := withKey
	header.Transport.AuthHeader = "x-goog-api-key"
	req, err = applyScheme(t, registry.AuthHeader, header)
	if err != nil || req.Header.Get("x-goog-api-key") != "k-1" || req.Header.Get("Authorization") != "" {
		t.Fatalf("header: %v %v", err, req.Header)
	}
	if _, err := applyScheme(t, registry.AuthHeader, withKey); err == nil {
		t.Fatal("header scheme without auth_header must fail")
	}

	req, err = applyScheme(t, registry.AuthNone, noKey)
	if err != nil || len(req.Header) != 0 {
		t.Fatalf("none: %v %v", err, req.Header)
	}
}

func contains(s, sub string) bool { return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

(If `llm` already has a string-contains test helper, use it instead of adding `contains`/`indexOf`; `strings.Contains` is fine too — the helper exists only to keep the test self-contained as written.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/registry/ -run TestApplyBodyConstants -count=1 && go test ./llm/ -run 'TestAPIAttemptRecordCarriesPrunedFields|TestTrivialAuthenticators' -count=1 && go test ./llm/apilog/ -run TestAPIAttemptRequestPrunedFieldsJSON -count=1`
Expected: FAIL (undefined `ApplyBodyConstants`, unknown field `PrunedFields`, unregistered schemes).

- [ ] **Step 3: Implement**

Append to `llm/registry/prune.go`:

```go
// ApplyBodyConstants sets each Transport.Body constant on body, creating
// parent objects as needed (spec §8.2 step 3). Constants run after the
// prune so they survive it, and they override any value the builder or a
// caller's ProviderOptions put at the same path. Keys are applied in sorted
// order so the result never depends on map iteration.
func ApplyBodyConstants(body map[string]any, constants map[string]any) {
	keys := make([]string, 0, len(constants))
	for k := range constants {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		setPath(body, k, constants[k])
	}
}
```

Append to `llm/registry/types.go`:

```go
// BoolValue reads an optional bool cap: nil and false both read as false.
func BoolValue(p *bool) bool { return p != nil && *p }

// StringValue reads an optional string cap: nil reads as "".
func StringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
```

Append to `llm/registry/derive.go`:

```go
// EffortCapable reports whether a row accepts a reasoning effort (spec
// §8.4): effort ∈ ReasoningControls, which after derivation is every
// reasoning row except one that lists controls without effort. A row with
// no controls and no Reasoning verdict (an unknown model) is capable too,
// so an explicit effort still reaches the wire as it did before the
// registry.
func (c Caps) EffortCapable() bool {
	if slices.Contains(c.ReasoningControls, "effort") {
		return true
	}
	return len(c.ReasoningControls) == 0 && c.Reasoning == nil
}
```

In `llm/api_attempt.go`, add to `APIAttemptMeta` (after `RequestBodyInexact`):

```go
	// PrunedFields lists the body paths registry.Prune removed before the
	// request was sent (spec §8.2); recorded as pruned_fields on the attempt.
	PrunedFields []string
```

and in `buildAPIAttemptRecord`, inside the `Request: apilog.APIAttemptRequest{...}` literal after `EndpointFamily`:

```go
			PrunedFields:   append([]string(nil), meta.PrunedFields...),
```

In `llm/apilog/record.go`, add to `APIAttemptRequest` after `EndpointFamily`:

```go
	PrunedFields   []string      `json:"pruned_fields,omitempty"`
```

and in the forbidden-evidence scan list at `record.go:248` add a row beside the endpoint-family row:

```go
		{name: "request pruned fields", value: strings.Join(r.Request.PrunedFields, ",")},
```

(add the `strings` import if the file lacks it).

Create `llm/authenticators.go`:

```go
package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// The four trivial auth schemes of spec §8.1 live here so every protocol
// works for API-key providers without importing a second package; the two
// token-minting schemes (gcp-adc, oauth-openai-codex) live in
// llm/providers/tokenauth.
func init() {
	RegisterAuthenticator(registry.AuthBearer, bearerAuth{})
	RegisterAuthenticator(registry.AuthOptionalBearer, optionalBearerAuth{})
	RegisterAuthenticator(registry.AuthHeader, headerAuth{})
	RegisterAuthenticator(registry.AuthNone, noneAuth{})
}

type bearerAuth struct{}

func (bearerAuth) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	if res.Credential.Value == "" {
		return missingCredential(res)
	}
	req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
	return nil
}

type optionalBearerAuth struct{}

func (optionalBearerAuth) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	if res.Credential.Value != "" {
		req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
	}
	return nil
}

type headerAuth struct{}

func (headerAuth) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	if res.Transport.AuthHeader == "" {
		return &ConfigurationError{Message: fmt.Sprintf("instance %q: auth = header needs auth_header", res.Instance)}
	}
	if res.Credential.Value == "" {
		return missingCredential(res)
	}
	req.Header.Set(res.Transport.AuthHeader, res.Credential.Value)
	return nil
}

type noneAuth struct{}

func (noneAuth) Apply(context.Context, *http.Request, registry.Resolved) error { return nil }

// missingCredential names the instance and repeats the registry's own
// "no credential" warning, which says which variable or login is missing.
func missingCredential(res registry.Resolved) error {
	msg := fmt.Sprintf("instance %q has no credential", res.Instance)
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "no credential") {
			msg += ": " + w
			break
		}
	}
	return &ConfigurationError{Message: msg}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: the Step 2 command, then `go test ./llm/... -count=1` (the apilog codec fuzz seeds and `api_attempt_test.go` must still pass).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add llm/registry/prune.go llm/registry/types.go llm/registry/derive.go llm/registry/prune_constants_test.go llm/api_attempt.go llm/api_attempt_pruned_fields_test.go llm/apilog/record.go llm/apilog/record_pruned_fields_test.go llm/authenticators.go llm/authenticators_test.go
git commit -m "feat(llm): body constants, pruned_fields on attempts, trivial authenticators"
```

---

### Task 3: The §12 error classifier

**Files:**
- Create: `llm/classify_http.go`
- Modify: `llm/errors.go:65-116` (`httpBaseError` gains `protocol` and `hint`; `Error()` renders the hint), `llm/errors.go:344-366` (`classifyByMessage` context-length row)
- Test: `llm/classify_http_test.go`, `llm/classify_http_fuzz_test.go`
- Modify: `scripts/fuzz/fuzz-targets.txt` (add `native:llm:.:FuzzClassifyHTTPError` next to `FuzzErrorFromHTTPStatus`)

**Interfaces:**
- Consumes: `ProviderFailureMessage(operation, body)` (`llm/failuremessage.go:27`), `extractErrorCode`, `errorFromHTTPStatus`, `classifyByMessage`, `parseUsageLimit`/`usageLimitMessage`/`usageLimitCodes` (`llm/usagelimit.go`), `ParseRetryAfter`, `ParseRateLimitHeaders`, `registry.PrunablePaths`.
- Produces: `ClassifyHTTPError(operation string, status int, headers http.Header, body []byte, res registry.Resolved) error`; `ErrorHint(err error) string`; `ErrorProtocol(err error) string`. `protocolhttp` (Task 4) is the only production caller. `ErrorFromHTTPStatus` stays for the old adapters.

The spec writes the signature as `ClassifyHTTPError(status, headers, body)`; `operation` keeps today's `"<operation>: <provider message>"` failure text and `res` supplies the instance, the protocol, and the caps the hints are chosen from. Record this as a plan ruling in the ledger.

- [ ] **Step 1: Write the failing tests**

`llm/classify_http_test.go`:

```go
package llm

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm/registry"
)

func chatRes(maxTokensField string) registry.Resolved {
	res := registry.Resolved{Instance: "work", Protocol: registry.ProtocolOpenAIChat, ModelID: "glm-5.2-nvfp4"}
	if maxTokensField != "" {
		res.Caps.MaxTokensField = new(maxTokensField)
	}
	return res
}

var responsesRes = registry.Resolved{Instance: "groq-responses", Protocol: registry.ProtocolOpenAIResponses, ModelID: "openai/gpt-oss-120b"}
var anthropicRes = registry.Resolved{Instance: "anthropic", Protocol: registry.ProtocolAnthropic, ModelID: "claude-opus-5"}

func TestClassifyHTTPErrorTable(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		headers   http.Header
		body      string
		res       registry.Resolved
		kind      ErrorKind
		retryable bool
		code      string
		hint      string // "" = no hint; "generic" = the generic inspect hint
	}{
		{
			name: "groq 413 TPM ceiling beats the rate-limit code", status: 413,
			body: `{"error":{"message":"Request too large for model ` + "`openai/gpt-oss-120b`" + ` in organization ` + "`org_01`" + ` service tier ` + "`on_demand`" + ` on tokens per minute (TPM): Limit 8000, Requested 12000, please reduce your message size and try again.","type":"tokens","code":"rate_limit_exceeded"}}`,
			res: responsesRes, kind: KindContextLength, code: "rate_limit_exceeded",
		},
		{
			name: "openai context_length_exceeded", status: 400,
			body: `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			res: chatRes(""), kind: KindContextLength, code: "context_length_exceeded",
		},
		{
			name: "openai chat unrecognized argument with param null names the other spelling", status: 400,
			body: `{"error":{"message":"Unrecognized request argument supplied: max_completion_tokens","type":"invalid_request_error","param":null,"code":null}}`,
			res: chatRes("max_completion_tokens"), kind: KindInvalidRequest, code: "invalid_request_error",
			hint: `set max_tokens_field = "max_tokens" on work/glm-5.2-nvfp4`,
		},
		{
			name: "openai unsupported max_tokens names max_completion_tokens", status: 400,
			body: `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`,
			res: chatRes(""), kind: KindInvalidRequest, code: "unsupported_parameter",
			hint: `set max_tokens_field = "max_completion_tokens" on work/glm-5.2-nvfp4`,
		},
		{
			name: "responses unknown_parameter in the prunable set", status: 400,
			body: `{"error":{"message":"Unknown parameter: 'store'.","type":"invalid_request_error","param":"store","code":"unknown_parameter"}}`,
			res: responsesRes, kind: KindInvalidRequest, code: "unknown_parameter",
			hint: "run `evener models inspect groq-responses/openai/gpt-oss-120b` and set fields.store = false",
		},
		{
			name: "responses unknown nested parameter gets the generic hint", status: 400,
			body: `{"error":{"message":"Unknown parameter: 'reasoning.summary'.","type":"invalid_request_error","param":"reasoning.summary","code":"unknown_parameter"}}`,
			res: responsesRes, kind: KindInvalidRequest, code: "unknown_parameter", hint: "generic",
		},
		{
			name: "groq invalid JSON body", status: 400,
			body: `{"error":{"message":"invalid JSON body","type":"invalid_request_error"}}`,
			res: responsesRes, kind: KindInvalidRequest, code: "invalid_request_error", hint: "generic",
		},
		{
			name: "anthropic prompt is too long", status: 400,
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`,
			res: anthropicRes, kind: KindContextLength, code: "invalid_request_error",
		},
		{
			name: "anthropic not supported with thinking names no parameter", status: 400,
			body: "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"`top_k` is not supported with thinking.\"}}",
			res: anthropicRes, kind: KindInvalidRequest, code: "invalid_request_error", hint: "generic",
		},
		{
			name: "openai insufficient_quota", status: 429,
			body: `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`,
			res: chatRes(""), kind: KindQuotaExceeded, code: "insufficient_quota",
		},
		{
			name: "chatgpt usage_limit_reached by code", status: 429,
			body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":3600}}`,
			res: registry.Resolved{Instance: "openai-codex", Protocol: registry.ProtocolOpenAIResponses, ModelID: "gpt-5.6"}, kind: KindQuotaExceeded, code: "usage_limit_reached",
		},
		{
			name: "usage limit by phrase on 429", status: 429,
			body: `{"error":{"type":"rate_limit_error","message":"You have hit your usage limit. Try again later."}}`,
			res: anthropicRes, kind: KindQuotaExceeded, code: "rate_limit_error",
		},
		{
			name: "rate limit with Retry-After", status: 429, headers: http.Header{"Retry-After": []string{"20"}},
			body: `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`,
			res: chatRes(""), kind: KindRateLimit, retryable: true, code: "rate_limit_exceeded",
		},
		{
			name: "rate limit with x-ratelimit-reset", status: 429, headers: http.Header{"X-Ratelimit-Reset-Requests": []string{time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)}},
			body: `{"error":{"message":"slow down","type":"rate_limit_error"}}`,
			res: chatRes(""), kind: KindRateLimit, retryable: true, code: "rate_limit_error",
		},
		{name: "401", status: 401, body: `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`, res: chatRes(""), kind: KindAuthentication, code: "invalid_api_key"},
		{name: "404 model", status: 404, body: `{"error":{"message":"The model 'nope' does not exist","type":"invalid_request_error","code":"model_not_found"}}`, res: chatRes(""), kind: KindNotFound, code: "model_not_found"},
		{name: "500", status: 500, body: `{"error":{"message":"boom","type":"server_error"}}`, res: chatRes(""), kind: KindServer, retryable: true, code: "server_error"},
		{name: "non-JSON 502", status: 502, body: `<html>Bad Gateway</html>`, res: chatRes(""), kind: KindServer, retryable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ClassifyHTTPError("op", tc.status, tc.headers, []byte(tc.body), tc.res)
			var le Error
			if !errors.As(err, &le) {
				t.Fatalf("not an llm.Error: %v", err)
			}
			if Kind(err) != tc.kind {
				t.Fatalf("kind = %v, want %v (%v)", Kind(err), tc.kind, err)
			}
			if le.Retryable() != tc.retryable {
				t.Fatalf("retryable = %v, want %v", le.Retryable(), tc.retryable)
			}
			if le.ErrorCode() != tc.code {
				t.Fatalf("code = %q, want %q", le.ErrorCode(), tc.code)
			}
			if le.Provider() != tc.res.Instance || ErrorProtocol(err) != tc.res.Protocol {
				t.Fatalf("provider/protocol = %q/%q", le.Provider(), ErrorProtocol(err))
			}
			if le.StatusCode() != tc.status {
				t.Fatalf("status = %d", le.StatusCode())
			}
			switch tc.hint {
			case "":
				if h := ErrorHint(err); h != "" {
					t.Fatalf("unexpected hint %q", h)
				}
			case "generic":
				want := "run `evener models inspect " + tc.res.Instance + "/" + tc.res.ModelID + "`; this endpoint rejected a field the registry sends; compare the pruned-field list against the provider's documentation"
				if ErrorHint(err) != want {
					t.Fatalf("hint = %q, want %q", ErrorHint(err), want)
				}
			default:
				if ErrorHint(err) != tc.hint {
					t.Fatalf("hint = %q, want %q", ErrorHint(err), tc.hint)
				}
				if !strings.Contains(err.Error(), "hint: "+tc.hint) {
					t.Fatalf("Error() must render the hint: %q", err.Error())
				}
			}
			if tc.status == 429 && tc.headers != nil {
				if le.RetryAfter() == nil || *le.RetryAfter() < 19*time.Second || *le.RetryAfter() > 31*time.Second {
					t.Fatalf("retry after = %v", le.RetryAfter())
				}
			}
			if tc.name == "chatgpt usage_limit_reached by code" {
				if _, ok := UsageLimitResetAt(err); !ok {
					t.Fatal("usage limit reset time lost")
				}
			}
		})
	}
}

func TestClassifyHTTPErrorKeepsProviderMessageVerbatim(t *testing.T) {
	err := ClassifyHTTPError("messages.create", 400, nil, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1 > 0 maximum"}}`), anthropicRes)
	if !strings.Contains(err.Error(), "prompt is too long: 1 > 0 maximum") || !strings.HasPrefix(err.Error(), "anthropic error (status=400): messages.create") {
		t.Fatalf("message = %q", err.Error())
	}
	if ErrorProtocol(errors.New("plain")) != "" || ErrorHint(errors.New("plain")) != "" {
		t.Fatal("plain errors carry no protocol or hint")
	}
}
```

The `usage_limit_reached` body shape follows `llm/usagelimit_test.go`; if that file's fixtures put `plan_type`/`resets_in_seconds` elsewhere in the object, mirror them.

`llm/classify_http_fuzz_test.go`:

```go
package llm

import (
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func FuzzClassifyHTTPError(f *testing.F) {
	f.Add(413, []byte(`{"error":{"code":"rate_limit_exceeded"}}`))
	f.Add(400, []byte(`{"error":{"message":"Unknown parameter: 'store'.","code":"unknown_parameter","param":"store"}}`))
	f.Add(429, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":10}}`))
	f.Add(400, []byte(`{"error":"a string, not an object"}`))
	f.Add(503, []byte(`not json`))
	res := registry.Resolved{Instance: "inst", Protocol: registry.ProtocolOpenAIChat, ModelID: "m"}
	f.Fuzz(func(t *testing.T, status int, body []byte) {
		err := ClassifyHTTPError("op", status, nil, body, res)
		var le Error
		if !errors.As(err, &le) {
			t.Fatalf("not an llm.Error: %v", err)
		}
		if le.StatusCode() != status || le.Provider() != "inst" || ErrorProtocol(err) != registry.ProtocolOpenAIChat {
			t.Fatalf("stamps lost: %v", err)
		}
		_ = err.Error()
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/ -run 'TestClassifyHTTPError' -count=1`
Expected: FAIL to compile (`undefined: ClassifyHTTPError`).

- [ ] **Step 3: Extend `httpBaseError` and `classifyByMessage`**

In `llm/errors.go`, add two fields to `httpBaseError` (after `behaviorTag`):

```go
	protocol    string
	hint        string
```

replace `Error()` with:

```go
// Error returns the error message in the form "<provider> error (status=<code>): <message>",
// falling back to "request failed" when the message is empty, followed by
// " (hint: <hint>)" when the classifier attached one (spec §12).
func (e *httpBaseError) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	s := fmt.Sprintf("%s error (status=%d): %s", e.provider, e.statusCode, msg)
	if e.hint != "" {
		s += " (hint: " + e.hint + ")"
	}
	return s
}
```

add after `BehaviorTag`:

```go
// Protocol returns the protocol id stamped by ClassifyHTTPError, or "".
func (e *httpBaseError) Protocol() string { return e.protocol }

// Hint returns the configuration hint attached by ClassifyHTTPError, or "".
func (e *httpBaseError) Hint() string { return e.hint }
```

and change the context-length row of `classifyByMessage` to:

```go
	case strings.Contains(lower, "context length") || strings.Contains(lower, "too many tokens") ||
		strings.Contains(lower, "maximum context") || strings.Contains(lower, "reduce the length") ||
		strings.Contains(lower, "prompt is too long"):
		return &contextLengthError{base}
```

- [ ] **Step 4: Write the classifier**

`llm/classify_http.go`:

```go
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	"primeradiant.com/evener/llm/registry"
)

// ClassifyHTTPError turns a non-2xx provider response into a typed Error
// (spec §12). Evaluation order: 413 first (Groq's per-request TPM ceiling
// arrives as 413 with code rate_limit_exceeded and recurs on retry), then
// the structured code, then the status (400 and 422 defer to the message
// rows), then the message patterns, then the generic type. operation labels
// the call in the message ("messages.create"); headers supply Retry-After
// and the x-ratelimit-reset-* delay; res names the instance and protocol
// stamped on the error and the caps the field hints are chosen from.
func ClassifyHTTPError(operation string, status int, headers http.Header, body []byte, res registry.Resolved) error {
	var raw map[string]any
	_ = json.Unmarshal(body, &raw) // a non-JSON body classifies by status alone
	now := time.Now()
	base := httpBaseError{
		provider:    res.Instance,
		protocol:    res.Protocol,
		statusCode:  status,
		message:     ProviderFailureMessage(operation, body),
		errorCode:   extractErrorCode(raw),
		retryAfter:  retryDelayFromHeaders(headers, now),
		rawResponse: raw,
	}
	code, param := errorCodeAndParam(raw)
	switch {
	case status == 413, code == "context_length_exceeded", code == "request_too_large":
		base.retryable = false
		return &contextLengthError{base}
	case code == "unknown_parameter", code == "unsupported_parameter":
		base.retryable = false
		base.hint = fieldHint(param, res)
		return &invalidRequestError{base}
	}
	if usageLimitCodes[code] {
		if limit, ok := parseUsageLimit(raw, now); ok {
			base.retryable = false
			base.message = usageLimitMessage(limit, now)
			return &quotaExceededError{httpBaseError: base, usageLimitResetsAt: limit.resetsAt}
		}
	}
	if status != 400 && status != 422 {
		// 401/403/404/408/429/5xx, including the 403 and 429 usage-limit
		// phrase checks, are today's rules unchanged.
		return errorFromHTTPStatus(base)
	}
	base.retryable = false
	if err := classifyByMessage(base); err != nil {
		return err
	}
	base.hint = fieldHint(parameterNameFromMessage(base.message), res)
	return &invalidRequestError{base}
}

// ErrorHint returns the configuration hint ClassifyHTTPError attached to an
// error (spec §12), or "" when it carries none.
func ErrorHint(err error) string {
	var h interface{ Hint() string }
	if errors.As(err, &h) {
		return h.Hint()
	}
	return ""
}

// ErrorProtocol returns the protocol id ClassifyHTTPError stamped on an
// error, or "" for errors raised outside a protocol exchange.
func ErrorProtocol(err error) string {
	var p interface{ Protocol() string }
	if errors.As(err, &p) {
		return p.Protocol()
	}
	return ""
}

func errorCodeAndParam(raw map[string]any) (code, param string) {
	errObj, ok := raw["error"].(map[string]any)
	if !ok {
		return "", ""
	}
	code, _ = errObj["code"].(string)
	if code == "" {
		code, _ = errObj["type"].(string)
	}
	param, _ = errObj["param"].(string)
	return code, param
}

// retryDelayFromHeaders honors Retry-After first, then the
// x-ratelimit-reset-* headers ParseRateLimitHeaders understands.
func retryDelayFromHeaders(h http.Header, now time.Time) *time.Duration {
	if h == nil {
		return nil
	}
	if d := ParseRetryAfter(h.Get("Retry-After"), now); d != nil {
		return d
	}
	if info := ParseRateLimitHeaders(h); info != nil && info.ResetAt != nil && info.ResetAt.After(now) {
		d := info.ResetAt.Sub(now)
		return &d
	}
	return nil
}

var parameterMessagePatterns = []*regexp.Regexp{
	regexp.MustCompile(`Unrecognized request argument supplied: ([A-Za-z0-9_.]+)`),
	regexp.MustCompile(`Unknown parameter: '([^']+)'`),
	regexp.MustCompile(`Unsupported parameter: '([^']+)'`),
	regexp.MustCompile(`(?i)unknown field ([A-Za-z0-9_.]+)`),
}

// parameterNameFromMessage extracts the rejected field from the message
// shapes of spec §12, or "" when the message names none.
func parameterNameFromMessage(message string) string {
	for _, re := range parameterMessagePatterns {
		if m := re.FindStringSubmatch(message); m != nil {
			return m[1]
		}
	}
	return ""
}

// fieldHint picks the spec §12 hint for a rejected field: the other
// max-tokens spelling when the name is the row's current one, a
// fields.<name> = false pointer when the name is prunable, else the generic
// inspect hint (a cap-governed or nested path is not a valid fields key).
func fieldHint(name string, res registry.Resolved) string {
	ref := res.Instance + "/" + res.ModelID
	if name == "" {
		return genericFieldHint(ref)
	}
	if res.Protocol == registry.ProtocolOpenAIChat {
		current := "max_tokens"
		if res.Caps.MaxTokensField != nil && *res.Caps.MaxTokensField != "" {
			current = *res.Caps.MaxTokensField
		}
		if name == current {
			other := "max_completion_tokens"
			if current == "max_completion_tokens" {
				other = "max_tokens"
			}
			return fmt.Sprintf("set max_tokens_field = %q on %s", other, ref)
		}
	}
	if slices.Contains(registry.PrunablePaths(res.Protocol), name) {
		return fmt.Sprintf("run `evener models inspect %s` and set fields.%s = false", ref, name)
	}
	return genericFieldHint(ref)
}

func genericFieldHint(ref string) string {
	return fmt.Sprintf("run `evener models inspect %s`; this endpoint rejected a field the registry sends; compare the pruned-field list against the provider's documentation", ref)
}
```

Add the fuzz row to `scripts/fuzz/fuzz-targets.txt` beside `FuzzErrorFromHTTPStatus`: `native:llm:.:FuzzClassifyHTTPError`.

- [ ] **Step 5: Run the tests and the fuzz smoke**

Run: `go test ./llm/ -run 'TestClassifyHTTPError|TestErrorFromHTTPStatus|Fuzz' -count=1 && go test ./llm/ -run FuzzClassifyHTTPError -fuzz FuzzClassifyHTTPError -fuzztime 10s && make fuzz-registry-check`
Expected: PASS (the existing `errors_test.go` table still passes: `ErrorFromHTTPStatus` is untouched apart from the new message row).

- [ ] **Step 6: Commit**

```bash
git add llm/classify_http.go llm/classify_http_test.go llm/classify_http_fuzz_test.go llm/errors.go scripts/fuzz/fuzz-targets.txt
git commit -m "feat(llm): ClassifyHTTPError with field hints and protocol stamps"
```

---

### Task 4: The shared runner `protocolhttp`

**Files:**
- Create: `llm/providers/internal/protocolhttp/call.go`, `exchange.go`, `stream.go`
- Test: `llm/providers/internal/protocolhttp/protocolhttp_test.go`

**Interfaces:**
- Consumes: `llm.AuthenticatorFor`, `llm.RequestPreparerFor`, `registry.Prune`, `registry.ApplyBodyConstants`, `transport.DoWithAPIAttempts`, `transport.APIAttemptCapture`, `llm.ClassifyHTTPError`, `llm.ApplyAdapterTimeout`, `llm.ClientWithAdapterTimeout`, `llm.APITimeoutSourceForTransport`, `llm.WrapContextError`, `llm.FinalResponseEndpointURL`, `llm.NewAPILogCredentialMaterial`, `llm.NewChanStream`, `llm.NewStreamAccumulator`.
- Produces (every protocol task uses exactly these):

```go
var DefaultClient = &http.Client{}

type Call struct {
	Operation      string            // failure-message and apilog label, e.g. "messages.create"
	EndpointFamily string            // apilog endpoint_family, e.g. "anthropic_messages"
	Method         string
	URL            string
	Body           map[string]any    // nil for GET
	Headers        map[string]string // protocol-fixed headers; set after res.Headers so the protocol wins
	Req            llm.Request
	Res            registry.Resolved
	Client         *http.Client      // nil → DefaultClient
	Reclassify     func(status int, body []byte, err error) error // optional post-classification (google's gRPC status remap)
}
type Prepared struct { Request *http.Request; Body []byte; PrunedFields []string }
type Result struct { StatusCode int; Header http.Header; Body []byte; Raw map[string]any; EndpointURL string; Material llm.APILogCredentialMaterial; PrunedFields []string }
type StreamDecoder func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *Result, attempt *transport.APIAttemptCapture)

func Prepare(ctx context.Context, c *Call) (*Prepared, error)
func Do(ctx context.Context, c *Call, finish func(r *Result) (*llm.Response, error)) error
func Stream(ctx context.Context, c *Call, decode StreamDecoder) (llm.Stream, error)
func CompleteViaStream(ctx context.Context, instance string, open func(context.Context) (llm.Stream, error)) (llm.Response, error)
func RequiresStreamingComplete(res registry.Resolved) bool
func URL(res registry.Resolved, template string) string
func ModelInBody(res registry.Resolved) bool
```

- [ ] **Step 1: Write the failing tests**

`llm/providers/internal/protocolhttp/protocolhttp_test.go`:

```go
package protocolhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// renamingPreparer is the test double for the Codex transport: it asserts
// the authenticator ran first and renames a field, proving the spec §8.2
// order build → prune → constants → auth → prepare.
type renamingPreparer struct{ sawAuth *bool }

func (p renamingPreparer) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
	return nil
}
func (p renamingPreparer) PrepareRequest(_ context.Context, req *http.Request, body map[string]any, _ llm.Request, _ registry.Resolved) error {
	*p.sawAuth = req.Header.Get("Authorization") != ""
	if v, ok := body["store"]; ok {
		body["store_renamed"] = v
		delete(body, "store")
	}
	req.Header.Set("x-prepared", "yes")
	return nil
}
func (renamingPreparer) RequiresStreamingComplete() bool { return true }

var registerOnce sync.Once
var preparerSawAuth bool

func registerTestSchemes() {
	registerOnce.Do(func() {
		llm.RegisterAuthenticator("test-runner-preparer", renamingPreparer{sawAuth: &preparerSawAuth})
	})
}

func testRes(baseURL, auth string) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses)}
	caps.Fields["metadata"] = false
	caps.Fields["store"] = true
	return registry.Resolved{
		Instance: "inst", Protocol: registry.ProtocolOpenAIResponses, ModelID: "m", WireID: "m-wire",
		Transport: registry.Transport{Auth: auth, BaseURL: baseURL, Endpoint: "/responses", StreamEndpoint: "/responses", Body: map[string]any{"reasoning.context": "all_turns"}},
		Headers: map[string]string{"X-Instance": "1"}, CredentialHeaders: map[string]string{"X-Gateway-Key": "gw-secret"},
		Credential: registry.Credential{Value: "key-secret", Source: "api_key"}, Caps: caps,
	}
}

type captureSink struct {
	mu       sync.Mutex
	attempts []apilog.APIAttemptRecord
}

func (s *captureSink) AppendAttempt(_ context.Context, r apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, r)
	return nil
}
func (s *captureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error { return nil }

func TestPrepareRunsPruneConstantsAuthPrepareInOrder(t *testing.T) {
	registerTestSchemes()
	res := testRes("https://api.example.test/v1", "test-runner-preparer")
	body := map[string]any{"metadata": map[string]string{"a": "b"}, "store": false, "reasoning": map[string]any{"effort": "high"}}
	p, err := Prepare(context.Background(), &Call{Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: body, Headers: map[string]string{"X-Instance": "protocol", "anthropic-version": "v"}, Res: res})
	if err != nil {
		t.Fatal(err)
	}
	if !preparerSawAuth {
		t.Fatal("preparer ran before the authenticator")
	}
	if got := strings.Join(p.PrunedFields, ","); got != "metadata" {
		t.Fatalf("pruned = %q", got)
	}
	var sent map[string]any
	_ = json.Unmarshal(p.Body, &sent)
	if _, still := sent["store"]; still || sent["store_renamed"] != false || sent["reasoning"].(map[string]any)["context"] != "all_turns" {
		t.Fatalf("body = %s", p.Body)
	}
	h := p.Request.Header
	if h.Get("Authorization") != "Bearer key-secret" || h.Get("X-Gateway-Key") != "gw-secret" || h.Get("X-Instance") != "protocol" || h.Get("anthropic-version") != "v" || h.Get("Content-Type") != "application/json" || h.Get("x-prepared") != "yes" {
		t.Fatalf("headers = %v", h)
	}
	if p.Request.URL.String() != "https://api.example.test/v1/responses" || p.Request.GetBody == nil || p.Request.ContentLength != int64(len(p.Body)) {
		t.Fatalf("request = %+v", p.Request)
	}
	if _, err := Prepare(context.Background(), &Call{Method: http.MethodPost, URL: "https://x", Body: map[string]any{}, Res: registry.Resolved{Instance: "i", Transport: registry.Transport{Auth: "no-such-scheme"}}}); err == nil {
		t.Fatal("unknown scheme must fail")
	}
}

func TestURLAndModelInBody(t *testing.T) {
	res := registry.Resolved{WireID: "models/x y", Transport: registry.Transport{BaseURL: "https://h/v1/", Endpoint: "/publishers/anthropic/models/{model}:rawPredict"}}
	if got := URL(res, res.Transport.Endpoint); got != "https://h/v1/publishers/anthropic/models/models%2Fx%20y:rawPredict" {
		t.Fatalf("URL = %q", got)
	}
	if ModelInBody(res) {
		t.Fatal("a {model} endpoint must not send model in the body")
	}
	if !ModelInBody(registry.Resolved{Transport: registry.Transport{Endpoint: "/messages"}}) {
		t.Fatal("plain endpoints send model in the body")
	}
}

func TestDoDecodesStampsAndLogs(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("x-ratelimit-remaining-requests", "7")
		_, _ = w.Write([]byte(`{"id":"r1"}`))
	}))
	defer srv.Close()
	res := testRes(srv.URL, registry.AuthBearer)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_do")), sink)
	var out llm.Response
	err := Do(ctx, &Call{Operation: "responses.create", EndpointFamily: "test", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{"metadata": map[string]string{"k": "v"}, "input": "hi"}, Req: llm.Request{Model: "m"}, Res: res, Client: srv.Client()}, func(r *Result) (*llm.Response, error) {
		if r.Raw["id"] != "r1" || r.StatusCode != 200 || r.Header.Get("x-ratelimit-remaining-requests") != "7" || !strings.HasSuffix(r.EndpointURL, "/responses") {
			t.Fatalf("result = %+v", r)
		}
		out = llm.Response{Model: "m"}
		return &out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "inst" {
		t.Fatalf("provider = %q", out.Provider)
	}
	if strings.Contains(string(gotBody), "metadata") || !strings.Contains(string(gotBody), `"reasoning":{"context":"all_turns"}`) {
		t.Fatalf("wire body = %s", gotBody)
	}
	llm.WaitForPriorAPIAttempts(ctx)
	if len(sink.attempts) != 1 {
		t.Fatalf("attempts = %d", len(sink.attempts))
	}
	rec := sink.attempts[0]
	if rec.ProviderInstance != "inst" || strings.Join(rec.Request.PrunedFields, ",") != "metadata" || *rec.Response.StatusCode != 200 {
		t.Fatalf("record = %+v", rec)
	}
	raw, _ := json.Marshal(rec)
	if strings.Contains(string(raw), "key-secret") || strings.Contains(string(raw), "gw-secret") {
		t.Fatalf("credential leaked into the attempt record: %s", raw)
	}
}

func TestDoClassifiesFailuresAndReclassifies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'store'.","code":"unknown_parameter","param":"store"}}`))
	}))
	defer srv.Close()
	res := testRes(srv.URL, registry.AuthBearer)
	call := &Call{Operation: "responses.create", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{}, Res: res, Client: srv.Client()}
	err := Do(context.Background(), call, func(*Result) (*llm.Response, error) { t.Fatal("finish must not run on 4xx"); return nil, nil })
	if llm.Kind(err) != llm.KindInvalidRequest || !strings.Contains(llm.ErrorHint(err), "fields.store = false") {
		t.Fatalf("err = %v", err)
	}
	marker := errors.New("reclassified")
	call.Reclassify = func(status int, body []byte, err error) error {
		if status != 400 || !strings.Contains(string(body), "unknown_parameter") || err == nil {
			t.Fatalf("reclassify inputs: %d %s %v", status, body, err)
		}
		return marker
	}
	if err := Do(context.Background(), call, nil); !errors.Is(err, marker) {
		t.Fatalf("reclassify not applied: %v", err)
	}
	srv.Close()
	if err := Do(context.Background(), call, nil); err == nil {
		t.Fatal("transport failure must surface")
	}
}

func TestStreamPublishesStartThenHandsOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "fail") {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"x\":1}\n\n"))
	}))
	defer srv.Close()
	res := testRes(srv.URL, registry.AuthBearer)
	call := &Call{Operation: "responses.create(stream)", Method: http.MethodPost, URL: srv.URL + "/fail", Body: map[string]any{"stream": true}, Res: res, Client: srv.Client()}
	if _, err := Stream(context.Background(), call, nil); llm.Kind(err) != llm.KindRateLimit {
		t.Fatalf("err = %v", err)
	}
	call.URL = srv.URL + "/ok"
	s, err := Stream(context.Background(), call, func(_ context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *Result, attempt *transport.APIAttemptCapture) {
		defer cancel()
		defer func() { _ = resp.Body.Close() }()
		defer s.CloseSend()
		data, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(data), `{"x":1}`) || r.EndpointURL == "" || attempt == nil {
			t.Errorf("decoder inputs: %q %q", data, r.EndpointURL)
		}
		if len(r.Material.HeaderNames) == 0 {
			t.Errorf("material must name the auth header")
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: 200}, llm.APITimeoutNone, nil, nil)
		s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &llm.Response{Provider: "inst"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	var types []llm.StreamEventType
	for ev := range s.Events() {
		types = append(types, ev.Type)
	}
	if len(types) < 2 || types[0] != llm.StreamEventStreamStart || types[len(types)-1] != llm.StreamEventFinish {
		t.Fatalf("events = %v", types)
	}
}
```

Add:

```go
func TestCompleteViaStreamAccumulates(t *testing.T) {
	open := func(context.Context) (llm.Stream, error) {
		s := llm.NewChanStream(func() {})
		go func() {
			s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "hi", TextID: "t1"})
			s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &llm.Response{Provider: "inst", Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}}})
			s.CloseSend()
		}()
		return s, nil
	}
	resp, err := CompleteViaStream(context.Background(), "inst", open)
	if err != nil || resp.Text() != "hi" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	failing := func(context.Context) (llm.Stream, error) { return nil, errors.New("open failed") }
	if _, err := CompleteViaStream(context.Background(), "inst", failing); err == nil {
		t.Fatal("open error must surface")
	}
	registerTestSchemes()
	if !RequiresStreamingComplete(registry.Resolved{Transport: registry.Transport{Auth: "test-runner-preparer"}}) || RequiresStreamingComplete(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthBearer}}) {
		t.Fatal("RequiresStreamingComplete must follow the scheme's preparer")
	}
}
```

(`llm.StreamEventTextDelta`, `TextID`, `Delta`, `CloseSend`, `Response.Text()` are the existing names from `llm/stream.go`, `llm/chan_stream.go`, `llm/types.go`.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/internal/protocolhttp/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Write the runner**

`llm/providers/internal/protocolhttp/call.go`:

```go
// Package protocolhttp is the HTTP plumbing shared by the protocol packages:
// it turns a built body and a Resolved record into a wire request in the
// spec §8.2 order (prune, body constants, authenticator, request preparer),
// executes it with API-attempt logging, and classifies failures. Protocol
// packages own only the body shape and the response decoding.
package protocolhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// DefaultClient serves protocols whose Client is nil.
var DefaultClient = &http.Client{}

// Call describes one HTTP exchange for a Resolved record.
type Call struct {
	// Operation labels the call in failure messages, e.g. "messages.create".
	Operation string
	// EndpointFamily is the apilog endpoint_family, e.g. "anthropic_messages".
	EndpointFamily string
	Method         string
	URL            string
	// Body is the built body; nil for GET.
	Body map[string]any
	// Headers are the protocol's fixed headers (anthropic-version, session
	// affinity); they are set after res.Headers so the protocol wins.
	Headers map[string]string
	Req     llm.Request
	Res     registry.Resolved
	// Client is nil for DefaultClient.
	Client *http.Client
	// Reclassify, when set, post-processes the classified error of a non-2xx
	// response (google's gRPC status remap); it receives the body and the
	// error ClassifyHTTPError produced.
	Reclassify func(status int, body []byte, err error) error
}

// Prepared is a Call after prune → constants → auth → prepare.
type Prepared struct {
	Request      *http.Request
	Body         []byte
	PrunedFields []string
	material     llm.APILogCredentialMaterial
}

// Prepare assembles the wire request without sending it (spec §8.2 steps
// 2–4): prune by Fields, apply Transport.Body constants, set the layered
// headers, run the authenticator, then the request preparer, and marshal
// the final body.
func Prepare(ctx context.Context, c *Call) (*Prepared, error) {
	var pruned []string
	if c.Body != nil {
		pruned = registry.Prune(c.Body, c.Res.Caps)
		registry.ApplyBodyConstants(c.Body, c.Res.Transport.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, c.Method, c.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.Res.Headers {
		httpReq.Header.Set(k, v)
	}
	for k, v := range c.Res.CredentialHeaders {
		httpReq.Header.Set(k, v)
	}
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}
	if c.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	auth, ok := llm.AuthenticatorFor(c.Res.Transport.Auth)
	if !ok {
		return nil, &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: no authenticator for auth scheme %q", c.Res.Instance, c.Res.Transport.Auth)}
	}
	if err := auth.Apply(ctx, httpReq, c.Res); err != nil {
		return nil, err
	}
	if preparer, ok := auth.(llm.RequestPreparer); ok && c.Body != nil {
		if err := preparer.PrepareRequest(ctx, httpReq, c.Body, c.Req, c.Res); err != nil {
			return nil, err
		}
	}
	p := &Prepared{Request: httpReq, PrunedFields: pruned}
	if c.Body != nil {
		b, err := json.Marshal(c.Body)
		if err != nil {
			return nil, err
		}
		p.Body = b
		httpReq.Body = io.NopCloser(bytes.NewReader(b))
		httpReq.ContentLength = int64(len(b))
		httpReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	}
	p.material = credentialMaterial(c.Res, httpReq)
	return p, nil
}

// URL joins the resolved base URL with an endpoint template, substituting
// {model} with the URL-escaped wire id (spec §9.1). Callers check for
// registry.EndpointUnsupported first.
func URL(res registry.Resolved, template string) string {
	path := strings.ReplaceAll(template, "{model}", url.PathEscape(res.WireID))
	return strings.TrimRight(res.Transport.BaseURL, "/") + path
}

// ModelInBody reports whether the completion path lacks {model}, in which
// case the body carries the wire id (spec §9.1).
func ModelInBody(res registry.Resolved) bool {
	return !strings.Contains(res.Transport.Endpoint, "{model}")
}

// authHeaderName is the header the scheme writes the credential to.
func authHeaderName(res registry.Resolved) string {
	if res.Transport.Auth == registry.AuthHeader && res.Transport.AuthHeader != "" {
		return res.Transport.AuthHeader
	}
	return "Authorization"
}

// credentialMaterial names every header that carries a credential and
// every value that must never reach a log: the resolved credential, the
// credential headers, and the value the authenticator wrote (a Codex or
// ADC bearer token is not res.Credential.Value).
func credentialMaterial(res registry.Resolved, httpReq *http.Request) llm.APILogCredentialMaterial {
	header := authHeaderName(res)
	names := []string{header}
	values := []string{res.Credential.Value}
	if httpReq != nil {
		if v := httpReq.Header.Get(header); v != "" {
			values = append(values, v, strings.TrimPrefix(v, "Bearer "))
		}
	}
	for name, value := range res.CredentialHeaders {
		names = append(names, name)
		values = append(values, value)
	}
	return llm.NewAPILogCredentialMaterial(names, nil, values...)
}

func (c *Call) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return DefaultClient
}

func (c *Call) metaBuilder(p *Prepared) transport.APIAttemptMetaBuilder {
	return func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   c.Res.Instance,
			RequestModel:       c.Req.Model,
			HistoryMode:        c.Req.HistoryMode,
			EndpointFamily:     c.EndpointFamily,
			RequestBody:        requestBody,
			PrunedFields:       p.PrunedFields,
			CredentialMaterial: credentialMaterial(c.Res, wireRequest),
		}
	}
}

// classify turns a non-2xx response into the typed error, applying the
// call's Reclassify hook when present.
func (c *Call) classify(status int, headers http.Header, body []byte) error {
	err := llm.ClassifyHTTPError(c.Operation, status, headers, body, c.Res)
	if c.Reclassify != nil {
		err = c.Reclassify(status, body, err)
	}
	return err
}
```

`llm/providers/internal/protocolhttp/exchange.go`:

```go
package protocolhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// Result is a completed 2xx exchange handed to the protocol's decoder.
type Result struct {
	StatusCode int
	Header     http.Header
	// Body is the full response body of a non-streaming exchange; nil for
	// a stream, whose body the decoder reads live.
	Body []byte
	// Raw is Body decoded as a JSON object, nil when it is not one.
	Raw map[string]any
	// EndpointURL is the final URL after redirects, for llm.StampEndpointURL.
	EndpointURL  string
	Material     llm.APILogCredentialMaterial
	PrunedFields []string
}

// Do performs a non-streaming exchange. finish decodes a 2xx Result into
// the caller's value and returns the *llm.Response (nil for listings and
// token counts) that completes the API-attempt record; it is not called for
// a non-2xx response, which is classified and returned. A finished
// Response carries the instance name as its Provider.
func Do(parentCtx context.Context, c *Call, finish func(r *Result) (*llm.Response, error)) (err error) {
	ctx, cancel := llm.ApplyAdapterTimeout(parentCtx, c.Req.AdapterTimeout, false)
	defer cancel()
	p, err := Prepare(ctx, c)
	if err != nil {
		return err
	}
	var (
		statusCode   int
		responseBody []byte
		decodeErr    error
		transportErr error
		attempt      *transport.APIAttemptCapture
		response     *llm.Response
	)
	defer func() {
		attemptErr := err
		if attemptErr == nil {
			attemptErr = decodeErr
		}
		attempt.Complete(llm.APIAttemptResult{
			StatusCode:   statusCode,
			ResponseBody: responseBody,
			Response:     response,
			Err:          attemptErr,
		}, llm.APITimeoutSourceForTransport(parentCtx, ctx, transportErr), decodeErr, transportErr)
	}()
	client := llm.ClientWithAdapterTimeout(c.httpClient(), c.Req.AdapterTimeout)
	resp, att, doErr := transport.DoWithAPIAttempts(parentCtx, client, p.Request, c.metaBuilder(p))
	attempt = att
	if doErr != nil {
		transportErr = doErr
		return llm.WrapContextError(c.Res.Instance, doErr)
	}
	statusCode = resp.StatusCode
	defer func() { _ = resp.Body.Close() }()
	rawBytes, readErr := io.ReadAll(resp.Body)
	responseBody = rawBytes
	var raw map[string]any
	jsonErr := json.Unmarshal(rawBytes, &raw)
	if readErr != nil {
		decodeErr = readErr
	} else {
		decodeErr = jsonErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.classify(resp.StatusCode, resp.Header, rawBytes)
	}
	if finish == nil {
		return nil
	}
	r := &Result{StatusCode: resp.StatusCode, Header: resp.Header, Body: rawBytes, Raw: raw, EndpointURL: llm.FinalResponseEndpointURL(resp, c.URL), Material: p.material, PrunedFields: p.PrunedFields}
	response, err = finish(r)
	if err != nil {
		response = nil
		return err
	}
	if response != nil {
		response.Provider = c.Res.Instance
	}
	return nil
}

// CompleteViaStream runs a streaming exchange to completion and returns the
// accumulated Response; the Codex backend answers every request as a stream
// (spec §9.5, RequiresStreamingComplete).
func CompleteViaStream(ctx context.Context, instance string, open func(context.Context) (llm.Stream, error)) (llm.Response, error) {
	stream, err := open(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	defer func() { _ = stream.Close() }()
	acc := llm.NewStreamAccumulator()
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			if ev.Err != nil {
				return llm.Response{}, ev.Err
			}
			return llm.Response{}, fmt.Errorf("%s stream failed", instance)
		}
		acc.Process(ev)
	}
	resp := acc.Response()
	if resp == nil {
		return llm.Response{}, errors.New(instance + " stream completed without final response")
	}
	return *resp, nil
}

// RequiresStreamingComplete reports whether the instance's transport
// answers Complete through Stream (spec §8.1 RequestPreparer).
func RequiresStreamingComplete(res registry.Resolved) bool {
	p, ok := llm.RequestPreparerFor(res.Transport.Auth)
	return ok && p.RequiresStreamingComplete()
}
```

`llm/providers/internal/protocolhttp/stream.go`:

```go
package protocolhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
)

// StreamDecoder consumes the live SSE response in its own goroutine and owns
// closing resp.Body and s, completing attempt, and calling cancel when done
// (the contract of today's decodeStream functions).
type StreamDecoder func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *Result, attempt *transport.APIAttemptCapture)

// Stream performs a streaming exchange: a 2xx response is handed to decode
// after STREAM_START is published; a non-2xx response is classified and
// returned and never reaches decode.
func Stream(parentCtx context.Context, c *Call, decode StreamDecoder) (llm.Stream, error) {
	sctx, cancel := context.WithCancel(parentCtx)
	sctx, timeoutCancel := llm.ApplyAdapterTimeout(sctx, c.Req.AdapterTimeout, true)
	cancelAll := func() {
		cancel()
		timeoutCancel()
	}
	p, err := Prepare(sctx, c)
	if err != nil {
		cancelAll()
		return nil, err
	}
	client := llm.ClientWithAdapterTimeout(c.httpClient(), c.Req.AdapterTimeout)
	resp, attempt, err := transport.DoWithAPIAttempts(parentCtx, client, p.Request, c.metaBuilder(p))
	if err != nil {
		returned := llm.WrapContextError(c.Res.Instance, err)
		attempt.Complete(llm.APIAttemptResult{Err: returned}, llm.APITimeoutSourceForTransport(parentCtx, sctx, err), nil, err)
		cancelAll()
		return nil, returned
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, readErr := io.ReadAll(resp.Body)
		returned := c.classify(resp.StatusCode, resp.Header, rawBytes)
		var raw map[string]any
		decodeErr := json.Unmarshal(rawBytes, &raw)
		if readErr != nil {
			decodeErr = readErr
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, ResponseBody: rawBytes, Err: returned}, llm.APITimeoutNone, decodeErr, nil)
		cancelAll()
		return nil, returned
	}
	s := llm.NewChanStream(cancelAll)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
	r := &Result{StatusCode: resp.StatusCode, Header: resp.Header, EndpointURL: llm.FinalResponseEndpointURL(resp, c.URL), Material: p.material, PrunedFields: p.PrunedFields}
	go decode(sctx, cancelAll, resp, s, r, attempt)
	return s, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./llm/providers/internal/protocolhttp/ -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add llm/providers/internal/protocolhttp
git commit -m "feat(providers): protocolhttp shared runner for Resolved-driven protocols"
```

---

### Task 5: `tokenauth` (gcp-adc and the Codex transport) and the shared `StrictifyJSONSchema`

**Files:**
- Modify: `llm/go.mod` (`golang.org/x/oauth2 v0.35.0` direct), `llm/go.sum`, `go.work.sum` if `go` touches it
- Create: `llm/providers/internal/openaichat/strict.go` (moved from `llm/providers/openai/responses.go:888-970`), `llm/providers/internal/openaichat/strict_test.go`
- Modify: `llm/providers/openai/responses.go:888-970` (replace the three functions with one-line delegations so the old package and its tests keep working)
- Create: `llm/providers/tokenauth/tokenauth.go`, `gcpadc.go`, `codex.go`
- Test: `llm/providers/tokenauth/gcpadc_test.go`, `llm/providers/tokenauth/codex_test.go`

**Interfaces:**
- Consumes: `llm.RegisterAuthenticator`, `registry.AuthGCPADC`, `registry.AuthOAuthOpenAICodex`, `registry.Resolved.Credential.Source` (`"oauth"`/`"adc"` from `llm/registry/instances.go:98-107`), `authopenai.NewService(authopenai.DefaultConfig(), nil).ResolveRuntimeCredentials(ctx, stateDir, instance)`, `authopenai.LoadAuth`, `authopenai.ParseIDTokenClaims`, `authopenai.DefaultStateDir`, `authopenai.ErrLoginRequired`, `golang.org/x/oauth2/google.FindDefaultCredentials`, `oauth2.ReuseTokenSource`.
- Produces: `openaichat.StrictifyJSONSchema(in map[string]any) map[string]any`; `tokenauth.GCPADC{FindCredentials}` and `tokenauth.Codex{StateDir, Credentials}` implementing `llm.Authenticator` (Codex also `llm.RequestPreparer`); exported registered instances `tokenauth.DefaultGCPADC` and `tokenauth.DefaultCodex` (the wire-capture harness sets their seams); `tokenauth.ClientVersion`.

- [ ] **Step 1: Move `strictifyJSONSchema` into `openaichat`**

Create `llm/providers/internal/openaichat/strict.go` by moving `strictifyJSONSchema`, `strictifyJSONSchemaInPlace`, and `deepCopyAny` from `llm/providers/openai/responses.go:888-970` verbatim, renaming the entry point to `StrictifyJSONSchema` with this doc comment:

```go
// StrictifyJSONSchema returns a deep copy of a tool parameter schema
// rewritten for OpenAI strict mode: every object gets
// additionalProperties: false, a properties map, and required = every
// property (sorted); arrays and anyOf/oneOf/allOf recurse. It runs only
// when Caps.StrictTools is set (spec §8.2), because the rewrite is not
// reversible by pruning strict afterwards.
```

In `llm/providers/openai/responses.go` replace the moved bodies with:

```go
func strictifyJSONSchema(in map[string]any) map[string]any { return openaichat.StrictifyJSONSchema(in) }
```

and delete `strictifyJSONSchemaInPlace`/`deepCopyAny` there (the openai package already imports `openaichat`). Write `strict_test.go`:

```go
package openaichat

import (
	"reflect"
	"testing"
)

func TestStrictifyJSONSchemaRewritesObjectsWithoutMutatingInput(t *testing.T) {
	in := map[string]any{"type": "object", "properties": map[string]any{
		"b": map[string]any{"type": "string"},
		"a": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "integer"}}}},
	}}
	out := StrictifyJSONSchema(in)
	if out["additionalProperties"] != false {
		t.Fatalf("out = %#v", out)
	}
	if req := out["required"]; !reflect.DeepEqual(req, []any{"a", "b"}) && !reflect.DeepEqual(req, []string{"a", "b"}) {
		t.Fatalf("required = %#v", req)
	}
	items := out["properties"].(map[string]any)["a"].(map[string]any)["items"].(map[string]any)
	if items["additionalProperties"] != false {
		t.Fatalf("nested object not strictified: %#v", items)
	}
	if _, touched := in["additionalProperties"]; touched {
		t.Fatal("input mutated")
	}
	if !reflect.DeepEqual(StrictifyJSONSchema(out), out) {
		t.Fatal("not idempotent")
	}
}
```

(Match the `required` element type to what the moved code emits; the existing openai tests pin it.)

Run: `go test ./llm/providers/internal/openaichat/ ./llm/providers/openai/ -count=1` — PASS. Commit:

```bash
git add llm/providers/internal/openaichat/strict.go llm/providers/internal/openaichat/strict_test.go llm/providers/openai/responses.go
git commit -m "refactor(openaichat): share StrictifyJSONSchema with the new protocol packages"
```

- [ ] **Step 2: Add the oauth2 dependency**

Run:

```bash
cd llm && go get golang.org/x/oauth2@v0.35.0 && go mod tidy && cd .. && go build ./... && git status --short
```

Expected: `llm/go.mod` lists `golang.org/x/oauth2 v0.35.0` in the direct `require` block (and `cloud.google.com/go/compute/metadata` indirect); the build is green. Stage `llm/go.mod`, `llm/go.sum`, and `go.work.sum` if it changed. Do not run `go work sync`.

- [ ] **Step 3: Write the failing tests**

`llm/providers/tokenauth/gcpadc_test.go`:

```go
package tokenauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestGCPADCAppliesTokenOncePerInstance(t *testing.T) {
	calls := 0
	a := &GCPADC{FindCredentials: func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		calls++
		if len(scopes) != 1 || scopes[0] != cloudPlatformScope {
			t.Fatalf("scopes = %v", scopes)
		}
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"})}, nil
	}}
	res := registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}}
	for range 2 {
		req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
		if err := a.Apply(context.Background(), req, res); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer adc-token" {
			t.Fatalf("Authorization = %q", got)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	_ = a.Apply(context.Background(), req, registry.Resolved{Instance: "google-vertex", Credential: registry.Credential{Source: "adc"}})
	if calls != 2 {
		t.Fatalf("FindDefaultCredentials called %d times, want once per instance", calls)
	}
}

func TestGCPADCReportsMissingCredentials(t *testing.T) {
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return nil, errors.New("could not find default credentials")
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, registry.Resolved{Instance: "vertex"})
	var cfg *llm.ConfigurationError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "vertex") || !strings.Contains(err.Error(), "default credentials") {
		t.Fatalf("err = %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("no header on failure")
	}
}

func TestDefaultsAreRegistered(t *testing.T) {
	if a, ok := llm.AuthenticatorFor(registry.AuthGCPADC); !ok || a != llm.Authenticator(DefaultGCPADC) {
		t.Fatal("gcp-adc not registered as DefaultGCPADC")
	}
	if p, ok := llm.RequestPreparerFor(registry.AuthOAuthOpenAICodex); !ok || !p.RequiresStreamingComplete() {
		t.Fatal("oauth-openai-codex not registered as a streaming-complete preparer")
	}
}
```

`llm/providers/tokenauth/codex_test.go`:

```go
package tokenauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func codexState(t *testing.T, instance, accountID string) string {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	rec := authopenai.AuthRecord{Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth, ObtainedAt: now, TokenType: "Bearer", AccessToken: "stale", RefreshToken: "rt", Expiry: now.Add(time.Hour), AccountID: accountID}
	if err := authopenai.SaveAuth(dir, instance, rec); err != nil {
		t.Fatal(err)
	}
	return dir
}

func codexRes(instance string) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses), ResponsesLite: new(true)}
	return registry.Resolved{Instance: instance, Protocol: registry.ProtocolOpenAIResponses, Credential: registry.Credential{Source: "oauth"}, Transport: registry.Transport{Auth: registry.AuthOAuthOpenAICodex}, Caps: caps}
}

func TestCodexApplySetsEveryRequestHeader(t *testing.T) {
	dir := codexState(t, "openai-codex", "acct_123")
	var gotDir, gotInstance string
	c := &Codex{StateDir: dir, Credentials: func(_ context.Context, stateDir, instance string) (authopenai.RuntimeCredentials, error) {
		gotDir, gotInstance = stateDir, instance
		return authopenai.RuntimeCredentials{BearerToken: "fresh-token", Source: authopenai.AuthSourceOAuth}, nil
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	req.Header.Set("User-Agent", "custom/1")
	if err := c.Apply(context.Background(), req, codexRes("openai-codex")); err != nil {
		t.Fatal(err)
	}
	if gotDir != dir || gotInstance != "openai-codex" {
		t.Fatalf("credentials asked for %q/%q", gotDir, gotInstance)
	}
	h := req.Header
	if h.Get("Authorization") != "Bearer fresh-token" || h.Get("ChatGPT-Account-ID") != "acct_123" || h.Get("originator") != "evener" || h.Get("User-Agent") != "custom/1" {
		t.Fatalf("headers = %v", h)
	}
	req2, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	_ = c.Apply(context.Background(), req2, codexRes("openai-codex"))
	if ua := req2.Header.Get("User-Agent"); !strings.HasPrefix(ua, "evener/dev (") {
		t.Fatalf("default User-Agent = %q", ua)
	}
}

func TestCodexApplyRequiresLogin(t *testing.T) {
	c := &Codex{StateDir: t.TempDir(), Credentials: func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		t.Fatal("credentials must not be resolved without an oauth credential source")
		return authopenai.RuntimeCredentials{}, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	res := codexRes("openai-codex")
	res.Credential = registry.Credential{Source: "none"}
	err := c.Apply(context.Background(), req, res)
	var cfg *llm.ConfigurationError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "evener openai login --instance openai-codex") {
		t.Fatalf("err = %v", err)
	}
	c.Credentials = func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		return authopenai.RuntimeCredentials{}, authopenai.ErrLoginRequired
	}
	err = c.Apply(context.Background(), req, codexRes("openai-codex"))
	if !errors.As(err, &cfg) || !errors.Is(err, authopenai.ErrLoginRequired) {
		t.Fatalf("expired login must be a configuration error wrapping ErrLoginRequired: %v", err)
	}
}

func TestCodexPrepareRequest(t *testing.T) {
	c := &Codex{}
	res := codexRes("openai-codex")
	res.Caps.Fields["metadata"] = true
	req := llm.Request{SessionID: " sess-1 ", ThreadID: "thread-1", ClientMetadata: map[string]string{"installation_id": "inst-9"}}
	body := map[string]any{"metadata": map[string]string{"trace": "t"}, "input": "x"}
	httpReq, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := c.PrepareRequest(context.Background(), httpReq, body, req, res); err != nil {
		t.Fatal(err)
	}
	h := httpReq.Header
	if h.Get("x-openai-internal-codex-responses-lite") != "true" || h.Get("session-id") != "sess-1" || h.Get("thread-id") != "thread-1" || h.Get("x-client-request-id") != "thread-1" {
		t.Fatalf("headers = %v", h)
	}
	if _, still := body["metadata"]; still {
		t.Fatal("metadata must be deleted")
	}
	if got := body["client_metadata"].(map[string]string); got["trace"] != "t" || got["installation_id"] != "inst-9" {
		t.Fatalf("client_metadata = %v", got)
	}

	off := codexRes("openai-codex")
	off.Caps.Fields["metadata"] = false
	off.Caps.ResponsesLite = nil
	body = map[string]any{"metadata": map[string]string{"trace": "t"}}
	httpReq, _ = http.NewRequest(http.MethodPost, "https://x", nil)
	_ = c.PrepareRequest(context.Background(), httpReq, body, req, off)
	if _, has := body["client_metadata"]; has || body["metadata"] != nil || httpReq.Header.Get("x-openai-internal-codex-responses-lite") != "" {
		t.Fatalf("metadata off: body = %v headers = %v", body, httpReq.Header)
	}

	empty := map[string]any{}
	_ = c.PrepareRequest(context.Background(), httpReq, empty, llm.Request{}, res)
	if _, has := empty["client_metadata"]; has {
		t.Fatal("an empty merge sends no client_metadata")
	}
	if !c.RequiresStreamingComplete() {
		t.Fatal("Codex answers Complete through Stream")
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./llm/providers/tokenauth/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 5: Write the package**

`llm/providers/tokenauth/tokenauth.go`:

```go
// Package tokenauth holds the two auth schemes that mint bearer tokens from
// a local credential store instead of sending a configured key: gcp-adc
// (Google application-default credentials) and oauth-openai-codex (the
// per-instance OAuth record written by `evener openai login`). The four
// trivial schemes live in package llm.
package tokenauth

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// DefaultGCPADC and DefaultCodex are the registered instances; tests that
// drive real protocol code through these schemes set their seams.
var (
	DefaultGCPADC = &GCPADC{}
	DefaultCodex  = &Codex{}
)

func init() {
	llm.RegisterAuthenticator(registry.AuthGCPADC, DefaultGCPADC)
	llm.RegisterAuthenticator(registry.AuthOAuthOpenAICodex, DefaultCodex)
}
```

`llm/providers/tokenauth/gcpadc.go`:

```go
package tokenauth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// GCPADC sends a bearer token from Google application-default credentials
// (spec §8.1): the credentials are looked up at an instance's first
// request, never at load, and the token source refreshes itself.
type GCPADC struct {
	// FindCredentials is the lookup seam; nil means google.FindDefaultCredentials.
	FindCredentials func(ctx context.Context, scopes ...string) (*google.Credentials, error)

	mu      sync.Mutex
	sources map[string]oauth2.TokenSource
}

// Apply sets Authorization from the instance's cached token source.
func (a *GCPADC) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	ts, err := a.tokenSource(ctx, res.Instance)
	if err != nil {
		return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: application-default credentials: %v (run `gcloud auth application-default login` or set GOOGLE_APPLICATION_CREDENTIALS)", res.Instance, err), Cause: err}
	}
	tok, err := ts.Token()
	if err != nil {
		return fmt.Errorf("instance %q: gcp-adc token: %w", res.Instance, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return nil
}

func (a *GCPADC) tokenSource(ctx context.Context, instance string) (oauth2.TokenSource, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ts, ok := a.sources[instance]; ok {
		return ts, nil
	}
	find := a.FindCredentials
	if find == nil {
		find = google.FindDefaultCredentials
	}
	// The source outlives the request that created it, so it must not
	// inherit that request's cancellation.
	creds, err := find(context.WithoutCancel(ctx), cloudPlatformScope)
	if err != nil {
		return nil, err
	}
	ts := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	if a.sources == nil {
		a.sources = map[string]oauth2.TokenSource{}
	}
	a.sources[instance] = ts
	return ts, nil
}
```

`llm/providers/tokenauth/codex.go`:

```go
package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"runtime"
	"strings"
	"sync"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// ClientVersion is reported in the User-Agent the OpenAI Codex backend
// expects; the evener binaries set it to the build version at startup.
var ClientVersion = "dev"

const (
	codexOriginator = "evener"
	codexLiteHeader = "x-openai-internal-codex-responses-lite"
)

// Codex is the oauth-openai-codex transport (spec §9.5). Apply reads the
// instance's OAuth record from <StateDir>/auth/<instance>.json through
// auth/openai (which refreshes and rewrites it) and sets the headers every
// Codex request carries, including ListModels; PrepareRequest adds the
// per-request headers and the client_metadata rule.
type Codex struct {
	// StateDir is the evener state directory; "" means authopenai.DefaultStateDir().
	StateDir string
	// Credentials is the token seam; nil means a shared authopenai.Service.
	Credentials func(ctx context.Context, stateDir, instance string) (authopenai.RuntimeCredentials, error)

	mu       sync.Mutex
	service  *authopenai.Service
	accounts map[string]string
}

// Apply implements llm.Authenticator.
func (c *Codex) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	if res.Credential.Source != "oauth" {
		return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q is not signed in (run `evener openai login --instance %s`)", res.Instance, res.Instance)}
	}
	creds, err := c.credentials(ctx, res.Instance)
	if err != nil {
		if errors.Is(err, authopenai.ErrLoginRequired) {
			return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: %v (run `evener openai login --instance %s`)", res.Instance, err, res.Instance), Cause: err}
		}
		return fmt.Errorf("instance %q: codex credentials: %w", res.Instance, err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.BearerToken)
	if id := c.accountID(res.Instance); id != "" {
		req.Header.Set("ChatGPT-Account-ID", id)
	}
	if req.Header.Get("originator") == "" {
		req.Header.Set("originator", codexOriginator)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent())
	}
	return nil
}

// PrepareRequest implements llm.RequestPreparer: the lite routing header
// (without it the backend hangs), the session and thread ids, and
// client_metadata = merge(body.metadata, req.ClientMetadata) when the row's
// metadata field is on, with metadata itself never sent (spec §9.5).
func (c *Codex) PrepareRequest(_ context.Context, httpReq *http.Request, body map[string]any, req llm.Request, res registry.Resolved) error {
	if res.Caps.ResponsesLite != nil && *res.Caps.ResponsesLite {
		httpReq.Header.Set(codexLiteHeader, "true")
	}
	if sid := strings.TrimSpace(req.SessionID); sid != "" {
		httpReq.Header.Set("session-id", sid)
	}
	if tid := strings.TrimSpace(req.ThreadID); tid != "" {
		httpReq.Header.Set("thread-id", tid)
		httpReq.Header.Set("x-client-request-id", tid)
	}
	if res.Caps.Fields["metadata"] {
		merged := map[string]string{}
		if m, ok := body["metadata"].(map[string]string); ok {
			maps.Copy(merged, m)
		}
		maps.Copy(merged, req.ClientMetadata)
		if len(merged) > 0 {
			body["client_metadata"] = merged
		}
	}
	delete(body, "metadata")
	return nil
}

// RequiresStreamingComplete reports that the Codex backend answers every
// request as a stream (spec §9.5).
func (*Codex) RequiresStreamingComplete() bool { return true }

func (c *Codex) stateDir() string {
	if c.StateDir != "" {
		return c.StateDir
	}
	return authopenai.DefaultStateDir()
}

func (c *Codex) credentials(ctx context.Context, instance string) (authopenai.RuntimeCredentials, error) {
	if c.Credentials != nil {
		return c.Credentials(ctx, c.stateDir(), instance)
	}
	c.mu.Lock()
	if c.service == nil {
		c.service = authopenai.NewService(authopenai.DefaultConfig(), nil)
	}
	service := c.service
	c.mu.Unlock()
	return service.ResolveRuntimeCredentials(ctx, c.stateDir(), instance)
}

// accountID reads the ChatGPT account id from the record (or its id token
// claims) once per instance; it is display metadata, so a missing or
// unreadable record yields "" rather than an error.
func (c *Codex) accountID(instance string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.accounts[instance]; ok {
		return id
	}
	id := ""
	if rec, err := authopenai.LoadAuth(c.stateDir(), instance); err == nil {
		id = rec.AccountID
		if id == "" {
			if claims, err := authopenai.ParseIDTokenClaims(rec.IDToken); err == nil {
				id = claims.AccountID
			}
		}
	}
	if c.accounts == nil {
		c.accounts = map[string]string{}
	}
	c.accounts[instance] = id
	return id
}

func userAgent() string {
	version := strings.TrimSpace(ClientVersion)
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("%s/%s (%s %s)", codexOriginator, version, runtime.GOOS, runtime.GOARCH)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./llm/providers/tokenauth/ -count=1 -race && go vet ./llm/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add llm/go.mod llm/go.sum llm/providers/tokenauth
git commit -m "feat(providers): gcp-adc and Codex authenticators (tokenauth)"
```

(Add `go.work.sum` to the `git add` if `git status` shows it changed.)

---

### Task 6: `chatcompletions` — body builder

**Files:**
- Create: `llm/providers/chatcompletions/protocol.go`, `request.go`, `reasoning.go`, `messages.go`, `cache.go`
- Test: `llm/providers/chatcompletions/request_test.go`, `messages_test.go`, `prunable_test.go`, `request_port_test.go`
- Source to copy from (read-only): `llm/providers/openaicompat/request.go`, `llm/providers/openaicompat/compat.go:327-371`

**Interfaces:**
- Consumes: `registry.BoolValue`, `registry.StringValue`, `(Caps).EffortCapable`, `openaichat.ToChatTools`, `openaichat.ToChatResponseFormat`, `openaichat.ToolArgumentsString`, `openaichat.StrictifyJSONSchema`, `llm.ClampReasoningEffort`, `llm.IsOpenAICompatReasoningField`, `llm.IsOpenAICompatEncryptedReasoning`, `protocolhttp.ModelInBody`, `registry.FieldDeveloperRole`.
- Produces: `chatcompletions.Protocol` with `ID`, `PrunablePaths`, `BuildBody`; unexported `buildBody(req, res, stream bool)`, `toChatMessages(msgs, caps, useReasoningDetails)`, `applyThinkingFormat`, `anthropicCacheControl`, `toChatToolChoice`, `requestWithoutToolResultImages`, `isReasoningControlKey` — Task 7 adds `Complete`/`Stream`/`ListModels`/`CountTokens` on the same type.

The cap that replaces each `openaicompat` quirk (spec §8.2–§8.4; every row is a decision the implementer applies verbatim):

| `openaicompat` quirk / `ModelCompat` field | In `chatcompletions` |
|---|---|
| `LockTemperature`, `LockTopP`, `LockFrequencyPenalty`, `LockPresencePenalty` | gone: `ShapeRequest` clears sampling (`Sampling`, `Fields`), the prune drops the penalty paths |
| `MaxStopSequences`, `DefaultMaxTokens`, `ThinkingLevels`, `TranslateMaxToXHigh`, `wireEffort` | gone: `ShapeRequest` truncates stops, fills `MaxTokens`, clamps the effort to the wire-spelled `EffortValues` ladder |
| `ToolChoiceAutoOnly`, `ToolChoiceAutoUnderReasoning` | `Caps.ToolChoiceForcing == false` → `required`/named → `"auto"` (the OpenRouter row sets it) |
| `StripEmptyContent` | `Caps.StripEmptyContent` |
| `NoJSONSchema` | `Caps.StructuredOutput == false` downgrades `json_schema` → `json_object` |
| `FinishReasonMap` | `Caps.FinishReasonMap` (response side, Task 7) |
| `ThinkingFormat`, `ThinkingAlwaysOn`, `ChatTemplateKwargs` | same-named caps |
| `SupportsReasoningEffort` | effort-capable = `"effort" ∈ Caps.ReasoningControls`, or an unknown row (`ReasoningControls` empty and `Reasoning == nil`) |
| `ReasoningOff` | `Caps.Reasoning == false` |
| `MaxTokensField` | `Caps.MaxTokensField` (default `max_tokens`) |
| `ToolStream` | `Caps.ToolStream` |
| `SendStoreFalse` | `Fields["store"]` true → `store: false` |
| `UseDeveloperRole` | `Fields["developer_role"]` |
| `OmitStreamUsage` | always emit `stream_options`; the prune removes it when `Fields["stream_options"]` is false |
| `RequireToolResultName` | `Caps.ToolResultName` |
| `RequireAssistantAfterToolResult` | `Caps.AssistantAfterToolResult` |
| `ThinkingAsText` | `Caps.ThinkingAsText` |
| `EmptyReasoningContentOnAssistant` | `Caps.EmptyReasoningContent` |
| `CacheControlFormat == "anthropic"`, `SupportsLongCacheRetention` | `Caps.CacheControl == "anthropic"`, marker `ttl` from `Caps.CacheTTL`; `prompt_cache_retention` from the request (`ShapeRequest` gates it) |
| `SendSessionAffinityHeaders` | `Caps.SessionAffinityHeaders` (headers, Task 7) |
| `SupportsStrictMode` (sent `strict: false`) | `Caps.StrictTools` true → `strict: true` and `StrictifyJSONSchema` on each tool (spec §8.2); nothing at baseline |
| `ProviderOptions["openai-compatible"]` | `ProviderOptions["openai-chat"]` |
| reasoning replay field | `Caps.ReasoningField` when set (`reasoning_details` selects the array shape), else the field the text arrived on (`Signature`), else `reasoning_content` (spec §8.4 Replay) |

- [ ] **Step 1: Write the failing tests**

`llm/providers/chatcompletions/prunable_test.go`:

```go
package chatcompletions

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestPrunablePathsMatchRegistry(t *testing.T) {
	p, ok := llm.ProtocolFor(registry.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("openai-chat not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolOpenAIChat); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}
```

`llm/providers/chatcompletions/request_test.go`:

```go
package chatcompletions

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// resolved builds a Resolved record for the openai-chat protocol with the
// baseline Fields table and the given cap overrides.
func resolved(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "work", Protocol: registry.ProtocolOpenAIChat, ModelID: "m", WireID: "m-wire", Transport: registry.Transport{Endpoint: "/chat/completions"}, Caps: caps}
}

func userReq(text string) llm.Request {
	return llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: text}}}}}
}

func build(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildBody_ThinkingFormats(t *testing.T) {
	high := "high"
	type tc struct {
		format   string
		explicit bool // effort "high" set on the request
		alwaysOn bool
		want     map[string]any // reasoning-related keys only
	}
	cases := []tc{
		{"", true, false, map[string]any{"reasoning_effort": "high"}},
		{"openai", false, true, map[string]any{"reasoning_effort": "medium"}},
		{"openai", false, false, map[string]any{}},
		{"openrouter", true, false, map[string]any{"reasoning": map[string]any{"effort": "high"}}},
		{"openrouter", false, true, map[string]any{"reasoning": map[string]any{"enabled": true}}},
		{"zai", true, false, map[string]any{"thinking": map[string]any{"type": "enabled", "clear_thinking": false}, "reasoning_effort": "high"}},
		{"zai", false, true, map[string]any{"thinking": map[string]any{"type": "enabled", "clear_thinking": false}}},
		{"deepseek", true, false, map[string]any{"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high"}},
		{"together", false, true, map[string]any{"reasoning": map[string]any{"enabled": true}}},
		{"qwen", true, false, map[string]any{"enable_thinking": true}},
		{"qwen-chat-template", false, true, map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true, "preserve_thinking": true}}},
		{"chat-template", true, false, map[string]any{"chat_template_kwargs": map[string]any{"thinking": true}}},
		{"string-thinking", true, false, map[string]any{"thinking": "high"}},
		{"string-thinking", false, true, map[string]any{"thinking": "medium"}},
	}
	keys := []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking", "chat_template_kwargs"}
	for _, c := range cases {
		t.Run(c.format+"/explicit="+jsonOf(t, c.explicit)+"/alwaysOn="+jsonOf(t, c.alwaysOn), func(t *testing.T) {
			res := resolved(func(caps *registry.Caps) {
				caps.Reasoning = new(true)
				caps.ReasoningControls = []string{"effort"}
				if c.format != "" {
					caps.ThinkingFormat = new(c.format)
				}
				if c.alwaysOn {
					caps.ThinkingAlwaysOn = new(true)
				}
				if c.format == "chat-template" {
					caps.ChatTemplateKwargs = map[string]any{"thinking": true}
				}
			})
			req := userReq("hi")
			if c.explicit {
				req.ReasoningEffort = &high
			}
			body := build(t, req, res)
			got := map[string]any{}
			for _, k := range keys {
				if v, ok := body[k]; ok {
					got[k] = v
				}
			}
			if jsonOf(t, got) != jsonOf(t, c.want) {
				t.Fatalf("got %s want %s", jsonOf(t, got), jsonOf(t, c.want))
			}
		})
	}
}

func TestBuildBody_ReasoningGates(t *testing.T) {
	high, none := "high", "none"
	off := resolved(func(c *registry.Caps) { c.Reasoning = new(false); c.ThinkingFormat = new("zai") })
	req := userReq("hi")
	req.ReasoningEffort = &high
	req.ProviderOptions = map[string]any{registry.ProtocolOpenAIChat: map[string]any{"reasoning": map[string]any{"effort": "high"}, "top_k": 3}}
	body := build(t, req, off)
	for _, k := range []string{"reasoning_effort", "reasoning", "thinking"} {
		if _, has := body[k]; has {
			t.Fatalf("Reasoning=false must strip %s: %v", k, body)
		}
	}
	if body["top_k"] != 3 {
		t.Fatal("non-reasoning provider options survive")
	}

	toggleOnly := resolved(func(c *registry.Caps) { c.Reasoning = new(true); c.ReasoningControls = []string{"toggle"}; c.ThinkingFormat = new("deepseek") })
	body = build(t, req, toggleOnly)
	if _, has := body["reasoning_effort"]; has || body["thinking"] == nil {
		t.Fatalf("toggle-only rows enable without an effort: %v", body)
	}

	req.ReasoningEffort = &none
	body = build(t, req, resolved(func(c *registry.Caps) { c.Reasoning = new(true); c.ReasoningControls = []string{"effort"}; c.ThinkingAlwaysOn = new(true) }))
	if _, has := body["reasoning_effort"]; has {
		t.Fatalf("none sends nothing: %v", body)
	}

	unknown := resolved(nil) // Reasoning nil, no controls: an explicit effort passes through
	req.ReasoningEffort = &high
	if body := build(t, req, unknown); body["reasoning_effort"] != "high" {
		t.Fatalf("unknown row must pass an explicit effort through: %v", body)
	}
}

func TestBuildBody_CapsShapeStructure(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Description: "d", Parameters: schema}}
	req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: schema}
	req.MaxTokens = new(50)
	req.Metadata = map[string]string{"k": "v"}
	req.SessionID = "sess"
	req.PromptCacheRetention = "24h"

	base := build(t, req, resolved(nil))
	fn := base["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if _, has := fn["strict"]; has || base["tool_choice"] != "required" || base["max_tokens"] != 50 {
		t.Fatalf("baseline: %s", jsonOf(t, base))
	}
	if base["response_format"].(map[string]any)["type"] != "json_schema" || base["store"] != nil || base["prompt_cache_key"] != nil {
		t.Fatalf("baseline: %s", jsonOf(t, base))
	}
	if base["metadata"] == nil || base["prompt_cache_retention"] != "24h" {
		t.Fatalf("prunable paths are emitted for the prune to decide: %s", jsonOf(t, base))
	}
	if base["model"] != "m-wire" {
		t.Fatalf("model must be the wire id: %v", base["model"])
	}

	shaped := build(t, req, resolved(func(c *registry.Caps) {
		c.StrictTools = new(true)
		c.StructuredOutput = new(false)
		c.ToolChoiceForcing = new(false)
		c.MaxTokensField = new("max_completion_tokens")
		c.ToolStream = new(true)
		c.Fields["store"] = true
		c.Fields["prompt_cache_key"] = true
	}))
	fn = shaped["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if fn["strict"] != true || fn["parameters"].(map[string]any)["additionalProperties"] != false {
		t.Fatalf("strict tools: %s", jsonOf(t, shaped))
	}
	if shaped["tool_choice"] != "auto" || shaped["response_format"].(map[string]any)["type"] != "json_object" || shaped["max_completion_tokens"] != 50 || shaped["max_tokens"] != nil || shaped["tool_stream"] != true {
		t.Fatalf("shaped: %s", jsonOf(t, shaped))
	}
	if shaped["store"] != false || shaped["prompt_cache_key"] != "evener-session-sess" {
		t.Fatalf("store/prompt cache: %s", jsonOf(t, shaped))
	}
	named := req
	named.ToolChoice = &llm.ToolChoice{Mode: "named", Name: "f"}
	if b := build(t, named, resolved(func(c *registry.Caps) { c.ToolChoiceForcing = new(false) })); b["tool_choice"] != "auto" {
		t.Fatalf("named choice must downgrade: %v", b["tool_choice"])
	}

	streaming, err := buildBody(llm.ShapeRequest(req, resolved(nil)), resolved(nil), true)
	if err != nil || streaming["stream"] != true || !reflect.DeepEqual(streaming["stream_options"], map[string]any{"include_usage": true}) {
		t.Fatalf("stream: %v %s", err, jsonOf(t, streaming))
	}

	inPath := resolved(nil)
	inPath.Transport.Endpoint = "/models/{model}/chat"
	if b := build(t, req, inPath); b["model"] != nil {
		t.Fatal("a {model} endpoint sends no model in the body")
	}
}

func TestBuildBody_AnthropicCacheControl(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
	}, Tools: []llm.ToolDefinition{{Name: "f"}}}
	body := build(t, req, resolved(func(c *registry.Caps) { c.CacheControl = new("anthropic"); c.CacheTTL = new("1h") }))
	msgs := body["messages"].([]map[string]any)
	sys := msgs[0]["content"].([]map[string]any)[0]["cache_control"].(map[string]any)
	if sys["type"] != "ephemeral" || sys["ttl"] != "1h" {
		t.Fatalf("system marker: %s", jsonOf(t, body))
	}
	if tool := body["tools"].([]map[string]any)[0]; tool["cache_control"] == nil {
		t.Fatalf("last tool marker missing: %s", jsonOf(t, body))
	}
	plain := build(t, req, resolved(func(c *registry.Caps) { c.CacheControl = new("anthropic") }))
	if m := plain["messages"].([]map[string]any)[0]["content"].([]map[string]any)[0]["cache_control"].(map[string]any); m["ttl"] != nil {
		t.Fatal("no ttl without CacheTTL")
	}
}
```

(`anthropicCacheControl` in `openaicompat/compat.go:327-371` decides the exact content shapes it rewrites; keep those shapes and adjust the type assertions above to what the ported function emits, e.g. `[]any` versus `[]map[string]any`.)

`llm/providers/chatcompletions/messages_test.go`:

```go
package chatcompletions

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func assistantTurn(thinking, sig, text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: thinking, Signature: sig}},
		{Kind: llm.ContentText, Text: text},
	}}
}

func TestToChatMessages_RoleAndContentCaps(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: ""}, {Kind: llm.ContentText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "f", Content: "ok"}}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "next"}}},
	}
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	caps.Fields[registry.FieldDeveloperRole] = true
	caps.ToolResultName = new(true)
	caps.StripEmptyContent = new(true)
	caps.AssistantAfterToolResult = new(true)
	out, err := toChatMessages(msgs, caps, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["role"] != "developer" {
		t.Fatalf("developer role: %v", out[0])
	}
	if out[1]["content"] != "hi" {
		t.Fatalf("empty text must be stripped: %v", out[1])
	}
	if out[3]["name"] != "f" {
		t.Fatalf("tool result name: %v", out[3])
	}
	if out[4]["role"] != "assistant" || out[4]["content"] != "" || out[5]["role"] != "user" {
		t.Fatalf("assistant turn must be inserted after the tool result: %v", out[4:])
	}
}

func TestToChatMessages_ReasoningReplay(t *testing.T) {
	base := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	turn := []llm.Message{assistantTurn("thought", "reasoning", "answer")}

	out, _ := toChatMessages(turn, base, false)
	if out[0]["reasoning"] != "thought" {
		t.Fatalf("Signature names the field the text arrived on: %v", out[0])
	}

	field := base
	field.ReasoningField = new("reasoning_content")
	out, _ = toChatMessages(turn, field, false)
	if out[0]["reasoning_content"] != "thought" || out[0]["reasoning"] != nil {
		t.Fatalf("ReasoningField wins over the signature: %v", out[0])
	}

	details := base
	details.ReasoningField = new("reasoning_details")
	out, _ = toChatMessages(turn, details, true)
	items, ok := out[0]["reasoning_details"].([]map[string]any)
	if !ok || len(items) != 1 || items[0]["text"] != "thought" || items[0]["type"] != "reasoning.text" {
		t.Fatalf("reasoning_details replay: %v", out[0])
	}

	signed := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought", Signature: "reasoning_details", EncryptedContent: `[{"type":"reasoning.text","text":"","signature":"sig-1","format":"anthropic-claude-v1","index":0}]`}},
		{Kind: llm.ContentText, Text: "answer"},
	}}}
	out, _ = toChatMessages(signed, base, false)
	items = out[0]["reasoning_details"].([]map[string]any)
	if len(items) != 1 || items[0]["text"] != "thought" || items[0]["signature"] != "sig-1" {
		t.Fatalf("signed item must absorb the text: %v", items)
	}

	asText := base
	asText.ThinkingAsText = new(true)
	out, _ = toChatMessages(turn, asText, false)
	if out[0]["content"] != "thought\n\nanswer" || out[0]["reasoning"] != nil {
		t.Fatalf("thinking as text: %v", out[0])
	}

	off := base
	off.Reasoning = new(false)
	out, _ = toChatMessages(turn, off, false)
	if out[0]["reasoning"] != nil || out[0]["reasoning_content"] != nil || out[0]["reasoning_details"] != nil {
		t.Fatalf("Reasoning=false drops replayed thinking: %v", out[0])
	}

	empty := base
	empty.EmptyReasoningContent = new(true)
	out, _ = toChatMessages([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "plain"}}}}, empty, false)
	if v, has := out[0]["reasoning_content"]; !has || v != "" {
		t.Fatalf("empty reasoning_content on assistant turns: %v", out[0])
	}
}
```

`EncryptedContent` is the field `openaicompat/request.go:510-525` (`encryptedDetailsFromParts`) reads; if `llm.ThinkingData` (`llm/types.go:209`) names it differently, use that name. The `signed` case's value must satisfy `llm.IsOpenAICompatEncryptedReasoning` (`llm/types.go:264`); mirror the shape `openaicompat/reasoning_fields_test.go` uses. The `details` case passes `useReasoningDetails = true` because `buildBody`, not `toChatMessages`, derives that flag from `ReasoningField`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/chatcompletions/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Write `protocol.go`, `request.go`, and `reasoning.go`**

`llm/providers/chatcompletions/protocol.go`:

```go
// Package chatcompletions implements the OpenAI Chat Completions wire
// protocol (registry.ProtocolOpenAIChat) as an llm.Protocol driven entirely
// by registry.Resolved: base URL, headers, auth scheme, and every quirk
// arrive as data (spec §8). It consolidates the two Chat Completions
// builders that existed before the registry (openaicompat and the openai
// adapter's chat fallback).
package chatcompletions

import (
	"net/http"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the single registered openai-chat implementation. Client is
// nil for protocolhttp.DefaultClient; tests inject httptest clients.
type Protocol struct {
	Client *http.Client
}

func init() { llm.RegisterProtocol(&Protocol{}) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolOpenAIChat }

// prunablePaths is the protocol's own statement of the optional wire
// fields its builder emits; TestPrunablePathsMatchRegistry proves it
// equals the registry's table (spec §8.2).
var prunablePaths = []string{
	registry.FieldDeveloperRole, "frequency_penalty", "logprobs", registry.FieldMaxTokens, "metadata", "n",
	"parallel_tool_calls", "presence_penalty", "prompt_cache_key", "prompt_cache_retention", "seed",
	"service_tier", "stop", "store", "stream_options", "temperature", "top_p", "user",
}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol for a non-streaming request.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	return buildBody(req, res, false)
}

```

`llm/providers/chatcompletions/request.go`:

```go
package chatcompletions

import (
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// buildBody builds the Chat Completions body for a shaped request (spec
// §8.2 step 1, §8.4). Every structural decision reads a cap; nothing here
// branches on the model id. Prunable paths are emitted whenever the
// request carries them; the runner's prune removes the ones the row turns
// off.
func buildBody(req llm.Request, res registry.Resolved, stream bool) (map[string]any, error) {
	caps := res.Caps
	if !registry.BoolValue(caps.MultimodalToolResults) {
		req = requestWithoutToolResultImages(req)
	}
	body := map[string]any{}
	if protocolhttp.ModelInBody(res) {
		body["model"] = res.WireID
	}
	reasoningOff := caps.Reasoning != nil && !*caps.Reasoning
	options, _ := req.ProviderOptions[registry.ProtocolOpenAIChat].(map[string]any)
	useReasoningDetails := registry.StringValue(caps.ReasoningField) == "reasoning_details"
	if _, ok := options["reasoning"]; ok && !reasoningOff {
		useReasoningDetails = true
	}
	msgs, err := toChatMessages(req.Messages, caps, useReasoningDetails)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs
	if len(req.Tools) > 0 {
		tools := openaichat.ToChatTools(req.Tools)
		if registry.BoolValue(caps.StrictTools) {
			for _, t := range tools {
				if fn, ok := t["function"].(map[string]any); ok {
					fn["strict"] = true
					if params, ok := fn["parameters"].(map[string]any); ok {
						fn["parameters"] = openaichat.StrictifyJSONSchema(params)
					}
				}
			}
		}
		body["tools"] = tools
		if registry.BoolValue(caps.ToolStream) {
			body["tool_stream"] = true
		}
	}
	if req.ToolChoice != nil {
		tc, err := toChatToolChoice(*req.ToolChoice)
		if err != nil {
			return nil, err
		}
		if caps.ToolChoiceForcing != nil && !*caps.ToolChoiceForcing && tc != "auto" && tc != "none" {
			tc = "auto"
		}
		body["tool_choice"] = tc
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body[maxTokensField(caps)] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		format := openaichat.ToChatResponseFormat(*req.ResponseFormat)
		if caps.StructuredOutput != nil && !*caps.StructuredOutput && format["type"] == "json_schema" {
			format = map[string]any{"type": "json_object"}
		}
		body["response_format"] = format
	}
	if !useReasoningDetails {
		applyThinkingFormat(body, req, caps)
	}
	if caps.Fields["store"] {
		body["store"] = false
	}
	if key := promptCacheKey(req, caps); key != "" {
		body["prompt_cache_key"] = key
	}
	if retention := strings.TrimSpace(req.PromptCacheRetention); retention != "" {
		body["prompt_cache_retention"] = retention
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	for k, v := range options {
		if reasoningOff && isReasoningControlKey(k) {
			continue
		}
		body[k] = v
	}
	if registry.StringValue(caps.CacheControl) == "anthropic" {
		anthropicCacheControl(body, registry.StringValue(caps.CacheTTL))
	}
	return body, nil
}

// maxTokensField is the spelling Caps.MaxTokensField selects, max_tokens by
// default (the compatible-server default; the openai overlay pins
// max_completion_tokens).
func maxTokensField(caps registry.Caps) string {
	if f := registry.StringValue(caps.MaxTokensField); f != "" {
		return f
	}
	return "max_tokens"
}

// promptCacheKey is the request's key, else the session-derived key when
// the row sends prompt_cache_key at all.
func promptCacheKey(req llm.Request, caps registry.Caps) string {
	if k := strings.TrimSpace(req.PromptCacheKey); k != "" {
		return k
	}
	if caps.Fields["prompt_cache_key"] {
		if sid := strings.TrimSpace(req.SessionID); sid != "" {
			return "evener-session-" + sid
		}
	}
	return ""
}
```

`llm/providers/chatcompletions/reasoning.go`:

```go
package chatcompletions

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// applyThinkingFormat writes the reasoning control in the row's dialect
// (spec §8.4, the table kept verbatim from openaicompat's
// applyThinkingFormat). The effort arrives already clamped by ShapeRequest;
// none sends nothing on every dialect; ThinkingAlwaysOn with no effort
// sends the enable object, or medium on the two dialects that carry a
// default effort.
func applyThinkingFormat(body map[string]any, req llm.Request, caps registry.Caps) {
	if caps.Reasoning != nil && !*caps.Reasoning {
		return
	}
	explicit := req.ReasoningEffort != nil
	wire := ""
	if explicit {
		wire = *req.ReasoningEffort
	}
	if wire == "none" {
		return
	}
	alwaysOn := registry.BoolValue(caps.ThinkingAlwaysOn)
	if wire == "" {
		if !alwaysOn {
			return
		}
		wire = llm.ClampReasoningEffort("medium", caps.EffortValues)
	}
	capable := caps.EffortCapable()
	switch registry.StringValue(caps.ThinkingFormat) {
	case "", "openai":
		if capable {
			body["reasoning_effort"] = wire
		}
	case "openrouter":
		if explicit {
			body["reasoning"] = map[string]any{"effort": wire}
		} else {
			body["reasoning"] = map[string]any{"enabled": true}
		}
	case "zai":
		body["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
		if explicit && capable {
			body["reasoning_effort"] = wire
		}
	case "deepseek":
		body["thinking"] = map[string]any{"type": "enabled"}
		if explicit && capable {
			body["reasoning_effort"] = wire
		}
	case "together":
		body["reasoning"] = map[string]any{"enabled": true}
		if explicit && capable {
			body["reasoning_effort"] = wire
		}
	case "qwen":
		body["enable_thinking"] = true
	case "qwen-chat-template":
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": true, "preserve_thinking": true}
	case "chat-template":
		if len(caps.ChatTemplateKwargs) > 0 {
			body["chat_template_kwargs"] = caps.ChatTemplateKwargs
		}
	case "string-thinking":
		body["thinking"] = wire
	}
}
```

- [ ] **Step 4: Port the message and cache helpers**

Create `llm/providers/chatcompletions/messages.go` by copying from `llm/providers/openaicompat/request.go`: lines 177-197 (`hasReasoningControls`, `isReasoningControlKey`), 294-538 (`toChatMessages`, `insertAssistantAfterToolResults`, `textFromParts`, `reasoningReplayField`, `encryptedDetailsFromParts`, `thinkingFromParts`), 541-643 (`hasImageContent`, `buildMultimodalParts`, `requestWithoutToolResultImages`, `requestHasToolResultImages`), and 645-681 (`toolCallsFromParts`, `toChatToolChoice`). Then apply exactly these edits:

1. `func toChatMessages(messages []llm.Message, mc ModelCompat, useReasoningDetails bool)` → `func toChatMessages(messages []llm.Message, caps registry.Caps, useReasoningDetails bool)`; delete `quirks := mc.Quirks`; add `reasoningOff := caps.Reasoning != nil && !*caps.Reasoning` at the top and replace every `mc.ReasoningOff` with `reasoningOff`.
2. `quirks.UseDeveloperRole` → `caps.Fields[registry.FieldDeveloperRole]`; `quirks.RequireToolResultName` → `registry.BoolValue(caps.ToolResultName)`; `quirks.StripEmptyContent` → `registry.BoolValue(caps.StripEmptyContent)`; `quirks.ThinkingAsText` → `registry.BoolValue(caps.ThinkingAsText)`; `quirks.EmptyReasoningContentOnAssistant` → `registry.BoolValue(caps.EmptyReasoningContent)`; `quirks.RequireAssistantAfterToolResult` → `registry.BoolValue(caps.AssistantAfterToolResult)`.
3. `reasoningReplayField(content)` → `reasoningReplayField(content, caps)`, whose body starts with `if f := registry.StringValue(caps.ReasoningField); f != "" && f != "reasoning_details" { return f }` before today's signature check.
4. Delete `hasReasoningControls` if nothing calls it after the edits (the OpenRouter "auto under reasoning" rule is now `ToolChoiceForcing`).
5. No other logic changes; the `llm.IsOpenAICompatReasoningField`/`IsOpenAICompatEncryptedReasoning` calls stay as they are.

Create `llm/providers/chatcompletions/cache.go` by copying `anthropicCacheControl` and `addCacheControlToTextContent` from `llm/providers/openaicompat/compat.go:327-371`, changing the signature to `anthropicCacheControl(body map[string]any, ttl string)` and the marker construction to:

```go
	marker := map[string]any{"type": "ephemeral"}
	if ttl != "" {
		marker["ttl"] = ttl
	}
```

(the old `longRetention bool` produced `ttl: "1h"`; `ttl` now comes from `Caps.CacheTTL`).

- [ ] **Step 5: Run the tests**

Run: `go test ./llm/providers/chatcompletions/ -count=1 && go vet ./llm/providers/chatcompletions/`
Expected: PASS.

- [ ] **Step 6: Port the deeper builder tests**

From `llm/providers/openaicompat/compat_request_test.go` and `reasoning_fields_test.go`, port every `Test*` that calls `buildRequestBody` or `toChatMessages` and whose quirk has a cap in the table above (developer role, tool-result name, assistant-after-tool, thinking-as-text, cache control, encrypted-details replay, duplicate-field non-doubling, signature survival, text-merge-into-signature-item, store, tool_stream, max-tokens spelling, JSON-schema downgrade) into `llm/providers/chatcompletions/request_port_test.go`, replacing the `ModelCompat`/`ProviderQuirks` fixture with a `resolved(func(c *registry.Caps){…})` call. Skip tests whose subject moved to `ShapeRequest` (sampling locks, `MaxStopSequences`, `DefaultMaxTokens`, `ThinkingLevels`, `TranslateMaxToXHigh`) and the adaptive-fallback tests. Run the package tests again: PASS.

- [ ] **Step 7: Commit**

```bash
git add llm/providers/chatcompletions
git commit -m "feat(chatcompletions): Resolved-driven Chat Completions body builder"
```

---

### Task 7: `chatcompletions` — Complete, Stream, ListModels, CountTokens

**Files:**
- Create: `llm/providers/chatcompletions/complete.go`, `stream.go`, `response.go`, `rescue.go`, `models.go`
- Test: `llm/providers/chatcompletions/transport_test.go`, `models_test.go`, `requestbuild_fuzz_test.go`, `stream_fuzz_test.go`
- Modify: `scripts/fuzz/fuzz-targets.txt`
- Source to copy from (read-only): `llm/providers/openaicompat/adapter.go:452-733` (`decodeStream`), `response.go` (all), `rescue.go:93-235`, `models.go:77-229`, `requestbuild_fuzz_test.go`, `stream_fuzz_test.go`

**Interfaces:**
- Consumes: `protocolhttp.Call/Do/Stream/URL/CompleteViaStream/RequiresStreamingComplete`, `transport.StreamRunner`, `transport.FatalStreamError`, `openaichat.ParseChatUsage`, `openaichat.InbandError`, `llm.ClassifyHTTPError` (through the runner), `llm.StampEndpointURL`, `llm.ParseRateLimitHeaders`, `llm.NormalizeFinishReason`.
- Produces: the complete `llm.Protocol` for `openai-chat`; `fromChatCompletionResponse(raw map[string]any, finishMap map[string]string) (llm.Response, error)`; `decodeStream(...)`; `modelEntry.row() registry.Model`.

- [ ] **Step 1: Write the failing tests**

`llm/providers/chatcompletions/transport_test.go`:

```go
package chatcompletions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const chatJSON = `{"id":"chatcmpl-1","model":"m-wire","choices":[{"index":0,"message":{"role":"assistant","content":"hello","reasoning_content":"why","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

const chatSSE = "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"{\\\"a\\\":\"}}]}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
	"data: [DONE]\n\n"

type capturedRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

// server answers every chat.completions call with the given body and
// records what it received.
func server(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.header = r.URL.RequestURI(), r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		if strings.HasPrefix(body, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func liveRes(srv *httptest.Server, mutate func(c *registry.Caps)) registry.Resolved {
	res := resolved(mutate)
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	res.Headers = map[string]string{"X-Portkey-Provider": "openai"}
	return res
}

func TestCompleteDecodesTextToolCallsAndUsage(t *testing.T) {
	srv, got := server(t, 200, chatJSON)
	res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	req.SessionID = "s1"
	resp, err := p.Complete(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "work" || resp.Text() != "hello" || len(resp.ToolCalls()) != 1 || resp.ToolCalls()[0].Name != "f" || resp.Finish.Reason != "tool_calls" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if got.path != "/v1/chat/completions" || got.header.Get("Authorization") != "Bearer k-1" || got.header.Get("X-Portkey-Provider") != "openai" || got.body["model"] != "m-wire" {
		t.Fatalf("wire: %s %v %v", got.path, got.header, got.body)
	}
	if got.header.Get("session_id") != "" {
		t.Fatal("no affinity headers unless the cap says so")
	}
	affinity := liveRes(srv, func(c *registry.Caps) { c.SessionAffinityHeaders = new(true) })
	if _, err := p.Complete(context.Background(), req, affinity); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("session_id") != "s1" || got.header.Get("x-client-request-id") != "s1" || got.header.Get("x-session-affinity") != "s1" {
		t.Fatalf("affinity headers: %v", got.header)
	}
}

func TestStreamEmitsTextThenToolCallThenFinish(t *testing.T) {
	srv, got := server(t, 200, chatSSE)
	res := liveRes(srv, func(c *registry.Caps) { c.Fields["stream_options"] = false })
	p := &Protocol{Client: srv.Client()}
	s, err := p.Stream(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	var types []llm.StreamEventType
	var final *llm.Response
	for ev := range s.Events() {
		types = append(types, ev.Type)
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil || final.Provider != "work" || final.Text() != "hello" || len(final.ToolCalls()) != 1 || string(final.ToolCalls()[0].Arguments) != `{"a":1}` || final.Usage.TotalTokens != 15 {
		t.Fatalf("final = %+v events = %v", final, types)
	}
	if types[0] != llm.StreamEventStreamStart || types[len(types)-1] != llm.StreamEventFinish {
		t.Fatalf("events = %v", types)
	}
	if got.body["stream"] != true {
		t.Fatalf("stream flag missing: %v", got.body)
	}
	if _, has := got.body["stream_options"]; has {
		t.Fatalf("Fields[stream_options]=false must prune it from the wire: %v", got.body)
	}
}

func TestStreamInbandErrorBecomesTypedError(t *testing.T) {
	srv, _ := server(t, 200, "data: {\"error\":{\"message\":\"Rate limit reached\",\"type\":\"rate_limit_error\",\"code\":429}}\n\n")
	p := &Protocol{Client: srv.Client()}
	s, err := p.Stream(context.Background(), userReq("hi"), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			streamErr = ev.Err
		}
	}
	var le llm.Error
	if streamErr == nil || !asLLMError(streamErr, &le) || le.StatusCode() != 429 || le.Provider() != "work" {
		t.Fatalf("stream err = %v", streamErr)
	}
}

func TestCompleteClassifiesHTTPErrorsWithHints(t *testing.T) {
	srv, _ := server(t, 400, `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`)
	p := &Protocol{Client: srv.Client()}
	_, err := p.Complete(context.Background(), userReq("hi"), liveRes(srv, nil))
	if llm.Kind(err) != llm.KindInvalidRequest || !strings.Contains(llm.ErrorHint(err), `set max_tokens_field = "max_completion_tokens" on work/m`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCountTokensIsUnsupported(t *testing.T) {
	srv, _ := server(t, 200, chatJSON)
	res := liveRes(srv, nil)
	if _, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), userReq("hi"), res); err != llm.ErrInputTokenCountUnsupported {
		t.Fatalf("err = %v", err)
	}
	res.Transport.CountTokensEndpoint = "/count"
	if _, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), userReq("hi"), res); err == nil {
		t.Fatal("a configured endpoint on a protocol without one is a configuration error")
	}
}

func asLLMError(err error, target *llm.Error) bool {
	le, ok := err.(llm.Error)
	if ok {
		*target = le
		return true
	}
	return false
}
```

(If `errors.As(err, target)` with `target *llm.Error` already works for wrapped stream errors, use it instead of `asLLMError`.)

`llm/providers/chatcompletions/models_test.go`:

```go
package chatcompletions

import (
	"context"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const openRouterModels = `{"data":[
 {"id":"anthropic/claude-opus-5","context_length":1000000,"supported_parameters":["tools","reasoning","temperature"],"architecture":{"input_modalities":["text","image"]},"reasoning":{"mandatory":true,"supported_efforts":["low","high","high"]},"pricing":{"prompt":"0.000005","completion":"0.000025"},"top_provider":{"max_completion_tokens":128000}},
 {"id":"plain/model","context_length":8192}
]}`

func TestListModelsMapsAdvertisedFacts(t *testing.T) {
	srv, got := server(t, 200, openRouterModels)
	res := liveRes(srv, nil)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/models" || got.header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}
	if len(rows) != 2 || rows[0].ID != "anthropic/claude-opus-5" || rows[1].ID != "plain/model" {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if *c.ContextWindow != 1000000 || *c.MaxOutputTokens != 128000 || !*c.Tools || !*c.Reasoning || !*c.ThinkingAlwaysOn {
		t.Fatalf("caps = %+v", c)
	}
	if len(c.EffortValues) != 2 || c.EffortValues[1] != "high" || len(c.InputModalities) != 2 || c.Cost == nil || c.Cost.Input != 5 || c.Cost.Output != 25 {
		t.Fatalf("caps = %+v", c)
	}
	plain := rows[1].Caps
	if *plain.ContextWindow != 8192 || plain.Tools != nil || plain.Reasoning != nil || plain.Cost != nil {
		t.Fatalf("unadvertised facts must stay nil: %+v", plain)
	}
	res.Transport.ModelsEndpoint = registry.EndpointUnsupported
	if _, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res); err != llm.ErrModelListingUnsupported {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/chatcompletions/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Write `complete.go`**

```go
package chatcompletions

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

const endpointFamily = "openai_chat_completions"

// call assembles the protocolhttp call: the session-affinity headers are
// the only protocol-fixed headers this protocol adds.
func (p *Protocol) call(operation, method, url string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	headers := map[string]string{}
	if registry.BoolValue(res.Caps.SessionAffinityHeaders) {
		if sid := strings.TrimSpace(req.SessionID); sid != "" {
			for _, h := range []string{"session_id", "x-client-request-id", "x-session-affinity"} {
				headers[h] = sid
			}
		}
	}
	return &protocolhttp.Call{Operation: operation, EndpointFamily: endpointFamily, Method: method, URL: url, Body: body, Headers: headers, Req: req, Res: res, Client: p.Client}
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := buildBody(req, res, false)
	if err != nil {
		return llm.Response{}, err
	}
	var out llm.Response
	err = protocolhttp.Do(ctx, p.call("chat.completions", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		if r.Raw == nil {
			return nil, fmt.Errorf("chat.completions: response is not a JSON object")
		}
		resp, err := fromChatCompletionResponse(r.Raw, res.Caps.FinishReasonMap)
		if err != nil {
			return nil, err
		}
		llm.StampEndpointURL(&resp, r.EndpointURL, r.Material)
		resp.RateLimit = llm.ParseRateLimitHeaders(r.Header)
		out = resp
		return &out, nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	return out, nil
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := buildBody(req, res, true)
	if err != nil {
		return nil, err
	}
	call := p.call("chat.completions(stream)", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		decodeStream(sctx, cancel, resp, s, req, res, r, attempt)
	})
}

// CountTokens implements llm.Protocol: Chat Completions has no counting
// endpoint, so the default "-" reports unsupported and any other value is
// a configuration mistake.
func (*Protocol) CountTokens(_ context.Context, _ llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	return 0, &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: openai-chat has no token counting endpoint (count_tokens_endpoint = %q)", res.Instance, res.Transport.CountTokensEndpoint)}
}

// mapFinishReason applies the row's FinishReasonMap before normalization.
func mapFinishReason(m map[string]string, raw string) string {
	if v, ok := m[raw]; ok {
		return v
	}
	return raw
}
```

- [ ] **Step 4: Port the decoder, response types, rescue, and models**

`response.go`: copy `llm/providers/openaicompat/response.go` in full, then: `fromChatCompletionResponse(raw map[string]any, quirks ProviderQuirks)` → `fromChatCompletionResponse(raw map[string]any, finishMap map[string]string)`; `quirks.mapFinishReason(rawFinish)` → `mapFinishReason(finishMap, rawFinish)`; drop the hardcoded `Provider: "openai-compatible"` (the runner and the decoder stamp `res.Instance`).

`stream.go`: copy `decodeStream` from `llm/providers/openaicompat/adapter.go:452-733` as `func decodeStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, res registry.Resolved, r *protocolhttp.Result, attempt *transport.APIAttemptCapture)`, then: `a.compatFor(req.Model).Quirks.mapFinishReason(x)` → `mapFinishReason(res.Caps.FinishReasonMap, x)`; the `rl *llm.RateLimitInfo` parameter → `rl := llm.ParseRateLimitHeaders(resp.Header)` at the top; the endpoint stamp argument (`a.BaseURL` + path) → `r.EndpointURL`; `a.apiLogCredentialMaterial(nil)` → `r.Material`; every `"openai-compatible"` provider literal (`transport.StreamRunner.Provider`, `llm.ErrorFromHTTPStatus(...)` for the in-band error, `llm.NewStreamError`, the final `Response.Provider`) → `res.Instance`. Keep the `transport.StreamRunner` epilogue, the `[DONE]` settlement, the `rescueClaudeXMLArgs` call, and the `attempt.Complete` calls exactly as they are.

`rescue.go`: copy `llm/providers/openaicompat/rescue.go` in full (package-private, unchanged).

`models.go`:

```go
package chatcompletions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// modelEntry is one /models row: the core OpenAI shape plus OpenRouter's
// extensions, which are absent (and therefore not advertised) elsewhere.
type modelEntry struct {
	ID                  string   `json:"id"`
	ContextLength       int      `json:"context_length"`
	SupportedParameters []string `json:"supported_parameters"`
	Architecture        struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	Reasoning struct {
		Mandatory        *bool    `json:"mandatory"`
		DefaultEnabled   *bool    `json:"default_enabled"`
		SupportedEfforts []string `json:"supported_efforts"`
	} `json:"reasoning"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

// row keeps only advertised facts (registry.ApplyLive keeps only those).
func (m modelEntry) row() registry.Model {
	caps := registry.Caps{}
	if m.ContextLength > 0 {
		caps.ContextWindow = new(m.ContextLength)
	}
	if m.TopProvider.MaxCompletionTokens != nil && *m.TopProvider.MaxCompletionTokens > 0 {
		caps.MaxOutputTokens = new(*m.TopProvider.MaxCompletionTokens)
	}
	if len(m.SupportedParameters) > 0 {
		caps.Tools = new(slices.Contains(m.SupportedParameters, "tools"))
		caps.Reasoning = new(slices.Contains(m.SupportedParameters, "reasoning") || slices.Contains(m.SupportedParameters, "reasoning_effort"))
	}
	if m.Reasoning.Mandatory != nil || m.Reasoning.DefaultEnabled != nil || len(m.Reasoning.SupportedEfforts) > 0 {
		caps.Reasoning = new(true)
		if m.Reasoning.Mandatory != nil && *m.Reasoning.Mandatory {
			caps.ThinkingAlwaysOn = new(true)
		}
		caps.EffortValues = dedupStrings(m.Reasoning.SupportedEfforts)
	}
	if len(m.Architecture.InputModalities) > 0 {
		caps.InputModalities = dedupStrings(m.Architecture.InputModalities)
	}
	if in, ok := perTokenCostToPerMillion(m.Pricing.Prompt); ok {
		if out, ok := perTokenCostToPerMillion(m.Pricing.Completion); ok {
			caps.Cost = &registry.Cost{Input: in, Output: out}
		}
	}
	return registry.Model{ID: m.ID, Caps: caps}
}

// ListModels implements llm.Protocol.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	call := &protocolhttp.Call{Operation: "models.list", EndpointFamily: "openai_models", Method: http.MethodGet, URL: protocolhttp.URL(res, res.Transport.ModelsEndpoint), Req: llm.Request{Model: "*"}, Res: res, Client: p.Client}
	var rows []registry.Model
	err := protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
		var payload struct {
			Data []modelEntry `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		for _, e := range payload.Data {
			if e.ID != "" {
				rows = append(rows, e.row())
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		return nil, nil
	})
	return rows, err
}

func dedupStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// perTokenCostToPerMillion converts OpenRouter's per-token price strings
// to the registry's per-million unit; "0" is a valid free price.
func perTokenCostToPerMillion(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v * 1e6, true
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./llm/providers/chatcompletions/ -count=1 -race`
Expected: PASS.

- [ ] **Step 6: Fuzz targets**

Create `requestbuild_fuzz_test.go` from `llm/providers/openaicompat/requestbuild_fuzz_test.go`: the target is `FuzzChatCompletionsBuildBody`, the fixture becomes `resolved(...)` with the fuzzed bytes also flipping `StrictTools`, `StructuredOutput`, `ToolChoiceForcing`, `ThinkingFormat` (index into the nine dialects), `ThinkingAlwaysOn`, `Reasoning`, and `CacheControl`, and the property is: `buildBody` never panics, its output marshals, and `registry.Prune` on it never panics. Create `stream_fuzz_test.go` from `llm/providers/openaicompat/stream_fuzz_test.go`: the target is `FuzzChatCompletionsStreamMetamorphic`, driving `(&Protocol{Client: srv.Client()}).Stream(ctx, req, liveRes(srv, nil))` with the byte-at-a-time re-chunk oracle unchanged. Add to `scripts/fuzz/fuzz-targets.txt`:

```
native:llm:./providers/chatcompletions:FuzzChatCompletionsBuildBody
native:llm:./providers/chatcompletions:FuzzChatCompletionsStreamMetamorphic
```

Run: `go test ./llm/providers/chatcompletions/ -run 'Fuzz' -fuzz FuzzChatCompletionsBuildBody -fuzztime 10s && go test ./llm/providers/chatcompletions/ -run 'Fuzz' -fuzz FuzzChatCompletionsStreamMetamorphic -fuzztime 10s && make fuzz-registry-check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add llm/providers/chatcompletions scripts/fuzz/fuzz-targets.txt
git commit -m "feat(chatcompletions): transport, stream decoding, model listing, fuzz targets"
```

---

### Task 8: `responses` — body builder

**Files:**
- Create: `llm/providers/responses/protocol.go`, `request.go`, `input.go`
- Test: `llm/providers/responses/request_test.go`, `prunable_test.go`
- Source to copy from (read-only): `llm/providers/openai/responses.go:23-223` (`buildRequestBody`), `838-886` (`toResponsesResponseFormat`, `toResponsesTools`), `975-1005` (`toResponsesToolChoice`), `1066-1345` (`toResponsesInput`, `reasoningSummaryInput`, `parseReasoningSummary`), `llm/providers/openai/adapter.go:533-539` (`appendUniqueString`), `llm/providers/openai/image_input.go` (all)

**Interfaces:**
- Consumes: `registry.BoolValue`, `registry.StringValue`, `(Caps).EffortCapable`, `openaichat.StrictifyJSONSchema`, `openaichat.ToolArgumentsString`, `protocolhttp.ModelInBody`.
- Produces: `responses.Protocol{Client, Hasher}` with `ID`, `PrunablePaths`, `BuildBody`; unexported `buildBody(req, res, stream bool)`, `toResponsesInput(msgs []llm.Message, imageDetail string) (instructions string, items []any, err error)`, `toResponsesTools(tools, strict bool)`, `toResponsesResponseFormat(rf, structured bool) any`, `toResponsesToolChoice`, `reasoningObject`, `appendUnique`.

Model-prefix branches that disappear (spec §8.3), each replaced by the cap the overlay sets: `responsesLiteModel` → `Caps.ResponsesLite`; `codexModelVariants`/`wireModel` → `res.WireID`; `defaultImageDetail` → `Caps.ImageDetail` (`""` = `high`, `omit` = no `detail` key); `reasoningSummaryLevel` → `Caps.ReasoningSummary`; every `usesCodexBackend()` gate → nothing (the field is emitted and the Codex rows' `Fields` prune it); `openAICodexUnsupportedRequestField` → gone; `parallel_tool_calls`, `text.verbosity`, `reasoning.context` → never emitted by the builder (`Transport.Body` constants on the Codex rows); `stop` → never emitted (not a Responses parameter); `client_metadata` → the Codex preparer (Task 5).

- [ ] **Step 1: Write the failing tests**

`llm/providers/responses/prunable_test.go`: as in Task 6 with `registry.ProtocolOpenAIResponses`.

`llm/providers/responses/request_test.go`:

```go
package responses

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func resolved(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses)}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "openai", Protocol: registry.ProtocolOpenAIResponses, ModelID: "gpt-5.5", WireID: "gpt-5.5", Transport: registry.Transport{Endpoint: "/responses"}, Caps: caps}
}

func openaiCaps(c *registry.Caps) {
	c.Reasoning = new(true)
	c.ReasoningControls = []string{"effort"}
	c.StrictTools = new(true)
	c.ReasoningSummary = new("auto")
	c.WebSearch = new(true)
	for _, f := range []string{"store", "prompt_cache_key", "include", "truncation", "safety_identifier", "service_tier", "previous_response_id", "conversation"} {
		c.Fields[f] = true
	}
}

func codexLiteCaps(c *registry.Caps) {
	openaiCaps(c)
	c.ResponsesLite = new(true)
	c.ThinkingAlwaysOn = new(true)
	c.ImageDetail = new("omit")
	c.ReasoningSummary = new("detailed")
}

func userReq(text string) llm.Request {
	return llm.Request{Model: "gpt-5.5", Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "be terse"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: text}}},
	}}
}

func build(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildBody_GroqBaselineSendsOnlyTheSpecFields(t *testing.T) {
	high := "high"
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}}}
	req.ReasoningEffort = &high
	req.MaxTokens = new(100)
	req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"type": "object"}}
	req.StopSequences = []string{"x"}
	res := resolved(func(c *registry.Caps) { c.Reasoning = new(true); c.ReasoningControls = []string{"effort"} })
	res.Instance, res.ModelID, res.WireID = "groq", "openai/gpt-oss-120b", "openai/gpt-oss-120b"
	body := build(t, req, res)
	for _, k := range []string{"model", "instructions", "input", "tools", "max_output_tokens", "reasoning", "text", "include"} {
		if _, has := body[k]; !has {
			t.Fatalf("missing %s: %s", k, jsonOf(t, body))
		}
	}
	for _, k := range []string{"stop", "store", "parallel_tool_calls", "metadata", "previous_response_id", "truncation"} {
		if _, has := body[k]; has {
			t.Fatalf("%s must not be built: %s", k, jsonOf(t, body))
		}
	}
	fn := body["tools"].([]map[string]any)[0]
	if _, has := fn["strict"]; has || fn["parameters"].(map[string]any)["additionalProperties"] != nil {
		t.Fatalf("no strict at baseline: %s", jsonOf(t, body))
	}
	if r := body["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != nil {
		t.Fatalf("reasoning: %s", jsonOf(t, body))
	}
	if body["model"] != "openai/gpt-oss-120b" || body["instructions"] != "be terse" {
		t.Fatalf("model/instructions: %s", jsonOf(t, body))
	}
	pruned := registry.Prune(body, res.Caps)
	if len(pruned) != 1 || pruned[0] != "include" {
		t.Fatalf("the baseline prunes include and nothing else that was built: %v", pruned)
	}
}

func TestBuildBody_OpenAIRow(t *testing.T) {
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}}}
	req.WebSearch = true
	req.Store = nil
	req.Include = []string{"web_search_call.results"}
	req.Metadata = map[string]string{"k": "v"}
	req.PreviousResponseID = "resp_prev"
	body := build(t, req, resolved(openaiCaps))
	tools := body["tools"].([]map[string]any)
	if len(tools) != 2 || tools[0]["strict"] != true || tools[0]["parameters"].(map[string]any)["additionalProperties"] != false || tools[1]["type"] != "web_search" {
		t.Fatalf("tools: %s", jsonOf(t, body))
	}
	if body["store"] != false || body["previous_response_id"] != "resp_prev" || body["metadata"] == nil {
		t.Fatalf("control fields: %s", jsonOf(t, body))
	}
	if _, has := body["reasoning"]; has {
		t.Fatalf("no effort and no always-on means no reasoning object: %s", jsonOf(t, body))
	}
	if inc := body["include"].([]string); len(inc) != 1 || inc[0] != "web_search_call.results" {
		t.Fatalf("include without a reasoning object carries only the caller's entries: %v", inc)
	}
	store := true
	req.Store = &store
	high := "high"
	req.ReasoningEffort = &high
	body = build(t, req, resolved(openaiCaps))
	if body["store"] != true {
		t.Fatal("an explicit Store overrides the privacy default")
	}
	if r := body["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != "auto" {
		t.Fatalf("reasoning: %s", jsonOf(t, body))
	}
	if inc := body["include"].([]string); len(inc) != 2 || inc[1] != "reasoning.encrypted_content" {
		t.Fatalf("include: %v", inc)
	}
	noWeb := resolved(openaiCaps)
	noWeb.Caps.WebSearch = new(false)
	if tools := build(t, req, noWeb)["tools"].([]map[string]any); len(tools) != 1 {
		t.Fatal("WebSearch=false drops the web_search tool")
	}
}

func TestBuildBody_CodexLite(t *testing.T) {
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
	res := resolved(codexLiteCaps)
	res.Instance, res.WireID = "openai-codex", "gpt-5.6-sol"
	body := build(t, req, res)
	if body["model"] != "gpt-5.6-sol" || body["instructions"] != "" {
		t.Fatalf("lite: %s", jsonOf(t, body))
	}
	if _, has := body["tools"]; has {
		t.Fatalf("lite moves tools into input: %s", jsonOf(t, body))
	}
	input := body["input"].([]any)
	first := input[0].(map[string]any)
	if first["type"] != "additional_tools" || first["role"] != "developer" || len(first["tools"].([]any)) != 1 {
		t.Fatalf("additional_tools item: %s", jsonOf(t, first))
	}
	if second := input[1].(map[string]any); second["type"] != "message" || second["role"] != "developer" {
		t.Fatalf("developer instructions item: %s", jsonOf(t, second))
	}
	if r := body["reasoning"].(map[string]any); r["summary"] != "detailed" || r["effort"] != nil {
		t.Fatalf("always-on without an effort sends the summary alone: %s", jsonOf(t, body))
	}
	for _, k := range []string{"parallel_tool_calls", "text"} {
		if _, has := body[k]; has {
			t.Fatalf("%s is a body constant, not a builder field: %s", k, jsonOf(t, body))
		}
	}
	empty := userReq("hi")
	if first := build(t, empty, res)["input"].([]any)[0].(map[string]any); len(first["tools"].([]any)) != 0 {
		t.Fatal("additional_tools is always present, even empty")
	}
}

func TestBuildBody_ImageDetailAndStructuredOutput(t *testing.T) {
	img := llm.Request{Model: "gpt-5.5", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "see"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://img.example/a.png"}},
	}}}}
	detailOf := func(body map[string]any) any {
		parts := body["input"].([]any)[0].(map[string]any)["content"].([]any)
		return parts[1].(map[string]any)["detail"]
	}
	if d := detailOf(build(t, img, resolved(nil))); d != "high" {
		t.Fatalf("baseline detail = %v", d)
	}
	if d := detailOf(build(t, img, resolved(func(c *registry.Caps) { c.ImageDetail = new("original") }))); d != "original" {
		t.Fatalf("original detail = %v", d)
	}
	if d := detailOf(build(t, img, resolved(func(c *registry.Caps) { c.ImageDetail = new("omit") }))); d != nil {
		t.Fatalf("omit must drop detail, got %v", d)
	}
	req := userReq("hi")
	req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"type": "object"}}
	format := build(t, req, resolved(func(c *registry.Caps) { c.StructuredOutput = new(false) }))["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Fatalf("StructuredOutput=false downgrades: %v", format)
	}
	req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	if tc := build(t, req, resolved(func(c *registry.Caps) { c.ToolChoiceForcing = new(false) }))["tool_choice"]; tc != "auto" {
		t.Fatalf("forcing off: %v", tc)
	}
	none := "none"
	req.ReasoningEffort = &none
	if _, has := build(t, req, resolved(openaiCaps))["reasoning"]; has {
		t.Fatal("none sends no reasoning object")
	}
	inPath := resolved(nil)
	inPath.Transport.Endpoint = "/openai/deployments/{model}/responses"
	if b := build(t, req, inPath); b["model"] != nil {
		t.Fatal("a {model} endpoint sends no model in the body")
	}
}
```

(`llm.ImageData{URL: …}` — use the field `toResponsesInput` reads for URL images, `llm/types.go:139`.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/responses/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Write `protocol.go` and `request.go`**

`llm/providers/responses/protocol.go`:

```go
// Package responses implements the OpenAI Responses wire protocol
// (registry.ProtocolOpenAIResponses) as an llm.Protocol driven by
// registry.Resolved (spec §8). It is the Responses half of the pre-registry
// openai adapter; the Codex transport's headers and body rules live in
// llm/providers/tokenauth and reach this package only through the
// RequestPreparer hook of protocolhttp.
package responses

import (
	"net/http"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the single registered openai-responses implementation.
// Client is nil for protocolhttp.DefaultClient. Hasher, when set, stamps
// resp.Raw["id_hash"] for the session's continuation bookkeeping (spec
// §7.6); step 3 wires it from the client's state root.
type Protocol struct {
	Client *http.Client
	Hasher *llm.ContinuationHasher
}

func init() { llm.RegisterProtocol(&Protocol{}) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolOpenAIResponses }

var prunablePaths = []string{
	"background", "conversation", "include", "max_output_tokens", "max_tool_calls", "metadata",
	"parallel_tool_calls", "previous_response_id", "prompt_cache_key", "prompt_cache_retention",
	"reasoning.context", "safety_identifier", "service_tier", "store", "temperature", "text.verbosity",
	"top_p", "truncation",
}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol for a non-streaming request.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	return buildBody(req, res, false)
}
```

`llm/providers/responses/request.go`:

```go
package responses

import (
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

const encryptedReasoning = "reasoning.encrypted_content"

// buildBody builds the Responses body for a shaped request (spec §8.2 step
// 1, §8.4, §9.5). Prunable control fields are emitted whenever the request
// carries them and pruned by the row's Fields; the lite shape, strict
// tools, image detail, and the reasoning object are cap decisions.
func buildBody(req llm.Request, res registry.Resolved, stream bool) (map[string]any, error) {
	caps := res.Caps
	lite := registry.BoolValue(caps.ResponsesLite)
	detail := "high"
	if d := registry.StringValue(caps.ImageDetail); d != "" {
		detail = d
	}
	instructions, inputItems, err := toResponsesInput(req.Messages, detail)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"instructions": instructions, "input": inputItems}
	if protocolhttp.ModelInBody(res) {
		body["model"] = res.WireID
	}
	if caps.Fields["store"] {
		// Today's privacy default: never let the endpoint store the turn
		// unless a continuation plan asked for it through req.Store.
		body["store"] = false
		if req.Store != nil {
			body["store"] = *req.Store
		}
	}
	var tools []map[string]any
	if len(req.Tools) > 0 {
		tools = toResponsesTools(req.Tools, registry.BoolValue(caps.StrictTools))
	}
	if req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch) {
		tools = append(tools, map[string]any{"type": "web_search"})
	}
	if lite {
		// codex-rs build_responses_request: tools ride as a developer
		// additional_tools item (always present, even empty), then the
		// instructions as a developer message, and the top-level fields go
		// empty.
		toolsAny := make([]any, 0, len(tools))
		for _, t := range tools {
			toolsAny = append(toolsAny, t)
		}
		prefix := []any{map[string]any{"type": "additional_tools", "role": "developer", "tools": toolsAny}}
		if instructions != "" {
			prefix = append(prefix, map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": instructions}}})
		}
		body["input"] = append(prefix, inputItems...)
		body["instructions"] = ""
	} else if len(tools) > 0 {
		body["tools"] = tools
	}
	if req.ToolChoice != nil {
		tc, err := toResponsesToolChoice(*req.ToolChoice)
		if err != nil {
			return nil, err
		}
		if caps.ToolChoiceForcing != nil && !*caps.ToolChoiceForcing && tc != "auto" && tc != "none" {
			tc = "auto"
		}
		body["tool_choice"] = tc
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body["max_output_tokens"] = *req.MaxTokens
	}
	for key, value := range map[string]string{
		"prompt_cache_key": req.PromptCacheKey, "prompt_cache_retention": req.PromptCacheRetention,
		"previous_response_id": req.PreviousResponseID, "conversation": req.ConversationID,
		"service_tier": req.ServiceTier, "safety_identifier": req.SafetyIdentifier, "truncation": req.Truncation,
	} {
		if v := strings.TrimSpace(value); v != "" {
			body[key] = v
		}
	}
	if req.MaxToolCalls != nil {
		body["max_tool_calls"] = *req.MaxToolCalls
	}
	if req.Background != nil {
		body["background"] = *req.Background
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	reasoningOff := caps.Reasoning != nil && !*caps.Reasoning
	if reasoning := reasoningObject(req, caps); reasoning != nil {
		body["reasoning"] = reasoning
		body["include"] = appendUnique(slices.Clone(req.Include), encryptedReasoning)
	} else if len(req.Include) > 0 {
		body["include"] = slices.Clone(req.Include)
	}
	if req.ResponseFormat != nil {
		structured := caps.StructuredOutput == nil || *caps.StructuredOutput
		if format := toResponsesResponseFormat(*req.ResponseFormat, structured); format != nil {
			text, _ := body["text"].(map[string]any)
			if text == nil {
				text = map[string]any{}
			}
			text["format"] = format
			body["text"] = text
		}
	}
	if stream {
		body["stream"] = true
	}
	if options, ok := req.ProviderOptions[registry.ProtocolOpenAIResponses].(map[string]any); ok {
		for k, v := range options {
			if reasoningOff && (k == "reasoning" || k == "include") {
				continue
			}
			body[k] = v
		}
	}
	return body, nil
}

// reasoningObject is spec §8.4 for openai-responses: effort when set and
// the row is effort-capable, summary from ReasoningSummary (omitted at
// none), and with ThinkingAlwaysOn and no effort the summary alone. nil
// means no reasoning object; none sends nothing.
func reasoningObject(req llm.Request, caps registry.Caps) map[string]any {
	if caps.Reasoning != nil && !*caps.Reasoning {
		return nil
	}
	out := map[string]any{}
	if req.ReasoningEffort != nil && *req.ReasoningEffort != "none" && caps.EffortCapable() {
		out["effort"] = *req.ReasoningEffort
	}
	summary := registry.StringValue(caps.ReasoningSummary)
	if summary == "none" {
		summary = ""
	}
	if summary != "" && (len(out) > 0 || registry.BoolValue(caps.ThinkingAlwaysOn)) {
		out["summary"] = summary
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
```

- [ ] **Step 4: Port `input.go`**

Create `llm/providers/responses/input.go` from `llm/providers/openai/responses.go`: `toResponsesInput` (1066-1305) with the signature `toResponsesInput(msgs []llm.Message, imageDetail string) (instructions string, items []any, err error)` — inside, the `responsesLiteModel(model)` checks around image `detail` (1159-1174, 1280-1297) become `if imageDetail != "omit"`, and `defaultImageDetail(model)` becomes `imageDetail` (the caller's `p.Image.Detail` still wins when set); `reasoningSummaryInput` and `parseReasoningSummary` (1307-1345) unchanged; `toResponsesTools` (858-886) with the signature `toResponsesTools(tools []llm.ToolDefinition, strict bool)` where the whole strict branch (the `strict` key and the `openaichat.StrictifyJSONSchema` call) runs only when `strict` is true, keeping today's per-tool explicit-`false` opt-out inside it; `toResponsesResponseFormat` (838-856) with the signature `(rf llm.ResponseFormat, structured bool) any`, returning `map[string]any{"type": "json_object"}` for `json_schema` when `!structured`; `toResponsesToolChoice` (975-1005) unchanged. Copy `llm/providers/openai/image_input.go` in full as `image_input.go` (the same functions, unchanged; the old package keeps its copy until step 3 deletes it — this is the one deliberate duplicate in the plan, ledger it).

- [ ] **Step 5: Run the tests**

Run: `go test ./llm/providers/responses/ -count=1 && go vet ./llm/providers/responses/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add llm/providers/responses
git commit -m "feat(responses): Resolved-driven Responses body builder"
```

---

### Task 9: `responses` — Complete, Stream, ListModels, CountTokens, fingerprint

**Files:**
- Create: `llm/providers/responses/complete.go`, `stream.go`, `response.go`, `models.go`, `tokens.go`, `fingerprint.go`
- Test: `llm/providers/responses/transport_test.go`, `models_test.go`, `fingerprint_test.go`, `requestbuild_fuzz_test.go`, `stream_fuzz_test.go`
- Modify: `scripts/fuzz/fuzz-targets.txt`
- Source to copy from (read-only): `llm/providers/openai/responses.go:316-800` (accumulator, in-band error, event table, `decodeResponsesStream`), `1347-1517` (`responseContentFromOutputItems`, `settleResponsesTerminalOutput`, `fromResponses`), `adapter.go:1077-1107` (`parseUsage`), `models.go`, `token_count.go:90-116`, `responses_continuation_fingerprint.go`

**Interfaces:**
- Consumes: `protocolhttp.*`, `transport.FatalStreamError`, `llm.ParseSSE`, `llm.StreamReadSSEOptions`, `llm.ResponsesEndpointFamily` constants (`llm/responses_continuation.go:63-72`), `llm.ContinuationHasher.HashContinuationHandle`.
- Produces: the complete `llm.Protocol` for `openai-responses`; `responses.EndpointFamily(res registry.Resolved) llm.ResponsesEndpointFamily`; `responses.RequestFingerprint(family llm.ResponsesEndpointFamily, body map[string]any) (string, error)` (step 3's continuation planner uses both).

- [ ] **Step 1: Write the failing tests**

`llm/providers/responses/transport_test.go`:

```go
package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const responseJSON = `{"id":"resp_1","status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"f","arguments":"{\"a\":1}"}],"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":2},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":16}}`

const responseSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"hel\"}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"lo\"}\n\n" +
	"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"

type capturedRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

func server(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.header = r.URL.RequestURI(), r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		if strings.HasPrefix(body, "event:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func liveRes(srv *httptest.Server, mutate func(c *registry.Caps)) registry.Resolved {
	res := resolved(mutate)
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/responses", StreamEndpoint: "/responses", ModelsEndpoint: "/models", CountTokensEndpoint: "/responses/input_tokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	return res
}

// streamingPreparer stands in for the Codex transport: RequiresStreamingComplete.
type streamingPreparer struct{}

func (streamingPreparer) Apply(_ context.Context, req *http.Request, _ registry.Resolved) error {
	req.Header.Set("Authorization", "Bearer codex-token")
	return nil
}
func (streamingPreparer) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}
func (streamingPreparer) RequiresStreamingComplete() bool { return true }

var registerOnce sync.Once

func TestCompleteDecodesOutputItems(t *testing.T) {
	srv, got := server(t, 200, responseJSON)
	res := liveRes(srv, openaiCaps)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "openai" || resp.Text() != "hello" || len(resp.ToolCalls()) != 1 || resp.ToolCalls()[0].ID != "call_1" || resp.Finish.Reason != "tool_calls" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 4 || resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if got.path != "/v1/responses" || got.header.Get("Authorization") != "Bearer k-1" || got.body["store"] != false {
		t.Fatalf("wire: %s %v %v", got.path, got.header, got.body)
	}
}

func TestStreamEmitsDeltasAndFinish(t *testing.T) {
	srv, got := server(t, 200, responseSSE)
	res := liveRes(srv, openaiCaps)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	var final *llm.Response
	for ev := range s.Events() {
		switch ev.Type {
		case llm.StreamEventTextDelta:
			deltas = append(deltas, ev.Delta)
		case llm.StreamEventFinish:
			final = ev.Response
		case llm.StreamEventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if strings.Join(deltas, "") != "hello" || final == nil || final.Provider != "openai" || final.Text() != "hello" || final.Usage.TotalTokens != 5 {
		t.Fatalf("deltas = %v final = %+v", deltas, final)
	}
	if got.body["stream"] != true {
		t.Fatalf("stream flag: %v", got.body)
	}
}

func TestCompleteThroughStreamWhenTheTransportRequiresIt(t *testing.T) {
	registerOnce.Do(func() { llm.RegisterAuthenticator("test-streaming-codex", streamingPreparer{}) })
	srv, got := server(t, 200, responseSSE)
	res := liveRes(srv, codexLiteCaps)
	res.Transport.Auth = "test-streaming-codex"
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "hello" || got.body["stream"] != true || got.header.Get("Authorization") != "Bearer codex-token" {
		t.Fatalf("complete via stream: %+v %v", resp, got.body)
	}
}

func TestCompleteClassifiesFailures(t *testing.T) {
	srv, _ := server(t, 400, `{"error":{"message":"Unknown parameter: 'store'.","type":"invalid_request_error","param":"store","code":"unknown_parameter"}}`)
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
	if llm.Kind(err) != llm.KindInvalidRequest || !strings.Contains(llm.ErrorHint(err), "fields.store = false") || llm.ErrorProtocol(err) != registry.ProtocolOpenAIResponses {
		t.Fatalf("err = %v", err)
	}
}

func TestCountTokens(t *testing.T) {
	srv, got := server(t, 200, `{"object":"response.input_tokens","input_tokens":42}`)
	res := liveRes(srv, openaiCaps)
	req := userReq("hi")
	req.MaxTokens = new(10)
	req.Metadata = map[string]string{"k": "v"}
	n, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), req, res)
	if err != nil || n != 42 {
		t.Fatalf("n = %d err = %v", n, err)
	}
	if got.path != "/v1/responses/input_tokens" {
		t.Fatalf("path = %s", got.path)
	}
	for _, k := range []string{"max_output_tokens", "metadata", "store", "temperature"} {
		if _, has := got.body[k]; has {
			t.Fatalf("%s must be stripped from the count body: %v", k, got.body)
		}
	}
	res.Transport.CountTokensEndpoint = registry.EndpointUnsupported
	if _, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), req, res); err != llm.ErrInputTokenCountUnsupported {
		t.Fatalf("err = %v", err)
	}
}
```

`llm/providers/responses/models_test.go`:

```go
package responses

import (
	"context"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestListModelsPlatformAndCodexShapes(t *testing.T) {
	srv, got := server(t, 200, `{"data":[{"id":"gpt-5.5","context_window":400000,"max_output_tokens":128000},{"id":"text-embedding-3-large"}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil || got.path != "/v1/models" {
		t.Fatalf("err = %v path = %s", err, got.path)
	}
	if len(rows) != 1 || rows[0].ID != "gpt-5.5" || *rows[0].Caps.ContextWindow != 400000 || *rows[0].Caps.MaxOutputTokens != 128000 {
		t.Fatalf("rows = %+v", rows)
	}

	srv2, got2 := server(t, 200, `{"models":[{"slug":"gpt-5.6-sol","display_name":"GPT-5.6","context_window":272000,"max_output_tokens":128000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"input_modalities":["text","image"]}]}`)
	res := liveRes(srv2, nil)
	res.Transport.ModelsEndpoint = "/models?client_version=0.0.0"
	rows, err = (&Protocol{Client: srv2.Client()}).ListModels(context.Background(), res)
	if err != nil || got2.path != "/v1/models?client_version=0.0.0" {
		t.Fatalf("err = %v path = %s", err, got2.path)
	}
	c := rows[0].Caps
	if rows[0].ID != "gpt-5.6-sol" || *c.ContextWindow != 272000 || len(c.EffortValues) != 2 || c.EffortValues[1] != "high" || !*c.Reasoning || len(c.InputModalities) != 2 {
		t.Fatalf("codex row = %+v", rows[0])
	}
	res.Transport.ModelsEndpoint = registry.EndpointUnsupported
	if _, err := (&Protocol{Client: srv2.Client()}).ListModels(context.Background(), res); err != llm.ErrModelListingUnsupported {
		t.Fatalf("err = %v", err)
	}
}
```

`llm/providers/responses/fingerprint_test.go`:

```go
package responses

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestRequestFingerprintIsStableAcrossBuildsAndStreaming(t *testing.T) {
	res := resolved(openaiCaps)
	req := userReq("hi")
	req.PreviousResponseID = "resp_a"
	a := build(t, req, res)
	b, _ := buildBody(llm.ShapeRequest(req, res), res, true)
	req.PreviousResponseID = "resp_b"
	c := build(t, req, res)
	fa, err := RequestFingerprint(EndpointFamily(res), a)
	if err != nil || !strings.HasPrefix(fa, "cont-req-v1:") {
		t.Fatalf("fingerprint = %q err = %v", fa, err)
	}
	fb, _ := RequestFingerprint(EndpointFamily(res), b)
	fc, _ := RequestFingerprint(EndpointFamily(res), c)
	if fa != fb || fa != fc {
		t.Fatalf("stream and previous_response_id must not change the fingerprint: %s %s %s", fa, fb, fc)
	}
	other := userReq("bye")
	other.Temperature = new(0.5)
	fo, _ := RequestFingerprint(EndpointFamily(res), build(t, other, res))
	if fo == fa {
		t.Fatal("a different body must fingerprint differently")
	}
	codex := resolved(codexLiteCaps)
	codex.Transport.Auth = registry.AuthOAuthOpenAICodex
	if EndpointFamily(codex) != llm.ResponsesEndpointFamilyOpenAICodex || EndpointFamily(res) != llm.ResponsesEndpointFamilyOpenAIPublic {
		t.Fatal("endpoint family follows the transport's auth scheme")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/responses/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Write `complete.go`, `tokens.go`, `fingerprint.go`**

`llm/providers/responses/complete.go`:

```go
package responses

import (
	"context"
	"fmt"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, method, url string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	return &protocolhttp.Call{Operation: operation, EndpointFamily: string(EndpointFamily(res)), Method: method, URL: url, Body: body, Req: req, Res: res, Client: p.Client}
}

// Complete implements llm.Protocol. Transports that answer only streams
// (Codex) are driven through Stream and accumulated.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := buildBody(req, res, false)
	if err != nil {
		return llm.Response{}, err
	}
	var out llm.Response
	err = protocolhttp.Do(ctx, p.call("responses.create", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		if r.Raw == nil {
			return nil, fmt.Errorf("responses.create: response is not a JSON object")
		}
		resp := fromResponses(r.Raw, req.Model)
		p.stampResponseIDHash(&resp)
		llm.StampEndpointURL(&resp, r.EndpointURL, r.Material)
		resp.RateLimit = llm.ParseRateLimitHeaders(r.Header)
		out = resp
		return &out, nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	return out, nil
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := buildBody(req, res, true)
	if err != nil {
		return nil, err
	}
	call := p.call("responses.create(stream)", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		p.decodeStream(sctx, cancel, resp, s, req, res, r, attempt)
	})
}

// stampResponseIDHash records the redaction-keyed hash of the response id
// for the session's continuation bookkeeping when a hasher is configured.
func (p *Protocol) stampResponseIDHash(resp *llm.Response) {
	if p.Hasher == nil || resp == nil || resp.ID == "" {
		return
	}
	hash, err := p.Hasher.HashContinuationHandle("response_id", resp.ID)
	if err != nil {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["id_hash"] = hash
}
```

(`stampResponseIDHash` mirrors `llm/providers/openai/adapter.go:668-680`; keep whatever `Raw` key and hasher call that function uses if they differ from the above.)

`llm/providers/responses/tokens.go`:

```go
package responses

import (
	"context"
	"fmt"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// inputTokenCountOutputFields are the fields /responses/input_tokens
// rejects: everything that shapes the output rather than the input.
var inputTokenCountOutputFields = []string{
	"background", "include", "max_output_tokens", "max_tool_calls", "metadata", "prompt_cache_retention",
	"safety_identifier", "service_tier", "store", "temperature", "top_p",
}

// CountTokens implements llm.Protocol: POST the completion body, minus the
// output fields, to the count-tokens endpoint (spec §8.1).
func (p *Protocol) CountTokens(ctx context.Context, req llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	body, err := buildBody(req, res, false)
	if err != nil {
		return 0, err
	}
	for _, f := range inputTokenCountOutputFields {
		delete(body, f)
	}
	var count int
	err = protocolhttp.Do(ctx, p.call("responses.input_tokens", http.MethodPost, protocolhttp.URL(res, res.Transport.CountTokensEndpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		n, ok := r.Raw["input_tokens"].(float64)
		if !ok {
			return nil, fmt.Errorf("responses.input_tokens: missing input_tokens in %q", r.Body)
		}
		count = int(n)
		return nil, nil
	})
	return count, err
}
```

`llm/providers/responses/fingerprint.go`: move `requestFingerprintForResponsesBody` and `responsesRequestFingerprintExcludedFields` from `llm/providers/openai/responses_continuation_fingerprint.go` as `RequestFingerprint` and `excludedFingerprintFields`, add `"stream"` to the always-excluded set (spec §13 "Continuation": the fingerprint must agree across `Complete` and `Stream`), keep the `cont-req-v1:` prefix and the sha256/base64url encoding, and add:

```go
// EndpointFamily reports which Responses endpoint family the instance's
// transport talks to (spec §7.6): the Codex transport's backend, or the
// public API everything else shares.
func EndpointFamily(res registry.Resolved) llm.ResponsesEndpointFamily {
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		return llm.ResponsesEndpointFamilyOpenAICodex
	}
	return llm.ResponsesEndpointFamilyOpenAIPublic
}
```

- [ ] **Step 4: Port the decoder, response decoding, and models**

`response.go`: copy `responseContentFromOutputItems`, `settleResponsesTerminalOutput`, and `fromResponses` from `llm/providers/openai/responses.go:1347-1517` and `parseUsage` from `adapter.go:1077-1107`, unchanged except `Provider: "openai"` (the runner stamps `res.Instance`).

`stream.go`: copy lines 316-611 (`responsesToolState`, `responsesOutputAccumulator` and its methods, `responsesInbandError`, `responsesAPIEventTypes`) unchanged, and `decodeResponsesStream` (613-800) as `func (p *Protocol) decodeStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, res registry.Resolved, r *protocolhttp.Result, attempt *transport.APIAttemptCapture)`, then: `a.responsesURL()` → `r.EndpointURL`; `a.apiLogCredentialMaterial(nil)` → `r.Material`; `a.stampResponseIDHash(x)` → `p.stampResponseIDHash(x)`; every `"openai"` provider literal → `res.Instance`; the in-band error from `responsesInbandError` passes through `llm.RewriteErrorProvider(err, res.Instance)` before it is wrapped in `transport.FatalStreamError`; the empty-stream sentinel stays a terminal `llm.NewStreamError(res.Instance, "responses stream closed with no events", nil)` (there is no chat fallback any more; the client never retries on it).

`models.go`: copy `openAIModelListEntry`, `codexReasoningLevel`, `codexModelListEntry`, `codexReasoningEfforts`, `codexSupportsVision`, `skipOpenAIModel` from `llm/providers/openai/models.go`, replacing `envvars.FirstNonEmpty` with a local `firstNonEmpty(values ...string) string`, and write:

```go
// ListModels implements llm.Protocol. The platform API answers with data[]
// and the Codex backend with models[]; both map to registry rows carrying
// only the facts they advertise.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	call := &protocolhttp.Call{Operation: "models.list", EndpointFamily: "openai_models", Method: http.MethodGet, URL: protocolhttp.URL(res, res.Transport.ModelsEndpoint), Req: llm.Request{Model: "*"}, Res: res, Client: p.Client}
	var rows []registry.Model
	err := protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
		var payload struct {
			Data   []openAIModelListEntry `json:"data"`
			Models []codexModelListEntry  `json:"models"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		for _, e := range payload.Data {
			if e.ID != "" && !skipOpenAIModel(e.ID) {
				rows = append(rows, e.row())
			}
		}
		for _, e := range payload.Models {
			if id := e.id(); id != "" && !skipOpenAIModel(id) {
				rows = append(rows, e.row())
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		return nil, nil
	})
	return rows, err
}
```

with `(openAIModelListEntry).row()` mapping `ID`, `ContextWindow` (first positive of `context_window`, `max_context_window`, `max_input_tokens`, `input_token_limit`), `MaxOutputTokens` (first positive of `max_output_tokens`, `output_token_limit`), and `(codexModelListEntry).row()` mapping `ID` = `firstNonEmpty(slug, id, model)`, the same window/output fields, `EffortValues` = `codexReasoningEfforts(levels)`, `Reasoning = true` when levels are non-empty, `InputModalities` verbatim. Replace the old `.modelInfo()` methods (they built `llm.ModelInfo`, which the registry does not use).

- [ ] **Step 5: Run the tests**

Run: `go test ./llm/providers/responses/ -count=1 -race`
Expected: PASS.

- [ ] **Step 6: Fuzz targets**

Create `requestbuild_fuzz_test.go` (`FuzzResponsesBuildBody`, from `llm/providers/openai`'s `FuzzOpenAIResponsesRequestBuild` in `requestbuild_fuzz_test.go`, flipping `ResponsesLite`, `StrictTools`, `ImageDetail`, `ReasoningSummary`, `ThinkingAlwaysOn`, `StructuredOutput`, `Reasoning`; property: no panic, marshals, `registry.Prune` no panic, and when `ResponsesLite` is set `input[0].type == "additional_tools"` and `tools` is absent) and `stream_fuzz_test.go` (`FuzzResponsesStreamMetamorphic`, from the file defining `FuzzOpenAIResponsesMetamorphic`, driving `(&Protocol{Client: srv.Client()}).Stream(ctx, req, liveRes(srv, openaiCaps))`). Add:

```
native:llm:./providers/responses:FuzzResponsesBuildBody
native:llm:./providers/responses:FuzzResponsesStreamMetamorphic
```

Run: `go test ./llm/providers/responses/ -run Fuzz -fuzz FuzzResponsesBuildBody -fuzztime 10s && go test ./llm/providers/responses/ -run Fuzz -fuzz FuzzResponsesStreamMetamorphic -fuzztime 10s && make fuzz-registry-check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add llm/providers/responses scripts/fuzz/fuzz-targets.txt
git commit -m "feat(responses): transport, stream decoding, listing, token counting, fingerprint"
```

---

### Task 10: `anthropic.Protocol`

**Files:**
- Modify: `llm/providers/anthropic/request.go:63-130` and `:188-242` (extract `applyAnthropicTools` and `reconcileThinkingContract` so both builders share them), `llm/providers/anthropic/adapter.go:402-814` (`decodeStream` becomes the package function `decodeMessagesStream`; the method becomes a one-line call)
- Create: `llm/providers/anthropic/protocol.go`, `protocol_request.go`, `protocol_transport.go`
- Test: `llm/providers/anthropic/protocol_test.go`, `protocol_transport_test.go`, `protocol_fuzz_test.go`
- Modify: `scripts/fuzz/fuzz-targets.txt`

**Interfaces:**
- Consumes: `toAnthropicMessages`, `applyAnthropicResponseFormat`, `betaHeaderFromProviderOptions`, `fromAnthropicResponse`, `parseUsage`, `inbandStreamError`, `fallbackMaxTokens`, `llm.ReasoningBudget`, `llm.RewriteErrorProvider`, `protocolhttp.*`, `registry.BoolValue/StringValue`.
- Produces: `anthropic.Protocol{Client}` (complete `llm.Protocol`); package functions `applyAnthropicTools(body map[string]any, req llm.Request, webSearch bool) error`, `reconcileThinkingContract(body map[string]any, maxTokens, thinkingBudget, maxCap int)`, `decodeMessagesStream(sctx, cancel, resp, s, req, provider, endpointURL string, material llm.APILogCredentialMaterial, attempt)`; `const anthropicVersion = "2023-06-01"`.

What changes against the old builder (spec §8.3, §8.4, §9.3, §9.4): the `[1m]` strip, `isClaude5OrNewer`, and the three `LookupModelInfo` calls are gone (the row's `WireID`, `ThinkingShape`, `ThinkingDisplay`, `ThinkingAlwaysOn`, `MaxOutputTokens`, and `EffortValues` say everything); `model` is omitted when the endpoint carries `{model}` (Vertex); `metadata.user_id` and `service_tier` are emitted from the request (prunable, off at baseline); `WebSearch = false` drops the web-search tool (Bedrock); the beta header merges the row's `anthropic-beta` (the `[1m]` alias rows) with the caller's `beta_headers`; `cache_control` markers stay unconditional (they are the protocol's native shape) and gain `ttl` from `CacheTTL` when set; `ListModels` returns the listing verbatim (the `[1m]` rows are curated aliases now).

- [ ] **Step 1: Write the failing tests**

`llm/providers/anthropic/protocol_test.go`:

```go
package anthropic

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func protoRes(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolAnthropic), MaxOutputTokens: new(64000), Reasoning: new(true), ReasoningControls: []string{"effort"}}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "anthropic", Protocol: registry.ProtocolAnthropic, ModelID: "claude-x", WireID: "claude-x-wire", Transport: registry.Transport{Endpoint: "/messages"}, Caps: caps}
}

func protoReq(effort string) llm.Request {
	req := llm.Request{Model: "claude-x", Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
	}}
	if effort != "" {
		req.ReasoningEffort = &effort
	}
	return req
}

func protoBuild(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestProtocolPrunablePathsMatchRegistry(t *testing.T) {
	p, ok := llm.ProtocolFor(registry.ProtocolAnthropic)
	if !ok {
		t.Fatal("anthropic not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolAnthropic); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}

func TestProtocolBuildBody_ThinkingShapes(t *testing.T) {
	cases := []struct {
		name         string
		shape        string
		display      string
		alwaysOn     bool
		effort       string
		wantThinking map[string]any
		wantEffort   any // output_config.effort, nil = absent
	}{
		{"unset shape sends nothing", "", "", false, "high", nil, nil},
		{"adaptive always-on no effort no display", "adaptive", "", true, "", map[string]any{"type": "adaptive"}, nil},
		{"adaptive with display and effort", "adaptive", "summarized", true, "high", map[string]any{"type": "adaptive", "display": "summarized"}, "high"},
		{"adaptive not always-on and no effort", "adaptive", "summarized", false, "", nil, nil},
		{"budget", "budget", "", false, "medium", map[string]any{"type": "enabled", "budget_tokens": float64(llm.ReasoningBudget("medium"))}, nil},
		{"budget without effort", "budget", "", false, "", nil, nil},
		{"budget+effort", "budget+effort", "", false, "high", map[string]any{"type": "enabled", "budget_tokens": float64(llm.ReasoningBudget("high"))}, "high"},
		{"none clears everything", "adaptive", "summarized", true, "none", map[string]any{"type": "adaptive", "display": "summarized"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := protoRes(func(caps *registry.Caps) {
				if c.shape != "" {
					caps.ThinkingShape = new(c.shape)
				}
				if c.display != "" {
					caps.ThinkingDisplay = new(c.display)
				}
				if c.alwaysOn {
					caps.ThinkingAlwaysOn = new(true)
				}
			})
			raw, _ := json.Marshal(protoBuild(t, protoReq(c.effort), res))
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if !reflect.DeepEqual(body["thinking"], anyOrNil(c.wantThinking)) {
				t.Fatalf("thinking = %v, want %v", body["thinking"], c.wantThinking)
			}
			var gotEffort any
			if oc, ok := body["output_config"].(map[string]any); ok {
				gotEffort = oc["effort"]
			}
			if gotEffort != c.wantEffort {
				t.Fatalf("output_config.effort = %v, want %v", gotEffort, c.wantEffort)
			}
		})
	}
}

func anyOrNil(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

func TestProtocolBuildBody_CapsAndRequestFields(t *testing.T) {
	req := protoReq("high")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
	req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	req.WebSearch = true
	req.Metadata = map[string]string{"user_id": "u1", "trace": "t"}
	req.ServiceTier = "auto"
	req.StopSequences = []string{"END"}
	res := protoRes(func(c *registry.Caps) { c.ThinkingShape = new("budget"); c.CacheTTL = new("1h") })
	body := protoBuild(t, req, res)
	if body["model"] != "claude-x-wire" || body["max_tokens"].(int) <= llm.ReasoningBudget("high") {
		t.Fatalf("model/max_tokens: %v %v", body["model"], body["max_tokens"])
	}
	if body["metadata"].(map[string]any)["user_id"] != "u1" || len(body["metadata"].(map[string]any)) != 1 || body["service_tier"] != "auto" {
		t.Fatalf("metadata/service_tier: %v %v", body["metadata"], body["service_tier"])
	}
	tools := body["tools"].([]map[string]any)
	if len(tools) != 2 || tools[1]["type"] != "web_search_20250305" || tools[1]["cache_control"].(map[string]any)["ttl"] != "1h" {
		t.Fatalf("tools: %v", tools)
	}
	if tc := body["tool_choice"].(map[string]any); tc["type"] != "auto" {
		t.Fatalf("forcing must downgrade to auto while thinking is on: %v", tc)
	}
	if sys := body["system"].([]map[string]any)[0]["cache_control"].(map[string]any); sys["ttl"] != "1h" {
		t.Fatalf("system marker: %v", sys)
	}
	if got := registry.Prune(body, res.Caps); !reflect.DeepEqual(got, []string{"metadata", "service_tier"}) {
		t.Fatalf("baseline prunes metadata and service_tier: %v", got)
	}

	noWeb := protoRes(func(c *registry.Caps) { c.WebSearch = new(false) })
	if tools := protoBuild(t, req, noWeb)["tools"].([]map[string]any); len(tools) != 1 {
		t.Fatalf("WebSearch=false drops the web search tool: %v", tools)
	}

	vertex := protoRes(nil)
	vertex.Transport.Endpoint = "/publishers/anthropic/models/{model}:rawPredict"
	if b := protoBuild(t, protoReq(""), vertex); b["model"] != nil {
		t.Fatal("a {model} endpoint sends no model in the body")
	}

	unknown := protoRes(func(c *registry.Caps) { c.MaxOutputTokens = nil })
	if b := protoBuild(t, protoReq(""), unknown); b["max_tokens"] != fallbackMaxTokens {
		t.Fatalf("max_tokens fallback = %v", b["max_tokens"])
	}
}

func TestBetaHeaderMergesRowAndCallerBetas(t *testing.T) {
	res := protoRes(nil)
	res.Headers = map[string]string{"anthropic-beta": "context-1m-2025-08-07"}
	req := protoReq("")
	req.ProviderOptions = map[string]any{"anthropic": map[string]any{"beta_headers": []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"}}}
	if got := betaHeader(res, req); got != "context-1m-2025-08-07,interleaved-thinking-2025-05-14" {
		t.Fatalf("beta header = %q", got)
	}
	if got := betaHeader(protoRes(nil), protoReq("")); got != "" {
		t.Fatalf("no betas = %q", got)
	}
}
```

`llm/providers/anthropic/protocol_transport_test.go`:

```go
package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const messagesJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x-wire","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`

const messagesSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x-wire\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

type protoCapture struct {
	paths  []string
	header http.Header
	body   map[string]any
}

func protoServer(t *testing.T, handler func(r *http.Request) (int, string)) (*httptest.Server, *protoCapture) {
	t.Helper()
	got := &protoCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.paths = append(got.paths, r.URL.RequestURI())
		got.header = r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		status, body := handler(r)
		if strings.HasPrefix(body, "event:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func protoLive(srv *httptest.Server) registry.Resolved {
	res := protoRes(nil)
	res.Transport = registry.Transport{Auth: registry.AuthHeader, AuthHeader: "x-api-key", BaseURL: srv.URL + "/v1", Endpoint: "/messages", StreamEndpoint: "/messages", ModelsEndpoint: "/models", CountTokensEndpoint: "/messages/count_tokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	res.Headers = map[string]string{"anthropic-beta": "context-1m-2025-08-07"}
	return res
}

func TestProtocolCompleteAndHeaders(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return 200, messagesJSON })
	res := protoLive(srv)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "anthropic" || resp.Text() != "hello" || resp.Finish.Reason != "stop" || resp.Usage.InputTokens != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	h := got.header
	if got.paths[0] != "/v1/messages" || h.Get("x-api-key") != "k-1" || h.Get("anthropic-version") != anthropicVersion || h.Get("anthropic-beta") != "context-1m-2025-08-07" || h.Get("Authorization") != "" {
		t.Fatalf("wire: %v %v", got.paths, h)
	}
}

func TestProtocolStreamDecodesThroughTheSharedDecoder(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return 200, messagesSSE })
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), protoReq(""), protoLive(srv))
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil || final.Provider != "anthropic" || final.Text() != "hello" || final.Usage.OutputTokens != 3 || got.body["stream"] != true {
		t.Fatalf("final = %+v body = %v", final, got.body)
	}
}

func TestProtocolListModelsPaginatesAndCountTokensStrips(t *testing.T) {
	srv, got := protoServer(t, func(r *http.Request) (int, string) {
		switch {
		case strings.Contains(r.URL.RawQuery, "after_id=m2"):
			return 200, `{"data":[{"id":"m3","display_name":"M3"}],"has_more":false,"last_id":"m3"}`
		case strings.HasPrefix(r.URL.Path, "/v1/models"):
			return 200, `{"data":[{"id":"m2","display_name":"M2"},{"id":"m1","display_name":"M1"}],"has_more":true,"last_id":"m2"}`
		default:
			return 200, `{"input_tokens":21}`
		}
	})
	p := &Protocol{Client: srv.Client()}
	res := protoLive(srv)
	rows, err := p.ListModels(context.Background(), res)
	if err != nil || len(rows) != 3 || rows[0].ID != "m1" || rows[2].ID != "m3" {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	if len(got.paths) != 2 || !strings.HasPrefix(got.paths[0], "/v1/models?limit=1000") || !strings.Contains(got.paths[1], "after_id=m2") {
		t.Fatalf("paths = %v", got.paths)
	}
	req := protoReq("high")
	req.MaxTokens = new(10)
	req.StopSequences = []string{"x"}
	n, err := p.CountTokens(context.Background(), req, res)
	if err != nil || n != 21 || got.paths[len(got.paths)-1] != "/v1/messages/count_tokens" {
		t.Fatalf("count = %d err = %v paths = %v", n, err, got.paths)
	}
	for _, k := range []string{"max_tokens", "temperature", "top_p", "stop_sequences", "service_tier", "cache_control"} {
		if _, has := got.body[k]; has {
			t.Fatalf("%s must be stripped from the count body: %v", k, got.body)
		}
	}
	res.Transport.ModelsEndpoint, res.Transport.CountTokensEndpoint = registry.EndpointUnsupported, registry.EndpointUnsupported
	if _, err := p.ListModels(context.Background(), res); err != llm.ErrModelListingUnsupported {
		t.Fatalf("err = %v", err)
	}
	if _, err := p.CountTokens(context.Background(), req, res); err != llm.ErrInputTokenCountUnsupported {
		t.Fatalf("err = %v", err)
	}
}

func TestProtocolClassifiesPromptTooLong(t *testing.T) {
	srv, _ := protoServer(t, func(*http.Request) (int, string) {
		return 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`
	})
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	if llm.Kind(err) != llm.KindContextLength || llm.ErrorProtocol(err) != registry.ProtocolAnthropic {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/anthropic/ -run 'TestProtocol|TestBetaHeader' -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Extract the shared builder helpers**

In `llm/providers/anthropic/request.go`:

1. Move lines 78-130 of `buildRequestBody` (the `tool_choice` mapping, `tools`, the web-search tool, and the last-tool cache marker) into:

```go
// applyAnthropicTools writes tool_choice, tools, and the web-search tool
// (when webSearch is on), and marks the last tool for caching.
func applyAnthropicTools(body map[string]any, req llm.Request, webSearch bool) error
```

   replacing the moved code with `if err := applyAnthropicTools(body, req, req.WebSearch); err != nil { return nil, err }` and `req.WebSearch` inside the moved block with the `webSearch` parameter. The cache marker it writes stays `map[string]any{"type": "ephemeral"}`; add a package-level `cacheMarker(ttl string) map[string]any` returning that map plus `"ttl": ttl` when non-empty, use it in the moved block with `""`, and in the old builder's two other marker sites.
2. Move lines 188-242 (the forced-tool-choice downgrade, the `max_tokens > budget` reconciliation, and the invariants) into:

```go
// reconcileThinkingContract enforces the two contracts Anthropic rejects
// with a 400: no forced tool_choice while thinking is on, and max_tokens
// strictly above budget_tokens (raised by the budget, clamped to maxCap
// when that still satisfies the contract).
func reconcileThinkingContract(body map[string]any, maxTokens, thinkingBudget, maxCap int)
```

   with the old builder calling `reconcileThinkingContract(body, maxTokens, thinkingBudget, catalogMax)`.

Run `go test ./llm/providers/anthropic/ -count=1`: the old adapter's tests still PASS (this is a pure move).

- [ ] **Step 4: Extract the decoder**

In `llm/providers/anthropic/adapter.go` rename the method body of `decodeStream` (402-814) into the package function

```go
func decodeMessagesStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, provider, endpointURL string, material llm.APILogCredentialMaterial, attempt *transport.APIAttemptCapture)
```

with these substitutions inside: `a.BaseURL+"/v1/messages"` (the `llm.FinalResponseEndpointURL` fallback) → `endpointURL`; `a.apiLogCredentialMaterial(nil)` → `material`; every `Provider: "anthropic"` field literal → `provider`; every `"anthropic"` passed to `llm.NewStreamError`/`llm.WrapContextError` → `provider`; where `inbandStreamError(payload)` is called, pass its result through `llm.RewriteErrorProvider(err, provider)`; `llm.NormalizeFinishReason("anthropic", …)` keeps its literal (it names the finish-reason dialect, not the instance). The method becomes:

```go
func (a *Adapter) decodeStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, _ []byte, attempt *transport.APIAttemptCapture) {
	decodeMessagesStream(sctx, cancel, resp, s, req, a.Name(), a.BaseURL+"/v1/messages", a.apiLogCredentialMaterial(nil), attempt)
}
```

Run `go test ./llm/providers/anthropic/ -count=1`: PASS (the wire-capture and stream tests pin the behavior).

- [ ] **Step 5: Write the protocol**

`llm/providers/anthropic/protocol.go`:

```go
package anthropic

import (
	"net/http"
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const anthropicVersion = "2023-06-01"

// Protocol is the registry-driven Messages API implementation (spec §8),
// registered beside the pre-registry Adapter until step 3 deletes it.
type Protocol struct {
	Client *http.Client
}

func init() { llm.RegisterProtocol(&Protocol{}) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolAnthropic }

var prunablePaths = []string{"container", "fallbacks", registry.FieldMaxTokens, "metadata", "service_tier", "stop_sequences", "temperature", "top_p"}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	return buildProtocolBody(req, res)
}

// betaHeader merges the row's anthropic-beta header (the curated [1m] alias
// rows carry one) with the caller's beta_headers, comma-joined without
// duplicates.
func betaHeader(res registry.Resolved, req llm.Request) string {
	var betas []string
	add := func(list string) {
		for _, b := range strings.Split(list, ",") {
			if b = strings.TrimSpace(b); b != "" && !slices.Contains(betas, b) {
				betas = append(betas, b)
			}
		}
	}
	add(res.Headers["anthropic-beta"])
	add(betaHeaderFromProviderOptions(req.ProviderOptions))
	return strings.Join(betas, ",")
}
```

`llm/providers/anthropic/protocol_request.go`:

```go
package anthropic

import (
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// buildProtocolBody builds the Messages body from the shaped request and
// the row's caps (spec §8.2, §8.4): ThinkingShape picks one of the three
// thinking bodies, ThinkingDisplay and ThinkingAlwaysOn refine it, and no
// model-id branch remains.
func buildProtocolBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	caps := res.Caps
	system, messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	system, err = applyAnthropicResponseFormat(system, req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	maxTokens := fallbackMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	ttl := registry.StringValue(caps.CacheTTL)
	body := map[string]any{
		"max_tokens":    maxTokens,
		"messages":      messages,
		"cache_control": cacheMarker(ttl),
	}
	if protocolhttp.ModelInBody(res) {
		body["model"] = res.WireID
	}
	if strings.TrimSpace(system) != "" {
		body["system"] = []map[string]any{{"type": "text", "text": system, "cache_control": cacheMarker(ttl)}}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop_sequences"] = req.StopSequences
	}
	if tier := strings.TrimSpace(req.ServiceTier); tier != "" {
		body["service_tier"] = tier
	}
	if uid := strings.TrimSpace(req.Metadata["user_id"]); uid != "" {
		body["metadata"] = map[string]any{"user_id": uid}
	}
	webSearch := req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch)
	if err := applyAnthropicTools(body, req, webSearch); err != nil {
		return nil, err
	}
	if len(req.Tools) > 0 || webSearch {
		if tools, ok := body["tools"].([]map[string]any); ok && len(tools) > 0 && ttl != "" {
			tools[len(tools)-1]["cache_control"] = cacheMarker(ttl)
		}
	}
	thinkingBudget := applyThinkingShape(body, req, caps)
	if ov, ok := req.ProviderOptions[registry.ProtocolAnthropic].(map[string]any); ok {
		for k, v := range ov {
			if k == "beta_headers" {
				continue
			}
			body[k] = v
		}
	}
	maxCap := 0
	if caps.MaxOutputTokens != nil {
		maxCap = *caps.MaxOutputTokens
	}
	reconcileThinkingContract(body, maxTokens, thinkingBudget, maxCap)
	return body, nil
}

// applyThinkingShape writes the thinking body the row's ThinkingShape
// selects and returns the budget it committed to (0 when none):
// adaptive → {type: adaptive} plus display, sent when ThinkingAlwaysOn or
// an effort is set, plus output_config.effort only for a caller effort;
// budget → {type: enabled, budget_tokens} only for an effort;
// budget+effort → both. none clears the effort; an unset shape sends
// nothing.
func applyThinkingShape(body map[string]any, req llm.Request, caps registry.Caps) int {
	effort := ""
	if req.ReasoningEffort != nil && *req.ReasoningEffort != "none" {
		effort = *req.ReasoningEffort
	}
	switch registry.StringValue(caps.ThinkingShape) {
	case "adaptive":
		if !registry.BoolValue(caps.ThinkingAlwaysOn) && effort == "" {
			return 0
		}
		thinking := map[string]any{"type": "adaptive"}
		if display := registry.StringValue(caps.ThinkingDisplay); display != "" {
			thinking["display"] = display
		}
		body["thinking"] = thinking
		if effort != "" {
			body["output_config"] = map[string]any{"effort": effort}
		}
	case "budget", "budget+effort":
		if effort == "" {
			return 0
		}
		budget := llm.ReasoningBudget(effort)
		if budget > 0 {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
		if registry.StringValue(caps.ThinkingShape) == "budget+effort" {
			body["output_config"] = map[string]any{"effort": effort}
		}
		return budget
	}
	return 0
}
```

`llm/providers/anthropic/protocol_transport.go`:

```go
package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, method, u string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	headers := map[string]string{"anthropic-version": anthropicVersion}
	if beta := betaHeader(res, req); beta != "" {
		headers["anthropic-beta"] = beta
	}
	return &protocolhttp.Call{Operation: operation, EndpointFamily: "anthropic_messages", Method: method, URL: u, Body: body, Headers: headers, Req: req, Res: res, Client: p.Client}
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := buildProtocolBody(req, res)
	if err != nil {
		return llm.Response{}, err
	}
	var out llm.Response
	err = protocolhttp.Do(ctx, p.call("messages.create", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		if r.Raw == nil {
			return nil, fmt.Errorf("messages.create: response is not a JSON object")
		}
		out = fromAnthropicResponse(r.Raw, req.Model)
		llm.StampEndpointURL(&out, r.EndpointURL, r.Material)
		out.RateLimit = llm.ParseRateLimitHeaders(r.Header)
		return &out, nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	return out, nil
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := buildProtocolBody(req, res)
	if err != nil {
		return nil, err
	}
	body["stream"] = true
	call := p.call("messages.create(stream)", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		decodeMessagesStream(sctx, cancel, resp, s, req, res.Instance, r.EndpointURL, r.Material, attempt)
	})
}

// ListModels implements llm.Protocol: GET the models endpoint page by page
// (limit=1000, after_id) and return the ids verbatim; the curated [1m]
// alias rows replace the synthesized variants of the old adapter.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	var rows []registry.Model
	afterID := ""
	for {
		u, err := withQuery(protocolhttp.URL(res, res.Transport.ModelsEndpoint), "limit", "1000", "after_id", afterID)
		if err != nil {
			return nil, err
		}
		call := p.call("models.list", http.MethodGet, u, nil, llm.Request{Model: "*"}, res)
		call.EndpointFamily = "anthropic_models"
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
			return nil, json.Unmarshal(r.Body, &page)
		}); err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			if m.ID != "" {
				rows = append(rows, registry.Model{ID: m.ID})
			}
		}
		if !page.HasMore || page.LastID == "" || page.LastID == afterID {
			break
		}
		afterID = page.LastID
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

// CountTokens implements llm.Protocol: the completion body minus the
// output-side fields, POSTed to count_tokens.
func (p *Protocol) CountTokens(ctx context.Context, req llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	body, err := buildProtocolBody(req, res)
	if err != nil {
		return 0, err
	}
	for _, k := range []string{"max_tokens", "temperature", "top_p", "stop_sequences", "service_tier", "cache_control"} {
		delete(body, k)
	}
	var count int
	err = protocolhttp.Do(ctx, p.call("messages.count_tokens", http.MethodPost, protocolhttp.URL(res, res.Transport.CountTokensEndpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		n := intFromAny(r.Raw["input_tokens"])
		if n <= 0 && r.Raw["input_tokens"] == nil {
			return nil, fmt.Errorf("messages.count_tokens: missing input_tokens in %q", r.Body)
		}
		count = n
		return nil, nil
	})
	return count, err
}

// withQuery adds key/value pairs (empty values skipped) to a URL that may
// already carry a query string.
func withQuery(raw string, pairs ...string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			q.Set(pairs[i], pairs[i+1])
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
```

(add `"encoding/json"` to the imports; `intFromAny` is `adapter.go:305`.)

- [ ] **Step 6: Run the tests and add the fuzz target**

Run: `go test ./llm/providers/anthropic/ -count=1 -race`. Then create `protocol_fuzz_test.go` with `FuzzAnthropicProtocolBuildBody` modeled on `requestbuild_fuzz_test.go` (`FuzzAnthropicRequestBuild`), fuzzing `ThinkingShape` (index into unset/adaptive/budget/budget+effort), `ThinkingDisplay`, `ThinkingAlwaysOn`, `WebSearch`, `CacheTTL`, and `MaxOutputTokens` alongside the request bytes; property: no panic, marshals, and when `thinking` is present with `budget_tokens`, `max_tokens > budget_tokens`. Add `native:llm:./providers/anthropic:FuzzAnthropicProtocolBuildBody` to `scripts/fuzz/fuzz-targets.txt`. Run: `go test ./llm/providers/anthropic/ -run Fuzz -fuzz FuzzAnthropicProtocolBuildBody -fuzztime 10s && make fuzz-registry-check`.
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add llm/providers/anthropic scripts/fuzz/fuzz-targets.txt
git commit -m "feat(anthropic): Resolved-driven Protocol beside the adapter"
```

---

### Task 11: `google.Protocol`

**Files:**
- Modify: `llm/providers/google/request.go:14-106` (`buildRequestBody` becomes a thin wrapper over the package function `generateContentBody`), `:254` (`toGeminiContents` takes the multimodal cap), `llm/providers/google/adapter.go:438-640` (`decodeStream` → package function `decodeGenerateContentStream`)
- Create: `llm/providers/google/protocol.go`, `protocol_transport.go`
- Test: `llm/providers/google/protocol_test.go`, `protocol_transport_test.go`, `protocol_fuzz_test.go`
- Modify: `scripts/fuzz/fuzz-targets.txt`

**Interfaces:**
- Consumes: `toGeminiContents`, `toGeminiFunctionDecls`, `sanitizeGeminiSchema`, `fromGeminiResponse`, `classifyGeminiError`, `tokenCountInt`, `supportsGenerateContent`, `protocolhttp.*` (including `Call.Reclassify`).
- Produces: `google.Protocol{Client}` (complete `llm.Protocol`); package functions `generateContentBody(req llm.Request, system string, contents []map[string]any, webSearch bool, options map[string]any) (map[string]any, error)`, `toGeminiContents(model string, msgs []llm.Message, multimodalToolResults bool)`, `decodeGenerateContentStream(sctx, cancel, resp, s, req, provider, endpointURL string, material, attempt)`.

What changes (spec §8.3, §9.4): auth is the `x-goog-api-key` header through the `header` scheme (the converter's default for `@ai-sdk/google`), never the `?key=` query parameter; `{model}` lives in every endpoint template, so the body never carries `model`; `MultimodalToolResults` replaces the `gemini-3` prefix check; `WebSearch = false` drops `google_search`; `ProviderOptions["google"]` only; the gRPC-status remap (`classifyGeminiError`) runs through the runner's `Reclassify` hook.

- [ ] **Step 1: Write the failing tests**

`llm/providers/google/protocol_test.go`:

```go
package google

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func protoRes(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolGoogle), Reasoning: new(true), ReasoningControls: []string{"effort"}}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "google", Protocol: registry.ProtocolGoogle, ModelID: "gemini-x", WireID: "gemini-x", Transport: registry.Transport{Endpoint: "/models/{model}:generateContent", StreamEndpoint: "/models/{model}:streamGenerateContent?alt=sse"}, Caps: caps}
}

func protoReq(effort string) llm.Request {
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
	}}
	if effort != "" {
		req.ReasoningEffort = &effort
	}
	return req
}

func protoBuild(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestProtocolPrunablePathsMatchRegistry(t *testing.T) {
	p, ok := llm.ProtocolFor(registry.ProtocolGoogle)
	if !ok {
		t.Fatal("google not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolGoogle); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}

func TestProtocolBuildBody(t *testing.T) {
	req := protoReq("high")
	req.WebSearch = true
	req.Temperature = new(0.2)
	req.ProviderOptions = map[string]any{"google": map[string]any{"safetySettings": []any{map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}}}}
	body := protoBuild(t, req, protoRes(nil))
	if _, has := body["model"]; has {
		t.Fatal("the model rides in the URL, never the body")
	}
	gen := body["generationConfig"].(map[string]any)
	if gen["temperature"] != 0.2 || gen["thinkingConfig"].(map[string]any)["thinkingBudget"] != llm.ReasoningBudget("high") {
		t.Fatalf("generationConfig = %v", gen)
	}
	if tools := body["tools"].([]map[string]any); len(tools) != 1 || tools[0]["google_search"] == nil {
		t.Fatalf("google_search expected without function tools: %v", body["tools"])
	}
	if body["safetySettings"] == nil || body["systemInstruction"] == nil {
		t.Fatalf("provider options and system instruction: %v", body)
	}

	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
	if tools := protoBuild(t, req, protoRes(nil))["tools"].([]map[string]any); len(tools) != 1 || tools[0]["functionDeclarations"] == nil {
		t.Fatalf("google_search never rides with function declarations: %v", tools)
	}
	req.Tools = nil
	if _, has := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.WebSearch = new(false) }))["tools"]; has {
		t.Fatal("WebSearch=false drops google_search")
	}

	none := protoReq("none")
	if _, has := protoBuild(t, none, protoRes(nil))["generationConfig"].(map[string]any)["thinkingConfig"]; has {
		t.Fatal("none sends no thinkingConfig")
	}
	off := protoBuild(t, protoReq("high"), protoRes(func(c *registry.Caps) { c.Reasoning = new(false) }))
	if _, has := off["generationConfig"].(map[string]any)["thinkingConfig"]; has {
		t.Fatal("Reasoning=false sends no thinkingConfig")
	}
}

func TestProtocolBuildBody_MultimodalToolResultsCap(t *testing.T) {
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shot", Arguments: []byte(`{}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "shot", Content: "ok", ImageData: []byte{1, 2, 3}, ImageMediaType: "image/png"}}}},
	}}
	if _, err := (&Protocol{}).BuildBody(req, protoRes(nil)); err == nil {
		t.Fatal("tool-result images need MultimodalToolResults")
	}
	body := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.MultimodalToolResults = new(true) }))
	parts := body["contents"].([]map[string]any)[1]["parts"].([]map[string]any)
	fr := parts[0]["functionResponse"].(map[string]any)
	if fr["parts"] == nil {
		t.Fatalf("inlineData must nest inside functionResponse: %v", fr)
	}
}
```

`llm/providers/google/protocol_transport_test.go`:

```go
package google

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const generateJSON = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`

const generateSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]}}]}\n\n" +
	"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7}}\n\n"

type protoCapture struct {
	path   string
	header http.Header
	body   map[string]any
}

func protoServer(t *testing.T, status int, body string) (*httptest.Server, *protoCapture) {
	t.Helper()
	got := &protoCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.header = r.URL.RequestURI(), r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		if strings.HasPrefix(body, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func protoLive(srv *httptest.Server) registry.Resolved {
	res := protoRes(nil)
	res.WireID = "gemini-2.5-flash"
	res.Transport = registry.Transport{Auth: registry.AuthHeader, AuthHeader: "x-goog-api-key", BaseURL: srv.URL + "/v1beta", Endpoint: "/models/{model}:generateContent", StreamEndpoint: "/models/{model}:streamGenerateContent?alt=sse", ModelsEndpoint: "/models", CountTokensEndpoint: "/models/{model}:countTokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	return res
}

func TestProtocolCompleteUsesHeaderAuthAndModelInPath(t *testing.T) {
	srv, got := protoServer(t, 200, generateJSON)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "google" || resp.Text() != "hello" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	if got.path != "/v1beta/models/gemini-2.5-flash:generateContent" || got.header.Get("x-goog-api-key") != "k-1" || strings.Contains(got.path, "key=") {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}
}

func TestProtocolStreamUsageOnFinishChunk(t *testing.T) {
	srv, got := protoServer(t, 200, generateSSE)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), protoReq(""), protoLive(srv))
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil || final.Provider != "google" || final.Text() != "hello" || final.Usage.TotalTokens != 7 {
		t.Fatalf("final = %+v", final)
	}
	if got.path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse" {
		t.Fatalf("path = %s", got.path)
	}
}

func TestProtocolReclassifiesGRPCStatus(t *testing.T) {
	srv, _ := protoServer(t, 400, `{"error":{"code":400,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	if llm.Kind(err) != llm.KindRateLimit || err.(llm.Error).Provider() != "google" {
		t.Fatalf("RESOURCE_EXHAUSTED on 400 must reclassify to a rate limit stamped with the instance: %v", err)
	}
}

func TestProtocolListModelsAndCountTokens(t *testing.T) {
	srv, got := protoServer(t, 200, `{"models":[{"name":"models/gemini-2.5-flash","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent"]},{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]}]}`)
	res := protoLive(srv)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if err != nil || len(rows) != 1 || rows[0].ID != "gemini-2.5-flash" || *rows[0].Caps.ContextWindow != 1048576 || *rows[0].Caps.MaxOutputTokens != 65536 {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	if got.path != "/v1beta/models?pageSize=1000" || got.header.Get("x-goog-api-key") != "k-1" {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}
	srv2, got2 := protoServer(t, 200, `{"totalTokens":13}`)
	n, err := (&Protocol{Client: srv2.Client()}).CountTokens(context.Background(), protoReq(""), protoLive(srv2))
	if err != nil || n != 13 || got2.path != "/v1beta/models/gemini-2.5-flash:countTokens" {
		t.Fatalf("count = %d err = %v path = %s", n, err, got2.path)
	}
	if gcr := got2.body["generateContentRequest"].(map[string]any); gcr["model"] != "models/gemini-2.5-flash" || gcr["contents"] == nil {
		t.Fatalf("count body = %v", got2.body)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/google/ -run 'TestProtocol' -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Refactor the shared builder pieces**

In `llm/providers/google/request.go`:

1. Turn `buildRequestBody` into the package function

```go
// generateContentBody assembles generationConfig, systemInstruction,
// tools (google_search only when webSearch is on and there are no
// function declarations), toolConfig, and the caller's options.
func generateContentBody(req llm.Request, system string, contents []map[string]any, webSearch bool, options map[string]any) (map[string]any, error)
```

   with the body of today's function, `req.WebSearch` → `webSearch`, and the two `ProviderOptions` copies replaced by `maps.Copy(body, options)`. The method becomes:

```go
func (a *Adapter) buildRequestBody(req llm.Request, system string, contents []map[string]any) (map[string]any, error) {
	options := map[string]any{}
	for _, key := range []string{"google", "gemini"} {
		if ov, ok := req.ProviderOptions[key].(map[string]any); ok {
			maps.Copy(options, ov)
		}
	}
	return generateContentBody(req, system, contents, req.WebSearch, options)
}
```

2. `toGeminiContents(model string, msgs []llm.Message)` → `toGeminiContents(model string, msgs []llm.Message, multimodalToolResults bool)`; inside, `if !geminiSupportsMultimodalFunctionResponse(model)` → `if !multimodalToolResults` (the error text keeps naming `model`). Update the old adapter's three call sites to pass `geminiSupportsMultimodalFunctionResponse(req.Model)`.

In `llm/providers/google/adapter.go` rename the body of `decodeStream` (438-640) into

```go
func decodeGenerateContentStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, provider, endpointURL string, material llm.APILogCredentialMaterial, attempt *transport.APIAttemptCapture)
```

with `a.apiLogCredentialMaterial(nil)` → `material`, the `endpoint` parameter → `endpointURL`, every `Provider: "google"` → `provider`, the `"google"` in `llm.NewStreamError`/`llm.WrapContextError` → `provider`, in-band errors (`inbandStreamError`) passed through `llm.RewriteErrorProvider(err, provider)`, and `llm.NormalizeFinishReason("google", …)` unchanged. The method becomes a one-line call passing `a.Name()`, `endpoint`, `a.apiLogCredentialMaterial(nil)`.

Run `go test ./llm/providers/google/ -count=1`: the old tests still PASS.

- [ ] **Step 4: Write the protocol**

`llm/providers/google/protocol.go`:

```go
package google

import (
	"net/http"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the registry-driven Gemini API implementation (spec §8),
// registered beside the pre-registry Adapter until step 3 deletes it. The
// model always rides in the endpoint path, and the credential in the
// header the transport names (x-goog-api-key, or a Vertex bearer token).
type Protocol struct {
	Client *http.Client
}

func init() { llm.RegisterProtocol(&Protocol{}) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolGoogle }

var prunablePaths = []string{"cachedContent", "generationConfig.stopSequences", "generationConfig.temperature", "generationConfig.topP", "labels", "safetySettings", "toolConfig"}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	caps := res.Caps
	system, contents, err := toGeminiContents(res.WireID, req.Messages, registry.BoolValue(caps.MultimodalToolResults))
	if err != nil {
		return nil, err
	}
	if caps.Reasoning != nil && !*caps.Reasoning {
		req.ReasoningEffort = nil
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort == "none" {
		req.ReasoningEffort = nil
	}
	options, _ := req.ProviderOptions[registry.ProtocolGoogle].(map[string]any)
	webSearch := req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch)
	return generateContentBody(req, system, contents, webSearch, options)
}
```

`llm/providers/google/protocol_transport.go`:

```go
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, family, method, u string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	return &protocolhttp.Call{Operation: operation, EndpointFamily: family, Method: method, URL: u, Body: body, Req: req, Res: res, Client: p.Client, Reclassify: reclassifyGemini(res.Instance)}
}

// reclassifyGemini applies the gRPC status remap of classifyGeminiError to
// the runner's classified error (RESOURCE_EXHAUSTED on a 400 is a rate
// limit, DEADLINE_EXCEEDED a timeout, UNAVAILABLE/INTERNAL a server error)
// and keeps the instance name on the remapped error.
func reclassifyGemini(instance string) func(status int, body []byte, err error) error {
	return func(status int, body []byte, err error) error {
		var retryAfter *time.Duration
		if le, ok := err.(llm.Error); ok {
			retryAfter = le.RetryAfter()
		}
		return llm.RewriteErrorProvider(classifyGeminiError(status, body, retryAfter, err), instance)
	}
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := p.BuildBody(req, res)
	if err != nil {
		return llm.Response{}, err
	}
	var out llm.Response
	err = protocolhttp.Do(ctx, p.call("generateContent", "google_generate_content", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		if r.Raw == nil {
			return nil, fmt.Errorf("generateContent: response is not a JSON object")
		}
		out = fromGeminiResponse(r.Raw, req.Model)
		llm.StampEndpointURL(&out, r.EndpointURL, r.Material)
		out.RateLimit = llm.ParseRateLimitHeaders(r.Header)
		return &out, nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	return out, nil
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := p.BuildBody(req, res)
	if err != nil {
		return nil, err
	}
	call := p.call("streamGenerateContent", "google_generate_content", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		decodeGenerateContentStream(sctx, cancel, resp, s, req, res.Instance, r.EndpointURL, r.Material, attempt)
	})
}

// ListModels implements llm.Protocol: one page of up to 1000 models,
// keeping those that support generateContent.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	u := protocolhttp.URL(res, res.Transport.ModelsEndpoint)
	if strings.Contains(u, "?") {
		u += "&pageSize=1000"
	} else {
		u += "?pageSize=1000"
	}
	var rows []registry.Model
	err := protocolhttp.Do(ctx, p.call("models.list", "google_models", http.MethodGet, u, nil, llm.Request{Model: "*"}, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		var payload struct {
			Models []struct {
				Name                       string   `json:"name"`
				InputTokenLimit            int      `json:"inputTokenLimit"`
				OutputTokenLimit           int      `json:"outputTokenLimit"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		for _, m := range payload.Models {
			if !supportsGenerateContent(m.SupportedGenerationMethods) {
				continue
			}
			row := registry.Model{ID: strings.TrimPrefix(m.Name, "models/")}
			if m.InputTokenLimit > 0 {
				row.Caps.ContextWindow = new(m.InputTokenLimit)
			}
			if m.OutputTokenLimit > 0 {
				row.Caps.MaxOutputTokens = new(m.OutputTokenLimit)
			}
			rows = append(rows, row)
		}
		return nil, nil
	})
	return rows, err
}

// CountTokens implements llm.Protocol: the completion body wrapped as a
// generateContentRequest with the model name, as the countTokens method
// expects.
func (p *Protocol) CountTokens(ctx context.Context, req llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	inner, err := p.BuildBody(req, res)
	if err != nil {
		return 0, err
	}
	inner["model"] = "models/" + res.WireID
	body := map[string]any{"generateContentRequest": inner}
	var count int
	err = protocolhttp.Do(ctx, p.call("countTokens", "google_count_tokens", http.MethodPost, protocolhttp.URL(res, res.Transport.CountTokensEndpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		count = tokenCountInt(r.Raw["totalTokens"])
		if count <= 0 && r.Raw["totalTokens"] == nil {
			return nil, fmt.Errorf("countTokens: missing totalTokens in %q", r.Body)
		}
		return nil, nil
	})
	return count, err
}
```

(The prune runs on the outer `body` for CountTokens, so the inner request keeps its `generationConfig`; that matches today's behavior, where nothing is pruned. If `Prune` must reach the inner request, prune `inner` with `registry.Prune(inner, res.Caps)` before wrapping — do that, and note it in the report.)

- [ ] **Step 5: Run the tests and add the fuzz target**

Run: `go test ./llm/providers/google/ -count=1 -race`. Then create `protocol_fuzz_test.go` with `FuzzGoogleProtocolBuildBody` modeled on `requestbuild_fuzz_test.go` (`FuzzGoogleRequestBuild`), fuzzing `MultimodalToolResults`, `WebSearch`, and `Reasoning` alongside the request bytes; property: no panic, marshals, never a `model` key, never `google_search` beside `functionDeclarations`. Add `native:llm:./providers/google:FuzzGoogleProtocolBuildBody` to `scripts/fuzz/fuzz-targets.txt`. Run: `go test ./llm/providers/google/ -run Fuzz -fuzz FuzzGoogleProtocolBuildBody -fuzztime 10s && make fuzz-registry-check`.
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add llm/providers/google scripts/fuzz/fuzz-targets.txt
git commit -m "feat(google): Resolved-driven Protocol beside the adapter"
```

---

### Task 12: Wire captures from `Resolved` inputs

**Files:**
- Create: `llm/providers/wirecapture/doc.go`, `llm/providers/wirecapture/wirecapture_test.go`, `llm/providers/wirecapture/testdata/golden/*.json` (one per case, generated with `-update`)

**Interfaces:**
- Consumes: `registry.Load` with `WithConfigPath`, `WithEnv`, `WithStateRoot`, `WithOffline(true)`, `WithoutCache()`; `(*Registry).Resolve`, `ApplyLive`; `llm.ShapeRequest`; the four protocol types with an injected `Client`; `tokenauth.DefaultCodex.{StateDir,Credentials}`, `tokenauth.DefaultGCPADC.FindCredentials`; `authopenai.SaveAuth`; `llm.WithAPIAttemptSink`/`WithAPIAttemptGroup`/`NewAPIAttemptGroup`/`WaitForPriorAPIAttempts` (for `pruned_fields`).
- Produces: the golden corpus spec §13 "Wire captures" asks for. Nothing imports this package.

Every case resolves a reference against the plan-1 golden configuration, shapes a fixed request, runs the real `Complete` or `Stream` through a recording `http.RoundTripper` that answers with a canned success body, and snapshots method, URL, headers (credential values replaced by `<credential>`), the canonical JSON body, and the pruned fields. The registry, the runner, the authenticators, and the builders are all exercised for real; only the network is replaced.

- [ ] **Step 1: Write the harness and the cases**

`llm/providers/wirecapture/doc.go`:

```go
// Package wirecapture holds the golden wire requests every protocol and
// transport produces from Resolved inputs (spec §13 "Wire captures"). It
// contains no production code; this file keeps the directory a package.
package wirecapture
```

`llm/providers/wirecapture/wirecapture_test.go`:

```go
package wirecapture

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	gauth "golang.org/x/oauth2/google"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

var update = flag.Bool("update", false, "rewrite testdata/golden files")

// config is the plan-1 golden configuration (llm/registry/golden_test.go),
// repeated here because that file is test-scoped to package registry.
const config = `
default = "anthropic"
[providers."groq-responses"]
base = "groq"
protocol = "openai-responses"
[providers.work]
base = "openai"
base_url = "https://gw.example.com/v1"
protocol = "openai-chat"
surface = "generic"
headers = { "X-Portkey-Provider" = "openai" }
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.work.fields]
stream_options = false
[providers.work.models."glm-5.2-nvfp4"]
context_window = 1048576
max_output_tokens = 131072
effort_values = ["high", "max"]
thinking_format = "zai"
[providers.local]
base = "openai-compatible"
base_url = "http://localhost:8080/v1"
auth = "none"
[providers.bedrock]
base = "amazon-bedrock"
[providers.bedrock.vars]
"AWS_REGION" = "us-east-1"
[providers.vertex]
base = "google-vertex-anthropic"
[providers.vertex.vars]
"GOOGLE_VERTEX_PROJECT" = "my-project"
"GOOGLE_VERTEX_LOCATION" = "global"
[providers.azure]
[providers.azure.vars]
"AZURE_RESOURCE_NAME" = "contoso-prod"
[providers.azure.models."gpt55-prod"]
alias_of = "gpt-5.5"
[providers.azure.models."claude-prod"]
alias_of = "claude-opus-4-5"
[providers.orclaude]
base = "openrouter"
protocol = "anthropic"
[providers.orclaude.models."minimax/*"]
surface = "anthropic"
[providers.ollama.models."qwen3*"]
context_window = 40960
`

// secrets are the fixture credentials; the test asserts none of them, nor
// the minted tokens, ever reaches a golden file.
var secrets = map[string]string{
	"ANTHROPIC_API_KEY": "SECRET-anthropic", "OPENAI_API_KEY": "SECRET-openai", "GROQ_API_KEY": "SECRET-groq",
	"OPENROUTER_API_KEY": "SECRET-openrouter", "GEMINI_API_KEY": "SECRET-gemini", "AZURE_API_KEY": "SECRET-azure",
	"AWS_BEARER_TOKEN_BEDROCK": "SECRET-bedrock", "PORTKEY_KEY": "SECRET-portkey",
}

var env = map[string]string{
	"OPENAI_ORG_ID": "org-golden", "GOOGLE_VERTEX_PROJECT": "my-project", "GOOGLE_VERTEX_LOCATION": "global", "OLLAMA_HOST": "localhost",
}

const (
	adcToken   = "SECRET-adc-token"
	codexToken = "SECRET-codex-token"
)

type capture struct {
	Case         string            `json:"case"`
	Ref          string            `json:"ref"`
	Stream       bool              `json:"stream"`
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
	PrunedFields []string          `json:"pruned_fields"`
}

// recorder is the fake network: it records the last request and answers
// with the canned success body of the protocol being driven.
type recorder struct {
	mu       sync.Mutex
	last     *http.Request
	lastBody []byte
	respond  func() (body, contentType string)
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	r.mu.Lock()
	r.last, r.lastBody = req, body
	r.mu.Unlock()
	payload, ctype := r.respond()
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": []string{ctype}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
}

var canned = map[string]map[bool]string{
	registry.ProtocolAnthropic: {
		false: `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		true:  "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	},
	registry.ProtocolGoogle: {
		false: `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
		true:  "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n",
	},
	registry.ProtocolOpenAIChat: {
		false: `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		true:  "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n",
	},
	registry.ProtocolOpenAIResponses: {
		false: `{"id":"resp_1","status":"completed","model":"m","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		true:  "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"ok\"}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"m\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	},
}

func text(s string) []llm.ContentPart { return []llm.ContentPart{{Kind: llm.ContentText, Text: s}} }

var weatherTool = llm.ToolDefinition{Name: "weather", Description: "Current weather", Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}

// toolsRequest is the common fixture: a system prompt, one user turn, one
// tool with forced tool choice, the session identifiers the Codex headers
// and the affinity headers read, and a 24h cache retention the rows gate.
func toolsRequest(effort string) llm.Request {
	req := llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: text("You are terse.")},
			{Role: llm.RoleUser, Content: text("What is the weather in Oslo?")},
		},
		Tools:                []llm.ToolDefinition{weatherTool},
		ToolChoice:           &llm.ToolChoice{Mode: "required"},
		SessionID:            "sess-golden",
		ThreadID:             "thread-golden",
		ClientMetadata:       map[string]string{"installation_id": "inst-golden"},
		PromptCacheRetention: "24h",
	}
	if effort != "" {
		req.ReasoningEffort = new(effort)
	}
	return req
}

func webSearchRequest() llm.Request {
	req := toolsRequest("")
	req.Tools, req.ToolChoice, req.WebSearch = nil, nil, true
	return req
}

// signedToolTurn replays a prior assistant turn whose thinking carried an
// OpenRouter reasoning_details signature, so the golden shows the signed
// round trip of spec §8.4.
func signedToolTurn() llm.Request {
	req := toolsRequest("high")
	req.Messages = append(req.Messages[:2],
		llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "Need the weather tool.", Signature: "reasoning_details", EncryptedContent: `[{"type":"reasoning.text","text":"","signature":"sig-golden","format":"anthropic-claude-v1","index":0}]`}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "weather", Arguments: []byte(`{"city":"Oslo"}`)}},
		}},
		llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "12C, rain"}}}},
		llm.Message{Role: llm.RoleUser, Content: text("And tomorrow?")},
	)
	return req
}

type wireCase struct {
	name    string
	ref     string
	stream  bool
	request llm.Request
}

var cases = []wireCase{
	{"anthropic-sonnet-4-5-budget", "anthropic/claude-sonnet-4-5", false, toolsRequest("high")},
	{"anthropic-sonnet-4-5-1m-stream", "anthropic/claude-sonnet-4-5[1m]", true, toolsRequest("high")},
	{"anthropic-opus-4-6-no-effort", "anthropic/claude-opus-4-6", false, toolsRequest("")},
	{"anthropic-opus-4-7-display", "anthropic/claude-opus-4-7", false, toolsRequest("high")},
	{"anthropic-opus-4-5-hybrid", "anthropic/claude-opus-4-5", false, toolsRequest("high")},
	{"openai-gpt-5-5", "openai/gpt-5.5", false, toolsRequest("high")},
	{"openai-codex-gpt-5-6-lite-stream", "openai-codex/gpt-5.6", true, toolsRequest("")},
	{"groq-chat-stream", "groq/openai/gpt-oss-120b", true, toolsRequest("high")},
	{"groq-responses", "groq-responses/openai/gpt-oss-120b", false, toolsRequest("high")},
	{"openrouter-opus-5-signed-tool-turn", "openrouter/anthropic/claude-opus-5", false, signedToolTurn()},
	{"work-glm-zai-stream", "work/glm-5.2-nvfp4", true, toolsRequest("high")},
	{"azure-gpt55-prod", "azure/gpt55-prod", false, toolsRequest("high")},
	{"azure-claude-prod", "azure/claude-prod", false, toolsRequest("high")},
	{"bedrock-sonnet-5-stream", "bedrock/anthropic.claude-sonnet-5", true, webSearchRequest()},
	{"vertex-opus-5", "vertex/claude-opus-5", false, toolsRequest("high")},
	{"vertex-gemini-stream", "google-vertex/gemini-2.5-flash", true, toolsRequest("")},
	{"google-flash-lite-web-search", "google/gemini-2.5-flash-lite", false, webSearchRequest()},
	{"ollama-llama3-optional-bearer", "ollama/llama3:8b", false, toolsRequest("")},
}

type harness struct {
	registry *registry.Registry
	sink     *captureSink
}

type captureSink struct {
	mu   sync.Mutex
	last *apilog.APIAttemptRecord
}

func (s *captureSink) AppendAttempt(_ context.Context, r apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = &r
	return nil
}
func (s *captureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error { return nil }

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if err := os.MkdirAll(filepath.Dir(adc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adc, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	now := time.Now()
	if err := authopenai.SaveAuth(state, "openai-codex", authopenai.AuthRecord{Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth, ObtainedAt: now, TokenType: "Bearer", AccessToken: "stale", RefreshToken: "rt", Expiry: now.Add(time.Hour), AccountID: "acct_golden"}); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := map[string]string{"HOME": home}
	maps.Copy(lookup, env)
	maps.Copy(lookup, secrets)
	r, err := registry.Load(
		registry.WithConfigPath(cfg),
		registry.WithEnv(func(k string) (string, bool) { v, ok := lookup[k]; return v, ok }),
		registry.WithStateRoot(state), registry.WithOffline(true), registry.WithoutCache(),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.ApplyLive("ollama", []registry.Model{{ID: "llama3:8b"}})

	prevDir, prevCreds, prevFind := tokenauth.DefaultCodex.StateDir, tokenauth.DefaultCodex.Credentials, tokenauth.DefaultGCPADC.FindCredentials
	tokenauth.DefaultCodex.StateDir = state
	tokenauth.DefaultCodex.Credentials = func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		return authopenai.RuntimeCredentials{BearerToken: codexToken, Source: authopenai.AuthSourceOAuth}, nil
	}
	tokenauth.DefaultGCPADC.FindCredentials = func(context.Context, ...string) (*gauth.Credentials, error) {
		return &gauth.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: adcToken})}, nil
	}
	t.Cleanup(func() {
		tokenauth.DefaultCodex.StateDir, tokenauth.DefaultCodex.Credentials, tokenauth.DefaultGCPADC.FindCredentials = prevDir, prevCreds, prevFind
	})
	return &harness{registry: r, sink: &captureSink{}}
}

func (h *harness) protocol(id string, client *http.Client) llm.Protocol {
	switch id {
	case registry.ProtocolAnthropic:
		return &anthropic.Protocol{Client: client}
	case registry.ProtocolGoogle:
		return &google.Protocol{Client: client}
	case registry.ProtocolOpenAIChat:
		return &chatcompletions.Protocol{Client: client}
	default:
		return &responses.Protocol{Client: client}
	}
}

func (h *harness) run(t *testing.T, c wireCase) capture {
	t.Helper()
	res, err := h.registry.Resolve(c.ref)
	if err != nil {
		t.Fatalf("%s: resolve %s: %v", c.name, c.ref, err)
	}
	rec := &recorder{respond: func() (string, string) {
		if c.stream {
			return canned[res.Protocol][true], "text/event-stream"
		}
		return canned[res.Protocol][false], "application/json"
	}}
	p := h.protocol(res.Protocol, &http.Client{Transport: rec})
	req := llm.ShapeRequest(c.request, res)
	req.Model = res.ModelID
	ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_"+c.name)), h.sink)
	if c.stream {
		s, err := p.Stream(ctx, req, res)
		if err != nil {
			t.Fatalf("%s: stream: %v", c.name, err)
		}
		for ev := range s.Events() {
			if ev.Type == llm.StreamEventError {
				t.Fatalf("%s: stream event error: %v", c.name, ev.Err)
			}
		}
	} else if _, err := p.Complete(ctx, req, res); err != nil {
		t.Fatalf("%s: complete: %v", c.name, err)
	}
	llm.WaitForPriorAPIAttempts(ctx)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.last == nil {
		t.Fatalf("%s: nothing was sent", c.name)
	}
	headers := map[string]string{}
	for name, values := range rec.last.Header {
		v := strings.Join(values, ", ")
		if isCredential(v) {
			v = "<credential>"
		}
		headers[name] = v
	}
	var body bytes.Buffer
	if len(rec.lastBody) > 0 {
		var decoded any
		if err := json.Unmarshal(rec.lastBody, &decoded); err != nil {
			t.Fatalf("%s: body is not JSON: %v\n%s", c.name, err, rec.lastBody)
		}
		enc := json.NewEncoder(&body)
		enc.SetIndent("", "  ")
		_ = enc.Encode(decoded)
	}
	var pruned []string
	h.sink.mu.Lock()
	if h.sink.last != nil {
		pruned = h.sink.last.Request.PrunedFields
	}
	h.sink.mu.Unlock()
	return capture{Case: c.name, Ref: c.ref, Stream: c.stream, Method: rec.last.Method, URL: rec.last.URL.String(), Headers: headers, Body: bytes.TrimSpace(body.Bytes()), PrunedFields: pruned}
}

func isCredential(v string) bool {
	for _, s := range secrets {
		if strings.Contains(v, s) {
			return true
		}
	}
	return strings.Contains(v, adcToken) || strings.Contains(v, codexToken)
}

func TestWireCaptures(t *testing.T) {
	h := newHarness(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.run(t, c)
			raw, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, '\n')
			if bytes.Contains(raw, []byte("SECRET-")) {
				t.Fatalf("credential leaked into the capture: %s", raw)
			}
			path := filepath.Join("testdata", "golden", c.name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden %s (run with -update): %v", path, err)
			}
			if !bytes.Equal(want, raw) {
				t.Fatalf("golden mismatch for %s (run with -update after an intended change)\n--- want\n%s\n--- got\n%s", path, want, raw)
			}
		})
	}
}

// TestWireCaptureAssertions pins the transport facts spec §13 names, so a
// golden regeneration cannot silently lose them.
func TestWireCaptureAssertions(t *testing.T) {
	h := newHarness(t)
	byName := map[string]capture{}
	for _, c := range cases {
		byName[c.name] = h.run(t, c)
	}
	has := func(name, key string) bool { _, ok := bodyOf(t, byName[name])[key]; return ok }
	check := func(cond bool, format string, args ...any) {
		t.Helper()
		if !cond {
			t.Errorf(format, args...)
		}
	}
	codex := byName["openai-codex-gpt-5-6-lite-stream"]
	check(strings.HasPrefix(codex.URL, "https://chatgpt.com/backend-api/codex/responses"), "codex url = %s", codex.URL)
	for _, hdr := range []string{"Authorization", "Chatgpt-Account-Id", "Originator", "User-Agent", "X-Openai-Internal-Codex-Responses-Lite", "Session-Id", "Thread-Id", "X-Client-Request-Id"} {
		check(codex.Headers[hdr] != "", "codex header %s missing: %v", hdr, codex.Headers)
	}
	check(codex.Headers["Openai-Organization"] == "" && codex.Headers["Openai-Project"] == "", "codex must not send org/project headers: %v", codex.Headers)
	codexBody := bodyOf(t, codex)
	check(codexBody["parallel_tool_calls"] == false && codexBody["instructions"] == "" && codexBody["tools"] == nil, "codex lite body: %s", codex.Body)
	check(codexBody["client_metadata"] != nil && codexBody["metadata"] == nil, "codex client_metadata rule: %s", codex.Body)
	check(codexBody["text"].(map[string]any)["verbosity"] == "low" && codexBody["reasoning"].(map[string]any)["context"] == "all_turns", "codex body constants: %s", codex.Body)
	check(codexBody["model"] == "gpt-5.6-sol", "codex wire id: %v", codexBody["model"])

	openai := byName["openai-gpt-5-5"]
	check(openai.Headers["Openai-Organization"] == "org-golden" && openai.Headers["Authorization"] == "<credential>", "openai headers: %v", openai.Headers)
	check(has("openai-gpt-5-5", "store") && has("openai-gpt-5-5", "prompt_cache_key"), "openai control fields: %s", openai.Body)

	groq := bodyOf(t, byName["groq-responses"])
	for _, k := range []string{"store", "include", "truncation", "safety_identifier", "prompt_cache_key", "previous_response_id", "metadata"} {
		check(groq[k] == nil, "groq responses must not send %s: %s", k, byName["groq-responses"].Body)
	}
	check(groq["tools"].([]any)[0].(map[string]any)["strict"] == nil, "groq tools carry no strict: %s", byName["groq-responses"].Body)
	groqChat := bodyOf(t, byName["groq-chat-stream"])
	check(groqChat["max_tokens"] != nil && groqChat["max_completion_tokens"] == nil && groqChat["stream_options"] != nil, "groq chat spelling/stream_options: %s", byName["groq-chat-stream"].Body)

	sonnet1m := byName["anthropic-sonnet-4-5-1m-stream"]
	check(strings.Contains(sonnet1m.Headers["Anthropic-Beta"], "context-1m-2025-08-07") && sonnet1m.Headers["X-Api-Key"] == "<credential>" && sonnet1m.Headers["Anthropic-Version"] != "", "sonnet [1m] headers: %v", sonnet1m.Headers)
	check(bodyOf(t, sonnet1m)["model"] == "claude-sonnet-4-5", "sonnet [1m] wire id: %v", bodyOf(t, sonnet1m)["model"])
	opus46 := bodyOf(t, byName["anthropic-opus-4-6-no-effort"])
	check(opus46["thinking"].(map[string]any)["type"] == "adaptive" && opus46["thinking"].(map[string]any)["display"] == nil && opus46["output_config"] == nil, "opus 4.6 adaptive without display: %s", byName["anthropic-opus-4-6-no-effort"].Body)
	opus47 := bodyOf(t, byName["anthropic-opus-4-7-display"])
	check(opus47["thinking"].(map[string]any)["display"] == "summarized" && opus47["output_config"] != nil, "opus 4.7 display+effort: %s", byName["anthropic-opus-4-7-display"].Body)
	opus45 := bodyOf(t, byName["anthropic-opus-4-5-hybrid"])
	check(opus45["thinking"].(map[string]any)["budget_tokens"] != nil && opus45["output_config"] != nil, "opus 4.5 hybrid: %s", byName["anthropic-opus-4-5-hybrid"].Body)

	azure := byName["azure-gpt55-prod"]
	check(azure.URL == "https://contoso-prod.openai.azure.com/openai/v1/responses" && azure.Headers["Api-Key"] == "<credential>" && bodyOf(t, azure)["model"] == "gpt55-prod", "azure responses: %s %v", azure.URL, azure.Headers)
	azureClaude := byName["azure-claude-prod"]
	check(azureClaude.URL == "https://contoso-prod.services.ai.azure.com/anthropic/v1/messages" && azureClaude.Headers["Api-Key"] == "<credential>", "azure claude: %s %v", azureClaude.URL, azureClaude.Headers)

	bedrock := byName["bedrock-sonnet-5-stream"]
	check(bedrock.URL == "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages" && bedrock.Headers["X-Api-Key"] == "<credential>", "bedrock: %s %v", bedrock.URL, bedrock.Headers)
	check(!has("bedrock-sonnet-5-stream", "tools"), "bedrock WebSearch=false drops the tool: %s", bedrock.Body)

	vertex := byName["vertex-opus-5"]
	check(vertex.URL == "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/anthropic/models/claude-opus-5:rawPredict", "vertex url = %s", vertex.URL)
	check(vertex.Headers["Authorization"] == "<credential>" && bodyOf(t, vertex)["anthropic_version"] == "vertex-2023-10-16" && bodyOf(t, vertex)["model"] == nil, "vertex auth/body: %v %s", vertex.Headers, vertex.Body)
	vertexGemini := byName["vertex-gemini-stream"]
	check(strings.HasPrefix(vertexGemini.URL, "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/google/models/gemini-2.5-flash:streamGenerateContent") && vertexGemini.Headers["Authorization"] == "<credential>", "vertex gemini: %s %v", vertexGemini.URL, vertexGemini.Headers)

	gemini := byName["google-flash-lite-web-search"]
	check(gemini.URL == "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-lite:generateContent" && gemini.Headers["X-Goog-Api-Key"] == "<credential>", "gemini: %s %v", gemini.URL, gemini.Headers)
	check(bodyOf(t, gemini)["tools"].([]any)[0].(map[string]any)["google_search"] != nil, "gemini google_search: %s", gemini.Body)

	or := bodyOf(t, byName["openrouter-opus-5-signed-tool-turn"])
	msgs := or["messages"].([]any)
	assistant := msgs[2].(map[string]any)
	details := assistant["reasoning_details"].([]any)
	check(len(details) == 1 && details[0].(map[string]any)["signature"] == "sig-golden" && details[0].(map[string]any)["text"] == "Need the weather tool.", "signed reasoning_details round trip: %v", assistant)
	check(or["reasoning"].(map[string]any)["effort"] == "high" && or["tool_choice"] == "auto", "openrouter reasoning/forcing: %s", byName["openrouter-opus-5-signed-tool-turn"].Body)
	check(byName["openrouter-opus-5-signed-tool-turn"].Headers["X-Session-Affinity"] == "sess-golden", "openrouter affinity headers: %v", byName["openrouter-opus-5-signed-tool-turn"].Headers)

	work := byName["work-glm-zai-stream"]
	check(work.Headers["X-Portkey-Provider"] == "openai" && work.Headers["Authorization"] == "<credential>" && bodyOf(t, work)["thinking"] != nil && bodyOf(t, work)["stream_options"] == nil, "work: %v %s", work.Headers, work.Body)
	check(slices.Contains(work.PrunedFields, "stream_options"), "work pruned fields must name stream_options: %v", work.PrunedFields)

	ollama := byName["ollama-llama3-optional-bearer"]
	check(ollama.URL == "http://localhost:11434/v1/chat/completions" && ollama.Headers["Authorization"] == "", "ollama: %s %v", ollama.URL, ollama.Headers)
}

func bodyOf(t *testing.T, c capture) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("%s: %v", c.Case, err)
	}
	return m
}
```

`http.Header` canonicalizes names (`Chatgpt-Account-Id`, `X-Openai-Internal-Codex-Responses-Lite`, `Api-Key`, `Originator`), which is why the assertions spell them that way; if `TestWireCaptures` shows a different canonical form, follow the recorded form.

- [ ] **Step 2: Generate the goldens and run**

Run: `go test ./llm/providers/wirecapture/ -run TestWireCaptures -update -count=1 && go test ./llm/providers/wirecapture/ -count=1`
Expected: 18 golden files written under `testdata/golden/`, then PASS. Read every golden once (`cat llm/providers/wirecapture/testdata/golden/*.json`) and compare each body against spec §8.2 (baselines), §8.4 (reasoning), §9.2–§9.5 (cloud transports and Codex) — any surprise is a bug in an earlier task, not a golden to accept; fix the task's package and regenerate.

- [ ] **Step 3: Commit**

```bash
git add llm/providers/wirecapture
git commit -m "test(providers): golden wire captures from Resolved inputs"
```

---

### Task 13: Adapt the cross-adapter differential

**Files:**
- Modify: `llm/providers/difftest/differential_fuzz_test.go:83-127` (`providers()`), `llm/providers/difftest/streamvsnonstream_differential_fuzz_test.go:76-145` (`jsonProviders()`), `llm/providers/difftest/conformance_golden_test.go` (fixture provider names), `llm/providers/difftest/doc.go` (package doc names the legs), `llm/providers/difftest/testdata/golden/conformance.json` (regenerated)
- Create: `llm/providers/difftest/resolved_test.go`
- Modify: `scripts/fuzz/fuzz-targets.txt` (the two `difftest` rows' `coverpkg`)

**Interfaces:**
- Consumes: `anthropic.Protocol`, `google.Protocol`, `responses.Protocol`, `chatcompletions.Protocol` (`Stream(ctx, req, res)` and `Complete(ctx, req, res)`), `registry.Baseline`.
- Produces: `resolvedFor(protocol, baseURL string) registry.Resolved`.

- [ ] **Step 1: Add the `Resolved` fixture**

`llm/providers/difftest/resolved_test.go`:

```go
package difftest

import "primeradiant.com/evener/llm/registry"

// resolvedFor is the differential's minimal Resolved record: the protocol's
// default endpoints against the leg's httptest server, no auth, the
// baseline Fields table, and no caps beyond that, so every leg decodes
// the same logical response through the same shaping.
func resolvedFor(protocol, baseURL string) registry.Resolved {
	endpoints := map[string][2]string{
		registry.ProtocolAnthropic:       {"/messages", "/messages"},
		registry.ProtocolGoogle:          {"/models/{model}:generateContent", "/models/{model}:streamGenerateContent?alt=sse"},
		registry.ProtocolOpenAIChat:      {"/chat/completions", "/chat/completions"},
		registry.ProtocolOpenAIResponses: {"/responses", "/responses"},
	}[protocol]
	return registry.Resolved{
		Instance: protocol, Protocol: protocol, ModelID: "test-model", WireID: "test-model",
		Transport: registry.Transport{Auth: registry.AuthNone, BaseURL: baseURL, Endpoint: endpoints[0], StreamEndpoint: endpoints[1], ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported},
		Caps:      registry.Caps{Fields: registry.Baseline(protocol)},
	}
}
```

- [ ] **Step 2: Rewire the legs**

In `differential_fuzz_test.go` replace the four adapter literals and the `ps` table (89-127) with:

```go
	anthRes, googRes, respRes, chatRes := resolvedFor(registry.ProtocolAnthropic, anthSrv.srv.URL), resolvedFor(registry.ProtocolGoogle, googSrv.srv.URL), resolvedFor(registry.ProtocolOpenAIResponses, oaiSrv.srv.URL), resolvedFor(registry.ProtocolOpenAIChat, compatSrv.srv.URL)
	anth, goog, resp, chat := &anthropic.Protocol{}, &google.Protocol{}, &responses.Protocol{}, &chatcompletions.Protocol{}

	ps := []provider{
		{name: "anthropic", encode: encodeAnthropic, drive: func(sse []byte) (*llm.Response, error) {
			return driveStream(func(ctx context.Context, req llm.Request) (llm.Stream, error) { return anth.Stream(ctx, req, anthRes) }, sse, anthSrv)
		}},
		{name: "google", encode: encodeGoogle, drive: func(sse []byte) (*llm.Response, error) {
			return driveStream(func(ctx context.Context, req llm.Request) (llm.Stream, error) { return goog.Stream(ctx, req, googRes) }, sse, googSrv)
		}},
		{name: "responses", encode: encodeOpenAIResponses, drive: func(sse []byte) (*llm.Response, error) {
			return driveStream(func(ctx context.Context, req llm.Request) (llm.Stream, error) { return resp.Stream(ctx, req, respRes) }, sse, oaiSrv)
		}},
		{name: "chatcompletions", encode: encodeOpenAICompat, drive: func(sse []byte) (*llm.Response, error) {
			return driveStream(func(ctx context.Context, req llm.Request) (llm.Stream, error) { return chat.Stream(ctx, req, chatRes) }, sse, compatSrv)
		}},
	}
```

(rename the server labels `"openai"` → `"responses"` and `"openaicompat"` → `"chatcompletions"`, and swap the `openai`/`openaicompat` imports for `responses`/`chatcompletions`). Do the same in `jsonProviders()` (`streamvsnonstream_differential_fuzz_test.go:76-145`): the `stream`/`complete` fields become closures over the `Resolved` records. In `conformance_golden_test.go`, rename the fixture `provider` values `"openai"` → `"responses"` and `"openaicompat"` → `"chatcompletions"`, and in `doc.go` name the four legs by their new packages. Update the two rows in `scripts/fuzz/fuzz-targets.txt`:

```
native:llm:./providers/difftest:FuzzCrossProviderDifferential:./providers/anthropic,./providers/google,./providers/responses,./providers/chatcompletions
native:llm:./providers/difftest:FuzzStreamVsNonStreamDifferential:./providers/anthropic,./providers/google,./providers/responses,./providers/chatcompletions
```

- [ ] **Step 3: Regenerate the conformance golden and run**

Run: `make fuzz-goldens && git diff --stat llm/providers/difftest/testdata && go test ./llm/providers/difftest/ -count=1 -race && go test ./llm/providers/difftest/ -run Fuzz -fuzz FuzzCrossProviderDifferential -fuzztime 20s && go test ./llm/providers/difftest/ -run Fuzz -fuzz FuzzStreamVsNonStreamDifferential -fuzztime 20s && make fuzz-registry-check`
Expected: the golden diff touches only the renamed provider labels (every decoded record stays byte-identical — the decoders did not change); all PASS. A changed decode record means a decoder port in Tasks 7–11 diverged; fix it there.

- [ ] **Step 4: Commit**

```bash
git add llm/providers/difftest scripts/fuzz/fuzz-targets.txt
git commit -m "test(difftest): drive the differential through Resolved-driven protocols"
```

---

### Task 14: Registrations and the gate

**Files:**
- Modify: `llm/providers/all/all.go`
- Test: `llm/providers/all/all_test.go`

- [ ] **Step 1: Register the new packages**

Add to `llm/providers/all/all.go`'s import block:

```go
	_ "primeradiant.com/evener/llm/providers/chatcompletions" // register the openai-chat protocol
	_ "primeradiant.com/evener/llm/providers/responses"       // register the openai-responses protocol
	_ "primeradiant.com/evener/llm/providers/tokenauth"       // register the gcp-adc and oauth-openai-codex authenticators
```

(`anthropic` and `google` are already imported and now register their protocols too.) Create `llm/providers/all/all_test.go`:

```go
package all

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestEveryProtocolAndSchemeIsRegistered(t *testing.T) {
	for _, id := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses, registry.ProtocolAnthropic, registry.ProtocolGoogle} {
		if _, ok := llm.ProtocolFor(id); !ok {
			t.Errorf("protocol %s not registered", id)
		}
	}
	for _, scheme := range []string{registry.AuthBearer, registry.AuthOptionalBearer, registry.AuthHeader, registry.AuthNone, registry.AuthGCPADC, registry.AuthOAuthOpenAICodex} {
		if _, ok := llm.AuthenticatorFor(scheme); !ok {
			t.Errorf("scheme %s not registered", scheme)
		}
	}
}
```

- [ ] **Step 2: Run the whole gate**

Run, from the repo root:

```bash
go build ./... && go test ./... -count=1 && PATH="$(go env GOROOT)/bin:$PATH" make lint && make fuzz-registry-check && go test -race -count=1 ./llm/providers/internal/protocolhttp/ ./llm/providers/tokenauth/ ./llm/providers/chatcompletions/ ./llm/providers/responses/ ./llm/providers/anthropic/ ./llm/providers/google/ ./llm/providers/wirecapture/ ./llm/providers/difftest/
```

Expected: everything green. Fix anything red at its source (never with a skip or an exclusion).

- [ ] **Step 3: Commit**

```bash
git add llm/providers/all
git commit -m "feat(providers): register the Resolved-driven protocols and authenticators"
```

---

## Spec coverage

| Spec requirement | Task |
|---|---|
| §8.1 `Protocol`/`Authenticator`/`RequestPreparer` registered once, looked up by id and scheme | 1, 14 |
| §8.1 trivial authenticators; `gcp-adc` via `x/oauth2/google` at first request, cached per instance, direct dependency | 2, 5 |
| §8.1 `ErrModelListingUnsupported`/`ErrInputTokenCountUnsupported` when an endpoint is `-` | 7, 9, 10, 11 |
| §8.2 build → prune → constants → prepare; `pruned_fields` on the attempt log; constants override options | 2, 4 |
| §8.2 `PrunablePaths()` = registry table, per package | 6, 8, 10, 11 |
| §8.2 baselines: `store: false` when enabled, `include` with every reasoning object, `reasoning.summary` at none omitted, `parallel_tool_calls`/`stop` never built, `developer_role` pseudo-path, `max_tokens` spelling | 6, 8 |
| §8.3 every model-prefix branch becomes a cap (`ResponsesLite`, `ImageDetail`, `ReasoningSummary`, `ThinkingShape`/`Display`/`AlwaysOn`, `MultimodalToolResults`, `WireID`, `[1m]` alias rows) | 6, 8, 10, 11 |
| §8.4 chat dialect table, `Reasoning = false` gating, replay via `ReasoningField`/`Signature`/`reasoning_details` | 6 |
| §8.4 Responses reasoning object; anthropic three shapes; google `thinkingConfig`; `none` sends nothing | 8, 10, 11 |
| §9.1 `{model}` in the path → no `model` in the body; `-` sentinel | 4, 8, 10, 11 |
| §9.2–§9.4 Azure `api-key`, Bedrock `x-api-key`, Vertex `gcp-adc` + `anthropic_version` constant + `:rawPredict` (captured) | 12 |
| §9.5 Codex: per-instance record, `Apply` headers on every request incl. `ListModels`, `PrepareRequest` headers, lite header, `client_metadata` rule, `RequiresStreamingComplete`, no org/project headers | 5, 9, 12 |
| §12 order (413 → codes → status → messages → generic), hints, `Protocol()`, message verbatim, `ErrorCode()` kept, `RetryAfter` from headers | 3 |
| §13 Pruner: build → prune → constants → prepare ordering incl. the Codex `client_metadata` rule in both flag states and the empty merge | 4, 5 |
| §13 Wire captures: per-protocol goldens from `Resolved`, cloud-transport cases, Codex header set, signed `reasoning_details` round trip | 12 |
| §13 Continuation: fingerprint stability across builds and Complete/Stream; families from `Resolved` | 9 |
| §13 Error classifier table of bodies | 3 |
| §13 Cross-adapter differential on `Resolved` inputs, `chatcompletions` leg | 13 |
| §14 step 2: old adapters keep running; `golang.org/x/oauth2` direct | every task's gate, 5 |
