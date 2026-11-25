# Copilot Roster/Auth Transport Boundary Design

## Intent

Stop using a connection's default model generation transport to decide how its roster is fetched, how its connection authenticates, and which endpoint environment is trusted. The three mix-ups caused repeated review defects across custom provider files, model overrides, Settings sign-in, and steering images.

Jesse chose **separate roster and generation transports** rather than Copilot-only exceptions or a new auth service.

## Goals

- A connection's roster listing, roster authority, connection credential status, sign-in actions, and endpoint identity use one roster/connection transport.
- A model's generation transport and credential remain independent and continue to select per-model launch and validation behavior.
- Endpoint-environment exclusions come from the effective trusted provider configuration that launch-check and serve actually load, including alternate provider files and custom `vars_env` mappings.
- Historical images and newly admitted attachments have distinct behavior on text-only targets: older images are projected out of the outbound copy, while the current prompt and steering messages keep their attachments for the provider's actionable refusal.
- AppWire shapes, persisted attachments, transcript history, and user-visible provider workflows stay unchanged outside these corrections.

## Non-goals and anti-goals

- No new provider identities, auth modes, launch services, or public request contracts.
- No Copilot SDK/CLI, GHES, PAT login, or new endpoint configuration surface.
- No per-provider timeout or model-behavior changes.
- No blanket project-environment filtering. Unrelated project env keys must survive.
- No Copilot-only image-history branch or heuristics based on message text.

## Current boundary and why it fails

`listingTransport` resolves the default model's generation transport. That is correct for per-model launch validation but not for the connection's roster or sign-in. A default row can route generation through a different gateway and auth scheme while discovery still belongs to Copilot. Settings can then offer the gateway's API-key action instead of Copilot sign-in, and roster authority can follow the wrong transport.

Endpoint exclusions currently derive from the hub's registry, which can be a different file from the one a child receives. A trusted alternate provider file can therefore contain a custom endpoint mapping that the exclusion scan never sees.

`turnInputCarriesImages` records only the opening prompt. Steering after the turn starts is a new user attachment but is not represented by that flag, so it can be stripped as though it were historical.

## Chosen design

### 1. Roster/connection transport

Introduce a registry helper that explicitly answers the transport used for the connection's roster, separate from generation. Copilot uses its provider-head transport; existing non-Copilot fallback behavior stays unchanged.

Use that transport for:

- `ResolveInstanceListing` and `listLive`, including listing parser and configured models URL;
- `LiveListingAuthoritative` and authority-driven static-row suppression;
- `Instance.Auth`, connection credential source/warnings, and connection endpoint identity;
- `AuthFingerprint` for connection-level listing identity;
- Settings auth modes, status, device start/poll, and logout through the existing instance metadata.

Keep `listingTransport` and its per-model behavior for generation, launch gates, and launch credential validation. `Instance.DefaultModel`, `Instance.Protocol`, and per-model generation credentials do not move to the roster transport.

### 2. Effective-child endpoint trust

`EndpointEnvNames` continues to derive endpoint-feeding names from actual provider configuration: URL templates, `vars_env`, host rules, and `$VAR` references.

Change spawn and resume to derive `ProjectEnvExclusions` from the effective trusted provider file the child will load, not from the hub's registry. Empty/no-user-layer configuration retains the existing built-in exclusions. This catches alternate provider files with custom mappings without blocking unrelated project keys or user/global/trusted/per-launch/parent settings.

Use the same helper at both spawn and resume so the two launch paths cannot diverge.

### 3. Current-turn attachment ownership

Replace the opening-prompt-only image check with the existing `turnHistoryBaseline`, which already identifies the first message of the in-flight turn. User-image parts before that baseline are historical and leave the outbound copy on text-only targets. Messages at or after it belong to the accepted prompt or admitted steering input and keep their attachments for the actionable refusal.

Apply the same ownership in normal requests and continuation fallback. Preserve the original request, transcript turns, and persisted bytes. Supported vision routes remain unchanged.

## Acceptance tests

- Alternate trusted provider file plus custom `vars_env`: project-layer value cannot redirect the child, launch-check, serve, or registry load. Unrelated project keys and trusted overrides survive.
- Default model generation override to a separate API-key gateway: Settings still offers Copilot device sign-in, connection status uses Copilot auth, roster authority stays Copilot, and generation validation uses the gateway credential.
- Two consecutive listings under that override preserve route, visibility, and configured models URL, with no static resurrection on an empty roster.
- Steering an image after a turn starts on a text-only model preserves that new attachment and its actionable refusal; older images are omitted. Opening-prompt refusal, supported Haiku images, and non-Copilot controls still pass.
- Continuation fallback after such a steer has the same image ownership, including mid-turn skill rebuild if the existing harness covers it.

## Size and verification

Target: at most a couple hundred production lines across registry, launchconfig/spawn/resume, and session request shaping, with a larger focused test suite. If the boundary requires more, stop and report measured size and what each part buys before merging.

Run focused RED tests first, then `go test ./llm/... ./cmd/evener`, `go test ./agent -timeout 25m -count=1`, and `go test ./cmd/evener-hub -timeout 25m -count=1`, followed by `make lint`. The live probe credentials are deleted; do not claim a fresh live gate on this boundary without new authorization. Earlier live evidence remains valid only for protocol paths this change does not alter.

## Alternatives considered

- **Copilot-only exceptions:** smaller initially, but leaves generation/connection confusion in place and repeats it for other providers and future custom mappings.
- **New hub auth service:** clean separation, but a much larger rewrite with new APIs and migration risk. The boundary belongs in the existing registry and request-shaping layers.
