"""Data layer -- read experiment run metadata, scoreboard, and task history."""

import json
from pathlib import Path


class ExperimentStore:
    """Read-only access to experiment data (runs, scoreboard, task history).

    Expected layout under base_dir:
        runs/*.json          — per-run result files (wave-* or experiment)
        tasks/*.json         — per-task history files
        scoreboard.json      — aggregate scoreboard
    """

    def __init__(self, base_dir):
        self.base_dir = Path(base_dir)
        self._runs = {}       # run_id -> run dict
        self._scoreboard = {} # full scoreboard dict
        self._tasks = {}      # task_name -> task dict
        self.reload()

    def reload(self):
        """Re-read all JSON files from disk."""
        self._runs = {}
        self._scoreboard = {}
        self._tasks = {}

        # Load runs
        runs_dir = self.base_dir / "runs"
        if runs_dir.is_dir():
            for f in runs_dir.iterdir():
                if f.is_file() and f.suffix == ".json":
                    try:
                        data = json.loads(f.read_text())
                        run_id = data.get("run_id")
                        if run_id:
                            self._runs[run_id] = data
                    except (json.JSONDecodeError, OSError):
                        pass

        # Load scoreboard
        scoreboard_file = self.base_dir / "scoreboard.json"
        if scoreboard_file.is_file():
            try:
                self._scoreboard = json.loads(scoreboard_file.read_text())
            except (json.JSONDecodeError, OSError):
                pass

        # Load tasks
        tasks_dir = self.base_dir / "tasks"
        if tasks_dir.is_dir():
            for f in tasks_dir.iterdir():
                if f.is_file() and f.suffix == ".json":
                    try:
                        data = json.loads(f.read_text())
                        task_name = data.get("task")
                        if task_name:
                            self._tasks[task_name] = data
                    except (json.JSONDecodeError, OSError):
                        pass

    def list_experiments(self, run_type=None):
        """Return all runs with computed fields, sorted by date descending.

        Each run dict includes computed fields:
            mean_score   — average score across all tasks in this run
            task_count   — number of tasks in this run
            perfect_count — number of tasks with score == 1.0

        run_type: None (all), "wave" (run_id starts with "wave-"),
                  or "experiment" (non-wave runs).
        """
        runs = list(self._runs.values())

        if run_type == "wave":
            runs = [r for r in runs if r["run_id"].startswith("wave-")]
        elif run_type == "experiment":
            runs = [r for r in runs if not r["run_id"].startswith("wave-")]

        result = []
        for run in runs:
            enriched = dict(run)
            results = run.get("results", {})
            scores = [t["score"] for t in results.values()]
            enriched["task_count"] = len(scores)
            enriched["mean_score"] = sum(scores) / len(scores) if scores else 0.0
            enriched["perfect_count"] = sum(1 for s in scores if s == 1.0)
            result.append(enriched)

        result.sort(key=lambda r: r.get("date", ""), reverse=True)
        return result

    def get_experiment(self, run_id):
        """Return a single run dict, or None if not found."""
        return self._runs.get(run_id)

    def get_scoreboard(self, filter=None):
        """Return scoreboard dict, optionally filtered by task status.

        filter: None (all tasks), "failing" (score < 1.0),
                or "solved" (score == 1.0).
        """
        if not self._scoreboard:
            return {}

        if filter is None:
            return self._scoreboard

        sb = dict(self._scoreboard)
        tasks = sb.get("tasks", {})

        if filter == "failing":
            filtered = {k: v for k, v in tasks.items() if v["score"] < 1.0}
        elif filter == "solved":
            filtered = {k: v for k, v in tasks.items() if v["score"] == 1.0}
        else:
            filtered = tasks

        sb["tasks"] = filtered
        return sb

    def get_task_history(self, task_name):
        """Return history list for a task, sorted by date descending."""
        task = self._tasks.get(task_name)
        if task is None:
            return []

        history = list(task.get("history", []))
        history.sort(key=lambda h: h.get("date", ""), reverse=True)
        return history
