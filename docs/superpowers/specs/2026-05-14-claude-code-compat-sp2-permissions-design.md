# SP2 — Permissions Matcher and Enforcement (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
Depends on: `docs/superpowers/specs/2026-05-14-claude-code-compat-sp1-config-loader-design.md`

## 1. Goal

SP2 takes the `permissions` block produced by SP1's loader and turns it into a decision oracle that serf consults on every tool call. It parses Claude Code's permission-rule grammar (`Bash`, `Bash(git push *)`, `Read(./.env)`, `Skill(*)`, `mcp__server__tool`, `WebFetch(domain:example.com)`, `Agent(Explore)`), evaluates `permissions.allow`, `permissions.deny`, and `permissions.defaultMode` against a `(tool_name, tool_input)` pair, and returns one of three decisions: `allow`, `deny`, or `ask`. The matcher itself is a pure function. The enforcement layer wires that function into `Session.execTool` immediately after `PreToolUse` hooks fire and before the tool registry executes the call, where SP5's `PermissionRequest`/`PermissionDenied` hooks will later attach.

SP2 ships the contract Claude Code's docs define ([code.claude.com/docs/en/permissions](https://code.claude.com/docs/en/permissions)) for the tool families serf actually exposes. Tool families serf does not host (PowerShell, the sandbox crossover) parse but are inert; tests pin that contract so plugins shipping such rules under serf do not crash.

## 2. Public API Surface

All new symbols live in package `agent`, in a new file `agent/permissions.go`. Names follow SP1's `LoadSerfConfigFile` / `MergeSerfConfigs` triad.

```go
// PermissionMatcher decides whether a tool call is allowed, denied, or
// requires user confirmation. Built once per session from the merged
// PermissionsConfig produced by SP1.
type PermissionMatcher struct {
    deny    []parsedRule
    ask     []parsedRule
    allow   []parsedRule
    mode    PermissionMode
    // env supplies cwd and home for path-anchor resolution.
    env     ExecutionEnvironment
}

// PermissionMode is the parsed form of permissions.defaultMode.
type PermissionMode string

const (
    PermissionModeDefault           PermissionMode = "default"
    PermissionModeAcceptEdits       PermissionMode = "acceptEdits"
    PermissionModePlan              PermissionMode = "plan"
    PermissionModeAuto              PermissionMode = "auto"
    PermissionModeDontAsk           PermissionMode = "dontAsk"
    PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
)

// PermissionDecision is the result of evaluating one tool call.
type PermissionDecision struct {
    // Outcome is one of "allow", "deny", or "ask".
    Outcome PermissionOutcome
    // Rule is the source-string of the rule that produced the decision,
    // or "" when no rule matched (Outcome derived from defaultMode).
    Rule string
    // Reason is human-readable; carried into PermissionDenied hook input
    // and into the tool-call error message.
    Reason string
}

type PermissionOutcome string

const (
    PermissionAllow PermissionOutcome = "allow"
    PermissionDeny  PermissionOutcome = "deny"
    PermissionAsk   PermissionOutcome = "ask"
)

// NewPermissionMatcher parses a PermissionsConfig and returns a ready
// matcher. Parse errors mention the offending rule string. cfg.DefaultMode
// of "" becomes PermissionModeDefault.
func NewPermissionMatcher(cfg PermissionsConfig, env ExecutionEnvironment) (*PermissionMatcher, error)

// Evaluate decides one tool call. toolName is the Claude Code tool name
// (post-MapSerfToolNameToClaude). toolInput is the parsed JSON arguments
// the model emitted; nil is legal and treated as empty.
func (m *PermissionMatcher) Evaluate(toolName string, toolInput map[string]any) PermissionDecision

// ParseRule parses a single permission-rule string. Exposed for testing
// and for SP5 if a hook needs to validate rules emitted via
// hookSpecificOutput.permissionDecisionRules.
func ParseRule(s string) (Rule, error)

// Rule is the parsed form of one permission string (e.g. "Bash(git push *)").
// Exposed as an opaque type; consumers only need its String() and Matches()
// methods.
type Rule interface {
    String() string
    Matches(toolName string, toolInput map[string]any, env ExecutionEnvironment) bool
}
```

The unexported `parsedRule` struct stores `(rule Rule, source string)` so decisions can report which rule matched.

### Wiring helpers

```go
// AskFallback dictates what Evaluate returns when a rule yields "ask" on a
// surface that has no human (serf -p, serfeval, hub batch). Surfaces opt in
// to one of these at session construction.
type AskFallback int

const (
    AskFallbackInteractive AskFallback = iota // ask reaches the user as-is
    AskFallbackDeny                            // ask collapses to deny
    AskFallbackAllow                           // ask collapses to allow (escape hatch; SP2 forbids by default)
)

// AskFallback is plumbed onto SessionConfig (§9) so each entry point picks one.
```

## 3. Permission-Rule Grammar

Source of truth: [code.claude.com/docs/en/permissions](https://code.claude.com/docs/en/permissions). SP2 implements the documented behavior exactly; deviations are listed in §11.

A rule is `Tool` or `Tool(specifier)`. The empty specifier `Tool()` is rejected at parse time (no upstream pattern uses it). `Tool(*)` is a documented alias for `Tool` — SP2 normalizes it during parse.

Supported tool keywords:

| Keyword       | Specifier form                                          | Source name on a serf tool call                          |
| ---           | ---                                                      | ---                                                       |
| `Bash`        | command pattern with glob                                | serf's `shell` tool, mapped to `Bash` by `MapSerfToolNameToClaude` |
| `Read`        | gitignore-style path pattern                             | `read_file`, `Grep`, `Glob` (Claude's "best effort")     |
| `Edit`        | gitignore-style path pattern                             | `edit_file`, `write_file`, `notebook_edit`               |
| `Write`       | alias for `Edit`                                         | same as `Edit`                                            |
| `WebFetch`    | `domain:<host>` typed specifier                          | `web_fetch`                                               |
| `WebSearch`   | bare only in v1; specifier reserved                      | `web_search`                                              |
| `Skill`       | skill name, `*` allowed                                  | invoked via the skills subsystem; tool name `Skill`      |
| `Agent`       | subagent name                                            | `spawn_agent`, mapped to `Agent`                          |
| `mcp__<srv>`  | bare server, `mcp__<srv>__<tool>`, `mcp__<srv>__*`       | tool name as registered (`mcp__server__tool`)            |
| `PowerShell`  | parses; never matches (serf has no PowerShell tool)      | inert                                                     |

Any other tool keyword parses as bare-or-specifier (treated as an exact-match on tool name and an opaque specifier) and warns once per startup that the matcher will treat the specifier as a literal-equality check. This keeps user-typed rules for third-party tools from being silently dropped.

### 3.1 Bare form

`Bash`, `WebFetch`, `Skill`, `Read`, … match any invocation of that tool, regardless of input. `Tool(*)` is equivalent.

### 3.2 Bash patterns

Wildcard semantics, exactly as Claude Code documents:

- `Bash(npm run build)` — exact match of the full command string.
- `Bash(npm run *)` — prefix match; the space before `*` enforces a word boundary, so `npm run-build` does **not** match.
- `Bash(ls*)` — substring-prefix without a word boundary, so `lsof` matches.
- `Bash(* install)` — suffix match.
- `Bash(git * main)` — interior wildcard; `*` matches any sequence including spaces.
- `Bash(ls:*)` — trailing `:*` is sugar for trailing ` *`. Recognized **only** at end of pattern. In `Bash(git:* push)`, the colon is literal and the rule does not match `git push origin main`.

Compound commands split on `&&`, `||`, `;`, `|`, `|&`, `&`, and newline. A rule must match **every** subcommand for the compound to match. SP2 implements compound-command splitting using a small hand-rolled tokenizer that respects single-quote and double-quote spans (quoted operators do not split). Inside a quoted span the operator is literal.

Process-wrapper stripping is implemented per the docs: `timeout`, `time`, `nice`, `nohup`, `stdbuf`, and bare `xargs` (no flags) are peeled before matching. Other wrappers (`devbox`, `docker exec`, `find -exec`, `watch`, etc.) are not stripped.

**Out of scope for v1, documented as a deferral in §11:** the Claude-Code "built-in read-only command" allowlist (`ls`, `cat`, `echo`, …) that bypasses permission checks entirely. Serf does not run a sandbox, and serf's `shell` tool already has its own pre-existing safety review path. SP2 does **not** auto-allow these commands; if a user wants them prompt-free, they write `"Bash(ls *)"` in their allow list. Tests pin this behavior so the deferral is explicit.

### 3.3 Read / Edit path patterns

Read and Edit rules use the **gitignore specification**, with four anchor types:

| Pattern              | Anchor                                            |
| ---                   | ---                                                |
| `//abs/path/**`       | absolute filesystem root                          |
| `~/path`              | user home (`os.UserHomeDir`)                      |
| `/path` (one slash)   | project root (git root via `ExecutionEnvironment`) |
| `path` or `./path`    | current working directory                         |

Glob semantics inside the path are gitignore: `*` matches within one path segment, `**` matches across segments. A bare filename like `Read(.env)` is equivalent to `Read(**/.env)` — it matches at any depth under its anchor.

`Edit` rules also fire on `write_file` and `notebook_edit`. `Write` is an alias for `Edit`.

Implementation note: serf already vendors a gitignore implementation in `agent/git_snapshot.go` (verify when implementing; if it does not expose a `Match(path, pattern)` function, pull `github.com/sabhiram/go-gitignore` or implement a minimal matcher restricted to the patterns Claude Code documents — no `!` negation, no escaped `#`).

Symlink resolution is **out of scope for v1**. Tests pin that SP2 evaluates against the literal path the tool was called with. Symlink double-checking is a follow-up; documented in §11.

### 3.4 WebFetch

`WebFetch` matches any web fetch. `WebFetch(domain:example.com)` matches calls whose target URL has `host == example.com` after `url.Parse`. Wildcards in the domain (`WebFetch(domain:*.example.com)`) follow Claude Code's documented behavior — match any subdomain at one level. `WebFetch(domain:**.example.com)` is rejected at parse time (Claude Code does not document `**` for hosts).

The specifier-prefix grammar (`domain:`) is extensible: SP2 reserves `port:`, `scheme:`, `path:` as parse-error-for-now sentinels so future Claude Code releases adding those do not silently match.

### 3.5 MCP

Three forms, all documented:

- `mcp__puppeteer` — matches any tool whose name has prefix `mcp__puppeteer__`.
- `mcp__puppeteer__*` — equivalent to bare server form.
- `mcp__puppeteer__puppeteer_navigate` — exact match on full tool name.

MCP rules have no parenthesized specifier. The tool-name itself **is** the rule, in canonical Claude-Code MCP naming (`mcp__server__tool`).

### 3.6 Skill

- `Skill` — every skill invocation.
- `Skill(*)` — equivalent.
- `Skill(name)` — exact match on skill name.
- `Skill(prefix:*)` / `Skill(prefix *)` — same wildcard rules as Bash (matches a name prefix). Tested but lightly used.

Tool name on the serf side is `Skill`. The skill name lives in `tool_input.name`.

### 3.7 Agent

- `Agent(Explore)`, `Agent(Plan)`, `Agent(my-custom)` — exact match on subagent name.
- Wildcards as for Skill.
- Tool name on the serf side is `Agent` (mapped from `spawn_agent` by `MapSerfToolNameToClaude`). The subagent type lives in `tool_input.subagent_type`.

### 3.8 Parse-error cases

- Empty string.
- Unbalanced parens (`Bash(rm`, `Bash(rm))`).
- Unknown tool keyword **and** the keyword does not match `mcp__.+`. (Unknown keywords still parse for forward-compat — see §3, last paragraph — but a leading `mcp__` with no `__tool` suffix beyond the server name still parses per §3.5.)
- `Tool()` empty specifier.
- `WebFetch(<not-domain:>...)` — typed specifier where the prefix is unsupported.
- Pattern containing a NUL byte.

Errors are reported during `NewPermissionMatcher` and carry the source string verbatim: `permission rule %q: <reason>`.

## 4. Matching Algorithm

`PermissionMatcher.Evaluate(toolName, toolInput)`:

1. If `mode == PermissionModeBypassPermissions`, return `{Outcome: allow, Reason: "bypassPermissions mode"}`. Deny rules are still consulted as a circuit breaker for `rm -rf /`-class commands; **deferred to a follow-up** — the docs say only the literal-root removal still prompts, and serf has no sandbox to enforce it. Tests pin "bypass means allow" for v1 and note the gap.
2. If `mode == PermissionModePlan` and the tool is in the *writes-files* set (`Edit`, `Write`, `NotebookEdit`, and `Bash` when the command parses as non-read-only — see §6.2), return `{Outcome: deny, Reason: "plan mode forbids file mutation"}`.
3. Walk `deny` rules in order. First match: return `{Outcome: deny, Rule: r.source, Reason: "denied by " + r.source}`.
4. Walk `ask` rules in order. First match: return `{Outcome: ask, Rule: r.source, Reason: "ask required by " + r.source}`.
5. Walk `allow` rules in order. First match: return `{Outcome: allow, Rule: r.source}`.
6. No rule matched. Apply `mode`:
   - `default` → `ask`.
   - `acceptEdits` → if the call is an `Edit`-class write to a path inside the working directory or its `additionalDirectories`, return `allow`; else `ask`. v1 reads "additionalDirectories" as empty (SP1 does not parse the field yet; it lives in CC's top-level settings, not `permissions`). Documented gap in §11.
   - `plan` → `deny` for mutations (already handled in step 2), `allow` for reads, `ask` for everything else.
   - `auto` → `ask` in v1. The classifier the CC docs reference is an Anthropic-internal service we do not host. Documented in §11.
   - `dontAsk` → `deny` (matches CC docs: "Auto-denies tools unless pre-approved via `/permissions` or `permissions.allow` rules").
   - `bypassPermissions` → already handled in step 1.

The first-matching-rule-wins precedence inside each list mirrors Claude Code exactly. The deny → ask → allow precedence between lists is the cross-list rule.

### 4.1 Per-rule matching detail

A rule matches a `(toolName, toolInput)` pair when:

- The rule's **tool keyword** equals `toolName` (after `MapSerfToolNameToClaude`), **or** for MCP rules the rule string is a prefix-or-exact match on `toolName`.
- The rule's **specifier** matches the relevant slice of `toolInput`:
  - `Bash` → `toolInput["command"]` as a string. The command is split on the shell operators in §3.2; every subcommand must satisfy the pattern after wrapper-stripping.
  - `Read` / `Edit` → `toolInput["file_path"]` (the canonical serf field — verify in `agent/tool_registry.go`). For `Grep`/`Glob`, the field is the search root.
  - `WebFetch` → `toolInput["url"]` parsed via `url.Parse`; specifier matched against `.Host`.
  - `Skill` → `toolInput["name"]`.
  - `Agent` → `toolInput["subagent_type"]`.
  - `mcp__*` → no specifier; the tool name carries the match.

When a required `toolInput` field is missing or wrong type, the rule **does not match**. (Defaulting to "match" or "deny" both surprise users; "do not match" lets the next rule or the `defaultMode` handle it.)

### 4.2 Pattern compilation

Each parse-time pattern compiles to a closure or a small struct:

- Bash patterns compile to a regular expression built from the glob. `*` → `.*`. A trailing ` *` or `:*` anchors with `(?:\s.*)?$`. A leading `* ` anchors with `^(?:.*\s)?`. Word-boundary behavior is exactly what the regex `\b`-equivalent produces given the documented examples. Tests fix this with a table.
- Path patterns compile to a gitignore-matcher entry pinned to the anchor type.
- Domain patterns compile to either an exact host match or a `*.suffix` match.
- Skill / Agent specifiers compile to the same wildcard machinery as Bash.

Compilation happens once at `NewPermissionMatcher`. `Evaluate` is allocation-free in the happy path.

## 5. Decision Algorithm — Worked Examples

Inputs are the documented examples from `permissions.md`, run against a session whose `mode == "default"`.

| Config | Tool call | Expected decision |
| --- | --- | --- |
| `allow:["Bash(npm run *)"], deny:["Bash(git push *)"]` | `Bash{command:"npm run test"}` | allow (rule `Bash(npm run *)`) |
| same | `Bash{command:"git push origin main"}` | deny (rule `Bash(git push *)`) |
| same | `Bash{command:"git status"}` | ask (no rule, `default` mode falls back to ask) |
| `deny:["Read(./.env)"], allow:["Read(./**)"]` | `Read{file_path:".env"}` | deny |
| same | `Read{file_path:"src/main.go"}` | allow |
| `allow:["WebFetch(domain:github.com)"]` | `WebFetch{url:"https://github.com/x"}` | allow |
| same | `WebFetch{url:"https://gitlab.com/x"}` | ask |
| `allow:["mcp__puppeteer"]` | tool `mcp__puppeteer__navigate` | allow |
| `allow:["mcp__puppeteer__navigate"]` | tool `mcp__puppeteer__click` | ask |
| `deny:["Agent(Explore)"]` | `Task{subagent_type:"Explore"}` | deny |
| `mode=plan, allow:["Edit(*)"]` | `Edit{file_path:"src/foo.go"}` | deny (plan-mode mutation block precedes allow rules) |
| `mode=acceptEdits` | `Edit{file_path:"<cwd>/src/foo.go"}` | allow (acceptEdits short-circuit) |
| `mode=bypassPermissions` | any tool | allow |
| `mode=dontAsk` | any tool with no matching allow | deny |

Each row becomes one table-driven test (§10).

## 6. Integration Point

### 6.1 Where the matcher is consulted

`agent/session.go:1275` defines `func (s *Session) execTool(ctx, call llm.ToolCallData) ToolExecResult`. Lines 1276–1301 run the `PreToolUse` hook block and can short-circuit with `Denied`. SP2 inserts its own check **immediately after** that block, before line 1303's `argsJSON, _ := json.Marshal(call.Arguments)`:

```go
// SP2: permission rules. Runs after PreToolUse so a hook returning
// updatedInput is seen by the matcher; runs before execution so a deny
// short-circuits without invoking the tool registry.
if s.permissionMatcher != nil {
    claudeName := MapSerfToolNameToClaude(call.Name)
    var toolInput map[string]any
    if len(call.Arguments) > 0 {
        _ = json.Unmarshal(call.Arguments, &toolInput)
    }
    decision := s.permissionMatcher.Evaluate(claudeName, toolInput)
    switch decision.Outcome {
    case PermissionDeny:
        // SP5 hook hook hook (PermissionDenied) fires here.
        return s.permissionDeniedResult(call, decision)
    case PermissionAsk:
        // SP5 hook hook hook (PermissionRequest) fires here.
        decision = s.resolveAsk(ctx, call, decision)
        if decision.Outcome == PermissionDeny {
            return s.permissionDeniedResult(call, decision)
        }
    case PermissionAllow:
        // fall through to execution.
    }
}
```

The matcher consumes `call.Arguments` **after** any `updatedInput` rewrite from a PreToolUse hook (the hook block at line 1278–1300 already mutates the call indirectly — SP5 hardens this; SP2 just reads `call.Arguments` from whatever it is at that point).

Tools that have side effects merely by being parsed (none today) would need a redesign; serf's tool registry is execution-only.

### 6.2 Session wiring

`Session` (`agent/session.go:214`) grows one field:

```go
permissionMatcher *PermissionMatcher
```

`NewSession` constructs it from a new `SessionConfig.Permissions PermissionsConfig` field (SP1 hands the merged value through; SP8 wires it from `DiscoverSerfConfig` into `SessionConfig`). `NewSession` also reads `SessionConfig.PermissionAskFallback AskFallback` so the entry-point chooses its surface behavior.

`SessionConfig.AllowedToolNames` and `SessionConfig.DeniedToolNames` (existing — `session.go:188-189`) continue to work for the subagent-restriction path. SP2 layers on top: subagent restrictions filter the tool registry before a call is ever made; SP2 evaluates whatever does get attempted.

### 6.3 Helper methods on Session

```go
// permissionDeniedResult produces a ToolExecResult shaped exactly like the
// PreToolUse hook-deny path (existing line 1293), so the model sees a
// uniform "tool denied" surface regardless of which layer denied.
func (s *Session) permissionDeniedResult(call llm.ToolCallData, d PermissionDecision) ToolExecResult

// resolveAsk emits a PermissionRequest hook (SP5) and consults the
// AskFallback. Returns the (possibly modified) decision.
func (s *Session) resolveAsk(ctx context.Context, call llm.ToolCallData, d PermissionDecision) PermissionDecision
```

`resolveAsk` is the integration seam where SP5 owns the *hook firing* and serf surfaces (CLI/TUI/Hub) own the *user prompt*. SP2 owns the contract:

- If a `PermissionRequest` hook returns `permissionDecision: "allow"`, `"deny"`, or `"defer"` (per SP5), that result overrides the matcher's `ask`.
- Else, dispatch to a surface-specific resolver based on `cfg.PermissionAskFallback`.
- The default for non-interactive surfaces is `AskFallbackDeny` (see §9).

## 7. Interaction with `PermissionRequest` / `PermissionDenied` Hooks (SP5)

SP2 owns the *trigger*; SP5 owns the *hook plumbing*.

- **`PermissionRequest`** fires from `Session.resolveAsk` immediately before the surface prompts. Its `HookInput` carries `tool_name`, `tool_input`, `permission_rule` (the matched source string), and `permission_mode`. SP5 defines the schema; SP2 provides the values.
- **`PermissionDenied`** fires from `Session.permissionDeniedResult` immediately before the result is returned. Same input shape, plus `denied_by`: one of `"deny_rule"`, `"ask_fallback"`, `"plan_mode"`, `"dontAsk_mode"`.

Hook output:

- A `PermissionRequest` hook returning `permissionDecision: "allow"` short-circuits to allow.
- Returning `"deny"` short-circuits to deny **and** fires `PermissionDenied` with `denied_by: "request_hook"`.
- Returning `"defer"` (SP5's new value) falls through to the surface prompt.

Deny rules outrank hooks — a `PermissionRequest` hook cannot upgrade a `deny`-rule match to `allow`. This mirrors Claude Code's docs: *"Deny and ask rules are evaluated regardless of what a PreToolUse hook returns"* — SP2 enforces the same invariant for its own ask phase.

`PermissionDenied` hooks are observe-only. Their return value is ignored. They exist so plugins can log denials, post to Slack, etc.

## 8. Error Contracts

### 8.1 Parse-time (config load)

`NewPermissionMatcher` returns `(nil, error)` if any rule fails to parse. Error text format: `permission rule %q: <reason>` — e.g. `permission rule "Bash(rm": unbalanced parentheses`.

`DiscoverSerfConfig` (SP1) does not call `NewPermissionMatcher`; SP8 does, at session bootstrap. A parse error aborts session startup with the rule-source and the originating file path (SP8 wraps with `serf config <path>:`).

### 8.2 Runtime

`Evaluate` cannot error. Inputs that cannot be matched (e.g. a `Bash` call with no `command` field) yield "no rule matched" and fall through to the `defaultMode` branch. This matches Claude Code's behavior: malformed tool input is the model's problem, not the matcher's.

`resolveAsk` may error only when the surface prompt itself errors (e.g. the user closes the TUI mid-prompt, or a hook returns invalid JSON). The error path collapses to deny with `Reason: "ask resolution failed: <err>"`.

## 9. Package and File Layout

New files in `agent/`:

| File | Purpose |
| --- | --- |
| `agent/permissions.go` | All §2 public API, the unexported parser, and per-rule matchers. |
| `agent/permissions_test.go` | All §10 tests. |
| `agent/testdata/permissions/*.json` | Fixture configs used by integration tests. |

Existing files modified:

| File | Change |
| --- | --- |
| `agent/session.go` | Add `permissionMatcher *PermissionMatcher` field on `Session`; construct in `NewSession` from new `SessionConfig.Permissions` and `SessionConfig.PermissionAskFallback`; insert the §6.1 enforcement block in `execTool`. |
| `agent/session.go` | Add helpers `permissionDeniedResult` and `resolveAsk`. |

No other files change. SP8 wires `SessionConfig.Permissions` from `DiscoverSerfConfig`; SP5 wires the `PermissionRequest`/`PermissionDenied` hook events.

`SessionConfig` (SP1's spec already names this field on the boundary):

```go
type SessionConfig struct {
    // ...
    Permissions             PermissionsConfig
    PermissionAskFallback   AskFallback
}
```

Default for `PermissionAskFallback` per surface:

| Surface                             | Default |
| ---                                  | ---      |
| `serf` interactive (TTY)             | `AskFallbackInteractive` |
| `serf` non-interactive (`-p`, stdin) | `AskFallbackDeny`        |
| `serf-tui`                           | `AskFallbackInteractive` |
| `serf-hub` (web)                     | `AskFallbackInteractive` |
| `serfeval`                           | `AskFallbackDeny`        |
| Subagents                            | inherit from parent      |

Each entry-point chooses at session construction. SP2 ships the type and the default; SP8 wires the surface logic.

## 10. Testing Strategy

TDD: write all of §10 first, then implement §2–6 until tests pass. No mocked filesystem; `t.TempDir()` for path-pattern tests. No mocked LLM. Fixtures are small JSON files when shared; inline-built `PermissionsConfig` values when one-off.

### 10.1 `ParseRule` table

`TestParseRule` is one big table. Each row: input string, expected `(rule.String(), parse-error-substring)`.

| # | Input | Expect |
| --- | --- | --- |
| 1 | `Bash` | `("Bash", "")` |
| 2 | `Bash(*)` | `("Bash", "")` (normalized) |
| 3 | `Bash(npm run build)` | exact-form |
| 4 | `Bash(npm run *)` | prefix-with-boundary |
| 5 | `Bash(ls*)` | prefix-no-boundary |
| 6 | `Bash(* install)` | suffix |
| 7 | `Bash(git * main)` | interior |
| 8 | `Bash(ls:*)` | normalized to `Bash(ls *)` semantics |
| 9 | `Bash(git:* push)` | colon-literal; pattern is exact `git:* push` |
| 10 | `Read(./.env)` | cwd-anchored |
| 11 | `Read(//tmp/scratch.txt)` | absolute-anchored |
| 12 | `Read(~/.zshrc)` | home-anchored |
| 13 | `Read(/docs/**)` | project-root-anchored |
| 14 | `Edit(src/**/*.ts)` | cwd-anchored, double-star |
| 15 | `Write(src/foo.go)` | alias of Edit |
| 16 | `WebFetch` | bare |
| 17 | `WebFetch(domain:example.com)` | host-exact |
| 18 | `WebFetch(domain:*.example.com)` | subdomain-wildcard |
| 19 | `WebFetch(domain:**.example.com)` | error `**` |
| 20 | `WebFetch(port:443)` | error unsupported specifier |
| 21 | `Skill(my-skill)` | exact skill |
| 22 | `Skill(*)` | bare-equivalent |
| 23 | `Agent(Explore)` | exact agent |
| 24 | `Agent(my-custom)` | exact agent |
| 25 | `mcp__puppeteer` | server-prefix |
| 26 | `mcp__puppeteer__*` | server-prefix (alias) |
| 27 | `mcp__puppeteer__click` | exact MCP tool |
| 28 | `Bash(rm` | error unbalanced |
| 29 | `Bash()` | error empty specifier |
| 30 | (empty string) | error |
| 31 | `Unknown(thing)` | parses (forward-compat); warning recorded |
| 32 | `Read(\x00foo)` | error NUL byte |
| 33 | `PowerShell(Get-ChildItem *)` | parses; inert under serf |

### 10.2 `Evaluate` table (Bash semantics)

`TestEvaluateBash` exercises every Bash rule shape against multiple inputs. Each row: matcher config, `(toolName, toolInput)`, expected decision (Outcome + Rule).

Rows include the entire word-boundary contract — `Bash(ls *)` vs `Bash(ls*)` against `ls -la`, `lsof`, `ls`, `ls foo`. Also compound-command rows: `Bash(safe-cmd *)` against `safe-cmd foo && other-cmd` must deny-or-ask (no allow rule covers `other-cmd`). Process-wrapper rows: `Bash(npm test *)` against `timeout 30 npm test bar` allows; against `xargs -n1 npm test` denies (xargs with flags is not stripped); against `xargs grep` … (verify by reading docs again — bare xargs is stripped).

At least 30 rows. Each example pulled from the verbatim docs is one row.

### 10.3 `Evaluate` table (Read/Edit paths)

`TestEvaluatePaths` uses `t.TempDir()` to create a fake git root with `<root>/sub/.env`, `<root>/docs/x.md`, `<root>/src/main.go`, plus a home-dir surrogate via `t.Setenv("HOME", ...)`. Rows cover:

- `Read(./.env)` matches `Read{file_path:".env"}` evaluated with cwd=`<root>`.
- `Read(.env)` (no anchor) matches `Read{file_path:"sub/.env"}` (gitignore depth-agnostic semantics).
- `Read(/docs/**)` matches `Read{file_path:"docs/x.md"}` evaluated with project-root anchor from `ExecutionEnvironment`.
- `Read(//tmp/scratch.txt)` does NOT match `Read{file_path:"tmp/scratch.txt"}` (relative path).
- `Read(//tmp/scratch.txt)` DOES match `Read{file_path:"/tmp/scratch.txt"}`.
- `Read(~/.zshrc)` matches `Read{file_path:"~/.zshrc"}` resolved to `<home>/.zshrc`.
- `Edit(src/**/*.ts)` matches a `.ts` write; does not match `.go`.
- `Read(/docs/**)` does NOT match `<project>/.claude/docs/x.md` (anchor pinned to `/docs/`, not deep).
- Missing `file_path` field → no match.

### 10.4 `Evaluate` table (WebFetch / Skill / Agent / MCP)

`TestEvaluateOtherTools`:

- WebFetch domain exact, subdomain wildcard, mismatch, missing url, malformed url.
- Skill exact, Skill wildcard, Skill bare, missing name.
- Agent exact, Agent missing subagent_type, Agent wildcard.
- MCP server-prefix matching `mcp__puppeteer__click`; mismatch on different server; MCP-exact-tool matching only that tool.

### 10.5 Decision precedence

`TestEvaluatePrecedence`:

- Deny beats allow when both match (`deny:[Bash(rm *)]`, `allow:[Bash(*)]`, call `rm -rf foo` → deny).
- Ask beats allow when both match.
- Deny beats ask.
- First-matching-rule wins inside a list: two allow rules both matching, only the first is reported in `decision.Rule`.
- No rule matches → falls back to `defaultMode`.

### 10.6 `defaultMode` behavior

`TestEvaluateDefaultMode`:

| Mode | No rule matches | Expected outcome |
| --- | --- | --- |
| `default` | `Bash{ls}` | ask |
| `acceptEdits` | `Edit{file_path: <cwd>/x}` | allow |
| `acceptEdits` | `Edit{file_path: //tmp/x}` | ask |
| `acceptEdits` | `Bash{ls}` | ask |
| `plan` | `Bash{ls}` | allow (read-only command — see deferral; v1 returns ask, test asserts that) |
| `plan` | `Edit{file_path: x}` | deny |
| `plan` | `Read{file_path: x}` | allow |
| `auto` | any | ask (v1 behavior; documented in §11) |
| `dontAsk` | any | deny |
| `bypassPermissions` | any | allow |

### 10.7 `Session.execTool` integration

`TestSessionExecToolPermissions` exercises the wiring through the real `Session` and a stub `ToolRegistry`. No LLM. Builds a session with:

- A `PermissionsConfig` carrying `deny:["Bash(rm *)"]` and a `defaultMode: "default"`.
- A fake `shell` tool registered on the `ToolRegistry` that records every call.
- `PermissionAskFallback: AskFallbackDeny`.

Cases:

1. Tool call matching the deny rule: tool is **not** executed; `ToolExecResult.IsError == true`; `Output` contains the rule source.
2. Tool call matching no rule with `defaultMode: default` and `AskFallbackDeny`: tool is **not** executed; result is a deny.
3. Tool call matching no rule with `defaultMode: default` and `AskFallbackAllow` (test-only): tool **is** executed.
4. Tool call matching no rule with `defaultMode: bypassPermissions`: tool **is** executed.
5. PreToolUse hook returns `updatedInput` rewriting the command: the permission matcher sees the rewritten command (assert by registering a deny rule against the rewrite target and checking it fires).
6. Hook returns `denied=true`: the matcher is never consulted (hook-deny short-circuits earlier).

### 10.8 `Session.execTool` ask path

`TestSessionExecToolAskPath` exercises `resolveAsk` with a stub hook runner that records `PermissionRequest` invocations and returns scripted decisions:

- Hook returns `allow` → execution proceeds.
- Hook returns `deny` → `PermissionDenied` fires; result is deny.
- Hook returns `defer` → falls back to `AskFallbackDeny`; result is deny.
- Hook absent → `AskFallback` directly applies.

### 10.9 Fixtures

`agent/testdata/permissions/cc-docs-examples.json` — every JSON snippet from the Claude Code permissions doc, kept verbatim. A meta-test loads this file, parses it via `NewPermissionMatcher`, and asserts zero parse errors. Catches regressions when the CC doc grows new examples we have not handled.

### 10.10 Coverage gate

- Every exported function in §2 has a direct test row.
- Every error path in §3.8 has a row in §10.1.
- Every `defaultMode` value in §3's type list has a row in §10.6.
- Every Bash example from the CC docs appears verbatim in §10.2.
- `go test ./agent/... -run Permission` is green.

## 11. Open Questions Settled Here

### 11.1 How `ask` surfaces

**Decision.** Each entry point picks an `AskFallback` value at session construction. Defaults are listed in §9. Three concrete behaviors:

- **TTY-attached `serf` and `serf-tui`**: an interactive prompt. `serf` prompts on stdin/stderr; `serf-tui` opens a dialog overlay. The `PermissionRequest` hook fires *before* the prompt so a hook can short-circuit ("auto-approve all calls in this CI run").
- **`serf-hub`**: enqueues a permission request in the session's event stream (`EventPermissionRequest`) and *blocks the tool call* on a `RespondToPermission(sessionID, callID, decision)` API call. Until the API returns, the call hangs. A timeout (default 60s, configurable per-session) collapses to the `AskFallback` value — `AskFallbackDeny` for hub.
- **`serf -p`, `serfeval`**: no human. `AskFallbackDeny` is the default. Documented in `--help`.

**Why this and not "always prompt on stdin"?** Hub is multi-user and async; blocking on stdin breaks it. Eval runs are batched; prompts deadlock them. Letting each surface declare its policy keeps the matcher pure.

**Why `AskFallbackDeny` and not `AskFallbackAllow` as the non-interactive default?** Falling open is the worst class of permission bug. `AskFallbackAllow` exists for the narrow case of an internal test harness; SP2 ships it but the CLI flags do not expose it. Surfaced only via `SessionConfig`.

### 11.2 `acceptEdits` and `plan` mode behavior in serf

**`acceptEdits`.** Documented CC behavior: auto-accepts file edits and common filesystem commands (`mkdir`, `touch`, `mv`, `cp`) within the working directory. Serf's v1 implementation: short-circuit `allow` for `Edit`, `Write`, `NotebookEdit` calls whose `file_path` resolves under `env.WorkingDir()`. Bash shortcuts for `mkdir`/`touch`/`mv`/`cp` are **deferred** — they require parsing the command line to identify the operand, which overlaps the read-only-Bash deferral. Tests pin "Edit auto-accepts in cwd; Bash falls through". Plugins relying on the Bash shortcut see an `ask` they would not see under CC; documented in the SP2 README the implementer ships.

**`plan` mode.** Documented CC behavior: read-only exploration; no file edits. Serf's v1 implementation: deny `Edit`/`Write`/`NotebookEdit` outright; allow `Read`/`Grep`/`Glob`/`WebFetch`/`WebSearch` outright; everything else (`Bash`, `Skill`, `Agent`, `mcp__*`) falls through to the normal rule pipeline. The Bash read-only-allowlist (`ls`, `cat`, …) is **not** implemented in v1; users wanting "cats are fine in plan mode" write `Bash(cat *)` in their allow list. Documented in §11 deferrals.

**`auto` mode.** CC's auto-mode runs an Anthropic-internal classifier we cannot reproduce. Serf's v1: `auto` collapses to `default` (ask on no match). Deferred.

**`dontAsk` mode.** Implemented as documented: deny everything not pre-approved. No deferral.

**`bypassPermissions` mode.** Implemented as "allow everything." The CC-documented `rm -rf /` circuit breaker is **deferred** — it requires the read-only/dangerous-command classifier we have not yet ported.

### 11.3 Deferrals (documented, not blocking)

Each is testable as a pinned "v1 does not do this; here is the workaround" assertion.

1. Symlink double-checking on Read/Edit rules.
2. Built-in read-only Bash command allowlist (`ls`, `cat`, …).
3. `acceptEdits` shortcut for `mkdir`/`touch`/`mv`/`cp`.
4. `bypassPermissions` circuit breaker for root-targeting `rm`.
5. PowerShell rule matching (rules parse and are inert).
6. `Read`/`Edit` rules applying to file-touching Bash subcommands (`cat foo`, `head bar`). v1 leaves Bash to the Bash rules.
7. `additionalDirectories` for `acceptEdits` scope expansion. SP1 does not carry this field yet; SP2 reads cwd only.
8. `auto` mode classifier.
9. `permissions.disableBypassPermissionsMode` / `disableAutoMode` / `disableAutoModeWarning`. These are CC managed-settings flags; SP2 treats them as inert.
10. The `--allowedTools` / `--disallowedTools` CLI flags. Existing serf fields (`AllowedToolNames`, `DeniedToolNames`) cover the bare-tool form; the CC-style flag with full rule syntax is deferred to SP8.

### 11.4 Dependencies on other sub-specs (NOT resolved here)

- **SP1** ships `PermissionsConfig{Allow, Deny, DefaultMode}`. SP2 consumes that exactly.
- **SP5** ships the `PermissionRequest` and `PermissionDenied` hook events and their input/output schemas. SP2 fires them via stable signatures (§7) but does not define their wire shape.
- **SP8** wires `DiscoverSerfConfig` into each entry point and threads `Permissions` + `PermissionAskFallback` into `SessionConfig`. SP2 must export those fields with stable types; SP8 owns the wiring.
- **`Ask` hook input — `permission_rule` field.** SP2 emits the matched rule's source string. SP5's hook schema must accept it (string).

## 12. Implementation Order

For the implementing session, in TDD order:

1. Write `permissions_test.go` with all of §10.1 (`ParseRule` table). Implement `ParseRule` until §10.1 passes.
2. Add §10.2 (Bash `Evaluate`). Implement Bash rule matching. Iterate.
3. Add §10.3 (paths). Implement Read/Edit rule matching, including the gitignore matcher choice.
4. Add §10.4 (WebFetch/Skill/Agent/MCP). Implement.
5. Add §10.5 and §10.6 (precedence and defaultMode). Implement `Evaluate`.
6. Add §10.7 (session integration). Modify `agent/session.go` to insert the §6.1 block and to construct the matcher in `NewSession`.
7. Add §10.8 (ask path). Implement `resolveAsk` against a stub `HookRunner`.
8. Add §10.9 meta-test. Pull every JSON snippet from the CC permissions docs into the fixture.
9. Verify `go test ./agent/... -run Permission` is green.
10. Run `go test ./...` for collateral coverage.
