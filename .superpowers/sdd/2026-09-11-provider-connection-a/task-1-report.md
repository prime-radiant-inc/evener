# Task 1 report — safe provider catalogue setup metadata

Status: **DONE**

## Scope and commits

- Base supplied by caller: `e3037b119` on isolated `wip/provider-connection-a`.
- Implementation commit: `e66d8c67c9ce3d407acea10761f3c0fb7efe836d` — `feat(hub): expose safe provider setup metadata`.
- This report is committed separately as `docs(report): record provider setup metadata verification` so it can name the implementation commit. Resolve its hash from the commit containing this path.
- Both commits use explicit named paths and ordinary `git commit`; no hooks are disabled or skipped. No merge, push, delegates, preview-server changes, frontend implementation edits, registry changes, or unrelated test/doc edits.

## Implementation

- `appwire/types.go:2798–2803`: additive `authModes,omitempty` and `setup,omitempty` fields on `ProviderDescriptor`.
- `cmd/evener-hub/app_instances.go:75–115`: retain the existing `Instances()` loop unchanged; separately build catalogue setup through `Registry.Instance`, falling back to `ResolveInstance` for uncredentialed implicit IDs. Copy only listing metadata, including credential **source**, never credential values or resolved header maps. Pass through existing `entryFor` for auth status, safe authored credential-reference fields, and URL sanitization.
- Existing instances take precedence over curated metadata. An overridden curated ID therefore exposes its real authored/resolved destination and source rather than an invented vendor default. Descriptor `AuthModes` comes from its descriptor transport's auth scheme; setup carries the actual instance's mode.
- `cmd/evener-hub/frontend/src/protocol/types.gen.ts:1398–1399`: generated `authModes?: string[]` and `setup?: InstanceEntry`.

### Clarified addressability contract

The caller explicitly approved: setup is present for every successfully resolved addressable ID, **including hidden implicit providers whose configuration is incomplete**. Hidden setup has `Hidden=true` and an empty destination; it is not launch-ready membership. Setup is nil only for unaddressable IDs, such as nonimplicit providers before instance configuration. This is documented in the wire comment and catalogue tests. UI consumers must inspect Hidden/configuration requirements and must never infer `Instances` membership from Setup.

## Tests and observed behaviors

`cmd/evener-hub/app_instances_test.go`:

- `TestProviderSetup_DiscoveryWithoutCredentials` (`:2141`): empty injected environment; independent identity/auth/destination/membership expectations for Anthropic, OpenAI API, separate Codex OAuth, optional-key Ollama, hidden Google Vertex ADC/credential JSON, and hidden OpenAI-compatible discovery. Anthropic/Codex remain absent from launch-ready instances. Every catalogue row retains auth modes, and nonimplicit rows have no setup before configuration.
- `TestProviderSetup_AuthoredOverrideIsSafeResolvedView` (`:2195`): authored Anthropic endpoint override has the same safe setup as the actual instance; verifies source, authored environment-variable reference, literal credential-header suppression, and safe destination. Serialized list excludes inline key, stored-key, environment-key, header-secret, URL userinfo/password/query/fragment sentinels.
- `TestProviderSetup_EnvironmentDestinationIsSanitized` (`:2232`): uncredentialed fallback setup reflects the real environment-resolved proxy destination, with userinfo/query/fragment removed.

`cmd/evener-hub/app_provider_onboarding_test.go:18`:

- `TestZeroProviderOnboarding_SaveCheckListAtHTTPBoundary`: real isolated credentials.toml, real registry reload, real llm client, and loopback `httptest.Server` as the only provider/network boundary. The existing probe-loader seam supplies fixture-owned roots/environment and reopens the persisted store; it does not fake a client or listing.
- Before key: setup exists, Anthropic is absent from instances, credential check returns `missing`, and no HTTP request occurs.
- Save: real `ApiKeySet` persists the submitted key, a separately reopened credential store reads it, registry membership changes, and setup reports active stored source.
- Check: actual `GET /v1/models` carries the saved `X-Api-Key`; response status is `success` and serialized responses contain no key.
- Inheritance: clear the directly stored key, establish/verify `env:ANTHROPIC_API_KEY`, prove a successful HTTP request with it, then author a changed destination. Setup reports source `none`; check returns `missing` without any provider request.
- Explicitly save a replacement for the authored destination; actual `GET /changed/v1/models` carries it. A scripted HTTP 401 body containing a response sentinel and the key yields `auth_rejected` without echoing either into wire responses.

## Red / green evidence

All commands ran from the worktree root. No provider call reached the external network.

### Red

Command:

```sh
go test ./cmd/evener-hub -run 'TestProviderSetup|TestZeroProviderOnboarding' -count=1
```

An initial test-only compile mistake (`registry.AuthXAPIKey`, which does not exist) exited 1. Corrected it to the registry's actual `AuthHeader`, then reran **before production edits**. Expected runtime red, exit 1:

```text
--- FAIL: TestProviderSetup_DiscoveryWithoutCredentials
    --- FAIL: TestProviderSetup_DiscoveryWithoutCredentials/anthropic
        auth modes=[], want [apiKey]
        missing discovery setup
    ... corresponding missing modes/setup assertions for the other variants ...
--- FAIL: TestProviderSetup_AuthoredOverrideIsSafeResolvedView
    missing authored discovery setup
--- FAIL: TestProviderSetup_EnvironmentDestinationIsSanitized
    setup does not reflect sanitized resolved environment destination: <nil>
--- FAIL: TestZeroProviderOnboarding_SaveCheckListAtHTTPBoundary
    missing pre-key discovery: <nil>
FAIL primeradiant.com/evener/cmd/evener-hub 0.203s
```

Retained complete red evidence: `job:job_034MjnovVvH5xE8xDKjonI_D2Rn3SiMVvzr`.

### Fixture correction after first implementation run

The first implemented run passed the catalogue tests but the HTTP test incorrectly expected editing the same identity's destination to invalidate a directly stored key. Actual setup correctly reported `ActiveSource:store`, `HasStoredFile:true`.

Primary-source evidence: `llm/registry/instances.go:468–470` always checks the credential store by `rec.name`. That is a direct instance key, **not inherited curated authentication**. The caller confirmed this distinction and approved changing the fixture to test inherited environment authentication instead. No production assertion was weakened to hide a bug and no auth/registry behavior changed. The revised test establishes and verifies the inherited source before changing the destination, as described above.

### Green (final source/test organization)

```sh
go test ./cmd/evener-hub -run 'TestProviderSetup|TestZeroProviderOnboarding|TestAuthTestCredentials|TestInstances' -count=1
```

```text
ok  primeradiant.com/evener/cmd/evener-hub  4.199s
```

Exit 0.

```sh
go test ./llm/registry -run 'TestInstances_(ImplicitFromEnv|PseudoProviderNeedsBaseURL)$' -count=1
```

```text
ok  primeradiant.com/evener/llm/registry  0.083s
```

Exit 0. Earlier complete runs of these exact commands also passed (`5.056s` and `0.079s`).

```sh
go generate ./appwire
git diff --check
```

Both exited 0 with no error output. Generator inspection at `appwire/doc.go:28–29` confirms the actual output paths are `docs/appwire-protocol.md` and `cmd/evener-hub/frontend/src/protocol/types.gen.ts`. The Markdown output did not change and is not included in the commit; generated TypeScript changed only the two additive fields.

## Self-review and limitations

- Reviewed production and generated diffs, fixture isolation, serialized sentinel assertions, source/membership expectations, and HTTP request method/path/key assertions.
- Preserved every existing registry membership/filter/auth rule. No new credential resolver, raw resolved header exposure, default writes, or durable verification status.
- Directly stored instance keys intentionally remain active after an endpoint edit under the existing registry rules. **Task 2 must require explicit destination/source review before probing changed endpoints, including when a direct stored key remains active.** The caller explicitly retained that frontend responsibility.
- A successful model-list check proves list access only, not inference/model usability.
- Verification is the brief's focused backend/registry commands and protocol generation, not a claim that the full repository suite, frontend suite, or browser gates ran in this unit.
- No unresolved implementation concern or public-shape ambiguity remains.

## Changed paths

1. `appwire/types.go`
2. `cmd/evener-hub/app_instances.go`
3. `cmd/evener-hub/app_instances_test.go`
4. `cmd/evener-hub/app_provider_onboarding_test.go` (new)
5. `cmd/evener-hub/frontend/src/protocol/types.gen.ts` (generated)
6. `.superpowers/sdd/2026-09-11-provider-connection-a/task-1-report.md` (this report)
