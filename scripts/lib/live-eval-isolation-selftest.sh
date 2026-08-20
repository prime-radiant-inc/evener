#!/usr/bin/env bash
set -eu

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
. "$repo/scripts/lib/live-eval-isolation.sh"

. "$repo/scripts/lib/selftest-lib.sh"

cleanup() {
	scratch_rm
}
trap cleanup EXIT
scratch_dir fixture evener-live-isolation-selftest

source_state="$fixture/source-state"
source_home="$fixture/source-home"
binary="$fixture/source-evener"
env_file="$fixture/live.env"
mkdir -p "$source_state/evener/auth" "$source_home/.config/evener"
printf 'oauth fixture\n' >"$source_state/evener/auth/openai.json"
printf 'provider fixture\n' >"$source_home/.config/evener/providers.toml"
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
first_evener="$LIVE_EVAL_EVENER"

live_eval_prepare_trial baseline-1
second_trial="$LIVE_EVAL_TRIAL_ROOT"

test "$first_trial" != "$second_trial"
test -f "$first_state/evener/auth/openai.json"
test -f "$first_home/.config/evener/providers.toml"
test -x "$first_evener"
test -f "$second_trial/state/evener/auth/openai.json"
test -f "$second_trial/home/.config/evener/providers.toml"
test "$first_trial" != "$run_root"
test "$(cat "$source_state/evener/auth/openai.json")" = "oauth fixture"
test "$(cat "$source_home/.config/evener/providers.toml")" = "provider fixture"

live_eval_cleanup
test ! -e "$run_root"
printf 'PASS live-eval-isolation\n'
