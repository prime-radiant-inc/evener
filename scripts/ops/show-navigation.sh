#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/ops/show-navigation.sh [hub-url]

Fetch the complete paginated navigation surface and print every project and
session. The bearer token is read from EVENER_AUTH_TOKEN_FILE, or the default
local hub token file.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

hub_url="${1:-${EVENER_HUB_URL:-http://127.0.0.1:9180}}"
hub_url="${hub_url%/}"
state_root="${EVENER_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/evener}"
token_file="${EVENER_AUTH_TOKEN_FILE:-$state_root/auth-token}"
section_limit=50
catalog_limit=100

for command in curl jq sort mktemp; do
  if ! command -v "$command" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$command" >&2
    exit 1
  fi
done

if [[ ! -r "$token_file" ]]; then
  printf 'auth token file is not readable: %s\n' "$token_file" >&2
  exit 1
fi
auth_token="$(<"$token_file")"
if [[ -z "$auth_token" ]]; then
  printf 'auth token file is empty: %s\n' "$token_file" >&2
  exit 1
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/evener-navigation.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT
project_keys_file="$tmp_dir/project-keys"
pin_section_ids_file="$tmp_dir/pin-section-ids"
: >"$project_keys_file"
: >"$pin_section_ids_file"

get_json() {
  local path="$1"
  curl -fsS --header "Accept: application/json" --header "Authorization: Bearer $auth_token" "$hub_url$path"
}

urlencode() {
  jq -nr --arg value "$1" '$value | @uri'
}

print_sessions() {
  local body="$1"
  local scope="$2"
  jq -r --arg scope "$scope" '
    def flatten_sessions:
      .[] as $session |
      $session,
      (($session.children // []) | flatten_sessions);
    (.sessions // [] | flatten_sessions)
    | [
        $scope,
        (.ref // ""),
        (.state // ""),
        (if .live then "live" else "saved" end),
        (.project // ""),
        (.title // "")
      ]
    | @tsv
  ' <<<"$body"
}

print_projects() {
  local body="$1"
  local scope="$2"
  jq -r --arg scope "$scope" '
    .projects[]
    | [
        $scope,
        (.key // ""),
        (.name // ""),
        (.session_count // 0 | tostring),
        (.rollup_state // ""),
        (.working_dir // "")
      ]
    | @tsv
  ' <<<"$body"
}

fetch_section() {
  local label="$1"
  local route="$2"
  local expected_count="$3"
  local offset=0

  printf '\n## %s sessions (manifest count: %s)\n' "$label" "$expected_count"
  while (( offset < expected_count )); do
    local body rows remaining
    body="$(get_json "$route?offset=$offset&limit=$section_limit")"
    rows="$(jq -r '.sessions | length' <<<"$body")"
    remaining="$(jq -r '.remaining' <<<"$body")"
    print_sessions "$body" "$label"
    if (( rows == 0 )); then
      if (( remaining != 0 )); then
        printf 'navigation pagination made no progress for %s at offset %s\n' "$label" "$offset" >&2
        exit 1
      fi
      break
    fi
    offset=$((offset + rows))
    if (( remaining == 0 )); then
      break
    fi
  done
}

fetch_pin_catalog() {
  local expected_count="$1"
  local offset=0

  printf '\n## pin sections (manifest count: %s)\n' "$expected_count"
  while (( offset < expected_count )); do
    local body rows remaining
    body="$(get_json "/api/navigation/pin-sections?offset=$offset&limit=$catalog_limit")"
    rows="$(jq -r '.pin_sections | length' <<<"$body")"
    remaining="$(jq -r '.remaining' <<<"$body")"
    jq -r '.pin_sections[] | [.id, .name, (.count | tostring)] | @tsv' <<<"$body"
    jq -r '.pin_sections[].id' <<<"$body" >>"$pin_section_ids_file"
    if (( rows == 0 )); then
      if (( remaining != 0 )); then
        printf 'navigation pagination made no progress for pin sections at offset %s\n' "$offset" >&2
        exit 1
      fi
      break
    fi
    offset=$((offset + rows))
    if (( remaining == 0 )); then
      break
    fi
  done
}

fetch_catalog() {
  local label="$1"
  local route="$2"
  local expected_count="$3"
  local offset=0

  printf '\n## %s projects (manifest count: %s)\n' "$label" "$expected_count"
  while (( offset < expected_count )); do
    local body rows remaining
    body="$(get_json "$route?offset=$offset&limit=$catalog_limit")"
    rows="$(jq -r '.projects | length' <<<"$body")"
    remaining="$(jq -r '.remaining' <<<"$body")"
    print_projects "$body" "$label"
    jq -r '.projects[].key' <<<"$body" >>"$project_keys_file"
    if (( rows == 0 )); then
      if (( remaining != 0 )); then
        printf 'navigation pagination made no progress for %s at offset %s\n' "$label" "$offset" >&2
        exit 1
      fi
      break
    fi
    offset=$((offset + rows))
    if (( remaining == 0 )); then
      break
    fi
  done
}

fetch_project_tier() {
  local project_key="$1"
  local encoded_key="$2"
  local tier="$3"
  local initial_body="$4"
  local offset rows remaining body

  offset="$(jq -r --arg tier "$tier" '.[$tier].sessions | length' <<<"$initial_body")"
  remaining="$(jq -r --arg tier "$tier" '.[$tier].remaining' <<<"$initial_body")"
  jq -r --arg scope "project:$project_key:$tier" --arg tier "$tier" '
    def flatten_sessions:
      .[] as $session |
      $session,
      (($session.children // []) | flatten_sessions);
    (.[$tier].sessions // [] | flatten_sessions)
    | [
        $scope,
        (.ref // ""),
        (.state // ""),
        (if .live then "live" else "saved" end),
        (.project // ""),
        (.title // "")
      ]
    | @tsv
  ' <<<"$initial_body"

  while (( remaining > 0 )); do
    body="$(get_json "/api/navigation/projects/$encoded_key?tier=$tier&offset=$offset&limit=$section_limit")"
    rows="$(jq -r '.sessions | length' <<<"$body")"
    remaining="$(jq -r '.remaining' <<<"$body")"
    print_sessions "$body" "project:$project_key:$tier"
    if (( rows == 0 )); then
      printf 'navigation pagination made no progress for project %s tier %s at offset %s\n' "$project_key" "$tier" "$offset" >&2
      exit 1
    fi
    offset=$((offset + rows))
  done
}

fetch_project() {
  local project_key="$1"
  local encoded_key body tier

  encoded_key="$(urlencode "$project_key")"
  body="$(get_json "/api/navigation/projects/$encoded_key")"
  printf '\n### project resource: %s\n' "$project_key"
  jq -r '[.key, (.current.sessions | length | tostring), (.recent.sessions | length | tostring), (.archived.sessions | length | tostring)] | @tsv' <<<"$body"
  for tier in current recent archived; do
    fetch_project_tier "$project_key" "$encoded_key" "$tier" "$body"
  done
}

manifest="$(get_json /api/navigation)"
generation_id="$(jq -r '.generation_id' <<<"$manifest")"
revision="$(jq -r '.revision' <<<"$manifest")"
printf 'Hub: %s\n' "$hub_url"
printf 'Navigation generation: %s\n' "$generation_id"
printf 'Manifest revision: %s\n' "$revision"
printf 'Columns: scope<TAB>ref-or-key<TAB>state-or-name<TAB>live<TAB>project-or-count<TAB>title-or-working-dir\n'

fetch_section live /api/navigation/sections/live "$(jq -r '.sections.live.count' <<<"$manifest")"
fetch_section needs_you /api/navigation/sections/needs-you "$(jq -r '.sections.needs_you.count' <<<"$manifest")"
fetch_pin_catalog "$(jq -r '.sections.pin_sections.count' <<<"$manifest")"
fetch_catalog projects /api/navigation/catalogs/projects "$(jq -r '.catalogs.projects.count' <<<"$manifest")"
fetch_catalog archived_projects /api/navigation/catalogs/archived-projects "$(jq -r '.catalogs.archived_projects.count' <<<"$manifest")"
fetch_catalog test_runs /api/navigation/catalogs/test-runs "$(jq -r '.catalogs.test_runs.count' <<<"$manifest")"

printf '\n## pin-section session resources\n'
sort -u "$pin_section_ids_file" | while IFS= read -r section_id; do
  [[ -n "$section_id" ]] || continue
  encoded_id="$(urlencode "$section_id")"
  body="$(get_json "/api/navigation/pin-sections/$encoded_id?offset=0&limit=$section_limit")"
  printf '\n### pin section: %s\n' "$section_id"
  print_sessions "$body" "pin:$section_id"
  offset="$(jq -r '.sessions | length' <<<"$body")"
  remaining="$(jq -r '.remaining' <<<"$body")"
  while (( remaining > 0 )); do
    body="$(get_json "/api/navigation/pin-sections/$encoded_id?offset=$offset&limit=$section_limit")"
    rows="$(jq -r '.sessions | length' <<<"$body")"
    remaining="$(jq -r '.remaining' <<<"$body")"
    print_sessions "$body" "pin:$section_id"
    if (( rows == 0 )); then
      printf 'navigation pagination made no progress for pin section %s at offset %s\n' "$section_id" "$offset" >&2
      exit 1
    fi
    offset=$((offset + rows))
  done
done

printf '\n## project session resources\n'
sort -u "$project_keys_file" >"$tmp_dir/project-keys-unique"
while IFS= read -r project_key; do
  [[ -n "$project_key" ]] || continue
  fetch_project "$project_key"
done <"$tmp_dir/project-keys-unique"
