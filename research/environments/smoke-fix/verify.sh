#!/bin/sh
# Verifier for smoke-fix: the task succeeded when the repo's tests pass.
# Argument 1 is the run's working directory (a copy of repo/).
set -e
cd "$1"
exec go test ./...
