# Issue #4971: shell diff digest cuts at hunk boundaries

## Scope and authority

The [issue](https://github.com/prime-radiant-inc/evener/issues/4971) authorizes
this change to the model-facing inline window for a completed shell command's
output. A large `git diff` currently shows a head and tail that each end inside
a hunk, so the model reads a partial change with no statement of which bytes or
hunk were omitted. This spec fixes how that window chooses its cuts. It does not
change `git` output, the output caps, the durable job record, or
`read_transcript`'s contract.

The isolated lane includes `origin/main`
`69c67e8580e69f18fd3970aa6b0d6847ca2c221b` before this specification is written.

## Cause and detection

A completed shell command whose output exceeds `shellRideWholeBytes` (8 KiB)
returns a compact digest instead of the whole output (`shellInlineDigest` in
`agent/job_output_digest.go`, reached through `marshalCompleteOrHandleResult` in
`agent/session_tools_shell.go`). The digest keeps up to 512 bytes from each end,
snapped to line boundaries, around an elision marker, and then applies a
200-line cap to each end.

Line boundaries are not hunk boundaries. A synthetic 9,735-byte unified diff
with 30 eight-line hunks produced a 503-byte head that ended with
`-old line 001` and whose next source line was `+new line 002`; the tail began
with ` context line 003` in a different hunk. The marker said only
`8.5 KB elided`. The model therefore reads two partial hunks and cannot tell
which file, hunk, or byte range was omitted. This is the reported defect, and
it is reachable in every production shell session: `LocalExecutionEnvironment`
implements `StreamingExecutor` (`agent/execenv/local.go:3060`), which is the
only non-test `StreamCommand` implementation, and `runShell` passes the whole
retained output plus `RetainedStart()` into this digest.

The generic registry truncator (`truncateCharsWithStatus`) is not the cause on
this path: the digest is roughly 1 KiB against a 30,000-character shell limit.
It can still cut a buffered test environment's shell result, but no production
executor is non-streaming, so this change leaves that generic path unchanged.

## Chosen change

`shellInlineDigest` detects a git-style unified diff structurally and, only for
a diff, chooses both windows at hunk or file boundaries. Non-diff output keeps
the existing head-and-tail digest and its 200-line cap byte for byte.

### Diff detection and line states

Scan the output once at line boundaries. A complete output is recognized as a
git-style diff only when its first line is a `diff --git ` file start and a
valid unified hunk header follows it. Anchoring at the first line keeps ordinary
output that merely quotes a diff (a build log, a test failure, or `git log -p`'s
commit preamble) on the existing head-and-tail digest. When retention evicted
the prefix (`dropped > 0`, so the visible start may be inside the diff), the
first line must look like diff content (a hunk header, a body line beginning
` `, `+`, `-`, or `\`, or a git metadata line such as `index `, `old mode `,
`similarity index `, a rename/copy line, or a binary marker) and a valid hunk
header must begin within the 512-byte head budget. The near-hunk rule keeps a
stray hunk-shaped line deep in ordinary output from reclassifying it. A valid
hunk header starts `@@ -` and contains ` +` and ` @@`. A bare `@@ ` line, a
`--- `/`+++ ` pair with no `diff --git `, and markers in the wrong order do not
classify the output as a diff. Plain `diff -u` output is not recognized because
a deleted line whose content starts `-- ` is indistinguishable from a `--- `
file header without `diff --git `; it keeps the existing digest.

When the retention cap cut the stream mid-line (`RetainedStartPartial`), the
first visible line is a fragment, not a line. It is excluded from structural
classification: it can neither establish a file-header section nor be a
boundary, and the window starts from the first complete line. This keeps a
fragment such as `index ...` (what remains when the evicted prefix held the `+`
of a `+index ...` body line) from turning following `--- `/`+++ ` body lines
into a filename.

Within a `diff --git ` section, the scanner tracks whether a hunk is open:

- `diff --git ` closes any hunk and opens a file section;
- a valid `@@ ` header opens a hunk;
- `--- ` and `+++ ` lines are file-header path lines only when a file-header
  section is established, by a `diff --git ` line or a git metadata line
  (`index `, modes, rename/copy, binary markers);
- `--- ` and `+++ ` lines inside an open hunk are body lines. A deleted line
  beginning `-- ` renders as `--- ` and must never become a cut boundary.

A retained prefix that starts inside a hunk has no file-header evidence, so its
path-like body lines (for example a deleted `-- old SQL comment` line rendered
as `--- old SQL comment`) stay body and the marker leaves the filename unknown
rather than reporting the comment as a file.

Boundaries are `diff --git ` file starts and valid `@@ ` hunk headers only.
`--- ` and `+++ ` are path sources, never boundaries.

### Head and tail selection

The head starts at the start of the retained output unless retention evicted a
prefix and that start is inside a hunk; then it starts at the first following
hunk or file boundary, so no partial hunk body is shown at the head's start. It
ends at the largest boundary at or before 512 bytes from its start. With several
small hunks the head holds complete file headers and complete hunks and cuts
immediately before an omitted hunk header or file start. With one hunk larger
than the 512-byte head budget, the head is the file header only and cuts
immediately before that hunk's `@@ ` line. If even the leading file header
exceeds the budget, the head is cut at the last line boundary within the budget
and still contains no hunk body. When the retained start is itself a `@@ `
line whose hunk exceeds the head budget (a prefix evicted the file header), the
head is empty and the marker names the hunk.

The tail starts at the first boundary at or after `len(full)-512` when one
fits, so it begins with a complete file header or hunk and runs to the natural
end of the output. When no boundary fits -- the last 512 bytes lie inside one
huge hunk, or the output ends with non-diff lines after a diff -- the tail
falls back to the existing line-aligned peek: the first line start at or after
`len(full)-512`. That keeps the last view the model had before this change
instead of dropping it, and the result still ends at the natural end of the
output. When no line start fits at all -- an unterminated final line longer
than the budget -- the peek is a rune-safe byte suffix of the last 512 bytes,
matching the old digest's behavior for that case. The head and tail never
overlap.

When the head cut falls between boundaries (for example a leading binary or
mode-only section larger than the head budget), the omitted range begins inside
the section that started at the last file boundary at or before the cut. The
marker names that containing file and does not borrow the next file's header or
name a hunk from the next file.

Both windows keep `shellDigestHalfBytes` (512 bytes) as their bound. The digest
does not apply the existing 200-line cap (`shellDigestLines`) to either diff
window: a hunk of 2-byte blank-line insertions can exceed 200 lines well inside
512 bytes, and that cap would reintroduce a mid-hunk cut. The byte budget alone
bounds the window. Head, optional tail, and marker stay below the existing
`FuzzArShellInlineDigest` bound of `2*shellDigestHalfBytes + digestMarkerBudget`.

The registry applies its own line limit to the final rendered shell result
(default 512 lines). A 512-byte tail of one-byte lines plus a many-line head can
exceed that, and the registry's line truncator would then cut the selected hunks
again. The digest counts its head lines, marker, tail lines, and the shell
footer; when that total exceeds the registry's line bound, it drops the optional
tail and advertises the whole remainder as omitted. The head alone is small
enough at the default 512-byte budget. The advertised range therefore describes
the final model-facing output, not an intermediate one.

### Omitted-range statement

The rendered marker keeps the existing `elided` + `read_transcript` wording
family and adds structural facts, not a rewritten diff:

- an elided byte count equal to the stated range length, in the same
  human-readable form as the existing digest, and, when nonzero, the bytes
  permanently dropped past the retention cap;
- the omitted lifetime byte range `[start,end)` where `start` is
  `prefix + HeadEnd` and `end` is `prefix + TailStart` (`prefix` is
  `total - len(full)`, equal to `dropped` in production), clamped to
  nonnegative values;
- the file and hunk that begin the omitted range when the section provides an
  unambiguous `+++ `/`--- `/`@@ ` line: rune-safe, newline-free, and capped at
  96 bytes each. Control bytes and invalid UTF-8 bytes become visible escapes
  before the cap, so the marker stays single-line valid UTF-8. A hunk at or
  after the tail start is visible in the tail and is not named as omitted. A
  hunkless section (mode-only, binary, or rename-only) has no path line, so its
  file is left unknown rather than guessed from its `diff --git ` header; the
  exact omitted range still targets the read;
- `head starts at byte N` when retention forced a non-prefix head;
- a `read_transcript` continuation hint naming `offset_bytes=start` and the
  returned job `transcript_ref`.

The marker is the only new prose. Tests assert the boundary offsets, the
range through the structured window fields, and the source-derived file and
hunk values. The complete rendered digest prose is pinned by whole-output
goldens under `agent/testdata/diffdigest/`, not by fragment assertions or
regexes over marker wording.

### Residual limitation

If the retention cap has evicted every `diff --git ` and `@@ ` line (for
example, one hunk larger than the retained window), nothing in the visible
bytes identifies a diff. The digest then falls back to the existing line-based
head and tail, and the elision marker still states how many bytes are missing.
This case is documented rather than guessed at.

## Non-goals

- No change to how git produces or formats a diff.
- No change to `read_transcript`, its `offset_bytes` semantics, or its
  contract.
- No change to `shellRideWholeBytes`, `shellDigestHalfBytes`, the shell tool's
  30,000-character registry limit, or job output retention.
- No change to non-diff digests or to the generic registry truncator.
- No colorized-diff handling. The shell tool spawns no TTY, so git does not
  color output unless a caller explicitly forces `color.ui=always`; stripping
  ANSI would have to rewrite bytes this change deliberately leaves alone.
- No combined-diff handling (`diff --cc`, `@@@ `). Those are not unified diffs
  and have a different body grammar.
- No filename inference from a `diff --git ` header for hunkless, binary or
  mode-only sections; the marker reports their byte range and leaves the file
  unknown.
- No commit-preamble handling. `git show` and `git log -p` begin with a commit
  header rather than `diff --git `, so they keep the existing digest; the
  issue names `git diff`, whose output is anchored at its first line.
- No new structured field on the tool result; the marker carries the range.
- No full local test suite. CI remains the full-suite source of truth.
- No labels. Unrelated defects become separate GitHub issues.

## Verification

1. Before the production change, add deterministic tests over a synthetic
   unified diff:
   - an oversized file with many hunks: both the head end and the tail start
     are hunk or file boundaries, the head contains at least one complete hunk,
     and the digest does not contain body lines from the omitted middle;
   - a single huge hunk: the head is the file header only and ends immediately
     before that hunk's `@@ ` line; no tail boundary fits, so the trailing peek
     starts at a line boundary inside the hunk, and the marker names the hunk
     and range;
   - a hunk whose deleted lines render as `--- ` and whose added lines render
     as `+++ ` near the head budget: the cut is still a real hunk boundary;
   - a single huge hunk of 2-byte lines: the 200-line cap does not cut it;
   - ordinary output containing a bare `@@ ` line and an unordered `--- `/`+++ `
     pair keeps its head-and-tail digest;
   - a diff followed by ordinary log output keeps the last log line in a
     line-aligned trailing peek;
   - ordinary output that quotes a diff after its first line keeps the existing
     head-and-tail digest;
   - a cut at a hunkless (mode-only) file section reports the omitted range and
     leaves the file unknown;
   - an evicted-prefix output that starts mid-hunk and keeps a later hunk: the
     head starts at that later boundary and the stated lifetime range is
     correct.
   - an evicted-prefix output that starts on a git metadata line (`index `)
     with a hunk header inside the head budget: it is recognized, while one
     whose only hunk header is beyond the head budget keeps the old digest.
   - a retained prefix that starts with path-like hunk body lines (`--- old
     comment`, `+++ new comment`) followed by surviving hunks: the marker does
     not report either comment as the filename.
   - a retained prefix whose first visible line is a fragment of a `+index ...`
     body line (`RetainedStartPartial`) followed by path-like body lines: the
     fragment is excluded and the filename stays unknown, while a fragment of a
     real `diff --git ` header still lets the retained `index `/`--- `/`+++ `
     lines name the file.
   - a leading hunkless section (binary patch) larger than the head budget
     followed by a text file: the cut reports the range and names neither the
     binary file nor the following text file.
   - hunkless sections whose `diff --git ` headers contain a quoted destination
     containing ` b/`, quoted paths with spaces, an unquoted path with a space,
     and a quoted path with `\n` and `\377`: each cut reports the range and no
     guessed filename.
   - a rendered digest whose parsed path contains a literal newline and an
     invalid byte: the marker is single-line, valid UTF-8, and shows visible
     `\n` and `\377` escapes.
   - mixed diff/log output ending in an unterminated line longer than 512
     bytes: the inline digest keeps a rune-safe byte suffix containing the
     final line.
   Each boundary assertion reads the source bytes at the chosen offset; the
   window's `HeadStart`, `HeadEnd`, and `TailStart` and the source-derived file
   and hunk values are compared directly, while rendered prose is pinned by the
   goldens in item 3. Record the red run.
2. Add diff seeds to `FuzzArShellInlineDigest` and run its seed corpus under
   the `evenerfuzz` tag.
3. Add one session-level test through the real shell tool: write a synthetic
   multi-hunk diff to a temporary file, run `cat` on it through a real
   `LocalExecutionEnvironment`, and assert the returned `shellToolResult`
   carries a `job_id`, `truncated=true`, boundary-aligned head and tail, and
   the omitted range. The full output remains readable through the existing
   job transcript path.
   Add a second session-level case whose diff ends in a 512-line run of blank
   log lines: the rendered model-facing `ExecResult.Output` must equal the
   tool's own `formatShellResult` (the registry did not truncate it), stay
   within the registry's 512-line bound, and show the tail as dropped with the
   range ending at the output total. Inspect `ExecResult.Output`, not only the
   structured state.
   Pin the complete rendered digests for the standard, hunkless, and
   retained-prefix markers with `agenttest.CheckGolden`; do not parse marker
   prose in assertions.
4. Run the focused tests before and after the fix:
   `go test ./agent -run 'ShellInlineDigest|DiffDigest|CompleteOrHandle|ShellRideWhole|LargeDiff' -count=1`,
   `go test -tags evenerfuzz ./agent -run 'FuzzArShellInlineDigest' -count=1`,
   then `go test ./agent/execenv/... -count=1`, then the shell tool package's
   tests. Run `make build`. Do not run `go test ./...` or `make test`.
5. Freeze this spec for two independent competing PAR reviewers before code.
   Resolve legitimate findings and re-review affected requirements. Repeat PAR
   over the frozen implementation diff, then run simplify-code.
6. Commit intended paths and run branch RoboRev. Before the final push, fetch
   `origin/main` again and merge if it moved, renewing affected checks and
   reviews. Open a main PR closing #4971 and shepherd its current-head CI and
   RoboRev. Do not merge; the parent owns the merge.
