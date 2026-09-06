# Native hub administration contract inventory

Inspected 6 September 2026 at `fea6040c3`. This is an implementation inventory,
not evidence of native feature completion. Current web UI and registered server
contracts are authoritative; old mobile presentation is not a feature source.

## Current operations and ownership

| Workflow | Current wire operations (prefix `evener/`) | Backlog |
| --- | --- | --- |
| Provider instances | `instance/list`, `create`, `edit`, `remove`, `setDefault` | MOB-012 |
| Credentials | `auth/apiKey/set`, `auth/apiKey/clear`, `auth/logout`, `auth/test` | MOB-012 |
| Sign-in | `auth/device/start`, `auth/device/poll`, `auth/login/start`, `auth/login/complete` | MOB-013 |
| Marketplaces | `marketplace/list`, `add`, `remove`, `refresh`, `browse` | MOB-014 |
| Plugins | `plugin/list`, `install`, `upgrade`, `remove`, `enable`, `disable`, `setAutoUpgrade` | MOB-014 |
| Runtime information | `settings/overview` | MOB-015 |
| Launch configuration | `launch/schema`, `getLayer`, `setLayer`, `resolve`, `trustRepo`; `path/validate`, `paths/complete` | MOB-015 / MOB-007 |
| Upgrade | `upgrade` | MOB-016 |

Names abbreviated within each family retain that family's prefix. Provider
instance responses return the full updated list. Credential mutations require
refreshing instance data; credential test results become stale when configuration
changes. The web disables instance CRUD on a provider-file load error while
retaining credential operations that do not write that file.

The web starts device login first, then starts browser login when the response
requests fallback. Browser completion includes the provider, flow ID and returned
redirect URL. Native backgrounding, external browser return and flow lifetime
need explicit testing; web behavior alone does not prove native acceptance.

## Boundaries that affect native design

- General/runtime, storage, agents and Codex launches include read-only summaries.
  Displaying these does not authorize inventing edit operations. In particular,
  listen address/run directory/spawn timeout are not settings form mutations.
- Plugin/skill directories and MCP configuration use the global launch layer.
  Preserve other fields when editing a collection; validate paths on the hub.
- `auth/updated`, `marketplace/updated`, `plugin/updated` and `launch/updated`
  drive refetches in current web stores. The runtime overview cache has no
  dedicated overview notification. Transcript display is a separate settings
  contract and does have its own changed notification.
- Web stores bind to the current web connection. Native administration must
  bind requests, cached data and subscriptions to the chosen hub/client lifetime.
  A singleton cache copied from the web would not establish multi-hub correctness.
- Saved mobile hub profiles/pairing belong to MOB-004. They are distinct from
  administering the server. Theme, notifications and transcript/display settings
  need their own native preference parity audit; this inventory does not close it.
- Upgrade is a command-palette operation in the web, not an editable About form.
  Its current request uses an empty `requested` value; do not infer a release
  chooser or availability feed from that call.

## Source evidence

Paths below are relative to the repository root:

- `cmd/evener-hub/frontend/src/panes/settings/sections.ts`: section inventory.
- `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx`:
  instance actions, diagnostics, credential result invalidation and sign-in fallback.
- `cmd/evener-hub/frontend/src/stores/credentials.ts`: typed requests and auth invalidation.
- `cmd/evener-hub/frontend/src/stores/extensions.ts`: marketplace/plugin requests,
  global launch layer editing and notification refresh.
- `cmd/evener-hub/frontend/src/stores/launchConfig.ts`: schema/layer/trust/path operations.
- `cmd/evener-hub/frontend/src/stores/settingsOverview.ts`: runtime overview caching.
- `cmd/evener-hub/frontend/src/panes/settings/sections/hub.tsx`: read-only runtime values.
- `cmd/evener-hub/frontend/src/shell/palette/commands.ts`: upgrade request.
- `cmd/evener-hub/frontend/src/protocol/types.gen.ts`: generated request/result contracts.
- `cmd/evener-hub/app_rpc.go`: typed server registration for these operations.
- `cmd/evener-hub/app_rpc_settings_overview.go`: runtime overview response construction.

Implementation order: provider instances and keys, sign-in, marketplaces/plugins,
launch settings and runtime information, then upgrade recovery. Each issue needs
its own tested native workflow; this inventory alone completes none of them.
