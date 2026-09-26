// Fixture data for the Evener for iPhone prototype. Shaped like measured
// usage on magic-kingdom (2026-09-25): a fleet of long-running coordinators,
// big subagent swarms, two hosts, many provider profiles, 14 plugins.
// Times are "seconds ago" at page load; the runtime turns them into clocks.
(function () {
  const M = 60, H = 3600, D = 86400;

  const hosts = [
    { id: "magic-kingdom", os: "Linux x86_64", version: "0.9.412", state: "connected", local: true, roots: ["/home/jesse/git", "/home/jesse"], origin: "hub.toml", cpus: 16, memGB: 62 },
    { id: "paradise-park", os: "macOS arm64", version: "0.9.409", state: "connected", local: false, roots: ["/Users/jesse/git"], origin: "hub.toml", lastError: null, cpus: 12, memGB: 36 },
  ];

  const providers = [
    { id: "lunaroute", base: "OpenAI-compatible", auth: "key", status: "ok", statusText: "Key set", isDefault: true, models: ["deepseek-4.1-flash", "glm-5.3-vision", "glm-5.2-vision", "glm-5.3-flash", "glm-5.3"] },
    { id: "codex-jesse-fsck.com", base: "OpenAI Codex", auth: "oauth", status: "expired", statusText: "Sign-in expired", models: ["gpt-5.6", "gpt-5.6-luna", "gpt-6-astra"] },
    { id: "codex-jesse-at-pr", base: "OpenAI Codex", auth: "oauth", status: "expiring", statusText: "Expires in 3d", models: ["gpt-5.6"] },
    { id: "oai-jrv", base: "OpenAI", auth: "key", status: "ok", statusText: "Key set", models: ["gpt-5.5", "codex-auto-review"] },
    { id: "meta", base: "Meta", auth: "key", status: "ok", statusText: "Key set", models: ["muse-spark-1.3"] },
    { id: "kimi-code", base: "Moonshot", auth: "key", status: "ok", statusText: "Key set", models: ["k3"] },
    { id: "openrouter-corp", base: "OpenRouter", auth: "key", status: "ok", statusText: "Key set", models: ["claude-sonnet-5", "qwen3-coder-plus"] },
    { id: "vertex", base: "Google Vertex", auth: "oauth", status: "ok", statusText: "Signed in", models: ["gemini-3-pro"] },
    { id: "ollama", base: "Ollama", auth: "none", status: "ok", statusText: "Local, no sign-in", models: ["qwen3-coder:30b"] },
    { id: "groq3", base: "Groq", auth: "key", status: "ok", statusText: "Key set", models: ["kimi-k3-instant"] },
    { id: "stepfun", base: "StepFun", auth: "key", status: "error", statusText: "Key rejected", models: ["step-3"] },
  ];

  // name, provider, context K, $in, $out per Mtok, vision, efforts
  const ALL = ["low", "medium", "high", "xhigh", "max"];
  const models = [
    ["deepseek-4.1-flash", "DeepSeek 4.1 Flash", "lunaroute", 256, 0.3, 1.2, false, ALL],
    ["glm-5.3-vision", "GLM 5.3 Vision", "lunaroute", 200, 0.6, 2.2, true, ALL],
    ["glm-5.2-vision", "GLM 5.2 Vision", "lunaroute", 200, 0.5, 2.0, true, ALL],
    ["glm-5.3-flash", "GLM 5.3 Flash", "lunaroute", 128, 0.1, 0.4, false, ["low", "medium", "high"]],
    ["glm-5.3", "GLM 5.3", "lunaroute", 200, 0.6, 2.2, false, ALL],
    ["gpt-5.6", "GPT-5.6", "codex-jesse-fsck.com", 400, 1.25, 10, true, ALL],
    ["gpt-5.6-luna", "GPT-5.6 Luna", "codex-jesse-fsck.com", 400, 1.25, 10, true, ALL],
    ["gpt-6-astra", "GPT-6 Astra", "codex-jesse-fsck.com", 1000, 5, 30, true, ALL],
    ["gpt-5.5", "GPT-5.5", "oai-jrv", 400, 1.25, 10, true, ALL],
    ["muse-spark-1.3", "Muse Spark 1.3", "meta", 1000, 2, 8, true, ["low", "medium", "high", "xhigh"]],
    ["k3", "Kimi K3", "kimi-code", 256, 0.6, 2.5, false, ["low", "medium", "high", "xhigh"]],
    ["claude-sonnet-5", "Claude Sonnet 5", "openrouter-corp", 1000, 3, 15, true, ALL],
    ["qwen3-coder-plus", "Qwen3 Coder Plus", "openrouter-corp", 256, 0.4, 1.6, false, ["low", "medium", "high"]],
    ["gemini-3-pro", "Gemini 3 Pro", "vertex", 1000, 2, 12, true, ALL],
    ["qwen3-coder:30b", "Qwen3 Coder 30B (local)", "ollama", 128, 0, 0, false, ["low", "medium", "high"]],
  ].map(([id, name, provider, ctx, pin, pout, vision, efforts]) => ({ id, name, provider, ctx, pin, pout, vision, efforts }));

  const marketplaces = [
    { id: "superpowers-marketplace", source: "github.com/obra/superpowers-marketplace" },
    { id: "claude-plugins-official", source: "github.com/anthropics/claude-plugins-official" },
    { id: "go-skills", source: "github.com/prime-radiant-inc/go-skills" },
    { id: "prime-radiant-marketplace", source: "github.com/prime-radiant-inc/marketplace" },
    { id: "simplify-code-dev", source: "/home/jesse/git/simplify-code" },
  ];

  const plugins = [
    { id: "superpowers", mp: "superpowers-marketplace", desc: "Brainstorming, TDD, debugging, plans and reviews", counts: "38 skills · 3 agents · 6 commands · 2 hooks", on: true, version: "6.4.1" },
    { id: "elements-of-style", mp: "superpowers-marketplace", desc: "Write clearly and concisely", counts: "1 skill", on: true, version: "1.2.0" },
    { id: "claude-session-driver", mp: "superpowers-marketplace", desc: "Manage other agent sessions as workers", counts: "1 skill · 1 command", on: true, version: "0.9.3" },
    { id: "private-journal-mcp", mp: "superpowers-marketplace", desc: "A private journal the agent can write to", counts: "1 MCP server", on: true, version: "2.1.0" },
    { id: "superpowers-chrome", mp: "superpowers-marketplace", desc: "Drive Chrome through DevTools", counts: "1 skill · 1 MCP server", on: false, version: "1.4.2" },
    { id: "frontend-design", mp: "claude-plugins-official", desc: "Distinctive, production-grade interfaces", counts: "1 skill", on: true, version: "1.0.0" },
    { id: "go", mp: "go-skills", desc: "Idiomatic Go, testing and profiling", counts: "12 skills", on: true, version: "3.2.0" },
    { id: "go-release", mp: "go-skills", desc: "Release engineering for Go modules", counts: "3 skills · 1 command", on: false, version: "1.1.0" },
    { id: "go-spec-reviewer", mp: "go-skills", desc: "Review Go design specs", counts: "1 agent", on: false, version: "0.4.0" },
    { id: "fileflow-pathologize", mp: "go-skills", desc: "Trace how files flow through a codebase", counts: "2 skills", on: true, version: "0.2.1" },
    { id: "iterative-development", mp: "prime-radiant-marketplace", desc: "Small steps with checkpoints", counts: "4 skills", on: true, version: "1.0.3" },
    { id: "shepherd-pr", mp: "prime-radiant-marketplace", desc: "Shepherd a PR through review to merge", counts: "2 skills · 2 commands · 1 hook", on: true, version: "0.7.0" },
    { id: "study-skills", mp: "prime-radiant-marketplace", desc: "Research and summarize unfamiliar code", counts: "3 skills", on: false, version: "0.3.0" },
    { id: "simplify-code", mp: "simplify-code-dev", desc: "Simplify recently changed code", counts: "1 skill · 1 command", on: true, version: "0.5.0", update: "0.6.0" },
  ];
  const defaultPlugins = plugins.filter((p) => p.on).map((p) => p.id);

  const recipes = [
    { id: "coordinator", name: "Evener coordinator", host: "magic-kingdom", project: "evener", model: "deepseek-4.1-flash", effort: "xhigh", plugins: defaultPlugins, access: "Workspace write", branch: "Current branch" },
    { id: "quick", name: "Quick question", host: "magic-kingdom", project: "home", model: "glm-5.3-flash", effort: "medium", plugins: ["superpowers", "elements-of-style"], access: "Read-only", branch: "Current branch" },
    { id: "mac", name: "Mac build", host: "paradise-park", project: "evener", model: "glm-5.3-vision", effort: "high", plugins: ["superpowers", "go", "go-release", "go-spec-reviewer"], access: "Workspace write", branch: "New worktree branch" },
  ];

  const projects = [
    { id: "evener", path: "git/prime-radiant-inc/evener", pinned: true },
    { id: "c-to-wasm", path: "git/c-to-wasm" },
    { id: "prime-radiant-inc.github.io", path: "git/prime-radiant/prime-radiant-inc.github.io" },
    { id: "alltheagents-org", path: "git/prime-radiant/alltheagents-org" },
    { id: "superpowers", path: "git/superpowers" },
    { id: "deslop", path: "git/deslop" },
    { id: "shepherd-pr", path: "git/shepherd-pr" },
    { id: "house", path: "git/house" },
    { id: "copy-writing", path: "git/copy-writing" },
    { id: "home", path: "" },
  ];

  const categories = [
    { id: "release", name: "Release" },
    { id: "research", name: "Research" },
  ];

  const P10 = defaultPlugins;
  const base = (o) => Object.assign({
    host: "magic-kingdom", project: "evener", model: "deepseek-4.1-flash", effort: "xhigh",
    plugins: P10, access: "Workspace write", branch: "main", live: true, archived: false,
    tasks: null, goal: null, notes: null, attachments: [], subs: null, unseen: false,
    usage: { in: 38.2e6, out: 1.1e6, cache: 29.4e6 }, cost: "~$11.40", workSec: 2 * H, ctx: { used: 96, window: 256 }, failedTools: 0,
  }, o);

  const sessions = [
    // Needs you
    base({ id: "s-retry", title: "Fix Endless Provider Retry Loop", state: "failed", host: "paradise-park", model: "gpt-5.6", effort: "high", ago: 2 * M, started: 3 * H,
      why: "Failed: codex-jesse-fsck.com sign-in expired (401)", subs: { run: 0, wait: 1, fail: 1, done: 2 }, cost: "~$6.80", branch: "fix-retry-loop",
      usage: { in: 11.4e6, out: 0.42e6, cache: 8.1e6 }, ctx: { used: 188, window: 400 }, failedTools: 5 }),
    base({ id: "s-audit", title: "Audit Tool Descriptions for Implied Options", state: "question", model: "glm-5.3-vision", ago: 14 * M, started: 55 * M,
      why: "Asks: keep or drop the implied options?", subs: { run: 0, wait: 0, fail: 0, done: 8 }, cost: "~$3.10", ctx: { used: 71, window: 200 },
      usage: { in: 8.8e6, out: 0.31e6, cache: 6.2e6 } }),
    base({ id: "s-mirror", title: "Mirror Docs Site Locally", state: "approval", project: "prime-radiant-inc.github.io", model: "deepseek-4.1-flash", effort: "high", ago: 21 * M, started: 34 * M,
      why: "Wants to write outside the workspace: ~/sites/docs", plugins: ["superpowers", "elements-of-style", "superpowers-chrome"], cost: "~$0.40",
      usage: { in: 1.2e6, out: 0.05e6, cache: 0.9e6 }, ctx: { used: 22, window: 256 } }),
    base({ id: "s-namer", title: "Tune Session Namer Token Cap", state: "restart", model: "glm-5.3-flash", effort: "medium", ago: 47 * M, started: 2 * H,
      why: "Needs a restart to finish updating", cost: "~$0.20", usage: { in: 0.9e6, out: 0.04e6, cache: 0.6e6 }, ctx: { used: 18, window: 128 } }),

    // Your move (finished, unseen)
    base({ id: "s-hier", title: "Host Project Hierarchy UI Mockups", state: "yourmove", unseen: true, model: "glm-5.3-vision", ago: 62 * M, started: 3 * H,
      why: "Three layouts are ready for review. I recommend B: hosts as badges inside projects…",
      attachments: [{ kind: "Plan", path: "docs/superpowers/plans/2026-09-25-host-project-hierarchy.md" }, { kind: "Artifact", id: "a-hier", title: "Hierarchy layouts" }],
      subs: { run: 0, wait: 0, fail: 0, done: 6 }, cost: "~$5.90" }),
    base({ id: "s-flakes", title: "Find Test Flakes in GitHub Issues", state: "yourmove", unseen: true, model: "gpt-5.6-luna", effort: "high", ago: 8 * M, started: 70 * M,
      why: "I found 11 open flake reports. Four are the same race in delegate settle…",
      attachments: [{ kind: "Doc", path: "docs/design/flake-triage.md" }], subs: { run: 0, wait: 0, fail: 0, done: 5 }, cost: "~$2.70" }),
    base({ id: "s-jobdisp", title: "Redesign Failed Job Display", state: "yourmove", unseen: true, model: "glm-5.3-vision", ago: 25 * M, started: 4 * H, category: "release",
      why: "Done. Failed jobs now show the exit code and the last 20 lines inline. PR #2331 is green.",
      subs: { run: 0, wait: 0, fail: 1, done: 11 }, cost: "~$8.20" }),
    base({ id: "s-branch", title: "Idiomatic Branch Naming for Delegates", state: "yourmove", unseen: true, model: "glm-5.3", ago: 3 * H, started: 6 * H,
      why: "Spec updated with your naming rules. Ready for your review.",
      attachments: [{ kind: "Spec", path: "docs/superpowers/specs/2026-09-22-delegate-lane-branch-names.md" }], subs: { run: 0, wait: 0, fail: 0, done: 3 }, cost: "~$1.90" }),

    // Working
    base({ id: "s-pr2138", title: "Get PR 2138 Test Clean", state: "working", ago: 5, started: 38 * M, category: "release",
      activity: "Waiting on 31 subagents", subs: { run: 31, wait: 3, fail: 2, done: 18 }, tasks: { done: 3, total: 7, current: "Fix the settle/drain race" },
      goal: { text: "PR 2138 green on CI without skipped tests", status: "active" }, cost: "~$19.60", usage: { in: 46.1e6, out: 1.9e6, cache: 38.8e6 }, ctx: { used: 38, window: 256 },
      attachments: [{ kind: "Plan", path: "docs/superpowers/plans/2026-09-25-settle-race.md" }], notes: { human: "Don't skip or quarantine tests. Fix causes.", agent: "Race is in retirement drain vs tree settle; see plan." } }),
    base({ id: "s-tasklist", title: "Rework Inline Task List Display", state: "working", model: "glm-5.3-vision", ago: 3, started: 12 * M,
      activity: "Running go test ./cmd/evener-hub/...", subs: { run: 3, wait: 0, fail: 0, done: 1 }, tasks: { done: 1, total: 4, current: "Fold the task card to one line" }, cost: "~$1.30" }),
    base({ id: "s-stumble", title: "Diagnose and Fix Tool Use Stumbles", state: "working", model: "glm-5.2-vision", ago: 8, started: 65 * M,
      activity: "Editing agent/tool_repair.go", subs: { run: 4, wait: 0, fail: 0, done: 5 }, cost: "~$7.70" }),
    base({ id: "s-gateway", title: "Design Gateway Token Command MVP", state: "working", model: "muse-spark-1.3", effort: "high", ago: 2, started: 22 * M,
      activity: "Thinking", plugins: ["superpowers", "elements-of-style", "go"], cost: "~$2.10" }),
    base({ id: "s-wasm", title: "Port Allocator to WASM Target", state: "working", project: "c-to-wasm", host: "paradise-park", model: "k3", effort: "high", ago: 12 * M, started: 51 * M, stuck: true,
      activity: "Running make test-wasm", subs: { run: 2, wait: 0, fail: 0, done: 1 }, plugins: ["superpowers", "go", "iterative-development"], cost: "~$1.60" }),
    base({ id: "s-resume", title: "Fix Missing Prompt on Session Resume", state: "working", ago: 4, started: 6 * M,
      activity: "Reading agent/session_resume.go", cost: "~$0.30" }),
    base({ id: "s-readintent", title: "Fix Missing File Read Intent Lines", state: "working", model: "glm-5.3-vision", ago: 6, started: 17 * M,
      activity: "Running make lint", subs: { run: 1, wait: 0, fail: 0, done: 2 }, cost: "~$1.10" }),
    base({ id: "s-landing", title: "Draft Agent Directory Landing Copy", state: "working", project: "alltheagents-org", model: "gpt-6-astra", effort: "high", ago: 9, started: 9 * M,
      activity: "Writing site/index.md", plugins: ["superpowers", "elements-of-style", "frontend-design"], cost: "~$0.90" }),
    base({ id: "s-sdk", title: "Lift Ask Dock Into Shared Client", state: "working", host: "paradise-park", model: "glm-5.3-vision", ago: 11, started: 29 * M,
      activity: "Running npm test in appwire-client/typescript", subs: { run: 2, wait: 0, fail: 0, done: 4 }, cost: "~$2.40" }),

    // Idle (seen)
    base({ id: "s-diff", title: "Evener Differentiation Rationale Doc", state: "idle", model: "glm-5.3-vision", ago: 2 * H, started: 5 * H, category: "research", why: "", cost: "~$1.20" }),
    base({ id: "s-deslop", title: "Deslop README Pass", state: "idle", project: "deslop", model: "gpt-5.5", ago: 5 * H, started: 6 * H, why: "", cost: "~$0.60" }),
    base({ id: "s-shepherd", title: "Shepherd PR Stack Rebase", state: "idle", project: "shepherd-pr", ago: 7 * H, started: 9 * H, why: "", cost: "~$0.80" }),

    // Shut down, not live (appear under Projects)
    base({ id: "s-roster", title: "Hub Roster Latency", state: "shutdown", live: false, ago: 1 * D, started: 1 * D + 3 * H }),
    base({ id: "s-sandbox", title: "Sandbox Network Egress Audit", state: "shutdown", live: false, ago: 2 * D, started: 2 * D + 2 * H }),
    base({ id: "s-skills", title: "Skills Lifecycle Cleanup", state: "shutdown", live: false, project: "superpowers", ago: 3 * D, started: 3 * D + H }),
    base({ id: "s-house", title: "Thermostat Schedule Script", state: "shutdown", live: false, project: "house", host: "paradise-park", ago: 4 * D, started: 4 * D + H }),
    base({ id: "s-copy", title: "Tighten Pricing Page Copy", state: "shutdown", live: false, project: "copy-writing", ago: 5 * D, started: 5 * D + H }),
    base({ id: "s-wasm2", title: "WASM Linker Symbol Clash", state: "shutdown", live: false, project: "c-to-wasm", host: "paradise-park", ago: 2 * D, started: 2 * D + 4 * H }),

    // Test runs (sessions started by the test suite against a disposable hub)
    base({ id: "s-test1", title: "Live Stack Smoke: Session on Host", state: "shutdown", live: false, test: true, project: "hub-test-env", ago: 20 * H, started: 20 * H + 600 }),
    base({ id: "s-test2", title: "Retirement Browser Guard Run", state: "shutdown", live: false, test: true, project: "hub-test-env", ago: 26 * H, started: 26 * H + 600 }),
    base({ id: "s-test3", title: "Spawn Guard Fixture Session", state: "shutdown", live: false, test: true, project: "hub-test-env", ago: 30 * H, started: 30 * H + 600 }),

    // Archived
    base({ id: "s-gocache", title: "Investigate Go Test Caching", state: "shutdown", live: false, archived: true, ago: 6 * D, started: 6 * D + 2 * H, model: "deepseek-4.1-flash" }),
    base({ id: "s-fuzz", title: "Harvest Fuzz Corpus Across Modules", state: "shutdown", live: false, archived: true, ago: 8 * D, started: 9 * D, subs: { run: 0, wait: 0, fail: 12, done: 455 } }),
    base({ id: "s-audit1", title: "Audit Tool Descriptions First Pass", state: "shutdown", live: false, archived: true, ago: 9 * D, started: 9 * D + H }),
    base({ id: "s-pairing", title: "Pairing Endpoint Review", state: "shutdown", live: false, archived: true, ago: 12 * D, started: 12 * D + H }),
    base({ id: "s-daemon", title: "Daemon Idle Retirement Timer", state: "shutdown", live: false, archived: true, ago: 15 * D, started: 15 * D + H }),
  ];

  // Subagents for the coordinators that have them. Big swarm is generated.
  const swarmNames = [
    "Reproduce TestRetirementTreeSettle locally", "Bisect settle ordering change", "Check drain ordering in tests", "Audit delegate settle callers",
    "Run agent tests under -race", "Run hubcore tests under -race", "Check appwire projector ordering", "Stabilize TestFoldPublicationMarkers",
    "Trace retirement drain wakeups", "Verify job tree revisions", "Run cmd/evener-hub browser guards", "Check interrupt marker ownership",
    "Explore retirement drain callers", "Review settle lock scope", "Run linux -race on agent", "Check TestQueueRetirement flake",
    "Read CI logs for run 34717544502", "Compare failing seeds", "Check compaction fold timing", "Audit watch timer shutdown",
    "Run jobstore descendant merge tests", "Check delegate attention flags", "Stabilize TestTranscriptPaging", "Probe navigation invalidation order",
    "Run make lint on changed modules", "Check go vet windows tags", "Replay failing job trees", "Measure settle pass duration",
    "Check restart continuity path", "Verify cancel queued on retire", "Run hub relay tests", "Inspect secret-scan output",
    "Check stacked merge duplication", "Run appwire codec tests", "Verify ask pending clears", "Run TUI transcript suite",
    "Check mutation outbox replay", "Run native shared-session tests", "Stabilize TestRelayCloseFrame", "Check projector item paging",
    "Run fuzz smoke on tool args", "Verify steering injection order", "Check delegate budget accounting", "Run workspace isolation tests",
    "Verify lane branch disposal", "Check CI cache keys", "Run makefile audits", "Check TestHostAttachRetry",
    "Verify session namer fallback", "Run SDK package qualification", "Check provider retry caps", "Run docs freshness check",
  ];
  const swarm = [];
  swarm.push({ id: "g-settle", title: "Fix race in tree settle", state: "failed", model: "glm-5.3-vision", lane: "fix-settle-race", ago: 6 * M, elapsed: 21 * M, tokens: "1.2M",
    line: "Failed: go test exited 1 (3 times)", children: [{ id: "g-settle-1", title: "Check drain ordering in tests", state: "running", model: "deepseek-4.1-flash", ago: 20, elapsed: 4 * M, tokens: "210K", line: "Reading agent/retirement_test.go" }] });
  swarm.push({ id: "g-repro", title: "Reproduce TestRetirementTreeSettleDrains", state: "failed", model: "deepseek-4.1-flash", ago: 9 * M, elapsed: 14 * M, tokens: "640K", line: "Failed: could not reproduce in 200 runs" });
  for (let i = 0; i < 31; i++) {
    const n = swarmNames[(i + 3) % swarmNames.length];
    swarm.push({ id: "g-run-" + i, title: n, state: "running", model: i % 4 === 0 ? "glm-5.3-vision" : "deepseek-4.1-flash", ago: 5 + (i * 7) % 90, elapsed: (2 + (i * 5) % 30) * M,
      tokens: (120 + (i * 37) % 900) + "K", lane: i % 6 === 0 ? "lane-" + (i + 1) : null,
      line: ["Running go test ./agent/...", "Reading agent/delegate_runtime.go", "Editing agent/retirement.go", "Searching for settleTree", "Running go test -race ./internal/hubcore", "Thinking"][i % 6] });
  }
  for (let i = 0; i < 3; i++) {
    swarm.push({ id: "g-wait-" + i, title: swarmNames[(i + 40) % swarmNames.length], state: "waiting", model: "deepseek-4.1-flash", ago: (3 + i) * M, elapsed: (8 + i) * M, tokens: "300K", line: "Finished; waiting for the coordinator to read its report" });
  }
  for (let i = 0; i < 18; i++) {
    swarm.push({ id: "g-done-" + i, title: swarmNames[(i + 20) % swarmNames.length], state: "done", model: "deepseek-4.1-flash", ago: (10 + i * 2) * M, elapsed: (5 + i % 9) * M, tokens: (90 + i * 23) + "K", line: ["Tests pass", "No race found in this path", "Fixed and verified", "Report written"][i % 4] });
  }

  const subagents = {
    "s-pr2138": swarm,
    "s-retry": [
      { id: "r-1", title: "Find where retries are scheduled", state: "done", model: "gpt-5.6", ago: 2 * H, elapsed: 9 * M, tokens: "380K", line: "Found the loop in llm/retry.go" },
      { id: "r-2", title: "Write a test for the 429 loop", state: "done", model: "gpt-5.6", ago: 90 * M, elapsed: 12 * M, tokens: "410K", line: "Test reproduces the endless retry" },
      { id: "r-3", title: "Cap retries with backoff", state: "failed", model: "gpt-5.6", ago: 3 * M, elapsed: 18 * M, tokens: "520K", line: "Failed: provider sign-in expired" },
      { id: "r-4", title: "Check other providers for the same loop", state: "waiting", model: "gpt-5.6", ago: 4 * M, elapsed: 6 * M, tokens: "150K", line: "Finished; waiting for the coordinator" },
    ],
    "s-tasklist": [
      { id: "t-1", title: "Update TaskCard tests", state: "running", model: "glm-5.3-vision", ago: 4, elapsed: 5 * M, tokens: "160K", line: "Running npm test" },
      { id: "t-2", title: "Update the browser guard", state: "running", model: "glm-5.3-vision", ago: 9, elapsed: 4 * M, tokens: "120K", line: "Editing scripts/layoutguard/run.mjs" },
      { id: "t-3", title: "Check the Tasks panel", state: "running", model: "deepseek-4.1-flash", ago: 7, elapsed: 3 * M, tokens: "95K", line: "Reading TasksPanel.tsx" },
      { id: "t-4", title: "Measure card height", state: "done", model: "deepseek-4.1-flash", ago: 6 * M, elapsed: 2 * M, tokens: "40K", line: "Card is 212pt today; one line is 36pt" },
    ],
  };
  function genericSubs(prefix, s) {
    const out = [];
    const c = s.subs || { run: 0, wait: 0, fail: 0, done: 0 };
    const pick = (i) => swarmNames[(prefix.length * 7 + i * 3) % swarmNames.length];
    let k = 0;
    for (let i = 0; i < c.fail; i++) out.push({ id: prefix + k, title: pick(k++), state: "failed", model: s.model, ago: 20 * M, elapsed: 9 * M, tokens: "300K", line: "Failed: tests still failing after 3 attempts" });
    for (let i = 0; i < c.run; i++) out.push({ id: prefix + k, title: pick(k++), state: "running", model: s.model, ago: 10, elapsed: 4 * M, tokens: "180K", line: "Working" });
    for (let i = 0; i < c.wait; i++) out.push({ id: prefix + k, title: pick(k++), state: "waiting", model: s.model, ago: 2 * M, elapsed: 6 * M, tokens: "120K", line: "Finished; waiting for the coordinator" });
    for (let i = 0; i < c.done; i++) out.push({ id: prefix + k, title: pick(k++), state: "done", model: s.model, ago: 40 * M, elapsed: 7 * M, tokens: "150K", line: "Report written" });
    return out;
  }
  for (const s of sessions) {
    if (s.subs && !subagents[s.id]) subagents[s.id] = genericSubs(s.id + "-g", s);
  }
  subagents["s-hier"] = [
    { id: "h-a", title: "Mock layout A: project, then host", state: "done", model: "glm-5.3-vision", ago: 80 * M, elapsed: 11 * M, tokens: "420K", line: "Layout A mock written" },
    { id: "h-b", title: "Mock layout B: host badges in projects", state: "done", model: "glm-5.3-vision", ago: 78 * M, elapsed: 12 * M, tokens: "460K", line: "Layout B mock written" },
    { id: "h-c", title: "Mock layout C: host, then project", state: "done", model: "glm-5.3-vision", ago: 77 * M, elapsed: 10 * M, tokens: "400K", line: "Layout C mock written" },
    { id: "h-d", title: "Count sessions per project and host", state: "done", model: "deepseek-4.1-flash", ago: 2 * H, elapsed: 3 * M, tokens: "80K", line: "88% of sessions are in evener" },
    { id: "h-e", title: "Review the three mocks", state: "done", model: "gpt-5.6", ago: 70 * M, elapsed: 6 * M, tokens: "210K", line: "B wins on scan time" },
    { id: "h-f", title: "Write the plan", state: "done", model: "glm-5.3-vision", ago: 65 * M, elapsed: 5 * M, tokens: "130K", line: "Plan written" },
  ];

  // Transcripts. Item types: time, user, agent, act, think, sub, doc, art, sys, qhist, err, ask, appr
  const T = {};
  T["s-audit"] = [
    { t: "time", label: "Today 1:58 PM" },
    { t: "user", text: "Audit every tool description in agent/internal/tool/definitions.go for options the tool doesn't actually accept. List them and fix the descriptions." },
    { t: "agent", md: "I'll read all 61 tool definitions and check each description against the tool's real argument schema. I'm splitting the tools across subagents so this goes quickly." },
    { t: "act", steps: [
      { i: "Read the tool definitions", g: "agent/internal/tool/definitions.go", s: "ok", out: "1,412 lines, 61 tools" },
      { i: "Found every argument schema", g: "grep -n \"Parameters:\" agent/internal/tool", s: "ok" },
      { i: "Started 8 subagents to check tool groups", g: "delegate × 8", s: "ok" },
    ] },
    { t: "sub", id: "s-audit-g0", title: "Check file tools", state: "done", line: "5 implied options in file tools" },
    { t: "sub", id: "s-audit-g1", title: "Check job tools", state: "done", line: "4 implied options in job tools" },
    { t: "sub", id: "s-audit-g2", title: "Check delegate tools", state: "done", line: "3 implied options in delegate tools" },
    { t: "agent", md: "## What I found\n\n14 descriptions mention options their tools don't accept. The worst three:\n\n| Tool | Implied option | Accepted |\n|---|---|---|\n| `read_file` | `--force` | no |\n| `job_watch` | `timeout_ms` | no |\n| `delegate` | `priority` | no |\n\nThere are two reasonable fixes and they change behavior differently, so I'd like your call." },
    { t: "ask", id: "q-audit" },
  ];
  const ASKS = {
    "q-audit": [
      { header: "Implied options", q: "Keep or drop the implied options?", why: "Fourteen descriptions mention flags the tools don't accept, like --force on read_file. Models try them and fail.", multi: false,
        opts: [{ l: "Drop them", d: "Remove the implied options from all 14 descriptions", rec: true }, { l: "Keep them and add the flags", d: "Implement the 9 missing flags; about 400 lines" }, { l: "Ask me per tool", d: "I'll list each one for a yes or no" }] },
      { header: "Next audit", q: "Which tool groups should I audit next?", why: "I can check the rest the same way while you review.", multi: true,
        opts: [{ l: "Job tools", d: "job_watch, job_status, job_list, job_stop", rec: true }, { l: "Delegate tools", d: "delegate, delegate_send" }, { l: "Search tools", d: "grep, glob, search" }, { l: "Notes tools", d: "notes and urls" }] },
    ],
    "q-gateway": [
      { header: "Token storage", q: "Where should the gateway token command store tokens?", why: "Both work on Linux and macOS; the keychain is safer but needs a helper on headless hosts.", multi: false,
        opts: [{ l: "System keychain", d: "Safer; falls back to a file on headless hosts", rec: true }, { l: "A file under ~/.config/evener", d: "Simplest; mode 0600" }, { l: "Both, configurable", d: "More code and more tests" }] },
    ],
  };

  T["s-hier"] = [
    { t: "time", label: "Today 11:52 AM" },
    { t: "user", text: "Mock up three ways to show hosts and projects together in the sidebar, and recommend one. Write it up as a plan." },
    { t: "agent", md: "I'll count where sessions actually live first, then have three subagents mock one layout each, and a fourth review them side by side." },
    { t: "act", steps: [
      { i: "Counted sessions per project and host", g: "evener/navigation/read", s: "ok", out: "evener: 88%  c-to-wasm: 9%  other: 3%\nmagic-kingdom: 91%  paradise-park: 9%" },
      { i: "Started 4 subagents", g: "delegate × 4", s: "ok" },
    ] },
    { t: "sub", id: "h-a", title: "Mock layout A: project, then host", state: "done", line: "Layout A mock written" },
    { t: "sub", id: "h-b", title: "Mock layout B: host badges in projects", state: "done", line: "Layout B mock written" },
    { t: "sub", id: "h-c", title: "Mock layout C: host, then project", state: "done", line: "Layout C mock written" },
    { t: "time", label: "Today 1:24 PM" },
    { t: "act", steps: [
      { i: "Wrote the plan", g: "docs/superpowers/plans/2026-09-25-host-project-hierarchy.md", s: "ok", diff: { file: "2026-09-25-host-project-hierarchy.md", add: 142, del: 0 } },
      { i: "Published the comparison artifact", g: "artifact_publish · Hierarchy layouts v3", s: "ok" },
    ] },
    { t: "doc", path: "docs/superpowers/plans/2026-09-25-host-project-hierarchy.md", kind: "Plan" },
    { t: "art", id: "a-hier" },
    { t: "agent", md: "Three layouts are ready for review. I recommend **B**: hosts as badges inside projects. 88% of your sessions live in one project, so a host-first tree puts almost everything one level deeper.\n\nThe plan has the reasoning and a six-step rollout. The artifact lets you compare the three side by side and pick one." },
  ];

  T["s-pr2138"] = [
    { t: "time", label: "Today 2:14 PM" },
    { t: "user", text: "PR 2138 has 6 failing tests on CI. Get it clean without skipping anything." },
    { t: "agent", md: "I'll pull the failing runs, group the failures by cause, and give each cause its own subagent. I'll keep the goal as *green without skipped tests*." },
    { t: "act", steps: [
      { i: "Read the CI failures", g: "gh run view 34717544502 --log-failed", s: "ok", out: "--- FAIL: TestRetirementTreeSettleDrainsPendingRootAttention (0.41s)\n--- FAIL: TestFoldPublicationMarkersLandInOrder (0.12s)\n--- FAIL: TestQueueRetirementCancelsQueued (0.08s)\nFAIL\tgithub.com/prime-radiant-inc/evener/agent\t41.2s" },
      { i: "Grouped 6 failures into 3 causes", g: "notes", s: "ok" },
      { i: "Ran the agent tests", g: "go test ./agent/... -run 'Retirement|Fold' -count=5", s: "fail", out: "--- FAIL: TestRetirementTreeSettleDrainsPendingRootAttention (0.44s)\n    retirement_test.go:212: settle finished before drain: got state idle, want draining\nFAIL\nexit status 1" },
      { i: "Edited the settle pass", g: "agent/retirement.go", s: "ok", diff: { file: "agent/retirement.go", add: 18, del: 4, lines: [
        ["ctx", "func (t *tree) settle(ctx context.Context) error {"],
        ["del", "\tt.mu.Lock()"],
        ["del", "\tdefer t.mu.Unlock()"],
        ["add", "\tif err := t.drain.wait(ctx); err != nil {"],
        ["add", "\t\treturn fmt.Errorf(\"settle: wait for drain: %w\", err)"],
        ["add", "\t}"],
        ["add", "\tt.mu.Lock()"],
        ["add", "\tdefer t.mu.Unlock()"],
        ["ctx", "\tfor _, n := range t.pending {"],
      ] } },
    ] },
    { t: "sys", text: "Context compacted · 412K → 38K tokens" },
    { t: "doc", path: "docs/superpowers/plans/2026-09-25-settle-race.md", kind: "Plan" },
    { t: "agent", md: "The flaky tests share one cause: a race between the **tree settle pass** and the **retirement drain**. Both take the tree lock, and settle can finish before drain has seen the pending work.\n\nI've written the fix up as a plan and I'm splitting it across subagents: one fixes the race, others run every affected package under `-race` to prove it." },
    { t: "sub", id: "g-settle", title: "Fix race in tree settle", state: "failed", line: "Failed: go test exited 1 (3 times)" },
    { t: "sub", id: "g-run-0", title: "Run agent tests under -race", state: "running", line: "Running go test -race ./agent/..." },
    { t: "user", text: "Also make sure the race test runs under -race on Linux, not just macOS.", kind: "steer" },
    { t: "agent", md: "Good call. I added a Linux `-race` run to the plan and gave it its own subagent on magic-kingdom." },
    { t: "act", live: true, steps: [
      { i: "Started 31 subagents for -race runs and fixes", g: "delegate × 31", s: "ok" },
      { i: "Waiting on subagents", g: "31 running · 3 waiting", s: "run" },
    ] },
  ];

  T["s-retry"] = [
    { t: "time", label: "Today 11:40 AM" },
    { t: "user", text: "Sessions on codex get stuck retrying forever when the provider returns 429. Find the loop and cap it." },
    { t: "agent", md: "I'll find where provider retries are scheduled, write a test that reproduces the endless loop, then cap retries with backoff." },
    { t: "sub", id: "r-1", title: "Find where retries are scheduled", state: "done", line: "Found the loop in llm/retry.go" },
    { t: "sub", id: "r-2", title: "Write a test for the 429 loop", state: "done", line: "Test reproduces the endless retry" },
    { t: "agent", md: "The loop is in `llm/retry.go`: a 429 resets the attempt counter instead of incrementing it, so it never gives up. The test reproduces it. Now capping retries at 5 with exponential backoff." },
    { t: "act", steps: [
      { i: "Edited the retry loop", g: "llm/retry.go", s: "ok", diff: { file: "llm/retry.go", add: 9, del: 3, lines: [
        ["ctx", "for attempt := 0; ; attempt++ {"],
        ["del", "\tif resp.StatusCode == 429 { attempt = 0 }"],
        ["add", "\tif attempt >= maxRetries {"],
        ["add", "\t\treturn nil, fmt.Errorf(\"gave up after %d attempts: %w\", attempt, lastErr)"],
        ["add", "\t}"],
      ] } },
      { i: "Ran the retry tests", g: "go test ./llm/... -run Retry", s: "fail", out: "llm: provider codex-jesse-fsck.com: 401 Unauthorized: sign-in expired\nFAIL\tgithub.com/prime-radiant-inc/evener/llm\t0.9s" },
    ] },
    { t: "sub", id: "r-3", title: "Cap retries with backoff", state: "failed", line: "Failed: provider sign-in expired" },
    { t: "err", e: "codex-jesse-fsck.com sign-in expired (401)", d: "The session stopped after the provider refused 5 requests in a row. Sign in again, then retry.", acts: ["signin", "retry"] },
  ];

  T["s-mirror"] = [
    { t: "time", label: "Today 2:08 PM" },
    { t: "user", text: "Mirror the docs site into ~/sites/docs so I can browse it offline." },
    { t: "agent", md: "I'll fetch the site map and write each page under `~/sites/docs`. That folder is outside this session's workspace, so I'll need your approval to write there." },
    { t: "act", steps: [
      { i: "Fetched the site map", g: "curl -s https://docs.prime-radiant.com/sitemap.xml", s: "ok", out: "214 URLs" },
      { i: "Planned the folder layout", g: "~/sites/docs/…", s: "ok" },
    ] },
    { t: "appr", id: "ap-mirror" },
  ];
  const APPROVALS = {
    "ap-mirror": { what: "Wants to write outside the workspace", tool: "write_file", target: "~/sites/docs/index.html", mode: "Sandbox: workspace write" },
  };

  T["s-namer"] = [
    { t: "time", label: "Today 12:10 PM" },
    { t: "user", text: "The session namer keeps running out of tokens on GLM Flash before it writes JSON. Raise the cap or change the prompt." },
    { t: "agent", md: "The cap is 80 tokens and GLM Flash spends most of it thinking. I'll try a tighter prompt first, then raise the cap to 160 if needed." },
    { t: "sys", text: "The hub was updated to 0.9.412" },
    { t: "err", e: "Needs a restart to finish updating", d: "This session is running an older version than the hub. Restart it to continue; nothing is lost.", acts: ["restart"] },
  ];

  T["s-tasklist"] = [
    { t: "time", label: "Today 2:40 PM" },
    { t: "user", text: "The inline task card in the transcript is too tall. Make it one line that expands, and keep the current task visible." },
    { t: "agent", md: "I'll fold `TaskCard` into one summary line (*→ Fold the task card · 1 of 4*) that expands in place, and keep the full list in the Tasks panel. Subagents will update the tests and the browser guard." },
    { t: "act", steps: [
      { i: "Read the task card", g: "cmd/evener-hub/frontend/src/panes/session/transcript/TaskCard.tsx", s: "ok" },
      { i: "Folded the card to one line", g: "TaskCard.tsx", s: "ok", diff: { file: "TaskCard.tsx", add: 48, del: 112, lines: [
        ["del", "  return <section className={styles.card}>{rows}</section>;"],
        ["add", "  if (!open) return <button className={styles.line} onClick={expand}>"],
        ["add", "    → {current.title} · {done} of {total}"],
        ["add", "  </button>;"],
      ] } },
      { i: "Ran the frontend tests", g: "npm test -- TaskCard", s: "ok", out: "✓ 14 tests passed" },
    ] },
    { t: "sub", id: "t-1", title: "Update TaskCard tests", state: "running", line: "Running npm test" },
    { t: "sub", id: "t-2", title: "Update the browser guard", state: "running", line: "Editing scripts/layoutguard/run.mjs" },
    { t: "act", live: true, steps: [{ i: "Running the hub tests", g: "go test ./cmd/evener-hub/...", s: "run" }] },
  ];

  T["s-flakes"] = [
    { t: "time", label: "Today 1:45 PM" },
    { t: "user", text: "Find every open flake report in GitHub issues and tell me which ones are the same bug." },
    { t: "act", steps: [{ i: "Listed flake issues", g: "gh issue list --label flake --state open", s: "ok", out: "11 issues" }, { i: "Started 5 subagents to group them", g: "delegate × 5", s: "ok" }] },
    { t: "doc", path: "docs/design/flake-triage.md", kind: "Doc" },
    { t: "agent", md: "I found 11 open flake reports. **Four are the same race in delegate settle** (#2121, #2263, #1394, #1418); two are cold-cache CI noise; five are unrelated one-offs. The triage doc groups them and proposes one fix per group." },
  ];

  T["s-jobdisp"] = [
    { t: "time", label: "Today 10:31 AM" },
    { t: "user", text: "Failed jobs in the transcript just say 'failed'. Show the exit code and the last lines of output inline." },
    { t: "act", steps: [{ i: "Edited the job row", g: "JobRow.tsx", s: "ok", diff: { file: "JobRow.tsx", add: 64, del: 20 } }, { i: "Ran the tests", g: "npm test", s: "ok", out: "✓ 212 passed" }, { i: "Opened PR #2331", g: "gh pr create", s: "ok" }] },
    { t: "agent", md: "Done. Failed jobs now show the exit code and the last 20 lines of output inline, with a link to the full log. PR #2331 is open and green." },
  ];

  T["s-branch"] = [
    { t: "time", label: "Today 8:40 AM" },
    { t: "user", text: "Update the lane branch naming spec with my rules: kebab case, no dates, max 40 characters, prefix with the parent's short name." },
    { t: "doc", path: "docs/superpowers/specs/2026-09-22-delegate-lane-branch-names.md", kind: "Spec" },
    { t: "agent", md: "Spec updated with your naming rules. Ready for your review." },
  ];

  T["s-gateway"] = [
    { t: "time", label: "Today 2:30 PM" },
    { t: "user", text: "Design the smallest useful version of an `evener gateway token` command." },
    { t: "agent", md: "I'll look at how the hub issues tokens today and sketch the command surface before writing anything." },
    { t: "act", steps: [{ i: "Read the auth token code", g: "auth/token.go", s: "ok" }] },
    { t: "think", live: true },
  ];

  T["s-gocache"] = [
    { t: "time", label: "Sep 19, 3:12 PM" },
    { t: "user", text: "Why does go test re-run packages that didn't change? Investigate the test cache misses in CI." },
    { t: "act", steps: [
      { i: "Compared two CI runs of the same commit", g: "gh run view --log", s: "ok" },
      { i: "Checked the test cache key inputs", g: "go help testflag; go env GOFLAGS", s: "ok", out: "GOFLAGS=-count=1 -mod=readonly" },
    ] },
    { t: "agent", md: "Found it. The cache key includes GOFLAGS, and CI sets -count=1 in some jobs, which disables caching for every package in that job. The race jobs set it on purpose; the lint job inherited it by accident.\n\nRemoving it from the lint job's environment brings its test step from 6m to 40s." },
  ];

  function generic(s) {
    return [
      { t: "time", label: "Today" },
      { t: "user", text: s.title.replace(/^./, (c) => c) + "." },
      { t: "agent", md: "Starting on this now. I'll read the relevant code first and report back." },
      { t: "act", live: s.state === "working", steps: [
        { i: "Read the relevant files", g: s.project === "evener" ? "agent/…" : "src/…", s: "ok" },
        { i: s.activity || "Worked on the change", g: "", s: s.state === "working" ? "run" : "ok" },
      ] },
    ].concat(s.state === "idle" ? [{ t: "agent", md: "Finished. Summary is above." }] : []);
  }
  for (const s of sessions) if (!T[s.id]) T[s.id] = generic(s);

  const docs = {
    "docs/superpowers/plans/2026-09-25-host-project-hierarchy.md": { kind: "Plan", ago: 62 * M, lines: 142, changed: [3, 9, 14], md: `# Host and project hierarchy in the sidebar

Sessions can run on several hosts, and every session belongs to a project. The sidebar needs to show both without making either one hard to find.

## What the data says

- 88% of sessions are in one project (evener).
- 91% run on magic-kingdom; the rest on paradise-park.
- People look for a session by project far more often than by host.

## Three layouts

**A. Project, then host.** Each project expands into its hosts, and each host into sessions. Accurate, but every session sits two levels down.

**B. Host badges inside projects.** Projects expand straight into sessions. Each session row carries a small host badge, and a filter narrows to one host.

**C. Host, then project.** Hosts at the top level. Good for fleet health, poor for finding work, because 91% of everything lands under one host.

## Recommendation

Use **B** as the default and keep **A** and **C** as the "Organize by" options. B keeps sessions one level down, and the badge answers "where is this running?" at a glance.

## Rollout

1. Add the host badge to session rows when more than one host exists.
2. Add a host filter chip to the Projects section header.
3. Keep "Organize by" with A and C as the alternatives.
4. Show offline hosts' sessions dimmed, with the host badge in amber.
5. Measure time-to-open for the ten most recent sessions before and after.
6. Remove the extra nesting level from the default layout.

## Risks

- Badges add width to every row. Keep them to the host's short name and hide them when only one host exists.
- Some people think host-first. The toggle stays.

## Open questions

- Should the badge show when the session's host is the hub's own host?
- Do archived sessions keep their host badge?
` },
    "docs/superpowers/plans/2026-09-25-settle-race.md": { kind: "Plan", ago: 21 * M, lines: 64, changed: [], md: `# Fix the settle/drain race

## Problem

The retirement drain and the tree settle pass both take the tree lock. When settle runs first, it can mark the tree idle before the drain has seen pending work, so the root's attention is never delivered.

## Fix

1. Settle waits for the drain to finish before it takes the tree lock.
2. The drain signals completion through a channel, not a shared flag.
3. Add a regression test that forces settle to run first.

## Proof

- Run every affected package under \`-race\` on macOS **and** Linux.
- Run the three flaky tests 200 times each with \`-count=200\`.
- No skipped or quarantined tests.

## Subagents

| Work | Subagents |
|---|---|
| Fix the race | 1 |
| -race runs, macOS | 14 |
| -race runs, Linux | 14 |
| Flake loops | 3 |
` },
    "docs/design/flake-triage.md": { kind: "Doc", ago: 8 * M, lines: 38, changed: [], md: `# Flake triage, 2026-09-25

## Same race in delegate settle (4)

- #2121 TestRetirementTreeSettleDrainsPendingRootAttention
- #2263 unexplained root-module FAIL on a cold cache
- #1394 agent race job hits the time cap
- #1418 relay close frame drops

**Fix:** one change to the settle ordering (see the settle-race plan).

## Cold-cache CI noise (2)

- #141 timing baseline blessed under load
- #144 timing budget re-runs the suite

**Fix:** re-bless quietly; source durations from the gate's own run.

## Unrelated one-offs (5)

Each needs its own look. Listed in the issue comments.
` },
    "docs/superpowers/specs/2026-09-22-delegate-lane-branch-names.md": { kind: "Spec", ago: 3 * H, lines: 51, changed: [2, 3], md: `# Delegate lane branch names

## Rules

- Kebab case, lowercase ASCII only.
- No dates.
- At most 40 characters.
- Prefix with the parent session's short name: \`pr2138-fix-settle-race\`.

## Collisions

When a name is taken, append \`-2\`, \`-3\` and so on.

## Disposal

Unchanged lanes are deleted, merged lanes are deleted after merge, and kept lanes stay until the parent is archived.
` },
  };

  const artifactHtml = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{color-scheme:light;font-family:-apple-system,system-ui,sans-serif;color:#252521;background:#fff}
body{margin:0;padding:16px 16px 28px}
h1{font-size:20px;margin:0 0 4px} p.s{margin:0 0 14px;color:#5f5f57;font-size:14px;line-height:1.4}
.tabs{display:flex;gap:6px;margin-bottom:12px}
.tabs button{flex:1;height:36px;border-radius:10px;border:1px solid #ddd;background:#f6f5f1;font:600 15px system-ui}
.tabs button.on{background:#252521;color:#fff;border-color:#252521}
.mock{border:1px solid #e3e2da;border-radius:12px;padding:10px;background:#fcfbf8;font-size:13px}
.r{padding:6px 8px;border-radius:6px;margin:2px 0;display:flex;justify-content:space-between;gap:6px}
.p{font-weight:600} .h{color:#6d6d64;padding-left:18px} .s1{padding-left:18px} .s2{padding-left:34px}
.b{font:600 10px system-ui;background:#e8f1ff;color:#0064c2;border-radius:5px;padding:1px 5px}
.note{margin-top:10px;color:#5f5f57;font-size:13px;line-height:1.4}
.pick{margin-top:14px;width:100%;height:44px;border-radius:22px;border:0;background:#0285ff;color:#fff;font:600 16px system-ui}
.pick.done{background:#189a4d}
textarea{width:100%;box-sizing:border-box;margin-top:12px;border:1px solid #ddd;border-radius:10px;padding:8px;font:15px system-ui;min-height:64px}
label{display:block;margin-top:12px;font:600 13px system-ui;color:#5f5f57}
</style></head><body>
<h1>Hierarchy layouts</h1>
<p class="s">Three ways to show hosts and projects together. Pick one to send your choice to the session.</p>
<div class="tabs"><button data-k="A">A</button><button data-k="B" class="on">B</button><button data-k="C">C</button></div>
<div id="m" class="mock"></div>
<div id="n" class="note"></div>
<label for="t">Note for the agent (optional)</label>
<textarea id="t" placeholder="Anything to change?"></textarea>
<button id="go" class="pick">Choose B</button>
<script>
var L={A:{t:'Project, then host',rows:[['p','evener',''],['h','magic-kingdom','12'],['s2','Get PR 2138 Test Clean',''],['s2','Rework Inline Task List',''],['h','paradise-park','2'],['s2','Fix Endless Provider Retry Loop','']],n:'Accurate, but every session sits two levels down.'},
B:{t:'Host badges inside projects',rows:[['p','evener',''],['s1','Get PR 2138 Test Clean','mk'],['s1','Rework Inline Task List','mk'],['s1','Fix Endless Provider Retry Loop','pp'],['p','c-to-wasm',''],['s1','Port Allocator to WASM','pp']],n:'Sessions stay one level down; the badge says where each runs.'},
C:{t:'Host, then project',rows:[['p','magic-kingdom',''],['h','evener','12'],['s2','Get PR 2138 Test Clean',''],['p','paradise-park',''],['h','evener','2'],['h','c-to-wasm','1']],n:'Good for fleet health; poor for finding work.'}};
var cur='B';
function draw(){var d=L[cur],h='<div style="font:600 12px system-ui;color:#6d6d64;margin-bottom:6px">'+d.t.toUpperCase()+'</div>';
d.rows.forEach(function(r){h+='<div class="r '+r[0]+'"><span class="'+(r[0]==='p'?'p':'')+'">'+r[1]+'</span>'+(r[2]?(r[0]==='h'?'<span style="color:#6d6d64">'+r[2]+' live</span>':'<span class="b">'+r[2]+'</span>'):'')+'</div>';});
document.getElementById('m').innerHTML=h;document.getElementById('n').textContent=d.n;
document.getElementById('go').textContent='Choose '+cur;
Array.prototype.forEach.call(document.querySelectorAll('.tabs button'),function(b){b.className=b.getAttribute('data-k')===cur?'on':'';});
try{parent.postMessage({type:'state',state:{selected:cur}},'*');}catch(e){}}
Array.prototype.forEach.call(document.querySelectorAll('.tabs button'),function(b){b.addEventListener('click',function(){cur=b.getAttribute('data-k');draw();});});
document.getElementById('go').addEventListener('click',function(){var note=document.getElementById('t').value.trim();
var text='Go with layout '+cur+' ('+L[cur].t.toLowerCase()+').'+(note?' '+note:'');
parent.postMessage({type:'ui/message',text:text},'*');});
draw();parent.postMessage({type:'ready'},'*');
</script></body></html>`;

  const artifacts = {
    "a-hier": { id: "a-hier", title: "Hierarchy layouts", summary: "Compare three layouts for hosts and projects, and pick one.", version: 3, ago: 62 * M, html: artifactHtml, session: "s-hier" },
  };

  const search = [
    { id: "s-gocache", kind: "session", title: "Investigate Go Test Caching", project: "evener", ago: 6 * D, archived: true, prompt: "Why does go test re-run packages that didn't change? Investigate the test cache misses in CI." },
    { id: "s-gocache", kind: "hit", title: "Investigate Go Test Caching", ago: 6 * D, snippet: "…the cache key includes GOFLAGS, and CI sets -count=1 in some jobs, which disables caching for every package in that job…" },
    { id: "s-roster", kind: "session", title: "Hub Roster Latency", project: "evener", ago: 1 * D, prompt: "The roster takes 4 seconds to load on the phone. Find out why." },
    { id: "s-fuzz", kind: "session", title: "Harvest Fuzz Corpus Across Modules", project: "evener", ago: 8 * D, archived: true, prompt: "Harvest fuzz corpora from every module and dedupe them." },
    { id: "s-pr2138", kind: "hit", title: "Get PR 2138 Test Clean", ago: 20 * M, snippet: "…a race between the tree settle pass and the retirement drain. Both take the tree lock…" },
    { id: "s-sandbox", kind: "session", title: "Sandbox Network Egress Audit", project: "evener", ago: 2 * D, prompt: "Audit which tools can reach the network under restricted sandbox mode." },
  ];

  window.EV_DATA = { hosts, providers, models, marketplaces, plugins, recipes, projects, categories, sessions, subagents, transcripts: T, asks: ASKS, approvals: APPROVALS, docs, artifacts, search, defaultPlugins, archivedTotal: 271 };
})();
