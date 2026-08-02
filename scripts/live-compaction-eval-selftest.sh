#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
fixture=$(mktemp -d -t serf-live-compaction-selftest.XXXXXX)
cleanup() {
	rm -rf -- "$fixture"
}
trap cleanup EXIT

source_state="$fixture/source-state"
source_home="$fixture/source-home"
binary="$fixture/source-serf"
env_file="$fixture/live.env"
observed="$fixture/observed.env"
mkdir -p "$source_state/serf/auth" "$source_home/.serf"
printf 'oauth fixture\n' >"$source_state/serf/auth/openai.json"
printf 'provider fixture\n' >"$source_home/.serf/providers.toml"

cat >"$binary" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

work=
state=
while [ "$#" -gt 0 ]; do
	case "$1" in
		--dir)
			work="$2"
			shift 2
			;;
		--state-dir)
			state="$2"
			shift 2
			;;
		*)
			shift
			;;
	esac
done

: "${SERF_LIVE_SELFTEST_LOG:?}"
: "${HOME:?}"
: "${XDG_STATE_HOME:?}"
: "${SERF_STATE_DIR:?}"
: "${SERF_PROVIDERS_CONFIG:?}"
test -n "$work"
test -n "$state"
test "$SERF_STATE_DIR" = "$state"
test "$SERF_PROVIDERS_CONFIG" = "$HOME/.serf/providers.toml"
test -f "$SERF_PROVIDERS_CONFIG"
test -f "$state/auth/openai.json"
printf '%s|%s|%s|%s\n' "$HOME" "$XDG_STATE_HOME" "$SERF_STATE_DIR" "$SERF_PROVIDERS_CONFIG" >>"$SERF_LIVE_SELFTEST_LOG"
printf 'answers\n' >"$work/answers.txt"
mkdir -p "$state/sessions"
printf '{"kind":"compact"}\n' >"$state/sessions/fake.transcript.jsonl"
EOF
chmod +x "$binary"
printf 'ISO=%s\nHOMEISO=%s\n' "$source_state" "$source_home" >"$env_file"

if (unset SERF_LIVE_TESTS; \
	SERF_LIVE_ENV="$env_file" SERF_LIVE_BINARY="$binary" \
	"$repo/test/live-compaction-retention-eval.sh") >"$fixture/no-opt-in.log" 2>&1; then
	printf 'retention eval ran without SERF_LIVE_TESTS=1\n' >&2
	exit 1
fi
grep -F 'set SERF_LIVE_TESTS=1 to opt into the live compaction eval' "$fixture/no-opt-in.log" >/dev/null
if (unset SERF_LIVE_TESTS; \
	SERF_LIVE_ENV="$env_file" SERF_LIVE_BINARY="$binary" \
	"$repo/test/live-compaction-dense-value-test.sh") >"$fixture/dense-no-opt-in.log" 2>&1; then
	printf 'dense eval ran without SERF_LIVE_TESTS=1\n' >&2
	exit 1
fi
grep -F 'set SERF_LIVE_TESTS=1 to opt into the live compaction eval' "$fixture/dense-no-opt-in.log" >/dev/null

: >"$observed"
SERF_LIVE_TESTS=1 \
	SERF_LIVE_ENV="$env_file" \
	SERF_LIVE_BINARY="$binary" \
	SERF_STATE_DIR="$fixture/ambient-state" \
	SERF_PROVIDERS_CONFIG="$fixture/ambient-providers.toml" \
	SERF_LIVE_SELFTEST_LOG="$observed" \
	"$repo/test/live-compaction-retention-eval.sh" >"$fixture/retention.log"
grep -F 'DONE_LIVE_EVAL' "$fixture/retention.log" >/dev/null
test "$(grep -c 'facts.md_mentions=0' "$fixture/retention.log")" -eq 4
grep -F 'feature mean recall: 0.00/7 (n=2)' "$fixture/retention.log" >/dev/null
grep -F 'baseline mean recall: 0.00/7 (n=2)' "$fixture/retention.log" >/dev/null

SERF_LIVE_TESTS=1 \
	SERF_LIVE_ENV="$env_file" \
	SERF_LIVE_BINARY="$binary" \
	SERF_STATE_DIR="$fixture/ambient-state" \
	SERF_PROVIDERS_CONFIG="$fixture/ambient-providers.toml" \
	SERF_LIVE_SELFTEST_LOG="$observed" \
	"$repo/test/live-compaction-dense-value-test.sh" >"$fixture/dense.log"
grep -F 'DONE_DENSE' "$fixture/dense.log" >/dev/null
grep -F 'mandated-note mean recall: 0.00/15 (n=2)' "$fixture/dense.log" >/dev/null
grep -F 'blind mean recall: 0.00/15 (n=2)' "$fixture/dense.log" >/dev/null

test "$(wc -l <"$observed")" -eq 8
while IFS='|' read -r trial_home trial_xdg trial_state trial_providers; do
	trial_root=${trial_home%/home}
	case "$trial_home" in
		*/home) ;;
		*) printf 'trial HOME escaped trial root: %s\n' "$trial_home" >&2; exit 1 ;;
	esac
	case "$trial_xdg" in
		"$trial_root/state") ;;
		*) printf 'trial XDG_STATE_HOME escaped trial root: %s\n' "$trial_xdg" >&2; exit 1 ;;
	esac
	case "$trial_state" in
		"$trial_root/state/serf") ;;
		*) printf 'trial SERF_STATE_DIR escaped trial root: %s\n' "$trial_state" >&2; exit 1 ;;
	esac
	case "$trial_providers" in
		"$trial_root/home/.serf/providers.toml") ;;
		*) printf 'trial provider config escaped trial root: %s\n' "$trial_providers" >&2; exit 1 ;;
	esac
	case "$trial_providers" in
		*ambient*) printf 'ambient provider config leaked: %s\n' "$trial_providers" >&2; exit 1 ;;
	esac
	case "$trial_state" in
		*ambient*) printf 'ambient state dir leaked: %s\n' "$trial_state" >&2; exit 1 ;;
	esac
done <"$observed"

printf 'PASS live-compaction-eval\n'
