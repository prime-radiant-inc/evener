#!/usr/bin/env bash
# Executed only from an exact committed bundle in the reserved Mac directory.
set -euo pipefail
sha=${1:?source SHA required}
phase=${2:?phase required}
harness_sha=${3:-$sha}
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || exit 2
[[ "$harness_sha" =~ ^[0-9a-f]{40}$ ]] || exit 2
root=/Users/jesse/tmp/evener-markdown-document-annotations
checkout="$root/checkouts/$sha"
ui_derived="$checkout/annotation-ui-derived-data-$harness_sha"
manifest="$root/task-manifest.json"
export PATH="/Users/jesse/.rbenv/versions/3.3.6/bin:/opt/homebrew/bin:$PATH"
cd "$checkout"
test "$(git rev-parse HEAD)" = "$sha"
test "$(git -C "$root/checkouts/$harness_sha" rev-parse HEAD)" = "$harness_sha"
/usr/bin/python3 - "$manifest" <<'PY'
import json, sys
assert json.load(open(sys.argv[1]))['task'] == 'markdown-document-annotations-task-2'
PY
source_artifacts="$root/artifacts/$sha"
artifacts="$source_artifacts/$phase-$harness_sha"
test ! -e "$artifacts"
mkdir -p "$artifacts"
exec > >(tee "$artifacts/$phase.log") 2>&1
printf 'Source SHA: %s\nHarness SHA: %s\nPhase: %s\n' "$sha" "$harness_sha" "$phase"
shasum -a 256 mobile-native/patches/react-native-enriched-markdown+1.0.2.patch mobile-native/package-lock.json mobile-native/Podfile.lock "$root/bundles/$harness_sha.bundle"
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
create_simulator() {
  test -z "$sim_id"
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
}
case "$phase" in
  cleanup) exit 0;;
  model)
    test -z "$sim_id"
    test ! -L mobile-native/node_modules
    npm ci --prefix mobile-native
    bash "$root/checkouts/$harness_sha/mobile-native/e2e/document-annotations-model.sh" "$artifacts" "$checkout"
    exit 0;;
  layout)
    test -z "$sim_id"
    test ! -L mobile-native/node_modules
    test ! -L appwire-client/typescript/node_modules
    npm ci --prefix mobile-native
    npm ci --prefix appwire-client/typescript
    node <<'JS'
const fs = require('node:fs');
const root = 'mobile-native/node_modules/react-native-enriched-markdown';
if (fs.existsSync(root + '/src/native/EnrichedMarkdownCodeText.tsx')) {
  const pkg = JSON.parse(fs.readFileSync(root + '/package.json', 'utf8'));
  if (pkg.codegenConfig.ios.components.EnrichedMarkdownCodeText?.className !== 'EnrichedMarkdownCodeText') {
    throw new Error('Reapplied patch lost native code-leaf class registration');
  }
  const props = fs.readFileSync(root + '/ios/generated/ReactCodegen/EnrichedMarkdownTextSpec/Props.h', 'utf8');
  for (const field of ['EnrichedMarkdownCodeTextProps', 'documentSnapshotId', 'blockSourceRange', 'fontSize', 'lineHeight']) {
    if (!props.includes(field)) throw new Error('Reapplied patch lost generated field: ' + field);
  }
  console.log('Reapplied code-leaf class registration and generated Fabric props verified');
}
JS
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
    create_simulator
    xcodebuild -workspace ios/Evener.xcworkspace -scheme Evener \
      -configuration Release -sdk iphonesimulator -destination "id=$sim_id" \
      -derivedDataPath ../annotation-derived-data \
      "ENTRY_FILE=$checkout/mobile-native/e2e/document-annotations-probe.tsx" -jobs 4 build
    app="$checkout/annotation-derived-data/Build/Products/Release-iphonesimulator/Evener.app"
    identity=$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Info.plist")
    test "$identity" = com.primeradiant.evener.native
    printf 'Built app identity: %s\n' "$identity"
    # A sorted file-and-digest inventory identifies the complete installed app.
    /usr/bin/python3 - "$app" "$source_artifacts/app-inventory.json" <<'PY'
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
    shasum -a 256 "$source_artifacts/app-inventory.json" "$app/Evener" "$app/main.jsbundle"
    xcrun simctl install "$sim_id" "$app"
    installed=$(xcrun simctl get_app_container "$sim_id" com.primeradiant.evener.native app)
    cmp "$app/Evener" "$installed/Evener"
    cmp "$app/main.jsbundle" "$installed/main.jsbundle"
    xcrun simctl launch "$sim_id" com.primeradiant.evener.native
    ;;
  restore)
    /usr/bin/python3 - "$checkout" "$sha" "$source_artifacts/retained-baseline-digests.json" <<'PY'
import hashlib, json, os, sys
root, sha, path = sys.argv[1:]
receipt = json.load(open(path)); assert receipt['sourceSHA'] == sha
for relative, expected in receipt['files'].items():
    with open(os.path.join(root, relative), 'rb') as f: actual = hashlib.sha256(f.read()).hexdigest()
    assert actual == expected, relative
print('Retained baseline probe, native executable, JS and compiled XCTest driver digests verified')
PY
    app="$checkout/annotation-derived-data/Build/Products/Release-iphonesimulator/Evener.app"
    /usr/bin/python3 - "$app" "$source_artifacts/app-inventory.json" <<'PY'
import hashlib, json, os, sys
root, inventory = sys.argv[1:]
for row in json.load(open(inventory)):
    with open(os.path.join(root, row['path']), 'rb') as f: digest = hashlib.sha256(f.read()).hexdigest()
    assert digest == row['sha256'], row['path']
print('Retained baseline app bytes match the original build inventory')
PY
    test "$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Info.plist")" = com.primeradiant.evener.native
    create_simulator
    xcrun simctl install "$sim_id" "$app"
    installed=$(xcrun simctl get_app_container "$sim_id" com.primeradiant.evener.native app)
    cmp "$app/Evener" "$installed/Evener"
    cmp "$app/main.jsbundle" "$installed/main.jsbundle"
    ;;
  font-diagnostic|empty-diagnostic)
    # Reinstall this source's exact retained app for supported metadata
    # inspection. No rebuild, selection assertions or GREEN claim follows.
    if [[ "$phase" == font-diagnostic ]]; then test -z "$sim_id"; fi
    if [[ -z "$sim_id" ]]; then
      app="$checkout/annotation-derived-data/Build/Products/Release-iphonesimulator/Evener.app"
      /usr/bin/python3 - "$app" "$source_artifacts/app-inventory.json" <<'PY'
import hashlib, json, os, sys
root, inventory = sys.argv[1:]
for row in json.load(open(inventory)):
    with open(os.path.join(root, row['path']), 'rb') as f: digest = hashlib.sha256(f.read()).hexdigest()
    assert digest == row['sha256'], row['path']
print('Diagnostic app bytes match the retained source inventory')
PY
      test "$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Info.plist")" = com.primeradiant.evener.native
      create_simulator
      xcrun simctl install "$sim_id" "$app"
      installed=$(xcrun simctl get_app_container "$sim_id" com.primeradiant.evener.native app)
      cmp "$app/Evener" "$installed/Evener"
      cmp "$app/main.jsbundle" "$installed/main.jsbundle"
      if [[ "$phase" == empty-diagnostic ]]; then
        xcrun simctl ui "$sim_id" content_size extra-extra-large
        test "$(xcrun simctl ui "$sim_id" content_size)" = extra-extra-large
      fi
    fi
    ;;
  code-preservation-scaled)
    test -n "$sim_id"
    xcrun simctl ui "$sim_id" content_size extra-extra-large
    test "$(xcrun simctl ui "$sim_id" content_size)" = extra-extra-large
    printf 'Owned simulator system content size: extra-extra-large\n'
    ;;
  long-press|code-preservation|qualify) test -n "$sim_id";;
  *) exit 2;;
esac
# Source and driver have separate pins. Never compile driver sources from the
# app checkout when a different committed harness was explicitly requested.
if [[ ! -d "$ui_derived/Build/Products" ]]; then
  cd "$root/checkouts/$harness_sha/mobile-native/e2e/document-annotations-ui"
  xcodegen generate
  xcodebuild -project DocumentAnnotationsUI.xcodeproj -scheme DocumentAnnotationsUI \
    -configuration Release -sdk iphonesimulator -destination "id=$sim_id" \
    -derivedDataPath "$ui_derived" -jobs 2 build-for-testing
fi
xctestrun=$(/usr/bin/python3 - "$ui_derived/Build/Products" <<'PY'
import glob, sys
files = glob.glob(sys.argv[1]+'/DocumentAnnotationsUI_*.xctestrun'); assert len(files) == 1, files
print(files[0])
PY
)
# Keep the generated input untouched. The run copy stays beside it so Xcode's
# __TESTROOT__ still resolves to the compiled products rather than evidence.
cp "$xctestrun" "$artifacts/original.xctestrun"
run_xctestrun="$(dirname "$xctestrun")/annotation-$phase-$harness_sha.xctestrun"
test ! -e "$run_xctestrun"
cp "$xctestrun" "$run_xctestrun"
cmp "$xctestrun" "$artifacts/original.xctestrun"
if [[ "$phase" == code-preservation-scaled ]]; then
  /usr/bin/python3 - "$run_xctestrun" <<'PY'
import plistlib, sys
p = sys.argv[1]
with open(p, 'rb') as f: run = plistlib.load(f)
assert run['__xctestrun_metadata__']['FormatVersion'] == 1
run['DocumentAnnotationsUITests'].setdefault('EnvironmentVariables', {})['ANNOTATION_REQUIRE_SCALED_CODE'] = '1'
with open(p, 'wb') as f: plistlib.dump(run, f)
PY
fi
case "$phase" in
  layout|restore) test_names=(testLayoutReceipt);;
  code-preservation|code-preservation-scaled) test_names=(testCodePreservationAndLegacyCopy);;
  font-diagnostic) test_names=(testMixedFontMetadataReceipt);;
  empty-diagnostic) test_names=(testEmptyCodeGeometryReceipt);;
  long-press|qualify)
    /usr/bin/python3 - "$run_xctestrun" "$checkout/mobile-native/e2e/measured-geometry.json" <<'PY'
import json, math, plistlib, sys
p, receipt = sys.argv[1:]
m = json.load(open(receipt))
env = {}
for name, point in m.items():
    assert name in ('FIRST', 'HANDLE', 'SECOND', 'TABLE', 'CODE', 'FIRST_TO_CODE',
                    'TABLE_HANDLE', 'CODE_HANDLE', 'CODE_TO_FIRST', 'TABLE_TO_CODE', 'CODE_TO_TABLE')
    for axis in ('x', 'y'):
        value = point[axis]; assert isinstance(value, (int, float)) and math.isfinite(value) and value >= 0
        env[f'ANNOTATION_{name}_{axis.upper()}'] = str(value)
with open(p, 'rb') as f: run = plistlib.load(f)
assert run['__xctestrun_metadata__']['FormatVersion'] == 1
run['DocumentAnnotationsUITests'].setdefault('EnvironmentVariables', {}).update(env)
with open(p, 'wb') as f: plistlib.dump(run, f)
PY
    if [[ "$phase" == long-press ]]; then test_names=(testLongPressReceipt)
    else
      test_names=(testCompletedOriginalSourceSelectionAcrossRows
        testTableOriginalSourceSelection testMarkdownCodeOriginalSourceSelection
        testProseToCodeBoundary testCodeToProseBoundary testTableToCodeBoundary
        testCodeToTableBoundary testCodePreservationAndLegacyCopy)
    fi;;
esac
result="$artifacts/$phase.xcresult"
test ! -e "$result"
test_args=()
for test_name in "${test_names[@]}"; do
  test_args+=("-only-testing:DocumentAnnotationsUITests/DocumentAnnotationsUITests/$test_name")
done
test_status=0
xcodebuild test-without-building -xctestrun "$run_xctestrun" \
  -destination "id=$sim_id" -parallel-testing-enabled NO \
  "${test_args[@]}" \
  -resultBundlePath "$result" || test_status=$?
if ! cmp "$xctestrun" "$artifacts/original.xctestrun"; then
  echo 'Original generated xctestrun changed during the run' >&2
  if [[ "$test_status" == 0 ]]; then test_status=1; fi
fi
runner="$ui_derived/Build/Products/Release-iphonesimulator/DocumentAnnotationsUITests-Runner.app"
installed_runner=$(xcrun simctl get_app_container "$sim_id" com.primeradiant.evener.native.annotation-tests.xctrunner app)
if ! cmp "$runner/DocumentAnnotationsUITests-Runner" "$installed_runner/DocumentAnnotationsUITests-Runner" ||
   ! cmp "$runner/PlugIns/DocumentAnnotationsUITests.xctest/DocumentAnnotationsUITests" \
     "$installed_runner/PlugIns/DocumentAnnotationsUITests.xctest/DocumentAnnotationsUITests"; then
  echo 'Installed XCTest bytes differ from the pinned driver' >&2
  if [[ "$test_status" == 0 ]]; then test_status=1; fi
fi
shasum -a 256 "$artifacts/original.xctestrun" "$run_xctestrun" \
  "$runner/DocumentAnnotationsUITests-Runner" \
  "$runner/PlugIns/DocumentAnnotationsUITests.xctest/DocumentAnnotationsUITests"
if [[ "$test_status" != 0 ]]; then exit "$test_status"; fi
if [[ "$phase" != qualify && "$phase" != font-diagnostic && "$phase" != empty-diagnostic ]]; then
  # Keep this exact newly created simulator while the operator measures its
  # screenshot. The next phase or explicit cleanup owns its teardown.
  trap - EXIT
  printf 'Awaiting measured geometry on task simulator %s, cleanup required\n' "$sim_id"
fi
