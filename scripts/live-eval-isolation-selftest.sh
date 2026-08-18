#!/usr/bin/env bash
set -eu

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
. "$repo/scripts/live-eval-isolation.sh"

. "$repo/scripts/selftest-lib.sh"

scratch_dir fixture evener-live-isolation-selftest
cleanup() {
	scratch_rm
}
trap cleanup EXIT

source_state="$fixture/source-state"
source_home="$fixture/source-home"
binary="$fixture/source-evener"
env_file="$fixture/live.env"
mkdir -p "$source_state/evener/auth" "$source_home/.evener"
printf 'oauth fixture\n' >"$source_state/evener/auth/openai.json"
printf 'provider fixture\n' >"$source_home/.evener/providers.toml"
cat >"$binary" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$binary"
printf 'ISO=%s\nHOMEISO=%s\n' "$source_state" "$source_home" >"$env_file"

EVENER_LIVE_ENV="$env_file"
EVENER_LIVE_BINARY="$binary"
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
test -f "$first_state/evener/auth/openai.json"
test -f "$first_home/.evener/providers.toml"
test -x "$first_serf"
test -f "$second_trial/state/evener/auth/openai.json"
test -f "$second_trial/home/.evener/providers.toml"
test "$first_trial" != "$run_root"
test "$(cat "$source_state/evener/auth/openai.json")" = "oauth fixture"
test "$(cat "$source_home/.evener/providers.toml")" = "provider fixture"

live_eval_cleanup
test ! -e "$run_root"
printf 'PASS live-eval-isolation\n'
