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
| `memory_read` | `file_path` | `offset` (1-based start line, default 1), `limit` (line count, default 2000), `vision_prompt` (image/PDF extraction request) | Ordinary `ReadFile` presentation, including line-numbered text and existing image/PDF handling. An explicit missing read fails normally. |
| `memory_write` | `file_path`, `content` | None | Ordinary `WriteFile`: create the file and parent directories if needed, or replace the entire existing file. Content is accepted unchanged. |
| `memory_edit` | `file_path`, `old_string`, `new_string` | `replace_all` (default false) | Ordinary `EditFile`: replace a unique exact match, or deliberately replace every occurrence when `replace_all` is true. Read first and include enough context for a unique match. |
| `memory_search` | `pattern` (regex) | `path`, `glob_filter`, `case_insensitive`, `max_results` (default 100), `context_lines` (0–10, default 0), `output_mode` (`content`, `files_with_matches`, `count`, default `content`) | Ordinary `Grep`, with matching lines, filenames or per-file counts. `glob_filter` supports `*`, `?`, `[]`, `**` and bounded brace alternatives. Dotfiles/directories and gitignored paths are excluded. |
| `memory_delete` | `file_path` | None | Remove one file using the policy-checked `FileMutator.RemovePath`. Missing files are a no-op. Directories are not recursively deleted. Repair links separately. |

The underlying definitions are in
[`agent/internal/tool/definitions.go`](../../agent/internal/tool/definitions.go).
Dispatch in [`session_tools_memory.go`](../../agent/session_tools_memory.go)
adds scope/path authority and forwards to the existing shared executors.
Applicable read-before-write warnings are tracked independently per scope,
not inherited from a workspace read of a same-named file.

## Focused correction

Read the page, make one exact edit, then read it back:

```json
{"scope":"project","file_path":"lessons.txt","offset":1,"limit":40}
```

```json
{"scope":"project","file_path":"lessons.txt","old_string":"outdated fact","new_string":"verified correction"}
```

The first argument object is for `memory_read`; the second is for `memory_edit`.
Index and page changes require separate calls. After an uncertain write result,
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
Automatic index truncation instead points to `memory_read` of `MEMORY.md`.

Deleting or editing active files does not remove old transcripts or retained
artifacts. Forgetting requires searching and editing active copies and repairing
links separately; it is not forensic erasure.
