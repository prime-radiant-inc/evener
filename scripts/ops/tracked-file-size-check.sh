#!/usr/bin/env bash
# tracked-file-size-check.sh fails if any git-tracked file exceeds 1 MiB and is
# not on the allowlist below. A large blob stays in history after it is
# deleted, so the budget catches it at the PR that adds it. Adding an entry is
# a reviewed decision: say in the PR why the file has to be committed.
#
# --root points the check at another git checkout; only the gate's own Go test
# passes it.
set -euo pipefail

limit=1048576

allowlist=(
	# The bundled Mermaid renderer the native app loads in a WebView; freshness
	# is tested by mobile-native/src/mermaidPage.test.ts.
	mobile-native/src/generated/mermaidPage.ts
	# Registry fixture: a trimmed copy of the real models.dev payload.
	llm/registry/testdata/models.dev.sample.json
)

root="$(cd -- "$(dirname -- "$0")/../.." && pwd)"
if [ "${1:-}" = "--root" ]; then
	[ -n "${2:-}" ] || { printf 'tracked-file-size-check.sh: --root needs a directory\n' >&2; exit 2; }
	root="$2"
fi

# pipefail (set above) makes a failing git, such as a bad --root, fail the
# script instead of looking like an empty, passing listing.
git -C "${root}" ls-files -z | {
	failed=0
	while IFS= read -r -d '' path; do
		[ -f "${root}/${path}" ] || continue
		size="$(wc -c <"${root}/${path}")"
		[ "${size}" -gt "${limit}" ] || continue
		for allowed in "${allowlist[@]}"; do
			[ "${path}" = "${allowed}" ] && continue 2
		done
		printf 'tracked-file-size-check.sh: %s is %s bytes, over the %s byte budget\n' "${path}" "${size}" "${limit}" >&2
		failed=1
	done
	exit "${failed}"
}
