#!/usr/bin/env bash
# parallelize-tests.sh — add t.Parallel() to a package's serial tests, one file
# at a time, keeping only the files that still pass.
#
# WHY: Go runs every non-t.Parallel() test to completion before releasing any
# parallel test, so a serial test's cost is paid on a single core while the rest
# of the suite waits. In the agent package that prefix was ~16s of a ~38s run.
# Converting serial tests is worthwhile but *not* uniformly safe: some share
# process-wide state (env vars, cwd, singletons, global counters) that only
# fails under concurrency. Static screening misses these — hazards hide behind
# helper functions, and shared-state collisions are invisible in the source.
#
# HOW: this script is empirical, not clever. For each candidate file it adds
# t.Parallel(), runs the package's full test suite, and reverts that file if
# anything fails. A file is kept only when the whole package still passes with
# it converted. Verification is by exit code, never by grepping output.
#
# USAGE:
#   scripts/parallelize-tests.sh --dir agent --candidates /path/to/list [--jobs N]
#   scripts/parallelize-tests.sh --dir agent --candidates list --dry-run
#
#   --dir DIR           package directory to work in (required)
#   --candidates FILE   newline-separated test function names eligible for
#                       conversion (required). Anything absent is left alone.
#   --run REGEX         -run regex for the verification suite
#                       (default '^(Test|Example)')
#   --parallel N        -parallel for verification (default 32)
#   --runs N            suite runs per verification (default 1). Shared-state
#                       couplings are often intermittent: a sweep that passed a
#                       single run per file was later measured failing 6 of 12
#                       full runs. Use --runs 3 or more when the result is meant
#                       to be trusted; a verification passes only if every run
#                       passes.
#   --dry-run           report what would be attempted, change nothing
#   --keep-going        continue after a file fails (default: yes)
#   --bisect            convert every candidate file at once, then binary-search
#                       out the files that break the suite. Much faster than the
#                       default when most files are safe: the default pays one
#                       full suite run per file (107 files x ~25s = ~45min, worse
#                       under load), while bisect pays roughly
#                       log2(files) x (1 + number of bad files) runs.
#
# OUTPUT: one line per file — KEEP (converted, suite green) or REVERT (suite
# red, change undone) — then a summary. Full output of a failing verification
# run is written to the log dir named in the summary, so this stays readable
# while the details remain recoverable.
#
# EXIT: 0 if every attempted file was resolved (kept or cleanly reverted),
# non-zero if the package could not be returned to a passing state.
set -uo pipefail

dir=""; candidates=""; runre='^(Test|Example)'; par=32; dryrun=0; bisect=0; runs=1

die() { printf 'parallelize-tests: %s\n' "$1" >&2; exit 2; }

while [ $# -gt 0 ]; do
	case "$1" in
		--dir) dir="${2:-}"; shift 2 ;;
		--candidates) candidates="${2:-}"; shift 2 ;;
		--run) runre="${2:-}"; shift 2 ;;
		--parallel) par="${2:-}"; shift 2 ;;
		--runs) runs="${2:-}"; shift 2 ;;
		--dry-run) dryrun=1; shift ;;
		--bisect) bisect=1; shift ;;
		--keep-going) shift ;;
		-h|--help) sed -n '2,40p' "$0"; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

[ -n "$dir" ] || die "--dir is required (see --help)"
[ -n "$candidates" ] || die "--candidates is required (see --help)"
[ -d "$dir" ] || die "no such directory: $dir"
[ -f "$candidates" ] || die "no such candidates file: $candidates"

candidates="$(cd "$(dirname "$candidates")" && pwd)/$(basename "$candidates")"
cd "$dir" || die "cannot enter $dir"

logdir="$(mktemp -d -t parallelize-tests.XXXXXX)"

# verify runs the package suite and reports success via exit code only.
verify() {
	local log="$1" i
	# Every run must pass. One green run does not establish safety: shared-state
	# collisions surface only in some interleavings.
	for i in $(seq 1 "$runs"); do
		if ! go test -short -count=1 -parallel "$par" -run "$runre" . >"$log.$i" 2>&1; then
			cp "$log.$i" "$log"
			return 1
		fi
	done
	cp "$log.$runs" "$log"
	return 0
}

# convert adds t.Parallel() to the candidate tests in one file; prints the count.
convert() {
	python3 - "$1" "$candidates" <<'PY'
import re, sys
path, candpath = sys.argv[1], sys.argv[2]
cand = set(open(candpath).read().split())
src = open(path).read()

def add(m):
    # Skip a test that already declares t.Parallel() anywhere in its signature
    # line block; the body check below guards the common case.
    return m.group(0) + '\n\tt.Parallel()' if m.group(1) in cand else m.group(0)

new = re.sub(r'func (Test\w+)\(t \*testing\.T\) \{', add, src)
# Never emit a doubled call: a test converted by an earlier run stays as-is.
new = re.sub(r'(\n\tt\.Parallel\(\)\n)(?:\tt\.Parallel\(\)\n)+', r'\1', new)
if new != src:
    open(path, 'w').write(new)
print(new.count('t.Parallel()') - src.count('t.Parallel()'))
PY
}

# Files holding at least one candidate test, most candidates first so the
# biggest wins land earliest.
# (read loop rather than mapfile: macOS ships bash 3.2, which lacks mapfile.)
files=()
while IFS= read -r line; do
	[ -n "$line" ] && files+=("$line")
done < <(
	python3 - "$candidates" <<'PY'
import re, glob, sys, collections
cand = set(open(sys.argv[1]).read().split())
count = collections.Counter()
for f in glob.glob('*_test.go'):
    for m in re.finditer(r'^func (Test\w+)\(t \*testing\.T\) \{', open(f).read(), re.M):
        if m.group(1) in cand:
            count[f] += 1
for f, c in count.most_common():
    print(f)
PY
)

[ "${#files[@]}" -gt 0 ] || die "no files contain any candidate test"

printf 'parallelize-tests: %d candidate files in %s\n' "${#files[@]}" "$dir"

if [ "$dryrun" -eq 1 ]; then
	printf '%s\n' "${files[@]}"
	printf 'dry run: no changes made\n'
	exit 0
fi

# Baseline: refuse to start from a red suite, or every file would look guilty.
printf 'verifying baseline ... '
if ! verify "$logdir/baseline.log"; then
	printf 'FAIL\nbaseline suite is already failing; fix that first.\nlog: %s/baseline.log\n' "$logdir"
	exit 1
fi
printf 'ok\n'

if [ "$bisect" -eq 1 ]; then
	# Convert everything, then binary-search out the bad files. Most candidate
	# files are safe, so this trades N suite runs for roughly
	# log2(N) x (1 + bad files).
	total=0
	for f in "${files[@]}"; do
		cp "$f" "$logdir/$(printf '%s' "$f" | tr / _).bak"
		n="$(convert "$f")"
		total=$((total + ${n:-0}))
	done
	printf 'converted %d tests across %d files; bisecting failures\n' "$total" "${#files[@]}"

	# revert_list <file...> — restore the pristine copies of the named files.
	revert_list() {
		local g
		for g in "$@"; do cp "$logdir/$(printf '%s' "$g" | tr / _).bak" "$g"; done
	}
	# reconvert_list <file...> — re-apply conversion to the named files.
	reconvert_list() {
		local g
		for g in "$@"; do convert "$g" >/dev/null; done
	}

	# pending = files still converted and not yet proven innocent.
	# Outer loop: while the suite fails, bisect `pending` to pin exactly one
	# culprit, revert it, and retest. Files are only ever declared good when a
	# run with all of `pending` converted comes back green, so nothing is
	# dropped unexamined.
	bad=(); runs=0
	pending=("${files[@]}")
	while :; do
		runs=$((runs + 1))
		if verify "$logdir/bisect-$runs.log"; then
			break
		fi
		if [ "${#pending[@]}" -eq 0 ]; then
			# Suite fails with nothing converted: not our doing.
			printf 'suite fails with all candidates reverted; not caused by this change\n'
			break
		fi
		if [ "${#pending[@]}" -eq 1 ]; then
			revert_list "${pending[0]}"
			printf 'REVERT  %-58s (suite failed)\n' "${pending[0]}"
			bad+=("${pending[0]}")
			pending=()
			continue
		fi
		# Narrow to one culprit: keep halving the converted set. `lo` is the
		# slice of `pending` still suspected; everything outside it is reverted
		# for the duration of the search.
		search=("${pending[@]}")
		while [ "${#search[@]}" -gt 1 ]; do
			half=$(( ${#search[@]} / 2 ))
			firstHalf=("${search[@]:0:$half}")
			secondHalf=("${search[@]:$half}")
			# Test with only firstHalf converted.
			revert_list "${secondHalf[@]}"
			runs=$((runs + 1))
			if verify "$logdir/bisect-$runs.log"; then
				# firstHalf is clean, so a culprit lives in secondHalf.
				search=("${secondHalf[@]}")
			else
				search=("${firstHalf[@]}")
			fi
			reconvert_list "${search[@]}"
		done
		culprit="${search[0]}"
		revert_list "$culprit"
		printf 'REVERT  %-58s (suite failed)\n' "$culprit"
		bad+=("$culprit")
		# Re-arm every other pending file and retest the whole set.
		newPending=()
		for f in "${pending[@]}"; do
			[ "$f" = "$culprit" ] || newPending+=("$f")
		done
		pending=("${newPending[@]:-}")
		[ "${#pending[@]}" -gt 0 ] && reconvert_list "${pending[@]}"
	done
	good=("${pending[@]:-}")

	printf '\nverifying final state ... '
	if verify "$logdir/final.log"; then printf 'ok\n'; status=0; else printf 'FAIL\n'; status=1; fi
	kept=0
	for f in "${good[@]:-}"; do [ -n "$f" ] && kept=$((kept + 1)); done
	printf 'kept %d files, reverted %d, suite runs %d\n' "$kept" "${#bad[@]}" "$runs"
	printf 'logs: %s\n' "$logdir"
	exit "$status"
fi

kept=0; reverted=0; converted=0
for f in "${files[@]}"; do
	backup="$logdir/$(printf '%s' "$f" | tr / _).bak"
	cp "$f" "$backup"
	n="$(convert "$f")"
	if [ "${n:-0}" -eq 0 ]; then
		rm -f "$backup"
		continue
	fi
	if verify "$logdir/$(printf '%s' "$f" | tr / _).log"; then
		printf 'KEEP    %-58s +%s\n' "$f" "$n"
		kept=$((kept + 1)); converted=$((converted + n))
		rm -f "$backup"
	else
		cp "$backup" "$f"
		printf 'REVERT  %-58s +%s (suite failed)\n' "$f" "$n"
		reverted=$((reverted + 1))
	fi
done

printf '\nverifying final state ... '
if verify "$logdir/final.log"; then
	printf 'ok\n'
	status=0
else
	printf 'FAIL\n'
	status=1
fi

printf 'kept %d files (%d tests parallelized), reverted %d\n' "$kept" "$converted" "$reverted"
printf 'logs: %s\n' "$logdir"
exit "$status"
