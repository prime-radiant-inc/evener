#!/usr/bin/env bash
#
# refresh-model-catalog.sh — refresh the vendored LiteLLM model catalog.
#
# llm/data/litellm_model_catalog.json is a verbatim snapshot of LiteLLM's
# published model_prices_and_context_window.json. It must NEVER be hand-edited:
# serf-specific metadata (effort levels, context windows for models upstream
# lacks, capability flags) lives in llm/data/serf_model_catalog_overrides.json,
# which is overlaid at load time and always wins. This script is the only
# sanctioned way to change the vendored file.
#
# Usage:
#   scripts/refresh-model-catalog.sh --check   # dry run: report the delta, write nothing
#   scripts/refresh-model-catalog.sh           # refresh the snapshot in place
#
# After a real refresh: review `git diff --stat llm/data/`, run the full gate,
# and eyeball the removed-models list below — entries that vanish upstream can
# silently drop effort levels or context windows serf relied on (the overrides
# layer is the fix for anything that must survive upstream churn).
set -euo pipefail

UPSTREAM_URL="https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
ROOT="$(git rev-parse --show-toplevel)"
TARGET="${ROOT}/llm/data/litellm_model_catalog.json"
# An upstream truncation/outage should fail loudly, not quietly shrink the
# catalog: refuse when the new snapshot has lost more than 10% of entries.
MIN_KEEP_RATIO="0.90"

mode="refresh"
case "${1:-}" in
  -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  --check)   mode="check" ;;
  "")        ;;
  *)         echo "error: unknown argument '$1' (try --help)" >&2; exit 2 ;;
esac

if [[ ! -f "${TARGET}" ]]; then
  echo "error: ${TARGET} not found — run from a serf checkout" >&2
  exit 1
fi

tmp="$(mktemp --suffix=.json)"
trap 'rm -f "${tmp}"' EXIT

echo "fetching ${UPSTREAM_URL}"
if ! curl -fsSL --max-time 120 "${UPSTREAM_URL}" -o "${tmp}"; then
  echo "error: download failed (network? upstream moved?)" >&2
  exit 1
fi

python3 - "${TARGET}" "${tmp}" "${MIN_KEEP_RATIO}" <<'PYEOF'
import json, sys

target, fresh_path, min_keep = sys.argv[1], sys.argv[2], float(sys.argv[3])

try:
    fresh = json.load(open(fresh_path))
except json.JSONDecodeError as e:
    sys.exit(f"error: upstream payload is not valid JSON: {e}")
if not isinstance(fresh, dict) or len(fresh) < 100:
    sys.exit(f"error: upstream payload looks wrong (type={type(fresh).__name__}, entries={len(fresh) if isinstance(fresh, dict) else '-'})")

current = json.load(open(target))
cur_keys, new_keys = set(current), set(fresh)
added = sorted(new_keys - cur_keys)
removed = sorted(cur_keys - new_keys)
changed = sorted(k for k in cur_keys & new_keys if current[k] != fresh[k])

if len(new_keys) < len(cur_keys) * min_keep:
    sys.exit(
        f"error: refusing to shrink the catalog from {len(cur_keys)} to {len(new_keys)} entries "
        f"(more than {int((1-min_keep)*100)}% loss) — check upstream before overriding by hand"
    )

print(f"current: {len(cur_keys)} entries; upstream: {len(new_keys)} entries")
print(f"delta: +{len(added)} added, -{len(removed)} removed, ~{len(changed)} changed")
def preview(label, keys):
    if keys:
        head = ", ".join(keys[:8])
        more = f" (+{len(keys)-8} more)" if len(keys) > 8 else ""
        print(f"  {label}: {head}{more}")
preview("added", added)
preview("removed", removed)
PYEOF

if [[ "${mode}" == "check" ]]; then
  echo "--check: no files written"
  exit 0
fi

mv "${tmp}" "${TARGET}"
trap - EXIT
echo "wrote ${TARGET}"

echo "running catalog sanity tests..."
if ! (cd "${ROOT}" && go test ./llm/ -run 'Catalog|LookupModelInfo' -count=1 >/dev/null); then
  echo "error: catalog tests FAILED against the new snapshot — inspect 'git diff llm/data/' and either fix overrides or revert" >&2
  exit 1
fi
echo "catalog tests pass. Next: git diff --stat llm/data/ && full gate before committing."
