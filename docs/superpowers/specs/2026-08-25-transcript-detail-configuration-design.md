# Unified Transcript Detail Configuration Design

**Date:** 2026-08-25
**Status:** Approved

## Summary

Evener will replace its fragmented transcript visibility controls with one configuration model. A five-level control will cover ordinary reading. Advanced controls will expose independent content, metric, and diagnostic options.

The live control and Settings serve different scopes:

- The live control changes the active view for every session in the current browser and layout class. This browser-local choice survives restarts but never affects another machine.
- Settings changes the Desktop and Mobile defaults stored by the hub. These defaults sync to every browser and device paired with that hub.

The live Session pane, read-only Transcript pane, and Settings examples will use one typed projection pipeline. This removes the current divergence between the Everything and Intent render trees.

## Goals

1. Give regular users one clear progression from conversation to expanded detail.
2. Let advanced users control transcript content, metrics, and diagnostics independently.
3. Apply live changes to every local session without changing another machine.
4. Sync Desktop and Mobile defaults across devices paired with one hub.
5. Preview each hub default with fixed example data in Settings.
6. Preserve critical information at every detail level.
7. Preserve scroll position, focus, and explicit disclosure choices when visibility changes.
8. Migrate explicit legacy browser preferences without weakening their storage contracts.

## Non-goals

This work will not:

- create user accounts or sync preferences across independent hubs;
- add named or user-created view profiles;
- persist settings in session data or URLs;
- create per-session or per-pane visibility overrides;
- put custom visibility vectors in shared links;
- fetch real transcript data for the Settings examples;
- change the always-visible session-total cost in the footer.

## Current system and constraints

The current implementation has two independent visibility systems:

- `Session.tsx` owns a transient `everything | intent` value and initializes every mount to `everything` (`cmd/evener-hub/frontend/src/panes/session/Session.tsx:124-137`). Everything renders full turns through `TurnBlock`; Intent builds a separate focused-entry tree (`Session.tsx:209-270,411-475`).
- `prefs.ts` stores transcript metadata and system-item booleans in localStorage. Settings places timings, tokens, prompts, and hook exits under Transcript, but places estimated cost under Display (`cmd/evener-hub/frontend/src/stores/prefs.ts:21-35,216-260`; `panes/settings/sections/transcript.tsx:19-47`; `panes/settings/sections/display.tsx:46-58`).

The split causes three visible problems:

1. Everything couples each tool's stated purpose with its call row. Intent removes the call but offers no independent content controls.
2. Intent bypasses `TurnBlock`, so it can hide failures, warnings, and other critical rows.
3. The live selector sits in `PaneScaffold.actions`; the pane header is absent on Mobile, so Mobile has no live selector.

Existing browser preferences use flat `evener.prefs.*` keys. Booleans use the exact strings `1` and `0`. In particular, `evener.prefs.showCost` is a pinned compatibility contract (`stores/prefs.ts:8-19,141-160`).

Evener has no user principal or account settings service. Every paired browser uses the same hub capability. The new synced defaults therefore belong to one hub, not one human across independent hubs.

## Vocabulary

The design uses these terms consistently:

- **Hub default:** The Desktop or Mobile configuration stored by one hub and sent to its paired clients.
- **Local active view:** The Desktop or Mobile configuration stored by one browser. It overrides the matching hub default.
- **Layout class:** `desktop` or `mobile`, classified by Evener's existing 899px boundary.
- **Regular level:** One of the five named content presets.
- **Advanced options:** Independent content, metric, and diagnostic controls.
- **Critical row:** A row that remains visible at every level because it reports required interaction, active work, steering, warning, denial, interruption, or failure.

## Regular detail levels

The compact control presents five cumulative levels:

| ID | Visible label | Content | Default disclosure state |
| --- | --- | --- | --- |
| `chat` | Chat | User and agent conversation | Collapsed where applicable |
| `intent` | Intent | Chat plus each tool's stated purpose | Collapsed |
| `tools` | Tools | Intent plus compact tool rows | Tool bodies collapsed |
| `activity` | Activity | Tools plus reasoning | All eligible bodies collapsed |
| `full` | Full detail | Same regular content as Activity | All visible eligible bodies open by default |

The narrow stepped track may abbreviate **Full detail** to **Full**, but its accessible name and surrounding readout must use the full label.

Selecting a regular level changes only the regular content vector. It preserves metric and diagnostic options because those options are Advanced-only.

### Full detail and explicit disclosure choices

Entering Full detail opens every currently visible eligible disclosure, including tool, reasoning, and enabled Advanced disclosure bodies. This transition clears prior per-item closed overrides for those entries and establishes a new open baseline. New eligible rows arrive open while Full detail remains active.

After that transition, an explicit per-item collapse wins. The renderer must not reopen a row on every render. Moving to another level and entering Full detail again establishes another open baseline.

## Always-visible critical content

Every regular and Custom view shows compact representations of:

- questions and other input requests;
- permission and sandbox-escalation requests;
- active or running work;
- user steering that changed the run;
- warnings and denials;
- failed tool calls and non-zero hook failures;
- interrupted or failed turns;
- recovery actions the user can take.

Low-detail views may omit arguments, output, and routine metadata from these rows, but they must preserve identity, status, and the available action. A tool call with no stated purpose renders a neutral summary such as **Action summary unavailable**; it never vanishes.

## Advanced options

Advanced opens one editor with three groups.

### Content

- Tool intent
- Tool calls
- Reasoning
- Expand visible details by default

These fields mirror the regular content vector. Changing one to a combination that matches a regular level normalizes back to that level. Any other combination becomes **Custom**. Custom has no falsely selected regular stop.

### Metrics

- Round timings
- Token counts
- Estimated cost

These fields do not change the regular level label. The trigger summarizes additions, for example, **Tools · 2 advanced**. Round timings continues to control both the friendly round annotation and the corresponding raw timing item. Estimated cost controls per-round cost only; session-total cost remains visible in the footer.

### Diagnostics

- Low-level system events
- System prompt and prompt-loaded events
- Hook exit messages: `none`, `successful`, or `all`

The hook field replaces the current overlapping `hookExitsNormal` and `hookExitsAll` booleans. Critical non-zero hook failures remain visible in compact form even when the field is `none`; `all` exposes their full diagnostic row.

The Advanced summary and opener are one control, not separate affordances. It summarizes the whole editor, including Custom content and independent extras. Example states include **Advanced · 2 enabled ›** and **Advanced · Custom content · 2 extras ›**. The live popover or sheet uses the same editor and summary rules.

## Configuration model

The shared domain model will have this shape:

```ts
type ViewportClass = "desktop" | "mobile";
type TranscriptLevel = "chat" | "intent" | "tools" | "activity" | "full";
type HookExitDetail = "none" | "successful" | "all";

type ContentSelection =
  | { kind: "preset"; level: TranscriptLevel }
  | {
      kind: "custom";
      toolIntent: boolean;
      toolCalls: boolean;
      reasoning: boolean;
      expandByDefault: boolean;
    };

type TranscriptDisplayConfigV1 = Readonly<{
  version: 1;
  content: ContentSelection;
  advanced: Readonly<{
    roundTimings: boolean;
    tokenCounts: boolean;
    estimatedCost: boolean;
    systemEvents: boolean;
    promptEvents: boolean;
    hookExits: HookExitDetail;
  }>;
}>;
```

User and agent messages do not appear as configurable fields. Critical content is also invariant.

Named presets persist by semantic name. A future release may add ordinary content to a named level deliberately. Custom configurations persist a versioned snapshot; a migration must add future Custom fields with a conservative value rather than silently expanding an existing Custom view.

A pure normalizer converts any Custom content vector that exactly matches a preset back to that preset.

## State and precedence

Evener will maintain two configuration layers for each layout class.

### Hub defaults

The hub stores one Desktop configuration and one Mobile configuration. Settings edits these values. The hub publishes changes to every paired client.

A new hub starts with:

- Desktop: Tools
- Mobile: Intent
- Advanced fields: all off

These are presentation defaults, not session data.

### Browser-local active views

The browser stores one Desktop configuration and one Mobile configuration in localStorage. The live transcript control edits these values.

A local change:

- updates every open Session and read-only Transcript pane in that browser;
- updates same-origin tabs through a browser synchronization channel;
- survives browser restarts;
- never publishes to the hub.

A browser with no local value follows the matching hub default. The live editor offers **Use hub default**, which deletes the local value for the active layout class.

### Resolution

The effective configuration resolves in this order:

```text
browser-local active view
  → hub default
  → shipped default
```

A hub-default notification updates every local view that follows that default. A browser-local active view remains stable until the user changes or clears it.

The Settings cards always edit and preview hub defaults. If the current browser has a local active view, Settings explains that the local view continues to override the edited default.

### Browser synchronization

The current preference store deliberately omits cross-tab synchronization. This feature adds synchronization for transcript display preferences only. A same-origin `BroadcastChannel` will notify other tabs; a storage-event path will provide a fallback where available. The originating tab updates its Zustand state directly.

The channel carries browser-local configuration only. Hub defaults continue to arrive through the hub protocol.

## Responsive behavior

Evener will use its existing layout classifier:

- Mobile: `(max-width: 899px)`
- Desktop: 900px and wider

The browser keeps separate local values for the two classes. Crossing the boundary applies the matching local value, or the matching hub default when no local value exists. Crossing back restores the other class's value.

Every open transcript updates together. The design has no per-pane or per-session exceptions.

The live trigger's compact layout responds to pane width with a container query. This avoids crowding narrow desktop dock panes without creating another device breakpoint.

## Live control

The live control replaces the current Everything/Intent selector.

### Placement

A small transcript-local toolbar sits above the transcript scroller and below critical escalation rails. It remains outside virtualized rows, so it neither scrolls away nor changes row indexing.

The toolbar shows a compact trigger such as **Detail: Tools ▾**. It must remain visible on Mobile, where the pane header is absent.

### Desktop

Desktop opens an anchored popover containing:

1. the five-stop control;
2. the current scope, such as **Local Desktop view** or **Using hub default**;
3. the single Advanced disclosure;
4. **Use hub default** when a local value exists;
5. a link to **Edit hub defaults**.

### Mobile

Mobile uses the same trigger but opens a bottom sheet. The sheet has a heading, a Close button, focus containment, focus return, and controls with 44px minimum touch targets.

## Settings editor

Settings will consolidate transcript visibility under **Settings → Transcript display**. Estimated cost moves out of Display; composer behavior remains there.

The page contains two stacked cards:

1. Desktop default
2. Mobile default

Each card presents:

1. the device label and current level;
2. the five-stop control;
3. one Advanced summary/disclosure row;
4. an example conversation directly below the controls;
5. a textual inventory of shown and hidden categories.

The page explains:

- **Hub defaults sync to devices paired with this hub.**
- **A live transcript choice is browser-local and does not change another machine.**

Settings sends acknowledged hub mutations. It must not emit an unconditional success toast before durable confirmation.

## Settings preview

Each card uses the same fixed scenario so users can compare Desktop and Mobile settings. The fixture includes:

- a user message;
- a tool call with a stated purpose;
- a successful tool row and body;
- a failed critical tool row;
- reasoning;
- an agent response;
- optional timing, token, and cost values;
- optional system, prompt, and hook events.

The preview runs through the production projector against fabricated `ThreadModel` data. It does not read `threadsStore`, use a network connection, or copy real user content.

Each preview says **Example only—not your data**. It also lists visible and hidden categories in text. It has no fake streaming, inner scroll region, hover-only explanation, or whole-preview live region.

Controls update the example immediately as a draft. A failed hub write restores the confirmed value and preview, then shows a retryable error.

## Hub persistence and protocol

The current `evener/settings/overview` RPC is read-only. Synced defaults require a new durable hub preference store and typed protocol methods.

The protocol will support:

- reading the canonical Desktop and Mobile defaults;
- patching one layout class at a time;
- returning the durable canonical value after a mutation;
- notifying all paired clients when either default changes.

Desktop and Mobile each carry a monotonic revision. A patch names one layout class and its expected revision, so edits to different classes cannot overwrite one another. The hub rejects a stale same-layout patch and returns the current canonical value for review instead of silently discarding either edit. The server validates the schema version, enum values, and complete Custom vector before writing. A malformed or unsupported request fails without changing the durable record.

These methods use hub scope and the existing hub capability. They do not create a user identity. Every client paired with the hub may read or change the defaults, just as every paired client shares the same hub authority today.

Hub writes must use the repository's existing durable-write pattern and survive restart. Notifications carry the layout class, revision, and canonical configuration; clients ignore stale revisions. A disconnected client refreshes both defaults after reconnect.

## Projection and rendering architecture

A side-effect-free transcript-display domain module will own:

1. preset expansion;
2. Custom normalization;
3. effective-config resolution;
4. typed item classification;
5. projection to stable entries;
6. metadata visibility flags;
7. a stable configuration fingerprint.

The data flow becomes:

```text
raw shared ThreadModel
  → effective TranscriptDisplayConfigV1
  → typed transcript projector
  → stable projected turns and entries
  → shared TranscriptBody
```

The live Session pane, read-only Transcript pane, and Settings examples will use `TranscriptBody`. The current separate focused-entry renderer will disappear.

### Projection rules

The projector will:

- classify by `ItemModel.type` and typed `eventKind`;
- filter before grouping so counts describe visible items;
- preserve source order and source IDs;
- create stable proxy IDs such as `intent:${item.id}`;
- avoid mutating `ThreadModel` or `ItemModel`;
- fail open for unknown item and event kinds.

When Intent is visible but Tools is hidden, a command execution projects to an intent proxy. When Tools is visible, the real tool row supersedes the proxy and renders the stated purpose as its first line. The transcript never renders duplicate purpose text for one call.

A protected tool failure may project to a compact critical entry when ordinary tools are hidden. It expands to the ordinary tool representation when Tools is visible.

## Disclosure state

The existing disclosure store remains the source of explicit per-item open and closed choices. The configuration supplies a default state, not a value that reasserts itself on every render.

Entering Full detail establishes a new open baseline for visible eligible entries. Subsequent manual choices win. Activity and lower levels default eligible rows closed without deleting transcript data.

The Settings examples use isolated disclosure scope so preview interactions cannot affect real sessions.

## Scroll and focus preservation

Every effective-configuration change uses the configuration fingerprint as the transcript view key.

Before projection changes, each mounted transcript captures:

- the top visible stable entry;
- its viewport offset;
- whether the reader is following the bottom;
- the currently focused transcript entry, when any.

After measurement, the pane restores the same entry and offset. If that entry is hidden, it restores the nearest surviving user or agent message. If none survives, it preserves normalized scroll position. A pane following the bottom remains at the bottom.

The current click handler captures only the pane where the user clicked. The new mechanism must also handle changes from Settings, another local session, another tab, a hub notification, and a breakpoint crossing.

If a change removes the focused entry, focus returns to the Detail trigger and a concise status announces the selected view. Evener must not announce every hidden or newly visible row.

## Accessibility

The five-stop control looks like a stepped slider but uses a labelled radio group. Each stop is a named radio option that pointer and voice input can address directly; Arrow, Home, and End keys move roving focus among the options.

Requirements:

- Keep all stop labels visible.
- Expose **Full detail** as the accessible name when the track displays **Full**.
- Use shape, text, or position in addition to color for selection.
- Expose Custom as text; do not mark a nearest preset.
- Give Mobile controls at least 44px touch targets.
- Give the popover and sheet a heading and deterministic focus return.
- Group Advanced options in labelled fieldsets.
- Explain disabled dependent fields with text and retain their stored values.
- Announce a committed preview change with a short polite status; do not make the preview itself live.
- Respect reduced-motion settings.

## Migration

### Legacy browser keys

The migration recognizes these existing values:

- `transcriptRoundTimings`
- `transcriptTokenCounts`
- `transcriptHookExitsAll`
- `transcriptHookExitsNormal`
- `transcriptPromptLoaded`
- `showCost`

If any legacy visibility key exists and no new browser-local config exists, the browser writes equivalent local Desktop and Mobile configurations:

- regular level: Activity;
- low-level system events: on, matching the old Everything path;
- round timings, tokens, prompt events, and estimated cost: existing values with their legacy fallbacks;
- hook exits:
  - all off → `none`
  - normal on and all off → `successful`
  - all on → `all`

The legacy fallbacks are timings on, tokens off, prompt events on, estimated cost off, and hook exits off. These values reproduce the old browser's effective view rather than applying the new hub defaults during migration.

The migrated configurations are local active views, not hub defaults. Migration on one browser must not change another machine.

The migration cannot recover a current Intent selection because the old selector never persisted it.

### Fresh-versus-untouched ambiguity

A browser that never wrote a legacy key is indistinguishable from a fresh browser. It receives no synthetic local override and follows the new hub defaults: Desktop Tools and Mobile Intent.

### Compatibility window

The migration will not delete legacy keys. Browser-local Advanced changes will dual-write the legacy transcript flags and `showCost` for the first stable release that includes this feature. Boolean values keep the exact `1` and `0` encoding. Removing the adapters requires a separate, approved migration after older clients leave the supported set.

An older client cannot represent the five-level selection because no legacy persistent key exists. It can still recover the dual-written metric and diagnostic flags.

## Error handling

### Browser storage

Malformed or unsupported local values behave as absent. A blocked or full localStorage keeps the change live in memory and reports that it will not survive restart. The UI must not claim persistence when the write fails.

### Hub state

A missing hub record uses the shipped defaults. The hub rejects a malformed durable record at the storage boundary, reports the condition for diagnosis, and uses shipped defaults without hiding critical rows.

A failed settings mutation leaves the last confirmed hub default active and offers retry. A disconnected browser keeps local controls available and refreshes hub defaults on reconnect.

### Projection

Unknown item and event kinds render as compact raw rows. Missing descriptions, metadata, pricing, or duration values omit only that optional field. They do not suppress the containing message, action, or failure.

## Testing

Default tests use fabricated models and scripted RPC boundaries. They never depend on provider credentials, network access, quota, or current model behavior.

### Pure configuration tests

- Expand each preset to its exact vector.
- Prove the five regular vectors are cumulative.
- Normalize every preset-shaped Custom vector.
- Round-trip arbitrary versioned Custom configurations.
- Preserve independent metric and diagnostic values across regular-level changes.
- Reject malformed versions and enum values.

### Projection tests

- Cover every current item type and event kind.
- Cover blank tool descriptions, failed tools, non-zero hooks, interrupted turns, and unknown future kinds.
- Prove critical rows remain visible at every level.
- Prove Tools supersedes its Intent proxy without duplicate purpose text.
- Prove filtering precedes grouping and keeps counts accurate.
- Prove projection preserves order, stable IDs, and source immutability.

### Rendering tests

- Render equivalent output in Session, read-only Transcript, and Settings previews.
- Cover each regular level and representative Custom vectors.
- Cover entering Full detail, manual collapse, new streaming rows, and leaving Full detail.
- Cover Settings preview drafts, confirmed writes, and failed-write reversion.
- Cover missing metrics and pricing without placeholders.

### State and persistence tests

- Resolve local view over hub default over shipped default.
- Keep Desktop and Mobile values independent.
- Update every local pane and same-origin tab without a hub publication.
- Restore local values after restart.
- Clear one layout with **Use hub default**.
- Apply hub notifications only to followers.
- Handle malformed and blocked browser storage.
- Cover the legacy migration truth table and exact dual-write encoding.

### Hub tests

- Read shipped defaults from an empty store.
- Persist Desktop and Mobile patches independently across restart.
- Validate and reject malformed mutations.
- Reject stale same-layout revisions and return the canonical value.
- Notify every paired client with canonical values and monotonic revisions.
- Ignore stale notifications.
- Refresh after reconnect.
- Cover failed durable writes and concurrent same-layout and cross-layout edits.

### Scroll, responsive, and accessibility tests

- Preserve anchors for local, cross-tab, hub, and breakpoint changes.
- Preserve bottom-follow state.
- Restore focus when a row disappears.
- Switch at the existing 899px boundary and restore each class's local value.
- Keep the live control reachable on Mobile and in a narrow desktop pane.
- Verify radio-group keyboard behavior, visible labels, Custom state, sheet focus containment, focus return, and touch geometry.

### Verification gates

Implementation will run focused Go and Vitest tests, then:

- `make test-web`
- `make test-web-browser`
- `make lint`
- `make vet`
- `make test`

Frontend files under `src/` will pass the repository's required Biome formatting step before the frontend gates.

## Alternatives considered

### Persistent stepped toolbar

A toolbar would make the five levels maximally discoverable. It would also consume vertical space in every transcript and crowd narrow panes. The approved design keeps a compact trigger visible and opens the full control on demand.

### Native select

A native select would provide strong platform accessibility and compact Mobile behavior. It would hide the progression from Chat to Full detail. The approved radio track preserves named, directly selectable stops while looking like the requested slider.

### Layer-first editor

A layer stack would explain how each preset is composed and scale to more categories. It would expose implementation concepts to regular users. The approved design puts the named ladder first and moves independent fields under Advanced.

### Named view profiles

Saved profiles would support reusable combinations such as cost audits or hook debugging. They would add naming, deletion, references, versioning, and conflict semantics without demonstrated demand. The approved design supports one local active view and two hub defaults.

### Account-wide synchronization

Account-wide defaults would sync across independent hubs. Evener has no user principal or shared settings service. The approved design uses one hub's existing pairing boundary and keeps live choices browser-local.

## Acceptance criteria

The design is implemented when:

1. Every transcript uses one effective configuration and one projection pipeline.
2. The live control offers Chat, Intent, Tools, Activity, and Full detail on Desktop and Mobile.
3. Critical rows remain visible in every regular and Custom view.
4. Advanced fields control content, metrics, and diagnostics independently.
5. A live change updates all local sessions, survives restart, and does not change another machine.
6. Settings stores separate Desktop and Mobile defaults on the hub and syncs them to paired clients.
7. Settings shows stacked device cards with controls above production-backed example conversations.
8. The preview contains no real transcript data and matches live projection semantics.
9. Layout changes, local sync, hub updates, and breakpoint crossings preserve scroll and focus.
10. Explicit legacy preferences migrate locally without deleting or re-encoding pinned keys.
11. Storage and protocol failures remain visible and never produce a false saved state.
12. The focused tests and repository gates pass.
