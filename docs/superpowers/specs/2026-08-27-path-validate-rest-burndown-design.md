# Remove the Superseded Path Validation REST Surface

## Status

Approved implementation slice: delete the duplicate `/api/path/validate`
application JSON surface. The hub AppWire server and all current frontend
consumers already use the typed `evener/path/validate` method, so this slice
changes no wire contract and adds no compatibility fallback.

## Evidence and scope

The contract invariant is that all active consumers use `evener/path/validate`
through AppWire. The caller audit at `origin/main` found no active production
HTTP caller for `/api/path/validate`; `app_rpc.go` registers the AppWire method
against the shared `fspaths.ValidateLaunchPath` implementation, and the
launch-config, spawn, and settings consumers are current examples of that
contract.

The remaining HTTP references are the mux registration and handler, route-only
coverage probes, one coverage-harness curl, and two current parity checklist
sentences that still describe an HTTP fallback. Historical superpowers
specs/plans are not current callers and remain unchanged.

## Deletions and documentation cleanup

- Remove `/api/path/validate` registration and `handleAPIPathValidate`.
- Remove its route-only coverage and fuzz probes and the coverage-harness curl.
- Update current parity docs to describe the AppWire validation request and
  remove the REST fallback claim.

## Non-goals

- Do not change `evener/path/validate`, `fspaths.ValidateLaunchPath`, or its
  AppWire handler and tests.
- Do not change the unrelated `/api/dirs/create` route used by spawn preflight.
- Do not add a test whose only purpose is to assert that the legacy route is
  absent.
- Do not rewrite historical design records.

## Verification

- Focused hub tests pass after route-only probes are removed.
- The exact source/current-doc audit has no non-historical
  `/api/path/validate` or `handleAPIPathValidate` references.
- `make lint`, `make vet`, and `make test` pass after implementation and the
  post-implementation simplify review.
