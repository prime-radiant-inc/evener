#!/usr/bin/env bash
# fuzz-drive.sh — Plan 12 Phase A driver: generate REAL provider traffic and
# harvest it into the fuzz seed corpus.
#
# It runs a corpus of varied coding tasks through the real `serf` one-shot CLI
# against each configured provider, with the fuzz-corpus recorders on
# (SERF_FUZZ_RECORD=1) and a shared staging state dir, then runs
# serf-fuzz-harvest over that state dir to emit shape-scrubbed, gitleaks-gated
# seeds. Real inputs reach decoder/tool states that random generation never will.
#
# Each task runs in its own throwaway working directory (so the agent's file ops
# can't collide or escape), but all runs share one --state-dir so their
# recordings (api-raw.jsonl, transcripts, jobs, appwire/http frames) accumulate
# for a single harvest pass.
#
# This makes LIVE provider calls — it costs money and can rate-limit. Use the
# cheap models it defaults to, and run it on demand (not in CI). It is the
# corpus-refresh step the continuous loop (Plan 12 Phase E) drives.
#
# Usage:
#   scripts/fuzz-drive.sh [options]
#
# Options:
#   --providers "p/m ..."  space/comma list of provider/model to drive
#                          (default: "kimi-anthropic/kimi-for-coding openai/gpt-5.4-mini")
#   --tasks-dir DIR        directory of prompt files, one task per file
#                          (default: fuzz/drive-tasks)
#   --runs N               cap total runs (default: every task x every provider)
#   --max-rounds N         per-run tool-round cap passed to serf (default: 30)
#   --effort LEVEL         reasoning effort passed to serf (default: low)
#   --per-task-timeout DUR wall-clock cap per run, `timeout` syntax (default: 180s)
#   --retries N            transient-failure (rate-limit) retries per run (default: 4)
#   --state-dir DIR        shared recording staging dir (default: a fresh mktemp)
#   --no-harvest           drive only; skip the harvest pass
#   --pr                   after harvest, push a branch and open a PR with new
#                          seeds (default: inspect-first — stage on a branch, no
#                          push). The continuous loop passes --pr.
#   -h, --help             this help
#
# Binary overrides (used by the self-test to inject stubs):
#   SERF_FUZZ_SERF_BIN     serf binary       (default: built from ./cmd/serf)
#   SERF_FUZZ_HARVEST_BIN  harvester binary  (default: go run ./cmd/serf-fuzz-harvest)
#   SERF_FUZZ_GH           gh binary         (default: gh)
#
# Self-test (offline, deterministic, no real provider calls):
#   scripts/fuzz-drive-selftest.sh
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"

providers_default="kimi-anthropic/kimi-for-coding openai/gpt-5.4-mini"
providers="${SERF_FUZZ_DRIVE_PROVIDERS:-$providers_default}"
tasks_dir="$repo_root/fuzz/drive-tasks"
runs_cap=0
max_rounds=30
effort="low"
per_task_timeout="180s"
retries=4
state_dir=""
do_harvest=1
do_pr=0

note() { printf '%s\n' "fuzz-drive: $*" >&2; }
die() { note "$*"; exit 2; }

while [ $# -gt 0 ]; do
	case "$1" in
		--providers) providers="$2"; shift 2 ;;
		--tasks-dir) tasks_dir="$2"; shift 2 ;;
		--runs) runs_cap="$2"; shift 2 ;;
		--max-rounds) max_rounds="$2"; shift 2 ;;
		--effort) effort="$2"; shift 2 ;;
		--per-task-timeout) per_task_timeout="$2"; shift 2 ;;
		--retries) retries="$2"; shift 2 ;;
		--state-dir) state_dir="$2"; shift 2 ;;
		--no-harvest) do_harvest=0; shift ;;
		--pr) do_pr=1; shift ;;
		-h|--help) sed -n '2,60p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "unknown option: $1 (try --help)" ;;
	esac
done

# Normalize commas to spaces so --providers accepts either separator.
providers="${providers//,/ }"

serf_bin="${SERF_FUZZ_SERF_BIN:-}"
harvest_bin="${SERF_FUZZ_HARVEST_BIN:-}"
gh="${SERF_FUZZ_GH:-gh}"
# Backoff sleep is a seam so the self-test can make retries instant.
sleep_cmd="${SERF_FUZZ_DRIVE_SLEEP:-sleep}"

# --- staging dirs ------------------------------------------------------------
if [ -z "$state_dir" ]; then
	state_dir="$(mktemp -d "${TMPDIR:-/tmp}/fuzz-drive-state.XXXXXX")"
fi
mkdir -p "$state_dir" || die "cannot create state dir $state_dir"

[ -d "$tasks_dir" ] || die "tasks dir not found: $tasks_dir"
mapfile -t tasks < <(find "$tasks_dir" -maxdepth 1 -type f \( -name '*.txt' -o -name '*.md' \) | sort)
[ "${#tasks[@]}" -gt 0 ] || die "no task files (*.txt/*.md) in $tasks_dir"

# Build serf once if not provided, so every run uses current code.
if [ -z "$serf_bin" ]; then
	serf_bin="$state_dir/serf"
	note "building serf -> $serf_bin"
	( cd "$repo_root" && go build -o "$serf_bin" ./cmd/serf ) || die "go build ./cmd/serf failed"
fi

# --- the drive loop ----------------------------------------------------------
total=0 ok=0 failed=0 skipped_provider=""
note "state dir: $state_dir"
note "providers: $providers"
note "tasks:     ${#tasks[@]} in $tasks_dir"

# transient_failure greps a run's stderr for rate-limit / overload / timeout
# signatures that warrant a backoff retry rather than skipping the task.
transient_failure() {
	grep -qiE 'rate.?limit|429|overloaded|too many requests|temporarily|timeout|deadline' "$1"
}

for pm in $providers; do
	# Skip a whole provider whose first run fails for a config/auth reason
	# (unknown instance, missing key) so a misconfigured provider doesn't abort
	# the corpus build for the others.
	provider_ok=-1
	for task_file in "${tasks[@]}"; do
		if [ "$runs_cap" -gt 0 ] && [ "$total" -ge "$runs_cap" ]; then break 2; fi
		total=$((total + 1))
		task="$(cat "$task_file")"
		work="$(mktemp -d "${TMPDIR:-/tmp}/fuzz-drive-work.XXXXXX")"
		# A tiny scaffold gives read/edit/search tasks something to touch.
		printf 'placeholder project for fuzz-drive task\n' >"$work/README.md"

		attempt=0 run_ok=0
		while :; do
			err="$work/run.$attempt.err"
			( cd "$work" && SERF_FUZZ_RECORD=1 SERF_STATE_DIR="$state_dir" \
				timeout "$per_task_timeout" "$serf_bin" \
					--model "$pm" --max-rounds "$max_rounds" \
					--reasoning-effort "$effort" --verbose "$task" \
					>"$work/run.$attempt.out" 2>"$err" )
			rc=$?
			if [ "$rc" -eq 0 ]; then run_ok=1; break; fi
			if [ "$attempt" -lt "$retries" ] && transient_failure "$err"; then
				backoff=$((2 ** (attempt + 1)))
				note "[$pm] transient failure (rc=$rc), backoff ${backoff}s (attempt $((attempt + 1))/$retries)"
				"$sleep_cmd" "$backoff"
				attempt=$((attempt + 1))
				continue
			fi
			break
		done

		if [ "$run_ok" -eq 1 ]; then
			ok=$((ok + 1)); provider_ok=1
			note "[$pm] ok: $(basename "$task_file")"
		else
			failed=$((failed + 1))
			note "[$pm] FAILED: $(basename "$task_file") (rc=$rc) — see $err"
			if [ "$provider_ok" -eq -1 ]; then
				skipped_provider="$skipped_provider $pm"
				note "[$pm] first run failed and looks non-transient; skipping the rest of this provider"
				rm -rf "$work"
				break
			fi
		fi
		rm -rf "$work"
	done
done

note "drive complete: $total run(s), $ok ok, $failed failed"
[ -n "$skipped_provider" ] && note "skipped (config/auth):$skipped_provider"

# Record-size proxy for cost/volume (exact token accounting is in the run logs).
for f in api-raw.jsonl appwire-frames.jsonl hub-http.jsonl; do
	[ -f "$state_dir/$f" ] && note "recorded $(wc -l <"$state_dir/$f") line(s) in $f"
done

if [ "$ok" -eq 0 ]; then
	die "no successful runs — nothing recorded; not harvesting"
fi

# --- harvest -----------------------------------------------------------------
if [ "$do_harvest" -eq 0 ]; then
	note "skipping harvest (--no-harvest); recordings in $state_dir"
	exit 0
fi

note "harvesting $state_dir -> seeds under $repo_root"
harvest_log="$state_dir/harvest.log"
if [ -n "$harvest_bin" ]; then
	harvest_cmd=("$harvest_bin")
else
	harvest_cmd=(go run ./cmd/serf-fuzz-harvest)
fi
if ! ( cd "$repo_root" && "${harvest_cmd[@]}" \
		--state-dir "$state_dir" --out-root "$repo_root" --log "$harvest_log" ); then
	die "harvest failed (gitleaks gate or error) — see $harvest_log; NOT opening a PR"
fi

# --- new seeds? --------------------------------------------------------------
mapfile -t new_seeds < <(cd "$repo_root" && git status --porcelain | grep -E 'testdata/fuzz/' | awk '{print $NF}')
if [ "${#new_seeds[@]}" -eq 0 ]; then
	note "harvest added no new seeds (corpus already covers this traffic)"
	exit 0
fi
note "harvest staged ${#new_seeds[@]} new/changed seed file(s)"

# --- PR (inspect-first by default) -------------------------------------------
branch="fuzz/drive-corpus-$(cd "$repo_root" && git rev-parse --short HEAD)"
( cd "$repo_root" && git checkout -b "$branch" >/dev/null 2>&1 || git checkout "$branch" >/dev/null 2>&1
	git add -- "${new_seeds[@]}"
	git commit -q -m "test(fuzz): harvest real-traffic seeds from fuzz-drive

${#new_seeds[@]} shape-scrubbed seed(s) from live provider traffic across:
$providers
Gitleaks-gated by serf-fuzz-harvest. Generated by scripts/fuzz-drive.sh." )

if [ "$do_pr" -eq 0 ]; then
	note "staged ${#new_seeds[@]} seed(s) on branch $branch (inspect-first; pass --pr to push + open a PR)"
	exit 0
fi

if ! command -v "$gh" >/dev/null 2>&1; then
	note "gh unavailable; left ${#new_seeds[@]} seed(s) on local branch $branch (no push, no PR)"
	exit 0
fi
( cd "$repo_root" && git push -u origin "$branch" >/dev/null 2>&1 ) || { note "git push failed; left on $branch"; exit 0; }
url="$("$gh" pr create --base main --head "$branch" --label fuzz-corpus \
	--title "Harvest real-traffic fuzz seeds (fuzz-drive)" \
	--body "Shape-scrubbed, gitleaks-gated seeds harvested from live provider traffic by scripts/fuzz-drive.sh." 2>/dev/null)" \
	&& note "opened PR: $url" || note "gh pr create failed; left on branch $branch"
