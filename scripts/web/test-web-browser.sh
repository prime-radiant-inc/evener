#!/usr/bin/env bash
# test-web-browser.sh — make test-web-browser's entry point. The gate itself is
# `evener-dev dev web-browser-guards` (cmd/evener-dev/webbrowser.go): the real
# browser-only frontend guards, their scheduling, verdicts and interrupt
# handling. This script only supplies how many guards run at once: each is a
# real browser (most with a Vite dev server) whose tripwires assume it gets
# CPU, so by default the slots are the machine's spare cores
# (scripts/lib/load-aware-workers.sh), all at once on an idle CI runner and one
# at a time on a saturated one. BROWSER_GUARD_CONCURRENCY overrides that.
#
# It execs the prebuilt ./evener-dev (the make target's build-dev
# prerequisite) rather than `go run`: go run neither relays SIGTERM or SIGHUP
# nor dies with its child, so an interrupt would kill it and orphan the gate
# with its browsers still running.
set -u

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$script_dir/../lib/load-aware-workers.sh"
cd "$script_dir/../.." || exit 1
BROWSER_GUARD_CONCURRENCY=${BROWSER_GUARD_CONCURRENCY:-$(load_aware_workers 0)} exec ./evener-dev dev web-browser-guards
