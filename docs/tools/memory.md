# Memory tools

The native tools operate on one bound personal or project memory scope. They
reuse ordinary file/search operations, argument validation, warnings, errors
and output recovery. See [memory ownership and lifetime](../product/memory.md).

## Scope and paths

Every call requires `scope`, exactly `personal` or `project`. Scope selects the
trusted session binding, not a root supplied by the model. Read/write/edit/delete
require `file_path`; search takes optional `path`, with blank meaning the scope
root. Paths are relative to that scope and may include subdirectories. Absolute
paths, escapes and symlink indirection are rejected by shared confinement.
There is no `root` or `project_id` argument.

Disabled or unbound sessions expose none of these tools. An unavailable project
binding does not authorize another project's storage. Stored text cannot grant
scope or filesystem access. Ordinary tool-call `intent` metadata, where
advertised, has the same purpose as for other tools.

## Arguments and effects

All rows below also require `scope`. Unknown arguments are rejected.

| Tool | Required operation arguments | Optional operation arguments | Effect |
| --- | --- | --- | --- |
| `memory_read` | `file_path` | `offset` (1-based start line, default 1), `limit` (line count, default 2000), `vision_prompt` (image/PDF extraction request) | Ordinary `ReadFile` presentation, including line-numbered text and existing image/PDF handling. An explicit missing read fails normally. A text page other than `MEMORY.md` longer than 4096 bytes ends with a note giving its size in KB (rounded up), pointing at the `gardening-memory` skill when the session can load it, otherwise saying a page should hold one fact. `MEMORY.md` at the scope root is not a file: the read renders the generated index in full, with no size cap, paged by `offset`/`limit`, or says the scope has no pages yet. |
| `memory_write` | `file_path`, `content` | None | Ordinary `WriteFile`: create the file and parent directories if needed, or replace the entire existing file. Content is accepted unchanged. `MEMORY.md` at the scope root (any case) is refused. |
| `memory_edit` | `file_path`, `old_string`, `new_string` | `replace_all` (default false) | Ordinary `EditFile`: replace a unique exact match, or deliberately replace every occurrence when `replace_all` is true. Read first and include enough context for a unique match. `MEMORY.md` at the scope root (any case) is refused. |
| `memory_search` | `pattern` (regex) | `path`, `glob_filter`, `case_insensitive`, `max_results` (default 100), `context_lines` (0–10, default 0), `output_mode` (`content`, `files_with_matches`, `count`, default `content`) | Ordinary `Grep`, with matching lines, filenames or per-file counts. `glob_filter` supports `*`, `?`, `[]`, `**` and bounded brace alternatives. Dotfiles/directories and gitignored paths are excluded. It never searches a root `MEMORY.md` (any case): the index is generated, and a hand-written one not yet migrated is skipped. |
| `memory_delete` | `file_path` | None | Remove one regular file through shared captured-parent confinement, without reading its body or requiring file read permission. Missing files or parents are a no-op. Directories, symlinks and special files are refused. Parent permissions and other removal errors still apply. `MEMORY.md` at the scope root (any case) is refused. The page's index line disappears with it; repair links from other pages separately. |

After a successful `memory_write` or `memory_edit` of a Markdown page, Evener
sets its `updated` and `by` frontmatter, keeping every other byte, and the
result notes a missing description or frontmatter that does not parse. A page
whose stamps would not change is not rewritten, and frontmatter an in-place
edit would break (a flow mapping, a block ended by `...`) is left unstamped.

The underlying definitions are in
[`agent/internal/tool/definitions.go`](../../agent/internal/tool/definitions.go).
Dispatch in [`session_tools_memory.go`](../../agent/session_tools_memory.go)
adds scope/path authority and forwards to the existing shared executors.
Applicable read-before-write warnings are tracked independently per scope,
not inherited from a workspace read of a same-named file.

Deletion checks the leaf's type beneath the authorized parent, then unlinks it
without a directory-removal fallback. A leaf replaced after admission can still
lose its replacement non-directory entry, but cannot redirect through a symlink
or remove a directory. There is no atomic file-identity guarantee.

Successful deletion reports `Removed or already absent: <path>`, with any
applicable read-before-write warning. It does not distinguish an actual unlink
from a missing file or parent, and does not count deleted bytes.

## Focused correction

Read the page, make one exact edit, then read it back:

```json
{"scope":"project","file_path":"lessons.txt","offset":1,"limit":40}
```

```json
{"scope":"project","file_path":"lessons.txt","old_string":"outdated fact","new_string":"verified correction"}
```

The first argument object is for `memory_read`; the second is for `memory_edit`.
The index follows the pages; there is no index edit. After an uncertain write result,
reread before retrying. Whole-file writes can overwrite concurrent changes;
there is no revision protocol, batch or atomic multi-file operation.

## Large results and recovery

Memory uses ordinary tool result events and the existing generic CLI/TUI,
browser and native evidence renderers. A large read/search can be truncated for
delivery while its full output is retained. The result includes an opaque
`artifact:<id>` reference and the existing `read_transcript` recovery route:

```json
{"transcript_ref":"artifact:<id>"}
```

Follow returned `continuation.offset_bytes` with the same `transcript_ref` for
later raw pages. Existing `output_match` and `context_lines` can search retained
output. See [transcript tools](transcripts.md) for retention and unavailable
artifact behavior. Artifact IDs are session-tree capabilities, not memory paths
or permanent links. There is no memory-specific cursor or result envelope.
An automatic index projection that does not fit its budget ends with a line
counting the pages it leaves out; `memory_read` of `MEMORY.md` returns the
whole index.

Deleting or editing active files does not remove old transcripts or retained
artifacts. Forgetting requires searching and editing active copies and repairing
links separately; it is not forensic erasure.
