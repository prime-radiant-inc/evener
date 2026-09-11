# Skills lifecycle design

- Date: 2026-09-10
- Revised: 2026-09-11 after roborev design reviews 7814 and 7839.
- Status: design sections approved by Jesse; written specification awaiting review.
- Base: `627e6c000491795773f7e6f542808298363abc5f`

## Purpose and decisions

Evener already discovers skill metadata and loads instructions on demand. The
[implementation study](../../research/2026-09-10-skills-implementation-study.md)
found inconsistent loading context, weak inline invocation guarantees, and no
ordinary-skill restoration contract after compaction.

Implement all five recommendation areas through shared activation, explicit
selection, agent-selected disk reload, invocation controls, and portable discovery.
Jesse approved these choices in the design discussion:

- Guaranteed inline invocation comes from explicit selection. Plain typed inline
  slash mentions keep their current model-interpreted behavior.
- Before compaction, show the skills loaded in this session and ask which to
  reload. The runtime reloads the selected skills from disk after compaction.
- An omitted or malformed selection produces a system notification listing all
  previously loaded skills and their descriptions, directing the model to reload
  them as relevant. An explicit empty selection means reload none.
- Share loading and rendering across routes, preserve the original request, and
  deliver complete instructions or report a loading failure.
- Role preloads keep their permanent-prompt and frozen-delegate lifetime.
- Existing sessions track future ordinary activations only. Do not reconstruct
  historical activations from old tool calls or transcript prose.
- Support invocation controls and portable directories without granting execution
  permissions or adding executable skill templates.
- Implement with TDD, run the required gates, obtain independent review, and open
  a PR. Implementation starts after review of this written specification.

## Architecture

Keep three responsibilities separate:

1. **Skill discovery and loading:** `agent/skill` owns metadata parsing, canonical
   resolution, diagnostics, file loading, content identity, and shared rendering.
2. **Session activation and lifetime:** the session owns invocation provenance,
   successful-activation records, current-context availability, reload selection,
   compaction publication, and restoration.
3. **Client selection:** AppWire transports canonical skill selections alongside
   ordinary input. The web composer exposes them through its existing completion
   menu and preserves them through the input lifecycle.

Use the existing tool-state, typed-turn, session-snapshot, and fold-publication
boundaries. Do not introduce a second session store or independent compaction
transaction. New internal type and helper names belong in the implementation
plan; the externally visible selection field specified below is `reload_skills`.

## Shared loading and activation

### Identity and rendering

A loaded skill carries its canonical catalog name, description, source path,
base directory, body, and a digest of the loaded file bytes. Rendering preserves
that identity and directory guidance around the complete frontmatter-stripped
body. Treat metadata and file contents as data when rendering delimiters.

These supported activation routes use this loader and renderer:

- `use_skill` returns skill instructions as tool content.
- Leading slash and explicit selection supply selected instructions as
  separately identifiable user context.
- Role preloads supply them through the existing activated-skills prompt section.
- Compaction reload supplies them through a typed post-compaction notification.

Shared rendering does not require identical API message roles. Keep the original
user text separately identifiable in live input, saved history, and client
projection. Arguments remain user context; do not substitute templates or execute
shell directives inside skill bodies. Supporting files remain lazy, live reads.

Generic file reads remain file reads, including the catalog-directed `read_file`
fallback in profiles without `use_skill`. Inspecting `SKILL.md` is not sufficient
evidence that the caller intended activation. This work does not register those
reads as skill activations or guarantee their restoration. Such profiles get
tracked activation through user selection, leading slash, and role preloads;
their model-selected file reads retain ordinary history/compaction behavior.
Document this boundary explicitly rather than inferring intent from file paths.

### Successful activation and current availability

Persist a session inventory keyed by canonical name. An entry retains its frozen
role-preload identity, if any, separately from its latest successful ordinary
activation. Ordinary records contain source, description, digest, invocation
provenance, and whether this session has explicit user authorization for that
source. Reactivation updates the ordinary record; historical versions remain
transcript evidence rather than accumulating inventory entries. A fresh ordinary
activation from a different source replaces that record and reevaluates
authorization. It never replaces the frozen preload.

A successful file read alone is insufficient. Record success only when the full
rendered instructions are admitted for model delivery. Failed reads, invalid
metadata, policy refusals, and partial outputs produce no success event or new
successful inventory entry. A failed reinvocation does not erase an earlier
successful inventory entry; it also does not mark the new content available.

Track **loaded previously** separately from **complete content currently present**.
Only the latter permits deduplication. Check canonical identity, source, and
rendered-content identity. A hit during tool execution is provisional because
compaction and history projection can still remove that body. `use_skill` may
return a structured already-present outcome with those identities and a brief
notice, but the session retains a pending delivery obligation for that invocation.
Preserve this obligation across retry and restart.

Revalidate provisional hits against the final outgoing context before dispatch.
If the complete body is still present, satisfy the obligation without repeating
it or emitting a new-body-delivery event. Otherwise, use the shared loader to
load the same source under the invocation's policy and supply complete instructions
through a typed activation notification linked to the original invocation. Report
changed disk content and any failure explicitly; the notification supersedes the
earlier already-present outcome. Apply current-activation budget priority and
complete-or-fail admission, including atomic failure for explicit selections.
Do not run another compact/reload cycle or duplicate a body another reload already
restored. Record delivery only after final admission succeeds.

Explicit invocation retains its selection record and original user text/arguments
while omitting only duplicate bodies. Keep invocation-specific arguments separate
so deduplication cannot discard a new request's arguments.

Remove the default suffix-only `use_skill` truncation behavior. Admit complete
skill content using the existing request token estimator, model context window,
and output-reservation budget. Configured output limits must likewise produce an
explicit complete-delivery failure rather than successful partial instructions.
Allow normal compaction of older history to make room, but never compact away or
mask a new activation before its first complete delivery. If the final request
still cannot fit, report a context-budget loading failure; do not silently shorten
the body, claim success, or enter a compact/reload loop. Explain that large skills
should move conditional detail into reference files.

Budget priority is existing mandatory prompt content (including frozen preloads),
the current request and its new activations, then compaction reloads in selected
order. Reserve space for the required handoff/reminder and failure metadata in
the same estimate. Reloads that do not fit fail individually; they cannot evict
a new activation or trigger another compaction in this dispatch. If mandatory
metadata and the current request cannot fit, fail dispatch visibly.

Keep existing tool/activation UI grouping. Attach causal tool-call identity where
available; standalone activations remain visible without duplicate tool rows.

## Explicit invocation and input lifecycle

### Resolution

Leading slash keeps existing command precedence, exact skill lookup, and unique
plugin-suffix resolution. A known but unreadable or disallowed skill produces a
visible activation failure rather than falling through to ordinary chat. Unknown
slash text keeps its existing ordinary-input behavior. Ambiguous skill suffixes
report candidates rather than choosing one.

Structured selection names an exact canonical skill and bypasses command-name
collisions. Extend AppWire's input items with a skill selection represented by
`type: "skill"` and `name: "<canonical-name>"`. Resolve it on the server using the
session catalog. Reject unknown identities and skill items containing a client
path or body; clients cannot turn selection into an arbitrary file-read request.

Advertise support through `ThreadCapabilities.skillInput`. The new client must
not send skill items when this capability is false or absent, including against
an older server. Keep existing selected drafts recoverable and show that the
target does not support selection; do not silently convert chips to slash text.
The new server rejects unsupported skill items before forwarding input to a
source. This is a fail-closed capability check, not an older-server emulation
or downgrade path. Update the wire schema and generated SDK together.

Selections apply to the submitted request, including queued requests and genuine
user steering. Resolve and load at consumption, so a queue does not carry a stale
body. Preserve selections with the original input through retries, mutation
identity, transcript replay, and restore. Fresh explicit selection is
all-or-nothing: resolve, load, check policy, and admit the combined instructions
before dispatch. If any selection fails, dispatch no dependent model work and
record no new successful activations from that request. Keep the original input
and selections visible with the failure for correction and explicit retry. The
same rule applies to queued and steering input: record the failure against that
input rather than dropping it or automatically retrying it as ordinary prose.
For failed steering, the in-flight turn continues without any of that steering
message, including its prose. Surface that failure prominently so the user can
resend the urgent text without the failed selection or interrupt the turn.

### Composer

Reuse the web command/skill completion menu. Selecting a skill adds a removable
selection indicator and records canonical identity separately from ordinary
text. Replace the completion token with the request-level selection, leaving
surrounding prose intact. Do not also leave an operative leading slash command
in the submitted text. Removing the indicator does not insert slash text.
Command selections continue to use command behavior. A skill indicator
means that skill is selected for the request regardless of later prose edits;
removing it cancels that selection. Explain that behavior through its accessible
label. Deduplicate repeated selection of the same canonical identity.

Draft persistence, thread switching, queue editing, failed-send restoration,
retry, and steering must retain the selections. Successful submission clears
only the submitted draft's selections. Preserve attachments and surrounding text.
Typing or pasting an inline slash token alone never creates structured selection.
The terminal and plain CLI retain their existing leading-slash route; this work
adds no new terminal completion menu. AppWire clients can use the structured form.

## Compaction and reload contract

### Selection before compaction

Build the loaded-skill list from successful session records, not tool-call names
or the remaining history prefix. Include canonical names and descriptions even
when earlier compactions removed their activation turns.

Automatic note elicitation and the context-pressure nudge for profiles with
`compact_context` present this list and ask which skills to reload after
compaction. Without that tool, the nudge lists the skills for awareness and keeps
its existing actionable note advice; it does not request a structured selection
the model cannot submit. Automatic elicitation can still collect the selection.
Preserve the existing must-keep note behavior.

Add optional `reload_skills` to `compact_context`: an array of exact canonical
names from this session's loaded inventory. Automatic note elicitation returns
the same selection in one `<skill-reload-selection>` block containing a JSON
object with a `reload_skills` field, alongside its free-text note. Remove a valid
selection block from the handed-forward note after parsing it. Missing, multiple,
or malformed blocks cannot authorize reloading. Preserve the free-text note even
when selection parsing fails. Do not infer selection from incidental skill
mentions in the note or summary. Selection presence is independent of note
presence.

| Selection | Meaning |
|---|---|
| Valid nonempty list | Reload those skills in listed order; collapse duplicates |
| Explicit empty array | Reload none |
| Omitted or null | Selection absent; emit the all-loaded-skills reminder |
| Malformed or containing an unknown name | Report invalid selection and use the reminder |

This table governs compaction that actually runs. Schema-invalid tool calls fail
normal argument validation and schedule no compaction.

`note_to_self` remains required. A skill-only request supplies
`note_to_self: ""` and `reload_skills`; the empty note clears any previous pinned
note, exactly as today. A present selection, including an explicit empty array,
requests compaction. Empty note plus no instructions and absent/null selection
retains today's clear-note-without-compaction behavior. An invalid selection in
an accepted request does not discard that request's valid note; it uses the
fallback reminder. Reject a second pending compaction request without overwriting
the first note or selection. An accepted selection belongs to one compaction
cycle and is consumed only by an actual, successfully published compaction.

Publishing unchanged history during a normal model request is not a compaction:
it neither consumes selection nor emits a reload/reminder. A published checkpoint
that performs actual compaction does count, even without a later summary layer.
If a forced request terminates without publishing its own compaction, report that
its selection was not applied and cancel only that request's selection, using its
generation to preserve newer intent. Keep the existing pinned-note behavior. A
losing fold attempt alone performs no cancellation; cancellation belongs to the
terminal request outcome after retries, not to an unpublished attempt.

A competing winner does not adopt a forced request's selection. It uses its own
selection, or emits the reminder if it has none. If the forced request exhausts
its retries, the model receives its selection-cancellation notice alongside any
handoff from the winner. The notice identifies which intent was lost; it must not claim
that no compaction occurred. This parallels the existing warning for unapplied
`compaction_instructions`.

An accepted automatic elicitation belongs to an operation and associated note
generation, including when its note is empty. It waits for the next actual
published compaction that captures and claims those generations. A no-op,
unpublished attempt, or drop in pressure leaves it pending; a checkpoint-only
compaction can fulfill it. Clearing or replacing the associated note cancels
that automatic operation's selection with a visible superseded outcome. Normal
publication claims consume it rather than cancelling it. A later fold with a
different generation cannot adopt the cancelled selection.

Latch automatic elicitation on a nonempty pinned note **or a pending operation**.
A selection-only response, including `[]`, therefore closes the latch. Accept
elicitation results only if the captured note generation is still current and
no competing operation has been accepted; stale responses cannot replace newer
intent. Do not re-elicit or overwrite an automatic selection while its operation
is pending. Explicit clear-note-without-compaction remains available to cancel
it; a second compaction request still follows the rejection rule above.

Automatic elicitation remains best-effort. Failure, no client, a preexisting note
without a selection, and forced compaction without a selection all take the
reminder path. Do not add a model round solely to repair a missing selection.

### Publication and model delivery

After the final successful fold, reload selected ordinary skills from disk before
the next model request. Use the shared loader, policy checks, and complete-body
admission. Keep the recorded source identity: do not silently retarget a skill
to a different collision winner. A different source requires fresh activation.
A changed digest at the same source loads current instructions with a visible
change notice, including old and new invocation-flag values when those changed.
Record those flag values with successful activation metadata so the comparison
survives compaction and restart. Same-source user authorization still follows the
policy below. Missing, unreadable, disallowed, or oversized skills yield a
structured failure identifying the skill; they remain unavailable for dependent
work. Continue with successfully reloaded skills and explicit failure notices,
without pretending the entire selection succeeded.

For absent/invalid selection, emit a post-compaction system notification carrying
all previously loaded canonical names and descriptions. Label each entry as
already present, reloadable, requiring explicit user activation, or unavailable,
using the policy below. Tell the model to reload relevant reloadable skills;
mark frozen preloads as permanently present and do not ask it to reload them.
If `use_skill` is unavailable, identify file reads as the untracked fallback
described above. No bodies are automatically loaded by this reminder. Omit it
when the inventory is empty. An explicit empty selection produces no reminder.

Keep the inventory complete; do not silently truncate the all-loaded list to a
recent subset. Its cost is metadata, not permanently pinned bodies, and remains
subject to the normal request budget. Budget failure must be visible.

Inventory and pending compaction state survive restart. Persist the pending
operation in the existing session snapshot: its generation, forced/automatic
origin, compaction instructions, associated note generation, selection presence
and values, and whether compaction has published but reload delivery is pending.
The operation owns its selection; never persist a selection without its owner or
attach it to an unrelated future automatic compaction. The existing pinned-note
slot still owns the note text and keeps its generation-checked claim semantics.
Save accepted operation state, including elicitation results, through the existing
snapshot machinery before relying on its durability; waiting for the current
post-publication `maybeAutoSave` would leave a restart gap. Save cancellation and
phase changes as well, and surface save failures. Store operation metadata, not
skill bodies, and avoid writes on unchanged pressure checks or fold retries.

Restart is interruption, not terminal cancellation. Resume an unpublished forced
operation at the first safe seam before normal model dispatch. Restore an
unpublished automatic operation and its elicitation latch, but let it wait for
normal pressure-driven compaction; restoration must not force a fold. A published
operation of either origin finishes only its pending reload/delivery; it does
not compact again. A previously recorded delivery is not repeated. Restored
operations use their origin-specific completion and cancellation rules above.
Preserve these states across the final checkpoint/summary boundary used by
`ResumeHistory`, using publication records to reconcile a stale snapshot. Derive
current-context availability from complete retained content, never from inventory
membership alone.

Use the existing generation-checked fold publication transaction. A losing fold
must not consume selection, mark instructions delivered, append reload results,
or emit success. Concurrent activations and steering must survive publication;
new inventory entries absent from an earlier selection remain discoverable at
later compactions. Avoid duplicate bodies already complete in the retained tail.

### Role preloads

Configured role preloads keep their existing permanent-prompt placement and
frozen-delegate restoration contract. They use the shared renderer and appear in
the successful inventory, identified as preloads. Their complete instructions
already survive compaction, so selecting one requires no disk reload or duplicate
injection. This is the approved exception to ordinary-skill disk reload. Ordinary
activations never become permanent role preloads.

When a name has both a frozen preload and an ordinary activation, show both
provenances in that inventory entry. A compaction selection targets its latest
ordinary activation and uses ordinary disk reload. With only a preload, selection
is a no-op. An ordinary activation of changed instructions does not rewrite or
override the frozen role prompt; the two remain separately identified and follow
the existing message-role precedence.

### Existing sessions and delegates

An older saved session without the new ordinary-activation inventory starts with
an empty ordinary inventory. Preserve its existing history and role-preload
restoration. Track subsequent successful activations using the new contract; do
not backfill from old events, tool calls, or body text. Previously loaded ordinary
skills are outside the reload/reminder guarantee until activated again through a
tracked route.

A new delegate owns a separate inventory, initially seeded only by its role
preloads. It does not inherit the parent's ordinary inventory, pending compaction,
or user authorization. `fork_context` retains its existing history-copy behavior;
copied text alone does not become a child activation record. Child activations
then use the normal contract. Resuming the same delegate restores its own state.

## Invocation controls and permissions

Parse `disable-model-invocation` and `user-invocable` as strict optional booleans,
with defaults false and true respectively. Invalid values make that skill
unavailable and produce a diagnostic rather than permissive defaults. If its
canonical name is known, a higher-precedence entry with invalid controls must
not expose a lower-precedence permissive entry under that name.

`disable-model-invocation` filters the general model catalog;
`user-invocable` filters user completion. Catalog filtering uses current metadata.
These are advertisement views: `use_skill` resolves against the full canonical
catalog and the session's source-scoped authorization record, then applies the
runtime policy. A name's absence from the advertised model catalog must not block
an authorized same-source reload or bypass policy for an unauthorized request.
Authorization for a prior user activation is session-local, scoped to canonical
name plus source, and survives resume of that session. It does not confer tool
permissions or authorize a replacement source. Model-generated text and delegate
prompts cannot create this authorization.

The runtime policy below applies after valid current metadata is loaded. A fresh
model activation means one without prior user authorization for that source.
Reloading an authorized source is continuation of its prior activation even when
the model chooses it through `reload_skills` or calls `use_skill` again.

| Route | Invocation rule |
|---|---|
| Genuine user leading slash or structured selection | Require `user-invocable: true`; either value of `disable-model-invocation` is allowed; success records user authorization |
| Model `use_skill` without prior user authorization | Require `disable-model-invocation: false`; either value of `user-invocable` is allowed |
| Model `use_skill` for a prior user-authorized source | Allow either flag combination; this is an authorized reload, not fresh model activation |
| Runtime post-compaction reload | Allow prior user authorization; otherwise require `disable-model-invocation: false`; `user-invocable` does not gate this continuation route |
| Model interpretation of a plain inline slash mention | Same rule as model `use_skill`; the text alone never grants user authorization |
| Configured role preload | Configuration-driven activation; retain its approved frozen lifetime independently of these user/model route flags |

Thus a user-only skill with no prior user authorization cannot be invoked by
plain inline slash prose; use leading slash or explicit selection. A skill whose
metadata becomes user-only cannot acquire authorization from that change. The
reminder identifies such an entry as requiring explicit user activation; if
`user-invocable` is also false, identify it as unavailable through ordinary routes.

These controls govern invocation routes, not filesystem ACLs or tool execution.
Reading skill text cannot expand sandbox access or grant execution permissions.
Catalog filtering also applies to the file-read fallback when `use_skill` is
unavailable; do not advertise hidden skills through that alternate prompt.

Preserve `allowed-tools` in string and array forms, but explicitly diagnose that
Evener neither grants nor restricts tools from it. Preserve useful descriptive
metadata without treating it as executable configuration. Diagnose unsupported
behavioral controls such as `context: fork`; do not implement their behavior.
Show diagnostics in session startup/inspection and client-visible skill details,
with source and machine-readable category. Missing optional skill directories
are normal; malformed existing files and unreadable configured sources are not.
Document the stricter parsing: quoted booleans and loose values such as `"yes"`
are invalid, with a diagnostic showing the field and expected boolean type.

## Portable discovery and trust

Use one discovery policy for live sessions and cold client catalog reads.
Lowest to highest bare-name precedence:

1. Embedded skills.
2. `~/.agents/skills/`.
3. Evener's existing user skills directory.
4. Repository root through cwd, at each level `.agents/skills/` then `skills/`.
5. Explicitly configured skill directories in their existing order.

A deeper project directory beats a shallower one. At the same level, `skills/`
beats `.agents/skills/`. Plugin skills retain qualified names and the existing
plugin merge behavior. Report collisions with both sources and the chosen winner;
command/skill collisions remain distinguishable in the completion UI.

Portable directories use the same trust behavior as existing skills: discovery
reads metadata, activation loads inert text, and subsequent actions remain subject
to normal tool permissions and sandboxing. A skill is untrusted input regardless
of its wrapper. Do not add automatic `.claude/skills/` discovery, new project-trust
UI, executable templates, shell substitution, or implicit permission grants.

## Implementation stages

The implementation plan must split this work into ordered, independently gated
stages, with scoped commits and reviewable diffs inside the requested single PR:

1. **Skill package:** loader, renderer, metadata controls, diagnostics, and
   portable discovery. Gate with package tests and discovery/catalog parity.
2. **Session activation:** inventory, provenance, complete-body admission,
   dispatch-time deduplication revalidation, and existing invocation routes.
   Gate at the provider-request boundary, including a dedupe hit followed by a
   pre-dispatch fold, and through persistence/preload restoration.
3. **Compaction:** selection protocol, automatic elicitation ownership and latch
   in `maybeElicitNoteBeforeCompaction`, pre-dispatch integration in
   `session_model_call.go`, persisted operation state, reload, reminder, and
   restart/publication behavior. Gate with deferred automatic compaction,
   selection-only elicitation, and competing-publication cases, plus the session
   activation regression cases.
4. **Explicit client selection:** AppWire input and capability, generated API/SDK
   surfaces, then composer integration. Regenerate SDK/types in the same stage
   as the wire change, before building browser tests against them. Gate with
   protocol/SDK tests, frontend tests, and real browser coverage.

Update documentation alongside each stage. Finish with cross-route live
evaluations, whole-repository gates, and independent implementation review. This
sequence is a delivery constraint; detailed TDD tasks follow written-spec approval.

## Verification and delivery

Follow [repository testing policy](../../developing-evener/testing.md). Use TDD
with a scripted provider only at the LLM boundary and real Evener behavior below
it. Assert structured state, events, wire inputs, request shapes, and opaque
fixture-data delivery. Do not pin natural-language prompt or warning prose.

Required deterministic acceptance coverage:

| Area | Evidence |
|---|---|
| Shared loading | Tool, slash, structured selection, and role preload deliver the same canonical provenance and complete fixture instructions at the provider boundary |
| Input | Original prose, arguments, attachments, and explicit selections survive normal submit, queued input, user steering, retry, and restart; fresh mixed-selection failure is atomic and retains retryable input |
| Resolution | Canonical names, unique/ambiguous plugin suffixes, command collisions, and stale selections take the specified routes |
| Completeness | Oversized and configured-limit cases never report partial success; a new activation survives any pre-dispatch compaction |
| Deduplication | Identical complete outgoing content deduplicates; a provisional hit followed by a pre-dispatch fold or projection reloads complete content through a causal notification; failed revalidation corrects the outcome without false delivery; retry/restart retain the obligation; new user arguments remain intact |
| Failure | Missing files, malformed metadata, denied routes, and failed reloads produce no false activation events |
| Compaction choice | Agent-forward nudge, automatic elicitation, and self-requested compaction use the successful inventory; valid, empty, omitted, null, malformed, and unknown-name selections differ correctly |
| Automatic lifetime | An attempt with no published compaction leaves the elicited choice pending; a later generation-matched compaction, including checkpoint-only, consumes it; clear/replacement cancels only its operation; lower pressure or restart never forces a fold |
| Elicitation latch | Selection-only responses, including `[]`, prevent repeated calls while pending; stale concurrent responses cannot overwrite newer notes or operations; a new cycle can elicit once both the note and pending operation are absent |
| Tool availability | Without `compact_context`, the nudge requests no unsupported structured response; automatic elicitation can still select reloads |
| Reload | Current disk bytes reach the next request; digest and invocation-flag changes are reported; missing/replaced sources and budget failures remain explicit |
| Repeated lifetime | Multiple compactions and restart retain the full inventory, preserve pending choice, and avoid replaying consumed choice |
| Publication | Losing attempts cause no ghost activation, silent selection loss, dropped steering, or duplicate reload; a competing winner does not adopt a forced selection and its handoff accompanies terminal cancellation; unchanged publications cause no handoff; forced no-ops cancel only their own selection; checkpoint followed by summary retains the final handoff |
| Invocation policy | Advertisement and runtime policy agree on their separate roles; full-catalog resolution permits authorized hidden-name reloads and rejects unauthorized ones; generated input cannot forge authorization, and flags change no execution permissions |
| Discovery | All precedence levels, namespaced plugins, collision diagnostics, malformed files, and live/cold catalog parity |
| Browser | Selecting/removing skills, accessible indicators, draft switching, queue editing, failed-send recovery, and steering retain exact identities and text |
| Role lifetime | Existing frozen descriptor restoration and permanent prompt content remain intact; a same-name ordinary activation retains distinct provenance and the specified selection target |
| Raw file reads | Full and partial `SKILL.md` reads do not create activation or authorization records; profiles without `use_skill` retain tracked explicit/preload routes |
| Budget priority | New activation plus compaction reload pressure preserves the new activation, rejects excess reloads individually, and causes no additional compact/reload cycle |
| Pending operation | Acceptance is saved before publication, including selection-only elicitation; restart runs forced operations at the safe seam but keeps automatic operations pressure-driven; restart after publication completes only pending delivery; no selection attaches to an unrelated fold; save failures are visible and unchanged checks do not save again |
| Note semantics | `note_to_self` remains required; empty note with a present selection clears the pinned note and requests compaction |
| Historical sessions | A saved session without inventory resumes without backfill, then records new verified activations |
| Delegates | New delegates and forked history import no parent inventory, pending operation, or authorization; delegate resume restores its own records |
| Protocol support | False/absent `skillInput` prevents selected input submission; unsupported sources reject skill items and preserve input for correction |

Add separately opted-in live evaluations for implicit model selection and inline
prompt compliance: operative requests, quotations, fenced code, paths/URLs,
negations, multiple skills, and near-miss names. Also evaluate whether the model
returns a useful reload selection and uses the fallback reminder. Prove a
known-good live smoke case on each participating model before interpreting
behavior comparisons. Default tests require neither credentials nor network.
Record configuration failures, skips, and unrun evaluations explicitly.

Update `docs/skills.md` with the loading, selection, lifetime, metadata, directory,
trust, and authoring contracts. Correct its inaccurate Codex comparison. Update
API/SDK documentation and generated surfaces through their normal generators.
Keep the original study as a dated pre-change report, linking to the implemented
contract when delivery is complete.

Before delivery, run impacted tests, the SDK gate, `make merge-approval-gate`
(lint, vet, and full tests including the frontend), and `make test-web-browser`.
Run Biome's formatter on touched frontend `src/` files before frontend gates.
Obtain independent implementation review, fix findings, and rerun affected gates.
Commit only scoped paths, push the isolated branch, and open a PR containing the
study, approved spec, implementation, tests, documentation, and actual evidence.
No source or test implementation is included in this specification commit.
