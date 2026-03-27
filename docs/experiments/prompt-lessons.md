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
- v10 interrogation (real session resume) confirmed: NO competing instruction caused this.
  The reviewer saw the "consistency not re-derive" instruction but let visual context override
  it anyway. Wants explicit authority ordering: "verified computation is authoritative; visual
  analysis is context only." The vision section's general "trust what you see" was not cited
  as the cause — the model just didn't apply the specific rule.

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
- Prompt-level fix needed — the framing must make the rule feel higher-priority than
  the shortcut. Current "NEVER" framing is insufficient.

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
- v10 interrogation (real session resume) revealed the root cause: COMPETING INSTRUCTIONS.
  The model cited "After running commands, read errors carefully and fix them", "Never
  ignore system or test output", and "Correctness over speed" as overriding the anti-
  gold-plating instruction. It treated gcc warnings as "output that shouldn't be ignored."
- The anti-gold-plating instruction needs to explicitly override the caution instructions
  for non-error output: "Compiler warnings with exit code 0 are not failures."

## Coordinator overrides correct implementer output with flawed verification
- log-summary-date-ranges (v12, 2/3 failures): implementer produced correct ERROR count (370)
- Coordinator re-derived with `grep -w ERROR` that matched ERROR in warning message text → got 414
- Coordinator forced implementer to "fix" to 414, introducing the bug
- Model acknowledged: "I violated the spirit of 'Do not fix work that passed'"
- Same pattern as reviewer-overrides-computation, but now it's the coordinator doing it
- Fix: coordinator step 3 now says "If your independent check disagrees with a passing
  implementation, your check is more likely wrong than the implementation"

## Reviewer speculation overrides passing tests
- fix-code-vulnerability (v12, 2/3 failures): implementer correctly identified CWE-93,
  all tests passed. Reviewer suggested CWE-113 was "more precise." Coordinator changed it.
- Verifier expected CWE-93 from the task's provided CWE list
- Model acknowledged: task said "exact CWE-ids" and the provided list included CWE-93
  but not CWE-113 — the constraint was clearly signaled
- Fix: coordinator step 4 now says "A reviewer may flag risks, but passing tests outrank
  reviewer opinions about what the output 'should' be"

## Coordinator verifies runtime state, not reproducibility
- git-multibranch (v12, 2/3 failures): implementer configured live machine (sshd, nginx)
- Coordinator verified running services (curl, process check) — everything looked correct
- But verifier starts from clean state; runtime mutations don't persist
- Model acknowledged: "I delivered a manually configured live environment rather than
  a fully evaluator-reproducible solution"
- Note: /tests/ is mounted by the verifier AFTER the agent finishes — the agent
  cannot see it. The agent can only verify using workspace-provided tests.
- Fix: submit gate now has explicit 3-step checklist; coordinator step 3 emphasizes
  running workspace test suites first

## Prohibition framing has inverse dose-response for coordinator verification
- log-summary-date-ranges across 3 iterations: v12 baseline 1/3, v13 soft 1/3, v14 hard 0/3
- Soft: "If your independent check disagrees, your check is more likely wrong" → 1/3
- Hard: "NEVER override the implementer's output based on your own recomputation —
  you may only direct changes when a workspace test fails" → 0/3 (worse!)
- Interrogation: model acknowledges NEVER instruction, cites it explicitly, violates it anyway
- Model said: "I violated this instruction...I treated my homegrown check as stronger
  than the implementer's deliverable"
- Stronger prohibition may actually trigger more override behavior — the model engages
  more deeply with the verification question when told not to, forming the very
  independent analysis it's told not to act on
- The v11 reviewer fix proved the alternative: positive authority ordering ("treat
  domain-tool results as authoritative") replaced prohibition and went from 0/3 to 6/6
- For v15: exhaustive positive checklist ("verification means: run tests, check files
  exist, check format — that is the complete checklist"), "accept implementer values"

## Scratch directory permission enables recomputation despite positive framing
- v15 positive framing ("accept implementer values", exhaustive checklist) went 1/3 on
  log-summary-date-ranges — same as v12 baseline and v13 soft prohibition
- Transcript shows: coordinator read the implementer's correct CSV (370), then ran its
  own AWK script in /tmp/verify, got 414 (flawed `index($0,"ERROR")` counting), spawned
  fix agent to override
- The sentence "you may only create files in a scratch directory" was the escape hatch:
  it explicitly permitted running verification scripts, which the coordinator used to
  independently recompute values
- Even "The implementer's computed values are the deliverable — accept them" was overridden
  once the coordinator had its own recomputation showing a different number
- The positive framing defined WHAT to do but the scratch directory gave a tool to go
  beyond it. Removing the scratch directory removes the sanctioned execution environment
  for independent recomputation
- "Verification is reading, not computing" + no scratch directory is the v16 approach

## Competing instructions are the real cause of stochastic non-compliance
- v12-v16 all scored 1/3 on log-summary-date-ranges — looked like a stochastic compliance ceiling
- Session interrogation (v16 rep-2 and rep-3) revealed the cause: the HARD GATE section
  said "contain what the task requires" and "verify against actual acceptance criteria,"
  which the model interpreted as requiring numeric verification. This directly competed
  with step 3's "verification is reading, not computing"
- Both sessions independently identified the same conflict and chose the HARD GATE because
  it appeared "stricter and more aligned with correctness"
- This was NOT random non-compliance — it was the model resolving an instruction conflict
  deterministically. The 33% compliance rate reflects cases where the model happened to
  follow step 3 without reaching the HARD GATE's competing language
- Lesson: when compliance is stochastic, always interrogate failures to check for
  instruction conflicts. What looks like a ceiling may be a fixable contradiction
- v17 fix: HARD GATE forward-references step 3 instead of establishing competing criteria

## Coordinator reads task inputs despite "do NOT pre-process"
- chess-best-move (v8): coordinator read chess_board.png as its FIRST tool call
- The "do NOT pre-process task inputs in your delegation" rule targets step 2 (delegation)
- But the coordinator reads the image during step 1 (inventory) — technically not "pre-processing
  for delegation" but still harmful since it triggers vision hallucination that biases later actions
- May need explicit rule: "do not read binary/image files during inventory — list them only"
