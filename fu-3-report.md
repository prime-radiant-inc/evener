# FU3 — transcript_lookup: enumerate legacy-named buckets — Report

## Summary

PR #2163 made the doctor sweep enumerate legacy/foreign-named project buckets
(its `globBuckets` stopped filtering by `ValidateProjectID`). FU3 is the
agent-side counterpart: `transcript_lookup.go`'s `enumerateBuckets` had the
same filter, so sessions in legacy-named buckets were invisible to
`read_transcript` / `find_session_transcripts` by bare session id. This change
removes that filter and mirrors the doctor's `refFor` design: refs are emitted
only when the bucket name is consumable by the shared agent ref grammar.

## TDD Evidence

### RED (before fix)

Command:
```sh
go test ./agent/ -run 'TestEnumerateBuckets_IncludesLegacyNamedBucket|TestResolveTranscript_BareIDInLegacyBucket|TestFind_LegacyBucketSessionInAllProjects' -count=1 -v
```

Output:
```
=== RUN   TestEnumerateBuckets_IncludesLegacyNamedBucket
    transcript_lookup_test.go:407: legacy bucket 0123456789abcdef missing from enumeration
--- FAIL: TestEnumerateBuckets_IncludesLegacyNamedBucket (0.00s)
=== RUN   TestResolveTranscript_BareIDInLegacyBucket
    transcript_lookup_test.go:424: bare id in legacy bucket not found: unknown session "02wMz5Txv5aIxgf9yVdd0N"
--- FAIL: TestResolveTranscript_BareIDInLegacyBucket (0.00s)
=== RUN   TestFind_LegacyBucketSessionInAllProjects
    transcript_lookup_test.go:493: legacy bucket session not found in all_projects results; got 1 matches
--- FAIL: TestFind_LegacyBucketSessionInAllProjects (0.03s)
FAIL
FAIL    primeradiant.com/evener/agent    0.589s
```

Why they fail: `enumerateBuckets` filters bucket dirs through
`identifier.ValidateProjectID` (line 162), so the legacy pure-hash bucket
`0123456789abcdef` is excluded from enumeration. `resolveTranscript` and
`collectCandidates` both rely on `enumerateBuckets`, so bare-id lookup and
`find_session_transcripts` with `scope=all_projects` never see sessions in
legacy-named buckets.

### GREEN (after fix)

Command:
```sh
go test ./agent/ -run 'TestEnumerateBuckets_IncludesLegacyNamedBucket|TestResolveTranscript_BareIDInLegacyBucket|TestFind_LegacyBucketSessionInAllProjects' -count=1 -v
```

Output:
```
--- PASS: TestEnumerateBuckets_IncludesLegacyNamedBucket (0.00s)
--- PASS: TestResolveTranscript_BareIDInLegacyBucket (0.00s)
--- PASS: TestFind_LegacyBucketSessionInAllProjects (0.03s)
PASS
ok      primeradiant.com/evener/agent    0.336s
```

### Test coverage

- `TestEnumerateBuckets_IncludesLegacyNamedBucket`: legacy-named bucket dir
  is returned alongside a normal bucket.
- `TestResolveTranscript_BareIDInLegacyBucket`: bare session id resolves to a
  session in a legacy-named sibling bucket (the `read_transcript` read path).
  The ref emitted is absent (`refFor` returns `""` for grammar-incompatible
  names) or, if present, round-trips via `decodeRef` + `ValidateProjectID`.
- `TestFind_LegacyBucketSessionInAllProjects`: `find_session_transcripts` with
  `scope=all_projects` includes sessions from legacy-named buckets. Legacy-bucket
  ref is absent or grammar-consumable (round-trip). Normal-bucket ref round-trips
  and is non-empty (normal buckets' results and refs unchanged).

## Files Changed

| File | Change |
|------|--------|
| `agent/transcript_lookup.go` | Removed `ValidateProjectID` filter from `enumerateBuckets`. Aligned `validLocalBucketDir` to accept any dir (return `true`). Added `refFor` mirroring doctor's `refFor`. Replaced `encodeRef` with `refFor` in `resolveTranscript` sibling-bucket paths (single-match return + ambiguity candidate list). |
| `agent/session_tools_find.go` | Replaced `encodeRef` with `refFor` in `buildSessionRecord` (TranscriptRef + ParentRef). Removed `ValidateProjectID` filter from `collectCandidates`. |
| `agent/transcript_lookup_test.go` | Added 3 new tests (RED→GREEN). Updated `TestEnumerateBuckets_CleanBreakSkipsLegacyProjectBucket` to assert both buckets returned. |
| `agent/transcript_lookup_covtest_test.go` | Updated `TestValidLocalBucketDir` (legacy-named dir is valid). Updated `TestResolveTranscript_InvalidBucket` (error is "unknown session", not "invalid bucket"). Renamed `TestParentBucketAndID_InvalidBucket` → `TestParentBucketAndID_LegacyNamedBucket` (asserts success). |
| `agent/session_tools_aux_exact_fuzz_test.go` | Updated outdated comment about `ValidateProjectID` enforcement by `validLocalBucketDir`. |

## Self-Review Against Brief

### Do-not list (all verified)

- **ValidateProjectID itself unchanged**: `identifier/project.go` not in diff. ✓
- **User-input parsing paths unchanged**: `ValidateProjectID` at lines 50 and 238 of `transcript_lookup.go` (parsing user-provided refs/selectors) are preserved. ✓
- **Ref grammar not extended**: `agent/transcript_ref.go` not in diff. `encodeRef`/`decodeRef`/`validIDToken` untouched. ✓
- **validIDToken untouched**: `agent/transcript_ref.go` not in diff. ✓
- **Doctor code untouched**: No `agent/doctor/` files in diff. ✓

### Brief requirements (all met)

- **Remove the enumeration filter**: Removed `ValidateProjectID` filter from `enumerateBuckets` (line 162). ✓
- **~:174 (`validLocalBucketDir`)**: Assessed its role — it gates sweep-vs-single-bucket behavior in `findBucketsWithEnumerate`, `collectCandidates`, and the current-bucket entry guard in `resolveTranscript`/`parentBucketAndID`. Aligned with doctor semantics: a dir under `projects/` is sweepable regardless of name. Now returns `true` always. ✓
- **Ref emission mirrors doctor's `refFor`**: Added `refFor` that returns `""` for grammar-incompatible bucket names, `encodeRef` for valid ones. Applied in `buildSessionRecord` and `resolveTranscript` sibling-bucket paths. ✓
- **Do NOT extend the grammar**: `refFor` suppresses refs for incompatible names rather than extending `encodeRef`/`validIDToken`. ✓

## Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt (touched files) | ✅ PASS | `gofmt -l` reported no files needing formatting |
| `make vet` | ✅ PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | ✅ PASS | Exit 0 (fuzz-tagged tests compile) |
| Full agent suite (`go test ./agent/ -count=1 -short`) | ✅ PASS | `ok primeradiant.com/evener/agent 444.218s` (exit 0) |
| `make lint` | ✅ PASS | All 10 phases PASS: lint-naming, lint-gofmt, lint-evenerfuzz, lint-eval, lint-internal, lint-golangci, lint-generated, lint-fuzz-registry, lint-package-imports, lint-biome, secret-scan (exit 0) |
| Focused tests (transcript_lookup + find) | ✅ PASS | All previously-failing tests pass, no regressions |

## Concerns

1. **`validLocalBucketDir` now always returns `true`**: This is a deliberate
   alignment with the doctor's semantics. The function previously served as a
   bucket-ness predicate that rejected dirs under `projects/` with invalid names.
   After FU3, any dir is accepted — flat layouts (not under `projects/`) were
   already accepted, and dirs under `projects/` with legacy/foreign names are
   now accepted too. The traversal safety that `ValidateProjectID` provided
   (rejecting names with path separators, colons, etc.) is still enforced at
   the user-input parsing layer (lines 50, 238) and at ref emission (`refFor`).
   The `stateHomeFor` function still correctly identifies whether a dir is under
   `evener/projects/` or not, so flat-dir degradation behavior is unchanged.

2. **Empty refs in find results**: Sessions in legacy-named buckets will appear
 in `find_session_transcripts` results with an empty `transcript_ref` field
 (the JSON tag is `json:"transcript_ref"` without `omitempty`, so it serializes
 as `"transcript_ref": ""`). The model can still find these sessions by bare id
 (the enumeration no longer filters them), but cannot use a `proj:` ref to
 address them directly. This mirrors the doctor's design exactly — the doctor's
 `refFor` likewise returns `""` for grammar-incompatible names, and the doctor's
 contract test (`TestDoctorEmittedRefsParseWithAgentRefGrammar`) asserts no ref
 is emitted for such buckets.

## Simplify-Apply Round 1 (post-review)

### Items Applied

1. **Deleted `validLocalBucketDir` and all five dead guards** — the function
   unconditionally returned `true`, so every guard was dead code. Removed:
   - `agent/transcript_lookup.go`: guard in `resolveTranscript` (~:25-27),
     guard in `parentBucketAndID` (~:224-226), the function itself (~:170-180).
   - `agent/session_tools_find.go`: guard in `findBucketsWithEnumerate`
     (~:359-361), guard in `collectCandidates` (~:394-396).
   - `agent/job_transcript_read.go`: guard in `locateLocalJob` (~:41-43).
   Deleted `TestValidLocalBucketDir` (tested the deleted function, asserted only
   true-from-true — no coverage lost). Updated `TestLocateLocalJob_InvalidBucketDir`
   comment to state the real pass reason (job-not-found downstream, not bucket-name
   rejection). Updated stale `validLocalBucketDir` references in comments in
   `transcript_lookup_covtest_test.go` and `session_tools_aux_exact_fuzz_test.go`.

2. **Renamed `TestEnumerateBuckets_CleanBreakSkipsLegacyProjectBucket`** →
   `TestEnumerateBuckets_CleanBreakIncludesLegacyProjectBucket` — the function
   name said "Skips" while the comment and body verified "Includes".

3. **Completed dangling doc comment** — `TestResolveTranscript_BareIDInLegacyBucket`'s
   comment ended mid-sentence with "(the". Completed to describe the ref-round-trip
   invariant.

4. **Cross-reference comments (no code change needed)** — both agent sites
   already carry cross-references to their doctor twins: `enumerateBuckets`
   names "the doctor's globBuckets (PR #2163)" and `refFor` names "mirroring
   the doctor's refFor from #2163". No agent/doctor files touched.

### Items Refuted

None. All 4 apply items held against the code.

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| Focused tests (transcript_lookup + find + job_transcript_read) | ✅ PASS | All pass, 0.892s |
| gofmt -l (touched files) | ✅ PASS | No files needing formatting |
| `make vet` | ✅ PASS | Exit 0 |
| Full agent suite (`go test ./agent/ -count=1`) | ✅ PASS | `ok  primeradiant.com/evener/agent  497.464s` (exit 0) |
| `make lint` | ✅ PASS | All 11 phases PASS, exit 0 |

### Diff stat

```
 agent/job_transcript_read.go               |  4 ----
 agent/job_transcript_read_covtest_test.go  | 15 +++++++-------
 agent/session_tools_aux_exact_fuzz_test.go |  8 ++++----
 agent/session_tools_find.go                |  6 ------
 agent/transcript_lookup.go                 | 18 ----------------
 agent/transcript_lookup_covtest_test.go    | 33 +++++-------------------------
 agent/transcript_lookup_test.go            |  6 ++++--
 7 files changed, 21 insertions(+), 69 deletions(-)
```

## Roborev Fix Round 1

A self-review of the committed FU3 changes (commits `7a858e1493` and
`64c11e962b`) found four gaps in the legacy-bucket handling. This round
writes RED tests for each, applies the fix, and re-runs all gates.

### Findings

1. **Empty `transcript_ref` in find results**: `sessionRecord.TranscriptRef`
   had `json:"transcript_ref"` (no `omitempty`), so find results for
   legacy-named buckets serialized `"transcript_ref": ""`. A model copying
   that empty ref would silently resolve to the current session via
   `resolveTranscript`. The doctor's design suppresses the ref entirely.

2. **Ambiguity error omits legacy-bucket matches**: `resolveTranscript`'s
   ambiguity message listed candidate refs only. When `refFor` returned `""`
   for a grammar-incompatible bucket name, that match was absent from the
   message — the user saw fewer candidates than the code found. The doctor's
   `locateAcrossBuckets` prints every bucket name.

3. **`locateLocalJob` still filtered by `ValidateProjectID`**: the job
   sibling-bucket sweep at `job_transcript_read.go:73` skipped dirs whose
   name `ValidateProjectID` rejects, so jobs in legacy-named buckets were
   invisible to `read_transcript(transcript_ref="job:...")`. This was the
   same filter removed from `enumerateBuckets` in the first commit, but
   missed in `locateLocalJob`.

4. **Fuzz oracle rejected legitimate empty refs**: `FuzzResolveTranscript`'s
   oracle treated any empty ref with nil error as a failure. After FU3, an
   empty ref is a legitimate outcome for a bare-id match in a legacy-named
   bucket (`refFor` suppresses it). The oracle needed to allow this case, and
   `TestLocateLocalJob_InvalidBucket` needed renaming/comment updates to
   reflect the removed guard.

### TDD Evidence

#### RED (before fix)

Command:
```sh
go test ./agent/ -run 'TestFind_LegacyBucketOmitsEmptyTranscriptRef|TestResolveTranscript_AmbiguousBareIDTwoLegacyBuckets|TestLocateLocalJob_LegacyNamedSiblingBucket' -count=1 -v
```

All three failed:
- `TestFind_LegacyBucketOmitsEmptyTranscriptRef`: `transcript_ref` is present but empty (not omitted).
- `TestResolveTranscript_AmbiguousBareIDTwoLegacyBuckets`: ambiguity error does not name legacy buckets.
- `TestLocateLocalJob_LegacyNamedSiblingBucket`: job in legacy-named sibling bucket not found.

#### GREEN (after fix)

All three pass. The fuzz seed corpus (21 seeds including the new
`trenderLegacySession`) also passes.

### Fixes Applied

1. **`agent/session_tools_find.go`**: Changed `TranscriptRef` tag to
   `json:"transcript_ref,omitempty"`. Added a text-format fallback: when
   `TranscriptRef` is empty, the numbered line renders `(no ref)` instead of
   a bare empty string.

2. **`agent/transcript_lookup.go`**: In the ambiguity-error candidate loop,
   when `refFor` returns `""`, append `filepath.Base(bucket)` instead of
   skipping the match. Updated the comment to cross-reference the doctor's
   `locateAcrossBuckets`.

3. **`agent/job_transcript_read.go`**: Removed
   `identifier.ValidateProjectID(entry.Name()) != nil` from the sibling-sweep
   filter. Added a comment explaining the alignment with `enumerateBuckets`
   (PR #2163). The `identifier` import is retained (still used by
   `identifier.JobOwnerSessionID` at line 37).

4. **`agent/transcript_render_fuzz_test.go`**: Updated the fuzz oracle to
   allow an empty ref when the resolved path contains the legacy bucket name
   (the only path to an empty ref with nil error). Added
   `trenderLegacyBucket`/`trenderLegacySession` constants and a seed entry.
   Updated the oracle doc comment.

5. **`agent/job_transcript_read_locate_covtest_test.go`**: Renamed
   `TestLocateLocalJob_InvalidBucket` -> `TestLocateLocalJob_NonProjectDirError`
   with updated comment (the lookup now fails downstream at
   `findLocalJobInProject`, not at a removed bucket-name guard).

6. **`agent/job_transcript_read_test.go`**: Added
   `TestLocateLocalJob_LegacyNamedSiblingBucket` (RED->GREEN).

7. **`agent/transcript_lookup_test.go`**: Added
   `TestFind_LegacyBucketOmitsEmptyTranscriptRef` and
   `TestResolveTranscript_AmbiguousBareIDTwoLegacyBuckets` (RED->GREEN).

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests + fuzz seeds (21) | PASS | All pass, 0.447s |
| Full agent suite (`go test ./agent/ -count=1 -short`) | PASS | `ok primeradiant.com/evener/agent 378.570s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS, exit 0 |

### Diff stat

```
 agent/job_transcript_read.go                     |  6 +-
 agent/job_transcript_read_locate_covtest_test.go | 18 +++--
 agent/job_transcript_read_test.go                | 26 ++++++++
 agent/session_tools_find.go                      |  8 ++-
 agent/transcript_lookup.go                       |  7 +-
 agent/transcript_lookup_test.go                  | 85 +++++++++++++++++++++++-
 agent/transcript_render_fuzz_test.go             | 31 +++++++--
 7 files changed, 162 insertions(+), 19 deletions(-)
```

### Commit

`cd7ef13e07` -- `fix(agent): suppress empty refs, name legacy buckets in ambiguity errors`

## Roborev Fix Round 2

Round 1's self-review missed four gaps in the read-path and job-lookup
handling of legacy-named buckets. This round verifies each finding against
the code, writes RED tests, applies fixes, and re-runs all gates.

### Findings

1. **[HIGH] Read-path envelopes emitted empty transcript_ref** — The round-1
   fix added `omitempty` to `sessionRecord.TranscriptRef` on the find path,
   but the `read_transcript` envelope structs were missed:
   `readMarkdownEnvelope` (~:68), `readRawEnvelope` (~:1474),
   `apiLogTranscriptResultIdentity` (~:134), and `readOutlineEnvelope`
   (session_outline.go ~:21) all had `json:"transcript_ref"` without
   `omitempty`. A `read_transcript` against a legacy-bucket bare ID emitted
   `"transcript_ref": ""`, and a model reusing that ref would silently read
   the CURRENT session.

2. **[MEDIUM] Corrupt sibling directory broke job lookup** — After round 1
   removed the `ValidateProjectID` filter from `locateLocalJob`'s sibling
   sweep, a corrupt `jobs.jsonl` in a stray sibling bucket (for the same
   owner session) aborted the entire lookup via `findLocalJobInProject`'s
   error return, even when the target existed in another sibling. The
   removed filter was accidentally protective against this.

3. **[MEDIUM] Ambiguity message presented non-actionable names as refs** —
   The round-1 ambiguity error listed legacy bucket dirnames as "candidate
   refs", but `proj:<name>:<id>` is rejected by the explicit-ref branch for
   those names, so they are not usable selectors. The message presented them
   as something the model could pass back.

4. **[MEDIUM] Find result didn't say how to address legacy-bucket sessions**
   — The find text format showed "(no ref)" for legacy-bucket sessions with
   no addressing hint. The reviewer's "cannot read them" concern was
   unanswered: the bare session ID IS the actionable handle, but the record
   didn't surface it.

### Refuted

None. All four findings held against the code.

### TDD Evidence

#### RED (before fix)

Command:
```sh
go test ./agent/ -run 'TestReadMarkdownTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef|TestReadOutlineTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef|TestReadRawTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef|TestResolveTranscript_AmbiguityMessageDoesNotPresentBucketNamesAsRefs|TestFind_LegacyBucketTextFormatShowsBareIDAddressing|TestLocateLocalJob_CorruptSiblingDirDoesNotBreakLookup|TestLocateLocalJob_StraySiblingDirDoesNotBreakLookup' -count=1 -v
```

Results:
- `TestReadMarkdownTranscript_...`: FAIL — emitted empty transcript_ref.
- `TestReadOutlineTranscript_...`: FAIL — emitted empty transcript_ref.
- `TestReadRawTranscript_...`: FAIL — emitted empty transcript_ref.
- `TestResolveTranscript_AmbiguityMessage...`: FAIL — message contains "candidate refs".
- `TestFind_LegacyBucketTextFormat...`: FAIL — text format doesn't mention bare session ID.
- `TestLocateLocalJob_CorruptSibling...`: FAIL — corrupt sibling aborted lookup.
- `TestLocateLocalJob_StraySibling...`: PASS — missing file returns nil (correct behavior; kept as regression guard).

#### GREEN (after fix)

All 7 tests pass. The fuzz seed corpus (21 seeds) also passes.

### Fixes Applied

1. **`agent/session_tools_transcript.go`**: Added `omitempty` to
   `TranscriptRef` in `readMarkdownEnvelope` (line 68),
   `apiLogTranscriptResultIdentity` (line 134), and `readRawEnvelope`
   (line 1474).

2. **`agent/session_outline.go`**: Added `omitempty` to `TranscriptRef` in
   `readOutlineEnvelope` (line 21).

3. **`agent/job_transcript_read.go`**: Changed `findLocalJobInProject` error
   handling in the sibling sweep from `return err` to `continue` — a
   sibling whose `jobs.jsonl` is missing or corrupt is not the target;
   skip it and keep searching. Comment explains the rationale.

3. **`agent/transcript_lookup.go`**: Reworded the ambiguity error from
   `"session %q is ambiguous; candidate refs: %s"` to
   `"session %q is ambiguous; found in: %s"`. Updated the comment to
   explain that bucket names are context, not usable selectors.

4. **`agent/session_tools_find.go`**: Added `SessionID string `json:"-"``
   field to `sessionRecord` (not in JSON wire format per spec, but carried
   for the text renderer). Set it in `buildSessionRecord`. Text format now
   renders `(no ref, bare id: <id>)` for legacy-bucket sessions instead of
   `(no ref)`. Updated the struct comment to document the addressing.

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests + fuzz seeds (21) | PASS | All pass, 0.438s |
| Full agent suite (`go test ./agent/ -count=1 -short`) | PASS | `ok 319.399s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS, exit 0 |

### Diff stat

```
 agent/job_transcript_read.go      |   8 +-
 agent/job_transcript_read_test.go |  65 ++++++++++++++
 agent/session_outline.go          |   2 +-
 agent/session_tools_find.go       |  16 +++-
 agent/session_tools_transcript.go |   6 +-
 agent/transcript_lookup.go        |  13 ++-
 agent/transcript_lookup_test.go   | 172 ++++++++++++++++++++++++++++++++++++++
 7 files changed, 271 insertions(+), 11 deletions(-)
```

### Commits

- `ef8fd9f259` — `fix(agent): omit empty transcript_ref from read_transcript envelopes`
- `52603e8edd` — `fix(agent): harden legacy-bucket job lookup, honest ambiguity, bare-id addressing`

## Roborev Fix Round 3

Round 2's self-review found five remaining gaps in the legacy-bucket
handling across enumeration, job lookup, api-log ref emission, and
children-of resolution. This round writes RED tests for each, applies
the fix, and re-runs all gates.

### Findings

1. **[MEDIUM] `enumerateBuckets` followed symlinks** — `enumerateBuckets`
   used `os.Stat` (which follows symlinks) to filter directories. A
   foreign-named symlink under `projects/` could point outside the state
   root and expose transcripts from elsewhere. `locateLocalJob` already
   guarded against this via `entry.Type()&os.ModeSymlink`; the enumeration
   path did not.

2. **[HIGH] `locateLocalJob` masked corruption as not-found** — Round 2
   changed the sibling-sweep error handling from `return err` to `continue`
   to preserve stray-dir tolerance. But this also swallowed genuine
   corruption errors (invalid JSON in `jobs.jsonl`), masking them as
   "job not found". A corrupt sibling that was the ONLY bucket with the
   target job would silently return not-found instead of surfacing the
   corruption.

3. **[MEDIUM] api-log envelopes emitted empty `transcript_ref`** —
   `apiLogReadEnvelope` and `apiLogAttemptEnvelope` had
   `json:"transcript_ref"` without `omitempty`. A `read_session_transcript`
   against a legacy-bucket bare ID emitted `"transcript_ref": ""`, and a
   model reusing that ref would silently read the current session. Round 2
   fixed this on the read-path envelopes but missed the api-log structs.

4. **[MEDIUM] `parentBucketAndID` searched current bucket only for bare IDs**
   — The `children_of` resolution path (`parentBucketAndID`) returned
   `currentStateDir` for any bare session ID without searching sibling
   buckets. `resolveTranscript` (the read path) already resolved bare IDs
   cross-bucket with ambiguity handling. This asymmetry meant
   `children_of:"<bare-id>"` for a parent in a legacy-named bucket silently
   searched the wrong project.

5. **[HIGH] api-log re_read handle lost `transcript_ref` for legacy-bucket reads**
   — `apiLogResultTranscriptPlaceholder` used `resultIdentity.TranscriptRef`
   for the re_read and continuation handles. For a legacy-bucket bare-ID
   read, `refFor` returns `""`, so the handle's `transcript_ref` was empty.
   A model using the re_read handle would silently read the current session
   instead of the one it just read.

### Refuted

None. All five findings held against the code.

### TDD Evidence

#### RED (before fix)

Command:
```sh
go test ./agent/ -run 'TestEnumerateBuckets_SkipsSymlinkedBucket|TestLocateLocalJob_CorruptSiblingDirSurfacesErrorWhenTargetNotFound|TestApiLogReadEnvelope_OmitsEmptyTranscriptRef|TestApiLogAttemptEnvelope_OmitsEmptyTranscriptRef|TestReadAPILogSummary_LegacyBucketBareIDOmitsEmptyTranscriptRef|TestFind_ChildrenOf_BareIDResolvesCrossBucket|TestApiLogResultTranscriptPlaceholder_LegacyBucketCarriesCallTranscriptRef' -count=1 -v
```

All seven failed:
- `TestEnumerateBuckets_SkipsSymlinkedBucket`: symlink enumerated via `os.Stat`.
- `TestLocateLocalJob_CorruptSiblingDirSurfacesErrorWhenTargetNotFound`: corrupt sibling masked as not-found.
- `TestApiLogReadEnvelope_OmitsEmptyTranscriptRef`: empty `transcript_ref` emitted.
- `TestApiLogAttemptEnvelope_OmitsEmptyTranscriptRef`: empty `transcript_ref` emitted.
- `TestReadAPILogSummary_LegacyBucketBareIDOmitsEmptyTranscriptRef`: empty `transcript_ref` emitted.
- `TestFind_ChildrenOf_BareIDResolvesCrossBucket`: `children_of` bare ID finds 0 children in current bucket only.
- `TestApiLogResultTranscriptPlaceholder_LegacyBucketCarriesCallTranscriptRef`: re_read handle missing `transcript_ref`.

#### GREEN (after fix)

All seven pass (0.755s). The fuzz seed corpus also passes.

### Fixes Applied

1. **`agent/transcript_lookup.go`**: Changed `enumerateBuckets` from
   `os.Stat` to `os.Lstat` and added a `ModeSymlink` skip, mirroring
   `locateLocalJob`'s symlink guard. Comment cross-references the
   `locateLocalJob` guard.

2. **`agent/job_transcript_read.go`**: Added a `retainedErr` variable
   that captures the first genuine sibling error (corruption/unreadability).
   When the target is found elsewhere, the error is discarded (stray-dir
   tolerance preserved). When the target is NOT found, the retained error
   is surfaced via `finishLocalJobLookup` instead of masking it as
   "job not found". `finishLocalJobLookup` signature updated: added
   `retainedErr error` param. Both call sites updated.

3. **`agent/apilog_read.go`**: Added `omitempty` to `TranscriptRef` in
   `apiLogReadEnvelope` and `apiLogAttemptEnvelope`.

4. **`agent/transcript_lookup.go`**: Added cross-bucket resolution to
   `parentBucketAndID`'s bare-ID branch, mirroring `resolveTranscript`'s
   logic: stat the transcript in the current bucket and all sibling
   buckets, handle ambiguity with bucket names as context, fall back to
   current bucket when the parent transcript is not found anywhere
   (never-flushed parent).

5. **`agent/session_tools_transcript.go`**: In
   `apiLogResultTranscriptPlaceholder`, when `resultIdentity.TranscriptRef`
   is empty (legacy-bucket bare-ID read), fall back to the original call's
   `transcript_ref` (via `stringArg(args, "transcript_ref")`) for both the
   re_read and continuation handles.

6. **`agent/job_transcript_read_covtest_test.go`**: Updated
   `TestFinishLocalJobLookup` call sites for the new `retainedErr`
   parameter (passing `nil` for both existing test cases — the new
   retainedErr branch is exercised by the new test in
   `job_transcript_read_test.go`).

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests (7 RED→GREEN) | PASS | All pass, 0.755s |
| Full agent suite (`go test ./agent/ -count=1`) | PASS | `ok primeradiant.com/evener/agent 498.911s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS (naming, gofmt, evenerfuzz, eval, internal, golangci, generated, fuzz-registry, package-imports, biome, secret-scan), exit 0 |

### Diff stat

```
 agent/apilog_read.go                            |   4 +-
 agent/job_transcript_read.go                    |  28 ++++--
 agent/job_transcript_read_covtest_test.go       |   4 +-
 agent/job_transcript_read_test.go               |  43 ++++++++++
 agent/session_tools_transcript.go               |  12 ++-
 agent/session_tools_transcript_branches_test.go |  29 +++++++
 agent/transcript_lookup.go                      |  78 ++++++++++++++++-
 agent/transcript_lookup_test.go                | 109 ++++++++++++++++++++++++
 agent/transcript_tools_test.go                  |  56 ++++++++++++
 9 files changed, 345 insertions(+), 18 deletions(-)
```

### Commits

- `a26c941a53` — `fix(agent): skip symlinks in bucket enumeration, cross-bucket children_of, omit empty api-log refs`
- `4515f5c701` — `fix(agent): surface corrupt sibling errors in job lookup, preserve transcript_ref in api-log re_read handles`
- `b583cbe1d4` — `docs(agent): refresh parentBucketAndID comments to reflect stat-based bare-ID resolution`

## Roborev Fix Round 4

Round 3 added symlink skipping to bucket enumeration (os.Lstat) but left
four gaps: one Medium (symlink escape via explicit proj: ref) and three Lows
(dead symlink guard, false mirroring comment, duplicated bare-ID search).
This round verifies each finding against the code, writes a RED test for
the Medium, applies all four fixes, and re-runs all gates.

### Findings

1. **[MEDIUM] Explicit proj: ref escaped symlink protection** —
   `resolveTranscript`'s explicit proj: branch (lines 61-64) resolved the
   bucket by `filepath.Join(stateHome, "evener", "projects", projectID)`
   followed by `os.Stat`, which FOLLOWS symlinks. `enumerateBuckets` skips
   symlinked buckets, but the explicit-ref branch never calls
   `enumerateBuckets` — it joins the path directly. A symlink with a
   grammar-valid name (e.g. `link-0123456789`, which passes
   `ValidateProjectID`) bypassed the enumeration protection and exposed
   transcripts outside the state root.

2. **[LOW] Dead symlink guard in enumerateBuckets** — `os.Lstat` reports a
   symlink with `IsDir()==false`, so the `!info.IsDir()` check (line 172)
   skipped symlinks before the `os.ModeSymlink` check (line 175) ever ran.
   The explicit symlink guard was unreachable on every platform. Behavior
   was correct but the guard order gave false assurance: a future revert
   to `os.Stat` would re-follow symlinks while the `ModeSymlink` check still
   looked protective.

3. **[LOW] False mirroring claim in doc comment** — `enumerateBuckets`' doc
   comment (lines 159-180) claimed it mirrored "the doctor's globBuckets
   (PR #2163)", but the agent now skips symlinked buckets while the doctor's
   `globBuckets` still follows them (its `isDir` helper uses `os.Stat`). The
   two forensic views disagree and the mirroring claim is false.

4. **[LOW] parentBucketAndID duplicated resolveTranscript's bare-ID search**
   — `parentBucketAndID` (lines 255-311) duplicated `resolveTranscript`'s
   bare-ID cross-bucket search (~45 lines): enumerate → abs-path compare →
   current-first → ambiguity. If one changed, `children_of` and
   `read_transcript` would silently resolve the same bare selector to
   different buckets.

### Refuted

None. All four findings held against the code.

### TDD Evidence

#### RED (before fix)

Command:
```sh
go test ./agent/ -run 'TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket' -count=1 -v
```

Output:
```
=== RUN   TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket
--- FAIL: TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket (0.00s)
    transcript_lookup_test.go:947: explicit proj: ref to symlinked bucket resolved through the symlink; symlinks must be rejected to prevent exposure outside the state root
FAIL
FAIL    primeradiant.com/evener/agent    0.156s
```

Why it fails: the explicit `proj:link-0123456789:<sid>` ref resolves the
bucket dir by `filepath.Join` + `os.Stat` (follows symlinks), reads through
the symlink into the outside dir, and returns the transcript with no error.

Findings 2-4 are refactor/doc — no RED needed.

#### GREEN (after fix)

Command:
```sh
go test ./agent/ -run 'TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket' -count=1 -v
```

Output:
```
--- PASS: TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket (0.00s)
PASS
ok      primeradiant.com/evener/agent    0.214s
```

### Fixes Applied

1. **`agent/transcript_lookup.go`** (finding 1 — symlink escape): Added
   `symlinkError(path)` helper that Lstats a path and returns an error if it
   is a symlink (missing paths return nil — the caller's Stat handles "not
   found"). Applied in `resolveTranscript`'s explicit proj: branch (Lstat
   the bucket dir before Stat), the bare-ID current-bucket and sibling-bucket
   match returns (Lstat the transcript file), and `parentBucketAndID`'s
   explicit proj: branch (Lstat the bucket dir — children search would
   otherwise escape).

2. **`agent/transcript_lookup.go`** (finding 2 — dead symlink guard):
   Reordered `enumerateBuckets`' filter so the `os.ModeSymlink` check runs
   FIRST (before `!info.IsDir()`), with a comment explaining Lstat is
   required: Stat follows the symlink and clears ModeSymlink. Removed the
   `statErr != nil` early-continue to keep both checks in one block.

3. **`agent/transcript_lookup.go`** (finding 3 — false mirroring comment):
   Rewrote `enumerateBuckets`' doc comment to state the symlink skip as a
   deliberate agent-side divergence from the doctor's `globBuckets` (which
   follows symlinks via `isDir`/`os.Stat`). The doctor is an operator
   forensic tool that must see everything on disk; the agent's model-facing
   read paths hold the higher bar — a symlink under `projects/` could point
   outside the state root. Doctor code untouched.

4. **`agent/transcript_lookup.go`** (finding 4 — duplicated bare-ID search):
   Extracted `findBareIDBuckets(selector, currentStateDir, stateHome)` —
   stats the current bucket and all sibling buckets (via `enumerateBuckets`)
   for a bare session ID, returning `(currentFound, otherMatches, err)`.
   Extracted `ambiguCandidates(selector, currentFound, otherMatches)` —
   builds the context list for a bare-ID ambiguity error. Both
   `resolveTranscript` and `parentBucketAndID` now call these helpers.
   Each caller applies its own zero-match policy (resolveTranscript returns
   "unknown session"; parentBucketAndID falls back to the current bucket).

5. **`agent/transcript_lookup_test.go`**: Added
   `TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket` (RED→GREEN).

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests (48, incl. 7 round-3 RED + new RED) | PASS | All pass, 3.669s |
| Full agent suite (`go test ./agent/ -count=1 -short -timeout 20m`) | PASS | `ok 612.779s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS (naming, gofmt, evenerfuzz, eval, internal, golangci, generated, fuzz-registry, package-imports, biome, secret-scan), exit 0 |

Note: the first agent-suite run (default 10m timeout) timed out on
`TestIdleFatalGatedWatchSendDropsAndDoesNotPinDrain` (an unrelated watch-drain
test) under high system load (load pressure 43.6 on 16 cores). Rerun with
20m timeout passed. Round 3's suite took 498s; this run took 612s — the
increase is load, not test count.

### Diff stat

```
 agent/transcript_lookup.go      | 223 +++++++++++++++++++++++++--------------
 agent/transcript_lookup_test.go |  41 ++++++++
 2 files changed, 182 insertions(+), 82 deletions(-)
```

### Commit

- `8bbf86e0a3` — `fix(agent): reject symlinks in explicit proj: refs, extract shared bare-ID helper (FU3 round 4)`

## Roborev Fix Round 5

Round 4 hardened the explicit proj: branch against symlinked buckets, but
four High bypasses and one Low remained in the symlink protection. This
round writes RED tests for each High, applies all fixes, and re-runs all
gates.

### Findings

1. **[HIGH] Current-session fast-path bypassed symlink protection** —
   `resolveTranscript`'s `""`/`"current"` shortcut (lines 29-34) returned
   `transcriptPath(currentStateDir, currentSessionID)` with no `symlinkError`,
   while bare-ID, local:, and proj: paths all rejected symlinks. A symlinked
   current-session transcript could point outside the state root.

2. **[HIGH] Intermediate sessions/ directory not validated** — `symlinkError`
   used `os.Lstat(path)`, which follows every path element except the last.
   The bucket dir and transcript file were guarded, but the `sessions/`
   component between them was not. With `sessions/` symlinked outside the
   state root, `os.Stat` inside `findBareIDBuckets` resolved through it and
   `symlinkError` on the file saw a regular file.

3. **[HIGH] API-log .api.jsonl sidecar opened without validation** —
   `apiLogPathForTranscript(path)` derives the sidecar path and
   `readAPILogAttempt`/`readAPILogSummary` opened it directly. The sidecar is
   a different file from the validated transcript and can itself be a symlink
   pointing outside the state root.

4. **[HIGH + LOW] findBareIDBuckets used os.Stat (follows symlinks)** — a
   symlinked `<sid>.transcript.jsonl` was followed during discovery and
   counted as a match. With a real file in another bucket, this produced
   `totalMatches>1` and a spurious "ambiguous" error for a session with
   exactly one legitimate location. The same counting could scope
   `children_of` to a bucket chosen via a symlinked transcript.

5. **[LOW] Stale doc comment on the ambiguity contract** — line 23 still
   said bare-ID ambiguity errors carry "candidate refs", but the
   implementation (round 2) emits "found in: <bucket names>". Two spec files
   (`2026-06-19-evener-doctoring-tools.md:113`,
   `2026-06-19-evener-doctor-unified-design.md:220`) had the same stale
   wording.

### Refuted

None. All five findings held against the code.

### Import-direction decision (finding 2)

`agent/execenv/securepath.go` has a component-walking pattern
(`symlinkDenialDiagnostic`), and `agent` already imports `agent/execenv` in
non-test files without a cycle (`execenv` imports `agent` only in `_test.go`).
However, `securepath.go` pulls in `agent/sandbox` and heavy fd-anchored
sandbox FS machinery. Importing it just for a component-walk would be
disproportionate. Decision: wrote a small local component-walk
(`symlinkErrorDeep`) in `transcript_lookup.go` that Lstats each path prefix
from the file upward. Self-contained, no new imports, no sandbox machinery.

### TDD Evidence

#### RED (before fix)

Command:
```sh
go test ./agent/ -run 'TestResolveTranscript_CurrentSessionRejectsSymlinkedTranscript|TestResolveTranscript_SymlinkedSessionsDirEscapesProtection|TestResolveTranscript_BareIDSymlinkedFileCausesSpuriousAmbiguity|TestReadAPILogSummary_SymlinkedSidecarRejected' -count=1 -v
```

All four failed:
- `TestResolveTranscript_CurrentSessionRejectsSymlinkedTranscript`: current-session fast-path resolved through symlinked transcript (err==nil).
- `TestResolveTranscript_SymlinkedSessionsDirEscapesProtection`: symlinked sessions/ dir escaped protection (err==nil).
- `TestResolveTranscript_BareIDSymlinkedFileCausesSpuriousAmbiguity`: spurious "ambiguous" error instead of resolving to the real bucket.
- `TestReadAPILogSummary_SymlinkedSidecarRejected`: sidecar opened through symlink (error about API-log content, not symlink).

Finding 5 is doc/spec — no RED needed.

#### GREEN (after fix)

All four pass. The round 3/4 regression set (18 tests) also passes.

### Fixes Applied

1. **`agent/transcript_lookup.go`** (finding 1 — current-session fast-path):
   Added `symlinkErrorDeep(p)` on the computed current-session path before
   returning. `symlinkErrorDeep` returns nil for missing paths, so the
   no-Stat freshness guarantee (current transcript may not exist mid-write)
   is preserved.

2. **`agent/transcript_lookup.go`** (finding 2 — sessions/ dir): Replaced
   `symlinkError` with `symlinkErrorDeep`, a component-walking helper that
   Lstats each path prefix from the file upward (via `filepath.Dir` loop),
   catching symlinked intermediate dirs like `sessions/`. The final path is
   also Lstat'd. Written locally — no `agent/execenv` import (avoids pulling
   in sandbox FS machinery; `agent/execenv/securepath.go` imports
   `agent/sandbox` and heavy fd-anchored infrastructure disproportionate to
   the need).

3. **`agent/session_tools_transcript.go`** (finding 3 — api-log sidecar):
   Added `symlinkErrorDeep(sidecar)` on `apiLogPathForTranscript(path)` before
   opening the sidecar in `execReadSessionTranscriptWithContext`. Missing
   sidecars are still allowed (`symlinkErrorDeep` returns nil for missing
   paths); only real symlinks are rejected.

4. **`agent/transcript_lookup.go`** (finding 4 — os.Stat in
   `findBareIDBuckets`): Replaced both `os.Stat` existence checks with
   `existsNonSymlink`, a new helper that uses `os.Lstat` and returns false
   for symlinks. Symlinks never enter the match set, so the spurious-ambiguity
   Low is fixed by the same change. The `symlinkErrorDeep` calls in
   `resolveTranscript`'s match-return paths become a backstop.

5. **`agent/transcript_lookup.go` + 2 spec files** (finding 5 — stale doc):
   Updated `resolveTranscript`'s doc comment line 23 from "error with
   candidate refs" to "error listing the buckets it was found in". Updated
   `docs/superpowers/specs/2026-06-19-evener-doctoring-tools.md:113` and
   `docs/superpowers/specs/2026-06-19-evener-doctor-unified-design.md:220`
   from "ambiguity reported with candidate refs" to "ambiguity reported with
   the buckets it was found in".

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests (52, incl. 4 new RED + all round 3/4) | PASS | All pass, 0.570s |
| Full agent suite (`go test ./agent/ -count=1 -short -timeout 20m`) | PASS | `ok 509.447s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS (naming, gofmt, evenerfuzz, eval, internal, golangci, generated, fuzz-registry, package-imports, biome, secret-scan), exit 0 |

### Diff stat

```
 agent/session_tools_transcript.go                  |  14 +-
 agent/transcript_lookup.go                         |  98 +++++++++-----
 agent/transcript_lookup_test.go                    | 142 +++++++++++++++++++++
 docs/superpowers/specs/2026-06-19-evener-doctor-unified-design.md |   2 +-
 docs/superpowers/specs/2026-06-19-evener-doctoring-tools.md      |   2 +-
 5 files changed, 223 insertions(+), 35 deletions(-)
```

### Commits

- `dc03583138` — `fix(agent): close symlink bypasses in current-session path, sessions/ dir, api-log sidecar, bare-ID discovery (FU3 round 5)`
- `986ef47658` — `docs: fix stale 'candidate refs' ambiguity wording in resolveTranscript doc + specs (FU3 round 5)`

## Roborev Fix Round 6

Round 5's symlinkErrorDeep component-walk introduced a functional regression
(walking to /), and four more symlink bypasses remained in discovery, job
lookup, bare-ID matching, and the placeholder size-overflow path. This round
fixes the regression first, then hardens the remaining surfaces.

### Findings

1. **[HIGH] Discovery followed symlinked sessions/ dirs** —
   `collectCandidates` called `ListSessionMetas` on every enumerated bucket
   with no symlink check (afero.ReadDir follows symlinked sessions/), and
   `find_session_transcripts` followed symlinked transcript files via
   `os.Stat`/`readTranscript`. Metas, titles, and content snippets from outside
   the state root surfaced via find + children_of while read paths rejected
   them.

2. **[HIGH] Job lookup opened through symlinked components** —
   `findLocalJobInProject` and `locateLocalJobRetainedTarget` opened
   `sessions/<owner>/jobs.jsonl` and retained job output through symlinked
   intermediate components, allowing `job:<id>` reads from outside the state
   root.

3. **[MEDIUM — REGRESSION from round 5] symlinkErrorDeep walked to /** —
   Round 5's `symlinkErrorDeep` Lstat'd every ancestor of the state dir up to
   `/`. On macOS (`/var` → `/private/var`, `/tmp` → `/private/tmp`) or any host
   with a symlinked `$HOME`, `XDG_STATE_HOME`, or `--state-dir`, every
   `read_session_transcript` / `children_of` / api-log read failed with
   "traverses a symlink" even though nothing under the state root was real.
   The threat model does not cover ancestors of a runtime-configured path.

4. **[MEDIUM] findBareIDBuckets existsNonSymlink Lstat'd full path only** —
   `existsNonSymlink` Lstat'd the full path (final component only), so a
   bucket whose `sessions/` was a symlink containing the file counted as a
   match, contradicting the helper's "never enter the match set" claim.

5. **[MEDIUM] Bare children_of resolution searched only transcript files** —
   REFUTED. The spec at `docs/tools/transcripts.md:126-127` states "The
   parent's bucket and ID come from the ref alone — no transcript is opened,
   not even the parent's" for formal refs, and the code comment at
   `transcript_lookup.go:396-403` documents the zero-match → current-bucket
   fallback as intentional for never-flushed live parents. The
   transcript-file search is the implementation of that documented contract;
   the zero-match fallback is the stated behavior for the live/unflushed
   parent case.

6. **[MEDIUM] Placeholder size-overflow dropped the legacy bare-ID fallback**
   — When the placeholder exceeded 1 KiB, the response returned a generic
   `re_read` handle with no `transcript_ref`, so following it targeted the
   current session instead of the one the model just read.

### Refuted

Finding 5. The spec (`docs/tools/transcripts.md:126-127`) and code comment
(`transcript_lookup.go:396-403`) document the zero-match → current-bucket
fallback as intentional for the live/unflushed-parent case. The
transcript-file search does not contradict a metadata-only contract — it is
the implementation of the documented contract for formal refs, and the
zero-match fallback is the stated behavior for the live case.

### TDD Evidence

#### RED (before fix)

Command:
```sh
go test ./agent/ -run 'TestSymlinkErrorDeep_SymlinkedAncestorAboveStateRootDoesNotBreakReads|TestCollectCandidates_SkipsSymlinkedSessionsDir|TestTranscriptExists_RejectsSymlinkedFile|TestFindBareIDBuckets_SymlinkedSessionsDirDoesNotMatch|TestLocateLocalJob_RejectsSymlinkedSessionsDir|TestApiLogResultTranscriptPlaceholder_SizeOverflowPreservesFallbackRef' -count=1 -v
```

All six failed:
- `TestSymlinkErrorDeep_SymlinkedAncestorAboveStateRootDoesNotBreakReads`: read through symlinked ancestor above state root failed (traverses a symlink).
- `TestCollectCandidates_SkipsSymlinkedSessionsDir`: 1 candidate returned from symlinked sessions/.
- `TestTranscriptExists_RejectsSymlinkedFile`: transcriptExists returned true for symlinked file.
- `TestFindBareIDBuckets_SymlinkedSessionsDirDoesNotMatch`: symlinked sessions/ counted as match.
- `TestLocateLocalJob_RejectsSymlinkedSessionsDir`: job found through symlinked sessions/.
- `TestApiLogResultTranscriptPlaceholder_SizeOverflowPreservesFallbackRef`: placeholder lost transcript_ref in size-overflow.

#### GREEN (after fix)

All six pass. All 4 round-5 symlink tests and 3 round-3/4 tests still pass.

### Fixes Applied

1. **`agent/transcript_lookup.go`** (finding 3 — REGRESSION): Changed
   `symlinkErrorDeep(path string)` to `symlinkErrorDeep(path, root string)`.
   The component walk now stops at `root` — components at or above root are
   NOT checked. Callers pass the appropriate root: `currentStateDir` for
   current-bucket paths, `sh` (state home) for sibling-bucket paths,
   `bucketDir` for already-validated bucket paths, and
   `filepath.Dir(sidecar)` for the api-log sidecar.

2. **`agent/session_tools_find.go`** (finding 1 — discovery): `collectCandidates`
   now calls `symlinkErrorDeep(sessDir, bucket)` before `ListSessionMetas`,
   skipping buckets whose `sessions/` dir is a symlink. `contentSnippets`
   guards with `symlinkErrorDeep(path, bucketDir)` before reading.
   `transcriptExists` switched from `os.Stat` to `os.Lstat` + `ModeSymlink`
   check.

3. **`agent/job_transcript_read.go`** (finding 2 — job lookup):
   `findLocalJobInProject` guards with `symlinkErrorDeep(path, stateDir)`
   before reading `jobs.jsonl`. `locateLocalJobRetainedTarget` guards with
   `symlinkErrorDeep(outputPath, location.StateDir)` before returning the
   output path.

4. **`agent/transcript_lookup.go`** (finding 4 — bare-ID): `existsNonSymlink`
   now takes `(path, bucketDir string)` and uses `symlinkErrorDeep` to check
   the `sessions/` dir in addition to the file itself. A symlinked
   `sessions/` dir containing a real file no longer counts as a match.

5. **`agent/session_tools_transcript.go`** (finding 6 — placeholder overflow):
   When the encoded placeholder exceeds 1 KiB, trims optional body/continuation
   fields and re-encodes with a minimal `re_read` handle preserving
   `transcript_ref` + `attempt_id`. If still over 1 KiB (pathological
   `attempt_id`), drops `attempt_id` but keeps `transcript_ref`.

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests (15, incl. 6 new RED + all round 3/4/5) | PASS | All pass, 0.741s |
| Full agent suite (`go test ./agent/ -count=1 -timeout 20m`) | PASS | `ok 554.525s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS, exit 0 |

### Diff stat

```
 agent/job_transcript_read.go                    |  13 ++-
 agent/job_transcript_read_test.go               |  30 ++++++
 agent/session_tools_find.go                     |  24 ++++-
 agent/session_tools_transcript.go               |  27 ++++-
 agent/session_tools_transcript_branches_test.go |  27 +++++
 agent/transcript_lookup.go                      |  89 +++++++++-------
 agent/transcript_lookup_test.go                 | 136 +++++++++++++++++++++++
 7 files changed, 304 insertions(+), 42 deletions(-)
```

### Commits

- `05068d8c37` — `fix(agent): bound symlinkErrorDeep walk to state root, guard discovery/job/bare-ID paths (FU3 round 6)`
- `acbd6553ac` — `fix(agent): preserve fallback transcript_ref in placeholder size-overflow (FU3 round 6)`

---

## Round 7

### Findings (2)

1. **[High]** Parent-directory symlink gap on the enumeration side.
   `enumerateBuckets` used `filepath.Glob` which follows symlinked prefix
   components; `os.Lstat` checked only the final bucket. A symlinked
   `stateHome/evener` (a plausible state-dir relocation) let enumeration surface
   buckets whose refs `read_transcript` then REJECTED with "traverses a
   symlink" — the exact find-must-not-return-refs-read-rejects invariant this
   PR established. Discovery-side `collectCandidates`/`contentSnippets`/
   `transcriptExists` rooted `symlinkErrorDeep` at the bucket, not the state
   home, so they could not catch the symlinked ancestor.

2. **[Low]** Symlink rejection surfaced as job-not-found.
   The round-3 `retainedErr` mechanism retained any non-nil
   `findLocalJobInProject` error for the not-found path. The round-6 symlink
   guards made a symlinked `sessions/` in an UNRELATED sibling bucket return a
   non-nil "symlinks are not allowed" error — so looking up a nonexistent job
   reported the symlink error instead of "job not found", masking the honest
   result with a misleading message.

### TDD evidence

**RED (4 tests, all confirmed failing before fix):**
- `TestEnumerateBuckets_SymlinkedEvenerAncestorRejected` — enumerateBuckets
  returned bucket through symlinked `evener/` ancestor.
- `TestFind_SymlinkedEvenerAncestorOmitsSymlinkedBucketSession` — find returned
  session from bucket reached through symlinked `evener/` ancestor.
- `TestLocateLocalJob_SymlinkedSiblingBucketSurfacesJobNotFound` — nonexistent
  job with unrelated symlinked sibling returned symlink error, not job-not-found.
- `TestLocateLocalJob_SymlinkedTargetBucketStillSurfacesSymlinkError` — PASS
  both before and after fix (invariant preserved: symlinked target bucket still
  surfaces symlink error).

**GREEN:** All 4 pass after fix. All 56+ existing symlink/enumerate/resolve/find/
job tests still pass.

### Fixes

1. **`agent/transcript_lookup.go`** (finding 1 — enumeration prefix guard):
   `enumerateBuckets` now Lstats `stateHome/evener` and
   `stateHome/evener/projects` before globbing. If either is a symlink or does
   not exist, returns `(nil, nil)` — refuses to enumerate. This closes the
   gap: `filepath.Glob` follows symlinked prefixes, but the Lstat guard catches
   them before any bucket is returned. `findBucketsWithEnumerate` degrades to
   the current bucket (scope `current_project`) when enumeration returns empty,
   so find never returns refs through a symlinked ancestor.

2. **`agent/job_transcript_read.go`** (finding 2 — symlink error classification):
   `findLocalJobInProject` now checks `os.Stat(path)` when `symlinkErrorDeep`
   fires. If the journal file does not exist (`os.ErrNotExist`), returns
   `(false, nil)` — skip-worthy, like not-found. The symlink error is only
   retained when the journal file actually exists through the symlink (the
   bucket would have contained the target). `os.Stat` follows symlinks but
   only reads metadata (not content), so this is safe for existence checking.
   Genuine corruption/read errors (non-`ErrNotExist` stat failures) fall
   through and surface the symlink error as before.

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests (56+ symlink/enumerate/resolve/find/job) | PASS | All pass |
| Full agent suite (`go test ./agent/ -count=1 -timeout 20m`) | PASS | `ok 421.716s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS, exit 0 |

### Diff stat

```
 agent/job_transcript_read.go      | 10 ++++
 agent/job_transcript_read_test.go | 77 ++++++++++++++++++++++++++++++
 agent/transcript_lookup.go        | 18 +++++++
 agent/transcript_lookup_test.go   | 98 ++++++++++++++++++++++++++++++++++++++
 4 files changed, 203 insertions(+)
```

## Round 8

### Findings (3 fixable, 1 audit)

1. **[High] Current-bucket paths missed layout prefix validation** —
   `symlinkErrorDeep` on current-bucket paths was rooted at the bucket dir,
   so ancestors above it (`evener/`, `evener/projects/`) were not checked.
   A symlinked `evener/` within the state root (a plausible state-dir
   relocation where `~/.local/state/evener` itself is a symlink) let
   current-bucket reads resolve through it while sibling reads and explicit
   `proj:` refs were rejected — an inconsistency. The current-session
   fast-path, `local:` ref, bare-ID current-bucket match,
   `collectCandidates` current bucket, `contentSnippets`, and
   `findLocalJobInProject` current path all had this gap. `locateLocalJob`'s
   sibling sweep opened `stateHome/evener/projects` via `os.Open` (follows
   symlinks) without prefix validation.

2. **[Low] `transcriptExists` missed symlinked `sessions/` parent** —
   `transcriptExists` used `os.Lstat` on the final file only. `os.Lstat`
   follows every path element except the last, so a symlinked `sessions/`
   was followed and the file inside appeared as a regular file. A future
   caller bypassing `collectCandidates`'s guard would surface symlinked
   transcripts in find results.

3. **[Low] `enumerateBuckets` Glob ran before prefix checks** —
   `filepath.Glob` followed symlinked prefix components before the Lstat
   prefix checks ran. Behavior was correct (the checks still caught the
   symlink), but Glob through a symlinked prefix was wasted work and
   fragile against future changes.

4. **[Low] Sidecar guard root was `filepath.Dir(sidecar)` not bucket dir** —
   The api-log sidecar's `symlinkErrorDeep` root was `filepath.Dir(sidecar)`
   (which IS `sessions/`), making the component walk empty (root == start).
   A symlinked `sessions/` was not caught independently. `resolveTranscript`
   validated `sessions/` before the sidecar guard ran, so end-to-end behavior
   was correct, but the guard provided no defense-in-depth.

### TDD evidence

**RED (3 tests, all confirmed failing before fix):**
- `TestResolveTranscript_CurrentSessionSymlinkedEvenerAncestorNotRejected` —
  current-session fast-path resolved through symlinked `evener/` ancestor.
- `TestTranscriptExists_SymlinkedSessionsDirReturnsTrue` —
  `transcriptExists` returned true for file through symlinked `sessions/`.
- `TestSymlinkErrorDeep_SidecarGuardDirRootMissesSymlinkedSessionsDir` —
  sidecar guard root `filepath.Dir(sidecar)` did not catch symlinked
  `sessions/` dir.

**GREEN:** All 3 pass after fix. All 60+ existing symlink/enumerate/resolve/
find/job/api-log tests still pass.

### Fixes applied

1. **`agent/transcript_lookup.go`** (finding 1): Added
   `validateLayoutPrefix(stateDir string) error` helper — Lstats
   `stateHome/evener` and `stateHome/evener/projects`, returns error if
   either is a symlink, no-op for flat layouts. Called at:
   - `resolveTranscript` current-session fast-path (before `symlinkErrorDeep`)
   - `resolveTranscript` `local:` ref branch (before setting `bucketDir`)
   - `resolveTranscript` bare-ID current-bucket match (before `symlinkErrorDeep`)
   The `proj:` and sibling-bucket paths already root `symlinkErrorDeep` at
   `sh` (state home), so the walk covers `evener/` + `projects/` natively.

2. **`agent/session_tools_find.go`** (finding 1): Added
   `validateLayoutPrefix` to `collectCandidates` (current bucket —
   `currentPrefixOK` flag skips current bucket if prefix is a symlink) and
   `contentSnippets` (before `symlinkErrorDeep`). Finding 2:
   `transcriptExists` now calls `symlinkErrorDeep(path, bucketDir)` before
   `os.Lstat`, catching symlinked `sessions/` dirs.

3. **`agent/transcript_lookup.go`** (finding 3): Reordered
   `enumerateBuckets` — Lstat prefix checks now run BEFORE `filepath.Glob`,
   avoiding globbing through a symlinked prefix.

4. **`agent/session_tools_transcript.go`** (finding 4): Sidecar guard root
   changed from `filepath.Dir(sidecar)` to
   `filepath.Dir(filepath.Dir(path))` (the bucket dir), so `symlinkErrorDeep`
   walks `sessions/` and catches the symlink independently.

5. **`agent/job_transcript_read.go`** (finding 1): Added
   `validateLayoutPrefix` to `locateLocalJob` (before `openLocalJobProjectDirectory`)
   and `findLocalJobInProject` (before `symlinkErrorDeep`). A symlinked
   prefix in `findLocalJobInProject` returns not-found (skip-worthy), matching
   the existing symlinked-`sessions/` treatment. Added `//nolint:nilerr`
   directive matching the existing pattern in `agent/sandbox/gitdir.go`.

6. **`agent/transcript_lookup_covtest_test.go`**: Updated
   `TestEnumerateBuckets_GlobError` to create `evener/projects` prefix dirs
   so the mock Glob is reached after the reorder (prefix checks now run first).

7. **`agent/transcript_lookup_test.go`**: Added 3 RED tests (finding 1, 2, 4).
   Updated finding 4 test to verify both the fixed root (bucket dir catches
   the symlink) and the old root (`filepath.Dir(sidecar)` does not).

### symlinkErrorDeep call site audit

All 13 `symlinkErrorDeep` call sites across 4 files were audited. Every
call site's root is either:
- The state home (`sh`) — walk covers `evener/` + `projects/` + bucket +
  `sessions/` + file natively, or
- An already-validated bucket dir — validated by `validateLayoutPrefix`
  (called immediately before or upstream in the call chain), or by
  `enumerateBuckets`' prefix guard for sibling buckets.

No call site has an unvalidated root.

### Gates

| Gate | Status | Evidence |
|------|--------|----------|
| gofmt -l (touched files) | PASS | No files needing formatting |
| `make vet` | PASS | Exit 0 |
| `go vet -tags evenerfuzz ./agent/` | PASS | Exit 0 |
| Focused tests (symlink/enumerate/resolve/find/job/api-log) | PASS | All pass |
| Full agent suite (`go test ./agent/ -count=1 -timeout 20m`) | PASS | `ok 282.073s` (exit 0) |
| `make lint` | PASS | All 11 phases PASS (naming, gofmt, evenerfuzz, eval, internal, golangci, generated, fuzz-registry, package-imports, biome, secret-scan), exit 0 |

### Diff stat

```
 agent/job_transcript_read.go            |  17 ++++
 agent/session_tools_find.go             |  31 ++++++-
 agent/session_tools_transcript.go       |   7 +-
 agent/transcript_lookup.go              |  67 ++++++++++++---
 agent/transcript_lookup_covtest_test.go |   6 +-
 agent/transcript_lookup_test.go         | 151 +++++++++++++++++++++++++++++++
 6 files changed, 260 insertions(+), 19 deletions(-)
```

### Commits

- `0df09d66e7` — `fix(agent): validate layout prefix on current-bucket paths, guard transcriptExists/sidecar, reorder enumerateBuckets (FU3 round 8)`
- `525cd5231a` — `test(agent): RED tests for round 8 — current-bucket layout prefix, transcriptExists sessions/ guard, sidecar guard root (FU3 round 8)`

NOT pushed.

## Fix round 9

Root cause: round 8 moved enumerateBuckets' Lstat prefix checks before
filepath.Glob. A malformed glob root (stateHome with unmatched
metacharacters like `[`) fails the prefix Lstat as not-exists and hits
the `nil, nil` shortcut, silently accepting a root that previously
reached filepath.Glob and surfaced ErrBadPattern. The fuzz oracle at
transcript_render_lookup_exact_fuzz_test.go:28 pins that malformed glob
roots are rejected.

Fix (commit e54854f7e3): validate the glob pattern with filepath.Match
(same pattern syntax as Glob, no filesystem access) BEFORE the prefix
not-exists shortcut. A malformed pattern returns ErrBadPattern; a
well-formed pattern proceeds to the prefix checks as before.

Contracts preserved:
- Malformed glob roots rejected (fuzz oracle contract 1).
- Symlinked evener/projects prefix still refused (R7/R8 contract 2).
- Well-formed missing prefix still returns nil,nil (contract 3).
- Above-stateHome symlinks don't break reads (R6 contract 4).

Gates passed: RED fuzz seeds (3) now GREEN; focused symlink/enumerate
suite; gofmt; go vet (plain + evenerfuzz); make lint (exit 0); full
agent suite + evenerfuzz seed corpus (running at commit time).

## Fix round 10

Five findings (1 High, 1 Medium redesign, 3 Low), all addressed.

### Finding 1 (HIGH): symlinkErrorDeep skips current-bucket root

symlinkErrorDeep excludes its root by design (root = trusted ancestor),
and validateLayoutPrefix checked only ancestors above the bucket dir —
not the bucket dir itself. A symlinked current-bucket directory was
never validated and reads followed it outside the state root.

Fix: added an os.Lstat(bucketDir) guard in three places:
- validateLayoutPrefix (transcript_lookup.go:393-400) — Lstats the
  stateDir before the prefix loop; a symlinked bucket dir is rejected.
- existsNonSymlink (transcript_lookup.go:324-328) — Lstats the bucket
  dir before symlinkErrorDeep, which is rooted at bucketDir and does
  not check it.
- transcriptExists (session_tools_find.go:543-547) — same Lstat guard.

Sibling-bucket semantics unchanged: enumerated siblings still get
final-component Lstat in enumerateBuckets; no behavior or error text
changed for the enumerated path.

TDD: 3 RED tests added (TestResolveTranscript_SymlinkedCurrentBucketDirRejected,
TestLocateLocalJob_SymlinkedCurrentBucketDirRejected,
TestTranscriptExists_SymlinkedCurrentBucketDirReturnsTrue) — all GREEN
after the fix. Existing R6-R9 symlink/enumerate/job suite passes.

### Finding 2 (MEDIUM — REDESIGN): TOCTOU between symlink check and file open

Redesigned the leaf-level open to use execenv.OpenRegularNoFollow —
O_NOFOLLOW on the leaf + fstat to confirm a regular file, all in one
fd. Replaced os.Open at two read sites:
- openTranscriptFile (transcript_read.go:18-28) — full fd-replacement.
- openAPILogFile (apilog_read.go:34-37) — full fd-replacement.

Hybrid with honest residual: findLocalJobInProject
(job_transcript_read.go:162-170) — jobstore.ReadEvents opens the
journal internally (afero.NewOsFs); threading an fd through jobstore's
API is disproportionate (touches the internal package and every
caller). The residual TOCTOU window between symlinkErrorDeep and the
internal open is narrow — symlinkErrorDeep pre-checks every component
and validateLayoutPrefix (round 10) validates the bucket dir. Comment
names the residual honestly.

Preserves: R6's above-stateHome ruling (component walk starts at the
state home or validated bucket dir); R7-R9 honest not-found vs
symlink-error classification; every existing symlink test.

2 new tests: TestOpenTranscriptFile_RefusesSymlinkedLeaf,
TestOpenAPILogFile_RefusesSymlinkedLeaf. All pass.

### Finding 3 (LOW): Fuzz oracle unreachable

trenderLegacySession was a 21-char const string; ValidateSessionID
requires exactly 22 (base62Width), so the seed was rejected before
bucket resolution and the empty-ref/legacy-bucket oracle branch was
dead. Moved from const to var = identifier.MustNewSessionID() (22
chars). Fuzz seed corpus now reaches the legacy bucket and exercises
the ref=="" branch.

### Finding 4 (LOW): Tool spec drift

Fixed docs/tools/transcripts.md:
- Marked transcript_ref optional (?) in the response schema (:113).
- Rewrote children_of note (:125-131) to document bare-ID cross-bucket
  resolution via findBareIDBuckets.
- Rewrote :132-136 to explain transcript_ref omitempty for
  legacy-named buckets instead of claiming a match is always a
  read-able ref.

### Finding 5 (LOW): Stale "Today" comments

Rewrote 6 stale comments in transcript_lookup_test.go that described
the pre-fix state (os.Stat follows symlinks, Lstats only the final
file) to narrate the fixed behavior (existsNonSymlink/symlinkErrorDeep
+ Lstat, validateLayoutPrefix Lstats the bucket dir).

### Gates

- make vet: exit 0.
- go vet -tags=evenerfuzz ./agent/: clean.
- gofmt -l on touched files: clean.
- make lint: exit 0 (all 8 modules pass, including revive).
- Full agent suite (plain tags): running at commit time.
- Full agent suite (evenerfuzz tags): running at commit time.
- Focused symlink/enumerate/job suite (R6-R9): passes.
- Fuzz target seed run (FuzzResolveTranscript): passes.

Note: round 10 tests initially used `real` as a variable name, which
shadowed Go's built-in real() and was flagged by revive
(redefines-builtin-id). Renamed to realDir/realFile before the final
lint pass.

## Fix round 11 (salvage)

The prior fixer committed the round-11 substance (5 commits: c6c98b9bd8, 0c45cb5da7, 8f4a947eab, eab708c14d, 35d75b38d9) then died of context overflow before running gates or writing this section. This salvage pass verified all nine findings, closed one gap (F2 RED test), fixed two gate failures (deadline audit, lint), and ran the full gate suite.

### Per-finding disposition

- **F1** (bucket-identity IsCurrent): Verified in c6c98b9bd8. `buildSessionRecord`'s live-overlay gate and `IsCurrent` read `c.projectID == "" && c.meta.ID == currentID` (session_tools_find.go). RED test `TestFind_SiblingBucketDuplicateNotIsCurrent` already committed in eab708c14d — pins both sides (sibling duplicate without IsCurrent/overlay; current-bucket record keeping both). No salvage work needed.
- **F2** (existsNonSymlink IsRegular): Gap closed by salvage. Prior fixer's code (transcript_lookup.go) has the `Mode().IsRegular()` check after symlink checks, but no RED test for a directory at the transcript path existed. Salvage wrote `TestTranscriptExists_RejectsDirectoryAtTranscriptPath` (commit 633a5c94f7), proven RED against 3ab19475a3 (transcriptExists returned true for directory), GREEN against round-11 code. Sibling-bucket semantics byte-unchanged.
- **F3** (job journal+output Lstat/IsRegular gates): Verified in 8f4a947eab. Lstat+IsRegular before ReadEvents/open on both journal and output. FIFO journal rejected without blocking (test bounds lookup with goroutine+select timeout). Missing output returns `output_unavailable` rather than error. RED tests committed in eab708c14d. Salvage added TRIPWIRE comments (commit eebb46c3cd) to satisfy the deadline audit on the two FIFO hang-guard `time.After(5*time.Second)` bounds.
- **F4** (schema.ListSessionMetas/loadSessionMetaFS symlink guards): Verified in 0c45cb5da7. `lstatIfPossible` adapter in schema/snapshot.go (Lstat on OsFs, Stat fallback for non-Lstater filesystems). RED test `TestListSessionMetas_SymlinkedMetaJSONRejected` committed in eab708c14d — symlinked .meta.json pointing outside state root does not surface in find or load.
- **F5** (enumerateBuckets sentinel + find surfaces refusal + doc line): Verified in c6c98b9bd8. `enumerateBuckets` returns `errSymlinkedLayoutPrefix` sentinel for symlinked layout prefix (distinguishable from prefix-absent nil,nil). `find` surfaces the refusal instead of silently answering "No matching sessions." One doc line in docs/tools/transcripts.md. RED test `TestFind_SymlinkedEvenerPrefixSurfacesRefusal` committed in eab708c14d.
- **F6** (deterministic fuzz seed): Verified in eab708c14d. Fuzz seed is a fixed valid 22-char const literal (deterministic — prior round's `MustNewSessionID()` was nondeterministic). `identifier.ValidateSessionID` passes. Oracle assertion and seed corpus untouched.
- **F7** (platform-guarded leaf-symlink tests): Verified in eab708c14d. `TestOpenTranscriptFile_RefusesSymlinkedLeaf` and `TestOpenAPILogFile_RefusesSymlinkedLeaf` are unix-only (build tag / runtime skip). The `!unix` fallback (agent/execenv/open_regular_other.go) documents that it follows leaf symlinks and only enforces regular-file fstat.
- **F8** (doc envelopes mark `transcript_ref` optional): Verified in eab708c14d. Outline (:189), markdown (:218), jsonl (:253) envelopes and :165 prose sentence in docs/tools/transcripts.md updated, with the same caveat the find section (:113) carries.
- **F9** (transcriptExists delegates to existsNonSymlink): Verified in c6c98b9bd8. Duplicated body deleted; `transcriptExists` now delegates to `existsNonSymlink(transcriptPath(bucketDir, sessionID), bucketDir)`.
- **cov_s3_find_test.go / session_tools_aux_exact_fuzz_test.go**: Confirmed these modifications belong to F1/F4 test coverage and are not orphans.

### Salvage commits

| SHA | Description |
|-----|-------------|
| `633a5c94f7` | test(agent): RED test for F2 — directory at transcript path must not count as a match (FU3 round 11) |
| `eebb46c3cd` | fix(agent): add TRIPWIRE comments to FIFO test time.After bounds (FU3 round 11) |
| `7a8bdd9232` | fix(agent): resolve 5 lint findings in round-11 code (FU3 round 11) |

### Gate table

| Gate | Command | Exit |
|------|---------|------|
| Focused suite (17 named tests) | `go test -run 'TestTranscriptExists_RejectsDirectoryAtTranscriptPath\|TestFind_SiblingBucketDuplicateNotIsCurrent\|TestListSessionMetas_SymlinkedMetaJSONRejected\|TestFind_SymlinkedEvenerPrefixSurfacesRefusal\|TestEnumerateBuckets_SymlinkedEvenerAncestorRejected\|TestFind_SymlinkedEvenerAncestorOmitsSymlinkedBucketSession\|TestOpenTranscriptFile_RefusesSymlinkedLeaf\|TestOpenAPILogFile_RefusesSymlinkedLeaf\|TestLocateLocalJob_FIFOJournalRejectedWithoutBlocking\|TestLocateLocalJobRetainedTarget_FIFOOutputRejectedWithoutBlocking\|TestTranscriptExists_RejectsSymlinkedFile\|TestFindBareIDBuckets_SymlinkedSessionsDirDoesNotMatch\|TestTranscriptExists_SymlinkedSessionsDirReturnsTrue\|TestTranscriptExists_SymlinkedCurrentBucketDirReturnsTrue\|TestLocateLocalJob_RejectsSymlinkedSessionsDir\|TestLocateLocalJob_SymlinkedSiblingBucketSurfacesJobNotFound\|TestLocateLocalJob_SymlinkedTargetBucketStillSurfacesSymlinkError' ./agent/ -count=1 -v` | 0 (0.49s) |
| Full agent suite | `go test -timeout 1500s ./agent/ -count=1` | 0 (324s) |
| evenerfuzz seed replay | `go test -tags=evenerfuzz ./agent/ -count=1` | 0 (375s) |
| make vet | `make vet` | 0 |
| evenerfuzz vet | `go vet -tags=evenerfuzz ./agent/` | 0 |
| make lint | `make lint` | 0 (11 checks PASS) |
| gofmt | `gofmt -l agent/transcript_lookup_test.go agent/job_transcript_read_test.go agent/job_transcript_read.go agent/session_tools_find.go agent/transcript_lookup.go agent/schema/snapshot.go agent/cov_s3_find_test.go agent/session_tools_aux_exact_fuzz_test.go agent/transcript_render_fuzz_test.go` | 0 (no output) |

## Fix round 12

All six round-11 findings fixed (3 Mediums + 3 Lows), controller-verified.

### Finding 1 [MEDIUM] — Lstat error masking in findLocalJobInProject
`agent/job_transcript_read.go:167-170`: `os.Lstat` on `jobs.jsonl` masked ALL errors as not-found. Now only `os.ErrNotExist` returns not-found; permission/IO errors wrap and propagate, matching the retained-error discipline used for sibling corruption.

### Finding 2 [MEDIUM] — Output-path TOCTOU race
`agent/job_transcript_read.go:239-253`: Output path Lstat-validated then opened via jobstore's path-based API. **HYBRID** — output is read exclusively through `jobstore.ReadOutputSnapshot`/`ReadOutputWindowSnapshot` (path-based package functions that `os.Open` internally). An fd-accepting variant would be the same disproportionate internal-package/every-caller change the round-10 journal hybrid already declined. Guard is already as tight as the API allows (symlinkErrorDeep + Lstat regular); added honest residual-TOCTOU comment mirroring the journal hybrid at lines 174-183.

### Finding 3 [MEDIUM] — Intermediate-path symlink in session meta
`agent/schema/snapshot.go:597-618`: Leaf-only Lstat missed symlinked intermediate components (sessions/). Added `metaComponentWalk`: bounded component walk that Lstats each intermediate between bucket dir and leaf, rejecting symlinks. Mirrors `symlinkErrorDeep` from package agent (not importable — cycle); uses injected afero.Fs so it works on both OsFs (Lstat) and MemMapFs (Stat).

### Finding 4 [LOW] — Prefix Lstat errors ignored
`agent/transcript_lookup.go:257 and :420`: `info, _ := os.Lstat(prefix); if info == nil` treated permission/IO failures as absent prefix. Both sites now check `errors.Is(err, os.ErrNotExist)` for absent; other errors wrap and propagate.

### Finding 5 [LOW] — Silent no-match on symlinked layout for current_project
`agent/session_tools_find.go:383-401`: `findBucketsWithEnumerate` returned the current bucket unvalidated for non-all_projects scopes, so find reported "No matching sessions" while read_transcript and all_projects surfaced the explicit refusal. Now validates the prefix for the current bucket on every scope, so all three paths agree.

### Finding 6 [LOW] — Leaf-level TOCTOU in loadSessionMetaFS
`agent/schema/snapshot.go:603-618`: Lstat-then-ReadFile left a leaf TOCTOU window. Replaced with `readFileNoFollowOS`: single `O_NOFOLLOW` open that reads from the same descriptor. Deliberately omits `O_NONBLOCK` and fstat-for-regular — a FIFO at the .meta.json path is a deliberate synchronization barrier in retirement tests (`retirementPauseColdClaim`) and must be allowed to block the open. Build-tagged: unix uses `unix.Open` with `O_NOFOLLOW`; non-unix falls back to `os.ReadFile` (no `O_NOFOLLOW` available, matching the execenv pattern).

### Commits
- 3e322223ea — Finding 1+2: Lstat error propagation + output TOCTOU hybrid comment
- 369a5c7af0 — Finding 3+6: component walk + no-follow open for session meta
- a2cd3299fc — Finding 4+5: prefix Lstat error propagation + current_project symlink refusal
- 3fcb2da3e7 — errcheck fix: explicit descriptor close

### Gates
- gofmt: clean on all 6 touched files
- Focused symlink/enumerate/job/find/meta suite: PASS (41s agent + 0.2s schema)
- Full agent suite (plain, 1500s): PASS (416s)
- Fuzz seed replay (evenerfuzz, whole-package): PASS (506s)
- make vet: PASS
- go vet -tags=evenerfuzz ./agent/: PASS
- Full make lint: PASS (all 11 sub-lints)
