#!/usr/bin/env bash
# Hand-run qualification glue, never part of the deterministic native gate.
set -euo pipefail
host=jesses-macbook-pro-2-1
ref=HEAD
harness_ref=HEAD
keep=0
phase=layout
geometry=
usage() {
  cat <<'HELP'
Usage: e2e-native-document-annotations.sh [--host HOST] [--source-ref REF]
       [--harness-ref REF] [--keep-artifacts]
       [--phase layout|restore|long-press|qualify|cleanup] [--geometry FILE]

Transfers an exact committed Git bundle into the reserved Mac directory.
Source and harness refs default to committed HEAD, and are recorded separately.
layout builds the qualification-only Release entry and saves XCTest geometry.
restore reinstalls that source's retained verified app without rebuilding it,
creates a new task simulator and records fresh geometry on that exact device.
long-press uses measured FIRST coordinates and saves the real handle receipt.
qualify uses measured FIRST/HANDLE/SECOND coordinates and runs source assertions.
Geometry JSON: {"FIRST":{"x":0,"y":0},"HANDLE":{"x":0,"y":0},"SECOND":{"x":0,"y":0}}
Coordinates must come from this simulator's actual layout/screenshot receipt.
layout/long-press retain only the new simulator for the next stage. Always run
cleanup if qualification cannot continue. qualify cleans that simulator on exit.
--keep-artifacts retains the local transfer bundle. Remote build/test evidence
is retained in the manifest-owned directory on success and failure.
HELP
}
while (($#)); do
  case "$1" in
    --host|--source-ref|--harness-ref|--phase|--geometry)
      (($# >= 2)) || { usage >&2; exit 2; }
      case "$1" in
        --host) host=$2;; --source-ref) ref=$2;; --harness-ref) harness_ref=$2;;
        --phase) phase=$2;; --geometry) geometry=$2;;
      esac
      shift 2;;
    --keep-artifacts) keep=1; shift;;
    --help|-h) usage; exit 0;;
    *) usage >&2; exit 2;;
  esac
done
case "$host" in -*|*[!a-zA-Z0-9@._-]*) echo 'Invalid SSH host' >&2; exit 2;; esac
case "$phase" in layout|restore|long-press|qualify|cleanup) ;; *) usage >&2; exit 2;; esac
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
sha=$(git rev-parse --verify "$ref^{commit}")
harness_sha=$(git rev-parse --verify "$harness_ref^{commit}")
ssh_options=(-o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=yes)
ssh "${ssh_options[@]}" "$host" 'xcodebuild -version; xcrun simctl list runtimes available'
# Inspect before any writes. An existing directory without our manifest is refused.
ssh "${ssh_options[@]}" "$host" 'if test -e /Users/jesse/tmp/evener-markdown-document-annotations; then
  ls -la /Users/jesse/tmp/evener-markdown-document-annotations
  test -f /Users/jesse/tmp/evener-markdown-document-annotations/task-manifest.json || exit 1
  cat /Users/jesse/tmp/evener-markdown-document-annotations/task-manifest.json
  /usr/bin/python3 -c '\''import json; p="/Users/jesse/tmp/evener-markdown-document-annotations/task-manifest.json"; assert json.load(open(p))["task"] == "markdown-document-annotations-task-2"'\''
fi'
scratch=${EVENER_SCRATCH_DIR:?EVENER_SCRATCH_DIR is required}
bundle="$scratch/document-annotations-$harness_sha.bundle"
if ! ssh "${ssh_options[@]}" "$host" "test -d /Users/jesse/tmp/evener-markdown-document-annotations/checkouts/$harness_sha"; then
  # HEAD is also included so a SHA-valued source-ref has a named bundle head.
  # The remote checkout always detaches at the verified exact requested SHA.
  git bundle create "$bundle" HEAD "$ref" "$harness_ref"
  git bundle verify "$bundle"
  sha256sum "$bundle"
  ssh "${ssh_options[@]}" "$host" 'set -eu
    if ! test -e /Users/jesse/tmp/evener-markdown-document-annotations; then
      umask 077
      mkdir /Users/jesse/tmp/evener-markdown-document-annotations
      printf '\''{"task":"markdown-document-annotations-task-2","simulators":{}}\n'\'' > /Users/jesse/tmp/evener-markdown-document-annotations/task-manifest.json
    fi
    mkdir -p /Users/jesse/tmp/evener-markdown-document-annotations/bundles /Users/jesse/tmp/evener-markdown-document-annotations/checkouts'
  scp "${ssh_options[@]}" "$bundle" "$host:/Users/jesse/tmp/evener-markdown-document-annotations/bundles/$harness_sha.bundle"
  ssh "${ssh_options[@]}" "$host" "set -eu
    cd /Users/jesse/tmp/evener-markdown-document-annotations
    test ! -e checkouts/$harness_sha
    git clone bundles/$harness_sha.bundle checkouts/$harness_sha
    git -C checkouts/$harness_sha checkout --detach $harness_sha"
fi
ssh "${ssh_options[@]}" "$host" "set -eu
  cd /Users/jesse/tmp/evener-markdown-document-annotations
  test \"\$(git -C checkouts/$harness_sha rev-parse HEAD)\" = $harness_sha
  if ! test -d checkouts/$sha; then
    git clone bundles/$harness_sha.bundle checkouts/$sha
    git -C checkouts/$sha checkout --detach $sha
  fi
  test \"\$(git -C checkouts/$sha rev-parse HEAD)\" = $sha"
if [[ -n "$geometry" ]]; then
  test -f "$geometry"
  scp "${ssh_options[@]}" "$geometry" "$host:/Users/jesse/tmp/evener-markdown-document-annotations/checkouts/$sha/mobile-native/e2e/measured-geometry.json"
fi
set +e
ssh "${ssh_options[@]}" "$host" "bash /Users/jesse/tmp/evener-markdown-document-annotations/checkouts/$harness_sha/mobile-native/e2e/document-annotations-mac.sh $sha $phase $harness_sha"
status=$?
set -e
if [[ "$keep" == 0 && -f "$bundle" ]]; then rm -- "$bundle"; fi
exit "$status"
