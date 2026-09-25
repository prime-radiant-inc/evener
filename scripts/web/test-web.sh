#!/usr/bin/env bash
# test-web.sh — make test-web's entry point. The gate itself is
# `evener-dev dev web-checks` (cmd/evener-dev/webchecks.go): the frontend's
# typecheck, unit tests and lint, run side by side, each in private roots, with
# verdicts, log replay and interrupt handling.
#
# It execs the prebuilt ./evener-dev (the make target's build-dev
# prerequisite) rather than `go run`: go run neither relays SIGTERM or SIGHUP
# nor dies with its child, so an interrupt would kill it and orphan the gate
# with its checks still running.
set -u

cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1
exec ./evener-dev dev web-checks
