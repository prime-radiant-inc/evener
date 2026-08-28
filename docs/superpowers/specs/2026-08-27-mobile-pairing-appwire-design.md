# Mobile pairing over AppWire

## Goal

Move the authenticated settings UI from `GET /api/mobile/pairing` to one
typed, hub-scoped AppWire read while preserving the pairing URL, configured
origin override, server-side origin policy, and unreachable-origin behavior.

This is one migration PR. `/auth?token=...` remains the HTTP bootstrap opened
by the phone. `/api/health`, `/doc/*`, the SPA/static resources, and every other
HTTP or AppWire method remain unchanged.

## Contract

The hub exposes `evener/mobile/pairing` with these typed parameters:

```json
{
  "origin": "http://192.168.1.20:9180"
}
```

The web caller supplies `window.location.origin`, which is the origin serving
the authenticated application. The successful response is:

```json
{
  "authUrl": "http://192.168.1.20:9180/auth?token=..."
}
```

The response keeps the AppWire camel-case naming convention. It contains the
same URL produced today by `hubedge.AuthURLFor`, including token escaping and
the `/auth?token=...` bootstrap path.

## Server behavior and trust boundary

The handler chooses and validates the base URL in this order:

1. When `MobileBaseURL` is configured, ignore the caller's `origin` and use the
   configured value.
2. Otherwise, use the explicit caller origin.
3. In both cases, pass the chosen value through the existing
   `safeMobileOrigin` policy before generating a URL. The handler never embeds
   an unvalidated caller value.

The origin policy remains unchanged: HTTPS may name a public or private
non-loopback host; HTTP must name a private/link-local/CGNAT address or a
`.local` host; loopback, localhost spellings, legacy numeric loopback forms,
and invalid origins are rejected.

When the selected origin is unsafe or unreachable, return an AppWire conflict
with the existing message:

```text
mobile pairing requires a reachable non-loopback Hub origin
```

The settings UI maps that conflict to its existing configuration guidance by
using the standard `WireError.evenerErrorInfo === "conflict"` discriminator,
not a numeric code or message comparison. Other request failures continue to
show the generic pairing-link error.

## Implementation scope

1. Add the `evener/mobile/pairing` method constant, parameter and response
   types, hub scope catalog entry, Go client wrapper, and generated TypeScript
   declarations/documentation.
2. Register a small hub handler that applies the selection and validation
   rules above and returns `hubedge.AuthURLFor(base, AuthToken)`.
3. Move the mobile-origin policy helpers out of the REST API file beside the
   new AppWire handler; their behavior does not change.
4. Update `MobileSection` to obtain the shell's injected AppWire client through
   `useClient()`, start the request through the existing `useConnectedEffect()`
   readiness gate, send `window.location.origin`, consume `authUrl`, and
   preserve its loading, warning, unreachable, and generic-error states.
5. Remove the REST mux registration and handler. Delete route-only HTTP tests;
   retain the meaningful configured-origin, caller-origin, URL-generation,
   conflict, and origin-policy coverage as AppWire handler tests.

No REST compatibility route or absence-only test is added.

## Testing and verification

- Prove the AppWire client/catalog and hub handler tests fail before adding the
  protocol and handler implementation.
- Prove the settings component test fails while it still calls `fetch`, then
  migrate it to a scripted `FakeClient` and assert rendered behavior from the
  typed response and conflict.
- Run `make generate`, Go formatting, and Biome on touched frontend `src/`
  files.
- Run focused `appwire`, hub, and mobile-section tests, then `make test-web`,
  `make lint`, `make vet`, and `make test`. Run `make test-web-browser` on this
  Chrome-capable host even though the change does not alter geometry.
- Fetch `origin/main` immediately before publication, rebase if it moved,
  rerun the required verification on the exact head, push the branch, and open
  a non-draft PR against `main` without merging it.
