# Onboarding browser guard

Run from the repository root:

```sh
make web-preflight
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestOnboarding(Browser|OAuthBrowser|Evidence)' -count=1 -v
```

Requires Node and Chrome. The guards build a disposable production `evener`
binary, serve the embedded production frontend from a real hub, and launch real
daemons through `HubSpawner`. The only scripted services are loopback HTTP
boundaries: an OpenAI-chat provider, a Codex-style model endpoint, and an OAuth
issuer. Endpoint definitions are seeded; no credential, OAuth record, model
selection, global default, session or browser storage is seeded.

The driver imports skillguard's exported Chrome lifecycle/input driver, not its
scenarios or frontend fixtures. All UI actions go through the production app.
The checkpoint URL only lets Node ask Go to inspect the real provider log and
rendezvous before Start, or to make a real `evener/instance/edit` call at the
named OAuth stage. Final checks compare both launch attempts and all generations
with the actual Start activation event, captured synchronously before its React
handler.

## `TestOnboardingBrowser`: automatic key-path journey

Builds the ordinary New-session draft, stages a real image attachment, chooses
the directory, saves an API key through the guided connector, and then waits
without any **Retry**, **Continue** or **Choose model** action. Coverage:

- The fixture withholds healthy model listings until the browser observes the
  connector's own failed `evener/auth/test` reply and reports the checkpoint's
  `auto-check-failed` stage. The connector then retries on its own timer and
  completes; the checkpoint requires both the failure and a healthy retry, and
  no session, process launch or provider generation exists before Start.
- Setup dismisses automatically with no forced model picker or success page.
  The untouched draft adopts the fixture's configured default, which is the
  second listed model (`gpt-4o`), not the first.
- The task text, working directory and one decoded attachment tile survive.
  The provider receives the task and an `image_url` part, and the durable
  transcript contains the user text plus an image part.
- Only after the explicit Start activation does a real daemon run. Exactly one
  streamed turn uses the adopted model and shows `ONBOARDING_REPLY_72e`; a
  second, deliberately blank launch has no user input, reply or completed turn.

The journey runs four times, once per real layout: 1440×1000 and 390×844 in
light and dark. The viewport and emulated system theme are set before any setup
interaction, and nothing in the acquisition capture resizes, clicks or focuses.
`acquisition-automatic-<width>-<theme>.png` and its JSON are taken only after
production has dismissed setup, adopted the configured model, enabled Start and
restored the composer. Focus is asserted there before any driver intervention.
The narrow session uses the visible page text and the rendered session pane's
real ref; desktop keeps the active-dock and transcript identity checks.

The session response is captured as `session-automatic-<width>-<theme>.png`.
Recording-only access views (written after the video) remain presentation
evidence: they navigate and resize, and never contribute to the acquisition or
focus oracle.

## `TestOnboardingOAuthBrowser`: device, redirect, cancel, endpoint change

Each subtest uses the real hub OAuth controller; `hubAuthControllerSetup` only
sets `IssuerBaseURL` and the HTTP client that reaches the scripted issuer. Real
device-code request/poll, token exchange, `auth/openai-codex.json` persistence,
registry reload, AppWire and browser UI remain production code. Subtests:

- `oauth-device`: device usercode, one pending poll, token exchange, automatic
  model-list check, setup dismissal and adoption of the connected instance's
  configured default. The saved record is read back from the real state root
  and the provider saw the OAuth access token. One token exchange and
  model-list access are required; no generation is attempted.
- `oauth-redirect`: the issuer's device endpoint returns 404, the connector
  falls back to the authorize URL, and the driver pastes a callback URL carrying
  the production flow's own `state`. The real code exchange and persistence are
  then verified as above.
- `oauth-cancel`: the issuer observes and holds the first device poll. The user
  cancels the editor; the checkpoint confirms no exchange, record or model
  check yet, then releases a real late authorization success. The in-flight
  request is allowed to reach the token exchange, and the browser waits for the
  real `evener/auth/device/poll` reply before reading the UI: production writes
  that reply only after the exchange, the endpoint check and the credential
  write or refusal have run. The browser's AppWire frame tap is passive: it
  records the frames the production client sends and receives and changes
  neither. Cancelling retires the dialog's ownership, not the already-running
  server-side sign-in: for the unchanged destination the checkpoint requires the
  saved record to match this scripted exchange (provider, access token and
  identity, never printed), and does not require a model-request count. The
  browser still requires no credential-saved UI, no focus change, the same draft
  intent and the retired model state. A fresh sign-in then completes and adopts
  the configured default.
- `oauth-endpoint-change`: while the poll is held, Go performs a real AppWire
  `evener/instance/edit` that re-points the instance. After the same released
  late success, the browser waits for the real poll reply (an error, because the
  changed destination refuses the write) and then must return to the connection
  editor with the `changed` alert and the polling editor gone; the token exchange
  completes, but the checkpoint requires no persisted record and no model-list
  check. The fixture's cleanup releases a held poll before closing its servers,
  and `TestOnboardingOAuthBrowserFixtureCleanupWithHeldPoll` proves that order.

Device and redirect each run in all four real layouts, from before setup opens
through acquisition and automatic focus restoration; no post-completion resize
contributes to those assertions. The redirect tab does not break the matrix
because the app tab stays in front. All three completing paths require the
adopted configured default and an enabled Start, matching
`TestOnboardingBrowser`. The launch-model publication that used to abort
adoption carries its own catalog-only `reason`; credential and instance changes
keep the empty reason and still retire the completion.

## Optional recording

Set `EVENER_ONBOARDING_VIDEO` to an absolute, new `.mp4` path outside source
and put `ffmpeg` with libx264 on PATH. Create its parent directory first. One
recording invocation selects one view, so record `desktop-light` alone and give
each such invocation its own fresh absolute output path:

```sh
EVENER_ONBOARDING_VIDEO=/absolute/session-artifacts/onboarding-desktop-light.mp4 \
  go test -tags browserguard ./cmd/evener-hub -run '^TestOnboardingBrowser/desktop-light$' -count=1 -v
```

This runs the same assertions with finite reading pauses. CDP screencast capture
begins after authentication and records only the page viewport, never the address
bar. Chrome remains headless with its own disposable profile. Frame timestamps
preserve elapsed time, including the pauses, in a 20fps H.264 MP4. A separate
offscreen canvas supplies a persistent recording annotation identifying the
scripted provider and fixture reply; it never changes production UI. The output
adds that 72px annotation above the 1440×1000 viewport. The encoder refuses to
overwrite an existing video (`ffmpeg -n`), so a recorded path cannot be reused
across views. Ordinary all-view test runs without `EVENER_ONBOARDING_VIDEO` are
unchanged. The annotation fixes desktop width; record desktop views for video
and rely on the ordinary tests and access-view PNGs for mobile.

The MP4 and eight access-view PNGs plus `access-views.json` leave the test's temporary root.
After the video, the driver captures resolved access and the real Change button's
focused owner at 1440×1000 and 390×844 in light and dark. CDP emulates the system
theme, which the production preference listener applies. No UI or RPC is replaced.
The JSON records theme, dimensions, access state and focus, not authentication data.
Frames, caption, fixture credentials, browser profile and daemon state are removed
by the normal finite lifecycle. Encoding refuses to overwrite an existing video.
Inspect representative frames and decode the finished video before publishing it;
a video alone does not prove the Go assertions passed. The test is never paused
as a recording server.

Sanitized journey screenshots can also be copied into a caller-named durable
directory with `EVENER_ONBOARDING_EVIDENCE_DIR=/absolute/dir`. Nothing else
leaves the test's temporary root.

## Not exercised by this guard

Remote-host OAuth/editor ownership, native onboarding, billing/quota or
generation-permission proof, a provider whose listing returns no usable models,
and an explicit model choice made during the check in a real browser. Those
remain covered by focused Go/component tests, task review, or separate work.

One initial-load timeout was observed in the first mobile-dark key run: the
page's body was empty for the whole readiness wait. It occurred when the CDP
device-metrics/theme emulation was applied before the first navigation. Applying
the target layout immediately after navigation (still before any setup
interaction) was followed by five consecutive mobile-dark passes. The cause is
inferred as a pre-navigation emulation race; if the blank document recurs, the
guard fails explicitly on the missing prerequisite rather than timing out a
stale layout.

## Activation lifecycle regression

`go test -tags browserguard ./cmd/evener-hub -run '^TestOnboardingEvidenceButtonLifecycle$' -count=1`
runs the actual walkthrough button helper against real Chrome DOM controls.
A recording-pause callback disables, aria-disables, hides or covers a control.
The test forwards all CDP commands/results unchanged, observes the blocked
activation/readiness operation, then replaces the control with a ready one.
It requires zero blocked activations and exactly one activation after recovery.
This is driver/DOM coverage, not a mock frontend E2E. The full onboarding test
still uses the production UI and real AppWire/provider boundary.

Recording pauses precede the bounded readiness wait. Visibility, native and ARIA
disabled state, center hit testing, and activation occur in the same browser turn.
