# Remove the Superseded Upgrade REST Surface

## Status

Approved implementation slice: delete the unused `/api/upgrade` HTTP surface.
The hub, browser, and TUI already use the canonical `evener/upgrade` AppWire
method, so this microproject changes no AppWire contract and adds no
compatibility shim.

## Evidence and scope

The caller audit at `origin/main` found no active production HTTP caller for
`/api/upgrade`. The browser command palette and TUI dispatch
`evener/upgrade`; the hub's `hubUpgrade` implementation and its tests already
exercise that method. The remaining HTTP references are the route and handler,
route-only tests and fuzz/coverage probes, and one current behavior-contract
sentence that still describes a hypothetical REST fallback.

The upgrade implementation seam (`hubUpgrade`, `runHubSelfUpgrade`, and the
AppWire protocol/client tests) remains in place. Historical superpowers
specs/plans are not current callers and are intentionally not rewritten here.

## Deletions and documentation cleanup

- Remove `/api/upgrade` registration and `handleAPIUpgrade`.
- Remove the route-only `webHubUpgrade` indirection.
- Remove the HTTP route test and route-only coverage/fuzz entries.
- Update the current palette behavior contract to describe the AppWire upgrade
  path rather than a REST fallback.

## Non-goals

- Do not change `evener/upgrade`, `hubUpgrade`, self-update behavior, or the
  AppWire wire types.
- Do not remove `runHubSelfUpgrade`; it is the AppWire handler's injected
  external-update seam.
- Do not add a test asserting that the legacy route is absent.
- Do not rewrite historical design records.

## Verification

- Focused hub tests pass after the route-only tests and probes are removed.
- The exact source audit has no non-historical `/api/upgrade` route/handler
  references.
- `gofmt` is clean for changed Go files.
- `make lint`, `make vet`, and `make test` pass after the implementation and
  post-implementation simplify review.
