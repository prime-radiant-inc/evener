# Self-Compaction by Choice — Prior Art

Research feeding the design of an **agent-invoked compact tool** for serf: a tool the
model itself decides to call at a task boundary, as opposed to the harness-triggered
auto-compaction serf already runs. See [`context.md`](./context.md) for the existing
subsystem this builds on.

## What serf has today (the baseline)

serf already does the industry-standard thing. The default `compact` strategy
(`agent/internal/contextmgr`) auto-compacts on context pressure with two prefix-replacing
layers:

| Layer | Threshold | Method |
|-------|-----------|--------|
| Checkpoint | >= 0.80 | Deterministic state snapshot (`TurnCheckpoint`) |
| LLM Summarize | >= 0.90 | Cheap-model narrative summary (`TurnSummary`) |

Both preserve the most recent `PreserveRecentTurns` (6) turns. `ForceCompact` already
exists as the user-initiated `/compact` (runs both layers unconditionally). The
masking/thinking-clearing layers were **removed from the default path** because mid-history
mutation busts prefix prompt caches across all providers — a first-order constraint for any
new design (they survive only in experimental strategies).

**Key takeaway:** the question is not "should serf compact" — it does. The question is
whether handing the *timing decision* (and the *summary authorship*) to the model improves
on pressure-threshold triggering.

## The landscape — who decides timing

| System | Who decides | Mechanism |
|---|---|---|
| **LangChain Deep Agents SDK** | **Model** (+ 85% fallback) | Self-callable compaction tool the agent invokes at "opportune" moments; auto-compact at 85% is a backstop |
| **MemGPT / Letta** | **Hybrid** | "Memory-pressure" system message at ~70% prompts the LLM to save state via function calls; hard FIFO flush at 100% is the fallback |
| **Anthropic memory tool** | **Model** | Agent-initiated file ops (distinct from Anthropic's compaction, which is automatic) |
| **Letta sleep-time compute** | **Separate background agent** | Deliberately *removes* memory tools from the primary agent; a second agent compacts every N steps |
| **Claude Code, Cline, Factory.ai, Anthropic SDK/API** | **Harness** | Threshold trigger; "the model never calls a tool or explicitly requests compaction" |

**Agent-by-choice compaction is the minority pattern.** Almost every shipped coding agent
auto-compacts at a token threshold with zero model agency over timing.

### The two most transferable patterns for serf

1. **Self-callable compact tool** (Deep Agents) — a tool registered through the existing
   `Strategy.Tools()` hook, layered over the threshold auto-compactor as a backstop.
2. **Memory-pressure warning + hard flush** (MemGPT) — inject a warning-threshold steering
   message ("you're at N%, consolidate now") and keep the auto-compactor as the forced
   fallback. This is the single closest prior art to a "compact at a stopping point" design,
   and it maps onto serf's existing `PreCompact` steering-injection machinery.

## 1. Decision policy — *when* should it choose?

Deep Agents is the only source that enumerates concrete "opportune moments," worth
stealing directly:

- At **task / subtask boundaries** (natural seams where detail stops mattering)
- **After extracting results** from a large context (you have the answer; drop the raw material)
- **Before consuming substantial new input** (make room deliberately)
- **Before a complex multi-step operation** (start it with a clean, focused context)

**Honest caveat (decision-relevant):** the claim that *"agents are conservative and
well-calibrated about when to self-compact"* was **refuted 0-3** in adversarial
verification. There is **no published evidence** that agents pick good moments. The
"opportune moments" list is a design aspiration from a vendor blog, not a measured result.
Shipping a self-compact tool bets on calibration nobody has demonstrated — so agent
calibration must be treated as a hypothesis to *measure*, not assume.

## 2. Tool / interface design — what the agent sees

Canonical handoff format (Anthropic cookbook; their example is harness-triggered, but the
*format* is reusable):

- Entire history including all tool results is **replaced** by a single summary wrapped in
  `<summary></summary>` tags.
- Summary holds: completed-work records, progress status, key patterns, next steps.
- **Deliberately retained:** structured identifiers — IDs, categories, outcomes, statuses.
- **Deliberately dropped:** full bodies — article text, drafted responses, detailed reasoning.

**Recovery of dropped detail** is where better designs differ. MemGPT pairs compaction with
**tiered storage** — evicted detail goes to archival/recall databases the agent can later
`search()` back in. Pure summarize-and-replace (Claude Code, Cline) has *no* recovery path.
serf's `TurnCheckpoint`/`TurnSummary` model is a head start; the open question is whether
dropped turns remain searchable.

## 3. Evidence it works — read skeptically

**Genuinely supported:** bounded/compressed context improves **task success, not just
cost**, because unbounded context degrades reasoning. ACON (ICML 2026, Microsoft): −26–54%
peak tokens *while improving* success; up to +46% for smaller models "by mitigating context
distraction." Corroborated by Chroma's Context Rot study across 18 models (a single
distractor measurably hurts; four compound).

**NOT supported (the biggest caveat):** there is **zero clean evidence that
agent-*chosen* timing beats threshold auto-compaction.** No source tests it head-to-head.
The efficacy evidence is for *compression in general*, not for *who pulls the trigger*.
MemGPT's headline accuracy gain (32% → 92.5%) as proof self-management helps was **refuted
1-2** — the baseline was a deliberately lossy fixed-context summarizer, an unfair comparison.

**Failure modes any self-compact tool must guard against** (verified 3-0; hit regardless of
who triggers):

- **Loss of structured high-value detail** — decisions, code snippets, root causes get
  flattened into prose. Claude Code #17237: "there's no way to extract or preserve this data
  before it gets summarized." Even PreCompact hooks can only back up the transcript
  *externally*, not preserve detail *within* the compacted context.
- **Brevity bias** — concise summaries drop the domain insight the task needed.
- **Context collapse** — iterative full-context rewriting erodes detail over successive
  compactions. ACE's quantified case: 18,282 tokens/66.7% acc collapsed to 122 tokens/57.1%
  acc — *below* the no-compaction baseline. Cause: "monolithic rewriting of context by an
  LLM." Mitigation: **delta/append updates to a structured record, not full rewrites.**

## Implications for serf

1. **Measure against the threshold baseline, not "no compaction."** serf already compacts;
   the new variable is model agency over timing + summary authorship.
2. **An agent-authored summary directly attacks brevity bias and structured-detail loss.**
   The agent that did the work writing what-to-keep/what-to-clear is strictly better-informed
   than a post-hoc cheap-model summarizer — and is itself somewhat novel vs. the prior art,
   where summaries are auto-generated. This is the strongest reason to prefer a self-compact
   tool over the status-quo Layer 2.
3. **Prefer the MemGPT hybrid shape over a pure self-callable tool.** A warning-threshold
   nudge + the existing auto-compactor as a hard fallback gives agency *without* betting the
   session on the model never forgetting to compact (which #1 says it might).
4. **Fix the failure modes regardless of trigger:** append/delta over monolithic rewrite
   (kills context collapse); keep cleared turns recoverable (tiered/searchable) rather than
   destroyed (addresses structured-detail loss). A full-prefix-replacing self-compact is
   cache-compatible (it replaces the whole old prefix), consistent with serf's cache
   constraint.

## Sources (all adversarially verified)

- [LangChain Deep Agents — autonomous context compression](https://www.langchain.com/blog/autonomous-context-compression)
- [MemGPT, arXiv:2310.08560](https://arxiv.org/pdf/2310.08560)
- [Anthropic — context management](https://www.anthropic.com/news/context-management)
- [Anthropic cookbook — automatic context compaction](https://platform.claude.com/cookbook/tool-use-automatic-context-compaction)
- [Anthropic cookbook — context-engineering tools (memory tool)](https://platform.claude.com/cookbook/tool-use-context-engineering-context-engineering-tools)
- [Letta — sleep-time compute](https://www.letta.com/blog/sleep-time-compute)
- [Factory.ai — compressing context](https://factory.ai/news/compressing-context)
- [Cline — auto-compact](https://docs.cline.bot/features/auto-compact)
- [ACON, arXiv:2510.00615](https://arxiv.org/abs/2510.00615)
- [ACE, arXiv:2510.04618](https://arxiv.org/abs/2510.04618)
- [Claude Code #17237 — preserve structured data before compaction](https://github.com/anthropics/claude-code/issues/17237)

## Open questions (carried into design)

- Does agent-chosen timing actually beat threshold auto-compaction? (Untested anywhere.)
- Right division of labor: self-callable tool, memory-pressure warning + hard flush,
  background sleep-time agent, or self-managed external memory with recoverable tiered
  storage?
- How to preserve structured detail *within* the post-compaction context?
- How to avoid context collapse under repeated self-compaction (delta vs. monolithic rewrite)?
