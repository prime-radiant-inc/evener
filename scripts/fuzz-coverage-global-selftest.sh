#!/usr/bin/env bash
# fuzz-coverage-global-selftest.sh exercises the replay runner without compiling
# Serf. A registry-check seam emits a tiny validated plan and a fake Go command
# records every invocation while writing synthetic package-local profiles.
set -euo pipefail

runner="$(cd "$(dirname "$0")" && pwd)/fuzz-coverage-global.sh"
makefile="$(cd "$(dirname "$runner")/.." && pwd)/Makefile"
work="$(mktemp -d -t fuzzcov-global-selftest.XXXXXX)"
# Canonicalize: the runner resolves its repo root with pwd -P, so the fixture's
# spelling must be physical too or the fake go's exact-$PWD dispatch never
# matches (macOS $TMPDIR lives behind the /var -> /private/var symlink).
work="$(cd "$work" && pwd -P)"
trap 'rm -rf "$work"' EXIT
checks=0
fails=0

ok() {
	checks=$((checks + 1))
	printf 'ok   - %s\n' "$1"
}

bad() {
	checks=$((checks + 1))
	fails=$((fails + 1))
	printf 'FAIL - %s\n' "$1"
}

has() {
	local haystack="$1" needle="$2" label="$3"
	if printf '%s' "$haystack" | grep -Fq -- "$needle"; then
		ok "$label"
	else
		bad "$label (missing: $needle)"
	fi
}

lacks() {
	local haystack="$1" needle="$2" label="$3"
	if printf '%s' "$haystack" | grep -Fq -- "$needle"; then
		bad "$label (unexpected: $needle)"
	else
		ok "$label"
	fi
}

count_has() {
	local haystack="$1" needle="$2" expected="$3" label="$4" count
	count="$(printf '%s' "$haystack" | grep -F -c -- "$needle" || true)"
	if [ "$count" -eq "$expected" ]; then
		ok "$label"
	else
		bad "$label (want $expected, got $count for $needle)"
	fi
}

assert_controlled_go_env() {
	local log_file="$1" label="$2"
	if awk -F '\t' '
		NF {
			seen = 1
			if ($3 != "goenv=off" || $4 != "goflags=") {
				bad = 1
			}
		}
		END { exit !seen || bad }
	' "$log_file"; then
		ok "$label"
	else
		bad "$label (unexpected Go env: $(awk -F '\t' '$3 != "goenv=off" || $4 != "goflags=" { print $3 "," $4 }' "$log_file" | tr '\n' ';'))"
	fi
}

assert_uncached_make_native_replays() {
	local log_file="$1" label="$2"
	if awk -F '\t' '
		$15 ~ /^test -run \^Fuzz -tags serffuzz / {
			seen = 1
			if ($3 != "goenv=off" || $4 != "goflags=" || $15 !~ /(^| )-count=1( |$)/) {
				bad = 1
			}
		}
		END { exit !seen || bad }
	' "$log_file"; then
		ok "$label"
	else
		bad "$label (native replay missing isolated environment or -count=1)"
	fi
}

# rapid_replays_opt_in_to_fuzz_tests reports (exit status only, no ok/bad side
# effect) whether SERF_FUZZ_TESTS=1 propagated to every Rapid ($15 ends in
# -run ^TestRapid$) go-test invocation ($16, appended after the command field
# so this never shifts existing column-indexed checks). Without it, the
# seqfuzz/schemafuzz gate this selftest is guarding t.Skip()s and the replay
# silently accepts a zero-count profile as coverage (Roborev 4819). Split from
# assert_rapid_replays_opt_in_to_fuzz_tests below so the regression-proof
# scenario can check the outcome without the assertion's own ok/bad polluting
# the tally with an intentionally-failing probe.
rapid_replays_opt_in_to_fuzz_tests() {
	local log_file="$1"
	awk -F '\t' '
		$15 ~ /-run \^TestRapid\$/ {
			seen = 1
			if ($16 != "serf_fuzz_tests=1") {
				bad = 1
			}
		}
		END { exit !seen || bad }
	' "$log_file"
}

assert_rapid_replays_opt_in_to_fuzz_tests() {
	local log_file="$1" label="$2"
	if rapid_replays_opt_in_to_fuzz_tests "$log_file"; then
		ok "$label"
	else
		bad "$label (a Rapid replay ran without SERF_FUZZ_TESTS=1: $(awk -F '\t' '$15 ~ /-run \^TestRapid\$/ { print $16 }' "$log_file" | tr '\n' ';'))"
	fi
}

repo="$work/repo"
mkdir -p "$repo/scripts"
cp "$runner" "$repo/scripts/fuzz-coverage-global.sh"
for module in added agent auth envvars fuzz invariant llm; do
	mkdir -p "$repo/$module"
done
# The Makefile's FUZZ_SEED_REPLAY cds into every GO_MODULES entry; identifier
# only needs to exist for that cd (its go commands hit the fake go). It stays
# out of the fixture go.work so discovery scenarios keep their exact module set.
mkdir -p "$repo/identifier"
mkdir -p "$repo/other" "$repo/missing-one" "$repo/missing-two"
ln -s agent "$repo/alias"
printf 'go 1.25.6\n\nuse (\n\t.\n\t./agent\n\t./auth\n\t./envvars\n\t./fuzz\n\t./invariant\n\t./llm\n\t./added\n)\n' >"$repo/go.work"
for module in . added agent auth envvars fuzz invariant llm; do
	printf 'module example.test/%s\n\ngo 1.25.6\n' "${module#.}" >"$repo/$module/go.mod"
done
: >"$repo/scripts/fuzzcov-global-exclusions.txt"
printf '# test floor file\n' >"$repo/scripts/fuzzcov-global-floors.txt"

# The cap seam stays transparent while preserving the production wrapper call.
cap="$work/cap.sh"
printf '#!/usr/bin/env bash\nexec "$@"\n' >"$cap"
chmod +x "$cap"

registry="$work/registry-check.sh"
cat >"$registry" <<'REGISTRY'
#!/usr/bin/env bash
set -euo pipefail
env_value() {
	local name="$1"
	# [ -n "${!name+x}" ], not [[ -v ]]: -v is bash 4.2+ and this stub runs
	# under the stock macOS bash (3.2).
	if [ -n "${!name+x}" ]; then
		printf '%s' "${!name}"
	else
		printf '<unset>'
	fi
}
printf 'registry\t%s\tgoenv=%s\tgoflags=%s\n' "${GOWORK:-}" "$(env_value GOENV)" "$(env_value GOFLAGS)" >>"$FAKE_REGISTRY_LOG"
if [ "${FAKE_REGISTRY_FAIL:-}" = "1" ]; then
	echo "synthetic registry drift" >&2
	exit 17
fi
if [ "${FAKE_REGISTRY_MALFORMED:-}" = "1" ]; then
	printf 'native\t.\t.\n'
	exit 0
fi
printf 'native\t.\t.\tFuzzNative\n'
printf 'native\t.\t./other\tFuzzOther\n'
printf 'rapid\t.\t.\tTestRapid\n'
if [ "${FAKE_REGISTRY_ADDED:-}" = "1" ]; then
	printf 'native\tadded\t.\tFuzzAdded\n'
fi
if [ "${FAKE_REGISTRY_ALIAS:-}" = "1" ]; then
	printf 'native\talias\t.\tFuzzAlias\n'
fi
REGISTRY
chmod +x "$registry"

gobin="$work/go.sh"
cat >"$gobin" <<'GOBIN'
#!/usr/bin/env bash
set -euo pipefail
env_value() {
	local name="$1"
	# [ -n "${!name+x}" ], not [[ -v ]]: -v is bash 4.2+ and this stub runs
	# under the stock macOS bash (3.2).
	if [ -n "${!name+x}" ]; then
		printf '%s' "${!name}"
	else
		printf '<unset>'
	fi
}
# serf_fuzz_tests is appended AFTER the command field ($15) rather than
# inserted before it, so every existing column-indexed assertion below ($3,
# $4, $15) keeps its field number.
printf '%s\t%s\tgoenv=%s\tgoflags=%s\tseed=%s\tchecks=%s\tsteps=%s\tfailfile=%s\tnofailfile=%s\tlog=%s\tv=%s\tdebug=%s\tdebugvis=%s\tshrinktime=%s\t%s\tserf_fuzz_tests=%s\n' \
	"$PWD" "${GOWORK:-}" "$(env_value GOENV)" "$(env_value GOFLAGS)" "$(env_value RAPID_SEED)" "$(env_value RAPID_CHECKS)" "$(env_value RAPID_STEPS)" "$(env_value RAPID_FAILFILE)" "$(env_value RAPID_NOFAILFILE)" "$(env_value RAPID_LOG)" "$(env_value RAPID_V)" "$(env_value RAPID_DEBUG)" "$(env_value RAPID_DEBUGVIS)" "$(env_value RAPID_SHRINKTIME)" "$*" "$(env_value SERF_FUZZ_TESTS)" >>"$FAKE_GO_LOG"
command="$1"
shift
case "$command" in
	env)
		# run-fuzz.sh asks for GOOS to decide whether Linux-only targets join the
		# registry; a fixed darwin answer keeps the fixture's target set identical
		# on every host.
		if [ "$#" -eq 1 ] && [ "$1" = GOARCH ]; then
			printf '%s\n' "${FAKE_GOARCH:-amd64}"
		elif [ "$#" -eq 1 ] && [ "$1" = GOOS ]; then
			printf '%s\n' "${FAKE_GOOS:-darwin}"
		else
			echo "fake go: unexpected env command" >&2; exit 25
		fi
		;;
	work)
		[ "$1" = edit ] && [ "$2" = -json ] || { echo "fake go: unexpected work command" >&2; exit 25; }
		if [ "${FAKE_WORKSPACE_OUTSIDE:-}" = "1" ]; then
			cat <<'JSON'
{
  "Use": [
    {"DiskPath":"."},
    {"DiskPath":"../outside"}
  ]
}
JSON
		elif [ "${FAKE_WORKSPACE_DUPLICATE:-}" = "1" ]; then
			cat <<'JSON'
{
  "Use": [
    {"DiskPath":"."},
    {"DiskPath":"./agent"},
    {"DiskPath":"./alias"}
  ]
}
JSON
		elif [ "${FAKE_WORKSPACE_ALIAS:-}" = "1" ]; then
			cat <<'JSON'
{
  "Use": [
    {"DiskPath":"."},
    {"DiskPath":"./alias"},
    {"DiskPath":"./auth"},
    {"DiskPath":"./envvars"},
    {"DiskPath":"./fuzz"},
    {"DiskPath":"./invariant"},
    {"DiskPath":"./llm"},
    {"DiskPath":"./added"}
  ]
}
JSON
		else
			cat <<'JSON'
{
  "Use": [
    {"DiskPath":"."},
    {"DiskPath":"./agent"},
    {"DiskPath":"./auth"},
    {"DiskPath":"./envvars"},
    {"DiskPath":"./fuzz"},
    {"DiskPath":"./invariant"},
    {"DiskPath":"./llm"},
    {"DiskPath":"./added"}
  ]
}
JSON
		fi
		;;
	list)
		if [ "${FAKE_GO_LIST_FAIL:-}" = "1" ]; then
			echo "synthetic go list failure" >&2
			exit 18
		fi
		# Root has two packages; the workspace modules list successfully but have no
		# packages, except added, proving the runner asks every go.work module and keeps profiles
		# separate within a module.
		if [ "$PWD" = "$FAKE_REPO" ]; then
			printf '%s\n' "$PWD"
			printf '%s/other\n' "$PWD"
			if [ "${FAKE_LIST_MISSING:-}" = "1" ]; then
				printf '%s/missing-one\n' "$PWD"
				printf '%s/missing-two\n' "$PWD"
			fi
		elif [ "$PWD" = "$FAKE_REPO/added" ] && [ "${FAKE_REGISTRY_ADDED:-}" = "1" ]; then
			printf '%s\n' "$PWD"
		elif [ "$PWD" = "$FAKE_REPO/alias" ] && [ "${FAKE_WORKSPACE_ALIAS:-}" = "1" ]; then
			# Go reports physical package directories even when the workspace uses a
			# logical symlink spelling.
			printf '%s\n' "$FAKE_REPO/agent"
		fi
		;;
	test)
		run=""
		profile=""
		for arg in "$@"; do
			case "$arg" in
				-run=*) run="${arg#-run=}" ;;
				-coverprofile=*) profile="${arg#-coverprofile=}" ;;
			esac
		done
		if [ -z "$run" ]; then
			for ((i = 1; i <= $#; i++)); do
				if [ "${!i}" = "-run" ]; then
					j=$((i + 1))
					run="${!j}"
				fi
			done
		fi
		# make fuzz replays ordinary deterministic tests without coverage profiles.
		[ -n "$profile" ] || exit 0
		if [ "${FAKE_GO_TEST_FAIL:-}" = "$run" ]; then
			echo "synthetic replay failure" >&2
			exit 20
		fi
		if [ "${FAKE_GO_NO_PROFILE:-}" = "$run" ]; then
			exit 0
		fi
		if [ "${FAKE_GO_BAD_PROFILE:-}" = "$run" ]; then
			echo 'mode: count' >"$profile"
			exit 0
		fi
		{
			echo 'mode: set'
			if [ "$run" = '^FuzzNative$' ]; then
				echo 'example.test/root.go:1.1,2.1 1 1'
				if [ "${FAKE_GO_EXACT_COUNT_UNION:-}" = "1" ]; then
					echo 'example.test/large-count.go:5.1,6.1 1 9007199254740992'
				fi
			elif [ "$run" = '^FuzzOther$' ]; then
				echo 'example.test/other.go:5.1,6.1 3 1'
			elif [ "$run" = '^FuzzAdded$' ]; then
				echo 'example.test/added.go:7.1,8.1 2 1'
			elif [ "$run" = '^FuzzAlias$' ]; then
				echo 'example.test/alias.go:9.1,10.1 2 1'
			else
				# Merge must retain this native-positive count and add the Rapid block.
				echo 'example.test/root.go:1.1,2.1 1 0'
				# Mirrors the real seqfuzz gate: without SERF_FUZZ_TESTS=1 the rapid
				# body t.Skip()s before rapid.Check ever runs, so its statements
				# execute zero times — a real `go test -coverprofile` still writes a
				# profile for a skipped test, just with every count at 0. This is the
				# exact shape of the bug Roborev 4819 found: a skip accepted as a
				# passing, zero-count profile.
				if [ "${SERF_FUZZ_TESTS:-}" = "1" ]; then
					echo 'example.test/rapid.go:3.1,4.1 2 1'
				else
					echo 'example.test/rapid.go:3.1,4.1 2 0'
				fi
				if [ "${FAKE_GO_EXACT_COUNT_UNION:-}" = "1" ]; then
					echo 'example.test/large-count.go:5.1,6.1 1 9007199254740993'
				fi
			fi
			if [ "${FAKE_GO_ZERO_STATEMENT_BLOCK:-}" = "1" ]; then
				echo 'example.test/zero.go:94.11,94.11 0 0'
				echo 'example.test/leading-zero.go:00094.00011,00094.00011 0 0'
				echo 'example.test/max-coordinate.go:9223372036854775807.1,9223372036854775807.1 0 0'
			fi
			if [ "${FAKE_GO_MALFORMED_BLOCK:-}" = "1" ]; then
				echo 'example.test/malformed.go:94.11,94.11 0 not-a-count'
			fi
			if [ "${FAKE_GO_MALFORMED_ZERO_LOCATION:-}" = "1" ]; then
				echo 'not-a-go-cover-block 0 0'
			fi
			if [ "${FAKE_GO_ZERO_POSITION:-}" = "1" ]; then
				echo 'example.test/zero-position.go:0.1,1.1 0 0'
			fi
			if [ "${FAKE_GO_BACKWARD_ZERO_RANGE:-}" = "1" ]; then
				echo 'example.test/backward-range.go:94.11,94.10 0 0'
			fi
			if [ "${FAKE_GO_PRECISION_BACKWARD_ZERO_RANGE:-}" = "1" ]; then
				echo 'example.test/precision-range.go:9007199254740993.1,9007199254740992.1 0 0'
			fi
			if [ "${FAKE_GO_OVERSIZED_ZERO_COORDINATE:-}" = "1" ]; then
				echo 'example.test/oversized-coordinate.go:9223372036854775808.1,9223372036854775808.1 0 0'
			fi
			if [ "${FAKE_GO_32BIT_OVERSIZED_ZERO_COORDINATE:-}" = "1" ]; then
				echo 'example.test/oversized-32bit-coordinate.go:2147483648.1,2147483648.1 0 0'
			fi
			if [ "${FAKE_GO_OVERSIZED_ZERO_COUNT:-}" = "1" ]; then
				echo 'example.test/oversized-zero-count.go:1.1,1.1 0 9223372036854775808'
			fi
			if [ "${FAKE_GO_32BIT_OVERSIZED_ZERO_COUNT:-}" = "1" ]; then
				echo 'example.test/oversized-32bit-zero-count.go:1.1,1.1 0 2147483648'
			fi
			if [ "${FAKE_GO_OVERSIZED_STATEMENT_COUNT:-}" = "1" ]; then
				echo 'example.test/oversized-statement-count.go:1.1,1.1 9223372036854775808 0'
			fi
		} >"$profile"
		;;
	run)
		[ "$1" = './cmd/serf-fuzzcov' ] || { echo "fake go: unexpected run target $1" >&2; exit 21; }
		if [ "${FAKE_GO_RUN_FAIL:-}" = "1" ]; then
			echo "synthetic global accounting failure" >&2
			exit 24
		fi
		if [ "${FAKE_GO_RUN_GATE_FAIL:-}" = "1" ]; then
			for arg in "$@"; do
				if [ "$arg" = -global-json ]; then
					printf '{"modules":[{"module":"fake-gate","covered":94,"total":100,"percent":94,"pass":false,"packages":[{"module":"fake-gate","package":".","covered":94,"total":100,"percent":94}]}],"raw_pass":false,"minimum":95,"applied_exclusions":null}\n'
					break
				fi
			done
			echo "serf-fuzzcov: RAW THRESHOLD BREACH: synthetic coverage below 95%" >&2
			exit 1
		fi
		manifest=""
		for ((i = 1; i <= $#; i++)); do
			if [ "${!i}" = '-global-manifest' ]; then
				j=$((i + 1))
				manifest="${!j}"
			fi
		done
		[ -n "$manifest" ] || { echo "fake go: no global manifest" >&2; exit 22; }
		cp "$manifest" "$FAKE_COV_MANIFEST"
		while IFS=$'\t' read -r module pkg profile; do
			case "$pkg" in
				.) cp "$profile" "$FAKE_COV_PROFILE" ;;
				./other) cp "$profile" "$FAKE_COV_OTHER_PROFILE" ;;
			esac
		done <"$manifest"
		for arg in "$@"; do
			if [ "$arg" = -global-json ]; then
				printf '{"modules":[{"module":"fake","covered":100,"total":100,"percent":100,"pass":true,"packages":[{"module":"fake","package":".","covered":100,"total":100,"percent":100}]}],"raw_pass":true,"minimum":95,"applied_exclusions":null}\n'
				break
			fi
		done
		;;
	*)
		echo "fake go: unexpected command $command" >&2
		exit 23
		;;
esac
GOBIN
chmod +x "$gobin"

# Exercise the Make rapid replay with the same fake Go boundary. Its ordinary
# native/test commands intentionally have no coverprofile, which the fake above
# accepts without producing a profile.
cp "$makefile" "$repo/Makefile"
cp "$cap" "$repo/scripts/run-capped.sh"
cat >"$repo/scripts/run-fuzz.sh" <<'RUN_FUZZ'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = --list ] || exit 26
printf 'rapid:.:.:TestRapid\n'
RUN_FUZZ
chmod +x "$repo/scripts/run-capped.sh" "$repo/scripts/run-fuzz.sh"
fake_bin="$work/bin"
mkdir -p "$fake_bin"
ln -s "$gobin" "$fake_bin/go"

go_log="$work/go.log"
registry_log="$work/registry.log"
cov_manifest="$work/cov-manifest.tsv"
cov_profile="$work/cov-profile.cov"
cov_other_profile="$work/cov-other-profile.cov"

reset_logs() {
	: >"$go_log"
	: >"$registry_log"
	rm -f "$cov_manifest" "$cov_profile" "$cov_other_profile"
}

run_runner() {
	env FAKE_REPO="$repo" FAKE_GO_LOG="$go_log" FAKE_REGISTRY_LOG="$registry_log" FAKE_COV_MANIFEST="$cov_manifest" FAKE_COV_PROFILE="$cov_profile" FAKE_COV_OTHER_PROFILE="$cov_other_profile" SERF_FUZZ_GO="$gobin" SERF_FUZZ_CAPPED="$cap" SERF_FUZZ_REGISTRY_CHECK="$registry" "$@" bash "$repo/scripts/fuzz-coverage-global.sh" --check --bless
}

run_runner_json() {
	env FAKE_REPO="$repo" FAKE_GO_LOG="$go_log" FAKE_REGISTRY_LOG="$registry_log" FAKE_COV_MANIFEST="$cov_manifest" FAKE_COV_PROFILE="$cov_profile" FAKE_COV_OTHER_PROFILE="$cov_other_profile" SERF_FUZZ_GO="$gobin" SERF_FUZZ_CAPPED="$cap" SERF_FUZZ_REGISTRY_CHECK="$registry" "$@" bash "$repo/scripts/fuzz-coverage-global.sh" --check --bless --format json
}

run_runner_modules() {
	local modules="$1"
	shift
	env FAKE_REPO="$repo" FAKE_GO_LOG="$go_log" FAKE_REGISTRY_LOG="$registry_log" FAKE_COV_MANIFEST="$cov_manifest" FAKE_COV_PROFILE="$cov_profile" FAKE_COV_OTHER_PROFILE="$cov_other_profile" SERF_FUZZ_GO="$gobin" SERF_FUZZ_CAPPED="$cap" SERF_FUZZ_REGISTRY_CHECK="$registry" "$@" bash "$repo/scripts/fuzz-coverage-global.sh" --check --bless --modules "$modules"
}

expect_failure() {
	set +e
	last_output="$(run_runner "$@" 2>&1)"
	last_status=$?
	set -e
	if [ "$last_status" -ne 0 ]; then
		ok "runner fails for ${1:-synthetic failure}"
	else
		bad "runner should fail for ${1:-synthetic failure}"
	fi
}

echo '== exact all-module replay and merge =='
reset_logs
out="$(run_runner GOENV="$work/ambient-goenv" GOFLAGS='-coverpkg=./... -shuffle=on -mod=mod' RAPID_STEPS=999 RAPID_FAILFILE="$work/ambient-failfile" RAPID_NOFAILFILE=false RAPID_LOG=true RAPID_V=true RAPID_DEBUG=true RAPID_DEBUGVIS=true RAPID_SHRINKTIME=1h 2>&1)"
has "$out" 'account package-local profiles' 'successful replay reaches global accounting'
has "$(cat "$registry_log")" $'registry\t'"$repo/go.work"$'\tgoenv=off\tgoflags=' 'registry checker inherits controlled Go workspace environment'

log="$(cat "$go_log")"
assert_controlled_go_env "$go_log" 'all runner Go commands ignore ambient GOENV and GOFLAGS'
for module in . added agent auth envvars fuzz invariant llm; do
	if [ "$module" = . ]; then
		module_dir="$repo"
	else
		module_dir="$repo/$module"
	fi
	has "$log" "$module_dir"$'\t'"$repo/go.work"$'\tgoenv=off\tgoflags=\tseed=<unset>\tchecks=<unset>\t' "go list -tags serffuzz covers module $module"
done
has "$log" $'\ttest -tags serffuzz -run ^FuzzNative$ -count=1 -coverprofile=' 'native replay has exact deterministic flags'
count_has "$log" $'\ttest -tags serffuzz -run ^FuzzNative$ -count=1 -coverprofile=' 1 'native target replays once'
count_has "$log" $'\ttest -tags serffuzz -run ^FuzzOther$ -count=1 -coverprofile=' 1 'other package native target replays once'
count_has "$log" $'\ttest -tags serffuzz -run ^TestRapid$ -count=1 -coverprofile=' 5 'rapid target replays fixed seed bank exactly once each'
for seed in 1 2 3 5 8; do
	has "$log" $'\tseed='"$seed"$'\tchecks=100\tsteps=30\tfailfile=<unset>\tnofailfile=true\tlog=false\tv=false\tdebug=false\tdebugvis=false\tshrinktime=30s\ttest -tags serffuzz -run ^TestRapid$ -count=1 -coverprofile=' "rapid seed $seed pins every behavior control"
done
assert_rapid_replays_opt_in_to_fuzz_tests "$go_log" 'every Rapid replay sets SERF_FUZZ_TESTS=1 (else the seqfuzz/schemafuzz gate skips it)'
lacks "$log" '-coverpkg' 'replay never uses coverpkg'
lacks "$log" '-shuffle=on' 'replay ignores ambient shuffle flags'
lacks "$log" '-mod=mod' 'replay ignores ambient module flags'
has "$log" $'\trun ./cmd/serf-fuzzcov -global-manifest ' 'global accounting runs serf-fuzzcov'
has "$log" "-repo-root $repo -global-exclusions $repo/scripts/fuzzcov-global-exclusions.txt -global-floors $repo/scripts/fuzzcov-global-floors.txt -global-minimum 95 -check -bless" 'global accounting receives root, policy files, minimum, check, and bless'

manifest="$(cat "$cov_manifest")"
count_has "$manifest" $'.\t.\t' 1 'global manifest has one merged profile for the package'
count_has "$manifest" $'.\t./other\t' 1 'global manifest has a separate profile for the other package'
merged="$(cat "$cov_profile")"
has "$merged" 'mode: set' 'merged profile preserves set mode'
has "$merged" 'example.test/root.go:1.1,2.1 1 1' 'merge retains coverage from native replay'
# The fake go emits this exact block ONLY when it observes SERF_FUZZ_TESTS=1
# (mirroring the real gate's t.Skip()); the sibling zero-count assertion right
# below is the fake's "skipped" shape, so this pair is a live tripwire for the
# opt-in actually propagating, not just its presence in the source.
has "$merged" 'example.test/rapid.go:3.1,4.1 2 1' 'Rapid replay actually RAN (nonzero execution count), proving SERF_FUZZ_TESTS=1 propagated'
lacks "$merged" 'example.test/rapid.go:3.1,4.1 2 0' 'Rapid replay did not fall back to the skipped/zero-count shape'
other_merged="$(cat "$cov_other_profile")"
has "$other_merged" 'example.test/other.go:5.1,6.1 3 1' 'other package profile keeps its own blocks'
lacks "$other_merged" 'example.test/root.go' 'profiles never merge blocks across packages'

echo '== regression proof: dropping the opt-in reproduces the exact Roborev 4819 bug =='
# Mutation-tests the two assertions above: strip SERF_FUZZ_TESTS=1 from the
# runner (simulating the bug this round fixes) and confirm the merged profile
# reverts to the skipped/zero-count shape instead of silently staying green.
mutated_runner="$work/fuzz-coverage-global.no-optin.sh"
sed 's/env -u RAPID_FAILFILE SERF_FUZZ_TESTS=1 /env -u RAPID_FAILFILE /' "$runner" >"$mutated_runner"
if ! diff -q "$runner" "$mutated_runner" >/dev/null 2>&1; then
	ok 'mutation fixture actually strips SERF_FUZZ_TESTS=1 from the runner source'
else
	bad 'mutation fixture failed to strip the opt-in — sed pattern no longer matches the runner source'
fi
cp "$mutated_runner" "$repo/scripts/fuzz-coverage-global.sh"
reset_logs
set +e
last_output="$(run_runner 2>&1)"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok 'mutated (no-opt-in) runner still exits zero — it accepts the skip without complaint, which is exactly the bug'
else
	bad "mutated runner unexpectedly failed, so it cannot demonstrate the silent-acceptance bug: $last_output"
fi
if [ -s "$cov_profile" ]; then
	mutated_merged="$(cat "$cov_profile")"
	has "$mutated_merged" 'example.test/rapid.go:3.1,4.1 2 0' 'REGRESSION PROOF: without the opt-in, Rapid coverage reverts to the skipped/zero-count block'
	lacks "$mutated_merged" 'example.test/rapid.go:3.1,4.1 2 1' 'REGRESSION PROOF: without the opt-in, the nonzero Rapid block never appears'
else
	bad 'mutated runner produced no merged profile to inspect for the regression proof'
fi
if ! rapid_replays_opt_in_to_fuzz_tests "$go_log"; then
	ok 'REGRESSION PROOF: the propagation check itself fails against the mutated (no-opt-in) runner'
else
	bad 'the propagation check should fail against the mutated (no-opt-in) runner but did not'
fi
cp "$runner" "$repo/scripts/fuzz-coverage-global.sh" # restore the real, fixed runner for every scenario below

reset_logs
set +e
last_output="$(run_runner FAKE_GO_EXACT_COUNT_UNION=1 2>&1)"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok 'runner merges large execution counts exactly'
else
	bad "runner should merge large execution counts exactly ($last_output)"
fi
if [ -s "$cov_profile" ]; then
	has "$(cat "$cov_profile")" 'example.test/large-count.go:5.1,6.1 1 9007199254740993' 'merge keeps the exact larger execution count'
else
	bad 'large-count replay produces a merged root profile'
fi

echo '== zero-statement coverage blocks =='
reset_logs
set +e
last_output="$(run_runner FAKE_GO_ZERO_STATEMENT_BLOCK=1 2>&1)"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok 'runner accepts Go zero-statement coverage blocks'
else
	bad "runner should accept Go zero-statement coverage blocks ($last_output)"
fi
if [ -s "$cov_profile" ]; then
	zero_merged="$(cat "$cov_profile")"
	lacks "$zero_merged" 'example.test/zero.go:94.11,94.11 0 0' 'merged profile omits zero-statement blocks'
	lacks "$zero_merged" 'example.test/leading-zero.go:00094.00011,00094.00011 0 0' 'leading-zero coordinates remain valid zero-statement blocks'
	lacks "$zero_merged" 'example.test/max-coordinate.go:9223372036854775807.1,9223372036854775807.1 0 0' 'max-int coordinates remain valid zero-statement blocks'
	has "$zero_merged" 'example.test/root.go:1.1,2.1 1 1' 'zero-statement filtering keeps native coverage blocks'
	has "$zero_merged" 'example.test/rapid.go:3.1,4.1 2 1' 'zero-statement filtering keeps Rapid coverage blocks'
else
	bad 'zero-statement replay produces a merged root profile'
fi
if [ -s "$cov_other_profile" ]; then
	lacks "$(cat "$cov_other_profile")" 'example.test/zero.go:94.11,94.11 0 0' 'every package-local merged profile omits zero-statement blocks'
else
	bad 'zero-statement replay produces a merged other-package profile'
fi

reset_logs
expect_failure FAKE_GO_MALFORMED_BLOCK=1
has "$last_output" 'invalid coverage block' 'malformed coverage blocks remain fatal'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'malformed coverage blocks skip global accounting'

reset_logs
expect_failure FAKE_GO_MALFORMED_ZERO_LOCATION=1
has "$last_output" 'invalid coverage block' 'malformed zero-statement locations remain fatal'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'malformed zero-statement locations skip global accounting'

reset_logs
expect_failure FAKE_GO_ZERO_POSITION=1
has "$last_output" 'invalid coverage block' 'zero-statement locations require positive positions'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'zero-position blocks skip global accounting'

reset_logs
expect_failure FAKE_GO_BACKWARD_ZERO_RANGE=1
has "$last_output" 'invalid coverage block' 'zero-statement locations reject backward ranges'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'backward-range blocks skip global accounting'

reset_logs
expect_failure FAKE_GO_PRECISION_BACKWARD_ZERO_RANGE=1
has "$last_output" 'invalid coverage block' 'zero-statement locations reject precision-boundary backward ranges'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'precision-boundary blocks skip global accounting'

reset_logs
expect_failure FAKE_GO_OVERSIZED_ZERO_COORDINATE=1
has "$last_output" 'invalid coverage block' 'zero-statement locations reject oversized coordinates'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'oversized-coordinate blocks skip global accounting'

reset_logs
expect_failure FAKE_GOARCH=386 FAKE_GO_32BIT_OVERSIZED_ZERO_COORDINATE=1
has "$last_output" 'invalid coverage block' 'zero-statement locations respect 32-bit Go coordinate limits'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' '32-bit oversized-coordinate blocks skip global accounting'

reset_logs
expect_failure FAKE_GO_OVERSIZED_ZERO_COUNT=1
has "$last_output" 'invalid coverage block' 'zero-statement rows reject oversized 64-bit execution counts'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'oversized 64-bit zero-row counts skip global accounting'

reset_logs
expect_failure FAKE_GOARCH=386 FAKE_GO_32BIT_OVERSIZED_ZERO_COUNT=1
has "$last_output" 'invalid coverage block' 'zero-statement rows reject oversized 32-bit execution counts'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'oversized 32-bit zero-row counts skip global accounting'

reset_logs
expect_failure FAKE_GO_OVERSIZED_STATEMENT_COUNT=1
has "$last_output" 'invalid coverage block' 'nonzero rows reject oversized statement counts'
lacks "$(cat "$go_log")" $'\trun ./cmd/serf-fuzzcov' 'oversized statement counts skip global accounting'

echo '== workspace discovery and module selection =='
reset_logs
set +e
last_output="$(run_runner FAKE_REGISTRY_ADDED=1 2>&1)"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok 'runner replays a module discovered from go.work'
else
	bad "runner should replay a module discovered from go.work ($last_output)"
fi
log="$(cat "$go_log")"
has "$log" "$repo/added"$'\t'"$repo/go.work"$'\tgoenv=off\tgoflags=\tseed=<unset>\tchecks=<unset>\tsteps=<unset>\tfailfile=<unset>\tnofailfile=<unset>\tlog=<unset>\tv=<unset>\tdebug=<unset>\tdebugvis=<unset>\tshrinktime=<unset>\tlist -tags serffuzz -f {{.Dir}} ./...' 'derived added module is preflighted'
has "$log" $'\ttest -tags serffuzz -run ^FuzzAdded$ -count=1 -coverprofile=' 'derived added module target is replayed'

reset_logs
set +e
last_output="$(run_runner_modules added FAKE_REGISTRY_ADDED=1 2>&1)"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok '--modules accepts a module derived from go.work'
else
	bad "--modules should accept a module derived from go.work ($last_output)"
fi
log="$(cat "$go_log")"
has "$log" "$repo/added"$'\t'"$repo/go.work"$'\tgoenv=off\tgoflags=\tseed=<unset>\tchecks=<unset>\tsteps=<unset>\tfailfile=<unset>\tnofailfile=<unset>\tlog=<unset>\tv=<unset>\tdebug=<unset>\tdebugvis=<unset>\tshrinktime=<unset>\tlist -tags serffuzz -f {{.Dir}} ./...' '--modules lists the derived module'
lacks "$log" "$repo/agent"$'\t'"$repo/go.work"$'\t' '--modules skips unselected workspace modules'

reset_logs
set +e
last_output="$(run_runner_modules missing-module 2>&1)"
last_status=$?
set -e
if [ "$last_status" -ne 0 ]; then
	ok '--modules rejects a label outside the derived workspace set'
else
	bad '--modules should reject a label outside the derived workspace set'
fi
has "$last_output" 'unknown go.work module: missing-module' '--modules reports the unknown derived label exactly'
lacks "$(cat "$go_log")" $'\tlist ' 'unknown --modules selection stops before preflight'

echo '== symlink workspace labels remain logical =='
reset_logs
set +e
last_output="$(run_runner FAKE_WORKSPACE_ALIAS=1 FAKE_REGISTRY_ALIAS=1 2>&1)"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok 'runner accepts a logical symlink module label'
else
	bad "runner should accept a logical symlink module label ($last_output)"
fi
has "$last_output" 'replay native:alias:.:FuzzAlias' 'replay keeps the alias label from go.work'
log="$(cat "$go_log")"
has "$log" "$repo/alias"$'\t'"$repo/go.work"$'\tgoenv=off\tgoflags=\tseed=<unset>\tchecks=<unset>\tsteps=<unset>\tfailfile=<unset>\tnofailfile=<unset>\tlog=<unset>\tv=<unset>\tdebug=<unset>\tdebugvis=<unset>\tshrinktime=<unset>\tlist -tags serffuzz -f {{.Dir}} ./...' 'alias workspace path is preflighted'
has "$log" $'\ttest -tags serffuzz -run ^FuzzAlias$ -count=1 -coverprofile=' 'alias plan target replays after physical package preflight'
has "$(cat "$cov_manifest")" $'alias\t.\t' 'global manifest preserves the alias module label'

reset_logs
set +e
last_output="$(run_runner FAKE_WORKSPACE_OUTSIDE=1 2>&1)"
last_status=$?
set -e
if [ "$last_status" -ne 0 ]; then
	ok 'runner rejects a lexical workspace path outside the repository'
else
	bad 'runner should reject a lexical workspace path outside the repository'
fi
has "$last_output" 'go.work module is outside repository root: ../outside' 'outside workspace path is reported exactly'

reset_logs
set +e
last_output="$(run_runner FAKE_WORKSPACE_DUPLICATE=1 2>&1)"
last_status=$?
set -e
if [ "$last_status" -ne 0 ]; then
	ok 'runner rejects two labels for one resolved module directory'
else
	bad 'runner should reject two labels for one resolved module directory'
fi
has "$last_output" 'go.work lists duplicate module directory: ./alias' 'duplicate resolved workspace directory is reported exactly'

echo '== JSON output is machine-readable =='
json_check="$work/json-check.go"
cat >"$json_check" <<'GO'
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

type globalReport struct {
	Modules []struct {
		Module string `json:"module"`
	} `json:"modules"`
	RawPass bool `json:"raw_pass"`
}

func main() {
	if len(os.Args) != 3 {
		panic("usage: json-check <module> <raw-pass>")
	}
	wantRawPass, err := strconv.ParseBool(os.Args[2])
	if err != nil {
		panic(err)
	}
	decoder := json.NewDecoder(os.Stdin)
	var report globalReport
	if err := decoder.Decode(&report); err != nil {
		panic(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		panic("trailing non-JSON output")
	}
	if len(report.Modules) != 1 || report.Modules[0].Module != os.Args[1] || report.RawPass != wantRawPass {
		panic(fmt.Sprintf("unexpected report: %#v", report))
	}
}
GO
reset_logs
json_out="$work/global.json"
json_err="$work/global.err"
set +e
run_runner_json >"$json_out" 2>"$json_err"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok 'JSON runner invocation succeeds'
else
	bad 'JSON runner invocation should succeed'
fi
if go run "$json_check" fake true <"$json_out" >/dev/null 2>&1; then
	ok 'JSON mode emits valid JSON only on stdout'
else
	bad "JSON mode stdout is not valid JSON: $(cat "$json_out")"
fi
lacks "$(cat "$json_out")" 'fuzz-coverage-global:' 'JSON stdout excludes runner progress'
has "$(cat "$json_err")" 'fuzz-coverage-global: validating registered native and Rapid targets' 'JSON runner progress goes to stderr'

echo '== JSON gate breaches preserve the report =='
reset_logs
json_gate_out="$work/global-gate.json"
json_gate_err="$work/global-gate.err"
set +e
run_runner_json FAKE_GO_RUN_GATE_FAIL=1 >"$json_gate_out" 2>"$json_gate_err"
last_status=$?
set -e
if [ "$last_status" -eq 1 ]; then
	ok 'JSON runner preserves a raw/floor gate exit status'
else
	bad "JSON runner gate exit = $last_status, want 1"
fi
if go run "$json_check" fake-gate false <"$json_gate_out" >/dev/null 2>&1; then
	ok 'JSON gate breach emits its complete report on stdout'
else
	bad "JSON gate breach stdout is not a complete report: $(cat "$json_gate_out")"
fi
lacks "$(cat "$json_gate_out")" 'fuzz-coverage-global:' 'JSON gate breach stdout excludes runner progress'
has "$(cat "$json_gate_err")" 'fuzz-coverage-global: validating registered native and Rapid targets' 'JSON gate breach runner progress stays on stderr'
has "$(cat "$json_gate_err")" 'serf-fuzzcov: RAW THRESHOLD BREACH: synthetic coverage below 95%' 'JSON gate breach error stays on stderr'
lacks "$(cat "$json_gate_err")" 'global coverage accounting failed' 'JSON gate breach is not misclassified as accounting failure'

echo '== JSON accounting failures fail closed =='
reset_logs
json_failure_out="$work/global-failure.json"
json_failure_err="$work/global-failure.err"
set +e
run_runner_json FAKE_GO_RUN_FAIL=1 >"$json_failure_out" 2>"$json_failure_err"
last_status=$?
set -e
if [ "$last_status" -ne 0 ]; then
	ok 'JSON runner fails for an accounting command failure'
else
	bad 'JSON runner should fail for an accounting command failure'
fi
if [ ! -s "$json_failure_out" ]; then
	ok 'JSON accounting failure emits no partial stdout report'
else
	bad "JSON accounting failure unexpectedly emitted stdout: $(cat "$json_failure_out")"
fi
has "$(cat "$json_failure_err")" 'global coverage accounting failed' 'JSON accounting failure remains fatal'

echo '== Make rapid replay pins the full Rapid environment =='
reset_logs
set +e
(cd "$repo" && PATH="$fake_bin:$PATH" FAKE_REPO="$repo" FAKE_GO_LOG="$go_log" GOENV="$work/ambient-goenv" GOFLAGS='-coverpkg=./... -shuffle=on -mod=mod' RAPID_STEPS=999 RAPID_FAILFILE="$work/ambient-failfile" RAPID_NOFAILFILE=false RAPID_LOG=true RAPID_V=true RAPID_DEBUG=true RAPID_DEBUGVIS=true RAPID_SHRINKTIME=1h make fuzz) >"$work/make.out" 2>"$work/make.err"
last_status=$?
set -e
if [ "$last_status" -eq 0 ]; then
	ok 'fake make fuzz succeeds'
else
	bad "fake make fuzz should succeed: $(cat "$work/make.err")"
fi
make_test_log="$work/make-go-test.log"
awk -F '\t' '$15 ~ /^test /' "$go_log" >"$make_test_log"
log="$(cat "$make_test_log")"
assert_controlled_go_env "$make_test_log" 'all make fuzz Go test replays ignore ambient GOENV and GOFLAGS'
assert_uncached_make_native_replays "$make_test_log" 'every make fuzz native replay disables cached test results'
lacks "$log" '-coverpkg' 'make fuzz never accepts ambient coverpkg flags'
lacks "$log" '-shuffle=on' 'make fuzz never accepts ambient shuffle flags'
lacks "$log" '-mod=mod' 'make fuzz never accepts ambient module flags'
count_has "$log" $'\ttest -tags serffuzz -run ^TestRapid$ -count=1 .' 5 'make fuzz replays the rapid target for every fixed seed'
for seed in 1 2 3 5 8; do
	has "$log" $'\tseed='"$seed"$'\tchecks=100\tsteps=30\tfailfile=<unset>\tnofailfile=true\tlog=false\tv=false\tdebug=false\tdebugvis=false\tshrinktime=30s\ttest -tags serffuzz -run ^TestRapid$ -count=1 .' "make fuzz rapid seed $seed pins every behavior control"
done
assert_rapid_replays_opt_in_to_fuzz_tests "$make_test_log" 'make fuzz sets SERF_FUZZ_TESTS=1 on every Rapid replay (else the seqfuzz/schemafuzz gate skips it)'

echo '== missing local surface stops before replays =='
reset_logs
expect_failure FAKE_LIST_MISSING=1
has "$last_output" 'missing local fuzz surface: .:./missing-one' 'first missing production package is named exactly'
has "$last_output" 'missing local fuzz surface: .:./missing-two' 'second missing production package is also reported'
log="$(cat "$go_log")"
lacks "$log" $'\ttest ' 'missing-surface preflight runs no tests'
lacks "$log" $'\trun ./cmd/serf-fuzzcov' 'missing-surface preflight skips global accounting'

echo '== failed replay and profile output are fatal =='
reset_logs
expect_failure FAKE_GO_TEST_FAIL='^FuzzNative$'
has "$last_output" 'replay failed: native:.:.:FuzzNative' 'failed native replay is fatal'
log="$(cat "$go_log")"
lacks "$log" $'\trun ./cmd/serf-fuzzcov' 'failed replay skips global accounting'

reset_logs
expect_failure FAKE_GO_NO_PROFILE='^FuzzNative$'
has "$last_output" 'coverage profile missing after replay: native:.:.:FuzzNative' 'missing replay profile is fatal'
log="$(cat "$go_log")"
lacks "$log" $'\trun ./cmd/serf-fuzzcov' 'missing profile skips global accounting'

reset_logs
expect_failure FAKE_GO_BAD_PROFILE='^FuzzNative$'
has "$last_output" 'coverage profile has invalid mode after replay: native:.:.:FuzzNative' 'malformed replay profile is fatal'
log="$(cat "$go_log")"
lacks "$log" $'\trun ./cmd/serf-fuzzcov' 'malformed profile skips global accounting'

reset_logs
expect_failure FAKE_GO_RUN_FAIL=1
has "$last_output" 'global coverage accounting failed' 'global accounting failure is fatal'

echo '== registry and package-list failures are fatal =='
reset_logs
expect_failure FAKE_REGISTRY_FAIL=1
has "$last_output" 'registry check failed; replay did not begin' 'registry failure stops before preflight'
lacks "$(cat "$go_log")" $'\tlist ' 'registry failure invokes no Go command'

reset_logs
expect_failure FAKE_REGISTRY_MALFORMED=1
has "$last_output" 'registry checker did not emit exact four-column TSV' 'malformed registry plan is fatal'
lacks "$(cat "$go_log")" $'\tlist ' 'malformed registry plan invokes no Go command'

reset_logs
expect_failure FAKE_GO_LIST_FAIL=1
has "$last_output" 'go list failed for module: .' 'go list failure is fatal'
lacks "$(cat "$go_log")" $'\ttest ' 'go list failure invokes no replay'

echo '----'
echo "fuzz-coverage-global-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
