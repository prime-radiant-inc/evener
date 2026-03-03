"""Named task sets for eval harness.

Used by run_eval.py to resolve --task arguments like `--task discriminators`
into concrete task name lists.

Methodology for the "discriminators" set:
  Analysis of terminal-bench 2.0 public leaderboard data (27 agent/model
  submissions, 10,947 trials) identified tasks with 10-75% failure rate
  across all submissions. Tasks outside this range are either too easy
  (every agent passes) or too hard (no agent passes) to provide signal.

  Source: https://gist.github.com/simonw/3bff274abcbbbf8766e9437a542db248
  Date of analysis: 2026-03-03
"""

# fmt: off
TASK_SETS: dict[str, list[str]] = {
    # 56 tasks with 10-75% failure rate across 27 agent/model submissions.
    # Sorted by failure rate descending (hardest discriminators first).
    "discriminators": [
        "make-mips-interpreter",        # 75.4%
        "gcode-to-text",                # 74.6%
        "regex-chess",                  # 70.1%
        "polyglot-c-py",                # 65.0%
        "polyglot-rust-c",              # 63.4%
        "query-optimize",               # 61.3%
        "path-tracing",                 # 59.3%
        "adaptive-rejection-sampler",   # 59.0%
        "qemu-alpine-ssh",              # 57.4%
        "path-tracing-reverse",         # 54.5%
        "protein-assembly",             # 52.9%
        "chess-best-move",              # 52.8%
        "write-compressor",             # 49.6%
        "configure-git-webserver",      # 47.1%
        "tune-mjcf",                    # 46.3%
        "winning-avg-corewars",         # 45.9%
        "cancel-async-tasks",           # 44.7%
        "financial-document-processor", # 43.9%
        "overfull-hbox",                # 43.7%
        "sanitize-git-repo",            # 43.4%
        "extract-elf",                  # 43.0%
        "schemelike-metacircular-eval",  # 39.5%
        "compile-compcert",             # 37.0%
        "feal-linear-cryptanalysis",    # 36.4%
        "circuit-fibsqrt",              # 35.9%
        "break-filter-js-from-html",    # 33.7%
        "sparql-university",            # 30.9%
        "largest-eigenval",             # 30.1%
        "build-pmars",                  # 29.3%
        "mailman",                      # 29.2%
        "large-scale-text-editing",     # 27.7%
        "bn-fit-modify",                # 27.6%
        "qemu-startup",                 # 27.6%
        "rstan-to-pystan",              # 26.9%
        "build-cython-ext",             # 23.6%
        "password-recovery",            # 23.6%
        "pytorch-model-cli",            # 23.6%
        "feal-differential-cryptanalysis",  # 23.3%
        "count-dataset-tokens",         # 23.1%
        "sqlite-db-truncate",           # 22.9%
        "llm-inference-batching-scheduler", # 21.9%
        "reshard-c4-data",              # 20.8%
        "mcmc-sampling-stan",           # 20.7%
        "fix-ocaml-gc",                 # 20.0%
        "openssl-selfsigned-cert",      # 19.5%
        "sqlite-with-gcov",             # 18.7%
        "pytorch-model-recovery",       # 18.5%
        "build-pov-ray",                # 17.4%
        "crack-7z-hash",                # 17.1%
        "kv-store-grpc",                # 15.4%
        "hf-model-inference",           # 14.9%
        "headless-terminal",            # 14.8%
        "merge-diff-arc-agi-task",      # 12.3%
        "pypi-server",                  # 11.4%
        "regex-log",                    # 11.4%
        "fix-code-vulnerability",       # 9.0%
    ],
}
# fmt: on


def resolve_tasks(task_args: list[str]) -> list[str]:
    """Expand named task sets in a list of task arguments.

    Each element is either a concrete task name or a key in TASK_SETS.
    Returns a deduplicated list preserving first-seen order.
    """
    seen: set[str] = set()
    result: list[str] = []
    for t in task_args:
        names = TASK_SETS.get(t, [t])
        for name in names:
            if name not in seen:
                seen.add(name)
                result.append(name)
    return result


def list_task_sets() -> str:
    """Format available task sets for display."""
    lines = []
    for name, tasks in TASK_SETS.items():
        lines.append(f"  {name}: {len(tasks)} tasks")
    return "\n".join(lines)
