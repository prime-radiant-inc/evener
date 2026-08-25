#!/bin/sh
set -eu

repo_root=$(pwd)
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
. "$script_dir/../lib/private-go-home.sh"
stage=$(mktemp -d "${TMPDIR:-/tmp}/evener-runtime-build.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
evener_prepare_private_go_home "$stage"

build_one() {
	output=$1
	package=$2
	if [ -n "${LDFLAGS:-}" ]; then
		go build -ldflags "$LDFLAGS" -o "$stage/$output" "$package"
	else
		go build -o "$stage/$output" "$package"
	fi
}

# The evener binary embeds the frontend SPA (via the hub subcommand) and
# contains the end-user subcommands. The evener-dev binary holds the
# dev/test infrastructure subcommands (agent-shards, module-lint, fuzz-*, etc.).
build_one evener ./cmd/evener/
build_one evener-dev ./cmd/evener-dev/bin/

mv "$stage/evener" "$repo_root/evener"
mv "$stage/evener-dev" "$repo_root/evener-dev"
