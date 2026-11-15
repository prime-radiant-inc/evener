#!/usr/bin/env bash
# Print the durable cache directory for the current worktree. The worktree
# root is hashed so sibling worktrees cannot replay each other's findings
# (#290), and the Git common directory is hashed into the parent bucket so
# independent clones — which share an XDG cache root but never a common dir —
# never share a sweeper's scope. The cache stays outside the repository so
# whole-tree secret scans never read it. The path is printed only;
# golangci-lint creates it on first use.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
common="$(git rev-parse --path-format=absolute --git-common-dir)"
cache_home="${XDG_CACHE_HOME:-${HOME:?HOME must be set}/.cache}"
repo_id="$(printf '%s' "$common" | shasum -a 256 | cut -c1-16)"
worktree_id="$(printf '%s' "$root" | shasum -a 256 | cut -c1-16)"
printf '%s/evener/golangci-lint/%s/%s\n' "$cache_home" "$repo_id" "$worktree_id"
