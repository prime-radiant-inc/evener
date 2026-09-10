# Skills implementation study

Date: 2026-09-10. Evener revision: `2664cc881d128d8b0c4a0af96683126e70d13cd7`.

## Scope and method

This study compares discovery, model-visible content, explicit invocation, and
context lifetime in Evener, Pi, OpenCode, Codex, and Claude Code. It uses current
official documentation, pinned open-source implementations, and focused Evener
plumbing tests. It does not benchmark model compliance or certify sandbox
security. Claude Code implementation details are limited to public documentation.

“Inlining” has two meanings here: putting every installed skill into the initial
prompt, or inserting one selected skill's instructions into the conversation.
The distinction matters when comparing implementations.

## Answers in brief

**We use the same progressive-disclosure architecture.** Installed skills contribute
metadata initially; selected skills contribute instructions. Exact message roles,
wrappers, argument processing, and retention vary. Inserting a selected body as
user-context text is common and consistent with Agent Skills guidance.

**Our slash feature has a familiar leading-command path and a different inline
path.** Leading `/name` expands in the runtime. An inline `/name` relies on the
model deciding to call `use_skill`. The latter is weaker than a deterministic
explicit-selection contract. Codex provides a useful comparison: it parses
inline `$name` references in code and injects the selected bodies itself.

| Client | Model-selected loading | Explicit user invocation | Explicit body delivery |
|---|---|---|---|
| Evener | `use_skill`, with file-read fallback | Leading `/name`; inline slash relies on model | Stripped body replaces user input; arguments appended |
| Pi | File read, or `bash` fallback | Leading `/skill:name` | User message with skill/source wrapper and base directory |
| OpenCode, traced HTTP path | `skill` tool | Leading `/name` | User command-template text with base directory |
| Codex, current source | Filesystem read or source-specific package read | `$name` anywhere in text; structured selection; `/skills` menu | Separate user-role skill fragment with name/path and file contents |
| Claude Code, documented | `Skill` tool | Leading `/name`, including leading skill stacks | Rendered conversation message; exact API role undocumented |

All rows use a metadata catalog rather than normally loading every body upfront.
Role-declared subagent preloads are a separate case. The upstream sections below
bound implementation/version claims and provide primary citations.

## Agent Skills guidance

The [format specification][as-spec] defines `SKILL.md`, frontmatter, and bundled
resources. It does **not** prescribe a slash-command syntax, tool name, API
message role, or installation directory.

The [client integration guide][as-client] recommends:

- Progressive disclosure: a compact name/description catalog, full instructions
  on activation, and supporting files only when needed.
- Model-selected activation through either a file-read tool or a dedicated skill
  tool. Both are valid. Full-file and frontmatter-stripped delivery are also valid.
- Explicit user activation handled by the harness, so the model receives the
  instructions without having to choose an activation action itself.
- A clear skill identity and base directory in the loaded content; optional,
  bounded resource listings, without eagerly reading those resources.
- Protecting skill instructions during compaction and avoiding duplicate copies
  when the same content remains in context.
- Discovering `.agents/skills/` alongside client-specific directories, explaining
  collisions, surfacing parse diagnostics, and considering project trust checks.

These are integration recommendations, distinct from format requirements. The
same guide deliberately recommends lenient loading for some formally invalid
names. Copying every recommendation without considering the client would also
be a mistake: for example, permanently pinning every skill can exhaust context.

The [authoring guide][as-author] recommends focused skills, descriptions that
explain when to use them, concise instructions, and real-task evaluations that
inspect execution traces. The suggested body size is under 500 lines / 5,000
tokens. Move conditional detail into references and reusable logic into tested
scripts.

## Evener's current implementation

### Discovery and catalog

Discovery layers are bundled skills, the user-global Evener skills directory,
project `skills/` directories from repository root to cwd, configured
`skills_dirs`, and namespaced plugin skills. Later bare-name sources shadow
earlier ones; plugin names remain qualified. The automatic discovery code does
not scan `.agents/skills/` or `.claude/skills/`, although configured paths can
point there. Discovery reads files to parse metadata; it does not put every body
into the model prompt. [E1]

The model receives a `<skill-catalog>` containing canonical names, descriptions,
and directory paths. If `use_skill` is unavailable, the template instead tells
the model to read `SKILL.md` at its file path. This normally appears in the system
prompt; `SystemPromptAsUser` can change the message role. [E2]

### Three body-loading paths

1. **Model-selected `use_skill`.** Looks up an exact catalog key, rereads the
   file, strips frontmatter, and returns a tool result containing a base-directory
   notice followed by the body. It does not substitute arguments, execute shell
   directives, or inline referenced files. [E3]
2. **Leading `/skill-name args`.** The runtime first tries a command of that
   name, then a skill. A skill can resolve by its exact key or an unambiguous
   bare plugin suffix. It replaces the submitted input with the stripped body
   and appends `User context:\n<args>`. This happens before the model turn. This
   path does not add the directory notice or a skill-identity wrapper. [E4]
3. **Role-declared preload.** Skills declared by a delegate's agent role are
   loaded before it starts and supplied through `ActivatedSkillBodies` in its
   prompt configuration. This is distinct from ordinary tool activation. Frozen
   delegate descriptors retain these bodies for restoration. [E5]

### Leading commands and inline mentions differ

`/review this diff` can use runtime expansion. `Please use /review on this diff`
is preserved as user text. The catalog prompt instructs the model to recognize
whitespace-bounded canonical slash tokens in prose and call `use_skill` before
acting, while ignoring code, paths, URLs, literal mentions, and negations. That
inline interpretation is a model behavior, not an enforced parser. [E2, E4, E6]

Consequences:

- Inline activation needs a model tool call; leading runtime expansion does not.
- Inline guidance requires the canonical catalog key. Leading expansion also
  accepts an unambiguous plugin suffix.
- Runtime command precedence and client built-ins can intercept names that also
  identify skills. The existing command palette is not a unified skill picker.
- Skill arguments are appended as context; `$ARGUMENTS` substitution belongs to
  Evener's separate command templates.

The inline test scripts a provider response that already calls `use_skill`. It
proves preserved input and correct tool plumbing. It cannot establish that a
real model will choose the tool or correctly interpret a negated mention. [E6]

### Context lifetime and metadata gaps

The ordinary `use_skill` result has a default **32,000-character, tail-preserving
limit**. An oversized result loses its beginning from the immediate model-visible
output, potentially including its base-directory notice and first instructions.
The limit is configurable. The tool infrastructure retains recoverable full
output when artifact retention succeeds; recovery requires another action. [E7]

Default compaction uses checkpoints/summaries without dedicated restoration of
ordinary activated skill bodies. Checkpoint construction records activated skill
names from tool calls, rather than preserving their bodies. Experimental
observation-masking strategies can additionally replace old successful skill
results with a name and character count; the default path does not run that
masking. Ordinary activation does not populate the role-preload configuration
described above. [E5, E8]

The skill parser reads `name`, `description`, and array-form `allowed-tools`.
The standard's experimental `allowed-tools` form is a space-separated string.
The inspected activation code does not enforce this metadata or interpret
`disable-model-invocation`, `user-invocable`, or `context: fork`. These latter
fields are client extensions, so their absence is a compatibility gap rather
than a violation of the core format. Unsupported invocation and permission
metadata should be visible to users. [E1, E3, as-spec]

Skills remain inert during expansion, including plugin skills. Plugin **commands**
have a separate trusted expansion path that can execute shell and include files.
Inert text can still influence a capable model. Skill discovery/loading has no
activation-specific project trust check; this observation is not a finding that
ordinary execution sandboxing is bypassed. [E1, E3, E4, E9]

## Upstream comparison

### Pi

Revision: `08dc60bc52d89d6823a9738cc90b1916e5e446e5`.

Pi puts a metadata/path catalog in the system prompt. The model normally reads
the selected `SKILL.md` using `read` (or `bash` if read is unavailable), yielding
ordinary file content, including frontmatter, as a tool result. Explicit
**`/skill:name args`** instead rereads the file, strips frontmatter, and creates
a user message containing a `<skill name="…" location="…">` block, a base-directory
notice, the body, and arguments outside the closing tag. The parser is
leading-only; an inline slash mention is ordinary prose. There is no skill-body
length limit in that expansion function. [P1, P2]

Pi's `/name` prompt templates are a separate feature with argument substitution.
They are not bare aliases for `/skill:name`. Pi consumes
`disable-model-invocation`, supports `.agents/skills/`, and gates project skill
discovery on trust. Its ordinary read output has head-preserving limits and
continuation. Compaction can summarize old bodies; it does not provide a general
promise to preserve every loaded skill verbatim. [P3]

### OpenCode

Revision: `b7ca4f91d9222dddb8487e689d3274a732999fd9`.

**Scope:** this comparison traces the `packages/opencode` HTTP command handler
through `SessionPrompt.command`. The same revision also contains a separate
`packages/core` implementation. Its client/deployment reach was not established;
these findings should not be assumed to describe every v2 client. [O1]

In the traced implementation, parsed skill bodies are cached in memory while
only a metadata catalog enters system context. The model can call `skill({name})`,
which checks skill permission and returns a `<skill_content>` tool result with
the body, base directory, and a sampled resource listing. [O2]

Skills also register as **`/name` commands**, below existing commands/MCP prompts
in precedence. The command path injects the cached body plus a base-directory
notice and arguments as user text. It does not call the skill tool. CLI/web slash
parsing is leading-only. Because this uses ordinary command templating, skill
bodies on this route inherit argument, shell, and file-reference expansion. It
also lacks the skill-tool-specific permission request. This is a reason to
review route parity, rather than copy OpenCode wholesale. [O3]

Automatic skill output gets generic head-preserving truncation with a recoverable
full-output artifact. `skill` is exempt from stale-tool-output pruning, but
summary construction still caps tool-output text. That exemption is useful;
it is not a complete verbatim-retention guarantee. Body changes require cache
invalidation rather than a fresh read on every invocation. [O4]

### Codex

Revision: `5c013177d85b72d61dc35e0634511c6d55af5cc5`, upstream HEAD retrieved
2026-09-10, not a claim about every released client.

Codex emits its metadata catalog as developer-role context and selected skill
instructions as a separate user-role fragment. **`$skill-name`** can appear
anywhere in user text. The TUI also supplies structured skill selections with
name/path, and `/skills` is a menu rather than a universal `/skill-name` route.
The runtime resolves explicit mentions, reads the skill, and injects it before
the model request. Its wrapper identifies the name and file path and contains
the complete file text, including frontmatter. Ordinary surrounding user text
remains separate; this path does not perform skill `$ARGUMENTS` substitution.
[X1, X2]

Model-selected loading is a separate path: filesystem skills are opened by the
model, while newer package-backed variants expose `skills.read` with pagination.
Invocation policy uses `agents/openai.yaml` and
`policy.allow_implicit_invocation: false`, rather than Claude's frontmatter flag.
This hides the skill from model discovery while retaining explicit invocation.
Deprecated `/prompts:name` custom prompts were a different templating feature.
[X3, X4]

Local host injection has no body truncation in the inspected loader; agent-plugin
host bodies have a separate prefix-preserving byte cap. Instructions persist in
history, but the inspected local compaction path does not establish Claude-style
per-skill reattachment. Retention across every remote/server configuration was
not established. [X2, X5]

This directly contradicts [our current documentation](../skills.md)'s claim that
Codex skill bodies reach the model only through model-initiated permission-checked
tool calls. That statement should be updated independently of any implementation
changes. [E9, X1, X2]

### Claude Code

Source: [official skills documentation][cc-skills], retrieved 2026-09-10.

Claude Code documents the same metadata-first model. Both **`/name`** and
model-selected **`Skill`** activation load the skill. Skills and custom slash
commands share an invocation surface; skill bodies support argument substitution
and permission-controlled shell preprocessing. Current docs also describe
stacking several leading skills, such as `/write-tests /fix-issue 123`. [C1]

The docs say the rendered content enters the conversation as one message. They
do not specify its API role, so a claim of exact wire-format equivalence would
exceed the evidence. Identical rendered reinvocations get an already-loaded note;
changed arguments or dynamic output can cause a fresh full insertion. After
compaction, Claude reattaches the latest invocation of each skill within documented
budgets: the first 5,000 tokens per skill, up to 25,000 tokens combined, favoring
recent invocations. Older skills can be omitted when the budget fills. [C1]

Claude has explicit invocation controls: `disable-model-invocation: true` prevents
automatic loading, and `user-invocable: false` disables the user slash route.
`context: fork` delegates the skill to a subagent. `allowed-tools` grants
permission without prompting during the invoking turn; it is **not a restriction
on the available tool set**. These extensions are useful precedents, not required
parts of the portable Agent Skills format. [C1]

## Recommendations for Evener

These are proposed priorities, not implemented changes.

1. **Unify selected-skill rendering and activation records.** Use the same
   canonical identity, source path, base-directory guidance, body, and success
   semantics for tool, slash, and role-preload routes. Keep the original user
   request separately identifiable. Our leading slash route currently loses
   loading context supplied by `use_skill`; Pi, Codex, and OpenCode demonstrate
   useful provenance patterns. Shared rendering does not require identical API
   roles. [E3–E5, P2, O2–O3, X1]
2. **Make context lifetime an explicit contract.** Avoid treating a suffix-only
   result as complete skill instructions. Keep an active skill's identity and
   source/version available through compaction, with bounded preservation or
   reloading before the next dependent action. Deduplicate only when the same
   rendered content is still available. Claude's bounded reattachment and
   OpenCode's pruning exemption are useful precedents. Indefinite full-body
   pinning would trade instruction loss for unbounded context growth. [E7–E8,
   O4, C1, as-client]
3. **Specify guaranteed explicit invocation.** Keep leading slash invocation
   deterministic. Make skills discoverable alongside commands and explain
   collisions. For guaranteed inline selection, prefer an explicit selection
   token/chip carrying canonical identity over asking the model to infer whether
   a slash mention is operative. Preserve ordinary prose, examples, code, and
   negated mentions. Codex proves deterministic inline references are possible;
   it does not establish that every slash-looking token should activate a skill.
   Changing our inline semantics needs a deliberate product decision. [E2, E4,
   E6, P2, O3, X1, as-client]
4. **Separate invocation policy from execution permissions.** Support a clear
   user-only mode for side-effectful workflows, or warn when imported controls
   are unsupported. Surface unsupported `allowed-tools` rather than implying
   enforcement. Keep skill text inert by default; preserve the separate trust
   decision for executable plugin commands. Catalog visibility, permission to
   read instructions, and permission to execute actions are distinct. [E1, E3,
   E9, X4, C1]
5. **Improve portability and author feedback.** Consider `.agents/skills/`
   discovery alongside existing directories, with explicit precedence and trust
   behavior. Preserve useful compatibility metadata and report malformed skills
   and collisions. Favor concise, discriminative descriptions and small bodies
   with conditional references. These changes need not include wholesale Claude
   command/template compatibility. [E1, as-spec, as-client, as-author]

### Verification to require before changing behavior

Use deterministic plumbing tests for canonical resolution, ambiguous names,
command collisions, user-input preservation, equivalent directory context,
arguments, missing files, unsupported controls, oversized bodies, reload, and
compaction/restoration. Exercise each invocation route at the provider-request
boundary, not just a rendering helper.

Use separately opted-in **live model evaluations** for implicit selection and
inline prompt compliance. Include operative requests, quoted examples, fenced
code, paths/URLs, negations, multiple skills, and near-miss names. Measure whether
the skill is actually loaded before dependent work. Our existing scripted inline
test is valuable plumbing coverage and cannot replace that evaluation. [E6]

## Local evidence

- [E1: discovery and parsing](../../agent/skill/skills.go), lines 15–103 and
  152–189; [initialization](../../agent/session_init.go), lines 1350–1377.
- [E2: catalog and inline instructions](../../agent/prompts/sections/skills.md.tmpl);
  [message role](../../agent/session_model_call.go), lines 903–920.
- [E3: tool activation](../../agent/session_tools_communicate.go), lines 176–198;
  [exact lookup](../../agent/session_tool_registry.go), lines 288–291.
- [E4: runtime slash expansion](../../agent/session_slash_command.go), lines
  21–72; [user-input boundary](../../agent/session_lifecycle.go), lines 1388–1398.
- [E5: delegate preload](../../agent/subagents.go), lines 933–958 and 1224–1227;
  [prompt construction](../../agent/session_prompts.go), line 158;
  [preload section](../../agent/prompts/sections/activated-skills.md.tmpl).
- [E6: inline plumbing test](../../agent/session_skills_test.go), lines 135–225;
  [slash resolution tests](../../agent/session_slash_command_test.go), lines 39–187.
- [E7: tool output limit](../../agent/internal/tool/registry.go), lines 972–999;
  [truncation tests](../../agent/internal/tool/registry_test.go), lines 422–451
  and 856–888; [artifact retention](../../agent/session_tool_artifacts.go),
  lines 26–37.
- [E8: observation masking](../../agent/internal/contextmgr/context_manager.go),
  lines 654–734 and 868–870; default-path exclusion, lines 223–227;
  checkpoint collection/rendering, lines 1089–1092 and 1156–1173.
- [E9: existing skills/commands documentation](../skills.md), especially the
  trust model and client caveats. Its claim that Codex skill bodies reach the
  model only through model-initiated tool calls needs correction; see the Codex
  comparison above.

[as-spec]: https://agentskills.io/specification
[as-client]: https://agentskills.io/client-implementation/adding-skills-support
[as-author]: https://agentskills.io/skill-creation/best-practices
[cc-skills]: https://code.claude.com/docs/en/skills

## Upstream evidence

- P1: [Pi catalog](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/skills.ts#L348-L382)
  and [system placement](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/system-prompt.ts#L43-L67).
- P2: [Pi explicit expansion](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/agent-session.ts#L1362-L1385)
  and [user message](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/agent-session.ts#L1265-L1277).
- P3: [Pi templates](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/prompt-templates.ts#L20-L102),
  [trusted discovery](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/package-manager.ts#L2397-L2499),
  [metadata controls](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/skills.ts#L304-L342),
  and [summary serialization](https://github.com/badlogic/pi-mono/blob/08dc60bc52d89d6823a9738cc90b1916e5e446e5/packages/coding-agent/src/core/compaction/utils.ts#L88-L149).
- O1: [OpenCode HTTP route](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts#L331-L338)
  and [separate core loader](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/core/src/skill.ts#L33-L124).
- O2: [OpenCode body cache](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/skill/index.ts#L123-L139),
  [catalog placement](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/session/system.ts#L107-L118),
  and [skill tool](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/tool/skill.ts#L8-L66).
- O3: [OpenCode skill commands](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/command/index.ts#L134-L151)
  and [command expansion](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/session/prompt.ts#L1356-L1473).
- O4: [OpenCode truncator](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/tool/truncate.ts#L75-L140),
  [pruning exemption](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/session/compaction.ts#L288-L303),
  and [summary limit](https://github.com/anomalyco/opencode/blob/b7ca4f91d9222dddb8487e689d3274a732999fd9/packages/opencode/src/session/compaction.ts#L28-L79).
- C1: [Claude Code skills][cc-skills], especially “Control who invokes a skill,”
  “Inject dynamic context,” and “Skill content lifecycle.”
- X1: [Codex catalog/body roles](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/ext/skills/src/fragments.rs#L39-L109),
  [mention parsing](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/skills/src/mentions.rs#L79-L149),
  and [TUI structured selection](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/tui/src/chatwidget/input_submission.rs#L228-L276).
- X2: [Codex explicit injection boundary](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/core/src/session/turn.rs#L1016-L1050)
  and [host loader](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/ext/skills/src/host_prompt.rs#L69-L104).
- X3: [Codex model guidance](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/ext/skills/src/catalog_prompt.rs#L7-L40),
  [package read schema](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/ext/skills/src/tools/read.rs#L34-L67),
  and [deprecated custom prompts](https://developers.openai.com/codex/custom-prompts/).
- X4: [Codex invocation policy](https://developers.openai.com/codex/skills/)
  and [metadata loader](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/ext/skills/src/loader/metadata.rs#L27-L55).
- X5: [Codex local compaction filtering](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/core/src/compact.rs#L553-L579)
  and [contextual skill recognition](https://github.com/openai/codex/blob/5c013177d85b72d61dc35e0634511c6d55af5cc5/codex-rs/core/src/context/contextual_user_message.rs#L19-L31).

## Verification scope

The following gate ran after `go mod download` in the isolated study worktree
at the Evener revision above and exited zero:

```sh
go test ./agent/skill ./agent ./agent/internal/contextmgr ./agent/internal/tool \
  -run 'Test(ExpandSlashCommand|UseSkill|StandaloneSkill|ProcessOneInput_SlashCommand|ProcessOneInput_ContinuationDoesNotExpandSlashCommand|LoadSkillBody|DiscoverSkills|BuildPromptDataHasUseSkill|SummarizeToolResult_UseSkill|MaskObservations|ToolRegistry_Trunc|TruncateChars_UTF8Aware)' \
  -count=1
```

All four selected packages passed. These are focused existing plumbing tests,
not a full repository gate or a live-model evaluation. No production code or
tests were changed for this study. Upstream full test suites and the Claude Code
binary were not exercised. Source reports were independently checked against
decisive loader implementations and current primary documentation. A separate
local fact check corrected the draft's compaction-strategy scope before delivery.
Final citation validation passed for 19 local links and 25 pinned upstream source
files, including their cited line ranges. Both upstream research passes reviewed
the integrated comparison and reported no factual corrections.
