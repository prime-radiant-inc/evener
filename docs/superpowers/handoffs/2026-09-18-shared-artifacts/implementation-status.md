# Shared artifacts implementation status

The feature is in progress on `codex/shared-artifacts-integration`. Nothing from
this feature has been merged to main. The executable browser host and the
complete interaction loop are not yet implemented or qualified.

The original [qualification](qualification.md) records the G0 experiments;
its next-step language is historical. The [contract](contract.md) remains the
record of adopted C01–C08 decisions and protocol/dependency qualification.
Those decisions are implementation rulings, not separate product approvals.

## Reviewed foundation

| Slice | Pull request | Reviewed head |
| --- | --- | --- |
| Qualification | [#1924](https://github.com/prime-radiant-inc/evener/pull/1924) | `b3184495d8e8f5ddeb078e1e272bcb8dab3ce577` |
| Domain contracts | [#1926](https://github.com/prime-radiant-inc/evener/pull/1926) | `cd85bc44f8fdc1ba3651409342eb4aef290b2f5a` |
| Durable SQLite store | [#1932](https://github.com/prime-radiant-inc/evener/pull/1932) | `1ca4ab09ccc487265ceef1a89c41e923ea74a909` |
| Shared service | [#1933](https://github.com/prime-radiant-inc/evener/pull/1933) | `b1d7399b326a9964086cd92836f8d9b2a87062f5` |
| Indexed transcript durability | [#1979](https://github.com/prime-radiant-inc/evener/pull/1979) | `898bd64190a87357b5d58fa325a0d62b9e4bece7` |
| Durable Hub authority | [#1987](https://github.com/prime-radiant-inc/evener/pull/1987) | `a65c970c299314bb2250e381394455e4a1ce8a2d` |
| Managed invocation runtime | [#1986](https://github.com/prime-radiant-inc/evener/pull/1986) | `f260c273e0f3640e18145855df74d645f0852e31` |

The runtime journals immutable invocation bytes and original outcomes until
transcript durability is established. Current authorization is checked during
recovery. Compaction retains canonical occurrences through direct transcript
references in one final durable marker, preserving recovered call/result pairs
and existing note, skill and delegate obligations through reopen. The neutral
MCP host result remains separate from model-facing text. A UI reference and
its projection are subsequent work.

## Recorded validation

All results below used Go 1.27.0 on Darwin arm64. Default tests were
credential-free; no live-model trial ran.

- At runtime head `8a7967916`, the canonical
  `MODULES='agent llm' WEB=0 scripts/gate/run-module-tests.sh -count=1` passed:
  agent 17.83s and llm 8.42s. Focused race, lint, vet and tagged compile checks
  also passed; their narrower scopes are recorded in the PR.
- After merging the reviewed transcript and backend parents at `f260c273e`,
  focused real Session/transcript recovery tests passed in 4.795s / 1.409s.
  `MODULES=. ROOT_FULL=1 WEB=0 scripts/gate/run-module-tests.sh -count=1`
  passed in 121.50s, covering the root-module Hub/daemon/projector consumers.
- After assembling the reviewed authority and runtime at `5eeba11d3`, the same
  full root-module gate passed in 119.39s. Authority tests exercised real SQLite
  commits followed by lost ensure/tombstone acknowledgments, and an owned Hub
  startup process killed before deletion import. Final parent-directory retry
  regressions passed (0.503s), matching race (1.515s), vet and pinned lint.
- Transcript head `898bd6419` passed its package tests (0.689s), race tests
  (1.950s), vet and pinned golangci-lint 2.13.1. Real OS sync/rollback faults
  verify indexed retained-record identity and settlement without reappend.
- The service tests exercised 100 direct SDK clients and one owned backend.
  The zero-shim claim still needs independent process classification; the
  existing fixture's literal zero is not measured evidence. This is not the
  required 100 actual agent constructions/acquisitions test.

Existing failed runs were retained during development, including a restored
session naming race and two canonical agent fixture failures. Subsequent fixes
and passing commands do not relabel those failed runs as passes. Three first-round
authority regression failures were collected against pre-fix code after the fix
was written; this was test-after-implementation evidence, not TDD. Both final
parent-path retry regressions failed before their production fixes. These results
are not a claim that every repository gate passed at the combined head.

The exact-head external review of runtime PR #1986 raised a result-pairing
metadata concern. Source and existing production-path tests showed that the
fields are recovery-only annotations and are already attached to unpaired
recovered results. The finding was ruled unsubstantiated without a reachable
failing interleaving; no production change or new test run was made for it. The
external review body remains recorded, rather than described as a clean review.

The exact-head external authority review of PR #1987 identified production
delete hooks and child grant revocation, both already assigned to the next
lifecycle slice. Its entropy-error finding does not apply to the pinned Go 1.27
`crypto/rand.Read`, which fills the buffer or terminates. Its suggestion to
accept unsupported directory sync would weaken the required durability
guarantee and was rejected; unsupported filesystems remain unqualified. Two
small cleanup findings are retained for the lifecycle slice. This disposition
is source analysis, not a new test run or a clean external-review claim.

## Remaining gates

Durable Hub authority is reviewed and assembled with the runtime. Private daemon bootstrap,
current lineage/lease fencing, the concrete bundled adapter and 100 actual agent
construction/acquisition tests remain. Then come MCP result/reference projection,
the comparison-table interaction through the real host/helper/runner, pane and
recovery lifetimes, security/scale qualification and operational documentation.

A01–A22 are not complete. Backend tests provide component evidence for receipt,
ordering, authorization, quota and restart cases; they do not substitute for
integrated agent or browser paths. The Chrome 153/macOS 26.5.2 G0 experiment is
a dependency/policy experiment, not production sandbox qualification. Other
browser families, Linux runtime, physical disk-full/power-loss behavior and
live-provider behavior have not been qualified. Raw execution remains gated.
