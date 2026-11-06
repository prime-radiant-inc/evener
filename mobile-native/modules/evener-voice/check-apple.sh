#!/bin/bash
# Real Apple SDK / ONNX link smoke. No app generation, signing, install or upload.
set -euo pipefail
if [[ $# != 2 ]]; then
  echo 'usage: bash check-apple.sh EXTRACTED_ONNX_RUNTIME_1_20_DIRECTORY NEW_OUTPUT_DIRECTORY' >&2
  exit 2
fi
source_dir=$(cd "$(dirname "$0")" && pwd)
runtime=$(cd "$1" && pwd)
mkdir "$2"
output=$(cd "$2" && pwd)
test -f "$source_dir/resources/silero_vad.onnx"
# The runtime directory contains the published Objective-C pod archive plus the
# C pod archive: objectivec/, Headers/, onnxruntime.xcframework/.
mkdir "$output/include"
cp "$runtime/objectivec/include/"*.h "$output/include/"
printf 'module onnxruntime_objc { umbrella header "onnxruntime.h" export * }\n' > "$output/include/module.modulemap"
for platform in simulator device macos; do
  case "$platform" in
    simulator) sdk_name=iphonesimulator; target=arm64-apple-ios16.4-simulator; slice=ios-arm64_x86_64-simulator ;;
    device) sdk_name=iphoneos; target=arm64-apple-ios16.4; slice=ios-arm64 ;;
    macos) sdk_name=macosx; target=arm64-apple-macos12.0; slice=macos-arm64_x86_64 ;;
  esac
  sdk=$(xcrun --sdk "$sdk_name" --show-sdk-path)
  framework="$runtime/onnxruntime.xcframework/$slice"
  mkdir "$output/$platform"
  for source in "$runtime/objectivec/"*.mm; do
    xcrun --sdk "$sdk_name" clang++ -c -std=c++17 -fobjc-arc -target "$target" -isysroot "$sdk" \
      -I "$runtime/objectivec/include" -I "$runtime/Headers" -F "$framework" \
      "$source" -o "$output/$platform/$(basename "$source").o"
  done
  if [[ "$platform" == macos ]]; then
    mkdir "$output/$platform/EvenerVoiceModels.bundle"
    cp "$source_dir/resources/"*.onnx "$source_dir/resources/wake-models.json" "$output/$platform/EvenerVoiceModels.bundle/"
    printf '%s\n' '<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.primeradiant.evener.voice-smoke-model</string></dict></plist>' > "$output/$platform/EvenerVoiceModels.bundle/Info.plist"
    SDKROOT="$sdk" xcrun --sdk "$sdk_name" swiftc -swift-version 5 -parse-as-library -target "$target" -sdk "$sdk" \
      -I "$output/include" -F "$framework" -framework onnxruntime -lc++ \
      "$source_dir/core/VoiceCore.swift" "$source_dir/ios/SileroVAD.swift" "$source_dir/runtime-tests/VADSmoke.swift" \
      "$output/$platform/"*.o -o "$output/$platform/VADSmoke"
    "$output/$platform/VADSmoke"
    test_frameworks="$(xcrun --sdk macosx --show-sdk-platform-path)/Developer/Library/Frameworks"
    test_libs="$(xcrun --sdk macosx --show-sdk-platform-path)/Developer/usr/lib"
    test_bundle="$output/$platform/WakeWordTests.xctest"
    mkdir -p "$test_bundle/Contents/MacOS" "$test_bundle/Contents/Resources"
    cp -R "$output/$platform/EvenerVoiceModels.bundle" "$test_bundle/Contents/Resources/"
    printf '%s\n' '<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.primeradiant.evener.wake-tests</string><key>CFBundleExecutable</key><string>WakeWordTests</string><key>CFBundlePackageType</key><string>BNDL</string></dict></plist>' > "$test_bundle/Contents/Info.plist"
    SDKROOT="$sdk" xcrun --sdk "$sdk_name" swiftc -swift-version 5 -emit-library -module-name EvenerWakeTests -target "$target" -sdk "$sdk" \
      -I "$output/include" -F "$framework" -framework onnxruntime -lc++ \
      -I "$test_libs" -L "$test_libs" -Xlinker -rpath -Xlinker "$test_libs" \
      -F "$test_frameworks" -framework XCTest -Xlinker -rpath -Xlinker "$test_frameworks" \
      "$source_dir/core/"*.swift "$source_dir/ios/OpenWakeWordDetector.swift" "$source_dir/runtime-tests/WakeWordTests.swift" \
      "$output/$platform/"*.o -o "$test_bundle/Contents/MacOS/WakeWordTests"
    xcrun xctest "$test_bundle"
  else
    SDKROOT="$sdk" xcrun --sdk "$sdk_name" swiftc -swift-version 5 -target "$target" -sdk "$sdk" \
      -I "$output/include" -F "$framework" -framework onnxruntime -lc++ -emit-library -module-name EvenerVoiceRuntime \
      "$source_dir/core/"*.swift "$source_dir/ios/SileroVAD.swift" "$source_dir/ios/OpenWakeWordDetector.swift" "$source_dir/ios/VoiceAudioDevice.swift" \
      "$source_dir/ios/VoiceSocket.swift" "$source_dir/ios/VoiceService.swift" "$source_dir/ios/VoiceAuthorization.swift" \
      "$output/$platform/"*.o -o "$output/$platform/libEvenerVoiceRuntime.dylib"
    file "$output/$platform/libEvenerVoiceRuntime.dylib"
  fi
done
