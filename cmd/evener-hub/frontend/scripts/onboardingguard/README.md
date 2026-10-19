# Onboarding browser guard

Run from the repository root:

```sh
make web-preflight
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestOnboardingBrowser$' -count=1
```

Requires Node and Chrome. `TestOnboardingBrowser` builds a disposable production
`evener` binary, serves the embedded production frontend from a real hub, and
launches real daemons through `HubSpawner`. The sole scripted service is a
loopback OpenAI-chat HTTP provider. An endpoint definition is seeded, but no
credential, model selection, default model, session or browser storage is seeded.

The driver imports skillguard's exported Chrome lifecycle/input driver, not its
scenarios or frontend fixtures. All UI actions go through the production app.
The separate checkpoint URL only lets Node ask Go to verify that neither a
provider generation, process launch attempt nor rendezvous session exists before
Start. Final checks compare both launch attempts and all generations with the
actual Start activation event, captured synchronously before its React handler.

Coverage: prerequisite ordering, directory/task drafts, API-key save and
model-list failure/retry, provider-scoped explicit model choice and focus,
explicit Start, actual provider reply, typed durable input/communication/turn
completion, configured-user bypass and a distinct blank-session composer whose
transcript has no user input, fixture reply or completed execution. Startup and
other bookkeeping records are permitted. Each active dock tab's session identity
is joined to its spawned daemon PID, rendezvous metadata and exact transcript
path/header. Both actual working-directory records must match the chosen path.
Reply and composer queries are scoped to that active pane. The provider verifies
the saved credential reaches both listing and generation. Metadata generation
after Start is allowed; exactly one streamed user turn is required.

Each run owns its home/config/state/cache, rendezvous root, credential file,
provider listener, hub listener, daemons and Chrome profile. Implicit Ollama
probing is redirected to the fixture's unavailable endpoint. Catalogue refresh
is offline. Cleanup runs on failure too. Test temp files are removed, so failure
page text is included in the Go output rather than pointing to deleted artifacts.

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
Frames, caption, fixture credentials,
browser profile and daemon state are removed by the normal finite lifecycle.
Encoding refuses to overwrite an existing video. Inspect representative frames
and decode the finished video before publishing it; a video alone does not prove
the Go assertions passed. The test is never paused as a recording server.

This guard is a desktop smoke acceptance path, not the complete visual/theme,
OAuth, remote-host, access-mode, catalogue-recovery or repository gate matrix.

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
