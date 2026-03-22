# Build Prompt: Sprout

You are building **sprout** — a self-improving coding agent. The full specification is at `~/prime-radiant/serf/self-improving-agent-spec.md`. Read it thoroughly before writing any code, especially **Appendix D (Design Rationale)** which contains the "why" behind every decision, open questions, anti-patterns to avoid, and implementation pitfalls learned the hard way.

The companion specs for the unified LLM client and coding agent loop are in the same directory: `~/prime-radiant/serf/unified-llm-spec.md` and `~/prime-radiant/serf/coding-agent-loop-spec.md`. These provide context on provider-aligned toolsets, execution environments, and output truncation. Sprout replaces the coding-agent-loop with its own architecture but reuses many of the same concepts.

## Project Setup

Create the repo at `~/prime-radiant/sprout`.

- **Language:** TypeScript
- **Runtime:** Bun (execute .ts directly, no compile step)
- **Testing:** `bun test` (Bun's built-in test runner, jest-compatible API)
- **Monorepo:** No. Single package. Keep it flat until complexity demands otherwise.
- **Dependencies:** Start minimal. Add only what you need as you build. Likely needs: `zod` (schema validation), `@anthropic-ai/sdk`, `openai`, `@google/genai` (provider SDKs for the unified LLM client), an embedding library (defer until Recall needs it).

```
sprout/
├── src/
│   ├── kernel/          # The immutable core: loop, primitives, types
│   ├── genome/          # Genome storage, search, versioning
│   ├── llm/             # LLM client adapters (multi-provider)
│   ├── agents/          # Agent runtime: spawning, lifecycle, events
│   └── learn/           # Learn process: signals, filtering, mutations
├── test/
├── bootstrap/           # Bootstrap agent specs (YAML)
├── package.json
├── tsconfig.json
└── README.md
```

## Build Order

Build in this order. Each phase should have passing tests before moving to the next. **TDD: write tests first, then implementation.**

### Phase 1: Types and Primitives

The foundation. Define the core types and implement the 7 required primitives.

**Types to define:**
- `AgentSpec`, `AgentConstraints` — agent specification
- `Perception`, `RecallResult`, `Delegation`, `ActResult`, `VerifyResult` — loop phase types
- `LearnSignal` — stumble signals for Learn
- `Memory`, `RoutingRule` — genome content types
- `PrimitiveResult` — primitive return type
- `SessionEvent`, `EventKind` — event system

**Primitives to implement:** All core tools from the coding-agent-loop-spec (Section 3), including provider-aligned variants:

- **Universal:** `read_file`, `write_file`, `exec` (shell), `grep`, `glob`, `fetch`
- **Anthropic/Gemini:** `edit_file` — exact string match with old_string/new_string replacement. This is the native editing format for Claude models. See coding-agent-loop-spec Section 3.5.
- **OpenAI:** `apply_patch` — v4a diff format supporting create, delete, update, and rename operations in a single patch. This is the native editing format for GPT models. See coding-agent-loop-spec Section 3.4 and Appendix A for the full v4a grammar.

The agent runtime selects which primitives are available based on the agent's model provider. An agent using Claude sees `edit_file`. An agent using GPT sees `apply_patch`. Both achieve "edit a file" but use the format the model was trained on.

These execute against an `ExecutionEnvironment` interface (see coding-agent-loop-spec Section 4). Start with `LocalExecutionEnvironment`. Each primitive should be independently testable.

**Important:** The `exec` primitive must use shell execution (`/bin/sh -c <command>`) for PATH resolution. Using `execFile` directly will fail with ENOENT for commands like `npx`, `npm`, etc. This is a known pitfall — see self-improving-agent-spec Appendix D.13.

**Tool output truncation:** Implement the head/tail truncation from coding-agent-loop-spec Section 5. Character-based truncation runs first (handles pathological cases like 10MB single-line CSVs), then line-based truncation. Default limits: read_file 50K chars, exec 30K chars, grep 20K chars. The full untruncated output goes to the event stream; the LLM gets the truncated version.

### Phase 2: Unified LLM Client

Build the full unified LLM client from `~/prime-radiant/serf/unified-llm-spec.md`. This is sprout's LLM layer — not a throwaway adapter, the real thing.

The spec defines a four-layer architecture:
1. **Provider Specification** — the `ProviderAdapter` interface
2. **Provider Utilities** — shared SSE parsing, retry logic, HTTP helpers
3. **Core Client** — routing, middleware hooks, configuration
4. **High-Level API** — `generate()`, `stream()` convenience functions

Sprout's agent loop will use the low-level `Client.complete()` and `Client.stream()` directly (same as the coding-agent-loop-spec — the loop manages its own turns).

**Implement all three provider adapters:**
- **Anthropic** — Messages API (`/v1/messages`). Must support: extended thinking with thinking blocks/signatures, prompt caching via `cache_control` annotations (the spec explains why this is critical for cost), beta headers for interleaved thinking and 1M context.
- **OpenAI** — Responses API (`/v1/responses`). Must support: reasoning tokens, the `apply_patch` tool format (v4a), server-side conversation state.
- **Gemini** — Gemini API (`/v1beta/models/*/generateContent`). Must support: grounding, system instructions, thinking config.

**Critical:** Each adapter must use the provider's NATIVE API, not a compatibility shim. See unified-llm-spec Section 2.7 for why.

**Prompt caching is not optional.** For agentic workloads, caching reduces input token costs by 50-90%. Anthropic requires explicit `cache_control` annotations — the adapter must inject these automatically. OpenAI and Gemini cache automatically. See unified-llm-spec Section 2.10.

**Environment variables:** `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`. Only providers with keys present are registered. The first registered provider is the default.

**Test with real API calls.** Verify that a simple completion works with each provider. Verify streaming. Verify tool calling round-trips.

### Phase 3: Core Loop (Without Recall and Learn)

Implement the loop: Perceive → Plan → Act → Verify. Skip Recall for now (just pass all available agents to Plan). Skip Learn (just log stumbles, don't act on them).

**Key pieces:**
- `perceive()` — collect inputs from queue
- `plan()` — build LLM request with agent-as-tool mapping, call LLM, parse response
- `act()` — spawn a subagent with goal + hints, run its loop, return result
- `verify()` — check objective signals, detect stumbles

**Agent-as-tool mapping:** Each agent in the genome becomes a tool definition for the LLM. The tool has two parameters: `goal` (required string) and `hints` (optional string array). When the LLM calls one of these tools, the loop interprets it as a delegation to that agent. See spec Section 6.3.

**Subagent spawning:** A subagent gets its own agent instance with independent history, running the same loop. It shares the parent's execution environment (same filesystem). Depth is tracked and enforced via `AgentConstraints.max_depth`.

**Natural completion:** When Plan produces text with no tool calls, the agent's task is complete.

**Test this phase with real API calls.** Create a bootstrap genome (Phase 5 agents), submit "Create a file hello.py that prints Hello World", verify the agent decomposes the task, delegates to code-editor, and the file gets created.

### Phase 4: Genome Storage and Recall

Implement the genome: agent specs, memories, routing rules. Implement Recall as a search over the genome.

**Storage:**
- Agent specs as YAML files in a directory, git-versioned
- Memories as JSONL (append-only)
- Routing rules as YAML
- Git auto-versioning: `git init` on first genome creation, `git commit` on every mutation

**Recall (start simple):**
- If genome has < 20 agents, return all of them
- Search memories by keyword matching (upgrade to embeddings later)
- Match routing rules by keyword matching
- Inject results into Plan's context (agents as tools, memories as system message, routing hints as system message)

**The cache-busting technique for loading agent specs:** If agent specs contain executable code (e.g., custom verification functions), use the temp-copy-import pattern: copy to `_tmp_{name}_{timestamp}.ts`, import, delete. See spec Appendix D.13. For Phase 4, agent specs are YAML data only, so this may not be needed yet — but design the loading interface to support it later.

### Phase 5: Bootstrap Agents

Create the 4 bootstrap agent specs as YAML files in `bootstrap/`:

```yaml
# bootstrap/root.yaml
name: root
description: "Decompose coding tasks into subgoals and delegate to specialist agents"
model: best
capabilities: [code-reader, code-editor, command-runner]
constraints:
  max_turns: 200
  max_depth: 3
  can_learn: true
tags: [core, orchestration]
system_prompt: |
  You are a coding agent that decomposes tasks and delegates to specialists.

  You NEVER read files, edit code, or run commands directly.
  You think at the level of goals: understand, find, edit, test, verify.
  You delegate each goal to the appropriate specialist.

  When you receive a task:
  1. Break it into clear subgoals
  2. Delegate each subgoal to the right agent
  3. Verify the results
  4. Report completion or iterate if something failed

  Available specialists will be presented as tools. Each takes a "goal"
  (what you want achieved) and optional "hints" (context that might help).
```

Write similar specs for `code-reader`, `code-editor`, and `command-runner`. Their system prompts should be focused and specific to their domain. See spec Section 11.2 for the minimal definitions and Section 3.4 for the code-reader example.

**Test:** Fresh genome with only bootstrap agents. Submit a multi-step task. Verify the root decomposes it and delegates correctly.

### Phase 6: Learn (Async)

The hardest phase. Implement the asynchronous learning process.

**LearnSignal queue:** Verify pushes stumble signals to a queue. Learn processes the queue in the background.

**Trigger filtering:** Not every signal triggers learning. See spec Section 8.3. Start with: always learn from failures, learn from repeated errors (3+ occurrences), skip one-off errors.

**Improvement actions:**
1. Create a memory (easiest — natural language fact stored in genome)
2. Update an agent's system prompt (medium — modify YAML, git commit)
3. Create a new agent (hardest — generate a full AgentSpec)
4. Create a routing rule (medium — add to routing YAML)

**Learn is itself an agent.** It receives LearnSignals as input and uses the best available LLM to reason about what improvement to make. Its Act phase writes to the genome.

**Evaluation:** Track stumble rates per-agent. After an improvement, compare the rate before and after. If the rate increased, the improvement is a candidate for rollback (`git revert`).

**Test:** Force a repeating error pattern (e.g., always try `pytest` in a vitest project). Verify Learn creates a memory or updates the command-runner agent. Verify the next invocation doesn't repeat the error.

### Phase 7: Event System and Host Interface

Implement the event system (spec Appendix A) and the host application interface.

**Events:** Every phase emits typed events. The host application (CLI, future IDE integration) consumes them for display.

**Host interface:**
```typescript
const agent = await createAgent({ genomePath: '~/.local/share/sprout-genome' });
const events = agent.submit("Fix the failing login test");
for await (const event of events) {
  // render to terminal, log, etc.
}
```

**CLI (minimal):**
```bash
sprout "Fix the failing login test"           # run a task
sprout --genome list                          # list agents in genome
sprout --genome log                           # git log of genome changes
sprout --genome rollback <commit>             # revert a genome change
```

### Phase 8: Integration Testing

Run the integration test from spec Section 14.9:
1. Fresh genome, simple file creation
2. Multi-step task requiring decomposition
3. Stumble-and-learn: repeated error triggers improvement
4. Genome growth: verify new agents/memories were created
5. Cross-session: new session with same genome uses learned improvements

## Principles

- **TDD.** Write tests before implementation. Every phase has tests that pass before the next phase starts.
- **Minimal dependencies.** Don't add a library for something you can do in 20 lines.
- **Read the spec appendices.** Appendix D has implementation pitfalls that will save you hours of debugging. The `exec` PATH issue, the cache-busting technique, the memory confidence model — these are all learned from real experience.
- **Build the LLM client properly.** The unified LLM spec is sprout's foundation — don't cut corners on it. All three providers should work before building the agent loop on top. Prompt caching especially — it's the difference between sprout being affordable to run and being absurdly expensive.
- **Commit frequently.** This is a complex build. Git history is your safety net.
- **When in doubt, simpler.** The spec describes the full vision. The first implementation should be the simplest thing that works for each piece. Learn can always be made smarter later. Recall can always be upgraded from keyword search to embeddings. Start dumb, prove the loop works, then improve.

## What Success Looks Like

When you're done, this should work:

```bash
# First session — fresh genome
sprout "Create a Python CLI tool that takes a name argument and prints a greeting. Include tests."
# Agent decomposes: create file, write tests, run tests
# Root delegates to code-editor, then command-runner for tests
# If tests fail, iterates

# Second session — same genome
sprout "Add a --uppercase flag to the greeting tool"
# Agent recalls memories from first session (project structure, test framework)
# Fewer stumbles because it knows the codebase

# After many sessions — genome has grown
sprout --genome list
# Shows: code-reader, code-editor, command-runner, test-runner-pytest, ...
# Agents that Learn created from experience
```

## Review Process

Your work will be reviewed by a senior agent who designed this spec. They'll be checking:
1. Does the loop actually work end-to-end?
2. Are the phases cleanly separated (Recall retrieves, Plan decides, they're not mixed)?
3. Are subagents genuinely goal-directed (not receiving instructions)?
4. Does Learn run async and actually produce useful genome mutations?
5. Is the immutable kernel actually immutable (no way for Learn to modify the loop or primitives)?
6. Are tests comprehensive and testing real behavior, not mocked behavior?
