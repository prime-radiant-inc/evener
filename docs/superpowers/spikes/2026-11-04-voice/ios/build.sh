#!/bin/bash
# Standalone Apple SDK compilation. No xcodebuild setup, signing keys, or project.
set -euo pipefail
if [[ $# == 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  printf '%s\n' 'usage: bash build.sh simulator|device-compile NEW_OUTPUT_DIRECTORY' \
    'Apple Silicon Mac build only, no signing or publishing. --help runs offline on any host.'
  exit 0
fi
if [[ $# != 2 || ( "$1" != "simulator" && "$1" != "device-compile" ) ]]; then
  echo "usage: bash build.sh simulator|device-compile NEW_OUTPUT_DIRECTORY" >&2
  exit 2
fi
mode=$1
output=$2
source_dir=$(cd "$(dirname "$0")" && pwd)
developer_dir=${DEVELOPER_DIR:-$(/usr/bin/xcode-select -p)}
swiftc="$developer_dir/Toolchains/XcodeDefault.xctoolchain/usr/bin/swiftc"
# Refuse to overwrite any caller-owned directory.
mkdir "$output"
output=$(cd "$output" && pwd)
mkdir "$output/tmp"
export TMPDIR="$output/tmp"
if [[ "$mode" == simulator ]]; then
  sdk="$developer_dir/Platforms/iPhoneSimulator.platform/Developer/SDKs/iPhoneSimulator.sdk"
  target=arm64-apple-ios17.0-simulator
  mkdir "$output/VoiceProbe.app"
  cp "$source_dir/Info.plist" "$output/VoiceProbe.app/Info.plist"
  binary="$output/VoiceProbe.app/VoiceProbe"
else
  sdk="$developer_dir/Platforms/iPhoneOS.platform/Developer/SDKs/iPhoneOS.sdk"
  target=arm64-apple-ios17.0
  binary="$output/VoiceProbe-UNSIGNED-NOT-INSTALLABLE"
fi
set -x
# -sdk selects compiler headers. SDKROOT also reaches clang's link step, which
# otherwise advertised SDK 17.0 rather than 27.2 on the measured toolchain.
SDKROOT="$sdk" "$swiftc" -swift-version 6 -parse-as-library -sdk "$sdk" -target "$target" \
  -module-cache-path "$output/module-cache" \
  -framework SwiftUI -framework UIKit -framework AVFoundation -framework UniformTypeIdentifiers \
  "$source_dir/Sources/PlaybackFence.swift" "$source_dir/Sources/AudioProbe.swift" \
  "$source_dir/Sources/VoiceProbeApp.swift" -o "$binary"
/usr/bin/file "$binary"
SDKROOT="$developer_dir/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk" \
  "$swiftc" -swift-version 6 -parse-as-library \
  -sdk "$developer_dir/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk" \
  -target arm64-apple-macosx14.0 -module-cache-path "$output/module-cache-macos" \
  "$source_dir/Sources/PlaybackFence.swift" "$source_dir/Tests/PlaybackFenceCheck.swift" \
  -o "$output/PlaybackFenceCheck"
"$output/PlaybackFenceCheck"
