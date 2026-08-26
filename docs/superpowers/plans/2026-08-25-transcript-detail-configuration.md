# Unified Transcript Detail Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Evener's fragmented transcript visibility controls with one five-level, Advanced-capable configuration that uses browser-local live views, hub-synced Desktop/Mobile defaults, and one production projector for every transcript surface and Settings preview.

**Architecture:** A versioned AppWire contract and atomic hub store own synchronized defaults. A separate browser store owns local Desktop/Mobile active views and resolves local → hub → shipped precedence. A pure TypeScript projector converts the shared `ThreadModel` into stable visible entries; Session, read-only Transcript, and Settings previews render those entries through one `TranscriptBody`.

**Tech Stack:** Go 1.26, AppWire JSON-RPC and code generation, afero-backed atomic state, TypeScript 6, React 19, Zustand 5, Vitest, Testing Library, CSS Modules, Popover, Sheet, RadioGroup, and headless-Chrome browser guards.

**Spec:** `docs/superpowers/specs/2026-08-25-transcript-detail-configuration-design.md`

## Global Constraints

- Read `docs/developing-evener/testing.md` before changing tests. Keep default tests deterministic and independent of provider credentials, network access, quota, current model behavior, ambient machine state, fixed sleeps, and polling races.
- Use TDD for every task: add the smallest failing test, run it and confirm the expected failure, implement the minimum production behavior, then rerun the focused test.
- Use a fresh `gpt-5.6-luna` implementation subagent for each task. After implementation, run separate specification-compliance and code-quality review gates before accepting the task.
- Do not hand-edit `docs/appwire-protocol.md` or `cmd/evener-hub/frontend/src/protocol/types.gen.ts`; regenerate them with `go generate ./appwire` or `make generate`.
- Preserve the pinned `evener.prefs.showCost` key and every legacy transcript boolean's exact `1`/`0` encoding during the compatibility release.
- Mobile remains `(max-width: 899px)`. Do not add a second device breakpoint; use a container query only for narrow transcript chrome.
- User/agent messages and critical rows are invariant. No regular or Custom configuration may hide questions, permission/escalation requests, active work, steering, warnings, denials, interrupted turns, failed turns, failed tools, non-zero hook failures, or recovery actions.
- The live value is browser-local and layout-specific. It updates every local transcript, survives restart, synchronizes same-origin tabs, and never publishes to the hub.
- Hub defaults are hub-scoped, layout-specific, revisioned, and durable. They sync to every paired client but do not create user/account identity.
- Settings previews use fabricated data and the production projector. They never read `threadsStore`, call the network, show real transcript data, stream fake content, or create an inner scroll region.
- Before frontend gates, run `npx biome check --write` on every touched file under `cmd/evener-hub/frontend/src/`.
- Stage named paths only. Never use `git add .` or `git add -A`.

---

## File Structure

### New Go files

- `appwire/transcript_display.go` — wire config validation, strict patch decoding, shipped defaults, and conflict data.
- `appwire/transcript_display_test.go` — protocol-domain tests for validation, strict decoding, defaults, and JSON shape.
- `cmd/evener-hub/internal/hubcore/transcript_display_store.go` — mutex-protected, atomic, revisioned hub-default store.
- `cmd/evener-hub/internal/hubcore/transcript_display_store_test.go` — default, persistence, revision, conflict, validation, and concurrency tests.
- `cmd/evener-hub/internal/hubcore/transcript_display_store_write_test.go` — pre/post-rename durability fault tests.
- `cmd/evener-hub/app_rpc_transcript_display.go` — GET/PATCH registration and successful-write notification.
- `cmd/evener-hub/app_rpc_transcript_display_test.go` — typed and raw-wire RPC, two-client notification, conflict, and failure tests.

### New frontend domain/store files

- `cmd/evener-hub/frontend/src/transcriptDisplay/config.ts` — frontend domain unions, preset expansion, normalization, wire conversion, strict local codec, summary, and fingerprint.
- `cmd/evener-hub/frontend/src/transcriptDisplay/config.test.ts` — exact vectors, Custom normalization, codec, precedence, and migration tests.
- `cmd/evener-hub/frontend/src/transcriptDisplay/projector.ts` — typed classification and projection to stable item, intent, and critical entries.
- `cmd/evener-hub/frontend/src/transcriptDisplay/projector.test.ts` — complete item/event matrix, invariants, ordering, grouping, and immutability.
- `cmd/evener-hub/frontend/src/transcriptDisplay/renderContext.tsx` — effective config, metadata, surface, and disclosure-scope context for existing renderers.
- `cmd/evener-hub/frontend/src/transcriptDisplay/previewFixture.ts` — deterministic fabricated `ThreadModel` used by Settings and the dev surface.
- `cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts` — local persistence, legacy migration/dual-write, same-origin synchronization, hub refresh/patch/notification, drafts, errors, and effective resolution.
- `cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts` — persistence, migration, synchronization, capability, reconnect, revision, and draft tests.

### New frontend rendering/UI files

- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx` — shared projected body for live, read-only, and preview surfaces.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.test.tsx` — surface parity, regular/Custom levels, and preview behavior.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.tsx` — compact live trigger, stepped radio control, Advanced editor, Popover/Sheet switching, and scope actions.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.test.tsx` — keyboard, scope, Custom, Popover, Sheet, and reset tests.
- `cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css` — toolbar, stepped track, Advanced groups, responsive container rules, and touch geometry.
- `cmd/evener-hub/frontend/src/panes/session/transcript/flow/transcriptViewRegistry.ts` — pre-change capture and post-render restore coordination for all mounted transcripts.
- `cmd/evener-hub/frontend/src/panes/session/transcript/flow/transcriptViewRegistry.test.ts` — multi-pane capture/restore/focus and lifecycle tests.
- `cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.tsx` — one hub-default editor and production-backed example.
- `cmd/evener-hub/frontend/src/panes/settings/sections/transcriptDisplayCard.module.css` — stacked card and non-scrolling preview layout.

### Existing files with focused changes

- `appwire/types.go`, `appwire/protocol.go`, `appwire/protocol_test.go` — methods, notification, feature bit, and typed wire payloads.
- `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/types.gen.ts` — generated outputs.
- `cmd/evener-hub/internal/hubcore/config.go`, `cmd/evener-hub/main.go`, `cmd/evener-hub/web.go`, `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/app_rpc_test.go` — store injection, startup, capability, and handler registration.
- `cmd/evener-hub/frontend/src/stores/connection.ts`, `cmd/evener-hub/frontend/src/shell/AppShell.tsx`, `cmd/evener-hub/frontend/src/shell/ConnectionBanner.tsx` and tests — retain handshake `FeatureSet` so older hubs produce an explicit unsupported state.
- `cmd/evener-hub/frontend/src/stores/prefs.ts` and tests — export guarded legacy access/encoding adapters without changing existing keys.
- `cmd/evener-hub/frontend/src/shell/useIsMobile.ts` and tests — one shared layout subscription usable before host remount.
- `cmd/evener-hub/frontend/src/widgets/radiogroup/index.tsx` and tests — visible-label/accessibility-label split for the Full/Full detail stop.
- `cmd/evener-hub/frontend/src/widgets/disclosure/disclosureStore.ts` and tests — explicit choice versus Full-entry baseline.
- `cmd/evener-hub/frontend/src/panes/session/Session.tsx`, `session.module.css`, and tests — remove the two render trees and header selector; mount the shared body and local toolbar.
- `cmd/evener-hub/frontend/src/panes/transcript/Transcript.tsx` and tests — preserve `job:` dispatch and replace duplicate turn rendering with the shared body.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.tsx`, `ToolCallItem.tsx`, `ToolCallCluster.tsx`, `types.ts`, `transcriptVisibility.ts`, message renderers, CSS, and focused tests — consume projection/context and preserve critical/disclosure rules.
- `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts` and tests — use config fingerprints and registry-driven external transitions.
- `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.tsx`, `transcript.module.css`, and tests — stacked Desktop/Mobile cards and hub-save behavior.
- `cmd/evener-hub/frontend/src/panes/settings/sections/display.tsx` and tests — remove only estimated cost; retain Enter behavior.
- `cmd/evener-hub/frontend/src/panes/settings/sections.ts` and tests — visible label `Transcript display`, stable route id `transcript`.
- `cmd/evener-hub/frontend/src/dev/surface-sections/transcript.tsx` and tests — consume the shared deterministic preview fixture/body.
- `cmd/evener-hub/frontend/src/panes/session/viewModes.ts` and tests — delete after their behavior moves into projector tests.
- `cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx` and `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs` — verify the live control and preview at representative widths.

---

### Task 1: Add the AppWire transcript-display contract

**Files:**
- Create: `appwire/transcript_display.go`
- Create: `appwire/transcript_display_test.go`
- Modify: `appwire/types.go`
- Modify: `appwire/protocol.go`
- Modify: `appwire/protocol_test.go`
- Generate: `docs/appwire-protocol.md`
- Generate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Produces wire methods `evener/settings/transcriptDisplay/get` and `evener/settings/transcriptDisplay/patch`.
- Produces notification `evener/settings/transcriptDisplay/changed`.
- Produces `FeatureSet.TranscriptDisplaySettings`.
- Produces strict `DecodeTranscriptDisplayDefaultsPatchParams(json.RawMessage)` and `ValidateTranscriptDisplayConfig(TranscriptDisplayConfig) error` for Tasks 2 and 3.

- [ ] **Step 1: Add failing protocol-domain tests**

Create tests that pin the method/notification catalogs, feature JSON field, shipped defaults, exact preset/custom validation, unknown-field rejection, missing Custom boolean rejection, invalid enum/version rejection, and trailing-JSON rejection.

```go
func TestTranscriptDisplayShippedDefaults(t *testing.T) {
    got := TranscriptDisplayShippedDefaults()
    if got.Desktop.Config.Content.Level != TranscriptLevelTools || got.Mobile.Config.Content.Level != TranscriptLevelIntent {
        t.Fatalf("defaults = %#v", got)
    }
    if got.Desktop.Revision != 0 || got.Mobile.Revision != 0 {
        t.Fatalf("new revisions must be zero: %#v", got)
    }
}

func TestDecodeTranscriptDisplayPatchRejectsIncompleteCustom(t *testing.T) {
    raw := json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"custom","custom":{"toolIntent":true}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`)
    if _, err := DecodeTranscriptDisplayDefaultsPatchParams(raw); err == nil {
        t.Fatal("expected incomplete Custom vector to fail")
    }
}
```

- [ ] **Step 2: Run the focused tests and confirm the contract is absent**

Run:

```bash
go test ./appwire -run 'TestTranscriptDisplay' -count=1
```

Expected: compile failure because the transcript-display types and functions do not exist.

- [ ] **Step 3: Define wire constants and payload types**

Add named layout, level, hook-detail, config, default, response, patch, changed-notification, and conflict-data types. Use camelCase JSON tags and non-omitempty Custom booleans.

```go
const (
    MethodEvenerSettingsTranscriptDisplayGet   = "evener/settings/transcriptDisplay/get"
    MethodEvenerSettingsTranscriptDisplayPatch = "evener/settings/transcriptDisplay/patch"
    NotifyEvenerSettingsTranscriptDisplayChanged = "evener/settings/transcriptDisplay/changed"
)

type TranscriptDisplayDefaultsPatchParams struct {
    Layout           TranscriptViewportClass `json:"layout"`
    ExpectedRevision uint64                  `json:"expectedRevision"`
    Config           TranscriptDisplayConfig `json:"config"`
}

type TranscriptDisplayDefault struct {
    Revision uint64                  `json:"revision"`
    Config   TranscriptDisplayConfig `json:"config"`
}

type TranscriptDisplayConflictData struct {
    EvenerErrorInfo ErrorInfo               `json:"evenerErrorInfo"`
    Layout          TranscriptViewportClass `json:"layout"`
    Current         TranscriptDisplayDefault `json:"current"`
}
```

Represent content with a discriminator plus nested Custom object so preset and Custom payloads cannot be confused:

```go
type TranscriptDisplayContent struct {
    Kind   TranscriptContentKind          `json:"kind"`
    Level  TranscriptLevel                `json:"level,omitempty"`
    Custom *TranscriptDisplayCustomContent `json:"custom,omitempty"`
}
```

- [ ] **Step 4: Implement strict decoding and validation**

Use `json.Decoder.DisallowUnknownFields`, detect trailing values, inspect the raw nested Custom object for all four required boolean keys, and require exactly one content representation. Return `appwire.InvalidParams` at the RPC boundary; keep the validator itself as an ordinary error for store load validation.

- [ ] **Step 5: Catalog the methods, global notification, and capability field**

Add ScopeHub method specs and the global notification spec. Add `TranscriptDisplaySettings bool` to `FeatureSet`, but leave existing servers' zero value false. Task 3 advertises it only after both handlers are registered, so no intermediate commit claims support that does not exist.

- [ ] **Step 6: Regenerate committed protocol outputs**

Run:

```bash
go generate ./appwire
```

Expected: updates only `docs/appwire-protocol.md` and `cmd/evener-hub/frontend/src/protocol/types.gen.ts` for this contract.

- [ ] **Step 7: Run protocol and generated-output tests**

Run:

```bash
go test ./appwire ./internal/appwiredoc ./internal/appwirets -run 'TestTranscriptDisplay|TestGeneratedFileCurrent|TestCatalog' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the contract**

```bash
git add appwire/types.go appwire/protocol.go appwire/protocol_test.go appwire/transcript_display.go appwire/transcript_display_test.go docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
git commit -m "feat(appwire): define transcript display settings"
```

---

### Task 2: Add the atomic hub-default store

**Files:**
- Create: `cmd/evener-hub/internal/hubcore/transcript_display_store.go`
- Create: `cmd/evener-hub/internal/hubcore/transcript_display_store_test.go`
- Create: `cmd/evener-hub/internal/hubcore/transcript_display_store_write_test.go`

**Interfaces:**
- Consumes Task 1's `appwire.TranscriptDisplayDefaultsResponse`, patch params/response, validator, shipped defaults, and conflict data.
- Produces `NewTranscriptDisplayStore`, `Snapshot`, and `Patch` for Task 3.

- [ ] **Step 1: Add failing store tests**

Cover missing-root defaults, restart persistence, independent revisions, no-op patch behavior, stale same-layout conflict with canonical data, cross-layout concurrency, same-layout concurrency, malformed snapshot fallback/read-only error state, overflow, and strict snapshot decoding.

```go
func TestTranscriptDisplayStorePatchesLayoutsIndependently(t *testing.T) {
    store, err := NewTranscriptDisplayStore(t.TempDir())
    if err != nil { t.Fatal(err) }
    desktop := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
    desktop.Content.Level = appwire.TranscriptLevelActivity
    got, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{
        Layout: appwire.TranscriptViewportDesktop, ExpectedRevision: 0, Config: desktop,
    })
    if err != nil { t.Fatal(err) }
    if got.Revision != 1 || store.Snapshot().Mobile.Revision != 0 {
        t.Fatalf("patch result=%#v snapshot=%#v", got, store.Snapshot())
    }
}
```

- [ ] **Step 2: Run the store tests and confirm the store is absent**

```bash
go test ./cmd/evener-hub/internal/hubcore -run 'TestTranscriptDisplayStore' -count=1
```

Expected: compile failure because `NewTranscriptDisplayStore` does not exist.

- [ ] **Step 3: Implement the in-memory state and validation**

Use one mutex and a snapshot with independent Desktop/Mobile records. `Snapshot` returns a deep value copy. `Patch` validates before locking, checks the selected revision under lock, rejects overflow, writes only the selected record, and increments only that revision.

```go
type TranscriptDisplayStore struct {
    mu     sync.Mutex
    fs     afero.Fs
    root   string
    state  transcriptDisplaySnapshot
    loadErr error
    faults transcriptDisplayStoreFaults
}
```

A malformed durable file sets `loadErr`, serves shipped defaults, and rejects patches until repaired; it must not silently overwrite evidence. `NewTranscriptDisplayStore` returns the usable fallback store together with the load error, and callers must retain the non-nil store while reporting the error. Clean and missing snapshots return the store with a nil error.

- [ ] **Step 4: Implement strict atomic persistence**

Store `transcript-display/state.json` under `HubStateRoot`. Follow the deletion-store sequence: validate, marshal, mkdir `0700`, create a sibling temp file, write, `Sync`, close, rename, then sync the directory. Publish the in-memory state after rename even when post-rename directory sync reports an error.

- [ ] **Step 5: Add pre/post-rename fault tests**

Use afero plus `BeforeRename` and `AfterRename` hooks. Prove pre-rename failure preserves old memory/disk, while post-rename failure retains the new canonical memory value and reloads it from disk.

- [ ] **Step 6: Run focused store tests including race detection**

```bash
go test -race ./cmd/evener-hub/internal/hubcore -run 'TestTranscriptDisplayStore' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the store**

```bash
git add cmd/evener-hub/internal/hubcore/transcript_display_store.go cmd/evener-hub/internal/hubcore/transcript_display_store_test.go cmd/evener-hub/internal/hubcore/transcript_display_store_write_test.go
git commit -m "feat(hub): persist transcript display defaults"
```

---

### Task 3: Expose hub-default GET, PATCH, and notifications

**Files:**
- Create: `cmd/evener-hub/app_rpc_transcript_display.go`
- Create: `cmd/evener-hub/app_rpc_transcript_display_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/config.go`
- Modify: `cmd/evener-hub/main.go`
- Modify: `cmd/evener-hub/web.go`
- Modify: `cmd/evener-hub/app_rpc.go`
- Modify: `cmd/evener-hub/app_rpc_test.go`

**Interfaces:**
- Consumes Task 2's store and Task 1's strict raw decoder.
- Produces live RPC methods and notification behavior consumed by the frontend store in Task 6.

- [ ] **Step 1: Add failing RPC and registration tests**

Use a real in-process AppWire server/client boundary. Cover GET defaults, PATCH persistence, raw unknown fields, stale conflict data, two-client BroadcastAll delivery, no notification on no-op/validation/conflict/durable failure, and the exact handler set.

```go
func TestHubRPCTranscriptDisplayPatchBroadcastsCanonicalValue(t *testing.T) {
    store, err := hubcore.NewTranscriptDisplayStore(t.TempDir())
    if err != nil { t.Fatal(err) }
    hub := newHubRPCTestServer(t, hubcore.WebConfig{TranscriptDisplayStore: store})
    defer hub.Close()

    clientA := dialHubRPC(t, hub)
    defer clientA.Close()
    clientB := dialHubRPC(t, hub)
    defer clientB.Close()
    for _, client := range []*appwire.Client{clientA, clientB} {
        if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
            t.Fatal(err)
        }
    }

    config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
    config.Content.Level = appwire.TranscriptLevelActivity
    var result appwire.TranscriptDisplayDefaultsPatchResponse
    if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsTranscriptDisplayPatch,
        appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, ExpectedRevision: 0, Config: config}, &result); err != nil {
        t.Fatal(err)
    }
    select {
    case notification := <-clientB.Notifications():
        if notification.Method != appwire.NotifyEvenerSettingsTranscriptDisplayChanged || result.Revision != 1 {
            t.Fatalf("result=%#v notification=%#v", result, notification)
        }
    case <-t.Context().Done():
        t.Fatal("notification was not delivered")
    }
}
```

- [ ] **Step 2: Run the focused hub tests and confirm methods are unregistered**

```bash
go test ./cmd/evener-hub -run 'TestHubRPC.*TranscriptDisplay|TestHubRPCRegistersExpectedHandlerSet' -count=1
```

Expected: FAIL with method-not-found or handler-set mismatch.

- [ ] **Step 3: Wire the store into `WebConfig` and startup**

Add `TranscriptDisplayStore *TranscriptDisplayStore` to `hubcore.WebConfig`. Production constructs it from `HubStateRoot` before `NewWebServer`; direct-test servers construct a store when none is injected. Retain a non-nil fallback store while surfacing load errors instead of silently discarding malformed state. In the same commit, set `FeatureSet.TranscriptDisplaySettings` true in the hub's `ServerConfig.Features`; older hubs and daemon servers keep the false zero value.

- [ ] **Step 4: Register strict GET and PATCH handlers**

Register GET with `HandleTyped`. Register PATCH with `Router.Handle` so Task 1's strict raw decoder runs before the store.

```go
func registerTranscriptDisplayHandlers(server *appserver.Server, store *hubcore.TranscriptDisplayStore) {
    appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsTranscriptDisplayGet,
        func(context.Context, appwire.EmptyParams) (appwire.TranscriptDisplayDefaultsResponse, error) {
            return store.Snapshot(), nil
        })
    server.Router().Handle(appwire.MethodEvenerSettingsTranscriptDisplayPatch,
        func(_ context.Context, raw json.RawMessage) (any, error) {
            params, err := appwire.DecodeTranscriptDisplayDefaultsPatchParams(raw)
            if err != nil { return nil, appwire.InvalidParams(err.Error()) }
            result, err := store.Patch(params)
            if err != nil { return nil, err }
            if result.Revision != params.ExpectedRevision {
                server.BroadcastAll(appwire.NotifyEvenerSettingsTranscriptDisplayChanged,
                    appwire.TranscriptDisplayDefaultsChangedParams(result))
            }
            return result, nil
        })
}
```

- [ ] **Step 5: Run hub RPC and restart tests**

```bash
go test ./cmd/evener-hub -run 'TestHubRPC.*TranscriptDisplay|TestHubRPCRegistersExpectedHandlerSet' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run hub package race tests for the new surface**

```bash
go test -race ./cmd/evener-hub/... -run 'Test.*TranscriptDisplay' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the RPC surface**

```bash
git add cmd/evener-hub/app_rpc_transcript_display.go cmd/evener-hub/app_rpc_transcript_display_test.go cmd/evener-hub/internal/hubcore/config.go cmd/evener-hub/main.go cmd/evener-hub/web.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_rpc_test.go
git commit -m "feat(hub): sync transcript display defaults"
```

---

### Task 4: Build the pure frontend configuration domain

**Files:**
- Create: `cmd/evener-hub/frontend/src/transcriptDisplay/config.ts`
- Create: `cmd/evener-hub/frontend/src/transcriptDisplay/config.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/prefs.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/prefs.test.ts`

**Interfaces:**
- Produces `TranscriptDisplayConfigV1`, preset helpers, strict local codec, wire conversion, precedence, summary, fingerprint, and legacy migration/dual-write helpers for every later frontend task.

- [ ] **Step 1: Add failing preset, codec, and migration tests**

Pin all five exact vectors, cumulative ordering, Custom normalization, stable fingerprint/property order, malformed-as-absent local decoding, wire round trips, local → hub → shipped precedence, shipped defaults, Advanced counts, visible/hidden inventory, migration truth table, and exact legacy `1`/`0` writes.

```ts
it("expands the five cumulative content presets", () => {
  expect(presetContent("chat")).toEqual({ toolIntent: false, toolCalls: false, reasoning: false, expandByDefault: false });
  expect(presetContent("intent")).toEqual({ toolIntent: true, toolCalls: false, reasoning: false, expandByDefault: false });
  expect(presetContent("tools")).toEqual({ toolIntent: true, toolCalls: true, reasoning: false, expandByDefault: false });
  expect(presetContent("activity")).toEqual({ toolIntent: true, toolCalls: true, reasoning: true, expandByDefault: false });
  expect(presetContent("full")).toEqual({ toolIntent: true, toolCalls: true, reasoning: true, expandByDefault: true });
});
```

- [ ] **Step 2: Run the pure tests and confirm the module is absent**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/transcriptDisplay/config.test.ts
```

Expected: module-not-found failure.

- [ ] **Step 3: Implement the domain unions and preset helpers**

Use the approved frontend shape:

```ts
export type ContentSelection =
  | { kind: "preset"; level: "chat" | "intent" | "tools" | "activity" | "full" }
  | { kind: "custom"; toolIntent: boolean; toolCalls: boolean; reasoning: boolean; expandByDefault: boolean };

export interface TranscriptDisplayConfigV1 {
  readonly version: 1;
  readonly content: ContentSelection;
  readonly advanced: Readonly<{
    roundTimings: boolean;
    tokenCounts: boolean;
    estimatedCost: boolean;
    systemEvents: boolean;
    promptEvents: boolean;
    hookExits: "none" | "successful" | "all";
  }>;
}

export interface HubTranscriptDisplayDefault {
  readonly revision: number;
  readonly config: TranscriptDisplayConfigV1;
}
```

Export `presetContent`, `normalizeContent`, `normalizeConfig`, `resolveEffectiveConfig`, `configFingerprint`, `configSummary`, `advancedEnabledCount`, and `visibleCategoryInventory`.

- [ ] **Step 4: Implement strict local and wire codecs**

Parse local JSON from `unknown`; require exact version, discriminators, enums, booleans, and complete Custom shape. Encode normalized objects in stable property order. Map the generated AppWire interfaces to/from the frontend union without trusting optional generated fields.

- [ ] **Step 5: Export guarded legacy adapters**

Keep existing key names and bool encoding unchanged. Export narrow helpers from `prefs.ts` for reading key presence/value and writing/removing exact `1`/`0` values; do not expose unrestricted localStorage access.

- [ ] **Step 6: Implement migration and dual-write helpers**

Migrate only when neither new per-layout key exists and at least one legacy key is present. Inspect the exact legacy keys `evener.prefs.transcriptRoundTimings`, `evener.prefs.transcriptTokenCounts`, `evener.prefs.transcriptHookExitsAll`, `evener.prefs.transcriptHookExitsNormal`, `evener.prefs.transcriptPromptLoaded`, and `evener.prefs.showCost`. Produce Activity plus `systemEvents: true`; use fallbacks timings on, tokens off, prompt events on, cost off, and hooks off. Map normal-only to `successful` and any all-on value to `all`. Write the same migration to Desktop and Mobile. Do not delete old keys.

- [ ] **Step 7: Run focused config and prefs tests**

```bash
npx vitest run src/transcriptDisplay/config.test.ts src/stores/prefs.test.ts
```

Expected: PASS.

- [ ] **Step 8: Commit the frontend domain**

```bash
git add cmd/evener-hub/frontend/src/transcriptDisplay/config.ts cmd/evener-hub/frontend/src/transcriptDisplay/config.test.ts cmd/evener-hub/frontend/src/stores/prefs.ts cmd/evener-hub/frontend/src/stores/prefs.test.ts
git commit -m "feat(web): model transcript display configuration"
```

---

### Task 5: Build the pure transcript projector

**Files:**
- Create: `cmd/evener-hub/frontend/src/transcriptDisplay/projector.ts`
- Create: `cmd/evener-hub/frontend/src/transcriptDisplay/projector.test.ts`
- Read/retire later: `cmd/evener-hub/frontend/src/panes/session/viewModes.ts`
- Read/retire later: `cmd/evener-hub/frontend/src/panes/session/transcript/transcriptVisibility.ts`

**Interfaces:**
- Consumes Task 4's normalized config/content vector.
- Produces `projectThread(model, config): TranscriptProjection` for `TranscriptBody`, scroll anchors, render context, and preview inventory.

- [ ] **Step 1: Add failing projector matrix tests**

Cover every current `ItemModel.type` and relevant `eventKind`, all five levels, representative Custom vectors, blank tool purposes, failed tools, non-zero hooks, asks, approvals, steering, warnings, active rows, interrupted/failed turns, prompts, raw timings, unknown future kinds, order, stable IDs, source indexes, input immutability, and filter-before-grouping.

```ts
it("uses an intent proxy until the real tool row supersedes it", () => {
  const model = threadWith(tool({ id: "tool-1", description: "Inspect the tree" }));
  expect(projectThread(model, preset("intent")).turns[0]?.entries).toEqual([
    expect.objectContaining({ kind: "intent", id: "intent:tool-1", rationale: "Inspect the tree" }),
  ]);
  expect(projectThread(model, preset("tools")).turns[0]?.entries).toEqual([
    expect.objectContaining({ kind: "item", id: "tool-1" }),
  ]);
});
```

- [ ] **Step 2: Run the projector tests and confirm the module is absent**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/transcriptDisplay/projector.test.ts
```

Expected: module-not-found failure.

- [ ] **Step 3: Define stable projected unions**

```ts
export type ProjectedEntry =
  | { kind: "item"; id: string; turnId: string; sourceIndex: number; item: ItemModel; isMessage: boolean }
  | { kind: "intent"; id: `intent:${string}`; turnId: string; sourceIndex: number; sourceItemId: string; rationale: string }
  | { kind: "critical"; id: string; turnId: string; sourceIndex: number; sourceItemId?: string; item: ItemModel; summary: string };

export interface ProjectedTurn {
  readonly id: string;
  readonly source: TurnModel;
  readonly entries: readonly ProjectedEntry[];
}

export interface ProjectedAnchor {
  readonly id: string;
  readonly sourceIndex: number;
  readonly index: number;
  readonly isMessage: boolean;
}

export interface TranscriptMetadataVisibility {
  readonly roundTimings: boolean;
  readonly tokenCounts: boolean;
  readonly estimatedCost: boolean;
  readonly systemEvents: boolean;
  readonly promptEvents: boolean;
  readonly hookExits: HookExitDetail;
}

export interface TranscriptProjection {
  readonly turns: readonly ProjectedTurn[];
  readonly anchors: readonly ProjectedAnchor[];
  readonly metadata: TranscriptMetadataVisibility;
  readonly eligibleDisclosureIds: readonly string[];
}
```

- [ ] **Step 4: Implement typed classification and critical invariants**

Classify with `item.type`, `eventKind`, structured exit/status fields, and turn status. Never parse English message text. Unknown kinds produce item entries. A blank tool description produces the fixed neutral summary contract.

- [ ] **Step 5: Implement projection, grouping input, and metadata**

Filter before building tool/system groups. Preserve each source turn and provide a projected item array for renderers that count neighboring items. Emit anchors with source indexes, not projected indexes. Return metadata flags directly from config.

- [ ] **Step 6: Run focused projector tests**

```bash
npx vitest run src/transcriptDisplay/projector.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit the projector**

```bash
git add cmd/evener-hub/frontend/src/transcriptDisplay/projector.ts cmd/evener-hub/frontend/src/transcriptDisplay/projector.test.ts
git commit -m "feat(web): project transcript detail levels"
```

---

### Task 6: Add browser-local and hub-default frontend state

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts`
- Create: `cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/connection.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/connection.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/ConnectionBanner.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/ConnectionBanner.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/useIsMobile.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/useIsMobile.test.ts`

**Interfaces:**
- Consumes Task 1's generated methods/notification/feature bit and Task 4's codec/migration.
- Produces `useEffectiveTranscriptDisplay`, local setters/reset, confirmed/draft hub setters, reconnect refresh, and unsupported/error state for UI tasks.

- [ ] **Step 1: Add failing store tests**

Cover shipped fallback, local-over-hub precedence, independent layouts, local restart, same-tab subscribers, BroadcastChannel, storage fallback, malformed channel/storage payloads, blocked/full storage warning, migration-once, exact dual-write, hub refresh, stale notification ignore, follower-only hub changes, local override stability, draft immediate preview, acknowledged commit, failed/conflict reversion, reconnect, replaced-client fencing, and older-hub unsupported state.

```ts
it("keeps a local Desktop view while a newer hub default updates followers", () => {
  transcriptDisplayStore.getState().setLocal("desktop", preset("activity"));
  transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 2, config: preset("chat") });
  expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(preset("activity"));
  expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(preset("chat"));
});
```

- [ ] **Step 2: Run the focused store tests and confirm the store is absent**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/stores/transcriptDisplay.test.ts src/stores/connection.test.ts src/shell/useIsMobile.test.ts
```

Expected: module-not-found or missing-feature-state failure.

- [ ] **Step 3: Retain handshake features in the connection store**

Extend connection state with `features?: FeatureSet`. Every handshake-driving caller sets `serverInfo` and `features` from one `InitializeResponse`. Clear stale features when replacing/closing a client. The transcript store remains `hubSupport: "unknown"` until a handshake completes, becomes `"supported"` only when `transcriptDisplaySettings === true`, and otherwise becomes `"unsupported"`. Tests must prove an older response with no field leaves the synced-default UI unsupported instead of issuing method-not-found requests.

- [ ] **Step 4: Implement the Zustand store and effective selector**

Use this stable state boundary:

```ts
interface TranscriptDisplayStoreState {
  viewport: ViewportClass;
  local: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
  hub: Partial<Record<ViewportClass, HubTranscriptDisplayDefault>>;
  drafts: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
  hubLoading: boolean;
  hubError: string | null;
  storageWarning: string | null;
  hubSupport: "unknown" | "supported" | "unsupported";
  setLocal(layout: ViewportClass, config: TranscriptDisplayConfigV1): void;
  clearLocal(layout: ViewportClass): void;
  effective(layout?: ViewportClass): TranscriptDisplayConfigV1;
  refreshHubDefaults(): Promise<void>;
  patchHubDefault(layout: ViewportClass, config: TranscriptDisplayConfigV1): Promise<void>;
}
```

- [ ] **Step 5: Implement per-layout persistence and same-origin sync**

Use keys `evener.prefs.transcriptDisplay.desktop` and `.mobile`, plus `BroadcastChannel("evener.transcript-display.v1")`. Include a random per-tab source id, encoded config or null, and fingerprint. Listen to storage events as fallback. The origin tab updates Zustand directly. Never place hub values on this channel.

- [ ] **Step 6: Implement legacy migration and dual-write**

Run migration before loading local values. Set a visible storage warning when a write fails but preserve the in-memory value. Document and test that the most recently edited layout wins the lossy legacy global Advanced flags.

- [ ] **Step 7: Implement hub refresh, patch, notifications, and reconnect fencing**

Rewire the active AppWire client like `threads.ts`: detach the old notification/ready handlers, fetch immediately when already ready, refresh on every future ready generation, ignore stale async responses from replaced clients, and apply only newer revisions. Keep a confirmed hub record separate from a draft. Parse conflict error data narrowly and restore the canonical response.

- [ ] **Step 8: Initialize the store before pane rendering**

Call `initTranscriptDisplay()` in `AppShell` beside `initPrefs()`. Subscribe once to the existing mobile query and update the store's layout class before host swap whenever possible.

- [ ] **Step 9: Run focused store/shell tests**

```bash
npx vitest run src/stores/transcriptDisplay.test.ts src/stores/connection.test.ts src/shell/useIsMobile.test.ts src/shell/AppShell.test.tsx src/shell/ConnectionBanner.test.tsx
```

Expected: PASS.

- [ ] **Step 10: Commit frontend state**

```bash
git add cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts cmd/evener-hub/frontend/src/stores/connection.ts cmd/evener-hub/frontend/src/stores/connection.test.ts cmd/evener-hub/frontend/src/shell/AppShell.tsx cmd/evener-hub/frontend/src/shell/AppShell.test.tsx cmd/evener-hub/frontend/src/shell/ConnectionBanner.tsx cmd/evener-hub/frontend/src/shell/ConnectionBanner.test.tsx cmd/evener-hub/frontend/src/shell/useIsMobile.ts cmd/evener-hub/frontend/src/shell/useIsMobile.test.ts
git commit -m "feat(web): resolve local and hub transcript views"
```

---

### Task 7: Make disclosure and render state configuration-aware

**Files:**
- Create: `cmd/evener-hub/frontend/src/transcriptDisplay/renderContext.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/disclosure/disclosureStore.ts`
- Modify: `cmd/evener-hub/frontend/src/widgets/disclosure/disclosureStore.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/types.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallCluster.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallCluster.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SystemNoticeItem.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SystemNoticeItem.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/TurnSeparator.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/TurnSeparator.test.tsx`

**Interfaces:**
- Consumes Task 4 configs and Task 5's metadata/eligible-disclosure outputs in focused tests.
- Produces configuration-aware renderer behavior that `TranscriptBody` can share without leaf `prefsStore` reads.
- Keeps `TurnBlock`'s existing `TurnModel` call shape working through one temporary legacy-config adapter; Task 8 removes that adapter when every production caller supplies the shared provider.

- [ ] **Step 1: Add failing disclosure-baseline tests**

Prove Activity defaults eligible entries closed, entering Full clears prior closed overrides and opens current eligible ids once, a later manual collapse wins, new eligible ids default open in Full, returning to Full creates a new baseline, and preview scopes never collide with live scopes.

- [ ] **Step 2: Run disclosure and renderer tests to confirm missing behavior**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/widgets/disclosure/disclosureStore.test.ts src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/ToolCallCluster.test.tsx src/panes/session/transcript/messages/ThinkBlock.test.tsx
```

Expected: failures showing local cluster state and always-collapsed defaults.

- [ ] **Step 3: Separate explicit choices from baseline defaults**

Extend the disclosure store with scoped baseline operations while preserving existing APIs:

```ts
export function beginDisclosureBaseline(scope: string, ids: readonly string[], open: boolean): void;
export function disclosureDefault(scope: string, id: string, fallback: boolean): boolean;
export function clearDisclosureScope(scope: string): void;
```

The baseline operation may clear explicit values only when entering Full, never on ordinary rerenders.

- [ ] **Step 4: Add render context**

Provide normalized config, projected metadata, surface (`live | readOnly | preview`), disclosure scope, and full-baseline generation. Existing memoized renderers must either consume context or include new props in their comparator; no renderer may remain stale after config changes. Give the context a test-only/transition default, and let `TurnBlock` construct one temporary compatibility value from the existing prefs when no provider is present. This keeps the current Session and read-only callers green until Task 8 moves both to `TranscriptBody`.

- [ ] **Step 5: Convert tool, reasoning, system, notification, and metadata renderers**

Remove direct `prefsStore` reads from leaf renderers and `TurnSeparator`. Convert `ToolCallCluster` and `NotificationCard` local disclosure state to scoped disclosure state. Preserve failed-tool auto-open as a fallback and preserve explicit user choices. Keep the temporary prefs-to-config adapter at the `TurnBlock` boundary only; no child renderer may read the old store.

- [ ] **Step 6: Keep critical failures under hidden routine diagnostics**

When hook diagnostics are `none`, render non-zero hooks through the compact critical projection. When full diagnostic rows are visible, avoid a duplicate critical row. Keep prompts/system events controlled only by Advanced fields.

- [ ] **Step 7: Run focused renderer tests**

```bash
npx vitest run src/widgets/disclosure/disclosureStore.test.ts src/panes/session/transcript/TurnBlock.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/ToolCallCluster.test.tsx src/panes/session/transcript/messages/ThinkBlock.test.tsx src/panes/session/transcript/messages/SystemNoticeItem.test.tsx src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/TurnSeparator.test.tsx
```

Expected: PASS.

- [ ] **Step 8: Commit renderer state changes**

```bash
git add cmd/evener-hub/frontend/src/transcriptDisplay/renderContext.tsx cmd/evener-hub/frontend/src/widgets/disclosure/disclosureStore.ts cmd/evener-hub/frontend/src/widgets/disclosure/disclosureStore.test.ts cmd/evener-hub/frontend/src/panes/session/transcript/types.ts cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallCluster.tsx cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallCluster.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/SystemNoticeItem.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/SystemNoticeItem.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/TurnSeparator.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/TurnSeparator.test.tsx
git commit -m "refactor(web): render transcript from display context"
```

---

### Task 8: Extract the shared projected TranscriptBody

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/session.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/transcript/Transcript.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/transcript/Transcript.test.tsx`
- Delete: `cmd/evener-hub/frontend/src/panes/session/viewModes.ts`
- Delete: `cmd/evener-hub/frontend/src/panes/session/viewModes.test.ts`

**Interfaces:**
- Consumes Task 5 projector, Task 6 effective store, and Task 7 render context.
- Produces one rendering path for Task 9 scroll coordination, Task 10 live controls, and Task 11 previews.

- [ ] **Step 1: Add failing shared-body parity tests**

Render one fabricated model through live, read-only, and preview surfaces. Assert equivalent projected content, no Intent/Tools purpose duplication, no preview virtual scroller, no read-only composer, and preserved `job:` dispatch.

```tsx
it("renders Intent through one body without raw tool rows", () => {
  render(<TranscriptBody model={fixture} config={preset("intent")} surface="preview" disclosureScope="preview:test" />);
  expect(screen.getByText("Inspect the tree")).toBeTruthy();
  expect(screen.queryByTestId("tool-call-item")).toBeNull();
});
```

- [ ] **Step 2: Run body/session/transcript tests and confirm missing component**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/session/transcript/TranscriptBody.test.tsx src/panes/session/Session.test.tsx src/panes/transcript/Transcript.test.tsx
```

Expected: module-not-found failure.

- [ ] **Step 3: Implement `TranscriptBody`**

Use props that keep common rendering central and surface-specific chrome injectable:

```ts
export interface TranscriptBodyProps {
  model: ThreadModel;
  config: TranscriptDisplayConfigV1;
  surface: "live" | "readOnly" | "preview";
  disclosureScope: string;
  sessionRef?: string;
  showSeenDividerTurnId?: string;
  loadOlderRow?: ReactNode;
  liveOverlay?: ReactNode;
  listRef?: RefObject<VirtualListHandle | null>;
  onMeasurementsChange?: () => void;
}
```

Live/read-only use the common projected VirtualList; preview maps projected turns in normal page flow. Import renderer-registration barrels at this boundary. Update `TurnBlock` to accept the projected turn/entries and remove Task 7's temporary prefs-to-config adapter now that every production caller supplies `TranscriptRenderProvider`.

- [ ] **Step 4: Replace Session's two render trees**

Remove `viewMode`, `focusedEntries`, `viewRows`, the focused branch, and the header RadioGroup. Keep cold start, deleted state, flow overlay, seen divider, composer, liveness, pending chips, and escalation rail unchanged. Resolve the effective config from Task 6 and pass its fingerprint to scroll flow.

- [ ] **Step 5: Replace read-only Transcript duplication**

Keep `job:` dispatch, hydration, pane title, no-composer behavior, and older loading. Render thread content through `TranscriptBody surface="readOnly"`.

- [ ] **Step 6: Retire old view modes**

Delete `viewModes.ts` and its tests after every retained assertion exists in projector/body tests. Search for and remove all imports and CSS selectors used only by the old focused tree.

- [ ] **Step 7: Run shared-body integration tests**

```bash
npx vitest run src/transcriptDisplay/projector.test.ts src/panes/session/transcript/TranscriptBody.test.tsx src/panes/session/Session.test.tsx src/panes/transcript/Transcript.test.tsx
```

Expected: PASS.

- [ ] **Step 8: Commit the shared body**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.test.tsx cmd/evener-hub/frontend/src/panes/session/Session.tsx cmd/evener-hub/frontend/src/panes/session/Session.test.tsx cmd/evener-hub/frontend/src/panes/session/session.module.css cmd/evener-hub/frontend/src/panes/transcript/Transcript.tsx cmd/evener-hub/frontend/src/panes/transcript/Transcript.test.tsx
git rm cmd/evener-hub/frontend/src/panes/session/viewModes.ts cmd/evener-hub/frontend/src/panes/session/viewModes.test.ts
git commit -m "refactor(web): share transcript projection body"
```

---

### Task 9: Preserve scroll and focus for every configuration source

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/flow/transcriptViewRegistry.ts`
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/flow/transcriptViewRegistry.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx`
- Modify: `cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts`

**Interfaces:**
- Produces registry registration and `transitionTranscriptViews(change)` used before local, tab, hub-follower, and layout changes.
- Extends scroll flow to restore focus or focus the Detail trigger when the focused entry disappears.

- [ ] **Step 1: Add failing multi-pane transition tests**

Cover two mounted panes, capture-before-store-publish ordering, same-entry restore, nearest surviving message, normalized fallback, bottom follow, focused-entry survival, focused-entry removal, unregister, and breakpoint host-remount cache.

- [ ] **Step 2: Run registry/scroll tests and confirm external transitions are unsupported**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/session/transcript/flow/transcriptViewRegistry.test.ts src/panes/session/transcript/flow/useTranscriptScroll.test.ts
```

Expected: failures showing only click-local capture exists.

- [ ] **Step 3: Implement the registry and transition order**

```ts
export interface CapturedTranscriptView {
  readonly anchorId?: string;
  readonly anchorOffset: number;
  readonly normalizedOffset: number;
  readonly followingBottom: boolean;
  readonly focusedEntryId?: string;
}

export interface RegisteredTranscriptView {
  id: string;
  capture(): CapturedTranscriptView;
  restore(captured: CapturedTranscriptView): void;
  focusDetailTrigger(): void;
  announce(summary: string): void;
}

export function registerTranscriptView(view: RegisteredTranscriptView): () => void;
export function captureTranscriptViews(): ReadonlyMap<string, CapturedTranscriptView>;
export function restoreTranscriptViews(captured: ReadonlyMap<string, CapturedTranscriptView>): void;
export function transitionTranscriptViews(publish: () => void, summary: string): void;
```

Every effective-change path captures all mounted views, publishes the state, then schedules restore after measurement. Coalesce concurrent identical fingerprints; do not capture/announce on unrelated hub-default changes hidden by a local override.

- [ ] **Step 4: Extend scroll anchors with focus and remount state**

Use stable projected entry IDs and source indexes. Preserve the current exact/nearest/proportion algorithm. Cache the captured state briefly across the desktop/mobile host remount and consume it once; use no wall-clock sleeps.

- [ ] **Step 5: Connect all store transition sources**

Route local set/reset, BroadcastChannel, storage, applicable hub notification, and viewport change through one transition function. Hub drafts in Settings preview do not change live followers until acknowledged.

- [ ] **Step 6: Run focused flow/store tests**

```bash
npx vitest run src/panes/session/transcript/flow/transcriptViewRegistry.test.ts src/panes/session/transcript/flow/useTranscriptScroll.test.ts src/stores/transcriptDisplay.test.ts src/panes/session/transcript/TranscriptBody.test.tsx
```

Expected: PASS.

- [ ] **Step 7: Commit view preservation**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript/flow/transcriptViewRegistry.ts cmd/evener-hub/frontend/src/panes/session/transcript/flow/transcriptViewRegistry.test.ts cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.test.ts cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts
git commit -m "fix(web): preserve transcript view across detail changes"
```

---

### Task 10: Add the compact live Detail control

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.test.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css`
- Modify: `cmd/evener-hub/frontend/src/widgets/radiogroup/index.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/radiogroup/radiogroup.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/transcript/Transcript.tsx`

**Interfaces:**
- Consumes Task 6 local/hub store and Task 4 config helpers.
- Produces controlled `TranscriptDetailEditor` for Task 11 and the desktop Popover/mobile Sheet `TranscriptDetailControl` live wrapper.
- Produces the Detail-trigger focus target used by Task 9.

```ts
export interface TranscriptDetailEditorProps {
  value: TranscriptDisplayConfigV1;
  onChange(value: TranscriptDisplayConfigV1): void;
  disabled?: boolean;
  compact?: boolean;
}

export interface TranscriptDetailControlProps {
  layout: ViewportClass;
  onEditHubDefaults(): void;
  triggerRef?: Ref<HTMLButtonElement>;
}
```

- [ ] **Step 1: Add failing accessibility and scope tests**

Cover five visible labels, `Full` visible with `Full detail` accessible name, Arrow/Home/End behavior, Custom with no false preset, Advanced fieldsets, `Tools · 2 advanced`, local Desktop/Mobile scope, Use hub default, Edit hub defaults, explicit unsupported copy for older hubs, Desktop Popover, Mobile bottom Sheet, Escape/Close/focus return, and 44px control classes.

- [ ] **Step 2: Run control/radio tests and confirm the control is absent**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/session/transcript/TranscriptDetailControl.test.tsx src/widgets/radiogroup/radiogroup.test.tsx
```

Expected: module-not-found or missing accessible-label support.

- [ ] **Step 3: Add visible/accessibility label support to RadioGroup**

Extend `RadioGroupOption` with `accessibleLabel?: string`. Render the visible label unchanged and apply the accessible label to the radio option. Preserve existing roving focus and disabled behavior.

- [ ] **Step 4: Implement the shared editor**

The stepped track changes regular content only and preserves metrics/diagnostics. Advanced Content changes normalize to a preset or Custom. Advanced Metrics/Diagnostics remain independent. Critical rows render as locked-on explanatory fields, not writable values.

- [ ] **Step 5: Implement Popover and Sheet wrappers**

Use the existing `Popover` at Desktop and `Sheet side="bottom"` at Mobile. The trigger sits in transcript-local toolbar chrome above the scroller, not `PaneScaffold.actions`. Register its ref with Task 9 for focus fallback.

- [ ] **Step 6: Add responsive and reduced-motion CSS**

Use a container query for compact narrow desktop panes. Keep all stop labels visible, minimum 44px Mobile targets, non-color selected state, no horizontal scrolling, and reduced-motion rules.

- [ ] **Step 7: Run focused control/body/session tests**

```bash
npx vitest run src/panes/session/transcript/TranscriptDetailControl.test.tsx src/widgets/radiogroup/radiogroup.test.tsx src/panes/session/transcript/TranscriptBody.test.tsx src/panes/session/Session.test.tsx src/panes/transcript/Transcript.test.tsx
```

Expected: PASS.

- [ ] **Step 8: Commit the live control**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css cmd/evener-hub/frontend/src/widgets/radiogroup/index.tsx cmd/evener-hub/frontend/src/widgets/radiogroup/radiogroup.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptBody.tsx cmd/evener-hub/frontend/src/panes/session/Session.tsx cmd/evener-hub/frontend/src/panes/transcript/Transcript.tsx
git commit -m "feat(web): add live transcript detail control"
```

---

### Task 11: Build stacked hub-default cards and production previews

**Files:**
- Create: `cmd/evener-hub/frontend/src/transcriptDisplay/previewFixture.ts`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.test.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/transcriptDisplayCard.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/display.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/display.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections.test.ts`
- Modify: `cmd/evener-hub/frontend/src/dev/surface-sections/transcript.tsx`
- Modify: `cmd/evener-hub/frontend/src/dev/surface-sections/transcript.test.tsx`

**Interfaces:**
- Consumes Task 6 drafts/confirmed hub state, Task 10 `TranscriptDetailEditor`, and Task 8 preview body.
- Produces the approved Settings UI and deterministic shared fixture.

```ts
export interface TranscriptDisplayCardProps {
  layout: ViewportClass;
  confirmed: HubTranscriptDisplayDefault;
  draft?: TranscriptDisplayConfigV1;
  localOverride?: TranscriptDisplayConfigV1;
  saveState: "idle" | "saving" | "error";
  error?: string;
  onChange(config: TranscriptDisplayConfigV1): void;
  onRetry(): void;
}
```

- [ ] **Step 1: Add failing Settings and preview tests**

Cover two stacked cards in Desktop-then-Mobile order, controls above examples, shared fixed scenario, `Example only—not your data`, visible/hidden inventory, no `threadsStore` or request on preview render, draft update before RPC completion, acknowledged save, failed/conflict reversion, retry, unsupported older-hub copy, local-override explanation, one Advanced disclosure row, and no success toast before confirmation.

- [ ] **Step 2: Run Settings/display tests and confirm the old switch UI remains**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcript.test.tsx src/panes/settings/sections/display.test.tsx src/panes/settings/sections.test.ts
```

Expected: failures because cards/fixture do not exist and estimated cost remains under Display.

- [ ] **Step 3: Extract the deterministic preview fixture**

Build one `ThreadModel` with user/agent messages, purpose-bearing successful tool and body, failed critical tool, reasoning, timing/token/cost, low-level system, prompt, and hook items. Use fixed timestamps only. Update the dev transcript surface to consume this fixture and production `TranscriptBody surface="preview"`.

- [ ] **Step 4: Implement one default card**

Each card renders device/current value, five-stop editor, one Advanced opener/summary, example below controls, textual inventory, confirmed/draft/error state, retry, and local-override explanation. Use an isolated preview disclosure scope.

- [ ] **Step 5: Replace the Transcript settings section**

Render Desktop then Mobile cards. Keep route id `transcript`; change visible label/heading to `Transcript display`. Explain hub sync and browser-local live overrides.

- [ ] **Step 6: Move estimated cost and preserve compatibility**

Remove only the estimated-cost row from Display; retain Enter-sends behavior. Advanced Metrics now owns cost. Keep legacy `showCost` adapters and dual-write tests.

- [ ] **Step 7: Run Settings, preview, and dev-surface tests**

```bash
npx vitest run src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcript.test.tsx src/panes/settings/sections/display.test.tsx src/panes/settings/sections.test.ts src/panes/session/transcript/TranscriptBody.test.tsx src/dev/surface-sections/transcript.test.tsx
```

Expected: PASS.

- [ ] **Step 8: Commit Settings and preview UI**

```bash
git add cmd/evener-hub/frontend/src/transcriptDisplay/previewFixture.ts cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.tsx cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections/transcriptDisplayCard.module.css cmd/evener-hub/frontend/src/panes/settings/sections/transcript.tsx cmd/evener-hub/frontend/src/panes/settings/sections/transcript.module.css cmd/evener-hub/frontend/src/panes/settings/sections/transcript.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections/display.tsx cmd/evener-hub/frontend/src/panes/settings/sections/display.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections.ts cmd/evener-hub/frontend/src/panes/settings/sections.test.ts cmd/evener-hub/frontend/src/dev/surface-sections/transcript.tsx cmd/evener-hub/frontend/src/dev/surface-sections/transcript.test.tsx
git commit -m "feat(web): preview synced transcript defaults"
```

---

### Task 12: Verify integration, responsive geometry, and compatibility

**Files:**
- Modify: `cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs`
- Modify: `docs/superpowers/plans/2026-08-25-transcript-detail-configuration.md` (check completed boxes only)

**Interfaces:**
- Consumes the complete feature.
- Produces verified repository state and gate evidence. No new product behavior belongs in this task except root-cause fixes for discovered failures.

- [ ] **Step 1: Format all touched frontend source files**

Run from `cmd/evener-hub/frontend` with the exact changed `src/` paths reported by `git diff --name-only`:

```bash
npx biome check --write src/transcriptDisplay src/stores/transcriptDisplay.ts src/panes/session src/panes/transcript src/panes/settings src/widgets/radiogroup src/widgets/disclosure src/shell src/dev/surface-sections/transcript.tsx
```

Expected: exit 0. Review every formatter change.

- [ ] **Step 2: Run the complete focused Go surface**

```bash
go test ./appwire ./internal/appwiredoc ./internal/appwirets ./cmd/evener-hub/internal/hubcore ./cmd/evener-hub -run 'Test.*TranscriptDisplay|TestGeneratedFileCurrent|TestHubRPCRegistersExpectedHandlerSet' -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the complete focused frontend surface**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/transcriptDisplay src/stores/transcriptDisplay.test.ts src/panes/session/transcript src/panes/session/Session.test.tsx src/panes/transcript/Transcript.test.tsx src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcript.test.tsx src/panes/settings/sections/display.test.tsx src/widgets/radiogroup/radiogroup.test.tsx src/widgets/disclosure/disclosureStore.test.ts src/shell/useIsMobile.test.ts src/shell/AppShell.test.tsx
npm run typecheck
npm run lint
```

Expected: PASS.

- [ ] **Step 4: Add/adjust a real-browser overflow case and mutation-test it**

Exercise 390px Mobile, 899px Mobile, 900px narrow desktop host, and a narrow dock pane. Assert the Detail trigger is reachable, the stepped control/sheet has no horizontal scroll, Settings cards stack, previews create no inner scroll, and 44px Mobile targets hold.

Before accepting the guard, make one path-scoped temporary mutation that removes the relevant width/overflow rule, prove the guard fails on the named element, restore only that mutation with the documented path-scoped stash procedure, and rerun green.

- [ ] **Step 5: Run canonical frontend gates**

From repository root:

```bash
make test-web
make test-web-browser
```

Expected: both exit 0. A missing Chrome/dependency is an incomplete gate, not a pass.

- [ ] **Step 6: Run repository lint and static analysis**

```bash
make lint
make vet
```

Expected: both exit 0. `make lint` must confirm generated outputs are current.

- [ ] **Step 7: Run the full deterministic test gate**

```bash
make test
```

Expected: exit 0. Read every warning, skip, and retained-log path.

- [ ] **Step 8: Run final status and diff checks**

```bash
git diff --check
git status --short
git log --oneline --decorate -15
```

Expected: no unstaged implementation changes, no unexpected untracked files, and one reviewed commit per accepted task.

- [ ] **Step 9: Commit only root-cause fixes or browser-guard changes from verification**

Stage the exact changed guard/test/fix paths and commit:

```bash
git commit -m "test(web): verify transcript detail layouts"
```

Skip this commit when verification required no file changes.
