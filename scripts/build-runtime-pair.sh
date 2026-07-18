#!/bin/sh
set -eu

repo_root=$(pwd)
stage=$(mktemp -d "${TMPDIR:-/tmp}/serf-runtime-build.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM

build_one() {
	output=$1
	package=$2
	if [ -n "${LDFLAGS:-}" ]; then
		go build -ldflags "$LDFLAGS" -o "$stage/$output" "$package"
	else
		go build -o "$stage/$output" "$package"
	fi
}

build_one serf ./cmd/serf/
build_one serf-hub ./cmd/serf-hub/

mv "$stage/serf" "$repo_root/serf"
mv "$stage/serf-hub" "$repo_root/serf-hub"
