# Native hub information and launch settings

Status: implementation direction for MOB-015 and MOB-007, 6 September 2026.
Feature authority is the current web and typed hub contracts, not the old mobile
UI. This document does not mark those backlog items complete.

## Read-only hub information

`evener/settings/overview` supplies hub version/commit, listen address, run
folder, spawn timeout, bearer-token age, past-index metadata, state folder,
agents, Codex launch entries and discovered MCP servers. The native view must
retain every exposed field; absent scalar/pointer data is unavailable. Go omitempty encodes empty agent and
Codex collections, MCP server lists, and zero index counters as omitted fields;
normalize those to the empty/zero values established by the server implementation. Token age is metadata, never a token reveal.

Web authority: settings sections `general.tsx`, `hub.tsx`, `storage.tsx`,
`agents.tsx`, `launchCodex.tsx` and `stores/settingsOverview.ts` under
`cmd/evener-hub/frontend/src`. Typed response: `SettingsOverviewResponse` in
`protocol/types.gen.ts`.

Use one hub-bound screen with a small version/address summary and disclosures
for Runtime and storage, Agents, Codex launches and discovered MCP servers.
Long paths wrap inside details; compact rows show names and secondary values.
Agent edit paths are server paths. Do not launch a phone editor for a hub path.
The web's external-editor action needs an explicit supported remote-file flow
before native can claim that action is covered.

Load through the connected client's overview RPC, retain known data on refresh
failure, expose retry, and invalidate the whole screen lifetime when the hub or
connection changes. No cross-hub global cache. Return from background/reconnect
must obtain current state. MCP's own discovery error is distinct from overview
transport failure. Environment entries expose key names only, as the wire does.

## Editable launch settings

Web authority: `sections/launchServer.tsx`, `launchShared/LaunchConfigForm.tsx`,
`launchShared/schema.ts`, `launchShared/fields.tsx`,
`launchShared/collectionFields.tsx`, and `stores/launchConfig.ts`.

The global form loads `evener/launch/schema`, `evener/launch/getLayer` with
`cwd: "/", layer: "global"`, and resolved effective defaults from
`evener/launch/resolve`. Save uses `evener/launch/setLayer` and its returned
resolved diagnostics. Path validation uses `evener/path/validate`, not phone
filesystem access. Directory assistance can use the native HubDirectoryField;
file-capable fields require file suggestions with the server's path-kind rules.

A native settings index groups the server schema into compact rows. Editing one
field opens the suitable native control and shows both its explicit override and
its effective/inherited value. Removing an override must be distinct from false,
zero, or empty values. Save preserves all unrelated fields in the layer, including
fields the current screen does not render. Use server choices, driver support,
layer applicability and diagnostics. Do not build a generic JSON editor or invent
an update catalog. Array, map, MCP, prompt and file fields remain required.

Coordinate project/per-launch scope with creation: resolve using the chosen hub
working directory, show provenance, preserve repository trust/hash checks, and
use the same schema interpretation across creation and global settings. An
uncertain save is reconciled by reading; do not replay a write on reconnect.
External `evener/launch/updated` notifications must refresh clean views and warn
an active editor about changed state before it overwrites another client's work.

## Delivery and evidence

1. Implement hub information loading and its native disclosures. Test late reads,
   refresh errors and missing optional sections; verify all displayed fields on
   two different isolated hubs and both native platforms.
2. Implement typed launch-layer loading/edit/save behavior with server schema and
   effective provenance. Test preservation of unrelated fields, explicit false/0,
   removing overrides, write failure/readback, external changes and hub disposal.
3. Build native field editors by schema kind, including collections and MCP.
   Reuse pure web schema helpers where their contracts fit; inspect those helpers
   before reuse. Verify server path validation and launch-layer applicability.
4. Complete per-project/per-launch and trust behavior in creation; manually create
   sessions that demonstrate the chosen overrides actually take effect.
5. Native acceptance: keyboards, large text, screen readers, nested detail
   navigation, long paths, partial errors, two hubs and background/reconnect.
   Visual acceptance must measure visible content and remove redundant labels,
   oversized controls and repeated paths rather than merely passing screenshots.

MOB-016 hub upgrade remains separate and must use an isolated disposable hub for
native mutation/restart acceptance. All broader app requirements remain open in
the authoritative backlog.


## Launch-layer controller progress

6 September 2026: `mobile-native/src/launchSettings.ts` implements one connected
hub/cwd/layer editor. It loads schema, explicit layer and effective resolution,
keeps resolution failure separate from editable-layer failure, and preserves
unrelated fields. Edits are constrained to schema fields applicable to the layer.
False/zero are explicit overrides; undefined removes the override. Value-kind and
path validation still belong to the forthcoming native editor/server validation.

Before whole-layer save it reads the current layer and refuses a changed baseline.
Dirty editors receiving relevant launch notifications retain their draft and require
reload. Saves exclude overlapping edits/saves, read back uncertain outcomes, and
never replay a mutation. There is no server compare-and-set; another client can
still write between preflight and mutation. Do not claim atomic conflict safety.

Ten deterministic controller tests cover override semantics, unrelated-field
preservation, field applicability, stale preflight, dirty external notification,
uncertain write/readback, partial resolution failure, disposal, overlapping saves,
retry after failed preflight and obsolete loads (some tests cover multiple related
assertions). All 282 native tests, TypeScript and touched-file Biome pass.

`mobile-native/scripts/launch-settings-smoke.mts` checks the exact isolated hub
state root before changing maxRounds, saving through the controller, independently
reading back and checking untouched fields, then restoring and comparing the whole
original layer. It passed against SecondHub; the original layer was restored.
This is real AppWire/controller evidence, not native editor E2E. No native editor
is wired yet; all schema field kinds, validation and native acceptance remain.
