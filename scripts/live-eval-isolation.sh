#!/usr/bin/env bash

# The live evals receive an isolated OAuth/config handoff and a built serf
# binary from the operator. Copy only those inputs into a fresh run/trial tree
# so sessions, transcripts, answers, and results cannot cross test boundaries.

live_eval_begin() {
	: "${SERF_LIVE_ENV:?set SERF_LIVE_ENV to the live-eval environment file}"
	: "${SERF_LIVE_BINARY:?set SERF_LIVE_BINARY to the built serf binary}"
	if [ ! -f "$SERF_LIVE_ENV" ]; then
		printf 'live eval environment file not found: %s\n' "$SERF_LIVE_ENV" >&2
		return 1
	fi
	if [ ! -f "$SERF_LIVE_BINARY" ]; then
		printf 'live eval binary not found: %s\n' "$SERF_LIVE_BINARY" >&2
		return 1
	fi

	. "$SERF_LIVE_ENV"
	: "${ISO:?$SERF_LIVE_ENV must define ISO as the source state root}"
	: "${HOMEISO:?$SERF_LIVE_ENV must define HOMEISO as the source home root}"
	if [ ! -d "$ISO" ]; then
		printf 'live eval source state root not found: %s\n' "$ISO" >&2
		return 1
	fi
	if [ ! -d "$HOMEISO" ]; then
		printf 'live eval source home root not found: %s\n' "$HOMEISO" >&2
		return 1
	fi

	LIVE_EVAL_ROOT=$(mktemp -d -t serf-live-eval.XXXXXX)
}

live_eval_prepare_trial() {
	: "${1:?live eval trial name is required}"
	LIVE_EVAL_TRIAL_ROOT=$(mktemp -d "$LIVE_EVAL_ROOT/$1.XXXXXX")
	LIVE_EVAL_STATE="$LIVE_EVAL_TRIAL_ROOT/state"
	LIVE_EVAL_HOME="$LIVE_EVAL_TRIAL_ROOT/home"
	LIVE_EVAL_SERF="$LIVE_EVAL_TRIAL_ROOT/serf"
	LIVE_EVAL_WORK="$LIVE_EVAL_TRIAL_ROOT/work"

	# The state root contains credentials as input, but no prior sessions. The
	# home root contains the provider configuration used by the live command.
	mkdir -p "$LIVE_EVAL_STATE/serf/auth" "$LIVE_EVAL_HOME/.serf" "$LIVE_EVAL_WORK"
	if [ ! -d "$ISO/serf/auth" ]; then
		printf 'live eval source has no serf/auth directory: %s\n' "$ISO" >&2
		return 1
	fi
	if [ ! -d "$HOMEISO/.serf" ]; then
		printf 'live eval source has no .serf directory: %s\n' "$HOMEISO" >&2
		return 1
	fi
	cp -R "$ISO/serf/auth/." "$LIVE_EVAL_STATE/serf/auth/"
	cp -R "$HOMEISO/.serf/." "$LIVE_EVAL_HOME/.serf/"
	cp "$SERF_LIVE_BINARY" "$LIVE_EVAL_SERF"
	chmod +x "$LIVE_EVAL_SERF"
}

live_eval_cleanup() {
	if [ -n "${LIVE_EVAL_ROOT:-}" ]; then
		rm -rf "$LIVE_EVAL_ROOT"
		LIVE_EVAL_ROOT=
	fi
}
