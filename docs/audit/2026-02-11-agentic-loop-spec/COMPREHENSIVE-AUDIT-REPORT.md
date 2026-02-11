# Agentic Loop Spec Compliance Audit

**Date:** 2026-02-11
**Scope:** Agentic loop implementation only (`coding-agent-loop-spec.md`). Does not cover the unified LLM layer, CLI, or other subsystems.
**Codebase:** `primeradiant.com/serf/internal/agent`
**Method:** 8 parallel auditors, one per spec section (2-9), each exhaustively searching the codebase

---

## Executive Summary

**68 raw gaps found** across 8 spec sections. After de-duplication (some findings appear in multiple sections), there are **55 unique gaps**.

| Severity | Count | Description |
|----------|-------|-------------|
| Critical | 2 | Architectural deviations that significantly affect fidelity |
| Important | 12 | Missing functionality or behavioral differences |
| Minor | 25 | Small deviations, missing tests, naming differences |
| Info | 16 | Extensions beyond spec, cosmetic differences |

**Bottom line:** The core agentic loop is solid. The biggest gaps are: (1) system prompts are NOT 1:1 copies of provider reference agents, (2) no agent-level integration smoke tests with real APIs, and (3) several parity matrix test cases are missing. The "out of scope" features are surprisingly well-implemented (4 of 6 done), and the implementation frequently extends beyond the spec in useful ways.

---

## Critical Gaps

### C1. System prompts are not 1:1 copies of provider reference agents
**Spec:** Section 3.1 — "The initial base for each provider should be a 1:1 copy of the provider's reference agent -- the exact same system prompt, the exact same tool definitions, byte for byte."
**Reality:** The prompts are short custom summaries (23-67 lines) that cover similar topics but are substantially different from codex-rs, Claude Code, and gemini-cli prompts.
**Impact:** Models may produce worse tool calls because they're not seeing the exact prompt they were trained/evaluated against.
**Source:** GAP-3.02

### C2. No agent-level integration smoke tests
**Spec:** Section 9.13 — 7 end-to-end scenarios with real API keys (file creation, editing, shell, truncation, steering, subagents, timeout).
**Reality:** Integration tests only exist at the LLM SDK layer. All agent tests use `fakeAdapter` stubs. There are zero tests that create a `Session`, submit real inputs to real LLM APIs, and verify actual file mutations.
**Impact:** No confidence that the full stack works end-to-end with real providers.
**Source:** GAP-9.13

---

## Important Gaps

### I1. Anthropic profile missing beta headers
**Spec:** Section 3.5 — "pass beta headers (e.g., for extended thinking, 1M context) via `provider_options.anthropic.beta_headers`"
**Reality:** `providerOpts` is an empty map. The adapter supports beta_headers but the profile doesn't populate any.
**Source:** GAP-3.03

### I2. Gemini system prompt references wrong tool name
**Spec:** Section 3.6 — Gemini profile maps `list_dir` to `list_directory`.
**Reality:** The system prompt says `list_dir` but the tool is exposed as `list_directory`. The model sees conflicting names in the prompt vs tool definitions.
**Source:** GAP-3.08

### I3. ProviderProfile lacks tool_registry / custom tools registered on Session not Profile
**Spec:** Section 3.2/3.7 — Profile owns a `tool_registry`; custom tools registered via `profile.tool_registry.register()`.
**Reality:** Session owns the registry. Profiles only carry immutable tool definitions. Custom tools can only be added post-session-creation.
**Source:** GAP-3.01, GAP-3.12

### I4. System prompt built once per input, not per loop iteration
**Spec:** Section 2.5 pseudocode — system prompt built inside the LOOP on each iteration.
**Reality:** Built once before the tool loop. Changes to environment during tool execution within a single input aren't reflected until the next input.
**Source:** GAP-2.05

### I5. SESSION_END emitted twice (after ProcessInput + on Close)
**Spec:** Implies a single SESSION_END per session.
**Reality:** Emitted once when input processing completes ("input_complete") and once on Close() ("session_closed"). Consumers must handle duplicates.
**Source:** GAP-2.10

### I6. Streaming events simulated, not real
**Spec:** Section 2.9 — ASSISTANT_TEXT_START/DELTA/END are streaming events.
**Reality:** Uses single-shot LLM calls. Entire text arrives in one DELTA event, not incrementally.
**Source:** GAP-2.09

### I7. `close_agent` returns bare "closed" string, not final status
**Spec:** Section 7.2 — `close_agent` returns "Final status" (status, output, turns used).
**Reality:** Returns the string `"closed"`. Agent result data is discarded.
**Source:** GAP-7.02

### I8. Subagent `max_turns` default not always 50
**Spec:** Section 7.2 — default 50 when not specified.
**Reality:** If parent has `MaxTurns=100`, subagent inherits 100 instead of defaulting to 50.
**Source:** GAP-7.03

### I9. `send_input` semantics inverted
**Spec:** Section 7.2 — "Send a message to a running subagent."
**Reality:** Rejects calls when the agent IS running. Only works when idle. Functions as "give idle agent a new task" rather than "inject message into active agent."
**Source:** GAP-7.07

### I10. `Close()` does not cancel in-flight LLM stream
**Spec:** Appendix B — Graceful shutdown step 1: "Cancel any in-flight LLM stream."
**Reality:** `Close()` doesn't own or cancel the context. Relies on caller to cancel the context externally.
**Source:** GAP-9.11A

### I11. Three parity matrix test cases missing across providers
**Spec:** Section 9.12 — 15 test cases x 3 providers.
**Reality:** "Tool output truncation", "Reasoning effort change", and "Subagent spawn and wait" are only tested with OpenAI, not as cross-provider parity tests.
**Source:** GAP-9.12A/B/C

### I12. Spec says no automatic compaction, but implementation has full 4-layer system
**Spec:** Section 5.5 — "The agent does NOT perform automatic compaction or summarization (that is out of scope)."
**Reality:** Full ContextManager with observation masking, thinking clearing, checkpoint, and LLM summarization. The spec needs updating.
**Source:** GAP-5.01

---

## Minor Gaps

### Tool & Profile Issues
- **M1.** Anthropic grep missing `output_mode` parameter (content/files_with_matches/count). (GAP-3.04)
- **M2.** `apply_patch` description shorter than spec (missing capability summary). (GAP-3.05)
- **M3.** `edit_file` fuzzy matching lacks Unicode equivalence (only whitespace normalization). (GAP-3.06)
- **M4.** Tool schema root type not validated as "object" during registration. (GAP-3.07)
- **M5.** Extra tools (task_list, communicate, use_skill, web_fetch) in all profiles beyond spec's enumerated lists. (GAP-3.11)
- **M6.** Tool name collision resolution not explicitly tested. (GAP-3.13)
- **M7.** `web_fetch` and `web_search` unconditionally included in Gemini profile (spec marks them "optional"). (GAP-3.14)

### Session & Loop Issues
- **M8.** No EventEmitter abstraction — uses raw Go channel. (GAP-2.02)
- **M9.** Turn types use tagged-union (single `Turn` struct) vs spec's distinct record types. (GAP-2.04)
- **M10.** Round count increments every loop iteration, not only on tool execution rounds. (GAP-2.06)
- **M11.** MaxTurns checked before loop, not inside loop on each round. (GAP-2.07)
- **M12.** Abort from AWAITING_INPUT requires direct Close() call (no signal mechanism). (GAP-2.08)
- **M13.** Custom tools registered post-creation don't appear in system prompt "Tools:" section. (GAP-9.08A)

### Execution Environment Issues
- **M14.** No Windows shell support (always uses `/bin/bash -c`). (GAP-4.01)
- **M15.** `EditFile` method on interface not in spec's interface definition. (GAP-4.03)
- **M16.** Env var deny patterns use `Contains` (broader than spec's suffix-glob `*_API_KEY`). (GAP-4.04)
- **M17.** No `ReadOnlyExecutionEnvironment` wrapper. (GAP-4.06)
- **M18.** Missing test coverage for TOKEN, PASSWORD, CREDENTIAL env var exclusion. (GAP-4.07)
- **M19.** Default filteredEnv allow-list missing CARGO_HOME, NVM_DIR, RUSTUP_HOME, PYENV_ROOT. (GAP-4.08)

### System Prompt Issues
- **M20.** Model field uses raw ID, not display name from catalog. (GAP-6.01)
- **M21.** Provider base prompts cover required topics but are minimal vs native agents. (GAP-6.03/04/05)
- **M22.** No dedicated unit test for `snapshotGit()`. (GAP-6.06)
- **M23.** No parity tests for Anthropic/Gemini project doc filtering. (GAP-6.07)

### Subagent Issues
- **M24.** `working_dir` creates new ExecutionEnvironment (separate PID tracking) instead of sharing parent's. (GAP-7.06)

### Shutdown Issues
- **M25.** No explicit event flush in Close() (best-effort buffered channel). (GAP-9.11B)
- **M26.** Graceful shutdown ordering reversed — subagent cleanup before SESSION_END. (GAP-9.11C)

---

## Info Items

- Session ID uses ULID instead of UUID. (GAP-2.01)
- `tool_output_limits` is richer struct vs spec's simple map. (GAP-2.03)
- 4 extra event kinds beyond spec (COMMUNICATE, SKILL_ACTIVATED, CONTEXT_COMPACTION, WARNING). (GAP-2.11)
- No GrepOptions type (uses expanded parameters). (GAP-4.02)
- No LoggingExecutionEnvironment wrapper (spec frames as example). (GAP-4.05)
- Git/skills blocks between env context and tool descriptions. (GAP-6.02)
- Extra tool output limits for post-spec tools (task_list, web_fetch, etc.). (GAP-5.03)
- Truncation strategy overridable per-tool (beyond spec). (GAP-5.04)
- `reasoning.effort` via first-class field vs provider_options (works correctly). (GAP-3.09/3.10)
- SubAgentHandle type name differs (unexported `subagent`). (GAP-7.01)
- Subagent tool descriptions differ from spec wording. (GAP-7.05)
- `wait` tool has undocumented `timeout_ms` extension. (GAP-7.04)
- Provider-specific editing format covered implicitly by existing parity tests. (GAP-9.12D)
- WARNING event used in code but not in spec's EventKind enum. (GAP-9.10A)
- Sandbox not implemented (spec marks as out-of-scope). (FEATURE-8.03)
- Approval system prompt-only, no runtime enforcement (spec marks as out-of-scope). (FEATURE-8.05)

---

## Out-of-Scope Features Status

| Feature | Status | Notes |
|---------|--------|-------|
| MCP (Model Context Protocol) | Fully Implemented | 3 transports, namespacing, 4-layer config discovery |
| Skills / Custom Commands | Fully Implemented | YAML frontmatter, discovery, progressive disclosure. Missing: user home auto-discovery, allowed-tools enforcement |
| Sandbox / Security Policies | Not Implemented | ExecutionEnvironment interface provides extension point |
| Compaction / Context Summarization | Fully Implemented | 4-layer progressive compaction, ~90 tests |
| Approval / Permission System | Prompt-Only | System prompts reference approval modes but no runtime enforcement |
| Read-Before-Write Guardrail | Fully Implemented | Warns (doesn't block). apply_patch not covered |

---

## Detailed Audit Files

Each section's full findings with file paths, line numbers, and verification details:

- `docs/audit/section-2-agentic-loop.md` — 11 gaps
- `docs/audit/section-3-provider-toolsets.md` — 14 gaps
- `docs/audit/section-4-execution-environment.md` — 8 gaps
- `docs/audit/section-5-output-context.md` — 4 gaps
- `docs/audit/section-6-system-prompts.md` — 7 gaps
- `docs/audit/section-7-subagents.md` — 7 gaps
- `docs/audit/section-8-out-of-scope.md` — 4 feature-level gaps
- `docs/audit/section-9-dod-and-errors.md` — 13 gaps
