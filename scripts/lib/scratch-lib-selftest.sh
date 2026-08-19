#!/usr/bin/env bash
# scratch-lib-selftest.sh — tests the scratch guard in scripts/scratch-lib.sh,
# and pins the one case that must never regress (kata 5hs2).
#
# The regression: a suite that built its scratch with an unchecked `mktemp -d`
# and then canonicalized it with `work="$(cd "$work" && pwd -P)"` resolved $work
# to its OWN WORKING DIRECTORY when mktemp failed, because `cd ""` succeeds and
# leaves $PWD alone. The EXIT trap's `rm -rf "$work"` then deleted the checkout.
#
# Every fixture below runs the guard directly, from a throwaway working
# directory seeded with sentinel files, against an mktemp sabotaged a different
# way. Each must refuse before anything is deleted and leave every sentinel
# standing. The guard's behaviour is proven ONCE, here; that every suite and
# tool actually goes through the guard is pinned statically by
# TestNoScriptFeedsVariableToRecursiveDelete and
# TestScratchDirCannotResolveToCWD, so nothing re-runs real suites under
# sabotage to re-prove a library property per consumer.
#
# Note what no fixture does: none names "/" or "$HOME". Testing a delete by
# aiming it at the thing you are protecting means one typo, or one mutation
# run, destroys the machine. That is not hypothetical here.
set -uo pipefail

. "$(dirname "$0")/selftest-lib.sh"
scripts_dir="$(cd "$(dirname "$0")" && pwd -P)"
scratch_dir work scratch-lib-selftest
trap 'scratch_rm' EXIT

# new_victim NAME — a throwaway working directory seeded the way a repo
# checkout is: a tracked-looking file, a nested directory, and a real git repo.
# Echoes its path. If the guard's cleanup ever aims at its caller's $PWD, this
# is what it destroys, and the assertions below notice.
#
# The git repo is real rather than an empty .git so that anything running git
# from here can never fail discovery and walk up into the checkout this test is
# running from.
new_victim() {
	local victim="$work/victim-$1"
	mkdir -p "$victim/nested"
	printf 'must survive\n' >"$victim/sentinel.txt"
	printf 'must survive\n' >"$victim/nested/sentinel.txt"
	git init -q "$victim" >/dev/null 2>&1
	printf '%s\n' "$victim"
}

# assert_victim_intact DIR DESC — every seeded file still where it was.
assert_victim_intact() {
	local victim="$1" desc="$2"
	if [ -f "$victim/sentinel.txt" ] && [ -f "$victim/nested/sentinel.txt" ] && [ -d "$victim/.git" ]; then
		ok "$desc"
	else
		bad "$desc (the working directory was damaged)"
		ls -A "$victim" 2>&1 | sed 's/^/    | /'
	fi
}

# run_fixture NAME BODY [PATH_PREFIX] — write BODY as a script that sources the
# lib the way a caller does, run it from a fresh victim directory with
# PATH_PREFIX first on PATH; leaves the combined output in $fixture_out and the
# exit status in $fixture_status.
fixture_out=""
fixture_status=0
fixture_victim=""
run_fixture() {
	local name="$1" body="$2" path_prefix="${3:-}"
	fixture_victim="$(new_victim "fixture-$name")"
	fixture_out="$work/fixture-$name.out"
	printf '%s\n' "$body" >"$work/fixture-$name.sh"
	(
		cd "$fixture_victim" &&
			PATH="${path_prefix:+$path_prefix:}$PATH" \
				bash "$work/fixture-$name.sh" "$scripts_dir"
	) >"$fixture_out" 2>&1
	fixture_status=$?
}

# An mktemp that fails outright, standing in for a full or read-only TMPDIR or
# a denied sandbox.
failing_mktemp_bin="$work/failing-mktemp-bin"
mkdir -p "$failing_mktemp_bin"
cat >"$failing_mktemp_bin/mktemp" <<'FAILING_MKTEMP'
#!/bin/sh
echo "mktemp: no space left on device" >&2
exit 1
FAILING_MKTEMP
chmod +x "$failing_mktemp_bin/mktemp"

# An mktemp that exits 0 having printed nothing. This is the shape that made
# the original bug destructive rather than merely broken: the caller's variable
# ends up empty, `cd ""` succeeds, and the empty path resolves to $PWD.
empty_mktemp_bin="$work/empty-mktemp-bin"
mkdir -p "$empty_mktemp_bin"
printf '#!/bin/sh\nexit 0\n' >"$empty_mktemp_bin/mktemp"
chmod +x "$empty_mktemp_bin/mktemp"

# An mktemp that exits 0 having printed an existing directory it did not create
# — the caller's own working directory, which is the path the original bug
# arrived at, and which can be under TMPDIR like anything else.
squatting_mktemp_bin="$work/squatting-mktemp-bin"
mkdir -p "$squatting_mktemp_bin"
printf '#!/bin/sh\npwd -P\n' >"$squatting_mktemp_bin/mktemp"
chmod +x "$squatting_mktemp_bin/mktemp"

# An mktemp that exits 0 having printed a correctly-named directory that sits
# outside the TMPDIR the caller set. The fixture using it gets a TMPDIR of its
# own so that "outside TMPDIR" can be arranged without writing anywhere but
# this suite's own scratch.
straying_mktemp_bin="$work/straying-mktemp-bin"
strayed_dir="$work/outside-tmpdir-fixture.abcdef"
fixture_tmpdir="$work/fixture-tmpdir"
mkdir -p "$straying_mktemp_bin" "$strayed_dir" "$fixture_tmpdir"
printf '#!/bin/sh\nprintf "%%s\\n" "%s"\n' "$strayed_dir" >"$straying_mktemp_bin/mktemp"
chmod +x "$straying_mktemp_bin/mktemp"

# The happy path: a real directory under TMPDIR, canonicalized, removable.
run_fixture happy '#!/usr/bin/env bash
set -uo pipefail
. "$1/scratch-lib.sh"
scratch_dir dir happy-fixture
[ -d "$dir" ] || { echo "no directory created"; exit 1; }
case "$dir" in
"$(cd "${TMPDIR:-/tmp}" && pwd -P)"/happy-fixture.*) ;;
*) echo "created outside TMPDIR: $dir"; exit 1 ;;
esac
scratch_rm
[ -e "$dir" ] && { echo "not removed: $dir"; exit 1; }
echo "happy path complete"
exit 0'
assert_eq "$fixture_status" 0 "the happy path creates a canonical scratch under TMPDIR and removes it"
assert_has "$fixture_out" "happy path complete" "the happy path runs to the end"

# The delete takes no path, so a clobbered caller variable has nothing to reach
# it with. This fixture wrecks every variable a caller might hold — including
# the one scratch_dir assigned — and then runs the trap the way a dying script
# would. Nothing in the working directory may be touched, and the real scratch
# must still be reclaimed.
run_fixture ignores-caller-paths '#!/usr/bin/env bash
set -uo pipefail
. "$1/scratch-lib.sh"
scratch_dir dir refuse-fixture
mkdir -p "$PWD/decoy"
printf keep > "$PWD/decoy/sentinel.txt"
real="$dir"
dir=""
work="$PWD"
scratch_rm
echo "rm-scratch with clobbered vars returned $?"
[ -d "$PWD/decoy" ] && [ -f "$PWD/decoy/sentinel.txt" ] && echo "decoy intact"
[ -e "$real" ] || echo "real scratch reclaimed"
scratch_rm
echo "second call returned $?"
scratch_rm "$PWD"
echo "call with an argument returned $?"
[ -d "$PWD/decoy" ] && echo "decoy still intact after argument call"
exit 0'
assert_victim_intact "$fixture_victim" "scratch_rm leaves the working directory alone"
assert_has "$fixture_out" "rm-scratch with clobbered vars returned 0" "scratch_rm does not depend on any caller variable"
assert_has "$fixture_out" "decoy intact" "scratch_rm deletes nothing it did not create"
assert_has "$fixture_out" "real scratch reclaimed" "scratch_rm still removes the scratch it minted"
assert_has "$fixture_out" "second call returned 0" "scratch_rm is safe to call twice"
assert_has "$fixture_out" "call with an argument returned 2" "scratch_rm rejects an argument rather than honouring it"
assert_has "$fixture_out" "decoy still intact after argument call" "a path handed to scratch_rm is never deleted"

# An mktemp that fails outright must be refused with a diagnostic naming the
# failure, before anything exists to delete.
run_fixture failing-mktemp '#!/usr/bin/env bash
set -uo pipefail
. "$1/scratch-lib.sh"
scratch_dir dir failing-fixture
# The guard must exit above this line; the marker below is the whole test.
echo "REACHED THE DELETE"
exit 0' "$failing_mktemp_bin"
if [ "$fixture_status" -ne 0 ]; then
	ok "an mktemp that fails exits non-zero"
else
	bad "an mktemp that fails exits zero"
fi
assert_has "$fixture_out" "failed; refusing to continue" "an mktemp that fails is named as such"
assert_not_has "$fixture_out" "REACHED THE DELETE" "an mktemp that fails stops before any delete"
assert_victim_intact "$fixture_victim" "an mktemp that fails deletes nothing"

# A TMPDIR that does not exist is the other spelling of the same failure: what
# an inherited TMPDIR from a reaped sandbox looks like. mktemp itself is real
# here, so this proves the guard rejects the root before asking mktemp for
# anything.
run_fixture missing-tmpdir '#!/usr/bin/env bash
set -uo pipefail
export TMPDIR="/nonexistent-tmpdir-for-scratch-lib-selftest"
. "$1/scratch-lib.sh"
scratch_dir dir missing-tmpdir-fixture
# The guard must exit above this line; the marker below is the whole test.
echo "REACHED THE DELETE"
exit 0'
if [ "$fixture_status" -ne 0 ]; then
	ok "a TMPDIR that does not exist exits non-zero"
else
	bad "a TMPDIR that does not exist exits zero"
fi
assert_has "$fixture_out" "is not a usable directory" "a TMPDIR that does not exist is named as such"
assert_not_has "$fixture_out" "REACHED THE DELETE" "a TMPDIR that does not exist stops before any delete"
assert_victim_intact "$fixture_victim" "a TMPDIR that does not exist deletes nothing"

# An mktemp that succeeds but hands back nothing must not become $PWD.
run_fixture empty-result '#!/usr/bin/env bash
set -uo pipefail
. "$1/scratch-lib.sh"
scratch_dir dir empty-result-fixture
# The guard must exit above this line; the marker below is the whole test.
echo "REACHED THE DELETE"
exit 0' "$empty_mktemp_bin"
if [ "$fixture_status" -ne 0 ]; then
	ok "an mktemp that returns nothing exits non-zero"
else
	bad "an mktemp that returns nothing exits zero"
fi
assert_has "$fixture_out" "returned no usable directory" "an mktemp that returns nothing is named as such"
assert_not_has "$fixture_out" "REACHED THE DELETE" "an mktemp that returns nothing stops before any delete"
assert_victim_intact "$fixture_victim" "an mktemp that returns nothing deletes nothing"

# An mktemp that hands back an existing directory it did not create — here the
# caller's own, which is under TMPDIR like everything else this suite makes —
# must be refused rather than adopted as scratch and later deleted.
run_fixture squatted-dir '#!/usr/bin/env bash
set -uo pipefail
. "$1/scratch-lib.sh"
scratch_dir dir squatted-fixture
# The guard must exit above this line; the marker below is the whole test.
echo "REACHED THE DELETE"
exit 0' "$squatting_mktemp_bin"
if [ "$fixture_status" -ne 0 ]; then
	ok "an mktemp that returns a directory it did not create exits non-zero"
else
	bad "an mktemp that returns a directory it did not create exits zero"
fi
assert_has "$fixture_out" "is not the directory it was asked to create" "a squatted scratch directory is named as such"
assert_not_has "$fixture_out" "REACHED THE DELETE" "a squatted scratch directory stops before any delete"
assert_victim_intact "$fixture_victim" "a squatted scratch directory deletes nothing"

# An mktemp that hands back a correctly-named directory outside the caller's
# TMPDIR must be refused too: scratch the wave runner's leak check cannot see
# is scratch nobody will notice is still there.
run_fixture outside-tmpdir "#!/usr/bin/env bash
set -uo pipefail
export TMPDIR=\"$fixture_tmpdir\"
. \"\$1/scratch-lib.sh\"
scratch_dir dir outside-tmpdir-fixture
# The guard must exit above this line; the marker below is the whole test.
echo \"REACHED THE DELETE\"
exit 0" "$straying_mktemp_bin"
if [ "$fixture_status" -ne 0 ]; then
	ok "an mktemp that returns a path outside TMPDIR exits non-zero"
else
	bad "an mktemp that returns a path outside TMPDIR exits zero"
fi
assert_has "$fixture_out" "resolved outside TMPDIR" "a scratch path outside TMPDIR is named as such"
assert_not_has "$fixture_out" "REACHED THE DELETE" "a scratch path outside TMPDIR stops before any delete"
if [ -d "$strayed_dir" ]; then
	ok "a scratch path outside TMPDIR is left where it was found"
else
	bad "a scratch path outside TMPDIR was deleted"
fi

# Sourcing the lib after calling the guard is the ordering mistake a future
# author can make. It must fail in the safe direction: no scratch, no deletion,
# non-zero exit — which `set -u` delivers on the first unset $dir.
run_fixture bad-order '#!/usr/bin/env bash
set -uo pipefail
scratch_dir dir bad-order-fixture
# $dir is UNSET when the lib was never sourced, so `set -u` aborts here. That
# expansion is the tripwire: keep the reference, never a weapon.
: "$dir"
echo "REACHED THE DELETE"
. "$1/scratch-lib.sh"
exit 0'
if [ "$fixture_status" -ne 0 ]; then
	ok "calling the guard before sourcing the lib exits non-zero"
else
	bad "calling the guard before sourcing the lib exits zero"
fi
assert_not_has "$fixture_out" "REACHED THE DELETE" "calling the guard before sourcing the lib stops before any delete"
assert_victim_intact "$fixture_victim" "calling the guard before sourcing the lib deletes nothing"

selftest_summary
