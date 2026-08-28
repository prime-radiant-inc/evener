# Remove Stale `/api/tree` REST Residue

## Status

Approved implementation slice: clean up the retired `/api/tree` application
JSON surface after navigation reads moved to `evener/navigation/read` over
AppWire. No `/api/tree` route is registered on `origin/main`; the remaining
active references are stale coverage or route language, while the tree
projection code is still shared by navigation and must remain.

## Evidence and invariant

The route table in `cmd/evener-hub/web.go` has no `/api/tree` registration.
Navigation reads are registered through `evener/navigation/read` in
`cmd/evener-hub/app_rpc.go`, and `navigation_service.go` still consumes the
tree-building helpers from `web_api_tree.go`. The migration invariant is:

> No active executable probe or current contract describes `/api/tree` as a
> live application API; shared navigation projection and behavior coverage
> remain intact.

## Scope

- Remove `/api/tree` from the real-binary HTTP coverage sweep.
- Confirm the refreshed base has no obsolete skipped test whose setup depends
  on the retired tree-read path; do not add an absence test or claim that
  another test covers deleted local-input behavior.
- Update the exact active production/test comments that describe navigation
  reads, shared tree projection, or qualified search refs.
- Update the current M6 parity claim and live scenario cards to distinguish the
  AppWire navigation manifest from its bounded section/catalog/project
  resources instead of describing the retired URL.
- Keep the tree cache, projection helpers, navigation tests, mutation
  invalidation, and AppWire navigation contract unchanged.

## Explicit exclusions

- Do not remove or rename `web_api_tree.go` or `hubcore/tree.go`; those are
  shared implementation layers for the AppWire navigation projection and
  mutation invalidation.
- Do not alter `internal/fuzzroutes/fuzzroutes.go`'s `/api/tree` entry or
  `web_fuzz_test.go`'s numeric retired-route seed. Their route indices are
  append-only corpus compatibility data.
- Do not rewrite historical `docs/superpowers/**` or legacy parity/design
  records whose purpose is to document the former REST behavior.
- Do not add a test that asserts the old route is absent.

## Expected result

The active source and live-scenario audit finds no `/api/tree` route/probe or
current-contract claim outside the explicitly retained append-only fuzz
registry/seed and historical records. Navigation behavior remains served by
`evener/navigation/read` and continues to use the existing shared tree
projection.
