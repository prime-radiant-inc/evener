#!/usr/bin/env bash

# The live evals receive an isolated OAuth/config handoff and a built evener
# binary from the operator. Copy only those inputs into a fresh run/trial tree
# so sessions, transcripts, answers, and results cannot cross test boundaries.

live_eval_begin() {
	: "${EVENER_LIVE_ENV:?set EVENER_LIVE_ENV to the live-eval environment file}"
	: "${EVENER_LIVE_BINARY:?set EVENER_LIVE_BINARY to the built evener binary}"
	if [ ! -f "$EVENER_LIVE_ENV" ]; then
		printf 'live eval environment file not found: %s\n' "$EVENER_LIVE_ENV" >&2
		return 1
	fi
	if [ ! -f "$EVENER_LIVE_BINARY" ]; then
		printf 'live eval binary not found: %s\n' "$EVENER_LIVE_BINARY" >&2
		return 1
	fi

	. "$EVENER_LIVE_ENV"
	: "${ISO:?$EVENER_LIVE_ENV must define ISO as the source state root}"
	: "${HOMEISO:?$EVENER_LIVE_ENV must define HOMEISO as the source home root}"
	if [ ! -d "$ISO" ]; then
		printf 'live eval source state root not found: %s\n' "$ISO" >&2
		return 1
	fi
	if [ ! -d "$HOMEISO" ]; then
		printf 'live eval source home root not found: %s\n' "$HOMEISO" >&2
		return 1
	fi

	# An explicit template, never -t: macOS mktemp -t ignores TMPDIR.
	LIVE_EVAL_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/evener-live-eval.XXXXXX")
}

live_eval_prepare_trial() {
	: "${1:?live eval trial name is required}"
	LIVE_EVAL_TRIAL_ROOT=$(mktemp -d "$LIVE_EVAL_ROOT/$1.XXXXXX")
	LIVE_EVAL_STATE="$LIVE_EVAL_TRIAL_ROOT/state"
	LIVE_EVAL_HOME="$LIVE_EVAL_TRIAL_ROOT/home"
	LIVE_EVAL_EVENER="$LIVE_EVAL_TRIAL_ROOT/evener"
	LIVE_EVAL_WORK="$LIVE_EVAL_TRIAL_ROOT/work"

	# The state root contains credentials as input, but no prior sessions. The
	# home root contains the provider configuration used by the live command.
	mkdir -p "$LIVE_EVAL_STATE/evener/auth" "$LIVE_EVAL_HOME/.evener" "$LIVE_EVAL_WORK"
	if [ ! -d "$ISO/evener/auth" ]; then
		printf 'live eval source has no evener/auth directory: %s\n' "$ISO" >&2
		return 1
	fi
	if [ ! -d "$HOMEISO/.evener" ]; then
		printf 'live eval source has no .evener directory: %s\n' "$HOMEISO" >&2
		return 1
	fi
	if [ ! -f "$HOMEISO/.evener/providers.toml" ]; then
		printf 'live eval source has no providers.toml: %s\n' "$HOMEISO/.evener" >&2
		return 1
	fi
	cp -R "$ISO/evener/auth/." "$LIVE_EVAL_STATE/evener/auth/"
	cp -R "$HOMEISO/.evener/." "$LIVE_EVAL_HOME/.evener/"
	cp "$EVENER_LIVE_BINARY" "$LIVE_EVAL_EVENER"
	chmod +x "$LIVE_EVAL_EVENER"
}

live_eval_cleanup() {
	if [ -n "${LIVE_EVAL_ROOT:-}" ]; then
		rm -rf "$LIVE_EVAL_ROOT"
		LIVE_EVAL_ROOT=
	fi
}
