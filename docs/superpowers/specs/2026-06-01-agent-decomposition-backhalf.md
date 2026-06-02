# Agent decomposition — back-half plan & decisions (overnight; for morning approval)

## Done + verified (committed on local `main`, NOT pushed)

| Commit | Carve |
|---|---|
| `5478140d` | `agent/execenv` |
| `56534533` | `agent/schema` (Turn + TurnKind) |
| `cdb4ae48` | gate execenv+schema in docscheck/internalcheck |
| `6784635a` | `agent/skill` |
| `ae513bfe` | `agent/task` |

HEAD = `ae513bfe`. Full `-race` + golangci(0×4) + docscheck/internalcheck/namingcheck
green at each step; **1278 tests preserved throughout**; every commit independently
re-verified by me (gate, `go doc` move, no-test-dropped, behavior-preserving diff).

## The finding that stopped further free-running

Those four were the genuinely-clean leaves. **Every remaining carve has bidirectional
coupling and multiple boundary crossings**, each requiring a decision. I free-ran the
clean ones and stop-on-boundary caught the rest before any broken commit; I'm queuing
them with precise resolutions rather than committing surgical agent-core changes while
you sleep. Four recurring boundary classes (all hit tonight):

1. **A moved type drags a companion** → move it too (`TaskTemplate`→task; `ToolOutputLimit`→schema).
2. **Engine calls an unexported helper that moved** → promote it exported (`scanSkillsDir`→`skill.ScanSkillsDir`).
3. **A moved file mixes public contract with engine-internal code** → split the file (transcript readers stay).
4. **White-box tests reach unexported internals across the new boundary** → relocate the tests or add a seam.

## Queued carves — scope, boundaries, decisions

### transcript → `agent/transcript` (PUBLIC) — fully mapped; ONE open question
The writer + format contract (`TranscriptHeader`, `TranscriptEntry`,
`TranscriptAPICall`, `TranscriptWriter`, `NewTranscriptWriter`,
`OpenTranscriptWriter`, `Append`/`AppendAPICall`/`Close`) → `agent/transcript`. A
trial carve was **mechanically green for all non-test code across 4 modules.** Three
boundaries, resolved:
- **Readers stay** (forced): `readTranscript`, `readTranscriptFull`, `transcriptData`,
  `ResumeHistory` (calls unexported `repairOrphanedToolResults`, Session/atif-wired) →
  new file `agent/transcript_read.go`, referencing `transcript.X`.
- **Const** `transcriptJSONLMaxLineBytes` (`128<<20`): keep a trivial per-package copy
  (matches existing convention — `cmd/serf-hub` already keeps its own).
- **DECISION:** 5 of 6 writer white-box tests relocate cleanly to `package transcript`.
  The 1 Session-coupled test (`TestSession_TranscriptWriteFailureEmitsWarning`) forces a
  write failure by poking the writer's unexported `file`, inaccessible once it moves.
  **Rec:** rework it to induce the failure via an exported path if one reproduces it,
  else add a minimal exported test seam. (Not droppable.) This is the only open question.

### tool → `agent/internal/tool` (INTERNAL) — the key unblocker for mcp/context
Move the registry + Session-free builtin tools (`tool_registry.go`,
`tool_definitions.go`, `apply_patch.go`); promote `toolRegistry`→`tool.Registry` and
`registeredTool`→`tool.RegisteredTool` (the session glue in `session_tool_registry.go`
constructs them, so they must export). The web tools (`tool_web_*.go`, `*Session`) and
the glue (`session_tool_registry.go`, `session_tools.go`) STAY in agent.
- **Prereq (forced):** move `ToolOutputLimit` + `TruncationStrategy` → `schema` (shared
  by `registeredTool.Limit` AND persistence's `ConfigSnapshot`; schema is the lowest
  shared layer). Clean value types.
- Expect class-1/2 boundary surprises (handled at execution with stop-on-boundary).
- **DECISION:** confirm `agent/internal/tool` (internal — it's engine machinery, not
  consumed outside the module) vs a public `agent/tool`. Rec: internal.

### mcp → `agent/internal/mcp` (after tool)
`mcpManager` → internal; `MCPServerConfig`/`MCPServerInfo` stay public (app-consumed).
`mcpManager.RegisterTools(reg tool.Registry)` is the `mcp→tool` edge — needs tool first.

### plugin → `agent/internal/plugin` (after mcp)
`hookRunner` + loader internals → internal; the public contract (`LoadedPlugin`,
`PluginAgent`, `HookEvent`, `RegisteredHook`, `LoadPlugin(s)`) stays public (cmd/serf-hub
consumes cross-module — review C2). Depends on skill✓ + mcpconfig.

### provider → `agent/provider` + collapse `ProviderProfile` to a struct (THE big decision)
`ProviderProfile` is an **18-method interface with ONE real impl** (`baseProfile`;
`anthropicProfile` overrides only `WithModel`) + 5 `WithX` decorators that type-switch
over the two concrete types and `default: return p` (silently no-op on anything else).
No external implementers. It's a concrete type in an interface costume.
- **Rec:** collapse to a concrete `provider.Profile` **struct**; the 5 decorators become
  methods (no leak); the anthropic `WithModel` variance becomes a struct field/func
  (I'll study exactly what differs at execution). Kills the fat interface AND the leaky
  decorators — the panel's single biggest item. **Breaking** (interface→struct; agent's
  profile param type changes); you've pre-approved reshaping when panel-agreed.
- **Smaller alternative:** keep the interface, make the 5 `WithX` interface methods (kills
  the no-op leak, keeps the fat interface). My rec is the struct.

### persistence → `schema` (SessionMeta/Snapshot/ConfigSnapshot)
- Move `ToolOutputLimit`+`TruncationStrategy` → schema (also tool's prereq above).
- New `schema.ConfigSnapshot` = SessionConfig's json-serializable wire subset, **identical
  json tags** → `meta.json` round-trips unchanged (on-disk format unchanged; Go type of
  `SessionMeta.Config` changes). Live engine `SessionConfig` (json:"-" fields) stays in agent.
- Move `SessionMeta`/`SessionSnapshot` → schema. **Must precede** the transcript-reader's
  eventual carve and the context carve (both touch `SessionSnapshot`).

### context → attempt after provider+tool+persistence, or accept it stays
Even after narrowing `strategyHost.Snapshot()`, context still hard-depends on **provider**
(`strategyHost.Profile()`, `contextManager.profile`, `ForkSummarize`), **tool**
(`contextStrategy.Tools() []registeredTool`), and **SessionSnapshot** (recall). Carve
LAST; then either it depends only on the lower packages → `agent/internal/context`, or it
**stays in `package agent`** like subagent. **Decision after the prereqs are in.**

### subagent → STAYS in `package agent`
`subagents.go` reaches ~18 parent + ~10 child unexported members + constructs child
`*Session`s; a faithful seam ≈ exposing the engine. Only `subagents.go` constructs a
`*Session`, so staying is cycle-free and free. **Rec: do nothing.**

## Recommended sequence (after approval)

`tool` (+ ToolOutputLimit→schema) → `mcp` → `plugin` → `transcript` → `provider`
(struct) → `persistence` (ConfigSnapshot) → `context` (attempt; may stay) → `subagent`
stays.

## The decisions I actually need from you (everything else is determined)

1. **provider: concrete struct (rec) vs keep-interface-fix-decorators?** ← most impactful.
2. **transcript: the 1 Session-test seam — rework vs minimal exported seam?** (I'll pick
   the cleanest at execution unless you have a preference.)
3. **tool/mcp/plugin/context: internal (`agent/internal/*`) vs public — confirm internal**
   for the machinery (the public data/config types stay public regardless).
4. **Appetite:** want me to execute all of this on approval, or stop after a subset?

Nothing is pushed; everything is separate revertible commits on local `main`.
