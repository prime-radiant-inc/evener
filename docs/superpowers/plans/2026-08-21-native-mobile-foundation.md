# Native Mobile Foundation and Shell Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a dedicated Tauri iOS app that securely pairs with one Hub, owns native HTTP/AppWire transport, and renders a tested phone-only onboarding, navigation shell, session roster, new-session shell, and settings shell.

**Architecture:** A new `mobile/` React/Vite renderer uses Tauri's bundled asset origin and imports only an exact allowlist of headless web protocol files. Rust owns the Hub capability, private-network policy, HTTP, and one AppWire socket; a versioned native plugin owns iOS Keychain, QR, haptics, lifecycle, and content-size events.

**Tech Stack:** React 19.2.7, TypeScript 6.0.3, Vite 8.1.5, Vitest 4.1.10, Zustand 5.0.14, Biome 2.5.5, Tauri CLI 2.11.4/API 2.11.1, Rust 1.98.0, Tauri 2.11.5, Tokio 1.53.1, Reqwest 0.13.4, tokio-tungstenite 0.30.0, Swift/XCTest, Go 1.26 workspace.

**Spec:** `docs/superpowers/specs/2026-08-21-native-mobile-app-design.md`

## Global Constraints

- Work only on branch/worktree `native-mobile-v1`; stage named paths, never `git add .` or `git add -A`.
- Read `docs/developing-evener/testing.md` before changing tests; default tests use only local scripted boundaries and deterministic clocks.
- The mobile renderer must not import Hub web presentation code. Its only imports under `cmd/evener-hub/frontend/src` come from the exact allowlist in `mobile/protocol-imports.json`.
- The Hub capability stays in iOS Keychain and native memory; JavaScript never receives a saved capability.
- Release HTTP permits only the private address ranges defined in the spec. HTTPS may be public or private. Redirects remain disabled.
- Mobile API version is `1`; AppWire remains `evener-appwire-v3`.
- Minimum deployment target is iOS 17.0. Required Info.plist usage strings are camera, microphone, speech recognition, and local network.
- Every interactive control has a measured 44×44 CSS-pixel minimum target.
- Run `npx biome check --write` on touched `mobile/src` files before frontend gates.

## File Structure

### New mobile application

- `mobile/package.json` — pinned scripts and JavaScript dependencies.
- `mobile/package-lock.json` — exact npm graph.
- `mobile/tsconfig.json`, `mobile/vite.config.ts`, `mobile/biome.jsonc`, `mobile/index.html` — compiler, build, lint, and HTML entry.
- `mobile/src/main.tsx`, `mobile/src/App.tsx` — dedicated entry and root composition.
- `mobile/src/ui/` — mobile-only tokens and primitives.
- `mobile/src/navigation/` — React history stack and bottom navigation.
- `mobile/src/native/contract.ts`, `mobile/src/native/client.ts`, `mobile/src/native/fake.ts` — versioned bridge and real/fake adapters.
- `mobile/src/services/` — typed connection, roster, spawn, and AppWire interfaces.
- `mobile/src/state/` — connection, roster, and preference stores.
- `mobile/src/screens/` — onboarding, sessions, new-session, and settings screens.
- `mobile/src/dev/FixtureApp.tsx` — deterministic browser fixture mode.
- `mobile/scripts/check-boundary.mjs` — resolved module-graph assertion.
- `mobile/protocol-imports.json` — exact shared-protocol allowlist.

### Tauri and Rust

- `mobile/rust-toolchain.toml` — Rust 1.98.0 with rustfmt and clippy.
- `mobile/src-tauri/Cargo.toml`, `build.rs`, `tauri.conf.json`, `capabilities/default.json` — Tauri app.
- `mobile/src-tauri/src/lib.rs`, `main.rs` — application setup.
- `mobile/src-tauri/src/pairing.rs`, `network_policy.rs`, `profile.rs` — URL, address, and secret boundaries.
- `mobile/src-tauri/src/http_transport.rs`, `appwire_transport.rs`, `commands.rs`, `diagnostics.rs` — native Hub transport and bounded metadata diagnostics.
- `mobile/tauri-plugin-evener-native/` — versioned Rust/Swift native plugin generated with Tauri CLI.

### Hub

- `hubapi/types.go` — `MobileAPIVersion` and health/pairing DTOs.
- `cmd/evener-hub/config.go` — optional `mobile_base_url`.
- `cmd/evener-hub/web_api.go`, `web.go` — health version and authenticated pairing endpoint.
- `cmd/evener-hub/frontend/src/panes/settings/sections/mobile.tsx` — web-only QR card.
- Focused Go/frontend tests beside each modified file.

---

### Task 1: Scaffold the Dedicated Renderer and Tauri App

**Files:**
- Create all configuration and entry files listed above under `mobile/` and `mobile/src-tauri/`.
- Test: `mobile/src/App.test.tsx`

**Interfaces:**
- Produces: `App(): JSX.Element`, npm scripts `check`, `test`, `build`, `boundary`, `tauri`, and a Tauri binary named `evener-mobile`.

- [ ] **Step 1: Install the pinned Rust toolchain outside the repository**

Run:

```bash
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain 1.98.0
source "$HOME/.cargo/env"
rustup component add rustfmt clippy
rustc --version
```

Expected: `rustc 1.98.0`; no repository file changed.

- [ ] **Step 2: Write the failing root-render test**

Create `mobile/src/App.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "./App";

describe("App", () => {
  it("renders the dedicated mobile fixture shell", () => {
    render(<App fixture />);
    expect(screen.getByRole("navigation", { name: "Primary" })).toBeVisible();
    expect(screen.queryByText("Dockview")).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 3: Create the pinned package and test configuration**

Use exact dependencies from the header. Scripts:

```json
{
  "check": "biome ci src && tsc --noEmit --incremental false",
  "test": "vitest run --maxWorkers=4",
  "build": "tsc --noEmit --incremental false && vite build",
  "boundary": "node scripts/check-boundary.mjs",
  "tauri": "tauri"
}
```

Install with `npm --prefix mobile install`; commit `package-lock.json`.

- [ ] **Step 4: Run the test and verify failure**

Run: `npm --prefix mobile test -- --run src/App.test.tsx`

Expected: FAIL because `App.tsx` does not exist.

- [ ] **Step 5: Implement the smallest dedicated shell**

Create `App.tsx` with a `main` and a three-button `nav` labelled Sessions, New, Settings. Create `main.tsx` and `ui/global.css` with system font, safe-area variables, semantic light/dark tokens, `100dvh`, and 44-pixel targets. Do not import any Hub frontend entry or stylesheet.

- [ ] **Step 6: Initialize Tauri and iOS**

Create `rust-toolchain.toml`:

```toml
[toolchain]
channel = "1.98.0"
components = ["rustfmt", "clippy"]
profile = "minimal"
```

Initialize the app non-interactively with `productName: Evener`, identifier `com.primeradiant.evener`, `frontendDist: ../dist`, and iOS minimum `17.0`. Run:

```bash
cd mobile
npx tauri ios init --ci
```

Expected: generated iOS project exits zero.

- [ ] **Step 7: Run focused gates**

Run:

```bash
npm --prefix mobile test -- --run src/App.test.tsx
npm --prefix mobile run check
npm --prefix mobile run build
cargo test --manifest-path mobile/src-tauri/Cargo.toml
git diff --check
```

Expected: all exit zero.

- [ ] **Step 8: Commit named scaffold files**

```bash
git add mobile/package.json mobile/package-lock.json mobile/tsconfig.json mobile/vite.config.ts mobile/biome.jsonc mobile/index.html mobile/rust-toolchain.toml mobile/src mobile/src-tauri
git commit -m "feat(mobile): scaffold dedicated Tauri client"
```

---

### Task 2: Enforce the No-Web-Renderer Boundary

**Files:**
- Create: `mobile/protocol-imports.json`
- Create: `mobile/scripts/check-boundary.mjs`
- Create: `mobile/scripts/check-boundary.test.mjs`
- Modify: `mobile/vite.config.ts`, `mobile/package.json`

**Interfaces:**
- Produces: `assertAllowedModule(id: string, root: string, allowlist: Set<string>): void`; `npm run boundary`.

- [ ] **Step 1: Write failing boundary tests**

Test three paths: allowed `protocol/types.gen.ts`, forbidden `shell/AppShell.tsx`, and forbidden dynamic CSS under `panes/`. The forbidden cases must throw the resolved path.

- [ ] **Step 2: Verify failure**

Run: `node --test mobile/scripts/check-boundary.test.mjs`

Expected: FAIL because `assertAllowedModule` is absent.

- [ ] **Step 3: Implement recursive resolved-graph enforcement**

Set the initial allowlist to:

```json
[
  "cmd/evener-hub/frontend/src/protocol/client.ts",
  "cmd/evener-hub/frontend/src/protocol/errors.ts",
  "cmd/evener-hub/frontend/src/protocol/model.ts",
  "cmd/evener-hub/frontend/src/protocol/reducer.ts",
  "cmd/evener-hub/frontend/src/protocol/transport.ts",
  "cmd/evener-hub/frontend/src/protocol/types.gen.ts"
]
```

Add a Vite plugin that checks every `this.getModuleIds()` entry at `buildEnd`. Reject every file below the frontend source root unless its repository-relative path is in the allowlist. Include CSS, worker, and dynamic modules.

- [ ] **Step 4: Make the production build emit and audit inputs**

Have `npm run boundary` invoke Vite build with the guard enabled and assert that all renderer entry modules resolve below `mobile/src`.

- [ ] **Step 5: Run tests and build**

```bash
node --test mobile/scripts/check-boundary.test.mjs
npm --prefix mobile run boundary
```

Expected: PASS; temporarily importing `AppShell.tsx` makes `boundary` fail with its path, then remove that deliberate edit.

- [ ] **Step 6: Commit**

```bash
git add mobile/protocol-imports.json mobile/scripts mobile/vite.config.ts mobile/package.json
 git commit -m "test(mobile): enforce renderer isolation"
```

---

### Task 3: Add the Hub Mobile Contract and Pairing QR

**Files:**
- Modify: `hubapi/types.go`, `hubapi/types_test.go`
- Modify: `cmd/evener-hub/config.go`, `config_test.go`
- Modify: `cmd/evener-hub/web_api.go`, `web.go`
- Test: `cmd/evener-hub/web_mobile_test.go`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/mobile.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections.ts`
- Test: `cmd/evener-hub/frontend/src/panes/settings/sections/mobile.test.tsx`

**Interfaces:**
- Produces: `const MobileAPIVersion = 1`; health JSON `mobile_api_version`; authenticated `GET /api/mobile/pairing` returning `{auth_url:string}` with `Cache-Control: no-store`.

- [ ] **Step 1: Write failing Go contract tests**

Assert health raw JSON contains `"mobile_api_version":1`. Assert `/api/mobile/pairing` rejects unauthenticated requests, rejects loopback when no `mobile_base_url`, uses a valid configured base, appends `/auth?token=`, and sets `no-store`.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./cmd/evener-hub ./hubapi -run 'Test.*Mobile|TestWeb_APIHealth' -count=1`

Expected: FAIL on missing fields/route.

- [ ] **Step 3: Implement version/config/endpoint**

Add `MobileBaseURL string \`toml:"mobile_base_url"\``. Validate an origin-only URL at config load. Build pairing URLs with `hubedge.AuthURLFor`; never log or cache the response. Register the route behind the existing auth guard.

- [ ] **Step 4: Write the failing web settings test**

Mock `/api/mobile/pairing` and assert the Mobile app card renders an image/canvas alternative, copy button, and private-network warning; a 409 no-reachable-origin response renders configuration guidance without a QR.

- [ ] **Step 5: Implement the web-only QR card**

Use a pinned QR renderer local to the Hub frontend and register the section in `sections.ts`. The card is for pairing another device; it is not imported by `mobile/`.

- [ ] **Step 6: Run focused gates**

```bash
go test ./cmd/evener-hub ./hubapi -run 'Test.*Mobile|TestWeb_APIHealth' -count=1
npm --prefix cmd/evener-hub/frontend test -- --run src/panes/settings/sections/mobile.test.tsx
```

Expected: PASS.

- [ ] **Step 7: Commit named paths**

```bash
git add hubapi/types.go hubapi/types_test.go cmd/evener-hub/config.go cmd/evener-hub/config_test.go cmd/evener-hub/web_api.go cmd/evener-hub/web.go cmd/evener-hub/web_mobile_test.go cmd/evener-hub/frontend/package.json cmd/evener-hub/frontend/package-lock.json cmd/evener-hub/frontend/src/panes/settings/sections.ts cmd/evener-hub/frontend/src/panes/settings/sections/mobile.tsx cmd/evener-hub/frontend/src/panes/settings/sections/mobile.test.tsx
git commit -m "feat(hub): expose mobile pairing contract"
```

---

### Task 4: Define the Versioned Native Bridge and iOS Secure Store

**Files:**
- Create: `mobile/src/native/contract.ts`, `contract.test.ts`, `client.ts`, `fake.ts`
- Create: `mobile/tauri-plugin-evener-native/` using the Tauri plugin generator
- Modify: `mobile/src-tauri/Cargo.toml`, `src/lib.rs`
- Test: Rust and Swift contract fixtures in the plugin

**Interfaces:**
- Produces: `NATIVE_BRIDGE_VERSION = 1`; `NativeBridge` with `secureGet`, `secureSet`, `secureDelete`, `scanPairingCode`, `haptic`, `getContentSize`, `onLifecycle`, and voice methods reserved for the voice plan.

- [ ] **Step 1: Write the failing TypeScript contract test**

Define fixture JSON for every V1 command/response/event discriminator and assert `decodeNativeEvent` rejects an unknown version or discriminator.

- [ ] **Step 2: Verify failure**

Run: `npm --prefix mobile test -- --run src/native/contract.test.ts`

Expected: FAIL because the decoder is absent.

- [ ] **Step 3: Implement the TypeScript contract and fake**

Use discriminated unions; return redacted `NativeError { id, kind, message }`. Do not include token fields in any response type.

- [ ] **Step 4: Generate the plugin**

From repository root:

```bash
npx --yes @tauri-apps/cli@2.11.4 plugin new evener-native --ios --no-example --directory mobile/tauri-plugin-evener-native
```

Wire the local plugin crate into `src-tauri`.

- [ ] **Step 5: Write failing Rust/Swift fixture tests**

The Rust and Swift tests decode the same checked-in `contract-v1.json` and assert every variant and bridge version.

- [ ] **Step 6: Implement iOS Keychain and QR primitives**

Store the capability under service `com.primeradiant.evener.hub`, account `active`, with `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`. `scanPairingCode` uses `AVCaptureSession` and returns the scanned text to Rust plugin code, which immediately passes it into pairing; JavaScript receives no scanned URL.

- [ ] **Step 7: Run contract and native tests**

```bash
npm --prefix mobile test -- --run src/native/contract.test.ts
cargo test --manifest-path mobile/tauri-plugin-evener-native/Cargo.toml
xcodebuild test -scheme EvenerNativePlugin -destination 'platform=iOS Simulator,name=iPhone 16 Pro'
```

Expected: all available tests pass; if the named simulator differs, select an installed iOS 17+ simulator and record it.

- [ ] **Step 8: Commit**

```bash
git add mobile/src/native mobile/tauri-plugin-evener-native mobile/src-tauri/Cargo.toml mobile/src-tauri/src/lib.rs
git commit -m "feat(mobile): add native bridge and secure pairing"
```

---

### Task 5: Implement Pairing URL and Private-Network Policy

**Files:**
- Create: `mobile/src-tauri/src/pairing.rs`, `network_policy.rs`, `profile.rs`, `error.rs`
- Test: sibling Rust unit tests

**Interfaces:**
- Produces: `PairingUrl::parse(&str) -> Result<PairingUrl>`; `NetworkPolicy::resolve(&Url, ReleaseMode) -> Result<PinnedOrigin>`; `ProfileStore` trait; redacted `ProfileSummary`.

- [ ] **Step 1: Write failing table tests**

Cover exact `/auth`, one 43-character base64url token, optional `next`, forbidden userinfo/fragments/query keys, noncanonical ports, HTTPS public host, HTTP RFC1918/CGNAT/ULA, mixed DNS answers, release loopback, debug loopback, and redirect refusal.

- [ ] **Step 2: Verify failure**

Run: `cargo test --manifest-path mobile/src-tauri/Cargo.toml pairing network_policy`

Expected: FAIL because modules are absent.

- [ ] **Step 3: Implement parsers with injected resolver**

Keep the original hostname for Host/TLS identity and a validated address set for connection. Resolve anew on every new HTTP/WebSocket connection. `Display` and errors expose only normalized origin, never query/token.

- [ ] **Step 4: Implement profile lifecycle**

Preferences store origin/display metadata. Keychain adapter stores/deletes token. Pairing probes health, checks mobile API 1, displays normalized origin for confirmation, then performs authenticated probe with redirects disabled before replacing the active profile.

- [ ] **Step 5: Run tests and Clippy**

```bash
cargo test --manifest-path mobile/src-tauri/Cargo.toml pairing network_policy profile
cargo clippy --manifest-path mobile/src-tauri/Cargo.toml --all-targets -- -D warnings
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add mobile/src-tauri/src/pairing.rs mobile/src-tauri/src/network_policy.rs mobile/src-tauri/src/profile.rs mobile/src-tauri/src/error.rs mobile/src-tauri/Cargo.toml mobile/src-tauri/Cargo.lock
git commit -m "feat(mobile): secure Hub pairing policy"
```

---

### Task 6: Implement Native HTTP and Shared AppWire Transport

**Files:**
- Create: `mobile/src-tauri/src/http_transport.rs`, `appwire_transport.rs`, `commands.rs`, `diagnostics.rs`
- Create: `mobile/src/services/nativeHttp.ts`, `mobile/src/services/appwireSocket.ts`
- Test: Rust local-server integration tests and TypeScript socket tests

**Interfaces:**
- Produces: `HubHttp.request(HubRequest) -> HubResponse`; `AppwireManager.open(Channel<AppwireEvent>) -> ConnectionId`; `TauriSocket implements WebSocketLike`.

- [ ] **Step 1: Write failing HTTP integration tests**

A local scripted server asserts pinned Host, bearer injection, removed cookie/authorization, allowlisted method/path/body, redirect rejection, cancellation, body fidelity, and no token in errors.

- [ ] **Step 2: Write failing AppWire integration tests**

Assert ordered text frames, close code propagation, one socket per profile, bounded overload close, generation rejection, and cancellation using a scripted local WebSocket server.

- [ ] **Step 3: Verify failures**

Run: `cargo test --manifest-path mobile/src-tauri/Cargo.toml transport`

Expected: FAIL because transport types are absent.

- [ ] **Step 4: Implement minimal Rust transport**

Use Reqwest with redirects disabled and Tokio Tungstenite. Restrict requests to route/method tables; return binary through `tauri::ipc::Response`. Use one bounded MPSC queue and generation counter for AppWire. `diagnostics.rs` keeps exactly 200 metadata-only entries and exposes only timestamp, operation, status class, byte count, generation, and opaque error ID; tests assert headers, URLs, bodies, filenames, and text never enter the ring.

- [ ] **Step 5: Write and run failing TypeScript adapter tests**

Fake Tauri invoke/channel primitives; assert `onopen`, `onmessage`, `onclose`, `onerror`, `send`, and stale-generation behavior match `WebSocketLike`.

- [ ] **Step 6: Implement TypeScript services**

The socket factory ignores browser WebSocket and identifies AppwireClient as `{name:"evener-mobile", version:"0.1.0"}`. No service imports Tauri directly except `nativeHttp.ts` and `appwireSocket.ts`.

- [ ] **Step 7: Run focused gates**

```bash
cargo test --manifest-path mobile/src-tauri/Cargo.toml transport
npm --prefix mobile test -- --run src/services
cargo clippy --manifest-path mobile/src-tauri/Cargo.toml --all-targets -- -D warnings
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add mobile/src-tauri/src mobile/src-tauri/Cargo.toml mobile/src-tauri/Cargo.lock mobile/src/services
git commit -m "feat(mobile): add authenticated Hub transport"
```

---

### Task 7: Build Onboarding and the Mobile Navigation Shell

**Files:**
- Create: `mobile/src/ui/*`, `mobile/src/navigation/*`
- Create: `mobile/src/state/connection.ts`, `preferences.ts`
- Create: `mobile/src/screens/OnboardingScreen.tsx`, `SessionsScreen.tsx`, `NewSessionScreen.tsx`, `SettingsScreen.tsx`
- Test: colocated `.test.tsx` files

**Interfaces:**
- Produces: `NavigationStore`; `ConnectionService`; `ConnectionStore`; reusable mobile `Button`, `IconButton`, `ListRow`, `Sheet`, `StatusMark`, `TopBar`, `BottomBar`.

- [ ] **Step 1: Write failing onboarding tests**

Assert scan is primary, paste is secondary, origin confirmation precedes save, token input clears, HTTP warning appears, errors redact query text, and successful pairing navigates to Sessions with success haptic.

- [ ] **Step 2: Write failing navigation/accessibility tests**

Assert three tabs, conversation push/pop history, no iOS swipe claim, accessible labels, 44-pixel classes, theme, reduced-motion, safe-area, and Dynamic Type bridge updates.

- [ ] **Step 3: Verify failures**

Run: `npm --prefix mobile test -- --run src/screens src/navigation src/ui`

Expected: FAIL on missing components.

- [ ] **Step 4: Implement design tokens and primitives**

Use mobile-only CSS modules/tokens. Keep one scroller per screen. Implement detent sheets in a portal with focus trap, Escape/back dismissal, and reduced-motion fallback.

- [ ] **Step 5: Implement connection/navigation stores and screens**

Services inject real or fake native clients. Shell screens show honest empty/loading/error states; do not import web widgets.

- [ ] **Step 6: Add fixture mode**

`?fixture=onboarding|sessions|new|settings` loads deterministic seeded data and fake bridge, enabling browser geometry tests without a Hub.

- [ ] **Step 7: Run checks**

```bash
npx --yes @biomejs/biome@2.5.5 check --write mobile/src
npm --prefix mobile run check
npm --prefix mobile test
npm --prefix mobile run boundary
npm --prefix mobile run build
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add mobile/src mobile/package.json mobile/package-lock.json
git commit -m "feat(mobile): add onboarding and native-feeling shell"
```

---

### Task 8: Wire the Session Roster and New-Session Basics

**Files:**
- Create: `mobile/src/data/hubTypes.ts`, `hubTypes.test.ts`
- Create: `mobile/src/services/roster.ts`, `spawn.ts`
- Create: `mobile/src/state/roster.ts`
- Modify: `SessionsScreen.tsx`, `NewSessionScreen.tsx`
- Test: colocated tests and Go fixture tests

**Interfaces:**
- Produces: strict `decodeTree`, `RosterService.refresh`, `groupRoster`, `SpawnService.start`.

- [ ] **Step 1: Add failing Go/TypeScript contract fixture tests**

Generate a canonical `/api/tree`, `/api/spawn-schema`, `/api/models`, and spawn response fixture from Go DTOs. TypeScript decoders must accept it and reject missing required keys/wrong types.

- [ ] **Step 2: Implement decoders and fixture generation**

Keep DTOs separate from view models. Add fixture freshness to `make lint-generated` only if generation is deterministic and documented.

- [ ] **Step 3: Write failing roster/new-session screen tests**

Assert Needs You/Running/Recent grouping, local title/project search, pull refresh, last-good error behavior, recent project selection, model/effort defaults, inline launch errors, and navigation after spawn.

- [ ] **Step 4: Implement services/stores/screens**

Use Hub defaults for omitted advanced launch fields. A tree notification from the shared AppWire client debounces one refresh through an injected clock.

- [ ] **Step 5: Run focused gates**

```bash
go test ./hubapi ./cmd/evener-hub -run 'Test.*Mobile.*Fixture' -count=1
npm --prefix mobile test -- --run src/data src/services src/state src/screens
npm --prefix mobile run boundary
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add hubapi cmd/evener-hub mobile/src/data mobile/src/services mobile/src/state mobile/src/screens
git commit -m "feat(mobile): add roster and session launch"
```

---

### Task 9: Produce the First iOS Simulator Build

**Files:**
- Modify generated iOS Info.plist/project files under `mobile/src-tauri/gen/apple/`
- Create: `mobile/scripts/check-ios-config.mjs`
- Test: `mobile/scripts/check-ios-config.test.mjs`

**Interfaces:**
- Produces: iOS 17+ app with required usage strings and local-network transport policy.

- [ ] **Step 1: Write failing Info.plist build test**

Assert deployment target 17.0 and nonempty camera, microphone, speech, and local-network usage strings in the built app plist.

- [ ] **Step 2: Verify failure**

Run the script against generated configuration; expect missing keys.

- [ ] **Step 3: Add precise permission copy and platform config**

Use user-facing strings that state why Evener needs each permission. Do not enable arbitrary public HTTP loads.

- [ ] **Step 4: Build simulator app**

```bash
cd mobile
npx tauri ios build --debug --target aarch64-sim
```

Expected: exit zero and an unsigned simulator `.app` path.

- [ ] **Step 5: Run configuration and smoke checks**

Launch the app with `xcrun simctl`, capture onboarding screenshot, verify fixture Sessions/New/Settings navigation, and run the built-plist check.

- [ ] **Step 6: Run foundation gates**

```bash
npm --prefix mobile run check
npm --prefix mobile test
npm --prefix mobile run boundary
cargo fmt --manifest-path mobile/src-tauri/Cargo.toml --check
cargo clippy --manifest-path mobile/src-tauri/Cargo.toml --all-targets -- -D warnings
cargo test --manifest-path mobile/src-tauri/Cargo.toml
go test ./cmd/evener-hub ./hubapi -count=1
git diff --check
```

Expected: all exit zero.

- [ ] **Step 7: Commit**

```bash
git add mobile/src-tauri/gen/apple mobile/scripts mobile/package.json mobile/package-lock.json
git commit -m "build(mobile): produce iOS simulator foundation"
```
