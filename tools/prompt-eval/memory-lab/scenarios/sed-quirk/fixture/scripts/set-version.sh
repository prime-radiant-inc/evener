#!/bin/sh
# Usage: scripts/set-version.sh NEW_VERSION
# Sets the version line at the top of every docs page.
set -eu
sed -i "s/^version: .*/version: $1/" docs/*.md
