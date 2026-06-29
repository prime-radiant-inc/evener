#!/usr/bin/env bash
# fuzz-oracle-audit.sh — prove every fuzz oracle reddens on the bug class it
# claims to catch (Phase 9, W1: detection-sensitivity audit). For each mutation
# in fuzz/mutations/manifest.tsv — a small patch that reintroduces a known fault,
# paired with the target whose oracle should catch it — apply it in a THROWAWAY
# git worktree at HEAD, run that target's seed corpus, and assert the run FAILS.
# A mutation that leaves the target green is a BLIND oracle and fails the audit.
# A patch that no longer applies is loud ("re-derive it"), never a silent skip.
# The production tree is never touched — the fault lives only in the disposable
# worktree, so a reintroduced bug can never be left behind.
#
# Usage:
#   scripts/fuzz-oracle-audit.sh [--gap-only] [mutation-id ...]
#     --gap-only   skip the runs; just list native targets that have no mutation.
#     mutation-id  audit only these ids (default: every mutation in the manifest).
#
# Env seams (defaults are production; used by the self-test):
#   SERF_FUZZ_REPO_ROOT  repo root (default: the parent of this script's dir)
#   SERF_FUZZ_RUNNER     the registry source (default: scripts/run-fuzz.sh)
#   SERF_FUZZ_GO         the go toolchain (default: go)
#   SERF_FUZZ_TAGS       build tags for the replay (default: serffuzz)
set -uo pipefail

repo_root="${SERF_FUZZ_REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
runner="${SERF_FUZZ_RUNNER:-$repo_root/scripts/run-fuzz.sh}"
go_bin="${SERF_FUZZ_GO:-go}"
tags="${SERF_FUZZ_TAGS:-serffuzz}"
manifest="$repo_root/fuzz/mutations/manifest.tsv"

gap_only=false
declare -a only=()
while [ $# -gt 0 ]; do
	case "$1" in
		--gap-only) gap_only=true; shift ;;
		-h|--help) sed -n '2,21p' "$0"; exit 0 ;;
		--*) echo "fuzz-oracle-audit: unknown flag $1" >&2; exit 2 ;;
		*) only+=("$1"); shift ;;
	esac
done

# native_targets prints every native target as "module:FuzzName"; mutated_targets
# prints the ones a manifest row covers. The registry fields are colon-separated
# and no field VALUE contains a colon, so awk -F: is exact.
native_targets() { bash "$runner" --list | awk -F: '$1=="native"{print $2":"$4}'; }
mutated_targets() { [ -f "$manifest" ] && awk -F'\t' '$1!~/^#/ && NF>=2{print $2}' "$manifest" | sort -u; }

report_gaps() {
	local t covered=0 gap=0 muts
	muts="$(mutated_targets)"
	echo "fuzz-oracle-audit: oracle coverage"
	while read -r t; do
		[ -n "$t" ] || continue
		if printf '%s\n' "$muts" | grep -qxF "$t"; then
			covered=$((covered + 1))
		else
			echo "    UNAUDITED: $t"; gap=$((gap + 1))
		fi
	done < <(native_targets | sort -u)
	echo "    $covered audited, $gap unaudited native target(s)"
}

if $gap_only; then report_gaps; exit 0; fi
[ -f "$manifest" ] || { echo "fuzz-oracle-audit: no manifest at $manifest" >&2; exit 2; }

# One disposable worktree at HEAD, reused across mutations (apply → run → revert).
wt="$(mktemp -d -t fuzz-oracle-audit.XXXXXX)"
git -C "$repo_root" worktree add --detach "$wt" HEAD >/dev/null 2>&1 \
	|| { echo "fuzz-oracle-audit: cannot create worktree at $wt" >&2; exit 2; }
cleanup() { git -C "$repo_root" worktree remove --force "$wt" >/dev/null 2>&1 || true; }
trap cleanup EXIT

want() {
	[ ${#only[@]} -eq 0 ] && return 0
	local id="$1" o
	for o in "${only[@]}"; do [ "$o" = "$id" ] && return 0; done
	return 1
}

# pkg_for resolves "module:FuzzName" to "module<TAB>pkg<TAB>name" via the registry.
pkg_for() {
	bash "$runner" --list | awk -F: -v t="$1" '$1=="native" && ($2":"$4)==t {print $2"\t"$3"\t"$4; exit}'
}

# run_seeds runs the target's seed corpus in the worktree, capturing output in
# REPRO_OUT and exit code in REPRO_RC. build_failed reports whether the run did
# not compile — a mutation that fails to BUILD must score ERROR, never "caught",
# or the audit would credit the oracle for a crash it never saw.
run_seeds() {
	REPRO_OUT="$(cd "$wt/$1" && "$go_bin" test ${tags:+-tags "$tags"} -run "^$3\$" -count=1 "$2" 2>&1)"
	REPRO_RC=$?
}
build_failed() { printf '%s' "$REPRO_OUT" | grep -qE '\[build failed\]|\[setup failed\]'; }

declare -A clean_ok=()
pass=0 blind=0 rot=0 err=0 audited=0

while IFS=$'\t' read -r id target patchfile desc; do
	[ -n "$id" ] || continue
	case "$id" in \#*) continue ;; esac
	want "$id" || continue
	audited=$((audited + 1))

	resolved="$(pkg_for "$target")"
	if [ -z "$resolved" ]; then echo "ERR  $id — target '$target' not in registry"; err=$((err + 1)); continue; fi
	IFS=$'\t' read -r module pkg name <<<"$resolved"
	patch="$repo_root/fuzz/mutations/$patchfile"
	[ -f "$patch" ] || { echo "ERR  $id — patch file missing: $patchfile"; err=$((err + 1)); continue; }

	# Clean sanity per target (once): the un-mutated target must pass, or a
	# "blind" verdict would be meaningless.
	if [ -z "${clean_ok[$target]:-}" ]; then
		git -C "$wt" checkout -q -- . 2>/dev/null || true
		run_seeds "$module" "$pkg" "$name"
		if [ "$REPRO_RC" -eq 0 ]; then clean_ok[$target]=ok; else clean_ok[$target]=bad; fi
	fi
	if [ "${clean_ok[$target]}" != ok ]; then
		echo "ERR  $id — target '$target' already fails on a CLEAN tree (cannot audit)"; err=$((err + 1)); continue
	fi

	git -C "$wt" checkout -q -- . 2>/dev/null || true
	if ! git -C "$wt" apply "$patch" 2>/dev/null; then
		echo "ROT  $id — patch no longer applies ($patchfile); re-derive it from the current code"; rot=$((rot + 1)); continue
	fi
	run_seeds "$module" "$pkg" "$name"
	if build_failed; then
		echo "ERR  $id — mutation does not compile ($target); a patch must reintroduce a bug, not break the build"; err=$((err + 1))
	elif [ "$REPRO_RC" -eq 0 ]; then
		echo "BLIND $id — oracle did NOT redden ($target): $desc"; blind=$((blind + 1))
	else
		echo "ok   $id — oracle caught it ($target)"; pass=$((pass + 1))
	fi
	git -C "$wt" checkout -q -- . 2>/dev/null || true
done < "$manifest"

echo "----"
echo "fuzz-oracle-audit: $pass caught, $blind blind, $rot rotted, $err error (of $audited)"
report_gaps
[ "$blind" -eq 0 ] && [ "$rot" -eq 0 ] && [ "$err" -eq 0 ]
