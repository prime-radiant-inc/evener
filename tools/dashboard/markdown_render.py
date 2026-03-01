"""Render dashboard views as markdown.

Markdown is the default response format. Every endpoint returns markdown
unless the client sends Accept: application/json.
"""


def render_run_list(runs):
    """Render the dashboard overview as a markdown table."""
    if not runs:
        return "# Eval Dashboard\n\nNo runs found.\n"

    lines = ["# Eval Dashboard", ""]
    lines.append("| Run | Pass Rate | Tasks |")
    lines.append("|-----|-----------|-------|")
    for r in runs:
        total = r.get("total_tasks", 0)
        passed = r.get("passed", 0)
        pct = f"{passed/total*100:.0f}%" if total > 0 else "-"
        rate = f"{passed}/{total} ({pct})"
        lines.append(f"| {r['job_name']} | {rate} | {total} |")
    lines.append("")
    return "\n".join(lines)


def render_run_detail(run, tasks):
    """Render a single run's detail page as markdown."""
    lines = [f"# {run['job_name']}", ""]

    total = run.get("total_tasks", 0)
    passed = run.get("passed", 0)
    pct = f"{passed/total*100:.0f}%" if total > 0 else "-"
    lines.append(f"**Pass rate:** {passed}/{total} ({pct})")

    # Failure breakdown
    fail_cats = {}
    for t in tasks:
        cat = t.get("failure_category")
        if cat:
            fail_cats[cat] = fail_cats.get(cat, 0) + 1
    if fail_cats:
        lines.append("")
        lines.append("**Failures:**")
        for cat, count in sorted(fail_cats.items()):
            lines.append(f"- {cat}: {count}")

    # Task table
    lines.extend(["", "## Tasks", ""])
    lines.append("| Task | Result | Category | Sessions |")
    lines.append("|------|--------|----------|----------|")
    for t in sorted(tasks, key=lambda t: (t["passed"], t["task_name"])):
        result = "PASS" if t["passed"] else "FAIL"
        cat = t.get("failure_category") or ""
        lines.append(
            f"| {t['task_name']} | {result} | {cat} | {t.get('session_count', 0)} |"
        )
    lines.append("")
    return "\n".join(lines)


def render_task_detail(task_name, job_name, reward, failure_category,
                       trajectory, verifier_output="", reviewer_verdict=""):
    """Render a task detail page with trajectory as markdown."""
    result = "PASSED" if reward and reward > 0 else "FAILED"
    lines = [f"# {task_name} — {job_name}", f"**Result:** {result}"]
    if failure_category:
        lines.append(f"**Failure category:** {failure_category}")

    # Trajectory
    lines.extend(["", "## Trajectory", ""])
    if not trajectory:
        lines.append("No trajectory data.")
    else:
        for r in trajectory:
            pad = str(r["round"]).rjust(3)
            action = r["action"].ljust(7)
            lines.append(f"    {pad}  {action}  {r['summary']}")

    # Verifier output
    if verifier_output:
        lines.extend(["", "## Verifier Output", "",
                       "```", verifier_output.strip()[-2000:], "```"])

    if reviewer_verdict:
        lines.extend(["", "## Reviewer Verdict", "", reviewer_verdict])

    lines.append("")
    return "\n".join(lines)
