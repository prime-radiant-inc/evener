#!/usr/bin/env bash
# run-capped.sh — run a command under a hard MEMORY ceiling so a leaky test or
# fuzz run gets OOM-killed *individually* (a cgroup/scope OOM) instead of firing
# the kernel's global OOM killer and taking the whole host — and its network —
# down with it. See docs/fuzzing.md ("Memory safety") for the why.
#
# Mechanism: a transient systemd USER scope with a cgroup-v2 MemoryMax (+ swap
# disabled so the run can't thrash the box into a multi-minute stall before the
# kill). This needs no root. `ulimit -v` is deliberately NOT used: the Go runtime
# reserves tens of GB of *virtual* address space without committing it, so an
# address-space cap kills every Go process instantly. RSS (MemoryMax) is the only
# limit that matches what actually pressures the host.
#
# Usage:
#   scripts/run-capped.sh <command> [args...]
#
# Two ceilings, both enforced by cgroup-v2:
#   - PER-RUN: each wrapped command gets its own scope MemoryMax, so one runaway
#     run is OOM-killed alone.
#   - TOTAL: every capped run also joins a shared slice (serf-capped.slice) with
#     a MemoryMax on the slice itself, so many CONCURRENT runs can't collectively
#     exhaust the host even though each is within its own per-run cap. (The Jun 29
#     incident was concurrency stacking — several uncapped runs at once — not a
#     single fat process, so the per-run cap alone would not have prevented it.)
#
# Tuning (env):
#   SERF_MEM_MAX     per-run ceiling (any systemd size, e.g. 16G, 8G). Default
#                    16G — several times a healthy run, well under the danger
#                    zone. SERF_MEM_MAX=0 disables capping entirely (run direct).
#   SERF_MEM_TOTAL   shared ceiling for ALL concurrent serf capped runs. Default
#                    32G — leaves the host ~28G + headroom for network/SSH.
#                    SERF_MEM_TOTAL=0 skips the slice (per-run cap only).
#
# Degrades gracefully: if systemd-run is missing or the user manager has no
# delegated memory cgroup (some CI containers), it prints a warning and runs the
# command UNCAPPED rather than failing — CI runners impose their own cgroup limit.
set -uo pipefail

cap="${SERF_MEM_MAX:-16G}"
total="${SERF_MEM_TOTAL:-32G}"
slice="serf-capped.slice"

if [ "$#" -eq 0 ]; then
	echo "run-capped: no command given" >&2
	exit 2
fi

# Re-entrancy guard: if we are already inside a serf cap scope (e.g. `make
# fuzz-nightly` wrapped run-fuzz.sh, which in turn wraps each target), do NOT
# create a second scope. A nested `systemd-run --user --scope` moves the process
# into a fresh SIBLING scope, which would silently ESCAPE the outer ceiling. The
# outermost cap already bounds this whole process tree, so just run.
if [ "${SERF_CAPPED:-}" = "1" ]; then
	exec "$@"
fi

# Explicitly disabled.
if [ "$cap" = "0" ]; then
	exec "$@"
fi

# systemd-run absent (macOS, minimal containers): can't cap, don't block.
if ! command -v systemd-run >/dev/null 2>&1; then
	echo "run-capped: systemd-run not found; running UNCAPPED (host not protected)" >&2
	exec "$@"
fi

# Probe that a user scope with a memory limit actually takes — cgroup-v2 memory
# must be delegated to the user manager. If the probe fails, fall back to
# uncapped rather than erroring out the whole run.
if ! systemd-run --user --scope -q -p MemoryMax="$cap" -p MemorySwapMax=0 -- true >/dev/null 2>&1; then
	echo "run-capped: user-scope memory cap unavailable; running UNCAPPED (host not protected)" >&2
	exec "$@"
fi

# Bound the SUM of all concurrent serf runs via a shared slice. Best-effort and
# idempotent; --runtime so the limit evaporates on reboot rather than persisting.
# If it can't be set (older systemd, no delegation), fall back to per-run only.
slice_arg=""
if [ "$total" != "0" ] && systemctl --user set-property --runtime "$slice" \
	MemoryMax="$total" MemorySwapMax=0 >/dev/null 2>&1; then
	slice_arg="--slice=$slice"
fi

if [ -n "$slice_arg" ]; then
	echo "run-capped: MemoryMax=$cap per run, $total shared (swap off) — a runaway OOMs its scope, not the host" >&2
else
	echo "run-capped: MemoryMax=$cap (swap off) — a runaway run OOMs this scope, not the host" >&2
fi
# SERF_CAPPED=1 marks the subtree as already bounded so nested run-capped calls
# (run-fuzz.sh self-caps each target) skip re-wrapping instead of escaping.
exec systemd-run --user --scope -q $slice_arg -p MemoryMax="$cap" -p MemorySwapMax=0 \
	--setenv=SERF_CAPPED=1 -- "$@"
