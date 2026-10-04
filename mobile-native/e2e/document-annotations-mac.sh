#!/usr/bin/env bash
# Executed only from an exact committed bundle in the reserved Mac directory.
set -euo pipefail
sha=${1:?source SHA required}
phase=${2:?phase required}
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || exit 2
root=/Users/jesse/tmp/evener-markdown-document-annotations
checkout="$root/checkouts/$sha"
manifest="$root/task-manifest.json"
export PATH="/Users/jesse/.rbenv/versions/3.3.6/bin:/opt/homebrew/bin:$PATH"
cd "$checkout"
test "$(git rev-parse HEAD)" = "$sha"
/usr/bin/python3 - "$manifest" <<'PY'
import json, sys
assert json.load(open(sys.argv[1]))['task'] == 'markdown-document-annotations-task-2'
PY
artifacts="$root/artifacts/$sha"
mkdir -p "$artifacts"
exec > >(tee "$artifacts/$phase.log") 2>&1
printf 'Source SHA: %s\nPhase: %s\n' "$sha" "$phase"
shasum -a 256 mobile-native/patches/react-native-enriched-markdown+1.0.2.patch mobile-native/package-lock.json mobile-native/Podfile.lock "$root/bundles/$sha.bundle"
xcodebuild -version
xcrun simctl list runtimes available
sim_id=$(/usr/bin/python3 - "$manifest" "$sha" <<'PY'
import json, sys
print(json.load(open(sys.argv[1]))['simulators'].get(sys.argv[2], ''))
PY
)
cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$sim_id" ]]; then
    # The ID is solely the result of this task's simctl create, recorded in its
    # manifest. Never select an existing device by name, erase all or kill lanes.
    xcrun simctl shutdown "$sim_id" || true
    if ! xcrun simctl delete "$sim_id" && [[ "$status" == 0 ]]; then status=1; fi
    /usr/bin/python3 - "$manifest" "$sha" <<'PY'
import json, sys
p, sha = sys.argv[1:]
m = json.load(open(p)); m['simulators'].pop(sha, None)
with open(p, 'w') as f: json.dump(m, f, indent=2)
PY
  fi
  printf 'Qualification exit: %s\n' "$status"
  exit "$status"
}
trap cleanup EXIT
case "$phase" in
  cleanup) exit 0;;
  layout)
    test -z "$sim_id"
    test ! -L mobile-native/node_modules
    test ! -L appwire-client/typescript/node_modules
    npm ci --prefix mobile-native
    npm ci --prefix appwire-client/typescript
    cd mobile-native
    npx expo prebuild --platform ios --no-install
    # Save actual generated phases. ENTRY_FILE must remain caller-owned.
    /usr/bin/plutil -convert json -o "$artifacts/generated-project.json" ios/Evener.xcodeproj/project.pbxproj
    /usr/bin/python3 - "$artifacts/generated-project.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
scripts = [o['shellScript'] for o in p['objects'].values() if 'shellScript' in o]
for s in scripts:
    if 'react-native-xcode.sh' in s:
        print('Actual generated bundle phase:\n' + s)
        assert 'if [[ -z "$ENTRY_FILE" ]]' in s, 'Generated phase must honor explicit ENTRY_FILE'
        break
else: raise RuntimeError('No generated React Native bundle phase')
PY
    cat ios/Podfile
    cp Podfile.lock ios/Podfile.lock
    bundle _2.7.2_ install
    bundle _2.7.2_ exec pod install --deployment --project-directory=ios
    cmp Podfile.lock ios/Podfile.lock
    sim_id=$(xcrun simctl create Evener-Document-Annotations \
      com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro \
      com.apple.CoreSimulator.SimRuntime.iOS-26-5)
    /usr/bin/python3 - "$manifest" "$sha" "$sim_id" <<'PY'
import json, sys
p, sha, sim = sys.argv[1:]
m = json.load(open(p)); assert sha not in m['simulators']; m['simulators'][sha] = sim
with open(p, 'w') as f: json.dump(m, f, indent=2)
PY
    printf 'Created simulator: %s\n' "$sim_id"
    xcrun simctl boot "$sim_id"
    xcrun simctl bootstatus "$sim_id" -b
    xcodebuild -workspace ios/Evener.xcworkspace -scheme Evener \
      -configuration Release -sdk iphonesimulator -destination "id=$sim_id" \
      -derivedDataPath ../annotation-derived-data \
      "ENTRY_FILE=$checkout/mobile-native/e2e/document-annotations-probe.tsx" -jobs 4 build
    app="$checkout/annotation-derived-data/Build/Products/Release-iphonesimulator/Evener.app"
    identity=$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Info.plist")
    test "$identity" = com.primeradiant.evener.native
    printf 'Built app identity: %s\n' "$identity"
    # A sorted file-and-digest inventory identifies the complete installed app.
    /usr/bin/python3 - "$app" "$artifacts/app-inventory.json" <<'PY'
import hashlib, json, os, sys
root, out = sys.argv[1:]
files = []
for base, dirs, names in os.walk(root):
    for name in names:
        p = os.path.join(base, name)
        if os.path.isfile(p):
            with open(p, 'rb') as f: digest = hashlib.sha256(f.read()).hexdigest()
            files.append({'path': os.path.relpath(p, root), 'sha256': digest})
with open(out, 'w') as f: json.dump(sorted(files, key=lambda r: r['path']), f, indent=2)
PY
    shasum -a 256 "$artifacts/app-inventory.json" "$app/Evener" "$app/main.jsbundle"
    xcrun simctl install "$sim_id" "$app"
    installed=$(xcrun simctl get_app_container "$sim_id" com.primeradiant.evener.native app)
    cmp "$app/Evener" "$installed/Evener"
    cmp "$app/main.jsbundle" "$installed/main.jsbundle"
    xcrun simctl launch "$sim_id" com.primeradiant.evener.native
    cd e2e/document-annotations-ui
    xcodegen generate
    xcodebuild -project DocumentAnnotationsUI.xcodeproj -scheme DocumentAnnotationsUI \
      -configuration Release -sdk iphonesimulator -destination "id=$sim_id" \
      -derivedDataPath "$checkout/annotation-ui-derived-data" -jobs 2 build-for-testing
    ;;
  long-press|qualify) test -n "$sim_id";;
  *) exit 2;;
esac
xctestrun=$(/usr/bin/python3 - "$checkout/annotation-ui-derived-data/Build/Products" <<'PY'
import glob, sys
files = glob.glob(sys.argv[1]+'/*.xctestrun'); assert len(files) == 1, files
print(files[0])
PY
)
case "$phase" in
  layout) test_name=testLayoutReceipt;;
  long-press|qualify)
    /usr/bin/python3 - "$xctestrun" "$checkout/mobile-native/e2e/measured-geometry.json" <<'PY'
import json, math, plistlib, sys
p, receipt = sys.argv[1:]
m = json.load(open(receipt))
env = {}
for name, point in m.items():
    assert name in ('FIRST', 'HANDLE', 'SECOND')
    for axis in ('x', 'y'):
        value = point[axis]; assert isinstance(value, (int, float)) and math.isfinite(value) and value >= 0
        env[f'ANNOTATION_{name}_{axis.upper()}'] = str(value)
with open(p, 'rb') as f: run = plistlib.load(f)
for c in run['TestConfigurations']:
    for t in c['TestTargets']: t.setdefault('EnvironmentVariables', {}).update(env)
with open(p, 'wb') as f: plistlib.dump(run, f)
PY
    if [[ "$phase" == long-press ]]; then test_name=testLongPressReceipt
    else test_name=testCompletedOriginalSourceSelectionAcrossRows; fi;;
esac
result="$artifacts/$phase.xcresult"
test ! -e "$result"
xcodebuild test-without-building -xctestrun "$xctestrun" \
  -destination "id=$sim_id" -parallel-testing-enabled NO \
  -only-testing:"DocumentAnnotationsUITests/DocumentAnnotationsUITests/$test_name" \
  -resultBundlePath "$result"
if [[ "$phase" != qualify ]]; then
  # Keep this exact newly created simulator while the operator measures its
  # screenshot. The next phase or explicit cleanup owns its teardown.
  trap - EXIT
  printf 'Awaiting measured geometry on task simulator %s, cleanup required\n' "$sim_id"
fi
