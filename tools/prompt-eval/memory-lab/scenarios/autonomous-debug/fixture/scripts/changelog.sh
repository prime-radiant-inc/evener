#!/bin/sh
# Usage: scripts/changelog.sh "Entry text"
# Adds an entry under the Unreleased heading of CHANGELOG.md.
set -eu
sed -i "/^## Unreleased/a - $*" CHANGELOG.md
