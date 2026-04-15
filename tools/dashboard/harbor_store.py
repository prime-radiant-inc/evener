"""Harbor state reader — synthesize experiment records from completed waves.

Dashboards usually rely on docs/experiments/runs/*.json files produced by
post_run.sh after each wave. HarborStore reads the same information
directly from harbor-runner's state/{runs,results}/ trees so the
dashboard can show runs before anyone invokes post_run.sh.

Returns dicts compatible with ExperimentStore:
    {run_id, date, git_sha, model, variant, results: {task: {score, reps,
     reps_pass, reps_total}}}
"""

import json
import re
import time
from pathlib import Path


# Harbor run IDs come in several formats we've seen in the wild:
#   wave-<sha>-YYYYMMDD-HHMM        (new wave launcher)
#   exp-<name>-YYYYMMDD-HHMM        (named experiments)
#   openai-<tag>_nogit_YYYYMMDD_HHMM  (older runs, underscore separators)
#   v<N>-<label>                    (old labeled variants, no date)
#   exp-<name>-<sha>                (experiment + build sha, no date)
# Accept any of these — if we can't find a date in the name, fall back
# to the directory's mtime so runs still sort chronologically.
_RUN_DATE_TIME_RE = re.compile(
    r"(?P<date>\d{8})[-_](?P<time>\d{4})$"
)
_WAVE_PREFIX_RE = re.compile(r"^wave-(?P<sha>[0-9a-f]{5,})")

# Matches pytest summary line:
#   "===== 3 passed in 0.52s ====="
#   "===== 2 failed, 3 passed in 0.52s ====="
_PYTEST_SUMMARY_RE = re.compile(
    r"(?:^|\s)(?P<count>\d+)\s+(?P<word>passed|failed)"
)


def _extract_sort_key(run: dict) -> str:
    """Extract sortable timestamp key from run.

    Returns YYYYMMDD-HHMMSS from run_id suffix, falling back to
    normalized date field if no timestamp in run_id.
    """
    run_id = run.get("run_id", "")
    m = _RUN_DATE_TIME_RE.search(run_id)
    if m:
        time = m.group('time').ljust(6, '0')  # Pad HHMM to HHMM00
        return f"{m.group('date')}-{time}"
    # Fall back to date field, normalized to YYYYMMDD-000000
    date = run.get("date", "")
    if date:
        return date.replace("-", "") + "-000000"
    return "00000000-000000"


def _extract_harness(agent_import: str) -> str:
    """Extract harness name from AGENT_IMPORT env var.

    Examples:
        "serf_agent:SerfAgent" → "serf"
        "" (empty) → "terminus"
        "foo_agent:FooAgent" → "foo"
    """
    if not agent_import:
        return "terminus"
    # Take the module part before ':'
    module = agent_import.split(":")[0]
    # Strip _agent suffix if present
    if module.endswith("_agent"):
        module = module[:-6]
    return module or "terminus"


class HarborStore:
    """Read completed wave results from harbor-runner state directory.

    Optionally augments with S3 data for reps that were synced to S3
    but not back to local harbor state.
    """

    def __init__(
        self, state_dir: str, s3_client=None, cache_ttl: int = 300,
        sync_cache_dir: str = None,
    ):
        """state_dir may point at state/runs/ (matching LiveStore's
        convention) or at state/ itself. We resolve both so the results/
        sibling is found either way.

        s3_client: optional S3Client used to backfill reps that aren't
        synced to local harbor state. Listings are cached per wave for
        cache_ttl seconds.

        sync_cache_dir: optional persistent directory for synced S3 data.
        When set, full run data is synced here and used for trajectory views.
        """
        p = Path(state_dir)
        if p.name == "runs" and (p.parent / "results").is_dir():
            self.runs_dir = p
            self.results_dir = p.parent / "results"
        else:
            self.runs_dir = p / "runs"
            self.results_dir = p / "results"
        self.base_dir = self.runs_dir.parent
        self.s3_client = s3_client
        self._cache_ttl = cache_ttl
        self._s3_cache = {}  # wave_id → (timestamp, per_task_from_s3)
        self.sync_cache_dir = Path(sync_cache_dir) if sync_cache_dir else None

    def list_runs(self) -> list[dict]:
        """Synthesize run records for all runs (local and S3-only).

        Discovers runs from .env files in runs/, which exist for both
        local and AWS-dispatched runs. For runs with local results,
        includes task scores. For S3-only runs, returns minimal
        metadata with empty results (scores load on detail view).

        Skips S3 backfill — that's N aws calls per wave, prohibitive
        for a list view. Detail requests via get_run() still fetch S3.
        """
        runs = []
        if not self.runs_dir.is_dir():
            return runs

        # Discover all runs from .env files
        env_files = sorted(
            self.runs_dir.glob("*.env"),
            key=lambda f: f.stem, reverse=True,
        )
        for env_file in env_files:
            run_id = env_file.stem
            wave_dir = self.results_dir / run_id
            run = None

            if wave_dir.is_dir():
                # Local results exist — try to build record with scores
                run = self._build_run(wave_dir, use_s3=False)

            # If local results are empty/missing, check sync cache
            if run is None or not run.get("results"):
                synced_dir = self._get_synced_run_dir(run_id)
                if synced_dir is not None:
                    run = self._build_run_from_synced(run_id, synced_dir)

            # Fall back to minimal record from .env
            if run is None or not run.get("results"):
                run = self._build_run_from_env(run_id, env_file)

            if run is not None:
                runs.append(run)

        # Sort by full timestamp from run_id (more reliable than date field)
        runs.sort(key=_extract_sort_key, reverse=True)
        return runs

    def get_run(self, run_id: str) -> dict | None:
        """Return the synthetic run dict for a specific wave, or None.

        Checks in order: local results, sync cache, S3 fetch.
        """
        # Check local harbor results first
        wave_dir = self.results_dir / run_id
        if wave_dir.is_dir():
            return self._build_run(wave_dir, use_s3=True)

        # Check sync cache (persistent S3 mirror)
        synced_dir = self._get_synced_run_dir(run_id)
        if synced_dir is not None:
            return self._build_run_from_synced(run_id, synced_dir)

        # No local results — check if .env exists (S3-only run)
        env_file = self.runs_dir / f"{run_id}.env"
        if env_file.is_file():
            return self._build_run_from_env(run_id, env_file, use_s3=True)
        return None

    def _get_synced_run_dir(self, run_id: str) -> Path | None:
        """Return the synced run directory if it exists and has data."""
        if self.sync_cache_dir is None:
            return None
        synced = self.sync_cache_dir / run_id
        if synced.is_dir() and any(synced.iterdir()):
            return synced
        return None

    def sync_run(self, run_id: str) -> bool:
        """Sync full run data from S3 to the sync cache.

        Returns True on success, False on error or if sync not configured.
        """
        if self.s3_client is None or self.sync_cache_dir is None:
            return False
        return self.s3_client.sync_run(run_id, str(self.sync_cache_dir))

    def _build_run_from_synced(self, run_id: str, synced_dir: Path) -> dict:
        """Build run record from synced S3 data.

        Synced layout: {sync_cache_dir}/{run_id}/rep-N/{run_id}_repN/{task}__hash/
        """
        # Get metadata from .env file
        env_file = self.runs_dir / f"{run_id}.env"
        env = self._parse_env(env_file) if env_file.is_file() else {}

        # Extract date from run_id
        date_match = _RUN_DATE_TIME_RE.search(run_id)
        if date_match:
            d = date_match.group("date")
            date = f"{d[:4]}-{d[4:6]}-{d[6:8]}"
        else:
            date = ""

        wave_m = _WAVE_PREFIX_RE.match(run_id)
        git_sha = wave_m.group("sha") if wave_m else ""

        # Walk synced reps and aggregate scores
        per_task = {}  # task_name → {rep_num: reward}
        for rep_dir in sorted(synced_dir.iterdir()):
            if not rep_dir.is_dir() or not rep_dir.name.startswith("rep-"):
                continue
            try:
                rep_num = int(rep_dir.name[4:])
            except ValueError:
                continue

            # Find the inner directory ({run_id}_rep{N})
            inner = next(
                (d for d in rep_dir.iterdir() if d.is_dir()),
                None
            )
            if inner is None:
                continue

            for task_dir in inner.iterdir():
                if not task_dir.is_dir() or "__" not in task_dir.name:
                    continue
                task_name = task_dir.name.rsplit("__", 1)[0]
                reward = self._compute_reward(task_dir)
                if reward is not None:
                    per_task.setdefault(task_name, {})[rep_num] = reward

        # Build results dict
        results = {}
        for task, reps_map in per_task.items():
            rep_nums = sorted(reps_map.keys())
            rep_rewards = [reps_map[n] for n in rep_nums]
            score = sum(rep_rewards) / len(rep_rewards) if rep_rewards else 0.0
            reps_pass = sum(1 for r in rep_rewards if r >= 1.0)
            results[task] = {
                "score": round(score, 3),
                "reps": rep_rewards,
                "reps_pass": reps_pass,
                "reps_total": len(rep_rewards),
            }

        return {
            "run_id": run_id,
            "date": date,
            "git_sha": git_sha,
            "model": env.get("MODEL", "unknown"),
            "harness": _extract_harness(env.get("AGENT_IMPORT", "")),
            "variant": "",
            "results": results,
        }

    def get_run_scores(self, run_id: str) -> dict | None:
        """Fetch just the score summary for a run (for batch loading).

        Returns {task_count, mean_score, perfect_count} or None if not found.
        Fetches from S3 if needed.
        """
        run = self.get_run(run_id)
        if run is None:
            return None
        results = run.get("results", {})
        scores = [t["score"] for t in results.values()]
        return {
            "run_id": run_id,
            "task_count": len(scores),
            "mean_score": sum(scores) / len(scores) if scores else 0.0,
            "perfect_count": sum(1 for s in scores if s == 1.0),
        }

    def get_run_scores_fast(self, run_id: str) -> dict | None:
        """Fast score summary using only reward.txt files (no pytest parsing).

        For aggregate stats in list views. Much faster than full fetch since
        reward.txt files are small and we skip test-stdout.txt entirely.
        Falls back to local results if available.

        Returns {run_id, task_count, mean_score, perfect_count, tasks: {name: score}}.
        """
        # Check local first
        wave_dir = self.results_dir / run_id
        if wave_dir.is_dir():
            run = self._build_run(wave_dir, use_s3=False)
            if run:
                results = run.get("results", {})
                task_scores = {name: t["score"] for name, t in results.items()}
                scores = list(task_scores.values())
                return {
                    "run_id": run_id,
                    "task_count": len(scores),
                    "mean_score": sum(scores) / len(scores) if scores else 0.0,
                    "perfect_count": sum(1 for s in scores if s == 1.0),
                    "tasks": task_scores,
                }

        # S3-only: fetch only reward.txt files
        if self.s3_client is None:
            return None

        s3_rewards = self._fetch_s3_rewards_fast(run_id)
        if not s3_rewards:
            return None

        # Compute per-task scores (average across reps)
        task_scores = {}
        for task, reps_map in s3_rewards.items():
            rep_rewards = list(reps_map.values())
            if rep_rewards:
                task_scores[task] = sum(rep_rewards) / len(rep_rewards)

        scores = list(task_scores.values())
        return {
            "run_id": run_id,
            "task_count": len(scores),
            "mean_score": sum(scores) / len(scores) if scores else 0.0,
            "perfect_count": sum(1 for s in scores if s == 1.0),
            "tasks": task_scores,
        }

    def _fetch_s3_rewards_fast(self, wave_id: str) -> dict:
        """Fetch only reward.txt files from S3 using sync (much faster).

        Uses `aws s3 sync --include '*reward.txt'` to fetch all reward files
        in a single operation, then reads them locally.

        Returns {task_name: {rep_num: reward}}.
        """
        import subprocess
        import tempfile

        # Sync reward.txt files to temp directory
        with tempfile.TemporaryDirectory() as tmpdir:
            prefix = f"runs/{wave_id}/"
            r = subprocess.run(
                ["aws", "s3", "sync",
                 f"s3://{self.s3_client.bucket}/{prefix}",
                 tmpdir,
                 "--exclude", "*",
                 "--include", "*/verifier/reward.txt",
                 "--region", self.s3_client.region],
                capture_output=True, text=True,
            )
            if r.returncode != 0:
                return {}

            # Parse synced files
            # Structure: {tmpdir}/rep-N/{wave_id}_repN/{task}__hash/verifier/reward.txt
            s3_rewards: dict[str, dict[int, float]] = {}
            escaped = re.escape(wave_id)
            pat = re.compile(
                r"rep-(\d+)/[^/]+/([^/]+)__[^/]+/verifier/reward\.txt$"
            )

            for reward_file in Path(tmpdir).rglob("reward.txt"):
                rel_path = str(reward_file.relative_to(tmpdir))
                m = pat.search(rel_path)
                if not m:
                    continue
                rep = int(m.group(1))
                task = m.group(2)
                try:
                    reward = float(reward_file.read_text().strip())
                    s3_rewards.setdefault(task, {})[rep] = reward
                except (ValueError, OSError):
                    continue

            return s3_rewards

    def _build_run_from_env(
        self, run_id: str, env_file: Path, use_s3: bool = False
    ) -> dict:
        """Build a run record from .env file metadata.

        Used for S3-only runs that don't have local results.
        When use_s3=True and s3_client is available, fetches results from S3.
        """
        env = self._parse_env(env_file)

        # Extract date from run_id
        date_match = _RUN_DATE_TIME_RE.search(run_id)
        if date_match:
            d = date_match.group("date")
            date = f"{d[:4]}-{d[4:6]}-{d[6:8]}"
        else:
            # Fall back to .env file mtime
            try:
                ts = env_file.stat().st_mtime
                from datetime import datetime
                date = datetime.fromtimestamp(ts).strftime("%Y-%m-%d")
            except OSError:
                date = ""

        # Extract git SHA from wave- prefix
        wave_m = _WAVE_PREFIX_RE.match(run_id)
        git_sha = wave_m.group("sha") if wave_m else ""

        # Fetch results from S3 if requested and available
        results = {}
        if use_s3 and self.s3_client is not None:
            s3_rewards = self._fetch_s3_rewards(run_id)
            for task, reps_map in s3_rewards.items():
                rep_nums = sorted(reps_map.keys())
                rep_rewards = [reps_map[n] for n in rep_nums]
                score = sum(rep_rewards) / len(rep_rewards) if rep_rewards else 0.0
                reps_pass = sum(1 for r in rep_rewards if r >= 1.0)
                results[task] = {
                    "score": round(score, 3),
                    "reps": rep_rewards,
                    "reps_pass": reps_pass,
                    "reps_total": len(rep_rewards),
                }

        record = {
            "run_id": run_id,
            "date": date,
            "git_sha": git_sha,
            "model": env.get("MODEL", "unknown"),
            "harness": _extract_harness(env.get("AGENT_IMPORT", "")),
            "variant": "",
            "results": results,
        }
        # Flag S3-only runs that weren't fetched (list view skips S3)
        if not use_s3 and not results:
            record["needs_s3_fetch"] = True
        return record

    # ------------------------------------------------------------------

    def _build_run(self, wave_dir: Path, use_s3: bool = True) -> dict | None:
        run_id = wave_dir.name
        # Date: pulled from the run id if present, else from mtime.
        date_match = _RUN_DATE_TIME_RE.search(run_id)
        if date_match:
            d = date_match.group("date")
            date = f"{d[:4]}-{d[4:6]}-{d[6:8]}"
        else:
            try:
                ts = wave_dir.stat().st_mtime
                from datetime import datetime
                date = datetime.fromtimestamp(ts).strftime("%Y-%m-%d")
            except OSError:
                date = ""
        # Pull out the SHA from wave-<sha> prefixes for display.
        wave_m = _WAVE_PREFIX_RE.match(run_id)
        git_sha = wave_m.group("sha") if wave_m else ""

        # Env file metadata
        model = "unknown"
        harness = "serf"  # Default for local runs
        env_file = self.runs_dir / f"{run_id}.env"
        if env_file.is_file():
            env = self._parse_env(env_file)
            model = env.get("MODEL", "unknown")
            harness = _extract_harness(env.get("AGENT_IMPORT", ""))

        # Walk reps and aggregate scores per task
        per_task = {}  # task_name → {rep_num: reward}
        for rep_dir in sorted(wave_dir.iterdir()):
            if not rep_dir.is_dir() or not rep_dir.name.startswith("rep-"):
                continue
            try:
                rep_num = int(rep_dir.name[4:])
            except ValueError:
                continue
            inner_candidates = [
                rep_dir / f"{run_id}_rep{rep_num}",
            ]
            inner_candidates.extend(
                d for d in rep_dir.iterdir() if d.is_dir()
            )
            inner = next((c for c in inner_candidates if c.is_dir()), None)
            if inner is None:
                continue
            for task_dir in inner.iterdir():
                if not task_dir.is_dir() or "__" not in task_dir.name:
                    continue
                task_name = task_dir.name.rsplit("__", 1)[0]
                reward = self._compute_reward(task_dir)
                if reward is None:
                    continue
                per_task.setdefault(task_name, {})[rep_num] = reward

        # Backfill from S3 for reps that were synced remotely but not
        # pulled back into harbor state. Skipped on list views.
        if use_s3:
            self._augment_from_s3(run_id, per_task)

        # Build results dict matching ExperimentStore shape
        results = {}
        for task, reps_map in per_task.items():
            rep_nums = sorted(reps_map.keys())
            rep_rewards = [reps_map[n] for n in rep_nums]
            score = sum(rep_rewards) / len(rep_rewards)
            reps_pass = sum(1 for r in rep_rewards if r >= 1.0)
            results[task] = {
                "score": round(score, 3),
                "reps": rep_rewards,
                "reps_pass": reps_pass,
                "reps_total": len(rep_rewards),
            }

        return {
            "run_id": run_id,
            "date": date,
            "git_sha": git_sha,
            "model": model,
            "harness": harness,
            "variant": "",
            "results": results,
        }

    def _compute_reward(self, task_dir: Path) -> float | None:
        """Determine pass/fail reward for one task trial."""
        # Explicit reward.txt wins if present
        reward_file = task_dir / "verifier" / "reward.txt"
        if reward_file.is_file():
            try:
                return float(reward_file.read_text().strip())
            except (ValueError, OSError):
                pass

        # Check result.json (terminal-bench format)
        result_file = task_dir / "result.json"
        if result_file.is_file():
            try:
                data = json.loads(result_file.read_text())
                verifier_result = data.get("verifier_result")
                if verifier_result:
                    rewards = verifier_result.get("rewards")
                    if rewards:
                        reward = rewards.get("reward")
                        if reward is not None:
                            return float(reward)
            except (json.JSONDecodeError, ValueError, OSError, TypeError):
                pass

        # Fall back to pytest output parse
        stdout_file = task_dir / "verifier" / "test-stdout.txt"
        if not stdout_file.is_file():
            return None
        try:
            content = stdout_file.read_text(errors="replace")
        except OSError:
            return None
        return _parse_pytest_reward(content)

    def _augment_from_s3(self, wave_id: str, per_task: dict) -> None:
        """Merge S3-only rep rewards into per_task.

        S3 is treated as the superset: any (task, rep) tuple that
        exists on S3 but not locally is fetched. reward.txt is
        preferred, otherwise test-stdout.txt is parsed.
        """
        if self.s3_client is None:
            return

        cached = self._s3_cache.get(wave_id)
        now = time.time()
        if cached and now - cached[0] < self._cache_ttl:
            s3_rewards = cached[1]
        else:
            s3_rewards = self._fetch_s3_rewards(wave_id)
            self._s3_cache[wave_id] = (now, s3_rewards)

        for task, reps_map in s3_rewards.items():
            local = per_task.setdefault(task, {})
            for rep_num, reward in reps_map.items():
                if rep_num not in local:
                    local[rep_num] = reward

    def _fetch_s3_rewards(self, wave_id: str) -> dict:
        """List S3 for a wave and fetch verifier rewards per (task, rep).

        Returns {task_name: {rep_num: reward}}. One aws s3 ls call +
        one aws s3 cp per (task, rep) verifier file found.
        """
        prefix = f"runs/{wave_id}/"
        keys = self.s3_client.list_objects(prefix)
        if not keys:
            return {}

        # Match: runs/{wave_id}/rep-N/{wave_id}_repN/{task}__hash/verifier/<file>
        escaped = re.escape(wave_id)
        pat = re.compile(
            r"^runs/" + escaped + r"/rep-(\d+)/[^/]+/"
            r"([^/]+)__[^/]+/verifier/(reward\.txt|test-stdout\.txt)$"
        )
        # (rep, task) → best available verifier file
        targets = {}
        for key in keys:
            m = pat.match(key)
            if not m:
                continue
            rep = int(m.group(1))
            task = m.group(2)
            fname = m.group(3)
            existing = targets.get((rep, task))
            # Prefer reward.txt over test-stdout.txt
            if existing is None or (fname == "reward.txt"
                                    and not existing.endswith("reward.txt")):
                targets[(rep, task)] = key

        s3_rewards: dict[str, dict[int, float]] = {}
        for (rep, task), key in targets.items():
            text = self.s3_client.get_text(key)
            if text is None:
                continue
            if key.endswith("reward.txt"):
                try:
                    reward = float(text.strip())
                except ValueError:
                    continue
            else:
                reward = _parse_pytest_reward(text)
                if reward is None:
                    continue
            s3_rewards.setdefault(task, {})[rep] = reward
        return s3_rewards

    def _parse_env(self, path: Path) -> dict[str, str]:
        env = {}
        try:
            text = path.read_text()
        except OSError:
            return env
        for line in text.splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            # Skip INSTANCE=... allocation lines
            if line.startswith("INSTANCE="):
                continue
            k, _, v = line.partition("=")
            env[k.strip()] = v.strip()
        return env


def _parse_pytest_reward(content: str) -> float | None:
    """Extract binary pass/fail from pytest output.

    terminal-bench is all-or-nothing: any failed test → reward=0.
    Look for the final summary-style line with "N passed" or "N failed"
    counts.
    """
    # Walk lines backwards looking for the summary line.
    for line in reversed(content.splitlines()):
        matches = _PYTEST_SUMMARY_RE.findall(line)
        if not matches:
            continue
        if not any(word in line for word in ("passed", "failed")):
            continue
        # Require "in" or "s" at the end to be the actual summary line
        if " in " not in line:
            continue
        has_failed = any(w == "failed" for _, w in matches)
        return 0.0 if has_failed else 1.0
    return None
