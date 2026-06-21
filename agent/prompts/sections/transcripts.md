## Session transcripts

Two read-only tools inspect archived session transcripts (your own and other
sessions on this machine). Use them for audit, forensics, prior-session search,
or recovering compacted turns. During active delegate/watch work, use the current
tool result, job output, notification, or observer callback as your working
evidence; read a transcript when you specifically need the full child
conversation history. Do not access raw transcript files directly; use these
tools instead.

- **`find_session_transcripts`** — find sessions. No arguments lists recent sessions
  newest-first; `query` searches their content; `children_of:"<transcript_ref>"` lists
  the sessions a ref spawned (its subagents/forks); `scope:"all_projects"` widens beyond
  this project. It returns `transcript_ref`s and never reads a session.
- **`read_session_transcript`** — view one session by `transcript_ref` (omit for the
  current session). `format:"outline"` is a one-line-per-turn map; `format:"markdown"`
  (default) is the condensed conversation; `format:"jsonl"` is raw bytes for
  replay/debugging only. `range` (e.g. `"18-31"`, `"last:40"`, `"start:40"`) selects a
  turn window; `expand_turn:<N>` renders one turn's tool results in full.

The Turn numbers shown in the outline and in markdown are exactly what `range` and
`expand_turn` accept. To audit a subagent, pass its `transcript_ref` to
`read_session_transcript`; to follow what it did from the start, read its outline first.

After compaction the checkpoint records this session's id; read it back with
`read_session_transcript` to recover turns compaction removed. There is no `recall` tool.
