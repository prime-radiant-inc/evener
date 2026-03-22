# Self-Improving Coding Agent Specification

This document specifies a coding agent that gets better over time by observing its own behavior, creating new capabilities, and evolving its approach through experience. It layers on top of the [Unified LLM Client Specification](./unified-llm-spec.md) for multi-provider LLM access.

This spec replaces the [Coding Agent Loop Specification](./coding-agent-loop-spec.md) with a fundamentally different architecture. Where that spec defines a fixed loop with static tools, this one defines a recursive agent architecture with a learning process that continuously modifies the agent's capabilities.

---

## Table of Contents

1. [Overview and Goals](#1-overview-and-goals)
2. [Core Loop](#2-core-loop)
3. [Agents and Primitives](#3-agents-and-primitives)
4. [The Genome](#4-the-genome)
5. [Recall](#5-recall)
6. [Plan](#6-plan)
7. [Verify](#7-verify)
8. [Learn](#8-learn)
9. [Primitives](#9-primitives)
10. [Multi-Provider Support](#10-multi-provider-support)
11. [Bootstrap](#11-bootstrap)
12. [The Immutable Kernel](#12-the-immutable-kernel)
13. [Implementation Language](#13-implementation-language)
14. [Definition of Done](#14-definition-of-done)

**Appendices:**
- [A: Event System](#appendix-a-event-system)
- [B: Fitness Function — Stumble Rate](#appendix-b-fitness-function--stumble-rate)
- [C: Relationship to Benchmark Evaluation](#appendix-c-relationship-to-benchmark-evaluation)
- [D: Design Rationale](#appendix-d-design-rationale) — **Read this.** Contains the "why" behind every decision, open questions, anti-patterns, and implementation pitfalls.

---

## 1. Overview and Goals

### 1.1 Problem Statement

Coding agents today are static. They ship with a fixed set of tools, a fixed system prompt, and fixed behaviors. When an agent fails — retries a command 4 times, reads 6 files to find a 3-line function, runs the wrong test framework — it learns nothing. The next session makes the same mistakes. The 1,000th session is no better than the 1st.

This is not a fundamental limitation. The agent can observe its own behavior (every action is logged). It can identify patterns in its failures (errors, retries, inefficient sequences). It can build new capabilities to address those failures (write code, save tools). It can evaluate whether those capabilities helped (measure stumble rate). The entire self-improvement loop is within the agent's existing abilities. What's missing is the architecture that closes the loop.

### 1.2 What This Is

A coding agent with five properties:

1. **It observes its own performance.** Every action, outcome, error, and timing is recorded.
2. **It identifies its own weaknesses.** Errors, retries, and high-friction sequences are detected.
3. **It builds capabilities to address them.** New agents, memories, and routing rules are created autonomously.
4. **It persists improvements across sessions.** The genome survives session boundaries.
5. **It evaluates whether improvements helped.** Stumble rate is the fitness function.

### 1.3 Design Principles

**Ultra-minimalist kernel.** The core is the loop and the primitives. Everything else — tools, agents, skills, routing — is genome that the agent creates and evolves. Ship almost nothing. Let the agent build what it needs.

**Everything mutable.** The genome is fully modifiable by the learning process. New agents can be created, existing agents can be modified, unused agents can be pruned. The only immutable parts are the kernel (the loop itself) and safety constraints.

**Recursive agents.** There is no distinction between "tools" and "agents." Every action is a goal-directed delegation to a subagent. Subagents can spawn their own subagents. Primitives (raw syscalls) are the base case.

**Goal-directed, not instruction-directed.** Subagents receive goals ("understand how auth works") not instructions ("read src/auth.ts lines 140-200"). The subagent owns the how. The parent specifies only the what.

**Stumbles are the fitness function.** Not "did the user like it" but "did I stumble getting there?" Errors, retries, wasted tool calls — these are objective, self-observable, and don't require user feedback.

### 1.4 Relationship to Other Specs

This spec uses the **Unified LLM Client** for all LLM communication. It references the same types: `Client`, `Request`, `Response`, `Message`, `ContentPart`, `ToolCall`, `ToolResult`, `Usage`, `StreamEvent`.

This spec replaces the **Coding Agent Loop** with a different architecture. The concepts of provider profiles, execution environments, tool output truncation, and system prompts still apply but are restructured around the recursive agent model.

### 1.5 Prior Art

The following systems solve related problems:

- **Voyager** (NVIDIA, 2023) — Minecraft agent that builds a growing skill library of executable code from experience. Closest conceptual ancestor for the persistent capability growth pattern.
- **DGM / Darwin Godel Machine** (Sakana AI, 2025) — Coding agent that rewrites its own Python codebase through evolutionary self-modification. Demonstrated 20% → 50% on SWE-bench.
- **SICA / Self-Improving Coding Agent** (Bristol, 2025) — Agent that edits its own implementation to improve coding performance. Same agent evaluates, identifies shortcomings, and updates itself.
- **Live-SWE-Agent** (2025) — Starts with minimal tools and invents new ones during execution. Most radical runtime self-modification approach. 77.4% SWE-bench Verified.
- **DSPy** (Stanford) — Metric-driven prompt optimization. The most principled approach to automatic prompt improvement.
- **ADAS** (Clune et al., 2024) — Meta-agent that programs new agent designs in code. Demonstrates that code-represented agents can be automatically improved through search.

None of these combine production coding agent + real-world performance observation + autonomous capability creation + persistent cross-session improvement + closed-loop evaluation. That is the contribution of this spec.

---

## 2. Core Loop

### 2.1 The Loop

```
Perceive → Recall → Plan → Act → Verify ─── loop
                                   │
                                   └──→ Learn (async)
```

Five synchronous phases execute in sequence. One asynchronous process forks from Verify and runs in the background.

### 2.2 Perceive

Receive input and observe the current state.

```
FUNCTION perceive(agent) -> Perception:
    -- Collect all pending inputs
    inputs = agent.input_queue.drain()

    RETURN Perception(
        inputs    = inputs,          -- user messages, tool results, subagent results
        env_state = agent.env.snapshot(),  -- working directory, git status, etc.
        timestamp = now()
    )
```

Perceive is passive. It collects what happened since the last cycle. It does not interpret or evaluate — that is Recall's and Plan's job.

**Inputs include:**
- User messages (natural language instructions)
- Results from previous Act (subagent completions)
- Steering messages injected by the host application
- System events (timeouts, errors, signals)

### 2.3 Recall

Search the genome for relevant context. This is a retrieval operation, not a decision.

```
FUNCTION recall(agent, perception) -> RecallResult:
    -- Search for relevant agents
    candidate_agents = agent.genome.search_agents(
        context = perception,
        limit   = 10
    )

    -- Search for relevant memories
    memories = agent.genome.search_memories(
        context = perception,
        limit   = 5
    )

    -- Retrieve any routing rules that match
    routing_hints = agent.genome.match_routing_rules(perception)

    RETURN RecallResult(
        agents        = candidate_agents,
        memories      = memories,
        routing_hints = routing_hints
    )
```

Recall is deterministic and cheap. It does not make decisions about which agent to use — it surfaces candidates for Plan to choose from.

**At small scale** (< 20 agents in the genome): return all agents. Plan sees everything and picks.

**At medium scale** (20-200 agents): use embedding similarity to narrow candidates. Embed the perception, compare against agent description embeddings, return top-k.

**At large scale** (200+ agents): layered retrieval. Category filtering, then embedding similarity, then re-ranking. The retrieval strategy itself is part of the genome and can be improved by Learn.

### 2.4 Plan

The LLM call. Takes perceived state + recalled context, produces a decision.

```
FUNCTION plan(agent, perception, recall_result) -> Plan:
    messages = build_messages(
        system_prompt  = agent.system_prompt,
        perception     = perception,
        recall_result  = recall_result,
        history        = agent.history
    )

    response = agent.llm_client.complete(Request(
        model    = agent.model,
        messages = messages,
        tools    = build_act_tools(recall_result.agents)
    ))

    RETURN parse_plan(response)
```

Plan is the only phase that calls the LLM. It is the most expensive phase and the only stochastic one.

**The LLM sees:**
- The agent's system prompt (from its genome spec)
- The current perception (what just happened)
- Recalled agents (as tool definitions — each agent appears as a callable tool)
- Recalled memories (injected as context)
- Routing hints (as guidance, not constraints)
- Conversation history

**The LLM produces:**
- Zero or more Act delegations (goals for subagents)
- Optional text output (for the user or parent agent)
- Optional reasoning (thinking/chain-of-thought)

When the LLM produces text with no Act delegations, the agent's task is complete. This is the natural termination condition.

### 2.5 Act

Delegate a goal to a subagent.

```
FUNCTION act(agent, delegation) -> ActResult:
    -- Resolve which agent to use (Plan already selected via tool call)
    subagent_spec = agent.genome.get_agent(delegation.agent_name)

    -- Create the subagent
    subagent = spawn_agent(
        spec  = subagent_spec,
        goal  = delegation.goal,
        hints = delegation.hints,
        env   = agent.env           -- shared execution environment
    )

    -- Run the subagent's own loop
    result = subagent.run()

    RETURN ActResult(
        goal      = delegation.goal,
        output    = result.output,
        success   = result.success,
        stumbles  = result.stumble_count,
        turns     = result.turns_used
    )
```

**Act is always goal-directed delegation.** The parent specifies:
- `goal` — what the subagent should achieve (natural language)
- `hints` — optional context that might help (files seen, errors encountered, prior attempts)

The parent does NOT specify how the subagent should work. The subagent runs its own Perceive → Recall → Plan → Act → Verify loop.

**Multiple Acts can run concurrently.** If Plan produces multiple delegations, they can execute in parallel when they are independent.

**Act delegation interface:**
```
RECORD Delegation:
    agent_name  : String            -- which agent to use (selected by Plan)
    goal        : String            -- what to achieve
    hints       : List<String>      -- optional context
```

### 2.6 Verify

Assess whether the Act achieved its goal.

```
FUNCTION verify(agent, act_result) -> VerifyResult:
    -- Check objective signals
    stumbled = act_result.stumbles > 0
               OR NOT act_result.success

    -- Record the outcome
    agent.history.append(Outcome(
        goal      = act_result.goal,
        success   = act_result.success,
        stumbles  = act_result.stumbles,
        output    = act_result.output
    ))

    -- Signal Learn if there is something to learn from
    IF stumbled:
        agent.learn_queue.push(LearnSignal(
            kind     = "stumble",
            goal     = act_result.goal,
            agent    = act_result.agent_name,
            details  = act_result
        ))

    RETURN VerifyResult(
        success  = act_result.success,
        stumbled = stumbled,
        output   = act_result.output
    )
```

Verify checks objective signals, not subjective quality. Did the subagent succeed? Did it stumble along the way? A subagent can succeed (achieve the goal) while still stumbling (errors, retries, inefficient paths) — both signals matter.

Verify feeds Learn. When a stumble is detected, a signal is pushed to the learn queue. Learn processes these signals asynchronously.

### 2.7 The Full Cycle

```
FUNCTION run(agent):
    LOOP:
        perception   = perceive(agent)
        recall_result = recall(agent, perception)
        plan_result   = plan(agent, perception, recall_result)

        -- Natural completion: Plan produced text, no delegations
        IF plan_result.delegations IS EMPTY:
            agent.emit(COMPLETE, text = plan_result.text)
            BREAK

        -- Execute delegations (parallel if independent)
        act_results = execute_delegations(agent, plan_result.delegations)

        -- Verify each result
        FOR EACH result IN act_results:
            verify(agent, result)

    END LOOP
```

---

## 3. Agents and Primitives

### 3.1 The Agent Abstraction

An agent is a self-contained unit that receives a goal and produces a result. Every agent runs the same core loop (Perceive → Recall → Plan → Act → Verify). Agents differ in:

- **System prompt** — what the agent knows about itself and its purpose
- **Model** — which LLM powers the agent's Plan phase (can vary by complexity)
- **Capabilities** — which other agents and primitives it can delegate to
- **Constraints** — limits on turns, depth, tool access

```
RECORD AgentSpec:
    name          : String              -- unique identifier
    description   : String              -- what this agent does (used by Recall)
    system_prompt : String              -- the agent's identity and instructions
    model         : String              -- LLM model to use for Plan
    capabilities  : List<String>        -- names of agents/primitives this agent can use
    constraints   : AgentConstraints
    tags          : List<String>        -- for Recall search/filtering
    version       : Integer             -- auto-incremented on modification

RECORD AgentConstraints:
    max_turns       : Integer = 50      -- maximum loop iterations
    max_depth       : Integer = 3       -- how deep subagent spawning can go
    timeout_ms      : Integer = 300000  -- 5 minute default
    can_spawn       : Boolean = true    -- whether this agent can spawn subagents
    can_learn       : Boolean = false   -- whether this agent contributes to Learn
```

### 3.2 Primitives

Primitives are atomic operations that do not involve LLM reasoning. They are the base case of the recursive agent model. A primitive executes directly against the execution environment.

```
INTERFACE Primitive:
    name        : String
    description : String
    parameters  : JSONSchema

    FUNCTION execute(args, env: ExecutionEnvironment) -> PrimitiveResult

RECORD PrimitiveResult:
    output    : String
    success   : Boolean
    error     : String | None
```

An agent's Act phase can delegate to either another agent (goal-directed, runs the full loop) or a primitive (instruction-directed, executes atomically). The distinction is made by Plan based on task complexity.

### 3.3 Required Primitives

These primitives must exist in the kernel. They cannot be removed or replaced by the learning process.

#### read_file

Read bytes from the filesystem.

```
PRIMITIVE read_file:
    parameters:
        path    : String (required)
        offset  : Integer (optional)    -- 1-based line number
        limit   : Integer (optional)    -- max lines to read
    returns: Line-numbered text content
    errors: File not found, permission denied
```

#### write_file

Write bytes to the filesystem.

```
PRIMITIVE write_file:
    parameters:
        path    : String (required)
        content : String (required)
    returns: Confirmation with bytes written
    errors: Permission denied, disk full
```

#### edit_file (Anthropic/Gemini)

Replace an exact string in a file. This is the native editing format for Claude and Gemini models.

```
PRIMITIVE edit_file:
    parameters:
        path        : String (required)
        old_string  : String (required)
        new_string  : String (required)
        replace_all : Boolean (optional, default false)
    returns: Confirmation with replacement count
    errors: File not found, string not found, ambiguous match
```

#### apply_patch (OpenAI)

Apply code changes using the v4a patch format. This is the native editing format for GPT models. Supports creating, deleting, updating, and renaming files in a single operation. See the Coding Agent Loop Specification Appendix A for the full v4a format grammar.

```
PRIMITIVE apply_patch:
    parameters:
        patch   : String (required)     -- patch content in v4a format
    returns: List of affected file paths and operations performed
    errors: Parse error, file not found, verification failure
```

The agent runtime selects `edit_file` or `apply_patch` based on the agent's model provider. Both achieve "edit a file" but use the format the model was trained on.

#### exec

Execute a process.

```
PRIMITIVE exec:
    parameters:
        command     : String (required)
        timeout_ms  : Integer (optional)
    returns: stdout, stderr, exit_code, duration_ms
    errors: Timeout, permission denied, command not found
```

Process execution details (process groups, SIGTERM/SIGKILL, environment variable filtering) follow the Coding Agent Loop Specification Section 4.

#### grep

Search file contents.

```
PRIMITIVE grep:
    parameters:
        pattern     : String (required)
        path        : String (optional)
        glob_filter : String (optional)
        max_results : Integer (optional, default 100)
    returns: Matching lines with file paths and line numbers
    errors: Invalid pattern, path not found
```

#### glob

Find files by name pattern.

```
PRIMITIVE glob:
    parameters:
        pattern : String (required)
        path    : String (optional)
    returns: Matching file paths sorted by modification time
    errors: Invalid pattern
```

#### fetch

Make an HTTP request.

```
PRIMITIVE fetch:
    parameters:
        url     : String (required)
        method  : String (optional, default "GET")
        headers : Map<String, String> (optional)
        body    : String (optional)
    returns: status, headers, body
    errors: Network error, timeout
```

### 3.4 Agents as "Smart Primitives"

The simplest possible agent is a thin wrapper around a primitive — what traditional architectures call a "tool." For example, a `code-reader` agent might start as:

```
AgentSpec(
    name          = "code-reader",
    description   = "Find and return relevant code from a file or codebase",
    system_prompt = "You help find specific code. Use grep to locate, read_file to retrieve.
                     Return only the relevant sections, not entire files.",
    model         = "fast",
    capabilities  = ["read_file", "grep", "glob"]
)
```

This agent receives a goal ("find the authentication middleware"), uses its primitives to locate and read the relevant code, and returns the result. It is an agent — it has its own loop, makes its own decisions — but its scope is narrow and it typically completes in 2-5 turns.

Over time, Learn might improve this agent's system prompt, add new capabilities, or split it into specialized variants (e.g., `code-reader-python` vs `code-reader-typescript`).

### 3.5 Agent Hierarchy

Agents form a tree at runtime. The root agent receives the user's task. It delegates to subagents, which delegate to their own subagents, down to primitives.

```
root-agent: "Fix the failing login test"
├── code-reader: "Find the login test and understand what it expects"
│   ├── grep("login.*test", "**/*.test.*")
│   ├── read_file("src/__tests__/login.test.ts", offset=45, limit=30)
│   └── read_file("src/auth/login.ts", offset=1, limit=50)
├── test-runner: "Run the login test and report failures"
│   ├── exec("npm test -- --filter login")
│   └── (parses output, extracts failure details)
├── code-editor: "Fix the null check on line 23 of login.ts"
│   └── edit_file("src/auth/login.ts", old_string=..., new_string=...)
└── test-runner: "Verify the fix by re-running the login test"
    └── exec("npm test -- --filter login")
```

The root agent never touches a file or runs a command directly. It thinks at the level of goals: understand, test, fix, verify. Each subagent handles the how.

---

## 4. The Genome

### 4.1 What the Genome Contains

The genome is the agent's complete mutable state — everything that can be modified by the learning process.

```
RECORD Genome:
    agents          : Map<String, AgentSpec>        -- agent specifications
    memories        : MemoryStore                   -- learned facts and patterns
    routing_rules   : List<RoutingRule>             -- when to prefer certain agents
    agent_embeddings: EmbeddingIndex                -- for Recall search
```

### 4.2 Agent Specifications

Each agent in the genome is a complete specification for how to handle a class of goals. See Section 3.1 for the `AgentSpec` record.

Agent specs are the primary unit of self-improvement. When Learn creates a new capability, it creates a new AgentSpec. When Learn improves an existing capability, it modifies an AgentSpec (system prompt, model choice, capabilities list).

### 4.3 Memories

Memories are facts and patterns the agent has learned from experience. They are not agent specs — they are context that gets injected into Plan via Recall.

```
RECORD Memory:
    id          : String
    content     : String            -- the memory itself (natural language)
    tags        : List<String>      -- for search
    source      : String            -- what created this memory (session ID, learn signal)
    created     : Timestamp
    last_used   : Timestamp
    use_count   : Integer
    confidence  : Float             -- 0.0 to 1.0, decays if never used
```

Examples of memories:
- "This project uses pytest, not vitest"
- "The auth module is at src/core/auth/, not src/auth/"
- "Running `npm test` in this repo requires `NODE_ENV=test`"
- "When edit_file fails with 'ambiguous match', adding one more line of context usually fixes it"

Memories decay. If a memory is never recalled and used, its confidence drops. Below a threshold, it is pruned. This prevents memory bloat and removes stale information.

### 4.4 Routing Rules

Routing rules are learned preferences for agent selection. They are stronger than embedding similarity but weaker than explicit instructions.

```
RECORD RoutingRule:
    id          : String
    condition   : String            -- natural language condition
    preference  : String            -- which agent to prefer
    strength    : Float             -- 0.0 to 1.0
    source      : String            -- what created this rule
```

Example: "When the goal involves running tests in a JavaScript project, prefer `test-runner-js` over generic `test-runner`."

Routing rules are surfaced by Recall and presented to Plan as hints, not constraints. Plan can override them.

### 4.5 Storage and Versioning

The genome is stored on the filesystem and versioned with git.

```
~/.local/share/agent-genome/
├── .git/                       -- every mutation is a commit
├── agents/
│   ├── code-reader.yaml
│   ├── test-runner.yaml
│   ├── code-editor.yaml
│   └── ...
├── memories/
│   └── memories.jsonl          -- append-only log, periodic compaction
├── routing/
│   └── rules.yaml
├── embeddings/
│   └── agents.index            -- rebuilt on agent changes
└── metrics/
    └── stumbles.jsonl          -- raw stumble data for Learn
```

**Every mutation is a git commit.** Agent created, agent modified, memory added, routing rule changed — each produces a commit with a descriptive message. This provides:
- Full history of how the genome evolved
- Rollback capability for any change
- Diffing to understand what changed between versions
- The ability to bisect when an improvement turns out to be a regression

### 4.6 Genome Initialization

On first run, the genome contains only:
- A small set of **bootstrap agents** (Section 11)
- No memories
- No routing rules

Everything else is built by Learn from experience.

---

## 5. Recall

### 5.1 Purpose

Recall searches the genome for context relevant to the current perception. It runs before Plan and injects its results into the LLM's input. This is how accumulated improvements influence current behavior — not through static prompt instructions, but through dynamic retrieval.

### 5.2 What Gets Retrieved

```
RECORD RecallResult:
    agents        : List<AgentSpec>     -- candidate agents for Act
    memories      : List<Memory>        -- relevant facts and patterns
    routing_hints : List<RoutingRule>   -- agent selection preferences
```

### 5.3 Retrieval Strategy

Recall's internal strategy depends on genome size and can itself be improved by Learn.

**Default strategy:**

```
FUNCTION recall(genome, perception) -> RecallResult:
    -- 1. Always include primitives
    agents = list_primitives()

    -- 2. Search agents by relevance
    IF genome.agent_count() < 20:
        agents += genome.all_agents()
    ELSE:
        query_embedding = embed(perception.summary())
        agents += genome.search_agents_by_embedding(query_embedding, limit = 10)

    -- 3. Search memories
    memories = genome.search_memories(
        query = perception.summary(),
        limit = 5,
        min_confidence = 0.3
    )

    -- 4. Match routing rules
    routing_hints = genome.match_routing_rules(perception)

    RETURN RecallResult(agents, memories, routing_hints)
```

### 5.4 Injection into Plan

Recall results are injected into the LLM's context as structured blocks:

- **Agents** become tool definitions in the LLM request's `tools` array. Each agent appears as a tool with `name`, `description`, and a parameter schema containing `goal` (required, string) and `hints` (optional, list of strings).
- **Memories** are injected as a system message section: `<memories>...</memories>`.
- **Routing hints** are injected as a system message section: `<routing_hints>...</routing_hints>`.

---

## 6. Plan

### 6.1 Purpose

Plan is the LLM call. It takes perceived state + recalled context and produces a decision: what to delegate, to whom, or whether the task is complete.

### 6.2 Input Construction

```
FUNCTION build_plan_input(agent, perception, recall_result) -> Request:
    system = agent.spec.system_prompt
           + render_environment_context(agent.env)
           + render_memories(recall_result.memories)
           + render_routing_hints(recall_result.routing_hints)

    messages = agent.history.as_messages()
             + [perception.as_message()]

    tools = []
    FOR EACH a IN recall_result.agents:
        tools.APPEND(agent_as_tool(a))

    RETURN Request(
        model    = agent.spec.model,
        messages = [Message.system(system)] + messages,
        tools    = tools
    )
```

### 6.3 Agent-as-Tool Mapping

Each recalled agent becomes a tool definition for the LLM:

```
FUNCTION agent_as_tool(spec: AgentSpec) -> ToolDefinition:
    RETURN ToolDefinition(
        name        = spec.name,
        description = spec.description,
        parameters  = {
            "type": "object",
            "properties": {
                "goal": {
                    "type": "string",
                    "description": "What you want this agent to achieve"
                },
                "hints": {
                    "type": "array",
                    "items": { "type": "string" },
                    "description": "Optional context that might help"
                }
            },
            "required": ["goal"]
        }
    )
```

When the LLM calls one of these tools, the loop interprets it as an Act delegation.

### 6.4 Model Selection

Different agents use different models based on their complexity:

| Agent type | Model class | Rationale |
|---|---|---|
| Root agent | Best available (Opus-class) | Needs deep reasoning for decomposition |
| Mid-level agents | Good + fast (Sonnet-class) | Balance of capability and cost |
| Simple agents | Fast + cheap (Haiku/Flash-class) | Narrow scope, speed matters |
| Learn process | Best available | Needs to reason about improvements |

Model selection is part of the AgentSpec and can be modified by Learn. An agent that consistently stumbles might be upgraded to a more capable model. An agent that handles simple tasks might be downgraded for efficiency.

### 6.5 Provider Alignment

The agent's system prompt and tool definitions should match the conventions of the LLM provider serving the model. Anthropic models should see `edit_file` (old_string/new_string). OpenAI models should see `apply_patch` (v4a format). This principle from the Coding Agent Loop Specification still applies.

When the root agent uses one provider and subagents use another, each agent's primitives are formatted for its own provider. The genome can contain provider-specific variants of the same agent.

---

## 7. Verify

### 7.1 Purpose

Verify assesses whether an Act achieved its goal. It checks objective signals and feeds Learn.

### 7.2 What Verify Checks

**Objective signals (always available):**
- Did the subagent succeed or fail?
- How many stumbles occurred? (errors, retries, timeouts)
- How many turns did it take? (efficiency)
- How long did it take? (wall-clock time)

**Goal-satisfaction signals (when assessable):**
- Did the subagent's output address the goal?
- Is the output complete or partial?

Goal satisfaction is harder to assess objectively. For simple goals ("read file X"), success/failure is clear. For complex goals ("fix the bug"), assessment may require running tests or reviewing output. Verify can delegate goal-assessment to a lightweight LLM call when the goal is complex and the objective signals are ambiguous.

### 7.3 Stumble Detection

A stumble is any deviation from the ideal path. Stumbles include:

| Stumble type | Signal | Example |
|---|---|---|
| Error | Subagent or primitive returned an error | File not found, command failed |
| Retry | Same action attempted multiple times | Reading a file 3 times with different offsets |
| Inefficiency | Excessive turns for a simple goal | 8 turns to read one function from a file |
| Timeout | Subagent hit its time limit | Test suite ran longer than expected |
| Failure | Subagent could not achieve its goal | Bug fix attempt that didn't fix the bug |

### 7.4 Learn Signal

When Verify detects a stumble, it pushes a signal to the learn queue:

```
RECORD LearnSignal:
    kind        : String            -- "error", "retry", "inefficiency", "timeout", "failure"
    goal        : String            -- what was being attempted
    agent_name  : String            -- which agent stumbled
    details     : ActResult         -- full context of the stumble
    session_id  : String
    timestamp   : Timestamp
```

---

## 8. Learn

### 8.1 Purpose

Learn is the asynchronous process that improves the genome based on observed stumbles. It runs in the background, does not block the main loop, and produces genome mutations that become available to future Recall steps.

### 8.2 Architecture

Learn is itself an agent. It receives LearnSignals, decides whether to act, and when it does, modifies the genome.

```
Learn Agent
├── Perceive: receive LearnSignals from the queue
├── Recall: search genome for similar past stumbles and existing agents
├── Plan: decide what improvement to make (or skip)
├── Act: modify the genome (create/update agent, add memory, add routing rule)
└── Verify: (deferred — improvement is evaluated on next occurrence)
```

### 8.3 Trigger Filtering

Not every stumble warrants a learning response. Learn applies a filter:

```
FUNCTION should_learn(signal: LearnSignal, genome: Genome) -> Boolean:
    -- Always learn from repeated stumbles
    IF genome.metrics.stumble_count(signal.agent_name, signal.kind) >= 3:
        RETURN true

    -- Always learn from failures (goal not achieved)
    IF signal.kind == "failure":
        RETURN true

    -- Skip one-off errors (might be environmental)
    IF signal.kind == "error" AND genome.metrics.stumble_count(...) < 2:
        RETURN false

    -- Skip if a recent improvement already addresses this
    IF genome.recent_improvements_address(signal):
        RETURN false

    RETURN true
```

The threshold for learning increases with genome maturity. Early on, learn aggressively (the genome is empty, everything is useful). Later, learn selectively (the genome is rich, avoid bloat).

### 8.4 Improvement Actions

Learn can produce four types of genome mutations:

**1. Create a new agent.** When no existing agent handles a recurring goal pattern well.

```
Example: The agent keeps stumbling when running tests because it doesn't know
the project's test framework. Learn creates:

AgentSpec(
    name = "test-runner-jest",
    description = "Run Jest tests, parse output, report failures",
    system_prompt = "You are a test runner specialized in Jest. ...",
    capabilities = ["exec", "read_file", "grep"]
)
```

**2. Update an existing agent.** When an agent works but could be better.

```
Example: The code-reader agent consistently takes too many turns.
Learn updates its system prompt: "When searching for a function, use grep
first to locate it before reading the entire file."
```

**3. Create a memory.** When a fact would prevent a recurring stumble.

```
Example: The agent keeps running `pytest` but this project uses `vitest`.
Learn creates: Memory("This project uses vitest, not pytest. Run: npx vitest run")
```

**4. Create a routing rule.** When the wrong agent keeps being selected.

```
Example: The generic test-runner keeps being selected for Go projects
when test-runner-go exists and is better.
Learn creates: RoutingRule(
    condition = "Go project testing",
    preference = "test-runner-go",
    strength = 0.8
)
```

### 8.5 Learn Frequency

Learn operates at multiple frequencies:

| Trigger | Frequency | Scope |
|---|---|---|
| Stumble signal | Per-stumble (filtered) | Single action improvement |
| End of task | Per-task | Pattern across multiple actions |
| End of session | Per-session | Broader patterns, agent lifecycle |
| Periodic review | Configurable (e.g., every 10 sessions) | Genome health, pruning, consolidation |

Higher-frequency triggers produce smaller improvements (memories, prompt tweaks). Lower-frequency triggers produce larger improvements (new agents, agent restructuring).

### 8.6 Evaluating Improvements

How does Learn know if an improvement helped? By measuring the stumble rate for the same goal pattern before and after the improvement.

```
FUNCTION evaluate_improvement(genome, improvement_id) -> EvaluationResult:
    before = genome.metrics.stumble_rate(
        agent   = improvement.agent_name,
        period  = "before_improvement"
    )
    after = genome.metrics.stumble_rate(
        agent   = improvement.agent_name,
        period  = "after_improvement"
    )

    IF after < before:
        RETURN EvaluationResult(verdict = "helpful", delta = before - after)
    ELSE IF after > before:
        RETURN EvaluationResult(verdict = "harmful", delta = after - before)
    ELSE:
        RETURN EvaluationResult(verdict = "neutral", delta = 0)
```

Improvements that increase the stumble rate are candidates for rollback. The genome's git history makes rollback a `git revert`.

### 8.7 Genome Pruning

Learn periodically prunes the genome to prevent bloat:

- **Agents** with zero usage for N sessions are candidates for removal
- **Memories** with confidence below threshold are removed
- **Routing rules** that are never triggered are removed
- **Agents** that are strict subsets of other agents are candidates for merging

The simplicity pressure is explicit: fewer, more general agents are preferred over many specialized ones. Learn should consider "can I merge these 3 similar agents into 1?" before creating a 4th variant.

---

## 9. Primitives

See Section 3.3 for primitive definitions. Primitives are the immutable base layer — the kernel provides them, the learning process cannot modify or remove them.

Primitives follow the execution environment abstraction from the Coding Agent Loop Specification. The same primitives work across local, Docker, Kubernetes, SSH, and WASM environments by delegating to the `ExecutionEnvironment` interface.

**Tool output truncation** (head/tail split, character limits, line limits) follows the Coding Agent Loop Specification Section 5. This applies to primitive outputs before they are returned to the calling agent.

---

## 10. Multi-Provider Support

### 10.1 Provider-Aligned Agents

Different LLM providers produce best results with different tool formats and system prompts. This principle from the Coding Agent Loop Specification applies to the agent architecture:

- Agents using **Anthropic models** should have `edit_file` (old_string/new_string) in their primitive set
- Agents using **OpenAI models** should have `apply_patch` (v4a format) in their primitive set
- Agents using **Gemini models** should follow gemini-cli tool conventions

The genome can contain provider-specific agent variants. A `code-editor-anthropic` and `code-editor-openai` agent can coexist, with routing rules directing to the right one based on the active provider.

### 10.2 Model Selection

The Unified LLM Client handles all provider communication. Agents specify model identifiers using the provider's native strings (e.g., `"claude-opus-4-6"`, `"gpt-5.2-mini"`, `"gemini-3-flash-preview"`).

For model class references (`"fast"`, `"best"`, `"balanced"`), the agent runtime resolves to specific models based on available providers:

| Class | Anthropic | OpenAI | Gemini |
|---|---|---|---|
| best | claude-opus-4-6 | gpt-5.2 | gemini-3-pro-preview |
| balanced | claude-sonnet-4-5 | gpt-5.2-mini | gemini-3-flash-preview |
| fast | claude-haiku-4-5 | gpt-5.2-mini | gemini-3-flash-preview |

### 10.3 Cross-Provider Agent Composition

A root agent using Opus can spawn subagents using GPT-5.2-mini or Gemini Flash. Each subagent's Plan uses its specified model, and its primitives are formatted for that model's provider conventions.

This enables cost optimization: expensive reasoning at the top level, cheap execution at lower levels.

---

## 11. Bootstrap

### 11.1 The Cold Start Problem

On first run, the genome is nearly empty. The agent has primitives but no agents to compose them. Bootstrap agents provide the minimum viable set to start working. They are intentionally simple — the learning process should improve them quickly.

### 11.2 Bootstrap Agents

```
code-reader:
    description: "Find and return relevant code from files"
    model: fast
    capabilities: [read_file, grep, glob]

code-editor:
    description: "Make targeted edits to code files"
    model: balanced
    capabilities: [read_file, edit_file, write_file]

command-runner:
    description: "Execute shell commands and interpret results"
    model: fast
    capabilities: [exec]

root:
    description: "Decompose coding tasks into subgoals and delegate"
    model: best
    capabilities: [code-reader, code-editor, command-runner]
```

Four agents. The root delegates to three specialists. The specialists use primitives directly. This is the minimum tree that can handle "read, edit, run" — the core coding workflow.

### 11.3 Growth Trajectory

The expected evolution from bootstrap to mature genome:

**Session 1-10:** Bootstrap agents handle everything. Stumbles are frequent. Learn creates memories ("this project uses yarn, not npm"). Learn tweaks agent prompts based on observed inefficiencies.

**Session 10-50:** Learn creates specialized agents as patterns emerge. `test-runner` splits from `command-runner`. `code-reader` gets better at finding relevant code. Routing rules start forming.

**Session 50-200:** The genome stabilizes around 10-20 well-tuned agents. Stumble rate drops measurably. Learn shifts from creating agents to refining them. Pruning removes unused variants.

**Session 200+:** Improvements are incremental. Learn mostly creates memories and fine-tunes prompts. Major structural changes are rare. The genome reflects the user's actual workflow.

This trajectory is illustrative, not prescriptive. The actual growth depends on the diversity and difficulty of tasks.

---

## 12. The Immutable Kernel

### 12.1 What Cannot Be Modified

The learning process can modify the genome (agents, memories, routing rules). It cannot modify:

1. **The core loop.** Perceive → Recall → Plan → Act → Verify is fixed. Learn cannot change the loop structure, skip phases, or reorder them.

2. **The primitives.** read_file, write_file, edit_file, apply_patch, exec, grep, glob, fetch are permanent. Learn cannot remove or redefine them. (It can create agents that wrap them with better behavior.)

3. **The Learn process itself.** Learn cannot modify its own trigger filtering, improvement actions, or evaluation logic. (This prevents the agent from disabling its own learning or removing safety checks.)

4. **The audit log.** Every action, stumble, and genome mutation is logged. Learn cannot suppress, modify, or delete log entries.

5. **Safety constraints.** The set of actions requiring human approval (configurable by the user, not the agent). Learn cannot expand its own permissions.

### 12.2 Rationale

Self-modification is powerful but dangerous. An agent that can modify its own learning process can disable learning. An agent that can modify its own safety constraints can remove them. An agent that can modify its own audit log can hide its actions.

The immutable kernel is the boundary between "the agent improves how it does its job" (genome mutations — safe) and "the agent changes what it fundamentally is" (kernel mutations — unsafe). The first is the entire point. The second is prohibited.

### 12.3 Human Override Points

The user (not the agent) can:
- View and approve/reject genome mutations before they take effect
- Roll back any genome change via git history
- Set approval requirements for different mutation types (e.g., auto-approve memories, require approval for new agents)
- Freeze the genome entirely (disable Learn)
- Manually edit the genome (add/remove agents, memories, routing rules)

The default should be permissive for low-risk mutations (memories, prompt tweaks) and gated for high-risk mutations (new agents, agent removal). The exact policy is user-configurable.

---

## 13. Implementation Language

### 13.1 TypeScript

The reference implementation should use TypeScript (running on Bun or Node.js). Rationale:

1. **Runtime code generation.** The learning process creates agents, which are code. TypeScript/JavaScript has native `new Function()`, `import()`, and eval. Languages like Go and Rust would require an embedded scripting engine.

2. **Dynamic imports with cache busting.** Agent specs stored as .ts files can be dynamically imported, modified, and re-imported. This is proven in the workbench tool registry.

3. **Ecosystem alignment.** The reference projects (pi-agent-core, gemini-cli, Vercel AI SDK) are TypeScript. The LLM provider SDKs have first-class TypeScript support.

4. **Type safety where it matters.** The core loop, agent specs, and genome types benefit from static typing. The dynamically generated agent code can use `any` at the boundaries.

5. **Fast iteration.** Bun executes .ts files directly without a compile step. Agents can be modified and reloaded instantly.

### 13.2 Non-Goals

The spec is language-agnostic in design. A Go, Rust, or Python implementation is possible — the primitives and loop structure are universal. Dynamic agent creation would require embedding a scripting runtime (e.g., Goja for Go, Deno for Rust). The spec does not prescribe a language, but the reference implementation is TypeScript for the reasons above.

---

## 14. Definition of Done

### 14.1 Core Loop

- [ ] Perceive collects inputs from queue and environment
- [ ] Recall searches genome and returns agents, memories, routing hints
- [ ] Plan calls LLM with perceived state + recalled context
- [ ] Plan produces Act delegations as tool calls (agents mapped to tools)
- [ ] Act spawns subagents with goal + hints
- [ ] Subagents run their own Perceive → Recall → Plan → Act → Verify loop
- [ ] Verify checks objective signals (success, stumbles, turns, time)
- [ ] Natural completion: Plan produces text with no delegations
- [ ] Multiple Acts execute concurrently when independent

### 14.2 Agents and Primitives

- [ ] AgentSpec fully describes an agent (name, description, prompt, model, capabilities, constraints)
- [ ] Primitives execute atomically against ExecutionEnvironment
- [ ] All required primitives implemented (read_file, write_file, edit_file, exec, grep, glob, fetch)
- [ ] Agents can spawn subagents (recursive)
- [ ] Max depth enforced
- [ ] Timeout enforced per-agent

### 14.3 Genome

- [ ] Agent specs stored as files, git-versioned
- [ ] Memories stored and searchable
- [ ] Routing rules stored and matchable
- [ ] Every mutation produces a git commit
- [ ] Genome can be initialized from bootstrap agents
- [ ] Genome can be rolled back to any previous state

### 14.4 Recall

- [ ] Returns all agents when genome is small (< 20)
- [ ] Uses embedding similarity when genome is larger
- [ ] Returns relevant memories
- [ ] Returns matching routing rules
- [ ] Results injected into Plan's LLM context

### 14.5 Learn

- [ ] Runs asynchronously, does not block main loop
- [ ] Receives LearnSignals from Verify
- [ ] Filters signals (not every stumble triggers learning)
- [ ] Can create new agents
- [ ] Can update existing agent specs
- [ ] Can create memories
- [ ] Can create routing rules
- [ ] Evaluates whether improvements reduced stumble rate
- [ ] Periodically prunes unused genome entries
- [ ] All improvements are git-committed with descriptive messages

### 14.6 Multi-Provider

- [ ] Works with Anthropic models (Claude)
- [ ] Works with OpenAI models (GPT)
- [ ] Works with Gemini models
- [ ] Provider-aligned primitives (edit_file for Anthropic, apply_patch for OpenAI)
- [ ] Cross-provider agent composition (root on Opus, subagents on Haiku/Flash)

### 14.7 Immutable Kernel

- [ ] Core loop cannot be modified by Learn
- [ ] Primitives cannot be removed by Learn
- [ ] Learn process cannot modify itself
- [ ] Audit log cannot be suppressed
- [ ] Safety constraints enforced

### 14.8 Bootstrap

- [ ] Fresh genome starts with bootstrap agents (code-reader, code-editor, command-runner, root)
- [ ] Bootstrap agents are sufficient to complete simple coding tasks
- [ ] Learn begins creating improvements after first session

### 14.9 Integration Test

End-to-end with real API keys:

```
-- 1. Bootstrap: fresh genome, simple task
agent = create_agent(genome = fresh_genome())
agent.submit("Create a file hello.py that prints 'Hello World'")
ASSERT file_exists("hello.py")

-- 2. Multi-step: requires decomposition
agent.submit("Add a command-line argument to hello.py that takes a name,
              then write a test for it")
ASSERT file_exists("hello.py") AND contains("argparse" OR "sys.argv")
ASSERT file_exists test file

-- 3. Stumble and learn: force an error pattern
agent.submit("Run the tests")  -- may stumble on test runner detection
agent.submit("Run the tests")  -- second time should be better if Learn worked
ASSERT second_run.stumbles <= first_run.stumbles

-- 4. Genome growth: verify agents were created
ASSERT genome.agent_count() > bootstrap_agent_count

-- 5. Cross-session persistence
agent2 = create_agent(genome = load_genome())  -- new session, same genome
agent2.submit("Run the tests")  -- should use learned test runner
ASSERT agent2.recall finds test-runner agent
```

---

## Appendix A: Event System

The agent emits typed events for host application integration. Events follow the pattern from the Coding Agent Loop Specification.

```
ENUM EventKind:
    SESSION_START
    SESSION_END
    PERCEIVE            -- input received
    RECALL              -- genome search completed
    PLAN_START          -- LLM call initiated
    PLAN_DELTA          -- streaming token
    PLAN_END            -- LLM call completed
    ACT_START           -- subagent spawned (includes goal)
    ACT_END             -- subagent completed (includes result)
    PRIMITIVE_START     -- primitive execution started
    PRIMITIVE_END       -- primitive execution completed (includes full output)
    VERIFY              -- verification result
    LEARN_SIGNAL        -- stumble detected, signal queued
    LEARN_START         -- Learn process acting on a signal
    LEARN_MUTATION      -- genome modified (includes diff)
    LEARN_END           -- Learn process completed
    STEERING            -- steering message injected
    WARNING             -- non-fatal issue
    ERROR               -- error occurred
```

Events carry the agent's ID and depth in the hierarchy, so the host can distinguish root-level events from subagent events.

---

## Appendix B: Fitness Function — Stumble Rate

The primary metric for self-improvement is the stumble rate: the ratio of stumbles to total actions over a window of sessions.

```
stumble_rate = total_stumbles / total_actions

Where:
    total_stumbles = errors + retries + timeouts + failures + inefficiencies
    total_actions  = all Act delegations + all primitive executions
```

The agent tracks stumble rate globally and per-agent. Learn uses per-agent stumble rate to identify which agents need improvement and to evaluate whether improvements helped.

**Baseline data** from analysis of 2,201 real coding agent sessions across 3 machines:
- Average stumble rate: 3.7% (2,890 stumbles across 77,220 tool uses)
- Top stumble sources: command failures (33%), test failures (11%), file-not-found (8%)

A self-improving agent should demonstrably reduce its stumble rate over time, with diminishing returns as easy improvements are captured first.

---

## Appendix C: Relationship to Benchmark Evaluation

The self-improvement loop described in this spec operates on real-world usage data (production loop). A complementary evaluation loop can run the agent against coding benchmarks (e.g., SWE-bench) to measure capability more rigorously.

**Production loop:** After each real session, analyze stumbles, improve genome. Signal: real user workflows. Frequency: continuous.

**Benchmark loop:** Periodically run the agent against a benchmark suite, measure performance, apply improvements, re-run. Signal: known-correct solutions. Frequency: periodic (e.g., nightly).

Both loops write to the same genome. Benchmark improvements transfer to production and vice versa. The benchmark loop provides rapid iteration (hundreds of runs against known tasks). The production loop provides coverage of real-world patterns the benchmarks miss.

The benchmark harness is external to this spec. The agent's improvement loop does not depend on it. But an implementation that supports both loops will improve faster than one that relies on production data alone.

---

## Appendix D: Design Rationale

This appendix captures the reasoning behind every significant design decision. It is written for the implementor who needs to understand not just what to build, but why each choice was made and what alternatives were rejected. This context is essential for making good judgment calls during implementation.

### D.1 The Data That Motivated This Design

This spec did not emerge from theory. It emerged from analyzing 2,201 real coding agent sessions (Claude Code) across 3 machines, totaling 77,220 tool uses and 237,000 messages. The analysis revealed:

**The agent makes the same mistakes over and over:**
- 3.7% average error rate, identical across all 3 machines. The 1,000th session was no better than the 1st.
- 260 identical `git status` retries across machines. The agent checks git status, gets the answer, then checks again minutes later because it has no memory.
- 308 test failures where the agent didn't know the project's test framework and tried the wrong one.

**The agent uses brute force where intelligence would help:**
- 14,236 `Bash→Bash→Bash` trigrams (3+ consecutive bash calls). Many are the agent manually doing what a purpose-built tool would do in one step.
- 3,597 `Read→Read→Read` trigrams. The agent reads file after file looking for something it could find in one grep.
- 700+ `pipe-to-head/tail` patterns. The agent runs a command, gets too much output, then pipes through head or tail. A smarter tool would truncate intelligently.

**The agent has tools but doesn't use them:**
- We built 9 productivity tools (test-loop, file-context, git-status-check, retry-guard, etc.) based on the analysis. They worked. But the agent didn't use them reliably because they were buried in an MCP eval tool behind two layers of indirection.
- The fundamental problem: tools exist in a registry, but there's no architectural mechanism to surface them at the right moment. The agent relies on static system prompt instructions ("remember to use your tools"), which the LLM forgets as context grows.

**This is what a self-improvement loop would fix.** The agent can observe these patterns (we proved it — we built transcript analysis tools that detect them automatically). It can build capabilities to address them (we proved it — we built the tools). What's missing is closing the loop so this happens autonomously.

### D.2 Why Recall Is a Separate Phase from Plan

**The problem it solves:** In every existing coding agent, the LLM receives a system prompt listing available tools, then must remember those tools across a conversation that may grow to hundreds of thousands of tokens. As context grows, the LLM's attention to the tool list degrades. Tools that exist are not used because the LLM has effectively forgotten them.

**Why not just include tools in the system prompt?** That's what everyone does today. It works when you have 10-15 tools. It breaks at 30+ (too many tokens, selection degrades). It breaks at long conversations (LLM attention to tools at the start of context weakens). And it's static — the same tools are shown regardless of whether they're relevant to the current task.

**Why not combine Recall with Plan?** If you let the LLM decide what to recall during planning, you're asking it to remember what it has forgotten. The LLM can't search for tools it doesn't know about. Recall must be a deterministic retrieval step that runs BEFORE the LLM, injecting relevant context into the LLM's input. The LLM then selects from what Recall surfaced — it doesn't have to remember the full catalog.

**The analogy:** When you need to fix a plumbing problem, you don't try to remember every tool in your garage. You walk to the garage (Recall), see what's there, grab what looks relevant, then decide what to do (Plan). The walk is a separate step from the decision.

**Implementation pitfall:** Do not implement Recall as an LLM call. Recall must be cheap and deterministic — embedding similarity, keyword matching, or just "return everything if the genome is small." Adding an LLM call to Recall defeats the purpose (you'd be calling the LLM twice per cycle, and the recall LLM has the same forgetting problem).

### D.3 Why Every Act Is a Goal-Directed Subagent

**The problem it solves:** In existing agents, "read a file" is an atomic tool call. But our data shows that "read a file" is almost never what the agent actually wants. It wants "find the relevant code in this file." That's a multi-step operation: grep for the function, read around those lines, maybe follow an import. The 3,597 Read→Read→Read trigrams are evidence that the agent is doing multi-step file navigation that should be a single delegation.

**Why goals not instructions?** Two reasons:

1. **Composability.** When a parent agent says "find the authentication middleware" (goal), the child agent can use any strategy: grep, read, glob, or a combination. When a parent says "read src/auth.ts lines 140-200" (instruction), the child is a dumb executor. Goals let the child be smart. This matters because a better child agent (improved by Learn) achieves the same goal more efficiently, without the parent needing to change at all.

2. **Self-improvement works at every level.** If the parent gives instructions, improving the child is meaningless — it just follows orders. If the parent gives goals, improving the child directly improves the system. A better code-reader agent that finds code in 2 turns instead of 5 makes every parent agent faster, with zero changes to the parents.

**Why hints?** Goals alone can be too vague. "Find the auth middleware" with no context means the subagent starts from scratch. Hints like "I saw a reference to it in src/routes/index.ts" or "this is a Go project using chi router" give the subagent a head start without constraining its approach. Hints are context, not instructions. The subagent can use them or ignore them.

**What makes a good hint vs a bad one:**
- Good: "I saw an import from ./auth in the main router" (context that narrows the search)
- Good: "The previous attempt to edit this file failed because old_string wasn't unique" (history of what went wrong)
- Bad: "Read src/auth.ts first, then check the imports" (this is an instruction disguised as a hint)
- Bad: "Use grep, not read_file" (this constrains the approach)

**Implementation pitfall:** The temptation will be to have the parent agent give detailed instructions instead of goals, because instructions feel more reliable. Resist this. The value of the architecture depends on goals. If parents give instructions, you've built an expensive function-call system, not an agent hierarchy.

### D.4 Why Learn Is Asynchronous

**The problem it solves:** If Learn runs synchronously in the main loop, every stumble pauses the agent's work while it reflects. This is like stopping to write in your journal after every typo — the overhead would make the agent unusable.

**Why not learn during idle time (between user messages)?** That works too, and end-of-task / end-of-session learning should happen during idle time. But stumble signals should be queued immediately and processed in the background, because:

1. **Context is freshest at the point of stumble.** If you defer all learning to session end, the signal loses context. "The agent stumbled running tests" is less useful than "the agent ran `pytest` but this project uses `vitest`, got a `command not found` error, then tried `npx vitest` which worked."

2. **The main loop should never wait.** Even a fast Learn decision (100ms) adds up across hundreds of actions. The main loop's job is to do the user's work. Learning is a background investment in the future.

3. **Learn can batch.** Multiple stumbles during a session can be processed together, finding patterns that individual signals miss. "The agent stumbled 3 times on file-not-found" is more actionable than any single instance.

**The race condition:** Learn modifies the genome. Recall reads the genome. If Learn writes a new agent while Recall is searching, what happens? This is benign — Recall reads a snapshot. The new agent appears on the next Recall, not the current one. This is actually desirable: improvements take effect on the next cycle, not mid-cycle.

### D.5 Why Stumble Rate, Not User Satisfaction

**The problem with user satisfaction:** The agent can't measure it. Users don't rate every interaction. When they do give feedback (corrections, "no, do it this way", Ctrl+C), it's sparse and noisy. Building a fitness function on user satisfaction requires either constant user feedback (annoying) or proxy metrics that may not correlate with actual satisfaction.

**Why stumble rate works:** Every stumble is objectively observable by the agent itself:
- Errors have error codes and messages
- Retries are detectable (same action repeated)
- Inefficiency is measurable (turn count for simple goals)
- Timeouts have timestamps
- Failures are reported by subagents

No user feedback is needed. The agent knows when it stumbled. The metric is: "did I get there without stumbling?" not "did the user like the result?"

**The subtle point:** A low stumble rate doesn't guarantee good output. The agent could achieve zero stumbles by doing nothing. The fitness function only works when combined with goal completion — you must complete the goal AND minimize stumbles. Stumble rate is a refinement metric: given that you're completing goals, how efficiently are you doing it?

**The gaming risk:** An agent optimizing for low stumble rate might learn to attempt less ambitious actions (can't stumble if you don't try). The counter-pressure is that goals must still be achieved. A subagent that returns "I can't do this" is a failure, which is the worst kind of stumble. Learn should treat failures more severely than errors or retries.

### D.6 Why Agents All the Way Down (And Where It Stops)

**The unification insight:** In existing agents, there's a sharp distinction between "tools" (atomic operations) and "agents" (reasoning entities). Tools are cheap but dumb. Agents are smart but expensive. The developer must decide which operations are tools and which are agents, and this decision is frozen at build time.

By making everything agents, you remove this distinction. The simplest agent is a thin wrapper around a primitive — it costs one LLM call (or zero, if the goal is trivially mapped to a primitive). The most complex agent decomposes a task across dozens of sub-delegations. They're the same abstraction at different weight classes.

**Why this matters for self-improvement:** Learn can promote a primitive-wrapper agent to a multi-step reasoning agent without changing any parent. If the `code-reader` agent starts as "grep then read_file" and evolves to "analyze file structure, identify relevant sections, read selectively, follow imports" — that's a capability upgrade that happens purely within the agent spec. No parent agent changes. No architectural modification.

**Where recursion stops:** At primitives. `read_file(path, offset, limit)` is not an agent. It's a syscall. No reasoning, no LLM call, no delegation. It reads bytes from disk and returns them. Primitives are the ground truth — the point where the agent's reasoning meets the physical world.

**The depth concern:** Won't deep agent hierarchies be slow and expensive? Yes, potentially. That's why `AgentConstraints.max_depth` exists. In practice, most tasks should be 2-3 levels deep. The root delegates to specialists, specialists use primitives. Going deeper than that usually means the decomposition is wrong. Learn should detect and correct excessive depth as an inefficiency.

**Implementation pitfall:** Do not require an LLM call for every agent invocation. A simple agent receiving a trivially mappable goal (e.g., a `file-writer` agent receiving "write this content to that path") should shortcut directly to the primitive. The agent abstraction is the interface, but the runtime can optimize away unnecessary LLM calls for simple cases.

### D.7 Why These Specific Bootstrap Agents

The bootstrap set is: `root`, `code-reader`, `code-editor`, `command-runner`. Four agents. This is not arbitrary — it's the minimum viable tree for the core coding workflow:

1. Every coding task involves some combination of reading, editing, and running commands.
2. The root agent needs to decompose tasks, so it must be able to delegate to at least these three capabilities.
3. Each specialist handles one concern cleanly, using 2-3 primitives.

**Why not start with just primitives and let Learn build all agents?** The cold start problem. Without any agents, the root agent would have to compose primitives directly, which means it's making low-level decisions ("which file to read, what offset") instead of high-level decisions ("understand the code, make the edit"). The bootstrap agents provide the abstraction layer that lets the root think at the right level from session 1.

**Why not start with more agents?** YAGNI. A `test-runner` agent would be useful, but it's the first thing Learn will create when the agent encounters test-running tasks. Starting with it would be premature — we don't know which test frameworks the user uses, how their project is structured, or what test-running patterns they prefer. Better to let Learn create a test-runner that's fit for the actual environment.

**Why is root's model "best" while the others are "fast"?** The root agent's job is decomposition and strategy — the hardest cognitive task. Getting decomposition wrong cascades failures through the entire tree. Specialists do simpler, more focused work where a cheaper model suffices. This is also a cost optimization: one expensive call at the top, many cheap calls below.

### D.8 Why Git Versioning for the Genome

**The primary value is rollback.** Learn is experimental by nature. It tries improvements that might not work. When an improvement increases the stumble rate, `git revert` undoes it cleanly. Without versioning, you'd need a custom undo mechanism.

**The secondary value is debugging.** When the agent starts behaving differently, `git log` shows exactly what changed and when. `git diff` between two genome states shows precisely which agent specs, memories, or routing rules were modified. `git bisect` can find the exact commit where a regression was introduced.

**Why not a database?** Databases are better for querying but worse for diffing, versioning, and human inspection. Agent specs are YAML files that humans should be able to read, edit, and understand. Git makes the genome transparent and auditable.

**Why YAML for agent specs, JSONL for memories?** Agent specs are edited (by Learn and by humans) and benefit from a readable format. YAML is human-friendly for structured data with embedded text (system prompts). Memories are append-heavy and rarely edited individually — JSONL (one JSON object per line) is efficient for appending and for streaming reads. Different access patterns justify different formats.

### D.9 Why Memories Are Separate from Agent Specs

Memories and agent specs both influence behavior, but they serve different purposes:

**Agent specs define capabilities.** "I am a test-runner agent. I know how to run tests and parse output." This is structural — it defines what an agent can do.

**Memories define context.** "This project uses vitest, not pytest." This is factual — it provides information that any agent might need but no single agent should hardcode.

If you put project-specific facts into agent specs, you get agent proliferation: `test-runner-vitest`, `test-runner-jest`, `test-runner-pytest`, each with the framework name baked in. Instead, one `test-runner` agent + a memory "this project uses vitest" is cleaner. The memory is injected by Recall into whatever agent needs it.

**The confidence decay model:** Memories decay because facts go stale. The project might switch from vitest to jest. A memory that was correct 6 months ago might be wrong today. Decay ensures that unused memories gradually disappear. A memory that keeps getting recalled and used has its confidence refreshed — it's still relevant. A memory that was recalled once and never again fades out.

**Implementation pitfall:** Do not implement memory confidence as a simple timer. Confidence should decay based on time since last USE, not time since creation. A memory created 6 months ago but used yesterday is still relevant. A memory created yesterday but never used might already be stale.

### D.10 Why Routing Rules Exist

You might wonder why routing rules are needed when Recall already surfaces agents by embedding similarity and Plan already selects from candidates. The answer is that embeddings miss domain-specific preferences that can only be learned from experience.

**Example:** The agent has both `test-runner` (generic) and `test-runner-go` (specialized). When the user says "run the tests" in a Go project, embedding similarity might rank both equally — the goal mentions "tests" and both agents mention "tests." But experience has shown that `test-runner-go` succeeds in 2 turns while `test-runner` stumbles for 5 turns in Go projects. A routing rule captures this: "In Go projects, prefer `test-runner-go`."

**Why not just improve the generic agent?** Sometimes you should. Learn's simplicity pressure should prefer making one good agent over maintaining two. But sometimes specialization genuinely helps — Go testing has different conventions than JavaScript testing. The routing rule is Learn's way of saying "I've noticed this pattern and here's my recommendation," while leaving the final decision to Plan.

**Why hints, not constraints?** Routing rules are presented to Plan as guidance: "for this type of task, agent X tends to work better." Plan can override them. This is intentional — the LLM might have context that the routing rule doesn't account for. Routing rules are heuristics, not policy.

### D.11 Open Questions for the Implementor

These are areas where the spec deliberately leaves room for judgment. The right answer depends on empirical results.

**1. How should primitives be exposed to agents?** Two options:
- (a) Primitives appear as tool definitions alongside agent tool definitions. Plan chooses between delegating to a subagent or calling a primitive directly.
- (b) Primitives are only accessible through agents. Even "read this file" goes through the code-reader agent.

Option (a) is more efficient (skip the subagent overhead for simple operations). Option (b) is purer (agents all the way down) and means every file read goes through an agent that might add value (reading only relevant sections). Start with (a) for pragmatism, but the architecture supports both. Learn might evolve toward (b) over time as agents get good enough to justify the overhead.

**2. Should memories be scoped?** A memory like "this project uses vitest" is project-specific. A memory like "when edit_file fails with ambiguous match, add more context lines" is universal. Should the genome maintain separate scopes (per-project, per-user, universal)? This adds complexity but prevents cross-project contamination. The spec doesn't prescribe an answer — start without scoping and add it if memory interference becomes a problem.

**3. How aggressive should Learn's pruning be?** Too aggressive and useful agents get removed during quiet periods. Too passive and the genome bloats with unused entries. The spec suggests pruning agents with "zero usage for N sessions" but doesn't specify N. Start conservative (N = 20 sessions). Observe whether the genome stabilizes at a reasonable size. Adjust.

**4. When should Verify use an LLM?** Verify can assess goal satisfaction cheaply (did the subagent succeed? any errors?) or expensively (ask an LLM "did this output actually address the goal?"). The cheap path misses subtle failures. The expensive path adds cost to every action. A reasonable heuristic: use cheap verification by default, escalate to LLM verification when the cheap signals are ambiguous (success with high turn count, or partial output).

**5. What's the right embedding model for Recall?** The spec says "use embedding similarity" for medium-scale genomes but doesn't specify which model. Options: OpenAI's text-embedding-3-small (cheap, good), a local model like all-MiniLM-L6-v2 (free, fast, worse quality), or the same LLM provider being used for Plan. Start with whatever's cheapest and local. Recall quality matters less than Recall speed — a slightly wrong retrieval is corrected by Plan, but a slow retrieval blocks every cycle.

**6. How should Learn handle conflicting improvements?** Two Learn signals might suggest contradictory changes to the same agent. E.g., "make code-reader more thorough" (from missed information stumbles) vs "make code-reader faster" (from inefficiency stumbles). The spec doesn't prescribe a resolution mechanism. The simplest approach: last-write-wins with git history for rollback. A more sophisticated approach: Learn weighs signal frequency and severity to choose which direction to optimize.

### D.12 Anti-Patterns to Avoid

**1. The Librarian Trap.** Don't build a separate "librarian" LLM that routes tool calls. This was considered and rejected. Adding an LLM hop to every action is expensive and slow. Instead, Recall (cheap, deterministic) surfaces candidates and Plan (the LLM you're already calling) makes the selection. One LLM call per cycle, not two.

**2. The Overfitting Trap.** Learn should not create hyper-specific agents for one-off situations. "The CI pipeline for the foo-bar repo needs `NODE_OPTIONS=--max-old-space-size=4096`" is a memory, not an agent. Agents should be general enough to handle a class of goals. The test: "would this agent be useful in a different project?" If no, it should probably be a memory, not an agent.

**3. The Abstraction Trap.** Don't create deep agent hierarchies for simple tasks. A root agent → code-reader agent → file-navigator agent → line-finder agent hierarchy for "read this function" is absurd. Two levels (root → specialist) should handle most tasks. Three levels for genuinely complex decompositions. More than three is almost certainly overengineered. Learn's inefficiency detection should flag excessive depth.

**4. The Stale Memory Trap.** Memories that were true once might not be true anymore. "This project uses React 17" might have been updated to React 19. Memory confidence decay helps, but it's not sufficient. Consider adding a validation step to Recall: when a memory is about to be injected, check if it's still consistent with observable state (e.g., check package.json for the React version). This is expensive and not always possible, so it's not required — but high-confidence memories about mutable facts (dependency versions, file locations) are the highest-risk category.

**5. The Metric Gaming Trap.** An agent optimizing for stumble rate can game the metric: attempt only easy tasks, give up quickly on hard ones, count "I decided not to do this" as success rather than failure. The counter-pressure must be built into the fitness function: failure to achieve a goal is the most expensive stumble, worse than multiple errors or retries on the path to success. Completing the goal matters more than the path. Stumble rate is only meaningful for goals that were completed.

### D.13 Why TypeScript Is the Right Language for This Specific Project

This section goes beyond the generic "TypeScript has eval" argument in Section 13.

**We already proved the pattern.** The workbench tool registry (`code-tool/src/registry.ts`) implements: TypeScript files as the unit of saved capability, dynamic import with cache busting (copy to temp file, import, delete), git auto-versioning of every mutation, Zod schemas for typed inputs. This is exactly the genome storage mechanism the spec describes. It works. It's been running in production across 2,201 sessions.

**The specific cache-busting technique matters.** When you modify a .ts file and re-import it, the JavaScript module cache returns the stale version. The workbench registry solves this by copying to a temp file with a unique name (`_tmp_{name}_{timestamp}.ts`), importing the temp file, then deleting it. This is ugly but reliable. It's been tested across hundreds of tool saves. An implementor should use this exact technique for agent spec loading, not try to be clever with import cache invalidation.

**Bun vs Node:** Bun executes .ts files natively without a compile step. This matters when Learn creates a new agent — the agent spec (a .ts file) is immediately importable without running tsc. Node requires either tsx, ts-node, or a pre-compilation step. The workbench uses Bun and this has been reliable.

**The `run()` helper gotcha:** The workbench's `run()` function uses `execFile`, which does NOT resolve PATH. If you write `run('npx', ['vitest'])` it fails with ENOENT because `npx` isn't at a full path. The solution is `run('/bin/sh', ['-c', 'npx vitest'])` — run through the shell for PATH resolution. This bit us during development and will bite the sprout implementor too. The `exec` primitive in this spec should use shell execution by default.

### D.14 The Conversation That Produced This Spec

This spec was designed collaboratively between a human (Jesse) and an AI agent over two extended sessions. The design process was:

1. **Empirical analysis first.** We analyzed 2,201 real coding agent transcripts across 3 machines before designing anything. The architecture is driven by data about actual agent behavior, not theoretical ideals.

2. **The loop was iterated multiple times.** The initial proposal was `observe → think → act → record`. Jesse identified that Recall and Plan are different operations (retrieval vs generation). He identified that Learn should be async, not blocking. He identified that Act should be goal-directed subagent delegation, not tool calls. He identified that "read a file" is almost always a multi-step agent task, not a primitive call. Each of these insights refined the loop.

3. **The name "sprout" was chosen** for the qualities it implies: starting from almost nothing (a seed), growing organically from experience, the process being natural and continuous rather than engineered and discrete.

4. **Prior art was thoroughly researched.** Two research agents surveyed the landscape. The key finding: the pieces exist (DGM for self-modification, Voyager for skill libraries, DSPy for metric-driven optimization) but nobody has assembled them into a production coding agent with closed-loop self-improvement. That gap is what sprout fills.
