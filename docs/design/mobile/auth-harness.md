# Native authorization fixture

This opt-in fixture uses the real hub auth/instance handlers, registry and
credential storage with scripted functions at the external OAuth boundary. It
creates temporary state and does not contact a provider or load production hubs.
It supports one manual device flow at a time. Do not run its smoke script while
someone is using its approval page: the script changes the fixture's mode/state.

Start from the repository root with an address reachable by the simulators:

```sh
EVENER_NATIVE_AUTH_HARNESS=1 \
EVENER_NATIVE_AUTH_ADDR=0.0.0.0:9201 \
EVENER_NATIVE_AUTH_ORIGIN=http://192.168.118.142:9201 \
go test ./cmd/evener-hub -run '^TestNativeAuthHarness$' -count=1 -v -timeout=30m
```

The LAN address above was verified for this workstation on 6 September 2026;
recheck it before reuse. The origin is embedded in returned authorization URLs.
Add a mobile hub profile pointing at that origin with an empty token. The fixture
has a `work` instance based on `openai-codex`. The fixture intentionally exposes
only provider administration/auth and an empty session list.

- Device mode is the default. The returned local page has an Approve button.
- `POST /mode/browser` switches subsequent device starts to browser fallback.
- The browser authorization page displays a full redirect URL for paste-back.
- `GET /status` returns only signed-in state and active credential source.
- `POST /stop` ends the fixture and lets Go clean up temporary storage.

Run the actual native client's wire smoke against localhost port 9201:

```sh
./mobile-native/node_modules/.bin/tsx mobile-native/scripts/auth-harness-smoke.mts
```

This checks device pending → authorized, browser fallback → completion, stored
OAuth source and logout. It leaves the fixture signed out in device mode. It does
not establish native UI/browser behavior. The fixture skips unless explicitly
enabled, so default Go tests neither listen nor wait for interaction.

Verified: default harness test skips/passes; the current auth device/login Go tests
pass; the checked-in native wire smoke completes both flows and returns signed
out. The first smoke exposed a missing fixture server version; the fixture now
supplies one and passes the real client's handshake validation. The iOS device browser round trip and credential clearing now have
[manual evidence](providers-evidence.md#ios-device-authorization-browser-round-trip).
The [Android device round trip](providers-evidence.md#android-device-authorization-browser-round-trip)
is also verified manually, including Chrome first-run interruption. Browser redirect
fallback and expiry/failure scenario controls remain.
