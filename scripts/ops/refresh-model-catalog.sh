#!/usr/bin/env bash
#
# refresh-model-catalog.sh — refresh the embedded models.dev snapshot.
#
# llm/registry/data/models.dev.json.gz is the raw https://models.dev/api.json,
# gzipped, and models.dev.meta.json records when and with which ETag it was
# fetched. Neither is ever hand-edited; evener-specific corrections live in
# llm/registry/data/providers_overlay.toml, which the registry overlays at
# load time (design: docs/superpowers/specs/2026-08-28-provider-registry-design.md §6).
#
# Usage:
#   scripts/ops/refresh-model-catalog.sh --check   # dry run: report the delta, write nothing
#   scripts/ops/refresh-model-catalog.sh           # refresh the snapshot in place
#
# After a real refresh: review `git diff --stat llm/registry/data/`, run
# `go test ./llm/registry/...`, and read the report at the end (overlay rows
# upstream now covers, dangling overlay aliases, output caps at or above the
# context window, and overlay pins whose upstream value changed).
set -euo pipefail

UPSTREAM_URL="https://models.dev/api.json"
ROOT="$(git rev-parse --show-toplevel)"
DATA="${ROOT}/llm/registry/data"
TARGET="${DATA}/models.dev.json.gz"
META="${DATA}/models.dev.meta.json"
MIN_KEEP_RATIO="0.90"

mode="refresh"
case "${1:-}" in
  -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  --check)   mode="check" ;;
  "")        ;;
  *)         echo "error: unknown argument '$1' (try --help)" >&2; exit 2 ;;
esac

mkdir -p "${DATA}"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/evener-models-dev.XXXXXX")"
fresh="${tmpdir}/api.json"
headers="${tmpdir}/headers.txt"
old="${tmpdir}/old.json"
trap 'rm -f "${fresh}" "${headers}" "${old}"; rmdir "${tmpdir}" 2>/dev/null || true' EXIT

echo "fetching ${UPSTREAM_URL}"
if ! curl -fsSL --max-time 120 -D "${headers}" "${UPSTREAM_URL}" -o "${fresh}"; then
  echo "error: download failed (network? upstream moved?)" >&2
  exit 1
fi
etag="$(grep -i '^etag:' "${headers}" | tail -1 | sed 's/^[Ee][Tt][Aa][Gg]:[[:space:]]*//; s/[[:space:]]*$//' || true)"

if [[ -f "${TARGET}" ]]; then
  gunzip -c "${TARGET}" > "${old}"
else
  echo '{}' > "${old}"
fi

python3 - "${old}" "${fresh}" "${MIN_KEEP_RATIO}" <<'PYEOF'
import json, sys
old_path, new_path, min_keep = sys.argv[1], sys.argv[2], float(sys.argv[3])
old = json.load(open(old_path)); new = json.load(open(new_path))
if not isinstance(new, dict) or not new:
    sys.exit("error: upstream did not return a provider map")
def rows(d):
    return {(p, m) for p, pv in d.items() for m in (pv.get("models") or {})}
op, np_ = set(old), set(new)
orows, nrows = rows(old), rows(new)
if old and len(new) < len(old) * min_keep:
    sys.exit(f"error: refusing refresh: providers shrank {len(old)} -> {len(new)} (below {min_keep:.0%} floor)")
if orows and len(nrows) < len(orows) * min_keep:
    sys.exit(f"error: refusing refresh: models shrank {len(orows)} -> {len(nrows)} (below {min_keep:.0%} floor)")
print(f"providers: {len(old)} -> {len(new)}  (+{len(np_-op)} / -{len(op-np_)})")
print(f"models:    {len(orows)} -> {len(nrows)}  (+{len(nrows-orows)} / -{len(orows-nrows)})")
for p in sorted(np_ - op): print("  + provider", p)
for p in sorted(op - np_): print("  - provider", p)
added = sorted(nrows - orows)
for p, m in added[:200]: print("  + model", f"{p}/{m}")
if len(added) > 200: print(f"  ... {len(added) - 200} more added models (diff the gunzipped files for the full list)")
for p, m in sorted(orows - nrows): print("  - model", f"{p}/{m}")
PYEOF

if [[ "${mode}" == "check" ]]; then
  echo "check mode: nothing written"
  exit 0
fi

gzip -9 -n -c "${fresh}" > "${TARGET}.tmp"
mv "${TARGET}.tmp" "${TARGET}"
printf '{\n  "fetched_at": "%s",\n  "etag": %s,\n  "source": "%s"\n}\n' \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "${etag}")" \
  "${UPSTREAM_URL}" > "${META}"
echo "wrote ${TARGET} and ${META}"

echo "running converter tests"
(cd "${ROOT}" && go test ./llm/registry/ -run 'TestEmbeddedSnapshot|TestFromModelsDev|TestCuratedOverlay' -count=1)

# The overlay report (rows upstream now covers, dangling aliases, junk caps,
# changed pins) is produced by the registry's snapshotreport tool once it
# exists (plan Task 11); older checkouts skip it.
if [[ -d "${ROOT}/llm/registry/internal/snapshotreport" ]]; then
  (cd "${ROOT}" && go run ./llm/registry/internal/snapshotreport)
fi
