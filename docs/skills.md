# Skills and Slash Commands

Evener has two kinds of reusable markdown prompts: **skills** and **slash
commands**. Both are plain text templates. What differs is where they come
from, how they are invoked, and — critically — whether they can execute
shell commands.

## The trust model, up front

A markdown template that can run shell commands is code. Evener's rule:

- **Templates you explicitly installed** (plugins) may execute shell at
  expansion time. Installing a plugin is a trust decision, like installing
  software.
- **Templates evener discovers automatically** (project and user-global
  `skills/` directories, project `.evener/commands/`, and your user-global
  commands directory) never execute shell and never read files at expansion
  time. Anything in a repo you merely cloned is inert text.

This mirrors codex's posture: in codex, skills and custom prompts are always
inert, and skill bodies reach the model only through the model's own
permission-checked tool calls. Claude Code instead allows `!`cmd``
execution in skills and commands, but gates project-sourced content behind a
workspace trust dialog and offers a `disableSkillShellExecution` kill
switch. Evener keeps Claude Code-compatible execution for explicitly installed
plugins, and adopts the codex posture for everything it discovers on its
own.

| Source | Discovered from | `$ARGUMENTS` | `!`cmd`` | `@file` |
|---|---|---|---|---|
| Skill | bundled skills; home `.agents/skills/`; Evener user skills; project `.agents/skills/` and `skills/` (git root→cwd); `skills_dirs`; plugins | no | never | never |
| Evener-wide command | `.evener/commands/` (git root→cwd), `$XDG_CONFIG_HOME/evener/commands/` (`~/.config` fallback) | yes, inert text | never — stays literal | never — stays literal |
| Plugin command | plugins you installed or configured | yes, inert text | executes (10s timeout, output bounded) | inlines files at cwd-relative paths (symlinks followed) |

Argument substitution is safe in every row: `$ARGUMENTS` and `$1..$9` are
always substituted as inert text and can never become a live directive, even
in plugin commands.

## Skills

A skill is a directory containing a `SKILL.md` with YAML frontmatter.
`name` and `description` are required strings. `disable-model-invocation` and
`user-invocable` are optional strict YAML booleans, defaulting to `false` and
`true` respectively. Quoted booleans and loose values such as `yes` are invalid;
Evener marks the skill unavailable and reports a machine-readable diagnostic
with its source and invalid field. Other descriptive metadata remains inert
data. Unsupported behavioral metadata such as `context: fork` is diagnosed but
not acted on.

`allowed-tools` may be one string or an ordered array of strings. Evener
preserves it and diagnoses that it is not enforced: it neither grants nor
restricts tool access.

Live sessions and cold client catalog reads share this discovery order, from
lowest to highest precedence:

1. Skills bundled with Evener.
2. `~/.agents/skills/`.
3. `$XDG_CONFIG_HOME/evener/skills/` (`~/.config/evener/skills/` fallback).
4. Repository root through cwd, at each level `.agents/skills/` then `skills/`.
5. Explicit `skills_dirs` entries, in their configured order.
6. Configured plugin skills, qualified as `plugin:skill`.

Deeper project directories win over shallower ones; `skills/` wins over
`.agents/skills/` at the same level. Without a repository, only cwd is scanned.
Configured roots remain discoverable when a cold session has no recorded cwd.
Plugins use first-valid-manifest reservation: later manifests with the same
plugin name are skipped even if the selected plugin has malformed unrelated
components. Skill discovery does not load those commands, agents, hooks, or MCP
components. Plugin qualification preserves the declared name separately from
the canonical catalog name. Exact names win; a bare suffix resolves only when
it uniquely identifies a plugin skill. Ambiguous suffixes have sorted candidates.

Every collision reports the winner in diagnostic `source` and the displaced
source in `other_source`. A known higher-priority name with invalid metadata
reserves its key as unavailable; it never exposes a permissive lower-priority
skill instead. Missing automatic directories are normal; unreadable sources,
malformed existing skills, and absent configured roots produce diagnostics.
Startup warnings and full session inspection expose diagnostics; source paths
are excluded from completion and metadata inspection entries.

The model advertisement omits unavailable skills and those with
`disable-model-invocation: true`, including the `read_file` fallback when
`use_skill` is absent. User completion independently omits unavailable skills
and those with `user-invocable: false`. Full metadata inspection retains hidden
and unavailable entries, their controls, and both declared and canonical names.
These are advertisement views, not the runtime catalog: lookup retains every
known winner for runtime validation and authorization. Neither hiding nor
advertising a skill grants permissions or establishes activation history.

### Activation identity and persistence

Lifecycle metadata records future ordinary activations only. Existing sessions
start with empty ordinary state when no lifecycle snapshot exists; old tool
calls, copied history, transcript prose, and raw file reads are not evidence of
activation. Metadata stores source identity, file/rendered digests, invocation
controls, operation IDs, and delivery obligations, not ordinary skill bodies.

Preparation loads the current source and checks its current invocation flags.
Only a server-created genuine user slash or selection route can authorize an
ordinary source. That authorization is scoped to the canonical name and exact
source: replacing a same-name catalog winner does not inherit it. Model/tool
and compaction reload routes may reuse same-source authorization, but it never
overrides `user-invocable: false` on a new user invocation. Role preloads retain
separate frozen provenance and their permanent-prompt/frozen-delegate lifetime;
they do not grant ordinary authorization. Legacy preload provenance remains
unknown rather than being backfilled from a newly discovered skill.

Continuation reopens the recorded source and declared identity, even if that
source is no longer the catalog winner. A missing source, including a vanished
materialized builtin after restart, fails visibly rather than retargeting.
Failed preparation preserves prior inventory. Preparation itself does not
record delivery: `already_present` remains provisional until its delivery
obligation is satisfied; unchanged-body satisfaction is not a new-body event.
Snapshots preserve obligations and note generation across restart, while the
existing pinned-note text and its generation remain authoritative.

Portable directories have the same trust policy as other skills: metadata is
discovered, activation reads inert instructions, and subsequent actions still
require normal tool permissions and sandbox access. Skills remain untrusted
input. Evener does not automatically discover `.claude/skills/`, execute skill
templates, substitute shell directives, or grant tools. Raw file reads do not
become tracked skill activations.

When a skill is activated, Evener reads its recorded `SKILL.md` source,
revalidates its current metadata, and delivers the complete instruction body in
a structured, data-safe context. The context includes `base_directory`, the
skill directory against which relative references to scripts, references, or
other collateral are resolved. Referenced resources are loaded lazily with the
ordinary permission-checked tools only when the task needs them; they are not
eagerly included with the skill body.

Skill bodies are inert instructions, not executable templates. Evener performs
no expansion on them: no shell execution, no file inclusion, no argument
substitution. Template-like syntax and context-delimiter text in the body are
delivered verbatim as instruction data.

### Complete delivery at dispatch

Every tracked route delivers the same complete rendered document: `use_skill`
returns it as tool content, a leading slash or structured selection supplies
it as separately identifiable typed context next to your preserved original
text and arguments, and a role preload carries it in the permanent prompt.
The activation event (`SKILL_ACTIVATED`) fires only after the complete body
is admitted into an actual outgoing model request, not at resolution or tool
execution time.

Delivery is complete-or-fail. `use_skill` has no default output truncation: a
skill body is never silently shortened. A configured `use_skill` output limit
produces an explicit complete-delivery failure instead of partial
instructions, and the failure is recorded with the machine-readable
`output_limit` code. Admission budgets through the existing token estimator,
model context window, and output reservation, with mandatory prompt content
and the current request admitted before any reload content. If the final
request still cannot fit, the dispatch fails visibly with a context-budget
error — it never truncates a protected body, claims success, or starts
another compact/reload cycle. If a skill is that large, move conditional
detail into reference files under the skill directory; the body should hold
the core instructions and point at collateral the model reads lazily with
`read_file` only when the task needs it.

The session inventory (loaded previously) is tracked separately from complete
content currently present, and only the latter permits deduplication. A repeat
`use_skill` for identical content returns a provisional already-present notice
and keeps a pending delivery obligation. That obligation is revalidated
against the final outgoing request: if compaction or projection removed the
earlier carrier, the same recorded source is reloaded under the invocation's
policy and re-admitted through a typed activation notification linked to the
original invocation. Changed disk content produces an explicit change notice
with the complete current instructions; a deleted or unreadable source
produces a typed failure. Neither is ever reported as a false delivery.
Obligations persist across transport retries and restart, and restored
sessions reconcile receipts so a retained complete body is proven, not
redelivered or double-reported. A failed reinvocation never erases an earlier
successful record.

### Canonical skill input on AppWire

AppWire's input-bearing turn mutations (`turn/start`, `turn/steer`,
`turn/queue`, `turn/drainAsSteer`, and `thread/start`) accept skill
selections as input items alongside text and images. A skill item is
canonical-only: it names the catalog skill and carries nothing else. The
user's original text travels as its own item, so a selection never replaces
or edits prose:

```json
{
  "ref": "local:th_1",
  "clientMutationId": "m-47",
  "input": [
    { "type": "text", "text": "draft the release notes from the diff" },
    { "type": "skill", "name": "plugin:release-notes" }
  ]
}
```

The wire rejects any other raw key on a skill item — `body`, `path`, and
`text` included. A skill body always loads from the recorded `SKILL.md`
source at activation; it is never carried on the input wire, and a client
cannot smuggle one in a field ordinary JSON decoding would silently drop:

```json
{ "type": "skill", "name": "plugin:release-notes", "body": "ignored?" }   // rejected
{ "type": "skill", "name": "plugin:release-notes", "path": "/sk.md" }     // rejected
{ "type": "skill", "name": "plugin:release-notes", "text": "" }           // rejected
```

Names must be non-blank and are trimmed; duplicate selections are passed
through for the runtime's identity and invocation policy to judge. Exact
catalog identity and invocation policy remain consumption-time checks.

Selections are consumed at the input's actual claim, not at submission: the
queued or started input keeps its selection across the durable queue, journal
payload, queue returns, drains, and restarts, and the claimed input prepares
all of its selections as one atomic group tied to the input's durable identity
(its queue entry, or the stable turn a start or steering mutation reserved).
Preparation resolves each name against the exact catalog identity — a bare
suffix or unknown name fails rather than retargeting — loads the complete
current bytes from the recorded `SKILL.md`, and checks the current invocation
flags. Bytes changed on disk after queueing are what the consumption delivers.

Consumption is all-or-nothing. A group whose second selection cannot be
prepared activates nothing and dispatches no dependent model work: the input
is recorded as a visible failed input that keeps both the names and the
original prose for correction and an explicit retry. A steering input whose
selection fails is never downgraded to text: the unrelated in-flight turn
keeps running with none of that steering input — not the bodies, not the
prose — while a prominent failed-steering record persists before the pending
execution clears, again keeping the names and prose for retry.

Submission is capability-gated. `ThreadCapabilities.skillInput` advertises
whether a thread accepts skill items; a daemon-sourced thread advertises it
exactly when its input-bearing turn mutations (turn/start, turn/steer,
turn/queue, turn/drainAsSteer) are wired to consume selections, and anywhere
it is false a skill item is an unsupported-input error rather than a silently
degraded message. Diagnostics carry the catalog context clients need to build
a selection UI:
`EvenerSkillInfo` reports `disableModelInvocation`, `userInvocable`,
`available`, and preserved `allowedTools` metadata, while
`EvenerDiagnostics.skillDiagnostics` reports discovery problems (collisions,
unreadable sources, invalid metadata) with their explicit sources. Completion
offers only skills that are both `available` and `userInvocable`, and the
composer still requires the target's `skillInput` capability before
submitting a selection.

#### Mixed-version fail-closed and recovery

The Hub is a separate process from the sessions it forwards to, so it never
assumes a daemon supports skill input: before any input-bearing mutation
carrying a selection is forwarded, the Hub reads the target's advertised
capability — on the same connection as the mutation for daemon-sourced
threads, and through the relay's own read for `turn/start` — and rejects the
request when the capability is false. This is what keeps a mixed-version
deployment honest:

- **Hub newer than daemon.** An older daemon never sends `skillInput`, so an
  absent capability reads exactly like a false one and every selection is
  rejected with an `unsupported on this target` invalid-params error. The
  daemon never sees the request, so no queue entry, journal payload, or turn
  is created for it.
- **Unreadable or unknown target.** A capability read that fails — an
  unreachable endpoint, a dying daemon, a read error — is a rejection, never
  permission. `thread/start`'s initial input is gated on the spawned session's
  own read: when that read fails, the synthesized thread carries no
  capabilities and the selection is refused. The hub's synthesized capability
  sets (ended threads, roster projections) deliberately leave `skillInput`
  false; a capability is only ever learned from a target that actually
  reported it.
- **Rejection shape.** A rejection preserves what the caller composed: the
  selection is not degraded into `/skill` slash prose, stripped to its text
  item, or partially delivered. The client keeps its input for an explicit
  retry.
- **Recovery.** A rejection means the running target does not consume
  selections, so the fix is on the target side: upgrade or restart the
  session's daemon (for a stopped session, resume spawns a current daemon) and
  send the input again. The recheck runs on every attempt — including the
  automatic resume inside `turn/start`, which re-reads the replacement daemon
  before forwarding — so the same request succeeds once the target that
  answers advertises `skillInput`.

### Reload selection at compaction

Compaction can drop a loaded skill's instruction body, so both compaction
paths accept a selection of skills to reload from disk afterward. The
selection names exact canonical names from this session's successful-inventory
records — never tool-call names or history text — and the runtime presents
that inventory (names and descriptions) when asking.

- **Explicit:** `compact_context` accepts an optional `reload_skills` array of
  strings. `note_to_self` remains required, and array elements validate as
  strings; a schema-invalid call is rejected by normal argument validation and
  schedules nothing.
- **Automatic:** note elicitation asks for the same selection in exactly one
  `<skill-reload-selection>` block containing a JSON object with a
  `reload_skills` field, alongside its free-text note. Only a valid block is
  removed from the handed-forward note; a missing, multiple, or malformed
  block authorizes no reload and the note is preserved verbatim. Incidental
  skill mentions in the note or summary never count as a selection.

Presence distinguishes three outcomes:

| Selection | Meaning |
|---|---|
| Valid list | Reload those skills in listed order; duplicates collapse to first occurrence. An explicit empty array reloads none. |
| Absent (omitted or `null`) | No selection was made; the all-loaded-skills reminder applies. |
| Invalid (malformed, or naming a skill outside the inventory) | Reported with `invalid_selection`/`unknown_skill`; no reload is authorized and the reminder applies. |

A present selection — including an explicit empty array — requests the
compaction even with an empty `note_to_self` (which still clears any previous
pinned note). Empty note, no instructions, and an absent/null selection keeps
the plain clear-note behavior. An invalid selection in an accepted request
never discards that request's valid note. A selection belongs to one
compaction cycle and is consumed only by a compaction that actually publishes.

Accepted requests are generation-owned, persisted compaction operations, not
per-round flags. Each operation mints a generation from the session's
lifecycle counter, records its origin (`forced` from `compact_context`,
`automatic` from note elicitation), the note generation it owns, its steering
instructions, and its selection; it stays pending until the fold publication
claims it (moving it to the published phase for delivery) or a terminal
cancellation retires it. The metadata save happens before the tool reports
success, so an intent is never durable-by-assertion: a failed save returns a
typed, retryable failure and retires the unsaved operation. Restart is not
cancellation: a resumed session re-arms an unpublished forced operation's
round-tail trigger from the persisted record, an automatic operation restores
its elicitation latch, and a published operation resumes for delivery only.
Clearing or replacing a note cancels only the automatic operation associated
with that note; a forced operation survives a note clear, and a second forced
request while one is pending is rejected without touching the first. While
any pending or published-but-undelivered operation owns the cycle, note
elicitation is latched — which is also the first-wins rule: a later
elicitation can never overwrite the concrete selection the cycle already
recorded, not even with an invalid one. Sessions persisted before this
lifecycle metadata existed resume with fresh compaction state; nothing is
reconstructed from old tool calls or transcript prose.

A fold publication claims its operation only when the fold actually
compacted something and the operation is the one the fold's own requesting
dispatch captured. The claim is generation-matched and runs inside the
winning publish's critical section, so:

- **No-op publications claim nothing.** A publication that staged no actual
  compaction (an unchanged publication) leaves the operation pending for the
  next real fold — a claimed cycle always corresponds to content that truly
  compacted.
- **Unrelated folds claim nothing.** A fold whose requesting caller captured
  no operation — for example a manual compact racing a pending one —
  publishes its own compaction without adopting the pending operation or
  touching its selection; the pending operation stays armed for its own
  dispatch.
- **Losing attempts record nothing.** A fold that loses the publication race
  attaches no receipts and cancels nothing; only the winner claims. A forced
  dispatch that exhausts its retries without ever publishing cancels its own
  operation with a `forced_not_published` notice — a forced intent never
  lingers past the dispatch that owned it.

The winning claim moves the operation to its published phase under the
publication's identity and stamps a typed receipt on every compaction turn
the publication wrote — the checkpoint and the summary turn of one
publication carry the same receipt, and the handoff list coalesces them into
one final handoff per publication. Delivery completes only after the
publication's transcript flush and metadata save both land: then the
operation slot and its selection are consumed, the cycle reopens for a new
request, and the receipt flips to delivered. A crash between the winning
publication and its metadata save is repaired on restart: transcript
receipts are reconciled against the persisted snapshot before history is
resumed, and an interrupted delivery completes exactly as the live
transaction would — the slot clears, the selection is consumed, and the
publication's handoff advances to delivered, never re-armed — while a
delivered operation stays consumed and a cancelled one stays retired. A
receipt only ever advances its own generation, and a stale snapshot can
never resurrect or repeat a claimed cycle. A persisted slot still in the
published phase is completed the same way at restore: it can only be a
concurrent save's snapshot of the delivery window (the live transaction
clears the slot before its own save), so restore finishes the delivery
rather than resuming it. Publication identities are minted
from the persisted lifecycle revision stamped at the claim, so a restored
session's later publications never collide with a restored handoff.

The context-pressure nudge lists the loaded skills either way: with
`compact_context` available it asks for the selection through `reload_skills`;
without the tool it lists the skills for awareness and keeps its existing
note advice, never requesting a structured response the model cannot submit.

### Reload delivery after compaction

The next model request after a publication consumes its handoff receipt —
and every other recorded, unconsumed handoff, whichever phase the
publication or restart reconciliation left it in. A valid selection reloads
exactly its named skills: each reload reopens the ordinary record's own
recorded source — never a catalog winner, so a missing or identity-replaced
source fails explicitly (`source_missing` / `source_changed`) rather than
retargeting — and delivers the complete current bytes from disk through the
shared loader and renderer. A changed source is reported with both the old
and new digests and both flag generations in the typed outcome. A name held
only by a preload needs no disk load: selecting it is a no-op.

An absent or invalid selection authorizes no body. Instead the session
delivers one complete typed inventory notification listing every loaded
skill — both provenances when a name holds a frozen preload and an ordinary
activation — each classified from current source metadata and actual
retained content:

| Availability | Meaning |
|---|---|
| `already_present` | Complete current content is already in the retained tail or was restored by another activation; reusing it needs nothing. |
| `reloadable` | The session can reload it: an authorized same-source continuation regardless of current flags, or current controls still admit the reload route. |
| `requires_user_activation` | Hidden but user-invocable: current controls block the model's routes; only a genuine user invocation restores it. |
| `unavailable` | Both flags block every ordinary route, or the recorded source can no longer be read (each unreadable source is diagnosed). |
| `permanent` | A frozen role preload, which keeps its permanent-prompt lifetime. |

The full list is always delivered — never trimmed to fit. In sessions
without the `use_skill` tool the reminder explains the untracked fallback:
reading the skill's `SKILL.md` directly restores the text to the conversation
but is not a tracked activation.

Reload bodies are admitted in selection order against the request's real
remaining budget: a new explicit activation already in the conversation
outranks them, each admitted reload consumes from the running total, and one
that does not fit fails individually with a typed `context_budget` outcome —
never a truncated body, never a second compaction or model-repair round.
Complete content already present in the retained tail or restored by another
activation is reused rather than duplicated, keeping a pending delivery
obligation until final admission. A failed reload stays visible as its own
typed outcome and never erases the earlier successful record.

After the reload carriers and obligations (or the reminder notification) are
durably recorded, the consumed receipts are removed by publication identity —
touching no other pending operation — and the snapshot save plus the
transcript receipt reconciliation keep a restart from repeating delivery.

## Evener-wide slash commands

A evener-wide slash command is a markdown file — frontmatter optional — in one
of two places:

- `<any dir from git root to cwd>/.evener/commands/name.md` (project commands)
- `$XDG_CONFIG_HOME/evener/commands/name.md` or `~/.config/evener/commands/name.md`
  (user-global commands)

The filename is the command name. Names cannot contain whitespace
(invocation parses the name up to the first space, so a spaced name can
never run) or colons (`:` is the plugin-namespace separator; evener skips
such files with a warning). Invoke it by typing `/name args` in a session. Optional
frontmatter: `description`, `argument-hint`, `model`, `allowed-tools` (the
last two are parsed but not enforced; evener warns when they appear).

Expansion substitutes `$ARGUMENTS` and `$1..$9` as inert text. `!`cmd``
spans and `@file` references in a evener-wide command body never execute or
read anything — they remain in the expanded text verbatim except that
argument substitution still applies inside them as inert text — and evener
warns at load time if a evener-wide command contains `!`` spans. If you want
an executable template, package it as a plugin command instead.

**Precedence:** project > user-global > plugin. A evener-wide command shadows
a plugin command of the same bare name; the plugin command stays reachable
as `/plugin:name`. Within project commands, the directory closest to your
cwd wins.

### Client caveats

- Both desktop surfaces intercept their own built-in slash commands before
  your input reaches the session: the TUI has its registry (`/status`,
  `/model`, `/help`, ...) and the web palette has its own, partly different
  set (`/status`, `/model`, `/help`, `/steer`, `/queue`, ...). A command
  whose name collides with a client's built-ins is unreachable in that
  client's typed input — pick another name. Headless input always works.
- The web UI opens its command palette when you type `/` into an empty
  composer. The palette lists plugin and user-global commands (badged by
  source) alongside the built-ins. If the name you typed isn't exactly one
  of the palette's commands, Enter sends it to the session as-is — a fuzzy
  near-miss (say `/stat` for `status`) still reaches your command. Project
  commands invoke through that fallthrough.
- Standalone skills are not command-file entries in the command catalog, but an
  exact `/skill-name` token is recognized by the session when that skill is
  loaded and activates the skill body. Skill names and descriptions are shown
  in the model's skill catalog.

## Plugin commands

Plugins (installed via the marketplace or configured via `plugin_dirs`) may
ship `commands/*.md` files. These are Claude Code-compatible: in addition
to `$ARGUMENTS` substitution, ``!`cmd` `` spans execute in the session
environment and `@file` inlines files at working-directory-relative paths
(the constraint is lexical; symlinks are followed). Only install
plugins you trust — a plugin command's body runs shell commands with the
same permissions as the session.

Plugin commands are namespaced: `/plugin:name`. The bare `/name` form
resolves to the plugin command when no evener-wide command shadows it and no
client built-in intercepts it (the client caveats above apply to plugin
commands too). If two plugins define the same command name, the bare form
resolves to one of them — which one is not guaranteed; use the qualified
form to be sure.

## Security checklist for command authors

- Treat every `.evener/commands/` file in a repo you did not write as
  untrusted text. Evener guarantees it cannot execute, but its contents still
  become prompt text for the model — read it before invoking it.
- Never put secrets in command bodies; they are sent to the model verbatim.
- If you need shell output in a prompt, prefer a plugin command (explicit
  trust) over asking users to paste output manually.
