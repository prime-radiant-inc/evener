# Issue #4603: sandbox reprovision after cleanup

## Scope and authorization

Correct `TestSandboxReprovisionAfterCleanup` to exercise the current production
`/clear` transition without weakening any freshness, retention, enforcement, or
spawn assertion. No product behavior changes are required.

Jesse authorized the issue workflow. After inspecting the production code, the
coordinating agent explicitly ruled that the test must use a fresh execution
environment and clear `cfg.SessionID`, matching `serve.go`. This corrects the
seam, rather than accepting reuse or changing same-session resume behavior.

## Non-goals

- Do not make the existing freshness assertion accept the same scratch path.
- Do not change allocation, retention, resume, policy, network, or sandbox modes.
- Do not change the production `/clear` implementation or scratch ownership.
- Do not expand local verification to full repository suites.

## Contracts

1. Cleanup releases the old environment's scratch lease and retains its path for
   human handoff. Files placed there remain available.
2. `/clear` creates a new session identity and a new execution environment before
   provisioning. Its scratch exists and differs from the old session's scratch.
3. The cleared environment is enforced and can spawn successfully. Old cleanup
   must not remove or invalidate the new scratch.
4. Resuming the same session reopens its named retained scratch, including its
   artifacts. Re-enabling a sandbox does not imply a new session identity.
5. Existing sandbox modes and the unsandboxed default keep their current behavior.

## Diagnosis

At base `29b37bb2734e7b77cef6f09ebed532970857d195`, the exact reproduction is red:

```sh
go test ./cmd/evener -run '^TestSandboxReprovisionAfterCleanup$' -count=1
```

It exits 1 at `sandbox_test.go:225`. The test passes the same `cfg` and the same
`env` to both provisioning calls. The first call populates `cfg.SessionID`.
`PickFreshSessionID` preserves a supplied ID (`agent/session_init.go:2656-2669`),
and `nameRootScratch` preserves an already-named environment
(`agent/session_init.go:2625-2639`). `newSessionScratch` opens named scratch
(`agent/execenv/local.go:738-748`), whose path deliberately depends only on the
root and session IDs (`agent/sandbox/session_scratch_tree.go:33-65`). Cleanup
releases the lease but retains the path. Re-enabling therefore legitimately
reopens that same path, not a stale wrapper or a broken random allocator.

Commit `6190f9d46` established handoff retention. The later commit
`752058d11c2f09ae1a45183a66d1147667a42549` (2026-10-05, #3742) introduced
named scratch and wired `PickFreshSessionID` into CLI provisioning. That same
commit reset `clearCfg.SessionID` in production `/clear`. The old test did not
adopt that new identity transition.

Production `/clear` resets `clearCfg.SessionID` (`cmd/evener/serve.go:1631-1634`)
and constructs a fresh environment (`cmd/evener/serve.go:1641-1648`). A diagnostic
using a Go test overlay confirmed all three enforced modes: the same identity
reopens retained scratch, while the full production fresh-ID/fresh-environment
transition retains an old artifact, gets a distinct scratch, denies an outside
write, and spawns with exit code zero. Clearing the config ID without replacing
the environment still reopens the old named scratch, as `nameRootScratch` intends.

## Fix at its source

The source of this failure is stale test setup, not production allocation.
In `cmd/evener/sandbox_test.go`, keep the existing test name and every behavioral
assertion. After cleaning up and checking the first scratch, clear `cfg.SessionID`
and construct a new `LocalExecutionEnvironment` for the second provisioning.
Register cleanup separately for that new environment. Update the test's comments
to describe a new `/clear` session rather than unsupported identity replacement
on the same environment.

Strengthen preservation by placing a sentinel artifact in the old scratch and
checking that it survives the transition. This is needed because path existence
alone cannot prove handoff content survives. Preserve the original cleanup-then-
provision sequence, then call the old environment's Cleanup again while the
replacement is live, before checking both scratch paths, the retained artifact,
and spawning. This verifies teardown isolation as well as Cleanup's idempotence,
without dropping the original after-cleanup regression scenario. Assert fresh
session identity and scratch existence, keep the enforced-env check and real
wrapped spawn, and also require the spawn's exit code to be zero.

Do not modify `PickFreshSessionID`, `EnableSandbox`, or `OpenSessionScratch`.
Existing named-scratch tests independently protect resume semantics. No evergreen
product contract changes are necessary because production behavior is unchanged.

## Verification

- Observe the exact named regression fail before changing its setup, then pass
  without removing or weakening its freshness assertion. Use `-v` additionally
  to distinguish actual execution from a host-capability skip.
- Run `go test ./cmd/evener -run 'Sandbox' -count=1 -v` for sandbox-related CLI
  tests, including the corrected `/clear` test.
- Run `go test ./agent/execenv -count=1` for the affected package and its existing
  named-scratch retention/resume coverage.
- Run `go test ./agent/sandbox -run '^TestOpenSessionScratchIsPerSessionAndReopensTheSamePath$' -count=1 -v`
  to verify the deliberately preserved same-identity contract.
- Run `go vet ./cmd/evener ./agent/execenv` and `make build`.
- Run competing PAR reviews of this frozen spec and the implementation diff,
  simplify, commit, and run branch RoboRev before pushing.
- Push one review round at a time, open a PR against `main`, and use the PR URL
  returned by GitHub. Merge updated `origin/main` locally if needed, renew head
  reviews/checks, and admin squash-merge only after all required checks are green
  and every RoboRev finding is low or conclusively refuted.

CI remains the source of truth for full suites. Report every actual verification
result, review finding and disposition, PR URL, merge SHA, and any unverified item.
