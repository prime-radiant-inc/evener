#!/usr/bin/env bash
# fuzz-bisect.sh — find the commit that introduced a fuzz crasher (roadmap 8.5,
# continuous infra: regression bisection). Given a saved Go fuzz corpus file
# that reproduces a crash and a known-good ref, it drives `git bisect run` to
# pinpoint the first bad commit, replaying that one corpus entry at each step.
#
# Usage:
#   scripts/fuzz-bisect.sh --target MODULE:FUZZNAME --crasher FILE --good REF [--bad REF]
#     --target   the registry entry "module:FuzzName" whose corpus entry crashes
#                (one of `scripts/run-fuzz.sh --list`, native targets only).
#     --crasher  path to the Go fuzz corpus file (begins "go test fuzz v1") that
#                reproduces the crash — typically the file the toolchain saved
#                under testdata/fuzz/<FuzzName>/ when it found the crasher.
#     --good     a ref (commit/tag/branch) where the crasher does NOT reproduce.
#     --bad      a ref where it DOES reproduce (default: HEAD).
#
# It first confirms the crash reproduces at --bad and not at --good (bisection
# is meaningless without that bracket), then bisects. A commit where the target
# does not build, or does not yet exist, is skipped rather than misjudged. The
# working tree is restored (git bisect reset) on exit.
#
# Env seams (defaults are production; used by the self-test):
#   SERF_FUZZ_REPO_ROOT  repo root (default: the parent of this script's dir)
#   SERF_FUZZ_RUNNER     the registry source (default: scripts/run-fuzz.sh)
#   SERF_FUZZ_GO         the go toolchain     (default: go)
#   SERF_FUZZ_TAGS       build tags for replay (default: serffuzz)
set -uo pipefail

repo_root="${SERF_FUZZ_REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
runner="${SERF_FUZZ_RUNNER:-$repo_root/scripts/run-fuzz.sh}"
go_bin="${SERF_FUZZ_GO:-go}"
tags="${SERF_FUZZ_TAGS:-serffuzz}"

target=""
crasher=""
good=""
bad="HEAD"
probe_mode=false # internal: print the reproduce status at the current checkout

while [ $# -gt 0 ]; do
	case "$1" in
		--target) target="$2"; shift 2 ;;
		--target=*) target="${1#*=}"; shift ;;
		--crasher) crasher="$2"; shift 2 ;;
		--crasher=*) crasher="${1#*=}"; shift ;;
		--good) good="$2"; shift 2 ;;
		--good=*) good="${1#*=}"; shift ;;
		--bad) bad="$2"; shift 2 ;;
		--bad=*) bad="${1#*=}"; shift ;;
		--__probe) probe_mode=true; shift ;;
		-h|--help) sed -n '2,30p' "$0"; exit 0 ;;
		*) echo "fuzz-bisect: unexpected argument $1" >&2; exit 2 ;;
	esac
done

[ -n "$target" ]  || { echo "fuzz-bisect: --target is required" >&2; exit 2; }
[ -n "$crasher" ] || { echo "fuzz-bisect: --crasher is required" >&2; exit 2; }
$probe_mode || [ -n "$good" ] || { echo "fuzz-bisect: --good is required" >&2; exit 2; }
[ -f "$crasher" ] || { echo "fuzz-bisect: crasher file not found: $crasher" >&2; exit 2; }
crasher="$(cd "$(dirname "$crasher")" && pwd)/$(basename "$crasher")" # absolutise
if ! head -n1 "$crasher" | grep -q '^go test fuzz v1'; then
	echo "fuzz-bisect: --crasher is not a Go fuzz corpus file (expected a 'go test fuzz v1' header)" >&2
	exit 2
fi

# Resolve module + package + FuzzName from the registry.
module="" pkg="" name=""
while IFS=: read -r tag m p n _rest; do
	[ "$tag" = native ] || continue
	if [ "$m:$n" = "$target" ]; then module="$m"; pkg="$p"; name="$n"; break; fi
done < <(bash "$runner" --list)
[ -n "$name" ] || { echo "fuzz-bisect: no native target '$target' in the registry" >&2; exit 2; }

# Where Go reads the corpus for this target, and the fixed name we replay under.
seed_dir="$repo_root/$module/${pkg#./}/testdata/fuzz/$name"
[ "$pkg" = "." ] && seed_dir="$repo_root/$module/testdata/fuzz/$name"
probe_name="__bisect_probe"

# reproduce_status replays just the crasher at the CURRENT checkout and prints
# one of: good (passes), bad (crashes), skip (does not build / target absent).
reproduce_status() {
	mkdir -p "$seed_dir"
	cp "$crasher" "$seed_dir/$probe_name"
	local out rc
	out="$(cd "$repo_root/$module" && "$go_bin" test -tags "$tags" -run "^${name}\$/${probe_name}\$" -count=1 "$pkg" 2>&1)"
	rc=$?
	rm -f "$seed_dir/$probe_name"
	rmdir "$seed_dir" 2>/dev/null || true
	if printf '%s' "$out" | grep -qiE 'build failed|cannot find|undefined:|no required module|no tests to run|unknown'; then
		echo skip; return
	fi
	[ "$rc" -eq 0 ] && echo good || echo bad
}

# Probe mode (invoked by `git bisect run` at each checkout): just print the
# status at the current tree and exit — never touch the checkout itself.
if $probe_mode; then
	reproduce_status
	exit 0
fi

# Bracket sanity: the crash must reproduce at --bad and not at --good, or the
# bisection has nothing to find.
orig_ref="$(git -C "$repo_root" rev-parse --abbrev-ref HEAD 2>/dev/null)"
[ "$orig_ref" = HEAD ] && orig_ref="$(git -C "$repo_root" rev-parse HEAD)"
restore() { git -C "$repo_root" bisect reset >/dev/null 2>&1 || true; git -C "$repo_root" checkout -q "$orig_ref" 2>/dev/null || true; }
trap restore EXIT

# Pin the endpoints to concrete commits NOW: the bracket checks below move HEAD,
# so a symbolic --bad/--good (e.g. the default HEAD) must be resolved first or it
# would drift to wherever the last checkout landed.
bad="$(git -C "$repo_root" rev-parse "$bad")"   || { echo "fuzz-bisect: bad ref not found" >&2; exit 2; }
good="$(git -C "$repo_root" rev-parse "$good")" || { echo "fuzz-bisect: good ref not found" >&2; exit 2; }

echo "fuzz-bisect: confirming the bracket for $target …"
git -C "$repo_root" checkout -q "$bad"
b="$(reproduce_status)"
git -C "$repo_root" checkout -q "$good"
g="$(reproduce_status)"
echo "    $bad → $b ;  $good → $g"
if [ "$b" != bad ]; then
	echo "fuzz-bisect: crasher does NOT reproduce at --bad ($bad); nothing to bisect." >&2
	exit 1
fi
if [ "$g" != good ]; then
	echo "fuzz-bisect: crasher already present (or unbuildable) at --good ($good); widen --good." >&2
	exit 1
fi

# The probe git bisect runs at each step: exit 0=good, 1=bad, 125=skip.
probe="$(mktemp -t fuzz-bisect-probe.XXXXXX.sh)"
cat >"$probe" <<PROBE
#!/usr/bin/env bash
s="\$(SERF_FUZZ_REPO_ROOT='$repo_root' SERF_FUZZ_RUNNER='$runner' SERF_FUZZ_GO='$go_bin' SERF_FUZZ_TAGS='$tags' bash '$0' --__probe --target '$target' --crasher '$crasher')"
case "\$s" in
	good) exit 0 ;;
	bad)  exit 1 ;;
	*)    exit 125 ;;
esac
PROBE
chmod +x "$probe"

echo "fuzz-bisect: bisecting $good..$bad …"
git -C "$repo_root" bisect start "$bad" "$good" >/dev/null
set +e
bisect_out="$(git -C "$repo_root" bisect run "$probe" 2>&1)"
set -e
rm -f "$probe"

culprit="$(printf '%s\n' "$bisect_out" | grep -iE 'is the first bad commit' | head -n1)"
echo "==============================================================="
if [ -n "$culprit" ]; then
	echo "fuzz-bisect: introduced by:"
	printf '%s\n' "$bisect_out" | sed -n '/is the first bad commit/,/^$/p' | sed 's/^/    /'
else
	echo "fuzz-bisect: bisection did not converge cleanly; raw output:"
	printf '%s\n' "$bisect_out" | sed 's/^/    /'
fi
echo "==============================================================="
