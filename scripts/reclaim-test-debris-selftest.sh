#!/usr/bin/env bash
# reclaim-test-debris-selftest.sh - deterministic safety tests for the debris reclaimer.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/reclaim-test-debris.sh"
work="$(mktemp -d -t reclaim-test-debris-selftest.XXXXXX)"
work="$(cd "$work" && pwd -P)"
trap 'rm -rf "$work"' EXIT

. "$(dirname "$0")/selftest-lib.sh"

tmpbase="$work/tmp"
mkdir -p "$tmpbase"

run_reclaim() (
	TMPDIR="$tmpbase" SERF_DEBRIS_MIN_AGE_MINUTES="${SERF_TEST_DEBRIS_AGE:-30}" \
	SERF_GOCACHE_DEBRIS_ROOT="$work/no-gocache-root" bash "$script" "$@"
)

# A shard can keep appending to an existing nested log without changing the
# shard directory mtime. A fresh heartbeat is the explicit ownership signal
# that must protect it from an age-based sweep.
live_shard="$tmpbase/agent-test-shards.live"
stale_shard="$tmpbase/agent-test-shards.stale"
mkdir -p "$live_shard/nested" "$stale_shard"
printf 'initial\n' >"$live_shard/nested/shard.log"
: >"$live_shard/.heartbeat"
touch -t 202001010000 "$live_shard" "$stale_shard"
printf 'appended while the shard directory stayed old\n' >>"$live_shard/nested/shard.log"

out="$work/shards-dry-run.out"
if SERF_TEST_DEBRIS_AGE=0 run_reclaim --dry-run >"$out" 2>&1; then
	ok "a shard debris dry-run exits zero"
else
	bad "a shard debris dry-run exits nonzero"
fi
assert_has "$out" "would remove: $stale_shard" "an old inactive shard is eligible"
if grep -qF "would remove: $live_shard" "$out"; then
	bad "a live heartbeat shard is eligible despite its old directory mtime"
else
	ok "a live heartbeat shard is protected despite its old directory mtime"
fi
assert_has "$out" "kept as possibly-live: 1" "the live shard is counted as kept"

if run_reclaim --yes >"$work/shards-reclaim.out" 2>&1; then
	ok "a shard debris reclaim exits zero"
else
	bad "a shard debris reclaim exits nonzero"
fi
[ -d "$live_shard" ] && ok "reclaim leaves the live shard in place" || bad "reclaim removed the live shard"
[ ! -e "$stale_shard" ] && ok "reclaim removes the old inactive shard" || bad "reclaim kept the old inactive shard"

# A heartbeat is not permanent protection: an abandoned marker ages out and
# the next sweep can reclaim the directory.
expired_shard="$tmpbase/agent-test-shards.expired"
mkdir -p "$expired_shard"
: >"$expired_shard/.heartbeat"
touch -t 202001010000 "$expired_shard" "$expired_shard/.heartbeat"
if run_reclaim --yes >"$work/expired-reclaim.out" 2>&1; then
	ok "an expired heartbeat reclaim exits zero"
else
	bad "an expired heartbeat reclaim exits nonzero"
fi
[ ! -e "$expired_shard" ] && ok "reclaim removes an expired heartbeat shard" || bad "reclaim keeps an expired heartbeat shard"

# Fake only the go boundary and point the debris path at throwaway fixtures.
# The real reclaimer still performs its canonical directory resolution and
# deletion decisions; no user GOCACHE or fixed /tmp debris path is touched.
fake_bin="$work/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/go" <<'FAKE_GO'
#!/bin/bash
if [ "${1:-}" = env ] && [ "${2:-}" = GOCACHE ]; then
	printf '%s\n' "${FAKE_GOCACHE:-}"
	exit 0
fi
exit 64
FAKE_GO
chmod +x "$fake_bin/go"

run_cache() (
	local live="$1" debris_root="$2"
	shift 2
	PATH="$fake_bin:/usr/bin:/bin" FAKE_GOCACHE="$live" \
	TMPDIR="$tmpbase" SERF_DEBRIS_MIN_AGE_MINUTES=30 \
	SERF_GOCACHE_DEBRIS_ROOT="$debris_root" bash "$script" "$@"
)

cache_root="$work/cache"
cache="$cache_root/serf-gocache-k3"
cache_link="$work/cache-link"
mkdir -p "$cache"
printf 'fixture cache\n' >"$cache/blob"
ln -s "$cache" "$cache_link"

equivalent_out="$work/cache-equivalent.out"
if run_cache "$cache_link/" "$cache_root" --yes >"$equivalent_out" 2>&1; then
	ok "an equivalent cache spelling exits zero"
else
	bad "an equivalent cache spelling exits nonzero"
fi
assert_has "$equivalent_out" "REFUSING $cache" "canonical cache identity detects symlink and trailing-slash equivalence"
[ -d "$cache" ] && ok "equivalent live-cache spelling is never deleted" || bad "equivalent live-cache spelling was deleted"

missing_out="$work/cache-missing.out"
missing_cache="$cache_root/missing"
if run_cache "$missing_cache" "$cache_root" --yes >"$missing_out" 2>&1; then
	bad "an unresolvable live cache spelling exits zero"
else
	ok "an unresolvable live cache spelling exits nonzero"
fi
assert_has "$missing_out" "could not establish canonical filesystem identity" "cache identity failure is reported"
[ -d "$cache" ] && ok "unresolvable live-cache identity fails closed" || bad "unresolvable live-cache identity allowed deletion"

other_cache="$cache_root/other"
mkdir -p "$other_cache"
if run_cache "$other_cache" "$cache_root" --yes >"$work/cache-distinct.out" 2>&1; then
	ok "a distinct verified live cache exits zero"
else
	bad "a distinct verified live cache exits nonzero"
fi
[ ! -e "$cache" ] && ok "a distinct verified live cache permits debris removal" || bad "a distinct verified live cache blocked debris removal"

# An abandoned fixed-namespace cache is read-only evidence for a dry run: the
# reclaimer may report it, but must not remove it until the operator chooses
# --yes. Keep the live cache distinct so the identity guard is exercised too.
abandoned="$cache_root/serf-gocache-k3"
mkdir -p "$abandoned"
printf 'abandoned fixture cache\n' >"$abandoned/blob"
dry_cache_out="$work/cache-abandoned-dry-run.out"
if run_cache "$other_cache" "$cache_root" --dry-run >"$dry_cache_out" 2>&1; then
	ok "an abandoned-cache dry-run exits zero"
else
	bad "an abandoned-cache dry-run exits nonzero"
fi
assert_has "$dry_cache_out" "gocache debris: $abandoned" "a dry-run reports the abandoned fixed-namespace cache"
assert_has "$dry_cache_out" "dry run: nothing removed" "a dry-run declares that it made no changes"
[ -d "$abandoned" ] && ok "a dry-run leaves the abandoned cache in place" || bad "a dry-run removed the abandoned cache"
[ -d "$other_cache" ] && ok "a dry-run leaves the verified live cache in place" || bad "a dry-run removed the live cache"

selftest_summary
