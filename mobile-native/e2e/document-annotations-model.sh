#!/usr/bin/env bash
# Real Foundation regression, no React UI runtime or generated selection events.
set -euo pipefail
[[ $(uname -s) == Darwin ]]
root=$(cd "$(dirname "$0")/../.." && pwd)
artifacts=${1:?new owned artifact directory required}
test -d "$artifacts"
pkg="$root/mobile-native/node_modules/react-native-enriched-markdown"
test ! -L "$root/mobile-native/node_modules"
test ! -L "$pkg"
test "$(node -p "require('$pkg/package.json').version")" = 1.0.2
mkdir "$artifacts/model-objects"
objects="$artifacts/model-objects"
cleanup() {
  local status=$?
  trap - EXIT
  for name in md4c.o parser.o ast.o model.o native-model; do
    if [[ -e "$objects/$name" ]]; then rm -- "$objects/$name" || status=1; fi
  done
  rmdir "$objects" || status=1
  exit "$status"
}
trap cleanup EXIT
cflags=(-g -O1 -fsanitize=address,undefined -fno-omit-frame-pointer)
xcrun clang "${cflags[@]}" -std=c99 -c "$pkg/cpp/md4c/md4c.c" -o "$objects/md4c.o"
xcrun clang++ "${cflags[@]}" -std=c++17 -c "$pkg/cpp/parser/MD4CParser.cpp" -o "$objects/parser.o"
xcrun clang "${cflags[@]}" -fobjc-arc -I "$pkg/ios/parser" -c "$pkg/ios/parser/MarkdownASTNode.m" -o "$objects/ast.o"
xcrun clang "${cflags[@]}" -fobjc-arc -Wall -Wextra -Werror -I "$pkg/ios/parser" \
  -c "$pkg/ios/parser/ENRMOriginalDocumentModel.m" -o "$objects/model.o"
xcrun clang++ "${cflags[@]}" -std=c++17 -fobjc-arc -Wall -Wextra -Werror -framework Foundation \
  -I "$pkg/ios/parser" -I "$pkg/cpp/parser" \
  "$pkg/ios/parser/MarkdownSourceParser.mm" "$root/mobile-native/e2e/document-annotations-model.mm" \
  "$objects/md4c.o" "$objects/parser.o" "$objects/ast.o" "$objects/model.o" -o "$objects/native-model"
"$objects/native-model"
