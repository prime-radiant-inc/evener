# Purpose-Built Native Mobile V1 Design

**Date:** 2026-08-21  
**Status:** Approved for implementation  
**First platform:** iOS  
**Client:** Tauri 2 with a dedicated mobile React renderer

## Goal

Ship a complete, focused Evener mobile client that feels designed for a phone rather than adapted from a desktop web application.

The app connects to one Evener Hub, pairs by QR code or pasted authorization URL, follows and controls live sessions, starts new sessions, exposes session activity, and supports foreground duplex voice with barge-in. The first build targets iOS. V1 defines a versioned platform-neutral bridge contract; an Android implementation and package remain a separate follow-up.

## Product Decision

The mobile app does **not** embed, wrap, copy, or restyle the existing Hub web renderer. It ships no Dockview shell, desktop rail, pane registry, web routes, desktop settings, or web component CSS.

It may reuse headless infrastructure whose contract is independent of presentation:

- generated AppWire TypeScript types;
- the AppWire handshake, request correlation, heartbeat, and reconnect client;
- pure event reduction and transcript model logic where it has no DOM or web-store dependency;
- Hub REST and AppWire protocol semantics; and
- markdown parsing and sanitization libraries.

Every visible mobile surface, layout, navigation pattern, and interaction is new.

## V1 Scope

V1 includes:

- multiple named Hub profiles with add, edit, remove, and explicit switching;
- private-network HTTP and HTTPS connections;
- QR and pasted-URL pairing with native secret storage;
- a searchable session roster grouped by attention and liveness;
- a purpose-built streaming conversation timeline;
- send, steer, queue, stop, compact, rename, shutdown, and model/effort controls when the thread advertises each capability;
- structured `ask_user` responses;
- image attachments and document/image viewing;
- tasks, delegates, jobs, usage, and session diagnostics;
- new-session creation with project, prompt, model, and reasoning effort;
- foreground native speech recognition and synthesis with barge-in;
- light and dark themes, Dynamic Type-aware sizing, safe areas, reduced motion, and haptics; and
- an unsigned iOS simulator build plus a physical-device verification checklist.

V1 excludes:

- Android packaging;
- offline transcripts or offline mutation queues;
- simultaneous live connections to several Hub profiles (V1 keeps exactly one active connection while retaining every saved profile);
- plugin, marketplace, provider-credential, or Hub administration screens;
- arbitrary workspace file browsing;
- background audio, wake words, or push notifications; and
- direct audio-to-audio model providers.

The excluded web administration features remain available in the Hub web app. They are not mobile parity gaps because V1 defines a different product surface.

## Information Architecture

### App launch and onboarding

With no valid profile, the app opens a full-screen connection flow. The primary action scans a Hub QR code. A secondary action pastes the authorization URL printed by Hub. The screen explains that plain HTTP belongs only on a trusted private network. The same flow opens from the server switcher to add another Hub.

After a successful connection check, the app asks for a short server name, stores a generated profile ID plus non-secret name/origin in app preferences, and stores the capability under that profile ID in native secure storage. It activates the new profile and opens Sessions. A profile failure returns to the same screen without destroying other profiles or redacted diagnostics.

### Root navigation

The root uses a three-item bottom bar:

1. **Sessions** — roster, attention, search, and recent work.
2. **New** — start a session through a short mobile flow.
3. **Settings** — connection, appearance, voice, privacy, and diagnostics.

A conversation pushes above the tab bar as a focused destination in React's history stack. It has an explicit 44-point back action and a short horizontal transition. V1 does not implement an interactive iOS edge-swipe gesture inside the WebView; Android's system back action will map to the same history pop in the Android follow-up. Session-specific secondary content appears in sheets, not permanent sidebars.

The Sessions top bar shows the active server name and opens a server sheet. The sheet lists saved profiles with reachability state and actions to switch, add, edit, or remove. Switching closes the old AppWire generation, clears server-scoped roster/conversation/activity state, activates the selected profile, then probes and hydrates it. Removing the active profile requires confirmation and activates the next saved profile or returns to onboarding.

### Sessions

The roster answers three questions immediately: what needs me, what is working, and what happened recently.

Sections appear only when populated:

- **Needs You** — pending `ask_user`, permission, failure, or other actionable state;
- **Running** — live sessions and their current phase;
- **Recent** — ended or quiet sessions ordered by update time.

Each row contains a title, project, concise state, relative update time, and at most one meaningful status mark. Running rows may show a restrained activity pulse. Search filters title and project locally after the current roster loads. Pull to refresh performs an explicit tree refresh. The app does not reproduce the web rail's hierarchy, pins, archived projects, or desktop row menus.

### Conversation

The conversation is a single vertical timeline with a fixed composer. It optimizes for reading a live agent exchange, not for auditing every wire frame at once.

Visible item families are:

- user messages;
- assistant prose;
- compact tool and system activity;
- steering and lifecycle notices;
- structured questions;
- failures; and
- attachments.

Assistant prose uses a quiet full-width reading column. User messages use compact trailing bubbles. Tool calls collapse into one-line activity cards with icon, label, state, and duration; tapping opens a sheet with arguments, output, and transcript action. Consecutive related tools cluster. Reasoning remains collapsed and clearly labeled. Failures stay inline and actionable.

The timeline follows new content only while the reader is near the bottom. Scrolling upward suspends follow mode and shows a “New activity” pill. Opening a session starts at its latest content. Loading older content preserves the reader's visual anchor.

### Composer

The composer is a bottom dock above the safe area and keyboard. It has a growing text field, attachment action, voice action, current model/effort summary, and one primary send action.

The primary action follows session state:

- idle session: **Send**;
- running session: **Steer**;
- explicit queue mode: **Queue**;
- generating with a stoppable turn: a separate **Stop** action remains visible.

Typed drafts remain per session in memory for the current process and contain no Hub credentials. Image previews are removable before send. Structured `ask_user` replaces the normal composer with native-feeling question cards and a fixed “Send answers” action; it preserves all current option, multi-select, note, fallback, decide, and skip semantics.

### Session activity

A toolbar action opens a medium/large detent sheet with four concise sections:

- **Tasks** — grouped by active, open, and done;
- **Work** — delegates, shell jobs, and watches as a nested activity list;
- **Usage** — tokens, cost, duration, and context pressure;
- **Controls** — compact, interrupt, model/effort, rename, and shutdown actions.

Rows summarize first and disclose detail on tap. Raw identifiers and payloads live behind a diagnostics disclosure. This replaces the web app's permanent tasks, details, and activity panes.

`ThreadCapabilities` is authoritative. V1 implements `send`, `steer`, `queue`, `interrupt`, `compact`, `changeModel`, `rename`, and `shutdown`; reasoning effort appears only when the thread reports supported levels. A false capability removes the primary action and leaves an “Unavailable for this source” explanation in Controls. If the server rejects a formerly advertised action, the app shows the structured reason, restores any draft, refreshes thread capabilities, and does not retry automatically.

### New session

New session is a short, progressive form:

1. choose or type a recent project path;
2. enter the initial prompt;
3. optionally change model and reasoning effort;
4. review provider or launch errors inline; and
5. start and navigate directly to the new conversation.

Advanced launch configuration stays in the Hub web app. The mobile flow uses Hub defaults for omitted fields.

### Settings

Settings includes only mobile concerns:

- saved Hub servers with add, edit, remove, switch, reconnect, and re-pair actions;
- system, light, or dark appearance;
- voice, speech rate, and automatic speaking preferences;
- permission status and links to system settings;
- mobile build, Hub, mobile API/AppWire, and connection diagnostics; and
- privacy and private-network warnings.

## Visual and Interaction Language

The app uses a dedicated design system under `mobile/src/ui`. It does not import web tokens or web components.

Principles:

- use the system font stack and respect Dynamic Type scaling;
- use semantic colors with strong light/dark contrast;
- keep the transcript visually quiet and reserve color for state and action;
- use 44-point minimum targets and generous edge spacing;
- honor top, bottom, and keyboard safe areas;
- use bottom sheets, segmented controls, disclosure rows, swipe actions, and navigation-stack transitions where they fit platform expectations;
- use spring motion sparingly and disable it under reduced motion;
- use haptics for connection success, send, stop, voice start/stop, and errors, never for streaming noise; and
- keep destructive actions explicit and confirm only irreversible operations.

The bottom bar, navigation bar, sheets, composer, list rows, and controls share one spacing and radius system. The UI avoids floating desktop cards, tiny icon-only targets, nested scroll regions, and hover-dependent behavior.

The iOS bridge reports the current `UIContentSizeCategory` and change notifications. The renderer maps that category to a bounded semantic CSS type scale instead of assuming WebView text automatically follows Dynamic Type. Layout tests exercise every supported category through the accessibility sizes.

## Architecture

The app lives in a new top-level `mobile/` project in the `native-mobile-v1` worktree. Its display name is `Evener`, and its bundle identifier is `com.primeradiant.evener`.

### Dedicated renderer

`mobile/src` is a new React/Vite application. It owns:

- navigation and screen composition;
- the mobile design system;
- roster, conversation, activity, new-session, onboarding, settings, and voice UI;
- screen-local and app state;
- presentation projections from protocol models; and
- accessibility and mobile geometry.

It may import only the approved headless modules from `cmd/evener-hub/frontend/src/protocol`. A build-time boundary test rejects imports from `shell`, `panes`, `widgets`, `styles`, `stores`, `notifications`, or the web `App` entry point.

The first implementation should reuse `protocol/client.ts`, `transport.ts`, `types.gen.ts`, `errors.ts`, and pure reducer/model code that passes the dependency boundary. If a candidate pulls in web state or DOM presentation, mobile implements a small protocol projection instead of extracting a broad shared framework.

### Mobile application state

Small independent stores own:

- **connection** — saved profile summaries, active profile ID, health, compatibility, switching, and reconnect state;
- **roster** — tree snapshot, search, refresh, and attention groups;
- **conversation** — one active thread projection, paging, pending mutations, follow mode, and draft;
- **activity** — tasks, jobs, delegates, usage, and sheet selection;
- **voice** — permission, capture, recognition, speech queue, and barge-in state; and
- **preferences** — theme and voice choices.

Stores consume typed service interfaces. They do not invoke Tauri commands directly. Tests substitute fake services and deterministic clocks.

### Native Hub transport

The bundled renderer uses Tauri's normal asset origin. It never connects directly to Hub and never receives the saved capability.

Rust owns Hub HTTP and WebSocket connections. It provides a narrow Tauri plugin interface:

- parse and validate an authorization URL;
- scan and pair;
- list redacted profiles, select one active profile, and read its health state;
- rename, replace credentials for, or remove a profile;
- perform an allowlisted relative Hub HTTP request;
- open one AppWire stream;
- send an AppWire text frame;
- close the stream; and
- fetch or upload bounded attachment bytes.

The HTTP command accepts only relative paths under the known Hub API and document/image routes, recognized methods, and bounded bodies. It removes caller authorization and cookie headers, injects the capability natively, rejects redirects to another origin, and redacts request diagnostics.

The AppWire command opens the upstream `/rpc` WebSocket with bearer authentication and the Hub Origin. Rust returns an ordered Tauri channel. A TypeScript `WebSocketLike` adapter converts channel messages and close/error records into the existing `AppwireClient` interface and identifies itself as `evener-mobile`. This preserves the tested handshake, heartbeat, correlation, timeout, and reconnect behavior without exposing a browser socket or token.

One Rust AppWire socket belongs to the active Hub profile. The roster and active conversation share its notification bus; tree-change notifications refresh the roster, while thread notifications route by source/thread reference. Opening or switching conversations requests different thread data but does not open a second socket. Switching profiles closes and invalidates the prior socket before any request for the new profile. Profile ID, connection generation, and conversation generation jointly reject late frames or hydration from another server/session.

Backpressure is explicit: the Rust reader has a bounded queue, preserves frame order, and closes with an overload error rather than dropping protocol frames. Reconnect creates a new connection generation and re-subscribes the roster and active conversation from authoritative state.

### Pairing and secure storage

The Hub web settings page gains an authenticated, no-store mobile-pairing QR. It uses a configured `mobile_base_url` when present or a safe non-loopback request origin. It refuses to emit a loopback QR that a phone cannot reach and applies the same scheme/address policy as the app.

The accepted authorization URL grammar is `http(s)://host[:port]/auth?token=<token>` with an optional `next` query. The parser rejects userinfo, fragments, other paths, unknown query keys, empty or non-base64url 256-bit tokens, and noncanonical ports. Before saving anything, the app shows the normalized scheme, host, and port for confirmation, probes `/api/health` without credentials, then probes an authenticated harmless endpoint without following redirects. The confirmed scheme/host/port becomes the immutable origin for every later request until the user re-pairs.

HTTPS may target a public or private origin. Production HTTP accepts only addresses in IPv4 loopback/RFC1918/link-local/CGNAT (`100.64.0.0/10`) or IPv6 loopback/link-local/unique-local ranges; release builds reject loopback because it cannot name the Hub host from a physical phone. Hostnames must resolve entirely into the allowed set. Rust resolves before each new HTTP or WebSocket connection and connects to the validated address while retaining the original Host header; redirects are disabled. Tests cover mixed public/private answers, IPv4, IPv6, `.local`, rebinding between connections, and forbidden redirects. Debug simulator builds may opt into loopback explicitly.

Each profile has a random UUID profile ID. On iOS, the native bridge stores its Hub capability in Keychain service `com.primeradiant.evener.hub` under account `profile:<uuid>` with after-first-unlock, this-device-only accessibility. Ordinary app preferences contain only ordered `{id, name, origin}` summaries and the active profile ID. Rust retrieves only the active profile's capability when creating an upstream request. Removing or re-pairing a profile deletes/replaces only that Keychain item.

Paste pairing transiently places the user-supplied URL in the connection field. The field clears immediately after native submission and is never persisted or logged. QR scanning completes pairing in the native layer and returns only redacted success or error data to React.

Hub's V1 capability is long-lived and replayable until the operator rotates it; QR scanning does not make it one-time. The onboarding copy states that fact. Adding/editing uses a two-phase native preview and atomic save: the old profile remains usable until the new authenticated probe and Keychain write both succeed. Names must be nonblank and unique case-insensitively; origins may repeat only when the user explicitly confirms a second credential/profile for that server.

Private-network HTTP is supported as requested and labeled clearly. The app supplies the iOS local-network usage description and the narrow transport exceptions needed for private HTTP. Anyone who can observe that network can observe the bearer capability; the UI does not imply otherwise.

### Contract compatibility

The mobile app and Hub share an explicit `mobile_api_version`. Hub exposes it in `/api/health`; mobile embeds the version it implements. Pairing requires an exact V1 match. AppWire's initialize handshake remains the stream-level check.

The mobile version covers the REST DTOs and actions that V1 consumes. Contract fixtures generated from Go `hubapi` types drive TypeScript decoder tests. Adding or changing a consumed field must update the fixture and decoder in the same change.

This contract is narrower than the web frontend hash. Hub and mobile may ship different renderers and asset revisions as long as their mobile API and AppWire versions agree.

## Data Flow

### Roster

1. The roster service asks Rust for `/api/tree`.
2. Rust authenticates and returns the body and status without the capability.
3. A strict mobile decoder maps the DTO to roster entities.
4. The roster store groups entities by attention and liveness.
5. AppWire tree-change notifications debounce a refresh.

### Conversation

1. Selecting a profile or foreground reconnect creates that active profile's shared AppWire connection.
2. Opening a session creates a conversation generation and requests its thread data through that connection.
3. Pure reducer logic builds a thread model.
4. A mobile projector maps items into the small visible item families.
5. React virtualizes and renders those items with the dedicated timeline components.
6. Mutations use AppWire methods and update through authoritative notifications; optimistic state is limited to drafts, pending sends, and attachments.

### Attachments and documents

Uploads accept at most eight JPEG, PNG, WebP, or HEIC images, each at most 8 MiB after HEIC-to-JPEG normalization. A native photo picker copies the selected item into an app-private, file-protected temporary file and returns an opaque handle, never a filesystem path, to React. Rust reads the handle, streams the bounded bytes into the existing attachment payload, and deletes the file on success, cancellation, session change, backgrounding, or a 15-minute cleanup deadline.

Authenticated viewing supports JPEG, PNG, WebP, and GIF images up to 20 MiB, plus UTF-8 plain text and Markdown up to 2 MiB. V1 rejects HTML, PDF, executable, archive, and unknown binary content with an explicit unsupported-format row. Rust returns validated binary responses through `tauri::ipc::Response`; React creates revocable object URLs for images and sanitizes text before rendering. Cancellation aborts the upstream request, and object URLs are revoked on close, session change, or backgrounding.

### Content safety

Every Hub, user, agent, tool, filename, and diagnostic string is untrusted. Components render plain text unless a field is explicitly assistant Markdown. Markdown passes through DOMPurify with a fixed allowlist; raw HTML, forms, embedded media, styles, and event attributes are removed. Links allow only `http` and `https`, display their destination, and open through the native external-browser policy. Markdown images never load automatically; authenticated attachment components own all media fetches. Tool arguments and output render as escaped text. Tests include raw HTML, script/event attributes, `javascript:` and `data:` URLs, malicious filenames, malformed Unicode, and oversized content.

### Local data and diagnostics

V1 stores no transcript cache and no draft across process termination. Typed and voice drafts are keyed by `{profileId, sessionRef}` in memory, survive an ordinary foreground reconnect or profile switch during the process, and clear when that profile is removed; speech captions clear when voice mode ends or the app backgrounds. App-private attachment files use complete-until-first-authentication file protection and the deletion rules above. Preferences contain only theme, voice choices, ordered redacted profile summaries, and the active profile ID.

Diagnostics use a 200-entry in-memory ring of timestamps, redacted profile IDs, operation names, status classes, byte counts, connection generations, and opaque error identifiers. They never include URL queries, authorization/cookie headers, request or response bodies, transcript text, filenames, attachment bytes, speech text, or provider payloads. Export applies the same allowlist. Entries for a removed profile are deleted; the ring clears on process termination; native crash reporting is disabled in V1.

## Voice Interaction

### Voice mode

The session composer microphone opens a focused full-screen voice surface rather than squeezing controls into the timeline. It shows:

- the current listening/speaking state;
- a restrained waveform or level response;
- live user captions;
- the latest assistant sentence;
- a clear end button; and
- an immediate keyboard fallback.

Voice mode does not hide session state: a compact title and connection indicator remain visible. Haptics mark start, accepted utterance, barge-in, and end.

### Native bridge

A Swift Tauri plugin wraps:

- `AVCaptureSession` for QR capture;
- `SFSpeechRecognizer` and `AVAudioEngine` for streaming recognition;
- `AVSpeechSynthesizer` for output; and
- `AVAudioSession` in play-and-record voice-chat mode for routing and available voice processing.

`mobile/src/native/contract.ts` defines bridge version 1 as discriminated command, response, and event unions for secure storage, QR, permissions, speech, synthesis, haptics, app lifecycle, and content-size category. The Rust plugin mirrors and validates that schema; the Swift implementation is the only V1 platform backend. A contract fixture test decodes every variant in TypeScript, Rust, and Swift. Android is deferred at this implementation boundary rather than assumed to match Swift internals.

The plugin exposes typed commands and an ordered event channel. Speech, synthesis, audio-session, and scanner APIs sit behind Swift protocols so tests can replace them.

### Turn semantics

Partial recognition stays in a separate voice draft. A final utterance:

- starts a turn when the session is idle;
- steers when a turn is active;
- never silently queues; and
- is disabled while a structured `ask_user` response is active.

The voice coordinator speaks only new assistant prose in the active conversation after voice mode begins. It excludes reasoning, tool output, system notices, replayed history, and other sessions. It strips markdown to speakable text, chunks at sentence boundaries, and records consumed offsets by session, turn, and item identity. A complete sentence queues within 250 ms of its text delta and begins within 750 ms when the synthesizer is idle. Replayed deltas never speak a consumed range twice.

### Barge-in

The microphone remains active during synthesis. The native bridge emits barge-in only after input voice activity and a new non-empty recognition partial occur after synthesis begins. The bridge must invoke synthesis stop within 250 ms of that combined signal and clear queued speech before it emits the barge-in event. The recognized text becomes the next voice draft and steers only after finalization; no partial text becomes a mutation.

Voice processing reduces echo but cannot guarantee cancellation on every speaker or Bluetooth route. The UI always provides an explicit stop control. Physical-device tests cover the supported routes.

### Lifecycle and privacy

V1 validates `en-US` recognition and synthesis on iOS 17 or later. Other installed locales may work when `SFSpeechRecognizer` reports availability, but they are not V1 acceptance targets. Built-in speaker is required; wired and Bluetooth routes are recorded when the test device provides them. Voice processing reduces echo but does not make an echo-cancellation guarantee.

V1 voice is foreground-only. Backgrounding, calls, route loss, permission changes, or unrecoverable recognition errors stop voice cleanly while typed interaction remains available. Apple speech recognition may use Apple networking depending on language, device, and system support; V1 does not claim offline recognition.

Permissions are requested at first use, not launch. Camera, microphone, speech, and local-network denial each produce a focused explanation and system-settings action. The generated iOS project targets iOS 17.0 or later and declares nonempty `NSCameraUsageDescription`, `NSMicrophoneUsageDescription`, `NSSpeechRecognitionUsageDescription`, and `NSLocalNetworkUsageDescription` values. Its audio session enables the Bluetooth route options supported by that deployment target. A build test reads the final app Info.plist and fails when the deployment target or any required usage string is missing.

### App lifecycle

On backgrounding, the app stops voice, closes the AppWire socket, cancels in-flight HTTP and attachment transfers without retry, revokes object URLs, deletes attachment temporaries, and marks the active conversation stale. It preserves only in-memory typed drafts if the process survives suspension; termination loses them by design. No user mutation is retried automatically.

On foregrounding, Rust re-reads saved summaries, the active profile ID, and that profile's Keychain item; probes health; opens a new profile/connection generation; refreshes the roster; and rehydrates the active conversation before enabling mutations. Visible authenticated images are refetched on demand. Deterministic lifecycle tests cover stale cross-profile events, suspended transfers, surviving drafts, terminated-state cold start, and reconnect failure; one physical-device check backgrounds and resumes during a live session.

## Error Handling

- **Hub unreachable:** keep the redacted profile, show reconnect/re-pair/switch actions, preserve local drafts, and leave other profiles usable.
- **Invalid or rotated capability:** stop requests for that profile and show re-pair or switch without echoing/logging the credential or invalidating other profiles.
- **Mobile API or AppWire mismatch:** stop with a precise update-required screen; do not retry identical handshakes.
- **Malformed DTO:** keep the last good roster or conversation, show a compatibility error, and record only bounded redacted diagnostics.
- **Stream overload or disconnect:** close the generation, retain authoritative content, and use the existing reconnect backoff.
- **Mutation conflict:** restore the draft and explain the current allowed action; never auto-retry a user mutation with changed semantics.
- **Voice unavailable:** disable voice and preserve all typed controls.
- **Native plugin failure:** isolate the failed feature and provide a redacted diagnostic identifier.

## Testing

Implementation must read and follow `docs/developing-evener/testing.md` before adding tests.

### Boundary tests

A static dependency test starts from every mobile entry point and recursively walks Vite's resolved module graph, including dynamic imports, workers, CSS, and assets. The only allowed paths under `cmd/evener-hub/frontend/src` are an exact file allowlist in `mobile/protocol-imports.json`; every other web source path fails the build. A production Vite metafile test repeats the check against resolved bundle inputs and requires all renderer entry points to originate under `mobile/src`. Package-name or output-string scanning alone is not accepted as proof.

### TypeScript

Vitest and Testing Library cover:

- onboarding and redacted pairing errors;
- roster grouping, search, refresh, and last-good behavior;
- timeline projection for every visible item family;
- paging and visual-anchor preservation;
- follow mode and new-activity behavior;
- send, steer, queue, stop, conflict recovery, attachments, and drafts;
- structured questions and exact answer composition;
- activity-sheet grouping and controls;
- new-session validation and submission;
- voice state, sentence chunking, replay de-duplication, and barge-in; and
- accessibility names, focus movement, reduced motion, and theme behavior.

A browser fixture mode runs the dedicated mobile renderer with fake native and Hub services. The required portrait matrix is 375×667 (small), 393×852 (standard), and 430×932 (large), plus 852×393 landscape for conversation and voice. It runs at default, Extra Extra Large, and Accessibility Extra Extra Extra Large semantic type scales; both themes; reduced motion; zero and representative notch/home-indicator safe-area insets; and closed plus 320-pixel keyboard states.

At every matrix point, tests require no document-level horizontal overflow, no clipped focused control, one vertical scroller per screen, a visible composer/primary action above the keyboard, scrollable sheets within the visual viewport, and a measured 44×44 CSS-pixel minimum hit box for every interactive target. Focused inputs must scroll into view, timeline paging must preserve its anchor within 2 CSS pixels, and the new-activity control must remain reachable. Browser tests retain screenshots for review but assert these geometry contracts rather than pixel identity.

### Rust

Unit and integration tests cover:

- authorization URL parsing and redaction;
- multi-profile add/edit/remove/switch lifecycle and atomic Keychain replacement;
- API path, method, body, and redirect allowlists;
- bearer injection and browser credential stripping;
- HTTP status and binary-body fidelity;
- WebSocket authentication, ordering, close codes, reconnect generations, overload behavior, and cancellation;
- Keychain adapter behavior through a fake secure store; and
- cleanup of sockets, channels, object resources, and temporary files.

Local scripted HTTP and WebSocket servers provide deterministic transport fixtures. Default tests use no live Hub, provider, credential, or network service.

### Hub and Go

Hub tests cover `mobile_base_url`, authenticated no-store QR generation, loopback refusal, `mobile_api_version`, and stable DTO fixtures. Existing Hub and AppWire tests remain green.

### Swift and iOS

Deterministic XCTest coverage drives scanner, speech, synthesis, and audio state through protocol fakes. It covers permission outcomes, partial/final recognition, synthesis queues, voice activity, barge-in, interruptions, route changes, backgrounding, and cleanup.

The build must produce an unsigned simulator app. A physical-device checklist on an iOS 17-or-later iPhone verifies QR scanning, local-network permission, microphone and speech permission, `en-US` captions, sentence start timing, 250 ms barge-in stop timing, no duplicate spoken chunks, built-in speaker behavior, available wired/Bluetooth routes, calls/route changes, background/resume, keyboard fallback, rotation, Dynamic Type, and safe areas. Timing uses timestamped bridge events and a screen recording; unavailable optional routes are marked unavailable rather than passed. Simulator success is not evidence for physical audio behavior.

### Repository gates

Required final gates are:

- mobile Biome, TypeScript, unit, bundle-boundary, and browser geometry checks;
- Rust formatting, Clippy, and tests;
- Swift/Xcode tests;
- a Tauri iOS simulator build;
- focused Hub Go tests;
- `make lint`, `make vet`, and `make test`; and
- `git diff --check`.

A gate counts only when it runs and exits zero. `docs/verification/native-mobile-v1.md` records the verified commit, date, host/Xcode/Rust/Node versions, simulator device/runtime, each command and exit status, and every physical checklist result. Review screenshots and timestamp logs live under `docs/verification/native-mobile-v1-assets/` with no credentials, transcripts, filenames, or speech text. A missing required record is an incomplete gate, not a pass.

## Delivery Slices

Implementation stays in the `native-mobile-v1` worktree and lands in reviewable slices.

1. **Foundation:** scaffold Tauri, native transport, secure multi-profile store, shared protocol boundary, fixture mode, and mobile design tokens.
2. **Onboarding and shell:** QR/paste pairing, server switcher/editor, root navigation, Sessions, New, and Settings shells.
3. **Conversation:** roster service, AppWire adapter, timeline projection, streaming UI, paging, composer, asks, attachments, and controls.
4. **Activity:** tasks, delegates, jobs, usage, diagnostics, and session action sheets.
5. **Voice:** Swift bridge, full-screen voice UI, synthesis coordinator, lifecycle, and barge-in.
6. **Hardening:** accessibility, geometry, simulator build, physical-device checklist, full gates, and independent review.

Each slice must pass focused tests before the next begins.

## Acceptance Criteria

V1 is complete when:

1. The bundled app contains a dedicated mobile renderer and the dependency/bundle tests prove it contains no Hub web renderer or desktop shell.
2. An iPhone or simulator can add at least two named servers by native QR scan or pasted authorization URL, edit/re-pair/rename/remove them, and switch the single active connection without exposing either saved capability outside native secure storage.
3. For each selected server, Sessions loads that server's Needs You, Running, and Recent groups with search, refresh, and actionable status; late data from the previously selected server cannot appear.
4. A user can open a session, page history, follow live AppWire output, inspect tools/activity, answer structured questions, attach an image, and use every advertised V1 action: send, steer, queue, interrupt, compact, change model/effort, rename, and shutdown. Unsupported actions explain their source capability without sending a request.
5. A user can start a session with project, prompt, model, and effort, then land in its live conversation.
6. Tasks, delegates, jobs, usage, bounded redacted diagnostics, and lifecycle controls work through mobile sheets without desktop panes.
7. The required viewport/type/theme/motion/safe-area/keyboard matrix passes every overflow, hit-target, focus, scrolling, composer, and anchor assertion.
8. On a physical iOS 17-or-later iPhone using `en-US`, recognition can start or steer a session, a completed assistant sentence begins speaking within 750 ms when idle, consumed text never repeats, and barge-in stops synthesis within 250 ms before a finalized steer.
9. Voice or native-feature failure never disables typed session control.
10. Focused tests, the simulator build, repository gates, and the physical-device checklist pass with commit-bound evidence in `docs/verification/native-mobile-v1.md` and its redacted assets.
