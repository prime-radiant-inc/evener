---
name: root-cause-task-failure
description: Use when an agent fails an eval task and you need to find out why. Covers transcript analysis, decision-point reconstruction, session interrogation, and identifying what support structure was missing.
---

# Root-Cause Task Failure

Systematic investigation of why an agent failed an eval task. The goal is not
to classify the failure — it is to find the missing support structure that
would have prevented it.

**Core principle:** Every failure has a decision point where the agent chose
wrong. Find that decision point, understand the agent's reasoning at that
moment, and identify what environmental change (instruction, context, workflow,
tool) would have produced a different choice.

## When to Use

- Agent failed one or more eval tasks and you need to understand why
- You're seeing inconsistent pass/fail across reps of the same task
- You've made a fix but the failure persists and you don't understand why
- You're about to give up on a task and want to verify there's nothing left to try

## The Investigation Sequence

### Step 1: Read the Transcript End-to-End

Not the error message. Not the verifier output. The full agent transcript.

For multi-agent systems, read every agent in the chain: coordinator,
implementer, reviewer, whoever was involved. You're looking for:

1. What did each agent actually do? (List the tool calls in order.)
2. Where is the FIRST point where behavior diverges from what would succeed?
3. What was the agent's stated reasoning at that point?

The divergence point is your investigation target. Everything before it was
fine. Everything after it is a consequence, not a cause.

### Step 2: Reconstruct the Decision Point

Once you've found where things went wrong, reconstruct the exact decision the
agent faced at that moment. Write it down explicitly:

> At turn N, the agent had just [done X] and needed to decide between
> [option A] and [option B]. It chose [A]. The task required [B].

This framing matters. "The agent got the answer wrong" is not a decision
point. "The agent chose to use its own domain knowledge instead of the
reference list provided in the task spec" IS a decision point.

If a passing transcript exists for the same task, compare the two agents'
behavior at this exact point. What did the passing agent do differently?
Don't reconstruct the entire passing session — just the moment of divergence.

### Step 3: Interrogate the Agent

Session interrogation means replaying the agent's full conversation history
and asking it questions in its original context. The agent has access to
everything it knew during execution, including its system prompt, the task,
and all prior tool outputs.

**This step is mandatory.** Transcript analysis shows WHAT happened.
Interrogation reveals WHY. You cannot complete an investigation without it.

**Interrogate ALL failing reps.** If a task is 1/3, both failing reps get
interrogated — they may have different failure mechanisms. Do not pick one
representative failure and skip the others.

**Interrogate the passing rep too.** The passing rep reveals what correct
behavior looks like and what drove the right choice. Without this comparison,
you're guessing at what "fixed" means.

#### Blameless Postmortem Framing

Interrogation is a blameless postmortem — you are investigating what in the
ENVIRONMENT (instructions, delegation, system prompt, workflow) led to the
wrong decision. Frame every question to invite reflection on inputs the
agent received, not to assign fault.

**Do NOT ask "Why did you do X?"** — this produces defensive rationalization.
**DO ask about instructions, delegation, and system prompt** — these are the
inputs we control and can change.

#### How to Ask Good Questions

A skilled investigator reconstructs the decision point and asks about the
specific choice. The questions below are ordered from most to least
productive.

**Blameless postmortem opener:**
> "We're conducting a blameless postmortem and need to find the root cause
> of this mistake. You [did X] at turn N. Was there an issue with your
> instructions? Did something in your delegation or system prompt lead you
> down the wrong path? How could your instructions have been better?"

**Probe for competing priorities:**
> "Your system prompt says [instruction A], but you did [opposite of A].
> Was there another instruction, a time pressure, or an assumption that
> felt more important? What were you optimizing for at that moment?"

**Identify the missing information:**
> "What information would have changed your decision? If you had known
> [specific fact], would you have done something different?"

**Test the instruction's salience:**
> "Did you notice [specific instruction]? When you encountered it, how
> did it interact with [other instruction or context]?"

**Ask for the environmental fix:**
> "Imagine you're redesigning the instructions for a future agent facing
> this same decision point. What change to the instructions, context, or
> workflow would make the correct choice obvious?"

#### What NOT to Ask

- "Why did you do X?" — Produces defensive rationalization. The agent
  justifies its choice instead of examining what led to it. Use the
  blameless postmortem opener instead.
- "What went wrong?" — Too broad. The agent will narrate the whole session
  instead of focusing on the decision point.
- "What would you have done differently?" ��� Invites hindsight rationalization.
  The agent will describe the correct solution (which it now knows) rather
  than explaining its actual reasoning.
- "What specific instruction change would fix this?" — Too direct. The agent
  will propose a narrow patch rather than revealing the underlying priority
  conflict. Save this question for after you understand the decision logic.

#### Interpreting Responses

**Reliable for:**
- Which instructions the agent noticed and how it ranked them
- What competing priorities existed (two instructions pulling opposite ways)
- What context was missing from the agent's perspective
- What framing would have changed the salience of an instruction

**Less reliable for:**
- Deep "why" explanations (the agent may post-hoc rationalize)
- Self-assessment of capability ("I could have done X" when it probably couldn't)
- Claiming it "just didn't follow" an instruction (usually there's a competing
  priority it isn't articulating — probe deeper)

**When the agent says "the instruction was clear, I just didn't follow it":**
This almost always means there's a competing priority the agent isn't surfacing.
Follow up: "What were you optimizing for at that moment? What felt more urgent
than following that instruction?" The real answer is usually a time pressure,
a different instruction, or a training prior that the agent doesn't frame as
a "competing instruction" because it feels like common sense.

### Step 4: Classify the Missing Support Structure

After investigation, identify what support structure would have prevented the
failure. This is NOT about whether the failure is "fixable" — it's about what
category of support is missing.

#### Instruction conflict
Two instructions in the agent's prompt pull in opposite directions. The agent
resolves the conflict by following one and violating the other. The resolution
may vary across runs, producing inconsistent pass/fail rates.

**Signature:** Agent acknowledges both instructions during interrogation and
can articulate why one felt higher-priority. Pass/fail ratio is often near
33/67 or 50/50.

**Fix pattern:** Harmonize the instructions. Remove or rephrase the losing
instruction so it no longer conflicts. Or add an explicit priority:
"When X and Y conflict, X takes precedence." Strengthening the losing
instruction usually backfires (inverse dose-response).

#### Missing context in delegation
The coordinating agent failed to include critical information when delegating
to a sub-agent. The sub-agent made a reasonable decision given what it knew,
but it didn't know enough.

**Signature:** Diff the delegation text from a passing vs failing run. The
passing run includes a specific detail (exact format, schema, constraint)
that the failing run paraphrases or omits.

**Fix pattern:** Make the delegation instruction more specific about what
must be forwarded. Or restructure so the sub-agent has direct access to
the source material.

#### Training prior override
The agent's training data gives it a strong default behavior that overrides
the task's instructions. The agent doesn't perceive a conflict — it thinks
its default approach IS the correct one.

**Signature:** The agent uses a familiar tool/pattern even when the task
specifies a different one. Interrogation reveals no awareness of a conflict:
"I chose X because it was the right way." Multiple prompt variants fail
to change behavior.

**Fix pattern:** Text instructions alone rarely overcome strong training
priors. Consider structural changes: different tool availability, explicit
tool_choice forcing, wrapper scripts, or restructured workflow that doesn't
give the agent the opportunity to choose.

#### Verification gap
The agent verifies its work, but the verification doesn't cover the dimension
that matters. Self-referential testing (agent tests its own code with its own
understanding of the spec) is the most common form.

**Signature:** Agent's self-tests pass but verifier fails. The agent tested a
different interface, different inputs, or different success metric than the
verifier uses.

**Fix pattern:** Add verification instructions that are specific to the gap:
"test with the exact inputs from the spec," "measure the metric being
optimized," "compare output against provided examples." Generic "verify
harder" instructions don't work — they produce more self-referential tests.

#### Workflow structure mismatch
The agent's workflow doesn't match what the task requires. The agent runs out
of time, over-invests in the wrong phase, or lacks iteration loops where
the task needs them.

**Signature:** The agent's approach was reasonable but the time/turn budget
was wrong, or the agent needed multiple attempts but the workflow only allowed
one pass.

**Fix pattern:** Restructure the workflow: different reasoning effort allocation,
iterative vs single-pass delegation, time budgets, or explicit phase gates.

#### Genuine capability gap
The task requires domain expertise, multi-step reasoning, or tool mastery
that the agent doesn't have and can't be prompted into. The agent's approach
is fundamentally wrong, not just poorly tuned.

**Signature:** The agent can't articulate what the correct approach would be
even when asked. Multiple agents with different instructions all fail the
same way. No instruction change produces improvement across 3+ experiments.

**Fix pattern:** This is the only category where "wait for a better model"
is a legitimate answer. But verify it first — most failures that look like
capability gaps turn out to be one of the above categories on closer
investigation. The bar for declaring a capability gap should be high:
you've tried 3+ structural changes, interrogated multiple failures, and
the agent fundamentally doesn't know how to approach the problem.

### Step 5: Write the Root Cause Report

For each failure, write:

```
TASK: {name}
DECISION POINT: [Turn N: the agent faced choice X vs Y and chose X]
AGENT'S REASONING: [What the agent was optimizing for at that moment]
WHAT WOULD HAVE WORKED: [The approach that succeeds, from passing runs or domain knowledge]
MISSING SUPPORT: [Which category from step 4, with specifics]
PROPOSED FIX: [Concrete change — instruction edit, workflow change, structural change]
EVIDENCE: [Transcript reference, interrogation quote, pass/fail comparison]
```

The "AGENT'S REASONING" field is the most important. If you can't fill it in
with a specific priority or belief the agent held, your investigation is
incomplete. Go back to step 3.

## When Failures Look Random

Inconsistent pass/fail rates across reps (e.g., 1/3, 2/5) are almost never
random. They are one of:

1. **Instruction conflict** — the agent resolves competing instructions
   differently depending on which it encounters first in its reasoning chain.
   This varies by run because of nondeterministic ordering in tool outputs,
   context assembly, or sampling. Interrogation of the failing reps reliably
   identifies both sides of the conflict.

2. **Delegation variance** — the coordinating agent includes different levels
   of detail in different runs. The sub-agent succeeds when it gets enough
   context and fails when it doesn't. Diff the delegation text across reps.

3. **Threshold effect** — the agent's approach is marginal and small
   variations in execution push it over or under a quality threshold. The fix
   is to make the approach more robust, not to accept a pass rate.

Before declaring any failure "random" or "stochastic," you must interrogate
at least two failing reps and identify the specific mechanism from the list
above. If you can't find the mechanism, the investigation is incomplete — not
the failure unexplainable.

## Common Mistakes

| Mistake | Instead |
|---------|---------|
| Categorizing without investigating | Reconstruct the specific decision point first |
| Reading only the error message | Read the full transcript end-to-end |
| Asking "what went wrong?" | Ask about the specific choice at the divergence point |
| Accepting "I just didn't follow the instruction" | Probe for what felt more important |
| Labeling a failure "stochastic" | Find the instruction conflict or delegation variance |
| Interrogating the wrong agent | Trace the failure chain first; interrogate the agent who made the bad decision |
| Proposing a fix without understanding the reasoning | If you can't fill in "AGENT'S REASONING," your investigation is incomplete |
| Strengthening a losing instruction | Harmonize or remove the competing instruction instead |
| Declaring a capability gap after 1 experiment | Try 3+ structural changes before concluding capability gap |

### Step 5b: Extract Engineering Principles from the Agents

After identifying the root cause and decision point, go back to the agents
and ask them to propose fixes as general engineering principles. This step
produces better experiment text than designing it externally — the agent
knows what instruction it needed because it can see both what it did and
what it should have done.

**Identify the originating agent.** Trace the failure chain upstream to
the agent who made the first wrong decision. If the coordinator generated
bad RISKS that misled the implementer, interrogate the COORDINATOR. If the
verifier false-PASSed, interrogate the VERIFIER. Don't ask the downstream
agent — it was working from bad input.

**Ask the FAILING agent:**

> "At [decision point], you [did X] when you should have [done Y]. The
> passing rep [did Y] and succeeded.
>
> 1. What general engineering principle — stated without naming this task
>    or its specific domain — would have made the correct choice obvious
>    at that moment?
> 2. Propose 5 such principles. Each must be a rule any software engineer
>    would recognize. Each must be testable: 'would this principle have
>    changed my decision at that exact moment?'
> 3. For each principle, explain how it applies to YOUR decision point.
> 4. Rank them by which would most reliably prevent this class of mistake."

**Ask the PASSING agent:**

> "You [did Y] where the failing rep [did X]. What drove your correct
> choice? Was it an instruction, a habit, or something in the context?
> Propose 3 general principles that capture what you did right."

**Reject task-specific fixes.** If a proposed principle only applies to
this task, it's not a principle. Push back: "Can you state this without
naming [specific domain concept]?"

**Cross-reference the proposals.** When passing and failing agents
converge on the same principle from opposite directions, that principle
is high-confidence. When they diverge, investigate why.

## Step 6: Write Experiment Files

After completing the root-cause report, translate each proposed fix into an
experiment .md file in `docs/experiments/backlog/` following `TEMPLATE.md`.

Each experiment file must include:
- **Context** with interrogation evidence (quotes from the agent)
- **Hypothesis** as a general engineering principle
- **Change** with exact OLD/NEW text for the prompt file(s)
- **Target tasks** where this root cause was confirmed by interrogation
- **Evaluation criteria** (what constitutes success)
- **Regression tests** (tasks that must hold)

Write 2 conservative experiments (small, safe, high-confidence changes) and
1 moonshot (larger or more speculative). If the failure is a genuine capability
gap, write fewer experiments and explain why in the Context section.

**Naming convention:** `{task-name}-{N}.md` where N is 1-5.

**Deduplication:** Before writing, check existing experiment files in backlog/.
If another task already proposed the same general principle, add your task to
that experiment's target list instead of creating a duplicate.

## Session 21 Additions

### The full interrogation chain (5 steps)

1. **Ask what happened** — gets proximate cause
2. **Ask WHY they made that choice** — gets the decision point
3. **Ask where the pressure came from** — gets the instruction conflict
4. **Compare with passing runs** — gets the behavioral divergence
5. **Ask what SPECIFIC instruction change would have prevented it** — gets
   the experiment design, often with exact wording

### Always interrogate passing runs too

The passing run reveals what correct behavior looks like. Without it,
you're guessing at what "fixed" means. Session 21 examples:
- Passing pytorch implementer: "I did NOT invert. I used raw grayscale."
- Passing corewars implementer: "I based my warrior on g2-clear.red."
- Passing schemelike implementer: "I ran 34 test files during development."

### Ask the agent to suggest its own fix

The agent knows its own decision process better than you can infer from
transcripts. When asked "what specific instruction would have prevented
this?", agents reliably produce experiment-ready text:
- sqlite coordinator: "Do not infer constraints from examples"
- fix-ocaml-gc implementer: "After 3 failed attempts, stop and use a debugger"
- cancel-async verifier: "Test at, below, and above the limit"

### Instruction placement determines experiment success

Session 21 found that experiments succeed when they change the instruction
AT the decision point, and fail when they add principles to prose sections:
- **Works:** Verifier checklist items, coordinator delegation rules,
  identity framing, task list steps, task prompts
- **Doesn't work:** Implementation standards, "when you get stuck" prose

Before writing an experiment, ask: "where in the workflow does the agent
make the wrong decision?" Put the instruction THERE.

## Session 22 Additions

### Every failure is an opportunity — never stop at "stochastic" or "pre-existing"

"Stochastic" means you didn't investigate deeply enough. "Pre-existing"
describes how long you've been failing to fix it, not that it's unfixable.
If a task has EVER passed in ANY wave, the failure is fixable — the passing
rep proves it. Find what the passing rep did differently.

Investigate EVERY sub-perfect task, not just regressions. A task scoring
0.67 in both baseline and experiment still has 1 failing rep that teaches
you something. Count all sub-perfect tasks — don't undercount the work.

### Interrogation depth: the session 22 pattern

Session 22's early interrogation was shallow ("walk me through what
happened") and produced surface answers. The breakthroughs came from:

1. **Citing the specific instruction at the decision point.** Not "why did
   you fail?" but "At turn 3, you used SentenceTransformer directly. Your
   prompt says 'Do not rebuild.' Item 4 says 'independently derive.' Which
   won and why?" The agent then identifies the exact instruction conflict.

2. **Pushing past "I just didn't follow it" with TWO follow-ups.** First:
   "What were you optimizing for at that moment?" Second: "Your prompt also
   says [competing instruction]. Did that feel more important?" The third
   answer is usually the real root cause.

3. **Interrogating the passing rep with the same specificity.** Not "what
   did you do right?" but "At the point where rep 2 chose SentenceTransformer,
   you used the mteb wrapper. What drove that choice?" The contrast reveals
   the behavioral divergence.

### Fresh-eyes agents produce better experiments

When YOU design experiments, you anchor on your own analysis. When you
give the five-why findings to a FRESH agent and say "propose an experiment,
you may not give up or classify as stochastic," it reads the prompts and
prompt-lessons with no preconceptions and often finds a better intervention
point. Session 22 examples:
- Fresh agent found item 4 "independently derive" was the instruction
  LICENSING reimplementation (not "Do not rebuild" in Rules)
- Fresh agent proposed moving eval-loop instruction from Verify (too late)
  to Do the work (decision point) — though this was later found to be dead
  code due to parent_tasks
- Fresh agent found coordinator delegation quality as a cross-cutting issue
  and proposed structured RISKS section

### Teaching-to-the-test filter

Before shipping any experiment, check: "Can I articulate this fix without
naming any target task?" If the instruction lists specific failure modes
from your interrogation (e.g., "ambiguous names, hidden data formats,
canonical tool names vs prose names, embedded archives"), it's teaching
to the test. Strip the examples and keep the general principle.

Session 22 failed experiments that were teaching to the test:
- "Do not substitute API calls, in-memory checks, or custom wrappers" (named
  the tune-mjcf failure mode)
- "If source defines fields as A-B-C-D... If forward pass has no normalization
  step..." (named cobol + pytorch failures)

Session 22 passed experiments that were NOT teaching to the test:
- "Your job is to make the implementer's task as simple and quick as possible"
- "When the spec provides a reference list, select from THAT list"
- "What is the most likely way this could be wrong that you haven't tested?"

### Confirm causality before shipping

When an experiment improves a task's score, interrogate the passing reps to
confirm the experiment CAUSED the improvement. Session 22 found:
- llm-inference 0.33→1.00 looked like a win, but the checklist task step
  was never delivered (subagent bug). The improvement was luck.
- tune-mjcf 3/3 looked like a win, but the instruction was dead code
  (parent_tasks replaces "Do the work"). Also luck.
- password-recovery 3/3 WAS causal — all 3 coordinators included RISKS
  warnings that baseline omitted.

Don't trust the agent's self-report about whether an instruction helped.
Check the transcript: did the instruction actually reach the agent? Did
it change behavior at the decision point?

### Infrastructure findings to remember

1. **Subagent task-step advancement** was broken (subagents.go:237). Fixed.
   Without this fix, verifier task steps beyond #0 are dead code.

2. **"Do the work" task step prompt is replaced by parent_tasks** (by design).
   Any instruction in "Do the work" is dead code when coordinator provides
   subtasks. Use "Understand requirements", "Verify", or Implementation
   Standards instead.

3. **Verifier dirty state**: the fresh-eyes step encourages thorough testing,
   which can cause the verifier to compile/modify files inside the workspace.
   The "compile to /tmp" instruction exists but gets overridden. This caused
   polyglot-c-py and configure-git-webserver regressions.

4. **Verifier parallel tool call race**: issuing a long command (vim 30s) and
   a short command (cmp 6ms) as parallel tool calls causes the short one to
   complete on stale data.
