# Vision Model Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the image-description vision side-channel configurable through one per-session `vision_model` setting (unset / `off` / `provider/model`), exposed via CLI flag, runtime appwire mutation, hub web UI picker, and TUI `/vision-model` command.

**Architecture:** The setting lives on `agent.SessionConfig` (persisted via `schema.ConfigSnapshot` + `schema.SessionMeta`, inherited by spawned children, restored on resume). The side-channel in `describeImageCall` gates on `off` and routes non-session models through a new `cheapmodel.Caller.CompleteRouted` (refusal learning + session-model fallback). Runtime changes follow the exact `thread/model/set` path: appwire method → daemon handler → `Session.SetVisionModel` → `thread/vision-model/changed` notification → hub/frontend/TUI.

**Tech Stack:** Go (agent, appwire, server, hub, TUI), React/TypeScript (hub frontend, Biome + vitest).

**Spec:** `docs/superpowers/specs/2026-08-27-vision-model-config-design.md`

## Global Constraints

- Setting values: `""` (unset: side-channel on the session's active model), `"off"` (disabled; reserved only as a bare word, case-insensitive; a ref containing `/` parses as `provider/model` first), bare `"model"` (active provider at call time), `"provider/model"` (pinned provider).
- Default behavior with an unset setting must be byte-identical to today.
- Tests are deterministic: scripted `llm.ProviderAdapter` at the LLM boundary; no live provider calls in the default suite; no prompt-prose assertions — assert route selection, request shape, event kinds, and structured fields.
- Gates per AGENTS.md: `make lint`, `make vet`, `make test`; frontend: `npx biome check --write` on touched files, then `make test-web` and `make test-web-browser`.
- Wire naming: method `thread/vision-model/set`, notification `thread/vision-model/changed`, capability `changeVisionModel`, thread payload field `visionModel` on `appwire.EvenerThread`.
- Commit after every task with the shown message; stage only the named files.

---

### Task 1: `cheapmodel.Caller.CompleteRouted`

**Files:**
- Modify: `agent/internal/cheapmodel/caller.go` (`Complete`, lines 52-60)
- Test: `agent/internal/cheapmodel/caller_test.go`

**Interfaces:**
- Consumes: existing `Caller.run`, `route{provider, model}`.
- Produces: `func (c *Caller) CompleteRouted(ctx context.Context, profile *provider.Profile, providerName, modelID string, req llm.Request) (llm.Response, error)` — resolves and executes an explicit route with the same refusal learning and session-model fallback as `Complete`. Task 3 calls this.

- [ ] **Step 1: Write the failing test**

Add to `agent/internal/cheapmodel/caller_test.go` (uses the file's existing `servesOnly`, `clientWith`, and `refusal` helpers):

```go
func TestCompleteRoutedUsesTheExplicitRoute(t *testing.T) {
	adapter := servesOnly("openai", "main", refusal(400, "The provided model identifier is invalid."))
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.NewOpenAIProfile("main")

	resp, err := caller.CompleteRouted(context.Background(), profile, "openai", "gpt-4.1-nano", llm.Request{
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
		t.Fatalf("CompleteRouted = (%q, %v), want answered via fallback", resp.Text(), err)
	}
	// Refusal of the routed model is learned: the second call goes straight to
	// the session model instead of re-probing.
	if _, err := caller.CompleteRouted(context.Background(), profile, "openai", "gpt-4.1-nano", llm.Request{
		Messages: []llm.Message{llm.User("hi")},
	}); err != nil {
		t.Fatalf("second CompleteRouted: %v", err)
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "main", "main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/cheapmodel/ -run TestCompleteRoutedUsesTheExplicitRoute -v`
Expected: FAIL — `caller.CompleteRouted undefined`.

- [ ] **Step 3: Implement**

In `agent/internal/cheapmodel/caller.go`, replace `Complete` (lines 52-60) with:

```go
// Complete resolves and executes a cheap-model request, falling back once to
// the session model when the provider refuses the resolved model. It resolves
// through the profile's cheap-model ref, which uses the session model when no
// cheap model is configured.
func (c *Caller) Complete(ctx context.Context, profile *provider.Profile, req llm.Request) (llm.Response, error) {
	cheapProvider, cheapModel := profile.CheapModelRef()
	return c.CompleteRouted(ctx, profile, cheapProvider, cheapModel, req)
}

// CompleteRouted is Complete for an explicit route chosen by the caller rather
// than the profile's cheap-model ref — e.g. the vision side-channel's
// configured vision model. It shares Complete's refusal learning and
// session-model fallback; an empty model or a route equal to the session
// route runs on the session model.
func (c *Caller) CompleteRouted(ctx context.Context, profile *provider.Profile, providerName, modelID string, req llm.Request) (llm.Response, error) {
	resp, _, err := c.run(ctx, profile, route{provider: providerName, model: modelID}, req)
	return resp, err
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./agent/internal/cheapmodel/`
Expected: PASS (new test plus the whole package — `Complete`'s refactor is behavior-preserving).

- [ ] **Step 5: Commit**

```bash
git add agent/internal/cheapmodel/caller.go agent/internal/cheapmodel/caller_test.go
git commit -m "cheapmodel: add CompleteRouted for caller-chosen routes"
```

---

### Task 2: `VisionModel` on SessionConfig, ConfigSnapshot, and SessionMeta

**Files:**
- Modify: `agent/session_config.go` (`SessionConfig`, `toSnapshot`, `configFromSnapshot`)
- Modify: `agent/schema/config_snapshot.go` (`ConfigSnapshot`, line 13)
- Modify: `agent/schema/` SessionMeta definition (the file declaring `SessionMeta`; populated in `agent/session_state.go:141-149`)
- Modify: `agent/session_state.go:141-149`

**Interfaces:**
- Produces: `SessionConfig.VisionModel string `json:"vision_model,omitempty"``, mirrored on `schema.ConfigSnapshot.VisionModel` and `schema.SessionMeta.VisionModel`. Tasks 3, 4, 7, 8, 9 consume these.

- [ ] **Step 1: Add the schema field and run the converter round-trip test to watch it fail**

In `agent/schema/config_snapshot.go`, add to `ConfigSnapshot` after the `SandboxNet` field:

```go
	VisionModel                 string                     `json:"vision_model,omitempty"`                 // vision side-channel routing: "" | "off" | "model" | "provider/model"
```

(Align the comment column with the file's existing gofmt alignment by running `gofmt -w agent/schema/config_snapshot.go`.)

Run: `go test ./agent/ -run 'RoundTrip|Snapshot' -v`
Expected: FAIL — the round-trip converter test (the one guarding that `toSnapshot`/`configFromSnapshot` mirror `ConfigSnapshot` exactly) reports the unmirrored `VisionModel` field.

- [ ] **Step 2: Add the SessionConfig field and converters**

In `agent/session_config.go`, add to `SessionConfig` after `SandboxNet` (after line 226):

```go
	// VisionModel routes the image-description vision side-channel: "" uses the
	// session's active model (the default), "off" disables the side-channel, a
	// bare model resolves on the active provider at call time, and
	// "provider/model" pins a provider instance. Runtime changes go through
	// Session.SetVisionModel, which writes this same field under s.mu.
	VisionModel string `json:"vision_model,omitempty"`
```

Add `VisionModel: c.VisionModel,` to `toSnapshot` (after `SandboxNet: c.SandboxNet,`) and `VisionModel: s.VisionModel,` to `configFromSnapshot` (after `SandboxNet: s.SandboxNet,`).

Run: `go test ./agent/ -run 'RoundTrip|Snapshot' -v`
Expected: PASS.

- [ ] **Step 3: Persist on SessionMeta**

In the `agent/schema` file declaring `SessionMeta` (the struct `agent/session_state.go:141-149` populates, next to its `CheapModel` field), add:

```go
	VisionModel string `json:"vision_model,omitempty"`
```

In `agent/session_state.go`, add to the `SessionMeta{...}` literal after `CheapModel: s.profile.CheapModelRefString(),`:

```go
		VisionModel:              s.cfg.VisionModel,
```

Run `gofmt -w agent/session_state.go agent/schema/` to realign the literal.

- [ ] **Step 4: Run the schema and persistence tests**

Run: `go test ./agent/schema/ ./agent/ -run 'Snapshot|SessionMeta|Meta' `
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_config.go agent/schema/config_snapshot.go agent/schema/ agent/session_state.go
git commit -m "agent: add vision_model to SessionConfig, ConfigSnapshot, and SessionMeta"
```

---

### Task 3: `describeImageCall` off-gate and routed execution

**Files:**
- Modify: `agent/session_tools.go` (`describeImageCall`, lines 387-495; new helpers beside it)
- Test: `agent/session_vision_model_test.go` (create)

**Interfaces:**
- Consumes: `cheapmodel.Caller.CompleteRouted` (Task 1); `SessionConfig.VisionModel` (Task 2); the session's `s.cheap` caller (the field `tool_web_fetch.go:196` uses).
- Produces:
  - `const visionModelOff = "off"` — the reserved sentinel, compared case-insensitively.
  - `func resolveVisionRoute(profile *provider.Profile, setting string) (providerName, modelID string, off bool)` — `""` → session route; `off` → `off=true`; `provider/model` → pinned route; bare → active-provider route.

- [ ] **Step 1: Write the failing tests**

Create `agent/session_vision_model_test.go` (package `agent`, mirroring `cov_s3_vision_test.go`'s `s3cov_visionSession` helper and `fakeAdapter`):

```go
package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func visionImageResult() tool.ExecResult {
	return tool.ExecResult{ImageData: []byte("fake-png"), ImageMediaType: "image/png", ImagePurpose: "describe it"}
}

func TestDescribeImage_OffMakesNoCall(t *testing.T) {
	t.Parallel()
	called := false
	sess := s3cov_visionSession(t, SessionConfig{VisionModel: "off"}, func(req llm.Request) llm.Response {
		called = true
		return llm.Response{Message: llm.Assistant("vision")}
	})
	if got := sess.describeImage(context.Background(), visionImageResult()); got != "" {
		t.Fatalf("off description = %q, want empty", got)
	}
	if called {
		t.Fatal("off made a vision call")
	}
}

func TestResolveVisionRoute(t *testing.T) {
	t.Parallel()
	profile := NewOpenAIProfile("session-model")
	cases := []struct {
		setting                        string
		wantProvider, wantModel, wantOff any
	}{}
	_ = cases
	if p, m, off := resolveVisionRoute(profile, ""); p != profile.ID() || m != "session-model" || off {
		t.Fatalf("unset = (%q, %q, %v), want session route", p, m, off)
	}
	if _, _, off := resolveVisionRoute(profile, "OFF"); !off {
		t.Fatal("OFF (case-insensitive) must be the off sentinel")
	}
	if p, m, off := resolveVisionRoute(profile, "anthropic/claude-x"); p != "anthropic" || m != "claude-x" || off {
		t.Fatalf("pinned = (%q, %q, %v)", p, m, off)
	}
	if p, m, off := resolveVisionRoute(profile, "other-model"); p != profile.ID() || m != "other-model" || off {
		t.Fatalf("bare = (%q, %q, %v), want active provider", p, m, off)
	}
	if p, m, off := resolveVisionRoute(profile, "/"); p != profile.ID() || m != "/" || off {
		t.Fatalf("malformed = (%q, %q, %v), want bare fallback", p, m, off)
	}
}
```

(Delete the `cases` placeholder lines — they exist only to keep this snippet's structure obvious; the assertions below them are the test.)

```go
func TestDescribeImage_RoutesToConfiguredProvider(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	openai := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("session-vision")} },
	}}
	anthropic := &fakeAdapter{name: "anthropic", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("routed-vision")} },
	}}
	c := llm.NewClient()
	c.Register(openai)
	c.Register(anthropic)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, VisionModel: "anthropic/claude-x"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	if got := sess.describeImage(context.Background(), visionImageResult()); got != "routed-vision" {
		t.Fatalf("routed description = %q", got)
	}
	if len(openai.Requests()) != 0 {
		t.Fatal("session provider received a vision call despite the pinned route")
	}
	reqs := anthropic.Requests()
	if len(reqs) != 1 || reqs[0].Model != "claude-x" {
		t.Fatalf("anthropic requests = %#v, want one claude-x call", reqs)
	}
}
```

Add `"primeradiant.com/evener/agent/execenv"` to the imports of the new file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run 'TestDescribeImage_OffMakesNoCall|TestResolveVisionRoute|TestDescribeImage_RoutesToConfiguredProvider' -v`
Expected: FAIL — `resolveVisionRoute undefined` and `off` not honored.

- [ ] **Step 3: Implement**

In `agent/session_tools.go`, beside `describeImageCall`:

```go
// visionModelOff is the reserved bare-word setting that disables the vision
// side-channel. Only a slash-free value can be the sentinel: a value with a
// slash always parses as "provider/model", so a provider named "off" stays
// reachable as "off/some-model".
const visionModelOff = "off"

// resolveVisionRoute maps the session's vision_model setting to the route the
// side-channel executes on. "" resolves to the session's active route, "off"
// (case-insensitive) disables the call, "provider/model" pins a provider, and
// a bare model resolves on the active provider at call time — so it follows
// SetModel switches. A malformed "x/" or "/x" value degrades to a bare-model
// lookup on the active provider rather than an unroutable request.
func resolveVisionRoute(profile *provider.Profile, setting string) (providerName, modelID string, off bool) {
	setting = strings.TrimSpace(setting)
	if setting == "" {
		return profile.ID(), profile.Model(), false
	}
	if strings.EqualFold(setting, visionModelOff) {
		return "", "", true
	}
	if prov, model, ok := strings.Cut(setting, "/"); ok && prov != "" && model != "" {
		return prov, model, false
	}
	return profile.ID(), setting, false
}

// visionRouteSupportsReasoning gates reasoning_effort for the vision request:
// the session route uses the profile's own answer (which may carry live
// provider metadata); any other route answers from the embedded catalog, and
// an uncatalogued model gets no effort knob rather than one it may reject.
func visionRouteSupportsReasoning(profile *provider.Profile, providerName, modelID string) bool {
	if providerName == profile.ID() && modelID == profile.Model() {
		return profile.SupportsReasoning()
	}
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		if mi := cat.LookupModelInfo(modelID); mi != nil {
			return mi.SupportsReasoning
		}
	}
	return false
}
```

In `describeImageCall`, extend the profile snapshot (lines 427-429) and branch:

```go
	s.mu.Lock()
	profile := s.profile
	visionSetting := s.cfg.VisionModel
	s.mu.Unlock()

	routeProvider, routeModel, visionOff := resolveVisionRoute(profile, visionSetting)
	if visionOff {
		return visionSideChannelResult{outcome: visionSideChannelSuccess}
	}
```

(Move this block above the prompt/media-part construction so `off` skips all of it; the prompt and media-part code then runs unchanged.)

Replace the reasoning-effort block (lines 458-461) with:

```go
	if visionRouteSupportsReasoning(profile, routeProvider, routeModel) {
		levels := profile.ReasoningEffortLevels()
		if routeProvider != profile.ID() || routeModel != profile.Model() {
			if cat := llm.EmbeddedModelCatalog(); cat != nil {
				if mi := cat.LookupModelInfo(routeModel); mi != nil && len(mi.ReasoningEffortLevels) > 0 {
					levels = mi.ReasoningEffortLevels
				}
			}
		}
		effort := llm.ClampReasoningEffort(visionReasoningEffort, levels)
		req.ReasoningEffort = &effort
	}
```

Replace the direct call (line 465) with the routed caller:

```go
	resp, err := s.cheap.CompleteRouted(visionCtx, profile, routeProvider, routeModel, req)
```

Check `session_tools.go` imports `"primeradiant.com/evener/agent/provider"` (needed for the helper signatures); add it if absent.

- [ ] **Step 4: Run the vision tests**

Run: `go test ./agent/ -run 'Vision|DescribeImage' -v`
Expected: PASS — new tests plus every pre-existing vision test (unset behavior is unchanged).

- [ ] **Step 5: Commit**

```bash
git add agent/session_tools.go agent/session_vision_model_test.go
git commit -m "agent: gate vision side-channel on vision_model and route it through cheapmodel"
```

---

### Task 4: `Session.SetVisionModel`, getter, and `EventVisionModelChanged`

**Files:**
- Modify: `agent/session.go` (new methods beside `SetModel`, line 1092)
- Modify: `agent/events/events.go` (kind const beside `EventModelChanged`, line 77)
- Modify: `agent/events/payloads.go` (data struct beside `ModelChangedData`, line 448)
- Modify: `agent/events/eventdata.go` (`eventKind()` mapping + `EventData` assertion list, lines 61, 116)
- Test: `agent/session_vision_model_test.go` (append)

**Interfaces:**
- Consumes: `SessionConfig.VisionModel` (Task 2); `visionModelOff` (Task 3).
- Produces:
  - `func (s *Session) SetVisionModel(ref string) error` — validates and stores the setting, emits the event.
  - `func (s *Session) VisionModel() string` — current setting for thread-read snapshots.
  - `events.EventVisionModelChanged` + `events.VisionModelChangedData{OldVisionModel, NewVisionModel string}`. Task 6 maps this event to the wire.

- [ ] **Step 1: Write the failing tests**

Append to `agent/session_vision_model_test.go`:

```go
func TestSetVisionModelValidatesAndEmits(t *testing.T) {
	t.Parallel()
	sess := s3cov_visionSession(t, SessionConfig{}, func(req llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("vision")}
	})

	if err := sess.SetVisionModel("anthropic/claude-x"); err == nil {
		t.Fatal("unregistered cross-provider ref must fail")
	}
	if got := sess.VisionModel(); got != "" {
		t.Fatalf("failed set changed the setting to %q", got)
	}
	if err := sess.SetVisionModel("off/some-model"); err != nil {
		t.Fatalf("a provider named off stays reachable as a ref: %v", err)
	}
}

func TestSetVisionModelStoresAndEmitsEvent(t *testing.T) {
	t.Parallel()
	sess := s3cov_visionSession(t, SessionConfig{}, func(req llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("vision")}
	})
	var sawEvent bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind == events.EventVisionModelChanged {
				if d, ok := ev.Data.(events.VisionModelChangedData); ok && d.NewVisionModel == "off" && d.OldVisionModel == "" {
					sawEvent = true
				}
			}
		}
	}()

	if err := sess.SetVisionModel("off"); err != nil {
		t.Fatalf("SetVisionModel(off): %v", err)
	}
	if got := sess.VisionModel(); got != "off" {
		t.Fatalf("VisionModel() = %q, want off", got)
	}
	if got := sess.cfg.toSnapshot().VisionModel; got != "off" {
		t.Fatalf("snapshot VisionModel = %q, want off (persistence rides the snapshot)", got)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	<-done
	if !sawEvent {
		t.Fatal("no EventVisionModelChanged emitted")
	}
}
```

Add `"primeradiant.com/evener/agent/events"` to the test file's imports. Note: `s3cov_visionSession`'s client registers only the `openai` fakeAdapter, which is why `"anthropic/claude-x"` must fail and `"off/some-model"` — a ref whose provider is literally `off` — must also fail... unless the test's intent is clearer with a registered provider. Adjust the first test: keep `"anthropic/claude-x"` expecting failure, and replace the `off/some-model` case with a malformed-ref case:

```go
	if err := sess.SetVisionModel("anthropic/"); err == nil {
		t.Fatal("malformed ref with an empty model must fail")
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run 'TestSetVisionModel' -v`
Expected: FAIL — `SetVisionModel`, `VisionModel`, and `events.EventVisionModelChanged` undefined.

- [ ] **Step 3: Implement the event**

In `agent/events/events.go` after `EventReasoningEffortChanged` (line 80):

```go
	// EventVisionModelChanged reports that SetVisionModel committed a change to
	// the vision side-channel routing.
	EventVisionModelChanged EventKind = "VISION_MODEL_CHANGED"
```

In `agent/events/payloads.go` after `ModelChangedData`:

```go
// VisionModelChangedData is the payload for an EventVisionModelChanged event:
// SetVisionModel committed a new vision side-channel setting ("", "off", or a
// model ref). Old and new let subscribers diff the change.
type VisionModelChangedData struct {
	OldVisionModel string `json:"old_vision_model"`
	NewVisionModel string `json:"new_vision_model"`
}
```

In `agent/events/eventdata.go`, add beside the `ModelChangedData` mapping (line 61):

```go
func (VisionModelChangedData) eventKind() EventKind  { return EventVisionModelChanged }
```

and add `_ EventData = VisionModelChangedData{}` to the assertion list beside line 116. Run `gofmt -w agent/events/` to align the mapping block.

- [ ] **Step 4: Implement the session methods**

In `agent/session.go` beside `SetModel`:

```go
// SetVisionModel changes the vision side-channel routing for future image
// reads. The ref is "" (describe with the session's active model), "off"
// (disable the side-channel), a bare model on the active provider, or
// "provider/model" to pin a provider instance, which must be registered in
// the client. It takes effect on the next image read and persists with the
// session config; it never alters the active model itself.
func (s *Session) SetVisionModel(ref string) error {
	ref = strings.TrimSpace(ref)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closingOrClosedLocked() {
		return nil
	}
	if err := s.validateVisionModelRefLocked(ref); err != nil {
		return err
	}
	old := s.cfg.VisionModel
	if old == ref {
		return nil
	}
	s.cfg.VisionModel = ref
	s.emit(events.EventVisionModelChanged, events.VisionModelChangedData{OldVisionModel: old, NewVisionModel: ref})
	return nil
}

func (s *Session) validateVisionModelRefLocked(ref string) error {
	if ref == "" || strings.EqualFold(ref, visionModelOff) {
		return nil
	}
	prov, model, ok := strings.Cut(ref, "/")
	if ok && (prov == "" || model == "") {
		return fmt.Errorf("invalid vision model ref %q: want \"model\" or \"provider/model\"", ref)
	}
	if ok && !strings.EqualFold(prov, s.profile.ID()) && !sessionClientHasProvider(s.client, prov) {
		return fmt.Errorf("vision model provider %q is not configured or has no credential (active provider %q)", prov, s.profile.ID())
	}
	return nil
}

func sessionClientHasProvider(client *llm.Client, name string) bool {
	if client == nil {
		return false
	}
	for _, p := range client.ProviderNames() {
		if strings.EqualFold(p, name) {
			return true
		}
	}
	return false
}

// VisionModel returns the session's configured vision side-channel setting
// ("", "off", or a model ref) for thread-read snapshots.
func (s *Session) VisionModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.VisionModel
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./agent/ -run 'TestSetVisionModel' -v && go test ./agent/events/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/events/events.go agent/events/payloads.go agent/events/eventdata.go agent/session_vision_model_test.go
git commit -m "agent: add Session.SetVisionModel with VISION_MODEL_CHANGED event"
```

---

### Task 5: Appwire protocol — method, notification, capability, thread field, client method

**Files:**
- Modify: `appwire/types.go` (method consts near line 35, notification consts near line 115, `ThreadCapabilities` line 501, `EvenerThread` line 272, params beside `ThreadReasoningEffortSetParams` line 1268)
- Modify: `appwire/client.go` (beside `ThreadReasoningEffortSet`, line 406)
- Test: `appwire/types_test.go` and/or `appwire/client_test.go` (existing decode/client patterns)

**Interfaces:**
- Produces (all consumed by Tasks 6, 7, 9, 10, 12):
  - `MethodThreadVisionModelSet = "thread/vision-model/set"`
  - `NotifyThreadVisionModelChanged = "thread/vision-model/changed"`
  - `ThreadVisionModelSetParams{Ref string, VisionModel string}`
  - `ThreadVisionModelChangedParams{ThreadID string, Ref string, VisionModel string}`
  - `ThreadCapabilities.ChangeVisionModel bool `json:"changeVisionModel"``
  - `EvenerThread.VisionModel string `json:"visionModel,omitempty"``
  - `func (c *Client) ThreadVisionModelSet(ctx context.Context, params ThreadVisionModelSetParams) error`

- [ ] **Step 1: Write the failing test**

Append to `appwire/types_test.go` (mirroring an existing params-decode test):

```go
func TestThreadVisionModelSetParamsDecode(t *testing.T) {
	raw := []byte(`{"ref":"local:01X","visionModel":"anthropic/claude-haiku-4-5"}`)
	var p ThreadVisionModelSetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Ref != "local:01X" || p.VisionModel != "anthropic/claude-haiku-4-5" {
		t.Fatalf("params = %+v", p)
	}
}
```

(Adjust to the file's existing import set and test style; if the file already imports `encoding/json` and uses this shape, it drops in unchanged.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./appwire/ -run TestThreadVisionModelSetParamsDecode -v`
Expected: FAIL — `ThreadVisionModelSetParams` undefined.

- [ ] **Step 3: Implement the types**

In `appwire/types.go`:

After `MethodThreadReasoningEffortSet` (line 36):

```go
	MethodThreadVisionModelSet      = "thread/vision-model/set"
```

After `NotifyThreadReasoningEffortChanged` (line 115):

```go
	// NotifyThreadVisionModelChanged pushes a mid-session vision-model change.
	// See ThreadVisionModelChangedParams.
	NotifyThreadVisionModelChanged = "thread/vision-model/changed"
```

In `ThreadCapabilities` after `ChangeModel` (line 509):

```go
	// ChangeVisionModel advertises support for thread/vision-model/set. True for
	// a live evener session whose daemon wires a vision-model hook.
	ChangeVisionModel bool `json:"changeVisionModel"`
```

In `EvenerThread` after `SupportsReasoning` (line 374):

```go
	// VisionModel is the session's vision side-channel setting: "" describes
	// with the session model, "off" disables the side-channel, anything else is
	// a model ref. Snapshot-only like the effort fields beside it; live updates
	// arrive as thread/vision-model/changed.
	VisionModel string `json:"visionModel,omitempty"`
```

After `ThreadReasoningEffortSetParams` (line 1271):

```go
// ThreadVisionModelSetParams sets the vision side-channel routing on a running
// session. VisionModel carries the whole setting: "" describes with the
// session's active model, "off" disables the side-channel, and any other value
// is a "model" or "provider/model" ref — a single string because a
// provider/model split cannot express the first two states.
type ThreadVisionModelSetParams struct {
	Ref         string `json:"ref"`
	VisionModel string `json:"visionModel"`
}

// ThreadVisionModelChangedParams reports a mid-session vision-model change.
type ThreadVisionModelChangedParams struct {
	ThreadID    string `json:"threadId"`
	Ref         string `json:"ref"`
	VisionModel string `json:"visionModel"`
}
```

In `appwire/client.go` after `ThreadReasoningEffortSet` (line 408):

```go
func (c *Client) ThreadVisionModelSet(ctx context.Context, params ThreadVisionModelSetParams) error {
	return c.request(ctx, MethodThreadVisionModelSet, params, nil)
}
```

Run `gofmt -w appwire/`.

- [ ] **Step 4: Run the appwire tests**

Run: `go test ./appwire/...`
Expected: PASS. If a fuzz-golden test (e.g. `FuzzMethodParams` goldens) fails because the new types extend the decode surface, regenerate goldens with the repo's mechanism: `make fuzz-goldens`, rerun, and stage the changed golden files.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go appwire/client.go appwire/types_test.go
git commit -m "appwire: add thread/vision-model/set + /changed and visionModel thread field"
```

---

### Task 6: Projector — map `EventVisionModelChanged` to `thread/vision-model/changed`

**Files:**
- Modify: `internal/appprojector/` (the projector file containing the `EventReasoningEffortChanged` case exercised by `appwire_projection_test.go:100-116`)
- Test: `internal/appprojector/appwire_projection_test.go`

**Interfaces:**
- Consumes: `events.EventVisionModelChanged` + `VisionModelChangedData` (Task 4); `appwire.NotifyThreadVisionModelChanged` + `ThreadVisionModelChangedParams` (Task 5).

- [ ] **Step 1: Write the failing test**

Append to `internal/appprojector/appwire_projection_test.go`, mirroring `TestProject_ReasoningEffortChanged` (lines 100-116):

```go
func TestProject_VisionModelChanged(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventVisionModelChanged,
		Data: events.VisionModelChangedData{OldVisionModel: "", NewVisionModel: "off"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyThreadVisionModelChanged {
		t.Fatalf("want one thread/vision-model/changed notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadVisionModelChangedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.ThreadVisionModelChangedParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.VisionModel != "off" {
		t.Fatalf("params = %+v", params)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appprojector/ -run TestProject_VisionModelChanged -v`
Expected: FAIL — zero notifications projected for the unknown kind.

- [ ] **Step 3: Implement**

In the projector's `Project` switch, beside the `EventReasoningEffortChanged` case (the one producing `NotifyThreadReasoningEffortChanged` with the projector's thread id/ref):

```go
	case events.EventVisionModelChanged:
		d, ok := ev.Data.(events.VisionModelChangedData)
		if !ok {
			return nil
		}
		return []AppNotification{{
			Method: appwire.NotifyThreadVisionModelChanged,
			Params: appwire.ThreadVisionModelChangedParams{
				ThreadID:    p.threadID,
				Ref:         p.ref,
				VisionModel: d.NewVisionModel,
			},
		}}
```

Match the exact names of the projector's id/ref fields and its notification slice type to the neighboring `EventReasoningEffortChanged` case — copy that case's shape verbatim and change only the kind, method, and params.

- [ ] **Step 4: Run the projector tests**

Run: `go test ./internal/appprojector/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/appprojector/
git commit -m "appprojector: project VISION_MODEL_CHANGED to thread/vision-model/changed"
```

---

### Task 7: Daemon — handler, hook, capability, thread/read field

**Files:**
- Modify: `server/appwire_runtime.go` (register the handler beside line 531; new `handleAppThreadVisionModelSet` beside line 1032; capability beside line 1412)
- Modify: `server/server.go` (`SetVisionModelFunc` beside `SetModelFunc`, line 615; new `visionModelFunc` field beside `modelFunc`)
- Modify: `server/server_handlers.go` (capability beside line 354)
- Modify: the two `EvenerThread` producers — `server/appwire_runtime.go`'s envelope builder and `cmd/evener/serve.go`'s evener-thread builder (both surfaced by `rg "ReasoningEffort:" server/ cmd/evener/`)
- Modify: `cmd/evener/serve.go` and `cmd/evener/run.go` (wire the hook beside the existing `SetModelFunc` wiring)
- Test: `server/model_set_test.go` (append; it has the exact mid-turn/reserved/hook-error patterns)

**Interfaces:**
- Consumes: `Session.SetVisionModel` / `Session.VisionModel` (Task 4); appwire types (Task 5).
- Produces: `func (s *Server) SetVisionModelFunc(fn func(string) error)`; daemon thread reads carry `visionModel` and `changeVisionModel` (Task 9 and the frontends consume).

- [ ] **Step 1: Write the failing tests**

Append to `server/model_set_test.go`, mirroring its three existing tests:

```go
func TestHandleAppThreadVisionModelSet_RejectsWhileProcessing(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	s.SetVisionModelFunc(func(string) error { called = true; return nil })
	s.SetProcessing(true)

	if _, err := s.handleAppThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{Ref: "local:x", VisionModel: "off"}); err == nil {
		t.Fatal("expected an error while a turn is processing")
	}
	if called {
		t.Fatal("vision-model hook must not be invoked while a turn is active")
	}
}

func TestHandleAppThreadVisionModelSet_HookReceivesTheSetting(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	s.SetVisionModelFunc(func(v string) error { got = v; return nil })

	if _, err := s.handleAppThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{Ref: "local:x", VisionModel: "anthropic/claude-x"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got != "anthropic/claude-x" {
		t.Fatalf("hook got %q, want the ref unchanged", got)
	}
}

func TestHandleAppThreadVisionModelSet_HookErrorSurfacesAsWireError(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetVisionModelFunc(func(string) error { return errors.New("vision model provider \"nope\" is not configured") })

	if _, err := s.handleAppThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{Ref: "local:x", VisionModel: "nope/m"}); err == nil {
		t.Fatal("hook error must surface")
	}
}
```

Check the test file's existing imports (`errors`, `context`, `appwire`) and the exact `Ref` value its other tests use (`requireRootMutationTarget` accepts `local:` refs); mirror them.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/ -run 'TestHandleAppThreadVisionModelSet' -v`
Expected: FAIL — `SetVisionModelFunc` / `handleAppThreadVisionModelSet` undefined.

- [ ] **Step 3: Implement the server pieces**

In `server/server.go`, beside `modelFunc` and `SetModelFunc` (line 614-619):

```go
// SetVisionModelFunc sets the function called by thread/vision-model/set.
func (s *Server) SetVisionModelFunc(fn func(string) error) {
	s.mu.Lock()
	s.visionModelFunc = fn
	s.mu.Unlock()
}
```

(Add the `visionModelFunc func(string) error` field beside `modelFunc` in the Server struct.)

In `server/appwire_runtime.go`, register beside line 531:

```go
	appserver.HandleTyped(router, appwire.MethodThreadVisionModelSet, s.handleAppThreadVisionModelSet)
```

Add the handler beside `handleAppThreadModelSet` (line 1032), mirroring its mid-turn gate and error mapping:

```go
func (s *Server) handleAppThreadVisionModelSet(_ context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	processing := s.processing
	reservedTurnID := strings.TrimSpace(s.appReservedTurnID)
	fn := s.visionModelFunc
	s.mu.RUnlock()
	if processing || reservedTurnID != "" {
		msg := "session is processing"
		if reservedTurnID != "" {
			msg = "turn " + reservedTurnID + " is active"
		}
		return appwire.EmptyResponse{}, appwire.Conflict(msg)
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("vision model change not available")
	}
	// "" and "off" are legitimate setting values (session-model and disabled),
	// so unlike model/set there is no empty-value rejection here; ref shape is
	// the session's job to validate (Session.SetVisionModel).
	if err := fn(params.VisionModel); err != nil {
		return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}
```

Add `ChangeVisionModel: s.visionModelFunc != nil && !closed,` beside both `ChangeModel:` capability sites (`server/server_handlers.go:354` and `server/appwire_runtime.go:1412`).

- [ ] **Step 4: Populate `visionModel` on thread reads**

At each `EvenerThread{...}` producer (the envelope in `server/appwire_runtime.go` and the evener-thread builder in `cmd/evener/serve.go` — the two hits from `rg "ReasoningEffort:" server/ cmd/evener/`), add `VisionModel` beside the `ReasoningEffort` assignment, sourced from the same session-status path that supplies `ReasoningEffort`. If that path is a struct (e.g. a server status/info struct), add a `VisionModel string` field to it and populate it from `Session.VisionModel()` in `cmd/evener`'s agent→server mapping, exactly the hop `ReasoningEffort` takes.

- [ ] **Step 5: Wire the hook in the daemon**

In `cmd/evener/serve.go` and `cmd/evener/run.go`, beside the existing `srv.SetModelFunc(...)` wiring (which passes `sess.SetModel` or an equivalent closure), add:

```go
	srv.SetVisionModelFunc(sess.SetVisionModel)
```

(Use the same session variable the `SetModelFunc` call uses.)

- [ ] **Step 6: Run the server and daemon tests**

Run: `go test ./server/ ./cmd/evener/ -run 'VisionModel|ModelSet|ThreadRead|Capabilities' -v` then `go test ./server/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add server/ cmd/evener/serve.go cmd/evener/run.go
git commit -m "server: handle thread/vision-model/set with capability and thread-read field"
```

---

### Task 8: CLI `--vision-model` flag

**Files:**
- Modify: `cmd/evener/main.go` (`runCLIFlags` line 30-32; flag registration beside line 250; `runConfig` call beside line 200)
- Modify: `cmd/evener/run.go` (`runConfig` struct + SessionConfig construction beside line 247)
- Modify: `cmd/evener/serve.go` (flag registration + SessionConfig construction beside line 417; `applyVisionModel` beside `applyFastCheapModel`, line 1248)
- Test: `cmd/evener/serve_fast_cheap_model_test.go` (append — it has `clientWithProviders` and the cross-provider test patterns)

**Interfaces:**
- Consumes: `SessionConfig.VisionModel` (Task 2); `clientHasProvider` (existing, serve.go:1262).

- [ ] **Step 1: Write the failing tests**

Append to `cmd/evener/serve_fast_cheap_model_test.go`, mirroring its existing tests:

```go
func TestApplyVisionModel_PassthroughStates(t *testing.T) {
	profile := NewOpenAIProfile("gpt-5.2")
	for _, raw := range []string{"", "off", "OFF", "gpt-4.1-mini", "openai/gpt-4.1-mini"} {
		got, err := applyVisionModel(profile, raw, clientWithProviders("openai"))
		if err != nil {
			t.Fatalf("applyVisionModel(%q): %v", raw, err)
		}
		want := raw
		if raw == "OFF" {
			want = "off" // sentinel canonicalizes to lowercase
		}
		if got != want {
			t.Fatalf("applyVisionModel(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestApplyVisionModel_CrossProviderWhenRegistered(t *testing.T) {
	profile := NewOpenAIProfile("gpt-5.2")
	got, err := applyVisionModel(profile, "anthropic/claude-haiku-4-5", clientWithProviders("openai", "anthropic"))
	if err != nil {
		t.Fatalf("applyVisionModel: %v", err)
	}
	if got != "anthropic/claude-haiku-4-5" {
		t.Fatalf("got %q", got)
	}
	if profile.Model() != "gpt-5.2" {
		t.Fatal("applyVisionModel must not touch the active profile")
	}
}

func TestApplyVisionModel_CrossProviderRejectedWhenNotRegistered(t *testing.T) {
	profile := NewOpenAIProfile("gpt-5.2")
	if _, err := applyVisionModel(profile, "anthropic/claude-x", clientWithProviders("openai")); err == nil {
		t.Fatal("unregistered cross-provider ref must fail")
	}
}

func TestApplyVisionModel_MalformedRef(t *testing.T) {
	profile := NewOpenAIProfile("gpt-5.2")
	for _, raw := range []string{"anthropic/", "/claude-x"} {
		if _, err := applyVisionModel(profile, raw, clientWithProviders("openai", "anthropic")); err == nil {
			t.Fatalf("malformed ref %q must fail", raw)
		}
	}
}
```

(Use the test file's own profile constructor and `clientWithProviders` helper names — mirror its imports.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/evener/ -run 'TestApplyVisionModel' -v`
Expected: FAIL — `applyVisionModel` undefined.

- [ ] **Step 3: Implement `applyVisionModel` and the flag**

In `cmd/evener/serve.go` beside `applyFastCheapModel`:

```go
// applyVisionModel validates the --vision-model ref against the registered
// providers and returns the canonical SessionConfig.VisionModel value. "off"
// (canonicalized to lowercase) disables the side-channel, a bare model keeps
// the active provider, and "provider/model" pins a provider that must be
// registered (configured AND credentialed) — the same rule --fast-cheap-model
// enforces. The active profile is never touched.
func applyVisionModel(profile *provider.Profile, raw string, client *llm.Client) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.EqualFold(raw, "off") {
		return "off", nil
	}
	if prov, model, ok := strings.Cut(raw, "/"); ok {
		if prov == "" || model == "" {
			return "", fmt.Errorf("--vision-model %q is malformed: want \"model\" or \"provider/model\"", raw)
		}
		if prov != profile.ID() && !clientHasProvider(client, prov) {
			return "", fmt.Errorf("--vision-model provider %q is not configured or has no credential (active provider %q); available providers: %s",
				prov, profile.ID(), strings.Join(client.ProviderNames(), ", "))
		}
	}
	return raw, nil
}
```

In `cmd/evener/main.go`: add `visionModel *string` to `runCLIFlags` (after `fastCheapModel`, line 32); register beside the `--fast-cheap-model` flag (line 250):

```go
	flags.visionModel = fs.String("vision-model", "", "vision side-channel model: 'off' disables image description, 'provider/model' or bare 'model' routes it (default: the session model)")
```

Pass `visionModel: *flags.visionModel` into the `runConfig` literal (line 200 area).

In `cmd/evener/run.go`: add `visionModel string` to `runConfig`; at the site where `applyFastCheapModel` runs, add:

```go
	visionModel, err := applyVisionModel(profile, cfg.visionModel, client)
	if err != nil {
		return err
	}
```

(Use that site's own profile/client variable names and error style.) Set `VisionModel: visionModel,` on the `agent.SessionConfig` literal (line 247 area).

In `cmd/evener/serve.go`: register the `--vision-model` flag the same way for `serve` and apply the same validation + SessionConfig assignment at its SessionConfig construction (line 417 area).

- [ ] **Step 4: Run the CLI tests**

Run: `go test ./cmd/evener/ -run 'TestApplyVisionModel|TestApplyFastCheapModel|Flag' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener/main.go cmd/evener/run.go cmd/evener/serve.go cmd/evener/serve_fast_cheap_model_test.go
git commit -m "cmd/evener: add --vision-model flag with cross-provider validation"
```

---

### Task 9: Hub — set-with-resume and thread-read passthrough

**Files:**
- Create: `cmd/evener-hub/app_vision_model.go`
- Modify: `cmd/evener-hub/app_rpc.go` (register beside line 533)
- Modify: `cmd/evener-hub/internal/appsource/` (add `SetThreadVisionModel` beside the source interface's `SetThreadModel`)
- Modify: `cmd/evener-hub/app_threadread.go` (`pastEntryThread`: `visionModel` + `changeVisionModel` for cold threads)
- Test: `cmd/evener-hub/app_model_test.go` or the app's existing set-with-resume test file (mirror its `thread/model/set` tests)

**Interfaces:**
- Consumes: appwire types + `Client.ThreadVisionModelSet` (Task 5); daemon handler + thread field (Task 7); `schema.SessionMeta.VisionModel` (Task 2).

- [ ] **Step 1: Write the failing test**

In the hub test file covering `setThreadModelWithResume` (find it with `rg -l "setThreadModelWithResume" cmd/evener-hub/`), add the parallel vision test: a source whose first `SetThreadVisionModel` answers session-unavailable, assert the hub resumes and retries once, and assert the second call receives `VisionModel: "off"` unchanged. Mirror the model test's fixture construction verbatim — same fake source, same `hubcore.WebConfig` — changing only the method names and params type.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/evener-hub/ -run 'VisionModel' -v`
Expected: FAIL — `setThreadVisionModelWithResume` undefined.

- [ ] **Step 3: Implement**

Create `cmd/evener-hub/app_vision_model.go`, mirroring `app_model.go` lines 11-39 exactly:

```go
package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadVisionModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadVisionModelSetParams) error {
	err := setThreadVisionModelOnce(ctx, cfg, sources, params)
	if err == nil {
		return nil
	}
	if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
		return err
	}
	if !shouldResumeAfterSessionUnavailable(err) {
		return err
	}
	if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref}); resumeErr != nil {
		return resumeErr
	}
	return setThreadVisionModelOnce(ctx, cfg, sources, params)
}

func setThreadVisionModelOnce(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadVisionModelSetParams) error {
	_, err := withDeletionTargetOwnership(cfg, params.Ref, "", "", func() (struct{}, error) {
		source, err := sourceForThreadWithManagedLaunchUnlocked(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return struct{}{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "vision-model"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, source.SetThreadVisionModel(ctx, params)
	})
	return err
}
```

In `cmd/evener-hub/internal/appsource/`, add `SetThreadVisionModel(ctx, appwire.ThreadVisionModelSetParams) error` to the source interface beside `SetThreadModel`, implemented on every source that implements `SetThreadModel` by forwarding `client.ThreadVisionModelSet(ctx, params)` — copy each `SetThreadModel` implementation's body and change the method call.

In `cmd/evener-hub/app_rpc.go`, register beside line 533:

```go
	appserver.HandleTyped(server.Router(), appwire.MethodThreadVisionModelSet, func(ctx context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, setThreadVisionModelWithResume(ctx, cfg, sources, params)
	})
```

In `cmd/evener-hub/app_threadread.go`'s `pastEntryThread` (cold threads): populate `Thread.Evener.VisionModel` from the persisted session meta's `VisionModel` field (Task 2) at the same site that entry sources other evener-thread fields, and set the entry's `changeVisionModel` capability to match its `changeModel` answer (a cold evener session can be resumed and then accept the set, exactly like model/set).

- [ ] **Step 4: Run the hub tests**

Run: `go test ./cmd/evener-hub/ -run 'VisionModel|ThreadModel|ThreadRead' -v` then `go test ./cmd/evener-hub/internal/appsource/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/app_vision_model.go cmd/evener-hub/app_rpc.go cmd/evener-hub/internal/appsource/ cmd/evener-hub/app_threadread.go cmd/evener-hub/
git commit -m "hub: forward thread/vision-model/set with resume and expose visionModel on reads"
```

---

### Task 10: Frontend — protocol model, reducer, store action

**Files:**
- Modify: `cmd/evener-hub/frontend/src/protocol/model.ts` (`ThreadModel` line 197; the capabilities interface declaring `changeModel`; the wire `Thread` type's `evener` object carrying `reasoningEffort`)
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.ts` (thread hydrate near line 327; new notification case beside line 870)
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` (interface line 171; implementation beside line 1959)
- Test: `cmd/evener-hub/frontend/src/protocol/reducer.test.ts`, `cmd/evener-hub/frontend/src/stores/threads.test.ts`

**Interfaces:**
- Produces: `ThreadModel.visionModel: string`; capabilities `changeVisionModel: boolean`; `threadsStore.setVisionModel(ref: string, visionModel: string): Promise<void>`. Task 11 consumes all three.

- [ ] **Step 1: Write the failing tests**

In `protocol/reducer.test.ts`, beside the `thread/reasoning-effort/changed` test (line 2084-2092):

```ts
it("applies thread/vision-model/changed", () => {
  const model = makeModel(/* mirror the neighboring test's factory */);
  const next = reduce(
    model,
    {
      method: "thread/vision-model/changed",
      params: { threadId: "thr_t", ref: "ref_t", visionModel: "anthropic/claude-haiku-4-5" },
    },
    2000,
  );
  expect(next.visionModel).toBe("anthropic/claude-haiku-4-5");
});
```

(Match the neighboring test's actual factory and `reduce` call signature verbatim.)

In `stores/threads.test.ts`, mirror the existing `setModel` test: `setVisionModel("ref", "off")` issues one `thread/vision-model/set` request with params `{ ref: "ref", visionModel: "off" }` and maps a Conflict rejection the same way `setModel` does.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/protocol/reducer.test.ts src/stores/threads.test.ts`
Expected: FAIL — `visionModel` not on the model, `setVisionModel` not on the store.

- [ ] **Step 3: Implement**

In `protocol/model.ts`: add `visionModel: string;` to `ThreadModel` beside `reasoningEffort?`; add `changeVisionModel: boolean;` to the capabilities interface beside `changeModel`; on the wire `Thread` type's `evener` object (the one carrying `reasoningEffort`), add `visionModel?: string;`.

In `protocol/reducer.ts`:
- In the thread-read hydrate (near line 327): add `visionModel: thread.evener.visionModel ?? "",` beside `reasoningEffort: thread.evener.reasoningEffort,`.
- Add the notification case beside `thread/reasoning-effort/changed` (line 870-873):

```ts
    case "thread/vision-model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, visionModel: n.params.visionModel, lastFrameAt: now };
    }
```

In `stores/threads.ts`:
- Interface, beside line 171: `setVisionModel(ref: string, visionModel: string): Promise<void>;`
- Implementation, beside `setModel` (line 1959-1967):

```ts
  async setVisionModel(ref, visionModel) {
    const client = requireClient();
    try {
      await client.request("thread/vision-model/set", { ref, visionModel });
    } catch (err) {
      throw mapConflict(err);
    }
  },
```

- [ ] **Step 4: Biome, then run the frontend tests**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/protocol/model.ts src/protocol/reducer.ts src/stores/threads.ts src/protocol/reducer.test.ts src/stores/threads.test.ts && npx vitest run src/protocol/reducer.test.ts src/stores/threads.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/protocol/model.ts cmd/evener-hub/frontend/src/protocol/reducer.ts cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/protocol/reducer.test.ts cmd/evener-hub/frontend/src/stores/threads.test.ts
git commit -m "web: carry visionModel through thread state and add setVisionModel"
```

---

### Task 11: Frontend — `VisionModelSwitch` picker in the status row

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/chrome/VisionModelSwitch.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/session/chrome/VisionModelSwitch.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.tsx` (render beside line 161)

**Interfaces:**
- Consumes: `ThreadModel.visionModel`, `capabilities.changeVisionModel`, `threadsStore.setVisionModel` (Task 10); `ModelSwitchTrigger`, `fetchModelCatalog`, `mergeScopedCatalog`, `modelLabel` (existing, used by `ModelSwitch.tsx`).

- [ ] **Step 1: Write the failing test**

Create `VisionModelSwitch.test.tsx` mirroring `ModelSwitch.test.tsx`'s render/pick flow:

```tsx
// Cases:
// 1. Unset: trigger label renders the session model's qualified label and the
//    catalog's "Current model" row sends "" via setVisionModel.
// 2. "off": trigger label renders "Off".
// 3. Pinned ref: trigger label renders the ref.
// 4. A vision-capable catalog row sends "provider/model"; a row with
//    supportsVision absent/false does not appear.
// 5. capabilities.changeVisionModel false disables the trigger.
```

Implement with the same testing-library idioms as `ModelSwitch.test.tsx` (its store stubbing, `render`, `fireEvent`/`userEvent`, and toast assertions).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/VisionModelSwitch.test.tsx`
Expected: FAIL — component does not exist.

- [ ] **Step 3: Implement the component**

Create `VisionModelSwitch.tsx`:

```tsx
// VisionModelSwitch: the per-session vision-model picker. Same trigger and
// catalog popover as ModelSwitch (issue #198's shared control), but the
// catalog is filtered to vision-capable entries and gains two pseudo-entries
// ahead of it: "Current model" (the unset default — the side-channel describes
// with the session's active model) and "Off" (the side-channel is disabled).
// Both map onto the single visionModel wire string, which is the whole
// setting: "" for current-model, "off" for disabled, "provider/model" for a
// pinned route.
import { useCallback } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { type ModelCatalog, type ModelCatalogEntry, useToasts } from "../../../widgets";
import { fetchModelCatalog } from "../../../widgets/modelCatalog/catalogClient";
import { mergeScopedCatalog } from "../../../widgets/modelCatalog/scopedCatalog";
import { ModelSwitchTrigger } from "./ModelSwitchTrigger";
import { modelLabel } from "./statusFormat";

const CURRENT_MODEL_ID = "";
const OFF_MODEL_ID = "off";

const PSEUDO_ENTRIES: ModelCatalogEntry[] = [
  { provider: "", model: CURRENT_MODEL_ID, displayName: "Current model" },
  { provider: "", model: OFF_MODEL_ID, displayName: "Off" },
];

export interface VisionModelSwitchProps {
  sessionRef: string;
  model: ThreadModel;
}

export function VisionModelSwitch({ sessionRef, model }: VisionModelSwitchProps) {
  const toasts = useToasts();
  const disabled = !model.capabilities.changeVisionModel;
  const currentLabel =
    model.visionModel === ""
      ? modelLabel(model.modelProvider, model.model)
      : model.visionModel === OFF_MODEL_ID
        ? "Off"
        : model.visionModel;

  const loadCatalog = useCallback(async (): Promise<ModelCatalog> => {
    const [scoped, enrichment] = await Promise.all([
      threadsStore.getState().listModels(),
      fetchModelCatalog().catch(() => null),
    ]);
    const merged = mergeScopedCatalog(scoped.data, enrichment);
    return [...PSEUDO_ENTRIES, ...merged.filter((entry) => entry.supportsVision === true)];
  }, []);

  async function handlePick(entry: ModelCatalogEntry): Promise<void> {
    const visionModel = entry.provider === "" ? entry.model : `${entry.provider}/${entry.model}`;
    try {
      await threadsStore.getState().setVisionModel(sessionRef, visionModel);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't change vision model", err));
    }
  }

  return (
    <ModelSwitchTrigger
      label={currentLabel}
      value={currentLabel}
      disabled={disabled}
      loadCatalog={loadCatalog}
      onPick={(entry) => void handlePick(entry)}
      data-testid="vision-model-switch-trigger"
      valueTestId="vision-model-switch-value"
    />
  );
}
```

In `StatusRow.tsx`, render it inside the identity span after `ReasoningEffortControl` (line 162):

```tsx
        <VisionModelSwitch sessionRef={sessionRef} model={model} />
```

(Add the import beside `ModelSwitch`'s.)

- [ ] **Step 4: Biome, then run the chrome tests**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/chrome/VisionModelSwitch.tsx src/panes/session/chrome/VisionModelSwitch.test.tsx src/panes/session/chrome/StatusRow.tsx && npx vitest run src/panes/session/chrome/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/chrome/VisionModelSwitch.tsx cmd/evener-hub/frontend/src/panes/session/chrome/VisionModelSwitch.test.tsx cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.tsx
git commit -m "web: add VisionModelSwitch picker to the session status row"
```

---

### Task 12: TUI `/vision-model` command

**Files:**
- Modify: `cmd/evener-tui/hub_command_registry.go` (new entry beside `"model"`, line 324)
- Modify: `cmd/evener-tui/hub_commands.go` (`sendHubVisionModelAction` beside `sendHubEffortAction`, line 642)
- Modify: `cmd/evener-tui/hub_types.go` (`hubSessionCapabilities` line 42 + mapping line 244; `hubSessionDetail` if the current setting is surfaced for the picker)
- Modify: `cmd/evener-tui/hub_update.go` (action confirm beside line 338; vision picker message handling beside the `hubSessionModelsMsg` case)
- Test: `cmd/evener-tui/hub_command_registry_test.go` or the file exercising `/model` (mirror it)

**Interfaces:**
- Consumes: `appwire.Client.ThreadVisionModelSet` (Task 5); `hubSessionCapabilities.ChangeVisionModel`.
- Produces: `func sendHubVisionModelAction(client *appwire.Client, ref appwire.Ref, visionModel string) tea.Cmd`; `/vision-model` registry entry.

- [ ] **Step 1: Write the failing tests**

In the TUI test file exercising the `/model` registry entry (`rg -l '"model"' cmd/evener-tui/ | grep registry` to find it), mirror its tests:

```go
func TestVisionModelCommandRegistered(t *testing.T) {
	cmd, ok := hubCommandByName("vision-model")
	if !ok {
		t.Fatal("no /vision-model command in the registry")
	}
	if cmd.PaletteLabel != "/vision-model" {
		t.Fatalf("PaletteLabel = %q", cmd.PaletteLabel)
	}
	available, _ := cmd.Available(hubCommandContext{mode: hubModeSession, caps: hubSessionCapabilities{ChangeVisionModel: true}})
	if !available {
		t.Fatal("command must be available when the capability is advertised")
	}
	available, _ = cmd.Available(hubCommandContext{mode: hubModeSession})
	if available {
		t.Fatal("command must gate on ChangeVisionModel")
	}
}

func TestSendHubVisionModelAction(t *testing.T) {
	// Mirror the sendHubEffortAction test's scripted appwire client: assert one
	// thread/vision-model/set request with VisionModel unchanged.
	for _, setting := range []string{"", "off", "anthropic/claude-x"} {
		// ... same fixture shape as the effort action test, asserting the params
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/evener-tui/ -run 'TestVisionModelCommand|TestSendHubVisionModelAction' -v`
Expected: FAIL — command and function undefined.

- [ ] **Step 3: Implement**

In `cmd/evener-tui/hub_types.go`: add `ChangeVisionModel bool` to `hubSessionCapabilities` after `ChangeModel` (line 51), and add `ChangeVisionModel: caps.ChangeVisionModel,` to the mapping at line 244. (Verify the mapped source struct — the `caps` there is `appwire.ThreadCapabilities` after Task 5.)

In `cmd/evener-tui/hub_commands.go` beside `sendHubEffortAction`:

```go
// visionModelRefKnown reports whether ref parses as a vision-model setting —
// "", "off", a bare model, or "provider/model" — so /vision-model can reject
// a malformed ref client-side without a wire round trip.
func visionModelRefKnown(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.EqualFold(ref, "off") {
		return true
	}
	prov, model, ok := strings.Cut(ref, "/")
	return !ok || (prov != "" && model != "")
}

func sendHubVisionModelAction(client *appwire.Client, ref appwire.Ref, visionModel string) tea.Cmd {
	return func() tea.Msg {
		err := client.ThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{Ref: ref.String(), VisionModel: visionModel})
		return hubActionMsg{action: "vision-model", err: err}
	}
}
```

In `cmd/evener-tui/hub_command_registry.go`, add beside the `"model"` entry:

```go
	{
		Name:               "vision-model",
		Summary:            "Set vision model (picker) or /vision-model <name|off>",
		PaletteLabel:       "/vision-model",
		PaletteDetail:      "set vision model",
		Scopes:             hubCommandSession,
		UnavailableAction:  "change vision model",
		UnavailableSummary: "Vision model change is not available for this session.",
		Available:          capabilityAvailable(func(c hubSessionCapabilities) bool { return c.ChangeVisionModel }, "source does not advertise change vision model"),
		Run: func(m *hubModel, args string) tea.Cmd {
			setting := strings.TrimSpace(args)
			if setting == "" {
				if m.client == nil {
					m.addSessionSystem("Vision model picker is not available without a hub client.")
					return nil
				}
				m.addSessionSystem("Fetching available models...")
				return fetchHubVisionSessionModels(m.client, m.detail.WorkingDir)
			}
			if !visionModelRefKnown(setting) {
				m.addSessionSystem(fmt.Sprintf("Invalid vision model %q. Use a model name, provider/model, or off.", setting))
				return nil
			}
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return sendHubVisionModelAction(m.client, ref, setting)
		},
	},
```

In `cmd/evener-tui/hub_commands.go` beside `fetchHubSessionModels`:

```go
// fetchHubVisionSessionModels loads the session's launchable models and
// filters them to vision-capable ones (embedded catalog), prepending the two
// pseudo-entries of the vision setting: current-model and off.
func fetchHubVisionSessionModels(client *appwire.Client, workingDir string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: workingDir})
		if err != nil {
			return hubVisionModelsMsg{err: err}
		}
		return hubVisionModelsMsg{models: visionModelPickerItems(modelPickerItemsFromResponse(resp, false))}
	}
}

func visionModelPickerItems(items []tuipick.ModelPickerItem) []tuipick.ModelPickerItem {
	cat := llm.EmbeddedModelCatalog()
	capable := func(id string) bool {
		if cat == nil {
			return false
		}
		_, model := splitProviderModel(id)
		mi := cat.LookupModelInfo(model)
		return mi != nil && mi.SupportsVision
	}
	out := []tuipick.ModelPickerItem{
		{ID: "", Display: "Current model"},
		{ID: "off", Display: "Off"},
	}
	for _, item := range items {
		if capable(item.ID) {
			out = append(out, item)
		}
	}
	return out
}
```

In `cmd/evener-tui/hub_update.go`: define `hubVisionModelsMsg` beside `hubSessionModelsMsg` (same fields: `models []tuipick.ModelPickerItem`, `err error`); in its handler, open a picker stored on a new `m.sessionVisionModelPicker` field (mirroring `m.sessionModelPicker` — `tuipick.NewModelPicker(items, m.detail.VisionModel, m.width)` with title "Select vision model"); in the picker-selection handler, send `sendHubVisionModelAction(m.client, ref, picked.ID)`; in the `hubActionMsg` switch beside `case "model":` (line 338) add `case "vision-model":` → `m.addSessionSystem("Vision model updated.")`. Surface the current setting on `hubSessionDetail` (a `VisionModel string` field populated from the thread payload's `Evener.VisionModel` at the same site other detail fields hydrate) so the picker can mark the active value.

Mirror the `sessionModelPicker` overlay-dispatch handling for `sessionVisionModelPicker` wherever pickers are routed (the `topmostOverlayName`/dispatch sites in `hub_keys.go` and `hub_update.go` that name `sessionModelPicker`).

- [ ] **Step 4: Run the TUI tests**

Run: `go test ./cmd/evener-tui/ -run 'VisionModel' -v` then `go test ./cmd/evener-tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-tui/hub_command_registry.go cmd/evener-tui/hub_commands.go cmd/evener-tui/hub_types.go cmd/evener-tui/hub_update.go cmd/evener-tui/hub_keys.go
git commit -m "tui: add /vision-model command with vision-capable picker"
```

---

### Task 13: Full gates

**Files:** none (verification only)

- [ ] **Step 1: Go gates**

Run: `make lint && make vet && make test`
Expected: all exit 0.

- [ ] **Step 2: Frontend gates**

Run: `make test-web && make test-web-browser`
Expected: all exit 0.

- [ ] **Step 3: Canonical merge gate**

Run: `make merge-approval-gate`
Expected: exit 0. Report any failure verbatim and fix the root cause before closing the work.

---

## Self-Review Notes (completed by the plan author)

- Spec coverage: knob semantics (Tasks 2-3), runtime mutation (4-7), CLI (8), hub (9), web picker (10-11), TUI (12), persistence/resume/inheritance (2 + Task 13 regression), refusal fallback (1, 3), wire naming (5). Non-goals are untouched: no spawn-pane field, no hubapi method, no `auto` mode, no `supports_vision` hard gate.
- Type consistency: `CompleteRouted(ctx, profile, providerName, modelID, req)`; `resolveVisionRoute(profile, setting) (providerName, modelID string, off bool)`; `visionModelOff = "off"`; `SetVisionModel(ref string) error`; `VisionModel() string`; `VisionModelChangedData{OldVisionModel, NewVisionModel}`; wire field `visionModel`; store `setVisionModel(ref, visionModel)`; TUI `sendHubVisionModelAction(client, ref, visionModel)` — identical across tasks.
- Deliberate deviations from mirror-patterns, by design: `ThreadVisionModelSetParams` is a single string (two of three states are not provider/model pairs); no transcript marker turn for vision changes (reasoning-effort precedent, not model precedent).
