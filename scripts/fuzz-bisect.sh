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
		-h|--help) sed -n '2,26p' "$0"; exit 0 ;;
		*) echo "fuzz-bisect: unexpected argument $1" >&2; exit 2 ;;
	esac
done

[ -n "$target" ]  || { echo "fuzz-bisect: --target is required" >&2; exit 2; }
[ -n "$crasher" ] || { echo "fuzz-bisect: --crasher is required" >&2; exit 2; }
[ -n "$good" ]    || { echo "fuzz-bisect: --good is required" >&2; exit 2; }
[ -f "$crasher" ] || { echo "fuzz-bisect: crasher file not found: $crasher" >&2; exit 2; }
crasher="$(cd "$(dirname "$crasher")" && pwd)/$(basename "$crasher")" # absolutise
if ! head -n1 "$crasher" | grep -q '^go test fuzz v1'; then
	echo "fuzz-bisect: --crasher is not a Go fuzz corpus file (expected a 'go test fuzz v1' header)" >&2
	exit 2
fi

# Resolve module + package + FuzzName from the registry ONCE, at the current
# checkout (where the registry exists). These resolved values get baked into the
# self-contained probe below, so the per-step replay never re-reads a repo script
# that a checkout might have removed.
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

# Generate a SELF-CONTAINED probe. `git bisect run` checks out each candidate
# commit in $repo_root, which DELETES any tracked file that did not exist at that
# commit — including this script and run-fuzz.sh, both new. So the probe must
# depend on NOTHING in the repo except git's checkout of the target package
# itself: every path, the crasher file (which lives outside the repo), the go
# binary, and the build tags are baked in as literals, and it never re-invokes
# this script or the registry. It classifies the current checkout for git bisect:
#   exit 0 = good (seed passes), 1 = bad (it crashes), 125 = skip (the target does
#   not build or does not exist here). Skip is judged off go's OWN markers, never
#   off arbitrary words in a panic/oracle message — 'unknown', 'cannot find', etc.
#   appear in real failure text and must not be read as "did not build".
probe="$(mktemp -t fuzz-bisect-probe.XXXXXX.sh)"
cat >"$probe" <<PROBE
#!/usr/bin/env bash
seed_dir='$seed_dir'
mkdir -p "\$seed_dir" 2>/dev/null || exit 125
cp '$crasher' "\$seed_dir/$probe_name" 2>/dev/null || exit 125
out="\$(cd '$repo_root/$module' 2>/dev/null && '$go_bin' test ${tags:+-tags '$tags'} -run '^${name}\$/${probe_name}\$' -count=1 '$pkg' 2>&1)"
rc=\$?
rm -f "\$seed_dir/$probe_name"; rmdir "\$seed_dir" 2>/dev/null
# Target/package does not build, or is absent, at this commit -> skip.
if printf '%s' "\$out" | grep -qE '\[build failed\]|\[setup failed\]'; then exit 125; fi
if printf '%s' "\$out" | grep -qF 'no tests to run'; then exit 125; fi
# Module directory absent at this commit: the cd failed, go never ran, output empty.
if [ -z "\$out" ] && [ "\$rc" -ne 0 ]; then exit 125; fi
[ "\$rc" -eq 0 ] && exit 0 || exit 1
PROBE
chmod +x "$probe"

orig_ref="$(git -C "$repo_root" rev-parse --abbrev-ref HEAD 2>/dev/null)"
[ "$orig_ref" = HEAD ] && orig_ref="$(git -C "$repo_root" rev-parse HEAD)"
restore() { git -C "$repo_root" bisect reset >/dev/null 2>&1 || true; git -C "$repo_root" checkout -q "$orig_ref" 2>/dev/null || true; rm -f "$probe"; }
trap restore EXIT

# Pin the endpoints to concrete commits NOW: the bracket checks below move HEAD,
# so a symbolic --bad/--good (e.g. the default HEAD) must be resolved first or it
# would drift to wherever the last checkout landed.
bad="$(git -C "$repo_root" rev-parse "$bad")"   || { echo "fuzz-bisect: bad ref not found" >&2; exit 2; }
good="$(git -C "$repo_root" rev-parse "$good")" || { echo "fuzz-bisect: good ref not found" >&2; exit 2; }

# Classify a checkout with the SAME self-contained probe git bisect will use.
status_word() { case "$1" in 0) echo good ;; 1) echo bad ;; *) echo skip ;; esac; }

echo "fuzz-bisect: confirming the bracket for $target …"
git -C "$repo_root" checkout -q "$bad"  || { echo "fuzz-bisect: cannot check out --bad ($bad)" >&2; exit 2; }
bash "$probe"; b=$?
git -C "$repo_root" checkout -q "$good" || { echo "fuzz-bisect: cannot check out --good ($good)" >&2; exit 2; }
bash "$probe"; g=$?
echo "    $bad → $(status_word "$b") ;  $good → $(status_word "$g")"
if [ "$b" -ne 1 ]; then
	echo "fuzz-bisect: crasher does NOT reproduce at --bad ($bad) [$(status_word "$b")]; nothing to bisect." >&2
	exit 1
fi
if [ "$g" -ne 0 ]; then
	echo "fuzz-bisect: crasher already present, or unbuildable, at --good ($good) [$(status_word "$g")]; widen --good." >&2
	exit 1
fi

echo "fuzz-bisect: bisecting $good..$bad …"
git -C "$repo_root" bisect start "$bad" "$good" >/dev/null
bisect_out="$(git -C "$repo_root" bisect run "$probe" 2>&1)"

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
