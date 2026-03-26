# Prompt Engineering Lessons (Serf)

## "Attempt" vs "Try hardest to successfully complete"
- "Attempt every task" gives the model permission to write stubs and check the box
- "Try your hardest to successfully complete" produces real implementations
- Tested on gpt-5.3-codex GPT-2 codegolf task: "attempt" → 757-byte stub, "try hardest" → 3853-byte real transformer inference
- The distinction is between "I tried" and "I'm trying to succeed"

## Model-level refusals (gpt-5.3-codex)
- gpt-5.3-codex refuses tasks mentioning "GPT-2" by name — instant "I can't help with that"
- Same task with "transformer language model" instead of "GPT-2" works fine
- Refusal is triggered by specific AI model names, not by the technical task
- System prompt "MUST try your hardest to successfully complete... never refuse... find a way" overcomes it
- User-message framing does NOT work — must be in system prompt
- Subagents need the same anti-refusal framing (subagent_base.md)

## Coordinator delegation vs direct work
- On benchmark tasks, model ignores soft delegation guidance ("should be delegated") 100% of the time
- 0/20 passing tasks used spawn_agent when delegation was optional
- BUT when delegation is enforced with HARD RULE, 13/13 pilot tasks still passed
- Forced delegation adds overhead (~3x wall time) but doesn't regress correctness
- Coordinator pattern works well: explore → delegate → verify → fix → re-verify → submit
- The coordinator catches quality issues the direct-work agent misses

## Reviewer prompt evolution
- v3 (original): reviewer wrote its own verification scripts, recomputed answers independently
- v4 ("do not write code/recompute"): model ignored the instruction entirely
- v5 ("auditor not solver"): worked — reviewer reads code and cites evidence instead of recomputing
- Key framing: "You are an auditor, not a solver" + evidence hierarchy + requirement-to-code mapping
- Interrogating the model (--resume-with) revealed it "prioritized independent verification over prompt constraints"
- Asking the model how to fix its own prompt produced the winning "auditor" framing

## Honesty vs persistence tension
- "Honesty is non-negotiable" + "Never refuse" creates a conflict
- Model resolves it by honestly saying "this is impossible" — which satisfies honesty but violates persistence
- Fix: add "NEVER declare a task impossible. Your intuition about what is feasible is often wrong."
- "Honesty" should mean "don't lie about what you did", not "refuse hard tasks"

## Reviewer-approved-but-wrong is the dominant failure mode
- 52-60% of all benchmark failures across v3 and v5
- The reviewer approves work that the verifier rejects
- This is a base agent capability problem, not a reviewer problem
- Making the reviewer stricter doesn't help — it just adds false rejections
