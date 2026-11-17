"""Run every arm from the three expert experiment lists against one TB 2.1 task.

Thirty experiments came back from three models asked independently. Each is a
prompt edit. This runner executes all of them, plus the vanilla control, plus a
length-matched control for each arm whose text is longer than a phrase, because
an arm that wins by being longer has not been shown to win by its content.

Delivery constraints, measured rather than assumed (see PROMPT-ARMS-RESULTS.md):

- `--system-prompt-append` lands at the very end of the system prompt, after the
  environment block. That is the highest-attention position and it is the only
  insertion point the one-shot CLI exposes.
- `--system-prompt FILE` replaces the doctrine while keeping the injected
  environment block, so an arm that needs a real section anchor is built by
  splicing its text into the doctrine at that anchor.
- `--system-prompt-as-user` moves the doctrine into the first user message.
- Nothing in the one-shot CLI injects text per turn. Arms that require it are
  recorded as unexecutable here with that reason, rather than approximated into
  something their author did not propose.

Usage:
  python3 run_all_experiments.py --task overfull-hbox --reps 3 --out /tmp/all-arms
  python3 run_all_experiments.py --report /tmp/all-arms
  python3 run_all_experiments.py --list
"""

from __future__ import annotations

import argparse
import dataclasses
import json
import os
import re
import shutil
import subprocess
import sys
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path

CACHE = Path.home() / ".cache/harbor/tasks/packages/terminal-bench"
OVERFULL_REF = "3c3b16e4b2de5fea90ca98db242b155a6843f9cc4dfd00be3479985a34bab4f7"

TASKS = {
    "overfull-hbox": {
        "fixture": CACHE / "overfull-hbox" / OVERFULL_REF / "environment/tests",
        "files": ["main.tex", "input.tex", "synonyms.txt"],
        "prompt": (
            'Ensure that the LaTeX document main.tex compiles successfully using the '
            'installed TeX distribution and pdflatex compiler with no "overfull hbox" '
            "warnings. In doing so, the only edits you may make are to replace words in "
            "input.tex with their specified synonyms in synonyms.txt (each line "
            "specifies a family of allowed synonyms). Do not edit main.tex or synonyms.txt."
        ),
        # A report that zero overfull boxes remain, matched against
        # whitespace-normalised tool output (see acceptance_pattern). The
        # A real report labels the count ("hbox count", "/underfull", "in
        # stdout"); echoed source calls it, "overfull(v)==0". The lookaheads
        # reject the call form, a hyphen glide (a workdir path like
        # ".../overfull-hbox-0"), a decimal (an "18.03pt" dimension) and a
        # comparison (a zero directly before "==").
        "acceptance": (
            r"overfull(?![-\w]*\()(?!-)[^0-9\n]{0,24}?0(?![.\d)\w])"
            r"|\b0(?![.\d=])\s+(?:lines?\s+(?:matching|with)\s+)?overfull(?!-)"
        ),
    },
}

BOOKKEEPING = {"communicate", "notes_agent_set", "notes_read"}

# Where an arm's text can actually land, given the one-shot CLI.
#   append      --system-prompt-append: the very end of the system prompt
#   doctrine:X  spliced into the doctrine at anchor X, then --system-prompt
#   subtract    doctrine edited in place with named deletions
#   as_user     doctrine plus the text, delivered as the first user message
#   task        appended to the task prompt
#   unexecutable: needs a delivery mechanism the CLI does not have
#
# `kind` is my classification of the experiment's form, from Jesse's ruling that
# prohibitions fail and redirection works, and from writing-skills' "Match the
# Form to the Failure": a prohibition plus rationalization table suits a
# discipline failure, a positive recipe or contract suits a shaping failure.
ARMS: dict[str, dict] = {
    # ---------------------------------------------------------------- control
    "baseline": {
        "source": "control",
        "kind": "control",
        "delivery": "none",
        "system": "",
        "task": "",
    },
    # ------------------------------------------------------- k3.1-pro (k01-10)
    "k01-unsure-not-wanted": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "task",
        "system": "",
        "task": (
            "If you are unsure whether an additional change or check is wanted, treat it "
            "as not wanted. When the requested deliverables satisfy every stated "
            "requirement, finish and report them. Unrequested improvements and extra "
            "confidence are not reasons to continue."
        ),
    },
    "k02-completion-checkpoint": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "hook",
        "system": "",
        "task": "",
    },
    "k03-requirement-line": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "append",
        "system": (
            "Before starting a new edit, search, or test after you have a candidate "
            "result, write one line: \"Requirement: <quoted task text or 'none'>; action: "
            "<planned action>.\" If the requirement is \"none,\" keep the current result "
            "and finish instead of doing that action. A need to establish that the current "
            "result meets a requirement is valid; a desire to improve it or learn more is "
            "not."
        ),
        "task": "",
    },
    "k04-subtractive": {
        "source": "k31pro",
        "kind": "subtraction",
        "delivery": "subtract",
        "system": "",
        "task": "",
    },
    "k05-nonbinding-criteria": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "doctrine:whole-spec",
        "system": (
            "Use the task's stated requirements to decide both correctness and "
            "completeness. Do not add an unstated objective such as the fewest edits, the "
            "best prose, the most general solution, or coverage of every invalid input. "
            "When a thought names an optional improvement, record no new task for it: "
            "finish if the stated requirements are already met, or leave it aside while "
            "meeting them."
        ),
        "task": "",
    },
    "k06-close-background": {
        "source": "k31pro",
        "kind": "mixed",
        "delivery": "doctrine:background",
        "system": (
            "A result found while the deliverable already satisfies the stated "
            "requirements cannot make it more complete. Once you have that passing "
            "deliverable, stop searches, delegates, and jobs whose purpose is to improve "
            "it, disregard their pending results, and finish. Do not launch such work "
            "speculatively while waiting for an acceptance check. Keep only work required "
            "to produce, install, or validate the requested result."
        ),
        "task": "",
    },
    "k07-bank-candidate": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "task",
        "system": "",
        "task": (
            "Whenever a candidate satisfies the stated requirements, immediately put that "
            "exact version in the required destination and run the acceptance check there "
            "before considering anything else. If you later make an unrequested change and "
            "the result no longer passes, restore the known-good version and finish."
        ),
    },
    "k08-first-satisfying": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "doctrine:simplest",
        "system": (
            "When searching for a result that must satisfy a condition, use a "
            "first-satisfying search unless the task explicitly requests all results, a "
            "proof of completeness, or the best result. Return the first result that "
            "satisfies the condition. Do not enumerate the remaining space to establish "
            "that a satisfying result is satisfying."
        ),
        "task": "",
    },
    "k09-verification-cases": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "task",
        "system": "",
        "task": (
            "Before verifying, list the cases needed to show each stated requirement on "
            "its required inputs. Test those cases. Treat cases outside that list, "
            "including invalid inputs the task does not mention, as unanswered questions "
            "rather than failures of the deliverable. Finish after the listed cases pass; "
            "do not expand the list to increase confidence."
        ),
    },
    "k10-reframe-delivery": {
        "source": "k31pro",
        "kind": "redirection",
        "delivery": "append",
        "system": (
            "Treat a result that meets the task's stated requirements as complete, not as "
            "a rough draft. Finishing then is successful performance of the requested "
            "task. More edits, broader tests, and better presentation do not earn "
            "additional credit unless the task requests them. Carry any unrequested idea "
            "only as a sentence in the report, not as more work."
        ),
        "task": "",
    },
    # ---------------------------------------------------------- astra (a01-10)
    "a01-scope-mandates": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:leave-better",
        "system": (
            "Scope of these working rules: they govern how you satisfy the task; they do "
            "not create additional deliverables. \"Leave the code better\" means make the "
            "requested change without introducing defects, not add a cleanup project. "
            "\"Run the rest of the suite\" means the existing applicable suite, not invent "
            "a new assurance campaign. Keeping a change small means respecting its scope, "
            "not searching for the smallest possible valid solution. A task that asks for "
            "optimization or broader assurance does require that work. Otherwise, treat "
            "craftsmanship preferences as constraints on required work, not independent "
            "goals that remain open after the requirements are established."
        ),
        "task": "",
    },
    "a02-bounded-proof-plan": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:small-path",
        "system": (
            "Before substantive work, use your existing plan or notes to record the task's "
            "required outcomes, allowed-change restrictions, delivery locations, and the "
            "observable evidence needed for each. Include requirements entailed by the "
            "request, not only its example cases. Choose evidence that can actually "
            "establish those requirements. Keep this acceptance plan separate from "
            "optional ideas. Revise it when the request changes or an observation exposes "
            "a missing requirement or an inadequate check; do not enlarge it merely "
            "because another improvement or experiment becomes interesting. An explicit "
            "optimization objective belongs in the plan. Once an item has adequate "
            "evidence, mark that item discharged rather than replacing it with a harder "
            "version."
        ),
        "task": "",
    },
    "a03-worked-examples": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:decisions",
        "system": (
            "Examples of choosing the next action:\n\n"
            "Request: Convert the supplied table to JSON at the named path, preserving all "
            "records and values.\n"
            "Observation: The output is at that path. It parses, and an independent "
            "comparison establishes preservation of every record and value.\n"
            "Correct next action: Complete the required report and finishing duties.\n"
            "Incorrect next action: Reformat the valid JSON to make it more elegant.\n\n"
            "Request: Repair a named regression and keep the existing test suite "
            "passing.\n"
            "Observation: The regression test and the applicable existing suite have "
            "passed on the delivered change.\n"
            "Correct next action: Report the change and those results.\n"
            "Incorrect next action: Start an unrelated stress-testing campaign to feel "
            "more certain.\n\n"
            "Request: Write a validator that accepts valid signatures and rejects invalid "
            "signatures.\n"
            "Observation: Only acceptance of a valid signature has been checked.\n"
            "Correct next action: Establish the required rejection behavior. This task is "
            "not yet verified.\n\n"
            "Request: Find the smallest valid artifact by exhaustively checking the "
            "supplied finite candidate set.\n"
            "Observation: A valid artifact has been found, but more candidates remain and "
            "the request asks for the smallest.\n"
            "Correct next action: Examine the remaining candidates, because the request "
            "requires the optimum."
        ),
        "task": "",
    },
    "a04-tool-warrant": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:check-finished",
        "system": (
            "Before a substantive tool call, give a short warrant in its existing intent "
            "field, or in one sentence if the tool has no such field: \"Requirement: "
            "[specific task obligation]. Gap: [missing evidence, observed failure, or "
            "unfinished delivery]. This call: [how it closes that gap].\" A requirement "
            "must come from the task or an applicable standing constraint, not from your "
            "preference for a tidier result. If existing evidence already closes the gap, "
            "the warrant is invalid. Necessary reporting, notes, and cleanup are finishing "
            "duties, not excuses for more substantive work. When no substantive call has a "
            "valid warrant, perform only the remaining finishing duties and report."
        ),
        "task": "",
    },
    "a05-deliver-candidate": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:finishing",
        "system": (
            "Candidate success and delivered success are different. Once a candidate has "
            "adequate evidence for all task requirements, make that candidate the "
            "deliverable at the requested locations before considering any alternative. "
            "Preserve the task's edit restrictions and permissions during this handoff. "
            "Establish that the delivered result is the validated candidate and check any "
            "requirement the handoff could invalidate. Do not delay this handoff to "
            "compare equivalent candidates. Keep the accepted deliverable intact; reopen "
            "substantive changes only for concrete evidence of an unmet requirement or a "
            "changed request. A correct copy left only in scratch is not delivery."
        ),
        "task": "",
    },
    "a06-expire-background": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:background",
        "system": (
            "A background job is owed attention only while its result is needed for an "
            "unfinished task obligation. When launching one, identify that obligation in "
            "the job's existing description or intent. If another result satisfies the "
            "obligation, reassess the job immediately: sunk effort is not a reason to wait "
            "for it. Cancel a search whose remaining results could only improve an already "
            "accepted answer, and clear watches that served only that search. Do not wait "
            "for its natural completion or consume late results as a new assignment. Keep "
            "jobs needed for an unmet requirement, and preserve services the user "
            "explicitly requested left running. Cancel safely without damaging the "
            "accepted deliverable."
        ),
        "task": "",
    },
    "a07-narrower-claim": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:reporting",
        "system": (
            "Match the breadth of your report to the evidence already obtained. Use the "
            "existing final report to say: \"Satisfied: [required outcomes]. Evidence: "
            "[checks actually completed]. Not established: [relevant properties outside "
            "the request, if any].\" An unrequested property may remain unestablished "
            "without becoming unfinished work. Do not expand the work merely to support a "
            "broader adjective such as robust, exhaustive, or fully tested. If a "
            "limitation instead exposes an unmet task requirement, resolve it or report "
            "the task incomplete; do not relabel it optional. No additional report or "
            "checklist is needed."
        ),
        "task": "",
    },
    "a08-reuse-evidence": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "doctrine:check-passed",
        "system": (
            "Treat a completed check as reusable evidence for the property, artifact "
            "contents, inputs, and environment it actually covered. Record that "
            "association briefly in your existing notes when needed. Before repeating the "
            "check, identify a relevant change, new contradictory observation, or an "
            "explicit repeated-testing requirement. Without one, cite the existing result. "
            "A new behavior not covered by the earlier check still needs evidence if the "
            "task requires it. Do not run commands solely to refresh your confidence or "
            "obtain a newer timestamp for an unchanged result."
        ),
        "task": "",
    },
    "a09-delivery-cost": {
        "source": "astra",
        "kind": "redirection",
        "delivery": "task",
        "system": "",
        "task": (
            "My preference is correctness first, then economical delivery. Between actions "
            "that satisfy the same complete request and required checks, prefer the one "
            "with less remaining tool use and waiting. When choosing another action, "
            "compare it with delivering the result already supported by the evidence, not "
            "merely with doing nothing. Extra quality outside my request does not "
            "compensate for extra time. Use this as a qualitative tie-breaker, not a new "
            "optimization project. Never save effort by dropping a requirement, and "
            "retain any optimization objective that the request actually includes."
        ),
    },
    "a10-reminder-at-decision": {
        "source": "astra",
        "kind": "prohibition",
        "delivery": "hook",
        "system": "",
        "task": "",
    },
    # --------------------------------------------------------- carbon (c01-10)
    "c01-intent-schema-gate": {
        "source": "carbon",
        "kind": "redirection",
        "delivery": "schema:intent",
        "system": (
            'Begin with either "Unmet: <the literal task-prompt requirement not yet '
            'satisfied>" or "Finish: <copy passing output to target | verify target '
            'once | stop background job | clean scratch | report>". Once every literal '
            'requirement in the task prompt passes on the target path, only "Finish:" '
            "calls are valid--do not call this tool to minimize diffs, polish, or test "
            "unrequested failure inputs."
        ),
        "task": "",
    },
    "c02-clean-exit-reminder": {
        "source": "carbon",
        "kind": "redirection",
        "delivery": "hook",
        "system": "",
        "task": "",
    },
    "c03-lexical-disambiguation": {
        "source": "carbon",
        "kind": "redirection",
        "delivery": "doctrine:decisions-end",
        "system": (
            "\"The whole specification\" is strict: satisfy the literal conditions in the "
            "task prompt, not unstated craft ideals. Specifically within this prompt: "
            "\"replace temporary work with the real implementation\" applies only to stubs "
            "or hardcoded fakes that violate the task prompt, never to a valid solution "
            "that uses more edits or simpler code than strictly minimal; \"leave the code "
            "you touch better than you found it\" and \"prove the triggering scenario ran\" "
            "apply only when fixing a bug against an existing codebase's test suite, never "
            "to inventing unrequested malformed or adversarial inputs for a newly created "
            "artifact or script; and \"raise what the person missed\" or \"say what you did "
            "not test\" belongs in one sentence of your final report, never in extra "
            "implementation or test runs."
        ),
        "task": "",
    },
    "c04-atomic-promotion": {
        "source": "carbon",
        "kind": "mixed",
        "delivery": "doctrine:background",
        "system": (
            "The instant any candidate in $EVENER_SCRATCH_DIR or the workspace passes a "
            "check of the task's stated requirements, immediately copy that passing file "
            "to its target deliverable path and verify it once in place before taking any "
            "other action, then call job_stop on every exploratory, search, or "
            "alternative-candidate background job or delegate still running. Never poll, "
            "wait on, or drain a background search for a smaller or cleaner variant once a "
            "passing artifact exists."
        ),
        "task": "",
    },
    "c05-pass-check-header": {
        "source": "carbon",
        "kind": "redirection",
        "delivery": "task",
        "system": "",
        "task": (
            "[Execution boundary: Before running your first command, write two lines in "
            "your reasoning - PASS CHECK: the minimum command(s) and observable outputs on "
            "the target path that prove every literal requirement above is met; NON-GOALS: "
            "optimizations, edit-count reductions, polish, or negative/invalid-input test "
            "cases not requested above. Treat PASS CHECK as frozen: the first time PASS "
            "CHECK passes on the target path, perform zero NON-GOALS and proceed directly "
            "to scratch cleanup, whiteboard update, and communicate(end_turn=true).]"
        ),
    },
    "c06-rationalization-table": {
        "source": "carbon",
        "kind": "rationalization-table",
        "delivery": "doctrine:finishing",
        "system": "",
        # Markdown rows, appended to the existing "When you think / Do this instead"
        # table, which is where this text belongs. Spliced by anchor below.
        "table_rows": (
            "| \"It passes, now let me see if I can do it with fewer edits, cleaner style, "
            "or a tighter result.\" | Keep the passing result. Ensure it is at the target "
            "path, clean scratch, update the whiteboard, and send your report. |\n"
            "| \"My script or check passes on the valid target; let me craft broken or "
            "edge-case inputs to prove it rejects them.\" | Unless the task prompt asked for "
            "tests or named invalid cases, read your script once to confirm each required "
            "check is real, run it on the task's target, and finish. |\n"
            "| \"The task prompt doesn't say whether optimization or extra checks are "
            "wanted, so continuing is safer.\" | Silence in the task prompt means out of "
            "scope. Once stated requirements pass on the target path, extra turns add "
            "timeout and regression risk - finish now. |"
        ),
        "task": "",
    },
    "c07-first-sufficiency-plan": {
        "source": "carbon",
        "kind": "mixed",
        "delivery": "doctrine:howyouwork-end",
        "system": (
            "Plan only to first sufficiency: never add a second pass for optimization, "
            "diff minimization, prose/code polishing, or unrequested negative testing to "
            "your initial plan or task_list. If an early step already satisfies every "
            "literal requirement in the task prompt, mark any remaining build or "
            "refinement tasks cancelled and move straight to Finishing rather than "
            "completing the plan."
        ),
        "task": "",
    },
    "c08-expected-utility": {
        "source": "carbon",
        "kind": "redirection",
        "delivery": "doctrine:context-room",
        "system": (
            "Context room is not clock room: an external wall-clock timer can terminate "
            "this run without warning on any turn, grading whatever sits on disk at that "
            "moment. Evaluation is strictly binary on the stated task conditions - zero "
            "extra credit is ever given for fewer edits, cleaner style, faster code, or "
            "unrequested negative tests, while every turn spent editing or searching after "
            "the target already passes strictly lowers expected success by risking clock "
            "termination or a last-minute regression."
        ),
        "task": "",
    },
    "c09-caveat-safe-harbor": {
        "source": "carbon",
        "kind": "redirection",
        "delivery": "schema:communicate",
        "system": (
            'Invoke communicate(end_turn=true) as soon as every deliverable or answer '
            "explicitly requested in the task prompt is verified in place (and "
            "scratch/whiteboard closing steps are complete). Naming unrequested "
            'optimizations or negative tests you intentionally skipped in message fully '
            'satisfies "say what you assumed and what you did not test"--never delay '
            "end_turn=true to eliminate those caveats with extra tool calls."
        ),
        "task": "",
    },
    "c10-first-witness-example": {
        "source": "carbon",
        "kind": "redirection",
        "delivery": "doctrine:report-examples",
        "system": (
            "A report where the first valid solution is enough:\n\n"
            "> `config/routes.yaml` routes all eight legacy paths with zero lint warnings "
            "(`route-lint config/routes.yaml` exited 0). Twelve explicit rules passed on "
            "the first build; collapsing them into fewer regexes or synthesizing malformed "
            "configs to negative-test `route-lint` was not asked for, so I left the "
            "twelve-rule file in place."
        ),
        "task": "",
    },
}

# Anchors in the doctrine, in the words the prompt actually uses. Splicing at a
# real anchor is what distinguishes an arm that says "put this under Finishing"
# from one that merely appends to the end.
ANCHORS = {
    "whole-spec": "The task prompt is the whole specification",
    "simplest": "Do the simplest thing that could possibly work",
    "check-finished": "Check finished work against the task's own requirements",
    "check-passed": "A check passed when it ran to the end",
    "leave-better": "Leave the code you touch better than you found it",
    "small-path": "Get a small end-to-end path running early",
    "decisions": "## Decisions",
    "decisions-end": "## How you work",
    "howyouwork-end": "## Delegating",
    "background": "## Background work",
    "reporting": "## Reporting",
    "finishing": "## Finishing",
    "report-examples": "When your answer turns on how parts connect",
    "context-room": "you have room: don't stop or hand off work because of context length",
}

VANILLA_CONFIG = "evener-bench-config"
DOCTRINE_CACHE = "doctrine-vanilla.txt"


@dataclass
class RunResult:
    arm: str
    task: str
    rep: int
    source: str = ""
    kind: str = ""
    delivery: str = ""
    exit_code: int | None = None
    error: str = ""
    timed_out: bool = False
    wall_s: float = 0.0
    total_calls: int = 0
    acceptance_call_index: int | None = None
    calls_after_acceptance: int = 0
    tail_work_calls: int = 0
    runaway_share: float = 0.0
    final_compiles_clean: bool | None = None
    # Where `final_compiles_clean` came from. A fresh run verifies the workdir it
    # still holds. A reanalyse can only re-verify a row whose workdir survived,
    # so every other row carries its recorded verdict and says so.
    verdict_source: str = ""
    verify_reason: str = ""
    saw_clean_in_transcript: bool | None = None
    prompt_chars: int = 0
    prompt_is_vanilla: bool | None = None
    notes: list[str] = field(default_factory=list)


def fail(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    raise SystemExit(1)


def build_workspace(task: str, dest: Path) -> None:
    spec = TASKS[task]
    dest.mkdir(parents=True, exist_ok=True)
    for name in spec["files"]:
        src = spec["fixture"] / name
        if not src.exists():
            fail(f"fixture file missing: {src}")
        shutil.copy(src, dest / name)


def render_doctrine(out: Path) -> str:
    """The vanilla doctrine, captured once from a real run and cached."""
    cached = out / DOCTRINE_CACHE
    if cached.exists():
        return cached.read_text()
    fail(
        f"missing {cached}; run the capture step first (see --capture-doctrine), "
        "or copy a known-good doctrine there"
    )


def apply_arm(arm: str, out: Path, doctrine: str) -> tuple[Path | None, bool]:
    """Build the system-prompt file for an arm.

    Returns (path, as_user). `path` is None when the arm leaves the system prompt
    untouched, which is the only case that should reproduce the baseline prompt.
    """
    spec = ARMS[arm]
    delivery = spec["delivery"]

    if delivery in ("none", "task") or delivery.startswith("schema:"):
        return None, False

    if delivery == "append":
        path = out / f"sp-{arm}.txt"
        path.write_text(doctrine + "\n\n" + spec["system"].strip() + "\n")
        return path, False

    if delivery == "subtract":
        # k04's named deletions, applied literally.
        text = doctrine
        old_open = (
            "you do it the way a seasoned expert in that field would: you think it "
            "through first, act on your own judgment, raise what the person missed, and "
            "push back when something is wrong"
        )
        new_open = "you satisfy the task's stated requirements, check that result, and report it"
        if old_open not in text:
            fail(f"{arm}: opening clause not found verbatim in the doctrine")
        text = text.replace(old_open, new_open)

        old_better = "Leave the code you touch better than you found it, and stay on your task."
        if old_better not in text:
            fail(f"{arm}: 'leave the code better' clause not found verbatim")
        text = text.replace(old_better, "Stay on your task.")

        # The "I'll keep this copy or log as evidence" table row, deleted whole.
        row = re.compile(r'^\| "I.ll keep this copy or log as evidence\.".*\n', re.MULTILINE)
        if not row.search(text):
            fail(f"{arm}: the evidence-table row was not found verbatim")
        text = row.sub("", text)

        path = out / f"sp-{arm}.txt"
        path.write_text(text)
        return path, False

    if delivery == "as_user":
        path = out / f"sp-{arm}.txt"
        path.write_text(doctrine + "\n\n" + spec["system"].strip() + "\n")
        return path, True

    if delivery.startswith("doctrine:"):
        anchor_key = delivery.split(":", 1)[1]
        anchor = ANCHORS[anchor_key]

        if "table_rows" in spec:
            # Append rows to the existing table, which ends at the blank line
            # before the next heading.
            m = re.search(
                r'(\| When you think \| Do this instead \|.*?)(\n\n)',
                doctrine, re.DOTALL,
            )
            if not m:
                fail(f"{arm}: the 'When you think' table was not found")
            text = doctrine[: m.end(1)] + "\n" + spec["table_rows"] + doctrine[m.end(1):]
        else:
            idx = doctrine.find(anchor)
            if idx < 0:
                fail(f"{arm}: anchor {anchor!r} not found in the doctrine")
            if anchor_key in ("decisions-end", "howyouwork-end"):
                # Insert immediately before the next heading, i.e. at the end of
                # the section starting at this anchor.
                text = doctrine[:idx] + spec["system"].strip() + "\n\n" + doctrine[idx:]
            else:
                # Insert immediately after the paragraph containing the anchor.
                end = doctrine.find("\n\n", idx)
                if end < 0:
                    end = len(doctrine)
                text = doctrine[:end] + "\n\n" + spec["system"].strip() + doctrine[end:]

        path = out / f"sp-{arm}.txt"
        path.write_text(text)
        return path, False

    return None, False


def run_once(arm: str, task: str, rep: int, out: Path, timeout: int,
             effort: str, model: str, keep: bool) -> RunResult:
    spec = TASKS[task]
    arm_spec = ARMS[arm]
    res = RunResult(arm=arm, task=task, rep=rep, source=arm_spec["source"],
                    kind=arm_spec["kind"], delivery=arm_spec["delivery"])
    workdir = out / f"work/{arm}-{task}-{rep}"
    state = out / f"state/{arm}-{task}-{rep}"
    shutil.rmtree(workdir, ignore_errors=True)
    shutil.rmtree(state, ignore_errors=True)
    build_workspace(task, workdir)
    state.mkdir(parents=True, exist_ok=True)

    args = [
        os.environ.get("EVENER_EXPERIMENT_BIN", "evener"), "--model", model,
        "--reasoning-effort", effort,
        "--disable-memory",
        "--no-default-marketplaces",
        *([] if arm_spec["delivery"] == "hook" else ["--enabled-plugins", ""]),
        "--state-dir", str(state),
        "--dir", str(workdir),
        "--max-rounds", "0",
        "--verbose",
    ]

    if arm_spec["delivery"] == "hook":
        # A hook arm's text arrives through the plugin's PostToolBatch hook. The
        # plugin dir is named after the arm, so a missing one is a loud failure
        # rather than a silently unmodified run.
        plugin = out / "hookplugins" / arm
        if not (plugin / "hooks/hooks.json").exists():
            fail(
                f"{arm}: missing hook plugin at {plugin}. The hook arms need "
                "plugins built by build_hook_plugins.py, which is not in this "
                f"repository. Run the other arms with --arms, or supply {plugin}."
            )
        args += ["--plugin-dir", str(plugin)]

    doctrine = render_doctrine(out)
    sp_path, as_user = apply_arm(arm, out, doctrine)
    if sp_path is not None:
        args += ["--system-prompt", str(sp_path)]
        if as_user:
            args += ["--system-prompt-as-user"]

    args += [spec["prompt"].format(dir=str(workdir)) + arm_spec["task"]]

    started = time.time()
    env = dict(os.environ)
    cfg_root = out / VANILLA_CONFIG
    cfg_root.mkdir(parents=True, exist_ok=True)
    env["XDG_CONFIG_HOME"] = str(cfg_root)
    delivery = arm_spec["delivery"]
    if delivery.startswith("schema:"):
        seam = delivery.split(":", 1)[1]
        var = {
            "intent": "EVENER_EXPERIMENT_INTENT_APPEND",
            "communicate": "EVENER_EXPERIMENT_COMMUNICATE_APPEND",
        }[seam]
        env[var] = arm_spec["system"].strip()
        if env.get("EVENER_EXPERIMENT_BIN", "evener") == "evener":
            fail(
                f"{arm}: a schema arm needs EVENER_EXPERIMENT_BIN pointing at a "
                "binary patched to read EVENER_EXPERIMENT_*_APPEND, which is not "
                "in this repository. Run the other arms with --arms, or set that "
                "variable."
            )
    log_path = out / f"logs/{arm}-{task}-{rep}.log"
    log_path.parent.mkdir(parents=True, exist_ok=True)
    try:
        proc = subprocess.run(
            args, cwd=str(workdir), capture_output=True, text=True, timeout=timeout,
            env=env,
        )
        res.exit_code = proc.returncode
        log_path.write_text(proc.stdout + proc.stderr)
    except subprocess.TimeoutExpired as exc:
        res.exit_code = None
        res.error = "timeout"
        res.timed_out = True
        log_path.write_text(
            (exc.stdout or "") + (exc.stderr or "") if isinstance(exc.stdout, str)
            else str(exc.stdout) + str(exc.stderr)
        )
    res.wall_s = round(time.time() - started, 1)

    res.final_compiles_clean, res.verify_reason = verify_final_state(task, workdir)
    res.verdict_source = "recomputed"
    analyse(res, state, out)
    if res.final_compiles_clean is not None and res.saw_clean_in_transcript is not None:
        if res.saw_clean_in_transcript and not res.final_compiles_clean:
            res.notes.append(
                "transcript showed a clean build but the final workspace compiles dirty"
            )
        elif res.final_compiles_clean and not res.saw_clean_in_transcript:
            res.notes.append(
                "final workspace compiles clean but no tool result said so"
            )
    if not keep:
        shutil.rmtree(workdir, ignore_errors=True)
    return res


def acceptance_pattern(task: str) -> re.Pattern:
    """The task's acceptance pattern, compiled against normalised whitespace.

    Tool output wraps and indents freely, so a report like
    "=== overfull hbox count ===\n0" reaches the model as two lines with a rule
    between them. Collapsing runs of whitespace before matching keeps the
    pattern readable and stops a formatting character from hiding acceptance.
    """
    return re.compile(TASKS[task]["acceptance"], re.IGNORECASE)


def normalise_whitespace(text: str) -> str:
    return re.sub(r"\s+", " ", text)


def analyse(res: RunResult, state: Path, out: Path) -> None:
    sessions = state / "sessions"
    metas = sorted(sessions.glob("*.meta.json"))
    roots = []
    for m in metas:
        try:
            meta = json.loads(m.read_text())
        except Exception:
            continue
        if not meta.get("parent_session_id"):
            roots.append(m)
    target = roots[0] if roots else (metas[0] if metas else None)
    if target is None:
        res.notes.append("no session meta found")
        return

    tid = target.name[: -len(".meta.json")]
    tf = sessions / f"{tid}.transcript.jsonl"
    if not tf.exists():
        res.notes.append("no transcript found")
        return
    rows = [json.loads(line) for line in tf.open()]

    header = next((r for r in rows if r.get("kind") == "header"), None)
    if header:
        sp = header.get("system_prompt") or ""
        res.prompt_chars = len(sp)
        # Vanilla means the doctrine is the shipped one: no personal doc, no
        # plugin text. The pilot's prompt had none of these.
        res.prompt_is_vanilla = (
            "AGENTS.md" not in sp and "marketplace" not in sp and "plugin" not in sp
        )

    # One entry per tool call, in call order: the name comes from the tool_call
    # part, the evidence from the matching tool_result. They are paired by id
    # rather than by position so a dropped result cannot shift the rest.
    # Scanning whole turns instead would let the agent's own prose ("0 overfull
    # now") count as acceptance, which is the claim being scored.
    names: dict[str, str] = {}
    order: list[str] = []
    results: dict[str, str] = {}
    for row in rows:
        if row.get("kind") != "entry":
            continue
        turn = row.get("turn") or {}
        for part in ((turn.get("message") or {}).get("content")) or []:
            if not isinstance(part, dict):
                continue
            if part.get("kind") == "tool_call":
                call = part.get("tool_call") or {}
                cid = call.get("id") or f"anon-{len(order)}"
                names[cid] = call.get("name") or "?"
                order.append(cid)
            elif part.get("kind") == "tool_result":
                result = part.get("tool_result") or {}
                results[result.get("tool_call_id") or ""] = (
                    result.get("content") or ""
                )
    calls: list[tuple[int, str, str]] = [
        (i, names[cid], results.get(cid, "")) for i, cid in enumerate(order)
    ]
    # results holds the raw result body, so an acceptance pattern written with
    # real newlines can match shell output as the model saw it.
    res.total_calls = len(calls)

    pattern = acceptance_pattern(res.task)
    acc_idx = None
    for i, _name, text in calls:
        if pattern.search(normalise_whitespace(text)):
            acc_idx = i
            break
    res.saw_clean_in_transcript = acc_idx is not None
    res.acceptance_call_index = acc_idx
    if acc_idx is not None:
        tail = calls[acc_idx + 1:]
        res.calls_after_acceptance = len(tail)
        res.tail_work_calls = sum(1 for _i, n, _t in tail if n not in BOOKKEEPING)
        if res.total_calls:
            res.runaway_share = round(res.calls_after_acceptance / res.total_calls, 4)
    else:
        # Nothing measured. A reanalyse of a row whose acceptance the current
        # detector no longer finds must not keep the previous detector's tail:
        # those values describe a measurement that is no longer there.
        res.calls_after_acceptance = 0
        res.tail_work_calls = 0
        res.runaway_share = 0.0


def verify_final_state(task: str, workdir: Path) -> tuple[bool | None, str]:
    """Re-run the task's own check on the workspace the run left behind.

    Returns the verdict and the condition that produced it, so a pass can be
    audited from runs.jsonl after the workdir is gone.
    """
    if task != "overfull-hbox":
        return None, "no oracle for this task"
    main = workdir / "main.tex"
    input_tex = workdir / "input.tex"
    if not main.exists() or not input_tex.exists():
        return False, "workspace missing main.tex or input.tex"
    started = time.time()
    try:
        proc = subprocess.run(
            ["pdflatex", "-interaction=nonstopmode", "main.tex"],
            cwd=str(workdir), capture_output=True, text=True, timeout=300,
        )
    except subprocess.TimeoutExpired:
        return False, "pdflatex timed out"
    log = ""
    for candidate in ("main.log",):
        p = workdir / candidate
        if p.exists():
            log = p.read_text(errors="replace")
    if proc.returncode != 0:
        # pdflatex returns nonzero on some warnings; the log is authoritative.
        if "Fatal error occurred" in log or not (workdir / "main.pdf").exists():
            return False, "pdflatex failed"
    # The task's own verifier asks for this marker, not just a zero exit.
    if "Output written on main.pdf" not in log:
        return False, "no 'Output written on main.pdf' marker"
    overfull = re.findall(r"^Overfull \\hbox", log, re.MULTILINE)
    if overfull:
        return False, f"{len(overfull)} overfull hbox lines remain"
    # The task forbids editing main.tex and synonyms.txt, and its verifier
    # compares them byte for byte against the fixture copies.
    fixture = TASKS["overfull-hbox"]["fixture"]
    for name in ("main.tex", "synonyms.txt"):
        if (workdir / name).read_bytes() != (fixture / name).read_bytes():
            return False, f"{name} was modified"
    if not substitutions_legal(input_tex):
        return False, "input.tex has an illegal substitution"
    return True, "compiles clean with only legal substitutions"


def substitutions_legal(input_tex: Path) -> bool:
    """The task's token-by-token rule: same tokens, each word swapped within its
    synonym family, and punctuation untouched.

    This is the upstream verifier's comparison, not a weaker proxy: it zips the
    original and the agent's file so a deleted, added or reordered token fails.
    """
    fixture = TASKS["overfull-hbox"]["fixture"]
    original = (fixture / "input.tex").read_text(errors="replace")
    agent_text = input_tex.read_text(errors="replace")

    families: dict[str, set[str]] = {}
    for line in (fixture / "synonyms.txt").read_text(errors="replace").splitlines():
        words = {w.strip() for w in line.strip().split(",") if w.strip()}
        for word in words:
            families[word] = words

    original = original.replace("---", " --- ")
    agent_text = agent_text.replace("---", " --- ")
    tokens = re.findall(r"\S+", original)
    agent_tokens = re.findall(r"\S+", agent_text)
    if len(tokens) != len(agent_tokens):
        return False
    for token, agent_token in zip(tokens, agent_tokens):
        m = re.match(r"^(\W*)(.*?)(\W*)$", token)
        a = re.match(r"^(\W*)(.*?)(\W*)$", agent_token)
        if not m:
            if a:
                return False
            continue
        if not a:
            return False
        p, w, s = m.groups()
        p_agent, w_agent, s_agent = a.groups()
        if p != p_agent or s != s_agent:
            return False
        if w != w_agent and not (w in families and w_agent in families[w]):
            return False
    return True


def load_runs(path: Path) -> list[RunResult]:
    """Read a runs.jsonl, keeping the fields this version still knows.

    A row outlives the dataclass that wrote it: an older run's record can carry
    a field this version no longer has, or lack one it now requires. Read what
    the row still offers rather than dying at report time.
    """
    known = {f.name for f in dataclasses.fields(RunResult)}
    return [
        RunResult(**{k: v for k, v in json.loads(line).items() if k in known})
        for line in path.open()
        if line.strip()
    ]


def reanalyse_root(root: Path) -> list[RunResult]:
    """Re-derive what a finished run root still holds, without destroying what it does not.

    A reanalyse exists to put a detector fix into effect on runs already paid
    for: the transcript is re-read so the tail metrics reflect the current
    pattern. It cannot re-derive the pass verdict. `verify_final_state` runs
    `pdflatex` against the workdir, and a run keeps its workdir only under
    `--keep`; the default deletes it once the verdict is recorded.

    So the verdict is recomputed exactly where it can be, from a workdir that
    still exists, and otherwise carried through untouched. Writing `None` there
    was the bug in #6597: it turned 3/3 into 0/3 on rows whose verdict had been
    measured correctly, and nothing left on disk could restore it. Each row
    records which happened in `verdict_source`, so a carried verdict is never
    mistaken for a fresh one.

    The rewrite is atomic: a crash mid-write must not cost the rows that were
    already there.
    """
    runs_path = root / "runs.jsonl"
    rows = load_runs(runs_path)
    for r in rows:
        state = root / f"state/{r.arm}-{r.task}-{r.rep}"
        workdir = root / f"work/{r.arm}-{r.task}-{r.rep}"
        if state.exists():
            analyse(r, state, root)
        if workdir.exists():
            r.final_compiles_clean, r.verify_reason = verify_final_state(r.task, workdir)
            r.verdict_source = "recomputed"
        else:
            r.verdict_source = "reused"
            note = (
                f"reanalyse kept the recorded verdict ({r.final_compiles_clean!r}); "
                "the workdir is gone, so it cannot be re-derived"
            )
            # A second reanalyse must not stack copies of the same note.
            if not any(n.startswith("reanalyse kept the recorded verdict") for n in r.notes):
                r.notes.append(note)
    tmp = runs_path.with_suffix(".jsonl.tmp")
    with tmp.open("w") as f:
        for r in rows:
            f.write(json.dumps(asdict(r)) + "\n")
    os.replace(tmp, runs_path)
    return rows


def tail_share(res: RunResult) -> float | None:
    """The share of a run's calls that came after the task's verifier first passed.

    `None` means unmeasurable, not zero. Acceptance is detected from tool-result
    text, so a run that reached a clean workspace without ever printing a
    recognised count has no acceptance point to divide from; its `runaway_share`
    is the dataclass default `0.0` and carries no measurement. Rendering that as
    `0%` reported the most overworking arm in the matrix (#6598) as the one that
    stopped cleanest, and sorted genuinely clean stops among runs nobody
    measured. Callers must keep the two apart: a measured `0.0` is a real
    perfect stop, and this returns it unchanged.
    """
    if res.acceptance_call_index is None:
        return None
    return res.runaway_share


def run_verdict(res: RunResult) -> str:
    """What one run counts as: "passed" | "slow-but-correct" | "failed" | "unverified".

    The one place a pass is decided, so the columns cannot disagree (#6605).

    A timed-out run is not a pass. `final_compiles_clean` alone is the wrong test:
    `verify_final_state` runs after the process ends, so a run killed at the time
    cap with an already-clean workspace records `True`. The pass column counted
    those while the legend said timeouts count as a fail, and the share list
    excluded them, so one row held both readings at once. Six of the 93 runs in
    the overfull-hbox matrix were in that state, on six arms sitting at the n=3
    boundary. A slow-but-correct run is its own category rather than either
    column: it separates "too slow" from "wrong". It is not a pass, because the
    benchmark the numbers are compared against scores a timeout as a failure.

    `None` was never verified (a task with no oracle), which is not a fail.
    """
    if res.final_compiles_clean is None:
        return "unverified"
    if not res.final_compiles_clean:
        return "failed"
    if res.timed_out:
        return "slow-but-correct"
    return "passed"


def is_pass(res: RunResult) -> bool:
    """Whether a run counts toward the pass column. See run_verdict()."""
    return run_verdict(res) == "passed"


def summarise(rows: list[RunResult]) -> list[dict]:
    by_arm: dict[str, list[RunResult]] = {}
    for r in rows:
        by_arm.setdefault(r.arm, []).append(r)
    out = []
    for arm, rs in by_arm.items():
        graded = [r for r in rs if not r.timed_out]
        verdicts = [run_verdict(r) for r in rs]
        passes = sum(1 for r in rs if is_pass(r))
        # A measured share only. An unmeasurable tail is counted in
        # `unmeasured_tails`, never folded in as 0.0 (#6598).
        shares = sorted(s for s in (tail_share(r) for r in graded) if s is not None)
        calls = sorted(r.total_calls for r in graded)
        # Measured runs only, the same filter `shares` uses. A run with no
        # detected acceptance keeps the default 0 and would drag the median
        # toward zero, which is #6598 again in the adjacent column.
        tails = sorted(r.tail_work_calls for r in graded if tail_share(r) is not None)
        out.append({
            "arm": arm,
            "source": rs[0].source,
            "kind": rs[0].kind,
            "delivery": rs[0].delivery,
            "runs": len(rs),
            "pass": passes,
            "timeouts": sum(1 for r in rs if r.timed_out),
            "slow_but_correct": verdicts.count("slow-but-correct"),
            "unverified": verdicts.count("unverified"),
            "shares": shares,
            "median_total_calls": calls[len(calls) // 2] if calls else None,
            "median_tail_work": tails[len(tails) // 2] if tails else None,
            "never_accepted": sum(1 for r in rs if r.acceptance_call_index is None),
            "unmeasured_tails": sum(1 for r in graded if tail_share(r) is None),
        })
    return out


UNMEASURED_SHARE = "-"


def render_share(share: float | None) -> str:
    """One share cell. `None` is unknown, and must not look like a measured zero."""
    return UNMEASURED_SHARE if share is None else f"{share:.0%}"


def arm_order_key(summary: dict) -> tuple[int, int, float]:
    """Sort key for the arm table: most passes first, then smallest median tail.

    An arm with no measured share sorts after every measured one in its pass
    group. Unknown is missing data, and treating it as the best possible value
    is what made unmeasured runs look like perfect stops (#6598).
    """
    shares = summary["shares"]
    if not shares:
        return (summary["pass"] * -1, 1, 0.0)
    return (summary["pass"] * -1, 0, shares[len(shares) // 2])


def baseline_summary(rows: list[RunResult]) -> dict:
    """The baseline's own numbers, over every rep.

    A dict keyed by arm would keep one entry per arm name, and every baseline
    rep shares the name "baseline", so that shape described a single run and
    printed a denominator of 1 (#6601).

    `runs` and `pass` cover every rep, so the pass denominator is the full
    count. The shares and the unmeasured count use the same population
    `summarise` uses for the baseline row -- graded runs only -- because the
    header and the arm table sit in one report and must not disagree about
    which shares the baseline has.
    """
    base = [r for r in rows if r.arm == "baseline"]
    graded = [r for r in base if not r.timed_out]
    return {
        "runs": len(base),
        "pass": sum(1 for r in base if is_pass(r)),
        "shares": sorted(s for s in (tail_share(r) for r in graded) if s is not None),
        "unmeasured": sum(1 for r in graded if tail_share(r) is None),
    }


def write_report(root: Path) -> None:
    rows = []
    for f in sorted(root.glob("*/runs.jsonl")) + [root / "runs.jsonl"]:
        if f.exists():
            rows.extend(load_runs(f))
    if not rows:
        fail(f"no runs.jsonl under {root}")

    base = baseline_summary(rows)
    rendered = " ".join(render_share(s) for s in base["shares"])
    if base["unmeasured"]:
        rendered = f"{rendered} {base['unmeasured']} unmeasured".strip()
    print(f"baseline tail share: {rendered or 'n/a'}")
    print(f"baseline pass: {base['pass']}/{base['runs']}\n")

    print(f"{'arm':<28}{'src':<8}{'kind':<22}{'deliv':<18}"
          f"{'pass':>6}{'slow':>5}{'t/o':>5}{'unmeas':>7}{'share':>16}"
          f"{'calls':>7}{'tail':>6}")
    print("-" * 128)
    for s in sorted(summarise(rows), key=arm_order_key):
        shares = " ".join(render_share(x) for x in s["shares"])
        shares = shares or UNMEASURED_SHARE
        # A measured zero is data and prints as 0; only an absent value is "-".
        unmeas = s["unmeasured_tails"]
        slow = s["slow_but_correct"]
        calls = "-" if s["median_total_calls"] is None else s["median_total_calls"]
        tail = "-" if s["median_tail_work"] is None else s["median_tail_work"]
        print(f"{s['arm']:<28}{s['source']:<8}{s['kind']:<22}{s['delivery']:<18}"
              f"{s['pass']:>4}/{s['runs']:<1}{slow:>5}{s['timeouts']:>5}"
              f"{unmeas:>7}{shares:>16}{calls:>7}{tail:>6}")
    print("\nkind      = prohibition | redirection | subtraction | "
          "rationalization-table | mixed | control")
    print("deliv     = append | doctrine:<anchor> | task | subtract | unexecutable")
    print("pass      = the task's own check passed and the run finished inside the cap")
    print("slow      = the check passed but the run was killed at the cap, so it counts "
          "as a fail")
    print("tail      = calls after acceptance excluding communicate/notes bookkeeping")
    print(f"unmeas    = graded runs whose tail is unmeasurable: acceptance was never "
          f"detected, so the share is unknown and prints as {UNMEASURED_SHARE}")

    # Why runs failed, so a low pass count can be read without the workdirs.
    reasons: dict[str, int] = {}
    for r in rows:
        if run_verdict(r) == "failed" and r.verify_reason:
            reasons[r.verify_reason] = reasons.get(r.verify_reason, 0) + 1
    if reasons:
        print("\nfailure reasons:")
        for reason, count in sorted(reasons.items(), key=lambda kv: -kv[1]):
            print(f"  {count:>3}x  {reason}")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--task", default="overfull-hbox")
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--out", type=Path, required=False)
    ap.add_argument("--timeout", type=int, default=900)
    ap.add_argument("--effort", default="high")
    ap.add_argument("--model", default="lunaroute/deepseek-4.1-flash")
    ap.add_argument("--arms", default="")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--report", type=Path)
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--capture-doctrine", action="store_true")
    ap.add_argument("--reanalyse", type=Path)
    args = ap.parse_args()

    if args.list:
        for arm, spec in ARMS.items():
            print(f"{arm:<28}{spec['source']:<9}{spec['kind']:<22}{spec['delivery']}")
            if spec["delivery"] == "unexecutable":
                print(f"    unexecutable: {spec['why_unexecutable']}")
        return

    if args.report:
        write_report(args.report)
        return
    if args.reanalyse:
        reanalyse_root(args.reanalyse)
        write_report(args.reanalyse)
        return

    if not args.out:
        ap.error("--out is required unless using --report or --list")
    out: Path = args.out
    (out / "logs").mkdir(parents=True, exist_ok=True)

    if args.capture_doctrine:
        # One throwaway run whose only purpose is to record the shipped doctrine.
        tmp = out / "capture"
        tmp.mkdir(parents=True, exist_ok=True)
        env = dict(os.environ)
        cfg = out / VANILLA_CONFIG
        cfg.mkdir(parents=True, exist_ok=True)
        env["XDG_CONFIG_HOME"] = str(cfg)
        state = tmp / "state"
        subprocess.run(
            ["evener", "--model", args.model, "--disable-memory",
             "--no-default-marketplaces", "--enabled-plugins", "",
             "--state-dir", str(state), "--max-rounds", "1", "hi"],
            capture_output=True, text=True, env=env, timeout=300,
        )
        tf = sorted((state / "sessions").glob("*.transcript.jsonl"))[0]
        sp = json.loads(tf.open().readline())["system_prompt"]
        i = sp.find("<environment>")
        if i < 0:
            fail("no <environment> block in the captured prompt")
        (out / DOCTRINE_CACHE).write_text(sp[:i])
        print(f"captured doctrine: {i} chars -> {out / DOCTRINE_CACHE}")
        return

    arms = [a.strip() for a in args.arms.split(",") if a.strip()] or list(ARMS)
    unknown = [a for a in arms if a not in ARMS]
    if unknown:
        ap.error(f"unknown arms: {unknown}")

    runs_path = out / "runs.jsonl"
    done: set[tuple[str, int]] = set()
    if runs_path.exists():
        for line in runs_path.open():
            r = json.loads(line)
            done.add((r["arm"], r["rep"]))
        print(f"resuming: {len(done)} runs already recorded")

    for arm in arms:
        if ARMS[arm]["delivery"] == "unexecutable":
            print(f"SKIP {arm}: {ARMS[arm]['why_unexecutable']}")
            continue
        for rep in range(args.reps):
            if (arm, rep) in done:
                continue
            print(f"=== {arm} rep{rep}", flush=True)
            res = run_once(arm, args.task, rep, out, args.timeout,
                           args.effort, args.model, args.keep)
            print(f"    calls={res.total_calls} acc={res.acceptance_call_index} "
                  f"tail={res.calls_after_acceptance} work={res.tail_work_calls} "
                  f"share={render_share(tail_share(res))} "
                  f"verdict={run_verdict(res)} "
                  f"to={res.timed_out} wall={res.wall_s}s "
                  f"vanilla={res.prompt_is_vanilla}", flush=True)
            with runs_path.open("a") as f:
                f.write(json.dumps(asdict(res)) + "\n")

    print()
    write_report(out)


if __name__ == "__main__":
    main()
