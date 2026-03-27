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

## Reviewer without tools re-derives via hallucination
- chess-best-move (v8): implementer used python-chess to find both checkmate moves (correct)
- Reviewer had no chess engine — fell back on vision hallucination, said "e2f3 (Qf3+)"
- Every time the image was read, vision produced a DIFFERENT wrong move (Qh2+, Qe4#, Qf3+)
- Coordinator trusted reviewer over implementer's computational proof
- Second implementer couldn't install python-chess (first's venv was gone), wrote wrong answer
- Fix: reviewer should check consistency and methodology, not re-derive without equivalent tools
- "Your reasoning cannot override a computational proof" — key framing

## Verification artifacts in workspace: verify-clean-reverify-forget
- polyglot-c-py (v8): coordinator compiled gcc into workspace, cleaned up, re-verified
  (re-creating the binary), forgot to clean up the second time
- The reviewer correctly flagged the artifact after first verify, triggering re-verification
- The re-verification loop is the problem: each verify pass creates artifacts
- Fix: verification must use a scratch directory, never the workspace
- Also: step 5 must be an active check (list workspace), not a passive condition

## Delegation info loss applies to reviewer delegation too
- chess-best-move (v9 rep-1): coordinator told reviewer "check that move.txt contains
  only the required move(s) in the correct format" but never specified what the format is
- The task spec says "If there are multiple winning moves, print them all, one per line"
- When interrogated, reviewer said: "I did not have the original task spec"
- Without format context, reviewer assumed single-move and rejected the correct two-move output
- The coordinator's delegation guidelines say "Include the COMPLETE original task description"
  but this only applies to implementer delegation — reviewer delegation has no equivalent rule
- Fix needed: coordinator must include relevant task spec in reviewer delegation too

## Coordinator knowingly violates rules under pressure
- chess-best-move (v9 rep-1): coordinator wrote /app/move.txt itself (cat/mv) after reviewer
  rejection, violating "You NEVER write or modify files yourself"
- When interrogated: "I was aware of that rule; I just failed to follow it"
- Also violated step 4 ("spawn a fix agent") and "trust domain-tool validation over reviewer"
- The model knows the rules but treats them as soft guidelines when a quick fix is available
- This may not be fixable with prompt alone — may need code-level enforcement (refuse to
  execute write/edit tools at coordinator depth)

## Implementer gold-plates beyond spec, consuming entire budget
- polyglot-c-py (v9 rep-5): implementer had correct, verified solution at t+44 seconds
- Both python3 and gcc produced correct output with zero exit codes
- Implementer then self-imposed `-Wall -Wextra -Werror` (not in the task spec)
- Spent remaining 856 seconds (14+ minutes) trying to suppress `'''` warnings in gcc
- Burned 138K reasoning tokens across 27 rounds, never called communicate, timed out
- The task spec says `gcc main.py.c -o cmain && cmain N` — no `-Werror`
- The implementer never re-read the spec to verify its self-imposed constraint
- Existing prompt "Keep changes minimal and focused" wasn't specific enough
- Fix: workflow.md now says "Verify against the spec's actual acceptance criteria,
  not stricter ones you invent. When your solution passes, submit it."

## Coordinator reads task inputs despite "do NOT pre-process"
- chess-best-move (v8): coordinator read chess_board.png as its FIRST tool call
- The "do NOT pre-process task inputs in your delegation" rule targets step 2 (delegation)
- But the coordinator reads the image during step 1 (inventory) — technically not "pre-processing
  for delegation" but still harmful since it triggers vision hallucination that biases later actions
- May need explicit rule: "do not read binary/image files during inventory — list them only"
