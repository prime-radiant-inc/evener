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

# Captured first so a failing git (bad --root, unreadable index) stops the
# script under set -e instead of looking like an empty, passing listing. A path
# with a newline in its name is outside what the sweep handles.
tracked="$(git -C "${root}" ls-files -z | tr '\0' '\n')"

failed=0
while IFS= read -r path; do
	[ -f "${root}/${path}" ] || continue
	size="$(wc -c <"${root}/${path}")"
	[ "${size}" -gt "${limit}" ] || continue
	for allowed in "${allowlist[@]}"; do
		[ "${path}" = "${allowed}" ] && continue 2
	done
	printf 'tracked-file-size-check.sh: %s is %s bytes, over the %s byte budget\n' "${path}" "${size}" "${limit}" >&2
	failed=1
done <<<"${tracked}"
exit "${failed}"
