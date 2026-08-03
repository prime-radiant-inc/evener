#!/usr/bin/env bash
set -euo pipefail

export PATH="/opt/homebrew/bin:$PATH"
ROOT=$(git rev-parse --show-toplevel)
FRONTEND="$ROOT/cmd/serf-hub/frontend"
EVIDENCE="$ROOT/.superpowers/sdd/2026-08-02-compact-input-footer/task-4-browser-evidence"
OUTPUT_DIR="${EVIDENCE_OUTPUT_DIR:-$EVIDENCE}"
RUN_LABEL="${EVIDENCE_RUN_LABEL:-current}"
RUN_ROOT="$SERF_SCRATCH_DIR/task-4-browser-run-$RUN_LABEL"
ORIGINAL_CDP="$RUN_ROOT/cdp.original.mjs"
SHIM_DYLIB="$RUN_ROOT/iokit-runloop-shim.dylib"
OVERFLOW_DIST="$RUN_ROOT/overflow-dist"
TRANSIENT_VITE_CONFIG="$FRONTEND/.task-4-overflow-vite.config.mjs"
CHROME="$HOME/Library/Caches/ms-playwright/chromium_headless_shell-1217/chrome-headless-shell-mac-arm64/chrome-headless-shell"
WIDTHS=(320 360 400 479 480 559 560 900)

mkdir -p "$RUN_ROOT"
mkdir -p "$OUTPUT_DIR"
rm -rf "$OVERFLOW_DIST"
rm -f \
  "$OUTPUT_DIR/layoutguard.log" \
  "$OUTPUT_DIR/layoutguard-measurements.jsonl" \
  "$OUTPUT_DIR/overflow-build.log" \
  "$OUTPUT_DIR/overflowguard.log" \
  "$OUTPUT_DIR/overflow-cdp-measurements.jsonl" \
  "$OUTPUT_DIR/overflow-measurements.jsonl" \
  "$OUTPUT_DIR/chrome-stderr.log" \
  "$OUTPUT_DIR/environment.log" \
  "$OUTPUT_DIR/cleanup.log"

if [[ "$OUTPUT_DIR" != "$EVIDENCE" ]]; then
  cp "$EVIDENCE/cdp-pipe.mjs" "$OUTPUT_DIR/cdp-pipe.mjs"
  cp "$EVIDENCE/viewport.mjs" "$OUTPUT_DIR/viewport.mjs"
  cp "$EVIDENCE/iokit-runloop-shim.c" "$OUTPUT_DIR/iokit-runloop-shim.c"
  cp "$EVIDENCE/overflow-pipe-run.mjs" "$OUTPUT_DIR/overflow-pipe-run.mjs"
  cp "$EVIDENCE/vite-overflow.config.mjs" "$OUTPUT_DIR/vite-overflow.config.mjs"
  cp "$EVIDENCE/run-browser-verification.sh" "$OUTPUT_DIR/run-browser-verification.sh"
fi

cp "$FRONTEND/scripts/layoutguard/cdp.mjs" "$ORIGINAL_CDP"

cleanup() {
  local rc=$?
  cp "$ORIGINAL_CDP" "$FRONTEND/scripts/layoutguard/cdp.mjs"
  rm -f "$TRANSIENT_VITE_CONFIG"
  rm -rf "$RUN_ROOT"
  {
    printf 'SCRIPT_EXIT=%s\n' "$rc"
    printf 'REPOSITORY_CDP_SHA256='
    shasum -a 256 "$FRONTEND/scripts/layoutguard/cdp.mjs" | awk '{print $1}'
    printf 'SAVED_ORIGINAL_CDP_SHA256='
    shasum -a 256 "$ORIGINAL_CDP" 2>/dev/null | awk '{print $1}' || printf 'removed-after-restore\n'
    if [[ -e "$TRANSIENT_VITE_CONFIG" ]]; then
      printf 'TRANSIENT_VITE_CONFIG_REMOVED=0\n'
    else
      printf 'TRANSIENT_VITE_CONFIG_REMOVED=1\n'
    fi
    if [[ -e "$RUN_ROOT" ]]; then
      printf 'TRANSIENT_RUN_ROOT_REMOVED=0\n'
    else
      printf 'TRANSIENT_RUN_ROOT_REMOVED=1\n'
    fi
  } >> "$OUTPUT_DIR/cleanup.log"
  exit "$rc"
}
trap cleanup EXIT INT TERM

{
  printf 'DATE_UTC='; date -u '+%Y-%m-%dT%H:%M:%SZ'
  printf 'ROOT=%s\n' "$ROOT"
  printf 'FRONTEND=%s\n' "$FRONTEND"
  printf 'EVIDENCE=%s\n' "$EVIDENCE"
  printf 'OUTPUT_DIR=%s\n' "$OUTPUT_DIR"
  printf 'RUN_LABEL=%s\n' "$RUN_LABEL"
  printf 'RUN_ROOT=%s\n' "$RUN_ROOT"
  printf 'CHROME=%s\n' "$CHROME"
  printf 'WIDTHS=%s\n' "${WIDTHS[*]}"
  printf 'NODE='; node --version
  printf 'NPM='; npm --version
  printf 'CHROME_VERSION='; "$CHROME" --version
  printf 'HEAD='; git -C "$ROOT" rev-parse HEAD
  printf 'WORKTREE_STATUS_BEFORE_BEGIN\n'; git -C "$ROOT" status --short; printf 'WORKTREE_STATUS_BEFORE_END\n'
  printf 'PIPE_DRIVER_SHA256='; shasum -a 256 "$EVIDENCE/cdp-pipe.mjs" | awk '{print $1}'
  printf 'OVERFLOW_ADAPTER_SHA256='; shasum -a 256 "$EVIDENCE/overflow-pipe-run.mjs" | awk '{print $1}'
  printf 'INTERPOSER_SOURCE_SHA256='; shasum -a 256 "$EVIDENCE/iokit-runloop-shim.c" | awk '{print $1}'
  printf 'VITE_CONFIG_SHA256='; shasum -a 256 "$EVIDENCE/vite-overflow.config.mjs" | awk '{print $1}'
} > "$OUTPUT_DIR/environment.log" 2>&1

clang -dynamiclib -framework CoreFoundation -framework IOKit \
  "$EVIDENCE/iokit-runloop-shim.c" -o "$SHIM_DYLIB"

cp "$EVIDENCE/cdp-pipe.mjs" "$FRONTEND/scripts/layoutguard/cdp.mjs"

(
  cd "$FRONTEND"
  SERF_HEADLESS_CHROME="$CHROME" \
  SERF_IOKIT_SHIM="$SHIM_DYLIB" \
  SERF_CHROME_PROFILE_ROOT="$RUN_ROOT" \
  SERF_CHROME_STDERR_LOG="$OUTPUT_DIR/chrome-stderr.log" \
  SERF_CDP_MEASUREMENTS="$OUTPUT_DIR/layoutguard-measurements.jsonl" \
  npm run layoutguard
) 2>&1 | tee "$OUTPUT_DIR/layoutguard.log"
printf 'LAYOUTGUARD_EXIT=0\n' | tee -a "$OUTPUT_DIR/layoutguard.log"

cp "$EVIDENCE/vite-overflow.config.mjs" "$TRANSIENT_VITE_CONFIG"
(
  cd "$FRONTEND"
  OVERFLOW_FRONTEND="$FRONTEND" \
  OVERFLOW_DIST="$OVERFLOW_DIST" \
  ./node_modules/.bin/vite build --config "$TRANSIENT_VITE_CONFIG"
) 2>&1 | tee "$OUTPUT_DIR/overflow-build.log"
printf 'OVERFLOW_BUILD_EXIT=0\n' | tee -a "$OUTPUT_DIR/overflow-build.log"

test -f "$OVERFLOW_DIST/overflowharness.html"

SERF_HEADLESS_CHROME="$CHROME" \
SERF_IOKIT_SHIM="$SHIM_DYLIB" \
SERF_CHROME_PROFILE_ROOT="$RUN_ROOT" \
SERF_CHROME_STDERR_LOG="$OUTPUT_DIR/chrome-stderr.log" \
SERF_CDP_MEASUREMENTS="$OUTPUT_DIR/overflow-cdp-measurements.jsonl" \
SERF_OVERFLOW_MEASUREMENTS="$OUTPUT_DIR/overflow-measurements.jsonl" \
OVERFLOW_HARNESS="$OVERFLOW_DIST/overflowharness.html" \
node "$EVIDENCE/overflow-pipe-run.mjs" "${WIDTHS[@]}" 2>&1 | tee "$OUTPUT_DIR/overflowguard.log" && overflow_rc=0 || overflow_rc=${PIPESTATUS[0]}
printf 'OVERFLOWGUARD_EXIT=%s\n' "$overflow_rc" | tee -a "$OUTPUT_DIR/overflowguard.log"

printf 'WORKTREE_STATUS_BEFORE_CLEANUP_BEGIN\n' >> "$OUTPUT_DIR/cleanup.log"
git -C "$ROOT" status --short >> "$OUTPUT_DIR/cleanup.log"
printf 'WORKTREE_STATUS_BEFORE_CLEANUP_END\n' >> "$OUTPUT_DIR/cleanup.log"

exit "$overflow_rc"
