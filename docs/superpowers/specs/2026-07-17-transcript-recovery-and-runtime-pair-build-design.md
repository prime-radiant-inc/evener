# Transcript Recovery and Runtime Pair Build Design

Date: 2026-07-17
Status: Approved for implementation planning

## Purpose

Restore recent Serf sessions that became unreadable after the transcript v2
cutover, make transcript read failures visible in the Hub Web UI, and prevent
the `serf` and `serf-hub` runtime binaries from being rebuilt from different
revisions.

The three changes address one incident but remain separate units:

1. the renderer distinguishes server application errors from transport loss;
2. an operator-only command converts selected v1 transcript artifacts to v2;
3. one build target stages both runtime binaries from the same checkout.

This design does not add v1 compatibility to the running product.

## Evidence and Root Cause

The affected session has a valid, non-empty v1 transcript: one v1 header, 22
semantic entries, and 9 interleaved `api_call` records. Authenticated AppWire
`thread/read` and `thread/turns/list` requests both return:

```text
unsupported transcript format: require transcript header with format_version 2
```

The local transcript corpus contains 1,047 v1 transcripts. The v2 flag-day
cutover intentionally made current readers reject v1 instead of providing a
compatibility path.

The Hub renderer currently handles every rejected `thread/read` promise as a
transport failure. It clears the stream, displays `Connection lost`, and starts
the reconnect loop even when the WebSocket is healthy and the server has
returned a deterministic transcript-format error. The server-rendered empty
snapshot therefore remains visible as `No messages yet`, while the useful
error is swallowed by connection-recovery behavior.

The running binaries were also built from different revisions. `serf-hub` was
built from revision `00803b744` on July 17, while the `serf` binary it launches
was built from revision `5973b9eb3` on July 15. The transcript v2 cutover landed
between those revisions. The Makefile permits this state because `make build`
and `make build-hub` rebuild only one runtime binary each.

## Goals

- Display the exact AppWire transcript read error in the workspace chrome.
- Preserve existing reconnect behavior for real transport failures.
- Upgrade recent v1 transcripts without changing current v2 reader semantics.
- Keep an untouched v1 backup of every upgraded transcript.
- Refuse to mutate malformed, partial, already-upgraded, or concurrently
  changing transcripts.
- Make either ordinary runtime build target rebuild both `serf` and `serf-hub`
  from one checkout with the same version metadata.
- Avoid publishing either newly compiled runtime binary when either build
  command fails.

## Non-goals

- Read v1 transcripts at runtime or add a compatibility mode.
- Upgrade the entire historical corpus automatically.
- Rewrite `.api.jsonl`, `.meta.json`, or other session artifacts.
- Reconstruct v2 provider provenance from v1 `api_call` records.
- Install or release the one-off upgrade command.
- Make publication of two filesystem paths transactionally atomic against
  external interruption after compilation succeeds.
- Refactor AppWire, transcript projection, or the general Hub connection model.

## Chosen Design

### 1. Surface AppWire application errors in the renderer

`appwire.js` marks errors returned by AppWire with their wire error code.
Transport errors are plain errors without that code. The renderer will use
that existing distinction when the initial or recovery `thread/read` fails.

For an AppWire application error, the renderer will:

- clear the pending stream subscription;
- stop the reconnect loop for that read;
- show a red workspace-chrome banner headed `Transcript unavailable`;
- include the server-provided error message verbatim as text; and
- leave the transcript area free of synthetic error turns.

For a plain transport error, the renderer will retain the current behavior:
show `Connection lost`, clear the failed stream, and retry according to the
existing reconnect policy.

The classifier will be a small named helper or predicate near the connection
handling code. It will test for the presence of the AppWire error code rather
than matching message text, so new server-side application errors receive the
same honest treatment without becoming format-specific UI code.

### 2. Convert recent v1 transcripts with an operator-only command

Add `cmd/serf-transcript-v2-upgrade`. The command is committed and tested so the
recovery is reproducible, but it is not added to install, distribution, release,
or aggregate product-build binary lists.

The interface is:

```text
serf-transcript-v2-upgrade --root <projects-root> --since 120h
serf-transcript-v2-upgrade --root <projects-root> --since 120h --apply
```

Dry-run is the default. `--root` is required so the command cannot silently
select an ambient state directory. `--since` selects files by modification time
relative to command start and defaults to `120h`, representing five rolling
days. Discovery is limited to `*/sessions/*.transcript.jsonl` below the root.

Each candidate is classified independently:

- v2 header: skip as already current;
- v1 header outside the cutoff: skip as too old;
- v1 header inside the cutoff: validate and prepare conversion;
- missing, unsupported, or malformed header: report an error;
- existing sibling `.v1.bak`: report an error rather than overwrite history.

The converter reads complete newline-terminated JSONL records without the
default `bufio.Scanner` record-size ceiling. It performs this transformation:

1. preserve the complete header object and change only `format_version` from 1
   to 2;
2. strictly decode each `entry` against the current transcript entry schema;
3. emit entries in their original order with contiguous sequence values
   starting at zero;
4. omit `api_call` records from the transcript; and
5. reject every unknown record type, invalid JSON object, or incomplete final
   record.

The command never modifies `.api.jsonl`; omitted `api_call` records remain
available in the existing API log when one is present. It does not fabricate
new v2 provenance fields.

Before replacement, the command renders the complete v2 result to a sibling
temporary file, flushes it, and validates every emitted record with the current
v2 transcript decoders. It preserves the original transcript's permission
mode. Immediately before replacement, it verifies that the original file's
size and modification time still match the values observed during conversion.
A changed candidate is reported as an error and left untouched.

In apply mode, replacement proceeds per file:

1. rename the original transcript to `<name>.v1.bak`;
2. rename the validated temporary v2 file to the original path; and
3. if step 2 fails, restore the original from the backup and report the error.

The backup remains after success. A failure in one candidate does not prevent
safe candidates from being processed, but any error makes the command exit
non-zero after printing the complete summary. The summary reports candidates,
eligible v1 files, upgraded files, skipped v2 files, skipped old files, removed
`api_call` records, and errors.

All transcript writers must be stopped before apply mode. The size and
modification-time check catches ordinary accidental changes, but it does not
replace that operational precondition.

### 3. Build the runtime pair through one staging target

Add a small `scripts/build-runtime-pair.sh` behavior boundary and a private
`build-runtime` Make target. The script builds both commands into a temporary
staging directory:

```text
current checkout
    |
    +-- cmd/serf      --+
    +-- cmd/serf-hub --+--> staged pair --> repository-root binaries
```

Both builds receive the same `LDFLAGS`, so embedded version, revision, and build
date describe the same checkout. Neither repository-root binary is replaced
until both `go build` commands have succeeded. A compile failure removes the
staging directory and leaves both existing binaries unchanged.

Make targets become aliases over the shared dependency:

- `make build` depends on `build-runtime`;
- `make build-hub` depends on `build-runtime`; and
- `make build-all` depends once on `build-runtime`, then builds the TUI and
  doctor commands.

When multiple aliases appear in one Make invocation, Make's dependency graph
executes the shared target once. Existing release and install flows continue to
build their additional binaries, but cannot produce the ordinary Hub/runtime
pair through separate revisions.

## Error Handling

- AppWire application failures are terminal for the current transcript read and
  remain visible until a later explicit load succeeds.
- Transport failures keep the existing bounded reconnect behavior.
- Migration validation errors identify the transcript path and record number;
  they never partially rewrite that transcript.
- A missing terminal newline is treated as an incomplete record, not silently
  discarded.
- Backup collisions are errors; the command never replaces an earlier backup.
- Migration reports all candidate failures and exits non-zero when any occur.
- A failed runtime compilation publishes neither staged binary.
- Temporary files and staging directories are removed on normal error paths.

## Testing

Before changing tests, implementation follows `docs/testing.md`: default tests
remain deterministic and exercise real behavior below external boundaries.

### Renderer

Extend the JSDOM connection tests with an AppWire error carrying a wire code.
Assert that the workspace displays `Transcript unavailable` and the exact
server message, does not display `Connection lost`, does not insert a
transcript turn, and does not schedule a reconnect for that deterministic
failure. Keep the existing plain-error test as the transport regression case.

### Upgrade command

Use temporary project trees and real transcript files. Cover:

- dry-run performs no writes;
- a valid v1 transcript becomes a decoder-valid v2 transcript;
- interleaved `api_call` records are removed and entries are renumbered in
  stable order;
- the original bytes remain in `.v1.bak` after apply;
- v2 and out-of-window v1 transcripts are skipped;
- malformed JSON, unknown records, incomplete final records, concurrent file
  changes, and backup collisions leave the original untouched and return an
  error; and
- the summary counts files and removed records accurately.

Tests assert parsed records, resulting files, and exit behavior rather than
matching a generated script or large output string.

### Runtime pair build

Run the build script in a temporary repository-shaped fixture with a fake `go`
executable at the external compiler boundary. Verify:

- success produces both staged outputs and publishes both;
- failure while building either command leaves two pre-existing outputs
  unchanged; and
- the same linker flags reach both compiler invocations.

Keep only a minimal Make smoke assertion that `make build` and `make build-hub`
reach the shared behavior. Do not regex-match the rendered shell script or
Makefile contents.

### Verification

Run the focused Go and JSDOM tests, the complete relevant package tests, the
full JSDOM suite, and the repository's normal deterministic test gate. Build
the pair and inspect both binaries with `go version -m` to confirm matching
revision and modified state.

## Recovery Runbook

After the implementation and deterministic tests pass:

1. stop the Hub launchd job and all Serf transcript writers;
2. run the upgrade command in dry-run mode over
   `~/.local/state/serf/projects` with `--since 120h`;
3. inspect the eligible, skipped, removed-record, and error counts;
4. run the same command with `--apply` only if dry-run has no unexplained
   errors;
5. build the runtime pair through the shared Make target;
6. confirm matching embedded revisions in `serf` and `serf-hub`;
7. restart the Hub service; and
8. verify the original session through authenticated AppWire reads and the Web
   UI transcript rendering path.

The `.v1.bak` files are retained after verification. Cleanup is a separate,
explicit operational decision.

## Success Criteria

- The original session displays its transcript in the Hub Web UI.
- A deliberately unsupported transcript shows its exact AppWire error instead
  of `No messages yet` or a false connection-loss state.
- Every applied migration has a byte-for-byte v1 backup and a current-decoder
  valid v2 replacement.
- No file outside the five-day selection is changed.
- `make build` and `make build-hub` each produce a same-revision runtime pair.
- A forced compiler failure leaves the prior runtime pair intact.
- No runtime v1 compatibility path is introduced.
