# Remove the Superseded Spawn-Schema REST Surface

## Status

Approved implementation slice: delete the unused `/api/spawn-schema` HTTP
surface. The browser and TUI already use the canonical
`evener/launch/schema` AppWire method, so this microproject changes no
AppWire contract and adds no compatibility shim.

## Evidence and scope

The exact caller audit at `codex/appwire-api-migration-plan` found no active
production caller for `/api/spawn-schema` or `hubapi.Client.SpawnSchema`.
The remaining references are the HTTP route and handler, the unused typed HTTP
client and response types, route-only tests/fuzz coverage, a coverage battery,
and one current research document describing the old surface. Existing
frontend and hub AppWire tests exercise `evener/launch/schema` directly.

Historical superpowers specs and plans retain the route as historical design
context; they are not current callers and are intentionally not rewritten in
this delete-only microproject.

## Deletions

- Remove `/api/spawn-schema` registration and `handleAPISpawnSchema`.
- Remove `hubapi.Client.SpawnSchema`, `hubapi.SpawnSchema`, and
  `hubapi.SpawnField`.
- Remove the `HealthCapabilities.SpawnSchema` flag, which only advertised the
  deleted route.
- Remove tests and fuzz/coverage calls that exist solely to exercise the old
  HTTP route.
- Remove the old route from `scripts/coverage/e2e-cover.sh`.
- Update `docs/research/api-fuzzing-toolkit.md` to identify the AppWire schema
  as the supported machine-readable launch schema.

## Non-goals

- Do not change `evener/launch/schema`, its generated protocol artifacts, or
  its launch-option behavior.
- Do not remove `launchHarnessIDs`; the live `/api/spawn` wrapper still uses
  it until that separate migration.
- Do not add a test asserting that legacy route text is absent.
- Do not rewrite historical design records.

## Verification

- Focused Go tests for `hubapi` and `cmd/evener-hub` pass after the deletions.
- The current source audit finds no non-historical production or test
  reference to the removed route/types.
- `gofmt` is clean for changed Go files.
- Applicable repository gates run after implementation, with no frontend
  changes in this microproject.
