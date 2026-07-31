#!/usr/bin/env bash
# report-tmp-debris-selftest.sh — offline, deterministic test of
# scripts/report-tmp-debris.sh (kata td3g), against a synthetic debris root.
#
# The real root is /tmp, which the selftest must never scan and must never
# write to; SERF_TMP_DEBRIS_ROOT exists so this test can point the script at a
# throwaway tree instead. Every scenario below also asserts the fixture still
# exists afterwards: this script's entire contract is that it reports and
# deletes nothing.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/report-tmp-debris.sh"
checks=0 fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }

work="$(mktemp -d -t report-tmp-debris-selftest.XXXXXX)"
# Resolve symlinks now (macOS's /var/folders is a symlink to /private/var/folders):
# the script prints the paths it was given, and --paths-only is compared by
# string equality below.
work="$(cd "$work" && pwd -P)"
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
mkdir -p "$repo"
(
	cd "$repo" &&
		git init -q &&
		git config user.email t@t &&
		git config user.name t &&
		git symbolic-ref HEAD refs/heads/main &&
		echo one >file &&
		git add -A &&
		git commit -qm init
) || {
	echo "FAIL: could not set up throwaway repo" >&2
	exit 1
}

# The fixture debris root. Sizes are deliberately far apart so the biggest-first
# ordering assertion cannot turn on filesystem block-rounding.
root="$work/faketmp"
mkdir -p "$root/serf-big" "$root/serf-small" "$root/other-thing"
mkdir -p "$root/serf-big/nested"
dd if=/dev/zero of="$root/serf-big/nested/blob" bs=1024 count=512 2>/dev/null
dd if=/dev/zero of="$root/serf-small/blob" bs=1024 count=64 2>/dev/null
# Debris is not all checkouts: kata gmpr's listing is screenshots, DOM dumps and
# logs as well, so a plain FILE matching the pattern has to be reported too.
echo "a log line" >"$root/serf-note.log"
echo "not ours" >"$root/other-thing/keep-me"
# A stale entry and a fresh one, so the RECENT marker has both cases to
# distinguish. touch -t backdates mtime without changing anything else.
touch -t 202001010000 "$root/serf-small"

out="$(cd "$repo" && SERF_TMP_DEBRIS_ROOT="$root" bash "$script")"

for name in serf-big serf-small serf-note.log; do
	if echo "$out" | grep -q "$name"; then
		ok "debris entry is reported ($name)"
	else
		bad "debris entry was NOT reported ($name): $out"
	fi
done
if echo "$out" | grep -q "other-thing"; then
	bad "an entry not matching the serf* pattern was reported: $out"
else
	ok "an entry not matching the serf* pattern is not reported"
fi
# Without this, "report every entry in the root" would satisfy the assertions
# above while making the pattern meaningless.
if echo "$out" | grep -qE 'Total:.*3 entr'; then
	ok "the total names the entry count"
else
	bad "no total line naming 3 entries: $out"
fi

# The volume note: debris on a different volume from the checkout cannot move
# this repo's disk floor, and saying otherwise is the exact dishonesty this
# kata exists to remove. Here the fixture and the repo are both under $work,
# so the same-volume branch is the one under test.
if echo "$out" | grep -q "same volume"; then
	ok "report says whether the debris shares the checkout's volume"
else
	bad "report did not say whether the debris is on the checkout's volume: $out"
fi

# RECENT is a one-way signal and the report has to be readable as one: a fresh
# mtime means somebody may still be using it, a stale mtime proves nothing.
recent_line=$(echo "$out" | grep 'serf-big')
stale_line=$(echo "$out" | grep 'serf-small')
if echo "$recent_line" | grep -q "RECENT"; then
	ok "a freshly-touched entry is flagged RECENT"
else
	bad "a freshly-touched entry was not flagged RECENT: $recent_line"
fi
if echo "$stale_line" | grep -q "RECENT"; then
	bad "an entry last touched in 2020 was flagged RECENT: $stale_line"
else
	ok "a long-stale entry is not flagged RECENT"
fi

if echo "$out" | grep -qi "Nothing here was deleted"; then
	ok "report states nothing was deleted"
else
	bad "report did not state that nothing was deleted: $out"
fi
for name in serf-big serf-small serf-note.log other-thing; do
	if [ -e "$root/$name" ]; then
		ok "fixture entry survives a report-only run ($name)"
	else
		bad "fixture entry was REMOVED by a report-only run ($name)"
	fi
done

# --paths-only is what feeds kata gmpr's authorized sweep, so its ordering and
# its exact contents both matter: biggest first, one absolute path per line,
# nothing else on the line.
paths_out="$(cd "$repo" && SERF_TMP_DEBRIS_ROOT="$root" bash "$script" --paths-only)"
expected=$(printf '%s\n%s\n%s' "$root/serf-big" "$root/serf-small" "$root/serf-note.log")
if [ "$paths_out" = "$expected" ]; then
	ok "--paths-only prints exactly the matching paths, biggest first"
else
	bad "--paths-only output unexpected: $paths_out"
fi

# An empty root is the state the sweep is trying to reach; it must read as
# success, not as a broken scan.
empty_root="$work/empty-tmp"
mkdir -p "$empty_root"
if empty_out="$(cd "$repo" && SERF_TMP_DEBRIS_ROOT="$empty_root" bash "$script")"; then
	if echo "$empty_out" | grep -qi "none found"; then
		ok "an empty debris root reports 'none found' and exits 0"
	else
		bad "an empty debris root exited 0 without saying so: $empty_out"
	fi
else
	bad "an empty debris root exited non-zero"
fi

# A root that does not exist is a typo or a wrong override, not "no debris":
# reporting "none found" for it would read as a clean machine.
if (cd "$repo" && SERF_TMP_DEBRIS_ROOT="$work/no-such-root" bash "$script" >"$work/missing.out" 2>&1); then
	bad "a nonexistent debris root exited 0: $(cat "$work/missing.out")"
else
	ok "a nonexistent debris root fails instead of reporting 'none found'"
fi

# Run from outside any git repository: the volume comparison needs a checkout to
# compare against, and not having one must degrade to a quieter report rather
# than to a failure.
if outside_out="$(cd "$work" && SERF_TMP_DEBRIS_ROOT="$root" bash "$script")"; then
	if echo "$outside_out" | grep -q "serf-big"; then
		ok "runs outside a git repository and still reports the debris"
	else
		bad "run outside a git repository reported no debris: $outside_out"
	fi
else
	bad "run outside a git repository exited non-zero: $outside_out"
fi

echo
echo "$checks checks, $fails failed"
[ "$fails" -eq 0 ]
