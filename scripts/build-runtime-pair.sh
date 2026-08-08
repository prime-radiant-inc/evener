#!/bin/sh
set -eu

repo_root=$(pwd)
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
. "$script_dir/private-go-home.sh"
stage=$(mktemp -d "${TMPDIR:-/tmp}/serf-runtime-build.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
serf_prepare_private_go_home "$stage"

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
