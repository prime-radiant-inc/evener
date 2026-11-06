#!/bin/bash
# Only a disarmed launch/control test. Never grant microphone permission.
set -euo pipefail
if [[ $# == 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  printf '%s\n' 'usage: bash run-simulator-smoke.sh APP NEW_RUN_DIRECTORY [RUNTIME_IDENTIFIER]' \
    'Creates a private simulator, runs disarmed only, then shuts it down. --help is offline and writes nothing.'
  exit 0
fi
if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: bash run-simulator-smoke.sh APP NEW_RUN_DIRECTORY [RUNTIME_IDENTIFIER]" >&2
  exit 2
fi
app=$1
run=$2
runtime=${3:-com.apple.CoreSimulator.SimRuntime.iOS-27-0}
developer_dir=${DEVELOPER_DIR:-$(/usr/bin/xcode-select -p)}
simctl="$developer_dir/usr/bin/simctl"
bundle=dev.evener.experimental.voiceprobe20261104
mkdir "$run"
run=$(cd "$run" && pwd)
mkdir "$run/devices"
device=""
cleanup() {
  if [[ -n "$device" ]]; then
    "$simctl" --set "$run/devices" shutdown "$device"
  fi
}
trap cleanup EXIT
set -x
created=$("$simctl" --set "$run/devices" create VoiceProbe-Disposable com.apple.CoreSimulator.SimDeviceType.iPhone-17 "$runtime")
printf '%s\n' "$created"
device=$(printf '%s\n' "$created" | /usr/bin/awk '/^[0-9A-Fa-f-]+$/ && length($0) == 36 {print}')
[[ -n "$device" && "$device" != *$'\n'* ]]
"$simctl" --set "$run/devices" boot "$device"
/usr/bin/python3 -c 'import subprocess,sys; subprocess.run(sys.argv[1:], check=True, timeout=120)' \
  "$simctl" --set "$run/devices" bootstatus "$device" -b
"$simctl" --set "$run/devices" install "$device" "$app"
/usr/bin/python3 -c 'import subprocess,sys; subprocess.run(sys.argv[1:], check=True, timeout=120)' \
  "$simctl" --set "$run/devices" launch --console "$device" "$bundle" --disarmed-smoke
/usr/bin/python3 - "$run" <<'PY'
import json, pathlib, shutil, sys
root = pathlib.Path(sys.argv[1])
files = list((root / 'devices').rglob('disarmed-smoke.json'))
assert len(files) == 1, files
data = json.loads(files[0].read_text())
assert data['runtime'] == 'ios_simulator', data
assert data['phase'] == 'disarmed' and data['engineRunning'] is False, data
assert data['capture']['frames'] == 0 and data['capture']['buffers'] == 0, data
assert data['generation'] == 1 and data['staleCallbacks'] == 1, data
assert data['scheduled'] == 0 and data['completed'] == 0, data
kinds = [e['kind'] for e in data['events']]
assert 'view_appeared' in kinds and 'disarmed_smoke_passed' in kinds, kinds
shutil.copyfile(files[0], root / 'disarmed-smoke.json')
print('PASS: SwiftUI view entered, stop advanced fence, stale callback rejected, no audio graph or capture')
print('EVIDENCE=' + str(root / 'disarmed-smoke.json'))
PY
# The trap shuts down only this private simulator. Keep run/ until evidence is
# collected, then the caller can remove its exact scratch directory.
