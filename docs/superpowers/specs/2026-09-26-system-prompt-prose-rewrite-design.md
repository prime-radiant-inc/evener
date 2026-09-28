# System prompt prose rewrite (part 2): design

Date: 2026-09-26. Approved section by section in conversation with Jesse; this document is for his review.

This is part 2 of the system prompt cleanup. Part 1 (`docs/superpowers/specs/2026-09-26-system-prompt-collapse-design.md`, on its way to `main`) collapses the prompt into one template and replaces the prose-pinning tests with assembly tests. Part 3 rewrites the role prompts (issue #2401, bundled-agent tests that pin role prose).

Appendices, both compiled by research agents for this design:

- `2026-09-26-system-prompt-prose-rewrite/rule-inventory.md` traces every rule in the behavioral prose to the commit and incident that added it.
- `2026-09-26-system-prompt-prose-rewrite/research.md` surveys the research and vendor guidance on writing agent system prompts, with 51 sources. Two of its central claims were checked against the sources; they are marked below.

## Goal

Evener's system prompt carries about 30KB of behavioral prose. Most of it was written one rule at a time, after benchmark failures, and it reads that way: dense sentences, abstract jargon, explanations of machinery, rules repeated across sections, and a heavy load of stylistic tics. The agents copy that style into their own writing.

The rewrite aims at two outcomes, weighted equally:

1. The agent's writing and reasoning read plainly. Sentences are short, words are plain, the outcome comes first, and tasks, issues, and files are called by their names.
2. The agent behaves better. It wastes fewer steps, verifies soundly, and hands its work back intact.

## What we know

### The rules

The inventory traced all 201 rules in the behavioral sections:

- 95 state general engineering practice. No incident stands behind them in the history.
- 68 were written after a specific failure: an eval trial, a live-model probe, a production bug, or a review finding.
- 36 explain machinery. Most of it repeats the tool descriptions. The ask-user section nearly restates its own tool's description.
- Seven topics are stated more than once. Advice to stop looping appears in three sections.
- Two pairs of rules pull against each other. "Send every user-facing message through the result tool" meets "a watch frame that needs no action needs only a short internal disposition." The rule protecting deliverables from cleanup has collided before with tasks whose end state requires teardown.
- A few machinery facts exist only in the prompt: how shell output is truncated and summarized, the pipefail-and-tee idiom for keeping a full log, and the job-watch safeguard against self-triggering loops.
- Some guidance is stale. "Prefer `rg`" predates the dedicated grep and glob tools.

Earlier work reached the same point from the other side. Issue #367 (doctrine recitation without application) found that failing agents quoted, word for word, the rule that would have prevented their failure. The rules were present and understood, and they did nothing at the moment of decision. Issue #395 (the doctrine backlog) measured the noise floor: between identical runs of the same binary, 6 to 10 of about 15 benchmark tasks flip.

### The agents' prose

An audit read 52 of the 125 sessions on Jesse's Mac closely and scanned all 197,000 words of agent prose in them:

- About 60% of the messages to Jesse from gpt-5.6-sol and DeepSeek carry an identifier nobody explains. In one session, "Task 1" through "Task 5" appear in 55 of 87 messages.
- Jargon traces to our own text. "Durable job" and "No action required" come from the background-jobs section. "The parent must rerun" and "environmental blockage" come from the verification section. "Lane" comes from the tool descriptions.
- DeepSeek delegates write dense reports, with a median of 313 words and about 17 em dashes per 1,000 words. The prompt itself carries 5.6 per 1,000 words.
- gpt-5.6-sol writes short, plain sentences but reports constantly: 87 messages for 3 requests.
- Jesse's CLAUDE.md reaches only root sessions (issue #2579). A DeepSeek root that had its writing rules broke three of them anyway.
- The ask-user section tells the agent to batch up to four questions in one call. Jesse's instructions say to ask one at a time.

The larger corpus on the host magic-kingdom, about 3,200 sessions that include the GLM and gpt-5.6-luna models, is not yet audited.

### The research

The research agrees on direction:

- Adherence falls as instructions accumulate. On real agent prompts averaging about 1,700 words, the best model tested met about 60% of the constraints. Over 30% of the failures on conditional rules were triggers the model never noticed (AgentIF, NeurIPS 2025).
- OpenAI's prompt guidance for GPT-5.6 reports that leaner system prompts improved its coding-agent evaluation scores by roughly 10 to 15% while cutting tokens by 41 to 66%. (Checked against the source.)
- Anthropic's guidance for Opus 5 says to remove explicit verification instructions, because they cause over-verification. It also says positive examples of the wanted communication style work better than instructions about what to avoid. (Checked against the source.)
- The vendors agree on the rest: the smallest prompt that covers the job, each point stated once with its reason, emphasis kept for one or two real invariants, and contradictions resolved in the text.
- Models echo the formatting of their prompts. Trained house style resists, so tics fall only modestly: an explicit ban on em dashes worked on Claude and only partly on GPT-4.1.
- Smaller models are the most sensitive to instruction density, negation, and the quality of guidance.
- With 15 tasks and one run each, a real difference can hide in the noise. Pooled across seven models, 5 to 9 runs per task for each prompt version can detect a change on one task.

## The new prompt

### Shape

- It opens with a persona, then one short paragraph on how evener writes, with two short examples: a status update and a final report.
- The body is guidance, organized by area: how to work, delegating, background work, git, and finishing and reporting. Each area gets a few plain paragraphs that say what to do, when, and why, the way one would brief a colleague.
- It keeps the facts about evener that a model cannot infer, and the incident lessons whose failures could still recur. Each kept lesson names a situation the agent will recognize and what to do in it.
- It drops:
  - the machinery the tool descriptions already explain;
  - practice statements that current models already follow, starting with instructions to verify and to be thorough;
  - the duplicates;
  - all emphasis except one or two real invariants, such as destructive git commands.
- It resolves each colliding pair in the text, saying which guidance wins and when.
- It will likely land near a third of today's size. The content decides; the estimate is a check on the draft.

### Voice

The prompt is written in the voice it asks for: plain paragraphs, with no em dashes, bolded labels, arrows, or capitals for emphasis. Its paragraph on writing describes what Jesse wants and draws on his definition of slop:

- Name the task, issue, or file instead of citing its identifier. The reader will not remember what the identifier meant.
- Lead with the outcome.
- Put one idea in each sentence.
- Use plain words where jargon would be shorter.

The paragraph stays positive and leans on the examples. A named tic enters the prompt only if it survives the rewrite and the measurements show it.

### Personas

The transcripts choose among these candidates:

- A. The journalist turned engineer. Evener spent years as a newspaper reporter after a BA in journalism from Wesleyan, then earned a master's in computer science. It writes for a busy reader.
- B. The principal engineer whose design docs and incident reports the team actually reads, because they are specific and plain.
- C. A control with no persona, carrying only the paragraph on writing.

The set stays open. More personas can join a later round.

### Scope

In scope: the behavioral sections from identity through ask-user, the tool guidance, the non-interactive text, and the prose that only delegates see. The tool descriptions that receive facts moving out of the prompt are in scope too.

Out of scope: the role prompts (issue #2401), the generated data blocks, whether delegates receive Jesse's instruction files (issue #2579), and reminders delivered by the runtime at the moment they apply. The research favors those reminders over prose rules; they are a mechanism change for later work.

## How we test it

- **Runner.** The tool-fluency runner (`tools/tool-fluency/cmd/evener-fluency`) already builds evener, runs task manifests with repetitions for each model, accepts a chosen binary (`--evener-bin`) and task directory (`--probes-dir`), and saves the transcripts. The rewrite's tasks get their own directory and README. Any runner change they need goes into the runner itself, for example a check command that runs after the agent finishes.
- **Prompt versions.** Each version is a commit on an experiment branch off `main` that edits the section files, and the runner uses that commit's binary. The baseline is `main` at the branch point. Each transcript records the exact prompt that produced it.
- **Models.** Every run goes through the lunarouter gateway, which charges a flat fee and caps concurrent requests. The models are Opus 5.5, Opus 5, Sonnet, gpt-5.6-luna, deepseek-4.1-flash, glm-5.3-vision, and glm-5.3-flash if the gateway serves it. A one-turn smoke run for each model confirms its name first. Concurrency starts low and rises until the gateway pushes back.
- **Tasks.** About eight, drawn from the scenario cards and the incidents behind the rules, so that every lesson we keep or cut gets exercised:
  1. a bug fix with tests;
  2. an investigation that ends in a written report to the user;
  3. a feature split across delegates;
  4. a long build or test run in the background;
  5. a git task: branch, commit, and merge;
  6. a research-and-report task;
  7. an ambiguous request with no one to ask;
  8. a task whose deliverable must be handed back intact.
- **Runs.** Three for each task, model, and prompt version. Pooled across the seven models, that is 21 runs per task for each version.
- **Measures.**
  - Tics and opaque identifiers per 1,000 words of agent prose, for each model and version.
  - Message length, and messages per request.
  - The task pass rate, including how often a task passes on every one of its runs.
  - A blind read of the transcripts against a rubric fixed before the runs and built from the audit's findings. The reader does not know which version produced a transcript and does not rely on the agent's own account of its choices. Jesse reads a sample.
- **Order.** The personas are chosen on the flash models first, since they are the most sensitive and have the most room to improve. The full matrix then runs the baseline against the finalists.

## How it lands

- The experiments start now, in parallel with part 1's remaining pull requests. The experiment branch never merges.
- The task manifests and runner changes land first, in their own pull request, because part 3 reuses them.
- After part 1's collapse lands and a version wins, one pull request moves the winning text into `agent/prompts/system.md.tmpl` and moves the facts that only the prompt held into their tool descriptions. Part 1's render comparison shows the exact change for each configuration.
- Part 1's assembly tests and the tool-mention sweep keep guarding the structure. No test asserts prose.
- The rewrite is done when, on the pooled runs, the new version cuts tics and opaque identifiers per 1,000 words on every model family, holds or raises the task pass rate, and wins the blind read, and Jesse reads a sample and agrees. If the pruned rewrite stalls short of that, a blank-page rewrite is the next candidate.

## Decisions

These were settled in conversation with Jesse:

1. Plain writing and better behavior carry equal weight.
2. Rewrite and prune first; consider a blank-page rewrite afterward.
3. Write guidance, with reasons, in place of rules.
4. Remove every restatement of a tool description.
5. Personas are candidates the transcripts choose among, and the set stays open.
6. Every run goes through the lunarouter gateway.
7. The rewrite picks its own tasks, informed by the audit of past sessions.

## Open items

- The exact model names on lunarouter, and whether it serves glm-5.3-flash. The smoke runs settle both.
- The gateway's concurrency cap. Raising concurrency until the gateway pushes back finds it.
- The audit of the magic-kingdom sessions. It waits on Jesse's decision about access for the audit agent.
- The flash models may need guidance that Claude does not. Every cut is checked against them first.
