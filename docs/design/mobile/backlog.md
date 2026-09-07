# Native mobile backlog

Owner: Bot, with Jesse setting product direction. Updated 6 September 2026.

This is the working issue backlog for the shared iOS/Android app. Add feedback
here with a stable ID, observed problem, acceptance criteria and evidence.
Closing an issue requires the final implementation and both-platform manual
evidence appropriate to its scope. A design study or passing unit suite alone
does not close a visual or interaction issue.

The full scope remains every current Evener workflow, multiple hubs and a
beautiful native experience. Voice/barge-in is outside v1. Feature authority
is current web behavior, server contracts and Jesse's requests. The old mobile
UI is not authority. The [capability inventory](../../superpowers/specs/2026-09-05-native-mobile-coverage.md)
and linked evidence are historical detail; entries below describe open work.

## Next work

### MOB-017 · P1 · Resolve Android input-dispatch ANR · Open

The installed Release app timed out while entering the launch-settings search
on Pixel 7/API 35. Android displayed “Evener isn't responding”; the event log
reported 5491 ms waiting for the R key. A delayed native stack caught the main
thread in Fabric view preallocation. Guest CPU pressure was also high, so the
trace does not yet establish app versus emulator causality.

Follow-up: the same app's main and JS threads were idle in a fresh dump, and
Android's Permission Controller also ANRed. After an explicit diagnostic app
restart, the same search and numeric edit/save/removal worked with no new ANR
events. Keep this issue open: recovery does not prove the cause or a fix.

Acceptance: reproduce with sequential UI actions, collect timely main/JS/render
thread evidence and system load, identify the cause, fix it where it originates,
and repeat keyboard/search/navigation checks on Android. Do not dismiss the ANR
because the app eventually responds or merely reduce observation frequency.
See [reconnect and Android evidence](hub-information-and-launch-settings.md#reconnect-draft-retention).

### MOB-011 · P1 · Move Submit to the composer controls row · In progress

Jesse: “the composer puts the submit button on the same row as text, rather
than putting it on the same row as controls. so it eats horizontal space from
the user input section. that's bad.”

The text input must use the full available composer width at ordinary text
sizes too. Submit belongs on the controls row with attachment/model/reasoning,
not beside the draft. This supersedes the earlier same-row draft/submit design
in the style guide and screen studies.

Acceptance: full-width short and multiline drafts on both platforms; Send/Steer
on the controls row with an intact native touch target; reachable model,
reasoning, attachment, Stop and Queue when applicable; deliberate wrapping at
large text sizes. Verify real software keyboards, long model labels, empty and
long drafts, and running-session states. Update the studies and style guide
before claiming visual acceptance. No controls may overlap or steal width from
the text area.

Follow-up native evidence: ordinary iOS software-keyboard placement is now
verified. Largest iOS text with keyboard and Android 2.0 text expose three
stacked control rows; full-width settings strand attachment on its own row.
The extra row is now corrected with separate settings and action rows, verified
with both real software keyboards. Largest iOS text still has cramped reading
space and a clipped draft line. See [layout evidence and remaining checks](fullwidth-composer.md#deliberate-accessibility-control-rows).

The [initial implementation and native evidence](fullwidth-composer.md) show
full-width drafts and controls-row submission. Android keyboard/send and iOS
stop were exercised against the isolated runtime. Empty drafts and ordinary
command layouts now have keyboard evidence on both platforms; Android 2.0
running controls and draft-preserving Stop are verified.

The largest-iOS-text `/project` overflow is corrected by bounding the entire
composer and scrolling oversized draft/actions separately from suggestions.
Both native command actions reached the project listing with large text and
real keyboards. Check smaller devices, landscape, screen-reader navigation,
long drafts and running controls in this layout before accepting the issue.
A 320 × 569 Android viewport at 200% text exposed a fixed-height allocation
failure; the adaptive cap now preserves editing and scrollable actions. Both
platforms reached the project listing again. iPad landscape, screen readers
and long drafts remain open. See [short viewport evidence](fullwidth-composer.md#short-viewport-allocation).

### MOB-001 · P1 · Use screen space deliberately · In progress

Jesse: “you're not making good use of visual space.” Current native screens
spend too much room on header controls, a separate healthy-connection row,
uniform transcript gaps, large secondary actions and composer feedback.
The result is less readable work visible at once, even after collapsing notices.

Resource settings now use compact named rows with full details on demand and
collapsed effective values. The same Android long-path fixture gains 46 physical
pixels before the input with unchanged Remove touch size; detail disclosure and
200% text/keyboard scrolling are exercised. See
[resource presentation evidence and gaps](hub-information-and-launch-settings.md#resource-presentation-refinement).

Acceptance:

- Review the entire viewport with identical content before/after: roster,
  conversation, running turn, command failure, question and approval.
- Consolidate orientation and session actions while keeping the destination
  unambiguous and every existing action discoverable. Long titles must gain
  useful room; accessibility text must not clip them or controls.
- Use different spacing for a new conversational turn, related activity,
  routine metadata and a consequential decision. Do not apply one large gap
  uniformly to every row.
- Keep draft, attachment, model/reasoning and primary action together. Bound
  suggestions/errors and input growth with the real keyboard visible.
- Measure viewport allocation to navigation, transcript, composer and keyboard
  on both platforms. Demonstrate more useful content visible without reducing
  readable text or native minimum touch targets. Record actual measurements;
  do not declare arbitrary pixel savings to be success.
- Exercise narrow screens, long labels, large text, light/dark appearance and
  screen-reader order. Expanded content must remain accessible and scrollable.

Evidence: [presentation study](presentation-study.html),
[current native screenshots](interruption-notices.md),
[composer and catalog behavior](command-completion.md).
The study is not the implemented outcome. [Header consolidation](session-menu.md)
now replaces Work/Session with one overflow icon and preserves all three
destinations. Both native destination/return paths pass; the narrow Android
title frame grows from 34 to 381 physical pixels with the same touch minimum.
Connection-row density and full-screen accessibility acceptance remain open.

The [first spacing correction](transcript-spacing.md) removes an empty header
slot and tightens routine-detail gaps. Android measurements show 284 pixels
reclaimed before the third user marker, with unchanged composer position and
touch-target height. This is partial progress, not closure of the viewport issue.

### MOB-002 · P1 · Establish a coherent presentation for every content family · Open

Routine notices, tool output, questions, failures and assistant prose still
look like unrelated controls assembled into a list. Give each a purposeful
presentation within one visual system: readable prose, expandable activity,
visible failures, focused decisions, and compact title-led session rows.

Acceptance: source-linked mapping for each current web item/decision type;
real-content studies and native examples; no classification guessed from prose;
no loss of raw details or required actions. Review full screens rather than
isolated components. Include code, tables, images, tool failure and notifications.

### MOB-003 · P1 · Preserve the reader's place and choices · Open

Disclosure choices now survive row/screen remounts in memory. Reading-position
restoration, long-list virtualization acceptance and process-restart behavior
remain incomplete. Streaming must not pull the reader away from older content.

Acceptance: native long-history scroll/return, background/relaunch, pagination,
image reflow and streaming tests; explicit decision on disk persistence;
separate hub/session state; no lost draft. See [disclosure evidence](interruption-notices.md).

### MOB-004 · P1 · Complete multiple-hub operation · Open

Saved profiles and isolated-hub draft tests exist. One foreground connection
does not establish the complete multiple-hub experience.

Acceptance: deliberate per-hub navigation and connection lifetimes; overlapping
session/item IDs; auth rotation, failed credentials, reconnect, removal and
switching during a pending operation. Native checks must prove destination
isolation and preserve unfinished work. Include physical LAN access and pairing.

### MOB-005 · P1 · Complete session and project navigation/management · Open

Finish current-web lifecycle, fork/edit/remove and organization workflows,
pin sections, remaining paging and branch recovery. Existing clear/project
command work is not full session-management acceptance.

Acceptance: every offered action maps to a current contract; native empty/error,
disconnected and uncertain outcomes; correct destination after rename, clear,
fork and removal; full navigation and final-head regression on both platforms.

### MOB-006 · P1 · Expose hub administration natively · Open

Provider authentication, instances, plugins/marketplaces, hub preferences and
upgrade are not yet complete native workflows. The [contract inventory](hub-administration-inventory.md)
splits implementation into MOB-012 through MOB-016; the parent remains open.

Acceptance: inventory current web/server operations, then split into individual
implementation issues before coding. Use purposeful native forms and lists,
not a generic RPC console. Include auth/device flows, live invalidation,
errors and consequential-action handling for each workflow.

### MOB-012 · P1 · Provider instances and API keys · In progress

The hub-bound provider controller now has automated coverage for retained data,
late responses after disposal, auth invalidation during reads, configuration write
refusal, overlapping writes, uncertain-write reconciliation without replay, and
pre-mutation read races. The first native list/detail/key-management screen now has [isolated-hub evidence](providers-evidence.md).
Creation and endpoint editing are implemented, with iOS creation and Android
edit/reset/removal exercised on the isolated hub. Credential testing now has
both-platform fixture success and automated sanitization/invalidation coverage. Sign-in,
reverse-platform form coverage and the complete manual acceptance matrix remain
open. iOS ordinary-text keyboard-open action reachability is now verified with
short strokes inside the visible form, including Save validation and Cancel.
Large-text and successful keyboard-open creation still need acceptance.

Child of MOB-006. Implement the server-derived provider list, instance detail,
create/edit/remove/default, credential test, set/clear stored key and logout.
Respect `writesRefused` for instance configuration while preserving independent
credential actions. Show credential source and diagnostics without exposing keys.

Acceptance: scripted provider tests plus both-platform native create/edit/default,
invalid key, clear/remove and reconnect flows on an isolated hub. Auth updates
refresh the correct hub; switching hubs during a request cannot apply its result
to the other hub. Follow [the contract inventory](hub-administration-inventory.md).

### MOB-013 · P1 · Provider browser and device sign-in · In progress

The hub/provider-bound sign-in controller now has eleven deterministic tests for
poll timing, authorization, expiry, browser fallback/completion, background
pause/resume, explicit poll retry and late-response disposal. Server states were
checked against `app_auth.go` (`pending`, `expired`, `authorized`). Native editor, AppState wiring and URL opening/code copy are implemented.
Both Release builds, 253 native tests and TypeScript pass. The [local auth fixture](auth-harness.md) now passes both flows through real hub
handlers and temporary credential storage using the native wire client.
iOS device authorization now has [manual browser-return evidence](providers-evidence.md#ios-device-authorization-browser-round-trip), including same-flow reconnect,
provider refresh and credential clearing, independently checked against fixture
state. [Android device-flow evidence](providers-evidence.md#android-device-authorization-browser-round-trip)
now covers Chrome first-run interruption, browser return, provider refresh and
credential clearing as well. [Browser redirect fallback](providers-evidence.md#browser-redirect-fallback-on-both-platforms)
now has native browser copy/paste and keyboard-open completion evidence on both
platforms. [iOS expiry/restart and explicit poll recovery](providers-evidence.md#ios-expiry-and-explicit-poll-recovery)
are verified against clock and OAuth-boundary fixture controls. [Android expiry/retry and canceled-flow hub switching](providers-evidence.md#android-expiry-retry-and-canceled-flow-hub-switch)
are also verified. iOS cancel/switch, a hub change during outstanding completion,
process death and provider denial remain unverified.

Visual follow-up: use proportionate space for the authorized state; show native
sign-in guidance instead of a primary CLI instruction, with server diagnostics
available in a deliberate disclosure. Copy feedback is observed; clipboard paste
verification remains open.

Integration constraint verified in `ConnectionProvider.tsx`: backgrounding closes
the hub connection. The sign-in flow must therefore be owned above the connected
provider list and survive same-hub transport replacement. `setConnection` now
pauses/resumes that flow, fences late replies and marks interrupted start/browser
completion uncertain without replay. Three additional tests cover same-flow
reconnect, old-client replies and interrupted browser completion. Changing hubs
must dispose the flow; never rebind it to a different hub. Browser URLs/redirect
submissions are transient and must not be saved with hub profiles.


Child of MOB-006; builds on MOB-012. Device start/poll and browser fallback must
use server-returned URLs, flow IDs and polling intervals. Provide code copy,
open-browser and redirect completion as supported by the current web flow.

Acceptance: pending, success, denial, expiry, restart, background/foreground and
hub switch on both platforms. Never restart or replay sign-in merely because
an observation timed out. Use scripted auth for deterministic automation;
real provider acceptance requires an explicitly chosen test account.

### MOB-014 · P1 · Marketplaces and installed plugins · In progress

The installed-plugin controller now implements all six plugin mutations with
plugin + marketplace identity, external-update refresh, stale-read fencing,
disposed-hub isolation and read-back after uncertain writes without replay.
Five deterministic tests pass; the full native suite passes 258 tests, TypeScript
and touched-file Biome pass. This is controller coverage, not rendered native
acceptance. The installed list/detail screen is wired with filtering, native switches,
upgrade, installation disclosure and confirmed removal. Both Release builds pass;
manual navigation and empty/error rendering are verified on both platforms
against isolated hubs. [Owned-marketplace lifecycle evidence](plugins-evidence.md) now covers live row
arrival on both devices, iOS disable, Android re-enable/auto-upgrade, Upgrade on
both, Android removal and iOS detail closing through notifications. The fixture
is directory-backed; fetching a newer Git revision remains unverified. The marketplace controller now implements list/select/browse, add/remove/source
refresh, external invalidation, separate read errors, selected-marketplace removal
and disposed-hub/stale-response fencing. Six deterministic tests cover those
contracts; all 264 native tests, TypeScript and touched-file Biome pass. [Browse and native catalog install](plugins-evidence.md#native-browse-and-installation)
are now wired and manually verified on both platforms. The hub-directory add form now has ordinary software-keyboard submission and
invalid-path recovery evidence on both platforms, plus cross-client removal in
both directions. The Android pass exposed and repaired keyboard obstruction after
a rejection. Directory-source refresh was exercised on Android; Git URL/GitHub
cloning and iOS source refresh remain unverified. See the [form evidence](plugins-evidence.md#ios-add-form-keyboard-recovery-and-reverse-removal).
Hub-directory selection now uses the web path-completion contract, with
keyboard-open lookup/selection and child listing verified on both platforms;
Android parent navigation is also verified. Four controller tests bring the native
suite to 268. See [directory evidence](plugins-evidence.md#hub-directory-selection).
Picker-to-marketplace submission and source-type reselection now have both-platform
native evidence at 666423050. Large directory lists, realistic catalog density,
visual hierarchy, large text, screen readers and full failure acceptance remain.

Presentation direction: compact Installed and Browse views, one tap target per
plugin row, actions in detail, and marketplace source management alongside Browse.
Keep hub identity visible and preserve source diagnostics in details. Match the
current web's Git URL, GitHub owner/repo and hub-local directory inputs; a local
path names the hub filesystem. Do not imply access to the phone filesystem.

Child of MOB-006. Browse/add/remove/refresh marketplaces; install, upgrade,
remove, enable/disable and set automatic upgrade for plugins. Use compact lists
with detail actions and preserve marketplace identity alongside plugin name.

Acceptance: both-platform native lifecycle on an owned fixture marketplace,
notification refresh, partial loading errors, cross-device changes, long lists,
large text, disconnect during mutation and hub switching without blind replay.

### MOB-015 · P1 · Hub information and launch settings · In progress

The native Hub settings screen now exposes the typed read-only overview and
links to Providers/Plugins, replacing their separate session-list actions. Four
controller tests cover refresh failure, stale responses, disposal and Go omitted
empty/zero values; all 272 native tests and both Release builds pass. Runtime
and storage are manually inspected on both platforms against SecondHub; iOS
agents and both-platform empty Codex/MCP states are verified. See
[hub-information evidence](hub-information-evidence.md). The launch-layer controller now has ten deterministic tests and a real isolated-hub
save/readback/restoration smoke pass. Native scalar editors are wired, with
296 passing native tests and TypeScript. Numeric save/removal has evidence on
both platforms; iOS also verifies invalid-number rejection. The environment
collection editor now has 300 passing native tests, both Release builds and
native save/reload/removal evidence for embedded equals and empty values.
iOS rejects a stale open environment sheet after Android removes its override;
independent AppWire reads verify restoration. See
[environment evidence and remaining checks](hub-information-and-launch-settings.md#environment-collection-editor).
Fallback editing now preserves order, rejects duplicates and distinguishes
explicit none from inheritance. Both native platforms and independent AppWire
readbacks exercise these states; 304 native tests pass. See
[fallback evidence and remaining checks](hub-information-and-launch-settings.md#model-fallback-collection-editor).
Path lists and file browsing now have 308 passing native tests and both Release
builds. Android exercises file selection with spaces, directory navigation,
file-type rejection, directory save/reload and restoration, with independent
AppWire reads. iOS path checks remain pending because the Mac locked. See
[path evidence and gaps](hub-information-and-launch-settings.md#path-collection-editor).
MCP server specifications now share the resource editor: 311 native tests and
both builds pass; Android validates/rejects commands, saves/reloads, retains a
backgrounded draft and removes the fixture, with independent wire readback.
[MCP evidence](hub-information-and-launch-settings.md#mcp-server-collection-editor)
separates this from real MCP server/session qualification, which remains open.
Launch model fields now browse the real hub catalog,
reuse the web's provider/recent/metadata rows, and retain selections on reconnect.
Native iOS search/select/save and Android read/removal have evidence. Open scalar
sheets reject newer field values, with two-device conflict/reopen/restoration
checks on both platforms. Reconnect drafts have controller coverage and
both-platform background/return evidence. Android save/removal and
automatic iOS readback pass on retry; MOB-017 remains unresolved. See
[launch progress](hub-information-and-launch-settings.md#native-scalar-editors).
Nonempty Codex/MCP fixtures, two-hub native isolation, failure/reconnect, large text,
screen readers and visual acceptance remain open.

Child of MOB-006; coordinate editable launch layers with MOB-007. Show runtime,
storage, agents, Codex launches and version information as read-only where the
web is read-only. Support existing schema-backed launch configuration, plugin
and skill directories, and MCP configuration through launch-layer contracts.

Acceptance: distinguish inherited/effective/editable values; validate paths on
the selected server; preserve unrelated launch fields; refresh launch changes.
Test two hubs with different settings and a late response after switching.
No fabricated hub.toml editor or server filesystem picker using phone paths.

### MOB-016 · P1 · Hub upgrade and recovery · Open

Child of MOB-006. Expose the existing hub upgrade operation with clear target hub,
progress/result and recovery after a connection drop. Version information comes
from the server; do not invent an available-update catalog.

Acceptance: deterministic success/failure/disconnect behavior, no automatic
mutation replay, and native end-to-end upgrade on an isolated disposable hub.
Never use the production hub as an upgrade test fixture.

### MOB-007 · P1 · Complete creation and launch configuration · Open

Finish path assistance, large model/harness catalogs, vision choices, launch
layers/schema and repository trust against current web/server behavior.

Directory browsing is now available in the creation form. Both native platforms
selected a hub directory and created a session there, independently confirmed by
AppWire. Hub rejection messages are retained alongside creation uncertainty;
see [creation evidence and remaining scope](session-creation.md).
Project launch-layer editing now has iOS save and Android inherit/readback
evidence, with independent scope checks. The settings round trip preserves a
valid selected model and revalidates reasoning against the refreshed catalog.
Repository trust now has iOS approval and Android stale-file rejection/review/
approval evidence, confirmed independently. 316 native tests and TypeScript pass.
Per-session schema options now have native scalar creation/readback evidence
on both platforms and share the saved-layer field editors. 318 native tests pass.
Creation model/reasoning now share the composer footer with Create, with a
searchable model sheet and collapsed harness choices. Both native model-picker
round trips preserve the draft. An intermittent Android footer-overlap observation
is retained in the creation evidence; keyboard and accessibility acceptance
remain incomplete. 321 native tests pass.
Opening image selection/removal and creation now have native evidence on both
platforms, independent SDK PNG readback and viewer checks; 324 native tests pass.
Creation drafts still need durable storage. Live item-image projection is fixed;
remaining running-work qualification is tracked under MOB-008.
Plugin selection, per-launch collection/precedence failure
coverage and complete project-layer/trust accessibility qualification remain open. The repository-trust SDK recipe now reproduces stale review and approval
independently under MOB-018.

Acceptance: real native creation with valid/invalid paths and configuration,
trust decisions, creation failure/uncertainty, keyboard and accessibility
coverage. Keep advertised options server-derived.

### MOB-008 · P1 · Finish running-work and decision acceptance · Open

Live image projection is fixed: snapshots and item events share image mapping,
with replacement/removal and stale-read/page regression coverage. Both native
apps displayed and opened a newly sent image before Stop; independent SDK reads
confirmed active turns. See the live-image evidence in session-creation.md.
New-session opening-window races and remaining image qualification stay open.

Goals, tasks, activity, queue operations, approvals and questions have partial
implementation/evidence. Complete remaining paging, concurrent updates,
resolution on another device, stale actions and uncertain delivery scenarios.

Acceptance: both-platform real harness E2E, fault recovery without blind replay,
reachable decisions with the keyboard open, and accessible large-content views.
Do not infer real execution/resumption from an injected notification alone.

### MOB-009 · P1 · Complete rich transcript and attachment interaction · Open

Finish authenticated image/gallery handling, multiple images, copy/link/code
interaction, device lifecycle and large content. Source-path links remain a
separate capability question; showing a link does not mean it can be opened.

Acceptance: native image selection, durable draft attachments, authenticated
viewing and errors, copy/open return paths, screen readers and process death.

### MOB-010 · P1 · Native accessibility, performance and release qualification · Open

Simulator Release builds are not distribution or physical-device qualification.

System font-scale changes recreate the Android activity and return resource
settings to Sessions. In one 1.0-to-2.0 check, the initial connection failed and
explicit Reconnect recovered; returning to1.0 connected automatically. Investigate
activity/location restoration and capture the failed connection's cause before
claiming lifecycle acceptance. The hub stayed reachable during the failure.

A manifest-only `fontScale` candidate retained an open numeric draft but failed
to resize its mounted text, even after background/resume. Reopening the editor
used the new size. Keep this unresolved until both draft retention and live text
resizing pass; see [candidate evidence](hub-information-and-launch-settings.md#android-font-scale-recreation-investigation).

Acceptance: iOS/Android physical devices, signing/distribution, measured scrolling
and input latency under streaming load, memory/leak checks where indicated,
large text, screen readers, reduced motion, light/dark and native back/keyboard
gestures. Run final-head repository gates and a workflow-level release matrix.

### MOB-018 · P1 · Supported client library and independently implementable protocol · Open

Jesse wants protocol documentation sufficient to implement a client without
reading the server or web code, backed by an API client library and runnable
examples covering the complete protocol.

The shared TypeScript transport now has a standalone package boundary. A clean
consumer installed its tarball, validated declarations, imported it through ESM
and CommonJS, and ran the read-only inspection recipe against an authenticated
isolated hub. The reference generator includes nested wire objects and JSON
representations. See the [client guide](../../appwire-client.md).

The generated catalog has 88 methods and 35 notifications at this snapshot; the
three recipes cover eight methods and one notification. The opt-in project-layer
recipe verified mutation, notification, effective-value readback and restoration
from a separate tarball consumer against the isolated hub. The packaged coverage
report lists gaps. Repository trust also has an independently installed recipe
for stale-hash rejection and fresh revision confirmation. This
is not completion: add fixture-backed creation, streaming/rejoin, mutation
receipt, approval, queue, navigation/management, provider, plugin, trust and
upgrade recipes, with method-specific errors, presence semantics and recovery.

Acceptance: an independent client consumer can implement every supported flow
from the guide and wire reference; the library and examples run outside this
checkout; a catalog-derived coverage matrix accounts for every supported method
and notification (including reserved-method rejection); deterministic fixtures
exercise failures and disconnects as well as success. Keep protocol docs and
recipes current as native functionality is implemented. Publishing is separate
from producing and testing a local package.

## Tracking rules

- Add new observations under the relevant issue or create a stable new ID.
  Preserve Jesse's concrete feedback and the screen/state that exposed it.
- Split large issues into actionable children when work starts; keep the parent
  scope visible. Do not relabel a partial implementation as completion.
- Record commit, platform/build, fixture and verification limits when closing.
- Inline argument completion is not a demonstrated web parity gap; do not
  restore that previously mistaken requirement without a product reason.
- This file is local repository tracking, not a GitHub Project. GitHub Projects
  could not be inspected with the current token's scopes. No remote issue or
  project was created by establishing this backlog.
