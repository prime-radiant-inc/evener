#!/usr/bin/env bash
# fuzz-registry-check.sh validates the authoritative run-fuzz target manifest
# against native and explicitly marked Rapid declarations. The checker emits the
# validated four-column replay plan only when the manifest has no drift.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
registry="$(mktemp -t evener-fuzz-registry.XXXXXX)"
trap 'rm -f "$registry"' EXIT

bash "$repo_root/scripts/fuzz/run-fuzz.sh" --list >"$registry"
(
	cd "$repo_root"
	go run ./cmd/evener-fuzzregistry --repo-root "$repo_root" --registry "$registry" --check --emit-plan
)
