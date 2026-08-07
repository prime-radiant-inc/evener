#!/usr/bin/env bash
# setup-gocache.sh — point the Go build cache at a big volume, durably.
#
# Why this exists (kata 98x9): the Go build cache is the single largest,
# fastest-growing consumer of disk in this repo's fleet workflow — one
# `go test -c` of the biggest package alone grows it ~1G from a warm cache.
# Left on the boot volume, it has twice driven that volume to 100% full mid
# fleet-run, at which point nothing could run at all (every tool call needs
# to write an output file before executing).
#
# `go env -w GOCACHE=<path>` fixes this, but it writes to a per-user global
# file (`go env GOENV`, normally ~/Library/Application Support/go/env on
# macOS) OUTSIDE this git checkout. A fresh checkout or a new machine does
# NOT inherit it — there is nothing in the repo to inherit it from. That is
# why this is a script to run once per machine, not a repo file to commit.
# If the target volume is later unmounted or stalls, nothing here catches it
# ahead of time: a `go` command will hang or fail against the unreachable
# path, same as any other unmounted-volume failure. Re-run this script
# against a reachable path to recover.
#
# Usage:
#   scripts/setup-gocache.sh /path/on/big/volume
#
# There is no default target: a big volume is a per-machine fact, and a
# baked-in path outlives the volume it names — this script once defaulted to
# an external volume that was later retired, and every no-argument run then
# chased the dead path. Name the target explicitly.
set -uo pipefail

target="${1:-}"

if [ -z "$target" ]; then
	echo "setup-gocache: no target path given. Usage: scripts/setup-gocache.sh /path/on/big/volume" >&2
	exit 2
fi

# The target's PARENT must already exist and be writable. If it does not,
# that is either a typo or — the failure mode this kata added — the volume
# it lives on is unmounted. Either way, refuse to silently create a deep
# path that masks the real problem (mkdir -p would happily create
# "/Volumes/SomeTypo/serf-build-cache" and nobody would notice until the
# next reboot mounts nothing there).
parent=$(dirname "$target")
if [ ! -d "$parent" ]; then
	cat >&2 <<-MSG
		setup-gocache: "$parent" does not exist.
		If this is meant to be an external volume, mount it first, then re-run.
		If the default target is wrong for this machine, pass the right path:
		  scripts/setup-gocache.sh /path/on/your/big/volume
	MSG
	exit 1
fi
if [ ! -w "$parent" ]; then
	echo "setup-gocache: \"$parent\" exists but is not writable by $(whoami)" >&2
	exit 1
fi

mkdir -p "$target" || {
	echo "setup-gocache: could not create \"$target\"" >&2
	exit 1
}

current=$(go env GOCACHE 2>/dev/null)
if [ "$current" = "$target" ]; then
	echo "setup-gocache: GOCACHE already set to $target"
	exit 0
fi

go env -w GOCACHE="$target" || {
	echo "setup-gocache: 'go env -w GOCACHE=...' failed" >&2
	exit 1
}

echo "setup-gocache: GOCACHE set to $target (was: ${current:-<default>})"
echo "This is a per-machine setting (go env -w writes outside the repo checkout)."
echo "Run this again on any other machine that will build this repo."
