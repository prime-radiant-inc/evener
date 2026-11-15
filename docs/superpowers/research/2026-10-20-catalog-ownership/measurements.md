# Catalog layer sharing: paired evidence

Sharing immutable capability-row maps reduces marginal retained heap per additional embedded registry from **8.289 MiB to 4.357 MiB (47.44%)**. Warm-load allocation falls **38.03%**, from 11,577,395 to 7,174,353 bytes per load. These are offline registry-fixture results, not whole-daemon or per-delegate savings.

## Source and execution identity

- Baseline product: `f18fe2290a5c1037aab21bd3201ce20bf09c9862`.
- Baseline checkout: `a16d1e78a0a49ade517f5466ff376ecb93d36dc0`, the audit branch. Before building, `git diff --exit-code f18fe2290a -- llm go.mod go.sum go.work` confirmed identical product code.
- Measured candidate: `1f1b3dccdac5f4311b0f877902e4834f28d56208`.
- Reviewed cleanup: `da907d825a33c641e287d774ad28c89ab78504be`. Its diff changes test setup only: existing fixture/resolve/load helpers, `testing.TB` helper typing, direct map mutation, and an independent fixture-owned preservation oracle. `load.go` is byte-identical to the measured candidate. The measured benchmark body and setup options are equivalent; the raw measurements below deliberately retain their original candidate SHA.
- Go 1.27.0 linux/amd64, `GOMAXPROCS=2`, builds serialized with `-p=1`. Each sample is a fresh subprocess. Compilation is outside measurement. No live provider requests, credentials, hub restart or deployment.
- [build-manifest.json](build-manifest.json) records both checkouts, compiler, all six binary hashes and source hashes. Identical benchmark source was overlaid into both test binaries. [benchmark-samples.md](benchmark-samples.md) preserves the five-repetition allocation/wall-time benchmarks and source blob identities.

## Retained heap and first-load allocation

Three alternating pairs produced 36 checkpoints in [retention-pairs.json](retention-pairs.json). Each process loads eight independent registries, keeps them alive through its checkpoints, then releases them and forces GC. All populated checkpoints contain 219 providers.

| Metric, median across three processes | Before | After | Change |
| --- | ---: | ---: | ---: |
| Post-GC heap, one registry, MiB | 14.266 | 10.333 | -27.56% |
| Post-GC heap, two registries, MiB | 22.554 | 14.724 | -34.72% |
| Post-GC heap, eight registries, MiB | 72.292 | 40.832 | -43.52% |
| Marginal heap per additional registry, MiB | 8.289 | 4.357 | -47.44% |
| Post-release heap, MiB | 5.940 | 5.940 | unchanged at this precision |
| First-load cumulative allocation, MiB | 33.048 | 28.848 | -12.71% |

Marginal heap is `(holding_8_post_gc - holding_1_post_gc) / 7`, computed separately for each process before taking the median. First-load allocation is the difference in `TotalAlloc` between the initial post-GC checkpoint and the first post-load checkpoint. It is not warm allocation or peak memory.

The retained result is expected for the embedded catalog, whose source maps remain process-global. A parsed custom/cache input can instead become the sole retained map and need not show the same retained saving. Ordinary fresh and restored delegates already share their parent's registry. Eight synthetic roots are not eight delegates. The unchanged post-release heap means this patch does not remove the process-global parsed catalog.

## Startup peak and CPU

**Superseded 2026-11-13 (#4799).** The rusage table below was measured with
Linux `wait4.ru_maxrss`, whose getrusage counters survive `exec` and are
floored by the launcher's own image, so the values could not exclude that
floor. [startup-pairs.json](startup-pairs.json) keeps the original samples as
history; the executed-image VmHWM rerun recorded beneath them supersedes the
table. The `peak_rss_kib`/`peak_rss_bytes` fields in the retained evidence
files (startup, retention and CPU pairs) were rusage-sourced under that old
name; new runs report the value as `rusage_maxrss_kib` and reserve
`peak_rss_kib` for the self-reported VmHWM.

Original rusage-based pairs (superseded; the recorded launcher was small
enough that the floor did not move them materially, which the rerun proves
was luck of the launcher's size rather than a property of the metric):

| Pair | Before MiB | After MiB |
| --- | ---: | ---: |
| 1 | 30.500 | 29.250 |
| 2 | 30.625 | 29.250 |
| 3 | 29.250 | 29.250 |

[startup-pairs-vmhwm.json](startup-pairs-vmhwm.json) records the revalidated
pairs: the same pinned before/after inputs and one-load workload, three
alternating pairs, each probe self-reporting its executed-image peak
(`/proc/self/status` VmHWM, parsed strictly as a single `kB` value) with
rusage retained only as a diagnostic. VmHWM is approximate kernel RSS
accounting, so single-run differences of a few MiB are noise:

| Pair | Before VmHWM MiB | After VmHWM MiB |
| --- | ---: | ---: |
| 1 | 31.070 | 29.527 |
| 2 | 30.980 | 29.805 |
| 3 | 29.734 | 27.184 |

No pair increased in the pad-0 rerun. Three single-run pairs inside a
run-to-run spread of several MiB establish the absence of a large
startup-RSS regression, not a stable saving: the pad-40 control below shows
pair 3's after-side reading (34.004 MiB) above its before-side (31.070 MiB),
and the same after-side binary moved 27.184 → 34.004 MiB between the two
runs. Peak RSS includes runtime, executable pages and probe
code; it is not a Go-heap high-water mark or daemon RSS.
[startup-pairs-vmhwm-parentpad40.json](startup-pairs-vmhwm-parentpad40.json)
is the sensitivity control: the same pairs run while the driver holds 40 MiB
of parent-only memory. Every rusage diagnostic floors at 68.875 MiB — the
inherited-image floor #4799 describes, which the old metric would have
reported as the startup peak — while the executed-image peaks stay in the
same band (27.2–34.0 MiB across both runs). Startup user+system CPU seconds
in the rerun were `0.077074 / 0.072471`, `0.069709 / 0.072027`, and
`0.068505 / 0.071914` before/after; the original readings
(`0.080634 / 0.077607`, `0.079022 / 0.078037`, `0.091114 / 0.092194`) remain
valid observations of their runs, because getrusage CPU counters are not
affected by the RSS floor. No stable startup CPU improvement is claimed. Rerun
provenance — Go 1.27.4 (the recorded run used 1.27.0; a different compiler
changes binary hashes without changing product source), `GOMAXPROCS=2`, and
the probe, driver, helper and binary hashes — is the
`startup_vmhwm_rerun_2026_11_13` block of [build-manifest.json](build-manifest.json).

## Lookup and list CPU

The [resolve samples](cpu-resolve-pairs.json) and [initial list samples](cpu-model-ids-pairs.json) each contain three alternating pairs of 100,000 timed operations. CPU includes process startup, registry setup and benchmark warmup. These costs are explicitly separate from the benchmark's wall-clock ns/op. Each run validates its exact benchmark name, operation count and PASS result.

Resolve user+system CPU was lower on the candidate in all initial pairs (`4.307251 / 3.521359`, `3.468678 / 3.280039`, `3.446649 / 3.179680` seconds), while allocation remained 231 objects per operation. This is fixture-level observed CPU, not an isolated per-call or whole-daemon CPU saving.

Initial model-list CPU was noisy (`1.019794 / 1.009841`, `0.975681 / 1.028936`, `0.972433 / 0.992982` seconds). Allocation remained 6,136 B/op and 15 allocs/op. The [longer follow-up](cpu-model-ids-long-pairs.json) used five alternating pairs of 1,000,000 timed list operations per process. Before/after CPU seconds were `9.396186 / 8.844553`, `9.067104 / 8.920355`, `9.032648 / 9.013752`, `10.474997 / 9.332877`, and `9.984049 / 9.697963`. No pair increased, and the after-side median was 4.07% lower. The initial increase did not repeat. This supports no observed list-CPU regression in the tested fixture; it does not establish a stable production CPU percentage saving.

## Behavior verification

The ownership test failed against the baseline and passed after sharing. Existing preservation cases passed on both. An intentional in-place filtering mutation failed the inheritance-preservation test and passed after restoration. The full registry suite, full registry race suite, selected client integration tests, package vet and package lint passed at the implementation revision. The test-only cleanup reran focused and full registry tests, benchmark smoke, formatting, vet and lint; it did not rerun race. The coordinator independently reran the final registry suite (exit 0, 4.372s) and all seven driver tests.

Two independent final adversarial reviewers approved with no findings. They read the whole product diff, ownership boundaries, probes/drivers, raw samples, manifest and reproduction instructions. Both independently reproduced the reported medians/percentages and checked source hashes, sample identities, pair ordering and RSS conversion. Neither reran runtime workloads; historical build/test provenance remains the recorded execution evidence. Four earlier simplify review angles produced four test cleanups; the coordinator added the independent fixture snapshot correction. Scoped re-review accepted all five, with no skipped or reverted findings.

[PR #4796](https://github.com/prime-radiant-inc/evener/pull/4796) merged as `8b592076562d16a4b4613c0b5efa7276bc80bae2`. CI run `39497098966` passed all test, race, frontend, native, package and static lanes; build run `39497099170` passed, with deployment skipped. RoboRev's combined review and range review both reported no issues for exact head `dc4c94af3cadcec99e15908042788c4d560e2f1c`. Main had been merged before that push; the registry subtree stayed identical to the reviewed cleanup. The coordinator fetched the squash commit, confirmed its entire tree matched the tested PR head, and reran the registry suite successfully (4.440s). The standing admin authorization was used only for the missing approving-review requirement, with all checks green and main current.

The product worktree and local branch were removed after this verification. The audit evidence remained committed locally until this landing placed the record, amended by the #4799 executed-image VmHWM correction, on main. No hub was restarted or deployed. See [reproduce.md](reproduce.md) for executable build and measurement commands.

## Measurement rulings

1. Require explicit full baseline/candidate revision flags alongside binary paths. Explicit-file Go builds do not reliably embed VCS metadata. Cost: callers must supply two flags, and incorrect labels still require detection through the build manifest.
2. Add fixed-count lookup/list subprocess CPU comparisons because ns/op is wall time. Cost: extra runs and startup/setup overhead, which must remain disclosed.
