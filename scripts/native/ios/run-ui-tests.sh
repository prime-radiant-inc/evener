#!/usr/bin/env bash
# run-ui-tests.sh — the installed-app qualification journeys, run inside the
# build runner's owned simulator lifetime.
#
# WHY: build success proves the app compiled and its bundles carry the right
# identity; it does not prove an installed app saves credentials, renders
# native Markdown, keeps an unsent draft across a cold launch, or answers its
# share scheme. Those journeys need the real app on the run's own simulator,
# a real hub whose only provider is the scripted loopback fixture, and the
# generated XCTest target from add-ui-test-target.rb. This runner owns all of
# that except the simulator: the parent build runner stays the sole
# simulator cleanup owner, and this script never touches simctl.
#
# WHAT IT DOES: builds the runtime pair and the evener binary, starts the
# fixture provider and an isolated hub under private XDG directories with
# HOME inherited, reads the fixture-only connection data from a mode-0600
# private file, injects it into a private copy of the generated test-run
# configuration, registers the UI test target, runs the three journeys
# against the Release app the parent built on the parent's simulator, and
# validates the xcresult: every intended test identity executed, none
# skipped, zero failures. The private state never reaches the output
# directory or any uploaded artifact.
#
# USAGE (from the build runner only, on macOS):
#   run-ui-tests.sh --build-identity ABSOLUTE_JSON --output ABSOLUTE_DIR
#
# END-USAGE
set -euo pipefail

SCRIPT_NAME="run-ui-tests"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
source "$repo_root/scripts/lib/scratch-lib.sh"

identity=""
output=""
while [ $# -gt 0 ]; do
	case "$1" in
	--build-identity)
		[ $# -ge 2 ] || { echo "$SCRIPT_NAME: --build-identity needs a path" >&2; exit 2; }
		identity="$2"
		shift 2
		;;
	--output)
		[ $# -ge 2 ] || { echo "$SCRIPT_NAME: --output needs a path" >&2; exit 2; }
		output="$2"
		shift 2
		;;
	*)
		echo "$SCRIPT_NAME: unknown flag: $1" >&2
		exit 2
		;;
	esac
done
if [ -z "$identity" ] || [ -z "$output" ]; then
	echo "$SCRIPT_NAME: needs --build-identity ABSOLUTE_JSON and --output ABSOLUTE_DIR" >&2
	exit 2
fi
case "$identity" in
	/*) ;;
	*)
		echo "$SCRIPT_NAME: --build-identity and --output must be absolute" >&2
		exit 2
		;;
esac
case "$output" in
	/*) ;;
	*)
		echo "$SCRIPT_NAME: --output must be absolute" >&2
		exit 2
		;;
esac

mkdir -p "$output"
log() { echo "==> $*" | tee -a "$output/ui-runner.log"; }

app_path="$(jq -r .appPath "$identity")"
sim_udid="$(jq -r .simulatorUDID "$identity")"
if [ -z "$app_path" ] || [ "$app_path" = "null" ] || [ -z "$sim_udid" ] || [ "$sim_udid" = "null" ]; then
	echo "$SCRIPT_NAME: build identity lacks appPath or simulatorUDID" >&2
	exit 2
fi
# .../DerivedData/Build/Products/Release-iphonesimulator/Evener.app
derived_data="$(cd "$(dirname "$app_path")/../../.." && pwd -P)"

# Private run state: none of this reaches the output directory. scratch_dir
# mints both directories and scratch_rm reclaims them; no recursive delete in
# this script ever takes a path argument.
scratch_dir run_dir evener-ui-qualification
scratch_dir workspace evener-ui-qualification-workspace
config_home="$run_dir/config"
state_home="$run_dir/state"
cache_home="$run_dir/cache"
mkdir -p "$config_home/evener" "$state_home" "$cache_home" "$workspace"
chmod 700 "$run_dir" "$config_home" "$state_home" "$cache_home" "$workspace"

provider_pid=""
hub_pid=""
daemons_stopped=0
provider_binary="$run_dir/fixture-provider"
evener_binary="$run_dir/evener"

cleanup() {
	status=$?
	# The hub deliberately outlives the daemons it spawns, and it announces
	# each one's pid in its log; stop this run's daemons before its state goes.
	if [ -f "$output/hub.log" ] && [ "$daemons_stopped" -eq 0 ]; then
		while read -r daemon_pid; do
			[ -n "$daemon_pid" ] || continue
			kill -0 "$daemon_pid" 2>/dev/null || continue
			ps -o args= -p "$daemon_pid" 2>/dev/null | grep -qF -- "$run_dir" || continue
			kill -TERM "$daemon_pid" 2>/dev/null || true
		done < <(grep -oE 'daemon session=[^ ]+ pid=[0-9]+' "$output/hub.log" | grep -oE '[0-9]+$')
		daemons_stopped=1
	fi
	if [ -n "$hub_pid" ]; then
		kill -TERM "$hub_pid" 2>/dev/null || true
		for _ in $(seq 1 50); do
			kill -0 "$hub_pid" 2>/dev/null || break
			sleep 0.1
		done
		kill -KILL "$hub_pid" 2>/dev/null || true
	fi
	if [ -n "$provider_pid" ]; then
		kill -TERM "$provider_pid" 2>/dev/null || true
		for _ in $(seq 1 50); do
			kill -0 "$provider_pid" 2>/dev/null || break
			sleep 0.1
		done
		kill -KILL "$provider_pid" 2>/dev/null || true
	fi
	scratch_rm
	exit "$status"
}
trap cleanup EXIT

cd "$repo_root"

log "building the runtime pair (make build-runtime)"
make build-runtime >"$output/build-runtime.log" 2>&1 || {
	echo "build-runtime failed; see $output/build-runtime.log" >&2
	tail -30 "$output/build-runtime.log" >&2
	exit 1
}

log "building the evener and fixture-provider binaries"
go build -o "$evener_binary" ./cmd/evener >"$output/build-evener.log" 2>&1
go build -o "$provider_binary" ./scripts/native/ios/fixture-provider >>"$output/build-evener.log" 2>&1

export XDG_CONFIG_HOME="$config_home"
export XDG_STATE_HOME="$state_home"
export XDG_CACHE_HOME="$cache_home"
# HOME stays inherited: the native README's requirement. Isolation comes from
# the private XDG directories above, never from replacing HOME.

log "starting the fixture provider"
"$provider_binary" --ready-file "$state_home/provider-ready.json" >"$output/provider.log" 2>&1 &
provider_pid=$!
provider_base=""
for _ in $(seq 1 600); do
	[ -f "$state_home/provider-ready.json" ] && {
		provider_base="$(jq -r .base_url "$state_home/provider-ready.json")"
		[ -n "$provider_base" ] && [ "$provider_base" != "null" ] && break
	}
	kill -0 "$provider_pid" 2>/dev/null || {
		echo "fixture provider exited before readiness:" >&2
		cat "$output/provider.log" >&2
		exit 1
	}
	sleep 0.1
done
if [ -z "$provider_base" ] || [ "$provider_base" = "null" ]; then
	echo "fixture provider never became ready" >&2
	exit 1
fi

# Current registry schema (§14.1 of the provider registry design): `base`
# inherits the curated openai-compatible row; the fake instance points at the
# loopback fixture and carries no credential.
cat >"$config_home/evener/providers.toml" <<EOF
schema = 2
default = "fake"

[providers.fake]
base = "openai-compatible"
base_url = "$provider_base"
api_key = "fakellm-not-a-secret"
EOF
export EVENER_PROVIDERS_CONFIG="$config_home/evener/providers.toml"

# A quiet disposable hub, the same choice the ratelimited-provider fixture
# makes: without this the hub refreshes seeded plugin marketplaces over the
# network the moment it starts.
cat >"$config_home/evener/hub.toml" <<'EOF'
plugin_auto_upgrade = false
EOF

log "starting the isolated hub"
"$evener_binary" hub -addr 127.0.0.1:0 -evener "$evener_binary" >"$output/hub.log" 2>&1 &
hub_pid=$!
hub_port=""
for _ in $(seq 1 600); do
	hub_port="$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$output/hub.log" 2>/dev/null | grep -oE '[0-9]+$')" || true
	[ -n "$hub_port" ] && break
	kill -0 "$hub_pid" 2>/dev/null || {
		echo "hub exited before it started listening:" >&2
		cat "$output/hub.log" >&2
		exit 1
	}
	sleep 0.1
done
if [ -z "$hub_port" ]; then
	echo "hub never started listening" >&2
	exit 1
fi
hub_origin="http://127.0.0.1:$hub_port"
if ! curl -s -o /dev/null "$hub_origin/"; then
	echo "hub did not answer at $hub_origin" >&2
	exit 1
fi

# The fixture-only connection data, kept in one private mode-0600 file. It is
# read back to build the test-run environment and never printed or uploaded.
connection="$state_home/connection.json"
printf '{"origin": "%s", "token": "%s"}\n' "$hub_origin" "$(cat "$state_home/evener/auth-token")" >"$connection"
chmod 600 "$connection"
hub_origin="$(jq -r .origin "$connection")"
hub_token="$(jq -r .token "$connection")"

# The red before the wiring: naming the intended tests against the project
# as prebuild generated it must fail, because the qualification target does
# not exist yet. An invocation that somehow succeeds here means the harness
# is lying about what it runs, and the run must not continue.
log "confirming the intended journeys fail without the qualification target"
set +e
xcodebuild test \
	-workspace mobile-native/ios/Evener.xcworkspace \
	-scheme Evener \
	-only-testing:EvenerQualificationUITests/testSavedHubMarkdownAndDraftRecovery \
	-destination "id=$sim_udid" \
	-derivedDataPath "$derived_data" \
	>"$output/absent-target-red.log" 2>&1
red_exit=$?
set -e
echo "$red_exit" >"$output/absent-target-red.exit"
if [ "$red_exit" -eq 0 ]; then
	echo "$SCRIPT_NAME: the intended journey ran before the qualification target existed; the harness cannot be trusted" >&2
	exit 1
fi

log "registering the generated UI test target"
(
	cd mobile-native
	bundle exec ruby ../scripts/native/ios/add-ui-test-target.rb ios/Evener.xcodeproj
) >"$output/add-ui-target.log" 2>&1

log "building the test bundles against the parent's products"
xcodebuild build-for-testing \
	-workspace mobile-native/ios/Evener.xcworkspace \
	-scheme Evener \
	-configuration Release \
	-destination "id=$sim_udid" \
	-derivedDataPath "$derived_data" \
	-quiet >"$output/build-for-testing.log" 2>&1

generated_run="$(find "$derived_data/Build/Products" -name '*.xctestrun' -print -quit)"
if [ -z "$generated_run" ]; then
	echo "build-for-testing produced no xctestrun under $derived_data" >&2
	exit 1
fi

# A private copy of the test-run configuration carries the connection data as
# the test process environment; the generated file itself stays untouched
# and the copy lives only in the private state that cleanup removes.
private_run="$state_home/qualification.xctestrun"
cp "$generated_run" "$private_run"
ui_key="$(plutil -convert json -o - "$private_run" | jq -r 'keys[] | select(startswith("EvenerQualificationUITests"))')"
if [ -z "$ui_key" ]; then
	echo "$SCRIPT_NAME: the test-run configuration has no EvenerQualificationUITests bundle" >&2
	exit 1
fi
# Xcode's own xctestrun can already carry an (empty) EnvironmentVariables
# dict for the bundle, so the container is added only when absent and every
# entry lands through whichever of Add or Set the file needs.
if ! /usr/libexec/PlistBuddy -c "Print :$ui_key:EnvironmentVariables" "$private_run" >/dev/null 2>&1; then
	/usr/libexec/PlistBuddy -c "Add :$ui_key:EnvironmentVariables dict" "$private_run" >/dev/null
fi
inject_environment() {
	if /usr/libexec/PlistBuddy -c "Print :$ui_key:EnvironmentVariables:$1" "$private_run" >/dev/null 2>&1; then
		/usr/libexec/PlistBuddy -c "Set :$ui_key:EnvironmentVariables:$1 string $2" "$private_run" >/dev/null
	else
		/usr/libexec/PlistBuddy -c "Add :$ui_key:EnvironmentVariables:$1 string $2" "$private_run" >/dev/null
	fi
}
inject_environment EVENER_QUALIFICATION_HUB_ORIGIN "$hub_origin"
inject_environment EVENER_QUALIFICATION_HUB_TOKEN "$hub_token"
inject_environment EVENER_QUALIFICATION_WORKSPACE "$workspace"
inject_environment EVENER_QUALIFICATION_HOME "$HOME"

xcresult="$output/ui.xcresult"
log "running the qualification journeys"
set +e
xcodebuild test-without-building \
	-xctestrun "$private_run" \
	-configuration Release \
	-derivedDataPath "$derived_data" \
	-destination "id=$sim_udid" \
	-resultBundlePath "$xcresult" \
	>"$output/xcodebuild-test.log" 2>&1
test_exit=$?
set -e
echo "$test_exit" >"$output/xcodebuild-test.exit"
log "xcodebuild exit: $test_exit"

# Validate the result bundle: every intended test identity must have
# actually executed, none skipped, zero failures. An unreadable or partial
# result fails; this never passes on absence.
summary="$output/xcresult-summary.json"
xcrun xcresulttool get test-results summary --path "$xcresult" >"$summary" 2>"$output/xcresulttool-summary.log"
tests_json="$output/xcresult-tests.json"
xcrun xcresulttool get test-results tests --path "$xcresult" >"$tests_json" 2>"$output/xcresulttool-tests.log"

failures="$(jq -r '.testFailures | length' "$summary")"
if [ "$failures" != "0" ]; then
	echo "$SCRIPT_NAME: $failures test failure(s) recorded in the xcresult" >&2
	cat "$summary" >&2
	exit 1
fi

# The three intended identities, exactly; each must appear as an executed
# test case with a passed result and no skipped outcome.
for intended in \
	testSavedHubMarkdownAndDraftRecovery \
	testSheetGestureKeepsControlsUsable \
	testWarmAndColdShareSchemeActivation; do
	node="$(jq -r --arg id "$intended" '
		[ .. | objects | select(.name? == $id) ] | if length == 0 then "absent"
		else (.[0].result? // "unknown") end' "$tests_json")"
	if [ "$node" = "absent" ] || [ -z "$node" ]; then
		echo "$SCRIPT_NAME: intended test $intended did not execute" >&2
		exit 1
	fi
	if [ "$node" != "Passed" ]; then
		echo "$SCRIPT_NAME: intended test $intended finished as $node" >&2
		exit 1
	fi
	log "journey $intended passed"
done

if [ "$test_exit" != "0" ]; then
	echo "$SCRIPT_NAME: xcodebuild exited $test_exit despite passing tests" >&2
	exit "$test_exit"
fi
log "all three qualification journeys passed"
