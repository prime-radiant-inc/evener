# Skills lifecycle design

- Date: 2026-09-10
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

All invocation routes use this loader and renderer:

- `use_skill` returns skill instructions as tool content.
- Leading slash and explicit selection supply selected instructions as
  separately identifiable user context.
- Role preloads supply them through the existing activated-skills prompt section.
- Compaction reload supplies them through a typed post-compaction notification.

Shared rendering does not require identical API message roles. Keep the original
user text separately identifiable in live input, saved history, and client
projection. Arguments remain user context; do not substitute templates or execute
shell directives inside skill bodies. Supporting files remain lazy, live reads.

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
Only the latter permits an already-loaded response. Deduplication checks canonical
identity, source, and rendered-content identity against the final outgoing
context, after compaction and history projection. Keep invocation-specific user
arguments separate so deduplication cannot discard a new request's arguments.

Remove the default suffix-only `use_skill` truncation behavior. Admit complete
skill content using the existing request token estimator, model context window,
and output-reservation budget. Configured output limits must likewise produce an
explicit complete-delivery failure rather than successful partial instructions.
Allow normal compaction of older history to make room, but never compact away or
mask a new activation before its first complete delivery. If the final request
still cannot fit, report a context-budget loading failure; do not silently shorten
the body, claim success, or enter a compact/reload loop. Explain that large skills
should move conditional detail into reference files.

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

The context-pressure nudge and automatic note elicitation present this list and
ask: which skills should be reloaded after compaction? Preserve the existing
must-keep note behavior.

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

An invalid selection must not destroy a valid handoff note. A skill-only
`compact_context` request, including an explicit empty array, requests compaction;
empty note plus no instructions and no selection retains today's clear-note
behavior. Reject a second pending compaction request without overwriting the
first note or selection. An accepted selection belongs to one compaction cycle
and is consumed only by an actual, successfully published compaction.

Publishing unchanged history during a normal model request is not a compaction:
it neither consumes selection nor emits a reload/reminder. If a forced request
terminates without an actual compaction, report that no compaction applied its
selection and cancel only that request's selection, using its generation to
preserve newer intent. Keep the existing pinned-note behavior. A losing fold
attempt alone performs no cancellation; cancellation belongs to the terminal
request outcome after retries, not to an unpublished attempt.

Automatic elicitation remains best-effort. Failure, no client, a preexisting note
without a selection, and forced compaction without a selection all take the
reminder path. Do not add a model round solely to repair a missing selection.

### Publication and model delivery

After the final successful fold, reload selected ordinary skills from disk before
the next model request. Use the shared loader, policy checks, and complete-body
admission. Keep the recorded source identity: do not silently retarget a skill
to a different collision winner. A different source requires fresh activation.
A changed digest at the same source loads current instructions with a visible
change notice. Missing, unreadable, disallowed, or oversized skills yield a
structured failure identifying the skill; they remain unavailable for dependent
work. Continue with successfully reloaded skills and explicit failure notices,
without pretending the entire selection succeeded.

For absent/invalid selection, emit a post-compaction system notification carrying
all previously loaded canonical names and descriptions and the instruction to
reload them as relevant. Include unavailable/policy status when known. No bodies
are automatically loaded by this fallback. Omit the reminder when no skills have
ever been loaded. An explicit empty selection produces no fallback reminder.

Keep the inventory complete; do not silently truncate the all-loaded list to a
recent subset. Its cost is metadata, not permanently pinned bodies, and remains
subject to the normal request budget. Budget failure must be visible.

Inventory and pending selection survive restart. Preserve them across the final
checkpoint/summary boundary used by `ResumeHistory`. Restored current-context
availability is derived from complete retained content, never from inventory
membership alone. Restart itself does not replay a consumed compaction selection.
If a published reload is still pending at restart, finish it before model work;
if its delivery was recorded, use that evidence to avoid duplicate restoration.

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

## Invocation controls and permissions

Parse `disable-model-invocation` and `user-invocable` as strict optional booleans,
with defaults false and true respectively. Invalid values make that skill
unavailable and produce a diagnostic rather than permissive defaults. If its
canonical name is known, a higher-precedence entry with invalid controls must
not expose a lower-precedence permissive entry under that name.

- `disable-model-invocation: true`: hide from the model catalog and reject fresh
  model-initiated `use_skill` activation.
- `user-invocable: false`: hide from user completion and reject leading-slash and
  structured user selection.
- Both restrictions may coexist; configured role preloads remain configuration-
  driven activation. Ordinary model and user routes are unavailable in that case.
- A previously explicit user activation authorizes later reload of that same
  source in the session, including after restart. Model-generated text or a
  delegate's starting prompt must not manufacture that user authorization.
- Reload through a model-only route must still satisfy current invocation policy.
  Current metadata becoming user-only cannot silently grant user authorization.

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
| Deduplication | Identical complete outgoing content deduplicates; changed or removed content reloads; new user arguments remain intact |
| Failure | Missing files, malformed metadata, denied routes, and failed reloads produce no false activation events |
| Compaction choice | Agent-forward nudge, automatic elicitation, and self-requested compaction use the successful inventory; valid, empty, omitted, null, malformed, and unknown-name selections differ correctly |
| Reload | Current disk bytes reach the next request; digest changes are reported; missing/replaced sources and budget failures remain explicit |
| Repeated lifetime | Multiple compactions and restart retain the full inventory, preserve pending choice, and avoid replaying consumed choice |
| Publication | Losing/concurrent folds cause no ghost activation, lost selection, dropped steering, or duplicate reload; unchanged publications cause no handoff; forced no-ops cancel only their own selection; checkpoint followed by summary retains the final handoff |
| Invocation policy | Catalog and runtime checks agree, prior human authorization enables reload, generated input cannot forge it, and flags change no execution permissions |
| Discovery | All precedence levels, namespaced plugins, collision diagnostics, malformed files, and live/cold catalog parity |
| Browser | Selecting/removing skills, accessible indicators, draft switching, queue editing, failed-send recovery, and steering retain exact identities and text |
| Role lifetime | Existing frozen descriptor restoration and permanent prompt content remain intact; a same-name ordinary activation retains distinct provenance and the specified selection target |

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
