# Mobile Concept Lab Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the isolated, offline Evener Concepts Tauri application and its concept-neutral fixtures, state, platform adapter, gallery, and lab controls.

**Architecture:** A new top-level `mobile-concepts/` package owns a minimal Tauri shell and a React renderer. Pure TypeScript fixtures and reducers define all product behavior; visual concept modules consume that state in the companion experience plan. The package imports nothing from production `mobile/` and exposes no native product commands.

**Tech Stack:** Tauri 2.11, Rust 1.98, React 19.2, TypeScript 6.0, Vite 8.1, Zustand 5.0, Vitest 4.1, Testing Library 16.3, Biome 2.5.

**Spec:** `docs/superpowers/specs/2026-08-25-mobile-concept-lab-design.md`

**Next plan:** `docs/superpowers/plans/2026-08-25-mobile-concept-lab-2-experiences.md`

## Global Constraints

- Work only under `mobile-concepts/` plus these plan documents; do not modify production `mobile/` or its generated projects.
- Product name is exactly `Evener Concepts`; Tauri, iOS, and Android identifiers are exactly `com.primeradiant.evener.concepts`.
- The application uses bundled local fixtures only and exposes no Hub, AppWire, credential, QR, audio, file, shell, updater, or production-plugin behavior.
- Renderer source must not call `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource`, or `sendBeacon`.
- Tauri CSP contains `connect-src 'none'`; remote assets, scripts, styles, fonts, and media are forbidden.
- Packaged builds use the actual iOS or Android platform. Only development and test builds accept a platform override.
- Tests are deterministic: no network, credentials, ambient paths, sleeps, polling races, or fixed promise-flush counts.
- Use exact event completion or pure state transitions instead of arbitrary timers.
- Run Biome with `--write` on touched files under `src/` before its check gate.
- Stage only paths named by each task. Never use `git add .` or `git add -A`.
- Preserve all pre-existing dirty production-mobile files.
- Every commit uses `git commit --only -- <task paths>` so pre-existing staged files remain outside the commit.

## Post-Green Falsification Rule

Before each task's commit, copy the named task-owned source file to `$EVENER_SCRATCH_DIR`, apply the table's one-line mutation, run the focused test and require a nonzero exit with the named assertion, restore the byte-for-byte copy, verify it with `cmp`, and rerun the focused test green. Never use `git checkout` or reset to restore.

| Task | Load-bearing mutation | Required red evidence |
|---|---|---|
| 1 | Change `Bootstrap`'s `data-network-mode` from `offline` to `online` | Bootstrap offline-contract assertion fails |
| 2 | Remove the `fetch` entry from exported `NETWORK_API_PATTERNS` | boundary test fails with missing `network-api` violation |
| 3 | Make `scenarioProjectors.offline` use the baseline projector, then separately allow fixture version `2` | offline projection assertion and decoder-version assertion each fail |
| 4 | Make `selectConcept` replace the current route with gallery | cross-concept route-preservation assertion fails |
| 5 | Change `platformPrimitives.android.minimumTarget` from `48` to `44`, then remove the navigation controller's `popstate` dispatch | Android primitive assertion and Android Back route-order assertion each fail |

## File Map

| Path | Responsibility |
|---|---|
| `mobile-concepts/package.json` | Independent scripts and pinned frontend dependencies |
| `mobile-concepts/vite.config.ts` | Vite build and Vitest jsdom configuration |
| `mobile-concepts/src-tauri/tauri.conf.json` | Distinct identity, local assets, strict CSP, minimum iOS version |
| `mobile-concepts/src-tauri/src/lib.rs` | Minimal command-free Tauri builder |
| `mobile-concepts/scripts/boundary.mjs` | Static package, dependency, API, CSP, and source-boundary gate |
| `mobile-concepts/src/core/model.ts` | Shared concepts, routes, fixtures, transcript, work, question, voice, and preference types |
| `mobile-concepts/src/core/fixtures.ts` | Canonical realistic fictional data |
| `mobile-concepts/src/core/decodeFixture.ts` | Fail-closed runtime fixture decoder with injected development diagnostics |
| `mobile-concepts/src/core/scenarios.ts` | Scenario projection from canonical fixtures |
| `mobile-concepts/src/core/state.ts` | Complete concept-neutral state and action union |
| `mobile-concepts/src/core/reducer.ts` | Pure state transitions |
| `mobile-concepts/src/core/store.tsx` | Zustand vanilla store adapter and React context |
| `mobile-concepts/src/core/persistence.ts` | Versioned allowlisted preference storage |
| `mobile-concepts/src/core/platform.ts` | Actual-platform detection and development-only override |
| `mobile-concepts/src/core/viewport.ts` | VisualViewport-derived height and keyboard inset with exact listener cleanup |
| `mobile-concepts/src/core/history.ts` | Browser-history adapter that makes Android system Back drive reducer routes and overlays |
| `mobile-concepts/src/app/ConceptGallery.tsx` | First-run concept choice and concept metadata |
| `mobile-concepts/src/app/LabControls.tsx` | Deterministic scenario and display controls |
| `mobile-concepts/src/app/App.tsx` | Foundation composition and recovery boundary |

---

### Task 1: Scaffold the Independent Offline Tauri Package

**Files:**
- Create: `mobile-concepts/.gitignore`
- Create: `mobile-concepts/biome.jsonc`
- Create: `mobile-concepts/index.html`
- Create: `mobile-concepts/package.json`
- Create: `mobile-concepts/package-lock.json`
- Create: `mobile-concepts/tsconfig.json`
- Create: `mobile-concepts/vite.config.ts`
- Create: `mobile-concepts/rust-toolchain.toml`
- Create: `mobile-concepts/src/main.tsx`
- Create: `mobile-concepts/src/vite-env.d.ts`
- Create: `mobile-concepts/src/app/Bootstrap.tsx`
- Create: `mobile-concepts/src/app/Bootstrap.test.tsx`
- Create: `mobile-concepts/src/styles/base.css`
- Create: `mobile-concepts/src/test/setup.ts`
- Create: `mobile-concepts/src-tauri/.gitignore`
- Create: `mobile-concepts/src-tauri/Cargo.toml`
- Create: `mobile-concepts/src-tauri/Cargo.lock`
- Create: `mobile-concepts/src-tauri/build.rs`
- Create: `mobile-concepts/src-tauri/capabilities/default.json`
- Create: `mobile-concepts/src-tauri/src/lib.rs`
- Create: `mobile-concepts/src-tauri/src/main.rs`
- Create: `mobile-concepts/src-tauri/tauri.conf.json`

**Interfaces:**
- Produces: a standalone npm package with `check`, `test`, `boundary`, `build`, and `tauri` scripts.
- Produces: Rust entry point `pub fn run()` with a command-free `tauri::Builder`.
- Produces: renderer entry point `<Bootstrap />` mounted in `#root`.
- Consumes: no production source or configuration.

- [ ] **Step 1: Create the package and test configuration**

Use these dependency versions so the lab matches the installed production toolchain without sharing its lockfile:

```json
{
  "name": "evener-mobile-concepts",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "check": "biome ci src && tsc --noEmit --incremental false",
    "test": "node --test scripts/*.test.mjs && vitest run --maxWorkers=4",
    "boundary": "node scripts/boundary.mjs",
    "build": "tsc --noEmit --incremental false && vite build",
    "tauri": "tauri"
  },
  "dependencies": {
    "react": "19.2.7",
    "react-dom": "19.2.7",
    "zustand": "5.0.14"
  },
  "devDependencies": {
    "@biomejs/biome": "2.5.5",
    "@tauri-apps/cli": "2.11.4",
    "@testing-library/jest-dom": "6.9.1",
    "@testing-library/react": "16.3.2",
    "@types/node": "^26.0.0",
    "@types/react": "19.2.17",
    "@types/react-dom": "19.2.3",
    "@vitejs/plugin-react": "6.0.3",
    "jsdom": "29.1.1",
    "typescript": "6.0.3",
    "vite": "8.1.5",
    "vitest": "4.1.10"
  }
}
```

Use this Vite/Vitest configuration:

```ts
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const tauriPlatform = process.env.TAURI_ENV_PLATFORM;
const buildPlatform = tauriPlatform === "android" ? "android" : "ios";

export default defineConfig({
  plugins: [react()],
  define: { __TAURI_BUILD_PLATFORM__: JSON.stringify(buildPlatform) },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    exclude: ["**/node_modules/**", "**/dist/**", "scripts/**"],
  },
});
```

Configure TypeScript with `strict: true`, `noUncheckedIndexedAccess: true`, `moduleResolution: "bundler"`, `target: "ES2022"`, `jsx: "react-jsx"`, DOM libraries, `noEmit: true`, and `include: ["src"]`. `src/vite-env.d.ts` declares `const __TAURI_BUILD_PLATFORM__: "ios" | "android";`. `src/test/setup.ts` imports `@testing-library/jest-dom/vitest` and restores DOM/storage state after each test.

Use the repository's small Biome baseline:

```json
{
  "$schema": "https://biomejs.dev/schemas/2.5.5/schema.json",
  "formatter": { "enabled": true, "indentStyle": "space" },
  "linter": { "enabled": true, "rules": { "preset": "recommended" } }
}
```

`.gitignore` names only owned output: `node_modules/`, `dist/`, `.tsbuildinfo`, `src-tauri/target/`, Apple/Android build directories, Gradle caches, and generated `local.properties`.

`index.html` includes `<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">` and a `meta[http-equiv="Content-Security-Policy"]` whose content exactly matches the Tauri policy in Step 5. The viewport declaration enables real safe-area environment insets in mobile WebViews. The bundled HTML CSP makes the same artifact policy executable in the real-browser gate; it is not a substitute for the Tauri config check.

- [ ] **Step 2: Write the failing renderer smoke test**

```tsx
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Bootstrap } from "./Bootstrap";

describe("Bootstrap", () => {
  it("identifies the package as an offline concept app", () => {
    render(<Bootstrap />);
    expect(screen.getByRole("main")).toHaveAttribute("data-network-mode", "offline");
    expect(screen.getByRole("heading", { level: 1 })).toHaveAccessibleName("Evener Concepts");
  });
});
```

- [ ] **Step 3: Install dependencies and verify the test fails for the missing component**

Run:

```bash
cd mobile-concepts
npm install
npx vitest run src/app/Bootstrap.test.tsx
```

Expected: FAIL because `./Bootstrap` does not exist.

- [ ] **Step 4: Implement the smallest runnable renderer**

```tsx
export function Bootstrap() {
  return (
    <main className="bootstrap" data-network-mode="offline">
      <p className="bootstrap__eyebrow">Offline interaction prototype</p>
      <h1>Evener Concepts</h1>
    </main>
  );
}
```

`src/main.tsx` must import `src/styles/base.css`, find `#root`, throw if it is absent, and mount `<Bootstrap />` inside `StrictMode`:

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Bootstrap } from "./app/Bootstrap";
import "./styles/base.css";

const root = document.getElementById("root");
if (!root) throw new Error("missing #root mount point");
createRoot(root).render(<StrictMode><Bootstrap /></StrictMode>);
```

Base CSS owns document sizing, `100dvh`, `box-sizing`, system fonts, color scheme, reduced-motion defaults, and overridable `--safe-area-top/right/bottom/left` variables initialized from the corresponding `env(safe-area-inset-*, 0px)` values. Application chrome consumes only those variables, with one marked safe-area owner per edge. It must not draw a phone frame or fake status bar.

- [ ] **Step 5: Add the minimal Rust shell and strict Tauri configuration**

Use this Rust dependency floor:

```toml
[package]
name = "evener-mobile-concepts"
version = "0.1.0"
description = "Offline Evener mobile UX concept lab"
edition = "2021"
rust-version = "1.98.0"

[lib]
name = "evener_mobile_concepts_lib"
crate-type = ["staticlib", "cdylib", "rlib"]

[[bin]]
name = "evener-mobile-concepts"
path = "src/main.rs"

[build-dependencies]
tauri-build = { version = "2.6.3", features = [] }

[dependencies]
tauri = { version = "=2.11.5", features = [] }
```

`src-tauri/src/lib.rs` contains only:

```rust
#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .run(tauri::generate_context!())
        .expect("failed to run Evener Concepts");
}
```

`src-tauri/build.rs` calls `tauri_build::build()`. `src-tauri/src/main.rs` calls `evener_mobile_concepts_lib::run()`. The Rust toolchain file pins channel `1.98.0` with `rustfmt` and `clippy` in the minimal profile.

`tauri.conf.json` must set:

```json
{
  "$schema": "../node_modules/@tauri-apps/cli/config.schema.json",
  "productName": "Evener Concepts",
  "version": "0.1.0",
  "identifier": "com.primeradiant.evener.concepts",
  "build": {
    "frontendDist": "../dist",
    "beforeBuildCommand": "npm run build"
  },
  "app": {
    "windows": [{ "title": "Evener Concepts", "width": 430, "height": 932 }],
    "security": {
      "csp": "default-src 'self'; connect-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; media-src 'self'; object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'"
    }
  },
  "bundle": {
    "active": true,
    "targets": "all",
    "iOS": { "minimumSystemVersion": "17.0" }
  }
}
```

The capability file grants only the Tauri core default permission to the main window:

```json
{
  "$schema": "../gen/schemas/desktop-schema.json",
  "identifier": "default",
  "description": "Allows only Tauri core window behavior for the offline concept lab",
  "windows": ["main"],
  "permissions": ["core:default"]
}
```

Do not add product plugins or permissions.

- [ ] **Step 6: Run the scaffold gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src
npm run check
npx vitest run src/app/Bootstrap.test.tsx
npm run build
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
```

Expected: every command exits 0. The Vite build contains only bundled local assets.

- [ ] **Step 7: Commit the isolated scaffold**

```bash
git add mobile-concepts/.gitignore mobile-concepts/biome.jsonc mobile-concepts/index.html mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/tsconfig.json mobile-concepts/vite.config.ts mobile-concepts/rust-toolchain.toml mobile-concepts/src/main.tsx mobile-concepts/src/vite-env.d.ts mobile-concepts/src/app/Bootstrap.tsx mobile-concepts/src/app/Bootstrap.test.tsx mobile-concepts/src/styles/base.css mobile-concepts/src/test/setup.ts mobile-concepts/src-tauri/.gitignore mobile-concepts/src-tauri/Cargo.toml mobile-concepts/src-tauri/Cargo.lock mobile-concepts/src-tauri/build.rs mobile-concepts/src-tauri/capabilities/default.json mobile-concepts/src-tauri/src/lib.rs mobile-concepts/src-tauri/src/main.rs mobile-concepts/src-tauri/tauri.conf.json
git commit --only -m "feat(concepts): scaffold offline mobile lab" -- mobile-concepts/.gitignore mobile-concepts/biome.jsonc mobile-concepts/index.html mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/tsconfig.json mobile-concepts/vite.config.ts mobile-concepts/rust-toolchain.toml mobile-concepts/src/main.tsx mobile-concepts/src/vite-env.d.ts mobile-concepts/src/app/Bootstrap.tsx mobile-concepts/src/app/Bootstrap.test.tsx mobile-concepts/src/styles/base.css mobile-concepts/src/test/setup.ts mobile-concepts/src-tauri/.gitignore mobile-concepts/src-tauri/Cargo.toml mobile-concepts/src-tauri/Cargo.lock mobile-concepts/src-tauri/build.rs mobile-concepts/src-tauri/capabilities/default.json mobile-concepts/src-tauri/src/lib.rs mobile-concepts/src-tauri/src/main.rs mobile-concepts/src-tauri/tauri.conf.json
```

### Task 2: Enforce the Production and Network Boundary

**Files:**
- Create: `mobile-concepts/scripts/boundary.mjs`
- Create: `mobile-concepts/scripts/boundary.test.mjs`
- Modify: `mobile-concepts/package.json`

**Interfaces:**
- Produces: `scanSource(root: string): BoundaryViolation[]` returning sorted structured violations.
- Produces: `validateTauriConfig(config: object): BoundaryViolation[]` returning sorted structured violations.
- Produces: `validateRustManifest(manifestText: string): BoundaryViolation[]` returning sorted structured violations.
- Consumes: source paths under `mobile-concepts/src` and native files under `mobile-concepts/src-tauri`.

- [ ] **Step 1: Write failing checker tests against temporary fixtures**

The tests must prove structure, not the checker's internal argv. Use `mkdtemp` and write small source/config fixtures.

```js
import assert from "node:assert/strict";
import test from "node:test";
import { scanText, validateTauriConfig } from "./boundary.mjs";

test("source scanner rejects network and production mobile imports", () => {
  const violations = [
    ...scanText("src/a.ts", 'fetch("https://example.invalid")'),
    ...scanText("src/b.ts", 'import x from "../../mobile/src/state/x"'),
  ];
  assert.deepEqual(
    violations.map((item) => item.code),
    ["network-api", "production-mobile-import"],
  );
});

test("config validator requires the concept identity and offline CSP", () => {
  assert.deepEqual(
    validateTauriConfig({ productName: "Evener", identifier: "com.primeradiant.evener" }).map(
      (item) => item.code,
    ),
    ["product-name", "identifier", "offline-csp"],
  );
});
```

Also cover `XMLHttpRequest`, `WebSocket`, `EventSource`, `navigator.sendBeacon`, remote URL literals, remote HTML subresources, CSS `url(https://…)`, external SVG references, mismatched HTML/Tauri CSP directives, production plugin names, invoke handlers, forbidden entitlements, and malformed config. Add one negative fixture for every forbidden browser capability: `navigator.mediaDevices`/`getUserMedia`, SpeechRecognition/webkitSpeechRecognition, `speechSynthesis`/`SpeechSynthesisUtterance`, AudioContext/webkitAudioContext, file input/capture plus `showOpenFilePicker`/`showSaveFilePicker`/`showDirectoryPicker`, geolocation, Notification, PushManager/service-worker notification calls, and `navigator.vibrate`.

- [ ] **Step 2: Run the tests and confirm the missing module failure**

Run: `cd mobile-concepts && node --test scripts/boundary.test.mjs`

Expected: FAIL because `boundary.mjs` does not exist.

- [ ] **Step 3: Implement the boundary scanner**

Represent violations structurally:

```js
/** @typedef {{ code: string, file: string, detail: string }} BoundaryViolation */

export function violation(code, file, detail) {
  return { code, file, detail };
}
```

`scanText` scans executable application source, not natural-language documentation or test code. Implement its rules as exported structured constants including `NETWORK_API_PATTERNS`, so direct tests and falsification exercise the actual rule table. The real-tree walker excludes `*.test.*`, `src/test/**`, and `src/test/setup.ts` because the isolation tests intentionally name trapped APIs. It also scans `index.html`, every application CSS file, and local SVG/asset metadata for remote script, link, image, font, media, CSS `url()`, or external SVG references. It rejects:

- relative or aliased imports that resolve into top-level `mobile/`;
- `@tauri-apps/api/core` invocation and production plugin names;
- browser network constructors and methods;
- browser microphone/camera/media-capture, speech recognition/synthesis, audio-context, file-picker/input, geolocation, notification/push, and vibration/haptic APIs;
- `http:` or `https:` runtime literals outside tests;
- dynamic remote imports; and
- credential-shaped fixture keys such as `token`, `authorizationUrl`, `apiKey`, or `secret`.

`validateTauriConfig` requires exact name and identifier, local `frontendDist`, `connect-src 'none'`, no asset protocol, no remote URLs, no updater, and no product permissions. It parses the HTML CSP meta element separately from raw URL scanning and requires canonical directive equality with the Tauri CSP; `self` and local `data:` images/fonts are the only nonempty resource sources. `validateRustManifest` allows only `tauri` at runtime and `tauri-build` at build time. The CLI scans the real package, prints every violation, and exits nonzero when any exists.

- [ ] **Step 4: Run tests and the real boundary gate**

Run:

```bash
cd mobile-concepts
node --test scripts/boundary.test.mjs
npm run boundary
```

Expected: both commands exit 0.

- [ ] **Step 5: Make the boundary gate part of every ordinary test run**

Change scripts to:

```json
{
  "pretest": "node scripts/boundary.mjs",
  "test": "node --test scripts/*.test.mjs && vitest run --maxWorkers=4"
}
```

Run: `cd mobile-concepts && npm test`

Expected: boundary and tests exit 0.

- [ ] **Step 6: Commit the boundary gate**

```bash
git add mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/scripts/boundary.mjs mobile-concepts/scripts/boundary.test.mjs
git commit --only -m "test(concepts): enforce offline package boundary" -- mobile-concepts/package.json mobile-concepts/package-lock.json mobile-concepts/scripts/boundary.mjs mobile-concepts/scripts/boundary.test.mjs
```

### Task 3: Define Canonical Fixtures and Scenario Projection

**Files:**
- Create: `mobile-concepts/src/core/model.ts`
- Create: `mobile-concepts/src/core/fixtures.ts`
- Create: `mobile-concepts/src/core/decodeFixture.ts`
- Create: `mobile-concepts/src/core/scenarios.ts`
- Create: `mobile-concepts/src/core/fixtures.test.ts`
- Create: `mobile-concepts/src/core/decodeFixture.test.ts`
- Create: `mobile-concepts/src/core/scenarios.test.ts`

**Interfaces:**
- Produces: `ConceptId`, `Platform`, `ScenarioId`, `Appearance`, `TextScale`, `RootTab`, and `Route`.
- Produces: `PrototypeFixture` as the canonical immutable data shape.
- Produces: `canonicalFixture: PrototypeFixture`.
- Produces: `decodeFixture(value: unknown, fallback: PrototypeFixture, diagnostics: DiagnosticSink): PrototypeFixture`.
- Produces: `projectScenario(fixture: PrototypeFixture, scenario: ScenarioId): ScenarioProjection`.
- Produces: `scenarioIds: readonly ScenarioId[]` in the exact Lab Controls order.

- [ ] **Step 1: Write the model and failing fixture invariants**

Define the discriminants first:

```ts
export type ConceptId = "stillwater" | "constellation" | "field-notes";
export type Platform = "ios" | "android";
export type ScenarioId =
  | "baseline"
  | "loading"
  | "empty"
  | "offline"
  | "error"
  | "needs-attention"
  | "multi-agent"
  | "question"
  | "completed"
  | "voice"
  | "long-content";
export type Appearance = "system" | "light" | "dark";
export type TextScale = "standard" | "large" | "accessibility";
export type RootTab = "sessions" | "search" | "new" | "settings";
export type Route =
  | { kind: "gallery" }
  | { kind: "root"; tab: RootTab }
  | { kind: "conversation"; sessionId: string; focusItemId?: string }
  | { kind: "work"; sessionId: string }
  | { kind: "voice"; sessionId: string };

export type SessionState =
  | "needs-answer"
  | "needs-permission"
  | "running"
  | "waiting"
  | "completed"
  | "failed";

export interface SessionRecord {
  id: string;
  title: string;
  project: string;
  state: SessionState;
  summary: string;
  updatedLabel: string;
}

export type TranscriptItem =
  | { id: string; sessionId: string; kind: "user"; body: string }
  | { id: string; sessionId: string; kind: "assistant"; body: string }
  | { id: string; sessionId: string; kind: "tool"; label: string; status: SessionState; arguments: string; output: string }
  | { id: string; sessionId: string; kind: "question"; questionId: string }
  | { id: string; sessionId: string; kind: "error"; title: string; detail: string }
  | { id: string; sessionId: string; kind: "attachment"; name: string; mediaType: string; description: string };

export interface QuestionOption {
  id: string;
  label: string;
  detail: string;
  recommended: boolean;
}

export interface QuestionFixture {
  id: string;
  prompt: string;
  mode: "single" | "multiple";
  options: readonly QuestionOption[];
  allowNote: boolean;
  allowFallback: boolean;
  allowDecide: boolean;
  allowSkip: boolean;
}

export interface WorkNode {
  id: string;
  sessionId: string;
  parentId: string | null;
  kind: "task" | "subagent" | "job";
  title: string;
  state: SessionState;
  phase: string;
  elapsedLabel: string;
  output: string;
}

export interface SearchDocument {
  id: string;
  sessionId: string;
  itemId: string | null;
  kind: "session" | "project" | "transcript" | "tool" | "task";
  title: string;
  body: string;
}

export interface ModelChoice { id: string; provider: string; model: string; label: string }
export interface ProjectChoice { id: string; label: string; path: string }
export interface VoiceStep { id: string; state: "idle" | "ready" | "listening" | "processing" | "speaking" | "interrupted" | "denied" | "error"; caption: string; level: number }
export interface HubPresentationFixture { id: string; name: string; context: string; state: "connected" | "offline" }
export interface UsageFixture { tokens: number; costLabel: string; durationLabel: string; contextPercent: number }

export interface PrototypeFixture {
  version: 1;
  sessions: readonly SessionRecord[];
  transcript: readonly TranscriptItem[];
  questions: readonly QuestionFixture[];
  work: readonly WorkNode[];
  search: readonly SearchDocument[];
  recentProjects: readonly ProjectChoice[];
  models: readonly ModelChoice[];
  efforts: readonly ("low" | "medium" | "high")[];
  voiceSteps: readonly VoiceStep[];
  hubs: readonly HubPresentationFixture[];
  usage: UsageFixture;
}

export interface FixtureDiagnostic {
  code: "fixture-invalid" | "fixture-version";
  path: string;
}

export interface DiagnosticSink {
  report(diagnostic: FixtureDiagnostic): void;
}
```

Keep these records presentation-neutral. Transcript items use their declared discriminated union, and work nodes express hierarchy through `parentId` rather than recursive object identity.

Tests assert:

- every ID is unique within its collection;
- every transcript and work parent reference resolves;
- every search result resolves to a session and transcript item;
- each question has unique option IDs and a valid selection mode;
- fixtures contain no URL, credential-shaped key, home-directory prefix, or real authorization value; and
- collections are deeply frozen in development tests.

Decoder tests pass the canonical fixture through unchanged and reject null, arrays, missing collections, wrong discriminants, dangling references, unknown fields with credential-shaped names, and versions other than `1`. Every rejected value returns the exact canonical fallback and emits one structured diagnostic with no copied bad value or prose payload. App composition supplies a console-backed sink only in development and a no-op sink in packaged builds.

- [ ] **Step 2: Run the fixture tests and confirm missing exports**

Run: `cd mobile-concepts && npx vitest run src/core/fixtures.test.ts src/core/decodeFixture.test.ts`

Expected: FAIL because fixture/model/decoder exports are absent.

- [ ] **Step 3: Implement realistic fictional canonical fixtures**

Use stable IDs and these five sessions so all concepts compare the deck's same content:

```ts
const sessions = [
  { id: "session-mobile-release", title: "Mobile release checklist", project: "Evener", state: "needs-answer", summary: "Answer 2 questions", updatedLabel: "now" },
  { id: "session-pairing-review", title: "Pairing security review", project: "prime-radiant", state: "needs-permission", summary: "Permission needed", updatedLabel: "3m" },
  { id: "session-native-client", title: "Native mobile client", project: "Evener", state: "running", summary: "3 subagents · Implementing voice", updatedLabel: "12m" },
  { id: "session-roster-latency", title: "Hub roster latency", project: "Evener", state: "running", summary: "Testing source timeout", updatedLabel: "18m" },
  { id: "session-pairing-pr", title: "Pairing endpoint review", project: "prime-radiant", state: "completed", summary: "Completed · 14 changes", updatedLabel: "1h" }
] as const satisfies readonly SessionRecord[];
```

Build `canonicalFixture` with `version: 1`, then pass it through `decodeFixture` at the App fixture boundary. The decoder validates the complete discriminated shape and cross-reference invariants before returning the input; it never partially repairs untrusted data.

Populate at least:

- two attention sessions, two running sessions, and one recent session;
- one conversation with user prose, assistant prose, two tool calls, one error, one attachment, and long code/output disclosure;
- one single-select and one multi-select structured question with note/fallback/decide/skip semantics;
- three tasks, two nested subagents, and two jobs across active, waiting, complete, and failed states;
- three model choices and three reasoning-effort choices;
- at least two recent project choices plus a distinct typed fixture-path outcome;
- a voice sequence covering idle, ready, listening, processing, speaking, interrupted, denied, and error;
- two clearly fictional Hub presentation rows whose context contains no hostname, address, URL, or credential;
- one usage record with tokens, fictional cost label, duration label, and bounded context percentage; and
- search documents for session, project, transcript, tool, and task matches.

Do not copy actual repository paths or credentials. Use fictional paths rooted at `/workspace/aurora` and `/workspace/harbor`.

- [ ] **Step 4: Write failing scenario projection tests**

```ts
it.each(scenarioIds)("projects %s without mutating canonical fixtures", (scenario) => {
  const before = structuredClone(canonicalFixture);
  const projected = projectScenario(canonicalFixture, scenario);
  expect(projected.id).toBe(scenario);
  expect(canonicalFixture).toEqual(before);
});
```

Add exact structural assertions: `loading` has a loading screen state, `empty` has no sessions, `offline` has stale content plus offline status, `error` has a recoverable error, `needs-attention` selects the question session, `multi-agent` exposes all work nodes, `completed` has no active work, `voice` begins ready, and `long-content` exposes the long transcript.

- [ ] **Step 5: Implement scenario projection as immutable overlays**

```ts
export interface ScenarioProjection {
  id: ScenarioId;
  screenState: "ready" | "loading" | "empty" | "offline" | "error";
  fixture: PrototypeFixture;
  selectedSessionId: string | null;
  recoverable: boolean;
}
```

Use explicit functions per scenario and an exported total `scenarioProjectors: Record<ScenarioId, Projector>` map. Return new arrays and records; never edit `canonicalFixture`.

- [ ] **Step 6: Run core fixture gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src/core
npx vitest run src/core/fixtures.test.ts src/core/decodeFixture.test.ts src/core/scenarios.test.ts
npm run check
npm run boundary
```

Expected: every command exits 0.

- [ ] **Step 7: Commit the fixture core**

```bash
git add mobile-concepts/src/core/model.ts mobile-concepts/src/core/fixtures.ts mobile-concepts/src/core/decodeFixture.ts mobile-concepts/src/core/scenarios.ts mobile-concepts/src/core/fixtures.test.ts mobile-concepts/src/core/decodeFixture.test.ts mobile-concepts/src/core/scenarios.test.ts
git commit --only -m "feat(concepts): add deterministic lab fixtures" -- mobile-concepts/src/core/model.ts mobile-concepts/src/core/fixtures.ts mobile-concepts/src/core/decodeFixture.ts mobile-concepts/src/core/scenarios.ts mobile-concepts/src/core/fixtures.test.ts mobile-concepts/src/core/decodeFixture.test.ts mobile-concepts/src/core/scenarios.test.ts
```

### Task 4: Implement the Pure State Machine and Allowlisted Persistence

**Files:**
- Create: `mobile-concepts/src/core/state.ts`
- Create: `mobile-concepts/src/core/reducer.ts`
- Create: `mobile-concepts/src/core/reducer.test.ts`
- Create: `mobile-concepts/src/core/persistence.ts`
- Create: `mobile-concepts/src/core/persistence.test.ts`
- Create: `mobile-concepts/src/core/store.tsx`
- Create: `mobile-concepts/src/core/store.test.tsx`

**Interfaces:**
- Produces: `PrototypeState` and total `PrototypeAction` union.
- Produces: `createInitialState(options: InitialStateOptions): PrototypeState`.
- Produces: `reducePrototype(state: PrototypeState, action: PrototypeAction): PrototypeState`.
- Produces: `decodePreferences(raw: string | null): PersistedPreferencesV1`.
- Produces: `encodePreferences(state: PrototypeState): string`.
- Produces: `createPrototypeStore(options): StoreApi<PrototypeStore>`.
- Produces: `PrototypeProvider`, `usePrototypeState(selector)`, and `usePrototypeActions(): PrototypeActions`.

- [ ] **Step 1: Define the complete state and action contract**

`PrototypeState` contains:

```ts
export interface PrototypeState {
  concept: ConceptId | null;
  platform: Platform;
  scenario: ScenarioId;
  appearance: Appearance;
  textScale: TextScale;
  reducedMotion: boolean;
  route: Route;
  history: Route[];
  overlay: "concept-switcher" | "lab-controls" | null;
  refreshState: "idle" | "refreshing" | "complete";
  sessionQuery: string;
  globalQuery: string;
  selectedSessionId: string | null;
  focusedItemId: string | null;
  expandedToolIds: ReadonlySet<string>;
  expandedWorkIds: ReadonlySet<string>;
  draft: string;
  composerMode: "send" | "steer" | "queue" | "running";
  syntheticTurn: "none" | "starting" | "complete" | "stopped";
  answers: Readonly<Record<string, QuestionAnswerState>>;
  newSession: NewSessionDraft;
  voicePreferences: VoicePreferences;
  voice: VoicePrototypeState;
  projection: ScenarioProjection;
  resetGeneration: number;
}

export interface QuestionAnswerState {
  selectedOptionIds: readonly string[];
  note: string;
  resolution: "answer" | "fallback" | "decide" | "skip" | null;
  submitted: boolean;
}

export interface NewSessionDraft {
  project: string;
  prompt: string;
  modelId: string;
  effort: "low" | "medium" | "high";
  outcome: "editing" | "starting" | "success" | "failure";
  errorCode: string | null;
}

export interface VoicePrototypeState {
  stepIndex: number;
  muted: boolean;
  stopped: boolean;
  ended: boolean;
}

export interface VoicePreferences {
  speakResponses: boolean;
  rate: "slow" | "normal" | "fast";
}

export interface InitialStateOptions {
  platform: Platform;
  preferences: PersistedPreferencesV1;
  projection?: ScenarioProjection;
}

export type PrototypeAction =
  | { type: "selectConcept"; concept: ConceptId }
  | { type: "setScenario"; scenario: ScenarioId }
  | { type: "setAppearance"; appearance: Appearance }
  | { type: "setTextScale"; textScale: TextScale }
  | { type: "setReducedMotion"; reducedMotion: boolean }
  | { type: "navigateRoot"; tab: RootTab }
  | { type: "openSession"; sessionId: string; focusItemId?: string }
  | { type: "openWork"; sessionId: string }
  | { type: "openVoice"; sessionId: string }
  | { type: "openOverlay"; overlay: "concept-switcher" | "lab-controls" }
  | { type: "goBack" }
  | { type: "refreshSessions" }
  | { type: "completeRefresh" }
  | { type: "setSessionQuery"; value: string }
  | { type: "setGlobalQuery"; value: string }
  | { type: "openSearchResult"; resultId: string }
  | { type: "toggleTool"; itemId: string }
  | { type: "toggleWork"; nodeId: string }
  | { type: "setDraft"; value: string }
  | { type: "setComposerMode"; mode: "send" | "steer" | "queue" }
  | { type: "submitComposer" }
  | { type: "completeSyntheticTurn" }
  | { type: "stopSyntheticTurn" }
  | { type: "setQuestionOption"; questionId: string; optionId: string; selected: boolean }
  | { type: "setQuestionNote"; questionId: string; note: string }
  | { type: "resolveQuestion"; questionId: string; resolution: "answer" | "fallback" | "decide" | "skip" }
  | { type: "setNewSessionProject"; value: string }
  | { type: "selectRecentProject"; projectId: string }
  | { type: "setNewSessionPrompt"; value: string }
  | { type: "setNewSessionModel"; modelId: string }
  | { type: "setNewSessionEffort"; effort: "low" | "medium" | "high" }
  | { type: "submitNewSession" }
  | { type: "completeNewSession"; result: "success" | "failure" }
  | { type: "setSpeakResponses"; enabled: boolean }
  | { type: "setSpeechRate"; rate: "slow" | "normal" | "fast" }
  | { type: "advanceVoice" }
  | { type: "setVoiceState"; state: VoiceStep["state"] }
  | { type: "toggleVoiceMute" }
  | { type: "stopVoice" }
  | { type: "endVoice" }
  | { type: "reset" };
```

Keep this union exhaustive. If implementation needs another product transition, update the union and reducer tests before adding a component-local substitute.

- [ ] **Step 2: Write failing reducer tests for cross-concept preservation and behavior**

Cover these positive conditions:

- `selectConcept` preserves route, scenario, queries, draft, answers, disclosures, and voice state;
- `setScenario` rebuilds scenario-backed state and preserves reviewer display preferences;
- route pushes and back pops are ordered and Android back uses the same action;
- `goBack` closes the current overlay before popping Work, Voice, or Conversation;
- refresh enters `refreshing`, completes only on `completeRefresh`, and never changes canonical fixtures;
- opening a search result routes to its session and sets `focusedItemId`;
- empty Search query exposes its prompt state, unmatched query exposes no-result state with query context, and matches retain result kind/context before focus;
- tool/work toggles are idempotent set operations;
- composer submit transitions through starting and complete without a timer;
- stop changes only a running synthetic turn;
- question resolution rejects invalid answers and accepts valid answer/note states;
- structured-question submit remains invalid/disabled until each required single/multi selection is satisfied;
- new-session submit enters `starting`; a separate completion action creates a stable synthetic session and route on success or preserves the form on failure;
- recent-project selection and direct fixture-path entry are distinct actions that produce the same validated project field;
- voice preferences update through typed actions but remain process-local;
- voice Stop halts the current visual step without ending the voice destination, while End exits it;
- each voice step projects its deterministic `level` value without reading audio hardware;
- voice advances through the fixture's explicit sequence and can jump to denied/error states; and
- reset increments `resetGeneration`, clears interaction state, and returns to the gallery.

- [ ] **Step 3: Run reducer tests and verify missing implementation failure**

Run: `cd mobile-concepts && npx vitest run src/core/reducer.test.ts`

Expected: FAIL because reducer exports do not exist.

- [ ] **Step 4: Implement the pure reducer**

Use exhaustive checking:

```ts
function assertNever(value: never): never {
  throw new Error(`unhandled prototype action: ${JSON.stringify(value)}`);
}
```

No action reads the clock, storage, DOM, or random values. `createInitialState` starts at `{ kind: "gallery" }` when the decoded concept is null and at `{ kind: "root", tab: "sessions" }` when a concept was persisted; all interaction fields still use canonical defaults. Synthetic IDs come from stable fixture-derived counters stored in state. Impossible actions return the same state object; valid actions return new sets/records and never mutate prior state.

- [ ] **Step 5: Write failing persistence tests**

The persisted shape is exact and versioned:

```ts
export interface PersistedPreferencesV1 {
  version: 1;
  concept: ConceptId | null;
  appearance: Appearance;
  textScale: TextScale;
  reducedMotion: boolean;
  scenario: ScenarioId;
}
```

Tests prove malformed JSON, unknown versions, unknown enum values, extra credential-shaped fields, and future records return defaults. Encoding must omit route, queries, draft, answers, sessions, disclosures, form values, voice preferences, and voice state.

- [ ] **Step 6: Implement persistence and the store adapter**

Use storage key `evener-concepts.preferences.v1`. Define a narrow dependency:

```ts
export interface PreferenceStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export interface PrototypeActions {
  dispatch(action: PrototypeAction): void;
  resetPrototype(): void;
}

export type PrototypeStore = PrototypeState & PrototypeActions;

export interface CreateStoreOptions {
  platform: Platform;
  storage: PreferenceStorage;
  fixtureInput: unknown;
  diagnostics: DiagnosticSink;
}
```

`createPrototypeStore(options: CreateStoreOptions): StoreApi<PrototypeStore>` decodes preferences from `storage`, decodes `fixtureInput` against `canonicalFixture`, then calls `createInitialState`. It delegates all behavior to `reducePrototype`, persists only after allowlisted preference actions, and exposes `resetPrototype()` that removes the key before dispatching reset.

React context receives a store instance. It must not construct a new store during rerenders.

- [ ] **Step 7: Run state and persistence gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src/core
npx vitest run src/core/reducer.test.ts src/core/persistence.test.ts src/core/store.test.tsx
npm run check
npm run boundary
```

Expected: every command exits 0.

- [ ] **Step 8: Commit the state core**

```bash
git add mobile-concepts/src/core/state.ts mobile-concepts/src/core/reducer.ts mobile-concepts/src/core/reducer.test.ts mobile-concepts/src/core/persistence.ts mobile-concepts/src/core/persistence.test.ts mobile-concepts/src/core/store.tsx mobile-concepts/src/core/store.test.tsx
git commit --only -m "feat(concepts): add deterministic prototype state" -- mobile-concepts/src/core/state.ts mobile-concepts/src/core/reducer.ts mobile-concepts/src/core/reducer.test.ts mobile-concepts/src/core/persistence.ts mobile-concepts/src/core/persistence.test.ts mobile-concepts/src/core/store.tsx mobile-concepts/src/core/store.test.tsx
```

### Task 5: Add Platform Adaptation, Concept Gallery, and Lab Controls

**Files:**
- Create: `mobile-concepts/src/core/platform.ts`
- Create: `mobile-concepts/src/core/platform.test.ts`
- Create: `mobile-concepts/src/core/viewport.ts`
- Create: `mobile-concepts/src/core/viewport.test.ts`
- Create: `mobile-concepts/src/core/history.ts`
- Create: `mobile-concepts/src/core/history.test.ts`
- Create: `mobile-concepts/src/app/conceptMetadata.ts`
- Create: `mobile-concepts/src/app/ConceptGallery.tsx`
- Create: `mobile-concepts/src/app/ConceptGallery.test.tsx`
- Create: `mobile-concepts/src/app/LabControls.tsx`
- Create: `mobile-concepts/src/app/LabControls.test.tsx`
- Create: `mobile-concepts/src/app/RecoveryBoundary.tsx`
- Create: `mobile-concepts/src/app/RecoveryBoundary.test.tsx`
- Create: `mobile-concepts/src/app/App.tsx`
- Create: `mobile-concepts/src/app/App.test.tsx`
- Create: `mobile-concepts/src/styles/foundation.css`
- Modify: `mobile-concepts/src/main.tsx`
- Delete: `mobile-concepts/src/app/Bootstrap.tsx`
- Delete: `mobile-concepts/src/app/Bootstrap.test.tsx`

**Interfaces:**
- Produces: `resolvePlatform(input: PlatformDetectionInput): Platform`.
- Produces: `platformPrimitives: Record<Platform, PlatformPrimitives>` and `getPlatformPrimitives(platform): PlatformPrimitives`.
- Produces: `resolveEffectiveAppearance(preference: Appearance, prefersDark: boolean): "light" | "dark"`.
- Produces: `resolveEffectiveReducedMotion(simulated: boolean, systemPrefersReducedMotion: boolean): boolean`.
- Produces: `installViewportMetrics(target: Window, root: HTMLElement): () => void`.
- Produces: `createNavigationController(store, historyTarget): NavigationController`.
- Produces: `conceptMetadata: Record<ConceptId, ConceptMetadata>`.
- Produces: `<ConceptGallery />`, `<LabControls />`, and `<RecoveryBoundary />`.
- Produces: `<App platformInput={...} storage={...} />` for the experience plan.
- Consumes: `PrototypeProvider` and store actions from Task 4.

- [ ] **Step 1: Write failing platform tests**

```ts
it.each([
  ["Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)", "ios"],
  ["Mozilla/5.0 (Linux; Android 15; Pixel 9)", "android"],
] as const)("detects %s", (userAgent, expected) => {
  expect(resolvePlatform({ userAgent, allowOverride: false, override: null, fallback: "ios" })).toBe(expected);
});

it("ignores an override in packaged mode", () => {
  expect(
    resolvePlatform({ userAgent: "Android", allowOverride: false, override: "ios", fallback: "ios" }),
  ).toBe("android");
});
```

Also prove a test/development override accepts only `ios` or `android`, an iPad desktop user agent resolves through touch/platform hints, and unknown packaged agents fail closed to the compile-time target passed by `main.tsx` rather than guessing iOS.

Define and test this total adapter:

```ts
export interface PlatformPrimitives {
  platform: Platform;
  navigation: "ios-tab-bar" | "material-navigation-bar";
  title: "large-or-inline" | "material-top-app-bar";
  sheet: "ios-sheet" | "material-modal-bottom-sheet";
  dialog: "ios-alert" | "material-dialog";
  elevation: "hairline" | "tonal-elevation";
  feedback: "ios-highlight" | "material-state-layer";
  back: "stack-control" | "system-predictive";
  minimumTarget: 44 | 48;
  safeArea: "ios-environment" | "android-insets";
}
```

`platformPrimitives` is a `satisfies Record<Platform, PlatformPrimitives>` map. Tests assert every field for both platforms and prove `minimumTarget`, back, sheet, dialog, feedback, and safe-area semantics differ as specified.

Test `resolveEffectiveAppearance` for explicit light/dark and both system color-scheme values. Test effective reduced motion as the OR of the reviewer simulation and system preference. Component tests dispatch real `matchMedia` change events for both queries and prove document attributes update and listeners are removed on unmount.

Test `installViewportMetrics` with an injected VisualViewport-shaped event target. It sets `--visual-viewport-height` and a nonnegative `--keyboard-inset` from `window.innerHeight - (visualViewport.height + visualViewport.offsetTop)`, updates on `resize` and `scroll`, and removes both exact listeners. When `visualViewport` is absent, it sets height from `innerHeight` and keyboard inset to zero.

Write navigation-history tests against jsdom's real `window.history` and `PopStateEvent`. Use this interface:

```ts
export interface NavigationController {
  dispatch(action: PrototypeAction): void;
  dispose(): void;
}

export interface ConceptHistoryState {
  owner: "evener-concepts";
  depth: number;
  key: string;
}
```

Tests prove initialization `replaceState`s one owned depth-0 entry; `openSession`, `openSearchResult`, successful `completeNewSession`, `openWork`, `openVoice`, Concept Switcher, and Lab Controls each push exactly one unique entry; duplicate state does not push; root-tab selection and Reset replace at depth zero rather than stack; explicit Back calls `history.back()`; and `popstate` closes overlay first then dispatches reducer `goBack` through Voice/Work/Conversation in order without repushing. A foreign or malformed history state fails closed to the current root entry. `dispose()` removes the exact popstate listener, and a post-dispose event changes nothing.

- [ ] **Step 2: Implement platform resolution and document attributes**

```ts
export interface PlatformDetectionInput {
  userAgent: string;
  navigatorPlatform?: string;
  maxTouchPoints?: number;
  allowOverride: boolean;
  override: string | null;
  fallback: Platform;
}
```

`main.tsx` passes `fallback: __TAURI_BUILD_PLATFORM__` and `allowOverride: import.meta.env.DEV || import.meta.env.MODE === "test" || import.meta.env.MODE === "browser-test"`. It reads `platform` from the query string only when overrides are allowed. The browser verification plan builds with the dedicated `browser-test` mode; Tauri's `beforeBuildCommand` remains `npm run build`, whose production mode rejects overrides. Resolve `system` appearance through `matchMedia("(prefers-color-scheme: dark)")` and effective motion through `matchMedia("(prefers-reduced-motion: reduce)")`, subscribe to their change events, and clean up the exact listeners. Apply effective `data-appearance` and `data-reduced-motion` plus `data-platform` and `data-text-scale` to the document root.

App composition resolves `getPlatformPrimitives(platform)` once and passes that same object to the gallery, concept switcher, Lab Controls, and selected concept renderer. Concepts retain distinct composition and style but must use these primitives for target size, navigation family, title hierarchy, sheet/dialog semantics, feedback, back behavior, and safe-area ownership.

Create one `NavigationController` for the store and pass its `dispatch` to every UI surface. `openSession`, `openSearchResult`, successful `completeNewSession`, `openWork`, `openVoice`, and `openOverlay` reduce state then push an owned browser-history entry. `navigateRoot` and `reset` reduce and replace the owned depth-0 entry. UI Back/Close/Escape dispatches `goBack`, which asks browser history to move; only the resulting `popstate` reduces the internal stack. Voice End dispatches `endVoice` to record the terminal state, then `goBack` to close the route. A suppression guard prevents pop reduction from creating another entry. Android system Back therefore reaches the same `popstate` path; at owned depth zero the WebView may perform its normal app-background/exit behavior.

Install viewport metrics once at the App boundary. Foundation CSS sizes the screen with `var(--visual-viewport-height, 100dvh)` and raises fixed bottom controls by `--keyboard-inset` without adding the bottom safe area twice.

- [ ] **Step 3: Write failing gallery and controls tests**

Tests prove:

- the gallery exposes three selectable articles with headings and theses;
- selecting a concept dispatches its exact `ConceptId`;
- the selected concept is conveyed through `aria-current` or `aria-pressed`;
- Lab Controls exposes every `scenarioIds` value;
- appearance, text scale, and reduced motion dispatch their typed actions;
- Reset clears persistence and returns to an unselected gallery; and
- controls remain keyboard reachable at large text.

Use role and state assertions rather than snapshots.

- [ ] **Step 4: Implement exact concept metadata**

```ts
export interface ConceptMetadata {
  name: string;
  number: "01" | "02" | "03";
  family: string;
  thesis: string;
  accent: string;
}

export const conceptMetadata = {
  stillwater: {
    name: "Stillwater",
    number: "01",
    family: "Quiet Instrument",
    thesis: "A calm, exact tool that disappears behind the work.",
    accent: "spruce",
  },
  constellation: {
    name: "Constellation",
    number: "02",
    family: "Living System",
    thesis: "See the shape of the work, not just its log.",
    accent: "luminous mint",
  },
  "field-notes": {
    name: "Field Notes",
    number: "03",
    family: "Transcript Studio",
    thesis: "Treat every session as a durable, readable record of work.",
    accent: "rust",
  },
} as const satisfies Record<ConceptId, ConceptMetadata>;
```

The gallery uses semantic buttons within articles, not clickable `div` elements. It previews color, hierarchy, and type without drawing phone hardware.

- [ ] **Step 5: Implement Lab Controls and recovery**

Lab Controls is a dialog or sheet selected through a normal button. It groups scenario, appearance, text, and motion controls in fieldsets. `RecoveryBoundary` catches renderer errors, shows a local recovery surface, and calls `resetPrototype`; it never reloads a remote URL.

- [ ] **Step 6: Replace Bootstrap with the foundation App**

`App` resolves the platform, creates one store, and renders the gallery plus Lab Controls. A selected card remains visibly selected. The experience plan will replace the post-selection gallery composition with the route renderer; this task does not invent temporary session screens.

- [ ] **Step 7: Run the foundation plan gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src
npm run check
npm test
npm run boundary
npm run build
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
git diff --check
```

Expected: every command exits 0; the ordinary test run executes boundary, Node, and Vitest suites.

- [ ] **Step 8: Commit the foundation app**

```bash
git add mobile-concepts/src/core/platform.ts mobile-concepts/src/core/platform.test.ts mobile-concepts/src/core/viewport.ts mobile-concepts/src/core/viewport.test.ts mobile-concepts/src/core/history.ts mobile-concepts/src/core/history.test.ts mobile-concepts/src/app/conceptMetadata.ts mobile-concepts/src/app/ConceptGallery.tsx mobile-concepts/src/app/ConceptGallery.test.tsx mobile-concepts/src/app/LabControls.tsx mobile-concepts/src/app/LabControls.test.tsx mobile-concepts/src/app/RecoveryBoundary.tsx mobile-concepts/src/app/RecoveryBoundary.test.tsx mobile-concepts/src/app/App.tsx mobile-concepts/src/app/App.test.tsx mobile-concepts/src/styles/foundation.css mobile-concepts/src/main.tsx mobile-concepts/src/app/Bootstrap.tsx mobile-concepts/src/app/Bootstrap.test.tsx
git commit --only -m "feat(concepts): add gallery and lab controls" -- mobile-concepts/src/core/platform.ts mobile-concepts/src/core/platform.test.ts mobile-concepts/src/core/viewport.ts mobile-concepts/src/core/viewport.test.ts mobile-concepts/src/core/history.ts mobile-concepts/src/core/history.test.ts mobile-concepts/src/app/conceptMetadata.ts mobile-concepts/src/app/ConceptGallery.tsx mobile-concepts/src/app/ConceptGallery.test.tsx mobile-concepts/src/app/LabControls.tsx mobile-concepts/src/app/LabControls.test.tsx mobile-concepts/src/app/RecoveryBoundary.tsx mobile-concepts/src/app/RecoveryBoundary.test.tsx mobile-concepts/src/app/App.tsx mobile-concepts/src/app/App.test.tsx mobile-concepts/src/styles/foundation.css mobile-concepts/src/main.tsx mobile-concepts/src/app/Bootstrap.tsx mobile-concepts/src/app/Bootstrap.test.tsx
```

## Foundation Completion Check

Before beginning the experience plan, verify:

```bash
cd mobile-concepts
npm run check
npm test
npm run boundary
npm run build
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
```

Record the exit code and test count for each gate. Do not proceed if any command fails or if any production `mobile/` path appears in the foundation commits.
