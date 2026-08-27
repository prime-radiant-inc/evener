# AppWire model-list migration

## Goal

Make the existing typed `model/list` AppWire method the only model-catalog
read used by Evener. The method already owns harness and working-directory
scoping; this slice adds the rich catalog fields that the legacy
`GET /api/models` adapter still enriches, moves every browser caller to that
method, and removes the REST route.

This is one migration PR. It does not change the model-source architecture or
the launchability rules.

## Wire contract

`ModelListParams` remains the scoped request:

```json
{ "harness": "codex-local", "cwd": "/work/project" }
```

`ModelListResponse.data` and `.recent` continue to contain provider/model
identities, with these optional metadata fields added to each descriptor. The
Go representation uses pointer fields for optional scalar metadata, so an
explicit `false` or `0` is distinguishable from an unknown value. `displayName`
is optional on the shared descriptor because daemon/source responses may still
be identity-only; the hub always synthesizes it from the model id when absent.

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-5",
  "displayName": "Claude Sonnet 4.5",
  "contextWindow": 200000,
  "supportsTools": true,
  "supportsVision": true,
  "maxOutputTokens": 8192,
  "supportsWebSearch": true,
  "supportsReasoning": true,
  "inputCostPerMillion": 3,
  "outputCostPerMillion": 15,
  "reasoningEffortLevels": ["low", "medium", "high"]
}
```

The metadata is presence-aware: omitted means the source did not know the
value, while an explicitly reported `false` remains `false`. In Go this means
pointer scalars (`*int`, `*bool`, `*float64`) and an omittable `displayName`;
the generated TypeScript fields are optional. `reasoningEffortLevels` is a
normal omittable slice: nil and empty both mean that no ladder was supplied,
which is sufficient when `supportsReasoning` is false. The widget adapter
normalizes an absent display name to the raw model id.

`diagnostics` keeps the existing typed `ModelListDiagnostic` shape. Empty
arrays remain non-null where the current AppWire contract promises arrays.

## Server behavior

The hub keeps the current source selection and response semantics. Harness and
working-directory selection belongs to the hub's `model/list` handler; a
daemon's existing `model/list` handler remains the daemon/provider-scoped
identity source and is not expanded to make provider API calls.

1. Resolve the requested harness and `cwd` through the existing launch/source
   path.
2. Preserve the existing launch-contract priority, provider diagnostics,
   recent-model filtering, and dated-snapshot ordering.
3. Enrich each returned descriptor at the hub boundary. The embedded catalog
   fills absent metadata. The existing `providers.toml` override semantics are
   preserved only for the fields it currently owns: `contextWindow`,
   `supportsReasoning`, and `reasoningEffortLevels`; it does not invent
   overrides for costs, capabilities, or output limits. The same enriched
   entries are reused for `recent` so recent rows do not lose metadata.
4. Preserve the current live-provider fallback for a default/local request
   when no launch model lister is configured, but move the existing provider
   listing and its per-hub TTL cache behind the typed `model/list` handler. It
   is the one live-list owner for this fallback; the browser and any catalog
   adapter must not issue a second provider listing. Live values already
   present in those typed descriptors win over catalog values.

The typed list keeps the existing stable ordering: provider ascending, with
dated snapshot ids after non-dated ids within each provider, preserving source
order otherwise. Recent entries are selected by provider/model identity from
the enriched data, not rebuilt as bare refs.

The implementation may refactor the existing REST-only map enrichment and
cache into typed helpers. It must not leave a second map-shaped catalog
contract behind after the HTTP adapter is deleted. The fallback may remain a
hub-owned implementation detail, but its public result is always the typed
AppWire response.

## Browser migration

The model-catalog loader requests `model/list` through the caller's existing
`AppwireClientLike`, sending `harness` and `cwd` as typed params. It only
adapts the response envelope (`data`/`recent` to `models`/`recent`) and
normalizes an absent display name; generated descriptor and diagnostic types
remain the source of truth rather than being copied into a second wire model.

The command palette and session model switch use the existing unscoped
`threadsStore.listModels()` cache and convert that single response. Spawn uses
one `(harness,cwd)`-keyed loader/promise shared by its prefetch, top-level
picker, and advanced fields, so the scoped path does not make a second
enrichment request or duplicate an identical `model/list` call. Settings
pickers use the same typed loader with the shared client. The old
two-source `mergeScopedCatalog` path is removed; only any general snapshot
merge that protects a less-informed later response remains.

Tests script `FakeClient`'s generated `model/list` method. No browser test
stubs `fetch` for model catalog data after this migration.

## REST removal

Remove the `/api/models` registration, handler, REST response writer, and
route-only raw-map/cache code. Delete only tests and fuzz/coverage entries that
exist solely to exercise that HTTP adapter; retain pure catalog enrichment and
launch-validation tests, converting them to typed AppWire/server tests where
they still protect behavior. Do not add a test whose purpose is merely to
assert that the deleted route is absent.

Update active comments and scenarios to name `model/list`. Checks that only
asserted the deleted HTTP serialization are removed; meaningful model-picker
behavior checks move to typed FakeClient/AppWire or UI assertions. Historical
notes may remain when they are explicitly historical.

## Non-goals

- no new model-list method and no compatibility HTTP shim;
- no provider API changes, catalog data refresh, or launchability-policy change;
- no migration of unrelated REST endpoints;
- no change to the daemon's existing `model/list` meaning beyond additive
  optional metadata.

## Verification

Before implementation, run the four-angle `simplify-code` review on this spec
and incorporate only quality-preserving feedback. After implementation, run
the same four-angle review again, then run the focused AppWire/hub/frontend
tests and the repository gates:

```text
make generate
make lint
make vet
make test
make test-web
make test-web-browser
```

Inspect generated artifacts, `git diff --check`, and the final diff for scope
drift before opening the PR.
