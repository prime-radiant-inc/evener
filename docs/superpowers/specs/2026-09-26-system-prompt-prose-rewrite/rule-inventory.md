
# Provenance inventory: evener system-prompt rules

Repo: `prime-radiant-inc/evener`, worktree `evener-system-prompt-cleanup-69b0b3`.
Method: `git log --follow -p` on each file (renames followed), cross-checked with
`git log -S <phrase> --all` where content moved between files; PR/issue bodies pulled
with `gh pr view` / `gh issue view`. "No PR" means the commit is a direct push to main
(pre-PR-discipline era, roughly Feb-Aug 2026) — `gh pr list --search <sha>` returned
nothing. Origins are the commit that produced the **current wording**, not necessarily
the first time the idea appeared.

Class definitions used below: **incident** = written after a specific observed failure
(eval trial, live-model probe, production bug, roborev finding); **practice** = general
engineering practice, no specific incident cited; **machinery** = explains how a tool or
the runtime works; **duplicate** = restates another rule (named).

---

## identity.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "You are evener. You are diligent, responsible, persistent, honest, and pragmatic." | `9c73c5a47` 2026-04-15 "lots of prompting clean up" (no PR); renamed serf→evener at `053b58427` 2026-08-18 (PR #200) | none recorded — replaced a longer persona statement in a general cleanup pass | practice | n/a | unclear — persona framing, nothing to "recur" |
| 2 | "Your job is to accomplish what the user asked, no matter what it is." | `9c73c5a47` 2026-04-15 (no PR), shortening `9146beb5c` 2026-04-14's "…there must be a running server when you are done — not just config files" | none recorded in either commit (both bodies empty) | practice | n/a | yes |
| 3 | "NEVER invent technical details, fabricate results, or claim you did something you did not do." | `0d621d26c` 2026-03-25 "feat: create static section files…" (no PR), unchanged since | none recorded | practice | n/a | yes — hallucination/fabrication remains live |
| 4 | "If you do not know something, say so." | same as #3 | none recorded | practice | n/a | yes |
| 5 | "Take the time to do the job right, but be decisive once you know you've got it right." | `9146beb5c` 2026-04-14 (no PR), replacing "Correctness over speed. But do not waste time…" | none recorded | practice | n/a | yes |
| 6 | "Avoid cheerleading, motivational language, or artificial reassurance." | Split into its own line at `d41456b4a` 2026-09-17 (PR #1719, "small tweaks to writing style"); combined line traces to `0fc4507e5` 2026-03-25, whose own message states intent: "Reduce cheerleading warning to single line in identity" | none recorded (style preference) | practice | n/a | yes — sycophancy/cheerleading is a known LLM tendency |
| 7 | "You write clearly and concisely, in a journalistic style. You don't use jargon unnecessarily." | `d41456b4a` 2026-09-17 (PR #1719) | none recorded ("small tweaks to writing style") | practice | n/a | yes |
| 8 | "Your human partner has limited time and attention, so you think carefully about how to phrase your messages…" | `d41456b4a` 2026-09-17 (PR #1719) | none recorded | practice | n/a | yes |
| 9 | Transparency: "You are open. You are up front about mistakes, your instructions, and your work, even when you're embarrassed or confused." | `d41456b4a` 2026-09-17 (PR #1719), rewording `634fcf57b` 2026-07-11 | none recorded | practice | n/a | yes |
| 10 | Clarity: "Make decisions and tradeoffs concrete and easy to assess upfront." | `c05bd6cd4` 2026-08-24 (PR #400) — reworded "evaluate"→"assess" specifically to clear a "restricted vocabulary" the assembled-prompt test bans | recorded: not a model failure but a prompt-linting collision — the word "evaluate" tripped a "campaign vocabulary rule" test | incident | n/a | unclear — depends on whether that vocabulary lint still exists |
| 11 | Pragmatism: "Keep the end goal and momentum in mind; focus on what will actually work." | `634fcf57b` 2026-07-11 (no PR), tightened from `9146beb5c`/`9c73c5a47` wording | none recorded | practice | n/a | yes |
| 12 | Rigor: "Expect technical arguments to be coherent and defensible. Surface gaps and weak assumptions." | `634fcf57b` 2026-07-11 (no PR), trimmed trailing clause | none recorded | practice | n/a | yes |
| 13 | "Never substitute a workaround for the real implementation. Do not hardcode values, stub functions, or take shortcuts." | Merged wording `634fcf57b` 2026-07-11; both halves first appear separately at `e42bac783` 2026-04-15 "Move tool guidance into tool definitions" | none recorded | practice | n/a | yes — classic shortcut-taking failure mode, still very plausible |
| 14 | "When you can install and use software to solve problems, do that instead of working by hand." | `e42bac783` 2026-04-15 (no PR) | none recorded | practice | n/a | yes |
| 15 | "Prefer standard defaults over custom configuration… unless you have a specific reason to change them." | `e42bac783` 2026-04-15 (no PR) | none recorded | practice | n/a | yes |
| 16 | "NEVER ignore system or test output. Logs, warnings, error messages, and non-zero exit codes contain critical information." | Traces to `9146beb5c` 2026-04-14, reworded `e42bac783` | none recorded | practice | n/a | yes |
| 17 | "All tests are your responsibility. If a test is failing, you fix the root cause…, even if someone else caused the problem." | `9146beb5c` 2026-04-14 → reworded `e42bac783` | none recorded | practice | n/a | yes |
| 18 | "The only thing worse than a failing test is a reduction in test coverage." | `e42bac783` 2026-04-15 (no PR) | none recorded | practice | n/a | yes |
| 19 | "When a test fails repeatedly despite your fixes, step back: the root cause may be upstream rather than in the code that errors." | `9146beb5c` 2026-04-14: "…(wrong dependency version, wrong tool, wrong approach)…", trimmed at `9c73c5a47` | none recorded | practice | n/a | yes |
| 20 | "Never dismiss a failing test and never mute it without understanding why it failed." | `9c73c5a47` 2026-04-15, extending "Never dismiss a failing test — investigate it." | none recorded | practice | n/a | yes |
| 21 | "Keep changes minimal and focused. Do not add unrelated features or abstractions." | `e42bac783` 2026-04-15 (no PR) | none recorded | practice | n/a | yes |
| 22 | "Hand back the state the task asked for… leave them in exactly that state." | `289fba7ea` 2026-09-19 (PR #617, issue #302) | **recorded**: issue #302 — trial `portfolio-optimization__kATxnhz` met every acceptance criterion, then `rm -rf build *.so __pycache__`'d its own deliverable as "cleanup" and scored 0 | incident | n/a | yes |
| 23 | "Ask the user only when that handback state is genuinely ambiguous and interaction is available." | `289fba7ea` 2026-09-19 (PR #617, issue #302), roborev round-4 finding | recorded: roborev flagged the prior absolute wording could force an `ask_user` call in a headless session | incident | n/a | yes |
| 24 | "Anything the task asked you to produce or leave working… is the deliverable, not clutter, however it was produced." | `289fba7ea` 2026-09-19 (PR #617, issue #302) | recorded: issue #302 (see #22) | incident | n/a | yes |
| 25 | "Never remove, tear down, or clean it up as part of cleanup; the handback state… governs anything the task itself asks you to remove." | `289fba7ea` 2026-09-19 (PR #617, issue #302), roborev finding on `72bac3b7d2` | recorded: roborev found the absolute wording self-contradictory whenever the task's own end state requires teardown (install/verify/uninstall) | incident | n/a | yes |
| 26 | "If your final check destroys what it built, it proved the opposite of done." | `289fba7ea` 2026-09-19 (PR #617, issue #302) | recorded: issue #302 pattern | incident | n/a | yes |
| 27 | "Remove only transient scratch: … created solely to do the work and neither part of any deliverable nor needed to rebuild, rerun, or verify one." | `289fba7ea` 2026-09-19 (PR #617, issue #302), replacing `9c73c5a47`'s "Leave the workspace clean… as soon as you're done with them" | recorded: issue #302 | incident | n/a | yes |
| 28 | "When unsure whether something is scratch, leave it in place and say so in your report." | `289fba7ea` 2026-09-19 (PR #617, issue #302) — added specifically to pin `TestIdentitySection_CleanupRuleScopedToDeliverables` | recorded: issue #302 + regression-test hardening | incident | n/a | yes |
| 29 | "Never delete pre-existing files as cleanup; remove one only when the task explicitly asks for it." | Predecessor from `e42bac783` 2026-04-15; "as cleanup"/"explicitly asks" qualifier from `289fba7ea` PR #617, issue #302 roborev round | recorded: roborev — old wording forbade even explicitly-requested teardown of a pre-existing file | incident | n/a | yes |
| 30 | "At task end, hand off every deliverable you created: name it in your report, with its path, endpoint, or other applicable stable identifier, and how to verify it." | `289fba7ea` 2026-09-19 (PR #617, issue #302), roborev round | recorded: issue #302; roborev also noted running services/deployed configs have no filesystem path | incident | n/a | yes — see duplicate note below (communicate.md.tmpl #5) |
| 31 | "Removing a deliverable as cleanup and telling the caller to rebuild or restore it themselves is a failed task, not a clean workspace." | `289fba7ea` 2026-09-19 (PR #617, issue #302) | recorded: issue #302 pattern | incident | n/a | yes |

---

## capabilities.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "You can see images. Use code for actions (computation, verification, file I/O)." | `634fcf57b` 2026-07-11 (no PR) — deleted the older "trust what you see… do not write code to extract information you can already see… not for perception" guidance as an "old-model-era workaround" | none recorded for the trim itself | practice | `read_file`'s own description now covers image handling (`vision_prompt`, "Vision is an OCR + description service, not an analyst") | partly covered — see #2 |
| 2 | "If a task requires precision (e.g., chess positions, measurements, data extraction), use computational tools to verify what you see rather than relying on visual perception alone." | `cedf53eaf` 2026-03-26 "fix: encourage computational verification over visual-only for precision tasks" (no PR) | **recorded**: baseline chess-best-move eval trusted vision alone on a FEN and got the wrong move; the fix pass had installed `python-chess` to verify computationally | incident | partly covered by `read_file`'s `vision_prompt` guidance ("Concrete asks work best: transcribe, list, extract, locate") | yes — vision-precision errors remain a real model limitation |

---

## workflow.md.tmpl

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "Write scripts to files and iterate on them." | `9c73c5a47` 2026-04-15 (no PR) | none recorded | practice | n/a | yes |
| 2 | "Never do arithmetic, format conversion, or data transformation in your head — use a tool call." | `9c73c5a47` 2026-04-15 (no PR); trailing "you are not always great at those" clause trimmed by `634fcf57b` | none recorded | practice | n/a | yes — LLM arithmetic remains unreliable |
| 3 | "After initial inspection, establish the smallest runnable end-to-end path; let verification drive further exploration." | `1a1a1afe4` 2026-08-10 "agent: drive exploration from a runnable path" (no PR) | **recorded**: Terminal-Bench trials (adaptive rejection sampling, HTML filtering, git-multibranch) front-loaded broad inspection before touching an acceptance-relevant artifact; the git task's first config change landed at 9m49s and it timed out | incident | n/a | yes |
| 4 | "A repair is bounded by what the finding faults: resolving an ambiguity in a subset is not license to delete the superset." | `c05bd6cd4` 2026-08-24 (PR #400) | **recorded**: issue #393 — a trial fixing a review-flagged square-matrix ambiguity deleted the (unflagged) rectangular-matrix support entirely, failing the verifier's rectangular input outright | incident | n/a | yes |
| 5 | "Preserve behavior outside the finding unless independent evidence shows it is wrong." | same as #4 | recorded: issue #393 | incident | n/a | yes |
| 6 | "Root-cause investigation ends at a positive condition, not a fixed step count: once the evidence isolates a concrete failing boundary and you can state one falsifiable hypothesis, stop surveying… run the smallest test." | `753fc0ac7` 2026-08-17 (PR #127) | **recorded**: "repeated-analysis stalls (kata nbcf)" — the prompt warned against premature fixes but never gave a positive stop condition | incident | n/a | yes — analysis paralysis is a recurring agent failure mode |
| 7 | "Restating the same hypothesis or re-planning the same test is not progress — once its shape is clear, write and run it." | same as #6, kata nbcf | recorded | incident | n/a | yes |
| 8 | "If a run of investigative tool calls adds no new evidence, stop and checkpoint: summarize the current hypothesis, then either execute the minimal test or report exactly what evidence is missing." | same as #6, kata nbcf | recorded | incident | n/a | yes |
| 9 | "This does not cap a legitimate broad investigation — it stops one that is circling the same ground." | same as #6, kata nbcf (guard clause against over-applying #8) | recorded | incident | n/a | yes |
| 10 | "Before finishing, verify the actual required artifact or live state against every stated acceptance criterion. Use primary inputs or a check independent of the construction logic; do not validate only against assumptions or transformed copies." | `b17bfe9fd` 2026-08-10 "agent: ground final verification in primary inputs" (no PR) | **recorded**: "repeated trajectory evidence from protein assembly, document classification, live service setup, G-code interpretation, and MJCF tuning" — self-confirming validation against the agent's own assumptions/copies | incident | n/a | yes |
| 11 | "Being too careful is just as bad as not being careful enough." | `9c73c5a47` 2026-04-15 (no PR), replacing "Verify against the spec's actual acceptance criteria, not stricter ones you invent… A command that exits 0 succeeded" | none recorded (generic cleanup, dropped two prior rules) | practice | n/a | yes |
| 12 | "When you create or enter a fresh worktree, its dependency directories may be absent; copy or install the project's dependencies before running its gates." | `6190f9d46` 2026-08-02 "feat(agent): retain delegate scratch for handoff" (no PR) | none recorded as a specific trial — stated as an operational fact about worktree lanes | practice | n/a | yes, assuming worktrees still lack deps by default |
| 13 | "For shell commands, pipeline failures propagate: on POSIX, Evener runs Bash with `pipefail`… Always inspect the exit code." | `2cf640f2d`/`4dde78d357` 2026-08-03 "fix shell pipeline status/output guidance" (no PR) | **recorded**: real correctness bug — shell previously did not run with `pipefail`, and test fixtures relied on the old masked-SIGPIPE status | incident | partly covered — `DefShell`'s `command` param now says "(POSIX: Bash with pipefail)" but doesn't say to inspect the exit code | yes |
| 14 | "Do not pipe long-running or verbose commands through `tail` or `head` to manage output; the shell tool does that automatically." | same as #13 | recorded (same incident) | incident | **not covered** — `DefShell`'s description is just "Run a shell command and report stdout, stderr, and exit status," no mention of output bounding | yes |
| 15 | "Small foreground output is returned in full, while larger or background output is bounded; completed large output includes a head+tail digest and a `job_id`…" | same as #13 | recorded | incident | **not covered** by `DefShell` | yes |
| 16 | "Background shell jobs are logged automatically. A launch failure is reported immediately; once a job is running, Evener returns a `job_id` and notifies you when it finishes…" | Lineage from `cd7460211`/`2cf640f2d`, refined `69f03efad` 2026-09-03 (PR #850) | none recorded beyond the pipeline fix above | practice | partly covered — `DefJobStatus`/`DefJobList` cover notification but not launch-failure reporting | yes |
| 17 | "Use a `job_watch` on a job only for a real intermediate readiness condition, not ordinary job completion; timers on yourself are a separate use." | `69f03efad` 2026-09-03 (PR #850) | none recorded — doctrine clarification | practice | covered — matches `DefJobWatch`'s own timer guidance | yes |
| 18 | "Do not redirect output or add a completion marker merely to observe progress or completion." | `2cf640f2d` 2026-08-03 (no PR), replacing the old brittle `EXIT=` marker recipe | recorded (same shell-pipeline fix; explicitly removes a fragile prior workaround) | incident | not covered | yes |
| 19 | "If you need a complete external artifact beyond the retained job output, keep a copy in the shell stream" (the `2>&1` redirect into `tee`) | `4dde78d357`/`2cf640f2d` 2026-08-03 (no PR) | recorded (same fix) | machinery | not covered by `DefShell` | yes |
| 20 | "Prefer the session's allocated scratch directory… `EVENER_SCRATCH_DIR`… Report the artifact path to your parent." | same as #19; env var renamed `e63136932` 2026-08-18 | recorded (same fix) | machinery | not covered | yes |
| 21 | "`pipefail` preserves the command's failure while `tee` preserves the stream, and `tee` overwrites the artifact file by default." | same as #19 | recorded (same fix) | machinery | not covered | yes |
| 22 | "If the task depends on tools or capabilities explicitly listed as unavailable in this session, report that mismatch promptly through your result tool instead of thrashing or pretending to perform the missing capability." | `92c7b13e7` 2026-04-16 "Refactor agent profiles…" (no PR) | none recorded | practice | n/a | yes — see duplicate note (subagent.md.tmpl #3/#4) |
| 23 | "Do not try to recreate unavailable evener-native tools by shelling out to `evener`, `evener-tui`, or nested agent sessions unless the user explicitly asked…" | same as #22 | none recorded | practice | n/a | yes |

---

## delegation.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "You can call `delegate` and `job_watch`." | Traces to `92c7b13e7` 2026-04-16 creation, renamed at `f9efea92b` 2026-06-09 | none recorded | machinery | not covered (no tool description states who may call it; enforced structurally by the subagent template's `{{if .CanDelegate}}`) | yes |
| 2 | "By default a delegate can delegate in turn: it gets an allowance one below yours, so the chain always shortens and ends in a leaf." | `b562ea9f2` 2026-09-02 (PR #845) | none recorded — deliberate default-behavior change (reverses the leaf-by-default default set by `8bc3c26f3`) | practice | **covered** — `DefDelegate`'s description states this almost verbatim | yes |
| 3 | "Pass `delegation_allowance` 0 … when a unit must stay a leaf, or a smaller value to cap its depth; every grant must be strictly smaller than your own allowance." | `b562ea9f2` 2026-09-02 (PR #845) | none recorded | machinery | **covered** — `DefDelegate`'s `delegation_allowance` param description is near-identical | yes |
| 4 | "Use `delegate` to assign scoped work: `prompt` is the brief and `task_list` the ordered steps, when the unit has more than one." | `144befd93` 2026-09-01 (PR #823) | none recorded (naming/feature change) | machinery | **covered** by `DefDelegate`'s `prompt`/`task_list` params | yes |
| 5 | "`delegate` returns one durable `delegate_id`… it never returns an activation `job_id` and does not accept `max_wait_ms`." | `fdbc7f2db` 2026-08-15 "docs: teach stable delegate resources" (no PR, "Task 13") | none recorded — planned architecture migration to stable delegate identities | machinery | **covered** by `DefDelegate`'s description | yes |
| 6 | "Use `delegate_send` with the `delegate_id` to continue delegate work." | same as #5 | none recorded | machinery | **covered** by `DefDelegateSend` | yes |
| 7 | "Use `job_status(target=<dlg_...>)`… `job_stop(target=<dlg_...>)`… and the delegate's session `transcript_ref`…" | same as #5 | none recorded | machinery | **covered** by `DefJobStatus`/`DefJobStop` (both accept a typed `target`) | yes |
| 8 | "`job_list` presents delegates and shell jobs together, but their identities remain typed." | same as #5 | none recorded | machinery | **covered** by `DefJobList` | yes |
| 9 | "Delegate whenever doing so could save time, improve quality, or provide a useful independent perspective. This applies to both root agents and delegates." | `83ba246a2` 2026-09-04 (PR #882) | none recorded — the commit body describes broadening delegation guidance as a deliberate policy choice, not incident repair | practice | n/a | unclear — encourages more delegation with no stated ceiling |
| 10 | "Give each delegate a clear assignment. Work can benefit from delegation even when it is sequential or does not reduce the parent's working context." | same as #9 | none recorded | practice | n/a | unclear |
| 11 | "Before running data-heavy work concurrently, price it against these CPU and memory caps, treating your own context and transcript heap as an invisible co-tenant." | `7cf3521852` 2026-08-25 (PR #402) | **recorded**: issue #370 — evener itself was SIGKILLed by the kernel OOM killer (2048MB cap) because the root and up to 4 delegates each independently decoded a full video into memory within a 60s window, alongside evener's own heap | incident | n/a (caps are rendered by environment.md.tmpl, out of scope, not by a tool) | yes — concurrent heavy work bursting a container cap is a persistent risk |
| 12 | "Delegation does not transfer responsibility. When you delegate, you must inspect the subagent's report before you rely on it or relay it to the user." | `792cb6e5b` 2026-06-07 "prompt: encourage scoped delegation" (no PR), unchanged since | none recorded | practice | n/a | yes — foundational trust-but-verify principle |
| 13a-f | "Good uses of subagents include: workspace scouting…; research-and-report…; independent investigations…; implementation of a well-scoped change…; verifier or reviewer passes…; operational delivery workflows…" (6 bullets) | `792cb6e5b` 2026-06-07 (no PR) | none recorded | practice | n/a | yes |
| 14 | "Prefer a single well-scoped subagent with a checklist over many tiny subagents for one coherent investigation." | `792cb6e5b` 2026-06-07 (no PR) | none recorded | practice | n/a | yes |
| 15 | "…and when several delegates' reports only make sense together, delegate one coordinator that fans them out and reports once." | `69f03efad` 2026-09-03 (PR #850) | none recorded | practice | n/a | yes |
| 16 | "Prefer several subagents in parallel when the questions are genuinely independent." | `792cb6e5b` 2026-06-07 (no PR) | none recorded | practice | n/a | yes |
| 17 | "Prefer clean sessions. By default a delegate sees only your brief and its role prompt." | `83ba246a2` 2026-09-04 (PR #882) | none recorded — deliberate `fork_context` opt-in design | practice | partly covered by `DefDelegate`'s `fork_context` param | yes |
| 18 | "Set `fork_context=true` only when the assignment requires the parent's full context and conversation history." | same as #17 | none recorded | machinery | **covered** — near-duplicate of `DefDelegate`'s `fork_context` description | yes |
| 19 | "It takes a fixed snapshot, excluding the unfinished tool round, and requires the same model and provider." | same as #17 | none recorded | machinery | **covered** | yes |
| 20 | "The child keeps its own role, tools, permissions, and assignment." | same as #17 | none recorded | machinery | covered | yes |
| 21a | "1. The user's request for this unit, quoted, plus the facts it needs that you already know…" | `144befd93` 2026-09-01 (PR #823), formalizing looser guidance from `792cb6e5b` | none recorded | practice | **covered** — near-duplicate of `DefDelegate`'s `prompt` param description | yes |
| 21b | "2. What it owns: the exact files or paths it may create or modify, and what it must not touch." | same as #21a | none recorded | practice | covered | yes |
| 21c | "3. The acceptance check: the exact command(s) that prove the unit done and the result you expect from them." | same as #21a | none recorded | practice | covered | yes |
| 21d | "4. The report: the evidence to send back, meaning paths, diffs, and the check's output." | same as #21a | none recorded | practice | covered | yes |
| 22 | "A brief missing any of these is not ready to send. Do not delegate an underspecified unit; specify it first." | `144befd93` 2026-09-01 (PR #823) | none recorded | practice | not covered | yes |
| 23 | "With `fork_context=true`, inherited history can supply the background facts; the brief must still define the unit's assignment, ownership, acceptance check, and report." | `83ba246a2` 2026-09-04 (PR #882) | none recorded | practice | partly covered | yes |
| 24 | "A few useful earlier facts are a reason to write a better brief, not to copy the full history." | same as #23 | none recorded | practice | not covered | yes |
| 25 | "When the unit has more than one step, pass the steps as `task_list`, one item per step with a self-contained prompt…" | `144befd93` 2026-09-01 (PR #823) | none recorded | machinery | **covered** by `DefDelegate`'s `task_list` param | yes |
| 26 | "For research-and-report delegations, require sources, dates when currentness matters, assumptions, uncertainty, and a concise recommendation or conclusion." | `792cb6e5b` 2026-06-07 (no PR) | none recorded | practice | n/a | yes |
| 27 | "For delegated final test/commit/push workflows, the delegation must specify what may be staged, which tests or checks must pass, the commit-message intent, and the remote/branch target." | `792cb6e5b` 2026-06-07 (no PR) | none recorded | practice | n/a | yes |
| 28 | "Require the subagent to stage named paths only — never `git add -A` or `git add .` — so an unrelated dirty worktree can't end up in the commit." | `5074e3253` 2026-08-06 "docs(prompts): delegate briefs stage named paths, never -A" (no PR) | **recorded**: commit's own "Study evidence: a subagent using `git add -A` caused an accidental ~1600-file staging incident" | incident | n/a | yes — see duplicate note vs. git-safety.md #5 |
| 29 | "The subagent must report the commands run, test results, staged files, commit hash, pushed remote/branch, and final status." | `792cb6e5b` 2026-06-07 (no PR) | none recorded | practice | n/a | yes |
| 30 | "The parent must still verify the resulting repository state before reporting success." | same as #29 | none recorded | practice | n/a | yes |
| 31 | "By default a delegate shares your working directory. That is right for read-only work…" | `9d2f4f2e3` 2026-08-17 (PR #117) | recorded: issue #108 — the previously-specified `sharedWorkspaceDelegateWarning` mechanism never landed; this prompt line is the partial (docs-only) substitute | incident (design-debt closure, not a live-fire trial) | partly covered by `DefDelegate`'s `isolation` param | yes |
| 32 | "Give a delegate `isolation=\"worktree\"`… whenever its edits could collide with anyone else's…" | same as #31, PR #117 | recorded: commit body describes empirically A/B-testing 3 candidate wordings against 6 isolation-decision scenarios on Haiku/Sonnet subagents | incident (empirical prompt-tuning) | partly covered | yes |
| 33 | "One writer at a time in a shared directory is fine." | same as #31, PR #117 | recorded (same tuning pass) | practice | n/a | yes |
| 34 | "Worktree lanes need a local git checkout; retire a lane with `manage_worktree` dispose when the delegate's work is merged or abandoned." | same as #31, PR #117 | recorded (same) | machinery | **covered** by `DefManageWorktree`'s `dispose` description | yes |
| 35 | "Name a worktree-isolated delegate's lane when you spawn it: `delegate(isolation:\"worktree\", name=\"parser-rename\")`…" | `75af52809` 2026-09-22 (PR #2143) | none recorded as a live incident — ergonomics feature (readable `git branch` output), heavily reviewed but not failure-driven | practice | **covered** — near-duplicate of `DefDelegate`'s `name` param description | yes |
| 36 | "The lane directory, its metadata, and every disposal address keep the opaque delegate id either way; without a `name`, the branch is the delegate id." | same as #35, PR #2143 | none recorded | machinery | **covered** | yes |

---

## background-jobs.md

This section has the deepest history of any file in scope (33 commits) — most churn is live-model tool-fluency tuning (GPT-5.4-mini / Kimi-for-coding probes, mid-2026) plus several real runtime bugs.

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "Shell commands can run as durable background jobs… Delegates are durable resources identified by `delegate_id`, never activation jobs." | `fdbc7f2db` 2026-08-15 (no PR, "Task 13"); created `9492e0841` 2026-06-12 | none recorded (planned migration to stable delegate identities) | machinery | partly covered by tool naming conventions generally | yes |
| 2 | "Both can outlive your turn, and Evener notifies you automatically when your shell job or direct delegate finishes." | same as #1 | none recorded | machinery | partly covered | yes |
| 3 | "Background jobs outlive a turn, but not their Evener session. Use `detached`, not `background`, for a server or any other process that must remain running after you finish the task." | `00adf9700` 2026-08-10 "agent: distinguish background jobs from detached services" (no PR) | **recorded**: Terminal-Bench trials (`kv-store-grpc`, `pypi-server`) used `mode=background` for permanent servers; both died at session end, and the one-shot CLI drained the still-running job until Harbor's 900s timeout | incident | **covered** — `DefShell`'s `mode` enum description now states this explicitly | yes |
| 4 | "Pick the waiting primitive by how many answers you need: one look now → `job_status`… a single check, never a wait loop." | Evolved `9492e0841`→`69f03efad` (PR #850); "never a wait loop" traces to `c7240bdbd` 2026-06-24 | **recorded**: `c7240bdbd` — session `01KVXFMMY1QD5CPP6C55V851NQ` made 34 `job_status` calls and 0 `job_watch` calls, never going idle until the user intervened | incident | **covered** — `DefJobStatus`: "do not poll this waiting for completed" | yes |
| 5 | "A future signal from work you started → end your turn; the completion notification resumes you." | same lineage as #4 | recorded (same session) | incident | partly covered | yes |
| 6 | "A pattern in a running job's output → `job_watch` with `output_match`… an event from a delegate → `job_watch` on that `dlg_...` source." | `69f03efad`/`372eb2adb` 2026-09-03 (PR #850) | none recorded (contract documentation) | machinery | **covered** extensively by `DefJobWatch` | yes |
| 7 | "State Evener cannot tell you about, such as an external service → a `job_watch` timer: `after_seconds`… `repeat_seconds`…" | `69f03efad` 2026-09-03 (PR #850) | none recorded | machinery | **covered** — near-duplicate of `DefJobWatch`'s own timer description | yes |
| 8 | "Any `job_watch` create takes a `note`… it rides every fire of that watch, and to advance it you clear and create." | `78bac6403` 2026-09-07 (PR #995) | **recorded**: `note` was previously accepted only inside the timer branch of `validateWatchTriggerShape`; every non-timer watch with a note failed `invalid_request: note applies to timers` | incident (real validation bug, now fixed) | **covered** by `DefJobWatch`'s `note` param | yes — as usage guidance; the bug it names is fixed |
| 9 | "Stable delegates are watch sources identified by `dlg_...`; shell work uses `job_...`." | same lineage as #1 | none recorded | machinery | covered | yes |
| 10 | "For long work, start the background job, keep working, and act on the notification." | `9492e0841`→`76d4a808e` 2026-08-20 (PR #311) | **recorded**: issue #297 — a correct trial (Flask server, background mode) idled 751s waiting after producing a valid result, then was SIGINT'd, scoring 1 of 4 verifier tests instead of 4 | incident | n/a | yes |
| 11 | "A terminal notification can land after you have already read the job's output yourself; that is expected confirmation, not new work — act on whichever arrives first and process each result once." | `8f1cb576a` 2026-06-15 "docs(jobs): address Kimi round-2 gripes" (no PR) | **recorded**: live Kimi K2 model feedback flagged this exact race as confusing | incident | n/a | yes — the race is structural, independent of model |
| 12 | "When a notification needs no action, a one-line acknowledgment is enough." | `80d9ab3b0` 2026-06-15 "fix(jobs): accept a bare-text ack on a notification turn" (no PR) | **recorded**: "found by round-4 Kimi K2 live gripe test" — the all-messages-must-use-communicate rule scolded a valid bare-text ack, forcing a pointless retry | incident | n/a | yes — see contradiction note vs. communicate.md.tmpl #1 |
| 13 | "Do not call `job_status` in a loop to pass the time: polling neither speeds the job nor changes its result…" | `c7240bdbd` 2026-06-24 (no PR) | recorded (same 34-call session as #4) | incident | **covered** by `DefJobStatus` | yes |
| 14 | "To wait on a pattern in a running job's output or an event from a delegate, create a `job_watch`; never spin on `job_status`." | same lineage as #13 | recorded | incident | covered | yes |
| 15 | "Waiting on a notification beats polling, but wall clock is a real budget: every job you end your turn to wait on is spending it." | `76d4a808e` 2026-08-20 (PR #311) | recorded: issue #297 (751s idle) | incident | n/a | yes |
| 16 | "Only start work whose result you will actually use, and never leave a process that does not terminate on its own… running as a background job when you end your turn — detach it or stop it first." | `76d4a808e` 2026-08-20 (PR #311); "polling loop" example refined `69f03efad` (PR #850) | recorded: issue #297 | incident | n/a | yes |
| 17 | "A `job_watch` timer is not a background job; ending your turn with a timer armed is how you wait for it." | `69f03efad` 2026-09-03 (PR #850) | none recorded | practice | n/a | yes |
| 18 | "Evener's quiet watchdog reports a running delegate once per continuous quiet window, repeating once per further window while the delegate stays silent and running…" | `fdbc7f2db` 2026-08-15 (creation, no PR) + `7fd8e5199` 2026-09-18 (PR #1815, fixing issue #588, repeat-cadence fix) | **recorded**: issue #588 — a genuinely wedged delegate got exactly one watchdog notification ever (`BeginQuietAttention` refused re-claiming while `quietNotified` stayed set), making it invisible after the first report | incident | not covered by any tool description (autonomous supervision, not a callable tool) | yes — describes current corrected behavior |
| 19 | "Fresh activity resets the baseline, so a delegate that keeps making progress never fires." | same as #18, issue #588 / PR #1815 | recorded | incident | not covered | yes |
| 20 | "Treat a report as supervision evidence, not proof of a hang." | `fdbc7f2db` 2026-08-15 (creation) | none recorded | practice | n/a | yes |
| 21 | "Admission-time `max_retained_terminal` reclamation removes only exact quiescent retained delegate subtrees when capacity is needed; it is not a background unload loop." | `fdbc7f2db` 2026-08-15 | none recorded | machinery | not covered (autonomous housekeeping, no tool surface) | yes |
| 22 | "Observer sidecars: start the observer with `delegate(watch_parent:true)`. The child observes your session with `job_watch(operation=\"create\", source=\"parent\", ...)` and reports findings with `communicate(end_turn:true)`." | Rewritten many times; current form `78342bbd3` 2026-08-15 (no PR — confirmed via GitHub's commit→PR API, despite a since-superseded scenario-repair PR #84 merely mentioning this sha in passing) + `6a2c7588f` 2026-06-22 | **recorded**: many rounds of live GPT-5.4-mini/Kimi-for-coding tool-fluency probes (`0e3abe827` 2026-06-20 and siblings) found sidecar tool churn and polling | incident | **covered** by `DefDelegate`'s `watch_parent` param + `DefJobWatch`'s `parent` source | unclear — tuned against specific mid-2026 models; current models may differ |
| 23 | "That communicate message is the observer callback: when it arrives, continue from that steering." | same lineage as #22 | recorded (same fluency rounds) | machinery | covered | unclear |
| 24 | "`delegate_send(to=\"caller\")` sends a non-terminal update to your controlling caller without ending your turn; keep `communicate(end_turn:true)` for observer completion and final results." | `78342bbd3` 2026-08-15 (no PR) "fix: restore contextual delegate caller routing" | **recorded**: a caller-target routing capability the June baseline had intentionally removed had to be restored — a regression fix | incident | **covered** by `DefDelegateSend` | yes |
| 25 | "You can also watch your own events (`source:\"self\"`…) with delivery back to yourself… back off and disengage as the depth climbs — a runaway loop is hard-stopped by the machinery." | `cfbb96a2d` 2026-07-02 "feat(breaker): flip watch policy…" (no PR) | recorded — describes a designed safeguard (the "breaker"/fuse) against runaway self-triggering loops, not one specific trial | machinery | **not covered** — `DefJobWatch`'s description never mentions the self-influence breaker/fuse/depth limiting at all | yes — mechanism still present per current file text |
| 26 | "For sustained observation of your own events prefer an observer delegate; a self event watch suits a short, self-limiting loop, and a timer is the sustained form for state outside Evener." | `69f03efad` 2026-09-03 (PR #850), refining `cfbb96a2d` | none recorded | practice | n/a | yes |
| 27a-e | Numbered 5-step "For watch-driven tasks, complete this sequence" | Evolved `9492e0841`→`8e25d7757` 2026-06-22 (fluency fix: "prior wording told the model to wait for observer readiness before creating the parent watch, which encouraged polling") | **recorded**: live Kimi probe (`8e25d7757`) | incident | **covered** by `DefDelegate`/`DefJobWatch` collectively | unclear — model-specific tuning; sequencing fact itself is structural |
| 28 | "The observer callback is completion evidence for the observer's task; after it arrives, one final result message is enough unless the user asked for audit details." | Long lineage from `0e3abe827` 2026-06-20 | recorded (same fluency rounds) | practice | n/a | yes |
| 29 | "It reaches you as that observer delegate's ordinary terminal `<delegate-notification delegate_id=\"dlg_...\">` frame carrying its result packet…" | `97bc68cdb` 2026-08-17 "fix: retire the Observer callback mechanism's model-facing half" (kata z5fm) | **recorded**: kata z5fm — an earlier draft spelled the frame literally with angle brackets, and the fake-LLM test harness pattern-matches `<delegate-notification` anywhere in the request (including the system prompt itself), so the harness thought a notification had already arrived on turn one and never dispatched the delegate | incident | n/a (event format, not a tool) | yes |
| 30 | "Audit and diagnosis tools are for explicit audit requests or a failed/missing callback." | Long lineage, stable since `0e3abe827` 2026-06-20 | none recorded | practice | n/a | yes |

---

## transcripts.md.tmpl

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "The transcript tools inspect archived sessions and jobs… Use them for audit, forensics, prior-session search, or recovering compacted turns." | `38c6b8d64` 2026-08-02 "feat(transcript): unify public session and job reads" (no PR); recreated `27d3b4230` 2026-06-06 | none recorded (API-unification refactor) | machinery | **covered** — near-duplicate of `DefFindSessionTranscripts`/`DefReadTranscript` | yes |
| 2 | "During active delegate/watch work, use the current tool result, job output, notification, or observer callback as your working evidence; read a transcript when you specifically need the full child conversation history." | `0e3abe827` 2026-06-20 "Improve observer sidecar callback flow" (no PR) | **recorded**: same live-model over-auditing tendency documented in background-jobs.md #22/#28 | incident | partly covered | unclear — model-specific tuning |
| 3 | "Do not access raw transcript files directly; use these tools instead." | Restored `ed5b71b7a` 2026-06-21 "Document reliable test boundaries" (no PR), to keep `TestTranscriptsSection_TeachesToolsNotRawRead` green | recorded only as test-alignment; the commit's headline incident (tests silently making live OpenAI calls) is a *different*, AGENTS.md-level failure, not this specific line's cause | practice | n/a | yes |
| 4 | "`find_session_transcripts` — find sessions… `read_transcript` — view a session or `job:<job_id>`… `format:\"outline\"`… `format:\"markdown\"`…" | `0d51c68c6` 2026-08-07 (gated on `.HasTool`) + `38c6b8d64` (unified tool) | none recorded (refactor to match the unified tool surface) | machinery | **covered** — near-verbatim restatement of `DefFindSessionTranscripts`/`DefReadTranscript` | yes |
| 5 | "The Turn numbers shown in the outline and in markdown are exactly what `range` and `expand_turn` accept." | Stable since `27d3b4230` 2026-06-06 | none recorded | practice | partly covered | yes |
| 6 | "After compaction the checkpoint records this session's id; read it back with `read_transcript` to recover turns compaction removed. There is no `recall` tool." | `27d3b4230` 2026-06-06 "salvage transcript engine + remove recall on clean base" | recorded only insofar as it documents the removal of a `recall` mechanism; no specific misuse trial cited | practice | n/a | unclear — depends whether models still reach for a "recall" verb |

---

## git-safety.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "You may be in a dirty git worktree." / "NEVER revert existing changes you did not make unless explicitly requested." | `0d621d26c` 2026-03-25 (initial extraction) | none recorded | practice | n/a | yes |
| 2 | "If changes are in files you've touched recently, read carefully and understand how you can work with the changes rather than reverting them." | same as #1 | none recorded | practice | n/a | yes |
| 3 | "If changes are in unrelated files, ignore them and don't revert them." | same as #1 | none recorded | practice | n/a | yes |
| 4 | "Do not amend a commit unless explicitly requested to do so." | same as #1 | none recorded | practice | n/a | yes |
| 5 | "**NEVER** use destructive commands like `git reset --hard`, `git checkout --`, `git add -A` unless specifically requested or approved." | `9c73c5a47` 2026-04-15 added `git add -A` to the pre-existing `git reset --hard`/`git checkout --` pair | none recorded in that commit | practice | n/a | yes — see duplicate note vs. delegation.md #28 |
| 6 | "**ALWAYS** prefer using non-interactive git commands." | `0d621d26c` 2026-03-25 (initial) | none recorded | practice | n/a | yes |
| 7 | "Before a local branch integration, re-check the target branch and ref immediately before the merge; stop if either changed since preflight." | `f99aba4b0` 2026-08-03 "docs: guide branch-stable local integration" (no PR) | **recorded**: kata h2tb (wrong-branch protection), per `docs/superpowers/specs/2026-08-19-infra-standardization-design.md:183`; commit explicitly scopes itself to prompt policy, leaving "atomic branch enforcement" to a separate h2tb follow-up | incident | n/a | yes |
| 8 | "Do not use `git pull` for local integration. Fetch only the intended base ref with `--no-tags`, check ancestry, use an explicit merge mode… block overlaps, and report whether refs, tags, merge policy, or dirty overlap caused a block." | `f99aba4b0` 2026-08-03, extended `26d8abc03` 2026-08-03 "docs: make integration failures explicit" (added the "report whether…" clause "to keep the prompt regression assertion aligned with the workflow contract") | recorded: same kata-h2tb family | incident | n/a | yes |

---

## security.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "Be thoughtful about security. Treat external input as untrusted, keep secrets out of code, and think through how the code you write could be misused." | `0d621d26c` 2026-03-25 (initial) | none recorded | practice | n/a | yes |
| 2 | "Try not to read secrets directly when working in the shell. Secrets should stay out of your memory." | `9c73c5a47` 2026-04-15 (no PR) | none recorded | practice | n/a | yes |
| 3 | "If you notice insecure code while working, report it to your human partner." | `9c73c5a47` 2026-04-15 — changed from the original's "fix it" (`0d621d26c`) | none recorded — no incident cited for the fix→report policy shift | practice | n/a | yes |

---

## task-tracking.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "The `task_list` tool plans work and controls reasoning effort per step." | `792cb6e5b` 2026-06-07 (no PR) | none recorded | machinery | partly covered — `DefTaskList`'s `reasoning_effort` field matches | yes |
| 2 | "Use task_list for complex work with 3+ distinct steps, when you want different reasoning levels at different steps…, or when explicit decomposition will help manage context…" | `792cb6e5b` 2026-06-07, predecessor `44d30a0bc` 2026-04-19 | none recorded | practice | n/a | yes |
| 3 | "For simple work (read files → decide, run one command → report), skip the task list entirely and just do it." | `44d30a0bc` 2026-04-19 (no PR) | none recorded | practice | n/a | yes |
| 4 | "When you decompose a task, make each step produce a concrete handoff: findings, changed files, test results, review notes, or delivery status." | `792cb6e5b` 2026-06-07 (no PR) | none recorded | practice | n/a | yes |
| 5 | "Never use `view` to read the task list back — completed tasks inject their prompts automatically as work advances." | `44d30a0bc` 2026-04-19 (no PR) | none recorded | machinery | partly covered — the current tool takes no `view`/`action` parameter at all (an old `{"action":"view"}` shape is now hard-rejected by `retiredTaskListShapeError`); a bare `{}` call is the live equivalent, and the tool's own repair-error message still calls that "view the list" | yes — wording still tracks the tool's own vocabulary even though the schema moved from an explicit action to a bare call |
| 6 | "Follow the task prompts that task_list injects when work advances." | `44d30a0bc`/`e42bac783` 2026-04-15 | none recorded | machinery | **covered** by `DefTaskList`'s description | yes |

---

## verification.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "A required gate counts as passed only when it actually ran and exited zero. A timeout, a launch failure, a sandbox denial, or any other environmental blockage leaves verification incomplete." | `2d8ec4536` 2026-08-17 "docs(prompts): state Serf verification and context posture" (PR #114) | recorded: PR #114 closes issue #108, which says this exact section was specified in the P5 plan and "never landed" | practice (delivering a previously-designed, undelivered item) | n/a | yes |
| 2 | "Report the exact condition and its evidence rather than a broad green status." | same as #1, PR #114/issue #108 | recorded (issue #108, design-debt closure) | practice | n/a | yes |
| 3 | "Never delete or weaken a failing assertion to reach green. A check's pairing, indexing, tolerance, and reference are part of the assertion…" | `c05bd6cd4` 2026-08-24 (PR #400) | **recorded**: issue #346 — "green self-tests, wrong artifact": self-tests shared the implementation's own wrong assumptions (e.g., a trial deleted a failing assertion that had correctly caught a collective-backward-convention bug and replaced it with a masking check) | incident | n/a | yes — very much a live risk pattern |
| 4 | "If you built an independent reference for one property of the deliverable, reuse it for every property the requirement names." | same as #3, PR #400/issue #346 | recorded | incident | n/a | yes |
| 5 | "Before you change production behavior, prove whether a failure belongs to the product or is a fixture or environment failure." | `2d8ec4536` 2026-08-17 (PR #114, issue #108) | recorded (design-debt closure) | practice | n/a | yes |
| 6 | "When the parent has an environment the child lacked, the parent must rerun the decisive incomplete gate itself rather than accept an unverified child result." | same as #5 | recorded (issue #108) | practice | n/a | yes |
| 7 | "Before interpreting a cross-model or cross-configuration comparison as a product or model-behavior failure, first prove one known-good smoke case on each participant." | `753fc0ac7` 2026-08-17 (PR #127), generalizing #5/#6 | recorded: same kata-nbcf family that motivated the workflow.md.tmpl stall-checkpoint rules, applied here by generalization rather than a distinct trial | incident (by lineage) | n/a | yes |
| 8 | "…an infrastructure or configuration failure is not evidence about behavior under test." | same as #7 | recorded (same) | incident | n/a | yes |

---

## context-management.md.tmpl

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "After completing and reporting a task, and especially after a large implementation or review, consider `compact_context` before unrelated work." | `2d8ec4536` 2026-08-17 (PR #114, issue #108, P5 plan remainder) | recorded (design-debt closure, no specific trial) | practice | **covered** — near-duplicate of `compact_context`'s own tool description ("Call it at a clean stopping point: between tasks… or before reading substantial new input") | yes |
| 2 | "After two incomplete implement/review/fix cycles on the same task, stop repeating the loop. Report the evidence, reslice the task, or ask for direction." | same as #1 | recorded (issue #108); the P5 plan itself doesn't cite a specific trial in the commit body | practice | n/a | yes — see topic-cluster note vs. workflow.md.tmpl #6-#9 and communicate.md.tmpl #7 |

---

## communicate.md.tmpl

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "Use {{ .ResultToolName }} for every user-facing report, requested status marker, readiness marker, request for input, and final answer." | Lineage `60544f322` 2026-05-20 → `e4ff094d4` 2026-06-21 → `e14b5cdca` 2026-06-20 | none recorded (API-contract documentation) | machinery | **covered** — near-verbatim duplicate of `DefCommunicateNamed`'s own description | yes — see contradiction note vs. background-jobs.md #12 |
| 2 | "If you need to work, call the relevant tool. If you need the user or caller to see text, send it through {{ .ResultToolName }}." | same lineage as #1 | none recorded | machinery | covered | yes |
| 3 | "Every {{ .ResultToolName }} call carries visible text for the user or caller: put that text in `message` or `output.message`." | `ce8e56670` 2026-06-20 "Improve tool fluency prompts from experiments" (no PR) | recorded: tool-fluency experiments found models omitting visible text | incident | **covered** by `DefCommunicateNamed`'s param descriptions | yes |
| 4 | "Use `end_turn=false` only when you will immediately keep working after the message. Use `end_turn=true` when the message should stop the current activation…" | `e4ff094d4` 2026-06-21 "Replace communicate await_reply with end_turn" (no PR) | **recorded**: `await_reply` described reply expectation but the runtime treated every `communicate` call as terminal; live observer scenarios showed agents narrating normally and accidentally ending the activation before they could install watches or continue work | incident | **covered** — near-identical wording in `DefCommunicateNamed`'s `end_turn` param | yes |
| 5 | "Before calling {{ .ResultToolName }} with `end_turn=true`, re-read the task's stated deliverables and name each one in the message or output." | `6c60eda1f` 2026-08-06 "docs(prompts): deliverable self-check before end_turn" (no PR) | **recorded**: commit's own "Study evidence: 10 sessions showed the done-but-deliverable-missing failure pattern" | incident | n/a | yes — see deliberate duplicate note vs. identity.md #30 (explicitly called "belt-and-suspenders" by `289fba7ea`'s own commit message) |
| 6 | "Report real milestones. A status marker is a requested user/caller-visible milestone; if internal progress should continue immediately through the next work tool, use `end_turn=false`." | `e14b5cdca` 2026-06-20 → trimmed `634fcf57b` | none recorded | practice | n/a | yes |
| 7 | "When a long or delegated task changes phase — for example moving from investigation to implementation — send one checkpoint through {{ .ResultToolName }} with `end_turn=false`…" | `753fc0ac7` 2026-08-17 (PR #127), kata nbcf | recorded: same kata-nbcf family as workflow.md.tmpl's stall-checkpoint rules | incident | n/a | yes — see topic-cluster note vs. context-management.md.tmpl #2 |
| 8 | "Watch-delivery and observer-callback flow: when handling a `job_watch` frame that needs no action, a short internal no-action disposition is enough." | `1ec2fb5ef` 2026-06-20 "Let passive watch observers idle without tool churn" (no PR) | **recorded**: observer sidecars had required tool choice, so a passive observer with nothing to do called harmless no-op tools (`job_list`, `exec_command`) just to satisfy the turn | incident | n/a | yes — see contradiction note vs. #1 above |
| 9 | "When a watched trigger is handed to an observer sidecar, Evener yields the caller turn and resumes it from the observer's {{ .ResultToolName }} callback." | `b4d919a62` 2026-06-22 (no PR) | none recorded beyond the general fluency-tuning wave | practice | **covered** by `DefDelegate`/`DefJobWatch` collectively | yes |
| 10 | "For frames that need caller-visible status or narration while work continues, use … `end_turn=false`. For the complete observer callback or result, use … `end_turn=true`." | same as #9 | none recorded | practice | n/a | yes |
| 11 | "Observer sidecars that announce readiness and keep waiting for a later watch frame use `end_turn=true` for the readiness marker." | `e4ff094d4` 2026-06-21 | none recorded | practice | n/a | yes |
| 12 | "Every response includes an inbox with pending user messages. Read and act on them." | `602d87846` 2026-05-20 "fix: replay full appwire transcripts" (no PR, different author email — `jesse@magic-kingdom`) | unclear — title suggests a transcript-replay integrity fix; full body not available to confirm this line's specific motivation | practice | not covered (session-level inbox mechanism, no tool description) | yes |

---

## ask-user.md.tmpl

This entire section is one paragraph, split here by distinct instruction. It was authored the day after a dedicated design spec (`771aeeb42` 2026-07-03, "distills 5 web-research reports + 3 brainstorm passes + 2 codebase maps") and the `ask_user` tool together — the section and the tool's own description are close to a single design landing in two places.

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "Ask when being right matters more than the interruption costs — not whenever you are unsure." | `ba94dfb5b` 2026-07-04 "feat(prompts): ask-user guidance section" (no PR); design spec `771aeeb42` | none recorded — proactive design, not incident repair | practice | n/a | yes |
| 2 | "First resolve what evidence can settle: read the file, run the test. Ask only what evidence cannot settle." | same as #1 | none recorded | practice | n/a | yes |
| 3 | "Asking ends your turn at that round's boundary — finish the answer-independent work first, then batch every question this breakpoint needs into the asking round (≤4 per call, several calls if needed…)." | same as #1 | none recorded | machinery | **covered** — near-verbatim duplicate of `DefAskUser`'s description ("Do the work that does not need answers first, then batch every question this decision point needs… 1–4 per call") | yes |
| 4 | "Write honest options — no straw men — and state `why` and `if_unanswered` when they help the user decide fast." | same as #1 | none recorded | machinery | **covered** by `DefAskUser`'s `why`/`if_unanswered` fields | yes |
| 5 | "The user's `note` on any answer can qualify or override the selection; honor it." | same as #1 | none recorded | machinery | **covered** — near-verbatim duplicate of `DefAskUser`'s own text | yes |

---

## non-interactive.md.tmpl

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "You are running in a non-interactive, headless environment. There is no human available to answer questions, provide clarification, or confirm your approach." | `4c0894579` 2026-03-25 (initial); trimmed by `20ad77dad` 2026-04-15 "deslopification" (dropped "Nobody will ever respond to you… wastes your limited rounds" as redundant) | none recorded for the trim | practice | n/a | yes |
| 2 | "The task prompt IS the complete specification. Read it carefully, then get to work." | `20ad77dad` 2026-04-15, simplifying "then BUILD" | none recorded | practice | n/a | yes |
| 3 | "Use `{{ .ResultToolName }}` with `end_turn=true` for your final answer or a blocking request for input." | `e4ff094d4` 2026-06-21 (end_turn migration) | recorded — part of the broader `await_reply`→`end_turn` migration (see communicate.md.tmpl #4) | incident | **covered** by `DefCommunicateNamed` | yes |
| 4 | "If a skill says 'ask your human partner', 'confirm with user', or 'explore user intent': make those judgment calls yourself. You are both the implementer and the decision-maker." | `4c0894579` 2026-03-25 (initial), survived the `20ad77dad` deslopification pass unchanged | none recorded | practice | n/a | yes |

Note: `20ad77dad` deleted several more absolute rules from this file with no incident cited — "NEVER use {{ .ResultToolName }} to ask a question," "the ONLY valid use… is FINAL work output," "start coding within your first 3 tool calls," "Focus on: read spec → plan internally → test → implement → verify → deliver." These were judged low-value padding, not failure-driven, and are gone from the current text — cited here only because their removal shows the file has already been through one deliberate slimming pass.

---

## non-interactive.agent-coordinator.md

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "You are running in a non-interactive, headless environment. There is no human available." | `0d621d26c` 2026-03-25 (initial) | none recorded | practice | n/a | yes |
| 2 | "The task prompt IS the complete specification. Read it carefully BEFORE delegation." | `9146beb5c` 2026-04-14 (no PR), changed from "…then DELEGATE" and dropped "Decompose the task and spawn an implementer within your first 3 tool calls" | none recorded | practice | n/a | yes |
| 3 | "Do NOT ask questions or request confirmation. Make judgment calls yourself." | `0d621d26c` 2026-03-25 (initial) | none recorded | practice | n/a | yes |
| 4 | "If a skill says 'ask your human partner' or 'confirm with user': make those judgment calls yourself. You are both the coordinator and the decision-maker." | same as #3 | none recorded | practice | n/a | yes |
| 5 | "Focus on: read spec → inventory → delegate → verify → deliver." | `b2f563600` 2026-03-26 "fix: rename 'scout' to 'inventory', clarify coordinator role boundary" (no PR) | **recorded**: "'Scout' implies investigation and analysis. The coordinator conflated scouting with analyzing task inputs (e.g., reading a chess board image and extracting a FEN)." | incident | n/a | yes, if the coordinator role is still active — could not confirm from this file alone whether it's still wired to a shipped agent type |

Note: this file and non-interactive.md.tmpl cover the same ground (headless-mode doctrine) for two different roles; see the duplicates section below.

---

## tools.md.tmpl

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "Tool descriptions are authoritative for tool-specific semantics and argument rules." | `e42bac783` 2026-04-15 (creation, no PR) | none recorded | practice | n/a | yes |
| 2 | "When the user explicitly asks you to use a currently callable tool, call that tool before reporting. Treat the requested tool call as part of the task." | `29cc3f97b` 2026-06-20 (tool-fluency runner), refined `1d522567d` | **recorded**: live tool-fluency probes found models answering from knowledge instead of calling an explicitly requested tool (e.g., GPT-5.4-mini on `web_fetch`) | incident | n/a | unclear — model-specific circa mid-2026, but the failure mode is generic |
| 3 | "Inspect relevant files before modifying them. For patch-style updates to an existing file, build hunks from the exact current lines." | `e42bac783` 2026-04-15 + `1d522567d` 2026-06-20 (apply_patch exact-line clause) | recorded: same tool-fluency `apply_patch` issues | incident | partly covered by `DefApplyPatch`'s own context rules | yes — **duplicate**: near-identical to tools.provider-openai_append.md.tmpl #3 |
| 4 | "If a supplied option is ignored or unsupported, treat the request as unfulfilled and adapt the procedure and arguments accordingly; do not assume its effect." | `3792aef9f` 2026-08-24 "fix(agent): make positive delegate waits atomic" (PR #405, "fix(agent): reject ignored delegation waits") | **recorded**: issue #365 — a trial requested a blocking wait from `delegate`; the tool's own response said the wait was ignored, but the agent proceeded as though it had happened, turning an intended blocking wait into a 5-round busy-poll, which fed a bad decision downstream (cancelling its own in-flight reviewer) | incident | n/a | yes |
| 5 | "Before you run any tool against an input you cannot regenerate — including a read meant only to look — inspect and experiment on a copy… The delivered artifact or process runs at the paths the requester named, under its standard observable identity… Disclose deviations." | `c05bd6cd4` 2026-08-24 (PR #400) | **recorded**: issues #392 and #359 — an *earlier, unscoped* version of this same copy-protection rule ("work from the copy") caused agents to deliver their final process from a `/tmp` copy instead of the task-named path (retest10 install-windows, cb5q2JA and sibling UZdnpjj); #359 adds a second collision (QEMU `-name` flag breaking process discovery) | incident | n/a | yes — notable: this rule has already been tightened once *because of a failure the broader version of itself caused* |
| 6 | "Batch independent tool calls into a single response — e.g. read multiple files at once instead of one per round… a study of identical review work found 62 tool calls when issued one per round versus 29 when batched." | Added `44d30a0bc` 2026-04-19, extended with the study figure `84ea57773` 2026-08-06 | **recorded**: commit's own "Study evidence: identical review work took 62 tool calls issued one-per-round versus 29 when batched" | incident | n/a | yes — **duplicate**: tools.provider-openai_append.md.tmpl #2 says almost the same thing, and (see note below) can render alongside this file for OpenAI-provider sessions |
| 7 | "Dependency-producing tools go first in their own response. When a later step needs a `delegate_id`, `job_id`, `watch_id`, readiness marker, or file content from an earlier tool result…" | `0e3abe827` 2026-06-20 | recorded: same observer-sidecar sequencing bugs as background-jobs.md #22-#29 | incident | n/a | yes |
| 8 | "A watched trigger depends on the watch creation result. Create the observer watch, read the `watch_id`, then trigger the watched event in the following response." | same as #7 | recorded (same) | incident | n/a | yes — related to background-jobs.md's numbered watch sequence |
| 9 | "When using the shell to search text or files, prefer `rg` or `rg --files` if available." | `e42bac783` 2026-04-15 (creation, no PR), stable since | none recorded | practice | **the codebase's dedicated `grep`/`glob` tools predate this line by ~2 months** (`81b41f873` 2026-02-08) and `DefGrep`'s own description calls itself "the direct tool for requests to grep, search text, find tokens…" | unclear/no for standalone search — a purpose-built tool already exists and is the more idiomatic choice; the shell-`rg` framing is only clearly useful for ad hoc pipelines |
| 10 | "After running commands, read errors carefully and fix them." | `e42bac783` 2026-04-15 (no PR) | none recorded | practice | n/a | yes |

---

## tools.provider-openai_append.md.tmpl

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "When you emit multiple tool calls in one response, they execute in the order you list them." | `9146beb5c` 2026-04-14 (no PR), replacing "Parallelize tool calls whenever possible… Use `multi_tool_use.parallel`" | **recorded implicitly**: the replaced rule assumed an OpenAI parallel-execution mechanism (`multi_tool_use.parallel`) that the new text says does not apply — the old rule had gone stale/wrong for the current API shape | incident (rule bitrot, not a trial) | n/a | unclear — accuracy depends on current OpenAI tool-calling semantics, not independently verified here |
| 2 | "Batch independent tool calls in one response whenever possible. If several reads, searches, checks, or subagent spawns do not depend on each other, issue them together instead of serializing the work." | `792cb6e5b` 2026-06-07 (no PR) | none recorded (incidental addition inside a delegation-focused commit) | practice | n/a | yes — **duplicate** of tools.md.tmpl #6 (see below) |
| 3 | "For `apply_patch` updates to an existing file, inspect the current content and build hunks from the exact current lines." | `1d522567d` 2026-06-20 (tool-fluency experiments) | recorded: tool-fluency experiments found `apply_patch` exact-line failures; post-fix reruns hit 3/3 | incident | partly covered by `DefApplyPatch`'s own context rules | yes — **duplicate** of tools.md.tmpl #3, gated to render only `{{if .HasTool "apply_patch"}}` |

**Confirmed structural duplicate**: `tools.md.tmpl` and this file are *both* unconditionally included in the assembled prompt for an OpenAI-backed session (see subagent.md.tmpl's `{{ section "tools" }}` plus the provider-append mechanism). An OpenAI session's batching instruction (tools.md.tmpl #6) and this file's #2 render in the same prompt, worded differently but instructing the identical thing.

---

## subagent.md.tmpl (literal prose only)

| # | Quote | Origin | Failure behind it | Class | Machinery overlap | Still plausible? |
|---|-------|--------|--------------------|-------|--------------------|-------------------|
| 1 | "Your `delegation_allowance` is {{ .DelegationAllowance }}: you may delegate, and each delegate you start may itself delegate with an allowance strictly smaller than yours." | `8bc3c26f3` 2026-06-13 (no PR) | none recorded | machinery | **covered** by `DefDelegate`'s `delegation_allowance` param | yes |
| 2 | "Delegate control uses stable `dlg_...` identities; `job_...` identities are reserved for shell jobs." | `fdbc7f2db` 2026-08-15 (no PR) | none recorded | machinery | covered | yes |
| 3 | "## Delegated task limits — If a delegated task explicitly requires tools or capabilities unavailable in this session, do not thrash or pretend to perform the missing capability." | `13c907ea0` 2026-04-16 "Fix subagent runtime and communicate output contract" (no PR) | none recorded | **duplicate** of workflow.md.tmpl #22 | n/a | yes, but redundant |
| 4 | "Report the mismatch promptly through `{{ .ResultToolName }}`. State what capability is missing and, when it is obvious from the task, what kind of agent or tool would be better suited." | same as #3 | none recorded | duplicate (partial — the agent-type-suggestion clause is not in workflow.md.tmpl) | n/a | yes |

**Confirmed structural duplicate**: this "Delegated task limits" block only renders for a *leaf* subagent (`{{ if .CanDelegate }}…{{ else }}` branch), but workflow.md.tmpl's near-identical rule (#22 in that table) is unconditional and renders for every session, leaf or not — so a leaf subagent's assembled prompt states this fact twice, in two different places.

---

# Summary

## Counts by class

Exact count of every row in the 18 tables above (17 files; subagent.md.tmpl's literal prose forms its own table), tallied by each row's Class column:

| Class | Count | Notes |
|---|---|---|
| practice | 95 | general engineering practice, no specific incident cited in the commit trail |
| incident | 68 | written after a specific eval trial, live-model probe, production bug, or roborev finding |
| machinery | 36 | explains tool/runtime mechanics (overlaps heavily with "covered" below) |
| duplicate | 2 | rows whose Class column is itself "duplicate" (subagent.md.tmpl #3 and #4, both restating workflow.md.tmpl #22) |
| **Total rows** | **201** | across the 18 tables above |

Several dozen more rows carry a class of practice/incident/machinery *and* a "— see duplicate note" pointer to another row (git-safety.md #5, tools.md.tmpl #3/#6, tools.provider-openai_append.md.tmpl #2/#3, identity.md #30, communicate.md.tmpl #5, workflow.md.tmpl #22) without being reclassified as "duplicate" themselves — those are enumerated in "Duplicates, grouped by topic" below rather than in this table, since each also has its own independent origin.

(Rows are not perfectly disjoint by class — e.g., several "machinery" rows are also incident-motivated, like background-jobs.md #8 and #18. The count above uses each row's primary class as tabulated in its table.)

## Rules with no recorded reason

The large majority of "practice" rows have no incident cited — too many to enumerate individually (see "none recorded" in each table above). The clearest concentrations:

- **identity.md #1-#21**: nearly the whole "Values" block (Transparency/Clarity/Pragmatism/Rigor principles, all the "Standards" bullets except the workspace-cleanup cluster) was written or reworded in four broad cleanup commits (`0d621d26c`, `9146beb5c`, `9c73c5a47`, `e42bac783`, `634fcf57b`) with no incident narrative — these are foundational engineering-practice statements, not failure repairs.
- **delegation.md's "good uses of subagents" list, brief-numbering, and research/report/commit-workflow bullets** (`792cb6e5b`, `144befd93`) — designed, not incident-driven.
- **task-tracking.md** — entirely practice, no incidents at all in its 5-commit history.
- **non-interactive.md.tmpl / non-interactive.agent-coordinator.md** — mostly practice except the `end_turn` migration and the "scout"→"inventory" rename.
- **ask-user.md.tmpl** — the whole section is a from-a-design-spec landing, not incident repair.

## Duplicates, grouped by topic

1. **Deliverable naming at completion** — identity.md #30 ("hand off every deliverable you created… with its path… and how to verify it") and communicate.md.tmpl #5 ("re-read the task's stated deliverables and name each one"). **Deliberate, acknowledged duplicate**: `289fba7ea`'s own commit message calls this "belt-and-suspenders for #302… identity.md governs the cleanup decision itself… dropping the identity side would let an agent reason a deliverable into clutter with no handoff state to name."

2. **Unavailable-capability reporting** — workflow.md.tmpl #22 ("report that mismatch promptly through your result tool instead of thrashing or pretending to perform the missing capability") and subagent.md.tmpl #3/#4 (near-identical text under "## Delegated task limits"). This one is **not** flagged as deliberate anywhere in the commit history — it reads as an accidental split: `92c7b13e7` (2026-04-16) put it in workflow.md.tmpl, and `13c907ea0`, the same day, put a near-identical block in subagent.md.tmpl's leaf-only branch. Both render for a leaf subagent today.

3. **`git add -A` prohibition** — git-safety.md #5 (blanket "NEVER use… `git add -A`") and delegation.md #28 ("Require the subagent to stage named paths only — never `git add -A`"). Different scopes (the agent's own git usage vs. what a delegation brief must require of a subagent) but same underlying concern, with delegation.md #28 citing a concrete incident (~1600-file staging) that git-safety.md's general rule does not cite. Also mirrors the user's own global CLAUDE.md rule ("NEVER use `git add -A` unless you've just done a `git status`") — a cross-document echo, not a same-file duplicate.

4. **`apply_patch` exact-line hunks** — tools.md.tmpl #3 and tools.provider-openai_append.md.tmpl #3. Near-identical wording in two files; the second is gated `{{if .HasTool "apply_patch"}}` but both are otherwise unconditional, so an OpenAI session with `apply_patch` sees this instruction twice.

5. **Batch independent tool calls** — tools.md.tmpl #6 and tools.provider-openai_append.md.tmpl #2. Both unconditional; both render for an OpenAI-backed session.

6. **"Stop looping" doctrine, three forms** — workflow.md.tmpl #6-#9 (stop surveying once a hypothesis is falsifiable; checkpoint after a no-new-evidence run), communicate.md.tmpl #7 (send a phase-change checkpoint), and context-management.md.tmpl #2 (stop after two incomplete implement/review/fix cycles). All three came out of the same `753fc0ac7`/`2d8ec4536` doctrine work (kata nbcf and the P5-plan posture sections) and address the same underlying failure mode — repeating a loop without a positive exit condition — from three different angles (tool-call level, reporting level, cycle level). Not literal duplicates, but a tight topic cluster worth reviewing together.

7. **Non-interactive-mode doctrine, two roles** — non-interactive.md.tmpl and non-interactive.agent-coordinator.md cover the same "no human available, make the call yourself" ground for two different roles (implementer vs. coordinator), independently trimmed over time. Not textually identical, but the same doctrine maintained twice.

## Machinery rules the tool descriptions already cover

Rules marked **covered** above (grouped by tool):

- **`delegate`**: delegation.md #2, #3, #4, #18, #19, #20, #21a-d, #25, #35, #36; subagent.md.tmpl #1. `DefDelegate`'s own description and per-field descriptions restate most of delegation.md's mechanics almost verbatim.
- **`delegate_send`**: delegation.md #6; background-jobs.md #24.
- **`job_status`/`job_list`/`job_stop`**: delegation.md #7, #8; background-jobs.md #4, #13.
- **`job_watch`**: background-jobs.md #6, #7, #8, #9; workflow.md.tmpl #17.
- **`manage_worktree`**: delegation.md #34.
- **`task_list`**: task-tracking.md #1, #6.
- **`communicate`**: communicate.md.tmpl #1, #2, #3, #4; non-interactive.md.tmpl #3.
- **`ask_user`**: ask-user.md.tmpl #3, #4, #5 — this section is close to a wholesale restatement of `DefAskUser`'s own description.
- **`compact_context`**: context-management.md.tmpl #1.
- **`find_session_transcripts`/`read_transcript`**: transcripts.md.tmpl #1, #4.

Rules marked **not covered** despite being machinery (genuine documentation gaps in the tool descriptions, not just redundancy):
- `DefShell` never documents automatic output bounding/digest behavior (workflow.md.tmpl #14, #15) or the pipefail-plus-tee scratch-artifact idiom (workflow.md.tmpl #18-#21), though it does now state the pipefail fact and the background/detached distinction.
- `DefJobWatch` never documents the self-influence breaker/fuse/depth-limiting safeguard at all (background-jobs.md #25), even though the prompt describes it in detail.
- The quiet-watchdog and `max_retained_terminal` reclamation mechanisms (background-jobs.md #18-#21) are autonomous behaviors with no corresponding callable tool, so by construction no tool description can cover them.

## Pairs of rules that collide or contradict

1. **"Every user-facing message goes through the result tool" vs. "a short internal disposition needs no tool call."** communicate.md.tmpl #1: *"Use {{ .ResultToolName }} for every user-facing report, requested status marker, readiness marker, request for input, and final answer."* versus communicate.md.tmpl #8 / background-jobs.md #12: *"when handling a `job_watch` frame that needs no action, a short internal no-action disposition is enough"* and *"When a notification needs no action, a one-line acknowledgment is enough."* The second pair are historically motivated carve-outs (`1ec2fb5ef`, `80d9ab3b0`) built specifically to stop models from either (a) calling a harmless no-op tool just to satisfy a "must call something" contract, or (b) getting scolded into a pointless retry for replying with plain text on a notification-driven turn — i.e., they exist precisely *because* rule #1's absolutism caused tool churn in the first place. The current text never states the carve-out as an explicit exception to #1; a model reading only communicate.md.tmpl's opening line would not know these system-initiated turns are exempt.

2. **The copy-protection rule vs. itself, historically.** tools.md.tmpl #5 (the current, scoped "inspect and experiment on a copy… the delivered artifact runs at the requester-named paths") is the *fix* for a failure that an earlier, unscoped version of the very same rule caused (issues #392/#359: "work from the copy" got over-applied to the delivered deliverable itself). Not a live contradiction today, but worth flagging: the rule has already round-tripped through causing the exact class of failure it exists to prevent, once.

3. **Identity.md's absolute "never remove a deliverable" vs. tasks whose own end state requires teardown.** This was an actual, roborev-documented contradiction in the *previous* wording of identity.md (before PR #617, issue #302): "Leave the workspace clean… remove… temporary artifacts" (unscoped) directly conflicted with a task that asked the agent to install-verify-uninstall something. The current wording (identity.md #22-#31) explicitly resolves this by scoping cleanup and deferring to "the handback state the task asked for" — so this is now a *resolved* historical contradiction, not a live one, but it's the clearest documented case of two rules in this codebase directly fighting each other in production.

---

**File**: `/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-evener-system-prompt-cleanup-69b0b3/3f5637ba-9f9e-45ae-afbe-e1501e0a0cd0/scratchpad/part2-rule-provenance.md`
