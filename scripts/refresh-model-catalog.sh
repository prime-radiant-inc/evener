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

tmp="$(mktemp "${TMPDIR:-/tmp}/serf-model-catalog.XXXXXX")"
trap 'rm -f "${tmp}"' EXIT

echo "fetching ${UPSTREAM_URL}"
if ! curl -fsSL --max-time 120 "${UPSTREAM_URL}" -o "${tmp}"; then
  echo "error: download failed (network? upstream moved?)" >&2
  exit 1
fi

OVERRIDES="${ROOT}/llm/data/serf_model_catalog_overrides.json"

python3 - "${TARGET}" "${tmp}" "${MIN_KEEP_RATIO}" "${OVERRIDES}" <<'PYEOF'
import json, sys

target, fresh_path, min_keep, overrides_path = sys.argv[1], sys.argv[2], float(sys.argv[3]), sys.argv[4]

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

# Drift audit: our curated overrides against the incoming snapshot. This is
# the generator-grade cross-check without a generator — curation that upstream
# has caught up with (or now contradicts) should be reconciled by hand.
overrides = {k: v for k, v in json.load(open(overrides_path)).items() if not k.startswith("_")}
materialized = sorted(k for k, v in overrides.items()
                      if k not in cur_keys and k in new_keys and "context_window" in v)
if materialized:
    print("DRIFT: upstream now defines models we materialized in overrides —")
    print("       our entry SHADOWS upstream; reconcile or slim the override:")
    for k in materialized:
        print(f"  {k}")
window_conflicts = []
for k, v in overrides.items():
    want = v.get("context_window")
    if want and k in fresh:
        got = fresh[k].get("max_input_tokens") or fresh[k].get("max_tokens")
        if got and got != want:
            window_conflicts.append(f"{k}: override {want} vs upstream {got}")
if window_conflicts:
    print("DRIFT: context_window disagreements (override wins at load; verify it should):")
    for line in window_conflicts:
        print(f"  {line}")
# An overlay-only override (no context_window) patches an EXISTING upstream
# entry; if upstream drops that entry the override dangles and the model
# silently VANISHES from the catalog (bit us 2026-07-02: upstream removed
# minimax/minimax-m2.7 and the model — plus its tests — disappeared).
dangling = sorted(k for k, v in overrides.items()
                  if "context_window" not in v and k not in new_keys)
if dangling:
    print("DRIFT: upstream DROPPED entries these overlay-only overrides patch —")
    print("       the model VANISHES from the catalog; materialize the override")
    print("       (add context_window etc.) or delete it:")
    for k in dangling:
        print(f"  {k}")
if not materialized and not window_conflicts and not dangling:
    print("overrides drift audit: clean")
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
