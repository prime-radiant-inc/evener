# Task 109 — PR #1145 RoboRev round 18

Status: done. Three commits, main merged, pushed, PR body appended.

- `249d9bd43` ci: let live members settle who owns a recorded group
- `9a1dbb195` docs: say that make tools needs perl
- `f5ca401e7` ci: name the package-list knobs after what they bound
- Pushed head: `45a99b87c` (merge of `origin/main` @ `f39aa2c83`)

Medium 1 — confirmed live members are now authoritative: the cleanup stops and
retains by pgid and never asks the leader pid, since a group's number stays
reserved while any member lives. Repro: stalling `go list` stand-in, runner's
group TERMed, and a `ps` stand-in that reports every per-process group lookup as
group 1 from the moment the signal lands (recycled-leader simulation), group
listing left real:
```
before  survivors of the recorded group: 1 ; records left: 0
after   survivors of the recorded group: 0 ; records left: 0
```
(both end with no record: the first discarded it without stopping anything, the
second removed it after a confirmed stop)

Medium 2 — perl documented as required for `make tools` in linting.md and in the
installer header, with why and the pointer to the gate's matching requirement. No
fallback, per the ruling.

Low — `EVENER_PACKAGE_LIST_TIMEOUT` / `EVENER_PACKAGE_LIST_ATTEMPTS` (and the
internal `PACKAGE_LIST_*`), testing.md updated, no alias. Verified the renamed
knobs still drive the bound ("attempt 1 of 2 timed out after 2s; retrying").

Gates: `bash -n` on both scripts and the lib before and after the merge;
`MODULES=envvars` PASS 0.40s, `MODULES=.` PASS 60.87s, `make tools-golangci`
against the real network installed the pinned version, `make lint-generated` PASS.

Concerns:
- One old spelling remains, in `docs/superpowers/plans/2026-08-06-selftest-unwind.md`:
  it is a plan record from August referring to a selftest script that no longer
  exists. Editing it would falsify the record, so I left it. Say the word if the
  convention here is to sweep plan docs too.
- Nothing in `.github/` referenced the old names, so no workflow changes.
