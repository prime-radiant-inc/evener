#!/usr/bin/env bash
set -eu

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
. "$repo/scripts/live-eval-isolation.sh"

fixture=$(mktemp -d -t serf-live-isolation-selftest.XXXXXX)
cleanup() {
	rm -rf -- "$fixture"
}
trap cleanup EXIT

source_state="$fixture/source-state"
source_home="$fixture/source-home"
binary="$fixture/source-serf"
env_file="$fixture/live.env"
mkdir -p "$source_state/serf/auth" "$source_home/.serf"
printf 'oauth fixture\n' >"$source_state/serf/auth/openai.json"
printf 'provider fixture\n' >"$source_home/.serf/providers.toml"
cat >"$binary" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$binary"
printf 'ISO=%s\nHOMEISO=%s\n' "$source_state" "$source_home" >"$env_file"

SERF_LIVE_ENV="$env_file"
SERF_LIVE_BINARY="$binary"
live_eval_begin
run_root="$LIVE_EVAL_ROOT"

live_eval_prepare_trial feature-1
first_trial="$LIVE_EVAL_TRIAL_ROOT"
first_state="$LIVE_EVAL_STATE"
first_home="$LIVE_EVAL_HOME"
first_serf="$LIVE_EVAL_SERF"

live_eval_prepare_trial baseline-1
second_trial="$LIVE_EVAL_TRIAL_ROOT"

test "$first_trial" != "$second_trial"
test -f "$first_state/serf/auth/openai.json"
test -f "$first_home/.serf/providers.toml"
test -x "$first_serf"
test -f "$second_trial/state/serf/auth/openai.json"
test -f "$second_trial/home/.serf/providers.toml"
test "$first_trial" != "$run_root"
test "$(cat "$source_state/serf/auth/openai.json")" = "oauth fixture"
test "$(cat "$source_home/.serf/providers.toml")" = "provider fixture"

live_eval_cleanup
test ! -e "$run_root"
printf 'PASS live-eval-isolation\n'
