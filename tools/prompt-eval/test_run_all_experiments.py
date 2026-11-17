"""Checks on the measurement code in run_all_experiments.py.

The patterns and verdicts here decide every number in the experiment table, so
they get their own tests. Each case below is a string a real run actually
produced, taken from transcripts under /tmp/arms-hbox and /tmp/smoke2, plus the
false positives those transcripts exposed.
"""

from __future__ import annotations

import importlib.util
import re
import sys
from pathlib import Path

import pytest

MODULE_PATH = Path(__file__).with_name("run_all_experiments.py")


@pytest.fixture(scope="module")
def rae():
    spec = importlib.util.spec_from_file_location("rae_under_test", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    sys.modules["rae_under_test"] = module
    spec.loader.exec_module(module)
    return module


def _norm(text: str) -> str:
    return re.sub(r"\s+", " ", text)


# Reports that zero overfull boxes remain, in the shapes agents actually wrote.
ACCEPTANCE_REPORTS = [
    "=== overfull hbox count === 0",
    "--- overfull/underfull --- 0 ---",
    "--- overfull count: 0 ---",
    "any Overfull in stdout --- 0 ---",
    "overfull hbox count: 0",
    "0 overfull lines",
    "0 lines matching overfull",
    "exit=0 --- overfull/underfull --- 0 --- pages --- Output written on main.pdf",
    "=== pass 1 === exit=0 === overfull hbox count === 0 "
    "=== any Overfull/Underfull lines === (none)",
]

# Text that mentions overfull boxes without reporting zero of them.
NOT_ACCEPTANCE = [
    "49:Overfull \\hbox (0.10312pt too wide) in paragraph at lines 5--6",
    "54:Overfull \\hbox (18.03828pt too wide) in paragraph at lines 5--6",
    "Overfull count: 3",
    "=== any Overfull/Underfull lines ===",
    "overfull hbox count === 10",
    # A workdir path whose task name ends in 0. This was a live false positive:
    # it reported acceptance eight calls before the run had actually checked.
    "natures -> personalities overfulls [] --- /tmp/smoke2/work/baseline-overfull-hbox-0",
    # Echoed source: a job_status result quoting the script the agent wrote.
    "overfull(v)==0",
    " if n==0",
]


def test_acceptance_matches_real_reports(rae):
    pattern = rae.acceptance_pattern("overfull-hbox")
    misses = [t for t in ACCEPTANCE_REPORTS if not pattern.search(_norm(t))]
    assert misses == []


def test_acceptance_rejects_near_misses(rae):
    pattern = rae.acceptance_pattern("overfull-hbox")
    false_positives = [t for t in NOT_ACCEPTANCE if pattern.search(_norm(t))]
    assert false_positives == []


def test_acceptance_needs_normalised_whitespace(rae):
    # The raw output has the count on its own line after a rule; only the
    # normalised form is matchable, which is why analyse() normalises first.
    raw = "=== overfull hbox count ===\n0\n"
    pattern = rae.acceptance_pattern("overfull-hbox")
    assert pattern.search(_norm(raw))


@pytest.fixture
def fixture_dir(rae):
    d = rae.TASKS["overfull-hbox"]["fixture"]
    if not d.is_dir():
        pytest.skip(f"terminal-bench fixture not installed at {d}")
    return d


def test_substitutions_legal_accepts_a_family_swap(rae, fixture_dir):
    import shutil

    work = Path("/tmp") / f"rae-legal-{id(object())}"
    shutil.rmtree(work, ignore_errors=True)
    work.mkdir(parents=True)
    try:
        for name in ("main.tex", "input.tex", "synonyms.txt"):
            (work / name).write_bytes((fixture_dir / name).read_bytes())
        original = (work / "input.tex").read_text()
        families = [
            [w.strip() for w in line.split(",") if w.strip()]
            for line in (work / "synonyms.txt").read_text().splitlines()
            if line.strip()
        ]
        swapped = original
        for family in families:
            hit = next((w for w in family if w in original), None)
            if hit:
                swapped = original.replace(hit, next(w for w in family if w != hit), 1)
                break
        assert swapped != original, "fixture has no substitutable word"
        (work / "input.tex").write_text(swapped)
        assert rae.substitutions_legal(work / "input.tex") is True
    finally:
        import shutil as sh

        sh.rmtree(work, ignore_errors=True)


def test_substitutions_legal_rejects_an_illegal_change(rae, fixture_dir):
    import shutil

    work = Path("/tmp") / f"rae-illegal-{id(object())}"
    shutil.rmtree(work, ignore_errors=True)
    work.mkdir(parents=True)
    try:
        for name in ("main.tex", "input.tex", "synonyms.txt"):
            (work / name).write_bytes((fixture_dir / name).read_bytes())
        text = (work / "input.tex").read_text()
        (work / "input.tex").write_text(text.replace("The", "Zzyzx", 1))
        assert rae.substitutions_legal(work / "input.tex") is False
    finally:
        import shutil as sh

        sh.rmtree(work, ignore_errors=True)


def test_verdict_rejects_the_untouched_fixture(rae, fixture_dir, tmp_path):
    # The task starts with overfull boxes, so an agent that changed nothing must
    # fail. This is the case an always-true oracle scored as a pass.
    work = tmp_path / "work"
    work.mkdir()
    for name in ("main.tex", "input.tex", "synonyms.txt"):
        (work / name).write_bytes((fixture_dir / name).read_bytes())
    clean, reason = rae.verify_final_state("overfull-hbox", work)
    assert clean is False
    assert "overfull" in reason


def test_verdict_rejects_a_missing_workspace(rae, tmp_path):
    clean, reason = rae.verify_final_state("overfull-hbox", tmp_path / "gone")
    assert clean is False
    assert reason


def test_verdict_rejects_a_modified_forbidden_file(rae, fixture_dir, tmp_path):
    """A tampered synonyms.txt must fail even though pdflatex still compiles.

    synonyms.txt is not read by the compiler, so the compile and overfull
    conditions can pass while the file is illegal. That isolates the byte
    comparison: this is the gate that catches an agent editing its way out.
    """
    work = tmp_path / "work"
    work.mkdir()
    for name in ("main.tex", "input.tex", "synonyms.txt"):
        (work / name).write_bytes((fixture_dir / name).read_bytes())
    # Replace the document input with something that compiles without overfull
    # boxes, leaving main.tex and synonyms.txt as the only divergence checks.
    (work / "input.tex").write_text("A short harmless paragraph.\n")
    (work / "synonyms.txt").write_text("tampered family\n")

    clean, reason = rae.verify_final_state("overfull-hbox", work)
    assert clean is False
    assert "synonyms.txt" in reason


def test_clean_compile_runs_the_remaining_checks(rae, fixture_dir, tmp_path):
    """With the compile clean, the verdict moves on to the substitution rule.

    Guards the ordering: a clean build must not short-circuit to a pass, and the
    reason must name the check that actually rejected the workspace.
    """
    work = tmp_path / "work"
    work.mkdir()
    for name in ("main.tex", "input.tex", "synonyms.txt"):
        (work / name).write_bytes((fixture_dir / name).read_bytes())
    (work / "input.tex").write_text("A short harmless paragraph.\n")

    clean, reason = rae.verify_final_state("overfull-hbox", work)
    assert clean is False
    assert "substitution" in reason


def test_every_arm_spec_has_the_keys_the_runner_reads(rae):
    """An arm missing a key crashes the run, not the arm.

    c06 declared its text as `table_rows` and no `task` key, so the runner died
    with KeyError('task') the moment it reached c06 and the remaining arms never
    ran. A missing key is only visible at that arm's turn, after hours of work,
    so every arm is checked up front.
    """
    required = ("source", "kind", "delivery", "system", "task")
    missing = {
        arm: [k for k in required if k not in spec]
        for arm, spec in rae.ARMS.items()
    }
    assert {a: m for a, m in missing.items() if m} == {}


def test_every_doctrine_arm_contributes_text(rae):
    """A doctrine arm whose text never reaches the prompt is a baseline in disguise.

    `system` is empty for the table-splice arm by design, so the check is that one
    of the two carriers is non-empty for every delivery that modifies the prompt.
    `subtract` is exempt because its edits are hard-coded in apply_arm, which fails
    loudly when a named clause is not found rather than rendering an untouched
    prompt.
    """
    for arm, spec in rae.ARMS.items():
        delivery = spec["delivery"]
        if delivery in ("none", "task") or delivery.startswith("schema:"):
            continue
        if delivery in ("hook", "subtract"):
            continue
        text = spec.get("system", "").strip() or spec.get("table_rows", "").strip()
        assert text, f"{arm}: delivery {delivery!r} carries no text"


def _write_rows(root, runs) -> None:
    import dataclasses
    import json

    root.mkdir(parents=True, exist_ok=True)
    with (root / "runs.jsonl").open("w") as f:
        for r in runs:
            f.write(json.dumps(dataclasses.asdict(r)) + "\n")


def test_reanalyse_keeps_a_pass_verdict_it_did_not_recompute(rae, tmp_path):
    """Reanalysing is how a detector fix reaches runs already paid for (#6597).

    `verify_final_state` re-runs the task's own check against the workdir, and a
    default run deletes that workdir once the verdict is recorded. So a
    reanalyse cannot recompute the verdict it is rewriting, and clearing it
    turned three measured passes into 0/3 with nothing left on disk to restore.
    """
    rows = [
        rae.RunResult(arm="baseline", task="overfull-hbox", rep=i,
                      final_compiles_clean=True, acceptance_call_index=3,
                      runaway_share=0.5, total_calls=8)
        for i in range(3)
    ]
    import json

    for r in rows:
        state = tmp_path / f"state/baseline-overfull-hbox-{r.rep}/sessions"
        state.mkdir(parents=True)
        (state / "s.meta.json").write_text(
            json.dumps({"session_id": "s", "parent_session_id": None})
        )
    _write_rows(tmp_path, rows)

    out = rae.reanalyse_root(tmp_path)
    assert [r.final_compiles_clean for r in out] == [True, True, True]
    assert [r.verdict_source for r in out] == ["reused"] * 3


def test_reanalyse_recomputes_the_verdict_when_the_workdir_survived(rae, tmp_path):
    """A kept workdir is the one case where the verdict can be re-derived."""
    rows = [rae.RunResult(arm="baseline", task="overfull-hbox", rep=0,
                          final_compiles_clean=False, verify_reason="stale")]
    _write_rows(tmp_path, rows)
    (tmp_path / "work/baseline-overfull-hbox-0").mkdir(parents=True)

    out = rae.reanalyse_root(tmp_path)
    assert out[0].verdict_source == "recomputed"
    assert out[0].verify_reason != "stale"


def test_unmeasurable_tail_is_unknown_not_zero(rae):
    """A run with no detected acceptance point has no measured tail (#6598).

    `runaway_share` defaults to 0.0, so reading the field alone cannot tell
    "stopped instantly" from "never observed finishing". Five passing runs in
    the overfull-hbox matrix were in the second state and sorted above runs with
    a real measured tail.
    """
    unmeasured = rae.RunResult(arm="a04-tool-warrant", task="overfull-hbox", rep=0,
                               final_compiles_clean=False, acceptance_call_index=None,
                               runaway_share=0.0, total_calls=41)
    measured_zero = rae.RunResult(arm="baseline", task="overfull-hbox", rep=0,
                                  final_compiles_clean=True, acceptance_call_index=22,
                                  runaway_share=0.0, total_calls=27)

    assert rae.tail_share(unmeasured) is None
    assert rae.tail_share(measured_zero) == 0.0
    assert rae.tail_share(unmeasured) != rae.tail_share(measured_zero)


def test_summary_keeps_unmeasured_runs_out_of_the_measured_shares(rae):
    """An unmeasurable tail must not enter the share distribution as 0.0."""
    measured = rae.RunResult(arm="baseline", task="overfull-hbox", rep=0,
                             final_compiles_clean=True, acceptance_call_index=22,
                             runaway_share=0.85, total_calls=40)
    unmeasured = rae.RunResult(arm="baseline", task="overfull-hbox", rep=1,
                               final_compiles_clean=True, acceptance_call_index=None,
                               runaway_share=0.0, total_calls=34)

    (summary,) = rae.summarise([measured, unmeasured])
    assert summary["shares"] == [0.85]
    assert summary["unmeasured_tails"] == 1
    assert summary["never_accepted"] == 1


def test_an_unmeasured_arm_sorts_after_a_measured_arm_that_passed_as_often(
        rae):
    """Unknown is missing data, so it cannot rank as the cleanest stop.

    Ordering by the raw default put every unmeasured run at 0% and therefore at
    the top of the table, which is how `a04-tool-warrant` -- the most
    overworking arm in the matrix -- read as one that stopped instantly.
    """
    measured = rae.RunResult(arm="measured", task="overfull-hbox", rep=0,
                             final_compiles_clean=True, acceptance_call_index=10,
                             runaway_share=0.4, total_calls=20)
    unmeasured = rae.RunResult(arm="unmeasured", task="overfull-hbox", rep=0,
                               final_compiles_clean=True, acceptance_call_index=None,
                               runaway_share=0.0, total_calls=20)

    summaries = rae.summarise([unmeasured, measured])
    order = [s["arm"] for s in sorted(summaries, key=rae.arm_order_key)]
    assert order == ["measured", "unmeasured"]


def test_tail_median_comes_from_the_measured_runs_only(rae):
    """An unmeasured run must not enter the tail median as a zero (#6598).

    This is the same defect as the share column, one column over. Graded tails
    of [unmeasured 0, 5, 20] median to 5, while the measured runs alone median
    to 20. The unmeasured run's zero is the dataclass default, not a
    measurement, so it must be left out rather than counted as a clean stop.
    """
    def run(rep, acc, tail):
        return rae.RunResult(arm="arm", task="overfull-hbox", rep=rep,
                             final_compiles_clean=True,
                             acceptance_call_index=acc, tail_work_calls=tail)

    unmeasured = run(0, None, 0)
    five = run(1, 10, 5)
    twenty = run(2, 10, 20)

    (summary,) = rae.summarise([unmeasured, five, twenty])
    assert summary["median_tail_work"] == 20
    assert summary["median_tail_work"] != 5


def test_reanalyse_clears_a_tail_it_can_no_longer_measure(rae, tmp_path):
    """A reanalysed row must not keep tail values the current detector cannot back.

    `analyse()` used to write the tail fields only when it found an acceptance
    point, so a row whose acceptance the fixed detector no longer matched kept
    the previous detector's numbers and reported a measured tail nothing
    measured.
    """
    import json

    row = rae.RunResult(arm="baseline", task="overfull-hbox", rep=0,
                        final_compiles_clean=True, acceptance_call_index=3,
                        calls_after_acceptance=9, tail_work_calls=7,
                        runaway_share=0.5, total_calls=12)
    _write_rows(tmp_path, [row])
    sessions = tmp_path / "state/baseline-overfull-hbox-0/sessions"
    sessions.mkdir(parents=True)
    (sessions / "s.meta.json").write_text(
        json.dumps({"session_id": "s", "parent_session_id": None})
    )
    # A transcript with one tool call that reports no acceptance at all.
    (sessions / "s.transcript.jsonl").write_text(
        json.dumps({"kind": "entry", "turn": {"message": {"content": [
            {"kind": "tool_call", "tool_call": {"id": "c1", "name": "list_directory"}},
        ]}}}) + "\n"
    )

    (out,) = rae.reanalyse_root(tmp_path)
    assert out.acceptance_call_index is None
    assert out.tail_work_calls == 0
    assert out.calls_after_acceptance == 0
    assert out.runaway_share == 0.0


def test_baseline_summary_counts_every_rep(rae):
    """The baseline header must describe all reps, not just the last (#6601).

    A dict keyed by arm keeps one entry per arm name, and every baseline rep
    shares that name, so the header described a single run and always printed
    a denominator of 1.
    """
    rows = [
        rae.RunResult(arm="baseline", task="overfull-hbox", rep=0,
                      final_compiles_clean=True, acceptance_call_index=4,
                      runaway_share=0.15, total_calls=20),
        rae.RunResult(arm="baseline", task="overfull-hbox", rep=1,
                      final_compiles_clean=True, acceptance_call_index=None,
                      runaway_share=0.0, total_calls=34),
        rae.RunResult(arm="baseline", task="overfull-hbox", rep=2,
                      final_compiles_clean=True, acceptance_call_index=5,
                      runaway_share=0.85, total_calls=40),
    ]

    base = rae.baseline_summary(rows)
    assert base["runs"] == 3
    assert base["pass"] == 3
    assert base["shares"] == [0.15, 0.85]
    assert base["unmeasured"] == 1


def test_measured_zero_is_data_and_absent_value_is_not(rae):
    """A measured zero must stay distinguishable from an absent value.

    The printed table reads every cell through one of these paths, and a
    truthiness check would print a measured 0 as if nothing were measured --
    the confusion this change set exists to remove, in the other direction.
    """
    measured_zero = rae.RunResult(arm="arm", task="overfull-hbox", rep=0,
                                 final_compiles_clean=True,
                                 acceptance_call_index=10, runaway_share=0.0,
                                 tail_work_calls=0, total_calls=20)
    unmeasured = rae.RunResult(arm="arm", task="overfull-hbox", rep=1,
                               final_compiles_clean=False,
                               acceptance_call_index=None, runaway_share=0.0,
                               tail_work_calls=0, total_calls=41)

    assert rae.tail_share(measured_zero) == 0.0
    assert rae.tail_share(unmeasured) is None
    assert rae.render_share(0.0) != rae.render_share(None)


def test_baseline_header_and_table_agree_on_the_shares(rae):
    """One report must not show two share sets for the baseline.

    The header and the arm table print the baseline's shares side by side. The
    arm table covers graded runs only, so if the header also counted a timed-out
    rep it would list a share the table leaves out. `pass` stays over every rep
    so the denominator remains the full count.
    """
    rows = [
        rae.RunResult(arm="baseline", task="overfull-hbox", rep=0,
                      final_compiles_clean=True, acceptance_call_index=4,
                      runaway_share=0.15, timed_out=False),
        # Timed out, but acceptance was detected before the kill.
        rae.RunResult(arm="baseline", task="overfull-hbox", rep=1,
                      final_compiles_clean=True, acceptance_call_index=6,
                      runaway_share=0.90, timed_out=True),
        rae.RunResult(arm="baseline", task="overfull-hbox", rep=2,
                      final_compiles_clean=True, acceptance_call_index=5,
                      runaway_share=0.85, timed_out=False),
    ]

    base = rae.baseline_summary(rows)
    (row,) = rae.summarise(rows)
    assert base["shares"] == row["shares"]
    assert base["unmeasured"] == row["unmeasured_tails"]
    assert base["runs"] == 3
    assert base["pass"] == 3
