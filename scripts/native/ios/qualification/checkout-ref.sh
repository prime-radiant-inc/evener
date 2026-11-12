#!/usr/bin/env bash
# checkout-ref.sh HEAD_SHA MERGE_SHA REQUIRED_PATH... — choose the commit the
# iOS qualification workflow tests.
#
# WHY: a pull_request run takes its workflow file from the merge ref, while the
# qualification used to check out only the PR head. When the workflow and the
# scripts it calls change together, a head that predates those scripts runs the
# new steps against a tree that lacks them and fails on a missing directory.
# The merge ref always carries the head's changes plus current main, so it has
# both. This keeps the documented PR-head testing whenever the head carries the
# harness the workflow needs, and falls back to the merge ref — naming the
# missing path — when it does not.
#
# The repository must already contain both commits (the workflow checks out the
# merge ref with full history, so the head is an ancestor and already local).
# Prints `ref=<sha>` and `reason=<text>` in GITHUB_OUTPUT form.
set -euo pipefail

if [ "$#" -lt 3 ]; then
	echo "usage: $0 HEAD_SHA MERGE_SHA REQUIRED_PATH..." >&2
	exit 2
fi
head="$1"
merge="$2"
shift 2

require_commit() {
	case "$1" in
	"" | *[!0-9a-fA-F]*)
		echo "checkout-ref: not a commit sha: $1" >&2
		exit 2
		;;
	esac
	if [ "${#1}" -ne 40 ]; then
		echo "checkout-ref: not a full commit sha: $1" >&2
		exit 2
	fi
	if ! git rev-parse --verify --quiet "$1^{commit}" >/dev/null; then
		echo "checkout-ref: commit $1 is not in this checkout" >&2
		exit 2
	fi
}
require_commit "$head"
require_commit "$merge"

for path in "$@"; do
	case "$path" in
	/* | .. | ../* | */../* | */..)
		echo "checkout-ref: required path must stay inside the repository: $path" >&2
		exit 2
		;;
	esac
	if ! git cat-file -e "$head:$path" 2>/dev/null; then
		echo "checkout-ref: the PR head $head predates $path; testing the merge ref $merge" >&2
		printf 'ref=%s\nreason=%s\n' "$merge" "merge ref: head $head lacks $path"
		exit 0
	fi
done

echo "checkout-ref: the PR head $head carries the harness; testing it" >&2
printf 'ref=%s\nreason=%s\n' "$head" "PR head"
