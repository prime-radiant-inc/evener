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

- The fixture's first model-list request is a 503. The connector retries it on
  its own timer and completes; the checkpoint requires at least the failure and
  retry, and no session, process launch or provider generation exists before
  Start.
- Setup dismisses automatically with no forced model picker or success page.
  The untouched draft adopts the fixture's configured default, which is the
  second listed model (`gpt-4o`), not the first.
- The task text, working directory and one decoded attachment tile survive.
  The provider receives the task and an `image_url` part, and the durable
  transcript contains the user text plus an image part.
- Only after the explicit Start activation does a real daemon run. Exactly one
  streamed turn uses the adopted model and shows `ONBOARDING_REPLY_72e`; a
  second, deliberately blank launch has no user input, reply or completed turn.

The driver captures `journey-automatic-draft-<width>-<theme>.png` and the
matching session screenshots at 1440×1000 and 390×844, light and dark. Each
capture is also an assertion: the settled draft has no dialog or open picker,
Start is visibly available, the model control shows the adopted model, the task,
directory and attachment are present, and focus stayed in a visible draft
control (the composer is required where the connector still owns focus, as it
does on the OAuth polling path). The narrow layout has no dockview tab strip, so
its session capture checks the visible page text instead of the desktop pane.
Reply and composer queries are scoped to the active pane at desktop sizes.

## `TestOnboardingOAuthBrowser`: device, redirect, cancel, endpoint change

Each subtest uses the real hub OAuth controller; `hubAuthControllerSetup` only
sets `IssuerBaseURL` and the HTTP client that reaches the scripted issuer. Real
device-code request/poll, token exchange, `auth/openai-codex.json` persistence,
registry reload, AppWire and browser UI remain production code. Subtests:

- `oauth-device`: device usercode, one pending poll, token exchange, automatic
  model-list check and setup dismissal. The saved record is read back from the
  real state root and the provider saw the OAuth access token. One token
  exchange and model-list access are required; no generation is attempted.
- `oauth-redirect`: the issuer's device endpoint returns 404, the connector
  falls back to the authorize URL, and the driver pastes a callback URL carrying
  the production flow's own `state`. The real code exchange and persistence are
  then verified as above.
- `oauth-cancel`: a device flow that stays pending is cancelled. After two poll
  intervals the checkpoint requires zero exchanges and zero model-list checks,
  proving no late completion. A second sign-in on the same editor then
  completes automatically.
- `oauth-endpoint-change`: while the device flow is pending, Go performs a real
  AppWire `evener/instance/edit` that re-points the instance. The browser must
  return to the connection editor with the `changed` alert and the polling
  editor gone; the checkpoint requires no token exchange or model-list check
  from the refused flow.

These subtests also prove the redirect tab does not break the theme matrix by
keeping the app tab in front before emulation. The OAuth completion currently
dismisses setup automatically but leaves the model control in its `required`
state with Start disabled; the browser logs this as `REPORT oauth completion
left model state ... and Start disabled`. That is a recorded finding, not a
pass condition here: these paths are required to prove persistence and
model-list access, and the configured-default adoption contract is proved by
`TestOnboardingBrowser`.

## Optional recording

Set `EVENER_ONBOARDING_VIDEO` to an absolute, new `.mp4` path outside source
and put `ffmpeg` with libx264 on PATH. Create its parent directory first:

```sh
EVENER_ONBOARDING_VIDEO=/absolute/session-artifacts/onboarding.mp4 \
  go test -tags browserguard ./cmd/evener-hub -run '^TestOnboardingBrowser$' -count=1 -v
```

This runs the same assertions with finite reading pauses. CDP screencast capture
begins after authentication and records only the page viewport, never the address
bar. Chrome remains headless with its own disposable profile. Frame timestamps
preserve elapsed time, including the pauses, in a 20fps H.264 MP4. A separate
offscreen canvas supplies a persistent recording annotation identifying the
scripted provider and fixture reply; it never changes production UI. The output
adds that 72px annotation above the 1440×1000 viewport.

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
an explicit model choice made during the check in a real browser, and the
OAuth completion's configured-default adoption (the finding above). Those
remain covered by focused Go/component tests, task review, or separate work.

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
