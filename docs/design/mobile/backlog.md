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

The [initial implementation and native evidence](fullwidth-composer.md) show
full-width drafts and controls-row submission. Android keyboard/send and iOS
stop were exercised against the isolated runtime. Large text, iOS keyboard,
running-control density and remaining acceptance states are still open.

### MOB-001 · P1 · Use screen space deliberately · In progress

Jesse: “you're not making good use of visual space.” Current native screens
spend too much room on header controls, a separate healthy-connection row,
uniform transcript gaps, large secondary actions and composer feedback.
The result is less readable work visible at once, even after collapsing notices.

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
The study is not the implemented outcome. Header consolidation is being
investigated; no header change has been made for this issue yet.

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
upgrade are not yet complete native workflows.

Acceptance: inventory current web/server operations, then split into individual
implementation issues before coding. Use purposeful native forms and lists,
not a generic RPC console. Include auth/device flows, live invalidation,
errors and consequential-action handling for each workflow.

### MOB-007 · P1 · Complete creation and launch configuration · Open

Finish path assistance, large model/harness catalogs, vision choices, launch
layers/schema and repository trust against current web/server behavior.

Acceptance: real native creation with valid/invalid paths and configuration,
trust decisions, creation failure/uncertainty, keyboard and accessibility
coverage. Keep advertised options server-derived.

### MOB-008 · P1 · Finish running-work and decision acceptance · Open

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

Acceptance: iOS/Android physical devices, signing/distribution, measured scrolling
and input latency under streaming load, memory/leak checks where indicated,
large text, screen readers, reduced motion, light/dark and native back/keyboard
gestures. Run final-head repository gates and a workflow-level release matrix.

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
