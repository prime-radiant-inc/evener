# Subagent runtime contracts

Status: Evergreen reference for the shipped subagent runtime. This documents what
serf **does today** across the cross-cutting runtime concerns that subagents,
plugins, hooks, helpers, and lineage share. It is the current-reality counterpart to
the point-in-time design specs in [`subagent-management/`](subagent-management/)
(`06`–`10`), which propose contracts and target states; where this doc and a spec
disagree, **this doc describes what ships** and the spec is the design record.

Scope boundaries with the other evergreen docs:

- The **job-control tools** (`shell`, `delegate`, `job_watch`, `job_list`, `job_stop`,
  `job_send_message`) — their parameters, return shapes, delegation allowance, and the
  per-context tool-availability matrix — are owned by [`job-control.md`](job-control.md).
  This doc covers the runtime *policy* those tools sit inside and does not restate them.
- The job-control runtime **mechanics** (the watch outbox, drive-down, single-hop
  forwarding, the deadlock-avoiding lock discipline) are owned by
  [`architecture.md`](architecture.md).

Citations are `path` + symbol rather than line numbers, so they survive edits — grep
the symbol in the cited file.

## Effective capability policy

A child agent can **narrow** but never **expand** the capability set available to its
parent/session. This is enforced structurally by a single per-session tool registry,
not by parallel policy checks:

- **One registry, narrowed at child init.** `initSessionState` (`agent/session_init.go`)
  builds the full registry, then applies the spawn restrictions: `RestrictKeepingResultTool`
  (`agent/internal/tool/registry.go`) drops every tool not in the child's allowed set
  except the result tool, and `Remove` drops each denied tool. A `tools: all` agent gets
  exactly the tools effective in *this* child session — not every tool registered in the
  process.
- **Visibility == execution.** The advertised tool set (`rebuildToolDefsCache`,
  `agent/session_tools.go`) and the execution gate (`Registry.ExecuteCall`,
  `agent/internal/tool/registry.go`) read the **same** `s.reg`. A tool removed from the
  registry is therefore both invisible to the model and unexecutable — a forged or stale
  model-returned call to a filtered tool hits the unknown-tool path and is denied before
  execution. The two surfaces cannot disagree because there is only one source.
- **`grant_tools` intersects, never expands.** A parent can only `grant_tools` a tool the
  session can already call; the spawn path rejects a grant of a root-only or
  not-currently-callable tool (`agent/subagents.go`, e.g. `"cannot grant tool %q via
  grant_tools…"`, `"cannot grant tool %q: it is not currently callable…"`).
- **Root-only delegation tools are allowance-gated.** `delegate` and `job_watch` are
  stripped from a child whose `delegation_allowance` is 0 and granted to one with a
  positive allowance — gated by allowance, **not** by a fixed depth. The authoritative
  contract is in [`job-control.md`](job-control.md) ("Delegation allowance",
  "V1 tool availability"); the strip happens in `agent/session_init.go`
  (`rootOnlySubagentTools`).
- **Close/cancel denies new child work.** Once a session/job-manager is closing,
  starting or resuming child work is refused (`trackAndLaunchPreparedSubagent`,
  `startOrSteerSubagentRun`, `agent/subagents.go`; `resumeOrFindRunningDelegate`,
  `agent/job_delegate.go`; `attachDelegateJobWithRestore`'s `errJobManagerClosing`).

Not yet centralized (these hold today via code ordering / per-feature resolution, not a
single enforced gate — see `subagent-management/10-runtime-contracts.md` Contract 1 for
the target): policy immutability after a tool execution starts is a property of the
sequential `PreToolUse → execute → PostToolUse` path, not a frozen-policy token; an
agent definition naming an **unknown MCP tool** yields a silently-absent tool (a forged
call later gets unknown-tool) rather than a deterministic spawn-time error; and an agent
definition naming an **unsupported provider feature** is resolved/overridden rather than
rejected with a policy error. MCP *server* unavailability is now **non-fatal and
parallel** (previously fail-fast): each server's transport build, connect, and
tools/list run concurrently in its own goroutine; a failure there, or later during tool
registration, is isolated to that server rather than aborting the batch, reported as a
`ServerOutcome` (`agent/internal/mcp` `NewManager`/`RegisterTools`). Each outcome
becomes a `pendingMCPWarnings` entry classified `SourceMCP`
(`agent/internal/diagnostic`), flushed after `SESSION_START` (`agent/session_init.go`
`initMCP` → `emitSessionStartEnvelope`) — the session constructs with whichever servers
came up healthy, and zero healthy servers is still a healthy session. A connection
dropped mid-session gets exactly one call-driven lazy reconnect attempt, gated on
`errors.Is(err, mcpsdk.ErrConnectionClosed)` (`conn.reconnect`). CLI
`--mcp-config`/`--mcp` config-parse errors (`agent/mcpconfig` `Discover`) are the one
thing that still stays fatal, since they're explicit user input for this invocation.

## Plugin and agent loading

What validation actually runs when serf loads a plugin (`agent/plugin/plugin.go`,
`agent/plugin/agents.go`):

- **Plugin name** is required and must be kebab-case (`validatePluginName` / `kebabCaseRe`).
- **`.codex-plugin` wins over `.claude-plugin`.** When both `<dir>/.codex-plugin/plugin.json`
  and `<dir>/.claude-plugin/plugin.json` exist, `Load` uses `.codex-plugin` and ignores
  `.claude-plugin` for that root.
- **Duplicate plugin names are rejected** across loaded plugins (`LoadAll`,
  `"duplicate plugin name %q…"`).
- **Agent `name`/`description`** are required non-empty strings (`getString`). **`tools`**
  accepts the scalar `all` or a list of strings; it rejects scalar `*`, list `all`, list
  `*`, and non-string entries. Claude tool names are mapped to serf canonical names at
  load (`toolname.ClaudeToSerf`). `model`/`color` default to `inherit`/`blue`.
- **Namespacing.** Plugin agents are exposed as `plugin:agent`, except a
  `coordinator-workflow` plugin exposes its bundled agents by bare name
  (`exposedAgentCatalogKey`, `agent/coordinator_workflow_plugin.go`). Plugin MCP servers
  are namespaced `plugin_<plugin>_<server>` (`discoverPluginMCPConfigs`); `.mcp.json` is
  the file source, merged with inline manifest `mcpServers`.
- Missing component directories are tolerated — a plugin with no agents/hooks/MCP is valid.

Not yet (these are proposed in `subagent-management/06-plugin-agent-validation.md`, not
built): agent-name kebab validation, duplicate **agent**-name rejection within a plugin
(a later agent silently overwrites an earlier same-name one), manifest field-type
validation, `agents`-override path-traversal/absolute-path rejection, `model`-value
validation, empty-body warnings, unsupported-Claude-field rejection, and any
`Validate*` Go API / `serf plugin validate` CLI. An inline `mcpServers` parse failure is
no longer a silent gap: it now degrades to a **plugin-level MCP warning**
(`Instance.MCPConfigWarnings`, `agent/plugin/plugin.go` `discoverPluginMCPConfigs`) — the
plugin's MCP layer is skipped but its skills/agents/hooks still load; a malformed
`.mcp.json` file and malformed inline JSON are warned the same way. One silent gap
remains: **`skills`/`tasks` entries are not value-validated** (empty strings/fields pass).

## Lifecycle hooks (Claude-compatible subset)

Serf fires a tested subset of the Claude hook contract; the rest is recognized but
reserved. (`agent/plugin/hooks.go`, `agent/internal/hooks/`.)

- **Nine events fire:** `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
  `Notification`, `SubagentStop`, `Stop`, `PreCompact`, `SessionEnd` (`validHookEvents`).
  The full ~30-name Claude vocabulary is recognized as `reserved-placeholder` — parsed,
  diagnosed, never fired (`recognizedClaudeEvents`); unknown names are bucketed
  separately and warned loudly at load.
- **Discovery:** inline manifest object, manifest string path, else default
  `<dir>/hooks/hooks.json`; both wrapper (`{description?, hooks:{}}`) and direct
  (events-at-top) JSON shapes parse.
- **Matchers** (`agent/internal/hooks/matcher.go`): empty/`*` = all; `[A-Za-z0-9_|]` =
  exact or pipe-list; otherwise Go RE2. `Bash` does not match `BashOutput`. Matchers run
  against the **Claude** tool name (`shell` → `Bash`). An invalid regex is skipped and
  diagnosed once at load, not per dispatch.
- **Handlers:** only `command` and `prompt` execute; `http`/`mcp_tool`/`agent` are
  reserved and skipped-with-diagnostic. `command` supports shell-form (`bash -c`) and
  exec-form (`args`); `${CLAUDE_PLUGIN_ROOT}`/`${PLUGIN_ROOT}` expand in command/prompt/
  args; the runner sets `CLAUDE_PLUGIN_ROOT`, `PLUGIN_ROOT`, `CLAUDE_PROJECT_DIR`, and
  `CLAUDE_EFFORT` (only when configured). It does not set `CLAUDE_CODE_REMOTE`
  (intentionally — serf has no remote/serve mode) or `CLAUDE_PLUGIN_DATA`. `prompt`
  is serf-native sugar that runs an LLM call with
  `$TOOL_INPUT/$TOOL_RESULT/$USER_PROMPT/$MESSAGE/$TOOL_NAME` substitution.
- **`PreToolUse` runs before registry schema validation**, can rewrite the tool input
  (`applyUpdatedToolInput`, applied before execution), and can deny (short-circuits the
  call). `allow`/`deny` with `permissionDecisionReason` are honored, including the
  deprecated `approve→allow`/`block→deny` mapping (preferred keys win); `ask`/`defer` are
  recognized but **not** honored (the tool proceeds with a diagnostic).
- **Exit-code 2 blocks only for `PreToolUse`, `Stop`, `SubagentStop`**
  (`agent/internal/hooks/exitcode.go` `exitBehavior`); every other event is non-blocking.
  JSON output is parsed only on exit 0; on exit 2 stdout is treated as stderr and JSON is
  ignored.
- **Output routing:** `additionalContext` goes to the model (wrapped as a system
  reminder); `systemMessage` goes to the user only — distinct channels (`routeOutput`).
- `/status` labels each hook event by tier (`claude-compatible-subset` vs
  `reserved-placeholder`, `plugin.EventTier`).

Known gaps (stated so you don't over-trust the compat surface): **`UserPromptSubmit` and
`PreCompact` do not enforce the exit-2 block** even though Claude's contract blocks; and
`continue`/`suppressOutput`/`terminalSequence` are **parsed but have no consumer** (a
hook returning `continue:false` still runs to completion). `if`/`async`/`statusMessage`
and the `http`/`mcp_tool`/`agent` handlers are parsed-but-inert. See
`subagent-management/07-lifecycle-hooks-claude-compat.md` for the full reserved set.

## Events and diagnostics

- **Job event payloads** (`agent/events/payloads.go`): `JOB_STARTED` carries `job_id`,
  `job_type`, `status`, `from_watch`; `JOB_FINISHED` carries `job_id`, `job_type`,
  `status`, `reason`, `exit_code`, `output_bytes`, `transcript_ref`, `from_watch`. The
  emitting session's ID — the job's owner — rides on the envelope
  (`SessionEvent.SessionID`). (Model-facing job *notifications* — what an
  agent actually sees — are documented in [`job-control.md`](job-control.md); these are
  the internal event-bus payloads.)
- **Warnings are first-class and surfaced, never silent.** `WarningData`
  (`agent/events/payloads.go`), carried on the `EventWarning` event, flows on the session
  event stream that TUI/Hub/SDK consume; emitting paths include plugin hook-config
  warnings (built in `agent/session_init.go`, emitted via `emitDiagnosticWarning`) and
  subagent task-seed failures (`agent/subagents.go`). A warning with no display surface
  is treated as a bug.
- **Policy denials name the action and the boundary**, not just "not allowed" — e.g. the
  `grant_tools` rejections above, `"agent_type %q is top-level only: it requires root-only
  tools"`, and the delegation-allowance / ownership errors in `agent/job_delegate.go`.
- **Compatibility gaps are reported, not half-accepted.** Recognized-but-unsupported and
  unknown hook events are recorded (`Instance.UnsupportedHooks`/`UnknownHooks`) and turned
  into load-time warnings; reserved handler fields are captured for diagnostics rather
  than treated as working.

Secret handling: the agent runtime has **no dedicated redaction layer** for diagnostics
or event payloads. Secrets are kept out **by construction** — warning and hook-lifecycle
events copy only names, IDs, and counts, never hook commands, env, argv, stdout/stderr,
or MCP env maps. (The only redaction in the codebase is CLI-level: env-secret and
launch-check scrubbing in `cmd/`.) There
is **no shared redaction helper** and validation is fail-on-first (no grouped
`[]ValidationIssue`); both are proposed in
`subagent-management/10-runtime-contracts.md` Contract 3. The residual risk to know: a
`command`/`prompt` hook's own stdout is delivered as context unscrubbed — that is
hook-author behavior, not a serf leak, but nothing redacts it.

## Lightweight helper isolation

Several operations make a one-shot LLM call that is **not** a subagent. These helper
calls route through `llm.Client.Complete`/`llm.GenerateObject` directly and never
register a subagent, create a child transcript, mutate the task store, run tools, or
appear in `job_list`. Verified call sites:

- `web_fetch` cheap-model summarization (`agent/tool_web_fetch.go` `webFetch`),
- session auto-naming (`agent/session_namer.go` `nameSession`, advisory — failure does
  not fail the session),
- image description (`describeImage`, `agent/session_tools.go` — side-channel call, no
  `Tools` set),
- prompt hooks (`agent/internal/hooks/hooks.go` `clientAdapter`/`executePromptHook`),
- context-manager strategies (fork-summarize, recursive distill, checkpoint prediction,
  memory crystals — `agent/internal/contextmgr/*`).

The isolation is structural: a session enters `job_list` only via `s.subagents.track`,
called only on the spawn/delegate paths (`agent/subagents.go`, `agent/job_delegate.go`),
which none of the helper sites reach. Because they all route through `llm.Client.Complete`
(`llm/client.go`) — directly, or via `llm.GenerateObject`/`Generate` for the schema'd
ones like session naming — they still inherit request validation, provider resolution,
middleware/logging, and error stamping. The proposed shared helper API
(`GenerateHelperText`, `LoadLLMClient`, …) in
`subagent-management/08-standalone-llm-calls.md` is **not built** — these sites achieve
tool-freeness simply by never populating `Tools`.

## History and lineage

Fork and subagent are **distinct relations** sharing lineage fields, kept explicit:

- **Fork copies history; a subagent starts fresh.** `ForkSession` (`agent/fork.go`)
  replays the parent's first `divergenceTurn-1` entries into the child transcript; a
  spawned subagent gets a new empty session with only a lineage pointer.
- **Lineage lives in metadata and headers, discoverable without reading transcript
  bodies.** `SessionMeta` (`agent/schema/snapshot.go`) carries `ParentSessionID`,
  `DivergenceTurn`, `ForkLabel`, and `IsSubagent`; the transcript `Header`
  (`agent/transcript/transcript.go`) carries `ParentSessionID` (and `ParentToolCallID`).
  `ForkSession` (`agent/fork.go`) writes the child header and meta inline from the same
  values; subagent spawn sets `ParentSessionID` at init (`agent/session_init.go`).
  `IsSubagent` is true when spawned with a parent and false for a fork.
- **Parent lineage is immutable and preserved** across meta rewrite and resume
  (`ParentSessionID`/`DivergenceTurn` are write-once; round-tripped by the meta save
  path; covered by `agent/fork_test.go`
  `TestForkSession_ChildLineagePreservedAcrossMetaRewrite` /
  `TestForkSession_ParentForkLabelPreservedAcrossMetaRewrite`).
- **The `ForkLabel` lives on the parent**, not the child (the child's is empty), so the
  label marks which branch diverged.
- **`children_of` is a metadata-derived view, not a stored index.**
  `find_session_transcripts(children_of=…)` (`agent/session_tools_find.go`) scans
  `*.meta.json` at query time, returns both forks and subagents, and tags each with a
  `kind` discriminator; results are scoped to the parent's project and gated on a
  readable transcript. A missing parent reference is a diagnostic, not a crash.
- **Parents reference children, not inline their bodies** — delegate results carry a
  `transcript_ref` (`encodeRef`), and child transcripts are read explicitly and bounded
  via the transcript tools.

Not built (proposed/assessed, not committed): the session-**tree** API (`ListSessionTree`
etc.) is deferred in `subagent-management/09-session-tree-history-assessment.md` in favor
of the metadata + `children_of` baseline above. Two known characterization gaps: the
transcript header alone cannot distinguish fork from subagent parentage (use the
metadata `kind`), and a single parent forked more than once has only one `ForkLabel`
slot (later forks overwrite it).

## Where the design specs live

The proposals, target contracts, acceptance criteria, and test matrices behind these
shipped behaviors are in [`subagent-management/`](subagent-management/):
`06-plugin-agent-validation`, `07-lifecycle-hooks-claude-compat`,
`08-standalone-llm-calls`, `09-session-tree-history-assessment`, and
`10-runtime-contracts`. Those are point-in-time design records; this doc is the
current-reality reference. When a behavior described here changes, update this doc.
