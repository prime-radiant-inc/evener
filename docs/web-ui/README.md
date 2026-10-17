# Evener Web Hub — UI/UX

Design documentation for the web hub (`cmd/evener-hub`).

## Current

- **[Session activity](../product/session-activity.md)** — ownership of scoped
  delegate, shell-job and watch reads, compact navigation, pagination and shared
  recovery. Use this contract when changing activity surfaces or subscriptions.
- **[design-system.md](design-system.md)** — the authoritative design law, starting with
  the [editorial-instrument rationale](design-system.md#design-model-an-editorial-instrument):
  conversation for understanding, evidence for verification and controls for intervention.
  It records behavioral consequences, tradeoffs and review questions, then tokens,
  the editorial system (warm paper/ink, serif reading, flat structure), inline tool/delegate
  grammar and provenance, source coverage, type, space, motion, the cadence instrument,
  and the widget library under
  `cmd/evener-hub/frontend/src/widgets/`.
- **[decisions.md](decisions.md)** — historical choices from the 2026-06 visual
  brainstorm onward, with point-in-time source verdicts. Read this before changing a
  transcript or navigator behaviour: it distinguishes a reasoned departure from
  a regression, and several apparent regressions are neither.
- **[ux-plan-2026-07.md](ux-plan-2026-07.md)** — the five-participant study of
  the SPA against the old server-rendered build, and the plan that came out of
  it. Cited from live source comments.
- **[typography-spacing-critique-2026-09-06.md](typography-spacing-critique-2026-09-06.md)** —
  measured critique of type scale, measure, rhythm and balance on desktop and
  phone. The ramp, measure and rhythm work landed; the current editorial system
  supersedes its two-face premise. See the canonical guide for shipped values.
- **[keybindings.md](keybindings.md)** — the keybindings dispatcher: registry,
  scope stack, precedence layers, per-binding policy flags, and how to
  register an action or a chord, the shipped default binding map (including
  the Phase 3 navigation chords and the AskDock keyboard contract), plus the
  hub-persisted override sync (payload contract, validation split, failure
  posture).
- **[parity/](parity/)** — the behaviour-parity checklists the React rewrite was
  graded against, mined from the legacy hub before it was deleted. The code they
  cite is gone, so their `path:line` citations no longer resolve; they survive
  because thirteen live source comments cite *them* for why a behaviour is the
  way it is, and every wave plan used them as its acceptance floor.
- **[specs/](specs/)** — two dated feasibility designs (multi-pane workspace,
  observer auto-open). Point-in-time; the multi-pane one is still named as
  source of truth by its implementation plan.

## Settings

Settings groups controls by task:

- **Preferences:** Appearance, Conversation display, Input & shortcuts, Notifications.
- **Agent setup:** Providers & credentials, Agents, AGENTS.md, Session defaults.
- **Extensions:** Plugins, Skill directories, MCP servers.
- **System:** Hosts, Devices, Hub, Storage, About.

Search matches destination names and control keywords, such as font, Enter,
tokens, marketplace and plugin directory. Results open the owning page.
Appearance is the default. Explicit unknown or retired `general`, `display`
`plugins` and `inrepo` section URLs open Appearance, without aliases or a remembered
section overriding that request. A host-qualified URL retains its host scope.
AppShell replaces retired or unknown URLs before placing their settings pane,
so mobile URL synchronization cannot restore retired params or discard Forward history.
Project settings remain reachable through the Project settings action in each sidebar project menu.
They combine repository-file review and explicit trust with local overrides for the
selected host and working directory. Shared `.evener/launch.toml` settings apply
only after approval of the reviewed hash; `.evener/launch.local.toml` overrides
remain separate. Discovery checks that directory, not its ancestors. Repository
inspection failures do not prevent editing local overrides.

Conversation display edits one device layout at a time, initially the current
device class. Both shared layouts remain available through the layout selector.
Use the shared default clears only the selected layout's browser-local override.
The preview stays visible; Technical details contains revisions and inventories.

On phones, settings opens as a full-width list. Drilling into a page and using
Back preserves the list's search and scroll position. Desktop keeps the list
beside the selected page.

Input & shortcuts brings Enter-to-send and keyboard customization together.
Enter-to-send and character-key accessibility stay visible. Customize shortcuts
opens the searchable editor; folding or filtering it preserves captured chords
and pending writes. Synchronization failures and retry remain visible outside it.
Enter-to-send and character-key enablement remain browser-local; keyboard
overrides retain their existing hub synchronization. Appearance and notification
preferences retain their existing browser storage. Conversation display keeps
its Desktop/Mobile hub defaults and browser overrides. Organization does not
change persistence or introduce new synchronization.

Notifications shows Loud for only when sound or desktop notifications are enabled.
Hiding that control preserves its selection. Browser tab indicator is independent
of those channels.

Plugins has one home with Installed, Browse and Sources views. Sources contains
marketplaces and additional plugin directories under one host picker. Directory
loading and editing recover independently of plugin and marketplace inventory
failures; inventory retries preserve directory drafts. Host switching keeps
reads and saves bound to the selected host. Skill directories remain separate.

The former General summary's runtime, listen address, masked token and token
age belong to Hub; state/index paths, run directory and page size belong to Storage; version
and credits belong to About. Hub and Storage fold these read-only facts under
Technical details; updates, resident controls and browser-cache management stay
visible. Existing stores and APIs remain authoritative.

## Older history demand

The browser [transcript reader](../../cmd/evener-hub/frontend/src/panes/session/transcript/useTranscript.ts)
retains older-page demand across transient failures, unresolved reads, and
navigation away from a session. Returning resumes paced recovery without another gesture.
Jump to live explicitly abandons that reader's demand, including demand from
its removed predecessor panes. An independently open session or transcript
keeps its demand even when its tab is inactive. Workspace pane identity and
session ref determine which readers remain open; replacing a pane record does
not itself abandon its demand. The thread store owns loaded content and page
merging, and an already-started read can still merge after cancellation.

The workspace Dock integration activates an already selected pane by activating
its group, preserving the mounted transcript and its native scroll position.
Selecting a different tab still activates that panel. Transcript scroll and
history recovery remain owned by the existing reader and virtual viewport.

## Browser transcript cache

The browser loads a session's transcript from the entry-backed repository, which
is the source of truth for what it holds. The server transcript stays
authoritative, and the [transcript repository](../../cmd/evener-hub/frontend/src/stores/transcriptRepository.ts)
is the browser's single owner of recorded-history ingestion and persistence; it
writes keyed item, turn and coverage rows to the existing `evener-session-cache`
IndexedDB. First foreground opening of a background-only session restores the
latest 200 bodies; older retained identities remain locally pageable. Within the
same source revision, the repository remembers the oldest foreground-loaded
position. Releasing the last foreground reader reclaims ended bodies under the
existing resident and pending byte budgets, not that loaded boundary. A local
reopen restores the required range, including complete tool payloads and
explicit-empty bodies, while accepted newer versions win over the opening cut.
A second foreground owner shares the established reader without opening again.

The thread store owns the bounded local-opening hold and automatic source
recovery. Complete current local coverage needs no activation read; usable
retained bodies may also avoid a read under the existing authority rules even
without a cache certificate. Body restoration alone creates no coverage,
freshness or server cursor. Missing required bodies or metadata that cannot be
reconstructed from retained history, failed writes and opening deadline expiry
fall back to source recovery. The accepted baseline supplies the display and
cursor for subsequent older paging and live updates. Reconnect and source changes
still reconcile, retiring obsolete cursors. Clear, deletion, final release and
superseded openings fence late publication, and the opening hold settles on
success, recovery or cancellation. The pane's read view and scroll owner retain
reading placement across responsive remounts; a genuine close and new pane still
starts at latest.

The legacy client-side cache is not migrated: old
cached transcripts are discarded on upgrade and rebuilt from the server. Active
unfinished output stays in memory and may exceed the background budget, and
charged ended-body overage is an accepted exception. Clear from Settings
suppresses persistence for the existing cache lifetime; a new explicit
foreground acquisition and authoritative read, or a new browser lifetime, can
then establish a fresh one. The affected surfaces are the session transcript
pane and its older-history reader; the settings storage row owns Clear.

DockHost retains saved tab labels while an inactive pane's session name is
unavailable. Loaded navigation or live thread metadata updates the label when
it recovers. Changing the pane's type or session ref retires the old title hint.
Hydrated panes can publish their own tab titles without extra resource reads.
The cascade tab keeps its registered selected-leaf title; its whole-view heading
does not replace that label on remount.

## Transcript reading position

Browser transcript readers retain the visible entry across viewport width and
height changes. A width change keeps approximate progress through the entry's
usable reading depth, and an entry whose tail is showing keeps that tail in
place rather than filling the pane. A height-only change, such as later
composer-height settlement after a pane widens, keeps the reading line where it
was. The existing
[transcript registration](../../cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts)
owns that intent; VirtualList owns measurement and committed geometry.
Mounted and remounted readers use the same reflow policy. Retained placement
waits while the thread store's ref-scoped local opening restores bodies, then
retries against accepted content and committed geometry. A settled empty or
anchorless projection completes its normalized fallback instead of waiting for
nonexistent entries. Restoration also waits for scroll read-back. Newer
viewport scrolling or an explicit positioning command supersedes older work
through the pane-lifetime [read view](../../cmd/evener-hub/frontend/src/panes/session/transcript/transcriptReadView.ts).
Reflow preserves editor and neighboring-pane focus. Row-only height changes,
end following, keyed older-page prepend and history recovery keep their existing
owners.
An explicit display change restores focus to the same entry or its visible proxy,
even when the viewport also changes. If that entry disappears, focus moves to the
Transcript region.

## Session Overview

Both session action menus offer **Overview** for inspection. It opens the shared
sidebar on the intended session, retaining that session's category choice.
The categories are Agents, Jobs, Watches, Tasks and About. About shows session
details through the shared Details renderer and adds no footer counter.

`/status` opens About and keeps it open on repeated use. Its completion reads
“Show session details in Overview” and remains searchable by details and info.
Saved standalone Details panes remain available. `/tasks` retains its existing
standalone pane toggle. Desktop footer counters still focus their owning pane
before selecting their category. A checked desktop session-menu Overview action
closes that session's sidebar; the rail action opens the requested session.

Phones use the same sidebar as a full-screen surface. Opening transfers focus
inside after the menu closes, and Tab stays inside until dismissal. Closing
returns to a visible opener or a visible session-actions control for the intended
session, preferring the originating pane. Existing footer categories retain
their owning-pane fallback. If child drilling replaces those controls, focus
returns to the intended parent's visible breadcrumb outside Overview without
navigating or creating a pane. Desktop Overview remains nonmodal.

Once a navigation location confirms the already open parent, the shell ends
route deferral. Opening a read-only child keeps that child focused while the
address bar retains the parent URL on both phone and desktop.

## Workspace geometry

[DockHost](../../cmd/evener-hub/frontend/src/shell/DockHost.tsx) owns the desktop
Dockview integration. Its empty floating overlay host stays out of layout so
cached grid dimensions cannot enlarge the document during a native window resize.
Dockview still owns layout and resize observation. Populated floating windows
keep their placement, permitted overhang and native pointer input, including
input outside the workspace bounds. The integration does not clip those windows.
The [shell guard](../../cmd/evener-hub/frontend/scripts/shellguard/run.mjs)
checks native resize and real floating-window preservation in Chrome.

## Exited-session controls

An exited session's follow-up keeps its model selector, session actions and Send
visible before typing. Its empty editor rests at one line and expands to three
when focused. It stays expanded while focus moves through the controls or their
pop-up menus and dialogs, including overlays outside the card's DOM. Focus is
tracked while the session is live, so stopping preserves an already-focused
editor. Leaving the composer shrinks only the empty editor; drafts and attachments
keep it expanded. Send remains disabled until there is content and sending is
available. The inline session chrome owns activity discovery at rest and while
engaged, without a second discovery mount.

The model picker remains available when the session advertises `changeModel`.
Selecting a model uses `thread/model/set`; the hub owns resuming a cold session
and applying the choice. Closing the desktop picker restores focus to its
trigger, including after a selection.

## Directory fields

All directory selection uses the [shared directory-picker contract](design-system.md#directory-selection-one-shared-interaction). Read it before adding or changing a path field; older plans and parity checklists describe retired interactions.

## The examples are the running app

There is no static example gallery to keep in sync. Run the dev server and open
**`/dev/widgets`** — every widget, every documented state, in both themes,
rendered from the real tokens. `src/dev/WidgetGallery.test.tsx` fails the build
the day a widget has no section, so it cannot silently go stale.

**`/dev/type`** does the same for the type system itself: Inter operations, Source Serif 4
reading (normal and italic), JetBrains Mono evidence, the size ramp, the
three line-heights, the eyebrow recipe, the four rhythm steps and a paragraph
at each measure, in both themes, so a ramp change is reviewed as a picture
rather than a diff.

**`/dev/surfaces`** renders real composite surfaces, including transcript evidence,
for checking the implemented editorial system in context. The guide's
[coverage summary](design-system.md#editorial-source-coverage) distinguishes direct
transcript/shell/form/composer/ledger work from inherited shared surfaces.
Source coverage is not browser acceptance. The guide's separate
[acceptance snapshot](design-system.md#acceptance-and-limits) records the draft PR,
automation, panel endorsements, pending native retest and preview refresh, and validation limits.

The galleries are dev-only: `App.tsx` gates it behind `import.meta.env.DEV`, so a
production build does not contain it and there is no link to it from the app.

## history/

The 2026-06 visual brainstorm, and the planning docs for the hub that no longer
exists.

`history/mockups/` holds 23 topics, each rendering four labelled alternatives,
plus `TARGETS.md` (what each topic set out to fix) and the tokens they were
built on. `history/examples/` holds the golden reference screen, a hard-cases
screen, the three explored visual directions, and the brief all three had to
render.

These remain historical evidence of interaction and hierarchy decisions;
`decisions.md` names their specific gaps and the superseding editorial direction. They are not maintained, their tokens
share two names with the live ones, and the app they were built against was
deleted in `660376f78`.

Older plan documents cite these files at their previous paths, without the
`history/` prefix. Those documents are point-in-time records and were left
unedited; the filenames are unchanged, so the paths still resolve by search.

## Goal

External-product polish for a power-user, dark-first agentic coding tool.
Conversation-first, first-class subagents, honest liveness. The current rationale and
tradeoffs live in the [design system](design-system.md#design-model-an-editorial-instrument);
[decisions.md](decisions.md) preserves the original principles and their later departures.
