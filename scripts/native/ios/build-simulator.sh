#!/usr/bin/env bash
# Compile the maintained runner; forward cancellation before reclaiming its binary.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
source "$root/scripts/lib/scratch-lib.sh"
child=""
cleanup() {
	if [ -n "$child" ]; then
		kill -TERM "$child" 2>/dev/null || true
		wait "$child" 2>/dev/null || true
	fi
	scratch_rm
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
scratch_dir tool evener-ios-qualification
cd "$root"
go build -o "$tool/qualification" ./scripts/native/ios/qualification
"$tool/qualification" "$@" &
child=$!
status=0
wait "$child" || status=$?
child=""
exit "$status"
