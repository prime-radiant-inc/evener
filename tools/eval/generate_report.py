#!/usr/bin/env python3
"""Generate standalone HTML report from a collected archive run."""

import html
import json
import os
import sys

AGENT_STDOUT_MAX_LINES = 200


def _read_file(path, max_lines=None):
    """Read a file, returning its content or None if missing."""
    if not os.path.isfile(path):
        return None
    with open(path) as f:
        if max_lines is None:
            return f.read()
        lines = []
        for i, line in enumerate(f):
            if i >= max_lines:
                lines.append(f"\n... truncated after {max_lines} lines ...\n")
                break
            lines.append(line)
        return "".join(lines)


def _css():
    """Embedded CSS for the report."""
    return """
        * { box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            max-width: 1200px; margin: 0 auto; padding: 20px;
            background: #fafafa; color: #333;
        }
        h1 { margin-bottom: 4px; }
        .subtitle { color: #666; margin-bottom: 24px; }
        .stats { display: flex; gap: 24px; margin-bottom: 24px; flex-wrap: wrap; }
        .stat-card {
            background: #fff; border: 1px solid #ddd; border-radius: 8px;
            padding: 16px 24px; min-width: 160px;
        }
        .stat-card .label { font-size: 13px; color: #888; text-transform: uppercase; }
        .stat-card .value { font-size: 28px; font-weight: bold; margin-top: 4px; }
        .stat-card .detail { font-size: 12px; color: #999; margin-top: 2px; }
        .pass { color: #1a7f37; }
        .fail { color: #cf222e; }
        table { width: 100%; border-collapse: collapse; background: #fff;
                border: 1px solid #ddd; border-radius: 8px; margin-bottom: 24px; }
        th, td { padding: 10px 14px; text-align: left; border-bottom: 1px solid #eee; }
        th { background: #f6f8fa; cursor: pointer; user-select: none;
             font-size: 13px; text-transform: uppercase; color: #666; }
        th:hover { background: #eef1f5; }
        th .sort-arrow { font-size: 10px; margin-left: 4px; }
        tr:last-child td { border-bottom: none; }
        tr:hover td { background: #f9f9fb; }
        .badge {
            display: inline-block; padding: 2px 8px; border-radius: 4px;
            font-size: 12px; font-weight: 600;
        }
        .badge-pass { background: #dafbe1; color: #1a7f37; }
        .badge-fail { background: #ffebe9; color: #cf222e; }
        .rep-dot {
            display: inline-block; width: 14px; height: 14px; border-radius: 50%;
            margin-right: 3px; vertical-align: middle;
        }
        .rep-pass { background: #2da44e; }
        .rep-fail { background: #cf222e; }
        .failure-bar { margin-bottom: 24px; }
        .failure-bar h2 { font-size: 16px; margin-bottom: 8px; }
        .bar-container { display: flex; border-radius: 6px; overflow: hidden;
                         height: 28px; background: #eee; }
        .bar-segment { display: flex; align-items: center; justify-content: center;
                       color: #fff; font-size: 12px; font-weight: 600;
                       min-width: 40px; }
        .bar-legend { display: flex; gap: 16px; margin-top: 8px; flex-wrap: wrap; }
        .bar-legend-item { display: flex; align-items: center; gap: 4px; font-size: 13px; }
        .bar-legend-swatch { width: 12px; height: 12px; border-radius: 3px; }
        details { margin-bottom: 12px; background: #fff; border: 1px solid #ddd;
                  border-radius: 8px; }
        details summary {
            padding: 12px 16px; cursor: pointer; font-weight: 600;
            list-style: none;
        }
        details summary::-webkit-details-marker { display: none; }
        details summary::before { content: "\\25B6  "; font-size: 10px; }
        details[open] summary::before { content: "\\25BC  "; }
        .detail-content { padding: 0 16px 16px; }
        .detail-content h4 { margin: 12px 0 4px; font-size: 13px; color: #666;
                             text-transform: uppercase; }
        pre {
            background: #f6f8fa; border: 1px solid #eee; border-radius: 6px;
            padding: 12px; overflow-x: auto; font-size: 13px; line-height: 1.5;
            max-height: 400px; overflow-y: auto; white-space: pre-wrap;
            word-wrap: break-word;
        }
        .section-title { font-size: 18px; margin: 24px 0 12px; }
    """


def _sort_js():
    """Minimal JS for sortable table columns."""
    return """
    function sortTable(colIdx) {
        var table = document.getElementById("results-table");
        var tbody = table.querySelector("tbody");
        var rows = Array.from(tbody.querySelectorAll("tr"));
        var th = table.querySelectorAll("th")[colIdx];
        var asc = th.getAttribute("data-sort-dir") !== "asc";
        th.setAttribute("data-sort-dir", asc ? "asc" : "desc");
        // Reset other headers
        table.querySelectorAll("th").forEach(function(h, i) {
            if (i !== colIdx) h.removeAttribute("data-sort-dir");
            var arrow = h.querySelector(".sort-arrow");
            if (arrow) arrow.textContent = i === colIdx ? (asc ? "\\u25B2" : "\\u25BC") : "";
        });
        rows.sort(function(a, b) {
            var aVal = a.cells[colIdx].getAttribute("data-sort-value") || a.cells[colIdx].textContent;
            var bVal = b.cells[colIdx].getAttribute("data-sort-value") || b.cells[colIdx].textContent;
            var aNum = parseFloat(aVal), bNum = parseFloat(bVal);
            if (!isNaN(aNum) && !isNaN(bNum)) {
                return asc ? aNum - bNum : bNum - aNum;
            }
            return asc ? aVal.localeCompare(bVal) : bVal.localeCompare(aVal);
        });
        rows.forEach(function(row) { tbody.appendChild(row); });
    }
    """


_BAR_COLORS = [
    "#cf222e", "#da3b20", "#bf5700", "#9a6700",
    "#6e7781", "#57606a", "#8250df", "#0969da",
]


def generate_report(run_dir):
    """Generate HTML report string from an archive run directory."""
    summary_path = os.path.join(run_dir, "summary.json")
    with open(summary_path) as f:
        summary = json.load(f)

    run_id = summary.get("run_id", "unknown")
    task_count = summary.get("task_count", 0)
    majority = summary.get("pass_count_majority", 0)
    strict = summary.get("pass_count_strict", 0)
    any_pass = summary.get("pass_count_any", 0)
    rate_maj = summary.get("pass_rate_majority", 0)
    ci = summary.get("pass_rate_majority_ci_95", [0, 1])
    fc_counts = summary.get("failure_categories", {})
    tasks = summary.get("tasks", [])

    parts = []

    # -- Doctype & head
    parts.append("<!DOCTYPE html>\n<html lang=\"en\">\n<head>")
    parts.append("<meta charset=\"utf-8\">")
    parts.append(f"<title>Eval Report: {html.escape(run_id)}</title>")
    parts.append(f"<style>{_css()}</style>")
    parts.append("</head>\n<body>")

    # -- Header
    parts.append(f"<h1>Eval Report</h1>")
    parts.append(f"<div class=\"subtitle\">{html.escape(run_id)}</div>")

    # -- Stat cards
    parts.append("<div class=\"stats\">")
    parts.append(_stat_card("Majority Pass", f"{majority}/{task_count}",
                            f"{rate_maj * 100:.1f}%"))
    parts.append(_stat_card("95% CI",
                            f"{ci[0] * 100:.1f}% &ndash; {ci[1] * 100:.1f}%", "Wilson"))
    parts.append(_stat_card("Strict Pass", f"{strict}/{task_count}",
                            f"{summary.get('pass_rate_strict', 0) * 100:.1f}%"))
    parts.append(_stat_card("Any Pass", f"{any_pass}/{task_count}",
                            f"{summary.get('pass_rate_any', 0) * 100:.1f}%"))
    parts.append("</div>")

    # -- Failure breakdown bar
    if fc_counts:
        parts.append(_failure_bar(fc_counts))

    # -- Summary table
    parts.append("<h2 class=\"section-title\">Results</h2>")
    parts.append(_results_table(tasks))

    # -- Per-task details
    parts.append("<h2 class=\"section-title\">Task Details</h2>")
    for task in tasks:
        parts.append(_task_details(run_dir, task))

    # -- Script & close
    parts.append(f"<script>{_sort_js()}</script>")
    parts.append("</body>\n</html>")

    return "\n".join(parts)


def _stat_card(label, value, detail=""):
    """Render a stat card."""
    return (
        f'<div class="stat-card">'
        f'<div class="label">{html.escape(label)}</div>'
        f'<div class="value">{value}</div>'
        f'<div class="detail">{detail}</div>'
        f'</div>'
    )


def _failure_bar(fc_counts):
    """Render a horizontal stacked bar of failure categories."""
    total = sum(fc_counts.values())
    if total == 0:
        return ""

    parts = ['<div class="failure-bar">']
    parts.append("<h2>Failure Breakdown</h2>")
    parts.append('<div class="bar-container">')

    sorted_cats = sorted(fc_counts.items(), key=lambda x: -x[1])
    for i, (cat, count) in enumerate(sorted_cats):
        pct = count / total * 100
        color = _BAR_COLORS[i % len(_BAR_COLORS)]
        parts.append(
            f'<div class="bar-segment" style="width:{pct:.1f}%;background:{color}">'
            f'{count}</div>'
        )

    parts.append("</div>")  # bar-container

    # Legend
    parts.append('<div class="bar-legend">')
    for i, (cat, count) in enumerate(sorted_cats):
        color = _BAR_COLORS[i % len(_BAR_COLORS)]
        parts.append(
            f'<div class="bar-legend-item">'
            f'<div class="bar-legend-swatch" style="background:{color}"></div>'
            f'{html.escape(cat)} ({count})'
            f'</div>'
        )
    parts.append("</div>")  # bar-legend
    parts.append("</div>")  # failure-bar
    return "\n".join(parts)


def _results_table(tasks):
    """Render the sortable results table."""
    parts = ['<table id="results-table">']
    parts.append("<thead><tr>")
    headers = ["Task", "Result", "Pass Rate", "Reps", "Failure Category"]
    for i, h in enumerate(headers):
        parts.append(
            f'<th onclick="sortTable({i})">{h}'
            f'<span class="sort-arrow"></span></th>'
        )
    parts.append("</tr></thead>")
    parts.append("<tbody>")

    for task in tasks:
        name = task["name"]
        is_pass = task.get("pass_majority", False)
        pass_rate = task.get("pass_rate", 0)
        reps_pass = task.get("reps_pass", 0)
        reps_total = task.get("reps_total", 0)
        reps = task.get("reps", [])

        badge_cls = "badge-pass" if is_pass else "badge-fail"
        badge_text = "PASS" if is_pass else "FAIL"

        # Collect failure categories for this task
        task_fcs = set()
        for r in reps:
            fc = r.get("failure_category")
            if fc:
                task_fcs.add(fc)
        fc_text = ", ".join(sorted(task_fcs)) if task_fcs else "-"

        # Rep dots
        dots = []
        for r in reps:
            cls = "rep-pass" if r.get("reward", 0) > 0 else "rep-fail"
            dots.append(f'<span class="rep-dot {cls}" '
                        f'title="Rep {r.get("rep", "?")}: {r.get("reward", 0)}"></span>')

        parts.append("<tr>")
        parts.append(f'<td>{html.escape(name)}</td>')
        parts.append(f'<td data-sort-value="{1 if is_pass else 0}">'
                     f'<span class="badge {badge_cls}">{badge_text}</span></td>')
        parts.append(f'<td data-sort-value="{pass_rate}">'
                     f'{reps_pass}/{reps_total}</td>')
        parts.append(f'<td>{"".join(dots)}</td>')
        parts.append(f'<td>{html.escape(fc_text)}</td>')
        parts.append("</tr>")

    parts.append("</tbody></table>")
    return "\n".join(parts)


def _task_details(run_dir, task):
    """Render collapsible detail section for one task."""
    name = task["name"]
    is_pass = task.get("pass_majority", False)
    reps = task.get("reps", [])
    result_class = "pass" if is_pass else "fail"
    result_text = "PASS" if is_pass else "FAIL"

    parts = [f'<details>']
    parts.append(
        f'<summary><span class="{result_class}">{result_text}</span> '
        f'{html.escape(name)}</summary>'
    )
    parts.append('<div class="detail-content">')

    for rep_info in reps:
        rep_num = rep_info.get("rep", "?")
        reward = rep_info.get("reward", 0)
        fc = rep_info.get("failure_category")

        rep_dir = os.path.join(run_dir, "tasks", name, f"rep-{rep_num}")
        rep_class = "pass" if reward > 0 else "fail"

        parts.append(f"<h4>Rep {rep_num} "
                     f"<span class=\"{rep_class}\">({reward})</span>"
                     f"{' - ' + html.escape(fc) if fc else ''}</h4>")

        # Verifier stdout
        verifier = _read_file(os.path.join(rep_dir, "verifier-stdout.txt"))
        if verifier:
            parts.append("<h4>Verifier Output</h4>")
            parts.append(f"<pre>{html.escape(verifier)}</pre>")

        # Agent stdout (truncated)
        agent = _read_file(
            os.path.join(rep_dir, "agent-stdout.txt"),
            max_lines=AGENT_STDOUT_MAX_LINES,
        )
        if agent:
            parts.append("<h4>Agent Output</h4>")
            parts.append(f"<pre>{html.escape(agent)}</pre>")

    parts.append("</div></details>")
    return "\n".join(parts)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <run-dir>", file=sys.stderr)
        sys.exit(1)
    run_dir = sys.argv[1]
    report_html = generate_report(run_dir)
    output_path = os.path.join(run_dir, "report.html")
    with open(output_path, "w") as f:
        f.write(report_html)
    print(f"Report written to {output_path}")
