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
provider generation nor a rendezvous session exists before Start.

Coverage: prerequisite ordering, directory/task drafts, API-key save and
model-list failure/retry, provider-scoped explicit model choice and focus,
explicit Start, actual provider reply, typed durable input/communication/turn
completion, configured-user bypass and a distinct blank-session composer with
an empty durable transcript. The provider verifies the saved credential reaches
both listing and generation. Metadata generation after Start is allowed; exactly
one streamed user turn is required.

Each run owns its home/config/state/cache, rendezvous root, credential file,
provider listener, hub listener, daemons and Chrome profile. Implicit Ollama
probing is redirected to the fixture's unavailable endpoint. Catalogue refresh
is offline. Cleanup runs on failure too. Test temp files are removed, so failure
page text is included in the Go output rather than pointing to deleted artifacts.

## Later recording work

This guard does not record video. Reuse its isolated provider, production binary
and actual UI path for the separate recording task. Put retained recordings in
a session-owned artifact directory outside source, not this test's temporary
root. Use a separate headed Chrome profile and explicit recording lifecycle,
show the provider as scripted, and start capture after authentication so private
links stay out of frame. Keep fixture keys masked and retain a sanitized evidence
manifest, not the auth URL or credential file. Do not leave a test paused on its
three-minute hang deadline as a recording server.

This guard is a desktop smoke acceptance path, not the complete visual/theme,
OAuth, remote-host, access-mode, catalogue-recovery or repository gate matrix.
