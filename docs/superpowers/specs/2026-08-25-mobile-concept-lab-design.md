# Evener Concepts Mobile Lab Design

- **Date:** 2026-08-25
- **Status:** Approved for specification and planning
- **Platforms:** iOS and Android
- **Client:** Standalone Tauri 2 application with a dedicated React renderer

## Goal

Build one installable, offline mobile prototype that lets reviewers use and compare three complete Evener UX directions on iOS and Android:

1. **Stillwater** — a quiet, exact system instrument;
2. **Constellation** — a living view of multi-agent orchestration; and
3. **Field Notes** — an editorial work journal centered on the transcript.

The prototype must make the concepts comparable, not merely display static screens. Reviewers can navigate, search, open sessions, inspect transcript activity, answer structured questions, start simulated sessions, change settings, exercise voice states, and switch concepts without reinstalling.

The prototype evaluates product structure, interaction, visual language, and platform expression. It does not evaluate Hub connectivity or production transport.

## Product Decision

Create a new top-level `mobile-concepts/` project. Do not add a concept mode, build flavor, or runtime switch to the production `mobile/` application.

The lab is a separate application:

- **display name:** `Evener Concepts`;
- **Tauri identifier:** `com.primeradiant.evener.concepts`;
- **iOS bundle identifier:** `com.primeradiant.evener.concepts`;
- **Android application ID:** `com.primeradiant.evener.concepts`;
- **data namespace:** owned only by the concept app; and
- **native projects:** generated and maintained under `mobile-concepts/src-tauri/gen/apple` and `mobile-concepts/src-tauri/gen/android`.

Production Evener and Evener Concepts must install together. Neither application may replace, migrate, read, or write the other's preferences, Keychain entries, files, caches, or package data.

### Rejected approaches

#### A concept flavor inside `mobile/`

A shared project would reduce initial setup, but it would also share production Rust startup, generated native projects, permissions, plugins, package configuration, and storage risks. A stale or partially merged build configuration could ship the production identifier or start production services. The small reduction in duplication does not justify that coupling.

#### A new React Native, Capacitor, SwiftUI, or Compose implementation

A second application stack could improve platform fidelity at the edges, but it would duplicate build infrastructure and discard the project's working Tauri toolchain. The lab needs credible native-feeling behavior, not a second production client architecture.

## Audience and Evaluation Questions

The primary reviewer is a technical operator who supervises concurrent AI coding work. The lab should answer:

- Which concept makes attention and active work easiest to scan?
- Which transcript is easiest to read for several minutes?
- Which concept explains tools, subagents, and task progress most clearly?
- Which intervention controls feel safest?
- Which voice treatment feels integrated rather than decorative?
- Does each concept retain its identity in both iOS and Android conventions?
- Which elements should form the final product direction?

All concepts use the same content, routes, actions, state transitions, and scenario controls. Differences should therefore come from information emphasis, composition, typography, color, motion, and platform treatment rather than unequal functionality.

## Shared Information Architecture

The lab has four root destinations:

1. **Sessions** — attention, running work, recent work, and session search;
2. **Search** — cross-session transcript and project search;
3. **New** — simulated session creation; and
4. **Settings** — appearance, voice preferences, concept selection, and lab controls.

Opening a session pushes a focused conversation destination. Session-specific work appears through an activity destination or sheet appropriate to the platform and concept. Voice opens as a focused full-screen experience.

The app starts with a concept gallery on first launch. Each card names the concept, states its thesis, previews its visual system, and offers one primary action. Choosing a card opens Sessions.

Every screen includes a small, accessible **Switch concept** action in its top-level navigation chrome. The action opens a concept sheet without changing the current route or fixture scenario. Voice retains the switch action in its top bar. Settings also contains a full concept picker.

The app remembers the last selected concept and reviewer display preferences across launches. Session, form, question, search, and voice interaction state resets to a deterministic baseline on launch. Switching concepts during one run preserves the current route, scenario, and concept-neutral interaction state so the reviewer compares the same moment.

## Shared Interaction Contract

Each concept implements the following flows with the same fixture data and outcomes.

### Sessions

- Show **Needs You**, **Running**, and **Recent** groups when populated.
- Search or filter sessions by title and project.
- Open a session from any group.
- Show loading, empty, offline, and error presentations without changing routes.
- Refresh deterministically from the selected scenario.

### Search

- Search sessions, projects, transcript prose, tool labels, and task titles.
- Update results locally as the query changes.
- Show result type and enough context to understand the match.
- Open the matching session and focus the represented transcript item.
- Show useful empty-query and no-result states.

### Conversation

- Render user messages, assistant prose, compact activity, structured questions, errors, and attachments.
- Keep assistant prose readable and subordinate protocol detail.
- Expand and collapse a tool call to reveal deterministic arguments and output.
- Open session work to inspect tasks, subagents, jobs, and usage.
- Enter text in the composer and simulate Send, Steer, Queue, and Stop states.
- Preserve a draft while switching concepts.
- Open voice from the composer.

### Structured questions

- Render single-select and multi-select questions.
- Support notes, fallback, “you decide,” and skip outcomes where the fixture permits them.
- Enable the submit action only when the current fixture is valid.
- Replace the pending question with a deterministic submitted state.
- Restore the scenario through **Reset** in Lab Controls.

### Work and subagents

- Show active, waiting, completed, and failed work.
- Preserve parent-child relationships without exposing raw protocol payloads by default.
- Expand a subagent or job for status, elapsed time, current phase, and concise output.
- Make attention states visible without relying on color alone.

### New session

- Choose a recent project or enter a fixture path.
- Enter a prompt.
- Choose a fixture model and reasoning effort.
- Submit into deterministic starting, success, and failure states.
- On success, navigate to a synthetic conversation with a visible starting state.
- Never create files, run commands, or contact a Hub.

### Settings

- Change system, light, and dark appearance.
- Change fixture voice preferences.
- Open concept selection and Lab Controls.
- Display clearly labeled fictional Hub rows as presentation fixtures only.
- Explain that the application is an offline prototype.

### Voice

- Exercise idle, ready, listening, processing, speaking, interrupted, permission-denied, and error presentations.
- Simulate transcript captions, audio level, mute, stop, and end controls.
- Demonstrate barge-in visually through a deterministic transition.
- Never request microphone or speech permissions and never access audio hardware.

## The Three Concepts

The source study is `.superpowers/brainstorm/91266-1787635811/content/three-mobile-concepts.html`. It contains 24 reference mockups: 12 iOS and 12 Android. The application should preserve each concept's thesis while replacing the deck's drawn phones with responsive, interactive UI.

### Stillwater — Quiet Instrument

**Thesis:** A calm, exact tool that disappears behind the work.

Stillwater uses neutral system surfaces, a restrained spruce accent, hairline grouping, an eight-point rhythm, and minimal decorative motion. Sessions behave as a triage list. The transcript is the primary reading surface. Work detail stays one level down.

- iOS uses large titles, inset grouped lists, native-feeling menus, sheets, and a tab bar.
- Android uses Material 3 top bars, tonal surfaces, a clear New action, modal bottom sheets, and predictive-back-compatible history.
- Motion communicates insertion, resolution, disclosure, and navigation only.
- Its principal risk is appearing generic; typography, state language, timing, Work, and Voice must provide distinction.

### Constellation — Living System

**Thesis:** See the shape of the work, not just its log.

Constellation emphasizes relationships among sessions, tasks, subagents, and jobs. It uses dark dimensional surfaces, restrained luminous connectors, depth, and sparse activity pulses. The transcript remains readable; energy concentrates around active work, attention, and voice.

- Relationships must remain understandable without decorative diagrams.
- Luminous state always has a text or shape equivalent.
- Reduced motion removes pulses and spatial transitions without hiding state.
- Android uses Material hierarchy and system navigation rather than copying iOS glass or tab treatments.
- Its principal risk is visual noise; active effects remain bounded to current work.

### Field Notes — Transcript Studio

**Thesis:** Treat every session as a durable, readable record of work.

Field Notes uses warm paper-like surfaces, editorial hierarchy, strong transcript typography, margin cues, and restrained rust accents. Tools and subagents read as annotated work records rather than chat bubbles or dashboards.

- iOS may use an editorial serif for reading roles while retaining system text for controls.
- Android uses Roboto or the platform sans stack for controls and body roles where a bundled serif would feel foreign or impair scaling.
- Rules, indentation, labels, and whitespace express chronology and nesting.
- Texture remains subtle and must not reduce contrast or suggest a static document.
- Its principal risk is making live work feel passive; active and attention states must remain immediate.

## Fixture and State Architecture

The renderer owns one concept-neutral state model. Concept renderers project that state into different component systems; they do not maintain independent copies of product behavior.

### State slices

- **lab** — concept, platform presentation in browser tests, scenario, reset generation, appearance, text scale, and reduced motion;
- **navigation** — root tab, pushed session, activity presentation, focused search result, and voice destination;
- **sessions** — fixture roster, query, refresh state, and selected session;
- **conversation** — transcript items, expanded disclosures, draft, composer mode, and synthetic turn state;
- **questions** — selected options, notes, validation, and submitted state;
- **work** — tasks, subagents, jobs, expanded rows, and usage;
- **new session** — project, prompt, model, effort, submission, and synthetic result;
- **settings** — prototype-only appearance and voice preferences; and
- **voice** — visual session state, caption, level sequence, mute, and interruption.

Reducers and selectors remain independent of React and the three visual systems. Every asynchronous-looking transition uses an injected deterministic scheduler or an explicit state step. Tests must not wait on arbitrary wall-clock delays.

### Fixture scenarios

Lab Controls exposes at least:

- baseline;
- loading;
- empty;
- offline;
- recoverable error;
- needs attention;
- active multi-agent work;
- waiting for structured answers;
- completed work;
- voice lifecycle; and
- long content and large data.

Display controls include system/light/dark appearance, standard/large/accessibility text, and reduced motion. Scenario selection updates every concept from the same fixture source.

Fixtures contain realistic but fictional names, projects, prompts, models, transcript text, tool arguments, outputs, tasks, and identifiers. They contain no copied secrets, authorization URLs, customer data, or ambient machine paths.

### Persistence and reset

A namespaced browser storage record may persist only:

- selected concept;
- appearance preference;
- text-size simulation;
- reduced-motion simulation; and
- the last Lab Controls scenario.

Do not persist transcript drafts, answers, synthetic sessions, tool disclosures, or voice state. A versioned storage decoder rejects malformed or future records and returns to defaults. **Reset prototype** clears allowed persisted preferences and rebuilds all state from canonical fixtures.

## Platform Adaptation

The packaged application detects the actual WebView platform and renders its corresponding expression. A pure platform adapter maps only `ios` or `android` into navigation, typography, elevation, sheet, dialog, feedback, and safe-area primitives.

A browser test or development build may set an explicit platform override. Packaged builds ignore query-string and storage overrides so an installed Android app cannot accidentally render as iOS, or vice versa.

### Shared requirements

- Use real safe-area environment values; do not draw device frames, status bars, camera islands, or navigation indicators.
- Use semantic HTML and explicit accessible names.
- Meet WCAG AA text contrast and preserve meaning without color.
- Support keyboard navigation in browser tests and assistive navigation in WebViews.
- Keep touch targets at least 44 by 44 CSS pixels on iOS and 48 by 48 on Android where platform components require it.
- Support standard through accessibility text scenarios without clipping controls or hiding primary actions.
- Keep one primary vertical scroll owner per destination.
- Honor reduced motion in every concept.

### iOS expression

Use iOS hierarchy and behavior: large or inline navigation titles as context requires, bottom tabs, grouped lists, trailing navigation actions, sheets, menus, and safe-area-aware bottom controls. Back actions follow navigation-stack order. Controls use iOS spacing and feedback rather than Material elevation.

### Android expression

Use Material 3 hierarchy and behavior: top app bars, navigation bar, tonal surfaces, modal bottom sheets, dialogs, visible state layers, and Android back history. The design may use a prominent New action where the concept calls for it, but the four root destinations remain available and labeled.

## Renderer Architecture

The standalone project owns its full dependency graph.

```text
mobile-concepts/
  package.json
  package-lock.json
  vite.config.ts
  index.html
  src/
    app/
    concepts/
      stillwater/
      constellation/
      field-notes/
    core/
      fixtures/
      state/
      platform/
      navigation/
    lab/
    ui/
    test/
  src-tauri/
    Cargo.toml
    tauri.conf.json
    src/
    gen/
      apple/
      android/
```

The first implementation does not import production `mobile/` renderer modules, stores, services, native plugins, generated native projects, or configuration. If planning identifies a genuinely generic pure TypeScript utility worth sharing, it must move behind an explicit package boundary with tests; a relative import into `mobile/` is not acceptable.

Each concept owns visual components and tokens but consumes the same core selectors and actions. Shared code should cover behavior, accessibility primitives, fixture parsing, and platform contracts. It should not flatten the concepts into one component tree controlled by a large theme object when composition differs materially.

## Native Shell and Security Boundary

The Rust shell exists only to host bundled assets and support packaging.

It must have:

- no production mobile plugin;
- no profile, pairing, Keychain, AppWire, Hub HTTP, QR, microphone, speech, haptic, file-picker, shell, process, or updater command;
- no invoke handlers for product behavior;
- no production mobile entitlements or usage-description strings;
- no copied production capabilities or permissions;
- no remote asset URLs; and
- no runtime dependency on credentials, a Hub, or a network.

The Tauri content security policy sets `connect-src 'none'` and limits scripts, styles, images, fonts, and media to the minimum local sources required by the bundled renderer. Prototype code must not call `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource`, `sendBeacon`, or remote dynamic imports. Tests enforce this boundary with static dependency checks and a runtime network trap.

If generated mobile templates retain a platform network permission required by Tauri or the WebView, documentation must state that fact. The application still must make no outbound request. Verification must not claim permission-level network denial unless the final native manifests prove it.

The lab requests no microphone, speech recognition, camera, photo library, local-network, Bonjour, location, notification, or background-mode permission.

## Build and Package Isolation

`mobile-concepts/` uses its own npm lockfile and Rust lockfile. Commands run from that directory and cannot rewrite `mobile/package-lock.json`, `mobile/src-tauri/Cargo.lock`, or production generated projects.

Both generated native projects derive identity from the concept configuration. Verification inspects the built products, not only source configuration:

- the built iOS application's `CFBundleIdentifier` equals `com.primeradiant.evener.concepts`;
- the installed Android package equals `com.primeradiant.evener.concepts`;
- the display name is `Evener Concepts` on both platforms;
- production Evener remains installed after concept-lab installation; and
- launching, clearing, or uninstalling the concept lab does not alter production Evener data.

No application icon may be confused with production Evener. The concept icon should use a clearly labeled or visually distinct lab treatment while retaining family resemblance.

## Error Handling

Because the lab is deterministic, errors are selected fixture states rather than failures from external systems.

- A malformed fixture or persisted preference fails closed to canonical defaults and emits a development diagnostic.
- An unknown route returns to the concept gallery or Sessions without a blank screen.
- A concept renderer error shows a local recovery surface with **Reset prototype**.
- State transitions reject impossible actions rather than silently inventing data.
- The packaged app contains no retry, reconnect, credential, or server-error behavior that could imply real connectivity.

## Testing Strategy

### Core and renderer tests

Use deterministic unit and component tests for:

- fixture decoding and redaction rules;
- route transitions and Android/iOS back behavior;
- first-launch selection and subsequent concept switching;
- preservation of route and scenario across concept switches;
- permitted preference persistence and full reset;
- search and result focus;
- transcript disclosure;
- composer modes and synthetic turn transitions;
- structured-question validation and submission;
- work hierarchy disclosure;
- new-session success and failure;
- voice-state transitions; and
- malformed storage and renderer recovery.

Run every shared behavior contract against all three concept adapters. A concept cannot pass by omitting a required control.

### Boundary tests

Tests must prove:

- no imports from production `mobile/` code or generated projects;
- no production plugin, command, entitlement, usage description, or application identifier;
- no browser network API use in prototype source;
- runtime network APIs throw during the complete smoke flow without breaking the app;
- CSP blocks connections; and
- fixture and storage records contain no credential-like values.

### Accessibility and geometry

Exercise representative iOS and Android viewports, portrait and at least one landscape case, light and dark appearance, standard and accessibility text, and reduced motion.

Automated checks cover:

- clipping and horizontal overflow;
- safe-area ownership;
- bottom controls above the keyboard viewport;
- one scroll owner;
- minimum target size;
- focus order and visible focus;
- accessible names, roles, selected states, and live-region restraint;
- text contrast; and
- screenshots for each concept's key destinations and states.

Browser screenshot baselines support comparison but do not substitute for installed-app verification.

### Build and install gates

Before completion, run and record:

- frontend formatting, lint, typecheck, unit, component, boundary, and production-build gates;
- Rust format, test, check, and Clippy gates;
- Tauri iOS generation and build;
- Tauri Android generation and build;
- install and launch on an iOS simulator;
- install and launch on the `droidmux` Android AVD;
- a signed physical iOS install when the configured device and signing identity are available;
- a complete interactive smoke flow on both platforms; and
- built-artifact package identity inspection.

A blocked physical or signing gate remains incomplete and must be reported as such. A browser run or compiler success does not count as an installed-app pass.

## Acceptance Criteria

The concept lab is complete only when:

1. one source project produces installable iOS and Android applications named **Evener Concepts**;
2. both built applications use `com.primeradiant.evener.concepts` and coexist with production Evener;
3. one install exposes Stillwater, Constellation, and Field Notes through the concept gallery and persistent switch action;
4. all three implement the shared Sessions, Search, Conversation, Work, Structured Questions, New Session, Settings, and Voice flows;
5. switching concepts preserves the current route and scenario during the run;
6. Lab Controls deterministically exercise normal, loading, empty, offline, error, attention, multi-agent, question, completed, voice, and long-content states;
7. iOS and Android use their own platform navigation and component expressions;
8. large text, reduced motion, dark mode, safe areas, keyboard geometry, touch targets, and semantic accessibility pass the defined checks;
9. the runtime uses only local fixtures and attempts no network, credential, audio, camera, file, or production-data access;
10. production `mobile/` code, configuration, generated projects, package data, and installed application remain untouched by the concept lab; and
11. required test, build, artifact-inspection, and installed smoke gates have exited successfully.

## Explicit Non-Goals

This project does not:

- connect to, discover, pair with, or authenticate to a Hub;
- read or store Hub credentials;
- use AppWire, Hub REST, WebSockets, or production transport;
- scan QR codes;
- access microphone, speech recognition, speech synthesis, camera, photos, files, notifications, location, or haptics;
- execute tools, shell commands, delegates, jobs, or session mutations;
- create real sessions or persist synthetic transcripts;
- test production latency, reliability, security, or provider behavior;
- replace the production mobile client;
- choose the final UX direction automatically; or
- merge the three concepts into a final hybrid during prototype implementation.

The final product direction follows hands-on review of this lab. The earlier recommendation remains: use Stillwater as the foundation, Constellation's work visualization selectively, and Field Notes' transcript typography selectively. That recommendation does not change the requirement to implement all three concepts faithfully in the lab.

## Delivery Sequence

1. Write and review this design specification.
2. Produce a file-level, test-driven implementation plan.
3. Scaffold the isolated Tauri project and prove package identity with the smallest installable shell.
4. Implement the concept-neutral fixtures, state, navigation, platform adapter, and Lab Controls.
5. Implement all three visual systems against the shared behavior contract.
6. Run browser accessibility and geometry verification.
7. Generate, build, install, and smoke-test iOS and Android applications.
8. Inspect final artifacts and verify coexistence before reporting completion.
