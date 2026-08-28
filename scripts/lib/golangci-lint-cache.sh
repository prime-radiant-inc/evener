#!/usr/bin/env bash
# Print the durable cache directory for the current worktree. The worktree
# root is hashed so sibling checkouts cannot replay each other's findings, and
# the cache stays outside the repository so whole-tree secret scans never read
# it. The path is printed only; golangci-lint creates it on first use.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cache_home="${XDG_CACHE_HOME:-${HOME:?HOME must be set}/.cache}"
worktree_id="$(printf '%s' "$root" | shasum -a 256 | cut -c1-16)"
printf '%s/evener/golangci-lint/%s\n' "$cache_home" "$worktree_id"
