# Issue #4437: fake429 provider fixture schema

## Scope and behavior

Repair `test/e2e/fake429/providers.toml` so the disposable hub launched by
`scripts/e2e/e2e-ratelimited-provider.sh` loads the `ratelimited` instance.
Preserve the literal `FAKE429_ADDR` substitution token and existing explanatory
comments. The complete configuration block becomes:

```toml
schema = 2
default = "ratelimited"

[providers.ratelimited]
base = "openai-compatible"
base_url = "http://FAKE429_ADDR/v1"
```

No API key or extra fields are needed: the registry's `openai-compatible` base
uses `optional-bearer`, whose authenticator accepts an empty credential.
The fixture backend does not require authentication.

## Audit extension

Extend `e2eprovidersschema_audit_test.go` to audit both the existing web turn
controls heredoc and the fake429 fixture through one shared parse/load/resolve
path. Keep the old-schema `errors.Is(err, registry.ErrOldSchema)` premise guard.
For each source, explicitly name the expected default instance, a model reference,
and the concrete loopback base URL. Substitute `$fakellm_port` or `FAKE429_ADDR`
with literal address values before parsing. Assert parsed default/provider/base
URL, resolved protocol/base URL, and completion/stream/model endpoint paths.
Preserve the web harness's parsed and resolved session-affinity checks; fake429
does not need session-affinity headers. Assert fake429 resolves with
`optional-bearer` authentication without adding a key.

## Non-goals and preservation

No production, harness, provider authentication, UI, or registry behavior changes.
No live provider requests, full local suites, fixed harness ports, or new labels.
Only the fixture, root audit, and this spec change. Existing web-harness coverage
must remain at least as strict. Recovery and saved-user-work behavior is unchanged:
this is a disposable test configuration repair, not a runtime recovery change.

## Verification and delivery

First extend the audit while leaving the fixture unchanged, then run
`go test . -run 'ProvidersSchema' -count=1` and observe fake429 fail with
`ErrOldSchema`. Change the fixture and rerun to green. Demonstrate that regressing
either source to schema 1 fails its audit, restoring each exact source afterward.
Run `go test ./test/e2e/fake429/...`, `go vet . ./test/e2e/fake429/...`, and
`make build`. Registry load uses a fresh temporary state root and no fetcher;
these checks prove the hub's registry boundary without launching a live hub.

Freeze and competitively review this spec before implementation. Review the
implementation diff with PAR, simplify, commit, run branch RoboRev, then publish
and shepherd a PR. Reconcile main before the final push if required. Merge only
with current required checks green and all RoboRev findings low or conclusively
refuted. CI owns the full-suite evidence.
