# Codex model-list client version

## Intent and why

Codex gates its model roster on the required `client_version` query parameter. The old static value omits current models. Use `0.888.0` by default and let each Codex provider connection override it.

## Goals

- Send `/models?client_version=0.888.0` for an uncustomized Codex provider.
- Support `codex_client_version = "<version>"` on `openai-codex` and providers based on it.
- Expose the authored override as a text input in the provider sheet's connection settings, the existing Advanced controls area.
- An empty input clears the authored override and restores the default.

## Non-goals and anti-goals

- Do not change completion requests or Evener's own build-version User-Agent.
- Do not expose a second way to edit the full models endpoint.
- Do not add a widget or redesign provider settings.
- Do not add model ids as a workaround for the stale discovery version.

## Constraints

- The backend requires `client_version`; omitting it returns HTTP 400.
- Provider configuration remains field-wise layered, and an authored setting must round-trip through hub writes.
- The field appears only for connections using `oauth-openai-codex`.
- Existing form identity, save, rename and stale-listing rules keep working for the new field.

## Approaches

1. **Provider transport option, chosen.** Add `Transport.CodexClientVersion`, merge it with the other transport fields, and replace `client_version` in the model-list URL. This keeps the concern with model discovery and inherits naturally from the Codex base.
2. **Expose the whole `models_endpoint`.** It needs no new registry field, but makes a user manage URL syntax to change one value. Rejected.
3. **Process-wide build version.** It cannot represent two Codex connections needing different rosters and conflates Evener's version with the Codex protocol version. Rejected.

## Design

The registry carries a Codex-only transport setting, default `0.888.0`. The Responses listing encodes it with standard URL query handling; unrelated parameters survive, and other protocols do not read it. The hub's instance entry exposes the authored override, never a resolved secret or credential. Instance create/edit accepts and clears it for Codex-auth providers. The TypeScript client is regenerated from the AppWire schema. The provider sheet shows the field inside its connection-settings disclosure only when `auth === "oauth-openai-codex"`. Empty input produces a clear mutation; a changed input produces the version override. Documentation names the configuration key and its scope.
