# Task 5 Installation Singleton Race Fix Report

## RED

Added `TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton` with a
controlled afero wrapper. All callers are released from a common initial-read
barrier; temporary creation is coordinated; each unconditional rename is then
allowed only after the prior caller has reread. This deterministically forces the
old implementation's writers to overwrite one another and returned different IDs.

Exact RED command:

```text
$ (cd agent && go test ./internal/installid -run TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton -count=1 -v)
=== RUN   TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton
    installation_id_test.go:122: callers returned different IDs: first "033pIe1s3yKonR3m3qSmaQ", got "033pIe1s3xiAoA2JoGurqJ"
--- FAIL: TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton (0.00s)
FAIL
FAIL	primeradiant.com/serf/agent/internal/installid	0.269s
```

The same RED phase also exposed the missing contention and cleanup behavior before
production changes: the contention-winner test observed the generated ID instead
of the injected winner, and lock-cleanup tests had no lock protocol to exercise.

## Fix / protocol

`LoadOrCreateInstallationIDWithFS` now uses a same-directory
`installation_id.lock` acquired with `OpenFile(O_WRONLY|O_CREATE|O_EXCL, 0600)`.

- Initial valid singleton reads still return immediately.
- Missing/invalid values create the state directory, then attempt the lock.
- On lock contention, callers reread and validate the singleton; if not yet
  present they retry through a bounded 32-attempt loop. There is no unbounded
  spin and no process-local-only mutex.
- The lock owner closes/syncs the lock, rereads under ownership, and returns an
  existing valid winner without writing.
- Only the owner writes a same-directory temporary file, syncs/closes it,
  applies mode `0600`, and atomically renames it while holding the lock.
- The owner rereads and validates after rename before returning. A generated ID
  is never returned unless it is the validated stored singleton.
- Owned lock files are removed on every owner path, including lock sync/close
  failure, temporary write failure, rename failure, and success. A contended
  caller never removes a lock it did not acquire.
- Existing afero race-winner/write-failure behavior remains covered.

Added/retained tests cover:

- deterministic multi-caller serialization and stored singleton equality;
- lock contention where another writer has already installed a valid winner;
- owned lock cleanup after temporary write failure;
- owned lock and temporary cleanup after rename failure;
- no temporary residue and mode `0600` after success;
- existing legacy/invalid replacement, valid reuse, empty-state, read-only, and
  race-winner tests.

Also corrected the stale Google stream fuzz comment from ULID wording to compact
synthetic identifier wording.

## GREEN evidence

```text
$ (cd agent && go test ./internal/installid -count=1)
ok  	primeradiant.com/serf/agent/internal/installid	0.191s

$ (cd agent && go test ./internal/installid ./internal/jobstore -count=1)
ok  	primeradiant.com/serf/agent/internal/installid	0.193s
ok  	primeradiant.com/serf/agent/internal/jobstore	2.883s

$ (cd agent && go test . -run 'Test.*Session.*ID|Test.*Fork|Test.*AttemptGroup' -count=1)
ok  	primeradiant.com/serf/agent	1.397s

$ (cd llm && go test ./providers/google -count=1)
ok  	primeradiant.com/serf/llm/providers/google	0.522s
```

Production audit:

```text
$ rg -n --glob '*.go' --glob '!**/*_test.go' 'ulid\.(Make|New)|oklog/ulid' .
# no matches
```

`gofmt` completed for all changed Go files. `git diff --check` passed.

## Changed files

- `agent/internal/installid/installation_id.go`
- `agent/internal/installid/installation_id_test.go`
- `llm/providers/google/stream_fuzz_test.go`
- `.superpowers/sdd/task-5-fix-report.md` (ignored report artifact; not committed)

No scratch ledger, Task 6 files, `progress.md`, or Task 1 report was edited.
The pre-existing `.superpowers/sdd/task-1-report.md` remains the only unrelated
worktree modification.

## Commit

Separate commit succeeded without amending `6b7ba3b48`:

```text
$ git commit -m 'fix(installid): serialize singleton replacement'
[identifier-refactor bd7d56f24] fix(installid): serialize singleton replacement
bd7d56f248218871b070b4bb49cd5e3b5e78b11b
```

The commit command printed an environment warning that it could not create
`packed-refs.lock` (`Operation not permitted`) but exited successfully and
created the commit. Final `HEAD` is
`bd7d56f248218871b070b4bb49cd5e3b5e78b11b`.

## Concerns

The bounded retry count is intentionally finite. If a separate process leaves a
stale lock indefinitely and no valid singleton exists, the caller returns empty
rather than deleting an unowned lock or spinning forever. A valid existing winner
is always preferred during contention.

## Follow-up contention-wait correction

Parent verification reproduced a second race in the first lock fix:

```text
$ (cd agent && go test ./internal/installid -run 'TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton|TestLoadOrCreateInstallationID_Lock' -count=25)
--- FAIL: TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton (0.00s)
    installation_id_test.go:122: caller returned invalid ID "": invalid UUID payload
FAIL
```

Root cause: contenders exhausted 32 tight `O_EXCL` attempts while the owner was
still syncing/writing/renaming. They observed contention with no valid singleton,
immediately retried without yielding, then returned empty before the owner could
publish the winner.

The correction adds `installationIDContentionWait`, invoked only after
`os.IsExist(err)` and an unsuccessful valid-winner reread. Production uses a
bounded 5ms wait for 100 attempts: maximum contention wait is 500ms. Non-contention
errors do not wait, and callers never remove an unowned lock. The test replaces
the seam with a channel-controlled hook: it proves the waiter reached contention,
installs the winner, removes the owner lock, releases the hook, and asserts the
waiter returns that winner. No test synchronization uses sleeps or timing.

The overlap wrapper was reworked to inspect the actual lock file rather than use a
`lockSeen` shortcut. Its channel barriers now coordinate true pre-lock callers,
while the locked implementation runs through its real lock path.

Required stability command after the correction:

```text
$ (cd agent && go test ./internal/installid -run 'TestLoadOrCreateInstallationID_ConcurrentCallersShareSingleton|TestLoadOrCreateInstallationID_Lock' -count=50)
ok  	primeradiant.com/serf/agent/internal/installid	1.260s
```

Final required GREEN evidence after the correction:

```text
$ (cd agent && go test ./internal/installid -count=1)
ok  	primeradiant.com/serf/agent/internal/installid	0.208s

$ (cd agent && go test ./internal/installid ./internal/jobstore -count=1)
ok  	primeradiant.com/serf/agent/internal/installid	0.213s
ok  	primeradiant.com/serf/agent/internal/jobstore	2.571s

$ (cd agent && go test . -run 'Test.*Session.*ID|Test.*Fork|Test.*AttemptGroup' -count=1)
ok  	primeradiant.com/serf/agent	1.506s

$ (cd llm && go test ./providers/google -count=1)
ok  	primeradiant.com/serf/llm/providers/google	0.411s
```

The production ULID audit still returned no matches; `gofmt` and `git diff --check`
also passed. The follow-up fix is included in a separate commit with message
`fix(installid): wait for singleton lock owner`; its hash is recorded in the
final response.

## Final review documentation fixes

Accepted Minor finding 1: `llm/providers/google/stream_fuzz_test.go` described
Gemini's synthetic tool-call ID as `call_` plus a random ULID. The comment now
describes the compact identifier-domain ID (`call_` plus a fixed-width payload)
and explains that normalization remains necessary because it is random per
parse.

Accepted Minor finding 2: `agent/internal/worktree/marker.go` described session
IDs as bare ULIDs and delegate IDs as `dlg_<ulid>`. The comment now describes
the unprefixed fixed-width compact session payload and `dlg_` plus a compact
payload for delegate IDs.

Exact files changed:

- `llm/providers/google/stream_fuzz_test.go`
- `agent/internal/worktree/marker.go`
- `.superpowers/sdd/task-5-fix-report.md`

Commands and results:

```text
$ go test ./llm/providers/google
FAIL (sandbox denied httptest listener bind: operation not permitted)
$ go test ./agent/internal/worktree
FAIL (sandbox denied temporary git/xcrun cache creation: operation not permitted)
$ git diff --check
PASS
$ git status --short
 M .superpowers/sdd/task-1-report.md
 M agent/internal/worktree/marker.go
 M llm/providers/google/stream_fuzz_test.go
```

Self-review: only the two requested source comments and this appended report
section were changed; no formatting was required, and the pre-existing Task 1
report modification was not staged. Both focused test commands were attempted;
their failures are environment permission failures before assertions ran.
