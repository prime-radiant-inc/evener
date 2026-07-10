#!/usr/bin/env bash
# fuzz-coverage-global-selftest.sh exercises the replay runner without compiling
# Serf. A registry-check seam emits a tiny validated plan and a fake Go command
# records every invocation while writing synthetic package-local profiles.
set -euo pipefail

runner="$(cd "$(dirname "$0")" && pwd)/fuzz-coverage-global.sh"
work="$(mktemp -d -t fuzzcov-global-selftest.XXXXXX)"
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

repo="$work/repo"
mkdir -p "$repo/scripts"
cp "$runner" "$repo/scripts/fuzz-coverage-global.sh"
for module in agent auth envvars fuzz invariant llm; do
	mkdir -p "$repo/$module"
done
printf 'go 1.25.6\n\nuse (\n\t.\n\t./agent\n\t./auth\n\t./envvars\n\t./fuzz\n\t./invariant\n\t./llm\n)\n' >"$repo/go.work"
for module in . agent auth envvars fuzz invariant llm; do
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
printf 'registry\t%s\n' "${GOWORK:-}" >>"$FAKE_REGISTRY_LOG"
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
REGISTRY
chmod +x "$registry"

gobin="$work/go.sh"
cat >"$gobin" <<'GOBIN'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\t%s\tseed=%s\tchecks=%s\t%s\n' "$PWD" "${GOWORK:-}" "${RAPID_SEED:-}" "${RAPID_CHECKS:-}" "$*" >>"$FAKE_GO_LOG"
command="$1"
shift
case "$command" in
	list)
		if [ "${FAKE_GO_LIST_FAIL:-}" = "1" ]; then
			echo "synthetic go list failure" >&2
			exit 18
		fi
		# Root has two packages; the other six list successfully but have no
		# packages, proving the runner asks all seven and keeps package profiles
		# separate within a module.
		if [ "$PWD" = "$FAKE_REPO" ]; then
			printf '%s\n' "$PWD"
			printf '%s/other\n' "$PWD"
			if [ "${FAKE_LIST_MISSING:-}" = "1" ]; then
				printf '%s/missing-one\n' "$PWD"
				printf '%s/missing-two\n' "$PWD"
			fi
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
		[ -n "$profile" ] || { echo "fake go: missing coverprofile" >&2; exit 19; }
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
			elif [ "$run" = '^FuzzOther$' ]; then
				echo 'example.test/other.go:5.1,6.1 3 1'
			else
				# Merge must retain this native-positive count and add the Rapid block.
				echo 'example.test/root.go:1.1,2.1 1 0'
				echo 'example.test/rapid.go:3.1,4.1 2 1'
			fi
		} >"$profile"
		;;
	run)
		[ "$1" = './cmd/serf-fuzzcov' ] || { echo "fake go: unexpected run target $1" >&2; exit 21; }
		if [ "${FAKE_GO_RUN_FAIL:-}" = "1" ]; then
			echo "synthetic global accounting failure" >&2
			exit 24
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
		;;
	*)
		echo "fake go: unexpected command $command" >&2
		exit 23
		;;
esac
GOBIN
chmod +x "$gobin"

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
out="$(run_runner 2>&1)"
has "$out" 'account package-local profiles' 'successful replay reaches global accounting'
has "$(cat "$registry_log")" "$repo/go.work" 'registry checker inherits anchored GOWORK'

log="$(cat "$go_log")"
for module in . agent auth envvars fuzz invariant llm; do
	if [ "$module" = . ]; then
		module_dir="$repo"
	else
		module_dir="$repo/$module"
	fi
	has "$log" "$module_dir"$'\t'"$repo/go.work"$'\tseed=\tchecks=\tlist -tags serffuzz -f {{.Dir}} ./...' "go list -tags serffuzz covers module $module"
done
has "$log" $'\ttest -tags serffuzz -run ^FuzzNative$ -count=1 -coverprofile=' 'native replay has exact deterministic flags'
count_has "$log" $'\ttest -tags serffuzz -run ^FuzzNative$ -count=1 -coverprofile=' 1 'native target replays once'
count_has "$log" $'\ttest -tags serffuzz -run ^FuzzOther$ -count=1 -coverprofile=' 1 'other package native target replays once'
count_has "$log" $'\ttest -tags serffuzz -run ^TestRapid$ -count=1 -coverprofile=' 5 'rapid target replays fixed seed bank exactly once each'
for seed in 1 2 3 5 8; do
	has "$log" $'\tseed='"$seed"$'\tchecks=100\ttest -tags serffuzz -run ^TestRapid$ -count=1 -coverprofile=' "rapid seed $seed uses RAPID_CHECKS=100"
done
lacks "$log" '-coverpkg' 'replay never uses coverpkg'
has "$log" $'\trun ./cmd/serf-fuzzcov -global-manifest ' 'global accounting runs serf-fuzzcov'
has "$log" "-repo-root $repo -global-exclusions $repo/scripts/fuzzcov-global-exclusions.txt -global-floors $repo/scripts/fuzzcov-global-floors.txt -global-minimum 95 -check -bless" 'global accounting receives root, policy files, minimum, check, and bless'

manifest="$(cat "$cov_manifest")"
count_has "$manifest" $'.\t.\t' 1 'global manifest has one merged profile for the package'
count_has "$manifest" $'.\t./other\t' 1 'global manifest has a separate profile for the other package'
merged="$(cat "$cov_profile")"
has "$merged" 'mode: set' 'merged profile preserves set mode'
has "$merged" 'example.test/root.go:1.1,2.1 1 1' 'merge retains coverage from native replay'
has "$merged" 'example.test/rapid.go:3.1,4.1 2 1' 'merge adds Rapid-only coverage block'
other_merged="$(cat "$cov_other_profile")"
has "$other_merged" 'example.test/other.go:5.1,6.1 3 1' 'other package profile keeps its own blocks'
lacks "$other_merged" 'example.test/root.go' 'profiles never merge blocks across packages'

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
