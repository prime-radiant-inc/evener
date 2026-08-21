# Native Mobile V1 Design

**Date:** 2026-08-21  
**Status:** Approved for implementation  
**First platform:** iOS  
**Client:** Tauri 2 with the existing React/Vite frontend

## Goal

Ship an iOS Evener client that connects to one Evener Hub, includes the full current web product, pairs by QR code or pasted Hub authorization URL, and supports foreground duplex voice with barge-in.

The app must reuse the web renderer rather than create a second UI. It must keep the Hub capability out of JavaScript, preserve AppWire behavior, and leave a platform-neutral bridge that Android can implement later.

## V1 Scope

V1 includes:

- one active Hub profile;
- private-network HTTP and HTTPS Hub connections;
- QR and pasted-URL pairing;
- every screen and action in the current web frontend while connected;
- the existing REST and AppWire reconnect behavior;
- native streaming speech recognition and speech synthesis;
- barge-in that stops speech and steers a running session; and
- an iOS simulator build plus a real-device verification checklist.

V1 excludes:

- Android packaging;
- offline data or mutation queues beyond existing web behavior;
- simultaneous connections to several Hubs;
- background audio, wake words, or push notifications;
- direct audio-to-audio model providers; and
- silent compatibility with a different Hub frontend build.

## Architecture

The app lives in a new top-level `mobile/` project. Development and commits occur in the `native-mobile-v1` worktree.

The mobile build consumes `cmd/evener-hub/frontend` directly. It does not copy the React tree, styles, reducer, stores, or generated AppWire types. Mobile-only surfaces join that frontend behind a runtime capability check, so the Hub and mobile app still produce the same Vite bundle.

At launch, the Tauri Rust core starts an ephemeral loopback gateway before it creates the WebView. The WebView loads the gateway origin. The gateway serves the bundled Vite assets and proxies all other HTTP and `/rpc` WebSocket traffic to the paired Hub.

This arrangement preserves the frontend's same-origin contract. Existing relative fetches, image and document URLs, OAuth device flows, attachment uploads, and `rpcURLFromLocation` continue to work without a second mobile API client.

### Components

#### Shared React frontend

The shared frontend keeps ownership of rendering, navigation, state, AppWire handshakes, reconnection, reducers, session actions, and responsive layout. It gains:

- a native-only connection route;
- a settings surface that displays the Hub pairing QR code;
- a native capability adapter;
- a voice-mode control in the session composer; and
- transient recognition and speech status UI.

The web build sees no voice control unless the native bridge reports that voice is available.

#### Tauri Rust core

The Rust core owns:

- application and WebView lifecycle;
- strict authorization-URL parsing;
- one non-secret Hub profile;
- Keychain access for the Hub capability;
- loopback gateway startup and reconfiguration;
- external-navigation policy;
- typed commands and events for the frontend; and
- redacted diagnostics.

The core never returns the saved Hub capability to frontend code.

#### Loopback gateway

The gateway has two responsibilities: serve the exact bundled frontend and act as an authenticated reverse proxy to the Hub.

For HTTP, it:

- streams request and response bodies;
- removes inbound browser cookies and authorization headers;
- removes hop-by-hop headers;
- injects `Authorization: Bearer <capability>` upstream;
- rewrites the upstream `Origin` and `Referer` to the selected Hub where the Hub's same-origin policy requires them;
- rewrites same-Hub `Location` responses to the loopback origin;
- leaves external redirects external; and
- never logs credentials or body content.

For `/rpc`, it:

- validates the WebView's loopback origin;
- opens the upstream Hub WebSocket with bearer authentication and the Hub origin;
- relays text frames, close codes, and cancellation in both directions; and
- closes the peer promptly when either side ends.

The gateway does not interpret AppWire messages. The existing TypeScript client remains the protocol authority.

#### iOS native bridge

A narrow Swift bridge owns camera, speech, synthesis, and audio-session APIs. It exposes typed commands and events rather than application state. Android can later implement the same contract.

The bridge wraps:

- `AVCaptureSession` or the selected native scanner for QR capture;
- `SFSpeechRecognizer` and `AVAudioEngine` for streaming recognition;
- `AVSpeechSynthesizer` for speech output; and
- `AVAudioSession` in play-and-record voice-chat mode for routing and echo control.

## Pairing

### QR flow

An authenticated Hub settings card renders a no-store QR image. The QR encodes the same `/auth?token=...` authorization URL that Hub already prints at startup. The endpoint that creates the image requires normal Hub authentication and returns `Cache-Control: no-store`.

The iOS scanner sends its result directly to Rust for parsing and storage. Frontend code receives only success, sanitized Hub identity, or a redacted error.

### Paste flow

The connection screen accepts a pasted Hub authorization URL. It passes the value once to Rust. Rust:

1. accepts only `http` or `https` URLs;
2. rejects userinfo, fragments, unknown paths, missing tokens, and malformed hosts;
3. extracts and removes the `token` query value;
4. normalizes the Hub base URL;
5. checks unauthenticated `/api/health` reachability;
6. checks authenticated access through a harmless endpoint;
7. compares frontend identities; and
8. stores the base URL in app preferences and the capability in Keychain.

The paste field clears after submission. Errors never echo the token.

### Private HTTP

Private-network HTTP is a supported V1 mode, as requested. The connection UI labels it "Private network only" because any party that can observe that network can observe the bearer capability. The app adds the iOS local-network usage description and the narrow transport exceptions required for private HTTP. HTTPS remains supported without a separate code path.

## Loopback Security Boundary

The gateway binds only to `127.0.0.1` on an operating-system-assigned port. It creates a new random session secret on each launch and establishes an HttpOnly, SameSite cookie for its WebView.

The gateway:

- accepts mutating HTTP requests only from its exact loopback Origin and Host;
- accepts WebSocket upgrades only from that origin;
- emits no permissive CORS headers;
- rejects requests before pairing except static connection assets and local status;
- strips Hub `Set-Cookie` headers because bearer injection replaces Hub cookies;
- supplies a restrictive CSP for bundled assets;
- prevents WebView navigation to arbitrary remote pages and opens approved external links in the system browser; and
- grants Tauri commands only to the loopback WebView origin.

A hostile web page may probe loopback ports, but it cannot read responses through CORS, send accepted mutations, open an accepted WebSocket, obtain the per-launch cookie, or reach the Hub capability.

## Compatibility

`/api/health` already reports the Hub frontend hash. The mobile app embeds the exact standard frontend build and its hash. Pairing requires equality between the embedded hash and the Hub hash.

This strict V1 rule makes full parity falsifiable: the bundled reducers, REST assumptions, generated types, and Hub handlers come from the same source revision. A mismatch produces an actionable screen that shows redacted app and Hub identities and asks the user to update one side. It does not attempt partial compatibility.

The normal AppWire initialize handshake remains a second protocol check. A protocol rejection stays terminal until the app or Hub changes.

## Full-Parity Data Flow

After pairing:

1. React requests a relative REST path or opens the relative `/rpc` WebSocket.
2. The loopback gateway validates the local request.
3. The gateway maps the path to the selected Hub and injects the Keychain capability.
4. Hub handles the request exactly as it handles the web app.
5. The gateway streams the response back without interpreting product data.
6. Existing React stores and reducers update the UI.

SPA fallback, static assets, documents, images, attachments, searches, settings, credentials, plugins, spawn, session controls, and AppWire notifications follow this path.

External authorization pages open in the system browser. Existing device-code and paste-back OAuth flows remain the product flow; the gateway does not capture provider credentials.

## Voice Interaction

### User experience

A microphone control appears in the session composer only when the iOS bridge reports permission and support. Tapping it enables foreground voice mode. Voice state is explicit: idle, listening, recognizing, speaking, interrupted, or unavailable.

Partial recognition appears as a separate voice draft. It never overwrites a typed draft. A final utterance uses existing session semantics:

- when no turn is running, it starts a turn;
- when a turn is running, it steers that turn;
- when a structured `ask_user` dock is active, voice submission is disabled and the structured controls remain authoritative.

### Assistant speech

A frontend voice coordinator observes only new assistant-message text in the active session. It does not speak reasoning, tool calls, system notices, history loaded on reconnect, or messages from inactive sessions.

The coordinator keys progress by session, turn, and item identity. It converts markdown to plain speakable text, queues complete sentence-sized chunks, and marks consumed offsets so replayed or reconciled deltas do not repeat.

### Barge-in

The microphone remains active while synthesis plays. The bridge configures the audio session for voice chat and enables available voice processing. When recognition reports a credible user partial during playback, the bridge immediately stops the current utterance and clears queued speech. The frontend keeps the recognized text as the next voice draft; its final form steers the running turn.

Voice processing reduces speaker echo but cannot prove perfect echo cancellation on every route. Real-device tests cover the built-in speaker, receiver, wired audio when available, and Bluetooth. The UI always offers an explicit stop control.

### Lifecycle and privacy

V1 voice works only in the foreground. App backgrounding, phone calls, audio-route loss, permission changes, or unrecoverable recognition errors end voice mode cleanly. Typed interaction and the AppWire connection remain active.

Apple's speech recognizer may use Apple networking depending on device, language, and system availability. V1 does not claim offline recognition. The app requests microphone, speech-recognition, camera, and local-network permissions only when the corresponding feature first needs them.

## Error Handling

- **Hub unreachable:** keep the profile, show the connection screen and retry action, and preserve existing AppWire backoff after reconnection.
- **Invalid or rotated capability:** stop proxying authenticated traffic and return to pairing; do not expose or automatically delete diagnostic metadata.
- **Frontend hash or AppWire mismatch:** stop with an update-required message; do not retry identical handshakes.
- **Gateway failure:** show a local fatal screen with a redacted error and cleanly stop the WebView connection.
- **Upstream body or WebSocket failure:** cancel the opposite direction and preserve the original status or close code when safe.
- **Voice permission denied or unavailable:** disable voice with a settings hint; keep every typed feature usable.
- **Speech or synthesis interruption:** stop voice state and retain any unsubmitted voice draft.

## Testing

Before adding tests, implementation must follow `docs/developing-evener/testing.md`.

### Rust

Unit tests cover URL parsing, base normalization, secret redaction, profile serialization, header filtering, Origin and Host validation, redirect rewriting, and shutdown state.

Gateway integration tests use local scripted HTTP and WebSocket servers. They prove bearer injection, browser credential stripping, response streaming, attachment handling, same-Hub redirects, external redirects, close-code propagation, cancellation, origin rejection, cookie enforcement, token non-exposure, and port release.

### Hub and Go

Hub tests prove that the pairing QR endpoint:

- requires authentication;
- uses the request's safe external origin;
- encodes the expected authorization URL;
- sets `Cache-Control: no-store`; and
- never appears in unauthenticated routes or logs.

Existing Hub and AppWire suites must remain green.

### React

Tests use a fake native bridge to cover connection states, paste clearing, redacted errors, capability gating, voice-state rendering, draft isolation, start-versus-steer routing, structured-ask exclusion, sentence chunking, replay de-duplication, and barge-in events.

The existing frontend typecheck, unit suite, Biome gate, overflow guard, layout guard, and real-browser mobile geometry guard remain required.

### Swift and iOS

Speech, synthesis, scanner, and audio session APIs sit behind Swift protocols. Deterministic tests drive the voice state machine with fakes and cover permission results, partial/final events, synthesis queues, interruption, barge-in, route changes, backgrounding, and cleanup.

The build must produce an unsigned iOS simulator app. A real-device checklist verifies camera pairing, microphone permission, partial recognition, streamed speech, speaker echo behavior, barge-in latency, headphones, route interruption, rotation, keyboard coexistence, and safe areas. Simulator success is not evidence for physical audio behavior.

### Repository gates

Required final gates are:

- Rust formatting, Clippy, and tests;
- Swift/Xcode unit tests;
- the Tauri iOS simulator build;
- frontend formatting, typecheck, unit, and browser gates;
- focused Hub Go tests;
- `make lint`, `make vet`, and `make test`; and
- `git diff --check`.

A gate counts only when it runs and exits zero.

## Delivery Slices

Implementation stays in one worktree but lands in three reviewable slices.

### Slice 1: Gateway and runtime compatibility

Create the Tauri project, install the pinned Rust toolchain, embed the standard frontend build, implement profile abstractions and the loopback HTTP/WebSocket gateway, enforce the local security boundary, and boot the existing app in an iOS simulator against a scripted Hub.

### Slice 2: iOS shell, pairing, and parity

Add Keychain storage, QR and paste pairing, the Hub QR settings surface, local-network configuration, external-link handling, compatibility checks, and a route-by-route parity audit of the current web product.

### Slice 3: Native voice

Add the Swift audio bridge, React voice coordinator and controls, deterministic tests, permission copy, foreground lifecycle handling, sentence streaming, barge-in, and the real-device checklist.

Each slice must pass its focused tests before the next begins. The final result must pass every repository gate listed above.

## Acceptance Criteria

V1 is complete when:

1. An iPhone or iOS simulator can launch the bundled app without loading UI code from Hub.
2. A user can pair by scanning the authenticated Hub QR or pasting the startup authorization URL.
3. The Hub capability resides in Keychain and never appears in frontend state, storage, logs, errors, or proxied responses.
4. A matching Hub exposes the full current web product through the loopback gateway, including live AppWire streaming and mutations.
5. A mismatched Hub stops at a clear compatibility error.
6. On a real iPhone, native recognition can start or steer a session, assistant prose speaks as it streams, and barge-in stops speech before steering.
7. Voice failure never disables typed interaction.
8. The simulator build, real-device checklist, focused suites, and repository gates pass with recorded evidence.
