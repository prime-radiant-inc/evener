# CI diagnosis: PR #1964 at `3c42857f491fa08fb468990022ade6f8f9717815`

## Attribution

The failure is in unchanged Go retirement code, outside the retained-draft diff. The known #1879 race is a plausible cause, but the saved log omits the inner assertion, so that cause is not confirmed.

- Run: `35426684765`
- Failed job: `tests`, job `105853766064`
- Job command: `ROOT_FULL=1 WEB=0 make test`
- The job reported `root PASS`, then `FAIL agent`; the other completed modules passed.
- The only failure named by the retained job log was `TestRetirementPreparationMalformedDescriptorStaysResident (0.07s)`.
- GitHub's retained wrapper log points to the runner-local survey log, but does not include the inner `t.Fatal` text:
  `/tmp/evener-module-tests.G5krIv/tmp/agent/agent-test-shards.28249/survey.log`
- Downloaded job log for this diagnosis:
  `/tmp/evener-ci-1964-tests-105853766064-rest.log`

The test source at the exact head has these possible assertions:

- `agent/session_retirement_prepare_test.go:403`: `idle delegate not claimable: %+v %v`
- `agent/session_retirement_prepare_test.go:415`: `preparation accepted a malformed delegate descriptor log`
- `agent/session_retirement_prepare_test.go:417`: `t.Fatal(err)` from `c.Abort`

The retained CI log does not establish which of these fired. The likely failure is the first assertion: `retirementIdleDelegate` settles a root `ProcessInput` and returns before the asynchronous session namer sender has joined, so the immediate raw `c.TryClaim(true)` can observe the `autonomous` retirement blocker. This is the same mechanism documented in issue #1879.

## Changed-path comparison

PR #1964 at `3c42857f...` changes only these four `mobile-native` files:

- `mobile-native/src/NativePreferencesProvider.test.tsx`
- `mobile-native/src/NativePreferencesProvider.tsx`
- `mobile-native/src/nativePreferences.test.ts`
- `mobile-native/src/nativePreferences.ts`

It changes no `agent/` files. The failing test and its helper were introduced before this PR in commit `69c48e8f56986bca694b884ea28829cba389cf03` (`#1248`), and `agent/session_retirement_prepare_test.go` is byte-identical between base `36c2d4768e4a13055398292103e5ea7dece3ed9e` and head `3c42857f491fa08fb468990022ade6f8f9717815` (SHA-256 `942ed5986a75677978bb7a963f90bf640b5085100714410bc9aab418d78089ba`).

Issue #1879 documents this family of asynchronous namer/autonomous-blocker races at raw retirement claim call sites. Issue #1394 describes a different failure: a `race-modules/agent` ten-minute timeout under `-race`; this run's `race-modules / agent` check was green and the failed check was the ordinary `tests` job.

No code, refs, CI state, or PR state were changed, and no tests were rerun.
