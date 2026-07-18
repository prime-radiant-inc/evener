# Transcript Recovery and Runtime Pair Build Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make transcript read failures honest in the Hub UI, safely convert selected recent v1 transcript artifacts to v2, and make ordinary builds publish a same-checkout `serf`/`serf-hub` pair.

**Architecture:** The renderer classifies existing AppWire-coded errors separately from plain transport failures. A standalone, uninstalled Go command performs strict per-file conversion and backup-preserving replacement. A shell behavior boundary stages both runtime binaries before Make publishes either compiled result.

**Tech Stack:** Go 1.25, POSIX shell, GNU Make, browser JavaScript, JSDOM.

## Global Constraints

- Do not add runtime transcript v1 compatibility.
- Dry-run is the migration default; `--root` is required and `--since` defaults to `120h`.
- Apply mode retains a sibling `.v1.bak` and never overwrites an existing backup.
- Reject malformed, unknown, oversized, or incomplete transcript records without mutating the original.
- Do not modify `.api.jsonl`, `.meta.json`, or transcripts older than the selected cutoff.
- Do not install, distribute, or release `serf-transcript-v2-upgrade`.
- Default tests are deterministic and follow `docs/testing.md`.
- Tests assert behavior and structured results, not large rendered scripts or command strings.

---

### Task 1: Honest transcript application errors in the Hub renderer

**Files:**
- Modify: `cmd/serf-hub/jstest/test-renderer-connection-banner.js`
- Modify: `cmd/serf-hub/assets/renderer.js`

**Interfaces:**
- Consumes: AppWire errors with an own `code` property, created by `cmd/serf-hub/assets/appwire.js:errorFromWire`.
- Produces: `SerfRenderer.isAppwireApplicationError(err) bool` and `showConnectionBanner(level, detail)` support for `level === "unavailable"`.

- [ ] **Step 1: Add the failing application-error behavior case**

Extend `test-renderer-connection-banner.js` after the existing transport cases:

```javascript
let applicationReadCalls = 0;
const applicationError = new Error("unsupported transcript format: require transcript header with format_version 2");
applicationError.code = -32000;
const w3 = createWindow({
  onConnectionLost: () => () => {},
  onConnectionRestored: () => () => {},
  readThread: () => {
    applicationReadCalls++;
    return Promise.reject(applicationError);
  },
});
await wait(600);
const b3 = banner(w3);
pass(!!b3 && b3.classList.contains("unavailable"), "application failure uses transcript-unavailable chrome");
pass(!!b3 && /transcript unavailable/i.test(b3.textContent), "application failure is labeled Transcript unavailable");
pass(!!b3 && b3.querySelector(".connection-banner-sub").textContent === applicationError.message, "application failure shows the exact server message");
pass(!/connection lost/i.test(b3.textContent), "application failure is not mislabeled as connection loss");
pass(applicationReadCalls === 1, "application failure is not retried");
pass(transcriptErrors(w3).length === 0, "application failure does not create a transcript turn");
```

- [ ] **Step 2: Run the focused test and verify the new case fails**

Run:

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-connection-banner.js
```

Expected: exit 1 because the rejection is rendered as `Connection lost` and retried.

- [ ] **Step 3: Implement the smallest classifier and banner branch**

Add a renderer predicate:

```javascript
isAppwireApplicationError(err) {
  return !!err && Object.prototype.hasOwnProperty.call(err, "code");
},
```

Change the `thread/read` catch branch:

```javascript
this.clearAppwireStream();
if (this.isAppwireApplicationError(err)) {
  this.showConnectionBanner("unavailable", err && err.message ? err.message : String(err));
  return;
}
this.showConnectionBanner("lost");
this.scheduleAppwireReconnect();
```

Extend `showConnectionBanner(level, detail)` so the unavailable branch uses
the existing red `lost` styling, fixed `Transcript unavailable` heading, and a
`.connection-banner-sub` whose `textContent` is assigned from `detail`. Do not
interpolate the server message into `innerHTML`.

- [ ] **Step 4: Run the focused and full renderer tests**

Run:

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-renderer-connection-banner.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh cmd/serf-hub/jstest/run-all.sh
```

Expected: both commands exit 0; the focused test reports application errors as unavailable while the existing amber-to-red transport behavior remains green.

- [ ] **Step 5: Commit the renderer correction**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-connection-banner.js
git commit -m "fix: surface transcript read application errors"
```

---

### Task 2: Strict recent-transcript v1-to-v2 upgrade command

**Files:**
- Create: `cmd/serf-transcript-v2-upgrade/main.go`
- Create: `cmd/serf-transcript-v2-upgrade/main_test.go`

**Interfaces:**
- Consumes: `transcript.ReadLine`, `transcript.DecodeHeader`, and `transcript.DecodeEntry` from `primeradiant.com/serf/agent/transcript`.
- Produces: `run(args []string, now time.Time, stdout, stderr io.Writer) int`, `upgradeRoot(options) summary`, `prepareTranscript(path) preparedTranscript`, and `replaceTranscript(preparedTranscript) error` inside the command package.

- [ ] **Step 1: Write failing filesystem behavior tests**

Create helpers that write complete JSONL fixtures under
`<temp>/project/sessions/<id>.transcript.jsonl`. Use a fixed `now` and set file
mtimes with `os.Chtimes`.

Cover these named tests with parsed-file assertions:

```go
func TestRunDryRunLeavesEligibleV1TranscriptUntouched(t *testing.T)
func TestRunApplyConvertsEntriesAndRetainsBackup(t *testing.T)
func TestRunSkipsCurrentAndOldTranscripts(t *testing.T)
func TestRunRejectsMalformedUnknownAndIncompleteRecords(t *testing.T)
func TestRunRejectsExistingBackup(t *testing.T)
func TestReplaceTranscriptRejectsChangedOriginal(t *testing.T)
```

The successful fixture must contain two entries separated by an `api_call`
record whose original sequences have gaps. Assert that the replacement:

```go
header, err := transcript.DecodeHeader(lines[0])
if err != nil {
    t.Fatalf("DecodeHeader: %v", err)
}
if header.FormatVersion != transcript.FormatVersion {
    t.Fatalf("format_version = %d, want %d", header.FormatVersion, transcript.FormatVersion)
}
first, _ := transcript.DecodeEntry(lines[1])
second, _ := transcript.DecodeEntry(lines[2])
if first.Seq != 0 || second.Seq != 1 {
    t.Fatalf("entry sequences = %d, %d; want 0, 1", first.Seq, second.Seq)
}
```

Also assert the `.v1.bak` bytes equal the entire original byte slice and that
no `.api.jsonl` fixture changes.

- [ ] **Step 2: Run the command tests and verify they fail to compile**

Run:

```bash
go test ./cmd/serf-transcript-v2-upgrade -count=1
```

Expected: FAIL because the command implementation does not exist.

- [ ] **Step 3: Implement CLI parsing and candidate discovery**

Implement:

```go
type options struct {
    root   string
    cutoff time.Time
    apply  bool
}

type summary struct {
    candidates, eligible, upgraded, skippedCurrent, skippedOld int
    removedAPICalls, errors int
}

func run(args []string, now time.Time, stdout, stderr io.Writer) int
```

Use `flag.ContinueOnError`. Require a non-empty `--root`, require a positive
`--since`, and reject positional arguments with exit 2. Walk only regular files
whose parent directory is `sessions` and whose name ends in
`.transcript.jsonl`. Sort paths before processing for deterministic output.

- [ ] **Step 4: Implement strict transformation and validation**

Use `transcript.ReadLine` for framing. A zero-byte EOF ends input; a nonzero
incomplete EOF is an error. Parse record boundaries as:

```go
var boundary struct {
    Kind          string `json:"kind"`
    FormatVersion int    `json:"format_version"`
}
```

For a v1 header, unmarshal into `map[string]json.RawMessage`, replace only the
`format_version` value, marshal it, and pass the result to
`transcript.DecodeHeader`. For every `entry`, call `transcript.DecodeEntry`, set
`Seq` to the next emitted ordinal, marshal it, and decode the result again.
For `api_call`, validate that the line is one JSON object and increment the
removed count without emitting it. Reject all other kinds.

- [ ] **Step 5: Implement backup-preserving replacement**

Write the full converted bytes to `os.CreateTemp(filepath.Dir(path),
".serf-transcript-v2-*")`, apply the original permission bits, call `Sync`, and
close it. Before rename, stat the original and require `os.SameFile`, identical
size, and identical modification time compared with the preparation snapshot.

If `<path>.v1.bak` exists, return an error. Otherwise rename original to backup,
rename temporary to original, and restore the backup if the second rename
fails. Leave the backup in place after success. Continue processing other
candidates, but return exit 1 if the final summary contains any errors.

- [ ] **Step 6: Run focused tests and inspect the command help**

Run:

```bash
go test ./cmd/serf-transcript-v2-upgrade -count=1
go run ./cmd/serf-transcript-v2-upgrade --help
```

Expected: tests pass; help shows required `--root`, default `--since 120h`, and
opt-in `--apply`.

- [ ] **Step 7: Commit the upgrade command**

```bash
git add cmd/serf-transcript-v2-upgrade/main.go cmd/serf-transcript-v2-upgrade/main_test.go
git commit -m "tools: add bounded transcript v2 upgrader"
```

---

### Task 3: Same-checkout runtime pair builds

**Files:**
- Create: `scripts/build-runtime-pair.sh`
- Create: `runtime_pair_build_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: Make's existing `LDFLAGS` value and `go` resolved through `PATH`.
- Produces: `scripts/build-runtime-pair.sh`, private phony `build-runtime`, and public aliases `build`, `build-hub`, and `build-all`.

- [ ] **Step 1: Write failing build behavior tests**

In `runtime_pair_build_test.go`, run the checked-in script with `cmd.Dir` set to
a temporary repository fixture and `PATH` beginning with a fake `go` script.
The fake compiler must parse `-o`, log its complete argument vector, optionally
fail for `./cmd/serf-hub/`, and otherwise write the package path into the output.

Add:

```go
func TestRuntimePairBuildPublishesBothWithSameLinkerFlags(t *testing.T)
func TestRuntimePairBuildFailureLeavesExistingPairUntouched(t *testing.T)
func TestMakeRuntimeAliasesBuildThePair(t *testing.T)
```

The Make test copies the Makefile and build script to the temporary fixture,
sets `LDFLAGS=test-flags`, and invokes both `make build` and `make build-hub` in
table subtests. Assert that each invocation produces both output files. Do not
assert Makefile or shell source text.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run:

```bash
go test . -run 'TestRuntimePairBuild|TestMakeRuntimeAliases' -count=1
```

Expected: FAIL because `scripts/build-runtime-pair.sh` and the shared target do
not exist.

- [ ] **Step 3: Add the staged pair build script**

Create an executable POSIX shell script with this behavior:

```sh
#!/bin/sh
set -eu

repo_root=$(pwd)
stage=$(mktemp -d "${TMPDIR:-/tmp}/serf-runtime-build.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM

build_one() {
  output=$1
  package=$2
  if [ -n "${LDFLAGS:-}" ]; then
    go build -ldflags "$LDFLAGS" -o "$stage/$output" "$package"
  else
    go build -o "$stage/$output" "$package"
  fi
}

build_one serf ./cmd/serf/
build_one serf-hub ./cmd/serf-hub/
mv "$stage/serf" "$repo_root/serf"
mv "$stage/serf-hub" "$repo_root/serf-hub"
```

Use `chmod +x` as a mechanical file-mode change.

- [ ] **Step 4: Route public Make targets through `build-runtime`**

Add `build-runtime` to `.PHONY`, replace the two one-binary recipes, and dedupe
`build-all`:

```make
build: build-runtime

build-runtime:
	LDFLAGS="$(LDFLAGS)" scripts/build-runtime-pair.sh

build-hub: build-runtime

build-all: build-runtime build-tui build-doctor
```

Leave cross-compilation, distribution, and installation targets otherwise
unchanged; the one-off migrator must not enter `SERF_INSTALL_BINS`.

- [ ] **Step 5: Run focused tests and a real pair build**

Run:

```bash
go test . -run 'TestRuntimePairBuild|TestMakeRuntimeAliases' -count=1
make build
go version -m ./serf
go version -m ./serf-hub
```

Expected: behavior tests pass; both binaries build; their embedded VCS revision
and modified state match.

- [ ] **Step 6: Commit the paired build infrastructure**

```bash
git add Makefile scripts/build-runtime-pair.sh runtime_pair_build_test.go
git commit -m "build: rebuild serf runtime binaries as a pair"
```

---

### Task 4: Deterministic gates and live recovery

**Files:**
- No source files expected.
- Runtime artifacts: selected files below `~/.local/state/serf/projects` and
  their retained `.v1.bak` siblings.

**Interfaces:**
- Consumes: completed renderer correction, upgrade command, and runtime pair build.
- Produces: a migrated recent corpus, matching live binaries, and a verified original Web UI session.

- [ ] **Step 1: Run deterministic verification**

Run:

```bash
go test ./cmd/serf-transcript-v2-upgrade -count=1
go test . -run 'TestRuntimePairBuild|TestMakeRuntimeAliases' -count=1
go test ./cmd/serf-hub -count=1
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh cmd/serf-hub/jstest/run-all.sh
make test
```

Expected: all gates exit 0 without provider credentials or network access.

- [ ] **Step 2: Stop transcript writers and dry-run the migration**

Stop `com.primeradiant.serf-hub.codex` and confirm no managed `serf` writer
processes remain. Then run:

```bash
go run ./cmd/serf-transcript-v2-upgrade --root "$HOME/.local/state/serf/projects" --since 120h
```

Expected: exit 0 with eligible/skipped/removal counts and no unexplained errors.

- [ ] **Step 3: Apply and validate the selected migration**

Run the same command with `--apply`. Independently enumerate every modified
transcript and require its `.v1.bak` sibling. Re-run dry-run; the just-upgraded
files must classify as current v2.

- [ ] **Step 4: Build and restart the runtime pair**

Run `make build`, compare `go version -m` VCS metadata, and restart the Hub
launchd job. Confirm the service listens on its expected local ports.

- [ ] **Step 5: Verify the original session through AppWire and the browser**

Using the existing local auth token without printing it, request the original
session through `thread/read` and `thread/turns/list`. Require non-empty turns
and no unsupported-format error. Load the original shared URL and require the
transcript to render rather than `No messages yet` or `Transcript unavailable`.

- [ ] **Step 6: Record final repository and runtime state**

Run:

```bash
git status --short --branch
git log --oneline --decorate -5
```

Report source commits, deterministic gate results, migrated/skipped/error
counts, backup retention, binary revisions, service state, and the live Web UI
result separately.
